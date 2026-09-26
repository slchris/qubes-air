package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/audit"
	"github.com/slchris/qubes-air/console/internal/desktopaccess"
	"github.com/slchris/qubes-air/console/internal/middleware"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/service"
	"github.com/slchris/qubes-air/console/internal/xpra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Credentials the desktop fixture accepts.
const (
	mcpToken      = "mcp-secret"
	otherMCPToken = "other-mcp-secret"
	readerToken   = "reader-secret"
	zoneToken     = "zone-b-secret"
)

var desktopTokens = []middleware.Token{
	{Name: "mcp", Value: mcpToken, Scope: middleware.ScopeControl},
	{Name: "other-mcp", Value: otherMCPToken, Scope: middleware.ScopeControl},
	{Name: "reader", Value: readerToken, Scope: middleware.ScopeReadOnly},
	{Name: "zone-b", Value: zoneToken, Scope: middleware.ScopeControl, Zones: []string{"zone-b"}},
}

// desktopQubes is a lookup and a zone resolver over a fixed set of qubes.
type desktopQubes struct {
	mu    sync.Mutex
	qubes map[string]*models.Qube
}

func newDesktopQubes(qubes ...*models.Qube) *desktopQubes {
	d := &desktopQubes{qubes: map[string]*models.Qube{}}
	for _, q := range qubes {
		d.qubes[q.ID] = q
	}
	return d
}

func (d *desktopQubes) GetByID(_ context.Context, id string) (*models.Qube, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	q, ok := d.qubes[id]
	if !ok {
		return nil, service.ErrQubeNotFound
	}
	copied := *q
	return &copied, nil
}

func (d *desktopQubes) set(id string, change func(*models.Qube)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	change(d.qubes[id])
}

func (d *desktopQubes) ZoneOfQube(_ context.Context, id string) (string, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	q, ok := d.qubes[id]
	if !ok {
		return "", false, nil
	}
	return q.ZoneID, true, nil
}

func (d *desktopQubes) ZoneOfJob(context.Context, string) (string, bool, error) {
	return "", false, nil
}

func healthyQube(id, zone string) *models.Qube {
	return &models.Qube{ID: id, Name: id, ZoneID: zone, Status: models.QubeStatusRunning, AgentHealth: models.AgentHealthHealthy}
}

// fakeFrames is a DesktopFrameCapturer that returns frame or err.
type fakeFrames struct {
	unavailable bool
	frame       []byte
	err         error
	block       chan struct{}
	mu          sync.Mutex
	captured    []string
}

func (f *fakeFrames) DesktopFrameAvailable() bool { return !f.unavailable }

func (f *fakeFrames) CaptureDesktopFrame(ctx context.Context, qubeID string, lease service.DesktopLease) (xpra.Screenshot, error) {
	f.mu.Lock()
	f.captured = append(f.captured, qubeID)
	f.mu.Unlock()
	if f.block != nil {
		select {
		case <-f.block:
		case <-lease.Done():
			return xpra.Screenshot{}, service.ErrDesktopGrantEnded
		case <-ctx.Done():
			return xpra.Screenshot{}, ctx.Err()
		}
	}
	if f.err != nil {
		return xpra.Screenshot{}, f.err
	}
	return xpra.Screenshot{PNG: f.frame}, nil
}

func (f *fakeFrames) calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.captured...)
}

// syncBuffer is an io.Writer the audit recorder and the test can share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type desktopFixture struct {
	router   *gin.Engine
	sessions *middleware.SessionStore
	store    *desktopaccess.Store
	qubes    *desktopQubes
	frames   *fakeFrames
	handler  *DesktopAccessHandler
	audit    *syncBuffer
}

