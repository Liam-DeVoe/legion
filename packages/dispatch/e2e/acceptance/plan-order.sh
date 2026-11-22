#!/usr/bin/env bash
# Drives the plan read of check c-plan-read (spec requirement r-plan-node of dispatch://INFRA-1098)
# against the `bun run dispatch:local` server, over the seeded corpus (lib-corpus.sh, which seeds
# it once where the database lacks it).
#
#   P1  One read, GET /api/v1/issues/{key}/tree?view=plan of the seeded issue, answers its plan
#       tasks with their dependencies, their sizes and the order those imply: each node with a
#       position comes after every node it waits on, and the plan task of the seeded issue's child
#       that waits on t-serves comes after it.
#   P2  A dependency is the needs tuple: the driver reads the child's plan task's needs tuple from
#       graph_tuples and finds the wait the read answered.
#   P3  The size is an estimate of duration in days and a value of the node: t-serves's size is
#       its value tuple, 2.5, which the read answers, and t-none, which the seed gives no size,
#       answers none, with no size value in the store.
#   P4  The reach of a needs tuple is the reach of every block pointer (CheckBlockPointers): on two
#       issues of the run's own, a parent and its child, the child's plan task's needs at a plan
#       task of the issue above resolves to that task's node, which the ordered read orders it
#       after; and a needs at a plan task of an issue outside the child's tree, the seeded
#       issue's, is refused and stores nothing.
#   P5  A cycle of two plan tasks, written in one version of a plan of an issue of the run's own,
#       is listed by the read with its two nodes, and no order is returned over it; a failure
#       prints the needs tuples the store holds beside the positions the read answered.
#
# Inputs: DATABASE_URL (the database the server at DISPATCH_URL serves, on this machine: the
# corpus's loader writes it and psql reads it), DISPATCH_URL (the server), DISPATCH_TOKEN or
# DISPATCH_TOKEN_FILE (an agent bearer, read here and never printed) and PROJECT (the project the
# corpus is seeded into, CORE). Each unmet input is exit 2. Every write is the agent bearer's,
# acting for a session of this run's own; it changes nothing of the seeded corpus, and it closes
# the issues it writes, also when a check fails.
#
# Each passing check prints `PASS: <section> <what>`; the first failing one prints
# `FAIL: <section> <what>: <observed>` and the script exits 1. The last line is
# `PASS: plan-order.sh complete`.
#
# Every check is `cmd || fail …`, never part of an `&&` list: under `set -e` a failing command
# anywhere but last in such a list does not stop the script.

# shellcheck disable=SC2016 # the single-quoted jq filters and SQL name jq variables, not shell ones
set -euo pipefail
set +x

usage() {
  cat >&2 <<'USAGE'
Usage: DATABASE_URL=postgres://…@127.0.0.1:5432/<db>?sslmode=disable DISPATCH_URL=http://127.0.0.1:8777 \
         DISPATCH_TOKEN=e2e-token PROJECT=CORE plan-order.sh
       (DISPATCH_TOKEN_FILE=<file holding the bearer> in place of DISPATCH_TOKEN; DATABASE_URL names
       the database the server at DISPATCH_URL serves, on this machine)
USAGE
}

# shellcheck source-path=SCRIPTDIR source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
# shellcheck source-path=SCRIPTDIR source=lib-corpus.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-corpus.sh"
require_env DATABASE_URL DISPATCH_URL PROJECT
read_bearer
require_commands curl jq psql

readonly driver=plan-order.sh
# shellcheck disable=SC2031 # the DISPATCH_URL lib.sh's `cli` exports is its own; this one is the caller's
readonly base="${DISPATCH_URL%/}"
# shellcheck disable=SC2153 # PROJECT and DATABASE_URL are the caller's inputs, checked by name above
readonly project="$PROJECT" database_url="$DATABASE_URL"
loopback_database_url "$database_url" || {
  printf 'DATABASE_URL names no database on this machine: the corpus loader writes it\n' >&2
  usage
  exit 2
}
corpus_tools
stamp="$(date +%s)-$$"
readonly stamp
readonly session="plan-order-$stamp"

work_dir
printf 'Authorization: Bearer %s\n' "$token" >"$work/auth"

