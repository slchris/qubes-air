package grpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/slchris/qubes-air/console/internal/repository"
)

// fakeRegistry is an in-memory CertRegistry whose answers can change mid-test,
// which is the whole point: revocation is a decision made between connections.
type fakeRegistry struct {
	mu      sync.Mutex
	revoked map[string]bool
	calls   int
}

func (f *fakeRegistry) Authorize(_ context.Context, fp string) (*repository.AgentCert, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.revoked[fp] {
		return nil, errors.New("certificate revoked")
	}
	return &repository.AgentCert{Fingerprint: fp, QubeID: "qube-1"}, nil
}

func (f *fakeRegistry) TouchLastSeen(_ context.Context, _ string) error { return nil }

func (f *fakeRegistry) revoke(fp string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revoked[fp] = true
}

func (f *fakeRegistry) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// TestRevocationSurvivesSessionResumption is the regression test for a hole in
// the revocation path.
//
// The registry check used to hang off tls.Config.VerifyPeerCertificate, which
// Go calls only during a FULL handshake. A client that resumes a session — TLS
// 1.3 PSK, or a 1.2 session ticket — skips certificate verification entirely
// and has its peer certificate restored from the cached session, so the check
// never ran. A revoked agent could keep reconnecting for the lifetime of its
// ticket, which is exactly the permanent access the registry exists to remove.
//
// The assertion that makes this test mean anything is didResume: without it a
// run in which resumption never happened would pass while proving nothing.
func TestRevocationSurvivesSessionResumption(t *testing.T) {
	ca, err := pki.NewCA("qubes-air-console", 0)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	agentBundle, err := ca.IssueAgentCert("agent-remote-dev", 0)
	if err != nil {
		t.Fatalf("issue agent cert: %v", err)
	}
	clientCert, err := tls.X509KeyPair([]byte(agentBundle.CertPEM), []byte(agentBundle.KeyPEM))
	if err != nil {
		t.Fatalf("client key pair: %v", err)
	}

	reg := &fakeRegistry{revoked: map[string]bool{}}
	srv := &Server{cfg: ServerConfig{CertRegistry: reg}}

	// Wire the server exactly as Serve does, with a probe around it that records
	// whether the TLS stack considered this handshake a resumption.
	var mu sync.Mutex
	var didResume bool
	serverTLS := mkServerTLSFromCA(t, ca)
	serverTLS.VerifyConnection = func(cs tls.ConnectionState) error {
		mu.Lock()
		didResume = cs.DidResume
		mu.Unlock()
		return srv.verifyRegisteredConnection(cs)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer lis.Close()

	// Accept and serve until the test is done. Handshake errors are expected on
	// the revoked attempt, so they are swallowed rather than failing the test.
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			raw, err := lis.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				tc := tls.Server(c, serverTLS)
				if err := tc.Handshake(); err != nil {
					return
				}
				// TLS 1.3 delivers the session ticket after the handshake, and
				// the client only processes it on a Read. Writing here is what
				// makes resumption possible on the second dial.
				_, _ = tc.Write([]byte("x"))
				select {
				case <-done:
				case <-time.After(2 * time.Second):
				}
			}(raw)
		}
	}()

	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	clientTLS := &tls.Config{
		Certificates:       []tls.Certificate{clientCert},
		RootCAs:            pool,
		ServerName:         "localhost",
		MinVersion:         tls.VersionTLS12,
		ClientSessionCache: tls.NewLRUClientSessionCache(8),
	}

	// First connection: the certificate is registered, so this must succeed.
	c1, err := tls.Dial("tcp", lis.Addr().String(), clientTLS)
	if err != nil {
		t.Fatalf("first connection was refused while the certificate was valid: %v", err)
	}
	buf := make([]byte, 1)
	if _, err := io.ReadFull(c1, buf); err != nil {
		t.Fatalf("reading from the first connection (needed to collect the session ticket): %v", err)
	}
	c1.Close()

	if reg.callCount() == 0 {
		t.Fatal("the registry was never consulted on a full handshake")
	}

	// Revoke, then reconnect reusing the cached session.
	reg.revoke(agentBundle.Fingerprint)

	// A successful Dial does NOT mean the server accepted. Under TLS 1.3 the
	// client finishes its side without waiting for the server, so a rejection
	// arrives as an alert on the first read. Reading is what makes the refusal
	// observable — asserting on Dial alone would pass even with no check at all.
	c2, dialErr := tls.Dial("tcp", lis.Addr().String(), clientTLS)
	if dialErr == nil {
		defer c2.Close()
		_ = c2.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, err := io.ReadFull(c2, buf); err == nil {
			t.Fatal("a REVOKED certificate was served data; the registry check " +
				"is not running on the resumed handshake path")
		}
	}

	mu.Lock()
	resumed := didResume
	mu.Unlock()
	if !resumed {
		t.Skip("the TLS stack did not resume the session, so this run does not " +
			"exercise the path the test exists to cover")
	}
}
