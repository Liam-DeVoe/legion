#!/usr/bin/env bash
# packages/envoy/scripts/forced-order.test.sh: the refusals forced-order.sh must make. Each case
# runs the script as a developer or a CI step does and checks its exit status and what it printed.
# Needs the fixture package internal/testnats/hooktest/skiponly/.
set -u
here="$(cd "$(dirname "$0")" && pwd)"
envoy="$(dirname "$here")"
out="$(mktemp -d)"
trap 'rm -rf -- "$out"' EXIT
failed=0

# expect <name> <want: refuse> <grep -E pattern that must not appear> -- <args...>
refused() {
  local name=$1 absent=$2
  shift 2
  local code=0
  (cd "$envoy" && scripts/forced-order.sh "$@") >"$out/$name" 2>&1 || code=$?
  if [ "$code" -eq 0 ]; then
    echo "FAIL $name: exit 0, want a refusal: forced-order.sh $*"
    failed=$((failed + 1))
  elif grep -qE "$absent" "$out/$name"; then
    echo "FAIL $name: exit $code, but the output holds /$absent/: forced-order.sh $*"
    grep -E "$absent" "$out/$name" | head -n 3 | sed 's/^/    /'
    failed=$((failed + 1))
  else
    echo "PASS $name: exit $code: $(tail -n 1 "$out/$name")"
  fi
}

# A package whose forced-order tests all skip runs no forced-order test.
refused skip-only '^ok[[:space:]]' ./internal/testnats/hooktest/skiponly/
# -drop names a hold point; a name that is none is refused before any line of nats-server is cut
# and before the build, so a control never "fails" on a compile error.
refused drop-not-a-point 'dropped [0-9]+ hook line|^# github.com/nats-io/nats-server' -drop stream ./internal/testnats/hooktest/
# -server with an empty value is no version, not the default.
refused server-empty '^=== RUN' -server '' ./internal/testnats/hooktest/

echo "forced-order.test.sh: $failed failed of 3"
[ "$failed" -eq 0 ]
