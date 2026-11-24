#!/usr/bin/env bash
# The release archives of the Go `legion`: one per target .github/go-release-targets.json lists,
# each holding that target's static binary and its THIRD_PARTY_NOTICES, packed into
# assets/legion-<os>-<arch>.tar.gz for the publish job to upload.
#
# This script is where the release says which targets legion is built for and with which
# environment and command. Its modes answer those questions from the same functions its build
# runs, so a reader asks it rather than keeping a copy:
#
#   .github/scripts/release-build-legion.sh
#   .github/scripts/release-build-legion.sh targets
#   .github/scripts/release-build-legion.sh list <os>/<arch> <go list flag>...
#   .github/scripts/release-build-legion.sh env <os>/<arch> <go env argument>...
#
# No argument: the build. It reads five variables of the step's environment: $VERSION, the
# version this run releases, and $PI_LEGION_VERSION and $PI_ENVOY_VERSION, the plugin pair this
# binary links, which the step's `env:` carries from the version and plugin_versions steps, and
# $GITHUB_SHA and $GITHUB_WORKSPACE, which the runner sets. It reads .github/go-release-targets.json
# and writes out/ and assets/ of the checkout, and stops, building nothing, when it cannot read
# the targets.
#
# targets: the legion targets of .github/go-release-targets.json, one <os>/<arch> a line, the list
# the build loops over. A target that is not lower-case letters and digits on each side of one /
# is refused with an ::error:: and exit 1: such a target is one word and one line, so a reader of
# this mode's lines gets the targets the build's loop over its words gets.
# list: `go list <go list flag>... ./cmd/legion` in packages/daemon, with the environment the build
# gives <os>/<arch>: what that target's binary is built from.
# env: `go env <go env argument>...` in packages/daemon with that environment, such as GOWORK, the
# go.work that target's build reads.
# The modes read none of the build's five variables and write nothing. A mode the script does not
# know, `targets` with an argument, and `list` or `env` without a target exit 2 with the usage.
#
# Run from the repository root.
# CI runs the build in release.yaml's legion_build job, at the step "Build legion", and
# .github/scripts/check-legion-release-paths.sh runs the three modes in ci.yaml's lint job.
set -euo pipefail

usage='usage: release-build-legion.sh [targets | list <os>/<arch> <go list flag>... | env <os>/<arch> <go env argument>...]'
targets_file=.github/go-release-targets.json

# targets: print the legion targets, one a line, or fail. jq's own failure is returned, not
# exited on: a command substitution does not stop on an error inside it, so the build's
# `list=$(targets)` would otherwise read an unreadable list as an empty one.
targets() {
  local list target
  list=$(jq -r '.legion[]' "$targets_file") || return
  [ -n "$list" ] || return 0
  while IFS= read -r target; do
    if ! [[ $target =~ ^[a-z0-9]+/[a-z0-9]+$ ]]; then
      echo "::error file=$targets_file::release-build-legion.sh: the legion target '$target' of $targets_file is not <os>/<arch>, lower-case letters and digits on each side of one /" >&2
      return 1
    fi
  done <<<"$list"
  printf '%s\n' "$list"
}

# for_target <os>/<arch> <command>...: run <command> with the environment the build gives that
# target: no cgo, so the binary is static, and the target's GOOS and GOARCH.
for_target() {
  local target=$1
  shift
  CGO_ENABLED=0 GOOS="${target%/*}" GOARCH="${target#*/}" "$@"
}

build() {
  local list target dir
  list=$(targets)
  mkdir -p out assets
  for target in $list; do
    dir="legion-${target%/*}-${target#*/}"
    mkdir -p "out/$dir"
    for_target "$target" go -C packages/daemon build -buildvcs=false \
      -ldflags "-X main.revision=$GITHUB_SHA -X main.release=v$VERSION -X main.piLegionVersion=$PI_LEGION_VERSION -X main.piEnvoyVersion=$PI_ENVOY_VERSION" \
      -o "$GITHUB_WORKSPACE/out/$dir/legion" ./cmd/legion
    (cd packages/daemon && for_target "$target" \
      ../../scripts/go-third-party-notices.sh "$GITHUB_WORKSPACE/out/$dir/THIRD_PARTY_NOTICES" ./cmd/legion)
    tar czf "assets/$dir.tar.gz" -C out "$dir"
  done
}

refuse() {
  echo "::error::release-build-legion.sh: $1; $usage" >&2
  exit 2
}

if [ $# -eq 0 ]; then
  build
  exit 0
fi
case $1 in
  targets)
    [ $# -eq 1 ] || refuse "targets takes no argument"
    targets
    ;;
  list | env)
    [ $# -ge 2 ] || refuse "$1 needs a target"
    mode=$1 target=$2
    shift 2
    if [ "$mode" = list ]; then
      for_target "$target" go -C packages/daemon list "$@" ./cmd/legion
    else
      for_target "$target" go -C packages/daemon env "$@"
    fi
    ;;
  *) refuse "unknown mode $1" ;;
esac
