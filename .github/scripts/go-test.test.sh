#!/usr/bin/env bash
# Tests for `go-test.sh`, the script every Go step of a job ci-ok needs runs its tests through:
# gotestsum with the standard-verbose format and a jsonfile, the one retry a merge group takes,
# and the read of the jsonfile after gotestsum ends. Each case runs the real gotestsum and go over
# the fixture module .github/scripts/testdata/go-test/ (a go.mod of its own, so no module of this
# repository builds it), copied with the scripts that run go-test.sh - database-skips.sh,
# backend-count.sh and race_memory.py - into a repository laid out as this one is, under a
# temporary directory. A fixture that fails once keeps its count of attempts in a file under a
# directory this harness makes fresh for each case (testdata/go-test/state).
#
# The cases are those of the merge-group retry: a test that fails once and then passes, one that
# fails twice, a data race, a -timeout panic, 11 failures, a build failure, a line on go test's
# own standard error (and the compiler's -gcflags=-m output, which is none), three ordered
# subtests rerun whole, a test database-skips.sh runs and
# one backend-count.sh runs that each fail once, a TestMain that fails a rerun that runs no test,
# race_memory.py over its limit with every test passing, and no retry on a pull_request event or
# where a step turns it off. Beside each, where the retry or the read decides it, a copy of the
# script with that part taken out runs the same case and must fail it.
#
# With GO_TEST_CAPTURE set to a directory, it copies the jsonfiles and rerun reports of the
# once-then-pass, the twice and the TestMain cases there: .github/scripts/testdata/ci-ok-retries/
# holds them for ci-ok-retries.test.sh, whose README names the command.
#
# Needs go (with cgo, for the -race case), gotestsum, jq and python3, and fails naming any that is
# missing. Run from anywhere: .github/scripts/go-test.test.sh
# CI runs it in ci.yaml's scripts job.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd "$script_dir/../.." && pwd)

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

missing=()
for tool in go gotestsum jq python3; do
  command -v "$tool" >/dev/null 2>&1 || missing+=("$tool")
done
if [ "${#missing[@]}" -gt 0 ]; then
  check "go, gotestsum, jq and python3 are on PATH; missing: ${missing[*]}" false
  summary "go-test.sh"
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# The repository the cases run in: the scripts under test where this one keeps them, and the
# fixture module where packages/envoy is, which is where database-skips.sh and backend-count.sh
# run their packages from.
repo="$work/repo"
module="$repo/packages/envoy"
mkdir -p "$repo/.github/scripts" "$module/scripts"
cp "$script_dir/go-test.sh" "$script_dir/go-test.jq" "$script_dir/race_memory.py" "$repo/.github/scripts/"
cp -R "$script_dir/testdata/go-test/." "$module/"
cp "$repo_root/packages/envoy/scripts/database-skips.sh" "$repo_root/packages/envoy/scripts/backend-count.sh" "$module/scripts/"
go_test="$repo/.github/scripts/go-test.sh"
fixture=example.com/gotestfixtures
# The fixture module is its own module: no go.work around the temporary directory takes it in.
export GOWORK=off

# run_case <case> <event> <command...>: runs the command from the fixture module on a
# GITHUB_EVENT_NAME of <event>, with GO_TEST_RETRY unset, the case's own attempt counters and its
# own reports directory, and leaves its merged output in $out and its status in $status.
run_case() {
  local name=$1 event=$2
  shift 2
  mkdir -p "$work/state/$name"
  status=0
  out=$(cd "$module" && env -u GO_TEST_RETRY GITHUB_EVENT_NAME="$event" \
    GO_TEST_FIXTURE_STATE="$work/state/$name" GO_TEST_REPORTS="$work/reports/$name" "$@" 2>&1) || status=$?
}

# attempts <case> <counter>: how many times a fixture of the case ran, 0 where it never did.
attempts() { cat "$work/state/$1/$2" 2>/dev/null || echo 0; }

# report <case> <file>: a file go-test.sh left in the case's reports directory, or nothing.
report() { cat "$work/reports/$1/$2" 2>/dev/null || true; }

