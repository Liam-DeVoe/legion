#!/usr/bin/env bash
# Tests for `check-go-offline.sh`, over trees it writes. Its first rule: a Go, a TypeScript, a
# Python and a shell test that each run `go list -deps` with no offline rule are refused and named
# with a GitHub annotation, and the offline form and the reasoned form of each pass, over a count
# that shows the scan saw the command; a reason six lines up is too far; an argv array and a shell
# command whose go names a directory with -C before its subcommand count too; a go command in a
# comment, a quoted string or a heredoc's body starts nothing. Each form its header names is
# planted once and refused bare: an argv Biome writes one element a line (Go, TypeScript, Python),
# exec.CommandContext with a context made by a call, a command beside a neighbour whose Env holds
# GOPROXY=off, the go binary as filepath.Join(runtime.GOROOT(), "bin", "go"), a path ending /go,
# exec.LookPath, Bun.which, shutil.which or a variable, a shell's -c, eval, a heredoc fed to bash,
# os.system, subprocess's string, shell=True and an f-string, execSync and exec, Bun's $`...` with
# cd before go and with -C and a substitution, env SETTING go in an argv, a "GOPROXY=off" in a
# trailing comment, GOPROXY=off before the inherited environment or before a later GOPROXY, the
# later command of a pipeline, an export inside another function, env -u GOPROXY, and a .mts
# test; and the offline form of the environment each language binds to its own command passes.
# Its file reason: refused in a test file, and in a file ci.yaml or a workflow ci.yaml calls names,
# but not in one only a workflow outside that set or a comment names. Its second rule: a job of any
# workflow that runs internal/daemon's tests, which build envoy commands through
# testbuild.EnvoyBinary, is refused when no step before that one runs `.github/scripts/go-fetch.sh
# deps packages/envoy` of every such command - a step with none, one after, one that leaves a
# command out, one in another job - and passes when one does; the tests a step runs through a
# script and the library it sources, after a cd, with -C, or through bash -c count; a compile to
# /dev/null runs no test. Its other half: a step that reaches an offline go build - in a script it
# runs, in a function a sourced library defines and the script calls with its module and packages,
# or in a script the TypeScript of a Playwright run names - is refused with no fetch of those
# packages before it, or one that leaves a package out, and passes after one, or with a reason. Its
# third rule: a required job whose fetch step comes before a restore of ~/go/pkg/mod, or after a
# restore of another path, is refused, and one whose restore comes first passes and is listed. A
# tree with no file of a language, a tree with testbuild and no caller, and a tree with no ci.yaml
# each give UNKNOWN and exit 2.
#
# The cases live in the files this one sources, one part of the check each:
# check-go-offline-cases-go.sh, -typescript.sh, -python.sh and -shell.sh (rule 1 in each
# language), -reasons.sh (its file reasons), -steps.sh (rule 2) and -restores.sh (rule 3). This
# file holds what they share, the fixtures, and the cases of a scan that cannot see.
#
# Each case builds a miniature tree under a temporary directory and symlinks the real script into
# it, so the script under test is the file CI runs: it resolves its root from its own path, which
# the symlink puts at the fixture root. No go command of a fixture runs: the script reads text.
# It needs a python3 with PyYAML, as the script does.
#
# Run from anywhere: .github/scripts/check-go-offline.test.sh
# CI runs it in ci.yaml's test job.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
check_script="$script_dir/check-go-offline.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

# put <root> <path>: writes this harness's stdin to <path> under <root>, making its directory.
put() {
  mkdir -p "$(dirname "$1/$2")"
  cat >"$1/$2"
}

# fixture <name>: a tree the script can scan: the script linked in, a ci.yaml with one job, and one
# file of each of the four languages that starts no go command. Echoes its root.
fixture() {
  local root="$work/$1"
  if [ -e "$root" ]; then
    echo "  FAIL: the fixture name '$1' is already taken by an earlier case" >&2
    return 1
  fi
  mkdir -p "$root/.github/scripts"
  ln -s "$check_script" "$root/.github/scripts/check-go-offline.sh"
  put "$root" .github/workflows/ci.yaml <<'EOF'
name: CI
on: pull_request
jobs:
  lint:
    runs-on: ubuntu-24.04
    steps:
      - run: echo lint
EOF
  printf 'package app\n\nfunc App() {}\n' | put "$root" packages/app/app.go
  printf 'export const app = 1;\n' | put "$root" packages/web/app.ts
  printf 'print("app")\n' | put "$root" tools/app.py
  printf '#!/usr/bin/env bash\necho app\n' | put "$root" tools/app.sh
  echo "$root"
}

