#!/usr/bin/env bash
# The driver of the check c-restricted-sweep: a read sweep of a Dispatch deployment as a
# restricted member, over every GET route of the route index and both of Dispatch's sockets, for
# the objects of one project whose access list does not name that member.
#
# It reads the route index the server's route table is held to
# (packages/envoy/internal/dispatch/api/testdata/routes.txt) and, for each GET row:
#   - a row whose path names objects is called with each wildcard filled with an object of the
#     project SWEEP_PROJECT, which a full member's token looked up first (the lookup). The member
#     must be answered 404 or 403, or what a missing object is answered, and never a row of that
#     project;
#   - a row whose path names no object is a list: it is called bare, and once more for each of
#     its query parameters (read from packages/contracts/schemas/dispatch-openapi.json) that
#     names an object of a project, filled with that project's object. A list may answer rows of
#     the member's own projects, never one of SWEEP_PROJECT.
# Two kinds of object no route lists are got another way: a question's run, which the full member
# asks of the project (POST /api/v1/going/requests), and a file of an agent's conversation, which
# SWEEP_CONVERSATION_FILE names. A GET row the sweep cannot fill leaves the run UNKNOWN.
# Then the sockets:
#   /ws/doc/{room}  the member is not admitted to the room of the project's document, while the
#                   same probe admits the member to a document of their own project;
#   /ws/live        the member's event feed, open for the whole of SWEEP_LIVE_SECONDS, is sent no
#                   event of the project while the full member writes a comment on
#                   SWEEP_WRITE_ISSUE, which the full member's own feed, open over the same window,
#                   is sent.
#
# "A row of the project" is a JSON member name or string value that is the project's key or holds
# an issue key of the project, an id the lookup found, or the title of the issue the lookup filled
# with; a body that is not JSON is read for the same markers. Each answer is read for them once
# the values the path was filled with are masked, since an answer may echo what it was asked, and
# before it is compared with anything, so an answer alike for a missing object excuses no row.
# Filled paths are also called with objects that do not exist: a 2xx for the project's object
# where a missing one is refused says the object exists, and is a row of it too, whatever the
# body holds. Every RFC 3339 instant and the durations a read measures are masked before two
# answers are compared. A session, a person, a repository, a priority, a broadcast and a delivery
# run belong to no project, so a row filled only with them is held to the markers alone.
#
# Each row also says whether it can fail: a filled row can when the full member is answered the
# project's object otherwise than a missing one, a list when the full member's answer holds a row
# of the project. A row that cannot is called and judged all the same, and says so (`blind`).
#
# Inputs, each refused by name when missing or malformed:
#   SWEEP_ORIGIN             the deployment's origin, https://<host>[:<port>]; http:// only to
#                            127.0.0.1, localhost or [::1], for a local stack.
#   SWEEP_PROJECT            the key of the project the member's access list must not name.
#   SWEEP_FULL_TOKEN         a bearer token of a full member who sees that project.
#   SWEEP_RESTRICTED_COOKIE  the restricted member's session cookie, as the `Cookie` header
#                            carries it (`<name>=<value>`).
#   SWEEP_WRITE_ISSUE        the issue of SWEEP_PROJECT the full member comments on while both
#                            event feeds are open. The comment and the question are the sweep's
#                            two writes, each in SWEEP_PROJECT and as the full member.
#   SWEEP_CONVERSATION_FILE  `<session id>/<slug>`: a file of the conversation of a session that
#                            worked in SWEEP_PROJECT alone, which no route lists. Left unset, the
#                            two rows that read such a file are not filled, and the run is UNKNOWN.
#   SWEEP_LIVE_SECONDS       how long both feeds are read after the comment (default 60, maximum
#                            2147413, within the socket probe's timer range).
#   SWEEP_CALL_SECONDS       how long one request waits (default 20).
#   SWEEP_PACE_SECONDS       the pause before each request (default 0.1, so the run stays far
#                            under the edge's per-address request limit); 0 for none.
#   SWEEP_ROUTES, SWEEP_OPENAPI
#                            the route index and the API document to read, by default this
#                            checkout's.
# SWEEP_LIST_KINDS=1 prints, for each GET row of the route index, its path and the kind of object
# each wildcard is filled with (`{key}=issue`), and exits 4, never PASS's 0: its test holds that
# to the server's own route probes.
#
# It needs bash 3.2 or later, curl 7.55 or later, jq 1.6 or later, and bun with this checkout's
# dependencies installed (`bun install`), which runs the socket probe
# e2e/acceptance/socket-access.ts. No token or cookie is printed or passed on a command line:
# curl and the probe read them from files of mode 0600 in a directory of the run's own, which it
# removes.
#
# It prints a line per call and a verdict:
#   PASS     exit 0: every GET row filled and called, every route and both sockets kept the
#            project from the member, every kind of object the project owns was found, at least
#            one row could fail, and the full member's own event feed was sent the comment.
#   FAIL     exit 1: the member was answered a row of the project, admitted to its document's
#            room, or sent an event of it. The verdict names the first route that did.
#   UNKNOWN  exit 2: anything that proved nothing, which is never a pass: an input refused, a
#            tool missing, an identity that is not what it must be, a kind of object the lookup
#            did not find, a row it could not fill, an answer no rule judges (401, 429, a 5xx, no
#            answer), no row that could fail, or a socket control that did not hold.
#
# Usage, from packages/dispatch:
#
#   SWEEP_ORIGIN=https://dispatch.example SWEEP_PROJECT=PRIV SWEEP_WRITE_ISSUE=PRIV-12 \
#     SWEEP_CONVERSATION_FILE=<session id>/<slug> SWEEP_FULL_TOKEN=... \
#     SWEEP_RESTRICTED_COOKIE=... bash e2e/acceptance/restricted-sweep.sh
#
# Its tests: e2e/__tests__/restricted-sweep-judging.test.ts holds each judging rule alone against
# a fake Dispatch; e2e/__tests__/restricted-sweep.test.ts runs it against the local stack
# (e2e/harness-pool.ts, which starts e2e/run-server.sh) and against a stack of a build with no read
# check, which it must FAIL.

# A shell other than bash stops here, before it reads a line it would take another way.
if [ -z "${BASH_VERSION:-}" ]; then
  echo 'UNKNOWN: run this with bash: bash restricted-sweep.sh'
  exit 2
fi
# Credentials leave the inherited environment before any child is started, including tools used
# to find this checkout. Tracing must stop before their values are expanded.
set +x
set +a
full_token=${SWEEP_FULL_TOKEN:-}
cookie=${SWEEP_RESTRICTED_COOKIE:-}
unset SWEEP_FULL_TOKEN SWEEP_RESTRICTED_COOKIE
export -n full_token cookie
set -u
# Every pattern below matches bytes, whatever locale the machine runs in.
LC_ALL=C
export LC_ALL

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../../.." && pwd)
routes=${SWEEP_ROUTES:-$root/packages/envoy/internal/dispatch/api/testdata/routes.txt}
openapi=${SWEEP_OPENAPI:-$root/packages/contracts/schemas/dispatch-openapi.json}
socket_probe=$here/socket-access.ts
work=''
watchers=''

