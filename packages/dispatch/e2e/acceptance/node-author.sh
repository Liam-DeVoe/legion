#!/usr/bin/env bash
# Drives what an author is, from both sides, against a running server (dispatch://INFRA-1950's
# c-agent-bearer-has-no-author, dispatch://INFRA-1950/spec#b-r-author). On the deployment's shared
# agent bearer, which every holder presents alike, it writes a lens on a spec and reads it back at
# `project`; asks for `author` on a lens write and is refused 403 AUTHOR_NAMES_NOBODY, the words of
# graphstore.ErrAuthorNamesNobody, with no lens stored; and is refused the going POST with 403
# GOING_ASKER_NAMES_NOBODY. Both refusals are the one rule, graphstore.ProvesAuthor, which
# requireOneAuthorRule (packages/envoy/internal/dispatch/api/node_visibility_test.go) holds the
# source to, so this driver greps no source.
#
# Then two people, the one DEV_SIGNIN_LOGIN names and bob, each sign in through the dev sign-in
# route and mint a personal token with that cookie (POST /api/v1/me/agent-tokens with a name and no
# scopes, as e2e/api.ts's mintAgentToken does). Each writes one lens reserved for its author with
# the cookie and one with the token, and reads each back as that person. Bob's token is refused the
# lens the first person's token wrote, though both tokens' writes name one session of this run, so
# the read tells model.Actor.SameCredential from SameAs. Last the shared bearer reads each of the
# four at its address and is answered what an address nobody holds answers, byte for byte once the
# id is substituted, and finds none of them in the spec's lenses, where it finds its own lens.
#
# Inputs: DISPATCH_URL (the server), DISPATCH_TOKEN or DISPATCH_TOKEN_FILE (the shared agent bearer,
# read here and never printed), PROJECT (an existing project key) and DEV_SIGNIN_LOGIN (the first
# person, whom the server's dev sign-in route, /auth/_dev/signin, signs in: `alice` on `bun run
# dispatch:local`); the second person is bob, whom the same route signs in. The bearer writes the
# issue and closes it when the run ends, and each person's cookie revokes the token it minted, also
# when a check fails. Each unmet input is exit 2.
#
# The first line is `PASS: node-author.sh the catalogue holds its ten kinds`, read before any lens
# is written. Each passing check prints `PASS: <section> <what>`; the first failing one prints
# `FAIL: <section> <what>: <observed>` and the script exits 1. The last line is
# `PASS: node-author.sh complete`.
#
# Every check is `cmd || fail …`, never part of an `&&` list: under `set -e` a failing command
# anywhere but last in such a list does not stop the script.

# shellcheck disable=SC2016 # the single-quoted jq filters name jq variables, not shell ones
set -euo pipefail
set +x

usage() {
  cat >&2 <<'USAGE'
Usage: DISPATCH_URL=http://127.0.0.1:8777 DISPATCH_TOKEN=e2e-token PROJECT=CORE \
         DEV_SIGNIN_LOGIN=alice node-author.sh
       (DISPATCH_TOKEN_FILE=<file holding the bearer> in place of DISPATCH_TOKEN; the bearer is
       the deployment's shared agent bearer, and the server must mount its dev sign-in route, as
       `bun run dispatch:local` does, for DEV_SIGNIN_LOGIN and for bob)
USAGE
}

# shellcheck source-path=SCRIPTDIR source=lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
require_env DISPATCH_URL PROJECT DEV_SIGNIN_LOGIN
require_commands curl jq sed
read_bearer

readonly driver=node-author.sh
# shellcheck disable=SC2031 # the DISPATCH_URL lib.sh's `cli` exports is its own; this one is the caller's
readonly base="${DISPATCH_URL%/}"
# The dashboard's origin, the scheme and host of DISPATCH_URL: a write the person's cookie
# authenticates must name it (enforceCookieOrigin).
base_host="${base#*://}"
readonly origin="${base%%://*}://${base_host%%/*}"
# shellcheck disable=SC2153 # PROJECT is the caller's input, checked by name above
readonly project="$PROJECT"
# The two people: the one DEV_SIGNIN_LOGIN names, and bob.
readonly first="$DEV_SIGNIN_LOGIN" second=bob
[[ "$first" != "$second" ]] || {
  printf 'DEV_SIGNIN_LOGIN is %s, the second person this driver signs in; name another login\n' "$second" >&2
  usage
  exit 2
}
stamp="$(date +%s)-$$"
readonly stamp
# The one session every bearer write and every personal token's write names in its body.
readonly session="node-author-$stamp"
# The ten kinds dispatch://INFRA-1297 records, which the catalogue's migration seeds.
readonly seeded_kinds='["best-practice","boring-solution","botec","conway","first-principles","inversion","pre-mortem","reversibility","second-order","trade-off"]'
# A lens id nobody ever wrote: the address whose answer an author-only lens is held to.
readonly missing="00000000-0000-4000-8000-000000000000"

