package grpc

import (
	"fmt"
	pb "github.com/slchris/qubes-air/console/internal/transport/relaypb"
	"sort"
)

// frames.go — shared Frame construction helpers for client and server.
// Keeps the oneof wrapping in one place so both sides build frames identically.

// stream_id conventions on a request_id (see proto DataChunk).
const (
	streamRequest  uint32 = 0 // request body: initiator → executor
	streamResponse uint32 = 1 // response body: executor → initiator
	streamStderr   uint32 = 2 // optional stderr
)

// protocolVersion is the wire protocol this build speaks.
//
// Bump it only when the frame format or its semantics change — NOT when the
// binary is released. Conflating the two would make every release an
// incompatible one and break every agent in the field.
const protocolVersion = "v1"

// supportedProtocolVersions is every wire version this build can serve.
//
// Kept as a set rather than an equality check so a protocol bump can be rolled
// out without a flag day: a build that speaks both v1 and v2 lets the two sides
// be upgraded in either order.
var supportedProtocolVersions = map[string]bool{
	"v1": true,
}

// BuildVersion is this binary's build version, reported in the handshake for
// observability only. It never participates in compatibility decisions.
// Overridden at link time with -ldflags "-X ...BuildVersion=x.y.z".
var BuildVersion = "dev"

// supportsProtocol reports whether this build can serve the given wire version.
func supportsProtocol(v string) bool { return supportedProtocolVersions[v] }

// protocolMismatchMessage explains a rejection in terms an operator can act on.
//
// A bare stream error tells the agent only that the connection closed, which is
// indistinguishable from a network fault — and sends whoever is debugging it
// looking at firewalls instead of versions.
func protocolMismatchMessage(got string) string {
	supported := make([]string, 0, len(supportedProtocolVersions))
	for v := range supportedProtocolVersions {
		supported = append(supported, v)
	}
	sort.Strings(supported)
	if got == "" {
		return fmt.Sprintf("peer sent no protocol version; this build serves %v", supported)
	}
	return fmt.Sprintf("peer speaks protocol %q but this build serves %v; upgrade whichever side is older",
		got, supported)
}

func handshakeFrame(relayName, remoteName string) *pb.Frame {
	return &pb.Frame{
		Kind: &pb.Frame_Handshake{Handshake: &pb.Handshake{
			ProtocolVersion: protocolVersion,
			RelayName:       relayName,
			RemoteName:      remoteName,
			BuildVersion:    BuildVersion,
		}},
	}
}

func requestHeaderFrame(reqID string, dir pb.Direction, service, source, target string, deadlineMs int64) *pb.Frame {
	return &pb.Frame{
		RequestId: reqID,
		Kind: &pb.Frame_RequestHeader{RequestHeader: &pb.RequestHeader{
			Direction:      dir,
			QrexecService:  service,
			SourceQube:     source,
			TargetQube:     target,
			DeadlineUnixMs: deadlineMs,
		}},
	}
}

func dataFrame(reqID string, streamID uint32, payload []byte) *pb.Frame {
	return &pb.Frame{
		RequestId: reqID,
		Kind:      &pb.Frame_Data{Data: &pb.DataChunk{StreamId: streamID, Payload: payload}},
	}
}

func eosFrame(reqID string, streamID uint32) *pb.Frame {
	return &pb.Frame{
		RequestId: reqID,
		Kind:      &pb.Frame_Eos{Eos: &pb.EndOfStream{StreamId: streamID}},
	}
}

func errorFrame(reqID, code, msg string) *pb.Frame {
	return &pb.Frame{
		RequestId: reqID,
		Kind:      &pb.Frame_Error{Error: &pb.CallError{Code: code, Message: msg}},
	}
}

func keepAliveFrame(unixMs int64) *pb.Frame {
	return &pb.Frame{
		Kind: &pb.Frame_KeepAlive{KeepAlive: &pb.KeepAlive{UnixMs: unixMs}},
	}
}

// stable CallError codes.
const (
	codeDenied      = "DENIED"      // remote policy rejected
	codeTimeout     = "TIMEOUT"     // deadline exceeded
	codeUnavailable = "UNAVAILABLE" // tunnel dropped / in-flight aborted
	codeInternal    = "INTERNAL"    // unexpected error
	codeInvalid     = "INVALID"     // malformed frame / bad name
)

// Stable CallError codes.
//
// These are matched by callers and surfaced to operators, so they are part of
// the contract: rename one and you break whoever is switching on it.
const (
	// CodeProtocolMismatch: the peer speaks a wire version this build cannot serve.
	CodeProtocolMismatch = "PROTOCOL_MISMATCH"
	// CodeDenied: local policy refused the call.
	CodeDenied = "DENIED"
	// CodeTimeout: the call exceeded its deadline.
	CodeTimeout = "TIMEOUT"
	// CodeUnavailable: the executor could not be reached.
	CodeUnavailable = "UNAVAILABLE"
	// CodeInternal: an unexpected failure.
	CodeInternal = "INTERNAL"
)

// orUnknown renders an empty version as something readable in a log line.
func orUnknown(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}
