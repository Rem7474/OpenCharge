package ingestion

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Failure kinds recorded by the ingesters that fan out over many URLs.
// Each kind's Params carries exactly what that ingester's RetryFailed
// needs to replay the request without re-running a full scan.
const (
	failKindIziviaStation     = "izivia_station"       // Params: the /map/markers marker (id, lat, lng, ...)
	failKindIziviaSquare      = "izivia_square"        // Params: iziviaSquare
	failKindTeslaSupercharger = "tesla_supercharger"   // Params: {"slug": ...}
	failKindFreshmileLocation = "freshmile_location"   // Params: {"id": ...}
	failKindFreshmileTile     = "freshmile_tile"       // Params: freshmileBBox
	failKindChargenowBBox     = "chargenow_query_bbox" // Params: chargenowBBox
	failKindChargenowPool     = "chargenow_price_pool" // Params: chargenowPool
)

// FailedFetch is one request that failed for good during an ingestion run
// — i.e. after the ingester's own HTTP retry/backoff budget was already
// exhausted (or on a non-retryable 4xx). It carries enough context (Kind +
// Params) for the matching ingester's RetryFailed to replay just that
// request in a later, targeted pass, without re-scanning everything.
type FailedFetch struct {
	Source   string          `json:"source"`
	Kind     string          `json:"kind"`
	URL      string          `json:"url,omitempty"`
	Params   json.RawMessage `json:"params,omitempty"`
	Error    string          `json:"error"`
	FailedAt time.Time       `json:"failedAt"`
}

// failedFetchFile is the on-disk JSON shape of a failure log.
type failedFetchFile struct {
	Source   string        `json:"source"`
	SavedAt  time.Time     `json:"savedAt"`
	Failures []FailedFetch `json:"failures"`
}

// FailureLog collects FailedFetch records during a run (safe for
// concurrent use — ingesters record from many worker goroutines) and
// persists them as a local JSON file, one file per source. A nil
// *FailureLog is valid and records/saves nothing, so ingesters can call
// it unconditionally and tests/callers that don't care can leave the
// field unset.
type FailureLog struct {
	path   string
	source string

	mu    sync.Mutex
	items []FailedFetch
}

// NewFailureLog returns a FailureLog that Save() will write to path.
func NewFailureLog(path, source string) *FailureLog {
	return &FailureLog{path: path, source: source}
}

// Record appends one failure. params, if non-nil, is JSON-marshalled and
// kept verbatim so a retry pass can replay the request; a params value
// that doesn't marshal is dropped rather than failing the record (the
// URL + error are still worth keeping).
func (l *FailureLog) Record(kind, url string, params any, err error) {
	if l == nil {
		return
	}
	var raw json.RawMessage
	if params != nil {
		if data, marshalErr := json.Marshal(params); marshalErr == nil {
			raw = data
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, FailedFetch{
		Source:   l.source,
		Kind:     kind,
		URL:      url,
		Params:   raw,
		Error:    err.Error(),
		FailedAt: time.Now().UTC(),
	})
}

// Save writes the recorded failures to the log's path (creating parent
// directories as needed), replacing whatever a previous run left there —
// the file always reflects the most recent run, whether that was a full
// scan or a retry pass. When this run recorded zero failures, any stale
// file from a previous run is removed instead, so repeated retry passes
// converge to "no file" once everything has succeeded.
func (l *FailureLog) Save() error {
	if l == nil || l.path == "" {
		return nil
	}
	l.mu.Lock()
	items := append([]FailedFetch(nil), l.items...)
	l.mu.Unlock()

	if len(items) == 0 {
		if err := os.Remove(l.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove stale failure log %s: %w", l.path, err)
		}
		return nil
	}

	if dir := filepath.Dir(l.path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create failure log directory: %w", err)
		}
	}
	data, err := json.MarshalIndent(failedFetchFile{
		Source:   l.source,
		SavedAt:  time.Now().UTC(),
		Failures: items,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal failure log: %w", err)
	}
	// Write-then-rename so a crash mid-write can't leave a truncated JSON
	// file that a later -retry-failed run would refuse to parse.
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write failure log: %w", err)
	}
	if err := os.Rename(tmp, l.path); err != nil {
		return fmt.Errorf("rename failure log into place: %w", err)
	}
	log.Printf("%s: %d failed fetch(es) saved to %s — replay them with -retry-failed", l.source, len(items), l.path)
	return nil
}

// saveAndLog is the deferred form used at the end of every Run/RetryFailed:
// a failure log that can't be saved shouldn't mask the run's own result,
// so the error is logged rather than returned.
func (l *FailureLog) saveAndLog() {
	if err := l.Save(); err != nil {
		log.Printf("%s: saving failure log: %v", l.source, err)
	}
}

// LoadFailedFetches reads a failure log written by Save. A missing file
// is returned as os.ErrNotExist for the caller to treat as "nothing to
// retry" rather than a hard error.
func LoadFailedFetches(path string) ([]FailedFetch, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var file failedFetchFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("decode failure log %s: %w", path, err)
	}
	return file.Failures, nil
}
