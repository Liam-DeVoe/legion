#!/usr/bin/env bash
# Tests for check-log-guard.sh: a network client a step runs outside ci-log-guard.sh fails by name
# unless .github/log-guard-allow.tsv names it with a reason; a routed one passes; a row naming no
# command, or giving no reason, fails; and a tree with no workflow fails rather than passes.
#
# The first two cases run over a copy of this repository's own .github: unplanted it passes, and
# with one unguarded step planted it fails naming that step. The rest run over fixture trees.
#
# Run from anywhere: .github/scripts/check-log-guard.test.sh
# CI runs it in ci.yaml's test job.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

# run_check <root>: runs the check in <root>, leaving its output in $out and its exit in $status.
run_check() {
  status=0
  out=$("$1/.github/scripts/check-log-guard.sh" 2>&1) || status=$?
}

# fixture <name> <workflow run: body> [<allow rows>]: a tree holding the check, one workflow whose
# one step runs the body, and an allow list of the rows given.
fixture() {
  local root="$work/$1"
  mkdir -p "$root/.github/scripts" "$root/.github/workflows" "$root/.github/actions"
  ln -s "$script_dir/check-log-guard.sh" "$root/.github/scripts/check-log-guard.sh"
  ln -s "$script_dir/shell_commands.py" "$root/.github/scripts/shell_commands.py"
  {
    printf 'on: push\njobs:\n  app:\n    runs-on: ubuntu-24.04\n    steps:\n      - name: Fetch\n        run: |\n'
    # shellcheck disable=SC2001 # every line of a multi-line body
    sed 's/^/          /' <<<"$2"
  } >"$root/.github/workflows/app.yaml"
  printf '# rows\n%s' "${3:-}" >"$root/.github/log-guard-allow.tsv"
  echo "$root"
}

echo "case: this repository's own tree passes, and an unguarded step planted in it fails by name"
repo=$(cd "$script_dir/../.." && pwd)
mkdir -p "$work/repo"
cp -r "$repo/.github" "$work/repo/"
run_check "$work/repo"
check "the tree as it is passes" "$(is "$status" 0)"
check "and says how many commands it read" "$(contains "$out" 'unguarded network commands, every one on')"
cat >>"$work/repo/.github/workflows/ci-job-test.yaml" <<'YAML'
  planted:
    runs-on: ubuntu-24.04
    steps:
      - name: Planted test step
        run: go test ./...
YAML
run_check "$work/repo"
check "a planted go test outside the guard fails" "$(is "$status" 1)"
check "naming the file, the job, the step and the command" \
  "$(contains "$out" '::error file=.github/workflows/ci-job-test.yaml::job planted step "Planted test step" runs .go test ./....')"

echo "case: each routed form passes"
root=$(fixture routed "$(cat <<'SH'
. .github/scripts/ci-log-guard.sh
ci_log_guard go test ./...
GOOS=darwin ci_log_guard go vet ./...
v=$(ci_log_guard_stderr gh api repos/o/r)
set -o pipefail
{ go list ./... | xargs go test; } 2>&1 | ci_log_guard cat
bun install --frozen-lockfile
git status
echo "go test is what runs"
# curl https://example.com
SH
)")
run_check "$root"
check "passes" "$(is "$status" 0)"
check "and reads no command as unguarded" "$(contains "$out" ' 0 unguarded network commands')"

echo "case: each unguarded form fails by name"
root=$(fixture unrouted "$(cat <<'SH'
GOOS=linux go build ./cmd/x
v=$(gh api repos/o/r)
sudo apt-get install -y zsh
bunx playwright install chromium
git fetch origin main
sh -c 'curl -sS https://example.com'
docker pull ghcr.io/o/i
SH
)")
run_check "$root"
check "fails" "$(is "$status" 1)"
for found in 'go build ./cmd/x' 'gh api repos/o/r' 'sudo apt-get install -y zsh' 'bunx playwright install chromium' \
  'git fetch origin main' 'curl -sS https://example.com' 'docker pull ghcr.io/o/i'; do
  check "names \`$found\`" "$(contains "$out" "step \"Fetch\" runs \`$found\`")"
done
check "and counts seven problems" "$(contains "$out" '^::error::check-log-guard: 7 problem(s)$')"

