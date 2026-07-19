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

func proxmoxZone() *models.Zone {
	return &models.Zone{
		ID:   "z1",
		Name: "infra",
		Type: models.ZoneTypeProxmox,
		Config: models.ZoneConfig{
			Proxmox: &models.ProxmoxZoneConfig{
				Node:          "infra-node1",
				DatastoreID:   "ceph-pve",
				NetworkBridge: "vmbr0",
				TemplateVMID:  901,
				SSHPublicKeys: []string{"ssh-ed25519 AAAA... operator"},
			},
		},
	}
}

func qubeWith(status models.QubeStatus, spec models.QubeSpec) *models.Qube {
	return &models.Qube{
		ID: "q1", Name: "dev-work", ZoneID: "z1",
		Type: models.QubeTypeWork, Status: status, Spec: spec,
	}
}

func TestRenderQube_ProxmoxFields(t *testing.T) {
	q := qubeWith(models.QubeStatusRunning, models.QubeSpec{
		VCPU: 4, Memory: 8192, Disk: 32, DataDiskGB: 50,
	})
	got, err := renderQube(q, proxmoxZone())
	require.NoError(t, err)

	assert.Equal(t, "proxmox-zone", got["zone"])
	assert.Equal(t, "work", got["type"])
	assert.Equal(t, true, got["compute_running"])
	assert.Equal(t, 4, got["cpu"])
	assert.Equal(t, 8192, got["memory"])
	assert.Equal(t, 32, got["disk"])
	assert.Equal(t, 50, got["data_disk_gb"])
	assert.Equal(t, "infra-node1", got["node_name"])
	assert.Equal(t, 901, got["template_vm_id"])
	assert.Equal(t, "ceph-pve", got["datastore_id"])
	assert.Equal(t, "vmbr0", got["network_bridge"])
}

// TestRenderQube_OmitsUnsetSizes — emitting a zero would pin the qube to a 0 GB
// disk instead of letting the module's per-type preset apply.
func TestRenderQube_OmitsUnsetSizes(t *testing.T) {
	got, err := renderQube(qubeWith(models.QubeStatusRunning, models.QubeSpec{}), proxmoxZone())
	require.NoError(t, err)

	for _, k := range []string{"cpu", "memory", "disk", "data_disk_gb"} {
		_, present := got[k]
		assert.False(t, present, "%s must be omitted when unset so the preset applies", k)
	}
}

// TestComputeRunningFollowsIntent is the switch that makes suspend/resume work.
// A suspended or released qube rendering true would have the next apply
// silently rebuild the compute instance the user asked us to release — and
// start billing for it again.
func TestComputeRunningFollowsIntent(t *testing.T) {
	running := []models.QubeStatus{
		models.QubeStatusRunning, models.QubeStatusCreating, models.QubeStatusResuming,
	}
	notRunning := []models.QubeStatus{
		models.QubeStatusStopped, models.QubeStatusSuspended, models.QubeStatusReleased,
		models.QubeStatusSuspending, models.QubeStatusDeleting, models.QubeStatusError,
	}
	for _, s := range running {
		assert.True(t, computeRunning(s), "%s must render compute_running=true", s)
	}
	for _, s := range notRunning {
		assert.False(t, computeRunning(s), "%s must render compute_running=false", s)
	}
}

// TestRenderQube_NodePinOverridesZoneDefault — a qube may pin its own node,
// which matters on shared storage where it can run anywhere.
func TestRenderQube_NodePinOverridesZoneDefault(t *testing.T) {
	q := qubeWith(models.QubeStatusRunning, models.QubeSpec{Node: "infra-node4"})
	got, err := renderQube(q, proxmoxZone())
	require.NoError(t, err)
	assert.Equal(t, "infra-node4", got["node_name"])
}

// TestRenderQube_RejectsIncompleteZone — these are failures we want loudly at
// render time. A missing template_vm_id would otherwise clone nothing and
// produce a VM with no operating system, and terraform would report success.
func TestRenderQube_RejectsIncompleteZone(t *testing.T) {
	t.Run("no proxmox config", func(t *testing.T) {
		z := proxmoxZone()
		z.Config.Proxmox = nil
		_, err := renderQube(qubeWith(models.QubeStatusRunning, models.QubeSpec{}), z)
		assert.ErrorContains(t, err, "no proxmox config")
	})

	t.Run("no template", func(t *testing.T) {
		z := proxmoxZone()
		z.Config.Proxmox.TemplateVMID = 0
		_, err := renderQube(qubeWith(models.QubeStatusRunning, models.QubeSpec{}), z)
		assert.ErrorContains(t, err, "template_vm_id")
	})

	t.Run("no node anywhere", func(t *testing.T) {
		z := proxmoxZone()
		z.Config.Proxmox.Node = ""
		_, err := renderQube(qubeWith(models.QubeStatusRunning, models.QubeSpec{}), z)
		assert.ErrorContains(t, err, "node")
	})
}

// TestRenderQube_RejectsUnmappedZoneType — terraform's zone_provider lookup
// falls back to proxmox for an unknown key, which would provision on the wrong
// platform. Fail here instead.
func TestRenderQube_RejectsUnmappedZoneType(t *testing.T) {
	z := proxmoxZone()
	z.Type = models.ZoneTypeAzure
	_, err := renderQube(qubeWith(models.QubeStatusRunning, models.QubeSpec{}), z)
	assert.ErrorContains(t, err, "no terraform mapping")
}

