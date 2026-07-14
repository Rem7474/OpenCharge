package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"chargingbackend/internal/importer"
	"chargingbackend/internal/model"
)

type Config struct {
	Workspace string
	OutDir    string
	Workers   int
	GridStep  float64
	Zoom      int
}

type Summary struct {
	Electra int
	Izivia  int
	IRVE    int
	Total   int
}

func ScrapeAll(ctx context.Context, cfg Config) (Summary, error) {
	if cfg.Workspace == "" {
		cfg.Workspace = "."
	}
	if cfg.OutDir == "" {
		cfg.OutDir = cfg.Workspace
	}
	if cfg.Workers <= 0 {
		cfg.Workers = 12
	}
	if cfg.GridStep <= 0 {
		cfg.GridStep = 2.0
	}
	if cfg.Zoom <= 0 {
		cfg.Zoom = 7
	}

	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return Summary{}, err
	}

	log.Printf("scrape start: workspace=%s out=%s workers=%d grid_step=%.2f zoom=%d", cfg.Workspace, cfg.OutDir, cfg.Workers, cfg.GridStep, cfg.Zoom)

	electraPath := filepath.Join(cfg.OutDir, "electra_raw.jsonl")
	iziviaAllDataPath := filepath.Join(cfg.OutDir, "izivia_all_data_pricing_fixed.jsonl")
	iziviaNormalizedPath := filepath.Join(cfg.OutDir, "izivia_normalized.jsonl")
	irvePath := filepath.Join(cfg.OutDir, "irve_raw.csv")
	var iziviaCount int

	electraCount, err := loadOrScrapeElectra(ctx, electraPath)
	if err != nil {
		return Summary{}, err
	}
	log.Printf("electra done: %d stations -> %s", electraCount, electraPath)

	if complete, expected, ok, err := artifactIsComplete(iziviaAllDataPath, countJSONLRecords); err != nil {
		return Summary{}, err
	} else if complete {
		log.Printf("izivia raw reuse: %s exists and is complete (%d lines), skip API calls", iziviaAllDataPath, expected)
		iziviaStations, err := importer.LoadIziviaAllDataJSONL(iziviaAllDataPath)
		if err != nil {
			return Summary{}, fmt.Errorf("normalize izivia all_data: %w", err)
		}
		iziviaCount = len(iziviaStations)
		if err := writeStationsJSONL(iziviaNormalizedPath, iziviaStations); err != nil {
			return Summary{}, err
		}
		log.Printf("izivia normalized done: %d stations -> %s", len(iziviaStations), iziviaNormalizedPath)
	} else {
		if ok {
			log.Printf("izivia raw partial: %s has %d lines, resuming completion", iziviaAllDataPath, expected)
		}
		iziviaCount, err = ScrapeIzivia(ctx, iziviaAllDataPath, IziviaConfig{
			Workers:  cfg.Workers,
			GridStep: cfg.GridStep,
			Zoom:     cfg.Zoom,
		})
		if err != nil {
			return Summary{}, err
		}
		log.Printf("izivia raw done: %d stations -> %s", iziviaCount, iziviaAllDataPath)
	}

	irveCount, err := loadOrScrapeIRVE(ctx, irvePath)
	if err != nil {
		return Summary{}, err
	}
	log.Printf("irve done: %d stations -> %s", irveCount, irvePath)
	log.Printf("scrape complete: electra=%d izivia=%d irve=%d total=%d", electraCount, iziviaCount, irveCount, electraCount+iziviaCount+irveCount)

	return Summary{
		Electra: electraCount,
		Izivia:  iziviaCount,
		IRVE:    irveCount,
		Total:   electraCount + iziviaCount + irveCount,
	}, nil
}

func writeStationsJSONL(path string, stations []model.Station) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	for _, station := range stations {
		if err := encoder.Encode(station); err != nil {
			return err
		}
	}
	return nil
}

func existsNonEmpty(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

func artifactIsComplete(path string, counter func(string) (int, error)) (bool, int, bool, error) {
	if !existsNonEmpty(path) {
		return false, 0, false, nil
	}
	meta, ok, err := loadArtifactMeta(path)
	if err != nil {
		return false, 0, ok, err
	}
	if !ok || !meta.Completed {
		count, err := counter(path)
		if err != nil {
			return false, 0, ok, err
		}
		return false, count, ok, nil
	}
	count, err := counter(path)
	if err != nil {
		return false, 0, ok, err
	}
	return count == meta.Count, meta.Count, ok, nil
}

func countJSONLRecords(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	count := 0
	buffer := make([]byte, 32*1024)
	for {
		read, err := file.Read(buffer)
		count += strings.Count(string(buffer[:read]), "\n")
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
	}
	return count, nil
}

func countCSVRecords(path string) (int, error) {
	count, err := countJSONLRecords(path)
	if err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, nil
	}
	return count - 1, nil
}

func loadOrScrapeElectra(ctx context.Context, path string) (int, error) {
	if complete, expected, ok, err := artifactIsComplete(path, countJSONLRecords); err != nil {
		return 0, err
	} else if complete {
		log.Printf("electra reuse: %s exists and is complete (%d lines), skip API calls", path, expected)
		stations, err := importer.LoadElectraJSONL(path)
		if err != nil {
			return 0, err
		}
		return len(stations), nil
	} else if ok {
		log.Printf("electra partial: %s has %d lines, refreshing from API", path, expected)
	}
	count, err := ScrapeElectra(ctx, path)
	if err != nil {
		return 0, err
	}
	if err := saveArtifactMeta(path, count); err != nil {
		return 0, err
	}
	return count, nil
}

func loadOrScrapeIRVE(ctx context.Context, path string) (int, error) {
	if complete, expected, ok, err := artifactIsComplete(path, countCSVRecords); err != nil {
		return 0, err
	} else if complete {
		log.Printf("irve reuse: %s exists and is complete (%d records), skip API calls", path, expected)
		stations, err := importer.LoadIRVECsv(path)
		if err != nil {
			return 0, err
		}
		return len(stations), nil
	} else if ok {
		log.Printf("irve partial: %s has %d records, refreshing from API", path, expected)
	}
	count, err := ScrapeIRVE(ctx, path)
	if err != nil {
		return 0, err
	}
	if err := saveArtifactMeta(path, count); err != nil {
		return 0, err
	}
	return count, nil
}
