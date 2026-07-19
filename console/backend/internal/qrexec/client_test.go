package qrexec

import (
	"context"
	"errors"
	"testing"
)

// fakeRunner records the call and returns a canned response.
type fakeRunner struct {
	target, service string
	input           []byte
	out             []byte
	err             error
	called          bool
}

func (f *fakeRunner) Run(_ context.Context, target, service string, input []byte) ([]byte, error) {
	f.called = true
	f.target, f.service, f.input = target, service, append([]byte(nil), input...)
	return f.out, f.err
}

func TestValidArg(t *testing.T) {
	ok := []string{"vault-cloud", "qubesair.GetCredential", "remote-gpu", "a_b.c+d"}
	bad := []string{"", "../etc", "has space", "semi;colon", "back`tick", "a|b"}
	for _, s := range ok {
		if !ValidArg(s) {
			t.Errorf("ValidArg(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if ValidArg(s) {
			t.Errorf("ValidArg(%q) = true, want false", s)
		}
	}
}

func TestCallValidatesAndForwards(t *testing.T) {
	fr := &fakeRunner{out: []byte("resp")}
	c := NewClient(WithRunner(fr))

	// valid call is forwarded to the runner
	out, err := c.Call(context.Background(), "vault-cloud", "qubesair.GetCredential+gcp-key", []byte("in"))
	if err != nil {
		t.Fatalf("Call err: %v", err)
	}
	if string(out) != "resp" {
		t.Errorf("out = %q, want resp", out)
	}
	if !fr.called || fr.target != "vault-cloud" || fr.service != "qubesair.GetCredential+gcp-key" || string(fr.input) != "in" {
		t.Errorf("runner got target=%q service=%q input=%q", fr.target, fr.service, fr.input)
	}
}

func TestCallRejectsInjection(t *testing.T) {
	fr := &fakeRunner{}
	c := NewClient(WithRunner(fr))

	if _, err := c.Call(context.Background(), "bad;name", "svc", nil); err == nil {
		t.Error("expected error for bad target")
	}
	if _, err := c.Call(context.Background(), "target", "bad service", nil); err == nil {
		t.Error("expected error for bad service")
	}
	if fr.called {
		t.Error("runner must not be called when validation fails")
	}
}

func TestCallPropagatesRunnerError(t *testing.T) {
	wantErr := errors.New("boom")
	c := NewClient(WithRunner(&fakeRunner{err: wantErr}))
	if _, err := c.Call(context.Background(), "t", "s", nil); !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
}
