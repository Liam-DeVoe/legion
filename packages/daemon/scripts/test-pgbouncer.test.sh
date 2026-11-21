#!/usr/bin/env bash
# packages/daemon/scripts/test-pgbouncer.test.sh
#
# Proves test-pgbouncer.sh's branches without Docker: a fake `docker` and a fake `psql` on PATH
# answer from the scenario the environment describes and record every call, so each case asserts
# what the script printed, every docker command it ran, and the pool configuration and auth_file
# line it handed the container. The cases: a DSN of a host off this machine is refused before any
# docker command; a localhost DSN gives the pool 127.0.0.1 as its server and prints the pool's URL;
# a `"` in the password is doubled in the auth_file line, percent-encoded in the URL and handed to
# psql as it is; a pool that exits without answering is logged and removed; a container of the
# script's name that already exists is refused and left alone; and a missing psql is named before
# anything runs.
#
# Run from anywhere: packages/daemon/scripts/test-pgbouncer.test.sh
# CI's daemon-go job runs it before it starts the pool.
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly script="${project_root}/packages/daemon/scripts/test-pgbouncer.sh"
temporary_dir="$(mktemp -d)"
readonly temporary_dir
trap 'rm -rf "$temporary_dir"' EXIT
mkdir -p "${temporary_dir}/bin" "${temporary_dir}/nopsql"

# The fake docker. FAKE_EXISTS=true makes `container inspect NAME` find a container of that name;
# `container inspect -f` prints FAKE_RUNNING. `run -d` writes the pool's configuration and auth_file
# line the script handed it through its environment to FAKE_CONFIG and FAKE_USERS.
cat >"${temporary_dir}/bin/docker" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"${FAKE_CALLS:?}"
case "$1 $2" in
  "container inspect")
    if [ "$3" = -f ]; then printf '%s\n' "${FAKE_RUNNING:?}"; exit 0; fi
    [ "${FAKE_EXISTS:-false}" = true ] || { echo "Error: No such container: $3" >&2; exit 1; }
    printf '[{}]\n'
    ;;
  "run -d")
    printf '%s' "${POOL_CONFIG:?}" >"${FAKE_CONFIG:?}"
    printf '%s' "${POOL_USERS:?}" >"${FAKE_USERS:?}"
    echo 0123456789ab
    ;;
  "logs "*) echo "fake pgbouncer log" ;;
  "rm -f") ;;
  *) echo "fake docker: unexpected command: $*" >&2; exit 2 ;;
esac
EOF
# The fake psql: answers when FAKE_PSQL_OK=true, and records the password it was handed.
cat >"${temporary_dir}/bin/psql" <<'EOF'
#!/usr/bin/env bash
printf '%s' "${PGPASSWORD-}" >"${FAKE_PSQL_PASSWORD:?}"
[ "${FAKE_PSQL_OK:?}" = true ]
EOF
chmod +x "${temporary_dir}/bin/docker" "${temporary_dir}/bin/psql"
# A PATH with every program the script needs but psql: the fake docker and the bash its first line
# asks for, the host's own timeout, and python3's own interpreter, linked by name (a version
# manager's shim would need the PATH this case takes away).
ln -s "${temporary_dir}/bin/docker" "${temporary_dir}/nopsql/docker"
ln -s "$BASH" "${temporary_dir}/nopsql/bash"
ln -s "$(command -v timeout)" "${temporary_dir}/nopsql/timeout"
ln -s "$(python3 -c 'import sys; print(sys.executable)')" "${temporary_dir}/nopsql/python3"

# A port nothing on this machine holds, for the script's own bind check.
port=$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])')
readonly port name="legion-test-pgbouncer-${port}"

failures=0
out="${temporary_dir}/out"
err="${temporary_dir}/err"
calls="${temporary_dir}/calls"
config="${temporary_dir}/config"
users="${temporary_dir}/users"
password_seen="${temporary_dir}/password"
fail() {
  printf 'FAIL: %s\n' "$1"
  printf '  stdout: %s\n' "$(cat "$out")"
  printf '  stderr: %s\n' "$(cat "$err")"
  printf '  docker: %s\n' "$(tr '\n' ';' <"$calls")"
  failures=$((failures + 1))
}
pass() { printf 'PASS: %s\n' "$1"; }
status=0
# Runs test-pgbouncer.sh --port $port with the scenario `env` describes; sets $status.
run_case() {
  : >"$out"
  : >"$err"
  : >"$calls"
  rm -f "$config" "$users" "$password_seen"
  status=0
  env PATH="${temporary_dir}/bin:${PATH}" FAKE_CALLS="$calls" FAKE_CONFIG="$config" FAKE_USERS="$users" \
    FAKE_PSQL_PASSWORD="$password_seen" FAKE_PSQL_OK=true FAKE_RUNNING=true \
    "$@" bash "$script" --port "$port" >"$out" 2>"$err" || status=$?
}

