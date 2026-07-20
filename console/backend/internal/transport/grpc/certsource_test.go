package grpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"math/big"
	"net"
	"sync/atomic"
	"testing"

	"github.com/slchris/qubes-air/console/internal/agent"
)

// swappableCert stands in for *agent.Identity: a certificate replaced by a
// single atomic store, exactly as a renewal replaces one.
type swappableCert struct {
	cur atomic.Pointer[tls.Certificate]
	err atomic.Pointer[error]
}

func (s *swappableCert) ServerCertificate() (*tls.Certificate, error) {
	if e := s.err.Load(); e != nil {
		return nil, *e
	}
	return s.cur.Load(), nil
}

func (s *swappableCert) set(c *tls.Certificate) { s.cur.Store(c) }

func (s *swappableCert) fail(err error) { s.err.Store(&err) }

// serialOf returns a certificate's serial, which is what distinguishes two
// leaves minted for the same name.
func serialOf(t *testing.T, cert tls.Certificate) *big.Int {
	t.Helper()
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	return leaf.SerialNumber
}

// peerLeaf opens ONE TLS connection and returns the certificate the server
// presented.
//
// Deliberately with no ServerName. Go only consults GetCertificate when
// tls.Config.Certificates is empty OR the ClientHello carried an SNI name, and
// the console's prober dials a qube by IP with nothing to put in SNI (see
// service.probeTLSConfig). A hot-reload hook that only works for SNI clients
// would be silently skipped for the exact caller renewal exists to serve, so
// the test dials the way that caller does.
func peerLeaf(t *testing.T, addr string, clientCert tls.Certificate) *x509.Certificate {
	t.Helper()
	raw, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	conn := tls.Client(raw, &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		// The chain is not what this test is about, and the agent's leaf carries
		// no name matching an IP dial anyway.
		InsecureSkipVerify: true,
		MinVersion:         tls.VersionTLS12,
	})
	defer func() { _ = conn.Close() }()

	if err := conn.Handshake(); err != nil {
		t.Fatalf("handshake with %s: %v", addr, err)
	}
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		t.Fatal("server presented no certificate")
	}
	return state.PeerCertificates[0]
}

// TestCertSourceServesRenewedCertificate is the hot-reload property.
//
// Without it, tls.Config.Certificates is read once at Serve and a renewed
// certificate takes effect only at the next restart — which on a fleet nobody
// reboots means never, so the certificate expires with a valid replacement
// sitting on disk. The test also asserts the tunnel that was already up keeps
// working across the swap: a renewal that dropped live connections would take
// the agent offline every ninety days on purpose.
func TestCertSourceServesRenewedCertificate(t *testing.T) {
	caCert, caKey := mkCA(t)
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	initial := mkLeaf(t, caCert, caKey, "agent-qube-1", true)
	renewed := mkLeaf(t, caCert, caKey, "agent-qube-1", true)
	clientCert := mkLeaf(t, caCert, caKey, "sys-relay-pve", false)

	src := &swappableCert{}
	src.set(&initial)

	// A builtin rather than a script: it also proves the builtin dispatch works
	// over the real transport, which is how renewal itself is reached.
	inv := agent.NewLocalInvoker("remote-dev", nil)
	if err := inv.RegisterBuiltin("qubesair.Status", func(context.Context, string, []byte) ([]byte, error) {
		return []byte("ok"), nil
	}); err != nil {
		t.Fatal(err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	srv := NewServer(ServerConfig{
		Listen: addr,
		TLS: &tls.Config{
			Certificates: []tls.Certificate{initial},
			ClientCAs:    pool,
			ClientAuth:   tls.RequireAndVerifyClientCert,
			MinVersion:   tls.VersionTLS12,
		},
		CertSource: src,
	}, inv)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()
	waitDial(t, addr)

	cli := NewClient(ClientConfig{
		RemoteEndpoint: addr,
		RelayName:      "sys-relay-pve",
		RemoteName:     "remote-dev",
		TLS: &tls.Config{
			Certificates: []tls.Certificate{clientCert},
			RootCAs:      pool,
			ServerName:   "localhost",
			MinVersion:   tls.VersionTLS12,
		},
	}, nil)
	go func() { _ = cli.Start(ctx) }()

	if _, err := callWhenReady(t, cli, "remote-dev", "qubesair.Status", nil); err != nil {
		t.Fatalf("call before renewal: %v", err)
	}

	before := peerLeaf(t, addr, clientCert)
	if before.SerialNumber.Cmp(serialOf(t, initial)) != 0 {
		t.Fatal("the server did not start on the certificate it was configured with")
	}

	// The renewal.
	src.set(&renewed)

	// The tunnel established under the OLD certificate must survive. Renewal
	// runs over this connection; tearing it down to install a certificate would
	// mean the console never hears whether the renewal it just performed
	// worked.
	if _, err := cli.Call(ctx, "remote-dev", "qubesair.Status", nil); err != nil {
		t.Fatalf("the live tunnel was dropped by a certificate swap: %v", err)
	}

	after := peerLeaf(t, addr, clientCert)
	if after.SerialNumber.Cmp(serialOf(t, renewed)) != 0 {
		t.Fatalf("a new connection was served serial %s, want the renewed %s "+
			"(the certificate only takes effect on restart)",
			after.SerialNumber, serialOf(t, renewed))
	}
}

// TestCertSourceFallsBackToStartupCertificate — a certificate source that
// cannot answer must not take the agent off the network. The previous
// certificate is still valid, and serving it beats answering nothing: an agent
// that completes no handshake cannot be reached by the console, which is the
// only thing that can repair it.
func TestCertSourceFallsBackToStartupCertificate(t *testing.T) {
	caCert, caKey := mkCA(t)
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	startup := mkLeaf(t, caCert, caKey, "agent-qube-1", true)
	clientCert := mkLeaf(t, caCert, caKey, "sys-relay-pve", false)

	src := &swappableCert{}
	src.fail(errors.New("identity unreadable"))

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	srv := NewServer(ServerConfig{
		Listen: addr,
		TLS: &tls.Config{
			Certificates: []tls.Certificate{startup},
			ClientCAs:    pool,
			ClientAuth:   tls.RequireAndVerifyClientCert,
			MinVersion:   tls.VersionTLS12,
		},
		CertSource: src,
	}, agent.NewLocalInvoker("remote-dev", nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()
	waitDial(t, addr)

	got := peerLeaf(t, addr, clientCert)
	if got.SerialNumber.Cmp(serialOf(t, startup)) != 0 {
		t.Fatal("a failing certificate source must fall back to the startup certificate")
	}
}
