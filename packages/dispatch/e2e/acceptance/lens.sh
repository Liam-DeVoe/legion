#!/usr/bin/env bash
# Drives the lens of check c-lens (spec requirement r-lens of dispatch://INFRA-1098) against the
# `bun run dispatch:local` server, over the seeded corpus (lib-corpus.sh, which seeds it once where
# the database lacks it).
#
#   L1  The catalogue holds at least the ten kinds dispatch://INFRA-1950 seeds.
#   L2  It invokes a lens on the seeded spec node r-one once for each kind the catalogue holds:
#       each answers one lens node under r-one, carrying its author and its time, with its finding
#       hanging under it as its own child node; each is read back from the store by that parent.
#   L3  A lens carries its kind as one has_kind tuple to a lens_kind node, read from both ends, and
#       holds no kind as a value of its node. A second has_kind tuple on one lens, a lens with no
#       kind and a has_kind tuple at a word no lens_kind node holds are each refused.
#   L4  An eleventh kind is one more node of the store: `envoy-dispatch lens-kind-add` writes a
#       lens_kind node with its constraint node and that source's reference, a lens invoked with it
#       takes it on its has_kind tuple, and the block schema version, the types the server lists
#       and the rows of schema_migrations stay as they were.
#   L5  The seeded inversion lens holds a triggered_by tuple to the seeded pre-mortem lens, read
#       from both ends: the inversion answers the pre-mortem as the lens that triggered it, and the
#       pre-mortem answers the inversion among the lenses it triggered.
#   L6  A lens is invoked on a seeded comment, which is no spec, and read under that comment.
#   L7  It converts this issue's own pre-mortem: the fourth fixture's markdown as the document
#       graph-premortem.md of an issue it creates (slug graph-premortem-md), whose Verification
#       document holds one check. It writes a map file of eleven lines, one for each risk, of three
#       tab-separated fields - the risk's number, `none` or the check's address, a reason - risk 8
#       naming that check and every other risk, risk 3 among them, none; and converts with
#       `envoy-dispatch premortem-convert --issue <KEY> --artifact graph-premortem-md
#       --prevented-by <map> --dry-run` and then without --dry-run, DATABASE_URL a prefix of that
#       one command. The dry run leaves the issue's document list and lenses unchanged; the
#       conversion answers one lens of kind pre-mortem with eleven flat risks, each prevented by the
#       check its line names or by nothing. No document of the issue holds the pre-mortem, which the
#       same search finds once a copy is planted in one of them; the old address answers a redirect
#       to the lens, which the run follows; and the retired document's first version is read by its
#       number.
#
# Inputs: DATABASE_URL (the database the server at DISPATCH_URL serves, on this machine: the
# corpus's loader, `envoy-dispatch premortem-convert` and `envoy-dispatch lens-kind-add` write it
# and psql reads it), DISPATCH_URL (the server), DISPATCH_TOKEN or DISPATCH_TOKEN_FILE (an agent
# bearer, read here and never printed) and PROJECT (the project the corpus is seeded into, CORE).
# envoy-dispatch is the one on PATH, this head's build. Each unmet input is exit 2. Every route
# write is the agent bearer's, acting for a session of this run's own. The lenses it invokes on the
# seeded issue's r-one and on its comment, and the kind it adds, stay, since no route removes a
# kind and a gone lens stays a node; none of them is a node the loader's self-check counts. It
# closes the issue it creates, also when a check fails.
#
# Each passing check prints `PASS: <section> <what>`; the first failing one prints
# `FAIL: <section> <what>: <observed>` and the script exits 1. The first line after setup is
# `PASS: lens.sh the catalogue holds its ten kinds`, and the last line is `PASS: lens.sh complete`.
#
# Every check is `cmd || fail …`, never part of an `&&` list: under `set -e` a failing command
# anywhere but last in such a list does not stop the script.

# shellcheck disable=SC2016 # the single-quoted jq filters and SQL name jq variables, not shell ones
set -euo pipefail
set +x

