#!/usr/bin/env bash
# Drives the one statement in Dispatch that removes bytes (dispatch://INFRA-1149's c-erasure).
# It writes a document, has a person approve a version of it, and writes a comment holding a
# secret that version's markdown holds too. It plants the same bytes in rows of other nodes - a
# document of another issue and a comment of that issue - which no erase here derives from.
#
# It then erases two nodes, each as an operator does: `envoy-dispatch erase --node <node>` lists
# every row it would delete, rewrite, redact or leave with the listing's digest and removes
# nothing, and `--confirm <digest>` removes what that listing names. The first node is the
# comment: its rows, its windows and its own events go, and the document, which derives from no
# comment, is listed and left with its version and its approval. The second is the block of the
# document that holds the secret: its rows, its document's version holding the text and the
# approval over it, its document's live state, search rows, windows and events. It proves, in
# order: each run without --confirm refuses, and the block's listing holds the version and the
# review it would end and the document whose live state it would replace; after the erases a
# read of the comment's address answers what a deleted node answers, byte for byte, no tuple and
# no projection row of either node remains, the version is gone and the review over it is gone,
# reported by its issue and its version; the document still loads, the marker where the secret
# was; a search of every column of every table of the database, which names no store and finds a
# control row planted beside it, marks each row holding the secret as derived from an erased node
# or not and finds none of the first kind; every row planted for the other nodes is byte for byte
# what it was; the events rows the listings named stand with their payload redacted, the marker
# naming the erase in each and the row's id, time and actor what they were before; each erase is
# recorded in one `graph_erasures` row, which holds what was removed - the block's own version
# and review by name - who ran it and when, and holds no erased text; every node left carries a
# supertag of the closed list the store itself holds, so the erases wrote no node of their own;
# `envoy-dispatch census --store` reads zero differences over rows it read; and the command's path
# over HTTP answers 404 to the deployment's agent bearer and to a person's cookie alike, because
# no route serves it.
#
# Every read that wants zero after the erases - the nodes' tuples, the version, the four stores of
# the erased nodes - runs before them as well and finds the rows that held the secret, and the
# comment's address answers 200 before it, so each answer after the erases is held against
# something that was there.
#
# The embedding store is read the same way, and its order is forced. The block-index pass cuts
# the windows of the comments and of the documents into `embeddings` on a tick of its own, so the
# driver waits until every text's windows hold the secret, then holds that pass's lease across
# both erases, from the first listing to the last confirmed run: no tick writes a window between a
# listing and its confirmation, and none while an erase runs.
#
# Inputs: DISPATCH_URL (the server), DISPATCH_TOKEN or DISPATCH_TOKEN_FILE (an agent bearer, read
# here and never printed), PROJECT (an existing project key), DATABASE_URL (the database that
# server serves, which `envoy-dispatch erase` and `envoy-dispatch census` read from their own
# environment: the command takes a database credential and no route), `envoy-dispatch` on PATH,
# and optionally DEV_SIGNIN_LOGIN (default alice), a person the server's dev sign-in route signs
# in, who approves the version: an approval needs the person and approve scopes, which an agent
# bearer does not hold. A missing DATABASE_URL and a missing command are each exit 2, as every
# unmet input of this driver is.
#
# It reads this pod's own test database and never a deployment's: it refuses a DATABASE_URL whose
# host is not loopback, and the erases it runs remove only the comment and the block this run
# wrote.
# Each passing check prints `PASS: <section> <what>`; the first failing one prints
# `FAIL: <section> <what>: <observed>` and the script exits 1.
#
# Every check is `cmd || fail …`, never part of an `&&` list: under `set -e` a failing command
# anywhere but last in such a list does not stop the script.

set -euo pipefail
set +x

usage() {
  cat >&2 <<'USAGE'
Usage: DATABASE_URL=postgres://user@127.0.0.1:5433/db DISPATCH_URL=http://127.0.0.1:8777 \
         DISPATCH_TOKEN=e2e-token PROJECT=CORE [DEV_SIGNIN_LOGIN=alice] graph-erase.sh
       (DISPATCH_TOKEN_FILE=<file holding the bearer> in place of DISPATCH_TOKEN; the built
       `envoy-dispatch` must be on PATH, and DATABASE_URL must name the database that server
       serves, on this machine)
USAGE
}

# shellcheck source-path=SCRIPTDIR source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_env DISPATCH_URL PROJECT DATABASE_URL
require_commands curl envoy-dispatch jq psql
read_bearer

readonly base="${DISPATCH_URL%/}"
# The dashboard's origin, the scheme and host of DISPATCH_URL: a write the person's cookie
# authenticates must name it (enforceCookieOrigin).
base_host="${base#*://}"
readonly origin="${base%%://*}://${base_host%%/*}"
# One statement, so the directive covers both: a directive comment reaches the command after it
# alone, and before this it left the second of them reported.
# shellcheck disable=SC2153 # PROJECT and DATABASE_URL are the caller's inputs, checked by name above
readonly project="$PROJECT" database_url="$DATABASE_URL"
script_dir="$(cd -- "$(dirname -- "$0")" && pwd)"
readonly script_dir
# The block-index pass's lease, which writes the embedding windows this driver holds still across
# the erase, read from the Go sources that declare it: the lease's name and the first key every
# loop lease is taken with (duty.Exclusive).
readonly loop_source="$script_dir/../../../envoy/internal/dispatch/docs/block_index.go"
readonly lease_source="$script_dir/../../../envoy/internal/dispatch/duty/exclusive.go"
# How long the driver waits for that pass to write the windows - it runs once a tick, 30 s apart
# on this build - and how long the session holding its lease sleeps, which is a hang guard alone:
# release_block_index interrupts it as soon as the erase is done.
readonly window_wait=120
readonly lease_seconds=900
readonly lease_polls=1500
readonly login="${DEV_SIGNIN_LOGIN:-alice}"
session="graph-erase-$(date +%s)-$$"
readonly session
# The bytes this run erases. Nothing but this run writes them, so a row that still holds them
# after the erase is a row the erase missed.
#
# They are one word of letters and digits, and not the `passphrase-<time>-<pid>-hunter2` this
# driver used before, because Postgres splits a hyphenated token into several lexemes: a
# tsvector holding that shape holds `passphras`, `-<time>`, `-<pid>` and `hunter2` and never the
# whole, so a search for the secret could not find it in a search vector, and the run-unique
# parts are numbers this run's session name holds as well. One word of letters and digits is one
# lexeme, spelled as the text spells it, so the search below finds these bytes in a tsvector, in
# a lexeme of the term statistics and in every text column alike.
secret="passphrase$(date +%s)x$$hunter2"
readonly secret

