package service

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- test doubles -----------------------------------------------------------

// fakeAgent stands in for one qube's agent across a renewal exchange. It records
// the order of the calls, which is the property that matters most here.
type fakeAgent struct {
	mu    sync.Mutex
	calls []string

	nonce  string
	csrPEM string
	// installed is what CompleteRenewal claims to hold. Empty means "echo back
	// whatever was sent", which is what a working agent does.
	installed   string
	beginErr    error
	completeErr error
	// completeSeen is the request body the agent received.
	completeSeen completeRenewalRequest
}

func (f *fakeAgent) address() string { return "10.0.0.7:8443" }

func (f *fakeAgent) call(_ context.Context, _, service string, in []byte) ([]byte, error) {
	f.mu.Lock()
	f.calls = append(f.calls, service)
	f.mu.Unlock()

	switch service {
	case beginRenewalService:
		if f.beginErr != nil {
			return nil, f.beginErr
		}
		return json.Marshal(beginRenewalReply{Nonce: f.nonce, CSRPEM: f.csrPEM})
	case completeRenewalService:
		if f.completeErr != nil {
			return nil, f.completeErr
		}
		var req completeRenewalRequest
		if err := json.Unmarshal(in, &req); err != nil {
			return nil, err
		}
		f.mu.Lock()
		f.completeSeen = req
		f.mu.Unlock()

		fp := f.installed
		if fp == "" {
			fp = fingerprintOfPEM(req.CertPEM)
		}
		return json.Marshal(completeRenewalReply{InstalledFingerprint: fp, NotAfter: time.Now().Add(time.Hour)})
	default:
		return nil, errors.New("unexpected service " + service)
	}
}

func (f *fakeAgent) order() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// fakeSigner is a CA that will sign anything it is asked to, so the renewer's
// own identity check is what is under test rather than the CA's.
type fakeSigner struct {
	mu       sync.Mutex
	err      error
	requests []string
	wantCNs  []string
}

func (f *fakeSigner) SignAgentCSR(
	_ context.Context, csrPEM, wantCN string, lifetime time.Duration,
) (*SignedAgentCert, error) {
	f.mu.Lock()
	f.requests = append(f.requests, csrPEM)
	f.wantCNs = append(f.wantCNs, wantCN)
	f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	certPEM, notAfter := issueTestCert(wantCN, lifetime)
	return &SignedAgentCert{
		CertPEM:     certPEM,
		CAPEM:       "-----BEGIN CERTIFICATE-----\ntest-ca\n-----END CERTIFICATE-----\n",
		Fingerprint: fingerprintOfPEM(certPEM),
		NotAfter:    notAfter,
		SubjectCN:   wantCN,
	}, nil
}

func (f *fakeSigner) signed() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// fakeRegistry records certificates and hands back the ones a qube holds.
type fakeRegistry struct {
	mu         sync.Mutex
	registered []*repository.AgentCert
	byQube     map[string][]*repository.AgentCert
	err        error
	listErr    error
}

func newFakeRegistry(certs ...*repository.AgentCert) *fakeRegistry {
	r := &fakeRegistry{byQube: map[string][]*repository.AgentCert{}}
	for _, c := range certs {
		r.byQube[c.QubeID] = append(r.byQube[c.QubeID], c)
	}
	return r
}

func (r *fakeRegistry) Register(_ context.Context, c *repository.AgentCert) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.registered = append(r.registered, c)
	r.byQube[c.QubeID] = append(r.byQube[c.QubeID], c)
	return nil
}

func (r *fakeRegistry) ListByQube(_ context.Context, qubeID string) ([]*repository.AgentCert, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listErr != nil {
		return nil, r.listErr
	}
	return append([]*repository.AgentCert(nil), r.byQube[qubeID]...), nil
}

func (r *fakeRegistry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.registered)
}

// --- helpers ----------------------------------------------------------------

// makeCSR builds a real certificate request signed by a real key, so
// CheckSignature in verifyRenewalCSR is exercised rather than stubbed.
func makeCSR(t *testing.T, cn string, dnsNames ...string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: cn},
		DNSNames: dnsNames,
	}, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

// issueTestCert produces a self-signed certificate for a name.
func issueTestCert(cn string, lifetime time.Duration) (string, time.Time) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	notAfter := time.Now().Add(lifetime)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     notAfter,
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), notAfter
}

