package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/provider"
	"github.com/slchris/qubes-air/console/internal/repository"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type runtimeQubeListStub struct {
	qubes []*models.Qube
	err   error
}

func (s runtimeQubeListStub) List(_ context.Context, opts repository.QubeListOptions) ([]*models.Qube, error) {
	if s.err != nil || opts.Offset >= len(s.qubes) {
		return nil, s.err
	}
	end := min(opts.Offset+opts.Limit, len(s.qubes))
	return s.qubes[opts.Offset:end], nil
}

type runtimeZoneStub struct {
	zone *models.Zone
	err  error
	// byID, when set, answers per zone ID instead of zone.
	byID map[string]*models.Zone
}

func (s runtimeZoneStub) GetByID(_ context.Context, id string) (*models.Zone, error) {
	if s.byID != nil {
		return s.byID[id], nil
	}
	return s.zone, s.err
}

type runtimeInfraStub struct {
	infra *provider.Infra
	err   error
}

func (s runtimeInfraStub) Get(context.Context, string) (*provider.Infra, error) {
	return s.infra, s.err
}

// lifecycleAdapterStub is an Adapter WITHOUT the runtime metrics capability.
type lifecycleAdapterStub struct{}

func (lifecycleAdapterStub) VerifyDestroyed(context.Context, *models.Qube, provider.Infra) error {
	return nil
}
func (lifecycleAdapterStub) EnsureStorage(context.Context, *models.Qube, *models.Zone, provider.Infra) (provider.Infra, error) {
	return provider.Infra{}, nil
}
func (lifecycleAdapterStub) EnsureCompute(context.Context, *models.Qube, *models.Zone, provider.Infra) (provider.Infra, error) {
	return provider.Infra{}, nil
}
func (lifecycleAdapterStub) StopCompute(context.Context, *models.Qube, provider.Infra) error {
	return nil
}
func (lifecycleAdapterStub) DestroyStorage(context.Context, *models.Qube, provider.Infra) error {
	return nil
}
func (lifecycleAdapterStub) Describe(context.Context, *models.Qube, provider.Infra) (provider.Observed, error) {
	return provider.Observed{}, nil
}

// runtimeAdapterStub adds the capability; read decides each answer.
type runtimeAdapterStub struct {
	lifecycleAdapterStub
	read func(context.Context, *models.Qube) (provider.RuntimeMetrics, error)
}

func (s runtimeAdapterStub) RuntimeMetrics(ctx context.Context, q *models.Qube, _ provider.Infra) (provider.RuntimeMetrics, error) {
	return s.read(ctx, q)
}

// logRecorder captures the collector's server-side log lines.
type logRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *logRecorder) logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

func (r *logRecorder) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

func registryWith(t *testing.T, adapter provider.Adapter) *provider.Registry {
	t.Helper()
	registry := provider.NewRegistry()
	require.NoError(t, registry.Register(models.ZoneTypeProxmox, func(context.Context, *models.Zone) (provider.Adapter, error) {
		return adapter, nil
	}))
	return registry
}

func fixedMetrics(context.Context, *models.Qube) (provider.RuntimeMetrics, error) {
	cpu := 0.5
	return provider.RuntimeMetrics{Source: "fixture", CPUFraction: &cpu}, nil
}

func runningMetricsQube(id string) *models.Qube {
	return &models.Qube{ID: id, Name: "vm-" + id, ZoneID: "z1", Status: models.QubeStatusRunning}
}

func proxmoxZone() runtimeZoneStub {
	return runtimeZoneStub{zone: &models.Zone{ID: "z1", Name: "lab", Type: models.ZoneTypeProxmox}}
}

func computeInfra() runtimeInfraStub {
	return runtimeInfraStub{infra: &provider.Infra{Node: "pve1", ComputeVMID: 100}}
}

// newTestCollector wires a collector over stubs with a private log recorder.
func newTestCollector(qubes []*models.Qube, zones runtimeZoneStub, infras runtimeInfraStub, registry *provider.Registry) (*RuntimeMetricsCollector, *logRecorder) {
	collector := NewRuntimeMetricsCollector(runtimeQubeListStub{qubes: qubes}, zones, infras, registry)
	logs := &logRecorder{}
	collector.logf = logs.logf
	return collector, logs
}

