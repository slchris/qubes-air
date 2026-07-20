package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
)

type fakeBootstrapQubes struct {
	qubes []*models.Qube
	err   error
	// gotStatuses records what the sweep asked for, so "running only" is
	// pinned rather than assumed.
	gotStatuses []models.QubeStatus
}

func (f *fakeBootstrapQubes) ListByStatus(_ context.Context, statuses []models.QubeStatus) ([]*models.Qube, error) {
	f.gotStatuses = statuses
	return f.qubes, f.err
}

type fakeCertLister struct {
	byQube map[string][]*repository.AgentCert
	err    error
}

func (f *fakeCertLister) ListByQube(_ context.Context, qubeID string) ([]*repository.AgentCert, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byQube[qubeID], nil
}

type recordingBootstrapper struct {
	result BootstrapResult
	// perQube overrides result for named qubes.
	perQube map[string]BootstrapResult
	dialed  []string
}

func (r *recordingBootstrapper) Bootstrap(_ context.Context, qube *models.Qube) BootstrapResult {
	r.dialed = append(r.dialed, qube.Name)
	if res, ok := r.perQube[qube.Name]; ok {
		return res
	}
	return r.result
}

func uncertifiedQube(id, name string) *models.Qube {
	return &models.Qube{ID: id, Name: name, Status: models.QubeStatusRunning, IPAddress: "10.0.0.9"}
}

func liveCert(qubeID string) *repository.AgentCert {
	expires := time.Now().Add(60 * 24 * time.Hour)
	return &repository.AgentCert{
		Fingerprint: "fp-" + qubeID,
		QubeID:      qubeID,
		SubjectCN:   "agent-" + qubeID,
		ExpiresAt:   &expires,
	}
}

func bootstrapMonitorFor(
	qubes *fakeBootstrapQubes, certs *fakeCertLister, bs *recordingBootstrapper,
) *BootstrapMonitor {
	return NewBootstrapMonitor(qubes, certs, bs, time.Minute)
}

// The sweep must only dial qubes with no usable certificate. Dialing a
// certified one would spend a token it does not have and log a failure every
// minute for the life of the fleet.
func TestSweepOnlyDialsUncertifiedQubes(t *testing.T) {
	qubes := &fakeBootstrapQubes{qubes: []*models.Qube{
		uncertifiedQube("q1", "has-cert"),
		uncertifiedQube("q2", "no-cert"),
	}}
	certs := &fakeCertLister{byQube: map[string][]*repository.AgentCert{
		"q1": {liveCert("q1")},
	}}
	bs := &recordingBootstrapper{result: BootstrapResult{Status: BootstrapOK}}

	bootstrapMonitorFor(qubes, certs, bs).Sweep(context.Background())

	assert.Equal(t, []string{"no-cert"}, bs.dialed)
	assert.Equal(t, []models.QubeStatus{models.QubeStatusRunning}, qubes.gotStatuses,
		"a suspended qube has no instance to dial; sweeping one guarantees a failure every pass")
}

// A qube whose only certificate is revoked has no working identity, so it
// needs bootstrapping — ranking it as certified would leave it permanently
// locked out with the console reporting nothing wrong.
func TestSweepTreatsARevokedCertificateAsNoCertificate(t *testing.T) {
	revoked := liveCert("q1")
	at := time.Now().Add(-time.Hour)
	revoked.RevokedAt = &at

	qubes := &fakeBootstrapQubes{qubes: []*models.Qube{uncertifiedQube("q1", "revoked")}}
	certs := &fakeCertLister{byQube: map[string][]*repository.AgentCert{"q1": {revoked}}}
	bs := &recordingBootstrapper{result: BootstrapResult{Status: BootstrapOK}}

	bootstrapMonitorFor(qubes, certs, bs).Sweep(context.Background())

	assert.Equal(t, []string{"revoked"}, bs.dialed)
}

