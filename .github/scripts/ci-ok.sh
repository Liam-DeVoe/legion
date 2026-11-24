#!/usr/bin/env bash
# ci-ok is one of the two checks main's ruleset requires of ci.yaml's router, and this script is
# its verdict. ci.yaml's `ci-ok` job runs it over `toJSON(needs)`, needing every job of the file
# but itself and those CI_OK_EXEMPT names, and fails when this script does, so the job's own
# check run carries the verdict — and a job's check run is in its run's own check suite, where a
# check run POSTed with POST /repos/.../check-runs joins the first suite at that commit, which is
# another workflow's as often as not (post-check-run.sh's header holds the measurement). It
# passes only when `changes` succeeded (it selected the jobs from this head's diff), every job
# ALWAYS_RUN names succeeded (each of those runs on every change), every job `changes` selected
# succeeded, and every job it did not select succeeded or was skipped.
#
# A selected job must succeed: skipped, cancelled or absent fails, and the error says which of
# the three it was, because they are not one fault. A skip means the job's `if:` did not admit
# the run although `changes` chose it — the two disagree, and whoever meets the failure has one
# of them to fix — and a verdict that accepted it would pass with nothing tested. A cancellation
# is the job cut off instead: its own `timeout-minutes` (a job its limit cut off reports
# `cancelled`, measured on probe run 41869007039), a cancelled run, a force-cancel, or a runner
# shutdown, none of them the filter's doing, so that error sends the author to the job's log and
# its limit rather than to its `if:`.
#
# A runner shutdown is the one of those that GitHub reports as a `failure`: the platform takes
# the machine away under a running job and the job's check run carries the runner's "has
# received a shutdown signal" annotation beside exit code 143 (runner-shutdown.sh holds the
# reading, and which fields of which endpoint it is). Such a job is read here as cancelled
# unless a failure annotation of it names another exit code: that is usually a step that broke
# the job, but a step allowed to fail leaves the same annotation on a job that succeeds, so a
# job whose `continue-on-error` steps fail on purpose — the `tool-fallback` job, whose install
# steps are ci.yaml's own — is not read as killed once one of those steps
# has failed, and a runner shutdown then stays a failure with the job's own log for a remedy.
# Where a job is read as cancelled
# and `changes` selected it, its error names the shutdown and its remedy rather than the job's
# own log or its `timeout-minutes`: nothing the change did failed, and nothing in the job's own
# limits would have saved it. The verdict's other branches
# print the sentence each of them always prints, naming the result — now `cancelled` — and no
# shutdown: `changes`, which selects the jobs from this head's diff; a job ALWAYS_RUN names,
# which runs on every change; a job `changes` did not select, which must succeed or be skipped;
# and a job in none of ci-ok's three classes, which it does not know what selects. The verdict
# does not move — a selected job that did not succeed fails ci-ok whichever of the two it is —
# so the pull request still goes red, and what changes is who it sends the author to. A failure
# runner-shutdown.sh cannot read that way, a read GitHub refused among them, stays a failure.
#
# A re-run of only the failed jobs leaves no such skip: `gh run rerun <id> --failed` was
# measured twice on pull request #2734, and each re-run kept 30 of the run's 31 jobs, ran only
# the failed job again, and ci-ok passed both times. A merge_group run of ci.yaml is never
# re-run at all: every job of a re-run attempt of a merge-group run but the `ci-ok` and `pr-title`
# jobs skips, each held back by a term of its own `if:`, deliberately, because a group is tested
# once, and a pull request the queue removed is tested again when it is added again
# (.github/ci-notes/ci.md#what-the-changes-job-selects-and-the-two-terms-every-job-carries says
# which term). Those two jobs run on such an attempt, because each is named for a required check and
# a skipped job's check run is a passing required check; a step of each fails the attempt before
# this script judges anything, so the run of skips it leaves never reaches this verdict.
#
# Its inputs, from the ci-ok job's step and from ci-ok-report.sh:
#   NEEDS              `changes`' result and outputs, as `toJSON(needs.changes)` gives them, and
#                      every other job's result, in the shape `toJSON(needs)` would give it. One
#                      of those outputs, `filter_verdicts`, is the paths filter's own answer for
#                      every filter rather than a gate on a job: it is how a job a pull request
#                      held back is told from one its paths never selected (ci-ok.jq).
#                      Another, `unnamed_files`, is the unnamed-file step's verdict: 'true' when a
#                      changed file matches no filter, which selects every job. On a merge group,
#                      where every job runs, the two together say which failed job no pull request
#                      that changes the group's files would have run (ci-ok.jq, missed_by_filters):
#                      such a job is named, on a line of its own and in the job's step summary,
#                      so its reader checks its filter for a read it misses and its test for a
#                      flake. The line decides nothing of the verdict.
#   EVENT              `github.event_name`.
#   SELECTED_BY        a JSON object of the exceptions: each job whose `if:` tests a `changes`
#                      output not named after the job's own id, to that output. Every other job
#                      but those ALWAYS_RUN names is selected by the output of its own id, and the
#                      job is selected when that output is 'true' (ci-ok.jq).
#   PULL_REQUEST_ONLY  a JSON array of the jobs whose `if:` also admits only pull_request: one is
#                      selected only when EVENT is pull_request too. (A job an event must hold
#                      back otherwise needs no list: `changes` sets its output false there, as it
#                      does for the slow suites on a pull request without the label full-ci or
#                      one of the job's own lane labels (main's names, lane:test and the like,
#                      which its output's terms name), and for kind on every pull request; the
#                      verdict accepts that skip, and its line says the pull request held it back.)
#   ALWAYS_RUN         a JSON array of the jobs no `changes` output gates, `changes` among them:
#                      each runs on every change, so ci-ok passes only when it succeeded, and a
#                      skip, a cancellation or an absence fails. ci.yaml's env names them in
#                      CI_OK_ALWAYS_RUN, and check-ci-layout.sh holds every job of that file to
#                      exactly one of the three classes this list, SELECTED_BY and CI_OK_EXEMPT
#                      make.
#   CI_OK_WORKFLOW     optional: the router's file, .github/workflows/ci.yaml beside this script
#                      by default (the ci-ok job's checkout of .github). The check-run name each
#                      leg and carrier gives through its `name:` is read off it (ci-ok.jq,
#                      check_run_jobs), since a leg whose `name:` is an expression carries the name
#                      it evaluates to and no job id, and runner-shutdown.sh reads a job's check
#                      runs through that map (ci-ok.jq, runs_of).
#   GH_TOKEN           the job's github.token, with `checks: write` (or `checks: read`): a job
#                      that ends `failure` is asked about on the commit's check runs, where the
#                      runner's shutdown annotation tells a platform kill from a fault of the
#                      change (runner-shutdown.sh).
#   HEAD_SHA           the commit this run's check runs are on: a pull request's head or
#                      github.sha, which is the expression ci.yaml's ci-ok job and the
#                      ci-ok-report action both already pass.
#   GITHUB_REPOSITORY, GITHUB_RUN_ID, as Actions sets them: the run those check runs are of.
#   GITHUB_STEP_SUMMARY, optional, as Actions sets it: the job summary the not-run list and the
#                      notes go to.
# A job of NEEDS that ALWAYS_RUN does not name, on a run whose `changes` succeeded with no output
# of the name that job is selected by, fails (ci-ok does not know what to make of its result), and
# so does a job ALWAYS_RUN or SELECTED_BY names that is absent from NEEDS. It
# prints every job's result and whether it was selected, and an `::error::` for each failure
# naming the job, its result and why.
#
# A job its paths selected and a pull request held back (ci-ok.jq's `gated`) did not run at this
# head, and the verdict accepts its skip as it accepts any job changes did not select. Its own
# line reads `<job>: skipped (...)` among the jobs that ran, and the pass line says every job
# changes selected succeeded, so a reader of a green ci-ok could take it for a job that passed.
# So after the verdict, pass or fail, each such job gets a line of its own, `not run at this head:
# <job> (selected by paths, held back on a pull request without full-ci or lane:<a> or lane:<b>;
# runs in the merge queue)`, naming each label the job's `changes` output tests (ci-ok.jq,
# hold_labels, reads them off the workflow's own terms), or `held back on every pull request` for
# an output no label lifts, and the job summary lists those lines apart from the jobs that ran and
# passed. No job runs in part: each runs all its steps wherever
# it runs, or its own output skips it, so a success is the whole job's. A run that held nothing
# back writes no summary: only a pull request can hold a job back, so ci-ok-report.sh's
# merge-group calls, inside other jobs, never write one.
#
# Run from anywhere:
#   NEEDS=... EVENT=... SELECTED_BY=... PULL_REQUEST_ONLY=... ALWAYS_RUN=... \
#     GH_TOKEN=... HEAD_SHA=... .github/scripts/ci-ok.sh
# ci.yaml's ci-ok job runs it over every job's result, and ci-ok-report.sh over the results it
# reads for the failing ci-ok it posts at once on a decided merge-group run; its tests are in
# ci-ok.test.sh, which ci.yaml's scripts-held job runs.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=.github/scripts/runner-shutdown.sh
source "$script_dir/runner-shutdown.sh"
workflow=${CI_OK_WORKFLOW:-$script_dir/../workflows/ci.yaml}
for input in NEEDS EVENT SELECTED_BY PULL_REQUEST_ONLY ALWAYS_RUN GH_TOKEN HEAD_SHA \
  GITHUB_REPOSITORY GITHUB_RUN_ID; do
  if [ -z "${!input:-}" ]; then
    echo "::error::ci-ok.sh needs $input in its environment"
    exit 1
  fi
