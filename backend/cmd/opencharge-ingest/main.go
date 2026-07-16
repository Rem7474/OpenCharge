package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"opencharge/internal/ingestion"
	"opencharge/internal/repository"
)

func main() {
	var (
		dsn          = flag.String("dsn", getEnv("DATABASE_URL", "postgres://opencharge:opencharge@localhost:5432/opencharge?sslmode=disable"), "PostgreSQL DSN")
		source       = flag.String("source", "", "source to ingest: irve, electra, izivia, tesla, freshmile, or all")
		irveURL      = flag.String("irve-url", ingestion.DefaultIRVEURL, "IRVE GeoJSON URL")
		electraURL   = flag.String("electra-url", ingestion.DefaultElectraURL, "Electra stations.js URL")
		teslaURL     = flag.String("tesla-url", ingestion.DefaultTeslaLocationsURL, "Tesla find-us get-locations URL")
		teslaChrome  = flag.String("tesla-chrome-path", getEnv("TESLA_CHROME_PATH", ""), "path to the Chromium/Chrome binary used to fetch tesla.com (empty = chromedp's own PATH lookup)")
		freshmileURL = flag.String("freshmile-url", ingestion.DefaultFreshmileBaseURL, "Freshmile charge API base URL")
		linkMaxM     = flag.Float64("link-max-distance-m", ingestion.DefaultLinkMaxDistanceMeters, "max distance (meters) to correlate a source station with an IRVE station")
		timeout      = flag.Duration("timeout", 30*time.Minute, "overall timeout for the ingestion run")
	)
	flag.Parse()

	if *source == "" {
		log.Fatal("missing -source flag: irve, electra, izivia, tesla, freshmile, or all")
	}

	// Canceling ctx on SIGINT/SIGTERM (instead of Go's default of killing
	// the process immediately) lets a Ctrl+C or `docker stop` mid-run flush
	// whatever's already been fetched instead of losing it: ingesters that
	// stream fetch+write (see FreshmileIngester.Run) write what they have
	// as soon as ctx is done rather than only at the very end.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	pool, err := repository.Open(ctx, *dsn)
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer pool.Close()

	stationRepo := repository.NewStationRepository(pool)
	sourceStationRepo := repository.NewSourceStationRepository(pool)
	tariffRepo := repository.NewTariffRepository(pool)
	linkRepo := repository.NewLinkRepository(pool)

	runIRVE := func() {
		ingester := ingestion.NewIRVEIngester(stationRepo, *irveURL)
		count, err := ingester.Run(ctx)
		if err != nil {
			log.Fatalf("irve ingestion failed: %v", err)
		}
		log.Printf("irve ingestion complete: %d stations", count)
	}
	runElectra := func() {
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
		count, err := ingester.Run(ctx)
		if err != nil {
			log.Fatalf("tesla ingestion failed: %v", err)
		}
		log.Printf("tesla ingestion complete: %d stations", count)
	}
	runFreshmile := func() {
		ingester := ingestion.NewFreshmileIngester(pool, sourceStationRepo, tariffRepo, linkRepo, *freshmileURL, ingestion.DefaultFreshmileConfig())
		ingester.MaxLinkDistanceM = *linkMaxM
		count, err := ingester.Run(ctx)
		if err != nil {
			log.Fatalf("freshmile ingestion failed: %v", err)
		}
		log.Printf("freshmile ingestion complete: %d locations", count)
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
	case "all":
		// IRVE first: it's the canonical referential that electra/izivia/
		// tesla/freshmile correlate against.
		runIRVE()
		runElectra()
		runIzivia()
		runTesla()
		runFreshmile()
	default:
		log.Fatalf("unknown -source %q: expected irve, electra, izivia, tesla, freshmile, or all", *source)
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
