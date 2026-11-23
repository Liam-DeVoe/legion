#!/usr/bin/env bash
# Tests for `leg-verdict.sh`, the verdict of each job of ci.yaml that carries the old check-run name
# of a job split into legs: it passes only when each of its own legs passed, read from the newest
# record of each leg in the jobs of its own run attempt.
#
# Each case runs the real script with a stand-in `gh` first on PATH, which answers the one read the
# script makes - every page of one attempt's jobs, projected with the script's own --jq - with a
# list captured from GitHub with that same command (testdata/leg-verdict, whose README holds each
# command): a run whose attempt 2 re-ran one job, which failed there after passing in attempt 1; a
# run with skipped jobs, a skipped matrix leg among them; and a dispatch run whose skipped job's
# record carries its name: expression's text. The check runs captured at each run's head, which
# Dispatch reads a check from, hold the verdict to theirs: for each name the run's jobs carry, the
# script passes a leg exactly when the newest check run of that name in the run succeeded.
#
# Beside the cases that decide the verdict, a copy of the script with that part changed runs the
# same case and must fail it: one that reads the run's first attempt rather than its own, and one
# that takes the oldest record of a name a list holds twice rather than the newest.
#
# Needs jq. Run from anywhere: .github/scripts/leg-verdict.test.sh
# CI runs it in ci.yaml's scripts job.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
captured="$script_dir/testdata/leg-verdict"

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
# The copies the controls run source gh-read.sh from beside themselves, as the script does.
cp "$script_dir/gh-read.sh" "$work/"

for input in run-44852710105-attempt-1.jobs run-44852710105-attempt-2.jobs run-44868985209-attempt-1.jobs \
  run-44967572160-attempt-1.jobs commit-9d197d9b66.check-runs commit-70cd65ee80.check-runs \
  commit-360fe13e76.check-runs; do
  if [ ! -s "$captured/$input" ]; then
    check "the capture $captured/$input is there" false
    summary "leg-verdict.sh"
  fi
done

