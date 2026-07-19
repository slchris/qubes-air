// Package orchestrator turns console intent (start/stop a qube) into real
// infrastructure actions. The core is the Executor interface: the console
// calls it, and a concrete implementation actually drives Terraform (compute
// /storage separation: suspend = destroy compute, keep the data disk; resume =
// rebuild compute, re-attach the same disk).
//
// The interface is deliberately small and injectable so that:
//   - production wires a TerraformExecutor that really shells out to terraform;
//   - tests wire a FakeExecutor that records calls without touching a cloud;
//   - a NoopExecutor keeps the console working when no terraform is configured,
//     preserving the pre-orchestration "just flip the DB status" behaviour.
package orchestrator

import "context"

// Executor triggers infrastructure actions for a single qube by name.
//
// Implementations MUST treat qubeName as untrusted input and validate it before
// interpolating it into any command line (see ValidQubeName). All methods are
// expected to be blocking; callers pass a context with an appropriate timeout.
type Executor interface {
	// Suspend releases the compute instance while preserving the data disk
	// (terraform: compute_running=false apply, or a -target destroy of the
	// compute VM). Cheap "off" state that keeps data.
	Suspend(ctx context.Context, qubeName string) error

	// Resume rebuilds the compute instance and re-attaches the existing data
	// disk (terraform: compute_running=true apply).
	Resume(ctx context.Context, qubeName string) error

	// Provision creates a qube for the first time (compute + data disk).
	Provision(ctx context.Context, qubeName string) error

	// Destroy tears down the qube including its data disk. Destructive and
	// irreversible — data is lost.
	Destroy(ctx context.Context, qubeName string) error

	// Status reads the current infrastructure status for the qube from
	// terraform output (e.g. "running" / "suspended"). The string is the
	// provider/terraform view, not the console DB view.
	Status(ctx context.Context, qubeName string) (string, error)
}
