package domain

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Station represents an IRVE charging point (canonical referential).
type Station struct {
	ID                 uuid.UUID       `json:"id"`
	IRVEIDStation      *string         `json:"irve_id_station,omitempty"`
	IRVEIDPDC          *string         `json:"irve_id_pdc,omitempty"`
	OperatorName       *string         `json:"operator_name,omitempty"`
	Amenageur          *string         `json:"amenageur,omitempty"`
	Enseigne           *string         `json:"enseigne,omitempty"`
	Name               *string         `json:"name,omitempty"`
	AddressStreet      *string         `json:"address_street,omitempty"`
	AddressPostalCode  *string         `json:"address_postal_code,omitempty"`
	AddressCity        *string         `json:"address_city,omitempty"`
	AddressCountryCode string          `json:"address_country_code"`
	Lat                float64         `json:"lat"`
	Lng                float64         `json:"lng"`
	PowerKw            *float64        `json:"power_kw,omitempty"`
	ConnectorType      *string         `json:"connector_type,omitempty"`
	AccessType         *string         `json:"access_type,omitempty"`
	Is24_7             *bool           `json:"is_24_7,omitempty"`
	Metadata           json.RawMessage `json:"metadata,omitempty"`
	CreatedAt          time.Time       `json:"created_at"`
	UpdatedAt          time.Time       `json:"updated_at"`
}

// StationListItem is the lightweight DTO returned by GET /stations (bbox query).
type StationListItem struct {
	ID             string          `json:"id"`
	Name           *string         `json:"name,omitempty"`
	Location       LatLng          `json:"location"`
	Operator       *string         `json:"operator,omitempty"`
	Address        Address         `json:"address"`
	Connectors     []ConnectorStat `json:"connectors"`
	HasTariffs     bool            `json:"hasTariffs"`
	TariffSources  []string        `json:"tariffSources"`
	PricingSummary *PricingSummary `json:"pricingSummary,omitempty"`
}

type LatLng struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

type Address struct {
	Street      *string `json:"street,omitempty"`
	City        *string `json:"city,omitempty"`
	PostalCode  *string `json:"postalCode,omitempty"`
	CountryCode string  `json:"countryCode"`
}

type ConnectorStat struct {
	Kind       string  `json:"kind"`
	MaxPowerKw float64 `json:"maxPowerKw"`
	Count      int     `json:"count"`
}

type PricingSummary struct {
	ACMinCentsPerKwh *float64 `json:"ac_min_cents_per_kwh,omitempty"`
	DCMinCentsPerKwh *float64 `json:"dc_min_cents_per_kwh,omitempty"`
}
