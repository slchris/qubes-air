package grpc

import (
	"context"
	"errors"
	"testing"
)

// fakeQrexec records calls and returns a canned response, satisfying qrexecClient.
type fakeQrexec struct {
	target, service string
	input           []byte
	out             []byte
	err             error
	calls           int
}

func (f *fakeQrexec) Call(_ context.Context, target, service string, input []byte) ([]byte, error) {
	f.calls++
	f.target, f.service, f.input = target, service, append([]byte(nil), input...)
	return f.out, f.err
}

func TestQrexecInvokerForwards(t *testing.T) {
	fq := &fakeQrexec{out: []byte("resp")}
	inv := newQrexecInvokerWith(fq)

	out, err := inv.Invoke(context.Background(), "remote-gpu", "qubesair.Echo", []byte("hi"))
	if err != nil {
		t.Fatalf("Invoke err: %v", err)
	}
	if string(out) != "resp" {
		t.Errorf("out = %q, want resp", out)
	}
	if fq.target != "remote-gpu" || fq.service != "qubesair.Echo" || string(fq.input) != "hi" {
		t.Errorf("qrexec got target=%q service=%q input=%q", fq.target, fq.service, fq.input)
	}
}

func TestQrexecInvokerPropagatesError(t *testing.T) {
	wantErr := errors.New("denied")
	inv := newQrexecInvokerWith(&fakeQrexec{err: wantErr})
	if _, err := inv.Invoke(context.Background(), "t", "s", nil); !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
}

func TestReverseHandlerDeliversToLocalTarget(t *testing.T) {
	fq := &fakeQrexec{out: []byte("secret")}
	// reverse call for service "qubesair.GetCredential+gcp-key" must be delivered
	// to the fixed local target (vault-cloud), NOT a remote-supplied target.
	h := newReverseHandlerWith("vault-cloud", fq)

	out, err := h(context.Background(), "qubesair.GetCredential+gcp-key", nil)
	if err != nil {
		t.Fatalf("reverse err: %v", err)
	}
	if string(out) != "secret" {
		t.Errorf("out = %q, want secret", out)
	}
	if fq.target != "vault-cloud" {
		t.Errorf("reverse delivered to target %q, want vault-cloud", fq.target)
	}
	if fq.service != "qubesair.GetCredential+gcp-key" {
		t.Errorf("service = %q", fq.service)
	}
}

func TestNewReverseHandlerDisabledWhenNoTarget(t *testing.T) {
	if h := NewReverseHandler(ReverseConfig{LocalTarget: ""}); h != nil {
		t.Error("expected nil handler when LocalTarget is empty (reverse disabled)")
	}
}