umask 077
work="$(mktemp -d)"
readonly work
printf 'Authorization: Bearer %s\n' "$token" >"$work/auth"

key=""
other_key=""
lease_pid=""
cleanup() {
  local code=$?
  local closing
  release_block_index
  for closing in "$key" "$other_key"; do
    [[ -n "$closing" ]] || continue
    send PATCH "/api/v1/issues/$closing" \
      "$(jq -cn --arg s "$session" '{status: "done", actor: {kind: "session", id: $s}}')"
    [[ "$status" == 200 ]] ||
      printf 'graph-erase.sh: could not close %s: HTTP %s\n' "$closing" "$status" >&2
  done
  rm -rf "$work"
  exit "$code"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# request AS METHOD PATH [JSON]: sends METHOD PATH as AS: `agent`, with the bearer in $work/auth,
# or `person`, with the session cookie the dev sign-in left in $work/jar and the dashboard's
# origin, which the server requires of every write a cookie authenticates. The answer's body goes
# to $work/body and its HTTP status to $status.
request() {
  local -a auth data=()
  if [[ "$1" == person ]]; then
    auth=(-b "$work/jar" -H "Origin: $origin")
  else
    auth=(-H "@$work/auth")
  fi
  [[ $# -lt 4 ]] || data=(-H 'Content-Type: application/json' --data-binary "$4")
  status="$(curl -sS -X "$2" -o "$work/body" -w '%{http_code}' "${auth[@]}" "${data[@]}" \
    "$base$3" 2>"$work/curl")" || status="curl failed: $(<"$work/curl")"
}

# send METHOD PATH [JSON]: request as the agent.
send() { request agent "$@"; }
# person_send METHOD PATH [JSON]: request as the person DEV_SIGNIN_LOGIN signed in.
person_send() { request person "$@"; }

# want_rows SECTION WHAT WANT SQL: the count SQL reads is WANT.
want_rows() {
  query "$1" "$2" "$4"
  [[ "$answer" == "$3" ]] || fail "$1" "$2" "read $answer, want $3 (from: $4)"
  pass "$1" "$2"
}

# want_held SECTION WHAT SQL: the count SQL reads is above zero, and the check prints it. Each of
# these is a read E5 later wants zero from, run before the erase, so that zero is a row the erase
# removed and not a search that matched nothing.
want_held() {
  query "$1" "$2" "$3"
  [[ "$answer" =~ ^[1-9][0-9]*$ ]] || fail "$1" "$2" "read $answer, want at least 1 (from: $3)"
  pass "$1" "$2: $answer row(s)"
}

# held_soon SECTION WHAT SQL: polls SQL, a count, until it reads above zero, for up to
# $window_wait seconds, and prints the count it read. It is for a row a pass of the server writes
# on its own tick, which no request of this driver can ask for.
held_soon() {
  local waited=0
  while true; do
    query "$1" "$2" "$3"
    [[ ! "$answer" =~ ^[1-9][0-9]*$ ]] || break
    [[ "$waited" -lt $((window_wait * 2)) ]] ||
      fail "$1" "$2" "read $answer after ${window_wait}s, want at least 1 (from: $3)"
    sleep 0.5
    waited=$((waited + 1))
  done
  pass "$1" "$2: $answer row(s)"
}

# declared SECTION WHAT FILE SED: the one value SED prints out of a Go source file, refused when
# the file or the declaration is gone, so a build that moved the lease moves this driver with it.
declared() {
  [[ -r "$3" ]] || fail "$1" "$2" "$3 is unreadable"
  answer="$(sed -n "$4" "$3")"
  [[ -n "$answer" ]] || fail "$1" "$2" "no such declaration in $3"
}

# hold_block_index SECTION: takes the block-index pass's own lease, waiting out a tick that holds
# it, so no tick writes an embedding window while this driver erases: duty.Exclusive takes this
# lease before every pass and skips the pass where another session holds it. The lease is a
# session lock, so a psql of this driver's holds it until release_block_index interrupts that
# session's sleep. It is graph-sizing.sh's hold_block_index.
hold_block_index() {
  local polls=0
  : >"$work/lease"
  env -u PGHOSTADDR psql "$database_url" -Atq -o /dev/null >/dev/null 2>"$work/lease" <<SQL &
select pg_advisory_lock($lease_namespace, hashtext('$lease_name'));
\\warn the $lease_name lease is held
select pg_sleep($lease_seconds);
SQL
  lease_pid=$!
  until grep -q "the $lease_name lease is held" "$work/lease"; do
    polls=$((polls + 1))
    [[ "$polls" -le "$lease_polls" ]] ||
      fail "$1" "hold the $lease_name lease" \
        "it was not given up inside $((lease_polls / 5))s: $(head -c 300 "$work/lease")"
    kill -0 "$lease_pid" 2>/dev/null ||
      fail "$1" "hold the $lease_name lease" "psql stopped: $(head -c 300 "$work/lease")"
    sleep 0.2
  done
  pass "$1" "this driver holds the $lease_name lease, so no tick writes an embedding window during the erase"
}

# release_block_index gives that lease back: SIGINT makes psql cancel its sleep and read the end of
# its own input, which closes the session and frees the lock there and then.
release_block_index() {
  [[ -n "$lease_pid" ]] || return 0
  kill -INT "$lease_pid" 2>/dev/null || true
  wait "$lease_pid" 2>/dev/null || true
  lease_pid=""
}

# erase SECTION ARGS...: runs `envoy-dispatch erase ARGS`, its output in $work/erase and its exit
# status in $erase_code. The command reads DATABASE_URL from its own environment and no bearer
# reaches it, so this driver's is kept out of the environment it runs in.
erase() {
  local section="$1"
  shift
  erase_code=0
  env -u DISPATCH_TOKEN -u DISPATCH_TOKEN_FILE DATABASE_URL="$database_url" \
    envoy-dispatch erase "$@" >"$work/erase" 2>&1 || erase_code=$?
  [[ "$erase_code" -lt 2 ]] ||
    fail "$section" "run envoy-dispatch erase $*" "exited $erase_code: $(<"$work/erase")"
}

# held_anywhere SECTION NEEDLE COMMENT DOCUMENT REF: searches every column of every table of the
# database for NEEDLE, and sets $answer to `columns <n>`, the number of columns searched, then one
# line `<schema>.<table>.<column> <derived> <other>` for each column holding it. <other> counts
# the rows that derive from the nodes this driver planted for others - the comment COMMENT and the
# document DOCUMENT, whose address is REF - and <derived> every other row holding NEEDLE, which
# can only derive from a node this driver erased. A row is the planted nodes' when its text names
# one of them; a display tree (`text_trees`) names no node and is keyed by the digest of its text,
# so it is theirs when that digest is the digest of one of their texts; and a project's term
# statistics (`search_term_stats`) count a word for as long as any item of the project holds it,
# which the planted comment does. A bytea is searched for NEEDLE's UTF-8 bytes (convert_to), since
# its text form is hex; every other column for NEEDLE in its text form, which is how a jsonb, a
# tsvector and an array spell what they hold. A column of a type that holds no text - a number, a
# time, a uuid - is not searched.
#
# The same session first writes NEEDLE into a table of its own, `erase_control`, temporary and
# so dropped with the session, and searches it with the rest: a search that reached no row would
# answer none for every column, and the caller reads the control's line to tell that apart.
held_anywhere() {
  answer="$(env -u PGHOSTADDR psql "$database_url" -Atq -v ON_ERROR_STOP=1 -v needle="$2" \
    -v comment="$3" -v document="$4" -v ref="$5" -f - 2>"$work/psql" <<'SQL'
select set_config('erase_driver.needle', :'needle', false) \g /dev/null
select set_config('erase_driver.comment', :'comment', false) \g /dev/null
select set_config('erase_driver.document', :'document', false) \g /dev/null
select set_config('erase_driver.ref', :'ref', false) \g /dev/null
create temp table erase_control (body text);
insert into erase_control values ('planted beside the search: ' || :'needle');
create temp table erase_held (place text, derived bigint, other bigint);
create temp table erase_searched (columns integer);
do $$
declare
  needle text := current_setting('erase_driver.needle');
  planted text[] := array[current_setting('erase_driver.comment'), current_setting('erase_driver.document'),
    current_setting('erase_driver.ref')];
  col record;
  test text;
  theirs text;
  derived bigint;
  other bigint;
  searched integer := 0;
begin
  for col in
    select c.table_schema, c.table_name, c.column_name, c.data_type, c.udt_name
      from information_schema.columns c
      join information_schema.tables t
        on t.table_schema = c.table_schema and t.table_name = c.table_name
     where t.table_type in ('BASE TABLE', 'LOCAL TEMPORARY')
       and c.table_schema not in ('pg_catalog', 'information_schema')
       and (c.table_schema not like 'pg_temp%' or c.table_schema = pg_my_temp_schema()::regnamespace::text)
       and c.data_type not in ('smallint', 'integer', 'bigint', 'numeric', 'real', 'double precision',
         'boolean', 'uuid', 'date', 'interval', 'time with time zone', 'time without time zone',
         'timestamp with time zone', 'timestamp without time zone', 'inet', 'cidr', 'macaddr',
         'macaddr8', 'oid', 'xid', 'xid8', 'pg_lsn', 'money', 'bit', 'bit varying')
     order by 1, 2, 3
  loop
    test := case
      when col.data_type = 'bytea' then
        format('position(convert_to($1, %L) in t.%I) > 0', 'UTF8', col.column_name)
      when col.udt_name = '_bytea' then
        format('exists (select 1 from unnest(t.%I) b where position(convert_to($1, %L) in b) > 0)', col.column_name, 'UTF8')
      else format('position($1 in t.%I::text) > 0', col.column_name)
    end;
    theirs := case
      when col.table_schema like 'pg_temp%' then 'false'
      when col.table_name = 'text_trees' then
        't.sum in (select sha256(convert_to(c.body, ''UTF8'')) from comments c where c.id::text = $2[1]
                   union select sha256(convert_to(v.markdown, ''UTF8'')) from artifact_versions v
                    where v.artifact_id::text = $2[2] and v.markdown is not null
                   union select sha256(convert_to(b.text, ''UTF8'')) from document_block_texts b
                    where b.artifact_id::text = $2[2] and b.text is not null)'
      when col.table_name = 'search_term_stats' then 'true'
      else 'exists (select 1 from unnest($2) named where position(named in to_jsonb(t)::text) > 0)'
    end;
    execute format('select count(*) filter (where not (%s)), count(*) filter (where %s) from %I.%I t where %s',
      theirs, theirs, col.table_schema, col.table_name, test)
      into derived, other using needle, planted;
    searched := searched + 1;
    if derived + other > 0 then
      insert into erase_held values (col.table_schema || '.' || col.table_name || '.' || col.column_name, derived, other);
    end if;
  end loop;
  insert into erase_searched values (searched);
end $$;
select 'columns ' || columns from erase_searched;
select place || ' ' || derived || ' ' || other from erase_held order by place;
SQL
  )" || fail "$1" "search every column of every table" "psql: $(<"$work/psql")"
}

# listed_digest SECTION FILE: the digest a run without --confirm printed into FILE, into $digest.
listed_digest() {
  digest="$(sed -n 's/^  listing digest: \([0-9a-f]\{64\}\)$/\1/p' "$2")"
  [[ -n "$digest" ]] || fail "$1" "read the listing's digest" "no listing digest in: $(<"$2")"
}

# listed_events FILE: the ids of the events rows the listing in FILE redacts, one per line.
listed_events() {
  sed -n 's/^    events \([0-9][0-9]*\) .*$/\1/p' "$1"
}

# E1: the inputs, and the database this run works on.
host="${database_url#*://}"
host="${host#*@}"
host="${host%%[:/]*}"
case "$host" in
  127.0.0.1 | ::1 | localhost) pass E1 "DATABASE_URL names this machine ($host)" ;;
  *) fail E1 "DATABASE_URL names this machine" "host $host is not loopback; this driver erases rows and reads this pod's own test database, never a deployment's" ;;
