package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestBuiltinCannotBeShadowedByFile is the security property of the builtin
// layer.
//
// ServiceDir is operator-writable, and on a host this project assumes is
// compromisable it is attacker-writable too. If a script there could answer
// qubesair.CompleteRenewal, dropping one in would intercept renewal: the
// console would get a plausible reply, the real certificate would never rotate,
// and the fleet would go dark on schedule with every health signal green until
// the day it did.
func TestBuiltinCannotBeShadowedByFile(t *testing.T) {
	dir := serviceDir(t, map[string]string{
		ServiceBeginRenewal:    "#!/bin/sh\necho SHADOWED\n",
		ServiceCompleteRenewal: "#!/bin/sh\necho SHADOWED\n",
	})
	inv := invokerOver(dir)
	for _, name := range []string{ServiceBeginRenewal, ServiceCompleteRenewal} {
		if err := inv.RegisterBuiltin(name, func(context.Context, string, []byte) ([]byte, error) {
			return []byte("BUILTIN"), nil
		}); err != nil {
			t.Fatalf("RegisterBuiltin %s: %v", name, err)
		}
	}

	for _, name := range []string{ServiceBeginRenewal, ServiceCompleteRenewal} {
		out, err := inv.Invoke(context.Background(), "console", name, nil)
		if err != nil {
			t.Fatalf("Invoke %s: %v", name, err)
		}
		if got := strings.TrimSpace(string(out)); got != "BUILTIN" {
			t.Errorf("%s was served by %q; a file in the service directory overrode certificate renewal", name, got)
		}
	}
}

// TestBuiltinShadowingViaArgumentForm closes the variant: dispatching
// "name+argument" on the full string would send it down the file path, and the
// shadowing script would answer after all.
func TestBuiltinShadowingViaArgumentForm(t *testing.T) {
	dir := serviceDir(t, map[string]string{
		ServiceCompleteRenewal: "#!/bin/sh\necho SHADOWED\n",
	})
	inv := invokerOver(dir)
	if err := inv.RegisterBuiltin(ServiceCompleteRenewal, func(context.Context, string, []byte) ([]byte, error) {
		return []byte("BUILTIN"), nil
	}); err != nil {
		t.Fatal(err)
	}

	out, err := inv.Invoke(context.Background(), "console", ServiceCompleteRenewal+"+anything", nil)
	if !errors.Is(err, ErrBuiltinTakesNoArgument) {
		t.Fatalf("want ErrBuiltinTakesNoArgument, got out=%q err=%v", out, err)
	}
	if strings.Contains(string(out), "SHADOWED") {
		t.Error("the argument form reached the file in the service directory")
	}
}

// TestBuiltinIgnoresAllowlist — the allowlist guards against scripts appearing
// by accident, which a builtin cannot do. What it would add is a way to disable
// renewal by forgetting to list it, and the only symptom of that would be
// certificates quietly not rotating.
func TestBuiltinIgnoresAllowlist(t *testing.T) {
	inv := invokerOver(t.TempDir(), "qubesair.Ping") // renewal deliberately absent
	if err := inv.RegisterBuiltin(ServiceBeginRenewal, func(context.Context, string, []byte) ([]byte, error) {
		return []byte("BUILTIN"), nil
	}); err != nil {
		t.Fatal(err)
	}

	out, err := inv.Invoke(context.Background(), "console", ServiceBeginRenewal, nil)
	if err != nil {
		t.Fatalf("an allowlist that omits renewal must not disable it: %v", err)
	}
	if string(out) != "BUILTIN" {
		t.Errorf("got %q", out)
	}
}

// TestBuiltinReceivesRequestBody — a builtin sees what a script would.
func TestBuiltinReceivesRequestBody(t *testing.T) {
	inv := invokerOver(t.TempDir())
	var gotTarget string
	var gotBody []byte
	if err := inv.RegisterBuiltin("qubesair.Echo", func(_ context.Context, target string, in []byte) ([]byte, error) {
		gotTarget, gotBody = target, in
		return in, nil
	}); err != nil {
		t.Fatal(err)
	}

	out, err := inv.Invoke(context.Background(), "console-probe", "qubesair.Echo", []byte(`{"nonce":"abc"}`))
	if err != nil {
		t.Fatal(err)
	}
	if gotTarget != "console-probe" {
		t.Errorf("target %q", gotTarget)
	}
	if string(gotBody) != `{"nonce":"abc"}` || string(out) != `{"nonce":"abc"}` {
		t.Errorf("body %q, out %q", gotBody, out)
	}
}

// TestBuiltinErrorsSurface — a builtin's failure must reach the caller, since
// the console records renewal failures against the agent-health fields.
func TestBuiltinErrorsSurface(t *testing.T) {
	inv := invokerOver(t.TempDir())
	sentinel := errors.New("no pending renewal")
	if err := inv.RegisterBuiltin("qubesair.Fail", func(context.Context, string, []byte) ([]byte, error) {
		return nil, sentinel
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := inv.Invoke(context.Background(), "console", "qubesair.Fail", nil); !errors.Is(err, sentinel) {
		t.Fatalf("want the builtin's own error, got %v", err)
	}
}

// TestRegisterBuiltinRejectsDuplicates — registering twice would silently
// decide which implementation of certificate renewal runs.
func TestRegisterBuiltinRejectsDuplicates(t *testing.T) {
	inv := invokerOver(t.TempDir())
	fn := func(context.Context, string, []byte) ([]byte, error) { return nil, nil }
	if err := inv.RegisterBuiltin(ServiceBeginRenewal, fn); err != nil {
		t.Fatal(err)
	}
	if err := inv.RegisterBuiltin(ServiceBeginRenewal, fn); !errors.Is(err, ErrBuiltinExists) {
		t.Fatalf("want ErrBuiltinExists, got %v", err)
	}
}

// TestRegisterBuiltinRejectsBadNames — a builtin registered under a name the
// invoker would reject on the wire is unreachable, so it is refused up front
// rather than discovered when a renewal fails.
func TestRegisterBuiltinRejectsBadNames(t *testing.T) {
	inv := invokerOver(t.TempDir())
	fn := func(context.Context, string, []byte) ([]byte, error) { return nil, nil }
	for _, name := range []string{"", "../escape", "a/b", "with+arg", "has space"} {
		if err := inv.RegisterBuiltin(name, fn); err == nil {
			t.Errorf("RegisterBuiltin accepted %q", name)
		}
	}
	if err := inv.RegisterBuiltin("qubesair.Ok", nil); err == nil {
		t.Error("RegisterBuiltin accepted a nil implementation")
	}
}

// TestRenewalBuiltinsRegistered checks the pair the agent actually ships, and
// that IsBuiltin reports them so startup does not warn about missing scripts
// that must never exist.
func TestRenewalBuiltinsRegistered(t *testing.T) {
	id, _, _ := installedIdentity(t, testAgentCN)
	inv := invokerOver(t.TempDir())
	if err := NewRenewalService(id, 0).RegisterBuiltins(inv); err != nil {
		t.Fatalf("RegisterBuiltins: %v", err)
	}
	for _, name := range []string{ServiceBeginRenewal, ServiceCompleteRenewal} {
		if !inv.IsBuiltin(name) {
			t.Errorf("%s is not registered as a builtin", name)
		}
	}
	if inv.IsBuiltin("qubesair.Ping") {
		t.Error("qubesair.Ping must stay a script; it is not part of the agent's TLS state")
	}
}
