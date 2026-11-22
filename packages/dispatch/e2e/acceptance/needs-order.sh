#!/usr/bin/env bash
# Drives the one dependency relation of check c-needs (spec requirements r-needs and r-issue-edges
# of dispatch://INFRA-1098) against the `bun run dispatch:local` server, over the seeded corpus
# (lib-corpus.sh, which seeds it once where the database lacks it).
#
#   N1  One tuple kind holds every dependency: the seeded issue waiting on an issue and the plan
#       task of the seeded issue's child waiting on a plan task of the seeded issue are each read
#       from graph_tuples, and they are one kind, needs.
#   N2  One ordered read answers both sets together: GET /api/v1/issues/{key}/tree?view=plan of the
#       seeded issue answers the issue's wait and the plan task's wait, every node with a position
#       after every node it waits on that has one, each pair counted.
#   N3  The needs tuples against their projections: issue_links equals the needs tuples between
#       issue nodes, and each plan task's needs attribute equals the needs tuples out of its node,
#       each census counting what it examined and reading zero differences.
#   N4  The issue read answers no blocked_by field: a search of the seeded issue's answer for a
#       member of that name finds none, where the same search finds needs, which holds the keys the
#       needs tuples hold, in their order.
#   N5  The write refuses an issue that would wait on itself through its own chain: the issue the
#       seeded issue waits on, asked to wait on the seeded issue, is refused DEPENDENCY_CYCLE
#       (assertNoWaitPath in api/issue_parent.go), and nothing is stored.
#   N6  An issue waits on an issue of another project: the agent bearer creates the second
#       project CORE2 (POST /api/v1/projects) - or finds it, made by an earlier run, since no route
#       removes a project - an issue in it and an issue in PROJECT, writes the PROJECT issue's needs
#       at the CORE2 issue through that bearer, which names no project, and reads the wait taken,
#       with its issue_links row and its needs tuple.
#   N7  A cycle of two plan tasks, written in one version of a plan of an issue of the run's own,
#       is listed by the ordered read with its two nodes, and no position is returned over it; a
#       failure prints the needs tuples the store holds beside the positions the read answered.
#
# Inputs: DATABASE_URL (the database the server at DISPATCH_URL serves, on this machine: the
# corpus's loader writes it and psql reads it), DISPATCH_URL (the server), DISPATCH_TOKEN or
# DISPATCH_TOKEN_FILE (an agent bearer, read here and never printed) and PROJECT (the project the
# corpus is seeded into, CORE). Each unmet input is exit 2. Every write is the agent bearer's,
# acting for a session of this run's own; it changes nothing of the seeded corpus, and it closes
# the issues it writes, also when a check fails. It reads no blocked_by member: the one search for
# that name asserts its absence.
#
# Each passing check prints `PASS: <section> <what>`; the first failing one prints
# `FAIL: <section> <what>: <observed>` and the script exits 1. The last line is
# `PASS: needs-order.sh complete`.
#
# Every check is `cmd || fail …`, never part of an `&&` list: under `set -e` a failing command
# anywhere but last in such a list does not stop the script.

# shellcheck disable=SC2016 # the single-quoted jq filters and SQL name jq variables, not shell ones
set -euo pipefail
set +x

