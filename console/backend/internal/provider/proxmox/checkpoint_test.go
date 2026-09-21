package proxmox

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/slchris/qubes-air/console/internal/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckpointFailurePreventsCreation(t *testing.T) {
	for _, compute := range []bool{false, true} {
		ad, rec := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/cluster/nextid") {
				writeData(w, "107")
				return
			}
			t.Errorf("request after failed checkpoint: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusServiceUnavailable)
		}, Options{})
		failure := errors.New("database unavailable")
		ctx := provider.WithCheckpoint(context.Background(), func(_ context.Context, in provider.Infra) error {
			assert.True(t, in.StorageVMID == 107 || in.ComputeVMID == 107)
			return failure
		})
		var err error
		if compute {
			_, err = ad.EnsureCompute(ctx, testQube(), testZone(), provider.Infra{DataVolume: "ceph-pve:vm-105-disk-0"})
		} else {
			_, err = ad.EnsureStorage(ctx, testQube(), testZone(), provider.Infra{})
		}
		require.ErrorIs(t, err, failure)
		assert.False(t, rec.has("POST", "/api2/json/nodes/infra-node1/qemu"))
	}
}

func TestRetryAdoptsCheckpointAfterLostCreateResponse(t *testing.T) {
	created := false
	creates := 0
	ad, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/cluster/nextid"):
			writeData(w, "105")
		case r.Method == http.MethodPost:
			created = true
			creates++
			http.Error(w, "lost acknowledgement", http.StatusServiceUnavailable)
		case strings.HasSuffix(r.URL.Path, "/status/current") && created:
			writeData(w, map[string]string{"status": "stopped"})
		case strings.HasSuffix(r.URL.Path, "/config") && created:
			writeData(w, map[string]string{
				"description": ownerMarker(testQube(), "storage"),
				"scsi0":       "ceph-pve:vm-105-disk-0,size=20G",
			})
		default:
			http.NotFound(w, r)
		}
	}, Options{})
	var saved provider.Infra
	ctx := provider.WithCheckpoint(context.Background(), func(_ context.Context, in provider.Infra) error {
		saved = in
		return nil
	})
	_, err := ad.EnsureStorage(ctx, testQube(), testZone(), provider.Infra{})
	require.Error(t, err)
	require.Equal(t, 105, saved.StorageVMID)
	out, err := ad.EnsureStorage(ctx, testQube(), testZone(), saved)
	require.NoError(t, err)
	assert.Equal(t, "ceph-pve:vm-105-disk-0", out.DataVolume)
	assert.Equal(t, 1, creates)
}

func TestOwnershipMismatchRefusesAdoptionAndDeletion(t *testing.T) {
	ad, _ := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("mutation of foreign resource: %s", r.Method)
		}
		if strings.HasSuffix(r.URL.Path, "/config") {
			writeData(w, map[string]string{"description": "another owner"})
			return
		}
		writeData(w, map[string]string{"status": "stopped"})
	}, Options{})
	in := provider.Infra{Node: "infra-node1", StorageVMID: 105, ComputeVMID: 106, DataVolume: "ceph-pve:vm-105-disk-0"}
	_, err := ad.EnsureStorage(testCheckpointContext(), testQube(), testZone(), in)
	require.ErrorContains(t, err, "ownership mismatch")
	_, err = ad.EnsureCompute(testCheckpointContext(), testQube(), testZone(), in)
	require.ErrorContains(t, err, "ownership mismatch")
	require.ErrorContains(t, ad.StopCompute(context.Background(), testQube(), in), "ownership mismatch")
	require.ErrorContains(t, ad.DestroyStorage(context.Background(), testQube(), in), "ownership mismatch")
}

func TestRetryFinishesExistingComputeWithoutCloning(t *testing.T) {
	ad, rec := newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/config"):
			writeData(w, map[string]string{"description": ownerMarker(testQube(), "compute"), "ipconfig0": "ip=192.0.2.10/24,gw=192.0.2.1"})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/status/current"):
			writeData(w, map[string]string{"status": "running"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/config"):
			writeData(w, nil)
		default:
			t.Errorf("unexpected retry request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected", http.StatusServiceUnavailable)
		}
	}, Options{Identity: fakeIdentity{vol: "local:snippets/agent.yaml"}})
	in := provider.Infra{Node: "infra-node1", StorageVMID: 105, ComputeVMID: 106, DataVolume: "ceph-pve:vm-105-disk-0"}
	out, err := ad.EnsureCompute(testCheckpointContext(), testQube(), testZone(), in)
	require.NoError(t, err)
	assert.Equal(t, 106, out.ComputeVMID)
	assert.Equal(t, "local:snippets/agent.yaml", out.IdentityVol)
	assert.Contains(t, rec.body("POST", "/api2/json/nodes/infra-node1/qemu/106/config"), "ipconfig0=ip%3D192.0.2.10")
}
