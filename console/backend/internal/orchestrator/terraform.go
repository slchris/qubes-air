package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultTimeout is applied to each terraform invocation when the executor is
// configured without an explicit timeout. terraform apply against a real cloud
// can take minutes, so this is generous.
const DefaultTimeout = 15 * time.Minute

// gracefulShutdownDelay is how long terraform gets to shut down cleanly after
// we signal it, before os/exec escalates to SIGKILL. terraform finishes the
// in-flight API call and writes state during this window; cutting it short is
// what orphans infrastructure, so it is deliberately generous.
const gracefulShutdownDelay = 2 * time.Minute

// ErrTargetNotInConfig means the qube is absent from the rendered remote_qubes
// map, so a -target naming it would match nothing.
//
// This exists because terraform does NOT fail on an unresolvable -target: it
// exits 0 with "No changes". Without this guard a caller would read that as
// success and record a qube as running when nothing was ever created.
var ErrTargetNotInConfig = errors.New("qube is not present in the rendered remote_qubes map")

// ValidQubeName reports whether name is safe to pass to an external command as a
// terraform -target / -var argument.
//
// This is the security boundary of the orchestrator: qube names flow from HTTP
// requests down into shell-adjacent exec calls. We allow only a conservative
// character set (alphanumerics plus - _ .) — the same philosophy as
// qrexec.validQrexecArg — so that a name can never inject shell metacharacters,
// terraform address separators, or extra flags. Note exec.Command does not use
// a shell, so this defends terraform argument parsing (and belt-and-braces
// against any future shell use), not `sh -c`.
//
// Rejected examples: "a;rm -rf /", "$(whoami)", "a b", "--var=x", "a\"b",
// "a[0]", "".
func ValidQubeName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	// Must start with an alphanumeric to avoid a leading '-' being parsed as a
	// flag by terraform.
	first := name[0]
	if !isAlnum(first) {
		return false
	}
	for i := 0; i < len(name); i++ {
		if !isValidNameChar(name[i]) {
			return false
		}
	}
	return true
}

func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// isValidNameChar reports whether c is allowed anywhere in a qube name.
func isValidNameChar(c byte) bool {
	return isAlnum(c) || c == '-' || c == '_' || c == '.'
}

// ErrInvalidQubeName is returned when a qube name fails ValidQubeName.
type ErrInvalidQubeName struct {
	Name string
}

func (e *ErrInvalidQubeName) Error() string {
	return fmt.Sprintf("invalid qube name %q: only alphanumerics, '-', '_' and '.' allowed (must start alphanumeric, max 64 chars)", e.Name)
}

// runner executes a prepared command and returns combined stdout. It is a seam
// so tests can capture the exact argv without spawning terraform.
type runner interface {
	run(ctx context.Context, workDir, name string, args, env []string) (string, error)
}

// execRunner is the production runner: it really invokes the binary via
// exec.CommandContext (no shell involved).
type execRunner struct{}

func (execRunner) run(ctx context.Context, workDir, name string, args, env []string) (string, error) {
	// #nosec G204 -- name is a fixed configured binary; every element of args is
	// either a constant or a value validated by ValidQubeName. No shell is used.
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = workDir

	// Credentials reach terraform as environment variables of THIS subprocess
	// rather than as terraform variables. That is not a stylistic choice: a
	// value passed as a terraform variable is recorded in state in plaintext,
	// and this repository's state design forbids long-lived credentials ever
	// entering state. Appending to the parent environment (rather than
	// replacing it) keeps PATH, HOME and the terraform plugin cache intact.
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}

	// exec.CommandContext's default Cancel is Process.Kill() — SIGKILL, which
	// terraform cannot catch. That is not merely an abrupt stop: terraform would
	// die with resources already created in Proxmox but before it persists
	// state, leaving a VM (and, on a provision, a Ceph RBD volume behind a
	// prevent_destroy storage-holder) that terraform no longer knows about. The
	// next apply then creates a *second* one, and the orphan cannot be cleaned
	// up through terraform at all.
	//
	// terraform installs a handler for os.Interrupt (see its makeShutdownCh) and
	// on the first signal stops gracefully, finishing the in-flight operation and
	// writing state. So: signal, then allow a grace period, and only let os/exec
	// escalate to SIGKILL if terraform is still alive after it.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = gracefulShutdownDelay

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%s %s failed: %w, stderr: %s",
			name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// TerraformExecutor is the production Executor. It shells out to the terraform