esac

# E2: the issue, the document whose version holds the secret, and the person's approval of it.
send POST "/api/v1/issues" "$(jq -cn --arg p "$project" --arg s "$session" \
  '{project: $p, title: "Store erasure check", spec: "The one statement that removes bytes.", actor: {kind: "session", id: $s}}')"
want_status 201 E2 "create the issue"
key="$(jq -er '.key' "$work/body")" || fail E2 "create the issue" "no key: $(body)"
pass E2 "created $key in $project"

# The comment's whole body is one paragraph of the document, so the block the second erase
# addresses holds exactly the bytes the first one does.
readonly leak="The passphrase is $secret and nobody else may read it."
send POST "/api/v1/issues/$key/artifacts" "$(jq -cn --arg s "$session" --arg leak "$leak" \
  '{name: "leaked-notes.md", content: ("# Leaked notes\n\n" + $leak + "\n"), actor: {kind: "session", id: $s}}')"
want_status 201 E2 "write the document"
document="$(jq -er '.artifact.id' "$work/body")" || fail E2 "write the document" "no id: $(body)"
document_ref="$(jq -er '.artifact.ref_key' "$work/body")" ||
  fail E2 "write the document" "no ref_key: $(body)"
version="$(jq -er '.version.number' "$work/body")" ||
  fail E2 "write the document" "no version: $(body)"
