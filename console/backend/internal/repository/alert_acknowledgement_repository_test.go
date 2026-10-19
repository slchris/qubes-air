package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAlertAcknowledgementsPersistAndExpireWithActiveAlerts(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()
	repo := NewAlertAcknowledgementRepository(db)
	ctx := context.Background()
	acknowledgedAt := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	require.NoError(t, repo.Acknowledge(ctx, "agent-unreachable:q1", "operator-a", acknowledgedAt))

	active, err := repo.SyncActive(ctx, []string{"agent-unreachable:q1"})
	require.NoError(t, err)
	require.Equal(t, "operator-a", active["agent-unreachable:q1"].AcknowledgedBy)
	require.True(t, acknowledgedAt.Equal(active["agent-unreachable:q1"].AcknowledgedAt))

	active, err = repo.SyncActive(ctx, []string{"zone-disconnected:z1"})
	require.NoError(t, err)
	require.Empty(t, active)
	active, err = repo.SyncActive(ctx, []string{"agent-unreachable:q1"})
	require.NoError(t, err)
	require.Empty(t, active, "a resolved alert must not retain its acknowledgement if it recurs")
}

func TestAlertAcknowledgementUpdateIsIdempotent(t *testing.T) {
	db, cleanup := setupQubeTestDB(t)
	defer cleanup()
	repo := NewAlertAcknowledgementRepository(db)
	ctx := context.Background()
	first := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	second := first.Add(time.Minute)
	require.NoError(t, repo.Acknowledge(ctx, "zone-disconnected:z1", "operator-a", first))
	require.NoError(t, repo.Acknowledge(ctx, "zone-disconnected:z1", "operator-b", second))
	active, err := repo.SyncActive(ctx, []string{"zone-disconnected:z1"})
	require.NoError(t, err)
	require.Equal(t, "operator-b", active["zone-disconnected:z1"].AcknowledgedBy)
	require.True(t, second.Equal(active["zone-disconnected:z1"].AcknowledgedAt))
}
