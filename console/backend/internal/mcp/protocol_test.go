package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// serveLines runs the serve loop over in-memory stdio pre-filled with the
// given request lines and returns the raw response output.
func serveLines(t *testing.T, reg *Registry, inputs ...string) *bytes.Buffer {
	t.Helper()
	in := bytes.NewBufferString(strings.Join(inputs, "\n") + "\n")
	out := new(bytes.Buffer)
	codec := NewCodec(in, out)
	srv := NewServer(codec, reg)
	if err := srv.Serve(context.Background()); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	return out
}

// parseResponses splits the newline-delimited output into JSON-RPC responses.
func parseResponses(t *testing.T, out *bytes.Buffer) []Response {
	t.Helper()
	var resps []Response
	for _, line := range bytes.Split(bytes.TrimRight(out.Bytes(), "\n"), []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var r Response
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatalf("unmarshal response %q: %v", line, err)
		}
		resps = append(resps, r)
	}
	return resps
}

// dummyClient is a client whose base is a dead loopback port; protocol-only
// tests never trigger an API call, so a connection failure would be a bug.
func dummyClient() *Client {
	return NewClient("http://127.0.0.1:1", "never-used-token")
}

func readOnlyReg(t *testing.T) *Registry {
	t.Helper()
	return NewRegistry(ScopeReadOnly, false, dummyClient())
}

func controlReg(t *testing.T, enableComputerUse bool) *Registry {
	t.Helper()
	return NewRegistry(ScopeControl, enableComputerUse, dummyClient())
}

func TestCodec_RoundTrip(t *testing.T) {
	var out bytes.Buffer
	c := NewCodec(strings.NewReader("{\"a\":1}\n{\"b\":2}\n"), &out)

	first, err := c.Read()
	if err != nil {
		t.Fatalf("Read 1: %v", err)
	}
	if string(first) != `{"a":1}` {
		t.Fatalf("first = %q", first)
	}
	second, err := c.Read()
	if err != nil {
		t.Fatalf("Read 2: %v", err)
	}
	if string(second) != `{"b":2}` {
		t.Fatalf("second = %q", second)
	}
	if _, err := c.Read(); err != io.EOF {
		t.Fatalf("expected EOF, got %v", err)
	}

	if err := c.Write([]byte(`{"c":3}`)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := out.String(); got != `{"c":3}`+"\n" {
		t.Fatalf("written = %q", got)
	}
}

func TestCodec_CRLFIsATerminator(t *testing.T) {
	c := NewCodec(strings.NewReader("{\"a\":1}\r\n"), io.Discard)
	line, err := c.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(line) != `{"a":1}` {
		t.Fatalf("line = %q (trailing CR must be trimmed)", line)
	}
}

func TestCodec_LinesAtTheCapAreAccepted(t *testing.T) {
	payload := strings.Repeat("x", DefaultMaxMessageSize)
	in := payload + "\n"
	c := NewCodec(strings.NewReader(in), io.Discard)
	line, err := c.Read()
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(line) != DefaultMaxMessageSize {
		t.Fatalf("line len = %d, want %d", len(line), DefaultMaxMessageSize)
	}
}

func TestCodec_OverCapLineIsRejectedWithoutBuffering(t *testing.T) {
	// One byte more than the default cap: Read must fail, never return it.
	payload := strings.Repeat("x", DefaultMaxMessageSize+1)
	c := NewCodec(strings.NewReader(payload+"\n"), io.Discard)
	if _, err := c.Read(); err != ErrLineTooLong {
		t.Fatalf("expected ErrLineTooLong, got %v", err)
	}
}

func TestCodec_SmallCustomCap(t *testing.T) {
	c := NewCodec(strings.NewReader("abcdef\n"), io.Discard)
	c.maxLine = 5
	if _, err := c.Read(); err != ErrLineTooLong {
		t.Fatalf("expected ErrLineTooLong, got %v", err)
	}

	c = NewCodec(strings.NewReader("abcde\n"), io.Discard)
	c.maxLine = 5
	if line, err := c.Read(); err != nil || string(line) != "abcde" {
		t.Fatalf("line=%q err=%v", line, err)
	}
}

func TestServer_Initialize(t *testing.T) {
	reg := readOnlyReg(t)
	out := serveLines(t, reg,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"test-client","version":"1.0"}}}`)

	resps := parseResponses(t, out)
	requireOne(t, resps)
	if resps[0].Error != nil {
		t.Fatalf("unexpected error: %+v", resps[0].Error)
	}
	if string(resps[0].ID) != "1" {
		t.Fatalf("id = %s", resps[0].ID)
	}
	result, ok := resps[0].Result.(map[string]any)
	if !ok {
		t.Fatalf("result type %T", resps[0].Result)
	}
	if result["protocolVersion"] != ProtocolVersion {
		t.Fatalf("protocolVersion = %v", result["protocolVersion"])
	}
	if caps, ok := result["capabilities"].(map[string]any); !ok || caps["tools"] == nil {
		t.Fatalf("capabilities = %v", result["capabilities"])
	}
	info, ok := result["serverInfo"].(map[string]any)
	if !ok || info["name"] != "qubes-air-mcp" {
		t.Fatalf("serverInfo = %v", result["serverInfo"])
	}
}

func TestServer_InitializedNotificationGetsNoResponse(t *testing.T) {
	out := serveLines(t, readOnlyReg(t),
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	resps := parseResponses(t, out)
	if len(resps) != 1 {
		t.Fatalf("expected exactly one response (initialize), got %d: %s", len(resps), out.Bytes())
	}
}

func TestServer_Ping(t *testing.T) {
	out := serveLines(t, readOnlyReg(t), `{"jsonrpc":"2.0","id":7,"method":"ping"}`)
	resps := parseResponses(t, out)
	requireOne(t, resps)
	if resps[0].Error != nil {
		t.Fatalf("unexpected error: %+v", resps[0].Error)
	}
	if result, ok := resps[0].Result.(map[string]any); !ok || len(result) != 0 {
		t.Fatalf("ping result = %v", resps[0].Result)
	}
}

func TestServer_UnknownRequestMethod(t *testing.T) {
	out := serveLines(t, readOnlyReg(t), `{"jsonrpc":"2.0","id":9,"method":"tools/read"}`)
	resps := parseResponses(t, out)
	requireOne(t, resps)
	if resps[0].Error == nil || resps[0].Error.Code != CodeMethodNotFound {
		t.Fatalf("want -32601, got %+v", resps[0].Error)
	}
}

func TestServer_UnknownNotificationIsSilentlyDropped(t *testing.T) {
	out := serveLines(t, readOnlyReg(t), `{"jsonrpc":"2.0","method":"bogus/notification"}`)
	if out.Len() != 0 {
		t.Fatalf("notifications must never be answered, got %q", out.Bytes())
	}
}

func TestServer_InvalidJSONIsParseError(t *testing.T) {
	out := serveLines(t, readOnlyReg(t), `{this is not json`)
	resps := parseResponses(t, out)
	requireOne(t, resps)
	if resps[0].Error == nil || resps[0].Error.Code != CodeParseError {
		t.Fatalf("want -32700, got %+v", resps[0].Error)
	}
	if string(resps[0].ID) != "null" {
		t.Fatalf("parse-error id = %s, want null", resps[0].ID)
	}
}

func TestServer_InvalidRequestVersion(t *testing.T) {
	out := serveLines(t, readOnlyReg(t), `{"jsonrpc":"1.0","id":3,"method":"ping"}`)
	resps := parseResponses(t, out)
	requireOne(t, resps)
	if resps[0].Error == nil || resps[0].Error.Code != CodeInvalidRequest {
		t.Fatalf("want -32600, got %+v", resps[0].Error)
	}
}

func TestServer_MissingMethodIsInvalidRequest(t *testing.T) {
	out := serveLines(t, readOnlyReg(t), `{"jsonrpc":"2.0","id":3}`)
	resps := parseResponses(t, out)
	requireOne(t, resps)
	if resps[0].Error == nil || resps[0].Error.Code != CodeInvalidRequest {
		t.Fatalf("want -32600, got %+v", resps[0].Error)
	}
}

func TestServer_ToolsCallUnknownTool(t *testing.T) {
	out := serveLines(t, readOnlyReg(t),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"does_not_exist","arguments":{}}}`)
	resps := parseResponses(t, out)
	requireOne(t, resps)
	if resps[0].Error == nil || resps[0].Error.Code != CodeInvalidParams {
		t.Fatalf("want -32602, got %+v", resps[0].Error)
	}
	if !strings.Contains(resps[0].Error.Message, "does_not_exist") {
		t.Fatalf("message = %q", resps[0].Error.Message)
	}
}