pass E2 "wrote $document_ref version $version holding the secret"

curl -sS -o /dev/null -c "$work/jar" "$base/auth/_dev/signin?login=$(q "$login")" 2>"$work/curl" ||
  fail E2 "sign $login in" "curl: $(<"$work/curl")"
[[ -s "$work/jar" ]] ||
  fail E2 "sign $login in" "the dev sign-in route left no cookie; this driver needs a person to approve a version"
person_send POST "/api/v1/artifacts/$document/reviews" '{"state":"approved"}'
want_status 201 E2 "approve $document_ref version $version"
review="$(jq -er '.review.id // .id' "$work/body")" || fail E2 "approve the version" "no review id: $(body)"
pass E2 "$login approved $document_ref version $version (review $review)"
want_rows E2 "the approval covers the version's bytes" 1 \
  "select count(*) from artifact_reviews where artifact_id = '$document' and version = $version and content_sha256 is not null"

send POST "/api/v1/issues/$key/comments" "$(jq -cn --arg s "$session" --arg leak "$leak" \
  '{body: $leak, actor: {kind: "session", id: $s}}')"
want_status 201 E2 "write the comment holding the secret"
comment="$(jq -er '.id' "$work/body")" || fail E2 "write the comment" "no id: $(body)"
readonly node="comment:$comment"
pass E2 "wrote $node"
want_rows E2 "the comment is a node of the store" 1 \
  "select count(*) from graph_nodes where kind = 'comment' and id = '$comment'"

# A second comment of the same session, which no erase touches: the store holds a session node
# for the author of a comment (R19's row side), so a session whose only row the erase removes
# would be left as a node the census rightly reports as projected by nothing. The erase removes
# the bytes it was pointed at and no author's node, and this comment is what keeps that true for
# the census below.
send POST "/api/v1/issues/$key/comments" "$(jq -cn --arg s "$session" \
  '{body: "A second note of this run, holding no secret.", actor: {kind: "session", id: $s}}')"
want_status 201 E2 "write a comment the erase does not touch"
pass E2 "the session that wrote both keeps a row the erase leaves"

# The rows of other nodes that hold the same bytes: a document of another issue and a comment of
# that issue, each its own text. No erase here derives from them, so each run lists them and
# leaves them byte for byte as they are.
send POST "/api/v1/issues" "$(jq -cn --arg p "$project" --arg s "$session" \
  '{project: $p, title: "Store erasure check, another node", spec: "Another issue, quoting nothing yet.", actor: {kind: "session", id: $s}}')"
want_status 201 E2 "create the other issue"
other_key="$(jq -er '.key' "$work/body")" || fail E2 "create the other issue" "no key: $(body)"
send POST "/api/v1/issues/$other_key/artifacts" "$(jq -cn --arg s "$session" --arg secret "$secret" \
  '{name: "quoted-notes.md", content: ("# Quoted notes\n\nSomebody else wrote " + $secret + " down here too.\n"), actor: {kind: "session", id: $s}}')"
want_status 201 E2 "write the other issue's document"
other_document="$(jq -er '.artifact.id' "$work/body")" || fail E2 "write the other issue's document" "no id: $(body)"
other_ref="$(jq -er '.artifact.ref_key' "$work/body")" ||
  fail E2 "write the other issue's document" "no ref_key: $(body)"
send POST "/api/v1/issues/$other_key/comments" "$(jq -cn --arg s "$session" --arg secret "$secret" \
  '{body: ("Another comment repeats " + $secret + " on its own."), actor: {kind: "session", id: $s}}')"
want_status 201 E2 "write the other issue's comment"
other_comment="$(jq -er '.id' "$work/body")" || fail E2 "write the other issue's comment" "no id: $(body)"
readonly other_key other_document other_ref other_comment
pass E2 "planted the secret in $other_ref and comment:$other_comment of $other_key, which no erase here derives from"

# Every row planted for those two nodes, read whole, so a read after the erases can say whether a
# byte of one moved: the comment, its own events' payloads, the document's versions, its blocks,
# its blocks' texts and the windows of both.
readonly planted_sql="select md5(concat_ws('|',
    (select string_agg(c.id || ':' || c.body, ';') from comments c where c.id = '$other_comment'),
    (select string_agg(e.id || ':' || e.payload::text, ';' order by e.id) from events e where e.payload->>'id' = '$other_comment'),
    (select string_agg(v.number || ':' || v.markdown, ';' order by v.number) from artifact_versions v where v.artifact_id = '$other_document'),
    (select string_agg(b.block_id || ':' || b.excerpt, ';' order by b.block_id) from document_blocks b where b.artifact_id = '$other_document'),
    (select string_agg(b.block_id || ':' || b.version || ':' || coalesce(b.text, ''), ';' order by b.block_id, b.version)
       from document_block_texts b where b.artifact_id = '$other_document'),
    (select string_agg(w.kind || ':' || w.id || ':' || w.text_snapshot, ';' order by w.kind, w.id) from embeddings w
      where split_part(w.id, '#', 1) in ('$other_document', 'comment/$other_comment'))))"

# The reads E5 wants zero from, each stated once here and run on both sides of the erases: before
# them each must find the rows that held the secret (E3), so after them each zero is an erase's
# work. Each reads the rows of an erased node alone - the comment's own row, and the version,
# the block texts and the excerpts of the document the block is of - since the planted rows of
# the other nodes hold the same bytes and stand.
readonly node_tuples_sql="select count(*) from graph_tuples where (from_kind = 'comment' and from_id = '$comment') or (to_kind = 'comment' and to_id = '$comment')"
readonly version_sql="select count(*) from artifact_versions where artifact_id = '$document' and number = $version"
readonly -a store_reads=(
  "comment row|select count(*) from comments where id = '$comment' and body like '%$secret%'"
  "document version|select count(*) from artifact_versions where artifact_id = '$document' and markdown like '%$secret%'"
  "block text|select count(*) from document_block_texts where artifact_id = '$document' and text like '%$secret%'"
  "block excerpt|select count(*) from document_blocks where artifact_id = '$document' and excerpt like '%$secret%'"
)

