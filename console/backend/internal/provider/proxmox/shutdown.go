package proxmox

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/provider"
)

const (
	guestShutdownSeconds = 60
	computeShutdownWait  = 90 * time.Second
)

// prepareComputeDeletion never escalates a guest shutdown into a hard stop.
// A shorter caller deadline wins; a stalled provider task cannot wait forever.
func (a *Adapter) prepareComputeDeletion(ctx context.Context, q *models.Qube, in provider.Infra) error {
	ctx, cancel := context.WithTimeout(ctx, computeShutdownWait)
	defer cancel()

	status, err := a.nonHAComputeStatus(ctx, in.Node, in.ComputeVMID)
	if err != nil {
		return err
	}
	if status == vmStatusRunning {
		a.logf(ctx, "requesting guest shutdown for compute VM %d (timeout %ds, forceStop=0)", in.ComputeVMID, guestShutdownSeconds)
		path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/status/shutdown", url.PathEscape(in.Node), in.ComputeVMID)
		form := url.Values{"timeout": {strconv.Itoa(guestShutdownSeconds)}, "forceStop": {"0"}}
		if err := a.postTask(ctx, in.Node, path, form); err != nil {
			return fmt.Errorf("guest shutdown did not complete: %w", err)
		}
	}

	// Ownership, locks and HA configuration can change while shutdown waits.
	// Cluster administrators remain trusted; these reads are not a provider lock.
	if _, err := a.ownedConfig(ctx, q, in.Node, in.ComputeVMID, "compute"); err != nil {
		return err
	}
	status, err = a.nonHAComputeStatus(ctx, in.Node, in.ComputeVMID)
	if err != nil {
		return err
	}
	if status != vmStatusStopped {
		return fmt.Errorf("compute VM %d is still %s after shutdown; refusing deletion", in.ComputeVMID, status)
	}
	return ctx.Err()
}

// PVE delegates HA shutdowns to its HA manager, whose stop path does not share
// the direct QEMU shutdown forceStop=0 contract. Missing metadata is not proof
// of an unmanaged VM, so only an explicit managed=0 and known state pass.
func (a *Adapter) nonHAComputeStatus(ctx context.Context, node string, vmid int) (string, error) {
	var status struct {
		Status string `json:"status"`
		HA     struct {
			Managed *int `json:"managed"`
		} `json:"ha"`
	}
	path := fmt.Sprintf("/api2/json/nodes/%s/qemu/%d/status/current", url.PathEscape(node), vmid)
	if err := a.client.get(ctx, path, &status); err != nil {
		return "", err
	}
	if status.HA.Managed == nil || *status.HA.Managed != 0 {
		return "", fmt.Errorf("compute VM %d requires explicit non-HA status; refusing shutdown/deletion", vmid)
	}
	if status.Status != vmStatusRunning && status.Status != vmStatusStopped {
		return "", fmt.Errorf("compute VM %d has unknown status %q; refusing shutdown/deletion", vmid, status.Status)
	}
	return status.Status, nil
}
