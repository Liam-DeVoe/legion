#!/usr/bin/env bash
# Red test: the order search of the shell stacks (scripts/e2e/lib/sweep-order.ts) reads a start as
# clean where errexit ends it before its removal of dead stacks' Postgres, in two forms it does not
# read:
#   1. errexit turned on by the script's first line, `#!/bin/bash -e` or `#!/usr/bin/env -S bash -e`,
#      with no `set -e`, before a bare `test ! -e <lock>`;
#   2. an arithmetic command whose operand is a command substitution, `(( $(stat …) == 0 ))`, under
#      `set -euo pipefail`.
#
# R3: "The next start of a test stack removes each Postgres of a stack that it can show dead." Item 5
# of round 14: "Under set -e (and set -o errexit, and a trap … ERR that exits), every command whose
# failure is not handled ends the start, so count each such command before the removal as a
# refusal unless it is shown to read only the start's own inputs." Decision 14: the search "fails
# safe". The search's own rule names `[`, `[[`, `test` with no test of a file, and the like, as
# input-only; neither form here is one of them.
#
# The test plants one driver per form in a tree of its own, with the tree's real lib/leftovers.sh.
# It RUNS each planted driver, as an executable (so the first line's -e counts), first with a lock
# file there (the start must end before it prints "removal reached") and then without it (the
# start must reach the removal): so each form is a refusal at run time. The planted tree holds no
# postgres-mark.ts, so its removal runs no sweep. Then it runs the tree's search over each planted
# tree. Control: the same check written `[ ! -e "$LOCK" ] || exit 1` must be FOUND, else UNKNOWN.
#
#   usage: order-search-errexit-shebang-arith.test.sh TREE     (TREE: a checkout's root)
# Exit 0: each form is found. Exit 1: a form the run shows refusing reads as clean. Exit 2: UNKNOWN.
set -u
tree=${1:?usage: order-search-errexit-shebang-arith.test.sh TREE}
search=$tree/scripts/e2e/lib/sweep-order.ts
[ -r "$search" ] && [ -r "$tree/scripts/e2e/lib/leftovers.sh" ] || { echo "UNKNOWN: no search or no leftovers.sh in $tree"; exit 2; }
command -v bun >/dev/null || { echo "UNKNOWN: no bun on PATH"; exit 2; }
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
lock=$work/held.lock
declare -A shebang=(
  [control-explicit]='#!/usr/bin/env bash'
  [shebang-bash-e]='#!/bin/bash -e'
  [shebang-env-S-bash-e]='#!/usr/bin/env -S bash -e'
  [arith-command-substitution]='#!/usr/bin/env bash'
)
declare -A check=(
  [control-explicit]=$'set -euo pipefail\n[ ! -e "$LOCK" ] || exit 1'
  [shebang-bash-e]='test ! -e "$LOCK"'
  [shebang-env-S-bash-e]='test ! -e "$LOCK"'
  [arith-command-substitution]=$'set -euo pipefail\n(( $(stat -c %s "$LOCK" 2>/dev/null || echo 0) == 0 ))'
)
bad=0
for name in control-explicit shebang-bash-e shebang-env-S-bash-e arith-command-substitution; do
  t=$work/$name
  mkdir -p "$t/scripts/e2e/lib"
  cp "$tree/scripts/e2e/lib/leftovers.sh" "$t/scripts/e2e/lib/"
  printf '%s\n' "${shebang[$name]}" 'root=$(cd "$(dirname "$0")/../.." && pwd)' \
    '. "$root/scripts/e2e/lib/leftovers.sh"' "${check[$name]}" 'echo "removal reached"' \
    'remove_leftover_postgres' >"$t/scripts/e2e/driver.sh"
  chmod +x "$t/scripts/e2e/driver.sh"
  printf 'held\n' >"$lock"
  blocked=$(LOCK=$lock timeout 20 "$t/scripts/e2e/driver.sh" 2>&1 | grep -c 'removal reached')
  rm -f -- "$lock"
  free=$(LOCK=$lock timeout 20 "$t/scripts/e2e/driver.sh" 2>&1 | grep -c 'removal reached')
  if [ "$blocked" != 0 ] || [ "$free" != 1 ]; then
    echo "UNKNOWN: $name does not end before its removal with the lock and reach it without (reached: with $blocked, without $free)"; exit 2
  fi
  bun "$search" "$t" >"$t.out" 2>&1; src=$?
  if [ "$src" = 1 ] && grep -q '^FOUND' "$t.out"; then
    echo "found:  $name (the start ends before its removal with the lock there; the search names it)"
  elif [ "$name" = control-explicit ]; then
    echo "UNKNOWN: the control form is not found (search exit $src): $(head -c 300 "$t.out")"; exit 2
  else
    echo "MISSED: $name: with the lock there the start ends before its removal, and the search says $(grep '^COUNT' "$t.out") (exit $src)"
    bad=$((bad + 1))
  fi
done
if [ "$bad" -gt 0 ]; then echo "FAIL: $bad form(s) that errexit makes a refusal read as clean"; exit 1; fi
echo "PASS: every form is found"
