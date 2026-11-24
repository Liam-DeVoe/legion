#!/usr/bin/env bash
# Tests for `ci-ok.sh`: ci-ok passes only when every job the always-run list names succeeded,
# every job `changes` selected succeeded, and every job it did not select succeeded or was
# skipped.
#
# Every input of ci-ok.sh but the one GitHub read below comes from its environment, so each case
# hands it the NEEDS ci.yaml's ci-ok job builds with `toJSON(needs)`, which ci-ok-report.sh builds
# for its early post too,
# and the SELECTED_BY, PULL_REQUEST_ONLY and ALWAYS_RUN of ci.yaml's env, shrunk to four selected
# jobs: typecheck and docs, each selected by the output of its own id, which SELECTED_BY therefore
# does not name, envoy-client, whose output SELECTED_BY names as an exception, and worker-image,
# another exception, which also runs only on a pull request. The always-run list is ci.yaml's own
# `changes` and lint, and a third job,
# smoke-gate, for the cases that prove the list is read rather than hard-coded. Some cases pass
# another event than pull_request, which is what
# PULL_REQUEST_ONLY turns on; the script takes the event as a string and knows no workflow's
# triggers. docs also stands for one of the slow suites, whose output a pull request
# without the label `full-ci` or one of the job's own lane labels holds false whatever the paths
# filter answered: eight cases hand the run the filter's own verdicts beside the outputs
# (with_verdicts below) and hold the report to telling that skip from a filter miss, and to
# naming such a job as not run at this head, with its own lane labels, or as held back on every
# pull request where no label lifts it, in its log and its job summary, only where the pull
# request held it back. Four cases run a merge group whose verdicts
# and unnamed-file verdict say which failed job no pull request of the group's files ran, and hold
# the line that names it, its step summary, and its absence where a changed file matched no filter.
# The check-run names of legs named by an expression are read off a ci.yaml of the case's own
# (CI_OK_WORKFLOW): one with no such name, but where a case reads a leg named by an expression.
#
# A job that ends `failure` is asked about: ci-ok.sh reads the check runs on this run's head to
# tell a job the platform killed from one the change broke (runner-shutdown.sh), so each case
# runs with a stand-in `gh` first on PATH, which answers that read and each check run's
# annotations from files the case writes.
#
# Run from anywhere: .github/scripts/ci-ok.test.sh
# CI runs it in ci.yaml's scripts-held job.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# The stand-in GitHub: the check runs on the head this run's jobs are on, and the annotations of
# one of them, each answered as `gh api --jq` would print it. A missing check-runs file is the
# route refusing the read.
mkdir "$work/bin"
cat > "$work/bin/gh" <<GH
#!/usr/bin/env bash
set -euo pipefail
case "\$*" in
  *"/commits/abc123/check-runs"*)
    if [ ! -e "$work/check-runs" ]; then echo "HTTP 502" >&2; exit 1; fi
    cat "$work/check-runs" ;;
  *"/check-runs/"*"/annotations"*)
    id=
    for arg in "\$@"; do
      case \$arg in
        *"/check-runs/"*"/annotations"*)
          id=\${arg#*/check-runs/}
          id=\${id%%/*} ;;
      esac
    done
    if [ -e "$work/annotations.\$id" ]; then
      jq -r 'select(.annotation_level == "failure") | .message' "$work/annotations.\$id"
    fi ;;
  *) echo "unexpected gh \$*" >&2; exit 2 ;;
esac
GH
chmod +x "$work/bin/gh"

# check_runs <job>[=<run id>]...: the check runs on the head, as the commit's check-runs route
# lists them, each job's own numbered in the order given and carrying the details_url of run 41
# unless the case gives it another run's. It clears the annotations with them, so a job of a run
# written this way carries none and its failure is read as a failure.
check_runs() {
  local spec name run n=0
  rm -f "$work/check-runs" "$work"/annotations.*
  for spec in "$@"; do
    name=${spec%%=*}
    run=${spec#*=}
    [ "$run" != "$spec" ] || run=41
    jq -nc --arg name "$name" --arg url "https://api.github.com/repos/o/r/check-runs/$n" \
      --arg details "https://github.com/o/r/actions/runs/$run/job/$n" \
      '{name: $name, url: $url, details_url: $details}' >> "$work/check-runs"
    n=$((n + 1))
  done
}

# annotations <job> <message>...: the failure annotations GitHub filed on that job's check run,
# which is where the runner's shutdown shows.
annotations() {
  local job=$1 id
  shift
  id=$(jq -r --arg job "$job" 'select(.name == $job) | .url | split("/")[-1]' "$work/check-runs")
  printf '%s\n' "$@" | jq -R '{annotation_level: "failure", message: .}' > "$work/annotations.$id"
}

# Every case but the last four runs over a head whose check runs carry no annotation at all, so
# nothing of it is read as a runner shutdown and a failure is a failure.
check_runs changes lint typecheck envoy-client worker-image docs smoke-gate contracts

selected_by='{"envoy-client": "envoy_client", "worker-image": "worker_image"}'
pull_request_only='["worker-image"]'
always_run='["changes", "lint"]'
# The same list with a third always-run job, which ci-ok.sh must judge as it judges lint.
three_always='["changes", "lint", "smoke-gate"]'

# needs <changes result> <changes outputs> <lint result> [<job>=<result>]...: the NEEDS
# ci-ok-report.sh hands ci-ok.sh, every other job with no outputs.
needs() {
  local changes=$1 outputs=$2 lint=$3 pair doc
  shift 3
  doc=$(jq -n --arg c "$changes" --argjson o "$outputs" --arg l "$lint" \
    '{changes: {result: $c, outputs: $o}, lint: {result: $l, outputs: {}}}')
  for pair in "$@"; do
    doc=$(jq --arg j "${pair%%=*}" --arg r "${pair#*=}" '.[$j] = {result: $r, outputs: {}}' <<<"$doc")
  done
  echo "$doc"
}

# The outputs of a `changes` that selected typecheck and worker-image and not envoy-client or docs.
selects='{"typecheck": "true", "envoy_client": "false", "worker_image": "true", "docs": "false"}'
# The same with docs selected too.
selects_docs='{"typecheck": "true", "envoy_client": "false", "worker_image": "true", "docs": "true"}'
# The same with envoy-client selected in docs' place.
selects_envoy='{"typecheck": "true", "envoy_client": "true", "worker_image": "true", "docs": "false"}'
# The same outputs read on another event, docs unselected: ci-ok.sh takes EVENT as given.
push_selects='{"typecheck": "true", "envoy_client": "false", "worker_image": "true", "docs": "false"}'

# `changes` publishes the paths filter's own verdict for every filter beside its outputs
# (ci.yaml's changes job, filter_verdicts), as the JSON text toJSON() gives. The slow
# suites' outputs carry a `full-ci` term in front of that verdict, so on a pull request without
# the label the output is false whatever the filter said; docs stands for one of them below.
with_verdicts() { # with_verdicts <outputs> <the filter's own verdicts>
  jq -nc --argjson outputs "$1" --argjson verdicts "$2" '$outputs + {filter_verdicts: ($verdicts | tojson)}'
}
# The filter said yes and the output is false: only the missing label can have done that.
gated_selects=$(with_verdicts "$selects" \
  '{"typecheck": "true", "envoy_client": "false", "worker_image": "true", "docs": "true"}')
# The filter said no, so the label decided nothing and the output would be false with it too.
unmatched_selects=$(with_verdicts "$selects" \
  '{"typecheck": "true", "envoy_client": "false", "worker_image": "true", "docs": "false"}')

# run_ci_ok <needs> [<event>] [<always run>]: sets out, status and step_summary. The event
# defaults to pull_request and the always-run list to ci.yaml's own two jobs; the workflow the
# names are read off is $workflow. The GitHub half of the environment is the ci-ok job's own,
# pointed at the stand-in above: the head its check runs are on, the run they must belong to, and
# gh-read.sh's retry delay turned off. GITHUB_STEP_SUMMARY is a fresh file of each case, never the
# summary of the job running this harness, and step_summary is what ci-ok.sh wrote to it.
# The case workflow's changes job holds the label terms of the held outputs docs and
# envoy_client, as ci.yaml's write them, which ci-ok.sh reads for the labels a not-run line names
# (ci-ok.jq, hold_labels).
cat >"$work/ci.yaml" <<'YAML'
jobs:
  changes:
    outputs:
      docs: ${{ github.event_name != 'pull_request' && 'true' || (contains(github.event.pull_request.labels.*.name, 'full-ci') || contains(github.event.pull_request.labels.*.name, 'lane:docs')) && steps.filter.outputs.docs || 'false' }}
      envoy_client: ${{ github.event_name != 'pull_request' && 'true' || (contains(github.event.pull_request.labels.*.name, 'full-ci') || contains(github.event.pull_request.labels.*.name, 'lane:envoy_client')) && steps.filter.outputs.envoy_client || 'false' }}
  typecheck: {runs-on: ubuntu-24.04-arm}
  envoy-client: {runs-on: ubuntu-24.04-arm}
YAML
workflow=$work/ci.yaml
run_ci_ok() {
  : >"$work/step-summary"
  set +e
  out=$(PATH="$work/bin:$PATH" GH_TOKEN=t GITHUB_REPOSITORY=o/r GITHUB_RUN_ID=41 \
    GH_RETRY_SECONDS=0 HEAD_SHA=abc123 NEEDS=$1 EVENT=${2:-pull_request} SELECTED_BY=$selected_by \
    PULL_REQUEST_ONLY=$pull_request_only ALWAYS_RUN=${3:-$always_run} CI_OK_WORKFLOW=$workflow \
    GITHUB_STEP_SUMMARY="$work/step-summary" "$script_dir/ci-ok.sh" 2>&1)
  status=$?
  set -e
  step_summary=$(cat "$work/step-summary")
}

echo "case: a job changes selected ended skipped"
run_ci_ok "$(needs success "$selects" success typecheck=skipped envoy-client=skipped worker-image=success docs=skipped)"
check "fails" "$(is "$status" 1)"
check "names the job, its result and why" \
  "$(contains "$out" "^::error::typecheck ended skipped, but changes selected it (needs.changes.outputs.typecheck is true)")"
check "says what the skip means" \
  "$(contains "$out" "a selected job must succeed, and a skip here means the job's if: did not admit the run although changes chose it, a disagreement between the two the author must fix\$")"
check "prints its selection" "$(contains "$out" '^typecheck: skipped (selected: needs.changes.outputs.typecheck is true)$')"

echo "case: every selected job succeeded and the unselected ones were skipped"
run_ci_ok "$(needs success "$selects" success typecheck=success envoy-client=skipped worker-image=success docs=skipped)"
check "passes" "$(is "$status" 0)"
check "prints every job's result" "$(contains "$out" '^changes: success$')"
check "and lint's" "$(contains "$out" '^lint: success$')"
check "and the selected job's selection" "$(contains "$out" '^typecheck: success (selected: needs.changes.outputs.typecheck is true)$')"
check "and the unselected job's" \
  "$(contains "$out" '^envoy-client: skipped (not selected: needs.changes.outputs.envoy_client is false)$')"
check "says why it passed" "$(contains "$out" 'changes and lint succeeded, every job changes selected succeeded')"
check "reports no error" "$(is "$(contains "$out" '::error::')" false)"

echo "case: a job changes did not select, skipped, beside a selected one that succeeded"
run_ci_ok "$(needs success '{"typecheck": "true", "envoy_client": "false", "worker_image": "false", "docs": "false"}' success \
  typecheck=success envoy-client=skipped worker-image=skipped docs=skipped)"
check "passes" "$(is "$status" 0)"
check "prints worker-image unselected" \
  "$(contains "$out" '^worker-image: skipped (not selected: needs.changes.outputs.worker_image is false)$')"

echo "case: a selected job was cancelled"
# A selected job reads `cancelled` when its own `timeout-minutes` cut it off, when the run was
# cancelled, or when a force-cancel ended it: none of the three is its filter's doing, so the
# error sends the author to the job's log and its limit rather than to its `if:`.
run_ci_ok "$(needs success "$selects" success typecheck=cancelled envoy-client=skipped worker-image=success docs=skipped)"
check "fails" "$(is "$status" 1)"
check "names it" "$(contains "$out" '^::error::typecheck ended cancelled, but changes selected it')"
check "says it was cut off, and where to look" \
  "$(contains "$out" "a cancellation here is the job cut off rather than filtered out — its own timeout-minutes, a cancelled run or a force-cancel — so the job's log and its timeout-minutes say which, not its filter\$")"
check "does not call it a disagreement between the filter and the if:" \
  "$(is "$(contains "$out" 'did not admit the run although changes chose it')" false)"

echo "case: an unselected job failed"
run_ci_ok "$(needs success "$selects" success typecheck=success envoy-client=failure worker-image=success docs=skipped)"
check "fails" "$(is "$status" 1)"
check "names it" "$(contains "$out" '^::error::envoy-client ended failure; a job changes did not select must succeed or be skipped')"

echo "case: worker-image's output is true on a push, where its if: admits only pull_request"
run_ci_ok "$(needs success "$selects" success typecheck=success envoy-client=skipped worker-image=skipped docs=skipped)" push
check "passes" "$(is "$status" 0)"
check "prints why it is not selected" \
  "$(contains "$out" '^worker-image: skipped (not selected: it runs only on pull_request, and this run is push)$')"

echo "case: worker-image's output is true on a pull request, and it was skipped"
run_ci_ok "$(needs success "$selects" success typecheck=success envoy-client=skipped worker-image=skipped docs=skipped)"
check "fails" "$(is "$status" 1)"
check "names it" \
  "$(contains "$out" "^::error::worker-image ended skipped, but changes selected it (needs.changes.outputs.worker_image is true on a pull_request)")"

echo "case: changes' docs output is false and docs was skipped, on an event that is not a pull request"
run_ci_ok "$(needs success "$push_selects" success typecheck=success envoy-client=skipped worker-image=skipped docs=skipped)" push
check "passes" "$(is "$status" 0)"
check "prints why it is not selected" \
  "$(contains "$out" '^docs: skipped (not selected: needs.changes.outputs.docs is false)$')"
check "reports no error" "$(is "$(contains "$out" '::error::')" false)"

echo "case: docs' output is true on a pull request, and it was skipped"
run_ci_ok "$(needs success "$selects_docs" success typecheck=success envoy-client=skipped worker-image=success docs=skipped)"
check "fails" "$(is "$status" 1)"
check "names it" \
  "$(contains "$out" "^::error::docs ended skipped, but changes selected it (needs.changes.outputs.docs is true)")"

echo "case: docs' output is true on a merge group, and it was skipped"
run_ci_ok "$(needs success "$selects_docs" success typecheck=success envoy-client=skipped worker-image=skipped docs=skipped)" merge_group
check "fails" "$(is "$status" 1)"
check "names it" \
  "$(contains "$out" "^::error::docs ended skipped, but changes selected it (needs.changes.outputs.docs is true)")"

echo "case: docs' paths matched and the pull request has no full-ci label, so changes held it back"
run_ci_ok "$(needs success "$gated_selects" success typecheck=success envoy-client=skipped worker-image=success docs=skipped)"
check "passes, as it does for any job changes did not select" "$(is "$status" 0)"
check "says the pull request held it back, not the filter" \
  "$(contains "$out" '^docs: skipped (not selected: selected by paths, held back on a pull request without full-ci or lane:docs: runs in the merge queue)$')"
check "reports no error" "$(is "$(contains "$out" '::error::')" false)"
# The line above sits among the jobs that ran, in the same `<job>: <result>` shape, and the pass
# line after it says every job changes selected succeeded: read together they let a green ci-ok
# stand for a docs that never ran. So the verdict names docs once more, after the pass line and
# in a shape of its own, and the job summary sets it apart from the jobs that ran and passed.
not_run_docs='not run at this head: docs (selected by paths, held back on a pull request without full-ci or lane:docs; runs in the merge queue)'
check "names docs after the pass line as not run at this head" \
  "$(contains "$(sed -n '/^changes and lint succeeded/,$p' <<<"$out")" "^$not_run_docs\$")"
check "the job summary names docs as not run at this head" \
  "$(contains "$step_summary" "^- $not_run_docs\$")"
check "the job summary lists the jobs that ran and passed apart from it" \
  "$(contains "$step_summary" '^\*\*Ran and passed at this head:\*\* changes, lint, typecheck, worker-image$')"
check "the job summary's headline says the verdict passed" "$(contains "$step_summary" '^### ci-ok: success, ')"

echo "case: docs held back by the label beside a selected job that failed"
# A red ci-ok sends its reader to the failure, and the not-run line still says docs did not run,
# so a re-run that turns the failure green leaves nobody thinking docs passed with it.
run_ci_ok "$(needs success "$gated_selects" success typecheck=failure envoy-client=skipped worker-image=success docs=skipped)"
check "fails" "$(is "$status" 1)"
check "still names docs as not run at this head" "$(contains "$out" "^$not_run_docs\$")"
check "and so does the job summary" "$(contains "$step_summary" "^- $not_run_docs\$")"
check "which leaves the failed job out of the jobs that ran and passed" \
  "$(contains "$step_summary" '^\*\*Ran and passed at this head:\*\* changes, lint, worker-image$')"
check "the job summary's headline says the verdict failed" "$(contains "$step_summary" '^### ci-ok: failure, ')"

echo "case: the not-run line names each held-back job's own lane label"
# The label that turns one held-back job on is lane:<its changes output>, which is not its job id:
# envoy-client's output is envoy_client, so the line must say lane:envoy_client, never a label
# that turns on nothing.
envoy_gated=$(with_verdicts "$selects" \
  '{"typecheck": "true", "envoy_client": "true", "worker_image": "true", "docs": "false"}')
run_ci_ok "$(needs success "$envoy_gated" success typecheck=success envoy-client=skipped worker-image=success docs=skipped)"
check "passes" "$(is "$status" 0)"
check "names envoy-client with its output's label" \
  "$(contains "$out" '^not run at this head: envoy-client (selected by paths, held back on a pull request without full-ci or lane:envoy_client; runs in the merge queue)$')"
check "and no label named for the job id" "$(is "$(contains "$out" 'lane:envoy-client')" false)"
check "and names docs, whose filter said no, nowhere as not run" \
  "$(is "$(contains "$out" 'not run at this head: docs')" false)"

echo "case: the not-run line names every label the held output's terms name, and no other"
# Two lane labels lift docs here, as two of main's labels lift one held job of the layout: the line
# names both, read off the changes job's terms, and no label the terms do not hold.
cp "$workflow" "$work/ci-two-labels.yaml"
sed -i "s/'lane:docs')) \&\& steps.filter.outputs.docs/'lane:docs') || contains(github.event.pull_request.labels.*.name, 'lane:docs_more')) \&\& steps.filter.outputs.docs/" "$work/ci-two-labels.yaml"
saved=$workflow
workflow=$work/ci-two-labels.yaml
run_ci_ok "$(needs success "$gated_selects" success typecheck=success envoy-client=skipped worker-image=success docs=skipped)"
workflow=$saved
check "passes" "$(is "$status" 0)"
check "names full-ci and both lane labels" \
  "$(contains "$out" '^not run at this head: docs (selected by paths, held back on a pull request without full-ci, lane:docs or lane:docs_more; runs in the merge queue)$')"

echo "case: the not-run line of an output no label lifts says every pull request holds it back"
# kind's output is true on a merge group, and on a dispatched run that asks for it, and false on
# every pull request whatever its filter said; no label term lifts it. Its line names no label,
# which would send its reader after one that turns on nothing. docs stands for it here.
cat >"$work/ci-no-label.yaml" <<'YAML'
jobs:
  changes:
    outputs:
      docs: ${{ github.event_name == 'merge_group' && 'true' || github.event_name == 'workflow_dispatch' && inputs.kind-and-agent-harness && 'true' || 'false' }}
  typecheck: {runs-on: ubuntu-24.04-arm}
YAML
saved=$workflow
workflow=$work/ci-no-label.yaml
run_ci_ok "$(needs success "$gated_selects" success typecheck=success envoy-client=skipped worker-image=success docs=skipped)"
workflow=$saved
check "passes" "$(is "$status" 0)"
check "says every pull request held docs back" \
  "$(contains "$out" '^docs: skipped (not selected: selected by paths, held back on every pull request: runs in the merge queue)$')"
check "and names it so after the pass line" \
  "$(contains "$out" '^not run at this head: docs (selected by paths, held back on every pull request; runs in the merge queue)$')"
check "naming no label" "$(is "$(contains "$out" 'without')" false)"

echo "case: docs' paths did not match on a pull request, where the label would have changed nothing"
run_ci_ok "$(needs success "$unmatched_selects" success typecheck=success envoy-client=skipped worker-image=success docs=skipped)"
check "passes" "$(is "$status" 0)"
check "keeps today's wording" \
  "$(contains "$out" '^docs: skipped (not selected: needs.changes.outputs.docs is false)$')"
check "says nothing about a pull request holding it back" "$(is "$(contains "$out" 'held back on a pull request')" false)"
check "names no job as not run at this head" "$(is "$(contains "$out" 'not run at this head')" false)"
check "writes no job summary" "$(is "$step_summary" "")"

echo "case: docs' paths matched and the pull request carries full-ci, so docs ran"
labelled_selects=$(with_verdicts "$selects_docs" \
  '{"typecheck": "true", "envoy_client": "false", "worker_image": "true", "docs": "true"}')
run_ci_ok "$(needs success "$labelled_selects" success typecheck=success envoy-client=skipped worker-image=success docs=success)"
check "passes" "$(is "$status" 0)"
check "prints docs as selected" "$(contains "$out" '^docs: success (selected: needs.changes.outputs.docs is true)$')"
check "names no job as not run at this head" "$(is "$(contains "$out" 'not run at this head')" false)"
check "writes no job summary" "$(is "$step_summary" "")"

echo "case: a merge-group run never says full-ci, since the label term of those outputs is already true there"
run_ci_ok "$(needs success "$gated_selects" success typecheck=success envoy-client=skipped worker-image=skipped docs=skipped)" merge_group
check "passes" "$(is "$status" 0)"
check "keeps today's wording" \
  "$(contains "$out" '^docs: skipped (not selected: needs.changes.outputs.docs is false)$')"
check "says nothing about a pull request holding it back" "$(is "$(contains "$out" 'held back on a pull request')" false)"
check "names no job as not run at this head" "$(is "$(contains "$out" 'not run at this head')" false)"
check "writes no job summary" "$(is "$step_summary" "")"

echo "case: changes failed"
run_ci_ok "$(needs failure '{}' success typecheck=success envoy-client=skipped worker-image=skipped docs=skipped)"
check "fails" "$(is "$status" 1)"
check "names it and why" "$(contains "$out" '^::error::changes ended failure; it selects the jobs from this head')"
check "prints the jobs it could not select" "$(contains "$out" '^envoy-client: skipped (not selected: needs.changes.outputs.envoy_client is unset)$')"

echo "case: lint was skipped"
run_ci_ok "$(needs success "$selects" skipped typecheck=success envoy-client=skipped worker-image=success docs=skipped)"
check "fails" "$(is "$status" 1)"
check "names it and why" "$(contains "$out" '^::error::lint ended skipped; lint runs on every change')"

echo "case: a job that changes has no output for"
# Selected by the output of its own id, contracts, which SELECTED_BY does not name, finds none
# among the outputs of a changes that succeeded: nothing says what selects it.
run_ci_ok "$(needs success "$selects" success typecheck=success envoy-client=skipped worker-image=success docs=skipped \
  contracts=skipped)"
check "fails" "$(is "$status" 1)"
check "names it, its result and why" \
  "$(contains "$out" '^::error::contracts ended skipped, and ci-ok does not know what selects it: changes has no output contracts;')"
check "names the three classes it could join" \
  "$(contains "$out" "map it in CI_OK_SELECTED_BY in ci.yaml's env to the changes output its if: tests when that output is not named after the job's own id, name it in CI_OK_ALWAYS_RUN when it runs on every change, or name it in CI_OK_EXEMPT when it is outside ci-ok's accounting\$")"

echo "case: a job SELECTED_BY does not name, selected by the output of its own id"
run_ci_ok "$(needs success "$selects_docs" success typecheck=success envoy-client=skipped worker-image=success docs=failure)"
check "fails" "$(is "$status" 1)"
check "reads docs' own output" \
  "$(contains "$out" '^::error::docs ended failure, but changes selected it (needs.changes.outputs.docs is true)')"
check "and an exception's output for envoy-client" \
  "$(contains "$out" '^envoy-client: skipped (not selected: needs.changes.outputs.envoy_client is false)$')"

echo "case: a job SELECTED_BY maps is absent from NEEDS"
run_ci_ok "$(needs success "$selects" success typecheck=success worker-image=success docs=skipped)"
check "fails" "$(is "$status" 1)"
check "names it as absent" "$(contains "$out" '^::error::envoy-client is absent from NEEDS')"
check "prints it" "$(contains "$out" '^envoy-client: absent ')"

echo "case: a job changes selected failed"
run_ci_ok "$(needs success "$selects" success typecheck=failure envoy-client=skipped worker-image=success docs=skipped)"
check "fails, so the ci-ok job's own check run carries the failure" "$(is "$status" 1)"
check "names the job, its result and why" \
  "$(contains "$out" '^::error::typecheck ended failure, but changes selected it (needs.changes.outputs.typecheck is true)')"
check "prints every other job's result" "$(contains "$out" '^worker-image: success (selected')"
check "says the job failed" \
  "$(contains "$out" 'a selected job must succeed, and this one failed: its log says what broke$')"

echo "case: a job the always-run list names succeeded"
# The class is ci.yaml's CI_OK_ALWAYS_RUN, not two names in the script: a third job of the list
# is judged as lint is, and the pass line names it with the other two.
run_ci_ok "$(needs success "$selects" success typecheck=success envoy-client=skipped worker-image=success docs=skipped \
  smoke-gate=success)" pull_request "$three_always"
check "passes" "$(is "$status" 0)"
check "prints its result with no selection" "$(contains "$out" '^smoke-gate: success$')"
check "names it in the pass line" \
  "$(contains "$out" '^changes, lint and smoke-gate succeeded, every job changes selected succeeded')"

echo "case: a job the always-run list names was skipped"
run_ci_ok "$(needs success "$selects" success typecheck=success envoy-client=skipped worker-image=success docs=skipped \
  smoke-gate=skipped)" pull_request "$three_always"
check "fails" "$(is "$status" 1)"
check "names it and why, as it does lint" \
  "$(contains "$out" '^::error::smoke-gate ended skipped; smoke-gate runs on every change, so ci-ok passes only when it succeeded$')"

echo "case: a job the always-run list names is absent from NEEDS"
run_ci_ok "$(needs success "$selects" success typecheck=success envoy-client=skipped worker-image=success docs=skipped)" \
  pull_request "$three_always"
check "fails" "$(is "$status" 1)"
check "prints it absent" "$(contains "$out" '^smoke-gate: absent$')"
check "names it and why" \
  "$(contains "$out" '^::error::smoke-gate ended absent; smoke-gate runs on every change')"

echo "case: a job the always-run list does not name, which ci-ok.sh judged by name before"
# smoke-gate is in no class while the list is ci.yaml's two: the verdict fails it with the three
# remedies rather than holding it to success.
run_ci_ok "$(needs success "$selects" success typecheck=success envoy-client=skipped worker-image=success docs=skipped \
  smoke-gate=success)"
check "fails" "$(is "$status" 1)"
check "names it and the three classes it could join" \
  "$(contains "$out" '^::error::smoke-gate ended success, and ci-ok does not know what selects it: changes has no output smoke-gate; map it in CI_OK_SELECTED_BY')"

echo "case: a selected job whose runner was shut down under it"
# GitHub reports such a job `failure`, and the runner's two ##[error] lines are the annotations
# on its check run (runner-shutdown.sh holds the reading). ci-ok reads it as cancelled: the
# verdict still fails, since a selected job that did not succeed fails whichever it is, and the
# error names the shutdown rather than the change or the job's timeout-minutes.
check_runs changes lint typecheck envoy-client worker-image docs
annotations typecheck \
  'The runner has received a shutdown signal. This can happen when the runner service is stopped, or a manually started runner is canceled.' \
  'Process completed with exit code 143.'
run_ci_ok "$(needs success "$selects" success typecheck=failure envoy-client=skipped worker-image=success docs=skipped)"
check "fails" "$(is "$status" 1)"
check "reads it as cancelled" \
  "$(contains "$out" '^typecheck: cancelled (selected: needs.changes.outputs.typecheck is true)$')"
check "names the job and that changes selected it" \
  "$(contains "$out" '^::error::typecheck ended cancelled, but changes selected it (needs.changes.outputs.typecheck is true)')"
check "says a runner shutdown cut it off, and what was read" \
  "$(contains "$out" 'a runner shutdown cut this one off: its check run carries the runner.s .has received a shutdown signal. annotation and exit code 143')"
check "blames the platform rather than the change" \
  "$(is "$(contains "$out" 'this one failed: its log says what broke')" false)"
check "does not send the author to the job's timeout-minutes" \
  "$(is "$(contains "$out" 'its own timeout-minutes, a cancelled run or a force-cancel')" false)"
check "tells the author to re-run the job, since a pull request's run can be re-run" \
  "$(contains "$out" 'rather than this change breaking anything, and re-running the job is the whole of the remedy$')"

echo "case: a selected job whose runner was shut down, on a merge-group run"
# A re-run is no remedy there: ci.yaml's "Refuse a re-run attempt of a merge-group run" step
# fails ci-ok on every attempt above the first of a merge-group run, so the author adds the pull
# request to the queue again and a new group tests it.
check_runs changes lint typecheck envoy-client worker-image docs
annotations typecheck \
  'The runner has received a shutdown signal. This can happen when the runner service is stopped, or a manually started runner is canceled.' \
  'Process completed with exit code 143.'
run_ci_ok "$(needs success "$selects" success typecheck=failure envoy-client=skipped worker-image=skipped docs=skipped)" merge_group
check "fails" "$(is "$status" 1)"
check "reads it as cancelled" \
  "$(contains "$out" '^typecheck: cancelled (selected: needs.changes.outputs.typecheck is true)$')"
check "sends the author to the queue rather than to a re-run" \
  "$(contains "$out" "adding the pull request to the merge queue again is the whole of the remedy: ci.yaml's .Refuse a re-run attempt of a merge-group run. step fails ci-ok on every attempt above the first, so re-running this run cannot pass\$")"
check "does not tell the author to re-run the job" \
  "$(is "$(contains "$out" 're-running the job is the whole of the remedy')" false)"

echo "case: a selected job that failed of itself"
# The one thing this reading must not do is take a real failure for a platform kill: the job's
# own error is what its check run carries, so the result and the error are today's.
check_runs changes lint typecheck envoy-client worker-image docs
annotations typecheck 'Process completed with exit code 1.'
run_ci_ok "$(needs success "$selects" success typecheck=failure envoy-client=skipped worker-image=success docs=skipped)"
check "fails" "$(is "$status" 1)"
check "keeps it a failure" \
  "$(contains "$out" '^typecheck: failure (selected: needs.changes.outputs.typecheck is true)$')"
check "says the job failed" \
  "$(contains "$out" 'a selected job must succeed, and this one failed: its log says what broke$')"
check "says nothing of a runner shutdown" "$(is "$(contains "$out" 'runner shutdown')" false)"

echo "case: the shutdown annotation with another exit code"
# Both facts are the reading: the message alone is not a platform kill, since a job can log that
# line and still fail of itself.
check_runs changes lint typecheck envoy-client worker-image docs
annotations typecheck \
  'The runner has received a shutdown signal. This can happen when the runner service is stopped, or a manually started runner is canceled.' \
  'Process completed with exit code 1.'
run_ci_ok "$(needs success "$selects" success typecheck=failure envoy-client=skipped worker-image=success docs=skipped)"
check "fails" "$(is "$status" 1)"
check "keeps it a failure" \
  "$(contains "$out" '^typecheck: failure (selected: needs.changes.outputs.typecheck is true)$')"

echo "case: a failure of the change followed by the shutdown"
# A step failed and GitHub filed `Process completed with exit code 1.`; the job then ran its
# `if: always()` ci-ok-report step, as every job of ci.yaml does, and the runner was taken away
# during that step, so the same check run carries the runner's two lines as well. The exit code
# that is not 143 is the change's own fault, and the job stays a failure.
check_runs changes lint typecheck envoy-client worker-image docs
annotations typecheck \
  'Process completed with exit code 1.' \
  'The runner has received a shutdown signal. This can happen when the runner service is stopped, or a manually started runner is canceled.' \
  'Process completed with exit code 143.'
run_ci_ok "$(needs success "$selects" success typecheck=failure envoy-client=skipped worker-image=success docs=skipped)"
check "fails" "$(is "$status" 1)"
check "keeps it a failure" \
  "$(contains "$out" '^typecheck: failure (selected: needs.changes.outputs.typecheck is true)$')"
check "says the job failed" \
  "$(contains "$out" 'a selected job must succeed, and this one failed: its log says what broke$')"
check "says nothing of a runner shutdown" "$(is "$(contains "$out" 'runner shutdown')" false)"

echo "case: a job whose continue-on-error steps failed on purpose, then the shutdown"
# The limit of reading the exit codes rather than the steps. The tool-fallback job
# (.github/actions/tool-fallback) drives tool installs against a corrupted payload
# or a held-down host, and the installs it means to fail carry `continue-on-error: true`, their
# outcomes read by report steps after them; GitHub files `Process completed with exit code 1.` on
# the check run of such a step although the job succeeds — measured on run 43168566331, where
# check run 131016374047 ended `success` carrying 23 of them. When the runner is then taken away
# under a job like that, those
# annotations are read as its own fault and it stays a failure, so the error sends the author to
# the job's log rather than naming the shutdown and its remedy. The verdict is the same either
# way, since a selected job that did not succeed fails ci-ok whichever it is; closing the limit
# would mean reading whether the failing step was continue-on-error.
check_runs changes lint typecheck envoy-client worker-image docs
annotations typecheck \
  'Process completed with exit code 1.' \
  'Process completed with exit code 1.' \
  'Process completed with exit code 1.' \
  'The runner has received a shutdown signal. This can happen when the runner service is stopped, or a manually started runner is canceled.' \
  'Process completed with exit code 143.'
run_ci_ok "$(needs success "$selects" success typecheck=failure envoy-client=skipped worker-image=success docs=skipped)"
check "fails" "$(is "$status" 1)"
check "keeps it a failure" \
  "$(contains "$out" '^typecheck: failure (selected: needs.changes.outputs.typecheck is true)$')"
check "says the job failed" \
  "$(contains "$out" 'a selected job must succeed, and this one failed: its log says what broke$')"
check "says nothing of a runner shutdown" "$(is "$(contains "$out" 'runner shutdown')" false)"

echo "case: another run's check run of the same name carries the shutdown"
# A commit carries every workflow's check runs and every attempt's, so the run id in the
# details_url is what makes the mapping this run's: a shutdown on another run's typecheck is
# nothing to do with this one.
check_runs changes lint envoy-client worker-image docs typecheck=99
annotations typecheck \
  'The runner has received a shutdown signal. This can happen when the runner service is stopped, or a manually started runner is canceled.' \
  'Process completed with exit code 143.'
run_ci_ok "$(needs success "$selects" success typecheck=failure envoy-client=skipped worker-image=success docs=skipped)"
check "fails" "$(is "$status" 1)"
check "keeps it a failure" \
  "$(contains "$out" '^typecheck: failure (selected: needs.changes.outputs.typecheck is true)$')"

echo "case: GitHub refuses the check runs of this head"
# The read is tried three times and the verdict is reached without it: a read that did not
# happen is no evidence that the platform killed anything, so the failure stands as a failure
# and the warning says why the two could not be told apart.
rm -f "$work/check-runs"
run_ci_ok "$(needs success "$selects" success typecheck=failure envoy-client=skipped worker-image=success docs=skipped)"
check "fails" "$(is "$status" 1)"
check "keeps it a failure" \
  "$(contains "$out" '^typecheck: failure (selected: needs.changes.outputs.typecheck is true)$')"
check "warns that it could not tell a shutdown from a failure" \
  "$(contains "$out" '::warning::GitHub refused the check runs on abc123 at every attempt gh-read.sh made')"

echo "case: a leg named by an expression whose runner was shut down, read through the workflow's names"
# A leg of a matrix job written in ci.yaml whose name: is an expression carries the old check-run
# name it evaluates to, here broker-client / broker-client for a leg of envoy-client, and no job
# id: only the name read off ci.yaml ties its check run to its job, so only through it is the
# shutdown read.
check_runs changes lint typecheck 'broker-client / broker-client' worker-image docs
annotations 'broker-client / broker-client' \
  'The runner has received a shutdown signal. This can happen when the runner service is stopped, or a manually started runner is canceled.' \
  'Process completed with exit code 143.'
cat >"$work/ci-legs.yaml" <<'YAML'
jobs:
  envoy-client:
    name: ${{ format('{0} / {0}', matrix.leg) }}
    strategy:
      fail-fast: false
      matrix:
        leg: [broker-client, envoy-client]
  pr-title:
    name: ${{ github.event_name == 'workflow_dispatch' && 'pr-title (dispatch)' || 'pr-title' }}
YAML
workflow=$work/ci-legs.yaml
run_ci_ok "$(needs success "$selects_envoy" success typecheck=success envoy-client=failure worker-image=success docs=skipped)"
check "fails" "$(is "$status" 1)"
check "reads the leg's job as cancelled" \
  "$(contains "$out" '^envoy-client: cancelled (selected: needs.changes.outputs.envoy_client is true)$')"
check "names the shutdown" "$(contains "$out" 'a runner shutdown cut this one off')"
check "passes over a job it does not judge whose name reads another context" \
  "$(is "$(contains "$out" 'is no form ci-ok.jq reads')" false)"
workflow=$work/ci.yaml
run_ci_ok "$(needs success "$selects_envoy" success typecheck=success envoy-client=failure worker-image=success docs=skipped)"
check "without the name in ci.yaml the leg's check run is no job's, and the failure stands" \
  "$(contains "$out" '^envoy-client: failure (selected: needs.changes.outputs.envoy_client is true)$')"

echo "case: a job ci-ok judges whose name ci-ok.jq cannot read"
cat >"$work/ci-bad-name.yaml" <<'YAML'
jobs:
  envoy-client:
    name: ${{ github.event_name == 'merge_group' && 'envoy-client (queue)' || 'envoy-client' }}
YAML
workflow=$work/ci-bad-name.yaml
run_ci_ok "$(needs success "$selects" success typecheck=success envoy-client=skipped worker-image=success docs=skipped)"
check "fails" "$(is "$status" 1)"
check "names the job and its name" \
  "$(contains "$out" "^::error::ci-ok.sh cannot read the check-run names of .*: .*job envoy-client's name: .* is no form ci-ok.jq reads")"
workflow=$work/ci.yaml

check_runs changes lint typecheck envoy-client worker-image docs

echo "case: a job the unnamed-file step alone selected, which a wrong if: skipped"
# The changes job's output for envoy-client is true because the pull request changes a file no
# filter names, while envoy-client's own filter matched nothing: the job is selected all the same,
# so its skip is a disagreement between its if: and changes.
run_ci_ok "$(needs success "$(jq -nc --argjson o "$selects_envoy" '$o + {unnamed_files: "true",
  filter_verdicts: ({typecheck: "true", envoy_client: "false", worker_image: "true", docs: "false"} | tojson)}')" \
  success typecheck=success envoy-client=skipped worker-image=success docs=skipped)"
check "fails" "$(is "$status" 1)"
check "names the job and that changes selected it" \
  "$(contains "$out" '^::error::envoy-client ended skipped, but changes selected it (needs.changes.outputs.envoy_client is true)')"

# A merge group selects every job, and changes publishes the filters' own verdicts for the group's
# changed files and the unnamed-file step's: envoy-client's filter matched none of them.
group_outputs() { # group_outputs <envoy_client's verdict> <unnamed_files>
  jq -nc --arg verdict "$1" --arg unnamed "$2" '{typecheck: "true", envoy_client: "true", worker_image: "true",
    docs: "true", unnamed_files: $unnamed,
    filter_verdicts: ({typecheck: "true", envoy_client: $verdict, worker_image: "true", docs: "true"} | tojson)}'
}

echo "case: a job that failed in a merge group, which the filters do not select for the group's files"
run_ci_ok "$(needs success "$(group_outputs false false)" success typecheck=success envoy-client=failure \
  worker-image=skipped docs=success)" merge_group
check "fails, as any selected job's failure does" "$(is "$status" 1)"
check "names the job as one no pull request of these files runs" \
  "$(contains "$out" "^envoy-client failed, and no pull request that changes this merge group's files runs it: the filters, given those files, do not select it (filter_verdicts gives envoy_client false")"
check "sends its reader to the filter and the test" \
  "$(contains "$out" 'read its filter for a file its tests read that the filter misses, and its test for a flake$')"
check "writes it to the step summary" \
  "$(contains "$step_summary" "^- envoy-client failed, and no pull request that changes this merge group's files runs it")"
check "names no other job so" "$(is "$(grep -c 'no pull request that changes' <<<"$out")" 1)"

echo "case: the same failure in a merge group that changes a file no filter names"
# That file selects every job on a pull request too, so the pull request ran envoy-client.
run_ci_ok "$(needs success "$(group_outputs false true)" success typecheck=success envoy-client=failure \
  worker-image=skipped docs=success)" merge_group
check "fails" "$(is "$status" 1)"
check "names no job as one the pull request did not run" "$(is "$(contains "$out" 'no pull request that changes')" false)"
check "writes nothing to the step summary" "$(is "$step_summary" '')"

echo "case: the same failure in a merge group whose files the job's filter matches"
run_ci_ok "$(needs success "$(group_outputs true false)" success typecheck=success envoy-client=failure \
  worker-image=skipped docs=success)" merge_group
check "names no job as one the pull request did not run" "$(is "$(contains "$out" 'no pull request that changes')" false)"

echo "case: the same verdicts on a pull request"
run_ci_ok "$(needs success "$(group_outputs false false)" success typecheck=success envoy-client=failure \
  worker-image=success docs=success)"
check "names no job as one the pull request did not run: this run is the pull request's" \
  "$(is "$(contains "$out" 'no pull request that changes')" false)"

summary "ci-ok.sh fails unless every job changes selected succeeded"
