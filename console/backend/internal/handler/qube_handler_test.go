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

func setupQubeTestRouter(t *testing.T) (*gin.Engine, service.ZoneService, service.QubeService, func()) {
	t.Helper()

	gin.SetMode(gin.TestMode)

	tmpFile, err := os.CreateTemp("", "qube-handler-test-*.db")
	require.NoError(t, err)
	tmpFile.Close()

	cfg := database.DefaultConfig()
	cfg.DSN = tmpFile.Name()

	db, err := database.New(cfg)
	require.NoError(t, err)

	zoneRepo := repository.NewZoneRepository(db)
	qubeRepo := repository.NewQubeRepository(db)
	zoneSvc := service.NewZoneService(zoneRepo, qubeRepo)
	qubeSvc := service.NewQubeService(qubeRepo, zoneRepo)

	qubeHandler := NewQubeHandler(qubeSvc)

	router := gin.New()
	v1 := router.Group("/api/v1")
	qubeHandler.RegisterRoutes(v1)

	cleanup := func() {
		db.Close()
		os.Remove(tmpFile.Name())
	}

	return router, zoneSvc, qubeSvc, cleanup
}

func createTestZoneForHandler(t *testing.T, zoneSvc service.ZoneService) *models.Zone {
	t.Helper()

	ctx := context.Background()
	zone, err := zoneSvc.Create(ctx, &models.ZoneCreateRequest{
		Name: "Test Zone",
		Type: models.ZoneTypeProxmox,
	})
	require.NoError(t, err)

	zone, err = zoneSvc.Connect(ctx, zone.ID)
	require.NoError(t, err)

	return zone
}

func TestQubeHandler_Create(t *testing.T) {
	router, zoneSvc, _, cleanup := setupQubeTestRouter(t)
	defer cleanup()

	zone := createTestZoneForHandler(t, zoneSvc)

	reqBody := models.QubeCreateRequest{
		Name:   "Test Qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	}
	body, _ := json.Marshal(reqBody)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/qubes", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)

	var qube models.Qube
	err := json.Unmarshal(w.Body.Bytes(), &qube)
	assert.NoError(t, err)
	assert.NotEmpty(t, qube.ID)
	assert.Equal(t, reqBody.Name, qube.Name)
}

func TestQubeHandler_Create_InvalidZone(t *testing.T) {
	router, _, _, cleanup := setupQubeTestRouter(t)
	defer cleanup()

	reqBody := models.QubeCreateRequest{
		Name:   "Test Qube",
		Type:   models.QubeTypeApp,
		ZoneID: "nonexistent-zone",
	}
	body, _ := json.Marshal(reqBody)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/qubes", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestQubeHandler_GetByID(t *testing.T) {
	router, zoneSvc, qubeSvc, cleanup := setupQubeTestRouter(t)
	defer cleanup()

	ctx := context.Background()
	zone := createTestZoneForHandler(t, zoneSvc)

	created, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name:   "Get Qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/qubes/"+created.ID, nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var qube models.Qube
	err = json.Unmarshal(w.Body.Bytes(), &qube)
	assert.NoError(t, err)
	assert.Equal(t, created.ID, qube.ID)
}

func TestQubeHandler_GetByID_NotFound(t *testing.T) {
	router, _, _, cleanup := setupQubeTestRouter(t)
	defer cleanup()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/qubes/nonexistent", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestQubeHandler_List(t *testing.T) {
	router, zoneSvc, qubeSvc, cleanup := setupQubeTestRouter(t)
	defer cleanup()

	ctx := context.Background()
	zone := createTestZoneForHandler(t, zoneSvc)

	for i := 0; i < 3; i++ {
		_, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
			Name:   "Qube " + string(rune('A'+i)),
			Type:   models.QubeTypeApp,
			ZoneID: zone.ID,
		})
		require.NoError(t, err)
	}

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/qubes", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var response struct {
		Qubes []models.Qube `json:"qubes"`
		Total int           `json:"total"`
	}
	err := json.Unmarshal(w.Body.Bytes(), &response)
	assert.NoError(t, err)
	assert.Len(t, response.Qubes, 3)
}

func TestQubeHandler_Delete(t *testing.T) {
	router, zoneSvc, qubeSvc, cleanup := setupQubeTestRouter(t)
	defer cleanup()

	ctx := context.Background()
	zone := createTestZoneForHandler(t, zoneSvc)

	created, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name:   "To Delete",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("DELETE", "/api/v1/qubes/"+created.ID, nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/qubes/"+created.ID, nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestQubeHandler_Start(t *testing.T) {
	router, zoneSvc, qubeSvc, cleanup := setupQubeTestRouter(t)
	defer cleanup()

	ctx := context.Background()
	zone := createTestZoneForHandler(t, zoneSvc)

	created, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name:   "Start Qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/qubes/"+created.ID+"/start", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	qube, err := qubeSvc.GetByID(ctx, created.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.QubeStatusRunning, qube.Status)
}

func TestQubeHandler_Stop(t *testing.T) {
	router, zoneSvc, qubeSvc, cleanup := setupQubeTestRouter(t)
	defer cleanup()

	ctx := context.Background()
	zone := createTestZoneForHandler(t, zoneSvc)

	created, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name:   "Stop Qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	})
	require.NoError(t, err)
	_, err = qubeSvc.Start(ctx, created.ID)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/qubes/"+created.ID+"/stop", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	qube, err := qubeSvc.GetByID(ctx, created.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.QubeStatusStopped, qube.Status)
}
