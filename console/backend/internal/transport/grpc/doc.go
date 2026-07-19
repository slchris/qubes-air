// Package grpc is the gRPC bidirectional-stream implementation of the qubes-air
// cross-machine transport (docs/grpc-transport-design.md, roadmap stage T).
//
// STATUS: implemented + integration-tested; NOT real-machine validated.
//   - client.go / server.go are implemented (not skeletons).
//   - integration_test.go stands up a real mTLS server, dials it, and drives a
//     forward Call end-to-end through the Tunnel — it passes under `go test`.
//   - Still TODO (outside this package): a concrete QrexecInvoker that shells to
//     `qrexec-client-vm` on the remote host; a ReverseHandler that routes
//     REMOTE_TO_LOCAL calls to the local dom0 (policy C: ask); mTLS certs fetched
//     from vault-cloud via qrexec ask; Salt/dom0 deployment; real-machine tests.
//
// Regenerating the proto (only needed if proto/relay_transport.proto changes):
//
//	protoc --proto_path=proto --go_out=. --go-grpc_out=. \
//	  --go_opt=module=github.com/slchris/qubes-air/console \
//	  --go-grpc_opt=module=github.com/slchris/qubes-air/console \
//	  proto/relay_transport.proto
//	# → internal/transport/relaypb/{relay_transport.pb.go, _grpc.pb.go}
//
// Roles:
//   - client.go runs on the LOCAL sys-relay: dials OUTBOUND to the remote, keeps
//     one long-lived bidi Tunnel, multiplexes qrexec calls by request_id,
//     reconnects on drop. Implements transport.Transport.
//   - server.go runs on the REMOTE Remote-Relay: accepts the tunnel, routes
//     forward frames to the QrexecInvoker after the remote dom0/policy re-checks,
//     and relays reverse frames back.
//   - mTLS certs come from vault-cloud via qrexec ask (not embedded here).
//   - The transport moves frames only; authorization stays in the two dom0s.
package grpc
