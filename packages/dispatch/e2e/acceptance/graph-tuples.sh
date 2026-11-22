#!/usr/bin/env bash
# Drives check c-tuples (spec requirements r-person-node, r-mentions, r-issue-edges and
# r-verification-edges of dispatch://INFRA-1098) against the `bun run dispatch:local` server, over
# the seeded corpus (lib-corpus.sh, which seeds it once where the database lacks it). It reads each
# relation of the corpus from the tuple store, and the projection beside it, through DATABASE_URL
# and the server's own reads.
#
#   T1  The identity table this driver carries (below), written here and read from no writer: for
#       each login, raw actor id, session and role holder, the one node it must resolve to. One
#       login is written in two letter cases (dana, written as "dana" and as "Dana") and must be
#       one person; two pairs of identities must stay apart (alice and bob; the two sessions that
#       held the role corpus-handover in turn). Each person is a node, and each comment the corpus
#       wrote as one of those identities points at that node through its authored_by, read against
#       the table rather than against either writer: one canonicalisation shared by the tuple
#       writer and the projection writer that sent a write to the wrong person would read zero
#       differences between the two and fail here.
#   T2  The files that match a person with author->>'id', enumerated rather than counted, each
#       printed with the reader it holds and whether that reader lowercases, so a file added later
#       is named rather than silently making a count wrong. The two lowercasing readers,
#       api/unread_replies.go and api/replies_agent.go, are among them.
#   T3  An issue's child_of, needs, duplicate_of and filed_from are tuples: issues.parent_key equals
#       the child_of tuples and issue_links (kind blocked_by) the needs tuples between issues, each
#       census read both ways and counted; and the run writes one issue duplicate of another and one
#       filed from a comment, on issues of its own, and reads each relation as a tuple and back
#       through the issue read.
#   T4  The Verification view and the coverage read answer from the serves and part_of tuples: the
#       count the coverage read gives each requirement of the seeded issue is the number of checks
#       whose serves tuple reaches it, for one requirement two checks serve and for each of the
#       three one check serves, and the Verification view answers exactly the serves tuples between
#       its checks and requirements. A spec and a Verification document of more than 2,000 blocks,
#       which the write leaves to the block index's reconciliation (deferBlockIndexBlocks in
#       internal/dispatch/docs), are written on an issue of the run's own: before the index reaches
#       them, the coverage read lists their requirement and the check that serves it, and drops
#       neither.
#   T5  A mention: the count the issue read gives the seeded issue's backlinks equals the rows the
#       references read lists for it; every comment_mentions row is a mentions tuple from the
#       comment's node to the person, session or role it points at; and every destination in an
#       event's published_destinations is a delivered_to tuple behind it.
#   T6  A delivery: comment_deliveries and message_deliveries each equal the delivery_attempt nodes
#       and their tuples, and the recipient of each delivery the listener took is the node the
#       identity table resolves its target to.
#
# Inputs: DATABASE_URL (the database the server at DISPATCH_URL serves, on this machine: the corpus's
# loader writes it and psql reads it), DISPATCH_URL (the server), DISPATCH_TOKEN or
# DISPATCH_TOKEN_FILE (an agent bearer, read here and never printed) and PROJECT (the project the
# corpus is seeded into, CORE). Each unmet input is exit 2. The issues T3 and T4 write are the
# run's own, closed when it exits, also when a check fails; none of them is a row the loader's
# self-check counts.
#
# Each passing check prints `PASS: <section> <what>`; the first failing one prints
# `FAIL: <section> <what>: <observed>` and the script exits 1. The last line is
# `PASS: graph-tuples.sh complete`. A person is printed by login, never by an email address.
#
# Every check is `cmd || fail …`, never part of an `&&` list: under `set -e` a failing command
# anywhere but last in such a list does not stop the script.

# shellcheck disable=SC2016 # the single-quoted jq filters and SQL name jq and psql variables, not shell ones
set -euo pipefail
set +x

usage() {
  cat >&2 <<'USAGE'
Usage: DATABASE_URL=postgres://…@127.0.0.1:5432/<db>?sslmode=disable DISPATCH_URL=http://127.0.0.1:8777 \
         DISPATCH_TOKEN=e2e-token PROJECT=CORE graph-tuples.sh
       (DISPATCH_TOKEN_FILE=<file holding the bearer> in place of DISPATCH_TOKEN; DATABASE_URL names
       the database the server at DISPATCH_URL serves, on this machine; run from a checkout, whose
       files T2 reads with git grep)
USAGE
}

