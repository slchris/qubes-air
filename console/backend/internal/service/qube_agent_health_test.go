package service

import (
	"testing"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/stretchr/testify/assert"
)

// TestLostVisibilityDoesNotLeaveStaleGreen — a console that can no longer probe
// must stop claiming the agent is healthy.
//
// The original code returned early whenever nothing was learned, meaning the row
// kept its previous "healthy" AND its old probe timestamp. A console whose CA
// became unusable — an unparseable stored CA, or a decrypt failure after a key
// rotation — therefore went on presenting a green verdict indistinguishable
// from one confirmed a second ago, while the agent could be dead the whole time.
// That is the "signal stayed green for hours" bug in a new place.
func TestLostVisibilityDoesNotLeaveStaleGreen(t *testing.T) {
	res := AgentProbeResult{Status: AgentProbeNotConfigured, Authoritative: true}
	assert.Equal(t, models.AgentHealthUnknown, agentHealthForResult(res, AgentProbeSteady),
		"a probe that could not run must not read as a probe that succeeded")
}

// TestNonAuthoritativeSuccessIsNotHealthy — the legacy global transport is
// pinned to a single endpoint whose invoker explicitly ignores the target name
// ("target carries no authority", internal/agent/invoker.go). It therefore
// answers for every qube alike.
//
// Recording that as healthy stores a verdict about a machine nothing ever
// contacted: a qube with no IP address at all would be marked green off a pong
// that named a different remote.
func TestNonAuthoritativeSuccessIsNotHealthy(t *testing.T) {
	global := AgentProbeResult{Status: AgentProbeOK, Authoritative: false}
	assert.Equal(t, models.AgentHealthUnknown, agentHealthForResult(global, AgentProbeSteady),
		"a pong that cannot be attributed to this qube is not evidence about this qube")

	perQube := AgentProbeResult{Status: AgentProbeOK, Authoritative: true}
	assert.Equal(t, models.AgentHealthHealthy, agentHealthForResult(perQube, AgentProbeSteady),
		"a real per-qube probe must still be able to report healthy")
}
