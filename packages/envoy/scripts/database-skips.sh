#!/usr/bin/env bash
# packages/envoy/scripts/database-skips.sh
#
# The one skip counter of the envoy module's database-backed packages. go test counts a skipped
# test as a pass and, without -v, prints nothing for it, so a package run with its database
# variable unset, or with a t.Skip left in one of its tests, reads as green. This script runs
# `go test -json` and counts the skips in that report, and fails on any.
#
#   database-skips.sh [--var NAME]... [--race] [--timeout D] [--run PATTERN] [--skip PATTERN] [PACKAGE...]
#   database-skips.sh --report FILE [--run PATTERN] [PACKAGE...]
#
# PACKAGE is a pattern under packages/envoy: `./dir/`, that package alone, or `./dir/...`, it
# and every package under it. With none it counts DEFAULT_PACKAGES below, the broker's packages
# and its command, which need BROKER_TEST_DATABASE_URL, less the packages DEFAULT_EXCEPT takes out
# of them. From packages/envoy it runs one
#
#   go test -json -count=1 [-race] [-run PATTERN] [-skip PATTERN] -timeout D PACKAGE...
#
# and reads that report. `--var NAME` names a variable the packages need, and repeats; with none
# it is BROKER_TEST_DATABASE_URL. When a named variable is unset or empty it prints
# `database_skips=UNKNOWN` naming it and exits 1, and runs no test. `--race` adds -race.
# `--timeout D` sets the -timeout, and without it the -timeout is ${GO_TEST_TIMEOUT:-15m}. The
# -timeout bounds each package's test binary, not the run, since go test runs the packages
# several at a time, so a caller that must bound the run sets a limit of its own (CI's steps do).
# `--skip PATTERN` is go test's -skip, for a test another step of the same job runs in the same
# build and environment (envoy-go's backend count): go test reports nothing for a test -skip
# leaves out, so it is neither run twice nor counted as a skip here.
# `--run PATTERN` is go test's -run, for one package that a matrix splits into legs, each running
# the tests one pattern selects (envoy-go-race-api, whose legs take their patterns from
# .github/scripts/go-test-shards.py). The package's test binary, built under the run's own flags
# (GOFLAGS, -race) and run in the package's directory as go test runs it, lists what PATTERN
# selects (-test.list), and only the EXPECTED_SKIPS lines of those tests excuse a skip and must
# be held by the report: each leg is held to the expected skips of the tests it ran. A line naming
# a test the binary does not list at all fails every leg, as a whole-package run fails it, so a
# renamed test leaves no line that no leg is held to; with go-test-shards.py's proof that the legs'
# patterns select every listed test exactly once, every line is held by exactly one leg. A test
# the report shows run that the listing does not select fails it too, since go test -run and
# -test.list then read the pattern apart. It takes one package, and a pattern with no `/`, at
# which go test -run splits a pattern and -test.list does not. It prints, on stderr, which
# EXPECTED_SKIPS tests the pattern selects.
# `--report FILE` reads FILE instead and runs nothing, which the controls use; the four options
# that shape a run are refused beside it. `--run PATTERN` may stand beside it: its binary is then
# built under GOFLAGS alone, so a report made with -race is read with GOFLAGS=-race.
#
# It prints each skipped test of a package the patterns name (as `go list` names them under the
# run's own flags) as `SKIP PACKAGE TEST`, then `database_skips=N`, and for a run of its own
# `go_test_exit=E`, go test's own exit status. A report that shows no run is no count at all: it
# prints `database_skips=UNKNOWN` and names what it could not read, rather than a zero. That is a
# report that is missing or empty, or that holds no test that passed or failed of a listed
# package with test files (a package whose tests all skipped, or whose TestMain returned before
# it ran them); and an EXPECTED_SKIPS line whose package is listed and whose test the report does
# not hold (under --run, of a test the pattern selects), since that line then excuses nothing it
# can name. It exits 0 only when every listed
# package with test files shows tests run, none of them skipped but those EXPECTED_SKIPS names
# and none failed, and, for a run of its own, go test passed; 1 otherwise. A run of its own also
# prints each listed package's `pass|fail PACKAGE <seconds>s` on stderr. On a failure it prints,
# on stderr, each failed test, each test a failed package never finished, the failed packages'
# output (a failed test's own lines, a timed-out binary's goroutine dump, a data race, and any line
# test2json files under a test after that test's own `--- PASS` or `--- SKIP`), go test's build
# output and, for a run of its own, its stderr. Each of those is read per run of a test, so a test
# a binary runs twice under one name - with -count, or a TestMain that calls m.Run twice - that
# fails in one run and passes in the other still has its failure's lines printed.
set -euo pipefail
# sort and comm compare the test lists under one collation, whatever the caller's locale.
export LC_ALL=C

