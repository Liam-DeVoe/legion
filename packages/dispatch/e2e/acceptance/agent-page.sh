#!/usr/bin/env bash
# Drives the agent's node and page of check c-agent-page (spec requirement r-agent-node of
# dispatch://INFRA-1098) against the `bun run dispatch:local` server, over the seeded corpus
# (lib-corpus.sh, which seeds it once where the database lacks it): the seeded issue's role, the
# session that holds it, and the role's page.
#
#   G1  The role, its session and its page are each a node of the store, and no node of kind agent
#       exists: the same count of node kinds finds the role, the session and the page.
#   G2  The session points at the role through part_of, and the role holds a held_by tuple to the
#       session that claims it.
#   G3  The driver ends that claim and reads the retraction: the role holds no held_by now, the
#       store keeps the assertion and the retraction in that order, the role is still a node, and
#       the seeded issue is still routed to it; then it puts the claim back. The end is written
#       through the store's writer (`dispatch-corpus end-claim`, seedcorpus.EndClaim) because
#       `dispatch:local` runs with no bus, and so no outbox; the Go tests that prove the outbox
#       writes the same retraction when a claim ends are TestTheStoreHoldsNodesAndAppendedTuples
#       (internal/dispatch/outbox/store_tuples_test.go) and
#       TestARetriedEventLeavesARoleANewerEventUnclaimedUnheld (store_claims_test.go).
#   G4  The store answers the session as a node with its facts - who made it and when, and its
#       part_of - which main's loadSessionNodes answered bare for any id, and answers no node for an
#       id no row of the store holds.
#   G5  Signed in as DEV_SIGNIN_LOGIN through the dev sign-in route, the driver writes a direct
#       message to the agent, POST /api/v1/agents/{session_id}/messages, which belongs to no issue,
#       and reads it as one node under the agent's page, with one part_of tuple, to that page, after
#       the nodes already there.
#   G6  The agent replies (POST /api/v1/messages/{id}/reply), and the reply reads under the message
#       it answers, not under the head of the thread.
#   G7  As that person the driver writes a message on the seeded issue to the agent's session
#       (POST /api/v1/issues/{key}/messages): it has one part_of tuple, to the issue, and none to the
#       page.
#   G8  The session's conversation read, GET /api/v1/agents/{session_id}/conversation, answers both
#       messages, and the seeded message posted on the issue to the agent whose delivery attempt
#       failed.
#   G9  The agent's reply reaches the person through the one inbox read, GET /api/v1/me/inbox, which
#       answers for that person alone: the agent bearer's read of it answers none of it.
#   G10 No second read answers a session's messages: GET /api/v1/agents/{session_id}/messages
#       answers 404 NOT_FOUND.
#   G11 messages equals the message nodes, and message_deliveries the delivery_attempt nodes with
#       their tuples: each census counts what it examined and reads zero differences.
#
# Inputs: DATABASE_URL (the database the server at DISPATCH_URL serves, on this machine: the
# corpus's loader writes it, ends and puts back the role's claim, and psql reads it), DISPATCH_URL
# (the server), DISPATCH_TOKEN or DISPATCH_TOKEN_FILE (an agent bearer, read here and never
# printed), PROJECT (the project the corpus is seeded into, CORE) and DEV_SIGNIN_LOGIN (the person
# the server's dev sign-in route signs in, alice on `bun run dispatch:local`). Each unmet input is
# exit 2. The claim it ends it puts back, also when a check fails, so the corpus reads as the
# loader wrote it for the next driver; the messages it writes stay, and none of them is a row the
# loader's self-check counts.
#
# Each passing check prints `PASS: <section> <what>`; the first failing one prints
# `FAIL: <section> <what>: <observed>` and the script exits 1. The last line is
# `PASS: agent-page.sh complete`.
#
# Every check is `cmd || fail …`, never part of an `&&` list: under `set -e` a failing command
# anywhere but last in such a list does not stop the script.

# shellcheck disable=SC2016 # the single-quoted jq filters and SQL name jq variables, not shell ones
set -euo pipefail
set +x

