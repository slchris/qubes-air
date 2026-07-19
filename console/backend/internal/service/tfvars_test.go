package service

import (
	"testing"

	"github.com/slchris/qubes-air/console/internal/models"
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
