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
# The fake hub keeps the killed broker's connection in its connection list for 300 ms after the
# kill, as a real hub does until it reads that connection's close, and a session that connects
# meanwhile is routed to the killed replica's place in the queue group and refused (measured on the
# real stack: a session opened at once after the kill was refused at the hub's 2 s authorization
# timeout, and the remaining broker never saw it).
#
# It asserts that: (a) a hub that answers as the auth service does passes all six lines, in order,
# ending `all 6 assertions passed`, the second broker is killed, and the check waits until the hub
# drops the killed broker's connection before it opens a session; (b) CALLOUT_CHECK_GRANT_ALL=1,
# a service that grants every session's delivery channel, FAILs the second line naming the channel
# it was served (the check's negative control); (c) a hub whose connection list names nothing
# reads UNKNOWN on line 5, not a pass; (d) a probe that prints nothing reads UNKNOWN; (e) an
# admission that takes longer than 2 s after the kill FAILs line 6; (f) an exported stack missing
# a variable the check needs refuses with exit 2, naming it; (g) a hub that never drops the killed
# broker's connection FAILs line 6; (h) a connection list that cannot be read after the kill reads
# UNKNOWN on line 6; and (i) a hub listing one auth service connection before the kill reads
# UNKNOWN on line 6 and kills nothing.
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

# --- The fake hub's view of the second broker: alive while its stand-in runs; once the stand-in is
# gone, listed while the hub still lists its connection (always with no-drop, else for drop-ms
# after the stand-in died, which run_check's watcher writes to dead-at.<pid>), then dropped. A
# reader that finds the stand-in gone before the watcher wrote dead-at reads the death as now. ---
cat >"${fake_bin_dir}/hub-state" <<EOF
#!/usr/bin/env bash
control="${control_dir}"
EOF
cat >>"${fake_bin_dir}/hub-state" <<'EOF'
broker="$(cat "$control/second-broker")"
if kill -0 "$broker" 2>/dev/null; then
  echo alive
elif [ -e "$control/no-drop" ]; then
  echo listed
else
  now="$(date +%s%3N)"
  dead_at="$(cat "$control/dead-at.$broker" 2>/dev/null || echo "$now")"
  if [ $((now - dead_at)) -lt "$(cat "$control/drop-ms")" ]; then echo listed; else echo dropped; fi
fi
EOF
chmod +x "${fake_bin_dir}/hub-state"

# --- Fake natsprobe: answers as the hub behind the auth service does. A connection with no
# credential, or with a token from any token endpoint but the stack's broker, is not admitted; the
# session's own channel is served; another session's is refused in nats-server's words, unless the
# control directory says the service grants every channel (grant-all). While the hub still lists
# the killed broker's connection, a session is routed to that replica, which never answers, and
# is not admitted (at the hub's authorization timeout, shortened here). With silent, it prints
# nothing; with slow, it takes 2.5 s once the second broker is gone. ---
cat >"${fake_bin_dir}/natsprobe" <<EOF
#!/usr/bin/env bash
control="${control_dir}"
hub_state="${fake_bin_dir}/hub-state"
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
if [ "$("$hub_state")" = listed ]; then
  sleep 0.2
  echo "not admitted: Authorization Violation"
  exit 4
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

# --- Fake curl: the hub's connection list, naming Dispatch's and the listener's service users and
# one envoy-broker-callout connection for each broker the hub lists (two, or one with one-broker,
# and one fewer once the killed broker's is dropped); nothing with no-connections; and no answer
# at all once the second broker is gone with connz-down-after-kill. ---
cat >"${fake_bin_dir}/curl" <<EOF
#!/usr/bin/env bash
control="${control_dir}"
hub_state="${fake_bin_dir}/hub-state"
EOF
cat >>"${fake_bin_dir}/curl" <<'EOF'
if [ -e "$control/no-connections" ]; then
  echo '{"connections": []}'
  exit 0
fi
state="$("$hub_state")"
if [ -e "$control/connz-down-after-kill" ] && [ "$state" != alive ]; then
  exit 7
fi
callouts=2
[ -e "$control/one-broker" ] && callouts=1
[ "$state" = dropped ] && callouts=$((callouts - 1))
entries='{"name": "dispatch", "authorized_user": "dispatch/dev-stack-dispatch"}, {"name": "listener", "authorized_user": "listener/dev-stack-test"}'
for _ in $(seq 1 "$callouts"); do
  entries="$entries"', {"name": "envoy-broker-callout", "authorized_user": "UAUTHSERVICE"}'
