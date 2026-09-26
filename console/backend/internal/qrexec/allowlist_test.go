package qrexec

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The rules here are the ones the guest re-checks at use time, so a value this
// function accepts is a value the agent will accept. Everything it rejects would
// otherwise be delivered and then refused (or split) inside the guest.
func TestValidatePathAllowlist(t *testing.T) {
	cases := []struct {
		name       string
		entries    []string
		forbidRoot bool
		wantErr    string
	}{
		{name: "empty list is the disabled default", entries: nil},
		{name: "program", entries: []string{"/usr/bin/id"}},
		{name: "several programs", entries: []string{"/usr/bin/id", "/usr/bin/uptime"}},
		{name: "directory root", entries: []string{"/var/tmp"}},
		{name: "root allowed for programs", entries: []string{"/"}},

		{name: "empty entry", entries: []string{"/usr/bin/id", ""}, wantErr: "empty entry"},
		{name: "relative", entries: []string{"usr/bin/id"}, wantErr: "not an absolute path"},
		{name: "colon inside an entry", entries: []string{"/usr/bin/a:b"}, wantErr: "separates entries"},
		{name: "newline injection", entries: []string{"/usr/bin/id\nQUBESAIR_ALLOW=everything"}, wantErr: "control character"},
		{name: "carriage return injection", entries: []string{"/usr/bin/id\r"}, wantErr: "control character"},
		{name: "nul", entries: []string{"/usr/bin/id\x00"}, wantErr: "control character"},
		{name: "tab", entries: []string{"/usr/bin/i\td"}, wantErr: "control character"},
		{name: "escape", entries: []string{"/usr/bin/\x1b[0mid"}, wantErr: "control character"},
		{name: "delete", entries: []string{"/usr/bin/id\x7f"}, wantErr: "control character"},
		{name: "duplicate entry", entries: []string{"/usr/bin/id", "/usr/bin/uptime", "/usr/bin/id"}, wantErr: "listed twice"},
		{name: "trailing slash", entries: []string{"/usr/bin/"}, wantErr: "not normalized"},
		{name: "dot segment", entries: []string{"/usr/./bin"}, wantErr: "not normalized"},
		{name: "dotdot segment", entries: []string{"/usr/bin/../sbin"}, wantErr: "not normalized"},
		{name: "duplicate slash", entries: []string{"/usr//bin"}, wantErr: "not normalized"},
		{name: "bare dot", entries: []string{"."}, wantErr: "not an absolute path"},

		{name: "root forbidden for filecopy roots", entries: []string{"/"}, forbidRoot: true, wantErr: "whole filesystem"},
		{name: "root forbidden even beside a real root", entries: []string{"/var/tmp", "/"}, forbidRoot: true, wantErr: "whole filesystem"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePathAllowlist("QUBESAIR_EXEC_ALLOW", tc.entries, tc.forbidRoot)
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// The wire format is colon-separated because that is what the agent splits on;
// joining with anything else delivers one path that does not exist.
func TestJoinPathAllowlistUsesTheAgentSeparator(t *testing.T) {
	require.Equal(t, "", JoinPathAllowlist(nil))
	require.Equal(t, "/usr/bin/id", JoinPathAllowlist([]string{"/usr/bin/id"}))
	require.Equal(t, "/usr/bin/id:/usr/bin/uptime", JoinPathAllowlist([]string{"/usr/bin/id", "/usr/bin/uptime"}))
	require.Equal(t, "/var/tmp:/srv/in", JoinPathAllowlist([]string{"/var/tmp", "/srv/in"}))
}

var testLabels = GrantLabels{Services: "services", Exec: "exec", FileCopy: "roots"}

// Every grant list is checked before it reaches a guest. Errors are for grants
// that cannot be delivered as written; the one inert-but-deliverable case (paths
// without their service) is a warning, covered separately below.
func TestValidateAgentGrants(t *testing.T) {
	cases := []struct {
		name     string
		services []string
		exec     []string
		roots    []string
		wantErr  string
	}{
		{name: "default deny", services: []string{"qubesair.Ping"}},
		{name: "no services at all", services: nil},
		{name: "exec with its service", services: []string{"qubesair.Ping", ExecService}, exec: []string{"/usr/bin/id"}},
		{name: "filecopy with its service", services: []string{FileCopyService}, roots: []string{"/home/user/data", "/data"}},
		{name: "argument-form service", services: []string{"qubesair.StreamTCP+5900"}},

		{name: "comma splits one grant into two", services: []string{"qubesair.Ping,qubesair.UnlockData"}, wantErr: "not a valid qrexec service name"},
		{name: "newline injects an agent.env line", services: []string{"qubesair.Ping\nQUBESAIR_EXEC_ALLOW=/bin/sh"}, wantErr: "not a valid qrexec service name"},
		{name: "leading space", services: []string{" qubesair.Ping"}, wantErr: "not a valid qrexec service name"},
		{name: "inner whitespace", services: []string{"qubesair Ping"}, wantErr: "not a valid qrexec service name"},
		{name: "tab", services: []string{"qubesair.Ping\t"}, wantErr: "not a valid qrexec service name"},
		{name: "empty service", services: []string{"qubesair.Ping", ""}, wantErr: "not a valid qrexec service name"},
		{name: "path traversal in a service", services: []string{"../qubesair.Ping"}, wantErr: "not a valid qrexec service name"},
		{name: "overlong service", services: []string{strings.Repeat("a", 129)}, wantErr: "not a valid qrexec service name"},
		{name: "duplicate service", services: []string{"qubesair.Ping", ExecService, "qubesair.Ping"}, wantErr: "listed twice"},

		{name: "relative executable", services: []string{ExecService}, exec: []string{"usr/bin/id"}, wantErr: "exec: \"usr/bin/id\" is not an absolute path"},
		{name: "parent traversal", services: []string{ExecService}, exec: []string{"/usr/bin/../bin/id"}, wantErr: "not normalized"},
		{name: "colon delimiter", services: []string{ExecService}, exec: []string{"/opt/tool:arg"}, wantErr: "separates entries"},
		{name: "control character in a path", services: []string{ExecService}, exec: []string{"/usr/bin/id\nQUBESAIR_ALLOW=qubesair.UnlockData"}, wantErr: "control character"},
		{name: "tab in a path", services: []string{ExecService}, exec: []string{"/usr/bin/i\td"}, wantErr: "control character"},
		{name: "duplicate executable", services: []string{ExecService}, exec: []string{"/usr/bin/id", "/usr/bin/id"}, wantErr: "exec: \"/usr/bin/id\" is listed twice"},
		{name: "filesystem root as filecopy root", services: []string{FileCopyService}, roots: []string{"/"}, wantErr: "whole filesystem"},
		{name: "duplicate filecopy root", services: []string{FileCopyService}, roots: []string{"/data", "/data"}, wantErr: "roots: \"/data\" is listed twice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			warnings, err := ValidateAgentGrants(testLabels, tc.services, tc.exec, tc.roots)
			if tc.wantErr == "" {
				require.NoError(t, err)
				require.Empty(t, warnings)
				return
			}
			require.ErrorContains(t, err, tc.wantErr)
			require.Nil(t, warnings)
		})
	}
}

// Paths without their service are delivered but grant nothing: the agent
// refuses the service before it ever reads the allowlist. That is reported, not
// refused, so a console configured that way (as the qubes-salt-config README
// currently instructs) still starts; the warning names the fix.
func TestValidateAgentGrantsWarnsAboutPathsWithoutTheirService(t *testing.T) {
	warnings, err := ValidateAgentGrants(testLabels, []string{"qubesair.Ping"}, []string{"/usr/bin/id"}, nil)
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "exec lists 1 path(s) but services does not allow qubesair.Exec")
	assert.Contains(t, warnings[0], "add qubesair.Exec to services or clear exec")

	warnings, err = ValidateAgentGrants(testLabels, nil, []string{"/usr/bin/id"}, []string{"/data", "/srv"})
	require.NoError(t, err)
	require.Len(t, warnings, 2)
	assert.Contains(t, warnings[1], "roots lists 2 path(s) but services does not allow qubesair.FileCopy")

	// Each pairing is independent: granting one service does not excuse the other.
	warnings, err = ValidateAgentGrants(testLabels, []string{ExecService}, []string{"/usr/bin/id"}, []string{"/data"})
	require.NoError(t, err)
	require.Len(t, warnings, 1)
	assert.Contains(t, warnings[0], "qubesair.FileCopy")

	// A hard error wins over a warning: nothing is returned but the error.
	warnings, err = ValidateAgentGrants(testLabels, []string{"qubesair.Ping"}, []string{"relative"}, nil)
	require.Error(t, err)
	assert.Nil(t, warnings)
}

// The agent.env labels are the names the guest actually reads.
func TestAgentEnvLabelsNameTheGuestVariables(t *testing.T) {
	assert.Equal(t, GrantLabels{Services: "QUBESAIR_ALLOW", Exec: "QUBESAIR_EXEC_ALLOW", FileCopy: "QUBESAIR_FILECOPY_ROOTS"}, AgentEnvLabels)
}
