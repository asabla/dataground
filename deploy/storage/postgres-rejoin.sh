#!/bin/sh
set -eu

: "${PGDATA:?PostgreSQL target data directory is required}"
: "${DATAGROUND_FENCE_PATH:?PostgreSQL fence path is required}"
: "${POSTGRES_USER:?PostgreSQL rejoin user is required}"
: "${POSTGRES_PASSWORD:?PostgreSQL rejoin password is required}"

fence="$DATAGROUND_FENCE_PATH"
if [ ! -f "$fence" ] || [ ! -s "$PGDATA/PG_VERSION" ]; then
  echo "PostgreSQL rejoin requires an initialized fenced data directory" >&2
  exit 1
fi
if pg_isready --host postgres --port 5432 >/dev/null 2>&1; then
  echo "PostgreSQL target must remain stopped during rejoin" >&2
  exit 1
fi
promoted=$(PGPASSWORD="$POSTGRES_PASSWORD" psql \
  --host postgres-standby \
  --port 5432 \
  --username "$POSTGRES_USER" \
  --dbname postgres \
  --tuples-only \
  --no-align \
  --command 'SELECT NOT pg_is_in_recovery()')
if [ "$promoted" != "t" ]; then
  echo "PostgreSQL rejoin source is not the promoted primary" >&2
  exit 1
fi

PGPASSWORD="$POSTGRES_PASSWORD" pg_rewind \
  --target-pgdata="$PGDATA" \
  --source-server="host=postgres-standby port=5432 user=$POSTGRES_USER dbname=postgres application_name=dataground-rejoined-primary sslmode=disable" \
  --write-recovery-conf \
  --no-ensure-shutdown \
  --progress

test -f "$PGDATA/standby.signal"
rm -f "$fence"
