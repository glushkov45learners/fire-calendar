#!/usr/bin/env bash
# Daily data refresh: FIRMS NRT -> ETL -> harmonize (updater image) -> PostGIS (load_hotspots.sh, load_model.sh).
# Works from any directory. Cron, once a day after FIRMS has published the previous day:
#   0 6 * * * /path/to/deploy/update_data.sh >> $HOME/fire-calendar-update.log 2>&1
# Needs FIRMS_MAP_KEY in deploy/.env and the ETL input in deploy/data/raw (see deploy/updater/update.sh).
set -euo pipefail
cd "$(dirname "$(realpath "$0")")"

exec 9>"${TMPDIR:-/tmp}/fire-calendar-update.lock"
flock -n 9 || { echo "update already running"; exit 0; }

echo "== $(date -u +%FT%TZ) update start"
docker compose --profile jobs run --rm updater
./load_hotspots.sh data/out/hotspots.csv.gz
./load_model.sh data/out/model
echo "== $(date -u +%FT%TZ) update done"
