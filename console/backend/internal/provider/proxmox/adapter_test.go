package proxmox

import (
	"bytes"
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeIdentity struct {
	path string
	vol  string
}

func (f fakeIdentity) IdentityPath(string) string     { return f.path }
func (f fakeIdentity) IdentityVolumeID(string) string { return f.vol }

func newTestAdapter(t *testing.T, h http.HandlerFunc, opts Options) (*Adapter, *fakeCalls) {
	t.Helper()
	rec := &fakeCalls{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	if opts.TaskPoll == 0 {
		opts.TaskPoll = time.Millisecond
	}
	ad, err := New(Config{Endpoint: srv.URL, CAPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})), APIToken: "root@pam!tok=secret", Timeout: 5 * time.Second}, opts)
	require.NoError(t, err)
	return ad, rec
}

func testQube() *models.Qube {
	return &models.Qube{
		ID:   "q1",
		Name: "remote-1",
		Type: models.QubeTypeDev,
		Spec: models.QubeSpec{VCPU: 2, Memory: 2048, DataDiskGB: 20},
	}
}

func testZone() *models.Zone {
	return &models.Zone{
		Name: "pve",
		Type: models.ZoneTypeProxmox,
		Config: models.ZoneConfig{Proxmox: &models.ProxmoxZoneConfig{
			Node:          "infra-node1",
			DatastoreID:   "ceph-pve",
			NetworkBridge: "vmbr0",
			TemplateVMID:  901,
			TemplateNode:  "infra-node1",
		}},
	}
}

func taskOK(w http.ResponseWriter) {
	writeData(w, map[string]string{"status": vmStatusStopped, "exitstatus": "OK"})
}

func TestEnsureStorage_CreatesHolderAndDisk(t *testing.T) {
	ad, rec := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/cluster/nextid"):
			writeData(w, "105")
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/nodes/infra-node1/qemu"):
			writeData(w, testUPID)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/tasks/"):
			taskOK(w)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/qemu/105/config"):
			writeData(w, map[string]string{"scsi0": "ceph-pve:vm-105-disk-0,discard=on,size=20G"})
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}, Options{})

	inf, err := ad.EnsureStorage(testCheckpointContext(), testQube(), testZone(), provider.Infra{})
	require.NoError(t, err)
	assert.Equal(t, "proxmox", inf.Provider)
	assert.Equal(t, "infra-node1", inf.Node)
	assert.Equal(t, 105, inf.StorageVMID)
	assert.Equal(t, "ceph-pve:vm-105-disk-0", inf.DataVolume)
	create := rec.body("POST", "/api2/json/nodes/infra-node1/qemu")
	assert.Contains(t, create, "scsi0=ceph-pve%3A20%2Cdiscard%3Don%2Ciothread%3D1")
	assert.Contains(t, create, "net0=virtio%2Cbridge%3Dvmbr0")
}

func TestRuntimeMetricsReadsLiveQEMUStatus(t *testing.T) {
	ad, rec := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api2/json/nodes/infra-node1/qemu/106/config" {
			writeData(w, map[string]string{"description": ownerMarker(testQube(), "compute")})
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/api2/json/nodes/infra-node1/qemu/106/status/current" {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		writeData(w, map[string]any{
			"status": vmStatusRunning, "cpu": 0.25, "mem": int64(512), "maxmem": int64(2048),
			"diskread": int64(10), "diskwrite": int64(20), "netin": int64(30), "netout": int64(40),
		})
	}, Options{})

	got, err := ad.RuntimeMetrics(context.Background(), testQube(), provider.Infra{Node: "infra-node1", ComputeVMID: 106})
	require.NoError(t, err)
	assert.Equal(t, "proxmox-qemu-status-current", got.Source)
	assert.False(t, got.CapturedAt.IsZero())
	assert.Equal(t, 0.25, *got.CPUFraction)
	assert.Equal(t, int64(512), *got.MemoryUsedBytes)
	assert.Equal(t, int64(2048), *got.MemoryMaxBytes)
	assert.Equal(t, int64(10), *got.DiskReadBytes)
	assert.Equal(t, int64(20), *got.DiskWriteBytes)
	assert.Equal(t, int64(30), *got.NetworkInBytes)
	assert.Equal(t, int64(40), *got.NetworkOutBytes)
	assert.True(t, rec.has(http.MethodGet, "/api2/json/nodes/infra-node1/qemu/106/status/current"))
	assert.True(t, rec.has(http.MethodGet, "/api2/json/nodes/infra-node1/qemu/106/config"))
}