# shellcheck source-path=SCRIPTDIR source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
# shellcheck source-path=SCRIPTDIR source=lib-corpus.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-corpus.sh"
require_env DATABASE_URL DISPATCH_URL PROJECT
read_bearer
require_commands awk curl git grep jq psql sort

readonly driver=graph-tuples.sh
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
checkout="$(git -C "$(dirname -- "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)" || {
  printf 'cannot find the checkout %s is part of: T2 reads its files\n' "$(dirname -- "${BASH_SOURCE[0]}")" >&2
  exit 2
}
readonly checkout
stamp="$(date +%s)-$$"
readonly stamp
readonly session="graph-tuples-$stamp"

# The identity table: each spelling a record carries of someone, the kind of spelling it is, and
# the one node it must resolve to, `kind:id`. A login is what a person signs in as and a raw actor
# id what a record stores of one; a session is an agent's run, which resolves to its own node and
# to no person, since the server proved none of these sessions a person's; and a role holder is the
# session a role's held_by names now. The four harness sessions are the ones of e2e/session-ids.ts
# that the harness's own seed (e2e/workspace.ts) writes as or delivers to (the planner, the tester,
# the reviewer and the observer); the corpus's are the ones seedcorpus names (seedcorpus.go).
readonly identities='
login	alice	person:alice
login	bob	person:bob
login	dana	person:dana
login	Dana	person:dana
actor	{"kind":"user","id":"alice"}	person:alice
actor	{"kind":"user","id":"bob"}	person:bob
actor	{"kind":"user","id":"dana"}	person:dana
actor	{"kind":"user","id":"Dana"}	person:dana
session	ses_corpus_agent	session:ses_corpus_agent
session	ses_corpus_handover_first	session:ses_corpus_handover_first
session	ses_corpus_handover_second	session:ses_corpus_handover_second
session	019a1c01-0d7b-7a2a-8e93-4a5b6c7d8e01	session:019a1c01-0d7b-7a2a-8e93-4a5b6c7d8e01
session	019a1c02-1e9c-7b3b-9f74-3b6c0d3e4f02	session:019a1c02-1e9c-7b3b-9f74-3b6c0d3e4f02
session	019a1c03-2f0d-7c4c-ab85-0c7d1e4f5a03	session:019a1c03-2f0d-7c4c-ab85-0c7d1e4f5a03
session	019a1c04-3a1e-7b5d-9c06-1d8e2f5a6b04	session:019a1c04-3a1e-7b5d-9c06-1d8e2f5a6b04
session	ses_corpus_recorder	session:ses_corpus_recorder
holder	corpus-agent	session:ses_corpus_agent
holder	corpus-handover	session:ses_corpus_handover_second
'
# The pairs that must stay apart: each resolves to a node of its own.
readonly apart='
login	alice	login	bob
session	ses_corpus_handover_first	session	ses_corpus_handover_second
'

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

# resolve KIND SPELLING: the node the identity table resolves the spelling to, "" for none.
resolve() {
  awk -F '\t' -v k="$1" -v s="$2" '$1 == k && $2 == s { print $3; exit }' <<<"$identities"
}

# login_of TEXT: TEXT with every email address reduced to its login, so no line prints one.
login_of() { sed -E 's/([A-Za-z0-9._%+-]+)@[A-Za-z0-9.-]+\.[A-Za-z]{2,}/\1/g' <<<"$1"; }

# current_sql FILTER: the tuples the store asserts now among those FILTER keeps, the newest
# statement of each triple.
current_sql() {
  printf "(select * from (select distinct on (from_kind, from_id, kind, to_kind, to_id) * from graph_tuples
     where %s order by from_kind, from_id, kind, to_kind, to_id, seq desc) latest where asserted)" "$1"
}

