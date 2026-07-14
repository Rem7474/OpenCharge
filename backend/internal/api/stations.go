package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Rem7474/opencharge/internal/domain"
	"github.com/Rem7474/opencharge/internal/repository"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type StationHandler struct {
	stationRepo *repository.StationRepository
	tariffRepo  *repository.TariffRepository
	linkRepo    *repository.LinkRepository
}

func NewStationHandler(
	stationRepo *repository.StationRepository,
	tariffRepo *repository.TariffRepository,
	linkRepo *repository.LinkRepository,
) *StationHandler {
	return &StationHandler{
		stationRepo: stationRepo,
		tariffRepo:  tariffRepo,
		linkRepo:    linkRepo,
	}
}

// ListStations : GET /stations?bbox=minLng,minLat,maxLng,maxLat&limit=500&offset=0
func (h *StationHandler) ListStations(c *gin.Context) {
	bboxStr := c.Query("bbox")
	if bboxStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bbox query param required (minLng,minLat,maxLng,maxLat)"})
		return
	}

	parts := strings.Split(bboxStr, ",")
	if len(parts) != 4 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bbox must have 4 values"})
		return
	}
	minLng, err1 := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	minLat, err2 := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	maxLng, err3 := strconv.ParseFloat(strings.TrimSpace(parts[2]), 64)
	maxLat, err4 := strconv.ParseFloat(strings.TrimSpace(parts[3]), 64)
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bbox values must be floats"})
		return
	}

	limit := 500
	offset := 0
	if l := c.Query("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 2000 {
			limit = v
		}
	}
	if o := c.Query("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = v
		}
	}

	stations, err := h.stationRepo.FindByBbox(c.Request.Context(), minLng, minLat, maxLng, maxLat, limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Filtre hasTariffs optionnel
	hasTariffsFilter := c.Query("hasTariffs")

	result := make([]gin.H, 0, len(stations))
	for _, s := range stations {
		summary, err := h.tariffRepo.SummaryByStationID(c.Request.Context(), s.ID)
		if err != nil {
			summary = &repository.PricingSummary{}
		}

		if hasTariffsFilter == "true" && !summary.HasTariffs {
			continue
		}

		result = append(result, stationListItem(s, summary))
	}

	c.JSON(http.StatusOK, result)
}

// GetStation : GET /stations/:id  (id = UUID ou "irve:<irve_id_pdc>")
func (h *StationHandler) GetStation(c *gin.Context) {
	idParam := c.Param("id")
	var s *domain.Station
	var err error

	if strings.HasPrefix(idParam, "irve:") {
		irvePDC := strings.TrimPrefix(idParam, "irve:")
		s, err = h.stationRepo.FindByIRVEPDC(c.Request.Context(), irvePDC)
	} else {
		parsedID, parseErr := uuid.Parse(idParam)
		if parseErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id format"})
			return
		}
		s, err = h.stationRepo.FindByID(c.Request.Context(), parsedID)
	}

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "station not found"})
		return
	}

	tariffs, err := h.tariffRepo.FindByStationID(c.Request.Context(), s.ID)
	if err != nil {
		tariffs = nil
	}

	links, _ := h.linkRepo.FindByStationID(c.Request.Context(), s.ID)

	c.JSON(http.StatusOK, gin.H{
		"station": stationDetail(s),
		"tariffs": tariffs,
		"links":   links,
	})
}

// ---- helpers de sérialisation ----

func stationListItem(s *domain.Station, summary *repository.PricingSummary) gin.H {
	return gin.H{
		"id":       "irve:" + s.IRVEIDPDCc,
		"name":     s.Name,
		"operator": s.OperatorName,
		"enseigne": s.Enseigne,
		"location": gin.H{"lat": s.Lat, "lng": s.Lng},
		"address": gin.H{
			"street":      s.AddressStreet,
			"postalCode":  s.AddressPostalCode,
			"city":        s.AddressCity,
			"countryCode": s.AddressCountryCode,
		},
		"powerKw":       s.PowerKw,
		"connectorType": s.ConnectorType,
		"hasTariffs":    summary.HasTariffs,
		"tariffSources": summary.TariffSources,
		"pricingSummary": gin.H{
			"ac_min_cents_per_kwh": summary.ACMinCentsPerKwh,
			"dc_min_cents_per_kwh": summary.DCMinCentsPerKwh,
		},
	}
}

func stationDetail(s *domain.Station) gin.H {
	return gin.H{
		"id":           "irve:" + s.IRVEIDPDCc,
		"irve_id_pdc":  s.IRVEIDPDCc,
		"name":         s.Name,
		"operator":     s.OperatorName,
		"amenageur":    s.Amenageur,
		"enseigne":     s.Enseigne,
		"location":     gin.H{"lat": s.Lat, "lng": s.Lng},
		"address": gin.H{
			"street":      s.AddressStreet,
			"postalCode":  s.AddressPostalCode,
			"city":        s.AddressCity,
			"countryCode": s.AddressCountryCode,
		},
		"powerKw":       s.PowerKw,
		"connectorType": s.ConnectorType,
		"accessType":    s.AccessType,
		"is24_7":        s.Is247,
	}
}
