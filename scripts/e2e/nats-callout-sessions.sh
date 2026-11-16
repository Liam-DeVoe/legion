#!/usr/bin/env bash
# scripts/e2e/nats-callout-sessions.sh — the live proof of dispatch://INFRA-840's r-kinds and
# r-refusal with real sessions (its verification's check c-callout-kinds): an Oh My Pi session and
# a Claude Code session, each connecting to the hub with its own broker token (NATS_AUTH=broker),
# are admitted to their own channels, receive a direct message, and keep receiving across a
# restart of the listener and across a restart of the hub's server on the same store; and a probe
# run in one session's own environment is refused the other session's channel by name.
#
# The stack is packages/envoy/scripts/dev-stack.sh --callout: the brokers, the listener, Dispatch
# and nats-server behind the NATS auth service, each the process production runs, built from this
# checkout. The sessions are the two plugins, each the real code a host runs, in processes of
# their own and with no model: this checkout's Oh My Pi extension (packages/pi-envoy) under the
# stand-in host its own suite drives (role-takeover/omp-client.ts, whose heartbeat this script
# ticks before each message, as the host's timer would), and Claude Code's channel server
# (packages/claude-envoy) under a stand-in `claude` that speaks MCP to it and plays the Legion
# inbox it writes each delivery to (role-takeover/claude-client.ts). The Oh My Pi session is told
# NATS_AUTH=broker in its environment and the Claude Code session `natsAuth: broker` in its
# envoy.json, so both ways of naming it run. Each trades the stack's pod identity (the exchange
# token file the issuer wrote, AGENT_SECRETS_MACHINE_IDENTITY=kubernetes) for a token naming its
# own session, which this script first registers for that pod, as a deployment's daemon does.
# dev-stack.sh restarts natsd and the listener in place on SIGUSR1 and SIGUSR2.
#
#   LEGION_TEST_PG_DSN=postgres://<user>@127.0.0.1:<port>/<database>?sslmode=disable \
#     bash scripts/e2e/nats-callout-sessions.sh [--binaries <dir>]
#
# It needs a Postgres server it may create two databases on and drops them on exit
# (LEGION_TEST_PG_DSN, else DATABASE_URL, as dev-stack.sh reads them), go (unless --binaries, passed
# on to dev-stack.sh, names the stack's binaries), bun with this checkout's node_modules
# (`bun install`), psql, curl, jq and python3; no model, no container and no credential, so a
# GitHub-hosted runner runs it. It inherits no AGENT_SECRETS_*, DISPATCH_*, ENVOY_* or NATS_*
# variable of its caller's: a Legion pod exports its deployment's, and none may reach this stack.
#
# Each check prints `PASS <name>` or `FAIL <name>: <reason>`, every check runs whatever the one
# before it did, and the last line says how many passed; the run exits 0 only when all did. A
# receive that fails says whether the session's connection was admitted: the hub's connection list
# (/connz?auth=true) names an admitted session's connection by the user the auth service admitted it
# as, `session/<id>`, and a session it names nowhere FAILs `not admitted`.
#
#   receives                           a direct message to each session reaches it
#   receives-after-listener-restart    the listener restarted on its port; again
#   receives-after-hub-restart         natsd restarted on its port and store; again
#   probe-refused-other-session        natsprobe in the Oh My Pi session's environment is served its
#                                      own channel and refused the Claude Code session's, by name
#
# Negative control: NATS_CALLOUT_SESSIONS_CONTROL=seedless starts both sessions with no credential
# named (neither NATS_AUTH nor natsAuth), so the hub's auth service admits neither connection and
# every receive FAILs `not admitted`; the run then exits 1.
#
# NATS_CALLOUT_SESSIONS_EVIDENCE_DIR (default a fresh directory under TMPDIR, kept and printed)
# keeps the stack's logs, each client's output and every answer the checks read.
set -Eeuo pipefail

pg_dsn=${LEGION_TEST_PG_DSN:-${DATABASE_URL:-}}
evidence=${NATS_CALLOUT_SESSIONS_EVIDENCE_DIR:-}
control=${NATS_CALLOUT_SESSIONS_CONTROL:-}
for variable in $(compgen -e); do
  case $variable in
    AGENT_SECRETS_* | DISPATCH_* | ENVOY_* | NATS_* | DATABASE_URL | LEGION_TEST_PG_DSN) unset "$variable" ;;
  esac
done
root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
drivers=$root/packages/pi-envoy/scripts/role-takeover
channel_server=$root/packages/claude-envoy/bin/envoy-channel.ts

refuse() {
  echo "nats-callout-sessions: $*" >&2
  exit 2
}
case $control in
  "" | seedless) ;;
  *) refuse "NATS_CALLOUT_SESSIONS_CONTROL is \"$control\"; the one control is seedless" ;;
