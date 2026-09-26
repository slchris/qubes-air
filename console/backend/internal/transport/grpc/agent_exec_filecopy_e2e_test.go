package grpc

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/agent"
	"github.com/stretchr/testify/require"
)

// stageServices copies the shipped qrexec scripts into a fresh service dir.
func stageServices(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		script, err := os.ReadFile("../../../../../remote/qubes-rpc/" + name)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), script, 0o700))
	}
	return dir
}

func absTool(t *testing.T, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	require.NoError(t, err)
	p, err = filepath.Abs(p)
	require.NoError(t, err)
	return p
}

func startAgentTestServer(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, invoker *agent.LocalInvoker) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := lis.Addr().String()
	require.NoError(t, lis.Close())
	server := NewServer(ServerConfig{Listen: addr, TLS: mkServerTLS(t, ca, caKey)}, invoker)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = server.Serve(ctx) }()
	waitDial(t, addr)
	return addr
}

func startAgentTestClient(t *testing.T, addr string, ca *x509.Certificate, caKey *ecdsa.PrivateKey) *Client {
	t.Helper()
	cli := NewClient(ClientConfig{
		RemoteEndpoint: addr,
		RelayName:      "sys-relay-pve",
		RemoteName:     "remote-dev",
		TLS:            mkClientTLS(t, ca, caKey),
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = cli.Start(ctx) }()
	_, err := callWhenReady(t, cli, "remote-dev", "qubesair.Ping", nil)
	require.NoError(t, err)
	return cli
}

// TestAgentExecAndFileCopyEndToEnd runs the shipped Exec and FileCopy scripts
// behind a real mTLS tunnel with the allowlists an agent.env would carry, and
// checks both sides of each grant: an allowed program runs and a program off
// the list is refused; a file inside an allowed root round-trips and a push
// outside every root is refused without creating anything.
func TestAgentExecAndFileCopyEndToEnd(t *testing.T) {
	serviceDir := stageServices(t, "qubesair.Ping", "qubesair.Exec", "qubesair.FileCopy")
	allowedProgram := absTool(t, "printf")
	allowedRoot, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	outsideRoot, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	t.Setenv("QUBESAIR_EXEC_ALLOW", allowedProgram)
	t.Setenv("QUBESAIR_FILECOPY_ROOTS", allowedRoot)

	ca, caKey := mkCA(t)
	invoker := agent.NewLocalInvoker("remote-dev", []string{"qubesair.Ping", "qubesair.Exec", "qubesair.FileCopy"})
	invoker.ServiceDir = serviceDir
	invoker.Timeout = 10 * time.Second
	cli := startAgentTestClient(t, startAgentTestServer(t, ca, caKey, invoker), ca, caKey)
	ctx := context.Background()

	execInput, err := json.Marshal([]string{allowedProgram, "%s", "exec-through-mtls"})
	require.NoError(t, err)
	res, err := cli.CallResult(ctx, "remote-dev", "qubesair.Exec", execInput)
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode, string(res.Stderr))
	require.Equal(t, "exec-through-mtls", string(res.Stdout))

	deniedInput, err := json.Marshal([]string{absTool(t, "id")})
	require.NoError(t, err)
	res, err = cli.CallResult(ctx, "remote-dev", "qubesair.Exec", deniedInput)
	require.NoError(t, err)
	require.Equal(t, 126, res.ExitCode)
	require.Contains(t, string(res.Stderr), "not allowed")

	filePath := filepath.Join(allowedRoot, "roundtrip.txt")
	res, err = cli.CallResult(ctx, "remote-dev", "qubesair.FileCopy", []byte("push "+filePath+"\nfilecopy-through-mtls"))
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode, string(res.Stderr))
	require.Contains(t, string(res.Stdout), "OK push")

	res, err = cli.CallResult(ctx, "remote-dev", "qubesair.FileCopy", []byte("pull "+filePath+"\n"))
	require.NoError(t, err)
	require.Equal(t, 0, res.ExitCode, string(res.Stderr))
	require.Equal(t, "filecopy-through-mtls", string(res.Stdout))

	deniedPath := filepath.Join(outsideRoot, "denied.txt")
	res, err = cli.CallResult(ctx, "remote-dev", "qubesair.FileCopy", []byte("push "+deniedPath+"\nshould-not-write"))
	require.NoError(t, err)
	require.Equal(t, 126, res.ExitCode)
	_, err = os.Stat(deniedPath)
	require.True(t, os.IsNotExist(err), "a refused push must not create the file")
}

// A path allowlist delivered without its service grants nothing: the agent
// refuses the service before the script (and its allowlist) is ever reached.
// This is why the console only warns about that pairing instead of refusing it.
func TestAgentRefusesExecWhoseServiceIsNotAllowedEvenWithPaths(t *testing.T) {
	serviceDir := stageServices(t, "qubesair.Ping", "qubesair.Exec")
	program := absTool(t, "printf")
	t.Setenv("QUBESAIR_EXEC_ALLOW", program)

	ca, caKey := mkCA(t)
	invoker := agent.NewLocalInvoker("remote-dev", []string{"qubesair.Ping"})
	invoker.ServiceDir = serviceDir
	cli := startAgentTestClient(t, startAgentTestServer(t, ca, caKey, invoker), ca, caKey)

	input, err := json.Marshal([]string{program, "ran"})
	require.NoError(t, err)
	res, err := cli.CallResult(context.Background(), "remote-dev", "qubesair.Exec", input)
	require.Error(t, err, "a service outside QUBESAIR_ALLOW must be refused, not run (got %q)", res.Stdout)
	require.Contains(t, err.Error(), "not allowed")
	require.Empty(t, res.Stdout)
}
