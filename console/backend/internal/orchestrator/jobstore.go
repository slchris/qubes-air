package orchestrator

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// ErrJobNotFound is returned when a job id is unknown.
var ErrJobNotFound = errors.New("job not found")

// MemoryJobStore keeps jobs in memory.
//
// Job records are for polling an in-flight operation, so they only need to
// outlive the request — not the process. Durability across a restart is instead
// provided by the qube's own status: a qube left transient is reconciled at
// startup. If job history ever needs to survive a restart, swap this for a
// table-backed store; the interface is the seam.
type MemoryJobStore struct {
	mu   sync.RWMutex
	jobs map[string]*Job
	// order preserves insertion order so ListByQube can return newest-first
	// without relying on map iteration.
	order []string
	// max bounds retention so a long-lived console does not grow without limit.
	max int
}

// DefaultJobRetention is how many completed jobs are kept.
const DefaultJobRetention = 500

// NewMemoryJobStore creates an in-memory job store.
func NewMemoryJobStore() *MemoryJobStore {
	return &MemoryJobStore{jobs: map[string]*Job{}, max: DefaultJobRetention}
}

// Insert records a new job.
func (m *MemoryJobStore) Insert(_ context.Context, j *Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *j
	m.jobs[j.ID] = &cp
	m.order = append(m.order, j.ID)
	m.evictLocked()
	return nil
}

// Update replaces a recorded job's lifecycle fields.
//
// Log timestamps are owned by MarkLogCreated/MarkLogPruned and are preserved
// here, mirroring the SQL repository, whose UPDATE does not touch those columns.
// The runner's finish path writes back a Job value captured before the log was
// opened; replacing the whole record would erase the creation time and make an
// existing log look lost.
func (m *MemoryJobStore) Update(_ context.Context, j *Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.jobs[j.ID]
	if !ok {
		return ErrJobNotFound
	}
	cp := *j
	cp.LogCreatedAt = existing.LogCreatedAt
	cp.LogPrunedAt = existing.LogPrunedAt
	m.jobs[j.ID] = &cp
	return nil
}

// MarkLogCreated records the first time a log file was opened for the job.
func (m *MemoryJobStore) MarkLogCreated(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return ErrJobNotFound
	}
	if j.LogCreatedAt == nil {
		t := at.UTC()
		j.LogCreatedAt = &t
	}
	return nil
}

// MarkLogPruned records that the retention pass removed the job's log file.
func (m *MemoryJobStore) MarkLogPruned(_ context.Context, id string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return ErrJobNotFound
	}
	t := at.UTC()
	j.LogPrunedAt = &t
	return nil
}

// GetByID returns a copy of the job.
func (m *MemoryJobStore) GetByID(_ context.Context, id string) (*Job, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	j, ok := m.jobs[id]
	if !ok {
		return nil, ErrJobNotFound
	}
	cp := *j
	return &cp, nil
}

// ListByQube returns a qube's jobs, newest first.
func (m *MemoryJobStore) ListByQube(_ context.Context, qubeID string, limit int) ([]*Job, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var out []*Job
	for _, j := range m.jobs {
		if j.QubeID == qubeID {
			cp := *j
			out = append(out, &cp)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].EnqueuedAt.After(out[b].EnqueuedAt) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// evictLocked drops the oldest records past the retention bound. Callers hold
// the write lock.
func (m *MemoryJobStore) evictLocked() {
	for len(m.order) > m.max {
		oldest := m.order[0]
		m.order = m.order[1:]
		delete(m.jobs, oldest)
	}
}