done
if ! jq -e 'type == "object"' <<<"$NEEDS" >/dev/null 2>&1; then
  echo "::error::NEEDS is not a JSON object: $NEEDS"
  exit 1
fi
if ! jq -e 'type == "object" and all(.[]; type == "string")' <<<"$SELECTED_BY" >/dev/null 2>&1; then
  echo "::error::SELECTED_BY is not a JSON object of job to output name: $SELECTED_BY"
  exit 1
fi
if ! jq -e 'type == "array" and all(.[]; type == "string")' <<<"$PULL_REQUEST_ONLY" >/dev/null 2>&1; then
  echo "::error::PULL_REQUEST_ONLY is not a JSON array of job names: $PULL_REQUEST_ONLY"
  exit 1
fi
if ! jq -e 'type == "array" and all(.[]; type == "string") and any(. == "changes")' \
  <<<"$ALWAYS_RUN" >/dev/null 2>&1; then
  echo "::error::ALWAYS_RUN is not a JSON array of job names naming changes, whose outputs every other job is judged by: $ALWAYS_RUN"
  exit 1
fi
# Each check-run name a leg or a carrier of a job ci-ok judges gives through its `name:`, to that
# job's id, read off the workflow itself (ci-ok.jq, check_run_jobs): runner-shutdown.sh reads a
# job's check runs through it. The workflow is read as JSON once, for this map and for the label
# terms below.
if ! workflow_json=$(yq -o=json '.' "$workflow") ||
  ! CHECK_RUN_JOBS=$(jq -c -L "$script_dir" --argjson judged "$(jq -c 'keys' <<<"$NEEDS")" \
    'include "ci-ok"; check_run_jobs($judged)' <<<"$workflow_json" 2>&1); then
  echo "::error::ci-ok.sh cannot read the check-run names of $workflow's jobs: ${CHECK_RUN_JOBS-}"
  exit 1