work_dir
printf 'Authorization: Bearer %s\n' "$token" >"$work/auth"

key=""
# The id of the personal token each person minted, by login, which cleanup revokes.
declare -A minted=()
cleanup() {
  local code=$? login
  for login in "${!minted[@]}"; do
    request "cookie:$login" DELETE "/api/v1/me/agent-tokens/$(q "${minted[$login]}")"
    [[ "$status" == 2?? ]] ||
      printf '%s: could not revoke the token %s minted: HTTP %s: %s\n' "$driver" "$login" "$status" "$(body)" >&2
  done
  if [[ -n "$key" ]]; then
    request agent PATCH "/api/v1/issues/$key" \
      "$(jq -cn --arg s "$session" '{status: "done", actor: {kind: "session", id: $s}}')"
    [[ "$status" == 200 ]] ||
      printf '%s: could not close %s: HTTP %s: %s\n' "$driver" "$key" "$status" "$(body)" >&2
  fi
  rm -rf "$work"
  exit "$code"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# request AS METHOD PATH [JSON]: sends METHOD PATH as AS: `agent`, the shared bearer in $work/auth;
# `cookie:<login>`, the session cookie the dev sign-in left in $work/jar.<login> and the dashboard's
# origin, which the server requires of every write a cookie authenticates; or `token:<login>`, the
# personal token that person minted, in $work/auth.<login>. No token reaches an argument list. The
# answer's body goes to $work/body and its HTTP status to $status.
request() {
  local -a auth data=()
  case "$1" in
    cookie:*) auth=(-b "$work/jar.${1#cookie:}" -H "Origin: $origin") ;;
    token:*) auth=(-H "@$work/auth.${1#token:}") ;;
    *) auth=(-H "@$work/auth") ;;
  esac
  [[ $# -lt 4 ]] || data=(-H 'Content-Type: application/json' --data-binary "$4")
  status="$(curl -sS --max-time 60 -X "$2" -o "$work/body" -w '%{http_code}' "${auth[@]}" "${data[@]}" \
    "$base$3" 2>"$work/curl")" || status="curl failed: $(<"$work/curl")"
}

# actor: the session the bearer's and the personal tokens' writes name.
actor() { jq -cn --arg s "$session" '{kind: "session", id: $s}'; }

# lens_write VISIBILITY [ACTOR]: the body of a pre-mortem on the spec at VISIBILITY, naming ACTOR
# where one is given.
lens_write() {
  jq -cn --arg on "$spec" --arg v "$1" --arg a "${2:-}" \
    '{on: $on, kind: "pre-mortem", visibility: $v} + (if $a == "" then {} else {actor: ($a | fromjson)} end)'
}

# invoke SECTION WHAT AS BODY: AS invokes the lens BODY names (POST /api/v1/issues/{key}/lenses) and
# the answer is 201. It sets $lens to the lens's address and $lens_id to its uuid, and keeps the
# answer in $work/invoked.
invoke() {
  request "$3" POST "/api/v1/issues/$key/lenses" "$4"
  want_status 201 "$1" "$2"
  cp "$work/body" "$work/invoked"
  lens="$(jq -er '.address' "$work/invoked")" || fail "$1" "$2" "no address: $(body)"
  lens_id="$(jq -er '.id' "$work/invoked")" || fail "$1" "$2" "no id: $(body)"
  [[ "$lens" == "dispatch://lens/$lens_id" ]] ||
    fail "$1" "$2" "the lens answers the address $lens, want dispatch://lens/$lens_id"
  pass "$1" "$2 ($lens)"
}

# lenses_on AS ADDRESS: AS reads GET /api/v1/lenses?on=ADDRESS, the answer left as it came.
lenses_on() { request "$1" GET "/api/v1/lenses?on=$(q "$2")"; }

# spec_lenses SECTION WHAT AS FILE: AS reads the spec's lenses, and FILE keeps the address of each,
# in the order the read answers them.
spec_lenses() {
  lenses_on "$3" "$spec"
  want_status 200 "$1" "$2"
  jq -c '[.lenses[].address]' "$work/body" >"$4" || fail "$1" "$2" "unreadable: $(body)"
}

# sign_in LOGIN: signs LOGIN in through the dev sign-in route, keeping the cookie in $work/jar.LOGIN.
sign_in() {
  status="$(curl -sS --max-time 60 -o "$work/body" -w '%{http_code}' -c "$work/jar.$1" \
    "$base/auth/_dev/signin?login=$(q "$1")" 2>"$work/curl")" || status="curl failed: $(<"$work/curl")"
  want_status 302 setup "dev sign-in as $1"
  grep -q $'\tdsession\t' "$work/jar.$1" || fail setup "dev sign-in as $1" "the answer set no session cookie"
  pass setup "signed in as $1"
}

# mint_token LOGIN: LOGIN's cookie mints a personal token with a name and no scopes, so it holds
# that person's own scopes; its plaintext, the answer's `token`, goes to $work/auth.LOGIN as the
# header request sends, and its id to minted[LOGIN]. The answer, which holds the plaintext, is
# cleared once read, so no failure message prints it.
mint_token() {
  local plaintext scopes
  request "cookie:$1" POST /api/v1/me/agent-tokens "$(jq -cn --arg n "node-author $1 $stamp" '{name: $n}')"
  want_status 201 setup "mint a personal token for $1"
  plaintext="$(jq -er '.token | select(type == "string" and . != "")' "$work/body")" ||
    fail setup "mint a personal token for $1" "the answer holds no token"
  minted[$1]="$(jq -er '.id' "$work/body")" || fail setup "mint a personal token for $1" "the answer holds no id"
  scopes="$(jq -c '.scopes' "$work/body")"
  printf 'Authorization: Bearer %s\n' "$plaintext" >"$work/auth.$1"
  : >"$work/body"
  pass setup "minted a personal token for $1 with no scopes asked for; it holds $scopes"
}

# read_back SECTION WHAT AS ADDRESS LOGIN: AS reads the lens ADDRESS at its address and finds it,
# reserved for its author, LOGIN.
read_back() {
  lenses_on "$3" "$4"
  want_status 200 "$1" "$2"
  expect "$1" "$2: it answers at its address, reserved for its author, $5" \
    "$(jq -cnS --arg a "$4" --arg l "$5" '{address: $a, author: {id: $l, kind: "person"}, visibility: "author"}')" \
    '.lens | {address, author, visibility}'
}

# nobody_holds SECTION AS WHO: AS, whom WHO names, reads the lens address nobody ever wrote,
# dispatch://lens/$missing, and the answer - its status and its body - is kept as the one an
# author-only lens AS may not see is held to. It is no 200, so it tells apart a lens AS reads.
nobody_holds() {
  lenses_on "$2" "dispatch://lens/$missing"
  printf '%s' "$status" >"$work/nobody.status"
  cp "$work/body" "$work/nobody.body"
  [[ "$status" != 200 ]] ||
    fail "$1" "an address nobody holds answers $3 otherwise than a lens it reads" "HTTP 200: $(body)"
  pass "$1" "an address nobody holds, dispatch://lens/$missing, answers $3 HTTP $status: $(body)"
}

# as_nobody AS ADDRESS: AS reads the lens ADDRESS at its address, and the answer, its id
# substituted by $missing's, is the one nobody_holds kept, status and body byte for byte.
as_nobody() {
  lenses_on "$1" "$2"
  sed "s/${2#dispatch://lens/}/$missing/g" "$work/body" >"$work/substituted.body"
  [[ "$status" == "$(<"$work/nobody.status")" ]] || return 1
  cmp -s "$work/substituted.body" "$work/nobody.body"
}

# --- the catalogue ------------------------------------------------------------------------------

request agent GET /api/v1/schema/blocks
want_status 200 "$driver" "read the catalogue"
held="$(jq -cS --argjson want "$seeded_kinds" '[.lens_kinds[].id] as $ids | [$want[] | select(. as $id | $ids | index($id))]' \
  "$work/body")" || fail "$driver" "read the catalogue" "unreadable: $(body)"
[[ "$held" == "$seeded_kinds" ]] ||
  fail "$driver" "the catalogue holds its ten kinds" "it holds $held of $seeded_kinds"
pass "$driver" "the catalogue holds its ten kinds"

# --- setup --------------------------------------------------------------------------------------

sign_in "$first"
sign_in "$second"

request agent POST /api/v1/issues "$(jq -cn --arg p "$project" --argjson a "$(actor)" \
  '{project: $p, title: "Node author check", force: true, actor: $a,
    spec: "The spec each lens of this run is written on.\n\nIt holds one more paragraph."}')"
