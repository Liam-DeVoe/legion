#!/usr/bin/env bash
# The check c-stores-moved: the boot of this head moves every row that the release before wrote in
# the priority store and the workflow store into the graph store, through a roll in which the
# release before still serves, a stopped move, a rollback and two runners, and within 5 minutes at
# ten times production's volume (dispatch://INFRA-1098's R32, the spec's Design part "How the two
# stores move").
#
# The run gives the driver the release before as BASELINE_SHA (`git merge-base origin/main HEAD`),
# three empty databases of its own (DATABASE_URL, BOOT_DATABASE_URL, VOLUME_DATABASE_URL), the
# bearer, the project and the person the drivers here take, and WALK_RULE_ISSUE, the child whose
# walk rule R32 relies on. It boots that commit and this head through lib-baseline.sh, and reads
# each server's log at $baseline_work/logs/NAME.log. It writes nothing to production and reads no
# production address: the run's own guards read production's /healthz before the driver starts.
#
#   1. The base. It fails where BASELINE_SHA lacks 20261109013400_graph_store.up.sql,
#      20261112024320_priority_nodes.up.sql or 20261116235202_workflow_nodes.up.sql, and where the
#      merge commit of the newest merged pull request of legion/<WALK_RULE_ISSUE>, which `gh`
#      answers with the role's own credential, is no ancestor of BASELINE_SHA. WALK_RULE_COMMIT,
#      where set, names that merge commit in place of `gh`, for a local run with no credential.
#   2. The roll, over DATABASE_URL: the fixtures through the base server's API, the reads before,
#      the inventory of the four tables; the rehearsal of the deploy's census with a planted
#      difference (left out under SKIP_PLANT=1), held while the base server writes, stopped after
#      2 seconds, and refusing or skipping pending migrations of another tree; four transactions
#      held open in the release before's shapes and run B beside them, while this head boots over
#      the same database and its swap waits, the driver acting on what pg_locks shows; the writes
#      of the base server through the views after the swap; every write read back through this
#      head, the reads before read again, the driver's own census against the inventory, the
#      census of node rows against their tuples and the two counts; a rollback to the base and
#      back; and the drop the release after makes.
#   3. A stopped move, a rollback over it and two runners, over BOOT_DATABASE_URL.
#   4. The time, over VOLUME_DATABASE_URL: ten times production's volume of 2026-11-23 beside
#      3,000,000 tuples of other kinds and more until graph_tuples passes one GiB, the census's
#      plans, and the boot's time from its migrations to the end of the move, at most 5 minutes.
#
# The driver acts on what pg_locks shows and never on time: each wait for a state of pg_locks, of
# a log or of a process gives up after 60 seconds (a server's boot after 180, and the volume boot
# after 600), prints the state it waited for, and exits 1. Its parsing is in functions that run
# when the file is sourced without running a step ([[ ${BASH_SOURCE[0]} == "$0" ]] && main), which
# stores-moved.test.sh runs over the fixtures under testdata/stores-moved/.
#
# Each passing assertion prints `PASS: <section> <what>`; the first failing one prints
# `FAIL: <section> <what>: <observed>` on stderr and the run exits 1; a missing or bad input exits
# 2. It prints no SKIP line. Every check is `cmd || fail …`, never part of an `&&` list: under
# `set -e` a failing command anywhere but last in such a list does not stop the script.

# shellcheck disable=SC2016 # the single-quoted jq filters and SQL name their own variables
set -euo pipefail
set +x

usage() {
  cat >&2 <<'USAGE'
Usage: BASELINE_SHA=$(git merge-base origin/main HEAD) WALK_RULE_ISSUE=<the child whose walk rule R32 relies on> \
         DATABASE_URL=${LEGION_TEST_PG_DSN%/*}/infra_1098_stores?sslmode=disable \
         BOOT_DATABASE_URL=${LEGION_TEST_PG_DSN%/*}/infra_1098_stores_boot?sslmode=disable \
         VOLUME_DATABASE_URL=${LEGION_TEST_PG_DSN%/*}/infra_1098_stores_volume?sslmode=disable \
         DISPATCH_TOKEN=e2e-token PROJECT=CORE DEV_SIGNIN_LOGIN=alice \
         [SKIP_PLANT=1] [WALK_RULE_COMMIT=<that child's merge commit>] [BASELINE_BINARY=<a build of BASELINE_SHA>] \
         [HEAD_BINARY=<this head's envoy-dispatch>] bash e2e/acceptance/stores-moved.sh
       (run from packages/dispatch. Each database is an empty one on this machine, which the run
       created and drops. HEAD_BINARY defaults to the envoy-dispatch on PATH, the Verification
       preamble's build of this head. SKIP_PLANT=1 leaves out the planted difference and the
       first census, for the second and third negative controls alone)
USAGE
}

# shellcheck source-path=SCRIPTDIR source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
# shellcheck source-path=SCRIPTDIR source=lib-baseline.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-baseline.sh"

readonly driver=stores-moved.sh

# The four names of the release before, the six tables the move reads and writes, and the kinds
# the move takes into the graph store.
readonly -a old_tables=(priority_nodes priority_tuples workflow_nodes workflow_tuples)
readonly moved_kinds="'priority', 'step', 'plan'"
# How the move's connection names itself in pg_stat_activity (storesmove's applicationName).
readonly move_application='envoy-dispatch stores move'
# The lines of the head's log the driver reads.
readonly gave_up_words="an attempt of the stores' swap gave up waiting for a lock"
readonly moved_nothing_words='are in the graph store already; the boot moves nothing'
readonly stopped_words="the stores' move stopped after the batches its test setting allows"
# The bound of every wait for a state: 60 seconds, polled every tenth of a second.
readonly state_limit=600
# The two migrations of the base whose rows of schema_migrations the steps of pending migrations
# delete and put back.
readonly supertags_migration=20261109051232_graph_supertags
readonly search_text_migration=20261121060636_search_document_text

# ---------------------------------------------------------------------------------------------
# Parsers. Each reads text the real systems print and answers what the driver acts on; none reads
# a global of the run, so stores-moved.test.sh runs each over the captured fixtures.
# ---------------------------------------------------------------------------------------------

# lock_modes CSV TABLE: the modes in which the backend whose pg_locks rows CSV holds (the columns
# relation, mode and granted, with a header, as psql --csv prints them) holds TABLE, granted, one
# space apart, or `none`.
lock_modes() {
  local modes
  modes="$(awk -F, -v table="$2" 'NR > 1 && $1 == table && $3 == "t" { printf "%s%s", sep, $2; sep = " " }' <<<"$1")"
  printf '%s\n' "${modes:-none}"
}

# lock_waited CSV: the relation the backend waits for, a row not granted; nothing where it waits
# for none.
lock_waited() { awk -F, 'NR > 1 && $3 == "f" { print $1; exit }' <<<"$1"; }

# swap_verdict CSV: the rule of the swap's locks at each wait on one of the four old tables: the
# backend holds graph_nodes in ShareRowExclusiveLock and not in AccessExclusiveLock, and holds none
# of the four old tables in ExclusiveLock. It prints `lock modes: <table> in <modes>` for the first
# that breaks it and returns 1; at any other state it prints nothing and returns 0.
swap_verdict() {
  local waited table modes
  waited="$(lock_waited "$1")"
  [[ -n "$waited" && " ${old_tables[*]} " == *" $waited "* ]] || return 0
  modes="$(lock_modes "$1" graph_nodes)"
  if [[ " $modes " != *" ShareRowExclusiveLock "* || " $modes " == *" AccessExclusiveLock "* ]]; then
    printf 'lock modes: graph_nodes in %s\n' "$modes"
    return 1
  fi
  for table in "${old_tables[@]}"; do
    modes="$(lock_modes "$1" "$table")"
    if [[ " $modes " == *" ExclusiveLock "* ]]; then
      printf 'lock modes: %s in %s\n' "$table" "$modes"
      return 1
    fi
  done
}

# advisory_counts CSV: `GRANTED WAITING`, how many rows of CSV's granted column read t and how many
# f, for the rows of pg_locks of the advisory lock stores-moved.
advisory_counts() {
  awk -F, 'NR == 1 { for (i = 1; i <= NF; i++) if ($i == "granted") c = i; next }
    c && $c == "t" { g++ } c && $c == "f" { w++ } END { print g + 0, w + 0 }' <<<"$1"
}

# log_line FILE WORDS: the first line of FILE that holds WORDS, as they are written.
log_line() { awk -v words="$2" 'index($0, words) { print; exit }' "$1"; }

# gave_up_lines LOG: each line of a server's log that says an attempt of the swap gave up waiting
# for a lock, its last attempt among them.
gave_up_lines() { awk -v words="$gave_up_words" 'index($0, words)' "$1"; }

# last_attempt_line LOG: the line that says the swap's last attempt gave up, and the boot fails.
last_attempt_line() { log_line "$1" "gave up waiting for a lock, its last; the boot fails"; }

# deadlock_line FILE…: the first line of the files, each that exists, that holds `deadlock
# detected`, as PostgreSQL words it in a server's log and in psql's output.
deadlock_line() {
  local file
  for file in "$@"; do
    if [[ -r "$file" ]]; then
      log_line "$file" 'deadlock detected'
    fi
  done | awk 'NR == 1'
}

# handler_failure_line LOG RELATION: the first line of a server's log that says an API handler
# failed and names RELATION whole, as `ERROR: relation "<name>" does not exist` names it.
handler_failure_line() {
  awk -v relation="$2" 'index($0, "msg=\"dispatch: API handler failed\"") &&
    match($0, "(^|[^a-z0-9_])" relation "([^a-z0-9_]|$)") { print; exit }' "$1"
}

# copy_ended OUT: `TUPLES TIME` of the census's line that its rehearsal's copy ended.
copy_ended() {
  sed -nE 's/^census: rehearsal: its copy ended: ([0-9]+) tuples of the four tables in ([^,]+), in batches of [0-9]+$/\1 \2/p' "$1" |
    awk 'NR == 1'
}

# rehearsal_count OUT STORE WHAT: `OLD MOVED DIFFERING` of the census's line of one comparison of
# its rehearsal, `census: rehearsal: STORE: WHAT: old N (md5 …), moved M (md5 …), D differing`.
rehearsal_count() {
  sed -nE "s/^census: rehearsal: $2: $3: old ([0-9]+) \\(md5 [0-9a-f]+\\), moved ([0-9]+) \\(md5 [0-9a-f]+\\), ([0-9]+) differing\$/\\1 \\2 \\3/p" "$1" |
    awk 'NR == 1'
}

# rehearsal_refusal OUT: the first line that starts `census: REFUSED rehearsal:`.
rehearsal_refusal() { awk 'index($0, "census: REFUSED rehearsal:") == 1 { print; exit }' "$1"; }

# refused_migrations OUT: the name of each pending migration the rehearsal refuses, as the line
# `census: the rehearsal refuses the pending migration <name>` names it.
refused_migrations() { sed -n 's/^census: the rehearsal refuses the pending migration //p' "$1"; }

# skipped_migration OUT NAME: the line that says the rehearsal skips NAME, a migration of another
# tree.
skipped_migration() {
  awk -v head="census: rehearsal: it skips the pending migration $2, of another tree's: " 'index($0, head) == 1 { print; exit }' "$1"
}

# rehearsal_lines OUT: each line of the rehearsal's report, a refusal of its own and a refused
# pending migration among them; none where no rehearsal ran.
rehearsal_lines() { awk '/^census: (rehearsal: |REFUSED rehearsal: |the rehearsal refuses )/' "$1"; }

# seq_scan_statement OUT: what the first statement of the census before the drop reads whose plan
# (DISPATCH_TEST_STORES_MOVE_EXPLAIN) reads graph_tuples by Seq Scan; nothing where none does.
seq_scan_statement() {
  awk -v head='census: rehearsal: plan of the census of the ' '
    index($0, head) == 1 { what = substr($0, length(head) + 1); sub(/:$/, "", what); next }
    /^census: / { what = "" }
    what != "" && /Seq Scan on graph_tuples( |$)/ { print what; exit }' "$1"
}

# slog_seconds LINE: the time= of a server's log line, in seconds since the epoch.
slog_seconds() {
  local at
  at="$(sed -nE 's/^time=([^ ]+) .*/\1/p' <<<"$1")"
  [[ -n "$at" ]] || return 1
  date -d "$at" +%s.%N
}

# boot_seconds LOG: the seconds from a boot's first line, which it writes before its migrations
# (it writes none at their start), to its line that ends the move, so the time holds the boot's
# pending migrations and the move. It fails where either line is missing.
boot_seconds() {
  local first ended
  first="$(awk 'NR == 1' "$1")"
  ended="$(log_line "$1" 'msg="dispatch: the move of the priority store and the workflow store into the graph store ended"')"
  [[ -n "$first" && -n "$ended" ]] || return 1
  first="$(slog_seconds "$first")" || return 1
  ended="$(slog_seconds "$ended")" || return 1
  awk -v a="$first" -v b="$ended" 'BEGIN { printf "%.3f\n", b - a }'
}

# ---------------------------------------------------------------------------------------------
# Helpers of the run.
# ---------------------------------------------------------------------------------------------

# sql URL SQL: one psql run against URL, stopping at the first error, its rows unaligned and
# tuples only on stdout. It runs without PGHOSTADDR, PGSERVICE and PGSERVICEFILE, as lib.sh's query
# does.
sql() {
  env -u PGHOSTADDR -u PGSERVICE -u PGSERVICEFILE psql "$1" -X -q -At -v ON_ERROR_STOP=1 -c "$2"
}

# sql_csv URL SQL: the same, as CSV with its header, the shape the pg_locks fixtures hold.
sql_csv() {
  env -u PGHOSTADDR -u PGSERVICE -u PGSERVICEFILE psql "$1" -X -q --csv -v ON_ERROR_STOP=1 -c "$2"
}

# value SECTION WHAT URL SQL: the one value SQL reads, in $answer.
value() {
  answer="$(sql "$3" "$4" 2>"$work/psql")" || fail "$1" "$2" "psql: $(<"$work/psql")"
}

# want SECTION WHAT URL WANT SQL: the one value SQL reads is WANT.
want() {
  value "$1" "$2" "$3" "$5"
  [[ "$answer" == "$4" ]] || fail "$1" "$2" "read '$answer', want '$4'"
  pass "$1" "$2"
}

# on URL: the server the next requests go to (lib.sh's request reads $base).
on() { base="$1"; }

# spawn NAME BINARY DATABASE_URL [NAME=value…]: starts BINARY as baseline_boot starts a server, with
# the environment it gives one and the pairs after it, logging to $baseline_work/logs/NAME.log, and
# returns at once: a boot of this head waits on locks the driver holds, so the driver reads its
# state while it boots. It leaves spawned_pid and spawned_log. baseline_boot's own paragraph says
# why each variable is set and why nothing else reaches the server.
spawn() {
  local name="$1" binary="$2" url="$3"
  shift 3
  local held="$baseline_workspace/scripts/e2e/lib/held-port.py"
  spawned_log="$baseline_work/logs/$name.log"
  : >"$spawned_log" || fail setup "start the server $name" "could not write $spawned_log"
  (
    set +x
    unheld
    local variable saved_path="$PATH"
    local -a exported
    mapfile -t exported < <(compgen -e)
    for variable in "${exported[@]}"; do
      unset "$variable" 2>/dev/null || true
    done
    export PATH="$saved_path" HOME=/nonexistent XDG_CACHE_HOME=/nonexistent XDG_CONFIG_HOME=/nonexistent
    export XDG_DATA_HOME=/nonexistent TMPDIR="$baseline_work" DATABASE_URL="$url"
    export DISPATCH_AGENT_TOKEN="$baseline_token" DISPATCH_DEV_SIGNIN=1
    export DISPATCH_DEV_SIGNIN_SHARED_KEY="$baseline_signin_key" DISPATCH_IDENTITY=cookie
    export DISPATCH_INSECURE_COOKIE=1 DISPATCH_LISTEN_HOST=127.0.0.1 DISPATCH_NATS_DISABLED=1
    export DISPATCH_WEB_DIST="$baseline_work/web-dist" ENVOY_URL=http://127.0.0.1:1
    exec env "$@" python3 "$held" exec --fd-env DISPATCH_LISTEN_FD \
      --env 'DISPATCH_PORT={port}' --env 'DISPATCH_SERVER_URL=http://127.0.0.1:{port}' -- "$binary"
  ) >>"$spawned_log" 2>&1 &
  spawned_pid=$!
  servers+=("$spawned_pid")
}

# listening SECTION NAME PID LOG [LIMIT]: waits until the server's log holds its listening line,
# for at most LIMIT seconds (180, the bound baseline_boot gives a boot), and sets $listened to its
# address. A server that exits first fails, with its log's last lines.
listening() {
  local section="$1" name="$2" pid="$3" log="$4" limit="${5:-180}" waited=0 line=""
  while [[ "$waited" -lt $((limit * 10)) ]]; do
    line="$(grep -m 1 'dispatch: listening' "$log" 2>/dev/null)" || line=""
    [[ -z "$line" ]] || break
    kill -0 "$pid" 2>/dev/null || fail "$section" "the server $name listens" "it exited first: $(tail -n 15 "$log")"
    sleep 0.1
    waited=$((waited + 1))
  done
  [[ -n "$line" ]] || fail "$section" "the server $name listens" "it did not within ${limit}s: $(tail -n 15 "$log")"
  [[ "$line" =~ addr=([^[:space:]]+) ]] || fail "$section" "the server $name listens" "its line names no address: $line"
  listened="http://${BASH_REMATCH[1]}"
}

# exited SECTION NAME PID [LIMIT]: waits until the process PID exits, for at most LIMIT seconds
# (60), and sets $exit_code to its status.
exited() {
  local section="$1" name="$2" pid="$3" limit="${4:-60}" waited=0
  while kill -0 "$pid" 2>/dev/null; do
    [[ "$waited" -lt $((limit * 10)) ]] || fail "$section" "$name exits" "it still runs after ${limit}s"
    sleep 0.1
    waited=$((waited + 1))
  done
  exit_code=0
  wait "$pid" 2>/dev/null || exit_code=$?
}

# stop PID: stops a server this run started (lib-baseline.sh's baseline_stop_pid).
stop() { baseline_stop_pid "$1"; }

# census_run NAME URL [NAME=value…]: runs this head's `envoy-dispatch census` over URL in the
# background, as the deploy's Job runs it, with the pairs given, its output in $work/NAME.out, and
# leaves census_pid. census_wait NAME [LIMIT] then waits for it and sets $census_code.
census_run() {
  local name="$1" url="$2"
  shift 2
  (
    set +x
    unheld
    exec env -i PATH="$PATH" HOME=/nonexistent DATABASE_URL="$url" "$@" "$head_binary" census
  ) >"$work/$name.out" 2>&1 &
  census_pid=$!
}
census_wait() {
  exited census "the census $1" "$census_pid" "${2:-60}"
  census_code="$exit_code"
  printf '%s\n' "--- the census $1 exited $census_code:"
  cat "$work/$1.out"
}

# wait_state SECTION STATE COMMAND…: runs COMMAND every tenth of a second until it succeeds, for at
# most 60 seconds, and fails naming STATE where it never does.
wait_state() {
  local section="$1" state="$2" tries=0
  shift 2
  until "$@"; do
    tries=$((tries + 1))
    [[ "$tries" -lt "$state_limit" ]] || fail "$section" "wait for $state" "waited 60s for $state, and it did not come"
    sleep 0.1
  done
}

# --- sessions the driver holds open ----------------------------------------------------------

declare -A session_fd=() session_pid=() session_backend=() session_url=()

# unheld: closes, in a child process, the pipe of every session this shell holds, so that the
# driver alone holds the write end of each, and a session's psql ends when the driver closes it.
unheld() {
  local fd
  for fd in "${session_fd[@]}"; do
    exec {fd}>&-
  done
}

# open_session NAME URL: a psql the driver feeds through a pipe it keeps, so its transaction stays
# open between statements. It names itself `stores-moved NAME` in pg_stat_activity, sets
# lock_timeout to 30 seconds, and leaves its backend's pid in session_backend[NAME].
open_session() {
  local name="$1" url="$2" fd
  mkfifo "$work/session.$name.in"
  (
    unheld
    PGAPPNAME="stores-moved $name" exec env -u PGHOSTADDR -u PGSERVICE -u PGSERVICEFILE \
      psql "$url" -X -q -At -v ON_ERROR_STOP=1 <"$work/session.$name.in"
  ) >"$work/session.$name.out" 2>&1 &
  session_pid[$name]=$!
  exec {fd}>"$work/session.$name.in"
  session_fd[$name]=$fd
  session_url[$name]="$url"
  printf '%s\n' "set lock_timeout = '30s';" >&"$fd"
  wait_state setup "the session $name to connect" session_connected "$name"
  session_backend[$name]="$answer"
}
session_connected() {
  answer="$(sql "${session_url[$1]}" "select pid from pg_stat_activity
    where application_name = 'stores-moved $1' and datname = current_database()")" && [[ -n "$answer" ]]
}

# say NAME SQL: sends one statement to the session NAME, and returns at once.
say() { printf '%s\n' "$2" >&"${session_fd[$1]}"; }

# settled NAME: the session's last statement has ended: its backend is idle, in its transaction or
# out of it. It fails where the statement failed, printing what psql said.
settled() {
  local state
  kill -0 "${session_pid[$1]}" 2>/dev/null || fail roll "the session $1" "psql exited: $(<"$work/session.$1.out")"
  state="$(sql "${session_url[$1]}" "select state from pg_stat_activity where pid = ${session_backend[$1]}")" || return 1
  case "$state" in
    'idle in transaction (aborted)') fail roll "the session $1" "its statement failed: $(<"$work/session.$1.out")" ;;
    'idle in transaction' | idle) return 0 ;;
  esac
  return 1
}

