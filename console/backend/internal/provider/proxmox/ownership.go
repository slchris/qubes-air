package proxmox

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/provider"
)

// A reserved VMID is not ownership: another allocator can win that ID. The
// marker is written by the create request itself and checked before adoption
// or deletion. Cluster administrators remain inside the provider trust boundary.
func ownerMarker(q *models.Qube, role string) string {
	return fmt.Sprintf("qubes-air:%x:%s", sha256.Sum256([]byte(q.ID)), role)
}

func (a *Adapter) ownedConfig(ctx context.Context, q *models.Qube, node string, vmid int, role string) (map[string]any, error) {
	var cfg map[string]any
	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/config", url.PathEscape(node), vmid)
	if err := a.client.get(ctx, path, &cfg); err != nil {
		return nil, err
	}
	if cfg["description"] != ownerMarker(q, role) {
		return nil, fmt.Errorf("proxmox: VM %d ownership mismatch; refusing %s operation", vmid, role)
	}
	if lock, _ := cfg["lock"].(string); lock != "" {
		return nil, fmt.Errorf("proxmox: VM %d remains locked; retry after the provider task settles", vmid)
	}
	return cfg, nil
}

// adoptCompute finishes configuration interrupted after clone. An already
// configured static address is preserved rather than probing its own lease.
func (a *Adapter) adoptCompute(ctx context.Context, pc *models.ProxmoxZoneConfig, node string, q *models.Qube, in provider.Infra) (provider.Infra, bool, error) {
	exists, err := a.vmExists(ctx, node, in.ComputeVMID)
	if err != nil || !exists {
		return in, false, err
	}
	cfg, err := a.ownedConfig(ctx, q, node, in.ComputeVMID, "compute")
	if err != nil {
		return in, false, err
	}
	if in.ObservedState != vmStatusRunning {
		ipconfig, _ := cfg["ipconfig0"].(string)
		if err := a.configureCompute(ctx, pc, node, in.ComputeVMID, q, &in, ipconfig); err != nil {
			return in, false, err
		}
	}
	if err := a.ensureStarted(ctx, node, in.ComputeVMID); err != nil {
		return in, false, err
	}
	in.ObservedState = vmStatusRunning
	return in, true, nil
}

// createCompute checkpoints the reservation before sending the clone request.
func (a *Adapter) createCompute(ctx context.Context, pc *models.ProxmoxZoneConfig, node string, q *models.Qube, in provider.Infra) (provider.Infra, error) {
	vmid := in.ComputeVMID
	if vmid == 0 {
		var err error
		vmid, err = a.nextID(ctx)
		if err != nil {
			return in, err
		}
	}
	in.ComputeVMID = vmid
	if err := provider.Checkpoint(ctx, in); err != nil {
		return in, err
	}
	if err := a.cloneTemplate(ctx, pc, node, vmid, q); err != nil {
		return in, err
	}
	if err := a.configureCompute(ctx, pc, node, vmid, q, &in, ""); err != nil {
		return in, err
	}
	if err := a.ensureStarted(ctx, node, vmid); err != nil {
		return in, err
	}

	in.ComputeVMID = vmid
	in.ObservedState = vmStatusRunning
	return in, nil
}
