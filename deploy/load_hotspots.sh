#!/usr/bin/env bash
# Replaces the hotspot table with the contents of hotspots.csv.gz (contract 1).
# Usage: deploy/load_hotspots.sh [file-or-url]     (works from any directory)
set -euo pipefail
SRC="${1:-https://storage.googleapis.com/nasa-viirs-modis/v1/hotspots.csv.gz}"
[[ "$SRC" == http* ]] || SRC="$(realpath "$SRC")"
cd "$(dirname "$(realpath "$0")")"   # docker compose below needs this directory's compose file

if [[ "$SRC" == http* ]]; then fetch() { curl -fsSL "$SRC"; }; else fetch() { cat "$SRC"; }; fi

# One psql session: the staging table is temporary, and the swap is one
# transaction, so readers never see a half-loaded table.
{
  cat <<'SQL'
\set ON_ERROR_STOP on
BEGIN;
CREATE TEMP TABLE stage (
    acq_date date, acq_time text, lat float8, lon float8, instrument text, satellite text,
    conf text, conf_raw text, frp real, daynight text, type smallint, product text,
    cell_id int, lc smallint
) ON COMMIT DROP;
COPY stage FROM STDIN WITH (FORMAT csv, HEADER true);
SQL
  fetch | gzip -dc
  cat <<'SQL'
\.
TRUNCATE hotspot RESTART IDENTITY;
INSERT INTO hotspot (acq_ts, acq_date, geom, instrument, satellite, conf, conf_raw,
                     frp, daynight, type, product, cell_id, lc)
SELECT (acq_date + make_time(left(acq_time, 2)::int, right(acq_time, 2)::int, 0)) AT TIME ZONE 'UTC',
       acq_date, ST_SetSRID(ST_MakePoint(lon, lat), 4326), instrument, satellite, conf, conf_raw,
       frp, daynight, type, product, cell_id, lc
FROM stage
ORDER BY acq_date, acq_time;
COMMIT;
ANALYZE hotspot;
SELECT count(*) AS rows, min(acq_date) AS first_day, max(acq_date) AS last_day FROM hotspot;
SQL
} | docker compose exec -T postgis psql -U fire -d fire -q
