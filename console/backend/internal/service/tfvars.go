package service

import (
	"context"
	"fmt"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/repository"
)

// IdentityLocator reports where a qube's rendered agent identity file lives.
type IdentityLocator interface {
	IdentityPath(qubeName string) string
}

// zoneNameForProvider maps a zone type onto the terraform zone key that
// main.tf's zone_provider lookup understands.
//
// An unknown key would silently fall back to proxmox in the terraform config,
// so the mapping is explicit and anything unrecognised is rejected rather than
// quietly provisioned on the wrong cloud.
var zoneNameForProvider = map[models.ZoneType]string{
	models.ZoneTypeProxmox: "proxmox-zone",
	models.ZoneTypeGCP:     "gcp-zone",
	models.ZoneTypeAWS:     "aws-zone",
}

// NewQubeSnapshot returns the function the terraform executor calls to learn
// which qubes should exist. It is the seam that makes the database, rather than
// a hand-edited tfvars file, the source of truth.
//
// Every rendered entry corresponds to a row; every row that still owns
// infrastructure is rendered. Those two halves are what keep terraform's view
// and the console's view from drifting apart.
func NewQubeSnapshot(qubeRepo repository.QubeRepository, zoneRepo repository.ZoneRepository, identity IdentityLocator) func(context.Context) (map[string]any, error) {
	return func(ctx context.Context) (map[string]any, error) {
		opts := repository.DefaultQubeListOptions()
		opts.Limit = 10000 // effectively unbounded: a partial map would destroy qubes
		qubes, err := qubeRepo.List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("list qubes: %w", err)
		}

		zones := map[string]*models.Zone{}
		out := make(map[string]any, len(qubes))

		for _, q := range qubes {
			if !isRenderable(q) {
				continue
			}
			zone, ok := zones[q.ZoneID]
			if !ok {
				zone, err = zoneRepo.GetByID(ctx, q.ZoneID)
				if err != nil {
					return nil, fmt.Errorf("qube %q: load zone %q: %w", q.Name, q.ZoneID, err)
				}
				zones[q.ZoneID] = zone
			}
			entry, err := renderQube(q, zone)
			if err != nil {
				return nil, fmt.Errorf("qube %q: %w", q.Name, err)
			}
			// Point terraform at the identity file, if one was rendered. The
			// PATH is passed, never the content: terraform's source_file keeps
			// only the path and volume id in state, while inlining the content
			// would put a private key there in plaintext.
			if identity != nil {
				if path := identity.IdentityPath(q.Name); path != "" {
					entry["agent_user_data_file"] = path
				}
			}
			out[q.Name] = entry
		}
		return out, nil
	}
}

// isRenderable reports whether a qube must appear in var.remote_qubes.
//
// A released qube is still rendered on purpose: its storage-holder VM remains
// in terraform state, and that VM carries lifecycle.prevent_destroy. Dropping
// the qube from the map does NOT let terraform destroy it — instead every
// subsequent plan fails, for every qube, because terraform sees an orphaned
// instance it is forbidden to remove. Only a purge (which lifts the guard and
// destroys the disk) may take a qube out of the map.
func isRenderable(q *models.Qube) bool {
	return q.ZoneID != "" && q.Status != models.QubeStatusPending
}

// renderQube converts one row into a var.remote_qubes entry.
//
// Keys mirror terraform/variables.tf. Only fields the module actually consumes
// are emitted, so a value appearing here means it has an effect.
func renderQube(q *models.Qube, zone *models.Zone) (map[string]any, error) {
	zoneKey, ok := zoneNameForProvider[zone.Type]
	if !ok {
		return nil, fmt.Errorf("zone type %q has no terraform mapping", zone.Type)
	}

	entry := map[string]any{
		"zone": zoneKey,
		"type": string(q.Type),
		// compute_running is the compute/storage separation switch: false
		// destroys the compute VM and keeps the data disk. A released or
		// suspended qube must render false, otherwise the next apply would
		// silently rebuild the instance the user asked us to release.
		"compute_running": computeRunning(q.Status),
	}

	// Only emit sizes that were actually chosen. Omitting them lets the module's
	// per-type presets apply; emitting a zero would pin the qube to a 0 GB disk.
	if q.Spec.VCPU > 0 {
		entry["cpu"] = q.Spec.VCPU
	}
	if q.Spec.Memory > 0 {
		entry["memory"] = q.Spec.Memory
	}
	if q.Spec.Disk > 0 {
		entry["disk"] = q.Spec.Disk
	}
	if q.Spec.DataDiskGB > 0 {
		entry["data_disk_gb"] = q.Spec.DataDiskGB
	}
	if q.Spec.GPU != nil {
		entry["gpu_type"] = q.Spec.GPU.Type
		entry["gpu_count"] = q.Spec.GPU.Count
	}

	if zone.Type == models.ZoneTypeProxmox {
		if err := renderProxmox(entry, q, zone); err != nil {
			return nil, err
		}
	}
	return entry, nil
}

// computeRunning maps a qube's status onto the terraform switch.
//
// Transient statuses report the state being moved TOWARD, because the render
// happens immediately before the apply that performs the move.
func computeRunning(status models.QubeStatus) bool {
	switch status {
	case models.QubeStatusRunning, models.QubeStatusCreating, models.QubeStatusResuming:
		return true
	default:
		// stopped, suspended, released, suspending, deleting, error
		return false
	}
}

// renderProxmox adds the Proxmox-specific placement fields.
func renderProxmox(entry map[string]any, q *models.Qube, zone *models.Zone) error {
	pc := zone.Config.Proxmox
	if pc == nil {
		return fmt.Errorf("zone %q has no proxmox config; set node, datastore_id, network_bridge and template_vm_id", zone.Name)
	}
	if pc.TemplateVMID <= 0 {
		return fmt.Errorf("zone %q has no template_vm_id; without a template the clone produces a VM with no operating system", zone.Name)
	}

	// A qube may pin its own node; otherwise the zone default applies. One of
	// them must resolve: terraform needs a concrete node to place the VM on.
	node := q.Spec.Node
	if node == "" {
		node = pc.Node
	}
	if node == "" {
		return fmt.Errorf("zone %q has no node and qube %q does not pin one", zone.Name, q.Name)
	}

	entry["node_name"] = node
	entry["template_vm_id"] = pc.TemplateVMID
	if pc.DatastoreID != "" {
		entry["datastore_id"] = pc.DatastoreID
	}
	if pc.NetworkBridge != "" {
		entry["network_bridge"] = pc.NetworkBridge
	}
	// Public keys only. A private key must never reach terraform state.
	if len(pc.SSHPublicKeys) > 0 {
		entry["ssh_public_keys"] = pc.SSHPublicKeys
	}
	return nil
}
