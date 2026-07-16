package api

import (
	"encoding/json"
	"log"
	"net/http"
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

// ListStations handles GET /stations?bbox=minLng,minLat,maxLng,maxLat&operator=&hasTariffs=&source=&limit=&offset=
// It never loads the whole dataset: bbox is mandatory, and the map/frontend
// is expected to re-query on every viewport change.
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
		Source:   q.Get("source"),
	}
	if v := q.Get("hasTariffs"); v != "" {
		b, err := strconv.ParseBool(v)
		if err == nil {
			filter.HasTariffs = &b
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
		log.Printf("api: list stations: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to list stations")
		return
	}

	items := make([]stationListItemDTO, 0, len(summaries))
	for _, s := range summaries {
		items = append(items, toStationListItemDTO(s))
	}
	writeJSON(w, http.StatusOK, items)
}

// GetStation handles GET /stations/{id} where id is "irve:<id_pdc_itinerance>".
func (h *StationsHandler) GetStation(w http.ResponseWriter, r *http.Request) {
	rawID := chi.URLParam(r, "id")
	irveID := strings.TrimPrefix(rawID, "irve:")
	if irveID == "" {
		writeError(w, http.StatusBadRequest, "missing station id")
		return
	}

	station, err := h.Stations.GetByIRVEID(r.Context(), irveID)
	if err != nil {
		log.Printf("api: get station: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to get station")
		return
	}
	if station == nil {
		writeError(w, http.StatusNotFound, "station not found")
		return
	}

	tariffs, err := h.Tariffs.ListByStation(r.Context(), station.ID)
	if err != nil {
		log.Printf("api: list tariffs: %v", err)
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
