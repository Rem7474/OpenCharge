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
//
// Sources selects which tariff sources SelectedSourcesPricing is computed
// from (e.g. the user picked "izivia" and "electra" as their reference
// networks). It never excludes stations from the result: a station with no
// tariff from any of Sources still comes back, just without a
// SelectedSourcesPricing price, so the map can show it grayed out instead
// of hiding it.
type StationFilter struct {
	MinLng, MinLat, MaxLng, MaxLat float64
	Operator                       string
	HasTariffs                     *bool
	Sources                        []string
	// ConnectorTypes, when non-empty, restricts results to stations whose
	// own connector_type is one of these (e.g. ["CCS", "CHAdeMO"]) — see
	// domain.ConnectorType* for the vocabulary.
	ConnectorTypes []string
	// MinPowerKW, when set, restricts results to stations with power_kw at
	// least this value. A station with power_kw unknown (NULL) never
	// matches — an unknown power shouldn't pass a "≥X kW" filter.
	MinPowerKW *float64
	// MinPriceCentsPerKWh/MaxPriceCentsPerKWh, when set, restrict results to
	// stations whose own "best price" (the same connector-kind-aware pick
	// utils/pricing.js#pickPriceCentsPerKWh does client-side, from
	// pricingSummary — the best price across ALL sources, not narrowed by
	// Sources) falls in [min, max]. A station with no known price never
	// matches either bound — an unknown price shouldn't pass a price-range
	// filter any more than it should pass a HasTariffs one.
	MinPriceCentsPerKWh *float64
	MaxPriceCentsPerKWh *float64
	// ExcludeSubscriptionPlans, when true, ignores tariffs on the
	// TariffPlanSubscription plan when computing PricingSummary and
	// SelectedSourcesPricing (and, transitively, the price-range filter
	// above) — a station whose only known price requires a paid
	// subscription then behaves as if it had no price at all.
	ExcludeSubscriptionPlans bool
	Limit                    int
	Offset                   int
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
	// SelectedSourcesPricing is nil unless StationFilter.Sources was
	// non-empty; it holds the cheapest price among just those sources.
	SelectedSourcesPricing *PricingSummary
}

// PricingSummary holds the cheapest AC/DC price for a station, either
// across every known source or restricted to a caller-selected subset.
type PricingSummary struct {
	ACMinCentsPerKWh *float64
	DCMinCentsPerKWh *float64
}
