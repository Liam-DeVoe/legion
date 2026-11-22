#!/usr/bin/env bash
# Drives the reply's pointers of check c-reply-answers (spec requirement r-reply-answers of
# dispatch://INFRA-1098) against the `bun run dispatch:local` server, over the seeded corpus
# (lib-corpus.sh, which seeds it once where the database lacks it), whose comments and messages
# the censuses read beside the thread the run writes on an issue of its own.
#
#   A1  A reply written against a comment that is not the head of its thread, the thread's first
#       reply, comes back pointing at that comment (answers) beside the thread head (reply_to).
#   A2  The write appended one tuple per pointer, each read from graph_tuples: answers to the
#       comment it answered and replies_to to the thread head, out of the reply's own node.
#   A3  Each column equals the tuple behind it: comments.answers, comments.reply_to,
#       comments.ask_id and messages.in_reply_to, each census counting the rows it examined and
#       reading zero differences.
#   A4  A reply that also points at a block of the comment it answers keeps both pointers, each a
#       column and a tuple.
#   A5  The driver retracts one tuple, the answers of A1's reply, in one transaction with the read
#       of its projection, and reads comments.answers follow it there; every thread read of the
#       issue then returns the entries it returned before, because each reads the head.
#
# Inputs: DATABASE_URL (the database the server at DISPATCH_URL serves, on this machine: the
# corpus's loader writes it and psql reads it and writes A5's retraction), DISPATCH_URL (the
# server), DISPATCH_TOKEN or DISPATCH_TOKEN_FILE (an agent bearer, read here and never printed) and
# PROJECT (the project the corpus is seeded into, CORE). Each unmet input is exit 2. Every route
# write is the agent bearer's, acting for a session of this run's own, on an issue it creates and
# closes when it exits, also when a check fails: a reply in a thread of the seeded issue would join
# a thread a seeded person wrote in, and change the replies and the unread rows the loader's
# self-check counts. No route ends a comment's pointer, so A5 appends the retraction to
# graph_tuples as the store's writer states one, as the test of the pointers' tuples does (api's
# retractCommentPointer on dispatch://INFRA-1152's head).
#
# Each passing check prints `PASS: <section> <what>`; the first failing one prints
# `FAIL: <section> <what>: <observed>` and the script exits 1. The last line is
# `PASS: reply-answers.sh complete`.
#
# Every check is `cmd || fail …`, never part of an `&&` list: under `set -e` a failing command
# anywhere but last in such a list does not stop the script.

# shellcheck disable=SC2016 # the single-quoted jq filters and SQL name jq variables, not shell ones
set -euo pipefail
set +x