func TestRuntimeMetricsCollectorReportsMeasurementsAndUnavailability(t *testing.T) {
	capturedAt := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	cpu := 0.5
	used, total := int64(1024), int64(4096)
	var reads atomic.Int32
	collector, logs := newTestCollector(
		[]*models.Qube{runningMetricsQube("q-running"), {ID: "q-suspended", Name: "suspended", ZoneID: "z1", Status: models.QubeStatusSuspended}},
		proxmoxZone(), computeInfra(),
		registryWith(t, runtimeAdapterStub{read: func(context.Context, *models.Qube) (provider.RuntimeMetrics, error) {
			reads.Add(1)
			return provider.RuntimeMetrics{
				CapturedAt: capturedAt, Source: "fixture", CPUFraction: &cpu,
				MemoryUsedBytes: &used, MemoryMaxBytes: &total,
			}, nil
		}}),
	)

	items, err := collector.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, "vm-q-running", items[0].QubeName)
	assert.Empty(t, items[0].Reason)
	require.NotNil(t, items[0].Metrics)
	assert.Equal(t, "fixture", items[0].Metrics.Source)
	assert.Equal(t, capturedAt, items[0].Metrics.CapturedAt)
	assert.Equal(t, RuntimeMetricsNotRunning, items[1].Reason)
	assert.Nil(t, items[1].Metrics)
	assert.Equal(t, int32(1), reads.Load(), "a qube that is not running is never read from the provider")
	assert.Empty(t, logs.text())
}

func TestRuntimeMetricsCollectorReportsAnEmptyFleetAsAnEmptyList(t *testing.T) {
	collector, _ := newTestCollector(nil, proxmoxZone(), computeInfra(), provider.NewRegistry())

	items, err := collector.Collect(context.Background())
	require.NoError(t, err)
	encoded, err := json.Marshal(items)
	require.NoError(t, err)
	assert.JSONEq(t, `[]`, string(encoded), "an empty fleet is [] on the wire, not null")
}

// Each failure is reported as a fixed reason code and never as a value; the
// cause is logged server-side and does not appear in the returned item.
func TestRuntimeMetricsCollectorReportsEachFailureByReasonOnly(t *testing.T) {
	const secretCause = "dial tcp 10.9.8.7:8006: connection refused"
	tests := []struct {
		name     string
		zones    runtimeZoneStub
		infras   runtimeInfraStub
		registry func(*testing.T) *provider.Registry
		reason   string
		logged   string
	}{
		{name: "zone lookup fails", zones: runtimeZoneStub{err: errors.New(secretCause)}, infras: computeInfra(),
			registry: func(t *testing.T) *provider.Registry { return registryWith(t, lifecycleAdapterStub{}) },
			reason:   RuntimeMetricsZoneUnavailable, logged: secretCause},
		{name: "zone missing", zones: runtimeZoneStub{}, infras: computeInfra(),
			registry: func(t *testing.T) *provider.Registry { return registryWith(t, lifecycleAdapterStub{}) },
			reason:   RuntimeMetricsZoneUnavailable, logged: "not found"},
		{name: "infra lookup fails", zones: proxmoxZone(), infras: runtimeInfraStub{err: errors.New(secretCause)},
			registry: func(t *testing.T) *provider.Registry { return registryWith(t, lifecycleAdapterStub{}) },
			reason:   RuntimeMetricsInfraUnavailable, logged: secretCause},
		{name: "no infra row", zones: proxmoxZone(), infras: runtimeInfraStub{},
			registry: func(t *testing.T) *provider.Registry { return registryWith(t, lifecycleAdapterStub{}) },
			reason:   RuntimeMetricsInfraMissing, logged: "no compute identity"},
		{name: "infra without node", zones: proxmoxZone(), infras: runtimeInfraStub{infra: &provider.Infra{ComputeVMID: 100}},
			registry: func(t *testing.T) *provider.Registry { return registryWith(t, lifecycleAdapterStub{}) },
			reason:   RuntimeMetricsInfraMissing, logged: "no compute identity"},
		{name: "no adapter for the zone type", zones: proxmoxZone(), infras: computeInfra(),
			registry: func(*testing.T) *provider.Registry { return provider.NewRegistry() },
			reason:   RuntimeMetricsProviderUnavailable, logged: "no provider adapter"},
		{name: "no registry", zones: proxmoxZone(), infras: computeInfra(),
			registry: func(*testing.T) *provider.Registry { return nil },
			reason:   RuntimeMetricsProviderUnavailable, logged: "no provider registry"},
		{name: "provider read fails", zones: proxmoxZone(), infras: computeInfra(),
			registry: func(t *testing.T) *provider.Registry {
				return registryWith(t, runtimeAdapterStub{read: func(context.Context, *models.Qube) (provider.RuntimeMetrics, error) {
					return provider.RuntimeMetrics{}, errors.New(secretCause)
				}})
			},
			reason: RuntimeMetricsUnavailable, logged: secretCause},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collector, logs := newTestCollector([]*models.Qube{runningMetricsQube("q1")}, tt.zones, tt.infras, tt.registry(t))

			items, err := collector.Collect(context.Background())
			require.NoError(t, err, "one qube's failure does not fail the collection")
			require.Len(t, items, 1)
			assert.Equal(t, tt.reason, items[0].Reason)
			assert.Nil(t, items[0].Metrics, "no value is fabricated for an unavailable measurement")
			encoded, err := json.Marshal(items)
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), "10.9.8.7", "the cause stays out of the response")
			assert.Contains(t, logs.text(), tt.logged)
			assert.Contains(t, logs.text(), "q1")
		})
	}
}

