#!/usr/bin/env bash
# packages/envoy/scripts/airlock-check.test.sh
#
# Proves that check k7 of airlock-check.sh refuses to start without the two names of the
# deployment whose consumers it drives, which this repository holds no default for:
# AIRLOCK_CHECK_GCLOUD_SECRET and AIRLOCK_CHECK_WORKSPACE_DOMAIN. A k7 run with either unset or
# empty exits 2 naming it, before it makes anything. No run here starts a stack: every run is
# pointed at a TMPDIR that does not exist, so a run the refusal lets through stops at its first
# step, making its work directory there, with mktemp's own error and exit 1.
#
# Negative control, run when this was written: drop k7's refusal from airlock-check.sh; every row
# of the four unset and empty cases fails, each run going on to mktemp's error (exit 1), and the two
# cases a run the refusal lets through still pass.
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly script="${project_root}/packages/envoy/scripts/airlock-check.sh"
temporary_dir="$(mktemp -d)"
readonly temporary_dir
trap 'rm -rf "$temporary_dir"' EXIT

# The two names, in neutral forms. Every run has both, unless it leaves one out (run_check
# --unset) or sets it empty.
export AIRLOCK_CHECK_GCLOUD_SECRET="GCLOUD_DEMO"
export AIRLOCK_CHECK_WORKSPACE_DOMAIN="example.com"
readonly absent_tmp="${temporary_dir}/absent"

failures=0
out="${temporary_dir}/out"
err="${temporary_dir}/err"
status=0

# run_check [--unset <variable>]... [env assignments...] -- <check id>: runs the script with
# TMPDIR at absent_tmp and each --unset variable taken out of its environment; sets $status.
run_check() {
  local unsets=() assignments=()
  while [ "$#" -gt 0 ] && [ "$1" != -- ]; do
    if [ "$1" = --unset ]; then
      unsets+=(-u "$2")
      shift 2
    else
      assignments+=("$1")
      shift
    fi
  done
  [ "$#" -eq 0 ] || shift
  : >"$out"
  : >"$err"
  status=0
  env "${unsets[@]}" TMPDIR="$absent_tmp" "${assignments[@]}" bash "$script" "$@" >"$out" 2>"$err" || status=$?
}

check() {
  if [ "$2" = true ]; then
    printf 'PASS: %s\n' "$1"
  else
    printf 'FAIL: %s\n' "$1"
    printf '  exit: %s\n' "$status"
    sed 's/^/  stdout: /' "$out"
    sed 's/^/  stderr: /' "$err"
    failures=$((failures + 1))
  fi
}
is() { [ "$1" = "$2" ] && echo true || echo false; }
says() { grep -qF -- "$1" "$err" && echo true || echo false; }
never_says() { grep -qF -- "$1" "$err" && echo false || echo true; }

for setting in AIRLOCK_CHECK_GCLOUD_SECRET AIRLOCK_CHECK_WORKSPACE_DOMAIN; do
  for shape in unset empty; do
    echo "case: k7 with ${setting} ${shape}"
    if [ "$shape" = unset ]; then
      run_check --unset "$setting" -- k7
    else
      run_check "${setting}=" -- k7
    fi
    check "exits 2" "$(is "$status" 2)"
    check "refuses naming ${setting}" "$(says "airlock-check: k7 needs ${setting}, which is unset or empty")"
    check "prints the usage" "$(says "usage: airlock-check.sh <id> [--keep-stack]")"
    check "refuses before it makes its work directory" "$(never_says "${absent_tmp}/airlock-check-k7.")"
  done
done

echo "case: k7 with both names set gets past the refusal"
run_check -- k7
check "goes on to make its work directory, and stops there with mktemp's error (exit 1)" \
  "$([ "$status" = 1 ] && grep -qF "${absent_tmp}/airlock-check-k7." "$err" && echo true || echo false)"
check "names neither setting" "$(never_says "AIRLOCK_CHECK_")"

echo "case: a check other than k7 needs neither name"
run_check --unset AIRLOCK_CHECK_GCLOUD_SECRET --unset AIRLOCK_CHECK_WORKSPACE_DOMAIN -- k1
check "goes on to make its work directory, and stops there with mktemp's error (exit 1)" \
  "$([ "$status" = 1 ] && grep -qF "${absent_tmp}/airlock-check-k1." "$err" && echo true || echo false)"
check "names neither setting" "$(never_says "AIRLOCK_CHECK_")"

if [ "$failures" -ne 0 ]; then
  printf '%s check(s) failed\n' "$failures"
  exit 1
fi
echo "all checks passed"
