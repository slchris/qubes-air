//go:build !linux

package systemmetrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHostSamplerIsUnsupportedOffLinux(t *testing.T) {
	got := NewHostSampler().Collect()

	assert.Equal(t, "unsupported-platform", got.Source)
	assert.Contains(t, got.Reason, "Linux only")
	assert.Nil(t, got.CPUUsage)
	assert.Nil(t, got.MemoryUsage)
	assert.Nil(t, got.DiskUsage)
	assert.Nil(t, got.NetworkIn)
	assert.Nil(t, got.NetworkOut)
}