# run_in SECTION WHAT NAME SQL: sends SQL to the session NAME and waits until it has ended.
run_in() {
  say "$3" "$4"
  wait_state "$1" "$2" settled "$3"
}

# close_session NAME: ends the session's psql by closing its pipe.
close_session() {
  local fd="${session_fd[$1]:-}"
  [[ -n "$fd" ]] || return 0
  exec {fd}>&-
  wait "${session_pid[$1]}" 2>/dev/null || true
  unset "session_fd[$1]"
}

# --- the fixtures, through the base server's API ----------------------------------------------

# fixture_actor: the agent session the fixtures' writes act for, and the daemon's session, which
# alone writes a run's record (legionDaemonSessionOf, api/steps.go).
fixture_actor() { jq -cn --arg s "$session" '{kind: "session", id: $s}'; }
daemon_actor() { jq -cn --arg s "legion-daemon:$project" '{kind: "session", id: $s}'; }

# create_issue SECTION TITLE: an issue of the project, its key in $created.
create_issue() {
  send POST /api/v1/issues "$(jq -cn --arg p "$project" --arg t "$2" --argjson a "$(fixture_actor)" \
    '{project: $p, title: $t, spec: "An issue of the stores fixture.", force: true, actor: $a}')"
  want_status 201 "$1" "create the issue \"$2\""
  created="$(jq -er '.key' "$work/body")" || fail "$1" "create the issue \"$2\"" "no key: $(body)"
}

# create_priority SECTION TITLE: a priority, its id in $created.
create_priority() {
  send POST /api/v1/priorities "$(jq -cn --arg t "$2" --argjson a "$(fixture_actor)" '{title: $t, actor: $a}')"
  want_status 201 "$1" "create the priority \"$2\""
  created="$(jq -er '.id' "$work/body")" || fail "$1" "create the priority \"$2\"" "no id: $(body)"
}

# set_advances SECTION ISSUE IDS-JSON: the issue's advances set, one act at a time.
set_advances() {
  send PATCH "/api/v1/issues/$2" "$(jq -cn --argjson ids "$3" --argjson a "$(fixture_actor)" '{advances: $ids, actor: $a}')"
  want_status 200 "$1" "write the advances of $2 as $3"
}

# put_step SECTION ISSUE ID JSON: one write of a run's record, as the daemon writes it.
put_step() {
  send PUT "/api/v1/issues/$2/steps/$3" "$(jq -c --argjson a "$(daemon_actor)" '. + {actor: $a}' <<<"$4")"
  want_status 200 "$1" "write the run $3 of $2"
}

# whole_run ISSUE ENDED REPORT: a run's whole identity, with its end and its report where given. Its
# task is the issue's key in lowercase, a task id's form (taskForm, workflowstore/write.go).
whole_run() {
  jq -cn --arg task "t-${1,,}" --arg ended "$2" --arg report "$3" '{task: $task, kind: "step", step: 1,
    role: "implementer", type: "code", generation: 1, agent: "omp", session: "s-stores-moved",
    started_at: "2026-11-23T10:00:00Z"}
    + (if $ended == "" then {} else {ended_at: $ended, result: "passed"} end)
    + (if $report == "" then {} else {report: $report} end)'
}

# write_run SECTION ISSUE ID: a run, written twice as the daemon writes it, at its start and at its
# end, each time with its whole identity.
write_run() {
  put_step "$1" "$2" "$3" "$(whole_run "$2" "" "")"
  put_step "$1" "$2" "$3" "$(whole_run "$2" "2026-11-23T10:30:00Z" "")"
}

