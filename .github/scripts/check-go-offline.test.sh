#!/usr/bin/env bash
# Tests for `check-go-offline.sh`, over trees it writes. Its first rule: a Go, a TypeScript, a
# Python and a shell test that each run `go list -deps` with no offline rule are refused and named
# with a GitHub annotation, and the offline form and the reasoned form of each pass, over a count
# that shows the scan saw the command; a reason six lines up is too far; an argv array and a shell
# command whose go names a directory with -C before its subcommand count too; a go command in a
# comment, a quoted string or a heredoc's body starts nothing. Its file reason: refused in a test
# file, and in a file ci.yaml or a workflow ci.yaml calls names, but not in one only a workflow
# outside that set or a comment names. Its second rule: a job of any workflow that runs
# internal/daemon's tests, which build envoy commands through testbuild.EnvoyBinary, is refused
# when no step before that one runs `.github/scripts/go-fetch.sh deps packages/envoy` of every such
# command - a step with none, one after, one that leaves a command out, one in another job - and
# passes when one does; the tests a step runs through a script and the library it sources count;
# a compile to /dev/null runs no test. A tree with no file of a language, a tree with testbuild and
# no caller, and a tree with no ci.yaml each give UNKNOWN and exit 2.
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

# --- The first rule, in each language ------------------------------------------------------------

echo "case: a Go test that runs go list -deps with no rule is refused, with an annotation"
root=$(fixture go-bare)
put "$root" packages/app/deps_test.go <<'EOF'
package app

import (
	"os/exec"
	"testing"
)

func TestDeps(t *testing.T) {
	if err := exec.Command("go", "list", "-deps", "./...").Run(); err != nil {
		t.Fatal(err)
	}
}
EOF
run_check "$root"
refused packages/app/deps_test.go 9
check "counts it against the total" "$(contains "$out" '1 problems over the 1 go commands')"

echo "case: the Go test with GOPROXY=off in the command's environment passes"
root=$(fixture go-offline)
put "$root" packages/app/deps_test.go <<'EOF'
package app

import (
	"os"
	"os/exec"
	"testing"
)

func TestDeps(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", "./...")
	cmd.Env = append(os.Environ(), "GOPROXY=off")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
}
EOF
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts the command as offline" "$(contains "$out" 'every one of the 1 go commands .* runs with GOPROXY=off (1), carries a reason (0)')"

echo "case: the Go test with a reason on the line above passes"
root=$(fixture go-reasoned)
put "$root" packages/app/deps_test.go <<'EOF'
package app

import (
	"os/exec"
	"testing"
)

func TestDeps(t *testing.T) {
	// go-offline: it lists the standard library, which reads no module.
	if err := exec.Command("go", "list", "-deps", "std").Run(); err != nil {
		t.Fatal(err)
	}
}
EOF
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts the command as reasoned" "$(contains "$out" 'runs with GOPROXY=off (0), carries a reason (1)')"

echo "case: a reason six lines above the Go command is too far"
root=$(fixture go-distant)
put "$root" packages/app/deps_test.go <<'EOF'
package app

import (
	"os/exec"
	"testing"
)

// go-offline: it lists the standard library, which reads no module.
func TestDeps(t *testing.T) {
	t.Helper()
	t.Parallel()
	t.Log("listing")
	t.Log("the standard library")
	if err := exec.Command("go", "list", "-deps", "std").Run(); err != nil {
		t.Fatal(err)
	}
}
EOF
run_check "$root"
refused packages/app/deps_test.go 14

echo "case: a TypeScript test that runs go list -deps with no rule is refused"
root=$(fixture ts-bare)
put "$root" packages/web/deps.test.ts <<'EOF'
import { expect, test } from "bun:test";

test("deps", () => {
  const listed = Bun.spawnSync(["go", "list", "-deps", "./..."]);
  expect(listed.exitCode).toBe(0);
});
EOF
run_check "$root"
refused packages/web/deps.test.ts 4

echo "case: the TypeScript test with GOPROXY: \"off\" in the spawn's environment passes"
root=$(fixture ts-offline)
put "$root" packages/web/deps.test.ts <<'EOF'
import { expect, test } from "bun:test";

test("deps", () => {
  const listed = Bun.spawnSync(["go", "list", "-deps", "./..."], {
    env: { ...process.env, GOPROXY: "off" },
  });
  expect(listed.exitCode).toBe(0);
});
EOF
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts the command as offline" "$(contains "$out" 'runs with GOPROXY=off (1), carries a reason (0)')"

echo "case: the TypeScript test with a reason on the line above passes"
root=$(fixture ts-reasoned)
put "$root" packages/web/deps.test.ts <<'EOF'
import { expect, test } from "bun:test";