fi

# Which of this run's failures were a runner shutdown rather than a fault of the change: the
# platform takes the machine away under a running job, GitHub reports that job `failure`, and
# its check run carries the runner's shutdown annotation beside exit code 143.
# runner_shutdown_kills reads the pair off this run's check runs, each job's by CHECK_RUN_JOBS
# (runner-shutdown.sh says which fields of which endpoint, and why a job it cannot read that way is
# no kill), and the program below reads each job it names as cancelled. A read GitHub refused
# leaves the list empty, with that script's warning in the log, so every failure stands as a
# failure.
mapfile -t failed < <(jq -r 'to_entries[] | select(.value.result == "failure") | .key' <<<"$NEEDS")
shutdown='[]'
if [ "${#failed[@]}" -gt 0 ] && killed_jobs=$(runner_shutdown_kills "${failed[@]}"); then
  shutdown=$(jq -R -s 'split("\n") | map(select(. != ""))' <<<"$killed_jobs")
fi

# One row per line, tab-separated: `info` and the line to print, `note` and a line that is also
# the step summary's, `error` and the ::error:: text, `passed` and a job that ended `success`, or
# `not_run` and the line naming a job a pull request held back. Every job is printed in NEEDS
# order, after each ALWAYS_RUN job NEEDS lacks and before each SELECTED_BY job it lacks. A job
# $shutdown names ended `failure` only because its runner was shut down, so `result` reads it as
# cancelled. `cut_off` is why a selected job that did not succeed failed: the causes those results
# carry are not one fault, and only the skip is the filter's. A `note` names a job that failed in a
# merge group although no pull request that changes the group's files runs it (missed_by_filters).
read -r -d '' program <<'JQ' || true
include "ci-ok";
(.changes.outputs // {}) as $out
| (.changes.result // "") as $changes_result
| def selection($job): selection($job; $map; $out; $pr; $event; $holds);
  def killed($job): $shutdown | any(. == $job);
  def result($job; $result):
    if $result == "failure" and killed($job) then "cancelled" else $result end;
  def cut_off($job; $result):
    if $result == "skipped" then
      "a skip here means the job's if: did not admit the run although changes chose it, a disagreement between the two the author must fix"
    elif $result == "cancelled" then
      if killed($job) then
        "a runner shutdown cut this one off: its check run carries the runner's \"has received a shutdown signal\" annotation and exit code 143, so GitHub took the machine away under the job rather than this change breaking anything, and \(if $event == "merge_group" then "adding the pull request to the merge queue again is the whole of the remedy: ci.yaml's \"Refuse a re-run attempt of a merge-group run\" step fails ci-ok on every attempt above the first, so re-running this run cannot pass" else "re-running the job is the whole of the remedy" end)"
      else
        "a cancellation here is the job cut off rather than filtered out — its own timeout-minutes, a cancelled run or a force-cancel — so the job's log and its timeout-minutes say which, not its filter"
      end
    elif $result == "failure" then
      "this one failed: its log says what broke"
    else
      "a \($result) is no conclusion at all, so this verdict was reached before the job ended"
    end;
  def rows($job; $raw):
    result($job; $raw) as $result |
    (select($result == "success") | ["passed", $job]),
    if $job == "changes" then
      ["info", "changes: \($result)"],
      (select($result != "success")
       | ["error", "changes ended \($result); it selects the jobs from this head's diff, so ci-ok passes only when it succeeded"])
    elif ($always | any(. == $job)) then
      ["info", "\($job): \($result)"],
      (select($result != "success")
       | ["error", "\($job) ended \($result); \($job) runs on every change, so ci-ok passes only when it succeeded"])
    elif $result != "absent" and $changes_result == "success" and ($out | has(output_of($job; $map)) | not) then
      ["info", "\($job): \($result) (in none of ci-ok's three classes)"],
      ["error", "\($job) ended \($result), and ci-ok does not know what selects it: changes has no output \(output_of($job; $map)); map it in CI_OK_SELECTED_BY in ci.yaml's env to the changes output its if: tests when that output is not named after the job's own id, name it in CI_OK_ALWAYS_RUN when it runs on every change, or name it in CI_OK_EXEMPT when it is outside ci-ok's accounting"]
    else
      output_of($job; $map) as $output
      | selection($job) as $s
      | ["info", "\($job): \($result) (\(if $s.selected then "selected" else "not selected" end): \($s.why))"],
        (select($result == "failure" and missed_by_filters($job; $map; $out; $event))
         | ["note", "\($job) failed, and no pull request that changes this merge group's files runs it: the filters, given those files, do not select it (filter_verdicts gives \($output) false, and no changed file is one no filter names); read its filter for a file its tests read that the filter misses, and its test for a flake"]),
        (select($s.gated and $result == "skipped")
         | ["not_run", "not run at this head: \($job) (selected by paths, held back \(held_back($holds[$output] // [])); runs in the merge queue)"]),
        if $result == "absent" then
          ["error", "\($job) is absent from NEEDS, but needs.changes.outputs.\($output) selects it; the ci-ok job's toJSON(needs) and ci-ok-report.sh both give every job ci-ok judges a result, so this is a defect of whichever of them built NEEDS"]
        elif $s.selected and $result != "success" then
          ["error", "\($job) ended \($result), but changes selected it (\($s.why)); a selected job must succeed, and \(cut_off($job; $result))"]
        elif ($s.selected | not) and $result != "success" and $result != "skipped" then
          ["error", "\($job) ended \($result); a job changes did not select must succeed or be skipped"]
        else empty end
    end;
  . as $needs
  | ($always[] | select(. as $job | $needs | has($job) | not) | rows(.; "absent")),
    ($needs | to_entries[] | rows(.key; .value.result // "absent")),
    ($map | keys_unsorted[] | select(. as $job | $needs | has($job) | not) | rows(.; "absent"))
| @tsv
JQ
# The labels that lift each held output's hold, read off the changes job's own terms in the
# workflow (ci-ok.jq, hold_labels), for the line naming a job a pull request held back.
if ! holds=$(jq -c -L "$script_dir" 'include "ci-ok"; hold_labels' <<<"$workflow_json" 2>&1); then
  echo "::error::ci-ok.sh cannot read the label terms of $workflow's changes outputs: $holds"
  exit 1
fi
rows=$(jq -L "$script_dir" -r --arg event "$EVENT" --argjson map "$SELECTED_BY" --argjson pr "$PULL_REQUEST_ONLY" \
  --argjson always "$ALWAYS_RUN" --argjson shutdown "$shutdown" --argjson holds "$holds" "$program" <<<"$NEEDS")

errors=()
notes=()
passed=()
not_run=()
while IFS=$'\t' read -r level text; do
  case $level in
    info) echo "$text" ;;
    note)
      echo "$text"
      notes+=("$text")
      ;;
    error) errors+=("$text") ;;
    passed) passed+=("$text") ;;
    not_run) not_run+=("$text") ;;
  esac
