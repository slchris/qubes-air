package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/service"
	"github.com/slchris/qubes-air/console/internal/transport"
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
		Name:   "test-qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	}
	body, _ := json.Marshal(reqBody)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/qubes", bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusCreated, w.Code)

	// The response is an operation envelope, not a bare qube: provisioning runs
	// asynchronously and the caller needs the job id to poll for the outcome.
	var op struct {
		Qube  models.Qube `json:"qube"`
		JobID string      `json:"job_id"`
	}
	err := json.Unmarshal(w.Body.Bytes(), &op)
	assert.NoError(t, err)
	assert.NotEmpty(t, op.Qube.ID)
	assert.Equal(t, reqBody.Name, op.Qube.Name)
}

func TestQubeHandler_Create_InvalidZone(t *testing.T) {
	router, _, _, cleanup := setupQubeTestRouter(t)
	defer cleanup()

	reqBody := models.QubeCreateRequest{
		Name:   "test-qube",
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

	createdOp, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name:   "get-qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/qubes/"+createdOp.Qube.ID, nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	var qube models.Qube
	err = json.Unmarshal(w.Body.Bytes(), &qube)
	assert.NoError(t, err)
	assert.Equal(t, createdOp.Qube.ID, qube.ID)
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

	for i := range 3 {
		_, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
			Name:   "qube-" + string(rune('a'+i)),
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

	createdOp, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name:   "to-delete",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	})
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("DELETE", "/api/v1/qubes/"+createdOp.Qube.ID, nil)
	router.ServeHTTP(w, req)

	// 202: the release was queued, not completed.
	assert.Equal(t, http.StatusAccepted, w.Code)

	// The qube still exists — DELETE releases compute and keeps the data disk.
	// It must remain in the terraform variable map until the disk is purged.
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/qubes/"+createdOp.Qube.ID, nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestQubeHandler_Start(t *testing.T) {
	router, zoneSvc, qubeSvc, cleanup := setupQubeTestRouter(t)
	defer cleanup()

	ctx := context.Background()
	zone := createTestZoneForHandler(t, zoneSvc)

	createdOp, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name:   "start-qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	})
	require.NoError(t, err)

	// Create provisions, so the qube is already running; suspend it to get a
	// startable state. Starting a running qube is a 409 by design.
	_, err = qubeSvc.Stop(ctx, createdOp.Qube.ID)
	require.NoError(t, err)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/qubes/"+createdOp.Qube.ID+"/start", nil)
	router.ServeHTTP(w, req)

	// 202: the terraform apply is queued, not done.
	assert.Equal(t, http.StatusAccepted, w.Code)

	qube, err := qubeSvc.GetByID(ctx, createdOp.Qube.ID)
	assert.NoError(t, err)
	assert.Equal(t, models.QubeStatusRunning, qube.Status)
}

func TestQubeHandler_Stop(t *testing.T) {
	router, zoneSvc, qubeSvc, cleanup := setupQubeTestRouter(t)
	defer cleanup()

	ctx := context.Background()
	zone := createTestZoneForHandler(t, zoneSvc)

	createdOp, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name:   "stop-qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	})
	require.NoError(t, err)
	// Create already leaves the qube running, ready to be stopped.
	require.Equal(t, models.QubeStatusRunning, createdOp.Qube.Status)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/qubes/"+createdOp.Qube.ID+"/stop", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusAccepted, w.Code)

	qube, err := qubeSvc.GetByID(ctx, createdOp.Qube.ID)
	assert.NoError(t, err)
	// Stop suspends: compute released, data retained.
	assert.Equal(t, models.QubeStatusSuspended, qube.Status)
}

