// Package orchestrator turns console intent (start/stop a qube) into real
// infrastructure actions. The core is the Executor interface: the console calls
// it, and a concrete implementation drives a provider adapter directly (compute
// /storage separation: suspend = destroy the compute instance, keep the data
// disk; resume = rebuild compute, re-attach the same disk).
//
// The interface is deliberately small and injectable so that:
//   - production wires a NativeExecutor that calls provider adapters;
//   - tests wire a FakeExecutor that records calls without touching a cloud;
//   - a NoopExecutor keeps the console working when orchestration is disabled,
//     preserving the DB-only "just flip the status" behavior.
package orchestrator

import (
	"context"
	"errors"
)

// ErrExecutorBusy reports that a provider operation is mid-run and a read-only
// query declined to wait. Distinct from a failure: nothing is wrong, the answer
// is simply not available right now.
var ErrExecutorBusy = errors.New("orchestrator executor is busy with another run")

// Executor triggers infrastructure actions for a single qube by name.
//
// Implementations MUST treat qubeName as untrusted input and validate it before
// using it with a provider (see ValidQubeName). All methods are expected to be
// blocking; callers pass a context with an appropriate timeout.
type Executor interface {
	// Suspend releases the compute instance while preserving the data disk.
	// Cheap "off" state that keeps data.
	Suspend(ctx context.Context, qubeName string) error

	// Resume rebuilds the compute instance and re-attaches the existing data
	// disk.
	Resume(ctx context.Context, qubeName string) error

	// Provision creates a qube for the first time (compute + data disk).
	Provision(ctx context.Context, qubeName string) error

	// Destroy tears down the qube including its data disk. Destructive and
	// irreversible — data is lost.
	Destroy(ctx context.Context, qubeName string) error

	// Status reads the current infrastructure status for the qube from the
	// provider. The string is the provider's view, not the console DB view.
	Status(ctx context.Context, qubeName string) (string, error)
}