// newDesktopFixture builds the /api/v1 chain the console runs (Audit,
// ScopedAuth, RequireControl, RequireZones) around the desktop routes.
func newDesktopFixture(t *testing.T, tokens []middleware.Token, frames *fakeFrames) *desktopFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &desktopFixture{
		sessions: middleware.NewSessionStore(time.Hour),
		store:    desktopaccess.NewStore(),
		qubes:    newDesktopQubes(healthyQube("qube-1", "zone-a"), healthyQube("qube-2", "zone-a"), healthyQube("qube-b", "zone-b")),
		frames:   frames,
		audit:    &syncBuffer{},
	}
	var capturer service.DesktopFrameCapturer
	if frames != nil {
		capturer = frames
	}
	f.handler = NewDesktopAccessHandler(f.store, f.qubes, capturer)
	f.router = gin.New()
	v1 := f.router.Group("/api/v1")
	v1.Use(middleware.Audit(audit.NewRecorder(f.audit)),
		middleware.ScopedAuth("", tokens, f.sessions),
		middleware.RequireControl(),
		middleware.RequireZones(f.qubes))
	f.handler.RegisterRoutes(v1)
	return f
}

func (f *desktopFixture) session(t *testing.T, subject string, scope middleware.Scope, zones ...string) *http.Cookie {
	t.Helper()
	s, err := f.sessions.Create(subject, scope, zones)
	require.NoError(t, err)
	return &http.Cookie{Name: middleware.SessionCookieName, Value: s.ID}
}

// call serves one in-process request from a loopback peer.
func (f *desktopFixture) call(method, path, body string, prepare func(*http.Request)) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.RemoteAddr = "127.0.0.1:40000"
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if prepare != nil {
		prepare(req)
	}
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func bearer(token string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
}

func operator(cookie *http.Cookie, withAction bool) func(*http.Request) {
	return func(r *http.Request) {
		r.AddCookie(cookie)
		if withAction {
			r.Header.Set(desktopActionHeader, desktopActionValue)
		}
	}
}

type mcpResult struct {
	status  int
	header  http.Header
	body    string
	payload desktopGrantResponse
}

