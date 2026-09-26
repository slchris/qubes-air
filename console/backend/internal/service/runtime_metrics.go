package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/slchris/qubes-air/console/internal/models"
	"github.com/slchris/qubes-air/console/internal/provider"
	"github.com/slchris/qubes-air/console/internal/repository"
)

const (
	// runtimeMetricsWorkers bounds concurrent provider reads in one sweep.
	runtimeMetricsWorkers  = 8
	runtimeMetricsPageSize = 200
	// DefaultRuntimeMetricsSweepDeadline bounds one whole collection, listing
	// included. It stays well inside the server's 15s WriteTimeout so a slow
	// provider yields a partial answer rather than a connection cut mid-reply.
	DefaultRuntimeMetricsSweepDeadline = 10 * time.Second
	// DefaultRuntimeMetricsPerQubeTimeout bounds the reads for one qube, so a
	// single unresponsive VM or cluster cannot use up the whole sweep.
	DefaultRuntimeMetricsPerQubeTimeout = 5 * time.Second
	// DefaultRuntimeMetricsReuseWindow is how long a completed sweep's result
	// is served to later callers before a new sweep runs.
	DefaultRuntimeMetricsReuseWindow = 5 * time.Second
)

// Reasons a qube has no runtime measurement. They are part of the API
// contract: the UI maps each to an explanation. The underlying error is only
// logged, never returned, because it can name internal endpoints.
const (
	RuntimeMetricsNotRunning          = "qube_not_running"
	RuntimeMetricsZoneUnavailable     = "zone_unavailable"
	RuntimeMetricsInfraUnavailable    = "infrastructure_unavailable"
	RuntimeMetricsInfraMissing        = "infrastructure_missing"
	RuntimeMetricsProviderUnavailable = "provider_unavailable"
	RuntimeMetricsUnsupported         = "provider_metrics_unsupported"
	RuntimeMetricsUnavailable         = "provider_metrics_unavailable"
	RuntimeMetricsTimeout             = "provider_metrics_timeout"
	// RuntimeMetricsBusy: a provider task (a backup, a migration) holds the
	// instance. Expected and usually transient, so it is logged at most once
	// per qube per runtimeMetricsBusyLogInterval: often enough that a lock
	// that never clears is visible server-side, rarely enough not to flood.
	RuntimeMetricsBusy = "provider_busy"
)

// runtimeMetricsBusyLogInterval rate-limits the provider_busy log line per qube.
const runtimeMetricsBusyLogInterval = 10 * time.Minute

type runtimeMetricQubeLister interface {
	List(context.Context, repository.QubeListOptions) ([]*models.Qube, error)
}

type runtimeMetricZoneGetter interface {
	GetByID(context.Context, string) (*models.Zone, error)
}

type runtimeMetricInfraGetter interface {
	Get(context.Context, string) (*provider.Infra, error)
}

// idleConnectionCloser is implemented by adapters that pool connections
// (proxmox.Adapter).
type idleConnectionCloser interface {
	CloseIdleConnections()
}

// QubeRuntimeMetrics is one provider observation or an explicit reason why an
// observation could not be made. An unavailable item has no metric values.
type QubeRuntimeMetrics struct {
	QubeID   string                   `json:"qube_id"`
	QubeName string                   `json:"qube_name"`
	ZoneID   string                   `json:"zone_id"`
	State    string                   `json:"state"`
	Reason   string                   `json:"reason,omitempty"`
	Metrics  *provider.RuntimeMetrics `json:"metrics,omitempty"`
}

// RuntimeMetricsCollector reads each running qube from its configured
// provider. One collector serves the whole process, which is what lets it
// share sweeps between callers.
type RuntimeMetricsCollector struct {
	qubes     runtimeMetricQubeLister
	zones     runtimeMetricZoneGetter
	infras    runtimeMetricInfraGetter
	providers *provider.Registry

	sweepDeadline  time.Duration
	perQubeTimeout time.Duration
	reuseWindow    time.Duration
	now            func() time.Time
	logf           func(format string, args ...any)

	// shareMu guards the sweep in flight and the last successful one.
	shareMu  sync.Mutex
	inflight *sharedSweep
	last     *sharedSweep
	lastAt   time.Time

	// busyMu guards busyLogged: when each busy qube was last logged.
	busyMu     sync.Mutex
	busyLogged map[string]time.Time
}

