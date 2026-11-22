#!/usr/bin/env bash
# Drives the chain read of check c-chain (spec requirements r-chain and r-reasoning-tree of
# dispatch://INFRA-1098) against the `bun run dispatch:local` server, over the seeded corpus
# (lib-corpus.sh, which seeds it once where the database lacks it). Every read is one
# GET /api/v1/issues/{key}/chain of the seeded issue unless it says otherwise, and the run prints
# the per-check standing each read answered (`shape:` lines): compared_with and pass_stands of each
# check, and commit_stands, check_words_moved and retracted of each result.
#
#   C1  One read returns the chain decision to requirement to check to result, with the words of
#       each hop, and the same read walks it back from the result to the decision. The read names
#       the commit it judged against: with no ?at= it names none and says what each check was
#       compared with; with ?at=<the seeded results' commit> it names that commit.
#   C2  The read reports each gap of the seed: the requirements no decision shaped, the requirement
#       no check serves, the check with no result, and the plan task that serves nothing.
#   C3  The read tells evidence from proof: the check whose only result failed reads unproven, the
#       result named failed; the check whose only result was recorded against earlier words reads
#       unproven, the words moved, which check_results' check_version and check_sha256 show beside
#       the check's own; the check whose pass stands reads proved.
#   C4  The check that serves three requirements is one node with one id in the chain of each of
#       them. The driver records one pass against it, reads that result counted once and the check
#       proved, then retracts it (POST /api/v1/results/{id}/retract) and reads the check unproven,
#       the result named retracted: a retracted result counts as nothing.
#   C5  The chain is read from the authoritative tuples and no second copy. On an issue of the
#       run's own it retracts one check's serves tuple and reads the check serving nothing and the
#       requirement it left a gap; it appends a serves tuple to another requirement and reads the
#       chain change; each write's tuples are read from graph_tuples, and no index is rebuilt
#       between a write and its read. The weaker control, kept as one: the same read after the
#       server's block-index pass ran again (duty_passes) answers the same.
#   C6  Placement and coverage apart: the three-way check's parent is one node, the tree read holds
#       no second copy of it, and its three requirements sit under two criteria; it retargets one
#       of its serves tuples and reads the parent unchanged, then puts the tuple back; the plan task
#       that serves nothing hangs from the issue; no check is the parent of a requirement; last it
#       moves the check under another requirement it serves through the re-parent route
#       POST /api/v1/issues/{key}/nodes/{id}/parent and reads coverage unchanged.
#
# Inputs: DATABASE_URL (the database the server at DISPATCH_URL serves, on this machine: the
# corpus's loader writes it and psql reads it), DISPATCH_URL (the server), DISPATCH_TOKEN or
# DISPATCH_TOKEN_FILE (an agent bearer, read here and never printed) and PROJECT (the project the
# corpus is seeded into, CORE). Each unmet input is exit 2. Every write is the agent bearer's,
# acting for a session of this run's own. What it changes of the seeded issue it puts back - the
# retargeted serves tuple, and the pass it records it retracts - so the corpus reads as the loader
# wrote it for the next driver; the issue it writes it closes, also when a check fails.
#
# Each passing check prints `PASS: <section> <what>`; the first failing one prints
# `FAIL: <section> <what>: <observed>` and the script exits 1. The last line is
# `PASS: chain.sh complete`.
#
# Every check is `cmd || fail …`, never part of an `&&` list: under `set -e` a failing command
# anywhere but last in such a list does not stop the script.

# shellcheck disable=SC2016 # the single-quoted jq filters and SQL name jq variables, not shell ones
set -euo pipefail
set +x

