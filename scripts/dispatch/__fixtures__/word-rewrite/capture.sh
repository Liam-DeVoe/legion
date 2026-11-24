#!/usr/bin/env bash
# Captures the fixtures the word rewrite's tests read (scripts/dispatch/__tests__/word-rewrite-*),
# each with the request that answers it, from a local Dispatch that
# scripts/dispatch/word-rewrite/local-seed.ts seeded (scripts/dispatch/README.md, "Word rewrite").
# Run it once on a freshly seeded Dispatch: its writes (an edit, two title changes, one document)
# change what a second run would read.
#
#   BASE=http://127.0.0.1:<port> bash capture.sh <the seed's --out file>
#
# LEGION_DAEMON_URL names a real daemon, for daemon-state-contract.json, and DISPATCH_URL the
# production Dispatch, read-only, for dispatch-healthz.json: the local Dispatch names no commit,
# which dispatch-healthz-local.json holds. Each is a GET with no credential.
set -euo pipefail

: "${BASE:?BASE names the local Dispatch}"
: "${LEGION_DAEMON_URL:?LEGION_DAEMON_URL names a real daemon, for daemon-state-contract.json}"
: "${DISPATCH_URL:?DISPATCH_URL names the production Dispatch, for dispatch-healthz.json}"
seed=${1:?usage: BASE=<url> capture.sh <seed output file>}
dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
root=$(cd "$dir/../../../.." && pwd)
token=${E2E_AGENT_TOKEN:-e2e-token}
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
: >"$scratch/captured"

value() { jq -r "$1" "$seed"; }
# The header goes to curl from a file descriptor (`-H @<(auth)`), never as an argument a process
# list shows.
auth() { printf 'Authorization: Bearer %s' "$token"; }
actor=$(jq -cn --arg id "$(value .session)" '{kind: "session", id: $id}')

# Writes the file $2 (an answer read whole) to the fixture $1.
keep() {
  mv "$2" "$dir/$1"
  printf '%s\n' "$1" >>"$scratch/captured"
}

# The fixture $1 from a GET of $2 as the session; a refused read stops the capture and writes
# nothing.
fetch() {
  curl -sS --fail-with-body -H @<(auth) -o "$scratch/answer" "$BASE$2"
  keep "$1" "$scratch/answer"
}

# The fixture $1 from a GET of the address $2 sent no credential, cut to the jq filter $3.
fetch_open() {
  curl -sS --fail-with-body -o "$scratch/whole" "$2"
  jq "$3" "$scratch/whole" >"$scratch/answer"
  keep "$1" "$scratch/answer"
}

# The fixture $1 from a request whose answer is the fixture whatever its status, which must be
# $2; the rest are curl's arguments.
expect() {
  local status
  status=$(curl -sS -o "$scratch/answer" -w '%{http_code}' "${@:3}")
  if [ "$status" != "$2" ]; then
    echo "capture: $1 answered $status, not $2" >&2
    cat "$scratch/answer" >&2
    exit 1
  fi
  keep "$1" "$scratch/answer"
}

main=$(value .issues.main)
mixed=$(value .documents.mixed)
capture=$(value .issues.capture)
capture_document=$(value .documents.capture)
closed=$(value .issues.closed)

# The reads, before any write of this script, so the event log holds the seed's last step alone.
fetch event-log.json "/api/v1/events/log?after=$(value .log_after)&limit=50"
fetch issues-open.json "/api/v1/issues?project=SEED&open=true"
fetch issues-all.json "/api/v1/issues?project=SEED"
fetch issue-artifacts.json "/api/v1/issues/$main/artifacts"
fetch issue-artifacts-approved.json "/api/v1/issues/$(value .issues.approved)/artifacts"
fetch issue-artifacts-earlier.json "/api/v1/issues/$(value .issues.earlier)/artifacts"
fetch issue-artifacts-requested.json "/api/v1/issues/$(value .issues.requested)/artifacts"
fetch issue-details.json "/api/v1/issues/$(value .issues.gated)"
fetch text-blocks.json "/api/v1/artifacts/$mixed/text?blocks=true"
fetch text-blocks-verification.json "/api/v1/artifacts/$(value .documents.verification)/text?blocks=true"
# mixed.md's versions as Dispatch lists them: a session's upload and a person's edit. No seed step
# gives a version a `system` author or `authors: null`, so no fixture holds one.
rm -f "$dir"/version-blocks-*.json
for n in $(jq --arg id "$mixed" '.[] | select(.id == $id) | .versions[].number' "$dir/issue-artifacts.json"); do
  fetch "version-blocks-$n.json" "/api/v1/artifacts/$mixed/versions/$n?blocks=true"
done
fetch issue-events.json "/api/v1/issues/$main/events?limit=200"
fetch whoami-shared.json /api/v1/whoami
printf 'Authorization: Bearer %s' "$(value .personal_token)" |
  curl -sS --fail-with-body -H @- -o "$scratch/answer" "$BASE/api/v1/whoami"
