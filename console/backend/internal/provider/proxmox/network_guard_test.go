package proxmox

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/provider"
	"github.com/stretchr/testify/require"
)

func TestNetworkGuardBeforeResourceAllocation(t *testing.T) {
	for _, phase := range []string{"storage", "compute"} {
		t.Run(phase, func(t *testing.T) {
			var writes atomic.Int32
			ad, rec := newRawTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					writes.Add(1)
				}
				switch {
				case strings.HasSuffix(r.URL.Path, "/network"):
					writeData(w, []any{})
				case strings.HasSuffix(r.URL.Path, "/cluster/nextid"):
					writeData(w, "105")
				case strings.Contains(r.URL.Path, "/tasks/"):
					taskOK(w)
				case r.Method != http.MethodGet:
					writeData(w, testUPID)
				default:
					writeData(w, map[string]string{"scsi0": "test:vm-105-disk-0,size=20G"})
				}
			}, Options{})
			q := testQube()
			q.Spec.Node = "node-pin"
			in := provider.Infra{}
			var err error
			if phase == "storage" {
				_, err = ad.EnsureStorage(testCheckpointContext(), q, testZone(), in)
			} else {
				in.Node, in.StorageVMID, in.DataVolume = "node-pin", 104, "test:vm-104-disk-0"
				_, err = ad.EnsureCompute(testCheckpointContext(), q, testZone(), in)
			}
			require.Error(t, err)
			require.Zero(t, writes.Load(), "pinned nodes and resumed compute must pass network preflight before any provider mutation")
			require.False(t, rec.has(http.MethodGet, "/api2/json/cluster/nextid"), "do not reserve a VM ID for an ineligible node")
		})
	}
}

func TestNetworkPreflightRefusalsDoNotCheckpoint(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{name: "missing bridge", body: `{"data":[]}`},
		{name: "inactive", body: `{"data":[{"iface":"vmbr0","type":"bridge","active":0}]}`},
		{name: "wrong VLAN", body: `{"data":[{"iface":"vmbr0","type":"bridge","active":1,"bridge_vlan_aware":1,"bridge_vids":"43"}]}`},
		{name: "unapplied change", body: `{"changes":"pending","data":[{"iface":"vmbr0","type":"bridge","active":1}]}`},
		{name: "unreadable", status: http.StatusForbidden, body: `{"data":[]}`},
		{name: "corrupt", body: `{"data":`},
		{name: "oversized", body: `{"data":[]}` + strings.Repeat(" ", 8<<20)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ad, rec := newRawTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, http.MethodGet, r.Method)
				require.Equal(t, "/api2/json/nodes/infra-node1/network", r.URL.Path)
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				fmt.Fprint(w, tc.body)
			}, Options{})
			ctx := provider.WithCheckpoint(context.Background(), func(context.Context, provider.Infra) error {
				t.Error("network refusal must not checkpoint an allocation")
				return nil
			})
			zone := testZone()
			zone.Config.Proxmox.VLANTag = 42
			out, err := ad.EnsureStorage(ctx, testQube(), zone, provider.Infra{})
			require.Error(t, err)
			require.Equal(t, provider.Infra{}, out)
			require.False(t, rec.has(http.MethodGet, "/api2/json/cluster/nextid"))
		})
	}
}

func TestNetworkPreflightCancellationAndTimeout(t *testing.T) {
	ad, _ := newRawTestAdapter(t, func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ad.EnsureStorage(ctx, testQube(), testZone(), provider.Infra{})
	require.ErrorIs(t, err, context.Canceled)
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = ad.EnsureCompute(ctx, testQube(), testZone(), provider.Infra{DataVolume: "test:vm-104-disk-0"})
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestNetworkGuardValidatesNamesBeforeAPI(t *testing.T) {
	ad, _ := newRawTestAdapter(t, func(http.ResponseWriter, *http.Request) {
		t.Error("invalid node or bridge must never reach the API")
	}, Options{})
	q, zone := testQube(), testZone()
	q.Spec.Node = "../node"
	_, err := ad.EnsureStorage(context.Background(), q, zone, provider.Infra{})
	require.ErrorContains(t, err, "invalid Proxmox node")
	q.Spec.Node = "node-a"
	zone.Config.Proxmox.NetworkBridge = "vmbr0,tag=4"
	_, err = ad.EnsureCompute(context.Background(), q, zone, provider.Infra{DataVolume: "test:vm-104-disk-0"})
	require.ErrorContains(t, err, "network_bridge")
}