// An EXPIRED certificate is the opposite case, and the asymmetry is the point.
//
// Expired means the guest has an identity on disk, so its agent came up in
// normal mode and does not serve the bootstrap calls at all — dialing it would
// fail every pass forever. That qube belongs to renewal, or to a rebuild.
//
// This test exists because the first implementation reused renewal's
// newestUsableCert here, which does NOT filter by expiry (it is looking for
// the certificate to replace, so an expired one is exactly what it wants).
// Sharing it read as tidy and was wrong in both directions at once.
func TestSweepLeavesAnExpiredCertificateToRenewal(t *testing.T) {
	expired := liveCert("q1")
	past := time.Now().Add(-time.Hour)
	expired.ExpiresAt = &past

	qubes := &fakeBootstrapQubes{qubes: []*models.Qube{uncertifiedQube("q1", "expired")}}
	certs := &fakeCertLister{byQube: map[string][]*repository.AgentCert{"q1": {expired}}}
	bs := &recordingBootstrapper{result: BootstrapResult{Status: BootstrapOK}}

	bootstrapMonitorFor(qubes, certs, bs).Sweep(context.Background())

	assert.Empty(t, bs.dialed,
		"a qube holding an expired certificate has an identity on disk; its agent is not in bootstrap mode")
}

// The counterfactual that protects the token: a registry read that FAILS must
// not be read as "this qube has no certificate". Bootstrapping on that guess
// spends a single-use token against a console that cannot currently tell
// whether one was already spent.
func TestSweepSkipsAQubeWhoseCertificatesCannotBeRead(t *testing.T) {
	qubes := &fakeBootstrapQubes{qubes: []*models.Qube{uncertifiedQube("q1", "unknown")}}
	certs := &fakeCertLister{err: errors.New("database is locked")}
	bs := &recordingBootstrapper{result: BootstrapResult{Status: BootstrapOK}}

	bootstrapMonitorFor(qubes, certs, bs).Sweep(context.Background())

	assert.Empty(t, bs.dialed,
		"an unreadable registry was treated as an absent certificate; that spends a single-use token on a guess")
}

func TestSweepDoesNothingWhenTheFleetIsCertified(t *testing.T) {
	qubes := &fakeBootstrapQubes{qubes: []*models.Qube{
		uncertifiedQube("q1", "a"), uncertifiedQube("q2", "b"),
	}}
	certs := &fakeCertLister{byQube: map[string][]*repository.AgentCert{
		"q1": {liveCert("q1")}, "q2": {liveCert("q2")},
	}}
	bs := &recordingBootstrapper{}

	bootstrapMonitorFor(qubes, certs, bs).Sweep(context.Background())

	assert.Empty(t, bs.dialed)
}

// This is the state the sweep lands in when it ships before cloud-init
// changes: no qube holds a token, so every dial fails as unreachable and the
// sweep must simply back off rather than do anything destructive.
func TestSweepBeforeCloudInitShipsTokensIsHarmless(t *testing.T) {
	qubes := &fakeBootstrapQubes{qubes: []*models.Qube{uncertifiedQube("q1", "legacy")}}
	certs := &fakeCertLister{}
	bs := &recordingBootstrapper{result: BootstrapResult{
		Status: BootstrapUnreachable,
		Reason: "nothing listening",
	}}

	m := bootstrapMonitorFor(qubes, certs, bs)
	m.Sweep(context.Background())

	require.Len(t, bs.dialed, 1)
	status, reason := m.Failure("q1")
	assert.Equal(t, BootstrapUnreachable, status)
	assert.Contains(t, reason, "nothing listening")
}

