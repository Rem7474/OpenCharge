package scraper

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const iziviaBaseURL = "https://fronts-map.izivia.com/api"

type IziviaConfig struct {
	Workers  int
	GridStep float64
	Zoom     int
}

type iziviaRecord struct {
	Marker  map[string]any `json:"marker"`
	Station map[string]any `json:"station"`
	Pricing []any          `json:"pricing"`
	Errors  map[string]any `json:"errors"`
}

var iziviaHeaders = map[string]string{
	"Accept":          "application/json",
	"Accept-Language": "fr",
	"User-Agent":      "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36 Edg/150.0.0.0",
	"Referer":         "https://fronts-map.izivia.com/",
	"Origin":          "https://fronts-map.izivia.com",
	"x-device-id":     "b1a5a1c8-68b4-41fb-a18f-78d53910878a",
	"Content-Type":    "application/json",
}

type iziviaSquare struct {
	CenterLng float64 `json:"centerLng"`
	CenterLat float64 `json:"centerLat"`
	Zoom      int     `json:"zoom"`
}

func ScrapeIzivia(ctx context.Context, outPath string, cfg IziviaConfig) (int, error) {
	if cfg.Workers <= 0 {
		cfg.Workers = 12
	}
	if cfg.GridStep <= 0 {
		cfg.GridStep = 2.0
	}
	if cfg.Zoom <= 0 {
		cfg.Zoom = 7
	}

	existingIDs, existingCount, err := loadExistingIziviaState(outPath)
	if err != nil {
		return 0, err
	}
	if existingCount > 0 {
		log.Printf("izivia resume: %s already has %d records, will append missing stations", outPath, existingCount)
	}

	squares := iziviaSquares(cfg.GridStep, cfg.Zoom)
	log.Printf("izivia start: squares=%d workers=%d grid_step=%.2f zoom=%d", len(squares), cfg.Workers, cfg.GridStep, cfg.Zoom)
	markers, err := fetchIziviaMarkers(ctx, squares)
	if err != nil {
		return 0, err
	}
	log.Printf("izivia markers found: %d", len(markers))
	if len(existingIDs) > 0 {
		filtered := make([]map[string]any, 0, len(markers))
		for _, marker := range markers {
			id, _ := marker["id"].(string)
			if id == "" {
				continue
			}
			if _, ok := existingIDs[id]; ok {
				continue
			}
			filtered = append(filtered, marker)
		}
		log.Printf("izivia resume: %d/%d stations already present, %d to fetch", existingCount, len(markers), len(filtered))
		markers = filtered
	}

	file, err := openIziviaOutput(outPath, existingCount > 0)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)

	markerCh := make(chan map[string]any)
	resultCh := make(chan iziviaRecord)
	var wg sync.WaitGroup
	var stationErrors int64

	worker := func() {
		defer wg.Done()
		for marker := range markerCh {
			record, err := fetchIziviaStation(ctx, marker)
			if err != nil {
				stationID, _ := marker["id"].(string)
				log.Printf("izivia station error: id=%s err=%v", stationID, err)
				atomic.AddInt64(&stationErrors, 1)
				continue
			}
			resultCh <- record
		}
	}

	for i := 0; i < cfg.Workers; i++ {
		wg.Add(1)
		go worker()
	}

	go func() {
		for _, marker := range markers {
			markerCh <- marker
		}
		close(markerCh)
		wg.Wait()
		close(resultCh)
	}()

	seen := map[string]struct{}{}
	var count int64
	for record := range resultCh {
		stationID := stationIDFromRecord(record)
		if stationID == "" {
			continue
		}
		if _, ok := seen[stationID]; ok {
			continue
		}
		seen[stationID] = struct{}{}
		if err := encoder.Encode(record); err != nil {
			return 0, err
		}
		count++
		if count == 1 || count%50 == 0 {
			log.Printf("izivia progress: %d stations written", count)
		}
	}

	total := existingCount + int(count)
	log.Printf("izivia write complete: %s (%d total stations, added=%d, station_errors=%d)", outPath, total, count, atomic.LoadInt64(&stationErrors))
	return total, nil
}

func loadExistingIziviaState(path string) (map[string]struct{}, int, error) {
	ids := map[string]struct{}{}
	count := 0
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ids, 0, nil
		}
		return nil, 0, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec iziviaRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if id := stationIDFromRecord(rec); id != "" {
			ids[id] = struct{}{}
		}
		count++
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, err
	}
	return ids, count, nil
}

func openIziviaOutput(path string, appendMode bool) (*os.File, error) {
	if appendMode {
		return os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	}
	return os.Create(path)
}

