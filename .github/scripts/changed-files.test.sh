#!/usr/bin/env bash
# Tests for `changed-files.sh`: the files of a pull request or a merge group, read with a counted
# retry and written as the repository ci.yaml's `changes` job hands its paths-filter steps.
#
# A pull request case runs the script with a stand-in `gh` first on PATH. It answers the files
# route with two pages of fixture rows, printed back to back as `gh api --paginate` prints them,
# and it records every call; told to, it refuses its first k calls - with a 502, with the answer
# paths-filter met on pull request #4449, or by hanging - so a case counts the attempts the script
# made. A merge group case builds an origin repository holding the base and the head, clones the
# head alone, and puts a stand-in `git` first on PATH that counts each fetch and can refuse the
# first k before handing the call to the real git. Each case reads the written repository the way
# paths-filter reads it with `base: HEAD`: `git diff --no-renames --name-status -z HEAD`.
#
# Run from anywhere: .github/scripts/changed-files.test.sh
# CI runs it in ci.yaml's test job.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir "$work/bin"
real_git=$(command -v git)
# A 90 s budget with no wait between attempts: (90 - 0) / 6 - 10 = 5 s an attempt. The hanging case
# waits it out once, and the fetches from a local origin, which take milliseconds, stay well inside
# it on a loaded runner.
export GH_RETRY_SECONDS=0 GITHUB_REPOSITORY=o/r STANDIN_DIR="$work"
BUDGET=90

# Two pages of the files route. The rows carry every status the API answers with, and two pairs of
# paths that trade a file for a directory (docs/old becomes a directory, lib/x/y becomes the file
# lib/x), which a repository holds only because a removal goes into the index before an addition.
cat > "$work/pages.json" <<'JSON'
[{"filename": "src/app.ts", "status": "modified"},
 {"filename": "src/new.ts", "status": "added"},
 {"filename": "src/gone.ts", "status": "removed"},
 {"filename": "pkg/after.go", "status": "renamed", "previous_filename": "pkg/before.go"}]
[{"filename": "copy/of.txt", "status": "copied", "previous_filename": "copy/source.txt"},
 {"filename": "bin/run.sh", "status": "changed"},
 {"filename": "docs/old", "status": "removed"},
 {"filename": "docs/old/index.md", "status": "added"},
 {"filename": "lib/x/y", "status": "removed"},
 {"filename": "lib/x", "status": "added"},
 {"filename": "name with spaces.md", "status": "added"}]
JSON
# What paths-filter's own read makes of those rows (src/main.ts, getChangedFilesFromApi): every
# filename, and a renamed row's previous_filename beside it. Sorted, as `diff_paths` prints.
expected_paths=$(printf '%s\n' 'bin/run.sh' 'copy/of.txt' 'docs/old' 'docs/old/index.md' 'lib/x' 'lib/x/y' \
  'name with spaces.md' 'pkg/after.go' 'pkg/before.go' 'src/app.ts' 'src/gone.ts' 'src/new.ts')

cat > "$work/bin/gh" <<'GH'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$STANDIN_DIR/gh-calls"
calls=$(wc -l < "$STANDIN_DIR/gh-calls")
if [ "$calls" -le "${GH_REFUSE_FIRST:-0}" ]; then
  case ${GH_REFUSAL:-502} in
    502) echo "gh: Bad Gateway (HTTP 502)" >&2 ;;
    # The answer paths-filter printed in run 44595619799; that log holds no HTTP status, and the
    # script retries a refusal whatever its status.
    load) echo "gh: Server Error: Sorry, this diff is temporarily unavailable due to heavy server load." >&2 ;;
    hang) exec sleep 600 ;;
    # A read that ignores TERM, so only the KILL `timeout -k` sends 10 s after its bound ends it.
    hang-ignoring-term) trap '' TERM; exec sleep 600 ;;
  esac
  exit 1
fi
cat "$STANDIN_DIR/pages.json"
GH
chmod +x "$work/bin/gh"

cat > "$work/bin/git" <<GIT
#!/usr/bin/env bash
if [ "\${1:-}" = fetch ]; then
  printf '%s\n' "\$*" >> "\$STANDIN_DIR/git-fetches"
  if [ "\$(wc -l < "\$STANDIN_DIR/git-fetches")" -le "\${GIT_REFUSE_FIRST:-0}" ]; then
    echo "fatal: unable to access 'https://github.com/o/r/': The requested URL returned error: 502" >&2
    exit 128
  fi