keep whoami-personal.json "$scratch/answer"
fetch legion-daemon-set.json /api/v1/projects/SEED/legion/daemon
fetch legion-daemon-null.json /api/v1/projects/NOADDR/legion/daemon
fetch_open daemon-state-no-contract.json "$(value .stubs.no_contract)/legion/v1/state" .
fetch_open daemon-state-contract.json "$LEGION_DAEMON_URL/legion/v1/state" '{contract}'
fetch_open dispatch-healthz.json "$DISPATCH_URL/healthz" '{commit}'
fetch_open dispatch-healthz-local.json "$BASE/healthz" '{commit}'

# The writes, each on an item that holds no trace of the word.
doc_token=$(curl -sS --fail-with-body -H @<(auth) "$BASE/api/v1/artifacts/$capture_document/text" | jq -r .token)
edit=$(jq -cn --argjson actor "$actor" --arg token "$doc_token" \
  '{actor: $actor, ops: [{op: "replace", find: "Capture text.", with: "Captured text."}],
    precondition: {document: $token}, summary: "Capture of the edit fixtures."}')
expect edit-ok.json 200 -X POST -H @<(auth) -H 'Content-Type: application/json' \
  -d "$edit" "$BASE/api/v1/artifacts/$capture_document/edits"
expect edit-precondition-failed.json 409 -X POST -H @<(auth) -H 'Content-Type: application/json' \
  -d "$edit" "$BASE/api/v1/artifacts/$capture_document/edits"

sequence=$(curl -sS --fail-with-body -H @<(auth) "$BASE/api/v1/issues/$capture" | jq .last_seq)
title() { jq -cn --argjson actor "$actor" --arg title "$1" --argjson seq "$2" \
  '{actor: $actor, title: $title, expected_sequence: $seq}'; }
expect title-ok.json 200 -X PATCH -H @<(auth) -H 'Content-Type: application/json' \
  -d "$(title "Capture, retitled" "$sequence")" "$BASE/api/v1/issues/$capture"
expect title-issue-changed.json 409 -X PATCH -H @<(auth) -H 'Content-Type: application/json' \
  -d "$(title "Capture, retitled again" "$sequence")" "$BASE/api/v1/issues/$capture"
closed_sequence=$(curl -sS --fail-with-body -H @<(auth) "$BASE/api/v1/issues/$closed" | jq .last_seq)
expect title-issue-closed.json 409 -X PATCH -H @<(auth) -H 'Content-Type: application/json' \
  -d "$(title "Closed, retitled" "$closed_sequence")" "$BASE/api/v1/issues/$closed"

# state-document.json: no write-mode run comes before this capture (the proof run makes the
# state), so the nearest real answer is Dispatch's text of a state the tool's own serializer made,
# uploaded as write mode uploads its state (a project document with no summary), under a slug of
# its own so that no run of the tool reads it.
SEED_FILE=$seed bun --eval "
  import { readFileSync } from 'node:fs';
  import { emptyState, serializeState } from '$root/scripts/dispatch/word-rewrite/state.ts';
  import { verdictKey } from '$root/scripts/dispatch/word-rewrite/verdicts.ts';
  const seed = JSON.parse(readFileSync(process.env.SEED_FILE, 'utf8'));
  const key = (id, index) => verdictKey(id, 'The paragraph of the capture.', index);
  const state = {
    ...emptyState(),
    cursor: seed.log_after,
    writeCursor: seed.log_after,
    whole: true,
    finished: ['SEED'],
    verdicts: new Map([[key(seed.documents.quotes, 0), 'q'], [key(seed.documents.kernel, 0), 'j']]),
    held: [{ project: 'SEED', issue: seed.issues.main, id: seed.documents.race, keys: [key(seed.documents.race, 0)], retry: true }],
    waiting: [{ project: 'SEED', issue: seed.issues.main, id: seed.documents.notice, gate: 'c' + seed.contract }],
    refused: [{ project: 'SEED', issue: seed.issues.main, id: seed.documents.kernel, key: key(seed.documents.kernel, 0) }],
    sessions: [seed.session],
  };
  process.stdout.write(serializeState(state));
" >"$scratch/state.md"
upload=$(jq -cn --argjson actor "$actor" --rawfile content "$scratch/state.md" \
  '{actor: $actor, name: "capture-state.md", content: $content}')
curl -sS --fail-with-body -o /dev/null -H @<(auth) -H 'Content-Type: application/json' \
  -d "$upload" "$BASE/api/v1/projects/SEED/artifacts"
fetch state-document.json /api/v1/projects/SEED/artifacts/capture-state-md/text

(cd "$root" && bunx biome format --write scripts/dispatch/__fixtures__/word-rewrite >/dev/null)
sed 's/^/captured /' "$scratch/captured"