# census SECTION WHAT ROWS TUPLES: ROWS and TUPLES are each a select of one text column, the
# projection's rows and the tuples behind them as one comparable line each; the census counts both
# sides and the lines each holds that the other lacks, refuses zero lines on either side, prints
# every difference, and fails on any.
census() {
  local section="$1" what="$2"
  query "$section" "census $what" "with rows as ($3), tuples as ($4)
    select json_build_object('rows', (select count(*) from rows), 'tuples', (select count(*) from tuples),
      'rows_without_a_tuple', coalesce((select json_agg(r) from (select * from rows except select * from tuples) r), '[]'),
      'tuples_without_a_row', coalesce((select json_agg(t) from (select * from tuples except select * from rows) t), '[]'))"
  local read
  read="$(login_of "$answer")"
  jq -e '.rows > 0 and .tuples > 0' <<<"$answer" >/dev/null ||
    fail "$section" "$what: the census examines at least one row and one tuple" "it examined $read"
  jq -e '(.rows_without_a_tuple | length) == 0 and (.tuples_without_a_row | length) == 0' <<<"$answer" >/dev/null ||
    fail "$section" "$what" "the census read $read"
  pass "$section" "$what: $(jq -c '{rows, tuples, differences: ((.rows_without_a_tuple | length) + (.tuples_without_a_row | length))}' <<<"$answer")"
}

# --- setup --------------------------------------------------------------------------------------

corpus_ensure setup
key="$corpus_issue"
readonly key

# --- T1: the identity table ---------------------------------------------------------------------

identity_rows="$(grep -c . <<<"$identities")"
pass T1 "the identity table holds $identity_rows spellings of $(awk -F '\t' 'NF == 3 { print $3 }' <<<"$identities" | sort -u | grep -c .) nodes, written in this driver"
# Each node the table names is one node of the store, and each person one node whatever case its
# login is written in.
while IFS= read -r node; do
  with_vars T1 "read the node $node" "select count(*) from graph_nodes where kind = :'kind' and id = :'id'" \
    "kind=${node%%:*}" "id=${node#*:}"
  [[ "$answer" == 1 ]] || fail T1 "the store holds the node $(login_of "$node") the identity table names" "it holds $answer"
done < <(awk -F '\t' 'NF == 3 { print $3 }' <<<"$identities" | sort -u)
pass T1 "the store holds each node the identity table names, once"
with_vars T1 "count the person nodes of each login of the table in any case" \
  "select coalesce(json_object_agg(l, n), '{}') from (select lower(t.l) as l, count(distinct p.id) as n
     from unnest(string_to_array(:'logins', ',')) t(l) left join graph_nodes p on p.kind = 'person' and lower(p.id) = lower(t.l)
     group by lower(t.l)) c" "logins=$(awk -F '\t' '$1 == "login" { print $2 }' <<<"$identities" | paste -sd, -)"
jq -e 'length > 0 and all(.[]; . == 1)' <<<"$answer" >/dev/null ||
  fail T1 "each login of the table, in any letter case, is one person node" "the store counts $answer"
pass T1 "each login of the table, in any letter case, is one person node: $answer"
# The login written in two letter cases: each of dana's two comments points at the one person.
for spelling in dana Dana; do
  want="$(resolve login "$spelling")"
  with_vars T1 "read the authored_by of the comment written as $spelling" \
    "select coalesce(string_agg(t.to_kind || ':' || t.to_id, ',' order by t.to_kind, t.to_id), '') from comments c
       join $(current_sql "from_kind = 'comment' and kind = 'authored_by'") t on t.from_id = c.id::text
     where c.issue_key = :'key' and c.body = 'Dana''s note, written as ' || :'spelling' || '.'" "key=$key" "spelling=$spelling"
  [[ "$answer" == "$want" ]] ||
    fail T1 "the comment written as $spelling points at $want, the node the identity table resolves $spelling to" "its authored_by reaches '$(login_of "$answer")'"
  pass T1 "the comment written as $spelling points at $want through its authored_by, as the identity table resolves it"
done
# The two pairs that must stay apart.
while IFS=$'\t' read -r kind_a spelling_a kind_b spelling_b; do
  [[ -n "$kind_a" ]] || continue
  node_a="$(resolve "$kind_a" "$spelling_a")" node_b="$(resolve "$kind_b" "$spelling_b")"
  [[ -n "$node_a" && -n "$node_b" && "$node_a" != "$node_b" ]] ||
    fail T1 "$spelling_a and $spelling_b stay apart in the identity table" "they resolve to '$node_a' and '$node_b'"
  pass T1 "$spelling_a and $spelling_b stay apart: $node_a and $node_b"