test("deps", () => {
  // go-offline: it lists the standard library, which reads no module.
  const listed = Bun.spawnSync(["go", "list", "-deps", "std"]);
  expect(listed.exitCode).toBe(0);
});
EOF
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts the command as reasoned" "$(contains "$out" 'runs with GOPROXY=off (0), carries a reason (1)')"

echo "case: a TypeScript argv array with -C and a directory before its subcommand is a site"
root=$(fixture ts-chdir)
put "$root" packages/web/deps.test.ts <<'EOF'
import { expect, test } from "bun:test";

test("deps", () => {
  const argv = ["go", "-C", moduleDir, "list", "-deps", "./..."];
  expect(Bun.spawnSync(argv).exitCode).toBe(0);
});
EOF
run_check "$root"
refused packages/web/deps.test.ts 4

echo "case: a Python test that runs go list -deps with no rule is refused"
root=$(fixture py-bare)
put "$root" tools/test_deps.py <<'EOF'
import subprocess


def test_deps():
    subprocess.run(["go", "list", "-deps", "./..."], check=True)
EOF
run_check "$root"
refused tools/test_deps.py 5

echo "case: the Python test with \"GOPROXY\": \"off\" in the run's environment passes"
root=$(fixture py-offline)
put "$root" tools/test_deps.py <<'EOF'
import os
import subprocess


def test_deps():
    subprocess.run(["go", "list", "-deps", "./..."], check=True,
                   env={**os.environ, "GOPROXY": "off"})
EOF
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts the command as offline" "$(contains "$out" 'runs with GOPROXY=off (1), carries a reason (0)')"

echo "case: the Python test with a reason on the line above passes"
root=$(fixture py-reasoned)
put "$root" tools/test_deps.py <<'EOF'
import subprocess


def test_deps():
    # go-offline: it lists the standard library, which reads no module.
    subprocess.run(["go", "list", "-deps", "std"], check=True)
EOF
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts the command as reasoned" "$(contains "$out" 'runs with GOPROXY=off (0), carries a reason (1)')"

echo "case: a shell test that runs go list -deps with no rule is refused"
root=$(fixture sh-bare)
put "$root" tools/deps.test.sh <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
go list -deps ./... >/dev/null
EOF
run_check "$root"
refused tools/deps.test.sh 3

echo "case: the shell test with GOPROXY=off on the command passes, and so does an export of it above"
root=$(fixture sh-offline)
put "$root" tools/deps.test.sh <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
GOPROXY=off go list -deps ./... >/dev/null
EOF
put "$root" tools/exported.test.sh <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
export GOPROXY=off
go list -deps ./... >/dev/null
EOF
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts both commands as offline" "$(contains "$out" 'runs with GOPROXY=off (2), carries a reason (0)')"

echo "case: the shell test with a reason on the line above passes"
root=$(fixture sh-reasoned)
put "$root" tools/deps.test.sh <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
# go-offline: it lists the standard library, which reads no module.
go list -deps std >/dev/null
EOF
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts the command as reasoned" "$(contains "$out" 'runs with GOPROXY=off (0), carries a reason (1)')"

echo "case: a shell go -C with a quoted directory before its subcommand is a site"
root=$(fixture sh-chdir)
put "$root" tools/deps.test.sh <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
go -C "$root/packages/app" list -deps ./... >/dev/null
EOF
run_check "$root"
refused tools/deps.test.sh 3

echo "case: a go command in a comment, a quoted string or a heredoc's body starts nothing"
root=$(fixture sh-prose)
put "$root" tools/prose.test.sh <<'EOF'
#!/usr/bin/env bash
# go list -deps ./... in a comment starts nothing.
echo "go list -deps ./..."
cat <<'TEXT'
go list -deps ./...
TEXT
EOF
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts no command at all" "$(contains "$out" 'every one of the 0 go commands')"

# --- The file reason ------------------------------------------------------------------------------

echo "case: a go-offline-file reason in a test file is refused, and its command with it"
root=$(fixture file-reason-test)
put "$root" tools/deps.test.sh <<'EOF'
#!/usr/bin/env bash
# go-offline-file: a person runs this by hand.
go list -deps ./... >/dev/null
EOF
run_check "$root"
check "fails" "$(is "$status" 1)"
check "refuses the reason, saying why" \
  "$(contains "$out" 'tools/deps.test.sh:1: a go-offline-file reason, which this file may not carry: it is a test file')"
check "and refuses the command it would cover" "$(contains "$out" 'tools/deps.test.sh:3: a go command with no GOPROXY=off')"

