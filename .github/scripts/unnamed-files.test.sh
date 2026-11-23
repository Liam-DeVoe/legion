#!/usr/bin/env bash
# Tests for `unnamed-files.sh`, the step of ci.yaml's `changes` job that selects every job when a
# pull request changes a file no filter names. Each case writes the pull request's changed files
# as gh prints them for the script's read, one `{filename, previous_filename}` per line, and runs
# the real script with a stand-in `gh` first on PATH that answers the read with every page under
# --paginate and with the first page of 100 without it, as GitHub does. Its fixtures: #4397's
# list of 686 files over seven pages (testdata/unnamed-files, whose README holds the command that
# captured it), a 3,000-file list made from that one's files under packages/, the real
# .github/ci-areas.yaml, and small filters files written here. On a merge group the script reads
# git's diff of the group's head against its base instead, which a scratch repository of three
# commits gives it.
#
# Beside the cases that decide the selection, a copy of the script with that part changed runs the
# same case and must fail it: one that reads one page of the list, one that takes a list of 3,000
# files as complete, and one that reads a `!` pattern as dorny/paths-filter's exclusion rather
# than as naming the files it drops.
#
# Needs git, jq and python3 with PyYAML. Run from anywhere: .github/scripts/unnamed-files.test.sh
# CI runs it in ci.yaml's test job.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
captured="$script_dir/testdata/unnamed-files/pull-4397-files.jsonl"
areas="$script_dir/../ci-areas.yaml"

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

for input in "$captured" "$areas"; do
  if [ ! -f "$input" ]; then
    check "the fixture $input is there" false
    summary "unnamed-files.sh"
  fi
done

# The stand-in GitHub: the read the script makes, exactly, answered from $work/files; every other
# call, and the same read with other fields or no per_page, fails, so the fixture stays what gh
# prints for the script's own command. $work/refuse makes it refuse the read.
mkdir "$work/bin"
cat >"$work/bin/gh" <<GH
#!/usr/bin/env bash
set -euo pipefail
if [ -e "$work/refuse" ]; then echo "HTTP 502" >&2; exit 1; fi
case "\$*" in
  "api --paginate repos/o/r/pulls/7/files?per_page=100 --jq .[] | {filename, previous_filename}")
    cat "$work/files" ;;
  "api repos/o/r/pulls/7/files?per_page=100 --jq .[] | {filename, previous_filename}")
    head -n 100 "$work/files" ;;
  *) echo "unexpected gh \$*" >&2; exit 2 ;;
esac
GH
chmod +x "$work/bin/gh"

# files <path>...: the pull request's changed files, none of them renamed.
files() {
  rm -f "$work/refuse"
  printf '%s\n' "$@" | jq -R -c '{filename: ., previous_filename: null}' >"$work/files"
}

# run_unnamed <filters file> [<script>]: runs the script (by default the real one) over $work/files
# and leaves its output in $out, its status in $status and the found it wrote in $found.
run_unnamed() {
  local script=${2:-$script_dir/unnamed-files.sh}
  : >"$work/output"
  status=0
  out=$(PATH="$work/bin:$PATH" GH_TOKEN=t GITHUB_REPOSITORY=o/r PR_NUMBER=7 GH_RETRY_SECONDS=0 \
    GITHUB_OUTPUT="$work/output" "$script" "$1" 2>&1) || status=$?
  found=$(sed -n 's/^found=//p' "$work/output")
}

# variant <name> <python or sed program> <what the copy does>: a copy of the script with the sed
# program applied, beside the gh-read.sh and workflow_paths.py it reads. It checks the copy differs
# from the script, so a control whose program no longer matches fails here rather than passing on
# an unchanged copy.
variant() {
  mkdir -p "$work/variants/$1"
  cp "$script_dir/gh-read.sh" "$script_dir/workflow_paths.py" "$work/variants/$1/"
  sed -E "$2" "$script_dir/unnamed-files.sh" >"$work/variants/$1/unnamed-files.sh"
  chmod +x "$work/variants/$1/unnamed-files.sh"
  check "the copy that $3 differs from the script" \
    "$(cmp -s "$work/variants/$1/unnamed-files.sh" "$script_dir/unnamed-files.sh" && echo false || echo true)"
}
variant one-page 's/ --paginate / /' "reads one page of the list"
variant complete 's/^  limit=3000$/  limit=1000000/' "takes a list of 3,000 files as complete"
# The copy that reads the filters as dorny/paths-filter's verdicts: a file is named only where a
# filter matches it with its `!` patterns excluding, as the action decides a job.
variant excludes 's/import UniqueKeyLoader, /import PathsFilter, UniqueKeyLoader, /;
  s/return any\(regex\.fullmatch\(changed\) for regex in naming\)/return any(PathsFilter(flatten(value), "some-with-excludes").covers(changed) for value in loaded.values())/' \
  "reads a ! pattern as an exclusion"

