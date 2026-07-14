package importer

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"chargingbackend/internal/model"
)

type electraRawRecord struct {
	Source string         `json:"source"`
	Raw    map[string]any `json:"raw"`
}

func LoadElectraJSONL(path string) ([]model.Station, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	dec := json.NewDecoder(file)
	var stations []model.Station
	for {
		var rec electraRawRecord
		if err := dec.Decode(&rec); err != nil {
			if err == io.EOF {
				return stations, nil
			}
			return nil, fmt.Errorf("decode electra %s: %w", path, err)
		}
		station, err := normalizeElectra(rec.Raw)
		if err != nil {
			return nil, err
		}
		stations = append(stations, station)
	}
}

func normalizeElectra(raw map[string]any) (model.Station, error) {
	externalID := stringValue(raw["id"])
	if externalID == "" {
		externalID = stringValue(raw["uuid"])
	}
	if externalID == "" {
		return model.Station{}, fmt.Errorf("electra record without id")
	}

	lat, _ := floatValue(raw["latitude"])
	lng, _ := floatValue(raw["longitude"])
	name := stringValue(raw["name"])
	status := stringValue(raw["visibility"])
	if available, ok := raw["available"].(bool); ok {
		if available {
			status = "available"
		} else if status == "" {
			status = "unavailable"
		}
	}

	station := model.Station{
		ID:       "electra:" + externalID,
		Source:   "electra",
		Operator: "Electra",
		Name:     name,
		Status:   status,
		Address: model.Address{
			Street:      stringValue(raw["address"]),
			CountryCode: normalizeCountry(stringValue(raw["country_code"])),
		},
		Location:    model.Location{Lat: lat, Lng: lng},
		ParkingType: stringValue(raw["parking_type"]),
		Is24_7:      boolValue(raw["is_open_twentyfourseven"]),
		Raw:         raw,
		UpdatedAt:   nowUTC(),
	}

	pricingSummary, connectors := normalizeElectraPricing(raw["pricings"])
	station.Pricing = pricingSummary
	station.Connectors = connectors
	station.BestPriceCentsPerKwh = pricingSummary.BestPriceCentsPerKwh
	station.Currency = pricingSummary.Currency
	return station, nil
}

func normalizeElectraPricing(value any) (model.PricingSummary, []model.Connector) {
	pricingSummary := model.PricingSummary{Model: "electra"}
	var connectors []model.Connector
	var candidates []*float64

	mapValue, ok := value.(map[string]any)
	if !ok {
		return pricingSummary, connectors
	}

	for connectorKind, rawPricing := range mapValue {
		pricingMap, ok := rawPricing.(map[string]any)
		if !ok {
			continue
		}

		pricingSummary.Currency = stringValue(pricingMap["currency"])
		if pricingSummary.Currency == "" {
			pricingSummary.Currency = "EUR"
		}

		windowsValue, ok := pricingMap["windows"].([]any)
		if !ok {
			continue
		}

		connector := model.Connector{Kind: connectorKind, Standards: []string{connectorKind}}
		var windows []model.PricingWindow
		for _, item := range windowsValue {
			windowMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			price, _ := floatValue(windowMap["energy_price_cents_per_kwh"])
			if price != nil {
				candidates = append(candidates, price)
			}
			window := model.PricingWindow{
				StartTime:              stringValue(windowMap["start_time"]),
				EndTime:                stringValue(windowMap["end_time"]),
				EnergyPriceCentsPerKwh: price,
			}
			if fee, ok := floatValue(windowMap["session_duration_price_cents_per_min"]); ok {
				window.SessionDurationPriceCentsPerMin = fee
			}
			if fee, ok := floatValue(windowMap["congestion_price_cents_per_min"]); ok {
				window.CongestionPriceCentsPerMin = fee
			}
			windows = append(windows, window)
		}
		pricingSummary.Windows = append(pricingSummary.Windows, windows...)
		connectors = append(connectors, connector)
	}

	pricingSummary.BestPriceCentsPerKwh = minPrice(candidates)
	return pricingSummary, connectors
}
