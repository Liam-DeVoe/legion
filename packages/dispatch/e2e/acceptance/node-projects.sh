#!/usr/bin/env bash
# Drives the project of every node (dispatch://INFRA-3862, slice N of its plan): every issue,
# document, ask, message, comment and block a create writes is a node of the store that names its
# project, written by its kind's own writer in the create's transaction.
#
# It creates one object of each kind through the API - an issue (whose spec is a document with
# blocks), a document uploaded to the issue, a document of the project, an ask, a comment and a
# message on the issue - and reads each one's node in graph_nodes: in the project, with the
# `project` value tuple that states it. It then runs `envoy-dispatch backfill-graph-store` and
# `envoy-dispatch census --store` and holds the census to `missing=0 differs=0 unscoped=0` for
# every project-owned kind. Last, the control: it takes the ask's node away and holds the census to
# naming exactly that ask, `missing=1` on the ask kind and `census: project incomplete ask <id>:
# missing`, so the zeros before are read from an instrument that sees a missing node.
#
# Inputs: DISPATCH_URL (the server), DISPATCH_TOKEN or DISPATCH_TOKEN_FILE (an agent bearer, read
# here and never printed), PROJECT (an existing project key), DATABASE_URL (the database that
# server serves, which `envoy-dispatch` reads from its own environment), and `envoy-dispatch` on
# PATH. It reads this pod's own test database and never a deployment's: it refuses a DATABASE_URL
# whose host is not loopback, and the one node it deletes is the ask this run made.
#
# The node writers' own grep, where a reader wants one: `graphstore\.(WriteUncaused|Write|WriteArms)\(|graphstore\.Node\{`,
# with api/comment_create.go as its control. `graphstore.Caused` is left out on purpose: it names
# every deferred write of the store, a block index's and a follow's among them, so it cannot tell a
# node writer from the cause of another write.
#
# Each passing check prints `PASS: <section> <what>`; the first failing one prints
# `FAIL: <section> <what>: <observed>` and the script exits 1.

set -euo pipefail
set +x

usage() {
  cat >&2 <<'USAGE'
Usage: DATABASE_URL=postgres://user@127.0.0.1:5433/db DISPATCH_URL=http://127.0.0.1:8777 \
         DISPATCH_TOKEN=e2e-token PROJECT=CORE node-projects.sh
       (DISPATCH_TOKEN_FILE=<file holding the bearer> in place of DISPATCH_TOKEN; the built
       `envoy-dispatch` must be on PATH, and DATABASE_URL must name the database that server
       serves, on this machine)
USAGE
}

. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_env DISPATCH_URL PROJECT DATABASE_URL
require_commands curl jq psql envoy-dispatch
read_bearer