func TestRuntimeMetricsRejectsUnavailableOrInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		data map[string]any
	}{
		{name: "stopped", data: map[string]any{"status": vmStatusStopped}},
		{name: "missing metrics", data: map[string]any{"status": vmStatusRunning}},
		{name: "negative cpu", data: map[string]any{"status": vmStatusRunning, "cpu": -0.1, "mem": int64(1), "maxmem": int64(2)}},
		{name: "negative memory", data: map[string]any{"status": vmStatusRunning, "cpu": 0.1, "mem": int64(-1), "maxmem": int64(2)}},
		{name: "invalid memory maximum", data: map[string]any{"status": vmStatusRunning, "cpu": 0.1, "mem": int64(1), "maxmem": int64(0)}},
		{name: "negative counter", data: map[string]any{"status": vmStatusRunning, "cpu": 0.1, "mem": int64(1), "maxmem": int64(2), "netin": int64(-1)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ad, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/config") {
					writeData(w, map[string]string{"description": ownerMarker(testQube(), "compute")})
					return
				}
				writeData(w, tt.data)
			}, Options{})
			_, err := ad.RuntimeMetrics(context.Background(), testQube(), provider.Infra{Node: "infra-node1", ComputeVMID: 106})
			require.Error(t, err)
		})
	}
}

// A cpu reading above 1 is not refused: PVE may count QEMU threads beyond the
// vCPUs (inferred, not verified on a cluster), and discarding the whole reading
// would hide the memory and I/O counters of exactly the busiest VM.
func TestRuntimeMetricsPassesCPUAboveOneThroughWithTheOtherCounters(t *testing.T) {
	ad, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/config") {
			writeData(w, map[string]string{"description": ownerMarker(testQube(), "compute")})
			return
		}
		writeData(w, map[string]any{"status": vmStatusRunning, "cpu": 1.04, "mem": int64(3), "maxmem": int64(4), "netin": int64(9)})
	}, Options{})

	got, err := ad.RuntimeMetrics(context.Background(), testQube(), provider.Infra{Node: "infra-node1", ComputeVMID: 106})
	require.NoError(t, err)
	require.NotNil(t, got.CPUFraction)
	assert.InDelta(t, 1.04, *got.CPUFraction, 1e-9)
	assert.Equal(t, int64(3), *got.MemoryUsedBytes)
	assert.Equal(t, int64(9), *got.NetworkInBytes)
}

func TestRuntimeMetricsRefusesDifferentVMOwner(t *testing.T) {
	ad, rec := newTestAdapter(t, func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, map[string]string{"description": "another-qube"})
	}, Options{})
	_, err := ad.RuntimeMetrics(context.Background(), testQube(), provider.Infra{Node: "infra-node1", ComputeVMID: 106})
	require.ErrorContains(t, err, "ownership mismatch")
	require.False(t, rec.has(http.MethodGet, "/api2/json/nodes/infra-node1/qemu/106/status/current"))
}

// A metrics read names one VM on one node, so an incomplete identity must be
// refused before anything is sent to the cluster: guessing a node or VMID would
// read, and report, a VM this qube does not own.
func TestRuntimeMetricsRefusesIncompleteIdentityWithoutCallingTheCluster(t *testing.T) {
	tests := []struct {
		name string
		qube *models.Qube
		in   provider.Infra
	}{
		{name: "nil qube", qube: nil, in: provider.Infra{Node: "infra-node1", ComputeVMID: 106}},
		{name: "qube without id", qube: &models.Qube{Name: "remote-1"}, in: provider.Infra{Node: "infra-node1", ComputeVMID: 106}},
		{name: "no compute vm", qube: testQube(), in: provider.Infra{Node: "infra-node1"}},
		{name: "negative compute vm", qube: testQube(), in: provider.Infra{Node: "infra-node1", ComputeVMID: -1}},
		{name: "no node", qube: testQube(), in: provider.Infra{ComputeVMID: 106}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			ad, _ := newTestAdapter(t, func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				http.Error(w, "unexpected request", http.StatusTeapot)
			}, Options{})
			_, err := ad.RuntimeMetrics(context.Background(), tt.qube, tt.in)
			require.Error(t, err)
			assert.Zero(t, calls.Load(), "an incomplete identity must not reach the cluster")
		})
	}
}

