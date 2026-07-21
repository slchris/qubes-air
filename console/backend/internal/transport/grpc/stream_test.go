package grpc

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// TestClientServerStreamTCP proves the bidirectional streaming path end to end:
// CallStream on the client reaches the server's qubesair.StreamTCP+<port>
// handler, which dials a loopback echo server, and bytes round-trip both ways
// unbuffered. This is what carries GUI (VNC/Xpra) over the agent's mTLS with no
// port exposed on the remote's LAN.
func TestClientServerStreamTCP(t *testing.T) {
	// A loopback echo server on an allowed GUI port (10000-10010).
	const port = "10005"
	echoLis, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Skipf("cannot bind 127.0.0.1:%s: %v", port, err)
	}
	defer echoLis.Close()
	go func() {
		for {
			c, err := echoLis.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()

	caCert, caKey := mkCA(t)
	serverTLS := mkServerTLS(t, caCert, caKey)
	clientTLS := mkClientTLS(t, caCert, caKey)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	srv := NewServer(ServerConfig{Listen: addr, TLS: serverTLS}, &fakeInvoker{})
	srvCtx, srvCancel := context.WithCancel(context.Background())
	defer srvCancel()
	go func() { _ = srv.Serve(srvCtx) }()
	waitDial(t, addr)

	cli := NewClient(ClientConfig{
		RemoteEndpoint: addr,
		RelayName:      "sys-relay-test",
		RemoteName:     "remote-test",
		KeepAlive:      200 * time.Millisecond,
		ReconnectMin:   20 * time.Millisecond,
		ReconnectMax:   200 * time.Millisecond,
		TLS:            clientTLS,
	}, nil)
	cliCtx, cliCancel := context.WithCancel(context.Background())
	defer cliCancel()
	go func() { _ = cli.Start(cliCtx) }()

	// Wait for the tunnel (a plain Ping call succeeds once connected).
	deadline := time.Now().Add(3 * time.Second)
	for {
		cctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err = cli.Call(cctx, "remote-gpu", "qubesair.Ping", nil)
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("tunnel never came up: %v", err)
		}
		time.Sleep(30 * time.Millisecond)
	}

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	go func() {
		err := cli.CallStream(context.Background(), "remote-gpu", streamServicePrefix+port, stdinR, stdoutW)
		_ = stdoutW.CloseWithError(err)
	}()

	msg := []byte("hello streaming world — GUI over mTLS")
	go func() { _, _ = stdinW.Write(msg) }()

	got := make([]byte, len(msg))
	readDone := make(chan error, 1)
	go func() { _, e := io.ReadFull(stdoutR, got); readDone <- e }()
	select {
	case e := <-readDone:
		if e != nil {
			t.Fatalf("read echo: %v", e)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("did not receive echoed bytes within 3s")
	}
	if string(got) != string(msg) {
		t.Fatalf("echo = %q, want %q", got, msg)
	}

	// A non-allowlisted port must be refused (no dial to arbitrary local ports).
	badErr := cli.CallStream(context.Background(), "remote-gpu", streamServicePrefix+"22", strings.NewReader(""), io.Discard)
	if badErr == nil {
		t.Fatal("stream to non-allowlisted port 22 should fail")
	}

	_ = stdinW.Close()
	cliCancel()
	srvCancel()
}