esac
[ -n "$pg_dsn" ] || refuse "name a Postgres server it may create databases on: LEGION_TEST_PG_DSN or DATABASE_URL"
for tool in bun psql curl jq python3; do
  command -v "$tool" >/dev/null || refuse "$tool is not on PATH"
done
[ -f "$root/node_modules/nats/package.json" ] || refuse "$root/node_modules has no nats package: run bun install"

[ -n "$evidence" ] || evidence=$(mktemp -d "${TMPDIR:-/tmp}/nats-callout-sessions-evidence.XXXXXX")
mkdir -p "$evidence/answers"
scratch=$(mktemp -d "${TMPDIR:-/tmp}/nats-callout-sessions.XXXXXX")
work=$scratch
mkdir -p "$work/logs"
echo "nats-callout-sessions: evidence in $evidence${control:+ (control: $control)}"

# ---- teardown -----------------------------------------------------------------------------------
stack_pid=""
scenario_pids=()
scenario_fds=()
cleanup() {
  local status=$1 pid fd
  trap - EXIT INT TERM
  set +e
  # A client's stdin closing is its shutdown: each deregisters and exits.
  for fd in "${scenario_fds[@]}"; do
    exec {fd}>&-
  done
  for pid in "${scenario_pids[@]}"; do
    for _ in $(seq 1 50); do
      kill -0 "$pid" 2>/dev/null || break
      sleep 0.1
    done
    kill "$pid" 2>/dev/null
    wait "$pid" 2>/dev/null
  done
  if [ -n "$stack_pid" ] && kill -0 "$stack_pid" 2>/dev/null; then
    kill -TERM "$stack_pid"
    wait "$stack_pid" 2>/dev/null
  fi
  cp -R "$work/logs" "$evidence/client-logs" 2>/dev/null
  [ -z "${DEV_STACK_DIR:-}" ] || cp -R "$DEV_STACK_DIR/logs" "$evidence/stack-logs" 2>/dev/null
  case $scratch in
    "${TMPDIR:-/tmp}"/nats-callout-sessions.*) rm -rf -- "$scratch" ;;
  esac
  echo "nats-callout-sessions: evidence kept in $evidence"
  exit "$status"
}
trap 'cleanup "$?"' EXIT
trap 'cleanup 130' INT
trap 'cleanup 143' TERM

# wait_for SECONDS COMMAND...: runs COMMAND every 0.2 s until it succeeds; 1 once SECONDS passed.
wait_for() {
  local deadline=$((SECONDS + $1))
  shift
  until "$@"; do
    ((SECONDS < deadline)) || return 1
    sleep 0.2
  done
}

# ---- the stack ----------------------------------------------------------------------------------
stack_log=$evidence/dev-stack.log
echo "nats-callout-sessions: starting dev-stack.sh --callout (log $stack_log)"
TMPDIR=$scratch bash "$root/packages/envoy/scripts/dev-stack.sh" --callout --database-url "$pg_dsn" "$@" \
  >"$stack_log" 2>&1 &