readonly base="${DISPATCH_URL%/}"
readonly project="$PROJECT"
readonly database_url="$DATABASE_URL"
case "$database_url" in
  *@127.0.0.1:* | *@localhost:* | *@\[::1\]:* | *@127.0.0.1/* | *@localhost/*) ;;
  *)
    printf 'node-projects.sh: DATABASE_URL names no loopback host; this driver deletes a node and runs only on its own test database\n' >&2
    exit 2
    ;;
esac
stamp="$(date +%s)-$$"
readonly stamp
readonly session="node-projects-$stamp"

umask 077
work="$(mktemp -d)"
readonly work
printf 'Authorization: Bearer %s\n' "$token" >"$work/auth"
key=""
cleanup() {
  local code=$?
  if [[ -n "$key" ]]; then
    send PATCH "/api/v1/issues/$key" \
      "$(jq -cn --arg s "$session" '{status: "done", actor: {kind: "session", id: $s}}')"
    [[ "$status" == 200 ]] || printf 'node-projects.sh: could not close %s: HTTP %s\n' "$key" "$status" >&2
  fi
  rm -rf "$work"
  exit "$code"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

send() {
  local -a data=()
  [[ $# -lt 3 ]] || data=(-H 'Content-Type: application/json' --data-binary "$3")
  status="$(curl -sS -X "$1" -o "$work/body" -w '%{http_code}' -H "@$work/auth" "${data[@]}" \
    "$base$2" 2>"$work/curl")" || status="curl failed: $(<"$work/curl")"
}

actor() { jq -cn --arg s "$session" '{kind: "session", id: $s}'; }

# node_names_project SECTION KIND ID: the node KIND/ID is in the project, and its `project` value
# tuple says so.
node_names_project() {
  query "$1" "the $2 node of $3 names $project" \
    "select coalesce((select project_key from graph_nodes where kind = '$2' and id = '$3'), '(no node)')"
  [[ "$answer" == "$project" ]] || fail "$1" "the $2 node of $3 names $project" "it names '$answer'"
  query "$1" "the $2 node of $3 states its project" \
    "select count(*) from graph_tuples where from_kind = '$2' and from_id = '$3' and kind = 'project' and to_kind = 'value' and to_id = '$project'"
  [[ "$answer" == 1 ]] || fail "$1" "the $2 node of $3 states its project" "it states it $answer time(s)"
  pass "$1" "the $2 node of $3 names $project"
}

# census_line KIND: the census's project line of KIND, from the last census run.
census_line() { grep -E "^census: project kind=$1 " "$work/census" || true; }

section=create
send POST /api/v1/issues "$(jq -cn --arg p "$project" --argjson a "$(actor)" \
  '{project: $p, title: "Every node names its project", spec: "First paragraph.\n\nSecond paragraph.\n", actor: $a}')"
want_status 201 "$section" "create an issue"
key="$(jq -r .key "$work/body")"
pass "$section" "created $key"
node_names_project "$section" issue "$key"
node_names_project "$section" artifact "$key/spec"
query "$section" "the spec's blocks name $project" \
  "select count(*) filter (where project_key is distinct from '$project') || '/' || count(*) from graph_nodes where kind = 'block' and starts_with(id, '$key/spec#')"
[[ "$answer" =~ ^0/[1-9] ]] || fail "$section" "the spec's blocks name $project" "blocks naming another / all: $answer"
pass "$section" "the spec's blocks name $project"

send POST "/api/v1/issues/$key/artifacts" "$(jq -cn --argjson a "$(actor)" '{name: "notes.md", content: "# Notes\n", actor: $a}')"
want_status 201 "$section" "upload an issue document"
node_names_project "$section" artifact "$(jq -r .artifact.ref_key "$work/body")"

send POST "/api/v1/projects/$project/artifacts" "$(jq -cn --argjson a "$(actor)" --arg n "Playbook $stamp.md" '{name: $n, content: "# Playbook\n", actor: $a}')"
want_status 201 "$section" "upload a project document"
node_names_project "$section" artifact "$(jq -r .artifact.ref_key "$work/body")"

send POST "/api/v1/issues/$key/asks" "$(jq -cn --argjson a "$(actor)" '{question: "Does every node name its project?", actor: $a}')"
want_status 201 "$section" "open an ask"
ask="$(jq -r .id "$work/body")"
node_names_project "$section" ask "$ask"

send POST "/api/v1/issues/$key/comments" "$(jq -cn --argjson a "$(actor)" '{body: "A remark.", actor: $a}')"
want_status 201 "$section" "write a comment"
node_names_project "$section" comment "$(jq -r .id "$work/body")"

send POST "/api/v1/issues/$key/messages" "$(jq -cn --argjson a "$(actor)" '{body: "A message.", actor: $a}')"
want_status 201 "$section" "write a message"
node_names_project "$section" message "$(jq -r .id "$work/body")"

section=census
envoy-dispatch backfill-graph-store >"$work/backfill" 2>&1 ||
  fail "$section" "backfill-graph-store runs" "$(tail -n 5 "$work/backfill")"
pass "$section" "backfill-graph-store runs"
envoy-dispatch census --store >"$work/census" 2>&1 || true
for kind in issue artifact ask message comment block; do
  line="$(census_line "$kind")"
  [[ "$line" =~ rows=[1-9][0-9]*\ missing=0\ differs=0\ unscoped=0$ ]] ||
    fail "$section" "the $kind census reads zeros over rows it read" "${line:-no line for $kind}"
  pass "$section" "the $kind census reads zeros: ${line#census: project }"
done

section=control
query "$section" "take the ask's node away" "
  with tuples as (delete from graph_tuples where from_kind = 'ask' and from_id = '$ask' returning 1),
       inbound as (delete from graph_tuples where to_kind = 'ask' and to_id = '$ask' returning 1)
  delete from graph_nodes where kind = 'ask' and id = '$ask' returning (select count(*) from tuples)"
pass "$section" "took the ask's node away"
envoy-dispatch census --store >"$work/census" 2>&1 && fail "$section" "the census refuses" "it exited 0" || true
line="$(census_line ask)"
[[ "$line" =~ missing=1\ differs=0\ unscoped=0$ ]] || fail "$section" "the ask census reads one missing node" "${line:-no line for ask}"
pass "$section" "the ask census reads one missing node"
grep -qxF "census: project incomplete ask $ask: missing" "$work/census" ||
  fail "$section" "the census names the ask" "$(grep '^census: project incomplete' "$work/census" | head -n 5)"
pass "$section" "the census names ask $ask"
