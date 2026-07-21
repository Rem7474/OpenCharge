package ingestion

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestParsePriceText(t *testing.T) {
	cases := []struct {
		name           string
		text           string
		wantPrice      *float64
		wantSession    *float64
		wantSessionFee *float64
		wantFee        *float64
	}{
		{"price and fee", "0,45€/kWh (Dont 15% de frais de service)", ptr(45.0), nil, nil, ptr(15.0)},
		{"price only, dot decimal", "0.30€/kWh", ptr(30.0), nil, nil, nil},
		{"price with TTC and spacing", "0,50 € TTC / kWh", ptr(50.0), nil, nil, nil},
		{"price with 'du kWh' wording", "Prix : 0,45€ du kWh", ptr(45.0), nil, nil, nil},
		{"per-minute price", "0,05€/min", nil, ptr(5.0), nil, nil},
		{"per-minute price, 'la minute' wording", "0,08 € la minute", nil, ptr(8.0), nil, nil},
		{"fee before price wording", "frais de service : 10%", nil, nil, nil, ptr(10.0)},
		{"empty", "", nil, nil, nil, nil},
		{
			"skips a leading zero price, takes the first non-zero one",
			"0.00 €/kWh\nFrais de service : 15%\n0,391€/kWh Une fois la charge terminée : 15 min à 0,0€/min puis 0,23€/min (Dont 15% de frais de service)",
			ptr(39.1), ptr(23.0), nil, ptr(15.0),
		},
		{"only a zero price present", "0.00 €/kWh", nil, nil, nil, nil},
		{"only a zero per-minute price present", "0,0€/min", nil, nil, nil, nil},
		// Both texts below are verbatim Izivia production strings. They pin
		// the euros-vs-cents scaling: a price left unscaled (0.663 in a
		// cents field) surfaces as "0.01 €/kWh" once the frontend divides by
		// 100 again, which is how the unit bug hid in plain sight.
		{
			"three-decimal price is scaled to cents, not left in euros",
			"0,663€/kWh \n (Dont 15% de frais de service)",
			ptr(66.3), nil, nil, ptr(15.0),
		},
		{
			"a per-session amount is read as a flat session fee, not a kWh or per-minute price",
			"0,4€ par session \n +0,663€/kWh \n (Dont 15% de frais de service)",
			ptr(66.3), nil, ptr(40.0), ptr(15.0),
		},
		{
			"real Izivia production string: flat session fee then €/kWh",
			"2,3€ la session de charge puis 0,51€/kWh (Dont 15% de frais de service)",
			ptr(51.0), nil, ptr(230.0), ptr(15.0),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			price, session, sessionFee, fee := parsePriceText(c.text)
			if !floatPtrEqual(price, c.wantPrice) {
				t.Errorf("price = %v, want %v", deref(price), deref(c.wantPrice))
			}
			if !floatPtrEqual(session, c.wantSession) {
				t.Errorf("session = %v, want %v", deref(session), deref(c.wantSession))
			}
			if !floatPtrEqual(sessionFee, c.wantSessionFee) {
				t.Errorf("sessionFee = %v, want %v", deref(sessionFee), deref(c.wantSessionFee))
			}
			if !floatPtrEqual(fee, c.wantFee) {
				t.Errorf("fee = %v, want %v", deref(fee), deref(c.wantFee))
			}
		})
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := firstNonEmpty("", "  ", "b", "c"); got != "b" {
		t.Errorf("firstNonEmpty = %q, want %q", got, "b")
	}
	if got := firstNonEmpty("", ""); got != "" {
		t.Errorf("firstNonEmpty = %q, want empty", got)
	}
}

func TestParseBooleanLoose(t *testing.T) {
	truthy := []string{"true", "1", "yes", "oui", "vrai", "  Oui  "}
	for _, v := range truthy {
		if !parseBooleanLoose(v) {
			t.Errorf("parseBooleanLoose(%q) = false, want true", v)
		}
	}
	falsy := []string{"false", "0", "non", "", "n/a"}
	for _, v := range falsy {
		if parseBooleanLoose(v) {
			t.Errorf("parseBooleanLoose(%q) = true, want false", v)
		}
	}
}