keys=()
cleanup() {
  local code=$? issue
  for issue in "${keys[@]}"; do
    close_issue "$issue"
  done
  rm -rf "$work"
  exit "$code"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# create SECTION WHAT TITLE SPEC [PARENT]: the agent bearer creates an issue in $project, under
# PARENT where given, closed when the run ends, and sets $created to its key.
create() {
  send POST /api/v1/issues "$(jq -cn --arg p "$project" --arg t "$3" --arg s "$4" --arg parent "${5-}" --argjson a "$(actor)" \
    '{project: $p, title: $t, spec: $s, force: true, actor: $a} + (if $parent == "" then {} else {parent: $parent} end)')"
  want_status 201 "$1" "$2"
  created="$(jq -er '.key' "$work/body")" || fail "$1" "$2" "no key: $(body)"
  keys+=("$created")
}

# plan SECTION WHAT KEY MARKDOWN: writes MARKDOWN as the next version of KEY's plan, the answer left
# as it came.
plan() {
  send POST "/api/v1/issues/$3/artifacts" "$(jq -cn --arg c "$4" --argjson a "$(actor)" '{name: "plan.md", content: $c, actor: $a}')"
}

# plan_view SECTION WHAT KEY: the ordered read of KEY's tree, kept in $work/plan.json and
# $work/body.
plan_view() {
  send GET "/api/v1/issues/$3/tree?view=plan"
  want_status 200 "$1" "$2"
  cp "$work/body" "$work/plan.json"
}

# The ordered read's nodes as entries: each plan node, and each issue as its two halves, by a key
# that names the node and the half, with its position, its cycle flag and the keys it waits on.
readonly entries_filter='def needkey: if .kind == "issue" then "issue:\(.id):\(.half)" else "\(.kind):\(.id)" end;
  [.nodes[] | select(.plan != null) | if .kind == "issue" then
      ({key: "issue:\(.id):start", position: .plan.start.position, cycle: .plan.start.cycle, needs: [.plan.start.needs[] | needkey]},
       {key: "issue:\(.id):done", position: .plan.done.position, cycle: .plan.done.cycle, needs: [.plan.done.needs[] | needkey]})
    else {key: "\(.kind):\(.id)", position: .plan.position, cycle: .plan.cycle, needs: [.plan.needs[] | needkey]} end]'
# Each wait between two nodes that both hold a position, and whether the waiting node comes after.
readonly order_filter="$entries_filter"' as $e | ($e | map({key, value: .position}) | from_entries) as $pos
  | [$e[] | select(.position != null) as $n | $n.needs[] | select($pos[.] != null) | {from: $n.key, to: ., after: ($pos[.] < $n.position)}]'

# in_order SECTION WHAT: every wait of the last ordered read between two placed nodes comes in
# order, at least one examined.
in_order() {
  local pairs late
  jq -c "$order_filter" "$work/plan.json" >"$work/order.json" || fail "$1" "$2" "$(head -c 600 "$work/plan.json")"
  pairs="$(jq 'length' "$work/order.json")"
  [[ "$pairs" -gt 0 ]] || fail "$1" "$2" "the read answers no wait between two placed nodes"
  late="$(jq -c '[.[] | select(.after | not)]' "$work/order.json")"
  [[ "$late" == "[]" ]] || fail "$1" "$2" "these waits come out of order: $late"
  pass "$1" "$2: $pairs waits examined"
}

# --- setup: the corpus ---------------------------------------------------------------------------

corpus_ensure setup
key="$corpus_issue"
readonly key
send GET "/api/v1/issues/$key"
want_status 200 setup "read the seeded issue $key"
child="$(jq -er '.children[0].key // .children[0] // empty' "$work/body")" ||
  fail setup "the seeded issue holds the child whose plan waits on its plan" "$(body)"
readonly child
readonly t_serves="$key/tree#t-serves" t_none="$key/tree#t-none"

# --- P1: one read: the plan tasks, their waits, their sizes and their order -----------------------

plan_view P1 "read the ordered read of $key" "$key"
expect P1 "the read answers $key's two plan tasks, each with what it waits on and its size: t-serves 2.5 days, t-none none" \
  "$(jq -cnS --arg k "$key" '[{id: "\($k)/tree#t-none", needs: [{half: "start", id: $k, kind: "issue"}], size: null},
      {id: "\($k)/tree#t-serves", needs: [{half: "start", id: $k, kind: "issue"}], size: "2.5"}]')" \
  --arg k "$key" '[.nodes[] | select(.supertag == "plan_task" and (.issue // $k) == $k) | {id, needs: .plan.needs, size: .plan.size}] | sort_by(.id)'
t_child="$(jq -r --arg c "$child" '[.nodes[] | select(.supertag == "plan_task" and .issue == $c) | .id] | first // empty' "$work/plan.json")"
[[ -n "$t_child" ]] || fail P1 "the read answers the plan task of $child" "$(head -c 600 "$work/plan.json")"
readonly t_child
expect P1 "the same read answers $t_child of $child waiting on t-serves of $key" \
  "$(jq -cn --arg t "$t_serves" '[{id: $t, kind: "block"}]')" \
  --arg c "$t_child" '[.nodes[] | select(.id == $c) | .plan.needs[] | select(.kind == "block")]'
in_order P1 "each node with a position comes after every node it waits on"
expect P1 "$t_child comes after t-serves, and every plan task of the tree holds a position" '{"after":true,"unplaced":[]}' \
  --arg c "$t_child" --arg t "$t_serves" '{after: ((.nodes[] | select(.id == $c) | .plan.position) > (.nodes[] | select(.id == $t) | .plan.position)),
    unplaced: [.nodes[] | select(.supertag == "plan_task" and .plan.position == null) | .id]}'

# --- P2: the dependency is the needs tuple --------------------------------------------------------

with_vars P2 "read the needs tuples out of $t_child" "select coalesce(json_agg(json_build_object('kind', to_kind, 'id', to_id) order by to_id), '[]')
  from (select distinct on (to_kind, to_id) to_kind, to_id, asserted from graph_tuples where from_kind = 'block' and from_id = :'node'
    and kind = 'needs' order by to_kind, to_id, seq desc) latest where asserted" "node=$t_child"
expect P2 "the store holds one needs tuple out of $t_child, to t-serves, the wait the read answers" \
  "$(jq -cS '.' <<<"$answer")" --arg c "$t_child" '[.nodes[] | select(.id == $c) | .plan.needs[] | select(.kind == "block")]'

# --- P3: the size is a value of the node, in days -------------------------------------------------

with_vars P3 "read the size values of t-serves and t-none" "select json_build_object(
    'serves', (select coalesce(json_agg(distinct to_id), '[]') from graph_tuples where from_kind = 'block' and from_id = :'serves' and kind = 'size' and to_kind = 'value' and asserted),
    'none', (select count(*) from graph_tuples where from_kind = 'block' and from_id = :'none' and kind = 'size'))" \
  "serves=$t_serves" "none=$t_none"
jq -e '.serves == ["2.5"] and .none == 0' <<<"$answer" >/dev/null ||
  fail P3 "t-serves holds its size as a value, 2.5, and t-none holds no size value" "the store holds $answer"
pass P3 "t-serves holds its size as a value, 2.5, and t-none holds no size value: $answer"
send GET "/api/v1/issues/$key/artifacts/plan-md/blocks"
want_status 200 P3 "read the blocks of $key's plan"
expect P3 "the size is an estimate of duration in days: t-serves's size attribute is of kind days" \
  '[{"kind":"days","value":"2.5"}]' '[.[] | select(.id == "t-serves") | (.attributes // [])[] | select(.name == "size") | {kind, value}]'

# --- P4: the reach of a needs tuple -----------------------------------------------------------------

create P4 "create the parent issue of the run's own" "plan-order.sh: the issue above" "An issue of plan-order.sh's own, with one plan task."
above="$created"
plan P4 "write $above's plan" "$above" $':::plan_task{#t-up size="1"}\nThe step the issue above takes first.\n:::\n'
want_status 201 P4 "write $above's plan, with one plan task"
create P4 "create the child issue of the run's own" "plan-order.sh: the issue below" "A child of $above, whose plan waits on its parent's." "$above"
below="$created"
readonly above below
plan P4 "write $below's plan" "$below" ":::plan_task{#t-down needs=\"dispatch://$above/artifact/plan-md#b-t-up\"}
The child's step, once its parent's step is taken.
:::
"
want_status 201 P4 "write $below's plan, whose task waits on $above's t-up, a block of the issue above"
want_value P4 "the needs tuple of $below's t-down resolves to the node of $above's t-up" "block:$above/tree#t-up" \
  "select coalesce(string_agg(to_kind || ':' || to_id, ','), '') from graph_tuples where from_kind = 'block' and from_id = '$below/tree#t-down' and kind = 'needs' and asserted"
plan_view P4 "read the ordered read of $above" "$above"
expect P4 "the ordered read of $above places $below's t-down after $above's t-up" true \
  --arg d "$below/tree#t-down" --arg u "$above/tree#t-up" '(.nodes[] | select(.id == $d) | .plan.position) > (.nodes[] | select(.id == $u) | .plan.position)'

query P4 "count the versions of $above's plan before the refused write" \
  "select count(*) from artifact_versions v join artifacts a on a.id = v.artifact_id where a.issue_key = '$above' and a.slug = 'plan-md'"
readonly versions_before="$answer"
plan P4 "write $above's plan with a needs outside its reach" "$above" ":::plan_task{#t-up size=\"1\"}
The step the issue above takes first.
:::

:::plan_task{#t-out needs=\"dispatch://$key/artifact/plan-md#b-t-serves\"}
A step that waits on a plan task of an issue outside this issue's tree.
:::
"
want_status 400 P4 "a needs at $key's t-serves, a block of an issue outside $above's tree, is refused"
expect P4 "the refusal names the pointer and the reach it is held to" '{"code":"INVALID_MARKDOWN","names":true}' \
  --arg t "dispatch://$key/artifact/plan-md#b-t-serves" '{code, names: (.error | contains($t) and contains("issue above it"))}'
want_value P4 "the refused write stored no version of $above's plan" "$versions_before" \
  "select count(*) from artifact_versions v join artifacts a on a.id = v.artifact_id where a.issue_key = '$above' and a.slug = 'plan-md'"
want_value P4 "and no needs tuple at t-serves from any node of $above" 0 \
  "select count(*) from graph_tuples where from_kind = 'block' and from_id like '$above/%' and kind = 'needs' and to_id = '$t_serves'"

# --- P5: a cycle of two plan tasks -----------------------------------------------------------------

create P5 "create an issue whose plan holds a cycle" "plan-order.sh: a plan whose two tasks wait on each other" \
  "An issue of plan-order.sh's own, whose plan holds two tasks that wait on each other."
cycled="$created"
readonly cycled
plan P5 "write $cycled's plan" "$cycled" ":::plan_task{#t-first needs=\"dispatch://$cycled/artifact/plan-md#b-t-second\"}
The first task, which waits on the second.
:::

:::plan_task{#t-second needs=\"dispatch://$cycled/artifact/plan-md#b-t-first\"}
The second task, which waits on the first.
:::
"
want_status 201 P5 "write $cycled's plan, whose two tasks wait on each other"
query P5 "read the needs tuples out of $cycled's plan tasks" \
  "select coalesce(string_agg(from_id || ' -> ' || to_kind || ':' || to_id, '; ' order by from_id), '') from graph_tuples
     where from_kind = 'block' and from_id like '$cycled/tree#%' and kind = 'needs' and asserted"
readonly cycle_tuples="$answer"
plan_view P5 "read the ordered read of $cycled" "$cycled"
jq -c "$entries_filter"' | {cycle: ([.[] | select(.cycle) | .key] | sort), positioned_in_cycle: [.[] | select(.cycle and .position != null) | .key]}' \
  "$work/plan.json" >"$work/cycle.json" || fail P5 "read the cycle" "$(head -c 600 "$work/plan.json")"
[[ "$(<"$work/cycle.json")" == "$(jq -cn --arg k "$cycled" '{cycle: ["block:\($k)/tree#t-first", "block:\($k)/tree#t-second"], positioned_in_cycle: []}')" ]] ||
  fail P5 "the read lists the cycle with its two nodes and returns no order over it" \
    "it answers $(<"$work/cycle.json"), over the needs tuples the store holds: ${cycle_tuples:-none}; positions: $(jq -c "$entries_filter"' | map({key, position, cycle})' "$work/plan.json")"
pass P5 "the read lists the cycle of t-first and t-second with no order over it; the store holds the needs tuples $cycle_tuples"

pass "$driver" complete
