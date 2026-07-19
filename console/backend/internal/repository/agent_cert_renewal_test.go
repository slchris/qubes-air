package repository

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/slchris/qubes-air/console/internal/pki"
)

// registerFor puts one certificate in the registry with a chosen expiry, and
// returns its fingerprint.
func registerFor(t *testing.T, repo *AgentCertRepository, qubeID, cn string, expires *time.Time) string {
	t.Helper()
	fp := Fingerprint(testCert(t, cn))
	require.NoError(t, repo.Register(context.Background(), &AgentCert{
		Fingerprint: fp, QubeID: qubeID, SubjectCN: cn,
		IssuedAt: time.Now().UTC(), ExpiresAt: expires,
	}))
	return fp
}

func at(d time.Duration) *time.Time {
	t := time.Now().UTC().Add(d)
	return &t
}

func fingerprints(certs []*AgentCert) []string {
	out := make([]string, 0, len(certs))
	for _, c := range certs {
		out = append(out, c.Fingerprint)
	}
	return out
}

// TestRenewalCandidateCutoffIsInclusive pins the boundary. The scheduler asks
// "what expires within N days"; a certificate landing exactly on the cutoff must
// be inside it, or the boundary case is the one that never gets renewed.
func TestRenewalCandidateCutoffIsInclusive(t *testing.T) {
	repo := certRepo(t)
	ctx := context.Background()

	cutoff := time.Now().UTC().Add(14 * 24 * time.Hour)
	exactly := cutoff
	justInside := cutoff.Add(-time.Second)
	justOutside := cutoff.Add(time.Second)

	onTheLine := registerFor(t, repo, "q-exact", "agent-exact", &exactly)
	inside := registerFor(t, repo, "q-inside", "agent-inside", &justInside)
	registerFor(t, repo, "q-outside", "agent-outside", &justOutside)

	got, err := repo.ListRenewalCandidates(ctx, cutoff)
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{onTheLine, inside}, fingerprints(got),
		"the cutoff is inclusive, and one second past it is not a candidate")
}

// TestRenewalCandidateIsTheLongestLivedCert is the property that stops renewal
// running away with itself.
//
// Renewal deliberately does not revoke the certificate it replaces, so a qube
// mid-window holds an old certificate near expiry AND a fresh one. If the query
// keyed on "any row approaching expiry" it would keep finding that old row and
// renew again on every sweep, minting a certificate per sweep forever.
func TestRenewalCandidateIsTheLongestLivedCert(t *testing.T) {
	repo := certRepo(t)
	ctx := context.Background()

	// A qube that just renewed: the old certificate is inside the window, the
	// new one is not.
	registerFor(t, repo, "q-renewed", "agent-renewed", at(3*24*time.Hour))
	registerFor(t, repo, "q-renewed", "agent-renewed", at(90*24*time.Hour))

	// A qube that has not renewed: both of its certificates are running out.
	registerFor(t, repo, "q-stale", "agent-stale", at(2*24*time.Hour))
	newest := registerFor(t, repo, "q-stale", "agent-stale", at(5*24*time.Hour))

	got, err := repo.ListRenewalCandidates(ctx, time.Now().UTC().Add(14*24*time.Hour))
	require.NoError(t, err)

	require.Len(t, got, 1, "one row per qube, and a qube that already renewed is not a candidate")
	assert.Equal(t, newest, got[0].Fingerprint,
		"the candidate must be the qube's best credential, not its worst")
	assert.Equal(t, "q-stale", got[0].QubeID)
}

// TestRenewalCandidatesExcludeRevoked — a revoked certificate cannot be renewed,
// and must not count as a qube's best credential either. A qube whose newest
// certificate was revoked is running on its older one, and that is what decides
// whether it needs renewing.
func TestRenewalCandidatesExcludeRevoked(t *testing.T) {
	repo := certRepo(t)
	ctx := context.Background()

	live := registerFor(t, repo, "q1", "agent-one", at(5*24*time.Hour))
	revokedButLongLived := registerFor(t, repo, "q1", "agent-one", at(90*24*time.Hour))
	require.NoError(t, repo.Revoke(ctx, revokedButLongLived, "key compromised"))

	// A qube with nothing left at all must simply drop out.
	onlyCert := registerFor(t, repo, "q-purged", "agent-purged", at(5*24*time.Hour))
	require.NoError(t, repo.Revoke(ctx, onlyCert, "qube purged"))

	got, err := repo.ListRenewalCandidates(ctx, time.Now().UTC().Add(14*24*time.Hour))
	require.NoError(t, err)

	assert.Equal(t, []string{live}, fingerprints(got),
		"a revoked certificate is neither renewable nor evidence the qube is covered")
}

