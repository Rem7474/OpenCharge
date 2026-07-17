package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// StaleSourceDataGracePeriod is how long a source_station/tariff can go
// unseen before SweepStaleSourceData is willing to delete it. Callers pass
// runStart.Add(-StaleSourceDataGracePeriod) as the sweep threshold, not
// runStart itself: a single run — even a fully successful one — isn't
// trusted on its own to mean "the source no longer has this station".
// Real-world scans can legitimately miss a station on a given run for
// reasons short of it actually being gone (a slow API response that still
// lands inside the overall timeout but after that station would have been
// reached, transient upstream flakiness on one page/tile, ...). Requiring
// a full month of being consistently unseen across many runs before
// deleting turns "one run's blind spot" from an immediate deletion into a
// non-event: the row simply survives until the next run has a chance to
// see it again.
const StaleSourceDataGracePeriod = 30 * 24 * time.Hour

// SweepStaleSourceData deletes source_stations (and, via their
// ON DELETE CASCADE to station_links, any links pointing at them) and
// station_tariffs rows for source whose updated_at predates before — i.e.
// things the source hasn't reported in a long time: a station delisted, or
// a tariff that disappeared (went free, the station went out of service,
// ...). Callers should pass runStart.Add(-StaleSourceDataGracePeriod), not
// a bare "now", so a single run's gaps don't get treated as removals (see
// StaleSourceDataGracePeriod).
//
// Every write to these tables refreshes updated_at via BulkUpsert/Upsert
// (see SourceStationRepository, TariffRepository), so "untouched since
// before" reliably means "not seen by any run in at least that long" — but
// this must ONLY be called after a full, successful ingestion run. A
// partial run (ctx canceled, timeout, a write error) hasn't necessarily
// observed everything the source has to offer, and calling this at all
// after such a run risks compounding a string of partial runs into an
// incorrect sweep.
//
// station_tariffs has no source_station_id column (it's keyed on
// station_id + source, not tied to a specific source_station row), so it
// needs its own sweep independent of the source_stations one — there's no
// FK cascade that covers it.
func SweepStaleSourceData(ctx context.Context, pool *pgxpool.Pool, source string, before time.Time) error {
	if _, err := pool.Exec(ctx, `DELETE FROM source_stations WHERE source = $1 AND updated_at < $2`, source, before); err != nil {
		return fmt.Errorf("sweep stale source_stations for %s: %w", source, err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM station_tariffs WHERE source = $1 AND updated_at < $2`, source, before); err != nil {
		return fmt.Errorf("sweep stale station_tariffs for %s: %w", source, err)
	}
	return nil
}
