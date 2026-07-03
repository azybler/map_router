#!/usr/bin/env bash
#
# Refresh graph.time.bin in place: download the latest OSM extract and re-run
# preprocessing, filtered to the Selangor/KL bounding box (--kl) — current
# usage is KL/Selangor only. Speeds are Google-calibrated on 1,430 routes
# (see datasets/elevete_route_cache/README.md); for full-peninsula coverage
# use FILTER='--bbox 1.2,99.6,6.8,104.5' (~722 MB graph).
#
# Safe-by-default:
#   - The PBF is downloaded to a temp file and only moved into place once it
#     downloads cleanly and looks like a valid OSM PBF, so a failed/partial
#     download can't clobber a working file.
#   - preprocess writes graph.bin.tmp and atomically renames it over graph.bin,
#     so the existing graph.bin survives if preprocessing fails.
#
# Usage:  ./refresh_graph.sh
#
# Overridable via env vars, e.g.:  FILTER=--singapore ./refresh_graph.sh
set -euo pipefail

# Run from the repo root (this script's directory) regardless of where it's called from.
cd "$(dirname "$0")"

OSM_URL="${OSM_URL:-https://download.geofabrik.de/asia/malaysia-singapore-brunei-latest.osm.pbf}"
PBF="${PBF:-malaysia-singapore-brunei-latest.osm.pbf}"
OUTPUT="${OUTPUT:-graph.time.bin}"
# Selangor/KL bbox. Alternatives: --singapore | --bbox minLat,minLng,maxLat,maxLng
FILTER="${FILTER:---kl}"
SPEEDS="${SPEEDS:-speeds.json}"   # tunable class speed table (see speeds.json)

echo "==> Building preprocess binary"
go build -o bin/map-router-preprocess ./cmd/preprocess

echo "==> Downloading latest OSM extract"
echo "    $OSM_URL"
tmp_pbf="${PBF}.download"
curl -L --fail --retry 3 -o "$tmp_pbf" "$OSM_URL"

# Validate it's a real OSM PBF: the first blob header contains "OSMHeader".
# Extract the printable bytes of the header into a variable and match with a
# bash glob. We avoid `grep` here because BSD grep is unreliable on the binary
# header (NUL/length-prefix bytes), and avoid a pipe in the conditional so
# `set -o pipefail` can't misreport the result.
header="$(head -c 64 "$tmp_pbf" | LC_ALL=C tr -cd '[:print:]')"
if [[ "$header" != *OSMHeader* ]]; then
	echo "ERROR: downloaded file is not a valid OSM PBF (missing OSMHeader); keeping existing $PBF" >&2
	rm -f "$tmp_pbf"
	exit 1
fi
mv -f "$tmp_pbf" "$PBF"
echo "    saved $PBF ($(du -h "$PBF" | cut -f1))"

echo "==> Regenerating $OUTPUT (filter: $FILTER, speeds: $SPEEDS)"
bin/map-router-preprocess --input "$PBF" --output "$OUTPUT" "$FILTER" --speeds "$SPEEDS"
# Record the speed-table hash. Prefer sha256sum (common on Linux/CI); fall back
# to shasum -a 256 (macOS) for portability.
if command -v sha256sum >/dev/null 2>&1; then
	sha256sum "$SPEEDS" | tee "${OUTPUT}.speeds.sha256"
else
	shasum -a 256 "$SPEEDS" | tee "${OUTPUT}.speeds.sha256"
fi

echo "==> Done. $OUTPUT refreshed ($(du -h "$OUTPUT" | cut -f1))."
echo "    Restart the server (./run_server.sh) to load the new graph."
