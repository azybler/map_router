package api

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"mime"
	"net/http"

	"github.com/azybler/map_router/pkg/routing"
)

// Handlers holds the HTTP handlers and their dependencies.
type Handlers struct {
	router routing.Router
	stats  StatsResponse
}

// NewHandlers creates handlers with the given router.
func NewHandlers(router routing.Router, stats StatsResponse) *Handlers {
	return &Handlers{
		router: router,
		stats:  stats,
	}
}


// HandleRoute handles POST /api/v1/route.
func (h *Handlers) HandleRoute(w http.ResponseWriter, r *http.Request) {
	// Enforce Content-Type.
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != "application/json" {
		writeError(w, http.StatusBadRequest, "invalid_request", "")
		return
	}

	// Parse request.
	var req RouteRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "")
		return
	}

	// Validate coordinates.
	if err := validateCoord(req.Start); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_coordinates", "start")
		return
	}
	if err := validateCoord(req.End); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_coordinates", "end")
		return
	}

	// Route.
	result, err := h.router.Route(r.Context(), routing.LatLng{Lat: req.Start.Lat, Lng: req.Start.Lng}, routing.LatLng{Lat: req.End.Lat, Lng: req.End.Lng})
	if err != nil {
		if errors.Is(err, routing.ErrPointTooFar) {
			writeError(w, http.StatusUnprocessableEntity, "point_too_far_from_road", "")
			return
		}
		if errors.Is(err, routing.ErrNoRoute) {
			writeError(w, http.StatusNotFound, "no_route_found", "")
			return
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			writeError(w, http.StatusServiceUnavailable, "request_timeout", "")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "")
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
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(HealthResponse{Status: "ok"})
}

// HandleStats handles GET /api/v1/stats.
func (h *Handlers) HandleStats(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(h.stats)
}

func validateCoord(ll LatLngJSON) error {
	if math.IsNaN(ll.Lat) || math.IsNaN(ll.Lng) || math.IsInf(ll.Lat, 0) || math.IsInf(ll.Lng, 0) {
		return errors.New("coordinates must be finite numbers")
	}
	if ll.Lat < -90 || ll.Lat > 90 || ll.Lng < -180 || ll.Lng > 180 {
		return errors.New("coordinates out of range")
	}
	return nil
}

func writeError(w http.ResponseWriter, status int, code, field string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(ErrorResponse{Error: code, Field: field})
}
