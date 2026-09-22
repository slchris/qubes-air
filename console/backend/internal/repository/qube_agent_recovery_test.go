package repository

import (
	"context"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
)

// failingProbe records one failed probe, failing the test if the write does.
func failingProbe(t *testing.T, repo QubeRepository, id string, at time.Time) {
	t.Helper()
	if err := repo.UpdateAgentHealth(context.Background(), id, models.AgentHealthUnreachable, at, "connection refused"); err != nil {
		t.Fatalf("failing probe at %v: %v", at, err)
	}
}

// TestAgentFailingSinceKeepsTheFirstFailureOfTheRun — the streak start is the
// observation that makes "nobody has answered for 20 minutes" answerable. If it
// crept forward with every sweep, a permanently dead agent would look
// perpetually one minute old and never reach the manual-recovery reading.
func TestAgentFailingSinceKeepsTheFirstFailureOfTheRun(t *testing.T) {
	repo, id := agentHealthEnv(t, "down-for-a-while", models.QubeStatusRunning)
	ctx := context.Background()

	first := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

	for i, gap := range []time.Duration{0, time.Minute, 2 * time.Minute} {
		failingProbe(t, repo, id, first.Add(gap))

		got, err := repo.GetByID(ctx, id)
		if err != nil {
			t.Fatalf("get after probe %d: %v", i, err)
		}
		if got.AgentFailingSince == nil || !got.AgentFailingSince.Equal(first) {
			t.Fatalf("probe %d must keep the streak start at %v, got %v", i, first, got.AgentFailingSince)
		}
		if got.AgentRecovery != models.AgentRecoveryPending {
			t.Fatalf("probe %d: %s of failure is inside the budget and must stay pending, got %q",
				i, gap, got.AgentRecovery)
		}
	}

	// The same streak, a probe later, has outlasted the unit's whole start
	// budget — and only the DERIVED reading changes. Nothing about the stored
	// streak moves.
	failingProbe(t, repo, id, first.Add(models.AgentStartLimitBudget))

	got, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.AgentFailingSince == nil || !got.AgentFailingSince.Equal(first) {
		t.Fatalf("the streak start must not move when the reading changes, got %v", got.AgentFailingSince)
	}
	if got.AgentRecovery != models.AgentRecoveryManual {
		t.Errorf("a failure older than the whole start-limit budget must read %q, got %q",
			models.AgentRecoveryManual, got.AgentRecovery)
	}
}

// TestAgentFailingSinceClearsOnAnyOtherVerdict — the streak describes the
// current run of failures only. An agent that answered, a qube that is starting
// and a console that lost visibility all mean the same thing for this field:
// there is no ongoing failure to age.
func TestAgentFailingSinceClearsOnAnyOtherVerdict(t *testing.T) {
	verdicts := []models.AgentHealth{
		models.AgentHealthHealthy,
		models.AgentHealthStarting,
		models.AgentHealthUnknown,
	}

	for _, verdict := range verdicts {
		t.Run(string(verdict), func(t *testing.T) {
			repo, id := agentHealthEnv(t, "verdict-"+string(verdict), models.QubeStatusRunning)
			ctx := context.Background()

			first := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
			failingProbe(t, repo, id, first)

			if err := repo.UpdateAgentHealth(ctx, id, verdict, first.Add(time.Minute), "reason"); err != nil {
				t.Fatalf("probe with verdict %q: %v", verdict, err)
			}

			got, err := repo.GetByID(ctx, id)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if got.AgentFailingSince != nil {
				t.Errorf("verdict %q must clear the streak, got start %v", verdict, got.AgentFailingSince)
			}
			if got.AgentRecovery != models.AgentRecoveryNone {
				t.Errorf("verdict %q leaves nothing to recover from, got %q", verdict, got.AgentRecovery)
			}
		})
	}
}

// TestAgentRecoveryIsClassifiedOnEveryRead — the classification is derived, not
// stored, so it has to be produced by the one read path every endpoint goes
// through. Anything that filled it in only on a single-qube GET would show up
// here as the list endpoint losing the state.
func TestAgentRecoveryIsClassifiedOnEveryRead(t *testing.T) {
	repo, id := agentHealthEnv(t, "listed-qube", models.QubeStatusRunning)
	ctx := context.Background()

	// A qube nobody has probed has no failure to classify.
	fresh, err := repo.GetByID(ctx, id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if fresh.AgentRecovery != models.AgentRecoveryNone {
		t.Fatalf("an unprobed qube must not carry a recovery state, got %q", fresh.AgentRecovery)
	}

	first := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	failingProbe(t, repo, id, first)

	listed, err := repo.List(ctx, DefaultQubeListOptions())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found *models.Qube
	for _, q := range listed {
		if q.ID == id {
			found = q
		}
	}
	if found == nil {
		t.Fatalf("qube %q missing from the list result", id)
	}
	if found.AgentRecovery != models.AgentRecoveryPending {
		t.Errorf("the list path must classify too, got %q", found.AgentRecovery)
	}
	if found.AgentFailingSince == nil || !found.AgentFailingSince.Equal(first) {
		t.Errorf("the list path must expose the streak start, got %v", found.AgentFailingSince)
	}
}

// TestAgentRecoveryReadsExactlyAtTheBudget — the boundary of the threshold, on
// the stored side: one second short of the unit's whole start-limit window is
// still "may come back on its own", and exactly at the window it is not.
func TestAgentRecoveryReadsExactlyAtTheBudget(t *testing.T) {
	tests := []struct {
		name string
		gap  time.Duration
		want models.AgentRecovery
	}{
		{"one second inside the budget", models.AgentStartLimitBudget - time.Second, models.AgentRecoveryPending},
		{"exactly at the budget", models.AgentStartLimitBudget, models.AgentRecoveryManual},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo, id := agentHealthEnv(t, "boundary", models.QubeStatusRunning)
			ctx := context.Background()

			first := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
			failingProbe(t, repo, id, first)
			failingProbe(t, repo, id, first.Add(tt.gap))

			got, err := repo.GetByID(ctx, id)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if got.AgentRecovery != tt.want {
				t.Errorf("a failure of %s must read %q, got %q", tt.gap, tt.want, got.AgentRecovery)
			}
		})
	}
}
