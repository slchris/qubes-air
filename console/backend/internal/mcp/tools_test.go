package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixture wires a Registry to a real httptest upstream that shares the MCP
// package's Client transport, so every tools test exercises the full path:
// stdio line → server dispatch → allowlist → HTTP → mapped result.
type fixture struct {
	mux *http.ServeMux
	reg *Registry
}

// recorder captures every request an upstream mux receives.
type recorder struct {
	mu     sync.Mutex
	reqs   []*http.Request
	bodies []string
}

func (r *recorder) handle(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.reqs = append(r.reqs, req)
		r.bodies = append(r.bodies, string(b))
		r.mu.Unlock()
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reqs)
}

func (r *recorder) last() *http.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.reqs) == 0 {
		return nil
	}
	return r.reqs[len(r.reqs)-1]
}

func (r *recorder) lastBody() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.bodies) == 0 {
		return ""
	}
	return r.bodies[len(r.bodies)-1]
}

// newFixture starts an upstream and builds a registry over it.
func newFixture(t *testing.T, scope Scope, enableCU bool, opts ...ClientOption) *fixture {
	t.Helper()
	mux := http.NewServeMux()
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	client := NewClient(ts.URL, testToken, opts...)
	reg := NewRegistry(scope, enableCU, client)
	return &fixture{mux: mux, reg: reg}
}