usage() {
  cat >&2 <<'USAGE'
Usage: DATABASE_URL=postgres://…@127.0.0.1:5432/<db>?sslmode=disable DISPATCH_URL=http://127.0.0.1:8777 \
         DISPATCH_TOKEN=e2e-token PROJECT=CORE chain.sh
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
require_commands curl jq psql seq sleep

readonly driver=chain.sh
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
readonly session="chain-$stamp"

work_dir
printf 'Authorization: Bearer %s\n' "$token" >"$work/auth"

key=""
own=""
# The seeded check's serves tuples as the run found them, put back where the run stopped between
# its retarget and the retarget back.
restore_serves=""
recorded=""
retracted=0
cleanup() {
  local code=$?
  if [[ -n "$restore_serves" ]]; then
    retype_serves C6 "put back the serves tuples of k-three" "$key" verification-md k-three "$restore_serves" ||
      printf '%s: could not put back the serves tuples of k-three: %s\n' "$driver" "$(body)" >&2
  fi
  if [[ -n "$recorded" && "$retracted" == 0 ]]; then
    send POST "/api/v1/results/$recorded/retract" "$(jq -cn --argjson a "$(actor)" \
      '{reason: "chain.sh stopped before its retraction; retracting the pass it recorded.", actor: $a}')"
    [[ "$status" == 2?? ]] ||
      printf '%s: could not retract the result %s: HTTP %s: %s\n' "$driver" "$recorded" "$status" "$(body)" >&2
  fi
  [[ -z "$own" ]] || close_issue "$own"
  rm -rf "$work"
  exit "$code"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# chain SECTION WHAT ISSUE [AT]: the chain read of ISSUE, at the commit AT where given, kept in
# $work/chain.json and $work/body.
chain() {
  send GET "/api/v1/issues/$3/chain${4:+?at=$4}"
  want_status 200 "$1" "$2"
  cp "$work/body" "$work/chain.json"
}

# id_of BLOCK [FILE]: the store id of the requirement, check or plan task BLOCK of the last chain.
id_of() {
  jq -r --arg b "$1" '[(.requirements, .checks, .plan_tasks)[] | select(.block_id == $b) | .id] | first // empty' \
    "${2:-$work/chain.json}"
}

# shape SECTION: prints the per-check standing the last chain read answered, one line a check.
shape() {
  jq -r --arg s "$1" '.checks[] | "shape: \($s) \(.block_id): " + ({compared_with, compared_nothing, pass_stands,
    results} | tojson)' "$work/chain.json"
  jq -r --arg s "$1" '.results[] | "shape: \($s) result \(.result.id) of \(.result.check_block_id): "
    + ({verdict: .result.verdict, commit_stands: .result.commit_stands, check_words_moved: .result.check_words_moved,
        retracted: .result.retracted, proves} | tojson)' "$work/chain.json"
}

# retype_serves SECTION WHAT ISSUE SLUG BLOCK SERVES: the check BLOCK of the document SLUG of ISSUE
# takes the serves SERVES, a JSON list of addresses, its other attributes as its blocks read answers
# them, through a retype op of the document's edits: the write that states a block's attributes.
# An empty list leaves the check serving nothing.
retype_serves() {
  local attributes
  send GET "/api/v1/issues/$3/artifacts/$4/blocks"
  [[ "$status" == 200 ]] || return 1
  attributes="$(jq -c --arg b "$5" --argjson s "$6" '[.[] | select(.id == $b) | .attributes[]
      | select(.name != "serves") | {key: .name, value: (if .kind == "block[]" then (.value | split(" ")) else .value end)}]
      | from_entries | if ($s | length) > 0 then . + {serves: $s} else . end' "$work/body")" || return 1
  send POST "/api/v1/issues/$3/artifacts/$4/edits" "$(jq -cn --arg b "$5" --argjson at "$attributes" --argjson a "$(actor)" \
    '{ops: [{op: "retype", block: $b, type: "check", attributes: $at}], actor: $a}')"
  [[ "$status" == 200 ]]
}

# current_serves SECTION WHAT NODE: the far ends of the serves tuples the store asserts now out of
# the block node NODE, sorted and comma-separated, in $answer, beside how many rows of that
# relation the store holds, retracted ones too, in $rows.
current_serves() {
  with_vars "$1" "$2" "select coalesce(string_agg(to_id, ',' order by to_id), '') from (
      select distinct on (to_kind, to_id) to_id, asserted from graph_tuples
      where from_kind = 'block' and from_id = :'node' and kind = 'serves' order by to_kind, to_id, seq desc) latest
    where asserted" "node=$3"
  local held="$answer"
  with_vars "$1" "$2" "select count(*) from graph_tuples where from_kind = 'block' and from_id = :'node' and kind = 'serves'" "node=$3"
  rows="$answer"
  answer="$held"
}

