package proxmox

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/slchris/qubes-air/console/internal/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const stopComputePath = "/api2/json/nodes/infra-node1/qemu/106"

type stopOverride func(http.ResponseWriter, *http.Request, bool) bool

// The fake accepts BOTH shutdown and the old hard-stop endpoint, so failure
// tests cannot pass just because the fake happens to reject an unsafe request.
func newStopComputeAdapter(t *testing.T, initiallyStopped bool, override stopOverride) (*Adapter, *fakeCalls) {
	t.Helper()
	var shutdown, deleted atomic.Bool
	return newTestAdapter(t, func(w http.ResponseWriter, r *http.Request) {
		if override != nil && override(w, r, shutdown.Load()) {
			return
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == stopComputePath+"/status/current":
			if deleted.Load() {
				http.NotFound(w, r)
				return
			}
			status := vmStatusRunning
			if initiallyStopped || shutdown.Load() {
				status = vmStatusStopped
			}
			writeData(w, map[string]any{"status": status, "ha": map[string]int{"managed": 0}})
		case r.Method == http.MethodGet && r.URL.Path == stopComputePath+"/config":
			writeData(w, map[string]string{"description": ownerMarker(testQube(), "compute")})
		case r.Method == http.MethodPost && (r.URL.Path == stopComputePath+"/status/shutdown" || r.URL.Path == stopComputePath+"/status/stop"):
			shutdown.Store(true)
			writeData(w, testUPID)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/tasks/"):
			taskOK(w)
		case r.Method == http.MethodDelete && r.URL.Path == stopComputePath:
			deleted.Store(true)
			writeData(w, testUPID)
		default:
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}, Options{})
}

func computeStopInfra() provider.Infra {
	return provider.Infra{Node: "infra-node1", ComputeVMID: 106, StorageVMID: 105, DataVolume: "ceph:vm-105-disk-0"}
}

func assertComputeRetained(t *testing.T, rec *fakeCalls) {
	t.Helper()
	assert.False(t, rec.has(http.MethodPost, stopComputePath+"/status/stop"), "hard stop is never a fallback")
	assert.False(t, rec.has(http.MethodDelete, stopComputePath), "failure must retain compute")
}

func TestStopCompute_AlreadyStoppedSkipsShutdown(t *testing.T) {
	ad, rec := newStopComputeAdapter(t, true, nil)
	require.NoError(t, ad.StopCompute(context.Background(), testQube(), computeStopInfra()))
	assert.False(t, rec.has(http.MethodPost, stopComputePath+"/status/shutdown"))
	assert.False(t, rec.has(http.MethodPost, stopComputePath+"/status/stop"))
	assert.True(t, rec.has(http.MethodDelete, stopComputePath))
}

func TestStopCompute_RejectsUncertainStatusBeforeMutation(t *testing.T) {
	tests := []struct {
		name   string
		status any
	}{
		{"unknown state", map[string]any{"status": "paused", "ha": map[string]int{"managed": 0}}},
		{"missing state", map[string]any{"ha": map[string]int{"managed": 0}}},
		{"HA running", map[string]any{"status": vmStatusRunning, "ha": map[string]int{"managed": 1}}},
		{"HA stopped", map[string]any{"status": vmStatusStopped, "ha": map[string]int{"managed": 1}}},
		{"missing HA", map[string]any{"status": vmStatusRunning}},
		{"null HA", map[string]any{"status": vmStatusStopped, "ha": nil}},
		{"missing managed", map[string]any{"status": vmStatusRunning, "ha": map[string]int{}}},
		{"null managed", map[string]any{"status": vmStatusRunning, "ha": map[string]any{"managed": nil}}},
		{"invalid managed", map[string]any{"status": vmStatusRunning, "ha": map[string]int{"managed": 2}}},
		{"string managed", map[string]any{"status": vmStatusRunning, "ha": map[string]string{"managed": "0"}}},
		{"boolean managed", map[string]any{"status": vmStatusRunning, "ha": map[string]bool{"managed": false}}},
		{"null status response", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ad, rec := newStopComputeAdapter(t, false, func(w http.ResponseWriter, r *http.Request, _ bool) bool {
				if r.URL.Path == stopComputePath+"/status/current" {
					writeData(w, tt.status)
					return true
				}
				return false
			})
			require.Error(t, ad.StopCompute(context.Background(), testQube(), computeStopInfra()))
			assertComputeRetained(t, rec)
			assert.False(t, rec.has(http.MethodPost, stopComputePath+"/status/shutdown"))
		})
	}
}

func TestStopCompute_ShutdownFailureRetainsCompute(t *testing.T) {
	for _, stage := range []string{"request", "missing task", "task failed", "task read failed", "cancel waiting"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ad, rec := newStopComputeAdapter(t, false, func(w http.ResponseWriter, r *http.Request, _ bool) bool {
				if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, stopComputePath+"/status/") {
					switch stage {
					case "request":
						http.Error(w, "guest refused shutdown", http.StatusInternalServerError)
						return true
					case "missing task":
						writeData(w, "")
						return true
					}
				}
				if strings.Contains(r.URL.Path, "/tasks/") {
					switch stage {
					case "task failed":
						writeData(w, map[string]string{"status": vmStatusStopped, "exitstatus": "shutdown timeout"})
					case "task read failed":
						http.Error(w, "status unavailable", http.StatusServiceUnavailable)
					case "cancel waiting":
						cancel()
						writeData(w, map[string]string{"status": vmStatusRunning})
					default:
						return false
					}
					return true
				}
				return false
			})
			err := ad.StopCompute(ctx, testQube(), computeStopInfra())
			require.Error(t, err)
			if stage == "cancel waiting" {
				require.ErrorIs(t, err, context.Canceled)
			}
			assertComputeRetained(t, rec)
		})
	}
}

