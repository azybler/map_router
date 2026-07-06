package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/azybler/map_router/pkg/routing"
)

// mockRouter implements routing.Router for testing.
type mockRouter struct {
	result *routing.RouteResult
	err    error
}

func (m *mockRouter) Route(ctx context.Context, start, end routing.LatLng) (*routing.RouteResult, error) {
	return m.result, m.err
}

func TestHandleRoute_Success(t *testing.T) {
	mock := &mockRouter{
		result: &routing.RouteResult{
			TotalDistanceMeters: 1234.5,
			Segments: []routing.Segment{
				{
					DistanceMeters: 1234.5,
					Geometry: []routing.LatLng{
						{Lat: 1.3, Lng: 103.8},
						{Lat: 1.35, Lng: 103.85},
					},
				},
			},
		},
	}
	h := NewHandlers(mock, StatsResponse{NumNodes: 100})

	body := `{"start":{"lat":1.3,"lng":103.8},"end":{"lat":1.35,"lng":103.85}}`
	req := httptest.NewRequest("POST", "/api/v1/route", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleRoute(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body: %s", w.Code, w.Body.String())
	}

	var resp RouteResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.TotalDistanceMeters != 1234.5 {
		t.Errorf("TotalDistanceMeters = %f, want 1234.5", resp.TotalDistanceMeters)
	}
	if len(resp.Segments) != 1 {
		t.Errorf("Segments length = %d, want 1", len(resp.Segments))
	}
}

func TestHandleRoute_InvalidJSON(t *testing.T) {
	h := NewHandlers(&mockRouter{}, StatsResponse{})

	req := httptest.NewRequest("POST", "/api/v1/route", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleRoute(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleRoute_MissingContentType(t *testing.T) {
	h := NewHandlers(&mockRouter{}, StatsResponse{})

	body := `{"start":{"lat":1.3,"lng":103.8},"end":{"lat":1.35,"lng":103.85}}`
	req := httptest.NewRequest("POST", "/api/v1/route", strings.NewReader(body))
	w := httptest.NewRecorder()

	h.HandleRoute(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleRoute_OutOfBounds(t *testing.T) {
	h := NewHandlers(&mockRouter{}, StatsResponse{})

	// Latitude out of valid range (-90 to 90).
	body := `{"start":{"lat":91.0,"lng":103.8},"end":{"lat":1.35,"lng":103.85}}`
	req := httptest.NewRequest("POST", "/api/v1/route", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleRoute(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestHandleRoute_NoRoute(t *testing.T) {
	mock := &mockRouter{err: routing.ErrNoRoute}
	h := NewHandlers(mock, StatsResponse{})

	body := `{"start":{"lat":1.3,"lng":103.8},"end":{"lat":1.35,"lng":103.85}}`
	req := httptest.NewRequest("POST", "/api/v1/route", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleRoute(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestHandleRoute_PointTooFar(t *testing.T) {
	mock := &mockRouter{err: routing.ErrPointTooFar}
	h := NewHandlers(mock, StatsResponse{})

	body := `{"start":{"lat":1.3,"lng":103.8},"end":{"lat":1.35,"lng":103.85}}`
	req := httptest.NewRequest("POST", "/api/v1/route", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.HandleRoute(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", w.Code)
	}
}

func TestHandleHealth(t *testing.T) {
	h := NewHandlers(&mockRouter{}, StatsResponse{})

	req := httptest.NewRequest("GET", "/api/v1/health", nil)
	w := httptest.NewRecorder()

	h.HandleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var resp HealthResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Status != "ok" {
		t.Errorf("status = %q, want 'ok'", resp.Status)
	}
}

func TestHandleStats(t *testing.T) {
	stats := StatsResponse{NumNodes: 500000, NumFwdEdges: 1000000, NumBwdEdges: 900000}
	h := NewHandlers(&mockRouter{}, stats)

	req := httptest.NewRequest("GET", "/api/v1/stats", nil)
	w := httptest.NewRecorder()

	h.HandleStats(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}

	var resp StatsResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.NumNodes != 500000 {
		t.Errorf("NumNodes = %d, want 500000", resp.NumNodes)
	}
}

// routeResult builds a RouteResult whose distance identifies which router ran.
func routeResult(dist float64) *routing.RouteResult {
	return &routing.RouteResult{
		TotalDistanceMeters: dist,
		Segments: []routing.Segment{
			{DistanceMeters: dist, Geometry: []routing.LatLng{{Lat: 1, Lng: 2}}},
		},
	}
}

func postRoute(t *testing.T, h *Handlers, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/route", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.HandleRoute(w, req)
	return w
}

func TestHandleRoute_MetricDefaultsToTime(t *testing.T) {
	h := NewHandlersMulti(map[string]routing.Router{
		MetricTime:     &mockRouter{result: routeResult(111)},
		MetricDistance: &mockRouter{result: routeResult(222)},
	}, StatsResponse{})

	w := postRoute(t, h, `{"start":{"lat":1.3,"lng":103.8},"end":{"lat":1.35,"lng":103.85}}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body: %s", w.Code, w.Body.String())
	}
	var resp RouteResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.TotalDistanceMeters != 111 {
		t.Errorf("default metric used wrong router: got %v, want time (111)", resp.TotalDistanceMeters)
	}
}

func TestHandleRoute_MetricTimeExplicit(t *testing.T) {
	h := NewHandlersMulti(map[string]routing.Router{
		MetricTime:     &mockRouter{result: routeResult(111)},
		MetricDistance: &mockRouter{result: routeResult(222)},
	}, StatsResponse{})

	w := postRoute(t, h, `{"start":{"lat":1.3,"lng":103.8},"end":{"lat":1.35,"lng":103.85},"metric":"time"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body: %s", w.Code, w.Body.String())
	}
	var resp RouteResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.TotalDistanceMeters != 111 {
		t.Errorf("metric=time used wrong router: got %v, want 111", resp.TotalDistanceMeters)
	}
}

func TestHandleRoute_MetricDistanceUsesDistanceRouter(t *testing.T) {
	h := NewHandlersMulti(map[string]routing.Router{
		MetricTime:     &mockRouter{result: routeResult(111)},
		MetricDistance: &mockRouter{result: routeResult(222)},
	}, StatsResponse{})

	w := postRoute(t, h, `{"start":{"lat":1.3,"lng":103.8},"end":{"lat":1.35,"lng":103.85},"metric":"distance"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. body: %s", w.Code, w.Body.String())
	}
	var resp RouteResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.TotalDistanceMeters != 222 {
		t.Errorf("metric=distance used wrong router: got %v, want distance (222)", resp.TotalDistanceMeters)
	}
}

func TestHandleRoute_MetricUnavailable(t *testing.T) {
	// Time-only handler (as when started without --graph-distance).
	h := NewHandlers(&mockRouter{result: routeResult(111)}, StatsResponse{})

	w := postRoute(t, h, `{"start":{"lat":1.3,"lng":103.8},"end":{"lat":1.35,"lng":103.85},"metric":"distance"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var e ErrorResponse
	json.Unmarshal(w.Body.Bytes(), &e)
	if e.Error != "metric_unavailable" || e.Field != "metric" {
		t.Errorf("error = %q field = %q, want metric_unavailable/metric", e.Error, e.Field)
	}
}

func TestHandleRoute_MetricInvalid(t *testing.T) {
	h := NewHandlers(&mockRouter{result: routeResult(111)}, StatsResponse{})

	w := postRoute(t, h, `{"start":{"lat":1.3,"lng":103.8},"end":{"lat":1.35,"lng":103.85},"metric":"walking"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	var e ErrorResponse
	json.Unmarshal(w.Body.Bytes(), &e)
	if e.Error != "invalid_request" || e.Field != "metric" {
		t.Errorf("error = %q field = %q, want invalid_request/metric", e.Error, e.Field)
	}
}

func TestHandleStats_AvailableMetrics(t *testing.T) {
	h := NewHandlers(&mockRouter{}, StatsResponse{AvailableMetrics: []string{"time", "distance"}})

	req := httptest.NewRequest("GET", "/api/v1/stats", nil)
	w := httptest.NewRecorder()
	h.HandleStats(w, req)

	var resp StatsResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.AvailableMetrics) != 2 || resp.AvailableMetrics[0] != "time" || resp.AvailableMetrics[1] != "distance" {
		t.Errorf("available_metrics = %v, want [time distance]", resp.AvailableMetrics)
	}
}
