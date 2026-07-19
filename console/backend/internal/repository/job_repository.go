package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/orchestrator"
)

// ErrJobNotFound is returned when a job id is unknown.
var ErrJobNotFound = errors.New("job not found")

// JobRepository persists orchestration jobs as an audit trail.
//
// Every job is a record of an infrastructure change: what was asked for, when,
// and what terraform reported. Rows outlive the qube they reference — a
// released qube's history is exactly what an audit wants to read — so there is
// no foreign key onto qubes.
type JobRepository struct {
	db *database.DB
}

// NewJobRepository creates a JobRepository.
func NewJobRepository(db *database.DB) *JobRepository {
	return &JobRepository{db: db}
}

// Insert records a newly queued job.
func (r *JobRepository) Insert(ctx context.Context, j *orchestrator.Job) error {
	const q = `
		INSERT INTO jobs (id, qube_id, qube_name, action, state, error, enqueued_at, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := r.db.DB().ExecContext(ctx, q,
		j.ID, j.QubeID, j.QubeName, string(j.Action), string(j.State), j.Error,
		j.EnqueuedAt, j.StartedAt, j.FinishedAt)
	return err
}

// Update records a job's progress.
//
// Only the mutable lifecycle fields are written: the identity of the job and
// what it was asked to do are immutable once recorded, which is what makes the
// table trustworthy as an audit trail.
func (r *JobRepository) Update(ctx context.Context, j *orchestrator.Job) error {
	const q = `
		UPDATE jobs SET state = ?, error = ?, started_at = ?, finished_at = ?
		WHERE id = ?`
	res, err := r.db.DB().ExecContext(ctx, q,
		string(j.State), j.Error, j.StartedAt, j.FinishedAt, j.ID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrJobNotFound
	}
	return nil
}

// GetByID returns a single job.
func (r *JobRepository) GetByID(ctx context.Context, id string) (*orchestrator.Job, error) {
	const q = `
		SELECT id, qube_id, qube_name, action, state, error, enqueued_at, started_at, finished_at
		FROM jobs WHERE id = ?`
	row := r.db.DB().QueryRowContext(ctx, q, id)

	j, err := scanJobRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrJobNotFound
	}
	return j, err
}

// ListByQube returns a qube's jobs, newest first.
func (r *JobRepository) ListByQube(ctx context.Context, qubeID string, limit int) ([]*orchestrator.Job, error) {
	if limit <= 0 {
		limit = 50
	}
	const q = `
		SELECT id, qube_id, qube_name, action, state, error, enqueued_at, started_at, finished_at
		FROM jobs WHERE qube_id = ? ORDER BY enqueued_at DESC LIMIT ?`
	rows, err := r.db.DB().QueryContext(ctx, q, qubeID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*orchestrator.Job
	for rows.Next() {
		j, err := scanJobRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// List returns the most recent jobs across all qubes — the audit view.
func (r *JobRepository) List(ctx context.Context, limit int) ([]*orchestrator.Job, error) {
	if limit <= 0 {
		limit = 100
	}
	const q = `
		SELECT id, qube_id, qube_name, action, state, error, enqueued_at, started_at, finished_at
		FROM jobs ORDER BY enqueued_at DESC LIMIT ?`
	rows, err := r.db.DB().QueryContext(ctx, q, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*orchestrator.Job
	for rows.Next() {
		j, err := scanJobRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// FailUnfinished marks every non-terminal job failed and returns how many were
// affected.
//
// Called at startup: the queue is in memory, so anything still queued or
// running belonged to a process that is gone. Leaving them would make the audit
// trail claim work is in flight that nobody is doing.
func (r *JobRepository) FailUnfinished(ctx context.Context, reason string) (int64, error) {
	const q = `
		UPDATE jobs SET state = ?, error = ?, finished_at = ?
		WHERE state IN (?, ?)`
	res, err := r.db.DB().ExecContext(ctx, q,
		string(orchestrator.JobFailed), reason, time.Now().UTC(),
		string(orchestrator.JobQueued), string(orchestrator.JobRunning))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanJobRow reads one job, translating nullable timestamps.
func scanJobRow(sc rowScanner) (*orchestrator.Job, error) {
	var (
		j          orchestrator.Job
		action     string
		state      string
		startedAt  sql.NullTime
		finishedAt sql.NullTime
	)
	if err := sc.Scan(
		&j.ID, &j.QubeID, &j.QubeName, &action, &state, &j.Error,
		&j.EnqueuedAt, &startedAt, &finishedAt,
	); err != nil {
		return nil, err
	}
	j.Action = orchestrator.Action(action)
	j.State = orchestrator.JobState(state)
	if startedAt.Valid {
		t := startedAt.Time
		j.StartedAt = &t
	}
	if finishedAt.Valid {
		t := finishedAt.Time
		j.FinishedAt = &t
	}
	return &j, nil
}
