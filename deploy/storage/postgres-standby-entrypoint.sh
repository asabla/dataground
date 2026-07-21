#!/bin/sh
set -eu

: "${PGDATA:?PostgreSQL standby data directory is required}"
: "${POSTGRES_USER:?PostgreSQL standby user is required}"
: "${POSTGRES_PASSWORD:?PostgreSQL standby password is required}"

if [ ! -s "$PGDATA/PG_VERSION" ]; then
  mkdir -p "$PGDATA"
  chmod 0700 "$PGDATA"
  until pg_isready --host postgres --port 5432 --username "$POSTGRES_USER" >/dev/null 2>&1; do
    sleep 1
  done
  PGPASSWORD="$POSTGRES_PASSWORD" pg_basebackup \
    --dbname="postgresql://$POSTGRES_USER@postgres:5432/postgres?application_name=dataground-standby&sslmode=disable" \
    --pgdata="$PGDATA" \
    --wal-method=stream \
    --write-recovery-conf \
    --checkpoint=fast \
    --no-password
fi

exec postgres -D "$PGDATA" -c hot_standby=on
