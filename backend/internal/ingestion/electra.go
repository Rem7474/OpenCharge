package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"opencharge/internal/domain"
	"opencharge/internal/repository"
)

const DefaultElectraURL = "https://stations.go-electra.com/stations.js"

// DefaultLinkMaxDistanceMeters is the default search radius used to
// correlate an external source station with the nearest IRVE station.
const DefaultLinkMaxDistanceMeters = 150.0

type ElectraIngester struct {
	SourceStations   *repository.SourceStationRepository
	Tariffs          *repository.TariffRepository
	Links            *repository.LinkRepository
	URL              string
	MaxLinkDistanceM float64
	client           *http.Client
}

func NewElectraIngester(sourceStations *repository.SourceStationRepository, tariffs *repository.TariffRepository, links *repository.LinkRepository, url string) *ElectraIngester {
	if url == "" {
		url = DefaultElectraURL
	}
	return &ElectraIngester{
		SourceStations:   sourceStations,
		Tariffs:          tariffs,
		Links:            links,
		URL:              url,
		MaxLinkDistanceM: DefaultLinkMaxDistanceMeters,
		client:           &http.Client{Timeout: 60 * time.Second},
	}
}

// Run downloads Electra's station list, stores each as a SourceStation with
// normalized tariffs, then correlates it with the nearest IRVE station.
func (ing *ElectraIngester) Run(ctx context.Context) (int, error) {
	stations, err := ing.fetch(ctx)
	if err != nil {
		return 0, err
	}
	log.Printf("electra: %d stations downloaded", len(stations))

	linked := 0
	for _, raw := range stations {
		sourceStation, tariffs, ok := normalizeElectraStation(raw)
		if !ok {
			continue
		}
		sourceStationID, err := ing.SourceStations.Upsert(ctx, sourceStation)
		if err != nil {
			return linked, err
		}

		if err := ing.linkAndStoreTariffs(ctx, sourceStation, sourceStationID, tariffs); err != nil {
			return linked, err
		}
		linked++
	}
	log.Printf("electra: done, %d source stations processed", linked)
	return linked, nil
}

func (ing *ElectraIngester) linkAndStoreTariffs(ctx context.Context, src domain.SourceStation, sourceStationID uuid.UUID, tariffs []domain.StationTariff) error {
	candidate, err := ing.Links.FindNearestStation(ctx, src.Lat, src.Lng, ing.MaxLinkDistanceM)
	if err != nil {
		return err
	}
	if candidate == nil {
		return nil
	}

	quality := domain.LinkQualityByGeolocation
	if strings.EqualFold(candidate.OperatorName, src.OperatorName) {
		quality = domain.LinkQualityByOperatorName
	}
	distance := candidate.DistanceMeters
	if err := ing.Links.Upsert(ctx, candidate.StationID, sourceStationID, src.Source, quality, &distance); err != nil {
		return err
	}

	for _, t := range tariffs {
		t.StationID = candidate.StationID
		if err := ing.Tariffs.Upsert(ctx, t); err != nil {
			return err
		}
	}
	return nil
}

func (ing *ElectraIngester) fetch(ctx context.Context) ([]map[string]any, error) {
	log.Printf("electra: downloading %s", ing.URL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ing.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := ing.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download electra stations: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return nil, fmt.Errorf("electra http %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	text := strings.TrimSpace(string(body))
	text = strings.TrimPrefix(text, "export default")
	text = strings.TrimSpace(text)
	text = strings.TrimSuffix(text, ";")

	var stations []map[string]any
	if err := json.Unmarshal([]byte(text), &stations); err != nil {
		return nil, fmt.Errorf("parse electra payload: %w", err)
	}
	return stations, nil
}

func normalizeElectraStation(raw map[string]any) (domain.SourceStation, []domain.StationTariff, bool) {
	externalID := firstNonEmpty(stringValue(raw["id"]), stringValue(raw["uuid"]))
	if externalID == "" {
		return domain.SourceStation{}, nil, false
	}
	lat, latOK := floatValue(raw["latitude"])
	lng, lngOK := floatValue(raw["longitude"])
	if !latOK || !lngOK {
		return domain.SourceStation{}, nil, false
	}

	src := domain.SourceStation{
		Source:          "electra",
		SourceStationID: externalID,
		Name:            stringValue(raw["name"]),
		OperatorName:    "Electra",
		AddressStreet:   stringValue(raw["address"]),
		AddressCountry:  strings.ToUpper(stringValue(raw["country_code"])),
		Lat:             *lat,
		Lng:             *lng,
		Raw:             raw,
	}

	return src, normalizeElectraTariffs(raw["pricings"]), true
}

func normalizeElectraTariffs(value any) []domain.StationTariff {
	pricingMap, ok := value.(map[string]any)
	if !ok {
		return nil
	}

	var tariffs []domain.StationTariff
	for connectorKind, rawPricing := range pricingMap {
		kind := electraKind(connectorKind)
		pricing, ok := rawPricing.(map[string]any)
		if !ok {
			continue
		}
		currency := firstNonEmpty(stringValue(pricing["currency"]), "EUR")
		windowsValue, _ := pricing["windows"].([]any)

		var windows []map[string]any
		var energyPrice, sessionPrice, congestionPrice *float64
		for _, item := range windowsValue {
			windowMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			price, _ := floatValue(windowMap["energy_price_cents_per_kwh"])
			session, _ := floatValue(windowMap["session_duration_price_cents_per_min"])
			congestion, _ := floatValue(windowMap["congestion_price_cents_per_min"])
			if price != nil {
				energyPrice = price
			}
			if session != nil {
				sessionPrice = session
			}
			if congestion != nil {
				congestionPrice = congestion
			}
			windows = append(windows, map[string]any{
				"startTime": stringValue(windowMap["start_time"]),
				"endTime":   stringValue(windowMap["end_time"]),
			})
		}

		tariffs = append(tariffs, domain.StationTariff{
			Source:                     "electra",
			Kind:                       kind,
			Model:                      "electra_fixed",
			Currency:                   currency,
			EnergyPriceCentsPerKWh:     energyPrice,
			SessionPriceCentsPerMin:    sessionPrice,
			CongestionPriceCentsPerMin: congestionPrice,
			Extra:                      map[string]any{"windows": windows},
		})
	}
	return tariffs
}

func electraKind(connectorKind string) string {
	lower := strings.ToLower(connectorKind)
	if strings.Contains(lower, "dc") || strings.Contains(lower, "combo") {
		return domain.TariffKindDC
	}
	if strings.Contains(lower, "ac") {
		return domain.TariffKindAC
	}
	return domain.TariffKindMixed
}
