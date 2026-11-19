#!/usr/bin/env bash
# Tests for `private_proc.py`: every route a test reads the processes by sees its own namespace's
# alone, which the same test run without the script does not; the test binary runs beneath a
# supervisor at PID 1, as its own user, in its own directory, with its environment; the script
# lists the tests that read a process table, each with the root it read, from the record each
# binary that links its module's internal/hostproc writes; and it says UNKNOWN, and fails, for a
# run in which no test binary ran in a namespace of its own, a list with no test in it, a package
# with tests that ran in none while the others did, and a binary that links internal/hostproc and
# recorded nothing. It also fails a namespace it could not make, a supervisor that finds itself in
# no namespace of its own, and a command that is no go test or that sets -exec itself.
#
# The cases run a real `go test` over two scratch modules written here. The first's test reads
# /proc by five routes: a direct directory read, os.DirFS, a joined glob, an opened directory
# handed to a helper, and pgrep. Each must find a process the test started and must not find one
# this harness started on the machine before the run; the two are told apart by a marker in their
# command line, never by a pid, which two namespaces can share. The second module's
# internal/hostproc stands in for packages/daemon's: it writes the record's lines as that one does
# (hostproc.RecordEnv), so the list and its UNKNOWNs are read from a binary that links it, one that
# links it and scans nothing, and one that does not link it. The script needs `sudo -n`, which CI's
# runners give, and a go on PATH.
#
# Run from anywhere: .github/scripts/private_proc.test.sh
# CI runs it in ci.yaml's test job.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
wrapper="$script_dir/private_proc.py"

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

work=$(mktemp -d)
outside=""
cleanup() {
  if [ -n "$outside" ]; then kill "$outside" 2>/dev/null || true; fi
  rm -rf "$work"
}
trap cleanup EXIT

if ! sudo -n true 2>/dev/null; then
  echo "private_proc.test.sh needs sudo -n, which makes the namespaces it tests" >&2
  exit 1
fi

# The scratch module: one package with the routes' test, and one with no test at all.
module="$work/routes"
mkdir -p "$module/notests"
printf 'module routes\n\ngo 1.22\n' > "$module/go.mod"
printf 'package notests\n\n// Nothing here is tested.\nconst Nothing = 0\n' > "$module/notests/notests.go"
cat > "$module/routes_test.go" <<'GO'
package routes

import (
	"crypto/rand"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// cmdlines is the command line of every process names lists, read under root by read.
func cmdlines(names []string, read func(string) ([]byte, error)) []string {
	var out []string
	for _, name := range names {
		if _, err := strconv.Atoi(name); err != nil {
			continue
		}
		if body, err := read(filepath.Join(name, "cmdline")); err == nil {
			out = append(out, strings.ReplaceAll(string(body), "\x00", " "))
		}
	}
	return out
}

// namesOf is the helper an opened directory is handed to.
func namesOf(dir *os.File) []string {
	names, _ := dir.Readdirnames(-1)
	return names
}

func TestEveryRouteSeesThisNamespaceAlone(t *testing.T) {
	if os.Getenv("ROUTES_FAIL") == "1" {
		t.Fatal("the harness asked this run to fail")
	}
	outside := os.Getenv("ROUTES_OUTSIDE_MARKER")
	if outside == "" {
		t.Fatal("ROUTES_OUTSIDE_MARKER names no marker of a process outside")
	}
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	inside := "inside-marker-" + hex.EncodeToString(random)
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	child := &exec.Cmd{Path: sleep, Args: []string{inside, "60"}}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if body, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(child.Process.Pid), "cmdline")); err == nil && strings.Contains(string(body), inside) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the inside marker's command line never showed")
		}
	}
	rooted := func(name string) ([]byte, error) { return os.ReadFile(filepath.Join("/proc", name)) }
	routes := map[string]func() []string{
		"a direct directory read": func() []string {
			entries, _ := os.ReadDir("/proc")
			var names []string
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			return cmdlines(names, rooted)
		},
		"os.DirFS": func() []string {
			fsys := os.DirFS("/proc")
			entries, _ := fs.ReadDir(fsys, ".")
			var names []string
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			return cmdlines(names, func(name string) ([]byte, error) { return fs.ReadFile(fsys, name) })
		},
		"a joined glob": func() []string {
			paths, _ := filepath.Glob(filepath.Join("/proc", "[0-9]*", "cmdline"))
			var out []string
			for _, path := range paths {
				if body, err := os.ReadFile(path); err == nil {
					out = append(out, strings.ReplaceAll(string(body), "\x00", " "))
				}
			}
			return out
		},
		"an opened directory handed to a helper": func() []string {
			dir, err := os.Open("/proc")
			if err != nil {
				return nil
			}
			defer dir.Close()
			return cmdlines(namesOf(dir), rooted)
		},
		"pgrep": func() []string {
			out, _ := exec.Command("pgrep", "-a", "-f", ".").Output()
			return strings.Split(string(out), "\n")
		},
	}
	names := make([]string, 0, len(routes))
	for name := range routes {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		seen := strings.Join(routes[name](), "\n")
		if !strings.Contains(seen, inside) {
			t.Errorf("%s does not find the inside marker, a process this test started", name)
		}
		if strings.Contains(seen, outside) {
			t.Errorf("%s finds the outside marker, a process of the machine this test did not start", name)
		}
	}
}

