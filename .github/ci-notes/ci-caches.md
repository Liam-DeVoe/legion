# Notes for the cache filler's workflow

The derivations behind the budgets of `.github/workflows/ci-caches.yaml`, which point into this
file with `# why:` lines, as `.github/ci-notes/README.md` says, and the record of what the workflow
fills and why. Each value's own arithmetic stays on its line in the workflow file. The budgets of
that file's other jobs are still derived in the comments above their keys.

## What this workflow fills, and why it runs on a push to main

It fills the Actions caches ci.yaml's jobs restore, so a pull request or a merge group finds
them warm. ci.yaml runs on a pull request, a merge group and a dispatch, and no longer on a push
to main: the queue tests every change on the tree it lands as, so a run on main would only test
that tree again (.github/ci-notes/ci.md#why-the-router-runs-on-these-events-and-no-others says
it). No step of that workflow saves a cache entry any more, and the actions it calls save only
in a dispatched run, so the families it restores need a filler of their own, and this workflow
is it.

Why the filler runs on a push: a run reads the cache scope of its own branch and that of the
default branch, and only an event that writes the default branch's scope (a push to main among
them) leaves an entry every later run can read. A save made in a merge group or on a pull
request lands in that run's own scope, which the next pull request's run cannot read, so it
would reach nobody.

