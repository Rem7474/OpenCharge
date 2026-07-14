package importer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"chargingbackend/internal/model"
)

type Result struct {
	Stations []model.Station
	Stats    map[string]int
}

func LoadWorkspace(workspace string) (Result, error) {
	var stations []model.Station
	stats := map[string]int{}

	load := func(source string, items []model.Station, err error) error {
		if err != nil {
			return err
		}
		stations = append(stations, items...)
		stats[source] += len(items)
		return nil
	}

	electraItems, err := loadElectra(filepath.Join(workspace, "electra_raw.jsonl"))
	if err := load("electra", electraItems, err); err != nil {
		return Result{}, err
	}
	iziviaNormalizedItems, err := loadIziviaNormalized(filepath.Join(workspace, "izivia_normalized.jsonl"))
	if err := load("izivia_normalized", iziviaNormalizedItems, err); err != nil {
		return Result{}, err
	}
	iziviaAllDataItems, err := loadIziviaAllData(filepath.Join(workspace, "izivia_all_data_pricing_fixed.jsonl"))
	if err := load("izivia_all_data", iziviaAllDataItems, err); err != nil {
		return Result{}, err
	}
	irveItems, err := loadIRVE(filepath.Join(workspace, "irve_raw.csv"))
	if err := load("irve", irveItems, err); err != nil {
		return Result{}, err
	}

	return Result{Stations: dedupeByID(stations), Stats: stats}, nil
}

func loadElectra(path string) ([]model.Station, error) {
	if !exists(path) {
		return nil, nil
	}
	return LoadElectraJSONL(path)
}

func loadIziviaNormalized(path string) ([]model.Station, error) {
	if !exists(path) {
		return nil, nil
	}
	return LoadIziviaNormalizedJSONL(path)
}

func loadIziviaAllData(path string) ([]model.Station, error) {
	if !exists(path) {
		return nil, nil
	}
	return LoadIziviaAllDataJSONL(path)
}

func loadIRVE(path string) ([]model.Station, error) {
	if !exists(path) {
		return nil, nil
	}
	return LoadIRVECsv(path)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func dedupeByID(stations []model.Station) []model.Station {
	byID := make(map[string]model.Station, len(stations))
	for _, station := range stations {
		if station.ID == "" {
			continue
		}
		byID[station.ID] = station
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]model.Station, 0, len(ids))
	for _, id := range ids {
		result = append(result, byID[id])
	}
	return result
}

func (r Result) Summary() string {
	parts := make([]string, 0, len(r.Stats))
	for source, count := range r.Stats {
		parts = append(parts, fmt.Sprintf("%s=%d", source, count))
	}
	sort.Strings(parts)
	return join(parts, ", ")
}

func join(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for _, part := range parts[1:] {
		result += sep + part
	}
	return result
}
