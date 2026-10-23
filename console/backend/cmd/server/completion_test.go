package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/orchestrator"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type completionQubes struct {
	repository.QubeRepository
	status  models.QubeStatus
	address string
}

type failingIdentityCleanup struct {
	err error
	ids []string
}

func (c *failingIdentityCleanup) RemoveIdentityFor(ctx context.Context, id, name string) error {
	c.ids = append(c.ids, id+":"+name)
	return errors.Join(ctx.Err(), c.err)
}

func TestPurgeCompletionRequiresIdentityCleanup(t *testing.T) {
	q := &completionQubes{status: models.QubeStatusDeleting}
	cleaner := &failingIdentityCleanup{err: errors.New("identity requires review")}
	hook := makeCompletionHook(q, nil, nil, cleaner)
	job := &orchestrator.Job{QubeID: "q", QubeName: "alpha", Action: orchestrator.ActionDestroy, State: orchestrator.JobSucceeded}
	require.ErrorContains(t, hook(context.Background(), job), "identity requires review")
	require.Equal(t, models.QubeStatusError, q.status)
	cleaner.err = nil
	require.NoError(t, hook(context.Background(), job))
	require.Equal(t, models.QubeStatusPurged, q.status)
	require.Equal(t, []string{"q:alpha", "q:alpha"}, cleaner.ids)
	job.State = orchestrator.JobFailed
	require.NoError(t, hook(context.Background(), job))
	require.Len(t, cleaner.ids, 2, "failed provider destruction must retain the delivery document")
}

func (q *completionQubes) UpdateStatus(_ context.Context, _ string, status models.QubeStatus) error {
	q.status = status
	return nil
}
func (q *completionQubes) UpdateIPAddress(_ context.Context, _, address string) error {
	q.address = address
	return nil
}

type cleanupCaller struct{ reply string }

func (c *cleanupCaller) Call(context.Context, string, string, []byte) ([]byte, error) {
	return []byte(c.reply), nil
}

func TestPurgeCompletionWaitsForRemoteVMCleanup(t *testing.T) {
	q := &completionQubes{address: "192.0.2.1", status: models.QubeStatusDeleting}
	caller := &cleanupCaller{reply: "FAILED"}
	hook := makeCompletionHook(q, nil, service.NewRemoteVMRegistrar(caller, true), nil)
	job := &orchestrator.Job{QubeID: "q", QubeName: "remote-one", Action: orchestrator.ActionDestroy, State: orchestrator.JobSucceeded}
	require.ErrorContains(t, hook(context.Background(), job), "remove RemoteVM")
	assert.Equal(t, models.QubeStatusError, q.status)
	assert.Empty(t, q.address)
	caller.reply = "OK RETIRED remote-remote-one relay=verified"
	require.NoError(t, hook(context.Background(), job))
	assert.Equal(t, models.QubeStatusPurged, q.status)
}

// ctxAwareCompletionQubes refuses a status write on a canceled context, like
// the SQL repository. The base fake ignores contexts, which would hid the bug.
type ctxAwareCompletionQubes struct {
	completionQubes
}

func (q *ctxAwareCompletionQubes) UpdateStatus(ctx context.Context, id string, status models.QubeStatus) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return q.completionQubes.UpdateStatus(ctx, id, status)
}

// blockingCaller consumes the hook's whole budget and then fails, the shape of
// a dom0 registration/deregistration call that times out.
type blockingCaller struct{}

func (c *blockingCaller) Call(ctx context.Context, _, _ string, _ []byte) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// TestPurgeCompletionPersistsErrorAfterCleanupTimeout — a timed-out RemoteVM
// deregistration must still leave the qube in a terminal, retryable state. The
// deferred status write used the exhausted hook context, so the row stayed on
// "deleting" with no terminal state at all.
func TestPurgeCompletionPersistsErrorAfterCleanupTimeout(t *testing.T) {
	q := &ctxAwareCompletionQubes{completionQubes: completionQubes{
		status: models.QubeStatusDeleting,
	}}
	hook := makeCompletionHook(q, nil, service.NewRemoteVMRegistrar(&blockingCaller{}, true), nil)
	job := &orchestrator.Job{
		QubeID: "q", QubeName: "remote-one",
		Action: orchestrator.ActionDestroy, State: orchestrator.JobSucceeded,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := hook(ctx, job)

	require.ErrorContains(t, err, "remove RemoteVM")
	assert.Equal(t, models.QubeStatusError, q.status,
		"a cleanup timeout must still record a terminal status")
}