# --- setup: the corpus ---------------------------------------------------------------------------

corpus_ensure setup
key="$corpus_issue"
readonly key

chain setup "read the chain of the seeded issue $key with no commit named" "$key"
cp "$work/chain.json" "$work/chain-none.json"
commit="$(jq -r '[.results[] | select(.result.check_block_id == "k-pass") | .result.commit_sha] | first // empty' "$work/chain.json")"
[[ "$commit" =~ ^[0-9a-f]{40}$ ]] ||
  fail setup "the seeded pass of k-pass names its commit" "the chain holds no result of k-pass: $(body)"
readonly commit
pass setup "the seeded pass of k-pass was recorded at $commit, the commit the reads below judge against"

# --- C1: decision to requirement to check to result, and back -----------------------------------

shape C1
cp "$work/chain-none.json" "$work/body"
expect C1 "with no commit named the read names none, and each check says what it was compared with" \
  '{"at":null,"checks_saying":true}' \
  '{at, checks_saying: ((.checks | length) > 0 and all(.checks[]; (.compared_with | length) > 0 or (.compared_nothing // "") != ""))}'

chain C1 "read the chain of $key at $commit" "$key" "$commit"
shape C1
expect C1 "the read names the commit it judged against, $commit, and compares every check with it alone" \
  "$(jq -cn --arg c "$commit" '{at: $c, compared: [{commit: $c, kind: "named"}]}')" \
  '{at, compared: ([.checks[].compared_with[]] | unique)}'

r_one="$(id_of r-one)" r_two="$(id_of r-two)" r_three="$(id_of r-three)" r_said="$(id_of r-said)"
k_three="$(id_of k-three)" k_pass="$(id_of k-pass)" k_fail="$(id_of k-fail)" k_stale="$(id_of k-stale)" k_bare="$(id_of k-bare)"
t_none="$(id_of t-none)"
for name in r_one r_two r_three r_said k_three k_pass k_fail k_stale k_bare t_none; do
  [[ -n "${!name}" ]] || fail C1 "the chain holds the seeded node ${name//_/-}" "$(body)"
done
readonly r_one r_two r_three r_said k_three k_pass k_fail k_stale k_bare t_none
pass C1 "the chain holds the seeded requirements, checks and plan tasks, each once by its store id"

query C1 "read the answered ask q-answered of $key" \
  "select json_build_object('id', id, 'question', question) from asks where issue_key = '$key' and block_id = 'q-answered'"
ask_id="$(jq -r '.id' <<<"$answer")" ask_question="$(jq -r '.question' <<<"$answer")"
readonly ask_id ask_question
expect C1 "the decision is the answered ask q-answered, with its question and its answer, and it shaped r-one" \
  "$(jq -cn --arg id "$ask_id" --arg q "$ask_question" --arg r "$r_one" '[{answered: true, id: $id, kind: "ask", shaped: [$r], text: $q}]')" \
  '[.decisions[] | {kind, id, text, answered: ((.answer // "") != ""), shaped}]'
expect C1 "r-one names that decision and the three checks that serve it, each with its words" \
  "$(jq -cn --arg d "$ask_id" --arg a "$k_three" --arg b "$k_pass" --arg c "$k_bare" '{checks: ([$a, $b, $c] | sort), decisions: [$d], words: true}')" \
  --arg r "$r_one" '.requirements[] | select(.id == $r) | {decisions, checks: (.checks | sort), words: ((.text | length) > 0)}'
result_pass="$(jq -r --arg k "$k_pass" '.checks[] | select(.id == $k) | .results[0] // empty' "$work/chain.json")"
readonly result_pass
expect C1 "k-pass names its one result, a pass at $commit, and every check of the chain carries its words" \
  "$(jq -cn --arg r "$result_pass" --arg c "$commit" '{commit: $c, results: [$r], verdict: "pass", words: true}')" \
  --arg k "$k_pass" --arg r "$result_pass" '{results: (.checks[] | select(.id == $k) | .results),
    verdict: (.results[] | select(.result.id == $r) | .result.verdict), commit: (.results[] | select(.result.id == $r) | .result.commit_sha),
    words: all(.checks[]; (.text | length) > 0)}'