usage() {
  cat >&2 <<'USAGE'
Usage: DATABASE_URL=postgres://…@127.0.0.1:5432/<db>?sslmode=disable DISPATCH_URL=http://127.0.0.1:8777 \
         DISPATCH_TOKEN=e2e-token PROJECT=CORE reply-answers.sh
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
require_commands cmp curl jq psql

readonly driver=reply-answers.sh
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
readonly session="reply-answers-$stamp"

work_dir
printf 'Authorization: Bearer %s\n' "$token" >"$work/auth"

own=""
cleanup() {
  local code=$?
  [[ -z "$own" ]] || close_issue "$own"
  rm -rf "$work"
  exit "$code"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# current SECTION WHAT FROM_KIND FROM_ID: the tuples the store asserts now out of the node, but its
# values and its authored_by, each `kind to_kind:to_id`, sorted and comma-separated, in $answer.
current() {
  query "$1" "$2" "select coalesce(string_agg(kind || ' ' || to_kind || ':' || to_id, ',' order by kind, to_kind, to_id), '') from (
      select distinct on (kind, to_kind, to_id) kind, to_kind, to_id, asserted from graph_tuples
      where from_kind = '$3' and from_id = '$4' and to_kind <> 'value' and kind <> 'authored_by'
      order by kind, to_kind, to_id, seq desc) latest
    where asserted"
}

# census SECTION WHAT SQL: a census whose one row is {rows, tuples, differences}: it passes where it
# examined rows and tuples and found no difference.
census() {
  query "$1" "$2" "$3"
  jq -e '.rows > 0 and .tuples > 0 and .differences == 0' <<<"$answer" >/dev/null ||
    fail "$1" "$2" "the census read $answer"
  pass "$1" "$2: $answer"
}

# thread_entries SECTION WHAT FILE: every comment of the run's issue as the issue's comment read
# answers it, each by its id, the head it replies to and the comment it answers, sorted, in FILE.
thread_entries() {
  send GET "/api/v1/issues/$own/comments"
  want_status 200 "$1" "$2"
  jq -cS '[(if type == "array" then . else .comments end)[] | {id, reply_to, answers}] | sort_by(.id)' "$work/body" >"$3" ||
    fail "$1" "$2" "unreadable: $(body)"
}

# comment SECTION WHAT BODY: the agent bearer writes the comment BODY, a JSON object, on the run's
# issue, and sets $comment to its id.
comment() {
  send POST "/api/v1/issues/$own/comments" "$(jq -c --argjson a "$(actor)" '. + {actor: $a}' <<<"$3")"
  want_status 201 "$1" "$2"
  comment="$(jq -er '.id' "$work/body")" || fail "$1" "$2" "no id: $(body)"
}

# --- setup: the corpus, and a thread of the run's own ------------------------------------------------

corpus_ensure setup
send POST /api/v1/issues "$(jq -cn --arg p "$project" --argjson a "$(actor)" '{project: $p, title: "reply-answers.sh: a thread whose reply answers a reply",
  force: true, actor: $a, spec: "An issue of reply-answers.sh'"'"'s own, whose thread holds a reply that answers a reply."}')"
want_status 201 setup "create an issue of the run's own"
own="$(jq -er '.key' "$work/body")" || fail setup "create an issue of the run's own" "no key: $(body)"
readonly own
comment setup "write the head of a thread on $own" '{"body": "The head of the thread reply-answers.sh writes."}'
root="$comment"
comment setup "write the first reply of that thread" "$(jq -cn --arg r "$root" '{body: "The first reply, which answers the head.", reply_to: $r}')"
reply="$comment"
readonly root reply
pass setup "wrote on $own the thread head $root and its first reply $reply"

# --- A1: a reply against a comment that is not the head ---------------------------------------------

send POST "/api/v1/issues/$own/comments" "$(jq -cn --arg r "$reply" --argjson a "$(actor)" \
  '{body: "reply-answers.sh answers the first reply, not the head of the thread.", reply_to: $r, actor: $a}')"
want_status 201 A1 "write a reply against $reply, which is not the head of its thread"
written="$(jq -er '.id' "$work/body")" || fail A1 "write a reply against $reply" "no id: $(body)"
readonly written
expect A1 "the reply $written comes back pointing at $reply, the comment it answered, beside the thread head $root" \
  "$(jq -cn --arg r "$reply" --arg h "$root" '{answers: $r, reply_to: $h}')" '{answers, reply_to}'

# --- A2: one tuple per pointer ----------------------------------------------------------------------

current A2 "read the tuples out of $written's node" comment "$written"
pointers="$(tr ',' '\n' <<<"$answer" | grep -E '^(answers|replies_to) ' | paste -sd, -)" || pointers=""
want_tuples="answers comment:$reply,replies_to comment:$root"
[[ "$pointers" == "$want_tuples" ]] ||
  fail A2 "the write appended one tuple per pointer: answers $reply and replies_to $root, out of $written's node" \
    "the store asserts '${answer:-no tuple}' out of it; want '$want_tuples' among them"
pass A2 "the write appended one tuple per pointer: answers $reply and replies_to $root; the node holds $answer"

# --- A3: each column equals the tuple behind it -----------------------------------------------------

readonly latest="select distinct on (from_kind, from_id, kind, to_kind, to_id) from_kind, from_id, kind, to_kind, to_id, asserted
  from graph_tuples order by from_kind, from_id, kind, to_kind, to_id, seq desc"
census A3 "comments.answers equals the answers tuples" \
  "with t as (select from_id as id, to_id as target from ($latest) l where asserted and from_kind = 'comment' and kind = 'answers' and to_kind = 'comment'),
     c as (select id::text, answers::text as target from comments where answers is not null)
   select json_build_object('rows', (select count(*) from c), 'tuples', (select count(*) from t),
     'differences', (select count(*) from ((select * from c except select * from t) union all (select * from t except select * from c)) d))"
census A3 "comments.reply_to equals the replies_to tuples to comments" \
  "with t as (select from_id as id, to_id as target from ($latest) l where asserted and from_kind = 'comment' and kind = 'replies_to' and to_kind = 'comment'),
     c as (select id::text, reply_to::text as target from comments where reply_to is not null)
   select json_build_object('rows', (select count(*) from c), 'tuples', (select count(*) from t),
     'differences', (select count(*) from ((select * from c except select * from t) union all (select * from t except select * from c)) d))"
census A3 "comments.ask_id equals the replies_to tuples to asks" \
  "with t as (select from_id as id, to_id as target from ($latest) l where asserted and from_kind = 'comment' and kind = 'replies_to' and to_kind = 'ask'),
     c as (select id::text, ask_id::text as target from comments where ask_id is not null)
   select json_build_object('rows', (select count(*) from c), 'tuples', (select count(*) from t),
     'differences', (select count(*) from ((select * from c except select * from t) union all (select * from t except select * from c)) d))"
census A3 "messages.in_reply_to equals the replies_to tuples to messages" \
  "with t as (select from_id as id, to_id as target from ($latest) l where asserted and from_kind = 'message' and kind = 'replies_to' and to_kind = 'message'),
     c as (select id::text, in_reply_to::text as target from messages where in_reply_to is not null)
   select json_build_object('rows', (select count(*) from c), 'tuples', (select count(*) from t),
     'differences', (select count(*) from ((select * from c except select * from t) union all (select * from t except select * from c)) d))"

# --- A4: a reply to a block of the comment keeps both pointers --------------------------------------

send GET "/api/v1/comments/$reply/blocks"
want_status 200 A4 "read the blocks of $reply"
block="$(jq -er '(if type == "array" then . else .blocks end)[0].id' "$work/body")" || fail A4 "read the blocks of $reply" "no block: $(body)"
readonly block
send POST "/api/v1/issues/$own/comments" "$(jq -cn --arg r "$reply" --arg b "$block" --argjson a "$(actor)" \
  '{body: "reply-answers.sh answers one block of the first reply.", reply_to: $r, reply_to_block: $b, actor: $a}')"
want_status 201 A4 "write a reply against $reply's block $block"
blocked="$(jq -er '.id' "$work/body")" || fail A4 "write a reply against $reply's block" "no id: $(body)"
readonly blocked
expect A4 "the reply keeps both pointers: the comment it answers, $reply, and its block $block, beside the head $root" \
  "$(jq -cn --arg r "$reply" --arg h "$root" --arg b "$block" '{answers: $r, block: $b, reply_to: $h}')" \
  '{answers, reply_to, block: (.reply_to_block // .replies_to_block)}'
current A4 "read the tuples out of $blocked's node" comment "$blocked"
[[ "$answer" == *"answers comment:$reply"* && "$answer" == *"replies_to block:comment/$reply#$block"* ]] ||
  fail A4 "the store holds both pointers of $blocked as tuples, answers $reply and replies_to its block" "it asserts '${answer:-no tuple}'"
pass A4 "the store holds both pointers of $blocked as tuples: $answer"

# --- A5: a retraction, and the thread reads unchanged -----------------------------------------------

thread_entries A5 "read the seeded issue's threads before the retraction" "$work/threads-before.json"
psql_run A5 "retract $written's answers tuple and read comments.answers in the same transaction" \
  -v id="$written" -v target="$reply" <<'SQL'
begin;
insert into graph_tuples (from_kind, from_id, kind, to_kind, to_id, at, who, source_event, asserted)
  values ('comment', :'id', 'answers', 'comment', :'target', now(), '{"kind": "system", "id": "reply-answers.sh"}', 0, false);
select coalesce(answers::text, 'null') from comments where id = :'id';
commit;
SQL
followed="$(tail -n 1 "$work/psql.out")"
[[ "$followed" == null ]] ||
  fail A5 "comments.answers follows the retraction in the same transaction" "it read $followed after the retraction, inside the transaction"
pass A5 "comments.answers follows the retraction of $written's answers tuple in the same transaction: it reads null"
thread_entries A5 "read the seeded issue's threads after the retraction" "$work/threads-after.json"
jq -e 'length > 0' "$work/threads-after.json" >/dev/null || fail A5 "the thread read answers the seeded threads" "it answers none"
jq -cS --arg w "$written" 'map(if .id == $w then del(.answers) else . end)' "$work/threads-before.json" >"$work/before-heads.json"
jq -cS --arg w "$written" 'map(if .id == $w then del(.answers) else . end)' "$work/threads-after.json" >"$work/after-heads.json"
cmp -s "$work/before-heads.json" "$work/after-heads.json" ||
  fail A5 "every thread read returns the entries it returned before" "before: $(<"$work/before-heads.json"); after: $(<"$work/after-heads.json")"
pass A5 "every thread read returns the $(jq 'length' "$work/threads-after.json") entries it returned before, each under its head"

pass "$driver" complete
