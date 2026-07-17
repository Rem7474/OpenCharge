package domain

import (
	"time"

	"github.com/google/uuid"
)

const (
	TariffKindAC    = "ac"
	TariffKindDC    = "dc"
	TariffKindMixed = "mixed"
)

// TariffPlanStandard is the default price tier for sources with a single
// pricing scheme (Izivia, IRVE text, ...). Sources with several tiers based
// on how the user pays (e.g. Electra's public/app/subscription) use their
// own plan ids instead.
const TariffPlanStandard = "standard"

// StationTariff is a normalized tariff attached to an IRVE station, coming
// from a given source (electra, izivia, irve_text, ...) and price plan
// (e.g. "standard", or "public"/"app"/"subscription" for Electra).
type StationTariff struct {
	ID                         uuid.UUID
	StationID                  uuid.UUID
	Source                     string
	Plan                       string
	Kind                       string
	Model                      string
	Currency                   string
	EnergyPriceCentsPerKWh     *float64
	SessionPriceCentsPerMin    *float64
	CongestionPriceCentsPerMin *float64
	ServiceFeePercent          *float64
	// SessionFeeCents is a flat, one-time fee for starting a charging
	// session, independent of energy or duration (e.g. Izivia's "2,3€ la
	// session de charge"). Not to be confused with SessionPriceCentsPerMin,
	// which despite the similar name is a per-minute rate.
	SessionFeeCents *float64
	ValidFrom       *time.Time
	ValidTo         *time.Time
	RawText         string
	Extra           map[string]any
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// SourcePlans lists the price plans available for a tariff source, e.g.
// {Source: "electra", Plans: ["app", "public", "subscription"]}.
type SourcePlans struct {
	Source string
	Plans  []string
}