expect C1 "the same read walks back from that result to k-pass, to r-one and to the answered ask" \
  "$(jq -cn --arg k "$k_pass" --arg r "$r_one" --arg d "$ask_id" '{check: $k, decisions: [$d], proves: true, requirements: [$r]}')" \
  --arg r "$result_pass" '.results[] | select(.result.id == $r) | {check, requirements, decisions, proves}'

# --- C2: the gaps of the seed ---------------------------------------------------------------------

gaps="$(jq -c '[.gaps[] | [.kind, .node]] | sort' "$work/chain.json")"
pass C2 "the read examined $(jq '.requirements | length' "$work/chain.json") requirements, $(jq '.checks | length' "$work/chain.json") checks and $(jq '.plan_tasks | length' "$work/chain.json") plan tasks, and reports $(jq 'length' <<<"$gaps") gaps: $(jq -r '[.gaps[].line] | join("; ")' "$work/chain.json")"
for gap in "no_decision $r_two" "no_decision $r_three" "no_decision $r_said" "no_check $r_said" "no_result $k_bare" "serves_nothing $t_none"; do
  jq -e --arg k "${gap%% *}" --arg n "${gap#* }" 'index([[$k, $n]]) != null' <<<"$gaps" >/dev/null ||
    fail C2 "the read reports the gap $gap" "its gaps are $gaps"
  pass C2 "the read reports the gap ${gap%% *} of ${gap#* }"
done
# The absence beside its control: the same list that names r-said's missing check names none of
# the three requirements a check serves.
expect C2 "no requirement a check serves is reported as lacking one" '[]' \
  --arg a "$r_one" --arg b "$r_two" --arg c "$r_three" '[.gaps[] | select(.kind == "no_check" and (.node == $a or .node == $b or .node == $c)) | .node]'

# --- C3: evidence is not proof --------------------------------------------------------------------

result_fail="$(jq -r --arg k "$k_fail" '.checks[] | select(.id == $k) | .results[0] // empty' "$work/chain.json")"
result_stale="$(jq -r --arg k "$k_stale" '.checks[] | select(.id == $k) | .results[0] // empty' "$work/chain.json")"
readonly result_fail result_stale
expect C3 "k-fail, whose only result failed, reads unproven, and the read says that result failed" \
  "$(jq -cn --arg r "$result_fail" '{proved: false, results: [$r], unproven: [{reason: "failed", result: $r}], verdict: "fail"}')" \
  --arg k "$k_fail" '(.checks[] | select(.id == $k)) as $c | {proved: $c.proved, results: $c.results,
    unproven: [$c.unproven[] | select(.reason == "failed")], verdict: (.results[] | select(.result.id == $c.results[0]) | .result.verdict)}'
expect C3 "k-stale, whose only result was recorded against earlier words, reads unproven, and the read says the words moved" \
  "$(jq -cn --arg r "$result_stale" '{moved: true, proved: false, results: [$r], unproven: [{reason: "check_words_moved", result: $r}]}')" \
  --arg k "$k_stale" '(.checks[] | select(.id == $k)) as $c | {proved: $c.proved, results: $c.results,
    unproven: [$c.unproven[] | select(.reason == "check_words_moved")], moved: (.results[] | select(.result.id == $c.results[0]) | .result.check_words_moved)}'
stale_now="$(jq -c --arg k "$k_stale" '.checks[] | select(.id == $k) | {version, sha256}' "$work/chain.json")"
query C3 "read the version and the text sum check_results keeps beside the result of k-stale" \
  "select json_build_object('version', check_version, 'sha256', check_sha256) from check_results where id = '$result_stale'"
jq -e --argjson now "$stale_now" '.version < $now.version and .sha256 != $now.sha256' <<<"$answer" >/dev/null ||
  fail C3 "the store keeps the result of k-stale against earlier words than the check holds now" "the result holds $answer, the check $stale_now"
