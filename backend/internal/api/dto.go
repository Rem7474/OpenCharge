package api

import (
	"strconv"
	"strings"
	"time"

	"opencharge/internal/domain"
)

type addressDTO struct {
	Street      string `json:"street,omitempty"`
	PostalCode  string `json:"postalCode,omitempty"`
	City        string `json:"city,omitempty"`
	CountryCode string `json:"countryCode,omitempty"`
}

type locationDTO struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

type connectorDTO struct {
	Kind       string   `json:"kind,omitempty"`
	MaxPowerKW *float64 `json:"maxPowerKw,omitempty"`
	Count      int      `json:"count,omitempty"`
}

type pricingSummaryDTO struct {
	ACMinCentsPerKWh *float64 `json:"ac_min_cents_per_kwh,omitempty"`
	DCMinCentsPerKWh *float64 `json:"dc_min_cents_per_kwh,omitempty"`
}

type stationListItemDTO struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	Location       locationDTO       `json:"location"`
	Operator       string            `json:"operator"`
	Enseigne       string            `json:"enseigne,omitempty"`
	Address        addressDTO        `json:"address"`
	Connectors     []connectorDTO    `json:"connectors"`
	HasTariffs     bool              `json:"hasTariffs"`
	TariffSources  []string          `json:"tariffSources"`
	PricingSummary pricingSummaryDTO `json:"pricingSummary"`
	// SelectedSourcesPricing is only present when the request's `source`
	// query param was non-empty: the cheapest price among just those
	// sources, so the map can price/gray markers against the operators
	// the user picked rather than the global cheapest.
	SelectedSourcesPricing *pricingSummaryDTO `json:"selectedSourcesPricing,omitempty"`
}

func toStationListItemDTO(s domain.StationSummary) stationListItemDTO {
	connectors := make([]connectorDTO, 0, len(s.Connectors))
	for _, c := range s.Connectors {
		connectors = append(connectors, connectorDTO{Kind: c.Kind, MaxPowerKW: c.MaxPowerKW, Count: c.Count})
	}
	sources := s.TariffSources
	if sources == nil {
		sources = []string{}
	}
	return stationListItemDTO{
		ID:       "irve:" + s.Station.IRVEIDPDC,
		Name:     s.Station.Name,
		Location: locationDTO{Lat: s.Station.Lat, Lng: s.Station.Lng},
		Operator: s.Station.OperatorName,
		Enseigne: s.Station.Enseigne,
		Address: addressDTO{
			Street:      s.Station.AddressStreet,
			PostalCode:  s.Station.AddressPostal,
			City:        s.Station.AddressCity,
			CountryCode: s.Station.AddressCountry,
		},
		Connectors:    connectors,
		HasTariffs:    s.HasTariffs,
		TariffSources: sources,
		PricingSummary: pricingSummaryDTO{
			ACMinCentsPerKWh: s.PricingSummary.ACMinCentsPerKWh,
			DCMinCentsPerKWh: s.PricingSummary.DCMinCentsPerKWh,
		},
		SelectedSourcesPricing: toPricingSummaryDTO(s.SelectedSourcesPricing),
	}
}

func toPricingSummaryDTO(p *domain.PricingSummary) *pricingSummaryDTO {
	if p == nil {
		return nil
	}
	return &pricingSummaryDTO{ACMinCentsPerKWh: p.ACMinCentsPerKWh, DCMinCentsPerKWh: p.DCMinCentsPerKWh}
}

type stationDetailDTO struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Operator   string         `json:"operator"`
	Enseigne   string         `json:"enseigne,omitempty"`
	Amenageur  string         `json:"amenageur,omitempty"`
	Address    addressDTO     `json:"address"`
	Location   locationDTO    `json:"location"`
	Connectors []connectorDTO `json:"connectors"`
	Is24_7     bool           `json:"is24_7"`
	AccessType string         `json:"accessType,omitempty"`
	// The four fields below come straight from the raw IRVE record kept in
	// Station.Metadata (the full GeoJSON "properties" object, stored as-is
	// at ingestion — see irve.go) rather than a dedicated column: they're
	// display-only, never filtered/queried on, so promoting them to typed
	// columns would just be a migration for no query benefit.
	PDCCount         *int   `json:"pdcCount,omitempty"`
	AccessibilityPMR string `json:"accessibilityPmr,omitempty"`
	CableT2Attached  *bool  `json:"cableT2Attached,omitempty"`
	// OpeningHours is IRVE's raw "horaires" text (e.g. "24/7", "Lun-Ven
	// 8h-20h") — Is24_7 above only captures the exact-"24/7" case, throwing
	// away every other schedule; this exposes the actual text instead.
	OpeningHours string `json:"openingHours,omitempty"`
}

