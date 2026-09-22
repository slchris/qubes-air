package qrexec

import (
	"testing"

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