pass C3 "the store keeps the result of k-stale against version $(jq -r '.version' <<<"$answer") of its words, and the check stands at $(jq -c '.' <<<"$stale_now")"
expect C3 "k-pass, whose pass stands at $commit, reads proved, and so does r-one, which it serves" \
  '{"check":true,"requirement":true,"unproven":[]}' \
  --arg k "$k_pass" --arg r "$r_one" '{check: (.checks[] | select(.id == $k) | .proved), unproven: (.checks[] | select(.id == $k) | .unproven),
    requirement: (.requirements[] | select(.id == $r) | .proved)}'

# --- C4: the three-way check, one node, one result counted once, and a retraction ----------------

expect C4 "k-three is one node with one id, in the chain of r-one, r-two and r-three alike" \
  "$(jq -cn --arg k "$k_three" --arg a "$r_one" --arg b "$r_two" --arg c "$r_three" '{entries: 1, in: ([$a, $b, $c] | sort), serves: ([$a, $b, $c] | sort)}')" \
  --arg k "$k_three" '{entries: ([.checks[] | select(.id == $k)] | length), serves: (.checks[] | select(.id == $k) | .serves | sort),
    in: ([.requirements[] | select(.checks | index($k)) | .id] | sort)}'

send POST "/api/v1/issues/$key/results" "$(jq -cn --arg c "$commit" --argjson a "$(actor)" \
  '{check: "k-three", role: "tester", verdict: "pass", commit: $c, actor: $a,
    negative_control: "A page that drew a decision with no reason beside it; the check read the row and failed."}')"
want_status 201 C4 "record a pass of k-three at $commit"
recorded_id="$(jq -er '.result.id' "$work/body")" || fail C4 "record a pass of k-three" "no result id: $(body)"
recorded="$recorded_id"
readonly recorded
pass C4 "recorded the pass $recorded of k-three at $commit"
chain C4 "read the chain of $key at $commit after the pass" "$key" "$commit"
shape C4
expect C4 "the pass is counted once: k-three names it once, the chain holds it once, and it walks back to the three requirements" \
  "$(jq -cn --arg a "$r_one" --arg b "$r_two" --arg c "$r_three" '{in_chain: 1, in_check: 1, proves: true, requirements: ([$a, $b, $c] | sort)}')" \
  --arg k "$k_three" --arg r "$recorded" '{in_check: ([.checks[] | select(.id == $k) | .results[] | select(. == $r)] | length),
    in_chain: ([.results[] | select(.result.id == $r)] | length),
    requirements: (.results[] | select(.result.id == $r) | .requirements | sort), proves: (.results[] | select(.result.id == $r) | .proves)}'
expect C4 "k-three reads proved, and so do r-two and r-three, which it serves" '{"check":true,"r_three":true,"r_two":true}' \
  --arg k "$k_three" --arg b "$r_two" --arg c "$r_three" '{check: (.checks[] | select(.id == $k) | .proved),
    r_two: (.requirements[] | select(.id == $b) | .proved), r_three: (.requirements[] | select(.id == $c) | .proved)}'

send POST "/api/v1/results/$recorded/retract" "$(jq -cn --argjson a "$(actor)" \
  '{reason: "chain.sh retracts the pass it recorded, to read a retracted result count as nothing.", actor: $a}')"
want_status 201 C4 "retract the pass $recorded"
retracted=1
pass C4 "retracted the pass $recorded"
chain C4 "read the chain of $key at $commit after the retraction" "$key" "$commit"
shape C4
expect C4 "k-three, whose passes are all retracted, reads unproven, the read naming $recorded retracted" \
  "$(jq -cn --arg r "$recorded" '{proved: false, retracted: true, unproven: [{reason: "retracted", result: $r}]}')" \
  --arg k "$k_three" --arg r "$recorded" '{proved: (.checks[] | select(.id == $k) | .proved),
    unproven: [.checks[] | select(.id == $k) | .unproven[] | select(.result == $r)],
    retracted: (.results[] | select(.result.id == $r) | .result.retracted != null)}'