echo "case: #4397's own list, seven pages, against the real filters"
cp "$captured" "$work/files"
run_unnamed "$areas"
check "exits 0" "$(is "$status" 0)"
check "prints how many files it read, every page's" \
  "$(contains "$out" "^read 686 changed file(s) of pull request #7 from every page of GitHub's list\$")"
check "selects every job: it changes files no filter names" "$(is "$found" true)"
check "names the files no filter names" "$(contains "$out" 'match no filter of .*ci-areas.yaml, so every job is selected: ')"
check "among them the old filters file" "$(contains "$out" '[:,] \.github/ci-filters\.yaml,')"
run_unnamed "$areas" "$work/variants/one-page/unnamed-files.sh"
check "the copy that reads one page counts 100 files" \
  "$(contains "$out" '^read 100 changed file(s) of pull request #7')"

echo "case: 101 files under one package directory and a new file at the root that sorts after them"
# c-ci-ok's temporary pull request: the new file is on the list's second page, and no filter names
# it, so every job that runs on a pull request and is not held runs.
mapfile -t package_files < <(for n in $(seq -w 1 101); do echo "packages/daemon/internal/zz/file-$n.go"; done)
files "${package_files[@]}" zz-new.txt
run_unnamed "$areas"
check "exits 0" "$(is "$status" 0)"
check "reads all 102" "$(contains "$out" '^read 102 changed file(s)')"
check "selects every job" "$(is "$found" true)"
check "names the new file" "$(contains "$out" 'so every job is selected: zz-new.txt$')"
run_unnamed "$areas" "$work/variants/one-page/unnamed-files.sh"
check "the copy that reads one page selects no job through this step" "$(is "$found" false)"

echo "case: the same 101 files alone"
files "${package_files[@]}"
run_unnamed "$areas"
check "selects nothing through this step: every file is named" "$(is "$found" false)"
check "says every file is named" "$(contains "$out" '^every changed file is named by a filter of ')"

echo "case: a list of 3,000 files, GitHub's limit, each of which a filter names"
# GitHub lists no more than 3,000 files of a pull request, so this list is cut off and a file past
# it may be one no filter names: every job is selected. The list is #4397's files under packages/,
# each of which a filter names, and new files under packages/daemon up to 3,000.
jq -r 'select(.filename | startswith("packages/")) | .filename' "$captured" >"$work/limit"
n=0
while [ "$(wc -l <"$work/limit")" -lt 3000 ]; do
  n=$((n + 1))
  echo "packages/daemon/internal/zz/limit-$n.go" >>"$work/limit"
done
mapfile -t limit_files <"$work/limit"
files "${limit_files[@]}"
run_unnamed "$areas"
check "exits 0" "$(is "$status" 0)"
check "reads 3,000" "$(contains "$out" '^read 3000 changed file(s)')"
check "selects every job" "$(is "$found" true)"
check "says why" "$(contains "$out" 'GitHub lists at most 3000 files of a pull request, so this list of 3000 is cut off')"
run_unnamed "$areas" "$work/variants/complete/unnamed-files.sh"
check "the copy that takes 3,000 files as complete selects no job through this step" "$(is "$found" false)"
files "${limit_files[@]:1}"
run_unnamed "$areas"
check "2,999 of them are a whole list, which selects nothing through this step" "$(is "$found" false)"

echo "case: a file a ! pattern of the real filters drops, alone"
# text-checks' filter drops docs/plans, which no text check reads, and no other filter names it,
# so the file counts as named and a pull request that changes only it runs only the jobs that run
# on every pull request.
files docs/plans/2026-11-23-a-plan.md
run_unnamed "$areas"
check "exits 0" "$(is "$status" 0)"
check "selects nothing through this step" "$(is "$found" false)"
run_unnamed "$areas" "$work/variants/excludes/unnamed-files.sh"
check "the copy that reads the ! pattern as an exclusion selects every job" "$(is "$found" true)"

