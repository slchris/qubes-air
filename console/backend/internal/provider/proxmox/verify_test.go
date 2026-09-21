package proxmox

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/slchris/qubes-air/console/internal/provider"
	"github.com/stretchr/testify/require"
)

func TestVerifyDestroyedRequiresIndependentAbsence(t *testing.T) {
	for _, remaining := range []string{"none", "compute", "holder", "data", "identity", "inventory-error", "permission-filtered"} {
		t.Run(remaining, func(t *testing.T) {
			ad, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/access/permissions"):
					permissions := map[string]map[string]int{}
					if remaining != "permission-filtered" {
						permissions[r.URL.Query().Get("path")] = map[string]int{"Datastore.Allocate": 0}
					}
					writeData(w, permissions)
				case strings.Contains(r.URL.Path, "/qemu/"):
					if (remaining == "compute" && strings.Contains(r.URL.Path, "/106/")) ||
						(remaining == "holder" && strings.Contains(r.URL.Path, "/105/")) {
						writeData(w, map[string]string{"status": "stopped"})
					} else {
						http.NotFound(w, r)
					}
				case remaining == "inventory-error":
					http.Error(w, "storage offline", http.StatusServiceUnavailable)
				default:
					volumes := []map[string]string{}
					if remaining == "data" {
						volumes = append(volumes, map[string]string{"volid": "ceph:vm-105-disk-0"})
					}
					if remaining == "identity" {
						volumes = append(volumes, map[string]string{"volid": "local:snippets/agent.yaml"})
					}
					writeData(w, volumes)
				}
			}, Options{})
			err := ad.VerifyDestroyed(context.Background(), testQube(), provider.Infra{
				Node: "node1", ComputeVMID: 106, StorageVMID: 105,
				DataVolume: "ceph:vm-105-disk-0", IdentityVol: "local:snippets/agent.yaml",
			})
			if remaining == "none" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
