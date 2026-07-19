package orchestrator

import (
	"context"
	"sync"
)

// Action names recorded by FakeExecutor.
const (
	ActionSuspend   = "suspend"
	ActionResume    = "resume"
	ActionProvision = "provision"
	ActionDestroy   = "destroy"
	ActionStatus    = "status"
)

// Call records a single Executor method invocation.
type Call struct {
	Action string
	Qube   string
}

// FakeExecutor is an in-memory Executor for tests. It records every call and
// lets a test inject a failure for a specific action to exercise error paths.
// It is safe for concurrent use.
type FakeExecutor struct {
	mu    sync.Mutex
	calls []Call

	// FailOn maps an action name to an error to return for that action. Nil or
	// missing means success.
	FailOn map[string]error
	// StatusResult is returned by Status when no failure is configured.
	StatusResult string
}

// NewFakeExecutor returns a ready-to-use FakeExecutor.
func NewFakeExecutor() *FakeExecutor {
	return &FakeExecutor{
		FailOn:       map[string]error{},
		StatusResult: "running",
	}
}

func (f *FakeExecutor) record(action, qube string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{Action: action, Qube: qube})
	if f.FailOn != nil {
		if err, ok := f.FailOn[action]; ok {
			return err
		}
	}
	return nil
}

// Suspend records a suspend call.
func (f *FakeExecutor) Suspend(_ context.Context, qubeName string) error {
	return f.record(ActionSuspend, qubeName)
}

// Resume records a resume call.
func (f *FakeExecutor) Resume(_ context.Context, qubeName string) error {
	return f.record(ActionResume, qubeName)
}

// Provision records a provision call.
func (f *FakeExecutor) Provision(_ context.Context, qubeName string) error {
	return f.record(ActionProvision, qubeName)
}

// Destroy records a destroy call.
func (f *FakeExecutor) Destroy(_ context.Context, qubeName string) error {
	return f.record(ActionDestroy, qubeName)
}

// Status records a status call and returns the configured result.
func (f *FakeExecutor) Status(_ context.Context, qubeName string) (string, error) {
	if err := f.record(ActionStatus, qubeName); err != nil {
		return "", err
	}
	return f.StatusResult, nil
}

// Calls returns a copy of the recorded calls in order.
func (f *FakeExecutor) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Call, len(f.calls))
	copy(out, f.calls)
	return out
}

// Reset clears the recorded calls.
func (f *FakeExecutor) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}
