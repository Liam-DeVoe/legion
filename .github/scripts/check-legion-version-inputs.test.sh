#!/usr/bin/env bash
# Tests for `check-legion-version-inputs.sh`: release.yaml's LEGION_GO_INPUTS block is what
# `go list -deps ./cmd/legion` names beyond $LEGION_VERSION_ARGS's paths, the version step passes
# it, and release-envoy-listener.yaml's $LEGION_ENVOY_VERSION_ARGS covers what go names for the
# archives that workflow builds.
#
# Each case builds a miniature repository under a temporary directory - a go.work over a daemon
# module whose cmd/legion imports packages of an envoy module, one of them on darwin alone, an
# envoy module with the three programs the listener release builds, the two workflows and the
# two target lists - and symlinks the real script into it, so the script under test is the file
# CI runs. No module of a fixture requires anything outside it, so its go commands load every
# package with nothing downloaded.
#
# Run from anywhere: .github/scripts/check-legion-version-inputs.test.sh
# CI runs it in ci.yaml's test job, after its setup-go.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
work=$(mktemp -d)
trap 'proxy_recorders_stop; rm -rf "$work"' EXIT

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"
# shellcheck source=.github/scripts/go-offline-test-lib.sh
source "$script_dir/go-offline-test-lib.sh"

# The hand-written arguments and the version step every fixture starts from.
hand_args='    -- packages/daemon/ .github/go-release-targets.json'
handoff='.github/scripts/release-push.sh version legion-v $LEGION_VERSION_ARGS $LEGION_GO_INPUTS'

# step <kind> <name>: a step of release_version_inputs.py as YAML lines of a fixture job's steps:
# `version <job>:<step id>`, one of VERSION_STEPS with its env: and its run: as a `|` block, or
# `before <name>`, one of STEPS_BEFORE_VERSION_STEPS. Every fixture's jobs are built from these, so
# each starts pinned as the check requires and each case below changes one piece of it.
step() {
  PYTHONDONTWRITEBYTECODE=1 PYTHONPATH="$script_dir" python3 - "$1" "$2" <<'PY'
import json
import sys

import release_version_inputs as plan

kind, name = sys.argv[1], sys.argv[2]
if kind == "version":
    spec = next(s for s in plan.VERSION_STEPS if f"{s.job}:{s.step_id}" == name)
    fields = {"id": spec.step_id, "env": spec.env, "run": spec.run}
else:
    spec = next(s for s in plan.STEPS_BEFORE_VERSION_STEPS if s.name == name)
    fields = {"uses": spec.uses, "with": spec.with_, "env": spec.env, "run": spec.run}
lines = []
for key, value in fields.items():
    if value is None:
        continue
    if key == "run":
        lines += ["run: |", *(f"  {line}" for line in value.rstrip("\n").split("\n"))]
    elif isinstance(value, dict):
        lines += [f"{key}:", *(f"  {k}: {json.dumps(v)}" for k, v in value.items())]
    else:
        lines.append(f"{key}: {value}")
print("\n".join(f"      {'- ' if i == 0 else '  '}{line}" for i, line in enumerate(lines)))
PY
}
legion_step=$(step version legion_build:version)
envoy_build_step=$(step version release_build:version)
envoy_confirm_step=$(step version release_publish:confirm)
checkout_step=$(step before checkout)
checkout_sha_step=$(step before "checkout of the event's commit")
determine_step=$(step before "Determine latest")
login_step=$(step before "log in to GHCR")
advance_step=$(step before "Advance latest image")
login_uses=$(PYTHONDONTWRITEBYTECODE=1 PYTHONPATH="$script_dir" python3 -c 'import release_version_inputs as p; print(p.LOGIN)')
confirm_version='          VERSION: "${{ needs.release_build.outputs.version }}"'
# The listener's, as release-envoy-listener.yaml writes them: the envoy module but its excluded
# infra directory, and the workspace's module files.
envoy_args='    -- :(exclude)packages/envoy/infra packages/envoy/ .github/go-release-targets.json'
envoy_modules='    go.work go.work.sum packages/daemon/go.mod packages/daemon/go.sum'