func fingerprintOfPEM(certPEM string) string {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return ""
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return ""
	}
	return repository.Fingerprint(cert)
}

func renewTestQube() *models.Qube {
	return &models.Qube{ID: "q1", Name: "dev-1", IPAddress: "10.0.0.7", Status: models.QubeStatusRunning}
}

// runExchange drives the protocol against a fake agent, skipping only the dial.
func runExchange(r *CertRenewer, agent agentCaller, qube *models.Qube) CertRenewalResult {
	return runExchangeAs(r, agent, qube, nil, "")
}

// runExchangeAs is runExchange with the two things the dial would have supplied:
// the certificate the peer authenticated with, and the one the registry says it
// currently holds. Both feed the purge guard, so a test that wants to exercise
// the guard has to be able to set them.
func runExchangeAs(
	r *CertRenewer, agent agentCaller, qube *models.Qube, peer *verifiedPeer, oldFingerprint string,
) CertRenewalResult {
	res := CertRenewalResult{
		QubeID: qube.ID, QubeName: qube.Name, At: time.Now().UTC(), OldFingerprint: oldFingerprint,
	}
	done := func(status CertRenewalStatus, format string, args ...any) CertRenewalResult {
		res.Status = status
		if format != "" {
			res.Reason = fmt.Sprintf(format, args...)
		}
		return res
	}
	return r.exchange(context.Background(), qube, agent, peer, &res, done)
}

// --- tests ------------------------------------------------------------------

// TestRenewalRefusesCSRForAnotherIdentity — the peer already proved which qube
// it is during the mTLS handshake. A CSR naming a different qube is an agent
// asking to be issued someone else's identity: it must fail loudly, and above
// all must never be silently rewritten to the name the console expected.
func TestRenewalRefusesCSRForAnotherIdentity(t *testing.T) {
	qube := renewTestQube()
	agent := &fakeAgent{nonce: "n1", csrPEM: makeCSR(t, "agent-prod-billing")}
	signer := &fakeSigner{}
	reg := newFakeRegistry()

	r := NewCertRenewer(nil, signer, reg, reg, "0.0.0.0:8443", time.Second)
	res := runExchange(r, agent, qube)

	assert.Equal(t, CertRenewalRefused, res.Status)
	assert.Contains(t, res.Reason, "agent-prod-billing")
	assert.Contains(t, res.Reason, agentCertCN(qube.Name))

	// The three things that must NOT have happened.
	assert.Zero(t, signer.signed(), "a mismatched identity must never reach the CA")
	assert.Zero(t, reg.count(), "nothing may be registered for a refused request")
	assert.Equal(t, []string{beginRenewalService}, agent.order(),
		"CompleteRenewal must not run after a refusal")
}

// TestVerifyRenewalCSR covers the identity rules directly.
func TestVerifyRenewalCSR(t *testing.T) {
	want := agentCertCN("dev-1")

	t.Run("accepts its own identity", func(t *testing.T) {
		require.NoError(t, verifyRenewalCSR(makeCSR(t, want), want))
	})

	t.Run("refuses a different common name", func(t *testing.T) {
		err := verifyRenewalCSR(makeCSR(t, agentCertCN("dev-2")), want)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "agent-dev-2")
	})

	t.Run("refuses subject alternative names", func(t *testing.T) {
		// Agent certificates carry no SANs. One in a CSR asks for a certificate
		// that hostname verification would accept somewhere the console never
		// intended — a privilege the requester does not have today.
		err := verifyRenewalCSR(makeCSR(t, want, "console.internal"), want)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "subject alternative name")
	})

	t.Run("refuses a request whose signature does not verify", func(t *testing.T) {
		// Proof of possession: without it an agent could relay a CSR captured
		// from another qube and have this console certify a key it does not hold.
		csr := makeCSR(t, want)
		block, _ := pem.Decode([]byte(csr))
		block.Bytes[len(block.Bytes)-1] ^= 0xff
		tampered := string(pem.EncodeToMemory(block))

		require.Error(t, verifyRenewalCSR(tampered, want))
	})

	t.Run("refuses something that is not a request at all", func(t *testing.T) {
		require.Error(t, verifyRenewalCSR("not pem", want))
	})
}

