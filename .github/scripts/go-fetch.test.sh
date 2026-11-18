#!/usr/bin/env bash
# Tests for `go-fetch.sh`: what it runs for each mode, the retries that ride out a fault of the
# module proxy or the checksum database, and each input it refuses.
#
# Each case runs the real script as a subprocess under a stand-in `go` first on PATH, which
# records every call (its directory and arguments) and fails the calls a case asks it to fail, so
# what is tested is the commands a workflow step runs, with no network and no Go toolchain.
#
# Run from anywhere: .github/scripts/go-fetch.test.sh
# CI runs it in ci.yaml's test job.
set -euo pipefail

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
go_fetch="$script_dir/go-fetch.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

# The stand-in go: each call appends "<pwd> <args>" to $work/calls; a call whose arguments
# contain FAIL_MATCH fails while the count of such calls is at most FAIL_TIMES.
mkdir -p "$work/bin"
cat > "$work/bin/go" <<'GO'
#!/usr/bin/env bash
echo "$PWD $*" >> "$GO_FETCH_TEST_CALLS"
if [ -n "${FAIL_MATCH:-}" ] && [[ "$*" == *"$FAIL_MATCH"* ]]; then
  count=$(( $(cat "$GO_FETCH_TEST_COUNT" 2>/dev/null || echo 0) + 1 ))
  echo "$count" > "$GO_FETCH_TEST_COUNT"
  if [ "$count" -le "${FAIL_TIMES:-0}" ]; then
    echo "verifying module: reading https://sum.golang.org/tile/8/0/x160/632: stream error" >&2
    exit 1
  fi
fi
exit 0
GO
chmod +x "$work/bin/go"

# fetch <args...>: runs the script under test with the stand-in go and no waits. Leaves its
# stderr in `err`, its exit code in `rc` and the go calls in `calls`.
fetch() {
  rm -f "$work/calls" "$work/count"
  set +e
  PATH="$work/bin:$PATH" GO_FETCH_TEST_CALLS="$work/calls" GO_FETCH_TEST_COUNT="$work/count" \
    GO_FETCH_WAIT=0 "$go_fetch" "$@" > "$work/stdout" 2> "$work/stderr"
  rc=$?
  set -e
  err=$(cat "$work/stderr")
  calls=$(cat "$work/calls" 2>/dev/null || true)
}

cat > "$work/tools.txt" <<'TOOLS'
# a comment line, and a blank one below

example.com/a/cmd/alpha@v1.2.3
example.com/b/v2@v2.0.1   # a comment after a pin
TOOLS

echo "case: tools installs each pin of the file, in its order, and nothing else"
fetch tools "$work/tools.txt"
check "exits 0" "$(is "$rc" 0)"
check "installs the first pin" "$(contains "$calls" '^.* install example.com/a/cmd/alpha@v1.2.3$')"
check "installs the second pin, its comment left off" "$(contains "$calls" '^.* install example.com/b/v2@v2.0.1$')"
check "runs two installs and nothing else" "$(is "$(grep -c . <<< "$calls")" 2)"
check "in the file's order" "$(is "$(sed -n 1p <<< "$calls" | grep -c alpha)" 1)"

echo "case: a pin whose install fails twice is tried again and installed"
FAIL_MATCH=alpha FAIL_TIMES=2 fetch tools "$work/tools.txt"
check "exits 0" "$(is "$rc" 0)"
check "tries the failing pin three times" "$(is "$(grep -c alpha <<< "$calls")" 3)"
check "then installs the next pin" "$(contains "$calls" 'install example.com/b/v2@v2.0.1$')"
check "says it tries again" "$(contains "$err" 'failed; trying again')"
check "shows the go command's own error" "$(contains "$err" 'sum.golang.org')"

echo "case: a pin whose install fails every attempt fails the run, naming it"
FAIL_MATCH=alpha FAIL_TIMES=99 GO_FETCH_ATTEMPTS=3 fetch tools "$work/tools.txt"
check "exits 1" "$(is "$rc" 1)"
check "tries it GO_FETCH_ATTEMPTS times" "$(is "$(grep -c alpha <<< "$calls")" 3)"
check "installs no later pin" "$(is "$(grep -c 'b/v2' <<< "$calls")" 0)"
check "names the command that failed" "$(contains "$err" 'failed 3 times: go install example.com/a/cmd/alpha@v1.2.3')"

echo "case: deps downloads what a build of the packages needs, in the directory named"
mkdir -p "$work/module"
fetch deps "$work/module" ./cmd/one ./cmd/two
check "exits 0" "$(is "$rc" 0)"
check "lists the packages' dependencies in that directory" \
  "$(is "$calls" "$work/module list -deps ./cmd/one ./cmd/two")"

echo "case: deps is tried again too"
FAIL_MATCH=list FAIL_TIMES=1 fetch deps "$work/module" ./cmd/one
check "exits 0" "$(is "$rc" 0)"
check "runs the listing twice" "$(is "$(grep -c 'list -deps' <<< "$calls")" 2)"

echo "case: a line that is no pin refuses the file before anything is installed"
printf 'example.com/a/cmd/alpha@v1.2.3\nexample.com/c/cmd/gamma@latest\n' > "$work/bad.txt"
fetch tools "$work/bad.txt"
check "exits 2" "$(is "$rc" 2)"
check "names the file, the line and the text" "$(contains "$err" "$work/bad.txt:2: example.com/c/cmd/gamma@latest")"
check "installs nothing" "$(is "$calls" '')"

echo "case: an empty list refuses rather than installing nothing"
printf '# nothing here\n' > "$work/empty.txt"
fetch tools "$work/empty.txt"
check "exits 2" "$(is "$rc" 2)"
check "says the file pins nothing" "$(contains "$err" 'pins no tool')"

echo "case: usage errors"
fetch
check "no mode exits 2" "$(is "$rc" 2)"
fetch download "$work/tools.txt"
check "an unknown mode exits 2" "$(is "$rc" 2)"
fetch tools "$work/absent.txt"
check "a missing file exits 2" "$(is "$rc" 2)"
fetch deps "$work/module"
check "deps with no package exits 2" "$(is "$rc" 2)"
fetch deps "$work/absent-dir" ./cmd/one
check "deps in a missing directory exits 2" "$(is "$rc" 2)"
check "and runs nothing" "$(is "$calls" '')"

summary "go-fetch.sh installs each pinned tool and downloads each build's modules, retrying each go command, and refuses every malformed input"