func TestRuntimeMetricsCollectorDoesNotLogAnExpectedlyUnsupportedProvider(t *testing.T) {
	collector, logs := newTestCollector([]*models.Qube{runningMetricsQube("q1")}, proxmoxZone(), computeInfra(),
		registryWith(t, lifecycleAdapterStub{}))

	items, err := collector.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, RuntimeMetricsUnsupported, items[0].Reason)
	assert.Nil(t, items[0].Metrics)
	assert.Empty(t, logs.text(), "a provider without the capability is a known state, not a fault to log every minute")
}

func TestRuntimeMetricsCollectorReturnsListFailure(t *testing.T) {
	collector := NewRuntimeMetricsCollector(runtimeQubeListStub{err: errors.New("database unavailable")}, nil, nil, nil)
	items, err := collector.Collect(context.Background())
	require.ErrorContains(t, err, "database unavailable")
	require.Nil(t, items)
}

func TestRuntimeMetricsCollectorHonorsRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	collector, _ := newTestCollector([]*models.Qube{runningMetricsQube("q1")}, runtimeZoneStub{}, runtimeInfraStub{}, provider.NewRegistry())
	items, err := collector.Collect(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, items)
}

func TestRuntimeMetricsCollectorReadsEveryPage(t *testing.T) {
	qubes := make([]*models.Qube, runtimeMetricsPageSize*2+1)
	for i := range qubes {
		qubes[i] = &models.Qube{ID: fmt.Sprintf("q%d", i), ZoneID: "z1", Status: models.QubeStatusStopped}
	}
	collector, _ := newTestCollector(qubes, proxmoxZone(), computeInfra(), provider.NewRegistry())

	items, err := collector.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, items, len(qubes))
	assert.Equal(t, "q400", items[400].QubeID)
}

// One unresponsive VM must not cost the others their measurement: the
// per-qube timeout ends its read and the rest are still observed. The sweep
// deadline is set far away so only the per-qube timeout can end the read.
func TestRuntimeMetricsCollectorTimesOutOneSlowQube(t *testing.T) {
	collector, logs := newTestCollector([]*models.Qube{runningMetricsQube("slow"), runningMetricsQube("fast")}, proxmoxZone(), computeInfra(),
		registryWith(t, runtimeAdapterStub{read: func(ctx context.Context, q *models.Qube) (provider.RuntimeMetrics, error) {
			if q.ID == "slow" {
				<-ctx.Done()
				return provider.RuntimeMetrics{}, ctx.Err()
			}
			return fixedMetrics(ctx, q)
		}}))
	collector.sweepDeadline = time.Minute
	collector.perQubeTimeout = 200 * time.Millisecond

	started := time.Now()
	items, err := collector.Collect(context.Background())
	elapsed := time.Since(started)
	require.NoError(t, err)
	assert.Less(t, elapsed, 2*time.Second, "the per-qube timeout, not the sweep deadline, must end the slow read")
	require.Len(t, items, 2)
	assert.Equal(t, RuntimeMetricsTimeout, items[0].Reason)
	assert.Nil(t, items[0].Metrics)
	require.NotNil(t, items[1].Metrics)
	assert.Contains(t, logs.text(), "provider_metrics_timeout")
}