usage() {
  cat >&2 <<'USAGE'
Usage: DATABASE_URL=postgres://…@127.0.0.1:5432/<db>?sslmode=disable DISPATCH_URL=http://127.0.0.1:8777 \
         DISPATCH_TOKEN=e2e-token PROJECT=CORE needs-order.sh
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
require_commands curl jq paste psql sort

readonly driver=needs-order.sh
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
readonly session="needs-order-$stamp"
# The second project the check names.
readonly second_project=CORE2

work_dir
printf 'Authorization: Bearer %s\n' "$token" >"$work/auth"

keys=()
waited_changed=""
cleanup() {
  local code=$? issue
  # A cycle write the server took is undone, so the corpus keeps the one wait the loader wrote.
  if [[ -n "$waited_changed" ]]; then
    send PATCH "/api/v1/issues/$waited_changed" "$(jq -cn --argjson a "$(actor)" '{needs: [], actor: $a}')"
    [[ "$status" == 200 ]] ||
      printf '%s: could not clear the needs of %s: HTTP %s: %s\n' "$driver" "$waited_changed" "$status" "$(body)" >&2
  fi
  for issue in "${keys[@]}"; do
    close_issue "$issue"
  done
  rm -rf "$work"
  exit "$code"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# create SECTION WHAT PROJECT TITLE SPEC: the agent bearer creates an issue, closed when the run
# ends, and sets $created to its key.
create() {
  send POST /api/v1/issues "$(jq -cn --arg p "$3" --arg t "$4" --arg s "$5" --argjson a "$(actor)" \
    '{project: $p, title: $t, spec: $s, force: true, actor: $a}')"
  want_status 201 "$1" "$2"
  created="$(jq -er '.key' "$work/body")" || fail "$1" "$2" "no key: $(body)"
  keys+=("$created")
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

# ref_node SECTION ADDRESS: the store id of the node ADDRESS names, in $node.
ref_node() {
  send GET "/api/v1/references?to=$(q "$2")"
  want_status 200 "$1" "read $2"
  node="$(jq -er '.node.id' "$work/body")" || fail "$1" "read $2" "no node id: $(body)"
}

# --- setup: the corpus ---------------------------------------------------------------------------

corpus_ensure setup
key="$corpus_issue"
readonly key
send GET "/api/v1/issues/$key"
want_status 200 setup "read the seeded issue $key"
cp "$work/body" "$work/issue.json"
waited="$(jq -er '.needs[0] // empty' "$work/issue.json")" || fail setup "the seeded issue waits on an issue" "$(body)"
child="$(jq -er '.children[0].key // .children[0] // empty' "$work/issue.json")" ||
  fail setup "the seeded issue holds the child whose plan waits on its plan" "$(body)"
assignee="$(jq -r '.assignee // empty' "$work/issue.json")"
readonly waited child assignee
pass setup "the seeded issue $key waits on $waited and holds the child $child"

# --- N1: one tuple kind ---------------------------------------------------------------------------

query N1 "read the tuples of the two seeded waits" \
  "select json_build_object('issue', (select json_agg(distinct kind) from graph_tuples
       where from_kind = 'issue' and from_id = '$key' and to_kind = 'issue' and to_id = '$waited' and asserted),
     'plan', (select json_agg(distinct t.kind) from graph_tuples t
       where t.from_kind = 'block' and t.from_id like '$child/tree#%' and t.to_kind = 'block' and t.to_id like '$key/tree#%'
         and t.asserted and t.kind <> 'serves'),
     'rows', (select count(*) from graph_tuples where kind = 'needs' and ((from_kind = 'issue' and from_id = '$key')
       or (from_kind = 'block' and from_id like '$child/tree#%'))))"
jq -e '.issue == ["needs"] and .plan == ["needs"] and .rows >= 2' <<<"$answer" >/dev/null ||
  fail N1 "the issue's wait and the plan task's wait are one tuple kind, needs" "the store holds $answer"
pass N1 "the issue's wait and the plan task's wait are one tuple kind, needs, over $(jq '.rows' <<<"$answer") rows: $answer"

# --- N2: one ordered read, both sets together ------------------------------------------------------

plan_view N2 "read the ordered read of $key" "$key"
expect N2 "the read answers the issue's wait: $key starts once $waited is done" \
  "$(jq -cn --arg w "$waited" '[{half: "done", id: $w, kind: "issue"}]')" \
  --arg k "$key" '[.nodes[] | select(.kind == "issue" and .id == $k) | .plan.start.needs[] | select(.kind == "issue" and .id != $k)]'
t_serves="$(jq -r --arg k "$key" '[.nodes[] | select(.kind == "block" and .id == "\($k)/tree#t-serves") | .id] | first // empty' "$work/plan.json")"
t_child="$(jq -r --arg c "$child" '[.nodes[] | select(.kind == "block" and (.id | startswith("\($c)/tree#"))) | .id] | first // empty' "$work/plan.json")"
[[ -n "$t_serves" && -n "$t_child" ]] || fail N2 "the read answers the seeded plan tasks of $key and of $child" "$(head -c 600 "$work/plan.json")"
readonly t_serves t_child
expect N2 "the same read answers the plan task's wait: $t_child of $child waits on $t_serves of $key" \
  "$(jq -cn --arg t "$t_serves" '[{id: $t, kind: "block"}]')" \
  --arg c "$t_child" '[.nodes[] | select(.id == $c) | .plan.needs[] | select(.kind == "block")]'
jq -c "$order_filter" "$work/plan.json" >"$work/order.json" || fail N2 "read the order" "$(head -c 600 "$work/plan.json")"
pairs="$(jq 'length' "$work/order.json")"
[[ "$pairs" -gt 0 ]] || fail N2 "each node comes after every node it waits on" "the read answers no wait between two placed nodes"
late="$(jq -c '[.[] | select(.after | not)]' "$work/order.json")"
[[ "$late" == "[]" ]] || fail N2 "each node comes after every node it waits on" "these waits come out of order: $late"
pass N2 "each node comes after every node it waits on: $pairs waits examined, among them $t_child after $t_serves"
expect N2 "the issue $waited, outside $key's tree, is a node with no position the read orders nothing over" '[]' \
  --arg w "$waited" '[.nodes[] | select(.kind == "issue" and .id == $w and .plan != null and (.plan.start.position != null or .plan.done.position != null))]'

# --- N3: the projections against the tuples --------------------------------------------------------

query N3 "census issue_links against the needs tuples between issue nodes" \
  "with tuples as (select from_id, to_id from (select distinct on (from_id, to_id) from_id, to_id, asserted from graph_tuples
       where from_kind = 'issue' and to_kind = 'issue' and kind = 'needs' order by from_id, to_id, seq desc) latest where asserted),
     links as (select issue_key as from_id, target_key as to_id from issue_links)
   select json_build_object('links', (select count(*) from links), 'tuples', (select count(*) from tuples),
     'differences', (select count(*) from ((select * from links except select * from tuples) union all (select * from tuples except select * from links)) d))"
