package repository

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/database"
	"github.com/slchris/qubes-air/console/internal/models"
)

// agentHealthEnv wires a DB, a repo and a zone, and returns a qube in the given
// status to probe against.
func agentHealthEnv(t *testing.T, name string, status models.QubeStatus) (QubeRepository, string) {
	t.Helper()
	repo, zoneID := claimTestEnv(t)
	return repo, claimTestQube(t, repo, zoneID, name, status)
}

// TestAgentHealthDefaultsToUnknown — a qube nobody has probed must not read as
// healthy. "No opinion" and "the agent answered" are the two readings this
// change exists to keep apart, and defaulting the wrong way would reproduce the
// original bug: green console, dead agent.
func TestAgentHealthDefaultsToUnknown(t *testing.T) {
	repo, id := agentHealthEnv(t, "fresh", models.QubeStatusRunning)

	got, err := repo.GetByID(context.Background(), id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.AgentHealth != models.AgentHealthUnknown {
		t.Errorf("a never-probed qube must be unknown, got %q", got.AgentHealth)
	}
	if got.AgentLastProbedAt != nil {
		t.Errorf("never probed must leave the probe time nil, got %v", got.AgentLastProbedAt)
	}
	if got.AgentLastHealthyAt != nil {
		t.Errorf("never seen healthy must leave the healthy time nil, got %v", got.AgentLastHealthyAt)
	}
	if got.AgentLastError != "" {
		t.Errorf("no probe has failed yet, got error %q", got.AgentLastError)
	}
}

// TestUpdateAgentHealthRoundTrip — a recorded probe survives a write and a read
// with its reason intact. The reason is the payload that matters: "unreachable"
// alone sends an operator to SSH into a hypervisor, which is exactly the hour of
// debugging this feature is meant to remove.
func TestUpdateAgentHealthRoundTrip(t *testing.T) {
	repo, id := agentHealthEnv(t, "probed", models.QubeStatusRunning)
	ctx := context.Background()

	probedAt := time.Now().UTC().Truncate(time.Second)
	const reason = `dial tcp 10.0.0.7:8443: connect: connection refused`

	if err := repo.UpdateAgentHealth(ctx, id, models.AgentHealthUnreachable, probedAt, reason); err != nil {
		t.Fatalf("UpdateAgentHealth: %v", err)
	}

	got, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.AgentHealth != models.AgentHealthUnreachable {
		t.Errorf("want unreachable, got %q", got.AgentHealth)
	}
	if got.AgentLastError != reason {
		t.Errorf("the probe failure reason must survive verbatim, got %q", got.AgentLastError)
	}
	if got.AgentLastProbedAt == nil || !got.AgentLastProbedAt.Equal(probedAt) {
		t.Errorf("want probe time %v, got %v", probedAt, got.AgentLastProbedAt)
	}
	// A failed probe is still not evidence the agent was ever up.
	if got.AgentLastHealthyAt != nil {
		t.Errorf("a failing probe must not set the last-healthy time, got %v", got.AgentLastHealthyAt)
	}
}

// TestAgentLastHealthyOnlyAdvancesOnHealthy — the field that answers "how long
// has this been broken". If a failing probe moved it, an agent down for an hour
// would look like it was fine a second ago.
func TestAgentLastHealthyOnlyAdvancesOnHealthy(t *testing.T) {
	repo, id := agentHealthEnv(t, "flapping", models.QubeStatusRunning)
	ctx := context.Background()

	healthyAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if err := repo.UpdateAgentHealth(ctx, id, models.AgentHealthHealthy, healthyAt, ""); err != nil {
		t.Fatalf("healthy probe: %v", err)
	}

	failedAt := time.Now().UTC().Truncate(time.Second)
	if err := repo.UpdateAgentHealth(ctx, id, models.AgentHealthUnreachable, failedAt, "timeout"); err != nil {
		t.Fatalf("failing probe: %v", err)
	}

	got, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.AgentLastHealthyAt == nil || !got.AgentLastHealthyAt.Equal(healthyAt) {
		t.Errorf("last-healthy must stay at the last successful probe %v, got %v", healthyAt, got.AgentLastHealthyAt)
	}
	if got.AgentLastProbedAt == nil || !got.AgentLastProbedAt.Equal(failedAt) {
		t.Errorf("last-probed must advance on every probe, got %v", got.AgentLastProbedAt)
	}
}

// TestAgentHealthRecoveryClearsTheError — the error describes the LAST probe,
// not the last failure ever seen, so a recovered agent stops displaying a
// complaint that no longer applies.
func TestAgentHealthRecoveryClearsTheError(t *testing.T) {
	repo, id := agentHealthEnv(t, "recovered", models.QubeStatusRunning)
	ctx := context.Background()

	if err := repo.UpdateAgentHealth(ctx, id, models.AgentHealthUnreachable, time.Now(), "connection refused"); err != nil {
		t.Fatalf("failing probe: %v", err)
	}
	if err := repo.UpdateAgentHealth(ctx, id, models.AgentHealthHealthy, time.Now(), ""); err != nil {
		t.Fatalf("healthy probe: %v", err)
	}

	got, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.AgentHealth != models.AgentHealthHealthy {
		t.Errorf("want healthy, got %q", got.AgentHealth)
	}
	if got.AgentLastError != "" {
		t.Errorf("a healthy probe must clear the stale reason, got %q", got.AgentLastError)
	}
}

// TestUpdateAgentHealthRejectsUnknownValue — an unrecognised value would be
// persisted and rendered verbatim, showing up as a mystery state in the UI
// instead of as a visible failure at the call site.
func TestUpdateAgentHealthRejectsUnknownValue(t *testing.T) {
	repo, id := agentHealthEnv(t, "bad-value", models.QubeStatusRunning)

	if err := repo.UpdateAgentHealth(context.Background(), id, models.AgentHealth("ok"), time.Now(), ""); err == nil {
		t.Fatal("an invalid agent health value must be refused, not stored")
	}
}

// TestUpdateAgentHealthOnMissingQube — a probe whose qube was deleted mid-flight
// must report that it wrote nothing. A probe loop that silently affects zero
// rows looks exactly like a probe loop that works.
func TestUpdateAgentHealthOnMissingQube(t *testing.T) {
	repo, _ := claimTestEnv(t)

	err := repo.UpdateAgentHealth(context.Background(), "gone", models.AgentHealthHealthy, time.Now(), "")
	if !errors.Is(err, ErrQubeNotFound) {
		t.Fatalf("want ErrQubeNotFound, got %v", err)
	}
}

// TestUpdateAgentHealthDoesNotDisturbStatus is the core separation guarantee:
// VM state and agent state are different facts, and recording one must not
// touch the other. A qube whose VM runs fine with a dead agent stays running.
func TestUpdateAgentHealthDoesNotDisturbStatus(t *testing.T) {
	repo, id := agentHealthEnv(t, "running-dead-agent", models.QubeStatusRunning)
	ctx := context.Background()

	before, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if err := repo.UpdateAgentHealth(ctx, id, models.AgentHealthUnreachable, time.Now(), "no route to host"); err != nil {
		t.Fatalf("UpdateAgentHealth: %v", err)
	}

	got, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != models.QubeStatusRunning {
		t.Errorf("the VM is still running; recording a dead agent must not change status, got %q", got.Status)
	}
	if got.AgentHealth != models.AgentHealthUnreachable {
		t.Errorf("want the agent recorded unreachable, got %q", got.AgentHealth)
	}
	// updated_at tracks changes to the qube itself. Probes run continuously, so
	// bumping it on every one would destroy its meaning.
	if !got.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("a probe must not bump updated_at: was %v, now %v", before.UpdatedAt, got.UpdatedAt)
	}
}

