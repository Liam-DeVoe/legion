#!/usr/bin/env bash
# The script of the composite action beside it (action.yml): it builds packages/envoy's spec-check
# command from the checkout of this repository the runner fetched for the action, and runs
# `spec-check tree` over a spec directory of the caller's checkout. The action runs it as
# `bash "$GITHUB_ACTION_PATH/check.sh" <mode>`, so its file mode does not matter, and a person can
# run it by hand the same way, with GITHUB_WORKSPACE pointed at any tree.
#
# Usage: check.sh build | check.sh run
#
#   build  Builds ./cmd/spec-check of $GITHUB_ACTION_PATH/../../../packages/envoy into
#          $RUNNER_TEMP/spec-check/bin/spec-check, with its module cache in
#          $RUNNER_TEMP/spec-check/mod and its build cache in $RUNNER_TEMP/spec-check/build, the
#          two directories the action's cache steps restore and save. It exits with go build's
#          status.
#   run    Runs `spec-check tree --dir <dir>` with that binary, <dir> being SPEC_CHECK_PATH
#          resolved against GITHUB_WORKSPACE (an absolute path stays as it is), with --allow-empty
#          when SPEC_CHECK_ALLOW_EMPTY is `true`. The binary's standard error goes to the log, and
#          its standard output to $RUNNER_TEMP/spec-check/stdout, which this script then prints.
#          When that output holds the count line, `spec-check: <dir>: <n> statements, <n> checks,
#          <n> components, <n> interfaces, <n> terms; the tree is valid`, `... is refused` or
#          `... is empty` (each noun in the singular for one), it appends `count=<n>`, the number
#          of statements, to GITHUB_OUTPUT. It exits with the binary's status: 0 valid or empty,
#          1 refused, 2 not judged (no count line then).
#
# Both modes need GITHUB_ACTION_PATH and RUNNER_TEMP. run also needs GITHUB_WORKSPACE,
# GITHUB_OUTPUT, SPEC_CHECK_PATH and SPEC_CHECK_ALLOW_EMPTY, which is exactly `true` or `false`. A
# missing variable, a SPEC_CHECK_ALLOW_EMPTY of any other value, a run before a build, or a mode
# other than the two is exit 2, with one line on standard error.
set -euo pipefail

usage='usage: check.sh build|run'

# refuse prints one line on standard error and exits 2, the status of a check that did not run.
refuse() {
  echo "spec-check action: $*" >&2
  exit 2
}

# require refuses unless each variable it names is set and not empty.
require() {
  local name
  for name in "$@"; do
    [ -n "${!name:-}" ] || refuse "$name is not set"
  done
}

case "$#:${1:-}" in
  1:build | 1:run) mode=$1 ;;
  *)
    echo "$usage" >&2
    exit 2
    ;;
esac

require GITHUB_ACTION_PATH RUNNER_TEMP
module="$GITHUB_ACTION_PATH/../../../packages/envoy"
work="$RUNNER_TEMP/spec-check"
binary="$work/bin/spec-check"

build() {
  mkdir -p "$work/mod" "$work/build" "$work/bin"
  cd "$module"
  # Each setting keeps the caller's job out of this build:
  # - GOWORK=off: the repository's root go.work sits above the action's directory, and the build
  #   reads packages/envoy/go.mod alone.
  # - GOTOOLCHAIN=local: a Go older than go.mod's go line fails by name, and the go command
  #   fetches no other toolchain in its place.
  # - GOFLAGS empty and GOENV=off: the go command reads an empty GOFLAGS from the user's
  #   `go env -w` file instead, so the variable alone does not clear a flag a runner image set.
  # - GOOS and GOARCH unset: a job that cross-compiles its own code cannot make a binary this
  #   runner cannot run.
  # - GOMODCACHE and GOCACHE: the action's own caches, so an entry holds this build alone and
  #   never the caller's.
  env -u GOOS -u GOARCH \
    GOWORK=off GOTOOLCHAIN=local GOFLAGS= GOENV=off \
    GOMODCACHE="$work/mod" GOCACHE="$work/build" \
    go build -buildvcs=false -o "$binary" ./cmd/spec-check
}

run() {
  require GITHUB_WORKSPACE GITHUB_OUTPUT SPEC_CHECK_PATH SPEC_CHECK_ALLOW_EMPTY
  case "$SPEC_CHECK_PATH" in
    /*) dir=$SPEC_CHECK_PATH ;;
    *) dir="$GITHUB_WORKSPACE/$SPEC_CHECK_PATH" ;;
  esac
  case "$SPEC_CHECK_ALLOW_EMPTY" in
    true) set -- tree --dir "$dir" --allow-empty ;;
    false) set -- tree --dir "$dir" ;;
    *) refuse "the input allow-empty must be 'true' or 'false'" ;;
  esac
  [ -x "$binary" ] || refuse "no command at $binary: run check.sh build first"

  local stdout="$work/stdout" status=0 line
  "$binary" "$@" > "$stdout" || status=$?
  cat "$stdout"
  local count_line='^spec-check: .*: ([0-9]+) statements?, [0-9]+ checks?, [0-9]+ components?, [0-9]+ interfaces?, [0-9]+ terms?; the tree is (valid|refused|empty)$'
  while IFS= read -r line || [ -n "$line" ]; do
    if [[ $line =~ $count_line ]]; then
      printf 'count=%s\n' "${BASH_REMATCH[1]}" >> "$GITHUB_OUTPUT"
      break
    fi
  done < "$stdout"
  exit "$status"
}

"$mode"
