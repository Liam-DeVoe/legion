#!/usr/bin/env bash
# Captures the fixtures the word rewrite's tests read (scripts/dispatch/__tests__/word-rewrite-*),
# each with the request that answers it, from a local Dispatch that
# scripts/dispatch/word-rewrite/local-seed.ts seeded (scripts/dispatch/README.md, "Word rewrite").
#
#   BASE=http://127.0.0.1:<port> bash capture.sh <the seed's --out file>
#
# Optional: LEGION_DAEMON_URL (a real daemon, for daemon-state-contract.json), DISPATCH_URL (the
# production Dispatch, read-only, for dispatch-healthz.json). The state document fixture needs a
# write run that kept a state first. Every file it writes replaces the hand-written one of that
# name, and its line leaves HAND-WRITTEN.md, which is deleted once it lists none.
set -euo pipefail

: "${BASE:?BASE names the local Dispatch}"
seed=${1:?usage: BASE=<url> capture.sh <seed output file>}
dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
token=${E2E_AGENT_TOKEN:-e2e-token}
captured=()

value() { jq -r "$1" "$seed"; }
# The header goes to curl from a file descriptor (`-H @<(auth)`), never as an argument a process
# list shows.
auth() { printf 'Authorization: Bearer %s' "$token"; }
actor=$(jq -cn --arg id "$(value .session)" '{kind: "session", id: $id}')

# A GET as the session; the file is written only from a 2xx answer.
get() { curl -sS --fail-with-body -H @<(auth) "$BASE$1"; }

# Writes standard input to the fixture $1.
keep() {
  cat >"$dir/$1"
  captured+=("$1")
}

# A request whose answer is the fixture whatever its status, which must be $2.
expect() {
  local status body
  body=$(mktemp)
  status=$(curl -sS -o "$body" -w '%{http_code}' "${@:3}")
  if [ "$status" != "$2" ]; then
    echo "capture: $1 answered $status, not $2" >&2
    cat "$body" >&2
    rm -f "$body"
    exit 1
  fi
  keep "$1" <"$body"
  rm -f "$body"
}

main=$(value .issues.main)
mixed=$(value .documents.mixed)
capture=$(value .issues.capture)
capture_document=$(value .documents.capture)
closed=$(value .issues.closed)

get "/api/v1/issues?project=SEED&open=true" | keep issues-open.json
get "/api/v1/issues?project=SEED" | keep issues-all.json
get "/api/v1/issues/$main/artifacts" | keep issue-artifacts.json
get "/api/v1/issues/$(value .issues.approved)/artifacts" | keep issue-artifacts-approved.json
get "/api/v1/issues/$(value .issues.earlier)/artifacts" | keep issue-artifacts-earlier.json
get "/api/v1/issues/$(value .issues.requested)/artifacts" | keep issue-artifacts-requested.json
get "/api/v1/issues/$(value .issues.gated)" | keep issue-details.json
get "/api/v1/artifacts/$mixed/text?blocks=true" | keep text-blocks.json
for n in $(jq --arg id "$mixed" '.[] | select(.id == $id) | .versions[].number' "$dir/issue-artifacts.json"); do
  get "/api/v1/artifacts/$mixed/versions/$n?blocks=true" | keep "version-blocks-$n.json"
done
get "/api/v1/issues/$main/events?limit=200" | keep issue-events.json

doc_token=$(get "/api/v1/artifacts/$capture_document/text" | jq -r .token)
edit=$(jq -cn --argjson actor "$actor" --arg token "$doc_token" \
  '{actor: $actor, ops: [{op: "replace", find: "Capture text.", with: "Captured text."}],
    precondition: {document: $token}, summary: "Capture of the edit fixtures."}')
expect edit-ok.json 200 -X POST -H @<(auth) -H 'Content-Type: application/json' \
  -d "$edit" "$BASE/api/v1/artifacts/$capture_document/edits"
expect edit-precondition-failed.json 409 -X POST -H @<(auth) -H 'Content-Type: application/json' \
  -d "$edit" "$BASE/api/v1/artifacts/$capture_document/edits"

sequence=$(get "/api/v1/issues/$capture" | jq .last_seq)
title() { jq -cn --argjson actor "$actor" --arg title "$1" --argjson seq "$2" \
  '{actor: $actor, title: $title, expected_sequence: $seq}'; }
expect title-ok.json 200 -X PATCH -H @<(auth) -H 'Content-Type: application/json' \
  -d "$(title "Capture, retitled" "$sequence")" "$BASE/api/v1/issues/$capture"
expect title-issue-changed.json 409 -X PATCH -H @<(auth) -H 'Content-Type: application/json' \
  -d "$(title "Capture, retitled again" "$sequence")" "$BASE/api/v1/issues/$capture"
closed_sequence=$(get "/api/v1/issues/$closed" | jq .last_seq)
expect title-issue-closed.json 409 -X PATCH -H @<(auth) -H 'Content-Type: application/json' \
  -d "$(title "Closed, retitled" "$closed_sequence")" "$BASE/api/v1/issues/$closed"

get /api/v1/whoami | keep whoami-shared.json
printf 'Authorization: Bearer %s' "$(value .personal_token)" | curl -sS --fail-with-body -H @- "$BASE/api/v1/whoami" |
  keep whoami-personal.json
get /api/v1/projects/SEED/legion/daemon | keep legion-daemon-set.json
get /api/v1/projects/NOADDR/legion/daemon | keep legion-daemon-null.json
get "/api/v1/events/log?after=$(value .log_after)&limit=50" | keep event-log.json
curl -sS --fail-with-body "$(value .stubs.no_contract)/legion/v1/state" |
  keep daemon-state-no-contract.json

if state=$(get /api/v1/projects/SEED/artifacts/rewrite-state-md/text 2>/dev/null); then
  printf '%s\n' "$state" | keep state-document.json
else
  echo "capture: no state document yet; run write mode with --state-document first" >&2
fi
if [ -n "${LEGION_DAEMON_URL:-}" ]; then
  curl -sS --fail-with-body "$LEGION_DAEMON_URL/legion/v1/state" | jq '{contract}' |
    keep daemon-state-contract.json
fi
if [ -n "${DISPATCH_URL:-}" ]; then
  curl -sS --fail-with-body "$DISPATCH_URL/healthz" | jq '{commit}' | keep dispatch-healthz.json
fi

(cd "$dir/../../../.." && bunx biome format --write scripts/dispatch/__fixtures__/word-rewrite >/dev/null)
listed="$dir/HAND-WRITTEN.md"
if [ -f "$listed" ]; then
  for file in "${captured[@]}"; do
    grep -v -F -- "- \`$file\`" "$listed" >"$listed.new" || true
    mv "$listed.new" "$listed"
  done
  if ! grep -q '^- ' "$listed"; then rm -f "$listed"; fi
fi
printf 'captured %s\n' "${captured[@]}"
