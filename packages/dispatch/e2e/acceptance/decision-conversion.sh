#!/usr/bin/env bash
# The check c-decisions-converted: an upgrade of a database the previous release wrote loses no
# answered ask, no historical version of one and no citation's resolution, and ends every section
# of every old decisions.md in the state the check's inventory gives it.
#
# The run gives the driver an empty database of its own as DATABASE_URL, the baseline commit as
# BASELINE_SHA and the commit of the build that converts as CONVERTER_SHA, and the driver boots
# three builds over that one database, in order, each through lib-baseline.sh, which reads no
# server started before it:
#
#   1. The baseline. lib-baseline.sh's baseline_start builds BASELINE_SHA stamped with that commit
#      (or boots BASELINE_BINARY), and the driver reads its /healthz naming that commit. Through
#      that server's own API alone - the agent bearer DISPATCH_TOKEN and the person DEV_SIGNIN_LOGIN
#      signed in at its dev sign-in route - it writes two projects, PROJECT, whose gated steps hold
#      decisions beside criteria and spec, and a project of its own whose list holds decisions
#      alone; T1's two captures, e2e/fixtures/acme-1042.json in PROJECT and acme-1115.json in the
#      other, each replayed as the issue, its spec, every ask of the capture and its decisions.md;
#      and two issues of its own in PROJECT, own-carried and own-superseded. So every row is the
#      old writer's.
#
#      A capture is replayed in the order its rows were written live. The spec is first written
#      with the capture's markdown and, under `## Earlier questions`, the ask block of every
#      captured ask whose block the capture's spec no longer holds; each ask of the capture is then
#      opened (a block ask by its block, a question of the issue through the issue's ask route, an
#      approval through the approval-request route, each at a version of the spec of its own) and
#      left open, answered by the person with the capture's options and words, or resolved, in the
#      capture's order; then the agent writes the capture's spec, which leaves out every earlier
#      block - so each answered one is an answered ask whose block no document holds, the first
#      shape of R5's restore - and uploads the capture's decisions.md. The capture's issue key and
#      each captured ask id are written as the replay's own, in every text the replay writes.
#
#      own-carried holds what the captures do not: two asks the person answers with the same word
#      "Yes" for two questions (q-nightly, whose wording the agent edits first, so the ask has a
#      historical version, and q-weekend); a decisions.md whose sections are a plain answer of
#      q-weekend, a section carrying reasons no ask holds that cites q-nightly, two sections whose
#      body is that same word for two questions of their own and cite no ask, a section holding
#      the ask block q-doc, which the person answers - the second shape of R5's restore - a section
#      that cites no ask, and one under `## Replaced`; the agent's removal of q-nightly's block from
#      the spec; a notes.md and two comments citing the old addresses of q-doc and q-nightly, and a
#      comment citing a section; and last the person's approval of the spec's newest version, which
#      the conversion's one version must carry: it restores two ask blocks under the `## Decisions`
#      heading it adds, writes decision blocks and a reading, and takes the answer's attributes off
#      q-weekend. own-superseded holds a spec the person approved before an agent changed the words
#      of its one block, and a decisions.md of one section, so the conversion's version changes the
#      text of a block relative to the approval.
#
#   2. Before the baseline stops, the driver captures, straight from its database, the inventory
#      the check names: each ask's id, kind, state, question, options, answer, answerer and author;
#      each historical version of each ask (every row of ask_block_texts, its parts at each
#      revision); every document version of the four issues; and every address that a citation of
#      an ask or of an ask's block names (refs, block_refs, ask_block_refs, comment_block_refs and
#      message_block_refs), with what the baseline's reference read resolves it to. It reads each
#      decisions.md section, its heading block and its links through the baseline's Decisions read
#      and joins them with decision-conversion-sections.txt, the expected state and source ask of
#      every section, refusing a section the file does not name and a line no section answers. It
#      reads the two projects' gated steps, each spec's newest review, and what the baseline's
#      search answers for the words of q-doc.
#
#   3. It stops the baseline and boots a build of CONVERTER_SHA, which lib-baseline.sh's
#      baseline_build builds from that commit's tree (git archive) and stamps with it as it builds
#      the baseline, with no DISPATCH_TEST_HOLD_DECISIONS_STEP, once no server process of the
#      database is live; it reads its /healthz naming that commit, and polls /healthz every 2 s,
#      printing each reading, until decisions_step_owed reads 0, failing after 900 s. It prints
#      every line the step logged, reads the two gated-steps rows the step rewrote straight from
#      the database, and stops that build.
#
#   4. It boots this head's server, the envoy-dispatch on PATH (HEAD_BINARY in its place), over the
#      converted database, waits until no document owes a settlement or a block index, and asserts
#      every sentence of the check against what this head answers: the inventory, row by row,
#      printing every row that differs; each section's state and source, printing every section
#      that ends otherwise and failing on them after the reads below; the retired document; the
#      check's link sentence, in two halves (the addresses section below): the reference read
#      resolving dispatch://KEY/decisions, the page URL /issues/KEY/decisions and
#      dispatch://KEY/artifact/decisions-md each to the issue node, live, and the reference route
#      of the web app (web/src/features/refs/routes.ts, run with bun) giving the first two the link
#      /issues/KEY/decisions; the census of issues not yet converted (`envoy-dispatch census
#      --store`) at zero; R20's three reads of a move - the block ids an issue's documents share,
#      read from document_blocks beside a planted share the same read finds, each moved address
#      resolving, and the search that finds the moved words
#      in the spec and not in the retired document; both shapes of R5's restore; the carried and the
#      superseded approvals, with the step's own report of each; and the two lists of gated steps.
#
# Each passing check prints `PASS: <section> <what>`; the first failing one prints
# `FAIL: <section> <what>: <observed>` on stderr and the run exits 1, after printing every row of
# the inventory, and every section, that differs; a missing or bad input exits 2. It prints no SKIP
# line. Every check is
# `cmd || fail …`, never part of an `&&` list: under `set -e` a failing command anywhere but last in
# such a list does not stop the script.

# shellcheck disable=SC2016 # the single-quoted jq filters name jq variables, not shell ones
set -euo pipefail
set +x