# fixture <name>: a tree whose block is still empty, so `write` fills it. Echoes its root.
fixture() {
  local root="$work/$1"
  if [ -e "$root" ]; then
    echo "  FAIL: the fixture name '$1' is already taken by an earlier case" >&2
    return 1
  fi
  mkdir -p "$root/.github/scripts" "$root/.github/workflows" "$root/packages/daemon/cmd/legion" \
    "$root/packages/daemon/lib" "$root/packages/envoy/alpha" "$root/packages/envoy/beta" \
    "$root/packages/envoy/gamma" "$root/packages/envoy/internal/delta" "$root/packages/envoy/infra/x" \
    "$root/packages/envoy/cmd/listener" "$root/packages/envoy/cmd/agent-secrets" \
    "$root/packages/envoy/cmd/agent-secrets-helper"
  for linked in check-legion-version-inputs.sh go_offline.py release_version_inputs.py; do
    ln -s "$script_dir/$linked" "$root/.github/scripts/$linked"
  done
  printf 'go 1.22\n\nuse (\n\t./packages/daemon\n\t./packages/envoy\n)\n' > "$root/go.work"
  : > "$root/go.work.sum"
  printf 'module example.com/envoy\n\ngo 1.22\n' > "$root/packages/envoy/go.mod"
  : > "$root/packages/envoy/go.sum"
  printf 'package alpha\n\nimport _ "example.com/envoy/internal/delta"\n' > "$root/packages/envoy/alpha/alpha.go"
  printf 'package beta\n' > "$root/packages/envoy/beta/beta.go"
  printf 'package gamma\n' > "$root/packages/envoy/gamma/gamma.go"
  printf 'package delta\n' > "$root/packages/envoy/internal/delta/delta.go"
  printf 'package x\n' > "$root/packages/envoy/infra/x/x.go"
  printf 'package main\n\nimport _ "example.com/envoy/alpha"\n\nfunc main() {}\n' > "$root/packages/envoy/cmd/listener/main.go"
  printf 'package main\n\nfunc main() {}\n' > "$root/packages/envoy/cmd/agent-secrets/main.go"
  printf 'package main\n\nfunc main() {}\n' > "$root/packages/envoy/cmd/agent-secrets-helper/main.go"
  printf 'module example.com/daemon\n\ngo 1.22\n\nrequire example.com/envoy v0.0.0\n\nreplace example.com/envoy => ../envoy\n' \
    > "$root/packages/daemon/go.mod"
  : > "$root/packages/daemon/go.sum"
  printf 'package lib\n' > "$root/packages/daemon/lib/lib.go"
  printf 'package main\n\nimport _ "example.com/envoy/alpha"\n\nfunc main() {}\n' > "$root/packages/daemon/cmd/legion/main.go"
  printf '//go:build darwin\n\npackage main\n\nimport _ "example.com/envoy/beta"\n' > "$root/packages/daemon/cmd/legion/darwin.go"
  printf '{"legion": ["linux/amd64", "darwin/arm64"], "envoy": ["amd64"]}\n' > "$root/.github/go-release-targets.json"
  printf '{"agent-secrets-client": ["darwin/arm64"]}\n' > "$root/.github/agent-secrets-client-targets.json"
  cat > "$root/.github/workflows/release-envoy-listener.yaml" <<EOF
name: Release Envoy Listener
on:
  push:
    branches: [main]
env:
  LEGION_ENVOY_VERSION_ARGS: >-
    --patch-path .github/go-release-targets.json
$envoy_args
$envoy_modules
EOF
  cat >> "$root/.github/workflows/release-envoy-listener.yaml" <<YAML
jobs:
  release_build:
    runs-on: ubuntu-24.04
    steps:
$checkout_step
$envoy_build_step
  release_publish:
    runs-on: ubuntu-24.04
    steps:
$checkout_sha_step
$determine_step
$login_step
$advance_step
$envoy_confirm_step
YAML
  cat > "$root/.github/workflows/release.yaml" <<EOF
name: Release Packages
on:
  push:
    branches: [main]
env:
  LEGION_VERSION_ARGS: >-
    --patch-path .github/go-release-targets.json
$hand_args
  # BEGIN generated by \`.github/scripts/check-legion-version-inputs.sh write\`
  # END generated
jobs:
  legion_build:
    runs-on: ubuntu-24.04
    steps:
$checkout_step
$legion_step
EOF
  echo "$root"
}

# written <name>: a fixture with its block written; fails the run when `write` fails on it.
written() {
  local root
  root=$(fixture "$1")
  "$root/.github/scripts/check-legion-version-inputs.sh" write > /dev/null
  echo "$root"
}

run_check() {
  local root=$1
  shift
  set +e
  out=$("$root/.github/scripts/check-legion-version-inputs.sh" "$@" 2>&1)
  status=$?
  set -e
}

# replace <file> <old> <new>: one literal replacement, failing the case loudly if <old> is absent.
replace() {
  local file=$1 old=$2 new=$3 text
  text=$(<"$file")
  if [[ $text != *"$old"* ]]; then
    echo "  FAIL: $file has no '$old' to replace" >&2
    return 1
  fi
  printf '%s\n' "${text/"$old"/"$new"}" > "$file"
}

release() { cat "$1/.github/workflows/release.yaml"; }

echo "case: write fills the block, and the check passes it"
root=$(fixture green)
run_check "$root" write
check "write exits 0" "$(is "$status" 0)"
check "says what it wrote" "$(contains "$out" "wrote .github/workflows/release.yaml's LEGION_GO_INPUTS: 7 paths")"
text=$(release "$root")
for path in go.work go.work.sum packages/envoy/alpha/ packages/envoy/beta/ packages/envoy/go.mod \
  packages/envoy/go.sum packages/envoy/internal/delta/; do
  check "the block names $path" "$(contains "$text" "^    $path\$")"
done
check "the block leaves out the package nothing links" "$(is "$(contains "$text" 'packages/envoy/gamma/')" false)"
check "the block leaves out what packages/daemon/ covers" "$(is "$(contains "$text" '^    packages/daemon/')" false)"
check "the block is a folded value" "$(contains "$text" '^  LEGION_GO_INPUTS: >-$')"
run_check "$root"
check "check exits 0" "$(is "$status" 0)"
check "says what it compared" \
  "$(contains "$out" '3 package directories and 4 module files, over linux/amd64, darwin/arm64')"
check "says what the listener's paths cover" \
  "$(contains "$out" "release-envoy-listener.yaml's LEGION_ENVOY_VERSION_ARGS covers what go decides the archives from: 5 package directories and 6 module files, over 4 listings")"
run_check "$root" write
check "a second write exits 0" "$(is "$status" 0)"
check "and changes nothing" "$(contains "$out" 'already holds what go names: 7 paths')"

