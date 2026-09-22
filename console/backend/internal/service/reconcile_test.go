package service

import (
	"context"
	"errors"
	"testing"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/orchestrator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type statusExecutor struct {
	statusFn func(name string) (string, error)
}

func (statusExecutor) Suspend(context.Context, string) error { return nil }
func (statusExecutor) Resume(context.Context, string) error  { return nil }
func (statusExecutor) Provision(context.Context, string) error {
	return nil
}
func (statusExecutor) Destroy(context.Context, string) error { return nil }
func (e statusExecutor) Status(_ context.Context, name string) (string, error) {
	return e.statusFn(name)
}

type fakeJobs struct {
	rows    []*orchestrator.Job
	updated []*orchestrator.Job
}

func (f *fakeJobs) ListByStates(_ context.Context, states []orchestrator.JobState) ([]*orchestrator.Job, error) {
	want := map[orchestrator.JobState]bool{}
	for _, s := range states {
		want[s] = true
	}
	var out []*orchestrator.Job
	for _, j := range f.rows {
		if want[j.State] {
			out = append(out, j)
		}
	}
	return out, nil
}

func (f *fakeJobs) Update(_ context.Context, j *orchestrator.Job) error {
	f.updated = append(f.updated, j)
	return nil
}

type fakeQubeWriter struct {
	statuses map[string]models.QubeStatus
}

func (f *fakeQubeWriter) UpdateStatus(_ context.Context, qubeID string, status models.QubeStatus) error {
	if f.statuses == nil {
		f.statuses = map[string]models.QubeStatus{}
	}
	f.statuses[qubeID] = status
	return nil
}

func TestReconcile_QueuedJobFails(t *testing.T) {
	jobs := &fakeJobs{rows: []*orchestrator.Job{
		{ID: "j1", QubeID: "q1", QubeName: "web01", Action: orchestrator.ActionProvision, State: orchestrator.JobQueued},
	}}
	qubes := &fakeQubeWriter{}
	exec := statusExecutor{statusFn: func(string) (string, error) { return "running", nil }}

	ReconcileUnfinishedJobs(context.Background(), jobs, exec, qubes)

	require.Len(t, jobs.updated, 1)
	assert.Equal(t, orchestrator.JobFailed, jobs.updated[0].State)
	assert.Equal(t, models.QubeStatusError, qubes.statuses["q1"], "preparation can have happened before enqueue")
}

func TestReconcile_RunningResolvesAgainstProvider(t *testing.T) {
	cases := []struct {
		action   orchestrator.Action
		observed string
		wantJob  orchestrator.JobState
		wantQube models.QubeStatus
	}{
		{orchestrator.ActionProvision, "running", orchestrator.JobUnknown, models.QubeStatusError},
		{orchestrator.ActionResume, "running", orchestrator.JobUnknown, models.QubeStatusError},
		{orchestrator.ActionSuspend, "suspended", orchestrator.JobUnknown, models.QubeStatusError},
		{orchestrator.ActionRelease, "suspended", orchestrator.JobUnknown, models.QubeStatusError},
		{orchestrator.ActionDestroy, "absent", orchestrator.JobUnknown, models.QubeStatusError},
		{orchestrator.ActionProvision, "suspended", orchestrator.JobUnknown, models.QubeStatusError},
		{orchestrator.ActionResume, "absent", orchestrator.JobUnknown, models.QubeStatusError},
	}
	for _, c := range cases {
		jobs := &fakeJobs{rows: []*orchestrator.Job{
			{ID: "j1", QubeID: "q1", QubeName: "web01", Action: c.action, State: orchestrator.JobRunning},
		}}
		qubes := &fakeQubeWriter{}
		exec := statusExecutor{statusFn: func(string) (string, error) { return c.observed, nil }}

		ReconcileUnfinishedJobs(context.Background(), jobs, exec, qubes)

		require.Len(t, jobs.updated, 1)
		assert.Equalf(t, c.wantJob, jobs.updated[0].State, "%s observed=%s", c.action, c.observed)
		assert.Equalf(t, c.wantQube, qubes.statuses["q1"], "%s observed=%s", c.action, c.observed)
	}
}

func TestReconcile_StatusErrorIsUnknown(t *testing.T) {
	jobs := &fakeJobs{rows: []*orchestrator.Job{
		{ID: "j1", QubeID: "q1", QubeName: "web01", Action: orchestrator.ActionResume, State: orchestrator.JobRunning},
	}}
	qubes := &fakeQubeWriter{}
	exec := statusExecutor{statusFn: func(string) (string, error) { return "", errors.New("cluster unreachable") }}

	ReconcileUnfinishedJobs(context.Background(), jobs, exec, qubes)

	require.Len(t, jobs.updated, 1)
	assert.Equal(t, orchestrator.JobUnknown, jobs.updated[0].State)
	assert.Contains(t, jobs.updated[0].Error, "cluster unreachable")
	assert.Equal(t, models.QubeStatusError, qubes.statuses["q1"])
}

// Compute status cannot attest to disk destruction and completion bookkeeping.
func TestReconcile_DestroyCannotSucceedFromComputeStatus(t *testing.T) {
	for _, observed := range []string{"suspended", "absent"} {
		t.Run(observed, func(t *testing.T) {
			jobs := &fakeJobs{rows: []*orchestrator.Job{{
				ID: "purge", QubeID: "q1", QubeName: "web01",
				Action: orchestrator.ActionDestroy, State: orchestrator.JobRunning,
			}}}
			qubes := &fakeQubeWriter{}
			exec := statusExecutor{statusFn: func(string) (string, error) { return observed, nil }}
			ReconcileUnfinishedJobs(context.Background(), jobs, exec, qubes)
			require.Len(t, jobs.updated, 1)
			assert.Equal(t, orchestrator.JobUnknown, jobs.updated[0].State)
			assert.Equal(t, models.QubeStatusError, qubes.statuses["q1"])
		})
	}
}
