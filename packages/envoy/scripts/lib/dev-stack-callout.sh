# shellcheck shell=bash disable=SC2034 # dev-stack.sh reads what this file sets
# packages/envoy/scripts/lib/dev-stack-callout.sh
#
# Everything scripts/dev-stack.sh --callout adds to the stack. With it the hub runs behind the NATS
# auth service (internal/broker/natscallout), as the deployment's hub does once its static users
# are gone: natsd runs with -callout-keys, so every client but the service's own user is sent to
# the service, and the service runs in two brokers on the one database, in one queue group, as two
# replicas do. The brokers admit a session by its broker access token and a proof of its key, and
# the listener and Dispatch by the platform token the issuer writes each of them for the NATS
# audience (envoy-nats), listed in BROKER_NATS_SERVICES under their own machine ids. Neither holds
# a seed. The banner then also exports DEV_STACK_CALLOUT=1, NATS_AUTH=broker,
# DEV_STACK_NATS_SERVICE_TOKEN_FILE (the listener's NATS token), DEV_STACK_EXCHANGE_SESSION (the
# session this run registered the pod for), DEV_STACK_BROKER_PIDS, the two brokers' pids, and
# DEV_STACK_NATS_MONITOR_URL, natsd's monitor. The second broker is the one replica the stack
# outlives: killing it stops nothing else, which is how scripts/callout-check.sh proves a stopped
# replica delays no connection.
#
# Sourced, never run: dev-stack.sh sources it with --callout and only then, once its accounts and
# work directory are named, and what it sets here replaces the empty additions and the steps that
# do nothing which dev-stack.sh defines for a run without the option. The two steps read
# dev-stack.sh's own names: its processes, await and its readers, and start_broker.

# The audience a service's NATS token carries, the tokens the issuer writes for the listener and
# for Dispatch at it, and the directory natsd keeps the auth callout's three seeds in, which the
# brokers read.
callout_audience="envoy-nats"
callout_listener_token="$WORK_DIR/listener-nats-token"
callout_dispatch_token="$WORK_DIR/dispatch-nats-token"
callout_dir="$WORK_DIR/nats-callout"

# The issuer also writes the listener's and Dispatch's tokens for the NATS audience, and each
# presents its own to the hub as a service (NATS_SERVICE_TOKEN_FILE).
callout_issuer_args=(--token "$callout_audience=$LISTENER_ACCOUNT=$callout_listener_token"
  --token "$callout_audience=$API_ACCOUNT=$callout_dispatch_token")
callout_listener_env=(NATS_SERVICE_TOKEN_FILE="$callout_listener_token")
callout_dispatch_env=(NATS_SERVICE_TOKEN_FILE="$callout_dispatch_token")
# natsd runs behind the auth callout, making its seeds in callout_dir, and serves its monitor port
# too, the kernel's choice, whose connection list (/connz?auth=true) names the user the auth
# service admitted each connection as.
callout_natsd_args=(-callout-keys "$callout_dir" -m -1)

# callout_after_natsd waits for natsd to report that monitor port, and sets callout_broker_env,
# which has every broker also run the NATS auth service on that natsd: the seeds natsd made, the
# listener and Dispatch listed by account under their machine ids (the listener's is
# dev-stack.sh's LISTENER_MACHINE_ID), and the NATS audience their tokens carry.
callout_monitor_url=""
nats_monitor_in() { sed -n 's/.*Starting http monitor on 127\.0\.0\.1:\([0-9][0-9]*\)$/\1/p' "$1" | head -n1; }
callout_after_natsd() {
  await natsd "$NATS_PID" "$NATS_LOG" "$NATS_LOG" 'its monitor port' nats_monitor_in
  # shellcheck disable=SC2154 # await sets awaited
  callout_monitor_url="http://127.0.0.1:$awaited"
  echo "dev-stack: natsd monitor $callout_monitor_url" >&2
  callout_broker_env=(
    BROKER_NATS_URLS="$NATS_URL"
    BROKER_NATS_SEED_FILE="$callout_dir/service.seed"
    BROKER_NATS_ISSUER_SEED_FILE="$callout_dir/issuer.seed"
    BROKER_NATS_XKEY_SEED_FILE="$callout_dir/xkey.seed"
    BROKER_NATS_SERVICES="listener/$LISTENER_MACHINE_ID/*=$LISTENER_ACCOUNT dispatch/dev-stack-dispatch/*=$API_ACCOUNT"
    BROKER_NATS_K8S_AUDIENCE="$callout_audience"
  )
}

# callout_after_first_broker starts the second replica once the first has migrated the database
# they share, and sets BROKER2_PID; it returns once both answer the hub, so the listener and
# Dispatch, which start next, never wait out an authorization timeout. It then sets
# callout_exports, the lines the banner prints after its first block of exports: what a second
# shell needs to drive the hub behind the auth service, as this file's header lists them.
callout_answering_in() { grep -q 'the NATS auth service is answering' "$1" && echo answering; }
callout_after_first_broker() {
  local log="$LOG_DIR/broker-2.log"
  echo "dev-stack: starting the second broker..." >&2
  start_broker "$log"
  # shellcheck disable=SC2154 # start_broker sets started_pid
  BROKER2_PID=$started_pid
  await 'the second broker' "$BROKER2_PID" "$log" "$log" 'its bound address' broker_addr_in
  echo "dev-stack: broker-2 http://$awaited" >&2
  # shellcheck disable=SC2153 # BROKER_PID and BROKER_LOG are dev-stack.sh's, the first broker's
  await broker "$BROKER_PID" "$BROKER_LOG" "$BROKER_LOG" 'its NATS auth service answering' callout_answering_in
  await 'the second broker' "$BROKER2_PID" "$log" "$log" 'its NATS auth service answering' callout_answering_in
  callout_exports="
  export DEV_STACK_CALLOUT=1
  export NATS_AUTH=broker
  export DEV_STACK_NATS_SERVICE_TOKEN_FILE=$callout_listener_token
  export DEV_STACK_EXCHANGE_SESSION=$EXCHANGE_SESSION
  export DEV_STACK_BROKER_PIDS=\"$BROKER_PID $BROKER2_PID\"
  export DEV_STACK_NATS_MONITOR_URL=$callout_monitor_url"
}
