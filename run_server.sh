#!/usr/bin/env bash
#
# Run the KL/Selangor routing server with BOTH metrics on one endpoint:
#   - "time"     (graph.time.bin, the default) — lowest travel time
#   - "distance" (graph.kl.dist.bin)           — true shortest road distance
# Clients choose per request via the "metric" field on POST /api/v1/route.
#
# On first run, graph.kl.dist.bin is built automatically from the local Malaysia
# OSM extract (same --kl bbox as the time graph, --distance metric). No other
# *.bin file is read, written, or deleted.
#
# Usage:  ./run_server.sh
# Overridable:  PORT=9000 DIST_GRAPH=graph.kl.dist.bin ./run_server.sh
set -euo pipefail

# Run from the repo root regardless of where it's called from.
cd "$(dirname "$0")"

TIME_GRAPH="${TIME_GRAPH:-graph.time.bin}"
DIST_GRAPH="${DIST_GRAPH:-graph.kl.dist.bin}"
PBF="${PBF:-malaysia-singapore-brunei-latest.osm.pbf}"
PORT="${PORT:-8086}"

# Build the distance graph on first run (won't touch any other .bin file).
if [[ ! -f "$DIST_GRAPH" ]]; then
	echo "==> $DIST_GRAPH not found; building it (preprocess --kl --distance)"
	go build -o bin/map-router-preprocess ./cmd/preprocess
	bin/map-router-preprocess --input "$PBF" --kl --distance --output "$DIST_GRAPH"
fi

echo "==> Building server binary"
go build -o bin/map-router-server ./cmd/server

echo "==> Serving $TIME_GRAPH (time) + $DIST_GRAPH (distance) on port $PORT"
bin/map-router-server --graph "$TIME_GRAPH" --graph-distance "$DIST_GRAPH" --port "$PORT"
