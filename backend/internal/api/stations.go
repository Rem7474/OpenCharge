package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"opencharge/internal/domain"
	"opencharge/internal/repository"
)

type StationsHandler struct {
	Stations *repository.StationRepository
	Tariffs  *repository.TariffRepository
}

func NewStationsHandler(stations *repository.StationRepository, tariffs *repository.TariffRepository) *StationsHandler {
	return &StationsHandler{Stations: stations, Tariffs: tariffs}
}

// ListStations handles GET /stations?bbox=minLng,minLat,maxLng,maxLat&operator=&hasTariffs=&source=&connectorType=&minPowerKw=&minPriceCentsPerKwh=&maxPriceCentsPerKwh=&chargeKWh=&chargeMinutes=&excludeSubscriptionPlans=&limit=&offset=
// It never loads the whole dataset: bbox is mandatory, and the map/frontend
// is expected to re-query on every viewport change.
//
// source accepts a comma-separated list of "source:plan" pairs (e.g.
// "izivia:standard,electra:subscription"); a bare source name (no ":plan")
// defaults to the "standard" plan. It never filters stations out of the
// response: it only controls which (source, plan) pairs
// selectedSourcesPricing is computed from, so the map can gray out
// stations lacking a tariff from the selection instead of hiding them.
//
// excludeSubscriptionPlans=true drops any tariff on the
// domain.TariffPlanSubscription plan from both pricingSummary and
// selectedSourcesPricing, so the price shown for a station never assumes a
// paid subscription the caller may not have.
//
// chargeKWh/chargeMinutes, when given alongside min/maxPriceCentsPerKwh,
// switch the price-range filter to the estimated TOTAL cost (in cents) of a
// session delivering chargeKWh over chargeMinutes (energy + any per-minute
// rate + any flat session fee) instead of a plain €/kWh rate — see
// domain.StationFilter.MinPriceCentsPerKWh's doc comment.
func (h *StationsHandler) ListStations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	bboxParam := q.Get("bbox")
	if bboxParam == "" {
		writeError(w, http.StatusBadRequest, "bbox query parameter is required (minLng,minLat,maxLng,maxLat)")
		return
	}
	parts := strings.Split(bboxParam, ",")
	if len(parts) != 4 {
		writeError(w, http.StatusBadRequest, "bbox must have 4 comma-separated values: minLng,minLat,maxLng,maxLat")
		return
	}
	coords := make([]float64, 4)
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bbox contains an invalid number: "+p)
			return
		}
		coords[i] = v
	}

	filter := domain.StationFilter{
		MinLng:   coords[0],
		MinLat:   coords[1],
		MaxLng:   coords[2],
		MaxLat:   coords[3],
		Operator: q.Get("operator"),
		Sources:  parseSourcePlanPairs(q.Get("source")),
	}
	if v := q.Get("hasTariffs"); v != "" {
		b, err := strconv.ParseBool(v)
		if err == nil {
			filter.HasTariffs = &b
		}
	}
	if v := q.Get("connectorType"); v != "" {
		filter.ConnectorTypes = strings.Split(v, ",")
	}
	if v := q.Get("minPowerKw"); v != "" {
		if p, err := strconv.ParseFloat(v, 64); err == nil {
			filter.MinPowerKW = &p
		}
	}
	if v := q.Get("minPriceCentsPerKwh"); v != "" {
		if p, err := strconv.ParseFloat(v, 64); err == nil {
			filter.MinPriceCentsPerKWh = &p
		}
	}
	if v := q.Get("maxPriceCentsPerKwh"); v != "" {
		if p, err := strconv.ParseFloat(v, 64); err == nil {
			filter.MaxPriceCentsPerKWh = &p
		}
	}
	if v := q.Get("chargeKWh"); v != "" {
		if p, err := strconv.ParseFloat(v, 64); err == nil {
			filter.ChargeKWh = &p
		}
	}
	if v := q.Get("chargeMinutes"); v != "" {
		if p, err := strconv.ParseFloat(v, 64); err == nil {
			filter.ChargeMinutes = &p
		}
	}
	if v := q.Get("excludeSubscriptionPlans"); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			filter.ExcludeSubscriptionPlans = b
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Limit = n
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			filter.Offset = n
		}
	}

	summaries, err := h.Stations.ListByBBox(r.Context(), filter)
	if err != nil {
		slog.Error("list stations", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list stations")
		return
	}

	items := make([]stationListItemDTO, 0, len(summaries))
	for _, s := range summaries {
		items = append(items, toStationListItemDTO(s))
	}
	writeJSON(w, http.StatusOK, items)
}

// ListSources handles GET /sources: every tariff source currently ingested,
// each with its available price plans (e.g. "izivia" -> ["standard"],
// "electra" -> ["app", "public", "subscription"]), so the frontend can
// build its operator filter and plan selector dynamically instead of
// hardcoding anything.
func (h *StationsHandler) ListSources(w http.ResponseWriter, r *http.Request) {
	sources, err := h.Tariffs.ListDistinctSourcesWithPlans(r.Context())
	if err != nil {
		slog.Error("list sources", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to list sources")
		return
	}
	items := make([]sourcePlansDTO, 0, len(sources))
	for _, s := range sources {
		items = append(items, sourcePlansDTO{ID: s.Source, Plans: s.Plans})
	}
	writeJSON(w, http.StatusOK, items)
}

// GetStation handles GET /stations/{id} where id is "irve:<id_pdc_itinerance>".
func (h *StationsHandler) GetStation(w http.ResponseWriter, r *http.Request) {
	rawID := chi.URLParam(r, "id")
	// chi does not percent-decode route params, and clients correctly
	// encode the ":" in "irve:<id>" (e.g. via encodeURIComponent), so
	// decode before matching the "irve:" prefix.
	if decoded, err := url.PathUnescape(rawID); err == nil {
		rawID = decoded
	}
	irveID := strings.TrimPrefix(rawID, "irve:")
	if irveID == "" {
		writeError(w, http.StatusBadRequest, "missing station id")
		return
	}

	station, err := h.Stations.GetByIRVEID(r.Context(), irveID)
	if err != nil {
		slog.Error("get station", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to get station")
		return
	}
	if station == nil {
		writeError(w, http.StatusNotFound, "station not found")
		return
	}

	tariffs, err := h.Tariffs.ListByStation(r.Context(), station.ID)
	if err != nil {
		slog.Error("list tariffs", "error", err)
		writeError(w, http.StatusInternalServerError, "failed to load tariffs")
		return
	}

	tariffDTOs := make([]tariffDTO, 0, len(tariffs))
	for _, t := range tariffs {
		tariffDTOs = append(tariffDTOs, toTariffDTO(t))
	}

	writeJSON(w, http.StatusOK, stationDetailResponse{
		Station: toStationDetailDTO(*station),
		Tariffs: tariffDTOs,
	})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// parseSourcePlanPairs splits a comma-separated "source" query param (e.g.
// "izivia,electra:subscription") into normalized "source:plan" pairs,
// defaulting a bare source name to the standard plan. Dropping empty
// entries and whitespace. An empty input returns nil.
func parseSourcePlanPairs(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	pairs := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.Contains(p, ":") {
			p = p + ":" + domain.TariffPlanStandard
		}
		pairs = append(pairs, p)
	}
	return pairs
}
