# shellcheck shell=bash
# packages/envoy/scripts/lib/dev-stack-callout.sh
#
# Everything scripts/dev-stack.sh --callout adds to the stack: the hub behind the NATS auth
# service (internal/broker/natscallout), as dev-stack.sh's header describes it.
#
# Sourced, never run. dev-stack.sh sources it once its work directory is laid out (WORK_DIR and
# LOG_DIR), and calls each function below at the one step of its start that function belongs to.
# Each function sets the array or variable it names; without --callout (dev-stack.sh's CALLOUT is
# not true) it sets that array or variable empty and does nothing else, so a run without the
# option is the stack it always was. The functions read dev-stack.sh's own names: its accounts
# and processes, await and its readers, and start_broker.

# The audience a service's NATS token carries, the token the issuer writes for the listener and
# for Dispatch at it, the machine id BROKER_NATS_SERVICES lists Dispatch under (the listener's is
# dev-stack.sh's LISTENER_MACHINE_ID), the directory natsd keeps the auth callout's three seeds
# in, which the brokers read, and the log of the second broker: the one replica the stack
# outlives.
NATS_AUDIENCE="envoy-nats"
LISTENER_NATS_TOKEN_FILE="$WORK_DIR/listener-nats-token"
DISPATCH_NATS_TOKEN_FILE="$WORK_DIR/dispatch-nats-token"
DISPATCH_MACHINE_ID="dev-stack-dispatch"
CALLOUT_DIR="$WORK_DIR/nats-callout"
BROKER2_LOG="$LOG_DIR/broker-2.log"

# callout_issuer_args sets callout_tokens, the issuer's arguments that also write the listener's
# and Dispatch's tokens for the NATS audience, which each presents to the hub as a service
# (NATS_SERVICE_TOKEN_FILE).
callout_issuer_args() {
  callout_tokens=()
  [ "$CALLOUT" = true ] || return 0
  # shellcheck disable=SC2034 # dev-stack.sh passes it to the issuer
  callout_tokens=(--token "$NATS_AUDIENCE=$LISTENER_ACCOUNT=$LISTENER_NATS_TOKEN_FILE"
    --token "$NATS_AUDIENCE=$API_ACCOUNT=$DISPATCH_NATS_TOKEN_FILE")
}

# callout_natsd_args MONITOR sets natsd_callout_args: natsd runs behind the auth callout, making its
# seeds in CALLOUT_DIR, and serves its monitor port too, whose connection list (/connz?auth=true)
# names the user the auth service admitted each connection as. MONITOR is that port: -1, the
# kernel's choice, at the start, and the port it reported then at an in-place restart
# (dev-stack.sh's SIGUSR1), since every client of the stack was told it.
callout_natsd_args() {
  natsd_callout_args=()
  [ "$CALLOUT" = true ] || return 0
  # shellcheck disable=SC2034 # dev-stack.sh passes it to natsd
  natsd_callout_args=(-callout-keys "$CALLOUT_DIR" -m "$1")
}

# callout_natsd_monitor waits for natsd to report that monitor port, and sets NATS_MONITOR_URL.
nats_monitor_in() { sed -n 's/.*Starting http monitor on 127\.0\.0\.1:\([0-9][0-9]*\)$/\1/p' "$1" | head -n1; }
callout_natsd_monitor() {
  NATS_MONITOR_URL=""
  [ "$CALLOUT" = true ] || return 0
  await natsd "$NATS_PID" "$NATS_LOG" "$NATS_LOG" 'its monitor port' nats_monitor_in
  # shellcheck disable=SC2154 # await sets awaited
  NATS_MONITOR_URL="http://127.0.0.1:$awaited"
  echo "dev-stack: natsd monitor $NATS_MONITOR_URL" >&2
}