// startMCPRequest issues the MCP grant request against a live server, the way
// the MCP process does, and returns its eventual result.
func startMCPRequest(t *testing.T, server *httptest.Server, token, qubeID, body string) <-chan mcpResult {
	t.Helper()
	out := make(chan mcpResult, 1)
	go func() {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/qubes/"+qubeID+"/desktop-access", strings.NewReader(body))
		if err != nil {
			out <- mcpResult{status: -1, body: err.Error()}
			return
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := server.Client().Do(req)
		if err != nil {
			out <- mcpResult{status: -1, body: err.Error()}
			return
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		result := mcpResult{status: resp.StatusCode, header: resp.Header, body: string(raw)}
		_ = json.Unmarshal(raw, &result.payload)
		out <- result
	}()
	return out
}

func awaitMCP(t *testing.T, ch <-chan mcpResult) mcpResult {
	t.Helper()
	select {
	case r := <-ch:
		require.NotEqual(t, -1, r.status, r.body)
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("the MCP request never returned")
		return mcpResult{}
	}
}

// queued waits until the operator queue holds n entries and returns them.
func (f *desktopFixture) queued(t *testing.T, cookie *http.Cookie, n int) []desktopaccess.Request {
	t.Helper()
	var requests []desktopaccess.Request
	require.Eventually(t, func() bool {
		rec := f.call(http.MethodGet, "/api/v1/desktop-access", "", operator(cookie, false))
		if rec.Code != http.StatusOK {
			return false
		}
		var body struct {
			Requests []desktopaccess.Request `json:"requests"`
		}
		if json.Unmarshal(rec.Body.Bytes(), &body) != nil {
			return false
		}
		requests = body.Requests
		return len(requests) == n
	}, 3*time.Second, 5*time.Millisecond)
	return requests
}

// grantFor runs the whole consent flow for token on qube and returns the
// grant the MCP caller received.
func (f *desktopFixture) grantFor(t *testing.T, token, qubeID string) string {
	t.Helper()
	server := httptest.NewServer(f.router)
	defer server.Close()
	cookie := f.session(t, "console-admin", middleware.ScopeControl)
	pending := startMCPRequest(t, server, token, qubeID, `{"operation":"frame"}`)
	queue := f.queued(t, cookie, 1)
	require.Equal(t, http.StatusOK, f.call(http.MethodPost, "/api/v1/desktop-access/"+queue[0].ID+"/approve", "", operator(cookie, true)).Code)
	result := awaitMCP(t, pending)
	require.Equal(t, http.StatusOK, result.status, result.body)
	return result.payload.Grant
}

func TestDesktopAccessGrantReachesOnlyTheWaitingMCPCaller(t *testing.T) {
	f := newDesktopFixture(t, desktopTokens, &fakeFrames{frame: []byte("png")})
	server := httptest.NewServer(f.router)
	defer server.Close()
	cookie := f.session(t, "console-admin", middleware.ScopeControl)

	pending := startMCPRequest(t, server, mcpToken, "qube-1", `{"operation":"frame"}`)
	queue := f.queued(t, cookie, 1)
	assert.Equal(t, "mcp", queue[0].Subject)
	assert.Equal(t, "qube-1", queue[0].QubeID)
	assert.Equal(t, desktopaccess.StatePending, queue[0].State)

	approved := f.call(http.MethodPost, "/api/v1/desktop-access/"+queue[0].ID+"/approve", "", operator(cookie, true))
	require.Equal(t, http.StatusOK, approved.Code, approved.Body.String())
	assert.Equal(t, "no-store", approved.Header().Get("Cache-Control"))
	assert.NotContains(t, approved.Body.String(), "grant")

	result := awaitMCP(t, pending)
	require.Equal(t, http.StatusOK, result.status, result.body)
	assert.Len(t, result.payload.Grant, 43)
	assert.Equal(t, "qube-1", result.payload.QubeID)
	assert.Equal(t, desktopaccess.OperationFrame, result.payload.Operation)
	assert.Equal(t, "no-store", result.header.Get("Cache-Control"))
	assert.NotContains(t, approved.Body.String(), result.payload.Grant)

	listing := f.call(http.MethodGet, "/api/v1/desktop-access", "", operator(cookie, false))
	assert.Equal(t, "no-store", listing.Header().Get("Cache-Control"))
	assert.NotContains(t, listing.Body.String(), result.payload.Grant)
	assert.NotContains(t, f.audit.String(), result.payload.Grant, "the audit trail must not carry grant material")
}

func TestDesktopFrameSpendsTheGrantOnce(t *testing.T) {
	frames := &fakeFrames{frame: []byte("\x89PNG test frame")}
	f := newDesktopFixture(t, desktopTokens, frames)
	grant := f.grantFor(t, mcpToken, "qube-1")

	body := `{"grant":"` + grant + `"}`
	first := f.call(http.MethodPost, "/api/v1/qubes/qube-1/desktop-frame", body, bearer(mcpToken))
	require.Equal(t, http.StatusOK, first.Code, first.Body.String())
	assert.Equal(t, "image/png", first.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", first.Header().Get("Cache-Control"))
	assert.Equal(t, "\x89PNG test frame", first.Body.String())

	replay := f.call(http.MethodPost, "/api/v1/qubes/qube-1/desktop-frame", body, bearer(mcpToken))
	assert.Equal(t, http.StatusForbidden, replay.Code, "a spent grant must not be replayable")
	assert.Equal(t, []string{"qube-1"}, frames.calls())

	auditLog := f.audit.String()
	assert.NotContains(t, auditLog, grant, "the audit trail must not carry grant material")
	assert.Contains(t, auditLog, `"route":"/api/v1/qubes/:id/desktop-frame"`)
	assert.Contains(t, auditLog, `"object":"qube-1"`)
	assert.Contains(t, auditLog, `"subject":"mcp"`)
}

// A grant is bound to the subject it was issued to and the qube it names;
// presented for another qube or by another credential it is refused, and it
// is burnt so the leaked secret cannot be used afterwards.
func TestDesktopFrameRefusesAGrantReplayedElsewhere(t *testing.T) {
	for _, tc := range []struct {
		name, token, qube string
	}{
		{"other qube", mcpToken, "qube-2"},
		{"other subject", otherMCPToken, "qube-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frames := &fakeFrames{frame: []byte("png")}
			f := newDesktopFixture(t, desktopTokens, frames)
			grant := f.grantFor(t, mcpToken, "qube-1")
			body := `{"grant":"` + grant + `"}`

			misused := f.call(http.MethodPost, "/api/v1/qubes/"+tc.qube+"/desktop-frame", body, bearer(tc.token))
			assert.Equal(t, http.StatusForbidden, misused.Code)
			assert.NotContains(t, misused.Body.String(), grant)
			rightful := f.call(http.MethodPost, "/api/v1/qubes/qube-1/desktop-frame", body, bearer(mcpToken))
			assert.Equal(t, http.StatusForbidden, rightful.Code, "a grant presented elsewhere is burnt")
			assert.Empty(t, frames.calls(), "no capture may run on a misbound grant")
		})
	}
}

func TestDesktopAccessRefusesTheInputOperation(t *testing.T) {
	f := newDesktopFixture(t, desktopTokens, &fakeFrames{})
	rec := f.call(http.MethodPost, "/api/v1/qubes/qube-1/desktop-access", `{"operation":"input"}`, bearer(mcpToken))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "only the frame operation is supported")
	assert.Empty(t, f.store.OperatorQueue(), "an operator must not be asked to approve input")
}

// With no dedicated desktop transport the console refuses before asking an
// operator, and a frame call does not consume its grant.
func TestDesktopAccessFailsClosedWithoutADesktopTransport(t *testing.T) {
	for _, tc := range []struct {
		name   string
		frames *fakeFrames
	}{
		{"no capturer", nil},
		{"capturer without a streamer", &fakeFrames{unavailable: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDesktopFixture(t, desktopTokens, tc.frames)
			rec := f.call(http.MethodPost, "/api/v1/qubes/qube-1/desktop-access", `{"operation":"frame"}`, bearer(mcpToken))
			assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
			assert.Empty(t, f.store.OperatorQueue())

			frame := f.call(http.MethodPost, "/api/v1/qubes/qube-1/desktop-frame", `{"grant":"x"}`, bearer(mcpToken))
			assert.Equal(t, http.StatusServiceUnavailable, frame.Code)
		})
	}
}

// A zone-scoped credential addressing a qube in another zone gets the same
// 404 as a missing qube, and the audit line records the denial.
func TestDesktopAccessHonoursZoneScope(t *testing.T) {
	f := newDesktopFixture(t, desktopTokens, &fakeFrames{frame: []byte("png")})
	for _, path := range []string{"/api/v1/qubes/qube-1/desktop-access", "/api/v1/qubes/qube-1/desktop-frame"} {
		rec := f.call(http.MethodPost, path, `{"operation":"frame"}`, bearer(zoneToken))
		assert.Equal(t, http.StatusNotFound, rec.Code, path)
	}
	assert.Empty(t, f.store.OperatorQueue())
	auditLog := f.audit.String()
	assert.Contains(t, auditLog, `"outcome":"denied"`)
	assert.Contains(t, auditLog, `"subject":"zone-b"`)

	// The approval queue is a fleet view: a zone-scoped session is refused.
	zoneSession := f.session(t, "zone-operator", middleware.ScopeControl, "zone-b")
	assert.Equal(t, http.StatusForbidden, f.call(http.MethodGet, "/api/v1/desktop-access", "", operator(zoneSession, false)).Code)
}

func TestDesktopAccessRequiresControlScope(t *testing.T) {
	f := newDesktopFixture(t, desktopTokens, &fakeFrames{})
	assert.Equal(t, http.StatusForbidden,
		f.call(http.MethodPost, "/api/v1/qubes/qube-1/desktop-access", `{"operation":"frame"}`, bearer(readerToken)).Code)
	assert.Equal(t, http.StatusForbidden,
		f.call(http.MethodPost, "/api/v1/qubes/qube-1/desktop-frame", `{"grant":"x"}`, bearer(readerToken)).Code)
	reader := f.session(t, "auditor", middleware.ScopeReadOnly)
	assert.Equal(t, http.StatusForbidden, f.call(http.MethodGet, "/api/v1/desktop-access", "", operator(reader, false)).Code)
	assert.Empty(t, f.store.OperatorQueue())
}

// Only a person at the Console may decide: a Bearer token, even a fleet-wide
// control one, cannot list, approve, deny or stop.
func TestDesktopApprovalRefusesBearerTokens(t *testing.T) {
	f := newDesktopFixture(t, desktopTokens, &fakeFrames{})
	pending, err := f.store.Request("mcp", "qube-1", desktopaccess.OperationFrame)
	require.NoError(t, err)
	withBearerAndAction := func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+mcpToken)
		r.Header.Set(desktopActionHeader, desktopActionValue)
	}
	assert.Equal(t, http.StatusForbidden, f.call(http.MethodGet, "/api/v1/desktop-access", "", bearer(mcpToken)).Code)
	for _, action := range []string{"approve", "deny", "stop"} {
		rec := f.call(http.MethodPost, "/api/v1/desktop-access/"+pending.ID+"/"+action, "", withBearerAndAction)
		assert.Equal(t, http.StatusForbidden, rec.Code, action)
	}
	_, still := f.store.PendingRequest(pending.ID)
	assert.True(t, still, "a refused decision must not change the request")
	assert.Contains(t, f.audit.String(), `"outcome":"denied"`)
}

