package transport

import (
	"context"
	"errors"
	"testing"
)

func TestValidName(t *testing.T) {
	ok := []string{"vault-cloud", "qubesair.GetCredential", "remote-gpu", "a_b.c+d", "x"}
	bad := []string{"", "../etc", "has space", "semi;colon", "back`tick", string(make([]byte, 200))}
	for _, s := range ok {
		if !ValidName(s) {
			t.Errorf("ValidName(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if ValidName(s) {
			t.Errorf("ValidName(%q) = true, want false", s)
		}
	}
}

func TestNoopTransport(t *testing.T) {
	var tr Transport = NoopTransport{} // compile-time: NoopTransport satisfies Transport
	// invalid name → ErrInvalidName
	if _, err := tr.Call(context.Background(), "bad name", "svc", nil); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("want ErrInvalidName, got %v", err)
	}
	// valid names → ErrNoTransport (loud failure, not silent success)
	if _, err := tr.Call(context.Background(), "vault-cloud", "qubesair.GetCredential", nil); !errors.Is(err, ErrNoTransport) {
		t.Fatalf("want ErrNoTransport, got %v", err)
	}
}

func TestFakeTransport(t *testing.T) {
	var tr Transport = &FakeTransport{} // compile-time: *FakeTransport satisfies Transport
	f := tr.(*FakeTransport)

	// default echoes input
	out, err := tr.Call(context.Background(), "remote-gpu", "qubesair.Foo", []byte("hi"))
	if err != nil {
		t.Fatalf("Call err: %v", err)
	}
	if string(out) != "hi" {
		t.Errorf("echo: got %q, want %q", out, "hi")
	}
	if f.CallCount() != 1 {
		t.Errorf("CallCount = %d, want 1", f.CallCount())
	}
	if got := f.Calls[0]; got.Target != "remote-gpu" || got.Service != "qubesair.Foo" {
		t.Errorf("recorded call = %+v", got)
	}

	// RespFn overrides
	f2 := &FakeTransport{RespFn: func(_, _ string, _ []byte) ([]byte, error) { return []byte("ok"), nil }}
	out, _ = f2.Call(context.Background(), "t", "s", nil)
	if string(out) != "ok" {
		t.Errorf("RespFn: got %q, want ok", out)
	}

	// invalid name rejected before recording
	if _, err := f2.Call(context.Background(), "../x", "s", nil); !errors.Is(err, ErrInvalidName) {
		t.Errorf("want ErrInvalidName, got %v", err)
	}
}
