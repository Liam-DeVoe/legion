#!/usr/bin/env bash
# packages/envoy/scripts/callout-check.test.sh
#
# Proves callout-check.sh's verdicts without a stack: a fake natsprobe and a fake curl on PATH
# answer as the hub would, and an exported stack (DEV_STACK_CALLOUT=1) points the check at them,
# with a sleep standing in for the second broker it kills. callout-check.sh runs each probe under
# env -i, so nothing of this test's environment reaches the fakes; each case instead sets files in
# a control directory the fakes read, baked into them when they are written, and
# CALLOUT_CHECK_GRANT_ALL=1 in a case's environment is the grant-all file: the fixture service
# granting every session's delivery channel.
#
# It asserts that: (a) a hub that answers as the auth service does passes all six lines, in order,
# ending `all 6 assertions passed`, and the second broker is killed; (b) CALLOUT_CHECK_GRANT_ALL=1,
# a service that grants every session's delivery channel, FAILs the second line naming the channel
# it was served (the check's negative control); (c) a hub whose connection list names nothing
# reads UNKNOWN on line 5, not a pass; (d) a probe that prints nothing reads UNKNOWN; (e) an
# admission that takes longer than 2 s after the kill FAILs line 6; and (f) an exported stack
# missing a variable the check needs refuses with exit 2, naming it.
set -euo pipefail

project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
readonly check="${project_root}/packages/envoy/scripts/callout-check.sh"
temporary_dir="$(mktemp -d "${TMPDIR:-/tmp}/callout-check-test.XXXXXX")"
readonly temporary_dir
readonly fake_bin_dir="${temporary_dir}/bin"
readonly control_dir="${temporary_dir}/control"
mkdir -p "$fake_bin_dir" "$control_dir"

broker_stand_in=""
cleanup() {
  if [ -n "$broker_stand_in" ] && kill -0 "$broker_stand_in" 2>/dev/null; then
    kill "$broker_stand_in" 2>/dev/null || true
  fi
  rm -rf "$temporary_dir"
}
trap cleanup EXIT

readonly stack_broker="http://127.0.0.1:7001"
readonly session="example-worker-session"

# --- Fake natsprobe: answers as the hub behind the auth service does. A connection with no
# credential, or with a token from any token endpoint but the stack's broker, is not admitted; the
# session's own channel is served; another session's is refused in nats-server's words, unless the
# control directory says the service grants every channel (grant-all). With silent, it prints
# nothing; with slow, it takes 2.5 s once the second broker is gone. ---
cat >"${fake_bin_dir}/natsprobe" <<EOF
#!/usr/bin/env bash
control="${control_dir}"
EOF
cat >>"${fake_bin_dir}/natsprobe" <<'EOF'
subject="" none=false
while [ "$#" -gt 0 ]; do
  case "$1" in
    --subscribe) subject="$2"; shift 2 ;;
    --no-credential) none=true; shift ;;
    *) shift ;;
  esac
done
[ -e "$control/silent" ] && exit 1
if [ -e "$control/slow" ] && ! kill -0 "$(cat "$control/second-broker")" 2>/dev/null; then
  sleep 2.5
fi
if [ "$none" = true ] || [ "${AGENT_SECRETS_URL:-}" != "$(cat "$control/stack-broker")" ]; then
  echo "not admitted: Authorization Violation"
  exit 4
fi
echo "connected as session/${AGENT_SECRETS_SESSION:-}"
if [ "$subject" = "notifications.envoy.deliver.${AGENT_SECRETS_SESSION:-}" ] || [ -e "$control/grant-all" ]; then
  echo "served $subject"
  exit 0
fi
echo "refused: Permissions Violation for Subscription to \"$subject\""
exit 3
EOF
chmod +x "${fake_bin_dir}/natsprobe"

# --- Fake curl: the hub's connection list, naming the listener's service user, or nothing when
# the control directory says so (no-connections). ---
cat >"${fake_bin_dir}/curl" <<EOF
#!/usr/bin/env bash
if [ -e "${control_dir}/no-connections" ]; then
  echo '{"connections": []}'
else
  echo '{"connections": [{"authorized_user": "dispatch/dev-stack-dispatch"}, {"authorized_user": "listener/dev-stack-test"}]}'
