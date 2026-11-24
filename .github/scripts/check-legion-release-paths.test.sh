#!/usr/bin/env bash
# Tests for `check-legion-release-paths.sh`: every package of this repository the `legion` binary
# links, for each target .github/go-release-targets.json lists for it, and the go.mod and go.sum
# of its module, is covered by a path of release.yaml's LEGION_VERSION_ARGS. The case that matters
# is the one the check exists for: a package the binary links that the list does not name, on the
# runner's own target or on another one.
#
# Each case builds a miniature repository under a temporary directory, with a daemon module whose
# `replace` reaches an envoy module as the real one does, and symlinks the real script into it, so
# the script under test is the file CI runs and the `go list` it runs is a real one. Needs Go and
# jq.
#
# Run from anywhere: .github/scripts/check-legion-release-paths.test.sh
# CI runs it in ci.yaml's test job, after setup-go.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
check_script="$script_dir/check-legion-release-paths.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

# The paths of a list that covers the fixture's whole binary: what fixture writes after `--`.
covering='packages/daemon/ packages/envoy/used/ packages/envoy/deep/ packages/envoy/go.mod packages/envoy/go.sum'

# fixture <name>: a tree that passes. Its legion command links the daemon's own helper package and
# the envoy module's used, which links deep; envoy's unused is linked by nothing. Its target list
# names a linux and a darwin target for legion, as the real one does. Echoes its root.
fixture() {
  local root="$work/$1"
  if [ -e "$root" ]; then
    echo "  FAIL: the fixture name '$1' is already taken by an earlier case" >&2
    return 1
  fi
  mkdir -p "$root/.github/scripts" "$root/.github/workflows" \
    "$root/packages/daemon/cmd/legion" "$root/packages/daemon/internal/helper" \
    "$root/packages/envoy/used" "$root/packages/envoy/deep" "$root/packages/envoy/unused"
  ln -s "$check_script" "$root/.github/scripts/check-legion-release-paths.sh"
  printf 'module github.com/example/daemon\n\ngo 1.22\n\nrequire github.com/example/envoy v0.0.0\n\nreplace github.com/example/envoy => ../envoy\n' \
    > "$root/packages/daemon/go.mod"
  printf 'module github.com/example/envoy\n\ngo 1.22\n' > "$root/packages/envoy/go.mod"
  : > "$root/packages/daemon/go.sum"
  : > "$root/packages/envoy/go.sum"
  cat > "$root/packages/daemon/cmd/legion/main.go" <<'GO'
package main

import (
	"github.com/example/daemon/internal/helper"
	"github.com/example/envoy/used"
)

func main() { helper.Help(); used.Use() }
GO
  printf 'package helper\n\nfunc Help() {}\n' > "$root/packages/daemon/internal/helper/helper.go"
  printf 'package used\n\nimport "github.com/example/envoy/deep"\n\nfunc Use() { deep.Deep() }\n' \
    > "$root/packages/envoy/used/used.go"
  printf 'package deep\n\nfunc Deep() {}\n' > "$root/packages/envoy/deep/deep.go"
  printf 'package unused\n\nfunc Unused() {}\n' > "$root/packages/envoy/unused/unused.go"
  printf '{\n  "legion": ["linux/amd64", "darwin/arm64"],\n  "envoy": ["amd64"]\n}\n' > "$root/.github/go-release-targets.json"
  release "$root" "$covering"
  echo "$root"
}

# release <root> <paths>: release.yaml with a LEGION_VERSION_ARGS of one --patch-path and <paths>
# after its `--`.
release() {
  printf 'name: Release\non: push\nenv:\n  LEGION_VERSION_ARGS: >-\n    --patch-path packages/daemon/internal/prompts/roles/\n    -- %s\njobs: {}\n' \
    "$2" > "$1/.github/workflows/release.yaml"
}

run_check() {
  local root=$1
  set +e
  out=$("$root/.github/scripts/check-legion-release-paths.sh" 2>&1)
  status=$?
  set -e
}

echo "case: the list covers every package the binary links and both modules' files"
root=$(fixture green)
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "says what it compared" \
  "$(contains "$out" "covers all 4 packages of this repository that go list -deps ./cmd/legion prints for the 2 legion targets of .github/go-release-targets.json, and the go.mod and go.sum of their 2 modules")"

echo "case: a package the binary links that the list does not name"
root=$(fixture planted)
release "$root" 'packages/daemon/ packages/envoy/used/ packages/envoy/go.mod packages/envoy/go.sum'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the package and the fix" \
  "$(contains "$out" "LEGION_VERSION_ARGS names no path that covers 1 of what the legion binary links (go list -deps ./cmd/legion in packages/daemon), so a change to it alone would release no legion: packages/envoy/deep/. Add each after the")"
check "names no package the binary does not link" "$(is "$(contains "$out" 'packages/envoy/unused')" false)"

