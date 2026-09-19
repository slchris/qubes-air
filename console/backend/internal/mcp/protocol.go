// Package mcp implements a Model Context Protocol server over stdio
// (docs/mcp-design.md). The server speaks JSON-RPC 2.0, one message per
// newline-terminated line, and acts as a loopback HTTP client for the Console
// API — it never adds a privilege path of its own.
//
// Scope is enforced here and inside the API: MCP placement (`--scope`) decides
// which tools exist in tools/list AND which tools a direct tools/call may
// invoke, so a read-only process cannot reach control tools even by calling
// them by name.
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

const (
	// JSONRPCVersion is the protocol version advertised on every message.
	JSONRPCVersion = "2.0"
	// ProtocolVersion is the MCP protocol version this server implements.
	ProtocolVersion = "2024-11-05"
)

// JSON-RPC 2.0 error codes (RFC 8628 §5.1).
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

const (
	// DefaultMaxMessageSize caps a single newline-delimited JSON message.
	// Enforced before the whole line is buffered so a hostile peer cannot grow
	// memory without bound.
	DefaultMaxMessageSize = 4 << 20 // 4 MiB

	serverName    = "qubes-air-mcp"
	serverVersion = "0.1.0"

	// MethodInitialize negotiates protocol version and capabilities.
	MethodInitialize = "initialize"
	// MethodInitializedNotification signals the client finished initialization.
	MethodInitializedNotification = "notifications/initialized"
	// MethodPing keeps the client-server round trip alive.
	MethodPing = "ping"
	// MethodToolsList lists the tools visible at this scope.
	MethodToolsList = "tools/list"
	// MethodToolsCall invokes one tool.
	MethodToolsCall = "tools/call"
)

// ErrLineTooLong is returned when a single stdio line exceeds the configured
// cap. The stream cannot be re-synchronized after this, so the serve loop
// terminates; callers must never try to keep reading.
var ErrLineTooLong = errors.New("message exceeds maximum single-line size")

// Request is a JSON-RPC 2.0 request or notification.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Notification reports whether this message carries no request id and must
// never receive a response. A literal "null" id counts as absent too.
func (r *Request) Notification() bool {
	id := bytes.TrimSpace(r.ID)
	return len(id) == 0 || bytes.Equal(id, []byte("null"))
}

// Response is a JSON-RPC 2.0 response. It always carries an id (null for a
// response to a message whose id could not be parsed).
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError is a JSON-RPC error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// ClientInfo identifies the connecting LLM client.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeParams is the payload of `initialize`.
type InitializeParams struct {
	ProtocolVersion string          `json:"protocolVersion"`
	Capabilities    json.RawMessage `json:"capabilities,omitempty"`
	ClientInfo      *ClientInfo     `json:"clientInfo,omitempty"`
}

// Implementation identifies this MCP server.
type Implementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Capabilities is the server's capability advertisement.
type Capabilities struct {
	Tools *ToolsCapability `json:"tools"`
}

// ToolsCapability describes what the tools resource supports.
type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// InitializeResult is the response to `initialize`.
type InitializeResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    Capabilities   `json:"capabilities"`
	ServerInfo      Implementation `json:"serverInfo"`
}

// ToolInfo is the public tool description returned by tools/list.
type ToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ListToolsResult is the response to tools/list.
type ListToolsResult struct {
	Tools []ToolInfo `json:"tools"`
}

// CallToolParams is the payload of tools/call.
type CallToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// ContentItem is one text block of a tool result.
type ContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// CallToolResult is the response to tools/call. IsError is true when the tool
// ran but failed (bad upstream response, timeout, not implemented); JSON-RPC
// errors are reserved for protocol violations.
type CallToolResult struct {
	Content []ContentItem `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// Codec reads and writes newline-delimited JSON messages on a stdio pair.
//
// The line cap exists to keep a malicious or broken peer from forcing unbounded
// allocation: Read never buffers past the cap, and a line that exceeds it fails
// the decode loop instead of being grown indefinitely.
type Codec struct {
	r       *bufio.Reader
	w       io.Writer
	maxLine int
}

// NewCodec creates a Codec over a stdio reader/writer pair.
func NewCodec(r io.Reader, w io.Writer) *Codec {
	return &Codec{
		r:       bufio.NewReader(r),
		w:       w,
		maxLine: DefaultMaxMessageSize,
	}
}

// Read returns the next raw JSON line, without its terminating newline.
//
// A line longer than the cap returns ErrLineTooLong. The underlying stream is
// then abandoned — the boundary has been lost — so the caller must stop
// decoding rather than read again.
func (c *Codec) Read() ([]byte, error) {
	var line []byte
	for {
		chunk, err := c.r.ReadSlice('\n')
		if len(chunk) > 0 {
			line = append(line, chunk...)
		}
		switch {
		case err == nil:
			line = bytes.TrimRight(line, "\r\n")
			if len(line) > c.maxLine {
				return nil, ErrLineTooLong
			}
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			if len(line) > c.maxLine {
				return nil, ErrLineTooLong
			}
			continue
		default:
			// io.EOF or a read failure before any delimiter: hand back whatever
			// partial line was accumulated, plus the error.
			if len(line) > c.maxLine {
				return nil, ErrLineTooLong
			}
			return line, err
		}
	}
}

// Write sends one newline-terminated message.
func (c *Codec) Write(msg []byte) error {
	if len(msg) > c.maxLine {
		return ErrLineTooLong
	}
	if _, err := c.w.Write(msg); err != nil {
		return err
	}
	_, err := io.WriteString(c.w, "\n")
	return err
}

// WriteJSON marshals v and sends it as one line.
func (c *Codec) WriteJSON(v any) error {
	msg, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.Write(msg)
}