# A relative --report is the caller's path; every go command runs from packages/envoy.
caller="$PWD"
cd "$(dirname "$0")/.."

# The broker's packages and its command, less DEFAULT_EXCEPT: every package of each opens the
# broker's Postgres, named by BROKER_TEST_DATABASE_URL, or skips. DEFAULT_EXCEPT is a `grep -Ev`
# pattern over the import paths `go list` prints for DEFAULT_PACKAGES, and a run with no PACKAGE
# runs the packages it keeps. It takes out the race lane's packages under the broker's tree (each
# a package of ENVOY_RACE_LANE in .github/scripts/ci_job_reads.py, which builds the same pattern
# as DATABASE_SKIPS_PATTERN and refuses this script when DEFAULT_EXCEPT says otherwise): the auth
# service's natscallout, the one today, opens no database, and envoy-go-race runs it under the
# detector, so a run here would be its second in the same CI run.
DEFAULT_PACKAGES=(./internal/cipher/... ./cmd/cipherd/)
DEFAULT_EXCEPT='/internal/cipher/natscallout$'

# EXPECTED_SKIPS: PACKAGE TEST REASON, one per line: a test that skips in every run that sets the
# database variables, and why. A skip of a test it names is not counted; a line whose package is
# under a run's patterns names a test that run must hold, and a line with no reason is refused
# when the script starts. A test that skips on some servers only is not here: its skip is counted
# where CI's server runs it (TestAdvancesListEachPriorityOnceHoweverTheDatabaseCollatesTheIDs
# needs a PostgreSQL built with ICU, which CI's image is).
EXPECTED_SKIPS='github.com/sjawhar/envoy/internal/cipher/proxyrules FuzzAirlock needs AIRLOCK_MODEL, a built Lean model of the airlock, which the airlock-lean job builds and runs it with
github.com/sjawhar/envoy/internal/cipher/proxyrules TestModelAgreesOnHostShapes needs AIRLOCK_MODEL, as FuzzAirlock does
github.com/sjawhar/envoy/internal/cipher/proxyrules TestTheModelReadsAHostlessEntryWithGuidance needs AIRLOCK_MODEL, as FuzzAirlock does
github.com/sjawhar/envoy/internal/dispatch/api TestDismissedScaleHoldsTheInboxsReads is a measurement, run only with LEGION_SCALE_TEST=1
github.com/sjawhar/envoy/internal/dispatch/api TestNearCopyRefusalReplay needs DISPATCH_NEARCOPY_CORPUS, a corpus scripts/near-copy-replay.sh writes
github.com/sjawhar/envoy/internal/dispatch/api TestRelatedRecallOnDuplicates needs DISPATCH_RECALL_CORPUS, a corpus scripts/related-recall.sh writes
github.com/sjawhar/envoy/internal/dispatch/api TestRelatedCheckLatencyOnRecallCorpus needs DISPATCH_RECALL_CORPUS and DISPATCH_RECALL_LATENCY_ISSUE
github.com/sjawhar/envoy/internal/dispatch/api TestSearchLatencyOnCorpus needs DISPATCH_BENCH_DATABASE_URL, a restored corpus copy
github.com/sjawhar/envoy/internal/dispatch/api TestStatementTimings is a measurement on the seeded database DISPATCH_BENCH_DATABASE_URL names (benchtime.Open), which CI leaves unset
github.com/sjawhar/envoy/internal/dispatch/api TestRelatedTermsRefreshTiming is a measurement on that same seeded database (benchtime.Open)
github.com/sjawhar/envoy/internal/dispatch/api TestGrowsLinearlyRejectsQuadraticFinalRungHelper is a subprocess helper, which its parent test starts with GROWS_LINEARLY_SLOW_FIRST set'

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