# daemon_fixture <name>: a fixture with testbuild and a daemon test that builds two envoy commands
# through it. Echoes its root.
daemon_fixture() {
  local root
  root=$(fixture "$1")
  printf 'package testbuild\n\nfunc EnvoyBinary(t any, pkg string) string { return pkg }\n' |
    put "$root" packages/daemon/internal/testbuild/testbuild.go
  put "$root" packages/daemon/internal/daemon/daemon_test.go <<'EOF'
package daemon

import (
	"testing"

	"example.com/daemon/internal/testbuild"
)

func TestBuildsTheCommands(t *testing.T) {
	_ = testbuild.EnvoyBinary(t, "./cmd/dispatch")
	_ = testbuild.EnvoyBinary(t, "./cmd/listener")
}
EOF
  mkdir -p "$root/packages/envoy"
  echo "$root"
}

# nightly <root>: a workflow outside the required set holding one job, from this harness's stdin
# (the job's steps).
nightly() {
  {
    printf 'name: Nightly\non: schedule\njobs:\n  proof:\n    runs-on: ubuntu-24.04\n    steps:\n'
    cat
  } | put "$1" .github/workflows/nightly.yaml
}

run_check() { # run_check <root>
  set +e
  out=$("$1/.github/scripts/check-go-offline.sh" 2>&1)
  status=$?
  set -e
}

refused() { # refused <path> <line>: the checks of a site refused with its annotation
  check "fails" "$(is "$status" 1)"
  check "names $1:$2" "$(contains "$out" "$1:$2: a go command with no GOPROXY=off and no go-offline reason")"
  check "annotates the line" "$(contains "$out" "::error file=$1,line=$2::")"
}

# refuses <name> <path> <line> [<sites>]: a fixture holding this harness's stdin at <path> is
# refused at <line> and nowhere else, over <sites> go commands (1 when not given).
refuses() {
  local root
  root=$(fixture "$1")
  put "$root" "$2"
  run_check "$root"
  refused "$2" "$3"
  check "refuses it alone, over ${4:-1} go commands" "$(contains "$out" "^1 problems over the ${4:-1} go commands")"
}

# passes_offline <name> <path>: a fixture holding this harness's stdin at <path> passes, its one go
# command read as offline.
passes_offline() {
  local root
  root=$(fixture "$1")
  put "$root" "$2"
  run_check "$root"
  check "exits 0" "$(is "$status" 0)"
  check "reads its one command as offline" "$(contains "$out" 'every one of the 1 go commands .* runs with GOPROXY=off (1)')"
}

# --- The cases, each file one part of the check ------------------------------------------------
# Each file this sources holds the cases of one part: rule 1's sites in each language and its file
# reasons, rule 2's steps and rule 3's restores. They share this file's fixtures and counters.
for part in go typescript python shell reasons steps restores; do
  # shellcheck source=/dev/null
  source "$script_dir/check-go-offline-cases-$part.sh"
done

# --- A scan that cannot see -----------------------------------------------------------------------

echo "case: a tree with no Python file gives UNKNOWN"
root=$(fixture blind-python)
rm "$root/tools/app.py"
run_check "$root"
check "exits 2" "$(is "$status" 2)"
check "names the language it did not read" "$(contains "$out" '^UNKNOWN: read no py file under ')"

echo "case: a tree with testbuild and no test that calls it gives UNKNOWN"
root=$(daemon_fixture blind-testbuild)
rm "$root/packages/daemon/internal/daemon/daemon_test.go"
run_check "$root"
check "exits 2" "$(is "$status" 2)"
check "names testbuild" "$(contains "$out" '^UNKNOWN: packages/daemon/internal/testbuild/testbuild.go exists and no daemon test calls testbuild.EnvoyBinary')"

echo "case: a tree whose tests call testbuild and no step runs them gives UNKNOWN"
root=$(daemon_fixture blind-jobs)
run_check "$root"
check "exits 2" "$(is "$status" 2)"
check "names the callers no step was seen to run" \
  "$(contains "$out" '^UNKNOWN: packages/daemon/internal/daemon call testbuild.EnvoyBinary and no step of any workflow was seen to run their tests')"

echo "case: a tree with no ci.yaml gives UNKNOWN"
root=$(fixture blind-ci)
rm "$root/.github/workflows/ci.yaml"
run_check "$root"
check "exits 2" "$(is "$status" 2)"
check "says no workflow is known to be required" "$(contains "$out" '^UNKNOWN: no .github/workflows/ci.yaml here')"

summary "check-go-offline.sh"
