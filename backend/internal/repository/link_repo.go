package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type LinkRepository struct {
	db dbtx
}

func NewLinkRepository(pool *pgxpool.Pool) *LinkRepository {
	return &LinkRepository{db: pool}
}

// WithTx returns a LinkRepository whose statements run inside tx instead of
// picking a connection from the pool per call.
func (r *LinkRepository) WithTx(tx pgx.Tx) *LinkRepository {
	return &LinkRepository{db: tx}
}

// NearestStationCandidate is the closest IRVE station to a source station,
// used to decide how a StationLink should be created. ConnectorType/PowerKW
// carry the matched IRVE station's own connector data — needed by sources
// (e.g. ChargeNow) whose own discovery API doesn't return per-connector
// power/type, so IRVE's already-known value for the physical location is
// the only signal available to query that source's own pricing API with.
type NearestStationCandidate struct {
	StationID      uuid.UUID
	OperatorName   string
	Name           string
	ConnectorType  string
	PowerKW        *float64
	DistanceMeters float64
}

// metersPerDegreeLat approximates how many meters one degree of latitude
// covers — effectively constant everywhere, unlike longitude, which
// shrinks with cos(latitude).
const metersPerDegreeLat = 111320.0

// minCosLatFrance is a deliberately conservative (small) lower bound for
// cos(latitude) across metropolitan France's operating range (~41°N to
// ~51.5°N, where cos ranges from ~0.755 down to ~0.622) — used to size the
// longitude side of a bounding-box pre-filter so it's never too narrow.
// Underestimating it could silently exclude a true nearest match; erring
// generous only costs a slightly larger (but still index-bounded) scan,
// so that's the safe direction to round in.
const minCosLatFrance = 0.6

// bboxDeltas returns how many degrees of longitude/latitude a
// maxDistanceMeters radius needs as a generous (never-too-small)
// rectangular envelope around a point. It exists to give PostGIS's GiST
// index on stations.location a cheap way (the && overlap operator) to
// rule out "nothing anywhere nearby" up front, instead of relying solely
// on ORDER BY <-> LIMIT 1 combined with a WHERE ST_DWithin filter: for a
// point with no true match within range, that combination forces the KNN
// scan to walk the whole index in distance order before it can conclude
// there's no candidate — observed in production to take minutes per
// 200-point batch once a source's coverage includes points IRVE (France
// only) has nothing near, e.g. a source that also returns stations just
// across the border.
func bboxDeltas(maxDistanceMeters float64) (lngDelta, latDelta float64) {
	latDelta = maxDistanceMeters / metersPerDegreeLat
	lngDelta = maxDistanceMeters / (metersPerDegreeLat * minCosLatFrance)
	return lngDelta, latDelta
}

// FindNearestStation returns the closest IRVE station within maxDistanceMeters
// of the given source station location, using PostGIS ST_DWithin/KNN.
func (r *LinkRepository) FindNearestStation(ctx context.Context, lat, lng, maxDistanceMeters float64) (*NearestStationCandidate, error) {
	lngDelta, latDelta := bboxDeltas(maxDistanceMeters)
	const query = `
		SELECT id, operator_name, name,
			ST_Distance(location::geography, ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography) AS distance_m
		FROM stations
		WHERE location && ST_MakeEnvelope($1 - $4, $2 - $5, $1 + $4, $2 + $5, 4326)
			AND ST_DWithin(location::geography, ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography, $3)
		ORDER BY location <-> ST_SetSRID(ST_MakePoint($1, $2), 4326)
		LIMIT 1`

	var c NearestStationCandidate
	err := r.db.QueryRow(ctx, query, lng, lat, maxDistanceMeters, lngDelta, latDelta).Scan(&c.StationID, &c.OperatorName, &c.Name, &c.DistanceMeters)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("find nearest station: %w", err)
	}
	return &c, nil
}

// NearestStationQuery is one point to resolve in a FindNearestStations
// bulk call. TargetPowerKW is only consulted by FindNearestStationsForKind
// (FindNearestStations ignores it) — see that method's doc comment.
type NearestStationQuery struct {
	Lat, Lng      float64
	TargetPowerKW *float64
}

