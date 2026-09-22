package service

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/orchestrator"
	"github.com/slchris/qubes-air/console/internal/provider"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeDataKeys records the per-qube key lifecycle for assertions.
type fakeDataKeys struct {
	ensured   []string
	deleted   []string
	deleteErr error
}

func (f *fakeDataKeys) EnsureDataKey(_ context.Context, qubeID string) (string, error) {
	f.ensured = append(f.ensured, qubeID)
	return "k-" + qubeID, nil
}

func (f *fakeDataKeys) DeleteDataKey(_ context.Context, qubeID string) error {
	f.deleted = append(f.deleted, qubeID)
	return f.deleteErr
}

// setupPurgeService builds a QubeService over a real temp DB, with the infra
// store wired so Purge can lift the disk's protection.
func setupPurgeService(t *testing.T, fake *orchestrator.FakeExecutor, dataKeys DataKeyStore) (ZoneService, QubeService, *repository.QubeInfraRepository, func()) {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "qube-purge-test-*.db")
	require.NoError(t, err)
	tmpFile.Close()

	cfg := database.DefaultConfig()
	cfg.DSN = tmpFile.Name()
	db, err := database.New(cfg)
	require.NoError(t, err)

	zoneRepo := repository.NewZoneRepository(db)
	qubeRepo := repository.NewQubeRepository(db)
	infraRepo := repository.NewQubeInfraRepository(db)

	zoneSvc := NewZoneService(zoneRepo, qubeRepo)
	qubeSvc := NewQubeService(qubeRepo, zoneRepo, WithExecutor(fake), WithInfraStore(infraRepo), WithDataKeyStore(dataKeys))

	return zoneSvc, qubeSvc, infraRepo, func() {
		db.Close()
		os.Remove(tmpFile.Name())
	}
}

// suspendedQube creates a qube, suspends it, and records a protected disk, so it
// is in exactly the state Purge accepts.
func suspendedQube(t *testing.T, zoneSvc ZoneService, qubeSvc QubeService, infraRepo *repository.QubeInfraRepository) *models.Qube {
	t.Helper()
	ctx := context.Background()

	zone, err := zoneSvc.Create(ctx, &models.ZoneCreateRequest{Name: "Purge Zone", Type: models.ZoneTypeProxmox})
	require.NoError(t, err)
	_, err = zoneSvc.Connect(ctx, zone.ID)
	require.NoError(t, err)

	op, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{Name: "purge-me", Type: models.QubeTypeApp, ZoneID: zone.ID})
	require.NoError(t, err)
	suspended, err := qubeSvc.Stop(ctx, op.Qube.ID)
	require.NoError(t, err)

	require.NoError(t, infraRepo.Save(ctx, &provider.Infra{
		QubeID: op.Qube.ID, Provider: "proxmox", StorageVMID: 42, Protected: true,
	}))
	return suspended.Qube
}

func TestPurge_RequiresExactConfirmation(t *testing.T) {
	fake := orchestrator.NewFakeExecutor()
	zoneSvc, qubeSvc, infraRepo, cleanup := setupPurgeService(t, fake, nil)
	defer cleanup()
	q := suspendedQube(t, zoneSvc, qubeSvc, infraRepo)
	fake.Reset()

	err := qubeSvc.Purge(context.Background(), q.ID, "wrong-name")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPurgeConfirmation)
	assert.Empty(t, fake.Calls(), "a wrong confirmation must not destroy anything")

	got, err := qubeSvc.GetByID(context.Background(), q.ID)
	require.NoError(t, err)
	assert.Equal(t, models.QubeStatusSuspended, got.Status)
}

func TestPurge_FromRunningIsRefused(t *testing.T) {
	fake := orchestrator.NewFakeExecutor()
	zoneSvc, qubeSvc, _, cleanup := setupPurgeService(t, fake, nil)
	defer cleanup()

	ctx := context.Background()
	zone, err := zoneSvc.Create(ctx, &models.ZoneCreateRequest{Name: "Z", Type: models.ZoneTypeProxmox})
	require.NoError(t, err)
	_, err = zoneSvc.Connect(ctx, zone.ID)
	require.NoError(t, err)
	op, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{Name: "running", Type: models.QubeTypeApp, ZoneID: zone.ID})
	require.NoError(t, err)
	fake.Reset()

	err = qubeSvc.Purge(ctx, op.Qube.ID, "running")
	assert.ErrorIs(t, err, repository.ErrTransitionConflict)
	assert.Empty(t, fake.Calls(), "a running qube must be released before it can be purged")
}

