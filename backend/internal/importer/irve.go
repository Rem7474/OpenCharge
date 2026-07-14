package importer

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"chargingbackend/internal/model"
)

func LoadIRVECsv(path string) ([]model.Station, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.Comma = ','
	reader.FieldsPerRecord = -1

	headers, err := reader.Read()
	if err != nil {
		return nil, err
	}
	index := make(map[string]int, len(headers))
	for i, header := range headers {
		index[header] = i
	}

	var stations []model.Station
	for {
		row, err := reader.Read()
		if err != nil {
			if err == io.EOF {
				return stations, nil
			}
			return nil, fmt.Errorf("read irve %s: %w", filepath.Base(path), err)
		}
		station, err := normalizeIRVERow(index, row)
		if err != nil {
			return nil, err
		}
		stations = append(stations, station)
	}
}

func normalizeIRVERow(index map[string]int, row []string) (model.Station, error) {
	get := func(name string) string {
		idx, ok := index[name]
		if !ok || idx < 0 || idx >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[idx])
	}

	stationID := firstNonEmpty(get("id_station_local"), get("id_station_itinerance"))
	if stationID == "" {
		return model.Station{}, fmt.Errorf("irve row without station id")
	}

	operator := firstNonEmpty(get("nom_operateur"), get("nom_enseigne"), get("nom_amenageur"), "IRVE")
	name := firstNonEmpty(get("nom_station"), stationID)
	lat, lng := parseIRVELocation(get("consolidated_longitude"), get("consolidated_latitude"), get("coordonneesXY"))

	connectors := normalizeIRVEConnectors(get("nbre_pdc"), get("puissance_nominale"), map[string]string{
		"prise_type_ef":        get("prise_type_ef"),
		"prise_type_2":         get("prise_type_2"),
		"prise_type_combo_ccs": get("prise_type_combo_ccs"),
		"prise_type_chademo":   get("prise_type_chademo"),
		"prise_type_autre":     get("prise_type_autre"),
	})

	bestPrice, currency := parseIRVEPrice(get("tarification"), get("observations"))

	return model.Station{
		ID:       "irve:" + stationID,
		Source:   "irve",
		Operator: operator,
		Name:     name,
		Status:   "unknown",
		Address: model.Address{
			Street:      get("adresse_station"),
			PostalCode:  get("consolidated_code_postal"),
			City:        firstNonEmpty(get("consolidated_commune"), get("code_insee_commune")),
			CountryCode: "FR",
		},
		Location:   model.Location{Lat: lat, Lng: lng},
		Is24_7:     strings.EqualFold(get("horaires"), "24/7"),
		Connectors: connectors,
		Pricing: model.PricingSummary{
			Model:                "irve_text",
			Currency:             currency,
			BestPriceCentsPerKwh: bestPrice,
			RawText:              firstNonEmpty(get("tarification"), get("observations")),
		},
		BestPriceCentsPerKwh: bestPrice,
		Currency:             currency,
		Raw:                  rowToMap(index, row),
		UpdatedAt:            nowUTC(),
	}, nil
}

func rowToMap(index map[string]int, row []string) map[string]any {
	result := make(map[string]any, len(index))
	for name, idx := range index {
		if idx >= 0 && idx < len(row) {
			result[name] = row[idx]
		}
	}
	return result
}

func parseIRVELocation(lngText, latText, xyText string) (*float64, *float64) {
	if lng, ok := parseLooseFloat(lngText); ok {
		if lat, ok := parseLooseFloat(latText); ok {
			return lat, lng
		}
	}
	if strings.TrimSpace(xyText) == "" {
		return nil, nil
	}
	trimmed := strings.Trim(xyText, "[]")
	parts := strings.Split(trimmed, ",")
	if len(parts) < 2 {
		return nil, nil
	}
	lng, ok1 := parseLooseFloat(parts[0])
	lat, ok2 := parseLooseFloat(parts[1])
	if !ok1 || !ok2 {
		return nil, nil
	}
	return lat, lng
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

func normalizeIRVEConnectors(countText, powerText string, flags map[string]string) []model.Connector {
	count, _ := strconv.Atoi(strings.TrimSpace(countText))
	power, _ := parseLooseFloat(powerText)
	maxPowerKw := divideByThousand(power)

	var connectors []model.Connector
	for column, value := range flags {
		if !parseBooleanLoose(value) {
			continue
		}
		connectors = append(connectors, model.Connector{
			Standard:   column,
			Standards:  []string{column},
			Kind:       mapIRVEKind(column),
			Count:      count,
			MaxPowerKw: maxPowerKw,
		})
	}
	if len(connectors) == 0 && count > 0 {
		connectors = append(connectors, model.Connector{
			Standard:   "unknown",
			Kind:       "unknown",
			Count:      count,
			MaxPowerKw: maxPowerKw,
		})
	}
	return connectors
}

func mapIRVEKind(column string) string {
	switch column {
	case "prise_type_combo_ccs", "prise_type_chademo":
		return "dc"
	case "prise_type_ef", "prise_type_2":
		return "ac"
	default:
		return "unknown"
	}
}

func parseIRVEPrice(values ...string) (*float64, string) {
	for _, candidate := range values {
		if candidate == "" {
			continue
		}
		if price, _ := parsePriceText(candidate); price != nil {
			return price, "EUR"
		}
	}
	return nil, "EUR"
}

func parseBooleanLoose(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "1", "yes", "oui", "vrai":
		return true
	default:
		return false
	}
}