stack_pid=$!
stack_ready() {
  grep -q '^dev-stack: ready\.' "$stack_log" 2>/dev/null && return 0
  kill -0 "$stack_pid" 2>/dev/null || {
    tail -n 30 "$stack_log" >&2
    refuse "dev-stack.sh stopped before it was ready"
  }
  return 1
}
wait_for 1200 stack_ready || refuse "dev-stack.sh was not ready within 20 minutes; see $stack_log"
# The banner's exports, read as values: each `  export NAME=value` line, a quoted value unquoted.
while IFS= read -r line; do
  name=${line#  export }
  value=${name#*=}
  name=${name%%=*}
  value=${value#\"}
  value=${value%\"}
  [ "$name" = PATH ] && value="${value%%:*}:$PATH"
  printf -v "$name" '%s' "$value"
done < <(sed -n 's/^\(  export [A-Z_0-9]*=.*\)$/\1/p' "$stack_log")
for required in AGENT_SECRETS_URL ENVOY_URL NATS_URLS DEV_STACK_DIR DEV_STACK_PID \
  DEV_STACK_EXCHANGE_TOKEN_FILE DEV_STACK_POD_TOKEN_FILE DEV_STACK_REGISTRAR_TOKEN_FILE \
  DEV_STACK_NATS_MONITOR_URL; do
  [ -n "${!required:-}" ] || refuse "dev-stack.sh exported no $required"
done
command -v natsprobe >/dev/null || refuse "natsprobe is not among the stack's binaries"

# ---- the two sessions ---------------------------------------------------------------------------
run=$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')
omp_session=ses_ncs_omp_$run
claude_session=ses_ncs_claude_$run
# A session trades only under a name its pod is registered for: the registrar stands in for the
# daemon that writes that row as it starts a pod.
for session in "$omp_session" "$claude_session"; do
  code=$(curl -sS --max-time 20 -o "$evidence/answers/register-$session.json" -w '%{http_code}' \
    -X POST "$AGENT_SECRETS_URL/v1/oauth/exchange/pod-sessions" \
    -H "Authorization: Bearer $(cat "$DEV_STACK_REGISTRAR_TOKEN_FILE")" -H 'Content-Type: application/json' \
    -d "$(jq -cn --arg p "$(cat "$DEV_STACK_POD_TOKEN_FILE")" --arg s "$session" '{pod_token: $p, session: $s}')") || code=unreachable
  [ "$code" = 200 ] || [ "$code" = 201 ] || [ "$code" = 204 ] ||
    refuse "registering the pod for $session answered $code: $(head -c 300 "$evidence/answers/register-$session.json")"
done

# What both sessions trade with: the stack's pod identity, at its broker.
broker_env=(
  AGENT_SECRETS_URL="$AGENT_SECRETS_URL"
  AGENT_SECRETS_MACHINE_IDENTITY=kubernetes
  AGENT_SECRETS_MACHINE_TOKEN_FILE="$DEV_STACK_EXCHANGE_TOKEN_FILE"
)
heartbeat_ms=5000
client_reply_timeout=60
# clients.sh's client_fifo, client_ready and send: one stdin fifo per client, its ready line, and
# one command answered on its stdout.
# shellcheck source=packages/pi-envoy/scripts/role-takeover/clients.sh
source "$drivers/clients.sh"

# The Oh My Pi session, told NATS_AUTH=broker in its environment.
omp_home=$work/omp-home
mkdir -p "$omp_home/cwd"
jq -n --arg url "$ENVOY_URL" --arg nats "$NATS_URLS" \
  '{envoy: {url: $url, natsUrls: [$nats]}, dispatch: {enabled: false}, askgate: {enabled: false}}' \
  >"$omp_home/envoy.json"
omp_auth=(NATS_AUTH=broker)
[ "$control" != seedless ] || omp_auth=()
client_fifo omp
(cd -- "$root/packages/pi-envoy" &&
  exec env -i PATH="$PATH" HOME="$omp_home" TMPDIR="$scratch" ENVOY_CONFIG_FILE="$omp_home/envoy.json" \
    ENVOY_HEARTBEAT_MS="$heartbeat_ms" ROLE_TAKEOVER_CWD="$omp_home/cwd" \
    ROLE_TAKEOVER_CLIENT_LOG="$work/logs/omp-notices.log" "${broker_env[@]}" "${omp_auth[@]}" \
    bun --preload "$drivers/host-stub.ts" "$drivers/omp-client.ts") \
  <"$work/omp.stdin" >"${client_out[omp]}" 2>"$work/logs/omp.err" &
scenario_pids+=($!)
wait_for 90 client_ready omp || refuse "the Oh My Pi client did not start: $(tail -n 10 "$work/logs/omp.err")"
send omp "$(jq -cn --arg s "$omp_session" '{op: "start", session: $s}')"

# The Claude Code session, told `natsAuth: broker` in its envoy.json.
claude_config=$work/claude-config
claude_state=$work/claude-state
mkdir -p "$claude_config" "$claude_state"
jq -n --arg url "$ENVOY_URL" --arg nats "$NATS_URLS" --arg control "$control" \
  '{envoy: ({enabled: true, url: $url, natsUrls: [$nats]} + (if $control == "seedless" then {} else {natsAuth: "broker"} end)),
    dispatch: {enabled: false}}' >"$claude_config/envoy.json"
printf '%s\n' "${broker_env[@]}" >"$work/claude.env"
client_fifo claude
(cd -- "$root/packages/claude-envoy" &&
  exec env -i PATH="$PATH" HOME="$work/bun-home" TMPDIR="$scratch" \
    bun "$drivers/claude-client.ts" --server "$channel_server" --session "$claude_session" \
    --state-dir "$claude_state" --config-dir "$claude_config" --heartbeat-ms "$heartbeat_ms" \
    --stderr "$work/logs/claude-channel.err" --env-file "$work/claude.env" --inbox "$work/claude.inbox") \
  <"$work/claude.stdin" >"${client_out[claude]}" 2>"$work/logs/claude.err" &
scenario_pids+=($!)
wait_for 120 client_ready claude ||
  refuse "the Claude Code client did not start: $(tail -n 10 "$work/logs/claude.err") $(tail -n 10 "$work/logs/claude-channel.err" 2>/dev/null)"

# ---- the checks ---------------------------------------------------------------------------------
ran=0
passed=0
pass() {
  ran=$((ran + 1))
  passed=$((passed + 1))
  echo "PASS $1"
}
fail() {
  ran=$((ran + 1))
  echo "FAIL $1: $2"
}

# admitted_as SESSION: the hub's connection list names a connection the auth service admitted as
# that session's own user.
admitted_as() {
  curl -sf --max-time 10 "$DEV_STACK_NATS_MONITOR_URL/connz?auth=true&limit=1024" |
    jq -e --arg u "session/$1" '[.connections[]? | select(.authorized_user == $u)] | length > 0' >/dev/null
}

# received LABEL MARKER: that client's session holds a delivery naming MARKER.
received() {
  send "$1" '{"op":"deliveries"}'
  jq -e --arg m "$2" '(.deliveries // []) + (.turns // []) | any(contains($m))' <<<"$reply" >/dev/null
}

# message_each STEP: one direct message to each session, by the listener's own send route, then
# a check per session that it arrived, naming whether its connection was admitted when it did not.
message_each() {
  local step=$1 label session marker code
  # The Oh My Pi host's timer ticks a pass, which registers the session again, as it would by now.
  send omp '{"op":"heartbeat"}'
  for label in omp claude; do
    session=$omp_session
    [ "$label" = omp ] || session=$claude_session
    marker="nats-callout-sessions $step $label $run"
    code=$(curl -sS --max-time 20 -o "$evidence/answers/$step-$label-send.json" -w '%{http_code}' \
      -X POST "$ENVOY_URL/v1/messages/send" -H 'Content-Type: application/json' \
      -d "$(jq -cn --arg t "$session" --arg m "$marker" \
        '{source: "agent", source_session: "nats-callout-sessions", target_session: $t, message: $m}')") ||
      code="unreachable"
    if wait_for 45 received "$label" "$marker"; then
      if admitted_as "$session"; then
        pass "$step ($label): $session took the message, admitted as session/$session"
      else
        fail "$step ($label)" "$session took the message, but the hub lists no connection admitted as session/$session"
      fi
    elif admitted_as "$session"; then
      fail "$step ($label)" "admitted as session/$session, but the message (send answered $code) did not arrive within 45 s"
    else
      fail "$step ($label)" "not admitted: the hub lists no connection admitted as session/$session (send answered $code)"
    fi
  done
}

# restart WHAT SIGNAL: dev-stack.sh restarts that process in place, and says so once it serves.
restart() {
  local what=$1 signal=$2 before
  before=$(grep -c "^dev-stack: $what restarted " "$stack_log" || true)
  # shellcheck disable=SC2153 # DEV_STACK_PID is the banner's, read above by name
  kill "-$signal" "$DEV_STACK_PID"
  restarted() { [ "$(grep -c "^dev-stack: $what restarted " "$stack_log" || true)" -gt "$before" ]; }
  wait_for 60 restarted || refuse "dev-stack.sh did not restart $what on SIG$signal; see $stack_log"
  echo "   $(grep "^dev-stack: $what restarted " "$stack_log" | tail -n 1)"
}

# Both sessions connect once their tokens are traded; the first message waits for that.
wait_for 60 admitted_as "$omp_session" || true
wait_for 60 admitted_as "$claude_session" || true
message_each receives

restart listener USR2
message_each receives-after-listener-restart

restart natsd USR1
message_each receives-after-hub-restart

# natsprobe in the Oh My Pi session's own environment: its trade names that session, so the hub
# serves its channel and refuses the other session's, in nats-server's own words.
probe() {
  probe_rc=0
  probe_out=$(env -i PATH="$PATH" HOME="$work/probe-home" "${broker_env[@]}" AGENT_SECRETS_SESSION="$omp_session" \
    natsprobe --servers "$NATS_URLS" "$@" 2>&1) || probe_rc=$?
  printf '%s\n' "$probe_out" >>"$evidence/answers/probe.txt"
}
mkdir -p "$work/probe-home"
own=notifications.envoy.deliver.$omp_session
other=notifications.envoy.deliver.$claude_session
probe --subscribe "$own" --wait 1s
served=$(printf '%s\n' "$probe_out" | sed -n 's/^served //p' | head -n1)
own_rc=$probe_rc
probe --subscribe "$other" --wait 2s
refusal=$(printf '%s\n' "$probe_out" | sed -n 's/^refused: //p' | head -n1)
want="Permissions Violation for Subscription to \"$other\""
if [ "$own_rc" -ne 0 ] || [ "$served" != "$own" ]; then
  fail probe-refused-other-session "the probe was not served its own channel $own (exit $own_rc): $(head -c 300 "$evidence/answers/probe.txt")"
elif [ "$probe_rc" -eq 3 ] && [ "$refusal" = "$want" ]; then
  pass "probe-refused-other-session: served $own, refused: $refusal"
else
  fail probe-refused-other-session "the probe was not refused $other by name (exit $probe_rc): $probe_out"
fi

echo "nats-callout-sessions: $passed of $ran checks passed${control:+ (control: $control)}"
[ "$ran" -gt 0 ] && [ "$passed" -eq "$ran" ]
