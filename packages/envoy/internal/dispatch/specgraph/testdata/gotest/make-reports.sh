#!/usr/bin/env bash
# Makes again the go test -json reports of this directory that come from the packages in cases/:
# for each package cases/<name>, the report <name>.jsonl. Each report is a real run of
# `go test -json -trimpath` of Go 1.27.3, the Go that packages/envoy/go.mod names, in a scratch
# module example.com/acme/gotest. The packages whose names start with kill run under -timeout 1s,
# count2 runs under -count=2, and each other package under -count=1. Run it when that Go moves or
# a case changes, and then read each new report and the rows of gotest_test.go that read it.
# -trimpath keeps each path of a stack relative to its module, and the script refuses a report
# that names its scratch directory.
set -euo pipefail
version=go1.27.3
here=$(cd "$(dirname "$0")" && pwd)
module=$(mktemp -d)
trap 'rm -rf "$module"' EXIT
cp -R "$here/cases/." "$module/"
printf 'module example.com/acme/gotest\n\ngo 1.27\n' >"$module/go.mod"
unset GOROOT
export GOTOOLCHAIN=$version GOWORK=off GOFLAGS=
cd "$module"
if [ "$(go env GOVERSION)" != "$version" ]; then
  echo "make-reports.sh: go env GOVERSION is $(go env GOVERSION), want $version" >&2
  exit 1
fi
for dir in */; do
  name=${dir%/}
  case $name in
    kill*) flags=(-count=1 -timeout 1s) ;;
    count2) flags=(-count=2) ;;
    *) flags=(-count=1) ;;
  esac
  status=0
  go test -json -trimpath "${flags[@]}" "./$name/" >"$here/$name.jsonl" || status=$?
  if grep -qF "$module" "$here/$name.jsonl"; then
    echo "make-reports.sh: $name.jsonl names the scratch directory $module" >&2
    exit 1
  fi
  echo "go test -json -trimpath ${flags[*]} ./$name/: exit $status, $(wc -l <"$here/$name.jsonl") events"
done