jq -e '.links > 0 and .tuples > 0 and .differences == 0' <<<"$answer" >/dev/null ||
  fail N3 "issue_links equals the needs tuples between issue nodes" "the census read $answer"
pass N3 "issue_links equals the needs tuples between issue nodes: $answer"

examined=0
for issue in "$key" "$child"; do
  send GET "/api/v1/issues/$issue/artifacts/plan-md/blocks"
  want_status 200 N3 "read the blocks of $issue's plan"
  jq -c '[.[] | select(.type == "plan_task") | {id, needs: ([(.attributes // [])[] | select(.name == "needs") | .value | split(" ")[]])}]' \
    "$work/body" >"$work/tasks.json" || fail N3 "read the plan tasks of $issue" "$(body)"
  while IFS= read -r task; do
    block="$(jq -r '.id' <<<"$task")"
    ref_node N3 "dispatch://$issue/artifact/plan-md#b-$block"
    task_node="$node"
    projected=()
    while IFS= read -r target; do
      [[ -n "$target" ]] || continue
      ref_node N3 "$target"
      projected+=("$node")
    done < <(jq -r '.needs[]' <<<"$task")
    with_vars N3 "read the needs tuples out of $task_node" "select coalesce(string_agg(to_id, ',' order by to_id collate \"C\"), '') from (
        select distinct on (to_kind, to_id) to_kind, to_id, asserted from graph_tuples
        where from_kind = 'block' and from_id = :'node' and kind = 'needs' and to_kind = 'block' order by to_kind, to_id, seq desc) latest
      where asserted" "node=$task_node"
    want="$(printf '%s\n' "${projected[@]}" | LC_ALL=C sort | paste -sd, -)"
    [[ "$answer" == "$want" ]] ||
      fail N3 "the needs attribute of $task_node equals the needs tuples out of it" "the attribute names '$want', the store asserts '$answer'"
    examined=$((examined + 1))
  done < <(jq -c '.[]' "$work/tasks.json")
done
[[ "$examined" -ge 3 ]] || fail N3 "the census of plan tasks examines the seeded plan tasks" "it examined $examined"
pass N3 "each plan task's needs attribute equals the needs tuples out of its node: $examined plan tasks examined, zero differences"

# --- N4: no blocked_by member in the issue read ----------------------------------------------------

cp "$work/issue.json" "$work/body"
expect N4 "a search of $key's issue read for a member named blocked_by finds none, where the same search finds needs" \
  '{"blocked_by":0,"needs":1}' '[paths | .[-1] | select(type == "string")] as $names
    | {blocked_by: ([$names[] | select(. == "blocked_by")] | length), needs: ([$names[] | select(. == "needs")] | length)}'
query N4 "read the needs tuples out of $key in the order they were written" \
  "select coalesce(json_agg(to_id order by seq), '[]') from (select distinct on (to_id) to_id, asserted, seq from graph_tuples
     where from_kind = 'issue' and from_id = '$key' and kind = 'needs' and to_kind = 'issue' order by to_id, seq desc) latest where asserted"
expect N4 "the issue read's needs holds the keys the needs tuples hold, in their order" "$(jq -c '.' <<<"$answer")" '.needs'

# --- N5: the write refuses a wait on oneself ------------------------------------------------------

send PATCH "/api/v1/issues/$waited" "$(jq -cn --arg k "$key" --argjson a "$(actor)" '{needs: [$k], actor: $a}')"
if [[ "$status" == 2?? ]]; then
  waited_changed="$waited"
fi
want_status 409 N5 "$waited, which $key waits on, asked to wait on $key"
expect N5 "the write is refused DEPENDENCY_CYCLE, naming $waited" '{"code":"DEPENDENCY_CYCLE","names":true}' \
  --arg w "$waited" '{code, names: (.error | contains($w))}'
send GET "/api/v1/issues/$waited"
want_status 200 N5 "read $waited"
expect N5 "$waited waits on nothing after the refusal" '[]' '.needs'

# --- N6: a wait across projects -------------------------------------------------------------------

