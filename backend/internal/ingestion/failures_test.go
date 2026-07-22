package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFailureLogRecordSaveLoadRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "izivia.json")
	log := NewFailureLog(path, "izivia")

	log.Record(failKindIziviaSquare, "https://example.com/map/markers",
		iziviaSquare{CenterLng: 2.2, CenterLat: 46.2, Zoom: 7}, errors.New("http 504"))
	log.Record(failKindIziviaStation, "https://example.com/charging-locations/abc",
		map[string]any{"id": "abc", "lat": 45.0, "lng": 3.0}, errors.New("http 500"))

	if err := log.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	failures, err := LoadFailedFetches(path)
	if err != nil {
		t.Fatalf("LoadFailedFetches: %v", err)
	}
	if len(failures) != 2 {
		t.Fatalf("got %d failures, want 2", len(failures))
	}

	if failures[0].Source != "izivia" || failures[0].Kind != failKindIziviaSquare || failures[0].Error != "http 504" {
		t.Errorf("first failure = %+v, want izivia/%s/http 504", failures[0], failKindIziviaSquare)
	}
	var square iziviaSquare
	if err := json.Unmarshal(failures[0].Params, &square); err != nil {
		t.Fatalf("decode square params: %v", err)
	}
	if square.CenterLng != 2.2 || square.CenterLat != 46.2 || square.Zoom != 7 {
		t.Errorf("square = %+v, want centerLng=2.2 centerLat=46.2 zoom=7", square)
	}

	var marker map[string]any
	if err := json.Unmarshal(failures[1].Params, &marker); err != nil {
		t.Fatalf("decode marker params: %v", err)
	}
	if marker["id"] != "abc" {
		t.Errorf("marker id = %v, want abc", marker["id"])
	}
}

// TestFailureLogSaveWithoutFailuresRemovesStaleFile pins the convergence
// behavior retry passes rely on: once a run (full or retry) records zero
// failures, the file left by a previous run must go away, otherwise
// -retry-failed would keep replaying long-since-recovered URLs forever.
func TestFailureLogSaveWithoutFailuresRemovesStaleFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tesla.json")
	if err := os.WriteFile(path, []byte(`{"source":"tesla","failures":[]}`), 0o644); err != nil {
		t.Fatalf("seed stale file: %v", err)
	}

	if err := NewFailureLog(path, "tesla").Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stale failure file still exists (stat err = %v), want removed", err)
	}
}

// A nil *FailureLog must be a safe no-op: ingesters record/save
// unconditionally, and tests (or callers that don't care about retry
// files) leave the Failures field unset.
func TestNilFailureLogIsNoOp(t *testing.T) {
	var log *FailureLog
	log.Record("kind", "url", nil, errors.New("boom"))
	if err := log.Save(); err != nil {
		t.Errorf("nil Save() = %v, want nil", err)
	}
	log.saveAndLog()
}

// TestFreshmileRetryFailedReRecordsStillFailingLocations exercises a
// whole retry pass end-to-end (minus the database, which never gets
// touched since nothing fetches successfully): a location recorded as
// failed is re-fetched, fails again, and ends up in the freshly-saved
// failure file so the next -retry-failed pass still sees it.
func TestFreshmileRetryFailedReRecordsStillFailingLocations(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/locations/") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_, _ = fmt.Fprint(w, `{"features": []}`)
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "freshmile.json")
	ing := NewFreshmileIngester(nil, nil, nil, nil, srv.URL, FreshmileConfig{Workers: 2})
	ing.retryBackoff = time.Millisecond
	ing.Failures = NewFailureLog(path, "freshmile")

	failures := []FailedFetch{{
		Source: "freshmile",
		Kind:   failKindFreshmileLocation,
		Params: json.RawMessage(`{"id": 42}`),
		Error:  "http 504",
	}}
	processed, err := ing.RetryFailed(context.Background(), failures)
	if err != nil {
		t.Fatalf("RetryFailed: %v", err)
	}
	if processed != 0 {
		t.Errorf("processed = %d, want 0 (the only location still fails)", processed)
	}

	saved, err := LoadFailedFetches(path)
	if err != nil {
		t.Fatalf("LoadFailedFetches after retry: %v", err)
	}
	if len(saved) != 1 {
		t.Fatalf("got %d re-recorded failures, want 1", len(saved))
	}
	if saved[0].Kind != failKindFreshmileLocation {
		t.Errorf("re-recorded kind = %q, want %q", saved[0].Kind, failKindFreshmileLocation)
	}
	var params struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(saved[0].Params, &params); err != nil || params.ID != 42 {
		t.Errorf("re-recorded params = %s (decode err %v), want id 42", saved[0].Params, err)
	}
	if !strings.Contains(saved[0].Error, "404") {
		t.Errorf("re-recorded error = %q, want the new 404, not the original 504", saved[0].Error)
	}
}

// TestFreshmileRetryFailedRemovesFileOnceEverythingSucceeds is the happy
// half of the convergence contract: when every retried tile scan comes
// back clean (here: tiles that yield no locations at all, so the
// database is never needed), the failure file from the previous run is
// gone afterwards.
func TestFreshmileRetryFailedRemovesFileOnceEverythingSucceeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"features": []}`)
	}))
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "freshmile.json")
	if err := os.WriteFile(path, []byte(`{"source":"freshmile","failures":[]}`), 0o644); err != nil {
		t.Fatalf("seed stale file: %v", err)
	}

	ing := NewFreshmileIngester(nil, nil, nil, nil, srv.URL, FreshmileConfig{Workers: 2})
	ing.retryBackoff = time.Millisecond
	ing.Failures = NewFailureLog(path, "freshmile")

	failures := []FailedFetch{{
		Source: "freshmile",
		Kind:   failKindFreshmileTile,
		Params: json.RawMessage(`{"minLng": 2, "minLat": 46, "maxLng": 4, "maxLat": 48}`),
		Error:  "http 504",
	}}
	if _, err := ing.RetryFailed(context.Background(), failures); err != nil {
		t.Fatalf("RetryFailed: %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("failure file still exists after a clean retry (stat err = %v), want removed", err)
	}
}
