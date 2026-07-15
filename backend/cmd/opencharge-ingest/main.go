package main

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/Rem7474/opencharge/internal/ingestion"
	"github.com/Rem7474/opencharge/internal/repository"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

func main() {
	source := flag.String("source", "irve", "Ingestion source: irve | electra | izivia")
	linkDist := flag.Float64("link-dist", 0.001, "Max distance in degrees for geolocation linking (~100m)")
	flag.Parse()

	logger, err := zap.NewProduction()
	if err != nil {
		log.Fatalf("init logger: %v", err)
	}
	defer logger.Sync()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://opencharge:opencharge@localhost:5432/opencharge?sslmode=disable"
	}

	pool, err := pgxpool.New(context.Background(), dbURL)
	if err != nil {
		logger.Fatal("pgxpool connect", zap.Error(err))
	}
	defer pool.Close()

	ctx := context.Background()
	stationRepo := repository.NewStationRepository(pool)
	tariffRepo := repository.NewTariffRepository(pool)
	linkRepo := repository.NewLinkRepository(pool)

	switch *source {
	case "irve":
		if err := ingestion.IngestIRVE(ctx, stationRepo, logger); err != nil {
			logger.Fatal("IRVE ingestion failed", zap.Error(err))
		}
	case "electra":
		if err := ingestion.IngestElectra(ctx, stationRepo, tariffRepo, linkRepo, logger, *linkDist); err != nil {
			logger.Fatal("Electra ingestion failed", zap.Error(err))
		}
	case "izivia":
		if err := ingestion.IngestIzivia(ctx, stationRepo, tariffRepo, linkRepo, logger, *linkDist); err != nil {
			logger.Fatal("Izivia ingestion failed", zap.Error(err))
		}
	default:
		logger.Fatal("Unknown source", zap.String("source", *source))
	}
}
