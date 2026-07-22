package domain

// Connector type strings — the canonical vocabulary set by IRVE ingestion
// (see ingestion.primaryConnectorType, driven by IRVE's own prise_type_*
// flags) and mirrored by any other source that can express connector-level
// granularity in its own data (today: only Freshmile, via
// ingestion.freshmileConnectorType).
const (
	ConnectorTypeCCS     = "CCS"
	ConnectorTypeCHAdeMO = "CHAdeMO"
	ConnectorTypeT2      = "T2"
	ConnectorTypeEF      = "EF"
	ConnectorTypeOther   = "other"
	ConnectorTypeUnknown = "unknown"
)

// dcConnectorTypes/acConnectorTypes back TariffKindForConnector — the
// single source of truth for AC/DC bucketing once something is already
// expressed in IRVE's own connector type vocabulary. Also duplicated
// independently in frontend/web/src/utils/pricing.js (its own JS sets)
// and ingestion/izivia.go (iziviaConnectorKind, which classifies directly
// from Izivia's raw "standard" strings — its own vocabulary, never
// translated to IRVE's — so it can't route through this function, but
// follows the same standard-first/power-fallback principle).
// ingestion/freshmile.go's freshmileTariffKind, by contrast, now maps its
// raw "standard" to an IRVE connector type first (freshmileConnectorType)
// and calls TariffKindForConnector on the result, falling back to a
// power-based heuristic only when that mapping is unclassifiable. It
// didn't always: freshmileTariffKind used to derive kind purely from a
// power threshold, never consulting the connector's own standard at all
// — a station-level "fast"/"superfast" best_power category silently
// forced every connector there to dc, AC sockets included.
var dcConnectorTypes = map[string]bool{ConnectorTypeCCS: true, ConnectorTypeCHAdeMO: true}
var acConnectorTypes = map[string]bool{ConnectorTypeT2: true, ConnectorTypeEF: true}

// TariffKindForConnector maps a connector type to the ac/dc TariffKind
// bucket it belongs to, or "" if unclassifiable (ConnectorTypeOther,
// ConnectorTypeUnknown, or any other value) — callers should fall back to
// another signal (e.g. a power threshold) in that case.
func TariffKindForConnector(connectorType string) string {
	if dcConnectorTypes[connectorType] {
		return TariffKindDC
	}
	if acConnectorTypes[connectorType] {
		return TariffKindAC
	}
	return ""
}
