#!/usr/bin/env bash
# The merge-group runs the queue has replaced, force-cancelled: this reads the run list of
# ci.yaml's merge_group runs, hands it to .github/scripts/merge-group-cancels.sh, which decides
# and prints one `cancel <run id>` line per run in the order to act on them, and makes one
# force-cancel call per line. A call that fails leaves a warning and not a red job: the run it
# names may have completed between the list and the call.
#
# Who runs it: the one step of .github/workflows/ci.yaml's cancel-started job, whose comment says
# what that act is for and the rules the job follows. It takes no argument and reads three
# variables that step's own `env:` map sets: GH_TOKEN, the run's own token, for `gh`; and
# HEAD_BRANCH and RUN_ID, this run's queue branch and id, which merge-group-cancels.sh reads
# beside the RUNS this script exports. That job checks out with `sparse-checkout: .github`, so
# this script and the one it runs are both in the checkout when the step runs.
#
# Run from the repository root: .github/scripts/cancel-started-act.sh
# CI runs it in ci.yaml's cancel-started job, at its one step.
set -euo pipefail

# The `--limit` is 100 because the queue builds up to 20 groups at once and this pull
# request's own groups are among them: gh's default of 20 runs could leave an older
# group of it out of the list, and so out of the act.
RUNS=$(gh run list --repo "$GITHUB_REPOSITORY" --workflow ci.yaml --event merge_group \
  --limit 100 --json databaseId,headBranch,status,createdAt)
export RUNS
acts=$(.github/scripts/merge-group-cancels.sh)
while read -r act id; do
  [ "$act" = cancel ] || continue
  gh api --silent --method POST "repos/$GITHUB_REPOSITORY/actions/runs/$id/force-cancel" ||
    echo "::warning::force-cancel of run $id failed; it may have completed already"
done <<<"$acts"
