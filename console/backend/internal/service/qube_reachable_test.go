package service

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/transport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupWithTransport builds a QubeService with an injected transport plus a
// connected zone and a qube in it, returning the qube id.
func setupWithTransport(t *testing.T, xport transport.Transport) (QubeService, string, func()) {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "qube-reachable-test-*.db")
	require.NoError(t, err)
	tmpFile.Close()

	cfg := database.DefaultConfig()
	cfg.DSN = tmpFile.Name()
	db, err := database.New(cfg)
	require.NoError(t, err)

	zoneRepo := repository.NewZoneRepository(db)
	qubeRepo := repository.NewQubeRepository(db)
	zoneSvc := NewZoneService(zoneRepo, qubeRepo)
	qubeSvc := NewQubeService(qubeRepo, zoneRepo, WithTransport(xport))

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)
	createdOp, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name:   "reach-qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	})
	require.NoError(t, err)

	cleanup := func() {
		db.Close()
		os.Remove(tmpFile.Name())
	}
	return qubeSvc, createdOp.Qube.ID, cleanup
}

func TestCheckReachable_OK(t *testing.T) {
	fake := &transport.FakeTransport{
		RespFn: func(target, service string, _ []byte) ([]byte, error) {
			// The service must forward the qube name as target and the ping service.
			if target != "reach-qube" || service != pingService {
				return nil, errors.New("unexpected call")
			}
			return []byte("pong\n"), nil
		},
	}
	svc, id, cleanup := setupWithTransport(t, fake)
	defer cleanup()

	resp, err := svc.CheckReachable(context.Background(), id)
	assert.NoError(t, err)
	assert.Equal(t, "pong", resp) // trimmed
	assert.Equal(t, 1, fake.CallCount())
	assert.Equal(t, "reach-qube", fake.Calls[0].Target)
	assert.Equal(t, pingService, fake.Calls[0].Service)
}

func TestCheckReachable_TransportError(t *testing.T) {
	fake := &transport.FakeTransport{
		RespFn: func(_, _ string, _ []byte) ([]byte, error) {
			return nil, errors.New("tunnel down")
		},
	}
	svc, id, cleanup := setupWithTransport(t, fake)
	defer cleanup()

	_, err := svc.CheckReachable(context.Background(), id)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnreachable)
}

func TestCheckReachable_NoTransportConfigured(t *testing.T) {
	// Default service uses NoopTransport → fails loudly with ErrUnreachable.
	zoneSvc, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)
	createdOp, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name:   "noop-qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	})
	require.NoError(t, err)

	_, err = qubeSvc.CheckReachable(ctx, createdOp.Qube.ID)
	assert.ErrorIs(t, err, ErrUnreachable)
}

func TestCheckReachable_NotFound(t *testing.T) {
	svc, _, cleanup := setupWithTransport(t, &transport.FakeTransport{})
	defer cleanup()

	_, err := svc.CheckReachable(context.Background(), "does-not-exist")
	assert.ErrorIs(t, err, ErrQubeNotFound)
}

