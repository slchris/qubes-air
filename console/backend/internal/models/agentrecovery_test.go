package models

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClassifyAgentRecovery — the whole point of the field is one distinction:
// "failing, but something automatic may still fix it" versus "failing for
// longer than anything automatic could still be doing". Getting the boundary
// wrong in the second direction is what makes an operator wait forever for an
// agent that already gave up; getting it wrong in the first direction paints a
// still-booting qube as hopeless.
func TestClassifyAgentRecovery(t *testing.T) {
	base := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time {
		tm := base.Add(d)
		return &tm
	}

	tests := []struct {
		name string
		qube *Qube
		want AgentRecovery
	}{
		{
			"no qube at all",
			nil,
			AgentRecoveryNone,
		},
		{
			"healthy",
			&Qube{AgentHealth: AgentHealthHealthy, AgentLastProbedAt: at(0)},
			AgentRecoveryNone,
		},
		{
			// A qube inside its boot grace window is not a recovery case: the
			// console has not made a verdict about it yet.
			"still starting",
			&Qube{AgentHealth: AgentHealthStarting, AgentLastProbedAt: at(0)},
			AgentRecoveryNone,
		},
		{
			"unknown (no opinion)",
			&Qube{AgentHealth: AgentHealthUnknown, AgentLastProbedAt: at(0)},
			AgentRecoveryNone,
		},
		{
			// Rows from before the streak column existed, or a qube a caller
			// built by hand. Failing, but with no observed span: the honest
			// reading is the one that does not raise an alarm.
			"failing with no recorded streak start",
			&Qube{AgentHealth: AgentHealthUnreachable, AgentLastProbedAt: at(0)},
			AgentRecoveryPending,
		},
		{
			"failing with a streak but no probe time",
			&Qube{AgentHealth: AgentHealthUnreachable, AgentFailingSince: at(0)},
			AgentRecoveryPending,
		},
		{
			"first failing probe of the streak",
			&Qube{AgentHealth: AgentHealthUnreachable, AgentFailingSince: at(0), AgentLastProbedAt: at(0)},
			AgentRecoveryPending,
		},
		{
			"one second inside the budget",
			&Qube{
				AgentHealth:       AgentHealthUnreachable,
				AgentFailingSince: at(0),
				AgentLastProbedAt: at(AgentStartLimitBudget - time.Second),
			},
			AgentRecoveryPending,
		},
		{
			// EXACTLY at the budget: every automatic start the unit's policy
			// allows has had its whole window, so waiting is no longer a plan.
			"exactly at the budget",
			&Qube{
				AgentHealth:       AgentHealthUnreachable,
				AgentFailingSince: at(0),
				AgentLastProbedAt: at(AgentStartLimitBudget),
			},
			AgentRecoveryManual,
		},
		{
			"one second past the budget",
			&Qube{
				AgentHealth:       AgentHealthUnreachable,
				AgentFailingSince: at(0),
				AgentLastProbedAt: at(AgentStartLimitBudget + time.Second),
			},
			AgentRecoveryManual,
		},
		{
			// Just recovered: the streak is cleared by the same write that
			// records the healthy probe, but the classification must not depend
			// on that happening first.
			"healthy with a stale streak still on the row",
			&Qube{
				AgentHealth:       AgentHealthHealthy,
				AgentFailingSince: at(0),
				AgentLastProbedAt: at(24 * time.Hour),
			},
			AgentRecoveryNone,
		},
		{
			// The console stopped probing (interval set to 0, or a dead prober)
			// and came back: the two observations it did make are a minute
			// apart, so the answer must stay "pending" however old they are.
			// Nothing here may consult the wall clock.
			"old observations whose span is short",
			&Qube{
				AgentHealth:       AgentHealthUnreachable,
				AgentFailingSince: at(-30 * 24 * time.Hour),
				AgentLastProbedAt: at(-30*24*time.Hour + time.Minute),
			},
			AgentRecoveryPending,
		},
		{
			// Cannot happen through UpdateAgentHealth; classified rather than
			// trusted, because a negative span must not be read as "long down".
			"probe time before the streak start",
			&Qube{
				AgentHealth:       AgentHealthUnreachable,
				AgentFailingSince: at(0),
				AgentLastProbedAt: at(-time.Hour),
			},
			AgentRecoveryPending,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ClassifyAgentRecovery(tt.qube))
		})
	}
}

