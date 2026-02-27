package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

//go:embed static
var staticFiles embed.FS

type latlng struct {
	Lat float64 `json:"lat"`
	Lng float64 `json:"lng"`
}

type compareRequest struct {
	Start latlng `json:"start"`
	End   latlng `json:"end"`
}

type routeResult struct {
	DistanceMeters float64     `json:"distance_meters"`
	LatencyMs      int64       `json:"latency_ms"`
	Geometry       [][]float64 `json:"geometry"` // [[lat, lng], ...]
	Error          string      `json:"error,omitempty"`
}

type compareResponse struct {
	MapRouter routeResult `json:"map_router"`
	ORS       routeResult `json:"ors"`
	Google    routeResult `json:"google"`
}

var (
	routerURL    string
	orsAPIKey    string
	googleAPIKey string
	httpClient   = &http.Client{Timeout: 15 * time.Second}
)

func main() {
	port := flag.Int("port", 3000, "HTTP port to serve on")
	flag.StringVar(&routerURL, "router-url", "http://localhost:8091", "map_router backend URL")
	flag.Parse()

	orsAPIKey = os.Getenv("ORS_API_KEY")
	if orsAPIKey == "" {
		log.Println("WARNING: ORS_API_KEY not set; ORS comparison will be unavailable")
	}

	googleAPIKey = os.Getenv("GOOGLE_API_KEY")
	if googleAPIKey == "" {
		log.Println("WARNING: GOOGLE_API_KEY not set; Google comparison will be unavailable")
	}

	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(staticFS)))
	mux.HandleFunc("/api/compare", handleCompare)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("Visualize server starting on http://localhost:%d", *port)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func handleCompare(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req compareRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	var resp compareResponse
	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		resp.MapRouter = queryMapRouter(req)
	}()

	go func() {
		defer wg.Done()
		resp.ORS = queryORS(req)
	}()

	go func() {
		defer wg.Done()
		resp.Google = queryGoogle(req)
	}()

	wg.Wait()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func queryMapRouter(req compareRequest) routeResult {
	start := time.Now()
	body, _ := json.Marshal(map[string]latlng{
		"start": req.Start,
		"end":   req.End,
	})

	resp, err := httpClient.Post(routerURL+"/api/v1/route", "application/json", bytes.NewReader(body))
	if err != nil {
		return routeResult{Error: fmt.Sprintf("request failed: %v", err)}
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return routeResult{Error: fmt.Sprintf("read failed: %v", err)}
	}

	if resp.StatusCode != http.StatusOK {
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &errResp) == nil && errResp.Error != "" {
			return routeResult{Error: errResp.Error}
		}
		return routeResult{Error: fmt.Sprintf("HTTP %d", resp.StatusCode)}
	}

	var routeResp struct {
		TotalDistanceMeters float64 `json:"total_distance_meters"`
		Segments            []struct {
			Geometry []latlng `json:"geometry"`
		} `json:"segments"`
	}
	if err := json.Unmarshal(data, &routeResp); err != nil {
		return routeResult{Error: fmt.Sprintf("decode failed: %v", err)}
	}

	var geometry [][]float64
	for _, seg := range routeResp.Segments {
		for _, pt := range seg.Geometry {
			geometry = append(geometry, []float64{pt.Lat, pt.Lng})
		}
	}

	return routeResult{
		DistanceMeters: routeResp.TotalDistanceMeters,
		LatencyMs:      time.Since(start).Milliseconds(),
		Geometry:       geometry,
	}
}

