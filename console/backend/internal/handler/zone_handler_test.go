package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/middleware"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/provider"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// proxmoxOnlyAdapters is the registry production builds: a Proxmox adapter and
// nothing else. The constructor is never called — zone creation only asks
// whether one is registered.
func proxmoxOnlyAdapters(t testing.TB) *provider.Registry {
	t.Helper()
	r := provider.NewRegistry()
	require.NoError(t, r.Register(models.ZoneTypeProxmox, func(context.Context, *models.Zone) (provider.Adapter, error) {
		return nil, errors.New("handler tests never build an adapter")
	}))
	return r
}

func setupTestRouter(t *testing.T) (*gin.Engine, service.ZoneService, func()) {
	t.Helper()
	router, zoneSvc, _, cleanup := setupZoneRouter(t)
	return router, zoneSvc, cleanup
}

// setupZoneRouter also returns the repository, so a test can store a row the
// service would refuse to create.
func setupZoneRouter(t *testing.T) (*gin.Engine, service.ZoneService, repository.ZoneRepository, func()) {
	t.Helper()

	gin.SetMode(gin.TestMode)

	tmpFile, err := os.CreateTemp("", "handler-test-*.db")
	require.NoError(t, err)
	tmpFile.Close()

	cfg := database.DefaultConfig()
	cfg.DSN = tmpFile.Name()

	db, err := database.New(cfg)
	require.NoError(t, err)

	zoneRepo := repository.NewZoneRepository(db)
	qubeRepo := repository.NewQubeRepository(db)
	zoneSvc := service.NewZoneService(zoneRepo, qubeRepo, proxmoxOnlyAdapters(t))

	zoneHandler := NewZoneHandler(zoneSvc)

	router := gin.New()
	v1 := router.Group("/api/v1")
	zoneHandler.RegisterRoutes(v1)

	cleanup := func() {
		db.Close()
		os.Remove(tmpFile.Name())
	}

	return router, zoneSvc, zoneRepo, cleanup
}

