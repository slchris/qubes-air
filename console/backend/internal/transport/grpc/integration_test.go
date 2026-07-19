package grpc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"
)

// fakeInvoker executes forward calls on the "remote" side: it upper-cases-tag
// the input so the test can assert the round-trip actually ran server-side.
type fakeInvoker struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeInvoker) Invoke(_ context.Context, target, service string, in []byte) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, target+"/"+service)
	f.mu.Unlock()
	return []byte("remote-handled:" + string(in)), nil
}

// tagInvoker echoes back handled[target/service]:input — used by the shared
// startTestServer/dialAndCall helpers.
type tagInvoker struct{}

func (tagInvoker) Invoke(_ context.Context, target, service string, in []byte) ([]byte, error) {
	return []byte("handled[" + target + "/" + service + "]:" + string(in)), nil
}

// startTestServer stands up a real mTLS gRPC server on a random localhost port
// with a tagInvoker, and returns its address. It stops on test cleanup.
func startTestServer(t *testing.T, serverTLS *tls.Config) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	srv := NewServer(ServerConfig{Listen: addr, TLS: serverTLS}, tagInvoker{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx) }()
	t.Cleanup(cancel)
	waitDial(t, addr)
	return addr
}

// dialAndCall starts a client from cfg, waits for the tunnel, and performs one
// forward Call to remote-gpu/qubesair.Ping with body "ping", returning the
// response. It cleans up the client on test cleanup.
func dialAndCall(t *testing.T, cfg ClientConfig) []byte {
	t.Helper()
	cli := NewClient(cfg, nil)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = cli.Start(ctx) }()
	t.Cleanup(cancel)

	deadline := time.Now().Add(3 * time.Second)
	for {
		callCtx, c := context.WithTimeout(context.Background(), time.Second)
		out, err := cli.Call(callCtx, "remote-gpu", "qubesair.Ping", []byte("ping"))
		c()
		if err == nil {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("Call never succeeded: %v", err)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// TestClientServerRoundTrip stands up a real mTLS gRPC server, dials it with the
// client, and drives one forward Call end-to-end through the Tunnel. This proves
// the whole stack wires up: mTLS handshake, Tunnel, Handshake frame, forward
// request/response framing, and response delivery by request_id.
func TestClientServerRoundTrip(t *testing.T) {
	caCert, caKey := mkCA(t)
	serverTLS := mkServerTLS(t, caCert, caKey)
	clientTLS := mkClientTLS(t, caCert, caKey)

	// Listen on a random localhost port so we know the address before Serve.
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close() // Serve re-listens on the same addr string; free it first.

	inv := &fakeInvoker{}
	srv := NewServer(ServerConfig{Listen: addr, TLS: serverTLS}, inv)

	srvCtx, srvCancel := context.WithCancel(context.Background())
	defer srvCancel()
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(srvCtx) }()
	waitDial(t, addr) // wait until the server is accepting connections

	// Client dials outbound with mTLS and keeps the tunnel alive.
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

	// Wait for the tunnel to be established (Call returns ErrNotConnected until then).
	var out []byte
	deadline := time.Now().Add(3 * time.Second)
	for {
		callCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		out, err = cli.Call(callCtx, "remote-gpu", "qubesair.Echo", []byte("hello"))
		cancel()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Call never succeeded, last err: %v", err)
		}
		time.Sleep(30 * time.Millisecond)
	}

	// Assert the response came back through the server's invoker.
	if got, want := string(out), "remote-handled:hello"; got != want {
		t.Fatalf("round-trip response = %q, want %q", got, want)
	}
	inv.mu.Lock()
	gotCalls := append([]string(nil), inv.calls...)
	inv.mu.Unlock()
	if len(gotCalls) == 0 || gotCalls[len(gotCalls)-1] != "remote-gpu/qubesair.Echo" {
		t.Fatalf("invoker calls = %v, want last = remote-gpu/qubesair.Echo", gotCalls)
	}

	// Clean shutdown.
	cliCancel()
	srvCancel()
	select {
	case <-serveErr:
	case <-time.After(2 * time.Second):
		t.Log("server did not stop within 2s (non-fatal)")
	}
}

// TestClientCallInvalidName ensures name validation rejects bad input before
// anything hits the wire.
func TestClientCallInvalidName(t *testing.T) {
	cli := NewClient(ClientConfig{RemoteEndpoint: "127.0.0.1:1"}, nil)
	if _, err := cli.Call(context.Background(), "bad name", "svc", nil); err == nil {
		t.Fatal("expected error for invalid target name")
	}
}

// --- test TLS material (self-signed CA → server & client leaf certs) ---

func mkCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ca key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "qubes-air-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("ca cert: %v", err)
	}
	cert, _ := x509.ParseCertificate(der)
	return cert, key
}

func mkLeaf(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, cn string, isServer bool) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("leaf key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	if isServer {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		tmpl.DNSNames = []string{"localhost"}
	} else {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("leaf cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("x509 key pair: %v", err)
	}
	return pair
}

func mkServerTLS(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey) *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return &tls.Config{
		Certificates: []tls.Certificate{mkLeaf(t, ca, caKey, "remote-relay", true)},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}
}

func mkClientTLS(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey) *tls.Config {
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return &tls.Config{
		Certificates: []tls.Certificate{mkLeaf(t, ca, caKey, "sys-relay", false)},
		RootCAs:      pool,
		ServerName:   "localhost",
		MinVersion:   tls.VersionTLS12,
	}
}

func waitDial(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("server never came up at %s: %v", addr, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
