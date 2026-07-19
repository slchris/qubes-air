package service

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/orchestrator"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupQubeServiceWithExecutor builds a QubeService wired to the given executor,
// backed by a real (temp) SQLite DB so status transitions are observable.
func setupQubeServiceWithExecutor(t *testing.T, exec orchestrator.Executor) (ZoneService, QubeService, func()) {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "qube-orch-test-*.db")
	require.NoError(t, err)
	tmpFile.Close()

	cfg := database.DefaultConfig()
	cfg.DSN = tmpFile.Name()

	db, err := database.New(cfg)
	require.NoError(t, err)

	zoneRepo := repository.NewZoneRepository(db)
	qubeRepo := repository.NewQubeRepository(db)

	zoneSvc := NewZoneService(zoneRepo, qubeRepo)
	qubeSvc := NewQubeService(qubeRepo, zoneRepo, WithExecutor(exec))

	cleanup := func() {
		db.Close()
		os.Remove(tmpFile.Name())
	}
	return zoneSvc, qubeSvc, cleanup
}

// orchTestQubeName is a terraform-safe qube name used by the orchestration
// tests (maps to a terraform -target address).
const orchTestQubeName = "web01"

// createQubeForOrch creates a qube and leaves it SUSPENDED, ready to be started.
//
// Create now provisions, so it lands on running with a provision call recorded.
// The tests below assert on exactly one executor call, so the fixture suspends
// the qube and clears the recording to give them a clean slate.
func createQubeForOrch(t *testing.T, zoneSvc ZoneService, qubeSvc QubeService, fake *orchestrator.FakeExecutor) *models.Qube {
	t.Helper()
	ctx := context.Background()

	zone, err := zoneSvc.Create(ctx, &models.ZoneCreateRequest{
		Name: "Orch Zone",
		Type: models.ZoneTypeProxmox,
	})
	require.NoError(t, err)
	_, err = zoneSvc.Connect(ctx, zone.ID)
	require.NoError(t, err)

	qubeOp, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name:   orchTestQubeName,
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	})
	require.NoError(t, err)

	stopped, err := qubeSvc.Stop(context.Background(), qubeOp.Qube.ID)
	require.NoError(t, err)
	if fake != nil {
		fake.Reset()
	}
	return stopped.Qube
}

// Start must call the executor's Resume, and only then flip DB status. We assert
// both that Resume was called and that the recorded status is running.
func TestStart_CallsResumeThenUpdatesStatus(t *testing.T) {
	fake := orchestrator.NewFakeExecutor()
	zoneSvc, qubeSvc, cleanup := setupQubeServiceWithExecutor(t, fake)
	defer cleanup()

	ctx := context.Background()
	qube := createQubeForOrch(t, zoneSvc, qubeSvc, fake)

	gotOp, err := qubeSvc.Start(ctx, qube.ID)
	require.NoError(t, err)
	assert.Equal(t, models.QubeStatusRunning, gotOp.Qube.Status)

	calls := fake.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, orchestrator.ActionResume, calls[0].Action)
	assert.Equal(t, orchTestQubeName, calls[0].Qube)
}

// When the executor's Resume fails, the qube must NOT be reported running — we
// never claim a qube is up that the infrastructure did not bring up. It lands on
// "error" rather than silently reverting, so the failure is visible; error is a
// valid source status for a retry.
func TestStart_ExecutorFailure_StatusUnchanged(t *testing.T) {
	fake := orchestrator.NewFakeExecutor()
	fake.FailOn[orchestrator.ActionResume] = errors.New("terraform apply failed")

	zoneSvc, qubeSvc, cleanup := setupQubeServiceWithExecutor(t, fake)
	defer cleanup()

	ctx := context.Background()
	qube := createQubeForOrch(t, zoneSvc, qubeSvc, fake)

	_, err := qubeSvc.Start(ctx, qube.ID)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrOrchestration)

	// Status must be unchanged (still the initial Stopped from Create).
	after, err := qubeSvc.GetByID(ctx, qube.ID)
	require.NoError(t, err)
	assert.NotEqual(t, models.QubeStatusRunning, after.Status,
		"must never report running when resume failed")
	assert.Equal(t, models.QubeStatusError, after.Status,
		"a failed resume surfaces as error, which is a valid source status for a retry")
}

