package agent

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func runFileCopy(t *testing.T, request []byte, roots string) (string, string, int) {
	t.Helper()
	content, err := os.ReadFile("../../../../remote/qubes-rpc/qubesair.FileCopy")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "FileCopy")
	require.NoError(t, os.WriteFile(path, content, 0o700))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "QUBESAIR_FILECOPY_ROOTS=" + roots}
	cmd.Stdin = bytes.NewReader(request)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	err = cmd.Run()
	require.NoError(t, ctx.Err())
	if err != nil {
		var exit *exec.ExitError
		require.ErrorAs(t, err, &exit)
		return out.String(), stderr.String(), exit.ExitCode()
	}
	return out.String(), stderr.String(), 0
}

func TestFileCopyRoundTripAndDeniedPaths(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	dst := filepath.Join(root, "file")
	out, stderr, code := runFileCopy(t, []byte("push "+dst+"\nhello"), root)
	require.Zero(t, code, stderr)
	require.Contains(t, out, "OK push 5 ")
	out, stderr, code = runFileCopy(t, []byte("pull "+dst+"\n"), root)
	require.Zero(t, code, stderr)
	require.Equal(t, "hello", out)
	outside, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "escape")))
	for _, target := range []string{outside + "/file", root + "/escape/file", root + "/../file", root} {
		_, _, code = runFileCopy(t, []byte("push "+target+"\nbad"), root)
		require.NotZero(t, code, target)
	}
	_, err = os.Stat(filepath.Join(outside, "file"))
	require.True(t, os.IsNotExist(err))
	_, _, code = runFileCopy(t, []byte("pull "+dst+"\n"), "")
	require.Equal(t, 77, code)
}

func TestFileCopyOversizePushDoesNotReplaceDestination(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	dst := filepath.Join(root, "file")
	require.NoError(t, os.WriteFile(dst, []byte("original"), 0o600))
	_, _, code := runFileCopy(t, []byte("push "+dst+"\n"+strings.Repeat("x", 16*1024*1024+1)), root)
	require.NotZero(t, code)
	content, err := os.ReadFile(dst)
	require.NoError(t, err)
	require.Equal(t, "original", string(content))
	entries, err := os.ReadDir(root)
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestFileCopyMalformedAndOversizePull(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	for _, input := range []string{"", "push relative\nbody", "delete " + root + "/file\n", "push " + root + "/file", "push " + strings.Repeat("a", 4096) + "\n", "pull " + root + "/bad\x00name\n"} {
		_, _, code := runFileCopy(t, []byte(input), root)
		require.NotZero(t, code)
	}
	file, err := os.Create(filepath.Join(root, "large"))
	require.NoError(t, err)
	require.NoError(t, file.Truncate(16*1024*1024+1))
	require.NoError(t, file.Close())
	out, _, code := runFileCopy(t, []byte("pull "+root+"/large\n"), root)
	require.NotZero(t, code)
	require.Empty(t, out)
	require.NoError(t, os.Mkdir(filepath.Join(root, "directory"), 0o700))
	_, _, code = runFileCopy(t, []byte("pull "+root+"/directory\n"), root)
	require.NotZero(t, code)
}