// TestIsRenderable_ReleasedQubesStayRendered is the subtle one, and getting it
// wrong breaks the whole stack: a released qube's storage VM is still in
// terraform state behind lifecycle.prevent_destroy. Dropping it from the map
// does not let terraform destroy it — every later plan fails instead, for every
// qube, because terraform sees an orphan it is forbidden to remove.
func TestIsRenderable_ReleasedQubesStayRendered(t *testing.T) {
	released := qubeWith(models.QubeStatusReleased, models.QubeSpec{})
	assert.True(t, isRenderable(released),
		"a released qube must stay in the map: its prevent_destroy storage VM is still in state")

	for _, s := range []models.QubeStatus{
		models.QubeStatusRunning, models.QubeStatusSuspended,
		models.QubeStatusError, models.QubeStatusDeleting,
	} {
		assert.True(t, isRenderable(qubeWith(s, models.QubeSpec{})), "%s must be rendered", s)
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

// TestRenderQube_OnlyPublicKeys guards a red line: a private key must never
// reach terraform state, which is stored in plaintext.
func TestRenderQube_OnlyPublicKeys(t *testing.T) {
	got, err := renderQube(qubeWith(models.QubeStatusRunning, models.QubeSpec{}), proxmoxZone())
	require.NoError(t, err)

	keys, ok := got["ssh_public_keys"].([]string)
	require.True(t, ok)
	for _, k := range keys {
		assert.NotContains(t, k, "PRIVATE KEY")
		assert.Regexp(t, `^(ssh-|ecdsa-)`, k, "only OpenSSH public keys belong here")
	}
}

// TestRenderQube_EmitsTemplateNode — the clone API must be called on the node
// the template lives on. Real deployment failed here: the scheduler placed a
// qube on infra-node4 while template 901 lived on infra-node1, and Proxmox
// answered "unable to find configuration file for VM 901 on node infra-node4".
func TestRenderQube_EmitsTemplateNode(t *testing.T) {
	zone := proxmoxZone()
	zone.Config.Proxmox.TemplateNode = "infra-node1"
	q := qubeWith(models.QubeStatusRunning, models.QubeSpec{Node: "infra-node4"})

	got, err := renderQube(q, zone)
	require.NoError(t, err)

	assert.Equal(t, "infra-node4", got["node_name"], "the qube runs where it was placed")
	assert.Equal(t, "infra-node1", got["template_node_name"], "but the clone is issued on the template's node")
}

// TestSnapshot_NodelessQubeDoesNotWedgeTheFleet — a qube that never got a node
// owns no infrastructure, because terraform needs a node to create anything at
// all. Failing the snapshot over it would freeze applies for every OTHER qube,
// which is how a single bad row took down the whole fleet in testing.
func TestSnapshot_NodelessQubeDoesNotWedgeTheFleet(t *testing.T) {
	zone := proxmoxZone()
	zone.Config.Proxmox.Node = "" // no zone default either

	healthy := qubeWith(models.QubeStatusRunning, models.QubeSpec{Node: "infra-node4"})
	stranded := qubeWith(models.QubeStatusError, models.QubeSpec{})
	stranded.ID, stranded.Name = "q2", "stranded"

	snap := NewQubeSnapshot(
		&stubQubeLister{qubes: []*models.Qube{healthy, stranded}},
		&stubZoneRepo{zone: zone},
		nil,
	)
	out, err := snap(t.Context())
	require.NoError(t, err, "one stranded qube must not fail the whole snapshot")

	assert.Contains(t, out, "dev-work", "the healthy qube still renders")
	assert.NotContains(t, out, "stranded", "the stranded qube is left out rather than rendered wrong")
}

// stubQubeLister satisfies repository.QubeRepository with only List doing work;
// the snapshot never calls anything else.
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

// TestSnapshot_NameCollisionKeepsNewestRow — the qube NAME is the terraform map
// key, but rows are not unique by name: deleting and recreating a qube leaves
// the released row in place until its job finishes. Both rows render into the
// same key.
//
// Observed in real deployment: the torn-down row's compute_running=false
// overwrote the new row's true, so terraform saw nothing to create and the job
// reported SUCCESS for a compute VM that was never built.
func TestSnapshot_NameCollisionKeepsNewestRow(t *testing.T) {
	old := qubeWith(models.QubeStatusReleased, models.QubeSpec{Node: "infra-node4"})
	old.ID = "old"
	old.CreatedAt = time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)

	fresh := qubeWith(models.QubeStatusCreating, models.QubeSpec{Node: "infra-node4"})
	fresh.ID = "fresh"
	fresh.CreatedAt = old.CreatedAt.Add(time.Minute)

	// Both orderings must agree, or the result depends on how the DB happened
	// to sort the rows — which is exactly the bug.
	for _, order := range [][]*models.Qube{{old, fresh}, {fresh, old}} {
		snap := NewQubeSnapshot(&stubQubeLister{qubes: order}, &stubZoneRepo{zone: proxmoxZone()}, nil)
		out, err := snap(t.Context())
		require.NoError(t, err)

		entry, ok := out["dev-work"].(map[string]any)
		require.True(t, ok, "the name must still render exactly once")
		assert.Equal(t, true, entry["compute_running"],
			"the newer row is the current intent; the released row must not overwrite it")
	}
}