usage() {
  cat >&2 <<'USAGE'
Usage: DATABASE_URL=postgres://…@127.0.0.1:5432/<db>?sslmode=disable DISPATCH_URL=http://127.0.0.1:8777 \
         DISPATCH_TOKEN=e2e-token PROJECT=CORE DEV_SIGNIN_LOGIN=alice agent-page.sh
       (DISPATCH_TOKEN_FILE=<file holding the bearer> in place of DISPATCH_TOKEN; DATABASE_URL names
       the database the server at DISPATCH_URL serves, on this machine; the server must mount its
       dev sign-in route, as `bun run dispatch:local` does)
USAGE
}

# shellcheck source-path=SCRIPTDIR source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
# shellcheck source-path=SCRIPTDIR source=lib-corpus.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-corpus.sh"
require_env DATABASE_URL DISPATCH_URL PROJECT DEV_SIGNIN_LOGIN
read_bearer
require_commands curl grep jq psql

readonly driver=agent-page.sh
# shellcheck disable=SC2031 # the DISPATCH_URL lib.sh's `cli` exports is its own; this one is the caller's
readonly base="${DISPATCH_URL%/}"
# shellcheck disable=SC2153 # PROJECT, DATABASE_URL and DEV_SIGNIN_LOGIN are the caller's inputs, checked by name above
readonly project="$PROJECT" database_url="$DATABASE_URL" login="$DEV_SIGNIN_LOGIN"
loopback_database_url "$database_url" || {
  printf 'DATABASE_URL names no database on this machine: the corpus loader writes it\n' >&2
  usage
  exit 2
}
corpus_tools
stamp="$(date +%s)-$$"
readonly stamp
readonly session="agent-page-$stamp"

work_dir
printf 'Authorization: Bearer %s\n' "$token" >"$work/auth"

# The claim the run ended and has not put back: role and session.
ended_role="" ended_session=""
cleanup() {
  local code=$?
  if [[ -n "$ended_role" ]]; then
    corpus_run restore-claim --role "$ended_role" --session "$ended_session"
    [[ "$corpus_code" == 0 ]] ||
      printf '%s: could not put back the claim of %s on %s: %s\n' "$driver" "$ended_session" "$ended_role" "$(<"$work/corpus.err")" >&2
  fi
  rm -rf "$work"
  exit "$code"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# held_by: the sessions the role's held_by names now, sorted and comma-separated, in $answer.
held_by() {
  query "$1" "read the holder of role $role" "select coalesce(string_agg(to_id, ',' order by to_id), '') from (
      select distinct on (to_kind, to_id) to_id, asserted from graph_tuples
      where from_kind = 'role' and from_id = '$role' and kind = 'held_by' order by to_kind, to_id, seq desc) latest
    where asserted"
}

# part_of SECTION KIND ID: the far ends of the part_of tuples the store asserts now out of the node
# KIND:ID, each `kind:id`, sorted and comma-separated, in $answer.
part_of() {
  with_vars "$1" "read the part_of tuples out of $2:$3" "select coalesce(string_agg(to_kind || ':' || to_id, ',' order by to_kind, to_id), '') from (
      select distinct on (to_kind, to_id) to_kind, to_id, asserted from graph_tuples
      where from_kind = :'kind' and from_id = :'id' and kind = 'part_of' order by to_kind, to_id, seq desc) latest
    where asserted" "kind=$2" "id=$3"
}

# message_id: the id of the message the last answer is, or holds.
message_id() { jq -er '.id // .message.id // .reply.id' "$work/body"; }

# --- setup: the corpus, its role and the session that holds it --------------------------------------

corpus_ensure setup
key="$corpus_issue"
readonly key
send GET "/api/v1/issues/$key"
want_status 200 setup "read the seeded issue $key"
route="$(jq -r '.route // empty' "$work/body")"
[[ "$route" == role:* ]] || fail setup "the seeded issue is routed to a role" "it is routed to '${route:-nothing}'"
readonly role="${route#role:}"
readonly page="role/$role"
held_by setup
agent="$answer"
[[ -n "$agent" && "$agent" != *,* ]] || fail setup "one session holds the role $role" "the store names '$agent'"
readonly agent
pass setup "the seeded issue $key is routed to the role $role, which the session $agent holds"

