#!/bin/sh
set -eu

printf 'host replication all all scram-sha-256\n' >>"$PGDATA/pg_hba.conf"

psql \
  --username "$POSTGRES_USER" \
  --dbname "$POSTGRES_DB" \
  --set=ON_ERROR_STOP=1 \
  --set=replication_role="$POSTGRES_USER" <<'SQL'
ALTER ROLE :"replication_role" WITH REPLICATION;
SQL
