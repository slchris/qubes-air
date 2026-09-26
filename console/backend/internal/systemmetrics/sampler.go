package systemmetrics

import (
	"bufio"
	"io/fs"
	"sync"
	"time"
)

const (
	// linuxSource labels metrics read from a Linux procfs.
	linuxSource = "console-host-linux"
	// incompleteReason is reported while any value is missing: CPU usage and
	// network rates need two samples, and any counter source may be unreadable.
	incompleteReason = "some counters are unavailable or awaiting a second sample"
	// MinSampleInterval is the shortest interval a delta is computed over. A
	// call sooner than this after the last sample gets that sample again: a
	// rate over a few milliseconds is noise, and a caller polling in a loop
	// must not turn into a /proc reader in a loop.
	MinSampleInterval = time.Second
)

// FilesystemUsage returns the block count and free blocks of the root
// filesystem.
type FilesystemUsage func() (blocks, free uint64, err error)

// counters is the previous sample that CPU usage and network rates are
// computed against. captured is on the monotonic clock.
type counters struct {
	cpu            cpuCounters
	received, sent uint64
	captured       time.Duration
}

// Sampler reads host counters. CPU usage and network rates are deltas between
// two samples, so a Sampler keeps the previous one; mu serializes Collect so
// concurrent requests can neither interleave a half-updated baseline nor move
// its timestamp backwards.
//
// Two clocks are kept apart. Intervals are measured on elapsed, a monotonic
// clock, so a wall-clock step (NTP, a manual change) cannot produce a negative
// or inflated rate; wall is read only for CapturedAt.
//
// A Sampler is created by NewHostSampler for the running platform. Its sources
// (procfs, the root filesystem and both clocks) are fields so tests can supply
// their own and each gets an independent baseline.
type Sampler struct {
	source string
	// unsupported, when set, is the reason this platform cannot be sampled;
	// Collect then reports no values at all.
	unsupported string
	proc        fs.FS
	rootfs      FilesystemUsage
	wall        func() time.Time
	elapsed     func() time.Duration

	mu         sync.Mutex
	previous   counters
	hasCPU     bool
	hasNetwork bool
	// last is the most recent sample, taken at lastAt on the monotonic clock,
	// returned again to a call within MinSampleInterval of it.
	last    Metrics
	lastAt  time.Duration
	hasLast bool
}

// newSampler builds a Linux procfs sampler over the given sources. elapsed
// must be monotonic.
func newSampler(proc fs.FS, rootfs FilesystemUsage, wall func() time.Time, elapsed func() time.Duration) *Sampler {
	return &Sampler{source: linuxSource, proc: proc, rootfs: rootfs, wall: wall, elapsed: elapsed}
}

// newUnsupportedSampler reports reason instead of measurements.
func newUnsupportedSampler(source, reason string) *Sampler {
	return &Sampler{source: source, unsupported: reason, wall: time.Now}
}

// Collect takes one sample, or returns the last one if it is less than
// MinSampleInterval old. A counter that cannot be read or parsed is left nil;
// CPU usage and network rates stay nil until a second valid sample.
func (s *Sampler) Collect() Metrics {
	if s.unsupported != "" {
		return Metrics{Source: s.source, CapturedAt: s.wall().UTC(), Reason: s.unsupported}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Read the clock under the lock: a timestamp taken before it could be
	// older than the baseline another caller has just stored.
	at := s.elapsed()
	if s.hasLast && at-s.lastAt < MinSampleInterval {
		return s.last
	}
	metrics := Metrics{Source: s.source, CapturedAt: s.wall().UTC()}
	s.sampleCPU(&metrics)
	s.sampleMemory(&metrics)
	s.sampleDisk(&metrics)
	s.sampleNetwork(at, &metrics)
	if metrics.CPUUsage == nil || metrics.MemoryUsage == nil || metrics.DiskUsage == nil ||
		metrics.NetworkIn == nil || metrics.NetworkOut == nil {
		metrics.Reason = incompleteReason
	}
	s.last, s.lastAt, s.hasLast = metrics, at, true
	return metrics
}

// sampleCPU reads the aggregate line of /proc/stat. Called with s.mu held.
func (s *Sampler) sampleCPU(metrics *Metrics) {
	file, err := s.proc.Open("stat")
	if err != nil {
		return
	}
	var line string
	scanner := bufio.NewScanner(file)
	if scanner.Scan() {
		line = scanner.Text()
	}
	closeErr := file.Close()
	if scanner.Err() != nil || closeErr != nil {
		return
	}
	idle, total, err := parseCPUTicks(line)
	if err != nil {
		return
	}
	metrics.CPUUsage = cpuDelta(s.previous.cpu, idle, total, s.hasCPU)
	s.previous.cpu = cpuCounters{idle: idle, total: total}
	s.hasCPU = true
}

// sampleMemory reads /proc/meminfo. It keeps no state.
func (s *Sampler) sampleMemory(metrics *Metrics) {
	file, err := s.proc.Open("meminfo")
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	if value, err := parseMemUsage(file); err == nil {
		metrics.MemoryUsage = floatPointer(value)
	}
}

// sampleDisk reports root filesystem usage. It keeps no state.
func (s *Sampler) sampleDisk(metrics *Metrics) {
	blocks, free, err := s.rootfs()
	if err != nil || blocks == 0 || free > blocks {
		return
	}
	metrics.DiskUsage = floatPointer(float64(blocks-free) / float64(blocks) * 100)
}

// sampleNetwork reads /proc/net/dev and derives byte rates from the previous
// sample, over the monotonic interval since it. A counter that went backwards
// (an interface was reset or removed) yields no rate for this interval but
// still becomes the new baseline. Called with s.mu held.
func (s *Sampler) sampleNetwork(at time.Duration, metrics *Metrics) {
	file, err := s.proc.Open("net/dev")
	if err != nil {
		return
	}
	received, sent, err := parseNetworkCounters(file)
	_ = file.Close()
	if err != nil {
		return
	}
	if s.hasNetwork && at > s.previous.captured {
		seconds := (at - s.previous.captured).Seconds()
		rxDelta, rxOK := counterDelta(s.previous.received, received)
		txDelta, txOK := counterDelta(s.previous.sent, sent)
		if rxOK && txOK {
			metrics.NetworkIn = int64Pointer(int64(float64(rxDelta) / seconds))
			metrics.NetworkOut = int64Pointer(int64(float64(txDelta) / seconds))
		}
	}
	s.previous.received, s.previous.sent = received, sent
	s.previous.captured = at
	s.hasNetwork = true
}
