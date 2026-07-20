package service

import (
	"context"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// memCredStore is an in-memory CredentialStore.
type memCredStore struct {
	creds   map[string]models.Credential
	secrets map[string]string
	failOn  string
}

func newMemCredStore() *memCredStore {
	return &memCredStore{creds: map[string]models.Credential{}, secrets: map[string]string{}}
}

func (m *memCredStore) List(context.Context) ([]models.Credential, error) {
	out := make([]models.Credential, 0, len(m.creds))
	for _, c := range m.creds {
		out = append(out, c)
	}
	return out, nil
}

func (m *memCredStore) GetSecret(_ context.Context, id string) (string, error) {
	s, ok := m.secrets[id]
	if !ok {
		return "", errors.New("not found")
	}
	return s, nil
}

func (m *memCredStore) Create(_ context.Context, req models.CredentialCreateRequest) (*models.Credential, error) {
	if m.failOn != "" && req.Name == m.failOn {
		return nil, errors.New("simulated storage failure")
	}
	c := models.Credential{ID: req.Name, Name: req.Name, Type: req.Type, Description: req.Description}
	m.creds[c.ID] = c
	m.secrets[c.ID] = req.SecretValue
	return &c, nil
}

// closedDB returns a database that has been closed, so writes fail the way an
// unavailable database does rather than panicking as a nil handle would.
func closedDB(t *testing.T) *database.DB {
	t.Helper()
	db := certTestDB(t)
	require.NoError(t, db.Close())
	return db
}

// certTestDB creates a throwaway database for the certificate registry.
func certTestDB(t *testing.T) *database.DB {
	t.Helper()
	f, err := os.CreateTemp("", "certs-test-*.db")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	cfg := database.DefaultConfig()
	cfg.DSN = f.Name()
	db, err := database.New(cfg)
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = db.Close()
		_ = os.Remove(f.Name())
	})
	return db
}

// issuerRig is everything a test needs to observe what IssueFor actually did:
// the token table it minted into, and the directory it rendered into.
type issuerRig struct {
	issuer *CertIssuer
	certs  *repository.AgentCertRepository
	tokens *repository.BootstrapTokenRepository
	store  *memCredStore
	dir    string
}

// deliveredDoc returns the cloud-init document IssueFor wrote for a qube.
func (r issuerRig) deliveredDoc(t *testing.T, qubeName string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.dir, SnippetFileName(qubeName)))
	require.NoError(t, err, "IssueFor rendered no delivery document")
	return string(b)
}

func issuerFixture(t *testing.T) (*CertIssuer, *repository.AgentCertRepository, *memCredStore) {
	t.Helper()
	r := newIssuerRig(t)
	return r.issuer, r.certs, r.store
}

func newIssuerRig(t *testing.T) issuerRig {
	t.Helper()
	db := certTestDB(t)
	certs := repository.NewAgentCertRepository(db)
	tokens := repository.NewBootstrapTokenRepository(db)
	store := newMemCredStore()
	dir := t.TempDir()
	issuer := NewCertIssuer(store, certs, dir, "0.0.0.0:8443", testAgentPackage()).
		WithBootstrapTokens(tokens, 0)
	return issuerRig{issuer: issuer, certs: certs, tokens: tokens, store: store, dir: dir}
}

func testQube(name string) *models.Qube {
	return &models.Qube{ID: "q-" + name, Name: name, Status: models.QubeStatusPending}
}

// The property this whole redesign exists for: the document delivered to a
// guest contains NO private key.
//
// It used to contain one, and on Proxmox cloud-init data is readable through
// the API by anyone holding VM.Config.Cloudinit — which made that permission
// equivalent to holding every agent identity ever delivered. This test is the
// thing that fails if anyone puts it back.
func TestDeliveredDocumentCarriesNoPrivateKey(t *testing.T) {
	r := newIssuerRig(t)
	require.NoError(t, r.issuer.IssueFor(context.Background(), testQube("dev-work")))

	doc := r.deliveredDoc(t, "dev-work")
	for _, marker := range []string{"PRIVATE KEY", "agent-key.pem", "key_pem"} {
		assert.NotContains(t, doc, marker,
			"the delivery document carries key material again; that is the hole the token design closed")
	}
	assert.Contains(t, doc, "/etc/qubes-air/ca.pem", "the agent needs the CA to verify the console")
	assert.Contains(t, doc, "/etc/qubes-air/bootstrap-token", "the agent needs a token to be issued anything")
}

