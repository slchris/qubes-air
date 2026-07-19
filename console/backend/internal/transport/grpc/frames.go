package grpc

import (
	pb "github.com/slchris/qubes-air/console/internal/transport/relaypb"
)

// frames.go — shared Frame construction helpers for client and server.
// Keeps the oneof wrapping in one place so both sides build frames identically.

// stream_id conventions on a request_id (see proto DataChunk).
const (
	streamRequest  uint32 = 0 // request body: initiator → executor
	streamResponse uint32 = 1 // response body: executor → initiator
	streamStderr   uint32 = 2 // optional stderr
)

const protocolVersion = "v1"

func handshakeFrame(relayName, remoteName string) *pb.Frame {
	return &pb.Frame{
		Kind: &pb.Frame_Handshake{Handshake: &pb.Handshake{
			ProtocolVersion: protocolVersion,
			RelayName:       relayName,
			RemoteName:      remoteName,
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