fi
EOF
chmod +x "${fake_bin_dir}/curl"
printf '%s' "$stack_broker" >"${control_dir}/stack-broker"
: >"${temporary_dir}/exchange-token"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# run_check runs the check against the fakes, with the control files named, after a fresh second
# broker stand-in; it sets output and status.
output=""
status=0
run_check() {
  rm -f "${control_dir}/grant-all" "${control_dir}/no-connections" "${control_dir}/silent" "${control_dir}/slow"
  if [ "${CALLOUT_CHECK_GRANT_ALL:-}" = 1 ]; then
    : >"${control_dir}/grant-all"
  fi
  local name
  for name in "$@"; do
    : >"${control_dir}/${name}"
  done
  sleep 600 &
  broker_stand_in=$!
  disown "$broker_stand_in"
  printf '%s' "$broker_stand_in" >"${control_dir}/second-broker"
  status=0
  output="$(env PATH="${fake_bin_dir}:${PATH}" TMPDIR="$temporary_dir" \
    DEV_STACK_CALLOUT=1 \
    AGENT_SECRETS_URL="$stack_broker" \
    NATS_URLS="nats://127.0.0.1:7002" \
    DEV_STACK_EXCHANGE_TOKEN_FILE="${temporary_dir}/exchange-token" \
    DEV_STACK_EXCHANGE_SESSION="$session" \
    DEV_STACK_BROKER_PIDS="1 $broker_stand_in" \
    DEV_STACK_NATS_MONITOR_URL="http://127.0.0.1:7003" \
    bash "$check" 2>&1)" || status=$?
}
line() { printf '%s\n' "$output" | sed -n "${1}p"; }

# (a) A hub that answers as the auth service does.
run_check
want=(
  "PASS a session subscribes its own delivery channel"
  "PASS a session is refused another session's delivery channel: Permissions Violation for Subscription to \"notifications.envoy.deliver.${session}-other\""
  "PASS a connection with no credential is refused: Authorization Violation"
  "PASS a token another broker issued is refused: Authorization Violation"
  "PASS the listener is connected by its service token: the hub lists a connection authorized as listener/dev-stack-test"
)
for index in "${!want[@]}"; do
  [ "$(line $((index + 1)))" = "${want[$index]}" ] || fail "line $((index + 1)) is '$(line $((index + 1)))', want '${want[$index]}'; output:
$output"
done
[[ "$(line 6)" == "PASS one broker killed and a new session admitted within 2 s: three sessions, the slowest "*" ms" ]] ||
  fail "line 6 is '$(line 6)'"
[ "$(line 7)" = "all 6 assertions passed" ] && [ "$status" -eq 0 ] || fail "want 'all 6 assertions passed' and exit 0, got exit $status:
$output"
! kill -0 "$broker_stand_in" 2>/dev/null || fail "the check did not kill the second broker $broker_stand_in"
echo "PASS: a hub that answers as the auth service does passes all six lines, and the second broker is killed"

# (b) The negative control: a service that grants every session's delivery channel.
CALLOUT_CHECK_GRANT_ALL=1 run_check
[ "$(line 2)" = "FAIL a session is refused another session's delivery channel: it was served notifications.envoy.deliver.${session}-other" ] ||
  fail "with CALLOUT_CHECK_GRANT_ALL=1 line 2 is '$(line 2)'"
[ "$status" -eq 1 ] && [ "$(line 7)" = "1 of 6 assertions failed" ] || fail "with CALLOUT_CHECK_GRANT_ALL=1: exit $status, last line '$(line 7)'"
echo "PASS: CALLOUT_CHECK_GRANT_ALL=1 FAILs line 2, naming the channel it was served, and the run exits 1"

# (c) A connection list that names nothing.
run_check no-connections
[[ "$(line 5)" == "UNKNOWN the listener is connected by its service token: "*"named no connection" ]] || fail "an empty connection list: line 5 is '$(line 5)'"
[ "$status" -eq 1 ] || fail "an empty connection list: exit $status"
echo "PASS: a hub whose connection list names nothing reads UNKNOWN on line 5 and fails the run"

# (d) A probe that prints nothing.
run_check silent
[[ "$(line 1)" == "UNKNOWN a session subscribes its own delivery channel: natsprobe printed nothing"* ]] || fail "a silent probe: line 1 is '$(line 1)'"
[ "$status" -eq 1 ] || fail "a silent probe: exit $status"
echo "PASS: a probe that prints nothing reads UNKNOWN and fails the run"

# (e) An admission slower than 2 s once a broker is killed.
run_check slow
[[ "$(line 6)" == "FAIL one broker killed and a new session admitted within 2 s: attempt 1 took "*" ms" ]] || fail "a slow admission: line 6 is '$(line 6)'"
[ "$status" -eq 1 ] || fail "a slow admission: exit $status"
echo "PASS: an admission slower than 2 s after the kill FAILs line 6"

# (f) An exported stack missing a variable.
status=0
output="$(env PATH="${fake_bin_dir}:${PATH}" TMPDIR="$temporary_dir" DEV_STACK_CALLOUT=1 \
  AGENT_SECRETS_URL="$stack_broker" NATS_URLS="nats://127.0.0.1:7002" \
  DEV_STACK_EXCHANGE_TOKEN_FILE="${temporary_dir}/exchange-token" DEV_STACK_EXCHANGE_SESSION="$session" \
  DEV_STACK_BROKER_PIDS="1 2" bash "$check" 2>&1)" || status=$?
[ "$status" -eq 2 ] && [[ "$output" == *"DEV_STACK_NATS_MONITOR_URL is not set"* ]] ||
  fail "a stack without DEV_STACK_NATS_MONITOR_URL: exit $status, output $output"
echo "PASS: an exported stack missing DEV_STACK_NATS_MONITOR_URL is refused with exit 2, naming it"
