package ingestion

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Izivia (and similar sources) describe pricing as free text rather than
// structured fields, e.g. "0,45€/kWh (Dont 15% de frais de service)",
// "0.30 € TTC du kWh", "Prix : 0,05€/min". These patterns are deliberately
// case-insensitive and tolerant of the wording/spacing variants seen across
// station descriptions, since the source gives no schema guarantee.
var pricePerKWhPattern = regexp.MustCompile(`(?i)([0-9]+(?:[.,][0-9]+)?)\s*(?:€|eur)\s*(?:ttc\s*)?(?:/\s*kwh|du\s+kwh|par\s+kwh|kwh)`)
var pricePerMinutePattern = regexp.MustCompile(`(?i)([0-9]+(?:[.,][0-9]+)?)\s*(?:€|eur)\s*(?:ttc\s*)?(?:/\s*min(?:ute)?s?|par\s+min(?:ute)?s?|la\s+minute)`)
var serviceFeePattern = regexp.MustCompile(`(?i)([0-9]+(?:[.,][0-9]+)?)\s*%\s*(?:de\s+)?frais\s+de\s+service|frais\s+de\s+service\s*(?:de\s*)?[:=]?\s*([0-9]+(?:[.,][0-9]+)?)\s*%`)

// sessionFeePattern matches a flat, one-time fee for starting a charging
// session (e.g. Izivia's "2,3€ la session de charge"), as distinct from
// pricePerMinutePattern's per-minute *rate*. "de charge" is optional and
// not matched — only "la session"/"par session"/"/session" is required
// right after the amount, same tight-adjacency approach as the other
// patterns here, so this doesn't accidentally match unrelated text.
var sessionFeePattern = regexp.MustCompile(`(?i)([0-9]+(?:[.,][0-9]+)?)\s*(?:€|eur)\s*(?:ttc\s*)?(?:la\s+session|par\s+session|/\s*session)`)

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