usage() {
  cat >&2 <<'USAGE'
Usage: BASELINE_SHA=<the baseline commit> CONVERTER_SHA=<the commit of the build that converts> \
         DATABASE_URL=${LEGION_TEST_PG_DSN%/*}/infra_1098_decisions?sslmode=disable \
         DISPATCH_TOKEN=e2e-token PROJECT=CORE DEV_SIGNIN_LOGIN=alice \
         [BASELINE_BINARY=<a build of BASELINE_SHA>] \
         [HEAD_BINARY=<this head's envoy-dispatch>] bash e2e/acceptance/decision-conversion.sh
       (run from packages/dispatch. DATABASE_URL names an empty database on this machine, which
       the run created and drops: every server here migrates and writes it. BASELINE_SHA,
       CONVERTER_SHA and DATABASE_URL are required and have no default. lib-baseline.sh builds
       each of the two commits from its own tree and stamps it, unless BASELINE_BINARY names the
       baseline's build; HEAD_BINARY defaults to the envoy-dispatch on PATH, the Verification
       preamble's build of this head.
       PROJECT is created here, with a second project of the driver's own)
USAGE
}

# shellcheck source-path=SCRIPTDIR source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
# shellcheck source-path=SCRIPTDIR source=lib-baseline.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-baseline.sh"

readonly driver=decision-conversion.sh
require_env BASELINE_SHA CONVERTER_SHA DATABASE_URL DISPATCH_TOKEN PROJECT DEV_SIGNIN_LOGIN TMPDIR
require_commands awk base64 bun cmp curl date git go jq psql python3 sed sha256sum sort tar tr
# Not readonly: lib-baseline.sh's baseline_boot takes a local of this name.
# shellcheck disable=SC2031 # the run's own DATABASE_URL: only a server's subshell sets another
database_url="$DATABASE_URL"
loopback_database_url "$database_url" || {
  printf 'DATABASE_URL names no database on this machine: every server here migrates it and the driver writes rows of it\n' >&2
  usage
  exit 2
}
# The servers get no pair the caller exported for another driver: the build of CONVERTER_SHA must
# run its step unheld, and a DISPATCH_TEST_HOLD_DECISIONS_STEP pair would hold it.
unset BASELINE_EXTRA_ENV
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly script_dir
checkout="$(git -C "$script_dir" rev-parse --show-toplevel)" || {
  printf 'cannot find the checkout %s is part of\n' "$script_dir" >&2
  exit 2
}
readonly checkout
converter_commit="$(git -C "$checkout" rev-parse --verify --quiet "${CONVERTER_SHA}^{commit}")" || {
  printf 'CONVERTER_SHA %s names no commit of %s\n' "$CONVERTER_SHA" "$checkout" >&2
  usage
  exit 2
}
readonly converter_commit
head_binary="${HEAD_BINARY:-$(command -v envoy-dispatch || true)}"
[[ -n "$head_binary" && -f "$head_binary" && -x "$head_binary" ]] || {
  printf 'no envoy-dispatch on PATH, and HEAD_BINARY names none: build this head as the Verification preamble does\n' >&2
  exit 2
}
[[ "$head_binary" == /* ]] || head_binary="$PWD/$head_binary"
readonly head_binary
readonly fixtures_dir="$script_dir/../fixtures"
readonly sections_file="$script_dir/decision-conversion-sections.txt"
readonly web_routes="$script_dir/../../web/src/features/refs/routes.ts"
for file in "$fixtures_dir/acme-1042.json" "$fixtures_dir/acme-1115.json" "$sections_file" "$web_routes"; do
  [[ -r "$file" ]] || {
    printf '%s cannot be read\n' "$file" >&2
    exit 2
  }
done
# shellcheck disable=SC2153 # the run's PROJECT, which require_env checked above
readonly project="$PROJECT"
readonly login="$DEV_SIGNIN_LOGIN"
# The project whose gated steps hold decisions alone: the driver's own, named from PROJECT so that
# it is never PROJECT itself.
readonly alone_project="X${project:0:9}"
[[ "$alone_project" != "$project" ]] || {
  printf 'PROJECT %s leaves the driver no key of its own for a second project\n' "$project" >&2
  exit 2
}
session="decision-conversion-$(date +%s)-$$"
readonly session
# The eight server-owned attributes of an ask block: an upload carries none of them, and of the
# ones a stored version keeps for one release (state, answered_by, answered_at) none is the answer.
readonly server_attributes='state|answered_by|answered_at|selected|answer|invalid|copied_from_document|copied_from'
# How long the build of CONVERTER_SHA gets to bring decisions_step_owed to zero, and the window
# after which a server process that wrote no heartbeat is no longer live (processes.LiveWindow).
readonly owed_limit=900
readonly live_window=15

work_dir
mkdir "$work/capture" "$work/head" "$work/home"
converter_pid=""
head_pid=""
cleanup() {
  local code=$?
  baseline_stop_pid "$converter_pid"
  baseline_stop_pid "$head_pid"
  baseline_stop
  rm -rf "$work"
  exit "$code"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# ---------------------------------------------------------------------------------------------
# Helpers.
# ---------------------------------------------------------------------------------------------
# field SECTION WHAT JQ: one value of the last answer's body, which must be there, in $value. It
# sets a variable rather than printing, since a fail inside `$(…)` would end only that subshell.
field() {
  value="$(jq -er "$3" "$work/body" 2>/dev/null)" || fail "$1" "$2" "no $3 in: $(body)"
}

# wait_zero SECTION WHAT SQL LIMIT: waits until SQL reads 0, polling every 0.2 s, for at most LIMIT
# seconds.
wait_zero() {
  local started=$SECONDS
  while true; do
    query "$1" "$2" "$3"
    [[ "$answer" != 0 ]] || return 0
    ((SECONDS - started < $4)) || fail "$1" "$2 within $4 s" "it still reads $answer"
    sleep 0.2
  done
}

# settled SECTION: waits until no document owes a settlement, so that a write after it - a
# review, an answer, a removal - names the version every earlier write left.
settled() {
  wait_zero "$1" "no document owes a settlement" 'select count(*) from doc_settlements_pending' 180
}

# no_live_process SECTION: waits until no server process of the database wrote a heartbeat within
# the live window, so that the next build boots alone: the step of CONVERTER_SHA converts nothing
# while a process of another build is live.
no_live_process() {
  wait_zero "$1" "no server process of the database is live" \
    "select count(*) from server_processes where heartbeat_at > now() - interval '$live_window seconds'" 60
}

# ask_row SECTION ISSUE DOCUMENT BLOCK: waits, within a minute, for the row the server settled the
# ask block BLOCK of DOCUMENT into, and leaves its id in $value.
ask_row() {
  local started=$SECONDS
  while true; do
    send GET "/api/v1/issues/$2/asks?state=all"
    want_status 200 "$1" "read the asks of $2"
    value="$(jq -r --arg b "$4" --arg d "$3" \
      '[.[] | select(.block_id == $b and .block_artifact.id == $d) | .id] | if length == 1 then .[0] else "" end' \
      "$work/body")" || value=""
    [[ -z "$value" ]] || return 0
    ((SECONDS - started < 60)) || fail "$1" "the ask of block $4 of $3" "no ask of $2 names it after 60 s"
    sleep 0.2
  done
}

# strip_attributes: stdin, a document's markdown, with every server-owned attribute taken off each
# ask block's opening line, as an agent's upload carries them: the server writes them back from
# each ask's row, so an upload that carried them would claim an answer no person gave yet.
strip_attributes() {
  sed -E "/^:+ask\\{/ s/ ($server_attributes)=\"[^\"]*\"//g"
}

# rewrite NAME: stdin, a text of capture NAME, with the capture's issue key written as the replay's
# key and each captured ask id the replay has opened written as the replay's id.
rewrite() {
  jq -Rrsj --arg from "${source_key[$1]}" --arg to "${key_of[$1]}" --argjson ids "$(ask_map "$1")" \
    'reduce ($ids | to_entries[]) as $id (.; gsub($id.key; $id.value)) | gsub("\\b" + $from + "\\b"; $to)'
}

# ask_map NAME: the captured ask ids of capture NAME the replay has opened, with the replay's id of
# each, as one JSON object.
ask_map() {
  local id
  for id in "${!replayed[@]}"; do
    if [[ "${replayed[$id]%%|*}" == "$1" ]]; then
      printf '%s\t%s\n' "$id" "${replayed[$id]#*|}"
    fi
  done | jq -Rn '[inputs | split("\t") | {(.[0]): .[1]}] | add // {}'
}

# create_issue SECTION NAME PROJECT TITLE SPEC_FILE: the agent creates an issue of PROJECT titled
# TITLE whose spec is SPEC_FILE's markdown, and keeps its key in key_of[NAME] and its spec's id in
# spec_of[NAME].
create_issue() {
  send POST /api/v1/issues "$(jq -cn --arg p "$3" --arg t "$4" --rawfile s "$5" --argjson a "$(actor)" \
    '{project: $p, title: $t, spec: $s, actor: $a}')"
  want_status 201 "$1" "create the issue $2"
  field "$1" "the key of $2" .key
  key_of["$2"]="$value"
  field "$1" "the spec of $2" .primary_artifact_id
  spec_of["$2"]="$value"
  issues+=("$2")
  printf 'issue %s is %s, its spec %s\n' "$2" "${key_of[$2]}" "${spec_of[$2]}"
}

# upload SECTION ISSUE NAME FILE: the agent uploads FILE as the document NAME of ISSUE (a key),
# replacing the document of that name where the issue holds one, and leaves its id in $value.
upload() {
  send POST "/api/v1/issues/$2/artifacts" "$(jq -cn --arg n "$3" --rawfile c "$4" --argjson a "$(actor)" \
    '{name: $n, content: $c, actor: $a}')"
  [[ "$status" == 200 || "$status" == 201 ]] || fail "$1" "upload $3 to $2" "HTTP $status: $(body)"
  field "$1" "the id of $3 of $2" '.artifact.id // .id'
}

# edit SECTION WHAT DOCUMENT OPS: the agent applies the edit ops OPS (a JSON list) to DOCUMENT.
edit() {
  send POST "/api/v1/artifacts/$3/edits" "$(jq -cn --argjson ops "$4" --argjson a "$(actor)" '{ops: $ops, actor: $a}')"
  want_status 200 "$1" "$2"
  jq -e '.changed == true' "$work/body" >/dev/null || fail "$1" "$2" "the edit changed nothing: $(body)"
}

# answer_ask SECTION ASK SELECTED TEXT: the person answers ASK with the option labels SELECTED (a
# JSON list) and the words TEXT ("" for none), naming the wording they read (the ask's edited_at,
# which the route requires of an ask whose wording changed), and waits until the answer's version
# is written.
answer_ask() {
  send GET "/api/v1/asks/$2"
  want_status 200 "$1" "read the ask $2 before its answer"
  send_person POST "/api/v1/asks/$2/answer" "$(jq -c --argjson s "$3" --arg t "$4" \
    '{selected: $s, text: (if $t == "" then null else $t end), expected_edited_at: .ask.edited_at}' "$work/body")"
  want_status 200 "$1" "$login answers the ask $2"
  settled "$1"
}

# resolve_ask SECTION ASK: the agent resolves ASK without an answer, as the capture's resolved asks
# were.
resolve_ask() {
  send POST "/api/v1/asks/$2/resolve" "$(jq -cn --argjson a "$(actor)" \
    '{kind: "resolved", reason: "Resolved before the capture, with no answer.", actor: $a}')"
  want_status 200 "$1" "resolve the ask $2"
  settled "$1"
}

# send_person METHOD PATH [JSON]: request as the person, whose cookie sign_in keeps.
send_person() { request "cookie:$login" "$@"; }

# latest_markdown SECTION DOCUMENT: the newest stored version of DOCUMENT, its number in $version
# and its markdown in $work/latest.md.
latest_markdown() {
  send GET "/api/v1/artifacts/$2"
  want_status 200 "$1" "read document $2"
  field "$1" "the newest version of $2" '[.versions[].number] | max'
  version="$value"
  send GET "/api/v1/artifacts/$2/versions/$version"
  want_status 200 "$1" "read version $version of $2"
  jq -j '.markdown' "$work/body" >"$work/latest.md"
}

# ask_block ID FILE: the markdown of the ask block ID in FILE, its fence and attributes included;
# nothing where FILE holds no such block.
ask_block() {
  awk -v id="$1" '
    !open && match($0, /^:+ask\{#/) {
      rest = substr($0, RLENGTH + 1)
      if (index(rest, id " ") == 1 || index(rest, id "}") == 1) {
        open = 1; fence = substr($0, 1, index($0, "ask{") - 1); print; next
      }
    }
    open { print; if ($0 == fence) exit }
  ' "$2"
}

# block_body FILE: the lines between the first line and the closing fence of the block in FILE,
# nested blocks left out, and the blank lines that end it: the question and the options a person
# reads.
block_body() {
  awk 'NR == 1 { next } { lines[NR] = $0; last = NR } END {
    depth = 0; kept = 0
    for (n = 2; n < last; n++) {
      line = lines[n]
      if (line ~ /^:::+[a-z]+\{/) { depth++; continue }
      if (depth > 0) { if (line ~ /^:::+$/) depth--; continue }
      out[++kept] = line
    }
    while (kept > 0 && out[kept] == "") kept--
    for (n = 1; n <= kept; n++) print out[n]
  }' "$1"
}

# expected_body ASK_FILE: the question and options of the ask ASK_FILE holds (an ask as its read
# answers it) as a block body: the question, and where it has options, a blank line and one
# `- label: description` line each.
expected_body() {
  jq -r '.question, (if (.options // []) | length > 0 then "", (.options[] | "- " + .label + (if (.description // "") == "" then "" else ": " + .description end)) else empty end)' "$1"
}

# dispatch_address ADDRESS: the dispatch:// reference of a stored node address - `KEY/spec#<id>`,
# `KEY/<slug>#<id>` or an ask's `ask:<id>` - as an author cites it.
dispatch_address() {
  local address="$1" document block
  if [[ "$address" == ask:* ]]; then
    printf 'dispatch://%s/ask/%s' "${ask_issue[${address#ask:}]}" "${address#ask:}"
    return
  fi
  document="${address%%#*}"
  block="${address#*#}"
  if [[ "$document" == */spec ]]; then
    printf 'dispatch://%s#b-%s' "$document" "$block"
  else
    printf 'dispatch://%s/artifact/%s#b-%s' "${document%%/*}" "${document#*/}" "$block"
  fi
}

# resolve SECTION ADDRESS: the reference read of the dispatch:// reference ADDRESS, as GET
# /api/v1/references?to= answers it, its status in $status and its node in $work/node.json (null
# on any status but 200).
resolve() {
  send GET "/api/v1/references?to=$(q "$2")"
  if [[ "$status" == 200 ]]; then
    jq -cS '.node' "$work/body" >"$work/node.json"
  else
    printf 'null\n' >"$work/node.json"
  fi
}

# moved_to_rows ADDRESS: every moved_to tuple from the block node ADDRESS, one `to_kind:to_id:asserted`
# a line, oldest first, in $answer.
moved_to_rows() {
  query moved_to "read the moved_to tuples of $1" \
    "select coalesce(string_agg(to_kind || ':' || to_id || ':' || asserted::text, ' ' order by seq), '') from graph_tuples
     where kind = 'moved_to' and from_kind = 'block' and from_id = '$1'"
}

