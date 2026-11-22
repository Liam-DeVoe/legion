#!/usr/bin/env bash
# Tests for `go-test-split.sh`, which splits the tests of a job's packages between its legs at run
# time by a hash of each name. It reads what `go test -list` printed, captured into
# .github/scripts/testdata/go-test-split/ with the commands its README names (a package of this
# repository with Tests, a Benchmark and Fuzz targets; a package with no test files; a scratch
# package with a Test, an Example and a Fuzz target), fed through GO_TEST_LIST_OUTPUT so no go
# command runs. It holds that each listed name lands in exactly one leg for 1 to 8 legs, that a
# name added later lands in exactly one leg, and that a package's `ok` and `?` lines and a
# Benchmark are no leg's.
#
# Run from anywhere: .github/scripts/go-test-split.test.sh
# CI runs it in ci.yaml's test job.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
splitter="$script_dir/go-test-split.sh"
captures="$script_dir/testdata/go-test-split"

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# split_leg <listing> <leg> <legs>: the script under test's -run for that leg over the listing, in
# $out, its status in $status and what it said on stderr in $said.
split_leg() {
  status=0
  out=$(GO_TEST_LIST_OUTPUT="$1" "$splitter" "$2" "$3" ./a-package/ 2>"$work/stderr") || status=$?
  said=$(cat "$work/stderr")
}

# names_of <run>: the names a printed `^(A|B)$` holds, one per line; none for `^$`.
names_of() {
  case "$1" in
    '^$') ;;
    *) sed -E 's/^\^\(//; s/\)\$$//' <<<"$1" | tr '|' '\n' ;;
  esac
}

# The shape every leg prints: `^(Name|Name|...)$`, or `^$` for a leg with no test.
RUN_SHAPE='^\^(\([A-Za-z0-9_|]+\))?\$$'

# landings <listing> <legs>: each name the listing's legs print, one line per time a leg printed it,
# sorted, into $work/landed; and each leg that exited non-zero or printed another shape into
# $work/broken.
landings() {
  local leg
  : >"$work/broken"
  for leg in $(seq 1 "$2"); do
    split_leg "$1" "$leg" "$2"
    [ "$status" = 0 ] || echo "leg $leg of $2 exited $status" >>"$work/broken"
    [[ "$out" =~ $RUN_SHAPE ]] || echo "leg $leg of $2 printed \"$out\"" >>"$work/broken"
    names_of "$out"
  done | sort >"$work/landed"
}

# listed <listing>: the Tests, Fuzz targets and Examples of a listing, sorted: the names its legs
# must print between them.
listed() { grep -E '^(Test|Fuzz|Example)[A-Za-z0-9_]*$' "$1" | sort -u || true; }

for capture in logmask test-example-fuzz no-test-files; do
  listing="$captures/$capture.txt"
  echo "case: $capture.txt, each listed name in exactly one leg"
  for legs in 1 2 3 4 5 6 7 8; do
    landings "$listing" "$legs"
    check "$legs legs: every leg exits 0 and prints a -run" "$(is "$(cat "$work/broken")" "")"
    check "$legs legs: the legs print each listed name once and nothing else" \
      "$(is "$(cat "$work/landed")" "$(listed "$listing")")"
  done
done

echo "case: the names a leg prints are the listing's, and its counts on stderr"
split_leg "$captures/logmask.txt" 1 1
check "one leg keeps the thirteen Tests and the two Fuzz targets" \
  "$(is "$(names_of "$out" | wc -l | tr -d ' ')" 15)"
check "it says how many it kept of how many" "$(contains "$said" 'leg 1 of 1 keeps 15 of the 15 tests')"
check "the Benchmark is no leg's" "$(is "$(contains "$out" 'BenchmarkWriter')" false)"
check "the package's ok line is no name" "$(is "$(contains "$out" 'logmask\|0\.062s')" false)"
split_leg "$captures/test-example-fuzz.txt" 1 1
check "the scratch package's Test, Fuzz target and Example are kept" \
  "$(is "$out" '^(ExampleGreeting|FuzzReverses|TestAdds)$')"
split_leg "$captures/no-test-files.txt" 1 1
check "a package with no test files gives a -run that runs no test" "$(is "$out" '^$')"
check "its ? line is no name" "$(contains "$said" 'keeps 0 of the 0 tests')"

echo "case: a name added later lands in exactly one leg, and moves no other"
{ cat "$captures/logmask.txt"; echo TestANameAddedAfterTheCapture; } >"$work/added.txt"
for legs in 2 3 4 5 6 7 8; do
  holding=0
  for leg in $(seq 1 "$legs"); do
    split_leg "$captures/logmask.txt" "$leg" "$legs"
    before=$out
    split_leg "$work/added.txt" "$leg" "$legs"
    if [ "$(contains "$out" 'TestANameAddedAfterTheCapture')" = true ]; then
      holding=$((holding + 1))
      after=$(names_of "$out" | grep -vx TestANameAddedAfterTheCapture || true)
    else
      after=$(names_of "$out")
    fi
    [ "$after" = "$(names_of "$before")" ] || holding=-100
  done
  check "$legs legs: exactly one leg takes the new name, and each leg keeps its other names" "$(is "$holding" 1)"
done

echo "case: what it refuses"
printf 'TestOne\ncan'"'"'t load package: package ./nowhere: no such directory\n' >"$work/unreadable.txt"
split_leg "$work/unreadable.txt" 1 2
check "a line that is neither a name nor a package's line exits 1" "$(is "$status" 1)"
check "and names that line" "$(contains "$said" "can't load package")"
split_leg "$captures/logmask.txt" 0 2
check "leg 0 exits 2" "$(is "$status" 2)"
split_leg "$captures/logmask.txt" 3 2
check "a leg past the number of legs exits 2" "$(is "$status" 2)"
split_leg "$captures/logmask.txt" 1 0
check "zero legs exits 2" "$(is "$status" 2)"

summary "go-test-split.sh"