# callout_broker_env sets broker_nats_env, which has every broker also run the NATS auth service
# on that natsd: the seeds natsd made, the listener and Dispatch listed by account under their
# machine ids, and the NATS audience their tokens carry.
callout_broker_env() {
  broker_nats_env=()
  [ "$CALLOUT" = true ] || return 0
  # shellcheck disable=SC2034 # dev-stack.sh's start_broker passes it to each broker
  broker_nats_env=(
    BROKER_NATS_URLS="$NATS_URL"
    BROKER_NATS_SEED_FILE="$CALLOUT_DIR/service.seed"
    BROKER_NATS_ISSUER_SEED_FILE="$CALLOUT_DIR/issuer.seed"
    BROKER_NATS_XKEY_SEED_FILE="$CALLOUT_DIR/xkey.seed"
    BROKER_NATS_SERVICES="listener/$LISTENER_MACHINE_ID/*=$LISTENER_ACCOUNT dispatch/$DISPATCH_MACHINE_ID/*=$API_ACCOUNT"
    BROKER_NATS_K8S_AUDIENCE="$NATS_AUDIENCE"
  )
}

# callout_after_first_broker starts the second replica once the first has migrated the database
# they share, and sets BROKER2_PID; it returns once both answer the hub, so the listener and
# Dispatch, which start next, never wait out an authorization timeout.
callout_answering_in() { grep -q 'the NATS auth service is answering' "$1" && echo answering; }
callout_after_first_broker() {
  [ "$CALLOUT" = true ] || return 0
  echo "dev-stack: starting the second broker..." >&2
  start_broker "$BROKER2_LOG"
  # shellcheck disable=SC2154 # start_broker sets started_pid
  BROKER2_PID=$started_pid
  await 'the second broker' "$BROKER2_PID" "$BROKER2_LOG" "$BROKER2_LOG" 'its bound address' broker_addr_in
  echo "dev-stack: broker-2 http://$awaited" >&2
  # shellcheck disable=SC2153 # BROKER_PID and BROKER_LOG are dev-stack.sh's, the first broker's
  await broker "$BROKER_PID" "$BROKER_LOG" "$BROKER_LOG" 'its NATS auth service answering' callout_answering_in
  await 'the second broker' "$BROKER2_PID" "$BROKER2_LOG" "$BROKER2_LOG" 'its NATS auth service answering' callout_answering_in
}

# callout_listener_env and callout_dispatch_env set listener_nats_env and dispatch_nats_env: the
# listener and Dispatch connect to the hub as services, each presenting the NATS-audience token
# the issuer keeps for it, and holding no seed.
callout_listener_env() {
  listener_nats_env=()
  [ "$CALLOUT" = true ] || return 0
  # shellcheck disable=SC2034 # dev-stack.sh passes it to the listener
  listener_nats_env=(NATS_SERVICE_TOKEN_FILE="$LISTENER_NATS_TOKEN_FILE")
}
callout_dispatch_env() {
  dispatch_nats_env=()
  [ "$CALLOUT" = true ] || return 0
  # shellcheck disable=SC2034 # dev-stack.sh passes it to Dispatch
  dispatch_nats_env=(NATS_SERVICE_TOKEN_FILE="$DISPATCH_NATS_TOKEN_FILE")
}

# callout_banner_exports sets CALLOUT_EXPORTS, the lines the banner prints after its first block
# of exports: what a second shell needs to drive the hub behind the auth service: that it is, the
# session this run registered the pod for, the listener's NATS token, the two brokers' pids, the
# second of which the stack outlives, and natsd's monitor.
callout_banner_exports() {
  CALLOUT_EXPORTS=""
  [ "$CALLOUT" = true ] || return 0
  # shellcheck disable=SC2034 # dev-stack.sh prints it in its banner
  CALLOUT_EXPORTS="
  export DEV_STACK_CALLOUT=1
  export NATS_AUTH=broker
  export DEV_STACK_NATS_SERVICE_TOKEN_FILE=$LISTENER_NATS_TOKEN_FILE
  export DEV_STACK_EXCHANGE_SESSION=$EXCHANGE_SESSION
  export DEV_STACK_BROKER_PIDS=\"$BROKER_PID $BROKER2_PID\"
  export DEV_STACK_NATS_MONITOR_URL=$NATS_MONITOR_URL"
}
