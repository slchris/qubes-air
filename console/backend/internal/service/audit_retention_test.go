package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/audit"
	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePruner records each cutoff it is asked for. With block set it waits for
// its context to end and reports why.
type fakePruner struct {
	mu      sync.Mutex
	cutoffs []time.Time
	err     error
	block   bool
	entered chan struct{}
	ctxErr  error
}

func (p *fakePruner) PruneBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	p.mu.Lock()
	p.cutoffs = append(p.cutoffs, cutoff)
	block, err, entered := p.block, p.err, p.entered
	p.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if block {
		<-ctx.Done()
		p.mu.Lock()
		p.ctxErr = ctx.Err()
		p.mu.Unlock()
		return 0, ctx.Err()
	}
	return 0, err
}

func (p *fakePruner) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.cutoffs)
}

type retentionLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *retentionLog) Logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *retentionLog) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

var retentionNow = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

// The cutoff is exactly the retention window before now: 90 days by default.
func TestAuditRetentionCutoffIsTheWindowBeforeNow(t *testing.T) {
	pruner := &fakePruner{}
	now := func() time.Time { return retentionNow }

	_, err := NewAuditRetention(pruner, AuditRetentionConfig{Now: now}).Prune(context.Background())
	require.NoError(t, err)
	_, err = NewAuditRetention(pruner, AuditRetentionConfig{Now: now, MaxAge: time.Hour}).Prune(context.Background())
	require.NoError(t, err)

	require.Len(t, pruner.cutoffs, 2)
	assert.Equal(t, retentionNow.Add(-90*24*time.Hour), pruner.cutoffs[0])
	assert.Equal(t, retentionNow.Add(-time.Hour), pruner.cutoffs[1])
}

// Start prunes at once and then on every tick; after Stop returns nothing
// runs again.
func TestAuditRetentionRunsAtStartAndEveryIntervalUntilStopped(t *testing.T) {
	pruner := &fakePruner{}
	r := NewAuditRetention(pruner, AuditRetentionConfig{Interval: 2 * time.Millisecond})
	r.Start()
	r.Start()

	deadline := time.Now().Add(5 * time.Second)
	for pruner.calls() < 3 {
		require.True(t, time.Now().Before(deadline), "the loop did not keep pruning")
		time.Sleep(time.Millisecond)
	}
	r.Stop()
	after := pruner.calls()
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, after, pruner.calls(), "a prune ran after Stop returned")
}

// Stop does not wait out a slow prune: it cancels it, and the cancellation is
// not reported as a failure.
func TestAuditRetentionStopCancelsAPruneInFlight(t *testing.T) {
	pruner := &fakePruner{block: true, entered: make(chan struct{}, 1)}
	logs := &retentionLog{}
	r := NewAuditRetention(pruner, AuditRetentionConfig{Timeout: time.Hour, Logf: logs.Logf})
	r.Start()
	<-pruner.entered

	stopped := make(chan struct{})
	go func() {
		r.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop waited for the prune instead of canceling it")
	}
	pruner.mu.Lock()
	defer pruner.mu.Unlock()
	assert.ErrorIs(t, pruner.ctxErr, context.Canceled)
	assert.NotContains(t, logs.all(), "failed", "a prune canceled by Stop is not a failure")
}

// A prune that runs past its timeout is cut off and logged; the loop lives on.
func TestAuditRetentionBoundsAndLogsAFailedPrune(t *testing.T) {
	logs := &retentionLog{}
	slow := &fakePruner{block: true}
	r := NewAuditRetention(slow, AuditRetentionConfig{Timeout: 5 * time.Millisecond, Logf: logs.Logf})
	r.Start()
	defer r.Stop()

	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.all(), "retention prune failed") {
		require.True(t, time.Now().Before(deadline), "a timed-out prune was not logged")
		time.Sleep(time.Millisecond)
	}
	assert.Contains(t, logs.all(), context.DeadlineExceeded.Error())

	failing := &fakePruner{err: errors.New("database is locked")}
	logs2 := &retentionLog{}
	r2 := NewAuditRetention(failing, AuditRetentionConfig{Logf: logs2.Logf})
	r2.pruneOnce()
	assert.Contains(t, logs2.all(), "database is locked")
}

// Stop is safe without Start and twice, and Start after Stop does nothing:
// shutdown paths must not hang or restart a job over a closed database.
func TestAuditRetentionStopWithoutStartAndStartAfterStop(t *testing.T) {
	pruner := &fakePruner{}
	r := NewAuditRetention(pruner, AuditRetentionConfig{Interval: time.Millisecond})
	r.Stop()
	r.Stop()
	r.Start()
	time.Sleep(10 * time.Millisecond)
	assert.Zero(t, pruner.calls())
}

// Against the real repository: rows older than 90 days go, rows inside the
// window stay, and the job is stopped before its database closes.
func TestAuditRetentionPrunesTheRealTrail(t *testing.T) {
	cfg := database.DefaultConfig()
	cfg.DSN = filepath.Join(t.TempDir(), "retention.db")
	db, err := database.New(cfg)
	require.NoError(t, err)
	repo := repository.NewAuditRepository(db, repository.DefaultAuditCaps())
	ctx := context.Background()
	for i, age := range []time.Duration{91 * 24 * time.Hour, 89 * 24 * time.Hour, time.Minute} {
		require.NoError(t, repo.AppendEvent(ctx, audit.Event{Time: retentionNow.Add(-age), RequestID: fmt.Sprintf("r%d", i),
			Authenticated: true, AuthMethod: audit.AuthMethodBearer, Subject: "operator", Outcome: audit.OutcomeSuccess,
			ZoneScope: "fleet"}))
	}

	r := NewAuditRetention(repo, AuditRetentionConfig{Now: func() time.Time { return retentionNow }})
	r.Start()
	var left int
	deadline := time.Now().Add(5 * time.Second)
	for {
		require.NoError(t, db.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_events`).Scan(&left))
		if left == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	r.Stop()
	require.NoError(t, db.Close())
	assert.Equal(t, 2, left, "only the row older than 90 days is removed")
}
