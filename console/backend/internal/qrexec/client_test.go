package qrexec

import (
	"context"
	"errors"
	"testing"
	"time"
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

// ctxRunner blocks for as long as its context allows, then reports why it
// stopped. It makes the deadline/cancellation the client imposes observable.
type ctxRunner struct{ entered chan struct{} }

func (r *ctxRunner) Run(ctx context.Context, _, _ string, _ []byte) ([]byte, error) {
	select {
	case r.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestCallAppliesConfiguredTimeout pins WithTimeout: the deadline the client
// wraps around every call must fire even when the caller passes a context with
// none, or a wedged qrexec-client-vm would pin the caller forever.
func TestCallAppliesConfiguredTimeout(t *testing.T) {
	c := NewClient(WithRunner(&ctxRunner{entered: make(chan struct{}, 1)}), WithTimeout(50*time.Millisecond))

	start := time.Now()
	_, err := c.Call(context.Background(), "vault-cloud", "qubesair.Ping", nil)
	elapsed := time.Since(start)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Call error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("Call took %s to honor the 50ms timeout", elapsed)
	}
}

// TestCallResultPropagatesCancellation pins that a caller-canceled context is a
// transport failure for CallResult too, never a fabricated zero-exit result:
// the exit code is what callers act on, so a cancellation must not look like a
// command that ran.
func TestCallResultPropagatesCancellation(t *testing.T) {
	runner := &ctxRunner{entered: make(chan struct{}, 1)}
	c := NewClient(WithRunner(runner), WithTimeout(time.Minute))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.CallResult(ctx, "vault-cloud", "qubesair.Ping", nil)
		done <- err
	}()

	select {
	case <-runner.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("runner was never invoked")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("CallResult error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CallResult did not return after cancellation")
	}
}
