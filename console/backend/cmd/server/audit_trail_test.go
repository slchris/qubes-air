package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/slchris/qubes-air/console/internal/audit"
	"github.com/slchris/qubes-air/console/internal/config"
	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/middleware"
	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openAuditDB opens a real console database for the persisted-trail tests.
func openAuditDB(t *testing.T) (*database.DB, string) {
	t.Helper()
	cfg := database.DefaultConfig()
	cfg.DSN = filepath.Join(t.TempDir(), "audit.db")
	db, err := database.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db, cfg.DSN
}

// persistingAPI is auditedAPI with the recorder persisting through a real
// repository, as startAuditTrail wires it. Stop the returned persister before
// reading the table: that is what drains its queue.
func persistingAPI(t *testing.T, tune func(*config.Config), caps repository.AuditCaps, pcfg audit.PersisterConfig) (
	*gin.Engine, *bytes.Buffer, *database.DB, *audit.Persister, *middleware.SessionStore) {
	t.Helper()
	db, _ := openAuditDB(t)
	persister := audit.NewPersister(repository.NewAuditRepository(db, caps), pcfg)
	persister.Start()
	t.Cleanup(persister.Stop)
	r, buf, sessions := auditedAPIWith(t, tune, mountStubRoutes, persister)
	return r, buf, db, persister, sessions
}

// storedLine reads the row for requestID back in the JSON line's own shape
// (key for key, JSON-decoded types), with its time as Unix nanoseconds.
func storedLine(t *testing.T, db *database.DB, requestID string) (map[string]any, int64) {
	t.Helper()
	var (
		occurred, status, latency                     int64
		authenticated, authDisabled, truncated        bool
		subject, source, method, route, object, scope string
		outcome, rid                                  string
	)
	require.NoError(t, db.DB().QueryRowContext(context.Background(), `
		SELECT occurred_at, request_id, authenticated, auth_disabled, subject, source, method, route, object,
		       object_truncated, status, outcome, latency_ms, zone_scope
		FROM audit_events WHERE request_id = ?`, requestID).Scan(
		&occurred, &rid, &authenticated, &authDisabled, &subject, &source, &method, &route, &object,
		&truncated, &status, &outcome, &latency, &scope), "no stored row for request %s", requestID)
	return map[string]any{
		"request_id": rid, "authenticated": authenticated, "auth_disabled": authDisabled, "subject": subject,
		"source": source, "method": method, "route": route, "object": object, "object_truncated": truncated,
		"status": float64(status), "outcome": outcome, "latency_ms": float64(latency), "zone_scope": scope,
	}, occurred
}

// assertRowEqualsLine checks a persisted row against the JSON line it was
// logged as: every field, and the line's time to the nanosecond.
func assertRowEqualsLine(t *testing.T, db *database.DB, line map[string]any) {
	t.Helper()
	requestID, _ := line["request_id"].(string)
	row, occurred := storedLine(t, db, requestID)
	stamp, err := time.Parse(time.RFC3339Nano, fmt.Sprint(line["time"]))
	require.NoError(t, err)
	assert.Equal(t, stamp.UnixNano(), occurred, "occurred_at must be the line's time")
	logged := map[string]any{}
	for key, value := range line {
		switch key {
		case "time", "level", "msg":
		default:
			logged[key] = value
		}
	}
	assert.Equal(t, logged, row, "the stored row must be the logged line")
}

// dumpAuditTable renders every stored value, so a leak is found by substring.
func dumpAuditTable(t *testing.T, db *database.DB) string {
	t.Helper()
	rows, err := db.DB().QueryContext(context.Background(), `SELECT * FROM audit_events`)
	require.NoError(t, err)
	defer rows.Close()
	cols, err := rows.Columns()
	require.NoError(t, err)
	var out strings.Builder
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		require.NoError(t, rows.Scan(ptrs...))
		fmt.Fprintln(&out, values...)
	}
	require.NoError(t, rows.Err())
	return out.String()
}

