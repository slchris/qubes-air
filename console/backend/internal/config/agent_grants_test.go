package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testPingService is the reachability probe, the one service granted by default.
const testPingService = "qubesair.Ping"

// The whole grant set is validated at startup, not only the path allowlists
// (TestConfig_ValidateRejectsUnsafePathAllowlists): a service name becomes a
// comma-separated entry on an agent.env line that a root service reads.
func TestConfig_ValidateRejectsUndeliverableAgentGrants(t *testing.T) {
	cases := []struct {
		name    string
		modify  func(cfg *Config)
		wantErr string
	}{
		{name: "services as the fleet config ships them", modify: func(c *Config) {
			c.Orchestrator.AgentAllowedServices = []string{testPingService, "qubesair.UnlockData", "qubesair.RekeyData"}
		}},
		{name: "exec paired with its service", modify: func(c *Config) {
			c.Orchestrator.AgentAllowedServices = []string{testPingService, "qubesair.Exec"}
			c.Orchestrator.AgentExecAllow = []string{"/usr/bin/qm"}
		}},
		{name: "service injecting an agent.env line", modify: func(c *Config) {
			c.Orchestrator.AgentAllowedServices = []string{testPingService + "\nQUBESAIR_EXEC_ALLOW=/bin/sh"}
		}, wantErr: "agent_allowed_services"},
		{name: "service containing a comma", modify: func(c *Config) {
			c.Orchestrator.AgentAllowedServices = []string{testPingService + ",qubesair.UnlockData"}
		}, wantErr: "not a valid qrexec service name"},
		{name: "service containing a space", modify: func(c *Config) {
			c.Orchestrator.AgentAllowedServices = []string{"qubesair Ping"}
		}, wantErr: "not a valid qrexec service name"},
		{name: "duplicate service", modify: func(c *Config) {
			c.Orchestrator.AgentAllowedServices = []string{testPingService, testPingService}
		}, wantErr: "listed twice"},
		{name: "duplicate exec path", modify: func(c *Config) {
			c.Orchestrator.AgentExecAllow = []string{"/usr/bin/id", "/usr/bin/id"}
		}, wantErr: "agent_exec_allow: \"/usr/bin/id\" is listed twice"},
		{name: "tab in a filecopy root", modify: func(c *Config) {
			c.Orchestrator.AgentFileCopyRoots = []string{"/var/\ttmp"}
		}, wantErr: "agent_filecopy_roots"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tc.modify(cfg)
			err := cfg.Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

// Paths whose service is not granted are inert, not unsafe: the console still
// starts, and the startup log says which grant does nothing and how to fix it.
func TestConfig_AgentGrantWarningsNameInertAllowlists(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Orchestrator.AgentExecAllow = []string{"/usr/bin/qm"}
	cfg.Orchestrator.AgentFileCopyRoots = []string{"/var/lib/vz/dump"}
	require.NoError(t, cfg.Validate(), "an unpaired allowlist must not stop the console from starting")

	warnings := cfg.AgentGrantWarnings()
	require.Len(t, warnings, 2)
	assert.Contains(t, warnings[0], "agent_exec_allow lists 1 path(s) but agent_allowed_services does not allow qubesair.Exec")
	assert.Contains(t, warnings[1], "agent_filecopy_roots lists 1 path(s) but agent_allowed_services does not allow qubesair.FileCopy")

	cfg.Orchestrator.AgentAllowedServices = []string{testPingService, "qubesair.Exec", "qubesair.FileCopy"}
	assert.Empty(t, cfg.AgentGrantWarnings())

	cfg.Orchestrator.AgentAllowedServices = []string{"bad service"}
	assert.Nil(t, cfg.AgentGrantWarnings(), "an invalid grant set is Validate's to refuse, not a warning")
}

// The same values arriving through the environment (the salt-managed path)
// are validated the same way: Load refuses them before anything is provisioned.
func TestConfig_LoadRefusesUndeliverableServicesFromEnv(t *testing.T) {
	t.Setenv("QUBES_AIR_AGENT_ALLOWED_SERVICES", testPingService+",qubesair.Ping")
	_, err := Load("")
	require.ErrorContains(t, err, "listed twice")
}
