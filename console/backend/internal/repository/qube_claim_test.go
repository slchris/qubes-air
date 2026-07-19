package repository

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/slchris/qubes-air/console/internal/models"
)

// claimTestQube inserts a qube in the given status and returns its id.
func claimTestQube(t *testing.T, repo QubeRepository, zoneID, name string, status models.QubeStatus) string {
	t.Helper()
	q := &models.Qube{
		ID:     name,
		Name:   name,
		Type:   models.QubeTypeApp,
		ZoneID: zoneID,
		Status: status,
	}
	if err := repo.Create(context.Background(), q); err != nil {
		t.Fatalf("create qube %q: %v", name, err)
	}
	return q.ID
}

// claimTestEnv wires the DB, repo and a zone the qubes can reference.
func claimTestEnv(t *testing.T) (QubeRepository, string) {
	t.Helper()
	db, cleanup := setupQubeTestDB(t)
	t.Cleanup(cleanup)
	zone := createTestZone(t, NewZoneRepository(db))
	return NewQubeRepository(db), zone.ID
}

// TestClaimTransitionRejectsWrongSource — a claim must only succeed from an
// expected status, so an operation cannot start from a state it does not make
// sense in (e.g. resuming a qube that is already running).
func TestClaimTransitionRejectsWrongSource(t *testing.T) {
	repo, zoneID := claimTestEnv(t)
	ctx := context.Background()

	id := claimTestQube(t, repo, zoneID, "already-running", models.QubeStatusRunning)

	err := repo.ClaimTransition(ctx, id,
		[]models.QubeStatus{models.QubeStatusStopped, models.QubeStatusSuspended},
		models.QubeStatusResuming)
	if !errors.Is(err, ErrTransitionConflict) {
		t.Fatalf("want ErrTransitionConflict, got %v", err)
	}

	got, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != models.QubeStatusRunning {
		t.Errorf("status must be untouched on a failed claim, got %q", got.Status)
	}
}

// TestClaimTransitionIsAtomicUnderConcurrency is the whole point of the method:
// N goroutines racing to claim the same qube must produce exactly one winner.
// A read-then-write in Go would let several through, and each would enqueue its
// own multi-minute terraform apply against the same qube.
func TestClaimTransitionIsAtomicUnderConcurrency(t *testing.T) {
	repo, zoneID := claimTestEnv(t)
	ctx := context.Background()

	id := claimTestQube(t, repo, zoneID, "contended", models.QubeStatusSuspended)

	const racers = 12
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		wins     int
		conflict int
		other    []error
	)
	start := make(chan struct{})

	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release everyone at once to maximise overlap
			err := repo.ClaimTransition(ctx, id,
				[]models.QubeStatus{models.QubeStatusSuspended},
				models.QubeStatusResuming)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ErrTransitionConflict):
				conflict++
			default:
				other = append(other, err)
			}
		}()
	}
	close(start)
	wg.Wait()

	if len(other) > 0 {
		t.Fatalf("unexpected errors: %v", other)
	}
	if wins != 1 {
		t.Errorf("exactly one claim must win, got %d (conflicts=%d)", wins, conflict)
	}
	if conflict != racers-1 {
		t.Errorf("want %d conflicts, got %d", racers-1, conflict)
	}
}

// TestClaimTransitionAppliesTargetStatus — the winner's status actually moves.
func TestClaimTransitionAppliesTargetStatus(t *testing.T) {
	repo, zoneID := claimTestEnv(t)
	ctx := context.Background()

	id := claimTestQube(t, repo, zoneID, "to-suspend", models.QubeStatusRunning)

	if err := repo.ClaimTransition(ctx, id,
		[]models.QubeStatus{models.QubeStatusRunning},
		models.QubeStatusSuspending); err != nil {
		t.Fatalf("claim: %v", err)
	}

	got, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != models.QubeStatusSuspending {
		t.Errorf("want suspending, got %q", got.Status)
	}
	if !got.Status.IsTransient() {
		t.Error("suspending must be classified transient, or startup reconciliation will not find it")
	}
}

// TestListByStatusFindsStrandedQubes — the queue is in memory, so a qube left
// in a transient status by a crashed process must be discoverable at startup.
// Without this it would stay "busy" forever and every future operation on it
// would be refused.
func TestListByStatusFindsStrandedQubes(t *testing.T) {
	repo, zoneID := claimTestEnv(t)
	ctx := context.Background()

	claimTestQube(t, repo, zoneID, "stranded-a", models.QubeStatusResuming)
	claimTestQube(t, repo, zoneID, "stranded-b", models.QubeStatusDeleting)
	claimTestQube(t, repo, zoneID, "settled", models.QubeStatusRunning)

	transient := []models.QubeStatus{
		models.QubeStatusCreating, models.QubeStatusResuming,
		models.QubeStatusSuspending, models.QubeStatusDeleting,
	}
	got, err := repo.ListByStatus(ctx, transient)
	if err != nil {
		t.Fatalf("ListByStatus: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 stranded qubes, got %d", len(got))
	}
	for _, q := range got {
		if !q.Status.IsTransient() {
			t.Errorf("qube %q has non-transient status %q", q.Name, q.Status)
		}
	}
}

// TestListByStatusEmptyInput — no statuses means no query, not a SQL error.
func TestListByStatusEmptyInput(t *testing.T) {
	repo, _ := claimTestEnv(t)

	got, err := repo.ListByStatus(context.Background(), nil)
	if err != nil {
		t.Fatalf("want no error for an empty status list, got %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want no rows, got %d", len(got))
	}
}
