// agentdial.go — the one place that decides HOW a qube's agent is reached.
//
// Three flows talk to agents — health probing, certificate renewal and
// bootstrap — and until now each opened its own connection. They happened to
// open the same kind, so it read as three copies of one line rather than three
// decisions. It is three decisions, and they have to agree: a zone the prober
// can reach but the renewer cannot is a fleet that reports healthy right up to
// the day its certificates expire.
//
// The seam exists because reachability is not one thing across providers
// (docs/bootstrap-design.md §12.5):
//
//   - ROUTED — a route to the qube's private network exists, so net.Dial works.
//     This covers a flat LAN, a WireGuard gateway, GCP Cloud VPN and AWS
//     Transit Gateway alike. They differ enormously as infrastructure and not
//     at all from here, which is why there is one implementation for all of
//     them, and why WireGuard appears nowhere in this package.
//   - PER-CONNECTION — GCP IAP and AWS SSM forwarding reach a private instance
//     with no inbound rule and no gateway host, authorized by IAM rather than a
//     shared key. Those are not routes; each connection is its own tunnel.
//
// Only the routed dialer exists today. The interface takes the QUBE rather than
// a pre-formatted address on purpose: IAP addresses an instance by
// project/zone/instance and never sees an IP, so an `addr string` parameter
// would be the exact decision that has to be undone when the second
// implementation lands.
package service

import (
	"context"
	"net"

	"github.com/slchris/qubes-air/console/internal/models"
)

// AgentDialer opens a transport-level connection to a qube's agent.
//
// It returns a raw net.Conn and does NOT do TLS. That split is load-bearing:
// the prober distinguishes "nothing is listening" from "the handshake was
// rejected", and those have opposite remedies — go look at the VM versus go
// look at the PKI. Folding TLS into the dialer would collapse them into one
// error and send operators to the wrong machine.
type AgentDialer interface {
	// DialAgent connects to the qube's agent port.
	DialAgent(ctx context.Context, qube *models.Qube) (net.Conn, error)
	// Address describes the target for logs, errors and the gRPC authority.
	// It is a label, not necessarily something dialable by anything else.
	Address(qube *models.Qube) string
}

// DirectDialer reaches a qube by dialing its recorded address.
//
// Correct whenever a route exists — which is the flat-LAN case today, and the
// WireGuard and Cloud VPN cases once either is built. Nothing here has to know
// which: the route either exists or it does not.
type DirectDialer struct {
	port string
}

// NewDirectDialer builds a dialer for the agent's listen address.
func NewDirectDialer(agentListen string) *DirectDialer {
	return &DirectDialer{port: agentPortFrom(agentListen)}
}

// DialAgent opens TCP to the qube's address.
func (d *DirectDialer) DialAgent(ctx context.Context, qube *models.Qube) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "tcp", d.Address(qube))
}

// Address is host:port from the qube's recorded IP.
//
// The callers check for a missing IP before they get here, and produce their
// own status for it, so this does not repeat the check — duplicating it would
// mean two places to keep in step with no test able to tell them apart. When a
// dialer arrives that does not address by IP, that precondition moves here.
func (d *DirectDialer) Address(qube *models.Qube) string {
	if qube == nil {
		return ""
	}
	return net.JoinHostPort(qube.IPAddress, d.port)
}

// dialFuncFor adapts an AgentDialer to the signature gRPC's WithContextDialer
// wants.
//
// The address gRPC passes is discarded: it comes from the target string, and
// the dialer already knows which qube it is reaching. Keeping the qube rather
// than round-tripping through a string is what lets a future dialer address by
// something that is not an address at all.
func dialFuncFor(d AgentDialer, qube *models.Qube) func(context.Context, string) (net.Conn, error) {
	if d == nil {
		return nil
	}
	return func(ctx context.Context, _ string) (net.Conn, error) {
		return d.DialAgent(ctx, qube)
	}
}