# --- G1: the role, its session and its page are nodes; no agent kind --------------------------------

query G1 "read the nodes of the role, the session and the page" \
  "select coalesce(string_agg(kind || ':' || id || ':' || state, ',' order by kind), '') from graph_nodes
   where (kind = 'role' and id = '$role') or (kind = 'session' and id = '$agent') or (kind = 'page' and id = '$page')"
[[ "$answer" == "page:$page:live,role:$role:live,session:$agent:live" ]] ||
  fail G1 "the role, its session and its page are each one live node of the store" "the store holds '$answer'"
pass G1 "the role, its session and its page are each one live node of the store: $answer"
query G1 "count the nodes of kind agent beside those of kind role, session and page" \
  "select json_build_object('agent', count(*) filter (where kind = 'agent'), 'role', count(*) filter (where kind = 'role'),
     'session', count(*) filter (where kind = 'session'), 'page', count(*) filter (where kind = 'page')) from graph_nodes"
jq -e '.agent == 0 and .role > 0 and .session > 0 and .page > 0' <<<"$answer" >/dev/null ||
  fail G1 "no node of kind agent exists, where the same count finds roles, sessions and pages" "it counts $answer"
pass G1 "no node of kind agent exists, where the same count finds roles, sessions and pages: $answer"

# --- G2: part_of and held_by -----------------------------------------------------------------------

part_of G2 session "$agent"
[[ ",$answer," == *",role:$role,"* ]] || fail G2 "the session $agent points at the role $role through part_of" "its part_of reaches '$answer'"
pass G2 "the session $agent points at the role $role through part_of"
pass G2 "the role $role holds a held_by tuple to $agent, the session that claims it"

# --- G3: the claim ends, and is put back ------------------------------------------------------------

corpus_claim G3 "end the claim of $agent on $role" end-claim --role "$role"
ended_role="$role" ended_session="$agent"
grep -qx "end-claim: retracted held_by role:$role -> session:$agent" "$work/corpus.out" ||
  fail G3 "dispatch-corpus end-claim retracts the held_by of $role to $agent" "it printed: $(<"$work/corpus.out")"
pass G3 "ended the claim of $agent on $role through the store's writer (dispatch-corpus end-claim), because dispatch:local runs with no bus; TestTheStoreHoldsNodesAndAppendedTuples (internal/dispatch/outbox/store_tuples_test.go) and TestARetriedEventLeavesARoleANewerEventUnclaimedUnheld (store_claims_test.go) prove the outbox writes the same retraction when a claim ends"
held_by G3
[[ -z "$answer" ]] || fail G3 "the role $role holds no held_by once the claim ends" "it names '$answer'"
pass G3 "the role $role holds no held_by once the claim ends"
query G3 "read the history of $role's held_by to $agent" \
  "select coalesce(string_agg(case when asserted then 'asserted' else 'retracted' end, ',' order by seq), '') from graph_tuples
   where from_kind = 'role' and from_id = '$role' and kind = 'held_by' and to_kind = 'session' and to_id = '$agent'"
[[ "$answer" == *asserted,retracted ]] ||
  fail G3 "the store keeps the claim and its retraction, in that order" "the held_by rows of $role to $agent read '$answer'"
pass G3 "the store keeps the claim and its retraction, in that order: $answer (read from graph_tuples through DATABASE_URL: no route answers a role's node, and parties have no dispatch:// form)"
want_value G3 "the role $role is still a node" 1 "select count(*) from graph_nodes where kind = 'role' and id = '$role' and state = 'live'"
send GET "/api/v1/issues/$key"
want_status 200 G3 "read the seeded issue $key with the claim ended"
expect G3 "the seeded issue is still routed to the role $role" "\"$route\"" '.route'
corpus_claim G3 "put back the claim of $agent on $role" restore-claim --role "$role" --session "$agent"
ended_role="" ended_session=""
held_by G3
[[ "$answer" == "$agent" ]] || fail G3 "the claim of $agent on $role is back" "the role's held_by names '$answer'"
pass G3 "put the claim of $agent on $role back through the store's writer (dispatch-corpus restore-claim)"

