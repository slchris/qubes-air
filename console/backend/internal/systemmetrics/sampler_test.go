package systemmetrics

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeHost is one test's private host: procfs files, root filesystem totals,
// a wall clock and a monotonic clock. Every test builds its own, so no sample
// baseline is shared between tests or between -count repetitions.
type fakeHost struct {
	files        fstest.MapFS
	blocks, free uint64
	statErr      error
	wall         time.Time
	mono         time.Duration
}

var sampleStart = time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

func newFakeHost() *fakeHost {
	h := &fakeHost{files: fstest.MapFS{}, blocks: 1000, free: 750, wall: sampleStart, mono: time.Hour}
	h.setCPU(80, 100)
	h.setMemory(1000, 250)
	h.setNetwork(1000, 2000)
	return h
}

// setCPU writes an aggregate /proc/stat line whose idle ticks are idle and
// whose summed ticks are total.
func (h *fakeHost) setCPU(idle, total uint64) {
	h.files["stat"] = &fstest.MapFile{Data: fmt.Appendf(nil, "cpu  %d 0 0 %d 0 0 0 0 0 0\ncpu0 1 2 3 4\n", total-idle, idle)}
}

func (h *fakeHost) setMemory(totalKB, availableKB uint64) {
	h.files["meminfo"] = &fstest.MapFile{Data: fmt.Appendf(nil, "MemTotal: %d kB\nMemFree: 1 kB\nMemAvailable: %d kB\n", totalKB, availableKB)}
}

// setNetwork writes /proc/net/dev with loopback traffic that must be ignored
// and one physical interface carrying rx/tx bytes.
func (h *fakeHost) setNetwork(rx, tx uint64) {
	h.files["net/dev"] = &fstest.MapFile{Data: fmt.Appendf(nil,
		"Inter-|   Receive                            |  Transmit\n"+
			" face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\n"+
			"    lo: 999999 1 0 0 0 0 0 0 999999 1 0 0 0 0 0 0\n"+
			"  eth0: %d 1 0 0 0 0 0 0 %d 1 0 0 0 0 0 0\n", rx, tx)}
}

// advance moves both clocks forward together, as they do without a wall step.
func (h *fakeHost) advance(d time.Duration) {
	h.wall = h.wall.Add(d)
	h.mono += d
}

func (h *fakeHost) sampler() *Sampler {
	return newSampler(h.files,
		func() (uint64, uint64, error) { return h.blocks, h.free, h.statErr },
		func() time.Time { return h.wall },
		func() time.Duration { return h.mono })
}

func TestSamplerFirstSampleReportsLevelsAndWaitsForDeltas(t *testing.T) {
	host := newFakeHost()

	got := host.sampler().Collect()

	assert.Equal(t, "console-host-linux", got.Source)
	assert.Equal(t, sampleStart, got.CapturedAt)
	require.NotNil(t, got.MemoryUsage)
	assert.InDelta(t, 75.0, *got.MemoryUsage, 0.001)
	require.NotNil(t, got.DiskUsage)
	assert.InDelta(t, 25.0, *got.DiskUsage, 0.001)
	assert.Nil(t, got.CPUUsage, "CPU usage is a delta and needs a second sample")
	assert.Nil(t, got.NetworkIn, "a rate needs a second sample")
	assert.Nil(t, got.NetworkOut, "a rate needs a second sample")
	assert.Equal(t, incompleteReason, got.Reason)
}

func TestSamplerSecondSampleComputesCPUUsageAndNetworkRates(t *testing.T) {
	host := newFakeHost()
	sampler := host.sampler()
	sampler.Collect()

	host.advance(2 * time.Second)
	host.setCPU(140, 200) // 100 ticks elapsed, 60 idle: 40% busy
	host.setNetwork(5000, 2400)
	got := sampler.Collect()

	require.NotNil(t, got.CPUUsage)
	assert.InDelta(t, 40.0, *got.CPUUsage, 0.001)
	require.NotNil(t, got.NetworkIn)
	require.NotNil(t, got.NetworkOut)
	assert.Equal(t, int64(2000), *got.NetworkIn, "4000 bytes over 2s, loopback excluded")
	assert.Equal(t, int64(200), *got.NetworkOut, "400 bytes over 2s, loopback excluded")
	assert.Empty(t, got.Reason, "every value is present")
}

