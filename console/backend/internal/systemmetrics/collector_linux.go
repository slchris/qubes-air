//go:build linux

package systemmetrics

import (
	"os"
	"syscall"
	"time"
)

// NewHostSampler samples this Linux host through /proc and the root
// filesystem. Create one per process and share it: each Sampler keeps its own
// baseline, so a fresh one per request would never produce a CPU or network
// delta. It takes one sample now, so the first request made a second or more
// later already has CPU usage and network rates.
func NewHostSampler() *Sampler {
	start := time.Now()
	sampler := newSampler(os.DirFS("/proc"), rootFilesystemUsage, time.Now,
		func() time.Duration { return time.Since(start) })
	sampler.Collect()
	return sampler
}

func rootFilesystemUsage() (blocks, free uint64, err error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs("/", &stat); err != nil {
		return 0, 0, err
	}
	return stat.Blocks, stat.Bfree, nil
}