// Every way a request is rendered reaches the table as exactly the line that
// was logged, with the request ID the caller was given, and no credential
// material in any column.
func TestAPIAuditPersistsExactlyTheLoggedLine(t *testing.T) {
	const (
		guess   = "login-guess-SECRET-c3d4"
		unknown = "guessed-bearer-SECRET-e5f6"
		cookie  = "stale-session-SECRET-a7b8"
	)
	noAuth := func(cfg *config.Config) { cfg.Auth.APIToken = ""; cfg.Auth.Tokens = nil }
	cases := []struct {
		name, path, body string
		tune             func(*config.Config)
		mutate           func(*http.Request)
		status           int
	}{
		{name: "authenticated success", path: "/api/v1/qubes/q-a/start", mutate: bearer(zoneTokenValue), status: http.StatusAccepted},
		{name: "read-only denial", path: "/api/v1/qubes/q-a/start", mutate: bearer(auditorTokenValue), status: http.StatusForbidden},
		{name: "unknown bearer", path: "/api/v1/qubes/q-a/start", mutate: bearer(unknown), status: http.StatusUnauthorized},
		{name: "stale session cookie", path: "/api/v1/qubes/q-a/start",
			mutate: withHeader("Cookie", middleware.SessionCookieName+"="+cookie), status: http.StatusUnauthorized},
		{name: "failed login", path: "/api/v1/session", body: `{"token":"` + guess + `"}`, status: http.StatusUnauthorized},
		{name: "truncated object", path: "/api/v1/qubes/" + strings.Repeat("%FF", 500) + "/start", status: http.StatusUnauthorized},
		{name: "auth disabled", path: "/api/v1/qubes/q-b/start", tune: noAuth, status: http.StatusAccepted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, buf, db, persister, _ := persistingAPI(t, tc.tune, repository.DefaultAuditCaps(), audit.PersisterConfig{})

			w := apiRequest(r, http.MethodPost, tc.path, tc.body, tc.mutate)
			require.Equal(t, tc.status, w.Code)
			persister.Stop()

			line := onlyAuditLine(t, buf)
			assertRowEqualsLine(t, db, line)
			assert.Equal(t, w.Result().Header.Get(middleware.RequestIDHeader), line["request_id"],
				"the row must carry the request ID the caller was given")
			stored := dumpAuditTable(t, db)
			for _, secret := range []string{guess, unknown, cookie, adminTokenValue, auditorTokenValue, zoneTokenValue,
				"Bearer", "Authorization", middleware.SessionCookieName} {
				assert.NotContains(t, stored, secret, "credential material reached the audit table")
			}
		})
	}
}

// stalledStore blocks every write until released (or its deadline), so a test
// can hold the persister mid-write.
type stalledStore struct{ release chan struct{} }

func (s stalledStore) AppendEvent(ctx context.Context, _ audit.Event) error {
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s stalledStore) AppendSuppression(ctx context.Context, _ audit.Suppression) error {
	return s.AppendEvent(ctx, audit.Event{})
}

// failingStore refuses every write.
type failingStore struct{}

func (failingStore) AppendEvent(context.Context, audit.Event) error {
	return errors.New("disk I/O error")
}

func (failingStore) AppendSuppression(context.Context, audit.Suppression) error {
	return errors.New("disk I/O error")
}

// syncLog collects a persister's log output.
type syncLog struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (l *syncLog) Logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(&l.buf, format+"\n", args...)
}