# write_fixtures SECTION URL: the fixtures of the check through the base server at URL. They are
# three priorities, a move of one in rank, the advances of two issues with one retracted, the
# delete of one priority, two runs of one issue (one with a report naming a handoff), the plan
# that issue ran from, a rating of its retro's run, and a finished issue that Legion never ran.
# Each id and key lands in fixture[…].
declare -A fixture=()
write_fixtures() {
  local section="$1"
  on "$2"
  sign_in "$section" "$login"
  send POST /api/v1/projects "$(jq -cn --arg k "$project" --arg d "$login" --argjson a "$(fixture_actor)" \
    '{key: $k, name: "The stores fixture", default_assignee: $d, actor: $a}')"
  [[ "$status" == 201 || "$status" == 409 ]] || fail "$section" "create the project $project" "HTTP $status: $(body)"
  local name
  for name in ran retracted written third finished; do
    create_issue "$section" "The $name issue of the stores fixture"
    fixture[$name]="$created"
  done
  for name in first second deleted; do
    create_priority "$section" "The $name priority of the stores fixture $stamp"
    fixture[$name]="$created"
  done
  send PATCH "/api/v1/priorities/${fixture[second]}" "$(jq -cn --arg b "${fixture[first]}" --argjson a "$(fixture_actor)" \
    '{rank: {before: $b}, actor: $a}')"
  want_status 200 "$section" "move the priority ${fixture[second]} above ${fixture[first]}"
  set_advances "$section" "${fixture[ran]}" "[\"${fixture[first]}\"]"
  set_advances "$section" "${fixture[retracted]}" "[\"${fixture[second]}\"]"
  set_advances "$section" "${fixture[retracted]}" '[]'
  send DELETE "/api/v1/priorities/${fixture[deleted]}" "$(jq -cn --argjson a "$(fixture_actor)" '{actor: $a}')"
  want_status 204 "$section" "delete the priority ${fixture[deleted]}"

  send POST "/api/v1/issues/${fixture[ran]}/handoffs/implement" "$(jq -cn --argjson a "$(daemon_actor)" \
    '{body: "{\"summary\": \"the implementer handed off\"}", actor: $a}')"
  want_status 201 "$section" "write the implementer's handoff of ${fixture[ran]}"
  fixture[handoff]="$(jq -er '.handoff_id // .id' "$work/body")" || fail "$section" "write the handoff" "no id: $(body)"
  fixture[plan]="$(uuid)"
  send PUT "/api/v1/issues/${fixture[ran]}/plans/${fixture[plan]}" "$(jq -cn --argjson a "$(daemon_actor)" \
    '{generation: 1, source: "default", tasks: [{id: "t1", steps: ["implementer", "reviewer"]}], actor: $a}')"
  want_status 200 "$section" "write the plan ${fixture[plan]} of ${fixture[ran]}"
  fixture[run]="$(uuid)"
  put_step "$section" "${fixture[ran]}" "${fixture[run]}" "$(whole_run "${fixture[ran]}" "" "")"
  put_step "$section" "${fixture[ran]}" "${fixture[run]}" \
    "$(whole_run "${fixture[ran]}" "2026-11-23T10:30:00Z" "${fixture[handoff]}")"
  fixture[retro]="$(uuid)"
  put_step "$section" "${fixture[ran]}" "${fixture[retro]}" "$(jq -cn '{task: "retro", kind: "retro", step: 0,
    role: "retro", generation: 1, started_at: "2026-11-23T11:00:00Z", ended_at: "2026-11-23T11:10:00Z", result: "passed"}')"
  send PUT "/api/v1/issues/${fixture[ran]}/steps/${fixture[retro]}/rating" \
    "$(jq -cn --argjson a "$(daemon_actor)" '{rating: 4, rating_reason: "The plan fit the work.", actor: $a}')"
  want_status 200 "$section" "rate the plan through the retro's run ${fixture[retro]}"
  for name in ran finished; do
    send PATCH "/api/v1/issues/${fixture[$name]}" "$(jq -cn --argjson a "$(fixture_actor)" '{status: "done", actor: $a}')"
    want_status 200 "$section" "close ${fixture[$name]}"
  done
  pass "$section" "wrote the fixtures through the base server: priorities ${fixture[first]}, ${fixture[second]}, ${fixture[deleted]} (deleted); runs ${fixture[run]} and ${fixture[retro]} and the plan ${fixture[plan]} of ${fixture[ran]}; ${fixture[finished]}, finished and never run"
}

# uuid: a new uuid, from the kernel.
uuid() { cat /proc/sys/kernel/random/uuid; }

# read_fixtures SECTION URL DIR: the reads the check compares across the move, each answer kept in
# DIR: the priorities, each priority's history, the issue list with advances set to each priority,
# to none and to any, the steps of the issue that ran, and the project's plans. Of the priorities
# read it keeps the list of priorities without each one's count of work, and leaves out the two
# fault lists: both move with the advances the driver writes after the inventory, which it reads
# back on their own.
read_fixtures() {
  local section="$1" dir="$3" id filter
  on "$2"
  mkdir -p "$dir"
  send GET /api/v1/priorities
  want_status 200 "$section" "read the priorities"
  jq -S '.priorities | map(del(.work))' "$work/body" >"$dir/priorities.json" ||
    fail "$section" "read the priorities" "unreadable: $(body)"
  for id in "${fixture[first]}" "${fixture[second]}" "${fixture[deleted]}"; do
    send GET "/api/v1/priorities/$(q "$id")/history"
    want_status 200 "$section" "read the history of $id"
    cp "$work/body" "$dir/history.$id.json"
  done
  for filter in "${fixture[first]}" "${fixture[second]}" "${fixture[deleted]}" none any; do
    send GET "/api/v1/issues?project=$(q "$project")&advances=$(q "$filter")"
    want_status 200 "$section" "read the issue list with advances=$filter"
    jq -S '[.[] | {key, advances, advances_revision, inherited_advances}] | sort_by(.key)' "$work/body" >"$dir/advances.$filter.json" ||
      fail "$section" "read the issue list with advances=$filter" "unreadable: $(body)"
  done
  send GET "/api/v1/issues/${fixture[ran]}/steps"
  want_status 200 "$section" "read the steps of ${fixture[ran]}"
  cp "$work/body" "$dir/steps.json"
  send GET "/api/v1/projects/$(q "$project")/plans"
  want_status 200 "$section" "read the plans of $project"
  cp "$work/body" "$dir/plans.json"
}