echo "case: a package taken out of the block"
root=$(written dropped)
replace "$root/.github/workflows/release.yaml" $'    packages/envoy/internal/delta/\n' ''
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the package and the fix" \
  "$(contains "$out" 'it lacks packages/envoy/internal/delta/, which go names; run `.github/scripts/check-legion-version-inputs.sh write`')"
check "points at the block" "$(contains "$out" '::error file=.github/workflows/release.yaml,line=9::')"

echo "case: a package no target links, added to the block"
root=$(written added)
replace "$root/.github/workflows/release.yaml" '    packages/envoy/go.mod' $'    packages/envoy/gamma/\n    packages/envoy/go.mod'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names it" "$(contains "$out" 'it names packages/envoy/gamma/, which go does not')"

echo "case: cmd/legion imports a package the block does not name"
root=$(written imported)
printf 'package main\n\nimport _ "example.com/envoy/gamma"\n' > "$root/packages/daemon/cmd/legion/gamma.go"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the package" "$(contains "$out" 'it lacks packages/envoy/gamma/, which go names')"
run_check "$root" write
check "write exits 0" "$(is "$status" 0)"
check "write adds it" "$(contains "$(release "$root")" '^    packages/envoy/gamma/$')"
run_check "$root"
check "the written block passes" "$(is "$status" 0)"

echo "case: a package one target alone links is named for that target"
root=$(written targets)
printf '{"legion": ["linux/amd64"], "envoy": ["amd64"]}\n' > "$root/.github/go-release-targets.json"
run_check "$root"
check "exits 1 when no target is darwin" "$(is "$status" 1)"
check "names the darwin-only package" "$(contains "$out" 'it names packages/envoy/beta/, which go does not')"

echo "case: a path of LEGION_VERSION_ARGS covers what the block would name"
root=$(written covered)
replace "$root/.github/workflows/release.yaml" "$hand_args" "$hand_args packages/envoy/"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names what the hand path covers" \
  "$(contains "$out" 'it names packages/envoy/alpha/, packages/envoy/beta/, packages/envoy/go.mod, packages/envoy/go.sum, packages/envoy/internal/delta/, which go does not')"
run_check "$root" write
check "write keeps the workspace files alone" "$(contains "$(release "$root")" $'^    go.work.sum\n  # END generated$')"
check "the written block passes" "$(run_check "$root"; is "$status" 0)"
replace "$root/.github/workflows/release.yaml" "$hand_args packages/envoy/" "$hand_args packages/envoy/ go.work go.work.sum"
run_check "$root" write
check "a block with nothing left is written empty" "$(contains "$(release "$root")" '^  LEGION_GO_INPUTS: ""$')"
run_check "$root"
check "and passes" "$(is "$status" 0)"

# The version step's run: must be release_version_inputs.py's text, character for character, and
# the check names the first line that differs. Most cases below change it so that bash would not
# hand release-push.sh the generated paths; the added line holds the strictness itself, since a
# line that is harmless today is refused all the same.
exact="legion_build's step \`version\`'s run: must be, character for character, the one .github/scripts/release_version_inputs.py names for it"
echo "case: the version step does not pass the block"
root=$(written handoff)
replace "$root/.github/workflows/release.yaml" ' $LEGION_GO_INPUTS >>' ' >>'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the step and the rule" "$(contains "$out" "$exact")"
check "and the line it runs instead" \
  "$(contains "$out" 'its line 2 is `.github/scripts/release-push.sh version legion-v $LEGION_VERSION_ARGS >> "$GITHUB_OUTPUT"` where')"

echo "case: the block's dollar escaped, so bash hands release-push.sh the variable's name"
root=$(written escaped)
replace "$root/.github/workflows/release.yaml" ' $LEGION_GO_INPUTS >>' ' \$LEGION_GO_INPUTS >>'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the escaped line" "$(contains "$out" 'its line 2 is `.github/scripts/release-push.sh version legion-v $LEGION_VERSION_ARGS \\$LEGION_GO_INPUTS')"

echo "case: a \${{ }} line GitHub renders into an assignment that empties the block"
root=$(written expression)
replace "$root/.github/workflows/release.yaml" "          $handoff" "          \${{ 'LEGION_GO_INPUTS=' }}"$'\n'"          $handoff"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the expression line" "$(contains "$out" "its line 2 is \`\${{ 'LEGION_GO_INPUTS=' }}\` where")"

echo "case: a line added after the command"
root=$(written added-line)
replace "$root/.github/workflows/release.yaml" "$handoff >> \"\$GITHUB_OUTPUT\"" "$handoff >> \"\$GITHUB_OUTPUT\""$'\n'"          echo done"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the added line" "$(contains "$out" 'its line 3 is `echo done` where that one.s is nothing')"

echo "case: the listener's release_build step changed"
root=$(written listener-step)
replace "$root/.github/workflows/release-envoy-listener.yaml" ' $LEGION_ENVOY_VERSION_ARGS >> ' ' "$LEGION_ENVOY_VERSION_ARGS" >> '
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names that step" "$(contains "$out" "release_build's step \`version\`'s run: must be, character for character")"

# The job is pinned up to and including the version step: each case below gives the command a
# value in place of the workflow's, or runs code the check does not read before it, one way each.
plan_file=.github/scripts/release_version_inputs.py
env_rule="legion_build's step \`version\`'s env: must be, key for key, the one $plan_file names for it"
before_rule="before legion_build's step \`version\`, is not one of $plan_file's STEPS_BEFORE_VERSION_STEPS"
echo "case: the version step's own env: sets the block's variable"
root=$(written stepenv)
replace "$root/.github/workflows/release.yaml" $'      - id: version\n' $'      - id: version\n        env:\n          LEGION_GO_INPUTS: ""\n'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the rule and the key" "$(contains "$out" "$env_rule (nothing), .*it sets LEGION_GO_INPUTS, which that one does not")"

