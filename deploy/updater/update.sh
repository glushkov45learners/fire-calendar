#!/usr/bin/env bash
# Runs inside the updater image. /data is mounted from the host:
#   /data/raw   firms/, firms_api/, landcover/, boundaries/ (the ETL input, copied once from the team's data/raw)
#   /data/out   hotspots.csv.gz and model/{cell_day.csv.gz,model_meta.json}, replaced only when every step succeeds
#
# 1. Re-downloads FIRMS area API chunks that end within the last REFRESH_DAYS days (NRT files grow during the day,
#    and NRT rows for a day stay until FIRMS publishes the standard product), plus any chunk that is missing.
# 2. ETL: all raw files -> hotspots.csv.gz (contract 1).
# 3. harmonize --no-tune -> cell_day.csv.gz + model_meta.json (contract 2), then check.
set -euo pipefail

: "${FIRMS_MAP_KEY:?set FIRMS_MAP_KEY in deploy/.env}"
RAW=/data/raw
OUT=/data/out
BBOX="${BBOX:-22.0,44.0,40.3,52.4}"     # Ukraine bbox (W,S,E,N); ETL clips to the border
REFRESH_DAYS="${REFRESH_DAYS:-10}"
THREADS="${THREADS:-2}"
API=https://firms.modaps.eosdis.nasa.gov/api/area/csv
# Chunk grids must match the files already in raw/firms_api: 5-day chunks from these start dates.
SOURCES="${SOURCES:-MODIS_NRT:2026-09-01 VIIRS_SNPP_NRT:2026-08-01 VIIRS_NOAA20_NRT:2026-08-01 VIIRS_NOAA21_NRT:2026-08-01}"

today=$(date -u +%F)
cut=$(date -u -d "$today -$REFRESH_DAYS day" +%F)
log() { echo "$(date -u +%FT%TZ) $*"; }

get() { # source start
  local dst="$RAW/firms_api/$1/$2.csv"
  mkdir -p "$RAW/firms_api/$1"
  for _ in 1 2 3; do
    if curl -fsS --max-time 120 "$API/$FIRMS_MAP_KEY/$1/$BBOX/5/$2" -o "$dst.part" && head -1 "$dst.part" | grep -q '^latitude'; then
      mv "$dst.part" "$dst"
      return 0
    fi
    sleep 10
  done
  log "FAIL $1 $2: $(head -c 200 "$dst.part" 2>/dev/null)"
  rm -f "$dst.part"
  return 1
}

log "fetch: chunks ending after $cut"
fetched=0
for s in $SOURCES; do
  src=${s%%:*} d=${s#*:}
  while [[ ! "$d" > "$today" ]]; do
    next=$(date -u -d "$d +5 day" +%F)
    if [[ ! -s "$RAW/firms_api/$src/$d.csv" || "$next" > "$cut" ]]; then
      get "$src" "$d"   # a failed chunk stops the job: the database keeps yesterday's data
      fetched=$((fetched + 1))
    fi
    d=$next
  done
done
log "fetch: $fetched chunks"

rm -rf "$OUT/next"
mkdir -p "$OUT/next/model"
log "etl"
etl -raw "$RAW" -out "$OUT/next/hotspots.csv.gz"
log "harmonize"
harmonize --input "$OUT/next/hotspots.csv.gz" --out "$OUT/next/model" --no-tune --threads "$THREADS"
check --out "$OUT/next/model"

# Swap in the new outputs only now, so a failed run never leaves half-written files for the loaders.
mv "$OUT/next/hotspots.csv.gz" "$OUT/hotspots.csv.gz"
rm -rf "$OUT/model"
mv "$OUT/next/model" "$OUT/model"
rm -rf "$OUT/next"
log "done"