# E3: each erase refuses until it has listed what it would remove, and each listing names what
# derives from its node and what it leaves.
#
# The event log as it stands before the erases: the rows of the comment's own events that hold
# the secret, and the id, the time and the actor of every row holding it. R5 redacts a payload in
# place rather than deleting its row, because the log is append-only, so these are the facts E5
# reads again afterwards.
query E3 "count the comment's own event rows holding the secret" \
  "select count(*) from events where payload->>'id' = '$comment' and payload::text like '%$secret%'"
[[ "$answer" -gt 0 ]] ||
  fail E3 "the event log holds the comment's own write" \
    "no events row of the comment holds the secret, so the redaction E5 reads would prove nothing"
pass E3 "the event log holds $answer row(s) of the comment's own with the secret"

# The rest of what E5 reads gone, read here while it stands: the node's tuples, the version that
# holds the erased text, and each of the four stores of the erased nodes.
want_held E3 "the comment's node holds tuples before the erase" "$node_tuples_sql"
want_held E3 "the version holding the secret is stored before the erase" "$version_sql"
for read in "${store_reads[@]}"; do
  want_held E3 "a ${read%%|*} holds the secret before the erase" "${read#*|}"
done

# The embedding windows every text is cut into, which the block-index pass writes on its own tick
# (refs.WriteItemWindows for a comment, refs.WriteDocumentWindows for a document) and no request
# writes: the driver waits for each to hold the secret, so every run erases nodes whose windows
# stand, and the planted windows are written before they are read whole. A run that erased before
# the pass had cut them left nothing in `embeddings` for the search of E5 to find, which is how an
# erase that never touched that store passed some runs and failed others.
for windows in "comment_block|comment/$comment" "document_block|$document" \
  "comment_block|comment/$other_comment" "document_block|$other_document"; do
  held_soon E3 "the ${windows%%|*} windows of ${windows#*|} hold the secret before the erase" \
    "select count(*) from embeddings where kind = '${windows%%|*}'
       and split_part(id, '#', 1) = '${windows#*|}' and position('$secret' in text_snapshot) > 0"
done

# The answer an address nobody holds gives, read before the erase, so the comparison after it is
# against this server's own words and not against a status this driver chose.
missing="00000000-0000-4000-8000-000000000000"
send GET "/api/v1/references?to=$(q "dispatch://$key/comment/$missing")"
readonly nobody_status="$status"
cp "$work/body" "$work/nobody"
pass E3 "an address nobody holds answers HTTP $nobody_status"
# The node's own address, read before the erase: it resolves, so what it answers after the erase
# is the erase's doing, and the two answers E5 compares differ before it.
send GET "/api/v1/references?to=$(q "dispatch://$key/comment/$comment")"
want_status 200 E3 "the comment's address resolves before the erase"
[[ "$nobody_status" != 200 ]] ||
  fail E3 "an address nobody holds answers otherwise than one that resolves" \
    "both answered HTTP 200, so E5's comparison could not tell the erased node from a live one"
pass E3 "the comment's address answers HTTP 200 before the erase, where an address nobody holds answers HTTP $nobody_status"

# From here to the last confirmed run the block-index pass's lease is held, so no tick writes a
# window between a listing and the run that confirms it, and none while an erase runs. The
# planted rows are read whole under it.
declared E3 "read the block index's lease" "$loop_source" \
  's/^const BlockIndexLease = "\(.*\)"$/\1/p'
lease_name="$answer"
declared E3 "read the first key of a loop lease" "$lease_source" \
  's/^const leaseNamespace int32 = \(0x[0-9A-Fa-f]*\)$/\1/p'
lease_namespace=$((answer))
hold_block_index E3
query E3 "read the planted rows whole" "$planted_sql"
readonly planted_before="$answer"
query E3 "read the id, time and actor of each event row holding the secret" \
  "select coalesce(jsonb_agg(jsonb_build_object('id', id, 'created_at', created_at, 'actor', actor) order by id), '[]'::jsonb)::text
     from events where payload::text like '%$secret%'"
readonly events_before="$answer"

# The comment's listing.
erase E3 --node "$node"
cp "$work/erase" "$work/comment.listing"
[[ "$erase_code" == 1 ]] ||
  fail E3 "refuse $node without --confirm" "exited $erase_code, want 1: $(<"$work/erase")"
grep -Fq "nothing was removed" "$work/erase" ||
  fail E3 "refuse $node without --confirm" "no refusal in: $(<"$work/erase")"
pass E3 "envoy-dispatch erase --node $node removed nothing and said why"
listed_digest E3 "$work/comment.listing"
readonly comment_digest="$digest"
grep -Eq "^  versions holding the erased text: 0\$" "$work/comment.listing" ||
  fail E3 "a comment's listing ends no version" "$(<"$work/comment.listing")"
grep -Eq "^    leave public\.artifact_versions .*\[$document_ref version $version of $key\] markdown\$" "$work/comment.listing" ||
  fail E3 "the comment's listing leaves $document_ref, which derives from no comment" "$(<"$work/comment.listing")"
grep -Eq "^    leave public\.artifact_versions .*\[$other_ref version 1 of $other_key\] markdown\$" "$work/comment.listing" ||
  fail E3 "the comment's listing leaves the other issue's document" "$(<"$work/comment.listing")"
grep -Eq "^    leave public\.comments \(id\)=\($other_comment\) .*body" "$work/comment.listing" ||
  fail E3 "the comment's listing leaves the other comment" "$(<"$work/comment.listing")"
[[ -n "$(listed_events "$work/comment.listing")" ]] ||
  fail E3 "the comment's listing names its own event rows by id" "$(<"$work/comment.listing")"
pass E3 "the listing of $node (digest $comment_digest) ends no version, names its own event rows, and leaves $document_ref, $other_ref and comment:$other_comment"
want_rows E3 "the refused run wrote nothing" 1 \
  "select count(*) from comments where id = '$comment'"
want_rows E3 "the refused run recorded no erasure" 0 \
  "select count(*) from graph_erasures where node_id = '$comment'"

# E4: the comment's erase, confirmed by its listing's digest.
erase E4 --node "$node" --confirm "$comment_digest"
cp "$work/erase" "$work/comment.erased"
[[ "$erase_code" == 0 ]] ||
  fail E4 "erase $node" "exited $erase_code: $(<"$work/erase")"
