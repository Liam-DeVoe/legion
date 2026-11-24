# Notes for the CI caches workflow

The derivations of `.github/workflows/ci-caches.yaml`, which points into this file.
Each `# why:` line of that workflow names the section below that holds the derivation it rests on.

## The entries that are no Go family

Four entries the workflow fills are no Go family: the elan home the airlock-lean job restores, whose
key is the content of the Lean toolchain the model names rather than a Go version; the two earlier
Dispatch builds dispatch-two-builds restores, each keyed on a release commit and the hash of the
script that builds it, which the dispatch-builds job fills; and zsh-debs, Ubuntu's two zsh packages
that `.github/actions/install-zsh` installs in the claude-envoy and dispatch-ui jobs, keyed on the
hash of that action, which pins them. Two others that were no Go family went with the jobs that read
them - the Playwright browsers and the .deb files of the apt list the browser suite installed -
because dispatch-browser.yaml's shard and alone jobs run in the image this repository builds, which
carries the browsers and their host packages, so neither entry has a restore left to serve. A save
whose restore is gone is what check-ci-filters.sh refuses, so the two jobs that wrote them are gone
with them.

## The caches this workflow does not fill

go-docs is not here: docs-pages.yaml saves it on this same push, from the build job it shares with
ci.yaml's docs job. The Bun cache is not here either: `.github/actions/setup-bun` reads and writes it
on main, and the workflows that run there call it, this one's dispatch-builds job too.

## The elan entry

The elan home ci.yaml's airlock-lean job restores, the Lean toolchain that job's `lake build` needs.
No setup-go and no Go family: the key is the runner's architecture, since elan and the toolchain are
binaries of it, and the content of the lean-toolchain the model names, since that file is what
decides which toolchain elan installs, so the entry is refreshed when the model moves to another
Lean and at no other time. install-elan puts elan under `$RUNNER_TEMP/elan` and `lake build`
downloads the named toolchain into it on a miss, which is the whole of what is saved. The job runs
on ubuntu-24.04-arm, the runner airlock-lean runs on.

## The zsh-debs entry

The two Ubuntu packages `.github/actions/install-zsh` pins, which ci.yaml's claude-envoy and
dispatch-ui jobs restore and the action then installs with `dpkg -i` and no `apt-get update`. The
key is the runner's architecture, since the packages are arm64's, and the hash of that action, since
the action holds the pins, so the entry is refreshed when a pin moves and at no other time. The
action with `install: 'false'` fetches the two files into `$RUNNER_TEMP/zsh-debs` and holds each to
its pin, which is the whole of what is saved. The job runs on ubuntu-24.04-arm, the runner both
readers run on.
