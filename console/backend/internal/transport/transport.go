// Package transport carries qrexec request/response frames between the local
// sys-relay and a remote Remote-Relay over a gRPC bidirectional stream (see
// docs/grpc-transport-design.md, roadmap-to-production.md stage T).
//
// This is a NEW path. It is NOT the same as orchestrator.Executor: that turns
// console intent (suspend/resume a qube) into terraform actions. This package
// forwards qrexec calls across machines — its semantics mirror the qrexec
// Client.Call(ctx, target, service, input) → output shape, not terraform.
//
// Security invariants (enforced by dom0 policy, NOT here):
//   - The transport only moves frames; authorization always lives in the two
//     dom0s. A forward call has already passed local dom0 policy A/B before it
//     reaches Call; the remote side re-checks via its own dom0/policy.
//   - A reverse call (remote → local, e.g. fetching a vault credential) comes
//     back through the stream and MUST pass local dom0 policy C (ask) before it
//     runs — that is the ReverseHandler's job to route to dom0, never to decide.
//   - Relay is a plain AppVM and must never reach dom0 directly.
//
// The interface is deliberately small and injectable so that production wires a
// gRPC transport, tests wire a FakeTransport, and NoopTransport keeps the
// console working when no transport is configured.
//
// STATUS: [TODO] skeleton for stage T. The gRPC implementation under grpc/ is a
// draft and does not compile until google.golang.org/grpc is added and the
// proto is generated (see grpc/doc.go).
package transport

import (
	"context"
	"errors"
	"regexp"
)

// Transport forwards a single qrexec call to a remote target and returns its
// response. Implementations MUST treat target and service as untrusted input
// and validate them (see ValidName) before putting them on the wire. All
// methods block; callers pass a context with an appropriate timeout/deadline.
type Transport interface {
	// Call sends an already-authorized (local dom0 passed) qrexec call to the
	// remote target's service over the tunnel and returns the response body.
	// It does not perform authorization — that happened at dom0 before Call,
	// and happens again at the remote dom0 after the frame arrives.
	Call(ctx context.Context, target, service string, in []byte) ([]byte, error)
}

// ReverseHandler handles a reverse (remote → local) call that arrived over the
// tunnel. The implementation MUST route it to the local dom0 (policy C: ask)
// and return the result — it must never decide authorization itself.
//
// [TODO] Wire this to a qrexec call into the local target (e.g. vault-cloud
// qubesair.GetCredential) so dom0 policy C prompts the user.
type ReverseHandler func(ctx context.Context, service string, in []byte) ([]byte, error)

// nameRe is the shared allow-list for qube/service names put on the wire.
// Matches orchestrator.ValidQubeName / qrexec.validQrexecArg: [A-Za-z0-9._-],
// non-empty, no path traversal.
var nameRe = regexp.MustCompile(`^[A-Za-z0-9._+-]+$`)

// ErrInvalidName is returned when a target/service name fails validation.
var ErrInvalidName = errors.New("transport: invalid target/service name")

// ValidName reports whether s is a safe target/service name to transmit.
func ValidName(s string) bool {
	return s != "" && len(s) <= 128 && nameRe.MatchString(s)
}

// NoopTransport is the default: it validates names and returns ErrNoTransport
// so the console keeps working (and fails loudly) when no gRPC transport is
// configured. Mirrors orchestrator.NoopExecutor.
type NoopTransport struct{}

// ErrNoTransport indicates no real transport is configured.
var ErrNoTransport = errors.New("transport: no gRPC transport configured (noop)")

// Call validates inputs then reports that no transport is wired.
func (NoopTransport) Call(_ context.Context, target, service string, _ []byte) ([]byte, error) {
	if !ValidName(target) || !ValidName(service) {
		return nil, ErrInvalidName
	}
	return nil, ErrNoTransport
}