// TestRenewalCandidatesIncludeAlreadyExpired — these are the qubes that already
// went dark. Renewal will probably fail for them, since the agent can no longer
// authenticate to be dialled, but that failure is the signal. Dropping them
// would hide exactly the fleet-wide silence this mechanism exists to catch.
func TestRenewalCandidatesIncludeAlreadyExpired(t *testing.T) {
	repo := certRepo(t)

	dark := registerFor(t, repo, "q-dark", "agent-dark", at(-30*24*time.Hour))

	got, err := repo.ListRenewalCandidates(context.Background(), time.Now().UTC().Add(14*24*time.Hour))
	require.NoError(t, err)

	assert.Equal(t, []string{dark}, fingerprints(got),
		"an already-expired certificate must stay visible, not vanish from the query")
}

// TestRenewalCandidatesAreOrderedByUrgency — a sweep that runs out of time, or
// is bounded to N qubes, must have spent it on the ones closest to going dark.
func TestRenewalCandidatesAreOrderedByUrgency(t *testing.T) {
	repo := certRepo(t)

	third := registerFor(t, repo, "q-c", "agent-c", at(10*24*time.Hour))
	first := registerFor(t, repo, "q-a", "agent-a", at(1*24*time.Hour))
	second := registerFor(t, repo, "q-b", "agent-b", at(4*24*time.Hour))

	got, err := repo.ListRenewalCandidates(context.Background(), time.Now().UTC().Add(14*24*time.Hour))
	require.NoError(t, err)

	assert.Equal(t, []string{first, second, third}, fingerprints(got),
		"soonest expiry first")
}

