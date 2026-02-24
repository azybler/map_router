package api

// RouteRequest is the JSON body for POST /api/v1/route.
type RouteRequest struct {
	Start LatLngJSON `json:"start"`
	End   LatLngJSON `json:"end"`
}

// LatLngJSON represents a lat/lng pair in JSON.
type LatLngJSON struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

// RouteResponse is the JSON response for a successful route query.
type RouteResponse struct {
	TotalDistanceMeters float64       `json:"total_distance_meters"`
	Segments            []SegmentJSON `json:"segments"`
}

// SegmentJSON represents a road segment in the response.
type SegmentJSON struct {
	DistanceMeters float64      `json:"distance_meters"`
	Geometry       []LatLngJSON `json:"geometry"`
}

// ErrorResponse is the JSON response for errors.
type ErrorResponse struct {
	Error          string  `json:"error"`
	Field          string  `json:"field,omitempty"`
	DistanceMeters float64 `json:"distance_meters,omitempty"`
}

// StatsResponse is the JSON response for GET /api/v1/stats.
type StatsResponse struct {
	NumNodes      uint32 `json:"num_nodes"`
	NumFwdEdges   int    `json:"num_fwd_edges"`
	NumBwdEdges   int    `json:"num_bwd_edges"`
}

// HealthResponse is the JSON response for GET /api/v1/health.
type HealthResponse struct {
	Status string `json:"status"`
}
