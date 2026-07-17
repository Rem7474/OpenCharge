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
// single source of truth for AC/DC bucketing by connector type. Previously
// duplicated independently in three places: frontend/web/src/utils/
// pricing.js (its own JS sets), ingestion/izivia.go (iziviaConnectorKind,
// its own vocabulary derived from Izivia's raw "standard" strings, not
// IRVE's), and ingestion/freshmile.go (which derived kind purely from a
// power threshold, not connector type at all). Izivia's and Freshmile's
// own mapping functions still exist (their raw data doesn't use IRVE's
// vocabulary directly), but for any code working in terms of IRVE-style
// connector type strings, this is the one place the AC/DC split lives.
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