# ask_block_address SECTION ASK: the address the block of ASK stands at now, `<ref_key>#<block id>`,
# as this head's ask read and the read of the document it names answer them, in $where; "" for an
# ask no block holds.
ask_block_address() {
  local document block
  send GET "/api/v1/asks/$2"
  want_status 200 "$1" "read the ask $2"
  document="$(jq -r '.ask.block_artifact.id // ""' "$work/body")"
  block="$(jq -r '.ask.block_id // ""' "$work/body")"
  where=""
  [[ -n "$document" && -n "$block" ]] || return 0
  send GET "/api/v1/artifacts/$document"
  want_status 200 "$1" "read the document the block of the ask $2 stands in"
  field "$1" "the ref_key of the document of the ask $2" .ref_key
  where="$value#$block"
}

# normalized: stdin with its whitespace collapsed to single spaces and trimmed.
normalized() { tr -s '[:space:]' ' ' | sed -E 's/^ //; s/ $//'; }

# boot_build SECTION NAME BINARY: boots BINARY over the database through lib-baseline.sh, once no
# server process is live, and reads the commit its /healthz names into $served.
boot_build() {
  no_live_process "$1"
  baseline_boot "$2" "$3" "$baseline_database_url"
  base="$baseline_boot_url"
  send GET /healthz
  want_status 200 "$1" "read the /healthz of $2"
  served="$(jq -r '.commit // ""' "$work/body")"
}

# ---------------------------------------------------------------------------------------------
# 1. The baseline writes the fixture.
# ---------------------------------------------------------------------------------------------
# The run's bearer, which baseline_start boots every server with; read before read_bearer would
# unset it, and printed nowhere.
baseline_start
unset DISPATCH_TOKEN DATABASE_URL
token="$baseline_token"
printf 'Authorization: Bearer %s\n' "$token" >"$work/auth"
base="$baseline_url"
send GET /healthz
want_status 200 baseline "read the baseline's /healthz"
served="$(jq -r '.commit // ""' "$work/body")"
if [[ -n "$baseline_commit" ]]; then
  [[ "$served" == "$baseline_commit" ]] ||
    fail baseline "the baseline serves the commit BASELINE_SHA names" "it serves '$served', and BASELINE_SHA is $baseline_commit"
  pass baseline "the baseline serves $served, the commit BASELINE_SHA names, built from its tree"
else
  pass baseline "the baseline is BASELINE_BINARY $baseline_binary, which serves '$served'"
fi
readonly baseline_served="$served"
sign_in baseline "$login" " at the baseline"

# The two projects, each with the person as the default assignee, whom every ask and approval
# request waits on, and their gated steps. The baseline's create takes no assignee, and its own
# route takes the person's.
declare -A gated_before=([$project]='["decisions","criteria","spec"]' [$alone_project]='["decisions"]')
declare -A gated_after=([$project]='["criteria","spec"]' [$alone_project]='["spec"]')
for key in "$project" "$alone_project"; do
  send POST /api/v1/projects "$(jq -cn --arg k "$key" --argjson a "$(actor)" \
    '{key: $k, name: ("Decisions converted: " + $k), actor: $a}')"
  want_status 201 baseline "create the project $key"
  send_person PUT "/api/v1/projects/$key/default-assignee" "$(jq -cn --arg p "$login" '{assignee: $p}')"
  want_status 200 baseline "make $login the default assignee of $key"
  send PUT "/api/v1/projects/$key/gated-steps" "$(jq -cn --argjson s "${gated_before[$key]}" --argjson a "$(actor)" \
    '{steps: $s, actor: $a}')"
  want_status 200 baseline "publish the gated steps of $key"
  expect baseline "the gated steps of $key hold ${gated_before[$key]}" "${gated_before[$key]}" '.steps'
done

# What the replay leaves: each issue by its name, its key, spec and decisions.md; each captured ask
# by its id, as `<capture>|<replay's id>`; each own ask by its name.
declare -A key_of=() spec_of=() decisions_of=() source_key=() replayed=() own_ask=() ask_issue=()
issues=()