usage() {
  cat >&2 <<'USAGE'
Usage: DATABASE_URL=postgres://…@127.0.0.1:5432/<db>?sslmode=disable DISPATCH_URL=http://127.0.0.1:8777 \
         DISPATCH_TOKEN=e2e-token PROJECT=CORE lens.sh
       (DISPATCH_TOKEN_FILE=<file holding the bearer> in place of DISPATCH_TOKEN; DATABASE_URL names
       the database the server at DISPATCH_URL serves, on this machine; envoy-dispatch on PATH)
USAGE
}

# shellcheck source-path=SCRIPTDIR source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
# shellcheck source-path=SCRIPTDIR source=lib-corpus.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib-corpus.sh"
require_env DATABASE_URL DISPATCH_URL PROJECT
read_bearer
require_commands cmp curl envoy-dispatch grep jq psql

readonly driver=lens.sh
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
fixture="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../fixtures" && pwd)/acme-1098-premortem.json"
readonly fixture
[[ -r "$fixture" ]] || {
  printf 'the capture %s cannot be read\n' "$fixture" >&2
  exit 2
}
stamp="$(date +%s)-$$"
readonly stamp
readonly session="lens-$stamp"
# A word no lens_kind node holds, and the eleventh kind this run adds: slugs the catalogue's ids
# take the form of, named for this run.
readonly unheld_kind="unheld-kind-${stamp//-/x}" added_kind="eleventh-kind-${stamp//-/x}"

work_dir
printf 'Authorization: Bearer %s\n' "$token" >"$work/auth"

created=""
cleanup() {
  local code=$?
  [[ -z "$created" ]] || close_issue "$created"
  rm -rf "$work"
  exit "$code"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# lenses_on SECTION WHAT ADDRESS: GET /api/v1/lenses?on=ADDRESS.
lenses_on() {
  send GET "/api/v1/lenses?on=$(q "$3")"
  want_status 200 "$1" "$2"
}

# references SECTION WHAT DIRECTION ADDRESS [KIND]: GET /api/v1/references with DIRECTION (from or
# to) at ADDRESS, kept to the edge kind KIND where it is given.
references() {
  send GET "/api/v1/references?$3=$(q "$4")${5:+&kind=$5}"
  want_status 200 "$1" "$2"
}

# has_kinds SECTION LENS: the far ends of the lens LENS's has_kind tuples the store asserts now,
# by id, sorted and comma-separated, in $answer.
has_kinds() {
  query "$1" "read the has_kind tuples of $2" "select coalesce(string_agg(to_id, ',' order by to_id), '') from (
      select distinct on (to_kind, to_id) to_id, asserted from graph_tuples
      where from_kind = 'lens' and from_id = '${2#dispatch://lens/}' and kind = 'has_kind' order by to_kind, to_id, seq desc) latest
    where asserted"
}

