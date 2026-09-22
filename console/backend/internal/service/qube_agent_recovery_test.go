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

// probeEnv wires a service whose probes fail against a real database and
// returns a running qube, so a probe result can be followed all the way into
// the row the API serves.
func probeEnv(t *testing.T, xport transport.Transport) (QubeService, *models.Qube) {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "qube-recovery-probe-*.db")
	require.NoError(t, err)
	tmpFile.Close()
	t.Cleanup(func() { os.Remove(tmpFile.Name()) })

	cfg := database.DefaultConfig()
	cfg.DSN = tmpFile.Name()
	db, err := database.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	zoneRepo := repository.NewZoneRepository(db)
	qubeRepo := repository.NewQubeRepository(db)
	zoneSvc := NewZoneService(zoneRepo, qubeRepo)
	qubeSvc := NewQubeService(qubeRepo, zoneRepo, WithTransport(xport))

	zone := createConnectedZone(t, zoneSvc)
	createdOp, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name: "probed-qube", Type: models.QubeTypeApp, ZoneID: zone.ID,
	})
	require.NoError(t, err)
	// A parked qube has no agent to have an opinion about, so the failure this
	// test is about requires a running compute instance.
	require.NoError(t, qubeRepo.UpdateStatus(ctx, createdOp.Qube.ID, models.QubeStatusRunning))

	qube, err := qubeSvc.GetByID(ctx, createdOp.Qube.ID)
	require.NoError(t, err)
	return qubeSvc, qube
}

// TestFailedProbeStartsAndKeepsTheFailureStreak — the classification is only
// useful if the production path fills it in: a real failing probe must start the
// streak, and a second one must not move it forward. A per-sweep overwrite
// would leave a permanently dead agent looking perpetually one probe old, so
// this is checked through the service, not only through the repository.
func TestFailedProbeStartsAndKeepsTheFailureStreak(t *testing.T) {
	fake := &transport.FakeTransport{
		RespFn: func(_, _ string, _ []byte) ([]byte, error) {
			return nil, errors.New("connection refused")
		},
	}
	svc, qube := probeEnv(t, fake)
	ctx := context.Background()

	_, err := svc.CheckReachable(ctx, qube.ID)
	require.ErrorIs(t, err, ErrUnreachable)

	first, err := svc.GetByID(ctx, qube.ID)
	require.NoError(t, err)
	assert.Equal(t, models.AgentHealthUnreachable, first.AgentHealth)
	require.NotNil(t, first.AgentFailingSince, "a failed probe must start the streak")
	assert.Equal(t, models.AgentRecoveryPending, first.AgentRecovery,
		"a failure this young may still be a restart in flight")

	_, err = svc.CheckReachable(ctx, qube.ID)
	require.ErrorIs(t, err, ErrUnreachable)

	second, err := svc.GetByID(ctx, qube.ID)
	require.NoError(t, err)
	require.NotNil(t, second.AgentFailingSince)
	assert.True(t, second.AgentFailingSince.Equal(*first.AgentFailingSince),
		"the streak start must not move: got %v, want %v", second.AgentFailingSince, first.AgentFailingSince)
	assert.Equal(t, models.AgentRecoveryPending, second.AgentRecovery)
	assert.GreaterOrEqual(t, second.AgentLastProbedAt.Sub(*second.AgentFailingSince), time.Duration(0),
		"the recorded span must be the failure's age, not a negative interval")
}

// TestNonFailingProbeClearsTheStreakThroughTheService — the other end of the
// same path: once the agent answers, the alarm has to be gone from the row the
// console serves. A streak that survived recovery would keep telling an operator
// to run reset-failed on a working agent.
//
// A pong over the global transport is recorded as UNKNOWN, not healthy —
// deliberately, because that transport answers for whatever remote it is pinned
// to and cannot be attributed to this qube (see
// TestNonAuthoritativeSuccessIsNotHealthy). What this test is about is the
// streak: any verdict that is not "failing" must end it.
func TestNonFailingProbeClearsTheStreakThroughTheService(t *testing.T) {
	failing := true
	fake := &transport.FakeTransport{
		RespFn: func(_, _ string, _ []byte) ([]byte, error) {
			if failing {
				return nil, errors.New("connection refused")
			}
			return []byte("pong\n"), nil
		},
	}
	svc, qube := probeEnv(t, fake)
	ctx := context.Background()

	_, err := svc.CheckReachable(ctx, qube.ID)
	require.ErrorIs(t, err, ErrUnreachable)

	first, err := svc.GetByID(ctx, qube.ID)
	require.NoError(t, err)
	require.NotNil(t, first.AgentFailingSince)

	failing = false
	pong, err := svc.CheckReachable(ctx, qube.ID)
	require.NoError(t, err)
	assert.Equal(t, "pong", pong)

	recovered, err := svc.GetByID(ctx, qube.ID)
	require.NoError(t, err)
	assert.Equal(t, models.AgentHealthUnknown, recovered.AgentHealth,
		"a non-authoritative pong is not evidence about this qube")
	assert.Nil(t, recovered.AgentFailingSince, "the streak must be over")
	assert.Equal(t, models.AgentRecoveryNone, recovered.AgentRecovery)
	assert.Empty(t, recovered.AgentLastError)
}