echo "case: a package the binary comes to link through a new import"
root=$(fixture new-import)
sed -i 's|"github.com/example/envoy/used"|"github.com/example/envoy/unused"\n\t"github.com/example/envoy/used"|; s|used.Use() }|used.Use(); unused.Unused() }|' \
  "$root/packages/daemon/cmd/legion/main.go"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the package the import brought in" "$(contains "$out" 'would release no legion: packages/envoy/unused/\.')"

echo "case: a package the binary links only when it is built for another target"
# The release builds legion for each target of the list, and a file a build constraint keeps to
# one of them brings in its imports there alone: darwin.go links unused only into the darwin
# binary, which no runner of this check is.
root=$(fixture other-target)
printf '//go:build darwin\n\npackage main\n\nimport _ "github.com/example/envoy/unused"\n' \
  > "$root/packages/daemon/cmd/legion/darwin.go"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the package only the darwin binary links" "$(contains "$out" 'would release no legion: packages/envoy/unused/\.')"

echo "case: a module file the list does not name"
root=$(fixture module-file)
release "$root" 'packages/daemon/ packages/envoy/used/ packages/envoy/deep/ packages/envoy/go.mod'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the go.sum" "$(contains "$out" 'would release no legion: packages/envoy/go.sum\.')"

echo "case: a directory covers every package below it"
root=$(fixture directory)
release "$root" 'packages/daemon/ packages/envoy/'
run_check "$root"
check "exits 0" "$(is "$status" 0)"

echo "case: an exclude takes a path back out"
root=$(fixture exclude)
release "$root" ':(exclude)packages/envoy/deep packages/daemon/ packages/envoy/'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the excluded package" "$(contains "$out" 'would release no legion: packages/envoy/deep/\.')"

echo "case: pathspec magic the check does not read"
root=$(fixture magic)
release "$root" 'packages/daemon/ :(glob)packages/envoy/**'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names it" "$(contains "$out" 'names :(glob)packages/envoy/\*\*, a pathspec magic this check does not read')"

echo "case: no list, or no paths in it"
root=$(fixture no-list)
printf 'name: Release\non: push\nenv:\n  OTHER: x\njobs: {}\n' > "$root/.github/workflows/release.yaml"
run_check "$root"
check "exits 1 with no LEGION_VERSION_ARGS" "$(is "$status" 1)"
check "says so" "$(contains "$out" 'has no env LEGION_VERSION_ARGS')"

root=$(fixture no-dashes)
printf 'name: Release\non: push\nenv:\n  LEGION_VERSION_ARGS: packages/daemon/\njobs: {}\n' > "$root/.github/workflows/release.yaml"
run_check "$root"
check "exits 1 with no --" "$(is "$status" 1)"
check "says so" "$(contains "$out" 'LEGION_VERSION_ARGS has no `--`')"

echo "case: a target list that names no legion target, or none at all"
root=$(fixture no-targets)
printf '{\n  "envoy": ["amd64"]\n}\n' > "$root/.github/go-release-targets.json"
run_check "$root"
check "exits 1 with no legion target" "$(is "$status" 1)"
check "says so" "$(contains "$out" 'go-release-targets.json lists no legion target, so the packages the legion binary links are unknown')"

root=$(fixture no-target-list)
rm "$root/.github/go-release-targets.json"
run_check "$root"
check "exits 1 with no target list" "$(is "$status" 1)"
check "says it could not read it" "$(contains "$out" 'could not read the legion targets of .github/go-release-targets.json')"

echo "case: a listing longer than one environment variable can hold"
# 500 packages with 245-character directory names make a go list output of about 180 KiB, more
# than the 128 KiB one environment variable holds: the check handed its listing over in one once,
# and CI's runner refused the python with "Argument list too long".
root=$(fixture long-listing)
pad=$(printf '%0240d' 0)
imports=""
for i in $(seq 1 500); do
  mkdir -p "$root/packages/envoy/p${i}_$pad"
  printf 'package p%s\n' "$i" > "$root/packages/envoy/p${i}_$pad/p.go"
  imports+=$'\t_ "github.com/example/envoy/p'"${i}_$pad"$'"\n'
done
printf 'package main\n\nimport (\n%s)\n\nfunc main() {}\n' "$imports" > "$root/packages/daemon/cmd/legion/main.go"
release "$root" 'packages/daemon/ packages/envoy/'
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts every package" "$(contains "$out" 'covers all 501 packages of this repository')"

echo "case: go list fails"
root=$(fixture broken)
printf 'package helper\n\nimport "github.com/example/daemon/internal/gone"\n\nfunc Help() { gone.Gone() }\n' \
  > "$root/packages/daemon/internal/helper/helper.go"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "says the packages are unknown" "$(contains "$out" 'go list -deps ./cmd/legion failed in packages/daemon, so the packages the legion binary links are unknown')"
check "names the target it failed for" "$(contains "$out" 'are unknown (target linux/amd64)')"

echo "case: go list printed no package of this repository"
root=$(fixture outside)
mv "$root/packages" "$work/outside-packages"
ln -s "$work/outside-packages" "$root/packages"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "says it compared nothing" "$(contains "$out" 'printed no package of this repository, so this check compared nothing')"

summary "check-legion-release-paths.sh"