// setupQubeAppsTestRouter is setupQubeTestRouter plus an injected transport and
// a ready qube, for the computer-use endpoints (appmenus / launch).
func setupQubeAppsTestRouter(t *testing.T, xport transport.Transport) (*gin.Engine, string, *transport.FakeTransport, func()) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	fake, _ := xport.(*transport.FakeTransport)

	tmpFile, err := os.CreateTemp("", "qube-apps-handler-test-*.db")
	require.NoError(t, err)
	tmpFile.Close()

	cfg := database.DefaultConfig()
	cfg.DSN = tmpFile.Name()
	db, err := database.New(cfg)
	require.NoError(t, err)

	zoneRepo := repository.NewZoneRepository(db)
	qubeRepo := repository.NewQubeRepository(db)
	zoneSvc := service.NewZoneService(zoneRepo, qubeRepo)
	qubeSvc := service.NewQubeService(qubeRepo, zoneRepo, service.WithTransport(xport))

	qubeHandler := NewQubeHandler(qubeSvc)
	router := gin.New()
	v1 := router.Group("/api/v1")
	qubeHandler.RegisterRoutes(v1)

	ctx := context.Background()
	zone := createTestZoneForHandler(t, zoneSvc)
	createdOp, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name:   "apps-qube",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	})
	require.NoError(t, err)

	cleanup := func() {
		db.Close()
		os.Remove(tmpFile.Name())
	}
	return router, createdOp.Qube.ID, fake, cleanup
}

func TestQubeHandler_GetAppMenus(t *testing.T) {
	fake := &transport.FakeTransport{
		RespFn: func(target, service string, _ []byte) ([]byte, error) {
			assert.Equal(t, "apps-qube", target)
			assert.Equal(t, "qubes.GetAppmenus", service)
			return []byte("firefox.desktop:Name=Firefox\n"), nil
		},
	}
	router, id, _, cleanup := setupQubeAppsTestRouter(t, fake)
	defer cleanup()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/qubes/"+id+"/appmenus", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "firefox.desktop:Name=Firefox")
	assert.Equal(t, 1, fake.CallCount())
}

func TestQubeHandler_GetAppMenus_TransportError(t *testing.T) {
	fake := &transport.FakeTransport{
		RespFn: func(_, _ string, _ []byte) ([]byte, error) {
			return nil, errors.New("tunnel down")
		},
	}
	router, id, _, cleanup := setupQubeAppsTestRouter(t, fake)
	defer cleanup()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/qubes/"+id+"/appmenus", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadGateway, w.Code)
}

func TestQubeHandler_GetAppMenus_NotFound(t *testing.T) {
	fake := &transport.FakeTransport{}
	router, _, _, cleanup := setupQubeAppsTestRouter(t, fake)
	defer cleanup()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/qubes/does-not-exist/appmenus", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Zero(t, fake.CallCount())
}

func TestQubeHandler_LaunchApp(t *testing.T) {
	fake := &transport.FakeTransport{
		RespFn: func(target, service string, _ []byte) ([]byte, error) {
			assert.Equal(t, "apps-qube", target)
			assert.Equal(t, "qubes.StartApp+firefox.desktop", service)
			return []byte("qubes.StartApp: launched 'firefox.desktop' on :100\n"), nil
		},
	}
	router, id, _, cleanup := setupQubeAppsTestRouter(t, fake)
	defer cleanup()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/qubes/"+id+"/apps/firefox.desktop/launch", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "launched")
	assert.Equal(t, "qubes.StartApp+firefox.desktop", fake.Calls[0].Service)
}