# pointer SECTION LENS BODY: one pointer tuple out of the lens LENS (POST
# /api/v1/issues/{key}/nodes/{id}/pointers), the answer left as it came.
pointer() {
  send POST "/api/v1/issues/$key/nodes/$(q "lens/${2#dispatch://lens/}")/pointers" "$3"
}

# documents SECTION KEY FILE: the issue KEY's document list, each document by its address, slug and
# the numbers of its versions, sorted, in FILE.
documents() {
  send GET "/api/v1/issues/$2/artifacts"
  want_status 200 "$1" "read the document list of $2"
  jq -cS '[.[] | {ref_key, slug, versions: [.versions[].number]}] | sort_by(.ref_key)' "$work/body" >"$3" ||
    fail "$1" "read the document list of $2" "unreadable: $(body)"
}

# holders SECTION KEY NEEDLE: the slugs of the documents of KEY whose text holds NEEDLE, in
# $holders, with how many documents the search read in $searched.
holders() {
  local held
  documents "$1" "$2" "$work/documents-searched.json"
  holders="" searched=0
  for held in $(jq -r '.[].slug' "$work/documents-searched.json"); do
    send GET "/api/v1/issues/$2/artifacts/$held/text"
    want_status 200 "$1" "read the text of $2's document $held"
    searched=$((searched + 1))
    if jq -r '.markdown // ""' "$work/body" | grep -qF -- "$3"; then
      holders="${holders:+$holders,}$held"
    fi
  done
}

# convert ARGS…: `envoy-dispatch premortem-convert ARGS` with DATABASE_URL as a prefix of that one
# command, its report in $work/convert.out and its errors in $work/convert.err; sets
# convert_code.
convert() {
  convert_code=0
  DATABASE_URL="$database_url" envoy-dispatch premortem-convert "$@" >"$work/convert.out" 2>"$work/convert.err" ||
    convert_code=$?
}

# --- setup: the corpus ------------------------------------------------------------------------------

corpus_ensure setup
key="$corpus_issue"
readonly key
readonly r_one="dispatch://$key/spec#b-r-one"

# --- L1: the catalogue ------------------------------------------------------------------------------

seeded_catalogue
kinds="$(jq -c '[.lens_kinds[].id]' "$work/catalogue.json")"
readonly kinds
pass L1 "the catalogue holds $(jq 'length' <<<"$kinds") kinds: $kinds"

# --- L2: one lens of each kind on the seeded spec node r-one ---------------------------------------

invoked=()
for kind in $(jq -r '.[]' <<<"$kinds"); do
  invoke L2 "invoke a $kind lens on r-one with one finding" agent "$(jq -cn --arg on "$r_one" --arg k "$kind" --argjson a "$(actor)" \
    '{on: $on, kind: $k, verdict: "A verdict of the \($k) lens on r-one.", findings: ["The finding of the \($k) lens."], actor: $a}')"
  expect L2 "$lens answers one $kind lens under r-one, by this run's session, at its time" \
    "$(jq -cnS --arg k "$kind" --arg on "$r_one" --arg s "$session" '{author: {id: $s, kind: "session"}, kind: $k, on: $on, timed: true}')" \
    '{author, kind, on, timed: ((.created_at // "") != "")}'
  invoked+=("$lens_id")
done
readonly invoked
ids="$(printf '%s\n' "${invoked[@]}" | jq -Rnc '[inputs]')"
with_vars L2 "read the lenses of this run back from the store by r-one, their parent" \
  "with mine as (select value as id from json_array_elements_text(:'ids'::json)),
     r_one as (select to_id from graph_tuples where from_kind = 'lens' and from_id = (select id from mine limit 1) and kind = 'part_of')
   select json_build_object(
     'under_r_one', (select count(distinct t.from_id) from graph_tuples t join mine m on m.id = t.from_id
        where t.from_kind = 'lens' and t.kind = 'part_of' and t.to_kind = 'block' and t.to_id in (select to_id from r_one) and t.asserted),
     'parent', (select min(to_id) from r_one),
     'timed', (select count(*) from graph_nodes n join mine m on m.id = n.id where n.kind = 'lens' and n.made_at is not null and n.made_by->>'id' = :'session'),
     'authored', (select count(distinct t.from_id) from graph_tuples t join mine m on m.id = t.from_id
        where t.from_kind = 'lens' and t.kind = 'authored_by' and t.to_kind = 'session' and t.to_id = :'session' and t.asserted),
     'findings', (select count(*) from graph_tuples t join mine m on t.to_id = m.id
        where t.from_kind = 'block' and t.from_id like 'lens/' || m.id || '#%' and t.kind = 'part_of' and t.to_kind = 'lens' and t.asserted))" \
  "ids=$ids" "session=$session"
count="${#invoked[@]}"
jq -e --argjson n "$count" --arg k "$key" '.under_r_one == $n and .timed == $n and .authored == $n and .findings == $n and .parent == "\($k)/tree#r-one"' \
  <<<"$answer" >/dev/null ||
  fail L2 "the store holds each of the $count lenses under r-one, made by this run's session at its time, with its finding under it" "it holds $answer"
pass L2 "the store holds each of the $count lenses under r-one, made by this run's session at its time, with its finding under it: $answer"

# --- L3: the kind is one has_kind tuple ------------------------------------------------------------

first_lens="dispatch://lens/${invoked[0]}"
first_kind="$(jq -r '.[0]' <<<"$kinds")"
readonly first_lens first_kind
references L3 "read the has_kind tuples out of $first_lens" from "$first_lens" has_kind
expect L3 "$first_lens holds one has_kind tuple, to lens_kind $first_kind" \
  "[[\"lens_kind\",\"dispatch://lens_kind/$first_kind\"]]" '[.edges[] | [.node.kind, .node.ref]]'
references L3 "read the backlinks of lens_kind $first_kind" to "dispatch://lens_kind/$first_kind" has_kind
expect L3 "the lens_kind node $first_kind answers $first_lens among the lenses that hold it" 1 \
  --arg l "$first_lens" '[.edges[] | select(.node.ref == $l)] | length'
query L3 "read the values of $first_lens's node" "select coalesce(json_agg(distinct kind), '[]') from graph_tuples
  where from_kind = 'lens' and from_id = '${invoked[0]}' and to_kind = 'value'"
jq -e '(index("kind") == null) and (index("has_kind") == null) and length > 0' <<<"$answer" >/dev/null ||
  fail L3 "$first_lens holds no kind as a value of its node" "its values are $answer"
pass L3 "$first_lens holds no kind as a value of its node: its values are $answer"

second_kind="$(jq -r '.[1]' <<<"$kinds")"
pointer L3 "$first_lens" "$(jq -cn --arg k "$second_kind" --argjson a "$(actor)" '{kind: "has_kind", action: "append", to: $k, actor: $a}')"
cp "$work/body" "$work/refusal.json"
refused_status="$status"
has_kinds L3 "$first_lens"
[[ "$answer" == "$first_kind" ]] ||
  fail L3 "a second has_kind tuple on $first_lens is refused" "it holds $answer after the write, which answered HTTP $refused_status: $(<"$work/refusal.json")"
cp "$work/refusal.json" "$work/body"
status="$refused_status"
want_status 409 L3 "a second has_kind tuple on $first_lens"
expect L3 "a second has_kind tuple on $first_lens is refused POINTER_ALREADY_ASSERTED, and the lens keeps its one kind" \
  '"POINTER_ALREADY_ASSERTED"' '.code'

lenses_on L3 "read the lenses on r-one before the refused writes" "$r_one"
jq -c '[.lenses[].address] | sort' "$work/body" >"$work/r-one-before.json"
send POST "/api/v1/issues/$key/lenses" "$(jq -cn --arg on "$r_one" --argjson a "$(actor)" '{on: $on, findings: ["A finding of no kind."], actor: $a}')"
want_status 400 L3 "a lens with no kind"
expect L3 "a lens with no kind is refused LENS_KIND_REQUIRED" '"LENS_KIND_REQUIRED"' '.code'
send POST "/api/v1/issues/$key/lenses" "$(jq -cn --arg on "$r_one" --arg k "$unheld_kind" --argjson a "$(actor)" '{on: $on, kind: $k, actor: $a}')"
want_status 400 L3 "a lens of the word $unheld_kind"
expect L3 "a has_kind tuple at $unheld_kind, a word no lens_kind node holds, is refused INVALID_POINTER" '"INVALID_POINTER"' '.code'
lenses_on L3 "read the lenses on r-one after the refused writes" "$r_one"
jq -c '[.lenses[].address] | sort' "$work/body" >"$work/r-one-after.json"
cmp -s "$work/r-one-before.json" "$work/r-one-after.json" ||
  fail L3 "the refused writes stored no lens on r-one" "before: $(<"$work/r-one-before.json"); after: $(<"$work/r-one-after.json")"
pass L3 "the refused writes stored no lens on r-one: it holds the $(jq 'length' "$work/r-one-after.json") lenses it held"

# --- L4: an eleventh kind is one more node ---------------------------------------------------------

readonly kind_source="Edward Tufte, The Visual Display of Quantitative Information, Graphics Press, 1983"
readonly kind_reference='Cheshire, CT: Graphics Press, 1983; ch. 4, p. 93'
readonly schema_read='{version, store_types: [.store_types[].name] | sort, types: [.types[].name] | sort}'
jq -cS "$schema_read" "$work/catalogue.json" >"$work/schema-before.json"
readonly migrations_sql="select coalesce(json_agg(version order by version), '[]') from schema_migrations"
query L4 "read the rows of schema_migrations before the add" "$migrations_sql"
readonly migrations_before="$answer"
DATABASE_URL="$database_url" envoy-dispatch lens-kind-add --id "$added_kind" --words "the data-ink check" \
  --source "$kind_source" --reference "$kind_reference" >"$work/kind.out" 2>&1 ||
  fail L4 "envoy-dispatch lens-kind-add --id $added_kind adds the kind" "it exited $?: $(<"$work/kind.out")"
pass L4 "envoy-dispatch lens-kind-add added the eleventh kind: $(<"$work/kind.out")"
send GET /api/v1/schema/blocks
want_status 200 L4 "read the catalogue after the add"
cp "$work/body" "$work/catalogue-after.json"
expect L4 "the catalogue holds $added_kind, with its constraint node and that source's reference" \
  "$(jq -cnS --arg id "$added_kind" --arg s "$kind_source" --arg r "$kind_reference" '[{id: $id, sources: [{reference: $r, standard: $s}]}]')" \
  --arg id "$added_kind" '[.lens_kinds[] | select(.id == $id) | {id, sources: [.sources[] | {standard, reference}]}]'
want_value L4 "the store holds the kind as one lens_kind node with a constrained_by tuple to its constraint node" 1 \
  "select count(*) from graph_nodes k join graph_tuples t on t.from_kind = 'lens_kind' and t.from_id = k.id and t.to_kind = 'constraint' and t.asserted
     where k.kind = 'lens_kind' and k.id = '$added_kind'"
invoke L4 "invoke a lens of $added_kind on r-one" agent "$(jq -cn --arg on "$r_one" --arg k "$added_kind" --argjson a "$(actor)" '{on: $on, kind: $k, actor: $a}')"
has_kinds L4 "$lens"
[[ "$answer" == "$added_kind" ]] || fail L4 "the lens takes $added_kind on its has_kind tuple" "its has_kind tuples reach $answer"
pass L4 "the lens $lens takes $added_kind on its has_kind tuple"
cp "$work/catalogue-after.json" "$work/body"
expect L4 "no block schema version and no type the server lists moved for $added_kind" "$(<"$work/schema-before.json")" "$schema_read"
query L4 "read the rows of schema_migrations after the add" "$migrations_sql"
[[ "$answer" == "$migrations_before" ]] || fail L4 "no migration ran for $added_kind" "before: $migrations_before; after: $answer"
pass L4 "no migration ran for $added_kind: schema_migrations holds the $(jq 'length' <<<"$answer") rows it held"

# --- L5: one lens triggered another ----------------------------------------------------------------

lenses_on L5 "read the lenses on $key" "dispatch://$key"
premortem="$(jq -r '[.lenses[] | select(.kind.id == "pre-mortem") | .address] | first // empty' "$work/body")"
inversion="$(jq -r '[.lenses[] | select(.kind.id == "inversion") | .address] | first // empty' "$work/body")"
[[ -n "$premortem" && -n "$inversion" ]] || fail L5 "the seeded issue holds its pre-mortem and its inversion lens" "$(body)"
readonly premortem inversion
expect L5 "the inversion answers the pre-mortem as the lens that triggered it, and the pre-mortem answers the inversion among those it triggered" \
  '{"inversion_triggered_by":true,"premortem_triggered":true}' --arg p "$premortem" --arg i "$inversion" \
  '{inversion_triggered_by: ((.lenses[] | select(.address == $i) | .triggered_by) == [$p]),
    premortem_triggered: ((.lenses[] | select(.address == $p) | .triggered) | index($i) != null)}'
