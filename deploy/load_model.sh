#!/usr/bin/env bash
# Replaces cell_day and upserts model_meta from the algorithm output (contract 2).
# Usage: deploy/load_model.sh [dir-or-url]     (works from any directory)
#   dir-or-url holds cell_day.csv.gz and model_meta.json;
#   default is the bucket folder of model v3.
set -euo pipefail
SRC="${1:-https://storage.googleapis.com/nasa-viirs-modis/v1/model/v3}"
SRC="${SRC%/}"
[[ "$SRC" == http* ]] || SRC="$(realpath "$SRC")"
cd "$(dirname "$(realpath "$0")")"   # docker compose below needs this directory's compose file

if [[ "$SRC" == http* ]]; then fetch() { curl -fsSL "$SRC/$1"; }; else fetch() { cat "$SRC/$1"; }; fi

META="$(fetch model_meta.json)"
# Fail before touching the database if the gzip is missing or unreadable.
fetch cell_day.csv.gz | gzip -t

# One psql session and one transaction: readers see the old model until COMMIT.
{
  cat <<'SQL'
\set ON_ERROR_STOP on
BEGIN;
CREATE TEMP TABLE stage (LIKE cell_day) ON COMMIT DROP;
COPY stage (cell_id, date, modis_count, viirs_count, count_h, frp_h) FROM STDIN WITH (FORMAT csv, HEADER true);
SQL
  fetch cell_day.csv.gz | gzip -dc
  cat <<'SQL'
\.
TRUNCATE cell_day;
INSERT INTO cell_day SELECT * FROM stage ORDER BY cell_id, date;
INSERT INTO model_meta (model_version, body)
SELECT :'meta'::jsonb ->> 'model_version', :'meta'::jsonb
ON CONFLICT (model_version) DO UPDATE SET body = EXCLUDED.body, loaded_at = now();
COMMIT;
ANALYZE cell_day;
SELECT count(*) AS rows, min(date) AS first_day, max(date) AS last_day,
       round(sum(count_h)) AS count_h, sum(modis_count) AS modis, sum(viirs_count) AS viirs
FROM cell_day;
SELECT model_version, loaded_at FROM model_meta ORDER BY loaded_at DESC;
SQL
} | docker compose exec -T postgis psql -U fire -d fire -q -v meta="$META"
