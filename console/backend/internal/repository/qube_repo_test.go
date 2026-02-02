package repository

import (
	"context"
	"os"
	"testing"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupQubeTestDB(t *testing.T) (*database.DB, func()) {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "qube-test-*.db")
	require.NoError(t, err)
	tmpFile.Close()

	cfg := database.DefaultConfig()
	cfg.DSN = tmpFile.Name()

	db, err := database.New(cfg)
	require.NoError(t, err)

	cleanup := func() {
		db.Close()
		os.Remove(tmpFile.Name())
	}

	return db, cleanup
}

func createTestZone(t *testing.T, repo ZoneRepository) *models.Zone {
	t.Helper()

	zone := &models.Zone{
		ID:     "test-zone",
		Name:   "Test Zone",
		Type:   models.ZoneTypeProxmox,
		Status: "connected",
	}

	err := repo.Create(context.Background(), zone)
	require.NoError(t, err)
	return zone
}

func TestQubeRepository_Create(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()

	zoneRepo := NewZoneRepository(db)
	repo := NewQubeRepository(db)
	ctx := context.Background()

	zone := createTestZone(t, zoneRepo)

	qube := &models.Qube{
		ID:     "test-qube-1",
		Name:   "Test Qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
		Status: models.QubeStatusStopped,
		Spec: models.QubeSpec{
			VCPU:   2,
			Memory: 2048,
		},
	}

	err := repo.Create(ctx, qube)
	assert.NoError(t, err)

	retrieved, err := repo.GetByID(ctx, qube.ID)
	assert.NoError(t, err)
	assert.Equal(t, qube.ID, retrieved.ID)
	assert.Equal(t, qube.Spec.VCPU, retrieved.Spec.VCPU)
}

func TestQubeRepository_GetByID(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()

	zoneRepo := NewZoneRepository(db)
	repo := NewQubeRepository(db)
	ctx := context.Background()

	_, err := repo.GetByID(ctx, "nonexistent")
	assert.Error(t, err)

	zone := createTestZone(t, zoneRepo)

	qube := &models.Qube{
		ID:     "test-qube-2",
		Name:   "Test Qube 2",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
		Status: models.QubeStatusStopped,
	}
	require.NoError(t, repo.Create(ctx, qube))

	retrieved, err := repo.GetByID(ctx, qube.ID)
	assert.NoError(t, err)
	assert.Equal(t, qube.Name, retrieved.Name)
}

func TestQubeRepository_List(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()

	zoneRepo := NewZoneRepository(db)
	repo := NewQubeRepository(db)
	ctx := context.Background()

	qubes, err := repo.List(ctx, DefaultQubeListOptions())
	assert.NoError(t, err)
	assert.Empty(t, qubes)

	zone := createTestZone(t, zoneRepo)

	for i := 0; i < 3; i++ {
		qube := &models.Qube{
			ID:     string(rune('a'+i)) + "-qube",
			Name:   "Qube " + string(rune('A'+i)),
			Type:   models.QubeTypeApp,
			ZoneID: zone.ID,
			Status: models.QubeStatusStopped,
		}
		require.NoError(t, repo.Create(ctx, qube))
	}

	qubes, err = repo.List(ctx, DefaultQubeListOptions())
	assert.NoError(t, err)
	assert.Len(t, qubes, 3)
}

func TestQubeRepository_Update(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()

	zoneRepo := NewZoneRepository(db)
	repo := NewQubeRepository(db)
	ctx := context.Background()

	zone := createTestZone(t, zoneRepo)

	qube := &models.Qube{
		ID:     "update-qube",
		Name:   "Original Name",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
		Status: models.QubeStatusStopped,
		Spec: models.QubeSpec{
			VCPU:   1,
			Memory: 1024,
		},
	}
	require.NoError(t, repo.Create(ctx, qube))

	qube.Name = "Updated Name"
	qube.Spec.VCPU = 4
	err := repo.Update(ctx, qube)
	assert.NoError(t, err)

	retrieved, err := repo.GetByID(ctx, qube.ID)
	assert.NoError(t, err)
	assert.Equal(t, "Updated Name", retrieved.Name)
	assert.Equal(t, 4, retrieved.Spec.VCPU)
}

func TestQubeRepository_Delete(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()

	zoneRepo := NewZoneRepository(db)
	repo := NewQubeRepository(db)
	ctx := context.Background()

	zone := createTestZone(t, zoneRepo)

	qube := &models.Qube{
		ID:     "delete-qube",
		Name:   "To Delete",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
		Status: models.QubeStatusStopped,
	}
	require.NoError(t, repo.Create(ctx, qube))

	err := repo.Delete(ctx, qube.ID)
	assert.NoError(t, err)

	_, err = repo.GetByID(ctx, qube.ID)
	assert.Error(t, err)
}

func TestQubeRepository_UpdateStatus(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()

	zoneRepo := NewZoneRepository(db)
	repo := NewQubeRepository(db)
	ctx := context.Background()

	zone := createTestZone(t, zoneRepo)

	qube := &models.Qube{
		ID:     "status-qube",
		Name:   "Status Qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
		Status: models.QubeStatusStopped,
	}
	require.NoError(t, repo.Create(ctx, qube))

	err := repo.UpdateStatus(ctx, qube.ID, models.QubeStatusRunning)
	assert.NoError(t, err)

	retrieved, err := repo.GetByID(ctx, qube.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.QubeStatusRunning, retrieved.Status)
}