done <<<"$apart"
# Each role holder of the table: the session the role's held_by names now is the one the table
# names, and a role whose holder changed keeps the first holder apart, retracted.
while IFS=$'\t' read -r kind role want; do
  [[ "$kind" == holder ]] || continue
  with_vars T1 "read the holder of the role $role" \
    "select coalesce(string_agg(to_kind || ':' || to_id, ',' order by to_id), '') from
     $(current_sql "from_kind = 'role' and kind = 'held_by'") t where t.from_id = :'role'" "role=$role"
  [[ "$answer" == "$want" ]] || fail T1 "the role $role is held by $want, as the identity table names" "its held_by names '$answer'"
  pass T1 "the role $role is held by $want, as the identity table names"
done <<<"$identities"
# Every comment written as an identity of the table points, through its authored_by, at the node
# the table resolves its stored author to, and at no other; a comment of an author the table does
# not hold is counted and left to the census of the store.
query T1 "read each comment's stored author and the far end of its authored_by" \
  "select coalesce(json_agg(json_build_object('id', c.id, 'author', jsonb_build_object('kind', c.author->>'kind', 'id', c.author->>'id'),
     'tuples', (select coalesce(json_agg(t.to_kind || ':' || t.to_id order by t.to_kind, t.to_id), '[]') from
       $(current_sql "from_kind = 'comment' and kind = 'authored_by'") t where t.from_id = c.id::text))), '[]')
   from comments c"
printf '%s\n' "$answer" >"$work/authors.json"
examined=0 outside=0
while IFS=$'\t' read -r id author tuples; do
  case "$(jq -r '.kind' <<<"$author")" in
    user) want="$(resolve actor "$(jq -c '{kind, id}' <<<"$author")")" ;;
    session) want="$(resolve session "$(jq -r '.id' <<<"$author")")" ;;
    *) want="" ;;
  esac
  if [[ -z "$want" ]]; then
    outside=$((outside + 1))
    continue
  fi
  examined=$((examined + 1))
  [[ "$tuples" == "[\"$want\"]" ]] ||
    fail T1 "the comment $id, stored as written by $(login_of "$author"), points through its authored_by at $want alone, the node the identity table resolves its author to" \
      "its authored_by reaches $(login_of "$tuples")"
done < <(jq -r '.[] | [.id, (.author | tostring), (.tuples | tostring)] | @tsv' "$work/authors.json")
[[ "$examined" -gt 0 ]] || fail T1 "every comment written as an identity of the table points at the node it resolves to" "it examined 0 comments"
pass T1 "every comment written as an identity of the table points through its authored_by at the node the table resolves its author to: examined $examined comments; $outside comments of authors the table does not hold were left to the census"

# --- T2: the readers that match a person with author->>'id' -----------------------------------

git -C "$checkout" grep -n --no-color "author->>'id'" -- packages/envoy ':!*_test.go' >"$work/readers.txt" ||
  fail T2 "enumerate the files that match a person with author->>'id'" "git grep found none in $checkout"
files="$(awk -F: '{ print $1 }' "$work/readers.txt" | sort -u)"
count="$(grep -c . <<<"$files")"
while IFS= read -r file; do
  lines="$(awk -F: -v f="$file" '$1 == f { $1 = ""; sub(/^ /, ""); print }' "$work/readers.txt")"
  lowered="as stored"
  ! grep -q "lower([^)]*author->>'id')" <<<"$lines" || lowered="lowercased"
  reader="$(head -n 1 <<<"$lines" | awk '{ $1 = ""; sub(/^ +/, ""); print }' | tr -s '[:space:]' ' ' | cut -c1-160)"
  pass T2 "$file matches a person with author->>'id' ($lowered, $(grep -c . <<<"$lines") lines), first at line $(head -n 1 <<<"$lines" | awk '{ print $1 }'): $reader"
done <<<"$files"
for file in packages/envoy/internal/dispatch/api/unread_replies.go packages/envoy/internal/dispatch/api/replies_agent.go; do
  grep -q "^$file:.*lower([^)]*author->>'id')" "$work/readers.txt" ||
    fail T2 "$file is one of the lowercasing readers" "the enumeration finds it holding no lower(author->>'id')"
done
pass T2 "enumerated $count files that match a person with author->>'id', each named above; api/unread_replies.go and api/replies_agent.go lowercase"

# --- T3: an issue's edges are tuples ------------------------------------------------------------

census T3 "issues.parent_key equals the child_of tuples between issues" \
  "select key || ' child_of ' || parent_key from issues where parent_key is not null" \
  "select t.from_id || ' child_of ' || t.to_id from $(current_sql "from_kind = 'issue' and kind = 'child_of' and to_kind = 'issue'") t"