# variant <name> <script> <sed program> <what the copy lacks>: a copy of go-test.sh, go-test.jq and
# race_memory.py under $work/variants/<name>, with the sed program applied to <script> there. It
# checks the copy differs from the script, so a control whose program no longer matches the
# script fails here rather than passing on an unchanged copy.
variant() {
  mkdir -p "$work/variants/$1"
  cp "$repo/.github/scripts/go-test.sh" "$repo/.github/scripts/go-test.jq" "$repo/.github/scripts/race_memory.py" "$work/variants/$1/"
  sed -E "$3" "$repo/.github/scripts/$2" >"$work/variants/$1/$2"
  check "the copy of $2 $4 differs from it" "$(cmp -s "$work/variants/$1/$2" "$repo/.github/scripts/$2" && echo false || echo true)"
}

# counter_variant <name> <script>: a copy of database-skips.sh or backend-count.sh beside it that
# reads every attempt's result of a test, as the scripts read go test's one attempt before
# gotestsum, rather than the last attempt's.
counter_variant() {
  sed -E 's/last_attempt_results\(inputs\)/(inputs | select(.Action == "pass" or .Action == "fail" or .Action == "skip"))/g' \
    "$module/scripts/$2" >"$module/scripts/$1"
  chmod +x "$module/scripts/$1"
  check "the copy of $2 that reads every attempt differs from it" \
    "$(cmp -s "$module/scripts/$1" "$module/scripts/$2" && echo false || echo true)"
}

# ran_in_attempt <case> <jsonfile> <attempt>: the tests that passed in that attempt of the
# jsonfile's packages, sorted, one line.
ran_in_attempt() {
  jq -n -r -L "$repo/.github/scripts" --argjson attempt "$3" 'include "go-test";
    [with_attempts(inputs) | select(._attempt == $attempt and .Action == "pass" and .Test != null) | .Test]
    | sort | join(" ")' "$work/reports/$1/$2"
}

capture() {
  [ -n "${GO_TEST_CAPTURE:-}" ] || return 0
  mkdir -p "$GO_TEST_CAPTURE"
  cp "$work/reports/$1/$1.json" "$GO_TEST_CAPTURE/$1.json"
  cp "$work/reports/$1/$1.rerun-fails.txt" "$GO_TEST_CAPTURE/$1.rerun-fails.txt"
}

retry_flags='--rerun-fails=1 --rerun-fails-run-root-test --rerun-fails-abort-on-data-race --rerun-fails-report '

echo "case: a test that fails once and then passes passes its step in a merge group"
run_case once merge_group "$go_test" --jsonfile once.json --packages ./once/ -- -count=1
check "exits 0" "$(is "$status" 0)"
check "runs gotestsum with one retry, from the root test, aborted by a data race" "$(contains "$out" "$retry_flags")"
check "ran the test twice" "$(is "$(attempts once once)" 2)"
check "prints the --- PASS line of the test that passed the first time" "$(contains "$out" '^--- PASS: TestPassesEveryTime ')"
check "prints the --- PASS line of the retried test" "$(contains "$out" '^--- PASS: TestFailsOnceThenPasses ')"
check "its rerun report gives the test two runs and one failure" \
  "$(contains "$(report once once.rerun-fails.txt)" "^$fixture/once\.TestFailsOnceThenPasses: 2 runs, 1 failures\$")"
check "gotestsum's summary has no === Errors section" "$(is "$(contains "$out" '^=== Errors')" false)"
capture once

variant no-retry go-test.sh 's/^(  retry_flags=)\(--rerun-fails=1 .*\)$/\1()/' "with no retry flags"
run_case once-no-retry merge_group "$work/variants/no-retry/go-test.sh" --jsonfile once.json --packages ./once/ -- -count=1
check "a copy with no retry fails the step" "$(is "$status" 1)"
check "a copy with no retry runs the test once" "$(is "$(attempts once-no-retry once)" 1)"

variant default-format go-test.sh 's/ --format standard-verbose//' "with gotestsum's default format"
run_case once-default-format merge_group "$work/variants/default-format/go-test.sh" --jsonfile once.json --packages ./once/ -- -count=1
check "a copy with gotestsum's default format passes the step" "$(is "$status" 0)"
check "a copy with gotestsum's default format prints no --- PASS line by name" \
  "$(is "$(contains "$out" '--- PASS: TestPassesEveryTime')" false)"

