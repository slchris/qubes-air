package service

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/orchestrator"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resumeFixture wires a QubeService to a REAL CertIssuer over a real database,
// so these tests cross the seam that matters: a resume is only fixed if a
// certificate is actually signed, registered and rendered into the cloud-init
// document terraform uploads. A fake issuer would pass while the qube still came
// back locked out.
type resumeFixture struct {
	svc      QubeService
	db       *database.DB
	qubes    repository.QubeRepository
	zones    repository.ZoneRepository
	certs    *repository.AgentCertRepository
	tokens   *repository.BootstrapTokenRepository
	issuer   *CertIssuer
	exec     *orchestrator.FakeExecutor
	zoneID   string
	qubeID   string
	qubeName string
}

func newResumeFixture(t *testing.T) *resumeFixture {
	t.Helper()

	f, err := os.CreateTemp("", "qube-resume-identity-*.db")
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

	zoneRepo := repository.NewZoneRepository(db)
	qubeRepo := repository.NewQubeRepository(db)
	certs := repository.NewAgentCertRepository(db)
	issuer := NewCertIssuer(newMemCredStore(), certs, t.TempDir(), "0.0.0.0:8443", testAgentPackage()).
		WithBootstrapTokens(repository.NewBootstrapTokenRepository(db), 0)

	exec := orchestrator.NewFakeExecutor()
	zoneSvc := NewZoneService(zoneRepo, qubeRepo)
	qubeSvc := NewQubeService(qubeRepo, zoneRepo, WithExecutor(exec), WithCertIssuer(issuer))

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)
	const name = "parked01"
	op, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name: name, Type: models.QubeTypeApp, ZoneID: zone.ID,
	})
	require.NoError(t, err)

	return &resumeFixture{
		svc: qubeSvc, db: db, qubes: qubeRepo, zones: zoneRepo, certs: certs,
		tokens: repository.NewBootstrapTokenRepository(db),
		issuer: issuer, exec: exec, zoneID: zone.ID, qubeID: op.Qube.ID, qubeName: name,
	}
}

// suspend parks the qube the way a user would, which is what destroys the
// compute instance and leaves nothing for renewal to talk to.
func (f *resumeFixture) suspend(t *testing.T) {
	t.Helper()
	_, err := f.svc.Stop(context.Background(), f.qubeID)
	require.NoError(t, err)
	f.exec.Reset()
}

// liveTokens returns the qube's unredeemed, unexpired bootstrap tokens. Under
// the token design this — not a certificate — is what provisioning produces.
func (f *resumeFixture) liveTokens(t *testing.T) []*repository.BootstrapToken {
	t.Helper()
	all, err := f.tokens.ListByQube(context.Background(), f.qubeID)
	require.NoError(t, err)
	var live []*repository.BootstrapToken
	for _, tok := range all {
		if tok.RedeemedAt == nil && tok.NotAfter.After(time.Now()) {
			live = append(live, tok)
		}
	}
	return live
}

// registerCert puts a certificate in the registry directly, standing in for a
// bootstrap that already happened. Provisioning no longer registers one.
func (f *resumeFixture) registerCert(t *testing.T, fingerprint string) {
	t.Helper()
	expires := time.Now().Add(90 * 24 * time.Hour)
	require.NoError(t, f.certs.Register(context.Background(), &repository.AgentCert{
		Fingerprint: fingerprint, QubeID: f.qubeID,
		SubjectCN: AgentCommonName(f.qubeName), IssuedAt: time.Now().UTC(), ExpiresAt: &expires,
	}))
}

// expire backdates a registered certificate, standing in for the months a qube
// can legitimately stay parked. Written directly because the point is elapsed
// time and nothing in the console can make a certificate age.
func (f *resumeFixture) expire(t *testing.T, fingerprint string, at time.Time) {
	t.Helper()
	_, err := f.db.DB().ExecContext(context.Background(),
		`UPDATE agent_certs SET expires_at = ? WHERE fingerprint = ?`, at.UTC(), fingerprint)
	require.NoError(t, err)
}

