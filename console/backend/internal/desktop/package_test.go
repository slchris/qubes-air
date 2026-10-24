package desktop

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPackageHooksOptInAndFailurePropagation(t *testing.T) {
	for _, tc := range []struct {
		hook, arg, failure string
		online             bool
	}{
		{"postinst", "configure", "", true}, {"postinst", "configure", "", false},
		{"postinst", "configure", "enable", true}, {"postinst", "configure", "daemon-reload", true},
		{"postinst", "configure", "try-restart", true}, {"prerm", "remove", "stop", true},
		{"prerm", "remove", "disable", true}, {"postrm", "remove", "daemon-reload", true},
	} {
		t.Run(tc.hook+"/"+tc.failure, func(t *testing.T) {
			dir := t.TempDir()
			data, err := os.ReadFile(filepath.Join("../../../../packaging/agent-deb", tc.hook))
			require.NoError(t, err)
			script := strings.ReplaceAll(string(data), "/run/systemd/system", filepath.Join(dir, "runtime"))
			script = strings.ReplaceAll(script, "/etc/systemd/system/qubes-air-agent.service", filepath.Join(dir, "absent-legacy-unit"))
			if tc.online {
				require.NoError(t, os.Mkdir(filepath.Join(dir, "runtime"), 0700))
			}
			path := filepath.Join(dir, "hook")
			require.NoError(t, os.WriteFile(path, []byte(script), 0700))
			stub := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$HOOK_CALLS\"\n[ \"$1\" != \"$HOOK_FAIL\" ]\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "systemctl"), []byte(stub), 0700))
			cmd := exec.Command("/bin/sh", path, tc.arg)
			cmd.Env = []string{"PATH=" + dir, "HOOK_CALLS=" + filepath.Join(dir, "calls"), "HOOK_FAIL=" + tc.failure}
			out, err := cmd.CombinedOutput()
			if tc.failure == "" {
				require.NoError(t, err, string(out))
			} else {
				require.Error(t, err)
			}
			calls, err := os.ReadFile(filepath.Join(dir, "calls"))
			require.NoError(t, err)
			if tc.hook == "postinst" {
				require.NotContains(t, string(calls), "qubes-air-desktop")
			}
			if !tc.online {
				require.Equal(t, "enable qubes-air-agent.service\n", string(calls))
			}
		})
	}
}