census T3 "issue_links (blocked_by) equals the needs tuples between issues" \
  "select issue_key || ' needs ' || target_key from issue_links where kind = 'blocked_by'" \
  "select t.from_id || ' needs ' || t.to_id from $(current_sql "from_kind = 'issue' and kind = 'needs' and to_kind = 'issue'") t"
# duplicate_of and filed_from have no projection: each is a tuple, which the issue read answers.
send POST /api/v1/issues "$(jq -cn --arg p "$project" --argjson a "$(actor)" \
  '{project: $p, title: "graph-tuples.sh: the issue another duplicates", spec: "The original.", force: true, actor: $a}')"
want_status 201 T3 "create the issue another duplicates"
original="$(jq -er '.key' "$work/body")" || fail T3 "create the issue another duplicates" "no key: $(body)"
keys+=("$original")
send POST "/api/v1/issues/$original/comments" "$(jq -cn --argjson a "$(actor)" '{body: "The comment an issue is filed from.", actor: $a}')"
want_status 201 T3 "write the comment an issue is filed from"
comment_id="$(jq -er '.id' "$work/body")" || fail T3 "write the comment an issue is filed from" "no id: $(body)"
readonly comment_ref="dispatch://$original/comment/$comment_id"
send POST /api/v1/issues "$(jq -cn --arg p "$project" --arg d "$original" --arg f "$comment_ref" --argjson a "$(actor)" \
  '{project: $p, title: "graph-tuples.sh: a duplicate filed from a comment", spec: "The duplicate.", force: true,
    duplicate_of: $d, filed_from: $f, actor: $a}')"
want_status 201 T3 "create an issue duplicate of $original, filed from $comment_ref"
duplicate="$(jq -er '.key' "$work/body")" || fail T3 "create the duplicate" "no key: $(body)"
keys+=("$duplicate")
with_vars T3 "read the duplicate_of and filed_from tuples of $duplicate" \
  "select coalesce(string_agg(kind || ' ' || to_kind || ':' || to_id, ',' order by kind), '') from
   $(current_sql "from_kind = 'issue' and kind in ('duplicate_of', 'filed_from')") t where t.from_id = :'key'" "key=$duplicate"
[[ "$answer" == "duplicate_of issue:$original,filed_from comment:$comment_id" ]] ||
  fail T3 "$duplicate holds a duplicate_of tuple to $original and a filed_from tuple to the comment $comment_id" "it holds '$answer'"
pass T3 "$duplicate holds a duplicate_of tuple to $original and a filed_from tuple to the comment $comment_id: $answer"
send GET "/api/v1/issues/$duplicate"
want_status 200 T3 "read $duplicate"
expect T3 "the issue read answers the duplicate_of and the filed_from its tuples hold" \
  "$(jq -cnS --arg d "$original" --arg f "$comment_ref" '{duplicate_of: $d, filed_from: $f}')" '{duplicate_of, filed_from}'

# --- T4: the Verification view and the coverage read answer from the tuples --------------------

# The checks whose current serves tuple reaches each requirement of the seeded issue, by block id.
with_vars T4 "read the checks serving each requirement of $key from the tuples" \
  "with reqs as (select n.id from graph_nodes n where n.kind = 'block' and n.state <> 'gone' and n.id like :'key' || '/tree#%'
       and exists (select 1 from graph_tuples x where x.from_kind = 'block' and x.from_id = n.id and x.kind = 'text'
         and x.seq = (select max(y.seq) from graph_tuples y where y.from_kind = 'block' and y.from_id = n.id and y.kind = 'text')
         and x.to_id like ':::requirement{%')),
     checks as (select n.id from graph_nodes n where n.kind = 'block' and n.state <> 'gone' and n.id like :'key' || '/tree#%'
       and exists (select 1 from graph_tuples x where x.from_kind = 'block' and x.from_id = n.id and x.kind = 'text'
         and x.seq = (select max(y.seq) from graph_tuples y where y.from_kind = 'block' and y.from_id = n.id and y.kind = 'text')
         and x.to_id like ':::check{%'))
   select coalesce(json_object_agg(split_part(r.id, '#', 2), (select count(*) from checks c
       join $(current_sql "from_kind = 'block' and kind = 'serves' and to_kind = 'block'") s on s.from_id = c.id where s.to_id = r.id)), '{}')
   from reqs r" "key=$key"
