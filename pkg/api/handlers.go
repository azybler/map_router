package api

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"

	"map_router/pkg/routing"
)

// Handlers holds the HTTP handlers and their dependencies.
type Handlers struct {
	router routing.Router
	stats  StatsResponse
	ready  bool
}

// NewHandlers creates handlers with the given router.
func NewHandlers(router routing.Router, stats StatsResponse) *Handlers {
	return &Handlers{
		router: router,
		stats:  stats,
		ready:  true,
	}
}

// Singapore bounding box.
const (
	sgMinLat = 1.15
	sgMaxLat = 1.48
	sgMinLng = 103.6
	sgMaxLng = 104.1
)

// HandleRoute handles POST /api/v1/route.
func (h *Handlers) HandleRoute(w http.ResponseWriter, r *http.Request) {
	// Enforce Content-Type.
	if ct := r.Header.Get("Content-Type"); ct != "application/json" {
		writeError(w, http.StatusBadRequest, "invalid_request", "Content-Type must be application/json", "", 0)
		return
	}

	// Parse request.
	var req RouteRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "", "", 0)
		return
	}

	// Validate coordinates.
	if err := validateCoord(req.Start, "start"); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_coordinates", err.Error(), "start", 0)
		return
	}
	if err := validateCoord(req.End, "end"); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_coordinates", err.Error(), "end", 0)
		return
	}

	// Route.
	result, err := h.router.Route(r.Context(), routing.LatLng{Lat: req.Start.Lat, Lng: req.Start.Lng}, routing.LatLng{Lat: req.End.Lat, Lng: req.End.Lng})
	if err != nil {
		if errors.Is(err, routing.ErrPointTooFar) {
			writeError(w, http.StatusUnprocessableEntity, "point_too_far_from_road", "", "", 0)
			return
		}
		if errors.Is(err, routing.ErrNoRoute) {
			writeError(w, http.StatusNotFound, "no_route_found", "", "", 0)
			return
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			writeError(w, http.StatusServiceUnavailable, "request_timeout", "", "", 0)
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "", "", 0)
		return
	}

	// Build response.
	resp := RouteResponse{
		TotalDistanceMeters: result.TotalDistanceMeters,
	}
	for _, seg := range result.Segments {
		geom := make([]LatLngJSON, len(seg.Geometry))
		for i, ll := range seg.Geometry {
			geom[i] = LatLngJSON{Lat: ll.Lat, Lng: ll.Lng}
		}
		resp.Segments = append(resp.Segments, SegmentJSON{
			DistanceMeters: seg.DistanceMeters,
			Geometry:       geom,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// HandleHealth handles GET /api/v1/health.
func (h *Handlers) HandleHealth(w http.ResponseWriter, r *http.Request) {
	resp := HealthResponse{Status: "ok"}
	if !h.ready {
		resp.Status = "loading"
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// HandleStats handles GET /api/v1/stats.
func (h *Handlers) HandleStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(h.stats)
}

func validateCoord(ll LatLngJSON, field string) error {
	if math.IsNaN(ll.Lat) || math.IsNaN(ll.Lng) || math.IsInf(ll.Lat, 0) || math.IsInf(ll.Lng, 0) {
		return errors.New("coordinates must be finite numbers")
	}
	if ll.Lat < sgMinLat || ll.Lat > sgMaxLat || ll.Lng < sgMinLng || ll.Lng > sgMaxLng {
		return errors.New("coordinates outside Singapore bounding box")
	}
	return nil
}

func writeError(w http.ResponseWriter, status int, code, message, field string, dist float64) {
	resp := ErrorResponse{
		Error: code,
		Field: field,
	}
	if dist > 0 {
		resp.DistanceMeters = dist
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp)
}
