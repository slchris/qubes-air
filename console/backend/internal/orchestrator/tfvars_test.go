package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tfExecutorIn builds an executor rooted at a real temp dir so the generated
// var-file can actually be written and inspected.
func tfExecutorIn(t *testing.T, r *recordingRunner, opts ...TerraformOption) *TerraformExecutor {
	t.Helper()
	ex := NewTerraformExecutor(t.TempDir(), opts...)
	ex.runner = r
	return ex
}

// lastCall returns the argv of the most recent run, or nil if none happened.
func lastCall(r *recordingRunner) []string {
	if len(r.calls) == 0 {
		return nil
	}
	return r.calls[len(r.calls)-1]
}

// TestVarFileOrdering pins the one thing that makes the whole two-file design
// safe: the console-generated file must be passed AFTER the operator's base
// file. terraform resolves -var-file left to right and the last file to define
// a variable wins, so reversing these two would let a hand-written tfvars
// silently override the qube set the console believes in.
func TestVarFileOrdering(t *testing.T) {
	rr := &recordingRunner{}
	ex := tfExecutorIn(t, rr,
		WithVarFile("environments/infra.tfvars"),
		WithGeneratedVarFile("generated/qubes.tfvars.json"),
		WithQubeSnapshot(func(context.Context) (map[string]any, error) {
			return map[string]any{"dev-work": map[string]any{"zone": "proxmox-zone"}}, nil
		}),
	)

	if err := ex.Resume(context.Background(), "dev-work"); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	var base, gen = -1, -1
	for i, a := range lastCall(rr) {
		switch a {
		case "-var-file=environments/infra.tfvars":
			base = i
		case "-var-file=generated/qubes.tfvars.json":
			gen = i
		}
	}
	if base < 0 || gen < 0 {
		t.Fatalf("expected both var-files in argv, got %v", lastCall(rr))
	}
	if gen < base {
		t.Errorf("generated var-file must come AFTER base so it wins; got base=%d gen=%d in %v", base, gen, lastCall(rr))
	}
}

// TestRequireKeyRejectsUnknownQube is the guard against terraform's most
// dangerous behaviour here: a -target that matches nothing exits 0 with "No
// changes". Without this check the caller reads that as success and records a
// qube as running when no infrastructure was ever created.
func TestRequireKeyRejectsUnknownQube(t *testing.T) {
	rr := &recordingRunner{}
	ex := tfExecutorIn(t, rr,
		WithGeneratedVarFile("generated/qubes.tfvars.json"),
		WithQubeSnapshot(func(context.Context) (map[string]any, error) {
			return map[string]any{"known": map[string]any{}}, nil
		}),
	)

	for _, op := range []struct {
		name string
		call func() error
	}{
		{"Resume", func() error { return ex.Resume(context.Background(), "ghost") }},
		{"Suspend", func() error { return ex.Suspend(context.Background(), "ghost") }},
		{"Provision", func() error { return ex.Provision(context.Background(), "ghost") }},
		{"Destroy", func() error { return ex.Destroy(context.Background(), "ghost") }},
	} {
		rr.calls = nil
		err := op.call()
		if !errors.Is(err, ErrTargetNotInConfig) {
			t.Errorf("%s on unknown qube: want ErrTargetNotInConfig, got %v", op.name, err)
		}
		if lastCall(rr) != nil {
			t.Errorf("%s must not invoke terraform for an unknown qube, ran: %v", op.name, lastCall(rr))
		}
	}
}

// TestStatusDoesNotRequireKey — asking the status of a qube terraform has never
// heard of is a legitimate query, not an error.
func TestStatusDoesNotRequireKey(t *testing.T) {
	rr := &recordingRunner{stdout: `{"other":{"status":"running"}}`}
	ex := tfExecutorIn(t, rr,
		WithGeneratedVarFile("generated/qubes.tfvars.json"),
		WithQubeSnapshot(func(context.Context) (map[string]any, error) {
			return map[string]any{"other": map[string]any{}}, nil
		}),
	)
	if _, err := ex.Status(context.Background(), "ghost"); errors.Is(err, ErrTargetNotInConfig) {
		t.Error("Status must not require the qube to be in the config")
	}
	if lastCall(rr) == nil {
		t.Error("Status should still have invoked terraform")
	}
}

