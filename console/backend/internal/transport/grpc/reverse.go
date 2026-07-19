package grpc

import (
	"context"

	"github.com/slchris/qubes-air/console/internal/qrexec"
	"github.com/slchris/qubes-air/console/internal/transport"
)

// reverse.go — local-side handling of REMOTE_TO_LOCAL calls.
//
// When a remote qube originates a reverse call (e.g. it needs a credential from
// the local vault-cloud), the frame arrives on the client's Tunnel and is
// routed to a transport.ReverseHandler. The handler runs the call locally by
// shelling to qrexec-client-vm — which triggers the LOCAL dom0 policy C (ask)
// prompt. The handler NEVER decides authorization; local dom0 does.

// ReverseConfig configures which local target a reverse call is delivered to.
type ReverseConfig struct {
	// LocalTarget is the local qube a reverse call is delivered to (e.g.
	// "vault-cloud"). The service name comes from the reverse RequestHeader.
	// Constraining the target here (rather than trusting a remote-supplied
	// target) limits what a reverse call can reach; dom0 policy C is still the
	// authority.
	LocalTarget string
}

// NewReverseHandler builds a transport.ReverseHandler that delivers reverse
// calls to cfg.LocalTarget via qrexec (triggering local dom0 policy C: ask).
// Returns nil if LocalTarget is empty (reverse calls disabled).
func NewReverseHandler(cfg ReverseConfig) transport.ReverseHandler {
	if cfg.LocalTarget == "" {
		return nil
	}
	return newReverseHandlerWith(cfg.LocalTarget, qrexec.NewClient())
}

// newReverseHandlerWith is the test seam: inject a fake qrexec client.
func newReverseHandlerWith(localTarget string, qc qrexecClient) transport.ReverseHandler {
	return func(ctx context.Context, service string, in []byte) ([]byte, error) {
		// Deliver to the fixed local target; dom0 policy C (ask) gates it.
		// service is validated inside qrexec.Client.Call.
		return qc.Call(ctx, localTarget, service, in)
	}
}
