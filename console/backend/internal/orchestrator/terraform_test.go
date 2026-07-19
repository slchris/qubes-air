package orchestrator

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// recordingRunner captures the argv and workdir a TerraformExecutor would run,
// without spawning terraform. It returns a canned stdout.
type recordingRunner struct {
	calls   [][]string
	env     []string
	workDir string
	stdout  string
	err     error
}

func (r *recordingRunner) run(_ context.Context, workDir, name string, args, env []string) (string, error) {
	r.workDir = workDir
	r.env = env
	// Store binary + args together so tests can assert the full command line.
	r.calls = append(r.calls, append([]string{name}, args...))
	return r.stdout, r.err
}

func newTestExecutor(r *recordingRunner, opts ...TerraformOption) *TerraformExecutor {
	t := NewTerraformExecutor("/tf", opts...)
	t.runner = r
	return t
}

func TestValidQubeName(t *testing.T) {
	valid := []string{
		"dev-work", "web01", "a", "gpu_node.1", "A-B_c.9",
		strings.Repeat("a", 64),
	}
	for _, name := range valid {
		if !ValidQubeName(name) {
			t.Errorf("expected %q to be valid", name)
		}
	}

	// These are the security-critical rejections: anything that could inject a
	// shell metacharacter, a terraform flag, an address separator, or whitespace.
	invalid := []string{
		"",                      // empty
		"a b",                   // space
		"a;rm -rf /",            // command separator
		"$(whoami)",             // command substitution
		"`id`",                  // backtick substitution
		"a&&b",                  // logical operator
		"a|b",                   // pipe
		"a>b",                   // redirect
		"-target=evil",          // leading dash -> looks like a flag
		"--var=x",               // flag
		"a\"b",                  // quote
		"a'b",                   // quote
		"a b\nc",                // newline
		"a\tb",                  // tab
		"a/b",                   // path separator
		"a\\b",                  // backslash
		"a[0]",                  // brackets (terraform address syntax)
		"a{b}",                  // braces
		"a\x00b",                // null byte
		"名字",                    // non-ascii
		strings.Repeat("a", 65), // too long
	}
	for _, name := range invalid {
		if ValidQubeName(name) {
			t.Errorf("expected %q to be REJECTED", name)
		}
	}
}

func TestSuspendRejectsMaliciousName(t *testing.T) {
	r := &recordingRunner{}
	exec := newTestExecutor(r)

	malicious := []string{"a;rm -rf /", "$(whoami)", "a b", "-target=x", "web[0]", ""}
	for _, name := range malicious {
		err := exec.Suspend(context.Background(), name)
		var inv *ErrInvalidQubeName
		if !errors.As(err, &inv) {
			t.Errorf("Suspend(%q): expected ErrInvalidQubeName, got %v", name, err)
		}
	}
	if len(r.calls) != 0 {
		t.Fatalf("terraform must not be invoked for malicious names, but got calls: %v", r.calls)
	}
}

func TestResumeRejectsMaliciousName(t *testing.T) {
	r := &recordingRunner{}
	exec := newTestExecutor(r)
	for _, name := range []string{"a;b", "$(x)", "web 1"} {
		if err := exec.Resume(context.Background(), name); err == nil {
			t.Errorf("Resume(%q): expected error, got nil", name)
		}
	}
	if len(r.calls) != 0 {
		t.Fatalf("no terraform call expected, got %v", r.calls)
	}
}

func TestAllMethodsRejectMaliciousName(t *testing.T) {
	r := &recordingRunner{stdout: "{}"}
	exec := newTestExecutor(r)
	bad := "a;rm -rf /"
	ctx := context.Background()

	if err := exec.Provision(ctx, bad); err == nil {
		t.Error("Provision should reject malicious name")
	}
	if err := exec.Destroy(ctx, bad); err == nil {
		t.Error("Destroy should reject malicious name")
	}
	if _, err := exec.Status(ctx, bad); err == nil {
		t.Error("Status should reject malicious name")
	}
	if len(r.calls) != 0 {
		t.Fatalf("no terraform call expected for malicious names, got %v", r.calls)
	}
}

func TestSuspendCommandConstruction(t *testing.T) {
	r := &recordingRunner{}
	exec := newTestExecutor(r, WithVarFile("environments/dev.tfvars"))

	if err := exec.Suspend(context.Background(), "dev-work"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(r.calls))
	}
	if r.workDir != "/tf" {
		t.Errorf("workdir = %q, want /tf", r.workDir)
	}
	cmd := r.calls[0]
	// terraform destroy -auto-approve -input=false -var-file=... -target=...
	assertContains(t, cmd, "terraform")
	assertContains(t, cmd, "destroy")
	assertContains(t, cmd, "-auto-approve")
	assertContains(t, cmd, "-var-file=environments/dev.tfvars")
	wantTarget := `-target=module.remote_qubes["dev-work"].module.proxmox[0].proxmox_virtual_environment_vm.compute`
	assertContains(t, cmd, wantTarget)
}