// TestQubeHandler_LaunchApp_InvalidAppID — a malformed app id is refused and
// NEVER reaches the transport (zero upstream calls). Two distinct boundaries
// both hold:
//
//   - a separator injected through the URL (encoded or not) is torn apart by
//     routing itself: the request never matches the :app route and is refused
//     as 404 without the handler — let alone the service — being consulted;
//   - a non-separator that still fails the allowlist (space, newline, over-long)
//     reaches the handler and is refused there with an explicit 400.
//
// Either way the transport is provably untouched, which is the security
// property this test exists for.
func TestQubeHandler_LaunchApp_InvalidAppID(t *testing.T) {
	// A fingerprinting transport: it must not be consulted at all.
	fake := &transport.FakeTransport{}
	router, id, fake, cleanup := setupQubeAppsTestRouter(t, fake)
	defer cleanup()

	refused404 := []struct{ name, app string }{
		{"path traversal", "..%2Ffirefox"},
		{"slash", "a%2Fb"},
		{"bare traversal", "../firefox"},
		{"bare slash", "a/b"},
	}
	for _, tt := range refused404 {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req, _ := http.NewRequest("POST", "/api/v1/qubes/"+id+"/apps/"+tt.app+"/launch", nil)
			router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusNotFound, w.Code)
		})
	}

	rejected400 := []struct{ name, app string }{
		{"space", "a%20b"},
		{"newline", "a%0Ab"},
	}
	for _, tt := range rejected400 {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			req, _ := http.NewRequest("POST", "/api/v1/qubes/"+id+"/apps/"+tt.app+"/launch", nil)
			router.ServeHTTP(w, req)
			assert.Equal(t, http.StatusBadRequest, w.Code)
		})
	}

	// Over-long: a plain (unencoded) path segment of allowed characters.
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST",
		"/api/v1/qubes/"+id+"/apps/"+strings.Repeat("a", service.MaxAppIDLen+1)+"/launch", nil)
	router.ServeHTTP(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	assert.Zero(t, fake.CallCount(), "an invalid app id must never reach the transport")
}

// TestQubeHandler_LaunchApp_InvalidAppID_Direct exercises the handler's
// allowlist boundary directly, including the empty param, which cannot be
// expressed as a URL path segment.
func TestQubeHandler_LaunchApp_InvalidAppID_Direct(t *testing.T) {
	fake := &transport.FakeTransport{}
	svc, _, cleanup := appTestService(t, fake)
	defer cleanup()
	h := NewQubeHandler(svc)

	for _, bad := range []string{"", "../firefox", "a/b", "a b", "a\nb", strings.Repeat("a", service.MaxAppIDLen+1)} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/api/v1/qubes/x/apps/x/launch", nil)
		c.Params = gin.Params{
			{Key: "id", Value: "some-qube"},
			{Key: "app", Value: bad},
		}
		h.LaunchApp(c)
		assert.Equal(t, http.StatusBadRequest, w.Code, "app %q", bad)
	}
	assert.Zero(t, fake.CallCount(), "an invalid app id must never reach the transport")
}

// appTestService builds a QubeService with a fake transport and a connected
// zone plus one registered qube, ready for direct handler calls.
func appTestService(t *testing.T, xport transport.Transport) (service.QubeService, string, func()) {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "qube-apps-service-test-*.db")
	require.NoError(t, err)
	tmpFile.Close()

	cfg := database.DefaultConfig()
	cfg.DSN = tmpFile.Name()
	db, err := database.New(cfg)
	require.NoError(t, err)

	zoneRepo := repository.NewZoneRepository(db)
	qubeRepo := repository.NewQubeRepository(db)
	zoneSvc := service.NewZoneService(zoneRepo, qubeRepo)
	qubeSvc := service.NewQubeService(qubeRepo, zoneRepo, service.WithTransport(xport))

	zone := createTestZoneForHandler(t, zoneSvc)
	op, err := qubeSvc.Create(context.Background(), &models.QubeCreateRequest{
		Name: "apps-qube", Type: models.QubeTypeApp, ZoneID: zone.ID,
	})
	require.NoError(t, err)

	cleanup := func() {
		db.Close()
		os.Remove(tmpFile.Name())
	}
	return qubeSvc, op.Qube.ID, cleanup
}

func TestQubeHandler_LaunchApp_TransportError(t *testing.T) {
	fake := &transport.FakeTransport{
		RespFn: func(_, _ string, _ []byte) ([]byte, error) {
			return nil, errors.New("tunnel down")
		},
	}
	router, id, _, cleanup := setupQubeAppsTestRouter(t, fake)
	defer cleanup()

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/qubes/"+id+"/apps/firefox.desktop/launch", nil)
	router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadGateway, w.Code)
}
