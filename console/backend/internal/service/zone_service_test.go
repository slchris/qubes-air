package service

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/provider"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// proxmoxOnlyAdapters is the registry production builds: a Proxmox adapter and
// nothing else. Tests use a real *provider.Registry so the create gate is
// exercised against the same type main.go wires in, not a test double of it.
func proxmoxOnlyAdapters(t testing.TB) *provider.Registry {
	t.Helper()
	return adaptersFor(t, models.ZoneTypeProxmox)
}

// adaptersFor registers a never-called constructor for each type: the zone
// service only asks whether one exists.
func adaptersFor(t testing.TB, types ...models.ZoneType) *provider.Registry {
	t.Helper()
	r := provider.NewRegistry()
	for _, zt := range types {
		require.NoError(t, r.Register(zt, func(context.Context, *models.Zone) (provider.Adapter, error) {
			return nil, errors.New("zone service tests never build an adapter")
		}))
	}
	return r
}

func setupTestServices(t *testing.T) (ZoneService, func()) {
	t.Helper()
	zoneSvc, _, cleanup := setupZoneService(t, proxmoxOnlyAdapters(t))
	return zoneSvc, cleanup
}

// setupZoneService also returns the repository, so a test can write a row the
// service itself would refuse — a zone recorded before the create gate existed.
func setupZoneService(t *testing.T, adapters ZoneTypeSupport) (ZoneService, repository.ZoneRepository, func()) {
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

	zoneSvc := NewZoneService(zoneRepo, qubeRepo, adapters)

	cleanup := func() {
		db.Close()
		os.Remove(tmpFile.Name())
	}

	return zoneSvc, zoneRepo, cleanup
}

func TestZoneService_Create(t *testing.T) {
	zoneSvc, cleanup := setupTestServices(t)
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
	zoneSvc, cleanup := setupTestServices(t)
	defer cleanup()

	ctx := context.Background()

	req := &models.ZoneCreateRequest{
		Name: "Invalid Zone",
		Type: "invalid-type",
	}

	_, err := zoneSvc.Create(ctx, req)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidZoneType)
	// A misspelled type is a malformed request, not a missing provider.
	assert.NotErrorIs(t, err, ErrZoneTypeNotImplemented)
}

// TestZoneService_Create_RefusesTypesWithoutAdapter — the model knows gcp, aws
// and azure, but only Proxmox has an adapter. Creating one of the others used
// to succeed and fail minutes later at the first provision job; it must be
// refused up front and leave nothing behind.
func TestZoneService_Create_RefusesTypesWithoutAdapter(t *testing.T) {
	zoneSvc, cleanup := setupTestServices(t)
	defer cleanup()
	ctx := context.Background()

	for _, zt := range []models.ZoneType{models.ZoneTypeGCP, models.ZoneTypeAWS, models.ZoneTypeAzure} {
		t.Run(string(zt), func(t *testing.T) {
			zone, err := zoneSvc.Create(ctx, &models.ZoneCreateRequest{
				Name:   "cloud-" + string(zt),
				Type:   zt,
				Config: models.ZoneConfig{Project: "p", Region: "r"},
			})
			require.ErrorIs(t, err, ErrZoneTypeNotImplemented)
			assert.NotErrorIs(t, err, ErrInvalidZoneType)
			assert.Nil(t, zone)
			assert.Contains(t, err.Error(), string(zt))
		})
	}

	zones, err := zoneSvc.List(ctx, repository.DefaultZoneListOptions())
	require.NoError(t, err)
	assert.Empty(t, zones, "a refused create must not persist a row")
}

// TestZoneService_Create_FollowsTheRegistry — the gate is the registry, not a
// hard-coded "proxmox": registering an adapter is what makes a type creatable.
func TestZoneService_Create_FollowsTheRegistry(t *testing.T) {
	zoneSvc, _, cleanup := setupZoneService(t, adaptersFor(t, models.ZoneTypeGCP))
	defer cleanup()
	ctx := context.Background()

	zone, err := zoneSvc.Create(ctx, &models.ZoneCreateRequest{Name: "gcp", Type: models.ZoneTypeGCP})
	require.NoError(t, err)
	assert.Equal(t, models.ZoneTypeGCP, zone.Type)

	_, err = zoneSvc.Create(ctx, &models.ZoneCreateRequest{Name: "pve", Type: models.ZoneTypeProxmox})
	assert.ErrorIs(t, err, ErrZoneTypeNotImplemented, "proxmox is refused when it is not registered")
}