# replay SECTION NAME PROJECT: replays the capture e2e/fixtures/NAME.json into PROJECT.
replay() {
  local section="$1" name="$2" into="$3" file="$fixtures_dir/$2.json" count index id kind state block spec_name
  local round=0
  source_key["$name"]="$(jq -r '.issue' "$file")"
  # The issue's spec starts as one line, since the capture's markdown cites the issue by its key,
  # which the replay knows once the issue exists.
  printf '# %s\n\nThe capture of %s, replayed.\n' "$(jq -r '.title' "$file")" "${source_key[$name]}" >"$work/$name.created.md"
  create_issue "$section" "$name" "$into" "$(jq -r '.title' "$file")" "$work/$name.created.md"
  send GET "/api/v1/artifacts/${spec_of[$name]}"
  want_status 200 "$section" "read the spec of $name"
  field "$section" "the name of the spec of $name" .name
  spec_name="$value"
  # The capture's spec, and every captured ask block it no longer holds under `## Earlier
  # questions`, closed by the paragraph each approval request's own version rewrites.
  jq -r '.documents.spec.markdown' "$file" | strip_attributes | rewrite "$name" >"$work/$name.spec.md"
  {
    cat "$work/$name.spec.md"
    printf '\n\n## Earlier questions\n\n'
    jq -r --rawfile spec "$work/$name.spec.md" '.asks[] | select(.blockId != null) | select(.blockId as $b | $spec | contains("{#" + $b) | not) |
      ":::ask{#" + .blockId + " urgency=\"med\"" + (if ((.answer.selected // []) | length) > 1 then " multiple=\"true\"" else "" end) + "}\n" +
      .question + (if (.options | length) > 0 then "\n\n" + ([.options[] | "- " + .label + (if (.description // "") != "" then ": " + .description else "" end)] | join("\n")) else "" end) +
      "\n:::\n"' "$file" | rewrite "$name"
    printf 'Approval round 0.\n'
  } >"$work/$name.earlier.md"
  upload "$section" "${key_of[$name]}" "$spec_name" "$work/$name.earlier.md"
  [[ "$value" == "${spec_of[$name]}" ]] || fail "$section" "the replay's spec of $name replaces its spec" "it wrote $value"
  settled "$section"
  count="$(jq '.asks | length' "$file")"
  for ((index = 0; index < count; index++)); do
    jq ".asks[$index]" "$file" >"$work/ask.json"
    id="$(jq -r '.id' "$work/ask.json")"
    kind="$(jq -r '.kind' "$work/ask.json")"
    state="$(jq -r '.state' "$work/ask.json")"
    block="$(jq -r '.blockId // ""' "$work/ask.json")"
    if [[ "$kind" == approval ]]; then
      # A request names the spec's newest version, and the server opens none over a version the
      # person approved already, so each request is made over a version of its own.
      round=$((round + 1))
      edit "$section" "the spec of $name takes the version approval request $round names" "${spec_of[$name]}" \
        "$(jq -cn --arg f "Approval round $((round - 1))." --arg w "Approval round $round." '[{op: "replace", find: $f, with: $w}]')"
      settled "$section"
      send POST "/api/v1/artifacts/${spec_of[$name]}/approval-requests" "$(jq -cn --argjson a "$(actor)" '{actor: $a}')"
      [[ "$status" == 200 || "$status" == 201 ]] || fail "$section" "request approval $round of the spec of $name" "HTTP $status: $(body)"
      field "$section" "the ask of approval request $round of $name" '.ask.id'
    elif [[ -n "$block" ]]; then
      ask_row "$section" "${key_of[$name]}" "${spec_of[$name]}" "$block"
    else
      send POST "/api/v1/issues/${key_of[$name]}/asks" "$(jq -c --arg q "$(jq -r '.question' "$work/ask.json" | rewrite "$name")" \
        --argjson a "$(actor)" '{question: $q, options: [.options[] | {label, description}], multiple: (((.answer.selected // []) | length) > 1), actor: $a}' "$work/ask.json")"
      want_status 201 "$section" "open the question $id of $name on its issue"
      field "$section" "the id of the question $id of $name" '.id // .ask.id'
    fi
    replayed["$id"]="$name|$value"
    ask_issue["$value"]="${key_of[$name]}"
    case "$state" in
      answered)
        answer_ask "$section" "$value" "$(jq -c '.answer.selected // []' "$work/ask.json")" \
          "$(jq -r '.answer.text // ""' "$work/ask.json" | rewrite "$name")"
        ;;
      resolved) resolve_ask "$section" "$value" ;;
      open) ;;
      *) fail "$section" "replay the ask $id of $name" "the capture holds it in the state $state, which no route writes" ;;
    esac
  done
  pass "$section" "replayed the $count asks of $name, ${source_key[$name]} as ${key_of[$name]}, in the capture's order and states"
  # The capture's spec, which leaves out every earlier block, and its decisions.md.
  jq -r '.documents.spec.markdown' "$file" | strip_attributes | rewrite "$name" >"$work/$name.spec.md"
  upload "$section" "${key_of[$name]}" "$spec_name" "$work/$name.spec.md"
  jq -r '.documents.decisions.markdown' "$file" | strip_attributes | rewrite "$name" >"$work/$name.decisions.md"
  upload "$section" "${key_of[$name]}" "$(jq -r '.documents.decisions.name' "$file")" "$work/$name.decisions.md"
  decisions_of["$name"]="$value"
  settled "$section"
  pass "$section" "the agent wrote the capture's spec of $name and its decisions.md ${decisions_of[$name]}"
}

replay replay acme-1042 "$project"
replay replay acme-1115 "$alone_project"

# own-carried.
cat >"$work/own-carried.spec.md" <<'MD'
# The census corpus

The census reads the corpus this issue seeds, and prints what it counts.

## Requirements

:::requirement{#r-store}
The census reads the seeded database and no other.
:::

:::requirement{#r-night}
The census runs while the database is quiet.
:::

:::requirement{#r-print}
The census prints plain counts, one line for each kind.
:::

## Questions

:::ask{#q-nightly urgency="med"}
Does the census run nightly?

- Yes: the database is quiet then.
- No: it runs when a person asks for it.
:::

:::ask{#q-weekend urgency="low"}
Does the census run on weekends too?

- Yes: every day of the week.
- No: on working days alone.
:::
MD
create_issue own own-carried "$project" "The census corpus" "$work/own-carried.spec.md"
readonly carried="${key_of[own-carried]}"
readonly carried_spec="${spec_of[own-carried]}"
ask_row own "$carried" "$carried_spec" q-nightly
own_ask[q-nightly]="$value"
ask_row own "$carried" "$carried_spec" q-weekend
own_ask[q-weekend]="$value"
send PATCH "/api/v1/asks/${own_ask[q-nightly]}" "$(jq -cn --argjson a "$(actor)" '{question: "Does the census run every night?", actor: $a}')"
want_status 200 own "the agent rewords q-nightly"
settled own
answer_ask own "${own_ask[q-nightly]}" '["Yes"]' "Yes"
answer_ask own "${own_ask[q-weekend]}" '["Yes"]' "Yes"
cat >"$work/own-carried.decisions.md" <<MD
# Decisions

## Weekends too: dispatch://$carried/ask/${own_ask[q-weekend]}

yes

## Why the census runs at night

Yes: the database is quiet at night, and no person waits on the counts before morning. Decided in dispatch://$carried/ask/${own_ask[q-nightly]}, and it shapes dispatch://$carried/spec#b-r-night.

## Does the census count closed issues

Yes

## Does the census count open asks

Yes

## Which store the census reads first

:::ask{#q-doc urgency="med"}
Which store does the census read first?

- The seeded database: the one the pod runs.
- A copy of production: which no pod reaches.
:::

## The census keeps no file

We write no file beside the database, which dispatch://$carried/spec#b-r-store depends on.

## Replaced

### The census once printed in colour

We first printed the counts in colour, and replaced that with plain counts, as dispatch://$carried/spec#b-r-print holds.
MD
upload own "$carried" decisions.md "$work/own-carried.decisions.md"
decisions_of[own-carried]="$value"
ask_row own "$carried" "${decisions_of[own-carried]}" q-doc
own_ask[q-doc]="$value"
answer_ask own "${own_ask[q-doc]}" '["The seeded database"]' "The seeded database, read once a night."
for name in q-nightly q-weekend q-doc; do
  ask_issue["${own_ask[$name]}"]="$carried"
done
edit own "the agent removes the answered q-nightly from the spec of $carried" "$carried_spec" \
  '[{"op":"delete","block":"q-nightly"}]'
settled own
send GET "/api/v1/issues/$carried/decisions"
want_status 200 own "read the Decisions of $carried at the baseline"
field own "the heading block of the section The census keeps no file" \
  '[.decisions[] | select(.heading == "The census keeps no file") | .block_id] | if length == 1 then .[0] else error end'
readonly file_heading="$value"
cat >"$work/own-carried.notes.md" <<MD
# Notes

The census reads the seeded database first, as dispatch://$carried/artifact/decisions-md#b-q-doc records.
MD
upload own "$carried" notes.md "$work/own-carried.notes.md"
for words in "Which store the census reads first is settled in dispatch://$carried/artifact/decisions-md#b-q-doc." \
  "Whether the census runs every night was asked in dispatch://$carried/spec#b-q-nightly." \
  "No file is kept, as dispatch://$carried/artifact/decisions-md#b-$file_heading says."; do
  send POST "/api/v1/issues/$carried/comments" "$(jq -cn --arg b "$words" --argjson a "$(actor)" '{body: $b, actor: $a}')"
  want_status 201 own "comment on $carried: $words"
done
settled own
send_person POST "/api/v1/artifacts/$carried_spec/reviews" '{"state":"approved"}'
want_status 201 own "$login approves the newest version of the spec of $carried"
settled own
pass own "own-carried is $carried: two asks answered Yes, the answered q-nightly gone from its spec, q-doc in its decisions.md, its newest spec approved"

# own-superseded.
cat >"$work/own-superseded.spec.md" <<'MD'
# The census schedule

:::requirement{#r-hour}
The census starts at two in the morning, after the backups end.
:::
MD
create_issue own own-superseded "$project" "The census schedule" "$work/own-superseded.spec.md"
readonly superseded="${key_of[own-superseded]}"
readonly superseded_spec="${spec_of[own-superseded]}"
cat >"$work/own-superseded.decisions.md" <<MD
# Decisions

## The census waits for the backups

The backups end at one in the morning, so the census starts after them. This shapes dispatch://$superseded/spec#b-r-hour.
MD
upload own "$superseded" decisions.md "$work/own-superseded.decisions.md"
decisions_of[own-superseded]="$value"
settled own
send_person POST "/api/v1/artifacts/$superseded_spec/reviews" '{"state":"approved"}'
want_status 201 own "$login approves the spec of $superseded"
settled own
edit own "an agent changes the words of r-hour in the spec of $superseded after the approval" "$superseded_spec" \
  '[{"op":"replace","find":"two in the morning","with":"three in the morning"}]'
settled own
pass own "own-superseded is $superseded: its spec approved, then the words of its block r-hour changed by an agent"

# ---------------------------------------------------------------------------------------------
# 2. The inventory, straight from the baseline's database, and the expected end of each section.
# ---------------------------------------------------------------------------------------------
issue_keys="$(for name in "${issues[@]}"; do printf "'%s'," "${key_of[$name]}"; done)"
readonly issue_keys="${issue_keys%,}"

# The asks: one canonical JSON object a line, by id.
asks_sql="select jsonb_build_object('id', a.id, 'issue', a.issue_key, 'kind', a.kind, 'state', a.state,
    'question', a.question, 'options', (select coalesce(jsonb_agg(jsonb_build_object('label', o->>'label', 'description', coalesce(o->>'description', ''))), '[]') from jsonb_array_elements(a.options) o),
    'multiple', a.multiple, 'author', jsonb_build_object('kind', a.author->>'kind', 'id', a.author->>'id'),
    'answer', case when a.answer is null then null else jsonb_build_object('text', a.answer->>'text', 'selected', coalesce(a.answer->'selected', '[]'), 'user', a.answer->>'user') end)::text
  from asks a where a.issue_key in ($issue_keys) order by a.id"
query capture "read the asks of the four issues" "$asks_sql"
printf '%s\n' "$answer" | jq -cS . >"$work/capture/asks.jsonl"
# The ask's own historical versions: each part of each ask at each indexed revision.
query capture "read the historical versions of the asks" \
  "select coalesce(string_agg(t.ask_id || ' ' || t.block_id || ' ' || t.revision || ' ' || coalesce(encode(sha256(convert_to(t.text, 'UTF8')), 'hex'), 'removed'), E'\n' order by t.ask_id, t.block_id, t.revision), '')
   from ask_block_texts t join asks a on a.id = t.ask_id where a.issue_key in ($issue_keys)"
printf '%s\n' "$answer" | sed '/^$/d' >"$work/capture/ask-versions.txt"
# Each part's text at each revision, by the same key, to compare with what this head answers.
query capture "read the texts of the asks' historical versions" \
  "select coalesce(jsonb_object_agg(t.ask_id || ' ' || t.block_id || ' ' || t.revision, t.text), '{}')::text
   from ask_block_texts t join asks a on a.id = t.ask_id where a.issue_key in ($issue_keys)"
printf '%s\n' "$answer" >"$work/capture/ask-version-texts.json"
# Every version of every document of the four issues.
query capture "read every document version of the four issues" \
  "select coalesce(string_agg(v.artifact_id || ' ' || v.number || ' ' || encode(sha256(convert_to(v.markdown, 'UTF8')), 'hex'), E'\n' order by v.artifact_id, v.number), '')
   from artifact_versions v join artifacts d on d.id = v.artifact_id where d.issue_key in ($issue_keys) and d.kind = 'doc'"
printf '%s\n' "$answer" | sed '/^$/d' >"$work/capture/versions.txt"
# Every address a citation names that is an ask of the four issues or an ask's block, with the
# table the citation is stored in.
query capture "read the citation tables" \
  "select string_agg(table_name, ' ' order by table_name) from information_schema.columns
   where table_schema = current_schema() and column_name = 'to_id'
     and table_name in ('refs', 'block_refs', 'ask_block_refs', 'comment_block_refs', 'message_block_refs')"
read -r -a citation_tables <<<"$answer"
[[ "${#citation_tables[@]}" -ge 2 ]] || fail capture "read the citation tables" "found only: $answer"
citations_sql="$(for table in "${citation_tables[@]}"; do
  printf "select '%s' as source, to_kind, to_id from %s union all " "$table" "$table"
done)"
query capture "read every citation of an ask or of an ask's block" \
  "with targets as (
     select 'ask:' || a.id::text as address, a.id::text as ask from asks a where a.issue_key in ($issue_keys)
     union all
     select d.ref_key || '#' || a.block_id, a.id::text from asks a join artifacts d on d.id = a.block_artifact_id
     where a.issue_key in ($issue_keys) and a.block_id is not null),
   cited as (${citations_sql% union all })
   select coalesce(string_agg(distinct t.address || ' ' || t.ask || ' ' || c.source, E'\n'), '')
   from cited c join targets t on t.address = case when c.to_kind = 'ask' then 'ask:' || c.to_id else c.to_id end"
printf '%s\n' "$answer" | sed '/^$/d' | sort >"$work/capture/citations.txt"
[[ -s "$work/capture/citations.txt" ]] || fail capture "read every citation of an ask or of an ask's block" "none is stored"
# What the baseline's reference read resolves each cited address to.
: >"$work/capture/resolved.txt"
while read -r address ask _; do
  grep -q "^$address " "$work/capture/resolved.txt" && continue
  resolve capture "$(dispatch_address "$address")"
  printf '%s %s %s %s\n' "$address" "$ask" "$status" "$(jq -c '{kind, id}' "$work/node.json" 2>/dev/null || printf 'null')" \
    >>"$work/capture/resolved.txt"
  if [[ "$status" == 200 && "$(jq -r '.kind' "$work/node.json")" == block ]]; then
    jq -r '.text' "$work/node.json" >"$work/capture/text.$(printf '%s' "$address" | sha256sum | cut -c1-16)"
  fi
done <"$work/capture/citations.txt"
pass capture "captured $(wc -l <"$work/capture/asks.jsonl") asks, $(wc -l <"$work/capture/ask-versions.txt") historical versions of their parts, $(wc -l <"$work/capture/versions.txt") document versions, and $(wc -l <"$work/capture/resolved.txt") cited addresses with what the baseline resolves each to"
printf 'captured address resolution: %s\n' "$(cut -d' ' -f1,3,4 "$work/capture/resolved.txt" | tr '\n' ';')"

# The asks of R5's two shapes, as the baseline's database holds them: an answered question whose
# row names its issue's spec and whose block that spec's index does not hold, and an ask whose block
# a decisions document holds.
query capture "read the asks the restore owes" \
  "select coalesce(string_agg(a.id || ' ' || i.key || ' ' || a.block_id || ' ' ||
     case when d.id = s.id then 'main' else 'second' end, E'\n' order by a.answer->>'at', a.id), '')
   from asks a join issues i on i.key = a.issue_key
   join artifacts s on s.issue_key = i.key and s.is_primary and s.kind = 'doc'
   join artifacts d on d.id = a.block_artifact_id
   where i.key in ($issue_keys) and a.block_id is not null and (
     (d.id = s.id and a.kind = 'question' and a.state = 'answered' and not exists (
       select 1 from document_blocks b where b.artifact_id = s.id and b.block_id = a.block_id and b.type = 'ask'))
     or (d.id <> s.id and (d.role = 'decisions' or d.slug = 'decisions-md')))"
printf '%s\n' "$answer" | sed '/^$/d' >"$work/capture/restores.txt"
grep -q ' main$' "$work/capture/restores.txt" || fail capture "the fixture holds an answered ask whose block no document holds" "$(<"$work/capture/restores.txt")"
grep -q ' second$' "$work/capture/restores.txt" || fail capture "the fixture holds an answered ask whose block a decisions.md holds" "$(<"$work/capture/restores.txt")"
while read -r id key block shape; do
  send GET "/api/v1/asks/$id"
  want_status 200 capture "read the ask $id at the baseline"
  jq '.ask' "$work/body" >"$work/capture/ask.$id.json"
  printf 'the restore owes %s: the ask %s of block %s, shape %s\n' "$key" "$id" "$block" "$shape"
done <"$work/capture/restores.txt"
pass capture "the restore owes $(grep -c ' main$' "$work/capture/restores.txt") asks of its first shape and $(grep -c ' second$' "$work/capture/restores.txt") of its second"

# Each section of each decisions.md, as the baseline's Decisions read answers it, joined with the
# check's inventory of expected ends.
: >"$work/capture/sections.tsv"
# A section's links are read as the conversion reads them: links to a block the issue's spec holds.
# An empty field is written "-", since read with IFS=tab would skip it and shift every field after.
for name in "${issues[@]}"; do
  send GET "/api/v1/artifacts/${spec_of[$name]}"
  want_status 200 capture "read the spec of $name"
  field capture "the ref_key of the spec of $name" .ref_key
  spec_ref="$value"
  query capture "read the blocks of the spec of $name" \
    "select coalesce(json_agg(block_id), '[]')::text from document_blocks where artifact_id = '${spec_of[$name]}'"
  spec_blocks="$answer"
  send GET "/api/v1/issues/${key_of[$name]}/decisions"
  want_status 200 capture "read the Decisions of ${key_of[$name]} at the baseline"
  jq -r --arg n "$name" --arg ref "$spec_ref" --argjson blocks "$spec_blocks" '
    def field: if . == "" then "-" else . end;
    .decisions[] | [$n, .heading, .block_id, (.text | @base64),
      ([.links[].to.id | select(startswith($ref + "#") and (ltrimstr($ref + "#") as $b | $blocks | index($b)))] | unique | join(",") | field),
      (.asks | join(",") | field)] | @tsv' "$work/body" >>"$work/capture/sections.tsv"
done
# The inventory's lines, each heading and source written with this run's key and ask ids.
own_map="$(for name in "${!own_ask[@]}"; do printf '%s\t%s\n' "$name" "${own_ask[$name]}"; done |
  jq -Rn '[inputs | split("\t") | {(.[0]): .[1]}] | add // {}')"
captured_map="$(for id in "${!replayed[@]}"; do printf '%s\t%s\n' "$id" "${replayed[$id]#*|}"; done |
  jq -Rn '[inputs | split("\t") | {(.[0]): .[1]}] | add // {}')"
: >"$work/capture/expected.tsv"
while IFS=$'\t' read -r name heading state source; do
  [[ -z "$name" || "$name" == \#* ]] && continue
  [[ -n "${key_of[$name]:-}" ]] || fail sections "the inventory names the issue $name" "the fixture holds no such issue"
  heading="$(jq -rn --arg h "$heading" --arg k "${key_of[$name]}" --argjson own "$own_map" \
    '$h | gsub("\\{key\\}"; $k) | gsub("\\{ask:(?<n>[a-z0-9-]+)\\}"; $own[.n] // ("unknown ask " + .n))')"
  case "$state" in
    decision | unsure | unplaced | replaced | reading | dropped) ;;
    *) fail sections "the inventory's state of \"$heading\"" "it names $state, no state the check knows" ;;
  esac
  if [[ "$source" != - ]]; then
    source="$(jq -rn --arg s "$source" --argjson own "$own_map" --argjson captured "$captured_map" \
      '[$s | split(",")[] | ($own[.] // $captured[.] // ("unknown ask " + .))] | join(",")')"
    [[ "$source" != *"unknown ask"* ]] || fail sections "the inventory's source of \"$heading\"" "$source"
  fi
  line="$(awk -F '\t' -v n="$name" -v h="$heading" '$1 == n && $2 == h' "$work/capture/sections.tsv")"
  [[ -n "$line" && "$(wc -l <<<"$line")" == 1 ]] ||
    fail sections "the inventory's section \"$heading\" of $name is one section the baseline's Decisions read answers" \
      "it answers: $(awk -F '\t' -v n="$name" '$1 == n { print $2 }' "$work/capture/sections.tsv" | tr '\n' '|')"
  printf '%s\t%s\t%s\n' "$line" "$state" "$source" >>"$work/capture/expected.tsv"
done <"$sections_file"
missing="$(awk -F '\t' 'NR == FNR { held[$1 "\t" $2] = 1; next } !(($1 "\t" $2) in held) { print $1 ": " $2 }' \
  "$work/capture/expected.tsv" "$work/capture/sections.tsv")"
[[ -z "$missing" ]] || fail sections "the inventory gives every section of every decisions.md its expected end" "it names none for: $missing"
while IFS=$'\t' read -r name heading block _ links cites state source; do
  printf 'expected: %s "%s" heading %s links [%s] cites [%s] -> %s %s\n' "$name" "$heading" "$block" "$links" "$cites" "$state" "$source"
done <"$work/capture/expected.tsv"
pass sections "the inventory gives each of the $(wc -l <"$work/capture/expected.tsv") sections of the four decisions.md its expected end and source ask"

# The gated steps, each spec's newest review, and what search finds of q-doc's words.
for key in "$project" "$alone_project"; do
  query capture "read the gated steps of $key" "select to_json(gated_steps)::text from projects where key = '$key'"
  [[ "$(jq -cS . <<<"$answer")" == "${gated_before[$key]}" ]] || fail capture "the baseline stored the gated steps of $key" "$answer"
done
declare -A approved_before=()
for name in "${issues[@]}"; do
  query capture "read the newest review of the spec of $name" \
    "select coalesce((select state || ' ' || version from artifact_reviews where artifact_id = '${spec_of[$name]}' order by created_at desc, id desc limit 1), 'none')"
  approved_before["$name"]="$answer"
  printf 'the newest review of the spec of %s: %s\n' "$name" "$answer"
done
[[ "${approved_before[own-carried]}" == "approved "* ]] || fail capture "the spec of own-carried is approved" "${approved_before[own-carried]}"
[[ "${approved_before[own-superseded]}" == "approved 1" ]] || fail capture "the spec of own-superseded is approved at version 1" "${approved_before[own-superseded]}"
readonly search_words="Which store does the census read first"
# search_documents SECTION: the documents GET /api/v1/search answers for search_words in PROJECT,
# each as `<issue key>/<slug>`, one a line, sorted, in $work/search.
search_documents() {
  send GET "/api/v1/search?q=$(q "$search_words")&project=$(q "$project")&limit=50"
  want_status 200 "$1" "search $project for \"$search_words\""
  jq -r '[.results[] | select(.artifact != null) | (.owner.key // .issue.key // "") + "/" + .artifact.slug] | unique | .[]' \
    "$work/body" >"$work/search"
}
send GET "/api/v1/artifacts/$carried_spec"
want_status 200 capture "read the spec of $carried"
field capture "the slug of the spec of $carried" .slug
readonly carried_spec_slug="$value"
search_documents capture
grep -qx "$carried/decisions-md" "$work/search" ||
  fail capture "the baseline's search finds q-doc's words in the decisions.md of $carried, which holds them" "it finds: $(tr '\n' ' ' <"$work/search")"
pass capture "the baseline's search finds \"$search_words\" in $carried/decisions-md, beside: $(tr '\n' ' ' <"$work/search")"
baseline_stop_server

# ---------------------------------------------------------------------------------------------
# 3. The build of CONVERTER_SHA migrates the rows and converts them.
# ---------------------------------------------------------------------------------------------
# lib-baseline.sh builds CONVERTER_SHA as it builds BASELINE_SHA: the commit's tree taken out with
# git archive, never a checkout, and the build stamped with the commit, which the process registers.
baseline_build converter "$converter_commit"
readonly converter_binary="$baseline_built_binary"
boot_build converter converter "$converter_binary"
converter_pid="$baseline_boot_pid"
readonly converter_log="$baseline_log"
[[ "$served" == "$converter_commit" ]] ||
  fail converter "the build of CONVERTER_SHA serves that commit" "it serves '$served', and CONVERTER_SHA is $converter_commit"
pass converter "the build of CONVERTER_SHA $converter_commit serves '$served' over the baseline's database, its step unheld"
started=$SECONDS
first_owed=""
while true; do
  send GET /healthz
  want_status 200 converter "read the /healthz of the build of CONVERTER_SHA"
  owed="$(jq -r '.decisions_step_owed // "absent"' "$work/body")"
  printf 'converter /healthz at %s s: decisions_step_owed=%s\n' "$((SECONDS - started))" "$owed"
  [[ -n "$first_owed" || "$owed" == absent ]] || first_owed="$owed"
  [[ "$owed" != 0 ]] || break
  ((SECONDS - started < owed_limit)) ||
    fail converter "/healthz answers decisions_step_owed 0 within $owed_limit s" \
      "it answers $owed; the step's last count: $(grep -F 'msg="dispatch: decisions step counted what it owes"' "$converter_log" | tail -n 1)"
  sleep 2
done
pass converter "/healthz of the build of CONVERTER_SHA answers decisions_step_owed 0 after $((SECONDS - started)) s, its first count $first_owed"
grep -F 'msg="dispatch: decisions step ' "$converter_log" >"$work/converter-report" || true
printf 'the step of the build of CONVERTER_SHA logged:\n'
sed 's/^/  /' "$work/converter-report"
for key in "$project" "$alone_project"; do
  query converter "read the gated steps of $key the step left" "select to_json(gated_steps)::text from projects where key = '$key'"
  printf 'the build of CONVERTER_SHA left the gated steps of %s stored as %s\n' "$key" "$answer"
done
baseline_stop_pid "$converter_pid"
converter_pid=""

# ---------------------------------------------------------------------------------------------
# 4. This head reads the converted database.
# ---------------------------------------------------------------------------------------------
boot_build head head "$head_binary"
head_pid="$baseline_boot_pid"
pass head "this head's server $head_binary serves '$served' over the converted database"
sign_in head "$login" " at this head"
query head "read whether document_block_versions holds store_owed_at" \
  "select count(*) from information_schema.columns where table_name = 'document_block_versions' and column_name = 'store_owed_at'"
owed_index="version is null"
[[ "$answer" == 0 ]] || owed_index="version is null or store_owed_at is not null"
wait_zero head "no document owes a settlement or its block index" \
  "select (select count(*) from doc_settlements_pending) + (select count(*) from document_block_versions where $owed_index)" 300

# -- The inventory, row by row --------------------------------------------------------------
differs=0
# differ WHAT: one row of the inventory this head answers otherwise, printed and counted.
differ() {
  printf 'DIFFERS: %s\n' "$1"
  differs=$((differs + 1))
}
while read -r captured; do
  id="$(jq -r '.id' <<<"$captured")"
  send GET "/api/v1/asks/$id"
  if [[ "$status" != 200 ]]; then
    differ "the ask $id: this head answers HTTP $status: $(body)"
    continue
  fi
  now="$(jq -cS '.ask | {id, issue: .issue_key, kind, state, question,
      options: [.options[] | {label, description: (.description // "")}], multiple,
      author: {kind: .author.kind, id: .author.id},
      answer: (if .answer == null then null else {text: .answer.text, selected: (.answer.selected // []), user: .answer.user} end)}' "$work/body")"
  [[ "$now" == "$captured" ]] || differ "the ask $id: captured $captured, this head answers $now"
done <"$work/capture/asks.jsonl"
pass inventory "read $(wc -l <"$work/capture/asks.jsonl") asks through this head: id, kind, state, question, options, answer, answerer and author"
while read -r id part revision digest; do
  resolve inventory "dispatch://${ask_issue[$id]}/ask/$id#b-$part@v$revision"
  if [[ "$status" != 200 ]]; then
    differ "the ask $id, part $part at revision $revision: this head answers HTTP $status: $(body)"
    continue
  fi
  if [[ "$digest" == removed ]]; then
    got="$(jq -r '.pin.state // "none"' "$work/node.json")"
    [[ "$got" == removed ]] || differ "the ask $id, part $part at revision $revision: captured removed, this head reads $got"
  else
    want="$(jq -r --arg k "$id $part $revision" '.[$k]' "$work/capture/ask-version-texts.json")"
    got="$(jq -r '.pin.text // ""' "$work/node.json")"
    [[ "$got" == "$want" ]] ||
      differ "the ask $id, part $part at revision $revision: captured $(jq -cn --arg t "$want" '$t'), this head answers $(jq -cn --arg t "$got" '$t')"
  fi
done <"$work/capture/ask-versions.txt"
pass inventory "read $(wc -l <"$work/capture/ask-versions.txt") historical versions of the asks' parts through this head's reference read, each pinned to its revision"
while read -r document number digest; do
  send GET "/api/v1/artifacts/$document/versions/$number"
  if [[ "$status" != 200 ]]; then
    differ "version $number of $document: this head answers HTTP $status: $(body)"
    continue
  fi
  got="$(jq -j '.markdown' "$work/body" | sha256sum | cut -d' ' -f1)"
  [[ "$got" == "$digest" ]] || differ "version $number of $document: captured sha256 $digest, this head answers $got"
done <"$work/capture/versions.txt"
pass inventory "read $(wc -l <"$work/capture/versions.txt") document versions through this head, each by its number"
while read -r address ask before node; do
  resolve inventory "$(dispatch_address "$address")"
  resolved_status="$status"
  ask_block_address inventory "$ask"
  if [[ "$address" == ask:* ]]; then
    [[ "$resolved_status" == 200 && "$(jq -r '.kind + ":" + .id' "$work/node.json")" == "ask:$ask" ]] ||
      differ "the citation $address: the baseline resolved it to $node, this head answers HTTP $resolved_status $(jq -c '{kind, id}' "$work/node.json")"
    continue
  fi
  got="$(jq -r 'if . == null then "none" else .kind + ":" + .id end' "$work/node.json")"
  if [[ "$got" != "block:$where" ]]; then
    differ "the citation $address of the ask $ask: the baseline resolved it ($before) to $node, this head resolves it to $got, not to the ask's block $where"
    continue
  fi
  if [[ "$before" == 200 ]]; then
    capture_text="$work/capture/text.$(printf '%s' "$address" | sha256sum | cut -c1-16)"
    jq -r '.text' "$work/node.json" >"$work/text.got"
    block_body "$capture_text" >"$work/body.want"
    block_body "$work/text.got" >"$work/body.got"
    cmp -s "$work/body.want" "$work/body.got" ||
      differ "the citation $address of the ask $ask resolves to $where with other words: captured $(<"$work/body.want"), this head $(<"$work/body.got")"
  fi
  printf 'citation %s: the baseline %s %s, this head %s\n' "$address" "$before" "$node" "$got"
done <"$work/capture/resolved.txt"
pass inventory "resolved $(wc -l <"$work/capture/resolved.txt") cited addresses through this head, each to its ask or to its ask's block where that block stands now, with the same question and options"
[[ "$differs" == 0 ]] || fail inventory "this head answers the inventory captured before the upgrade" "$differs rows differ, each printed above"
pass inventory "every ask, every historical version and every citation's resolution captured before the upgrade reads the same through this head"

# -- Each section's end -----------------------------------------------------------------------
declare -A entries_read=()
# entries ISSUE_NAME: this head's Decisions read of the issue, in $work/entries.<name>.json.
entries() {
  [[ -n "${entries_read[$1]:-}" ]] && return 0
  send GET "/api/v1/issues/${key_of[$1]}/decisions"
  want_status 200 rules "read the Decisions of ${key_of[$1]} at this head"
  cp "$work/body" "$work/entries.$1.json"
  entries_read["$1"]=1
}
declare -A readings_of=()
# Every section is read, and each one that ends otherwise than the inventory says is printed and
# counted, so a run names every such section; the run fails on them after its other reads.
section_differs=0
# differ_section WHAT OBSERVED: one section that ends otherwise than the inventory says.
differ_section() {
  printf 'DIFFERS: rules %s: %s\n' "$1" "$2"
  section_differs=$((section_differs + 1))
}
# current_tuples KIND FROM_KIND FROM_ID: the far ends, `to_kind:to_id`, of the asserted current
# tuples of KIND out of a node, sorted and joined with commas, in $answer.
current_tuples() {
  query rules "read the $1 tuples of $3" \
    "select coalesce(string_agg(to_kind || ':' || to_id, ',' order by to_kind, to_id), '') from (
       select distinct on (to_kind, to_id) to_kind, to_id, asserted from graph_tuples
       where kind = '$1' and from_kind = '$2' and from_id = '$3' order by to_kind, to_id, seq desc) t where asserted"
}
# check_section: the section the loop below read ends as the inventory says. It returns 1 at the
# first difference, which it prints.
check_section() {
  local what="$name \"$heading\"" ask link shaped want_replaced matching
  case "$state" in
    decision | unsure | unplaced | replaced)
      [[ "$(jq length <<<"$decision")" == 1 ]] ||
        { differ_section "$what is one decision under its heading block $block" "this head answers $decision"; return 1; }
      decision="$(jq -c '.[0]' <<<"$decision")"
      jq -e --arg h "$heading" '.text | contains($h)' <<<"$decision" >/dev/null ||
        { differ_section "$what: the decision holds its heading's words" "$(jq -c '.text' <<<"$decision")"; return 1; }
      [[ "$(jq -r '.document.artifact_id' <<<"$decision")" == "${spec_of[$name]}" ]] ||
        { differ_section "$what: the decision is a block of the spec of $key" "$(jq -c '.document' <<<"$decision")"; return 1; }
      want_replaced=false
      [[ "$state" != replaced ]] || want_replaced=true
      [[ "$(jq -r '.replaced' <<<"$decision")" == "$want_replaced" ]] ||
        { differ_section "$what: replaced is $want_replaced" "$(jq -c '{replaced}' <<<"$decision")"; return 1; }
      case "$state" in
        unsure | unplaced)
          [[ "$(jq -r '.uncertainty' <<<"$decision")" == high && -n "$(jq -r '.uncertainty_reason // ""' <<<"$decision")" ]] ||
            { differ_section "$what: uncertainty is high, with its reason" "$(jq -c '{uncertainty, uncertainty_reason}' <<<"$decision")"; return 1; }
          ;;
        *)
          [[ "$(jq -r '.uncertainty' <<<"$decision")" == null ]] ||
            { differ_section "$what: the decision carries no uncertainty" "$(jq -c '{uncertainty, uncertainty_reason}' <<<"$decision")"; return 1; }
          ;;
      esac
      current_tuples from_ask block "$(jq -r '.node.id' <<<"$decision")"
      if [[ "$state" == unplaced ]]; then
        jq -e '.uncertainty_reason | contains("could not place this section under one ask")' <<<"$decision" >/dev/null ||
          { differ_section "$what: the reason says the conversion could not place it under one ask" "$(jq -c '.uncertainty_reason' <<<"$decision")"; return 1; }
        for ask in ${source//,/ }; do
          jq -e --arg a "$ask" '.text | contains("/ask/" + $a)' <<<"$decision" >/dev/null ||
            { differ_section "$what: the decision cites the ask $ask it could not place it under" "$(jq -c '.text' <<<"$decision")"; return 1; }
        done
        [[ -z "$answer" ]] || { differ_section "$what: the decision settles no one ask, so it holds no from_ask" "it holds $answer"; return 1; }
      elif [[ "$source" != - ]]; then
        [[ "$answer" == "ask:$source" ]] ||
          { differ_section "$what: the decision's from_ask is the ask $source it cites" "it holds '${answer:-no from_ask}'"; return 1; }
      fi
      shaped="$(jq -r '[.shaped[].to.id] | unique | join(",") | if . == "" then "-" else . end' <<<"$decision")"
      [[ "$shaped" == "$links" ]] ||
        { differ_section "$what: the decision takes the section's links as shaped" "it shapes [$shaped], the section linked [$links]"; return 1; }
      [[ "$resolved_to" == "block:$(jq -r '.node.id' <<<"$decision")" ]] ||
        { differ_section "$what: the section's heading address resolves to its decision" "$heading_address resolves to $resolved_to"; return 1; }
      pass rules "$what ends as a decision of the spec ($state), keeping its heading block $block, shaping [$shaped], from_ask [${answer:-none}], and its old address resolves to it"
      ;;
    reading | dropped)
      ask_entry="$(jq -c --arg a "$source" '[.entries[] | select(.node.kind == "ask" and .node.id == $a)] | if length == 1 then .[0] else null end' "$file")"
      [[ "$ask_entry" != null ]] || { differ_section "$what: the ask $source it restates is one entry" "$(jq -c '[.entries[].node]' "$file")"; return 1; }
      [[ "$(jq length <<<"$decision")" == 0 ]] ||
        { differ_section "$what: no decision keeps its heading block $block" "$decision"; return 1; }
      ask_block_address rules "$source"
      [[ "$resolved_to" == "block:$where" ]] ||
        { differ_section "$what: its heading address resolves to the block of the ask $source it restates" "$heading_address resolves to $resolved_to"; return 1; }
      # A reading's text is its block, fence lines and all; its words are the lines between them.
      matching="$(jq --arg w "$words" '[.readings[] | select((.text // "") | split("\n") |
          (if (.[0] // "" | startswith(":::")) then .[1:-1] else . end) | join(" ") | gsub("\\s+"; " ") | ltrimstr(" ") | rtrimstr(" ") == $w)] | length' <<<"$ask_entry")"
      if [[ "$state" == reading ]]; then
        [[ "$matching" == 1 ]] ||
          { differ_section "$what: one reading under the ask $source holds the section's words" \
            "its readings: $(jq -c '[.readings[].text]' <<<"$ask_entry"), the section: $words"; return 1; }
        if [[ "$links" != - ]]; then
          for link in ${links//,/ }; do
            jq -e --arg l "$link" 'any(.shaped[]; .to.id == $l)' <<<"$ask_entry" >/dev/null ||
              { differ_section "$what: the ask $source takes the section's link $link as shaped" "$(jq -c '[.shaped[].to.id]' <<<"$ask_entry")"; return 1; }
          done
        fi
        pass rules "$what ends as a reading under the ask $source, its links shaped by the ask, and its old address resolves to the ask's block $where"
      else
        [[ "$matching" == 0 ]] ||
          { differ_section "$what: no reading is written for a dropped section" "$(jq -c '[.readings[].text]' <<<"$ask_entry")"; return 1; }
        pass rules "$what is dropped as the ask $source's restatement: no reading, no decision, and its old address resolves to the ask's block $where"
      fi
      ;;
  esac
}
while IFS=$'\t' read -r name heading block text links cites state source; do
  key="${key_of[$name]}"
  entries "$name"
  file="$work/entries.$name.json"
  # The old read's text of a section opens with its heading's line, which no reading holds.
  words="$(base64 -d <<<"$text" | sed '1{/^#/d}' | normalized)"
  heading_address="dispatch://$key/artifact/decisions-md#b-$block"
  resolve rules "$heading_address"
  resolved_to="$(jq -r 'if . == null then "none" else .kind + ":" + .id end' "$work/node.json")"
  decision="$(jq -c --arg b "$block" '[.entries[] | select(.kind == "agent" and .document.block_id == $b)]' "$file")"
  if [[ "$state" == reading ]]; then
    readings_of["$source"]=$((${readings_of[$source]:-0} + 1))
  fi
  check_section || true
done <"$work/capture/expected.tsv"
# No ask holds a reading the inventory does not expect.
for name in "${issues[@]}"; do
  entries "$name"
  while read -r ask count; do
    [[ "$count" == "${readings_of[$ask]:-0}" ]] ||
      differ_section "the ask $ask of ${key_of[$name]} holds a reading for each section the inventory reads under it" \
        "it holds $count, the inventory expects ${readings_of[$ask]:-0}"
  done < <(jq -r '.entries[] | select(.node.kind == "ask") | .node.id + " " + (.readings | length | tostring)' "$work/entries.$name.json")
done
if [[ "$section_differs" == 0 ]]; then
  pass rules "every section of the four decisions.md ends as the inventory expects, and no ask holds a reading it does not"
else
  printf '%s sections, or readings, end otherwise than the inventory says, each printed above; the run fails on them after its other reads\n' "$section_differs"
fi

# -- The retired document and the addresses ------------------------------------------------
# The check's link sentence is asserted in two halves, each run once against a deliberate break of
# its own, made in a scratch copy of the checkout and never here, which it failed on:
#
#   - The server half: the reference read (GET /api/v1/references?to=) resolves
#     dispatch://KEY/decisions, the page URL /issues/KEY/decisions and
#     dispatch://KEY/artifact/decisions-md each to the issue node itself: kind issue, id KEY,
#     state live. Its break: the scratch copy's packages/envoy/internal/dispatch/text/refs.go
#     without the branch of parseDispatch that reads `dispatch://KEY/decisions` as the issue, built
#     as HEAD_BINARY, so that reference names no node and the half fails on its first address.
#   - The web half: the web app's route builder (web/src/features/refs/routes.ts, run with bun)
#     gives dispatch://KEY/decisions and the page URL /issues/KEY/decisions the link
#     /issues/KEY/decisions. Its break: the scratch copy's routes.ts without "decisions" among
#     PLAIN_ISSUE_TABS, so neither names a route, the builder prints `none` for each, and the half
#     fails on them.
cat >"$work/route.ts" <<TS
import { buildReferencePath, referenceRouteFromHref } from "$web_routes";
for (const href of process.argv.slice(2)) {
  const route = referenceRouteFromHref(href, "http://dispatch.invalid");
  console.log(route === undefined ? "none" : buildReferencePath(route));
}
TS
for name in "${issues[@]}"; do
  key="${key_of[$name]}"
  document="${decisions_of[$name]}"
  query retired "read the decisions.md of $key" \
    "select coalesce(role, '') || '|' || (retired_at is not null)::text || '|' || slug from artifacts where id = '$document'"
  [[ "$answer" == "|true|decisions-md" ]] ||
    fail retired "the decisions.md of $key is retired, its role clear and its slug kept" "role|retired|slug reads $answer"
  query retired "read the node of the decisions.md of $key" \
    "select coalesce((select n.state from graph_nodes n join artifacts a on n.kind = 'artifact' and n.id = a.ref_key where a.id = '$document'), 'no node')"
  [[ "$answer" == gone ]] || fail retired "the node of the decisions.md of $key reads gone" "it reads $answer"
  send GET "/api/v1/issues/$key/artifacts"
  want_status 200 retired "list the documents of $key"
  jq -e --arg s "${spec_of[$name]}" 'any(.[]; .id == $s)' "$work/body" >/dev/null ||
    fail retired "the document list of $key holds its spec" "$(jq -c '[.[].slug]' "$work/body")"
  ! jq -e --arg d "$document" 'any(.[]; .id == $d)' "$work/body" >/dev/null ||
    fail retired "the document list of $key no longer holds its decisions.md" "$(jq -c '[.[].slug]' "$work/body")"
  send GET "/api/v1/artifacts/$document"
  want_status 200 retired "read the retired decisions.md of $key by its id"
  expect retired "the retired decisions.md of $key keeps its slug and holds no role" '{"role":null,"slug":"decisions-md"}' '{slug, role}'
  pass retired "the decisions.md of $key is retired and not deleted: no role, its node gone, out of the document list beside the spec, its slug kept, every version answering by number"
  # The server half.
  for address in "dispatch://$key/decisions" "$base/issues/$key/decisions" "dispatch://$key/artifact/decisions-md"; do
    resolve addresses "$address"
    # The reference read's whole answer for each address, so a reader sees what it gives an
    # address of the Decisions view: a node of the issue, and no link member of its own.
    printf 'reference read GET /api/v1/references?to=%s answers HTTP %s: %s\n' "$(q "$address")" "$status" \
      "$(jq -c '{node, edges: (.edges | length), unknown_kinds}' "$work/body" 2>/dev/null || body)"
    [[ "$status" == 200 && "$(jq -r '[.kind, .id, .state] | map(. // "") | join(":")' "$work/node.json")" == "issue:$key:live" ]] ||
      fail addresses "$address resolves to the issue node $key, live" "HTTP $status $(jq -c '{kind, id, state}' "$work/node.json")"
  done
  pass addresses "the reference read resolves dispatch://$key/decisions, $base/issues/$key/decisions and dispatch://$key/artifact/decisions-md each to the issue node: kind issue, id $key, state live"
  send GET "/api/v1/issues/$key/decisions"
  want_status 200 addresses "read the Decisions read of $key"
  expect addresses "the Decisions read of $key names no decisions document" null '.document'
  # The web half.
  links="$(bun "$work/route.ts" "dispatch://$key/decisions" "/issues/$key/decisions" 2>&1)" ||
    fail addresses "run the reference route of the web app over the two addresses of the Decisions of $key" "$links"
  [[ "$links" == "/issues/$key/decisions"$'\n'"/issues/$key/decisions" ]] ||
    fail addresses "the web app's route builder gives dispatch://$key/decisions and /issues/$key/decisions the link /issues/$key/decisions" "it gives: $links"
  pass addresses "the web app's route builder gives dispatch://$key/decisions and /issues/$key/decisions the link /issues/$key/decisions"
done

# -- The census of issues not yet converted ------------------------------------------------
census_code=0
# shellcheck disable=SC2031 # this shell's own PATH: only a server's subshell sets another
env -i PATH="$PATH" HOME="$work/home" TMPDIR="$work" DATABASE_URL="$baseline_database_url" \
  "$head_binary" census --store >"$work/census.out" 2>&1 || census_code=$?
[[ "$census_code" -le 1 ]] || fail census "envoy-dispatch census --store" "it exited $census_code: $(tail -n 20 "$work/census.out")"
grep '^census: decisions ' "$work/census.out" | sed 's/^/  /' || true
grep -qx 'census: decisions documents=0 unconverted_sections=0 specs_with_moved_answers=0' "$work/census.out" ||
  fail census "the census of issues not yet converted reads zero" "$(grep '^census: decisions\|^census: spec_with_moved_answers' "$work/census.out" || echo 'no decisions line')"
pass census "this head's census --store reads no decisions document, no unconverted section and no spec whose answered asks left it, where the build of CONVERTER_SHA first counted $first_owed owed"

# -- R20: the census of shared block ids, the redirect, the re-key of search --------------
shared_sql="select coalesce(string_agg(issue_key || '#' || block_id, ' '), '') from (
    select d.issue_key, b.block_id from document_blocks b join artifacts d on d.id = b.artifact_id
    where d.issue_key in ($issue_keys) and d.kind = 'doc'
    group by d.issue_key, b.block_id having count(distinct b.artifact_id) > 1) shared"
# The control: the same read over a planted share, a copy of one block of the spec of own-carried
# under its retired decisions.md, made and taken back in one transaction.
with_vars move "read the shares a planted block makes" "begin;
  create temp table planted as select * from document_blocks where artifact_id = :'spec' and block_id = 'r-night';
  update planted set artifact_id = :'decisions';
  insert into document_blocks select * from planted;
  $shared_sql;
  rollback;" spec="$carried_spec" decisions="${decisions_of[own-carried]}"
[[ "$answer" == *"$carried#r-night"* ]] ||
  fail move "the census of shared block ids finds a planted block of two documents of $carried" "it reads '$answer'"
pass move "the census of block ids shared between an issue's documents finds the planted $carried#r-night"
query move "read the block ids an issue's documents share" "$shared_sql"
[[ -z "$answer" ]] || fail move "no block id stands in two documents of one issue of the fixture" "shared: $answer"
pass move "no block id stands in two documents of one of the four issues, the retired decisions.md among them"
while read -r id key block shape; do
  [[ "$shape" == second ]] || continue
  moved_to_rows "$key/decisions-md#$block"
  [[ "$answer" == "block:$key/spec#$block:true" ]] ||
    fail move "one moved_to tuple from $key/decisions-md#$block to $key/spec#$block" "${answer:-none}"
  resolve move "dispatch://$key/artifact/decisions-md#b-$block"
  [[ "$status" == 200 && "$(jq -r '.id' "$work/node.json")" == "$key/spec#$block" ]] ||
    fail move "the old address of the moved block $block resolves to it in the spec" "HTTP $status $(jq -c '{kind, id}' "$work/node.json")"
  pass move "the moved ask block $block keeps resolving at its old address $key/decisions-md#$block, through one moved_to tuple to $key/spec#$block"
done <"$work/capture/restores.txt"
search_documents move
grep -qx "$carried/$carried_spec_slug" "$work/search" ||
  fail move "this head's search finds q-doc's words in the spec of $carried, where the block moved" "it finds: $(tr '\n' ' ' <"$work/search")"
! grep -qx "$carried/decisions-md" "$work/search" ||
  fail move "this head's search no longer finds q-doc's words in the retired decisions.md of $carried" "it finds: $(tr '\n' ' ' <"$work/search")"
pass move "search follows the moved block: \"$search_words\" is found in $carried/$carried_spec_slug, and no longer in the retired $carried/decisions-md, where the baseline found it"

# -- R5: the two shapes of the restore --------------------------------------------------------
declare -A spec_markdown=()
while read -r id key block shape; do
  for name in "${issues[@]}"; do
    if [[ "${key_of[$name]}" == "$key" ]]; then
      spec="${spec_of[$name]}"
    fi
  done
  if [[ -z "${spec_markdown[$spec]:-}" ]]; then
    latest_markdown restore "$spec"
    cp "$work/latest.md" "$work/spec.$spec.md"
    spec_markdown["$spec"]="$work/spec.$spec.md"
  fi
  send GET "/api/v1/asks/$id"
  want_status 200 restore "read the ask $id at this head"
  jq '.ask' "$work/body" >"$work/ask.now.json"
  expect restore "the ask $id keeps its id, its answer and its answerer" \
    "$(jq -cS '{id, answer: (.answer | if . == null then null else {text, selected, user} end)}' "$work/capture/ask.$id.json")" \
    '.ask | {id, answer: (.answer | if . == null then null else {text, selected, user} end)}'
  [[ "$(jq -r '.block_artifact.id' "$work/ask.now.json")" == "$spec" && "$(jq -r '.block_id' "$work/ask.now.json")" == "$block" ]] ||
    fail restore "the ask $id stands in the spec of $key under its old block id $block" "$(jq -c '{block_id, block_artifact}' "$work/ask.now.json")"
  ask_block "$block" "${spec_markdown[$spec]}" >"$work/block.md"
  [[ -s "$work/block.md" ]] || fail restore "the newest version of the spec of $key holds the ask block $block" "it holds none"
  sed -n '/^## Decisions$/,/^## /p' "${spec_markdown[$spec]}" | grep -q "^:*ask{#${block}[ }]" ||
    fail restore "the ask block $block stands in the spec's ## Decisions part" "$(grep -n '^## \|^:*ask{' "${spec_markdown[$spec]}" | tr '\n' ' ')"
  opening="$(head -n 1 "$work/block.md")"
  [[ ! "$opening" =~ \ (selected|answer|copied_from|copied_from_document)=\" ]] ||
    fail restore "the restored block $block holds no answer and no mark of a copy" "$opening"
  block_body "$work/block.md" >"$work/body.got"
  expected_body "$work/ask.now.json" >"$work/body.want"
  cmp -s "$work/body.got" "$work/body.want" ||
    fail restore "the restored block $block holds the question and options of the ask's node, character for character" \
      "want $(<"$work/body.want"), got $(<"$work/body.got")"
  query restore "read the copied_from tuples of $key/spec#$block" \
    "select count(*) from graph_tuples where kind = 'copied_from' and from_kind = 'block' and from_id = '$key/spec#$block'"
  [[ "$answer" == 0 ]] || fail restore "the restored block $block carries no copied_from" "$answer tuples"
  if [[ "$shape" == main ]]; then
    # The restore writes no moved_to of its own: its old address answers again. The tuples that
    # end at the block are the moves of the sections that restate the ask (R8), from their headings.
    moved_to_rows "$key/spec#$block"
    [[ -z "$answer" ]] || fail restore "no moved_to tuple leaves $key/spec#$block, which returned under its old id" "$answer"
    query restore "read the moved_to tuples that end at $key/spec#$block" \
      "select coalesce(string_agg(from_id, ' ' order by seq), '') from graph_tuples where kind = 'moved_to' and to_kind = 'block' and to_id = '$key/spec#$block'"
    [[ -z "$answer" || "$answer" != *"/spec#"* ]] ||
      fail restore "every moved_to tuple that ends at $key/spec#$block leaves a section of the decisions document" "$answer"
    pass restore "the answered ask $id whose block no document held is written again into the spec of $key from its node, at its old block id $block, with no answer, no copy mark and no moved_to tuple of its own (moves of sections that restate it: ${answer:-none}): $opening"
  else
    # The ask node's own part_of tuples, each `to_kind:to_id:asserted:event`: the move appends one
    # to the spec and retracts the one to the decisions document, under one event.
    query restore "read the part_of tuples of the ask $id" \
      "select coalesce(string_agg(to_kind || ':' || to_id || ':' || asserted::text || ':' || source_event, ' ' order by seq), '') from graph_tuples
       where kind = 'part_of' and from_kind = 'ask' and from_id = '$id'"
    printf 'the part_of tuples of the ask %s: %s\n' "$id" "$answer"
    moved_event="$(tr ' ' '\n' <<<"$answer" | awk -F: -v s="artifact:$key/spec" '$1 ":" $2 == s && $3 == "true" { e = $4 } END { print e }')"
    current="$(tr ' ' '\n' <<<"$answer" | awk -F: 'NF == 4 { last[$1 ":" $2] = $3 } END { for (k in last) if (last[k] == "true") print k }' | sort | tr '\n' ' ')"
    [[ -n "$moved_event" && " $answer " == *" artifact:$key/decisions-md:false:$moved_event "* ]] ||
      fail restore "the re-parent of the ask $id appends a part_of tuple to the spec and retracts the one to the decisions document, under one event" "$answer"
    [[ "$current" == "artifact:$key/spec " ]] ||
      fail restore "the ask $id is part of the spec of $key alone" "its current part_of: $current"
    pass restore "the ask $id whose block the decisions.md of $key held moved into the spec under its own id $block, re-parented by a part_of tuple to the spec and the retraction of the old one under event $moved_event, with no copy mark"
  fi
done <"$work/capture/restores.txt"

# -- The approvals ----------------------------------------------------------------------------
for name in "${issues[@]}"; do
  key="${key_of[$name]}"
  send GET "/api/v1/artifacts/${spec_of[$name]}"
  want_status 200 approval "read the spec of $key"
  jq '.approval' "$work/body" >"$work/approval.json"
  latest="$(jq -r '[.versions[].number] | max' "$work/body")"
  issue_line="$(grep -F 'msg="dispatch: decisions step restored and converted an issue"' "$work/converter-report" | grep -F " issue=$key " || true)"
  if [[ "$name" == own-carried ]]; then
    jq -e --arg l "$login" --argjson v "$latest" '.state == "approved" and .version == $v and .by.id == $l' "$work/approval.json" >/dev/null ||
      fail approval "the spec of $key comes back approved by $login at its new version $latest" "$(jq -c . "$work/approval.json")"
    query approval "read the digest of version $latest of the spec of $key" \
      "select encode(sha256(convert_to(markdown, 'UTF8')), 'hex') from artifact_versions where artifact_id = '${spec_of[$name]}' and number = $latest"
    [[ "$(jq -r '.content_sha256 // ""' "$work/approval.json")" == "$answer" ]] ||
      fail approval "the carried approval of $key stands against the digest of its version $latest" "$(jq -c '{content_sha256}' "$work/approval.json"), the version reads $answer"
    [[ "$issue_line" == *" spec_approval=carried "* ]] ||
      fail approval "the step reports the approval of the spec of $key carried" "${issue_line:-no report line names $key}"
    pass approval "the spec of $key, whose version holds restored asks, the Decisions heading, decision blocks, a reading and the answer's attributes taken off, comes back approved by $login at version $latest, against its digest $answer"
  else
    approved="${approved_before[$name]}"
    [[ "$approved" == "approved "* ]] || continue
    superseded_line="$(grep -F 'msg="dispatch: decisions step superseded an approval"' "$work/converter-report" |
      grep -F " issue=$key " | grep -F " approved_version=${approved#approved } " || true)"
    [[ -n "$superseded_line" ]] ||
      fail approval "the step's write reports the approval of version ${approved#approved } of the spec of $key it superseded" "$(grep -F " issue=$key " "$work/converter-report" || echo 'no line names it')"
    jq -e --argjson v "$latest" '.state != "approved" or .version != $v' "$work/approval.json" >/dev/null ||
      fail approval "the spec of $key, a version of which changes the text of a block, comes back unapproved" "$(jq -c . "$work/approval.json")"
    pass approval "the spec of $key comes back unapproved at version $latest ($(jq -r '.state' "$work/approval.json")), and the step reported the approval of version ${approved#approved } it superseded: $superseded_line"
  fi
done

# -- The gated steps --------------------------------------------------------------------------
send GET /api/v1/projects
want_status 200 gated "list the projects at this head"
for key in "$project" "$alone_project"; do
  got="$(jq -c --arg k "$key" '[.[] | select(.key == $k) | .gated_steps][0]' "$work/body")"
  [[ "$got" == "${gated_after[$key]}" ]] ||
    fail gated "this head reads the gated steps of $key, stored as ${gated_before[$key]}, as ${gated_after[$key]}" "it reads $got"
  pass gated "this head reads the gated steps of $key, which the baseline stored as ${gated_before[$key]}, as $got"
done
[[ "$section_differs" == 0 ]] ||
  fail rules "every section of the four decisions.md ends as the check's inventory says" "$section_differs end otherwise, each printed above as DIFFERS: rules"
pass "$driver" "complete: the upgrade of a database the baseline $baseline_served wrote, converted by $converter_commit, reads every row the check names at this head"
