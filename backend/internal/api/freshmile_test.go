package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func newFreshmileRouter(h *FreshmileHandler) http.Handler {
	r := chi.NewRouter()
	r.Get("/freshmile/availability/{locationId}", h.GetAvailability)
	return r
}

func TestFreshmileHandler_GetAvailability_Available(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/829320" {
			t.Errorf("upstream path = %q, want /829320", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":829320,"is_available":true}}`))
	}))
	defer upstream.Close()

	h := &FreshmileHandler{BaseURL: upstream.URL, client: upstream.Client()}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/freshmile/availability/829320", nil)
	newFreshmileRouter(h).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Body.String(); got != `{"isAvailable":true}`+"\n" {
		t.Errorf("body = %q, want {\"isAvailable\":true}", got)
	}
}

func TestFreshmileHandler_GetAvailability_Unavailable(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"id":3658,"is_available":false}}`))
	}))
	defer upstream.Close()

	h := &FreshmileHandler{BaseURL: upstream.URL, client: upstream.Client()}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/freshmile/availability/3658", nil)
	newFreshmileRouter(h).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Body.String(); got != `{"isAvailable":false}`+"\n" {
		t.Errorf("body = %q, want {\"isAvailable\":false}", got)
	}
}

func TestFreshmileHandler_GetAvailability_InvalidLocationID(t *testing.T) {
	h := &FreshmileHandler{BaseURL: "http://unused.invalid", client: http.DefaultClient}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/freshmile/availability/not-a-number", nil)
	newFreshmileRouter(h).ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestFreshmileHandler_GetAvailability_UpstreamNotFound(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()

	h := &FreshmileHandler{BaseURL: upstream.URL, client: upstream.Client()}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/freshmile/availability/1", nil)
	newFreshmileRouter(h).ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestFreshmileHandler_GetAvailability_UpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer upstream.Close()

	h := &FreshmileHandler{BaseURL: upstream.URL, client: upstream.Client()}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/freshmile/availability/1", nil)
	newFreshmileRouter(h).ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rr.Code)
	}
}