echo "case: on a pull_request event a test that fails once fails its step"
run_case once-pull-request pull_request "$go_test" --jsonfile once.json --packages ./once/ -- -count=1
check "exits 1" "$(is "$status" 1)"
check "runs gotestsum with no --rerun-fails" "$(is "$(contains "$out" '--rerun-fails')" false)"
check "ran the test once" "$(is "$(attempts once-pull-request once)" 1)"
check "prints the --- FAIL line by name" "$(contains "$out" '^--- FAIL: TestFailsOnceThenPasses ')"

echo "case: GO_TEST_RETRY=off takes the retry away in a merge group, as a mutant's run does"
run_case once-off merge_group env GO_TEST_RETRY=off "$go_test" --jsonfile once.json --packages ./once/ -- -count=1
check "exits 1" "$(is "$status" 1)"
check "ran the test once" "$(is "$(attempts once-off once)" 1)"

echo "case: GO_TEST_RETRY=on gives a workflow_dispatch run the merge group's retry"
run_case once-on workflow_dispatch env GO_TEST_RETRY=on "$go_test" --jsonfile once.json --packages ./once/ -- -count=1
check "exits 0" "$(is "$status" 0)"
check "ran the test twice" "$(is "$(attempts once-on once)" 2)"

echo "case: a test that fails twice fails its step"
run_case twice merge_group "$go_test" --jsonfile twice.json --packages ./twice/ -- -count=1
check "exits 1" "$(is "$status" 1)"
check "ran the test twice" "$(is "$(attempts twice twice)" 2)"
check "its rerun report gives the test two runs and two failures" \
  "$(contains "$(report twice twice.rerun-fails.txt)" "^$fixture/twice\.TestFailsEveryAttempt: 2 runs, 2 failures\$")"
capture twice

echo "case: a test whose first run reports a data race is not run again"
run_case race merge_group env CGO_ENABLED=1 "$go_test" --jsonfile race.json --packages ./race/ -- -count=1 -race
check "fails its step" "$([ "$status" -ne 0 ] && echo true || echo false)"
check "ran the test once" "$(is "$(attempts race race)" 1)"
check "says the data race aborted the rerun" "$(contains "$out" 'rerun aborted because previous run had a data race')"
variant no-race-abort go-test.sh 's/ --rerun-fails-abort-on-data-race//' "without --rerun-fails-abort-on-data-race"
run_case race-no-abort merge_group env CGO_ENABLED=1 "$work/variants/no-race-abort/go-test.sh" --jsonfile race.json --packages ./race/ -- -count=1 -race
check "a copy without --rerun-fails-abort-on-data-race passes the step" "$(is "$status" 0)"
check "a copy without --rerun-fails-abort-on-data-race runs the test again" "$(is "$(attempts race-no-abort race)" 2)"

echo "case: a test that passes its -timeout is not run again"
run_case timeout merge_group "$go_test" --jsonfile timeout.json --packages ./timeout/ -- -count=1 -timeout 5s
check "fails its step" "$([ "$status" -ne 0 ] && echo true || echo false)"
check "ran the test once" "$(is "$(attempts timeout timeout)" 1)"
check "prints the timeout's panic" "$(contains "$out" 'panic: test timed out after 5s')"
check "says the panic aborted the rerun" "$(contains "$out" 'rerun aborted because previous run had a suspected panic')"

echo "case: a package with 11 tests that fail once is not run again"
run_case eleven merge_group "$go_test" --jsonfile eleven.json --packages ./eleven/ -- -count=1
check "fails its step" "$([ "$status" -ne 0 ] && echo true || echo false)"
check "ran the first test once" "$(is "$(attempts eleven TestFlake01)" 1)"
check "ran the eleventh test once" "$(is "$(attempts eleven TestFlake11)" 1)"
check "says 11 failures pass gotestsum's maximum of 10" "$(contains "$out" 'number of test failures (11) exceeds maximum (10)')"
run_case eleven-hundred merge_group "$go_test" --rerun-fails-max-failures 100 --jsonfile eleven.json --packages ./eleven/ -- -count=1
check "with --rerun-fails-max-failures 100 the step passes" "$(is "$status" 0)"
check "with --rerun-fails-max-failures 100 gotestsum is given it" "$(contains "$out" '--rerun-fails-max-failures=100')"
check "with --rerun-fails-max-failures 100 the eleventh test runs again" "$(is "$(attempts eleven-hundred TestFlake11)" 2)"

