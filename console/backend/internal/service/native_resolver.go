package service

import (
	"context"
	"fmt"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/orchestrator"
	"github.com/slchris/qubes-air/console/internal/repository"
)

// nativeQubeZoneResolver maps a qube name to its row and zone for the native
// provider executor.
//
// Name selection mirrors NewQubeSnapshot: a name can be shared by more than one
// row during a delete/recreate, and the newest renderable row is the current
// intent. Reusing the same rule here is what keeps "which qube is being acted
// on" identical between the executor and the rest of the console — a resolver
// that picked a different row would target the wrong cloud resource.
type nativeQubeZoneResolver struct {
	qubes repository.QubeRepository
	zones repository.ZoneRepository
}

// NewNativeQubeZoneResolver builds the resolver the NativeExecutor uses.
func NewNativeQubeZoneResolver(qubes repository.QubeRepository, zones repository.ZoneRepository) orchestrator.QubeZoneResolver {
	return &nativeQubeZoneResolver{qubes: qubes, zones: zones}
}

// Resolve returns the newest renderable row for name and its zone. A qube that
// does not exist returns (nil, nil, nil), which the executor reports as
// not-found rather than acting on an arbitrary row.
func (r *nativeQubeZoneResolver) Resolve(ctx context.Context, name string) (*models.Qube, *models.Zone, error) {
	opts := repository.DefaultQubeListOptions()
	opts.Limit = 10000 // effectively unbounded; names are not indexed
	qubes, err := r.qubes.List(ctx, opts)
	if err != nil {
		return nil, nil, fmt.Errorf("list qubes: %w", err)
	}

	chosen := newestRenderableByName(qubes, name)
	if chosen == nil {
		return nil, nil, nil
	}
	zone, err := r.zones.GetByID(ctx, chosen.ZoneID)
	if err != nil {
		return nil, nil, fmt.Errorf("load zone %q for qube %q: %w", chosen.ZoneID, name, err)
	}
	return chosen, zone, nil
}

// newestRenderableByName picks the row a name refers to: the most recently
// created row that owns (or will own) infrastructure.
func newestRenderableByName(qubes []*models.Qube, name string) *models.Qube {
	var chosen *models.Qube
	for _, q := range qubes {
		if q.Name != name || !isRenderable(q) {
			continue
		}
		if chosen == nil || q.CreatedAt.After(chosen.CreatedAt) {
			chosen = q
		}
	}
	return chosen
}
