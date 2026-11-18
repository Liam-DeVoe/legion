#!/usr/bin/env bash
# Red test: the order search of the shell stacks (scripts/e2e/lib/sweep-order.ts) reads a start as
# clean where `set -e` ends it before its removal of dead stacks' Postgres, and a `set +e` that bash
# never applies to the start's own shell comes first.
#
# R3: "The next start of a test stack removes each Postgres of a stack that it can show dead." Bash
# keeps `set -e` per shell: a subshell, a command substitution, a function whose body is in
# parentheses, each command of a pipeline of more than one and a list in the background run in a
# shell of their own, and a `set +e` there leaves the start's shell as it was; a `set +e` in a
# branch, a list or a loop that does not run changes nothing. The search fails safe: a state that
# one path may leave on is on after the paths join.
#
# The test plants one driver per form in a tree of its own, with the tree's real lib/leftovers.sh:
# `set -Eeuo pipefail`, the form, then `test ! -e "$LOCK"`, then the removal. It RUNS each planted
# driver first with the lock file there (the start must end before it prints "removal reached") and
# then without it (the start must reach the removal), so each form is a refusal at run time. The
# planted tree holds no postgres-mark.ts, so its removal runs no sweep. Then it runs the tree's
# search over each planted tree, and requires that it names the check's own line. Controls: the
# check written `[ ! -e "$LOCK" ] || exit 1` must be found, and a `set +e` in both branches of an
# `if`, which bash always applies, must make the start reach its removal with the lock there and
# read as clean; else UNKNOWN.
#
#   usage: order-search-errexit-scope.test.sh TREE     (TREE: a checkout's root)
# Exit 0: each form is found. Exit 1: a form the run shows refusing reads as clean. Exit 2: UNKNOWN.
set -u
tree=${1:?usage: order-search-errexit-scope.test.sh TREE}
search=$tree/scripts/e2e/lib/sweep-order.ts
[ -r "$search" ] && [ -r "$tree/scripts/e2e/lib/leftovers.sh" ] || { echo "UNKNOWN: no search or no leftovers.sh in $tree"; exit 2; }
command -v bun >/dev/null || { echo "UNKNOWN: no bun on PATH"; exit 2; }
work=$(mktemp -d)
trap 'rm -rf -- "$work"' EXIT
lock=$work/held.lock
check='test ! -e "$LOCK"'
declare -A form=(
  [control-explicit]=''
  [control-off-in-both-branches]=$'if [ -n "${NEVER_SET:-}" ]; then set +e; else set +e; fi'
  [branch-that-does-not-run]='if false; then set +e; fi'
  [list-that-does-not-run]='true || set +e'
  [loop-that-does-not-run]='while false; do set +e; done'
  [case-arm-that-does-not-run]='case "${NEVER_SET:-}" in never) set +e ;; esac'
  [subshell]='( set +e )'
  [command-substitution]='saved=$(set +e)'
  [function-in-parentheses]=$'relax() ( set +e )\nrelax'
  [pipeline]='set +e | cat'
  [background]='set +e &'
)
names=(control-explicit control-off-in-both-branches branch-that-does-not-run list-that-does-not-run
  loop-that-does-not-run case-arm-that-does-not-run subshell command-substitution
  function-in-parentheses pipeline background)
bad=0
for name in "${names[@]}"; do
  t=$work/$name
  mkdir -p "$t/scripts/e2e/lib"
  cp "$tree/scripts/e2e/lib/leftovers.sh" "$t/scripts/e2e/lib/"
  own=$check
  [ "$name" = control-explicit ] && own='[ ! -e "$LOCK" ] || exit 1'
  lines=('#!/usr/bin/env bash' 'set -Eeuo pipefail' 'root=$(cd "$(dirname "$0")/../.." && pwd)'
    '. "$root/scripts/e2e/lib/leftovers.sh"')
  [ -n "${form[$name]}" ] && while IFS= read -r line; do lines+=("$line"); done <<<"${form[$name]}"
  lines+=("$own")
  at=${#lines[@]}
  lines+=('echo "removal reached"' 'remove_leftover_postgres')
  printf '%s\n' "${lines[@]}" >"$t/scripts/e2e/driver.sh"
  chmod +x "$t/scripts/e2e/driver.sh"
  # A planted run is bash, lib/leftovers.sh and four commands, with no sweep: well under a second
  # at a load of 500. 20 s is room for a box under far more load; a run without the lock that this
  # limit ends is UNKNOWN below.
  printf 'held\n' >"$lock"
  blocked=$(LOCK=$lock timeout 20 "$t/scripts/e2e/driver.sh" 2>&1 | grep -c 'removal reached')
  rm -f -- "$lock"
  free=$(LOCK=$lock timeout 20 "$t/scripts/e2e/driver.sh" 2>&1 | grep -c 'removal reached')
  bun "$search" "$t" >"$t.out" 2>&1; src=$?
  if [ "$name" = control-off-in-both-branches ]; then
    if [ "$blocked" != 1 ] || [ "$free" != 1 ] || [ "$src" != 0 ]; then
      echo "UNKNOWN: $name must reach its removal with the lock there and read as clean (reached: with $blocked, without $free; search exit $src: $(grep -m1 '^FOUND' "$t.out"))"; exit 2
    fi
    echo "clean:  $name (bash applies its set +e on every path; the search reads no refusal)"
    continue
  fi
  if [ "$blocked" != 0 ] || [ "$free" != 1 ]; then
    echo "UNKNOWN: $name does not end before its removal with the lock and reach it without (reached: with $blocked, without $free)"; exit 2
  fi
  if [ "$src" = 1 ] && grep -q "^FOUND .*scripts/e2e/driver.sh:$at\b" "$t.out"; then
    echo "found:  $name (the start ends before its removal with the lock there; the search names line $at)"
  elif [ "$name" = control-explicit ]; then
    echo "UNKNOWN: the control form is not found at line $at (search exit $src): $(head -c 300 "$t.out")"; exit 2
  else
    echo "MISSED: $name: with the lock there the start ends before its removal, and the search says $(grep '^COUNT' "$t.out") (exit $src), naming no refusal at line $at"
    bad=$((bad + 1))
  fi
done
if [ "$bad" -gt 0 ]; then echo "FAIL: $bad form(s) whose set +e bash never applies to the start read as clean"; exit 1; fi
echo "PASS: every form is found"