references L5 "read the triggered_by tuple out of $inversion" from "$inversion" triggered_by
expect L5 "the tuple triggered_by runs from $inversion to $premortem" "[\"$premortem\"]" '[.edges[] | .node.ref]'
references L5 "read the triggered_by backlinks of $premortem" to "$premortem" triggered_by
expect L5 "$premortem answers $inversion through that tuple from its own end" 1 --arg i "$inversion" '[.edges[] | select(.node.ref == $i)] | length'

# --- L6: a lens on a node that is no spec -----------------------------------------------------------

query L6 "read a seeded comment of $key" "select id from comments where issue_key = '$key' and reply_to is null and author->>'kind' = 'user' order by created_at limit 1"
comment="dispatch://$key/comment/$answer"
readonly comment
invoke L6 "invoke an inversion lens on the comment $comment" agent "$(jq -cn --arg on "$comment" --argjson a "$(actor)" '{on: $on, kind: "inversion", actor: $a}')"
lenses_on L6 "read the lenses on $comment" "$comment"
expect L6 "$comment answers as a comment, and the lens invoked on it stands under it" \
  "$(jq -cn --arg l "$lens" '{kind: "comment", lens: 1}')" --arg l "$lens" '{kind: .on.kind, lens: ([.lenses[] | select(.address == $l)] | length)}'

