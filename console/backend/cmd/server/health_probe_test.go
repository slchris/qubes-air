package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The probe writes to the database, and /health cannot require a token, so the
// throttle is what keeps a probe stream (a broken monitor, or anyone who can
// reach the port) from becoming a write stream that competes with job traffic
// for SQLite's single writer lock.
func TestHealthProbeThrottlesRepeatedProbes(t *testing.T) {
	now := time.Unix(1700000000, 0)
	var calls int
	p := newHealthProbe(func(context.Context) error { calls++; return nil }, 2*time.Second, func() time.Time { return now })

	require.NoError(t, p.check(context.Background()))
	require.Equal(t, 1, calls)

	// Inside the window: served from the last success, no second write.
	now = now.Add(500 * time.Millisecond)
	require.NoError(t, p.check(context.Background()))
	now = now.Add(time.Second)
	require.NoError(t, p.check(context.Background()))
	require.Equal(t, 1, calls, "probes inside the window must not touch the database")

	// Past the window: probed again, so the check cannot go permanently stale.
	now = now.Add(2 * time.Second)
	require.NoError(t, p.check(context.Background()))
	require.Equal(t, 2, calls)
}

// A failure must never be cached: a database that stops being writable has to go
// red on the next request, not after the window expires — and a cached failure
// would hide the recovery just as badly.
func TestHealthProbeNeverCachesFailure(t *testing.T) {
	now := time.Unix(1700000000, 0)
	diskFull := errors.New("database or disk is full")
	var calls int
	p := newHealthProbe(func(context.Context) error { calls++; return diskFull }, time.Minute, func() time.Time { return now })

	require.ErrorIs(t, p.check(context.Background()), diskFull)
	require.ErrorIs(t, p.check(context.Background()), diskFull)
	require.Equal(t, 2, calls, "a failed probe must be retried on the next request")

	// A success after a failure starts a fresh window; once that window is past,
	// the probe runs again and the new failure is reported — and is not cached.
	var fail atomic.Bool
	now = now.Add(time.Second)
	var flaky int
	q := newHealthProbe(func(context.Context) error {
		flaky++
		if fail.Load() {
			return diskFull
		}
		return nil
	}, time.Minute, func() time.Time { return now })

	require.NoError(t, q.check(context.Background()))
	require.Equal(t, 1, flaky)

	fail.Store(true)
	now = now.Add(2 * time.Minute) // past the window: a probe is due again
	require.ErrorIs(t, q.check(context.Background()), diskFull)
	require.Equal(t, 2, flaky)
	require.ErrorIs(t, q.check(context.Background()), diskFull)
	require.Equal(t, 3, flaky, "a failure must clear the cached success, not be hidden by it")
}

// A burst is one write, not one per request: the mutex is held across the probe.
func TestHealthProbeSerializesABurstIntoOneProbe(t *testing.T) {
	var calls int32
	release := make(chan struct{})
	p := newHealthProbe(func(context.Context) error {
		atomic.AddInt32(&calls, 1)
		<-release // hold the probe open so every goroutine is inside check()
		return nil
	}, time.Minute, time.Now)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			require.NoError(t, p.check(context.Background()))
		}()
	}
	// Let the first goroutine take the lock, then release it; the rest must
	// observe the cached success instead of probing again.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	require.Equal(t, int32(1), atomic.LoadInt32(&calls), "concurrent probes must collapse into one")
}
