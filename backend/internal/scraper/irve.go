package scraper

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

const irveURL = "https://www.data.gouv.fr/api/1/datasets/r/2729b192-40ab-4454-904d-735084dca3a3"

func ScrapeIRVE(ctx context.Context, outPath string) (int, error) {
	log.Printf("irve start: downloading %s", irveURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, irveURL, nil)
	if err != nil {
		return 0, err
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return 0, fmt.Errorf("irve http %d: %s", resp.StatusCode, string(body))
	}

	file, err := os.Create(outPath)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	written, err := io.Copy(file, resp.Body)
	if err != nil {
		return 0, err
	}

	if written == 0 {
		return 0, fmt.Errorf("irve response empty")
	}
	log.Printf("irve write complete: %s (%d bytes)", outPath, written)
	return int(written), nil
}