# --- L7: this issue's own pre-mortem, converted ------------------------------------------------------

jq -j '.document.markdown' "$fixture" >"$work/premortem.md"
readonly document_name="graph-premortem.md" slug="graph-premortem-md"
[[ "$(jq -r '.document.name' "$fixture")" == "$document_name" ]] ||
  fail L7 "the fourth fixture records the document $document_name" "it records $(jq -c '.document.name' "$fixture")"
needle="$(jq -r '.risks[1].title | split(". ")[1:] | join(". ")' "$fixture")"
readonly needle
[[ -n "$needle" ]] || fail L7 "the fixture names a heading for risk 1, the search's needle" "it names none"
grep -qF -- "$needle" "$work/premortem.md" ||
  fail L7 "the fixture's risk 1 heading is a line of its markdown, the search's needle" "needle '$needle'"
send POST /api/v1/issues "$(jq -cn --arg p "$project" --argjson a "$(actor)" '{project: $p, title: "lens.sh: the graph model'"'"'s own pre-mortem, converted",
  force: true, actor: $a, spec: "The issue whose document graph-premortem.md lens.sh converts into nodes."}')"
want_status 201 L7 "create the issue the pre-mortem is converted on"
created="$(jq -er '.key' "$work/body")" || fail L7 "create the issue the pre-mortem is converted on" "no key: $(body)"
readonly created
send POST "/api/v1/issues/$created/artifacts" "$(jq -cn --arg k "$created" --argjson a "$(actor)" '{name: "verification.md", actor: $a,
  content: "How \($k) is proved.\n\n:::check{#c-guard}\nThe check that keeps the graph store and the block index telling one truth.\n:::\n"}')"
