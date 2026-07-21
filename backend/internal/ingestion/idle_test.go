package ingestion

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestIdleWatchdogFiresAfterTimeoutWithNoPing(t *testing.T) {
	ctx, cancel, _ := startIdleWatchdog(context.Background(), 20*time.Millisecond)
	defer cancel()

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("idle watchdog never fired")
	}

	cause := context.Cause(ctx)
	if cause == nil || !strings.Contains(cause.Error(), "no successful request") {
		t.Errorf("Cause = %v, want a message mentioning \"no successful request\"", cause)
	}
}

// TestIdleWatchdogPingResetsDeadline pins the core behavior the whole
// mechanism exists for: as long as Ping keeps arriving faster than
// idleTimeout, the context must never fire, even well past what a single
// idleTimeout window would allow — a long-but-healthy run (Freshmile
// processing tens of thousands of locations) must never be cut off just
// because it's been running a while.
func TestIdleWatchdogPingResetsDeadline(t *testing.T) {
	const idleTimeout = 30 * time.Millisecond
	ctx, cancel, watchdog := startIdleWatchdog(context.Background(), idleTimeout)
	defer cancel()

	deadline := time.Now().Add(idleTimeout * 8)
	for time.Now().Before(deadline) {
		watchdog.Ping()
		time.Sleep(idleTimeout / 3)
		if ctx.Err() != nil {
			t.Fatalf("ctx canceled despite regular pings: %v", context.Cause(ctx))
		}
	}
}

// TestIdleWatchdogFiresAfterPingsStop is the other half: once pings stop
// arriving, the watchdog must still fire idleTimeout after the *last*
// ping, not idleTimeout after startup.
func TestIdleWatchdogFiresAfterPingsStop(t *testing.T) {
	const idleTimeout = 30 * time.Millisecond
	ctx, cancel, watchdog := startIdleWatchdog(context.Background(), idleTimeout)
	defer cancel()

	// Keep the watchdog alive for longer than idleTimeout by pinging
	// regularly, then stop — if the timer were anchored to startup
	// instead of the last Ping, it would have already fired during this
	// loop.
	for i := 0; i < 5; i++ {
		watchdog.Ping()
		time.Sleep(idleTimeout / 2)
	}
	if ctx.Err() != nil {
		t.Fatalf("ctx canceled while still pinging: %v", context.Cause(ctx))
	}

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("idle watchdog never fired after pings stopped")
	}
}

func TestIdleWatchdogDisabledWhenTimeoutIsZero(t *testing.T) {
	parent := context.Background()
	ctx, cancel, watchdog := startIdleWatchdog(parent, 0)
	defer cancel()

	if ctx != parent {
		t.Error("startIdleWatchdog(parent, 0) should return parent unwrapped")
	}
	if watchdog != nil {
		t.Error("startIdleWatchdog(parent, 0) should return a nil watchdog")
	}
	// Ping on the nil watchdog a disabled caller would still hold a
	// reference to must not panic.
	watchdog.Ping()
}

func TestIdleWatchdogCancelStopsGoroutineImmediately(t *testing.T) {
	ctx, cancel, _ := startIdleWatchdog(context.Background(), time.Hour)
	cancel()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("ctx not done immediately after cancel()")
	}
}

// TestFreshmileRetryFailedGivesUpOnSustainedFailure exercises the idle
// watchdog through a real ingester end-to-end: every request fails, so no
// Ping ever fires, and the run must abort once idleTimeout elapses rather
// than retrying/backing off forever.
func TestFreshmileRetryFailedGivesUpOnSustainedFailure(t *testing.T) {
	ing := NewFreshmileIngester(nil, nil, nil, nil, "http://127.0.0.1:1", FreshmileConfig{Workers: 1})
	ing.retryBackoff = time.Millisecond
	ing.IdleTimeout = 30 * time.Millisecond
	path := t.TempDir() + "/freshmile.json"
	ing.Failures = NewFailureLog(path, "freshmile")

	failures := []FailedFetch{{
		Source: "freshmile",
		Kind:   failKindFreshmileLocation,
		Params: []byte(`{"id": 1}`),
	}}

	done := make(chan error, 1)
	go func() {
		_, err := ing.RetryFailed(context.Background(), failures)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "no successful request") {
			t.Errorf("RetryFailed error = %v, want a message about no successful request", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RetryFailed did not give up within the idle timeout")
	}
}