fi
exec "$real_git" "\$@"
GIT
chmod +x "$work/bin/git"

# list_files <event> <directory> [VAR=value...]: runs the script with --budget $BUDGET under a
# `timeout` of CASE_SECONDS (60 by default) over $work/event.json, writing the repository to
# <directory>; sets out, err and status, and leaves the stand-ins' call logs in gh_calls and
# fetches.
list_files() {
  local event_name=$1 dest=$2
  shift 2
  rm -f "$work/gh-calls" "$work/git-fetches"
  set +e
  out=$(env PATH="$work/bin:$PATH" GITHUB_EVENT_NAME="$event_name" GITHUB_EVENT_PATH="$work/event.json" "$@" \
    timeout "${CASE_SECONDS:-60}" "$script_dir/changed-files.sh" --budget "$BUDGET" "$dest" 2> "$work/err")
  status=$?
  set -e
  err=$(cat "$work/err")
  gh_calls=$(cat "$work/gh-calls" 2> /dev/null || true)
  fetches=$(cat "$work/git-fetches" 2> /dev/null || true)
}

# diff_paths <directory>: the paths paths-filter reads there with `base: HEAD`, one a line, sorted.
diff_paths() {
  "$real_git" -C "$1" diff --no-renames --name-status -z HEAD | tr '\0' '\n' | awk 'NR % 2 == 0' | LC_ALL=C sort
}

# diff_status <directory> <path>: the status that diff gives the path.
diff_status() {
  "$real_git" -C "$1" diff --no-renames --name-status -z HEAD | tr '\0' '\n' |
    awk -v want="$2" 'NR % 2 == 1 { status = $0; next } $0 == want { print status }'
}

call_count() { if [ -z "$1" ]; then echo 0; else wc -l <<<"$1" | tr -d ' '; fi; }

jq -n '{action: "synchronize", number: 7, pull_request: {number: 7}}' > "$work/event.json"

echo "case: a pull request whose files read at the first attempt"
list_files pull_request "$work/pr"
check "it answers" "$(is "$status" 0)"
check "it read the files route once, every page of it" \
  "$(is "$gh_calls" "api --paginate repos/o/r/pulls/7/files?per_page=100")"
check "the repository's diff names exactly the paths paths-filter's own read names" \
  "$(is "$(diff_paths "$work/pr")" "$expected_paths")"
check "an added file is added" "$(is "$(diff_status "$work/pr" src/new.ts)" A)"
check "a removed file is deleted" "$(is "$(diff_status "$work/pr" src/gone.ts)" D)"
check "a modified file is modified" "$(is "$(diff_status "$work/pr" src/app.ts)" M)"
check "a renamed file is its new name added" "$(is "$(diff_status "$work/pr" pkg/after.go)" A)"
check "  and its old name deleted" "$(is "$(diff_status "$work/pr" pkg/before.go)" D)"
check "a file that became a directory is deleted" "$(is "$(diff_status "$work/pr" docs/old)" D)"
check "  and the file under it added" "$(is "$(diff_status "$work/pr" docs/old/index.md)" A)"
check "a directory that became a file is its file deleted" "$(is "$(diff_status "$work/pr" lib/x/y)" D)"
check "  and the new file added" "$(is "$(diff_status "$work/pr" lib/x)" A)"
check "it says how many path lines it read, and from where" \
  "$(is "$out" "12 path(s) changed: pull request #7, read from GET repos/o/r/pulls/7/files")"

echo "case: GitHub answers 502 to the first two reads and then the files"
list_files pull_request "$work/pr-502" GH_REFUSE_FIRST=2
check "it answers" "$(is "$status" 0)"
check "after two retries: three reads" "$(is "$(call_count "$gh_calls")" 3)"
check "with the same paths" "$(is "$(diff_paths "$work/pr-502")" "$expected_paths")"
check "each refusal is in the log" "$(is "$(grep -c 'HTTP 502' <<<"$err")" 2)"

echo "case: GitHub answers 'this diff is temporarily unavailable' once and then the files"
list_files pull_request "$work/pr-load" GH_REFUSE_FIRST=1 GH_REFUSAL=load
check "it answers" "$(is "$status" 0)"
check "after one retry: two reads" "$(is "$(call_count "$gh_calls")" 2)"
check "with the same paths" "$(is "$(diff_paths "$work/pr-load")" "$expected_paths")"