echo "case: a go-offline-file reason in a file ci.yaml names is refused"
root=$(fixture file-reason-ci)
put "$root" tools/list-deps.sh <<'EOF'
#!/usr/bin/env bash
# go-offline-file: a person runs this by hand.
go list -deps ./... >/dev/null
EOF
printf '      - run: bash tools/list-deps.sh\n' >>"$root/.github/workflows/ci.yaml"
run_check "$root"
check "fails" "$(is "$status" 1)"
check "refuses the reason, naming ci.yaml" \
  "$(contains "$out" 'tools/list-deps.sh:1: a go-offline-file reason, which this file may not carry: a required workflow names it (.github/workflows/ci.yaml)')"
check "and refuses the command" "$(contains "$out" 'tools/list-deps.sh:3: a go command with no GOPROXY=off')"

echo "case: a go-offline-file reason in a file a workflow ci.yaml calls names is refused"
root=$(fixture file-reason-called)
put "$root" tools/list-deps.sh <<'EOF'
#!/usr/bin/env bash
# go-offline-file: a person runs this by hand.
go list -deps ./... >/dev/null
EOF
printf '  checks:\n    uses: ./.github/workflows/ci-job-checks.yaml\n' >>"$root/.github/workflows/ci.yaml"
put "$root" .github/workflows/ci-job-checks.yaml <<'EOF'
name: CI job checks
on: workflow_call
jobs:
  checks:
    runs-on: ubuntu-24.04
    steps:
      - run: tools/list-deps.sh
EOF
run_check "$root"
check "fails" "$(is "$status" 1)"
check "refuses the reason, naming the called workflow" \
  "$(contains "$out" 'a required workflow names it (.github/workflows/ci-job-checks.yaml)')"

echo "case: a go-offline-file reason in a file only a workflow outside the required set, and a comment of ci.yaml, name passes"
root=$(fixture file-reason-allowed)
put "$root" tools/list-deps.sh <<'EOF'
#!/usr/bin/env bash
# go-offline-file: a person runs this by hand.
go list -deps ./... >/dev/null
EOF
printf '      # tools/list-deps.sh is a person'"'"'s.\n' >>"$root/.github/workflows/ci.yaml"
nightly "$root" <<'EOF'
      - run: bash tools/list-deps.sh
EOF
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts the command as covered by its file's reason" "$(contains "$out" 'is in a file whose reason covers it (1)')"

# --- The second rule: a job that runs testbuild's tests fetches their commands first ---------------

echo "case: a job that runs internal/daemon's tests with no fetch step is refused"
root=$(daemon_fixture job-bare)
nightly "$root" <<'EOF'
      - name: Test the daemon
        run: go test ./internal/daemon/
        working-directory: packages/daemon
EOF
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the workflow, the job and the step" \
  "$(contains "$out" '.github/workflows/nightly.yaml:1: job proof, step "Test the daemon", runs the tests of packages/daemon/internal/daemon')"
check "names the fetch to add, with both commands" \
  "$(contains "$out" 'add `.github/scripts/go-fetch.sh deps packages/envoy ./cmd/dispatch ./cmd/listener` before it')"

echo "case: the same job with the fetch step before the tests passes"
root=$(daemon_fixture job-fetched)
nightly "$root" <<'EOF'
      - name: Fetch the modules of the commands the tests build
        run: .github/scripts/go-fetch.sh deps packages/envoy ./cmd/dispatch ./cmd/listener
      - name: Test the daemon
        run: go test ./internal/daemon/
        working-directory: packages/daemon
EOF
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts the step it judged" "$(contains "$out" "each of the 1 steps that run testbuild's tests comes after its fetch")"

echo "case: a fetch step after the tests is refused"
root=$(daemon_fixture job-late)
nightly "$root" <<'EOF'
      - name: Test the daemon
        run: go test ./internal/daemon/
        working-directory: packages/daemon
      - name: Fetch the modules of the commands the tests build
        run: .github/scripts/go-fetch.sh deps packages/envoy ./cmd/dispatch ./cmd/listener
EOF
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the step" "$(contains "$out" 'job proof, step "Test the daemon"')"

echo "case: a fetch step that leaves a command out is refused"
root=$(daemon_fixture job-partial)
nightly "$root" <<'EOF'
      - name: Fetch the modules of the Dispatch server
        run: .github/scripts/go-fetch.sh deps packages/envoy ./cmd/dispatch
      - name: Test the daemon
        run: go test ./internal/daemon/
        working-directory: packages/daemon
EOF
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the step" "$(contains "$out" 'job proof, step "Test the daemon"')"

echo "case: a fetch step in another job does not count"
root=$(daemon_fixture job-other)
nightly "$root" <<'EOF'
      - name: Test the daemon
        run: go test ./internal/daemon/
        working-directory: packages/daemon
  fetch:
    runs-on: ubuntu-24.04
    steps:
      - run: .github/scripts/go-fetch.sh deps packages/envoy ./cmd/dispatch ./cmd/listener
