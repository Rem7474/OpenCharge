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
	_ "time/tzdata" // embed the IANA database: the distroless runtime image ships no /usr/share/zoneinfo

	"github.com/jackc/pgx/v5/pgxpool"

	"opencharge/internal/domain"
	"opencharge/internal/repository"
)

const DefaultElectraURL = "https://stations.go-electra.com/stations.js"

// electraLocation is the timezone Electra's scraped HH:MM window
// boundaries are assumed to be in (a French network). The container this
// runs in has no system timezone data and defaults to UTC, so "now" must
// be explicitly converted here rather than relying on the host/container
// clock's zone — otherwise the "current window" price picked near a
// boundary would be off by 1-2h (CET/CEST).
var electraLocation = mustLoadElectraLocation()

func mustLoadElectraLocation() *time.Location {
	loc, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		// Should be unreachable with time/tzdata embedded; fall back to UTC
		// rather than panic; a stale mapping is better than a crashed run.
		log.Printf("electra: failed to load Europe/Paris timezone, falling back to UTC: %v", err)
		return time.UTC
	}
	return loc
}

// DefaultLinkMaxDistanceMeters is the default search radius used to
// correlate an external source station with the nearest IRVE station.
const DefaultLinkMaxDistanceMeters = 150.0

type ElectraIngester struct {
	Pool             *pgxpool.Pool
	SourceStations   *repository.SourceStationRepository
	Tariffs          *repository.TariffRepository
	Links            *repository.LinkRepository
	URL              string
	MaxLinkDistanceM float64
	client           *http.Client
}

func NewElectraIngester(pool *pgxpool.Pool, sourceStations *repository.SourceStationRepository, tariffs *repository.TariffRepository, links *repository.LinkRepository, url string) *ElectraIngester {
	if url == "" {
		url = DefaultElectraURL
	}
	return &ElectraIngester{
		Pool:             pool,
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
	runStart := time.Now()

	stations, err := ing.fetch(ctx)
	if err != nil {
		return 0, err
	}
	log.Printf("electra: %d stations downloaded", len(stations))

	linked := 0
	for i := 0; i < len(stations); i += ingestionBulkChunkSize {
		end := i + ingestionBulkChunkSize
		if end > len(stations) {
			end = len(stations)
		}

		var items []normalizedSourceStation
		for _, raw := range stations[i:end] {
			sourceStation, stationTariffs, ok := normalizeElectraStation(raw)
			if !ok {
				continue
			}
			items = append(items, normalizedSourceStation{Station: sourceStation, Tariffs: stationTariffs})
		}

		n, err := writeSourceStationChunk(ctx, ing.Pool, ing.SourceStations, ing.Tariffs, ing.Links, ing.MaxLinkDistanceM, items)
		linked += n
		if err != nil {
			return linked, err
		}
		log.Printf("electra: %d/%d processed", linked, len(stations))
	}
	log.Printf("electra: done, %d source stations processed", linked)

	// Only sweep after every station in this run was written successfully
	// (see repository.SweepStaleSourceData) — a run that returned early on error above
	// never reaches this point. linked > 0 additionally guards against a
	// download that "succeeded" but returned an empty/malformed station
	// list (e.g. Electra changed their JS payload shape) looking identical
	// to "Electra has zero stations" and wiping the entire known dataset —
	// see the same guard in izivia.go.
	if linked > 0 {
		if err := repository.SweepStaleSourceData(ctx, ing.Pool, "electra", runStart.Add(-repository.StaleSourceDataGracePeriod)); err != nil {
			return linked, err
		}
	}
	return linked, nil
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
	startTime, endTime         string
	priceCentsPerKWh           *float64
	sessionPriceCentsPerMin    *float64
	congestionPriceCentsPerMin *float64
}

func (w electraWindow) toExtra() map[string]any {
	m := map[string]any{"startTime": w.startTime, "endTime": w.endTime}
	if w.priceCentsPerKWh != nil {
		m["energyPriceCentsPerKwh"] = *w.priceCentsPerKWh
	}
	if w.sessionPriceCentsPerMin != nil {
		m["sessionPriceCentsPerMin"] = *w.sessionPriceCentsPerMin
	}
	if w.congestionPriceCentsPerMin != nil {
		m["congestionPriceCentsPerMin"] = *w.congestionPriceCentsPerMin
	}
	return m
}

// normalizeElectraTariffs turns Electra's per-connector pricing into three
// StationTariff rows per kind (ac/dc): "public" (flat, no app), "app" (the
// scraped price, which can vary by time window), and "subscription" (the
// app price minus the Electra Smart discount, on every window).
func normalizeElectraTariffs(value any) []domain.StationTariff {
	return normalizeElectraTariffsAt(value, time.Now().In(electraLocation))
}

func normalizeElectraTariffsAt(value any, now time.Time) []domain.StationTariff {
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

		// Each window keeps its own price/session/congestion figures — they
		// must not be collapsed into a single "last window wins" value,
		// since a station can have different pricing per time-of-day.
		var appWindows []electraWindow
		for _, item := range windowsValue {
			windowMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			price, _ := floatValue(windowMap["energy_price_cents_per_kwh"])
			session, _ := floatValue(windowMap["session_duration_price_cents_per_min"])
			congestion, _ := floatValue(windowMap["congestion_price_cents_per_min"])
			appWindows = append(appWindows, electraWindow{
				startTime:                  stringValue(windowMap["start_time"]),
				endTime:                    stringValue(windowMap["end_time"]),
				priceCentsPerKWh:           price,
				sessionPriceCentsPerMin:    session,
				congestionPriceCentsPerMin: congestion,
			})
		}

		// The public tier has no scraped price of its own (fixed constant),
		// but session/congestion fees are physical, time-based charges that
		// still apply without the app — borrow them from whichever app
		// window is active right now.
		publicPrice := electraPublicPriceCentsPerKWh
		publicWindow := electraWindow{startTime: "00:00", endTime: "23:59", priceCentsPerKWh: &publicPrice}
		if currentApp := currentWindow(appWindows, now); currentApp != nil {
			publicWindow.sessionPriceCentsPerMin = currentApp.sessionPriceCentsPerMin
			publicWindow.congestionPriceCentsPerMin = currentApp.congestionPriceCentsPerMin
		}
		publicWindows := []electraWindow{publicWindow}

		subscriptionWindows := make([]electraWindow, len(appWindows))
		for i, w := range appWindows {
			subscriptionWindows[i] = electraWindow{
				startTime:                  w.startTime,
				endTime:                    w.endTime,
				priceCentsPerKWh:           subtractCents(w.priceCentsPerKWh, electraSubscriptionDiscountCentsPerKWh),
				sessionPriceCentsPerMin:    w.sessionPriceCentsPerMin,
				congestionPriceCentsPerMin: w.congestionPriceCentsPerMin,
			}
		}

		base := domain.StationTariff{Source: "electra", Kind: kind, Model: "electra_fixed", Currency: currency}
		tariffs = append(tariffs,
			withPlan(base, "public", publicWindows, now),
			withPlan(base, "app", appWindows, now),
			withPlan(base, "subscription", subscriptionWindows, now),
		)
	}
	return tariffs
}