// With authentication disabled there is no browser session, so nobody can
// approve; requesting a grant needs a named control credential too.
func TestDesktopAccessIsImpossibleWithAuthenticationDisabled(t *testing.T) {
	f := newDesktopFixture(t, nil, &fakeFrames{})
	assert.Equal(t, http.StatusForbidden,
		f.call(http.MethodPost, "/api/v1/qubes/qube-1/desktop-access", `{"operation":"frame"}`, nil).Code)
	pending, err := f.store.Request("mcp", "qube-1", desktopaccess.OperationFrame)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, f.call(http.MethodGet, "/api/v1/desktop-access", "", nil).Code)
	withAction := func(r *http.Request) { r.Header.Set(desktopActionHeader, desktopActionValue) }
	assert.Equal(t, http.StatusForbidden,
		f.call(http.MethodPost, "/api/v1/desktop-access/"+pending.ID+"/approve", "", withAction).Code)
	_, still := f.store.PendingRequest(pending.ID)
	assert.True(t, still)
}

func TestDesktopApprovalRequiresTheActionHeader(t *testing.T) {
	f := newDesktopFixture(t, desktopTokens, &fakeFrames{})
	cookie := f.session(t, "console-admin", middleware.ScopeControl)
	pending, err := f.store.Request("mcp", "qube-1", desktopaccess.OperationFrame)
	require.NoError(t, err)
	for _, action := range []string{"approve", "deny", "stop"} {
		rec := f.call(http.MethodPost, "/api/v1/desktop-access/"+pending.ID+"/"+action, "", operator(cookie, false))
		assert.Equal(t, http.StatusForbidden, rec.Code, action)
	}
	_, still := f.store.PendingRequest(pending.ID)
	assert.True(t, still)
}