from_tuples="$answer"
send GET "/api/v1/issues/$key/coverage"
want_status 200 T4 "read the coverage of $key"
from_read="$(jq -cS '[.requirements[] | {(.block_id): .checks}] | add // {}' "$work/body")"
[[ "$(jq -cS . <<<"$from_tuples")" == "$from_read" ]] ||
  fail T4 "the coverage read counts, for each requirement of $key, the checks whose serves tuple reaches it" \
    "the read counts $from_read; the tuples count $from_tuples"
jq -e '[.[] | select(. == 2)] | length > 0' <<<"$from_read" >/dev/null ||
  fail T4 "one requirement of $key is served by two checks (bullet 6)" "the coverage counts $from_read"
pass T4 "the coverage read counts, for each requirement of $key, the checks whose serves tuple reaches it: $from_read, one requirement served by two checks"
with_vars T4 "read the requirements k-three serves" \
  "select coalesce(string_agg(split_part(s.to_id, '#', 2), ',' order by s.to_id), '') from
   $(current_sql "from_kind = 'block' and kind = 'serves' and to_kind = 'block'") s where s.from_id = :'key' || '/tree#k-three'" "key=$key"
served="$answer"
[[ "$served" =~ ^[^,]+,[^,]+,[^,]+$ ]] || fail T4 "the check k-three serves three requirements (bullet 6)" "it serves '$served'"
for requirement in ${served//,/ }; do
  jq -e --arg r "$requirement" '.[$r] >= 1' <<<"$from_read" >/dev/null ||
    fail T4 "the coverage read counts k-three among the checks of $requirement" "it counts $from_read"
done
pass T4 "the check k-three serves three requirements, $served, and the coverage read counts it under each: $(jq -c --arg s "$served" '[to_entries[] | select(.key as $k | $s | split(",") | index($k))] | from_entries' <<<"$from_read")"
send GET "/api/v1/issues/$key/tree?view=verification"
want_status 200 T4 "read the Verification view of $key"
from_view="$(jq -c '[.tuples[] | select(.kind == "serves" and .from.kind == "block" and .to.kind == "block") | "\(.from.id) serves \(.to.id)"] | sort' "$work/body")"
with_vars T4 "read the serves tuples from the checks of $key to its requirements" \
  "select coalesce(json_agg(s.from_id || ' serves ' || s.to_id order by s.from_id || ' serves ' || s.to_id), '[]') from
   $(current_sql "from_kind = 'block' and kind = 'serves' and to_kind = 'block'") s
   where s.from_id like :'key' || '/tree#%' and s.to_id like :'key' || '/tree#%'
     and exists (select 1 from graph_tuples x where x.from_kind = 'block' and x.from_id = s.from_id and x.kind = 'text'
       and x.seq = (select max(y.seq) from graph_tuples y where y.from_kind = 'block' and y.from_id = s.from_id and y.kind = 'text')
       and x.to_id like ':::check{%')" "key=$key"
[[ "$(jq -cS . <<<"$answer")" == "$(jq -cS . <<<"$from_view")" ]] ||
  fail T4 "the Verification view of $key answers exactly the serves tuples from its checks to its requirements" \
    "the view answers $from_view; the store holds $answer"
pass T4 "the Verification view of $key answers exactly the $(jq 'length' <<<"$answer") serves tuples from its checks to its requirements"

# The block index has not reached a document of more than 2,000 blocks when its write answers: the
# write leaves it to the reconciliation, every 30 s. Each paragraph of the padding is one block. A
# check's serves must point at a block a document holds when it is written, so the issue's spec is
# written first with one requirement, r-early, which its own small write indexes; then the
# Verification document, k-late serving r-early with the padding, which the index has not reached;
# then the spec again, r-early, a second requirement r-late and the padding, which it has not
# reached either.
padding="$(for ((n = 1; n <= 2001; n++)); do printf 'Pad %d.\n\n' "$n"; done)"
readonly early=':::requirement{#r-early}
The requirement a check the block index has not reached serves.
:::
'
send POST /api/v1/issues "$(jq -cn --arg p "$project" --arg s "$early" --argjson a "$(actor)" \
  '{project: $p, title: "graph-tuples.sh: documents the block index has not reached", spec: $s, force: true, actor: $a}')"
want_status 201 T4 "create an issue of the run's own"
deferred="$(jq -er '.key' "$work/body")" || fail T4 "create an issue of the run's own" "no key: $(body)"
keys+=("$deferred")
spec_id="$(jq -er '.primary_artifact_id' "$work/body")" || fail T4 "read the spec of $deferred" "no primary_artifact_id: $(body)"
send GET "/api/v1/artifacts/$spec_id"
want_status 200 T4 "read the spec of $deferred"
spec_name="$(jq -er '.name' "$work/body")" || fail T4 "read the name of the spec of $deferred" "none: $(body)"
send POST "/api/v1/issues/$deferred/artifacts" "$(jq -cn --arg c ":::check{#k-late serves=\"dispatch://$deferred/spec#b-r-early\"}
The check the block index has not reached.
:::

$padding" --argjson a "$(actor)" '{name: "verification.md", content: $c, actor: $a}')"
[[ "$status" == 200 || "$status" == 201 ]] || fail T4 "write the Verification document of $deferred, of more than 2,000 blocks" "HTTP $status: $(body)"
send POST "/api/v1/issues/$deferred/artifacts" "$(jq -cn --arg c "$early
:::requirement{#r-late}
The requirement the block index has not reached.
:::

$padding" --arg n "$spec_name" --argjson a "$(actor)" '{name: $n, content: $c, actor: $a}')"
[[ "$status" == 200 || "$status" == 201 ]] || fail T4 "write the spec of $deferred again, of more than 2,000 blocks" "HTTP $status: $(body)"
with_vars T4 "read whether the block index has reached the documents of $deferred" \
  "select coalesce(string_agg(a.slug || '=' || coalesce(d.version::text, 'owed'), ',' order by a.slug), '') from document_block_versions d
     join artifacts a on a.id = d.artifact_id where a.issue_key = :'key'" "key=$deferred"
owed="$answer"
send GET "/api/v1/issues/$deferred/coverage"
want_status 200 T4 "read the coverage of $deferred"
cp "$work/body" "$work/deferred-coverage.json"
[[ "$owed" == *spec=owed* && "$owed" == *verification-md=owed* ]] ||
  fail T4 "the block index has not reached the spec and the Verification document of $deferred when the coverage read answers" \
    "it reached one first ($owed), so this run proves nothing of a block it has not reached: run it again"
jq -e '[.requirements[] | select(.block_id == "r-early" and .checks == 1)] | length == 1' "$work/deferred-coverage.json" >/dev/null ||
  fail T4 "the coverage read counts the check k-late, which the block index has not reached ($owed), under r-early" \
    "it answers $(jq -c '{requirements: [.requirements[] | {block_id, checks}], gaps}' "$work/deferred-coverage.json")"
jq -e '[.requirements[] | select(.block_id == "r-late")] | length == 1' "$work/deferred-coverage.json" >/dev/null ||
  fail T4 "the coverage read lists the requirement r-late, which the block index has not reached ($owed)" \
    "it answers $(jq -c '{requirements: [.requirements[] | {block_id, checks}], gaps}' "$work/deferred-coverage.json")"
pass T4 "before the block index reaches them ($owed), the coverage read lists the requirement r-late and counts the check k-late under r-early, and drops neither"

# --- T5: a mention is a tuple -------------------------------------------------------------------

send GET "/api/v1/issues/$key"
want_status 200 T5 "read $key"
counted="$(jq -er '.referenced_by_count' "$work/body")" || fail T5 "read the backlink count of $key" "none: $(body)"
send GET "/api/v1/references?to=$(q "dispatch://$key")"
want_status 200 T5 "read the references to $key"
listed="$(jq '.edges | length' "$work/body")"
[[ "$counted" -gt 0 && "$counted" == "$listed" ]] ||
  fail T5 "the count the issue read gives $key equals the rows the references read lists for it" "it counts $counted and lists $listed"
pass T5 "the count the issue read gives $key equals the rows the references read lists for it: $counted"
census T5 "comment_mentions equals the mentions tuples from each comment to the session, role or person it points at" \
  "select comment_id::text || ' mentions ' || replace(target, 'user:', 'person:') from comment_mentions" \
  "select t.from_id || ' mentions ' || t.to_kind || ':' || t.to_id from
     $(current_sql "from_kind = 'comment' and kind = 'mentions' and to_kind in ('session', 'role', 'person')") t"
# A destination the bus reached is a session's topic or a role's (contracts.AgentTopicPrefix,
# RoleTopicPrefix), and the node behind it the session or the role; each is held to a delivered_to
# tuple from the item the event names to that node.
query T5 "read each destination of events.published_destinations, with the delivered_to tuple behind it" \
  "with reached as (select e.id, e.payload->>'id' as item,
       case when d like 'notifications.agent.%' then 'session:' || substr(d, 21)
            when d like 'notifications.role.%' then 'role:' || substr(d, 20) end as node
     from events e cross join lateral unnest(e.published_destinations) d where coalesce(e.payload->>'id', '') <> '')
   select json_build_object('examined', count(*), 'without_a_tuple', coalesce(json_agg(r.id || ' ' || r.item || ' ' || r.node)
     filter (where not exists (select 1 from $(current_sql "kind = 'delivered_to'") t
       where t.from_id = r.item and t.to_kind || ':' || t.to_id = r.node)), '[]'))
   from reached r where r.node is not null"
jq -e '.examined > 0' <<<"$answer" >/dev/null ||
  fail T5 "each destination of events.published_destinations is a delivered_to tuple behind it" \
    "it examined $answer: no event of this database holds a destination the bus reached"
jq -e '(.without_a_tuple | length) == 0' <<<"$answer" >/dev/null ||
  fail T5 "each destination of events.published_destinations is a delivered_to tuple behind it" "the read finds $answer"
pass T5 "each destination of events.published_destinations is a delivered_to tuple behind it: $answer"

# --- T6: a delivery is a delivery_attempt node --------------------------------------------------

census T6 "comment_deliveries equals the delivery_attempt nodes of comments" \
  "select 'comment/' || comment_id::text || '/' || target || '/' || attempt from comment_deliveries" \
  "select id from graph_nodes where kind = 'delivery_attempt' and id like 'comment/%'"
census T6 "message_deliveries equals the delivery_attempt nodes of messages" \
  "select 'message/' || message_id::text || '/' || attempt from message_deliveries" \
  "select id from graph_nodes where kind = 'delivery_attempt' and id like 'message/%'"
query T6 "count the delivery_attempt nodes that state no tuple" \
  "select json_build_object('nodes', count(*), 'untied', count(*) filter (where not exists (select 1 from graph_tuples t
     where t.from_kind = 'delivery_attempt' and t.from_id = n.id and t.asserted))) from graph_nodes n where n.kind = 'delivery_attempt'"