want_status 201 setup "create the issue"
key="$(jq -er '.key' "$work/body")" || fail setup "create the issue" "no key: $(body)"
readonly spec="dispatch://$key/spec"
pass setup "created $key in $project, whose spec is $spec"

mint_token "$first"
mint_token "$second"

# --- B1: the shared bearer writes a lens, and it is the project's --------------------------------

invoke B1 "the shared bearer writes a pre-mortem on the spec, asking for no visibility" agent \
  "$(jq -cn --arg on "$spec" --argjson a "$(actor)" '{on: $on, kind: "pre-mortem", actor: $a}')"
readonly bearer_lens="$lens"
cp "$work/invoked" "$work/body"
expect B1 "the write answers $bearer_lens at project, with no author" '{"author":null,"visibility":"project"}' \
  '{author, visibility}'
lenses_on agent "$bearer_lens"
want_status 200 B1 "the shared bearer reads $bearer_lens at its address"
expect B1 "$bearer_lens reads back at project, with no author" \
  "$(jq -cnS --arg a "$bearer_lens" '{address: $a, author: null, visibility: "project"}')" \
  '.lens | {address, author, visibility}'

# --- B2: the shared bearer asks for author and is refused, storing nothing -----------------------

spec_lenses B2 "the shared bearer reads the spec's lenses before it asks for author" agent "$work/before.json"
request agent POST "/api/v1/issues/$key/lenses" "$(lens_write author "$(actor)")"
[[ "$status" == 403 ]] ||
  fail B2 "the shared bearer's lens write asking for author is refused 403 AUTHOR_NAMES_NOBODY" \
    "it was answered HTTP $status and stored the lens $(jq -r '.address // "(none named)"' "$work/body" 2>/dev/null): $(body)"