echo "case: a read that hangs is ended at its bound and counts as one refused attempt"
list_files pull_request "$work/pr-hang" GH_REFUSE_FIRST=1 GH_REFUSAL=hang
check "it answers" "$(is "$status" 0)"
check "two reads" "$(is "$(call_count "$gh_calls")" 2)"
check "with the same paths" "$(is "$(diff_paths "$work/pr-hang")" "$expected_paths")"

echo "case: GitHub never answers the files"
list_files pull_request "$work/pr-down" GH_REFUSE_FIRST=99
check "it fails" "$(is "$status" 1)"
check "after three attempts" "$(is "$(call_count "$gh_calls")" 3)"
check "naming the pull request, the read, the attempts and their bounds" \
  "$(contains "$err" "^::error::the changes job could not read the files of pull request #7: GET repos/o/r/pulls/7/files failed 3 attempts, 0 s and 0 s apart, each bounded at 5 s within the step's 90 s budget ")"
check "it writes no repository" "$(is "$(test -e "$work/pr-down" && echo yes || echo no)" no)"

echo "case: GitHub never answers, and each read ignores TERM, so only KILL ends it"
# The worst case the budget is divided for, on the one read a pull request makes: three attempts,
# each running to its 5 s bound and killed 10 s later, 45 s in all. The case's own bound is the
# 90 s budget, so a script that spent more than its budget is killed here and fails the first check.
CASE_SECONDS=$BUDGET list_files pull_request "$work/pr-term" GH_REFUSE_FIRST=99 GH_REFUSAL=hang-ignoring-term
check "it fails by itself, inside its budget" "$(is "$status" 1)"
check "after three attempts" "$(is "$(call_count "$gh_calls")" 3)"
check "naming the read" "$(contains "$err" "^::error::the changes job could not read the files of pull request #7: ")"

echo "case: the budget is divided over two reads of three attempts and their waits"
# 300 s with a 1 s backoff unit, so 1 s and 2 s between attempts: (300 - 2 x 3) / 6 - 10 = 39 s an
# attempt. Every read is refused at once, so the case reads the bound from the error line.
BUDGET=300
list_files pull_request "$work/pr-300" GH_REFUSE_FIRST=99 GH_RETRY_SECONDS=1
BUDGET=90
check "it fails after three refused attempts" "$(is "$status" 1)"
check "each attempt bounded at 39 s within the 300 s budget" \
  "$(contains "$err" "failed 3 attempts, 1 s and 2 s apart, each bounded at 39 s within the step's 300 s budget ")"

echo "case: a pull request that changes no file"
echo '[]' > "$work/pages.json"
list_files pull_request "$work/pr-empty"
check "it answers" "$(is "$status" 0)"
check "the repository's diff is empty, so every filter is false" "$(is "$(diff_paths "$work/pr-empty")" "")"

# 65 s: 65 / 6 - 10 = 0 s an attempt. 66 s would leave 1 s, which the script takes.
echo "case: a budget that leaves no second for an attempt, and no budget at all"
rm -f "$work/gh-calls"
set +e
err=$(env PATH="$work/bin:$PATH" GITHUB_EVENT_NAME=pull_request GITHUB_EVENT_PATH="$work/event.json" \
  "$script_dir/changed-files.sh" --budget 65 "$work/pr-small" 2>&1 > /dev/null)
status=$?
set -e
check "a 65 s budget is refused" "$(is "$status" 2)"
check "  and says why" "$(contains "$err" "^::error::changed-files.sh's budget of 65 s leaves no second for an attempt: 2 reads of 3 attempts, each with 10 s between TERM and KILL, and 0 s of waiting a read\$")"
set +e
err=$(env PATH="$work/bin:$PATH" GITHUB_EVENT_NAME=pull_request GITHUB_EVENT_PATH="$work/event.json" \
  "$script_dir/changed-files.sh" "$work/pr-nobudget" 2>&1 > /dev/null)
status=$?
set -e
check "a call with no budget is refused" "$(is "$status" 2)"
check "  and says the budget comes first" "$(contains "$err" '^::error::usage: changed-files.sh --budget <seconds> <directory>')"
check "neither read the API" "$(is "$(cat "$work/gh-calls" 2> /dev/null || true)" "")"

