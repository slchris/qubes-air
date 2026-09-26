package systemmetrics

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnavailableMetricsSerializeAsNull(t *testing.T) {
	payload, err := json.Marshal(Metrics{Source: "unsupported-platform"})
	require.NoError(t, err)
	require.JSONEq(t, `{"cpuUsage":null,"memoryUsage":null,"diskUsage":null,"networkIn":null,"networkOut":null,"source":"unsupported-platform","capturedAt":"0001-01-01T00:00:00Z"}`, string(payload))
}

func TestParseCPUTicks(t *testing.T) {
	idle, total, err := parseCPUTicks("cpu  10 2 3 80 5 1 2 0 70 80")
	require.NoError(t, err)
	require.Equal(t, uint64(85), idle)
	require.Equal(t, uint64(103), total)
	_, _, err = parseCPUTicks("cpu bad")
	require.Error(t, err)
}

func TestCPUUsageUsesCounterDeltas(t *testing.T) {
	previous := cpuCounters{idle: 50, total: 100}
	require.Nil(t, cpuDelta(previous, 55, 120, false))
	value := cpuDelta(previous, 55, 120, true)
	require.NotNil(t, value)
	require.InDelta(t, 75.0, *value, 0.001)
	require.Nil(t, cpuDelta(previous, 40, 120, true))
}

func TestParseMemoryUsage(t *testing.T) {
	value, err := parseMemUsage(strings.NewReader("MemTotal: 1000 kB\nMemAvailable: 250 kB\n"))
	require.NoError(t, err)
	require.InDelta(t, 75.0, value, 0.001)
	_, err = parseMemUsage(strings.NewReader("MemTotal: 0 kB\nMemAvailable: 0 kB\n"))
	require.Error(t, err)
}

func TestParseNetworkCountersAggregatesPhysicalInterfacesOnly(t *testing.T) {
	input := "Inter-| Receive | Transmit\n" +
		" face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\n" +
		" lo: 900 1 0 0 0 0 0 0 800 1 0 0 0 0 0 0\n" +
		" eth0: 100 1 0 0 0 0 0 0 200 1 0 0 0 0 0 0\n" +
		" wlan0: 40 1 0 0 0 0 0 0 60 1 0 0 0 0 0 0\n"
	rx, tx, err := parseNetworkCounters(strings.NewReader(input))
	require.NoError(t, err)
	require.Equal(t, uint64(140), rx)
	require.Equal(t, uint64(260), tx)
}

func TestCounterDeltaRejectsCounterReset(t *testing.T) {
	delta, valid := counterDelta(100, 120)
	require.True(t, valid)
	require.Equal(t, uint64(20), delta)
	_, valid = counterDelta(120, 100)
	require.False(t, valid)
}
