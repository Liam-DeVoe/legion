#!/usr/bin/env bash
# The unnamed-file step of ci.yaml's `changes` job, beside its dorny/paths-filter step. It reads
# every file the run changes, prints how many it read, and matches each against every filter of
# the file the paths-filter step reads (.github/ci-areas.yaml). A changed file that no filter names
# selects every job: the step writes `found=true` to $GITHUB_OUTPUT. On a pull request each job's
# output of `changes` is true when its filter matches or `found` is, so a new file or directory
# runs every job its holds allow. A merge group selects every job anyway; there `found` tells
# ci-ok whether the pull request ran every job, so its summary names a failed job the filters do
# not select only where no changed file selected every job (ci-ok.jq, missed_by_filters).
#
#   unnamed-files.sh <filters file>
#
# Where it reads the changed files. On a pull request (PR_NUMBER), every page of GitHub's list of
# the pull request's files. GitHub lists at most 3,000 files of a pull request ("List pull
# requests files" in its REST reference), so a list of 3,000 is cut off and a file beyond it may be
# one no filter names: such a list selects every job. On a merge group (BASE_SHA, the group's base),
# `git diff --name-only --no-renames` between that base, fetched when the checkout lacks it, and
# the group's head checked out in the working directory, which has no such limit. A renamed file
# changes two paths, the new and the old, as dorny/paths-filter reads it (src/main.ts,
# getChangedFilesFromApi), so both are matched; the count is of the list's entries, which
# GitHub's `changed_files` counts too. A list GitHub refuses three times (gh-read.sh), or a base
# git cannot fetch, is no list, and selects every job, with the reason in the log.
#
# A file a filter's `!` pattern drops counts as named by that filter: that pattern says the job
# reads none of those files, so a change to one alone selects no job beyond those its other
# filters select. Each pattern is matched with its `!` taken off, by workflow_paths.py's model of
# the action's matcher (glob_regex), after the filters are read as the action reads them: js-yaml
# refuses a repeated key (UniqueKeyLoader), and an anchor's list, a list of lists and a change
# type's mapping are flattened into the filter that names them. A pattern holding a brace, which
# picomatch expands and glob_regex reads literally, fails the step, as an unread filter would.
#
# Its inputs, from the step's env: GITHUB_OUTPUT; on a pull request PR_NUMBER
# (github.event.pull_request.number), GH_TOKEN (a token that reads the pull request) and
# GITHUB_REPOSITORY; on a merge group BASE_SHA (github.event.merge_group.base_sha). GH_RETRY_SECONDS
# is gh-read.sh's knob. It exits 1 on a missing input or a filters file it cannot read, and 0
# otherwise.
#
# Run from anywhere: PR_NUMBER=... .github/scripts/unnamed-files.sh .github/ci-areas.yaml
# Its tests are in unnamed-files.test.sh.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=.github/scripts/gh-read.sh
source "$script_dir/gh-read.sh"