func TestStopCompute_RetryAfterFailedShutdown(t *testing.T) {
	var failed atomic.Bool
	ad, rec := newStopComputeAdapter(t, false, func(w http.ResponseWriter, r *http.Request, _ bool) bool {
		if strings.Contains(r.URL.Path, "/tasks/") && failed.CompareAndSwap(false, true) {
			writeData(w, map[string]string{"status": vmStatusStopped, "exitstatus": "shutdown timeout"})
			return true
		}
		return false
	})
	require.Error(t, ad.StopCompute(context.Background(), testQube(), computeStopInfra()))
	assertComputeRetained(t, rec)
	// The guest can finish shutting down after the first provider task fails.
	// A new operation rechecks that state and can finish without a hard stop.
	require.NoError(t, ad.StopCompute(context.Background(), testQube(), computeStopInfra()))
	assert.True(t, rec.has(http.MethodDelete, stopComputePath))
	assert.False(t, rec.has(http.MethodPost, stopComputePath+"/status/stop"))
}

func TestStopCompute_CancelBeforeDeletion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ad, rec := newStopComputeAdapter(t, false, func(w http.ResponseWriter, r *http.Request, shutdown bool) bool {
		if shutdown && r.URL.Path == stopComputePath+"/status/current" {
			cancel()
			writeData(w, map[string]any{"status": vmStatusStopped, "ha": map[string]int{"managed": 0}})
			return true
		}
		return false
	})
	require.ErrorIs(t, ad.StopCompute(ctx, testQube(), computeStopInfra()), context.Canceled)
	assertComputeRetained(t, rec)
}

func TestStopCompute_RechecksBeforeDeletion(t *testing.T) {
	for _, stage := range []string{"running", "unknown", "HA changed", "missing HA", "status failed", "config failed", "owner changed", "lock changed"} {
		t.Run(stage, func(t *testing.T) {
			ad, rec := newStopComputeAdapter(t, false, func(w http.ResponseWriter, r *http.Request, shutdown bool) bool {
				if !shutdown {
					return false
				}
				if r.URL.Path == stopComputePath+"/status/current" {
					switch stage {
					case "running", "unknown":
						writeData(w, map[string]any{"status": stage, "ha": map[string]int{"managed": 0}})
					case "HA changed":
						writeData(w, map[string]any{"status": vmStatusStopped, "ha": map[string]int{"managed": 1}})
					case "missing HA":
						writeData(w, map[string]string{"status": vmStatusStopped})
					case "status failed":
						http.Error(w, "status unavailable", http.StatusServiceUnavailable)
					default:
						return false
					}
					return true
				}
				if r.URL.Path == stopComputePath+"/config" {
					switch stage {
					case "config failed":
						http.Error(w, "config unavailable", http.StatusServiceUnavailable)
					case "owner changed":
						writeData(w, map[string]string{"description": "another owner"})
					case "lock changed":
						writeData(w, map[string]string{"description": ownerMarker(testQube(), "compute"), "lock": "backup"})
					default:
						return false
					}
					return true
				}
				return false
			})
			err := ad.StopCompute(context.Background(), testQube(), computeStopInfra())
			require.Error(t, err)
			if stage == "lock changed" {
				require.ErrorIs(t, err, provider.ErrInstanceBusy)
			}
			assertComputeRetained(t, rec)
		})
	}
}

type shutdownTransport func(*http.Request) (*http.Response, error)

func (f shutdownTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestStopCompute_ShutdownWaitIsBounded(t *testing.T) {
	for _, callerLimit := range []time.Duration{0, 15 * time.Second} {
		t.Run(callerLimit.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ad, err := New(Config{Endpoint: "https://pve.example", APIToken: "test"}, Options{TaskPoll: 10 * time.Second})
				require.NoError(t, err)
				rec := &fakeCalls{}
				ad.client.http.Transport = shutdownTransport(func(r *http.Request) (*http.Response, error) {
					rec.add(r)
					data := `{"data":{"status":"running"}}`
					switch r.URL.Path {
					case stopComputePath + "/status/current":
						data = `{"data":{"status":"running","ha":{"managed":0}}}`
					case stopComputePath + "/config":
						data = `{"data":{"description":"` + ownerMarker(testQube(), "compute") + `"}}`
					case stopComputePath + "/status/shutdown", stopComputePath + "/status/stop":
						data = `{"data":"` + testUPID + `"}`
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(data)), Header: http.Header{}}, nil
				})
				// An outer test guard also terminates the pre-fix unbounded wait.
				ctx, guard := context.WithTimeout(context.Background(), 2*time.Minute)
				defer guard()
				wantElapsed := 90 * time.Second
				if callerLimit > 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, callerLimit)
					defer cancel()
					wantElapsed = callerLimit
				}
				started := time.Now()
				require.ErrorIs(t, ad.StopCompute(ctx, testQube(), computeStopInfra()), context.DeadlineExceeded)
				assert.Equal(t, wantElapsed, time.Since(started))
				assertComputeRetained(t, rec)
			})
		})
	}
}