// The token in the delivered document must be the one the console will accept,
// for this qube. A token that does not redeem is a qube that can never obtain
// an identity, and nothing before the console dials it would say so.
func TestDeliveredTokenRedeemsForItsOwnQube(t *testing.T) {
	r := newIssuerRig(t)
	ctx := context.Background()
	require.NoError(t, r.issuer.IssueFor(ctx, testQube("dev-work")))

	tok, err := r.tokens.Redeem(ctx, tokenFromDoc(t, r.deliveredDoc(t, "dev-work")), time.Now())
	require.NoError(t, err, "the token handed to the guest was not redeemable")
	assert.Equal(t, "q-dev-work", tok.QubeID)
	assert.Equal(t, "dev-work", tok.QubeName)
}

// A qube is created with NO certificate, on purpose — bootstrap is what issues
// one. Registering a certificate here would make BootstrapMonitor skip the qube
// forever, and it would sit there uncertified with the registry claiming
// otherwise.
func TestIssueForRegistersNoCertificate(t *testing.T) {
	r := newIssuerRig(t)
	ctx := context.Background()
	require.NoError(t, r.issuer.IssueFor(ctx, testQube("dev-work")))

	list, err := r.certs.ListByQube(ctx, "q-dev-work")
	require.NoError(t, err)
	assert.Empty(t, list, "a certificate was registered before the agent ever proved it holds the key")
}

// Re-provisioning must not leave the previous token usable. Two live tokens for
// one qube are two ways to claim its identity, which is exactly what single-use
// exists to prevent.
func TestReissuingInvalidatesThePreviousToken(t *testing.T) {
	r := newIssuerRig(t)
	ctx := context.Background()

	require.NoError(t, r.issuer.IssueFor(ctx, testQube("dev-work")))
	first := tokenFromDoc(t, r.deliveredDoc(t, "dev-work"))

	require.NoError(t, r.issuer.IssueFor(ctx, testQube("dev-work")))
	second := tokenFromDoc(t, r.deliveredDoc(t, "dev-work"))
	require.NotEqual(t, first, second, "re-issuing handed out the same token")

	_, err := r.tokens.Redeem(ctx, first, time.Now())
	require.ErrorIs(t, err, repository.ErrBootstrapTokenRejected,
		"the superseded token still redeems; the qube has two live identities-in-waiting")

	_, err = r.tokens.Redeem(ctx, second, time.Now())
	assert.NoError(t, err, "the current token must still work")
}

