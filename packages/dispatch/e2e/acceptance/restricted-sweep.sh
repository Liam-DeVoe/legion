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
# Two reads more:
#   direct messages  a direct message the full member reads, to a session that worked in the
#                    project alone, by GET /api/v1/messages/{id} and its /blocks, beside that
#                    session's list of messages, which the route index's row reads;
#   the GitHub proxy every read packages/envoy/internal/dispatch/githubapi/proxy.go forwards
#                    (/api/github/rest/repos/{owner}/{repo}/...), filled with a repository only
#                    the project's issues resolve to, which must answer the member as a repository
#                    the App is not installed on answers, or refuse them 403.
# A GET row of a route that answers an object its caller may not see exactly as a missing one
# (`as_missing_routes`: whether the App is installed on a repository, which the server holds to
# the GitHub proxy's rule), filled with the project's objects, must answer the member exactly what
# it answers them for a missing object, status and text: no refusal of the project's own.
# Then the sockets:
#   /ws/doc/{room}  the member is not admitted to the room of the project's document, while the
#                   same probe admits the member to a document of their own project;
#   /ws/live        the member's event feed, open for the whole of SWEEP_LIVE_SECONDS, is sent no
#                   event of the project while the full member writes a comment on
#                   SWEEP_WRITE_ISSUE, which the full member's own feed, open over the same window,
#                   is sent.
#
# "A row of the project" is a JSON member name or string value (`row_rule`) that is, whole, the
# project's key or a marker: an issue key of the project, an id the lookup found, the title of the
# issue the lookup filled with, or a session or a person of the project, one the lookup read as an
# author in the project that the full member finds in none of the projects the member sees, nor
# is the member. A string a person wrote (a `body`, `title`, `snippet` and the like) counts only
# whole: a member's own comment may cite the project's items, its whole body a link, and the route
# index names example keys. Any other string with no whitespace, a value the server wrote, is one
# too when the key, an issue key or a marker stands in it as a whole part between separators: a
# reference, a path, a URL; a string with whitespace counts only whole. A body that is not JSON is
# read line by line by the same rule, and a socket's refused upgrade is read by the body the
# server wrote. Each answer is read for them once the values the path was filled with are masked,
# since an answer may echo what it was asked, and before it is compared with anything, so an
# answer alike for a missing object excuses no row. Filled paths are also called with objects
# that do not exist: any answer for the project's object (a 2xx, or a refusal other than 403 and
# 404) where a missing one is answered otherwise says the object exists, and is a row of it too,
# whatever the body holds. Every RFC 3339 instant and the durations a read measures are masked
# before two answers are compared. A session, a person, a repository, a priority, a broadcast and
# a delivery run belong to no project, so a row filled only with them is held to the markers
# alone. A line about the full member's feed names its frames' types and ids, never what they
# hold.
#
# Each row also says whether it can fail: a filled row can when the full member is answered the
# project's object otherwise than a missing one, a list when the full member's answer holds a row
# of the project. A row that cannot is called and judged all the same, and says so (`blind`). A
# GitHub proxy read or a row of a route held to a missing object's answer that cannot fail leaves
# the run UNKNOWN instead: its rule compares the member's answer with a missing object's, which
# the full member was answered alike too, so its answer then says nothing either way.
#
# It runs in one of four modes, chosen by SWEEP_MODE and SWEEP_FILLS_FILE:
#   one run      SWEEP_MODE unset, no SWEEP_FILLS_FILE: both halves in one process, the full
#                member's (the lookup, the question and each row's can-fail call) and the
#                member's, with the live comment and its control. The stack test runs this.
#   fills        SWEEP_MODE=fills, where a full member's credential may be held (a developer box):
#                the full member's half alone, written to SWEEP_FILLS_FILE. Exit 0 means the file
#                is whole; any other exit leaves no file. It makes the one write the lookup needs
#                (the question) and no comment.
#   runner       SWEEP_FILLS_FILE set, SWEEP_MODE unset, where no full member's credential may be
#                (a public runner): the member's half alone, each row's can-fail read from the
#                fills. It refuses a full member's credential. The live check reads the member's
#                feed over a window it prints (`live window <W0> <W1>`, UTC) and makes no write:
#                it holds only once a writer's delivered write falls strictly inside that window,
#                which whoever ran both joins.
#   writer       SWEEP_MODE=writer, beside a runner's run: the full member's own feed open, a
#                comment on SWEEP_WRITE_ISSUE every SWEEP_WRITE_EVERY s for SWEEP_WRITE_SECONDS,
#                and one line a comment, `write <UTC> <id> delivered` or `... not delivered`, for
#                whether that feed was sent it. Exit 0 only when every comment was.
#
# The fills file is ASCII, one `name<TAB>value` line a fill, its first line
# `restricted-sweep-fills<TAB>1`, then `project` and `made_at` (Unix seconds). It holds no
# credential: the project's object ids and keys, its repository names, the member's login and own
# projects, the found values that mark a row (ids, the project's own sessions and people, the
# reference prefix, and the target issue's title, which the search rows take as their text), one
# `can_fail<TAB><row>` a row whose full-member half can fail, and one `full_status<TAB><status>
# <row>` a list. Fills mode refuses to write one above 40 KiB, or where a row it can tell cannot
# fail must (a GitHub proxy read, a route held to a missing object's answer). A runner refuses
# fills that are absent, empty, of another version or project, or older than 55 minutes, since
# the question's run is kept an hour and read only by its asker.
#
# Inputs, each refused by name when missing or malformed (the mode each is read in, in brackets):
#   SWEEP_ORIGIN             [all] the deployment's origin, https://<host>[:<port>]; http:// only
#                            to 127.0.0.1, localhost or [::1], for a local stack.
#   SWEEP_PROJECT            [all] the key of the project the member's access list must not name.
#   SWEEP_FULL_TOKEN         [one run, fills, writer] a bearer token of a full member who sees that
#                            project; any bearer Dispatch accepts, a broker-traded one included.
#   SWEEP_RESTRICTED_COOKIE  [one run, runner] the restricted member's session cookie, as the
#                            `Cookie` header carries it (`<name>=<value>`).
#   SWEEP_MEMBER             [fills] the restricted member's login. The member's own projects are
#                            the ones whose access lists name it, which a runner checks against
#                            the projects the member is listed.
#   SWEEP_FULL_COOKIE        [one run, fills] the full member's session cookie, alike. The GitHub
#                            proxy reads as the browser's person and never as a token, so only
#                            with it are the proxy reads made as the full member too and able to
#                            fail; without it they cannot fail, and the run is UNKNOWN, naming
#                            them, and fills mode writes no file.
#   SWEEP_WRITE_ISSUE        [one run, writer] the issue of SWEEP_PROJECT the full member comments
#                            on while the feeds are open. A comment and the question are the
#                            sweep's writes, each in SWEEP_PROJECT and as the full member.
#   SWEEP_CONVERSATION_FILE  [one run, fills] `<session id>/<slug>`: a file of the conversation of
#                            a session that worked in SWEEP_PROJECT alone, which no route lists. The
#                            full member must read it, and the member must see the session
#                            nowhere. Left unset, or failing either, the two rows that read such a
#                            file are not filled, and the run is UNKNOWN.
#   SWEEP_FILLS_FILE         [fills, runner] the fills file to write, or to read.
#   SWEEP_LIVE_SECONDS       [one run, runner] how long the feeds are read (default 60, maximum
#                            2147413, within the socket probe's timer range).
#   SWEEP_WRITE_SECONDS, SWEEP_WRITE_EVERY
#                            [writer] how long the writer writes (default 600) and how often
#                            (default 15): whole seconds.
#   SWEEP_CALL_SECONDS       [all] how long one request waits (default 20).
#   SWEEP_PACE_SECONDS       [all] the pause before each request (default 0.1, so the run stays far
#                            under the edge's per-address request limit); 0 for none.
#   SWEEP_ROUTES, SWEEP_OPENAPI
#                            the route index and the API document to read, by default this
#                            checkout's.
# SWEEP_LIST_KINDS=1 prints, for each GET row of the route index, its path and the kind of object
# each wildcard is filled with (`{key}=issue`), and exits 4, never PASS's 0: its test holds that
# to the server's own route probes. SWEEP_LIST_PROXY=1 prints the GitHub proxy reads alike.
#
# It needs bash 3.2 or later, curl 7.55 or later, jq 1.6 or later, and bun with this checkout's
# dependencies installed (`bun install`), which runs the socket probe
# e2e/acceptance/socket-access.ts. No token or cookie is printed, written to the fills or passed on
# a command line: curl and the probe read them from files of mode 0600 in a directory of the run's
# own, which it removes.
#
# It prints a line per call and a verdict:
#   PASS     exit 0: every GET row filled and called, every route and both sockets kept the
#            project from the member, every kind of object the project owns was found, at least
#            one row could fail, every GitHub proxy read and every row of a route held to a
#            missing object's answer could fail, and the full member's own event feed was sent the
#            comment (a runner: the member's feed was read over the whole live window).
#   FAIL     exit 1: the member was answered a row of the project, admitted to its document's
#            room, or sent an event of it, or a route held to a missing object's answer answered
#            them otherwise for the project's. The verdict names the first route that did.
#   UNKNOWN  exit 2: anything that proved nothing, which is never a pass: an input refused, a
#            tool missing, an identity that is not what it must be, a kind of object the lookup
#            did not find, a row it could not fill, an answer no rule judges (401, 429, a 5xx, no
#            answer), no row that could fail, a GitHub proxy read or a row of a route held to a
#            missing object's answer that could not fail, fills a runner cannot read, or a socket
#            control that did not hold.
#   FILLS    exit 0 (fills mode): the file is whole, and where it is.
#   WRITER   exit 0 when every write was delivered, 1 when one was not (writer mode).
#
# Usage, from packages/dispatch:
#
#   SWEEP_ORIGIN=https://dispatch.example SWEEP_PROJECT=PRIV SWEEP_WRITE_ISSUE=PRIV-12 \
#     SWEEP_CONVERSATION_FILE=<session id>/<slug> SWEEP_FULL_TOKEN=... \
#     SWEEP_RESTRICTED_COOKIE=... bash e2e/acceptance/restricted-sweep.sh
#
#   # The split: fills and the writer where the full member's credential is, the runner apart.
#   SWEEP_MODE=fills SWEEP_ORIGIN=... SWEEP_PROJECT=PRIV SWEEP_MEMBER=<login> SWEEP_FULL_TOKEN=... \
#     SWEEP_CONVERSATION_FILE=<session id>/<slug> SWEEP_FILLS_FILE=fills.tsv bash e2e/acceptance/restricted-sweep.sh
#   SWEEP_MODE=writer SWEEP_ORIGIN=... SWEEP_PROJECT=PRIV SWEEP_WRITE_ISSUE=PRIV-12 SWEEP_FULL_TOKEN=... \
#     bash e2e/acceptance/restricted-sweep.sh &
#   SWEEP_ORIGIN=... SWEEP_PROJECT=PRIV SWEEP_FILLS_FILE=fills.tsv SWEEP_RESTRICTED_COOKIE=... \
#     bash e2e/acceptance/restricted-sweep.sh
#
# Its tests: e2e/__tests__/restricted-sweep-judging.test.ts holds each judging rule alone against
# a fake Dispatch, the modes among them; e2e/__tests__/restricted-sweep.test.ts runs it against the
# local stack (e2e/harness-pool.ts, which starts e2e/run-server.sh) and against a stack of a build
# with no read check, which it must FAIL, each in one run and split into fills and a runner.

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
full_cookie=${SWEEP_FULL_COOKIE:-}
unset SWEEP_FULL_TOKEN SWEEP_RESTRICTED_COOKIE SWEEP_FULL_COOKIE
export -n full_token cookie full_cookie
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

