#!/usr/bin/env bash
# Tests for `go-test-shards.py`: the split of one Go package's tests into shards, and the check
# that the shards' -run patterns select every test exactly once.
#
# Each case builds a miniature Go module under a temporary directory and runs the real script
# over it, so the script under test is the file CI runs, the binary is a real `go test -c` build
# and every pattern is matched by Go's own regexp. The controls are the check's reason to exist:
# a test no pattern selects, and a test two patterns select, must each fail it by name, and the
# patterns the split itself prints must pass it.
#
# Needs go and python3 on PATH; no network and no cgo (no case builds with -race). The modules
# declare no dependency, and GOWORK=off and GOTOOLCHAIN=local keep the go command on the fixture
# module and the toolchain it already has.
#
# Run from anywhere: .github/scripts/go-test-shards.test.sh
# CI runs it in ci.yaml's test job.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
shards="$script_dir/go-test-shards.py"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
export GOWORK=off GOTOOLCHAIN=local GOFLAGS=

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

# module <name>: an empty Go module of that name with one package at its root. Echoes its path.
module() {
  local root="$work/$1"
  if [ -e "$root" ]; then
    echo "  FAIL: the fixture name '$1' is already taken by an earlier case" >&2
    return 1
  fi
  mkdir -p "$root"
  printf 'module example.com/%s\n\ngo 1.21\n' "$1" >"$root/go.mod"
  echo "$root"
}

# tests <file> <name>...: a test file declaring one empty top-level test per name.
tests() {
  local path=$1
  shift
  printf 'package fixture\n\nimport "testing"\n\n' >"$path"
  for name in "$@"; do
    printf 'func %s(t *testing.T) {}\n' "$name" >>"$path"
  done
}

# run_shards <directory> <argument>...: runs the script in that directory; sets out, err and
# status, keeping the two streams apart, because a refusal must print nothing on standard output.
run_shards() {
  local directory=$1
  shift
  set +e
  out=$(cd "$directory" && "$shards" "$@" 2>"$work/stderr")
  status=$?
  set -e
  err=$(cat "$work/stderr")
}

