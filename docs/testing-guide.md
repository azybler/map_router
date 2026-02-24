# Testing Guide

## Unit Tests

Run all unit tests:

```bash
make test
```

Verbose output:

```bash
make test-verbose
```

## Benchmarks

```bash
make bench
```

This benchmarks the geo, routing, and CH packages.

## Static Analysis

```bash
make vet
```

## End-to-End Testing with Singapore Data

### 1. Download Singapore OSM Extract

Download the latest Singapore extract from Geofabrik:

```bash
wget https://download.geofabrik.de/asia/malaysia-singapore-brunei-latest.osm.pbf -O singapore.osm.pbf
```

The file is approximately 100-150 MB.

### 2. Build Binaries

```bash
make build
```

This produces `bin/preprocess` and `bin/server`.

### 3. Preprocess the Graph

```bash
bin/preprocess --input singapore.osm.pbf --output graph.bin
```

The preprocessor will:

1. Parse the `.osm.pbf` file (two-pass: ways then nodes)
2. Build a CSR graph from the parsed edges
3. Extract the largest connected component (removes islands/ferries)
4. Run Contraction Hierarchies preprocessing
5. Serialize the CH graph to `graph.bin`

Expected output:

```
Parsing OSM file singapore.osm.pbf...
Parsed: XXXXX edges, XXXXX nodes
Building graph...
Built graph: XXXXX nodes, XXXXX edges
Extracting largest connected component...
Largest component: XXXXX nodes (XX.X%)
Contracting graph...
Contraction complete: XXXXX shortcuts created
Writing graph to graph.bin...
Done. File size: XX MB
```

### 4. Start the Server

```bash
bin/server --graph graph.bin --port 8080
```

Optional flags:

- `--cors-origin http://localhost:3000` — enable CORS for a frontend

### 5. Test Queries

Health check:

```bash
curl http://localhost:8080/api/v1/health
```

Expected: `{"status":"ok"}`

Graph stats:

```bash
curl http://localhost:8080/api/v1/stats
```

Expected: `{"num_nodes":...,"num_fwd_edges":...,"num_bwd_edges":...}`

Route query (Marina Bay Sands to Changi Airport):

```bash
curl -s -X POST http://localhost:8080/api/v1/route \
  -H 'Content-Type: application/json' \
  -d '{
    "start": {"lat": 1.2838, "lng": 103.8591},
    "end": {"lat": 1.3644, "lng": 103.9915}
  }' | python3 -m json.tool
```

Route query (Orchard Road to Sentosa):

```bash
curl -s -X POST http://localhost:8080/api/v1/route \
  -H 'Content-Type: application/json' \
  -d '{
    "start": {"lat": 1.3048, "lng": 103.8318},
    "end": {"lat": 1.2494, "lng": 103.8303}
  }' | python3 -m json.tool
```

### 6. Expected Error Responses

Coordinates outside Singapore:

```bash
curl -s -X POST http://localhost:8080/api/v1/route \
  -H 'Content-Type: application/json' \
  -d '{"start": {"lat": 51.5, "lng": -0.1}, "end": {"lat": 1.35, "lng": 103.85}}'
```

Returns HTTP 400: `{"error":"invalid_coordinates","field":"start"}`

Point too far from any road:

Returns HTTP 422: `{"error":"point_too_far_from_road"}`

No route found (disconnected points):

Returns HTTP 404: `{"error":"no_route_found"}`

Missing Content-Type header:

```bash
curl -s -X POST http://localhost:8080/api/v1/route \
  -d '{"start": {"lat": 1.3, "lng": 103.8}, "end": {"lat": 1.35, "lng": 103.85}}'
```

Returns HTTP 400: `{"error":"invalid_request"}`