echo "case: the job's env: sets the hand-written variable"
root=$(written jobenv)
replace "$root/.github/workflows/release.yaml" $'    runs-on: ubuntu-24.04\n' $'    runs-on: ubuntu-24.04\n    env:\n      LEGION_VERSION_ARGS: -- packages/daemon/\n'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the job's env:" \
  "$(contains "$out" "legion_build's env: is .*a job holding a checked version step sets no env: of its own")"

echo "case: the command left in a shell comment"
root=$(written comment)
replace "$root/.github/workflows/release.yaml" "          $handoff" "          # $handoff"$'\n'"          echo skipped"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the commented line" "$(contains "$out" "its line 2 is \`# $handoff")"

echo "case: the block's variable quoted, so its paths are one word"
root=$(written quoted)
replace "$root/.github/workflows/release.yaml" ' $LEGION_GO_INPUTS >>' ' "$LEGION_GO_INPUTS" >>'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "shows the quoted word" "$(contains "$out" 'legion-v $LEGION_VERSION_ARGS "$LEGION_GO_INPUTS" >>')"

echo "case: the run: empties the variable before the command"
root=$(written assigned)
replace "$root/.github/workflows/release.yaml" "          $handoff" "          LEGION_GO_INPUTS="$'\n'"          $handoff"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the assignment" "$(contains "$out" 'its line 2 is `LEGION_GO_INPUTS=` where')"

echo "case: an earlier step of the job writes \$GITHUB_ENV"
root=$(written githubenv)
replace "$root/.github/workflows/release.yaml" $'    steps:\n' $'    steps:\n      - run: echo "LEGION_GO_INPUTS=" >> "$GITHUB_ENV"\n'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the step and the rule" "$(contains "$out" "step 1 of legion_build, $before_rule")"
check "and says it is no listed command" \
  "$(contains "$out" 'its run: is no listed command (its line 1 is `echo "LEGION_GO_INPUTS=" >> "$GITHUB_ENV"`)')"

echo "case: the version step runs under another shell"
root=$(written shell)
replace "$root/.github/workflows/release.yaml" $'      - id: version\n' $'      - id: version\n        shell: python\n'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the shell" "$(contains "$out" "its own shell: is \`python\`, where legion_build's step \`version\` must run under \`bash\` itself")"

# A file of shell a shell sources before the run:, and a shell line other than `bash` itself, can
# each set the variables in place of the workflow's: version-startup.sh below is the file each case
# names, and it empties the block.
startup='LEGION_GO_INPUTS='
echo "case: the workflow's env: sets BASH_ENV"
root=$(written bashenv)
printf '%s\n' "$startup" > "$root/version-startup.sh"
replace "$root/.github/workflows/release.yaml" $'env:\n  LEGION_VERSION_ARGS' $'env:\n  BASH_ENV: ./version-startup.sh\n  LEGION_VERSION_ARGS'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the variable and the list" \
  "$(contains "$out" ".github/workflows/release.yaml's workflow env: sets BASH_ENV, which $plan_file's WORKFLOW_ENV_KEYS does not list for it")"

