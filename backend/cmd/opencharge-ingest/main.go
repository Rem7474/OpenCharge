package main

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/Rem7474/opencharge/internal/ingestion"
	"github.com/Rem7474/opencharge/internal/repository"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	source := flag.String("source", "", "Source à ingérer: irve | electra | izivia")
	dbURL := flag.String("db", "", "PostgreSQL URL (ou env DB_URL)")
	radiusMeters := flag.Float64("radius", 150, "Rayon de corrélation IRVE en mètres")
	// Izivia
	iziviaLng := flag.Float64("izivia-lng", 6.1, "Longitude du centre pour Izivia")
	iziviaLat := flag.Float64("izivia-lat", 45.9, "Latitude du centre pour Izivia")
	iziviaZoom := flag.Int("izivia-zoom", 9, "Zoom pour Izivia")
	flag.Parse()

	if *source == "" {
		log.Fatal("Usage: opencharge-ingest --source irve|electra|izivia")
	}

	if *dbURL == "" {
		*dbURL = os.Getenv("DB_URL")
	}
	if *dbURL == "" {
		*dbURL = "postgres://opencharge:opencharge@localhost:5432/opencharge?sslmode=disable"
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *dbURL)
	if err != nil {
		log.Fatalf("DB connect: %v", err)
	}
	defer pool.Close()

	stationRepo := repository.NewStationRepository(pool)
	tariffRepo := repository.NewTariffRepository(pool)
	linkRepo := repository.NewLinkRepository(pool)

	switch *source {
	case "irve":
		log.Println("=== Ingestion IRVE ===")
		if err := ingestion.IngestIRVE(ctx, stationRepo); err != nil {
			log.Fatalf("IRVE ingest error: %v", err)
		}
	case "electra":
		log.Println("=== Ingestion Electra ===")
		if err := ingestion.IngestElectra(ctx, linkRepo, tariffRepo, *radiusMeters); err != nil {
			log.Fatalf("Electra ingest error: %v", err)
		}
	case "izivia":
		log.Println("=== Ingestion Izivia ===")
		if err := ingestion.IngestIziviaSquare(
			ctx, linkRepo, tariffRepo,
			*iziviaLng, *iziviaLat, *iziviaZoom, *radiusMeters,
		); err != nil {
			log.Fatalf("Izivia ingest error: %v", err)
		}
	default:
		log.Fatalf("Source inconnue: %s (irve|electra|izivia)", *source)
	}

	log.Println("=== Ingestion terminée ===")
}
