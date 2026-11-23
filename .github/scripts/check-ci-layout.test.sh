#!/usr/bin/env bash
# Tests for `check-ci-layout.sh`: a copy of this repository's own layout passes, and each copy that
# breaks one rule the way the Verification document's c-ci-shape check names is refused, naming
# the rule's subject: a step that tests an output of changes (main's step gate on the two-build
# Go step), a -skip list that loses one test a -run list of another job names, kind kept to pull
# requests in CI_OK_PULL_REQUEST_ONLY or by its own condition or with merge_group left out of it,
# a go test outside go-test.sh, a go-test.sh call with no --jsonfile, a retry turned off, the
# mutants script taking the retry, the search job tested on an output, a step inside an action
# gated on merge_group, a job without the run_attempt term, a liveGroups entry no kind leg runs,
# a job gated on an output changes does not declare, a carrier with a fourth step, a carrier for a
# job that is not split, a carrier with no report step, a split job's legs that leave an index out
# and that name one twice, pr-title needing changes, a literal name
# on a job a run can skip (worker-image, an agent-harness part, dispatch-browser.yaml's visual),
# a changes output reading another step, the airlock job on x64, the race step dropping a package
# the fill names, a push-only if: in an action only ci.yaml runs, a cache key drifting inside an
# action and a cache path drifting inside one ci-caches.yaml runs too, an install that loses its
# 11-minute bound and two timed installs folded into one, a matrix without fail-fast: false, a
# name of main's ci.yaml the layout no longer carries, a second merge_group workflow, a notes
# section no link names, a link to no section, a `## <name>:` heading naming no output or job, and
# a darwin vet that stops reading go_build_tags.py or whose tag sets miss a file.
#
# Each case copies .github/ of this checkout and links every other top-level entry of it, so the
# scripts under test, the workflows and the actions are this checkout's own and the Go sources the
# step reader reads are its tree; the case edits only files it copied. Rule 20's go list runs over
# a module of the case's own, and every other case pins no darwin vet, so no case lists this
# checkout's modules. It needs python3 with PyYAML, jq and go.
#
# Run from anywhere: .github/scripts/check-ci-layout.test.sh
# CI runs it in ci.yaml's scripts job (its test leg).
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
repo=$(cd "$script_dir/../.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

# replace <file> <old> <new>: one literal replacement, failing the case loudly if <old> is absent.
replace() {
  local file=$1 old=$2 new=$3 text
  text=$(<"$file")
  if [[ $text != *"$old"* ]]; then
    echo "  FAIL: $file has no '$old' to replace" >&2
    return 1
  fi
  printf '%s\n' "${text/"$old"/"$new"}" >"$file"
}

base=$work/base
mkdir -p "$base"
cp -a "$repo/.github" "$base/.github"
rm -rf "$base/.github/scripts/__pycache__"
for entry in "$repo"/* "$repo"/.[!.]*; do
  name=$(basename "$entry")
  case $name in .github | .git | .jj) continue ;; esac
  ln -s "$entry" "$base/$name"
done
printf '\n# The cases but rule 20s own vet no module of this checkout.\nDARWIN_VETS = []\n' \
  >>"$base/.github/scripts/ci_pins.py"

# fresh <name>: a copy of the base for one case. Echoes its root.
fresh() {
  cp -a "$base" "$work/$1"
  echo "$work/$1"
}

run_check() {
  set +e
  out=$("$1/.github/scripts/check-ci-layout.sh" 2>&1)
  status=$?
  set -e
}

has() { grep -Fq -- "$2" <<<"$1" && echo true || echo false; }

pins() { printf '%s\n' "$2" >>"$1/.github/scripts/ci_pins.py"; }

echo "case: this checkout's own layout passes"
root=$(fresh head)
run_check "$root"
check "exits 0" "$(is "$status" 0)"
check "says every rule holds" "$(has "$out" 'the CI layout holds every rule of check-ci-layout.sh')"
check "prints no error" "$(is "$(grep -c '^::error' <<<"$out" || true)" 0)"

echo "case: the gates of ci.yaml's jobs and steps"
root=$(fresh gates)
ci=$root/.github/workflows/ci.yaml
# main's step gate on the two-build Go step, here on the two-build leg's step
replace "$ci" "if: matrix.leg == 'dispatch-two-builds'" \
  "if: matrix.leg == 'dispatch-two-builds' && needs.changes.outputs.go-envoy-held == 'true'"
# kind kept to pull requests in ci-ok's list
replace "$ci" "CI_OK_PULL_REQUEST_ONLY: '[\"worker-image\"]'" "CI_OK_PULL_REQUEST_ONLY: '[\"worker-image\", \"kind\"]'"
# the search job's condition tests an output
replace "$ci" $'github.run_attempt == \'1\') }}\n    name: ${{ matrix.name }}\n    runs-on: ${{ matrix.runner }}' \
  $'github.run_attempt == \'1\') && needs.changes.outputs.scripts == \'true\' }}\n    name: ${{ matrix.name }}\n    runs-on: ${{ matrix.runner }}'
# a job gated on an output changes does not declare
replace "$ci" "needs.changes.outputs.plugin-pack == 'true'" "needs.changes.outputs.plugin-pack-x == 'true'"
# a job without the run_attempt term
replace "$ci" "if: needs.changes.outputs.daemon-live == 'true' && (github.event_name != 'merge_group' || github.run_attempt == '1')" \
  "if: needs.changes.outputs.daemon-live == 'true'"
# pr-title needs changes
replace "$ci" $'  pr-title:\n' $'  pr-title:\n    needs: changes\n'
# a changes output reading a step that is none of the three
replace "$ci" "      unnamed_files: \${{ steps.unnamed.outputs.found }}" \
  $'      unnamed_files: ${{ steps.unnamed.outputs.found }}\n      extra: ${{ steps.ref.outputs.other }}'
# the airlock job on x64
replace "$ci" $'    runs-on: ubuntu-24.04-arm\n    timeout-minutes: 45' $'    runs-on: ubuntu-24.04\n    timeout-minutes: 45'
run_check "$root"
check "fails" "$(is "$status" 1)"
check "refuses a step that tests an output of changes" \
  "$(has "$out" "job go-envoy-held step \"dispatch-two-builds\" tests \`needs.changes.outputs.go-envoy-held\`")"
check "refuses kind in CI_OK_PULL_REQUEST_ONLY (rule 7)" \
  "$(has "$out" "CI_OK_PULL_REQUEST_ONLY lists kind, whose if: has no github.event_name == 'pull_request' term")"
check "refuses the search job gated on an output (rule 7)" \
  "$(has "$out" "CI_OK_ALWAYS_RUN names lint, whose if: tests needs.changes.outputs.scripts")"
check "refuses a gate on an undeclared output (rule 2)" \
  "$(has "$out" "job plugin-pack's if: tests needs.changes.outputs.plugin-pack-x, which the changes job does not declare")"
check "refuses a job without the run_attempt term (rule 12)" \
  "$(has "$out" "job daemon-live's if: has no \`(github.event_name != 'merge_group' || github.run_attempt == '1')\`")"
check "refuses pr-title needing a job" "$(has "$out" "job pr-title needs a job")"
check "refuses an output reading another step (rule 19)" \
  "$(has "$out" "the changes job's output extra reads steps.ref.outputs.other, and step ref is neither")"
check "refuses the airlock job off arm (rule 26)" "$(has "$out" "job airlock-arm runs on \`ubuntu-24.04\`")"

echo "case: kind kept to pull requests by its own condition"
root=$(fresh kind-pr)
replace "$root/.github/workflows/ci.yaml" "if: \${{ !cancelled() && needs.kind-image.result == 'success'" \
  "if: \${{ !cancelled() && github.event_name == 'pull_request' && needs.kind-image.result == 'success'"
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names kind's gate on the event (rule 28)" "$(has "$out" "job kind has \`\${{ !cancelled() && github.event_name == 'pull_request'")"
check "names the merge group it skips" "$(has "$out" "which skips it on merge_group for the event or a label")"

echo "case: kind's condition leaves merge_group out"
root=$(fresh kind-no-merge-group)
replace "$root/.github/workflows/ci.yaml" "if: \${{ !cancelled() && needs.kind-image.result == 'success'" \
  "if: \${{ !cancelled() && (github.event_name == 'pull_request' || github.event_name == 'workflow_dispatch') && needs.kind-image.result == 'success'"
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the merge group kind's condition skips (rule 28)" \
  "$(has "$out" "github.event_name == 'workflow_dispatch') && needs.kind-image.result == 'success'")"

echo "case: the Go steps' retry, jsonfile and split lists"
root=$(fresh go-steps)
# a -skip list that keeps plugin-pack's tests out of go-daemon loses one of them
replace "$root/.github/actions/go-daemon/action.yml" '|TestTheRealPluginsOnTheRealOhMyPi)' ')'
# a go-test.sh call with no --jsonfile
replace "$root/.github/actions/plugin-pack/action.yml" '--jsonfile plugin-pack-plugin-pack-pack.json ' ''
# a retry turned off
replace "$root/.github/actions/daemon-live/action.yml" 'run: ../../.github/scripts/go-test.sh' \
  'run: GO_TEST_RETRY=off ../../.github/scripts/go-test.sh'
# a step inside broker-multiprocess gated on merge_group
replace "$root/.github/actions/go-envoy/broker-multiprocess/action.yml" \
  $'    - name: Two-process tests of the secrets broker\n' \
  $'    - name: Two-process tests of the secrets broker\n      if: github.event_name == \'merge_group\'\n'
# the race step drops a package the fill still names
replace "$root/.github/actions/go-daemon/action.yml" './internal/intake/ ./internal/shimwire/"' './internal/intake/"'
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the test the -skip list lost (rule 22)" \
  "$(has "$out" "runs TestTheRealPluginsOnTheRealOhMyPi of")"
check "names the step with no --jsonfile (rule 24)" "$(has "$out" "plugin-pack runs go-test.sh with no --jsonfile")"
check "names the retry turned off (rule 24)" "$(has "$out" "daemon-live sets GO_TEST_RETRY=off")"
check "names the step gated on merge_group (rule 28)" \
  "$(has "$out" "step \"Two-process tests of the secrets broker\" has \`github.event_name == 'merge_group'\`, which skips it on pull_request")"
check "names the package the fill compiles and the race step drops (rule 16)" \
  "$(has "$out" "job daemon's race build names ./internal/shimwire/, which the race step")"

echo "case: a go test outside go-test.sh, and the mutants script taking the retry"
root=$(fresh no-retry)
replace "$root/.github/actions/plugin-pack/action.yml" \
  '../../.github/scripts/go-test.sh --jsonfile plugin-pack-plugin-pack-pack.json --packages ./internal/daemon/ --' \
  'go test ./internal/daemon/'
grep -v '^export GO_TEST_RETRY=off' "$repo/packages/envoy/scripts/airlock-mutants.sh" >"$root/mutants.sh"
pins "$root" 'NO_RETRY_SCRIPT = "mutants.sh"'
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the go test outside go-test.sh (rule 24)" "$(has "$out" "plugin-pack runs \`go test")"
check "names the mutants script taking the retry (rule 24)" \
  "$(has "$out" "mutants.sh does not \`export GO_TEST_RETRY=off\`")"

echo "case: a liveGroups entry no kind leg runs"
root=$(fresh live-groups)
cp "$repo/packages/daemon/internal/runtime/sandbox/live_test.go" "$root/live_test.go"
replace "$root/live_test.go" $'var liveGroups = []liveGroup{\n' $'var liveGroups = []liveGroup{\n\t{name: "unrun", checks: nil},\n'
pins "$root" 'LIVE_GROUPS = {**LIVE_GROUPS, "file": "live_test.go"}'
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the group in no leg (rule 27)" \
  "$(has "$out" "liveGroups entry unrun of live_test.go is in 0 kind legs' -run (none), and must be in exactly one")"

echo "case: the carriers, the names and the matrices"
root=$(fresh carriers)
ci=$root/.github/workflows/ci.yaml
replace "$ci" $'leg-verdict.sh \'envoy-go (1/3)\' \'envoy-go (2/3)\' \'envoy-go (3/3)\'\n' \
  $'leg-verdict.sh \'envoy-go (1/3)\' \'envoy-go (2/3)\' \'envoy-go (3/3)\'\n      - run: echo a fourth step\n'
replace "$ci" $'leg-verdict.sh \'envoy-go-race-api (1/2)\' \'envoy-go-race-api (2/2)\'\n      - {if: always(), uses: ./.github/actions/ci-ok-report, with: {changes: \'${{ toJSON(needs.changes) }}\', job-status: \'${{ job.status }}\'}}\n' \
  $'leg-verdict.sh \'envoy-go-race-api (1/2)\' \'envoy-go-race-api (2/2)\'\n'
replace "$ci" '  # why: .github/ci-notes/ci.md#the-airlock-arm-job' \
  "  plugin-pack-carrier:
    name: \${{ 'plugin-pack / plugin-pack' }}
    needs: [changes, plugin-pack]
    if: \${{ !cancelled() && needs.changes.outputs.plugin-pack == 'true' && (github.event_name != 'merge_group' || github.run_attempt == '1') }}
    runs-on: ubuntu-24.04-arm
    timeout-minutes: 10
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with: {sparse-checkout: .github, persist-credentials: false}
      - env: {GH_TOKEN: '\${{ github.token }}'}
        run: .github/scripts/leg-verdict.sh 'plugin-pack / plugin-pack'
      - {if: always(), uses: ./.github/actions/ci-ok-report, with: {changes: '\${{ toJSON(needs.changes) }}', job-status: '\${{ job.status }}'}}

  # why: .github/ci-notes/ci.md#the-airlock-arm-job"
replace "$ci" "name: \${{ 'worker-image' }}" 'name: worker-image'
replace "$ci" "name: \${{ format('agent-harness / {0}', matrix.leg) }}" 'name: agent-harness'
replace "$root/.github/workflows/dispatch-browser.yaml" "    name: \${{ 'visual' }}"$'\n' ''
replace "$ci" $'    strategy:\n      fail-fast: false\n      matrix:\n        include:\n          - {leg: lint,' \
  $'    strategy:\n      matrix:\n        include:\n          - {leg: lint,'
replace "$ci" '[text-checks / text-checks]' '[text-check / text-check]'
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the carrier with a fourth step (rule 32)" "$(has "$out" "carrier envoy-go holds 4 step(s)")"
check "names the carrier with no report step (rule 7)" "$(has "$out" "job envoy-go-race-api's last step is not")"
check "names the carrier of a job that is not split (rule 32)" \
  "$(has "$out" "job plugin-pack-carrier runs .github/scripts/leg-verdict.sh, a carrier, and carries \`plugin-pack / plugin-pack\`")"
check "names worker-image's literal name (rule 25)" "$(has "$out" "job worker-image of .github/workflows/ci.yaml has an if: that can skip it and the literal name")"
check "names agent-harness's literal name (rule 25)" "$(has "$out" "job agent-harness of .github/workflows/ci.yaml has an if: that can skip it and the literal name")"
check "names the visual job with no name (rule 25)" "$(has "$out" "job visual of .github/workflows/dispatch-browser.yaml has an if: that can skip it and no name:")"
check "names the matrix without fail-fast: false (rule 29)" "$(has "$out" "job lint's matrix does not set fail-fast: false")"
check "names main's check-run name the layout lost (rule 30)" "$(has "$out" "carries the check-run name \`text-checks / text-checks\`")"

echo "case: the indexes the legs of a split job name"
root=$(fresh shards)
ci=$root/.github/workflows/ci.yaml
# envoy-go-race-api's two legs named as two of three: index 3 names no leg
replace "$ci" 'name: envoy-go-race-api (1/2)' 'name: envoy-go-race-api (1/3)'
replace "$ci" 'name: envoy-go-race-api (2/2)' 'name: envoy-go-race-api (2/3)'
# envoy-go-docs-finalizers' third leg named as its second: index 2 names two legs
replace "$ci" 'name: envoy-go-docs-finalizers (3/3)' 'name: envoy-go-docs-finalizers (2/3)'
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the index no leg names (rule 34)" \
  "$(has "$out" "job go-envoy-held splits envoy-go-race-api into 3 legs, and no leg is \`envoy-go-race-api (3/3)\`")"
check "names the index two legs name (rule 34)" \
  "$(has "$out" "job go-envoy-held splits envoy-go-docs-finalizers into 3 legs, and 2 legs are \`envoy-go-docs-finalizers (2/3)\`")"
check "names the leg whose part is not its index (rule 34)" \
  "$(has "$out" "leg \`envoy-go-docs-finalizers (2/3)\` of job go-envoy-held hands its steps part 3, not its own index 2")"

echo "case: the actions' conditions, caches and timed steps, and the merge-group workflows"
root=$(fresh actions)
ci=$root/.github/workflows/ci.yaml
replace "$root/.github/actions/plugin-pack/action.yml" '      run: ../../.github/scripts/go-test.sh' \
  $'      if: github.event_name == \'push\'\n      run: ../../.github/scripts/go-test.sh'
replace "$root/.github/actions/go-daemon/action.yml" \
  'key: go-daemon-${{ runner.os }}-${{ runner.arch }}-go${{ steps.setup-go.outputs.go-version }}' \
  'key: go-daemon-${{ runner.os }}-${{ runner.arch }}-go${{ steps.setup-go.outputs.go-version }}-drift'
replace "$root/.github/actions/architecture-check/action.yml" '${{ runner.temp }}/architecture-check/mod' \
  '${{ runner.temp }}/architecture-check/modules'
replace "$ci" '{if: matrix.arm, timeout-minutes: 11, uses: ./.github/actions/install-crane}' \
  '{if: matrix.arm, uses: ./.github/actions/install-crane}'
replace "$ci" $'      - {if: matrix.x64, timeout-minutes: 11, uses: ./.github/actions/install-elan}\n' ''
printf 'name: Extra\non:\n  merge_group:\njobs:\n  extra:\n    runs-on: ubuntu-24.04\n    steps:\n      - run: "true"\n' \
  >"$root/.github/workflows/extra.yaml"
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the push-only if: in an action only ci.yaml runs (rule 10)" \
  "$(has "$out" "of .github/actions/plugin-pack/action.yml has \`if: github.event_name == 'push'\`, which tests")"
check "names the cache key that drifted inside an action (rule 11)" \
  "$(has "$out" "but job go-daemon of .github/workflows/ci.yaml (in .github/actions/go-daemon/action.yml) restores it under")"
check "names the cache path that drifted inside an action ci-caches.yaml runs (rule 11)" \
  "$(has "$out" "the save of .github/actions/architecture-check/action.yml, which job architecture-check runs")"
check "names the install that lost its bound (rule 31)" \
  "$(has "$out" "holds 1 step(s) with if matrix.arm, uses ./.github/actions/install-crane bound to 11 minutes, where 2 kept")"
check "names the timed installs folded into one (rule 31)" \
  "$(has "$out" "holds 2 step(s) with if matrix.x64, uses ./.github/actions/install-elan bound to 11 minutes, where 3 kept")"
check "names the second merge-group workflow (rule 14)" "$(has "$out" ".github/workflows/extra.yaml runs on merge_group")"

echo "case: the notes"
root=$(fresh notes)
printf '\n## daemon_go_tests: The daemon-go job: what its two Go steps read together\n\nText.\n\n## Old filters\n\nText.\n' \
  >>"$root/.github/ci-notes/ci.md"
printf '# why: .github/ci-notes/ci.md#daemon_go_tests-the-daemon-go-job-what-its-two-go-steps-read-together\n# why: .github/ci-notes/ci.md#no-such-section\n' \
  >>"$root/.github/actions/plugin-pack/action.yml"
run_check "$root"
check "fails" "$(is "$status" 1)"
check "names the heading that names no output or job (rule 33)" \
  "$(has "$out" "\`## daemon_go_tests: The daemon-go job: what its two Go steps read together\` names daemon_go_tests, which is neither")"
check "names the section no link names (rule 33)" "$(has "$out" "\`## Old filters\` is the target of no why: link")"
check "names the link to no section (rule 33)" "$(has "$out" "links .github/ci-notes/ci.md#no-such-section, a section that file does not hold")"

echo "case: a darwin vet over a module whose tag sets cover it, and over two that do not"
root=$(fresh vet)
mkdir -p "$root/vetmod" "$root/.github/actions/vetmod"
printf 'module example.com/vetmod\n\ngo 1.22\n' >"$root/vetmod/go.mod"
printf 'go 1.22\n\nuse .\n' >"$root/vetmod/go.work"
printf 'package vetmod\n\nfunc A() int { return 1 }\n' >"$root/vetmod/a.go"
printf '//go:build special\n\npackage vetmod\n\nfunc B() int { return 2 }\n' >"$root/vetmod/b_special.go"
printf '//go:build !special\n\npackage vetmod\n\nfunc C() int { return 3 }\n' >"$root/vetmod/c_plain.go"
vet_action() { # vet_action <run text>
  {
    printf 'name: vet\ndescription: A darwin vet.\nruns:\n  using: composite\n  steps:\n'
    printf '    - name: Vet vetmod for darwin\n      shell: bash\n      working-directory: vetmod\n      run: |\n'
    sed 's/^/        /' <<<"$1"
  } >"$root/.github/actions/vetmod/action.yml"
}
vet_action "$(PYTHONPATH="$root/.github/scripts" python3 -c 'import ci_pins; print(ci_pins.darwin_vet_script("vetmod"))')"
pins "$root" 'DARWIN_VETS = [(".github/actions/vetmod/action.yml", "Vet vetmod for darwin", "vetmod")]'
run_check "$root"
check "a vet over the sets go_build_tags.py prints passes" "$(is "$status" 0)"
check "reports the two sets it vets under" "$(has "$out" '"Vet vetmod for darwin": 2 tag set(s), 3 of 3 files compiled, 0 missed')"
vet_action $'GOOS=darwin GOARCH=arm64 go vet -tags=special ./...'
run_check "$root"
check "a vet that stops reading go_build_tags.py fails (rule 20)" "$(is "$status" 1)"
check "names the step and the vet it must run" \
  "$(has "$out" 'holds no single step "Vet vetmod for darwin" in vetmod whose run: is the darwin vet that takes its tag sets from .github/scripts/go_build_tags.py')"
vet_action "$(PYTHONPATH="$root/.github/scripts" python3 -c 'import ci_pins; print(ci_pins.darwin_vet_script("vetmod"))')"
replace "$root/.github/scripts/go_build_tags.py" '    sets = [every]' '    return [every]'
run_check "$root"
check "a set list that misses a file fails (rule 20)" "$(is "$status" 1)"
check "names the file no set compiles" "$(has "$out" 'none of them compiles vetmod/c_plain.go, which darwin builds with -tags (none)')"

summary "check-ci-layout.sh holds the CI layout"