// TestRenewalCandidatesSkipCertsWithoutExpiry — a certificate with no recorded
// expiry is one Authorize never times out, so it is not approaching anything and
// there is no date to schedule a renewal against.
func TestRenewalCandidatesSkipCertsWithoutExpiry(t *testing.T) {
	repo := certRepo(t)
	registerFor(t, repo, "q-forever", "agent-forever", nil)

	got, err := repo.ListRenewalCandidates(context.Background(), time.Now().UTC().Add(365*24*time.Hour))
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestRenewalCandidatesEmptyRegistry — no qubes must mean no work, not an error
// the scheduler has to special-case on every sweep.
func TestRenewalCandidatesEmptyRegistry(t *testing.T) {
	got, err := certRepo(t).ListRenewalCandidates(context.Background(), time.Now().UTC())
	require.NoError(t, err)
	assert.Empty(t, got)
}

// TestRecordRenewalLeavesTheOldCertWorking is the safety property the whole
// design turns on. Between the console signing a renewal and the agent
// installing it, the agent still has connections open on the old certificate.
// Revoking it here would drop them, and would leave the agent holding nothing
// valid if the install then failed.
func TestRecordRenewalLeavesTheOldCertWorking(t *testing.T) {
	repo := certRepo(t)
	ctx := context.Background()

	old := registerFor(t, repo, "q1", "agent-one", at(3*24*time.Hour))
	newFP := Fingerprint(testCert(t, "agent-one"))

	require.NoError(t, repo.RecordRenewal(ctx, old, &AgentCert{
		Fingerprint: newFP, QubeID: "q1", SubjectCN: "agent-one",
		IssuedAt: time.Now().UTC(), ExpiresAt: at(90 * 24 * time.Hour),
	}))

	_, err := repo.Authorize(ctx, newFP)
	assert.NoError(t, err, "the renewed certificate must authorize immediately")

	prev, err := repo.Authorize(ctx, old)
	assert.NoError(t, err, "the previous certificate must keep working until it expires")
	assert.False(t, prev.Revoked(), "renewal must never revoke what it replaces")
}

// TestRecordRenewalStopsTheQubeBeingACandidate — the loop closes. A qube that
// renewed must drop out of the query on the next sweep, or the scheduler renews
// it again, and again.
func TestRecordRenewalStopsTheQubeBeingACandidate(t *testing.T) {
	repo := certRepo(t)
	ctx := context.Background()
	window := 14 * 24 * time.Hour

	old := registerFor(t, repo, "q1", "agent-one", at(3*24*time.Hour))

	before, err := repo.ListRenewalCandidates(ctx, time.Now().UTC().Add(window))
	require.NoError(t, err)
	require.Len(t, before, 1, "must be a candidate before renewing")

	require.NoError(t, repo.RecordRenewal(ctx, old, &AgentCert{
		Fingerprint: Fingerprint(testCert(t, "agent-one")), QubeID: "q1", SubjectCN: "agent-one",
		IssuedAt: time.Now().UTC(), ExpiresAt: at(90 * 24 * time.Hour),
	}))

	after, err := repo.ListRenewalCandidates(ctx, time.Now().UTC().Add(window))
	require.NoError(t, err)
	assert.Empty(t, after, "a renewed qube must stop being a candidate, or renewal runs forever")
}

// TestRecordRenewalRefusesAfterPurge — a qube purged mid-renewal has already had
// its access revoked. Inserting a fresh unrevoked row afterwards would hand a
// decommissioned machine a working credential back, and nothing downstream would
// notice: a registered unrevoked certificate is what a legitimate agent looks
// like.
func TestRecordRenewalRefusesAfterPurge(t *testing.T) {
	repo := certRepo(t)
	ctx := context.Background()

	old := registerFor(t, repo, "q-doomed", "agent-doomed", at(3*24*time.Hour))
	_, err := repo.RevokeByQube(ctx, "q-doomed", "qube purged")
	require.NoError(t, err)

	newFP := Fingerprint(testCert(t, "agent-doomed"))
	err = repo.RecordRenewal(ctx, old, &AgentCert{
		Fingerprint: newFP, QubeID: "q-doomed", SubjectCN: "agent-doomed",
		IssuedAt: time.Now().UTC(), ExpiresAt: at(90 * 24 * time.Hour),
	})
	assert.ErrorIs(t, err, ErrCertRevoked)

	_, err = repo.Authorize(ctx, newFP)
	assert.ErrorIs(t, err, ErrCertNotRegistered,
		"a purged qube must not get a working credential back through renewal")
}

// TestRecordRenewalRacingAPurge — the invariant that survives a purge landing at
// the worst possible moment.
//
// RecordRenewal puts its guard inside the insert precisely so there is no window
// between checking that the previous certificate is live and writing the new
// one. Whichever order these two land in, the qube must end up with nothing
// unrevoked: if the renewal commits first the purge revokes both, and if the
// purge commits first the renewal must be refused. The outcome this rules out is
// a decommissioned machine holding a working credential.
func TestRecordRenewalRacingAPurge(t *testing.T) {
	ctx := context.Background()

	for i := 0; i < 30; i++ {
		repo := certRepo(t)
		old := registerFor(t, repo, "q-doomed", "agent-doomed", at(3*24*time.Hour))

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = repo.RecordRenewal(ctx, old, &AgentCert{
				Fingerprint: Fingerprint(testCert(t, "agent-doomed")),
				QubeID:      "q-doomed", SubjectCN: "agent-doomed",
				IssuedAt: time.Now().UTC(), ExpiresAt: at(90 * 24 * time.Hour),
			})
		}()
		go func() {
			defer wg.Done()
			_, _ = repo.RevokeByQube(ctx, "q-doomed", "qube purged")
		}()
		wg.Wait()

		certs, err := repo.ListByQube(ctx, "q-doomed")
		require.NoError(t, err)
		for _, c := range certs {
			assert.True(t, c.Revoked(),
				"iteration %d: %s survived the purge; a decommissioned qube must keep no working credential",
				i, c.Fingerprint)
		}
	}
}

// TestRecordRenewalRefusesIdentityMismatch — a renewal recorded against the
// wrong qube would let a certificate issued for one agent be authorized as
// another. The CA refuses to sign across identities; this enforces the same rule
// where the authorization decision actually reads from.
func TestRecordRenewalRefusesIdentityMismatch(t *testing.T) {
	repo := certRepo(t)
	ctx := context.Background()
	old := registerFor(t, repo, "q1", "agent-one", at(3*24*time.Hour))

	cases := map[string]*AgentCert{
		"different qube": {QubeID: "q2", SubjectCN: "agent-one"},
		"different name": {QubeID: "q1", SubjectCN: "agent-two"},
		"both":           {QubeID: "q2", SubjectCN: "agent-two"},
	}
	for name, renewed := range cases {
		t.Run(name, func(t *testing.T) {
			renewed.Fingerprint = Fingerprint(testCert(t, renewed.SubjectCN))
			renewed.IssuedAt = time.Now().UTC()
			renewed.ExpiresAt = at(90 * 24 * time.Hour)

			err := repo.RecordRenewal(ctx, old, renewed)
			assert.ErrorIs(t, err, ErrRenewalIdentityMismatch)

			_, err = repo.Authorize(ctx, renewed.Fingerprint)
			assert.ErrorIs(t, err, ErrCertNotRegistered,
				"a refused renewal must leave nothing behind in the registry")
		})
	}
}