// TestRenewalRegistersBeforeInstalling — the ordering that stops a qube locking
// itself out. If the console died between telling the agent to install and
// writing the registry row, the agent would hold a certificate Authorize refuses
// with ErrCertNotRegistered: a successful renewal that ends in the qube being
// unable to authenticate.
func TestRenewalRegistersBeforeInstalling(t *testing.T) {
	qube := renewTestQube()
	agent := &fakeAgent{nonce: "n1", csrPEM: makeCSR(t, agentCertCN(qube.Name))}
	signer := &fakeSigner{}

	var order []string
	reg := newFakeRegistry()
	recording := &orderedRegistry{inner: reg, log: &order}
	agentLog := &orderedAgent{inner: agent, log: &order}

	r := NewCertRenewer(nil, signer, recording, reg, "0.0.0.0:8443", time.Second)
	res := runExchange(r, agentLog, qube)

	require.Equal(t, CertRenewalOK, res.Status, res.Reason)
	assert.Equal(t,
		[]string{"call:" + beginRenewalService, "register", "call:" + completeRenewalService},
		order)
}

// TestRenewalNotDeliveredWhenRegistrationFails — a certificate the registry
// refused would be rejected at the next handshake, so handing it to the agent is
// strictly worse than leaving the agent with the one that still works.
func TestRenewalNotDeliveredWhenRegistrationFails(t *testing.T) {
	qube := renewTestQube()
	agent := &fakeAgent{nonce: "n1", csrPEM: makeCSR(t, agentCertCN(qube.Name))}
	reg := newFakeRegistry()
	reg.err = errors.New("database is locked")

	r := NewCertRenewer(nil, &fakeSigner{}, reg, reg, "0.0.0.0:8443", time.Second)
	res := runExchange(r, agent, qube)

	assert.Equal(t, CertRenewalConsoleFailed, res.Status)
	assert.Equal(t, []string{beginRenewalService}, agent.order(),
		"an unregistered certificate must never be delivered")
}

// TestRenewalRejectsWrongInstalledFingerprint — an agent that reports holding
// something other than what was signed has not renewed, whatever it says. The
// next handshake is when that stops working, so it must not read as success.
func TestRenewalRejectsWrongInstalledFingerprint(t *testing.T) {
	qube := renewTestQube()
	agent := &fakeAgent{
		nonce:     "n1",
		csrPEM:    makeCSR(t, agentCertCN(qube.Name)),
		installed: strings.Repeat("a", 64),
	}
	reg := newFakeRegistry()

	r := NewCertRenewer(nil, &fakeSigner{}, reg, reg, "0.0.0.0:8443", time.Second)
	res := runExchange(r, agent, qube)

	assert.Equal(t, CertRenewalInstallFailed, res.Status)
	assert.Contains(t, res.Reason, "reports installing")

	// The registry row exists at this point, so the result has to name it. It
	// used to come back empty — exchange filled in a COPY of the result while
	// the caller returned the original — leaving an operator chasing an orphan
	// certificate with no fingerprint to chase it by, which is the one thing
	// this field is for.
	assert.NotEmpty(t, res.NewFingerprint,
		"the certificate that was registered but not installed must be named in the result")
	assert.False(t, res.NotAfter.IsZero())
}

// TestRenewalSendsCAAndNonce — the nonce is what ties the signed certificate to
// the private key the agent is holding in memory, and the CA travels with it so
// a CA rotation cannot leave the agent unable to verify the console.
func TestRenewalSendsCAAndNonce(t *testing.T) {
	qube := renewTestQube()
	agent := &fakeAgent{nonce: "nonce-42", csrPEM: makeCSR(t, agentCertCN(qube.Name))}
	reg := newFakeRegistry()

	r := NewCertRenewer(nil, &fakeSigner{}, reg, reg, "0.0.0.0:8443", time.Second)
	res := runExchange(r, agent, qube)
	require.Equal(t, CertRenewalOK, res.Status, res.Reason)

	agent.mu.Lock()
	seen := agent.completeSeen
	agent.mu.Unlock()

	assert.Equal(t, "nonce-42", seen.Nonce)
	assert.Contains(t, seen.CAPEM, "BEGIN CERTIFICATE")
	assert.NotContains(t, seen.CertPEM, "PRIVATE KEY", "no private key may ever cross the wire")
	require.Equal(t, 1, reg.count())
	assert.Equal(t, agentCertCN(qube.Name), reg.registered[0].SubjectCN)
	assert.NotNil(t, reg.registered[0].ExpiresAt, "an unexpiring registry row would never be renewed again")
}