// A cluster that refuses the status read, or a VM locked by a running provider
// task, yields an error rather than a partial or zero measurement.
func TestRuntimeMetricsReportsProviderFailures(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    string
		busy    bool
	}{
		{
			name: "status read fails",
			handler: func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/config") {
					writeData(w, map[string]string{"description": ownerMarker(testQube(), "compute")})
					return
				}
				http.Error(w, "cluster busy", http.StatusInternalServerError)
			},
			want: "read Qube",
		},
		{
			name: "vm locked",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				writeData(w, map[string]string{"description": ownerMarker(testQube(), "compute"), "lock": "backup"})
			},
			want: "locked",
			busy: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ad, _ := newTestAdapter(t, tt.handler, Options{})
			got, err := ad.RuntimeMetrics(context.Background(), testQube(), provider.Infra{Node: "infra-node1", ComputeVMID: 106})
			require.ErrorContains(t, err, tt.want)
			assert.Equal(t, tt.busy, errors.Is(err, provider.ErrInstanceBusy), "only a provider task lock reads as busy")
			assert.Nil(t, got.CPUFraction)
			assert.Nil(t, got.MemoryUsedBytes)
		})
	}
}

// A metrics sweep builds its adapter, reads, and drops it. The connection it
// used must not outlive the sweep as an idle keep-alive (two goroutines per
// connection on the client side, until the provider hangs up).
func TestCloseIdleConnectionsReleasesTheSweepConnections(t *testing.T) {
	ad, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/config") {
			writeData(w, map[string]string{"description": ownerMarker(testQube(), "compute")})
			return
		}
		writeData(w, map[string]any{"status": vmStatusRunning, "cpu": 0.1, "mem": int64(1), "maxmem": int64(2)})
	}, Options{})
	baseline := runtime.NumGoroutine()

	_, err := ad.RuntimeMetrics(context.Background(), testQube(), provider.Infra{Node: "infra-node1", ComputeVMID: 106})
	require.NoError(t, err)
	require.Greater(t, runtime.NumGoroutine(), baseline, "the keep-alive connection is pooled after the read")

	ad.CloseIdleConnections()
	// Polled inline: require.Eventually runs its condition on goroutines of its
	// own, which would be counted.
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	require.LessOrEqual(t, runtime.NumGoroutine(), baseline, "no connection goroutine may outlive CloseIdleConnections")
}

func TestEnsureCompute_ClonesConfiguresStarts(t *testing.T) {
	ad, rec := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/cluster/nextid"):
			writeData(w, "106")
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/qemu/901/clone"):
			writeData(w, testUPID)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/qemu/106/config"):
			writeData(w, nil)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/qemu/106/status/current"):
			writeData(w, map[string]any{"status": vmStatusStopped, "vmid": 106})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/qemu/106/status/start"):
			writeData(w, testUPID)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/tasks/"):
			taskOK(w)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}, Options{Identity: fakeIdentity{vol: "cephfs-snippets:snippets/x.yaml"}})

	in := provider.Infra{Node: "infra-node1", StorageVMID: 105, DataVolume: "ceph-pve:vm-105-disk-0", Protected: true}
	out, err := ad.EnsureCompute(testCheckpointContext(), testQube(), testZone(), in)
	require.NoError(t, err)
	assert.Equal(t, 106, out.ComputeVMID)
	assert.Equal(t, vmStatusRunning, out.ObservedState)
	assert.Equal(t, 105, out.StorageVMID)

	cfg := rec.body("POST", "/api2/json/nodes/infra-node1/qemu/106/config")
	assert.Contains(t, cfg, "scsi1=ceph-pve%3Avm-105-disk-0")
	assert.Contains(t, cfg, "cicustom=user%3Dcephfs-snippets%3Asnippets%2Fx.yaml")
	assert.Contains(t, cfg, "ipconfig0=ip%3Ddhcp")
	assert.Contains(t, cfg, "ciuser=qubes")
	assert.Contains(t, cfg, "cores=2")
	assert.Contains(t, cfg, "memory=2048")
	assert.True(t, rec.has("POST", "/api2/json/nodes/infra-node1/qemu/901/clone"))
}

