#!/usr/bin/env bash
# Drives check c-visibility (spec requirements r-visibility and r-interaction of
# dispatch://INFRA-1098) against the `bun run dispatch:local` server, over the seeded corpus
# (lib-corpus.sh, which seeds it once where the database lacks it). The enum clause of the check is
# not this driver's: the Go test TestTheNodeKindEnumHoldsExactlyTheKindsTheGraphResolves
# (packages/envoy/internal/dispatch/refs) is the check's second run, and this driver reads no enum.
#
#   V1  Every node of the store carries a visibility of project or author, each counted.
#   V2  The corpus's node held at author (bullet 22): DEV_SIGNIN_LOGIN's lens reserved for her, on a
#       project requirement of the seeded issue, between two project lenses on that requirement and
#       with a project lens triggered by it, so no surface misses it through isolation.
#   V3  The store refuses author on a comment, a kind no writer may reserve, naming the kind, and
#       stores nothing; the agent bearer, which proves no author, is refused author on a lens.
#   V4  The direct read of the node's own address: its author's cookie and her personal token find
#       it; bob's cookie, bob's personal token and the agent bearer each read what an address of the
#       same form nobody wrote answers, status and body, once the id is substituted. A personal
#       token resolves as a session with an owner, so a predicate comparing kind and id alone would
#       hand bob's token every node alice holds (api/routes_table_test.go says why).
#   V5  Every surface that walks or summarizes the graph: the issue tree expansion and the parent
#       walk inside it (a node stands under the nearest node above it its reader may see), search
#       (the actor carried into the read), the inbox, the chain, the coverage read, the references
#       panel with its backlink count on each neighbour, the lens read of the requirement, the
#       issue read with its backlink count, the person page of the author, a historical version
#       read of the spec, and the notification feed (the Inbox threads, the issue's event log and
#       the whole event log). On each, bob's cookie, bob's token and the agent bearer read no id
#       and no words of the node, and each count they read is the author's count less what she
#       reads of the node; the author's read finds a public node the read covers, so each absence
#       stands beside a read that finds what it covers. A read that answers each person their own
#       rows (the inbox, the Inbox threads) holds bob's token to his cookie, and the agent bearer,
#       who is nobody, is refused it.
#   V6  The driver causes an interaction rather than reading a seeded one: the author asks the
#       command bar one question (POST /api/v1/going/requests, going_routes.go, which records the
#       run) and writes a comment under that run (the Dispatch-Going-Run header). The interaction
#       node arises, reserved for the credential that asked it, with the comment's changed_by_run
#       tuple to it; its label is interaction, one member of the closed union of GraphNode.kind;
#       and its backlink count, read by that credential, is the changed_by_run it holds. Her
#       personal token, bob and the agent bearer read it as an address nobody wrote. No reply is
#       written for this: no reply path writes going_requests (recordGoingRequest in going.go is the
#       one writer), so a reply would cause no interaction.
#   V7  The seeded interaction nodes under the same rule: a row whose author is a credential's
#       answers that credential and no other; a row whose author proves nobody reaches nobody.
#
# The author half of this check is harness-only: the two person-proving credentials are the
# harness's dev sign-in (GET /auth/_dev/signin answers 302 and a session cookie) and a personal
# token each person mints with that cookie (POST /api/v1/me/agent-tokens). A Dispatch deployed
# without dev sign-in has no /auth/_dev/signin, so no driver of this tree runs this check there.
#
# Inputs: DATABASE_URL (the database the server at DISPATCH_URL serves, on this machine: the corpus's
# loader writes it and psql reads it), DISPATCH_URL (the server), DISPATCH_TOKEN or
# DISPATCH_TOKEN_FILE (an agent bearer, read here and never printed), PROJECT (the project the
# corpus is seeded into, CORE) and DEV_SIGNIN_LOGIN (the person the server's dev sign-in route
# signs in and the author of the corpus's node held at author, alice on `bun run dispatch:local`);
# the second person is bob, whom the harness seeds too. Each unmet input is exit 2. The issue V6
# writes on is the run's own, which it closes when it exits, also when a check fails, and the two
# personal tokens are revoked then; nothing it writes is a row the loader's self-check counts.
#
# Each passing check prints `PASS: <section> <what>`; the first failing one prints
# `FAIL: <section> <what>: <observed>` and the script exits 1. The last line is
# `PASS: visibility.sh complete`.
#
# Every check is `cmd || fail …`, never part of an `&&` list: under `set -e` a failing command
# anywhere but last in such a list does not stop the script.

