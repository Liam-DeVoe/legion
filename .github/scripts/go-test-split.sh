#!/usr/bin/env bash
# .github/scripts/go-test-split.sh
#
# Splits the tests of one job's packages between its legs at run time, so a package too slow for
# one leg's time bound runs whole across the legs, each test, one added later included, in
# exactly one leg:
#
#   go-test-split.sh <leg> <legs> [-flag=value]... <package>...
#
# lists the packages' tests with `go test -list '.*' [-flag=value]... <package>...`, run in the
# caller's directory, keeps the Tests, Fuzz targets and Examples (a Benchmark runs under -bench,
# which no leg passes, so a -run share of one would run nothing), and keeps those of leg <leg> of
# <legs> (1 to <legs>) by a hash of the name alone, so a name lands in one leg whatever else the
# packages hold and a name two packages share lands in the same leg in both. It prints that leg's
# share as the -run the leg passes go test, `^(Name|Name|…)$`, sorted, or `^$`, which runs no test,
# for a leg the hash gives none. The flags before the packages are the ones the leg's go test runs
# with that choose which test files build (-tags=…, -race), each written as one word, and
# -run=<pattern>, which narrows the split to the tests the pattern selects: the listing is
# `go test -list <pattern>` in place of '.*', so the legs between them run what one go test
# -run <pattern> of the packages would run, and no other test.
#
# The hash is h = (h * 31 + byte) mod 2147483647 over the name's bytes, and the leg is h mod
# <legs>, plus one: every value stays below 2^36, which awk's double holds exactly on any runner.
#
# GO_TEST_LIST_OUTPUT, where set, names a file holding what `go test -list` printed, which is read
# instead of running go: .github/scripts/go-test-split.test.sh feeds it captured listings.
#
# It reads go's standard output alone, the names and each package's line; what go writes to
# standard error (a `go: downloading` line for each module it fetches, a build's errors) passes
# through to this script's own. A line of standard output that is neither a name nor a package's
# `ok`, `?` or `FAIL` line is one the test binary printed itself while it listed, from its
# TestMain or a package's init (`docs: 0 services built …` in packages/envoy's
# internal/dispatch/docs), and passes through to standard error too: a name is a Go identifier
# that starts with Test, Fuzz or Example, so no such line is read as one. It exits 1 when go test
# -list fails, and 2 on a usage error. It reports how many names it kept on stderr.
set -euo pipefail
export LC_ALL=C

usage() {
  echo "go-test-split.sh: $1 (usage: go-test-split.sh <leg> <legs> [-flag=value]... <package>...)" >&2
  exit 2
}

[ $# -ge 3 ] || usage "it takes a leg, the number of legs and at least one package"
leg=$1
legs=$2
shift 2
[[ "$legs" =~ ^[1-9][0-9]*$ ]] || usage "the number of legs is a number above 0, not \"$legs\""
[[ "$leg" =~ ^[1-9][0-9]*$ ]] && [ "$leg" -le "$legs" ] || usage "the leg is a number from 1 to $legs, not \"$leg\""
flags=()
packages=()
list='.*'
for word in "$@"; do
  if [ "${#packages[@]}" -eq 0 ] && [[ "$word" == -run=* ]]; then
    list=${word#-run=}
    [ -n "$list" ] || usage "-run= names no pattern, which would list every test"
  elif [ "${#packages[@]}" -eq 0 ] && [[ "$word" == -* ]]; then
    flags+=("$word")
  else
    [[ "$word" != -* ]] || usage "the flag $word comes after a package; the flags come first"
    packages+=("$word")
  fi
done
[ "${#packages[@]}" -gt 0 ] || usage "no package is named"

if [ -n "${GO_TEST_LIST_OUTPUT:-}" ]; then
  listing=$(cat "$GO_TEST_LIST_OUTPUT")
else
  listing=$(go test -list "$list" "${flags[@]}" "${packages[@]}") || {
    echo "go-test-split.sh: go test -list '$list' ${flags[*]} ${packages[*]} failed (its errors are above), so no leg's share can be known:" >&2
    printf '%s\n' "$listing" >&2
    exit 1
  }
fi

unread=$(grep -Ev '^((Test|Fuzz|Example|Benchmark)[A-Za-z0-9_]*|(ok|\?|FAIL)[[:space:]].*)$' <<<"$listing" || true)
if [ -n "$unread" ]; then
  echo "go-test-split.sh: the test binaries printed these lines while they listed their tests; none is a name:" >&2
  printf '%s\n' "$unread" >&2
fi

names=$(grep -E '^(Test|Fuzz|Example)[A-Za-z0-9_]*$' <<<"$listing" | sort -u || true)
kept=$(awk -v leg="$leg" -v legs="$legs" '
  BEGIN { for (i = 0; i < 256; i++) code[sprintf("%c", i)] = i }
  {
    h = 0
    for (i = 1; i <= length($0); i++) h = (h * 31 + code[substr($0, i, 1)]) % 2147483647
    if (h % legs + 1 == leg) print
  }' <<<"$names")

total=$(grep -c . <<<"$names" || true)
count=$(grep -c . <<<"$kept" || true)
echo "go-test-split.sh: leg $leg of $legs keeps $count of the $total tests go test -list names in ${packages[*]}" >&2
if [ "$count" -eq 0 ]; then
  echo '^$'
else
  echo "^($(paste -sd '|' <<<"$kept"))\$"
fi