func TestTheBinaryRunsBeneathASupervisor(t *testing.T) {
	if pid := os.Getpid(); pid == 1 {
		t.Errorf("the test binary is PID 1, want a supervisor there")
	}
	if parent := os.Getppid(); parent != 1 {
		t.Errorf("the test binary's parent is pid %d, want the supervisor at PID 1", parent)
	}
	if uid := strconv.Itoa(os.Getuid()); uid != os.Getenv("ROUTES_UID") {
		t.Errorf("the test binary runs as uid %s, want the user's %s", uid, os.Getenv("ROUTES_UID"))
	}
	groups, _ := os.Getgroups()
	slices.Sort(groups)
	var got []string
	for _, group := range groups {
		got = append(got, strconv.Itoa(group))
	}
	if strings.Join(got, ",") != os.Getenv("ROUTES_GROUPS") {
		t.Errorf("the test binary holds groups %v, want the user's %s", got, os.Getenv("ROUTES_GROUPS"))
	}
	if dir, _ := os.Getwd(); dir != os.Getenv("ROUTES_DIR") {
		t.Errorf("the test binary runs in %s, want its package's directory %s", dir, os.Getenv("ROUTES_DIR"))
	}
}
GO

marker="outside-marker-$$-$RANDOM$RANDOM"
bash -c "exec -a $marker sleep 600" > /dev/null 2>&1 &
outside=$!
for _ in $(seq 50); do
  if tr '\0' ' ' < "/proc/$outside/cmdline" 2>/dev/null | grep -q -- "$marker"; then break; fi
  sleep 0.1
done
uid=$(id -u)
groups=$(id -G | tr ' ' '\n' | sort -n | paste -sd,)

# run <command...>: runs it in the scratch module with the routes' environment, and leaves its
# merged output in $out and its status in $status.
run() {
  set +e
  out=$(cd "$module" && env ROUTES_OUTSIDE_MARKER="$marker" ROUTES_UID="$uid" ROUTES_GROUPS="$groups" \
    ROUTES_DIR="$(cd "$module" && pwd -P)" "$@" 2>&1)
  status=$?
  set -e
}

echo "case: every route sees the namespace's processes and none of the machine's"
run "$wrapper" go test -count=1 -v ./...
check "exits 0" "$(is "$status" 0)"
check "the routes' test passes" "$(contains "$out" '^--- PASS: TestEveryRouteSeesThisNamespaceAlone')"
check "the supervisor's test passes" "$(contains "$out" '^--- PASS: TestTheBinaryRunsBeneathASupervisor')"
check "says the one binary ran beneath PID 1 of a namespace of its own" \
  "$(contains "$out" 'private-proc: 1 test binaries ran, each beneath PID 1 of a PID namespace of its own, of the 1 packages with tests')"

echo "case: the control, the same test without the script, sees the machine"
run go test -count=1 ./...
check "exits non-zero" "$(is "$([ "$status" -ne 0 ] && echo yes)" yes)"
for route in 'a direct directory read' 'os.DirFS' 'a joined glob' 'an opened directory handed to a helper' 'pgrep'; do
  check "$route finds the outside marker" "$(contains "$out" "$route finds the outside marker")"
done
check "the binary's parent is no supervisor" "$(contains "$out" "the test binary's parent is pid")"

echo "case: a failing test's status passes through"
run env ROUTES_FAIL=1 "$wrapper" go test -count=1 ./...
check "exits 1" "$(is "$status" 1)"
check "go names the failure" "$(contains "$out" 'the harness asked this run to fail')"

echo "case: a record in a sticky directory others can write, as a runner's /tmp, still takes the binary"
# The supervisor is root and the record is the user's; where fs.protected_regular is set, Linux
# refuses root an O_CREAT open of another user's file in such a directory, which a runner's TMPDIR
# is and this machine's may not be.
run env TMPDIR=/tmp "$wrapper" go test -count=1 ./...
check "exits 0" "$(is "$status" 0)"
check "says the one binary ran beneath PID 1 of a namespace of its own" \
  "$(contains "$out" 'private-proc: 1 test binaries ran, each beneath PID 1 of a PID namespace of its own, of the 1 packages with tests')"
