package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeQrexec records the call and returns a canned reply.
type fakeQrexec struct {
	target, service string
	input           string
	out             string
	err             error
	calls           int
}

func (f *fakeQrexec) Call(_ context.Context, target, service string, input []byte) ([]byte, error) {
	f.calls++
	f.target, f.service, f.input = target, service, string(input)
	return []byte(f.out), f.err
}

func TestRegisterSendsOneLineToDom0(t *testing.T) {
	f := &fakeQrexec{out: "register: DONE — remote-dev-1 -> dev-1"}
	r := NewRemoteVMRegistrar(f, true)

	if err := r.Register(context.Background(), "dev-1"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if f.target != "dom0" || f.service != "qubesair.RegisterRemoteVM" {
		t.Errorf("called %s/%s, want dom0/qubesair.RegisterRemoteVM", f.target, f.service)
	}
	// The addressing shell is remote-<qube>; the REMOTE name is the bare qube
	// name, which is also its hostname and agent certificate subject.
	if f.input != "register remote-dev-1 dev-1\n" {
		t.Errorf("input = %q", f.input)
	}
}

func TestDeregisterSendsName(t *testing.T) {
	f := &fakeQrexec{out: "OK RETIRED remote-dev-1 relay=verified"}
	r := NewRemoteVMRegistrar(f, true)

	if err := r.Deregister(context.Background(), "dev-1"); err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	if f.input != "deregister remote-dev-1\n" {
		t.Errorf("input = %q", f.input)
	}
}

func TestDeregisterRequiresCurrentRetirementAcknowledgement(t *testing.T) {
	for _, body := range []string{"", "OK", "deregister: DONE", "OK RETIRED remote-other relay=verified", "OK RETIRED remote-dev-1 relay=unknown"} {
		r := NewRemoteVMRegistrar(&fakeQrexec{out: body}, true)
		if err := r.Deregister(context.Background(), "dev-1"); err == nil {
			t.Errorf("unverified retirement response %q was accepted", body)
		}
	}
	r := NewRemoteVMRegistrar(&fakeQrexec{out: "OK RETIRED remote-dev-1 relay=disabled"}, true)
	if err := r.Deregister(context.Background(), "dev-1"); err != nil {
		t.Fatalf("a deployment without CSR endpoint caching should acknowledge that explicitly: %v", err)
	}
}

// The service reports refusals on STDOUT and exits zero, because qrexec does
// not relay stderr. Treating a zero exit as success would record a qube as
// addressable when dom0 never registered it.
func TestRefusalOnStdoutIsAnError(t *testing.T) {
	for _, body := range []string{
		"register: REFUSED — 'sys-net' is not a remote-* qube name",
		"register: FAILED to create RemoteVM:\n  some qvm-create error",
	} {
		f := &fakeQrexec{out: body}
		r := NewRemoteVMRegistrar(f, true)
		err := r.Register(context.Background(), "dev-1")
		if err == nil {
			t.Errorf("body %q was accepted as success", body)
			continue
		}
		if !strings.Contains(err.Error(), "refused by dom0") {
			t.Errorf("error did not name the cause: %v", err)
		}
	}
}

func TestTransportErrorPropagates(t *testing.T) {
	f := &fakeQrexec{err: errors.New("qrexec call failed: Request refused")}
	r := NewRemoteVMRegistrar(f, true)
	if err := r.Register(context.Background(), "dev-1"); err == nil {
		t.Fatal("a failed qrexec call was reported as success")
	}
}

// Disabled is the default until the dom0 policy exists. It must not call out,
// and must not pretend to have succeeded either.
func TestDisabledDoesNotCall(t *testing.T) {
	f := &fakeQrexec{}
	r := NewRemoteVMRegistrar(f, false)

	if r.Enabled() {
		t.Error("Enabled() true with enabled=false")
	}
	if err := r.Register(context.Background(), "x"); err == nil {
		t.Error("Register on a disabled registrar reported success")
	}
	if f.calls != 0 {
		t.Errorf("made %d qrexec calls while disabled", f.calls)
	}
}

func TestNilCallerIsDisabled(t *testing.T) {
	r := NewRemoteVMRegistrar(nil, true)
	if r.Enabled() {
		t.Error("a registrar with no qrexec caller reported itself enabled")
	}
}

// A name with whitespace would inject a second field into the request line and
// change which qube dom0 acts on.
func TestArgumentsWithWhitespaceAreRejected(t *testing.T) {
	f := &fakeQrexec{out: "ok"}
	r := NewRemoteVMRegistrar(f, true)

	for _, bad := range []string{"", "dev 1 sys-net", "dev\t1", "dev\n1"} {
		if err := r.Register(context.Background(), bad); err == nil {
			t.Errorf("accepted %q as a qube name", bad)
		}
	}
	if f.calls != 0 {
		t.Errorf("a rejected name still reached qrexec (%d calls)", f.calls)
	}
}

// The quiet variants exist so a registration gap cannot fail a job that already
// produced a working machine.
func TestQuietVariantsDoNotPanicOrCallWhenDisabled(t *testing.T) {
	f := &fakeQrexec{}
	r := NewRemoteVMRegistrar(f, false)
	r.RegisterQuietly(context.Background(), "dev-1")
	r.DeregisterQuietly(context.Background(), "dev-1")
	if f.calls != 0 {
		t.Errorf("disabled registrar made %d calls", f.calls)
	}
}

func TestRegisterQuietlySwallowsFailure(t *testing.T) {
	f := &fakeQrexec{err: errors.New("boom")}
	r := NewRemoteVMRegistrar(f, true)
	// Must not panic and must not propagate — the provision already succeeded.
	r.RegisterQuietly(context.Background(), "dev-1")
	if f.calls != 1 {
		t.Errorf("expected one attempt, got %d", f.calls)
	}
}

// TestRemoteVMCallTimeout pins the budget that must cover qvm-create plus the
// qvm-prefs writes in dom0. It is larger than the qrexec client default because
// the production console timed out at 30s with a half-configured shell.
func TestRemoteVMCallTimeout(t *testing.T) {
	if RemoteVMCallTimeout != 2*time.Minute {
		t.Fatalf("RemoteVMCallTimeout = %s, want 2m", RemoteVMCallTimeout)
	}
}