expect C4 "r-two and r-three read unproven again, and r-one stays proved by k-pass" '{"r_one":true,"r_three":false,"r_two":false}' \
  --arg a "$r_one" --arg b "$r_two" --arg c "$r_three" '{r_one: (.requirements[] | select(.id == $a) | .proved),
    r_two: (.requirements[] | select(.id == $b) | .proved), r_three: (.requirements[] | select(.id == $c) | .proved)}'

# --- C5: the authoritative tuples, read with no index rebuilt -------------------------------------

send POST /api/v1/issues "$(jq -cn --arg p "$project" --argjson a "$(actor)" '{project: $p, title: "chain.sh: the chain read over moved tuples",
  force: true, actor: $a, spec: ":::requirement{#r-x}\nThe first requirement of the run.\n:::\n\n:::requirement{#r-y}\nThe second requirement of the run.\n:::\n"}')"
want_status 201 C5 "create an issue of the run's own"
own="$(jq -er '.key' "$work/body")" || fail C5 "create an issue of the run's own" "no key: $(body)"
readonly own
send POST "/api/v1/issues/$own/artifacts" "$(jq -cn --arg k "$own" --argjson a "$(actor)" '{name: "verification.md", actor: $a,
  content: ":::check{#k-x serves=\"dispatch://\($k)/spec#b-r-x\"}\nThe first check of the run.\n:::\n\n:::check{#k-y serves=\"dispatch://\($k)/spec#b-r-y\"}\nThe second check of the run.\n:::\n"}')"
want_status 201 C5 "write the verification document of $own"
chain C5 "read the chain of $own" "$own"
own_rx="$(id_of r-x)" own_ry="$(id_of r-y)" own_kx="$(id_of k-x)" own_ky="$(id_of k-y)"
readonly own_rx own_ry own_kx own_ky
expect C5 "$own's chain holds k-x serving r-x and k-y serving r-y, and no requirement lacks a check" \
  "$(jq -cn --arg rx "$own_rx" --arg ry "$own_ry" --arg kx "$own_kx" --arg ky "$own_ky" '{no_check: [], rx: [$kx], ry: [$ky]}')" \
  --arg rx "$own_rx" --arg ry "$own_ry" '{rx: (.requirements[] | select(.id == $rx) | .checks), ry: (.requirements[] | select(.id == $ry) | .checks),
    no_check: [.gaps[] | select(.kind == "no_check") | .node]}'

retype_serves C5 "retract k-x's serves tuple" "$own" verification-md k-x '[]' ||
  fail C5 "retract the serves tuple of k-x" "HTTP $status: $(body)"
pass C5 "retracted the serves tuple of k-x, the document's edit and nothing else"
current_serves C5 "read k-x's serves tuples from the store" "$own_kx"
[[ -z "$answer" && "$rows" -ge 2 ]] ||
  fail C5 "the store holds k-x's serves tuple retracted" "it asserts '$answer' over $rows rows"
pass C5 "the store holds k-x's serves tuple retracted: none current over $rows rows"
chain C5 "read the chain of $own right after the retraction, no index rebuilt" "$own"
expect C5 "k-x reads serving nothing, and r-x, which it left, reads as a gap" \
  "$(jq -cn --arg kx "$own_kx" --arg rx "$own_rx" '{gaps: ([["no_check", $rx], ["serves_nothing", $kx]] | sort), rx: [], serves: []}')" \
  --arg kx "$own_kx" --arg rx "$own_rx" '{serves: (.checks[] | select(.id == $kx) | .serves), rx: (.requirements[] | select(.id == $rx) | .checks),
    gaps: ([.gaps[] | select((.kind == "no_check" and .node == $rx) or (.kind == "serves_nothing" and .node == $kx)) | [.kind, .node]] | sort)}'

own_ry_ref="dispatch://$own/spec#b-r-y"
retype_serves C5 "append a serves tuple from k-x to r-y" "$own" verification-md k-x "$(jq -cn --arg r "$own_ry_ref" '[$r]')" ||
  fail C5 "append a serves tuple from k-x to r-y" "HTTP $status: $(body)"
