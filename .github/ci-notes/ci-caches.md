# Notes for the CI caches workflow

The derivations and the history of `.github/workflows/ci-caches.yaml`, which points into this
file. Each `# why:` line of that workflow names the section below that holds the derivation or the
record its head comment or its job rests on. A value's own arithmetic stays beside the value.
In the text moved here, "this workflow" is `.github/workflows/ci-caches.yaml`, and a job "above"
or "below" is a job of it.

## The seven Go families, their keys and the runners each is filled on

Seven Go families, each one a restore step of ci.yaml's run names, keyed on the runner's
architecture and on the Go version the family's own module installs (go-envoy-infra on the hash of
its module files too, for the reason its job gives): go-notices (lint's notices step), go-daemon
(typecheck, daemon-go, plugin-pack and pi-legion), go-envoy (envoy-go-build, envoy-go-checks,
envoy-go-race-docs, envoy-go-docs-finalizers, listener-server-change, envoy-e2e-local and
envoy-session-stall), go-envoy-test (envoy-go, and envoy-go-claim-model, which builds the test
binary of internal/dispatch/api out of that set), go-dispatch-browser (the binaries job of
.github/workflows/dispatch-browser.yaml, which ci.yaml's dispatch-browser and
dispatch-browser-pr jobs call: it builds the Dispatch server, the real broker's commands and the
dev OIDC issuer for the whole run with CGO_ENABLED=0, and an entry filled with cgo on holds
almost none of those objects, as the dispatch-browser job below says; no other job of that
workflow runs a go command), go-envoy-darwin-vet (envoy-go-build's darwin vet, whose build cache
is a directory of its own, the GOCACHE of that step's env) and go-envoy-infra
(envoy-infra-build and envoy-infra-go, and the build job of release-envoy-infra.yaml). The key
carries `runner.arch` because Go's build cache holds the objects of the architecture that
compiled them: an entry an x86_64 runner filled gives an arm64 job its modules and none of its
objects. So each family is filled on every architecture one of its readers runs on, one matrix
leg per runner: go-notices on ubuntu-24.04-arm alone, where lint runs, go-dispatch-browser on
ubuntu-24.04 alone, where the binaries job runs, go-envoy-darwin-vet on ubuntu-24.04 alone,
where envoy-go-build runs, and go-daemon, go-envoy, go-envoy-test and go-envoy-infra on
ubuntu-24.04-arm and on ubuntu-24.04 both, since each has a reader on each, as each reader's own
`runs-on` decides (the envoy job's comment below names go-envoy's and go-envoy-test's readers by
runner). A leg builds what that runner's readers build and no more: go-daemon's arm64 reader is
typecheck alone, so its arm64 leg runs typecheck's vet and leaves the test and race builds to
the x86_64 leg. go-notices carries the architecture before the OS
(go-notices-<arch>-<os>-go<version>) and the other six after it: the go-notices entry holds
go-licenses, a program lint's notices step runs, and a run of a workflow file older than the
architecture in the key restores by go-notices-<os>-, which an <os>-<arch> key starts with
(ci-job-lint.yaml's restore step says what that did). One Go setup per leg, since the key
carries the version that module's go.mod names: the daemon's go.mod for go-daemon and for
go-notices, which lint installs the daemon's Go for; the envoy module's for go-envoy,
go-envoy-test, go-dispatch-browser and go-envoy-darwin-vet; the infra module's for
go-envoy-infra.

The dispatch-browser job's own notes are
.github/ci-notes/dispatch-browser.md#the-fill-of-go-dispatch-browser-in-ci-cachesyaml, and the
envoy job's reading of its readers' runners is the next section.

## Which runners the readers of go-envoy and go-envoy-test run on

The envoy job fills one leg per runner its readers run on, which each reader's own `runs-on`
decides. Read on 2026-11-19 in a tree holding main's 5f3d552e: envoy-go-checks
(go-envoy, ci-job-envoy-go-checks.yaml), envoy-go-claim-model (go-envoy-test,
ci-job-envoy-go-claim-model.yaml) and dispatch-browser-nightly.yaml's re-record job
(go-envoy-test) run on ubuntu-24.04, and every other job that restores either family on
ubuntu-24.04-arm; envoy-go-build (go-envoy, ci-job-envoy-go-build.yaml), which main's #5175
split out of envoy-go-checks, runs on ubuntu-24.04 too. dispatch-browser.yaml's binaries job,
which restored go-envoy-test on ubuntu-24.04 in that tree, restores go-dispatch-browser now,
which this workflow's dispatch-browser job fills.
