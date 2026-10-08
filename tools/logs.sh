#!/usr/bin/env bash
# Follow the payload's logs (Syncthing log + console log) from the PS5,
# reconnecting whenever the payload restarts. Each line is prefixed with the
# local time. Pass a file name to also append to it.
#   tools/logs.sh [logfile]
set -uo pipefail
source "$(dirname "$0")/env.sh"
LOG="${1:-/dev/null}"
while true; do
  if nc -z -w 2 "$PS5_HOST" "$SYNCPS5_LOG_PORT" 2>/dev/null; then
    echo "$(date '+%F %T') [logs] connected to $PS5_HOST:$SYNCPS5_LOG_PORT" | tee -a "$LOG"
    nc "$PS5_HOST" "$SYNCPS5_LOG_PORT" | while IFS= read -r line; do
      echo "$(date '+%T') $line" | tee -a "$LOG"
    done
    echo "$(date '+%F %T') [logs] disconnected" | tee -a "$LOG"
  fi
  sleep 3
done