// TestRenewalLeavesTheOldCertificateAlone — revoking on renewal would kill
// connections in flight and open a window where the agent holds nothing valid.
// The old certificate is left to expire on its own.
func TestRenewalLeavesTheOldCertificateAlone(t *testing.T) {
	qube := renewTestQube()
	old := time.Now().Add(20 * 24 * time.Hour)
	reg := newFakeRegistry(&repository.AgentCert{
		Fingerprint: "old-fp", QubeID: qube.ID, SubjectCN: agentCertCN(qube.Name),
		IssuedAt: time.Now().Add(-70 * 24 * time.Hour), ExpiresAt: &old,
	})
	agent := &fakeAgent{nonce: "n1", csrPEM: makeCSR(t, agentCertCN(qube.Name))}

	r := NewCertRenewer(nil, &fakeSigner{}, reg, reg, "0.0.0.0:8443", time.Second)

	// currentCert is read by Renew; call it the same way exchange's caller does.
	prev := r.currentCert(context.Background(), qube.ID)
	require.NotNil(t, prev)

	res := runExchange(r, agent, qube)
	require.Equal(t, CertRenewalOK, res.Status, res.Reason)

	list, err := reg.ListByQube(context.Background(), qube.ID)
	require.NoError(t, err)
	require.Len(t, list, 2)
	for _, c := range list {
		assert.False(t, c.Revoked(), "renewal must never revoke: in-flight connections would die")
	}
}

// TestRenewUnreachableWithoutAddress — no address is not a broken agent, and
// must not consume a dial. It is reported as unreachable because that is the
// honest answer to "could this qube be renewed".
func TestRenewUnreachableWithoutAddress(t *testing.T) {
	reg := newFakeRegistry()
	r := NewCertRenewer(&stubCAProvider{}, &fakeSigner{}, reg, reg, "0.0.0.0:8443", time.Second)

	res := r.Renew(context.Background(), &models.Qube{ID: "q1", Name: "dev-1"})

	assert.Equal(t, CertRenewalUnreachable, res.Status)
	assert.False(t, res.Status.AgentAnswered())
}

// TestRenewNotConfigured — a console that cannot renew has learned NOTHING
// about the agent, which is deliberately not the same as a failure. Recording it
// as a failure would blame every qube in the fleet for the console's own missing
// configuration.
func TestRenewNotConfigured(t *testing.T) {
	var r *CertRenewer
	res := r.Renew(context.Background(), renewTestQube())
	assert.Equal(t, CertRenewalNotConfigured, res.Status)
	assert.False(t, res.Status.AgentAnswered())
}

// TestNewestUsableCertPrefersLatestExpiry — a renewal that was signed and
// registered but never installed leaves a NEWER row than the certificate the
// agent actually holds. Choosing by issuance would then read the fleet as
// freshly renewed while every agent still carries the old certificate.
func TestNewestUsableCertPrefersLatestExpiry(t *testing.T) {
	now := time.Now()
	near := now.Add(24 * time.Hour)
	far := now.Add(80 * 24 * time.Hour)
	revoked := now.Add(365 * 24 * time.Hour)
	revokedAt := now

	best := newestUsableCert([]*repository.AgentCert{
		{Fingerprint: "revoked", ExpiresAt: &revoked, RevokedAt: &revokedAt},
		{Fingerprint: "near", ExpiresAt: &near},
		{Fingerprint: "far", ExpiresAt: &far},
		{Fingerprint: "no-expiry"},
	})

	require.NotNil(t, best)
	assert.Equal(t, "far", best.Fingerprint)
}

// --- ordering helpers -------------------------------------------------------

// orderedRegistry and orderedAgent record the interleaving of registry writes
// and agent calls, which is the only way to assert the ordering property.
type orderedRegistry struct {
	inner *fakeRegistry
	log   *[]string
}

func (o *orderedRegistry) Register(ctx context.Context, c *repository.AgentCert) error {
	*o.log = append(*o.log, "register")
	return o.inner.Register(ctx, c)
}

type orderedAgent struct {
	inner *fakeAgent
	log   *[]string
}

func (o *orderedAgent) address() string { return o.inner.address() }

func (o *orderedAgent) call(ctx context.Context, target, service string, in []byte) ([]byte, error) {
	*o.log = append(*o.log, "call:"+service)
	return o.inner.call(ctx, target, service, in)
}

