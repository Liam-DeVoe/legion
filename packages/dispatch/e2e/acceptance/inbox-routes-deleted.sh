#!/usr/bin/env bash
# Drives dispatch://INFRA-1098's c-routes-deleted: the three reads the one inbox read replaced are
# gone, the send on the same path stays, and Mark all read and the dismissal write over the rows
# the Inbox page draws and over no other row.
#
# The three reads are GET /api/v1/inbox (listInbox), GET /api/v1/me/replies (listMyReplies) and
# GET /api/v1/agents/{session_id}/messages (listAgentMessages). POST
# /api/v1/agents/{session_id}/messages, the send on the same path, stays.
#
#   routes      with DEV_SIGNIN_LOGIN signed in through the dev sign-in route, each of the three
#               reads answers 404 NOT_FOUND, and the send still takes a message (201).
#   table       packages/envoy/internal/dispatch/api/routes_table.go holds no entry of the three
#               reads, neither a route row nor a scope row.
#   handlers    no Go file under packages/envoy declares listInbox, listMyReplies or
#               listAgentMessages.
#   owner       no Go file under packages/envoy holds myRepliesOwner, the filter the deleted
#               listMyReplies shared with the two writes.
#   contract    nothing under packages/contracts/src holds MyRepliesResponse, the one contract type
#               of the three reads that no other read returns.
#   clients     packages/dispatch/web/src, packages/envoy-client/src, packages/pi-envoy/src and
#               packages/claude-envoy/src hold no read of the three paths, matched as a string and
#               as the segment list envoy-client's dispatch-http.ts builds; a send is no read.
#   writes      over the two seeded rows of DEV_SIGNIN_LOGIN: Mark all read leaves the page's row
#               read and the other row's state as it was; a dismissal of the page's row takes it out
#               of the page, and a dismissal of the other row writes nothing.
#
# Every search runs first over a scratch copy of what it reads with one planted instance of what it
# looks for, and must find it, so a search that matches nothing cannot read as a clean tree; each
# prints how many files it read, and a count of zero fails.
#
# The two rows of the writes section are T8's seed (INFRA-1098's plan): for DEV_SIGNIN_LOGIN, one
# unread thread row the Inbox page draws, and one unread thread row it does not draw, the thread of
# an open ask on an open issue assigned to that person. INBOX_DRAWN_THREAD and
# INBOX_UNDRAWN_THREAD name them by thread id; where either is unset the driver reads it from the
# store (seeded_rows below): the newest unread user_threads row of the person whose thread is no
# open ask that waits on them, and the newest unread one whose thread is an open ask on an open
# issue assigned to them.
#
# Inputs: DISPATCH_URL (the server, dispatch:local), DISPATCH_TOKEN or DISPATCH_TOKEN_FILE (its
# agent bearer, read here and never printed), PROJECT (an existing project key), DEV_SIGNIN_LOGIN
# (a login the dev sign-in route signs in), DATABASE_URL (the database that server serves, on this
# machine), and optionally INBOX_DRAWN_THREAD and INBOX_UNDRAWN_THREAD.
# INBOX_ROUTES_DELETED_SECTIONS is a space-separated subset of the sections above; table,
# handlers, owner, contract and clients need no server and read none of the server's inputs. A
# missing or bad input is exit 2.
#
# Each passing assertion prints `PASS: <section> <what>`; a failing one prints
# `FAIL: <section> <what>: <observed>` and ends its section. Every section runs whatever an earlier
# one found, and the script exits 1 at its end if any failed.
#
# --- references: INFRA-4704's part ---------------------------------------------------------------
# c-routes-deleted also reads GET /api/v1/issues/{key}/references of a seeded issue and finds
# neither `members` nor `truncated`, and finds no Members or Truncated field of
# model.IssueReferences, no ReferenceMember type under packages/envoy, and no read of
# references.members or references.truncated in web/src/features/artifacts/ArtifactsTab.tsx or
# envoy-client/src/dispatch-commands/render-issue.ts. That part is built on INFRA-4704's branch: it
# adds a `references` section beside the ones below, named in all_sections (and, for its searches,
# offline_sections), each search with its planted instance first.

# shellcheck disable=SC2016 # the single-quoted jq filters name jq variables, not shell ones
set -euo pipefail
set +x