func TestFailureBacksOffExponentiallyAndSuccessClearsIt(t *testing.T) {
	qubes := &fakeBootstrapQubes{qubes: []*models.Qube{uncertifiedQube("q1", "flaky")}}
	certs := &fakeCertLister{}
	bs := &recordingBootstrapper{result: BootstrapResult{Status: BootstrapUnreachable}}

	now := time.Now().UTC()
	m := bootstrapMonitorFor(qubes, certs, bs)
	m.now = func() time.Time { return now }

	m.Sweep(context.Background())
	require.Len(t, bs.dialed, 1)

	// Immediately again: still backing off, so no second dial.
	m.Sweep(context.Background())
	assert.Len(t, bs.dialed, 1, "a qube was retried before its backoff elapsed")

	// Past the first backoff: dialed again, and the wait grows.
	now = now.Add(bootstrapRetryBase + time.Second)
	m.Sweep(context.Background())
	require.Len(t, bs.dialed, 2)

	now = now.Add(bootstrapRetryBase + time.Second)
	m.Sweep(context.Background())
	assert.Len(t, bs.dialed, 2, "the backoff did not grow after a second failure")

	// A success clears the history entirely.
	now = now.Add(bootstrapRetryMax)
	bs.result = BootstrapResult{Status: BootstrapOK}
	m.Sweep(context.Background())
	require.Len(t, bs.dialed, 3)

	status, _ := m.Failure("q1")
	assert.Empty(t, status, "a successful bootstrap left failure history behind")
}

// A refused token cannot become accepted by waiting — only re-provisioning
// helps. Retrying it on the fast backoff would fail every few seconds and bury
// the failures that mean something.
func TestRefusedTokenBacksOffFarLongerThanAnUnreachableAgent(t *testing.T) {
	qubes := &fakeBootstrapQubes{qubes: []*models.Qube{uncertifiedQube("q1", "stale-token")}}
	certs := &fakeCertLister{}
	bs := &recordingBootstrapper{result: BootstrapResult{Status: BootstrapRefused}}

	now := time.Now().UTC()
	m := bootstrapMonitorFor(qubes, certs, bs)
	m.now = func() time.Time { return now }

	m.Sweep(context.Background())
	require.Len(t, bs.dialed, 1)

	// Well past the normal ceiling, still not retried.
	now = now.Add(bootstrapRetryMax + time.Minute)
	m.Sweep(context.Background())
	assert.Len(t, bs.dialed, 1, "a refused token was retried on the fast backoff")

	// But it IS retried eventually, because re-provisioning happens out of band
	// and the sweep has to notice when it has.
	now = now.Add(bootstrapRefusedRetry)
	m.Sweep(context.Background())
	assert.Len(t, bs.dialed, 2, "a refused token was never retried; re-provisioning would go unnoticed")
}

// A panic under the sweep must not end bootstrapping for the process. Without
// containment no qube created afterwards would ever be certified, and nothing
// would say so once the trace scrolled away.
func TestSweepPanicIsContained(t *testing.T) {
	qubes := &fakeBootstrapQubes{qubes: []*models.Qube{uncertifiedQube("q1", "boom")}}
	certs := &fakeCertLister{}
	bs := &panickingBootstrapper{}

	m := bootstrapMonitorFor(qubes, certs, nil)
	m.bootstrapper = bs

	assert.NotPanics(t, func() { m.sweepGuarded(context.Background()) })
	assert.True(t, bs.called)
}

type panickingBootstrapper struct{ called bool }

func (p *panickingBootstrapper) Bootstrap(context.Context, *models.Qube) BootstrapResult {
	p.called = true
	panic("registry returned something nobody expected")
}

// A disabled sweep must not silently do nothing: under the token design that
// means every new qube boots with a credential nobody redeems.
func TestDisabledMonitorDoesNotStart(t *testing.T) {
	m := NewBootstrapMonitor(&fakeBootstrapQubes{}, &fakeCertLister{}, &recordingBootstrapper{}, 0)
	m.Start() // logs the warning; must not spawn a loop
	m.Shutdown(time.Second)
}

func TestNilMonitorIsSafe(t *testing.T) {
	var m *BootstrapMonitor
	assert.NotPanics(t, func() {
		m.Start()
		m.Shutdown(time.Second)
		status, reason := m.Failure("q1")
		assert.Empty(t, status)
		assert.Empty(t, reason)
	})
}