func (l *syncLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// A store that fails or stalls must not change or hold up the response: the
// caller gets the same status and request ID, the line is still written, and
// the loss is logged and counted instead.
func TestAPIAuditStoreFailureDoesNotChangeTheResponse(t *testing.T) {
	baseline, _, _ := auditedAPI(t, nil)
	want := apiRequest(baseline, http.MethodPost, "/api/v1/qubes/q-a/start", "", bearer(adminTokenValue))

	t.Run("failing", func(t *testing.T) {
		logs := &syncLog{}
		persister := audit.NewPersister(failingStore{}, audit.PersisterConfig{Logf: logs.Logf})
		persister.Start()
		defer persister.Stop()
		r, buf, _ := auditedAPIWith(t, nil, mountStubRoutes, persister)

		w := apiRequest(r, http.MethodPost, "/api/v1/qubes/q-a/start", "", bearer(adminTokenValue))

		assert.Equal(t, want.Code, w.Code)
		assert.Equal(t, want.Body.String(), w.Body.String())
		line := onlyAuditLine(t, buf)
		assertAuditFields(t, line, w, map[string]any{"outcome": audit.OutcomeSuccess, "subject": "api_token"})
		persister.Stop()
		assert.EqualValues(t, 1, persister.Stats().Failed)
		assert.True(t, persister.Stats().Degraded)
		assert.Contains(t, logs.String(), "request_id="+fmt.Sprint(line["request_id"]))
		assert.Contains(t, logs.String(), "disk I/O error")
	})

	t.Run("stalled", func(t *testing.T) {
		store := stalledStore{release: make(chan struct{})}
		persister := audit.NewPersister(store, audit.PersisterConfig{WriteTimeout: time.Minute, Logf: (&syncLog{}).Logf})
		persister.Start()
		r, buf, _ := auditedAPIWith(t, nil, mountStubRoutes, persister)

		// Both requests are answered while the store holds the first write:
		// reaching the assertions at all is the proof ServeHTTP did not wait.
		first := apiRequest(r, http.MethodPost, "/api/v1/qubes/q-a/start", "", bearer(adminTokenValue))
		second := apiRequest(r, http.MethodPost, "/api/v1/qubes/q-a/start", "", bearer(adminTokenValue))

		assert.Equal(t, want.Code, first.Code)
		assert.Equal(t, want.Code, second.Code)
		assert.Len(t, decodeAuditLines(t, buf), 2)
		close(store.release)
		persister.Stop()
		assert.EqualValues(t, 2, persister.Stats().Persisted)
	})
}

// An unauthenticated flood through the real middleware chain cannot grow the
// table past the sampled cap or push out authenticated rows, whether the
// budget or the cap is what stops it; every request still has its line.
func TestAPIAuditFloodIsBoundedInTheTable(t *testing.T) {
	fixed := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name          string
		caps          repository.AuditCaps
		budget        audit.Budget
		wantSampled   int
		wantSuppessed int64
	}{
		// The default budget: 20 stored, the other 280 in one summary row.
		{name: "budget", caps: repository.DefaultAuditCaps(), wantSampled: 21, wantSuppessed: 280},
		// A budget that admits everything: the repository's cap stops it.
		{name: "cap", caps: repository.AuditCaps{Full: 100, Sampled: 25}, budget: audit.Budget{Burst: 10_000, Every: time.Second},
			wantSampled: 25},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, buf, db, persister, _ := persistingAPI(t, nil, tc.caps,
				audit.PersisterConfig{Budget: tc.budget, Now: func() time.Time { return fixed }, Logf: (&syncLog{}).Logf})

			operatorIDs := make([]string, 0, 10)
			for range 10 {
				w := apiRequest(r, http.MethodPost, "/api/v1/qubes/q-a/start", "", bearer(adminTokenValue))
				operatorIDs = append(operatorIDs, w.Result().Header.Get(middleware.RequestIDHeader))
			}
			for i := range 300 {
				apiRequest(r, http.MethodPost, fmt.Sprintf("/api/v1/qubes/flood-%d/start", i), "", nil)
			}
			persister.Stop()

			assert.Len(t, decodeAuditLines(t, buf), 310, "every request keeps its log line")
			for _, id := range operatorIDs {
				assert.Equal(t, 1, countAuditRows(t, db, "request_id = ?", id), "an authenticated row was lost")
			}
			assert.Equal(t, 10, countAuditRows(t, db, "persist_class = 'full'"))
			assert.Equal(t, tc.wantSampled, countAuditRows(t, db, "persist_class = 'sampled'"))
			var suppressed int64
			require.NoError(t, db.DB().QueryRowContext(context.Background(),
				`SELECT COALESCE(SUM(suppressed), 0) FROM audit_events`).Scan(&suppressed))
			assert.Equal(t, tc.wantSuppessed, suppressed)
		})
	}
}

// A credential flooding past its rate limit cannot push another subject's
// records out: rate limiting runs after authentication, so every 429 is an
// authenticated event, and those are budgeted like anonymous failures rather
// than stored in full. Only the requests the limiter let through (the burst)
// reach the full class, whatever the token's scope.
func TestAPIAuditTokenFloodCannotEvictAnotherSubjectsRows(t *testing.T) {
	fixed := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	const burst = 5
	cases := []struct {
		name, token, path, subject string
		passed                     int
	}{
		{name: "read-only token", token: auditorTokenValue, path: "/api/v1/qubes/q-a/start", subject: "auditor", passed: http.StatusForbidden},
		{name: "zone-scoped token", token: zoneTokenValue, path: "/api/v1/qubes/q-a/start", subject: "zone-a-control", passed: http.StatusAccepted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _, db, persister, _ := persistingAPI(t, func(cfg *config.Config) {
				cfg.Server.RateLimitPerSec = 0.001
				cfg.Server.RateLimitBurst = burst
			}, repository.AuditCaps{Full: 4 * burst, Sampled: 25},
				audit.PersisterConfig{Now: func() time.Time { return fixed }, Logf: (&syncLog{}).Logf})

			adminIDs := make([]string, 0, burst)
			for range burst {
				w := apiRequest(r, http.MethodPost, "/api/v1/qubes/q-a/start", "", bearer(adminTokenValue))
				require.Equal(t, http.StatusAccepted, w.Code)
				adminIDs = append(adminIDs, w.Result().Header.Get(middleware.RequestIDHeader))
			}
			throttled := 0
			for range 1000 {
				w := apiRequest(r, http.MethodPost, tc.path, "", bearer(tc.token))
				switch w.Code {
				case tc.passed:
				case http.StatusTooManyRequests:
					throttled++
				default:
					t.Fatalf("unexpected status %d", w.Code)
				}
			}
			persister.Stop()

			require.Equal(t, 1000-burst, throttled, "the limiter must have refused all but the burst")
			for _, id := range adminIDs {
				assert.Equal(t, 1, countAuditRows(t, db, "request_id = ?", id), "a flood evicted another subject's row")
			}
			assert.Equal(t, burst, countAuditRows(t, db, "persist_class = 'full' AND subject = ?", tc.subject),
				"only what the limiter let through belongs in the full class")
			assert.Zero(t, countAuditRows(t, db, "persist_class = 'full' AND status = 429"), "a 429 must never be a full-class row")
			assert.LessOrEqual(t, countAuditRows(t, db, "persist_class = 'sampled'"), 25)
		})
	}
}

