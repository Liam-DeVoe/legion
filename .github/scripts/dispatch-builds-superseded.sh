#!/usr/bin/env bash
# Deletes the entries of one Dispatch build's cache family that this repository keeps no more:
# main's entries older than the one ci-caches.yaml's dispatch-builds job just saved, and every
# entry of the family on any other ref. The comment above ci-caches.yaml's
# dispatch-builds-superseded job says why each is safe to delete.
#
# Its inputs, from the step's env and the runner's:
#   KEEP      the key of the entry to keep, which must have the shape dispatch-builds gives it:
#             dispatch-<build>-build-<OS>-<architecture>-<the script's 64-hex hash>-<the 40-hex
#             commit>. Any other shape deletes nothing and exits 1.
#   REF       the ref whose entry is kept (refs/heads/main).
#   GH_TOKEN  a token holding `actions: write`, and GH_REPO the repository, both for gh.
#   JOB_TIMEOUT_MINUTES  the job's limit, within which .github/scripts/gh-read.sh waits out a
#             spent API budget.
#
# gh's own lines, GitHub's error replies among them, reach the log only inside a
# .github/scripts/ci-log-guard.sh block, where they cannot become workflow commands; this script's
# own workflow commands go to stderr, where they stay in order with the blocks.
#
# Run from the repository root: .github/scripts/dispatch-builds-superseded.sh
# CI runs it in ci-caches.yaml's dispatch-builds-superseded job, once per build.
set -euo pipefail

# Each call waits out a spent API budget within the job's limit, and fails by name past it.
# shellcheck source=.github/scripts/gh-read.sh
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/gh-read.sh"
# shellcheck source=.github/scripts/ci-log-guard.sh
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ci-log-guard.sh"

if ! [[ ${KEEP:-} =~ ^dispatch-(previous|first-rollout)-build-[A-Za-z0-9]+-[A-Za-z0-9]+-[0-9a-f]{64}-[0-9a-f]{40}$ ]]; then
  echo "::error::the kept key does not have the shape dispatch-builds gives it, so nothing is deleted" >&2
  exit 1
fi
ref=${REF:?REF must name the ref whose entry is kept}
# The build, OS and architecture: the key without its script hash and commit.
prefix=${KEEP%-*}
prefix=${prefix%-*}-
# Every ref's entries, newest first by GitHub's creation order; gh pages past 100 up to the limit,
# far above what one build holds.
entries=$(ci_log_guard_stderr gh_wait_reset gh cache list --key "$prefix" --sort created_at --order desc --limit 1000 --json id,key,ref)
if ! jq -e --arg keep "$KEEP" --arg ref "$ref" 'any(.[]; .key == $keep and .ref == $ref)' <<<"$entries" >/dev/null; then
  echo "::warning::main holds no entry under $KEEP, so none is deleted" >&2
  exit 0
fi
jq -r --arg keep "$KEEP" --arg ref "$ref" --arg prefix "$prefix" \
  '(map(.key == $keep and .ref == $ref) | index(true)) as $kept
   | to_entries[] | .key as $at | .value
   | select((.key | startswith($prefix)) and (.ref != $ref or $at > $kept)) | .id' <<<"$entries" |
  while read -r id; do
    if ! [[ $id =~ ^[0-9]+$ ]]; then
      echo "::error::the cache list gave an id that is not a number, so nothing more is deleted" >&2
      exit 1
    fi
    echo "Deleting entry $id of $prefix, which this repository keeps no more" >&2
    ci_log_guard gh_wait_reset gh cache delete "$id"
  done
