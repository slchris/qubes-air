package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/pki"
	"github.com/slchris/qubes-air/console/internal/scheduler"
	"github.com/stretchr/testify/require"
)

func networkScheduler(t *testing.T, networks map[string]string) *ClusterScheduler {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "PVEAPIToken=test@pve!reader=test", r.Header.Get("Authorization"))
		if r.URL.Path == "/api2/json/cluster/resources" {
			fmt.Fprint(w, `{"data":[{"type":"node","node":"node-a","status":"online","maxmem":137438953472,"mem":0},{"type":"node","node":"node-b","status":"online","maxmem":68719476736,"mem":0}]}`)
			return
		}
		if body, ok := networks[r.URL.Path]; ok {
			fmt.Fprint(w, body)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	zone := schedZone("node-a")
	zone.Config.Proxmox.NetworkBridge = "vmbr7"
	zone.Config.Proxmox.VLANTag = 42
	return NewClusterScheduler(&stubZoneRepo{zone: zone}, func(context.Context, string) (scheduler.Credentials, error) {
		return scheduler.Credentials{
			Endpoint: srv.URL, APIToken: "test@pve!reader=test",
			CAPEM: pki.EncodeCACertPEM(&pki.CA{Cert: srv.Certificate()}),
		}, nil
	})
}

const eligibleNetwork = `{"data":[{"iface":"vmbr7","type":"bridge","active":1,"bridge_vlan_aware":1,"bridge_vids":"2-4094"}]}`

func TestPlacementSkipsEmptierNodeWithoutZoneNetwork(t *testing.T) {
	cs := networkScheduler(t, map[string]string{
		"/api2/json/nodes/node-a/network": `{"data":[]}`,
		"/api2/json/nodes/node-b/network": eligibleNetwork,
	})
	p, err := cs.Place(context.Background(), "z1", scheduler.Requirements{MemoryMB: 2048})
	require.NoError(t, err)
	require.Equal(t, "node-b", p.Node, "free memory cannot make a node without the zone bridge eligible")
	require.Len(t, p.Considered, 2)
	require.False(t, p.Considered[0].Eligible)
	require.Contains(t, p.Considered[0].Reason, "vmbr7")
}

func TestPlacementNoZoneNetworkDoesNotFallBack(t *testing.T) {
	cs := networkScheduler(t, map[string]string{
		"/api2/json/nodes/node-a/network": `{"data":[]}`,
		"/api2/json/nodes/node-b/network": `{"data":[]}`,
	})
	svc := &QubeServiceImpl{placer: cs}
	node, _, err := svc.resolvePlacement(context.Background(), schedQube(""), schedZone("node-a"))
	require.Error(t, err, "a network rejection must not silently fall back to the same ineligible node")
	require.Empty(t, node)
}

func TestPlacementNetworkFailureAndPendingConfig(t *testing.T) {
	for _, body := range []string{
		`{"changes":"not applied","data":[{"iface":"vmbr7","type":"bridge","active":1,"bridge_vlan_aware":1,"bridge_vids":"42"}]}`,
		`{"data":[{"iface":"vmbr7","type":"bridge","active":1,"bridge_vlan_aware":1,"bridge_vids":"43"}]}`,
		`invalid JSON`,
	} {
		cs := networkScheduler(t, map[string]string{
			"/api2/json/nodes/node-a/network": body,
			"/api2/json/nodes/node-b/network": eligibleNetwork,
		})
		p, err := cs.Place(context.Background(), "z1", scheduler.Requirements{MemoryMB: 2048})
		require.NoError(t, err)
		require.Equal(t, "node-b", p.Node)
	}
	cs := networkScheduler(t, nil) // Both network reads return HTTP 404.
	_, err := cs.Place(context.Background(), "z1", scheduler.Requirements{MemoryMB: 2048})
	require.ErrorIs(t, err, scheduler.ErrNetworkUnavailable)
}

func TestPlacementCanceledRequestCannotUseDefault(t *testing.T) {
	cs := networkScheduler(t, nil)
	svc := &QubeServiceImpl{placer: cs}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	node, _, err := svc.resolvePlacement(ctx, schedQube(""), schedZone("node-a"))
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, node)
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	node, _, err = svc.resolvePlacement(ctx, schedQube(""), schedZone("node-a"))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Empty(t, node)
}