# The GitHub proxy's reads, after /api/github/rest/repos/{owner}/{repo}/, one a line: every path
# packages/envoy/internal/dispatch/githubapi/proxy.go's `readable` forwards, the only GET the
# proxy answers, which routes.txt does not list. Its test holds this list to that expression.
proxy_reads='pulls/{number}
issues/{number}
commits/{sha}/check-runs
actions/runs?head_sha={sha}'

if [ "${SWEEP_LIST_PROXY:-}" = 1 ]; then
  printf '%s\n' "$proxy_reads"
  exit 4
fi

# The GET rows whose wildcards the sweep fills that answer an object the caller may not see exactly
# as a missing one, one a line: whether the App is installed on a repository, which the server
# holds to the GitHub proxy's rule (packages/envoy/internal/dispatch/api/github_app_installation.go,
# refs.RepositoryClause), so a repository only the project's issues resolve to answers the member
# as one the App is not installed on, status and bytes.
as_missing_routes='/api/v1/github/app-installation/{owner}/{repo}'

# answers_as_missing PATTERN: whether the GET row PATTERN is one of as_missing_routes.
answers_as_missing() {
  case $'\n'"$as_missing_routes"$'\n' in *$'\n'"$1"$'\n'*) return 0 ;; esac
  return 1
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
# The mode: fills and writer by name; a fills file read by a runner; else one run.
mode=${SWEEP_MODE:-}
fills_file=${SWEEP_FILLS_FILE:-}
case $mode in
  fills | writer) ;;
  '') if [ -n "$fills_file" ]; then mode=runner; else mode=one; fi ;;
  *) unknown "SWEEP_MODE must be fills or writer, or unset: $mode" ;;
esac
if [ "$mode" = fills ]; then
  [ -n "$fills_file" ] || unknown 'SWEEP_FILLS_FILE is required in fills mode: the file to write the fills to'
  case $fills_file in */) unknown "SWEEP_FILLS_FILE must name a file: $fills_file" ;; esac
fi
[ "$mode" != writer ] || [ -z "$fills_file" ] || unknown 'SWEEP_FILLS_FILE is no input of writer mode'
# The full member's credential: required where the full member's half runs, and refused by name on
# a runner, whose fills already hold that half.
if [ "$mode" = runner ]; then
  [ -z "$full_token" ] && [ -z "$full_cookie" ] ||
    unknown 'a runner holds no full member'"'"'s credential: unset SWEEP_FULL_TOKEN and SWEEP_FULL_COOKIE, whose half SWEEP_FILLS_FILE holds'
else
  [ -n "$full_token" ] ||
    unknown "SWEEP_FULL_TOKEN is required: a full member's bearer token that sees $project, or, where no full member's credential may be, SWEEP_FILLS_FILE: the fills a run of SWEEP_MODE=fills wrote"
  case $full_token in *[!A-Za-z0-9._~+/=-]*) unknown 'SWEEP_FULL_TOKEN holds a byte no bearer token holds' ;; esac
fi
if [ "$mode" = one ] || [ "$mode" = runner ]; then
  [ -n "$cookie" ] || unknown 'SWEEP_RESTRICTED_COOKIE is required: the restricted member'"'"'s session cookie, <name>=<value>'
  holds_line_break "$cookie" && unknown 'SWEEP_RESTRICTED_COOKIE holds a line break'
  case $cookie in *=*) ;; *) unknown 'SWEEP_RESTRICTED_COOKIE must be <name>=<value>, as the Cookie header carries it' ;; esac
  case $cookie in *[!\ -~]*) unknown 'SWEEP_RESTRICTED_COOKIE holds a non-printable header byte' ;; esac
fi
if [ -n "$full_cookie" ]; then
  holds_line_break "$full_cookie" && unknown 'SWEEP_FULL_COOKIE holds a line break'
  case $full_cookie in *=*) ;; *) unknown 'SWEEP_FULL_COOKIE must be <name>=<value>, as the Cookie header carries it' ;; esac
  case $full_cookie in *[!\ -~]*) unknown 'SWEEP_FULL_COOKIE holds a non-printable header byte' ;; esac
fi
# The member's login, which fills mode reads the member's own projects by.
sweep_member=''
if [ "$mode" = fills ]; then
  sweep_member=${SWEEP_MEMBER:-}
  case $sweep_member in
    '') unknown "SWEEP_MEMBER is required in fills mode: the restricted member's login" ;;
    *[!\!-~]* | *@*@* | @* | *@) unknown "SWEEP_MEMBER is no login: $sweep_member" ;;
    ?*@?*) ;;
    *) unknown "SWEEP_MEMBER is no login: $sweep_member" ;;
  esac
fi
write_issue=''
if [ "$mode" = one ] || [ "$mode" = writer ]; then
  write_issue=${SWEEP_WRITE_ISSUE:-}
  case $write_issue in
    '') unknown "SWEEP_WRITE_ISSUE is required: the issue of $project the full member comments on" ;;
    "$project"-[1-9]*) is_count "${write_issue#"$project"-}" || unknown "SWEEP_WRITE_ISSUE is no issue of $project: $write_issue" ;;
    *) unknown "SWEEP_WRITE_ISSUE is no issue of $project: $write_issue" ;;
  esac
fi
# A file of a conversation: the session id, a "/", and the file's slug, neither holding a "/".
conversation_file=''
conversation_session='' conversation_slug=''
conversation_why='SWEEP_CONVERSATION_FILE names no file of a conversation, and no route lists them'
if [ "$mode" = one ] || [ "$mode" = fills ]; then
  conversation_file=${SWEEP_CONVERSATION_FILE:-}
  case $conversation_file in
    '') ;;
    */*/* | /* | */ | *[!A-Za-z0-9._/-]*)
      unknown "SWEEP_CONVERSATION_FILE must be <session id>/<slug>: $conversation_file" ;;
    */*) conversation_session=${conversation_file%/*} conversation_slug=${conversation_file#*/} ;;
    *) unknown "SWEEP_CONVERSATION_FILE must be <session id>/<slug>: $conversation_file" ;;
  esac
fi
live_seconds=${SWEEP_LIVE_SECONDS:-60}
is_count "$live_seconds" && [ "$live_seconds" -gt 0 ] && [ "$live_seconds" -le 2147413 ] ||
  unknown "SWEEP_LIVE_SECONDS must be a whole number of seconds, at least 1 and at most 2147413: $live_seconds"
