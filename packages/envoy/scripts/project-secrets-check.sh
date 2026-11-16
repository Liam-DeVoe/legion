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

# --- The stack, and what this run reads off its banner. ---
if [ -z "$LIVE_BROKER_URL" ]; then
  start_stack
  BROKER_URL="$(exported AGENT_SECRETS_URL)"
  DISPATCH_URL="$(exported DISPATCH_URL)"
  STACK_DIR="$(exported DEV_STACK_DIR)"
  PRIV_PROJECT="$(exported DEV_STACK_PRIV_PROJECT)"
  PRIV_SECRET="$(exported DEV_STACK_PRIV_SECRET)"
  PRIV_OWNER="$(exported DEV_STACK_PRIV_OWNER)"
  PRIV_SERVICE="$(exported DEV_STACK_PRIV_SERVICE)"
  PRIV_EXCHANGE_TOKEN_FILE="$(exported DEV_STACK_PRIV_EXCHANGE_TOKEN_FILE)"
  PRIV_POD_TOKEN_FILE="$(exported DEV_STACK_PRIV_POD_TOKEN_FILE)"
  AIRLOCK_PROXY_URL="$(exported DEV_STACK_AIRLOCK_PROXY_URL)"
  AIRLOCK_BOUND_URL="$(exported DEV_STACK_AIRLOCK_BOUND_URL)"
  AIRLOCK_CA_CERT_FILE="$(exported DEV_STACK_AIRLOCK_CA_CERT_FILE)"
  BIN_DIR="$STACK_DIR/bin"
  FAKE_SECRETS_FILE="$STACK_DIR/fake-secrets.json"
  pass "the stack is ready ($STACK_DIR)"
  note "broker $BROKER_URL, Dispatch $DISPATCH_URL, airlock proxy $AIRLOCK_PROXY_URL"
  note "$PRIV_PROJECT's pod trades $PRIV_EXCHANGE_TOKEN_FILE and enrols as the service $PRIV_SERVICE"
else
  BROKER_URL="$LIVE_BROKER_URL"
  DISPATCH_URL="$LIVE_DISPATCH_URL"
  PRIV_SECRET="$LIVE_SECRET"
  pass "live mode against $BROKER_URL and $DISPATCH_URL, secret $PRIV_SECRET"
fi

# --- The secrets this run writes, unless it is live: PRIV_KEY of the private project, shared and
# agent tier, so owner and tier alone would give it to every session at once and only the project
# decides; PUB_KEY with no project at all, the control that must go on being served to everyone
# its owner and tier allow. The control run (--control untagged) writes PRIV_KEY with no project
# tag, which is exactly a secret the rule cannot see. ---
PUB_SECRET="PUB_KEY"
write_secrets() {
  local tag='"dispatch-project": "'"$PRIV_PROJECT"'", '
  [ "$CONTROL" != untagged ] || tag=""
  local prefix="example/agent-secrets/"
  local key="arn:aws:kms:us-east-1:111122223333:key/1234abcd-12ab-34cd-56ef-1234567890ab"
  python3 - "$FAKE_SECRETS_FILE" <<PY
import json, sys
path = sys.argv[1]
with open(path) as handle:
    store = json.load(handle)
store["secrets"] = [s for s in store["secrets"] if s["name"] not in (
    "${prefix}priv-key", "${prefix}pub-key")]
store["secrets"].append({"name": "${prefix}priv-key", "kms_key_id": "${key}",
    "tags": {${tag}"owner": "shared", "tier": "agent"}, "value": "priv-key-value"})
store["secrets"].append({"name": "${prefix}pub-key", "kms_key_id": "${key}",
    "tags": {"owner": "shared", "tier": "agent"}, "value": "pub-key-value"})
with open(path, "w") as handle:
    json.dump(store, handle, indent=2)
PY
  curl -sf -X POST "$BROKER_URL/v1/secrets/$PRIV_SECRET/reread" >/dev/null ||
    fail "the broker would not reread $PRIV_SECRET"
  curl -sf -X POST "$BROKER_URL/v1/secrets/$PUB_SECRET/reread" >/dev/null ||
    fail "the broker would not reread $PUB_SECRET"
}

# --- PRIV, private, with carol its owner. Avenue A's own route for this is
# PATCH /api/v1/projects/{key}/access {"visibility": "private"}, which succeeds only once t-a-5
# (DISPATCH_ACCESS_ACCOUNTS at boot) and t-a-20 (DISPATCH_PRIVATE_PROJECTS and the bus write) are
# on the branch under test; until then it answers 409 PRIVATE_PROJECTS_OFF. So this run writes the
# column directly, which is the one thing K3's answer and the read rule both read, and says so.
# When the route answers, this stand-in goes and the route takes its place.
make_priv_private() {
  local database="$1"
  psql "$database" -v ON_ERROR_STOP=1 -q \
    -c "insert into projects (key, name) values ('$PRIV_PROJECT', '$PRIV_PROJECT') on conflict (key) do nothing;" \
    -c "update projects set visibility = 'private', owner = '$PRIV_OWNER' where key = '$PRIV_PROJECT';" ||
    fail "could not make $PRIV_PROJECT private (the stand-in for PATCH /api/v1/projects/$PRIV_PROJECT/access, avenue A's t-a-5 and t-a-20)"
  note "made $PRIV_PROJECT private by writing its row: the stand-in for PATCH /api/v1/projects/$PRIV_PROJECT/access {\"visibility\":\"private\"} until avenue A's t-a-5 and t-a-20 land"
}

echo
echo "project-secrets-check: the rows below need avenue A's access routes (t-a-3) and its" >&2
echo "  DISPATCH_ACCESS_ACCOUNTS boot setting (t-a-5) on the branch under test." >&2
exit 1