[[ -n "$assignee" ]] || fail N6 "the seeded issue names a person, the second project's default assignee" "$(<"$work/issue.json")"
send POST /api/v1/projects "$(jq -cn --arg k "$second_project" --arg d "$assignee" --argjson a "$(actor)" \
  '{key: $k, name: "The second project of needs-order.sh", default_assignee: $d, actor: $a}')"
case "$status" in
  201) pass N6 "created the project $second_project, whose default assignee is $assignee" ;;
  409)
    send GET /api/v1/projects
    want_status 200 N6 "list the projects"
    jq -e --arg k "$second_project" '[.[] | select(.key == $k)] | length == 1' "$work/body" >/dev/null ||
      fail N6 "the project $second_project exists" "the create answered 409 and the list holds no $second_project: $(body)"
    pass N6 "the project $second_project exists already, made by an earlier run: no route removes a project"
    ;;
  *) fail N6 "create the project $second_project" "HTTP $status: $(body)" ;;
esac
create N6 "create an issue in $second_project" "$second_project" "needs-order.sh: an issue of $second_project another project waits on" \
  "An issue of $second_project, which an issue of $project waits on."
far="$created"
create N6 "create an issue in $project" "$project" "needs-order.sh: an issue of $project waiting on one of $second_project" \
  "An issue of $project, which waits on an issue of $second_project."
near="$created"
readonly far near
send PATCH "/api/v1/issues/$near" "$(jq -cn --arg f "$far" --argjson a "$(actor)" '{needs: [$f], actor: $a}')"
want_status 200 N6 "the agent bearer, which names no project, writes $near's needs at $far of $second_project"
expect N6 "$near waits on $far" "[\"$far\"]" '.needs'
want_value N6 "the wait is stored as one issue_links row from $near to $far" 1 \
  "select count(*) from issue_links where issue_key = '$near' and target_key = '$far'"
want_value N6 "and as one needs tuple from $near's node to $far's" 1 \
  "select count(*) from graph_tuples where from_kind = 'issue' and from_id = '$near' and kind = 'needs' and to_kind = 'issue' and to_id = '$far' and asserted"

# --- N7: a cycle of two plan tasks -----------------------------------------------------------------

# The plan is written as an author writes one, in one version whose two tasks name each other;
# the census of N3 and the ordered read below each read what that write stored.
create N7 "create an issue whose plan holds a cycle" "$project" "needs-order.sh: a plan whose two tasks wait on each other" \
  "An issue of needs-order.sh's own, whose plan holds two tasks that wait on each other."
cycled="$created"
readonly cycled
send POST "/api/v1/issues/$cycled/artifacts" "$(jq -cn --arg k "$cycled" --argjson a "$(actor)" '{name: "plan.md", actor: $a,
  content: ":::plan_task{#t-first needs=\"dispatch://\($k)/artifact/plan-md#b-t-second\"}\nThe first task, which waits on the second.\n:::\n\n:::plan_task{#t-second needs=\"dispatch://\($k)/artifact/plan-md#b-t-first\"}\nThe second task, which waits on the first.\n:::\n\n:::plan_task{#t-free}\nA task that waits on nothing.\n:::\n"}')"
want_status 201 N7 "write $cycled's plan, two of whose tasks wait on each other"
query N7 "read the needs tuples out of $cycled's plan tasks" \
  "select coalesce(string_agg(from_id || ' -> ' || to_kind || ':' || to_id, '; ' order by from_id), '') from graph_tuples
     where from_kind = 'block' and from_id like '$cycled/tree#%' and kind = 'needs' and asserted"
readonly cycle_tuples="$answer"
plan_view N7 "read the ordered read of $cycled" "$cycled"
jq -c --arg k "$cycled" "$entries_filter"' | {cycle: ([.[] | select(.cycle) | .key] | sort), positioned_in_cycle: [.[] | select(.cycle and .position != null) | .key],
    free: ([.[] | select(.key == "block:\($k)/tree#t-free") | .position != null] == [true])}' "$work/plan.json" >"$work/cycle.json" ||
  fail N7 "read the cycle" "$(head -c 600 "$work/plan.json")"
[[ "$(<"$work/cycle.json")" == "$(jq -cn --arg k "$cycled" '{cycle: ["block:\($k)/tree#t-first", "block:\($k)/tree#t-second"], positioned_in_cycle: [], free: true}')" ]] ||
  fail N7 "the read lists the cycle with its two nodes, gives neither a position, and still places the task outside it" \
    "it answers $(<"$work/cycle.json"), over the needs tuples the store holds: ${cycle_tuples:-none}; positions: $(jq -c "$entries_filter"' | map({key, position, cycle})' "$work/plan.json")"
pass N7 "the read lists the cycle of t-first and t-second with no position over it, and places t-free; the store holds the needs tuples $cycle_tuples"

pass "$driver" complete
