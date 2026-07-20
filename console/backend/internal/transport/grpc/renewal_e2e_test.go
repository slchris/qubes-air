package grpc

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/agent"
	"github.com/slchris/qubes-air/console/internal/pki"
)

// TestRenewalOverTheTunnel is the whole change, exercised the way it will
// actually run: the console dials the agent, renews its certificate over that
// same mTLS connection, and the next connection is served the new certificate.
//
// It is deliberately end to end. The three pieces were built separately —
// builtin dispatch, atomic install, live certificate selection — and each has
// its own tests, but the failure this feature exists to prevent is a fleet
// whose certificates expire because renewal quietly did not happen. Only a test
// that runs the real path can say it does.
func TestRenewalOverTheTunnel(t *testing.T) {
	ca, err := pki.NewCA("qubes-air-console", 0)
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	const qubeCN = "agent-qube-1" // service.agentCertCN(qube.Name)

	// Lay out the agent's identity as cloud-init does at first boot.
	dir := t.TempDir()
	bundle, err := ca.IssueAgentCert(qubeCN, time.Hour)
	if err != nil {
		t.Fatalf("IssueAgentCert: %v", err)
	}
	certPath := filepath.Join(dir, "agent.pem")
	keyPath := filepath.Join(dir, "agent-key.pem")
	caPath := filepath.Join(dir, "ca.pem")
	writeFixture(t, certPath, bundle.CertPEM, 0o644)
	writeFixture(t, keyPath, bundle.KeyPEM, 0o600)
	writeFixture(t, caPath, bundle.CAPEM, 0o644)

	identity, err := agent.LoadIdentity(certPath, keyPath, caPath)
	if err != nil {
		t.Fatalf("LoadIdentity: %v", err)
	}
	inv := agent.NewLocalInvoker("remote-dev", nil)
	if err := agent.NewRenewalService(identity, 0).RegisterBuiltins(inv); err != nil {
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
		CertSource: identity,
	}, inv)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()
	waitDial(t, addr)

	// The console's side of the dial: a throwaway certificate from the same CA,
	// and the peer held to the qube's own name (service.probeTLSConfig).
	probeBundle, err := ca.IssueAgentCert("console-probe", 5*time.Minute)
	if err != nil {
		t.Fatalf("issue probe cert: %v", err)
	}
	probeTLS := consoleTLS(t, probeBundle, ca, qubeCN)

	cli := NewClient(ClientConfig{
		RemoteEndpoint: addr,
		RelayName:      "console-probe",
		RemoteName:     "remote-dev",
		TLS:            probeTLS.Clone(),
	}, nil)
	go func() { _ = cli.Start(ctx) }()

	// --- 1. BeginRenewal: the agent generates a key and returns only a CSR.
	out, err := callWhenReady(t, cli, "remote-dev", agent.ServiceBeginRenewal, nil)
	if err != nil {
		t.Fatalf("BeginRenewal over the tunnel: %v", err)
	}
	var began struct {
		Nonce  string `json:"nonce"`
		CSRPEM string `json:"csr_pem"`
	}
	if err := json.Unmarshal(out, &began); err != nil {
		t.Fatalf("decode BeginRenewal reply %q: %v", out, err)
	}

	// --- 2. The console signs it. The identity in the CSR must match the qube
	// it dialed; anything else is an escalation attempt, not a typo.
	csr := mustParseCSR(t, began.CSRPEM)
	if csr.Subject.CommonName != qubeCN {
		t.Fatalf("CSR asks for %q, but this connection proved %q", csr.Subject.CommonName, qubeCN)
	}
	renewedPEM := signCSRAs(t, ca, csr, 90*24*time.Hour)

	// --- 3. CompleteRenewal: verified and installed on the agent.
	body, err := json.Marshal(map[string]string{
		"nonce":    began.Nonce,
		"cert_pem": renewedPEM,
		"ca_pem":   bundle.CAPEM,
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err = cli.Call(ctx, "remote-dev", agent.ServiceCompleteRenewal, body)
	if err != nil {
		t.Fatalf("CompleteRenewal over the tunnel: %v", err)
	}
	var completed struct {
		InstalledFingerprint string    `json:"installed_fingerprint"`
		NotAfter             time.Time `json:"not_after"`
	}
	if err := json.Unmarshal(out, &completed); err != nil {
		t.Fatalf("decode CompleteRenewal reply %q: %v", out, err)
	}
	if completed.InstalledFingerprint == bundle.Fingerprint {
		t.Fatal("the agent reports the same certificate it started with")
	}
	if !completed.NotAfter.After(bundle.NotAfter) {
		t.Errorf("renewal did not extend the expiry: %s is not after %s",
			completed.NotAfter, bundle.NotAfter)
	}

	// --- 4. The tunnel the renewal ran over is still up. If installing a
	// certificate dropped it, the console could never hear the result of the
	// call it just made.
	if _, err := cli.Call(ctx, "remote-dev", agent.ServiceBeginRenewal, nil); err != nil {
		t.Fatalf("the tunnel did not survive the renewal it carried: %v", err)
	}

	// --- 5. A NEW connection is served the renewed certificate, with no
	// restart. This is the step whose absence made the certificate lifetime a
	// rebuild deadline.
	leaf := peerLeafVerified(t, addr, probeTLS)
	if got := pki.FingerprintOf(leaf); got != completed.InstalledFingerprint {
		t.Fatalf("a new connection was served %s, but the agent installed %s",
			got[:16], completed.InstalledFingerprint[:16])
	}
	if leaf.Subject.CommonName != qubeCN {
		t.Errorf("renewal changed the agent's identity to %q; the console would stop recognizing it",
			leaf.Subject.CommonName)
	}

	// And the pair on disk still loads, which is the state a restart finds.
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
		t.Fatalf("the installed pair does not load; this agent would not restart: %v", err)
	}
}