done <<<"$rows"

if [ "${#notes[@]}" -gt 0 ] && [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  {
    echo "### Failed jobs the pull request did not run"
    echo
    for note in "${notes[@]}"; do
      echo "- $note"
    done
  } >>"$GITHUB_STEP_SUMMARY"
fi

# The job summary, written only when a pull request held a job back (the header says why): the
# jobs that ran and passed, and apart from them each job its paths selected that did not run at
# this head.
if [ "${#not_run[@]}" -gt 0 ] && [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  verdict=success
  [ "${#errors[@]}" -eq 0 ] || verdict=failure
  passed_text=none
  [ "${#passed[@]}" -eq 0 ] || passed_text=$(jq -rn '$ARGS.positional | join(", ")' --args "${passed[@]}")
  {
    echo "### ci-ok: $verdict, and not every job its paths selected ran at this head"
    echo
    echo "**Not run at this head**, so this verdict says nothing of them:"
    echo
    printf -- '- %s\n' "${not_run[@]}"
    echo
    echo "**Ran and passed at this head:** $passed_text"
  } >>"$GITHUB_STEP_SUMMARY"
fi

if [ "${#errors[@]}" -gt 0 ]; then
  for error in "${errors[@]}"; do
    echo "::error::$error"
  done
  [ "${#not_run[@]}" -eq 0 ] || printf '%s\n' "${not_run[@]}"
  exit 1
fi
# The pass line names the jobs it held to success, as the errors above name theirs: ALWAYS_RUN in
# the file's order, "a, b and c".
always_run_text=$(jq -r 'if length > 1 then (.[:-1] | join(", ")) + " and " + .[-1] else .[0] end' <<<"$ALWAYS_RUN")
echo "$always_run_text succeeded, every job changes selected succeeded, and every other job succeeded or was skipped"
[ "${#not_run[@]}" -eq 0 ] || printf '%s\n' "${not_run[@]}"