// setupReachableWithRepo is setupWithTransport plus the repository, so a test
// can read back what a probe recorded.
func setupReachableWithRepo(t *testing.T, xport transport.Transport) (QubeService, repository.QubeRepository, string) {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "qube-reachable-health-*.db")
	require.NoError(t, err)
	tmpFile.Close()

	cfg := database.DefaultConfig()
	cfg.DSN = tmpFile.Name()
	db, err := database.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		db.Close()
		os.Remove(tmpFile.Name())
	})

	zoneRepo := repository.NewZoneRepository(db)
	qubeRepo := repository.NewQubeRepository(db)
	zoneSvc := NewZoneService(zoneRepo, qubeRepo)
	qubeSvc := NewQubeService(qubeRepo, zoneRepo, WithTransport(xport))

	zone := createConnectedZone(t, zoneSvc)
	op, err := qubeSvc.Create(context.Background(), &models.QubeCreateRequest{
		Name:   "recorded-qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	})
	require.NoError(t, err)
	return qubeSvc, qubeRepo, op.Qube.ID
}

// TestCheckReachable_RecordsWhatItFound is the reconciliation between the
// on-demand endpoint and the background prober.
//
// CheckReachable used to be a second, separate implementation that asked the
// global transport and threw the answer away. Two code paths answering "is this
// agent alive" is how the duplicate systemd unit went unnoticed earlier in this
// project: whichever one you happened to look at told you something, and they
// were not obliged to agree. Now a manual check writes the same field a sweep
// would, so the API and the stored health cannot diverge.
func TestCheckReachable_RecordsWhatItFound(t *testing.T) {
	fake := &transport.FakeTransport{
		RespFn: func(_, _ string, _ []byte) ([]byte, error) { return []byte("pong recorded-qube\n"), nil },
	}
	svc, qubeRepo, id := setupReachableWithRepo(t, fake)
	ctx := context.Background()

	before, err := qubeRepo.GetByID(ctx, id)
	require.NoError(t, err)
	require.Equal(t, models.AgentHealthUnknown, before.AgentHealth)

	resp, err := svc.CheckReachable(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "pong recorded-qube", resp)

	after, err := qubeRepo.GetByID(ctx, id)
	require.NoError(t, err)

	// Recorded, but NOT as healthy. This fake answers through the legacy global
	// transport, which is pinned to one endpoint whose invoker ignores the
	// target name ("target carries no authority", internal/agent/invoker.go) —
	// so it replies for every qube alike. Calling that healthy would store a
	// verdict about a machine nothing ever contacted; a qube with no address at
	// all would go green off a pong naming some other remote.
	//
	// Only a per-qube probe, which dials this qube's own address and binds the
	// peer certificate to its name, can produce healthy. See
	// TestCheckReachable_PrefersThePerQubeProber.
	assert.Equal(t, models.AgentHealthUnknown, after.AgentHealth,
		"a pong that cannot be attributed to THIS qube is not evidence about it")
	require.NotNil(t, after.AgentLastProbedAt,
		"an on-demand probe is still a probe; discarding the attempt is what left the console guessing")
}

// TestCheckReachable_RecordsFailureWithItsReason — the reason is the payload
// that saves an SSH session. "unreachable" on its own does not.
func TestCheckReachable_RecordsFailureWithItsReason(t *testing.T) {
	fake := &transport.FakeTransport{
		RespFn: func(_, _ string, _ []byte) ([]byte, error) {
			return nil, errors.New("connection refused")
		},
	}
	svc, qubeRepo, id := setupReachableWithRepo(t, fake)
	ctx := context.Background()

	_, err := svc.CheckReachable(ctx, id)
	require.ErrorIs(t, err, ErrUnreachable)
	assert.Contains(t, err.Error(), "connection refused", "the real cause must survive to the caller")

	after, err := qubeRepo.GetByID(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, models.AgentHealthUnreachable, after.AgentHealth)
	assert.Contains(t, after.AgentLastError, "connection refused")
	assert.Nil(t, after.AgentLastHealthyAt, "this agent has never answered")
}

// TestCheckReachable_UnconfiguredStaysUnknown — a console that cannot probe has
// learned nothing. Recording "unreachable" here would blame the agent for this
// console's own missing configuration, and an operator would go debug a qube
// that is perfectly fine.
func TestCheckReachable_UnconfiguredStaysUnknown(t *testing.T) {
	svc, qubeRepo, id := setupReachableWithRepo(t, transport.NoopTransport{})
	ctx := context.Background()

	_, err := svc.CheckReachable(ctx, id)
	require.ErrorIs(t, err, ErrUnreachable)

	after, err := qubeRepo.GetByID(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, models.AgentHealthUnknown, after.AgentHealth)

	// The ATTEMPT is recorded even though nothing was learned.
	//
	// Skipping the write looked conservative and was the opposite: the row kept
	// whatever it held before — including a stale "healthy" — along with its old
	// timestamp, so a console that had LOST the ability to probe presented a
	// green verdict indistinguishable from one confirmed a second ago. Writing
	// unknown with a fresh time makes the loss of visibility itself visible.
	require.NotNil(t, after.AgentLastProbedAt,
		"a console that can no longer see must say when it last tried, or stale green stands forever")
	assert.NotEmpty(t, after.AgentLastError, "and it must say why")
}

// TestCheckReachable_PrefersThePerQubeProber — with a prober wired, the answer
// must come from THIS qube's own address.
//
// The global transport is pinned to one configured RemoteEndpoint, so on a
// console with more than one remote it answers about whichever qube that
// endpoint happens to be. Falling back to it while a prober is available would
// reintroduce exactly the confident-but-wrong reading this feature removes.
func TestCheckReachable_PrefersThePerQubeProber(t *testing.T) {
	ca := newCA(t)
	addr, _ := startAgent(t, ca, ca, "agent-probed-qube", &fakeInvoker{resp: []byte("pong")})
	host, port := hostPort(t, addr)

	// The transport would answer happily if it were consulted. It must not be.
	fake := &transport.FakeTransport{
		RespFn: func(_, _ string, _ []byte) ([]byte, error) {
			return []byte("pong from the wrong qube"), nil
		},
	}

	tmpFile, err := os.CreateTemp("", "qube-prober-pref-*.db")
	require.NoError(t, err)
	tmpFile.Close()
	cfg := database.DefaultConfig()
	cfg.DSN = tmpFile.Name()
	db, err := database.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() {
		db.Close()
		os.Remove(tmpFile.Name())
	})

	zoneRepo := repository.NewZoneRepository(db)
	qubeRepo := repository.NewQubeRepository(db)
	zoneSvc := NewZoneService(zoneRepo, qubeRepo)
	prober := NewAgentProber(staticCA{ca: ca}, nil, "0.0.0.0:"+port, 10*time.Second)
	qubeSvc := NewQubeService(qubeRepo, zoneRepo, WithTransport(fake), WithAgentProber(prober))

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)
	op, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name: "probed-qube", Type: models.QubeTypeApp, ZoneID: zone.ID,
	})
	require.NoError(t, err)
	require.NoError(t, qubeRepo.UpdateIPAddress(ctx, op.Qube.ID, host))

	resp, err := qubeSvc.CheckReachable(ctx, op.Qube.ID)
	require.NoError(t, err)
	assert.Contains(t, resp, "probed-qube", "the reply must come from the qube that was asked about")
	assert.Zero(t, fake.CallCount(), "the pinned global transport must not be consulted when a prober exists")

	after, err := qubeRepo.GetByID(ctx, op.Qube.ID)
	require.NoError(t, err)
	assert.Equal(t, models.AgentHealthHealthy, after.AgentHealth)
}