// TestStart_ResumeIssuesAFreshIdentity is the fix for the suspended-qube
// lockout.
//
// Renewal runs over the agent's mTLS channel and suspend DESTROYS the compute
// instance, so a suspended qube has no agent to renew against and the renewal
// sweep skips it — correctly. Something has to hand a parked qube a working
// identity when it comes back, and resume is the only moment at which the
// delivery channel (cloud-init) is open. If this stops happening, a qube parked
// long enough returns with an expired certificate and no way to be repaired.
func TestStart_ResumeIssuesAFreshIdentity(t *testing.T) {
	f := newResumeFixture(t)
	ctx := context.Background()

	before := f.liveTokens(t)
	require.Len(t, before, 1, "creation mints exactly one bootstrap credential")
	original := before[0].SecretHash

	f.suspend(t)
	_, err := f.svc.Start(ctx, f.qubeID)
	require.NoError(t, err)

	after := f.liveTokens(t)
	require.Len(t, after, 1,
		"a resumed qube must hold exactly one live credential; a leftover second is another way to claim its name")
	assert.NotEqual(t, original, after[0].SecretHash,
		"resume must mint a NEW token; the one from months ago has long expired")
	assert.True(t, after[0].NotAfter.After(time.Now()),
		"the resumed qube must come back with a credential that has not already expired")

	// Minting is only half of it: the credential has to reach the remote, and
	// the cloud-init document terraform uploads is the only channel that does.
	// A token in the table with no rendered document is one the agent never
	// receives.
	path := f.issuer.IdentityPath(f.qubeName)
	require.NotEmpty(t, path)
	rendered, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(rendered), "/etc/qubes-air/bootstrap-token",
		"resume must rewrite the cloud-init document, not just the token table")
	assert.NotContains(t, string(rendered), "PRIVATE KEY",
		"resume must not reintroduce key delivery")
}

// TestStart_ResumeRetiresThePreviousIdentity — the instance that held the old
// certificate no longer exists, so nothing may legitimately present it again.
//
// Renewal deliberately does NOT revoke, because there the agent is still running
// and still holding its certificate. Resume is the opposite case: suspend
// destroyed the instance and its OS disk, and a still-valid certificate for a
// machine that is gone is usable by whoever kept a copy of the snippet or the
// disk image, for the rest of its ninety days.
func TestStart_ResumeRetiresThePreviousIdentity(t *testing.T) {
	f := newResumeFixture(t)
	ctx := context.Background()

	// Stand in for a bootstrap that already ran: the qube holds a certificate.
	const original = "fp-from-an-earlier-bootstrap"
	f.registerCert(t, original)
	_, err := f.certs.Authorize(ctx, original)
	require.NoError(t, err, "precondition: the parked qube holds a working certificate")

	f.suspend(t)
	_, err = f.svc.Start(ctx, f.qubeID)
	require.NoError(t, err)

	_, err = f.certs.Authorize(ctx, original)
	assert.ErrorIs(t, err, repository.ErrCertRevoked,
		"the certificate of a destroyed instance must stop being accepted")

	// And the qube must have a way back: a live token for the fresh bootstrap
	// that follows. Revoking without reissuing is the lockout from the other
	// side.
	assert.Len(t, f.liveTokens(t), 1,
		"the resumed qube was left revoked with no credential to bootstrap with")
}