# same_reads SECTION BEFORE AFTER: each read kept in BEFORE equals the one in AFTER, but for
# advances_revision, and for the rows of an issue or a priority that a write after the inventory
# touched (touched_issues, touched_priorities), which the driver reads back on their own.
same_reads() {
  local section="$1" before="$2" after="$3" file name differs
  local touched
  touched="$(printf '%s\n' "${touched_issues[@]}" "${touched_priorities[@]}" | jq -R . | jq -sc .)"
  for file in "$before"/*.json; do
    name="${file##*/}"
    [[ -r "$after/$name" ]] || fail "$section" "read $name again" "no such read after"
    differs="$(jq -n --slurpfile b "$file" --slurpfile a "$after/$name" --argjson touched "$touched" '
      def scrub: walk(if type == "object" then del(.advances_revision) else . end);
      def untouched: if type == "array"
        then map(select((type != "object") or ((.key // .id // .issue // "") as $k | ($touched | index($k)) | not)))
        else . end;
      # project(e): the later value read on the shape of the earlier one, e: a field the later
      # read adds is no difference, and a field the earlier read holds that differs is.
      def project($e): if ($e | type) == "object" and type == "object"
        then with_entries(select(.key as $k | $e | has($k)) | .key as $k | .value |= project($e[$k]))
        elif ($e | type) == "array" and type == "array" and length == ($e | length)
        then [range(0; length) as $i | .[$i] | project($e[$i])]
        else . end;
      ($b[0] | scrub | untouched) as $e | ($a[0] | scrub | untouched) as $l
      | if ($l | project($e)) == $e then empty else {before: $e, after: ($l | project($e))} end')" ||
      fail "$section" "compare $name" "jq failed"
    [[ -z "$differs" ]] || fail "$section" "the read $name equals its read before" "$differs"
  done
  pass "$section" "every read before ($(find "$before" -name '*.json' | wc -l) reads) reads the same, advances_revision aside"
}

# take_inventory SECTION URL: every row of the four tables, copied into the schema stores_inventory
# of the same database, with the moment it was taken; it prints how many rows each table holds.
take_inventory() {
  local section="$1" url="$2" table counts
  sql "$url" "drop schema if exists stores_inventory cascade; create schema stores_inventory;
    create table stores_inventory.taken as select clock_timestamp() as at;" >/dev/null 2>"$work/psql" ||
    fail "$section" "take the inventory" "psql: $(<"$work/psql")"
  for table in "${old_tables[@]}"; do
    sql "$url" "create table stores_inventory.$table as table public.$table" >/dev/null 2>"$work/psql" ||
      fail "$section" "take the inventory of $table" "psql: $(<"$work/psql")"
  done
  counts="$(sql "$url" "select string_agg(t || ' ' || n, ', ') from (
    select 'priority_nodes' as t, count(*) as n from stores_inventory.priority_nodes union all
    select 'priority_tuples', count(*) from stores_inventory.priority_tuples union all
    select 'workflow_nodes', count(*) from stores_inventory.workflow_nodes union all
    select 'workflow_tuples', count(*) from stores_inventory.workflow_tuples) c")" ||
    fail "$section" "count the inventory" "psql failed"
  pass "$section" "took the inventory of the four tables: $counts"
}

# inventory_count URL TABLE: how many rows the inventory holds of TABLE.
inventory_count() { sql "$1" "select count(*) from stores_inventory.$2"; }

# untouched_old SECTION URL: the four tables stand, no table stores_moved_seqs exists, and the
# graph store holds no row of the three kinds, so a rehearsal's transaction left nothing.
untouched_old() {
  local section="$1" url="$2" kinds graph
  kinds="$(sql "$url" "select string_agg(n || '=' || coalesce(c.relkind::text, 'none'), ' ' order by n)
    from unnest(array['priority_nodes', 'priority_tuples', 'workflow_nodes', 'workflow_tuples', 'stores_moved_seqs']) n
    left join pg_class c on c.oid = to_regclass(n)")" || fail "$section" "read pg_class" "psql failed"
  [[ "$kinds" == "priority_nodes=r priority_tuples=r stores_moved_seqs=none workflow_nodes=r workflow_tuples=r" ]] ||
    fail "$section" "the four tables stand and no table stores_moved_seqs exists" "pg_class reads $kinds"
  graph="$(sql "$url" "select (select count(*) from graph_nodes where kind in ($moved_kinds))
    + (select count(*) from graph_tuples where from_kind in ($moved_kinds))
    + (select count(*) from graph_tuples where to_kind = 'priority' and kind = 'advances')")" ||
    fail "$section" "count the graph store's rows of the three kinds" "psql failed"
  [[ "$graph" == 0 ]] || fail "$section" "the graph store holds no row of the three kinds" \
    "it holds $graph: $(sql "$url" "select kind || ':' || id from graph_nodes where kind in ($moved_kinds) limit 5")"
  pass "$section" "the four tables stand, no table stores_moved_seqs exists and the graph store holds no row of priority, step or plan"
}

# driver_census SECTION URL: the driver's own census, the inventory against the graph store, both
# directions, counted and digested, as the census under the swap's locks reads them, leaving out
# every write the driver made after the inventory (each dated after it was taken). It prints the
# count of each kind and fails on a difference, naming the first, and on a kind with no row.
driver_census() {
  local section="$1" url="$2" report
  report="$(sql "$url" "$(census_sql)" 2>"$work/psql")" || fail "$section" "take the driver's census" "psql: $(<"$work/psql")"
  printf '%s\n' "$report"
  if grep -q '^differs ' <<<"$report"; then
    fail "$section" "the driver's census reads no difference" "$(grep -m 1 '^differs ' <<<"$report")"
  fi
  if grep -Eq '^kind (priority|step|plan) 0$' <<<"$report"; then
    fail "$section" "the driver's census reads a row of each kind" "$(grep -E '^kind ' <<<"$report" | tr '\n' ' ')"
  fi
  pass "$section" "the driver's census reads the inventory and the store equal: $(grep -E '^kind ' <<<"$report" | tr '\n' ' ')"
}

# census_sql: the statement driver_census runs. Each side is a set of rows, a node row by its kind
# and id and a tuple by its node and its place among that node's tuples in the order of seq, so
# the two compare as sets; the values the move states of a run or a plan (its visibility and its
# state, and its issue's project where it holds none) are compared with the old row's columns.
census_sql() {
  cat <<'SQL'
with taken as (select at from stores_inventory.taken),
old_nodes as (
  select kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event from stores_inventory.priority_nodes
  union all
  select kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event from stores_inventory.workflow_nodes
),
moved_nodes as (
  select kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event from graph_nodes
  where kind in ('priority', 'step', 'plan') and made_at <= (select at from taken)
),
old_tuples as (
  select from_kind, from_id, row_number() over (partition by from_kind, from_id order by seq) as place,
         kind, to_kind, to_id, at, who, source_event, asserted
  from (select * from stores_inventory.priority_tuples union all select * from stores_inventory.workflow_tuples) o
),
moved_tuples as (
  select from_kind, from_id, row_number() over (partition by from_kind, from_id order by seq) as place,
         kind, to_kind, to_id, at, who, source_event, asserted
  from graph_tuples g
  where g.at <= (select at from taken)
    and (g.from_kind in ('priority', 'step', 'plan') or (g.to_kind = 'priority' and g.kind = 'advances'))
    and not (g.from_kind in ('step', 'plan') and g.to_kind = 'value' and (g.kind in ('visibility', 'state')
         or g.kind = 'project' and exists (select 1 from stores_inventory.workflow_nodes w
           where w.kind = g.from_kind and w.id = g.from_id and not w.attributes ? 'project')))
),
old_stated as (
  select w.kind as from_kind, w.id as from_id, s.kind, s.value as to_id, w.made_at as at, w.made_by as who, w.source_event
  from stores_inventory.workflow_nodes w
  left join issues i on i.key = w.attributes ->> 'issue'
  cross join lateral (values ('visibility', w.visibility), ('state', w.state), ('project', i.project_key)) s(kind, value)
  where s.kind <> 'project' or not w.attributes ? 'project'
),
moved_stated as (
  select g.from_kind, g.from_id, g.kind, g.to_id, g.at, g.who, g.source_event
  from graph_tuples g join stores_inventory.workflow_nodes w on w.kind = g.from_kind and w.id = g.from_id
  where g.to_kind = 'value' and (g.kind in ('visibility', 'state') or g.kind = 'project' and not w.attributes ? 'project')
),
node_diff as (
  select coalesce(o.kind, m.kind) as kind, coalesce(o.id, m.id) as id from old_nodes o full join moved_nodes m using (kind, id)
  where o.kind is null or m.kind is null
     or (o.supertag, o.visibility, o.state, o.attributes, o.made_at, o.made_by, o.source_event)
        is distinct from (m.supertag, m.visibility, m.state, m.attributes, m.made_at, m.made_by, m.source_event)
),
tuple_diff as (
  select coalesce(o.from_kind, m.from_kind) as kind, coalesce(o.from_id, m.from_id) as id, coalesce(o.place, m.place) as place
  from old_tuples o full join moved_tuples m using (from_kind, from_id, place)
  where o.from_kind is null or m.from_kind is null
     or (o.kind, o.to_kind, o.to_id, o.at, o.who, o.source_event, o.asserted)
        is distinct from (m.kind, m.to_kind, m.to_id, m.at, m.who, m.source_event, m.asserted)
),
stated_diff as (
  select coalesce(o.from_kind, m.from_kind) as kind, coalesce(o.from_id, m.from_id) as id, coalesce(o.kind, m.kind) as what
  from old_stated o full join moved_stated m using (from_kind, from_id, kind)
  where o.from_kind is null or m.from_kind is null or (o.to_id, o.at, o.who, o.source_event) is distinct from (m.to_id, m.at, m.who, m.source_event)
)
select 'kind ' || k || ' ' || (select count(*) from moved_nodes where kind = k) from unnest(array['priority', 'step', 'plan']) k
union all
select 'node rows: old ' || (select count(*) from old_nodes) || ' (md5 ' || (select md5(coalesce(string_agg(n::text, E'\n' order by kind, id), '')) from old_nodes n)
  || '), moved ' || (select count(*) from moved_nodes) || ' (md5 ' || (select md5(coalesce(string_agg(n::text, E'\n' order by kind, id), '')) from moved_nodes n) || ')'
union all
select 'tuples: old ' || (select count(*) from old_tuples) || ' (md5 ' || (select md5(coalesce(string_agg(t::text, E'\n' order by from_kind, from_id, place), '')) from old_tuples t)
  || '), moved ' || (select count(*) from moved_tuples) || ' (md5 ' || (select md5(coalesce(string_agg(t::text, E'\n' order by from_kind, from_id, place), '')) from moved_tuples t) || ')'
union all
select 'stated values: old ' || (select count(*) from old_stated) || ', moved ' || (select count(*) from moved_stated)
union all
(select 'differs node row kind=' || kind || ' id=' || id from node_diff order by kind, id limit 1)
union all
(select 'differs tuple kind=' || kind || ' id=' || id || ' place=' || place from tuple_diff order by kind, id, place limit 1)
union all
(select 'differs stated value kind=' || kind || ' id=' || id || ' ' || what from stated_diff order by kind, id, what limit 1)
SQL
}

# projection_census SECTION URL: each node row of priority, step and plan against the value tuples
# it projects, both directions, counted and digested: each attribute of the row against the newest
# asserted value tuple of its name, and the row's visibility, state and project_key against the
# newest visibility, state and project value tuples. A plan's tasks is compared as JSON. Then the
# two counts: the runs and plans whose issue attribute names another issue than their part_of, and
# those whose project_key is null or names another project than their issue's.
projection_census() {
  local section="$1" url="$2" report
  report="$(sql "$url" "$(projection_sql)" 2>"$work/psql")" || fail "$section" "read the node rows against their tuples" "psql: $(<"$work/psql")"
  printf '%s\n' "$report"
  if grep -q '^differs ' <<<"$report"; then
    fail "$section" "each node row of priority, step and plan equals the value tuples it projects" "$(grep '^differs ' <<<"$report" | head -n 3)"
  fi
  pass "$section" "each node row of priority, step and plan equals the value tuples it projects, both ways"
  grep -q '^other issue 0$' <<<"$report" ||
    fail "$section" "no run or plan names another issue than its part_of" "$(grep '^other issue' <<<"$report")"
  pass "$section" "no run or plan names another issue than its part_of"
  grep -q '^other project 0$' <<<"$report" ||
    fail "$section" "no run or plan has a null project_key or another project than its issue's" "$(grep '^other project' <<<"$report")"
  pass "$section" "no run or plan has a null project_key or another project than its issue's"
}

projection_sql() {
  cat <<'SQL'
with nodes as (select * from graph_nodes where kind in ('priority', 'step', 'plan')),
row_side as (
  select n.kind, n.id, a.key as what,
         case when jsonb_typeof(a.value) in ('array', 'object') then a.value::text else a.value #>> '{}' end as value
  from nodes n cross join lateral jsonb_each(n.attributes) a
  where a.key not in ('visibility', 'state', 'project')
  union all
  select n.kind, n.id, c.what, c.value from nodes n
  cross join lateral (values ('visibility', n.visibility), ('state', n.state), ('project', n.project_key)) c(what, value)
  where c.what <> 'project' or n.project_key is not null
),
newest as (
  select distinct on (t.from_kind, t.from_id, t.kind) t.from_kind as kind, t.from_id as id, t.kind as what, t.to_id, t.asserted
  from graph_tuples t join nodes n on n.kind = t.from_kind and n.id = t.from_id
  where t.to_kind = 'value'
  order by t.from_kind, t.from_id, t.kind, t.at desc, t.seq desc
),
tuple_side as (
  select kind, id, what,
         case when what = 'tasks' then to_id::jsonb::text else to_id end as value
  from newest where asserted
),
row_norm as (
  select kind, id, what, case when what = 'tasks' then value::jsonb::text else value end as value from row_side
),
diff as (
  select coalesce(r.kind, t.kind) as kind, coalesce(r.id, t.id) as id, coalesce(r.what, t.what) as what
  from row_norm r full join tuple_side t using (kind, id, what)
  where r.kind is null or t.kind is null or r.value is distinct from t.value
)
select 'projection rows ' || (select count(*) from row_norm) || ' (md5 ' || (select md5(coalesce(string_agg(r::text, E'\n' order by kind, id, what), '')) from row_norm r)
  || '), tuples ' || (select count(*) from tuple_side) || ' (md5 ' || (select md5(coalesce(string_agg(t::text, E'\n' order by kind, id, what), '')) from tuple_side t) || ')'
union all
(select 'differs ' || kind || ' ' || id || ' ' || what from diff order by kind, id, what limit 3)
union all
select 'other issue ' || count(*) || coalesce(', first ' || min(n.kind || ' ' || n.id), '') from graph_nodes n
where n.kind in ('step', 'plan') and (n.attributes ->> 'issue') is distinct from (
  select case when t.asserted and t.to_kind = 'issue' then t.to_id end from graph_tuples t
  where t.from_kind = n.kind and t.from_id = n.id and t.kind = 'part_of' order by t.at desc, t.seq desc limit 1)
union all
select 'other project ' || count(*) || coalesce(', first ' || min(n.kind || ' ' || n.id), '') from graph_nodes n
left join issues i on i.key = n.attributes ->> 'issue'
where n.kind in ('step', 'plan') and (n.project_key is null or n.project_key is distinct from i.project_key)
SQL
}

# graph_digest URL: the count and digest of every row of graph_nodes and graph_tuples.
graph_digest() {
  sql "$1" "select (select count(*) || ' ' || md5(coalesce(string_agg(n::text, E'\n' order by kind, id), '')) from graph_nodes n)
    || ' / ' || (select count(*) || ' ' || md5(coalesce(string_agg(t::text, E'\n' order by seq), '')) from graph_tuples t)"
}

# --- pg_locks of the move ---------------------------------------------------------------------

# move_backend URL: the pid of the move's connection in the database URL names, the oldest where
# two servers' moves are connected, in $answer.
move_backend() {
  answer="$(sql "$1" "select pid from pg_stat_activity where application_name = '$move_application'
    and datname = current_database() order by backend_start limit 1")" && [[ -n "$answer" ]]
}

# move_locks URL PID: the relation locks of the backend PID, as the fixtures hold them.
move_locks() {
  sql_csv "$1" "select l.relation::regclass::text as relation, l.mode, l.granted from pg_locks l
    where l.pid = $2 and l.locktype = 'relation' order by 1, 2"
}

# advisory_rows URL: the rows of pg_locks of the advisory lock stores-moved, as the fixtures hold
# them.
advisory_rows() {
  sql_csv "$1" "select a.backend_start::text <> '' as known, l.granted from pg_locks l join pg_stat_activity a on a.pid = l.pid
    where l.locktype = 'advisory' and l.objsubid = 1 and ((l.classid::bigint << 32) | l.objid::bigint) = hashtext('stores-moved')::bigint
    order by a.backend_start"
}

# advisory_reads URL WANT: the advisory lock stores-moved reads `GRANTED WAITING` as WANT.
advisory_reads() {
  local rows
  rows="$(advisory_rows "$1")" || return 1
  [[ "$(advisory_counts "$rows")" == "$2" ]]
}

# --- reads of the fixtures --------------------------------------------------------------------

# issue_reads SECTION URL DIR: the issue read and the tree read of each fixture issue, kept in DIR.
issue_reads() {
  local section="$1" dir="$3" name
  on "$2"
  mkdir -p "$dir"
  for name in ran retracted written third finished; do
    send GET "/api/v1/issues/${fixture[$name]}"
    want_status 200 "$section" "read the issue ${fixture[$name]}"
    jq -S 'del(.advances_revision)' "$work/body" >"$dir/issue.${fixture[$name]}.json"
    send GET "/api/v1/issues/${fixture[$name]}/tree"
    want_status 200 "$section" "read the tree of ${fixture[$name]}"
    jq -S . "$work/body" >"$dir/tree.${fixture[$name]}.json"
  done
}

# revision_of SECTION URL KEY: the advances_revision the issue read answers, in $answer.
revision_of() {
  on "$2"
  send GET "/api/v1/issues/$3"
  want_status 200 "$1" "read the issue $3"
  answer="$(jq -er '.advances_revision' "$work/body")" || fail "$1" "read the advances_revision of $3" "$(body)"
}

# lists SECTION WHAT JQ WANT: the last answer's body, read through JQ, holds WANT.
lists() {
  local held
  held="$(jq -r "$3" "$work/body" 2>/dev/null)" || fail "$1" "$2" "unreadable: $(body)"
  grep -Fxq -- "$4" <<<"$held" || fail "$1" "$2" "it does not hold $4: $(body)"
  pass "$1" "$2"
}

# read_back SECTION URL: every write the driver made after the inventory, read back through the
# server at URL: each priority in the priorities read, each advances relation in its issue's read,
# each run in its issue's steps.
read_back() {
  local section="$1" entry kind subject object
  on "$2"
  for entry in "${written_after[@]}"; do
    IFS=' ' read -r kind subject object <<<"$entry"
    case "$kind" in
      priority)
        send GET /api/v1/priorities
        want_status 200 "$section" "read the priorities"
        lists "$section" "the priority $subject reads back" '.priorities[].id' "$subject"
        ;;
      advances)
        send GET "/api/v1/issues/$subject"
        want_status 200 "$section" "read the issue $subject"
        lists "$section" "$subject advances $object" '.advances[]? | if type == "object" then .id // .priority else . end' "$object"
        ;;
      run)
        send GET "/api/v1/issues/$subject/steps"
        want_status 200 "$section" "read the steps of $subject"
        lists "$section" "the run $object of $subject reads back" '.tasks[].steps[].id' "$object"
        ;;
    esac
  done
}

