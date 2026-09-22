package service

import (
	"context"
	"log"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/orchestrator"
)

// UnfinishedJobs lists and updates jobs left behind by a previous process.
// repository.JobRepository satisfies it.
type UnfinishedJobs interface {
	ListByStates(ctx context.Context, states []orchestrator.JobState) ([]*orchestrator.Job, error)
	Update(ctx context.Context, j *orchestrator.Job) error
}

// QubeStatusWriter writes a qube's terminal status. repository.QubeRepository
// satisfies it.
type QubeStatusWriter interface {
	UpdateStatus(ctx context.Context, qubeID string, status models.QubeStatus) error
}

// ReconcileUnfinishedJobs restores a retryable state without replaying mutations.
// Queued jobs did not execute provider work, but preparation may have happened.
// Running jobs have an unknown outcome: compute status alone cannot attest to
// resource checkpoints, reachability, or completion cleanup. Explicit retries
// use the retained identity and irreversible purge intent.
func ReconcileUnfinishedJobs(ctx context.Context, jobs UnfinishedJobs, exec orchestrator.Executor, qubes QubeStatusWriter) {
	if queued, err := jobs.ListByStates(ctx, []orchestrator.JobState{orchestrator.JobQueued}); err != nil {
		log.Printf("orchestrator: could not scan for queued jobs: %v", err)
	} else {
		for _, j := range queued {
			markJob(ctx, jobs, j, orchestrator.JobFailed,
				"console restarted before execution; preparation may have completed; retry explicitly")
			setQubeStatus(ctx, qubes, j, models.QubeStatusError)
		}
	}

	running, err := jobs.ListByStates(ctx, []orchestrator.JobState{orchestrator.JobRunning})
	if err != nil {
		log.Printf("orchestrator: could not scan for running jobs: %v", err)
		return
	}
	for _, j := range running {
		observed, statusErr := exec.Status(ctx, j.QubeName)
		switch {
		case statusErr != nil:
			log.Printf("orchestrator: job %s (%s %s): provider status unreadable: %v",
				j.ID, j.Action, j.QubeName, statusErr)
			markJob(ctx, jobs, j, orchestrator.JobUnknown,
				"outcome unknown: provider status could not be read: "+statusErr.Error())
			setQubeStatus(ctx, qubes, j, models.QubeStatusError)
		default:
			log.Printf("orchestrator: job %s (%s %s) is UNKNOWN: provider is %q",
				j.ID, j.Action, j.QubeName, observed)
			markJob(ctx, jobs, j, orchestrator.JobUnknown,
				"outcome unknown: provider reports the qube is "+observed)
			setQubeStatus(ctx, qubes, j, models.QubeStatusError)
		}
	}
}

func markJob(ctx context.Context, jobs UnfinishedJobs, j *orchestrator.Job, state orchestrator.JobState, note string) {
	now := time.Now().UTC()
	j.State = state
	j.Error = note
	j.FinishedAt = &now
	if err := jobs.Update(ctx, j); err != nil {
		log.Printf("orchestrator: recording reconciled job %s failed: %v", j.ID, err)
	}
}

func setQubeStatus(ctx context.Context, qubes QubeStatusWriter, j *orchestrator.Job, status models.QubeStatus) {
	if err := qubes.UpdateStatus(ctx, j.QubeID, status); err != nil {
		log.Printf("orchestrator: setting qube %s to %q after reconciling job %s failed: %v",
			j.QubeName, status, j.ID, err)
	}
}