report=""
go_test_exit=""

# by_run is the report's events in order, each event of a test given `_run`: its package, its name
# and which run of it this is. A test binary can run a test more than once under one name (-count,
# or a TestMain that calls m.Run twice, as internal/cipher/proxy's does, once per protocol),
# and test2json reports every run under that one name, so what a run did is read by that key and
# not by the name: read by the name, a test that failed in one run and passed in the other counted
# as passed, and the failure's own lines were left out.
# shellcheck disable=SC2016 # $e is jq's variable, not the shell's
by_run='def by_run: foreach .[] as $e ({};
  if $e.Test != null and $e.Action == "run" then .["\($e.Package) \($e.Test)"] += 1 else . end;
  if $e.Test == null then $e else $e + {_run: "\($e.Package) \($e.Test) \(.["\($e.Package) \($e.Test)"] // 0)"} end);'

# failures prints, on stderr, what a failed run left in the report: each failed test, each run of a
# test a failed package started and never ended (the one a timed-out binary was running), every
# output line of a failed package but those of the runs of its tests that passed or skipped, every
# line filed under a run of a test after that run's own `--- PASS` or `--- SKIP` line, and every
# line from the package's first `panic: ` on (so the goroutine dump of a timeout, a data race and
# the package's own FAIL line), the build output, and, for a run of its own, go test's stderr.
failures() {
  jq -rs "$by_run"' [by_run] as $events
    | (reduce ($events[] | select(.Test != null and (.Action == "pass" or .Action == "fail" or .Action == "skip"))) as $e ({}; .[$e._run] = true)) as $ended
    | (reduce ($events[] | select(.Action == "fail" and .Test == null)) as $e ({}; .[$e.Package] = true)) as $failed
    | $events[] | select(.Test != null)
    | if .Action == "fail" then "FAIL \(.Package) \(.Test)"
      elif .Action == "run" and $failed[.Package] and ($ended[._run] | not) then "UNFINISHED \(.Package) \(.Test)"
      else empty end' "$report" >&2 || true
  # test2json files each output line under the test whose framing line came last. A test's own
  # output comes before its `--- PASS` or `--- SKIP` line, so a line filed under it after that line
  # is another's: a data race report after a subtest passed, or a panic after a parallel test
  # passed. Those lines are printed, as is every line of a failed package from its first `panic: `
  # on, whatever test they are filed under.
  jq -rsj "$by_run"' [by_run] as $events
    | (reduce ($events[] | select(.Test != null and (.Action == "pass" or .Action == "skip"))) as $e ({}; .[$e._run] = true)) as $passed
    | (reduce ($events[] | select(.Action == "fail" and .Test == null)) as $e ({}; .[$e.Package] = true)) as $failed
    | foreach ($events[] | select(.Action == "output" and $failed[.Package // ""])) as $e
        ({panicked: {}, ended: {}, show: false};
         ($e.Output | startswith("panic: ")) as $panic
         | .show = ($panic or .panicked[$e.Package] == true or ($e._run != null and .ended[$e._run] == true))
         | if $panic then .panicked[$e.Package] = true else . end
         | if $e._run != null and ($e.Output | test("^\\s*--- (PASS|SKIP): ")) then .ended[$e._run] = true else . end;
         [.show, $e])
    | .[0] as $misfiled | .[1]
    | select($misfiled or .Test == null or ($passed[._run] | not))
    | .Output' "$report" >&2 || true
  jq -rj 'select(.Action == "build-output") | .Output' "$report" >&2 || true
  [ -z "$go_test_exit" ] || cat "$work/stderr" >&2
}

unknown() {
  echo "database_skips=UNKNOWN"
  echo "database-skips.sh: $1, so no skip was counted" >&2
  if [ -n "$go_test_exit" ]; then
    echo "go_test_exit=$go_test_exit"
    [ "$go_test_exit" -eq 0 ] || failures
  fi
  exit 1
}

usage() {
  echo "database-skips.sh: $1 (usage: database-skips.sh [--var NAME]... [--race] [--timeout D] [--run PATTERN] [--skip PATTERN] [PACKAGE...], or --report FILE [--run PATTERN] [PACKAGE...])" >&2
  exit 1
}

while IFS= read -r line; do
  [ -n "$line" ] || continue
  read -r _ _ reason <<<"$line"
  [ -n "$reason" ] || unknown "EXPECTED_SKIPS line \"$line\" gives no reason it skips"
done <<<"$EXPECTED_SKIPS"

vars=()
race=()
skip=()
timeout=""
run=""
packages=()
while [ $# -gt 0 ]; do
  case "$1" in
    --var)
      [[ "${2:-}" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || usage "--var needs a variable name, not \"${2:-}\""
      vars+=("$2")
      shift 2
      ;;
    --race)
      race=(-race)
      shift
      ;;
    --timeout)
      [ -n "${2:-}" ] || usage "--timeout needs a duration"
      timeout="$2"
      shift 2
      ;;
    --skip)
      [ -n "${2:-}" ] || usage "--skip needs a go test -skip pattern"
      skip=(-skip "$2")
      shift 2
      ;;
    --run)
      [ -n "${2:-}" ] || usage "--run needs a go test -run pattern"
      case "$2" in
        */*) usage "--run takes a pattern of top-level tests, with no /: go test -run splits a pattern at each /, and -test.list, which says what the pattern selects, does not" ;;
      esac
      run="$2"
      shift 2
      ;;
    --report)
      [ -n "${2:-}" ] || usage "--report needs the go test -json report to read"
      case "$2" in
        /*) report="$2" ;;
        *) report="$caller/$2" ;;
      esac
      shift 2
      ;;
    ./*) packages+=("$1"); shift ;;
    *) usage "unknown argument $1: a package is a pattern under packages/envoy that starts with ./" ;;
  esac
done
if [ -n "$report" ] && { [ ${#vars[@]} -gt 0 ] || [ ${#race[@]} -gt 0 ] || [ -n "$timeout" ] || [ ${#skip[@]} -gt 0 ]; }; then
  usage "--report reads a run made elsewhere, so --var, --race, --timeout and --skip, which shape a run of its own, are refused beside it"
fi
# runs is what the go list and go test below are given: the patterns as written, or for the
# default table the import paths go list prints for it under the run's own flags, less
# DEFAULT_EXCEPT, which they take as they take a pattern. The messages name the patterns.
if [ ${#packages[@]} -eq 0 ]; then
  packages=("${DEFAULT_PACKAGES[@]}")
  go list "${race[@]}" "${DEFAULT_PACKAGES[@]}" >"$work/default" 2>"$work/list" || {
    echo "database_skips=UNKNOWN"
    echo "database-skips.sh: go list ${DEFAULT_PACKAGES[*]} failed, so no test ran and no skip was counted:" >&2
    cat "$work/list" >&2
    exit 1
  }
  mapfile -t runs < <(grep -Ev "$DEFAULT_EXCEPT" "$work/default")
  [ ${#runs[@]} -gt 0 ] || unknown "DEFAULT_EXCEPT takes every package of ${DEFAULT_PACKAGES[*]} out, so no test ran"
else
  runs=("${packages[@]}")
fi
[ ${#vars[@]} -gt 0 ] || vars=(BROKER_TEST_DATABASE_URL)

if [ -z "$report" ]; then
  for name in "${vars[@]}"; do
    [ -n "${!name:-}" ] || unknown "$name is unset or empty, and the packages' database tests skip without it; no test ran"
  done
fi

# The packages the patterns name, as go lists them under the run's own flags (GOFLAGS, -race):
# each by its canonical import path, which is how the report names it whatever spelling the
# pattern used, with how many test files it has.
go list "${race[@]}" -f '{{.ImportPath}} {{len .TestGoFiles}} {{len .XTestGoFiles}}' "${runs[@]}" >"$work/listed" 2>"$work/list" || {
  echo "database_skips=UNKNOWN"
  echo "database-skips.sh: go list ${packages[*]} failed, so no test ran and no skip was counted:" >&2
  cat "$work/list" >&2
  exit 1
}
listed="$(jq -R -s -c '[split("\n")[] | select(length > 0) | split(" ") | {(.[0]): true}] | add // {}' "$work/listed")"
# shellcheck disable=SC2016 # $pkg and $listed are jq's variables, not the shell's
under='def under($pkg): $listed[$pkg] == true;'

# Under --run: what the pattern selects (selected) and every test the package's test binary holds
# (compiled), each `PACKAGE TEST` per line, as that binary lists them, built under the run's own
# flags and run in the package's directory. go's standard error goes through as go writes it, so
# a build that stalls is named while it waits.
runflag=()
if [ -n "$run" ]; then
  runflag=(-run "$run")
  [ "$(wc -l <"$work/listed")" -eq 1 ] ||
    usage "--run reads one package's test binary, and ${packages[*]} lists $(wc -l <"$work/listed") packages"
  read -r package _ <"$work/listed"
  dir="$(go list "${race[@]}" -f '{{.Dir}}' "${runs[@]}")" ||
    unknown "go list of ${packages[*]}'s directory failed (its standard error is above), so nothing says what --run selects"
  go test "${race[@]}" -c -o "$work/selection.test" "${runs[@]}" >&2 ||
    unknown "go test -c of ${packages[*]} failed (its standard error is above), so nothing says what --run selects"
  [ -x "$work/selection.test" ] || unknown "go test -c of ${packages[*]} wrote no test binary, so nothing says what --run selects"
  for listing in "selected:$run" "compiled:."; do
    (cd "$dir" && "$work/selection.test" -test.list "${listing#*:}") >"$work/listing" ||
      unknown "the test binary of ${packages[*]} exited $? listing ${listing#*:} (its standard error is above), so nothing says what --run selects"
    { grep -E '^(Test|Fuzz|Example|Benchmark)[^[:space:]]*$' "$work/listing" || true; } |
      awk -v p="$package" '{ print p, $0 }' | sort -u >"$work/${listing%%:*}"
  done
fi

if [ -z "$report" ]; then
  report="$work/report.json"
  go_test_exit=0
  go test -json -count=1 "${race[@]}" "${runflag[@]}" "${skip[@]}" -timeout "${timeout:-${GO_TEST_TIMEOUT:-15m}}" "${runs[@]}" >"$report" 2>"$work/stderr" || go_test_exit=$?
fi

[ -e "$report" ] || unknown "the report $report does not exist"
[ -s "$report" ] || unknown "the report $report is empty"
jq -r --argjson listed "$listed" "$under"' select(.Action == "pass" or .Action == "fail" or .Action == "skip") | select(under(.Package // "")) | "\(.Action) \(.Package) \(.Test // "")"' "$report" >"$work/ended" 2>"$work/jq" ||
  unknown "the report $report is not a go test -json stream ($(head -c 200 "$work/jq"))"
# A test that passed or failed is the evidence a package's tests ran: a skip is what this script
# counts, so a package whose tests all skipped shows none ran, and so does a package whose TestMain
# returned before it ran them, which passes with no test. Every listed package with a test file
# must hold one; a package with none has nothing to run.
awk 'NF == 3 && $1 != "skip" { print $2 }' "$work/ended" | sort -u >"$work/evidenced"
[ -s "$work/evidenced" ] || unknown "the report $report holds no test of a package under ${packages[*]} that passed or failed"
awk '$2 + $3 > 0 { print $1 }' "$work/listed" | sort -u | comm -23 - "$work/evidenced" >"$work/silent"
[ ! -s "$work/silent" ] || unknown "the report $report holds no test that passed or failed in $(paste -sd ',' "$work/silent" | sed 's/,/, /g'), which has test files"

awk 'NF == 3 && $1 == "skip" { print $2, $3 }' "$work/ended" | sort -u >"$work/skipped"
awk 'NF == 3 { print $2, $3 }' "$work/ended" | sort -u >"$work/held"
printf '%s\n' "$EXPECTED_SKIPS" | jq -R -r --argjson listed "$listed" "$under"' select(length > 0) | split(" ") | select(under(.[0])) | "\(.[0]) \(.[1])"' | sort -u >"$work/expected"
# Under --run, only the lines of the tests the pattern selects excuse a skip and must be held, and
# a line whose test the binary does not list at all, or a test the report shows run that the
# listing does not select, is no count (the header says why).
if [ -n "$run" ]; then
  comm -23 "$work/expected" "$work/compiled" >"$work/unlisted"
  [ ! -s "$work/unlisted" ] || unknown "EXPECTED_SKIPS names $(paste -sd ',' "$work/unlisted" | sed 's/,/, /g'), which the test binary of ${packages[*]} does not list, so no --run selects it"
  comm -12 "$work/expected" "$work/selected" >"$work/excused"
  excused="$(cut -d ' ' -f 2 "$work/excused" | paste -sd ',' | sed 's/,/, /g')"
  echo "database-skips.sh: --run selects $(wc -l <"$work/excused") of the $(wc -l <"$work/expected") tests EXPECTED_SKIPS names under ${packages[*]}: ${excused:-none}" >&2
  mv "$work/excused" "$work/expected"
  awk '$2 !~ /\// { print }' "$work/held" | comm -23 - "$work/selected" >"$work/unselected"
  [ ! -s "$work/unselected" ] || unknown "the report $report holds $(paste -sd ',' "$work/unselected" | sed 's/,/, /g'), which --run $run does not select as the test binary lists it, so go test -run and -test.list read the pattern apart"
fi
comm -23 "$work/expected" "$work/held" >"$work/stale"
[ ! -s "$work/stale" ] || unknown "EXPECTED_SKIPS names $(paste -sd ',' "$work/stale" | sed 's/,/, /g'), under ${packages[*]}, which the report $report does not hold"

comm -23 "$work/skipped" "$work/expected" >"$work/counted"
sed 's/^/SKIP /' "$work/counted"
database_skips="$(wc -l <"$work/counted")"
echo "database_skips=$database_skips"

status=0
[ "$database_skips" -eq 0 ] || status=1
# A failed test or package fails the count too, in a report read with --report as in a run.
awk '$1 == "fail"' "$work/ended" >"$work/failed"
[ ! -s "$work/failed" ] || status=1
if [ -n "$go_test_exit" ]; then
  # Each listed package's time, which go test prints without -json and the CI steps' limits are
  # set from, on stderr.
  jq -r --argjson listed "$listed" "$under"' select(.Test == null and (.Action == "pass" or .Action == "fail") and under(.Package // "")) | "\(.Action) \(.Package) \(.Elapsed // 0)s"' "$report" >&2 || true
  echo "go_test_exit=$go_test_exit"
  [ "$go_test_exit" -eq 0 ] || status=1
fi
if [ -s "$work/failed" ] || { [ -n "$go_test_exit" ] && [ "$go_test_exit" -ne 0 ]; }; then
  failures
fi
exit "$status"
