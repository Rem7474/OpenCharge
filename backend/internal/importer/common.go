package importer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var pricePattern = regexp.MustCompile(`([0-9]+(?:[.,][0-9]+)?)\s*€(?:/?kWh|/kWh)?`)
var serviceFeePattern = regexp.MustCompile(`([0-9]+(?:[.,][0-9]+)?)%\s+de frais de service`)

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case nil:
		return ""
	default:
		return fmt.Sprint(typed)
	}
}

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, _ := strconv.ParseBool(strings.TrimSpace(typed))
		return parsed
	case float64:
		return typed != 0
	default:
		return false
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

func minPrice(values []*float64) *float64 {
	var best *float64
	for _, value := range values {
		if value == nil {
			continue
		}
		if best == nil || *value < *best {
			candidate := *value
			best = &candidate
		}
	}
	return best
}

func normalizeCountry(code string) string {
	switch strings.TrimSpace(strings.ToUpper(code)) {
	case "FRA", "FRANCE":
		return "FR"
	default:
		return code
	}
}

func nowUTC() time.Time {
	return time.Now().UTC()
}

func readJSONLines(path string, decode func(map[string]any) error) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	for {
		var raw map[string]any
		if err := decoder.Decode(&raw); err != nil {
			if err == os.ErrClosed || strings.Contains(err.Error(), "EOF") {
				return nil
			}
			return fmt.Errorf("decode %s: %w", filepath.Base(path), err)
		}
		if err := decode(raw); err != nil {
			return err
		}
	}
}