// serveJSON sends one request with an optional JSON body.
func serveJSON(t *testing.T, router *gin.Engine, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req, err := http.NewRequest(method, path, &buf)
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func listedZones(t *testing.T, router *gin.Engine) []models.Zone {
	t.Helper()
	w := serveJSON(t, router, http.MethodGet, "/api/v1/zones", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Zones []models.Zone `json:"zones"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return body.Zones
}

func TestZoneHandler_Create(t *testing.T) {
	router, _, cleanup := setupTestRouter(t)
	defer cleanup()

	reqBody := models.ZoneCreateRequest{
		Name: "Test Zone",
		Type: models.ZoneTypeProxmox,
		Config: models.ZoneConfig{
			Endpoint: "https://proxmox.local:8006",
		},
	}
	body, _ := json.Marshal(reqBody)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/zones", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)

	var zone models.Zone
	err := json.Unmarshal(w.Body.Bytes(), &zone)
	assert.NoError(t, err)
	assert.NotEmpty(t, zone.ID)
	assert.Equal(t, reqBody.Name, zone.Name)
}

func TestZoneHandler_Create_InvalidType(t *testing.T) {
	router, _, cleanup := setupTestRouter(t)
	defer cleanup()

	reqBody := map[string]string{
		"name": "Invalid Zone",
		"type": "invalid-type",
	}
	body, _ := json.Marshal(reqBody)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/zones", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestZoneHandler_Create_RefusesTypesWithoutAdapter — gcp, aws and azure are
// valid zone types with no registered adapter. The API answers 422, which the
// client can tell apart from a malformed type (400), and stores nothing.
func TestZoneHandler_Create_RefusesTypesWithoutAdapter(t *testing.T) {
	router, _, cleanup := setupTestRouter(t)
	defer cleanup()

	for _, zt := range []models.ZoneType{models.ZoneTypeGCP, models.ZoneTypeAWS, models.ZoneTypeAzure} {
		t.Run(string(zt), func(t *testing.T) {
			w := serveJSON(t, router, http.MethodPost, "/api/v1/zones", models.ZoneCreateRequest{
				Name: "cloud-" + string(zt),
				Type: zt,
			})
			require.Equal(t, http.StatusUnprocessableEntity, w.Code)

			var resp ErrorResponse
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			assert.Contains(t, resp.Message, "provider not implemented")
			assert.Contains(t, resp.Message, string(zt))
		})
	}

	assert.Empty(t, listedZones(t, router), "a refused create must not persist a row")
}

// TestZoneHandler_Update_CannotChangeType — the adapter check runs on create
// only, so it must not be possible to create a proxmox zone and then turn it
// into a gcp one. The update body has no type field; a "type" key is ignored.
func TestZoneHandler_Update_CannotChangeType(t *testing.T) {
	router, zoneSvc, cleanup := setupTestRouter(t)
	defer cleanup()

	created, err := zoneSvc.Create(context.Background(), &models.ZoneCreateRequest{Name: "pve", Type: models.ZoneTypeProxmox})
	require.NoError(t, err)

	w := serveJSON(t, router, http.MethodPut, "/api/v1/zones/"+created.ID,
		map[string]string{"name": "pve-renamed", "type": string(models.ZoneTypeGCP)})
	require.Equal(t, http.StatusOK, w.Code)

	stored, err := zoneSvc.GetByID(context.Background(), created.ID)
	require.NoError(t, err)
	assert.Equal(t, "pve-renamed", stored.Name)
	assert.Equal(t, models.ZoneTypeProxmox, stored.Type)
}

// TestZoneHandler_UnimplementedZonesAlreadyStoredStayManageable — a gcp row
// written before the create gate existed still lists, loads and deletes.
func TestZoneHandler_UnimplementedZonesAlreadyStoredStayManageable(t *testing.T) {
	router, _, zoneRepo, cleanup := setupZoneRouter(t)
	defer cleanup()

	now := time.Now()
	require.NoError(t, zoneRepo.Create(context.Background(), &models.Zone{
		ID: "legacy-gcp", Name: "old-gcp", Type: models.ZoneTypeGCP,
		Status: models.ZoneStatusDisconnected, CreatedAt: now, UpdatedAt: now,
	}))

	listed := listedZones(t, router)
	require.Len(t, listed, 1)
	assert.Equal(t, models.ZoneTypeGCP, listed[0].Type)

	w := serveJSON(t, router, http.MethodGet, "/api/v1/zones/legacy-gcp", nil)
	require.Equal(t, http.StatusOK, w.Code)

	w = serveJSON(t, router, http.MethodDelete, "/api/v1/zones/legacy-gcp", nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Empty(t, listedZones(t, router))
}

func TestZoneHandler_GetByID(t *testing.T) {
	router, zoneSvc, cleanup := setupTestRouter(t)
	defer cleanup()

	ctx := context.Background()
	created, err := zoneSvc.Create(ctx, &models.ZoneCreateRequest{
		Name: "Get Zone",
		Type: models.ZoneTypeProxmox,
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/zones/"+created.ID, nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var zone models.Zone
	err = json.Unmarshal(w.Body.Bytes(), &zone)
	assert.NoError(t, err)
	assert.Equal(t, created.ID, zone.ID)
}

func TestZoneHandler_GetByID_NotFound(t *testing.T) {
	router, _, cleanup := setupTestRouter(t)
	defer cleanup()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/zones/nonexistent", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestZoneHandler_List(t *testing.T) {
	router, zoneSvc, cleanup := setupTestRouter(t)
	defer cleanup()

	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_, err := zoneSvc.Create(ctx, &models.ZoneCreateRequest{
			Name: "Zone " + string(rune('A'+i)),
			Type: models.ZoneTypeProxmox,
		})
		require.NoError(t, err)
	}

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/zones", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response struct {
		Zones []models.Zone `json:"zones"`
		Total int           `json:"total"`
	}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Len(t, response.Zones, 3)
}

func TestZoneHandler_Delete(t *testing.T) {
	router, zoneSvc, cleanup := setupTestRouter(t)
	defer cleanup()

	ctx := context.Background()
	created, err := zoneSvc.Create(ctx, &models.ZoneCreateRequest{
		Name: "To Delete",
		Type: models.ZoneTypeProxmox,
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("DELETE", "/api/v1/zones/"+created.ID, nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/zones/"+created.ID, nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestZoneHandler_Connect(t *testing.T) {
	router, zoneSvc, cleanup := setupTestRouter(t)
	defer cleanup()

	ctx := context.Background()
	created, err := zoneSvc.Create(ctx, &models.ZoneCreateRequest{
		Name: "Connect Zone",
		Type: models.ZoneTypeProxmox,
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/zones/"+created.ID+"/connect", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	zone, err := zoneSvc.GetByID(ctx, created.ID)
	assert.NoError(t, err)
	assert.Equal(t, "connected", zone.Status)
}

// TestZoneHandler_ListFilteredByZoneScope — a zone-scoped credential's list
// request is narrowed in the query to its allowlist. The context key is set
// here directly because ScopedAuth already has its own tests.
func TestZoneHandler_ListFilteredByZoneScope(t *testing.T) {
	_, zoneSvc, cleanup := setupTestRouter(t)
	defer cleanup()

	ctx := context.Background()
	allowed, err := zoneSvc.Create(ctx, &models.ZoneCreateRequest{Name: "Allowed", Type: models.ZoneTypeProxmox})
	require.NoError(t, err)
	_, err = zoneSvc.Create(ctx, &models.ZoneCreateRequest{Name: "Other", Type: models.ZoneTypeProxmox})
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	scoped := gin.New()
	v1 := scoped.Group("/api/v1")
	v1.Use(func(c *gin.Context) {
		c.Set(middleware.ZonesContextKey, []string{allowed.ID})
		c.Next()
	})
	NewZoneHandler(zoneSvc).RegisterRoutes(v1)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodGet, "/api/v1/zones", nil)
	scoped.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var body struct {
		Zones []models.Zone `json:"zones"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Zones, 1)
	assert.Equal(t, allowed.ID, body.Zones[0].ID)
}
