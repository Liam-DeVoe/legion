#!/usr/bin/env bash
# codex-delivery-loops.sh <code commit> <main commit> [runs]
#
# Measures tests/codex-delivery.test.ts under load in a small, slow sandbox (a 2-CPU gVisor pod),
# where its tests used to fail: four loops side by side, each run its own `bun test` process, with
# one CPU burner beside them standing in for an agent's own work.
#
#   head      the file at <code commit>, which must be this checkout's code apart from this script
#   inject    the same, with turnEnded() held 40 ms before its own check and sweep, so the poll's
#             sweep settles a write while the caller still awaits turnEnded()
#   control-main   <main commit>'s test file on <main commit>'s code (the flake as it was)
#   control-gaps   <code commit>'s test file on <main commit>'s code (the tests that put a sweep,
#                  a stop() or a session end into a write's gap fail every run there)
#
# A run passes only when Bun reports every test of its file ran with 0 failures; a run that names
# no failed test and does not pass is unknown, never a pass. Exits 0 only when head and inject pass
# every run and both controls fail at least once, so a loop that cannot fail never reports a pass.
set -uo pipefail
code=$1
main=$2
runs=${3:-300}
here=$(cd "$(dirname "$0")/.." && pwd)
cd "$here"
self=packages/claude-envoy/scripts/codex-delivery-loops.sh
if ! git -C "$(git rev-parse --show-toplevel)" diff --quiet "$code" HEAD -- . ":(exclude)$self"; then
  echo "this checkout's code differs from $code beyond $self: refusing"
  exit 2
fi
echo "measuring $(git rev-parse "$code") against main $(git rev-parse "$main"), $runs runs per loop"
echo "cpus: $(nproc); kernel: $(dmesg 2>/dev/null | sed -n 1p)"

(cd ../.. && bun install --frozen-lockfile --ignore-scripts 2>&1 | tail -3)

# The control copies, beside the real files so their imports resolve; none is committed.
git show "$main:packages/claude-envoy/src/codex/codex-inbox.ts" > src/codex/codex-inbox.main.ts
git show "$main:packages/claude-envoy/src/codex/codex-delivery.ts" |
  sed 's#from "./codex-inbox"#from "./codex-inbox.main"#' > src/codex/codex-delivery.main.ts
git show "$main:packages/claude-envoy/tests/codex-delivery.test.ts" |
  sed -e 's#"../src/codex/codex-delivery"#"../src/codex/codex-delivery.main"#' \
    -e 's#"../src/codex/codex-inbox"#"../src/codex/codex-inbox.main"#' > tests/control-main.test.ts
sed -e 's#"../src/codex/codex-delivery"#"../src/codex/codex-delivery.main"#' \
  -e 's#"../src/codex/codex-inbox"#"../src/codex/codex-inbox.main"#' \
  tests/codex-delivery.test.ts > tests/control-gaps.test.ts
awk '{print} /async turnEnded\(\) \{/ {getline; print; print "      await Bun.sleep(40)"}' \
  src/codex/codex-delivery.ts > src/codex/codex-delivery.inject.ts
sed 's#"../src/codex/codex-delivery"#"../src/codex/codex-delivery.inject"#' \
  tests/codex-delivery.test.ts > tests/inject.test.ts
if cmp -s src/codex/codex-delivery.ts src/codex/codex-delivery.inject.ts; then
  echo "the delay was not injected: refusing"
  exit 2
fi

count_tests() { # the number of tests Bun reports for one run of <file>
  bun test --timeout=60000 "$1" 2>&1 | sed -n 's/^Ran \([0-9]*\) tests across 1 file.*/\1/p'
}

out=$(mktemp -d)
loop() { # <label> <file> <tests in the file>
  local label=$1 file=$2 tests=$3 pass=0 fail=0 unknown=0 i code log
  mkdir -p "$out/$label"
  for ((i = 1; i <= runs; i++)); do
    log="$out/$label/run-$i.log"
    timeout -k 10 120 bun test --timeout=60000 "$file" > "$log" 2>&1
    code=$?
    if [ "$code" -eq 0 ] && grep -q "^Ran $tests tests across 1 file" "$log" && grep -q '^ 0 fail$' "$log"; then
      pass=$((pass + 1))
      rm -f "$log"
    elif [ "$code" -ne 0 ] && grep -q '^(fail) ' "$log"; then
      fail=$((fail + 1))
    else
      unknown=$((unknown + 1))
    fi
  done
  echo "$label runs=$runs pass=$pass fail=$fail unknown=$unknown" > "$out/$label/summary"
}

declare -A tests
for f in head:tests/codex-delivery.test.ts inject:tests/inject.test.ts \
  control-main:tests/control-main.test.ts control-gaps:tests/control-gaps.test.ts; do
  tests[${f%%:*}]=$(count_tests "${f#*:}")
  [ -n "${tests[${f%%:*}]}" ] || { echo "no test count for ${f#*:}: refusing"; exit 2; }
done
echo "tests per file: head=${tests[head]} inject=${tests[inject]} control-main=${tests[control-main]} control-gaps=${tests[control-gaps]}"

sh -c 'while :; do :; done' &
burner=$!
started=$(date -u +%FT%TZ)
loop head tests/codex-delivery.test.ts "${tests[head]}" &
a=$!
loop inject tests/inject.test.ts "${tests[inject]}" &
b=$!
loop control-main tests/control-main.test.ts "${tests[control-main]}" &
c=$!
loop control-gaps tests/control-gaps.test.ts "${tests[control-gaps]}" &
d=$!
wait "$a" "$b" "$c" "$d"
kill "$burner"
echo "loops from $started to $(date -u +%FT%TZ)"

status=0
for label in head inject control-main control-gaps; do
  cat "$out/$label/summary"
  grep -h '^(fail) ' "$out/$label"/run-*.log 2>/dev/null | sed 's/ \[[0-9.]*m\?s\]$//' | sort | uniq -c
  read -r pass fail unknown < <(sed 's/.*pass=\([0-9]*\) fail=\([0-9]*\) unknown=\([0-9]*\)/\1 \2 \3/' "$out/$label/summary")
  case $label in
    head | inject)
      if [ "$pass" != "$runs" ]; then
        status=1
        for log in "$out/$label"/run-*.log; do
          [ -e "$log" ] || continue
          echo "--- $log"
          grep -B30 '^(fail) ' "$log" | sed -n '1,60p'
        done
      fi
      ;;
    *) [ "$fail" -gt 0 ] || { echo "$label never failed: the control did not show the flake"; status=1; } ;;
  esac
done
rm -f src/codex/codex-inbox.main.ts src/codex/codex-delivery.main.ts src/codex/codex-delivery.inject.ts \
  tests/control-main.test.ts tests/control-gaps.test.ts tests/inject.test.ts
echo "LOOPS-DONE status=$status"
exit "$status"