# The forms a review found the check passed: a client behind a command that runs it with options
# of its own (their values must not be read as the client), behind find's -execdir, between two
# brace groups on one line, and a ci_log_guard_stderr whose stdout goes to the log.
echo "case: each client behind a runner, a find, between two groups, or a loose stderr guard fails by name"
root=$(fixture runners "$(cat <<'SH'
nice -n 10 go test ./...
timeout -s KILL 5m go test ./a
xargs -n 1 go test
sudo -u runner apt-get install -y zsh
env -i PATH=/usr/bin go build ./x
command go build ./y
find . -name go.mod -execdir go build ./... \;
{ true; }; curl -sS https://example.com/x; { true; } 2>&1 | ci_log_guard cat
ci_log_guard_stderr gh api repos/o/r
command -v go
SH
)")
run_check "$root"
check "fails" "$(is "$status" 1)"
for found in 'nice -n 10 go test ./...' 'timeout -s KILL 5m go test ./a' 'xargs -n 1 go test' \
  'sudo -u runner apt-get install -y zsh' 'env -i PATH=/usr/bin go build ./x' 'command go build ./y' \
  'find . -name go.mod -execdir go build ./... ;' 'curl -sS https://example.com/x'; do
  check "names \`$found\`" "$(contains "$out" "step \"Fetch\" runs \`$found\`")"
done
check "names the stderr guard whose stdout reaches the log" \
  "$(contains "$out" 'step "Fetch" runs .ci_log_guard_stderr gh api repos/o/r.: ci_log_guard_stderr blocks only stderr')"
check "reads \`command -v go\` as no client, and counts nine problems" \
  "$(contains "$out" '^::error::check-log-guard: 9 problem(s)$')"