// TestStart_LongSuspendedQubeWithAnExpiredCertificateResumes is the lockout
// itself, reproduced end to end.
//
// A qube parked across its entire renewal window comes back with an expired
// certificate. The agent refuses to start without a valid one — deliberately,
// since running without mTLS would let anyone on the LAN execute its qrexec
// services — so the channel that could repair it is the channel that is broken.
// Before reissue-on-resume this qube was unrecoverable.
func TestStart_LongSuspendedQubeWithAnExpiredCertificateResumes(t *testing.T) {
	f := newResumeFixture(t)
	ctx := context.Background()

	// The qube bootstrapped once, long ago, and its certificate has since run
	// out while it sat parked.
	const stale = "fp-from-before-the-long-park"
	f.registerCert(t, stale)
	f.suspend(t)
	f.expire(t, stale, time.Now().UTC().Add(-24*time.Hour))
	_, err := f.certs.Authorize(ctx, stale)
	require.ErrorIs(t, err, repository.ErrCertExpired,
		"precondition: the parked qube's certificate has expired while it was down")

	op, err := f.svc.Start(ctx, f.qubeID)
	require.NoError(t, err, "a long-suspended qube must still be resumable; it cannot renew its way out")
	assert.Equal(t, models.QubeStatusRunning, op.Qube.Status)

	// The way back is a fresh bootstrap, not a fresh certificate: resume hands
	// the qube a live token, and the console issues the certificate once it can
	// reach the agent. What matters is that the expired one is gone and a usable
	// credential took its place — without that the qube is unrecoverable,
	// because the channel that would repair it is the one that is broken.
	live := f.liveTokens(t)
	require.Len(t, live, 1, "the resumed qube must come back with a way to obtain an identity")
	assert.True(t, live[0].NotAfter.After(time.Now()))

	_, err = f.certs.Authorize(ctx, stale)
	assert.ErrorIs(t, err, repository.ErrCertRevoked,
		"the expired certificate of a destroyed instance must also stop being accepted")
}

// TestStart_ReissueFailureDoesNotStrandTheClaim — nothing is running when
// issuance fails, so the qube must not be left pinned in "resuming", which would
// refuse every future operation on it as busy.
//
// It must also not reach terraform: a resume that cannot deliver an identity
// would rebuild the compute instance around an agent that then refuses to start,
// which is the same lockout dressed up as a successful apply.
func TestStart_ReissueFailureDoesNotStrandTheClaim(t *testing.T) {
	f := newResumeFixture(t)
	ctx := context.Background()
	f.suspend(t)

	// Same qube and same repositories, but an issuer whose registry is
	// unavailable — so issuance fails the way it does when the database is down,
	// while the qube row itself remains readable and writable.
	brokenDB := closedDB(t)
	broken := NewCertIssuer(newMemCredStore(), repository.NewAgentCertRepository(brokenDB),
		t.TempDir(), "0.0.0.0:8443", testAgentPackage()).
		WithBootstrapTokens(repository.NewBootstrapTokenRepository(brokenDB), 0)
	svc := NewQubeService(f.qubes, f.zones, WithExecutor(f.exec), WithCertIssuer(broken))

	_, err := svc.Start(ctx, f.qubeID)
	require.Error(t, err, "a resume that cannot deliver an identity must fail rather than promise one")
	assert.ErrorIs(t, err, ErrOrchestration)

	after, err := f.qubes.GetByID(ctx, f.qubeID)
	require.NoError(t, err)
	assert.Equal(t, models.QubeStatusSuspended, after.Status,
		"the claim must be released; a qube stuck in a transient status refuses every later operation")
	assert.Empty(t, f.exec.Calls(), "terraform must not run for a qube that has no identity to deliver")
}

// TestProbeAgent_SuspendedQubeIsNotAFailure — "no compute instance, so nothing
// to probe" is an expected state, not a fault.
//
// Recording it as unreachable would paint every parked qube red for as long as
// it stayed parked, and a health field that is red for expected states is one
// operators learn to skip — the same reasoning that made the settle phase
// necessary for booting qubes.
func TestProbeAgent_SuspendedQubeIsNotAFailure(t *testing.T) {
	f := newResumeFixture(t)
	ctx := context.Background()

	f.suspend(t)
	parked, err := f.qubes.GetByID(ctx, f.qubeID)
	require.NoError(t, err)
	require.Equal(t, models.QubeStatusSuspended, parked.Status)

	res := f.svc.ProbeAgent(ctx, parked, AgentProbeSteady)
	assert.Equal(t, AgentProbeNoCompute, res.Status)
	assert.False(t, res.Reachable)

	after, err := f.qubes.GetByID(ctx, f.qubeID)
	require.NoError(t, err)
	assert.Equal(t, models.AgentHealthUnknown, after.AgentHealth,
		"a suspended qube is not unhealthy; there is simply no agent to have an opinion about")

	// The attempt is still recorded. A probe path that writes nothing at all
	// looks identical from outside to one that never ran.
	require.NotNil(t, after.AgentLastProbedAt)
	assert.Contains(t, after.AgentLastError, "no agent to probe",
		"and it must say why, or 'unknown' is indistinguishable from a console that lost visibility")
}

