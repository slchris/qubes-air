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

func setupTestDB(t *testing.T) (*database.DB, func()) {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "test-*.db")
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

func TestZoneRepository_Create(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewZoneRepository(db)
	ctx := context.Background()

	zone := &models.Zone{
		ID:     "test-zone-1",
		Name:   "Test Zone",
		Type:   models.ZoneTypeProxmox,
		Status: "disconnected",
		Config: models.ZoneConfig{
			Endpoint: "https://proxmox.local:8006",
			Username: "root@pam",
		},
	}

	err := repo.Create(ctx, zone)
	assert.NoError(t, err)

	retrieved, err := repo.GetByID(ctx, zone.ID)
	assert.NoError(t, err)
	assert.Equal(t, zone.ID, retrieved.ID)
	assert.Equal(t, zone.Name, retrieved.Name)
}

func TestZoneRepository_GetByID(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewZoneRepository(db)
	ctx := context.Background()

	_, err := repo.GetByID(ctx, "nonexistent")
	assert.Error(t, err)

	zone := &models.Zone{
		ID:     "test-zone-2",
		Name:   "Test Zone 2",
		Type:   models.ZoneTypeAWS,
		Status: "connected",
	}
	require.NoError(t, repo.Create(ctx, zone))

	retrieved, err := repo.GetByID(ctx, zone.ID)
	assert.NoError(t, err)
	assert.Equal(t, zone.Name, retrieved.Name)
}

func TestZoneRepository_List(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewZoneRepository(db)
	ctx := context.Background()

	zones, err := repo.List(ctx, DefaultZoneListOptions())
	assert.NoError(t, err)
	assert.Empty(t, zones)

	for i := 0; i < 3; i++ {
		zone := &models.Zone{
			ID:     string(rune('a'+i)) + "-zone",
			Name:   "Zone " + string(rune('A'+i)),
			Type:   models.ZoneTypeProxmox,
			Status: "disconnected",
		}
		require.NoError(t, repo.Create(ctx, zone))
	}

	zones, err = repo.List(ctx, DefaultZoneListOptions())
	assert.NoError(t, err)
	assert.Len(t, zones, 3)
}

func TestZoneRepository_Update(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewZoneRepository(db)
	ctx := context.Background()

	zone := &models.Zone{
		ID:     "update-zone",
		Name:   "Original Name",
		Type:   models.ZoneTypeProxmox,
		Status: "disconnected",
	}
	require.NoError(t, repo.Create(ctx, zone))

	zone.Name = "Updated Name"
	zone.Status = "connected"
	err := repo.Update(ctx, zone)
	assert.NoError(t, err)

	retrieved, err := repo.GetByID(ctx, zone.ID)
	assert.NoError(t, err)
	assert.Equal(t, "Updated Name", retrieved.Name)
	assert.Equal(t, "connected", retrieved.Status)
}

func TestZoneRepository_Delete(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewZoneRepository(db)
	ctx := context.Background()

	zone := &models.Zone{
		ID:     "delete-zone",
		Name:   "To Delete",
		Type:   models.ZoneTypeProxmox,
		Status: "disconnected",
	}
	require.NoError(t, repo.Create(ctx, zone))

	err := repo.Delete(ctx, zone.ID)
	assert.NoError(t, err)

	_, err = repo.GetByID(ctx, zone.ID)
	assert.Error(t, err)
}

func TestZoneRepository_UpdateStatus(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	repo := NewZoneRepository(db)
	ctx := context.Background()

	zone := &models.Zone{
		ID:     "status-zone",
		Name:   "Status Zone",
		Type:   models.ZoneTypeProxmox,
		Status: "disconnected",
	}
	require.NoError(t, repo.Create(ctx, zone))

	err := repo.UpdateStatus(ctx, zone.ID, "connected")
	assert.NoError(t, err)

	retrieved, err := repo.GetByID(ctx, zone.ID)
	assert.NoError(t, err)
	assert.Equal(t, "connected", retrieved.Status)
}