func writeFixture(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// consoleTLS mirrors service.probeTLSConfig: the agent's certificate carries no
// SAN for the address dialed and is client-auth only, so the chain is verified
// by hand against this CA and the peer held to the qube's name.
func consoleTLS(t *testing.T, b *pki.Bundle, ca *pki.CA, wantCN string) *tls.Config {
	t.Helper()
	pair, err := tls.X509KeyPair([]byte(b.CertPEM), []byte(b.KeyPEM))
	if err != nil {
		t.Fatalf("probe key pair: %v", err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca.Cert)

	verify := func(certs []*x509.Certificate) error {
		if len(certs) == 0 {
			return errors.New("agent presented no certificate")
		}
		if _, err := certs[0].Verify(x509.VerifyOptions{
			Roots:     pool,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
		}); err != nil {
			return fmt.Errorf("not signed by this console's CA: %w", err)
		}
		if certs[0].Subject.CommonName != wantCN {
			return fmt.Errorf("certificate names %q, want %q", certs[0].Subject.CommonName, wantCN)
		}
		return nil
	}

	return &tls.Config{
		Certificates:       []tls.Certificate{pair},
		RootCAs:            pool,
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true,
		VerifyConnection:   func(cs tls.ConnectionState) error { return verify(cs.PeerCertificates) },
	}
}

// peerLeafVerified opens one connection and returns the verified leaf.
func peerLeafVerified(t *testing.T, addr string, cfg *tls.Config) *x509.Certificate {
	t.Helper()
	conn, err := tls.Dial("tcp", addr, cfg.Clone())
	if err != nil {
		t.Fatalf("dial %s after renewal: %v", addr, err)
	}
	defer func() { _ = conn.Close() }()
	return conn.ConnectionState().PeerCertificates[0]
}

func mustParseCSR(t *testing.T, csrPEM string) *x509.CertificateRequest {
	t.Helper()
	block, _ := pem.Decode([]byte(csrPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		t.Fatalf("not a CSR: %q", csrPEM)
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatalf("parse CSR: %v", err)
	}
	if err := csr.CheckSignature(); err != nil {
		t.Fatalf("CSR is not signed by the key it carries: %v", err)
	}
	return csr
}

// signCSRAs stands in for the console's CSR signing, which lives on the other
// side of this change.
func signCSRAs(t *testing.T, ca *pki.CA, csr *x509.CertificateRequest, lifetime time.Duration) string {
	t.Helper()
	pub, ok := csr.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("CSR carries a %T, not an ECDSA key", csr.PublicKey)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: csr.Subject.CommonName, Organization: []string{"Qubes Air Agent"}},
		NotBefore:             time.Now().Add(-5 * time.Minute),
		NotAfter:              time.Now().Add(lifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.Cert, pub, ca.Key)
	if err != nil {
		t.Fatalf("sign CSR: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
