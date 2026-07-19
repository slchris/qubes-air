package orchestrator

import "context"

// NoopExecutor is an Executor that does nothing and always succeeds. It is the
// default when no terraform environment is configured, preserving the console's
// original behaviour (flip the DB status without touching infrastructure) so
// that existing CRUD flows and tests keep working on a machine without a cloud.
//
// It still validates the qube name so that even the no-op path rejects unsafe
// input consistently with the real executor.
type NoopExecutor struct{}

// NewNoopExecutor returns a NoopExecutor.
func NewNoopExecutor() NoopExecutor { return NoopExecutor{} }

func (NoopExecutor) guard(qubeName string) error {
	if !ValidQubeName(qubeName) {
		return &ErrInvalidQubeName{Name: qubeName}
	}
	return nil
}

// Suspend validates the name and returns nil.
func (n NoopExecutor) Suspend(_ context.Context, qubeName string) error { return n.guard(qubeName) }

// Resume validates the name and returns nil.
func (n NoopExecutor) Resume(_ context.Context, qubeName string) error { return n.guard(qubeName) }

// Provision validates the name and returns nil.
func (n NoopExecutor) Provision(_ context.Context, qubeName string) error { return n.guard(qubeName) }

// Destroy validates the name and returns nil.
func (n NoopExecutor) Destroy(_ context.Context, qubeName string) error { return n.guard(qubeName) }

// Status validates the name and returns an empty status.
func (n NoopExecutor) Status(_ context.Context, qubeName string) (string, error) {
	return "", n.guard(qubeName)
}