func TestResumeCommandConstruction(t *testing.T) {
	r := &recordingRunner{}
	exec := newTestExecutor(r, WithVarFile("environments/dev.tfvars"))

	if err := exec.Resume(context.Background(), "dev-work"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cmd := r.calls[0]
	assertContains(t, cmd, "apply")
	assertContains(t, cmd, "-auto-approve")
	wantTarget := `-target=module.remote_qubes["dev-work"].module.proxmox[0].proxmox_virtual_environment_vm.compute`
	assertContains(t, cmd, wantTarget)
	// Must NOT be a destroy.
	for _, a := range cmd {
		if a == "destroy" {
			t.Fatalf("resume must not run destroy: %v", cmd)
		}
	}
}

func TestNoVarFileOmitsFlag(t *testing.T) {
	r := &recordingRunner{}
	exec := newTestExecutor(r) // no var file

	if err := exec.Resume(context.Background(), "web01"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, a := range r.calls[0] {
		if strings.HasPrefix(a, "-var-file=") {
			t.Fatalf("no -var-file expected when unset, got %v", r.calls[0])
		}
	}
}

func TestStatusParsesTerraformOutput(t *testing.T) {
	out := `{
      "dev-work": {"status": "suspended", "ip_address": "", "compute_running": false, "data_disk_id": "disk-1"},
      "web01":    {"status": "running",   "ip_address": "10.0.0.5", "compute_running": true, "data_disk_id": "disk-2"}
    }`
	r := &recordingRunner{stdout: out}
	exec := newTestExecutor(r)

	got, err := exec.Status(context.Background(), "dev-work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "suspended" {
		t.Errorf("status = %q, want suspended", got)
	}

	// The status command reads the remote_qubes output as JSON.
	assertContains(t, r.calls[0], "output")
	assertContains(t, r.calls[0], "-json")
	assertContains(t, r.calls[0], "remote_qubes")
}

func TestStatusUnknownQube(t *testing.T) {
	r := &recordingRunner{stdout: `{"web01": {"status": "running"}}`}
	exec := newTestExecutor(r)
	if _, err := exec.Status(context.Background(), "ghost"); err == nil {
		t.Error("expected error for qube missing from output")
	}
}

func TestExecPropagatesRunnerError(t *testing.T) {
	r := &recordingRunner{err: errors.New("boom")}
	exec := newTestExecutor(r)
	if err := exec.Resume(context.Background(), "web01"); err == nil {
		t.Error("expected runner error to propagate")
	}
}

func TestMissingWorkDir(t *testing.T) {
	exec := NewTerraformExecutor("")
	exec.runner = &recordingRunner{}
	if err := exec.Resume(context.Background(), "web01"); err == nil {
		t.Error("expected error when WorkDir is empty")
	}
}

func assertContains(t *testing.T, argv []string, want string) {
	t.Helper()
	if slices.Contains(argv, want) {
		return
	}
	t.Errorf("argv %v does not contain %q", argv, want)
}

// TestReadOnlyQueryDoesNotRenderVarFile — `terraform output` reads state and
// ignores variables, so rendering the generated var-file would be a pure side
// effect. It matters because the agent-health monitor calls Address(): a
// READ-ONLY health probe was rewriting the file that defines which qubes should
// exist.
func TestReadOnlyQueryDoesNotRenderVarFile(t *testing.T) {
	dir := t.TempDir()
	generated := filepath.Join(dir, "generated", "qubes.tfvars.json")

	rendered := 0
	ex := &TerraformExecutor{
		WorkDir: dir, Binary: "terraform", GeneratedVarFile: "generated/qubes.tfvars.json",
		Snapshot: func(context.Context) (map[string]any, error) {
			rendered++
			return map[string]any{"dev-work": map[string]any{}}, nil
		},
	}
	ex.runner = &recordingRunner{stdout: `{"dev-work":{"ip_address":"10.0.0.5"}}`}

	_, _ = ex.Address(context.Background(), "dev-work")

	assert.Zero(t, rendered, "a read-only query must not invoke the snapshot")
	assert.NoFileExists(t, generated, "a read-only query must not write the generated var-file")
}

// TestReadOnlyQueryDoesNotBlockOnAnApply — exec holds the executor mutex for the
// entire terraform subprocess, up to 15 minutes, and a context deadline cannot
// interrupt a mutex acquisition. The health monitor iterates qubes serially, so
// one address-less qube would stall the whole sweep for the length of an
// in-flight apply: no qube probed, no dead agent noticed.
func TestReadOnlyQueryDoesNotBlockOnAnApply(t *testing.T) {
	ex := &TerraformExecutor{WorkDir: t.TempDir(), Binary: "terraform"}
	ex.runner = &recordingRunner{}

	ex.mu.Lock() // stand in for an apply in progress
	defer ex.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		_, err := ex.Address(context.Background(), "dev-work")
		done <- err
	}()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, ErrExecutorBusy, "a busy executor must be reported, not waited on")
	case <-time.After(5 * time.Second):
		t.Fatal("Address blocked on the executor mutex; one qube would stall the whole health sweep")
	}
}
