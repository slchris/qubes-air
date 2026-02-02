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

func setupQubeTestServices(t *testing.T) (ZoneService, QubeService, func()) {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "qube-service-test-*.db")
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

func createConnectedZone(t *testing.T, zoneSvc ZoneService) *models.Zone {
	t.Helper()

	ctx := context.Background()
	req := &models.ZoneCreateRequest{
		Name: "Test Zone",
		Type: models.ZoneTypeProxmox,
	}
	zone, err := zoneSvc.Create(ctx, req)
	require.NoError(t, err)

	zone, err = zoneSvc.Connect(ctx, zone.ID)
	require.NoError(t, err)

	return zone
}

func TestQubeService_Create(t *testing.T) {
	zoneSvc, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)

	req := &models.QubeCreateRequest{
		Name:   "Test Qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	}

	qube, err := qubeSvc.Create(ctx, req)
	assert.NoError(t, err)
	assert.NotEmpty(t, qube.ID)
	assert.Equal(t, req.Name, qube.Name)
	assert.Equal(t, models.QubeStatusStopped, qube.Status)
}

func TestQubeService_Create_WithDefaultSpecs(t *testing.T) {
	zoneSvc, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)

	req := &models.QubeCreateRequest{
		Name:   "Default Spec Qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	}

	qube, err := qubeSvc.Create(ctx, req)
	assert.NoError(t, err)
	assert.Greater(t, qube.Spec.VCPU, 0)
	assert.Greater(t, qube.Spec.Memory, 0)
}

func TestQubeService_Create_InvalidZone(t *testing.T) {
	_, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()

	req := &models.QubeCreateRequest{
		Name:   "Invalid Zone Qube",
		Type:   models.QubeTypeApp,
		ZoneID: "nonexistent-zone",
	}

	_, err := qubeSvc.Create(ctx, req)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrZoneNotFound)
}

func TestQubeService_Create_InvalidType(t *testing.T) {
	zoneSvc, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)

	req := &models.QubeCreateRequest{
		Name:   "Invalid Type Qube",
		Type:   "invalid-type",
		ZoneID: zone.ID,
	}

	_, err := qubeSvc.Create(ctx, req)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidQubeType)
}

func TestQubeService_GetByID(t *testing.T) {
	zoneSvc, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)

	req := &models.QubeCreateRequest{
		Name:   "Get Qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	}
	created, err := qubeSvc.Create(ctx, req)
	require.NoError(t, err)

	qube, err := qubeSvc.GetByID(ctx, created.ID)
	assert.NoError(t, err)
	assert.Equal(t, created.ID, qube.ID)
}

func TestQubeService_GetByID_NotFound(t *testing.T) {
	_, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()

	_, err := qubeSvc.GetByID(ctx, "nonexistent-id")
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrQubeNotFound)
}

func TestQubeService_List(t *testing.T) {
	zoneSvc, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)

	for i := 0; i < 3; i++ {
		req := &models.QubeCreateRequest{
			Name:   "Qube " + string(rune('A'+i)),
			Type:   models.QubeTypeApp,
			ZoneID: zone.ID,
		}
		_, err := qubeSvc.Create(ctx, req)
		require.NoError(t, err)
	}

	qubes, err := qubeSvc.List(ctx, repository.DefaultQubeListOptions())
	assert.NoError(t, err)
	assert.Len(t, qubes, 3)
}

func TestQubeService_Update(t *testing.T) {
	zoneSvc, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)

	createReq := &models.QubeCreateRequest{
		Name:   "Original",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	}
	created, err := qubeSvc.Create(ctx, createReq)
	require.NoError(t, err)

	newName := "Updated"
	updateReq := &models.QubeUpdateRequest{
		Name: &newName,
	}
	updated, err := qubeSvc.Update(ctx, created.ID, updateReq)
	assert.NoError(t, err)
	assert.Equal(t, "Updated", updated.Name)
}

func TestQubeService_Delete(t *testing.T) {
	zoneSvc, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)

	req := &models.QubeCreateRequest{
		Name:   "To Delete",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	}
	created, err := qubeSvc.Create(ctx, req)
	require.NoError(t, err)

	err = qubeSvc.Delete(ctx, created.ID)
	assert.NoError(t, err)

	_, err = qubeSvc.GetByID(ctx, created.ID)
	assert.ErrorIs(t, err, ErrQubeNotFound)
}

func TestQubeService_Start(t *testing.T) {
	zoneSvc, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)

	req := &models.QubeCreateRequest{
		Name:   "Start Qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	}
	created, err := qubeSvc.Create(ctx, req)
	require.NoError(t, err)

	qube, err := qubeSvc.Start(ctx, created.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.QubeStatusRunning, qube.Status)
}

func TestQubeService_Stop(t *testing.T) {
	zoneSvc, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)

	req := &models.QubeCreateRequest{
		Name:   "Stop Qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	}
	created, err := qubeSvc.Create(ctx, req)
	require.NoError(t, err)
	_, err = qubeSvc.Start(ctx, created.ID)
	require.NoError(t, err)

	qube, err := qubeSvc.Stop(ctx, created.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.QubeStatusStopped, qube.Status)
}

func TestQubeService_Start_ZoneDisconnected(t *testing.T) {
	zoneSvc, qubeSvc, cleanup := setupQubeTestServices(t)
	defer cleanup()

	ctx := context.Background()
	zone := createConnectedZone(t, zoneSvc)

	req := &models.QubeCreateRequest{
		Name:   "Disconnected Zone Qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	}
	created, err := qubeSvc.Create(ctx, req)
	require.NoError(t, err)

	_, err = zoneSvc.Disconnect(ctx, zone.ID)
	require.NoError(t, err)

	_, err = qubeSvc.Start(ctx, created.ID)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrZoneDisconnected)
}
