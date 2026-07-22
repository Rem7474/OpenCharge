package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

// DefaultFreshmileLocationsBaseURL mirrors ingestion.DefaultFreshmileBaseURL
// + "/locations". Duplicated here rather than imported: this package's only
// relationship to Freshmile is proxying one GET, not sharing ingestion
// internals, and the two evolving independently (e.g. if ingestion's base
// URL ever needs a query param this proxy doesn't) is the safer default.
const DefaultFreshmileLocationsBaseURL = "https://prod-driver-api.freshmile.com/charge/api/v2/locations"

const freshmileProxyTimeout = 8 * time.Second

// FreshmileHandler proxies a single Freshmile endpoint server-side.
type FreshmileHandler struct {
	BaseURL string
	client  *http.Client
}

func NewFreshmileHandler() *FreshmileHandler {
	return &FreshmileHandler{
		BaseURL: DefaultFreshmileLocationsBaseURL,
		client:  &http.Client{Timeout: freshmileProxyTimeout},
	}
}

// GetAvailability handles GET /freshmile/availability/{locationId}: a
// same-origin proxy for Freshmile's own GET /locations/{id}. It exists
// because a direct browser -> Freshmile call is blocked by CORS in
// production (Freshmile's API sends no Access-Control-Allow-Origin
// header, confirmed against the real deployment) — see frontend
// components/FreshmileAvailability.jsx, which calls this endpoint instead
// of Freshmile directly now.
//
// Returns only the one field the frontend actually needs (isAvailable),
// not Freshmile's whole response, so this endpoint's contract doesn't
// shift every time Freshmile's own payload does.
func (h *FreshmileHandler) GetAvailability(w http.ResponseWriter, r *http.Request) {
	idParam := chi.URLParam(r, "locationId")
	locationID, err := strconv.Atoi(idParam)
	if err != nil || locationID <= 0 {
		writeError(w, http.StatusBadRequest, "locationId must be a positive integer")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), freshmileProxyTimeout)
	defer cancel()

	url := fmt.Sprintf("%s/%d", h.BaseURL, locationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to build upstream request")
		return
	}
	req.Header.Set("Accept", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		log.Printf("api: freshmile availability proxy: %v", err)
		writeError(w, http.StatusBadGateway, "failed to reach freshmile")
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotFound {
		writeError(w, http.StatusNotFound, "location not found")
		return
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		log.Printf("api: freshmile availability proxy: upstream http %d: %s", resp.StatusCode, body)
		writeError(w, http.StatusBadGateway, "freshmile returned an unexpected response")
		return
	}

	var envelope struct {
		Data struct {
			IsAvailable *bool `json:"is_available"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		log.Printf("api: freshmile availability proxy: decode upstream body: %v", err)
		writeError(w, http.StatusBadGateway, "freshmile returned an unexpected response")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"isAvailable": envelope.Data.IsAvailable})
}
