package systemmetrics

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type cpuCounters struct{ idle, total uint64 }

func cpuDelta(previous cpuCounters, idle, total uint64, available bool) *float64 {
	if !available || total <= previous.total || idle < previous.idle {
		return nil
	}
	totalDelta := total - previous.total
	idleDelta := idle - previous.idle
	if idleDelta > totalDelta {
		return nil
	}
	return floatPointer(float64(totalDelta-idleDelta) / float64(totalDelta) * 100)
}

func counterDelta(previous, current uint64) (uint64, bool) {
	if current < previous {
		return 0, false
	}
	return current - previous, true
}

func parseCPUTicks(line string) (idle, total uint64, err error) {
	fields := strings.Fields(line)
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0, fmt.Errorf("invalid aggregate CPU counters")
	}
	// Linux includes guest counters in user/nice, so summing the final guest
	// fields would double-count virtual CPU time.
	cpuFields := fields[1:]
	if len(cpuFields) > 8 {
		cpuFields = cpuFields[:8]
	}
	for index, field := range cpuFields {
		value, parseErr := strconv.ParseUint(field, 10, 64)
		if parseErr != nil {
			return 0, 0, fmt.Errorf("invalid CPU counter: %w", parseErr)
		}
		total += value
		if index == 3 || index == 4 { // idle and iowait
			idle += value
		}
	}
	return idle, total, nil
}

func parseMemUsage(reader io.Reader) (float64, error) {
	values := make(map[string]uint64, 3)
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		if fields[0] != "MemTotal:" && fields[0] != "MemAvailable:" {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid memory counter: %w", err)
		}
		values[fields[0]] = value
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	total, hasTotal := values["MemTotal:"]
	available, hasAvailable := values["MemAvailable:"]
	if !hasTotal || !hasAvailable || total == 0 || available > total {
		return 0, fmt.Errorf("memory counters are incomplete")
	}
	return float64(total-available) / float64(total) * 100, nil
}

func parseNetworkCounters(reader io.Reader) (received, sent uint64, err error) {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.Contains(line, ":") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if strings.TrimSpace(parts[0]) == "lo" {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 9 {
			return 0, 0, fmt.Errorf("invalid network counters")
		}
		rx, parseErr := strconv.ParseUint(fields[0], 10, 64)
		if parseErr != nil {
			return 0, 0, fmt.Errorf("invalid received byte counter: %w", parseErr)
		}
		tx, parseErr := strconv.ParseUint(fields[8], 10, 64)
		if parseErr != nil {
			return 0, 0, fmt.Errorf("invalid sent byte counter: %w", parseErr)
		}
		received += rx
		sent += tx
	}
	return received, sent, scanner.Err()
}
