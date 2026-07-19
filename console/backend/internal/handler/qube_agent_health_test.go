package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupAgentHealthRouter mirrors setupQubeTestRouter but also hands back the
// qube repository. Probe results are written there, not through the service, so
// these tests need it to stage a qube whose VM runs and whose agent does not.
func setupAgentHealthRouter(t *testing.T) (*gin.Engine, service.ZoneService, service.QubeService, repository.QubeRepository) {
	t.Helper()

	gin.SetMode(gin.TestMode)

	tmpFile, err := os.CreateTemp("", "qube-agent-health-test-*.db")
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

	router := gin.New()
	v1 := router.Group("/api/v1")
	NewQubeHandler(qubeSvc).RegisterRoutes(v1)

	t.Cleanup(func() {
		db.Close()
		os.Remove(tmpFile.Name())
	})

	return router, zoneSvc, qubeSvc, qubeRepo
}

// TestQubeHandler_ExposesRunningVMWithDeadAgent is the whole point of the
// change: the API must let an operator see "the VM is running, the agent is
// unreachable, and here is why" without SSHing to a hypervisor node. The two
// facts are reported side by side and neither is collapsed into the other.
func TestQubeHandler_ExposesRunningVMWithDeadAgent(t *testing.T) {
	router, zoneSvc, qubeSvc, qubeRepo := setupAgentHealthRouter(t)

	ctx := context.Background()
	zone := createTestZoneForHandler(t, zoneSvc)

	createdOp, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name:   "dead-agent",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	})
	require.NoError(t, err)
	// Create provisions, so the VM itself is up. That much was always visible.
	require.Equal(t, models.QubeStatusRunning, createdOp.Qube.Status)

	const reason = `rpc error: code = Unavailable desc = connection refused`
	probedAt := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, qubeRepo.UpdateAgentHealth(
		ctx, createdOp.Qube.ID, models.AgentHealthUnreachable, probedAt, reason))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/qubes/"+createdOp.Qube.ID, nil)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var qube models.Qube
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &qube))

	assert.Equal(t, models.QubeStatusRunning, qube.Status,
		"the VM is genuinely running; a dead agent must not be reported as a stopped VM")
	assert.Equal(t, models.AgentHealthUnreachable, qube.AgentHealth)
	assert.Equal(t, reason, qube.AgentLastError,
		"the reason is what saves the operator an SSH session")
	require.NotNil(t, qube.AgentLastProbedAt)
	assert.True(t, qube.AgentLastProbedAt.Equal(probedAt))
	assert.Nil(t, qube.AgentLastHealthyAt, "this agent has never answered")

	// The raw payload is the operator-facing contract; assert on the keys, not
	// only on the decoded struct, so a renamed tag cannot pass unnoticed.
	var raw map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
	assert.Equal(t, "running", raw["status"])
	assert.Equal(t, "unreachable", raw["agent_health"])
	assert.Equal(t, reason, raw["agent_last_error"])
}

// TestQubeHandler_UnprobedQubeReportsUnknown — a qube nobody has probed must
// say so. Omitting the field, or defaulting it to healthy, is how a broken agent
// stayed green on this console for hours.
func TestQubeHandler_UnprobedQubeReportsUnknown(t *testing.T) {
	router, zoneSvc, qubeSvc, _ := setupAgentHealthRouter(t)

	ctx := context.Background()
	zone := createTestZoneForHandler(t, zoneSvc)

	createdOp, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name:   "never-probed",
		Type:   models.QubeTypeApp,
		ZoneID: zone.ID,
	})
	require.NoError(t, err)
	assert.Equal(t, models.AgentHealthUnknown, createdOp.Qube.AgentHealth,
		"the create response and a later GET must agree")

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/qubes/"+createdOp.Qube.ID, nil)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
	require.Contains(t, raw, "agent_health", "agent_health is never omitted")
	assert.Equal(t, "unknown", raw["agent_health"])
	assert.NotContains(t, raw, "agent_last_probed_at", "no probe has run, so no probe time is claimed")
	assert.NotContains(t, raw, "agent_last_error")
}

// TestQubeHandler_ListExposesAgentHealth — the list is where an operator scans
// for trouble, so the per-qube health has to survive that path too.
func TestQubeHandler_ListExposesAgentHealth(t *testing.T) {
	router, zoneSvc, qubeSvc, qubeRepo := setupAgentHealthRouter(t)

	ctx := context.Background()
	zone := createTestZoneForHandler(t, zoneSvc)

	healthy, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name: "good", Type: models.QubeTypeApp, ZoneID: zone.ID,
	})
	require.NoError(t, err)
	broken, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name: "bad", Type: models.QubeTypeApp, ZoneID: zone.ID,
	})
	require.NoError(t, err)

	require.NoError(t, qubeRepo.UpdateAgentHealth(
		ctx, healthy.Qube.ID, models.AgentHealthHealthy, time.Now(), ""))
	require.NoError(t, qubeRepo.UpdateAgentHealth(
		ctx, broken.Qube.ID, models.AgentHealthUnreachable, time.Now(), "systemd unit failed to start"))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/qubes", nil)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var response struct {
		Qubes []models.Qube `json:"qubes"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Len(t, response.Qubes, 2)

	got := map[string]models.Qube{}
	for _, q := range response.Qubes {
		got[q.Name] = q
	}
	assert.Equal(t, models.AgentHealthHealthy, got["good"].AgentHealth)
	assert.NotNil(t, got["good"].AgentLastHealthyAt)
	assert.Equal(t, models.AgentHealthUnreachable, got["bad"].AgentHealth)
	assert.Equal(t, "systemd unit failed to start", got["bad"].AgentLastError)
	// Both VMs are running. Only the agent state tells them apart, which is
	// precisely why it is not folded into status.
	assert.Equal(t, models.QubeStatusRunning, got["good"].Status)
	assert.Equal(t, models.QubeStatusRunning, got["bad"].Status)
}