grep -Fq "removed by" "$work/erase" || fail E4 "erase $node" "$(<"$work/erase")"
pass E4 "envoy-dispatch erase --node $node --confirm $comment_digest removed it"
want_rows E4 "the comment's erase left $document_ref version $version, which derives from the document" 1 "$version_sql"
want_rows E4 "the comment's erase left the approval of it" 1 \
  "select count(*) from artifact_reviews where artifact_id = '$document' and version = $version"
grep -Eq "^  redacted: events [1-9][0-9]*\$" "$work/comment.erased" ||
  fail E4 "the run says how many rows it redacted" "no redacted events line in: $(<"$work/comment.erased")"
pass E4 "envoy-dispatch erase reported the rows it redacted"

# The block of the document that holds the secret, which a version of the document recorded.
query E4 "read the block of $document_ref holding the secret" \
  "select a.ref_key || '#' || t.block_id from document_block_texts t join artifacts a on a.id = t.artifact_id
    where t.artifact_id = '$document' and position('$secret' in t.text) > 0 order by t.version desc limit 1"
[[ -n "$answer" ]] || fail E4 "read the block of $document_ref holding the secret" "no block text holds it"
readonly block="$answer" block_node="block:$answer"
readonly block_tuples_sql="select count(*) from graph_tuples where (from_kind = 'block' and from_id = '$block') or (to_kind = 'block' and to_id = '$block')"
want_held E4 "$block_node holds tuples before its erase" "$block_tuples_sql"

# The block's listing: the version, the review and the document's live state it would end.
erase E4 --node "$block_node"
cp "$work/erase" "$work/block.listing"
[[ "$erase_code" == 1 ]] ||
  fail E4 "refuse $block_node without --confirm" "exited $erase_code, want 1: $(<"$work/erase")"
grep -Fq "nothing was removed" "$work/erase" ||
  fail E4 "refuse $block_node without --confirm" "no refusal in: $(<"$work/erase")"
pass E4 "envoy-dispatch erase --node $block_node removed nothing and said why"
listed_digest E4 "$work/block.listing"
readonly block_digest="$digest"
grep -Eq "^  versions holding the erased text: [1-9][0-9]*\$" "$work/block.listing" ||
  fail E4 "list the versions it would end" "$(<"$work/block.listing")"
grep -Fqx "    $document_ref version $version of $key" "$work/block.listing" ||
  fail E4 "list the versions it would end" "no $document_ref version $version in: $(<"$work/block.listing")"
pass E4 "the list holds $document_ref version $version"
grep -Eq "^  reviews over those bytes: [1-9][0-9]*\$" "$work/block.listing" ||
  fail E4 "list the reviews it would end" "$(<"$work/block.listing")"
grep -Fqx "    $document_ref version $version approved (review $review) of $key" "$work/block.listing" ||
  fail E4 "list the reviews it would end" "no review $review in: $(<"$work/block.listing")"
pass E4 "the list holds the approval over those bytes (review $review)"
# The document's live state, which the erase replaces: its live text with the marker where the
# secret was, seeded as its one stored update in place of its history. That costs what a rebuild
# costs, so the list says it before anything goes: the operator's last look.
grep -Eq "^  live documents whose history is replaced: [1-9][0-9]*\$" "$work/block.listing" ||
  fail E4 "list the documents whose live state it would replace" "$(<"$work/block.listing")"
grep -Eq "^    $document_ref of $key: its [1-9][0-9]* stored update\(s\), [0-9]+ checkpoint\(s\) and [0-9]+ snapshot\(s\) are replaced by one seed of its live text, the marker standing where the erased text was; its anchor marks and margin records go with them; an editor open on it reloads and loses what it has not stored\$" "$work/block.listing" ||
  fail E4 "list what replacing $document_ref's live state costs" "no such line for $document_ref in: $(<"$work/block.listing")"
pass E4 "the list holds $document_ref's live state: its history replaced, and what that costs"
grep -Eq "^    leave public\.artifact_versions .*\[$other_ref version 1 of $other_key\] markdown\$" "$work/block.listing" ||
  fail E4 "the block's listing leaves the other issue's document" "$(<"$work/block.listing")"
grep -Eq "^    leave public\.comments \(id\)=\($other_comment\) .*body" "$work/block.listing" ||
  fail E4 "the block's listing leaves the other comment" "$(<"$work/block.listing")"
pass E4 "the listing of $block_node (digest $block_digest) leaves $other_ref and comment:$other_comment"
want_rows E4 "the refused runs wrote nothing" 1 "$version_sql"
want_rows E4 "the refused runs recorded no erasure of the block" 0 \
  "select count(*) from graph_erasures where node_id = '$block'"

# The block's erase, confirmed by its listing's digest.
erase E4 --node "$block_node" --confirm "$block_digest"
cp "$work/erase" "$work/block.erased"
[[ "$erase_code" == 0 ]] ||
  fail E4 "erase $block_node" "exited $erase_code: $(<"$work/erase")"
grep -Fq "removed by" "$work/erase" || fail E4 "erase $block_node" "$(<"$work/erase")"
grep -Eq "^  live document $document_ref of $key: its live text is redacted, the marker standing where the erased text was; [0-9]+ anchor mark\(s\) and [0-9]+ margin record\(s\) go with its history\$" "$work/block.erased" ||
  fail E4 "the block's erase redacted $document_ref's live text" "$(<"$work/block.erased")"
pass E4 "envoy-dispatch erase --node $block_node --confirm $block_digest removed it and redacted $document_ref's live text"
query E4 "read the planted rows whole after both erases" "$planted_sql"
[[ "$answer" == "$planted_before" ]] ||
  fail E4 "every row planted for the other nodes is as it was" "the planted rows read $answer after the erases, $planted_before before them"
pass E4 "every row planted for $other_ref and comment:$other_comment is byte for byte what it was"
release_block_index
pass E4 "the $lease_name lease was held across both erases and given back"

# E5: the bytes are gone from every row that derives from an erased node, and so is everything
# that stood on them.
send GET "/api/v1/references?to=$(q "dispatch://$key/comment/$comment")"
[[ "$status" == "$nobody_status" ]] ||
  fail E5 "the node's address answers what a deleted node answers" \
    "HTTP $status, want $nobody_status: $(body)"
