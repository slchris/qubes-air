// Package grpc is the gRPC bidirectional-stream implementation of the qubes-air
// cross-machine transport (docs/grpc-transport-design.md, roadmap stage T).
//
// STATUS: implemented + integration-tested; NOT real-machine validated.
//   - client.go / server.go are implemented (not skeletons).
//   - invoker.go (QrexecInvokerImpl) shells to `qrexec-client-vm` on the remote
//     host; reverse.go (NewReverseHandler) routes REMOTE_TO_LOCAL calls to the
//     local dom0 (policy C: ask); vaultcerts.go fetches mTLS certs from
//     vault-cloud via qrexec ask.
//   - integration_test.go stands up a real mTLS server, dials it, and drives a
//     forward Call end-to-end through the Tunnel — it passes under `go test`.
//   - Still not done: Salt/dom0 deployment and real-machine tests.
//
// Regenerating the proto (only needed if proto/relay_transport.proto changes):
//
//	cd proto
//	protoc --go_out=../internal/transport/relaypb --go_opt=paths=source_relative \
//	  --go-grpc_out=../internal/transport/relaypb --go-grpc_opt=paths=source_relative \
//	  relay_transport.proto
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