EOF
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the job without the fetch" "$(contains "$out" 'job proof, step "Test the daemon"')"

# As e2e.yaml's proof job runs them: the step runs an entry script, which sources the library that
# compiles the daemon's tests into a driver.
echo "case: a step that runs the tests through a script and the library it sources is refused, and passes after the fetch"
root=$(daemon_fixture job-script)
put "$root" scripts/e2e/proof.sh <<'EOF'
#!/usr/bin/env bash
root=$(cd "$(dirname "$0")/../.." && pwd)
. "$root/scripts/e2e/lib/proof.sh"
EOF
put "$root" scripts/e2e/lib/proof.sh <<'EOF'
# go-offline-file: a person runs the proof that sources this, and the nightly job.
go -C "$root/packages/daemon" test -c -o "$work/driver" ./internal/daemon
EOF
nightly "$root" <<'EOF'
      - name: Run the proof
        run: bash scripts/e2e/proof.sh
EOF
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the step that runs the script" "$(contains "$out" 'job proof, step "Run the proof", runs the tests of packages/daemon/internal/daemon')"
nightly "$root" <<'EOF'
      - run: .github/scripts/go-fetch.sh deps packages/envoy ./cmd/dispatch ./cmd/listener
      - name: Run the proof
        run: bash scripts/e2e/proof.sh
EOF
run_check "$root"
check "passes once the job fetches first" "$(is "$status" 0)"
check "counts the step it judged" "$(contains "$out" "each of the 1 steps that run testbuild's tests")"

# needs_fetch <name> <step name> <steps> [<script>]: a job of those steps alone is refused, naming
# the step, and the same job with the fetch step first passes, judging that one step. <script>,
# when given, is written to scripts/daemon-tests.sh in both trees.
needs_fetch() {
  local root fetch='      - run: .github/scripts/go-fetch.sh deps packages/envoy ./cmd/dispatch ./cmd/listener'
  root=$(daemon_fixture "$1")
  printf '%s\n' "$3" | nightly "$root"
  [ -z "${4:-}" ] || printf '%s\n' "$4" | put "$root" scripts/daemon-tests.sh
  run_check "$root"
  check "fails without the fetch" "$(is "$status" 1)"
  check "names the step" "$(contains "$out" "job proof, step \"$2\", runs the tests of packages/daemon/internal/daemon")"
  root=$(daemon_fixture "$1-fetched")
  printf '%s\n%s\n' "$fetch" "$3" | nightly "$root"
  [ -z "${4:-}" ] || printf '%s\n' "$4" | put "$root" scripts/daemon-tests.sh
  run_check "$root"
  check "passes with the fetch before it" "$(is "$status" 0)"
  check "judges the step" "$(contains "$out" "each of the 1 steps that run testbuild's tests")"
}

echo "case: a step that runs the tests after a cd on its own line is judged"
needs_fetch job-cd "Test the daemon after a cd" "$(cat <<'EOF'
      - name: Test the daemon after a cd
        run: cd packages/daemon && go test ./internal/daemon/
EOF
)"

echo "case: a step that runs the tests with go test -C is judged"
needs_fetch job-test-chdir "Test the daemon with go test -C" "$(cat <<'EOF'
      - name: Test the daemon with go test -C
        run: go test -C packages/daemon ./internal/daemon/
EOF
)"

echo "case: a step whose script runs the tests in a ( cd ... && go test ) group is judged"
needs_fetch job-script-cd "Run the tests of the daemon" "$(cat <<'EOF'
      - name: Run the tests of the daemon
        run: bash scripts/daemon-tests.sh
EOF
)" "$(cat <<'EOF'
#!/usr/bin/env bash
root=$(cd "$(dirname "$0")/.." && pwd)
(cd "$root/packages/daemon" && GOPROXY=off go test ./internal/daemon/)
EOF
)"

echo "case: a step that compiles the tests to /dev/null runs none and needs no fetch"
root=$(daemon_fixture job-compile)
nightly "$root" <<'EOF'
      - name: Compile the daemon's tests
        run: go test -c -o /dev/null ./internal/daemon/
        working-directory: packages/daemon
      - run: .github/scripts/go-fetch.sh deps packages/envoy ./cmd/dispatch ./cmd/listener
      - name: Test the daemon
        run: go test ./internal/daemon/
        working-directory: packages/daemon
EOF
run_check "$root"
check "exits 0, the compile before the fetch refused nothing" "$(is "$status" 0)"
check "judges the test step alone" "$(contains "$out" "each of the 1 steps that run testbuild's tests")"

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
