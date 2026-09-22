package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func execServicePath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs("../../../../remote/qubes-rpc/qubesair.Exec")
	require.NoError(t, err)
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	installed := filepath.Join(t.TempDir(), "qubesair.Exec")
	require.NoError(t, os.WriteFile(installed, content, 0o700))
	return installed
}

func runExecService(t *testing.T, input, allow string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, execServicePath(t))
	cmd.Env = []string{"PATH=/usr/bin:/bin", "QUBESAIR_EXEC_ALLOW=" + allow}
	cmd.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	require.NoError(t, ctx.Err())
	if err != nil {
		var exit *exec.ExitError
		require.ErrorAs(t, err, &exit)
		return stdout.String(), stderr.String(), exit.ExitCode()
	}
	return stdout.String(), stderr.String(), 0
}

func TestExecRejectsShellCommandWithoutSideEffects(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "injected")
	_, _, code := runExecService(t, "/usr/bin/printf ok ; /usr/bin/touch "+marker, "/usr/bin/printf")
	_, err := os.Stat(marker)
	require.True(t, os.IsNotExist(err), "shell injection created a file")
	require.NotZero(t, code)
}

func TestExecLiteralArguments(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "injected")
	arg := "$(/usr/bin/touch " + marker + "); echo bad\n> " + marker
	input, err := json.Marshal([]string{"/usr/bin/printf", "%s", arg})
	require.NoError(t, err)
	out, stderr, code := runExecService(t, string(input), "/usr/bin/printf")
	require.Equal(t, 0, code, stderr)
	require.Equal(t, arg, out)
	_, err = os.Stat(marker)
	require.True(t, os.IsNotExist(err))
}

func TestExecInvalidRequests(t *testing.T) {
	for _, input := range []string{
		"", "null", "{}", "[]", `["printf"]`, `["/usr/bin/../bin/printf"]`,
		`["/usr/bin/printf",1]`, `["/usr/bin/printf","\u0000"]`,
		`["/usr/bin/printf"] []`, strings.Repeat(" ", 65537),
	} {
		t.Run(input[:min(len(input), 40)], func(t *testing.T) {
			_, _, code := runExecService(t, input, "/usr/bin/printf")
			require.NotZero(t, code)
		})
	}
	_, _, code := runExecService(t, `["/usr/bin/printf","hello"]`, "")
	require.Equal(t, 77, code)
	_, _, code = runExecService(t, `["/usr/bin/printf","hello"]`, "/usr/bin/id")
	require.Equal(t, 126, code)
}

func installedExecInvoker(t *testing.T, program string) *LocalInvoker {
	t.Helper()
	t.Setenv("QUBESAIR_EXEC_ALLOW", program)
	inv := invokerOver(filepath.Dir(execServicePath(t)), "qubesair.Exec")
	return inv
}

func TestExecStructuredExitAndBoundaries(t *testing.T) {
	dir := serviceDir(t, map[string]string{"result": "#!/bin/sh\nprintf out\nprintf err >&2\nexit 7\n"})
	program := filepath.Join(dir, "result")
	inv := installedExecInvoker(t, program)
	input, err := json.Marshal([]string{program})
	require.NoError(t, err)
	result, err := inv.Invoke(context.Background(), "remote-dev", "qubesair.Exec", input)
	require.NoError(t, err)
	require.Equal(t, 7, result.ExitCode)
	require.Equal(t, "out", string(result.Stdout))
	require.Equal(t, "err", string(result.Stderr))

	for _, count := range []int{128, 129} {
		args := make([]string, count)
		args[0] = "/usr/bin/printf"
		args[1] = "%s"
		input, err = json.Marshal(args)
		require.NoError(t, err)
		_, _, code := runExecService(t, string(input), "/usr/bin/printf")
		if count == 128 {
			require.Zero(t, code)
		} else {
			require.NotZero(t, code)
		}
	}
	for _, size := range []int{4096, 4097} {
		input, err = json.Marshal([]string{"/usr/bin/printf", "%s", strings.Repeat("a", size)})
		require.NoError(t, err)
		out, _, code := runExecService(t, string(input), "/usr/bin/printf")
		if size == 4096 {
			require.Zero(t, code)
			require.Len(t, out, size)
		} else {
			require.NotZero(t, code)
		}
	}
}

func TestExecCancellationIsAnError(t *testing.T) {
	dir := serviceDir(t, map[string]string{"sleep": "#!/bin/sh\nexec sleep 30\n"})
	program := filepath.Join(dir, "sleep")
	inv := installedExecInvoker(t, program)
	input, err := json.Marshal([]string{program})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(200*time.Millisecond, cancel)
	defer timer.Stop()
	defer cancel()
	_, err = inv.Invoke(ctx, "remote-dev", "qubesair.Exec", input)
	require.ErrorIs(t, err, context.Canceled)
}

func TestExecOutputLimitAndTimeout(t *testing.T) {
	dir := serviceDir(t, map[string]string{"flood": "#!/bin/sh\nexec head -c 20000000 /dev/zero\n", "slow": "#!/bin/sh\nexec sleep 30\n"})
	program := filepath.Join(dir, "flood")
	inv := installedExecInvoker(t, program)
	input, err := json.Marshal([]string{program})
	require.NoError(t, err)
	_, err = inv.Invoke(context.Background(), "remote-dev", "qubesair.Exec", input)
	require.ErrorIs(t, err, ErrResponseTooLarge)
	program = filepath.Join(dir, "slow")
	inv = installedExecInvoker(t, program)
	inv.Timeout = 200 * time.Millisecond
	input, err = json.Marshal([]string{program})
	require.NoError(t, err)
	_, err = inv.Invoke(context.Background(), "remote-dev", "qubesair.Exec", input)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}