want_status 201 L7 "write $created's Verification document, holding one check"
readonly check_ref="dispatch://$created/artifact/verification-md#b-c-guard"
send GET "/api/v1/issues/$created/artifacts/verification-md/blocks"
want_status 200 L7 "read the blocks of $created's Verification document"
expect L7 "$created's Verification document holds the check c-guard, $check_ref" '["check"]' '[.[] | select(.id == "c-guard") | .type]'
# The map: one line for each risk the fixture numbers, risk 8 naming the check of this issue and
# every other risk, risk 3 among them, none, each with its reason.
readonly map="$work/map.tsv"
jq -r --arg c "$check_ref" '.risks[].index | if . == 8 then "8\t\($c)\tc-guard keeps the two indexers from drifting apart."
    elif . == 3 then "3\tnone\tNo check of this issue prevents risk 3."
    else "\(.)\tnone\tNo check of this issue prevents risk \(.)." end' "$fixture" >"$map" ||
  fail L7 "write the map file" "jq refused $fixture"
[[ "$(wc -l <"$map")" -eq 11 && "$(awk -F '\t' 'NF == 3' "$map" | wc -l)" -eq 11 ]] ||
  fail L7 "the map holds eleven lines of three tab-separated fields" "it holds: $(<"$map")"
[[ "$(awk -F '\t' '$1 == "8" { print $2 }' "$map")" == "$check_ref" && "$(awk -F '\t' '$1 == "3" { print $2 }' "$map")" == none ]] ||
  fail L7 "the map names $check_ref for risk 8 and none for risk 3" "it holds: $(<"$map")"