// stubCAProvider satisfies CAProvider without ever being reachable in these
// tests; the address check fires first.
type stubCAProvider struct{}

func (stubCAProvider) CA(context.Context) (*pki.CA, error) { return nil, errors.New("no CA") }

// compile-time guard that the fakes match the production interfaces.
var (
	_ agentCaller     = (*fakeAgent)(nil)
	_ agentCaller     = (*orderedAgent)(nil)
	_ CSRSigner       = (*fakeSigner)(nil)
	_ CertRegistrar   = (*fakeRegistry)(nil)
	_ CertRegistrar   = (*orderedRegistry)(nil)
	_ AgentCertLister = (*fakeRegistry)(nil)
	_ CAProvider      = stubCAProvider{}
)

// TestSigningWorksAgainstTheRealCA — the seam no test crossed.
//
// SignAgentCSR used to reach the CA through an interface assertion whose
// declared return type (*pki.Bundle) did not match what the CA returns
// (*pki.SignedCert). The assertion therefore always failed, every renewal in
// the fleet reported "not configured", and the entire feature was dead — with a
// completely green suite, because every other test here supplies a fake signer.
//
// This test uses the REAL CA and a REAL CSR, so the join is exercised rather
// than assumed. Two independent reviewers found the gap; nothing in the suite did.
func TestSigningWorksAgainstTheRealCA(t *testing.T) {
	issuer, _, _ := issuerFixture(t)

	// A genuine request from a key the caller holds, as an agent would send.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	csrDER, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "agent-real-ca"}}, key)
	require.NoError(t, err)
	csrPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))

	signed, err := issuer.SignAgentCSR(t.Context(), csrPEM, "agent-real-ca", time.Hour)
	require.NoError(t, err, "the console must be able to sign against its own CA")
	require.NotNil(t, signed)
	assert.NotEmpty(t, signed.CertPEM)
	assert.NotEmpty(t, signed.Fingerprint)

	// The certificate must actually belong to the key that requested it,
	// otherwise the agent installs something it cannot serve.
	leaf, err := parseCertPEM(signed.CertPEM)
	require.NoError(t, err)
	assert.Equal(t, "agent-real-ca", leaf.Subject.CommonName)
	assert.True(t, leaf.PublicKey.(*ecdsa.PublicKey).Equal(&key.PublicKey),
		"the signed certificate must carry the requester's public key")
}

// TestUnconfirmedInstallIsNotRevoked — the two failure directions are NOT
// symmetric, and treating them as if they were bricks qubes.
//
// The agent installs BEFORE it replies, so the commonest way a renewal "fails"
// — a lost reply, a dropped tunnel, the exchange deadline — describes an agent
// that is ALREADY serving the new certificate. Revoking there is fatal: both the
// prober and the renewer refuse a revoked peer, and renewal runs over the very
// channel that just went away. Only a rebuild recovers it.
//
// Failing to revoke merely leaves an orphan row that makes the scheduler think
// this qube is fresh — bad, and the reason discardUninstalled exists, but
// recoverable the moment anyone looks. When the two cannot be told apart, the
// recoverable one is the only defensible choice.
func TestUnconfirmedInstallIsNotRevoked(t *testing.T) {
	certs := repository.NewAgentCertRepository(certTestDB(t))
	ctx := t.Context()

	fp := "a" + strings.Repeat("0", 63)
	expires := time.Now().Add(90 * 24 * time.Hour).UTC()
	require.NoError(t, certs.Register(ctx, &repository.AgentCert{
		Fingerprint: fp, QubeID: "q1", SubjectCN: "agent-x",
		IssuedAt: time.Now().UTC(), ExpiresAt: &expires,
	}))

	// No address, so the agent cannot be asked what it holds.
	r := &CertRenewer{revoker: certs}
	r.discardUninstalled(ctx, &models.Qube{ID: "q1", Name: "x"}, "", fp)

	got, err := certs.GetByFingerprint(ctx, fp)
	require.NoError(t, err)
	assert.False(t, got.Revoked(),
		"an unconfirmed install must be left alone; revoking one the agent DID install is unrecoverable")
}

// --- the purge guard -------------------------------------------------------

