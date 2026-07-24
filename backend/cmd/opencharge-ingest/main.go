package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"opencharge/internal/ingestion"
	"opencharge/internal/repository"
)

func main() {
	var (
		dsn              = flag.String("dsn", getEnv("DATABASE_URL", "postgres://opencharge:opencharge@localhost:5432/opencharge?sslmode=disable"), "PostgreSQL DSN")
		source           = flag.String("source", "", "source to ingest: irve, electra, izivia, tesla, freshmile, fastned, lidl, chargenow, ionity, eborn, sowatt, or all")
		irveURL          = flag.String("irve-url", ingestion.DefaultIRVEURL, "IRVE GeoJSON URL")
		electraURL       = flag.String("electra-url", ingestion.DefaultElectraURL, "Electra stations.js URL")
		teslaURL         = flag.String("tesla-url", ingestion.DefaultTeslaLocationsURL, "Tesla find-us get-locations URL")
		teslaChrome      = flag.String("tesla-chrome-path", getEnv("TESLA_CHROME_PATH", ""), "path to the Chromium/Chrome binary used to fetch tesla.com (empty = chromedp's own PATH lookup)")
		freshmileURL     = flag.String("freshmile-url", ingestion.DefaultFreshmileBaseURL, "Freshmile charge API base URL")
		freshmileNetwork = flag.String("freshmile-network", "", "restrict Freshmile map-locations discovery to this network id (e.g. FRFR1 for Freshmile's own network); empty scans every network")
		chargenowURL     = flag.String("chargenow-url", ingestion.DefaultChargenowBaseURL, "ChargeNow map API base URL")
		linkMaxM         = flag.Float64("link-max-distance-m", ingestion.DefaultLinkMaxDistanceMeters, "max distance (meters) to correlate a source station with an IRVE station")
		idleTimeout      = flag.Duration("idle-timeout", ingestion.DefaultIdleTimeout, "for izivia/tesla/freshmile/chargenow: abort the run if it goes this long without a single successful request (0 disables it) — unlike a flat overall timeout, this doesn't cut off a run that's still making progress")
		failedDir        = flag.String("failed-dir", getEnv("INGEST_FAILED_DIR", "ingest-failures"), "directory where each source saves its failed URLs as <source>.json, for a later -retry-failed pass")
		retryFailed      = flag.Bool("retry-failed", false, "instead of a full scan, replay only the URLs recorded as failed in -failed-dir by a previous run (izivia, tesla, freshmile, chargenow)")
	)
	flag.Parse()

	if *source == "" {
		log.Fatal("missing -source flag: irve, electra, izivia, tesla, freshmile, fastned, lidl, chargenow, ionity, eborn, sowatt, or all")
	}

	// Canceling ctx on SIGINT/SIGTERM (instead of Go's default of killing
	// the process immediately) lets a Ctrl+C or `docker stop` mid-run flush
	// whatever's already been fetched instead of losing it: ingesters that
	// stream fetch+write (see FreshmileIngester.Run) write what they have
	// as soon as ctx is done rather than only at the very end.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := repository.Open(ctx, *dsn)
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer pool.Close()

	stationRepo := repository.NewStationRepository(pool)
	sourceStationRepo := repository.NewSourceStationRepository(pool)
	tariffRepo := repository.NewTariffRepository(pool)
	linkRepo := repository.NewLinkRepository(pool)

	// Sources that fan out over many URLs (izivia, tesla, freshmile,
	// chargenow) save whatever failed for good to <failed-dir>/<source>.json
	// at the end of every run; -retry-failed replays just those instead of
	// a full scan. The other sources are a single download (or none at
	// all) — re-running them IS the retry, so -retry-failed skips them.
	failureLogPath := func(source string) string {
		return filepath.Join(*failedDir, source+".json")
	}
	// loadFailures returns (failures, true) when there's something to
	// retry; a missing or empty file just means the previous run fully
	// succeeded, which isn't an error.
	loadFailures := func(source string) ([]ingestion.FailedFetch, bool) {
		path := failureLogPath(source)
		failures, err := ingestion.LoadFailedFetches(path)
		if errors.Is(err, os.ErrNotExist) {
			log.Printf("%s: no failed-URL file at %s, nothing to retry", source, path)
			return nil, false
		}
		if err != nil {
			log.Fatalf("%s: read failed-URL file: %v", source, err)
		}
		if len(failures) == 0 {
			log.Printf("%s: %s lists no failures, nothing to retry", source, path)
			return nil, false
		}
		return failures, true
	}
	skipRetryUnsupported := func(source string) bool {
		if *retryFailed {
			log.Printf("%s: single-download source, no per-URL failure tracking — re-run without -retry-failed instead", source)
			return true
		}
		return false
	}

	runIRVE := func() {
		if skipRetryUnsupported("irve") {
			return
		}
		ingester := ingestion.NewIRVEIngester(stationRepo, *irveURL)
		count, err := ingester.Run(ctx)
		if err != nil {
			log.Fatalf("irve ingestion failed: %v", err)
		}
		log.Printf("irve ingestion complete: %d stations", count)
	}
	runElectra := func() {
		if skipRetryUnsupported("electra") {
			return
		}
		ingester := ingestion.NewElectraIngester(pool, sourceStationRepo, tariffRepo, linkRepo, *electraURL)
		ingester.MaxLinkDistanceM = *linkMaxM
		count, err := ingester.Run(ctx)
		if err != nil {
			log.Fatalf("electra ingestion failed: %v", err)
		}
		log.Printf("electra ingestion complete: %d stations", count)
	}
	runIzivia := func() {
		ingester := ingestion.NewIziviaIngester(pool, sourceStationRepo, tariffRepo, linkRepo, ingestion.DefaultIziviaConfig())
		ingester.MaxLinkDistanceM = *linkMaxM
		ingester.IdleTimeout = *idleTimeout
		ingester.Failures = ingestion.NewFailureLog(failureLogPath("izivia"), "izivia")
		if *retryFailed {
			failures, ok := loadFailures("izivia")
			if !ok {
				return
			}
			count, err := ingester.RetryFailed(ctx, failures)
			if err != nil {
				log.Fatalf("izivia retry failed: %v", err)
			}
			log.Printf("izivia retry complete: %d stations", count)
			return
		}
		count, err := ingester.Run(ctx)
		if err != nil {
			log.Fatalf("izivia ingestion failed: %v", err)
		}
		log.Printf("izivia ingestion complete: %d stations", count)
	}
	runTesla := func() {
		ingester := ingestion.NewTeslaIngester(pool, sourceStationRepo, tariffRepo, linkRepo, *teslaURL, ingestion.DefaultTeslaConfig())
		ingester.MaxLinkDistanceM = *linkMaxM
		ingester.ChromeExecPath = *teslaChrome
		ingester.IdleTimeout = *idleTimeout
		ingester.Failures = ingestion.NewFailureLog(failureLogPath("tesla"), "tesla")
		if *retryFailed {
			failures, ok := loadFailures("tesla")
			if !ok {
				return
			}
			count, err := ingester.RetryFailed(ctx, failures)
			if err != nil {
				log.Fatalf("tesla retry failed: %v", err)
			}
			log.Printf("tesla retry complete: %d stations", count)
			return
		}
		count, err := ingester.Run(ctx)
		if err != nil {
			log.Fatalf("tesla ingestion failed: %v", err)
		}
		log.Printf("tesla ingestion complete: %d stations", count)
	}
	runFreshmile := func() {
		ingester := ingestion.NewFreshmileIngester(pool, sourceStationRepo, tariffRepo, linkRepo, *freshmileURL, ingestion.DefaultFreshmileConfig())
		ingester.MaxLinkDistanceM = *linkMaxM
		ingester.IdleTimeout = *idleTimeout
		ingester.Network = *freshmileNetwork
		ingester.Failures = ingestion.NewFailureLog(failureLogPath("freshmile"), "freshmile")
		if *retryFailed {
			failures, ok := loadFailures("freshmile")
			if !ok {
				return
			}
			count, err := ingester.RetryFailed(ctx, failures)
			if err != nil {
				log.Fatalf("freshmile retry failed: %v", err)
			}
			log.Printf("freshmile retry complete: %d locations", count)
			return
		}
		count, err := ingester.Run(ctx)
		if err != nil {
			log.Fatalf("freshmile ingestion failed: %v", err)
		}
		log.Printf("freshmile ingestion complete: %d locations", count)
	}
	runFastned := func() {
		if skipRetryUnsupported("fastned") {
			return
		}
		// No source URL/config: Fastned's stations are already in IRVE,
		// this only tags them with fixed tariffs — see fastned.go.
		ingester := ingestion.NewFastnedIngester(pool, stationRepo, tariffRepo)
		count, err := ingester.Run(ctx)
		if err != nil {
			log.Fatalf("fastned ingestion failed: %v", err)
		}
		log.Printf("fastned ingestion complete: %d stations", count)
	}
	runLidl := func() {
		if skipRetryUnsupported("lidl") {
			return
		}
		// Same shape as fastned: no source URL/config, just tags
		// already-known IRVE stations — see lidl.go.
		ingester := ingestion.NewLidlIngester(pool, stationRepo, tariffRepo)
		count, err := ingester.Run(ctx)
		if err != nil {
			log.Fatalf("lidl ingestion failed: %v", err)
		}
		log.Printf("lidl ingestion complete: %d stations", count)
	}
	runChargenow := func() {
		ingester := ingestion.NewChargenowIngester(pool, sourceStationRepo, tariffRepo, linkRepo, *chargenowURL, ingestion.DefaultChargenowConfig())
		ingester.MaxLinkDistanceM = *linkMaxM
		ingester.IdleTimeout = *idleTimeout
		ingester.Failures = ingestion.NewFailureLog(failureLogPath("chargenow"), "chargenow")
		if *retryFailed {
			failures, ok := loadFailures("chargenow")
			if !ok {
				return
			}
			count, err := ingester.RetryFailed(ctx, failures)
			if err != nil {
				log.Fatalf("chargenow retry failed: %v", err)
			}
			log.Printf("chargenow retry complete: %d stations", count)
			return
		}
		count, err := ingester.Run(ctx)
		if err != nil {
			log.Fatalf("chargenow ingestion failed: %v", err)
		}
		log.Printf("chargenow ingestion complete: %d stations", count)
	}
	runIonity := func() {
		if skipRetryUnsupported("ionity") {
			return
		}
		// No source URL/config: Ionity's stations are already in IRVE,
		// this only tags them with fixed tariffs — see ionity.go.
		ingester := ingestion.NewIonityIngester(pool, stationRepo, tariffRepo)
		count, err := ingester.Run(ctx)
		if err != nil {
			log.Fatalf("ionity ingestion failed: %v", err)
		}
		log.Printf("ionity ingestion complete: %d stations", count)
	}
	runEborn := func() {
		if skipRetryUnsupported("eborn") {
			return
		}
		// Same shape as ionity/fastned/lidl: no source URL/config, just
		// tags already-known IRVE stations — see eborn.go.
		ingester := ingestion.NewEbornIngester(pool, stationRepo, tariffRepo)
		count, err := ingester.Run(ctx)
		if err != nil {
			log.Fatalf("eborn ingestion failed: %v", err)
		}
		log.Printf("eborn ingestion complete: %d stations", count)
	}
	runSowatt := func() {
		if skipRetryUnsupported("sowatt") {
			return
		}
		// Same shape as ionity/fastned/lidl/eborn: no source URL/config,
		// just tags already-known IRVE stations — see sowatt.go.
		ingester := ingestion.NewSowattIngester(pool, stationRepo, tariffRepo)
		count, err := ingester.Run(ctx)
		if err != nil {
			log.Fatalf("sowatt ingestion failed: %v", err)
		}
		log.Printf("sowatt ingestion complete: %d stations", count)
	}

	switch *source {
	case "irve":
		runIRVE()
	case "electra":
		runElectra()
	case "izivia":
		runIzivia()
	case "tesla":
		runTesla()
	case "freshmile":
		runFreshmile()
	case "fastned":
		runFastned()
	case "lidl":
		runLidl()
	case "chargenow":
		runChargenow()
	case "ionity":
		runIonity()
	case "eborn":
		runEborn()
	case "sowatt":
		runSowatt()
	case "all":
		// IRVE first: it's the canonical referential that electra/izivia/
		// tesla/freshmile/chargenow correlate against, and that
		// fastned/lidl/ionity/eborn/sowatt tag directly — so it must exist
		// before any of those run too.
		runIRVE()
		runElectra()
		runIzivia()
		runTesla()
		runFreshmile()
		runFastned()
		runLidl()
		runChargenow()
		runIonity()
		runEborn()
		runSowatt()
	default:
		log.Fatalf("unknown -source %q: expected irve, electra, izivia, tesla, freshmile, fastned, lidl, chargenow, ionity, eborn, sowatt, or all", *source)
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