// binary to drive compute/storage separation for a qube.
//
// It is intentionally configurable (binary path, working dir, var-file,
// timeout) and never interpolates user input into a shell. Suspend/Resume flip
// compute_running for the qube via a targeted apply; the address template is
// how a qube name maps to its compute resource in the root module.
type TerraformExecutor struct {
	// Binary is the terraform executable (default "terraform").
	Binary string
	// WorkDir is the terraform root directory to run in (required).
	WorkDir string
	// BaseVarFile is the OPERATOR-owned var-file (endpoint, node, enable_*_zone,
	// wireguard...). It must NOT define remote_qubes — see GeneratedVarFile.
	BaseVarFile string
	// GeneratedVarFile is the CONSOLE-owned var-file holding remote_qubes,
	// rendered from the database. It is ALWAYS passed last.
	//
	// The ordering is a correctness guarantee, not cosmetics: terraform applies
	// -var-file in the order given and the last one wins for a given variable.
	// It is also deliberately NOT named *.auto.tfvars.json, because an
	// explicit -var-file on the command line outranks any auto-loaded file —
	// so an auto-named file would be silently discarded whenever BaseVarFile is
	// passed. Relative paths resolve against WorkDir.
	GeneratedVarFile string
	// Timeout bounds each invocation (default DefaultTimeout).
	Timeout time.Duration
	// Snapshot returns the desired remote_qubes map (from the DB). When set, it
	// is rendered to GeneratedVarFile before each invocation, making the
	// database the single source of truth for which qubes exist.
	Snapshot QubeSnapshotFunc
	// EnvFunc supplies extra environment variables for the terraform process,
	// typically provider credentials resolved from the encrypted credential
	// store. Returning an error aborts the invocation rather than letting
	// terraform run unauthenticated against whatever the parent environment
	// happens to hold.
	EnvFunc EnvFunc

	// mu serializes render+exec. Both the generated var-file and the terraform
	// state are shared mutable state; without this two concurrent requests can
	// render conflicting maps or collide on terraform's state lock.
	//
	// NOTE: this is correctness-only mutual exclusion. It is NOT the mechanism
	// that makes long operations tolerable — holding a lock for the ~6 minutes a
	// real apply takes would block callers indefinitely. Long-running work is
	// expected to be driven through a job queue that calls in here from a single
	// worker; the mutex is the backstop.
	mu sync.Mutex

	runner runner
}

// QubeSnapshotFunc returns the current desired remote_qubes map, keyed by qube
// name. It is the seam between the console's database and terraform's view of
// which qubes should exist.
type QubeSnapshotFunc func(ctx context.Context) (map[string]any, error)

// EnvFunc returns extra "KEY=value" entries for the terraform process.
//
// Values are secrets. They are never logged, never written to a file, and never
// passed as terraform variables — only handed to the child process.
type EnvFunc func(ctx context.Context) ([]string, error)

// TerraformOption configures a TerraformExecutor.
type TerraformOption func(*TerraformExecutor)

// WithBinary overrides the terraform binary path.
func WithBinary(bin string) TerraformOption {
	return func(t *TerraformExecutor) { t.Binary = bin }
}

// WithVarFile sets the operator-owned base -var-file.
func WithVarFile(path string) TerraformOption {
	return func(t *TerraformExecutor) { t.BaseVarFile = path }
}

// WithGeneratedVarFile sets the console-owned var-file that carries the
// remote_qubes map. It is always passed to terraform last so it wins.
func WithGeneratedVarFile(path string) TerraformOption {
	return func(t *TerraformExecutor) { t.GeneratedVarFile = path }
}

