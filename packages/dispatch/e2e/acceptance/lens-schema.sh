#!/usr/bin/env bash
# Drives a type of the store read in the write's own transaction (dispatch://INFRA-1950's
# c-schema-bump, dispatch://INFRA-1950/spec#b-r-lens-catalogue). A type is a `supertag` row of the
# store, so a write of a node checks the node's type against the store inside its own transaction
# and no process holds a list of types read at boot. This driver starts one Dispatch server over an
# empty database of the run's own, which the server migrates at boot, and then, with that one
# process serving throughout:
#
#   - deletes the `lens` supertag row and the attribute rows of that supertag with psql, in one
#     transaction, and writes a lens under a spec through the server: the write is refused with a
#     4xx status and a code, the spec holds no lens, and the store holds nothing of the write - no
#     lens, no finding and no tuple - so neither the node nor the rest of the write was stored,
#     where a list of types read at boot would have taken it;
#   - writes the deleted rows back with psql, as they were, writes the same lens again through the
#     same server and reads it among the spec's lenses;
#   - reads the server's row of server_processes before and after, and fails if its instance or
#     its started_at changed, so no restart stood between the two writes;
#   - adds one lens kind with its constraint through `envoy-dispatch lens-kind-add` and reads it in
#     the catalogue, with the installed block schema version unmoved from boot.
#
# It starts the server with e2e/run-server.sh, one server process, on a DISPATCH_E2E_PORT of its
# own, and the three fakes run-server.sh needs - e2e/fake-envoy.ts, e2e/fake-github.ts and
# e2e/fake-broker.ts - each on a port the OS chooses, read from the line each prints. It stops
# every process it started when it exits, also when a check fails, so the run can drop its
# database after it.
#
# Inputs: DATABASE_URL (an empty database on this machine that the run created and may drop; the
# server migrates it and this driver reads and writes its rows), and optionally
# DISPATCH_E2E_SERVER_BIN (the envoy-dispatch binary to serve and to run lens-kind-add with,
# default $TMPDIR/bin/envoy-dispatch, this head's build), DISPATCH_E2E_PORT (the server's port,
# default a free one) and DEV_SIGNIN_LOGIN (the person the dev sign-in route signs in to own the
# project, default alice). The server accepts the bearer `e2e-token` (run-server.sh). Each unmet
# input is exit 2.
#
# The first PASS line is `PASS: lens-schema.sh the catalogue holds its ten kinds`, read before any
# lens is written; the lines before it say what was started. Each passing check prints
# `PASS: <section> <what>`; the first failing one prints `FAIL: <section> <what>: <observed>`,
# naming the write and the answer it took, and the script exits 1. The last line is
# `PASS: lens-schema.sh complete`.
#
# Every check is `cmd || fail …`, never part of an `&&` list: under `set -e` a failing command
# anywhere but last in such a list does not stop the script.

# shellcheck disable=SC2016 # the single-quoted jq filters and SQL name jq and psql variables
set -euo pipefail
set +x

usage() {
  cat >&2 <<'USAGE'
Usage: DATABASE_URL=postgres://user@127.0.0.1:5433/infra_1950_schema?sslmode=disable \
         [DISPATCH_E2E_SERVER_BIN=$TMPDIR/bin/envoy-dispatch] [DISPATCH_E2E_PORT=<port>] \
         [DEV_SIGNIN_LOGIN=alice] lens-schema.sh
       (DATABASE_URL names an empty database on this machine, which the server migrates)
USAGE
}

# shellcheck source-path=SCRIPTDIR source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_env DATABASE_URL
require_commands bun curl jq openssl psql python3

