# Routing Service with Contraction Hierarchies

**Date:** 2026-02-24
**Status:** Brainstorm

## What We're Building

A Golang routing service that loads OpenStreetMap data for Singapore, builds an in-memory weighted directed graph of the road network, and uses Contraction Hierarchies (CH) for fast shortest-path queries. The service exposes both a Go library and an HTTP API.

**Input:** Two lat/lng coordinates (start and end points), snapped to the nearest positions on the road network.

**Output:** An ordered sequence of road segments from start to end, including full geometry (lat/lng polyline) for map rendering.

### Core Concepts

- **Graph nodes:** OSM nodes at road intersections and endpoints.
- **Graph edges (segments):** Straight-line road sections between nodes. A curvy road is many short segments connected in sequence.
- **Point on road:** Represented as a segment ID + ratio (0.0 to 1.0 along the segment). Input lat/lng coordinates are snapped to the nearest such point.
- **Edge weights:** Physical distance in meters (derived from node coordinates using Haversine formula).
- **Directed graph:** One-way streets produce a single directed edge; two-way streets produce edges in both directions.
- **Contraction Hierarchies:** A preprocessing technique that adds "shortcut" edges to speed up Dijkstra queries from O(n) to O(sqrt(n) * log(n)).

## Why This Approach

### Graph Representation: CSR (Compressed Sparse Row) Flat Arrays

We chose flat arrays (CSR format) over maps/structs because:

- **Cache-friendly:** Contiguous memory layout means fast sequential access during graph traversal, critical for CH bidirectional Dijkstra.
- **Minimal memory:** No map overhead or pointer indirection. Singapore's road network (~500K nodes, ~1M edges) fits compactly.
- **Industry standard:** OSRM, Valhalla, and other production routing engines use this pattern.
- **Matches our workflow:** Graph is built once during preprocessing, then loaded read-only for queries. CSR is ideal for static graphs.

### Two-Phase Architecture: Preprocess + Serve

1. **Preprocessor CLI** (`cmd/preprocess`): Reads `.osm.pbf` -> builds graph -> computes CH -> serializes to binary file.
2. **Server** (`cmd/server`): Loads pre-built binary -> serves HTTP route queries.

This separates the expensive one-time work (minutes) from the fast query path (milliseconds).

## Key Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Language | Go | User requirement |
| Interface | HTTP API + Go library | Core logic importable; thin HTTP layer on top |
| Region | Singapore | Manageable size (~500K nodes) for initial development |
| Edge weights | Distance (meters) | Simplest; derived from coordinates via Haversine |
| Vehicle mode | Car only | Filter for motorable roads; respect one-way streets |
| Input format | Lat/Lng coordinates | Snapped to nearest road segment internally |
| Output format | Segment IDs + geometry | Ordered segments with full lat/lng polyline |
| Graph structure | CSR flat arrays | Cache-friendly, minimal memory, fast traversal |
| One-way streets | Respected | Parse OSM `oneway` tags for correct directed edges |
| Data loading | Preprocess + serialize | Separate CLI builds binary; server loads instantly |
| OSM format | .osm.pbf | Standard compressed binary OSM format |
| CH algorithm | Standard node ordering + shortcut creation | Well-documented algorithm with known correctness |
| PBF library | `paulmach/osm` | Well-maintained, idiomatic Go, clean scanner API |
| Spatial index | R-tree | Handles non-uniform road density; good library support |
| Serialization | Custom binary | Fastest load time; write raw arrays with header |
| Connected components | Keep largest only | Clean graph, no unreachable-destination errors |

## Component Overview

### 1. OSM Parser
- Read `.osm.pbf` using `paulmach/osm` library
- Extract highway ways and their nodes
- Filter for car-accessible roads (highway=motorway, trunk, primary, secondary, tertiary, residential, etc.)
- Parse `oneway` tags (yes, no, -1, reversible)
- Collect node coordinates for distance calculation

### 2. Graph Builder
- Convert OSM ways into directed edges between consecutive nodes
- Calculate edge weights (Haversine distance in meters)
- Remove isolated components — keep only the largest connected component
- Store as CSR format: `first_out[]`, `head[]`, `weight[]` arrays

### 3. Contraction Hierarchies Preprocessor
- **Node ordering:** Assign importance levels to nodes (using edge difference, contracted neighbors, etc.)
- **Contraction:** Process nodes in order — for each removed node, add shortcut edges if the node lies on a shortest path between its neighbors
- **Output:** Augmented graph with shortcut edges + node order/levels

### 4. Query Engine (Library)
- **Nearest-point snapping:** Given lat/lng, find nearest segment + ratio using R-tree spatial index
- **Bidirectional Dijkstra on CH:** Forward search from start (upward in hierarchy), backward search from end (upward), meet in the middle
- **Path unpacking:** Recursively unpack shortcut edges back to original segments
- **Result assembly:** Return ordered segments with geometry

### 5. HTTP API
- `POST /api/v1/route` — accepts `{start: {lat, lng}, end: {lat, lng}}`, returns route
- `GET /api/v1/health` — health check
- `GET /api/v1/stats` — graph statistics (node count, edge count, etc.)

## Resolved Questions

| Question | Decision |
|---|---|
| PBF parsing library | `paulmach/osm` — well-maintained, idiomatic Go, clean scanner API |
| Spatial index for snapping | R-tree — handles non-uniform density well, good Go library support |
| Binary serialization format | Custom binary — write raw arrays with a header, fastest load time for flat arrays |
| Connected components | Keep largest only — filter during preprocessing for a clean graph |
