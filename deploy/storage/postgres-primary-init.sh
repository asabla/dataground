#!/bin/sh
set -eu

: "${DATAGROUND_HEALTH_PASSWORD:?PostgreSQL health password is required}"

printf 'host replication all all scram-sha-256\n' >>"$PGDATA/pg_hba.conf"

psql \
  --username "$POSTGRES_USER" \
  --dbname "$POSTGRES_DB" \
  --set=ON_ERROR_STOP=1 \
  --set=database="$POSTGRES_DB" \
  --set=health_password="$DATAGROUND_HEALTH_PASSWORD" \
  --set=health_role=dataground_health \
  --set=replication_role="$POSTGRES_USER" <<'SQL'
ALTER ROLE :"replication_role" WITH REPLICATION;
CREATE ROLE :"health_role" WITH LOGIN PASSWORD :'health_password'
  NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION;
REVOKE CONNECT, TEMPORARY ON DATABASE :"database" FROM PUBLIC;
GRANT CONNECT ON DATABASE :"database" TO :"health_role";
ALTER ROLE :"health_role" SET search_path = pg_catalog;
ALTER ROLE :"health_role" SET statement_timeout = '2s';
ALTER ROLE :"health_role" SET lock_timeout = '1s';
ALTER ROLE :"health_role" SET idle_in_transaction_session_timeout = '2s';
SQL
