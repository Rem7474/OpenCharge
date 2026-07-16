package ingestion

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var pricePattern = regexp.MustCompile(`([0-9]+(?:[.,][0-9]+)?)\s*€(?:/?kWh|/kWh)?`)
var serviceFeePattern = regexp.MustCompile(`([0-9]+(?:[.,][0-9]+)?)%\s+de frais de service`)

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

// parsePriceText extracts a €/kWh price and an optional service fee
// percentage from a free-text tariff description (e.g. Izivia's
// "0,45€/kWh (Dont 15% de frais de service)").
func parsePriceText(text string) (*float64, *float64) {
	if strings.TrimSpace(text) == "" {
		return nil, nil
	}
	var price *float64
	if match := pricePattern.FindStringSubmatch(text); len(match) == 2 {
		if parsed, err := strconv.ParseFloat(strings.ReplaceAll(match[1], ",", "."), 64); err == nil {
			price = &parsed
		}
	}
	var fee *float64
	if match := serviceFeePattern.FindStringSubmatch(text); len(match) == 2 {
		if parsed, err := strconv.ParseFloat(strings.ReplaceAll(match[1], ",", "."), 64); err == nil {
			fee = &parsed
		}
	}
	return price, fee
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// minPrice returns the smallest non-nil value, or nil if all are nil.
func minPrice(values []*float64) *float64 {
	var best *float64
	for _, v := range values {
		if v == nil {
			continue
		}
		if best == nil || *v < *best {
			best = v
		}
	}
	return best
}
