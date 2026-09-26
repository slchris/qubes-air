package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	pinTestToken = "one-shot-secret"
	pinTestQube  = "remote-a"
)

func TestBootstrapPlaceholderKeyIsDeterministicAndScoped(t *testing.T) {
	first, err := BootstrapPlaceholderPrivateKey(pinTestToken, pinTestQube)
	require.NoError(t, err)
	second, err := BootstrapPlaceholderPrivateKey(pinTestToken, pinTestQube)
	require.NoError(t, err)
	require.Equal(t, first, second, "both sides must derive the same key from the same token")

	otherToken, err := BootstrapPlaceholderPrivateKey("different-secret", pinTestQube)
	require.NoError(t, err)
	require.NotEqual(t, first, otherToken)
	otherName, err := BootstrapPlaceholderPrivateKey(pinTestToken, "remote-b")
	require.NoError(t, err)
	require.NotEqual(t, first, otherName, "a token must not pin the same key for two qubes")
}

func TestBootstrapPlaceholderRejectsMissingInputs(t *testing.T) {
	for _, tc := range []struct{ token, name string }{{"", "remote"}, {"token", ""}, {"", ""}} {
		_, err := BootstrapPlaceholderPrivateKey(tc.token, tc.name)
		require.Error(t, err)
		_, err = BootstrapPlaceholderSPKIFingerprint(tc.token, tc.name)
		require.Error(t, err)
		_, err = NewBootstrapPlaceholderCertificate(tc.token, tc.name, time.Now(), time.Hour)
		require.Error(t, err)
	}
	_, err := NewBootstrapPlaceholderCertificate(pinTestToken, pinTestQube, time.Now(), 0)
	require.ErrorContains(t, err, "positive lifetime")
}

// The pin the console stores at issue time must be exactly the SPKI digest of
// the certificate the agent later mints from the same token.
func TestBootstrapPinMatchesTheMintedPlaceholder(t *testing.T) {
	pin, err := BootstrapPlaceholderSPKIFingerprint(pinTestToken, pinTestQube)
	require.NoError(t, err)
	require.Len(t, pin, 64)

	cert, err := NewBootstrapPlaceholderCertificate(pinTestToken, pinTestQube, time.Now(), time.Hour)
	require.NoError(t, err)
	sum := sha256.Sum256(cert.Leaf.RawSubjectPublicKeyInfo)
	assert.Equal(t, hex.EncodeToString(sum[:]), pin)
	assert.Equal(t, BootstrapPlaceholderCommonName(pinTestQube), cert.Leaf.Subject.CommonName)
	role, err := RoleOf(cert.Leaf)
	require.NoError(t, err)
	assert.Equal(t, RoleAgent, role)

	require.NoError(t, VerifyBootstrapPlaceholder([]*x509.Certificate{cert.Leaf}, pinTestQube, pin))
}

// placeholderSpec describes one variant of a peer certificate. The zero value
// of each field means "as the genuine agent mints it".
type placeholderSpec struct {
	cn        string
	notBefore time.Time
	notAfter  time.Time
	usage     x509.KeyUsage
	extUsage  []x509.ExtKeyUsage
	roles     []string
	issuerCN  string        // non-empty: signed "by" a parent with this subject
	signer    crypto.Signer // non-nil: signed by this key instead of the derived one
}

func mintVariant(t *testing.T, spec placeholderSpec) *x509.Certificate {
	t.Helper()
	key, err := BootstrapPlaceholderPrivateKey(pinTestToken, pinTestQube)
	require.NoError(t, err)
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(7),
		Subject:      pkix.Name{CommonName: BootstrapPlaceholderCommonName(pinTestQube)},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if spec.cn != "" {
		tmpl.Subject.CommonName = spec.cn
	}
	if !spec.notBefore.IsZero() {
		tmpl.NotBefore, tmpl.NotAfter = spec.notBefore, spec.notAfter
	}
	if spec.usage != 0 {
		tmpl.KeyUsage = spec.usage
	}
	if spec.extUsage != nil {
		tmpl.ExtKeyUsage = spec.extUsage
	}
	roles := spec.roles
	if roles == nil {
		roles = []string{"agent"}
	}
	for _, r := range roles {
		u, err := url.Parse("spiffe://qubes-air/role/" + r)
		require.NoError(t, err)
		tmpl.URIs = append(tmpl.URIs, u)
	}
	parent := tmpl
	if spec.issuerCN != "" {
		parent = &x509.Certificate{Subject: pkix.Name{CommonName: spec.issuerCN}}
	}
	var signer crypto.Signer = key
	if spec.signer != nil {
		signer = spec.signer
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, key.Public(), signer)
	require.NoError(t, err)
	leaf, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return leaf
}