// call drives one tools/call request through the serve loop.
func (f *fixture) call(t *testing.T, name, argsJSON string) Response {
	t.Helper()
	params := `"name":` + jsonQuote(name)
	if argsJSON != "" {
		params += `,"arguments":` + argsJSON
	} else {
		params += `,"arguments":{}`
	}
	out := serveLines(t, f.reg, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{`+params+`}}`)
	resps := parseResponses(t, out)
	requireOne(t, resps)
	return resps[0]
}

func (f *fixture) list(t *testing.T) Response {
	out := serveLines(t, f.reg, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	resps := parseResponses(t, out)
	requireOne(t, resps)
	return resps[0]
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// resultText extracts the first text content item of a successful result.
func resultText(t *testing.T, r Response) string {
	t.Helper()
	if r.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %+v", r.Error)
	}
	m, ok := r.Result.(map[string]any)
	if !ok {
		t.Fatalf("result = %T", r.Result)
	}
	content, ok := m["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("content = %v", m["content"])
	}
	item, ok := content[0].(map[string]any)
	if !ok {
		t.Fatalf("content item = %T", content[0])
	}
	return item["text"].(string)
}

func isErrorResult(r Response) bool {
	if r.Error != nil {
		return true
	}
	m, ok := r.Result.(map[string]any)
	if !ok {
		return false
	}
	b, _ := m["isError"].(bool)
	return b
}

func TestToolsCall_ReadToolForwardsAuthAndFilters(t *testing.T) {
	f := newFixture(t, ScopeReadOnly, false)
	rec := &recorder{}
	f.mux.HandleFunc("/api/v1/qubes", rec.handle(http.StatusOK, `{"qubes":[],"total":0}`))

	resp := f.call(t, "qube_list", `{"status":"running","type":"app","zone_id":"z-1"}`)
	text := resultText(t, resp)
	if !strings.Contains(text, `"total":0`) {
		t.Fatalf("body = %q", text)
	}
	last := rec.last()
	if last == nil {
		t.Fatal("upstream was never reached")
	}
	if q := last.URL.Query().Get("status"); q != "running" {
		t.Fatalf("status filter = %q", q)
	}
	if q := last.URL.Query().Get("type"); q != "app" {
		t.Fatalf("type filter = %q", q)
	}
	if q := last.URL.Query().Get("zone_id"); q != "z-1" {
		t.Fatalf("zone_id filter = %q", q)
	}
	if auth := last.Header.Get("Authorization"); auth != "Bearer "+testToken {
		t.Fatalf("Authorization = %q", auth)
	}
}

func TestToolsCall_GetToolBuildsThePath(t *testing.T) {
	f := newFixture(t, ScopeReadOnly, false)
	rec := &recorder{}
	f.mux.HandleFunc("/api/v1/qubes/", rec.handle(http.StatusOK, `{"name":"q1"}`))

	resp := f.call(t, "qube_get", `{"id":"11111111-2222-3333-4444-555555555555"}`)
	if text := resultText(t, resp); !strings.Contains(text, `"name":"q1"`) {
		t.Fatalf("body = %q", text)
	}
	if last := rec.last(); last == nil || last.URL.Path != "/api/v1/qubes/11111111-2222-3333-4444-555555555555" {
		t.Fatalf("path = %v", last)
	}
}

func TestToolsCall_PathTraversalIDIsRejected(t *testing.T) {
	f := newFixture(t, ScopeReadOnly, false)
	rec := &recorder{}
	f.mux.HandleFunc("/api/v1/", rec.handle(http.StatusOK, `{}`))

	for _, bad := range []string{"../start", "../../etc/passwd", "a/b", "id with space", "a\nb", strings.Repeat("a", 200)} {
		resp := f.call(t, "zone_get", `{"id":`+jsonQuote(bad)+`}`)
		if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
			t.Fatalf("id %q: want -32602, got %+v", bad, resp.Error)
		}
	}
	if rec.count() != 0 {
		t.Fatalf("upstream was reached %d times for rejected ids", rec.count())
	}
}

func TestToolsCall_NonStringIDIsRejected(t *testing.T) {
	f := newFixture(t, ScopeReadOnly, false)
	f.mux.HandleFunc("/api/v1/", (&recorder{}).handle(http.StatusOK, `{}`))

	resp := f.call(t, "qube_get", `{"id":12345}`)
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("want -32602, got %+v", resp.Error)
	}
}

func TestToolsCall_Upstream404IsPassedThrough(t *testing.T) {
	f := newFixture(t, ScopeReadOnly, false)
	f.mux.HandleFunc("/api/v1/qubes/", (&recorder{}).handle(http.StatusNotFound, `{"error":"qube not found"}`))

	resp := f.call(t, "qube_get", `{"id":"00000000-0000-0000-0000-000000000000"}`)
	text := resultText(t, resp)
	if !isErrorResult(resp) {
		t.Fatalf("expected a failed tool result")
	}
	if !strings.Contains(text, "404") || !strings.Contains(text, "qube not found") {
		t.Fatalf("pass-through text = %q", text)
	}
}

func TestToolsCall_Upstream503IsPassedThrough(t *testing.T) {
	f := newFixture(t, ScopeReadOnly, false)
	f.mux.HandleFunc("/api/v1/zones", (&recorder{}).handle(http.StatusServiceUnavailable, `{"error":"cluster unreachable"}`))

	resp := f.call(t, "zone_list", "")
	text := resultText(t, resp)
	if !isErrorResult(resp) || !strings.Contains(text, "503") || !strings.Contains(text, "cluster unreachable") {
		t.Fatalf("text = %q isError=%v", text, isErrorResult(resp))
	}
}

func TestToolsCall_UpstreamTimeoutSurfacesAsFailedResult(t *testing.T) {
	f := newFixture(t, ScopeReadOnly, false, WithTimeout(60*time.Millisecond))
	f.mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})

	resp := f.call(t, "status", "")
	text := resultText(t, resp)
	if !isErrorResult(resp) {
		t.Fatalf("expected a failed tool result, got %q", text)
	}
	if !strings.Contains(text, "console API request failed") {
		t.Fatalf("text = %q", text)
	}
	if strings.Contains(text, testToken) {
		t.Fatalf("timeout result leaked the token: %q", text)
	}
}

func TestToolsCall_ControlNamesRejectedUnderReadOnly(t *testing.T) {
	f := newFixture(t, ScopeReadOnly, false)
	rec := &recorder{}
	f.mux.HandleFunc("/api/v1/qubes/", rec.handle(http.StatusAccepted, `{}`))
	f.mux.HandleFunc("/api/v1/monitoring/alerts/", rec.handle(http.StatusOK, `{}`))

	// The registry encodes scope: control tools simply do not exist in a
	// read-only process, so calling one by name is an unknown-tool rejection
	// and the upstream is never touched.
	for _, name := range []string{"qube_start", "qube_stop", "qube_delete", "qube_create", "alert_acknowledge"} {
		resp := f.call(t, name, `{"id":"00000000-0000-0000-0000-000000000000"}`)
		if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
			t.Fatalf("%s: want -32602 unknown tool, got %+v", name, resp.Error)
		}
		if !strings.Contains(resp.Error.Message, name) {
			t.Fatalf("%s: message = %q", name, resp.Error.Message)
		}
	}
	if rec.count() != 0 {
		t.Fatalf("upstream reached %d times despite read-only scope", rec.count())
	}
}

func TestToolsCall_ControlActionsRunUnderControlScope(t *testing.T) {
	f := newFixture(t, ScopeControl, false)
	rec := &recorder{}
	f.mux.HandleFunc("/api/v1/qubes/", rec.handle(http.StatusAccepted, `{"job_id":"j1"}`))
	f.mux.HandleFunc("/api/v1/monitoring/alerts/", rec.handle(http.StatusOK, `{"acknowledged":true}`))
	id := "00000000-0000-0000-0000-000000000000"

	cases := []struct {
		tool, method, path string
	}{
		{"qube_start", http.MethodPost, "/api/v1/qubes/" + id + "/start"},
		{"qube_stop", http.MethodPost, "/api/v1/qubes/" + id + "/stop"},
		{"qube_delete", http.MethodDelete, "/api/v1/qubes/" + id},
		{"alert_acknowledge", http.MethodPost, "/api/v1/monitoring/alerts/" + id + "/acknowledge"},
	}
	for _, c := range cases {
		before := rec.count()
		resp := f.call(t, c.tool, `{"id":`+jsonQuote(id)+`}`)
		if resp.Error != nil {
			t.Fatalf("%s: %+v", c.tool, resp.Error)
		}
		last := rec.last()
		if before+1 != rec.count() || last.URL.Path != c.path || last.Method != c.method {
			t.Fatalf("%s: count=%d path=%s method=%s", c.tool, rec.count(), last.URL.Path, last.Method)
		}
	}
}

func TestToolsCall_CreateRelaysArgumentsAsBody(t *testing.T) {
	f := newFixture(t, ScopeControl, false)
	rec := &recorder{}
	f.mux.HandleFunc("/api/v1/qubes", rec.handle(http.StatusCreated, `{"id":"new-1"}`))

	resp := f.call(t, "qube_create", `{"name":"web","type":"app","zone_id":"z-1","spec":{"vcpu":2,"memory":2048}}`)
	if resp.Error != nil {
		t.Fatalf("%+v", resp.Error)
	}
	last := rec.last()
	if last == nil || last.Method != http.MethodPost || last.URL.Path != "/api/v1/qubes" {
		t.Fatalf("request = %v %v", last, last)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(rec.lastBody()), &body); err != nil {
		t.Fatalf("body json: %v (%q)", err, rec.lastBody())
	}
	if body["name"] != "web" || body["type"] != "app" || body["zone_id"] != "z-1" {
		t.Fatalf("body = %v", body)
	}
	spec, _ := body["spec"].(map[string]any)
	if spec["vcpu"].(float64) != 2 {
		t.Fatalf("spec = %v", spec)
	}
	if text := resultText(t, resp); !strings.Contains(text, `"id":"new-1"`) {
		t.Fatalf("result = %q", text)
	}
}

// TestToolsCall_DesktopAppsList — the real appmenus action: GET against the
// qube-scoped endpoint, menu text returned verbatim.
func TestToolsCall_DesktopAppsList(t *testing.T) {
	f := newFixture(t, ScopeControl, true)
	rec := &recorder{}
	f.mux.HandleFunc("/api/v1/qubes/", rec.handle(http.StatusOK,
		"firefox.desktop:Name=Firefox\norg.gnome.Terminal:Name=Terminal\n"))

	resp := f.call(t, "desktop_apps_list", `{"id":"qube-1"}`)
	if resp.Error != nil {
		t.Fatalf("%+v", resp.Error)
	}
	last := rec.last()
	if last.Method != http.MethodGet || last.URL.Path != "/api/v1/qubes/qube-1/appmenus" {
		t.Fatalf("request = %v %s", last.Method, last.URL.Path)
	}
	if text := resultText(t, resp); !strings.Contains(text, "firefox.desktop") {
		t.Fatalf("result = %q", text)
	}
}

func TestToolsCall_DesktopAppsList_MissingID(t *testing.T) {
	f := newFixture(t, ScopeControl, true)
	f.mux.HandleFunc("/api/v1/", (&recorder{}).handle(http.StatusOK, `{}`))

	resp := f.call(t, "desktop_apps_list", `{}`)
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("want -32602, got %+v", resp.Error)
	}
}

// TestToolsCall_DesktopAppLaunch — the app id becomes the URL path segment (and
// behind the Console API, the qrexec service argument).
func TestToolsCall_DesktopAppLaunch(t *testing.T) {
	f := newFixture(t, ScopeControl, true)
	rec := &recorder{}
	f.mux.HandleFunc("/api/v1/qubes/", rec.handle(http.StatusOK,
		"qubes.StartApp: launched 'firefox.desktop' on :100\n"))

	resp := f.call(t, "desktop_app_launch", `{"id":"qube-1","app":"firefox.desktop"}`)
	if resp.Error != nil {
		t.Fatalf("%+v", resp.Error)
	}
	last := rec.last()
	if last.Method != http.MethodPost || last.URL.Path != "/api/v1/qubes/qube-1/apps/firefox.desktop/launch" {
		t.Fatalf("request = %v %s", last.Method, last.URL.Path)
	}
	if text := resultText(t, resp); !strings.Contains(text, "launched") {
		t.Fatalf("result = %q", text)
	}
}

// TestToolsCall_DesktopAppLaunch_InvalidApp — an app id that fails the
// allowlist is refused with -32602 BEFORE any upstream request is made: the
// upstream recorder proves zero calls. Slashes, spaces and separators are the
// attacks that would otherwise redirect the request or forge a qrexec service
// argument.
func TestToolsCall_DesktopAppLaunch_InvalidApp(t *testing.T) {
	f := newFixture(t, ScopeControl, true)
	rec := &recorder{}
	f.mux.HandleFunc("/api/v1/", rec.handle(http.StatusOK, `{}`))

	badApps := []string{
		"",                       // empty
		"../firefox",             // path traversal
		"a/b",                    // separator
		"a b",                    // space
		"a\nb",                   // newline
		strings.Repeat("a", 129), // over-long
	}
	for _, app := range badApps {
		args := `{"id":"qube-1","app":` + jsonQuote(app) + `}`
		resp := f.call(t, "desktop_app_launch", args)
		if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
			t.Fatalf("app %q: want -32602, got %+v", app, resp.Error)
		}
	}
	// Missing required argument entirely.
	resp := f.call(t, "desktop_app_launch", `{"id":"qube-1"}`)
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("missing app: want -32602, got %+v", resp.Error)
	}
	if got := rec.count(); got != 0 {
		t.Fatalf("invalid app ids made %d upstream calls, want 0", got)
	}
}

// TestToolsCall_DesktopAppLaunch_UpstreamError — a transport/upstream failure
// (here a 502) surfaces as a failed tool result, not a JSON-RPC error.
func TestToolsCall_DesktopAppLaunch_UpstreamError(t *testing.T) {
	f := newFixture(t, ScopeControl, true)
	f.mux.HandleFunc("/api/v1/", (&recorder{}).handle(http.StatusBadGateway, `qube is unreachable over the transport`))

	resp := f.call(t, "desktop_app_launch", `{"id":"qube-1","app":"firefox.desktop"}`)
	if !isErrorResult(resp) {
		t.Fatalf("want failed result, got %+v", resp.Result)
	}
}

func TestToolsCall_ComputerUseStubsRefuseLoudly(t *testing.T) {
	f := newFixture(t, ScopeControl, true)

	for _, name := range []string{"desktop_frame_get", "desktop_input_send"} {
		resp := f.call(t, name, `{"id":"qube-1"}`)
		text := resultText(t, resp)
		if !isErrorResult(resp) || !strings.Contains(text, "not implemented") {
			t.Fatalf("%s: text=%q isError=%v", name, text, isErrorResult(resp))
		}
	}
}

func TestToolsList_NamesDifferByScope(t *testing.T) {
	read := newFixture(t, ScopeReadOnly, true)
	r := read.list(t)
	readNames := map[string]bool{}
	for _, item := range r.Result.(map[string]any)["tools"].([]any) {
		readNames[item.(map[string]any)["name"].(string)] = true
	}
	if readNames["qube_list"] != true {
		t.Fatalf("read-only list must contain read tools")
	}
	if readNames["qube_start"] {
		t.Fatalf("read-only list leaked control tools even with --enable-computer-use")
	}
	if readNames["desktop_frame_get"] {
		t.Fatalf("read-only list leaked computer-use tools even with --enable-computer-use")
	}

	control := newFixture(t, ScopeControl, true)
	r = control.list(t)
	controlNames := map[string]bool{}
	for _, item := range r.Result.(map[string]any)["tools"].([]any) {
		controlNames[item.(map[string]any)["name"].(string)] = true
	}
	if !controlNames["qube_start"] || !controlNames["desktop_frame_get"] {
		t.Fatal("control list must contain control and computer-use tools")
	}
}

func TestToolsCall_MissingRequiredIDIsInvalidParams(t *testing.T) {
	f := newFixture(t, ScopeControl, false)
	f.mux.HandleFunc("/api/v1/", (&recorder{}).handle(http.StatusOK, `{}`))

	resp := f.call(t, "qube_start", `{}`)
	if resp.Error == nil || resp.Error.Code != CodeInvalidParams {
		t.Fatalf("want -32602, got %+v", resp.Error)
	}
}
