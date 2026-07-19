package orchestrator

import (
	"context"
	"errors"
	"sort"
	"sync"
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

// Update replaces a recorded job.
func (m *MemoryJobStore) Update(_ context.Context, j *Job) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.jobs[j.ID]; !ok {
		return ErrJobNotFound
	}
	cp := *j
	m.jobs[j.ID] = &cp
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
