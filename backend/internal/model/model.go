package model

import "time"

type Address struct {
	Street      string `json:"street,omitempty"`
	PostalCode  string `json:"postalCode,omitempty"`
	City        string `json:"city,omitempty"`
	CountryCode string `json:"countryCode,omitempty"`
}

type Location struct {
	Lat *float64 `json:"lat,omitempty"`
	Lng *float64 `json:"lng,omitempty"`
}

type Connector struct {
	Kind           string   `json:"kind,omitempty"`
	Standard       string   `json:"standard,omitempty"`
	Standards      []string `json:"standards,omitempty"`
	MaxPowerKw     *float64 `json:"maxPowerKw,omitempty"`
	Count          int      `json:"count,omitempty"`
	AvailableCount *int     `json:"availableCount,omitempty"`
}

type PricingWindow struct {
	StartTime                       string   `json:"startTime,omitempty"`
	EndTime                         string   `json:"endTime,omitempty"`
	EnergyPriceCentsPerKwh          *float64 `json:"energyPriceCentsPerKwh,omitempty"`
	SessionDurationPriceCentsPerMin *float64 `json:"sessionDurationPriceCentsPerMin,omitempty"`
	CongestionPriceCentsPerMin      *float64 `json:"congestionPriceCentsPerMin,omitempty"`
}

type PricingSummary struct {
	Model                string          `json:"model,omitempty"`
	Currency             string          `json:"currency,omitempty"`
	BestPriceCentsPerKwh *float64        `json:"bestPriceCentsPerKwh,omitempty"`
	ServiceFeePercent    *float64        `json:"serviceFeePercent,omitempty"`
	RawText              string          `json:"rawText,omitempty"`
	Windows              []PricingWindow `json:"windows,omitempty"`
}

type Station struct {
	ID                    string         `json:"id"`
	Source                string         `json:"source"`
	Operator              string         `json:"operator"`
	Name                  string         `json:"name"`
	Status                string         `json:"status,omitempty"`
	Address               Address        `json:"address,omitempty"`
	Location              Location       `json:"location,omitempty"`
	ParkingType           string         `json:"parkingType,omitempty"`
	AccessibleForDisabled bool           `json:"accessibleForDisabled,omitempty"`
	Is24_7                bool           `json:"is24_7,omitempty"`
	Connectors            []Connector    `json:"connectors,omitempty"`
	Pricing               PricingSummary `json:"pricing,omitempty"`
	BestPriceCentsPerKwh  *float64       `json:"bestPriceCentsPerKwh,omitempty"`
	Currency              string         `json:"currency,omitempty"`
	Raw                   map[string]any `json:"raw,omitempty"`
	UpdatedAt             time.Time      `json:"updatedAt"`
}

type StationFilter struct {
	Source   string
	Operator string
	City     string
	MinPrice *float64
	Limit    int
	Offset   int
	Sort     string
}