# cleanup stops the socket probes still running and removes the run directory, credentials and
# all, however the run ends.
# shellcheck disable=SC2329 # the EXIT trap below runs it
cleanup() {
  local pid
  for pid in $watchers; do kill "$pid" 2>/dev/null; done
  for pid in $watchers; do wait "$pid" 2>/dev/null; done
  [ -z "$work" ] || rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# unknown MESSAGE: the verdict for a run that cannot go on.
unknown() {
  printf 'UNKNOWN: %s\n' "$1"
  exit 2
}

# wildcards_of PATTERN prints the name of each {wildcard} of PATTERN, one per line.
wildcards_of() {
  local rest=$1
  while :; do
    case $rest in
      *'{'*'}'*)
        rest=${rest#*\{}
        printf '%s\n' "${rest%%\}*}"
        rest=${rest#*\}}
        ;;
      *) return 0 ;;
    esac
  done
}

# kind_of PATTERN WILDCARD prints the kind of object WILDCARD names in PATTERN, as the server's
# route probes name it (internal/dispatch/api/cmd/routeprobes), or nothing for a wildcard this
# sweep does not know.
kind_of() {
  case $2 in
    key)
      case $1 in
        /api/v1/issues/*) echo issue ;;
        /api/v1/projects/* | /api/v1/access/projects/*) echo project ;;
      esac
      ;;
    id)
      case $1 in
        */artifacts/\{id\}* | /api/v1/me/documents/\{id\}*) echo document ;;
        */asks/\{id\}*) echo ask ;;
        */comments/\{id\}*) echo comment ;;
        */messages/\{id\}*) echo message ;;
        */handoffs/\{id\}*) echo handoff ;;
        */broadcasts/\{id\}*) echo broadcast ;;
        */priorities/\{id\}*) echo priority ;;
        */near-copy-checks/\{id\}*) echo near-copy-check ;;
        /api/v1/going/requests/\{id\}*) echo going-run ;;
        /api/v1/delivery/runs/\{id\}*) echo delivery-run ;;
      esac
      ;;
    block_id) echo block ;;
    number) echo document-version ;;
    slug) echo document-slug ;;
    phase) echo handoff-phase ;;
    session_id) echo session ;;
    login) echo person ;;
    owner) echo repository-owner ;;
    repo) echo repository-name ;;
  esac
}

if [ "${SWEEP_LIST_KINDS:-}" = 1 ]; then
  [ -r "$routes" ] || unknown "the route index $routes cannot be read"
  while read -r method pattern _ || [ -n "${method:-}" ]; do
    [ "$method" = GET ] || continue
    line=$pattern
    for wildcard in $(wildcards_of "$pattern"); do
      line="$line $wildcard=$(kind_of "$pattern" "$wildcard")"
    done
    printf '%s\n' "$line"
  done <"$routes"
  exit 4
fi

is_count() { case $1 in '' | *[!0-9]*) return 1 ;; esac; }
is_seconds() { case $1 in '' | *[!0-9.]* | .* | *. | *.*.*) return 1 ;; esac; }
holds_line_break() { case $1 in *$'\n'* | *$'\r'*) return 0 ;; esac; return 1; }

origin=${SWEEP_ORIGIN:-}
origin=${origin%/}
case $origin in
  https://*) authority=${origin#https://} ;;
  http://127.0.0.1 | http://127.0.0.1:* | http://localhost | http://localhost:* | 'http://[::1]' | 'http://[::1]:'*)
    authority=${origin#http://} ;;
  '') unknown 'SWEEP_ORIGIN is required: the deployment origin, https://<host>[:<port>]' ;;
  *) unknown "SWEEP_ORIGIN must be https://<host>[:<port>], or http:// to 127.0.0.1, localhost or [::1]: $origin" ;;
esac
# A host and a port alone: no path, query, user or space.
case $authority in
  '' | *[!]A-Za-z0-9.:[-]*) unknown "SWEEP_ORIGIN must be an origin alone, https://<host>[:<port>]: $origin" ;;
esac
# A project key as Dispatch's own rule has it: a capital letter, then one to nine capitals or digits.
project=${SWEEP_PROJECT:-}
[ -n "$project" ] || unknown 'SWEEP_PROJECT is required: the key of the project the member is not listed on'
case $project in
  [A-Z]?*) case ${project#?} in *[!A-Z0-9]*) unknown "SWEEP_PROJECT is no project key: $project" ;; esac ;;
  *) unknown "SWEEP_PROJECT is no project key: $project" ;;
esac
[ "${#project}" -le 10 ] || unknown "SWEEP_PROJECT is no project key: $project"
[ -n "$full_token" ] || unknown "SWEEP_FULL_TOKEN is required: a full member's bearer token that sees $project"
case $full_token in *[!A-Za-z0-9._~+/=-]*) unknown 'SWEEP_FULL_TOKEN holds a byte no bearer token holds' ;; esac
[ -n "$cookie" ] || unknown 'SWEEP_RESTRICTED_COOKIE is required: the restricted member'"'"'s session cookie, <name>=<value>'
holds_line_break "$cookie" && unknown 'SWEEP_RESTRICTED_COOKIE holds a line break'
case $cookie in *=*) ;; *) unknown 'SWEEP_RESTRICTED_COOKIE must be <name>=<value>, as the Cookie header carries it' ;; esac
case $cookie in *[!\ -~]*) unknown 'SWEEP_RESTRICTED_COOKIE holds a non-printable header byte' ;; esac
write_issue=${SWEEP_WRITE_ISSUE:-}
case $write_issue in
  '') unknown "SWEEP_WRITE_ISSUE is required: the issue of $project the full member comments on" ;;
  "$project"-[1-9]*) is_count "${write_issue#"$project"-}" || unknown "SWEEP_WRITE_ISSUE is no issue of $project: $write_issue" ;;
  *) unknown "SWEEP_WRITE_ISSUE is no issue of $project: $write_issue" ;;
esac
# A file of a conversation: the session id, a "/", and the file's slug, neither holding a "/".
conversation_file=${SWEEP_CONVERSATION_FILE:-}
conversation_session='' conversation_slug=''
conversation_why='SWEEP_CONVERSATION_FILE names no file of a conversation, and no route lists them'
case $conversation_file in
  '') ;;
  */*/* | /* | */ | *[!A-Za-z0-9._/-]*)
    unknown "SWEEP_CONVERSATION_FILE must be <session id>/<slug>: $conversation_file" ;;
  */*) conversation_session=${conversation_file%/*} conversation_slug=${conversation_file#*/} ;;
  *) unknown "SWEEP_CONVERSATION_FILE must be <session id>/<slug>: $conversation_file" ;;
esac
live_seconds=${SWEEP_LIVE_SECONDS:-60}
is_count "$live_seconds" && [ "$live_seconds" -gt 0 ] && [ "$live_seconds" -le 2147413 ] ||
  unknown "SWEEP_LIVE_SECONDS must be a whole number of seconds, at least 1 and at most 2147413: $live_seconds"
