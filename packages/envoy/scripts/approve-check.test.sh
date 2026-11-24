#!/usr/bin/env bash
# packages/envoy/scripts/approve-check.test.sh
#
# approve-check.sh's exit status, from the steps library it records its steps with
# (scripts/lib/approve-check-steps.sh), with no stack: each case runs a sequence of step results in
# a subshell of its own and reads the status it ends with and the rows it wrote. The header of
# approve-check.sh promises 1 when a step failed, 3 when none failed and one is UNKNOWN, and 0 when
# every step passed; an early stop (a stack that did not start, a sign-in or a setup that did not
# complete) is UNKNOWN, so after a FAIL it must still end 1, since a caller reads 3 as "nothing
# failed".
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LIB="$SCRIPT_DIR/lib/approve-check-steps.sh"
ROOT="$(mktemp -d)"
trap 'rm -rf "$ROOT"' EXIT
failures=0

# run_case <name> <want status> <want rows> <commands>: the commands, after sourcing the library,
# in a subshell whose WORK is a directory of its own.
run_case() {
  local name="$1" want="$2" rows="$3" commands="$4" work status got
  work="$(mktemp -d "$ROOT/case.XXXXXX")"
  status=0
  (
    set -euo pipefail
    WORK="$work"
    FAILED=0
    UNKNOWN=0
    # shellcheck source=lib/approve-check-steps.sh
    . "$LIB"
    eval "$commands"
    finish
  ) >"$work/out" 2>&1 || status=$?
  got="$(wc -l <"$work/results.tsv" 2>/dev/null || echo 0)"
  got="${got//[[:space:]]/}"
  if [ "$status" = "$want" ] && [ "$got" = "$rows" ]; then
    printf 'PASS: %s (exit %s, %s rows)\n' "$name" "$status" "$got"
  else
    printf 'FAIL: %s: exit %s and %s rows, want exit %s and %s rows\n%s\n' "$name" "$status" "$got" "$want" "$rows" "$(cat "$work/out")"
    failures=$((failures + 1))
  fi
}

run_case "every step passed" 0 2 'result a PASS x; result b PASS y'
run_case "a step failed" 1 2 'result a FAIL x; result b PASS y'
run_case "an UNKNOWN and no failure" 3 2 'result a PASS x; result b UNKNOWN y'
run_case "a failure and an UNKNOWN" 1 2 'result a FAIL x; result b UNKNOWN y'
run_case "an early stop with nothing failed" 3 2 'result a PASS x; stop_unknown "b stack" "did not start"; result c PASS z'
run_case "a failure, then an early stop" 1 2 'result a FAIL x; stop_unknown "b stack" "did not start"; result c PASS z'
run_case "an early stop first" 3 1 'stop_unknown "sign-in" "printed no code"'

if [ "$failures" != 0 ]; then
  echo "approve-check.test: $failures case(s) failed" >&2
  exit 1
fi
echo "approve-check.test: every case passed"
