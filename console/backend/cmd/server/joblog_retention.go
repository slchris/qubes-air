package main

import (
	"context"
	"log"
	"time"

	"github.com/slchris/qubes-air/console/internal/orchestrator"
	"github.com/slchris/qubes-air/console/internal/repository"
)

const (
	// jobLogRetention is how long a job's operation output is kept. Job logs
	// are not part of the encrypted database backup, so after a restore a job
	// whose file is gone reports "lost" rather than pretending it never logged.
	jobLogRetention = 30 * 24 * time.Hour
	// jobLogPruneInterval is how often the retention pass runs after startup.
	jobLogPruneInterval = 24 * time.Hour
	jobLogPruneTimeout  = time.Minute
	// jobLogMarkTimeout bounds recording the removals. It is detached from the
	// prune context so a shutdown that cancels the pass still records the files
	// it already deleted; otherwise those jobs would look "lost" after restart.
	jobLogMarkTimeout = 5 * time.Second
)

// startJobLogRetention prunes old job log files once at startup and daily
// afterwards, recording each removal on the job row so the API can distinguish
// an expired log from one that was never written or is missing after a restore.
func startJobLogRetention(logs *orchestrator.JobLogStore, jobs *repository.JobRepository) (context.CancelFunc, chan struct{}) {
	if logs == nil || jobs == nil {
		return nil, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		prune := func() {
			pruneCtx, pruneCancel := context.WithTimeout(ctx, jobLogPruneTimeout)
			defer pruneCancel()
			ids, err := logs.Prune(pruneCtx, time.Now().UTC().Add(-jobLogRetention))
			if len(ids) > 0 {
				// Record what was deleted even if the pass was canceled mid-way:
				// the files are gone, and leaving no trace would report them as
				// "lost" rather than "expired" after the next restart.
				markCtx, markCancel := context.WithTimeout(context.WithoutCancel(pruneCtx), jobLogMarkTimeout)
				prunedAt := time.Now().UTC()
				for _, id := range ids {
					if markErr := jobs.MarkLogPruned(markCtx, id, prunedAt); markErr != nil {
						log.Printf("job logs: pruned %s but could not record it: %v", id, markErr)
					}
				}
				markCancel()
			}
			if err != nil {
				if ctx.Err() == nil {
					log.Printf("job logs: prune failed: %v", err)
				}
				return
			}
			if len(ids) > 0 {
				log.Printf("job logs: pruned %d file(s) older than %s", len(ids), jobLogRetention)
			}
		}
		prune()
		ticker := time.NewTicker(jobLogPruneInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				prune()
			}
		}
	}()
	return cancel, done
}
