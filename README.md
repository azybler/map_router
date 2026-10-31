# map_router

A high-performance routing engine for road networks. Computes shortest paths using Contraction Hierarchies with bidirectional Dijkstra, serving queries over an HTTP API.

Loads OpenStreetMap data, builds an optimized graph, and answers route queries in milliseconds.

## How It Works

The system has two phases:

1. **Preprocess** — parse an OSM PBF file, build a road graph, run Contraction Hierarchies, and serialize to a binary file
2. **Serve** — load the binary graph and answer route queries via HTTP

## Getting Started

### Prerequisites

- Go 1.26+

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
- `--wmy` — filter to West (Peninsular) Malaysia bounding box (1.25,99.6 → 6.75,104.35)
- `--bbox lat_min,lng_min,lat_max,lng_max` — custom bounding box
- `--speeds` — JSON travel-time model for the time metric (see [Time model](#time-model)); built-in Malaysian priors when omitted
- `--distance` — weight edges by physical road length (shortest-**distance** routing) instead of travel time; ignores `--speeds`
- `--min-component N` — keep every strongly-connected road network with ≥ `N` nodes (`0` = largest only, default). Use a small value like `2` to retain disconnected networks such as islands (e.g. Tasmania)
- `--close-core yes|no|auto` — give the uncontracted CH core exact all-pairs shortcuts. Without it the query can miss the fastest route when it crosses the core (~5% of KL routes, up to ~20% slower). `auto` (default) closes it for the time metric only, which keeps `--distance` graphs byte-identical to earlier builds

The routing engine is metric-agnostic: it minimizes the sum of edge weights, so `--distance` yields shortest-distance routes with no changes to the server or query code. Reported distances always come from the path geometry, independent of the metric.

### Road access

A car-class way (`highway=motorway` … `service`, links and `living_street`)
is kept unless its access tags close it to cars. The most specific
transport-mode tag present decides, as OSM defines it: `motorcar`, then
`motor_vehicle`, then `vehicle`, then `access`. So `access=no` +
`motor_vehicle=yes` (a motor-vehicles-only flyover or link road) is open, and
`motorcar=no` (a motorcycle lane) is closed whatever `access` says.

| deciding value | the way is |
|----------------|------------|
| `no` | dropped |
| `private`, `permit`, `residents` | kept as restricted: reachable for the last mile, penalized as a cut-through |
| `destination`, `customers` | public on `access`, restricted on a mode tag |
| anything else | public |

### Time model

`speeds.json` sets each edge's travel time: a free-flow speed per highway class
(`class_kmh`; links run at `link_factor` × their parent class), posted
`maxspeed` tags scaled by `maxspeed_factor`, and per-class floors/caps.
`ref_factor` then scales the speed of a way whose `ref` tag lists one of its
keys. Its `delay_s` block adds the time lost at nodes, charged on entering the
node:

| key | charged when |
|-----|--------------|
| `junction_major` | entering a junction (3+ neighbours) on a road that outranks every other road there |
| `junction_equal` | … where the biggest other road ranks the same |
| `junction_minor` | … where a bigger road crosses or joins |
| `traffic_signal` | entering a node tagged `highway=traffic_signals` |
| `toll_booth` | entering a node tagged `barrier=toll_booth` |

The values are calibrated against Google's routes for real delivery trips
(`datasets/elevete_route_cache/`, gitignored).

### Australia (shortest distance, whole continent)

```sh
./run_server_au.sh
```

On first run this builds `graph.au.bin` via `refresh_graph_au.sh` (downloads the Geofabrik Australia extract, then preprocesses it with `--distance --min-component 2` — the whole continent including Tasmania and other road-connected islands), then serves it on port `8090`. Driving between disconnected landmasses (e.g. mainland → Tasmania) correctly returns `no_route_found`. No other `*.bin` file is read or modified.

### Run the Server

```sh
bin/map-router-server --graph graph.bin --port 8080
```

Flags:

- `--graph` — path to the preprocessed binary graph
- `--graph-distance` — optional distance-weighted graph binary; enables `metric: "distance"` routing (omit for time-only)
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
  "end": { "lat": 1.2903, "lng": 103.8515 },
  "metric": "distance"
}
```

`metric` is optional: `"time"` (default — lowest travel time) or `"distance"`
(true shortest road distance). `"distance"` requires the server to be started
with a distance graph (`--graph-distance`); otherwise it returns
`metric_unavailable`. Omitting the field is identical to `"time"`.

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
| 400 | `invalid_request` | Unknown `metric` (only `time`/`distance` are valid) |
| 400 | `metric_unavailable` | Requested metric's graph is not loaded on this server |

### Health

```
GET /api/v1/health
```

Returns `{"status": "ok"}`.

### Stats

```
GET /api/v1/stats
```

Returns node and edge counts for the time graph, plus `available_metrics`
(e.g. `["time","distance"]`) listing which metrics this server can route.

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
