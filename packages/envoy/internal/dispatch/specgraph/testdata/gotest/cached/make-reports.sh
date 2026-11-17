#!/usr/bin/env bash
# Makes again the go test -json reports of this directory: real runs of Go 1.27.3, the Go that
# packages/envoy/go.mod names, of the packages in cases/, in a scratch module
# example.com/acme/gotest with a test cache of its own, so a run is replayed from the cache exactly
# when an earlier run of this script filled it. Each command runs two times, GOTEST_RUN=1 and then
# GOTEST_RUN=2, and the second report is kept:
#   - first.jsonl: the first run of `go test -json -trimpath ./cached/`, before the cache holds it.
#   - cached.jsonl: `go test -json -trimpath ./cached/`, replayed: "ok  \t<path>\t(cached)".
#   - cached-cover.jsonl: with -cover: "(cached)\tcoverage: 100.0% of statements".
#   - cached-norun.jsonl: with -run '^TestNone$': "(cached) [no tests to run]".
#   - cached-two.jsonl: `./cached/ ./fresh/`: cached is replayed, and fresh runs again, because its
#     test reads GOTEST_RUN.
# Run it when that Go moves or a case changes, and then read each new report and the rows of
# cached_test.go that read it. The script refuses a report that names one of its scratch
# directories.
set -euo pipefail
version=go1.27.3
here=$(cd "$(dirname "$0")" && pwd)
module=$(mktemp -d)
trap 'rm -rf "$module"' EXIT
cp -R "$here/cases/." "$module/"
printf 'module example.com/acme/gotest\n\ngo 1.27\n' >"$module/go.mod"
unset GOROOT
export GOTOOLCHAIN=$version GOWORK=off GOFLAGS= GOCACHE=$module/.gocache
cd "$module"
if [ "$(go env GOVERSION)" != "$version" ]; then
  echo "make-reports.sh: go env GOVERSION is $(go env GOVERSION), want $version" >&2
  exit 1
fi
# report <name> <go test arguments>...: the two runs, the second kept as <name>.jsonl.
report() {
  local name=$1
  shift
  GOTEST_RUN=1 go test -json -trimpath "$@" >"$module/$name.first.jsonl"
  GOTEST_RUN=2 go test -json -trimpath "$@" >"$here/$name.jsonl"
  if grep -qF "$module" "$here/$name.jsonl"; then
    echo "make-reports.sh: $name.jsonl names a scratch directory of this run" >&2
    exit 1
  fi
  echo "go test -json -trimpath $*, the second run: $(wc -l <"$here/$name.jsonl") events"
}
GOTEST_RUN=1 go test -json -trimpath ./cached/ >"$here/first.jsonl"
echo "go test -json -trimpath ./cached/, the first run: $(wc -l <"$here/first.jsonl") events"
report cached ./cached/
report cached-cover -cover ./cached/
report cached-norun -run '^TestNone$' ./cached/
report cached-two ./cached/ ./fresh/