// The handler refuses a zone-restricted session itself, not only through the
// fleet-route middleware.
func TestDesktopApprovalRefusesZoneRestrictedSessionsWithoutTheMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sessions := middleware.NewSessionStore(time.Hour)
	store := desktopaccess.NewStore()
	router := gin.New()
	v1 := router.Group("/api/v1")
	v1.Use(middleware.ScopedAuth("", desktopTokens, sessions), middleware.RequireControl())
	NewDesktopAccessHandler(store, newDesktopQubes(healthyQube("qube-1", "zone-a")), &fakeFrames{}).RegisterRoutes(v1)
	s, err := sessions.Create("zone-operator", middleware.ScopeControl, []string{"zone-a"})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/desktop-access", nil)
	req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: s.ID})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestDesktopApprovalRechecksTheQube(t *testing.T) {
	f := newDesktopFixture(t, desktopTokens, &fakeFrames{})
	cookie := f.session(t, "console-admin", middleware.ScopeControl)
	pending, err := f.store.Request("mcp", "qube-1", desktopaccess.OperationFrame)
	require.NoError(t, err)
	f.qubes.set("qube-1", func(q *models.Qube) { q.Status = models.QubeStatusStopped })

	rec := f.call(http.MethodPost, "/api/v1/desktop-access/"+pending.ID+"/approve", "", operator(cookie, true))
	assert.Equal(t, http.StatusPreconditionFailed, rec.Code)
	_, still := f.store.PendingRequest(pending.ID)
	assert.True(t, still, "a failed recheck must not consume the request")

	missing := f.call(http.MethodPost, "/api/v1/desktop-access/no-such-request/approve", "", operator(cookie, true))
	assert.Equal(t, http.StatusNotFound, missing.Code)
}