echo "case: the job's env: sets ENV"
root=$(written jobenv-env)
replace "$root/.github/workflows/release.yaml" $'    runs-on: ubuntu-24.04\n' $'    runs-on: ubuntu-24.04\n    env:\n      ENV: ./version-startup.sh\n'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the job's env:" "$(contains "$out" "legion_build's env: is \`{\"ENV\": \"./version-startup.sh\"}\`")"

echo "case: the listener's confirm step's own env: sets BASH_ENV"
root=$(written stepenv-bashenv)
replace "$root/.github/workflows/release-envoy-listener.yaml" "$confirm_version"$'\n' \
  "$confirm_version"$'\n          BASH_ENV: ./version-startup.sh\n'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the step and the key" \
  "$(contains "$out" "release_publish's step \`confirm\`'s env: must be, key for key, .*it sets BASH_ENV, which that one does not")"

echo "case: the job's defaults.run.shell is a bash that sources a file first"
root=$(written rcfile)
replace "$root/.github/workflows/release.yaml" $'    runs-on: ubuntu-24.04\n' \
  $'    runs-on: ubuntu-24.04\n    defaults:\n      run:\n        shell: bash --noprofile --rcfile ./version-startup.sh -i {0}\n'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the shell line" \
  "$(contains "$out" "its job's defaults.run shell: is \`bash --noprofile --rcfile ./version-startup.sh -i {0}\`")"

echo "case: the workflow's defaults.run.shell is sh"
root=$(written workflow-shell)
replace "$root/.github/workflows/release.yaml" $'jobs:\n' $'defaults:\n  run:\n    shell: sh -e {0}\njobs:\n'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names it" "$(contains "$out" "the workflow's defaults.run shell: is \`sh -e {0}\`")"

echo "case: a defaults.run.working-directory runs the relative release-push.sh from another tree"
root=$(written working-directory)
replace "$root/.github/workflows/release.yaml" $'    runs-on: ubuntu-24.04\n' \
  $'    runs-on: ubuntu-24.04\n    defaults:\n      run:\n        working-directory: elsewhere\n'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the defaults" "$(contains "$out" "legion_build's defaults: is \`{\"run\": {\"working-directory\": \"elsewhere\"}}\`")"
root=$(written step-working-directory)
replace "$root/.github/workflows/release.yaml" $'      - id: version\n' $'      - id: version\n        working-directory: elsewhere\n'
run_check "$root"
check "exits 1 on the step's own" "$(is "$status" 1)"
check "names the key" "$(contains "$out" "legion_build's step \`version\` sets working-directory: (\`elsewhere\`), which a checked version step does not")"

echo "case: an earlier step of the job writes \$GITHUB_PATH"
root=$(written githubpath)
replace "$root/.github/workflows/release.yaml" $'    steps:\n' $'    steps:\n      - run: echo "$PWD/fake-bin" >> "$GITHUB_PATH"\n'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the step and the rule" "$(contains "$out" "step 1 of legion_build, $before_rule")"

# An action is code the check does not read, and one can write $GITHUB_ENV or $GITHUB_PATH for
# every later step: before a version step only the module's steps run, each exactly as listed.
echo "case: an action the module does not list runs before the version step"
root=$(written unlisted-action)
replace "$root/.github/workflows/release.yaml" $'    steps:\n' $'    steps:\n      - uses: example/setup-environment@v1\n'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the action and the list" \
  "$(contains "$out" "step 1 of legion_build, $before_rule.*it runs the action \`example/setup-environment@v1\`, which no listed step runs")"

echo "case: a listed action at another pin runs before the listener's confirm step"
root=$(written other-pin)
replace "$root/.github/workflows/release-envoy-listener.yaml" "      - uses: $login_uses" "      - uses: docker/login-action@v3"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names it" \
  "$(contains "$out" "step 3 of release_publish, before release_publish's step \`confirm\`, .*it runs the action \`docker/login-action@v3\`")"

echo "case: the listed checkout with another ref, so the step reads another commit's release-push.sh"
root=$(written checkout-ref)
replace "$root/.github/workflows/release.yaml" $'          persist-credentials: false\n' \
  $'          persist-credentials: false\n          ref: refs/heads/elsewhere\n'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names its with:" "$(contains "$out" "step 1 of legion_build, $before_rule.*its with: is \`{\"fetch-depth\": 0, \"persist-credentials\": false, \"ref\": \"refs/heads/elsewhere\"}\`")"

echo "case: the version step's job runs in a container"
root=$(written container)
replace "$root/.github/workflows/release.yaml" $'    runs-on: ubuntu-24.04\n' $'    runs-on: ubuntu-24.04\n    container: debian:trixie-slim\n'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the job" "$(contains "$out" "legion_build runs in a container:, so legion_build's step \`version\` runs under that image's bash")"

echo "case: the version step's job calls a reusable workflow"
root=$(written reusable)
replace "$root/.github/workflows/release.yaml" $'    runs-on: ubuntu-24.04\n' $'    runs-on: ubuntu-24.04\n    uses: ./.github/workflows/other.yaml\n'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the workflow" "$(contains "$out" "legion_build calls the reusable workflow \`./.github/workflows/other.yaml\`")"

echo "case: the listener's confirm step sets its paths' variable"
root=$(written confirmenv)
replace "$root/.github/workflows/release-envoy-listener.yaml" "$confirm_version"$'\n' \
  "$confirm_version"$'\n          LEGION_ENVOY_VERSION_ARGS: -- packages/envoy/\n'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the step and the key" \
  "$(contains "$out" "release_publish's step \`confirm\`'s env: must be, key for key, .*it sets LEGION_ENVOY_VERSION_ARGS, which that one does not")"

# The round-3 review's three reproductions, each of which the check passed before the job was
# pinned while the unchanged command then reported skip=true, and the class each belongs to.
echo "case: an expression in place of an env: mapping, which can carry BASH_ENV"
root=$(written env-expression)
replace "$root/.github/workflows/release.yaml" $'      - id: version\n' $'      - id: version\n        env: ${{ fromJSON(needs.setup.outputs.env) }}\n'
run_check "$root"
check "exits 1 on the version step's" "$(is "$status" 1)"
check "names it" "$(contains "$out" "$env_rule (nothing), .*it is \`\${{ fromJSON(needs.setup.outputs.env) }}\`, an expression, not a literal mapping")"
root=$(written job-env-expression)
replace "$root/.github/workflows/release.yaml" $'    runs-on: ubuntu-24.04\n' $'    runs-on: ubuntu-24.04\n    env: ${{ fromJSON(needs.setup.outputs.env) }}\n'
run_check "$root"
check "exits 1 on the job's" "$(is "$status" 1)"
check "names it" "$(contains "$out" "legion_build's env: is \`\${{ fromJSON(needs.setup.outputs.env) }}\`")"
root=$(written earlier-env-expression)
replace "$root/.github/workflows/release.yaml" $'        with:\n' $'        env: ${{ fromJSON(needs.setup.outputs.env) }}\n        with:\n'
run_check "$root"
check "exits 1 on an earlier step's" "$(is "$status" 1)"
check "names it" "$(contains "$out" "step 1 of legion_build, $before_rule.*its env: is \`\${{ fromJSON(needs.setup.outputs.env) }}\` where \`checkout\`'s is nothing")"

echo "case: an earlier step writes \$GITHUB_PATH without spelling it, or through a helper"
root=$(written indirect-path)
replace "$root/.github/workflows/release.yaml" $'    steps:\n' \
  $'    steps:\n      - run: |\n          f=GITHUB_; f+=PATH\n          echo "$PWD/shim" >> "${!f}"\n'
run_check "$root"
check "exits 1 on the indirect spelling" "$(is "$status" 1)"
check "names it" "$(contains "$out" "step 1 of legion_build, $before_rule.*its run: is no listed command (its line 1 is \`f=GITHUB_; f+=PATH\`)")"
root=$(written helper-path)
replace "$root/.github/workflows/release.yaml" $'    steps:\n' $'    steps:\n      - name: Prepare\n        run: .github/scripts/prepare.sh\n'
run_check "$root"
check "exits 1 on a helper" "$(is "$status" 1)"
check "names the step by its name" "$(contains "$out" "step 1 of legion_build (\`Prepare\`), $before_rule")"

echo "case: a PATH override, which picks the bash release-push.sh's #!/usr/bin/env bash runs"
root=$(written path-override)
replace "$root/.github/workflows/release.yaml" $'      - id: version\n' $'      - id: version\n        env:\n          PATH: ./shim:/usr/bin:/bin\n'
run_check "$root"
check "exits 1 on the version step's" "$(is "$status" 1)"
check "names it" "$(contains "$out" "$env_rule (nothing), .*it sets PATH, which that one does not")"
root=$(written path-workflow)
replace "$root/.github/workflows/release.yaml" $'env:\n  LEGION_VERSION_ARGS' $'env:\n  PATH: ./shim:/usr/bin:/bin\n  LEGION_VERSION_ARGS'
run_check "$root"
check "exits 1 on the workflow's" "$(is "$status" 1)"
check "names it" "$(contains "$out" "workflow env: sets PATH, which $plan_file's WORKFLOW_ENV_KEYS does not list for it")"

# The same class beyond the review's three spellings: a variable git or gh reads, and the loader's.
echo "case: GH_REPO in the workflow's env:, and LD_PRELOAD in a listed step's env:"
root=$(written gh-repo)
replace "$root/.github/workflows/release-envoy-listener.yaml" $'env:\n  LEGION_ENVOY_VERSION_ARGS' $'env:\n  GH_REPO: elsewhere/fork\n  LEGION_ENVOY_VERSION_ARGS'
run_check "$root"
check "exits 1 on GH_REPO" "$(is "$status" 1)"
check "names it" "$(contains "$out" "release-envoy-listener.yaml's workflow env: sets GH_REPO, which")"
root=$(written ld-preload)
replace "$root/.github/workflows/release-envoy-listener.yaml" $'          OWN_DIGEST: "${{ needs.image_index.outputs.digest }}"\n' \
  $'          OWN_DIGEST: "${{ needs.image_index.outputs.digest }}"\n          LD_PRELOAD: ./hook.so\n'
run_check "$root"
check "exits 1 on LD_PRELOAD" "$(is "$status" 1)"
check "names the listed step it differs from" \
  "$(contains "$out" "step 2 of release_publish, before release_publish's step \`confirm\`, .*its env: is .*where \`Determine latest\`'s is")"

echo "case: an exclude among LEGION_VERSION_ARGS's paths drops a path go names"
root=$(written magic)
replace "$root/.github/workflows/release.yaml" "$hand_args" "$hand_args :(exclude)packages/envoy/alpha"
before=$(release "$root")
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the exclude and what it drops" \
  "$(contains "$out" 'LEGION_VERSION_ARGS'"'"'s `:(exclude)packages/envoy/alpha` drops packages/envoy/alpha/, which go names for ./cmd/legion for linux/amd64')"
run_check "$root" write
check "write refuses too" "$(is "$status" 1)"
check "and writes nothing" "$(is "$(release "$root")" "$before")"
root=$(written glob)
replace "$root/.github/workflows/release.yaml" "$hand_args" "$hand_args :(glob)packages/envoy/*"
run_check "$root"
check "exits 1 on other magic" "$(is "$status" 1)"
check "names it" "$(contains "$out" 'LEGION_VERSION_ARGS names `:(glob)packages/envoy/\*`, a pathspec with magic this check cannot read')"

echo "case: an exclude inside a package directory go names"
root=$(written inside)
replace "$root/.github/workflows/release.yaml" "$hand_args" "$hand_args :(exclude)packages/envoy/alpha/alpha.go"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the file and the package directory it is in" \
  "$(contains "$out" 'drops packages/envoy/alpha/alpha.go, inside packages/envoy/alpha/, a package directory go names for ./cmd/legion for linux/amd64')"
root=$(written inside-short)
replace "$root/.github/workflows/release-envoy-listener.yaml" "$envoy_args" "$envoy_args :!packages/envoy/internal/delta/delta.go"
run_check "$root"
check "exits 1 on the listener's short form too" "$(is "$status" 1)"
check "names it" \
  "$(contains "$out" 'LEGION_ENVOY_VERSION_ARGS'"'"'s `:!packages/envoy/internal/delta/delta.go` drops packages/envoy/internal/delta/delta.go, inside packages/envoy/internal/delta/')"

echo "case: an exclude with a wildcard"
root=$(written wildcard)
replace "$root/.github/workflows/release.yaml" "$hand_args" "$hand_args :(exclude)packages/envoy/*.go"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names it" \
  "$(contains "$out" 'LEGION_VERSION_ARGS names `:(exclude)packages/envoy/\*.go`, an exclude with a wildcard')"

# git reads each of these as an exclude of packages/envoy/alpha/alpha.go, a file of a package the
# binary links; a compare of paths as written does not. Each is refused, naming the canonical
# spelling.
echo "case: an exclude of a linked file spelled another way git reads as the same path"
i=0
for spelled in ':(exclude)./packages/envoy/alpha/alpha.go|:(exclude)' ':!./packages/envoy/alpha/alpha.go|:!' \
  ':!/packages/envoy/alpha/alpha.go|:!' ':(exclude)packages/envoy//alpha/alpha.go|:(exclude)' \
  ':(exclude)packages/envoy/beta/../alpha/alpha.go|:(exclude)'; do
  i=$((i + 1))
  word=${spelled%|*} form=${spelled#*|}
  root=$(written "spelled-$i")
  replace "$root/.github/workflows/release.yaml" "$hand_args" "$hand_args $word"
  run_check "$root"
  check "exits 1 on $word" "$(is "$status" 1)"
  check "names the canonical spelling of $word" \
    "$(contains "$out" "an exclude whose path is not canonical: .*write it \`$form""packages/envoy/alpha/alpha.go\`")"
done
root=$(written caret)
replace "$root/.github/workflows/release.yaml" "$hand_args" "$hand_args :^./packages/envoy/alpha/alpha.go"
run_check "$root"
check "exits 1 on :^" "$(is "$status" 1)"
check "names both canonical spellings" \
  "$(contains "$out" "git's \`:^\` spelling of an exclude, which this check reads in two spellings alone; write it \`:(exclude)packages/envoy/alpha/alpha.go\` or \`:!packages/envoy/alpha/alpha.go\`")"
root=$(written listener-spelled)
replace "$root/.github/workflows/release-envoy-listener.yaml" "$envoy_args" "$envoy_args :!packages/envoy//internal/delta/delta.go"
run_check "$root"
check "exits 1 on the listener's paths too" "$(is "$status" 1)"
check "names it" \
  "$(contains "$out" 'LEGION_ENVOY_VERSION_ARGS names `:!packages/envoy//internal/delta/delta.go`, an exclude whose path is not canonical')"

