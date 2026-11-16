#!/usr/bin/env bash
# packages/envoy/scripts/project-secrets-check.sh
# dispatch://INFRA-3862's R11, driven on the binaries production runs: a private project's secret
# goes only to a credential that names that project.
#
# It starts one scripts/dev-stack.sh run with --project-access (the real broker, the real Dispatch,
# the real dev-oidc-issuer, the real agent-secrets, the airlock's proxy), makes PRIV private with
# carol as its owner and bob on its access list, keeps PUB public, writes PRIV_KEY (tagged
# dispatch-project=PRIV, shared, agent tier) and PUB_KEY (untagged), and then runs one row per
# caller, asserting each answer and printing one line per row. A failure names the caller and the
# secret.
#
# The rows:
#   * PRIV's agent, the production path: the pod of legion-priv-worker, whose exchange entry maps
#     it to PRIV for carol. It trades its own projected token and expects a token of project scope
#     "named" carrying ["PRIV"], then enrols itself (agent-secrets enroll --pod) and expects both
#     PRIV_KEY and PUB_KEY granted.
#   * The refusal case, a dev-box session listing PRIV: carol's session on the stack's machine,
#     which no machine registration names. It reads avenue F's machine check when the broker under
#     test has it: F's refusal passes the row. Before F's check exists, the broker mints, and the
#     row passes only for a token of person scope carrying ["PRIV"] (D1's rule). Any other answer
#     fails the row. The line says which branch ran.
#   * alice's session listing PRIV: her person cannot see PRIV, so the trade and the enrolment are
#     both refused, with or without F's check.
#   * carol's session naming nothing: PUB_KEY granted, PRIV_KEY denied project-not-named.
#   * The machine's own session: PUB_KEY as today, PRIV_KEY denied.
#   * The airlock, with PRIV's pod: a grant of PRIV_KEY behind the airlock, then carol taken off
#     PRIV's access list, and the next proxied request refused 407 with grant-not-live, the grant
#     ended. While access holds, the same CONNECT is not 407.
#
#     What this row does NOT drive, and why: a bound host listens on port 443
#     (airlockproxy.hostHeaderOf trims ":443" alone, so any other port is a host header mismatch),
#     and a machine whose /proc/sys/net/ipv4/ip_unprivileged_port_start is above 443 cannot bind
#     one without privilege. Where the stack could bind one, the row reads the served request too;
#     where it could not, it reads the proxy's own CONNECT answer, which is where
#     requests.Machine.AirlockConnection decides. The line says which.
#
# The negative control, --control untagged, writes PRIV_KEY with no dispatch-project tag. Every
# caller then gets it, and the driver fails naming the first caller that should have been refused.
# It fails for the reason the probe would be blind: a secret the rule cannot see is exactly an
# untagged one.
#
# Live mode, --broker-url URL --dispatch-url URL --secret NAME: no stack and no write, the session
# half alone. It runs the production path from a pod whose account an exchange entry maps to the
# secret's project (AGENT_SECRETS_MACHINE_IDENTITY and AGENT_SECRETS_MACHINE_TOKEN_FILE name it)
# and the refusal case from the operator's own machine login listing that project.
#
# It needs the same tools dev-stack.sh needs (go unless --binaries, psql, curl, jq, python3) and a
# Postgres server to create this run's databases on: --database-url, else LEGION_TEST_PG_DSN, else
# DATABASE_URL.
#
#   LEGION_TEST_PG_DSN=… bash scripts/project-secrets-check.sh
#   LEGION_TEST_PG_DSN=… bash scripts/project-secrets-check.sh --control untagged
#   bash scripts/project-secrets-check.sh --broker-url https://secrets… --dispatch-url https://dispatch… --secret PROBE_PRIV
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

usage() {
  cat <<'EOF'
usage: project-secrets-check.sh [--database-url <postgres url>] [--binaries <dir>]
                                [--control untagged]
       project-secrets-check.sh --broker-url <url> --dispatch-url <url> --secret <NAME>

Drive dispatch://INFRA-3862's R11 on the binaries production runs: a private project's secret goes
only to a credential that names the project. With no --broker-url it starts one dev-stack.sh run
of its own and drives every caller against it.

  --database-url   the Postgres server dev-stack.sh creates this run's databases on
  --binaries       prebuilt binaries, as dev-stack.sh takes them
  --control untagged
                   write PRIV_KEY with no dispatch-project tag: every caller then gets it and this
                   run fails, naming the first caller that should have been refused
  --broker-url, --dispatch-url, --secret
                   live mode: drive the session half against a running deployment, writing nothing
EOF
}