readonly driver=lens-schema.sh
# shellcheck disable=SC2153 # DATABASE_URL is the caller's input, checked by name above
readonly database_url="$DATABASE_URL"
# shellcheck disable=SC2031 # the TMPDIR lib.sh's `cli` exports is its own; this one is the caller's
server_bin="${DISPATCH_E2E_SERVER_BIN:-${TMPDIR:-/tmp}/bin/envoy-dispatch}"
[[ "$server_bin" == /* ]] || server_bin="$PWD/$server_bin"
readonly server_bin
[[ -x "$server_bin" ]] || {
  printf 'DISPATCH_E2E_SERVER_BIN names %s, which is not an executable file\n' "$server_bin" >&2
  usage
  exit 2
}
readonly login="${DEV_SIGNIN_LOGIN:-alice}"
readonly token=e2e-token
e2e_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
readonly e2e_dir
stamp="$(date +%s)-$$"
readonly stamp
readonly session="lens-schema-$stamp"
readonly project=SCHEMA
# The ten kinds dispatch://INFRA-1297 records, which the catalogue's migration seeds.
readonly seeded_kinds='["best-practice","boring-solution","botec","conway","first-principles","inversion","pre-mortem","reversibility","second-order","trade-off"]'
# The kind lens-kind-add adds: a slug of letters and digits in words joined by single hyphens.
readonly added_kind="schema-bump-$stamp"
# How long a process gets to print the line that says it listens. The server's first boot migrates
# a whole schema from empty, the longest this wait covers.
readonly listen_limit=180

# The rows deleted and written back: the `lens` supertag row, and each attribute row an
# attribute_of tuple ties to that supertag, which is how a reader of the schema reads them.
readonly lens_type_rows="(n.kind = 'supertag' and n.id = 'lens')
  or (n.kind = 'attribute' and exists (
    select 1 from graph_tuples t
    where t.from_kind = 'attribute' and t.from_id = n.id and t.kind = 'attribute_of'
      and t.to_kind = 'supertag' and t.to_id = 'lens' and t.asserted))"
# What a lens write leaves in the store: lens nodes, findings (block nodes whose id names a lens),
# and the tuples out of either.
readonly lens_census="select (select count(*) from graph_nodes where kind = 'lens') || '|'
  || (select count(*) from graph_nodes where kind = 'block' and id like 'lens/%') || '|'
  || (select count(*) from graph_tuples where from_kind = 'lens' or (from_kind = 'block' and from_id like 'lens/%'))"

work_dir

pids=()
# stop_pid PID: ends the process PID, with SIGKILL for one that does not go on SIGTERM within ten
# seconds, and reaps it.
stop_pid() {
  local waited=0
  kill -TERM "$1" 2>/dev/null || return 0
  while kill -0 "$1" 2>/dev/null && [[ "$waited" -lt 20 ]]; do
    sleep 0.5
    waited=$((waited + 1))
  done
  kill -KILL "$1" 2>/dev/null || true
  wait "$1" 2>/dev/null || true
}
cleanup() {
  local code=$?
  local pid
  for pid in "${pids[@]}"; do
    stop_pid "$pid"
  done
  rm -rf "$work"
  exit "$code"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# fail_with_log SECTION WHAT OBSERVED LOG: fail, with the last lines of LOG after OBSERVED.
fail_with_log() {
  fail "$1" "$2" "$3; the last lines of its log: $(tail -n 20 "$4" 2>/dev/null)"
}

# psql_run SECTION WHAT ARGS…: psql against the run's database, stopping at the first error, its
# output in $work/psql.out. psql runs without PGHOSTADDR, which libpq would otherwise connect to in
# place of the DSN's host.
psql_run() {
  local section="$1" what="$2"
  shift 2
  env -u PGHOSTADDR psql "$database_url" -X -q -At -v ON_ERROR_STOP=1 "$@" >"$work/psql.out" 2>&1 ||
    fail "$section" "$what" "psql: $(<"$work/psql.out")"
}

# want_value SECTION WHAT WANT SQL: the one value SQL reads is WANT.
want_value() {
  query "$1" "$2" "$4"
  [[ "$answer" == "$3" ]] || fail "$1" "$2" "read '$answer', want '$3' (from: $4)"
  pass "$1" "$2"
}

# with_rows SECTION WHAT SQL: runs SQL with the rows this driver deleted bound to the psql
# variable `rows` (read as :'rows'), the one value it reads in $answer. The SQL is read from stdin,
# where psql substitutes a variable, which it does not do in a -c command.
with_rows() {
  answer="$(env -u PGHOSTADDR psql "$database_url" -X -q -At -v ON_ERROR_STOP=1 \
    -v rows="$(<"$work/lens-type-rows.json")" <<<"$3" 2>"$work/psql")" ||
    fail "$1" "$2" "psql: $(<"$work/psql")"
}

# await_line SECTION WHAT PID LOG PATTERN: waits for PATTERN in LOG while PID lives, for up to
# $listen_limit seconds, and sets $line to the first line holding it.
await_line() {
  local waited=0
  until line="$(grep -m 1 -E "$5" "$4" 2>/dev/null)"; do
    kill -0 "$3" 2>/dev/null || fail_with_log "$1" "$2" "the process stopped before it printed /$5/" "$4"
    [[ "$waited" -lt $((listen_limit * 2)) ]] ||
      fail_with_log "$1" "$2" "no line /$5/ after $listen_limit s" "$4"
    sleep 0.5
    waited=$((waited + 1))
  done
}

# start_fake NAME FILE: starts the fake e2e/FILE on a port the OS chooses, which its own line
# reports, and sets $fake_port to it.
start_fake() {
  env -u FAKE_ENVOY_PORT -u FAKE_GITHUB_PORT -u FAKE_BROKER_PORT -u FAKE_EMBEDDER_PORT \
    -u DISPATCH_E2E_STACKS -u DISPATCH_E2E_STACK_COUNT \
    bun "$e2e_dir/$2" >"$work/$1.log" 2>&1 &
  pids+=("$!")
  await_line setup "start the $1" "$!" "$work/$1.log" "listener on 127\.0\.0\.1:[0-9]+"
  fake_port="${line##*:}"
  [[ "$fake_port" =~ ^[0-9]+$ ]] || fail setup "start the $1" "its line reads: $line"
  printf '%s: the %s listens on 127.0.0.1:%s\n' "$driver" "$1" "$fake_port"
}

# request METHOD PATH [JSON]: sends METHOD PATH with the bearer the server accepts. The answer's
# body goes to $work/body and its HTTP status to $status.
request() {
  local -a data=()
  [[ $# -lt 3 ]] || data=(-H 'Content-Type: application/json' --data-binary "$3")
  status="$(curl -sS --max-time 60 -X "$1" -o "$work/body" -w '%{http_code}' -H "Authorization: Bearer $token" \
    "${data[@]}" "$base$2" 2>"$work/curl")" || status="curl failed: $(<"$work/curl")"
}

# actor: the session every agent write of this run acts for.
actor() { jq -cn --arg s "$session" '{kind: "session", id: $s}'; }

# server_row SECTION WHAT: sets $server_row to the instance and started_at of the one row of
# server_processes, which is this server's.
server_row() {
  query "$1" "$2" "select count(*) || '|' || coalesce(min(instance::text), '') || '|' || coalesce(min(started_at::text), '')
    from server_processes"
  [[ "$answer" == 1\|* ]] || fail "$1" "$2" "server_processes holds '$answer', want one row, this server's"
  server_row="${answer#1|}"
}

# --- boot ---------------------------------------------------------------------------------------

host="${database_url#*://}"
host="${host#*@}"
host="${host%%[:/?]*}"
case "$host" in
  127.0.0.1 | ::1 | localhost) ;;
  *)
    printf 'DATABASE_URL names the host %s, which is not this machine: the server dev sign-in refuses it, and this driver deletes rows of its store\n' "$host" >&2
    exit 2
    ;;
esac
query boot "read the run's database before the server starts" \
  "select count(*) from information_schema.tables where table_schema = 'public'"
readonly tables_before="$answer"
[[ "$tables_before" == 0 ]] ||
  fail boot "the run's database is empty before the server starts" "it holds $tables_before tables"

start_fake "fake Envoy" fake-envoy.ts
readonly envoy_port="$fake_port"
start_fake "fake GitHub" fake-github.ts
readonly github_port="$fake_port"
start_fake "fake secrets broker" fake-broker.ts
readonly broker_port="$fake_port"

port="${DISPATCH_E2E_PORT:-}"
if [[ -z "$port" ]]; then
  port="$(python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
fi
[[ "$port" =~ ^[0-9]+$ ]] || {
  printf 'DISPATCH_E2E_PORT is %s, not a port number\n' "$port" >&2
  exit 2
}
readonly port
readonly base="http://127.0.0.1:$port"
DATABASE_URL="$database_url" FAKE_ENVOY_PORT="$envoy_port" FAKE_GITHUB_PORT="$github_port" \
  FAKE_BROKER_PORT="$broker_port" DISPATCH_E2E_PORT="$port" DISPATCH_E2E_SERVER_BIN="$server_bin" \
  bash "$e2e_dir/run-server.sh" >"$work/server.log" 2>&1 &
readonly server_pid="$!"
pids+=("$server_pid")
await_line setup "start the server" "$server_pid" "$work/server.log" 'msg="dispatch: listening" addr=127\.0\.0\.1:[0-9]+'
[[ "$line" == *"addr=127.0.0.1:$port"* ]] ||
  fail_with_log setup "start the server on port $port" "it listens at: $line" "$work/server.log"
printf '%s: one server, %s, serves %s (pid %s)\n' "$driver" "$server_bin" "$base" "$server_pid"

# --- the catalogue ------------------------------------------------------------------------------

request GET /api/v1/schema/blocks
want_status 200 "$driver" "read the catalogue"
cp "$work/body" "$work/schema-boot.json"
held="$(jq -cS --argjson want "$seeded_kinds" '[.lens_kinds[].id] as $ids | [$want[] | select(. as $id | $ids | index($id))]' \
  "$work/body")" || fail "$driver" "read the catalogue" "unreadable: $(body)"
[[ "$held" == "$seeded_kinds" ]] ||
  fail "$driver" "the catalogue holds its ten kinds" "it holds $held of $seeded_kinds"
pass "$driver" "the catalogue holds its ten kinds"
boot_version="$(jq -r '.version' "$work/schema-boot.json")"
readonly boot_version

# --- boot: one process, over a database it migrated ---------------------------------------------

pass boot "the run's database held no table before the server started"
want_value boot "the server ran its migrations at boot: the store holds the lens supertag" 1 \
  "select count(*) from graph_nodes where kind = 'supertag' and id = 'lens' and state <> 'gone'"
server_row boot "read the server's row of server_processes"
readonly row_before="$server_row"
pass boot "server_processes holds one row, this server's: $row_before"
query boot "read the block schema version the server installed" \
  "select schema_version from server_processes"
readonly boot_schema_version="$answer"
[[ "$boot_schema_version" == "$boot_version" ]] ||
  fail boot "the server's row names the block schema version it installed" \
    "the row reads $boot_schema_version and GET /api/v1/schema/blocks reads $boot_version"
pass boot "the server installed block schema version $boot_version, which its row names"
cp "$work/schema-boot.json" "$work/body"
expect boot "the server reads lens among the store's types" true \
  '[.store_types[].name] | index("lens") != null'
# A type is a row: each of the five types this tree's nodes take is a supertag row of the store,
# and none of them is a block type, so adding one declares no block schema version.
want_value boot "the store holds lens, risk, constraint, interaction and lens_kind as supertag rows" 5 \
  "select count(*) from graph_nodes where kind = 'supertag' and state <> 'gone'
     and id in ('lens', 'risk', 'constraint', 'interaction', 'lens_kind')"
expect boot "the server lists the five among the store's types, and none among the block types it installed" \
  '{"block_types":[],"store_types":["constraint","interaction","lens","lens_kind","risk"]}' \
  '["constraint", "interaction", "lens", "lens_kind", "risk"] as $five
   | {store_types: [.store_types[].name | select(. as $n | $five | index($n))] | sort,
      block_types: [.types[].name | select(. as $n | $five | index($n))]}'

# --- setup: the person, the project and the spec ------------------------------------------------

status="$(curl -sS --max-time 60 -o "$work/body" -w '%{http_code}' -c "$work/jar" \
  "$base/auth/_dev/signin?login=$(q "$login")" 2>"$work/curl")" || status="curl failed: $(<"$work/curl")"
want_status 302 setup "dev sign-in as $login"
grep -q $'\tdsession\t' "$work/jar" || fail setup "dev sign-in as $login" "the answer set no session cookie"
pass setup "signed in as $login, the project's default assignee"
request POST /api/v1/projects "$(jq -cn --arg k "$project" --arg l "$login" --argjson a "$(actor)" \
  '{key: $k, name: "Lens schema check", default_assignee: $l, actor: $a}')"
want_status 201 setup "create the project $project"
request POST /api/v1/issues "$(jq -cn --arg p "$project" --argjson a "$(actor)" \
  '{project: $p, title: "Lens schema check", force: true, actor: $a, spec: "The spec a lens is written under."}')"
want_status 201 setup "create the issue"
key="$(jq -er '.key' "$work/body")" || fail setup "create the issue" "no key: $(body)"
readonly key spec="dispatch://$key/spec"
pass setup "created $key in $project, whose spec is $spec"
lens_write="$(jq -cn --arg on "$spec" --argjson a "$(actor)" \
  '{on: $on, kind: "trade-off", verdict: "One store.", findings: ["A finding written with the lens."], actor: $a}')"
readonly lens_write

# --- S1: a write of a type the store no longer holds --------------------------------------------

query S1 "read the lens supertag row and its attribute rows" \
  "select coalesce(json_agg(row_to_json(n) order by n.kind, n.id), '[]') from graph_nodes n where $lens_type_rows"
printf '%s' "$answer" >"$work/lens-type-rows.json"
jq -e '([.[] | select(.kind == "supertag" and .id == "lens")] | length) == 1
  and ([.[] | select(.kind == "attribute")] | length) >= 1' "$work/lens-type-rows.json" >/dev/null ||
  fail S1 "the store holds the lens supertag row and its attribute rows" "$(<"$work/lens-type-rows.json")"
pass S1 "the store holds the lens supertag row and its attribute rows: $(jq -c '[.[] | "\(.kind)/\(.id)"]' "$work/lens-type-rows.json")"
query S1 "count what the store holds of lenses" "$lens_census"
readonly census_before="$answer"

psql_run S1 "delete the lens supertag row and its attribute rows in one transaction" \
  -c "begin;
      create temporary table deleted_lens_type on commit drop as select n.kind, n.id from graph_nodes n where $lens_type_rows;
      delete from graph_nodes n using deleted_lens_type d where n.kind = d.kind and n.id = d.id;
      commit;"
with_rows S1 "read the deleted rows" \
  "select count(*) from graph_nodes n where (n.kind, n.id) in
     (select kind, id from json_populate_recordset(null::graph_nodes, :'rows'::json))"
[[ "$answer" == 0 ]] ||
  fail S1 "the lens supertag row and its attribute rows are gone from the store" "$answer of them are still there"
pass S1 "the lens supertag row and its attribute rows are gone from the store"
request GET /api/v1/schema/blocks
want_status 200 S1 "read the store's types after the delete"
expect S1 "the store's types, which listed lens, list it no more" false \
  '[.store_types[].name] | index("lens") != null'

request POST "/api/v1/issues/$key/lenses" "$lens_write"
cp "$work/body" "$work/refusal.json"
refused_status="$status"
request GET "/api/v1/lenses?on=$(q "$spec")"
want_status 200 S1 "read the spec's lenses after the write"
stored="$(jq -c '[.lenses[].address]' "$work/body")"
[[ "$stored" == "[]" ]] ||
  fail S1 "the write of a lens, whose type the store no longer holds, is refused" \
    "the write POST /api/v1/issues/$key/lenses $lens_write was taken: HTTP $refused_status $(<"$work/refusal.json"); the spec holds the lens $stored"
pass S1 "the spec holds no lens after the write"
query S1 "count what the store holds of lenses after the write" "$lens_census"
[[ "$answer" == "$census_before" ]] ||
  fail S1 "the store holds nothing of the refused write: no lens, no finding, no tuple" \
    "lenses|findings|tuples read $census_before before the write and $answer after it; the write answered HTTP $refused_status $(<"$work/refusal.json")"
pass S1 "the store holds nothing of the refused write: no lens, no finding, no tuple ($answer)"
cp "$work/refusal.json" "$work/body"
status="$refused_status"
[[ "$status" == 4?? ]] ||
  fail S1 "the write is refused with a 4xx status" "the write $lens_write answered HTTP $status: $(body)"
pass S1 "the write is refused with HTTP $status"
code="$(jq -r '.code // empty' "$work/body")"
[[ -n "$code" ]] || fail S1 "the refusal carries a code" "the write $lens_write answered HTTP $status: $(body)"
pass S1 "the refusal carries a code, $code"
expect S1 "the refusal is SUPERTAG_NOT_HELD, naming the type lens" '{"code":"SUPERTAG_NOT_HELD","names_lens":true}' \
  '{code, names_lens: (.error | contains("no type lens"))}'

# --- S2: the rows written back, and the same write taken by the same process --------------------

env -u PGHOSTADDR psql "$database_url" -X -q -At -v ON_ERROR_STOP=1 -v rows="$(<"$work/lens-type-rows.json")" \
  >"$work/psql.out" 2>&1 <<'SQL' || fail S2 "write the deleted rows back" "psql: $(<"$work/psql.out")"
insert into graph_nodes select * from json_populate_recordset(null::graph_nodes, :'rows'::json);
SQL
pass S2 "wrote the lens supertag row and its attribute rows back with psql"
query S2 "read the rows written back" \
  "select coalesce(json_agg(row_to_json(n) order by n.kind, n.id), '[]') from graph_nodes n where $lens_type_rows"
[[ "$(jq -cS '.' <<<"$answer")" == "$(jq -cS '.' "$work/lens-type-rows.json")" ]] ||
  fail S2 "the rows are back as they were" "before: $(<"$work/lens-type-rows.json"); now: $answer"
pass S2 "the rows are back as they were, column for column"
request GET /api/v1/schema/blocks
want_status 200 S2 "read the store's types after the rows came back"
expect S2 "the store's types list lens again" true '[.store_types[].name] | index("lens") != null'

request POST "/api/v1/issues/$key/lenses" "$lens_write"
[[ "$status" == 201 ]] ||
  fail S2 "the same lens write is taken by the same server" "the write $lens_write answered HTTP $status: $(body)"
lens="$(jq -er '.address' "$work/body")" || fail S2 "the same lens write is taken" "no address: $(body)"
readonly lens
pass S2 "the same lens write is taken by the same server ($lens)"
request GET "/api/v1/lenses?on=$(q "$spec")"
want_status 200 S2 "read the spec's lenses"
expect S2 "the spec's lenses hold the lens, of its kind, with its finding" \
  "$(jq -cnS --arg l "$lens" '[{address: $l, findings: ["A finding written with the lens."], kind: "trade-off", verdict: "One store."}]')" \
  '[.lenses[] | {address, kind: .kind.id, verdict, findings: [.findings[].text]}]'

server_row S2 "read the server's row of server_processes again"
[[ "$server_row" == "$row_before" ]] ||
  fail S2 "the server's instance and started_at are the ones it had" "before: $row_before; after: $server_row"
kill -0 "$server_pid" 2>/dev/null || fail S2 "the server this driver started still serves" "pid $server_pid is gone"
pass S2 "one process took both writes: its instance and started_at are the ones it had ($server_row)"

# --- S3: a kind added at runtime, with the block schema version unmoved -------------------------

readonly kind_source="Edward Tufte, The Visual Display of Quantitative Information, Graphics Press, 1983"
readonly kind_reference='Cheshire, CT: Graphics Press, 1983; ch. 4, “Data-Ink and Graphical Redesign”, p. 93'
DATABASE_URL="$database_url" "$server_bin" lens-kind-add --id "$added_kind" --words "the data-ink check" \
  --source "$kind_source" --reference "$kind_reference" >"$work/kind.out" 2>&1 ||
  fail S3 "envoy-dispatch lens-kind-add --id $added_kind adds the kind" "it exited $?: $(<"$work/kind.out")"
grep -q "^lens-kind-add: added dispatch://lens_kind/$added_kind, held to its source dispatch://constraint/" "$work/kind.out" ||
  fail S3 "envoy-dispatch lens-kind-add --id $added_kind adds the kind" "it printed: $(<"$work/kind.out")"
pass S3 "envoy-dispatch lens-kind-add added $added_kind: $(<"$work/kind.out")"

request GET /api/v1/schema/blocks
want_status 200 S3 "read the catalogue after the add"
expect S3 "the catalogue holds the kind added, with its words and its one source" \
  "$(jq -cnS --arg id "$added_kind" --arg s "$kind_source" --arg r "$kind_reference" \
    '[{id: $id, ref: "dispatch://lens_kind/\($id)", words: "the data-ink check", sources: [{standard: $s, reference: $r}]}]')" \
  --arg id "$added_kind" '[.lens_kinds[] | select(.id == $id) | {id, ref, words, sources: [.sources[] | {standard, reference}]}]'
expect S3 "the installed block schema version is the one the server installed at boot, $boot_version" \
  "$boot_version" '.version'
want_value S3 "the server's row still names block schema version $boot_schema_version" "$boot_schema_version" \
  "select schema_version from server_processes"
server_row S3 "read the server's row of server_processes after the add"
[[ "$server_row" == "$row_before" ]] ||
  fail S3 "the server's instance and started_at are the ones it had" "before: $row_before; after: $server_row"
pass S3 "the same process answered the kind added: its instance and started_at are the ones it had"

pass "$driver" complete