// WithQubeSnapshot supplies the database-backed source of truth for which
// qubes should exist. Without it the generated var-file is never refreshed.
func WithQubeSnapshot(fn QubeSnapshotFunc) TerraformOption {
	return func(t *TerraformExecutor) { t.Snapshot = fn }
}

// WithEnvFunc supplies provider credentials to the terraform process.
func WithEnvFunc(fn EnvFunc) TerraformOption {
	return func(t *TerraformExecutor) { t.EnvFunc = fn }
}

// WithTimeout overrides the per-invocation timeout.
func WithTimeout(d time.Duration) TerraformOption {
	return func(t *TerraformExecutor) { t.Timeout = d }
}

// NewTerraformExecutor builds a TerraformExecutor rooted at workDir.
func NewTerraformExecutor(workDir string, opts ...TerraformOption) *TerraformExecutor {
	t := &TerraformExecutor{
		Binary:  "terraform",
		WorkDir: workDir,
		Timeout: DefaultTimeout,
		runner:  execRunner{},
	}
	for _, opt := range opts {
		opt(t)
	}
	if t.Binary == "" {
		t.Binary = "terraform"
	}
	if t.Timeout <= 0 {
		t.Timeout = DefaultTimeout
	}
	if t.runner == nil {
		t.runner = execRunner{}
	}
	return t
}

// computeTarget returns the terraform resource address for a qube's compute
// instance. It mirrors the Makefile tf-suspend/tf-resume -target expression.
// The name is assumed already validated by the caller.
func computeTarget(qubeName string) string {
	return fmt.Sprintf(`module.remote_qubes[%q].module.proxmox[0].proxmox_virtual_environment_vm.compute`, qubeName)
}

// varFileArgs returns the -var-file flags, base first and generated last.
//
// The order is load-bearing. terraform resolves -var-file left to right and the
// last file to set a variable wins, so putting the console-generated file last
// is what guarantees an operator's hand-written tfvars can never silently
// override the qube set the console believes in. Note maps are replaced
// wholesale, never merged: exactly one file must own remote_qubes.
func (t *TerraformExecutor) varFileArgs() []string {
	var args []string
	if t.BaseVarFile != "" {
		args = append(args, "-var-file="+t.BaseVarFile)
	}
	if t.GeneratedVarFile != "" {
		args = append(args, "-var-file="+t.GeneratedVarFile)
	}
	return args
}

// generatedPath resolves GeneratedVarFile against WorkDir when it is relative.
func (t *TerraformExecutor) generatedPath() string {
	if t.GeneratedVarFile == "" {
		return ""
	}
	if filepath.IsAbs(t.GeneratedVarFile) {
		return t.GeneratedVarFile
	}
	return filepath.Join(t.WorkDir, t.GeneratedVarFile)
}

