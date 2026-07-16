package domain

import (
	"time"

	"github.com/google/uuid"
)

// Station is a point of charge from the IRVE referential, the canonical
// dataset this project builds on top of.
type Station struct {
	ID             uuid.UUID
	IRVEIDStation  *string
	IRVEIDPDC      string
	OperatorName   string
	Amenageur      string
	Enseigne       string
	Name           string
	AddressStreet  string
	AddressPostal  string
	AddressCity    string
	AddressCountry string
	Lat            float64
	Lng            float64
	PowerKW        *float64
	ConnectorType  string
	AccessType     string
	Is24_7         bool
	Metadata       map[string]any
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// StationFilter narrows a bbox query for GET /stations.
type StationFilter struct {
	MinLng, MinLat, MaxLng, MaxLat float64
	Operator                       string
	HasTariffs                     *bool
	Source                         string
	Limit                          int
	Offset                         int
}

// Connector is an aggregated connector group exposed by the API
// (one station can expose several connector kinds/powers).
type Connector struct {
	Kind       string
	MaxPowerKW *float64
	Count      int
}

// StationSummary is the shape returned by GET /stations (list view).
type StationSummary struct {
	Station        Station
	Connectors     []Connector
	HasTariffs     bool
	TariffSources  []string
	PricingSummary PricingSummary
}

type PricingSummary struct {
	ACMinCentsPerKWh *float64
	DCMinCentsPerKWh *float64
}
