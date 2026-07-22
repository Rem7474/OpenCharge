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

// realFreshmileTwoEvsePayload is a real /locations/{id} response (CM
// Annecy, id 199891) for a site with two identical evses, each exposing
// both a Type 2 and a domestic (EF) connector on the same physical charge
// point — real production data showing an evse's is_available applies to
// both connectors it exposes at once, not one independently of the other.
const realFreshmileTwoEvsePayload = `{"data":{"id":199891,"ref":"FRTCB002958","evses_available_count":2,"evses_total_count":2,"evses":[{"id":239824,"is_available":true,"connectors":[{"standard":"DOMESTIC_F"},{"standard":"IEC_62196_T2"}]},{"id":239825,"is_available":true,"connectors":[{"standard":"DOMESTIC_F"},{"standard":"IEC_62196_T2"}]}]}}`

// realFreshmileMixedKindPayload mirrors ingestion/freshmile_test.go's
// realFreshmileAnnecyMixedConnectorsPayload shape: one CCS-only evse plus
// several T2/EF evses at the same site, so a per-connector-type breakdown
// actually differs by type (unlike realFreshmileTwoEvsePayload above,
// where every evse exposes the same two types).
const realFreshmileMixedKindPayload = `{"data":{"id":829320,"evses_available_count":3,"evses_total_count":4,"evses":[{"id":1,"is_available":false,"connectors":[{"standard":"IEC_62196_T2_COMBO"}]},{"id":2,"is_available":true,"connectors":[{"standard":"IEC_62196_T2"}]},{"id":3,"is_available":true,"connectors":[{"standard":"DOMESTIC_F"},{"standard":"IEC_62196_T2"}]},{"id":4,"is_available":true,"connectors":[{"standard":"DOMESTIC_F"}]}]}}`

func TestFreshmileHandler_GetAvailability_TwoEvseBothConnectorsAvailable(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/199891" {
			t.Errorf("upstream path = %q, want /199891", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(realFreshmileTwoEvsePayload))
	}))
	defer upstream.Close()

	h := &FreshmileHandler{BaseURL: upstream.URL, client: upstream.Client()}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/freshmile/availability/199891", nil)
	newFreshmileRouter(h).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	want := `{"connectorAvailability":{"EF":{"available":2,"total":2},"T2":{"available":2,"total":2}},"evsesAvailableCount":2,"evsesTotalCount":2}` + "\n"
	if got := rr.Body.String(); got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestFreshmileHandler_GetAvailability_PerConnectorTypeBreakdownDiffers(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(realFreshmileMixedKindPayload))
	}))
	defer upstream.Close()

	h := &FreshmileHandler{BaseURL: upstream.URL, client: upstream.Client()}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/freshmile/availability/829320", nil)
	newFreshmileRouter(h).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	// CCS: 1 evse, unavailable. T2: appears on evse 2 (available) and evse
	// 3 (available) -> 2/2. EF: appears on evse 3 (available) and evse 4
	// (available) -> 2/2. The one *un*available evse (CCS-only) must not
	// drag down T2/EF's counts just because it shares the site.
	want := `{"connectorAvailability":{"CCS":{"available":0,"total":1},"EF":{"available":2,"total":2},"T2":{"available":2,"total":2}},"evsesAvailableCount":3,"evsesTotalCount":4}` + "\n"
	if got := rr.Body.String(); got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestFreshmileHandler_GetAvailability_NoEvses(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"id":3658,"evses":[]}}`))
	}))
	defer upstream.Close()

	h := &FreshmileHandler{BaseURL: upstream.URL, client: upstream.Client()}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/freshmile/availability/3658", nil)
	newFreshmileRouter(h).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	want := `{"connectorAvailability":{},"evsesAvailableCount":0,"evsesTotalCount":0}` + "\n"
	if got := rr.Body.String(); got != want {
		t.Errorf("body = %s, want %s", got, want)
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