// Every refusal the console relies on, one per row. The genuine row proves the
// harness itself mints an acceptable peer, so each negative row fails for its
// own reason and not because the fixture is broken.
func TestVerifyBootstrapPlaceholderRefusesEveryWrongPeer(t *testing.T) {
	pin, err := BootstrapPlaceholderSPKIFingerprint(pinTestToken, pinTestQube)
	require.NoError(t, err)
	attackerPin, err := BootstrapPlaceholderSPKIFingerprint("attacker-token", pinTestQube)
	require.NoError(t, err)
	attacker, err := NewBootstrapPlaceholderCertificate("attacker-token", pinTestQube, time.Now(), time.Hour)
	require.NoError(t, err)
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	now := time.Now()
	// What an agent built before pinning serves: a fresh random key, the right
	// CN, no role.
	legacyTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(9),
		Subject:      pkix.Name{CommonName: BootstrapPlaceholderCommonName(pinTestQube)},
		NotBefore:    now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	legacyDER, err := x509.CreateCertificate(rand.Reader, legacyTmpl, legacyTmpl, &otherKey.PublicKey, otherKey)
	require.NoError(t, err)
	legacy, err := x509.ParseCertificate(legacyDER)
	require.NoError(t, err)

	tests := []struct {
		name  string
		certs []*x509.Certificate
		qube  string
		pin   string
		want  string
	}{
		{name: "genuine placeholder", certs: []*x509.Certificate{mintVariant(t, placeholderSpec{})}, qube: pinTestQube, pin: pin},
		{name: "no certificate", qube: pinTestQube, pin: pin, want: "did not present"},
		{name: "nil leaf", certs: []*x509.Certificate{nil}, qube: pinTestQube, pin: pin, want: "did not present"},
		{name: "placeholder minted from another token", certs: []*x509.Certificate{attacker.Leaf}, qube: pinTestQube, pin: pin, want: "does not match the pin"},
		{name: "placeholder from an agent that predates pinning", certs: []*x509.Certificate{legacy}, qube: pinTestQube, pin: pin, want: "does not match the pin"},
		{name: "genuine placeholder against another token's pin", certs: []*x509.Certificate{mintVariant(t, placeholderSpec{})}, qube: pinTestQube, pin: attackerPin, want: "does not match the pin"},
		{name: "wrong target", certs: []*x509.Certificate{mintVariant(t, placeholderSpec{})}, qube: "remote-b", pin: pin, want: "should be serving"},
		{name: "agent CN instead of placeholder CN", certs: []*x509.Certificate{mintVariant(t, placeholderSpec{cn: AgentCommonName(pinTestQube)})}, qube: pinTestQube, pin: pin, want: "should be serving"},
		{name: "missing pin", certs: []*x509.Certificate{mintVariant(t, placeholderSpec{})}, qube: pinTestQube, want: "pin is missing"},
		{name: "pin not hex", certs: []*x509.Certificate{mintVariant(t, placeholderSpec{})}, qube: pinTestQube, pin: strings.Repeat("zz", 32), want: "not a SHA-256"},
		{name: "pin too short", certs: []*x509.Certificate{mintVariant(t, placeholderSpec{})}, qube: pinTestQube, pin: pin[:62], want: "not a SHA-256"},
		{name: "missing qube name", certs: []*x509.Certificate{mintVariant(t, placeholderSpec{})}, pin: pin, want: "no qube name"},
		{name: "signed by another CA", certs: []*x509.Certificate{mintVariant(t, placeholderSpec{issuerCN: "attacker-ca", signer: otherKey})}, qube: pinTestQube, pin: pin, want: "not self-signed"},
		{name: "self-named but signed by another key", certs: []*x509.Certificate{mintVariant(t, placeholderSpec{signer: otherKey})}, qube: pinTestQube, pin: pin, want: "not self-signed"},
		{name: "console role", certs: []*x509.Certificate{mintVariant(t, placeholderSpec{roles: []string{"console"}})}, qube: pinTestQube, pin: pin, want: "agent role"},
		{name: "no role", certs: []*x509.Certificate{mintVariant(t, placeholderSpec{roles: []string{}})}, qube: pinTestQube, pin: pin, want: "agent role"},
		{name: "two roles", certs: []*x509.Certificate{mintVariant(t, placeholderSpec{roles: []string{"agent", "console"}})}, qube: pinTestQube, pin: pin, want: "agent role"},
		{name: "key encipherment usage", certs: []*x509.Certificate{mintVariant(t, placeholderSpec{usage: x509.KeyUsageKeyEncipherment})}, qube: pinTestQube, pin: pin, want: "server authentication"},
		{name: "extra key usage", certs: []*x509.Certificate{mintVariant(t, placeholderSpec{usage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign})}, qube: pinTestQube, pin: pin, want: "server authentication"},
		{name: "client-auth only", certs: []*x509.Certificate{mintVariant(t, placeholderSpec{extUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})}, qube: pinTestQube, pin: pin, want: "server authentication"},
		{name: "server and client auth", certs: []*x509.Certificate{mintVariant(t, placeholderSpec{extUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}})}, qube: pinTestQube, pin: pin, want: "server authentication"},
		{name: "expired", certs: []*x509.Certificate{mintVariant(t, placeholderSpec{notBefore: now.Add(-2 * time.Hour), notAfter: now.Add(-time.Hour)})}, qube: pinTestQube, pin: pin, want: "outside its validity"},
		{name: "not yet valid", certs: []*x509.Certificate{mintVariant(t, placeholderSpec{notBefore: now.Add(time.Hour), notAfter: now.Add(2 * time.Hour)})}, qube: pinTestQube, pin: pin, want: "outside its validity"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := VerifyBootstrapPlaceholder(tc.certs, tc.qube, tc.pin)
			if tc.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func consoleBootstrapPair(t *testing.T) tls.Certificate {
	t.Helper()
	ca, err := NewCA("pin-test-ca", 0)
	require.NoError(t, err)
	bundle, err := ca.IssueAgentCert("console-bootstrap", time.Hour)
	require.NoError(t, err)
	pair, err := tls.X509KeyPair([]byte(bundle.CertPEM), []byte(bundle.KeyPEM))
	require.NoError(t, err)
	return pair
}

func TestBootstrapDialTLSConfigRefusesInvalidInput(t *testing.T) {
	pair := consoleBootstrapPair(t)
	pin, err := BootstrapPlaceholderSPKIFingerprint(pinTestToken, pinTestQube)
	require.NoError(t, err)

	_, err = BootstrapDialTLSConfig(tls.Certificate{}, pinTestQube, pin)
	require.ErrorContains(t, err, "no client certificate")
	_, err = BootstrapDialTLSConfig(pair, "", pin)
	require.ErrorContains(t, err, "no qube name")
	_, err = BootstrapDialTLSConfig(pair, pinTestQube, "")
	require.ErrorContains(t, err, "pin is missing")
	_, err = BootstrapDialTLSConfig(pair, pinTestQube, "not-a-pin")
	require.ErrorContains(t, err, "not a SHA-256")

	cfg, err := BootstrapDialTLSConfig(pair, pinTestQube, pin)
	require.NoError(t, err)
	assert.Equal(t, uint16(tls.VersionTLS13), cfg.MinVersion)
	require.True(t, cfg.InsecureSkipVerify, "the self-signed placeholder needs the pin verifier in place of the stack's")
	require.NotNil(t, cfg.VerifyConnection, "InsecureSkipVerify without VerifyConnection would accept any peer")
	require.ErrorContains(t, cfg.VerifyConnection(tls.ConnectionState{}), "did not present")
}

// handshakeAgainst runs a real TLS 1.3 handshake between a client built by
// BootstrapDialTLSConfig and a server presenting serverCert, and reports the
// client's error plus whether the server ever read application data.
func handshakeAgainst(t *testing.T, serverCert *tls.Certificate, pin string) (clientErr error, serverGotData bool) {
	t.Helper()
	cfg, err := BootstrapDialTLSConfig(consoleBootstrapPair(t), pinTestQube, pin)
	require.NoError(t, err)

	clientConn, serverConn := net.Pipe()
	server := tls.Server(serverConn, &tls.Config{Certificates: []tls.Certificate{*serverCert}, MinVersion: tls.VersionTLS13})
	got := make(chan bool, 1)
	go func() {
		defer serverConn.Close()
		_ = server.SetDeadline(time.Now().Add(5 * time.Second))
		if server.Handshake() != nil {
			got <- false
			return
		}
		var buf [16]byte
		n, _ := server.Read(buf[:])
		got <- n > 0
	}()

	client := tls.Client(clientConn, cfg)
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	clientErr = client.Handshake()
	if clientErr == nil {
		_, clientErr = client.Write([]byte("begin"))
	}
	_ = client.Close()
	return clientErr, <-got
}

func TestBootstrapDialTLSConfigHandshake(t *testing.T) {
	pin, err := BootstrapPlaceholderSPKIFingerprint(pinTestToken, pinTestQube)
	require.NoError(t, err)

	genuine, err := NewBootstrapPlaceholderCertificate(pinTestToken, pinTestQube, time.Now(), time.Hour)
	require.NoError(t, err)
	clientErr, served := handshakeAgainst(t, genuine, pin)
	require.NoError(t, clientErr)
	assert.True(t, served, "a genuine pending agent must receive the console's request")

	impostor, err := NewBootstrapPlaceholderCertificate("attacker-token", pinTestQube, time.Now(), time.Hour)
	require.NoError(t, err)
	clientErr, served = handshakeAgainst(t, impostor, pin)
	require.ErrorContains(t, clientErr, "does not match the pin")
	assert.False(t, served, "an impostor must not receive a single byte of the bootstrap request")
}
