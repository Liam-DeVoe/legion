#!/usr/bin/env bash
# Sourced by check-legion-version-inputs.test.sh, at the point where these cases run: the cases for
# what decides whether a release job runs at all. They hold each decision job's and each chained
# job's needs: and own if: (the review rounds' N5, N6 and J mutations), the workflows' on: triggers,
# and the push paths that must cover what a version step reads (P and R).
#
# They build their fixtures with check-legion-version-inputs-fixtures.sh and read the harness's
# $plan_file and $readers_rule; they set nothing the harness reads after them. The harness is the
# one entry point, and its summary counts these cases with the rest.
#
# Not executable: `source "$script_dir/check-legion-version-inputs-run-cases.sh"`.
# shellcheck disable=SC2154 # out and status are the fixture library's run_check's, plan_file and
# readers_rule the harness's

# What each job that carries a decision needs, order aside: the round-8 review's N5 mutations.
needs_rule="where $plan_file's JOB_NEEDS names"
echo "case: a decision job's needs: changed (N5a, N5b, N5c)"
root=$(written needs-publish)
replace "$root/.github/workflows/release.yaml" '"claude_envoy_build", "legion_build", ' '"claude_envoy_build", '
run_check "$root"
check "exits 1 on legion_build taken from publish's needs (N5a)" "$(is "$status" 1)"
check "names the needs and the module's" \
  "$(contains "$out" "publish's needs: is \`\[\"claude_envoy_build\", \"pi_envoy_build\", \"pi_legion_build\"\]\`, $needs_rule")"
root=$(written needs-release-publish)
replace "$root/.github/workflows/release-envoy-listener.yaml" '"image_index", "release_build", ' '"image_index", '
run_check "$root"
check "exits 1 on release_build taken from release_publish's needs (N5b)" "$(is "$status" 1)"
check "names it" "$(contains "$out" "release_publish's needs: is \`\[\"build\", \"image_index\", \"targets\"\]\`")"
root=$(written needs-added)
replace "$root/.github/workflows/release.yaml" $'    runs-on: ubuntu-24.04\n' $'    runs-on: ubuntu-24.04\n    needs: gate\n'
printf '  gate:\n    runs-on: ubuntu-24.04\n    if: github.ref != %s\n    steps:\n      - run: "true"\n' "'refs/heads/main'" \
  >> "$root/.github/workflows/release.yaml"
run_check "$root"
check "exits 1 on legion_build given a need that skips on main (N5c)" "$(is "$status" 1)"
check "names it" "$(contains "$out" "legion_build's needs: is \`gate\`, $needs_rule \`\[\]\` for it")"
root=$(written needs-order)
replace "$root/.github/workflows/release.yaml" '"claude_envoy_build", "legion_build", "pi_envoy_build", "pi_legion_build"' \
  'pi_legion_build, legion_build, claude_envoy_build, pi_envoy_build'
run_check "$root"
check "passes publish's needs in another order" "$(is "$status" 0)"

# The jobs the decision jobs need, which a decision job's own if: reads or which run before it:
# their own if: and needs: are pinned as the decision jobs' are (the round-8 review's N6).
echo "case: a job the decision jobs need skips on main (N6a, N6b)"
root=$(written chain-targets-if)
replace "$root/.github/workflows/release-envoy-listener.yaml" $'  targets:\n    runs-on: ubuntu-24.04\n' \
  $'  targets:\n    runs-on: ubuntu-24.04\n    if: github.ref != \'refs/heads/main\'\n'
run_check "$root"
check "exits 1 on targets given an if: false on main (N6a)" "$(is "$status" 1)"
check "names the job's if: and the module's nothing" \
  "$(contains "$out" "the job's own if: of targets has the if: \`github.ref != 'refs/heads/main'\`, where $plan_file's DECISION_READERS names nothing for it")"
check "gives a chain job's reason, not a reader's" \
  "$(contains "$out" "names nothing for it: a job that carries the release decision needs it, so an if: that skips it skips that job too")"
check "and not the reader's" "$(is "$(contains "$out" "of targets has the if: .*it reads a checked version step's decision")" false)"
root=$(written chain-plugin-if)
replace "$root/.github/workflows/release.yaml" $'  pi_envoy_build:\n    runs-on: ubuntu-24.04\n    if: "github.ref == \'refs/heads/main\'"\n' \
  $'  pi_envoy_build:\n    runs-on: ubuntu-24.04\n    if: false\n'