# wrote KIND SUBJECT [OBJECT]: one write after the inventory, which read_back reads and the
# driver's census and same_reads leave out.
wrote() {
  written_after+=("$1 $2 ${3:-}")
  case "$1" in
    priority) touched_priorities+=("$2") ;;
    advances | run) touched_issues+=("$2") ;;
  esac
}

# no_rehearsal SECTION WHAT NAME: the census NAME exited 0 and printed no line of a rehearsal.
no_rehearsal() {
  [[ "$census_code" == 0 ]] || fail "$1" "$2" "the census exited $census_code"
  [[ -z "$(rehearsal_lines "$work/$3.out")" ]] || fail "$1" "$2" "it printed a rehearsal: $(rehearsal_lines "$work/$3.out" | awk 'NR <= 3')"
  pass "$1" "$2"
}

# no_deadlock SECTION: no line of this head's boot log or of a session's output says
# `deadlock detected`.
no_deadlock() {
  local line
  line="$(deadlock_line "$head_log" "$work"/session.*.out)"
  [[ -z "$line" ]] || fail "$1" "no transaction met a deadlock" "$line"
}

# --- the steps --------------------------------------------------------------------------------

# check_base: the release before holds both stores and the walk rule of WALK_RULE_ISSUE.
check_base() {
  local file walk
  base_commit="$(git -C "$checkout" rev-parse --verify --quiet "${BASELINE_SHA}^{commit}")" || {
    printf 'BASELINE_SHA %s names no commit of %s\n' "$BASELINE_SHA" "$checkout" >&2
    usage
    exit 2
  }
  for file in 20261109013400_graph_store 20261112024320_priority_nodes 20261116235202_workflow_nodes; do
    git -C "$checkout" cat-file -e "$base_commit:packages/envoy/internal/dispatch/store/migrations/$file.up.sql" 2>/dev/null ||
      fail base "the release before $base_commit holds $file.up.sql" "it lacks it, so it holds not both stores"
  done
  pass base "the release before $base_commit holds the graph store and both stores"
  walk="${WALK_RULE_COMMIT:-}"
  if [[ -z "$walk" ]]; then
    walk="$(cd "$checkout" && gh pr list --head "legion/$WALK_RULE_ISSUE" --state merged --json mergeCommit,mergedAt \
      --jq 'sort_by(.mergedAt) | last | .mergeCommit.oid // empty' 2>"$work/gh")" ||
      fail base "find the merge of legion/$WALK_RULE_ISSUE" "gh: $(<"$work/gh")"
  fi
  [[ -n "$walk" ]] || fail base "find the merge of legion/$WALK_RULE_ISSUE" "gh answers no merged pull request of that branch"
  git -C "$checkout" merge-base --is-ancestor "$walk" "$base_commit" 2>/dev/null ||
    fail base "the release before holds the merge of $WALK_RULE_ISSUE" "the release before lacks the merge of $WALK_RULE_ISSUE ($walk)"
  pass base "the release before holds the merge of $WALK_RULE_ISSUE, $walk"
  head_holds_walk=""
  if git -C "$checkout" merge-base --is-ancestor "$walk" HEAD 2>/dev/null; then
    head_holds_walk=1
  fi
}

# rehearse_planted: the census refuses the planted plan, names it by its address and exits 1, and
# its transaction leaves nothing.
rehearse_planted() {
  local planted refusal
  planted="$(uuid)"
  sql "$database_url" "insert into workflow_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event)
    values ('plan', '$planted', 'plan', 'project', 'live', jsonb_build_object('issue', '${fixture[ran]}', 'project', '$project',
      'generation', 2, 'source', 'default', 'tasks', '[{\"id\": \"t1\", \"steps\": [\"implementer\"]}]'::jsonb), now(), '$(fixture_actor)', 0);
    insert into workflow_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, asserted)
    values ('plan', '$planted', 'part_of', 'issue', '${fixture[third]}', now(), '$(fixture_actor)', 0, true)" >/dev/null 2>"$work/psql" ||
    fail rehearsal "plant a plan whose issue attribute names another issue than its part_of" "psql: $(<"$work/psql")"
  census_run census-planted "$database_url"
  census_wait census-planted
  refusal="$(rehearsal_refusal "$work/census-planted.out")"
  [[ "$census_code" == 1 ]] ||
    fail rehearsal "the census refuses the planted plan $planted and exits 1" "it exited $census_code where the driver planted a difference in the plan $planted"
  [[ "$refusal" == *"store=workflow node kind=plan id=$planted:"* ]] ||
    fail rehearsal "the census names the planted plan $planted by its address" "its refusal reads: ${refusal:-none}"
  pass rehearsal "the census refuses the planted plan by its address and exits 1: $refusal"
  untouched_old rehearsal "$database_url"
  sql "$database_url" "delete from workflow_tuples where from_kind = 'plan' and from_id = '$planted';
    delete from workflow_nodes where kind = 'plan' and id = '$planted'" >/dev/null ||
    fail rehearsal "delete the planted plan" "psql failed"
}

# census_holds: the held census has printed that its copy ended, and still runs.
census_holds() {
  kill -0 "$census_pid" 2>/dev/null || fail rehearsal "the census holds its rehearsal" "it exited: $(<"$work/census-held.out")"
  [[ -n "$(copy_ended "$work/census-held.out")" ]]
}

# rehearse_held: while the rehearsal stands, the base server's writes answer and commit, and a read
# of graph_edges answers; then the census exits 0 with counts equal to the inventory's.
rehearse_held() {
  local hold="$work/rehearsal.hold" priority run copy store what table counts want
  census_run census-held "$database_url" DISPATCH_TEST_HOOKS=1 DISPATCH_TEST_STORES_REHEARSAL_HOLD_FILE="$hold"
  wait_state rehearsal "the census to print that its rehearsal's copy ended" census_holds
  on "$base_url"
  create_priority rehearsal "Written while the rehearsal stands $stamp"
  priority="$created"
  set_advances rehearsal "${fixture[third]}" "[\"$priority\"]"
  run="$(uuid)"
  write_run rehearsal "${fixture[third]}" "$run"
  send GET "/api/v1/references?to=$(q "dispatch://${fixture[ran]}")"
  want_status 200 rehearsal "read the edges of ${fixture[ran]} through graph_edges while the rehearsal stands"
  kill -0 "$census_pid" 2>/dev/null || fail rehearsal "the rehearsal still stands after the writes" "the census exited"
  wrote priority "$priority"
  wrote advances "${fixture[third]}" "$priority"
  wrote run "${fixture[third]}" "$run"
  read_back rehearsal "$base_url"
  want rehearsal "the priority $priority, its advances tuple and the run $run read back committed by SQL" "$database_url" 3 \
    "select (select count(*) from priority_nodes where kind = 'priority' and id = '$priority')
      + (select count(*) from priority_tuples where from_id = '${fixture[third]}' and kind = 'advances' and to_id = '$priority' and asserted)
      + (select count(*) from workflow_nodes where kind = 'step' and id = '$run')"
  kill -0 "$census_pid" 2>/dev/null || fail rehearsal "the rehearsal still stands after the reads" "the census exited"
  pass rehearsal "the rehearsal held up no write: each answered and read back committed while it stood, and the read of graph_edges answered"
  touch "$hold"
  census_wait census-held
  [[ "$census_code" == 0 ]] || fail rehearsal "the held census exits 0" "it exited $census_code: $(rehearsal_refusal "$work/census-held.out")"
  copy="$(copy_ended "$work/census-held.out")"
  [[ -n "$copy" ]] || fail rehearsal "the census prints its copy's time" "no line says its copy ended"
  printf 'the rehearsal copied %s tuples in %s\n' "${copy% *}" "${copy#* }"
  for counts in "priority:node rows:priority_nodes" "priority:tuples:priority_tuples" \
    "workflow:node rows:workflow_nodes" "workflow:tuples:workflow_tuples"; do
    IFS=: read -r store what table <<<"$counts"
    want="$(inventory_count "$database_url" "$table")"
    [[ "$(rehearsal_count "$work/census-held.out" "$store" "$what")" == "$want $want 0" ]] ||
      fail rehearsal "the rehearsal's $store $what equal the inventory's $want" "it reads $(rehearsal_count "$work/census-held.out" "$store" "$what")"
  done
  pass rehearsal "the census exits 0 and its counts of each store equal the inventory's, its copy in ${copy#* }"
  untouched_old rehearsal "$database_url"
}

# rehearse_stopped: the rehearsal stops after 2 seconds, refuses naming the stop, exits 1 and
# leaves nothing.
rehearse_stopped() {
  local refusal
  census_run census-stopped "$database_url" DISPATCH_TEST_HOOKS=1 DISPATCH_TEST_STORES_REHEARSAL_STOP=2s \
    DISPATCH_TEST_STORES_REHEARSAL_HOLD_FILE="$work/never.hold"
  census_wait census-stopped
  refusal="$(rehearsal_refusal "$work/census-stopped.out")"
  [[ "$census_code" == 1 && "$refusal" == *"it stopped after 2s"* ]] ||
    fail rehearsal "the rehearsal stops after 2s, refuses naming the stop and exits 1" "it exited $census_code, refusing: ${refusal:-nothing}"
  pass rehearsal "the rehearsal stops after 2s and refuses: $refusal"
  untouched_old rehearsal "$database_url"
}

# without_row VERSION NAME [NAME=value…]: the census run NAME over the roll's database with the row
# of the migration VERSION deleted from schema_migrations, which it puts back after.
without_row() {
  local version="$1" name="$2"
  shift 2
  sql "$database_url" "drop table if exists stores_inventory.saved_$version;
    create table stores_inventory.saved_$version as select * from schema_migrations where version = $version;
    delete from schema_migrations where version = $version" >/dev/null 2>"$work/psql" ||
    fail pending "delete the row of $version" "psql: $(<"$work/psql")"
  census_run "$name" "$database_url" "$@"
  census_wait "$name"
  sql "$database_url" "insert into schema_migrations select * from stores_inventory.saved_$version" >/dev/null 2>"$work/psql" ||
    fail pending "put the row of $version back" "psql: $(<"$work/psql")"
}

# rehearse_pending: the census refuses a pending migration of the base that touches graph_nodes
# with exit 2, skips one that touches none of the six tables, and refuses with exit 2 a file that
# replaces the function of graph_nodes' two schema triggers.
rehearse_pending() {
  local name refused skipped extra="$work/block_schema_changed.up.sql"
  name="$supertags_migration.up.sql"
  without_row "${supertags_migration%%_*}" census-pending-supertags
  refused="$(refused_migrations "$work/census-pending-supertags.out")"
  [[ "$census_code" == 2 ]] || fail pending "the census exits 2 over the pending $name" "it exited $census_code, refusing: ${refused:-no pending migration}"
  grep -Fxq "$name" <<<"$refused" || fail pending "the census refuses the pending migration $name" "it refuses: ${refused:-none}"
  pass pending "the census refuses the pending migration $name and exits 2"
  name="$search_text_migration.up.sql"
  without_row "${search_text_migration%%_*}" census-pending-search-text
  [[ "$census_code" == 0 ]] || fail pending "the census exits 0 over the pending $name" "it exited $census_code: $(refused_migrations "$work/census-pending-search-text.out")"
  [[ -n "$(skipped_migration "$work/census-pending-search-text.out" "$name")" ]] ||
    fail pending "the census names $name as one the rehearsal skips" "no line says so"
  pass pending "the census skips the pending migration $name, whose functions no trigger of the six tables runs, and exits 0"
  git -C "$checkout" show "$base_commit:packages/envoy/internal/dispatch/store/migrations/$supertags_migration.up.sql" |
    awk '/^create or replace function block_schema_changed\(\)/ { on = 1 } on { print } on && /\$\$;$/ { exit }' >"$extra"
  grep -q '^create or replace function block_schema_changed()' "$extra" ||
    fail pending "write the file that replaces block_schema_changed" "$supertags_migration.up.sql at $base_commit gives no such statement"
  census_run census-extra "$database_url" DISPATCH_TEST_HOOKS=1 DISPATCH_TEST_STORES_REHEARSAL_EXTRA_MIGRATION="$extra"
  census_wait census-extra
  refused="$(refused_migrations "$work/census-extra.out")"
  skipped="$(skipped_migration "$work/census-extra.out" "$extra")"
  [[ "$census_code" == 2 ]] || fail pending "the census exits 2 over the file that replaces block_schema_changed" \
    "it exited $census_code${skipped:+, naming $extra as one the rehearsal skips}"
  grep -Fxq "$extra" <<<"$refused" || fail pending "the census refuses $extra" "it refuses: ${refused:-none}"
  pass pending "the census refuses the file that replaces block_schema_changed, the function of graph_nodes' schema triggers, and exits 2"
}