pass B2 "the shared bearer's lens write asking for author is refused HTTP 403"
expect B2 "the refusal is AUTHOR_NAMES_NOBODY" '"AUTHOR_NAMES_NOBODY"' '.code'
expect B2 "its message holds names nobody, the words of graphstore.ErrAuthorNamesNobody" true \
  '.error | contains("names nobody")'
pass B2 "the refusal reads: $(jq -r '.error' "$work/body")"
spec_lenses B2 "the shared bearer reads the spec's lenses after the refusal" agent "$work/after.json"
cmp -s "$work/before.json" "$work/after.json" ||
  fail B2 "the refused write stored no lens on the spec" \
    "the spec held $(<"$work/before.json") and holds $(<"$work/after.json")"
expect B2 "the spec's lenses are the one the shared bearer wrote, and no new lens" \
  "$(jq -cn --arg a "$bearer_lens" '[$a]')" '[.lenses[].address]'

# --- B3: the going POST refuses that shared credential as its asker too ---------------------------

request agent POST /api/v1/going/requests "$(jq -cn --arg p "$project" '{words: "Who wrote the spec?", project: $p}')"
want_status 403 B3 "the going POST refuses the shared bearer as its asker"
expect B3 "the refusal is GOING_ASKER_NAMES_NOBODY" '"GOING_ASKER_NAMES_NOBODY"' '.code'
expect B3 "its message holds names nobody, as the lens write's refusal does" true \
  '.error | contains("names nobody")'
