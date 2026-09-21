package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// serviceDir creates a temp service directory with the given executable scripts.
//
// Service names here follow the real Qubes convention (qubesair.X / qubes.X).
// That is not only for realism: macOS refuses to execute a file whose extension
// is ".Service", killing it with SIGKILL before it runs, so a test using a name
// like "echo.Service" fails on a Mac for reasons that have nothing to do with
// this code. Linux, where the agent actually runs, does not care.
func serviceDir(t *testing.T, scripts map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range scripts {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func invokerOver(dir string, allowed ...string) *LocalInvoker {
	inv := NewLocalInvoker("remote-dev", allowed)
	inv.ServiceDir = dir
	// Generous on purpose. This bound exists to stop a genuinely hung service
	// from wedging the suite, NOT to assert how fast forking /bin/sh is. At 5s
	// it was measuring machine load instead: green alone, red under `go test
	// ./...` where 15 packages compete for CPU. A suite that fails under
	// parallelism teaches people to re-run until green, which is how a real
	// regression gets waved through. Passing runs finish in well under a second,
	// so the higher ceiling costs nothing.
	inv.Timeout = 30 * time.Second
	return inv
}

// TestInvokeRunsService — the ordinary path, including that the remote name
// reaches the script. qubesair.Ping depends on that variable.
func TestInvokeRunsService(t *testing.T) {
	dir := serviceDir(t, map[string]string{
		"qubesair.Ping": "#!/bin/sh\nprintf 'pong %s\\n' \"$QUBESAIR_REMOTE_NAME\"\n",
	})
	out, err := invokerOver(dir, "qubesair.Ping").
		Invoke(context.Background(), "remote-dev", "qubesair.Ping", nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if got := strings.TrimSpace(string(out.Stdout)); got != "pong remote-dev" {
		t.Errorf("want %q, got %q", "pong remote-dev", got)
	}
}

// TestInvokeRejectsPathTraversal is the one that matters most: the service name
// arrives over the network and becomes a path element. Without this an attacker
// could name any executable on the host.
func TestInvokeRejectsPathTraversal(t *testing.T) {
	dir := serviceDir(t, map[string]string{"ok": "#!/bin/sh\necho fine\n"})
	inv := invokerOver(dir) // empty allowlist: name validation must stand alone

	for _, bad := range []string{
		"../../../bin/sh",
		"..",
		"a/b",
		`a\b`,
		"/bin/sh",
		"",
		"foo;rm -rf /",
		"foo$(whoami)",
		"foo bar",
	} {
		_, err := inv.Invoke(context.Background(), "t", bad, nil)
		if !errors.Is(err, ErrInvalidServiceName) && !errors.Is(err, ErrUnknownService) {
			t.Errorf("service %q must be rejected, got %v", bad, err)
		}
	}
}

// TestInvokeAllowlist — an allowlisted set means anything else is refused even
// when the script exists. Guards against a script dropped into the directory
// becoming callable by accident.
func TestInvokeAllowlist(t *testing.T) {
	dir := serviceDir(t, map[string]string{
		"qubesair.Ping": "#!/bin/sh\necho pong\n",
		"qubes.VMShell": "#!/bin/sh\necho SHOULD-NOT-RUN\n",
	})
	inv := invokerOver(dir, "qubesair.Ping")

	if _, err := inv.Invoke(context.Background(), "t", "qubesair.Ping", nil); err != nil {
		t.Errorf("allowed service must run: %v", err)
	}
	_, err := inv.Invoke(context.Background(), "t", "qubes.VMShell", nil)
	if !errors.Is(err, ErrServiceNotAllowed) {
		t.Errorf("want ErrServiceNotAllowed for a present-but-unlisted service, got %v", err)
	}
}

// TestAllowlistMatchesBaseName — Qubes services may carry a "+argument", and an
// allowlist entry must cover them without listing every possible argument.
func TestAllowlistMatchesBaseName(t *testing.T) {
	dir := serviceDir(t, map[string]string{
		"qubesair.Status": "#!/bin/sh\nprintf 'arg=%s' \"$1\"\n",
	})
	out, err := invokerOver(dir, "qubesair.Status").
		Invoke(context.Background(), "t", "qubesair.Status+disk", nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if got := string(out.Stdout); got != "arg=disk" {
		t.Errorf("the argument must reach the script, got %q", got)
	}
}

// TestInvokeUnknownService — a missing implementation is distinct from a denied
// one, because the operator's next action differs.
func TestInvokeUnknownService(t *testing.T) {
	inv := invokerOver(serviceDir(t, nil))
	_, err := inv.Invoke(context.Background(), "t", "qubesair.Nope", nil)
	if !errors.Is(err, ErrUnknownService) {
		t.Errorf("want ErrUnknownService, got %v", err)
	}
}

// TestInvokeNonExecutable — a file that exists but is not executable is a
// common mistake, and the error should say so rather than report "not found".
func TestInvokeNonExecutable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "qubesair.Ping"), []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := invokerOver(dir).Invoke(context.Background(), "t", "qubesair.Ping", nil)
	if !errors.Is(err, ErrUnknownService) {
		t.Fatalf("want ErrUnknownService, got %v", err)
	}
	if !strings.Contains(err.Error(), "not executable") {
		t.Errorf("the error must name the real problem, got %v", err)
	}
}

// TestInvokePassesStdin — services read their request body from stdin.
func TestInvokePassesStdin(t *testing.T) {
	dir := serviceDir(t, map[string]string{"qubesair.Echo": "#!/bin/sh\ncat\n"})
	out, err := invokerOver(dir).Invoke(context.Background(), "t", "qubesair.Echo", []byte("hello agent"))
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if string(out.Stdout) != "hello agent" {
		t.Errorf("stdin must reach the service, got %q", out.Stdout)
	}
}

// TestInvokeReportsExitCodeAndStderr — a service that fails is a RESULT, not an
// invocation error: the exit code and stderr must come back structured so a
// caller can tell a failed command from a call that never ran.
func TestInvokeReportsExitCodeAndStderr(t *testing.T) {
	dir := serviceDir(t, map[string]string{
		"qubesair.Failing": "#!/bin/sh\necho 'vault is sealed' >&2\necho partial\nexit 3\n",
	})
	res, err := invokerOver(dir).Invoke(context.Background(), "t", "qubesair.Failing", nil)
	if err != nil {
		t.Fatalf("a non-zero exit must not be an error, got %v", err)
	}
	if res.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3", res.ExitCode)
	}
	if !strings.Contains(string(res.Stderr), "vault is sealed") {
		t.Errorf("stderr must be captured separately, got %q", res.Stderr)
	}
	if got := strings.TrimSpace(string(res.Stdout)); got != "partial" {
		t.Errorf("stdout must be preserved alongside a non-zero exit, got %q", got)
	}
}

// TestInvokeTimeout — a hanging service must not hold the tunnel forever.
func TestInvokeTimeout(t *testing.T) {
	dir := serviceDir(t, map[string]string{"qubesair.Slow": "#!/bin/sh\nsleep 30\n"})
	inv := invokerOver(dir)
	inv.Timeout = 200 * time.Millisecond

	start := time.Now()
	_, err := inv.Invoke(context.Background(), "t", "qubesair.Slow", nil)
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("the error must say it timed out, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("timeout was not enforced promptly: %s", elapsed)
	}
}

// TestInvokeMinimalEnvironment — the agent's own environment may hold its TLS
// key path and endpoints. A service script has no need of them, and inheriting
// wholesale is how such things end up in logs.
func TestInvokeMinimalEnvironment(t *testing.T) {
	t.Setenv("QUBESAIR_AGENT_SECRET", "must-not-leak")

	dir := serviceDir(t, map[string]string{"qubesair.Env": "#!/bin/sh\nenv\n"})
	out, err := invokerOver(dir).Invoke(context.Background(), "t", "qubesair.Env", nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	env := string(out.Stdout)
	if strings.Contains(env, "must-not-leak") {
		t.Error("the agent's environment must not be inherited by services")
	}
	if !strings.Contains(env, "QUBESAIR_REMOTE_NAME=remote-dev") {
		t.Errorf("the remote name must be exported, got:\n%s", env)
	}
}

// TestRealPingScript runs the actual shipped service, so its contract with
// CheckReachable is exercised rather than a stand-in.
func TestRealPingScript(t *testing.T) {
	src, err := os.ReadFile("../../../../remote/qubes-rpc/qubesair.Ping")
	if err != nil {
		t.Skipf("shipped Ping script not found: %v", err)
	}
	dir := serviceDir(t, map[string]string{"qubesair.Ping": string(src)})

	out, err := invokerOver(dir, "qubesair.Ping").
		Invoke(context.Background(), "t", "qubesair.Ping", nil)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	fields := strings.Fields(string(out.Stdout))
	if len(fields) != 3 || fields[0] != "pong" {
		t.Fatalf(`want "pong <name> <ts>", got %q`, out.Stdout)
	}
	if fields[1] != "remote-dev" {
		t.Errorf("want the remote name, got %q", fields[1])
	}
}

// TestInvokeTimeoutSurvivesBackgroundedChild — the timeout must bound the call
// even when the service leaves a process behind.
//
// Stdout is a buffer, not a file, so exec opens an OS pipe and Wait blocks until
// every writer closes it. The context kills only the direct child; a grandchild
// inherits the write end and holds it open. Without cmd.WaitDelay the call
// therefore ran for the grandchild's lifetime — 30s against a 200ms timeout —
// so any service that backgrounds anything could pin an agent worker.
func TestInvokeTimeoutSurvivesBackgroundedChild(t *testing.T) {
	dir := serviceDir(t, map[string]string{
		// Exits immediately, leaving a child that keeps the pipe open.
		"qubesair.Leaky": "#!/bin/sh\nsleep 30 &\nexit 0\n",
	})
	inv := invokerOver(dir)
	inv.Timeout = 200 * time.Millisecond

	start := time.Now()
	_, _ = inv.Invoke(context.Background(), "t", "qubesair.Leaky", nil)

	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("a backgrounded grandchild held the call open for %s; WaitDelay is not bounding the drain", elapsed)
	}
}

// TestCapWriterBoundsOutput — the cap is enforced WHILE writing, not after
// buffering the whole thing. The aborting writer fails the write (so exec stops
// the copy and the command dies); the truncating one keeps at most limit bytes.
func TestCapWriterBoundsOutput(t *testing.T) {
	abort := &capWriter{limit: 4, abortOnOverflow: true}
	if _, err := abort.Write([]byte("abcdef")); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("aborting writer: want ErrResponseTooLarge, got %v", err)
	}
	if !abort.overflow || abort.buf.Len() != 4 {
		t.Errorf("aborting writer kept %d bytes, overflow=%v", abort.buf.Len(), abort.overflow)
	}

	trunc := &capWriter{limit: 4}
	n, err := trunc.Write([]byte("abcdef"))
	if err != nil || n != 6 {
		t.Fatalf("truncating writer: n=%d err=%v", n, err)
	}
	if !trunc.overflow || trunc.buf.String() != "abcd" {
		t.Errorf("truncating writer kept %q overflow=%v", trunc.buf.String(), trunc.overflow)
	}
}

// TestInvokeOversizeStdoutIsRejected — a service that floods stdout is stopped at
// the cap rather than buffered in full and only then rejected.
func TestInvokeOversizeStdoutIsRejected(t *testing.T) {
	dir := serviceDir(t, map[string]string{
		"qubesair.Flood": "#!/bin/sh\ntr '\\0' x < /dev/zero | head -c 20000000\n",
	})
	_, err := invokerOver(dir, "qubesair.Flood").
		Invoke(context.Background(), "remote-dev", "qubesair.Flood", nil)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("want ErrResponseTooLarge, got %v", err)
	}
}