pass C5 "appended a serves tuple from k-x to r-y"
current_serves C5 "read k-x's serves tuples from the store" "$own_kx"
[[ "$answer" == "$own_ry" ]] || fail C5 "the store asserts k-x's serves tuple to r-y alone" "it asserts '$answer' over $rows rows"
pass C5 "the store asserts k-x's serves tuple to r-y alone, over $rows rows"
chain C5 "read the chain of $own right after the append, no index rebuilt" "$own"
readonly moved_filter='{kx: (.checks[] | select(.id == $kx) | .serves), ry: (.requirements[] | select(.id == $ry) | .checks | sort),
  rx_gap: ([.gaps[] | select(.kind == "no_check" and .node == $rx)] | length)}'
want_moved="$(jq -cn --arg kx "$own_kx" --arg ky "$own_ky" --arg ry "$own_ry" '{kx: [$ry], rx_gap: 1, ry: ([$kx, $ky] | sort)}')"
readonly want_moved
expect C5 "the chain changes with the tuple: k-x serves r-y, r-y names k-x beside k-y, and r-x stays a gap" "$want_moved" \
  --arg kx "$own_kx" --arg rx "$own_rx" --arg ry "$own_ry" "$moved_filter"

query C5 "read when the server's block-index pass last ran" "select boundary from duty_passes where lease = 'block-index'"
readonly boundary_before="$answer"
[[ -n "$boundary_before" ]] || fail C5 "the server records its block-index pass" "duty_passes holds no row of lease block-index"
for _ in $(seq 1 60); do
  query C5 "read when the server's block-index pass last ran" "select boundary > '$boundary_before' from duty_passes where lease = 'block-index'"
  if [[ "$answer" == t ]]; then
    break
  fi
  sleep 2
done
[[ "$answer" == t ]] || fail C5 "the server's block-index pass runs again" "its boundary stayed $boundary_before for two minutes"
pass C5 "the server's block-index pass ran again after the two writes (it last ran at $boundary_before before)"
chain C5 "read the chain of $own after the block-index pass, the weaker control" "$own"
expect C5 "after the block-index pass the chain answers the same, as it did with none" "$want_moved" \
  --arg kx "$own_kx" --arg rx "$own_rx" --arg ry "$own_ry" "$moved_filter"

# --- C6: placement and coverage apart --------------------------------------------------------------

chain C6 "read the chain of $key at $commit" "$key" "$commit"
expect C6 "k-three's parent is one node, a requirement it serves, and its three requirements sit under two criteria" \
  '{"criteria":2,"parent":{"kind":"block","supertag":"requirement"},"under_parent":true}' \
  --arg k "$k_three" '(.checks[] | select(.id == $k)) as $c | {parent: ($c.parent | {kind, supertag}),
    under_parent: ($c.serves | index($c.parent.id) != null),
    criteria: ([.requirements[] | select(.id as $r | $c.serves | index($r)) | .parent.id] | unique | length)}'
k_three_parent="$(jq -c --arg k "$k_three" '.checks[] | select(.id == $k) | .parent' "$work/chain.json")"
readonly k_three_parent
send GET "/api/v1/issues/$key/tree"
want_status 200 C6 "read the tree of $key"
expect C6 "the tree of $key holds k-three once, under that one parent, and no second copy of it" \
  "$(jq -cn --argjson p "$k_three_parent" '{copies: 1, parents: [$p.id]}')" \
  --arg k "$k_three" '{copies: ([.nodes[] | select(.id == $k)] | length), parents: [.nodes[] | select(.id == $k) | .parent.id]}'

send GET "/api/v1/issues/$key/artifacts/verification-md/blocks"
want_status 200 C6 "read the blocks of $key's verification document"
restore="$(jq -c '[.[] | select(.id == "k-three") | .attributes[] | select(.name == "serves") | .value | split(" ")[]]' "$work/body")"
[[ "$(jq 'length' <<<"$restore")" == 3 ]] || fail C6 "k-three names three requirements in its serves" "it names $restore"
retargeted="$(jq -c --arg from "dispatch://$key/spec#b-r-three" --arg to "dispatch://$key/spec#b-r-said" 'map(if . == $from then $to else . end)' <<<"$restore")"
restore_serves="$restore"
retype_serves C6 "retarget k-three's serves tuple from r-three to r-said" "$key" verification-md k-three "$retargeted" ||
  fail C6 "retarget k-three's serves tuple from r-three to r-said" "HTTP $status: $(body)"
