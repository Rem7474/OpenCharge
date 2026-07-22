package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"opencharge/internal/domain"
	"opencharge/internal/repository"
)

const DefaultIRVEURL = "https://hydra.s3.rbx.io.cloud.ovh.net/geojson/eb76d20a-8501-400e-b336-d85724de5435.geojson"

type geoJSONFeatureCollection struct {
	Features []geoJSONFeature `json:"features"`
}

type geoJSONFeature struct {
	Geometry   geoJSONGeometry `json:"geometry"`
	Properties map[string]any  `json:"properties"`
}

type geoJSONGeometry struct {
	Type        string    `json:"type"`
	Coordinates []float64 `json:"coordinates"`
}

type IRVEIngester struct {
	Stations *repository.StationRepository
	URL      string
	client   *http.Client
}

func NewIRVEIngester(stations *repository.StationRepository, url string) *IRVEIngester {
	if url == "" {
		url = DefaultIRVEURL
	}
	return &IRVEIngester{Stations: stations, URL: url, client: &http.Client{Timeout: 5 * time.Minute}}
}

const irveBulkChunkSize = 500

func (ing *IRVEIngester) Run(ctx context.Context) (int, error) {
	slog.Info("downloading", "source", "irve", "url", ing.URL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ing.URL, nil)
	if err != nil {
		return 0, err
	}
	resp, err := ing.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("download irve dataset: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return 0, fmt.Errorf("irve http %d: %s", resp.StatusCode, string(body))
	}

	var collection geoJSONFeatureCollection
	if err := json.NewDecoder(resp.Body).Decode(&collection); err != nil {
		return 0, fmt.Errorf("decode irve geojson: %w", err)
	}
	slog.Info("features downloaded", "source", "irve", "count", len(collection.Features))

	// Normalize all features first
	var stations []domain.Station
	for _, feature := range collection.Features {
		station, ok := normalizeIRVEFeature(feature)
		if !ok {
			continue
		}
		stations = append(stations, station)
	}

	// Bulk upsert in chunks
	count := 0
	for i := 0; i < len(stations); i += irveBulkChunkSize {
		end := i + irveBulkChunkSize
		if end > len(stations) {
			end = len(stations)
		}
		chunk := stations[i:end]
		if err := ing.Stations.BulkUpsertStations(ctx, chunk); err != nil {
			return count, fmt.Errorf("bulk upsert chunk %d-%d: %w", i, end, err)
		}
		count += len(chunk)
		slog.Info("upsert progress", "source", "irve", "upserted", count, "total", len(stations))
	}

	slog.Info("ingestion done", "source", "irve", "upserted", count)
	return count, nil
}

func normalizeIRVEFeature(f geoJSONFeature) (domain.Station, bool) {
	props := f.Properties
	get := func(key string) string {
		return strings.TrimSpace(stringValue(props[key]))
	}

	pdcID := firstNonEmpty(get("id_pdc_itinerance"), get("id_pdc_local"))
	if pdcID == "" {
		return domain.Station{}, false
	}

	lat, lng, ok := coordinatesFromGeometry(f.Geometry)
	if !ok {
		return domain.Station{}, false
	}

	stationID := firstNonEmpty(get("id_station_itinerance"), get("id_station_local"))

	power, _ := parseLooseFloat(get("puissance_nominale"))

	accessType := "unknown"
	if parseBooleanLoose(get("gratuit")) {
		accessType = "free"
	} else if parseBooleanLoose(get("paiement_acte")) || parseBooleanLoose(get("paiement_cb")) {
		accessType = "paid"
	}

	station := domain.Station{
		IRVEIDPDC:      pdcID,
		OperatorName:   get("nom_operateur"),
		Amenageur:      get("nom_amenageur"),
		Enseigne:       get("nom_enseigne"),
		Name:           firstNonEmpty(get("nom_station"), pdcID),
		AddressStreet:  get("adresse_station"),
		AddressPostal:  firstNonEmpty(get("consolidated_code_postal"), get("code_postal_station")),
		AddressCity:    firstNonEmpty(get("consolidated_commune"), get("code_insee_commune")),
		AddressCountry: "FR",
		Lat:            lat,
		Lng:            lng,
		PowerKW:        power,
		ConnectorType:  primaryConnectorType(props),
		AccessType:     accessType,
		Is24_7:         strings.EqualFold(get("horaires"), "24/7"),
		Metadata:       props,
	}
	if stationID != "" {
		station.IRVEIDStation = &stationID
	}
	return station, true
}

func coordinatesFromGeometry(g geoJSONGeometry) (lat, lng float64, ok bool) {
	if strings.ToLower(g.Type) != "point" || len(g.Coordinates) < 2 {
		return 0, 0, false
	}
	return g.Coordinates[1], g.Coordinates[0], true
}

func primaryConnectorType(props map[string]any) string {
	flags := []struct {
		key  string
		kind string
	}{
		{"prise_type_combo_ccs", domain.ConnectorTypeCCS},
		{"prise_type_chademo", domain.ConnectorTypeCHAdeMO},
		{"prise_type_2", domain.ConnectorTypeT2},
		{"prise_type_ef", domain.ConnectorTypeEF},
		{"prise_type_autre", domain.ConnectorTypeOther},
	}
	for _, flag := range flags {
		if parseBooleanLoose(strings.TrimSpace(stringValue(props[flag.key]))) {
			return flag.kind
		}
	}
	return domain.ConnectorTypeUnknown
}
