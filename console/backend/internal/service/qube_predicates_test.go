package service

import (
	"context"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func qubeWith(status models.QubeStatus, spec models.QubeSpec) *models.Qube {
	return &models.Qube{
		ID: "q1", Name: "dev-work", ZoneID: "z1",
		Type: models.QubeTypeWork, Status: status, Spec: spec,
	}
}

// TestComputeRunningFollowsIntent is the switch behind suspend/resume: a
// suspended or released qube must report no compute instance, or the next
// resume/operation would act on a VM that was deliberately released.
func TestComputeRunningFollowsIntent(t *testing.T) {
	running := []models.QubeStatus{
		models.QubeStatusRunning, models.QubeStatusCreating, models.QubeStatusResuming,
	}
	notRunning := []models.QubeStatus{
		models.QubeStatusStopped, models.QubeStatusSuspended, models.QubeStatusReleased,
		models.QubeStatusSuspending, models.QubeStatusDeleting, models.QubeStatusError,
	}
	for _, s := range running {
		assert.True(t, computeRunning(s), "%s must report a compute instance", s)
	}
	for _, s := range notRunning {
		assert.False(t, computeRunning(s), "%s must report no compute instance", s)
	}
}

// TestIsRenderable_ReleasedQubesStayResolvable — a released qube still owns a
// protected data disk, so it must stay resolvable until a purge removes it.
func TestIsRenderable_ReleasedQubesStayRendered(t *testing.T) {
	released := qubeWith(models.QubeStatusReleased, models.QubeSpec{})
	assert.True(t, isRenderable(released),
		"a released qube must stay resolvable: its protected data disk still exists")

	for _, s := range []models.QubeStatus{
		models.QubeStatusRunning, models.QubeStatusSuspended,
		models.QubeStatusError, models.QubeStatusDeleting,
	} {
		assert.True(t, isRenderable(qubeWith(s, models.QubeSpec{})), "%s must be resolvable", s)
	}
}

// TestIsRenderable_SkipsUnprovisionable — a pending qube has no infrastructure
// yet, and a zoneless one has nowhere to be placed.
func TestIsRenderable_SkipsUnprovisionable(t *testing.T) {
	assert.False(t, isRenderable(qubeWith(models.QubeStatusPending, models.QubeSpec{})))

	zoneless := qubeWith(models.QubeStatusRunning, models.QubeSpec{})
	zoneless.ZoneID = ""
	assert.False(t, isRenderable(zoneless))
}

// TestResolver_NameCollisionKeepsNewestRow — rows are not unique by name:
// deleting and recreating a qube leaves the released row in place until its
// purge finishes, so both rows match the name. The newest row is the current
// intent; picking the other would target the wrong cloud resource.
func TestResolver_NameCollisionKeepsNewestRow(t *testing.T) {
	old := qubeWith(models.QubeStatusReleased, models.QubeSpec{Node: "infra-node4"})
	old.ID = "old"
	old.CreatedAt = time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)

	fresh := qubeWith(models.QubeStatusCreating, models.QubeSpec{Node: "infra-node4"})
	fresh.ID = "fresh"
	fresh.CreatedAt = old.CreatedAt.Add(time.Minute)

	// Both orderings must agree, or the result depends on how the DB happened
	// to sort the rows.
	for _, order := range [][]*models.Qube{{old, fresh}, {fresh, old}} {
		resolver := NewNativeQubeZoneResolver(&stubQubeLister{qubes: order}, &stubZoneRepo{zone: testZone()})
		q, _, err := resolver.Resolve(context.Background(), "dev-work")
		require.NoError(t, err)
		require.NotNil(t, q)
		assert.Equal(t, "fresh", q.ID, "the newer row is the current intent")
	}
}

func testZone() *models.Zone {
	return &models.Zone{ID: "z1", Name: "infra", Type: models.ZoneTypeProxmox}
}

// stubQubeLister satisfies repository.QubeRepository with only List doing work;
// the resolver never calls anything else.
type stubQubeLister struct{ qubes []*models.Qube }

func (s *stubQubeLister) List(context.Context, repository.QubeListOptions) ([]*models.Qube, error) {
	return s.qubes, nil
}
func (s *stubQubeLister) Create(context.Context, *models.Qube) error { return nil }
func (s *stubQubeLister) GetByID(context.Context, string) (*models.Qube, error) {
	return nil, nil
}
func (s *stubQubeLister) Update(context.Context, *models.Qube) error { return nil }
func (s *stubQubeLister) Delete(context.Context, string) error       { return nil }
func (s *stubQubeLister) UpdateStatus(context.Context, string, models.QubeStatus) error {
	return nil
}
func (s *stubQubeLister) UpdateIPAddress(context.Context, string, string) error { return nil }
func (s *stubQubeLister) ClaimTransition(context.Context, string, []models.QubeStatus, models.QubeStatus) error {
	return nil
}
func (s *stubQubeLister) ListByStatus(context.Context, []models.QubeStatus) ([]*models.Qube, error) {
	return nil, nil
}
func (s *stubQubeLister) UpdateAgentHealth(
	context.Context, string, models.AgentHealth, time.Time, string,
) error {
	return nil
}

func (s *stubQubeLister) ClaimPurge(context.Context, string, []models.QubeStatus) error { return nil }