pass C6 "retargeted k-three's serves tuple from r-three to r-said"
chain C6 "read the chain of $key after the retarget" "$key" "$commit"
expect C6 "the retarget moved the coverage and left the parent: k-three serves r-said in r-three's place, under the parent it had" \
  "$(jq -cnS --arg a "$r_one" --arg b "$r_two" --arg c "$r_said" --argjson p "$k_three_parent" '{parent: $p, r_said_gap: 0, serves: ([$a, $b, $c] | sort)}')" \
  --arg k "$k_three" --arg c "$r_said" '{serves: (.checks[] | select(.id == $k) | .serves | sort), parent: (.checks[] | select(.id == $k) | .parent),
    r_said_gap: ([.gaps[] | select(.kind == "no_check" and .node == $c)] | length)}'
retype_serves C6 "put back k-three's serves tuple to r-three" "$key" verification-md k-three "$restore" ||
  fail C6 "put back k-three's serves tuple to r-three" "HTTP $status: $(body)"
restore_serves=""
current_serves C6 "read k-three's serves tuples from the store" "$k_three"
[[ "$answer" == "$(jq -rn --arg a "$r_one" --arg b "$r_two" --arg c "$r_three" '[$a, $b, $c] | sort | join(",")')" ]] ||
  fail C6 "k-three serves r-one, r-two and r-three again" "the store asserts $answer"
pass C6 "k-three serves r-one, r-two and r-three again, as the corpus seeded it"
chain C6 "read the chain of $key after the serves tuple is back" "$key" "$commit"

expect C6 "t-none, the plan task that serves nothing, hangs from the issue and reads serving nothing" \
  "$(jq -cn --arg k "$key" --arg t "$t_none" '{gap: [["serves_nothing", $t]], parent: {id: $k, kind: "issue"}, serves: []}')" \
  --arg t "$t_none" '{parent: (.plan_tasks[] | select(.id == $t) | .parent | {kind, id}), serves: (.plan_tasks[] | select(.id == $t) | .serves),
    gap: [.gaps[] | select(.node == $t) | [.kind, .node]]}'
expect C6 "no check is the parent of a requirement: of the $(jq '.requirements | length' "$work/chain.json") requirements read, each stands under a criterion" \
  '{"checks":0,"criteria":true}' '{checks: ([.requirements[] | select(.parent.supertag == "check")] | length),
    criteria: ((.requirements | length) > 0 and all(.requirements[]; .parent.supertag == "criterion"))}'

send POST "/api/v1/issues/$key/nodes/$(q "$k_three")/parent" "$(jq -cn --arg p "$r_two" --argjson a "$(actor)" '{parent: $p, actor: $a}')"
[[ "$status" == 200 ]] ||
  fail C6 "move k-three under r-two, another requirement it serves, through POST /api/v1/issues/{key}/nodes/{id}/parent" \
    "HTTP $status: $(body)"
chain C6 "read the chain of $key after the move" "$key" "$commit"
expect C6 "the move changed the place and left the coverage: k-three hangs from r-two and serves the three it served" \
  "$(jq -cn --arg a "$r_one" --arg b "$r_two" --arg c "$r_three" '{parent: $b, serves: ([$a, $b, $c] | sort)}')" \
  --arg k "$k_three" '{parent: (.checks[] | select(.id == $k) | .parent.id), serves: (.checks[] | select(.id == $k) | .serves | sort)}'

# The corpus seeded k-three under r-one: the move is put back, so the next driver reads the
# placement the loader wrote.
send POST "/api/v1/issues/$key/nodes/$(q "$k_three")/parent" \
  "$(jq -cn --arg p "$(jq -r '.id' <<<"$k_three_parent")" --argjson a "$(actor)" '{parent: $p, actor: $a}')"
want_status 200 C6 "move k-three back under the parent the corpus gave it"
pass C6 "moved k-three back under $(jq -r '.id' <<<"$k_three_parent")"

pass "$driver" complete
