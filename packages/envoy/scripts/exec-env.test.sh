#!/usr/bin/env bash
# packages/envoy/scripts/exec-env.test.sh
#
# Proves, with no stack, no broker build and no Postgres, how lib/exec-env.sh starts a process,
# the helper dev-stack.sh starts each of its processes with (start_with_only):
#   (b) a readonly variable the starting shell exports reaches the started program no more: no
#       unset removes it, and exec_with_env takes off its export instead;
#   (c) a start that names a readonly variable among its settings is refused, naming the variable,
#       and runs nothing, in a subshell that keeps set -e as the callers' do;
#   (e) under bash -x, broker_env_of and with_broker_env trace nothing of what they copy.
# The rows keep the letters of judge-check.test.sh, whose rows (a) and (d) held judge-check.sh's
# own broker start and went with that script. Needs bash and Linux's /proc.
set -euo pipefail
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
work="$(mktemp -d "${TMPDIR:-/tmp}/exec-env-test.XXXXXX")"
stand_in=""
trap '[[ -z "$stand_in" ]] || kill "$stand_in" 2>/dev/null || true; rm -rf "$work"' EXIT
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

# --- (b) and (c) exec_with_env and a readonly variable of the starting shell, each run in a
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

# --- (e) Under bash -x, broker_env_of and with_broker_env trace nothing of the environment they
# copy: a stand-in process holds a password in its database URL. ---
"$real_env" -i PATH="/usr/bin:/bin" BROKER_DATABASE_URL='postgres://broker:not-a-secret@127.0.0.1:5999/db' \
  STAND_IN=1 sleep 60 &
stand_in=$!
# broker_env_of reads the stand-in's environment once it is sleep's, not env's before it execs
# sleep; a stand-in that is not sleep by the end of the wait ends the test, naming what it is.
stand_in_cmdline() { tr '\0' ' ' <"/proc/$stand_in/cmdline" 2>/dev/null; }
for _ in $(seq 1 50); do
  [[ "$(stand_in_cmdline)" == sleep* ]] && break
  sleep 0.1
done
[[ "$(stand_in_cmdline)" == sleep* ]] ||
  { printf 'FAIL: the stand-in is not sleep after 5 s: %s\n' "$(stand_in_cmdline)"; exit 1; }
cat >"$work/trace-caller" <<'EOF'
. "$EXEC_ENV_LIB"
broker_env_of "$TRACE_STAND_IN"
(with_broker_env "$TRACE_ENV") >/dev/null
EOF
EXEC_ENV_LIB="$script_dir/lib/exec-env.sh" TRACE_STAND_IN="$stand_in" TRACE_ENV="$real_env" \
  bash -x "$work/trace-caller" 2>"$work/trace" || true
traced="$(grep -n not-a-secret "$work/trace" | cut -c1-160 || true)"
check "under bash -x, broker_env_of and with_broker_env trace no line holding a setting of the environment they copy" \
  test -z "$traced"
[[ -z "$traced" ]] || printf 'trace lines holding the password: %s\n' "$traced"

if [[ "$failed" -ne 0 ]]; then
  printf 'exec-env.test: %s failed\n' "$failed"
  exit 1
fi
echo 'exec-env.test: passed'
