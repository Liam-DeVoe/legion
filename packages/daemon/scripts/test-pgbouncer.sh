#!/usr/bin/env bash
# test-pgbouncer.sh [--port PORT] [--name NAME]: start PgBouncer 1.25.2 in transaction mode in
# front of the PostgreSQL server LEGION_TEST_PG_DSN names, and print the postgres:// URL that
# reaches that server through it, which is what LEGION_TEST_PGBOUNCER_DSN holds:
#
#   export LEGION_TEST_PGBOUNCER_DSN="$(packages/daemon/scripts/test-pgbouncer.sh --port 6433)"
#
# internal/ompsessions' TestEveryPoolPinsItsSearchPath runs its read through this pool. Where
# LEGION_TEST_PG_DSN is set and LEGION_TEST_PGBOUNCER_DSN is not, it skips naming the variable,
# and on GitHub Actions it fails. The session database's pods reach it through a pool in
# transaction mode, which refuses search_path as a startup parameter and carries no setting from
# one transaction to the next, so the package pins search_path in each transaction, and the
# pooled leg is what shows the pin holds there.
#
# CI's daemon-go job runs this script in its step "Start PgBouncer in front of the Postgres
# service" (.github/workflows/ci-job-daemon-go.yaml) with the default port, so CI and a developer
# run one image and one configuration, and both are written here alone:
#
#   * the image is .github/ci-images.yaml's pgbouncer row, PgBouncer 1.25.2 by digest. 1.26.0 and
#     later accept search_path at startup, so a later version would pass a startup pin that the
#     session database's pool refuses.
#   * pool_mode = transaction, and server_lifetime = 0, so each server connection serves one
#     transaction and is closed, and a setting one transaction leaves on its session reaches no
#     later one: the strictest stand-in for a pool in transaction mode.
#   * one wildcard [databases] entry, so the pool serves every database a test makes on that
#     server under the name it has there.
#   * the one login LEGION_TEST_PG_DSN names, its password in the pool's auth_file, so a login
#     that is no superuser, such as a Legion pod's test login, signs in as well as CI's postgres.
#
# The container runs on the host's network, listening on 127.0.0.1:PORT (default 6432), so
# LEGION_TEST_PG_DSN must name a server on this machine: its host must be 127.0.0.1, localhost or
# ::1. The pool reaches localhost as 127.0.0.1, since PgBouncer tries every address a name
# resolves to in turn, and a server published on IPv4 alone would refuse the connections it sends
# to ::1. The container is named NAME (default legion-test-pgbouncer-PORT) and keeps running after
# the script exits; the script prints on stderr the command that removes it. The configuration
# reaches the container through its environment, never through its command line, and the
# container writes it to its own /tmp, so the script leaves no file behind and no process list
# holds the password.
#
# It prints the URL only once a statement has run through the pool, within 60 s. It exits 1,
# naming what, for an unset LEGION_TEST_PG_DSN, one that is no postgres:// URL of a loopback
# host, a port that is taken, and a pool that exits or answers no statement in 60 s, such as one
# whose server refuses the DSN's password, after printing the pool's log. A container it
# started and is not handing over, because it fails or is interrupted, it removes; a container of
# its name that already exists it refuses, naming it, and leaves alone. It needs docker, python3,
# psql and timeout, and refuses naming whichever is missing. The pool runs with 1 CPU and 256 MiB,
# as docs/testing.md asks of every container a test starts on a shared machine.
set -euo pipefail

image=ghcr.io/cybermonklabs/legion/ci@sha256:7d7a27d9e90985cab5cf42256f5c13a3120baa4b055b69df37beb272b89b2340

fail() {
  echo "test-pgbouncer: $*" >&2
  exit 1
}

port=6432
name=""
while [ $# -gt 0 ]; do
  case $1 in
    --port) [ $# -ge 2 ] || fail "--port takes a port"; port=$2; shift 2 ;;
    --name) [ $# -ge 2 ] || fail "--name takes a container name"; name=$2; shift 2 ;;
    -h | --help) sed -n '2,6p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) fail "unknown argument $1; usage: test-pgbouncer.sh [--port PORT] [--name NAME]" ;;
  esac
done
case $port in
  '' | *[!0-9]*) fail "--port $port is no port number" ;;
esac
name=${name:-legion-test-pgbouncer-$port}
missing=""
for tool in docker python3 psql timeout; do
  command -v "$tool" >/dev/null 2>&1 || missing="$missing $tool"
done
[ -z "$missing" ] || fail "needs$missing on PATH"
[ -n "${LEGION_TEST_PG_DSN:-}" ] || fail "LEGION_TEST_PG_DSN is unset: name the PostgreSQL server the pool goes in front of"

