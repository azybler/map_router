#!/usr/bin/env bash
#
# Run the routing server for ALL of Australia, using the shortest-distance graph
# (graph.au.bin). Australia counterpart of run_server.sh — same server binary and
# routing engine, only the graph differs.
#
# On first run, if graph.au.bin does not exist yet, it is built automatically via
# refresh_graph_au.sh (downloads the Australia OSM extract and preprocesses it).
# No other *.bin file is read, written, or deleted.
#
# Usage:  ./run_server_au.sh
# Overridable:  PORT=9000 GRAPH=graph.au.bin ./run_server_au.sh
set -euo pipefail

# Run from the repo root (this script's directory) regardless of where it's called from.
cd "$(dirname "$0")"

GRAPH="${GRAPH:-graph.au.bin}"
PORT="${PORT:-8090}"   # distinct from run_server.sh (8086) so both can run at once

# Build the Australia graph on first run (won't touch any other .bin file).
if [[ ! -f "$GRAPH" ]]; then
	echo "==> $GRAPH not found; building it first via refresh_graph_au.sh"
	./refresh_graph_au.sh
fi

echo "==> Building server binary"
go build -o bin/map-router-server ./cmd/server

echo "==> Serving $GRAPH on port $PORT (shortest-distance routing, all of Australia)"
bin/map-router-server --graph "$GRAPH" --port "$PORT"