# The probe accepts canonical decimal seconds, and its timers must fit a signed 32-bit millisecond delay.
while [ "${live_seconds#0}" != "$live_seconds" ]; do live_seconds=${live_seconds#0}; done
write_seconds=${SWEEP_WRITE_SECONDS:-600}
write_every=${SWEEP_WRITE_EVERY:-15}
if [ "$mode" = writer ]; then
  is_count "$write_seconds" && [ "$write_seconds" -gt 0 ] && [ "$write_seconds" -le 2147000 ] ||
    unknown "SWEEP_WRITE_SECONDS must be a whole number of seconds, at least 1: $write_seconds"
  is_count "$write_every" && [ "$write_every" -gt 0 ] ||
    unknown "SWEEP_WRITE_EVERY must be a whole number of seconds, at least 1: $write_every"
  while [ "${write_seconds#0}" != "$write_seconds" ]; do write_seconds=${write_seconds#0}; done
  while [ "${write_every#0}" != "$write_every" ]; do write_every=${write_every#0}; done
fi
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
if [ "$mode" != runner ]; then
  printf 'Authorization: Bearer %s\n' "$full_token" >"$work/full.h" || unknown 'could not write the full member header'
fi
if [ "$mode" = one ] || [ "$mode" = runner ]; then
  printf 'Cookie: %s\n' "$cookie" >"$work/restricted.h" || unknown 'could not write the restricted member header'
fi
# The full member as the browser's person, for the GitHub proxy, which reads no token.
full_browser=full
if [ -n "$full_cookie" ]; then
  printf 'Cookie: %s\n' "$full_cookie" >"$work/full-browser.h" || unknown 'could not write the full member cookie header'
  full_browser=full-browser
fi
unset full_token cookie full_cookie
: >"$work/failed"
: >"$work/unknown"
: >"$work/values"
: >"$work/can-fail"
: >"$work/full-status"
# Fills mode leaves no file but a whole one: an older file at the path goes before the first request.
if [ "$mode" = fills ]; then
  [ ! -d "$fills_file" ] || unknown "SWEEP_FILLS_FILE names a directory: $fills_file"
  rm -f -- "$fills_file" || unknown "could not remove the older SWEEP_FILLS_FILE: $fills_file"
fi

# What the lookup finds, or a runner reads from the fills.
issue='' issue_title='' doc='' doc_slug='' block='' pdoc='' pdoc_slug='' comment='' ask=''
message='' message_issue='' handoff='' handoff_issue='' handoff_phase='' near_copy=''
session='' person='' repo='' repo_owner='' repo_name='' priority='' broadcast='' delivery_run=''
going_run='' ref_prefix='' going_why='' dm_session='' dm_message='' proxy_repo='' scanned=0

# The fills a runner reads: every one before the first request, so fills it cannot read stop the
# run before it reaches the deployment.
fills_own='' fills_member=''
if [ "$mode" = runner ]; then
  [ -f "$fills_file" ] && [ -r "$fills_file" ] ||
    unknown "SWEEP_FILLS_FILE names no file this run can read: $fills_file"
  [ -s "$fills_file" ] || unknown "SWEEP_FILLS_FILE is empty: $fills_file"
  IFS= read -r first_fill <"$fills_file" || unknown "SWEEP_FILLS_FILE holds no line: $fills_file"
  [ "$first_fill" = "restricted-sweep-fills	1" ] ||
    unknown "SWEEP_FILLS_FILE holds no fills of this sweep's version 1: its first line is $(printf '%s' "$first_fill" | cut -c1-60)"
  : >"$work/fills-own"
  fills_project='' made_at=''
  while IFS='	' read -r name value || [ -n "${name:-}" ]; do
    case $name in
      restricted-sweep-fills) ;;
      project) fills_project=$value ;;
      made_at) made_at=$value ;;
      member) fills_member=$value ;;
      own) printf '%s\n' "$value" >>"$work/fills-own" ;;
      value) printf '%s\n' "$value" >>"$work/values" ;;
      can_fail) printf '%s\n' "$value" >>"$work/can-fail" ;;
      full_status) printf '%s\n' "$value" >>"$work/full-status" ;;
      issue) issue=$value ;;
      title) issue_title=$value ;;
      doc) doc=$value ;;
      doc_slug) doc_slug=$value ;;
      block) block=$value ;;
      pdoc) pdoc=$value ;;
      pdoc_slug) pdoc_slug=$value ;;
      comment) comment=$value ;;
      ask) ask=$value ;;
      message) message=$value ;;
      message_issue) message_issue=$value ;;
      handoff) handoff=$value ;;
      handoff_issue) handoff_issue=$value ;;
      handoff_phase) handoff_phase=$value ;;
      near_copy) near_copy=$value ;;
      session) session=$value ;;
      person) person=$value ;;
      repo) repo=$value ;;
      priority) priority=$value ;;
      broadcast) broadcast=$value ;;
      delivery_run) delivery_run=$value ;;
      going_run) going_run=$value ;;
      ref_prefix) ref_prefix=$value ;;
      dm_session) dm_session=$value ;;
      dm_message) dm_message=$value ;;
      proxy_repo) proxy_repo=$value ;;
      conversation_session) conversation_session=$value ;;
      conversation_slug) conversation_slug=$value ;;
      *) unknown "SWEEP_FILLS_FILE holds a fill this sweep does not read: $(printf '%s' "$name" | cut -c1-60)" ;;
    esac
  done <"$fills_file"
  [ "$fills_project" = "$project" ] ||
    unknown "SWEEP_FILLS_FILE holds the fills of project ${fills_project:-none}, not of $project"
  is_count "$made_at" || unknown "SWEEP_FILLS_FILE says no time it was made: ${made_at:-none}"
  fills_age=$(($(date -u +%s) - made_at))
  [ "$fills_age" -ge -300 ] || unknown "SWEEP_FILLS_FILE was made $((-fills_age)) s in the future"
  [ "$fills_age" -le 3300 ] ||
    unknown "SWEEP_FILLS_FILE was made $((fills_age / 60)) minutes ago, past 55: its question's run is kept an hour, read only by its asker"
  [ -n "$fills_member" ] || unknown 'SWEEP_FILLS_FILE names no member'
  [ -z "$repo" ] || { repo_owner=${repo%%/*} && repo_name=${repo#*/}; }
  fills_own=$(sort -u "$work/fills-own")
fi

calls=0
# call WHO PATH OUT [JSON FILE]: GET PATH as WHO (full, restricted, or full-browser, the full
# member's cookie), or with JSON FILE a POST of that file, the body in OUT and the status in
# $status, or `curl failed: ...` when no answer came. A cookie goes with the origin's Origin, as the
# dashboard sends it; curl reads no ~/.curlrc (-q), no proxy, and no URL pattern (-g).
call() {
  local -a credential data
  data=()
  case $1 in
    full) credential=(-H "@$work/full.h") ;;
    *) credential=(-H "@$work/$1.h" -H "Origin: $origin") ;;
  esac
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
  for header in full restricted full-browser; do
    [ -r "$work/$header.h" ] || continue
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