func TestServer_ToolsCallMissingName(t *testing.T) {
	out := serveLines(t, readOnlyReg(t),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"arguments":{}}}`)
	resps := parseResponses(t, out)
	requireOne(t, resps)
	if resps[0].Error == nil || resps[0].Error.Code != CodeInvalidParams {
		t.Fatalf("want -32602, got %+v", resps[0].Error)
	}
}

func TestServer_ToolsCallMalformedArguments(t *testing.T) {
	out := serveLines(t, readOnlyReg(t),
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"status","arguments":"not-object"}}`)
	resps := parseResponses(t, out)
	requireOne(t, resps)
	if resps[0].Error == nil || resps[0].Error.Code != CodeInvalidParams {
		t.Fatalf("want -32602, got %+v", resps[0].Error)
	}
}

func TestServer_OverLongLineTerminatesTheLoop(t *testing.T) {
	in := bytes.NewBufferString(strings.Repeat("x", DefaultMaxMessageSize+1) + "\n")
	out := new(bytes.Buffer)
	codec := NewCodec(in, out)
	srv := NewServer(codec, readOnlyReg(t))
	if err := srv.Serve(context.Background()); err != ErrLineTooLong {
		t.Fatalf("expected ErrLineTooLong, got %v", err)
	}
	resps := parseResponses(t, out)
	requireOne(t, resps)
	if resps[0].Error == nil || resps[0].Error.Code != CodeParseError {
		t.Fatalf("want a parse-error reply before teardown, got %+v", resps[0].Error)
	}
}

func requireOne(t *testing.T, resps []Response) {
	t.Helper()
	if len(resps) != 1 {
		t.Fatalf("expected exactly one response, got %d", len(resps))
	}
}