// TestQubeAgentRecoveryJSONKey — the UI and any API client read this key by
// name, and renaming the tag would silently take the state off the screen again.
// The field is omitted only when there is nothing to say (a caller-built struct
// that was never classified), never as an empty string.
func TestQubeAgentRecoveryJSONKey(t *testing.T) {
	raw, err := json.Marshal(&Qube{
		AgentHealth:   AgentHealthUnreachable,
		AgentRecovery: AgentRecoveryManual,
	})
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	assert.Equal(t, "manual", decoded["agent_recovery"])
	assert.Equal(t, "unreachable", decoded["agent_health"])

	raw, err = json.Marshal(&Qube{AgentHealth: AgentHealthHealthy})
	require.NoError(t, err)
	decoded = map[string]any{}
	require.NoError(t, json.Unmarshal(raw, &decoded))
	_, present := decoded["agent_recovery"]
	assert.False(t, present,
		"an unclassified qube must omit the field rather than render an empty state")
}

// unitSetting matches a systemd unit directive: the key must start the line, so
// the indented continuation lines of ExecStart are not mistaken for settings.
var unitSetting = regexp.MustCompile(`(?m)^([A-Za-z][A-Za-z0-9]*)=(.*)$`)

// TestAgentStartLimitBudgetMatchesTheShippedUnit — AgentStartLimitBudget is a
// claim about the unit this repository ships, not a console preference, so it
// must be checked against that file. Without this test, raising
// StartLimitIntervalSec in the unit would silently make the console declare a
// still-retrying agent hopeless.
//
// What this does NOT test: systemd itself. There is no systemd on the machine
// this test runs on, so the rate limiter's behavior is taken from the unit's
// own settings, which is all the claim needs.
func TestAgentStartLimitBudgetMatchesTheShippedUnit(t *testing.T) {
	const unitPath = "../../../../packaging/agent-deb/qubes-air-agent.service"

	body, err := os.ReadFile(unitPath)
	require.NoError(t, err, "the unit the budget is derived from must exist at %s", unitPath)

	settings := map[string]string{}
	for _, m := range unitSetting.FindAllStringSubmatch(string(body), -1) {
		settings[m[1]] = strings.TrimSpace(m[2])
	}

	require.Equal(t, "on-failure", settings["Restart"],
		"the derivation assumes the unit is restarted on failure at all")

	interval := unitTimeSpan(t, settings["StartLimitIntervalSec"])
	burst := unitCount(t, settings["StartLimitBurst"])
	restartDelay := unitTimeSpan(t, settings["RestartSec"])

	assert.GreaterOrEqual(t, AgentStartLimitBudget, interval,
		"the budget must cover the whole start-rate-limit window (%s)", interval)

	spread := time.Duration(burst-1) * restartDelay
	assert.GreaterOrEqual(t, AgentStartLimitBudget, spread,
		"the budget must cover every start the burst allows, %d x %s apart (%s)", burst-1, restartDelay, spread)
}

// unitTimeSpan parses a systemd time span as used by the unit: a bare integer
// is seconds. Anything else must fail loudly rather than be guessed at, because
// guessing here would silently weaken the derived budget.
func unitTimeSpan(t *testing.T, value string) time.Duration {
	t.Helper()
	require.NotEmpty(t, value, "the unit is missing a setting this derivation depends on")

	if secs, err := strconv.Atoi(value); err == nil {
		return time.Duration(secs) * time.Second
	}
	d, err := time.ParseDuration(value)
	require.NoErrorf(t, err,
		"cannot read systemd time span %q; extend this parser rather than guessing", value)
	return d
}

// unitCount parses a plain count from the unit.
func unitCount(t *testing.T, value string) int {
	t.Helper()
	n, err := strconv.Atoi(value)
	require.NoErrorf(t, err, "cannot read count %q from the unit", value)
	return n
}