// renewalFixture wires a renewer over the REAL certificate repository, with a
// predecessor certificate already registered for the qube.
//
// Real repository on purpose. The guard being tested lives in one SQL statement;
// a fake registry that re-implemented the condition in Go would be testing the
// fake. Returned peer is what the handshake would have recorded.
func renewalFixture(t *testing.T) (
	*CertRenewer, *repository.AgentCertRepository, *models.Qube, string, *verifiedPeer,
) {
	t.Helper()
	certs := repository.NewAgentCertRepository(certTestDB(t))
	qube := renewTestQube()

	previous := "c" + strings.Repeat("2", 63)
	expires := time.Now().Add(20 * 24 * time.Hour).UTC()
	require.NoError(t, certs.Register(t.Context(), &repository.AgentCert{
		Fingerprint: previous, QubeID: qube.ID, SubjectCN: agentCertCN(qube.Name),
		IssuedAt: time.Now().Add(-70 * 24 * time.Hour).UTC(), ExpiresAt: &expires,
	}))

	r := NewCertRenewer(nil, &fakeSigner{}, certs, certs, "0.0.0.0:8443", time.Second)
	peer := &verifiedPeer{}
	peer.note(previous)
	return r, certs, qube, previous, peer
}

// TestPurgeRacingARenewalIsRefused — the failure repository.RecordRenewal was
// written for, and which nothing called it to prevent.
//
// A renewal takes seconds: dial, BeginRenewal, sign, register, CompleteRenewal.
// A purge committing anywhere inside that window has already run RevokeByQube
// and taken the qube's access away. An unconditional INSERT landing afterwards
// hands the decommissioned machine a fresh, unrevoked, ninety-day credential —
// and nothing downstream can ever flag it, because a registered unrevoked
// certificate is precisely what a legitimate agent looks like.
func TestPurgeRacingARenewalIsRefused(t *testing.T) {
	r, certs, qube, previous, peer := renewalFixture(t)
	agent := &fakeAgent{nonce: "n1", csrPEM: makeCSR(t, agentCertCN(qube.Name))}

	// The purge, landing while the renewal is in flight.
	n, err := certs.RevokeByQube(t.Context(), qube.ID, "qube purged")
	require.NoError(t, err)
	require.Equal(t, int64(1), n)

	res := runExchangeAs(r, agent, qube, peer, previous)

	assert.Equal(t, CertRenewalWithdrawn, res.Status,
		"a purge racing a renewal is not a console fault and must not be reported as one")
	assert.NotEqual(t, CertRenewalConsoleFailed, res.Status)
	assert.Contains(t, res.Reason, "withdrawn")
	assert.Contains(t, res.Reason, "not a fault")

	// The two things that must not have happened.
	assert.Equal(t, []string{beginRenewalService}, agent.order(),
		"a certificate the registry refused must never be delivered")

	list, err := certs.ListByQube(t.Context(), qube.ID)
	require.NoError(t, err)
	for _, c := range list {
		assert.True(t, c.Revoked(),
			"a purged qube must not end the renewal holding an unrevoked certificate: %s", c.Fingerprint)
	}
}

// TestRenewalIdentityMismatchIsNotReportedAsAPurge — the two sentinel errors are
// different events with different remedies. A purge racing a renewal is routine
// and self-correcting; a registry that disagrees about which qube a certificate
// belongs to is a defect that retrying cannot fix, and an operator sent to look
// for a purge would find nothing and stop looking.
func TestRenewalIdentityMismatchIsNotReportedAsAPurge(t *testing.T) {
	certs := repository.NewAgentCertRepository(certTestDB(t))
	qube := renewTestQube()

	// The predecessor is live, but the registry says it belongs to someone else.
	previous := "d" + strings.Repeat("3", 63)
	expires := time.Now().Add(20 * 24 * time.Hour).UTC()
	require.NoError(t, certs.Register(t.Context(), &repository.AgentCert{
		Fingerprint: previous, QubeID: "some-other-qube", SubjectCN: agentCertCN(qube.Name),
		IssuedAt: time.Now().UTC(), ExpiresAt: &expires,
	}))

	r := NewCertRenewer(nil, &fakeSigner{}, certs, certs, "0.0.0.0:8443", time.Second)
	peer := &verifiedPeer{}
	peer.note(previous)
	agent := &fakeAgent{nonce: "n1", csrPEM: makeCSR(t, agentCertCN(qube.Name))}

	res := runExchangeAs(r, agent, qube, peer, previous)

	require.Equal(t, CertRenewalIdentityMismatch, res.Status)
	assert.NotEqual(t, CertRenewalWithdrawn, res.Status,
		"a confused registry must not be reported as a routine purge")
	assert.Contains(t, res.Reason, "some-other-qube")
	assert.Contains(t, res.Reason, "by hand", "this one needs a human, and must say so")
	assert.Equal(t, []string{beginRenewalService}, agent.order())
}