# shellcheck disable=SC2016 # the single-quoted jq filters and SQL name jq and psql variables, not shell ones
set -euo pipefail
set +x

usage() {
  cat >&2 <<'USAGE'
Usage: DATABASE_URL=postgres://…@127.0.0.1:5432/<db>?sslmode=disable DISPATCH_URL=http://127.0.0.1:8777 \
         DISPATCH_TOKEN=e2e-token PROJECT=CORE DEV_SIGNIN_LOGIN=alice visibility.sh
       (DISPATCH_TOKEN_FILE=<file holding the bearer> in place of DISPATCH_TOKEN; DATABASE_URL names
       the database the server at DISPATCH_URL serves, on this machine; the server must mount its
       dev sign-in route, as `bun run dispatch:local` does, for DEV_SIGNIN_LOGIN and for bob)
USAGE
}

# shellcheck source-path=SCRIPTDIR source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
# shellcheck source-path=SCRIPTDIR source=lib-corpus.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-corpus.sh"
require_env DATABASE_URL DISPATCH_URL PROJECT DEV_SIGNIN_LOGIN
read_bearer
require_commands awk cmp curl grep jq psql sed

readonly driver=visibility.sh
# shellcheck disable=SC2031 # the DISPATCH_URL lib.sh's `cli` exports is its own; this one is the caller's
readonly base="${DISPATCH_URL%/}"
# shellcheck disable=SC2153 # PROJECT, DATABASE_URL and DEV_SIGNIN_LOGIN are the caller's inputs, checked by name above
readonly project="$PROJECT" database_url="$DATABASE_URL" login="$DEV_SIGNIN_LOGIN"
readonly second=bob
[[ "$login" != "$second" ]] || {
  printf 'DEV_SIGNIN_LOGIN is %s, the second person this driver signs in; name the corpus author\n' "$second" >&2
  usage
  exit 2
}
loopback_database_url "$database_url" || {
  printf 'DATABASE_URL names no database on this machine: the corpus loader writes it\n' >&2
  usage
  exit 2
}
corpus_tools
stamp="$(date +%s)-$$"
readonly stamp
readonly session="visibility-$stamp"
# An id nobody ever wrote: the address of the same form each hidden node's address is held to.
readonly missing="00000000-0000-4000-8000-000000000000"

work_dir
printf 'Authorization: Bearer %s\n' "$token" >"$work/auth"

created=""
cleanup() {
  local code=$?
  revoke_tokens
  [[ -z "$created" ]] || close_issue "$created"
  rm -rf "$work"
  exit "$code"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# The three readers that prove no author of the node: bob's cookie, bob's personal token and the
# agent bearer, each as lib.sh's request names it.
readonly -a outsiders=("cookie:$second" "token:$second" agent)

# label READER: the reader as a line names it.
label() {
  case "$1" in
    cookie:*) printf "%s's cookie" "${1#cookie:}" ;;
    token:*) printf "%s's personal token" "${1#token:}" ;;
    *) printf 'the agent bearer' ;;
  esac
}

# like_nobody SECTION WHAT READER ADDRESS ID: READER reads GET /api/v1/references?to=ADDRESS, and
# the answer is what the address of the same form whose id is $missing, which nobody wrote,
# answers READER: its status, its Content-Type and its body, byte for byte once ID is substituted.
like_nobody() {
  local section="$1" what="$2" reader="$3" address="$4" id="$5" nobody_head
  request "$reader" GET "/api/v1/references?to=$(q "${address%"$id"}$missing")"
  [[ "$status" != 200 ]] || fail "$section" "an address nobody wrote answers $(label "$reader") no node" "HTTP 200: $(body)"
  cp "$work/body" "$work/nobody.json"
  nobody_head="$status $content_type"
  request "$reader" GET "/api/v1/references?to=$(q "$address")"
  sed "s/$id/$missing/g" "$work/body" >"$work/substituted.json"
  if [[ "$status $content_type" != "$nobody_head" ]] || ! cmp -s "$work/substituted.json" "$work/nobody.json"; then
    fail "$section" "$what" "HTTP $status, $content_type: $(body); the address nobody wrote answers $nobody_head: $(head -c 600 "$work/nobody.json")"
  fi
  pass "$section" "$what: HTTP $status, $(head -c 200 "$work/body")"
}

