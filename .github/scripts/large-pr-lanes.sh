#!/usr/bin/env bash
# The two halves of the approval-time run of a large pull request
# (.github/ci-notes/ci.md#a-large-pull-request-runs-its-slow-suites-once-at-the-approved-head).
#
# A pull request's run holds the slow suites back (they run in the merge queue), so a defect only
# a slow suite finds first fails in the queue, which removes the entry and rebuilds every group
# behind it. A large pull request is where that costs most, so its slow suites run once more
# before the queue: at the head a reviewer approves. That run is
# .github/workflows/large-pr-lanes.yaml, which calls ci.yaml on the approval's pull_request_review
# event; on that event every `changes` output a pull request holds back takes its paths filter's
# verdict, as on a merge group, so the run selects exactly the slow suites the change's paths
# select, beside every fast job.
#
#   label  large-pr-label.yaml's job on a pull request: reads the pull request's size and puts the
#          label large-pr on a large pull request, or takes it off one that is no longer large.
#          Large is LARGE_FILES (100) changed files or more, or LARGE_LINES (10,000) additions
#          plus deletions or more. The approval workflow reads the label rather than the size,
#          because a review's event carries no size (its payload holds a simple pull request: no
#          additions, deletions or changed_files) and the workflow's jobs must be decided from the
#          event alone, before any runner starts: see `hold`. The size and the labels are read
#          from the pull_request event's payload (GITHUB_EVENT_PATH), and from the pull request
#          itself only where the payload leaves a size out. A fork's pull request is passed over:
#          its token cannot write a label, and its change reaches the queue only once a maintainer
#          takes the branch into this repository, where it is labelled at the next push.
#
#   hold   large-pr-lanes.yaml's job named ci-ok. It has no needs, so GitHub creates its check run,
#          queued, the moment it creates the run, and that pending ci-ok holds the pull request out
#          of the queue: GitHub reads the newest ci-ok of every workflow and event on the head, this
#          one among them, and a pending required check is one the queue waits for. The verdict
#          itself is ci.yaml's own ci-ok job, called as `<CALLER> / ci-ok`, whose check run GitHub
#          creates only once every job it needs has ended. So this job waits for that job and
#          concludes as it did: success exits 0, anything else exits 1. A run that ends with no
#          verdict of this attempt (a job of it listed, none left open, LARGE_PR_HOLD_ENDED_READS
#          reads in a row) fails too, so a run that never reaches ci.yaml's ci-ok cannot hold the
#          head pending until this job's timeout; a run whose caller has not started yet (it can
#          wait in a concurrency group, as ci.yaml's own) is waited for.
#
# Its inputs, from the step's env:
#   label: GH_TOKEN, and GITHUB_EVENT_PATH and GITHUB_REPOSITORY as Actions sets them.
#   hold:  GH_TOKEN, GITHUB_REPOSITORY, GITHUB_RUN_ID, GITHUB_RUN_ATTEMPT (as Actions sets them),
#          CALLER (the id of large-pr-lanes.yaml's job that calls ci.yaml),
#          LARGE_PR_HOLD_POLL_SECONDS (optional, 60 by default; the tests set 0) and
#          LARGE_PR_HOLD_ENDED_READS (optional, 3 by default).
#   GH_RETRY_SECONDS   optional: gh-read.sh's knob, the tests' way to retry without waiting.
#
# Run from anywhere: large-pr-lanes.sh label|hold. Its tests are in large-pr-lanes.test.sh, which
# ci.yaml's test job runs.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=.github/scripts/gh-read.sh
source "$script_dir/gh-read.sh"

LABEL='large-pr'
LARGE_FILES=100
LARGE_LINES=10000

need() {
  local input
  for input in "$@"; do
    if [ -z "${!input:-}" ]; then
      echo "::error::large-pr-lanes.sh $mode needs $input in its environment"
      exit 1
    fi
  done
}

label() {
  need GH_TOKEN GITHUB_EVENT_PATH GITHUB_REPOSITORY
  local fields number head_repo additions deletions changed has
  if ! fields=$(jq -er --arg label "$LABEL" '.pull_request
      | if type == "object" and (.number | type) == "number" then . else error("no pull request") end
      | [.number, (.head.repo.full_name // "-"), (.additions // "-"), (.deletions // "-"),
         (.changed_files // "-"), ([.labels[]?.name | ascii_downcase] | index($label) != null)]
      | @tsv' "$GITHUB_EVENT_PATH" 2>/dev/null); then
    echo "::error::large-pr-lanes.sh label reads a pull_request event, and $GITHUB_EVENT_PATH carries no pull request"
    exit 1
  fi
  IFS=$'\t' read -r number head_repo additions deletions changed has <<<"$fields"
  if [ "$head_repo" != "$GITHUB_REPOSITORY" ]; then
    echo "#$number comes from $head_repo, a fork, whose token cannot write a label: passed over; it is labelled at its first push once a maintainer takes the branch into $GITHUB_REPOSITORY"
    return 0
  fi
  if [ "$additions" = - ] || [ "$deletions" = - ] || [ "$changed" = - ]; then
    if ! fields=$(gh_read gh api "repos/$GITHUB_REPOSITORY/pulls/$number" \
      --jq '[.additions, .deletions, .changed_files] | @tsv'); then
      echo "::error::the event left out the size of #$number and GitHub refused the pull request at every attempt gh-read.sh made, so this step cannot tell whether an approval of it must run the slow suites"
      exit 1
    fi
    IFS=$'\t' read -r additions deletions changed <<<"$fields"
  fi
  local value
  for value in "$additions" "$deletions" "$changed"; do
    if ! [[ $value =~ ^[0-9]+$ ]]; then
      echo "::error::the size of #$number reads '$additions' additions, '$deletions' deletions and '$changed' changed files, which are not all whole numbers"
      exit 1
    fi
  done
  local lines=$((additions + deletions)) large=false
  if [ "$changed" -ge "$LARGE_FILES" ] || [ "$lines" -ge "$LARGE_LINES" ]; then
    large=true
  fi
  local size="$changed changed files and $lines changed lines (large is $LARGE_FILES files or $LARGE_LINES lines)"
  case $large/$has in
    true/true) echo "#$number is large, $size, and carries $LABEL already" ;;
    false/false) echo "#$number is not large, $size, and carries no $LABEL" ;;
    true/false)
      # A large pull request without the label gets no approval-time run, so a write GitHub
      # refused at every attempt fails the step: the rule would otherwise lapse unseen.
      if ! gh_read gh api --method POST "repos/$GITHUB_REPOSITORY/issues/$number/labels" \
        -f "labels[]=$LABEL" --silent; then
        echo "::error::GitHub refused the label $LABEL on #$number at every attempt gh-read.sh made, so an approval of it would run no slow suite before the merge queue; it has $size"
        exit 1
      fi
      echo "#$number is large, $size: labelled $LABEL, so an approval runs the slow suites its paths select"
      ;;
    false/true)
      # A label left on costs one approval-time run and blocks nothing, so a refusal only warns.
      if ! gh_read gh api --method DELETE "repos/$GITHUB_REPOSITORY/issues/$number/labels/$LABEL" \
        --silent; then
        echo "::warning::GitHub refused to take $LABEL off #$number at every attempt gh-read.sh made, so its next approval runs the slow suites although it has $size"
        return 0
      fi
      echo "#$number is not large, $size: took $LABEL off"
      ;;
  esac
}

