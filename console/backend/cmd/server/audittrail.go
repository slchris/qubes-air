package main

import (
	"io"
	"log"

	"github.com/slchris/qubes-air/console/internal/audit"
	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/service"
)

// auditTrail is the Console API audit trail as the server runs it: the
// recorder the /api/v1 middleware writes through, the persister that stores
// the same events, and the job that ages them out.
type auditTrail struct {
	recorder  *audit.Recorder
	persister *audit.Persister
	retention *service.AuditRetention
}

// startAuditTrail writes every audit line to w, as before, and persists the
// same events in audit_events: off the request path, with unauthenticated
// failures sampled, each persist class capped by the repository, and rows
// older than the retention window pruned (docs/security-controls.md, "持久化
// 审计"; defaults UD-24 in docs/runtime-defaults.md).
func startAuditTrail(db *database.DB, w io.Writer) auditTrail {
	repo := repository.NewAuditRepository(db, repository.DefaultAuditCaps())
	persister := audit.NewPersister(repo, audit.PersisterConfig{})
	retention := service.NewAuditRetention(repo, service.AuditRetentionConfig{})
	persister.Start()
	retention.Start()
	log.Printf("audit: persisting the API audit trail for %s (at most %d full-class and %d sampled rows; "+
		"unauthenticated failures stored %d at once, then one per %s)",
		service.DefaultAuditRetention, repository.DefaultAuditFullRows, repository.DefaultAuditSampledRows,
		audit.DefaultSampledBurst, audit.DefaultSampledEvery)
	return auditTrail{recorder: audit.NewRecorder(w).WithSink(persister), persister: persister, retention: retention}
}

// stop writes out what the persister still holds and ends retention. Both
// write the database, so Dependencies.Close calls it before closing that.
func (t auditTrail) stop() {
	if t.persister != nil {
		t.persister.Stop()
	}
	if t.retention != nil {
		t.retention.Stop()
	}
}

// /health words for the persisted trail. "degraded" means the last write
// failed or an event was dropped, and nothing has been written since; the log
// lines are unaffected either way.
const (
	auditTrailOK       = "ok"
	auditTrailDegraded = "degraded"
	auditTrailDisabled = "disabled"
)

// health reports the persisted trail's state for /health.
func (t auditTrail) health() string {
	switch {
	case t.persister == nil:
		return auditTrailDisabled
	case t.persister.Stats().Degraded:
		return auditTrailDegraded
	default:
		return auditTrailOK
	}
}