pass B3 "the going POST and the lens write refuse the one bearer, 403 each: $(jq -r '.error' "$work/body")"

# --- P1: each person writes an author-only lens with the cookie and with the token ----------------

declare -A by_cookie=() by_token=()
for login in "$first" "$second"; do
  invoke P1 "$login's cookie writes a pre-mortem on the spec reserved for its author" "cookie:$login" \
    "$(lens_write author)"
  by_cookie[$login]="$lens"
  cp "$work/invoked" "$work/body"
  expect P1 "the write answers ${by_cookie[$login]} reserved for $login" \
    "$(jq -cnS --arg l "$login" '{author: {id: $l, kind: "person"}, visibility: "author"}')" '{author, visibility}'
  read_back P1 "$login's cookie reads ${by_cookie[$login]} back" "cookie:$login" "${by_cookie[$login]}" "$login"

  invoke P1 "$login's personal token writes a pre-mortem on the spec reserved for its author, naming the session $session" \
    "token:$login" "$(lens_write author "$(actor)")"
  by_token[$login]="$lens"
  cp "$work/invoked" "$work/body"
  expect P1 "the write answers ${by_token[$login]} reserved for $login" \
    "$(jq -cnS --arg l "$login" '{author: {id: $l, kind: "person"}, visibility: "author"}')" '{author, visibility}'
  read_back P1 "$login's personal token reads ${by_token[$login]} back" "token:$login" "${by_token[$login]}" "$login"
done
readonly hidden=("${by_cookie[$first]}" "${by_token[$first]}" "${by_cookie[$second]}" "${by_token[$second]}")

# --- P2: the second person's token is refused the first person's token's lens ---------------------

# Both tokens' writes named the one session of this run, and each token proves a session with an
# empty id: under SameAs, which compares the kind and the id alone, the two are one writer.
# SameCredential compares the person each token belongs to as well, so the second token reads the
# first's lens as an address nobody holds.
nobody_holds P2 "token:$second" "$second's token"
target="${by_token[$first]}"
as_nobody "token:$second" "$target" ||
  fail P2 "$second's token is refused $target, which $first's token wrote and read back above" \
    "HTTP $status: $(body); an address nobody holds answers HTTP $(<"$work/nobody.status"): $(<"$work/nobody.body")"
pass P2 "$second's token reads $target, which $first's token reads back, as an address nobody holds: HTTP $status, byte for byte once the id is substituted"

# --- N1: the shared bearer reads none of the four ----------------------------------------------

nobody_holds N1 agent "the shared bearer"
for address in "${hidden[@]}"; do
  as_nobody agent "$address" ||
    fail N1 "the shared bearer reads $address, which its author reads back above, as an address nobody holds" \
      "HTTP $status: $(body); an address nobody holds answers HTTP $(<"$work/nobody.status"): $(<"$work/nobody.body")"
  pass N1 "the shared bearer reads $address as an address nobody holds: HTTP $status, byte for byte once the id is substituted"
done

# The absence beside its control: each person's read of the spec's lenses finds that person's two,
# and the shared bearer's read of the same finds its own lens and none of the four.
for login in "$first" "$second"; do
  spec_lenses N1 "$login's cookie reads the spec's lenses" "cookie:$login" "$work/spec-$login.json"
  expect N1 "$login's read of the spec's lenses holds the shared bearer's lens and both of $login's" true \
    --arg b "$bearer_lens" --arg c "${by_cookie[$login]}" --arg t "${by_token[$login]}" \
    '[.lenses[].address] | index($b) != null and index($c) != null and index($t) != null'
done
spec_lenses N1 "the shared bearer reads the spec's lenses" agent "$work/spec-bearer.json"
hidden_ids="$(jq -cn '$ARGS.positional | map(ltrimstr("dispatch://lens/"))' --args "${hidden[@]}")"
expect N1 "the shared bearer's read of the spec's lenses holds its own lens, and no string naming one of the four" \
  "$(jq -cn --arg a "$bearer_lens" '{held: [], lenses: [$a]}')" --argjson ids "$hidden_ids" \
  '{lenses: [.lenses[].address], held: [.. | strings | select(. as $s | any($ids[]; . as $id | $s | contains($id)))]}'

pass "$driver" complete
