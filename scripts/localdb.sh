#!/usr/bin/env sh
# Run a throwaway PostgreSQL in ./.localdb using a locally installed server
# (for example Homebrew's postgresql@17) when Docker is not available.
#
#   scripts/localdb.sh start     # initdb on first run, then start on $PGPORT (default 54329)
#   scripts/localdb.sh stop
#   scripts/localdb.sh url       # prints the connection string
#   scripts/localdb.sh destroy   # stop and delete the data directory
set -eu

# macOS ships locale settings PostgreSQL rejects ("postmaster became multithreaded").
export LC_ALL=C LANG=C

PGPORT="${PGPORT:-54329}"
DIR="$(cd "$(dirname "$0")/.." && pwd)/.localdb"
BIN="${PG_BIN:-}"
if [ -z "$BIN" ]; then
  for candidate in /opt/homebrew/opt/postgresql@17/bin /opt/homebrew/opt/postgresql@16/bin /usr/lib/postgresql/17/bin /usr/lib/postgresql/16/bin; do
    if [ -x "$candidate/pg_ctl" ]; then BIN="$candidate"; break; fi
  done
fi
if [ -z "$BIN" ]; then
  echo "no PostgreSQL server found; set PG_BIN to its bin directory" >&2
  exit 1
fi
URL="postgres://variants:variants@127.0.0.1:${PGPORT}/variants?sslmode=disable"

case "${1:-}" in
  start)
    if [ ! -f "$DIR/PG_VERSION" ]; then
      mkdir -p "$DIR"
      "$BIN/initdb" -D "$DIR" -U variants --auth=trust -E UTF8 --locale=C >/dev/null
    fi
    if ! "$BIN/pg_ctl" -D "$DIR" status >/dev/null 2>&1; then
      "$BIN/pg_ctl" -D "$DIR" -o "-p $PGPORT -c listen_addresses=127.0.0.1 -k /tmp" -l "$DIR/server.log" -w start >/dev/null
    fi
    "$BIN/psql" -h 127.0.0.1 -p "$PGPORT" -U variants -d postgres -tAc "SELECT 1 FROM pg_database WHERE datname='variants'" | grep -q 1 \
      || "$BIN/createdb" -h 127.0.0.1 -p "$PGPORT" -U variants variants
    echo "export DATABASE_URL='$URL'"
    echo "export TEST_DATABASE_URL='$URL'"
    ;;
  stop)
    "$BIN/pg_ctl" -D "$DIR" -m fast -w stop >/dev/null 2>&1 || true
    ;;
  url)
    echo "$URL"
    ;;
  destroy)
    "$BIN/pg_ctl" -D "$DIR" -m fast -w stop >/dev/null 2>&1 || true
    rm -rf "$DIR"
    ;;
  *)
    echo "usage: $0 start|stop|url|destroy" >&2
    exit 2
    ;;
esac
