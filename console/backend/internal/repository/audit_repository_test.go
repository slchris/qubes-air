package repository

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/audit"
	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var auditT0 = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

func newAuditTestDB(t *testing.T) *database.DB {
	t.Helper()
	cfg := database.DefaultConfig()
	cfg.DSN = filepath.Join(t.TempDir(), "audit.db")
	db, err := database.New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func fullEvent(i int) audit.Event {
	return audit.Event{Time: auditT0.Add(time.Duration(i) * time.Second), RequestID: fmt.Sprintf("full-%d", i),
		Authenticated: true, Subject: "operator", Source: "192.0.2.1", Method: "POST",
		Route: "/api/v1/qubes/:id/start", Object: "q-a", Status: 202, Outcome: audit.OutcomeSuccess,
		LatencyMS: 4, ZoneScope: "fleet"}
}

func sampledEvent(i int) audit.Event {
	return audit.Event{Time: auditT0.Add(time.Duration(i) * time.Second), RequestID: fmt.Sprintf("anon-%d", i),
		Subject: audit.AnonymousSubject, Source: "198.51.100.9", Method: "POST",
		Route: "/api/v1/qubes/:id/start", Object: "q-a", Status: 401, Outcome: audit.OutcomeDenied, ZoneScope: "none"}
}

// storedEvent reads a row back into the Event it was written from, plus the
// three bookkeeping columns.
func storedEvent(t *testing.T, db *database.DB, requestID string) (audit.Event, string, int64, int64) {
	t.Helper()
	var ev audit.Event
	var occurred, suppressed, since int64
	var class string
	require.NoError(t, db.DB().QueryRowContext(context.Background(), `
		SELECT occurred_at, request_id, authenticated, auth_disabled, subject, source, method, route, object,
		       object_truncated, status, outcome, latency_ms, zone_scope, persist_class, suppressed, suppressed_since
		FROM audit_events WHERE request_id = ?`, requestID).Scan(
		&occurred, &ev.RequestID, &ev.Authenticated, &ev.AuthDisabled, &ev.Subject, &ev.Source, &ev.Method,
		&ev.Route, &ev.Object, &ev.ObjectTruncated, &ev.Status, &ev.Outcome, &ev.LatencyMS, &ev.ZoneScope,
		&class, &suppressed, &since))
	ev.Time = time.Unix(0, occurred).UTC()
	return ev, class, suppressed, since
}

func auditCount(t *testing.T, db *database.DB, where string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, db.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM audit_events WHERE `+where, args...).Scan(&n))
	return n
}

// An event is stored field for field, to the nanosecond, including on a
// database that was upgraded from v2 rather than created fresh.
func TestAuditRepositoryStoresTheEventAsLogged(t *testing.T) {
	for name, db := range map[string]*database.DB{"fresh": newAuditTestDB(t), "upgraded from v2": openV2FixtureDB(t)} {
		t.Run(name, func(t *testing.T) {
			repo := NewAuditRepository(db, DefaultAuditCaps())
			ev := fullEvent(1)
			ev.Time = ev.Time.Add(123456789 * time.Nanosecond)
			ev.AuthDisabled = true
			ev.ObjectTruncated = true
			ev.ZoneScope = "zone-a,zone-b"
			require.NoError(t, repo.AppendEvent(context.Background(), ev))

			got, class, suppressed, since := storedEvent(t, db, ev.RequestID)
			assert.Equal(t, ev, got)
			assert.Equal(t, string(audit.ClassFull), class)
			assert.Zero(t, suppressed)
			assert.Zero(t, since)
		})
	}
}

// A suppression summary is a sampled-class row that says how many events it
// stands for, over what span and from where, with the auth mode the events
// had and no request fields.
func TestAuditRepositoryStoresSuppressionSummary(t *testing.T) {
	db := newAuditTestDB(t)
	repo := NewAuditRepository(db, DefaultAuditCaps())
	s := audit.Suppression{First: auditT0, Last: auditT0.Add(time.Minute), Count: 4242, Sources: 3,
		TopSources:   []audit.SourceCount{{Prefix: "198.51.100.0/24", Count: 4200}, {Prefix: "2001:db8:1::/64", Count: 40}},
		AuthDisabled: true}
	require.NoError(t, repo.AppendSuppression(context.Background(), s))

	got, class, suppressed, since := storedEvent(t, db, "")
	assert.Equal(t, string(audit.ClassSampled), class)
	assert.EqualValues(t, 4242, suppressed)
	assert.Equal(t, auditT0.UnixNano(), since)
	assert.True(t, got.Time.Equal(s.Last))
	assert.Equal(t, audit.OutcomeSuppressed, got.Outcome)
	assert.True(t, got.AuthDisabled, "the summary keeps the events' auth mode")
	assert.False(t, got.Authenticated)
	for name, field := range map[string]string{"subject": got.Subject, "source": got.Source, "route": got.Route,
		"method": got.Method, "object": got.Object, "zone_scope": got.ZoneScope} {
		assert.Empty(t, field, "a summary is not a request: %s must be empty", name)
	}
	var sources int
	var top string
	require.NoError(t, db.DB().QueryRowContext(context.Background(),
		`SELECT suppressed_sources, suppressed_top_sources FROM audit_events WHERE outcome = 'suppressed'`).Scan(&sources, &top))
	assert.Equal(t, 3, sources)
	assert.Equal(t, "198.51.100.0/24 4200; 2001:db8:1::/64 40; others 2", top)
}

// The headline bound: an unauthenticated flood fills at most the sampled cap,
// keeps its newest rows, and never evicts a full-class row.
func TestAuditRepositoryAnonymousFloodCannotEvictFullRows(t *testing.T) {
	db := newAuditTestDB(t)
	repo := NewAuditRepository(db, AuditCaps{Full: 50, Sampled: 20})
	ctx := context.Background()
	for i := range 30 {
		require.NoError(t, repo.AppendEvent(ctx, fullEvent(i)))
	}

	for i := range 1000 {
		require.NoError(t, repo.AppendEvent(ctx, sampledEvent(1000+i)))
		if i%100 == 0 {
			require.NoError(t, repo.AppendSuppression(ctx,
				audit.Suppression{First: auditT0, Last: auditT0.Add(time.Duration(1000+i) * time.Second), Count: 99}))
		}
		require.LessOrEqual(t, auditCount(t, db, "persist_class = 'sampled'"), 20,
			"the sampled class went over its cap after flood row %d", i)
	}

	assert.Equal(t, 30, auditCount(t, db, "persist_class = 'full'"), "a flood must not evict full-class rows")
	for i := range 30 {
		assert.Equal(t, 1, auditCount(t, db, "request_id = ?", fmt.Sprintf("full-%d", i)))
	}
	assert.Equal(t, 1, auditCount(t, db, "request_id = 'anon-1999'"), "the newest flood row is kept")
	assert.Equal(t, 0, auditCount(t, db, "request_id = 'anon-1000'"), "the oldest flood rows are the ones evicted")
	assert.LessOrEqual(t, auditCount(t, db, "1 = 1"), 50+20)
}

// The full class is capped too, oldest first, trimming to 1% under the cap so
// eviction does not run on every insert.
func TestAuditRepositoryFullClassEvictsOldestFirst(t *testing.T) {
	db := newAuditTestDB(t)
	repo := NewAuditRepository(db, AuditCaps{Full: 200, Sampled: 20})
	ctx := context.Background()
	for i := range 200 {
		require.NoError(t, repo.AppendEvent(ctx, fullEvent(i)))
	}
	require.Equal(t, 200, auditCount(t, db, "persist_class = 'full'"), "the cap itself is allowed")

	require.NoError(t, repo.AppendEvent(ctx, fullEvent(200)))

	assert.Equal(t, 198, auditCount(t, db, "persist_class = 'full'"), "one over the cap trims to 1%% below it")
	assert.Equal(t, 0, auditCount(t, db, "request_id IN ('full-0', 'full-1', 'full-2')"))
	assert.Equal(t, 1, auditCount(t, db, "request_id = 'full-3'"))
	assert.Equal(t, 1, auditCount(t, db, "request_id = 'full-200'"))
}

// The cap holds across restarts (a new repository finds the rows a previous
// process wrote) and across pruning (counts are reloaded, not guessed).
func TestAuditRepositoryCapHoldsAcrossRestartAndPrune(t *testing.T) {
	db := newAuditTestDB(t)
	ctx := context.Background()
	caps := AuditCaps{Full: 50, Sampled: 20}
	first := NewAuditRepository(db, caps)
	for i := range 15 {
		require.NoError(t, first.AppendEvent(ctx, sampledEvent(i)))
	}

	second := NewAuditRepository(db, caps)
	for i := 15; i < 30; i++ {
		require.NoError(t, second.AppendEvent(ctx, sampledEvent(i)))
	}
	assert.Equal(t, 20, auditCount(t, db, "persist_class = 'sampled'"))

	removed, err := second.PruneBefore(ctx, sampledEvent(25).Time)
	require.NoError(t, err)
	assert.EqualValues(t, 15, removed)
	for i := 30; i < 60; i++ {
		require.NoError(t, second.AppendEvent(ctx, sampledEvent(i)))
	}
	assert.Equal(t, 20, auditCount(t, db, "persist_class = 'sampled'"))
}

// PruneBefore removes rows strictly older than the cutoff: one a nanosecond
// older goes, one exactly at the cutoff stays.
func TestAuditRepositoryPruneBoundaryIsExclusive(t *testing.T) {
	db := newAuditTestDB(t)
	repo := NewAuditRepository(db, DefaultAuditCaps())
	ctx := context.Background()
	cutoff := auditT0.Add(time.Hour)
	for i, at := range []time.Time{cutoff.Add(-time.Nanosecond), cutoff, cutoff.Add(time.Nanosecond)} {
		ev := fullEvent(i)
		ev.Time = at
		require.NoError(t, repo.AppendEvent(ctx, ev))
	}

	removed, err := repo.PruneBefore(ctx, cutoff)
	require.NoError(t, err)
	assert.EqualValues(t, 1, removed)
	assert.Equal(t, 0, auditCount(t, db, "request_id = 'full-0'"))
	assert.Equal(t, 1, auditCount(t, db, "request_id = 'full-1'"), "a row exactly at the cutoff is kept")
	assert.Equal(t, 1, auditCount(t, db, "request_id = 'full-2'"))
}

// A backlog larger than one batch is pruned completely, in batches, and a
// canceled context stops the prune before it writes.
func TestAuditRepositoryPrunesABacklogInBatches(t *testing.T) {
	db := newAuditTestDB(t)
	repo := NewAuditRepository(db, DefaultAuditCaps())
	ctx := context.Background()
	const backlog = 2*auditPruneBatch + 500
	tx, err := db.DB().BeginTx(ctx, nil)
	require.NoError(t, err)
	for i := range backlog {
		_, err := tx.ExecContext(ctx, insertAuditEvent, auditT0.Add(time.Duration(i)).UnixNano(), fmt.Sprintf("old-%d", i),
			true, false, "operator", "192.0.2.1", "POST", "/r", "", false, 200, "success", 0, "fleet", "full", 0, 0, 0, "")
		require.NoError(t, err)
	}
	require.NoError(t, tx.Commit())
	require.NoError(t, repo.AppendEvent(ctx, fullEvent(10_000)))

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	removed, err := repo.PruneBefore(canceled, auditT0.Add(time.Hour))
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, removed)

	removed, err = repo.PruneBefore(ctx, auditT0.Add(time.Hour))
	require.NoError(t, err)
	assert.EqualValues(t, backlog, removed)
	assert.Equal(t, 1, auditCount(t, db, "1 = 1"), "only the row inside the window is left")
}