// withPlan attaches a plan's windows to a copy of base: the tariff's
// top-level EnergyPriceCentsPerKWh/SessionPriceCentsPerMin/
// CongestionPriceCentsPerMin reflect whichever window covers the current
// time of day (a single representative number for map/list display and
// sorting), while extra.windows keeps the full per-window breakdown for the
// hourly price chart.
func withPlan(base domain.StationTariff, plan string, windows []electraWindow, now time.Time) domain.StationTariff {
	t := base
	t.Plan = plan
	extraWindows := make([]map[string]any, len(windows))
	for i, w := range windows {
		extraWindows[i] = w.toExtra()
	}
	t.Extra = map[string]any{"windows": extraWindows}

	if current := currentWindow(windows, now); current != nil {
		t.EnergyPriceCentsPerKWh = current.priceCentsPerKWh
		t.SessionPriceCentsPerMin = current.sessionPriceCentsPerMin
		t.CongestionPriceCentsPerMin = current.congestionPriceCentsPerMin
	}
	return t
}

// currentWindow returns the window covering at's time of day (HH:MM,
// half-open [start, end)), or the first window if none matches (e.g.
// malformed/missing time strings) so a representative price is still
// available.
func currentWindow(windows []electraWindow, at time.Time) *electraWindow {
	if len(windows) == 0 {
		return nil
	}
	hm := at.Format("15:04")
	for i := range windows {
		if timeInWindow(hm, windows[i].startTime, windows[i].endTime) {
			return &windows[i]
		}
	}
	return &windows[0]
}

// timeInWindow reports whether hm ("HH:MM") falls in [start, end), handling
// windows that wrap past midnight (e.g. 22:00-06:00).
func timeInWindow(hm, start, end string) bool {
	if start == "" || end == "" {
		return false
	}
	if start <= end {
		return hm >= start && hm < end
	}
	return hm >= start || hm < end
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