jq -e '.nodes > 0 and .untied == 0' <<<"$answer" >/dev/null ||
  fail T6 "each delivery_attempt node states its tuples" "the store counts $answer"
pass T6 "each delivery_attempt node states its tuples: $answer"
query T6 "read each delivery attempt the listener took, with its target and the delivered_to tuples of its item" \
  "select coalesce(json_agg(d), '[]') from (
     select 'comment' as item, c.comment_id::text as id, c.target, c.session_id,
       (select coalesce(json_agg(t.to_kind || ':' || t.to_id), '[]') from $(current_sql "from_kind = 'comment' and kind = 'delivered_to'") t
         where t.from_id = c.comment_id::text) as reached
     from comment_deliveries c where c.state in ('sent', 'queued')
     union all
     select 'message', m.message_id::text, 'session:' || coalesce(m.session_id, ''), m.session_id,
       (select coalesce(json_agg(t.to_kind || ':' || t.to_id), '[]') from $(current_sql "from_kind = 'message' and kind = 'delivered_to'") t
         where t.from_id = m.message_id::text)
     from message_deliveries m where m.state in ('sent', 'queued')) d"
printf '%s\n' "$answer" >"$work/deliveries.json"
examined=0
while IFS=$'\t' read -r item id target reached; do
  examined=$((examined + 1))
  case "$target" in
    session:*) want="$(resolve session "${target#session:}")" ;;
    role:*) want="role:${target#role:}" ;;
    *) want="" ;;
  esac
  [[ -n "$want" ]] || fail T6 "the identity table resolves the target $target of the $item $id" "it holds no such spelling"
  jq -e --arg w "$want" 'index($w) != null' <<<"$reached" >/dev/null ||
    fail T6 "the $item $id, delivered to $target, holds a delivered_to tuple to $want, the node the identity table resolves its target to" \
      "its delivered_to tuples reach $(login_of "$reached")"
done < <(jq -r '.[] | [.item, .id, .target, (.reached | tostring)] | @tsv' "$work/deliveries.json")
[[ "$examined" -gt 0 ]] || fail T6 "each delivery the listener took reaches the node the identity table resolves its target to" "it examined 0 deliveries"
pass T6 "each of the $examined deliveries the listener took reaches the node the identity table resolves its target to"

pass "$driver" complete
