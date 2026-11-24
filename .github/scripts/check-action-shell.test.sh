#!/usr/bin/env bash
# Tests for `check-action-shell.sh`: a composite action whose run: bodies shellcheck passes at
# warning severity passes, with an expression in a body read as a word; a body with SC2164 (a cd
# with no `|| exit`) fails, naming the action and the step; a body of a step whose shell is not
# bash or sh is not read; and a script of .github/scripts with a warning fails, naming the script.
#
# Each case builds a miniature repository under a temporary directory and symlinks the real
# script into it. SHELLCHECK names the shellcheck it runs, as it does for the script: the scripts
# job gives it the actionlint image's.
#
# Run from anywhere: .github/scripts/check-action-shell.test.sh
# CI runs it in ci.yaml's scripts-held job.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

# fixture <name> <run body>: a tree whose one action has a step running <run body> and a
# PowerShell step no shellcheck reads. Echoes its root.
fixture() {
  local root="$work/$1"
  mkdir -p "$root/.github/scripts" "$root/.github/actions/demo"
  ln -s "$script_dir/check-action-shell.sh" "$root/.github/scripts/check-action-shell.sh"
  {
    printf 'name: demo\ndescription: A demo action.\nruns:\n  using: composite\n  steps:\n'
    printf '    - name: Do the work\n      shell: bash\n      run: |\n'
    sed 's/^/        /' <<<"$2"
    printf '    - name: A PowerShell step\n      shell: pwsh\n      run: cd $HOME\n'
  } >"$root/.github/actions/demo/action.yml"
  echo "$root"
}

run_check() {
  set +e
  out=$("$1/.github/scripts/check-action-shell.sh" 2>&1)
  status=$?
  set -e
}

echo "case: a body shellcheck passes, an expression in it read as a word"
root=$(fixture clean $'cd "${{ inputs.dir }}" || exit 1\necho "done"')
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "says what it read" \
  "$(contains "$out" '^shellcheck finds nothing at warning severity in the 1 run: bodies of .github/actions or the 1 scripts of .github/scripts$')"

echo "case: a body with a cd that has no || exit"
root=$(fixture cd $'cd packages/envoy\necho "done"')
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the action, the step and the line" \
  "$(contains "$out" '^::error::.github/actions/demo/action.yml step "Do the work":2:1: warning: .*\[SC2164\]$')"
check "prints SC2164" "$(contains "$out" 'SC2164')"
check "reads no PowerShell step" "$(is "$(contains "$out" 'A PowerShell step')" false)"

echo "case: a script of .github/scripts with a warning"
root=$(fixture script 'echo "done"')
printf '#!/usr/bin/env bash\ncd somewhere\n' >"$root/.github/scripts/moved.sh"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the script and the line" \
  "$(contains "$out" '^::error file=.github/scripts/moved.sh::.github/scripts/moved.sh:2:1: warning: .*\[SC2164\]$')"

summary "check-action-shell.sh"