// NewRuntimeMetricsCollector builds a collector with the default deadlines.
func NewRuntimeMetricsCollector(
	qubes runtimeMetricQubeLister,
	zones runtimeMetricZoneGetter,
	infras runtimeMetricInfraGetter,
	providers *provider.Registry,
) *RuntimeMetricsCollector {
	return &RuntimeMetricsCollector{
		qubes: qubes, zones: zones, infras: infras, providers: providers,
		sweepDeadline:  DefaultRuntimeMetricsSweepDeadline,
		perQubeTimeout: DefaultRuntimeMetricsPerQubeTimeout,
		reuseWindow:    DefaultRuntimeMetricsReuseWindow,
		now:            time.Now,
		logf:           log.Printf,
		busyLogged:     make(map[string]time.Time),
	}
}

// Collect reports every qube, including non-running and unsupported resources,
// so consumers can distinguish missing telemetry from an empty fleet.
//
// Callers share sweeps: one arriving while a sweep runs waits for it, and one
// arriving within reuseWindow of a successful sweep gets that result. The
// provider load is therefore bounded by time, not by how often the endpoint
// is called. A failed sweep is not reused. The sweep runs detached from any
// single caller, so a caller that gives up gets ctx.Err() while the others
// still get the result. The returned slice is shared and must not be
// modified.
//
// When the sweep deadline passes, the qubes not yet observed are reported with
// RuntimeMetricsTimeout and the rest are returned: a partial answer is more
// useful than none. An error means the qube list itself could not be read, or
// the caller's own context ended.
func (c *RuntimeMetricsCollector) Collect(ctx context.Context) ([]QubeRuntimeMetrics, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	call := c.joinSweep(ctx)
	select {
	case <-call.done:
		return call.items, call.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// sharedSweep is one sweep's result, published by closing done.
type sharedSweep struct {
	done  chan struct{}
	items []QubeRuntimeMetrics
	err   error
}

// joinSweep returns the reusable last result, the sweep in flight, or a newly
// started sweep, in that order of preference.
func (c *RuntimeMetricsCollector) joinSweep(ctx context.Context) *sharedSweep {
	c.shareMu.Lock()
	defer c.shareMu.Unlock()
	if c.last != nil && c.now().Sub(c.lastAt) < c.reuseWindow {
		return c.last
	}
	if c.inflight == nil {
		c.inflight = &sharedSweep{done: make(chan struct{})}
		go c.runSharedSweep(context.WithoutCancel(ctx), c.inflight)
	}
	return c.inflight
}

func (c *RuntimeMetricsCollector) runSharedSweep(ctx context.Context, call *sharedSweep) {
	call.items, call.err = c.sweep(ctx)
	c.shareMu.Lock()
	c.inflight = nil
	if call.err == nil {
		c.last, c.lastAt = call, c.now()
	}
	c.shareMu.Unlock()
	close(call.done)
}

// sweep reads every qube once, within sweepDeadline.
func (c *RuntimeMetricsCollector) sweep(ctx context.Context) ([]QubeRuntimeMetrics, error) {
	sweep, cancel := context.WithTimeout(ctx, c.sweepDeadline)
	defer cancel()
	adapters := newSweepAdapters(sweep, c.providers)
	defer adapters.release()
	qubes, err := listAllRuntimeMetricQubes(sweep, c.qubes)
	if err != nil {
		return nil, fmt.Errorf("list qubes for runtime metrics: %w", err)
	}
	observations := make([]QubeRuntimeMetrics, len(qubes))
	running := make([]int, 0, len(qubes))
	for index, qube := range qubes {
		observations[index] = newRuntimeItem(qube)
		observations[index].Reason = RuntimeMetricsNotRunning
		if qube.Status == models.QubeStatusRunning {
			// Replaced by the qube's observation; left in place for a qube
			// the sweep deadline did not let us observe.
			observations[index].Reason = RuntimeMetricsTimeout
			running = append(running, index)
		}
	}
	c.observeRunning(sweep, adapters, qubes, running, observations)
	c.logUnobserved(observations, running)
	return observations, nil
}

// runtimeObservation carries one worker's result back to the collecting
// goroutine, which is the only writer of the observations slice.
type runtimeObservation struct {
	index int
	item  QubeRuntimeMetrics
}

// observeRunning fans the running qubes out to a bounded worker pool and
// stores each result as it arrives, until all are in or ctx ends. It does not
// wait for a worker still inside a provider call: an adapter that ignored its
// context would otherwise hold the request past its deadline. Such a worker
// finishes into the buffered channel and exits; its late result is dropped.
func (c *RuntimeMetricsCollector) observeRunning(ctx context.Context, adapters *sweepAdapters, qubes []*models.Qube, running []int, out []QubeRuntimeMetrics) {
	jobs := make(chan int, len(running))
	for _, index := range running {
		jobs <- index
	}
	close(jobs)
	results := make(chan runtimeObservation, len(running))
	for range min(runtimeMetricsWorkers, len(running)) {
		go func() {
			for index := range jobs {
				if ctx.Err() != nil {
					return
				}
				results <- runtimeObservation{index: index, item: c.collectOne(ctx, adapters, qubes[index])}
			}
		}()
	}
	for range running {
		select {
		case <-ctx.Done():
			return
		case result := <-results:
			out[result.index] = result.item
		}
	}
}

func (c *RuntimeMetricsCollector) logUnobserved(observations []QubeRuntimeMetrics, running []int) {
	unobserved := 0
	for _, index := range running {
		if observations[index].Reason == RuntimeMetricsTimeout {
			unobserved++
		}
	}
	if unobserved > 0 {
		c.logf("monitoring: runtime metrics sweep reached its %s deadline; %d of %d running qubes were not observed",
			c.sweepDeadline, unobserved, len(running))
	}
}

func newRuntimeItem(qube *models.Qube) QubeRuntimeMetrics {
	return QubeRuntimeMetrics{QubeID: qube.ID, QubeName: qube.Name, ZoneID: qube.ZoneID, State: string(qube.Status)}
}

// listAllRuntimeMetricQubes reads every page. A qube created while the pages
// are read shifts the newest-first order down by one, so the row at a page
// boundary is served twice; it is kept once. A qube deleted meanwhile can
// shift one row past a boundary unread until the next sweep.
func listAllRuntimeMetricQubes(ctx context.Context, lister runtimeMetricQubeLister) ([]*models.Qube, error) {
	var all []*models.Qube
	seen := make(map[string]bool)
	for offset := 0; ; offset += runtimeMetricsPageSize {
		page, err := lister.List(ctx, repository.QubeListOptions{Limit: runtimeMetricsPageSize, Offset: offset})
		if err != nil {
			return nil, err
		}
		for _, qube := range page {
			if !seen[qube.ID] {
				seen[qube.ID] = true
				all = append(all, qube)
			}
		}
		if len(page) < runtimeMetricsPageSize {
			return all, nil
		}
	}
}

// collectOne observes one running qube under its own timeout. A failure is
// logged with its cause and reported by reason only.
func (c *RuntimeMetricsCollector) collectOne(ctx context.Context, adapters *sweepAdapters, qube *models.Qube) QubeRuntimeMetrics {
	item := newRuntimeItem(qube)
	call, cancel := context.WithTimeout(ctx, c.perQubeTimeout)
	defer cancel()
	metrics, reason, err := c.observe(call, adapters, qube)
	if reason == "" {
		item.Metrics = &metrics
		return item
	}
	if call.Err() != nil {
		// Whatever step failed, it failed because time ran out; saying
		// "zone unavailable" for a timed-out lookup would mislead.
		reason = RuntimeMetricsTimeout
	}
	switch {
	case reason == RuntimeMetricsBusy:
		c.logBusy(qube, err)
	case err != nil:
		c.logf("monitoring: runtime metrics for qube %q (%s): %s: %v", qube.Name, qube.ID, reason, err)
	}
	item.Reason = reason
	return item
}

// logBusy logs a busy qube at most once per runtimeMetricsBusyLogInterval, and
// forgets qubes whose last line is older than that.
func (c *RuntimeMetricsCollector) logBusy(qube *models.Qube, cause error) {
	now := c.now()
	c.busyMu.Lock()
	last, seen := c.busyLogged[qube.ID]
	if seen && now.Sub(last) < runtimeMetricsBusyLogInterval {
		c.busyMu.Unlock()
		return
	}
	for id, at := range c.busyLogged {
		if now.Sub(at) >= runtimeMetricsBusyLogInterval {
			delete(c.busyLogged, id)
		}
	}
	c.busyLogged[qube.ID] = now
	c.busyMu.Unlock()
	c.logf("monitoring: qube %q (%s) is held by a provider task, so its runtime metrics are unavailable "+
		"(logged at most every %s per qube): %v", qube.Name, qube.ID, runtimeMetricsBusyLogInterval, cause)
}

// observe returns the measurement, or the reason it is unavailable together
// with its cause: nil for an unsupported provider, rate-limited in the log for
// provider_busy, logged for everything else.
func (c *RuntimeMetricsCollector) observe(ctx context.Context, adapters *sweepAdapters, qube *models.Qube) (provider.RuntimeMetrics, string, error) {
	zone, err := c.zones.GetByID(ctx, qube.ZoneID)
	if err != nil {
		return provider.RuntimeMetrics{}, RuntimeMetricsZoneUnavailable, err
	}
	if zone == nil {
		return provider.RuntimeMetrics{}, RuntimeMetricsZoneUnavailable, fmt.Errorf("zone %q not found", qube.ZoneID)
	}
	in, err := c.infras.Get(ctx, qube.ID)
	if err != nil {
		return provider.RuntimeMetrics{}, RuntimeMetricsInfraUnavailable, err
	}
	if in == nil || in.ComputeVMID <= 0 || in.Node == "" {
		return provider.RuntimeMetrics{}, RuntimeMetricsInfraMissing, errors.New("no compute identity is recorded for a running qube")
	}
	adapter, err := adapters.forZone(zone)
	if err != nil {
		return provider.RuntimeMetrics{}, RuntimeMetricsProviderUnavailable, err
	}
	reader, ok := adapter.(provider.RuntimeMetricsReader)
	if !ok {
		// Expected for providers without per-instance telemetry; not logged.
		return provider.RuntimeMetrics{}, RuntimeMetricsUnsupported, nil
	}
	metrics, err := reader.RuntimeMetrics(ctx, qube, *in)
	if errors.Is(err, provider.ErrInstanceBusy) {
		return provider.RuntimeMetrics{}, RuntimeMetricsBusy, err
	}
	if err != nil {
		return provider.RuntimeMetrics{}, RuntimeMetricsUnavailable, err
	}
	return metrics, "", nil
}

// sweepAdapters builds each zone's adapter at most once per sweep and shares
// it between that zone's qubes: one credential resolution and, for a password
// credential, one provider login per zone instead of one per qube. Adapters
// are built with the sweep's context, so one qube's timeout cannot fail the
// whole zone.
type sweepAdapters struct {
	ctx       context.Context
	providers *provider.Registry

	mu    sync.Mutex
	zones map[string]*sweepAdapter
}

type sweepAdapter struct {
	once    sync.Once
	built   bool
	adapter provider.Adapter
	err     error
}

func newSweepAdapters(ctx context.Context, providers *provider.Registry) *sweepAdapters {
	return &sweepAdapters{ctx: ctx, providers: providers, zones: make(map[string]*sweepAdapter)}
}

func (s *sweepAdapters) forZone(zone *models.Zone) (provider.Adapter, error) {
	if s.providers == nil {
		return nil, errors.New("no provider registry is configured")
	}
	s.mu.Lock()
	entry, ok := s.zones[zone.ID]
	if !ok {
		entry = &sweepAdapter{}
		s.zones[zone.ID] = entry
	}
	s.mu.Unlock()
	entry.once.Do(func() {
		adapter, err := s.providers.For(s.ctx, zone)
		s.mu.Lock()
		entry.built, entry.adapter, entry.err = true, adapter, err
		s.mu.Unlock()
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	return entry.adapter, entry.err
}

// release drops the idle connections of every adapter built in this sweep.
// One a deadline-abandoned worker is still building is left to the provider
// transport's idle timeout (providerhttp.IdleConnTimeout).
func (s *sweepAdapters) release() {
	s.mu.Lock()
	built := make([]provider.Adapter, 0, len(s.zones))
	for _, entry := range s.zones {
		if entry.built && entry.adapter != nil {
			built = append(built, entry.adapter)
		}
	}
	s.mu.Unlock()
	for _, adapter := range built {
		if closer, ok := adapter.(idleConnectionCloser); ok {
			closer.CloseIdleConnections()
		}
	}
}