echo "case: a package that fails to build fails its step with no second run"
run_case build merge_group "$go_test" --jsonfile build.json --packages ./unbuildable/ -- -count=1
check "fails its step" "$([ "$status" -ne 0 ] && echo true || echo false)"
check "gotestsum lists the build output under === Errors" "$(contains "$out" '^=== Errors')"
check "says the errors aborted the rerun" "$(contains "$out" 'rerun aborted because previous run had errors')"
check "writes no rerun report, since no rerun ran" "$([ ! -e "$work/reports/build/build.rerun-fails.txt" ] && echo true || echo false)"
check "the jsonfile marks the package's failure as a build's" \
  "$(contains "$(jq -r "select(.Action == \"fail\" and .Package == \"$fixture/unbuildable\") | .FailedBuild // empty" "$work/reports/build/build.json")" .)"
check "go-test.sh does not take the build failure for a package that failed with no test failing" \
  "$(is "$(contains "$out" 'with no test failing')" false)"

echo "case: a line on go test's own standard error blocks the retry"
# gotestsum reruns nothing after a line on go test's standard error that none of its allowed
# prefixes starts (cmd/rerunfails.go, hasErrors; testjson/execution.go, readStderr), and lists
# the line under === Errors: which is why a GODEBUG that prints goes on the test binary
# (-exec 'env GODEBUG=…'), whose output go test -json carries as test output, and never on the go
# command itself. A pattern that matches no package makes go print one such line.
run_case stderr merge_group "$go_test" --jsonfile stderr.json --packages './once/ ./nogo/...' -- -count=1
check "fails its step" "$([ "$status" -ne 0 ] && echo true || echo false)"
check "gotestsum's summary shows an === Errors section" "$(contains "$out" '^=== Errors')"
check "the section holds go's line" "$(contains "$out" 'go: warning: "./nogo/..." matched no packages')"
check "ran the test that fails once only once" "$(is "$(attempts stderr once)" 1)"
# The compiler's output under -gcflags=-m is no such line: go test -json files it as one
# build-output event that begins with its `# <package>` header, which gotestsum's addError passes
# over, so the retry still runs.
run_case gcflags merge_group "$go_test" --jsonfile gcflags.json --packages ./once/ -- -count=1 -gcflags=-m
check "the compiler's -gcflags=-m output blocks no retry: the step passes" "$(is "$status" 0)"
check "and gotestsum's summary shows no === Errors section" "$(is "$(contains "$out" '^=== Errors')" false)"

echo "case: three ordered subtests whose second fails once pass on a rerun of the root test"
run_case ordered merge_group "$go_test" --jsonfile ordered.json --packages ./ordered/ -- -count=1
check "exits 0" "$(is "$status" 0)"
check "the second attempt ran the root test and all three subtests" \
  "$(is "$(ran_in_attempt ordered ordered.json 2)" "TestThreeOrderedSubtests TestThreeOrderedSubtests/first TestThreeOrderedSubtests/second TestThreeOrderedSubtests/third")"
variant subtest-only go-test.sh 's/ --rerun-fails-run-root-test//' "without --rerun-fails-run-root-test"
run_case ordered-subtest-only merge_group "$work/variants/subtest-only/go-test.sh" --jsonfile ordered.json --packages ./ordered/ -- -count=1
check "a copy that reruns the failed subtest alone passes the step" "$(is "$status" 0)"
check "a copy that reruns the failed subtest alone leaves the third subtest unrun in the second attempt" \
  "$(is "$(contains "$(ran_in_attempt ordered-subtest-only ordered.json 2)" 'TestThreeOrderedSubtests/third')" false)"

echo "case: a test database-skips.sh runs that fails once leaves its step passing"
run_case dbcount merge_group env FIXTURE_DATABASE_URL=set GITHUB_JOB=fixture \
  scripts/database-skips.sh --var FIXTURE_DATABASE_URL ./dbcount/
check "exits 0" "$(is "$status" 0)"
check "counts no skip" "$(contains "$out" '^database_skips=0$')"
check "ran the test twice" "$(is "$(attempts dbcount dbcount)" 2)"
check "leaves its jsonfile under its job's name" "$([ -s "$work/reports/dbcount/fixture-database-skips.json" ] && echo true || echo false)"
counter_variant database-skips-every-attempt.sh database-skips.sh
run_case dbcount-every merge_group env FIXTURE_DATABASE_URL=set \
  scripts/database-skips-every-attempt.sh --var FIXTURE_DATABASE_URL ./dbcount/