// metadataString reads a string field from a Station's raw IRVE metadata
// (see stationDetailDTO's comment on why these read from metadata rather
// than a typed column).
func metadataString(metadata map[string]any, key string) string {
	v, ok := metadata[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(v)
}

// metadataBool loosely parses a metadata string field as a boolean — IRVE's
// raw values are Python-style "True"/"False" strings, not JSON booleans.
func metadataBool(metadata map[string]any, key string) bool {
	switch strings.ToLower(metadataString(metadata, key)) {
	case "true", "1", "yes", "oui", "vrai":
		return true
	default:
		return false
	}
}

// metadataInt parses a metadata string field as an int, returning nil if
// absent or unparseable rather than a misleading 0.
func metadataInt(metadata map[string]any, key string) *int {
	s := metadataString(metadata, key)
	if s == "" {
		return nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return nil
	}
	return &n
}

// metadataBoolPtr is like metadataBool but distinguishes "field absent"
// (nil) from "field present and false" — used for cableT2Attached, where
// an explicit "False" is meaningful information, not just unknown.
func metadataBoolPtr(metadata map[string]any, key string) *bool {
	if _, ok := metadata[key]; !ok {
		return nil
	}
	b := metadataBool(metadata, key)
	return &b
}

type sourcePlansDTO struct {
	ID    string   `json:"id"`
	Plans []string `json:"plans"`
}

type tariffDTO struct {
	Source                     string   `json:"source"`
	Plan                       string   `json:"plan"`
	Kind                       string   `json:"kind"`
	Model                      string   `json:"model"`
	Currency                   string   `json:"currency"`
	EnergyPriceCentsPerKWh     *float64 `json:"energy_price_cents_per_kwh,omitempty"`
	SessionPriceCentsPerMin    *float64 `json:"session_price_cents_per_min,omitempty"`
	CongestionPriceCentsPerMin *float64 `json:"congestion_price_cents_per_min,omitempty"`
	ServiceFeePercent          *float64 `json:"service_fee_percent,omitempty"`
	// SessionFeeCents is a flat, one-time fee for starting a session,
	// distinct from SessionPriceCentsPerMin (a per-minute rate).
	SessionFeeCents *float64 `json:"session_fee_cents,omitempty"`
	// SessionPriceGraceMinutes, when set, means SessionPriceCentsPerMin only
	// applies to charging minutes beyond this threshold (e.g. Izivia's
	// "après 1h de charge") — nil means the rate applies from minute 1.
	SessionPriceGraceMinutes *float64 `json:"session_price_grace_minutes,omitempty"`
	// ConnectorType is set only when the source differentiates price by
	// actual connector (today: only Freshmile) — empty means this tariff
	// applies to the whole Kind bucket regardless of connector.
	ConnectorType string         `json:"connector_type,omitempty"`
	RawText       string         `json:"raw_text,omitempty"`
	Extra         map[string]any `json:"extra,omitempty"`
	// UpdatedAt is when an ingestion run last wrote this tariff. Every run
	// rewrites it whether or not the price moved, so it reads as "last
	// checked" (how fresh the data is) rather than "price changed on".
	// A pointer so a tariff with no timestamp is omitted rather than
	// serialized as a meaningless year-0001 date.
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

type stationDetailResponse struct {
	Station stationDetailDTO `json:"station"`
	Tariffs []tariffDTO      `json:"tariffs"`
}

func toStationDetailDTO(s domain.Station) stationDetailDTO {
	connectors := []connectorDTO{}
	if s.ConnectorType != "" {
		connectors = append(connectors, connectorDTO{Kind: s.ConnectorType, MaxPowerKW: s.PowerKW, Count: 1})
	}
	return stationDetailDTO{
		ID:        "irve:" + s.IRVEIDPDC,
		Name:      s.Name,
		Operator:  s.OperatorName,
		Enseigne:  s.Enseigne,
		Amenageur: s.Amenageur,
		Address: addressDTO{
			Street:      s.AddressStreet,
			PostalCode:  s.AddressPostal,
			City:        s.AddressCity,
			CountryCode: s.AddressCountry,
		},
		Location:         locationDTO{Lat: s.Lat, Lng: s.Lng},
		Connectors:       connectors,
		Is24_7:           s.Is24_7,
		AccessType:       s.AccessType,
		PDCCount:         metadataInt(s.Metadata, "nbre_pdc"),
		AccessibilityPMR: metadataString(s.Metadata, "accessibilite_pmr"),
		CableT2Attached:  metadataBoolPtr(s.Metadata, "cable_t2_attache"),
		OpeningHours:     metadataString(s.Metadata, "horaires"),
	}
}

func toTariffDTO(t domain.StationTariff) tariffDTO {
	extra := t.Extra
	if extra == nil {
		extra = map[string]any{}
	}
	dto := tariffDTO{
		Source:                     t.Source,
		Plan:                       t.Plan,
		Kind:                       t.Kind,
		Model:                      t.Model,
		Currency:                   t.Currency,
		EnergyPriceCentsPerKWh:     t.EnergyPriceCentsPerKWh,
		SessionPriceCentsPerMin:    t.SessionPriceCentsPerMin,
		CongestionPriceCentsPerMin: t.CongestionPriceCentsPerMin,
		ServiceFeePercent:          t.ServiceFeePercent,
		SessionFeeCents:            t.SessionFeeCents,
		SessionPriceGraceMinutes:   t.SessionPriceGraceMinutes,
		ConnectorType:              t.ConnectorType,
		RawText:                    t.RawText,
		Extra:                      extra,
	}
	if !t.UpdatedAt.IsZero() {
		dto.UpdatedAt = &t.UpdatedAt
	}
	return dto
}
