package service

import (
	"context"
	"os"
	"testing"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestServices(t *testing.T) (ZoneService, QubeService, func()) {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "service-test-*.db")
	require.NoError(t, err)
	tmpFile.Close()

	cfg := database.DefaultConfig()
	cfg.DSN = tmpFile.Name()

	db, err := database.New(cfg)
	require.NoError(t, err)

	zoneRepo := repository.NewZoneRepository(db)
	qubeRepo := repository.NewQubeRepository(db)

	zoneSvc := NewZoneService(zoneRepo, qubeRepo)
	qubeSvc := NewQubeService(qubeRepo, zoneRepo)

	cleanup := func() {
		db.Close()
		os.Remove(tmpFile.Name())
	}

	return zoneSvc, qubeSvc, cleanup
}

func TestZoneService_Create(t *testing.T) {
	zoneSvc, _, cleanup := setupTestServices(t)
	defer cleanup()

	ctx := context.Background()

	req := &models.ZoneCreateRequest{
		Name: "Test Zone",
		Type: models.ZoneTypeProxmox,
		Config: models.ZoneConfig{
			Endpoint: "https://proxmox.local:8006",
			Username: "root@pam",
		},
	}

	zone, err := zoneSvc.Create(ctx, req)
	assert.NoError(t, err)
	assert.NotEmpty(t, zone.ID)
	assert.Equal(t, req.Name, zone.Name)
	assert.Equal(t, "disconnected", zone.Status)
}

func TestZoneService_Create_InvalidType(t *testing.T) {
	zoneSvc, _, cleanup := setupTestServices(t)
	defer cleanup()

	ctx := context.Background()

	req := &models.ZoneCreateRequest{
		Name: "Invalid Zone",
		Type: "invalid-type",
	}

	_, err := zoneSvc.Create(ctx, req)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidZoneType)
}

func TestZoneService_Create_EmptyName(t *testing.T) {
	zoneSvc, _, cleanup := setupTestServices(t)
	defer cleanup()

	ctx := context.Background()

	req := &models.ZoneCreateRequest{
		Name: "",
		Type: models.ZoneTypeProxmox,
	}

	_, err := zoneSvc.Create(ctx, req)
	assert.Error(t, err)
}

func TestZoneService_GetByID(t *testing.T) {
	zoneSvc, _, cleanup := setupTestServices(t)
	defer cleanup()

	ctx := context.Background()

	req := &models.ZoneCreateRequest{
		Name: "Get Zone",
		Type: models.ZoneTypeAWS,
	}
	created, err := zoneSvc.Create(ctx, req)
	require.NoError(t, err)

	zone, err := zoneSvc.GetByID(ctx, created.ID)
	assert.NoError(t, err)
	assert.Equal(t, created.ID, zone.ID)
}

func TestZoneService_GetByID_NotFound(t *testing.T) {
	zoneSvc, _, cleanup := setupTestServices(t)
	defer cleanup()

	ctx := context.Background()

	_, err := zoneSvc.GetByID(ctx, "nonexistent-id")
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrZoneNotFound)
}

func TestZoneService_List(t *testing.T) {
	zoneSvc, _, cleanup := setupTestServices(t)
	defer cleanup()

	ctx := context.Background()

	for i := 0; i < 3; i++ {
		req := &models.ZoneCreateRequest{
			Name: "Zone " + string(rune('A'+i)),
			Type: models.ZoneTypeProxmox,
		}
		_, err := zoneSvc.Create(ctx, req)
		require.NoError(t, err)
	}

	zones, err := zoneSvc.List(ctx, repository.DefaultZoneListOptions())
	assert.NoError(t, err)
	assert.Len(t, zones, 3)
}

func TestZoneService_Update(t *testing.T) {
	zoneSvc, _, cleanup := setupTestServices(t)
	defer cleanup()

	ctx := context.Background()

	createReq := &models.ZoneCreateRequest{
		Name: "Original",
		Type: models.ZoneTypeProxmox,
	}
	created, err := zoneSvc.Create(ctx, createReq)
	require.NoError(t, err)

	newName := "Updated"
	updateReq := &models.ZoneUpdateRequest{
		Name: &newName,
	}
	updated, err := zoneSvc.Update(ctx, created.ID, updateReq)
	assert.NoError(t, err)
	assert.Equal(t, "Updated", updated.Name)
}

func TestZoneService_Delete(t *testing.T) {
	zoneSvc, _, cleanup := setupTestServices(t)
	defer cleanup()

	ctx := context.Background()

	req := &models.ZoneCreateRequest{
		Name: "To Delete",
		Type: models.ZoneTypeProxmox,
	}
	created, err := zoneSvc.Create(ctx, req)
	require.NoError(t, err)

	err = zoneSvc.Delete(ctx, created.ID)
	assert.NoError(t, err)

	_, err = zoneSvc.GetByID(ctx, created.ID)
	assert.ErrorIs(t, err, ErrZoneNotFound)
}

func TestZoneService_Connect(t *testing.T) {
	zoneSvc, _, cleanup := setupTestServices(t)
	defer cleanup()

	ctx := context.Background()

	req := &models.ZoneCreateRequest{
		Name: "Connect Zone",
		Type: models.ZoneTypeProxmox,
	}
	created, err := zoneSvc.Create(ctx, req)
	require.NoError(t, err)

	zone, err := zoneSvc.Connect(ctx, created.ID)
	assert.NoError(t, err)
	assert.Equal(t, "connected", zone.Status)
}

func TestZoneService_Disconnect(t *testing.T) {
	zoneSvc, _, cleanup := setupTestServices(t)
	defer cleanup()

	ctx := context.Background()

	req := &models.ZoneCreateRequest{
		Name: "Disconnect Zone",
		Type: models.ZoneTypeProxmox,
	}
	created, err := zoneSvc.Create(ctx, req)
	require.NoError(t, err)
	_, err = zoneSvc.Connect(ctx, created.ID)
	require.NoError(t, err)

	zone, err := zoneSvc.Disconnect(ctx, created.ID)
	assert.NoError(t, err)
	assert.Equal(t, "disconnected", zone.Status)
}
