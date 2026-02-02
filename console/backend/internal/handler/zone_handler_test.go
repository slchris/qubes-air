package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestRouter(t *testing.T) (*gin.Engine, service.ZoneService, func()) {
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
	zoneSvc := service.NewZoneService(zoneRepo, qubeRepo)

	zoneHandler := NewZoneHandler(zoneSvc)

	router := gin.New()
	v1 := router.Group("/api/v1")
	zoneHandler.RegisterRoutes(v1)

	cleanup := func() {
		db.Close()
		os.Remove(tmpFile.Name())
	}

	return router, zoneSvc, cleanup
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