func countAuditRows(t *testing.T, db *database.DB, where string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, db.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM audit_events WHERE `+where, args...).Scan(&n))
	return n
}

// Close must drain the audit queue before it closes the database. The table
// is write-locked while the events are recorded, so they are still queued
// when Close runs; closing the database first would lose them.
func TestCloseDrainsTheAuditTrailBeforeTheDatabase(t *testing.T) {
	db, path := openAuditDB(t)
	var lines bytes.Buffer
	trail := startAuditTrail(db, &lines)
	deps := &Dependencies{db: db, auditTrail: trail}

	lockDB, err := sql.Open("sqlite3", path+"?_busy_timeout=5000")
	require.NoError(t, err)
	defer lockDB.Close()
	ctx := context.Background()
	conn, err := lockDB.Conn(ctx)
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE")
	require.NoError(t, err)

	const n = 50
	for i := range n {
		trail.recorder.Record(audit.Entry{RequestID: fmt.Sprintf("close-%d", i), Authenticated: true,
			Subject: "operator", Method: http.MethodPost, Route: "/api/v1/zones", Status: 201, Outcome: audit.OutcomeSuccess})
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = conn.ExecContext(ctx, "ROLLBACK")
		_ = conn.Close()
	}()
	deps.Close()

	reopened := openPath(t, path)
	assert.Equal(t, n, countAuditRows(t, reopened, "request_id LIKE 'close-%'"), "Close lost queued audit events")
}

func openPath(t *testing.T, path string) *database.DB {
	t.Helper()
	cfg := database.DefaultConfig()
	cfg.DSN = path
	db, err := database.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// The production wiring end to end: initDependencies builds the trail,
// setupRouter hands its recorder to the API chain, /health reports it, and a
// mutation's row is in the database after Close.
func TestAuditTrailIsPersistedThroughTheRealWiring(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Server.Mode = gin.TestMode
	cfg.Database.DSN = filepath.Join(t.TempDir(), "console.db")
	deps, err := initDependencies(cfg)
	require.NoError(t, err)
	router := setupRouter(cfg, deps)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/zones",
		strings.NewReader(`{"name":"zone-audit","type":"`+string(models.ZoneTypeProxmox)+`"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	require.Equal(t, http.StatusCreated, w.Code)
	requestID := w.Result().Header.Get(middleware.RequestIDHeader)
	require.NotEmpty(t, requestID)

	health := httptest.NewRecorder()
	router.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/health", nil))
	assert.Contains(t, health.Body.String(), `"audit_trail":"ok"`)

	deps.Close()
	db := openPath(t, cfg.Database.DSN)
	assert.Equal(t, 1, countAuditRows(t, db,
		"request_id = ? AND route = '/api/v1/zones' AND outcome = 'success' AND persist_class = 'full'", requestID))
}

// /health reports the persisted trail as a status word and never turns red
// for it: the log lines are written whatever the store does.
func TestHealthReportsTheAuditTrailWithoutTurningRed(t *testing.T) {
	db, _ := openAuditDB(t)
	working := audit.NewPersister(repository.NewAuditRepository(db, repository.DefaultAuditCaps()), audit.PersisterConfig{})
	working.Start()
	defer working.Stop()
	broken := audit.NewPersister(failingStore{}, audit.PersisterConfig{Logf: (&syncLog{}).Logf})
	broken.Start()
	defer broken.Stop()
	broken.Submit(audit.Event{RequestID: "x", Authenticated: true, Outcome: audit.OutcomeSuccess})
	deadline := time.Now().Add(5 * time.Second)
	for broken.Stats().Failed == 0 {
		require.True(t, time.Now().Before(deadline), "the failing write never happened")
		time.Sleep(time.Millisecond)
	}

	for name, tc := range map[string]struct {
		trail auditTrail
		want  string
	}{
		"not wired": {trail: auditTrail{}, want: auditTrailDisabled},
		"working":   {trail: auditTrail{persister: working}, want: auditTrailOK},
		"failing":   {trail: auditTrail{persister: broken}, want: auditTrailDegraded},
	} {
		t.Run(name, func(t *testing.T) {
			code, body := getHealthBody(t, healthHandler(newHealthDB(t), nil, tc.trail, buildForTest()))
			assert.Equal(t, http.StatusOK, code)
			assert.Equal(t, statusHealthy, body.Status)
			assert.Equal(t, tc.want, body.AuditTrail)
		})
	}
}
