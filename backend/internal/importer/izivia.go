package importer

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"chargingbackend/internal/model"
)

type iziviaNormalizedRecord struct {
	ID                    string            `json:"id"`
	Source                string            `json:"source"`
	Operator              string            `json:"operator"`
	Name                  string            `json:"name"`
	Status                string            `json:"status"`
	Address               model.Address     `json:"address"`
	Location              model.Location    `json:"location"`
	ParkingType           string            `json:"parkingType"`
	AccessibleForDisabled bool              `json:"accessibleForDisabled"`
	Is24_7                bool              `json:"is24_7"`
	Connectors            []model.Connector `json:"connectors"`
	Pricing               map[string]any    `json:"pricing"`
	BestPriceCentsPerKwh  *float64          `json:"bestPriceCentsPerKwh"`
	Currency              string            `json:"currency"`
	Raw                   map[string]any    `json:"raw"`
}

type iziviaAllDataRecord struct {
	Marker  map[string]any `json:"marker"`
	Station map[string]any `json:"station"`
	Pricing []any          `json:"pricing"`
	Errors  map[string]any `json:"errors"`
}

func LoadIziviaNormalizedJSONL(path string) ([]model.Station, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	dec := json.NewDecoder(file)
	var stations []model.Station
	for {
		var rec iziviaNormalizedRecord
		if err := dec.Decode(&rec); err != nil {
			if err == io.EOF {
				return stations, nil
			}
			return nil, fmt.Errorf("decode izivia normalized %s: %w", path, err)
		}
		station := model.Station{
			ID:                    rec.ID,
			Source:                rec.Source,
			Operator:              rec.Operator,
			Name:                  rec.Name,
			Status:                rec.Status,
			Address:               rec.Address,
			Location:              rec.Location,
			ParkingType:           rec.ParkingType,
			AccessibleForDisabled: rec.AccessibleForDisabled,
			Is24_7:                rec.Is24_7,
			Connectors:            rec.Connectors,
			BestPriceCentsPerKwh:  rec.BestPriceCentsPerKwh,
			Currency:              rec.Currency,
			Raw:                   map[string]any{"source_record": rec.Raw},
			UpdatedAt:             nowUTC(),
		}
		if station.ID == "" {
			station.ID = "izivia:" + strings.TrimPrefix(stringValue(rec.Raw["id"]), "izivia:")
		}
		station.Pricing = pricingFromIziviaAny(rec.Pricing)
		if station.BestPriceCentsPerKwh == nil {
			station.BestPriceCentsPerKwh = station.Pricing.BestPriceCentsPerKwh
		}
		if station.Currency == "" {
			station.Currency = station.Pricing.Currency
		}
		stations = append(stations, station)
	}
}

func LoadIziviaAllDataJSONL(path string) ([]model.Station, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	dec := json.NewDecoder(file)
	var stations []model.Station
	for {
		var rec iziviaAllDataRecord
		if err := dec.Decode(&rec); err != nil {
			if err == io.EOF {
				return stations, nil
			}
			return nil, fmt.Errorf("decode izivia all_data %s: %w", path, err)
		}
		station, err := normalizeIziviaAllData(rec)
		if err != nil {
			return nil, err
		}
		stations = append(stations, station)
	}
}

