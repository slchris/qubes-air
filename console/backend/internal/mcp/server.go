package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Server dispatches JSON-RPC 2.0 messages read from a Codec against a
// Registry. It answers initialize / ping / tools/list / tools/call, swallows
// notifications (including notifications/initialized), and refuses anything
// else with -32601.
type Server struct {
	codec *Codec
	reg   *Registry
}

// NewServer wires a serve loop to a codec and a registry.
func NewServer(codec *Codec, reg *Registry) *Server {
	return &Server{codec: codec, reg: reg}
}

// Serve runs the request/response loop until EOF (clean, nil), a decode error
// that breaks the stream (returns the cause), or ctx cancellation.
//
// A single over-long line is terminal: the newline boundary has been lost, so
// the stream cannot be trusted again. The line cap is what protects the process
// from unbounded memory, so giving up the decode loop on it is the intended
// trade-off, not a retry opportunity.
func (s *Server) Serve(ctx context.Context) error {
	for {
		raw, err := s.codec.Read()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			if errors.Is(err, ErrLineTooLong) {
				// One last reply with a null id, then stop.
				_ = s.respond(&Response{JSONRPC: JSONRPCVersion, Error: &RPCError{
					Code:    CodeParseError,
					Message: "message exceeds maximum single-line size",
				}})
				return err
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}

		var req Request
		if err := json.Unmarshal(raw, &req); err != nil {
			// Invalid JSON on its own line: the next line is still a valid
			// boundary, so the loop may continue after answering.
			if err := s.respond(&Response{JSONRPC: JSONRPCVersion, Error: &RPCError{
				Code:    CodeParseError,
				Message: "parse error: invalid JSON",
			}}); err != nil {
				return err
			}
			continue
		}

		if err := s.dispatch(ctx, &req); err != nil {
			return err
		}
	}
}

// dispatch routes one parsed request. Notifications never receive responses,
// even on error, per JSON-RPC 2.0. Writes that fail end the loop.
func (s *Server) dispatch(ctx context.Context, req *Request) error {
	if req.JSONRPC != JSONRPCVersion || req.Method == "" {
		if !req.Notification() {
			return s.respond(&Response{JSONRPC: JSONRPCVersion, ID: req.ID, Error: &RPCError{
				Code:    CodeInvalidRequest,
				Message: "invalid request: expected jsonrpc \"2.0\" and a method",
			}})
		}
		return nil
	}

	switch req.Method {
	case MethodInitialize:
		return s.handleInitialize(req)
	case MethodInitializedNotification:
		return nil
	case MethodPing:
		if !req.Notification() {
			return s.respond(&Response{JSONRPC: JSONRPCVersion, ID: req.ID, Result: map[string]any{}})
		}
		return nil
	case MethodToolsList:
		if !req.Notification() {
			return s.respond(&Response{JSONRPC: JSONRPCVersion, ID: req.ID, Result: ListToolsResult{Tools: s.reg.Tools()}})
		}
		return nil
	case MethodToolsCall:
		if !req.Notification() {
			return s.handleToolsCall(ctx, req)
		}
		return nil
	default:
		if !req.Notification() {
			return s.respond(&Response{JSONRPC: JSONRPCVersion, ID: req.ID, Error: &RPCError{
				Code:    CodeMethodNotFound,
				Message: "method not found: " + req.Method,
			}})
		}
		return nil
	}
}

// handleInitialize negotiates the protocol version and advertises capabilities.
func (s *Server) handleInitialize(req *Request) error {
	var params InitializeParams
	if len(req.Params) > 0 && !emptyObject(req.Params) {
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return s.respond(&Response{JSONRPC: JSONRPCVersion, ID: req.ID, Error: &RPCError{
				Code:    CodeInvalidParams,
				Message: "invalid initialize params: " + err.Error(),
			}})
		}
	}

	result := InitializeResult{
		ProtocolVersion: ProtocolVersion,
		Capabilities: Capabilities{
			Tools: &ToolsCapability{ListChanged: false},
		},
		ServerInfo: Implementation{Name: serverName, Version: serverVersion},
	}
	return s.respond(&Response{JSONRPC: JSONRPCVersion, ID: req.ID, Result: result})
}

// handleToolsCall runs one tool. Name/protocol violations are JSON-RPC errors;
// operational outcomes (success or IsError) come back as results.
func (s *Server) handleToolsCall(ctx context.Context, req *Request) error {
	var params CallToolParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return s.respond(&Response{JSONRPC: JSONRPCVersion, ID: req.ID, Error: &RPCError{
			Code:    CodeInvalidParams,
			Message: "invalid tools/call params: " + err.Error(),
		}})
	}
	if params.Name == "" {
		return s.respond(&Response{JSONRPC: JSONRPCVersion, ID: req.ID, Error: &RPCError{
			Code:    CodeInvalidParams,
			Message: "missing tool name",
		}})
	}

	tool, ok := s.reg.Tool(params.Name)
	if !ok {
		// The registry already encodes scope: a tool the process's scope may
		// not use never exists in it, so this reply is both "unknown" and
		// "denied" at once — and a read-only process provably cannot reach a
		// control tool even by name.
		return s.respond(&Response{JSONRPC: JSONRPCVersion, ID: req.ID, Error: &RPCError{
			Code:    CodeInvalidParams,
			Message: "unknown tool: " + params.Name,
		}})
	}

	args := map[string]any{}
	if len(params.Arguments) > 0 && !emptyObject(params.Arguments) {
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			return s.respond(&Response{JSONRPC: JSONRPCVersion, ID: req.ID, Error: &RPCError{
				Code:    CodeInvalidParams,
				Message: "invalid tool arguments: " + err.Error(),
			}})
		}
	}

	result, err := tool.Handler(ctx, args)
	switch {
	case err == nil && result == nil:
		result = &CallToolResult{}
	case errors.Is(err, ErrInvalidParams):
		return s.respond(&Response{JSONRPC: JSONRPCVersion, ID: req.ID, Error: &RPCError{
			Code:    CodeInvalidParams,
			Message: fmt.Sprintf("invalid arguments for tool %q: %v", params.Name, err),
		}})
	case err != nil:
		// Unexpected handler failure: surface as an IsError result rather than
		// losing it to a protocol error.
		result = resultError("tool %q failed: %v", params.Name, err)
	}
	return s.respond(&Response{JSONRPC: JSONRPCVersion, ID: req.ID, Result: result})
}

// emptyObject reports whether the raw JSON is "{}" or whitespace.
func emptyObject(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("{}"))
}

// respond sends one response line.
func (s *Server) respond(resp *Response) error {
	return s.codec.WriteJSON(resp)
}
