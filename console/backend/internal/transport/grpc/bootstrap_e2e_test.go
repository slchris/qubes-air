package grpc

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/agent"
	"github.com/slchris/qubes-air/console/internal/pki"
)

// TestBootstrapOverTheTunnel is first-certificate issuance run the way it will
// actually run: a guest that booted with nothing but a token and a CA, a
// console that dials in, and an agent that ends up serving a real identity on
// the next handshake — with the private key never having crossed the wire.
//
// End to end on purpose. The pieces have their own tests, but the failure this
// replaces was a qube that booted clean, reported healthy, and had no working
// agent. Only a test that runs the real path can say this one does.
func TestBootstrapOverTheTunnel(t *testing.T) {
	ca, err := pki.NewCA("qubes-air-console", 0)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	const (
		qubeName = "remote-dev"
		token    = "one-shot-token"
	)
	qubeCN := pki.AgentCommonName(qubeName)

	// What cloud-init delivers under the token design: the CA and nothing else.
	// No certificate, no private key.
	dir := t.TempDir()
	certPath := filepath.Join(dir, "agent.pem")
	keyPath := filepath.Join(dir, "agent-key.pem")
	caPath := filepath.Join(dir, "ca.pem")
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Cert.Raw}))
	writeFixture(t, caPath, caPEM, 0o644)

	identity, err := agent.NewPendingIdentity(certPath, keyPath, caPath)
	if err != nil {
		t.Fatalf("NewPendingIdentity: %v", err)
	}
	if identity.HasCertificate() {
		t.Fatalf("a guest given only a CA came up already holding an identity")
	}

	inv := agent.NewLocalInvoker(qubeName, nil)
	boot, err := agent.NewBootstrapService(identity, qubeName, token, nil)
	if err != nil {
		t.Fatalf("NewBootstrapService: %v", err)
	}
	if err := boot.RegisterBuiltins(inv); err != nil {
		t.Fatalf("RegisterBuiltins: %v", err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	// The listener still demands a client certificate chaining to the
	// cloud-init CA — that requirement is what protects the token — while
	// presenting the placeholder, because there is nothing else to present.
	srv := NewServer(ServerConfig{
		Listen:     addr,
		TLS:        identity.ServerTLSConfig(),
		CertSource: boot,
	}, inv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()
	waitDial(t, addr)

	// The console's side: a short-lived certificate from the same CA, and NO
	// verification of the peer — the agent has no certificate yet, which is the
	// condition being repaired. The token is what authenticates it in return.
	consoleBundle, err := ca.IssueAgentCert("console-bootstrap", 5*time.Minute)
	if err != nil {
		t.Fatalf("issue console cert: %v", err)
	}
	consolePair, err := tls.X509KeyPair([]byte(consoleBundle.CertPEM), []byte(consoleBundle.KeyPEM))
	if err != nil {
		t.Fatalf("console key pair: %v", err)
	}
	bootTLS := &tls.Config{
		Certificates:       []tls.Certificate{consolePair},
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true,
	}

	cli := NewClient(ClientConfig{
		RemoteEndpoint: addr,
		RelayName:      "console-bootstrap",
		RemoteName:     qubeName,
		TLS:            bootTLS.Clone(),
	}, nil)
	go func() { _ = cli.Start(ctx) }()

	// --- 1. BeginBootstrap: the agent generates a key, keeps it, and hands
	// back only the token and a CSR.
	out, err := callWhenReady(t, cli, qubeName, agent.ServiceBeginBootstrap, nil)
	if err != nil {
		t.Fatalf("BeginBootstrap over the tunnel: %v", err)
	}
	if strings.Contains(string(out), "PRIVATE KEY") {
		t.Fatalf("BeginBootstrap put private key material on the wire")
	}
	var began struct {
		Nonce  string `json:"nonce"`
		Token  string `json:"token"`
		CSRPEM string `json:"csr_pem"`
	}
	if err := json.Unmarshal(out, &began); err != nil {
		t.Fatalf("decode BeginBootstrap reply %q: %v", out, err)
	}
	if began.Token != token {
		t.Fatalf("agent surrendered token %q, want the one it was provisioned with", began.Token)
	}

	// --- 2. The console redeems the token (elsewhere) and signs the CSR for
	// the name the TOKEN authorizes — never the name the CSR asked for.
	csr := mustParseCSR(t, began.CSRPEM)
	if csr.Subject.CommonName != qubeCN {
		t.Fatalf("CSR asks for %q, want %q", csr.Subject.CommonName, qubeCN)
	}
	issuedPEM := signCSRAs(t, ca, csr, 90*24*time.Hour)

	// --- 3. CompleteBootstrap: verified and installed on the agent.
	body, err := json.Marshal(map[string]string{
		"nonce":    began.Nonce,
		"cert_pem": issuedPEM,
		"ca_pem":   caPEM,
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err = cli.Call(ctx, qubeName, agent.ServiceCompleteBootstrap, body)
	if err != nil {
		t.Fatalf("CompleteBootstrap over the tunnel: %v", err)
	}
	var completed struct {
		InstalledFingerprint string    `json:"installed_fingerprint"`
		NotAfter             time.Time `json:"not_after"`
	}
	if err := json.Unmarshal(out, &completed); err != nil {
		t.Fatalf("decode CompleteBootstrap reply %q: %v", out, err)
	}
	if completed.InstalledFingerprint == "" {
		t.Fatalf("the agent did not report which certificate it installed")
	}

	// --- 4. A NEW connection is served the real identity, and this time the
	// console verifies it properly — the same check the prober will make on
	// every sweep from here on. Before bootstrap this handshake could not have
	// succeeded at all.
	verifying := consoleTLS(t, consoleBundle, ca, qubeCN)
	leaf := peerLeafVerified(t, addr, verifying)
	if got := pki.FingerprintOf(leaf); got != completed.InstalledFingerprint {
		t.Fatalf("a new connection was served %s, but the agent installed %s",
			got[:16], completed.InstalledFingerprint[:16])
	}
	if leaf.Subject.CommonName != qubeCN {
		t.Fatalf("bootstrapped identity names %q, want %q", leaf.Subject.CommonName, qubeCN)
	}

	// --- 5. The pair reached disk and loads, which is the state a restart
	// finds — the agent must come up in normal mode, not bootstrap mode.
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
		t.Fatalf("the installed pair does not load; this agent would not restart: %v", err)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("stat installed key: %v", err)
	}
	restarted, err := agent.NewPendingIdentity(certPath, keyPath, caPath)
	if err != nil {
		t.Fatalf("restart after bootstrap: %v", err)
	}
	if !restarted.HasCertificate() {
		t.Fatalf("a restart after bootstrap came up pending again; it would try to spend a spent token")
	}

	// --- 6. And the token surface is closed.
	if _, err := cli.Call(ctx, qubeName, agent.ServiceBeginBootstrap, nil); err == nil {
		t.Fatalf("the agent handed out its token again after bootstrapping")
	}
}

// TestBootstrapRefusesAPeerTheCADidNotSign is the property the whole direction
// rests on. The console dialing an un-bootstrapped agent cannot verify the
// peer, so the token is the agent's only protection — and it must be
// surrendered ONLY to a caller holding a certificate from the CA cloud-init
// delivered. If a random client on the LAN could complete this handshake, the
// token would be readable by anyone who can reach the port, and with it they
// could obtain a real fleet identity.
func TestBootstrapRefusesAPeerTheCADidNotSign(t *testing.T) {
	ca, err := pki.NewCA("qubes-air-console", 0)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	const qubeName = "remote-dev"

	dir := t.TempDir()
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Cert.Raw}))
	writeFixture(t, filepath.Join(dir, "ca.pem"), caPEM, 0o644)
	identity, err := agent.NewPendingIdentity(
		filepath.Join(dir, "agent.pem"),
		filepath.Join(dir, "agent-key.pem"),
		filepath.Join(dir, "ca.pem"),
	)
	if err != nil {
		t.Fatalf("NewPendingIdentity: %v", err)
	}

	inv := agent.NewLocalInvoker(qubeName, nil)
	boot, err := agent.NewBootstrapService(identity, qubeName, "one-shot-token", nil)
	if err != nil {
		t.Fatalf("NewBootstrapService: %v", err)
	}
	if err := boot.RegisterBuiltins(inv); err != nil {
		t.Fatalf("RegisterBuiltins: %v", err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	srv := NewServer(ServerConfig{
		Listen:     addr,
		TLS:        identity.ServerTLSConfig(),
		CertSource: boot,
	}, inv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()
	waitDial(t, addr)

	// An attacker's own CA. Chains to nothing this agent trusts.
	foreign, err := pki.NewCA("attacker", 0)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	foreignBundle, err := foreign.IssueAgentCert("console-bootstrap", 5*time.Minute)
	if err != nil {
		t.Fatalf("issue foreign cert: %v", err)
	}
	foreignPair, err := tls.X509KeyPair([]byte(foreignBundle.CertPEM), []byte(foreignBundle.KeyPEM))
	if err != nil {
		t.Fatalf("foreign key pair: %v", err)
	}

	for name, cfg := range map[string]*tls.Config{
		"a certificate from another CA": {
			Certificates:       []tls.Certificate{foreignPair},
			MinVersion:         tls.VersionTLS13,
			InsecureSkipVerify: true,
		},
		"no client certificate at all": {
			MinVersion:         tls.VersionTLS13,
			InsecureSkipVerify: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			conn, err := tls.Dial("tcp", addr, cfg)
			if err == nil {
				// Some failures surface only once bytes flow; either way, no
				// token may come back.
				_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
				var buf [1]byte
				_, err = conn.Read(buf[:])
				_ = conn.Close()
			}
			if err == nil {
				t.Fatalf("an unvouched-for client completed the handshake; the token is exposed to the LAN")
			}
		})
	}
}