check "the supervisor wrote its record" "$(is "$(printf '%s\n' "$out" | grep -c 'PermissionError')" 0)"

echo "case: a go test that ran no test binary says UNKNOWN and fails"
# go test -c compiles the binary and runs nothing, and exits 0: the record then holds no package,
# which is what the script must catch. (go never answers a run from its cache while -exec is set.)
run "$wrapper" go test -c -o "$work/routes.test" .
check "exits 1" "$(is "$status" 1)"
check "the binary was built" "$(is "$([ -x "$work/routes.test" ] && echo built)" built)"
check "says UNKNOWN: nothing ran" "$(contains "$out" 'private-proc: UNKNOWN: no test binary ran in a namespace of its own, so nothing was read')"
check "prints no list" "$(is "$(contains "$out" 'the tests that read a process table')" false)"

echo "case: a list with no test in it says UNKNOWN and fails"
run "$wrapper" go test -count=1 ./notests/
check "exits 1" "$(is "$status" 1)"
check "says UNKNOWN: the list runs nothing" "$(contains "$out" 'private-proc: UNKNOWN: no package of ./notests/ has a test file, so no test ran and nothing was read')"

echo "case: a namespace that cannot be made fails the binary, and leaves no environment behind"
mkdir -p "$work/bin" "$work/tmp"
printf '#!/bin/sh\necho "sudo: a password is required" >&2\nexit 1\n' > "$work/bin/sudo"
chmod +x "$work/bin/sudo"
run env PATH="$work/bin:$PATH" TMPDIR="$work/tmp" "$wrapper" go test -count=1 ./...
check "exits non-zero" "$(is "$([ "$status" -ne 0 ] && echo yes)" yes)"
check "says the namespace was not made" "$(contains "$out" 'was not made (sudo exited 1); nothing ran on the machine')"
check "no file of the environment is left" "$(is "$(find "$work/tmp" -name 'private-proc-env-*' | wc -l)" 0)"

echo "case: a supervisor that finds itself in no namespace of its own refuses"
printf 'X=1' > "$work/env"
set +e
out=$("$wrapper" --init "$work/env" "$uid" "$(id -g)" "$groups" "$work" "$work/record" -- /bin/true 2>&1)
status=$?
set -e
check "exits 125" "$(is "$status" 125)"
check "says it is no PID 1" "$(contains "$out" 'no namespace of its own for /bin/true: the supervisor is pid')"
check "starts nothing, and records nothing" "$(is "$([ -e "$work/record" ] && echo recorded || echo none)" none)"

echo "case: a command that is no go test, or that sets -exec, is refused"
run "$wrapper" go vet ./...
check "go vet exits 2" "$(is "$status" 2)"
run "$wrapper" go test -exec /bin/env ./...
check "a -exec of its own exits 2" "$(is "$status" 2)"
check "says why" "$(contains "$out" 'sets -exec itself, which would run its test binaries outside a namespace of their own')"

# The second module: a stand-in for packages/daemon's internal/hostproc, which writes the record's
# lines as that package does, and three packages with tests: reads, whose test records two scans
# of its own (and, with SCANS_REFUSE=1, a refusal, and ends its binary as a refusal does); quiet,
# which links the stand-in and scans nothing; and plain, which does not link it.
scans="$work/scans"
mkdir -p "$scans/internal/hostproc" "$scans/reads" "$scans/quiet" "$scans/plain"
printf 'module scans\n\ngo 1.22\n' > "$scans/go.mod"
cat > "$scans/internal/hostproc/hostproc.go" <<'GO'
// Package hostproc stands in for packages/daemon's internal/hostproc: it writes the lines of the
// record HOSTPROC_RECORD names as that package does, a binary line as the binary starts and a
// scan line for each Scan, and nothing at all with SCANS_SILENT=1.
package hostproc

import (
	"os"
	"strconv"
	"strings"
)

func write(fields ...string) {
	path := os.Getenv("HOSTPROC_RECORD")
	if path == "" || os.Getenv("SCANS_SILENT") == "1" {
		return
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		panic(err)
	}
	defer file.Close()
	if _, err := file.WriteString(strings.Join(fields, "\t") + "\n"); err != nil {
		panic(err)
	}
}

func init() { write("binary", strconv.Itoa(os.Getpid())) }

// Scan records one scan of test's.
func Scan(kind, test, scanner, root, reason string) { write("scan", kind, test, scanner, root, reason) }
GO
cat > "$scans/reads/reads_test.go" <<'GO'
package reads