func queryORS(req compareRequest) routeResult {
	start := time.Now()
	if orsAPIKey == "" {
		return routeResult{Error: "ORS_API_KEY not configured"}
	}

	// ORS uses [lng, lat] order
	body, _ := json.Marshal(map[string]any{
		"coordinates": [][]float64{
			{req.Start.Lng, req.Start.Lat},
			{req.End.Lng, req.End.Lat},
		},
	})

	orsReq, _ := http.NewRequest(http.MethodPost,
		"https://api.openrouteservice.org/v2/directions/driving-car/geojson",
		bytes.NewReader(body))
	orsReq.Header.Set("Content-Type", "application/json")
	orsReq.Header.Set("Authorization", orsAPIKey)

	resp, err := httpClient.Do(orsReq)
	if err != nil {
		return routeResult{Error: fmt.Sprintf("request failed: %v", err)}
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return routeResult{Error: fmt.Sprintf("read failed: %v", err)}
	}

	if resp.StatusCode != http.StatusOK {
		return routeResult{Error: fmt.Sprintf("HTTP %d: %s", resp.StatusCode, truncate(string(data), 200))}
	}

	var orsResp struct {
		Features []struct {
			Properties struct {
				Summary struct {
					Distance float64 `json:"distance"`
				} `json:"summary"`
			} `json:"properties"`
			Geometry struct {
				Coordinates [][]float64 `json:"coordinates"` // [lng, lat]
			} `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(data, &orsResp); err != nil {
		return routeResult{Error: fmt.Sprintf("decode failed: %v", err)}
	}

	if len(orsResp.Features) == 0 {
		return routeResult{Error: "no route found"}
	}

	feat := orsResp.Features[0]
	geometry := make([][]float64, len(feat.Geometry.Coordinates))
	for i, coord := range feat.Geometry.Coordinates {
		// Convert [lng, lat] → [lat, lng]
		geometry[i] = []float64{coord[1], coord[0]}
	}

	return routeResult{
		DistanceMeters: feat.Properties.Summary.Distance,
		LatencyMs:      time.Since(start).Milliseconds(),
		Geometry:       geometry,
	}
}

func queryGoogle(req compareRequest) routeResult {
	start := time.Now()
	if googleAPIKey == "" {
		return routeResult{Error: "GOOGLE_API_KEY not configured"}
	}

	url := fmt.Sprintf(
		"https://maps.googleapis.com/maps/api/directions/json?origin=%f,%f&destination=%f,%f&key=%s",
		req.Start.Lat, req.Start.Lng, req.End.Lat, req.End.Lng, googleAPIKey,
	)

	resp, err := httpClient.Get(url)
	if err != nil {
		return routeResult{Error: fmt.Sprintf("request failed: %v", err)}
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return routeResult{Error: fmt.Sprintf("read failed: %v", err)}
	}

	var gResp struct {
		Status string `json:"status"`
		Routes []struct {
			Legs []struct {
				Distance struct {
					Value float64 `json:"value"`
				} `json:"distance"`
				Steps []struct {
					Polyline struct {
						Points string `json:"points"`
					} `json:"polyline"`
				} `json:"steps"`
			} `json:"legs"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(data, &gResp); err != nil {
		return routeResult{Error: fmt.Sprintf("decode failed: %v", err)}
	}

	if gResp.Status != "OK" {
		return routeResult{Error: fmt.Sprintf("Google API: %s", gResp.Status)}
	}
	if len(gResp.Routes) == 0 || len(gResp.Routes[0].Legs) == 0 {
		return routeResult{Error: "no route found"}
	}

	leg := gResp.Routes[0].Legs[0]
	var geometry [][]float64
	for _, step := range leg.Steps {
		points := decodePolyline(step.Polyline.Points)
		geometry = append(geometry, points...)
	}

	return routeResult{
		DistanceMeters: leg.Distance.Value,
		LatencyMs:      time.Since(start).Milliseconds(),
		Geometry:       geometry,
	}
}

// decodePolyline decodes a Google encoded polyline string into [[lat, lng], ...].
func decodePolyline(encoded string) [][]float64 {
	var points [][]float64
	lat, lng := 0, 0
	i := 0
	for i < len(encoded) {
		// Decode latitude.
		shift, result := uint(0), 0
		for {
			b := int(encoded[i]) - 63
			i++
			result |= (b & 0x1f) << shift
			shift += 5
			if b < 0x20 {
				break
			}
		}
		if result&1 != 0 {
			lat += ^(result >> 1)
		} else {
			lat += result >> 1
		}

		// Decode longitude.
		shift, result = 0, 0
		for {
			b := int(encoded[i]) - 63
			i++
			result |= (b & 0x1f) << shift
			shift += 5
			if b < 0x20 {
				break
			}
		}
		if result&1 != 0 {
			lng += ^(result >> 1)
		} else {
			lng += result >> 1
		}

		points = append(points, []float64{float64(lat) / 1e5, float64(lng) / 1e5})
	}
	return points
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