// The sweep deadline holds even against an adapter that ignores its context:
// the answer arrives on time, with the stuck qube reported as not observed.
func TestRuntimeMetricsCollectorKeepsItsDeadlineAgainstAStuckProvider(t *testing.T) {
	release := make(chan struct{})
	finished := make(chan struct{})
	collector, logs := newTestCollector([]*models.Qube{runningMetricsQube("stuck"), runningMetricsQube("fine")}, proxmoxZone(), computeInfra(),
		registryWith(t, runtimeAdapterStub{read: func(ctx context.Context, q *models.Qube) (provider.RuntimeMetrics, error) {
			if q.ID == "stuck" {
				defer close(finished)
				<-release // deliberately ignores ctx
				return provider.RuntimeMetrics{}, errors.New("late")
			}
			return fixedMetrics(ctx, q)
		}}))
	collector.sweepDeadline = 500 * time.Millisecond
	collector.perQubeTimeout = time.Minute

	started := time.Now()
	items, err := collector.Collect(context.Background())
	elapsed := time.Since(started)
	close(release)
	<-finished

	require.NoError(t, err)
	assert.Less(t, elapsed, 5*time.Second, "Collect must not wait for a provider call that ignores its context")
	require.Len(t, items, 2)
	assert.Equal(t, RuntimeMetricsTimeout, items[0].Reason)
	assert.Nil(t, items[0].Metrics)
	require.NotNil(t, items[1].Metrics, "a qube observed before the deadline keeps its measurement")
	assert.Contains(t, logs.text(), "1 of 2 running qubes were not observed")
}

func TestRuntimeMetricsCollectorBoundsConcurrentProviderReads(t *testing.T) {
	qubes := make([]*models.Qube, 3*runtimeMetricsWorkers)
	for i := range qubes {
		qubes[i] = runningMetricsQube(fmt.Sprintf("q%d", i))
	}
	var inFlight, peak atomic.Int32
	collector, _ := newTestCollector(qubes, proxmoxZone(), computeInfra(),
		registryWith(t, runtimeAdapterStub{read: func(ctx context.Context, q *models.Qube) (provider.RuntimeMetrics, error) {
			current := inFlight.Add(1)
			defer inFlight.Add(-1)
			for {
				seen := peak.Load()
				if current <= seen || peak.CompareAndSwap(seen, current) {
					break
				}
			}
			time.Sleep(5 * time.Millisecond)
			return fixedMetrics(ctx, q)
		}}))

	items, err := collector.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, items, len(qubes))
	for _, item := range items {
		assert.NotNil(t, item.Metrics, item.QubeID)
	}
	assert.LessOrEqual(t, peak.Load(), int32(runtimeMetricsWorkers))
}

// Two requests at once (two open browser tabs) each get a complete answer;
// under -race this also shows the sweeps share no mutable state.
func TestRuntimeMetricsCollectorServesConcurrentSweeps(t *testing.T) {
	qubes := []*models.Qube{runningMetricsQube("q1"), runningMetricsQube("q2"), {ID: "q3", ZoneID: "z1", Status: models.QubeStatusStopped}}
	collector, _ := newTestCollector(qubes, proxmoxZone(), computeInfra(), registryWith(t, runtimeAdapterStub{read: fixedMetrics}))

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			items, err := collector.Collect(context.Background())
			assert.NoError(t, err)
			if assert.Len(t, items, 3) {
				assert.NotNil(t, items[0].Metrics)
				assert.NotNil(t, items[1].Metrics)
				assert.Equal(t, RuntimeMetricsNotRunning, items[2].Reason)
			}
		}()
	}
	wg.Wait()
}

