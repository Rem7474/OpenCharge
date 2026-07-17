package api

import (
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
}

type sourcePlansDTO struct {
	ID    string   `json:"id"`
	Plans []string `json:"plans"`
}

type tariffDTO struct {
	Source                     string         `json:"source"`
	Plan                       string         `json:"plan"`
	Kind                       string         `json:"kind"`
	Model                      string         `json:"model"`
	Currency                   string         `json:"currency"`
	EnergyPriceCentsPerKWh     *float64       `json:"energy_price_cents_per_kwh,omitempty"`
	SessionPriceCentsPerMin    *float64       `json:"session_price_cents_per_min,omitempty"`
	CongestionPriceCentsPerMin *float64       `json:"congestion_price_cents_per_min,omitempty"`
	ServiceFeePercent          *float64       `json:"service_fee_percent,omitempty"`
	RawText                    string         `json:"raw_text,omitempty"`
	Extra                      map[string]any `json:"extra,omitempty"`
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
		Location:   locationDTO{Lat: s.Lat, Lng: s.Lng},
		Connectors: connectors,
		Is24_7:     s.Is24_7,
		AccessType: s.AccessType,
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
		RawText:                    t.RawText,
		Extra:                      extra,
	}
	if !t.UpdatedAt.IsZero() {
		dto.UpdatedAt = &t.UpdatedAt
	}
	return dto
}
