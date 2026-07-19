package grpc

import (
	"context"

	"github.com/slchris/qubes-air/console/internal/qrexec"
)

// qrexecClient is the subset of *qrexec.Client the invoker needs. Declaring it
// as an interface keeps the invoker unit-testable without a real
// qrexec-client-vm (inject a fake).
type qrexecClient interface {
	Call(ctx context.Context, target, service string, input []byte) ([]byte, error)
}

// QrexecInvokerImpl is the remote-side executor: on the Remote-Relay host it
// runs a forward call by shelling out to qrexec-client-vm (via qrexec.Client).
//
// SECURITY: reaching Invoke means the REMOTE dom0/policy has already
// re-authorized this call (policy lives in dom0, not here). This type only
// executes an already-authorized call; it makes no authorization decision.
type QrexecInvokerImpl struct {
	qc qrexecClient
}

// compile-time check: *QrexecInvokerImpl satisfies QrexecInvoker.
var _ QrexecInvoker = (*QrexecInvokerImpl)(nil)

// NewQrexecInvoker builds the remote-side invoker over a real qrexec client.
func NewQrexecInvoker() *QrexecInvokerImpl {
	return &QrexecInvokerImpl{qc: qrexec.NewClient()}
}

// newQrexecInvokerWith is the test seam: inject a fake qrexec client.
func newQrexecInvokerWith(qc qrexecClient) *QrexecInvokerImpl {
	return &QrexecInvokerImpl{qc: qc}
}

// Invoke runs the forward qrexec call locally (post remote-dom0 re-check) and
// returns its response. Name validation happens in qrexec.Client.Call.
func (i *QrexecInvokerImpl) Invoke(ctx context.Context, target, service string, in []byte) ([]byte, error) {
	return i.qc.Call(ctx, target, service, in)
}
