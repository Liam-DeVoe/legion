#!/usr/bin/env bash
# Tests for `check-omp-offline.sh`: a Go test that starts Oh My Pi in a function that never calls
# testbin's offline helper is refused, by its door (testbin.OMP) and by its exec.Command alike, and
# so is a TypeScript start whose environment calls neither ompOffline nor ompEnv; the helper form
# and the reasoned form of each pass; a reason six lines up is too far; a start spelled in a
# comment or a string starts nothing; a regular expression holding a backtick hides no start after
# it; a start that reads LEGION_TEST_OMP itself is a start; a Sync start under the helper is
# refused, since the helper's listener cannot answer while the call holds the event loop; a helper
# that stopped naming HTTPS_PROXY fails every start it would pass; and a scan that read nothing,
# or found no start where testbin exists, reports UNKNOWN and exits 2. Last, the real repository
# passes over a count of starts it actually found.
#
# Each case builds a miniature tree under a temporary directory and symlinks the real script into
# it, so the script under test is the file CI runs: it resolves its root from its own path, which
# the symlink puts at the fixture root.
#
# Run from anywhere: .github/scripts/check-omp-offline.test.sh
# CI runs it in ci.yaml's test job.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
check_script="$script_dir/check-omp-offline.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

# empty <name>: a tree holding the script and nothing else. Echoes its root.
empty() {
  local root="$work/$1"
  if [ -e "$root" ]; then
    echo "  FAIL: the fixture name '$1' is already taken by an earlier case" >&2
    return 1
  fi
  mkdir -p "$root/.github/scripts"
  ln -s "$check_script" "$root/.github/scripts/check-omp-offline.sh"
  echo "$root"
}

# put <root> <path>: writes the file at <path> under <root> from this harness's stdin.
put() {
  mkdir -p "$(dirname "$1/$2")"
  cat > "$1/$2"
}

# fixture <name>: a tree with both helpers, as testbin and the harness hold them, one Go test that
# starts Oh My Pi under testbin.OMPEnviron (two starts: its door and its exec.Command) and the
# harness's own start under ompEnv (one). Echoes its root.
fixture() {
  local root
  root=$(empty "$1")
  put "$root" packages/daemon/internal/testbin/omp.go <<'EOF'
package testbin

import "testing"

func OMPEnviron(t *testing.T, home string) []string {
	return []string{"HOME=" + home, OMPOffline(t)}
}

func OMPOffline(t *testing.T) string {
	return "HTTPS_PROXY=http://127.0.0.1:1"
}
EOF
  put "$root" packages/daemon/internal/podsafety/helped_test.go <<'EOF'
package podsafety

func TestHelped(t *testing.T) {
	omp := testbin.OMP(t)
	cmd := exec.Command(omp, "models", "list")
	cmd.Env = testbin.OMPEnviron(t, t.TempDir())
}
EOF
  put "$root" packages/pi-shared/test/omp-offline.ts <<'EOF'
export function ompOffline(cleanup: (() => Promise<void>)[]): { readonly HTTPS_PROXY: string } {
  return { HTTPS_PROXY: "http://127.0.0.1:1" };
}
EOF
  put "$root" packages/pi-shared/test/omp-harness.ts <<'EOF'
import { ompOffline } from "./omp-offline";

function ompEnv(home: string, cleanup: (() => Promise<void>)[]): Record<string, string> {
  return { HOME: home, ...ompOffline(cleanup) };
}

export function spawnRpc(binary: string, home: string, cleanup: (() => Promise<void>)[]) {
  return Bun.spawn([binary, "--mode", "rpc"], {
    env: { ...ompEnv(home, cleanup) },
  });
}
EOF
  echo "$root"
}

run_check() { # run_check <root>
  set +e
  out=$("$1/.github/scripts/check-omp-offline.sh" 2>&1)
  status=$?
  set -e
}

echo "case: the helper form of each language passes, every start counted"
root=$(fixture helped)
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts the two Go starts and the TypeScript one" "$(contains "$out" '3 starts of Oh My Pi in 4 test files read (Go 2, TypeScript 1)')"
check "all three under the helper" "$(contains "$out" 'offline helper (3) or carries a reason (0)')"

echo "case: a Go test that starts Oh My Pi without the helper is refused, door and exec alike"
root=$(fixture go_bare)
put "$root" packages/daemon/internal/podsafety/bare_test.go <<'EOF'
package podsafety