# frames_of FILE: the type of each frame a socket probe printed, and its id where it has one, or
# the first two words of a line that is no frame (`closed 1011`, `http 404`). A line about the
# full member's feed quotes this and never the frames, which hold what the project holds.
frames_of() {
  jq -R -r -s 'split("\n") | map(select(length > 0)
      | (fromjson? // null) as $frame
      | if ($frame | type) == "object"
        then ($frame.type // "untyped" | tostring)
          + (if $frame.id == null then "" else " " + ($frame.id | tostring) end)
        else split(" ") | .[0:2] | join(" ") end)
    | if length == 0 then "nothing" else join(", ") end' "$1" 2>/dev/null || printf 'nothing it could read\n'
}

printf 'restricted-sweep: %s, project %s, %s mode, %s\n' "$origin" "$project" "$mode" "$(date -u +%Y-%m-%dT%H:%M:%SZ)"

# Writer mode: the full member's own feed open, a comment on SWEEP_WRITE_ISSUE every
# SWEEP_WRITE_EVERY s for SWEEP_WRITE_SECONDS, and a line for each as soon as whether that feed was
# sent it is known, so a writer stopped early has printed every write but the one in flight. A
# write's time is read once the server answered it, so it was committed by then.
if [ "$mode" = writer ]; then
  call full /api/v1/whoami "$work/body"
  [ "$status" = 200 ] || unknown "GET /api/v1/whoami with SWEEP_FULL_TOKEN answered $status: $(head_of "$work/body")"
  bun "$socket_probe" watch "$origin" "$work/full.h" "$work/ready-full" "$work/written" \
    "$((write_seconds + write_every + 15))" >"$work/live-full" 2>&1 &
  watcher_full=$!
  watchers=$watcher_full
  ready_started=$SECONDS
  until [ -e "$work/ready-full" ] || ! kill -0 "$watcher_full" 2>/dev/null; do
    [ "$((SECONDS - ready_started))" -lt 30 ] || break
    sleep 0.2
  done
  if [ ! -e "$work/ready-full" ] || ! kill -0 "$watcher_full" 2>/dev/null; then
    unknown "the full member's feed did not subscribe in 30 s: it was sent $(frames_of "$work/live-full")"
  fi
  : >"$work/written" || unknown 'could not signal the socket observation window'
  printf 'writer  a comment on %s every %s s for %s s, each held to the full member'"'"'s own feed\n' \
    "$write_issue" "$write_every" "$write_seconds"
  writes=0 missed=0 wait_each=$((write_every > 5 ? write_every : 5))
  write_started=$SECONDS
  while [ "$((SECONDS - write_started))" -lt "$write_seconds" ]; do
    next=$((SECONDS + write_every))
    jq -cn --arg b "restricted-sweep: a probe of who is sent this comment live, written $(date -u +%Y-%m-%dT%H:%M:%SZ)." \
      '{body: $b, actor: {kind: "session", id: "restricted-sweep"}}' >"$work/comment.json" ||
      unknown 'could not write the live comment request'
    call full "/api/v1/issues/$write_issue/comments" "$work/body" "$work/comment.json"
    stamp=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    id=''
    [ "$status" != 201 ] || id=$(first_of "$work/body" '.id | select(type == "string" and length > 0)')
    writes=$((writes + 1))
    if [ -z "$id" ]; then
      printf 'write %s - not written: %s %s\n' "$stamp" "$status" "$(head_of "$work/body" 120)"
      missed=$((missed + 1))
    else
      outcome='not delivered'
      until_at=$((SECONDS + wait_each))
      while :; do
        if grep -F -q -e "$id" "$work/live-full"; then outcome=delivered; break; fi
        if [ "$SECONDS" -ge "$until_at" ] || ! kill -0 "$watcher_full" 2>/dev/null; then break; fi
        sleep 0.2
      done
      printf 'write %s %s %s\n' "$stamp" "$id" "$outcome"
      [ "$outcome" = delivered ] || missed=$((missed + 1))
    fi
    if ! kill -0 "$watcher_full" 2>/dev/null; then
      printf 'writer  the full member'"'"'s feed ended: it was sent %s\n' "$(frames_of "$work/live-full")"
      missed=$((missed + 1))
      break
    fi
    while [ "$SECONDS" -lt "$next" ] && [ "$((SECONDS - write_started))" -lt "$write_seconds" ]; do sleep 0.2; done
  done
  kill "$watcher_full" 2>/dev/null
  wait "$watcher_full" 2>/dev/null
  watchers=''
  if [ "$missed" -gt 0 ] || [ "$writes" -eq 0 ]; then
    printf 'WRITER: %s writes, %s not delivered to the full member'"'"'s own feed\n' "$writes" "$missed"
    exit 1
  fi
  printf 'WRITER: %s writes, each delivered to the full member'"'"'s own feed\n' "$writes"
  exit 0
fi

# put NAME VALUE: one fill, a line of the fills file fills mode writes. A value no line can hold
# whole and plain (a tab, a line break, a byte outside printable ASCII) stops the run, as no fill
# then says what it was.
put() {
  case $2 in *[!\ -~]*) unknown "the fill $1 holds a byte the fills file cannot: $(printf '%s' "$2" | cut -c1-60)" ;; esac
  printf '%s\t%s\n' "$1" "$2" >>"$work/fills" || unknown 'could not write a fill'
}

# The identities. The member must be a restricted member the server signs in (fills mode, which
# holds no member's cookie, takes SWEEP_MEMBER); the full member's token must see the project,
# whose access list must not name the member. A runner's fills checked the full member's half.
if [ "$mode" = fills ]; then
  member=$sweep_member
else
  call restricted /api/v1/whoami "$work/body"
  [ "$status" = 200 ] || unknown "GET /api/v1/whoami as the member answered $status: $(head_of "$work/body")"
  member=$(first_of "$work/body" 'select(.kind == "user" and .member_kind == "restricted") | .login')
  [ -n "$member" ] || unknown "the cookie signs in no restricted member: GET /api/v1/whoami answered $(head_of "$work/body")"
fi
if [ "$mode" = runner ]; then
  [ "$(printf '%s' "$member" | tr '[:upper:]' '[:lower:]')" = "$(printf '%s' "$fills_member" | tr '[:upper:]' '[:lower:]')" ] ||
    unknown "the cookie signs in $member, and SWEEP_FILLS_FILE's fills are of $fills_member"