// renderQubes writes the desired remote_qubes map to GeneratedVarFile.
//
// The write is atomic (temp file in the same directory + rename) so a crash or
// a concurrent terraform read can never observe a half-written file. Callers
// must hold t.mu.
//
// An absent qube set is serialized as an explicit empty map rather than an
// empty document: a variable missing from the last -var-file falls through to
// the earlier one instead of being reset, so omitting the key would silently
// resurrect whatever the operator's base file happens to contain.
func (t *TerraformExecutor) renderQubes(qubes map[string]any) error {
	path := t.generatedPath()
	if path == "" {
		return nil
	}
	if qubes == nil {
		qubes = map[string]any{}
	}

	blob, err := json.MarshalIndent(map[string]any{"remote_qubes": qubes}, "", "  ")
	if err != nil {
		return fmt.Errorf("render remote_qubes: %w", err)
	}
	blob = append(blob, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("render remote_qubes: mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".qubes-*.tfvars.json")
	if err != nil {
		return fmt.Errorf("render remote_qubes: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once renamed

	if _, err := tmp.Write(blob); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("render remote_qubes: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("render remote_qubes: close: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("render remote_qubes: chmod: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("render remote_qubes: rename: %w", err)
	}
	return nil
}

// exec validates the name, refreshes the generated var-file, builds argv, and
// runs terraform. Every caller below funnels through here so neither validation
// nor the render can be skipped.
//
// requireKey asks for the qube to be present in the rendered map before we
// bother terraform. This is not belt-and-braces: terraform exits 0 with "No
// changes" when a -target matches nothing, so without this check a resume of an
// unknown qube looks like a success and the caller records it as running.
// execReadOnly runs a terraform command that only READS state.
//
// Two things it deliberately does not do, both of which exec must:
//
//   - It does not render the generated var-file. `terraform output` reads state
//     and ignores variables entirely, so rendering would be a pure side effect —
//     and the health monitor calls this, meaning a read-only probe was rewriting
//     the file that defines which qubes should exist.
//
//   - It does not block on the executor mutex. exec holds that lock for the
//     whole terraform subprocess, up to DefaultTimeout (15 minutes), and a
//     context deadline cannot interrupt a mutex acquisition. A single
//     address-less qube would therefore stall the entire health sweep for the
//     length of an in-flight apply — no qube probed, no dead agent noticed,
//     which is exactly what the sweep exists to prevent. If the lock is busy we
//     say so and move on; the next sweep will ask again.
func (t *TerraformExecutor) execReadOnly(ctx context.Context, qubeName string, buildArgs func() []string) (string, error) {
	if !ValidQubeName(qubeName) {
		return "", &ErrInvalidQubeName{Name: qubeName}
	}
	if t.WorkDir == "" {
		return "", fmt.Errorf("terraform executor: WorkDir is not configured")
	}

	if !t.mu.TryLock() {
		return "", ErrExecutorBusy
	}
	defer t.mu.Unlock()

	if dl, ok := ctx.Deadline(); !ok || time.Until(dl) > t.Timeout {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t.Timeout)
		defer cancel()
	}

	var env []string
	if t.EnvFunc != nil {
		var err error
		if env, err = t.EnvFunc(ctx); err != nil {
			return "", fmt.Errorf("terraform executor: resolve credentials: %w", err)
		}
	}
	return t.runner.run(ctx, t.WorkDir, t.Binary, buildArgs(), env)
}

// ErrExecutorBusy reports that terraform is mid-run and a read-only query
// declined to wait. Distinct from a failure: nothing is wrong, the answer is
// simply not available right now.
var ErrExecutorBusy = errors.New("terraform executor is busy with another run")

func (t *TerraformExecutor) exec(ctx context.Context, qubeName string, requireKey bool, buildArgs func() []string) (string, error) {
	if !ValidQubeName(qubeName) {
		return "", &ErrInvalidQubeName{Name: qubeName}
	}
	if t.WorkDir == "" {
		return "", fmt.Errorf("terraform executor: WorkDir is not configured")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// The key assertion and the render are deliberately independent. Tying the
	// assertion to GeneratedVarFile being set would let a misconfiguration
	// (Snapshot supplied, GeneratedVarFile forgotten) silently disable the very
	// check that stops us reporting success for a qube terraform never saw.
	if t.Snapshot != nil {
		qubes, err := t.Snapshot(ctx)
		if err != nil {
			return "", fmt.Errorf("terraform executor: snapshot qubes: %w", err)
		}
		if requireKey {
			if _, ok := qubes[qubeName]; !ok {
				return "", fmt.Errorf("%w: %q", ErrTargetNotInConfig, qubeName)
			}
		}
		if err := t.renderQubes(qubes); err != nil {
			return "", fmt.Errorf("terraform executor: %w", err)
		}
	}

	// Only impose our own deadline when the caller has not already set a tighter
	// one. Callers must NOT hand us an HTTP request context: cancelling mid-apply
	// signals terraform away before it persists state, which orphans real VMs.
	// The job runner owns a lifetime context derived from Background for this
	// reason; the timeout here is a backstop for direct use.
	if dl, ok := ctx.Deadline(); !ok || time.Until(dl) > t.Timeout {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, t.Timeout)
		defer cancel()
	}

	var env []string
	if t.EnvFunc != nil {
		var err error
		if env, err = t.EnvFunc(ctx); err != nil {
			// Fail rather than fall through to whatever the parent environment
			// holds: silently running with stale or absent credentials is how a
			// deployment ends up authenticating as something unexpected.
			return "", fmt.Errorf("terraform executor: resolve credentials: %w", err)
		}
	}

	return t.runner.run(ctx, t.WorkDir, t.Binary, buildArgs(), env)
}

// Suspend destroys the qube's compute instance while keeping the data disk,
// matching `make tf-suspend QUBE=<name>`.
func (t *TerraformExecutor) Suspend(ctx context.Context, qubeName string) error {
	_, err := t.exec(ctx, qubeName, true, func() []string {
		args := []string{"destroy", "-auto-approve", "-input=false"}
		args = append(args, t.varFileArgs()...)
		args = append(args, "-target="+computeTarget(qubeName))
		return args
	})
	return err
}

// Resume rebuilds the qube's compute instance and re-attaches the data disk,
// matching `make tf-resume QUBE=<name>`.
func (t *TerraformExecutor) Resume(ctx context.Context, qubeName string) error {
	_, err := t.exec(ctx, qubeName, true, func() []string {
		args := []string{"apply", "-auto-approve", "-input=false"}
		args = append(args, t.varFileArgs()...)
		args = append(args, "-target="+computeTarget(qubeName))
		return args
	})
	return err
}

// Provision creates the qube for the first time. Without a -target this applies
// the full configuration; the qube must already be present in the tfvars/state
// for terraform to know about it.
func (t *TerraformExecutor) Provision(ctx context.Context, qubeName string) error {
	_, err := t.exec(ctx, qubeName, true, func() []string {
		args := []string{"apply", "-auto-approve", "-input=false"}
		args = append(args, t.varFileArgs()...)
		// Provision both the compute and its data-disk/holder for this qube.
		args = append(args, "-target=module.remote_qubes["+strconvQuote(qubeName)+"]")
		return args
	})
	return err
}

// Destroy tears the qube down including its data disk (whole module instance).
func (t *TerraformExecutor) Destroy(ctx context.Context, qubeName string) error {
	_, err := t.exec(ctx, qubeName, true, func() []string {
		args := []string{"destroy", "-auto-approve", "-input=false"}
		args = append(args, t.varFileArgs()...)
		args = append(args, "-target=module.remote_qubes["+strconvQuote(qubeName)+"]")
		return args
	})
	return err
}

// Status reads terraform output and extracts this qube's status field. It runs
// `terraform output -json remote_qubes` and parses the "status" for the qube.
func (t *TerraformExecutor) Status(ctx context.Context, qubeName string) (string, error) {
	// requireKey=false: reading status for a qube terraform has never heard of
	// is a legitimate query, not an error — it simply has no infrastructure yet.
	out, err := t.exec(ctx, qubeName, false, func() []string {
		return []string{"output", "-json", "remote_qubes"}
	})
	if err != nil {
		return "", err
	}
	return parseQubeStatus(out, qubeName)
}

// Address reads terraform output and extracts this qube's IP address.
//
// Deliberately NOT part of the Executor interface: every other method there
// CHANGES infrastructure, and adding a read-only accessor would oblige the noop
// and fake executors to answer a question they have no way to answer. Consumers
// type-assert for it (see service.AgentAddressReader) and degrade when it is
// absent.
func (t *TerraformExecutor) Address(ctx context.Context, qubeName string) (string, error) {
	out, err := t.execReadOnly(ctx, qubeName, func() []string {
		return []string{"output", "-json", "remote_qubes"}
	})
	if err != nil {
		return "", err
	}
	return parseQubeAddress(out, qubeName)
}

// strconvQuote quotes a string for a terraform module index. We use %q via
// fmt to get the same escaping terraform expects for a quoted map key.
func strconvQuote(s string) string {
	return fmt.Sprintf("%q", s)
}
