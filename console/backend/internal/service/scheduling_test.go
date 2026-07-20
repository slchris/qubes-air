package service

import (
	"context"
	"errors"
	"testing"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/slchris/qubes-air/console/internal/scheduler"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubPlacer returns a fixed decision.
type stubPlacer struct {
	node   string
	err    error
	called bool
}

func (s *stubPlacer) Place(context.Context, string, scheduler.Requirements) (*scheduler.Placement, error) {
	s.called = true
	if s.err != nil {
		return nil, s.err
	}
	return &scheduler.Placement{Node: s.node, Reason: "stub"}, nil
}

func schedZone(defaultNode string) *models.Zone {
	return &models.Zone{
		ID: "z1", Name: "infra", Type: models.ZoneTypeProxmox,
		Config: models.ZoneConfig{Proxmox: &models.ProxmoxZoneConfig{Node: defaultNode}},
	}
}

func schedQube(pinned string) *models.Qube {
	return &models.Qube{
		ID: "q1", Name: "dev-work", ZoneID: "z1",
		Spec: models.QubeSpec{Memory: 8192, VCPU: 4, Node: pinned},
	}
}

// TestPlacementPinWins — an explicit node beats the scheduler. Automatic
// placement is a convenience, not a policy that overrides an operator.
func TestPlacementPinWins(t *testing.T) {
	placer := &stubPlacer{node: "infra-node4"}
	svc := &QubeServiceImpl{placer: placer}

	node, reason, err := svc.resolvePlacement(context.Background(), schedQube("infra-node2"), schedZone("infra-node1"))
	require.NoError(t, err)
	assert.Equal(t, "infra-node2", node)
	assert.Contains(t, reason, "pinned")
	assert.False(t, placer.called, "the scheduler must not even be consulted when a node is pinned")
}

// TestPlacementSchedulerBeatsZoneDefault — this is the whole point. On the real
// cluster the zone default (infra-node1) was the most loaded node available.
func TestPlacementSchedulerBeatsZoneDefault(t *testing.T) {
	svc := &QubeServiceImpl{placer: &stubPlacer{node: "infra-node4"}}

	node, _, err := svc.resolvePlacement(context.Background(), schedQube(""), schedZone("infra-node1"))
	require.NoError(t, err)
	assert.Equal(t, "infra-node4", node, "the scheduler's choice must win over a static default")
}

// TestPlacementCapacityErrorIsFatal — when the cluster genuinely has no room,
// falling back to a default node would place a qube that cannot fit. Proxmox
// accepts the overcommit and the node thrashes, so refuse instead.
func TestPlacementCapacityErrorIsFatal(t *testing.T) {
	svc := &QubeServiceImpl{placer: &stubPlacer{err: scheduler.ErrInsufficientCapacity}}

	_, _, err := svc.resolvePlacement(context.Background(), schedQube(""), schedZone("infra-node1"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, scheduler.ErrInsufficientCapacity) || isCapacityError(err))
}

// TestPlacementDegradesWhenClusterUnreachable — scheduling is an optimisation.
// An unreachable cluster or a missing credential must not stop a qube being
// created; it falls back to the zone default.
func TestPlacementDegradesWhenClusterUnreachable(t *testing.T) {
	svc := &QubeServiceImpl{placer: &stubPlacer{err: errors.New("dial tcp: connection refused")}}

	node, reason, err := svc.resolvePlacement(context.Background(), schedQube(""), schedZone("infra-node1"))
	require.NoError(t, err, "an unreachable cluster must not block creation")
	assert.Equal(t, "infra-node1", node)
	assert.Contains(t, reason, "zone default")
}

// TestPlacementUnconfiguredZoneDefers — a zone not yet configured leaves the
// node unset rather than refusing. The tfvars renderer already rejects a qube
// that reaches provisioning without one, by name, so the error is deferred to
// the point where it is actionable rather than blocking the row.
func TestPlacementUnconfiguredZoneDefers(t *testing.T) {
	svc := &QubeServiceImpl{}
	zone := &models.Zone{ID: "z1", Name: "bare", Type: models.ZoneTypeProxmox}

	node, reason, err := svc.resolvePlacement(context.Background(), schedQube(""), zone)
	require.NoError(t, err)
	assert.Empty(t, node)
	assert.Contains(t, reason, "provision time")
}

// TestParseProxmoxSecret — the two accepted shapes, told apart by the '!' that
// only ever appears in a token id.
func TestParseProxmoxSecret(t *testing.T) {
	tok := parseProxmoxSecret("terraform@pve!tf=aaaa-bbbb-cccc")
	assert.Equal(t, "terraform@pve!tf=aaaa-bbbb-cccc", tok.APIToken)
	assert.Empty(t, tok.Username, "a token must not be misread as a username")

	pw := parseProxmoxSecret("terraform@pve:hunter2")
	assert.Equal(t, "terraform@pve", pw.Username)
	assert.Equal(t, "hunter2", pw.Password)
	assert.Empty(t, pw.APIToken)

	assert.False(t, parseProxmoxSecret("").Valid())
	assert.False(t, parseProxmoxSecret("garbage").Valid())
}

// TestCredentialsValidRequiresEndpoint — a secret alone is not enough to reach
// a cluster, and a half-configured zone should say so rather than dial nothing.
func TestCredentialsValidRequiresEndpoint(t *testing.T) {
	assert.False(t, scheduler.Credentials{APIToken: "u@pve!t=s"}.Valid())
	assert.True(t, scheduler.Credentials{Endpoint: "https://pve", APIToken: "u@pve!t=s"}.Valid())
	assert.True(t, scheduler.Credentials{Endpoint: "https://pve", Username: "u", Password: "p"}.Valid())
	assert.False(t, scheduler.Credentials{Endpoint: "https://pve", Username: "u"}.Valid())
}

// stubZoneRepo returns a fixed zone.
type stubZoneRepo struct{ zone *models.Zone }

func (s *stubZoneRepo) GetByID(context.Context, string) (*models.Zone, error) { return s.zone, nil }
func (s *stubZoneRepo) Create(context.Context, *models.Zone) error            { return nil }
func (s *stubZoneRepo) List(context.Context, repository.ZoneListOptions) ([]*models.Zone, error) {
	return nil, nil
}
func (s *stubZoneRepo) Update(context.Context, *models.Zone) error         { return nil }
func (s *stubZoneRepo) Delete(context.Context, string) error               { return nil }
func (s *stubZoneRepo) UpdateStatus(context.Context, string, string) error { return nil }
func (s *stubZoneRepo) GetByName(context.Context, string) (*models.Zone, error) {
	return s.zone, nil
}

// TestCapacityCloudZoneIsNotANodePool is the distinction the whole abstraction
// exists for. A cloud has no node to pick: the provider chooses the machine and
// never tells you which. Reporting an empty node pool would have the UI offer a
// node picker with nothing in it; reporting kind=quota tells it to hide node
// selection entirely.
func TestCapacityCloudZoneIsNotANodePool(t *testing.T) {
	for _, zt := range []models.ZoneType{models.ZoneTypeGCP, models.ZoneTypeAWS, models.ZoneTypeAzure} {
		zone := &models.Zone{ID: "z", Name: "cloud", Type: zt}
		cs := NewClusterScheduler(&stubZoneRepo{zone: zone}, nil)

		got, err := cs.Capacity(context.Background(), "z")
		require.NoError(t, err, "%s", zt)
		assert.Equal(t, CapacityKindQuota, got.Kind, "%s must report quota, not node_pool", zt)
		assert.Empty(t, got.Nodes, "%s must not report nodes", zt)
		assert.NotEmpty(t, got.Note, "an unimplemented provider must say so rather than look empty")
	}
}

// TestCapacityUnknownZoneType — an unrecognized provider says so instead of
// silently presenting as one of the known models.
func TestCapacityUnknownZoneType(t *testing.T) {
	zone := &models.Zone{ID: "z", Name: "weird", Type: models.ZoneType("kubevirt")}
	cs := NewClusterScheduler(&stubZoneRepo{zone: zone}, nil)

	got, err := cs.Capacity(context.Background(), "z")
	require.NoError(t, err)
	assert.Equal(t, CapacityKindUnknown, got.Kind)
	assert.Contains(t, got.Note, "kubevirt")
}