# The two answers differ in the address each was asked about and in nothing else, so each is
# read with its own id written as the same placeholder: what is compared is every other byte.
sed "s/$comment/<address>/g" "$work/body" >"$work/erased.answer"
sed "s/$missing/<address>/g" "$work/nobody" >"$work/nobody.answer"
cmp -s "$work/erased.answer" "$work/nobody.answer" ||
  fail E5 "the node's address answers what a deleted node answers" \
    "got $(head -c 300 "$work/erased.answer"), want $(head -c 300 "$work/nobody.answer")"
pass E5 "the node's address answers what an address nobody holds answers, byte for byte"
want_rows E5 "no node of the comment remains" 0 \
  "select count(*) from graph_nodes where kind = 'comment' and id = '$comment'"
want_rows E5 "no tuple of the comment remains" 0 "$node_tuples_sql"
want_rows E5 "no projection row of the comment remains" 0 \
  "select count(*) from comments where id = '$comment'"
want_rows E5 "no node of the block remains" 0 \
  "select count(*) from graph_nodes where kind = 'block' and id = '$block'"
want_rows E5 "no tuple of the block remains" 0 "$block_tuples_sql"
want_rows E5 "no projection row of the block remains" 0 \
  "select count(*) from document_blocks where node_kind = 'block' and node_address = '$block'"
want_rows E5 "the version holding the erased text is gone" 0 "$version_sql"
want_rows E5 "the review over those bytes is gone ($key, $document_ref version $version)" 0 \
  "select count(*) from artifact_reviews where artifact_id = '$document' and version = $version"
# The stores of the erased nodes that held the bytes: E3 read each holding the secret.
for read in "${store_reads[@]}"; do
  want_rows E5 "no ${read%%|*} still holds the erased text" 0 "${read#*|}"
done
# The document that held the secret still loads, from the one update the erase seeded its history
# with: its text keeps everything the erase did not take, and reads the marker where the secret
# was. It is read before the search below, so a read that wrote the secret back - a room still
# holding the old history, storing what it read - is a row that search finds.
send GET "/api/v1/artifacts/$document/text"
want_status 200 E5 "read $document_ref's live text after the erase"
jq -er '.markdown' "$work/body" >"$work/live.md" ||
  fail E5 "read $document_ref's live text after the erase" "no markdown: $(body)"
if grep -Fq "$secret" "$work/live.md"; then
  fail E5 "$document_ref's live text no longer holds the erased text" "$(<"$work/live.md")"
fi
grep -Fq "# Leaked notes" "$work/live.md" ||
  fail E5 "$document_ref keeps the text the erase did not take" "$(<"$work/live.md")"
grep -Fq "erased by envoy-dispatch erase $block_node, recorded in graph_erasures" "$work/live.md" ||
  fail E5 "$document_ref reads the marker where the erased text was" "$(<"$work/live.md")"
pass E5 "$document_ref still loads: its heading as it was, and the marker where the erased text was"
# Every column of every table, which the lines above cannot say: they name the stores a run of
# 2026-11-12 measured, and a search of the whole database after a passing run of this driver found
# the secret in four more (doc_updates, text_trees, document_search and search_term_queue,
# measured 2026-11-13 at d92227ce05e3). This search names no store, so a store added later is
# searched without anyone adding a line here. It marks each row it finds as the planted nodes'
# or not, and a row that is not theirs can only derive from a node this driver erased.
held_anywhere E5 "$secret" "$other_comment" "$other_document" "$other_ref"
held="$(printf '%s\n' "$answer" | grep -v "^columns " | grep -v "^pg_temp_[0-9]*\.erase_control\.body " || true)"
derived="$(printf '%s\n' "$held" | awk 'NF == 3 && $2 > 0' || true)"
[[ -z "$derived" ]] ||
  fail E5 "no row that derives from an erased node still holds the erased text" "$(printf '%s' "$derived" | tr '\n' ';')"
printf '%s\n' "$held" | grep -Eq '^public\.comments\.body 0 [1-9][0-9]*$' ||
  fail E5 "the search finds the planted comment holding the bytes, marked as another node's" "$(printf '%s' "$held" | tr '\n' ';')"
printf '%s\n' "$held" | grep -Eq '^public\.artifact_versions\.markdown 0 [1-9][0-9]*$' ||
  fail E5 "the search finds the planted document holding the bytes, marked as another node's" "$(printf '%s' "$held" | tr '\n' ';')"
printf '%s\n' "$answer" | grep -Eq "^pg_temp_[0-9]+\.erase_control\.body 1 0\$" ||
  fail E5 "the search finds the control row planted beside it" "the search answered: $(printf '%s' "$answer" | tr '\n' ';')"
pass E5 "no row that derives from an erased node holds the erased text ($(printf '%s\n' "$answer" | sed -n 's/^columns //p') columns searched); every row the search found holding it is the planted nodes': $(printf '%s' "$held" | tr '\n' ';'); and the same search found the control row planted in it"
# What the event log holds instead, which the lines above cannot say: the marker naming each erase
# (grapherase.eraseMarker, one constant), through which a reader of a redacted payload reaches the
# `graph_erasures` row recording the run, in every row its listing named. Each row that held the
# secret has the id, the time and the actor it had before, because R5 moves the payload and
# nothing else.
for erased in "$node|$work/comment.listing" "$block_node|$work/block.listing"; do
  marker="[erased by envoy-dispatch erase ${erased%%|*}, recorded in graph_erasures]"
  while read -r event; do
    [[ -n "$event" ]] || continue
    want_rows E5 "event $event, which the listing of ${erased%%|*} named, holds its marker and no erased text" 1 \
      "select count(*) from events where id = $event and position('$marker' in payload::text) > 0
         and position('$secret' in payload::text) = 0"
  done < <(listed_events "${erased#*|}")
done
query E5 "read the id, time and actor of each event row that held the secret" \
  "select coalesce(jsonb_agg(jsonb_build_object('id', id, 'created_at', created_at, 'actor', actor) order by id), '[]'::jsonb)::text
     from events where id in (select (value->>'id')::bigint from jsonb_array_elements('$events_before'::jsonb))"
[[ "$answer" == "$events_before" ]] ||
  fail E5 "the event rows that held the secret stand, their id, time and actor untouched" \
    "now $answer, before the erases $events_before"
pass E5 "the event rows that held the secret stand, each with its id, its time and its actor unchanged"
# Each erase waits out the bound of a read's detached store of its display trees after its commit
# and deletes once more the trees it deleted (grapherase's sweepTrees), so a read that began
# before the commit cannot leave the tree of an erased text behind; each report says how many it
# found then.
for report in "$work/comment.erased" "$work/block.erased"; do
  grep -Eq "^  text_trees swept after the commit: [0-9]+\$" "$report" ||
    fail E5 "the run says how many display trees it swept after the commit" "no sweep line in: $(<"$report")"
