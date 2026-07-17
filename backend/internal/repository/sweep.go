package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SweepStaleSourceData deletes source_stations (and, via their
// ON DELETE CASCADE to station_links, any links pointing at them) and
// station_tariffs rows for source that weren't touched during a run that
// started at runStart — i.e. things the source no longer reports: a
// station delisted, or a tariff that disappeared (went free, the station
// went out of service, ...).
//
// Every write to these tables refreshes updated_at via BulkUpsert/Upsert
// (see SourceStationRepository, TariffRepository), so "untouched since
// runStart" reliably means "not seen in this run" — but this must ONLY be
// called after a full, successful ingestion run. A partial run (ctx
// canceled, timeout, a write error) hasn't necessarily observed everything
// the source has to offer, so treating "not touched yet" as "gone" would
// wrongly delete stations that were simply never reached this run rather
// than ones that actually vanished from the source.
//
// station_tariffs has no source_station_id column (it's keyed on
// station_id + source, not tied to a specific source_station row), so it
// needs its own sweep independent of the source_stations one — there's no
// FK cascade that covers it.
func SweepStaleSourceData(ctx context.Context, pool *pgxpool.Pool, source string, runStart time.Time) error {
	if _, err := pool.Exec(ctx, `DELETE FROM source_stations WHERE source = $1 AND updated_at < $2`, source, runStart); err != nil {
		return fmt.Errorf("sweep stale source_stations for %s: %w", source, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM station_tariffs WHERE source = $1 AND updated_at < $2`, source, runStart); err != nil {
		return fmt.Errorf("sweep stale station_tariffs for %s: %w", source, err)
	}
	return nil
}
