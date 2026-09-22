package repository

import (
	"context"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestListFiltersByAllowedZones — the zone allowlist of a credential is applied
// in the query, so a list request cannot return another zone's objects even
// before the handler sees them.
func TestListFiltersByAllowedZones(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	zoneRepo := NewZoneRepository(db)
	qubeRepo := NewQubeRepository(db)

	for _, id := range []string{"z1", "z2"} {
		require.NoError(t, zoneRepo.Create(ctx, &models.Zone{
			ID: id, Name: id, Type: models.ZoneTypeProxmox, Status: models.ZoneStatusConnected,
			CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}))
	}
	for _, q := range []struct{ id, zone string }{{"q1", "z1"}, {"q2", "z2"}} {
		require.NoError(t, qubeRepo.Create(ctx, &models.Qube{
			ID: q.id, Name: q.id, ZoneID: q.zone, Type: "dev", Status: models.QubeStatusPending,
			Spec: models.QubeSpec{}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}))
	}

	z1Only := DefaultZoneListOptions()
	z1Only.Zones = []string{"z1"}
	zones, err := zoneRepo.List(ctx, z1Only)
	require.NoError(t, err)
	require.Len(t, zones, 1)
	assert.Equal(t, "z1", zones[0].ID)

	scopedQubes := DefaultQubeListOptions()
	scopedQubes.Zones = []string{"z1"}
	qubes, err := qubeRepo.List(ctx, scopedQubes)
	require.NoError(t, err)
	require.Len(t, qubes, 1)
	assert.Equal(t, "q1", qubes[0].ID)

	// An explicit zone filter for a foreign zone must not widen the allowlist.
	foreign := DefaultQubeListOptions()
	foreign.Zones = []string{"z1"}
	foreign.ZoneID = "z2"
	qubes, err = qubeRepo.List(ctx, foreign)
	require.NoError(t, err)
	assert.Empty(t, qubes)

	// A fleet-wide query (no Zones) still sees everything.
	qubes, err = qubeRepo.List(ctx, DefaultQubeListOptions())
	require.NoError(t, err)
	assert.Len(t, qubes, 2)
}
