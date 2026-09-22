package provider

import (
	"context"
	"testing"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubAdapter is an Adapter that does nothing; registry tests only exercise
// construction.
type stubAdapter struct{}

func (stubAdapter) EnsureStorage(context.Context, *models.Qube, *models.Zone, Infra) (Infra, error) {
	return Infra{}, nil
}
func (stubAdapter) EnsureCompute(context.Context, *models.Qube, *models.Zone, Infra) (Infra, error) {
	return Infra{}, nil
}
func (stubAdapter) StopCompute(context.Context, *models.Qube, Infra) error    { return nil }
func (stubAdapter) DestroyStorage(context.Context, *models.Qube, Infra) error { return nil }
func (stubAdapter) Describe(context.Context, *models.Qube, Infra) (Observed, error) {
	return Observed{}, nil
}

func TestRegistry_RegisterRejectsNil(t *testing.T) {
	r := NewRegistry()
	err := r.Register(models.ZoneTypeProxmox, nil)
	require.Error(t, err)
	assert.False(t, r.Has(models.ZoneTypeProxmox))
}

func TestRegistry_RegisterRejectsDuplicate(t *testing.T) {
	r := NewRegistry()
	ctor := func(context.Context, *models.Zone) (Adapter, error) { return stubAdapter{}, nil }
	require.NoError(t, r.Register(models.ZoneTypeProxmox, ctor))
	err := r.Register(models.ZoneTypeProxmox, ctor)
	require.Error(t, err)
	assert.True(t, r.Has(models.ZoneTypeProxmox))
}

func TestRegistry_ForReturnsNoAdapter(t *testing.T) {
	r := NewRegistry()
	zone := &models.Zone{Name: "pve-1", Type: models.ZoneTypeProxmox}

	_, err := r.For(context.Background(), zone)
	require.Error(t, err)

	var noAdapter *ErrNoAdapter
	require.ErrorAs(t, err, &noAdapter)
	assert.Equal(t, models.ZoneTypeProxmox, noAdapter.ZoneType)
	assert.Equal(t, "pve-1", noAdapter.ZoneName)
}

func TestRegistry_ForBuildsAdapter(t *testing.T) {
	r := NewRegistry()
	want := stubAdapter{}
	constructorCalled := false
	ctor := func(_ context.Context, zone *models.Zone) (Adapter, error) {
		constructorCalled = true
		assert.Equal(t, models.ZoneTypeGCP, zone.Type)
		return want, nil
	}
	require.NoError(t, r.Register(models.ZoneTypeGCP, ctor))

	got, err := r.For(context.Background(), &models.Zone{Name: "gcp-1", Type: models.ZoneTypeGCP})
	require.NoError(t, err)
	assert.True(t, constructorCalled)
	assert.Equal(t, want, got)
}

func (stubAdapter) VerifyDestroyed(context.Context, *models.Qube, Infra) error { return nil }
