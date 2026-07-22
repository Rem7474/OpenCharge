package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"opencharge/internal/domain"
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

// freshmileConnectorTypeForAvailability maps a Freshmile connector
// "standard" to the same connector-type vocabulary used elsewhere
// (domain.ConnectorType*) — a copy of ingestion/freshmile.go's own
// (unexported) freshmileConnectorType, kept in sync by hand: this package
// doesn't otherwise depend on ingestion (a batch-job package) and pulling
// it in for one small string-mapping function isn't worth that coupling —
// the same "kept in sync by hand" tradeoff the frontend already makes
// against the same Go source (see utils/pricing.js's DC_CONNECTOR_TYPES).
func freshmileConnectorTypeForAvailability(standard string) string {
	switch strings.ToUpper(standard) {
	case "IEC_62196_T2_COMBO":
		return domain.ConnectorTypeCCS
	case "CHADEMO":
		return domain.ConnectorTypeCHAdeMO
	case "IEC_62196_T2":
		return domain.ConnectorTypeT2
	case "DOMESTIC_E", "DOMESTIC_F":
		return domain.ConnectorTypeEF
	default:
		return standard
	}
}

// connectorAvailabilityDTO is how many of a given connector type's EVSEs
// are currently available vs. how many exist at this site in total.
type connectorAvailabilityDTO struct {
	Available int `json:"available"`
	Total     int `json:"total"`
}

// GetAvailability handles GET /freshmile/availability/{locationId}: a
// same-origin proxy for Freshmile's own GET /locations/{id}. It exists
// because a direct browser -> Freshmile call is blocked by CORS in
// production (Freshmile's API sends no Access-Control-Allow-Origin
// header, confirmed against the real deployment) — see frontend
// components/FreshmileAvailability.jsx, which calls this endpoint instead
// of Freshmile directly now.
//
// A single Freshmile "location" (site) is a real site.evses_available_count
// out of site.evses_total_count — one physical charge point (evse) can
// expose several connectors (e.g. a Type 2 and a domestic socket sharing
// the same plug/power, as in real production data), all sharing that one
// evse's own is_available flag, so availability is only meaningful per
// evse, not finer-grained than that. connectorAvailability breaks that
// same count down per connector type (matching the frontend's own
// per-connector-kind display — see StationDetails.jsx's
// ConnectorPriceSection, one per IRVE-correlated connector kind) by
// counting, for each connector type ever seen at this site, how many of
// its evses are currently available.
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
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		log.Printf("api: freshmile availability proxy: decode upstream body: %v", err)
		writeError(w, http.StatusBadGateway, "freshmile returned an unexpected response")
		return
	}

	evsesAvailable, evsesTotal, byConnectorType := countFreshmileEvseAvailability(envelope.Data)

	writeJSON(w, http.StatusOK, map[string]any{
		"evsesAvailableCount":   evsesAvailable,
		"evsesTotalCount":       evsesTotal,
		"connectorAvailability": byConnectorType,
	})
}

// countFreshmileEvseAvailability walks a /locations/{id} response's evses,
// each with its own is_available flag and one or more connectors, into an
// overall available/total evse count plus the same breakdown per connector
// type. A connector type is counted at most once per evse (an evse with
// two connectors of the same type — not observed in practice, but not
// impossible — would otherwise double-count that one evse's availability).
func countFreshmileEvseAvailability(details map[string]any) (available, total int, byType map[string]connectorAvailabilityDTO) {
	byType = map[string]connectorAvailabilityDTO{}
	evses, _ := details["evses"].([]any)
	for _, rawEvse := range evses {
		evse, ok := rawEvse.(map[string]any)
		if !ok {
			continue
		}
		isAvailable, _ := evse["is_available"].(bool)
		total++
		if isAvailable {
			available++
		}

		seenTypes := map[string]bool{}
		connectors, _ := evse["connectors"].([]any)
		for _, rawConn := range connectors {
			conn, ok := rawConn.(map[string]any)
			if !ok {
				continue
			}
			standard, _ := conn["standard"].(string)
			connectorType := freshmileConnectorTypeForAvailability(standard)
			if seenTypes[connectorType] {
				continue
			}
			seenTypes[connectorType] = true

			entry := byType[connectorType]
			entry.Total++
			if isAvailable {
				entry.Available++
			}
			byType[connectorType] = entry
		}
	}
	return available, total, byType
}
