# Fixtures written by hand

Each file below was written by hand from `packages/contracts/schemas/dispatch-openapi.json` and the Go handlers, with the texts `scripts/dispatch/word-rewrite/local-seed.ts` seeds, until `capture.sh` captures it from a local Dispatch.
`capture.sh` takes each file's line out of this list as it replaces the file, and deletes this list once it names none.

- `daemon-state-contract.json`: a real daemon's `{contract}` (needs `LEGION_DAEMON_URL`)
- `daemon-state-no-contract.json`: the seed's stub daemon with no `contract` key
- `dispatch-healthz.json`: production Dispatch's `{commit}` (needs `DISPATCH_URL`)
- `edit-ok.json`
- `edit-precondition-failed.json`
- `event-log.json`
- `issue-artifacts.json`
- `issue-artifacts-approved.json`
- `issue-artifacts-earlier.json`
- `issue-artifacts-requested.json`
- `issue-details.json`
- `issue-events.json`
- `issues-all.json`
- `issues-open.json`
- `legion-daemon-null.json`
- `legion-daemon-set.json`
- `state-document.json`: needs a write run that kept a state first
- `text-blocks.json`
- `title-issue-changed.json`
- `title-issue-closed.json`
- `title-ok.json`
- `version-blocks-1.json`
- `version-blocks-2.json`
- `version-blocks-3.json`
- `whoami-personal.json`
- `whoami-shared.json`