func TestEnsureCompute_IdentityRequiredWhenResolverConfigured(t *testing.T) {
	ad, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/cluster/nextid"):
			writeData(w, "106")
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/qemu/901/clone"):
			writeData(w, testUPID)
		case strings.Contains(r.URL.Path, "/tasks/"):
			taskOK(w)
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}, Options{Identity: fakeIdentity{}})

	in := provider.Infra{Node: "infra-node1", StorageVMID: 105, DataVolume: "ceph-pve:vm-105-disk-0"}
	_, err := ad.EnsureCompute(testCheckpointContext(), testQube(), testZone(), in)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no agent identity")
}

func TestEnsureCompute_RequiresDataVolume(t *testing.T) {
	ad, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unexpected", http.StatusNotFound)
	}, Options{})

	_, err := ad.EnsureCompute(testCheckpointContext(), testQube(), testZone(), provider.Infra{Node: "infra-node1"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "data volume")
}

func TestStopCompute_StopsAndDeletes(t *testing.T) {
	deleted := false
	ad, rec := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/qemu/106/status/current"):
			if deleted {
				http.NotFound(w, r)
				return
			}
			writeData(w, map[string]any{"status": vmStatusRunning, "vmid": 106})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/qemu/106/config"):
			writeData(w, map[string]string{"description": ownerMarker(testQube(), "compute")})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/qemu/106/status/stop"):
			writeData(w, testUPID)
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/qemu/106"):
			deleted = true
			writeData(w, testUPID)
		case strings.Contains(r.URL.Path, "/tasks/"):
			taskOK(w)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}, Options{})

	in := provider.Infra{Node: "infra-node1", StorageVMID: 105, ComputeVMID: 106}
	require.NoError(t, ad.StopCompute(context.Background(), testQube(), in))
	assert.True(t, rec.has("POST", "/api2/json/nodes/infra-node1/qemu/106/status/stop"))
	assert.True(t, rec.has("DELETE", "/api2/json/nodes/infra-node1/qemu/106"))
}

func TestStopCompute_NoComputeIsNoop(t *testing.T) {
	ad, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unexpected", http.StatusNotFound)
	}, Options{})
	require.NoError(t, ad.StopCompute(context.Background(), testQube(), provider.Infra{Node: "infra-node1"}))
}

func TestDestroyStorage_DeletesHolder(t *testing.T) {
	ad, rec := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/qemu/105/status/current"):
			writeData(w, map[string]any{"status": vmStatusStopped, "vmid": 105})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/qemu/105/config"):
			writeData(w, map[string]string{"description": ownerMarker(testQube(), "storage"), "scsi0": "ceph-pve:vm-105-disk-0"})
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/qemu/105"):
			writeData(w, testUPID)
		case strings.Contains(r.URL.Path, "/tasks/"):
			taskOK(w)
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}, Options{})

	require.NoError(t, ad.DestroyStorage(testCheckpointContext(), testQube(), provider.Infra{Node: "infra-node1", StorageVMID: 105}))
	assert.True(t, rec.has("DELETE", "/api2/json/nodes/infra-node1/qemu/105"))
}

