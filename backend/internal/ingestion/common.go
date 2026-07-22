package ingestion

import (
	"context"
	"fmt"
	"log"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DefaultLinkMaxDistanceMeters is the default search radius used to
// correlate an external source station with the nearest IRVE station —
// shared by every ingester that does this correlation (Electra, Izivia,
// Tesla, Freshmile, ChargeNow).
const DefaultLinkMaxDistanceMeters = 150.0

// defaultMaxRetries is how many times withRetries retries a transient
// failure (network error, timeout, or 5xx) before giving up on a single
// request — shared by every fan-out ingester (Izivia, Freshmile,
// ChargeNow, Tesla). A single upstream timeout used to permanently drop
// whatever that request covered (an entire grid square during discovery,
// or one station during detail fetch), logged and skipped, with no way to
// recover it within the run — see FailureLog/-retry-failed for what
// happens once a request exhausts this budget. 4xx responses are never
// retried — they won't succeed on a second try.
const defaultMaxRetries = 5

// withRetries retries do (one request attempt) on a transient failure —
// network error, timeout, or 5xx — up to maxRetries times with
// exponential backoff, starting at backoff and doubling each attempt.
// do's int return is the request's status code (0 if it never got a
// response at all, e.g. tesla's chromedp fetch which has no HTTP status
// of its own — treated the same as a network error, always retried); a
// non-zero status below 500 is a definitive client error and stops
// retrying immediately. label identifies this specific request in
// retry/failure log lines — for endpoints whose URL is the same constant
// across many different requests (e.g. Izivia's /map/markers, one per
// grid square, distinguished only by payload), callers should pass
// something more specific than the bare URL, or every retry/failure log
// line becomes indistinguishable from any other in-flight request.
func withRetries(ctx context.Context, sourceName, label string, maxRetries int, backoff time.Duration, do func() ([]byte, int, error)) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			wait := (1 << (attempt - 1)) * backoff
			log.Printf("%s: retrying %s in %v (attempt %d/%d) after: %v", sourceName, label, wait, attempt+1, maxRetries+1, lastErr)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, context.Cause(ctx)
			}
		}

		body, status, err := do()
		if err == nil {
			return body, nil
		}
		lastErr = err
		if status != 0 && status < 500 {
			break
		}
	}
	return nil, lastErr
}

// effectiveWorkers returns configured if positive, otherwise fallback.
// Every fan-out ingester's Config.Workers can legitimately be its zero
// value — a caller building a Config struct directly (most commonly a
// test) instead of via its DefaultXConfig constructor — so every place
// that needs an actual worker count applies the same fallback rather than
// silently spinning up zero workers and hanging forever.
func effectiveWorkers(configured, fallback int) int {
	if configured > 0 {
		return configured
	}
	return fallback
}

// frenchWSClass matches everyday ASCII whitespace (Go regexp's \s only
// matches [\t\n\f\r ]) plus two Unicode space variants French
// locale-aware number formatting actually produces between an amount and
// its unit/currency symbol: U+00A0 (no-break space) and U+202F (narrow
// no-break space — what JavaScript's Intl.NumberFormat('fr-FR',
// {style:'currency', currency:'EUR'}) inserts by default in modern
// browsers/Node, e.g. "0,20 €"). Visually indistinguishable from a
// plain space when printed/logged, so a price pattern written with bare
// \s can look correct in a test or a pasted log line while silently
// failing to match the real production text — see mustCompileFrenchWS.
const frenchWSClass = `[\s\x{00A0}\x{202F}]`

// mustCompileFrenchWS compiles pattern after substituting every bare \s
// with frenchWSClass, so a price-extraction pattern can be written
// normally (using \s, same as any other regex) while still matching the
// Unicode space variants described there. Every price/fee pattern below
// uses this instead of regexp.MustCompile directly.
func mustCompileFrenchWS(pattern string) *regexp.Regexp {
	return regexp.MustCompile(strings.ReplaceAll(pattern, `\s`, frenchWSClass))
}