func TestDesktopDenyReleasesTheMCPCallerWithoutAGrant(t *testing.T) {
	f := newDesktopFixture(t, desktopTokens, &fakeFrames{})
	server := httptest.NewServer(f.router)
	defer server.Close()
	cookie := f.session(t, "console-admin", middleware.ScopeControl)
	pending := startMCPRequest(t, server, mcpToken, "qube-1", `{"operation":"frame"}`)
	queue := f.queued(t, cookie, 1)

	denied := f.call(http.MethodPost, "/api/v1/desktop-access/"+queue[0].ID+"/deny", "", operator(cookie, true))
	require.Equal(t, http.StatusOK, denied.Code)
	assert.NotContains(t, denied.Body.String(), "grant")
	result := awaitMCP(t, pending)
	assert.Equal(t, http.StatusForbidden, result.status)
	assert.Contains(t, result.body, "denied")
	assert.Empty(t, result.payload.Grant)
}

// Stop through the API ends an active capture: the frame call returns 403
// and no frame.
func TestDesktopStopEndsAnActiveCapture(t *testing.T) {
	frames := &fakeFrames{frame: []byte("png"), block: make(chan struct{})}
	f := newDesktopFixture(t, desktopTokens, frames)
	grant := f.grantFor(t, mcpToken, "qube-1")
	cookie := f.session(t, "console-admin", middleware.ScopeControl)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- f.call(http.MethodPost, "/api/v1/qubes/qube-1/desktop-frame", `{"grant":"`+grant+`"}`, bearer(mcpToken))
	}()
	queue := f.queued(t, cookie, 1)
	require.Eventually(t, func() bool { return len(frames.calls()) == 1 }, 3*time.Second, time.Millisecond)
	assert.Equal(t, desktopaccess.StateActive, queue[0].State)

	stop := f.call(http.MethodPost, "/api/v1/desktop-access/"+queue[0].ID+"/stop", "", operator(cookie, true))
	require.Equal(t, http.StatusNoContent, stop.Code)
	select {
	case rec := <-done:
		assert.Equal(t, http.StatusForbidden, rec.Code)
		assert.NotContains(t, rec.Body.String(), "png")
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not end the capture")
	}
}