// A VM held by a provider task is reported as provider_busy. It is logged,
// so a lock that never clears is visible server-side, but at most once per
// qube per interval rather than on every poll.
func TestRuntimeMetricsCollectorLogsABusyInstanceAtMostOncePerInterval(t *testing.T) {
	busy := map[string]bool{"q1": true}
	var busyMu sync.Mutex
	collector, logs := newTestCollector([]*models.Qube{runningMetricsQube("q1"), runningMetricsQube("q2")}, proxmoxZone(), computeInfra(),
		registryWith(t, runtimeAdapterStub{read: func(ctx context.Context, q *models.Qube) (provider.RuntimeMetrics, error) {
			busyMu.Lock()
			defer busyMu.Unlock()
			if busy[q.ID] {
				return provider.RuntimeMetrics{}, fmt.Errorf("proxmox: VM 100 remains locked (%w)", provider.ErrInstanceBusy)
			}
			return fixedMetrics(ctx, q)
		}}))
	collector.reuseWindow = 0
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	collector.now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return clock
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		defer clockMu.Unlock()
		clock = clock.Add(d)
	}
	busyLines := func() int { return strings.Count(logs.text(), "held by a provider task") }

	items, err := collector.Collect(context.Background())
	require.NoError(t, err)
	assert.Equal(t, RuntimeMetricsBusy, items[0].Reason)
	assert.Nil(t, items[0].Metrics)
	require.Equal(t, 1, busyLines())
	assert.Contains(t, logs.text(), "q1")
	assert.Contains(t, logs.text(), "remains locked")

	advance(runtimeMetricsBusyLogInterval - time.Second)
	_, err = collector.Collect(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, busyLines(), "a lock seen again within the interval is not logged again")

	busyMu.Lock()
	busy["q2"] = true
	busyMu.Unlock()
	_, err = collector.Collect(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, busyLines(), "another qube's lock gets its own line")

	advance(time.Second)
	_, err = collector.Collect(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, busyLines(), "q1's lock is logged again once the interval has passed")
	assert.NotContains(t, logs.text(), "monitoring: runtime metrics for qube",
		"busy is never logged through the unlimited per-failure line")
}

// Each zone's adapter is built once per sweep and shared by its qubes: one
// credential resolution and at most one provider login per zone, not per qube.
// The next sweep builds afresh, so nothing outlives the request.
func TestRuntimeMetricsCollectorBuildsOneAdapterPerZonePerSweep(t *testing.T) {
	zones := map[string]*models.Zone{
		"z1": {ID: "z1", Name: "one", Type: models.ZoneTypeProxmox},
		"z2": {ID: "z2", Name: "two", Type: models.ZoneTypeProxmox},
	}
	qubes := make([]*models.Qube, 0, 12)
	for i := range 12 {
		qube := runningMetricsQube(fmt.Sprintf("q%d", i))
		if i%3 == 0 {
			qube.ZoneID = "z2"
		}
		qubes = append(qubes, qube)
	}
	var builds sync.Map
	var total atomic.Int32
	registry := provider.NewRegistry()
	require.NoError(t, registry.Register(models.ZoneTypeProxmox, func(_ context.Context, zone *models.Zone) (provider.Adapter, error) {
		total.Add(1)
		counter, _ := builds.LoadOrStore(zone.ID, new(atomic.Int32))
		counter.(*atomic.Int32).Add(1)
		return runtimeAdapterStub{read: fixedMetrics}, nil
	}))
	collector, _ := newTestCollector(qubes, runtimeZoneStub{byID: zones}, computeInfra(), registry)
	collector.reuseWindow = 0 // the second call below must sweep again

	items, err := collector.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, items, len(qubes))
	for _, item := range items {
		assert.NotNil(t, item.Metrics, item.QubeID)
	}
	assert.Equal(t, int32(2), total.Load(), "one adapter per zone, not one per qube")
	for _, zoneID := range []string{"z1", "z2"} {
		counter, ok := builds.Load(zoneID)
		require.True(t, ok, zoneID)
		assert.Equal(t, int32(1), counter.(*atomic.Int32).Load(), zoneID)
	}

	_, err = collector.Collect(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int32(4), total.Load(), "adapters are per sweep, not cached across requests")
}