// Izivia (and similar sources) describe pricing as free text rather than
// structured fields, e.g. "0,45€/kWh (Dont 15% de frais de service)",
// "0.30 € TTC du kWh", "Prix : 0,05€/min". These patterns are deliberately
// case-insensitive and tolerant of the wording/spacing variants seen across
// station descriptions, since the source gives no schema guarantee.
var pricePerKWhPattern = mustCompileFrenchWS(`(?i)([0-9]+(?:[.,][0-9]+)?)\s*(?:€|eur)\s*(?:ttc\s*)?(?:/\s*kwh|du\s+kwh|par\s+kwh|kwh)`)
var pricePerMinutePattern = mustCompileFrenchWS(`(?i)([0-9]+(?:[.,][0-9]+)?)\s*(?:€|eur)\s*(?:ttc\s*)?(?:/\s*min(?:ute)?s?|par\s+min(?:ute)?s?|la\s+minute)`)
var serviceFeePattern = mustCompileFrenchWS(`(?i)([0-9]+(?:[.,][0-9]+)?)\s*%\s*(?:de\s+)?frais\s+de\s+service|frais\s+de\s+service\s*(?:de\s*)?[:=]?\s*([0-9]+(?:[.,][0-9]+)?)\s*%`)

// sessionFeePattern matches a flat, one-time fee for starting a charging
// session (e.g. Izivia's "2,3€ la session de charge"), as distinct from
// pricePerMinutePattern's per-minute *rate*. "de charge" is optional and
// not matched — only "la session"/"par session"/"/session" is required
// right after the amount, same tight-adjacency approach as the other
// patterns here, so this doesn't accidentally match unrelated text.
var sessionFeePattern = mustCompileFrenchWS(`(?i)([0-9]+(?:[.,][0-9]+)?)\s*(?:€|eur)\s*(?:ttc\s*)?(?:la\s+session|par\s+session|/\s*session)`)

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	case fmt.Stringer:
		return typed.String()
	default:
		return fmt.Sprint(typed)
	}
}

func floatValue(value any) (*float64, bool) {
	switch typed := value.(type) {
	case float64:
		return &typed, true
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil, false
		}
		parsed, err := strconv.ParseFloat(strings.ReplaceAll(typed, ",", "."), 64)
		if err != nil {
			return nil, false
		}
		return &parsed, true
	default:
		return nil, false
	}
}

func parseLooseFloat(value string) (*float64, bool) {
	value = strings.TrimSpace(strings.ReplaceAll(value, ",", "."))
	if value == "" {
		return nil, false
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return nil, false
	}
	return &parsed, true
}

func parseBooleanLoose(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes", "oui", "vrai":
		return true
	default:
		return false
	}
}

// parsePriceText extracts an energy price (cents/kWh), a per-minute price
// (cents/min), a flat session fee (cents, e.g. Izivia's "2,3€ la session de
// charge") and an optional service fee percentage from a free-text tariff
// description (e.g. Izivia's "0,45€/kWh (Dont 15% de frais de service)" or
// "0,05€/min"). Prices in the source text are euros, but StationTariff
// fields are cents, so matches are scaled by 100.
func parsePriceText(text string) (energyCentsPerKWh, sessionCentsPerMin, sessionFeeCents, serviceFeePercent *float64) {
	if strings.TrimSpace(text) == "" {
		return nil, nil, nil, nil
	}
	energyCentsPerKWh = matchEuroCentsFirstNonZero(pricePerKWhPattern, text)
	sessionCentsPerMin = matchEuroCentsFirstNonZero(pricePerMinutePattern, text)
	sessionFeeCents = matchEuroCentsFirstNonZero(sessionFeePattern, text)
	if match := serviceFeePattern.FindStringSubmatch(text); match != nil {
		raw := firstNonEmpty(match[1], match[2])
		if parsed, err := strconv.ParseFloat(strings.ReplaceAll(raw, ",", "."), 64); err == nil {
			serviceFeePercent = &parsed
		}
	}
	return energyCentsPerKWh, sessionCentsPerMin, sessionFeeCents, serviceFeePercent
}

// matchEuroCentsFirstNonZero scans all of pattern's matches in text and
// converts the first one that isn't zero to cents. Some Izivia pricing
// text lists a bogus/placeholder "0.00 €/kWh" before the real price (e.g.
// "0.00 €/kWh ... 0,391€/kWh ..."), or "0,0€/min" for an idle-fee grace
// period before the real per-minute rate — taking the first match
// unconditionally would silently report a free tariff instead of skipping
// to the actual price. A text with only zero prices is treated the same
// as one with no price at all: nil, not 0.
func matchEuroCentsFirstNonZero(pattern *regexp.Regexp, text string) *float64 {
	for _, match := range pattern.FindAllStringSubmatch(text, -1) {
		if len(match) < 2 {
			continue
		}
		parsed, err := strconv.ParseFloat(strings.ReplaceAll(match[1], ",", "."), 64)
		if err != nil || parsed == 0 {
			continue
		}
		// Round to avoid float64 noise from the euro->cents multiplication
		// (e.g. 2.3 * 100 = 229.99999999999997).
		cents := math.Round(parsed*10000) / 100
		return &cents
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