# The probe accepts canonical decimal seconds, and its timers must fit a signed 32-bit millisecond delay.
while [ "${live_seconds#0}" != "$live_seconds" ]; do live_seconds=${live_seconds#0}; done
call_seconds=${SWEEP_CALL_SECONDS:-20}
is_count "$call_seconds" && [ "$call_seconds" -gt 0 ] || unknown "SWEEP_CALL_SECONDS must be a whole number of seconds, at least 1: $call_seconds"
pace=${SWEEP_PACE_SECONDS:-0.1}
is_seconds "$pace" || unknown "SWEEP_PACE_SECONDS must be a number of seconds: $pace"

for tool in curl jq bun mktemp sed awk grep sort; do
  command -v "$tool" >/dev/null 2>&1 || unknown "$tool is not on PATH"
done
curl_version=$(curl --version 2>/dev/null | awk 'NR == 1 { print $2 }')
curl_major=${curl_version%%.*}
curl_minor=${curl_version#*.}
curl_minor=${curl_minor%%.*}
if ! is_count "$curl_major" || ! is_count "$curl_minor"; then
  unknown "curl reports no version this script reads: $curl_version"
fi
if [ "$curl_major" -lt 7 ] || { [ "$curl_major" -eq 7 ] && [ "$curl_minor" -lt 55 ]; }; then
  unknown "curl $curl_version is older than 7.55, which reads a header from a file"
fi
[ -r "$routes" ] || unknown "the route index $routes cannot be read"
[ -r "$openapi" ] || unknown "the API document $openapi cannot be read"
[ -r "$socket_probe" ] || unknown "the socket probe $socket_probe cannot be read"

umask 077
work=$(mktemp -d "${TMPDIR:-/tmp}/restricted-sweep.XXXXXXXX") || unknown 'mktemp could not make the run directory'
printf 'Authorization: Bearer %s\n' "$full_token" >"$work/full.h" || unknown 'could not write the full member header'
printf 'Cookie: %s\n' "$cookie" >"$work/restricted.h" || unknown 'could not write the restricted member header'
unset full_token cookie
: >"$work/failed"
: >"$work/unknown"

calls=0
# call WHO PATH OUT [JSON FILE]: GET PATH as WHO (full or restricted), or with JSON FILE a POST of
# that file, the body in OUT and the status in $status, or `curl failed: ...` when no answer came.
# The member sends its cookie with the origin's Origin, as the dashboard does; curl reads no
# ~/.curlrc (-q), no proxy, and no URL pattern (-g).
call() {
  local -a credential data
  credential=(-H "@$work/full.h") data=()
  [ "$1" = full ] || credential=(-H "@$work/restricted.h" -H "Origin: $origin")
  [ -z "${4:-}" ] || data=(-X POST -H 'Content-Type: application/json' --data-binary "@$4")
  [ "$pace" = 0 ] || sleep "$pace" || unknown 'SWEEP_PACE_SECONDS is not a duration sleep accepts'
  : >"$3" || unknown 'could not create the response body file'
  calls=$((calls + 1))
  status=$(curl -q -sS -g --noproxy '*' --max-time "$call_seconds" -o "$3" -w '%{http_code}' \
    "${credential[@]}" ${data[@]+"${data[@]}"} "$origin$2" </dev/null 2>"$work/curl.err") ||
    status="curl failed: $(tr '\n' ' ' <"$work/curl.err" | cut -c1-200)"
}

uri() { jq -rn --arg v "$1" '$v|@uri'; }
# first_of FILE FILTER prints the first value jq's FILTER yields of FILE, or nothing.
first_of() { jq -r "first($2) // empty" "$1" 2>/dev/null; }
# Redact credentials before shortening diagnostics, including individually reflected cookie values.
redacted() {
  local text=$1 header secret rest part
  for header in full restricted; do
    IFS= read -r secret <"$work/$header.h" || continue
    secret=${secret#*: }
    text=${text//"$secret"/<redacted>}
    if [ "$header" = full ]; then
      secret=${secret#Bearer }
      [ -z "$secret" ] || text=${text//"$secret"/<redacted>}
    else
      rest=$secret
      while [ -n "$rest" ]; do
        part=${rest%%;*}
        secret=${part#*=}
        [ -z "$secret" ] || text=${text//"$secret"/<redacted>}
        [ "$part" != "$rest" ] || break
        rest=${rest#*;}
      done
    fi
  done
  printf '%s' "$text"
}
head_of() { redacted "$(cat "$1" 2>/dev/null)" | tr '\n' ' ' | cut -c1-"${2:-200}"; }
fail() {
  local message
  message=$(redacted "$1")
  printf 'FAIL    %s\n' "$message"
  printf '%s\n' "$message" >>"$work/failed" || unknown 'could not record a failed check'
}
unproven() {
  local message
  message=$(redacted "$1")
  printf 'UNKNOWN %s\n' "$message"
  printf '%s\n' "$message" >>"$work/unknown" || unknown 'could not record an unproven check'
}

printf 'restricted-sweep: %s, project %s, %s\n' "$origin" "$project" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# The identities. The member must be a restricted member the server signs in; the full member's
# token must see the project, whose access list must not name the member.
call restricted /api/v1/whoami "$work/body"
[ "$status" = 200 ] || unknown "GET /api/v1/whoami as the member answered $status: $(head_of "$work/body")"
member=$(first_of "$work/body" 'select(.kind == "user" and .member_kind == "restricted") | .login')
[ -n "$member" ] || unknown "the cookie signs in no restricted member: GET /api/v1/whoami answered $(head_of "$work/body")"
call full /api/v1/whoami "$work/body"
[ "$status" = 200 ] || unknown "GET /api/v1/whoami with SWEEP_FULL_TOKEN answered $status: $(head_of "$work/body")"
call full /api/v1/projects "$work/body"
[ "$status" = 200 ] || unknown "GET /api/v1/projects with SWEEP_FULL_TOKEN answered $status: $(head_of "$work/body")"
[ -n "$(first_of "$work/body" ".[] | select(.key == \"$project\") | .key")" ] ||
  unknown "SWEEP_FULL_TOKEN does not see $project: GET /api/v1/projects lists no such project"
call full "/api/v1/projects/$project/access" "$work/body"
[ "$status" = 200 ] || unknown "GET /api/v1/projects/$project/access with SWEEP_FULL_TOKEN answered $status: $(head_of "$work/body")"
[ -z "$(jq -r --arg m "$member" '.people[]? | select(ascii_downcase == ($m | ascii_downcase))' "$work/body")" ] ||
  unknown "$project's access list names $member, so the member may read it and this sweep proves nothing"
printf 'member  %s, a restricted member %s'"'"'s access list does not name\n' "$member" "$project"

# The lookup, as the full member: an object of each kind the routes name. A kind the project owns
# and the lookup did not find is written to `missing`, which leaves the run UNKNOWN.
issue='' issue_title='' doc='' doc_slug='' block='' pdoc='' pdoc_slug='' comment='' ask=''
message='' message_issue='' handoff='' handoff_issue='' handoff_phase='' near_copy=''
session='' person='' repo_owner='' repo_name='' priority='' broadcast='' delivery_run='' going_run=''
ref_prefix='' going_why=''
: >"$work/values"
# found LABEL VALUE: a project object the lookup found, which marks a row of the project.
found() {
  printf 'found   %s %s\n' "$1" "$2"
  printf '%s\n' "$2" >>"$work/values"
}
# A page of the project's issues: given a limit, the list answers {issues: [...]}.
call full "/api/v1/issues?project=$project&limit=50" "$work/body"
[ "$status" = 200 ] || unknown "GET /api/v1/issues?project=$project with SWEEP_FULL_TOKEN answered $status: $(head_of "$work/body")"
jq -c 'if type == "object" then .issues else . end' "$work/body" >"$work/issues" 2>/dev/null
jq -r '.[]? | .key' "$work/issues" 2>/dev/null >"$work/keys"
[ -s "$work/keys" ] || unknown "the full member finds no issue of $project, so no route can be filled with its objects"
scanned=0
# authors FILE FILTER: the first person and the first session that FILTER's authors name.
authors() {
  [ -n "$person" ] || person=$(first_of "$1" "$2 | select(.kind == \"user\") | .id")
  [ -n "$session" ] || session=$(first_of "$1" "$2 | select(.kind == \"session\") | .id")
}
while read -r key && [ "$scanned" -lt 25 ]; do
  scanned=$((scanned + 1))
  if [ -z "$block" ]; then
    call full "/api/v1/issues/$key/summary" "$work/body"
    if [ "$status" = 200 ]; then
      spec=$(first_of "$work/body" '.documents[]? | select(.primary) | .id')
      spec_slug=$(first_of "$work/body" '.documents[]? | select(.primary) | .slug')
      if [ -n "$spec" ] && [ -n "$spec_slug" ]; then
        call full "/api/v1/artifacts/$(uri "$spec")/blocks" "$work/body"
        first_block=$(first_of "$work/body" '.[]? | .id')
        if [ "$status" = 200 ] && [ -n "$first_block" ]; then
          issue=$key doc=$spec doc_slug=$spec_slug block=$first_block
          issue_title=$(jq -r --arg k "$key" 'first(.[]? | select(.key == $k) | .title) // empty' "$work/issues")
        fi
      fi
    fi
  fi
  if [ -z "$comment" ]; then
    call full "/api/v1/issues/$key/comments" "$work/body"
    [ "$status" != 200 ] || { comment=$(first_of "$work/body" '.[]? | .id') && authors "$work/body" '.[]? | .author'; }
  fi
  if [ -z "$ask" ]; then
    call full "/api/v1/issues/$key/asks" "$work/body"
    [ "$status" != 200 ] || { ask=$(first_of "$work/body" '.[]? | .id') && authors "$work/body" '.[]? | .author'; }
  fi
  if [ -z "$message" ]; then
    call full "/api/v1/issues/$key/messages" "$work/body"
    if [ "$status" = 200 ]; then
      message=$(first_of "$work/body" '.[]? | .message.id')
      [ -z "$message" ] || message_issue=$key
      authors "$work/body" '.[]? | .message.author'
    fi
  fi
  if [ -z "$handoff" ]; then
    call full "/api/v1/issues/$key/handoffs" "$work/body"
    if [ "$status" = 200 ]; then
      handoff=$(first_of "$work/body" '.[]? | .id')
      handoff_phase=$(first_of "$work/body" '.[]? | .phase')
      [ -z "$handoff" ] || handoff_issue=$key
      authors "$work/body" '.[]? | .author'
    fi
  fi
  [ -z "$block" ] || [ -z "$comment" ] || [ -z "$ask" ] || [ -z "$message" ] || [ -z "$handoff" ] ||
    [ -z "$person" ] || [ -z "$session" ] || break
done <"$work/keys"
repo=$(first_of "$work/issues" '.[]? | (.repo // .resolved_repo) | select(type == "string" and test("^[^/]+/[^/]+$"))')
[ -z "$repo" ] || { repo_owner=${repo%%/*} && repo_name=${repo#*/}; }
call full "/api/v1/projects/$project/artifacts" "$work/body"
if [ "$status" = 200 ]; then
  pdoc=$(first_of "$work/body" '.[]? | select(.issue_key == null) | .id')
  pdoc_slug=$(first_of "$work/body" '.[]? | select(.issue_key == null) | .slug')
fi
call full "/api/v1/near-copy-checks?project=$project" "$work/body"
[ "$status" != 200 ] || near_copy=$(first_of "$work/body" '.checks[]? | .id')
# The kinds no project owns, looked up where a route lists them.
call full /api/v1/priorities "$work/body"
[ "$status" != 200 ] || priority=$(first_of "$work/body" '.priorities[]? | .id')
call full /api/v1/broadcasts "$work/body"
[ "$status" != 200 ] || broadcast=$(first_of "$work/body" '.[]? | .id')
call full /api/v1/delivery/timeline "$work/body"
[ "$status" != 200 ] || delivery_run=$(first_of "$work/body" '.runs[]? | .id | tostring')
# How a reference to an object is spelled, as the server itself writes one: a comment's node
# names its ref, which starts with the comment's issue key. Never spelled here, so a change of
# spelling reaches the sweep from the server.
if [ -n "$comment" ]; then
  call full "/api/v1/comments/$(uri "$comment")/blocks" "$work/body"
  if [ "$status" = 200 ]; then
    ref=$(first_of "$work/body" '.item.ref | select(type == "string")')
    ref_issue=$(first_of "$work/body" '.item.issue_key | select(type == "string")')
    case $ref in ?*"$ref_issue"/*) [ -z "$ref_issue" ] || ref_prefix=${ref%%"$ref_issue"/*} ;; esac
  fi
fi
# A question's run belongs to the credential that asked it, and no route lists one, so the full
# member asks one of the project: the sweep's first write.
jq -cn --arg p "$project" \
  '{words: "restricted-sweep: which issues does this project hold?", project: $p}' >"$work/question.json" ||
  unknown 'could not write the question'
call full /api/v1/going/requests "$work/body" "$work/question.json"
[ "$status" != 201 ] || going_run=$(first_of "$work/body" '.id | select(type == "string")')
[ -z "$going_run" ] || found going-run "$going_run"
[ -n "$going_run" ] || going_why="POST /api/v1/going/requests answered $status: $(head_of "$work/body")"

: >"$work/missing"
# need LABEL VALUE WHERE: a kind the project owns, found or written to `missing`.
need() {
  if [ -n "$2" ]; then
    found "$1" "$2"
  else
    printf 'missing %s: %s\n' "$1" "$3"
    printf '%s: %s\n' "$1" "$3" >>"$work/missing"
  fi
}
need issue "$issue" "no issue of the first $scanned holds a spec with a block"
need document "$doc" "no issue of the first $scanned holds a spec with a block"
need block "$block" "no issue of the first $scanned holds a spec with a block"
need comment "$comment" "no issue of the first $scanned holds a comment"
need ask "$ask" "no issue of the first $scanned holds an ask"
need message "$message" "no issue of the first $scanned holds a message"
need handoff "$handoff" "no issue of the first $scanned holds a handoff"
need project-document "$pdoc" "GET /api/v1/projects/$project/artifacts lists no document of the project itself"
need near-copy-check "$near_copy" "GET /api/v1/near-copy-checks?project=$project lists none"
# A person, a session and a repository belong to no project, but the routes that name them are
# filled with ones that worked in the project, which every project of the deployment has.
[ -n "$person" ] || printf 'person: no comment, ask, message or handoff of the first %s issues names a person\n' "$scanned" >>"$work/missing"
[ -n "$session" ] || printf 'session: no comment, ask, message or handoff of the first %s issues names a session\n' "$scanned" >>"$work/missing"
[ -n "$repo" ] || printf 'repository: no issue of GET /api/v1/issues?project=%s names a repository\n' "$project" >>"$work/missing"
printf 'found   person %s, session %s, repository %s, priority %s, broadcast %s, delivery run %s\n' \
  "${person:-none}" "${session:-none}" "${repo:-none}" "${priority:-none}" "${broadcast:-none}" "${delivery_run:-none}"
[ -z "$issue_title" ] || [ "${#issue_title}" -lt 8 ] || printf '%s\n' "$issue_title" >>"$work/values"
jq -Rn --arg key "$project" '{key: $key, values: ([inputs | select(length > 0) | {(.): true}] | add // {})}' \
  "$work/values" >"$work/markers.json" || unknown 'jq could not write the markers'

# marker_in FILE prints the first marker of the project, including references embedded in JSON
# text. A JSON stream is read in full; false and null are JSON too. A body jq cannot read as JSON
# is read with grep for the same markers. One jq a body: the sweep reads thousands.
marker_in() {
  jq -r -s --slurpfile m "$work/markers.json" '$m[0] as $m
      | ("(^|[^A-Za-z0-9])(?<issue>" + $m.key + "-[1-9][0-9]*)([^A-Za-z0-9]|$)") as $issue
      | first(.[] | .. | ((objects | keys[]), strings) | . as $value
        | if . == $m.key then $m.key
          elif test($issue) then capture($issue).issue
          else $m.values | keys[] | select(. as $marker | $value == $marker
            or (($marker | length) >= 8 and ($value | contains($marker)))) end) // empty' "$1" 2>/dev/null ||
    { grep -E -o "(^|[^A-Za-z0-9])$project-[1-9][0-9]*([^A-Za-z0-9]|$)" "$1"
      grep -F -x "$project" "$1"
      grep -F -o -f "$work/values" "$1"
    } 2>/dev/null | head -n 1
}

# masked FILE VALUE... masks literal fills, their URL and JSON encodings, every RFC 3339 instant
# and the durations a read measures, so two answers read at two moments for two fills compare
# alike when they say the same: a read's own `as_of`, `computed_at` and window bounds differ at
# every read, and a row's own times come with its ids, which the markers read.
masked() {
  local file=$1
  shift
  jq -nr --rawfile body "$file" --arg project "$project" '
    reduce $ARGS.positional[] as $value ($body;
      if $value == "" then .
      elif $value == $project then
        gsub("(?<![A-Za-z0-9-])" + $value + "(?![A-Za-z0-9-])"; "<fill>")
      else
        reduce ([$value, ($value | @uri), ($value | tojson | .[1:-1])] | unique[]) as $literal
          (.; split($literal) | join("<fill>"))
      end)
    | gsub("[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\\.[0-9]+)?(Z|[+-][0-9]{2}:[0-9]{2})";
        "<time>")
    | gsub("\"(?<key>took_ms|age_seconds)\"[[:space:]]*:[[:space:]]*[0-9.]+";
        "\"\(.key)\":<n>")' --args "$@"
}

# masked_marker FILE VALUE...: the first row of the project FILE holds once each VALUE, a value
# the request was filled with and which the answer may echo, is masked.
masked_marker() {
  local file=$1
  shift
  masked "$file" "$@" >"$work/masked" || unknown 'could not mask an answer'
  marker_in "$work/masked"
}

is_2xx() { case $1 in 2??) return 0 ;; esac; return 1; }
is_refusal() { case $1 in 403 | 404) return 0 ;; esac; return 1; }
# is_judgeable STATUS: a status some rule below reads, which 401, 429, a 5xx and no answer are not.
is_judgeable() { case $1 in 401 | 429 | 5?? | curl*) return 1 ;; [1-4]??) return 0 ;; esac; return 1; }

live=0 blind=0 judged=0 unfilled=0
# counted CAN: counts the row as one that can fail (CAN 1) or a blind one, and sets can_text to
# which, for the row's line.
counted() {
  judged=$((judged + 1))
  if [ "$1" = 1 ]; then
    live=$((live + 1))
    can_text='can fail'
  else
    blind=$((blind + 1))
    can_text=blind
  fi
}

# judge LABEL TARGET MISSING STRICT TARGET_FILLS MISSING_FILLS: the row LABEL, called as TARGET
# (filled with the project's objects) and, where an answer needs it, as MISSING (filled with
# objects that do not exist). The fills are newline-separated lists of the values each side was
# filled with, masked before two answers are compared. STRICT is 1 when an object of the
# project fills the row, which then must answer the member as a missing one, or refuse.
judge() {
  local label=$1 target=$2 missing=$3 strict=$4 t_fills=$5 m_fills=$6 rt rm ft fm can=0 marker line mt mm
  local -a t_list m_list
  t_list=() m_list=()
  while IFS= read -r line; do [ -z "$line" ] || t_list+=("$line"); done <<EOF
$t_fills
EOF
  while IFS= read -r line; do [ -z "$line" ] || m_list+=("$line"); done <<EOF
$m_fills
EOF
  # Whether the row can fail: the full member is answered the project's object otherwise than a
  # missing one, or a row of it beyond what the request echoes. A failed missing-object request
  # establishes nothing.
  call full "$target" "$work/ft"
  ft=$status
  if is_2xx "$ft" && [ "$strict" = 1 ]; then
    call full "$missing" "$work/fm"
    fm=$status
    mt=$(masked "$work/ft" ${t_list[@]+"${t_list[@]}"}) || unknown 'could not mask the full member answer'
    mm=$(masked "$work/fm" ${m_list[@]+"${m_list[@]}"}) || unknown 'could not mask the missing-object control'
    if is_judgeable "$fm" && { [ "$fm" != "$ft" ] || [ "$mt" != "$mm" ]; }; then
      can=1
    fi
  fi
  if [ "$can" = 0 ] && is_judgeable "$ft" && ! is_refusal "$ft" &&
    [ -n "$(masked_marker "$work/ft" ${t_list[@]+"${t_list[@]}"})" ]; then
    can=1
  fi
  counted "$can"
  call restricted "$target" "$work/rt"
  rt=$status
  if ! is_judgeable "$rt"; then
    unproven "$label: the member is answered $rt, which no rule judges: $(head_of "$work/rt")"
    return
  fi
  # The answer is read for rows before it is compared with anything, so an answer alike for a
  # missing object, a filter the server ignored, excuses none.
  marker=$(masked_marker "$work/rt" ${t_list[@]+"${t_list[@]}"})
  if [ -n "$marker" ]; then
    if is_refusal "$rt"; then
      fail "$label: the refusal $rt holds $marker, a row of $project: $(head_of "$work/rt")"
    else
      fail "$label: the member is answered $rt holding $marker, a row of $project: $(head_of "$work/rt")"
    fi
    return
  fi
  if is_refusal "$rt"; then
    printf 'ok      %s: %s (%s)\n' "$label" "$rt" "$can_text"
    return
  fi
  mt=$(masked "$work/rt" ${t_list[@]+"${t_list[@]}"}) || unknown 'could not mask the member answer'
  call restricted "$missing" "$work/rm"
  rm=$status
  mm=$(masked "$work/rm" ${m_list[@]+"${m_list[@]}"}) || unknown 'could not mask the missing-object answer'
  if [ "$rm" = "$rt" ] && [ "$mt" = "$mm" ]; then
    printf 'ok      %s: %s, as a missing one is answered (%s)\n' "$label" "$rt" "$can_text"
  elif is_2xx "$rt" && [ "$strict" = 0 ]; then
    printf 'ok      %s: %s, no row of %s (%s)\n' "$label" "$rt" "$project" "$can_text"
  elif is_2xx "$rt" && is_refusal "$rm"; then
    # The object exists for the member where a missing one does not, an empty answer included.
    fail "$label: the member is answered $rt for $project's object, where a missing one is refused $rm: $(head_of "$work/rt")"
  elif is_2xx "$rt" && is_judgeable "$rm"; then
    fail "$label: the member is answered $rt for $project's object, otherwise than a missing one answered $rm: $(head_of "$work/rt")"
  else
    unproven "$label: the member is answered $rt, where a missing one is answered $rm: $(head_of "$work/rt")"
  fi
}

# judge_list LABEL PATH: a row whose path names no object, called bare. Its answer to the member
# may hold the member's own rows, never one of the project; a refusal of any other 4xx is
# judged alike for both callers.
judge_list() {
  local label=$1 path=$2 rt ft can=0 marker
  call full "$path" "$work/ft"
  ft=$status
  if is_2xx "$ft" && [ -n "$(marker_in "$work/ft")" ]; then can=1; fi
  counted "$can"
  call restricted "$path" "$work/rt"
  rt=$status
  marker=$(marker_in "$work/rt")
  if is_judgeable "$rt" && [ -n "$marker" ]; then
    fail "$label: the member is answered $rt holding $marker, a row of $project: $(head_of "$work/rt")"
    return
  fi
  if is_refusal "$rt"; then
    printf 'ok      %s: %s (%s)\n' "$label" "$rt" "$can_text"
  elif is_2xx "$rt"; then
    printf 'ok      %s: %s, no row of %s (%s)\n' "$label" "$rt" "$project" "$can_text"
  elif is_judgeable "$rt" && [ "$rt" = "$ft" ]; then
    printf 'ok      %s: %s, as the full member is answered (%s)\n' "$label" "$rt" "$can_text"
  else
    unproven "$label: the member is answered $rt, the full member $ft: $(head_of "$work/rt")"
  fi
}

# not_filled LABEL WHY: a row the sweep could not fill, which leaves the run UNKNOWN.
not_filled() {
  unfilled=$((unfilled + 1))
  printf 'not filled %s: %s\n' "$1" "$2"
  printf '%s is not filled: %s\n' "$1" "$2" >>"$work/unknown" || unknown 'could not record an unfilled row'
}

# The missing side of each kind: an id, a key or a name nothing holds.
none_uuid=00000000-0000-4000-8000-000000000000

# fill KIND PATTERN sets fill_target and fill_missing to the values KIND's wildcard takes in a
# row of PATTERN, fill_owned to 1 when the project owns KIND, and fill_why to why the target is
# empty when the lookup found none.
fill() {
  fill_owned=1 fill_why="the lookup found no $1 of $project" fill_missing=$none_uuid fill_target=''
  case $1 in
    project) fill_target=$project fill_missing=ZZNONE ;;
    issue)
      fill_missing=ZZNONE-1
      case $2 in
        */messages/\{id\}*) fill_target=$message_issue ;;
        */handoffs/\{phase\}*) fill_target=$handoff_issue ;;
        *) fill_target=$issue ;;
      esac
      ;;
    document)
      case $2 in
        # A document's own asks, comments, events and subscribers are a project document's: an
        # issue's documents answer them 400 ARTIFACT_LINKED and take them through the issue.
        /api/v1/artifacts/\{id\}/asks | /api/v1/artifacts/\{id\}/comments | /api/v1/artifacts/\{id\}/events | \
          /api/v1/artifacts/\{id\}/subscribers)
          fill_target=$pdoc fill_why="the lookup found no document of $project itself" ;;
        *) fill_target=$doc ;;
      esac
      ;;
    document-slug)
      fill_missing=no-such-document
      case $2 in
        /api/v1/issues/*) fill_target=$doc_slug ;;
        /api/v1/projects/*) fill_target=$pdoc_slug fill_why="the lookup found no document of $project itself" ;;
        *) fill_target=$conversation_slug fill_why=$conversation_why ;;
      esac
      ;;
    document-version) fill_target=1 fill_missing=1 ;;
    block) fill_target=$block fill_missing=b-nosuchblock ;;
    ask) fill_target=$ask ;;
    comment) fill_target=$comment ;;
    message) fill_target=$message ;;
    handoff) fill_target=$handoff ;;
    handoff-phase) fill_target=$handoff_phase fill_missing=$handoff_phase ;;
    near-copy-check) fill_target=$near_copy ;;
    going-run) fill_target=$going_run fill_why="the full member's question was refused: $going_why" ;;
    session)
      fill_missing=ses-nosuchsession
      case $2 in
        # A file of the conversation of a session that worked in the project alone, which the
        # member, no party to it, must be answered as missing.
        /api/v1/agents/\{session_id\}/artifacts/*) fill_target=$conversation_session fill_why=$conversation_why ;;
        *) fill_owned=0 fill_target=$session fill_why="no object of $project the lookup read names a session" ;;
      esac
      ;;
    person) fill_owned=0 fill_target=$person fill_missing=nobody@example.com fill_why="no object of $project the lookup read names a person" ;;
    repository-owner) fill_owned=0 fill_target=$repo_owner fill_missing=$repo_owner fill_why="no issue of $project names a repository" ;;
    repository-name) fill_owned=0 fill_target=$repo_name fill_missing=no-such-repo fill_why="no issue of $project names a repository" ;;
    priority) fill_owned=0 fill_target=$priority fill_missing=no-such-priority fill_why='the full member reads no priority' ;;
    broadcast) fill_owned=0 fill_target=$broadcast fill_why='the full member reads no broadcast' ;;
    delivery-run) fill_owned=0 fill_target=$delivery_run fill_missing=999999999999 fill_why='the full member reads no delivery run' ;;
    *) fill_why="the sweep fills no {$1} wildcard" ;;
  esac
}

# query_kind PATTERN PARAM prints how a list row's query parameter is filled: a kind of object,
# `skip` for one that names no object of a project, or nothing for one the sweep does not know.
query_kind() {
  case "$1 $2" in
    '/api/v1/block-links from') echo document-refs ; return ;;
    '/api/v1/block-links to') echo skip ; return ;;
    '/api/v1/references from' | '/api/v1/references to' | '/api/v1/reference-targets ref') echo issue-ref ; return ;;
    '/api/v1/references/questions id') echo comment ; return ;;
    '/api/v1/search q' | '/api/v1/search/results q') echo title ; return ;;
  esac
  case $2 in
    project | number_project) echo project ;;
    issue | parent | ref) echo issue ;;
    artifact) echo document ;;
    repo) echo repository ;;
    # A person or a session is no project's node; the rest name no object.
    author_session | parent_agent | assignee | approver | person | author | session) echo skip ;;
    advances | after | at | before | blocks | closed_before | closed_since | component | cursor | deployed | \
      download | exclude_issue | exclude_issues | from | ids | keywords_only | kind | label | limit | max_chars | \
      moved | offset | open | order | pinned | priority | q | rework | route_status | since | source_kind | state | \
      status | summary | text | thread | title | to | unlinked | updated_since | versions | wait) echo skip ;;
  esac
}

# Each GET path's query parameters, from the API document, one `path param` line each.
jq -r '.paths | to_entries[] | .key as $p | .value.get.parameters[]? | select(.in == "query") | "\($p) \(.name)"' \
  "$openapi" >"$work/params" 2>/dev/null || unknown "the API document $openapi holds no paths jq reads"
[ -s "$work/params" ] || unknown "the API document $openapi names no query parameter, so it is not the document this sweep reads"

# sweep_queries PATTERN: each query parameter of the list PATTERN that names an object of a
# project, filled with the project's and with a missing one.
sweep_queries() {
  local pattern=$1 param kind target missing strict t_fills m_fills fixed
  while read -r param; do
    [ -n "$param" ] || continue
    kind=$(query_kind "$pattern" "$param")
    strict=1 target='' missing=''
    case $kind in
      skip) continue ;;
      '')
        unproven "GET $pattern takes ?$param=, which the sweep does not classify"
        continue
        ;;
      project) target=$project missing=ZZNONE ;;
      issue) target=$issue missing=ZZNONE-1 ;;
      issue-ref) [ -z "$issue" ] || [ -z "$ref_prefix" ] || target=$ref_prefix$issue missing=${ref_prefix}ZZNONE-1 ;;
      document) target=$doc missing=$none_uuid ;;
      comment) target=$comment missing=00000000-0000-4000-8000-000000000002 ;;
      repository) target=$repo missing=$repo_owner/no-such-repo strict=0 ;;
      title) target=$issue_title missing='zz no such words zz' strict=0 ;;
      # Both ends of a block link must name a document: the issue's spec and the project's own.
      document-refs) [ -z "$issue" ] || [ -z "$pdoc_slug" ] || [ -z "$ref_prefix" ] || target=$issue ;;
    esac
    if [ -z "$target" ]; then
      case $kind in
        issue-ref | document-refs) not_filled "GET $pattern?$param=" "no node the lookup read names its reference, so the sweep cannot spell one" ;;
        *) not_filled "GET $pattern?$param=" "the lookup found no $kind of $project" ;;
      esac
      continue
    fi
    # A refusal names a reference's issue by its bare key too, so the fills mask that as well.
    t_fills=$target$'\n'$issue m_fills=$missing$'\n'ZZNONE-1
    case $kind in
      document-refs)
        judge "GET $pattern?from=&to=" \
          "$pattern?from=$(uri "$ref_prefix$issue/spec")&to=$(uri "$ref_prefix$project/artifact/$pdoc_slug")" \
          "$pattern?from=$(uri "${ref_prefix}ZZNONE-1/spec")&to=$(uri "${ref_prefix}ZZNONE/artifact/no-such-document")" \
          1 "$project/artifact/$pdoc_slug"$'\n'"$issue" "ZZNONE/artifact/no-such-document"$'\n'ZZNONE-1
        ;;
      comment)
        judge "GET $pattern?kind=comment&id=" "$pattern?kind=comment&id=$(uri "$target")" \
          "$pattern?kind=comment&id=$(uri "$missing")" 1 "$target" "$missing"
        ;;
      issue-ref)
        judge "GET $pattern?$param=" "$pattern?$param=$(uri "$target")" "$pattern?$param=$(uri "$missing")" \
          1 "$t_fills" "$m_fills"
        ;;
      *)
        # A search answers nothing but 400 without its text, so its filters go with the title.
        fixed=''
        case "$pattern $param" in
          '/api/v1/search q' | '/api/v1/search/results q') ;;
          '/api/v1/search '* | '/api/v1/search/results '*) [ -z "$issue_title" ] || fixed="q=$(uri "$issue_title")&" ;;
        esac
        judge "GET $pattern?${fixed:+q=<title>&}$param=" "$pattern?$fixed$param=$(uri "$target")" \
          "$pattern?$fixed$param=$(uri "$missing")" "$strict" "$target" "$missing"
        ;;
    esac
  done <<EOF
$(awk -v p="$pattern" '$1 == p { print $2 }' "$work/params")
EOF
}

# sweep_filled PATTERN: a row whose path names objects, each filled from the lookup.
sweep_filled() {
  local pattern=$1 wildcard kind target=$1 missing=$1 strict=0 t_fills='' m_fills=''
  for wildcard in $(wildcards_of "$pattern"); do
    kind=$(kind_of "$pattern" "$wildcard")
    fill "$kind" "$pattern"
    if [ -z "$fill_target" ]; then
      not_filled "GET $pattern" "{$wildcard}: $fill_why"
      return
    fi
    target=${target//"{$wildcard}"/$(uri "$fill_target")}
    missing=${missing//"{$wildcard}"/$(uri "$fill_missing")}
    if [ "$fill_target" != "$fill_missing" ]; then
      t_fills=$t_fills$fill_target$'\n'
      m_fills=$m_fills$fill_missing$'\n'
    fi
    [ "$fill_owned" = 0 ] || strict=1
  done
  judge "GET $pattern" "$target" "$missing" "$strict" "$t_fills" "$m_fills"
}

started=$SECONDS
rows=0
while read -r method pattern _ || [ -n "${method:-}" ]; do
  [ "$method" = GET ] || continue
  rows=$((rows + 1))
  [ -n "$pattern" ] || unknown 'the route index holds a GET row with no path'
  case $pattern in
    *'{'*) sweep_filled "$pattern" ;;
    *)
      judge_list "GET $pattern" "$pattern"
      sweep_queries "$pattern"
      ;;
  esac
done <"$routes"
[ "$rows" -gt 0 ] || unknown "the route index $routes holds no GET row"

# The document socket. The member's cookie is the one credential it admits; the control is the
# same probe admitting the member to a document of a project the member sees.
call restricted /api/v1/schema/blocks "$work/body"
schema_version=$(first_of "$work/body" '.version // .schema_version | tostring')
own_doc=''
call restricted /api/v1/projects "$work/body"
if [ "$status" = 200 ]; then
  jq -r --arg p "$project" '.[]? | .key | select(. != $p)' "$work/body" 2>/dev/null >"$work/own"
  while read -r own; do
    call restricted "/api/v1/projects/$own/artifacts" "$work/body"
    [ "$status" != 200 ] || own_doc=$(first_of "$work/body" '.[]? | .id')
    [ -z "$own_doc" ] || break
  done <"$work/own"
fi
if [ -z "$schema_version" ]; then
  unproven "/ws/doc/{room}: GET /api/v1/schema/blocks names no schema version"
elif [ -z "$doc" ]; then
  unproven "/ws/doc/{room}: the lookup found no document of $project"
elif [ -z "$own_doc" ]; then
  unproven "/ws/doc/{room}: the member sees no document of a project of theirs, so no control shows the probe admits them anywhere"
else
  bun "$socket_probe" doc "$origin" "$work/restricted.h" "$own_doc" "$schema_version" >"$work/doc-own" 2>&1
  own_exit=$?
  bun "$socket_probe" doc "$origin" "$work/restricted.h" "$doc" "$schema_version" >"$work/doc-target" 2>&1
  answer_exit=$?
  own_answer=$(head_of "$work/doc-own")
  answer=$(head_of "$work/doc-target")
  case $answer in
    'authenticated '*) fail "/ws/doc/{room}: the member is admitted to the room of $project's document: $answer" ;;
    *)
      if [ "$own_exit" != 0 ] || [ "$answer_exit" != 0 ]; then
        unproven "/ws/doc/{room}: a probe failed (control $own_exit, member $answer_exit): $own_answer; $answer"
      elif [ "${own_answer#authenticated }" = "$own_answer" ]; then
        unproven "/ws/doc/{room}: the control did not hold: the member is not admitted to their own document: $own_answer"
      else
        masked "$work/doc-target" "$doc" >"$work/doc-refusal" || unknown 'could not mask the document refusal'
        marker=$(marker_in "$work/doc-refusal")
        if [ -n "$marker" ]; then
          fail "/ws/doc/{room}: the refusal holds $marker, a row of $project: $answer"
        else
          case $answer in
            'denied '* | 'http 403 '* | 'http 404 '*) printf 'ok      /ws/doc/{room}: %s (control: admitted to their own document)\n' "$answer" ;;
            *) unproven "/ws/doc/{room}: the member's answer for $project's document is no admission and no refusal: $answer" ;;
          esac
        fi
      fi
      ;;
  esac
fi

# The live socket: both feeds open, the full member comments on SWEEP_WRITE_ISSUE, both are read
# for the window. The full member's feed must be sent the comment, or the member's silence
# proves nothing.
live_result='' written=''
bun "$socket_probe" watch "$origin" "$work/restricted.h" "$work/ready-restricted" "$work/written" \
  "$live_seconds" >"$work/live-restricted" 2>&1 &
watcher_restricted=$!
bun "$socket_probe" watch "$origin" "$work/full.h" "$work/ready-full" "$work/written" \
  "$live_seconds" >"$work/live-full" 2>&1 &
watcher_full=$!
watchers="$watcher_restricted $watcher_full"
ready_started=$SECONDS
until { [ -e "$work/ready-restricted" ] || ! kill -0 "$watcher_restricted" 2>/dev/null; } &&
  { [ -e "$work/ready-full" ] || ! kill -0 "$watcher_full" 2>/dev/null; }; do
  [ "$((SECONDS - ready_started))" -lt 30 ] || break
  sleep 0.2
done
if [ ! -e "$work/ready-full" ] || ! kill -0 "$watcher_full" 2>/dev/null; then
  live_result="the full member's feed did not stay subscribed: $(head_of "$work/live-full")"
elif [ ! -e "$work/ready-restricted" ] && kill -0 "$watcher_restricted" 2>/dev/null; then
  live_result="the member's feed did not start in 30 s"
elif [ -e "$work/ready-restricted" ] && ! kill -0 "$watcher_restricted" 2>/dev/null; then
  live_result="the member's feed ended before the comment"
elif [ ! -e "$work/ready-restricted" ] && ! grep -E -q '^http (403|404) ' "$work/live-restricted"; then
  live_result="the member's probe ended without subscribing or refusing the upgrade: $(head_of "$work/live-restricted")"
else
  # A failed write ends the check immediately instead of waiting out a potentially long window.
  jq -cn --arg b "restricted-sweep: a probe of who is sent this comment live, written $(date -u +%Y-%m-%dT%H:%M:%SZ)." \
    '{body: $b, actor: {kind: "session", id: "restricted-sweep"}}' >"$work/comment.json" ||
    unknown 'could not write the live comment request'
  : >"$work/body" || unknown 'could not create the comment response file'
  [ "$pace" = 0 ] || sleep "$pace" || unknown 'SWEEP_PACE_SECONDS is not a duration sleep accepts'
  calls=$((calls + 1))
  status=$(curl -q -sS -g --noproxy '*' --max-time "$call_seconds" -o "$work/body" -w '%{http_code}' -X POST \
    -H "@$work/full.h" -H 'Content-Type: application/json' --data-binary "@$work/comment.json" \
    "$origin/api/v1/issues/$write_issue/comments" </dev/null 2>"$work/curl.err") || status="curl failed: $(head_of "$work/curl.err")"
  if [ "$status" = 201 ]; then
    written=$(first_of "$work/body" '.id | select(type == "string" and length > 0)')
  fi
  [ "$status" = 201 ] && [ -n "$written" ] ||
    live_result="the full member's comment on $write_issue answered $status: $(head_of "$work/body")"
fi
observation_started=$SECONDS
if [ -z "$live_result" ]; then
  : >"$work/written" || unknown 'could not signal the socket observation window'
  while kill -0 "$watcher_restricted" 2>/dev/null || kill -0 "$watcher_full" 2>/dev/null; do
    elapsed=$((SECONDS - observation_started))
    if [ "$elapsed" -lt "$live_seconds" ] &&
      { ! kill -0 "$watcher_full" 2>/dev/null ||
        { [ -e "$work/ready-restricted" ] && ! kill -0 "$watcher_restricted" 2>/dev/null; }; }; then
      live_result='a subscribed feed ended before the observation window finished'
      break
    fi
    if [ "$elapsed" -ge "$((live_seconds + 15))" ]; then
      live_result='a socket probe did not finish after the observation window'
      break
    fi
    sleep 0.2
  done
  if [ -z "$live_result" ] && [ "$((SECONDS - observation_started))" -lt "$live_seconds" ]; then
    live_result='the feeds did not cover the observation window'
  fi
fi
if [ -n "$live_result" ]; then
  for pid in $watchers; do kill "$pid" 2>/dev/null; done
fi
wait "$watcher_restricted"
restricted_exit=$?
wait "$watcher_full"
full_exit=$?
watchers=''
[ -z "$written" ] || printf '%s\n' "$written" >>"$work/values"
jq -Rn --arg key "$project" '{key: $key, values: ([inputs | select(length > 0) | {(.): true}] | add // {})}' \
  "$work/values" >"$work/markers.json" || unknown 'could not update the live event markers'
marker=$(marker_in "$work/live-restricted")
if [ -n "$marker" ]; then
  fail "/ws/live: the member is sent an event holding $marker, of $project: $(head_of "$work/live-restricted")"
elif [ -n "$live_result" ]; then
  unproven "/ws/live: $live_result"
elif [ "$full_exit" != 0 ] || ! jq -s -e --arg c "$written" '
  length > 0 and .[0].type == "events_start"
  and all(.[]; type == "object" and (.type | type == "string"))
  and any(.[]; .type == "event" and any(.. | strings; . == $c))' "$work/live-full" >/dev/null 2>&1; then
  unproven "/ws/live: the control did not hold: the full member's feed did not cover $live_seconds s and receive comment $written: $(head_of "$work/live-full")"
elif [ "$restricted_exit" != 0 ]; then
  unproven "/ws/live: the member's feed gave no answer: $(head_of "$work/live-restricted")"
else
  first_line=$(head -n 1 "$work/live-restricted")
  case $first_line in
    'http 403 '* | 'http 404 '*) printf 'ok      /ws/live: the member'"'"'s upgrade is refused: %s\n' "$(head_of "$work/live-restricted")" ;;
    'http '*) unproven "/ws/live: the member's upgrade is answered $(head_of "$work/live-restricted")" ;;
    *)
      if [ ! -e "$work/ready-restricted" ] || ! jq -s -e '
        length > 0 and .[0].type == "events_start"
        and all(.[]; type == "object" and (.type | type == "string"))' "$work/live-restricted" >/dev/null 2>&1; then
        unproven "/ws/live: the member's feed did not cover the observation window: $(head_of "$work/live-restricted")"
      else
        frames=$(jq -s '[.[] | select(.type == "event")] | length' "$work/live-restricted")
        printf 'ok      /ws/live: the member is sent %s events in %s s, none of %s; the full member is sent the comment (control)\n' \
          "$frames" "$live_seconds" "$project"
      fi
      ;;
  esac
fi

printf 'swept   %s GET rows: %s calls judged, %s can fail, %s blind, %s not filled; %s requests in %s s\n' \
  "$rows" "$judged" "$live" "$blind" "$unfilled" "$calls" "$((SECONDS - started))"
if [ -s "$work/failed" ]; then
  printf 'FAIL: %s answers gave the member what %s holds, the first: %s\n' "$(wc -l <"$work/failed" | tr -d ' ')" "$project" "$(head -n 1 "$work/failed")"
  exit 1
fi
[ "$live" -gt 0 ] || printf 'no row could fail: the full member is answered no row of %s anywhere\n' "$project" >>"$work/unknown"
[ "$judged" -gt 0 ] || printf 'no route was judged\n' >>"$work/unknown"
if [ -s "$work/missing" ] || [ -s "$work/unknown" ]; then
  printf 'UNKNOWN: nothing reached the member, but this run did not prove it:\n'
  sed 's/^/  /' "$work/missing" "$work/unknown"
  exit 2
fi
printf 'PASS: no route and neither socket gave the member a row of %s\n' "$project"
exit 0