// httpAdapterStub reads through a real pooled HTTP client, as the Proxmox
// adapter does, so the test can see connections left behind by a sweep.
type httpAdapterStub struct {
	lifecycleAdapterStub
	client *http.Client
	url    string
}

func (s httpAdapterStub) RuntimeMetrics(ctx context.Context, q *models.Qube, _ provider.Infra) (provider.RuntimeMetrics, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return provider.RuntimeMetrics{}, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return provider.RuntimeMetrics{}, err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return fixedMetrics(ctx, q)
}

func (s httpAdapterStub) CloseIdleConnections() { s.client.CloseIdleConnections() }

// The adapters a sweep builds must not leave idle keep-alive connections, and
// their goroutines, behind once the response is ready.
func TestRuntimeMetricsCollectorReleasesProviderConnectionsAfterTheSweep(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)
	registry := provider.NewRegistry()
	require.NoError(t, registry.Register(models.ZoneTypeProxmox, func(context.Context, *models.Zone) (provider.Adapter, error) {
		return httpAdapterStub{client: &http.Client{Transport: &http.Transport{}}, url: srv.URL}, nil
	}))
	collector, _ := newTestCollector(
		[]*models.Qube{runningMetricsQube("q1"), runningMetricsQube("q2"), runningMetricsQube("q3")},
		proxmoxZone(), computeInfra(), registry)
	baseline := runtime.NumGoroutine()

	items, err := collector.Collect(context.Background())
	require.NoError(t, err)
	for _, item := range items {
		require.NotNil(t, item.Metrics, item.QubeID)
	}

	// Polled inline: require.Eventually's own goroutines would be counted.
	deadline := time.Now().Add(5 * time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	assert.LessOrEqual(t, runtime.NumGoroutine(), baseline, "no provider connection goroutine may outlive the sweep")
}

// A qube created between two page reads pushes the newest-first order down by
// one, so the row at the page boundary comes back on the next page too. It
// must be reported once.
func TestRuntimeMetricsCollectorReportsAQubeOnceWhenACreateShiftsThePages(t *testing.T) {
	qubes := make([]*models.Qube, runtimeMetricsPageSize+50)
	for i := range qubes {
		qubes[i] = &models.Qube{ID: fmt.Sprintf("q%03d", i), ZoneID: "z1", Status: models.QubeStatusStopped}
	}
	lister := &shiftingLister{qubes: qubes}
	collector := NewRuntimeMetricsCollector(lister, proxmoxZone(), computeInfra(), provider.NewRegistry())

	items, err := collector.Collect(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, lister.calls)
	seen := make(map[string]bool)
	for _, item := range items {
		assert.False(t, seen[item.QubeID], "qube %s reported twice", item.QubeID)
		seen[item.QubeID] = true
	}
	assert.Len(t, items, len(qubes))
}

// shiftingLister serves pages of qubes and inserts a new newest qube after the
// first page has been read, as a concurrent create would.
type shiftingLister struct {
	qubes []*models.Qube
	calls int
}

func (l *shiftingLister) List(_ context.Context, opts repository.QubeListOptions) ([]*models.Qube, error) {
	l.calls++
	current := l.qubes
	if l.calls > 1 {
		current = append([]*models.Qube{{ID: "created-meanwhile", ZoneID: "z1"}}, l.qubes...)
	}
	if opts.Offset >= len(current) {
		return nil, nil
	}
	return current[opts.Offset:min(opts.Offset+opts.Limit, len(current))], nil
}

// countingLister counts sweeps (each starts with a first-page List) and can
// fail the next ones.
type countingLister struct {
	qubes    []*models.Qube
	pages    atomic.Int32
	failNext atomic.Int32
}

func (l *countingLister) List(_ context.Context, opts repository.QubeListOptions) ([]*models.Qube, error) {
	if opts.Offset == 0 {
		l.pages.Add(1)
		if l.failNext.Load() > 0 {
			l.failNext.Add(-1)
			return nil, errors.New("database unavailable")
		}
	}
	if opts.Offset >= len(l.qubes) {
		return nil, nil
	}
	return l.qubes[opts.Offset:min(opts.Offset+opts.Limit, len(l.qubes))], nil
}