pass L7 "wrote the map file of eleven lines: risk 8 names $check_ref, risk 3 and every other risk none"
send POST "/api/v1/issues/$created/artifacts" "$(jq -cn --arg n "$document_name" --rawfile c "$work/premortem.md" --argjson a "$(actor)" \
  '{name: $n, content: $c, actor: $a}')"
want_status 201 L7 "write $document_name on $created"
expect L7 "the document's slug is $slug" "\"$slug\"" '.artifact.slug'
readonly document="dispatch://$created/artifact/$slug"

documents L7 "$created" "$work/documents-before.json"
lenses_on L7 "read the lenses on $created before the dry run" "dispatch://$created"
jq -cS '.' "$work/body" >"$work/lenses-before.json"
convert --issue "$created" --artifact "$slug" --prevented-by "$map" --dry-run
[[ "$convert_code" == 0 ]] ||
  fail L7 "envoy-dispatch premortem-convert --issue $created --artifact $slug --prevented-by <map> --dry-run exits 0" \
    "it exited $convert_code: $(<"$work/convert.err") $(head -c 600 "$work/convert.out")"
cp "$work/convert.out" "$work/body"
expect L7 "the dry run reports what it would write and writes nothing: a pre-mortem lens with eleven risks, at no address" \
  '{"address":null,"dry_run":true,"kind":"pre-mortem","risks":11}' '{dry_run, address: .lens.address, kind: .lens.kind, risks: (.risks | length)}'
documents L7 "$created" "$work/documents-after.json"
cmp -s "$work/documents-before.json" "$work/documents-after.json" ||
  fail L7 "the dry run leaves $created's document list unchanged" "before: $(<"$work/documents-before.json"); after: $(<"$work/documents-after.json")"
pass L7 "the dry run leaves $created's document list unchanged: $(jq -c '[.[].slug]' "$work/documents-after.json")"
lenses_on L7 "read the lenses on $created after the dry run" "dispatch://$created"
jq -cS '.' "$work/body" >"$work/lenses-after.json"
cmp -s "$work/lenses-before.json" "$work/lenses-after.json" ||
  fail L7 "the dry run leaves $created's lenses unchanged" "before: $(<"$work/lenses-before.json"); after: $(<"$work/lenses-after.json")"
pass L7 "the dry run leaves $created's lenses unchanged: $(jq -c '[.lenses[].address]' "$work/lenses-after.json")"

convert --issue "$created" --artifact "$slug" --prevented-by "$map"
[[ "$convert_code" == 0 ]] ||
  fail L7 "envoy-dispatch premortem-convert --issue $created --artifact $slug --prevented-by <map> exits 0" \
    "it exited $convert_code: $(<"$work/convert.err") $(head -c 600 "$work/convert.out")"
