//go:build !linux

package systemmetrics

// NewHostSampler reports that host metrics are unsupported on this platform
// rather than presenting zeros as measurements. The Console is deployed on
// Linux; other platforms are development hosts.
func NewHostSampler() *Sampler {
	return newUnsupportedSampler("unsupported-platform", "host metrics collection is supported on Linux only")
}
