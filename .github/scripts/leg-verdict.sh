#!/usr/bin/env bash
# .github/scripts/leg-verdict.sh
#
# The verdict of a job of ci.yaml that carries the old check-run name of a job split into legs
# (`envoy-go / envoy-go`, `daemon-go / daemon-go`, ...): it passes only when each of its own legs
# passed, whatever the other legs of the job that holds them did.
#
#   leg-verdict.sh LEG...
#
# Each LEG is the check-run name one of its legs carries, as that leg's `name:` gives it and
# ci.yaml's CI_OK_CHECK_RUN_JOBS lists it (`envoy-go (1/3)`). It reads the jobs of this run's
# attempt,
#
#   gh api --paginate "repos/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID/attempts/$GITHUB_RUN_ATTEMPT/jobs?per_page=50" \
#     --jq '.jobs[] | {id, name, run_attempt, status, conclusion}'
#
# one JSON object a line, through gh-read.sh's retry ladder. GitHub lists every job of the run in
# an attempt's list, each job that attempt did not run again copied in with the result it had and
# the attempt's number (testdata/leg-verdict holds such a list, of an attempt that re-ran one job),
# so the list is the newest record of each job: a re-run of one leg leaves the other legs' records
# standing and replaces the re-run one's. Where a name is listed more than once, the record with the
# highest run_attempt, then the highest id, is the leg's. 50 a page, not 100: GitHub answered every
# read of a re-run attempt's 86 jobs with 100 a page with an HTTP 502, and every read with 50 with
# the list (2026-11-23).
#
# It prints one line a leg, `PASS <leg>: success` or `FAIL <leg>: <why>`, and an `::error::` for
# each FAIL. It exits 0 when every leg's newest record is completed with the conclusion success;
# 1 when a leg has no record (a leg a run skipped whole carries its `name:` expression's text, not
# the name it would evaluate to), has not completed, or concluded anything else, skipped
# included; and 2 on a usage error or a list GitHub refused three times, which decides nothing.
#
# Run from anywhere, with GH_TOKEN (actions: read), GITHUB_REPOSITORY, GITHUB_RUN_ID and
# GITHUB_RUN_ATTEMPT: .github/scripts/leg-verdict.sh LEG...
# ci.yaml's carrying jobs run it; its tests are in leg-verdict.test.sh.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=.github/scripts/gh-read.sh
source "$script_dir/gh-read.sh"

usage() {
  echo "::error::leg-verdict.sh: $1 (usage: leg-verdict.sh LEG...)"
  exit 2
}

[ $# -gt 0 ] || usage "no leg is named"
for leg in "$@"; do
  [ -n "$leg" ] || usage "a leg's name is empty"
done

for input in GITHUB_REPOSITORY GITHUB_RUN_ID GITHUB_RUN_ATTEMPT; do
  [ -n "${!input:-}" ] || usage "$input is not set"
done
if ! jobs=$(gh_read gh api --paginate \
  "repos/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID/attempts/$GITHUB_RUN_ATTEMPT/jobs?per_page=50" \
  --jq '.jobs[] | {id, name, run_attempt, status, conclusion}'); then
  echo "::error::GitHub refused the jobs of run $GITHUB_RUN_ID, attempt $GITHUB_RUN_ATTEMPT, three times, so no leg's result is known"
  exit 2
fi
if ! jobs=$(jq -s 'map(select(type == "object" and (.name | type) == "string"))' <<<"$jobs"); then
  echo "::error::the jobs list is not one JSON object a line"
  exit 2
fi

status=0
for leg in "$@"; do
  line=$(jq -r --arg leg "$leg" '
    [.[] | select(.name == $leg)] | sort_by(.run_attempt, .id) | last
    | if . == null then "FAIL no job of this run attempt carries the name"
      elif .status != "completed" then "FAIL its job \(.id) of attempt \(.run_attempt) is \(.status)"
      elif .conclusion != "success" then "FAIL its job \(.id) of attempt \(.run_attempt) concluded \(.conclusion)"
      else "PASS \(.conclusion)" end' <<<"$jobs")
  verdict=${line%% *}
  echo "$verdict $leg: ${line#* }"
  if [ "$verdict" != PASS ]; then
    echo "::error::the leg $leg did not pass: ${line#* }"
    status=1
  fi
done
exit "$status"