// TestUpdateAgentHealthDoesNotClobberConcurrentStatus — the race the method is
// written to survive. The probe result is derived from a copy of the row read
// BEFORE provisioning changed the status; writing the whole row back from that
// copy would silently revert the qube to its pre-apply status, which is the
// class of bug ClaimTransition exists to prevent elsewhere.
func TestUpdateAgentHealthDoesNotClobberConcurrentStatus(t *testing.T) {
	repo, id := agentHealthEnv(t, "mid-apply", models.QubeStatusRunning)
	ctx := context.Background()

	// The probe worker's view of the qube, taken before the apply lands.
	stale, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if stale.Status != models.QubeStatusRunning {
		t.Fatalf("setup: want running, got %q", stale.Status)
	}

	// Provisioning finishes on its own goroutine while the probe is in flight.
	if err := repo.UpdateStatus(ctx, id, models.QubeStatusSuspended); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}

	// The probe result lands afterwards, carrying the stale row with it.
	if err := repo.UpdateAgentHealth(ctx, id, models.AgentHealthUnreachable, time.Now(), "probe timed out"); err != nil {
		t.Fatalf("UpdateAgentHealth: %v", err)
	}

	got, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != models.QubeStatusSuspended {
		t.Errorf("the late probe reverted status to %q, losing the suspend", got.Status)
	}
	if got.AgentHealth != models.AgentHealthUnreachable {
		t.Errorf("the probe result was lost, got %q", got.AgentHealth)
	}
}