func TestPurge_DestroysDiskAndClearsProtection(t *testing.T) {
	fake := orchestrator.NewFakeExecutor()
	keys := &fakeDataKeys{}
	zoneSvc, qubeSvc, infraRepo, cleanup := setupPurgeService(t, fake, keys)
	defer cleanup()
	ctx := context.Background()

	zone, err := zoneSvc.Create(ctx, &models.ZoneCreateRequest{Name: "Z", Type: models.ZoneTypeProxmox})
	require.NoError(t, err)
	_, err = zoneSvc.Connect(ctx, zone.ID)
	require.NoError(t, err)
	op, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{Name: "goner", Type: models.QubeTypeApp, ZoneID: zone.ID})
	require.NoError(t, err)
	_, err = qubeSvc.Stop(ctx, op.Qube.ID)
	require.NoError(t, err)
	require.NoError(t, infraRepo.Save(ctx, &provider.Infra{QubeID: op.Qube.ID, Provider: "proxmox", StorageVMID: 7, Protected: true}))
	fake.Reset()

	require.NoError(t, qubeSvc.Purge(ctx, op.Qube.ID, "goner"))

	// The Disk is destroyed through the executor...
	calls := fake.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, orchestrator.ActionDestroy, calls[0].Action)

	// ...protection was lifted beforehand (the executor refuses a protected disk)...
	inf, err := infraRepo.Get(ctx, op.Qube.ID)
	require.NoError(t, err)
	require.NotNil(t, inf)
	assert.False(t, inf.Protected)

	// ...and the qube is terminal.
	got, err := qubeSvc.GetByID(ctx, op.Qube.ID)
	require.NoError(t, err)
	assert.Equal(t, models.QubeStatusPurged, got.Status)

	// Create minted the per-qube key; purge shredded it.
	assert.Equal(t, []string{op.Qube.ID}, keys.ensured)
	assert.Equal(t, []string{op.Qube.ID}, keys.deleted)
}

func TestPurge_AlreadyPurgedIsNoop(t *testing.T) {
	fake := orchestrator.NewFakeExecutor()
	zoneSvc, qubeSvc, infraRepo, cleanup := setupPurgeService(t, fake, nil)
	defer cleanup()
	q := suspendedQube(t, zoneSvc, qubeSvc, infraRepo)
	fake.Reset()
	ctx := context.Background()

	require.NoError(t, qubeSvc.Purge(ctx, q.ID, q.Name))
	require.NoError(t, qubeSvc.Purge(ctx, q.ID, q.Name), "purging an already-purged qube is a no-op")
	assert.Len(t, fake.Calls(), 1, "the second purge must not enqueue another destroy")
}

func TestPurgePartialFailureCannotResume(t *testing.T) {
	fake := orchestrator.NewFakeExecutor()
	keys := &fakeDataKeys{deleteErr: errors.New("injected key deletion failure")}
	zoneSvc, qubeSvc, infraRepo, cleanup := setupPurgeService(t, fake, keys)
	defer cleanup()
	q := suspendedQube(t, zoneSvc, qubeSvc, infraRepo)
	ctx := context.Background()
	require.Error(t, qubeSvc.Purge(ctx, q.ID, q.Name))
	fake.Reset()
	_, err := qubeSvc.Start(ctx, q.ID)
	require.Error(t, err, "purge has irreversible side effects; resume must be refused")
	assert.Empty(t, fake.Calls())
	keys.deleteErr = nil
	require.NoError(t, qubeSvc.Purge(ctx, q.ID, q.Name), "explicit purge retry must remain available")
}

type purgeFailingInfra struct{ orchestrator.InfraStore }

func (s purgeFailingInfra) Save(context.Context, *provider.Infra) error {
	return errors.New("protection save failed")
}

type purgeFailingQueue struct{}

func (purgeFailingQueue) Submit(
	context.Context, string, string, orchestrator.Action, ...orchestrator.Step,
) (*orchestrator.Job, error) {
	return nil, orchestrator.ErrQueueFull
}

func TestPurgePreparationAndQueueFailuresRemainRetryable(t *testing.T) {
	for _, stage := range []string{"protection", "queue"} {
		t.Run(stage, func(t *testing.T) {
			fake := orchestrator.NewFakeExecutor()
			zoneSvc, svc, infra, cleanup := setupPurgeService(t, fake, &fakeDataKeys{})
			defer cleanup()
			q := suspendedQube(t, zoneSvc, svc, infra)
			impl := svc.(*QubeServiceImpl)
			if stage == "protection" {
				impl.infraStore = purgeFailingInfra{infra}
			} else {
				impl.submitter = purgeFailingQueue{}
			}
			ctx := context.Background()
			require.Error(t, svc.Purge(ctx, q.ID, q.Name))
			current, err := svc.GetByID(ctx, q.ID)
			require.NoError(t, err)
			assert.True(t, current.PurgeRequested)
			assert.Equal(t, models.QubeStatusError, current.Status)
			_, err = svc.Start(ctx, q.ID)
			require.Error(t, err)
			impl.infraStore, impl.submitter = infra, nil
			require.NoError(t, svc.Purge(ctx, q.ID, q.Name))
		})
	}
}