# --- G4: the session is a node with its facts --------------------------------------------------------

query G4 "read the node of the session $agent" \
  "select json_build_object('made', made_at is not null, 'made_by', made_by->>'id', 'visibility', visibility) from graph_nodes
   where kind = 'session' and id = '$agent'"
jq -e --arg s "$agent" '.made and .made_by == $s and .visibility == "project"' <<<"$answer" >/dev/null ||
  fail G4 "the store answers the session $agent with who made it and when" "it answers $answer"
pass G4 "the store answers the session $agent as a node with its facts, not bare: $answer, beside its part_of to $role"
want_value G4 "the store answers no node for a session id no row holds" 0 \
  "select count(*) from graph_nodes where kind = 'session' and id = 'agent-page-unheld-$stamp'"

# --- G5: a direct message hangs from the agent's page ------------------------------------------------

sign_in G5 "$login"
query G5 "read the nodes under the page $page before the message" \
  "select count(*) from graph_tuples where kind = 'part_of' and to_kind = 'page' and to_id = '$page' and asserted"
readonly under_page_before="$answer"
person_send POST "/api/v1/agents/$(q "$agent")/messages" "$(jq -cn '{body: "agent-page.sh writes to the agent directly.", delivery: "steer"}')"
want_status 201 G5 "$login writes a direct message to $agent"
direct="$(message_id)" || fail G5 "$login writes a direct message to $agent" "no id: $(body)"
readonly direct
expect G5 "the direct message $direct belongs to no issue" null '.issue_key'
part_of G5 message "$direct"
[[ "$answer" == "page:$page" ]] ||
  fail G5 "the direct message $direct is one node under the agent's page $page, with one part_of tuple, to that page" \
    "its part_of tuples reach '${answer:-nothing}'"
query G5 "read the nodes under $page in the order they were placed" \
  "select coalesce(string_agg(from_kind || ':' || from_id, ',' order by seq), '') from graph_tuples
   where kind = 'part_of' and to_kind = 'page' and to_id = '$page' and asserted"
[[ "$answer" == *"message:$direct" ]] ||
  fail G5 "the direct message comes after the $under_page_before nodes already under $page" "the page holds, in order: $answer"
pass G5 "the direct message $direct is one node under $page, with one part_of tuple, after the $under_page_before nodes already there"

# --- G6: the agent's reply hangs from the message it answers -----------------------------------------

attempt="$(jq -er '.deliveries[0].attempt // 1' "$work/body" 2>/dev/null || printf 1)"
send POST "/api/v1/messages/$direct/reply" "$(jq -cn --arg s "$agent" --argjson n "$attempt" \
  '{attempt: $n, body: "The agent answers the direct message.", actor: {kind: "session", id: $s}}')"
[[ "$status" == 2?? ]] || fail G6 "the agent $agent replies to $direct" "HTTP $status: $(body)"
reply="$(message_id)" || fail G6 "the agent $agent replies to $direct" "no id: $(body)"
readonly reply
part_of G6 message "$reply"
[[ "$answer" == "message:$direct" ]] ||
  fail G6 "the reply $reply reads under the message it answers, $direct" "its part_of tuples reach '${answer:-nothing}'"
pass G6 "the reply $reply reads under the message it answers, $direct"

# --- G7: a message on the issue hangs from the issue -------------------------------------------------

person_send POST "/api/v1/issues/$key/messages" "$(jq -cn --arg s "$agent" \
  '{body: "agent-page.sh writes to the agent on the seeded issue.", target: "session:\($s)", delivery: "steer"}')"