func TestDesktopGrantRequestRefusesRemotePlainHTTP(t *testing.T) {
	f := newDesktopFixture(t, desktopTokens, &fakeFrames{})
	remote := func(r *http.Request) {
		r.RemoteAddr = "198.51.100.20:1234"
		r.Header.Set("Authorization", "Bearer "+mcpToken)
	}
	assert.Equal(t, http.StatusForbidden,
		f.call(http.MethodPost, "/api/v1/qubes/qube-1/desktop-access", `{"operation":"frame"}`, remote).Code)
	assert.Equal(t, http.StatusForbidden,
		f.call(http.MethodPost, "/api/v1/qubes/qube-1/desktop-frame", `{"grant":"x"}`, remote).Code)
	assert.Empty(t, f.store.OperatorQueue())
}

func TestDesktopAccessAcceptsOnlyStrictSmallBodies(t *testing.T) {
	f := newDesktopFixture(t, desktopTokens, &fakeFrames{})
	for _, tc := range []struct {
		name, path, body string
		want             int
	}{
		{"unknown field", "desktop-access", `{"operation":"frame","target":"127.0.0.1"}`, http.StatusBadRequest},
		{"two values", "desktop-access", `{"operation":"frame"}{"operation":"frame"}`, http.StatusBadRequest},
		{"not JSON", "desktop-access", `operation=frame`, http.StatusBadRequest},
		{"unknown operation", "desktop-access", `{"operation":"exec"}`, http.StatusBadRequest},
		{"oversized", "desktop-access", `{"operation":"frame","pad":"` + strings.Repeat("x", desktopAccessBodyLimit) + `"}`, http.StatusRequestEntityTooLarge},
		{"missing grant", "desktop-frame", `{}`, http.StatusBadRequest},
		{"frame unknown field", "desktop-frame", `{"grant":"x","qube":"qube-2"}`, http.StatusBadRequest},
		{"oversized grant", "desktop-frame", `{"grant":"` + strings.Repeat("x", desktopAccessBodyLimit) + `"}`, http.StatusRequestEntityTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := f.call(http.MethodPost, "/api/v1/qubes/qube-1/"+tc.path, tc.body, bearer(mcpToken))
			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}
	assert.Empty(t, f.store.OperatorQueue())
}

func TestDesktopAccessRequiresARunningHealthyQube(t *testing.T) {
	f := newDesktopFixture(t, desktopTokens, &fakeFrames{})
	f.qubes.set("qube-1", func(q *models.Qube) { q.AgentHealth = models.AgentHealthUnreachable })
	rec := f.call(http.MethodPost, "/api/v1/qubes/qube-1/desktop-access", `{"operation":"frame"}`, bearer(mcpToken))
	assert.Equal(t, http.StatusPreconditionFailed, rec.Code)
	assert.Empty(t, f.store.OperatorQueue())
}

func TestDesktopAccessCapsPendingRequests(t *testing.T) {
	f := newDesktopFixture(t, desktopTokens, &fakeFrames{})
	for range desktopaccess.MaxPendingRequests {
		_, err := f.store.Request("mcp", "qube-1", desktopaccess.OperationFrame)
		require.NoError(t, err)
	}
	rec := f.call(http.MethodPost, "/api/v1/qubes/qube-1/desktop-access", `{"operation":"frame"}`, bearer(mcpToken))
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
}

// Captures over the cap are refused at once and do not spend their grant.
func TestDesktopFrameCapsConcurrentCaptures(t *testing.T) {
	frames := &fakeFrames{frame: []byte("png")}
	f := newDesktopFixture(t, desktopTokens, frames)
	grant := f.grantFor(t, mcpToken, "qube-1")
	releases := make([]func(), 0, MaxConcurrentDesktopFrames)
	for range MaxConcurrentDesktopFrames {
		release, ok := f.handler.reserveFrameSlot()
		require.True(t, ok)
		releases = append(releases, release)
	}
	body := `{"grant":"` + grant + `"}`
	busy := f.call(http.MethodPost, "/api/v1/qubes/qube-1/desktop-frame", body, bearer(mcpToken))
	assert.Equal(t, http.StatusServiceUnavailable, busy.Code)
	assert.Equal(t, "1", busy.Header().Get("Retry-After"))
	assert.Empty(t, frames.calls())

	for _, release := range releases {
		release()
	}
	ok := f.call(http.MethodPost, "/api/v1/qubes/qube-1/desktop-frame", body, bearer(mcpToken))
	assert.Equal(t, http.StatusOK, ok.Code, "the refused call must not have spent the grant")
}

// The approval wait outlives the server's timeouts: the handler sets its own
// write deadline, so the MCP caller still receives the grant. ReadTimeout is
// short too, as in runServer, to show it does not end the wait either.
func TestDesktopAccessWaitOutlivesTheServerWriteTimeout(t *testing.T) {
	f := newDesktopFixture(t, desktopTokens, &fakeFrames{})
	server := httptest.NewUnstartedServer(f.router)
	server.Config.WriteTimeout = 100 * time.Millisecond
	server.Config.ReadTimeout = 100 * time.Millisecond
	server.Start()
	defer server.Close()
	cookie := f.session(t, "console-admin", middleware.ScopeControl)

	pending := startMCPRequest(t, server, mcpToken, "qube-1", `{"operation":"frame"}`)
	queue := f.queued(t, cookie, 1)
	time.Sleep(3 * server.Config.WriteTimeout)
	require.Equal(t, http.StatusOK, f.call(http.MethodPost, "/api/v1/desktop-access/"+queue[0].ID+"/approve", "", operator(cookie, true)).Code)
	result := awaitMCP(t, pending)
	assert.Equal(t, http.StatusOK, result.status, result.body)
	assert.Len(t, result.payload.Grant, 43)
}

// Every capture failure is answered with a fixed message; the detail (remote
// text, host names) stays in the log.
func TestDesktopFrameMapsFailuresToFixedMessages(t *testing.T) {
	detail := "agent 192.0.2.7 said secret-detail"
	for _, tc := range []struct {
		err  error
		want int
	}{
		{fmt.Errorf("%w: %s", service.ErrDesktopUnreachable, detail), http.StatusBadGateway},
		{fmt.Errorf("capture: %w (%s)", xpra.ErrScreenshotUnsupported, detail), http.StatusBadGateway},
		{fmt.Errorf("capture: %w (%s)", xpra.ErrEmptyScreenshot, detail), http.StatusConflict},
		{fmt.Errorf("capture: %w (%s)", xpra.ErrAuthenticationRequired, detail), http.StatusBadGateway},
		{fmt.Errorf("capture: %w (%s)", xpra.ErrUnsupportedUpgrade, detail), http.StatusBadGateway},
		{fmt.Errorf("capture: %w: %s", xpra.ErrScreenshotSession, detail), http.StatusBadGateway},
		{fmt.Errorf("capture: %w (%s)", context.DeadlineExceeded, detail), http.StatusGatewayTimeout},
		{fmt.Errorf("%w: %s", service.ErrDesktopNotReady, detail), http.StatusPreconditionFailed},
		{fmt.Errorf("%w: %s", service.ErrDesktopTransportUnavailable, detail), http.StatusServiceUnavailable},
		{fmt.Errorf("%w: %s", service.ErrDesktopGrantEnded, detail), http.StatusForbidden},
		{fmt.Errorf("%w: %s", service.ErrZoneDisconnected, detail), http.StatusPreconditionFailed},
		{errors.New(detail), http.StatusInternalServerError},
	} {
		t.Run(tc.err.Error(), func(t *testing.T) {
			f := newDesktopFixture(t, desktopTokens, &fakeFrames{err: tc.err})
			grant := f.grantFor(t, mcpToken, "qube-1")
			rec := f.call(http.MethodPost, "/api/v1/qubes/qube-1/desktop-frame", `{"grant":"`+grant+`"}`, bearer(mcpToken))
			assert.Equal(t, tc.want, rec.Code)
			assert.NotContains(t, rec.Body.String(), "secret-detail")
			assert.NotContains(t, rec.Body.String(), "192.0.2.7")
			assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
		})
	}
}
