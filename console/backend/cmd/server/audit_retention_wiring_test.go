package main

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/audit"
	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startAuditTrail must start retention: a row older than 90 days is pruned on
// the first run, which happens at start, and a recent one stays.
func TestAuditTrailPrunesOldRowsThroughTheRealWiring(t *testing.T) {
	db, _ := openAuditDB(t)
	repo := repository.NewAuditRepository(db, repository.DefaultAuditCaps())
	ctx := context.Background()
	now := time.Now()
	for id, at := range map[string]time.Time{
		"expired": now.Add(-service.DefaultAuditRetention - time.Hour),
		"recent":  now.Add(-time.Hour),
	} {
		require.NoError(t, repo.AppendEvent(ctx, audit.Event{Time: at, RequestID: id, Authenticated: true,
			AuthMethod: audit.AuthMethodBearer, Subject: "operator", Outcome: audit.OutcomeSuccess, ZoneScope: "fleet"}))
	}

	var lines bytes.Buffer
	trail := startAuditTrail(db, &lines)
	defer trail.stop()

	deadline := time.Now().Add(5 * time.Second)
	for countAuditRows(t, db, "request_id = 'expired'") != 0 {
		require.True(t, time.Now().Before(deadline), "the wired retention job never pruned a row older than 90 days")
		time.Sleep(5 * time.Millisecond)
	}
	assert.Equal(t, 1, countAuditRows(t, db, "request_id = 'recent'"), "a row inside the window must stay")
}

// closeProbePruner blocks in PruneBefore until its context ends, then records
// whether the database was still open at that moment.
type closeProbePruner struct {
	db      *database.DB
	entered chan struct{}

	mu       sync.Mutex
	canceled bool
	dbClosed bool
}

func (p *closeProbePruner) PruneBefore(ctx context.Context, _ time.Time) (int64, error) {
	p.entered <- struct{}{}
	<-ctx.Done()
	err := p.db.DB().PingContext(context.Background())
	p.mu.Lock()
	p.canceled = true
	p.dbClosed = err != nil
	p.mu.Unlock()
	return 0, ctx.Err()
}

func (p *closeProbePruner) state() (canceled, dbClosed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.canceled, p.dbClosed
}

// Dependencies.Close must stop retention, and before the database closes: a
// prune in flight is canceled while the database is still open, not left
// running (or started again) against a closed handle.
func TestCloseStopsRetentionBeforeTheDatabase(t *testing.T) {
	db, _ := openAuditDB(t)
	pruner := &closeProbePruner{db: db, entered: make(chan struct{}, 1)}
	retention := service.NewAuditRetention(pruner, service.AuditRetentionConfig{Timeout: time.Hour})
	t.Cleanup(retention.Stop) // releases the probe if Close forgot to
	deps := &Dependencies{db: db, auditTrail: auditTrail{retention: retention}}
	retention.Start()
	<-pruner.entered

	deps.Close()

	canceled, dbClosed := pruner.state()
	require.True(t, canceled, "Close returned with the retention prune still running")
	assert.False(t, dbClosed, "retention was stopped only after the database had closed")
}