converted="$(jq -er '.lens.address' "$work/convert.out")" || fail L7 "the conversion reports its lens" "$(head -c 600 "$work/convert.out")"
readonly converted
pass L7 "envoy-dispatch premortem-convert converted $document into $converted"
lenses_on L7 "read the lenses on $created" "dispatch://$created"
jq -c --arg l "$converted" '.lenses[] | select(.address == $l)' "$work/body" >"$work/converted.json"
expect L7 "$created's own pre-mortem is one lens node of kind pre-mortem" "[[\"$converted\",\"pre-mortem\"]]" '[.lenses[] | [.address, .kind.id]]'
cp "$work/converted.json" "$work/body"
risks="$(jq -c '[.risks[].address]' "$work/converted.json")"
[[ "$(jq 'length' <<<"$risks")" == 11 ]] || fail L7 "eleven risk nodes hang under $converted" "it holds $risks"
pass L7 "eleven risk nodes hang under $converted"
with_vars L7 "read where each risk hangs and what it holds under it" \
  "with risks as (select replace(value, 'dispatch://risk/', '') as id from json_array_elements_text(:'risks'::json))
   select json_build_object('flat', (select count(*) from graph_tuples t join risks r on r.id = t.from_id
       where t.from_kind = 'risk' and t.kind = 'part_of' and t.to_kind = 'lens' and t.to_id = :'lens' and t.asserted),
     'under_a_risk', (select count(*) from graph_tuples t join risks r on r.id = t.to_id where t.to_kind = 'risk' and t.kind = 'part_of' and t.from_kind = 'risk'))" \
  "risks=$risks" "lens=${converted#dispatch://lens/}"
jq -e '.flat == 11 and .under_a_risk == 0' <<<"$answer" >/dev/null ||
  fail L7 "the eleven risks are flat: each hangs from the lens and none holds another risk under it" "the store holds $answer"
pass L7 "the eleven risks are flat: each hangs from the lens and none holds another risk under it: $answer"
risk_8="$(jq -r '.risks[] | select(.number == 8) | .address' "$work/convert.out")"
risk_3="$(jq -r '.risks[] | select(.number == 3) | .address' "$work/convert.out")"
readonly risk_8 risk_3
expect L7 "each risk answers the check it points at through prevented_by, or nothing: risk 8 answers $check_ref and every other risk, risk 3 among them, nothing" \
  "$(jq -cn --arg r "$risk_8" --arg c "$check_ref" --arg t "$risk_3" '{risk_3: [], with_check: [[$r, [$c]]]}')" \
  --arg t "$risk_3" '{with_check: [.risks[] | select(.prevented_by != []) | [.address, .prevented_by]], risk_3: (.risks[] | select(.address == $t) | .prevented_by)}'
references L7 "read the prevented_by tuple of risk 8" from "$risk_8" prevented_by
expect L7 "risk 8's prevented_by tuple reaches $check_ref" "[\"$check_ref\"]" '[.edges[] | .node.ref]'

holders L7 "$created" "$needle"
[[ "$searched" -gt 0 && -z "$holders" ]] ||
  fail L7 "no document of $created holds the pre-mortem" "the search read $searched documents and found it in: ${holders:-none}"
pass L7 "no document of $created holds the pre-mortem: the search read $searched documents"
send POST "/api/v1/issues/$created/artifacts" "$(jq -cn --rawfile c "$work/premortem.md" --argjson a "$(actor)" '{name: "planted-copy.md", content: $c, actor: $a}')"
want_status 201 L7 "plant a copy of the pre-mortem in a document of $created"
holders L7 "$created" "$needle"
[[ "$holders" == planted-copy-md ]] ||
  fail L7 "the same search finds the copy planted in planted-copy.md" "it read $searched documents and found it in: ${holders:-none}"
pass L7 "the same search finds the copy planted in planted-copy.md, among $searched documents"

references L7 "read $document, the retired document's old address" to "$document"
expect L7 "$document answers a redirect to the lens node $converted" "$(jq -cn --arg l "$converted" '{kind: "lens", ref: $l}')" '.node | {kind, ref}'
lenses_on L7 "follow the redirect of $document" "$(jq -r '.node.ref' "$work/body")"
expect L7 "the redirect reaches $converted and its eleven risks" "$(jq -cn --arg l "$converted" '{address: $l, risks: 11}')" \
  '{address: .lens.address, risks: (.lens.risks | length)}'
send GET "/api/v1/issues/$created/artifacts/$slug/versions/1"
want_status 200 L7 "read the first version of the retired document by its number, through its slug $slug"
expect L7 "the retired document keeps its slug, which the read went through, and its first version, the fixture's markdown" \
  '{"number":1,"same":true}' --rawfile md "$work/premortem.md" '{number, same: ((.content // .markdown // .text) == $md)}'

pass "$driver" complete
