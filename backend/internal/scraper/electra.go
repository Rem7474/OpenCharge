package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

const electraURL = "https://stations.go-electra.com/stations.js"

func ScrapeElectra(ctx context.Context, outPath string) (int, error) {
	log.Printf("electra start: downloading %s", electraURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, electraURL, nil)
	if err != nil {
		return 0, err
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return 0, fmt.Errorf("electra http %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	text := strings.TrimSpace(string(body))
	text = strings.TrimPrefix(text, "export default")
	text = strings.TrimSpace(text)
	text = strings.TrimSuffix(text, ";")
	text = strings.TrimSpace(text)

	var stations []any
	if err := json.Unmarshal([]byte(text), &stations); err != nil {
		return 0, fmt.Errorf("parse electra payload: %w", err)
	}
	log.Printf("electra parsed: %d stations", len(stations))

	file, err := os.Create(outPath)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	for _, station := range stations {
		if err := encoder.Encode(map[string]any{"source": "electra", "raw": station}); err != nil {
			return 0, err
		}
	}
	log.Printf("electra write complete: %s", outPath)

	return len(stations), nil
}