else
  call full /api/v1/whoami "$work/body"
  [ "$status" = 200 ] || unknown "GET /api/v1/whoami with SWEEP_FULL_TOKEN answered $status: $(head_of "$work/body")"
  call full /api/v1/projects "$work/body"
  [ "$status" = 200 ] || unknown "GET /api/v1/projects with SWEEP_FULL_TOKEN answered $status: $(head_of "$work/body")"
  [ -n "$(first_of "$work/body" ".[] | select(.key == \"$project\") | .key")" ] ||
    unknown "SWEEP_FULL_TOKEN does not see $project: GET /api/v1/projects lists no such project"
  jq -r --arg p "$project" '.[]? | .key | select(type == "string" and . != $p)' "$work/body" >"$work/full-projects" 2>/dev/null
  call full "/api/v1/projects/$project/access" "$work/body"
  [ "$status" = 200 ] || unknown "GET /api/v1/projects/$project/access with SWEEP_FULL_TOKEN answered $status: $(head_of "$work/body")"
  [ -z "$(jq -r --arg m "$member" '.people[]? | select(ascii_downcase == ($m | ascii_downcase))' "$work/body")" ] ||
    unknown "$project's access list names $member, so the member may read it and this sweep proves nothing"
fi
printf 'member  %s, a restricted member %s'"'"'s access list does not name\n' "$member" "$project"

# found LABEL VALUE: a project object the lookup found, which marks a row of the project.
found() {
  printf 'found   %s %s\n' "$1" "$2"
  printf '%s\n' "$2" >>"$work/values"
}
: >"$work/missing"
: >"$work/own"
if [ "$mode" = runner ]; then
  # The member's own projects, which the document socket's control reads too, must be the ones the
  # fills were made for: the fills told the project's sessions, people and repositories from those
  # of the member's own projects by them. A project the member sees that the fills did not know
  # leaves the run UNKNOWN; the project itself among them is judged by the routes.
  call restricted /api/v1/projects "$work/body"
  [ "$status" = 200 ] || unknown "GET /api/v1/projects as the member answered $status: $(head_of "$work/body")"
  jq -r --arg p "$project" '.[]? | .key | select(type == "string" and . != $p)' "$work/body" >"$work/own" 2>/dev/null ||
    unknown "GET /api/v1/projects as the member answered no list: $(head_of "$work/body")"
  member_own=$(sort -u "$work/own")
  [ "$member_own" = "$fills_own" ] ||
    unproven "the member is listed the projects $(printf '%s' "$member_own" | tr '\n' ' '), but SWEEP_FILLS_FILE was made for $(printf '%s' "$fills_own" | tr '\n' ' ')"
  printf 'fills   of %s for %s, made %s s ago: %s values, %s rows that can fail\n' "$project" "$member" \
    "$fills_age" "$(wc -l <"$work/values" | tr -d ' ')" "$(wc -l <"$work/can-fail" | tr -d ' ')"
else
  # The lookup, as the full member: an object of each kind the routes name. A kind the project
  # owns and the lookup did not find is written to `missing`, which leaves the run UNKNOWN.
  # A page of the project's issues: given a limit, the list answers {issues: [...]}.
  call full "/api/v1/issues?project=$project&limit=50" "$work/body"
  [ "$status" = 200 ] || unknown "GET /api/v1/issues?project=$project with SWEEP_FULL_TOKEN answered $status: $(head_of "$work/body")"
  jq -c 'if type == "object" then .issues else . end' "$work/body" >"$work/issues" 2>/dev/null
  jq -r '.[]? | .key' "$work/issues" 2>/dev/null >"$work/keys"
  [ -s "$work/keys" ] || unknown "the full member finds no issue of $project, so no route can be filled with its objects"
  : >"$work/actors"
  : >"$work/sessions"
  # actors_in FILE KINDS: every actor of one of KINDS (a jq array) FILE's objects name, one per line.
  actors_in() {
    jq -r --argjson kinds "$2" \
      '.. | objects | select((.kind as $k | any($kinds[]; . == $k)) and (.id | type == "string")) | .id' \
      "$1" 2>/dev/null
  }
  # authors FILE FILTER: the first person and the first session that FILTER's authors name, and
  # every session and person FILE names, into `actors`, its sessions also into `sessions`.
  authors() {
    [ -n "$person" ] || person=$(first_of "$1" "$2 | select(.kind == \"user\") | .id")
    [ -n "$session" ] || session=$(first_of "$1" "$2 | select(.kind == \"session\") | .id")
    actors_in "$1" '["user", "session"]' >>"$work/actors"
    actors_in "$1" '["session"]' >>"$work/sessions"
  }
  # Every issue of the page, until an object of each kind is found: the page holds 50.
  while read -r key; do
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
  # The member's own projects, which the document socket's control reads too. Fills mode, with no
  # member's cookie, reads them as the projects whose access lists name the member, which is all a
  # restricted member is listed; a runner checks them against the projects it is listed.
  if [ "$mode" = fills ]; then
    while read -r other; do
      call full "/api/v1/projects/$(uri "$other")/access" "$work/body"
      if [ "$status" != 200 ]; then
        printf 'own projects: the full member is answered %s for the access list of %s, so the sweep cannot tell whether it names %s\n' \
          "$status" "$other" "$member" >>"$work/missing"
        continue
      fi
      [ -z "$(jq -r --arg m "$member" '.people[]? | select(ascii_downcase == ($m | ascii_downcase))' "$work/body")" ] ||
        printf '%s\n' "$other" >>"$work/own"
    done <"$work/full-projects"
  else
    call restricted /api/v1/projects "$work/body"
    [ "$status" = 200 ] || unknown "GET /api/v1/projects as the member answered $status: $(head_of "$work/body")"
    jq -r --arg p "$project" '.[]? | .key | select(type == "string" and . != $p)' "$work/body" >"$work/own" 2>/dev/null ||
      unknown "GET /api/v1/projects as the member answered no list: $(head_of "$work/body")"
  fi
  # The sessions and people the member may see: their own login, and every session and person the
  # full member finds in a project the member sees, by kind or as a login anywhere in its answers.
  # A session or a person the lookup read in the project that is none of these is a row of the
  # project: a list of sessions or people that names one gave the member what the project holds.
  printf '%s\n' "$member" >"$work/seen-actors" || unknown 'could not record the member'
  # seen_in FILE: every session and person FILE names by kind, and every login it holds.
  seen_in() {
    jq -r '(.. | objects | select((.kind == "user" or .kind == "session") and (.id | type == "string")) | .id),
      (.. | strings | select(test("^[^@[:space:]]+@[^@[:space:]]+$")))' "$1" 2>/dev/null
  }
  : >"$work/own-repos"
  while read -r own; do
    call full "/api/v1/projects/$own/access" "$work/body"
    [ "$status" != 200 ] || seen_in "$work/body" >>"$work/seen-actors"
    call full "/api/v1/issues?project=$own&limit=50" "$work/own-issues"
    if [ "$status" != 200 ]; then
      printf 'actor: the full member is answered %s for the issues of %s, a project the member sees, so the sweep cannot tell its sessions, people and repositories from %s'"'"'s\n' \
        "$status" "$own" "$project" >>"$work/missing"
      continue
    fi
    seen_in "$work/own-issues" >>"$work/seen-actors"
    jq -r 'if type == "object" then .issues else . end | .[]? | (.repo, .resolved_repo) | select(type == "string")' \
      "$work/own-issues" >>"$work/own-repos" 2>/dev/null
    jq -r 'if type == "object" then .issues else . end | .[]? | .key' "$work/own-issues" >"$work/own-keys" 2>/dev/null
    while read -r key; do
      for part in '' /comments /asks /messages /handoffs; do
        call full "/api/v1/issues/$key$part" "$work/body"
        [ "$status" != 200 ] || seen_in "$work/body" >>"$work/seen-actors"
      done
    done <"$work/own-keys"
  done <"$work/own"
  sort -u "$work/actors" | grep -v -x -F -f "$work/seen-actors" >"$work/project-actors"
  if [ -s "$work/project-actors" ]; then
    while read -r actor; do found actor "$actor"; done <"$work/project-actors"
  else
    printf 'missing actor: every session and person the lookup read in %s also works in a project the member sees\n' "$project"
    printf 'actor: every session and person the lookup read in %s also works in a project the member sees, so no list of sessions or people can fail\n' \
      "$project" >>"$work/missing"
  fi
  # A direct message to a session that worked in the project alone, which the full member reads:
  # the member must read it by no route, its session's list of messages included.
  sort -u "$work/sessions" | grep -x -F -f "$work/project-actors" >"$work/project-sessions"
  while read -r candidate; do
    call full "/api/v1/agents/$(uri "$candidate")/messages" "$work/body"
    [ "$status" = 200 ] || continue
    jq -r '.[]? | .message.id | select(type == "string")' "$work/body" >"$work/direct-messages" 2>/dev/null
    [ -s "$work/direct-messages" ] || continue
    dm_session=$candidate dm_message=$(head -n 1 "$work/direct-messages")
    while read -r id; do found direct-message "$id"; done <"$work/direct-messages"
    break
  done <"$work/project-sessions"
  if [ -z "$dm_message" ]; then
    printf 'missing direct-message: no session the lookup read in %s alone has a direct message the full member reads\n' "$project"
    printf 'direct-message: no session the lookup read in %s alone has a direct message the full member reads\n' \
      "$project" >>"$work/missing"
  fi
  # A repository only the project's issues resolve to, which the GitHub proxy's reads are filled
  # with.
  proxy_repo=$(jq -r '.[]? | (.repo, .resolved_repo) | select(type == "string" and test("^[^/]+/[^/]+$"))' "$work/issues" 2>/dev/null |
    grep -v -x -F -f "$work/own-repos" -e '' | head -n 1)
  if [ -n "$proxy_repo" ]; then
    found proxy-repository "$proxy_repo"
  else
    printf 'missing proxy-repository: every repository %s'"'"'s issues resolve to is one a project the member sees resolves to as well\n' "$project"
    printf 'proxy-repository: every repository %s'"'"'s issues resolve to is one a project the member sees resolves to as well\n' \
      "$project" >>"$work/missing"
  fi
  # The conversation file must be one the full member reads, of a session the member does not see;
  # any other leaves its two rows unfilled, with the reason.
  if [ -n "$conversation_session" ]; then
    if grep -q -x -F -e "$conversation_session" "$work/seen-actors"; then
      conversation_why="SWEEP_CONVERSATION_FILE's session $conversation_session works in a project the member sees"
      conversation_session='' conversation_slug=''
    else
      call full "/api/v1/agents/$(uri "$conversation_session")/artifacts/$(uri "$conversation_slug")" "$work/body"
      if [ "$status" = 200 ]; then
        found conversation-file "$conversation_file"
      else
        conversation_why="the full member is answered $status for SWEEP_CONVERSATION_FILE's $conversation_file"
        conversation_session='' conversation_slug=''
      fi
    fi
  fi
  [ -z "$issue_title" ] || [ "${#issue_title}" -lt 8 ] || printf '%s\n' "$issue_title" >>"$work/values"
fi
# write_markers: the markers file marker_in reads: the project's key, the found values (`values`,
# one a line) by value, each found value that holds a separator, with its regex spelling, for the
# rule that reads a value inside a reference, and the member names whose strings a person writes.
write_markers() {
  jq -Rn --arg key "$project" '
    [inputs | select(length > 0)] as $found
    | {key: $key,
       issue: ("^" + $key + "-[1-9][0-9]*$"),
       separator: "[/:#?&=]",
       values: (($found | map({(.): true}) | add) // {}),
       multi: [$found[] | select(test("[/:#?&=]") and (test("\\s") | not))
         | {value: ., re: gsub("(?<c>[.*+?^${}()|\\[\\]\\\\])"; "\\\(.c)")}],
       text: (["body", "title", "snippet", "text", "markdown", "content", "question", "description",
         "words", "summary", "name"] | map({(.): true}) | add)}' \
    "$work/values" >"$work/markers.json" || unknown 'jq could not write the markers'
}
write_markers

# The one rule that says whether a string an answer holds is a row of the project, as a jq
# function of the string, the markers file and whether a person wrote the string. A string is
# one when it is, whole, the project's key or a value the lookup found (an issue key, an id, the
# issue's title, a session of the project alone). A string a person wrote - the value of a member
# such as `body`, `title` or `snippet` (`text` in the markers file) - counts no key and no issue
# key, even whole: a member's own comment may cite the project's items by any form, its whole
# body or its first line one key, and the route index's own text names example keys of any
# project; it counts only a found value, whole. Any other string with no whitespace in it is a
# value the server wrote - a key, an id, a reference, a path or a URL - and it is one too when the
# key, an issue key of the project or a found value stands in it as a whole part between
# separators (`/ : # ? & =`) or its ends: `KEY-12/spec`, a reference prefix and `KEY-12`,
# `/issues/KEY-12`, `/projects/KEY`, a dashboard URL, `session:<id>`. A member this rule does not
# name as a person's is read as the server's, so a new text member fails closed.
# shellcheck disable=SC2016 # the `$` names in it are jq's variables, never the shell's
readonly row_rule='
  def row($m; $written):
    . as $s
    | if $written and ($s == $m.key or ($s | test($m.issue))) then empty
      elif $s == $m.key or $m.values[$s] != null then $s
      elif $written or ($s | test("\\s")) then empty
      else first(
          ($s | splits($m.separator)
            | select(. == $m.key or $m.values[.] != null
                or (startswith($m.key + "-") and test($m.issue)))),
          ($m.multi[] | . as $v
            | select($s | test("(^|" + $m.separator + ")" + $v.re + "(" + $m.separator + "|$)"))
            | $v.value))
      end;'

# marker_in FILE prints the first string FILE holds that `row_rule` names a row of the project.
# A JSON body (a stream is read in full; false and null are JSON too) is read as each string value,
# with the member name it stands under, and each member name. A body jq cannot read as JSON is read
# as each of its lines and each double-quoted string in them, as the server's, by the same rule. An
# answer no jq reads is recorded as unproven, never as clean. One jq a body: the sweep reads
# thousands.
marker_in() {
  jq -r -s --slurpfile m "$work/markers.json" "$row_rule"'
    $m[0] as $m
    | first(
        (paths(type == "string") as $p
          | ([$p[] | strings] | last // "") as $member
          | getpath($p) | row($m; $m.text[$member] != null)),
        (.. | objects | keys[] | row($m; false))) // empty' "$1" 2>/dev/null &&
    return
  jq -r -R -s --slurpfile m "$work/markers.json" "$row_rule"'
    $m[0] as $m
    | first(splits("\n") | select(length > 0)
        | (., (split("\"") as $parts | $parts | keys[] | select(. % 2 == 1) | $parts[.]))
        | row($m; false)) // empty' "$1" 2>/dev/null ||
    printf 'an answer the markers could not be read in: %s\n' "$(head_of "$1" 120)" >>"$work/unknown"
}

# masked FILE VALUE... masks literal fills, their URL and JSON encodings, every RFC 3339 instant
# and the durations a read measures, so two answers read at two moments for two fills compare
# alike when they say the same: a read's own `as_of`, `computed_at` and window bounds differ at
# every read, and a row's own times come with its ids, which the markers read. A masked JSON
# answer stays JSON, the duration's mask a string, so the markers read it as JSON.
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
    | gsub("\"(?<key>took_ms|age_seconds|oldest_lacking_seconds)\"[[:space:]]*:[[:space:]]*[0-9.]+";
        "\"\(.key)\":\"<n>\"")' --args "$@"
}

# refusal_body FILE OUT: the answer a socket probe printed, without the `http <status> ` its
# first line starts with when the upgrade was refused, so the markers read the server's body as
# the server wrote it.
refusal_body() {
  sed '1s/^http [0-9][0-9][0-9] //' "$1" >"$2" || unknown 'could not read a socket answer'
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

# fills_can LABEL: on a runner, whether the fills say the row LABEL can fail.
fills_can() {
  if grep -q -x -F -e "$1" "$work/can-fail"; then echo 1; else echo 0; fi
}
# filled LABEL CAN: in fills mode, the row's full-member half, kept for a runner and printed.
filled() {
  [ "$2" = 0 ] || put can_fail "$1"
  printf 'fill    %s (%s)\n' "$1" "$can_text"
}

# judge LABEL TARGET MISSING STRICT TARGET_FILLS MISSING_FILLS: the row LABEL, called as TARGET
# (filled with the project's objects) and, where an answer needs it, as MISSING (filled with
# objects that do not exist). The fills are newline-separated lists of the values each side was
# filled with. An answer is read for rows with its own side's fills masked; two answers are
# compared with both sides' fills masked in each, since an answer may hold a fill's text that is
# no echo of it - a member's own comment that names the project's key - and that text must read
# alike on both sides. STRICT is 1 when an object of the project fills the row, which then must
# answer the member as a missing one, or refuse.
judge() {
  local label=$1 target=$2 missing=$3 strict=$4 t_fills=$5 m_fills=$6 rt rm ft fm can=0 marker line mt mm
  local -a t_list m_list both_list
  t_list=() m_list=()
  while IFS= read -r line; do [ -z "$line" ] || t_list+=("$line"); done <<EOF
$t_fills
EOF
  while IFS= read -r line; do [ -z "$line" ] || m_list+=("$line"); done <<EOF
$m_fills
EOF
  both_list=(${t_list[@]+"${t_list[@]}"} ${m_list[@]+"${m_list[@]}"})
  # Whether the row can fail: the full member is answered the project's object otherwise than a
  # missing one, or a row of it beyond what the request echoes. A failed missing-object request
  # establishes nothing. A runner reads it from the fills.
  if [ "$mode" = runner ]; then
    can=$(fills_can "$label")
  else
    call full "$target" "$work/ft"
    ft=$status
    if is_2xx "$ft" && [ "$strict" = 1 ]; then
      call full "$missing" "$work/fm"
      fm=$status
      mt=$(masked "$work/ft" ${both_list[@]+"${both_list[@]}"}) || unknown 'could not mask the full member answer'
      mm=$(masked "$work/fm" ${both_list[@]+"${both_list[@]}"}) || unknown 'could not mask the missing-object control'
      if is_judgeable "$fm" && { [ "$fm" != "$ft" ] || [ "$mt" != "$mm" ]; }; then
        can=1
      fi
    fi
    if [ "$can" = 0 ] && is_judgeable "$ft" && ! is_refusal "$ft" &&
      [ -n "$(masked_marker "$work/ft" ${t_list[@]+"${t_list[@]}"})" ]; then
      can=1
    fi
  fi
  counted "$can"
  if [ "$mode" = fills ]; then
    filled "$label" "$can"
    return
  fi
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
  mt=$(masked "$work/rt" ${both_list[@]+"${both_list[@]}"}) || unknown 'could not mask the member answer'
  call restricted "$missing" "$work/rm"
  rm=$status
  mm=$(masked "$work/rm" ${both_list[@]+"${both_list[@]}"}) || unknown 'could not mask the missing-object answer'
  if [ "$rm" = "$rt" ] && [ "$mt" = "$mm" ]; then
    printf 'ok      %s: %s, as a missing one is answered (%s)\n' "$label" "$rt" "$can_text"
  elif is_2xx "$rt" && [ "$strict" = 0 ]; then
    printf 'ok      %s: %s, no row of %s (%s)\n' "$label" "$rt" "$project" "$can_text"
  elif is_2xx "$rt" && is_refusal "$rm"; then
    # The object exists for the member where a missing one does not, an empty answer included.
    fail "$label: the member is answered $rt for $project's object, where a missing one is refused $rm: $(head_of "$work/rt")"
  elif [ "$strict" = 1 ] && is_judgeable "$rm"; then
    # Any answer of its own for the project's object, a refusal such as 400 ARTIFACT_LINKED
    # included, where a missing one is answered otherwise, says the object exists.
    fail "$label: the member is answered $rt for $project's object, otherwise than a missing one answered $rm: $(head_of "$work/rt")"
  else
    unproven "$label: the member is answered $rt, where a missing one is answered $rm: $(head_of "$work/rt")"
  fi
}

# judge_list LABEL PATH: a row whose path names no object, called bare. Its answer to the member
# may hold the member's own rows, never one of the project; a refusal of any other 4xx is
# judged alike for both callers. Fills mode keeps the full member's status for a runner.
judge_list() {
  local label=$1 path=$2 rt ft='' can=0 marker
  if [ "$mode" = runner ]; then
    can=$(fills_can "$label")
    ft=$(awk -v l="$label" 'index($0, " ") && substr($0, index($0, " ") + 1) == l { print substr($0, 1, index($0, " ") - 1); exit }' \
      "$work/full-status")
  else
    call full "$path" "$work/ft"
    ft=$status
    if is_2xx "$ft" && [ -n "$(marker_in "$work/ft")" ]; then can=1; fi
  fi
  counted "$can"
  if [ "$mode" = fills ]; then
    case $ft in *' '*) ft=${ft%% *} ;; esac
    put full_status "$ft $label"
    filled "$label" "$can"
    return
  fi
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

# judge_as_missing LABEL TARGET MISSING TARGET_FILLS MISSING_FILLS: a row of a route that answers
# an object its caller may not see exactly as a missing one (`as_missing_routes`), called as
# TARGET (filled with the project's objects) and as MISSING (filled with objects that do not
# exist). The member must be answered for TARGET exactly what they are for MISSING, status and
# text with both sides' fills masked: any other answer, a refusal of TARGET alone among them, tells
# them the project's object is there. The row can fail only where the full member is answered
# TARGET otherwise than MISSING (the App installed on the project's repository, and on no missing
# one): a server that answered every caller as for a missing object would otherwise pass too, so a
# row that cannot fail leaves the run UNKNOWN, never a pass (fills mode: no file). The answer is
# read for rows of the project first, as every row's is. A runner reads whether it can fail from
# the fills.
judge_as_missing() {
  local label=$1 target=$2 missing=$3 t_fills=$4 m_fills=$5 line ft fm rt rm mt mm can=0 why marker
  local -a t_list both_list
  t_list=() both_list=()
  while IFS= read -r line; do [ -z "$line" ] || t_list+=("$line"); done <<EOF
$t_fills
EOF
  while IFS= read -r line; do [ -z "$line" ] || both_list+=("$line"); done <<EOF
$t_fills
$m_fills
EOF
  if [ "$mode" = runner ]; then
    can=$(fills_can "$label")
    why="SWEEP_FILLS_FILE holds no full member's read of it that could fail"
  else
    call full "$target" "$work/ft"
    ft=$status
    call full "$missing" "$work/fm"
    fm=$status
    if ! is_judgeable "$ft" || ! is_judgeable "$fm"; then
      why="the full member is answered $ft for $project's object and $fm for a missing one, which no rule judges"
    else
      mt=$(masked "$work/ft" ${both_list[@]+"${both_list[@]}"}) || unknown 'could not mask the full member answer'
      mm=$(masked "$work/fm" ${both_list[@]+"${both_list[@]}"}) || unknown 'could not mask the missing-object control'
      if [ "$ft" != "$fm" ] || [ "$mt" != "$mm" ]; then
        can=1
      else
        why="the full member is answered $project's object as a missing one is ($ft), so the row could not fail"
      fi
    fi
  fi
  counted "$can"
  if [ "$mode" = fills ]; then
    if [ "$can" = 1 ]; then filled "$label" "$can"; else unproven "$label: $why"; fi
    return
  fi
  call restricted "$target" "$work/rt"
  rt=$status
  call restricted "$missing" "$work/rm"
  rm=$status
  if ! is_judgeable "$rt" || ! is_judgeable "$rm"; then
    unproven "$label: the member is answered $rt, and $rm for a missing one, which no rule judges: $(head_of "$work/rt")"
    return
  fi
  marker=$(masked_marker "$work/rt" ${t_list[@]+"${t_list[@]}"})
  if [ -n "$marker" ]; then
    fail "$label: the member is answered $rt holding $marker, a row of $project: $(head_of "$work/rt")"
    return
  fi
  mt=$(masked "$work/rt" ${both_list[@]+"${both_list[@]}"}) || unknown 'could not mask the member answer'
  mm=$(masked "$work/rm" ${both_list[@]+"${both_list[@]}"}) || unknown 'could not mask the missing-object answer'
  if [ "$rt" != "$rm" ] || [ "$mt" != "$mm" ]; then
    fail "$label: the member is answered $rt $(head_of "$work/rt") for $project's object, where a missing one is answered $rm $(head_of "$work/rm")"
  elif [ "$can" = 0 ]; then
    unproven "$label: the member is answered $rt, as a missing one is, but $why"
  else
    printf 'ok      %s: %s, as a missing one is answered (%s)\n' "$label" "$rt" "$can_text"
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
        # The session of the project alone the direct message is to, whose ids mark its rows.
        /api/v1/agents/\{session_id\}/messages)
          fill_owned=0 fill_target=$dm_session
          fill_why="no session the lookup read in $project alone has a direct message the full member reads"
          ;;
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
  if answers_as_missing "$pattern"; then
    judge_as_missing "GET $pattern" "$target" "$missing" "$t_fills" "$m_fills"
  else
    judge "GET $pattern" "$target" "$missing" "$strict" "$t_fills" "$m_fills"
  fi
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

# The direct message, by the routes that read one message: a message of the project's session.
if [ -n "$dm_message" ]; then
  for suffix in '' /blocks; do
    judge "GET /api/v1/messages/{id}$suffix (a direct message)" "/api/v1/messages/$(uri "$dm_message")$suffix" \
      "/api/v1/messages/$none_uuid$suffix" 1 "$dm_message" "$none_uuid"
  done
fi

# judge_proxy LABEL TARGET MISSING NAME: a GitHub proxy read of the project's repository (TARGET),
# beside the same read of one the App is not installed on (MISSING, the repository's name in
# NAME's place). The member must be answered as that missing one is, its name masked, or refused
# 403; any other answer told the member what the App reads of the project's repository. A read
# the full member is answered as that missing one is cannot fail, and leaves the run UNKNOWN (fills
# mode: no file): the proxy reads the browser's person alone, so without SWEEP_FULL_COOKIE the full
# member's read is answered as no one's.
judge_proxy() {
  local label=$1 target=$2 missing=$3 name=$4 rt rm ft fm can=0 mt mm why
  local cookie_why="the proxy reads the browser's person alone, whom SWEEP_FULL_COOKIE names"
  if [ "$mode" = runner ]; then
    can=$(fills_can "$label")
    why="SWEEP_FILLS_FILE holds no full member's read of it that could fail: fills mode writes one only where the full member's read can fail, with SWEEP_FULL_COOKIE, whose person the proxy reads"
  else
    # As the full member's browser where SWEEP_FULL_COOKIE names it: the proxy reads no token.
    call "$full_browser" "$target" "$work/ft"
    ft=$status
    call "$full_browser" "$missing" "$work/fm"
    fm=$status
    if is_judgeable "$ft" || [ "${ft#5}" != "$ft" ]; then
      mt=$(masked "$work/ft" "$name" no-such-repo) || unknown 'could not mask a proxy answer'
      mm=$(masked "$work/fm" "$name" no-such-repo) || unknown 'could not mask a proxy answer'
      [ "$ft" = "$fm" ] && [ "$mt" = "$mm" ] || can=1
      why="the full member is answered $ft for $project's repository as for one the App is not installed on ($fm), so the read could not fail: $cookie_why"
    else
      why="the full member is answered $ft for $project's repository, which no rule judges, so the read could not fail: $cookie_why"
    fi
  fi
  counted "$can"
  if [ "$mode" = fills ]; then
    if [ "$can" = 1 ]; then filled "$label" "$can"; else unproven "$label: $why"; fi
    return
  fi
  call restricted "$target" "$work/rt"
  rt=$status
  call restricted "$missing" "$work/rm"
  rm=$status
  case "$rt $rm" in
    *curl* | 401* | 429* | *' 401' | *' 429')
      unproven "$label: the member is answered $rt, and $rm for a repository the App is not installed on, which no rule judges: $(head_of "$work/rt")"
      return
      ;;
  esac
  mt=$(masked "$work/rt" "$name" no-such-repo) || unknown 'could not mask a proxy answer'
  mm=$(masked "$work/rm" "$name" no-such-repo) || unknown 'could not mask a proxy answer'
  if { [ "$rt" = "$rm" ] && [ "$mt" = "$mm" ]; } || { [ "$rt" = 403 ] && [ -z "$(masked_marker "$work/rt" "$name")" ]; }; then
    if [ "$can" = 0 ]; then
      unproven "$label: the member is answered $rt, but $why"
    elif [ "$rt" = "$rm" ] && [ "$mt" = "$mm" ]; then
      printf 'ok      %s: %s, as a repository the App is not installed on (%s)\n' "$label" "$rt" "$can_text"
    else
      printf 'ok      %s: 403 (%s)\n' "$label" "$can_text"
    fi
  else
    fail "$label: the member is answered $rt for $project's repository, where one the App is not installed on is answered $rm: $(head_of "$work/rt")"
  fi
}

# The GitHub proxy, each read filled with the repository only the project's issues resolve to.
proxy_sha=0123456789abcdef0123456789abcdef01234567
if [ -n "$proxy_repo" ]; then
  proxy_owner=${proxy_repo%%/*} proxy_name=${proxy_repo#*/}
  while IFS= read -r shape; do
    path=$shape
    path=${path//\{number\}/1}
    path=${path//\{sha\}/$proxy_sha}
    judge_proxy "GET /api/github/rest/repos/{owner}/{repo}/$shape" \
      "/api/github/rest/repos/$(uri "$proxy_owner")/$(uri "$proxy_name")/$path" \
      "/api/github/rest/repos/$(uri "$proxy_owner")/no-such-repo/$path" "$proxy_name"
  done <<EOF
$proxy_reads
EOF
fi

# Fills mode ends here, with the full member's half of every row: the fills, whole, in
# SWEEP_FILLS_FILE, or no file and the reasons. The sockets are the member's alone, a runner's.
if [ "$mode" = fills ]; then
  printf 'swept   %s GET rows: %s rows, %s can fail, %s blind, %s not filled; %s requests in %s s\n' \
    "$rows" "$judged" "$live" "$blind" "$unfilled" "$calls" "$((SECONDS - started))"
  [ "$live" -gt 0 ] || printf 'no row could fail: the full member is answered no row of %s anywhere\n' "$project" >>"$work/unknown"
  [ "$judged" -gt 0 ] || printf 'no route was judged\n' >>"$work/unknown"
  if [ -s "$work/missing" ] || [ -s "$work/unknown" ]; then
    sed 's/^/  /' "$work/missing" "$work/unknown"
    printf 'UNKNOWN: no fills were written: the full member'"'"'s half of this run did not prove the %s reasons above\n' \
      "$(cat "$work/missing" "$work/unknown" | wc -l | tr -d ' ')"
    exit 2
  fi
  mv "$work/fills" "$work/fills-rows" 2>/dev/null || : >"$work/fills-rows"
  : >"$work/fills"
  put restricted-sweep-fills 1
  put project "$project"
  put made_at "$(date -u +%s)"
  put member "$member"
  sort -u "$work/own" >"$work/own-sorted"
  while read -r own; do [ -z "$own" ] || put own "$own"; done <"$work/own-sorted"
  for name in issue doc doc_slug block pdoc pdoc_slug comment ask message message_issue handoff \
    handoff_issue handoff_phase near_copy session person repo priority broadcast delivery_run going_run \
    ref_prefix dm_session dm_message proxy_repo conversation_session conversation_slug; do
    eval "fill_value=\${$name}"
    [ -z "$fill_value" ] || put "$name" "$fill_value"
  done
  [ -z "$issue_title" ] || put title "$issue_title"
  sort -u "$work/values" >"$work/values-sorted"
  while IFS= read -r value; do [ -z "$value" ] || put value "$value"; done <"$work/values-sorted"
  cat "$work/fills-rows" >>"$work/fills" || unknown 'could not gather the fills'
  fills_bytes=$(wc -c <"$work/fills" | tr -d ' ')
  [ "$fills_bytes" -le 40960 ] ||
    unknown "the fills hold $fills_bytes bytes, above the 40 KiB one workflow input can carry; no fills were written"
  partial="$fills_file.partial.$$"
  if ! cp "$work/fills" "$partial" || ! mv -f "$partial" "$fills_file"; then
    rm -f "$partial"
    unknown "could not write SWEEP_FILLS_FILE: $fills_file"
  fi
  printf 'FILLS: %s fills of %s, %s bytes, %s rows that can fail, written to %s; a runner may read them for 55 minutes\n' \
    "$(wc -l <"$work/fills" | tr -d ' ')" "$project" "$fills_bytes" "$live" "$fills_file"
  exit 0
fi

# The document socket. The member's cookie is the one credential it admits; the control is the
# same probe admitting the member to a document of a project the member sees (`own`, read with
# the lookup).
call restricted /api/v1/schema/blocks "$work/body"
schema_version=$(first_of "$work/body" '.version // .schema_version | tostring')
own_doc=''
while read -r own; do
  call restricted "/api/v1/projects/$own/artifacts" "$work/body"
  [ "$status" != 200 ] || own_doc=$(first_of "$work/body" '.[]? | .id')
  [ -z "$own_doc" ] || break
done <"$work/own"
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
        refusal_body "$work/doc-target" "$work/doc-body"
        masked "$work/doc-body" "$doc" >"$work/doc-refusal" || unknown 'could not mask the document refusal'
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

# The live socket. One run: both feeds open, the full member comments on SWEEP_WRITE_ISSUE, both
# are read for the window, and the full member's feed must be sent the comment, or the member's
# silence proves nothing. A runner holds no full member's credential: it reads the member's feed
# alone and prints the window it read (`live window <W0> <W1>`, UTC, whole seconds), and its
# silence is proven only by a writer's write delivered strictly inside that window. W1 leaves a
# margin before the window's end, for a write's delivery.
utc_of() { date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -r "$1" +%Y-%m-%dT%H:%M:%SZ; }
live_result='' written='' window_line='' watcher_full=''
bun "$socket_probe" watch "$origin" "$work/restricted.h" "$work/ready-restricted" "$work/written" \
  "$live_seconds" >"$work/live-restricted" 2>&1 &
watcher_restricted=$!
watchers=$watcher_restricted
if [ "$mode" = one ]; then
  bun "$socket_probe" watch "$origin" "$work/full.h" "$work/ready-full" "$work/written" \
    "$live_seconds" >"$work/live-full" 2>&1 &
  watcher_full=$!
  watchers="$watcher_restricted $watcher_full"
fi
# alive PID: whether a socket probe of this run is still running; no probe is none.
alive() { [ -n "$1" ] && kill -0 "$1" 2>/dev/null; }
ready_started=$SECONDS
until { [ -e "$work/ready-restricted" ] || ! alive "$watcher_restricted"; } &&
  { [ "$mode" != one ] || [ -e "$work/ready-full" ] || ! alive "$watcher_full"; }; do
  [ "$((SECONDS - ready_started))" -lt 30 ] || break
  sleep 0.2
done
if [ "$mode" = one ] && { [ ! -e "$work/ready-full" ] || ! alive "$watcher_full"; }; then
  live_result="the full member's feed did not stay subscribed: it was sent $(frames_of "$work/live-full")"
elif [ ! -e "$work/ready-restricted" ] && alive "$watcher_restricted"; then
  live_result="the member's feed did not start in 30 s"
elif [ -e "$work/ready-restricted" ] && ! alive "$watcher_restricted"; then
  live_result="the member's feed ended before the observation window started"
elif [ ! -e "$work/ready-restricted" ] && ! grep -E -q '^http (403|404) ' "$work/live-restricted"; then
  live_result="the member's probe ended without subscribing or refusing the upgrade: $(head_of "$work/live-restricted")"
elif [ "$mode" = one ]; then
  # A failed write ends the check immediately instead of waiting out a potentially long window.
  jq -cn --arg b "restricted-sweep: a probe of who is sent this comment live, written $(date -u +%Y-%m-%dT%H:%M:%SZ)." \
    '{body: $b, actor: {kind: "session", id: "restricted-sweep"}}' >"$work/comment.json" ||
    unknown 'could not write the live comment request'
  call full "/api/v1/issues/$write_issue/comments" "$work/body" "$work/comment.json"
  if [ "$status" = 201 ]; then
    written=$(first_of "$work/body" '.id | select(type == "string" and length > 0)')
  fi
  [ "$status" = 201 ] && [ -n "$written" ] ||
    live_result="the full member's comment on $write_issue answered $status: $(head_of "$work/body")"
fi
observation_started=$SECONDS
window_start=$(date -u +%s)
if [ -z "$live_result" ]; then
  : >"$work/written" || unknown 'could not signal the socket observation window'
  while alive "$watcher_restricted" || alive "$watcher_full"; do
    elapsed=$((SECONDS - observation_started))
    if [ "$elapsed" -lt "$live_seconds" ] &&
      { { [ "$mode" = one ] && ! alive "$watcher_full"; } ||
        { [ -e "$work/ready-restricted" ] && ! alive "$watcher_restricted"; }; }; then
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
full_exit=0
if [ -n "$watcher_full" ]; then
  wait "$watcher_full"
  full_exit=$?
fi
watchers=''
# The probe prints `closed <code> <reason>` only for a feed that closed before its window ended,
# which the whole-second clock above can miss by a second: a subscribed feed that printed it did
# not cover the window.
if [ -z "$live_result" ]; then
  for feed in restricted full; do
    if [ -e "$work/ready-$feed" ] && grep -q '^closed ' "$work/live-$feed"; then
      live_result='a subscribed feed ended before the observation window finished'
    fi
  done
fi
[ -z "$written" ] || printf '%s\n' "$written" >>"$work/values"
write_markers
# A refused upgrade is judged by the body the server wrote, without the probe's status prefix.
refused_upgrade=''
case $(head -n 1 "$work/live-restricted") in 'http '[0-9][0-9][0-9]' '*) refused_upgrade=1 ;; esac
refusal_body "$work/live-restricted" "$work/live-body"
marker=$(marker_in "$work/live-body")
if [ -n "$marker" ] && [ -n "$refused_upgrade" ]; then
  fail "/ws/live: the member's refused upgrade holds $marker, a row of $project: $(head_of "$work/live-restricted")"
elif [ -n "$marker" ]; then
  fail "/ws/live: the member is sent an event holding $marker, of $project: $(head_of "$work/live-restricted")"
elif [ -n "$live_result" ]; then
  unproven "/ws/live: $live_result"
elif [ "$mode" = one ] && { [ "$full_exit" != 0 ] || ! jq -s -e --arg c "$written" '
  length > 0 and .[0].type == "events_start"
  and all(.[]; type == "object" and (.type | type == "string"))
  and any(.[]; .type == "event" and any(.. | strings; . == $c))' "$work/live-full" >/dev/null 2>&1; }; then
  unproven "/ws/live: the control did not hold: the full member's feed did not cover $live_seconds s and receive comment $written: it was sent $(frames_of "$work/live-full")"
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
        if [ "$mode" = one ]; then
          printf 'ok      /ws/live: the member is sent %s events in %s s, none of %s; the full member is sent the comment (control)\n' \
            "$frames" "$live_seconds" "$project"
        else
          margin=$((live_seconds >= 20 ? 5 : 1))
          window_line="$(utc_of "$window_start") $(utc_of "$((window_start + live_seconds - margin))")"
          printf 'ok      /ws/live: the member is sent %s events in %s s, none of %s; its control is a writer'"'"'s write delivered inside the live window\n' \
            "$frames" "$live_seconds" "$project"
          printf 'live window %s\n' "$window_line"
        fi
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
  # The verdict is the last line, as a reader of the log's end takes it.
  sed 's/^/  /' "$work/missing" "$work/unknown"
  printf 'UNKNOWN: nothing reached the member, but this run did not prove it, for the %s reasons above\n' \
    "$(cat "$work/missing" "$work/unknown" | wc -l | tr -d ' ')"
  exit 2
fi
verdict="PASS: no route and neither socket gave the member a row of $project"
if [ "$mode" = runner ] && [ -n "$window_line" ]; then
  verdict="$verdict; the live check holds once a writer's delivered write falls inside the live window $window_line, which the tailnet leg joins"
fi
printf '%s\n' "$verdict"
exit 0