# names_of <pattern>: the names an anchored alternation `^(A|B|C)$` holds, one per line.
names_of() {
  local inner=${1#'^('}
  inner=${inner%')$'}
  tr '|' '\n' <<<"$inner"
}

# union_of <pattern>...: every name the patterns hold, sorted, one per line.
union_of() {
  local p
  for p in "$@"; do names_of "$p"; done | LC_ALL=C sort
}

# without <name> <pattern>...: the same patterns with that one name taken out of the shard
# that holds it, one pattern per line - the control that leaves a test in no shard.
without() {
  local name=$1 p
  shift
  for p in "$@"; do
    printf '^(%s)$\n' "$(names_of "$p" | grep -v -x "$name" | paste -sd '|')"
  done
}

# The package every split case reads: seven plain tests, a fuzz target, an example with an output
# comment, and the four things that are no test of the split - an example without an output
# comment (go test compiles it and never runs it, and -test.list does not print it), a benchmark
# (-run never starts one), TestMain and a method named like a test. TestMain prints a line while
# the binary lists, as a TestMain that logs would. One more file carries a build tag, so the
# build flags decide whether its test exists, which is what `//go:build !race` decides for the
# race build of internal/dispatch/api.
root=$(module split)
tests "$root/plain_test.go" TestAlpha TestBeta TestGamma TestDelta TestEpsilon TestZeta TestEta
cat >"$root/kinds_test.go" <<'EOF'
package fixture

import (
	"fmt"
	"os"
	"testing"
)

type harness struct{}

func (harness) TestMethodIsNoTest(t *testing.T) {}

func TestMain(m *testing.M) {
	fmt.Println("listing as TestMain")
	os.Exit(m.Run())
}

func FuzzTheParser(f *testing.F) { f.Fuzz(func(t *testing.T, s string) {}) }

func Example_shown() {
	fmt.Println("shown")
	// Output: shown
}

func Example_unshown() { fmt.Println("never run") }

func BenchmarkTheSpeed(b *testing.B) {}
EOF
cat >"$root/tagged_test.go" <<'EOF'
//go:build shardtag

package fixture

import "testing"

func TestOnlyUnderTheTag(t *testing.T) {}
EOF
everything='Example_shown
FuzzTheParser
TestAlpha
TestBeta
TestDelta
TestEpsilon
TestEta
TestGamma
TestZeta'

echo "case: three shards split the package's tests, each test into exactly one"
patterns=()
for index in 0 1 2; do
  run_shards "$root" pattern "$index" 3 .
  check "shard $index exits 0" "$(is "$status" 0)"
  check "shard $index prints one anchored alternation" "$(contains "$out" '^\^(\([A-Za-z0-9_|]*\))\$$')"
  check "shard $index says what it holds and that the check passed" \
    "$(contains "$err" "^go-test-shards.py: shard $((index + 1)) of 3 holds [0-9]* of the 9 tests of \.; every test is in exactly one of the 3 shards$")"
  patterns+=("$out")
done
union=$(union_of "${patterns[@]}")
check "together the three hold every test, and each once" "$(is "$union" "$everything")"
check "no shard holds what is no test of the split" \
  "$(is "$(grep -c -E 'Example_unshown|BenchmarkTheSpeed|TestMain$|TestMethodIsNoTest|TestOnlyUnderTheTag' <<<"$union" || true)" 0)"
unsorted=0
for p in "${patterns[@]}"; do
  [ "$(names_of "$p")" = "$(names_of "$p" | LC_ALL=C sort)" ] || unsorted=$((unsorted + 1))
done
check "each shard's names are sorted" "$(is "$unsorted" 0)"

echo "case: the patterns the split prints pass the check (the current list passes)"
run_shards "$root" check . -- "${patterns[@]}"
check "exits 0" "$(is "$status" 0)"
check "prints nothing on standard output" "$(is "$out" "")"
check "says every test is in exactly one shard" \
  "$(contains "$err" '^go-test-shards.py: every one of the 9 tests of \. is in exactly one of the 3 shards$')"

echo "case: control - a test left out of every shard fails the check, by name"
mapfile -t without_gamma < <(without TestGamma "${patterns[@]}")
run_shards "$root" check . -- "${without_gamma[@]}"
check "exits 1" "$(is "$status" 1)"
check "prints nothing on standard output" "$(is "$out" "")"
check "names the test and says no leg runs it" \
  "$(contains "$err" '^go-test-shards.py: TestGamma is in no shard: none of the 3 patterns selects it, so no leg runs it$')"
check "names no other test" "$(is "$(grep -c 'is in' <<<"$err")" 1)"

echo "case: control - a test in two shards fails the check, by name"
run_shards "$root" check . -- "${patterns[@]}" '^(TestAlpha)$'
check "exits 1" "$(is "$status" 1)"
check "prints nothing on standard output" "$(is "$out" "")"
check "names the test and both shards" \
  "$(contains "$err" "^go-test-shards.py: TestAlpha is in shards [123] and 4 of 4: each of those legs runs it$")"
check "names no other test" "$(is "$(grep -c 'is in' <<<"$err")" 1)"

echo "case: control - the check matches as Go's -run does, not as the pattern's author meant"
# '^TestE' is no alternation of whole names: Go's regexp selects TestEpsilon and TestEta by it,
# so beside the split's own patterns each of them is in two shards.
run_shards "$root" check . -- "${patterns[@]}" '^TestE'
check "exits 1" "$(is "$status" 1)"
check "TestEpsilon is in two shards" "$(contains "$err" '^go-test-shards.py: TestEpsilon is in shards [123] and 4 of 4')"
check "and so is TestEta" "$(contains "$err" '^go-test-shards.py: TestEta is in shards [123] and 4 of 4')"
check "and nothing else" "$(is "$(grep -c 'is in' <<<"$err")" 2)"

echo "case: control - a pattern holding a / fails the check, by pattern"
# -test.list matches each top-level name against the whole pattern, so it lists TestAlpha under
# '^TestAlpha$' alone. go test -run splits '^TestBeta$|^TestAlpha$/child' at the / and starts
# TestAlpha for its subtest too, so the two legs would both run TestAlpha while every listing said
# once.
root_slash=$(module slash)
tests "$root_slash/slash_test.go" TestAlpha TestBeta
run_shards "$root_slash" check . -- '^TestAlpha$' '^TestBeta$|^TestAlpha$/child'
check "exits 1" "$(is "$status" 1)"
check "prints nothing on standard output" "$(is "$out" "")"
# shellcheck disable=SC2016 # literal backticks and $ of the message, no expansion wanted
check "names the pattern and the /" \
  "$(contains "$err" '^go-test-shards.py: pattern 2 of 2, `^TestBeta\$|^TestAlpha\$/child`, holds a `/`: ')"
check "and names nothing else" "$(is "$(grep -c '^go-test-shards.py: ' <<<"$err")" 1)"
run_shards "$root_slash" check . -- '^TestAlpha$' '^TestBeta$'
check "the same tests without the / pass" "$(is "$status" 0)"
run_shards "$root_slash" pattern 0 1 .
check "and the pattern the split prints holds no /" "$(is "$(grep -c / <<<"$out" || true)" 0)"

echo "case: a test whose name is not ASCII is split and checked like any other"
# go test runs TestÉclair, Test日本 and TestᲉ: cmd/go asks only that the rune after `Test` is not
# lower case. A listing that kept ASCII names alone would leave them in no shard, and its proof,
# reading the same listing, would pass while no leg ran them. TestᲉ is U+1C89, a letter of Unicode
# 16.0: the go that builds this module (Go 1.27, Unicode 17.0) compiles and runs it, while Python
# 3.12's own tables (Unicode 15.0) do not know it, so a listing that read names with `\w` left it
# out the same way.
root_unicode=$(module unicode)
tests "$root_unicode/names_test.go" TestAlpha TestBeta TestÉclair Test日本 TestᲉ
unicode_patterns=()
for index in 0 1; do
  run_shards "$root_unicode" pattern "$index" 2 .
  check "shard $index exits 0" "$(is "$status" 0)"
  unicode_patterns+=("$out")
done
check "the two shards hold all five tests, each once" \
  "$(is "$(union_of "${unicode_patterns[@]}" | paste -sd ' ')" \
    "TestAlpha TestBeta TestÉclair TestᲉ Test日本")"
run_shards "$root_unicode" check . -- "${unicode_patterns[@]}"
check "and the check passes them" "$(is "$status" 0)"
check "counting all five" \
  "$(contains "$err" '^go-test-shards.py: every one of the 5 tests of \. is in exactly one of the 2 shards$')"

for left_out in 'TestÉclair' 'TestᲉ'; do
  echo "case: control - $left_out, left out of every shard, fails the check by name"
  mapfile -t without_one < <(without "$left_out" "${unicode_patterns[@]}")
  run_shards "$root_unicode" check . -- "${without_one[@]}"
  check "exits 1" "$(is "$status" 1)"
  check "prints nothing on standard output" "$(is "$out" "")"
  check "names the test and says no leg runs it" \
    "$(contains "$err" "^go-test-shards.py: $left_out is in no shard: none of the 2 patterns selects it, so no leg runs it\$")"
  check "names no other test" "$(is "$(grep -c 'is in' <<<"$err")" 1)"
done

echo "case: the build flags decide which tests exist"
run_shards "$root" pattern 0 1 -tags shardtag .
check "exits 0" "$(is "$status" 0)"
check "the tagged test is in the split under its tag" "$(is "$(names_of "$out" | grep -c -x TestOnlyUnderTheTag)" 1)"
check "with the other nine" "$(is "$(names_of "$out" | wc -l | tr -d ' ')" 10)"
run_shards "$root" check . -- "$out"
check "the same pattern checked without the tag still covers every test that build holds" "$(is "$status" 0)"

echo "case: a test added later lands in exactly one shard, and moves no other"
root_grown=$(module grown)
cp "$root"/*_test.go "$root_grown/"
tests "$root_grown/added_test.go" TestAddedLater
grown=()
for index in 0 1 2; do
  run_shards "$root_grown" pattern "$index" 3 .
  check "shard $index exits 0" "$(is "$status" 0)"
  grown+=("$out")
done
check "the new test is in exactly one shard" \
  "$(is "$(union_of "${grown[@]}" | grep -c -x TestAddedLater)" 1)"
moved=0
for index in 0 1 2; do
  before=$(names_of "${patterns[$index]}")
  after=$(names_of "${grown[$index]}" | grep -v -x TestAddedLater)
  [ "$before" = "$after" ] || moved=1
done
check "every earlier test is in the shard it was in" "$(is "$moved" 0)"
run_shards "$root_grown" check . -- "${grown[@]}"
check "and the grown package passes the check" "$(is "$status" 0)"

echo "case: the split is the same on every run"
run_shards "$root" pattern 1 3 .
check "shard 1 prints what it printed before" "$(is "$out" "${patterns[1]}")"

echo "case: an index outside the leg count"
run_shards "$root" pattern 3 3 .
check "exits 2" "$(is "$status" 2)"
check "prints nothing on standard output" "$(is "$out" "")"
check "says which indexes there are" "$(contains "$err" 'index 3 is not a shard of 3: an index runs from 0 to 2$')"
run_shards "$root" pattern -1 3 .
check "a negative index exits 2" "$(is "$status" 2)"
run_shards "$root" pattern 0 0 .
check "a total of none exits 2" "$(is "$status" 2)"
run_shards "$root" pattern one 3 .
check "a word for a number exits 2" "$(is "$status" 2)"
check "and says the two must be whole numbers" "$(contains "$err" 'must be whole numbers')"

echo "case: more legs than the package has tests"
run_shards "$root" pattern 0 40 .
check "exits 2" "$(is "$status" 2)"
check "prints nothing on standard output" "$(is "$out" "")"
check "says the matrix has too many legs" "$(contains "$err" 'the matrix has more legs than this package needs$')"

echo "case: a package with no test to split"
root_none=$(module none)
printf 'package fixture\n\nfunc Helper() {}\n' >"$root_none/helper.go"
run_shards "$root_none" pattern 0 2 .
check "exits 2" "$(is "$status" 2)"
check "prints nothing on standard output" "$(is "$out" "")"
check "says it has no test files" "$(contains "$err" 'has no test files$')"
printf 'package fixture\n\nimport "testing"\n\nfunc BenchmarkOnly(b *testing.B) {}\n' >"$root_none/bench_test.go"
run_shards "$root_none" pattern 0 2 .
check "a package of benchmarks alone exits 2" "$(is "$status" 2)"
check "and says it lists no test" "$(contains "$err" 'lists no test, so there is nothing to split$')"

echo "case: a package that does not build"
root_broken=$(module broken)
printf 'package fixture\n\nimport "testing"\n\nfunc TestBroken(t *testing.T) { undefined() }\n' >"$root_broken/broken_test.go"
run_shards "$root_broken" pattern 0 1 .
check "exits 2" "$(is "$status" 2)"
check "prints nothing on standard output" "$(is "$out" "")"
check "passes on the compiler's error" "$(contains "$err" 'undefined: undefined')"

echo "case: a pattern longer than one argument may be"
# 1,300 names of 115 characters make a one-shard pattern of about 150,000 bytes, past
# MAX_ARG_STRLEN; two shards of the same package fit.
root_long=$(module long)
long_names=()
for n in $(seq -w 1 1300); do
  long_names+=("TestAVeryLongNameThatStandsForTheLongestTestNamesOfARealPackageAndKeepsGoingUntilItIsOneHundredAndTwentyLongNo$n")
done
tests "$root_long/long_test.go" "${long_names[@]}"
run_shards "$root_long" pattern 0 1 .
check "exits 2" "$(is "$status" 2)"
check "prints nothing on standard output" "$(is "$out" "")"
check "says the pattern is too long for one argument and to add a leg" \
  "$(contains "$err" 'is longer than one argument may be (131071 bytes, MAX_ARG_STRLEN).*add a leg to the matrix')"
run_shards "$root_long" pattern 0 2 .
check "two legs of it exit 0" "$(is "$status" 0)"

echo "case: the wrong arguments"
run_shards "$root"
check "none exits 2" "$(is "$status" 2)"
check "none prints the usage" "$(contains "$err" 'usage: go-test-shards.py pattern')"
run_shards "$root" pattern 0 3
check "no package exits 2" "$(is "$status" 2)"
run_shards "$root" pattern 0 3 -race
check "a flag where the package goes exits 2" "$(is "$status" 2)"
check "and says so" "$(contains "$err" 'the last argument, -race, is a flag, not a package')"
run_shards "$root" check . '^(TestAlpha)$'
check "a check with no -- exits 2" "$(is "$status" 2)"
# shellcheck disable=SC2016 # literal backticks of the message, no expansion wanted
check "and says so" "$(contains "$err" 'no `--` before the patterns')"
run_shards "$root" check . --
check "a check with no pattern exits 2" "$(is "$status" 2)"

summary "go-test-shards.py"
