package proxmox

import (
	"context"
	"fmt"
	"net/http"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/proxmoxnetwork"
)

func (a *Adapter) checkNetwork(ctx context.Context, node string, pc *models.ProxmoxZoneConfig) error {
	req := proxmoxnetwork.Requirement{Bridge: pc.NetworkBridge, VLANTag: pc.VLANTag}
	if err := req.Validate(); err != nil {
		return err
	}
	path, err := proxmoxnetwork.Path(node)
	if err != nil {
		return err
	}
	raw, err := a.client.requestRaw(ctx, http.MethodGet, path, nil)
	if err != nil {
		return fmt.Errorf("node %q network preflight: %w", node, err)
	}
	if err := req.Check(raw); err != nil {
		return fmt.Errorf("node %q network preflight: %w", node, err)
	}
	return nil
}