DATABASE_URL_OPTION=""
BINARIES_OPTION=""
CONTROL=""
LIVE_BROKER_URL=""
LIVE_DISPATCH_URL=""
LIVE_SECRET=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --database-url) DATABASE_URL_OPTION="${2:-}"; shift 2 ;;
    --database-url=*) DATABASE_URL_OPTION="${1#--database-url=}"; shift ;;
    --binaries) BINARIES_OPTION="${2:-}"; shift 2 ;;
    --binaries=*) BINARIES_OPTION="${1#--binaries=}"; shift ;;
    --control)
      CONTROL="${2:-}"
      if [ "$CONTROL" != untagged ]; then
        echo "project-secrets-check: --control takes untagged, not '${CONTROL}'" >&2
        exit 2
      fi
      shift 2
      ;;
    --broker-url) LIVE_BROKER_URL="${2:-}"; shift 2 ;;
    --dispatch-url) LIVE_DISPATCH_URL="${2:-}"; shift 2 ;;
    --secret) LIVE_SECRET="${2:-}"; shift 2 ;;
    --help | -h) usage; exit 0 ;;
    *)
      echo "project-secrets-check: unexpected argument $1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [ -n "$LIVE_BROKER_URL$LIVE_DISPATCH_URL$LIVE_SECRET" ]; then
  if [ -z "$LIVE_BROKER_URL" ] || [ -z "$LIVE_DISPATCH_URL" ] || [ -z "$LIVE_SECRET" ]; then
    echo "project-secrets-check: live mode needs --broker-url, --dispatch-url and --secret together" >&2
    exit 2
  fi
  if [ -n "$CONTROL" ]; then
    echo "project-secrets-check: --control writes a secret, which live mode never does" >&2
    exit 2
  fi
fi

# --- What every row prints, and what a failure says. Each row's line names the caller and the
# secret, so a reader of the output alone knows which credential was refused what. ---
rows_failed=0
pass() { printf 'PASS: %s\n' "$*"; }
fail() {
  printf 'FAIL: %s\n' "$*" >&2
  rows_failed=$((rows_failed + 1))
}
note() { printf '      %s\n' "$*"; }

# --- The stack, unless this is live mode. Its banner is read for the exports every row drives
# through, exactly as a person reads them. ---
WORK_DIR=""
STACK_PID=""
STACK_LOG=""
cleanup() {
  local rc=$?
  if [ -n "$STACK_PID" ] && kill -0 "$STACK_PID" 2>/dev/null; then
    kill -TERM "$STACK_PID" 2>/dev/null || true
    wait "$STACK_PID" 2>/dev/null || true
  fi
  exit "$rc"
}
trap cleanup EXIT INT TERM

# exported reads one "export NAME=..." line of the stack's banner.
exported() { grep -o "export $1=.*" "$STACK_LOG" | head -n1 | cut -d= -f2- || true; }

start_stack() {
  WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/project-secrets-check.XXXXXX")"
  STACK_LOG="$WORK_DIR/dev-stack.log"
  : >"$STACK_LOG"
  local -a options=(--project-access)
  [ -z "$DATABASE_URL_OPTION" ] || options+=(--database-url "$DATABASE_URL_OPTION")
  [ -z "$BINARIES_OPTION" ] || options+=(--binaries "$BINARIES_OPTION")
  bash "$SCRIPT_DIR/dev-stack.sh" "${options[@]}" >"$STACK_LOG" 2>&1 &
  STACK_PID=$!
  local waited=0
  while [ "$waited" -lt 600 ]; do
    grep -q 'dev-stack: ready\.' "$STACK_LOG" 2>/dev/null && return 0
    kill -0 "$STACK_PID" 2>/dev/null || break
    sleep 0.5
    waited=$((waited + 1))
  done
  echo "project-secrets-check: the stack never reached ready; its log:" >&2
  cat "$STACK_LOG" >&2
  exit 1
}

echo "project-secrets-check: this driver needs Dispatch's access routes (avenue A's t-a-3) and its" >&2
echo "  DISPATCH_ACCESS_ACCOUNTS boot setting (t-a-5). Run it on a branch that has both." >&2
if [ -z "$LIVE_BROKER_URL" ]; then
  start_stack
  pass "the stack is ready ($(exported DEV_STACK_DIR))"
  note "PRIV is $(exported DEV_STACK_PRIV_PROJECT), its pod $(exported DEV_STACK_PRIV_ACCOUNT)"
else
  pass "live mode against $LIVE_BROKER_URL and $LIVE_DISPATCH_URL, secret $LIVE_SECRET"
fi

echo
echo "project-secrets-check: not yet implemented past this point." >&2
echo "  The rows above are the stack; the rows below need avenue A's routes to make PRIV private." >&2
exit 1
