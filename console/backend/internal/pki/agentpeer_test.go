package pki

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"
)

// peerCert is a certificate a test agent presents, with the key it proves
// possession of.
type peerCert struct {
	leaf *x509.Certificate
	key  *ecdsa.PrivateKey
}

// tlsCert is the peer as the TLS stack serves it: leaf first, then any extra
// chain certificates.
func (p peerCert) tlsCert(extra ...*x509.Certificate) tls.Certificate {
	chain := make([][]byte, 0, 1+len(extra))
	chain = append(chain, p.leaf.Raw)
	for _, c := range extra {
		chain = append(chain, c.Raw)
	}
	return tls.Certificate{Certificate: chain, PrivateKey: p.key, Leaf: p.leaf}
}

// agentTemplate is the shape signAgentCert gives an agent server certificate.
// Tests change one property at a time to prove each is checked.
func agentTemplate(t *testing.T, cn string) *x509.Certificate {
	t.Helper()
	serial, err := randomSerial()
	if err != nil {
		t.Fatalf("serial: %v", err)
	}
	return &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           ekuForRole(RoleAgent),
		BasicConstraintsValid: true,
		URIs:                  []*url.URL{roleURI(RoleAgent)},
	}
}

// signPeer signs tmpl with issuer (a CA, or — to forge — any leaf and its key).
func signPeer(t *testing.T, tmpl, issuer *x509.Certificate, issuerKey *ecdsa.PrivateKey) peerCert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, issuer, &key.PublicKey, issuerKey)
	if err != nil {
		t.Fatalf("sign peer certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse peer certificate: %v", err)
	}
	return peerCert{leaf: leaf, key: key}
}

// issuedPeer is a certificate from the CA's real issuance path.
func issuedPeer(t *testing.T, ca *CA, cn string) peerCert {
	t.Helper()
	b, err := ca.IssueAgentCert(cn, time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentCert(%q): %v", cn, err)
	}
	pair, err := tls.X509KeyPair([]byte(b.CertPEM), []byte(b.KeyPEM))
	if err != nil {
		t.Fatalf("load issued pair: %v", err)
	}
	key, ok := pair.PrivateKey.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatalf("issued key is %T, want ECDSA", pair.PrivateKey)
	}
	return peerCert{leaf: parseLeaf(t, b.CertPEM), key: key}
}

func rootsOf(ca *CA) *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)
	return pool
}

// dialConfig is the client config relay-call and pingcheck dial "target" with:
// a console client certificate from ca, trusting ca only.
func dialConfig(t *testing.T, ca *CA) *tls.Config {
	t.Helper()
	cfg, err := AgentDialTLSConfig(issuedPeer(t, ca, "console-relay").tlsCert(), rootsOf(ca), "target")
	if err != nil {
		t.Fatalf("AgentDialTLSConfig: %v", err)
	}
	return cfg
}

// handshake runs a real TLS 1.3 handshake between cfg and a server presenting
// serverCert, and returns the client's verdict. Going through the stack rather
// than calling the callback proves the callback is actually wired into the
// handshake that InsecureSkipVerify would otherwise leave unchecked.
//
// Loopback TCP rather than net.Pipe: a pipe has no buffer, so a client that
// refuses the server mid-flight blocks writing its alert while the server
// blocks writing the rest of its flight.
func handshake(t *testing.T, cfg *tls.Config, serverCert tls.Certificate) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		// The server's verdict is not under test; a client that refuses the
		// peer makes this fail, which is expected.
		_ = tls.Server(conn, &tls.Config{
			Certificates: []tls.Certificate{serverCert},
			MinVersion:   tls.VersionTLS13,
			ClientAuth:   tls.RequireAnyClientCert,
		}).HandshakeContext(ctx)
	}()

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	client := tls.Client(conn, cfg)
	err = client.HandshakeContext(ctx)
	_ = conn.Close()
	<-serverDone
	return err
}