done
pass E5 "envoy-dispatch erase reported its sweep of text_trees after each commit"

# E6: the record of each erase: one row each, read whole.
for erased in "comment|$comment" "block|$block"; do
  want_rows E6 "the erase of ${erased%%|*}:${erased#*|} recorded one graph_erasures row" 1 \
    "select count(*) from graph_erasures where node_kind = '${erased%%|*}' and node_id = '${erased#*|}'"
  query E6 "read the graph_erasures row of ${erased%%|*}:${erased#*|}" \
    "select jsonb_build_object('node_kind', node_kind, 'node_id', node_id, 'removed', removed, 'ran_by', ran_by, 'ran_at', ran_at)::text
       from graph_erasures where node_kind = '${erased%%|*}' and node_id = '${erased#*|}'"
  [[ -n "$answer" ]] || fail E6 "read the graph_erasures row of ${erased%%|*}:${erased#*|}" "the erase recorded nothing"
  printf '%s' "$answer" >"$work/erasure.${erased%%|*}.json"
  jq -e --arg kind "${erased%%|*}" --arg id "${erased#*|}" '.node_kind == $kind and .node_id == $id' \
    "$work/erasure.${erased%%|*}.json" >/dev/null ||
    fail E6 "the record names the node" "$(<"$work/erasure.${erased%%|*}.json")"
  jq -e '(.removed.rows | length) > 0' "$work/erasure.${erased%%|*}.json" >/dev/null ||
    fail E6 "the record holds the rows it removed" "$(<"$work/erasure.${erased%%|*}.json")"
  jq -e '(.ran_by.database_user | length) > 0 and (.ran_at | length) > 0' "$work/erasure.${erased%%|*}.json" >/dev/null ||
    fail E6 "the record holds who ran it and when" "$(<"$work/erasure.${erased%%|*}.json")"
  jq -e --arg s "$secret" 'tostring | contains($s) | not' "$work/erasure.${erased%%|*}.json" >/dev/null ||
    fail E6 "the record holds no erased text" "the graph_erasures row of ${erased%%|*}:${erased#*|} holds the erased secret"
  pass E6 "the graph_erasures row of ${erased%%|*}:${erased#*|} holds what was removed, who ran it and when, and no erased text"
done
# The version and the review the block's erase ended, each by its own name: a record that named
# some other version or review, or none, is not the record of this erase.
jq -e --arg doc "$document_ref" --arg key "$key" --argjson v "$version" \
  'any(.removed.versions[]; .document == $doc and .issue == $key and .version == $v)' \
  "$work/erasure.block.json" >/dev/null ||
  fail E6 "the record holds the version the erase ended ($document_ref version $version of $key)" \
    "$(jq -c '.removed.versions' "$work/erasure.block.json")"
jq -e --arg doc "$document_ref" --arg key "$key" --argjson v "$version" --arg r "$review" \
  'any(.removed.reviews[]; .id == $r and .document == $doc and .issue == $key and .version == $v)' \
  "$work/erasure.block.json" >/dev/null ||
  fail E6 "the record holds the review the erase ended (review $review of $document_ref version $version)" \
    "$(jq -c '.removed.reviews' "$work/erasure.block.json")"
jq -e '(.removed.versions | length) == 0 and (.removed.reviews | length) == 0' "$work/erasure.comment.json" >/dev/null ||
  fail E6 "the comment's record ends no version and no review" "$(<"$work/erasure.comment.json")"
pass E6 "the block's graph_erasures row holds $document_ref version $version and review $review, and the comment's ends none"

# E7: the erases wrote no node of their own, so every node left carries a supertag of the closed
# list the store itself holds.
want_rows E7 "every node carries a supertag the store declares" 0 \
  "select count(*) from graph_nodes n where not exists (select 1 from graph_nodes s where s.kind = 'supertag' and s.id = n.supertag)"
query E7 "count the supertag nodes" "select count(*) from graph_nodes where kind = 'supertag'"
[[ "$answer" == 29 ]] ||
  fail E7 "the closed list holds 29 supertags" "the store holds $answer supertag node(s)"
pass E7 "the closed list of 29 supertags is what every node's supertag comes from"

# E8: the census after the erases reads zero differences.
census_code=0
env -u DISPATCH_TOKEN -u DISPATCH_TOKEN_FILE DATABASE_URL="$database_url" \
  envoy-dispatch census --store >"$work/census" 2>&1 || census_code=$?
[[ "$census_code" == 0 ]] ||
  fail E8 "the store census after the erase" "envoy-dispatch census --store exited $census_code: $(cat "$work/census")"
grep -Eq '^census: store rows_checked=[0-9,]+ differences=0$' "$work/census" ||
  fail E8 "the store census after the erase" "$(grep -E '^census: store (rows_checked|REFUSED)' "$work/census" || true)"
# Zero differences over zero rows compares nothing with nothing, so the census must have read rows.
grep -Eq '^census: store rows_checked=[1-9][0-9,]* ' "$work/census" ||
  fail E8 "the store census read rows" "$(grep -E '^census: store rows_checked' "$work/census" || true)"
grep -Fqx 'census: store ok' "$work/census" ||
  fail E8 "the store census after the erase" "$(tail -n 3 "$work/census")"
pass E8 "envoy-dispatch census --store reads zero differences over $(sed -n 's/^census: store rows_checked=\([0-9,]*\) .*$/\1/p' "$work/census") rows"

# E9: no route serves the erase. Both a credential that reaches every write route and a person's
# own cookie read 404, which is what a path this server does not mount answers.
for path in "/api/v1/erase" "/api/v1/nodes/comment/$comment" "/api/v1/graph/erase"; do
  for method in POST DELETE; do
    send "$method" "$path" '{"node":"comment:x","confirm":true}'
    [[ "$status" == 404 ]] ||
      fail E9 "the agent bearer reaches no erase route" "$method $path answered HTTP $status: $(body)"
    person_send "$method" "$path" '{"node":"comment:x","confirm":true}'
    [[ "$status" == 404 ]] ||
      fail E9 "a person's cookie reaches no erase route" "$method $path answered HTTP $status: $(body)"
  done
done
pass E9 "the erase is reached by the command and the database credential alone: every HTTP path answers 404"

printf 'graph-erase.sh: every check passed\n'