# t1_priority ID: the release before's write of one priority, its node row and its tuples through
# the old tables, as prioritystore.Create writes them.
t1_priority() {
  printf "%s" "begin; insert into priority_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event)
    values ('priority', '$1', 'priority', 'project', 'live', '{\"title\": \"Held open\", \"rank\": \"zz\"}', now(), '$(fixture_actor)', 0);
    insert into priority_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, asserted)
    select 'priority', '$1', s.kind, 'value', s.value, now(), '$(fixture_actor)', 0, true
    from (values (1, 'visibility', 'project'), (2, 'state', 'live'), (3, 'rank', 'zz'), (4, 'title', 'Held open')) s(place, kind, value) order by s.place;"
}

# whole_run_sql ID ISSUE: the release before's write of a run whole, in the statements of putNode
# and appendTuples (workflowstore/write.go): the insert that makes the node, the read of what it
# holds for update, the update of its attributes and the append of its tuples.
whole_run_sql() {
  local daemon
  daemon="$(daemon_actor)"
  printf "%s" "insert into workflow_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event)
    values ('step', '$1', 'step', 'project', 'live', '{}', now(), '$daemon', 0) on conflict (kind, id) do nothing;
    select attributes from workflow_nodes where kind = 'step' and id = '$1' for update;
    update workflow_nodes set attributes = attributes || '{\"issue\": \"$2\", \"project\": \"$project\", \"task\": \"t-held\",
      \"kind\": \"step\", \"step\": 1, \"role\": \"implementer\", \"generation\": 1}'::jsonb where kind = 'step' and id = '$1';
    insert into workflow_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, asserted)
    select 'step', '$1', s.kind, s.to_kind, s.to_id, now(), '$daemon', 0, true
    from (values (1, 'issue', 'value', '$2'), (2, 'project', 'value', '$project'), (3, 'generation', 'value', '1'),
      (4, 'task', 'value', 't-held'), (5, 'kind', 'value', 'step'), (6, 'step', 'value', '1'),
      (7, 'role', 'value', 'implementer'), (8, 'part_of', 'issue', '$2')) s(place, kind, to_kind, to_id) order by s.place;"
}

# swap_stage SECTION STATE: the move's backend's locks, read once: each wait on an old table keeps
# the rule (swap_verdict), no line says deadlock detected, and this head's boot still runs. It
# leaves the locks in $locks and the relation waited for in $waited, and fails where it waits for
# none.
swap_stage() {
  local verdict
  no_deadlock "$1"
  kill -0 "$head_pid" 2>/dev/null || fail "$1" "this head's boot runs while the driver waits for $2" "it exited: $(tail -n 12 "$head_log")"
  locks="$(move_locks "$database_url" "$move_pid")" || return 1
  waited="$(lock_waited "$locks")"
  [[ -n "$waited" ]] || return 1
  lock_reads=$((lock_reads + 1))
  printf '%s\n' "$locks" >"$work/locks.$lock_reads.csv"
  verdict="$(swap_verdict "$locks")" || fail "$1" "the swap's locks at its wait on $waited" "$verdict"
}

# swap_waits SECTION STATE TEST: reads the locks until TEST holds of them, for at most 60 seconds,
# and fails naming STATE where it never does.
swap_waits() {
  local section="$1" state="$2" test="$3" tries=0
  until swap_stage "$section" "$state" && eval "$test"; do
    tries=$((tries + 1))
    [[ "$tries" -lt "$state_limit" ]] || fail "$section" "wait for $state" "waited 60s for $state, and it did not come"
    sleep 0.1
  done
}

# roll_swap: the four transactions in the release before's shapes and run B, this head's boot over
# the same database while the base server serves, and each step on the state pg_locks shows.
roll_swap() {
  local hold="$work/move.hold" t1 t2 run_a run_b lines session_name
  # This head's migrations run before the transactions open: its concurrent index build waits for
  # every transaction that writes graph_nodes or is older than its snapshot, and the driver's
  # transactions end only once the swap waits. So the move holds stores-moved, before its copy,
  # until the driver has opened them.
  spawn head-roll "$head_binary" "$database_url" DISPATCH_TEST_HOOKS=1 DISPATCH_TEST_STORES_MOVE_BATCH=2 \
    DISPATCH_TEST_STORES_MOVE_HOLD_FILE="$hold"
  head_pid="$spawned_pid" head_log="$spawned_log"
  wait_state swap "this head's move holding stores-moved before its copy" advisory_reads "$database_url" "1 0"
  move_backend "$database_url" || fail swap "find the move's connection" "pg_stat_activity names none"
  move_pid="$answer"
  t1="held-open-$stamp" t2="$project-$((900 + RANDOM % 99))" run_a="$(uuid)" run_b="$(uuid)"
  open_session t1 "$database_url"
  open_session t2 "$database_url"
  open_session t3 "$database_url"
  open_session t4 "$database_url"
  run_in swap "the first transaction writes the priority $t1" t1 "$(t1_priority "$t1")"
  run_in swap "the second transaction creates $t2 in the create shape" t2 "begin;
    insert into issues (key, project_key, number, title, created_by, labels, priority, rank, node_kind, node_address)
    select '$t2', project_key, ${t2#*-}, 'Created in the create shape', created_by, labels, priority, rank || 'x', node_kind, '$t2'
    from issues where key = '${fixture[third]}';
    insert into graph_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event, project_key)
    select kind, '$t2', supertag, visibility, state, attributes, now(), made_by, source_event, project_key
    from graph_nodes where kind = 'issue' and id = '${fixture[third]}';"
  run_in swap "the third transaction patches ${fixture[written]}, reading the advances of ${fixture[ran]}" t3 "begin;
    select key from issues where key = '${fixture[written]}' for update;
    select t.to_id, t.seq, t.asserted from priority_tuples t
    left join priority_nodes live on live.kind = 'priority' and live.id = t.to_id and live.state = 'live'
    where t.from_kind = 'issue' and t.from_id = '${fixture[ran]}' and t.kind = 'advances';"
  run_in swap "the fourth transaction writes run A $run_a" t4 "begin; $(whole_run_sql "$run_a" "${fixture[third]}")"
  sql "$database_url" "begin; $(whole_run_sql "$run_b" "${fixture[third]}") commit;" >/dev/null 2>"$work/psql" ||
    fail swap "write and commit run B $run_b" "psql: $(<"$work/psql")"
  pass swap "four transactions stand open in the release before's shapes, and run B $run_b is committed beside run A $run_a"
  touch "$hold"

  lock_reads=0
  swap_waits swap "the swap waiting on one of the five tables" '[[ " graph_nodes ${old_tables[*]} " == *" $waited "* ]]'
  run_in swap "the first transaction commits" t1 "commit;"
  run_in swap "the fourth transaction commits" t4 "commit;"
  pass swap "the swap waits on $waited; the first and the fourth transactions committed"
  swap_waits swap "the swap waiting on graph_nodes" '[[ "$waited" == graph_nodes ]]'
  run_in swap "the second transaction reads ${fixture[first]} and appends the advances of $t2" t2 \
    "select id from priority_nodes where kind = 'priority' and id = '${fixture[first]}';
    insert into priority_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, asserted)
    values ('issue', '$t2', 'advances', 'priority', '${fixture[first]}', now(), '$(fixture_actor)', 0, true); commit;"
  pass swap "the swap waits on graph_nodes; the second transaction appended its advances tuple and committed"
  swap_waits swap "the swap holding graph_nodes and waiting on priority_nodes" \
    '[[ "$waited" == priority_nodes && " $(lock_modes "$locks" graph_nodes) " == *" ShareRowExclusiveLock "* ]]'
  run_in swap "the third transaction reads the backlinks of ${fixture[written]} within 100ms" t3 \
    "set local lock_timeout = '100ms';
    select count(*) from graph_nodes where kind = 'issue' and id = '${fixture[written]}';
    set local lock_timeout = '30s';"
  say t3 "update graph_nodes set attributes = attributes where kind = 'issue' and id = '${fixture[written]}';
    insert into priority_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, asserted)
    values ('issue', '${fixture[written]}', 'advances', 'priority', '${fixture[first]}', now(), '$(fixture_actor)', 0, true);"
  wait_state swap "the third transaction's write to return" settled t3
  run_in swap "the third transaction commits" t3 "commit;"
  pass swap "the swap holds graph_nodes and waits on priority_nodes; the third transaction read graph_nodes within 100ms, then wrote it behind the swap, and committed"
  for session_name in t1 t2 t3 t4; do
    close_session "$session_name"
  done
  listening swap head-roll "$head_pid" "$head_log"
  head_url="$listened"
  no_deadlock swap
  lines="$(gave_up_lines "$head_log" | wc -l)"
  [[ "$lines" -ge 1 ]] || fail swap "this head's boot log holds an attempt of the swap that gave up" "it holds none"
  pass swap "this head's move ended and it listens; its boot log holds $lines attempts of the swap that gave up, and no deadlock"
  wrote priority "$t1"
  wrote advances "$t2" "${fixture[first]}"
  wrote advances "${fixture[written]}" "${fixture[first]}"
  wrote run "${fixture[third]}" "$run_a"
  wrote run "${fixture[third]}" "$run_b"
}

# through_views: the base server writes one priority and one run, the run twice, through the views
# and the two triggers; the run holds one visibility and one state tuple and its project.
through_views() {
  local priority run
  on "$base_url"
  create_priority views "Written through the views $stamp"
  priority="$created"
  run="$(uuid)"
  write_run views "${fixture[third]}" "$run"
  want views "the run $run holds one visibility and one state tuple and the project_key $project" "$database_url" "1 1 $project" \
    "select count(*) filter (where t.kind = 'visibility') || ' ' || count(*) filter (where t.kind = 'state') || ' '
      || coalesce((select project_key from graph_nodes where kind = 'step' and id = '$run'), 'null')
     from graph_tuples t where t.from_kind = 'step' and t.from_id = '$run' and t.to_kind = 'value'"
  wrote priority "$priority"
  wrote run "${fixture[third]}" "$run"
  pass views "the base server wrote the priority $priority and the run $run through the views"
}

# moved_reads SECTION URL: what this head's server answers after the move.
moved_reads() {
  local section="$1" url="$2" revision before tree name node key
  read_back "$section" "$url"
  read_fixtures "$section" "$url" "$work/$section/after"
  same_reads "$section" "$work/roll/before" "$work/$section/after"
  for key in "${fixture[ran]}" "${fixture[retracted]}"; do
    revision_of "$section" "$url" "$key"
    revision="$answer"
    for before in "${revisions_before[@]}"; do
      [[ "$revision" -gt "$before" ]] ||
        fail "$section" "the advances_revision of $key is greater than every revision before the move" "it reads $revision, and one before read $before"
    done
    pass "$section" "the advances_revision of $key, $revision, is greater than every revision read before the move (${revisions_before[*]})"
  done
  # The write goes to the issue whose relation the fixtures retracted, which is open: the issue that
  # ran is closed, and a write of a closed issue is refused before its revision is read.
  on "$url"
  send PATCH "/api/v1/issues/${fixture[retracted]}" "$(jq -cn --arg p "${fixture[first]}" --argjson r "${revisions_before[1]}" \
    --argjson a "$(fixture_actor)" '{advances: [$p], advances_revision: $r, actor: $a}')"
  want_status 409 "$section" "a whole-set advances write of ${fixture[retracted]} with the revision ${revisions_before[1]} read before the move is refused"
  expect "$section" "the refusal is ADVANCES_REVISION and names the revision $revision the store holds" true \
    --arg r "$revision" '.code == "ADVANCES_REVISION" and (.error | test("(^|[^0-9])" + $r + "([^0-9]|$)"))'
  driver_census "$section" "$database_url"
  projection_census "$section" "$database_url"
  want "$section" "the run ${fixture[run]}'s reports tuple names the handoff ${fixture[handoff]}" "$database_url" "handoff ${fixture[handoff]}" \
    "select string_agg(to_kind || ' ' || to_id, ', ') from graph_tuples where from_kind = 'step' and from_id = '${fixture[run]}' and kind = 'reports'"
  if [[ -n "$head_holds_walk" ]]; then
    for name in ran third; do
      send GET "/api/v1/issues/${fixture[$name]}/tree"
      want_status 200 "$section" "read the tree of ${fixture[$name]}"
      tree="$(<"$work/body")"
      for node in "${fixture[run]}" "${fixture[retro]}" "${fixture[plan]}" "${fixture[handoff]}"; do
        [[ "$tree" != *"$node"* ]] || fail "$section" "the tree read of ${fixture[$name]} answers no run, plan or handoff" "it names $node"
      done
    done
    pass "$section" "the tree read of each issue answers no run, no plan and no handoff"
  fi
  want "$section" "pg_class holds the four names as views and no table stores_moved_seqs" "$database_url" \
    "priority_nodes=v priority_tuples=v stores_moved_seqs=none workflow_nodes=v workflow_tuples=v" \
    "select string_agg(n || '=' || coalesce(c.relkind::text, 'none'), ' ' order by n)
     from unnest(array['priority_nodes', 'priority_tuples', 'workflow_nodes', 'workflow_tuples', 'stores_moved_seqs']) n
     left join pg_class c on c.oid = to_regclass(n)"
}