# hold: waits for `<CALLER> / ci-ok` and concludes as it did. Each read lists the newest
# execution of every job of this run (`filter=latest`). A verdict of this attempt decides. One of
# an earlier attempt decides only when no other job ran in this attempt (a re-run of this job
# alone): when other jobs re-ran, ci.yaml's ci-ok re-runs after them, and until GitHub creates it
# the newest one listed is the earlier attempt's.
hold() {
  need GH_TOKEN GITHUB_REPOSITORY GITHUB_RUN_ID GITHUB_RUN_ATTEMPT CALLER
  local poll=${LARGE_PR_HOLD_POLL_SECONDS:-60} ended_reads=${LARGE_PR_HOLD_ENDED_READS:-3}
  local want="$CALLER / ci-ok" ended=0 jobs verdict others listed open
  local route="repos/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID/jobs?filter=latest&per_page=100"
  echo "waiting for $want, the verdict of this approval-time run (attempt $GITHUB_RUN_ATTEMPT)"
  while true; do
    if jobs=$(gh_read gh api --paginate "$route" \
      --jq '.jobs[] | [.name, .status, (.conclusion // "-"), (.run_attempt | tostring), .html_url] | @tsv'); then
      verdict=$(awk -F'\t' -v want="$want" '$1 == want' <<<"$jobs")
      others=$(awk -F'\t' -v attempt="$GITHUB_RUN_ATTEMPT" -v want="$want" \
        '$1 != "ci-ok" && $1 != want && $4 == attempt' <<<"$jobs" | wc -l)
      listed=$(awk -F'\t' -v want="$want" '$1 != "ci-ok" && $1 != want' <<<"$jobs" | wc -l)
      open=$(awk -F'\t' -v want="$want" '$1 != "ci-ok" && $1 != want && $2 != "completed"' <<<"$jobs" | wc -l)
      if [ -n "$verdict" ]; then
        local status conclusion attempt url
        IFS=$'\t' read -r _ status conclusion attempt url <<<"$verdict"
        if [ "$status" = completed ] && { [ "$attempt" = "$GITHUB_RUN_ATTEMPT" ] || [ "$others" -eq 0 ]; }; then
          echo "$want concluded $conclusion on attempt $attempt: $url"
          if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
            printf '%s\n' "This ci-ok is the verdict of the approval-time run of a large pull request: [$want]($url) concluded $conclusion. That run ran every fast job and every slow suite this change's paths select (.github/ci-notes/ci.md#a-large-pull-request-runs-its-slow-suites-once-at-the-approved-head)." \
              >>"$GITHUB_STEP_SUMMARY"
          fi
          [ "$conclusion" = success ] && return 0
          echo "::error::$want concluded $conclusion, so this ci-ok fails: that job's own log names the jobs that failed"
          return 1
        fi
      fi
      # No verdict of this attempt yet. GitHub creates ci.yaml's ci-ok only once its needs have
      # ended, so a moment with every other job completed and no verdict is normal; the same
      # reading several reads in a row is a run that will not reach one. A run with no job of
      # CALLER listed yet has not started it: a caller can wait in a concurrency group (ci.yaml's
      # own, or one a later change adds), and this job waits with it.
      if [ "$listed" -gt 0 ] && [ "$open" -eq 0 ]; then
        ended=$((ended + 1))
        if [ "$ended" -ge "$ended_reads" ]; then
          echo "::error::no job of this approval-time run is open and $want has not concluded on attempt $GITHUB_RUN_ATTEMPT, $ended reads in a row, so this run reaches no verdict and this ci-ok fails"
          return 1
        fi
      else
        ended=0
      fi
    else
      echo "::warning::GitHub refused this run's jobs at every attempt gh-read.sh made; reading again in $poll s"
    fi
    sleep "$poll"
  done
}

mode=${1:-}
case $mode in
  label) label ;;
  hold) hold ;;
  *)
    echo "::error::usage: large-pr-lanes.sh label|hold"
    exit 2
    ;;
esac
