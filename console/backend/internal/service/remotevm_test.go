package service

import (
	"context"
	"errors"
	"strings"
	"testing"
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
	f := &fakeQrexec{out: "register: DONE — remote-dev-1 -> remote-dev-1"}
	r := NewRemoteVMRegistrar(f, true)

	if err := r.Register(context.Background(), "remote-dev-1"); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if f.target != "dom0" || f.service != "qubesair.RegisterRemoteVM" {
		t.Errorf("called %s/%s, want dom0/qubesair.RegisterRemoteVM", f.target, f.service)
	}
	// Local and remote name are the same string by design; see Register's doc.
	if f.input != "register remote-dev-1 remote-dev-1\n" {
		t.Errorf("input = %q", f.input)
	}
}

func TestDeregisterSendsName(t *testing.T) {
	f := &fakeQrexec{out: "deregister: DONE"}
	r := NewRemoteVMRegistrar(f, true)

	if err := r.Deregister(context.Background(), "remote-dev-1"); err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	if f.input != "deregister remote-dev-1\n" {
		t.Errorf("input = %q", f.input)
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
		err := r.Register(context.Background(), "remote-dev-1")
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
	if err := r.Register(context.Background(), "remote-dev-1"); err == nil {
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

	for _, bad := range []string{"", "remote-dev-1 sys-net", "remote\tdev", "remote\ndev"} {
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
	r.RegisterQuietly(context.Background(), "remote-dev-1")
	r.DeregisterQuietly(context.Background(), "remote-dev-1")
	if f.calls != 0 {
		t.Errorf("disabled registrar made %d calls", f.calls)
	}
}

func TestRegisterQuietlySwallowsFailure(t *testing.T) {
	f := &fakeQrexec{err: errors.New("boom")}
	r := NewRemoteVMRegistrar(f, true)
	// Must not panic and must not propagate — the provision already succeeded.
	r.RegisterQuietly(context.Background(), "remote-dev-1")
	if f.calls != 1 {
		t.Errorf("expected one attempt, got %d", f.calls)
	}
}
