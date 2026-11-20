#!/usr/bin/env bash
# The files a pull request or a merge group changes, read once with a counted retry, and handed to
# ci.yaml's `changes` job's three dorny/paths-filter steps as a git repository of their own.
#
# Those steps used to read the list themselves, and paths-filter reads it once with no retry: on a
# pull request it lists the files from the GitHub API, one read per step, and one answer of
# "Server Error: Sorry, this diff is temporarily unavailable due to heavy server load." failed the
# third step and with it the `changes` job, which every job of the run waits on (run 44595619799,
# attempt 1, of pull request #4449; the re-run passed). On a merge group it fetches the group's
# base commit from origin, once, with no retry either. Both reads are GitHub's to fail now and
# then, so here each read goes through gh-read.sh's retry ladder: three attempts, 10 s and then
# 20 s apart, each attempt bounded by `timeout` so that a read that hangs counts as a refused
# attempt instead of holding the job until its own timeout. After the third the script fails with
# an ::error:: line that names the read and the attempts.
#
# The budget is the most the script may take, and it divides it: at most two reads (a merge
# group's base and head, where the checkout lacks them; a pull request makes one), three attempts
# each and 10 s + 20 s of waiting each, so each attempt is bounded at
# (budget - 2 x 30 s) / 6 - 10 s, since `timeout` sends TERM at the bound and KILL 10 s later. The
# worst case - every attempt running to its bound and ignoring TERM - is never more than the
# budget, so the named error prints inside it. .github/scripts/check-retry-budgets.sh, in ci.yaml's
# lint job, refuses a budget above half the job's timeout-minutes. The changes job passes 300 s of
# its 10 minutes: each attempt is bounded at 30 s, and the API's largest list, 30 pages, takes
# 11 s at the 0.36 s one page took in run 44595619799.
#
#   pull_request  GET repos/<repo>/pulls/<number>/files, every page, the read paths-filter v4.0.3
#                 makes (src/main.ts, getChangedFilesFromApi). A renamed file counts under both its
#                 names, as paths-filter counts it. The API lists at most 3,000 files, for
#                 paths-filter as for this script.
#   merge_group   `git diff --no-renames --name-status -z <base_sha>..<head_sha>` in the checkout,
#                 the diff paths-filter runs for a merge group (src/git.ts, getChanges), after
#                 fetching either commit the checkout lacks, as paths-filter's ensureRefAvailable
#                 fetches it: `git fetch --depth=1 --no-tags origin <sha>`.
#
# The hand-over: paths-filter takes no list of files, but with `base: HEAD` it reads the changes of
# the repository it runs in, `git diff --no-renames --name-status -z HEAD` (src/git.ts,
# getChangesOnHead), whatever the event. So the script writes a repository at <directory> whose
# HEAD holds every path the change removed or modified, as a file reading `before`, and whose index
# and work tree hold every path the change added or modified, as a file reading `after`. That diff
# then names every path of the change, and no other: a path is in it exactly when some line of the
# change names it, since the two contents always differ. The filters match on paths alone
# (ci.yaml's filters carry no added/modified/deleted form), so each verdict is the one paths-filter
# reached from its own read. The index is written with `git update-index --index-info`, every
# removal handed to it before any addition, so a path that turned from a file into a directory, or
# back, is no conflict.
#
# Usage: changed-files.sh --budget <seconds> <directory>
# Its inputs, from Actions' own environment and the step's env:
#   GITHUB_EVENT_NAME  pull_request or merge_group; any other event is refused.
#   GITHUB_EVENT_PATH  the payload: .pull_request.number, or .merge_group.base_sha and head_sha.
#   GITHUB_REPOSITORY  owner/repo. pull_request only.
#   GH_TOKEN           the job's github.token. pull_request only.
#   GH_RETRY_SECONDS   gh-read.sh's backoff unit, 10 by default; the tests set 0. Its waits are
#                      taken out of the budget like every other second.
# A merge group runs it at the root of a checkout of the group's head.
#
# Its tests are in changed-files.test.sh, which ci.yaml's test job runs.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=.github/scripts/gh-read.sh
source "$script_dir/gh-read.sh"

readonly READS=2 ATTEMPTS=3 KILL_SECONDS=10
if [ "${1:-}" != --budget ] || ! [[ ${2:-} =~ ^[0-9]+$ ]] || [ -z "${3:-}" ]; then
  echo "::error::usage: changed-files.sh --budget <seconds> <directory>; the budget is the most the reads may take, which .github/scripts/check-retry-budgets.sh holds to half the job's timeout-minutes" >&2
  exit 2