check "a copy of database-skips.sh that reads every attempt fails the step" "$(is "$status" 1)"

echo "case: a test backend-count.sh runs that fails once on one store leaves its step passing"
run_case stores merge_group scripts/backend-count.sh ./stores/ -- TestCountsOnEachStore
check "exits 0" "$(is "$status" 0)"
check "counts one pass on each store, the last attempt's" "$(contains "$out" '^TestCountsOnEachStore aws=1 gcp=1$')"
check "ran the gcp subtest twice" "$(is "$(attempts stores gcp)" 2)"
counter_variant backend-count-every-attempt.sh backend-count.sh
run_case stores-every merge_group scripts/backend-count-every-attempt.sh ./stores/ -- TestCountsOnEachStore
check "a copy of backend-count.sh that fails on any fail event fails the step" "$(is "$status" 1)"

echo "case: a TestMain that fails after its tests ran fails its step, though its rerun runs no test"
run_case testmain merge_group "$go_test" --jsonfile testmain.json --packages ./testmain/ -- -count=1
check "exits 1" "$(is "$status" 1)"
check "gotestsum itself exited 0, its rerun with -test.run=^\$ passing" "$(contains "$out" 'gotestsum exited 0, this step exits 1')"
check "names the package and the attempt" \
  "$(contains "$out" "$fixture/testmain failed in attempt 1 with no test failing")"
capture testmain
variant no-read go-test.sh 's/no_test_failures\(inputs\)/empty/' "that reads nothing of the jsonfile"
run_case testmain-no-read merge_group "$work/variants/no-read/go-test.sh" --jsonfile testmain.json --packages ./testmain/ -- -count=1
check "a copy that does not read the jsonfile passes the step" "$(is "$status" 0)"

echo "case: race_memory.py over its limit fails its step although every test passed"
run_case memory merge_group env RACE_MEM_LIMIT_KB=1 "$go_test" --race-memory --jsonfile memory.json --packages ./memory/ -- -count=1
check "exits 1" "$(is "$status" 1)"
check "the test passed" "$(contains "$out" '^--- PASS: TestHoldsMemoryWhileSampled ')"
check "a sample names the test running, read from gotestsum's jsonfile" \
  "$(contains "$out" '^race memory: at .*; tests running: TestHoldsMemoryWhileSampled$')"
check "says the peak passed the limit" "$(contains "$out" 'passed the limit of 1 kB')"
variant memory-exit-0 race_memory.py '/^    if peak_kb > limit_kb:$/{n;s/return 1/return 0/}' "that exits 0 whatever the peak"
run_case memory-exit-0 merge_group env RACE_MEM_LIMIT_KB=1 "$work/variants/memory-exit-0/go-test.sh" --race-memory --jsonfile memory.json --packages ./memory/ -- -count=1
check "a copy of race_memory.py that exits 0 whatever the peak passes the step" "$(is "$status" 0)"
variant memory-no-jsonfile race_memory.py 's/Jsonfile\(path, tests\)/Jsonfile(os.devnull, tests)/' "that reads no jsonfile"
run_case memory-no-jsonfile merge_group env RACE_MEM_LIMIT_KB=1 "$work/variants/memory-no-jsonfile/go-test.sh" --race-memory --jsonfile memory.json --packages ./memory/ -- -count=1
check "a copy of race_memory.py that reads no jsonfile names no test in its samples" \
  "$(is "$(contains "$out" 'tests running: TestHoldsMemoryWhileSampled')" false)"

echo "case: what go-test.sh refuses before it runs anything"
run_case refuse-v merge_group "$go_test" --jsonfile once.json --packages ./once/ -- -count=1 -v
check "a -v exits 2" "$(is "$status" 2)"
check "a -v is refused for taking test2json's framing away" "$(contains "$out" "takes test2json's framing away")"
check "a -v runs no test" "$(is "$(attempts refuse-v once)" 0)"
run_case refuse-packages merge_group "$go_test" --jsonfile once.json --packages ' ' -- -count=1
check "an empty --packages exits 2" "$(is "$status" 2)"
run_case refuse-name merge_group "$go_test" --jsonfile reports/once.json --packages ./once/ -- -count=1
check "a jsonfile name with a directory in it exits 2" "$(is "$status" 2)"

summary "go-test.sh"
