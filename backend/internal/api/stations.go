package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/Rem7474/opencharge/internal/domain"
	"github.com/Rem7474/opencharge/internal/repository"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

type StationsHandler struct {
	stationRepo *repository.StationRepository
	tariffRepo  *repository.TariffRepository
	linkRepo    *repository.LinkRepository
	logger      *zap.Logger
}

func NewStationsHandler(
	sr *repository.StationRepository,
	tr *repository.TariffRepository,
	lr *repository.LinkRepository,
	logger *zap.Logger,
) *StationsHandler {
	return &StationsHandler{stationRepo: sr, tariffRepo: tr, linkRepo: lr, logger: logger}
}

// RegisterRoutes attaches the station routes to a gin router group.
func (h *StationsHandler) RegisterRoutes(rg *gin.RouterGroup) {
	rg.GET("/stations", h.ListStations)
	rg.GET("/stations/:id", h.GetStation)
}

// ListStations handles GET /api/v1/stations?bbox=minLng,minLat,maxLng,maxLat
func (h *StationsHandler) ListStations(c *gin.Context) {
	bboxStr := c.Query("bbox")
	if bboxStr == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bbox query param is required (minLng,minLat,maxLng,maxLat)"})
		return
	}

	parts := strings.Split(bboxStr, ",")
	if len(parts) != 4 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bbox must have 4 comma-separated values"})
		return
	}

	coords := make([]float64, 4)
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid bbox coordinate: " + p})
			return
		}
		coords[i] = v
	}

	var operatorFilter *string
	if op := c.Query("operator"); op != "" {
		operatorFilter = &op
	}

	var hasTariffs *bool
	if ht := c.Query("hasTariffs"); ht != "" {
		v := ht == "true"
		hasTariffs = &v
	}

	limit := 500
	offset := 0
	if l := c.Query("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 1000 {
			limit = v
		}
	}
	if o := c.Query("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			offset = v
		}
	}

	items, err := h.stationRepo.FindByBbox(c.Request.Context(),
		coords[0], coords[1], coords[2], coords[3],
		operatorFilter, hasTariffs, limit, offset,
	)
	if err != nil {
		h.logger.Error("FindByBbox failed", zap.Error(err))
		c.JSON(http.StatusInternalServerError, gin.H{"error": "database error"})
		return
	}

	if items == nil {
		items = []domain.StationListItem{}
	}
	c.JSON(http.StatusOK, items)
}

// GetStation handles GET /api/v1/stations/:id  (id = "irve:<irve_id_pdc>" or UUID)
func (h *StationsHandler) GetStation(c *gin.Context) {
	idParam := c.Param("id")

	var station *domain.Station
	var err error

	if strings.HasPrefix(idParam, "irve:") {
		idPDC := strings.TrimPrefix(idParam, "irve:")
		station, err = h.stationRepo.GetByIRVEIDPDC(c.Request.Context(), idPDC)
	} else {
		parsedID, parseErr := uuid.Parse(idParam)
		if parseErr != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid station id"})
			return
		}
		station, err = h.stationRepo.GetByID(c.Request.Context(), parsedID)
	}

	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "station not found"})
		return
	}

	tariffs, err := h.tariffRepo.GetByStationID(c.Request.Context(), station.ID)
	if err != nil {
		h.logger.Error("GetByStationID tariffs failed", zap.Error(err))
		tariffs = nil
	}

	links, err := h.linkRepo.GetLinksByStationID(c.Request.Context(), station.ID)
	if err != nil {
		links = nil
	}

	c.JSON(http.StatusOK, gin.H{
		"station": gin.H{
			"id":       "irve:" + derefOrEmpty(station.IRVEIDPDc),
			"name":     station.Name,
			"operator": station.OperatorName,
			"address": gin.H{
				"street":      station.AddressStreet,
				"city":        station.AddressCity,
				"postalCode":  station.AddressPostalCode,
				"countryCode": station.AddressCountryCode,
			},
			"location": gin.H{"lat": station.Lat, "lng": station.Lng},
			"powerKw":       station.PowerKw,
			"connectorType": station.ConnectorType,
		},
		"tariffs": tariffs,
		"links":   links,
	})
}

func derefOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