run_case LEGION_TEST_PG_DSN='postgres://postgres:ci@10.0.0.5:5432/postgres'
if [ "$status" -ne 1 ] || [ -s "$out" ] || [ -s "$calls" ] || ! grep -qF 'names no server on this machine' "$err"; then
  fail "a DSN of a host off this machine: refused before any docker command"
else
  pass "a DSN of a host off this machine: refused before any docker command"
fi

run_case LEGION_TEST_PG_DSN='postgres://postgres:ci@localhost:5544/sessions?sslmode=disable'
if [ "$status" -ne 0 ] || [ "$(cat "$out")" != "postgres://postgres:ci@127.0.0.1:${port}/sessions?sslmode=disable" ] ||
  ! grep -qxF '* = host=127.0.0.1 port=5544' "$config" || ! grep -qxF "listen_port = ${port}" "$config" ||
  ! grep -qxF 'pool_mode = transaction' "$config" ||
  ! grep -q "^run -d --name ${name} --network host --cpus 1 --memory 256m " "$calls" || grep -q '^rm ' "$calls"; then
  fail "a localhost DSN: the pool's server is 127.0.0.1, its URL printed, the container kept"
else
  pass "a localhost DSN: the pool's server is 127.0.0.1, its URL printed, the container kept"
fi

# The password c"i@, which the URL carries percent-encoded.
encoded='c%22i%40'
run_case LEGION_TEST_PG_DSN="postgres://postgres:${encoded}@127.0.0.1:5432/postgres"
if [ "$status" -ne 0 ] || [ "$(cat "$users")" != '"postgres" "c""i@"' ] ||
  [ "$(cat "$out")" != "postgres://postgres:${encoded}@127.0.0.1:${port}/postgres?sslmode=disable" ] ||
  [ "$(cat "$password_seen")" != 'c"i@' ]; then
  fail 'a " in the password: doubled in the auth_file line, percent-encoded in the URL, handed to psql as it is'
else
  pass 'a " in the password: doubled in the auth_file line, percent-encoded in the URL, handed to psql as it is'
fi

run_case LEGION_TEST_PG_DSN='postgres://postgres:ci@127.0.0.1:5432/postgres' FAKE_PSQL_OK=false FAKE_RUNNING=false
if [ "$status" -ne 1 ] || [ -s "$out" ] || ! grep -qF 'ran no statement' "$err" || ! grep -qF 'fake pgbouncer log' "$err" ||
  ! grep -qxF "logs ${name}" "$calls" || ! grep -qxF "rm -f -v ${name}" "$calls"; then
  fail "a pool that exits without answering: its log printed, the container removed"
else
  pass "a pool that exits without answering: its log printed, the container removed"
fi

run_case LEGION_TEST_PG_DSN='postgres://postgres:ci@127.0.0.1:5432/postgres' FAKE_EXISTS=true
if [ "$status" -ne 1 ] || [ -s "$out" ] || ! grep -qF "a container named ${name} already exists" "$err" ||
  grep -qE '^(run|rm) ' "$calls"; then
  fail "a container of the script's name exists: refused, nothing run or removed"
else
  pass "a container of the script's name exists: refused, nothing run or removed"
fi

: >"$out"
: >"$err"
: >"$calls"
status=0
env PATH="${temporary_dir}/nopsql" FAKE_CALLS="$calls" LEGION_TEST_PG_DSN='postgres://postgres:ci@127.0.0.1:5432/postgres' \
  "$BASH" "$script" --port "$port" >"$out" 2>"$err" || status=$?
if [ "$status" -ne 1 ] || [ -s "$calls" ] || ! grep -qxF 'test-pgbouncer: needs psql on PATH' "$err"; then
  fail "no psql on PATH: named before anything runs"
else
  pass "no psql on PATH: named before anything runs"
fi

if [ "$failures" -ne 0 ]; then
  printf '%d case(s) failed\n' "$failures"
  exit 1
fi
echo "all cases passed"
