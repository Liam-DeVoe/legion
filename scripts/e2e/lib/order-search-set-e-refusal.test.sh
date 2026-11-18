#!/usr/bin/env bash
# Red test: the order search of the shell stacks (scripts/e2e/lib/sweep-order.ts) reads a start as
# clean where `set -e` ends it before its removal of dead stacks' Postgres.
#
# R3: "The next start of a test stack removes each Postgres of a stack that it can show dead." The
# search says it "reads a refusal by what it does, not by its name". Under `set -e` (every driver of
# scripts/e2e runs `set -euo pipefail` or `set -Eeuo pipefail`), a command that fails ends the
# start, as `exit 1` does: `flock -n 9` on a lock a dead run's pane holds, `mkdir` of a fixed
# directory a dead run left, `[ ! -e <file a dead run left> ]`. The search counts `exit`, `return`
# and helpers that always exit, not such a command.
#
# The test plants one driver per form in a tree of its own: it sources the tree's real
# lib/leftovers.sh, runs one check of a lock file this test makes, and then removes dead stacks'
# Postgres. It first RUNS each driver with the lock file there, and requires that the start ends
# before the removal (it never prints "removal reached"), so the form is a refusal at run time. Then
# it runs the tree's search over each planted tree. Controls: the same check with an explicit
# `|| exit 1` must be FOUND (else UNKNOWN: the search or the plant is broken), and the driver must
# reach its removal when the lock file is gone.
#
#   usage: order-search-set-e-refusal.test.sh TREE     (TREE: a checkout's root)
# Exit 0: every form is found. Exit 1: a form the run shows refusing reads as clean. Exit 2: UNKNOWN.
set -u
tree=${1:?usage: order-search-set-e-refusal.test.sh TREE}
search=$tree/scripts/e2e/lib/sweep-order.ts
[ -r "$search" ] && [ -r "$tree/scripts/e2e/lib/leftovers.sh" ] || { echo "UNKNOWN: no search or no leftovers.sh in $tree"; exit 2; }
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
lock=$work/held.lock
declare -A forms=(
  [explicit-exit]='[ ! -e "$LOCK" ] || exit 1'
  [set-e-test]='test ! -e "$LOCK"'
  [set-e-flock]='exec 9>>"$LOCK"; flock -n 9'
  [set-e-mkdir]='mkdir "$LOCK.d"'
)
plant() { # NAME CHECK
  local t=$work/$1
  mkdir -p "$t/scripts/e2e/lib"
  cp "$tree/scripts/e2e/lib/leftovers.sh" "$t/scripts/e2e/lib/"
  printf '%s\n' '#!/usr/bin/env bash' 'set -euo pipefail' 'root=$(cd "$(dirname "$0")/../.." && pwd)' \
    '. "$root/scripts/e2e/lib/leftovers.sh"' "$2" 'echo "removal reached"' 'remove_leftover_postgres' >"$t/scripts/e2e/driver.sh"
}
bad=0
for name in explicit-exit set-e-test set-e-flock set-e-mkdir; do
  plant "$name" "${forms[$name]}"
  : >"$lock"; mkdir -p "$lock.d"
  # A process of this test holds the lock, as a dead run's pane would.
  (exec 9>>"$lock"; flock 9; sleep 30) & holder=$!
  sleep 0.5
  ran=$(LOCK=$lock bash "$work/$name/scripts/e2e/driver.sh" 2>&1); rc=$?
  kill "$holder" 2>/dev/null; wait "$holder" 2>/dev/null
  rm -rf -- "$lock" "$lock.d"
  free=$(LOCK=$lock PATH=/usr/bin:/bin bash "$work/$name/scripts/e2e/driver.sh" 2>&1 | grep -c 'removal reached')
  if [[ $ran == *"removal reached"* ]] || [ "$rc" = 0 ] || [ "$free" != 1 ]; then
    echo "UNKNOWN: $name does not refuse with the lock and reach the removal without it (exit $rc, reached free $free)"; exit 2
  fi
  bun "$search" "$work/$name" >"$work/$name.out" 2>&1; src=$?
  if [ "$src" = 1 ] && grep -q '^FOUND' "$work/$name.out"; then
    echo "found:  $name (the start ends before its removal with the lock held; the search names it)"
  elif [ "$name" = explicit-exit ]; then
    echo "UNKNOWN: the control form is not found (search exit $src): $(head -c 300 "$work/$name.out")"; exit 2
  else
    echo "MISSED: $name: with the lock held the start ends (exit $rc) before its removal, and the search says $(grep '^COUNT' "$work/$name.out") (exit $src)"
    bad=$((bad + 1))
  fi
done
if [ "$bad" -gt 0 ]; then echo "FAIL: $bad form(s) that set -e makes a refusal read as clean"; exit 1; fi
echo "PASS: every form is found"