run_check "$root"
check "exits 1 on a plugin build's if: false (N6b)" "$(is "$status" 1)"
check "names it" "$(contains "$out" "the job's own if: of pi_envoy_build has the if: \`false\`, where $plan_file's DECISION_READERS names \`github.ref == 'refs/heads/main'\` for it: a job that carries the release decision needs it")"
root=$(written chain-needs)
replace "$root/.github/workflows/release-envoy-listener.yaml" $'    needs: ["build", "targets"]\n' $'    needs: ["targets"]\n'
run_check "$root"
check "exits 1 on docker's needs changed" "$(is "$status" 1)"
check "names it" "$(contains "$out" "docker's needs: is \`\[\"targets\"\]\`, $needs_rule \`\[\"build\", \"targets\"\]\` for it")"

# worker_image_release carries publish's release on, and GitHub's needs context holds a job's
# direct needs alone: its needs and worker_image's own if: are pinned (the round-9 review's J).
echo "case: worker_image_release's needs changed, or worker_image skipped on main (J4, J5, J6)"
root=$(written worker-needs-publish)
replace "$root/.github/workflows/release.yaml" $'    needs: ["publish", "worker_image"]\n' $'    needs: ["worker_image"]\n'
run_check "$root"
check "exits 1 on publish taken from worker_image_release's needs (J4)" "$(is "$status" 1)"
check "names the needs and the module's" \
  "$(contains "$out" "worker_image_release's needs: is \`\[\"worker_image\"\]\`, $needs_rule \`\[\"publish\", \"worker_image\"\]\` for it")"
root=$(written worker-if)
replace "$root/.github/workflows/release.yaml" \
  $'    if: "always() && (needs.legion_build.result == \'success\' || needs.legion_build.result == \'skipped\')"\n' \
  $'    if: github.ref != \'refs/heads/main\'\n'
run_check "$root"
check "exits 1 on worker_image's if: false on main (J5)" "$(is "$status" 1)"
check "names its if: and the module's" \
  "$(contains "$out" "the job's own if: of worker_image has the if: \`github.ref != 'refs/heads/main'\`, where $plan_file's DECISION_READERS names \`always() && (needs.legion_build.result == 'success'.*: a job that carries the release decision needs it")"
root=$(written worker-needs-attest)
replace "$root/.github/workflows/release.yaml" $'    needs: ["publish", "worker_image"]\n' $'    needs: ["worker_image", "worker_image_attest"]\n'
run_check "$root"
check "exits 1 on worker_image_attest in place of publish (J6)" "$(is "$status" 1)"
check "names it" "$(contains "$out" "worker_image_release's needs: is \`\[\"worker_image\", \"worker_image_attest\"\]\`")"

# Each workflow's on: events, push keys and push branches are pinned: a push to main is what
# releases, so another filter skips every release with a green run and no release step changes.
trigger_rule="where $plan_file's"
echo "case: a workflow's push branch changed, an event taken away, or a push filter added"
root=$(written on-branches)
replace "$root/.github/workflows/release-envoy-listener.yaml" $'    branches: ["main"]\n' $'    branches: ["elsewhere"]\n'
run_check "$root"
check "exits 1 on branches: [elsewhere] on the listener" "$(is "$status" 1)"
check "names the branches and the module's" \
  "$(contains "$out" "release-envoy-listener.yaml's on.push.branches is \`\[\"elsewhere\"\]\`, $trigger_rule PUSH_BRANCHES names \`\[\"main\"\]\`")"
root=$(written on-events)
replace "$root/.github/workflows/release.yaml" $'  workflow_dispatch:\n' ''
run_check "$root"
check "exits 1 on workflow_dispatch taken from release.yaml" "$(is "$status" 1)"
check "names the events and the module's" "$(contains "$out" "release.yaml's on: is .*, $trigger_rule TRIGGERS names the events")"
root=$(written on-push-keys)
replace "$root/.github/workflows/release.yaml" $'    paths: ["**"]\n' $'    paths: ["**"]\n    tags: ["v*"]\n'
run_check "$root"
check "exits 1 on a tags: filter added to release.yaml's push" "$(is "$status" 1)"
check "names the keys" "$(contains "$out" "release.yaml's on.push sets \`\[\"branches\", \"paths\", \"tags\"\]\`, $trigger_rule PUSH_KEYS names")"

# Each path a version step reads is covered whole by a push path - `**`, the file itself, or
# `X/**` for a directory X it lies in - with no later `!` pattern reaching in, but one inside an
# exclude of the arguments (the round-9 review's P).
paths_rule="runs the workflow for no change to"
echo "case: a path the version step reads left out of on.push.paths (P1, P2, P4, P6)"
root=$(written paths-release)
replace "$root/.github/workflows/release.yaml" $'    paths: ["**"]\n' $'    paths: ["packages/**", "go.work", "go.work.sum"]\n'
run_check "$root"
check "exits 1 on release.yaml without its target list (P1)" "$(is "$status" 1)"
check "names the path and what reads it" \
  "$(contains "$out" "release.yaml's on.push.paths $paths_rule .github/go-release-targets.json, which its version step reads through LEGION_VERSION_ARGS and LEGION_GO_INPUTS")"
root=$(written paths-listener-modules)
replace "$root/.github/workflows/release-envoy-listener.yaml" $'    paths: ["**"]\n' $'    paths: ["packages/envoy/**", ".github/**"]\n'
run_check "$root"
check "exits 1 on the listener without the workspace's module files (P2)" "$(is "$status" 1)"
for path in go.work go.work.sum packages/daemon/go.mod packages/daemon/go.sum; do
  check "names $path" "$(contains "$out" "release-envoy-listener.yaml's on.push.paths $paths_rule $path, which its version step reads through LEGION_ENVOY_VERSION_ARGS")"
done
root=$(written paths-listener-sum)
replace "$root/.github/workflows/release-envoy-listener.yaml" $'    paths: ["**"]\n' $'    paths: ["packages/**", ".github/**", "go.work"]\n'
run_check "$root"
check "exits 1 on the listener without go.work.sum alone (P4)" "$(is "$status" 1)"
check "names it" "$(contains "$out" "$paths_rule go.work.sum, which")"
check "and nothing else" "$(is "$(grep -c '::error' <<< "$out")" 1)"
root=$(written paths-listener-targets)
replace "$root/.github/workflows/release-envoy-listener.yaml" $'    paths: ["**"]\n' $'    paths: ["packages/**", "go.work", "go.work.sum"]\n'
run_check "$root"
check "exits 1 on the listener without its target list (P6's form)" "$(is "$status" 1)"
check "names it" "$(contains "$out" "release-envoy-listener.yaml's on.push.paths $paths_rule .github/go-release-targets.json")"
root=$(written paths-generated)
replace "$root/.github/workflows/release.yaml" $'    paths: ["**"]\n' $'    paths: ["packages/daemon/**", ".github/**", "go.work", "go.work.sum"]\n'
run_check "$root"
check "exits 1 on a package of the generated block left out" "$(is "$status" 1)"
check "names it" "$(contains "$out" "$paths_rule packages/envoy/alpha/, which its version step reads through LEGION_VERSION_ARGS and LEGION_GO_INPUTS")"

# GitHub's `**` keeps the `/` before it: `F/**` covers what lies inside a directory F and never the
# file F (the round-10 review's R1 to R3, the P cases in another spelling).
echo "case: a file the version step reads written as X/** in on.push.paths (R1, R2, R3)"
root=$(written paths-file-as-dir-listener)
replace "$root/.github/workflows/release-envoy-listener.yaml" $'    paths: ["**"]\n' \
  $'    paths: ["packages/**", ".github/**", "go.work/**", "go.work.sum"]\n'
run_check "$root"
check "exits 1 on the listener's go.work as go.work/** (R1)" "$(is "$status" 1)"
check "names it" \
  "$(contains "$out" "release-envoy-listener.yaml's on.push.paths $paths_rule go.work, which its version step reads through LEGION_ENVOY_VERSION_ARGS")"
check "and nothing else, so packages/** still covers the directories" "$(is "$(grep -c '::error' <<< "$out")" 1)"
root=$(written paths-file-as-dir-release)
replace "$root/.github/workflows/release.yaml" $'    paths: ["**"]\n' \
  $'    paths: ["packages/**", "go.work", "go.work.sum", ".github/go-release-targets.json/**"]\n'
run_check "$root"
check "exits 1 on release.yaml's target list as a directory glob (R2)" "$(is "$status" 1)"
check "names it" \
  "$(contains "$out" "release.yaml's on.push.paths $paths_rule .github/go-release-targets.json, which its version step reads through LEGION_VERSION_ARGS and LEGION_GO_INPUTS")"
root=$(written paths-file-as-dir-modules)
replace "$root/.github/workflows/release-envoy-listener.yaml" $'    paths: ["**"]\n' \
  $'    paths: ["packages/envoy/**", ".github/**", "go.work/**", "go.work.sum/**", "packages/daemon/go.mod/**", "packages/daemon/go.sum/**"]\n'
run_check "$root"
check "exits 1 on the listener's four module files as directory globs (R3)" "$(is "$status" 1)"
for path in go.work go.work.sum packages/daemon/go.mod packages/daemon/go.sum; do
  check "names $path" "$(contains "$out" "release-envoy-listener.yaml's on.push.paths $paths_rule $path, which its version step reads through LEGION_ENVOY_VERSION_ARGS")"
done

echo "case: a later ! pattern reaching into a path the version step reads"
root=$(written paths-negated)
replace "$root/.github/workflows/release-envoy-listener.yaml" $'    paths: ["**"]\n' $'    paths: ["**", "!packages/envoy/alpha/**"]\n'
run_check "$root"
check "exits 1" "$(is "$status" 1)"
check "names the pattern and the path" \
  "$(contains "$out" "release-envoy-listener.yaml's on.push.paths pattern \`!packages/envoy/alpha/\*\*\`, after \`\*\*\`, can leave out a change to packages/envoy/")"
root=$(written paths-negated-excluded)
replace "$root/.github/workflows/release-envoy-listener.yaml" $'    paths: ["**"]\n' $'    paths: ["**", "!packages/envoy/infra/**"]\n'
run_check "$root"
check "passes a ! pattern inside an exclude of the arguments" "$(is "$status" 0)"
root=$(written paths-unread)
replace "$root/.github/workflows/release.yaml" $'    paths: ["**"]\n' $'    paths: ["**", "!go.work+"]\n'
run_check "$root"
check "exits 1 on a pattern GitHub reads another way" "$(is "$status" 1)"
check "names it" "$(contains "$out" "release.yaml's on.push.paths pattern \`!go.work+\` uses")"

# The four decision jobs' own if: (position 0), which the fixtures render on each job: one `if:
# false` there skips every reader of the job at once.
echo "case: a decision job's own if: false"
root=$(written job-if-legion-build)
replace "$root/.github/workflows/release.yaml" $'    if: "github.ref == \'refs/heads/main\'"\n' $'    if: false\n'
run_check "$root"
check "exits 1 on legion_build" "$(is "$status" 1)"
check "names the job's if: and the module's" \
  "$(contains "$out" "the job's own if: of legion_build has the if: \`false\`, where $readers_rule \`github.ref == 'refs/heads/main'\` for it")"
root=$(written job-if-publish)
replace "$root/.github/workflows/release.yaml" $'    if: "always() && github.ref == \'refs/heads/main\'"\n' $'    if: false\n'
run_check "$root"
check "exits 1 on publish" "$(is "$status" 1)"
check "names it" "$(contains "$out" "the job's own if: of publish has the if: \`false\`")"
root=$(written job-if-release-build)
replace "$root/.github/workflows/release-envoy-listener.yaml" $'    if: "github.ref == \'refs/heads/main\'"\n' $'    if: false\n'
run_check "$root"
check "exits 1 on the listener's release_build" "$(is "$status" 1)"
check "names it" "$(contains "$out" "the job's own if: of release_build has the if: \`false\`")"
root=$(written job-if-removed)
replace "$root/.github/workflows/release-envoy-listener.yaml" $'    if: "always() && github.ref == \'refs/heads/main\'' $'    if: "github.ref == \'refs/heads/main\''
run_check "$root"
check "exits 1 on release_publish's if: changed" "$(is "$status" 1)"
check "names it" "$(contains "$out" "the job's own if: of release_publish has the if: \`github.ref == 'refs/heads/main' && needs.build.result")"