// Stop must call the executor's Suspend, then set status to Suspended.
func TestStop_CallsSuspendThenUpdatesStatus(t *testing.T) {
	fake := orchestrator.NewFakeExecutor()
	zoneSvc, qubeSvc, cleanup := setupQubeServiceWithExecutor(t, fake)
	defer cleanup()

	ctx := context.Background()
	qube := createQubeForOrch(t, zoneSvc, qubeSvc, fake)

	// Bring it up first.
	_, err := qubeSvc.Start(ctx, qube.ID)
	require.NoError(t, err)
	fake.Reset()

	gotOp, err := qubeSvc.Stop(ctx, qube.ID)
	require.NoError(t, err)
	assert.Equal(t, models.QubeStatusSuspended, gotOp.Qube.Status)

	calls := fake.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, orchestrator.ActionSuspend, calls[0].Action)
	assert.Equal(t, orchTestQubeName, calls[0].Qube)
}

// When Suspend fails, status must remain running (the qube is still up).
func TestStop_ExecutorFailure_StatusUnchanged(t *testing.T) {
	fake := orchestrator.NewFakeExecutor()
	zoneSvc, qubeSvc, cleanup := setupQubeServiceWithExecutor(t, fake)
	defer cleanup()

	ctx := context.Background()
	qube := createQubeForOrch(t, zoneSvc, qubeSvc, fake)
	_, err := qubeSvc.Start(ctx, qube.ID)
	require.NoError(t, err)

	// Now make suspend fail.
	fake.FailOn[orchestrator.ActionSuspend] = errors.New("terraform destroy failed")

	_, err = qubeSvc.Stop(ctx, qube.ID)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrOrchestration)

	after, err := qubeSvc.GetByID(ctx, qube.ID)
	require.NoError(t, err)
	assert.NotEqual(t, models.QubeStatusSuspended, after.Status,
		"must never report suspended when suspend failed — the compute instance may still be running and billing")
	assert.Equal(t, models.QubeStatusError, after.Status)
}

// A precondition failure (zone disconnected) must be caught BEFORE the executor
// is called — we don't want to touch infra for a qube we won't start.
func TestStart_ZoneDisconnected_ExecutorNotCalled(t *testing.T) {
	fake := orchestrator.NewFakeExecutor()
	zoneSvc, qubeSvc, cleanup := setupQubeServiceWithExecutor(t, fake)
	defer cleanup()

	ctx := context.Background()
	qube := createQubeForOrch(t, zoneSvc, qubeSvc, fake)

	// Disconnect the zone.
	_, err := zoneSvc.Disconnect(ctx, qube.ZoneID)
	require.NoError(t, err)

	_, err = qubeSvc.Start(ctx, qube.ID)
	require.ErrorIs(t, err, ErrZoneDisconnected)

	assert.Empty(t, fake.Calls(), "executor must not be called when precondition fails")
}

// The default NoopExecutor path (no WithExecutor) still works and rejects unsafe
// qube names consistently, without touching any infrastructure.
func TestCreate_RejectsUnsafeName(t *testing.T) {
	zoneSvc, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)

	// A display-style name with a space is not a valid terraform map key or
	// -target address. Create now rejects it up front rather than accepting a
	// qube that could never be started.
	_, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name:   "Unsafe Name", // deliberately invalid: spaces are not allowed
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	})
	require.Error(t, err, "create must reject a name that is not terraform-safe")
	assert.ErrorIs(t, err, ErrInvalidQubeName)
}