func TestSamplerDropsDeltasAcrossCounterResetAndRebaselines(t *testing.T) {
	host := newFakeHost()
	sampler := host.sampler()
	sampler.Collect()

	// Counters went backwards, as they do when an interface is recreated. No
	// usage or rate may be derived from that interval.
	host.advance(time.Second)
	host.setCPU(10, 20)
	host.setNetwork(10, 20)
	reset := sampler.Collect()
	assert.Nil(t, reset.CPUUsage)
	assert.Nil(t, reset.NetworkIn)
	assert.Nil(t, reset.NetworkOut)
	assert.Equal(t, incompleteReason, reset.Reason)

	// The reset sample is the new baseline, so the next interval is measured
	// from it rather than from the stale pre-reset counters.
	host.advance(time.Second)
	host.setCPU(60, 120)
	host.setNetwork(110, 70)
	next := sampler.Collect()
	require.NotNil(t, next.CPUUsage)
	assert.InDelta(t, 50.0, *next.CPUUsage, 0.001)
	require.NotNil(t, next.NetworkIn)
	assert.Equal(t, int64(100), *next.NetworkIn)
	require.NotNil(t, next.NetworkOut)
	assert.Equal(t, int64(50), *next.NetworkOut)
}

// Rates are measured on the monotonic clock. A wall-clock step backwards (an
// NTP correction, a manual change) must neither suppress nor distort them, and
// CapturedAt still reports the wall time, in UTC.
func TestSamplerMeasuresIntervalsOnTheMonotonicClock(t *testing.T) {
	host := newFakeHost()
	sampler := host.sampler()
	sampler.Collect()

	host.mono += 2 * time.Second
	host.wall = host.wall.Add(-time.Hour).In(time.FixedZone("UTC+8", 8*60*60))
	host.setNetwork(5000, 6000)
	got := sampler.Collect()

	require.NotNil(t, got.NetworkIn, "a wall step backwards must not suppress the rate")
	assert.Equal(t, int64(2000), *got.NetworkIn, "4000 bytes over the 2 monotonic seconds")
	require.NotNil(t, got.NetworkOut)
	assert.Equal(t, int64(2000), *got.NetworkOut)
	assert.Equal(t, time.UTC, got.CapturedAt.Location())
	assert.True(t, got.CapturedAt.Equal(sampleStart.Add(-time.Hour)), "CapturedAt is the wall time")
}

// A call sooner than MinSampleInterval after the last sample gets that sample
// again, unchanged, without reading the counters.
func TestSamplerReusesTheLastSampleWithinTheMinimumInterval(t *testing.T) {
	host := newFakeHost()
	sampler := host.sampler()
	sampler.Collect()
	host.advance(2 * time.Second)
	host.setCPU(140, 200)
	sampled := sampler.Collect()
	require.NotNil(t, sampled.CPUUsage)

	host.advance(MinSampleInterval - time.Millisecond)
	host.setCPU(141, 1000)
	host.setNetwork(9e9, 9e9)
	host.free = 0
	reused := sampler.Collect()
	assert.Equal(t, sampled, reused, "within the minimum interval the last sample is returned as it was")

	host.advance(time.Millisecond)
	fresh := sampler.Collect()
	assert.True(t, fresh.CapturedAt.After(sampled.CapturedAt))
	require.NotNil(t, fresh.DiskUsage)
	assert.InDelta(t, 100.0, *fresh.DiskUsage, 0.001, "at the minimum interval the counters are read again")
	require.NotNil(t, fresh.CPUUsage)
	assert.InDelta(t, 100.0*(800-1)/800, *fresh.CPUUsage, 0.001, "measured against the last real sample")
}

