package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestQubeHandler_ExposesAgentRecoveryReading is the acceptance test for the
// state this change exists to make visible: a qube whose VM is running and
// whose agent has not answered for longer than the agent unit's own start
// budget. "unreachable" alone cannot say whether waiting is a plan; this reading
// says it is not.
//
// It also pins the wire contract an operator's browser reads, name and value,
// through the real service, repository and database rather than a fixture.
func TestQubeHandler_ExposesAgentRecoveryReading(t *testing.T) {
	router, zoneSvc, qubeSvc, qubeRepo := setupAgentHealthRouter(t)

	ctx := context.Background()
	zone := createTestZoneForHandler(t, zoneSvc)

	createdOp, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name: "gave-up", Type: models.QubeTypeApp, ZoneID: zone.ID,
	})
	require.NoError(t, err)

	// A run of failures that has now outlasted the shipped unit's start-limit
	// budget: the first probe that failed, and a later one still failing.
	const reason = "nothing is listening on 10.0.0.7:8443: connect: connection refused"
	firstFailure := time.Now().UTC().Add(-models.AgentStartLimitBudget).Truncate(time.Second)
	require.NoError(t, qubeRepo.UpdateAgentHealth(
		ctx, createdOp.Qube.ID, models.AgentHealthUnreachable, firstFailure, reason))
	require.NoError(t, qubeRepo.UpdateAgentHealth(
		ctx, createdOp.Qube.ID, models.AgentHealthUnreachable,
		firstFailure.Add(models.AgentStartLimitBudget), reason))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/qubes/"+createdOp.Qube.ID, nil)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
	assert.Equal(t, "running", raw["status"], "the VM is fine; this is an agent failure")
	assert.Equal(t, "unreachable", raw["agent_health"])
	assert.Equal(t, "manual", raw["agent_recovery"],
		"an operator reads this key to know that waiting will not fix it")
	assert.NotEmpty(t, raw["agent_failing_since"], "and since when")

	var qube models.Qube
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &qube))
	assert.Equal(t, models.AgentRecoveryManual, qube.AgentRecovery)
	require.NotNil(t, qube.AgentFailingSince)
	assert.True(t, qube.AgentFailingSince.Equal(firstFailure))

	// The list view — where a fleet is actually scanned — carries the same
	// reading, so the two endpoints cannot disagree.
	w = httptest.NewRecorder()
	req, _ = http.NewRequest("GET", "/api/v1/qubes", nil)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var response struct {
		Qubes []models.Qube `json:"qubes"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	require.Len(t, response.Qubes, 1)
	assert.Equal(t, models.AgentRecoveryManual, response.Qubes[0].AgentRecovery)
}

// TestQubeHandler_ExposesInBudgetFailureAsPending — the third wire value, and
// the reason the two states are separate: an agent that stopped answering
// seconds ago may still be mid-restart, and the browser must be able to tell
// that apart from one that will never come back without help. The strings are
// pinned here because the frontend switches on them.
func TestQubeHandler_ExposesInBudgetFailureAsPending(t *testing.T) {
	router, zoneSvc, qubeSvc, qubeRepo := setupAgentHealthRouter(t)

	ctx := context.Background()
	zone := createTestZoneForHandler(t, zoneSvc)

	createdOp, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name: "maybe-restarting", Type: models.QubeTypeApp, ZoneID: zone.ID,
	})
	require.NoError(t, err)

	// One probe inside the unit's own restart window: the failure is real, its
	// age is not yet evidence that the unit gave up.
	failingSince := time.Now().UTC().Add(-time.Second).Truncate(time.Second)
	require.NoError(t, qubeRepo.UpdateAgentHealth(
		ctx, createdOp.Qube.ID, models.AgentHealthUnreachable, failingSince, "connection refused"))
	require.NoError(t, qubeRepo.UpdateAgentHealth(
		ctx, createdOp.Qube.ID, models.AgentHealthUnreachable,
		failingSince.Add(time.Second), "connection refused"))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/qubes/"+createdOp.Qube.ID, nil)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
	assert.Equal(t, "unreachable", raw["agent_health"])
	assert.Equal(t, "pending", raw["agent_recovery"],
		"a fresh failure must not be reported as needing manual recovery")
	assert.NotEmpty(t, raw["agent_failing_since"], "but its start is already recorded")
}

// TestQubeHandler_RecoveredAgentClearsTheRecoveryReading — "just recovered" must
// not keep an alarm on screen. The streak is cleared by the healthy probe, and
// the reading follows it back to none.
func TestQubeHandler_RecoveredAgentClearsTheRecoveryReading(t *testing.T) {
	router, zoneSvc, qubeSvc, qubeRepo := setupAgentHealthRouter(t)

	ctx := context.Background()
	zone := createTestZoneForHandler(t, zoneSvc)

	createdOp, err := qubeSvc.Create(ctx, &models.QubeCreateRequest{
		Name: "recovered", Type: models.QubeTypeApp, ZoneID: zone.ID,
	})
	require.NoError(t, err)

	firstFailure := time.Now().UTC().Add(-models.AgentStartLimitBudget).Truncate(time.Second)
	require.NoError(t, qubeRepo.UpdateAgentHealth(
		ctx, createdOp.Qube.ID, models.AgentHealthUnreachable, firstFailure, "connection refused"))
	require.NoError(t, qubeRepo.UpdateAgentHealth(
		ctx, createdOp.Qube.ID, models.AgentHealthUnreachable,
		firstFailure.Add(models.AgentStartLimitBudget), "connection refused"))

	// The unit was reset-failed and the agent came back.
	require.NoError(t, qubeRepo.UpdateAgentHealth(
		ctx, createdOp.Qube.ID, models.AgentHealthHealthy, time.Now().UTC(), ""))

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/qubes/"+createdOp.Qube.ID, nil)
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
	assert.Equal(t, "healthy", raw["agent_health"])
	assert.Equal(t, "none", raw["agent_recovery"])
	assert.NotContains(t, raw, "agent_failing_since", "the streak is over and must not linger")
	assert.NotContains(t, raw, "agent_last_error", "and the stale complaint goes with it")
}