# The fields the pool needs, read from the DSN in the environment, so no command line holds its
# password: the server's host and port, the login, its password, the database, the auth_file line
# and the URL through the pool. They come back NUL-separated, since a password may hold a
# newline.
fields=()
while IFS= read -r -d '' field; do
  fields+=("$field")
done < <(POOL_PORT=$port python3 - <<'PY'
import os
import sys
from urllib.parse import quote, unquote, urlsplit

dsn = urlsplit(os.environ["LEGION_TEST_PG_DSN"])
def refuse(why):
    print(f"test-pgbouncer: LEGION_TEST_PG_DSN {why}", file=sys.stderr)
    sys.exit(1)
if dsn.scheme not in ("postgres", "postgresql"):
    refuse("is no postgres:// URL")
host = dsn.hostname
if host not in ("127.0.0.1", "localhost", "::1"):
    refuse("names no server on this machine (127.0.0.1, localhost or ::1): the pool runs on the host's network")
if not dsn.username:
    refuse("names no login")
user, password = unquote(dsn.username), unquote(dsn.password or "")
database = unquote(dsn.path.lstrip("/")) or user
userlist = '"' + user.replace('"', '""') + '" "' + password.replace('"', '""') + '"'
url = (f"postgres://{quote(user, safe='')}:{quote(password, safe='')}@127.0.0.1:{os.environ['POOL_PORT']}"
       f"/{quote(database, safe='')}?sslmode=disable")
server = "127.0.0.1" if host == "localhost" else host
sys.stdout.write("\0".join([server, str(dsn.port or 5432), user, password, database, userlist, url]) + "\0")
PY
)
[ ${#fields[@]} -eq 7 ] || exit 1
server_host=${fields[0]} server_port=${fields[1]} user=${fields[2]} password=${fields[3]}
database=${fields[4]} userlist=${fields[5]} url=${fields[6]}

# A bind, not a connect: a port an outgoing connection holds as its own end refuses PgBouncer's
# bind as surely as a listener does, and answers no connect.
if ! python3 -c 'import socket, sys; socket.socket().bind(("127.0.0.1", int(sys.argv[1])))' "$port" 2>/dev/null; then
  fail "127.0.0.1:$port is taken: name another port with --port"
fi

config="[databases]
* = host=$server_host port=$server_port

[pgbouncer]
listen_addr = 127.0.0.1
listen_port = $port
unix_socket_dir =
auth_type = scram-sha-256
auth_file = /tmp/userlist.txt
pool_mode = transaction
server_lifetime = 0
max_client_conn = 500
default_pool_size = 20
max_prepared_statements = 1000
"

if docker container inspect "$name" >/dev/null 2>&1; then
  fail "a container named $name already exists: remove it (docker rm -f -v $name), or name another with --name"
fi
# The container is the script's to remove from the moment docker may have made it until the URL
# is printed: on a failure, a container docker made but could not start included, and on an
# interrupt, which the second trap turns into an exit so that the first one runs.
started=1
trap '[ -z "$started" ] || docker rm -f -v "$name" >/dev/null 2>&1 || true' EXIT
trap 'exit 1' INT TERM
# shellcheck disable=SC2016 # the container's own shell expands the two variables
POOL_CONFIG=$config POOL_USERS=$userlist docker run -d --name "$name" --network host --cpus 1 --memory 256m \
  -e POOL_CONFIG -e POOL_USERS --entrypoint sh "$image" \
  -c 'printf "%s\n" "$POOL_USERS" > /tmp/userlist.txt && printf "%s" "$POOL_CONFIG" > /tmp/pgbouncer.ini && exec pgbouncer /tmp/pgbouncer.ini' >/dev/null

# A bound in seconds, not in tries: a try against a server that refuses the login waits for the
# pool's own retry, so sixty tries would be minutes.
deadline=$((SECONDS + 60))
while [ "$SECONDS" -lt "$deadline" ]; do
  if PGPASSWORD=$password PGCONNECT_TIMEOUT=2 timeout 5 psql -X -At -h 127.0.0.1 -p "$port" -U "$user" -d "$database" \
    -c 'select 1' >/dev/null 2>&1; then
    started=""
    echo "test-pgbouncer: PgBouncer 1.25.2 runs as container $name on 127.0.0.1:$port in front of $server_host:$server_port; remove it with: docker rm -f -v $name" >&2
    printf '%s\n' "$url"
    exit 0
  fi
  # A pool that exited, a port taken since the check above say, answers no later try either.
  [ "$(docker container inspect -f '{{.State.Running}}' "$name" 2>/dev/null)" = true ] || break
  sleep 1
done
docker logs "$name" >&2 || true
fail "the pool in container $name ran no statement (its log is above), so the container is removed"