// TestPurgeGuardChecksTheCertificateThePeerPresented — which certificate counts
// as "the one being replaced".
//
// Not the registry's best row: a previous renewal whose install failed and whose
// cleanup revocation also failed leaves an ORPHAN with a longer expiry, and
// newestUsableCert ranks that first. Keying the guard on it would check a
// credential no agent has ever held — so a purge that revoked the certificate
// the agent is actually using would sail straight through, which is the exact
// hole the guard exists to close.
func TestPurgeGuardChecksTheCertificateThePeerPresented(t *testing.T) {
	r, certs, qube, previous, peer := renewalFixture(t)

	// The orphan: longer-lived, unrevoked, never installed anywhere.
	orphan := "e" + strings.Repeat("4", 63)
	far := time.Now().Add(89 * 24 * time.Hour).UTC()
	require.NoError(t, certs.Register(t.Context(), &repository.AgentCert{
		Fingerprint: orphan, QubeID: qube.ID, SubjectCN: agentCertCN(qube.Name),
		IssuedAt: time.Now().UTC(), ExpiresAt: &far,
	}))
	require.Equal(t, orphan, r.currentCert(t.Context(), qube.ID).Fingerprint,
		"fixture check: the registry must rank the orphan first, or this test proves nothing")

	// Only the certificate the agent actually holds is revoked.
	require.NoError(t, certs.Revoke(t.Context(), previous, "key leaked"))

	agent := &fakeAgent{nonce: "n1", csrPEM: makeCSR(t, agentCertCN(qube.Name))}
	res := runExchangeAs(r, agent, qube, peer, orphan)

	assert.Equal(t, CertRenewalWithdrawn, res.Status,
		"the guard must check what the peer authenticated with, not the registry's highest-ranked row")
}

// TestRegistrationFailureIsStillAConsoleFault — the sentinel handling must not
// swallow ordinary database failures into a reassuring "withdrawn". A registry
// that will not write is this console's problem and has to say so.
func TestRegistrationFailureIsStillAConsoleFault(t *testing.T) {
	qube := renewTestQube()
	reg := newFakeRegistry()
	reg.err = errors.New("database is locked")
	agent := &fakeAgent{nonce: "n1", csrPEM: makeCSR(t, agentCertCN(qube.Name))}

	r := NewCertRenewer(nil, &fakeSigner{}, reg, reg, "0.0.0.0:8443", time.Second)
	res := runExchangeAs(r, agent, qube, nil, "old-fp")

	assert.Equal(t, CertRenewalConsoleFailed, res.Status)
	assert.Equal(t, []string{beginRenewalService}, agent.order())
}

// TestRenewalWithoutAPredecessorStillRegisters — a renewer wired without a
// conditional registry must keep working. The guard is a safety net, not a
// prerequisite; making it one would turn a missing capability into a fleet that
// cannot renew at all.
func TestRenewalWithoutAPredecessorStillRegisters(t *testing.T) {
	qube := renewTestQube()
	reg := newFakeRegistry() // no RecordRenewal
	agent := &fakeAgent{nonce: "n1", csrPEM: makeCSR(t, agentCertCN(qube.Name))}

	r := NewCertRenewer(nil, &fakeSigner{}, reg, reg, "0.0.0.0:8443", time.Second)
	res := runExchangeAs(r, agent, qube, nil, "")

	require.Equal(t, CertRenewalOK, res.Status, res.Reason)
	assert.Equal(t, 1, reg.count())
}

// --- clock skew -------------------------------------------------------------

