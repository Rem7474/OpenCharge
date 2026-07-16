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

// electraPublicPriceCentsPerKWh is Electra's public tariff (no app), which
// is a flat rate published on their site rather than something exposed by
// the stations.js feed. It must be updated by hand if Electra changes it.
const electraPublicPriceCentsPerKWh = 64.0

// electraSubscriptionDiscountCentsPerKWh is the Electra Smart subscription
// discount applied on top of the "app" (scraped) price, on every window.
const electraSubscriptionDiscountCentsPerKWh = 20.0

type electraWindow struct {
	startTime, endTime string
	priceCentsPerKWh   *float64
}

func (w electraWindow) toExtra() map[string]any {
	m := map[string]any{"startTime": w.startTime, "endTime": w.endTime}
	if w.priceCentsPerKWh != nil {
		m["energyPriceCentsPerKwh"] = *w.priceCentsPerKWh
	}
	return m
}

// normalizeElectraTariffs turns Electra's per-connector pricing into three
// StationTariff rows per kind (ac/dc): "public" (flat, no app), "app" (the
// scraped price, which can vary by time window), and "subscription" (the
// app price minus the Electra Smart discount, on every window).
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

		var appWindows []electraWindow
		var sessionPrice, congestionPrice *float64
		for _, item := range windowsValue {
			windowMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			price, _ := floatValue(windowMap["energy_price_cents_per_kwh"])
			session, _ := floatValue(windowMap["session_duration_price_cents_per_min"])
			congestion, _ := floatValue(windowMap["congestion_price_cents_per_min"])
			if session != nil {
				sessionPrice = session
			}
			if congestion != nil {
				congestionPrice = congestion
			}
			appWindows = append(appWindows, electraWindow{
				startTime:        stringValue(windowMap["start_time"]),
				endTime:          stringValue(windowMap["end_time"]),
				priceCentsPerKWh: price,
			})
		}

		publicPrice := electraPublicPriceCentsPerKWh
		publicWindows := []electraWindow{{startTime: "00:00", endTime: "23:59", priceCentsPerKWh: &publicPrice}}

		subscriptionWindows := make([]electraWindow, len(appWindows))
		for i, w := range appWindows {
			subscriptionWindows[i] = electraWindow{startTime: w.startTime, endTime: w.endTime, priceCentsPerKWh: subtractCents(w.priceCentsPerKWh, electraSubscriptionDiscountCentsPerKWh)}
		}

		base := domain.StationTariff{
			Source: "electra", Kind: kind, Model: "electra_fixed", Currency: currency,
			SessionPriceCentsPerMin: sessionPrice, CongestionPriceCentsPerMin: congestionPrice,
		}
		tariffs = append(tariffs,
			withPlan(base, "public", publicWindows),
			withPlan(base, "app", appWindows),
			withPlan(base, "subscription", subscriptionWindows),
		)
	}
	return tariffs
}

// withPlan attaches a plan's windows to a copy of base: the tariff's
// top-level EnergyPriceCentsPerKWh becomes the cheapest window (a single
// representative number for map/list display), while extra.windows keeps
// the full per-window breakdown for the hourly price chart.
func withPlan(base domain.StationTariff, plan string, windows []electraWindow) domain.StationTariff {
	t := base
	t.Plan = plan
	extraWindows := make([]map[string]any, len(windows))
	var prices []*float64
	for i, w := range windows {
		extraWindows[i] = w.toExtra()
		prices = append(prices, w.priceCentsPerKWh)
	}
	t.EnergyPriceCentsPerKWh = minPrice(prices)
	t.Extra = map[string]any{"windows": extraWindows}
	return t
}

func subtractCents(price *float64, delta float64) *float64 {
	if price == nil {
		return nil
	}
	result := *price - delta
	return &result
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