echo "case: anchors, a list of lists and a change type, as dorny/paths-filter flattens them"
cat >"$work/nested.yaml" <<'YAML'
first:
  - &one lib/**
  - &group [docs/**, [deep/**]]
second:
  - *group
  - [*one, [other/file.txt]]
  - added|modified: typed/**
third:
  - src/**
  - '!src/gen/**'
YAML
files deep/a.txt other/file.txt typed/b.txt src/gen/c.txt lib/d.txt
run_unnamed "$work/nested.yaml"
check "exits 0" "$(is "$status" 0)"
check "names a file under a nested list, an aliased list and a change type, and one a ! drops" "$(is "$found" false)"
files deep/a.txt elsewhere/e.txt
run_unnamed "$work/nested.yaml"
check "selects every job for a file none of them names" "$(is "$found" true)"
check "and names it" "$(contains "$out" 'so every job is selected: elsewhere/e.txt$')"

echo "case: a renamed file whose old path no filter names"
# dorny/paths-filter reads a rename as the new path added and the old one deleted, so the old path
# is a changed file too.
jq -n -c '{filename: "lib/moved.txt", previous_filename: "elsewhere/old.txt"}' >"$work/files"
run_unnamed "$work/nested.yaml"
check "counts it once" "$(contains "$out" '^read 1 changed file(s)')"
check "selects every job" "$(is "$found" true)"
check "names the old path" "$(contains "$out" 'so every job is selected: elsewhere/old.txt$')"

echo "case: GitHub refuses the list"
files lib/d.txt
touch "$work/refuse"
run_unnamed "$work/nested.yaml"
check "exits 0" "$(is "$status" 0)"
check "selects every job, since a list it could not read may hold a file no filter names" "$(is "$found" true)"
check "warns why" "$(contains "$out" '::warning::GitHub refused the changed files of pull request #7 three times')"

echo "case: a filter with a brace"
printf "first:\n  - '{lib,docs}/**'\n" >"$work/brace.yaml"
files lib/d.txt
run_unnamed "$work/brace.yaml"
check "fails" "$(is "$status" 1)"
check "names the pattern" "$(contains "$out" "has {lib,docs}/\*\*, a brace picomatch expands")"
check "writes no found" "$(is "$found" '')"

echo "case: a filters file that holds one filter twice"
printf "first:\n  - lib/**\nfirst:\n  - docs/**\n" >"$work/repeated.yaml"
run_unnamed "$work/repeated.yaml"
check "fails" "$(is "$status" 1)"
check "names the key" "$(contains "$out" "the mapping key 'first' of line 3 is the key of line 1 again")"

echo "case: a merge group's changed files, read with git against its base"
# A merge group selects every job; there the step tells ci-ok whether a changed file selected
# every job on the pull request too. Its files are git's diff of the group's head against its
# base, a rename as the old path deleted and the new one added.
group="$work/group"
git init -q "$group"
commit() { git -C "$group" add -A && git -C "$group" -c user.name=t -c user.email=t@example.com commit -q -m "$1"; }
mkdir -p "$group/lib" "$group/elsewhere"
echo one >"$group/lib/a.txt"
echo two >"$group/elsewhere/old.txt"
commit base
base=$(git -C "$group" rev-parse HEAD)
echo three >"$group/lib/a.txt"
git -C "$group" mv elsewhere/old.txt lib/moved.txt
commit group
# run_group <base>: the script in the group's checkout, on a merge group with that base.
run_group() {
  : >"$work/output"
  status=0
  out=$(cd "$group" && GITHUB_OUTPUT="$work/output" BASE_SHA=$1 "$script_dir/unnamed-files.sh" "$work/nested.yaml" 2>&1) ||
    status=$?
  found=$(sed -n 's/^found=//p' "$work/output")
}
run_group "$base"
check "exits 0" "$(is "$status" 0)"
check "reads the three paths the group changes" \
  "$(contains "$out" "^read 3 changed file(s) of this merge group against its base $base, from git diff\$")"
check "finds the renamed file's old path, which no filter names" "$(is "$found" true)"
check "and names it" "$(contains "$out" 'so every job is selected: elsewhere/old.txt$')"
echo four >"$group/lib/a.txt"
commit second
run_group "$(git -C "$group" rev-parse HEAD~1)"
check "a group whose files a filter names finds nothing" "$(is "$found" false)"
run_group 0123456789abcdef0123456789abcdef01234567
check "a base git cannot fetch exits 0" "$(is "$status" 0)"
check "and selects every job" "$(is "$found" true)"
check "and warns why" "$(contains "$out" '::warning::git could not fetch the merge group.s base 0123456789abcdef0123456789abcdef01234567')"

echo "case: neither a pull request number nor a merge group's base"
set +e
out=$(PATH="$work/bin:$PATH" GH_TOKEN=t GITHUB_REPOSITORY=o/r GITHUB_OUTPUT="$work/output" \
  "$script_dir/unnamed-files.sh" "$areas" 2>&1)
status=$?
set -e
check "fails" "$(is "$status" 1)"
check "names the inputs" \
  "$(contains "$out" '::error::unnamed-files.sh needs PR_NUMBER (a pull request) or BASE_SHA (a merge group) in its environment')"

summary "unnamed-files.sh selects every job when a changed file matches no filter"
