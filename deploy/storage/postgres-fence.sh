#!/bin/sh
set -eu

: "${PGDATA:?PostgreSQL primary data directory is required}"
: "${DATAGROUND_FENCE_PATH:?PostgreSQL fence path is required}"

if pg_isready --host postgres --port 5432 >/dev/null 2>&1; then
  echo "PostgreSQL primary must be stopped before fencing" >&2
  exit 1
fi
if [ ! -s "$PGDATA/PG_VERSION" ]; then
  echo "PostgreSQL primary data directory is not initialized" >&2
  exit 1
fi

umask 077
printf '%s\n' 'stale-primary-start-refused' >"$DATAGROUND_FENCE_PATH"
