package main

import (
	"context"
	"testing"

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
	hook := makeCompletionHook(q, nil, service.NewRemoteVMRegistrar(caller, true))
	job := &orchestrator.Job{QubeID: "q", QubeName: "remote-one", Action: orchestrator.ActionDestroy, State: orchestrator.JobSucceeded}
	require.ErrorContains(t, hook(context.Background(), job), "remove RemoteVM")
	assert.Equal(t, models.QubeStatusError, q.status)
	assert.Empty(t, q.address)
	caller.reply = "OK"
	require.NoError(t, hook(context.Background(), job))
	assert.Equal(t, models.QubeStatusPurged, q.status)
}