import (
	"os"
	"testing"

	"scans/internal/hostproc"
)

func TestReadsARootOfItsOwn(t *testing.T) {
	hostproc.Scan("own", "TestReadsARootOfItsOwn", "the stand-in's scanner", "/fixture/proc", "")
	hostproc.Scan("reason", "TestReadsARootOfItsOwn", "the reasoned stand-in", "/proc", "it takes only its own pid")
	if os.Getenv("SCANS_REFUSE") == "1" {
		hostproc.Scan("refused", "TestReadsARootOfItsOwn", "the refused stand-in", "/proc", "")
		os.Exit(86)
	}
}
GO
printf 'package quiet\n\nimport (\n\t"testing"\n\n\t_ "scans/internal/hostproc"\n)\n\nfunc TestScansNothing(t *testing.T) {}\n' > "$scans/quiet/quiet_test.go"
printf 'package plain\n\nimport "testing"\n\nfunc TestLinksNoScanner(t *testing.T) {}\n' > "$scans/plain/plain_test.go"

# run_scans <command...>: run, in the second module.
run_scans() {
  set +e
  out=$(cd "$scans" && "$@" 2>&1)
  status=$?
  set -e
}

echo "case: the list names each test that read a process table, with the root it read, and no UNKNOWN"
run_scans "$wrapper" go test -count=1 ./...
check "exits 0" "$(is "$status" 0)"
check "names the package whose test read one" "$(contains "$out" '^private-proc:   reads: 1 tests read a process table$')"
check "names its own root" "$(contains "$out" "^private-proc:     TestReadsARootOfItsOwn: .*the stand-in's scanner, own /fixture/proc")"
check "names the host's, with its reason" "$(contains "$out" "^private-proc:     TestReadsARootOfItsOwn: the reasoned stand-in, host /proc, by its reason: it takes only its own pid")"
check "says the package that scanned nothing read none" "$(contains "$out" '^private-proc:   quiet: no test read a process table$')"
check "says the package with no scanner has none" "$(contains "$out" '^private-proc:   plain: no scanner of the module is in its test binary (it does not link internal/hostproc)$')"
check "says no UNKNOWN" "$(is "$(contains "$out" 'UNKNOWN')" false)"
check "says the three binaries ran" "$(contains "$out" 'private-proc: 3 test binaries ran, each beneath PID 1 of a PID namespace of its own, of the 3 packages with tests')"

echo "case: a refusal is named in the list, and the binary's failure is the step's"
run_scans env SCANS_REFUSE=1 "$wrapper" go test -count=1 ./...
check "exits 1, go's own status" "$(is "$status" 1)"
check "names the refusal and the binary's exit" "$(contains "$out" "refused: the refused stand-in, host /proc, with no reason (the package's binary exited 86)")"

echo "case: binaries that link internal/hostproc and recorded nothing say UNKNOWN and fail"
run_scans env SCANS_SILENT=1 "$wrapper" go test -count=1 ./...
check "exits 1" "$(is "$status" 1)"
check "names both binaries" "$(contains "$out" 'private-proc: UNKNOWN: the test binary of quiet, reads links internal/hostproc and recorded nothing, so what its tests read is not known')"
check "prints no success line" "$(is "$(contains "$out" 'test binaries ran, each beneath PID 1')" false)"

echo "case: a package that ran outside the namespace while the others ran in theirs says UNKNOWN and fails"
# A stand-in for sudo runs plain's binary on the machine, outside the namespace and so outside the
# supervisor's record, and removes the environment file as the supervisor would, so its wrapper
# takes it for a binary that ran; every other binary goes to the real sudo. go exits 0.
real_sudo=$(command -v sudo)
mkdir -p "$work/escape"
cat > "$work/escape/sudo" <<SH
#!/usr/bin/env bash
args=("\$@")
for i in "\${!args[@]}"; do [ "\${args[\$i]}" = --init ] && at=\$i; done
if [ "\${args[at+5]##*/}" = plain ]; then
  rm -f "\${args[at+1]}"
  cd "\${args[at+5]}" && exec "\${args[@]:at+8}"
fi
exec $real_sudo "\$@"
SH
chmod +x "$work/escape/sudo"
run_scans env PATH="$work/escape:$PATH" "$wrapper" go test -count=1 ./...
check "exits 1" "$(is "$status" 1)"
check "names the package that ran in no namespace" "$(contains "$out" 'private-proc: UNKNOWN: 1 of the 3 packages with tests ran in no namespace of their own, so what their tests read is not known: plain')"
check "prints no success line" "$(is "$(contains "$out" 'test binaries ran, each beneath PID 1')" false)"

summary "private_proc.py"
