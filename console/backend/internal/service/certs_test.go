package service

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"strings"
	"testing"

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

func issuerFixture(t *testing.T) (*CertIssuer, *repository.AgentCertRepository, *memCredStore) {
	t.Helper()
	certs := repository.NewAgentCertRepository(certTestDB(t))
	store := newMemCredStore()
	return NewCertIssuer(store, certs), certs, store
}

func testQube(name string) *models.Qube {
	return &models.Qube{ID: "q-" + name, Name: name, Status: models.QubeStatusPending}
}

// TestIssuedCertIsImmediatelyAuthorized is the property the whole chain rests
// on: a certificate the CA signs must be one the registry accepts. If issuance
// and registration ever drift apart, every agent connects with a valid-looking
// certificate and is refused as unregistered.
func TestIssuedCertIsImmediatelyAuthorized(t *testing.T) {
	issuer, certs, _ := issuerFixture(t)
	ctx := context.Background()

	bundle, err := issuer.IssueFor(ctx, testQube("dev-work"))
	require.NoError(t, err)

	got, err := certs.Authorize(ctx, bundle.Fingerprint)
	require.NoError(t, err, "a freshly issued certificate must be authorized")
	assert.Equal(t, "q-dev-work", got.QubeID)
	assert.Equal(t, "agent-dev-work", got.SubjectCN)
}

// TestBundleVerifiesAgainstItsOwnCA — the agent is handed a CA it can actually
// verify its peer with.
func TestBundleVerifiesAgainstItsOwnCA(t *testing.T) {
	issuer, _, _ := issuerFixture(t)
	bundle, err := issuer.IssueFor(context.Background(), testQube("dev-work"))
	require.NoError(t, err)

	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM([]byte(bundle.CAPEM)))

	block, _ := pem.Decode([]byte(bundle.CertPEM))
	require.NotNil(t, block)
	leaf, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)

	_, err = leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	assert.NoError(t, err)
}

// TestCAIsReusedAcrossIssuances — two agents must trust the same root, or they
// cannot talk to the same relay. A CA minted per call would look fine in
// isolation and fail the moment a second agent appeared.
func TestCAIsReusedAcrossIssuances(t *testing.T) {
	issuer, _, _ := issuerFixture(t)
	ctx := context.Background()

	a, err := issuer.IssueFor(ctx, testQube("alpha"))
	require.NoError(t, err)
	b, err := issuer.IssueFor(ctx, testQube("beta"))
	require.NoError(t, err)

	assert.Equal(t, a.CAPEM, b.CAPEM, "all agents must share one root of trust")
	assert.NotEqual(t, a.Fingerprint, b.Fingerprint, "but each gets its own identity")
}

// TestCASurvivesRestart — a new issuer over the same credential store must load
// the stored CA rather than mint a fresh one, which would invalidate every
// certificate already in the field.
func TestCASurvivesRestart(t *testing.T) {
	certs := repository.NewAgentCertRepository(certTestDB(t))
	store := newMemCredStore()
	ctx := context.Background()

	first, err := NewCertIssuer(store, certs).IssueFor(ctx, testQube("alpha"))
	require.NoError(t, err)

	// A completely new issuer, as after a process restart.
	second, err := NewCertIssuer(store, certs).IssueFor(ctx, testQube("beta"))
	require.NoError(t, err)

	assert.Equal(t, first.CAPEM, second.CAPEM,
		"a restart must not mint a new CA; that would invalidate every issued certificate")
}

// TestHalfPresentCARefuses — one half of the CA missing means a partial write
// or a partial deletion. Minting a replacement would silently invalidate every
// certificate the missing half signed, so it must fail loudly instead.
func TestHalfPresentCARefuses(t *testing.T) {
	issuer, _, store := issuerFixture(t)
	ctx := context.Background()

	_, err := issuer.IssueFor(ctx, testQube("alpha"))
	require.NoError(t, err)

	// Delete only the key, and use a fresh issuer so the cached CA is gone.
	delete(store.creds, caKeyCredentialName)
	delete(store.secrets, caKeyCredentialName)

	fresh := NewCertIssuer(store, repository.NewAgentCertRepository(certTestDB(t)))
	_, err = fresh.IssueFor(ctx, testQube("beta"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "half-present",
		"a partial CA must be refused, not silently replaced")
}

// TestRegistrationFailureYieldsNoBundle — a certificate that could not be
// registered can never be authorized, so handing it out would deliver a
// credential that silently does not work.
func TestRegistrationFailureYieldsNoBundle(t *testing.T) {
	store := newMemCredStore()
	// A closed database makes Register fail the way an unavailable one would.
	issuer := NewCertIssuer(store, repository.NewAgentCertRepository(closedDB(t)))

	bundle, err := issuer.IssueFor(context.Background(), testQube("dev-work"))
	require.Error(t, err, "issuance must fail when registration does")
	assert.Nil(t, bundle, "no bundle may be returned for an unregistered certificate")
}

// TestRevokeForRemovesAccess — purging a qube must take its agent's credential
// with it.
func TestRevokeForRemovesAccess(t *testing.T) {
	issuer, certs, _ := issuerFixture(t)
	ctx := context.Background()

	bundle, err := issuer.IssueFor(ctx, testQube("doomed"))
	require.NoError(t, err)
	_, err = certs.Authorize(ctx, bundle.Fingerprint)
	require.NoError(t, err)

	require.NoError(t, issuer.RevokeFor(ctx, "q-doomed", "qube purged"))

	_, err = certs.Authorize(ctx, bundle.Fingerprint)
	assert.ErrorIs(t, err, repository.ErrCertRevoked)
}

// TestCAKeyStoredWithAWarning — the stored description is what an operator sees
// when browsing credentials, and this one deserves to be alarming.
func TestCAKeyStoredWithAWarning(t *testing.T) {
	issuer, _, store := issuerFixture(t)
	_, err := issuer.IssueFor(context.Background(), testQube("alpha"))
	require.NoError(t, err)

	key, ok := store.creds[caKeyCredentialName]
	require.True(t, ok, "the CA key must be stored in the credential store, not on disk")
	assert.Contains(t, strings.ToLower(key.Description), "private key")
	assert.Contains(t, strings.ToLower(key.Description), "mint")
}
