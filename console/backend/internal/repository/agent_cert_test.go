package repository

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testCert mints a throwaway certificate so the fingerprint under test is
// derived from a real DER encoding rather than an invented string.
func testCert(t *testing.T, cn string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return cert
}

func certRepo(t *testing.T) *AgentCertRepository {
	t.Helper()
	db, cleanup := setupQubeTestDB(t)
	t.Cleanup(cleanup)
	return NewAgentCertRepository(db)
}

// TestAuthorizeAcceptsRegistered — the ordinary path.
func TestAuthorizeAcceptsRegistered(t *testing.T) {
	repo := certRepo(t)
	ctx := context.Background()
	cert := testCert(t, "agent-dev-work")
	fp := Fingerprint(cert)

	require.NoError(t, repo.Register(ctx, &AgentCert{
		Fingerprint: fp, QubeID: "q1", SubjectCN: cert.Subject.CommonName,
		IssuedAt: time.Now().UTC(),
	}))

	got, err := repo.Authorize(ctx, fp)
	require.NoError(t, err)
	assert.Equal(t, "q1", got.QubeID)
}

// TestAuthorizeRejectsUnregistered is the property that makes the registry
// worth having: possession of a CA signature is not, by itself, permission to
// connect. Without this check, anyone who ever obtained a signed certificate
// keeps access forever.
func TestAuthorizeRejectsUnregistered(t *testing.T) {
	repo := certRepo(t)
	cert := testCert(t, "never-issued")

	_, err := repo.Authorize(context.Background(), Fingerprint(cert))
	assert.ErrorIs(t, err, ErrCertNotRegistered)
}

// TestRevokeTakesEffect — the whole reason mTLS is operationally viable here.
// A CRL would need publishing and fetching, and one that is never fetched
// provides nothing; this takes effect on the next authorization call.
func TestRevokeTakesEffect(t *testing.T) {
	repo := certRepo(t)
	ctx := context.Background()
	cert := testCert(t, "compromised")
	fp := Fingerprint(cert)

	require.NoError(t, repo.Register(ctx, &AgentCert{
		Fingerprint: fp, QubeID: "q1", SubjectCN: "compromised", IssuedAt: time.Now().UTC(),
	}))
	_, err := repo.Authorize(ctx, fp)
	require.NoError(t, err, "must be authorized before revocation")

	require.NoError(t, repo.Revoke(ctx, fp, "host compromised"))

	got, err := repo.Authorize(ctx, fp)
	assert.ErrorIs(t, err, ErrCertRevoked)
	require.NotNil(t, got, "the record is still returned so the reason can be logged")
	assert.Equal(t, "host compromised", got.RevokedReason)
	assert.True(t, got.Revoked())
}

// TestRevokeIsIdempotent — revoking twice must not error or move the timestamp.
// An operator who clicks twice, or a retry, should not be punished.
func TestRevokeIsIdempotent(t *testing.T) {
	repo := certRepo(t)
	ctx := context.Background()
	fp := Fingerprint(testCert(t, "x"))
	require.NoError(t, repo.Register(ctx, &AgentCert{
		Fingerprint: fp, QubeID: "q1", SubjectCN: "x", IssuedAt: time.Now().UTC(),
	}))

	require.NoError(t, repo.Revoke(ctx, fp, "first"))
	first, err := repo.GetByFingerprint(ctx, fp)
	require.NoError(t, err)

	require.NoError(t, repo.Revoke(ctx, fp, "second"))
	second, err := repo.GetByFingerprint(ctx, fp)
	require.NoError(t, err)

	assert.Equal(t, first.RevokedAt.UnixNano(), second.RevokedAt.UnixNano(),
		"a second revoke must not move the original timestamp")
	assert.Equal(t, "first", second.RevokedReason, "the original reason is the true one")
}

// TestExpiredCertRejected — the registry enforces its own expiry rather than
// trusting only the certificate's NotAfter, so an issued lifetime can be
// shortened after the fact.
func TestExpiredCertRejected(t *testing.T) {
	repo := certRepo(t)
	ctx := context.Background()
	fp := Fingerprint(testCert(t, "stale"))
	past := time.Now().UTC().Add(-time.Hour)

	require.NoError(t, repo.Register(ctx, &AgentCert{
		Fingerprint: fp, QubeID: "q1", SubjectCN: "stale",
		IssuedAt: time.Now().UTC().Add(-2 * time.Hour), ExpiresAt: &past,
	}))

	_, err := repo.Authorize(ctx, fp)
	assert.ErrorIs(t, err, ErrCertExpired)
}

// TestErrorsAreDistinct — an unregistered certificate carrying a valid CA
// signature is a very different event from an ordinary revocation. Collapsing
// them would hide the first among the second in the logs.
func TestErrorsAreDistinct(t *testing.T) {
	assert.NotEqual(t, ErrCertNotRegistered, ErrCertRevoked)
	assert.NotEqual(t, ErrCertRevoked, ErrCertExpired)
	assert.NotEqual(t, ErrCertNotRegistered, ErrCertExpired)
}

// TestRevokeByQube — purging a qube must take its agent's access with it,
// otherwise a decommissioned machine keeps a working credential.
func TestRevokeByQube(t *testing.T) {
	repo := certRepo(t)
	ctx := context.Background()

	for _, cn := range []string{"a", "b"} {
		require.NoError(t, repo.Register(ctx, &AgentCert{
			Fingerprint: Fingerprint(testCert(t, cn)), QubeID: "doomed",
			SubjectCN: cn, IssuedAt: time.Now().UTC(),
		}))
	}
	other := Fingerprint(testCert(t, "bystander"))
	require.NoError(t, repo.Register(ctx, &AgentCert{
		Fingerprint: other, QubeID: "keeper", SubjectCN: "bystander", IssuedAt: time.Now().UTC(),
	}))

	n, err := repo.RevokeByQube(ctx, "doomed", "qube purged")
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	certs, err := repo.ListByQube(ctx, "doomed")
	require.NoError(t, err)
	for _, c := range certs {
		assert.True(t, c.Revoked(), "cert %s must be revoked with its qube", c.SubjectCN)
	}

	_, err = repo.Authorize(ctx, other)
	assert.NoError(t, err, "another qube's cert must be untouched")
}

// TestFingerprintIsStableAndUnique — the fingerprint is the registry key, so a
// collision or an unstable value would misauthorize.
func TestFingerprintIsStableAndUnique(t *testing.T) {
	a := testCert(t, "same-cn")
	b := testCert(t, "same-cn")

	assert.Equal(t, Fingerprint(a), Fingerprint(a), "must be stable across calls")
	assert.NotEqual(t, Fingerprint(a), Fingerprint(b),
		"two distinct certs must differ even with an identical subject")
	assert.Len(t, Fingerprint(a), 64, "SHA-256 hex is 64 characters")
}
