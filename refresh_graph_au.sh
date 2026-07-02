#!/usr/bin/env bash
#
# Build graph.au.bin: download the latest OpenStreetMap extract for ALL of
# Australia and preprocess it into a routing graph weighted by physical road
# length, so the engine routes by SHORTEST DISTANCE (no speed model / no time
# tuning — see --distance in cmd/preprocess).
#
# This is the Australia counterpart of refresh_graph.sh. It does NOT touch any
# other *.bin file: the Malaysia/Singapore graphs (graph.bin, graph.time.bin,
# graph.phase1.bin, graph.bin.bak) are left exactly as they are.
#
# Safe-by-default:
#   - The PBF is downloaded to a temp file and only moved into place once it
#     downloads cleanly and looks like a valid OSM PBF, so a failed/partial
#     download can't clobber a working file. An already-present PBF is reused
#     (instead of re-downloading ~900 MB) only if it passes a size + header
#     sanity check, so a header-intact-but-truncated leftover is re-downloaded
#     rather than trusted. Override the floor with MIN_PBF_BYTES (0 disables it,
#     e.g. when pointing OSM_URL at a smaller state extract).
#   - preprocess writes graph.au.bin.tmp and atomically renames it over
#     graph.au.bin, so an existing graph.au.bin survives if preprocessing fails.
#
# Usage:  ./refresh_graph_au.sh
#
# Overridable via env vars, e.g.:  OUTPUT=graph.au2.bin ./refresh_graph_au.sh
set -euo pipefail

# Run from the repo root (this script's directory) regardless of where it's called from.
cd "$(dirname "$0")"

OSM_URL="${OSM_URL:-https://download.geofabrik.de/australia-oceania/australia-latest.osm.pbf}"
PBF="${PBF:-australia-latest.osm.pbf}"
OUTPUT="${OUTPUT:-graph.au.bin}"
MIN_PBF_BYTES="${MIN_PBF_BYTES:-700000000}"   # ~700 MB floor; the AU extract is ~900 MB and only grows over time.

# Portable file size in bytes: BSD stat (macOS) || GNU stat (Linux) || 0.
file_size() { stat -f%z "$1" 2>/dev/null || stat -c%s "$1" 2>/dev/null || echo 0; }

# Validate that $1 looks like a COMPLETE OSM PBF: it is at least MIN_PBF_BYTES
# (so a truncated fragment isn't trusted) AND its first blob header contains
# "OSMHeader". We read the printable bytes of the header and match with a bash
# glob (BSD grep is unreliable on the binary, NUL/length-prefixed header).
is_valid_pbf() {
	[[ -s "$1" ]] || return 1
	[[ "$(file_size "$1")" -ge "$MIN_PBF_BYTES" ]] || return 1
	local header
	header="$(head -c 64 "$1" | LC_ALL=C tr -cd '[:print:]')"
	[[ "$header" == *OSMHeader* ]]
}

echo "==> Building preprocess binary"
go build -o bin/map-router-preprocess ./cmd/preprocess

if is_valid_pbf "$PBF"; then
	echo "==> Reusing existing $PBF ($(du -h "$PBF" | cut -f1))"
else
	echo "==> Downloading latest Australia OSM extract"
	echo "    $OSM_URL"
	tmp_pbf="${PBF}.download"
	curl -L --fail --retry 3 -o "$tmp_pbf" "$OSM_URL"
	if ! is_valid_pbf "$tmp_pbf"; then
		echo "ERROR: downloaded file is not a valid OSM PBF (missing OSMHeader); keeping existing $PBF" >&2
		rm -f "$tmp_pbf"
		exit 1
	fi
	mv -f "$tmp_pbf" "$PBF"
	echo "    saved $PBF ($(du -h "$PBF" | cut -f1))"
fi

echo "==> Regenerating $OUTPUT for ALL of Australia (metric: shortest distance)"
# No bounding box           -> the whole extract (all of Australia) is kept.
# --distance                -> edge weight is physical road length, so routing = shortest path.
# --min-component 2         -> keep every drivable road network, not just the mainland, so
#                              islands like Tasmania are included (you can't drive between
#                              them, but within each, routing works).
bin/map-router-preprocess --input "$PBF" --output "$OUTPUT" --distance --min-component 2

echo "==> Done. $OUTPUT built ($(du -h "$OUTPUT" | cut -f1))."
echo "    Start the Australia server with ./run_server_au.sh"