echo "case: a \${{ }} expression GitHub renders into an exclude"
root=$(written expression-paths)
replace "$root/.github/workflows/release.yaml" "$hand_args" "$hand_args \${{ ':(exclude)packages/envoy/alpha' }}"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names it" "$(contains "$out" 'LEGION_VERSION_ARGS holds a `${{ }}` expression, which GitHub renders into the paths')"

echo "case: no block, or two"
root=$(fixture unmarked)
replace "$root/.github/workflows/release.yaml" $'  # BEGIN generated by `.github/scripts/check-legion-version-inputs.sh write`\n' ''
run_check "$root" write
check "write exits 1 with none" "$(is "$status" 1)"
check "says how many" "$(contains "$out" 'it holds 0')"
root=$(fixture twice)
replace "$root/.github/workflows/release.yaml" $'  # END generated\n' \
  $'  # END generated\n  # BEGIN generated by `.github/scripts/check-legion-version-inputs.sh write`\n  # END generated\n'
run_check "$root"
check "exits 1 with two" "$(is "$status" 1)"
check "says how many" "$(contains "$out" 'it holds 2')"

# go list reads a file's package clause and imports alone, so it is an import it cannot read that
# leaves the set unknown: a clause that does not parse, or a package of the workspace's own
# modules that is not there, which go lists with no directory.
echo "case: a package of the workspace go list cannot load"
root=$(written broken)
printf 'package alpha\n\nimport (\n\t_ "example.com/envoy/internal/delta"\n' > "$root/packages/envoy/alpha/alpha.go"
before=$(release "$root")
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the package" "$(contains "$out" 'go list cannot load example.com/envoy/alpha for ./cmd/legion for linux/amd64')"
run_check "$root" write
check "write refuses too" "$(is "$status" 1)"
check "and writes nothing" "$(is "$(release "$root")" "$before")"
root=$(written missing)
printf 'package alpha\n\nimport _ "example.com/envoy/internal/gone"\n' > "$root/packages/envoy/alpha/alpha.go"
run_check "$root"
check "exits 1 on an import of the envoy module that is not there" "$(is "$status" 1)"
check "names that import" "$(contains "$out" 'go list cannot load example.com/envoy/internal/gone for ./cmd/legion for linux/amd64')"

