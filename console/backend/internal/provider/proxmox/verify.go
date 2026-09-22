package proxmox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/provider"
)

func (a *Adapter) requireVMAbsent(ctx context.Context, node string, id int) error {
	if id == 0 {
		return nil
	}
	if node == "" {
		return errors.New("proxmox: cannot verify VM absence without a node")
	}
	exists, err := a.vmExists(ctx, node, id)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("proxmox: VM %d remains after deletion", id)
	}
	return nil
}

// volumePresent requires a successful datastore inventory. A failed query is
// never evidence of absence; a missing holder does not imply a missing disk.
func (a *Adapter) volumePresent(ctx context.Context, node, volume string) (bool, error) {
	store, _, ok := strings.Cut(volume, ":")
	if !ok || store == "" || node == "" {
		return false, errors.New("proxmox: incomplete volume identity")
	}
	if err := a.requireStorageVisibility(ctx, store); err != nil {
		return false, err
	}
	var contents []struct {
		Volume string `json:"volid"`
	}
	path := fmt.Sprintf("/api2/json/nodes/%s/storage/%s/content", url.PathEscape(node), url.PathEscape(store))
	if err := a.client.get(ctx, path, &contents); err != nil {
		return false, err
	}
	if contents == nil {
		return false, errors.New("proxmox: missing volume inventory")
	}
	if err := a.requireStorageVisibility(ctx, store); err != nil {
		return false, err
	}
	for _, item := range contents {
		if item.Volume == volume {
			return true, nil
		}
	}
	return false, nil
}

// VerifyDestroyed checks every recorded provider resource independently.
// Residual volumes retain the infra row for investigation and explicit retry.
func (a *Adapter) VerifyDestroyed(ctx context.Context, _ *models.Qube, in provider.Infra) error {
	for _, id := range []int{in.ComputeVMID, in.StorageVMID} {
		if err := a.requireVMAbsent(ctx, in.Node, id); err != nil {
			return err
		}
	}
	if in.StorageVMID != 0 && in.DataVolume == "" {
		return errors.New("proxmox: data-volume identity unresolved; cannot certify destruction")
	}
	for _, volume := range []string{in.DataVolume, in.IdentityVol} {
		if volume == "" {
			continue
		}
		present, err := a.volumePresent(ctx, in.Node, volume)
		if err != nil {
			return err
		}
		if present {
			return fmt.Errorf("proxmox: recorded volume %q remains; cleanup not complete", volume)
		}
	}
	return nil
}

// PVE silently filters inventory entries without volume access. Datastore.Allocate
// grants unfiltered access; its map value is propagation, not the permission bit.
func (a *Adapter) requireStorageVisibility(ctx context.Context, store string) error {
	path := "/storage/" + store
	var permissions map[string]map[string]json.RawMessage
	if err := a.client.get(ctx, "/api2/json/access/permissions?path="+url.QueryEscape(path), &permissions); err != nil {
		return err
	}
	if _, ok := permissions[path]["Datastore.Allocate"]; !ok {
		return errors.New("proxmox: Datastore.Allocate required to verify unfiltered volume inventory")
	}
	return nil
}