func TestDescribe_RunningWithAgentIP(t *testing.T) {
	ad, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/qemu/106/status/current"):
			writeData(w, map[string]any{"status": vmStatusRunning, "vmid": 106})
		case strings.HasSuffix(r.URL.Path, "/agent/network-get-interfaces"):
			writeData(w, map[string]any{"result": []any{
				map[string]any{"name": "lo", "ip-addresses": []any{
					map[string]any{"ip-address-type": "ipv4", "ip-address": "127.0.0.1"}}},
				map[string]any{"name": "eth0", "ip-addresses": []any{
					map[string]any{"ip-address-type": "ipv4", "ip-address": "10.0.0.9"}}},
			}})
		default:
			http.Error(w, "unexpected "+r.Method+" "+r.URL.Path, http.StatusNotFound)
		}
	}, Options{})

	obs, err := ad.Describe(context.Background(), testQube(),
		provider.Infra{Node: "infra-node1", StorageVMID: 105, ComputeVMID: 106})
	require.NoError(t, err)
	assert.Equal(t, vmStatusRunning, obs.State)
	assert.Equal(t, "10.0.0.9", obs.IPAddress)
	assert.Equal(t, 106, obs.ComputeVMID)
}

func TestDescribe_SuspendedWhenNoCompute(t *testing.T) {
	ad, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unexpected", http.StatusNotFound)
	}, Options{})

	obs, err := ad.Describe(context.Background(), testQube(), provider.Infra{Node: "infra-node1", StorageVMID: 105})
	require.NoError(t, err)
	assert.Equal(t, "suspended", obs.State)
	assert.Equal(t, 105, obs.StorageVMID)
}

func TestUploadSnippet_RejectsUnsafeName(t *testing.T) {
	err := uploadSnippet(context.Background(), "infra-node1", SSHConfig{PrivateKey: "irrelevant"}, "bad;name.yaml", []byte("x"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsafe snippet name")
}

func TestNodeAddress_ResolvesFromClusterStatus(t *testing.T) {
	ad, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		writeData(w, []any{
			map[string]any{"type": "cluster", "name": "infra", "ip": ""},
			map[string]any{"type": "node", "name": "infra-node4", "ip": "10.31.0.203"},
		})
	}, Options{})

	assert.Equal(t, "10.31.0.203", ad.nodeAddress(context.Background(), "infra-node4"))
}

func TestNodeAddress_FallsBackToName(t *testing.T) {
	ad, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		writeData(w, []any{})
	}, Options{})

	assert.Equal(t, "infra-node4", ad.nodeAddress(context.Background(), "infra-node4"))
}

func TestEnsureStorage_LogsToContextSink(t *testing.T) {
	ad, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/cluster/nextid"):
			writeData(w, "105")
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/nodes/infra-node1/qemu"):
			writeData(w, testUPID)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/tasks/"):
			taskOK(w)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/qemu/105/config"):
			writeData(w, map[string]string{"scsi0": "ceph-pve:vm-105-disk-0,discard=on,size=20G"})
		default:
			http.Error(w, "unexpected", http.StatusNotFound)
		}
	}, Options{})

	var sink bytes.Buffer
	ctx := provider.WithLogWriter(testCheckpointContext(), &sink)
	_, err := ad.EnsureStorage(ctx, testQube(), testZone(), provider.Infra{})
	require.NoError(t, err)
	assert.Contains(t, sink.String(), "creating storage VM",
		"provider steps must reach the job log sink, not only the console journal")
}

func TestEnsureStorageRetainsAttemptedIdentityOnTaskFailure(t *testing.T) {
	ad, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/cluster/nextid") {
			writeData(w, "105")
			return
		}
		http.Error(w, "connection lost after acceptance", http.StatusServiceUnavailable)
	}, Options{})
	in, err := ad.EnsureStorage(testCheckpointContext(), testQube(), testZone(), provider.Infra{})
	require.Error(t, err)
	assert.Equal(t, 105, in.StorageVMID, "an ambiguous create must retain the attempted ID")
}

// Unit fixtures provide a recorder; production receives the database checkpoint
// from NativeExecutor. Persistence-failure tests install their own callback.
func testCheckpointContext() context.Context {
	return provider.WithCheckpoint(context.Background(), func(context.Context, provider.Infra) error { return nil })
}
