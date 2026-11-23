#!/usr/bin/env bash
# packages/envoy/scripts/airlock-check.test.sh
#
# Tests the running-stack flags of scripts/airlock-check.sh and scripts/dev-airlock-lease.test.sh,
# with no stack, no Postgres and no network: a stub of each tool airlock-check.sh names that this
# machine lacks, and stack logs this test writes. It checks, one CHECK line each: that
# airlock-check.sh refuses --control without --stack-log, and --stack-log naming no file, as
# usage errors (exit 2); that on a log holding no ready banner it fails its setup (exit 1) and
# starts no stack; and that dev-airlock-lease.test.sh does the same for its --stack-log.
#
# Run from anywhere: packages/envoy/scripts/airlock-check.test.sh
set -euo pipefail

scripts="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin" "$work/tmp"
for tool in curl jq go python3 openssl node psql git; do
  command -v "$tool" >/dev/null 2>&1 || printf '#!/usr/bin/env bash\nexit 1\n' >"$work/bin/$tool"
done
chmod -R +x "$work/bin"
printf 'dev-airlock: starting dev-broker.sh (Postgres, dev-oidc-issuer, its broker)...\n' >"$work/no-banner.log"

failed=0
check() { # <name> <0 for a pass> <what failed>
  if [ "$2" = 0 ]; then echo "CHECK $1: PASS"; else echo "CHECK $1: FAIL: $3"; failed=1; fi
}
# run <script> <args...>: runs it under the stubs and this test's TMPDIR, setting status and out.
run() {
  status=0
  out=$(PATH="$work/bin:$PATH" TMPDIR="$work/tmp" bash "$scripts/$1" "${@:2}" 2>&1) || status=$?
}
# stacks: the work directories dev-airlock.sh made under this test's TMPDIR, which is none.
stacks() { find "$work/tmp" -maxdepth 1 -name 'agent-secrets-airlock.*' | wc -l; }

run airlock-check.sh k9 --control
check airlock-check-control-needs-a-stack-log "$([ "$status" = 2 ] && [[ $out == *"--stack-log names"* ]] && echo 0 || echo 1)" "it exited $status: $out"
run airlock-check.sh k9 --stack-log "$work/missing.log"
check airlock-check-stack-log-needs-a-file "$([ "$status" = 2 ] && echo 0 || echo 1)" "it exited $status: $out"
run airlock-check.sh k9 --stack-log "$work/no-banner.log"
check airlock-check-refuses-a-stack-that-is-not-up "$([ "$status" = 1 ] && [[ $out == *"CHECK setup: FAIL: $work/no-banner.log holds no 'dev-airlock: ready.' banner"* ]] && [ "$(stacks)" = 0 ] && echo 0 || echo 1)" \
  "it exited $status with $(stacks) stacks started: $out"

run dev-airlock-lease.test.sh --stack-log "$work/missing.log"
check lease-stack-log-needs-a-file "$([ "$status" = 2 ] && echo 0 || echo 1)" "it exited $status: $out"
run dev-airlock-lease.test.sh --stack-log "$work/no-banner.log"
check lease-refuses-a-stack-that-is-not-up "$([ "$status" = 1 ] && [[ $out == *"holds no 'dev-airlock: ready.' banner"* ]] && [ "$(stacks)" = 0 ] && echo 0 || echo 1)" \
  "it exited $status with $(stacks) stacks started: $out"

exit "$failed"
