package service

import (
	"context"
	"errors"
	"fmt"
	"log"

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
// so the mapping is explicit and anything unrecognized is rejected rather than
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
		// seen tracks which row currently owns each terraform map key, so a name
		// collision resolves by age rather than by list order.
		seen := make(map[string]*models.Qube, len(qubes))

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
			if errors.Is(err, errNoInfrastructure) {
				// Rendering it can only fail, and failing the snapshot would wedge
				// every OTHER qube's applies too — one row that never got off the
				// ground would freeze the whole fleet. Skipping is safe precisely
				// because it owns nothing: the prevent_destroy hazard that forces
				// released qubes to stay in the map does not apply to a qube that
				// never had infrastructure to protect.
				log.Printf("tfvars: skipping qube %q (%s): %v", q.Name, q.ID, err)
				continue
			}
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
			// The NAME is the terraform map key, but names are not unique across
			// rows: a released-but-not-yet-purged qube coexists with a freshly
			// created one of the same name during a delete/recreate. Both render
			// into the same key, and whichever the list yields last silently wins.
			//
			// That is not a cosmetic race. It was observed reporting success while
			// the compute VM was never built: the torn-down row's
			// compute_running=false overwrote the new row's true, terraform saw
			// nothing to create, and the job reported success for a qube that did
			// not exist.
			//
			// The newest row is by definition the current intent, so it wins —
			// deterministically, and loudly.
			if prev, clash := seen[q.Name]; clash {
				if !q.CreatedAt.After(prev.CreatedAt) {
					log.Printf("tfvars: qube name %q is used by %s and %s; keeping the newer row %s",
						q.Name, prev.ID, q.ID, prev.ID)
					continue
				}
				log.Printf("tfvars: qube name %q is used by %s and %s; keeping the newer row %s",
					q.Name, prev.ID, q.ID, q.ID)
			}
			seen[q.Name] = q
			out[q.Name] = entry
		}
		return out, nil
	}
}

// errNoInfrastructure marks a qube that terraform could never have built
// anything for, so it can be left out of the map instead of failing the whole
// snapshot.
var errNoInfrastructure = errors.New("qube owns no infrastructure")

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
	if zone.Type == models.ZoneTypeGCP {
		if err := renderGCP(entry, zone); err != nil {
			return nil, err
		}
	}
	return entry, nil
}

// renderGCP adds the GCP-specific placement fields.
//
// Refuses rather than defaults on the two that cannot be guessed. A missing
// compute zone means the data disk and the instance can land in different zones
// and the disk simply will not attach; a missing identity bucket means the
// agent's identity has nowhere to go, and the VM boots without one — a machine
// that looks provisioned and whose agent can never authenticate. Both are
// cheaper to refuse here than to diagnose on a running instance.
func renderGCP(entry map[string]any, zone *models.Zone) error {
	gc := zone.Config.GCP
	if gc == nil {
		return fmt.Errorf(
			"zone %q has no gcp config; set zone, identity_bucket and credential_id", zone.Name)
	}
	if gc.Zone == "" {
		return fmt.Errorf(
			"zone %q has no compute zone; the data disk and the instance must share one "+
				"or the disk cannot be attached", zone.Name)
	}
	entry["gcp_zone"] = gc.Zone

	if gc.SourceImage != "" {
		entry["source_image"] = gc.SourceImage
	}
	// Only required when an identity is actually being delivered; the module
	// skips identity delivery entirely when either side is empty.
	if _, wantsIdentity := entry["agent_user_data_file"]; wantsIdentity && gc.IdentityBucket == "" {
		return fmt.Errorf(
			"zone %q has an agent identity to deliver but no identity_bucket; the identity "+
				"cannot go in instance metadata because terraform would write the agent's "+
				"private key into state", zone.Name)
	}
	if gc.IdentityBucket != "" {
		entry["identity_bucket"] = gc.IdentityBucket
	}
	if gc.ServiceAccountEmail != "" {
		entry["service_account_email"] = gc.ServiceAccountEmail
	}
	if gc.Network != "" {
		entry["network"] = gc.Network
	}
	if gc.Subnetwork != "" {
		entry["subnetwork"] = gc.Subnetwork
	}
	entry["assign_public_ip"] = gc.AssignPublicIP
	return nil
}

// computeRunning maps a qube's status onto the terraform switch.
//
// Transient statuses report the state being moved TOWARD, because the render
// happens immediately before the apply that performs the move.
//
// It is also the console's answer to "does this qube have a compute instance at
// all", and the probe and certificate-reissue paths ask it here rather than
// keeping their own list of statuses (see ProbeAgent and
// reissueIdentityForResume in qube_service.go). One predicate, because a
// disagreement between "terraform will not build a VM for this" and "there is a
// VM here to talk to" is precisely how a qube ends up being probed at an address
// that DHCP has since handed to somebody else, or having its identity file
// rewritten underneath a live instance.
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
		// No node means terraform was never able to build anything for this
		// qube: a node is required to create the VM at all. So this is not a
		// qube whose infrastructure we must keep describing — it is one that
		// never had any.
		return fmt.Errorf("%w: zone %q has no default node and qube %q was never placed on one",
			errNoInfrastructure, zone.Name, q.Name)
	}

	entry["node_name"] = node
	entry["template_vm_id"] = pc.TemplateVMID
	// Where the template LIVES, as opposed to where the qube runs. Omitting it
	// when the scheduler places a qube away from the template's node makes the
	// clone fail with "unable to find configuration file".
	if pc.TemplateNode != "" {
		entry["template_node_name"] = pc.TemplateNode
	}
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