func normalizeIziviaAllData(rec iziviaAllDataRecord) (model.Station, error) {
	stationMap := rec.Station
	if stationMap == nil {
		return model.Station{}, fmt.Errorf("izivia record without station")
	}

	stationID := stringValue(stationMap["id"])
	if stationID == "" {
		return model.Station{}, fmt.Errorf("izivia station without id")
	}

	coords, _ := stationMap["coordinates"].([]any)
	var lat, lng *float64
	if len(coords) >= 2 {
		lng, _ = floatValue(coords[0])
		lat, _ = floatValue(coords[1])
	}

	addressMap, _ := stationMap["address"].(map[string]any)
	openingHours, _ := stationMap["openingHours"].(map[string]any)
	hours, _ := openingHours["hours"].(map[string]any)
	pricing := pricingFromIziviaAny(rec.Pricing)
	bestPrice := pricing.BestPriceCentsPerKwh
	if bestPrice == nil {
		bestPrice = parseIziviaBestPrice(rec.Pricing)
	}

	return model.Station{
		ID:       "izivia:" + stationID,
		Source:   "izivia",
		Operator: "Izivia",
		Name:     stringValue(stationMap["name"]),
		Status:   stringValue(stationMap["status"]),
		Address: model.Address{
			Street:      stringValue(addressMap["street"]),
			PostalCode:  stringValue(addressMap["postalCode"]),
			City:        stringValue(addressMap["city"]),
			CountryCode: normalizeCountry(stringValue(addressMap["country"])),
		},
		Location:              model.Location{Lat: lat, Lng: lng},
		ParkingType:           stringValue(stationMap["parkingType"]),
		AccessibleForDisabled: boolValue(stationMap["accessibleForDisabled"]),
		Is24_7:                boolValue(hours["twentyFourSeven"]),
		Connectors:            normalizeIziviaConnectors(stationMap),
		Pricing:               pricing,
		BestPriceCentsPerKwh:  bestPrice,
		Currency:              firstNonEmpty(pricing.Currency, "EUR"),
		Raw:                   map[string]any{"marker": rec.Marker, "station": rec.Station, "pricing": rec.Pricing, "errors": rec.Errors},
		UpdatedAt:             nowUTC(),
	}, nil
}

func normalizeIziviaConnectors(stationMap map[string]any) []model.Connector {
	stats, _ := stationMap["chargingConnectorsStats"].([]any)
	connectors := make([]model.Connector, 0, len(stats))
	for _, item := range stats {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		standard := stringValue(entry["standard"])
		maxPowerW, _ := floatValue(entry["maxPowerInW"])
		availableCount, _ := floatValue(entry["availableConnectorCount"])
		totalCount, _ := floatValue(entry["totalConnectorCount"])
		connector := model.Connector{
			Kind:       mapStandardToKind(standard),
			Standard:   standard,
			Standards:  []string{standard},
			MaxPowerKw: divideByThousand(maxPowerW),
		}
		if totalCount != nil {
			connector.Count = int(*totalCount)
		}
		if availableCount != nil {
			value := int(*availableCount)
			connector.AvailableCount = &value
		}
		connectors = append(connectors, connector)
	}
	return connectors
}

func pricingFromIziviaAny(value any) model.PricingSummary {
	summary := model.PricingSummary{Model: "izivia_text", Currency: "EUR"}
	if value == nil {
		return summary
	}

	var items []any
	switch typed := value.(type) {
	case []any:
		items = typed
	case map[string]any:
		items = []any{typed}
	default:
		return summary
	}

	var candidates []*float64
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		chargingStations, _ := entry["chargingStations"].([]any)
		for _, cs := range chargingStations {
			csMap, ok := cs.(map[string]any)
			if !ok {
				continue
			}
			texts := extractStringList(csMap["pricingInfos"])
			if len(texts) == 0 {
				texts = extractStringList(csMap["rawPricingInfos"])
			}
			if len(texts) == 0 {
				continue
			}
			price, fee := parsePriceText(texts[0])
			if price != nil {
				candidates = append(candidates, price)
			}
			if summary.RawText == "" {
				summary.RawText = texts[0]
			}
			if fee != nil {
				summary.ServiceFeePercent = fee
			}
		}
	}
	summary.BestPriceCentsPerKwh = minPrice(candidates)
	return summary
}

func parseIziviaBestPrice(value any) *float64 {
	return pricingFromIziviaAny(value).BestPriceCentsPerKwh
}

func extractStringList(value any) []string {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(list))
	for _, item := range list {
		text := stringValue(item)
		if text != "" {
			result = append(result, text)
		}
	}
	return result
}

func mapStandardToKind(standard string) string {
	standard = strings.ToLower(standard)
	if strings.Contains(standard, "combo") {
		return "dc"
	}
	return "ac"
}

func divideByThousand(value *float64) *float64 {
	if value == nil {
		return nil
	}
	result := *value / 1000.0
	return &result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