usage() {
  cat >&2 <<'USAGE'
Usage: DATABASE_URL=postgres://user@127.0.0.1:5433/db DISPATCH_URL=http://127.0.0.1:8777 \
         DISPATCH_TOKEN=e2e-token PROJECT=CORE DEV_SIGNIN_LOGIN=alice \
         [INBOX_DRAWN_THREAD=<thread id>] [INBOX_UNDRAWN_THREAD=<thread id>] \
         [INBOX_ROUTES_DELETED_SECTIONS='table handlers owner contract clients'] inbox-routes-deleted.sh
       (DISPATCH_TOKEN_FILE=<file holding the bearer> in place of DISPATCH_TOKEN; the offline
       sections alone need none of the server's inputs)
USAGE
}

# What lib.sh's functions set for this script: the bearer (read_bearer), the work directory
# (work_dir) and each read's answer (query). They are named here so that shellcheck, which reads
# one file at a time unless it is run with -x, sees them assigned.
token="" work="" answer=""
# shellcheck source-path=SCRIPTDIR source=lib.sh disable=SC1091 # followed under shellcheck -x
. "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
readonly script_dir
repo="$(cd -- "$script_dir/../../../.." && pwd)"
readonly repo

readonly all_sections="routes table handlers owner contract clients writes"
readonly offline_sections="table handlers owner contract clients"
sections="${INBOX_ROUTES_DELETED_SECTIONS:-$all_sections}"
for chosen in $sections; do
  [[ " $all_sections " == *" $chosen "* ]] || {
    printf 'inbox-routes-deleted.sh: INBOX_ROUTES_DELETED_SECTIONS names %s, which is none of: %s\n' "$chosen" "$all_sections" >&2
    usage
    exit 2
  }
done
online=0
for chosen in $sections; do
  [[ " $offline_sections " == *" $chosen "* ]] || online=1
done
readonly sections online

require_commands cp git grep jq sed sort tar wc
if [[ "$online" == 1 ]]; then
  require_env DISPATCH_URL PROJECT DEV_SIGNIN_LOGIN DATABASE_URL
  require_commands curl env psql
  # shellcheck disable=SC2153 # DATABASE_URL is the caller's input, checked by name above
  loopback_database_url "$DATABASE_URL" || {
    printf 'inbox-routes-deleted.sh: DATABASE_URL names no single loopback host; the writes section reads the rows it marks, and only a test database on this machine is one\n' >&2
    exit 2
  }
  for variable in INBOX_DRAWN_THREAD INBOX_UNDRAWN_THREAD; do
    [[ -z "${!variable:-}" || "${!variable}" =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] || {
      printf 'inbox-routes-deleted.sh: %s is %s, not a thread id\n' "$variable" "${!variable}" >&2
      usage
      exit 2
    }
  done
  read_bearer
fi

# shellcheck disable=SC2031 # the DISPATCH_URL lib.sh's `cli` exports is its own; this one is the caller's
base="${DISPATCH_URL:-}"
readonly base="${base%/}"
base_host="${base#*://}"
# The dashboard's origin, which a write the person's cookie authenticates must name
# (enforceCookieOrigin).
readonly origin="${base%%://*}://${base_host%%/*}"
# shellcheck disable=SC2153 # these are the caller's inputs, checked by name above
readonly person="${DEV_SIGNIN_LOGIN:-}" database_url="${DATABASE_URL:-}"
stamp="$(date +%s)-$$"
readonly stamp

work_dir
printf 'Authorization: Bearer %s\n' "$token" >"$work/auth"
trap 'rm -rf "$work"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# request AS METHOD PATH [JSON]: sends METHOD PATH as AS: `agent`, with the bearer, or `person`,
# with the cookie dev sign-in gave DEV_SIGNIN_LOGIN and, for a write, the dashboard's origin. The
# answer's body goes to $work/body and its HTTP status to $status.
request() {
  local who="$1" method="$2" path="$3"
  local -a auth data=()
  if [[ "$who" == agent ]]; then
    auth=(-H "@$work/auth")
  else
    auth=(-b "$work/jar" -H "Origin: $origin")
  fi
  [[ $# -lt 4 ]] || data=(-H 'Content-Type: application/json' --data-binary "$4")
  status="$(curl -sS -X "$method" -o "$work/body" -w '%{http_code}' "${auth[@]}" "${data[@]}" \
    "$base$path" 2>"$work/curl")" || status="curl failed: $(<"$work/curl")"
}

# sign_in SECTION: has the dev sign-in route sign DEV_SIGNIN_LOGIN in, keeping the cookie in
# $work/jar.
sign_in() {
  [[ -s "$work/jar" ]] && return 0
  curl -sS -o /dev/null -c "$work/jar" "$base/auth/_dev/signin?login=$(q "$person")" 2>"$work/curl" ||
    fail "$1" "sign $person in" "curl: $(<"$work/curl")"
  [[ -s "$work/jar" ]] || fail "$1" "sign $person in" "the dev sign-in route left no cookie"
  pass "$1" "signed $person in through the dev sign-in route"
}

# isolated COMMAND...: runs COMMAND in a subshell of its own, with errexit, and sets
# $isolated_status to its exit status, so a `fail` inside ends COMMAND alone.
isolated() {
  set +e
  (
    set -e
    "$@"
  )
  isolated_status=$?
  set -e
}

# grep_tree ROOT GIT-GREP-ARGS...: `git grep -nIE` over ROOT: its tracked files and the untracked
# ones git does not ignore where ROOT is a checkout, and every file under it where it is a scratch
# directory (`--no-index`). Its hits go to stdout, one `path:line:text` each; no hit is no failure.
grep_tree() {
  local root="$1" code=0
  shift
  if git -C "$root" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    git -C "$root" grep --untracked -nIE "$@" || code=$?
  else
    (cd -- "$root" && git grep --no-index -nIE "$@") || code=$?
  fi
  [[ "$code" -le 1 ]]
}

# files_in ROOT PATHSPEC...: how many files a search of ROOT under PATHSPEC reads, by the rule
# grep_tree reads them: each file the search reads holds a line the empty pattern matches.
files_in() {
  local root="$1"
  shift
  grep_tree "$root" -l -e '' -- "$@" | wc -l
}

# scratch NAME PATHSPEC...: a scratch copy of the tracked files under PATHSPEC, at $work/NAME.
scratch() {
  local name="$1"
  shift
  mkdir -p "$work/$name"
  # The index still lists a file the working copy deleted, which tar cannot read, so a path the
  # working copy holds no file at is left out: the copy is of the tree as it stands.
  git -C "$repo" ls-files -z --cached --others --exclude-standard -- "$@" |
    while IFS= read -r -d '' file; do
      [[ ! -e "$repo/$file" ]] || printf '%s\0' "$file"
    done | tar -C "$repo" --null -T - -cf - | tar -C "$work/$name" -xf -
}

# searched SECTION WHAT ROOT PATHSPEC...: prints how many files the search of ROOT read and fails
# where it read none: a search over no file finds nothing whatever the tree holds.
searched() {
  local section="$1" what="$2" root="$3" count
  shift 3
  count="$(files_in "$root" "$@")"
  [[ "$count" -gt 0 ]] || fail "$section" "$what" "the search read no file under $*"
  printf '%s\n' "$count"
}

# search_section SECTION WHAT PATTERN PLANT-FILE PLANT-LINE PATHSPEC...: one search with its
# control: PATTERN over a scratch copy of PATHSPEC holding PLANT-LINE in PLANT-FILE must find that
# line, and then over the tree must find nothing.
search_section() {
  local section="$1" what="$2" pattern="$3" plant="$4" line="$5" found count
  shift 5
  scratch "plant-$section" "$@"
  mkdir -p "$work/plant-$section/$(dirname "$plant")"
  printf '%s\n' "$line" >>"$work/plant-$section/$plant"
  found="$(grep_tree "$work/plant-$section" -e "$pattern" -- "$@")" || fail "$section" "search the scratch copy" "git grep failed"
  [[ "$found" == *"$plant:"* ]] || fail "$section" "the search finds the planted $what in a scratch copy" "it found: ${found:-nothing}"
  count="$(searched "$section" "search the scratch copy" "$work/plant-$section" "$@")"
  pass "$section" "the search finds the planted $what in a scratch copy of $* ($count files read)"
  found="$(grep_tree "$repo" -e "$pattern" -- "$@")" || fail "$section" "search the tree" "git grep failed"
  count="$(searched "$section" "search the tree" "$repo" "$@")"
  [[ -z "$found" ]] || fail "$section" "no $what under $*" "$found"
  pass "$section" "no $what under $* ($count files read)"
}

# --- routes: the three reads answer 404, the send stays ------------------------------------------

# agent_session SECTION: a session the server's listener lists, which the send writes to and the
# deleted read of that path is asked about.
agent_session() {
  request agent GET /api/v1/agents
  want_status 200 "$1" "list the agents"
  jq -er '(if type == "array" then . else .agents end)[0].session_id' "$work/body" 2>/dev/null ||
    fail "$1" "find a session the server lists" "it lists none: $(body)"
}

routes_section() {
  local section=routes session path
  sign_in "$section"
  session="$(agent_session "$section")"
  for path in /api/v1/inbox "/api/v1/me/replies" "/api/v1/agents/$(q "$session")/messages"; do
    request person GET "$path"
    [[ "$status" == 404 ]] || fail "$section" "GET $path answers 404 NOT_FOUND" "GET $path answered HTTP $status: $(body)"
    expect "$section" "GET $path answers 404 NOT_FOUND" '"NOT_FOUND"' '.code'
  done
  request person POST "/api/v1/agents/$(q "$session")/messages" \
    "$(jq -cn --arg b "A send through the route that stays, $stamp." '{body: $b, delivery: "btw"}')"
  want_status 201 "$section" "POST /api/v1/agents/$session/messages takes a message"
  expect "$section" "POST /api/v1/agents/$session/messages stores the message it took" true '.id | type == "string"'
}

# --- table, handlers, owner, contract: what the three reads were, gone from the tree -------------

readonly table_file=packages/envoy/internal/dispatch/api/routes_table.go
readonly table_pattern='"(GET /api/v1/inbox|GET /api/v1/me/replies|GET /api/v1/agents/\{session_id\}/messages)"|http\.MethodGet, "(/api/v1/inbox|/api/v1/me/replies|/api/v1/agents/\{session_id\}/messages)"'

table_section() {
  search_section table "route or scope row of the three reads" "$table_pattern" "$table_file" \
    '	"GET /api/v1/me/replies":                                 {model.ScopePerson},' "$table_file"
}

handlers_section() {
  search_section handlers "declaration of listInbox, listMyReplies or listAgentMessages" \
    'func \(s \*server\) (listInbox|listMyReplies|listAgentMessages)\(' \
    packages/envoy/internal/dispatch/api/planted.go \
    'func (s *server) listMyReplies(w http.ResponseWriter, r *http.Request) {' 'packages/envoy/*.go'
}

owner_section() {
  search_section owner "Go file holding myRepliesOwner" '\bmyRepliesOwner\b' \
    packages/envoy/internal/dispatch/api/planted.go 'const myRepliesOwner = `' 'packages/envoy/*.go'
}

contract_section() {
  search_section contract "MyRepliesResponse" '\bMyRepliesResponse\b' packages/contracts/src/planted.ts \
    'export type MyRepliesResponse = components["schemas"]["MyRepliesResponse"];' packages/contracts/src
}

# --- clients: no read of the three paths in the four trees --------------------------------------

readonly caller_trees=(packages/dispatch/web/src packages/envoy-client/src packages/pi-envoy/src packages/claude-envoy/src)
# Each path the three reads answer at, spelled as a string - a literal, a template, a route
# pattern - or as the segment list envoy-client's dispatch-http.ts builds a URL from.
readonly path_inbox='/api/v1/inbox([^/A-Za-z0-9_-]|$)|"api", *"v1", *"inbox" *\]'
readonly path_replies='/api/v1/me/replies([^/A-Za-z0-9_-]|$)|"api", *"v1", *"me", *"replies" *\]'
readonly path_messages='/api/v1/agents/[^/[:space:]"'"'"'`]+/messages([^/A-Za-z0-9_-]|$)|"api", *"v1", *"agents", *[^],]+, *"messages" *\]'
# A send is what names the POST - its method, the web client's send (createAgentMessage) or a send
# helper (`writeRequest("post"`, `#json("POST"`) - on the line of the path or within six lines of
# it; a read is a GET or a read helper (`read(`, `#json("GET"`). A line that names a read is a read
# whatever is near it, and a comment line calls nothing.
readonly names_a_send='\bPOST\b|["'"'"']post["'"'"']|\.post\(|createAgentMessage|\bsend[A-Z][A-Za-z0-9_]*\(|\bsend\('
readonly names_a_read='\bGET\b|["'"'"']get["'"'"']|\.get\(|\.read\(|#json\("GET"'
readonly a_comment='^[[:space:]]*(//|\*|/\*|#)'

# callers_in ROOT: every read of the three paths under ROOT's four trees, one `path:line: <read>`
# each.
callers_in() {
  local root="$1" hits file line text read from
  hits="$(grep_tree "$root" -e "$path_inbox" -e "$path_replies" -e "$path_messages" -- "${caller_trees[@]}")" ||
    return 1
  [[ -n "$hits" ]] || return 0
  while IFS= read -r hit; do
    file="${hit%%:*}"
    line="${hit#*:}"
    text="${line#*:}"
    line="${line%%:*}"
    [[ ! "$text" =~ $a_comment ]] || continue
    if grep -qE "$path_inbox" <<<"$text"; then
      read="GET /api/v1/inbox"
    elif grep -qE "$path_replies" <<<"$text"; then
      read="GET /api/v1/me/replies"
    else
      from=$((line > 6 ? line - 6 : 1))
      if ! grep -qE "$names_a_read" <<<"$text" &&
        sed -n "${from},$((line + 6))p" "$root/$file" | grep -qE "$names_a_send"; then
        continue
      fi
      read="GET /api/v1/agents/{session_id}/messages"
    fi
    printf '%s:%s: %s: %s\n' "$file" "$line" "$read" "$(sed -E 's/^[[:space:]]+//' <<<"$text" | head -c 160)"
  done <<<"$hits"
}

clients_section() {
  local section=clients plant="$work/plant-clients" found want count
  scratch plant-clients "${caller_trees[@]}"
  # A read of each path, and a send far enough below them that no read is within its six lines.
  cat >"$plant/packages/envoy-client/src/planted-caller.ts" <<'TS'
export async function planted(base: string, session: string, client: { json: Function }) {
  const replies = await fetch(`${base}/api/v1/me/replies?kind=reply`);
  const asks = await client.json("GET", ["api", "v1", "inbox"], undefined, { assignee: "me" });
  const thread = await fetch(`${base}/api/v1/agents/${session}/messages`);







  await fetch(`${base}/api/v1/agents/${session}/messages`, { method: "POST", body: "{}" });
  return [replies, asks, thread];
}
TS
  found="$(callers_in "$plant")" || fail "$section" "search the scratch copy" "git grep failed"
  for want in "planted-caller.ts:2: GET /api/v1/me/replies" "planted-caller.ts:3: GET /api/v1/inbox" \
    "planted-caller.ts:4: GET /api/v1/agents/{session_id}/messages"; do
    [[ "$found" == *"$want"* ]] ||
      fail "$section" "the search finds a planted read (${want##*: }) in a scratch copy of the four trees" "it found: ${found:-nothing}"
  done
  [[ "$(grep -c . <<<"$found")" == 3 ]] ||
    fail "$section" "the search finds the three planted reads in the scratch copy, and nothing else" "it found: $found"
  count="$(searched "$section" "search the scratch copy" "$plant" "${caller_trees[@]}")"
  pass "$section" "the search finds the three planted reads in a scratch copy of the four trees, and not the planted send ($count files read)"

  found="$(callers_in "$repo")" || fail "$section" "search the four trees" "git grep failed"
  count="$(searched "$section" "search the four trees" "$repo" "${caller_trees[@]}")"
  [[ -z "$found" ]] || fail "$section" "no read of the three paths in ${caller_trees[*]}" "$found"
  pass "$section" "no read of GET /api/v1/inbox, GET /api/v1/me/replies or GET /api/v1/agents/{session_id}/messages in ${caller_trees[*]} ($count files read)"
}

# --- writes: Mark all read and the dismissal over the two seeded rows --------------------------

# The canonical login the store keys the person's rows on, as the server writes it.
person_login() { tr '[:upper:]' '[:lower:]' <<<"$person"; }

# seeded_rows SECTION: sets $drawn and $undrawn, the two rows of T8's seed (the header says which).
seeded_rows() {
  local login
  login="$(person_login)"
  drawn="${INBOX_DRAWN_THREAD:-}"
  undrawn="${INBOX_UNDRAWN_THREAD:-}"
  if [[ -z "$undrawn" ]]; then
    query "$1" "read the seeded row the page does not draw" "
      select u.thread_id from user_threads u
      join asks a on a.id = u.thread_id and a.state = 'open'
      join issues i on i.key = a.issue_key and i.closed_at is null
      where u.login = '$login' and u.unread and i.assignee = '$login'
        and coalesce(a.asked_of, i.assignee) = '$login'
      order by u.replied_at desc limit 1"
    undrawn="$answer"
  fi
  if [[ -z "$drawn" ]]; then
    query "$1" "read the seeded row the page draws" "
      select u.thread_id from user_threads u
      where u.login = '$login' and u.unread and u.dismissed_at is null and u.kind = 'comment'
      order by u.replied_at desc limit 1"
    drawn="$answer"
  fi
  [[ -n "$drawn" && -n "$undrawn" ]] ||
    fail "$1" "find T8's two seeded rows" "drawn='${drawn:-none}' undrawn='${undrawn:-none}': set INBOX_DRAWN_THREAD and INBOX_UNDRAWN_THREAD, or seed them"
  pass "$1" "the seeded rows: $drawn, which the page draws, and $undrawn, which it does not"
}

# stored SECTION THREAD: the person's row of THREAD as the store holds it, into $answer.
stored() {
  query "$1" "read the stored row of $2" "
    select concat_ws(' ', 'read_through=' || read_through::text, 'unread=' || unread::text,
      'dismissed_at=' || coalesce(dismissed_at::text, ''), 'version=' || version::text)
    from user_threads where login = '$(person_login)' and thread_id = '$2'"
  [[ -n "$answer" ]] || fail "$1" "read the stored row of $2" "$person has no row of it"
}

# page_thread STATE THREAD: THREAD's thread row in the page under STATE, as one compact JSON
# object, or empty where the page draws none.
page_thread() {
  request person GET "/api/v1/me/inbox?state=$1"
  want_status 200 writes "GET /api/v1/me/inbox?state=$1"
  jq -c --arg t "$2" 'first(.entries[]? | select(.thread.thread_id == $t) | .thread) // empty' "$work/body"
}

writes_section() {
  local section=writes before row version
  sign_in "$section"
  seeded_rows "$section"
  stored "$section" "$undrawn"
  before="$answer"
  [[ "$before" == *"unread=true"* ]] || fail "$section" "the row the page does not draw is unread" "$before"
  [[ -n "$(page_thread unread "$drawn")" ]] || fail "$section" "the page draws $drawn under state=unread" "it does not: $(body)"
  [[ -z "$(page_thread inbox "$undrawn")" ]] || fail "$section" "the page draws no thread row for $undrawn" "it does: $(body)"
  pass "$section" "the page draws $drawn and no thread row for $undrawn"

  request person POST /api/v1/me/inbox/read "$(jq -cn --arg at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '{read_through: $at}')"
  want_status 200 "$section" "Mark all read"
  row="$(page_thread inbox "$drawn")"
  [[ "$(jq -r '.unread' <<<"$row")" == false ]] ||
    fail "$section" "the one read answers $drawn read after Mark all read" "its row: ${row:-none}"
  pass "$section" "the one read answers $drawn read after Mark all read"
  stored "$section" "$undrawn"
  [[ "$answer" == "$before" ]] ||
    fail "$section" "Mark all read leaves $undrawn as it was" "$undrawn: before $before, after $answer"
  pass "$section" "Mark all read leaves $undrawn as it was ($answer)"

  version="$(jq -r '.version' <<<"$row")"
  request person POST /api/v1/me/inbox/dismiss "$(jq -cn --arg r "reply:$drawn" --argjson v "$version" \
    '{threads: [$r], seen_replies: {($r): {version: $v}}}')"
  want_status 200 "$section" "dismiss $drawn"
  expect "$section" "the dismissal of $drawn changes it" 1 '.dismissed'
  [[ -z "$(page_thread inbox "$drawn")" ]] || fail "$section" "the dismissed $drawn is out of the page's rows" "the page still draws it: $(body)"
  pass "$section" "the dismissed $drawn is out of the page's rows"

  version="${answer##*version=}"
  request person POST /api/v1/me/inbox/dismiss "$(jq -cn --arg r "reply:$undrawn" --argjson v "$version" \
    '{threads: [$r], seen_replies: {($r): {version: $v}}}')"
  want_status 200 "$section" "dismiss $undrawn"
  expect "$section" "a dismissal of $undrawn, which the page does not draw, writes nothing" 0 '.dismissed'
  stored "$section" "$undrawn"
  [[ "$answer" == "$before" ]] ||
    fail "$section" "a dismissal of $undrawn leaves it as it was" "$undrawn: before $before, after $answer"
  pass "$section" "a dismissal of $undrawn leaves it as it was ($answer)"
}

# --- the run -----------------------------------------------------------------------------------

failed_sections=()
for chosen in $all_sections; do
  [[ " $sections " == *" $chosen "* ]] || continue
  isolated "${chosen}_section"
  [[ "$isolated_status" == 0 ]] || failed_sections+=("$chosen")
done

if [[ "${#failed_sections[@]}" -gt 0 ]]; then
  printf 'inbox-routes-deleted.sh: %d section(s) failed: %s\n' "${#failed_sections[@]}" "${failed_sections[*]}" >&2
  exit 1
fi
printf 'inbox-routes-deleted.sh: every check passed (%s)\n' "$sections"