func TestSamplerReportsUnreadableSourcesAsMissing(t *testing.T) {
	host := newFakeHost()
	host.files = fstest.MapFS{}
	host.statErr = errors.New("statfs: permission denied")

	got := host.sampler().Collect()

	assert.Equal(t, "console-host-linux", got.Source)
	assert.Nil(t, got.CPUUsage)
	assert.Nil(t, got.MemoryUsage)
	assert.Nil(t, got.DiskUsage)
	assert.Nil(t, got.NetworkIn)
	assert.Nil(t, got.NetworkOut)
	assert.Equal(t, incompleteReason, got.Reason)
}

func TestSamplerRejectsImpossibleFilesystemTotals(t *testing.T) {
	for name, totals := range map[string][2]uint64{
		"no blocks":            {0, 0},
		"more free than total": {100, 101},
	} {
		t.Run(name, func(t *testing.T) {
			host := newFakeHost()
			host.blocks, host.free = totals[0], totals[1]
			assert.Nil(t, host.sampler().Collect().DiskUsage)
		})
	}
}

func TestSamplerIgnoresMalformedCountersWithoutPoisoningTheBaseline(t *testing.T) {
	host := newFakeHost()
	sampler := host.sampler()
	sampler.Collect()

	host.advance(time.Second)
	host.files["stat"] = &fstest.MapFile{Data: []byte("cpu not numbers at all\n")}
	host.files["meminfo"] = &fstest.MapFile{Data: []byte("MemTotal: many kB\n")}
	host.files["net/dev"] = &fstest.MapFile{Data: []byte("  eth0: 1 2 3\n")}
	bad := sampler.Collect()
	assert.Nil(t, bad.CPUUsage)
	assert.Nil(t, bad.MemoryUsage)
	assert.Nil(t, bad.NetworkIn)
	assert.NotNil(t, bad.DiskUsage, "one bad source does not hide the others")

	// The next valid sample is measured against the last VALID one.
	host.advance(time.Second)
	host.setCPU(130, 200)
	host.setMemory(1000, 500)
	host.setNetwork(1400, 2200)
	next := sampler.Collect()
	require.NotNil(t, next.CPUUsage)
	assert.InDelta(t, 50.0, *next.CPUUsage, 0.001)
	require.NotNil(t, next.NetworkIn)
	assert.Equal(t, int64(200), *next.NetworkIn, "400 bytes over the 2s since the last valid sample")
}

// Concurrent requests share one Sampler. Under -race this proves the baseline
// is only touched under the lock, and the timestamps it stores never go back.
func TestSamplerIsSafeForConcurrentCollect(t *testing.T) {
	host := newFakeHost()
	var ticks atomic.Int64
	// Every call moves the clocks 100ms, so the 800 calls below span many
	// sampling intervals and many reuses of a sample.
	sampler := newSampler(host.files,
		func() (uint64, uint64, error) { return host.blocks, host.free, nil },
		func() time.Time { return sampleStart.Add(time.Duration(ticks.Load()) * 100 * time.Millisecond) },
		func() time.Duration { return time.Duration(ticks.Add(1)) * 100 * time.Millisecond })

	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				got := sampler.Collect()
				assert.NotNil(t, got.MemoryUsage)
			}
		}()
	}
	wg.Wait()

	last := sampler.Collect()
	assert.True(t, last.CapturedAt.After(sampleStart))
	require.NotNil(t, last.NetworkIn, "unchanged counters over elapsed time are a zero rate, not a missing one")
	assert.Zero(t, *last.NetworkIn)
}

func TestUnsupportedSamplerReportsReasonInsteadOfValues(t *testing.T) {
	got := newUnsupportedSampler("unsupported-platform", "not on this platform").Collect()

	assert.Equal(t, "unsupported-platform", got.Source)
	assert.Equal(t, "not on this platform", got.Reason)
	assert.False(t, got.CapturedAt.IsZero())
	assert.Nil(t, got.CPUUsage)
	assert.Nil(t, got.MemoryUsage)
	assert.Nil(t, got.DiskUsage)
	assert.Nil(t, got.NetworkIn)
	assert.Nil(t, got.NetworkOut)
}