func TestBare(t *testing.T) {
	omp := testbin.OMP(t)
	cmd := exec.Command(omp, "models", "list")
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin"}
}
EOF
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the door" "$(contains "$out" 'bare_test.go:4: a test takes the real Oh My Pi (testbin.OMP)')"
check "names the exec" "$(contains "$out" 'bare_test.go:5: a test starts Oh My Pi')"
check "annotates the line" "$(contains "$out" '::error file=packages/daemon/internal/podsafety/bare_test.go,line=4::')"
check "passes the helped test beside it" "$(is "$(contains "$out" 'helped_test.go')" false)"

echo "case: an exec of a field named omp, in a function apart from the door, is refused"
root=$(fixture go_field)
put "$root" packages/daemon/internal/daemon/live_test.go <<'EOF'
package daemon

func (r *live) paneOmp(args ...string) *exec.Cmd {
	cmd := exec.Command(r.omp, args...)
	cmd.Env = []string{"HOME=" + r.home}
	return cmd
}
EOF
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the exec" "$(contains "$out" 'live_test.go:4: a test starts Oh My Pi')"

echo "case: a read of LEGION_TEST_OMP through a constant is a door"
root=$(fixture go_getenv)
put "$root" packages/daemon/internal/daemon/vars.go <<'EOF'
package daemon

const ompVariable = "LEGION_TEST_OMP"
EOF
put "$root" packages/daemon/internal/daemon/getenv_test.go <<'EOF'
package daemon

func TestGetenv(t *testing.T) {
	binary := os.Getenv(ompVariable)
	run(binary)
}
EOF
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the read" "$(contains "$out" 'getenv_test.go:4: a test reads LEGION_TEST_OMP')"

echo "case: the reasoned form of a Go start passes"
root=$(fixture go_reasoned)
put "$root" packages/daemon/internal/daemon/install_test.go <<'EOF'
package daemon

func installPlugins(t *testing.T, omp string, environ []string) {
	// omp-offline: environ is the caller's, which testbin.OMPEnviron makes.
	install := exec.Command(omp, "plugin", "install", "dir")
	install.Env = environ
}
EOF
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts the reason" "$(contains "$out" 'offline helper (3) or carries a reason (1)')"

echo "case: a reason six lines up is too far"
root=$(fixture go_distant)
put "$root" packages/daemon/internal/daemon/distant_test.go <<'EOF'
package daemon

func installPlugins(t *testing.T, omp string, environ []string) {
	// omp-offline: environ is the caller's, which testbin.OMPEnviron makes.
	_ = 1
	_ = 2
	_ = 3
	_ = 4
	_ = 5
	install := exec.Command(omp, "plugin", "install", "dir")
	install.Env = environ
}
EOF
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the exec" "$(contains "$out" 'distant_test.go:10:')"

echo "case: a Go start spelled in a comment or a string starts nothing"
root=$(fixture go_prose)
put "$root" packages/daemon/internal/daemon/prose_test.go <<'EOF'
package daemon

// A test once ran omp := testbin.OMP(t) and exec.Command(omp, "models", "list") with no proxy.
func TestProse(t *testing.T) {
	message := `exec.Command(omp, "models") and testbin.OMP(t)`
	other := "exec.Command(omp, \"models\")"
	t.Log(message, other)
}
EOF
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts no start of its own" "$(contains "$out" '3 starts of Oh My Pi in 5 test files read')"

echo "case: a TypeScript start whose environment calls no helper is refused"
root=$(fixture ts_bare)
put "$root" packages/pi-envoy/extensions/bare-omp.test.ts <<'EOF'
const omp = process.env.LEGION_TEST_OMP;

test("models", async () => {
  const listed = Bun.spawn([omp!, "models", "list"], {
    env: { HOME: "/tmp/home", PATH: "/usr/bin:/bin" },
  });
  await listed.exited;
});
EOF
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the start" "$(contains "$out" 'bare-omp.test.ts:4: a start of the real Oh My Pi whose environment calls neither ompOffline nor ompEnv')"
check "annotates the line" "$(contains "$out" '::error file=packages/pi-envoy/extensions/bare-omp.test.ts,line=4::')"

echo "case: the helper form of a TypeScript start in a test passes"
root=$(fixture ts_helped)
put "$root" packages/pi-envoy/extensions/helped-omp.test.ts <<'EOF'
import { ompOffline } from "@legion/pi-shared/test/omp-offline";

test("models", async () => {
  const listed = Bun.spawn([binary, "models", "refresh"], {
    cwd: workspace,
    env: { HOME: home, PATH: "/usr/bin:/bin", ...ompOffline(cleanup) },
  });
  await listed.exited;
});
EOF
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts it under the helper" "$(contains "$out" 'offline helper (4) or carries a reason (0)')"