// Without a token store a qube would be provisioned with a document that can
// never yield an identity. Refusing beats rendering something that looks fine.
func TestIssueForRefusesWithoutATokenStore(t *testing.T) {
	certs := repository.NewAgentCertRepository(certTestDB(t))
	issuer := NewCertIssuer(newMemCredStore(), certs, t.TempDir(), "0.0.0.0:8443", testAgentPackage())

	err := issuer.IssueFor(context.Background(), testQube("dev-work"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bootstrap token store")
}

// TestBundleVerifiesAgainstItsOwnCA — the agent is handed a CA it can actually
// parse and use to verify its peer.
func TestDeliveredCAIsUsable(t *testing.T) {
	r := newIssuerRig(t)
	require.NoError(t, r.issuer.IssueFor(context.Background(), testQube("dev-work")))

	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM([]byte(caFromDoc(t, r.deliveredDoc(t, "dev-work")))),
		"the delivered CA does not parse; the agent would trust nothing")
}

// TestCAIsReusedAcrossIssuances — two agents must trust the same root, or they
// cannot talk to the same console. A CA minted per call would look fine in
// isolation and fail the moment a second agent appeared.
func TestCAIsReusedAcrossIssuances(t *testing.T) {
	r := newIssuerRig(t)
	ctx := context.Background()

	require.NoError(t, r.issuer.IssueFor(ctx, testQube("alpha")))
	require.NoError(t, r.issuer.IssueFor(ctx, testQube("beta")))

	assert.Equal(t, caFromDoc(t, r.deliveredDoc(t, "alpha")), caFromDoc(t, r.deliveredDoc(t, "beta")),
		"all agents must share one root of trust")
	assert.NotEqual(t, tokenFromDoc(t, r.deliveredDoc(t, "alpha")), tokenFromDoc(t, r.deliveredDoc(t, "beta")),
		"but each gets its own credential")
}

// TestCASurvivesRestart — a new issuer over the same credential store must load
// the stored CA rather than mint a fresh one, which would invalidate every
// certificate already in the field.
func TestCASurvivesRestart(t *testing.T) {
	db := certTestDB(t)
	certs := repository.NewAgentCertRepository(db)
	tokens := repository.NewBootstrapTokenRepository(db)
	store := newMemCredStore()
	ctx := context.Background()

	dirA, dirB := t.TempDir(), t.TempDir()
	require.NoError(t, NewCertIssuer(store, certs, dirA, "0.0.0.0:8443", testAgentPackage()).
		WithBootstrapTokens(tokens, 0).IssueFor(ctx, testQube("alpha")))
	// A completely new issuer, as after a process restart.
	require.NoError(t, NewCertIssuer(store, certs, dirB, "0.0.0.0:8443", testAgentPackage()).
		WithBootstrapTokens(tokens, 0).IssueFor(ctx, testQube("beta")))

	readDoc := func(dir, name string) string {
		b, err := os.ReadFile(filepath.Join(dir, SnippetFileName(name)))
		require.NoError(t, err)
		return string(b)
	}
	assert.Equal(t, caFromDoc(t, readDoc(dirA, "alpha")), caFromDoc(t, readDoc(dirB, "beta")),
		"a restart must not mint a new CA; that would invalidate every issued certificate")
}

// TestHalfPresentCARefuses — one half of the CA missing means a partial write
// or a partial deletion. Minting a replacement would silently invalidate every
// certificate the missing half signed, so it must fail loudly instead.
func TestHalfPresentCARefuses(t *testing.T) {
	r := newIssuerRig(t)
	ctx := context.Background()
	require.NoError(t, r.issuer.IssueFor(ctx, testQube("alpha")))

	// Delete only the key, and use a fresh issuer so the cached CA is gone.
	delete(r.store.creds, caKeyCredentialName)
	delete(r.store.secrets, caKeyCredentialName)

	db := certTestDB(t)
	fresh := NewCertIssuer(r.store, repository.NewAgentCertRepository(db), t.TempDir(), "0.0.0.0:8443", testAgentPackage()).
		WithBootstrapTokens(repository.NewBootstrapTokenRepository(db), 0)
	err := fresh.IssueFor(ctx, testQube("beta"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "half-present",
		"a partial CA must be refused, not silently replaced")
}

// TestRevokeForRemovesAccess — purging a qube must take its agent's credential
// with it. The certificate is registered directly here because IssueFor no
// longer creates one; bootstrap does.
func TestRevokeForRemovesAccess(t *testing.T) {
	r := newIssuerRig(t)
	ctx := context.Background()

	expires := time.Now().Add(time.Hour)
	require.NoError(t, r.certs.Register(ctx, &repository.AgentCert{
		Fingerprint: "fp-doomed", QubeID: "q-doomed",
		SubjectCN: "agent-doomed", IssuedAt: time.Now().UTC(), ExpiresAt: &expires,
	}))
	_, err := r.certs.Authorize(ctx, "fp-doomed")
	require.NoError(t, err)

	require.NoError(t, r.issuer.RevokeFor(ctx, "q-doomed", "qube purged"))

	_, err = r.certs.Authorize(ctx, "fp-doomed")
	assert.ErrorIs(t, err, repository.ErrCertRevoked)
}

// tokenFromDoc extracts the bootstrap token from a rendered cloud-init
// document, the way the guest's write_files stanza delivers it.
func tokenFromDoc(t *testing.T, doc string) string {
	t.Helper()
	return valueUnderWriteFile(t, doc, "/etc/qubes-air/bootstrap-token")
}

// caFromDoc extracts the delivered CA PEM.
func caFromDoc(t *testing.T, doc string) string {
	t.Helper()
	return valueUnderWriteFile(t, doc, "/etc/qubes-air/ca.pem")
}

// valueUnderWriteFile pulls the indented literal block cloud-init writes for a
// path. Deliberately parses the RENDERED document rather than trusting an
// accessor, so a change that stops delivering something is visible here.
func valueUnderWriteFile(t *testing.T, doc, path string) string {
	t.Helper()
	lines := strings.Split(doc, "\n")
	for i, ln := range lines {
		if !strings.Contains(ln, "path: "+path) {
			continue
		}
		for j := i; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) != "content: |" {
				continue
			}
			var body []string
			for k := j + 1; k < len(lines); k++ {
				if lines[k] != "" && !strings.HasPrefix(lines[k], "      ") {
					break
				}
				body = append(body, strings.TrimPrefix(lines[k], "      "))
			}
			return strings.TrimSpace(strings.Join(body, "\n"))
		}
	}
	t.Fatalf("no write_files entry for %s in the rendered document", path)
	return ""
}

// TestCAKeyStoredWithAWarning — the stored description is what an operator sees
// when browsing credentials, and this one deserves to be alarming.
func TestCAKeyStoredWithAWarning(t *testing.T) {
	issuer, _, store := issuerFixture(t)
	require.NoError(t, issuer.IssueFor(context.Background(), testQube("alpha")))

	key, ok := store.creds[caKeyCredentialName]
	require.True(t, ok, "the CA key must be stored in the credential store, not on disk")
	assert.Contains(t, strings.ToLower(key.Description), "private key")
	assert.Contains(t, strings.ToLower(key.Description), "mint")
}
