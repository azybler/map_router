#!/usr/bin/env bash
#
# Run the KL/Selangor routing server with BOTH metrics on one endpoint:
#   - "time"     (graph.time.bin, the default) — lowest travel time
#   - "distance" (graph.kl.dist.bin)           — true shortest road distance
# Clients choose per request via the "metric" field on POST /api/v1/route.
#
# Memory: the two metrics share an identical road network, so instead of loading
# two self-contained graphs (each with its own coords, topology, geometry, and
# snap index) the server loads the shared BASE once and stitches a small
# per-metric OVERLAY onto it — one base + one Snapper in RAM instead of two.
# On the KL graph that is ~315 MB instead of ~407 MB (~23% less).
#
# The split files (graph.base.bin + graph.{time,dist}.overlay.bin) are derived
# on first run from the combined graphs — graph.time.bin (produced/refreshed by
# refresh_graph.sh) and graph.kl.dist.bin (built here from the OSM extract on
# first run) — via `preprocess --split-from`, which just re-serializes them and
# needs no OSM parse. They are regenerated automatically whenever the combined
# graph they came from is newer.
#
# Usage:  ./run_server.sh
# Overridable:  PORT=9000 DIST_GRAPH=graph.kl.dist.bin ./run_server.sh
set -euo pipefail

# Run from the repo root regardless of where it's called from.
cd "$(dirname "$0")"

TIME_GRAPH="${TIME_GRAPH:-graph.time.bin}"
DIST_GRAPH="${DIST_GRAPH:-graph.kl.dist.bin}"
BASE_GRAPH="${BASE_GRAPH:-graph.base.bin}"
TIME_OVERLAY="${TIME_OVERLAY:-graph.time.overlay.bin}"
DIST_OVERLAY="${DIST_OVERLAY:-graph.dist.overlay.bin}"
PBF="${PBF:-malaysia-singapore-brunei-latest.osm.pbf}"
PORT="${PORT:-8086}"

echo "==> Building preprocess binary"
go build -o bin/map-router-preprocess ./cmd/preprocess

if [[ ! -f "$TIME_GRAPH" ]]; then
	echo "ERROR: $TIME_GRAPH not found. Build it first with ./refresh_graph.sh" >&2
	exit 1
fi

# Build the distance graph on first run (won't touch any other .bin file).
if [[ ! -f "$DIST_GRAPH" ]]; then
	echo "==> $DIST_GRAPH not found; building it (preprocess --kl --distance)"
	bin/map-router-preprocess --input "$PBF" --kl --distance --output "$DIST_GRAPH"
fi

# Derive the shared base + time overlay from the time graph (regenerate when the
# combined graph is newer than the overlay, e.g. after ./refresh_graph.sh).
if [[ ! -f "$BASE_GRAPH" || ! -f "$TIME_OVERLAY" || "$TIME_GRAPH" -nt "$TIME_OVERLAY" ]]; then
	echo "==> Deriving $BASE_GRAPH + $TIME_OVERLAY from $TIME_GRAPH"
	bin/map-router-preprocess --split-from "$TIME_GRAPH" \
		--output-base "$BASE_GRAPH" --output-overlay "$TIME_OVERLAY"
fi

# Derive the distance overlay from the distance graph. The base it emits is
# byte-identical to $BASE_GRAPH when both graphs come from the same OSM snapshot,
# so it is written to a throwaway path and discarded; the server pairs the
# distance overlay with the time-derived base and rejects it at load if their
# topology identities disagree (i.e. the two graphs are out of sync).
if [[ ! -f "$DIST_OVERLAY" || "$DIST_GRAPH" -nt "$DIST_OVERLAY" ]]; then
	echo "==> Deriving $DIST_OVERLAY from $DIST_GRAPH"
	discard_base="graph.dist-base.discard.bin"
	bin/map-router-preprocess --split-from "$DIST_GRAPH" \
		--output-base "$discard_base" --output-overlay "$DIST_OVERLAY"
	rm -f "$discard_base"
fi

echo "==> Building server binary"
go build -o bin/map-router-server ./cmd/server

echo "==> Serving $TIME_OVERLAY (time) + $DIST_OVERLAY (distance) over shared $BASE_GRAPH on port $PORT"
bin/map-router-server \
	--graph-base "$BASE_GRAPH" \
	--graph "$TIME_OVERLAY" \
	--graph-distance "$DIST_OVERLAY" \
	--port "$PORT"