if [ $# -ne 1 ]; then
  echo "::error::unnamed-files.sh takes one argument, the filters file the paths-filter step reads (usage: unnamed-files.sh .github/ci-areas.yaml)"
  exit 1
fi
filters=$1
require() {
  local input
  for input in "$@"; do
    if [ -z "${!input:-}" ]; then
      echo "::error::unnamed-files.sh needs $input in its environment"
      exit 1
    fi
  done
}
require GITHUB_OUTPUT

# GitHub's most files in a pull request's list; 0 for a list with no such bound.
limit=0
if [ -n "${PR_NUMBER:-}" ]; then
  require GH_TOKEN GITHUB_REPOSITORY
  limit=3000
  source_text="of pull request #$PR_NUMBER from every page of GitHub's list"
  if ! listed=$(gh_read gh api --paginate "repos/$GITHUB_REPOSITORY/pulls/$PR_NUMBER/files?per_page=100" \
    --jq '.[] | {filename, previous_filename}'); then
    echo "::warning::GitHub refused the changed files of pull request #$PR_NUMBER three times, so this step cannot tell whether one of them is a file no filter names; it selects every job"
    echo "found=true" >>"$GITHUB_OUTPUT"
    exit 0
  fi
elif [ -n "${BASE_SHA:-}" ]; then
  source_text="of this merge group against its base $BASE_SHA, from git diff"
  if ! git cat-file -e "$BASE_SHA^{commit}" 2>/dev/null \
    && ! git fetch --no-tags --quiet --depth=1 origin "$BASE_SHA"; then
    echo "::warning::git could not fetch the merge group's base $BASE_SHA, so this step cannot tell whether a changed file is one no filter names; it selects every job"
    echo "found=true" >>"$GITHUB_OUTPUT"
    exit 0
  fi
  listed=$(git diff --name-only --no-renames -z "$BASE_SHA" HEAD |
    jq -R -s -c 'split("\u0000")[] | select(. != "") | {filename: ., previous_filename: null}')
else
  echo "::error::unnamed-files.sh needs PR_NUMBER (a pull request) or BASE_SHA (a merge group) in its environment"
  exit 1
fi

# The program reads the list on its standard input, one `{filename, previous_filename}` per line
# as gh's --jq prints it; a limit of 0 is a list with no bound.
read -r -d '' program <<'PY' || true
import json
import sys

import yaml
from workflow_paths import UniqueKeyLoader, glob_regex, unread_filter_patterns

path, limit, source, output = sys.argv[1], int(sys.argv[2]), sys.argv[3], sys.argv[4]


def fail(message):
    print(f"::error file={path}::{message}")
    sys.exit(1)


def flatten(value):
    """A filter's patterns as dorny/paths-filter reads them: an anchor's list, a list of lists and
    a change type's mapping each flattened into the filter that names them."""
    if isinstance(value, str):
        return [value]
    if isinstance(value, list):
        return [pattern for item in value for pattern in flatten(item)]
    if isinstance(value, dict):
        return [pattern for item in value.values() for pattern in flatten(item)]
    return []


try:
    with open(path) as handle:
        loaded = yaml.load(handle, Loader=UniqueKeyLoader)
except (OSError, yaml.YAMLError) as error:
    fail(f"{path} cannot be read as the paths-filter step's filters: {' '.join(str(error).split())}")
if not isinstance(loaded, dict) or not loaded:
    fail(f"{path} holds no mapping of filter name to patterns, so no changed file can be named")
patterns = sorted({pattern.removeprefix("!") for value in loaded.values() for pattern in flatten(value)})
if unread_filter_patterns(patterns):
    fail(f"{path} has {unread_filter_patterns(patterns)[0]}, a brace picomatch expands and this step reads "
         f"as literal characters, so it cannot say which files the filter names")
naming = [glob_regex(pattern) for pattern in patterns]


def named(changed):
    """Whether a pattern of some filter, its `!` taken off, matches the changed file."""
    return any(regex.fullmatch(changed) for regex in naming)


entries = [json.loads(line) for line in sys.stdin.read().splitlines() if line.strip()]
paths = [p for entry in entries for p in (entry.get("filename"), entry.get("previous_filename")) if p]
print(f"read {len(entries)} changed file(s) {source}")
unnamed = [p for p in dict.fromkeys(paths) if not named(p)]
if limit and len(entries) >= limit:
    print(f"GitHub lists at most {limit} files of a pull request, so this list of {len(entries)} is cut off and "
          f"a file it leaves out may be one no filter names: every job is selected")
    found = True
elif unnamed:
    shown = ", ".join(unnamed[:20]) + (f" and {len(unnamed) - 20} more" if len(unnamed) > 20 else "")
    print(f"{len(unnamed)} changed file(s) match no filter of {path}, so every job is selected: {shown}")
    found = True
else:
    print(f"every changed file is named by a filter of {path} (a file a `!` pattern drops counts as named)")
    found = False
with open(output, "a") as handle:
    handle.write(f"found={'true' if found else 'false'}\n")
PY
PYTHONDONTWRITEBYTECODE=1 PYTHONPATH="$script_dir" python3 -c "$program" "$filters" "$limit" "$source_text" \
  "$GITHUB_OUTPUT" <<<"$listed"