# leaks FILE: the strings of the answer FILE that name the hidden node or carry its words, as a JSON
# array ($work/hidden.json holds them).
leaks() {
  jq -c --slurpfile hidden "$work/hidden.json" \
    '[.. | strings | select(. as $s | any($hidden[0][]; . as $h | $s | contains($h)))] | unique' "$1"
}

# counts FILE ARRAYS SCALARS: of the answer FILE, the length of each array the jq object ARRAYS
# names, with how many of its items are the author's own, and the value of each count the JSON
# object SCALARS names, as one JSON object. An item is the author's own where it names the hidden
# node, or is itself a node at author visibility (the author's other reserved nodes, such as her
# runs of the command bar, which reach no other reader either), and, in an answer of nodes and the
# tuples between them (the tree read), where it is a node that only tuples naming the hidden node
# tie to the answer: the context a node brings with it, as the lens_kind a has_kind of the hidden
# lens names, is absent where the node is.
counts() {
  jq -cS --slurpfile hidden "$work/hidden.json" --argjson scalars "$3" "
    def names: ([.. | strings] | any(. as \$s | any(\$hidden[0][]; . as \$h | \$s | contains(\$h))))
      or (type == \"object\" and .visibility == \"author\");
    . as \$answer
    | ($2) as \$arrays
    | (if (\$arrays | has(\"nodes\") and has(\"tuples\")) then
         ([\$arrays.tuples[] | select(names)]) as \$held
         | ([\$arrays.tuples[] | select(names | not)]) as \$kept
         | [\$arrays.nodes[] | select(names | not) | {kind, id} as \$n
             | select(any(\$held[]; .from == \$n or .to == \$n) and (any(\$kept[]; .from == \$n or .to == \$n) | not))] | length
       else 0 end) as \$brought
    | {arrays: (\$arrays | with_entries(.value |= ((. // []) | {all: length, hidden: (map(select(names)) | length)}))
         | if has(\"nodes\") and has(\"tuples\") then .nodes.hidden += \$brought else . end),
       scalars: (\$scalars | with_entries(.value = (\$answer[.key] // null)))}" "$1"
}

# current_sql FILTER: the tuples the store asserts now among those FILTER keeps, the newest
# statement of each triple.
current_sql() {
  printf "(select * from (select distinct on (from_kind, from_id, kind, to_kind, to_id) * from graph_tuples
     where %s order by from_kind, from_id, kind, to_kind, to_id, seq desc) latest where asserted)" "$1"
}

# words FIRST|LAST N TEXT: N words of TEXT, its punctuation dropped, from its start or its end.
words() {
  awk -v from="$1" -v n="$2" '{
    first = (from == "first") ? 1 : (NF > n ? NF - n + 1 : 1)
    last = (from == "first") ? (NF < n ? NF : n) : NF
    out = ""
    for (i = first; i <= last; i++) out = out (out == "" ? "" : " ") $i
    print out
  }' <<<"${3//[^[:alnum:] ]/}"
}

# --- setup: the corpus, the two people and their tokens ----------------------------------------

corpus_ensure setup
key="$corpus_issue"
readonly key
sign_in setup "$login"
sign_in setup "$second"
mint_token "$login"
mint_token "$second"

# --- V1: every node is project or author ------------------------------------------------------

query V1 "count the nodes of the store by visibility" \
  "select json_build_object('project', count(*) filter (where visibility = 'project'), 'author', count(*) filter (where visibility = 'author'),
     'other', count(*) filter (where visibility not in ('project', 'author')), 'examined', count(*)) from graph_nodes"
jq -e '.examined > 0 and .project > 0 and .author > 0 and .other == 0' <<<"$answer" >/dev/null ||
  fail V1 "every node of the store carries a visibility of project or author" "the store counts $answer"
pass V1 "every node of the store carries a visibility of project or author: $answer"

# --- V2: the node held at author, and its project neighbourhood -------------------------------

with_vars V2 "find $login's lens reserved for her on a requirement of $key" \
  "select coalesce(json_agg(json_build_object('id', n.id, 'parent', p.to_id)), '[]') from graph_nodes n
     join $(current_sql "from_kind = 'lens' and kind = 'part_of' and to_kind = 'block'") p on p.from_id = n.id
   where n.kind = 'lens' and n.visibility = 'author' and n.made_by->>'id' = :'login' and p.to_id like :'key' || '/tree#%'" \
  "login=$login" "key=$key"
[[ "$(jq 'length' <<<"$answer")" == 1 ]] ||
  fail V2 "the corpus holds one lens of $login reserved for her on a requirement of $key (bullet 22)" "the store holds $answer"
hidden_id="$(jq -r '.[0].id' <<<"$answer")"
parent_node="$(jq -r '.[0].parent' <<<"$answer")"
readonly hidden_id parent_node
readonly hidden="dispatch://lens/$hidden_id"
with_vars V2 "read the verdict of $hidden" \
  "select coalesce((select to_id from graph_tuples where from_kind = 'lens' and from_id = :'id' and kind = 'verdict'
     order by seq desc limit 1), '')" "id=$hidden_id"
verdict="$answer"
[[ -n "$verdict" ]] || fail V2 "the hidden lens $hidden carries a verdict, its words" "it carries none"
readonly verdict
# The store keys a typed block under the supertag block; its type is the fence its text opens with.
with_vars V2 "read the visibility and the type of $parent_node" \
  "select coalesce((select n.visibility || ':' || coalesce((select substring(t.to_id from '^:::([a-z_]+)[{]') from graph_tuples t
       where t.from_kind = 'block' and t.from_id = n.id and t.kind = 'text' order by t.seq desc limit 1), '')
     from graph_nodes n where n.kind = 'block' and n.id = :'id'), '')" "id=$parent_node"
[[ "$answer" == project:requirement ]] ||
  fail V2 "the hidden lens stands on a project requirement" "its parent $parent_node is '$answer' (visibility:type)"
with_vars V2 "read the lenses beside $hidden on $parent_node" \
  "select coalesce(string_agg(n.id || ':' || n.visibility, ',' order by n.id), '') from graph_nodes n
     join $(current_sql "from_kind = 'lens' and kind = 'part_of' and to_kind = 'block'") p on p.from_id = n.id
   where n.kind = 'lens' and p.to_id = :'parent' and n.id <> :'id'" "parent=$parent_node" "id=$hidden_id"
neighbours="$answer"
[[ "$neighbours" =~ ^[^,]+:project,[^,]+:project$ ]] ||
  fail V2 "two project lenses stand beside the hidden lens on $parent_node" "the lenses beside it are '$neighbours'"
with_vars V2 "read the lenses triggered by $hidden" \
  "select coalesce(string_agg(n.id || ':' || n.visibility, ',' order by n.id), '') from graph_nodes n
     join $(current_sql "from_kind = 'lens' and kind = 'triggered_by' and to_kind = 'lens'") t on t.from_id = n.id
   where n.kind = 'lens' and t.to_id = :'id'" "id=$hidden_id"
[[ "$answer" =~ ^[^,:]+:project$ ]] ||
  fail V2 "one project lens is triggered by the hidden lens, its backlink" "the lenses it triggered are '$answer'"
readonly backlink="dispatch://lens/${answer%%:*}"
pass V2 "$hidden is $login's lens reserved for her on the project requirement $parent_node, between the project lenses $neighbours, and the project lens $backlink is triggered by it"
# What no answer to a reader who proves no author of it may name: its id and its words.
jq -cn --args '$ARGS.positional' "$hidden_id" "$verdict" >"$work/hidden.json"

# --- V3: the store refuses author where it may not be held -------------------------------------

readonly refused_body="A comment visibility.sh asks to hold at author, $stamp."
request "cookie:$login" POST "/api/v1/issues/$key/comments" \
  "$(jq -cn --arg b "$refused_body" '{body: $b, visibility: "author"}')"
want_status 400 V3 "$login writes a comment at author visibility"
expect V3 "a comment at author visibility is refused VISIBILITY_NOT_ADMITTED" '"VISIBILITY_NOT_ADMITTED"' '.code'
jq -e '.error | test("node kind comment\\b")' "$work/body" >/dev/null ||
  fail V3 "the refusal names the kind, comment" "it says $(body)"
refusal="$(jq -r '.error' "$work/body")"
with_vars V3 "count the comments that hold the refused body" "select count(*) from comments where body = :'b'" "b=$refused_body"
[[ "$answer" == 0 ]] || fail V3 "the refused comment stores nothing" "comments holds $answer rows of its body"
pass V3 "the store refuses author on a comment, naming the kind, and stores nothing: $refusal"
send POST "/api/v1/issues/$key/lenses" "$(jq -cn --arg on "dispatch://$key/spec" --argjson a "$(actor)" \
  '{on: $on, kind: "inversion", verdict: "A lens the agent bearer asks to hold at author.", findings: ["A finding of it."], visibility: "author", actor: $a}')"
want_status 403 V3 "the agent bearer writes a lens at author visibility"
expect V3 "the agent bearer, which proves no author, is refused a lens at author: AUTHOR_NAMES_NOBODY" '"AUTHOR_NAMES_NOBODY"' '.code'

# --- V4: the direct read of the node's own address ----------------------------------------------

direct="/api/v1/references?to=$(q "$hidden")"
readonly direct
for reader in "cookie:$login" "token:$login"; do
  request "$reader" GET "$direct"
  want_status 200 V4 "$(label "$reader") reads $hidden"
  expect V4 "$(label "$reader") reads $hidden, reserved for her" \
    "$(jq -cnS --arg r "$hidden" '{ref: $r, visibility: "author"}')" '.node | {ref, visibility}'
done
for reader in "${outsiders[@]}"; do
  like_nobody V4 "$(label "$reader")'s read of $hidden answers what dispatch://lens/$missing, an address nobody wrote, answers" \
    "$reader" "$hidden" "$hidden_id"
done

# --- V5: every surface that walks or summarizes the graph -------------------------------------

request "cookie:$login" GET "$direct"
parent_ref="$(jq -er '.node.parent.ref' "$work/body")" || fail V5 "read the parent of $hidden" "no parent ref: $(body)"
readonly parent_ref
readonly block="${parent_ref##*#b-}"
# The words of a public node, the requirement the hidden lens stands on (the line after its
# opening fence), and of the hidden lens.
with_vars V5 "read the words of $parent_node" \
  "select coalesce((select split_part(to_id, E'\n', 2) from graph_tuples where from_kind = 'block' and from_id = :'id' and kind = 'text'
     order by seq desc limit 1), '')" "id=$parent_node"