# The second review's forms. A ci_log_guard_stderr is judged by its own command: a redirection of
# another command on its line, or a `$(` closed before it, does not capture its stdout. A shell's
# -c script behind a runner, gh-read.sh's gh_wait_reset and `time -p` are read through.
echo "case: a stderr guard judged by its own command, and a client behind a runner's shell, fail by name"
root=$(fixture own-command "$(cat <<'SH'
ci_log_guard_stderr gh api repos/o/a || { echo "::error::x" >&2; exit 1; }
ci_log_guard_stderr gh api repos/o/b; echo ok >&2
ci_log_guard_stderr gh api repos/o/c && echo done > out.txt
v=$(date); ci_log_guard_stderr gh api repos/o/d
x=$(ci_log_guard_stderr gh api repos/o/e); ci_log_guard_stderr gh api repos/o/f
timeout 600 bash -c 'go test ./a'
sudo sh -c 'apt-get install -y x'
xargs -I{} sh -c 'go test {}'
find . -exec sh -c 'go vet ./...' \;
time -p go test ./b
gh_wait_reset --stderr err.txt gh api repos/o/g
SH
)")
run_check "$root"
check "fails" "$(is "$status" 1)"
for found in 'gh api repos/o/a' 'gh api repos/o/b' 'gh api repos/o/c' 'gh api repos/o/d' 'gh api repos/o/f'; do
  check "names the uncaptured stderr guard of \`$found\`" \
    "$(contains "$out" "runs .[^\`]*ci_log_guard_stderr ${found}[^\`]*.: ci_log_guard_stderr blocks only stderr")"
done
check "reads the captured call of its line as captured" \
  "$(is "$(grep -c 'ci_log_guard_stderr blocks only stderr' <<<"$out")" 5)"
for found in "timeout 600 bash -c go test ./a" "sudo sh -c apt-get install -y x" "xargs -I{} sh -c go test {}" \
  "find . -exec sh -c go vet ./... ;" "time -p go test ./b" "gh_wait_reset --stderr err.txt gh api repos/o/g"; do
  check "names \`$found\`" "$(contains "$out" "step \"Fetch\" runs \`$found\`")"
done

echo "case: a stderr guard whose own command captures or redirects its stdout passes"
root=$(fixture own-command-safe "$(cat <<'SH'
ci_log_guard_stderr gh api repos/o/a >&2 || { echo "::error::x" >&2; exit 1; }
v=$(ci_log_guard_stderr gh api repos/o/b) || exit 1
ci_log_guard_stderr gh api repos/o/c > out.json 2>/dev/null
ci_log_guard_stderr gh api repos/o/d &> both.txt
ci_log_guard_stderr gh api repos/o/e 2>&1 >&2
{ ci_log_guard_stderr gh api repos/o/p; } > out.json
( ci_log_guard_stderr gh api repos/o/q ) > out.json
ci_log_guard_stderr gh api repos/o/r | jq . > out.json
ci_log_guard_stderr gh api repos/o/t | sort >> sorted.txt 2>/dev/null
v="$(ci_log_guard_stderr gh api repos/o/s)"
SH
)")
run_check "$root"
check "passes" "$(is "$status" 0)"

# The third review's forms: a capture opened inside double quotes closes at its own `)`, so a call
# after it on the line is no part of it; a quoted capture holding another, and one split across a
# backslash continuation, close the same way. A comment's `>` redirects nothing, and stdout sent
# into `>(…)` reaches the log through the command there.
echo "case: a call after a quoted capture, after a comment's >, or into >(…), fails by name"
root=$(fixture quoted-capture "$(cat <<'SH'
v="$(date)"; ci_log_guard_stderr gh api repos/o/h
[ -n "$(git rev-parse HEAD)" ] && ci_log_guard_stderr gh api repos/o/i
cd "$(pwd)" && ci_log_guard_stderr gh api repos/o/j
v="$(echo "$(date)")"; ci_log_guard_stderr gh api repos/o/k
v="$(printf '%s' \
  x)"; ci_log_guard_stderr gh api repos/o/l
ci_log_guard_stderr gh api repos/o/m  # stdout -> the log
ci_log_guard_stderr gh api repos/o/n > >(tee out.txt)
SH
)")
run_check "$root"
check "fails" "$(is "$status" 1)"
for found in h i j k l m n; do
  check "names the stderr guard of \`gh api repos/o/$found\`" \
    "$(contains "$out" "runs .[^\`]*ci_log_guard_stderr gh api repos/o/${found}[^\`]*.: ci_log_guard_stderr blocks only stderr")"
done
check "and counts seven problems" "$(contains "$out" '^::error::check-log-guard: 7 problem(s)$')"

# The fourth and fifth reviews' forms: after a pipe the last stage writes after the guard's block
# has closed, so only a file keeps that output out of the log; a stage naming /dev/stderr, a dup to
# a file descriptor and a pipe into ci_log_guard (whose block interleaves with this one under the
# runner's one stop state) all reach it. The shell applies redirections left to right, so the last
# one of stdout decides: a file followed by a dup or /dev/stderr reaches the log too.
echo "case: a call piped into a stage that writes to the log, or into ci_log_guard, fails by name"
root=$(fixture piped "$(cat <<'SH'
ci_log_guard_stderr gh api repos/o/u | sort >&2
ci_log_guard_stderr gh api repos/o/v | tail -n 20 >&2
ci_log_guard_stderr gh api repos/o/w | jq . 1>&2
ci_log_guard_stderr gh api repos/o/x | tee /dev/stderr > out.json
ci_log_guard_stderr gh api repos/o/y | ci_log_guard cat
ci_log_guard_stderr gh api repos/o/z | jq . > /dev/stdout
ci_log_guard_stderr gh api repos/o/A | sort > out.json >&2
ci_log_guard_stderr gh api repos/o/B | sort > out.json 1>&2
ci_log_guard_stderr gh api repos/o/C | sort > out.json > /dev/stderr
ci_log_guard_stderr gh api repos/o/D > out.json >&1
SH
)")
run_check "$root"
check "fails" "$(is "$status" 1)"
for found in u v w x y z A B C D; do
  check "names the stderr guard of \`gh api repos/o/$found\`" \
    "$(contains "$out" "runs .[^\`]*ci_log_guard_stderr gh api repos/o/${found}[^\`]*.: ci_log_guard_stderr blocks only stderr")"
done
check "and counts ten problems" "$(contains "$out" '^::error::check-log-guard: 10 problem(s)$')"

echo "case: the last redirection of stdout decides, so a file after a dup keeps the stdout"
root=$(fixture last-redirect "$(cat <<'SH'
ci_log_guard_stderr gh api repos/o/E | sort >&2 > out.json
ci_log_guard_stderr gh api repos/o/F >&2 > out.json
ci_log_guard_stderr gh api repos/o/G > /dev/stderr 2>/dev/null
SH
)")
run_check "$root"
check "passes" "$(is "$status" 0)"

echo "case: a row with its reason allows its command, and only that one"
root=$(fixture allowed 'docker pull ghcr.io/o/i' \
  "$(printf '.github/workflows/app.yaml\tapp\tFetch\tdocker\tthe image is pulled by digest and its text was probed')")
run_check "$root"
check "passes" "$(is "$status" 0)"

echo "case: a row with no reason fails"
root=$(fixture no-reason 'docker pull ghcr.io/o/i' "$(printf '.github/workflows/app.yaml\tapp\tFetch\tdocker\t')")
run_check "$root"
check "fails" "$(is "$status" 1)"
check "naming the row" "$(contains "$out" 'row 2 is not file, job, step, client and a reason')"

echo "case: a row naming no unguarded command fails"
root=$(fixture stale 'ci_log_guard docker pull ghcr.io/o/i' \
  "$(printf '.github/workflows/app.yaml\tapp\tFetch\tdocker\tpulled by digest')")
run_check "$root"
check "fails" "$(is "$status" 1)"
check "naming the row and the step" "$(contains "$out" 'row 2 names job app step "Fetch" of .github/workflows/app.yaml running docker unguarded, and no such step does')"

echo "case: a tree with no workflow fails rather than passes"
root=$(fixture empty 'true')
rm "$root/.github/workflows/app.yaml"
run_check "$root"
check "fails" "$(is "$status" 1)"
check "saying it checked nothing" "$(contains "$out" 'read no workflow file and no composite action')"

summary "check-log-guard.sh"
