# Notes for the cache fills

The derivations and the history of `.github/workflows/ci-caches.yaml` that this file holds. A
`# why:` line of that workflow that names this file names the section below that holds the
derivation or the record its job or step rests on; its other `# why:` lines name sections of
`.github/ci-notes/ci.md`. A value's own arithmetic stays beside the value.

## What the refill-superseded job deletes, and why it is a job of its own

The entries of go-daemon, go-notices, go-envoy, go-envoy-test and go-envoy-darwin-vet that the
fills replaced, deleted once each fill has saved its new one. Each fill saves a new entry, over a
gigabyte for most families, so without this the entries would pile up: GitHub drops an entry only
after seven days unread, and over the repository's cache limit it evicts the least recently used
entries, which can be another family's live one. For each family and architecture this keeps one
entry of this ref, the one the fill just saved, and deletes every other one of that family, runner
and Go: those whose key is the readers' key followed by `-`, and the one under the readers' key
itself, which the fills saved before they named the commit and which would otherwise win every
reader's restore as an exact match. The family then holds what it held before this change, one
entry per runner, in a repository near its cache limit. A job that is downloading a deleted entry
at that moment gets a cache miss: actions/cache/restore warns and the job goes on, compiling what
the entry would have given it, slower but not failed. An entry of another runner or another Go is
no such entry and stays. None is deleted unless the new key is among this ref's entries, so a save
that did not land leaves the old entries in place.

A job of its own, so the token that may delete caches is in no job that builds code. A cache key is
text any build that saves on this ref chose, so the step never splits or prints one raw: the list
stays JSON, jq selects the numeric ids to delete, and each id is checked to be a number before
`gh cache delete` gets it. One leg per family and architecture, each keeping the key that fill's
leg saved, under the output name the fill gives it; a leg whose fill saved nothing (it failed
before its save) skips its steps, and the job runs when any fill failed, so one failed fill does
not stop the others' deletes. Every leg runs on ubuntu-24.04-arm: gh and jq need nothing of an x64
machine.
