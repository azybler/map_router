# map_router

A high-performance routing engine for road networks. Computes shortest paths using Contraction Hierarchies with bidirectional Dijkstra, serving queries over an HTTP API.

Loads OpenStreetMap data, builds an optimized graph, and answers route queries in milliseconds.

## How It Works

The system has two phases:

1. **Preprocess** — parse an OSM PBF file, build a road graph, run Contraction Hierarchies, and serialize to a binary file
2. **Serve** — load the binary graph and answer route queries via HTTP

## Getting Started

### Prerequisites

- Go 1.25+

### Build

```sh
make build
```

This produces three binaries in `bin/`:

- `map-router-preprocess` — builds the graph from OSM data
- `map-router-server` — serves the HTTP API
- `map-router-visualize` — web UI for comparing routes

### Download OSM Data

```sh
make download-osm
```

Downloads the Malaysia/Singapore/Brunei extract from Geofabrik.

### Preprocess

```sh
bin/map-router-preprocess \
  --input malaysia-singapore-brunei-latest.osm.pbf \
  --output graph.bin \
  --singapore
```

Flags:

- `--input` — path to the `.osm.pbf` file
- `--output` — path for the binary graph output
- `--singapore` — filter to Singapore bounding box
- `--kl` — filter to Kuala Lumpur bounding box
- `--bbox lat_min,lng_min,lat_max,lng_max` — custom bounding box

### Run the Server

```sh
bin/map-router-server --graph graph.bin --port 8080
```

Flags:

- `--graph` — path to the preprocessed binary graph
- `--port` — HTTP port (default: 8080)
- `--cors-origin` — allowed CORS origin (optional)

## API

### Route

```
POST /api/v1/route
Content-Type: application/json
```

Request:

```json
{
  "start": { "lat": 1.3521, "lng": 103.8198 },
  "end": { "lat": 1.2903, "lng": 103.8515 }
}
```

Response:

```json
{
  "total_distance_meters": 12345.6,
  "segments": [
    {
      "distance_meters": 500.2,
      "geometry": [
        { "lat": 1.3521, "lng": 103.8198 },
        { "lat": 1.3450, "lng": 103.8250 }
      ]
    }
  ]
}
```

Errors:

| Status | Code | Description |
|--------|------|-------------|
| 400 | `invalid_request` | Malformed JSON or missing Content-Type |
| 400 | `invalid_coordinates` | Coordinates out of range or non-finite |
| 404 | `no_route_found` | No path between the two points |
| 422 | `point_too_far_from_road` | Start or end point is more than 500m from a road |

### Health

```
GET /api/v1/health
```

Returns `{"status": "ok"}`.

### Stats

```
GET /api/v1/stats
```

Returns node and edge counts for the loaded graph.

## Project Structure

```
cmd/
  preprocess/    OSM parsing, graph building, CH contraction
  server/        HTTP API server
  visualize/     Web UI for route comparison
pkg/
  osm/           OSM PBF parser (car-accessible roads)
  graph/         CSR graph data structure and binary serialization
  geo/           Haversine, equirectangular distance, point-to-segment
  ch/            Contraction Hierarchies preprocessing
  routing/       Snapping, CH Dijkstra, path unpacking
  api/           HTTP handlers and models
```

## Testing

```sh
make test          # run tests
make test-verbose  # verbose output
make bench         # benchmarks
make vet           # static analysis
```

## License

MIT