// TestProbeAgent_SuspendedQubeDropsAStaleRenewalWarning — suspension must not go
// on publishing a renewal failure.
//
// A qube that failed to renew while running and was then parked would otherwise
// keep reporting "certificate EXPIRED, this qube can no longer authenticate" for
// as long as it stayed suspended. That warning is about an instance that no
// longer exists, and the qube is handed a fresh identity the moment it is
// resumed — so it is not merely noisy, it is false.
func TestProbeAgent_SuspendedQubeDropsAStaleRenewalWarning(t *testing.T) {
	f := newResumeFixture(t)
	ctx := context.Background()

	const warning = certRenewalWarningPrefix + " (unreachable): nobody answered"
	svc := NewQubeService(f.qubes, f.zones, WithRenewalWatch(stuckRenewal(warning)))

	running, err := f.qubes.GetByID(ctx, f.qubeID)
	require.NoError(t, err)
	require.Equal(t, models.QubeStatusRunning, running.Status)

	// While it is running the warning is exactly what should surface: the agent
	// may be alive today and dead on a known date, and only this field says so.
	svc.ProbeAgent(ctx, running, AgentProbeSteady)
	live, err := f.qubes.GetByID(ctx, f.qubeID)
	require.NoError(t, err)
	require.Contains(t, live.AgentLastError, certRenewalWarningPrefix,
		"a running qube whose certificate is not renewing must keep saying so")

	f.suspend(t)
	parked, err := f.qubes.GetByID(ctx, f.qubeID)
	require.NoError(t, err)

	svc.ProbeAgent(ctx, parked, AgentProbeSteady)
	after, err := f.qubes.GetByID(ctx, f.qubeID)
	require.NoError(t, err)
	assert.NotContains(t, after.AgentLastError, certRenewalWarningPrefix,
		"a parked qube must not accumulate renewal failures; it has nothing to renew against")
}

// stuckRenewal is a RenewalWatch that never clears, standing in for a qube the
// renewal monitor has an outstanding failure recorded against.
type stuckRenewal string

func (s stuckRenewal) RenewalWarning(string) string { return string(s) }

// TestErrorStatusDoesNotRevokeALiveAgent — starting an Error qube must not
// withdraw the certificate its running agent is using.
//
// The old guard asked computeRunning(prior), which answers "should terraform
// build a VM", not "is one running". Error is exactly where those diverge, and
// it is reached with a live VM routinely: reconcileStrandedQubes rewrites every
// Creating/Resuming qube to Error when the console restarts, including ones
// whose apply had already succeeded and whose agent is healthy.
//
// Reissuing there revokes a running agent's certificate and replaces nothing —
// terraform sees the VM already matching compute_running=true, so it is not
// rebuilt and never re-reads cloud-init. Both the prober and the renewer then
// refuse the peer: unreachable, unrenewable, rebuild-only.
func TestErrorStatusDoesNotRevokeALiveAgent(t *testing.T) {
	assert.False(t, instanceProvablyDestroyed(models.QubeStatusError),
		"Error may have a live VM behind it; revoking there is unrecoverable")
	assert.False(t, instanceProvablyDestroyed(models.QubeStatusStopped),
		"Stopped does not prove terraform destroyed anything")
	assert.False(t, instanceProvablyDestroyed(models.QubeStatusRunning))

	// Only the statuses terraform reaches by actually destroying the instance.
	assert.True(t, instanceProvablyDestroyed(models.QubeStatusSuspended))
	assert.True(t, instanceProvablyDestroyed(models.QubeStatusReleased))
}