echo "case: the reasoned form of a TypeScript start passes"
root=$(fixture ts_reasoned)
put "$root" packages/pi-envoy/extensions/tmux-omp.test.ts <<'EOF'
async function tmuxCommand(binary: string, args: readonly string[], environment: Record<string, string>) {
  // omp-offline: `binary` is tmux; its server runs under paneEnvironment's, which holds the proxy.
  const child = Bun.spawn([binary, "-u", ...args], {
    env: environment,
  });
  return child.exited;
}
EOF
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts the reason" "$(contains "$out" 'offline helper (3) or carries a reason (1)')"

echo "case: a TypeScript start spelled in a comment or a string starts nothing"
root=$(fixture ts_prose)
put "$root" packages/pi-envoy/extensions/prose-omp.test.ts <<'EOF'
// Bun.spawn([omp, "models", "list"]) once ran here with no proxy.
const code = `const r = Bun.spawnSync([omp, "agents"]);`;
const text = 'Bun.spawn([binary, "models"])';
EOF
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "counts no start of its own" "$(contains "$out" '3 starts of Oh My Pi in 5 test files read')"

# A backtick inside a regular expression literal, read as the opening of a template, would blank
# every line up to the next backtick and hide whatever start sits there. The second line is the
# one that would: in the first, the quote before the backtick opens a string the line's end closes.
echo "case: a regular expression holding a backtick hides no start after it"
root=$(fixture ts_regex)
put "$root" packages/pi-envoy/extensions/regex-omp.test.ts <<'EOF'
const QUOTED = /["$`\\]/;
const TICKS = /`+/g;

test("models", async () => {
  const listed = Bun.spawn([omp, "models", "list"], {
    env: { HOME: home },
  });
  await listed.exited;
});
EOF
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the start" "$(contains "$out" 'regex-omp.test.ts:5: a start of the real Oh My Pi')"

echo "case: a Sync start under the helper is refused"
root=$(fixture ts_sync)
put "$root" packages/pi-shared/test/agents.ts <<'EOF'
import { ompOffline } from "./omp-offline";

export function bundledAgents(omp: string, cleanup: (() => Promise<void>)[]) {
  const unpacked = Bun.spawnSync([omp, "agents", "unpack"], {
    env: { HOME: "/tmp/home", ...ompOffline(cleanup) },
  });
  return unpacked.exitCode;
}
EOF
run_check "$root"
check "fails" "$(is "$status" 1)"
check "says why" "$(contains "$out" 'agents.ts:4: a Sync start of Oh My Pi under ompOffline.s proxy')"

echo "case: a helper that stopped naming HTTPS_PROXY fails"
root=$(fixture helper_bare)
put "$root" packages/daemon/internal/testbin/omp.go <<'EOF'
package testbin

import "testing"

func OMPEnviron(t *testing.T, home string) []string {
	return []string{"HOME=" + home, OMPOffline(t)}
}

func OMPOffline(t *testing.T) string {
	return "PATH=/usr/bin"
}
EOF
put "$root" packages/pi-shared/test/omp-harness.ts <<'EOF'
function ompEnv(home: string): Record<string, string> {
  return { HOME: home };
}

export function spawnRpc(binary: string, home: string) {
  return Bun.spawn([binary, "--mode", "rpc"], {
    env: { ...ompEnv(home) },
  });
}
EOF
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the Go helper" "$(contains "$out" 'omp.go:9: no Go OMPOffline that names HTTPS_PROXY')"
check "names the environment helper that calls no ompOffline" "$(contains "$out" 'omp-harness.ts:1: ompEnv does not call ompOffline')"

echo "case: an empty tree is UNKNOWN"
root=$(empty nothing)
run_check "$root"
check "exits 2" "$(is "$status" 2)"
check "says what it could not read" "$(contains "$out" 'UNKNOWN: read no Go and no TypeScript test file')"

echo "case: testbin with no start of Oh My Pi anywhere is UNKNOWN"
root=$(fixture no_start)
rm "$root/packages/daemon/internal/podsafety/helped_test.go"
run_check "$root"
check "exits 2" "$(is "$status" 2)"
check "says the reading sees none" "$(contains "$out" 'UNKNOWN: packages/daemon/internal/testbin exists and no Go test starts Oh My Pi')"

# The count matters as much as the exit status here: a script that resolved the wrong root would
# scan nothing and could not print this line, and one whose patterns went blind would print a
# count of zero.
echo "case: the real repository passes, over starts it actually found"
set +e
out=$("$check_script" 2>&1)
status=$?
set -e
check "exits 0" "$(is "$status" 0)"
check "starts of each language counted" "$(contains "$out" '(Go [1-9][0-9]*, TypeScript [1-9][0-9]*)')"

summary "check-omp-offline.sh"
