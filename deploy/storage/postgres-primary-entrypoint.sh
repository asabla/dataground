#!/bin/sh
set -eu

: "${PGDATA:?PostgreSQL primary data directory is required}"
: "${DATAGROUND_FENCE_PATH:?PostgreSQL fence path is required}"

if [ -e "$DATAGROUND_FENCE_PATH" ]; then
  echo "PostgreSQL primary start refused: data directory is fenced" >&2
  exit 78
fi

exec /usr/local/bin/docker-entrypoint.sh "$@"

