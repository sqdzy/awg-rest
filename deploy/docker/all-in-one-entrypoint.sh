#!/bin/sh
set -e

# Single-container entrypoint: starts PostgreSQL, waits for it, then execs
# the control-plane API (with embedded worker).

PGDATA=${PGDATA:-/var/lib/postgresql/data}
AWG_STATE_DIR=${AWG_STATE_DIR:-/var/lib/awg-rest}
JWT_SECRET_PATH=${JWT_SECRET_PATH:-$AWG_STATE_DIR/jwt_secret}

mkdir -p "$AWG_STATE_DIR" "${BOOTSTRAP_CONF_DIR:-$AWG_STATE_DIR/bootstrap}"
chmod 700 "$AWG_STATE_DIR" "${BOOTSTRAP_CONF_DIR:-$AWG_STATE_DIR/bootstrap}"

# Generate a persistent HMAC secret for first-run demos. Production operators
# should set JWT_SECRET explicitly and share it only with the backend signer.
if [ -z "${JWT_SECRET:-}" ]; then
    if [ ! -f "$JWT_SECRET_PATH" ]; then
        umask 077
        dd if=/dev/urandom bs=32 count=1 2>/dev/null | base64 > "$JWT_SECRET_PATH"
    fi
    JWT_SECRET="$(tr -d '\r\n' < "$JWT_SECRET_PATH")"
    export JWT_SECRET
fi

# Initialise PostgreSQL data directory if empty.
if [ ! -f "$PGDATA/PG_VERSION" ]; then
    echo "[entrypoint] Initialising PostgreSQL ..."
    mkdir -p "$PGDATA"
    chown postgres:postgres "$PGDATA"
    su-exec postgres initdb -D "$PGDATA" --auth-local=trust --auth-host=trust
    printf "\nlisten_addresses = '127.0.0.1'\n" >> "$PGDATA/postgresql.conf"
fi

echo "[entrypoint] Starting PostgreSQL ..."
su-exec postgres pg_ctl -D "$PGDATA" -l "$PGDATA/logfile" start

# Wait until postgres accepts connections.
until pg_isready -h 127.0.0.1 -U postgres -d postgres >/dev/null 2>&1; do
    echo "[entrypoint] Waiting for PostgreSQL ..."
    sleep 1
done

# Create the awg database if it does not exist.
su-exec postgres psql -h 127.0.0.1 -U postgres -tc "SELECT 1 FROM pg_database WHERE datname='awg'" | grep -q 1 || \
    su-exec postgres createdb -h 127.0.0.1 -U postgres awg

export DATABASE_URL=${DATABASE_URL:-postgres://postgres@127.0.0.1:5432/awg?sslmode=disable}

echo "[entrypoint] Database ready. Starting control plane ..."
exec "$@"