// FindNearestStations resolves the closest IRVE station within
// maxDistanceMeters for every point in a single round trip, instead of one
// FindNearestStation call (and one geospatial index scan) per point. The
// result is keyed by the point's index in points; a point with no
// candidate within range is simply absent from the map.
func (r *LinkRepository) FindNearestStations(ctx context.Context, points []NearestStationQuery, maxDistanceMeters float64) (map[int]NearestStationCandidate, error) {
	if len(points) == 0 {
		return nil, nil
	}

	lngs := make([]float64, len(points))
	lats := make([]float64, len(points))
	idxs := make([]int32, len(points))
	for i, p := range points {
		lngs[i] = p.Lng
		lats[i] = p.Lat
		idxs[i] = int32(i)
	}

	lngDelta, latDelta := bboxDeltas(maxDistanceMeters)

	// LEFT JOIN LATERAL (not CROSS JOIN) so a point with no candidate in
	// range still produces a row (with NULL station columns) instead of
	// disappearing from the result — otherwise "no neighbor" would be
	// indistinguishable from "never queried". The && bbox pre-filter (see
	// bboxDeltas) matters even more here than in FindNearestStation: this
	// runs the LATERAL subquery once per point in the batch, so a handful
	// of points with no true match nearby forcing a full-index KNN walk
	// each multiplies straight into total batch time.
	const query = `
		SELECT q.idx, s.id, s.operator_name, s.name, s.distance_m
		FROM unnest($1::float8[], $2::float8[], $3::int[]) AS q(lng, lat, idx)
		LEFT JOIN LATERAL (
			SELECT st.id, st.operator_name, st.name,
				ST_Distance(st.location::geography, ST_SetSRID(ST_MakePoint(q.lng, q.lat), 4326)::geography) AS distance_m
			FROM stations st
			WHERE st.location && ST_MakeEnvelope(q.lng - $5, q.lat - $6, q.lng + $5, q.lat + $6, 4326)
				AND ST_DWithin(st.location::geography, ST_SetSRID(ST_MakePoint(q.lng, q.lat), 4326)::geography, $4)
			ORDER BY st.location <-> ST_SetSRID(ST_MakePoint(q.lng, q.lat), 4326)
			LIMIT 1
		) s ON true`

	rows, err := r.db.Query(ctx, query, lngs, lats, idxs, maxDistanceMeters, lngDelta, latDelta)
	if err != nil {
		return nil, fmt.Errorf("find nearest stations (bulk): %w", err)
	}
	defer rows.Close()

	result := map[int]NearestStationCandidate{}
	for rows.Next() {
		var idx int32
		var id *uuid.UUID
		var operatorName, name *string
		var distance *float64
		if err := rows.Scan(&idx, &id, &operatorName, &name, &distance); err != nil {
			return nil, fmt.Errorf("scan nearest station (bulk): %w", err)
		}
		if id == nil {
			continue
		}
		c := NearestStationCandidate{StationID: *id, DistanceMeters: *distance}
		if operatorName != nil {
			c.OperatorName = *operatorName
		}
		if name != nil {
			c.Name = *name
		}
		result[int(idx)] = c
	}
	return result, rows.Err()
}

// candidateKindFilterFragment is the SQL predicate FindNearestStationsForKind
// uses to identify a "kind-compatible" IRVE candidate: connector types are
// classified DC (CCS/CHAdeMO) or AC (T2/EF), mirroring
// domain.TariffKindForConnector — kept in sync by hand since this lives in
// SQL, not Go, for the same reason bboxDeltas' index-friendly filtering
// does: it needs to run inside the query, not after fetching rows.
const candidateKindFilterFragment = `
	CASE
		WHEN st.connector_type IN ('CCS', 'CHAdeMO') THEN 'dc'
		WHEN st.connector_type IN ('T2', 'EF') THEN 'ac'
		ELSE ''
	END`

