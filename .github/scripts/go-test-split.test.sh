#!/usr/bin/env bash
# Tests for `go-test-split.sh`, which splits the tests of a job's packages between its legs at run
# time by a hash of each name. It reads what `go test -list` printed, captured into
# .github/scripts/testdata/go-test-split/ with the commands its README names (a package of this
# repository with Tests, a Benchmark and Fuzz targets; a package with no test files; a scratch
# package with a Test, an Example and a Fuzz target, listed whole and under a -list pattern), fed
# through GO_TEST_LIST_OUTPUT, or printed by a stand-in go that records the command and writes a
# captured `go: downloading` line to standard error, so no real go command runs. It holds that
# each listed name lands in exactly one leg for 1 to 8 legs, that a name added later lands in
# exactly one leg, that a package's `ok` and `?` lines and a Benchmark are no leg's, that go's
# standard error and a line a test binary prints while it lists are no name, that a listing that
# fails exits 1, and that -run=<pattern> lists under the pattern and keeps each name's leg.
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

for capture in logmask test-example-fuzz no-test-files run-pattern; do
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

# A stand-in go on PATH that records its arguments, prints the capture of `go test -list` under
# the pattern and writes the capture of what go wrote to standard error while it fetched a module,
# so the case reads the command the script runs as well as what it makes of each stream.
echo "case: -run=<pattern> narrows the split to the tests the pattern selects"
mkdir -p "$work/bin"
cat >"$work/bin/go" <<'SH'
#!/usr/bin/env bash
printf '%s\n' "$@" >"$GO_ARGUMENTS"
cat "$GO_STDERR" >&2
cat "$GO_LISTING"
SH
chmod +x "$work/bin/go"
pattern='^(TestAdds|ExampleGreeting)$'
for legs in 1 2 3; do
  : >"$work/narrowed"
  : >"$work/unnarrowed"
  for leg in $(seq 1 "$legs"); do
    status=0
    PATH="$work/bin:$PATH" GO_ARGUMENTS="$work/arguments" GO_LISTING="$captures/run-pattern.txt" \
      GO_STDERR="$captures/downloading.stderr.txt" \
      "$splitter" "$leg" "$legs" -tags=memory "-run=$pattern" ./scratch >"$work/out" 2>"$work/stderr" || status=$?
    check "$legs legs, leg $leg: exits 0, the module go fetched on standard error read as no name" "$(is "$status" 0)"
    check "$legs legs, leg $leg: lists under the pattern, with the other flags, and passes go no -run" \
      "$(is "$(cat "$work/arguments")" "$(printf '%s\n' test -list "$pattern" -tags=memory ./scratch)")"
    check "$legs legs, leg $leg: passes go's standard error through to its own" \
      "$(contains "$(cat "$work/stderr")" '^go: downloading cloud.google.com/go/iam v1.15.1$')"
    names_of "$(cat "$work/out")" >>"$work/narrowed"
    # The names this leg takes without the pattern: a name keeps its leg under it.
    split_leg "$captures/test-example-fuzz.txt" "$leg" "$legs"
    { names_of "$out" | grep -v '^FuzzReverses$' || true; } >>"$work/unnarrowed"
  done
  check "$legs legs: the legs print the pattern's two names once each, and not the Fuzz target it leaves out" \
    "$(is "$(sort "$work/narrowed")" "$(printf '%s\n' ExampleGreeting TestAdds)")"
  check "$legs legs: each name lands in the leg it lands in without the pattern" \
    "$(is "$(cat "$work/narrowed")" "$(cat "$work/unnarrowed")")"
done

echo "case: a line a test binary prints while it lists is no name, and passes through"
split_leg "$captures/docs-testmain.txt" 1 1
check "exits 0" "$(is "$status" 0)"
check "keeps every name of the package" \
  "$(is "$(names_of "$out" | wc -l | tr -d ' ')" "$(grep -cE '^(Test|Fuzz|Example)[A-Za-z0-9_]*$' "$captures/docs-testmain.txt")")"
check "and no word of the TestMain line" "$(is "$(names_of "$out" | grep -c '^docs' || true)" 0)"
check "writes that line to standard error" "$(contains "$said" '^docs: 0 services built, 0 still alive after 45ms$')"

echo "case: what it refuses"
# A stand-in go whose `go test -list` fails, as one does for a package that does not build.
mkdir -p "$work/fails"
cat >"$work/fails/go" <<'SH'
#!/usr/bin/env bash
echo "can't load package: package ./nowhere: no such directory" >&2
exit 1
SH
chmod +x "$work/fails/go"
status=0
PATH="$work/fails:$PATH" "$splitter" 1 2 ./nowhere >/dev/null 2>"$work/stderr" || status=$?
check "a go test -list that fails exits 1" "$(is "$status" 1)"
check "and passes go's error through" "$(contains "$(cat "$work/stderr")" "can't load package")"
split_leg "$captures/logmask.txt" 0 2
check "leg 0 exits 2" "$(is "$status" 2)"
split_leg "$captures/logmask.txt" 3 2
check "a leg past the number of legs exits 2" "$(is "$status" 2)"
split_leg "$captures/logmask.txt" 1 0
check "zero legs exits 2" "$(is "$status" 2)"
status=0
GO_TEST_LIST_OUTPUT="$captures/run-pattern.txt" "$splitter" 1 2 -run= ./scratch >/dev/null 2>"$work/stderr" || status=$?
check "an empty -run= exits 2" "$(is "$status" 2)"
check "and says it would list every test" "$(contains "$(cat "$work/stderr")" 'names no pattern')"

summary "go-test-split.sh"