done
echo "{\"connections\": [$entries]}"
EOF
chmod +x "${fake_bin_dir}/curl"
printf '%s' "$stack_broker" >"${control_dir}/stack-broker"
: >"${temporary_dir}/exchange-token"

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

# run_check runs the check against the fakes, with the control files named, after a fresh second
# broker stand-in (the last one ended first, since a case can leave it running) and a watcher that
# writes the time the stand-in died, which the fake hub's drop is counted from; it sets output and
# status.
output=""
status=0
run_check() {
  local name
  for name in grant-all no-connections silent slow no-drop connz-down-after-kill one-broker; do
    rm -f "${control_dir}/${name}"
  done
  if [ "${CALLOUT_CHECK_GRANT_ALL:-}" = 1 ]; then
    : >"${control_dir}/grant-all"
  fi
  for name in "$@"; do
    : >"${control_dir}/${name}"
  done
  printf '300' >"${control_dir}/drop-ms"
  if [ -n "$broker_stand_in" ] && kill -0 "$broker_stand_in" 2>/dev/null; then
    kill "$broker_stand_in" 2>/dev/null || true
  fi
  sleep 600 &
  broker_stand_in=$!
  disown "$broker_stand_in"
  printf '%s' "$broker_stand_in" >"${control_dir}/second-broker"
  (
    while kill -0 "$broker_stand_in" 2>/dev/null; do sleep 0.01; done
    date +%s%3N >"${control_dir}/dead-at.${broker_stand_in}"
  ) >/dev/null 2>&1 &
  disown "$!"
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
line6="$(line 6)"
[[ "$line6" =~ ^"PASS one broker killed and a new session admitted within 2 s: the hub dropped the killed broker's connection after "([0-9]+)" ms; three sessions, the slowest "([0-9]+)" ms, the first counted from the kill"$ ]] ||
  fail "line 6 is '$line6'"
[ "${BASH_REMATCH[1]}" -ge 300 ] && [ "${BASH_REMATCH[2]}" -ge "${BASH_REMATCH[1]}" ] ||
  fail "line 6 has the hub drop the killed broker's connection after ${BASH_REMATCH[1]} ms and its slowest session at ${BASH_REMATCH[2]} ms: the fake hub keeps that connection for 300 ms, so the check did not wait for the drop, or did not count the first session from the kill"
[ "$(line 7)" = "all 6 assertions passed" ] && [ "$status" -eq 0 ] || fail "want 'all 6 assertions passed' and exit 0, got exit $status:
$output"
! kill -0 "$broker_stand_in" 2>/dev/null || fail "the check did not kill the second broker $broker_stand_in"
echo "PASS: a hub that answers as the auth service does passes all six lines, the second broker is killed, and no session is opened before the hub drops its connection"

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

# (g) A hub that never drops the killed broker's connection.
run_check no-drop
[[ "$(line 6)" == "FAIL one broker killed and a new session admitted within 2 s: the hub still listed 2 connections named envoy-broker-callout "*" ms after the kill" ]] ||
  fail "a hub that never drops the killed broker's connection: line 6 is '$(line 6)'"
[ "$status" -eq 1 ] || fail "a hub that never drops the killed broker's connection: exit $status"
echo "PASS: a hub that never drops the killed broker's connection FAILs line 6"

# (h) A connection list that cannot be read once the broker is killed.
run_check connz-down-after-kill
[ "$(line 6)" = "UNKNOWN one broker killed and a new session admitted within 2 s: the hub's connection list (http://127.0.0.1:7003/connz) could not be read after the kill" ] ||
  fail "a connection list unreadable after the kill: line 6 is '$(line 6)'"
[ "$status" -eq 1 ] || fail "a connection list unreadable after the kill: exit $status"
echo "PASS: a connection list that cannot be read after the kill reads UNKNOWN on line 6 and fails the run"

# (i) One auth service connection before the kill: the check cannot see a drop, so it kills nothing.
run_check one-broker
[ "$(line 6)" = "UNKNOWN one broker killed and a new session admitted within 2 s: the hub lists 1 connection named envoy-broker-callout before the kill, not one for each of the two brokers" ] ||
  fail "one auth service connection before the kill: line 6 is '$(line 6)'"
[ "$status" -eq 1 ] || fail "one auth service connection before the kill: exit $status"
kill -0 "$broker_stand_in" 2>/dev/null || fail "with one auth service connection listed the check killed the second broker anyway"
echo "PASS: a hub listing one auth service connection before the kill reads UNKNOWN on line 6, and nothing is killed"