func ptr(v float64) *float64 { return &v }

func deref(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

func floatPtrEqual(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// TestWithRetriesRetriesTransientFailureThenSucceeds pins the shared
// retry/backoff helper every fan-out source (izivia, freshmile,
// chargenow, tesla) delegates to: a transient failure (status 0, e.g. a
// network error, or a 5xx) is retried up to maxRetries times before
// giving up.
func TestWithRetriesRetriesTransientFailureThenSucceeds(t *testing.T) {
	var attempts int32
	do := func() ([]byte, int, error) {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			return nil, 502, errors.New("bad gateway")
		}
		return []byte("ok"), 200, nil
	}

	body, err := withRetries(context.Background(), "test", "label", defaultMaxRetries, time.Millisecond, do)
	if err != nil {
		t.Fatalf("withRetries: %v", err)
	}
	if string(body) != "ok" {
		t.Errorf("body = %q, want ok", body)
	}
	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Errorf("attempts = %d, want 3 (2 failures + 1 success)", got)
	}
}

// TestWithRetriesDoesNotRetryOnDefinitiveClientError pins the 4xx/5xx
// split: a non-zero status below 500 is a permanent client error and
// must stop retrying immediately, unlike status 0 (no response at all —
// used by tesla's chromedp-based do, which has no HTTP status of its
// own) or a 5xx, both always retried.
func TestWithRetriesDoesNotRetryOnDefinitiveClientError(t *testing.T) {
	var attempts int32
	do := func() ([]byte, int, error) {
		atomic.AddInt32(&attempts, 1)
		return nil, 404, errors.New("not found")
	}

	_, err := withRetries(context.Background(), "test", "label", defaultMaxRetries, time.Millisecond, do)
	if err == nil {
		t.Fatal("withRetries = nil error, want the 404 surfaced")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("attempts = %d, want 1 (no retry on a definitive 4xx)", got)
	}
}

// TestWithRetriesGivesUpAfterMaxRetries pins the exhaustion case with a
// small maxRetries (mirroring tesla's teslaMaxRetries, smaller than the
// shared defaultMaxRetries since each attempt there is a real browser
// tab): maxRetries+1 total attempts, then the last error is returned.
func TestWithRetriesGivesUpAfterMaxRetries(t *testing.T) {
	var attempts int32
	do := func() ([]byte, int, error) {
		atomic.AddInt32(&attempts, 1)
		return nil, 0, errors.New("still stuck")
	}

	_, err := withRetries(context.Background(), "test", "label", teslaMaxRetries, time.Millisecond, do)
	if err == nil || err.Error() != "still stuck" {
		t.Errorf("withRetries error = %v, want the last attempt's error", err)
	}
	if got := atomic.LoadInt32(&attempts); got != teslaMaxRetries+1 {
		t.Errorf("attempts = %d, want %d (initial attempt + %d retries)", got, teslaMaxRetries+1, teslaMaxRetries)
	}
}

// TestWithRetriesStopsOnContextCancellation ensures a canceled ctx aborts
// the backoff sleep between attempts instead of waiting it out, and
// surfaces context.Cause (not a bare "context canceled" with no further
// info) — see idleWatchdog for why a richer cause matters here.
func TestWithRetriesStopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var attempts int32
	do := func() ([]byte, int, error) {
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			cancel()
		}
		return nil, 0, errors.New("network blip")
	}

	start := time.Now()
	_, err := withRetries(ctx, "test", "label", defaultMaxRetries, time.Hour, do)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("withRetries took %v, want it to abort immediately on ctx cancellation instead of waiting out the hour-long backoff", elapsed)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestEffectiveWorkers(t *testing.T) {
	cases := []struct {
		name       string
		configured int
		fallback   int
		want       int
	}{
		{"positive configured value wins", 10, 40, 10},
		{"zero falls back", 0, 40, 40},
		{"negative falls back", -1, 40, 40},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := effectiveWorkers(c.configured, c.fallback); got != c.want {
				t.Errorf("effectiveWorkers(%d, %d) = %d, want %d", c.configured, c.fallback, got, c.want)
			}
		})
	}
}
