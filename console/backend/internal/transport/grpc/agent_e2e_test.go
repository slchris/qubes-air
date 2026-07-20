package grpc

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/agent"
)

// TestAgentEndToEnd drives the whole remote path: a relay dials the agent over
// mTLS, sends a qrexec call through the tunnel, and the agent executes the real
// service script and returns its output.
//
// This is the chain that could not work before — the remote had no way to run a
// qrexec service, because qrexec-client-vm cannot be installed on a non-Qubes
// host (see docs/remote-agent-design.md). It exercises the shipped
// qubesair.Ping rather than a stand-in, so the contract CheckReachable depends
// on is the one under test.
func TestAgentEndToEnd(t *testing.T) {
	// Stage the real service script into a temp service directory.
	src, err := os.ReadFile("../../../../../remote/qubes-rpc/qubesair.Ping")
	if err != nil {
		t.Skipf("shipped Ping script not found: %v", err)
	}
	svcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(svcDir, "qubesair.Ping"), src, 0o700); err != nil {
		t.Fatalf("stage service: %v", err)
	}

	ca, caKey := mkCA(t)

	// The agent: the remote side, executing local services.
	inv := agent.NewLocalInvoker("remote-dev", []string{"qubesair.Ping"})
	inv.ServiceDir = svcDir
	inv.Timeout = 10 * time.Second

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close() // hand the port to the server

	srv := NewServer(ServerConfig{
		Listen: addr,
		TLS:    mkServerTLS(t, ca, caKey),
	}, inv)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ctx) }()

	// The relay: the local side, dialing outbound.
	cli := NewClient(ClientConfig{
		RemoteEndpoint: addr,
		RelayName:      "sys-relay-pve",
		RemoteName:     "remote-dev",
		TLS:            mkClientTLS(t, ca, caKey),
	}, nil)

	waitDial(t, addr)
	// Start runs the reconnect loop and blocks; it stops with the context.
	go func() { _ = cli.Start(ctx) }()

	// Call reports ErrNotConnected until the tunnel is established, so retry
	// briefly rather than racing the handshake.
	resp, err := callWhenReady(t, cli, "remote-dev", "qubesair.Ping", nil)
	if err != nil {
		t.Fatalf("Ping through the tunnel: %v", err)
	}

	fields := strings.Fields(string(resp))
	if len(fields) != 3 || fields[0] != "pong" {
		t.Fatalf(`want "pong <name> <ts>", got %q`, resp)
	}
	if fields[1] != "remote-dev" {
		t.Errorf("the agent must report its own remote name, got %q", fields[1])
	}
	t.Logf("end-to-end reply: %s", strings.TrimSpace(string(resp)))
}

// TestAgentRefusesDisallowedService — a service outside the allowlist must not
// run even when its script exists. Defense in depth against a script dropped
// into the directory becoming reachable; the real boundary stays in dom0 policy.
func TestAgentRefusesDisallowedService(t *testing.T) {
	svcDir := t.TempDir()
	// A script that would be very bad to run by accident.
	if err := os.WriteFile(filepath.Join(svcDir, "qubes.VMShell"),
		[]byte("#!/bin/sh\necho SHOULD-NOT-RUN\n"), 0o700); err != nil {
		t.Fatal(err)
	}

	ca, caKey := mkCA(t)
	inv := agent.NewLocalInvoker("remote-dev", []string{"qubesair.Ping"})
	inv.ServiceDir = svcDir

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	srv := NewServer(ServerConfig{Listen: addr, TLS: mkServerTLS(t, ca, caKey)}, inv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()

	cli := NewClient(ClientConfig{
		RemoteEndpoint: addr, RelayName: "sys-relay-pve", RemoteName: "remote-dev",
		TLS: mkClientTLS(t, ca, caKey),
	}, nil)
	waitDial(t, addr)
	go func() { _ = cli.Start(ctx) }()

	// Retry only until the tunnel is up; once it is, the refusal is the answer
	// we want and must not be retried away.
	out, err := callWhenReady(t, cli, "remote-dev", "qubes.VMShell", nil)
	if err == nil {
		t.Fatalf("a disallowed service must not run, got output %q", out)
	}
	if strings.Contains(string(out), "SHOULD-NOT-RUN") {
		t.Fatal("the disallowed script actually executed")
	}
}

// callWhenReady retries a call until the tunnel is established, then returns
// whatever the call produced.
//
// Only ErrNotConnected is retried: any other outcome — including a deliberate
// refusal — is the result under test and must be returned as-is, or a test
// asserting a rejection would spin until it timed out.
func callWhenReady(t *testing.T, cli *Client, target, service string, in []byte) ([]byte, error) {
	t.Helper()
	// The overall budget is the real guard against a hang; the per-call timeout
	// only paces the retries.
	deadline := time.Now().Add(30 * time.Second)
	for {
		callCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		out, err := cli.Call(callCtx, target, service, in)
		cancel()
		if time.Now().After(deadline) {
			return out, err
		}
		// "Not connected" and "the per-call timeout expired" both mean the tunnel
		// is not up YET, so both must be retried.
		//
		// Retrying only ErrNotConnected is what made this test flaky: under
		// `go test ./...` the handshake takes longer than one per-call timeout,
		// so the failure arrives as DeadlineExceeded instead — a different error
		// with the same meaning — and the loop returned it immediately without
		// ever spending the budget it was given.
		if !errors.Is(err, ErrNotConnected) && !errors.Is(err, context.DeadlineExceeded) {
			return out, err
		}
		time.Sleep(25 * time.Millisecond)
	}
}
