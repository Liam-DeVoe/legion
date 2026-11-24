#!/usr/bin/env bash
# packages/envoy/scripts/exec-env.test.sh
#
# Proves, with no stack, no broker build and no Postgres, how lib/exec-env.sh starts a process,
# the helper dev-stack.sh starts each of its processes with (start_with_only):
#   (a) a readonly variable the starting shell exports reaches the started program no more: no
#       unset removes it, and exec_with_env takes off its export instead;
#   (b) a start that names a readonly variable among its settings is refused, naming the variable,
#       and runs nothing, in a subshell that keeps set -e as the callers' do.
# Needs bash.
set -euo pipefail
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/exec-env-test.XXXXXX")"
trap 'rm -rf "$work"' EXIT
failed=0
check() { # <label> <command>...
  local label="$1"
  shift
  if "$@"; then
    printf 'PASS: %s\n' "$label"
  else
    printf 'FAIL: %s\n' "$label"
    failed=$((failed + 1))
  fi
}

# shellcheck source-path=SCRIPTDIR source=lib/exec-env.sh
. "$script_dir/lib/exec-env.sh"
real_env="$(command -v env)"

# --- (a) and (b) exec_with_env and a readonly variable of the starting shell, each run in a
# subshell of its own in the background, as its callers run it: `( exec_with_env ... ) >log &`.
# That subshell keeps this test's set -e, as theirs keep their scripts'; a command substitution
# would drop it, and with it the way a failed assignment ends the shell. ---
(
  export EXEC_ENV_TEST_READONLY=not-a-secret-readonly
  readonly EXEC_ENV_TEST_READONLY
  exec_with_env ONLY=1 -- "$real_env"
) >"$work/readonly.out" 2>&1 &
wait "$!" || true
readonly_reached="$(cat "$work/readonly.out")"
check "a readonly variable the starting shell exports reaches the started program no more" \
  test "$readonly_reached" = "ONLY=1"
[[ "$readonly_reached" == "ONLY=1" ]] || printf 'the program had: %s\n' "$readonly_reached"
refused_status=0
(
  # shellcheck disable=SC2034 # the start below names it: the row is that it cannot take a value
  readonly EXEC_ENV_TEST_NAMED=1
  exec_with_env EXEC_ENV_TEST_NAMED=2 -- "$real_env"
) >"$work/refused.out" 2>&1 &
wait "$!" || refused_status=$?
refused_said="$(cat "$work/refused.out")"
check "a start that names a readonly variable among its settings is refused with 2, naming it, and runs nothing" \
  test "$refused_status:$refused_said" = "2:${0##*/}: refused to start ${real_env}: EXEC_ENV_TEST_NAMED is readonly in this shell, so it cannot take the value the start gives it"
[[ "$refused_status" -eq 2 ]] || printf 'the start exited %s, saying: %s\n' "$refused_status" "$refused_said"

if [[ "$failed" -ne 0 ]]; then
  printf 'exec-env.test: %s failed\n' "$failed"
  exit 1
fi
echo 'exec-env.test: passed'