func stationIDFromRecord(record iziviaRecord) string {
	if record.Station != nil {
		if id, _ := record.Station["id"].(string); id != "" {
			return id
		}
	}
	if record.Marker != nil {
		if id, _ := record.Marker["id"].(string); id != "" {
			return id
		}
	}
	return ""
}

func fetchIziviaMarkers(ctx context.Context, squares []iziviaSquare) ([]map[string]any, error) {
	all := make([]map[string]any, 0)
	seen := map[string]struct{}{}
	totalSquares := len(squares)
	for index, square := range squares {
		if index == 0 || (index+1)%25 == 0 || index+1 == totalSquares {
			log.Printf("izivia markers progress: square %d/%d unique_markers=%d", index+1, totalSquares, len(all))
		}
		payload := map[string]any{
			"square":  square,
			"filters": map[string]any{},
		}
		body, err := postJSON(ctx, iziviaBaseURL+"/map/markers", payload)
		if err != nil {
			log.Printf("izivia markers error: square %d/%d err=%v", index+1, totalSquares, err)
			continue
		}
		var markers []map[string]any
		if err := json.Unmarshal(body, &markers); err != nil {
			log.Printf("izivia markers decode error: square %d/%d err=%v", index+1, totalSquares, err)
			continue
		}
		for _, marker := range markers {
			id, _ := marker["id"].(string)
			if id == "" {
				continue
			}
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			all = append(all, marker)
		}
		if (index+1)%25 == 0 || index+1 == totalSquares {
			log.Printf("izivia markers update: square %d/%d unique_markers=%d", index+1, totalSquares, len(all))
		}
	}
	log.Printf("izivia markers fetch complete: %d unique markers", len(all))
	return all, nil
}

func fetchIziviaStation(ctx context.Context, marker map[string]any) (iziviaRecord, error) {
	stationID, _ := marker["id"].(string)
	if stationID == "" {
		return iziviaRecord{}, fmt.Errorf("izivia marker without id")
	}
	log.Printf("izivia station fetch: %s", stationID)

	stationBody, err := postJSON(ctx, fmt.Sprintf("%s/charging-locations/%s", iziviaBaseURL, stationID), map[string]any{})
	if err != nil {
		return iziviaRecord{}, fmt.Errorf("station details failed for %s: %w", stationID, err)
	}
	var station map[string]any
	if err := json.Unmarshal(stationBody, &station); err != nil {
		return iziviaRecord{}, fmt.Errorf("decode izivia station %s: %w", stationID, err)
	}

	stationEmipID, _ := station["firstStationEmipId"].(string)
	var pricing []any
	if stationEmipID != "" {
		pricingBody, err := getJSON(ctx, fmt.Sprintf("%s/charging-locations/%s/pricing-info-items?stationEmipId=%s", iziviaBaseURL, stationID, stationEmipID))
		if err == nil && len(strings.TrimSpace(string(pricingBody))) > 0 {
			_ = json.Unmarshal(pricingBody, &pricing)
		} else if err != nil {
			log.Printf("izivia pricing error: id=%s err=%v", stationID, err)
		}
	}
	log.Printf("izivia station fetched: %s", stationID)

	return iziviaRecord{
		Marker:  marker,
		Station: station,
		Pricing: pricing,
		Errors:  map[string]any{},
	}, nil
}

func iziviaSquares(step float64, zoom int) []iziviaSquare {
	minLng, maxLng := -5.5, 9.8
	minLat, maxLat := 41.0, 51.5
	var squares []iziviaSquare
	for lat := minLat; lat <= maxLat; lat += step {
		for lng := minLng; lng <= maxLng; lng += step {
			squares = append(squares, iziviaSquare{CenterLng: lng, CenterLat: lat, Zoom: zoom})
		}
	}
	return squares
}

func postJSON(ctx context.Context, url string, payload map[string]any) ([]byte, error) {
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	for key, value := range iziviaHeaders {
		req.Header.Set(key, value)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return nil, fmt.Errorf("izivia http %d: %s", resp.StatusCode, string(data))
	}
	return io.ReadAll(resp.Body)
}

func getJSON(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for key, value := range iziviaHeaders {
		req.Header.Set(key, value)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return nil, fmt.Errorf("izivia http %d: %s", resp.StatusCode, string(data))
	}
	return io.ReadAll(resp.Body)
}

func sortedMarkerIDs(markers []map[string]any) []string {
	ids := make([]string, 0, len(markers))
	for _, marker := range markers {
		if id, _ := marker["id"].(string); id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}