# The stand-in GitHub: the script's read of one attempt's jobs, exactly, answered from
# $work/answer.<attempt> (a list, or the word `refuse`, which answers an HTTP 502 as GitHub does).
# Any other call fails, so a change to the read's endpoint, page size or projection without new
# captures fails here.
mkdir "$work/bin"
cat >"$work/bin/gh" <<GH
#!/usr/bin/env bash
set -euo pipefail
attempt=\$(sed -nE 's|^repos/cybermonklabs/legion/actions/runs/[0-9]+/attempts/([0-9]+)/jobs\?per_page=50\$|\1|p' <<<"\${3:-}")
if [ \$# -ne 5 ] || [ "\$1 \$2" != "api --paginate" ] || [ -z "\$attempt" ] || [ "\$4" != --jq ] \
  || [ "\$5" != '.jobs[] | {id, name, run_attempt, status, conclusion}' ] || [ ! -e "$work/answer.\$attempt" ]; then
  echo "stand-in gh: no answer for: gh \$*" >&2
  exit 1
fi
if [ "\$(cat "$work/answer.\$attempt")" = refuse ]; then
  echo "gh: Server Error (HTTP 502)" >&2
  exit 1
fi
cat "$work/answer.\$attempt"
GH
chmod +x "$work/bin/gh"

# verdict <script> <run> <attempt> <leg>...: the script run as that run's attempt, its output, then
# `status=<its exit status>`. The stand-in answers each attempt from $work/answer.<attempt>, which
# the case writes first.
verdict() {
  local script=$1 run=$2 attempt=$3
  shift 3
  local status=0
  PATH="$work/bin:$PATH" GH_RETRY_SECONDS=0 GITHUB_REPOSITORY=cybermonklabs/legion GITHUB_RUN_ID="$run" \
    GITHUB_RUN_ATTEMPT="$attempt" bash "$script" "$@" 2>&1 || status=$?
  echo "status=$status"
}

# answer <attempt> <list file or "refuse">: what the stand-in answers that attempt's read with.
answer() {
  rm -f "$work"/answer.*
  if [ "$2" = refuse ]; then echo refuse >"$work/answer.$1"; else cp "$2" "$work/answer.$1"; fi
}

real="$script_dir/leg-verdict.sh"
first="$captured/run-44852710105-attempt-1.jobs"
rerun="$captured/run-44852710105-attempt-2.jobs"

echo "case: a leg re-run in a later attempt is read from that attempt"
answer 2 "$rerun"
out=$(verdict "$real" 44852710105 2 'kind / rig')
check "fails" "$(contains "$out" '^status=1$')"
check "names the leg, its job of attempt 2 and its conclusion" \
  "$(contains "$out" '^FAIL kind / rig: its job 136726616024 of attempt 2 concluded failure$')"
check "says so on an error line" "$(contains "$out" '^::error::the leg kind / rig did not pass')"

echo "case: the legs the later attempt did not re-run keep the records it copied in"
out=$(verdict "$real" 44852710105 2 'kind / shares' 'kind / tree-claude' 'dispatch-two-builds / dispatch-two-builds')
check "passes" "$(contains "$out" '^status=0$')"
check "each leg is read as passed" "$(is "$(grep -c '^PASS .*: success$' <<<"$out")" 3)"

echo "case: a leg of another suite of the same job failed, and these legs passed"
out=$(verdict "$real" 44852710105 2 'kind / image' 'kind / runtime-root')
check "passes, though kind / rig of the same job failed" "$(contains "$out" '^status=0$')"

echo "case: attempt 1, before the re-run"
answer 1 "$first"
out=$(verdict "$real" 44852710105 1 'kind / rig')
check "passes the leg that attempt ran green" "$(contains "$out" '^status=0$')"

echo "case: a skipped leg"
answer 1 "$captured/run-44868985209-attempt-1.jobs"
out=$(verdict "$real" 44868985209 1 'dispatch-two-builds / dispatch-two-builds' 'dispatch-two-builds / dispatch-two-builds-same')
check "fails" "$(contains "$out" '^status=1$')"
check "names the skipped leg" \
  "$(contains "$out" '^FAIL dispatch-two-builds / dispatch-two-builds-same: its job 136739109519 of attempt 1 concluded skipped$')"
check "and passes the other" "$(contains "$out" '^PASS dispatch-two-builds / dispatch-two-builds: success$')"

echo "case: a leg no record carries, as a job a run skipped whole carries its expression's text"
answer 1 "$captured/run-44967572160-attempt-1.jobs"
out=$(verdict "$real" 44967572160 1 'pr-title')
check "fails" "$(contains "$out" '^status=1$')"
check "says no job of the attempt carries the name" \
  "$(contains "$out" '^FAIL pr-title: no job of this run attempt carries the name$')"

echo "case: a list that holds a name twice, as the run's lists of both attempts together"
cat "$first" "$rerun" >"$work/both.jobs"
answer 2 "$work/both.jobs"
out=$(verdict "$real" 44852710105 2 'kind / rig')
check "reads the newest record, attempt 2's failure" \
  "$(contains "$out" '^FAIL kind / rig: its job 136726616024 of attempt 2 concluded failure$')"

echo "case: the verdict of each leg is the newest check run of its name in the run, as Dispatch reads it"
# For each name an attempt's jobs carry, the script's PASS or FAIL against the newest check run
# (the highest id) of that name at the run's head whose details_url is of the run.
for spec in 44852710105:2:commit-9d197d9b66 44868985209:1:commit-70cd65ee80 44967572160:1:commit-360fe13e76; do
  IFS=: read -r run attempt head <<<"$spec"
  jobs="$captured/run-$run-attempt-$attempt.jobs"
  answer "$attempt" "$jobs"
  mapfile -t names < <(jq -r '.name' "$jobs" | sort -u)
  out=$(verdict "$real" "$run" "$attempt" "${names[@]}")
  read_by_script=$(grep -E '^(PASS|FAIL) ' <<<"$out" | sed -E 's/^(PASS|FAIL) (.*): .*$/\2\t\1/' | sort)
  read_from_checks=$(jq -rs --arg run "/actions/runs/$run/" '
    map(select(.details_url | contains($run))) | group_by(.name)[] | max_by(.id)
    | "\(.name)\t\(if .status == "completed" and .conclusion == "success" then "PASS" else "FAIL" end)"' \
    "$captured/$head.check-runs" | sort)
  check "run $run attempt $attempt: each of its ${#names[@]} names, as the head's check runs give it" \
    "$(is "$read_by_script" "$read_from_checks")"
done

echo "case: no leg named"
answer 2 "$rerun"
out=$(verdict "$real" 44852710105 2)
check "is a usage error" "$(contains "$out" '^status=2$')"

echo "case: a run attempt that is not given"
out=$(verdict "$real" 44852710105 '' 'kind / rig')
check "is a usage error" "$(contains "$out" '^status=2$')"
check "names the variable" "$(contains "$out" 'GITHUB_RUN_ATTEMPT is not set')"

echo "case: GitHub refuses the read three times"
answer 2 refuse
out=$(verdict "$real" 44852710105 2 'kind / rig')
check "decides nothing" "$(contains "$out" '^status=2$')"
check "says the list was refused" "$(contains "$out" 'GitHub refused the jobs of run 44852710105, attempt 2, three times')"

echo "control: a copy that reads the run's first attempt passes the re-run leg that failed"
sed 's|attempts/\$GITHUB_RUN_ATTEMPT/jobs|attempts/1/jobs|' "$real" >"$work/first-attempt.sh"
check "the copy differs from the script" "$(cmp -s "$real" "$work/first-attempt.sh" && echo false || echo true)"
answer 2 "$rerun"
cp "$first" "$work/answer.1"
out=$(verdict "$work/first-attempt.sh" 44852710105 2 'kind / rig')
check "the copy passes kind / rig, which the re-run attempt failed" "$(contains "$out" '^PASS kind / rig: success$')"

echo "control: a copy that takes the oldest record of a name passes the twice-listed leg that failed"
sed 's/sort_by(.run_attempt, .id) | last/sort_by(.run_attempt, .id) | first/' "$real" >"$work/oldest.sh"
check "the copy differs from the script" "$(cmp -s "$real" "$work/oldest.sh" && echo false || echo true)"
answer 2 "$work/both.jobs"
out=$(verdict "$work/oldest.sh" 44852710105 2 'kind / rig')
check "the copy passes kind / rig" "$(contains "$out" '^PASS kind / rig: success$')"

summary "leg-verdict.sh passes a carrying job only when each of its own legs' newest record passed"