fi
budget=$2
dest=$3
event=${GITHUB_EVENT_PATH:?GITHUB_EVENT_PATH must name the event payload}
event_name=${GITHUB_EVENT_NAME:?GITHUB_EVENT_NAME must name the event}

retry_seconds=${GH_RETRY_SECONDS:-10}
attempt_seconds=$(((budget - READS * 3 * retry_seconds) / (READS * ATTEMPTS) - KILL_SECONDS))
if [ "$attempt_seconds" -lt 1 ]; then
  echo "::error::changed-files.sh's budget of $budget s leaves no second for an attempt: $READS reads of $ATTEMPTS attempts, each with $KILL_SECONDS s between TERM and KILL, and $((3 * retry_seconds)) s of waiting a read" >&2
  exit 2
fi
ladder="3 attempts, $retry_seconds s and $((2 * retry_seconds)) s apart, each bounded at $attempt_seconds s within the step's $budget s budget"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
# One record per path line of the change: its status, A, D or M (any other status git prints, a
# type change, counts as M), then the path, each ending in a NUL.
records="$work/records"

case $event_name in
  pull_request)
    number=$(jq -r '.pull_request.number' "$event")
    repository=${GITHUB_REPOSITORY:?GITHUB_REPOSITORY must name the repository as owner/repo}
    route="repos/$repository/pulls/$number/files"
    if ! gh_read timeout -k "$KILL_SECONDS" "$attempt_seconds" gh api --paginate "$route?per_page=100" > "$work/files.json"; then
      echo "::error::the changes job could not read the files of pull request #$number: GET $route failed $ladder (the log above holds each attempt's answer)" >&2
      exit 1
    fi
    # --paginate prints one array per page, back to back; jq reads each as an input of its own.
    jq -j '.[]
      | if .status == "renamed" then ["D", .previous_filename], ["A", .filename]
        elif .status == "removed" then ["D", .filename]
        elif .status == "added" or .status == "copied" then ["A", .filename]
        else ["M", .filename] end
      | .[0] + "\u0000" + .[1] + "\u0000"' "$work/files.json" > "$records"
    source_line="pull request #$number, read from GET $route"
    ;;
  merge_group)
    base=$(jq -r '.merge_group.base_sha' "$event")
    head=$(jq -r '.merge_group.head_sha' "$event")
    for sha in "$base" "$head"; do
      git cat-file -e "$sha^{commit}" 2> /dev/null && continue
      if ! gh_read timeout -k "$KILL_SECONDS" "$attempt_seconds" git fetch --depth=1 --no-tags origin "$sha" >&2; then
        echo "::error::the changes job could not fetch commit $sha of the merge group from origin: git fetch failed $ladder (the log above holds each attempt's answer)" >&2
        exit 1
      fi
    done
    git diff --no-renames --name-status -z "$base..$head" > "$records"
    source_line="merge group $base..$head, read with git diff"
    ;;
  *)
    echo "::error::changed-files.sh lists the files of a pull_request or a merge_group event, not of $event_name" >&2
    exit 1
    ;;
esac

rm -rf "$dest"
git init -q --initial-branch=change "$dest"
before=$(printf 'before\n' | git -C "$dest" hash-object -w --stdin)
after=$(printf 'after\n' | git -C "$dest" hash-object -w --stdin)
absent=0000000000000000000000000000000000000000
removed="$work/removed"
present="$work/present"
: > "$removed"
: > "$present"
paths=0
while IFS= read -r -d '' status && IFS= read -r -d '' path; do
  paths=$((paths + 1))
  [ "$status" = A ] || printf '100644 %s\t%s\0' "$before" "$path" >> "$removed"
  [ "$status" = D ] || printf '100644 %s\t%s\0' "$after" "$path" >> "$present"
done < "$records"

# HEAD: what the change found.
git -C "$dest" update-index -z --index-info < "$removed"
git -C "$dest" -c user.name=changes -c user.email=changes@localhost -c commit.gpgsign=false \
  commit -q --allow-empty --no-verify -m before
# The index and the work tree: what the change left. Every path of HEAD leaves the index first.
{
  git -C "$dest" ls-files -z | while IFS= read -r -d '' path; do printf '0 %s\t%s\0' "$absent" "$path"; done
  cat "$present"
} | git -C "$dest" update-index -z --index-info
git -C "$dest" checkout-index --all --force

echo "$paths path(s) changed: $source_line"