// TestAgentHealthAndStatusWritersRaceCleanly — the same guarantee under real
// concurrency: status writes and probe writes touch disjoint columns, so
// neither writer can lose the other's work regardless of interleaving.
func TestAgentHealthAndStatusWritersRaceCleanly(t *testing.T) {
	repo, id := agentHealthEnv(t, "contended-health", models.QubeStatusRunning)
	ctx := context.Background()

	const rounds = 25
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan error, 2*rounds)

	wg.Add(2)
	go func() {
		defer wg.Done()
		<-start
		for range rounds {
			if err := repo.UpdateStatus(ctx, id, models.QubeStatusRunning); err != nil {
				errs <- err
			}
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for range rounds {
			if err := repo.UpdateAgentHealth(ctx, id, models.AgentHealthUnreachable, time.Now(), "refused"); err != nil {
				errs <- err
			}
		}
	}()
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent write: %v", err)
	}

	got, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != models.QubeStatusRunning {
		t.Errorf("status writer lost its write, got %q", got.Status)
	}
	if got.AgentHealth != models.AgentHealthUnreachable {
		t.Errorf("probe writer lost its write, got %q", got.AgentHealth)
	}
}

// legacyQubesSchema is the qubes table exactly as it existed before agent health
// was recorded. Tests migrate a database created with it; an upgrade that
// required a wipe would destroy every qube an operator already has.
const legacyQubesSchema = `
CREATE TABLE IF NOT EXISTS qubes (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	type TEXT NOT NULL,
	zone_id TEXT DEFAULT '',
	status TEXT NOT NULL DEFAULT 'stopped',
	spec TEXT DEFAULT '{}',
	ip_address TEXT DEFAULT '',
	created_at DATETIME NOT NULL,
	updated_at DATETIME NOT NULL
)`

// TestMigrationUpgradesLegacyDatabase — an existing database must keep working.
// The row is written by the OLD schema, then read back through the new one:
// nothing is lost, and the qube is reported unknown rather than healthy, since
// nobody has ever probed it.
func TestMigrationUpgradesLegacyDatabase(t *testing.T) {
	tmp, err := os.CreateTemp("", "qube-legacy-*.db")
	if err != nil {
		t.Fatalf("temp db: %v", err)
	}
	tmp.Close()
	t.Cleanup(func() { os.Remove(tmp.Name()) })

	// Create the database as the previous schema version would have.
	legacy, err := sql.Open("sqlite3", tmp.Name())
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := legacy.Exec(legacyQubesSchema); err != nil {
		t.Fatalf("legacy schema: %v", err)
	}
	created := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Second)
	if _, err := legacy.Exec(
		`INSERT INTO qubes (id, name, type, zone_id, status, spec, ip_address, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"legacy-1", "legacy-qube", string(models.QubeTypeApp), "test-zone",
		string(models.QubeStatusRunning), `{"vcpu":4,"memory":8192}`, "10.0.0.42", created, created,
	); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	// Open it with the current code, which must migrate rather than fail.
	cfg := database.DefaultConfig()
	cfg.DSN = tmp.Name()
	db, err := database.New(cfg)
	if err != nil {
		t.Fatalf("migrating an existing database must succeed: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	repo := NewQubeRepository(db)
	ctx := context.Background()

	got, err := repo.GetByID(ctx, "legacy-1")
	if err != nil {
		t.Fatalf("reading a migrated row: %v", err)
	}
	if got.Name != "legacy-qube" || got.IPAddress != "10.0.0.42" || got.Spec.VCPU != 4 {
		t.Errorf("the migration lost pre-existing data: %+v", got)
	}
	if got.Status != models.QubeStatusRunning {
		t.Errorf("the VM's own status must survive the migration, got %q", got.Status)
	}
	if got.AgentHealth != models.AgentHealthUnknown {
		t.Errorf("a legacy row was never probed and must backfill to unknown, got %q", got.AgentHealth)
	}
	if got.AgentLastProbedAt != nil || got.AgentLastHealthyAt != nil {
		t.Error("a legacy row must not claim probe times that never happened")
	}

	// The migrated row is fully usable, not merely readable.
	probedAt := time.Now().UTC().Truncate(time.Second)
	if err := repo.UpdateAgentHealth(ctx, "legacy-1", models.AgentHealthHealthy, probedAt, ""); err != nil {
		t.Fatalf("probing a migrated row: %v", err)
	}
	got, err = repo.GetByID(ctx, "legacy-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.AgentHealth != models.AgentHealthHealthy || got.AgentLastHealthyAt == nil {
		t.Errorf("probe result did not stick on a migrated row: %+v", got)
	}
}

// TestMigrationIsRepeatable — migrate() runs on every startup, so adding the
// columns a second time must be a no-op rather than an error that stops the
// console from booting.
func TestMigrationIsRepeatable(t *testing.T) {
	tmp, err := os.CreateTemp("", "qube-remigrate-*.db")
	if err != nil {
		t.Fatalf("temp db: %v", err)
	}
	tmp.Close()
	t.Cleanup(func() { os.Remove(tmp.Name()) })

	cfg := database.DefaultConfig()
	cfg.DSN = tmp.Name()

	for i := range 3 {
		db, err := database.New(cfg)
		if err != nil {
			t.Fatalf("startup %d: %v", i+1, err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close %d: %v", i+1, err)
		}
	}
}
