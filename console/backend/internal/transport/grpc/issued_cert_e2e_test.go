package grpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/slchris/qubes-air/console/internal/agent"
	"github.com/slchris/qubes-air/console/internal/pki"
)

// TestIssuedCertAuthenticatesEndToEnd closes the loop between the CA and the
// transport.
//
// The two halves were verified separately: pki produces certificates that
// verify, and the transport accepts registered ones. This asserts they agree in
// practice — a certificate minted by the console's own CA is one the agent's
// TLS stack accepts, and a call carried over it reaches a real service. If the
// key usage, the chain, or the TLS requirements ever drift apart, every agent
// in the field fails to connect, and it would look like a network fault.
func TestIssuedCertAuthenticatesEndToEnd(t *testing.T) {
	src, err := os.ReadFile("../../../../../remote/qubes-rpc/qubesair.Ping")
	if err != nil {
		t.Skipf("shipped Ping script not found: %v", err)
	}
	svcDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(svcDir, "qubesair.Ping"), src, 0o700); err != nil {
		t.Fatal(err)
	}

	// Mint everything the way the console does.
	ca, err := pki.NewCA("qubes-air-console", 0)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	relayBundle, err := ca.IssueAgentCert("agent-relay", 0)
	if err != nil {
		t.Fatalf("issue relay cert: %v", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(relayBundle.CAPEM)) {
		t.Fatal("CA from the bundle is not usable")
	}
	relayCert, err := tls.X509KeyPair([]byte(relayBundle.CertPEM), []byte(relayBundle.KeyPEM))
	if err != nil {
		t.Fatalf("relay key pair: %v", err)
	}

	// The agent's server certificate. Issued separately because a client-auth
	// certificate must not be usable to impersonate a server — see
	// TestIssuedCertIsClientAuthOnly.
	serverTLS := mkServerTLSFromCA(t, ca)

	inv := agent.NewLocalInvoker("remote-dev", []string{"qubesair.Ping"})
	inv.ServiceDir = svcDir

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	srv := NewServer(ServerConfig{Listen: addr, TLS: serverTLS}, inv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()

	cli := NewClient(ClientConfig{
		RemoteEndpoint: addr,
		RelayName:      "sys-relay-pve",
		RemoteName:     "remote-dev",
		TLS: &tls.Config{
			Certificates: []tls.Certificate{relayCert},
			RootCAs:      pool,
			// Must match a SAN on the server leaf, which mkLeaf sets to
			// localhost / 127.0.0.1 — not the common name.
			ServerName: "localhost",
			MinVersion: tls.VersionTLS12,
		},
	}, nil)

	waitDial(t, addr)
	go func() { _ = cli.Start(ctx) }()

	resp, err := callWhenReady(t, cli, "remote-dev", "qubesair.Ping", nil)
	if err != nil {
		t.Fatalf("a console-issued certificate must authenticate: %v", err)
	}
	if !strings.HasPrefix(string(resp), "pong ") {
		t.Fatalf(`want "pong ...", got %q`, resp)
	}
	t.Logf("authenticated with console-issued cert %s: %s",
		relayBundle.Fingerprint[:16], strings.TrimSpace(string(resp)))
}

// mkServerTLSFromCA issues a server certificate from the given CA.
func mkServerTLSFromCA(t *testing.T, ca *pki.CA) *tls.Config {
	t.Helper()
	// Reuse the test helper's leaf minting for the server side, which needs
	// ServerAuth — deliberately not something IssueAgentCert will produce.
	leaf := mkLeaf(t, ca.Cert, ca.Key, "localhost", true)
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	return &tls.Config{
		Certificates: []tls.Certificate{leaf},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}
}
