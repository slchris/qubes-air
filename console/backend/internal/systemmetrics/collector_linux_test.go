//go:build linux

package systemmetrics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Smoke test against the real /proc of the machine running the tests. It uses
// its own Sampler, so it is independent of every other test and of -count.
// The delta arithmetic is covered hermetically in sampler_test.go.
func TestHostSamplerReadsThisLinuxHost(t *testing.T) {
	sampler := NewHostSampler()

	// Within MinSampleInterval of construction this is the priming sample.
	first := sampler.Collect()
	require.Equal(t, "console-host-linux", first.Source)
	require.False(t, first.CapturedAt.IsZero())
	require.Equal(t, time.UTC, first.CapturedAt.Location())
	require.NotNil(t, first.MemoryUsage)
	require.NotNil(t, first.DiskUsage)
	require.GreaterOrEqual(t, *first.MemoryUsage, 0.0)
	require.LessOrEqual(t, *first.MemoryUsage, 100.0)
	require.GreaterOrEqual(t, *first.DiskUsage, 0.0)
	require.LessOrEqual(t, *first.DiskUsage, 100.0)

	// The construction-time sample is the baseline, so the first sample taken
	// after MinSampleInterval already carries CPU usage and network rates.
	var latest Metrics
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		time.Sleep(250 * time.Millisecond)
		latest = sampler.Collect()
		if latest.CPUUsage != nil && latest.NetworkIn != nil {
			break
		}
	}
	require.NotNil(t, latest.CPUUsage, "the CPU tick counters advance within a few seconds")
	require.NotNil(t, latest.NetworkIn, "a network rate is available after the primed baseline")
	require.GreaterOrEqual(t, *latest.CPUUsage, 0.0)
	require.LessOrEqual(t, *latest.CPUUsage, 100.0)
	require.GreaterOrEqual(t, *latest.NetworkIn, int64(0))
}