public_words="$(words first 4 "$answer")"
hidden_words="$(words last 6 "$verdict")"
[[ -n "$public_words" && -n "$hidden_words" ]] ||
  fail V5 "read the words search is asked for" "the requirement reads '$answer', the hidden lens '$verdict'"
readonly public_words hidden_words
# The historical version read: the oldest version of the spec that holds the requirement, a version
# the hidden lens was written on top of, read by its number.
send GET "/api/v1/issues/$key/artifacts/spec"
want_status 200 V5 "read the spec of $key"
spec_version=""
for number in $(jq -r '[.versions[].number] | sort | .[]' "$work/body"); do
  send GET "/api/v1/issues/$key/artifacts/spec/versions/$number"
  want_status 200 V5 "read version $number of the spec of $key"
  if jq -e --arg b "$block" '.markdown | contains("{#" + $b)' "$work/body" >/dev/null; then
    spec_version="$number"
    break
  fi
done
[[ -n "$spec_version" ]] || fail V5 "a version of the spec of $key holds the requirement $block" "none does"
readonly spec_version

# The surfaces, each `name~path~arrays~scalars~finder~mode` (a jq filter holds `|`, and no field
# holds `~`): arrays is a jq object of the arrays whose items the read counts; scalars a JSON object
# naming each count the read answers beside them, with the array it counts, or "" for a count of no
# array the answer holds; finder a jq filter true of the author's answer where it holds a public
# node the read covers; and mode `shared` for a read every reader is answered from the same rows,
# `person` for one a credential without the person scope is refused (403 OAUTH_SCOPE_REQUIRED), so
# the agent bearer reads nothing of it, and `own` for one that answers each person their own rows,
# so that the author's counts are no measure of bob's, his token's are held to his cookie's, and the
# agent bearer, who is nobody, is refused it.
surfaces=(
  "the issue tree expansion and the parent walk in it~/api/v1/issues/$key/tree~{nodes: .nodes, tuples: .tuples}~{}~any(.nodes[]; .id == \$parent)~shared"
  "the chain~/api/v1/issues/$key/chain~{requirements, decisions, checks, plan_tasks, results, gaps}~{}~any(.. | strings; contains(\$block))~shared"
  "the coverage read~/api/v1/issues/$key/coverage~{requirements, gaps}~{}~any(.. | strings; contains(\$block))~shared"
  "the references panel of the requirement, with its backlink count~/api/v1/references?to=$(q "$parent_ref")~{edges}~{}~.node.ref != null~shared"
  "the references panel of the lens the hidden lens triggered, out of it~/api/v1/references?from=$(q "$backlink")~{edges}~{}~.node.ref == \$backlink~shared"
  "the references panel of the lens the hidden lens triggered, into it~/api/v1/references?to=$(q "$backlink")~{edges}~{}~.node.ref == \$backlink~shared"
  "the lens read of the requirement~/api/v1/lenses?on=$(q "$parent_ref")~{lenses, chains}~{}~any(.lenses[]; .address == \$backlink or .visibility == \"project\")~shared"
  "the issue read, with its backlink count~/api/v1/issues/$key~{artifacts, open_asks, children}~{\"referenced_by_count\": \"\"}~.key == \$key~shared"
  "the person page of $login~/api/v1/people/$(q "$login")/nodes?limit=200~{rows}~{\"total\": \"rows\"}~(.rows | length) > 0~person"
  "search for the hidden lens's words, the actor carried into the read~/api/v1/search?q=$(q "$hidden_words")~{results}~{\"total\": \"results\"}~true~shared"
  "search for the requirement's words, the actor carried into the read~/api/v1/search?q=$(q "$public_words")~{results}~{\"total\": \"results\"}~(.total // 0) > 0~shared"
  "a historical version read of the spec, version $spec_version~/api/v1/issues/$key/artifacts/spec/versions/$spec_version~{}~{}~.markdown | contains(\$block)~shared"
  "the issue's event log~/api/v1/issues/$key/events~{events: .}~{}~length > 0~shared"
  "the inbox~/api/v1/inbox~{asks: .}~{}~true~own"
  "the notification feed, the Inbox threads~/api/v1/me/replies?kind=reply,agent&state=inbox~{rows}~{\"total\": \"rows\"}~true~own"
)