Six Go families, each one a restore step of ci.yaml's run names, keyed on the runner's
architecture and on the Go version the family's own module installs (go-envoy-infra on the hash
of its module files too, for the reason its job gives): go-notices (lint's notices step),
go-daemon (typecheck, daemon-go, plugin-pack and pi-legion), go-envoy (envoy-go-checks,
envoy-go-race-docs, envoy-go-docs-finalizers, listener-server-change, envoy-e2e-local and
envoy-session-stall, and the embedder job of .github/workflows/dispatch-browser.yaml),
go-envoy-test (envoy-go, envoy-go-claim-model, which builds the API's test binary of that set,
and the binaries, multiprocess and visual jobs of .github/workflows/dispatch-browser.yaml, which
ci.yaml's dispatch-browser job calls: those three build the Dispatch server and the real
broker's commands, whose packages the go-envoy entry's build of the listener and natsd alone
does not hold), go-envoy-darwin-vet (envoy-go-checks' darwin vet, whose build cache is a
directory of its own, the GOCACHE of that step's env) and go-envoy-infra (envoy-infra-go, and
the build job of release-envoy-infra.yaml).
The key carries `runner.arch` because Go's build cache holds the objects of the architecture
that compiled them: an entry an x86_64 runner filled gives an arm64 job its modules and none of
its objects. So each family is filled on every architecture one of its readers runs on, one
matrix leg per runner: go-notices on ubuntu-24.04-arm alone, where lint runs, and go-daemon,
go-envoy, go-envoy-test and go-envoy-infra on ubuntu-24.04-arm and on ubuntu-24.04 both, since
each has a reader on each (typecheck and the envoy lanes but one on the first; daemon-go,
plugin-pack, pi-legion, envoy-go-checks, envoy-go-claim-model, the browser lane and the release
builds on the second), and go-envoy-darwin-vet on ubuntu-24.04 alone, where envoy-go-checks
runs. A leg builds what that runner's readers build and no more: go-daemon's arm64 reader is
typecheck alone, so its arm64 leg runs typecheck's vet and leaves the test and race builds to
the x86_64 leg.
go-notices carries the architecture before the OS (go-notices-<arch>-<os>-go<version>) and the
other five after it: the go-notices entry holds go-licenses, a program lint's notices step
runs, and a run of a workflow file older than the architecture in the key restores by
go-notices-<os>-, which an <os>-<arch> key starts with (ci-job-lint.yaml's restore step says what
that did). One Go setup per leg, since the key carries the version that module's go.mod names:
the daemon's go.mod for go-daemon and for go-notices, which lint installs the daemon's Go for;
the envoy module's for go-envoy, go-envoy-test and go-envoy-darwin-vet; the infra module's for
go-envoy-infra.

go-daemon, go-notices, go-envoy, go-envoy-test and go-envoy-darwin-vet are filled again on
every push to main. The fill restores the newest entry of its family, runs on it the build the
ci.yaml job runs, which compiles only what changed since that entry was saved, and saves the
result under the key ci.yaml's readers restore followed by the commit (`-${{ github.sha }}`).
The fill's restore asks for the readers' key, with that key and a `-` as its restore-keys.
GitHub matches the key itself as a prefix too, so an entry of another Go version is restored
only when that version's text starts with this one's (go1.27.30 for go1.27.3), and Go keys every
object of its build cache on the toolchain that compiled it, so such an entry costs only its
download. No entry carries the readers' key itself any more, so GitHub's matching gives a reader
the newest entry whose key starts with it: the last fill's.
Why: these keys used to name the Go version alone, and the fill saved only when that exact key
was missing, so each family was filled once per Go version. The go-daemon, go-envoy and
go-envoy-test entries every job restored on 2026-11-22 had been saved on 2026-11-18 between
13:03 and 13:08Z (go-notices' on 2026-11-19), and at main's e7381b7d4c9 a job that restored
them had to compile again 312 of the daemon module's 335 packages and 573 of the envoy module's
618 (`go list -test -deps -f {{.Stale}}`); from an entry filled five merges earlier, 135 and
321. The refill-superseded job deletes the other entries of the family once the new one is
listed, three a run at most, newest first, so that its deletes are three priced ladders inside
the job's budget that check-download-retry.sh can read (#the-refill-superseded-job); a common run
supersedes one, so each family keeps one entry per runner, as it did before. A job that is downloading
a deleted entry at that moment gets a cache miss: actions/cache/restore warns and the job goes
on, compiling what the entry would have given it, so it is slower, not failed.
The build cache in an entry does not grow with each fill: Go deletes the objects no build has
used for five days (trimLimit in Go's cmd/go/internal/cache), and a restore keeps each file's
modification time, which is the time of last use Go reads. The module cache keeps every module
version it has downloaded (Go removes none by itself), until a Go bump starts the family again:
after a bump the fill finds no entry of the new Go (unless an older version's text starts with
the new one's, as the prefix note above says) and builds from nothing.

go-envoy-infra, elan and the two dispatch builds are still looked up first (`lookup-only`, and
no `restore-keys`: the question is whether that exact key exists, and the answer downloads
nothing), and only a miss runs the build and saves what it left. Each of their keys carries a
hash of the files that choose what the entry holds, and a run that finds its key builds
nothing. Each save takes the key its lookup computed (`steps.<id>.outputs.cache-primary-key`),
never a hashFiles() of its own: the build can change a hashed file (a go command may add the
checksums it fetched to go.sum or go.work.sum), so a hash taken after the build need not be the
lookup's. go-envoy-infra's save hashed its files again, and its key drifted: in run 45088325195
the lookup asked for ...-85ddfce1... and the save wrote ...-f2bbc22a..., so every push missed the
lookup and built for seven to ten minutes a leg. Where a job fills two families it fills the
smaller one first, so the entry that carries the other's objects too is the one that was large
already. A leg of a matrix that fails leaves the other running (`fail-fast: false`): a cancelled
leg saves nothing, and one architecture's fill has no reason to cost the other's.

Beside those six, the architecture-check job fills
architecture-check-<os>-<arch>-go<version>-<hash>, the family of the composite action
.github/actions/architecture-check, and the spec-check job fills
spec-check-<os>-<arch>-go<version>-<hash>, the family of .github/actions/spec-check; ci.yaml's
envoy-go-checks runs both actions. Neither is a lookup step of this workflow: each action
restores and saves its entry itself, keyed on the runner's architecture, on the Go version its
own setup-go installs from packages/envoy/go.mod and on the SHA-256 of packages/envoy/go.sum,
so both jobs run on ubuntu-24.04, the runner envoy-go-checks runs on. On a miss it restores the
newest entry of that Go version (restore-keys). It saves only on a miss of the exact key in a
push, schedule or workflow_dispatch run: a run of any other event (pull_request,
pull_request_target, merge_group, issue_comment, workflow_run and the rest) may hold another
person's code, so it restores the entry and never saves it. This workflow's push to main is
where the entry lands in the scope every later run reads. check-ci-filters.sh's cache-family
rule reads this workflow's own actions/cache steps and not a composite action's, so it holds
nothing of these two families; the "Caches" paragraph of
.github/ci-notes/ci.md#the-router-every-check-a-pull-request-or-the-merge-queue-runs is the
record of their restores and saves.

Four families here are no Go family. The elan home the airlock-lean job restores, whose key is
the content of the Lean toolchain the model names rather than a Go version. The bun family,
~/.bun/bin, which .github/actions/setup-bun restores in every job that calls it and saves only
where its caller passes `save: 'true'` on a push to main: the bun job is the one caller that
does, the family's one saving job as each Go family has one. The key names the runner's
architecture, so each architecture needs an entry and a saving job of its own, and the bun job
runs a leg on each runner the action's callers run on, ubuntu-24.04 and ubuntu-24.04-arm. And
the two builds dispatch-two-builds runs the tree against, the previous release's and the
first-rollout release's, which that job restores and this workflow's dispatch-builds job builds
and saves. Two others that were no Go family went with the jobs that read them, the Playwright
browsers and the .deb files of the apt list the browser suite installed, because
dispatch-browser.yaml's shard and alone jobs run in the image this repository builds, which
carries the browsers and their host packages, so neither entry has a restore left to serve
(dispatch://INFRA-1443). A save whose restore is gone is what check-ci-filters.sh refuses, so
the two jobs that wrote them are gone with them.

go-docs is not here: docs-pages.yaml saves it on this same push, from the build job it shares
with ci.yaml's docs job.

## The refill-superseded job

It deletes the entries of go-daemon, go-notices, go-envoy, go-envoy-test and go-envoy-darwin-vet
that the fills replaced, once each fill has saved its new one. Each fill saves a new entry, over
a gigabyte for most families, so without this the entries would pile up: GitHub drops an entry
only after seven days unread, and over the repository's cache limit it evicts the least recently
used entries, which can be another family's live one. For each family and architecture it keeps
one entry of this ref, the one the fill just saved, and deletes the others of that family,
runner and Go, three a run at most (below): those whose key is the readers' key followed by
`-`, and the one under the readers' key itself, which the fills saved before they named the
commit and which would otherwise win every reader's restore as an exact match. The family then
holds what it held before the refill, one entry per runner, in a repository near its cache
limit. A job that is downloading a deleted entry at that moment gets a cache miss:
actions/cache/restore warns and the job goes on, compiling what the entry would have given it,
slower but not failed. An entry of another runner or another Go is no such entry and stays. None
is deleted unless the new key is among this ref's entries, so a save that did not land leaves
the old entries in place. It is a job of its own, so the token that may delete caches is in no
job that builds code. A cache key is text any build that saves on this ref chose, so the step
never splits or prints one raw: the list stays JSON, jq selects the numeric ids to delete, and
each id is checked to be a number before `gh cache delete` gets it. One leg per family and
architecture, each keeping the key that fill's leg saved, under the output name the fill gives
it; a leg whose fill saved nothing (it failed before its save) skips its steps, and the job runs
when any fill failed, so one failed fill does not stop the others' deletes. Every leg runs on
ubuntu-24.04-arm: gh and jq need nothing of an x64 machine.

Each read and each delete retries through `.github/scripts/ci-retry.sh` at 30 s an attempt, the
bound this repository gives a `gh` read of the API (dispatch://INFRA-1545, R1), run through
gh-read.sh's gh_wait_reset: the list is one ladder, 5 x 30 + 150 = 300 s, and each delete one
more. A leg deletes three entries at most, newest first, in a loop over three literal passes,
which is a count `.github/scripts/check-download-retry.sh` reads: a common run deletes one, the
entry the fill before it saved, and the first run after the keys took the commit deletes the
entry under the readers' key beside it; a third is room for one a failed run left. Any more
stay, a warning says how many, and the next fill deletes them, since each fill adds one entry
and may delete three. A delete whose first attempt landed and whose answer was lost is refused
on its retry as an entry that is gone, and fails the step by name; the next fill deletes what is
left. The step's own budget is 300 + 3 x 300 = 1200 s, a whole number of minutes, so it takes
the minute above, 21; the job's is 300 s, the 5 minutes main's #5141 gave it, plus that one
retried step, 300 + 1200 = 1500 s -> 26 min.

## The elan job's budget

The job's measured term is 1800 s, the 30 minutes it was written with: one toolchain download and
one build of the model on a miss, where a run that finds the key costs the checkout alone. The
Budgets note of `.github/ci-budgets.md` adds its single slowest retried step, install-elan's one
`ci_tools_fetch` of the elan release tarball, 1708 s (that note's third worst case), above the Lean
toolchain step's 750 s. 1800 + 1708 = 3508 s -> 59 min.

The Lean toolchain step is airlock-lean's, run here on the same miss that runs the build:
`elan toolchain install` of the toolchain lean-toolchain names, through
`.github/scripts/ci-retry.sh` at 120 s an attempt, so the build after it downloads nothing and the
entry saved holds the toolchain airlock-lean's build reads.
`.github/ci-notes/ci-job-airlock-lean.md#the-lean-toolchain-step-and-its-bound` derives that
bound from four measured installs, this job's own among them: job 134958116292 of run 44348716140,
the first fill on ubuntu-24.04-arm, where elan's download took 4.8 s and the whole install 11.6 s.
5 x 120 + 150 = 750 s -> 13 min.

## The bun job's budget

The bun job is the bun family's one saving job (the job's own comment says why there is one leg
per runner architecture). No run of it is measured yet, so its first term is built from what it
runs: a checkout of one commit, 68 s at most over the 1,032 checkouts of the 25 ci.yaml runs
created from 2026-11-21T07:46Z to 08:23Z (median 4 s), and `.github/actions/setup-bun`, 5 s at
most over the 482 calls in those runs that restored the entry. A miss adds the release asset of
51,794,595 bytes and the save of what it installed. 600 s holds that with room, and the Budgets
note of `.github/ci-budgets.md` adds the job's one retried step, setup-bun's two `ci_tools_fetch`
calls, 2 x 1708 = 3416 s (that note's terms). 600 + 3416 = 4016 s -> 67 min, above that step's own
57.

## The dispatch-builds job's budget

The job builds, on a miss of its key, each of the two builds ci.yaml's dispatch-two-builds job
restores, with the script and the bounds that job's build steps run
(`.github/ci-notes/ci-job-dispatch-two-builds.md#the-build-steps-budgets`): each build step is
1050 + 1050 + 116 = 2216 s -> 37 min.

No run of it is measured yet, so its first term is built from what it runs: the checkout of the
whole history, 96 s at most over the 753 checkouts of dispatch-two-builds that job's own comment
counts, and 68 s at most in the window the bun job's term above reads; setup-go and setup-bun, the
latter 5 s at most on a restore; and the two builds, 116 s at most each. That is under 400 s, and
900 s holds it with room. Its slowest retried step is setup-bun's two `ci_tools_fetch` calls,
3416 s, above each build's 2216 s. 900 + 3416 = 4316 s -> 72 min, above every step's own budget.

## The notices step whose script the gate does not read

`Write every Go release archive's third-party notices and cross-build the commands`, in the notices
job, runs `.github/scripts/go-release-notices.sh`, the script ci.yaml's lint job runs, and so waits
for the one go-licenses install of `scripts/go-third-party-notices.sh` that
`.github/ci-notes/ci-job-lint.md#the-notices-step-whose-script-the-gate-does-not-read` derives,
1050 s, which `.github/scripts/check-download-retry.sh` does not price. Beside it is the step's
measured duration from a cold cache, which holds the cold build of every release target: 674 s at
most over the 2 runs of the step in the 100 successful runs of this workflow created from
2026-11-19T12:04Z to 2026-11-21T09:04Z (job 136376790855 of run 44762031448; every other run then
found the key and skipped it). Since main's #5141 the step runs on every push, on the family's
newest entry, where it compiles only what changed since that fill, so the cold figure is the most
a run of it takes. 674 + 1050 = 1724 s -> 29 min, under the job's 127.
