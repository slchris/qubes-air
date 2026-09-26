package main

import (
	"bytes"
	"log"
	"testing"

	"github.com/slchris/qubes-air/console/internal/config"
	"github.com/stretchr/testify/assert"
)

// An allowlist whose service is not granted does nothing in the guest. The
// console still starts with it, so the startup log is where the operator learns
// that, with the config key to change.
func TestStartupLogsInertAgentGrants(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })

	cfg := config.DefaultConfig()
	cfg.Orchestrator.AgentExecAllow = []string{"/usr/bin/qm"}
	logStartupWarnings(cfg)
	assert.Contains(t, buf.String(),
		"WARNING: agent grants: agent_exec_allow lists 1 path(s) but agent_allowed_services does not allow qubesair.Exec")

	buf.Reset()
	cfg.Orchestrator.AgentAllowedServices = []string{"qubesair.Ping", "qubesair.Exec"}
	logStartupWarnings(cfg)
	assert.NotContains(t, buf.String(), "agent grants")
}
