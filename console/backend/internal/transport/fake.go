package transport

import (
	"context"
	"sync"
)

// FakeTransport records calls without touching the network — the test seam,
// mirroring orchestrator.FakeExecutor. Set RespFn to control responses, or
// leave it nil to echo the input back.
type FakeTransport struct {
	mu     sync.Mutex
	Calls  []FakeCall
	RespFn func(target, service string, in []byte) ([]byte, error)
}

// FakeCall is one recorded Call.
type FakeCall struct {
	Target  string
	Service string
	In      []byte
}

// Call records the call and returns RespFn's result (or echoes input).
func (f *FakeTransport) Call(_ context.Context, target, service string, in []byte) ([]byte, error) {
	if !ValidName(target) || !ValidName(service) {
		return nil, ErrInvalidName
	}
	f.mu.Lock()
	f.Calls = append(f.Calls, FakeCall{Target: target, Service: service, In: append([]byte(nil), in...)})
	f.mu.Unlock()
	if f.RespFn != nil {
		return f.RespFn(target, service, in)
	}
	return in, nil
}

// CallCount returns how many Call invocations were recorded.
func (f *FakeTransport) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Calls)
}