// TestRelayCertificateToleratesRealisticSkew — whose clock decides.
//
// The AGENT verifies the console's relay certificate on the AGENT's clock. That
// is the only place a clock disagreement decides whether renewal can happen, and
// the dangerous direction is an agent running ahead: the console sees weeks of
// runway, the agent has already passed the relay certificate's notAfter, the
// handshake fails, and every sweep reports "unreachable" — which reads as a dead
// VM while the certificate quietly runs out.
//
// Verified against the real CA at a skewed time rather than asserted about a
// constant, because the property is "an agent an hour out can still be renewed",
// not "the constant is an hour".
func TestRelayCertificateToleratesRealisticSkew(t *testing.T) {
	issuer, _, _ := issuerFixture(t)
	ca, err := issuer.CA(t.Context())
	require.NoError(t, err)

	bundle, err := ca.IssueAgentCert(renewRelayName, renewRelayCertLifetime)
	require.NoError(t, err)
	leaf, err := parseCertPEM(bundle.CertPEM)
	require.NoError(t, err)

	caPEM, _, err := ca.MarshalCA()
	require.NoError(t, err)
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM([]byte(caPEM)))

	verifyAt := func(at time.Time) error {
		_, err := leaf.Verify(x509.VerifyOptions{
			Roots: roots, CurrentTime: at,
			KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		})
		return err
	}

	// An agent half an hour ahead of the console must still accept it. Under the
	// five-minute probe certificate this failed, and renewal was impossible on
	// any host whose clock had drifted more than a coffee break.
	assert.NoError(t, verifyAt(time.Now().Add(30*time.Minute)),
		"an agent running ahead of the console must still be renewable")
	assert.NoError(t, verifyAt(time.Now().Add(-caClockSkewBackdate/2)),
		"the CA's backdate must cover an agent running slightly behind")

	// And what it does NOT cover, pinned so the limit is documented by a test
	// rather than only by a comment.
	assert.Error(t, verifyAt(time.Now().Add(renewRelayCertLifetime+time.Minute)),
		"skew beyond the relay certificate's lifetime is NOT survivable and must not be claimed to be")
	assert.Error(t, verifyAt(time.Now().Add(-caClockSkewBackdate-time.Minute)),
		"an agent behind by more than the CA's backdate is NOT survivable from here")
}

// TestClockSkewHintNamesTheRealCause — a skew failure and a powered-off VM
// arrive as the same word, "unreachable". Without the hint an operator goes
// looking for a dead machine that is running perfectly.
func TestClockSkewHintNamesTheRealCause(t *testing.T) {
	assert.Empty(t, clockSkewHint(nil))
	assert.Empty(t, clockSkewHint(errors.New("connection refused")),
		"an ordinary dial failure must not be blamed on clocks")

	for _, err := range []error{
		errors.New("remote error: tls: bad certificate"),
		errors.New("x509: certificate has expired or is not yet valid: current time is after"),
		x509.CertificateInvalidError{Reason: x509.Expired},
	} {
		hint := clockSkewHint(err)
		require.NotEmpty(t, hint, "%v", err)
		assert.Contains(t, hint, "CLOCK SKEW")
		assert.Contains(t, hint, "NTP")
	}
}

// TestRevokedCertCannotRenewItself — renewal must consult the registry, not just
// the CA chain.
//
// The prober calls Authorize (rejecting revoked, expired and unregistered
// certificates); the renewer originally did not. So the same peer was refused by
// one and handed a fresh 90-day certificate by the other, in the same sweep.
//
// Failure scenario: an agent key leaks and the operator revokes that
// fingerprint. The holder answers at the qube's recorded address when the sweep
// runs, presents the revoked-but-unexpired certificate, and walks away with a
// brand-new certificate for a key of their choosing — revocation undone by the
// mechanism meant to keep credentials current.
func TestRevokedCertCannotRenewItself(t *testing.T) {
	certs := repository.NewAgentCertRepository(certTestDB(t))
	ctx := t.Context()

	fp := "b" + strings.Repeat("1", 63)
	expires := time.Now().Add(30 * 24 * time.Hour).UTC()
	require.NoError(t, certs.Register(ctx, &repository.AgentCert{
		Fingerprint: fp, QubeID: "q1", SubjectCN: "agent-x",
		IssuedAt: time.Now().UTC(), ExpiresAt: &expires,
	}))
	require.NoError(t, certs.Revoke(ctx, fp, "key leaked"))

	r := &CertRenewer{authz: certs}
	peer := &verifiedPeer{}
	authorize := r.authorizeFingerprint(ctx, peer)
	require.NotNil(t, authorize, "a renewer with a registry must authorize")

	err := authorize(fp)
	require.Error(t, err, "a revoked certificate must not be able to renew itself")
	assert.Contains(t, err.Error(), "not authorized to renew")
	assert.Empty(t, peer.presented(),
		"a refused certificate must not become the predecessor the purge guard checks against")
}