index=0
for surface in "${surfaces[@]}"; do
  index=$((index + 1))
  IFS='~' read -r name path arrays scalars finder mode <<<"$surface"
  request "cookie:$login" GET "$path"
  want_status 200 V5 "$login reads $name"
  author_file="$work/surface.$index.author.json"
  cp "$work/body" "$author_file"
  author_counts="$(counts "$author_file" "$arrays" "$scalars")" ||
    fail V5 "count $login's read of $name" "unreadable: $(head -c 600 "$author_file")"
  jq -e --arg parent "$parent_node" --arg backlink "$backlink" --arg key "$key" --arg block "$block" \
    "$finder" "$author_file" >/dev/null ||
    fail V5 "$login's read of $name finds a public node the read covers, beside each absence below" \
      "it holds none: $(head -c 600 "$author_file")"
  pass V5 "$login's read of $name finds a public node it covers, and names the hidden lens in $(leaks "$author_file" | jq 'length') strings; it counts $author_counts"
  cookie_counts=""
  for reader in "${outsiders[@]}"; do
    request "$reader" GET "$path"
    if [[ "$mode" != shared && "$reader" == agent ]]; then
      want_status 403 V5 "the agent bearer, which holds no person scope, reads $name"
      pass V5 "the agent bearer, which holds no person scope, is refused $name: HTTP 403 $(jq -r '.code // ""' "$work/body"), so it reads nothing of $hidden"
      continue
    fi
    want_status 200 V5 "$(label "$reader") reads $name"
    reader_file="$work/surface.$index.reader.json"
    cp "$work/body" "$reader_file"
    got="$(leaks "$reader_file")" || fail V5 "read $(label "$reader")'s answer of $name" "unreadable: $(body)"
    [[ "$got" == "[]" ]] ||
      fail V5 "$(label "$reader")'s read of $name names no id and no words of $hidden" "the answer holds $got: $(head -c 1200 "$reader_file")"
    reader_counts="$(counts "$reader_file" "$arrays" "$scalars")" || fail V5 "count $(label "$reader")'s read of $name" "unreadable: $(body)"
    if [[ "$mode" == own ]]; then
      if [[ "$reader" == "cookie:$second" ]]; then
        cookie_counts="$reader_counts"
      else
        [[ "$reader_counts" == "$cookie_counts" ]] ||
          fail V5 "$(label "$reader")'s counts of $name are $second's cookie's" "the token counts $reader_counts, the cookie $cookie_counts"
      fi
      pass V5 "$(label "$reader")'s read of $name, which answers each person their own rows, names no id and no words of $hidden and counts $reader_counts"
      continue
    fi
    # Each count is the author's less what she reads of the hidden node: an array loses exactly the
    # items that name it, and a count the items of the array it counts.
    want="$(jq -cS --argjson s "$scalars" '
      . as $c
      | {arrays: (.arrays | with_entries(.value = {all: (.value.all - .value.hidden), hidden: 0})),
         scalars: (.scalars | with_entries(
           .value = (if .value == null or ($s[.key] // "") == "" then .value
             else .value - ($c.arrays[$s[.key]].hidden // 0) end)))}' <<<"$author_counts")"
    [[ "$reader_counts" == "$want" ]] ||
      fail V5 "$(label "$reader")'s counts of $name are $login's less what she reads of the hidden lens" \
        "it counts $reader_counts; $login counts $author_counts, so the counts unmoved by the hidden lens are $want"
    pass V5 "$(label "$reader")'s read of $name names no id and no words of $hidden, and its counts are $login's less what she reads of it: $reader_counts"
  done
done
# The whole event log is every project's and moves with every write anywhere, so its counts are no
# measure of the hidden node: its absence alone is read, beside the same read finding the first
# comment of the seeded issue.
with_vars V5 "read a comment of $key" "select coalesce((select id::text from comments where issue_key = :'key' order by created_at limit 1), '')" "key=$key"
public_comment="$answer"
[[ -n "$public_comment" ]] || fail V5 "the seeded issue $key holds a comment" "it holds none"
for reader in "cookie:$second" "token:$second" agent; do
  request "$reader" GET /api/v1/events/log
  want_status 200 V5 "$(label "$reader") reads the whole event log"
  got="$(leaks "$work/body")" || fail V5 "read $(label "$reader")'s event log" "unreadable: $(body)"
  [[ "$got" == "[]" ]] || fail V5 "$(label "$reader")'s read of the whole event log names no id and no words of $hidden" "the log holds $got"
  pass V5 "$(label "$reader")'s read of the whole event log names no id and no words of $hidden; it reads $(jq 'length' "$work/body") events, $(jq --arg c "$public_comment" '[.[] | select(tostring | contains($c))] | length' "$work/body") of them naming the public comment $public_comment"
done

# --- V6: an interaction the driver causes -----------------------------------------------------

send POST /api/v1/issues "$(jq -cn --arg p "$project" --argjson a "$(actor)" \
  '{project: $p, title: "visibility.sh: the issue a run of the command bar changes", spec: "An issue a comment under a run lands on.", force: true, actor: $a}')"
want_status 201 V6 "the agent bearer creates an issue of the run's own"
created="$(jq -er '.key' "$work/body")" || fail V6 "create an issue of the run's own" "no key: $(body)"
readonly run_words="Which run changed the comment, visibility.sh $stamp?"
request "cookie:$login" POST /api/v1/going/requests "$(jq -cn --arg p "$project" --arg w "$run_words" '{words: $w, project: $p}')"
want_status 201 V6 "$login asks the command bar one question"
run_id="$(jq -er '.id' "$work/body")" || fail V6 "ask the command bar one question" "no id: $(body)"
readonly run_id interaction="dispatch://interaction/$run_id"
# The comment must land inside the run's window, the only one the server admits the header for
# (withinGoingRun), so it is written straight after the run is asked.
request "cookie:$login" POST "/api/v1/issues/$created/comments" \
  "$(jq -cn --arg s "$stamp" '{body: "A comment written under a run of the command bar, \($s)."}')" "$run_id"
want_status 201 V6 "$login writes a comment on $created under her run, named in the Dispatch-Going-Run header"
changed="$(jq -er '.id' "$work/body")" || fail V6 "write the comment under the run" "no id: $(body)"
readonly changed
with_vars V6 "read the interaction node of $run_id" \
  "select coalesce((select json_build_object('visibility', visibility, 'author', made_by->>'id', 'supertag', supertag)
     from graph_nodes where kind = 'interaction' and id = :'id'), 'null')" "id=$run_id"
jq -e --arg l "$login" '.visibility == "author" and .author == $l and .supertag == "interaction"' <<<"$answer" >/dev/null ||
  fail V6 "the run $run_id arose as an interaction node reserved for $login" "the store holds $answer"
pass V6 "the run $run_id arose as an interaction node reserved for the credential that asked it: $answer"
with_vars V6 "read the changed_by_run tuples to $run_id" \
  "select coalesce(string_agg(from_kind || ':' || from_id, ',' order by from_kind, from_id), '') from
   $(current_sql "kind = 'changed_by_run' and to_kind = 'interaction'") t where t.to_id = :'id'" "id=$run_id"
[[ "$answer" == "comment:$changed" ]] ||
  fail V6 "the comment the run changed holds one changed_by_run tuple to $interaction" "the tuples into the run come from '$answer'"
pass V6 "the comment $changed holds one changed_by_run tuple to $interaction, the node of the run that changed it"
request "cookie:$login" GET "/api/v1/references?to=$(q "$interaction")"
want_status 200 V6 "$login reads $interaction"
expect V6 "$login's read of $interaction answers a node labelled interaction, one member of the closed union of GraphNode.kind, reserved for her" \
  '{"kind":"interaction","visibility":"author"}' '.node | {kind, visibility}'
expect V6 "$login's read of $interaction counts one backlink, the changed_by_run of the comment it changed" \
  "$(jq -cn --arg c "$changed" '[["changed_by_run", "comment", $c]]')" '[.edges[] | [.kind, .node.kind, .node.id]]'
request "cookie:$login" GET "/api/v1/references?from=$(q "dispatch://$created/comment/$changed")"
want_status 200 V6 "$login reads the tuples out of the comment $changed"
expect V6 "$login reads the changed_by_run tuple from the comment $changed to the run that changed it" \
  "$(jq -cn --arg r "$run_id" '[["changed_by_run", "interaction", $r]]')" \
  '[.edges[] | select(.kind == "changed_by_run") | [.kind, .node.kind, .node.id]]'
for reader in "token:$login" "${outsiders[@]}"; do
  like_nobody V6 "$(label "$reader")'s read of $interaction answers what an address nobody wrote answers: a run is read by the whole credential that asked it" \
    "$reader" "$interaction" "$run_id"
done

# --- V7: the seeded interaction nodes ---------------------------------------------------------

with_vars V7 "read the seeded interaction nodes" \
  "select coalesce(json_agg(json_build_object('id', n.id, 'kind', n.made_by->>'kind', 'author', n.made_by->>'id',
     'proven', coalesce((n.made_by->>'proven')::boolean, false)) order by n.id), '[]')
   from graph_nodes n where n.kind = 'interaction' and n.id <> :'run' and n.state <> 'gone'" "run=$run_id"
seeded="$answer"
by_person="$(jq -c --arg a "$login" --arg b "$second" '[.[] | select(.kind == "user" and .proven and (.author == $a or .author == $b))]' <<<"$seeded")"
by_nobody="$(jq -c '[.[] | select((.kind == "user" and .proven) or (.kind == "session" and .proven) | not)]' <<<"$seeded")"
[[ "$(jq 'length' <<<"$by_person")" -gt 0 ]] ||
  fail V7 "the corpus holds an interaction the credential of $login or $second wrote (bullet 20)" "it holds $seeded"
while IFS=$'\t' read -r id author; do
  other="$login"
  [[ "$author" != "$login" ]] || other="$second"
  request "cookie:$author" GET "/api/v1/references?to=$(q "dispatch://interaction/$id")"
  want_status 200 V7 "$author's cookie reads the seeded run $id"
  expect V7 "$author's cookie, the credential whose row the seeded run $id is, reads it" \
    "$(jq -cn --arg i "$id" '{id: $i, kind: "interaction"}')" '.node | {id, kind}'
  for reader in "cookie:$other" "token:$author" agent; do
    like_nobody V7 "$(label "$reader")'s read of the seeded run $id, which $author's cookie asked, answers what an address nobody wrote answers" \
      "$reader" "dispatch://interaction/$id" "$id"
  done
done < <(jq -r '.[] | [.id, .author] | @tsv' <<<"$by_person")
pass V7 "examined $(jq 'length' <<<"$by_person") seeded runs a person's credential wrote: $(jq -c '[.[].id]' <<<"$by_person")"
[[ "$(jq 'length' <<<"$by_nobody")" -gt 0 ]] ||
  fail V7 "a seeded interaction row whose author proves nobody reaches nobody" \
    "the corpus holds no such row: each of its $(jq 'length' <<<"$seeded") interaction nodes was written by a proven credential, $(jq -c '[.[] | {id, kind, author: ((.author // "") | sub("@.*$"; ""))}]' <<<"$seeded"), so the clause would read nobody over zero rows"
for id in $(jq -r '.[].id' <<<"$by_nobody"); do
  for reader in "cookie:$login" "token:$login" "${outsiders[@]}"; do
    like_nobody V7 "$(label "$reader")'s read of the seeded run $id, whose author proves nobody, answers what an address nobody wrote answers" \
      "$reader" "dispatch://interaction/$id" "$id"
  done
done
pass V7 "examined $(jq 'length' <<<"$by_nobody") seeded runs whose author proves nobody, each reaching nobody"

pass "$driver" complete