# roll: the roll over DATABASE_URL, the rehearsal, the swap, the reads after and the rollback.
roll() {
  local key priority run base_pid
  baseline_start
  base_url="$baseline_url"
  write_fixtures roll "$base_url"
  read_fixtures roll "$base_url" "$work/roll/before"
  for key in "${fixture[ran]}" "${fixture[retracted]}"; do
    revision_of roll "$base_url" "$key"
    revisions_before+=("$answer")
  done
  take_inventory roll "$database_url"
  if [[ "${SKIP_PLANT:-}" != 1 ]]; then
    rehearse_planted
  fi
  rehearse_held
  rehearse_stopped
  rehearse_pending
  roll_swap
  through_views
  baseline_stop_server
  moved_reads moved "$head_url"
  census_run census-after-swap "$database_url"
  census_wait census-after-swap
  no_rehearsal moved "the census after the swap exits 0 and prints no rehearsal" census-after-swap

  # The rollback: the base boots again over the moved database and writes through the views, and
  # this head reads each write back.
  stop "$head_pid"
  spawn base-rollback "$baseline_binary" "$database_url"
  base_pid="$spawned_pid"
  listening rollback base-rollback "$base_pid" "$spawned_log"
  base_url="$listened"
  on "$base_url"
  create_priority rollback "Written after the rollback $stamp"
  priority="$created"
  set_advances rollback "${fixture[retracted]}" "[\"$priority\"]"
  run="$(uuid)"
  write_run rollback "${fixture[written]}" "$run"
  wrote priority "$priority"
  wrote advances "${fixture[retracted]}" "$priority"
  wrote run "${fixture[written]}" "$run"
  read_back rollback "$base_url"
  stop "$base_pid"
  spawn head-again "$head_binary" "$database_url"
  head_pid="$spawned_pid" head_log="$spawned_log"
  listening rollback head-again "$head_pid" "$head_log"
  head_url="$listened"
  read_back rollback "$head_url"
  driver_census rollback "$database_url"
  projection_census rollback "$database_url"

  # The release after's drop, and the issue list's advances filter on this head.
  sql "$database_url" "set lock_timeout = '30s';
    drop trigger graph_nodes_workflow_state on graph_nodes; drop trigger graph_nodes_workflow_project on graph_nodes;
    drop function graph_nodes_workflow_state(); drop function graph_nodes_workflow_project();
    drop view priority_nodes; drop view priority_tuples; drop view workflow_nodes; drop view workflow_tuples;" >/dev/null 2>"$work/psql" ||
    fail drop "drop the two triggers and the four views, as the release after's boot step does" "psql: $(<"$work/psql")"
  pass drop "dropped the two triggers and the four views, as the release after's boot step does"
  on "$head_url"
  send GET "/api/v1/issues?project=$(q "$project")&advances=$(q "${fixture[first]}")"
  [[ "$status" == 200 ]] || fail drop "this head's issue list reads the advances filter after the drop" \
    "HTTP $status: $(body); $(handler_failure_line "$head_log" priority_tuples)"
  lists drop "this head's issue list reads the advances filter after the drop, ${fixture[ran]} among its rows" '.[].key' "${fixture[ran]}"
  census_run census-after-drop "$database_url"
  census_wait census-after-drop
  no_rehearsal drop "the census after the drop exits 0 and prints no rehearsal" census-after-drop
  stop "$head_pid"
}

# boot_section: a stopped move, a rollback over it and two runners, over BOOT_DATABASE_URL.
boot_section() {
  local url="$boot_url" hold="$work/boot-move.hold" seqs first_pid first_log second_pid second_log lines before after
  written_after=() touched_issues=() touched_priorities=()
  database_url="$url"
  spawn base-boot "$baseline_binary" "$url"
  listening boot base-boot "$spawned_pid" "$spawned_log"
  write_fixtures boot "$listened"
  read_fixtures boot "$listened" "$work/boot/before"
  issue_reads boot "$listened" "$work/boot/before-issues"
  take_inventory boot "$url"
  stop "$spawned_pid"

  spawn head-stopped "$head_binary" "$url" DISPATCH_TEST_HOOKS=1 DISPATCH_TEST_STORES_MOVE_BATCH=2 DISPATCH_TEST_STORES_MOVE_STOP_AFTER=3
  exited boot "the boot stopped after its third batch" "$spawned_pid" 180
  [[ "$exit_code" != 0 && -n "$(log_line "$spawned_log" "$stopped_words")" ]] ||
    fail boot "the boot exits non-zero right after its third batch" "it exited $exit_code: $(tail -n 5 "$spawned_log")"
  seqs="$(sql "$url" "select count(*) from stores_moved_seqs")" || fail boot "count stores_moved_seqs" "psql failed"
  [[ "$seqs" == 6 ]] || fail boot "stores_moved_seqs holds the 6 rows of three batches of two" "it holds $seqs"
  pass boot "the boot stopped after its third batch, and stores_moved_seqs holds 6 rows"

  spawn base-partial "$baseline_binary" "$url"
  listening boot base-partial "$spawned_pid" "$spawned_log"
  read_fixtures boot "$listened" "$work/boot/partial"
  same_reads boot "$work/boot/before" "$work/boot/partial"
  issue_reads boot "$listened" "$work/boot/partial-issues"
  diff -r "$work/boot/before-issues" "$work/boot/partial-issues" >"$work/boot/issues.diff" ||
    fail boot "the base reads each fixture issue and its tree as before the stopped boot" "$(head -c 600 "$work/boot/issues.diff")"
  pass boot "the base, booted over the partial move, reads every fixture as before it, each issue and its tree among them"
  stop "$spawned_pid"

  open_session dump "$url"
  run_in boot "a session holds access share on priority_nodes, as the nightly dump does" dump \
    "begin isolation level repeatable read; lock table priority_nodes in access share mode;"
  spawn head-first "$head_binary" "$url" DISPATCH_TEST_HOOKS=1 DISPATCH_TEST_STORES_MOVE_BATCH=2 DISPATCH_TEST_STORES_MOVE_HOLD_FILE="$hold"
  first_pid="$spawned_pid" first_log="$spawned_log"
  wait_state boot "the first server holding the advisory lock stores-moved" advisory_reads "$url" "1 0"
  spawn head-second "$head_binary" "$url" DISPATCH_TEST_HOOKS=1 DISPATCH_TEST_STORES_MOVE_BATCH=2
  second_pid="$spawned_pid" second_log="$spawned_log"
  wait_state boot "the second server waiting on stores-moved" advisory_reads "$url" "1 1"
  pass boot "the first server holds stores-moved and the second waits on it"
  touch "$hold"
  wait_state boot "an attempt of the first server's swap that gave up" eval '[[ -n "$(gave_up_lines "$first_log")" ]] || ! kill -0 "$first_pid" 2>/dev/null'
  # Ten seconds more, as the check holds the session: longer than three attempts take.
  sleep 10
  kill -0 "$first_pid" 2>/dev/null ||
    fail boot "the first server still boots while the session holds priority_nodes" "it exited: $(last_attempt_line "$first_log")"
  run_in boot "the session ends" dump "commit;"
  close_session dump
  listening boot head-first "$first_pid" "$first_log"
  lines="$(gave_up_lines "$first_log" | wc -l)"
  [[ "$lines" -gt 1 ]] || fail boot "the first server's boot log holds more than one attempt that gave up" "it holds $lines"
  read_fixtures boot "$listened" "$work/boot/first"
  same_reads boot "$work/boot/before" "$work/boot/first"
  listening boot head-second "$second_pid" "$second_log"
  [[ -n "$(log_line "$second_log" "$moved_nothing_words")" ]] || fail boot "the second server moves nothing" "its log says no such thing"
  read_fixtures boot "$listened" "$work/boot/second"
  same_reads boot "$work/boot/before" "$work/boot/second"
  pass boot "the first server's swap waited out the session in $lines attempts and moved; the second found the four views, moved nothing, and both serve every fixture"
  driver_census boot "$url"
  stop "$first_pid"
  stop "$second_pid"
  before="$(graph_digest "$url")" || fail boot "digest the graph store" "psql failed"
  spawn head-third "$head_binary" "$url"
  listening boot head-third "$spawned_pid" "$spawned_log"
  stop "$spawned_pid"
  after="$(graph_digest "$url")" || fail boot "digest the graph store" "psql failed"
  [[ "$before" == "$after" ]] || fail boot "a third boot over the moved database changes no row of the graph store" "before: $before; after: $after"
  pass boot "a third boot over the moved database changes no row of graph_nodes or graph_tuples: $after"
}

# The ten-times volume of 2026-11-23 (the spec's Design part "How the two stores move"): 130
# priorities with 81 tuples each; 2,410 issues, each with one plan of 7 value tuples and a part_of;
# 21,950 runs, each with 14 value tuples and a part_of, 16,280 of them with a report's value and its
# reports relation and 2,640 of them with a rating and its reason. The move states two more of each
# run and plan, so it writes 445,620 tuples.
readonly volume_priorities=130 volume_issues=2410 volume_runs=21950 volume_reports=16280 volume_ratings=2640

# volume_sql: the writes of that volume by SQL, in the shape each writer of the release before
# stores: a node row with its attributes, and its tuples in the order the writer appends them. Its
# psql variables are the project, a template issue the base server created, the writer, and the
# counts above.
volume_sql() {
  cat <<'SQL'
begin;
insert into issues (key, project_key, number, title, created_by, labels, priority, rank, node_kind, node_address)
select :'project' || '-' || (5000 + n), project_key, 5000 + n, 'Volume issue ' || n, created_by, labels, priority,
       rank || lpad(n::text, 5, '0'), node_kind, :'project' || '-' || (5000 + n)
from issues, generate_series(1, :issues) n where key = :'template';
insert into graph_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event, project_key)
select kind, :'project' || '-' || (5000 + n), supertag, visibility, state, attributes, made_at, made_by, source_event, project_key
from graph_nodes, generate_series(1, :issues) n where kind = 'issue' and id = :'template';
insert into priority_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event)
select 'priority', 'volume-' || p, 'priority', 'project', 'live',
       jsonb_build_object('title', 'Volume ' || p || ' 78', 'rank', lpad(p::text, 4, '0') || '79'),
       timestamptz '2026-11-01' + p * interval '1 minute', :'who'::jsonb, 0
from generate_series(1, :priorities) p;
insert into priority_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, asserted)
select 'priority', 'volume-' || p, s.kind, 'value', s.value,
       timestamptz '2026-11-01' + p * interval '1 minute' + s.place * interval '1 second', :'who'::jsonb, 0, true
from generate_series(1, :priorities) p
cross join lateral (
  select 1 as place, 'visibility' as kind, 'project' as value union all
  select 2, 'state', 'live' union all
  select 2 + r, case when r % 2 = 1 then 'rank' else 'title' end,
         case when r % 2 = 1 then lpad(p::text, 4, '0') || r else 'Volume ' || p || ' ' || r end
  from generate_series(1, 79) r
) s
order by p, s.place;
insert into workflow_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event)
select 'plan', md5('plan ' || i)::uuid::text, 'plan', 'project', 'live',
       jsonb_build_object('issue', :'project' || '-' || (5000 + i), 'project', :'project', 'generation', 1, 'source', 'default',
         'artifact_id', md5('artifact ' || i)::uuid::text, 'version', 1,
         'tasks', '[{"id": "default", "steps": ["planner", "implementer", "reviewer"]}]'::jsonb),
       timestamptz '2026-11-16' + i * interval '1 minute', :'who'::jsonb, 0
from generate_series(1, :issues) i;
insert into workflow_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, asserted)
select 'plan', md5('plan ' || i)::uuid::text, s.kind, s.to_kind, s.to_id, timestamptz '2026-11-16' + i * interval '1 minute', :'who'::jsonb, 0, true
from generate_series(1, :issues) i
cross join lateral (values
  (1, 'issue', 'value', :'project' || '-' || (5000 + i)), (2, 'project', 'value', :'project'), (3, 'generation', 'value', '1'),
  (4, 'source', 'value', 'default'), (5, 'artifact_id', 'value', md5('artifact ' || i)::uuid::text), (6, 'version', 'value', '1'),
  (7, 'tasks', 'value', '[{"id": "default", "steps": ["planner", "implementer", "reviewer"]}]'),
  (8, 'part_of', 'issue', :'project' || '-' || (5000 + i))) s(place, kind, to_kind, to_id)
