package grpc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"testing"
	"time"
)

// vaultQrexec is a fake qrexecClient that returns different PEMs per credential
// name, simulating vault-cloud's qubesair.GetCredential+<name>.
type vaultQrexec struct {
	byService map[string][]byte
	err       error
	calls     []string
}

func (v *vaultQrexec) Call(_ context.Context, target, service string, _ []byte) ([]byte, error) {
	v.calls = append(v.calls, target+"|"+service)
	if v.err != nil {
		return nil, v.err
	}
	if out, ok := v.byService[service]; ok {
		return out, nil
	}
	return nil, errors.New("no such credential")
}

func TestFetchClientMTLS(t *testing.T) {
	caPEM, _, _ := genCAPEM(t)
	certPEM, keyPEM := genLeafPEM(t)

	fq := &vaultQrexec{byService: map[string][]byte{
		"qubesair.GetCredential+relay-cert": certPEM,
		"qubesair.GetCredential+relay-key":  keyPEM,
		"qubesair.GetCredential+relay-ca":   caPEM,
	}}

	tlsCfg, err := fetchClientMTLSWith(context.Background(), VaultCertConfig{
		CertName:   "relay-cert",
		KeyName:    "relay-key",
		CAName:     "relay-ca",
		ServerName: "remote-relay",
	}, fq)
	if err != nil {
		t.Fatalf("FetchClientMTLS err: %v", err)
	}
	if len(tlsCfg.Certificates) != 1 {
		t.Fatalf("got %d certs, want 1", len(tlsCfg.Certificates))
	}
	if tlsCfg.RootCAs == nil {
		t.Error("RootCAs not set from CA credential")
	}
	if tlsCfg.ServerName != "remote-relay" {
		t.Errorf("ServerName = %q", tlsCfg.ServerName)
	}
	// default vault qube is vault-cloud
	for _, c := range fq.calls {
		if want := "vault-cloud|"; len(c) < len(want) || c[:len(want)] != want {
			t.Errorf("call %q not to vault-cloud", c)
		}
	}
}

func TestFetchClientMTLSRequiresCertAndKey(t *testing.T) {
	fq := &vaultQrexec{byService: map[string][]byte{}}
	if _, err := fetchClientMTLSWith(context.Background(), VaultCertConfig{KeyName: "k"}, fq); err == nil {
		t.Error("expected error when cert_name missing")
	}
	if _, err := fetchClientMTLSWith(context.Background(), VaultCertConfig{CertName: "c"}, fq); err == nil {
		t.Error("expected error when key_name missing")
	}
}

func TestFetchClientMTLSPropagatesVaultError(t *testing.T) {
	fq := &vaultQrexec{err: errors.New("vault ask denied")}
	_, err := fetchClientMTLSWith(context.Background(), VaultCertConfig{CertName: "c", KeyName: "k"}, fq)
	if err == nil {
		t.Error("expected error when vault call fails")
	}
}

// --- helpers: minimal CA + leaf PEMs (self-contained) ---

func genCAPEM(t *testing.T) (caPEM []byte, caCert *x509.Certificate, caKey *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), cert, key
}

func genLeafPEM(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "sys-relay"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalECPrivateKey(key)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}
