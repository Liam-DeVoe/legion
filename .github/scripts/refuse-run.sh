#!/usr/bin/env bash
# The refusals of ci.yaml's pr-title and ci-ok jobs, the two checks main's ruleset requires: a
# step of each runs this under an `if:` that names its case, and fails with the reason it prints.
#
#   refuse-run.sh pr-title-attempt | ci-ok-draft | ci-ok-attempt
#
# pr-title-attempt and ci-ok-attempt refuse attempt GITHUB_RUN_ATTEMPT of a merge-group run, and
# ci-ok-draft a draft pull request's run; the notes section each step's `# why:` line names says
# why the refusal is a step and not a term of the job's `if:`. It prints one ::error:: line and
# exits 1, or exits 2 on a case it does not know, which fails the step as well.
set -euo pipefail

case "${1:-}" in
  pr-title-attempt)
    why="pr-title is failure on attempt ${GITHUB_RUN_ATTEMPT:-} of this merge-group run, whatever the pull request's text says: a merge group is tested once, the queue builds a new group rather than re-running one, and this attempt checks a tree the queue is no longer waiting on." ;;
  ci-ok-draft)
    why="ci-ok is failure because this pull request is a draft: every other job of this run skipped, so nothing in it has been tested. Mark the pull request ready for review: that starts a run which selects by the same diff and concludes a real verdict over it." ;;
  ci-ok-attempt)
    why="ci-ok is failure on attempt ${GITHUB_RUN_ATTEMPT:-} of this merge-group run, whatever the jobs of it did: a merge group is tested once, the queue builds a new group rather than re-running one, and this attempt judges a tree the queue is no longer waiting on." ;;
  *)
    echo "::error::refuse-run.sh: unknown case '${1:-}' (usage: refuse-run.sh pr-title-attempt|ci-ok-draft|ci-ok-attempt)"
    exit 2 ;;
esac
echo "::error::$why"
exit 1
