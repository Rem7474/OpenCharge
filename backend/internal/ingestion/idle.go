package ingestion

import (
	"context"
	"fmt"
	"time"
)

// DefaultIdleTimeout is how long a fan-out ingester (izivia, tesla,
// freshmile, chargenow) tolerates going without a single successful
// request before giving up on the whole run — see idleWatchdog.
const DefaultIdleTimeout = 5 * time.Minute

// idleWatchdog cancels its context — with a descriptive cause, readable
// via context.Cause — once Ping hasn't been called for at least
// idleTimeout. It exists because a source's own per-request retry/backoff
// budget only bounds a single request: scanning/fetching all of
// metropolitan France can legitimately take far longer than any fixed
// wall-clock budget as long as it keeps making progress (Freshmile alone
// processes tens of thousands of locations in a single run), so a flat
// "-timeout" cutoff either kills a healthy run early or has to be set so
// generously it can't catch a source that's genuinely stopped responding.
// Tracking time-since-last-success instead lets a run go as long as it
// needs to while it's working, but gives up promptly once every request
// starts failing (e.g. a source's API going down mid-run).
type idleWatchdog struct {
	ping chan struct{}
}

// startIdleWatchdog derives a cancelable context from parent and starts
// the watchdog goroutine; the returned cancel must be deferred by the
// caller immediately so the goroutine is torn down as soon as the run
// ends, regardless of whether idleTimeout ever elapsed. A zero or
// negative idleTimeout disables the watchdog entirely (parent is
// returned unwrapped, and the returned *idleWatchdog is nil — Ping on a
// nil *idleWatchdog is a safe no-op) — used by callers/tests that don't
// want a background timer at all.
func startIdleWatchdog(parent context.Context, idleTimeout time.Duration) (context.Context, context.CancelFunc, *idleWatchdog) {
	if idleTimeout <= 0 {
		return parent, func() {}, nil
	}
	ctx, cancel := context.WithCancelCause(parent)
	w := &idleWatchdog{ping: make(chan struct{}, 1)}
	go w.run(ctx, cancel, idleTimeout)
	return ctx, func() { cancel(nil) }, w
}

func (w *idleWatchdog) run(ctx context.Context, cancel context.CancelCauseFunc, idleTimeout time.Duration) {
	timer := time.NewTimer(idleTimeout)
	defer timer.Stop()
	for {
		select {
		case <-w.ping:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(idleTimeout)
		case <-timer.C:
			cancel(fmt.Errorf("no successful request in the last %s, aborting run", idleTimeout))
			return
		case <-ctx.Done():
			// The caller's deferred cancel fired (Run/RetryFailed
			// returning) or a parent context (SIGINT, a real deadline)
			// was canceled first — either way, nothing left to watch.
			return
		}
	}
}

// Ping records a successful request, resetting the idle deadline. Safe to
// call on a nil *idleWatchdog: the watchdog may be disabled (idleTimeout
// <= 0), or the caller may never have wired one up at all (e.g. a unit
// test exercising a single request method directly, without going
// through Run/RetryFailed).
func (w *idleWatchdog) Ping() {
	if w == nil {
		return
	}
	select {
	case w.ping <- struct{}{}:
	default:
		// A ping is already buffered and hasn't been consumed by the
		// watchdog goroutine yet — one pending reset is as good as two,
		// so drop this one rather than block the caller.
	}
}