// TestZoneService_Create_NoRegistryFailsClosed — a service wired without a
// registry must refuse every type rather than admit every valid one. The typed
// nil covers a *provider.Registry that was declared but never built.
func TestZoneService_Create_NoRegistryFailsClosed(t *testing.T) {
	var unbuilt *provider.Registry
	for name, adapters := range map[string]ZoneTypeSupport{"nil interface": nil, "nil registry": unbuilt} {
		t.Run(name, func(t *testing.T) {
			zoneSvc, _, cleanup := setupZoneService(t, adapters)
			defer cleanup()
			ctx := context.Background()

			_, err := zoneSvc.Create(ctx, &models.ZoneCreateRequest{Name: "pve", Type: models.ZoneTypeProxmox})
			require.ErrorIs(t, err, ErrZoneTypeNotImplemented)

			zones, err := zoneSvc.List(ctx, repository.DefaultZoneListOptions())
			require.NoError(t, err)
			assert.Empty(t, zones)
		})
	}
}

// TestZoneService_UnimplementedZonesAlreadyStoredStayManageable — rows of a
// type without an adapter may predate the create gate. The gate must not make
// them unreadable or undeletable: they have to be listable to be noticed and
// deletable to be cleaned up.
func TestZoneService_UnimplementedZonesAlreadyStoredStayManageable(t *testing.T) {
	zoneSvc, zoneRepo, cleanup := setupZoneService(t, proxmoxOnlyAdapters(t))
	defer cleanup()
	ctx := context.Background()

	now := time.Now()
	legacy := &models.Zone{
		ID: "legacy-gcp", Name: "old-gcp", Type: models.ZoneTypeGCP,
		Status: models.ZoneStatusDisconnected, CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, zoneRepo.Create(ctx, legacy))

	got, err := zoneSvc.GetByID(ctx, legacy.ID)
	require.NoError(t, err)
	assert.Equal(t, models.ZoneTypeGCP, got.Type)

	zones, err := zoneSvc.List(ctx, repository.DefaultZoneListOptions())
	require.NoError(t, err)
	require.Len(t, zones, 1)

	renamed := "renamed-gcp"
	updated, err := zoneSvc.Update(ctx, legacy.ID, &models.ZoneUpdateRequest{Name: &renamed})
	require.NoError(t, err)
	assert.Equal(t, renamed, updated.Name)
	assert.Equal(t, models.ZoneTypeGCP, updated.Type)

	require.NoError(t, zoneSvc.Delete(ctx, legacy.ID))
	_, err = zoneSvc.GetByID(ctx, legacy.ID)
	assert.ErrorIs(t, err, ErrZoneNotFound)
}

func TestZoneService_Create_EmptyName(t *testing.T) {
	zoneSvc, cleanup := setupTestServices(t)
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
	zoneSvc, cleanup := setupTestServices(t)
	defer cleanup()

	ctx := context.Background()

	req := &models.ZoneCreateRequest{
		Name: "Get Zone",
		Type: models.ZoneTypeProxmox,
	}
	created, err := zoneSvc.Create(ctx, req)
	require.NoError(t, err)

	zone, err := zoneSvc.GetByID(ctx, created.ID)
	assert.NoError(t, err)
	assert.Equal(t, created.ID, zone.ID)
}

func TestZoneService_GetByID_NotFound(t *testing.T) {
	zoneSvc, cleanup := setupTestServices(t)
	defer cleanup()

	ctx := context.Background()

	_, err := zoneSvc.GetByID(ctx, "nonexistent-id")
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrZoneNotFound)
}

func TestZoneService_List(t *testing.T) {
	zoneSvc, cleanup := setupTestServices(t)
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
	zoneSvc, cleanup := setupTestServices(t)
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
	zoneSvc, cleanup := setupTestServices(t)
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
	zoneSvc, cleanup := setupTestServices(t)
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
	zoneSvc, cleanup := setupTestServices(t)
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