// TestAgentDialTLSConfig_Handshake is the negative matrix AGENTS.md §5 demands
// for InsecureSkipVerify — wrong CA, wrong role, wrong target, expired — plus
// the usage and chain cases that sit beside them. Every case is a real
// handshake against the config relay-call and pingcheck dial with.
func TestAgentDialTLSConfig_Handshake(t *testing.T) {
	ca := mustCA(t)
	otherCA := mustCA(t)
	realAgent := issuedPeer(t, ca, AgentCommonName("target"))

	withTemplate := func(mutate func(*x509.Certificate)) tls.Certificate {
		tmpl := agentTemplate(t, AgentCommonName("target"))
		mutate(tmpl)
		return signPeer(t, tmpl, ca.Cert, ca.Key).tlsCert()
	}
	forged := signPeer(t, agentTemplate(t, AgentCommonName("target")), realAgent.leaf, realAgent.key)

	cases := []struct {
		name    string
		server  tls.Certificate
		wantErr string // empty: the handshake must succeed
	}{
		{"the agent it dialed, from its CA", realAgent.tlsCert(), ""},
		{"wrong CA", issuedPeer(t, otherCA, AgentCommonName("target")).tlsCert(),
			"signed by unknown authority"},
		// The same foreign agent, now also sending its own CA's self-signed
		// certificate. Whatever the peer sends is at most an intermediate: a
		// verifier that let the peer's chain into the roots would accept any CA
		// the peer cared to bring with it.
		{"wrong CA, sending that CA as an intermediate",
			issuedPeer(t, otherCA, AgentCommonName("target")).tlsCert(otherCA.Cert),
			"signed by unknown authority"},
		{"wrong role: relay certificate with server usage",
			withTemplate(func(c *x509.Certificate) { c.URIs = []*url.URL{roleURI(RoleRelay)} }),
			`peer role "relay" is not an agent`},
		{"wrong role: no role at all",
			withTemplate(func(c *x509.Certificate) { c.URIs = nil }),
			"carries no usable role"},
		{"wrong role: agent and console at once",
			withTemplate(func(c *x509.Certificate) { c.URIs = append(c.URIs, roleURI(RoleConsole)) }),
			"carries no usable role"},
		{"wrong target: another qube's valid agent certificate",
			issuedPeer(t, ca, AgentCommonName("other")).tlsCert(),
			`should be serving "agent-target"`},
		// Inside the CA's own validity window (it starts five minutes back), so
		// the leaf's expiry, not the CA's, is what refuses it.
		{"expired",
			withTemplate(func(c *x509.Certificate) {
				c.NotBefore = time.Now().Add(-4 * time.Minute)
				c.NotAfter = time.Now().Add(-time.Minute)
			}),
			"expired or is not yet valid"},
		{"not yet valid",
			withTemplate(func(c *x509.Certificate) {
				c.NotBefore = time.Now().Add(time.Hour)
				c.NotAfter = time.Now().Add(2 * time.Hour)
			}),
			"expired or is not yet valid"},
		{"wrong usage: console client certificate answering as a server",
			issuedPeer(t, ca, "console-relay").tlsCert(),
			"incompatible key usage"},
		{"wrong usage: agent role with client usage only",
			withTemplate(func(c *x509.Certificate) { c.ExtKeyUsage = ekuForRole(RoleConsole) }),
			"incompatible key usage"},
		// A real agent's leaf key signing a certificate for the same name, sent
		// with that leaf as the intermediate: the leaf is not a CA, so the
		// chain stops there.
		{"leaf used as an issuer",
			forged.tlsCert(realAgent.leaf),
			"cannot sign this kind of certificate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := handshake(t, dialConfig(t, ca), tc.server)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("handshake with the right agent failed: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("handshake succeeded; want refusal containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("refusal %q does not say %q", err, tc.wantErr)
			}
		})
	}
}

// TestAgentDialTLSConfig_Shape pins the properties the callback depends on.
func TestAgentDialTLSConfig_Shape(t *testing.T) {
	ca := mustCA(t)
	cfg := dialConfig(t, ca)

	if cfg.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %#x, want TLS 1.3", cfg.MinVersion)
	}
	if cfg.VerifyConnection == nil {
		t.Fatal("InsecureSkipVerify without VerifyConnection accepts any peer")
	}
	if len(cfg.Certificates) != 1 {
		t.Errorf("config carries %d client certificates, want 1", len(cfg.Certificates))
	}
	if cfg.RootCAs == nil {
		t.Error("config carries no roots")
	}
}

// TestAgentDialTLSConfig_MissingPeerCertificate — the callback runs on resumed
// sessions too, where no full handshake preceded it; an empty peer chain must be
// refused, not treated as nothing to check.
func TestAgentDialTLSConfig_MissingPeerCertificate(t *testing.T) {
	cfg := dialConfig(t, mustCA(t))
	err := cfg.VerifyConnection(tls.ConnectionState{})
	if err == nil || !strings.Contains(err.Error(), "presented no certificate") {
		t.Fatalf("empty peer chain: got %v, want a refusal", err)
	}
}

// TestAgentDialTLSConfig_RefusesIncompleteInput — every missing input would
// quietly weaken the check rather than fail it: no roots means the system store,
// no name means a pin to "agent-", no client certificate means an agent that
// cannot authenticate the caller at all.
func TestAgentDialTLSConfig_RefusesIncompleteInput(t *testing.T) {
	ca := mustCA(t)
	client := issuedPeer(t, ca, "console-relay").tlsCert()
	roots := rootsOf(ca)

	cases := []struct {
		name   string
		client tls.Certificate
		roots  *x509.CertPool
		remote string
	}{
		{"no client certificate", tls.Certificate{}, roots, "target"},
		{"client certificate without its key", tls.Certificate{Certificate: client.Certificate}, roots, "target"},
		{"no roots", client, nil, "target"},
		{"no remote name", client, roots, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if cfg, err := AgentDialTLSConfig(tc.client, tc.roots, tc.remote); err == nil {
				t.Fatalf("got a config (%v), want a refusal", cfg != nil)
			}
		})
	}
}

// TestVerifyAgentChain_RefusesIncompleteInput covers the direct callers (the
// console probe) that build their own config around VerifyAgentChain.
func TestVerifyAgentChain_RefusesIncompleteInput(t *testing.T) {
	ca := mustCA(t)
	good := []*x509.Certificate{issuedPeer(t, ca, AgentCommonName("target")).leaf}

	if err := VerifyAgentChain(rootsOf(ca), good, AgentCommonName("target")); err != nil {
		t.Fatalf("the right agent was refused: %v", err)
	}

	// Each refusal must come from the input check itself. Without it a nil pool
	// would still fail here — but only because the system store happens not to
	// hold this test CA — and an empty name only by the CN comparison.
	cases := []struct {
		name   string
		roots  *x509.CertPool
		certs  []*x509.Certificate
		wantCN string
		want   string
	}{
		{"nil roots", nil, good, AgentCommonName("target"), "no CA pool"},
		{"no expected identity", rootsOf(ca), good, "", "no agent identity"},
		{"no peer certificate", rootsOf(ca), nil, AgentCommonName("target"), "presented no certificate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifyAgentChain(tc.roots, tc.certs, tc.wantCN)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want a refusal saying %q", err, tc.want)
			}
		})
	}
}