// TestRecordRenewalRefusesUnknownPrevious — renewing something that was never
// registered means the caller is confused about what it dialled. Inserting
// anyway would create a certificate with no lineage.
func TestRecordRenewalRefusesUnknownPrevious(t *testing.T) {
	repo := certRepo(t)
	newFP := Fingerprint(testCert(t, "agent-ghost"))

	err := repo.RecordRenewal(context.Background(), Fingerprint(testCert(t, "agent-ghost")), &AgentCert{
		Fingerprint: newFP, QubeID: "q1", SubjectCN: "agent-ghost",
		IssuedAt: time.Now().UTC(), ExpiresAt: at(90 * 24 * time.Hour),
	})
	assert.ErrorIs(t, err, ErrCertNotRegistered)

	_, err = repo.Authorize(context.Background(), newFP)
	assert.ErrorIs(t, err, ErrCertNotRegistered)
}

// TestRenewedCertificateVerifiesAndAuthorizes is the end-to-end contract between
// the two halves of renewal: the CA signs a request the agent generated, and the
// resulting certificate both chains to the CA and authorizes through the
// registry.
//
// These live in different packages and could drift independently. If they ever
// did, a renewed agent would be turned away as unregistered — which looks like a
// connectivity fault, not a code change, and would be chased for a long time.
func TestRenewedCertificateVerifiesAndAuthorizes(t *testing.T) {
	repo := certRepo(t)
	ctx := context.Background()

	ca, err := pki.NewCA("test-ca", 0)
	require.NoError(t, err)

	// Bootstrap: the console mints the first identity and registers it.
	bundle, err := ca.IssueAgentCert("agent-dev-work", 0)
	require.NoError(t, err)
	notAfter := bundle.NotAfter
	require.NoError(t, repo.Register(ctx, &AgentCert{
		Fingerprint: bundle.Fingerprint, QubeID: "q1", SubjectCN: "agent-dev-work",
		IssuedAt: time.Now().UTC(), ExpiresAt: &notAfter,
	}))

	// Renewal: the agent generates a key, keeps it, and sends only a request.
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	csrDER, err := x509.CreateCertificateRequest(rand.Reader,
		&x509.CertificateRequest{Subject: pkix.Name{CommonName: "agent-dev-work"}}, key)
	require.NoError(t, err)
	csrPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))

	signed, err := ca.SignAgentCSR(csrPEM, "agent-dev-work", 0)
	require.NoError(t, err)

	block, _ := pem.Decode([]byte(signed.CertPEM))
	require.NotNil(t, block)
	leaf, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)

	// It must verify against the CA the way the transport will verify it.
	pool := x509.NewCertPool()
	require.True(t, pool.AppendCertsFromPEM([]byte(signed.CAPEM)))
	_, err = leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	require.NoError(t, err, "a renewed certificate must chain to the CA")

	// The fingerprint the CA reports must be the one the verifier computes.
	require.Equal(t, signed.Fingerprint, Fingerprint(leaf),
		"a fingerprint mismatch would reject every renewed agent")

	renewedNotAfter := signed.NotAfter
	require.NoError(t, repo.RecordRenewal(ctx, bundle.Fingerprint, &AgentCert{
		Fingerprint: signed.Fingerprint, QubeID: "q1", SubjectCN: "agent-dev-work",
		IssuedAt: time.Now().UTC(), ExpiresAt: &renewedNotAfter,
	}))

	// What the TLS stack would hand Authorize on the next handshake.
	got, err := repo.Authorize(ctx, Fingerprint(leaf))
	require.NoError(t, err, "a renewed agent must be authorized on its next connection")
	assert.Equal(t, "q1", got.QubeID)
}