order by i, s.place;
insert into workflow_nodes (kind, id, supertag, visibility, state, attributes, made_at, made_by, source_event)
select 'step', md5('run ' || r)::uuid::text, 'step', 'project', 'live',
       jsonb_build_object('issue', :'project' || '-' || (5000 + 1 + r % :issues), 'project', :'project', 'generation', 1,
         'task', 'default', 'kind', 'step', 'step', 1 + r % 4, 'role', 'implementer', 'type', 'code', 'agent', 'omp',
         'session', 's-' || r, 'model', 'model-a', 'started_at', '2026-11-17T00:00:00Z', 'ended_at', '2026-11-17T01:00:00Z', 'result', 'passed')
       || case when r <= :reports then jsonb_build_object('report', md5('handoff ' || r)::uuid::text) else '{}' end
       || case when r > :runs - :ratings then jsonb_build_object('rating', 4, 'rating_reason', 'The plan fit the work.') else '{}' end,
       timestamptz '2026-11-17' + r * interval '1 second', :'who'::jsonb, 0
from generate_series(1, :runs) r;
insert into workflow_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, asserted)
select 'step', md5('run ' || r)::uuid::text, s.kind, s.to_kind, s.to_id, timestamptz '2026-11-17' + r * interval '1 second', :'who'::jsonb, 0, true
from generate_series(1, :runs) r
cross join lateral (
  select place, kind, to_kind, to_id from (values
    (1, 'issue', 'value', :'project' || '-' || (5000 + 1 + r % :issues)), (2, 'project', 'value', :'project'), (3, 'generation', 'value', '1'),
    (4, 'task', 'value', 'default'), (5, 'kind', 'value', 'step'), (6, 'step', 'value', (1 + r % 4)::text), (7, 'role', 'value', 'implementer'),
    (8, 'type', 'value', 'code'), (9, 'agent', 'value', 'omp'), (10, 'session', 'value', 's-' || r), (11, 'model', 'value', 'model-a'),
    (12, 'started_at', 'value', '2026-11-17T00:00:00Z'), (13, 'ended_at', 'value', '2026-11-17T01:00:00Z'), (14, 'result', 'value', 'passed'),
    (15, 'part_of', 'issue', :'project' || '-' || (5000 + 1 + r % :issues))) v(place, kind, to_kind, to_id)
  union all
  select place, kind, to_kind, to_id from (values (16, 'report', 'value', md5('handoff ' || r)::uuid::text),
    (17, 'reports', 'handoff', md5('handoff ' || r)::uuid::text)) v(place, kind, to_kind, to_id) where r <= :reports
  union all
  select place, kind, to_kind, to_id from (values (18, 'rating', 'value', '4'), (19, 'rating_reason', 'value', 'The plan fit the work.'))
    v(place, kind, to_kind, to_id) where r > :runs - :ratings
) s
order by r, s.place;
commit;
analyze priority_nodes, priority_tuples, workflow_nodes, workflow_tuples, issues;
SQL
}

# The terms of a day's tuples of other kinds, each `kind:tuples_a_day`, from the `census: sizing
# tuples-a-write` lines cybermonklabs/legion#3178's body records, those above zero: the split
# c-moved-read-cost gives its 1,000,000 tuples, here of 3,000,000.
readonly other_terms='comment:1.50 authored_by:0.75 author_alias:0.04 delivery:0.14 bus_delivery:0.25 block:2.00
block_ref:0.07 block_text:1.00 message_delivery:0.14 read_mark:0.39 snooze:0.04 person:0.14 session:2.50 role:0.21
page:0.32 message:1.00 message_pointer:0.32 reply:0.36 mention:0.32 participation:0.39 person_delivery:0.36
body_block:4.39 body_block_ref:0.14 delivery_attempt:4.50 issue:0.86 issue_relation:0.04 artifact:1.39
document_version:0.93 criterion:5.00 ask:2.29 answer:0.18 ask_resolved:0.07 ask_follower:0.04 decision:50.00
reading:10.00 interaction:40.00 frame:500.00 lens:12.00 lens_finding:40.00 lens_kind:3.14 constraint:5.00 risk:44.00'

# other_tuples URL COUNT ROUND: COUNT tuples of other kinds into graph_tuples, split by other_terms,
# each a value tuple of a node of its term's kind, named for ROUND so no two rounds share a node.
other_tuples() {
  local terms
  terms="$(tr ' ' '\n' <<<"$other_terms" | awk -F: -v q="'" 'NF == 2 { printf "%s(%s%s%s, %s)", sep, q, $1, q, $2; sep = ", " }')"
  sql "$1" "insert into graph_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, legacy_source_seq, asserted)
    select t.kind, 'r$3-' || (n / 7), t.kind, 'value', md5(n::text || t.kind), timestamptz '2026-11-01' + n * interval '1 second',
           '{\"kind\": \"session\", \"id\": \"stores-moved\"}'::jsonb, 0, null, true
    from (select kind, round($2 * weight / sum(weight) over ())::bigint as share from (values $terms) w(kind, weight)) t
    cross join lateral generate_series(1, t.share) n" >/dev/null 2>"$work/psql" ||
    fail volume "write $2 tuples of other kinds" "psql: $(<"$work/psql")"
}

# volume_section: the time of the move at ten times production's volume, over VOLUME_DATABASE_URL.
volume_section() {
  local url="$volume_url" size round=1 template copy statement seconds counts store what want base_pid
  database_url="$url"
  spawn base-volume "$baseline_binary" "$url"
  base_pid="$spawned_pid"
  listening volume base-volume "$base_pid" "$spawned_log"
  on "$listened"
  sign_in volume "$login"
  send POST /api/v1/projects "$(jq -cn --arg k "$project" --arg d "$login" --argjson a "$(fixture_actor)" \
    '{key: $k, name: "The stores volume", default_assignee: $d, actor: $a}')"
  want_status 201 volume "create the project $project"
  create_issue volume "The template of the volume's issues"
  template="$created"
  stop "$base_pid"
  volume_sql | env -u PGHOSTADDR -u PGSERVICE -u PGSERVICEFILE psql "$url" -X -q -v ON_ERROR_STOP=1 -v project="$project" \
    -v template="$template" -v who="$(daemon_actor)" -v issues="$volume_issues" -v priorities="$volume_priorities" \
    -v runs="$volume_runs" -v reports="$volume_reports" -v ratings="$volume_ratings" >"$work/volume.out" 2>&1 ||
    fail volume "write ten times the volume of 2026-11-23" "psql: $(tail -n 5 "$work/volume.out")"
  pass volume "wrote ten times the volume of 2026-11-23 in the release before's shape: $(sql "$url" "select (select count(*) from priority_nodes)
    || ' priorities with ' || (select count(*) from priority_tuples) || ' tuples, ' || (select count(*) from workflow_nodes where kind = 'step')
    || ' runs and ' || (select count(*) from workflow_nodes where kind = 'plan') || ' plans with ' || (select count(*) from workflow_tuples) || ' tuples'")"
  other_tuples "$url" 3000000 "$round"
  size="$(sql "$url" "select pg_total_relation_size('graph_tuples')")"
  while [[ "$size" -le 1073741824 ]]; do
    round=$((round + 1))
    other_tuples "$url" 500000 "$round"
    size="$(sql "$url" "select pg_total_relation_size('graph_tuples')")"
  done
  sql "$url" "analyze graph_tuples, graph_nodes" >/dev/null
  pass volume "graph_tuples holds $(sql "$url" "select count(*) from graph_tuples") tuples, $(sql "$url" "select pg_size_pretty(pg_total_relation_size('graph_tuples'))"), above one GiB"

  census_run census-volume "$url" DISPATCH_TEST_HOOKS=1 DISPATCH_TEST_STORES_MOVE_EXPLAIN=1
  census_wait census-volume 600
  [[ "$census_code" == 0 ]] || fail volume "the census over the volume exits 0" "it exited $census_code: $(rehearsal_refusal "$work/census-volume.out")"
  for counts in "priority:node rows:$volume_priorities" "priority:tuples:$((volume_priorities * 81))" \
    "workflow:node rows:$((volume_runs + volume_issues))" \
    "workflow:tuples:$((volume_runs * 15 + volume_reports * 2 + volume_ratings * 2 + volume_issues * 8))" \
    "workflow:stated values:$(((volume_runs + volume_issues) * 2))"; do
    IFS=: read -r store what want <<<"$counts"
    [[ "$(rehearsal_count "$work/census-volume.out" "$store" "$what")" == "$want $want 0" ]] ||
      fail volume "the rehearsal's $store $what equal the seeded $want" "it reads $(rehearsal_count "$work/census-volume.out" "$store" "$what")"
  done
  copy="$(copy_ended "$work/census-volume.out")"
  printf 'the rehearsal copied %s tuples in %s\n' "${copy% *}" "${copy#* }"
  statement="$(seq_scan_statement "$work/census-volume.out")"
  [[ -z "$statement" ]] || fail volume "no plan of the census before the drop reads graph_tuples by Seq Scan" "the census of the $statement does"
  pass volume "the census exits 0, its rehearsal's counts equal the seeded volume, its copy took ${copy#* }, and no plan reads graph_tuples by Seq Scan"

  spawn head-volume "$head_binary" "$url"
  head_pid="$spawned_pid" head_log="$spawned_log"
  listening volume head-volume "$head_pid" "$head_log" 600
  seconds="$(boot_seconds "$head_log")" || fail volume "read the boot's time" "its log holds no line that ends the move"
  printf "this head's boot took %ss from its first line, before its migrations, to the end of the move\n" "$seconds"
  awk -v s="$seconds" 'BEGIN { exit !(s <= 300) }' || fail volume "the boot's migrations and move take at most 5 minutes" "they took ${seconds}s"
  pass volume "the boot's migrations and move took ${seconds}s, within 5 minutes"
  stop "$head_pid"
}

main() {
  require_env BASELINE_SHA WALK_RULE_ISSUE DATABASE_URL BOOT_DATABASE_URL VOLUME_DATABASE_URL DISPATCH_TOKEN PROJECT DEV_SIGNIN_LOGIN TMPDIR
  require_commands awk curl date diff git go jq mkfifo psql python3 sed sort tar
  local url
  for url in "$DATABASE_URL" "$BOOT_DATABASE_URL" "$VOLUME_DATABASE_URL"; do
    loopback_database_url "$url" || {
      printf 'a database URL names no database on this machine: every server here migrates it and the driver writes rows of it\n' >&2
      usage
      exit 2
    }
  done
  [[ -n "${WALK_RULE_COMMIT:-}" ]] || require_commands gh
  # shellcheck disable=SC2031 # the run's own DATABASE_URL: only a server's subshell sets another
  database_url="$DATABASE_URL"
  boot_url="$BOOT_DATABASE_URL" volume_url="$VOLUME_DATABASE_URL"
  # The servers get no pair the caller exported for another driver.
  unset BASELINE_EXTRA_ENV
  checkout="$(git -C "$(dirname -- "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)" || {
    printf 'cannot find the checkout %s is part of\n' "${BASH_SOURCE[0]}" >&2
    exit 2
  }
  head_binary="${HEAD_BINARY:-$(command -v envoy-dispatch || true)}"
  [[ -n "$head_binary" && -f "$head_binary" && -x "$head_binary" ]] || {
    printf 'no envoy-dispatch on PATH, and HEAD_BINARY names none: build this head as the Verification preamble does\n' >&2
    exit 2
  }
  [[ "$head_binary" == /* ]] || head_binary="$PWD/$head_binary"
  project="$PROJECT" login="$DEV_SIGNIN_LOGIN"
  stamp="$(date +%s)"
  session="stores-moved-$stamp-$$"
  servers=() written_after=() touched_issues=() touched_priorities=() revisions_before=()
  work_dir
  printf 'Authorization: Bearer %s\n' "$DISPATCH_TOKEN" >"$work/auth"
  trap cleanup EXIT
  trap 'exit 130' INT
  trap 'exit 143' TERM
  check_base
  roll
  boot_section
  volume_section
  pass "$driver" "the boot moved every row of the two stores into the graph store through a roll, a stopped move, a rollback and two runners, and within 5 minutes at ten times production's volume"
}

# cleanup: every session, every server this run started and lib-baseline.sh's own, and the work
# directory. The databases are the run's, which it drops.
cleanup() {
  local code=$? name pid
  for name in "${!session_fd[@]}"; do
    close_session "$name"
  done
  for pid in "${servers[@]}"; do
    stop "$pid"
  done
  baseline_stop
  rm -rf "$work"
  exit "$code"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