echo "case: a listing that names nothing of cmd/legion"
root=$(written gone)
rm "$root"/packages/daemon/cmd/legion/*.go
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "says the set is unknown" "$(contains "$out" 'so what it links is unknown')"

echo "case: the block's header edited by hand"
root=$(written header)
replace "$root/.github/workflows/release.yaml" 'An edit by' 'A change by'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "says the text is not write's" "$(contains "$out" "its text is not the block write writes")"

echo "case: the block outside the workflow env:"
root=$(written moved)
block=$(sed -n '/^  # BEGIN generated/,/^  # END generated$/p' "$root/.github/workflows/release.yaml")
replace "$root/.github/workflows/release.yaml" $'\n'"$block" ''
replace "$root/.github/workflows/release.yaml" $'on:\n' $'on:\n'"$block"$'\n'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "says it is not env's" \
  "$(contains "$out" "is not the LEGION_GO_INPUTS of .github/workflows/release.yaml's workflow env:")"

echo "case: a mode the check does not take"
root=$(written modes)
run_check "$root" fix
check "exits 1 on an unknown word" "$(is "$status" 1)"
check "names the two modes" "$(contains "$out" 'this check takes one optional mode: `check` (the default)')"
run_check "$root" check write
check "exits 1 on two words" "$(is "$status" 1)"

echo "case: the listener's paths as they were, without the workspace's module files"
root=$(fixture workspace)
replace "$root/.github/workflows/release-envoy-listener.yaml" $'\n'"$envoy_modules" ''
run_check "$root" write
check "exits 1" "$(is "$status" 1)"
check "write still writes the legion block, which the listener's paths do not decide" \
  "$(contains "$out" "wrote .github/workflows/release.yaml's LEGION_GO_INPUTS: 7 paths")"
for path in go.work go.work.sum; do
  check "names $path" "$(contains "$out" "::error file=.github/workflows/release-envoy-listener.yaml::LEGION_ENVOY_VERSION_ARGS names no path that covers $path, which go names for go env GOWORK in packages/envoy")"
done
for path in packages/daemon/go.mod packages/daemon/go.sum; do
  check "names $path" "$(contains "$out" "LEGION_ENVOY_VERSION_ARGS names no path that covers $path, which go names for go list -m in packages/envoy")"
done
check "names no path the list covers" "$(is "$(contains "$out" 'covers packages/envoy/')" false)"

echo "case: the listener imports, under cgo alone, a package outside the list"
root=$(written cgo)
printf '//go:build cgo\n\npackage main\n\nimport _ "example.com/daemon/lib"\n' > "$root/packages/envoy/cmd/listener/cgo.go"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names it and the cgo listing" \
  "$(contains "$out" 'covers packages/daemon/lib/, which go names for ./cmd/listener for linux/amd64 with CGO_ENABLED=1')"

echo "case: agent-secrets for a client target alone imports a package outside the list"
root=$(written client)
printf '//go:build darwin\n\npackage main\n\nimport _ "example.com/daemon/lib"\n' > "$root/packages/envoy/cmd/agent-secrets/darwin.go"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names it and the client listing" \
  "$(contains "$out" 'covers packages/daemon/lib/, which go names for ./cmd/agent-secrets for darwin/arm64')"

echo "case: an archive links what the listener's exclude drops"
root=$(written excluded)
printf 'package main\n\nimport _ "example.com/envoy/infra/x"\n\nfunc main() {}\n' > "$root/packages/envoy/cmd/agent-secrets-helper/main.go"
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the exclude and what it drops" \
  "$(contains "$out" 'LEGION_ENVOY_VERSION_ARGS'"'"'s `:(exclude)packages/envoy/infra` drops packages/envoy/infra/x/, which go names for ./cmd/agent-secrets and ./cmd/agent-secrets-helper for linux/amd64')"

echo "case: a listener program gone, or the listener's paths"
root=$(written nolistener)
rm "$root/packages/envoy/cmd/listener/main.go"
run_check "$root"
check "exits 1 with the program gone" "$(is "$status" 1)"
check "says the set is unknown" "$(contains "$out" 'so what it links is unknown')"
root=$(written noargs)
replace "$root/.github/workflows/release-envoy-listener.yaml" 'LEGION_ENVOY_VERSION_ARGS:' 'OTHER_ARGS:'
run_check "$root"
check "exits 1 with the paths gone" "$(is "$status" 1)"
check "names the key" "$(contains "$out" "release-envoy-listener.yaml's workflow env: holds no LEGION_ENVOY_VERSION_ARGS string")"

# The environment go_offline.py runs every go command in, seen by a stand-in go
# (go-offline-test-lib.sh): what a cold runner and a warm machine agree on only while it holds.
echo "case: every go command of both lines runs offline over an empty module cache"
root=$(fixture offline)
go_spy "$work/spy-offline"
set +e
out=$(PATH="$work/spy-offline/shim:$PATH" "$root/.github/scripts/check-legion-version-inputs.sh" write 2>&1)
status=$?
set -e
check "exits 0" "$(is "$status" 0)"
check "every go command ran with GOPROXY=off, GONOPROXY=none and GOENV=off over an empty cache of its own" \
  "$(is "$(go_spy_verdict "$work/spy-offline")" offline)"
check "the stand-in saw both lines' listings" \
  "$(is "$(($(wc -l < "$work/spy-offline/log") >= 10))" 1)"

# GOPROXY=off is not offline while a GONOPROXY or GOPRIVATE the machine sets matches a module:
# go fetches it directly first. Here the daemon requires a third-party module, with its go.sum
# lines, that the empty cache lacks, and the machine's settings match it; a proxy recorder stands
# in for the network, and go-offline-test-lib.sh's network_control is its own control.
echo "case: an inherited GONOPROXY sends no go command of either line to the network"
root=$(written network)
printf 'module example.com/daemon\n\ngo 1.22\n\nrequire (\n\texample.com/envoy v0.0.0\n\t%s v1.0.0\n)\n\nreplace example.com/envoy => ../envoy\n' \
  "$GO_OFFLINE_VENDOR" > "$root/packages/daemon/go.mod"
go_offline_vendor_sums "$root/packages/daemon/go.sum"
printf 'package main\n\nimport _ "%s"\n' "$GO_OFFLINE_VENDOR" > "$root/packages/daemon/cmd/legion/vendor.go"
network_recorder "$work/network"
check "the recorder sees a go list under GOPROXY=off alone try example.org" \
  "$(network_control "$work/network" "$root/packages/daemon" ./cmd/legion)"
set +e
out=$("${GO_OFFLINE_INHERITED[@]}" "$root/.github/scripts/check-legion-version-inputs.sh" 2>&1)
status=$?
set -e
check "the check passes, the third-party module aside" "$(is "$status" 0)"
check "no go command of the check tried the network" "$(is "$(network_requests "$work/network")" 0)"

summary "check-legion-version-inputs.sh"
