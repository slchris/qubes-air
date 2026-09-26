// Package systemmetrics samples host-wide counters of the machine the Console
// runs on. The values describe that host, not the managed qubes: a Console VM's
// CPU or disk says nothing about a qube running in a remote zone.
package systemmetrics

import "time"

// Metrics contains host-wide observations. A value that could not be measured
// is nil and serializes as null; it is never reported as zero.
type Metrics struct {
	// CPUUsage is the share of non-idle CPU time across all CPUs, 0-100, over
	// the interval since the previous sample.
	CPUUsage *float64 `json:"cpuUsage"`
	// MemoryUsage is (MemTotal - MemAvailable) / MemTotal, 0-100.
	MemoryUsage *float64 `json:"memoryUsage"`
	// DiskUsage is the share of the root filesystem's blocks in use, 0-100.
	DiskUsage *float64 `json:"diskUsage"`
	// NetworkIn and NetworkOut are bytes per second summed over every
	// non-loopback interface since the previous sample.
	NetworkIn  *int64 `json:"networkIn"`
	NetworkOut *int64 `json:"networkOut"`
	// Source names the collector, so a reader can tell which host produced the
	// numbers or that the platform is unsupported.
	Source     string    `json:"source"`
	CapturedAt time.Time `json:"capturedAt"`
	// Reason says why at least one value is missing; empty when all are set.
	Reason string `json:"reason,omitempty"`
}

func floatPointer(value float64) *float64 { return &value }

func int64Pointer(value int64) *int64 { return &value }