// FindNearestStationsForKind is FindNearestStations, but among the several
// nearest candidates within range it prefers one whose own connector type
// matches the given ac/dc kind — falling back to the plain nearest overall
// if no kind-compatible candidate exists within maxDistanceMeters.
//
// This exists because IRVE models one row per physical connector: a single
// address can have several PDC rows at (near enough to be indistinguishable
// by distance) the same coordinates — e.g. a CCS 300kW row and a T2 22kW
// row for what's really one charging point. Picking "the nearest" by
// distance alone is then close to arbitrary, and since a single source
// station's tariffs can span both ac and dc kinds (e.g. Electra), the plain
// nearest-only lookup could attach a dc tariff to the row that's actually
// the ac-only connector, permanently hiding that price from anyone at the
// dc station (confirmed in production: a CCS 300kW station showing no
// price at all because its dc tariff landed on the co-located T2 row).
//
// Each point's LATERAL subquery still uses the same bbox-prefiltered KNN
// index scan as FindNearestStations (see bboxDeltas), just with LIMIT 5
// instead of LIMIT 1 — picking the best-of-5 by kind happens in Go, not by
// reordering the SQL's ORDER BY, so the index-accelerated pure-distance
// scan that makes this fast at all is untouched.
//
// connectorType, when non-empty, takes priority over everything else below:
// among the kind-matching candidates, an exact connector_type match (e.g.
// "CCS") is preferred outright over a same-kind-but-different-connector one
// (e.g. "CHAdeMO"), regardless of distance or power. This exists because a
// single physical site can have TWO IRVE rows of the very same kind (e.g. a
// CHAdeMO and a CCS row, both dc) — a source whose own tariffs already
// distinguish connector types (today: only Freshmile, via
// domain.StationTariff.ConnectorType) needs each tariff to land on its own
// matching row, not just "whichever dc row is nearest" for both: confirmed
// in production, a co-located CHAdeMO row kept a source's dc tariff while
// its sibling CCS row got none, because the kind-only match above can only
// ever resolve to a single station per (point, kind). Callers that don't
// distinguish connector types (Electra, Izivia, ChargeNow, Tesla) pass "".
//
// When a point also sets TargetPowerKW, kind alone still isn't always
// enough: IRVE can carry several same-kind rows at the same coordinates
// (e.g. two DC connectors of different power for one physical location —
// observed with Izivia, whose station detail exposes each connector's own
// rated power). Among the kind-matching candidates, the one whose own
// power_kw is closest to TargetPowerKW is preferred over just the nearest;
// a point with no TargetPowerKW (or candidates with no power_kw on file)
// falls back to the plain nearest-of-kind behavior. This tie-break only
// ever runs once connectorType has failed to resolve a match (or wasn't
// given), since an exact connector-type match is a strictly stronger
// signal than power proximity.
func (r *LinkRepository) FindNearestStationsForKind(ctx context.Context, points []NearestStationQuery, kind string, connectorType string, maxDistanceMeters float64) (map[int]NearestStationCandidate, error) {
	if len(points) == 0 {
		return nil, nil
	}

	lngs := make([]float64, len(points))
	lats := make([]float64, len(points))
	idxs := make([]int32, len(points))
	for i, p := range points {
		lngs[i] = p.Lng
		lats[i] = p.Lat
		idxs[i] = int32(i)
	}

	lngDelta, latDelta := bboxDeltas(maxDistanceMeters)

	query := fmt.Sprintf(`
		SELECT q.idx, s.id, s.operator_name, s.name, s.connector_type, s.power_kw, s.distance_m, s.candidate_kind
		FROM unnest($1::float8[], $2::float8[], $3::int[]) AS q(lng, lat, idx)
		LEFT JOIN LATERAL (
			SELECT st.id, st.operator_name, st.name, st.connector_type, st.power_kw,
				ST_Distance(st.location::geography, ST_SetSRID(ST_MakePoint(q.lng, q.lat), 4326)::geography) AS distance_m,
				(%s) AS candidate_kind
			FROM stations st
			WHERE st.location && ST_MakeEnvelope(q.lng - $5, q.lat - $6, q.lng + $5, q.lat + $6, 4326)
				AND ST_DWithin(st.location::geography, ST_SetSRID(ST_MakePoint(q.lng, q.lat), 4326)::geography, $4)
			ORDER BY st.location <-> ST_SetSRID(ST_MakePoint(q.lng, q.lat), 4326)
			LIMIT 5
		) s ON true
		ORDER BY q.idx, s.distance_m`, candidateKindFilterFragment)

	rows, err := r.db.Query(ctx, query, lngs, lats, idxs, maxDistanceMeters, lngDelta, latDelta)
	if err != nil {
		return nil, fmt.Errorf("find nearest stations for kind %s (bulk): %w", kind, err)
	}
	defer rows.Close()

	type scored struct {
		candidate     NearestStationCandidate
		candidateKind string
	}
	byIdx := map[int][]scored{}
	for rows.Next() {
		var idx int32
		var id *uuid.UUID
		var operatorName, name, rowConnectorType *string
		var powerKW *float64
		var distance *float64
		var candidateKind *string
		if err := rows.Scan(&idx, &id, &operatorName, &name, &rowConnectorType, &powerKW, &distance, &candidateKind); err != nil {
			return nil, fmt.Errorf("scan nearest station for kind %s (bulk): %w", kind, err)
		}
		if id == nil {
			continue
		}
		c := NearestStationCandidate{StationID: *id, DistanceMeters: *distance, PowerKW: powerKW}
		if operatorName != nil {
			c.OperatorName = *operatorName
		}
		if name != nil {
			c.Name = *name
		}
		if rowConnectorType != nil {
			c.ConnectorType = *rowConnectorType
		}
		ck := ""
		if candidateKind != nil {
			ck = *candidateKind
		}
		byIdx[int(idx)] = append(byIdx[int(idx)], scored{candidate: c, candidateKind: ck})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	targetPowerByIdx := make(map[int]*float64, len(points))
	for i, p := range points {
		targetPowerByIdx[i] = p.TargetPowerKW
	}

	result := map[int]NearestStationCandidate{}
	for idx, candidates := range byIdx {
		// candidates is already ordered by distance ascending (ORDER BY
		// q.idx, s.distance_m above), so the first kind match found is also
		// the nearest kind match; candidates[0] (the overall nearest) is
		// the fallback if none match.
		best := candidates[0].candidate

		// Tier 1: an exact connector_type match within this kind, if the
		// caller asked for one, wins outright — see this method's doc
		// comment for why that has to outrank both plain kind-matching and
		// the power tie-break below.
		if connectorType != "" {
			foundExact := false
			for _, c := range candidates {
				if c.candidateKind == kind && strings.EqualFold(c.candidate.ConnectorType, connectorType) {
					best = c.candidate
					foundExact = true
					break
				}
			}
			if foundExact {
				result[idx] = best
				continue
			}
		}

		// Tier 2: plain kind match, optionally power-tie-broken (unchanged
		// from before connectorType existed).
		haveKindMatch := false
		var bestPowerDelta float64
		targetPowerKW := targetPowerByIdx[idx]
		for _, c := range candidates {
			if c.candidateKind != kind {
				continue
			}
			if !haveKindMatch {
				best = c.candidate
				haveKindMatch = true
				if targetPowerKW != nil && c.candidate.PowerKW != nil {
					bestPowerDelta = powerDeltaAbs(*targetPowerKW, *c.candidate.PowerKW)
				} else {
					bestPowerDelta = -1 // no power comparison available yet
				}
				continue
			}
			if targetPowerKW == nil || c.candidate.PowerKW == nil {
				continue
			}
			delta := powerDeltaAbs(*targetPowerKW, *c.candidate.PowerKW)
			if bestPowerDelta < 0 || delta < bestPowerDelta {
				best = c.candidate
				bestPowerDelta = delta
			}
		}
		result[idx] = best
	}
	return result, nil
}

func powerDeltaAbs(a, b float64) float64 {
	d := a - b
	if d < 0 {
		return -d
	}
	return d
}

// Upsert creates or refreshes the correlation between an IRVE station and a
// source station.
func (r *LinkRepository) Upsert(ctx context.Context, stationID, sourceStationID uuid.UUID, source, linkQuality string, distanceMeters *float64) error {
	const query = `
		INSERT INTO station_links (station_id, source_station_id, source, link_quality, distance_meters)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (station_id, source_station_id) DO UPDATE SET
			link_quality = EXCLUDED.link_quality,
			distance_meters = EXCLUDED.distance_meters`

	if _, err := r.db.Exec(ctx, query, stationID, sourceStationID, source, linkQuality, distanceMeters); err != nil {
		return fmt.Errorf("upsert station link: %w", err)
	}
	return nil
}

// LinkUpsert is one link to write in a BulkUpsert call.
type LinkUpsert struct {
	StationID       uuid.UUID
	SourceStationID uuid.UUID
	Source          string
	LinkQuality     string
	DistanceMeters  float64
}

// BulkUpsert writes many links in a single round trip. Each
// (StationID, SourceStationID) pair is unique per call by construction
// (SourceStationID is a freshly upserted source station's own UUID), so
// unlike tariffs there's no same-batch conflict-key collision to dedupe.
func (r *LinkRepository) BulkUpsert(ctx context.Context, links []LinkUpsert) error {
	if len(links) == 0 {
		return nil
	}

	n := len(links)
	stationIDs := make([]uuid.UUID, n)
	sourceStationIDs := make([]uuid.UUID, n)
	sources := make([]string, n)
	qualities := make([]string, n)
	distances := make([]float64, n)
	for i, l := range links {
		stationIDs[i] = l.StationID
		sourceStationIDs[i] = l.SourceStationID
		sources[i] = l.Source
		qualities[i] = l.LinkQuality
		distances[i] = l.DistanceMeters
	}

	const query = `
		INSERT INTO station_links (station_id, source_station_id, source, link_quality, distance_meters)
		SELECT * FROM unnest($1::uuid[], $2::uuid[], $3::text[], $4::text[], $5::float8[])
		ON CONFLICT (station_id, source_station_id) DO UPDATE SET
			link_quality = EXCLUDED.link_quality,
			distance_meters = EXCLUDED.distance_meters`

	if _, err := r.db.Exec(ctx, query, stationIDs, sourceStationIDs, sources, qualities, distances); err != nil {
		return fmt.Errorf("bulk upsert station links: %w", err)
	}
	return nil
}