echo "case: a merge group whose base the checkout lacks"
origin="$work/origin"
"$real_git" init -q --initial-branch=main "$origin"
"$real_git" -C "$origin" config uploadpack.allowAnySHA1InWant true
commit() { "$real_git" -C "$origin" add -A && "$real_git" -C "$origin" -c user.name=t -c user.email=t@localhost -c commit.gpgsign=false commit -q --no-verify -m "$1"; }
mkdir -p "$origin/src" "$origin/docs" "$origin/lib/x"
printf 'app\n' > "$origin/src/app.ts"
printf 'gone\n' > "$origin/src/gone.ts"
printf 'same\n' > "$origin/src/same.ts"
printf 'old\n' > "$origin/docs/old"
printf 'y\n' > "$origin/lib/x/y"
printf 'before\n' > "$origin/src/before.go"
commit base
base_sha=$("$real_git" -C "$origin" rev-parse HEAD)
printf 'app, changed\n' > "$origin/src/app.ts"
rm "$origin/src/gone.ts" "$origin/docs/old" "$origin/lib/x/y"
rmdir "$origin/lib/x"
"$real_git" -C "$origin" mv src/before.go src/after.go
mkdir -p "$origin/docs/old"
printf 'index\n' > "$origin/docs/old/index.md"
printf 'x\n' > "$origin/lib/x"
printf 'new\n' > "$origin/src/new.ts"
commit head
head_sha=$("$real_git" -C "$origin" rev-parse HEAD)
"$real_git" clone -q --depth 1 "file://$origin" "$work/checkout"
group_paths=$(printf '%s\n' 'docs/old' 'docs/old/index.md' 'lib/x' 'lib/x/y' 'src/after.go' 'src/app.ts' \
  'src/before.go' 'src/gone.ts' 'src/new.ts')
jq -n --arg base "$base_sha" --arg head "$head_sha" '{merge_group: {base_sha: $base, head_sha: $head}}' > "$work/event.json"
cd "$work/checkout"
check "the clone lacks the base" "$(is "$("$real_git" cat-file -e "$base_sha^{commit}" 2> /dev/null && echo has || echo lacks)" lacks)"
list_files merge_group "$work/group"
check "it answers" "$(is "$status" 0)"
check "it fetched the base, and only the base" "$(is "$fetches" "fetch --depth=1 --no-tags origin $base_sha")"
check "it read no API" "$(is "$gh_calls" "")"
check "the repository's diff names exactly the paths of git diff base..head" \
  "$(is "$(diff_paths "$work/group")" "$group_paths")"
check "  which is the diff paths-filter runs for a merge group" \
  "$(is "$("$real_git" diff --no-renames --name-only "$base_sha..$head_sha" | LC_ALL=C sort)" "$group_paths")"
check "a file that became a directory is deleted" "$(is "$(diff_status "$work/group" docs/old)" D)"
check "a renamed file is its new name added" "$(is "$(diff_status "$work/group" src/after.go)" A)"
check "it says where it read them" "$(is "$out" "9 path(s) changed: merge group $base_sha..$head_sha, read with git diff")"

echo "case: a merge group whose base fetch is refused twice and then served"
"$real_git" clone -q --depth 1 "file://$origin" "$work/checkout-502"
cd "$work/checkout-502"
list_files merge_group "$work/group-502" GIT_REFUSE_FIRST=2
check "it answers" "$(is "$status" 0)"
check "after two retries: three fetches" "$(is "$(call_count "$fetches")" 3)"
check "with the same paths" "$(is "$(diff_paths "$work/group-502")" "$group_paths")"

echo "case: a merge group whose base fetch is never served"
"$real_git" clone -q --depth 1 "file://$origin" "$work/checkout-down"
cd "$work/checkout-down"
list_files merge_group "$work/group-down" GIT_REFUSE_FIRST=99
check "it fails" "$(is "$status" 1)"
check "after three attempts" "$(is "$(call_count "$fetches")" 3)"
check "naming the commit, the read and the attempts" \
  "$(contains "$err" "^::error::the changes job could not fetch commit $base_sha of the merge group from origin: git fetch failed 3 attempts, ")"
check "it writes no repository" "$(is "$(test -e "$work/group-down" && echo yes || echo no)" no)"
cd "$work"

echo "case: an event that carries no change is refused"
list_files workflow_dispatch "$work/dispatched"
check "it fails" "$(is "$status" 1)"
check "and names the event" \
  "$(contains "$err" "^::error::changed-files.sh lists the files of a pull_request or a merge_group event, not of workflow_dispatch\$")"

summary "the change's files the changes job hands its paths-filter steps"