// TestRenderEmptyMapIsExplicit guards a subtle terraform behaviour: a variable
// ABSENT from the last -var-file falls through to the earlier file rather than
// being reset. So "no qubes" must serialize as an explicit empty map, otherwise
// a console with zero qubes would silently inherit the operator's stale set.
func TestRenderEmptyMapIsExplicit(t *testing.T) {
	// stdout must be parseable: we drive the render via Status, whose own
	// output parsing would otherwise fail before we get to inspect the file.
	rr := &recordingRunner{stdout: `{}`}
	ex := tfExecutorIn(t, rr,
		WithGeneratedVarFile("generated/qubes.tfvars.json"),
		WithQubeSnapshot(func(context.Context) (map[string]any, error) { return nil, nil }),
	)
	// The Status result is irrelevant here — we only need it to trigger a
	// render. "not found in terraform output" is expected for an empty map.
	_, _ = ex.Status(context.Background(), "anything")

	blob, err := os.ReadFile(filepath.Join(ex.WorkDir, "generated/qubes.tfvars.json"))
	if err != nil {
		t.Fatalf("read generated file: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(blob, &doc); err != nil {
		t.Fatalf("generated file is not valid JSON: %v\n%s", err, blob)
	}
	qubes, ok := doc["remote_qubes"]
	if !ok {
		t.Fatalf("remote_qubes key must be present even when empty, got: %s", blob)
	}
	m, ok := qubes.(map[string]any)
	if !ok || len(m) != 0 {
		t.Errorf("want an explicit empty map, got %#v", qubes)
	}
}

// TestRenderIsAtomic — no partial file is ever visible, and no temp files are
// left behind. terraform reading a half-written var-file would see a truncated
// qube set.
func TestRenderIsAtomic(t *testing.T) {
	rr := &recordingRunner{stdout: `{"a":{"status":"running"}}`}
	ex := tfExecutorIn(t, rr,
		WithGeneratedVarFile("generated/qubes.tfvars.json"),
		WithQubeSnapshot(func(context.Context) (map[string]any, error) {
			return map[string]any{
				"a": map[string]any{"zone": "proxmox-zone", "template_vm_id": 901},
				"b": map[string]any{"zone": "proxmox-zone", "compute_running": false},
			}, nil
		}),
	)
	if _, err := ex.Status(context.Background(), "a"); err != nil {
		t.Fatalf("Status: %v", err)
	}

	dir := filepath.Join(ex.WorkDir, "generated")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".qubes-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}

	blob, err := os.ReadFile(filepath.Join(dir, "qubes.tfvars.json"))
	if err != nil {
		t.Fatalf("read generated: %v", err)
	}
	var doc struct {
		RemoteQubes map[string]map[string]any `json:"remote_qubes"`
	}
	if err := json.Unmarshal(blob, &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(doc.RemoteQubes) != 2 {
		t.Errorf("want 2 qubes, got %d: %s", len(doc.RemoteQubes), blob)
	}
	if got := doc.RemoteQubes["a"]["template_vm_id"]; got != float64(901) {
		t.Errorf("template_vm_id round-trip: got %#v", got)
	}
	// compute_running=false must survive as false, not be dropped as a zero value.
	if got, ok := doc.RemoteQubes["b"]["compute_running"]; !ok || got != false {
		t.Errorf("compute_running=false must be emitted, got %#v (present=%v)", got, ok)
	}
}

// TestSnapshotErrorDoesNotRunTerraform — if we cannot determine the desired
// qube set we must not run terraform against a stale generated file.
func TestSnapshotErrorDoesNotRunTerraform(t *testing.T) {
	rr := &recordingRunner{}
	sentinel := errors.New("db down")
	ex := tfExecutorIn(t, rr,
		WithGeneratedVarFile("generated/qubes.tfvars.json"),
		WithQubeSnapshot(func(context.Context) (map[string]any, error) { return nil, sentinel }),
	)
	err := ex.Resume(context.Background(), "dev-work")
	if !errors.Is(err, sentinel) {
		t.Errorf("want the snapshot error to propagate, got %v", err)
	}
	if lastCall(rr) != nil {
		t.Errorf("terraform must not run when the snapshot failed, ran: %v", lastCall(rr))
	}
}

// TestKeyAssertionIndependentOfRendering — a snapshot without a generated
// var-file is a misconfiguration, but it must not silently disable the
// unknown-qube guard. That guard is the only thing standing between an
// unresolvable -target (which terraform reports as success) and the console
// recording a qube as running that was never created.
func TestKeyAssertionIndependentOfRendering(t *testing.T) {
	rr := &recordingRunner{}
	ex := tfExecutorIn(t, rr, // note: no WithGeneratedVarFile
		WithQubeSnapshot(func(context.Context) (map[string]any, error) {
			return map[string]any{"known": map[string]any{}}, nil
		}),
	)
	if err := ex.Resume(context.Background(), "ghost"); !errors.Is(err, ErrTargetNotInConfig) {
		t.Errorf("guard must hold without a generated var-file, got %v", err)
	}
	if lastCall(rr) != nil {
		t.Errorf("terraform must not run, ran: %v", lastCall(rr))
	}
}

// TestEnvFuncSuppliesCredentials — credentials reach terraform through the
// subprocess environment. They must NOT travel as terraform variables: a
// variable's value is written into state in plaintext, and long-lived
// credentials are forbidden from entering state.
func TestEnvFuncSuppliesCredentials(t *testing.T) {
	rr := &recordingRunner{}
	ex := tfExecutorIn(t, rr,
		WithQubeSnapshot(func(context.Context) (map[string]any, error) {
			return map[string]any{"dev-work": map[string]any{}}, nil
		}),
		WithEnvFunc(func(context.Context) ([]string, error) {
			return []string{"PROXMOX_VE_ENDPOINT=https://pve", "PROXMOX_VE_API_TOKEN=u@pve!t=s"}, nil
		}),
	)
	if err := ex.Resume(context.Background(), "dev-work"); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	joined := strings.Join(rr.env, " ")
	if !strings.Contains(joined, "PROXMOX_VE_API_TOKEN=") {
		t.Errorf("credentials must reach the process environment, got %v", rr.env)
	}
	// The same secret must not appear anywhere on the command line, where it
	// would show up in process listings and in error messages.
	for _, a := range lastCall(rr) {
		if strings.Contains(a, "u@pve!t=s") {
			t.Errorf("secret leaked into argv: %q", a)
		}
	}
}

// TestEnvFuncErrorAbortsRun — if credentials cannot be resolved we must not run
// terraform anyway, which would authenticate as whatever the parent environment
// happens to hold.
func TestEnvFuncErrorAbortsRun(t *testing.T) {
	rr := &recordingRunner{}
	sentinel := errors.New("vault sealed")
	ex := tfExecutorIn(t, rr,
		WithQubeSnapshot(func(context.Context) (map[string]any, error) {
			return map[string]any{"dev-work": map[string]any{}}, nil
		}),
		WithEnvFunc(func(context.Context) ([]string, error) { return nil, sentinel }),
	)
	if err := ex.Resume(context.Background(), "dev-work"); !errors.Is(err, sentinel) {
		t.Errorf("want the resolver error to propagate, got %v", err)
	}
	if lastCall(rr) != nil {
		t.Errorf("terraform must not run without credentials, ran: %v", lastCall(rr))
	}
}