want_status 201 G7 "$login writes a message on $key to $agent"
posted="$(message_id)" || fail G7 "$login writes a message on $key to $agent" "no id: $(body)"
readonly posted
part_of G7 message "$posted"
[[ "$answer" == "issue:$key" ]] ||
  fail G7 "the message $posted on $key has one part_of tuple, to the issue, and none to the page" "its part_of tuples reach '${answer:-nothing}'"
pass G7 "the message $posted on $key has one part_of tuple, to the issue, and none to the page"

# --- G8: the session's conversation read -------------------------------------------------------------

query G8 "read the seeded message on $key to $agent whose delivery attempt failed" \
  "select coalesce(min(m.id::text), '') from messages m where m.issue_key = '$key' and m.target = 'session:$agent'
     and exists (select 1 from message_deliveries d where d.message_id = m.id)
     and not exists (select 1 from message_deliveries d where d.message_id = m.id and d.state <> 'failed')"
failed_message="$answer"
[[ -n "$failed_message" ]] || fail G8 "the seed holds a message on $key to $agent whose delivery attempt failed" "none"
readonly failed_message
send GET "/api/v1/agents/$(q "$agent")/conversation"
want_status 200 G8 "read the conversation of $agent"
expect G8 "the conversation answers the direct message, the message on $key, and the seeded one whose attempt failed" \
  '{"direct":true,"failed":true,"posted":true}' --arg d "$direct" --arg p "$posted" --arg f "$failed_message" \
  '[.. | objects | .id? // empty] as $ids | {direct: ($ids | index($d) != null), posted: ($ids | index($p) != null), failed: ($ids | index($f) != null)}'

# --- G9: the one inbox read --------------------------------------------------------------------------

person_send GET /api/v1/me/inbox
want_status 200 G9 "read $login's inbox"
expect G9 "the agent's reply reaches $login through GET /api/v1/me/inbox" true --arg r "$reply" '[.. | strings] | index($r) != null'
send GET /api/v1/me/inbox
expect G9 "the read answers for $login's own person alone: the agent bearer's read answers none of the reply" false \
  --arg r "$reply" '[.. | strings] | index($r) != null'

# --- G10: no second read of a session's messages ----------------------------------------------------

send GET "/api/v1/agents/$(q "$agent")/messages"
want_status 404 G10 "GET /api/v1/agents/{session_id}/messages"
expect G10 "GET /api/v1/agents/{session_id}/messages answers 404 NOT_FOUND" '"NOT_FOUND"' '.code'

# --- G11: the censuses --------------------------------------------------------------------------------

query G11 "census messages against the message nodes" \
  "with rows as (select id::text as id from messages), nodes as (select id from graph_nodes where kind = 'message')
   select json_build_object('rows', (select count(*) from rows), 'nodes', (select count(*) from nodes),
     'differences', (select count(*) from ((select * from rows except select * from nodes) union all (select * from nodes except select * from rows)) d))"
jq -e '.rows > 0 and .nodes > 0 and .differences == 0' <<<"$answer" >/dev/null || fail G11 "messages equals the message nodes" "the census read $answer"
pass G11 "messages equals the message nodes: $answer"
query G11 "census message_deliveries against the delivery_attempt nodes and their tuples" \
  "with rows as (select 'message/' || message_id::text || '/' || attempt as id from message_deliveries),
     nodes as (select id from graph_nodes where kind = 'delivery_attempt' and id like 'message/%'),
     tied as (select n.id from nodes n where exists (select 1 from graph_tuples t where t.from_kind = 'delivery_attempt' and t.from_id = n.id and t.asserted))
   select json_build_object('rows', (select count(*) from rows), 'nodes', (select count(*) from nodes), 'untied', (select count(*) from nodes) - (select count(*) from tied),
     'differences', (select count(*) from ((select * from rows except select * from nodes) union all (select * from nodes except select * from rows)) d))"
jq -e '.rows > 0 and .nodes > 0 and .untied == 0 and .differences == 0' <<<"$answer" >/dev/null ||
  fail G11 "message_deliveries equals the delivery_attempt nodes with their tuples" "the census read $answer"
pass G11 "message_deliveries equals the delivery_attempt nodes with their tuples: $answer"

pass "$driver" complete