// newSharedCollector wires a collector over a counting lister and a frozen
// clock that only advance moves.
func newSharedCollector(t *testing.T, read func(context.Context, *models.Qube) (provider.RuntimeMetrics, error)) (*RuntimeMetricsCollector, *countingLister, func(time.Duration)) {
	t.Helper()
	lister := &countingLister{qubes: []*models.Qube{runningMetricsQube("q1")}}
	collector := NewRuntimeMetricsCollector(lister, proxmoxZone(), computeInfra(), registryWith(t, runtimeAdapterStub{read: read}))
	collector.logf = (&logRecorder{}).logf
	clock := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	collector.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return clock
	}
	advance := func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		clock = clock.Add(d)
	}
	return collector, lister, advance
}

// A read-only token polling at the rate limit must not start a sweep per
// request: callers arriving while a sweep runs wait for that one.
func TestRuntimeMetricsCollectorSharesOneSweepBetweenConcurrentCallers(t *testing.T) {
	release := make(chan struct{})
	var reads atomic.Int32
	collector, lister, _ := newSharedCollector(t, func(ctx context.Context, q *models.Qube) (provider.RuntimeMetrics, error) {
		reads.Add(1)
		<-release
		return fixedMetrics(ctx, q)
	})

	const callers = 20
	var wg sync.WaitGroup
	results := make([][]QubeRuntimeMetrics, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			items, err := collector.Collect(context.Background())
			assert.NoError(t, err)
			results[i] = items
		}()
	}
	time.Sleep(100 * time.Millisecond) // let the callers arrive while the sweep is held
	close(release)
	wg.Wait()

	assert.Equal(t, int32(1), lister.pages.Load(), "one sweep for all concurrent callers")
	assert.Equal(t, int32(1), reads.Load(), "one provider read per qube, not per caller")
	for i, items := range results {
		if assert.Len(t, items, 1, "caller %d", i) {
			assert.NotNil(t, items[0].Metrics, "caller %d", i)
		}
	}
}

func TestRuntimeMetricsCollectorReusesAResultWithinTheWindowOnly(t *testing.T) {
	collector, lister, advance := newSharedCollector(t, fixedMetrics)

	first, err := collector.Collect(context.Background())
	require.NoError(t, err)
	require.Equal(t, int32(1), lister.pages.Load())

	advance(DefaultRuntimeMetricsReuseWindow - time.Millisecond)
	again, err := collector.Collect(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int32(1), lister.pages.Load(), "within the window the last result is served")
	assert.Equal(t, first, again)

	advance(time.Millisecond)
	_, err = collector.Collect(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int32(2), lister.pages.Load(), "once the window has passed a new sweep runs")
}

func TestRuntimeMetricsCollectorDoesNotReuseAFailedSweep(t *testing.T) {
	collector, lister, _ := newSharedCollector(t, fixedMetrics)
	lister.failNext.Store(1)

	_, err := collector.Collect(context.Background())
	require.ErrorContains(t, err, "database unavailable")

	items, err := collector.Collect(context.Background())
	require.NoError(t, err, "a failure is not served from the reuse window")
	require.Len(t, items, 1)
	assert.NotNil(t, items[0].Metrics)
	assert.Equal(t, int32(2), lister.pages.Load())
}

// A caller that gives up does not cancel the sweep others are waiting on, and
// the sweep's result is still kept for the next caller.
func TestRuntimeMetricsCollectorKeepsASharedSweepRunningWhenOneCallerLeaves(t *testing.T) {
	release := make(chan struct{})
	collector, lister, _ := newSharedCollector(t, func(ctx context.Context, q *models.Qube) (provider.RuntimeMetrics, error) {
		<-release
		if err := ctx.Err(); err != nil {
			return provider.RuntimeMetrics{}, err
		}
		return fixedMetrics(ctx, q)
	})

	ctx, cancel := context.WithCancel(context.Background())
	leaving := make(chan error, 1)
	go func() {
		_, err := collector.Collect(ctx)
		leaving <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	require.ErrorIs(t, <-leaving, context.Canceled)
	close(release)

	items, err := collector.Collect(context.Background())
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.NotNil(t, items[0].Metrics, "the abandoned caller's context did not cancel the provider read")
	assert.Equal(t, int32(1), lister.pages.Load(), "the next caller joined or reused that sweep")
}
