#!/usr/bin/env bash
# Tests for `large-pr-lanes.sh`, the two halves of the approval-time run of a large pull request
# (.github/ci-notes/ci.md#a-large-pull-request-runs-its-slow-suites-once-at-the-approved-head):
#
#   label  puts large-pr on a pull request of 100 changed files or 10,000 changed lines or more,
#          from the pull_request event's payload, takes it off one that is no longer large, writes
#          nothing when the label already says the right thing, passes a fork's pull request over,
#          reads the size from the pull request when the payload leaves it out, and fails the step
#          when GitHub refuses the label a large pull request needs.
#   hold   the job named ci-ok of large-pr-lanes.yaml concludes exactly as `approval / ci-ok`
#          does, waits while that job is absent or still running, reads an earlier attempt's
#          verdict only when nothing else re-ran, and fails a run that ends with no verdict.
#
# Each case runs the real script with a stand-in `gh` first on PATH, which answers each read with
# the lines the script's `--jq` would print and records each write it is sent.
#
# Run from anywhere: .github/scripts/large-pr-lanes.test.sh
# CI runs it in ci.yaml's test job.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

mkdir "$work/bin"
cat > "$work/bin/gh" <<GH
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "\$*" >> "$work/calls"
case "\$*" in
  *"--method POST repos/o/r/issues/7/labels -f labels[]=large-pr"*)
    if [ -e "$work/write-fails" ]; then echo "HTTP 403: Resource not accessible by integration" >&2; exit 1; fi
    echo added >> "$work/writes" ;;
  *"--method DELETE repos/o/r/issues/7/labels/large-pr"*)
    if [ -e "$work/write-fails" ]; then echo "HTTP 403: Resource not accessible by integration" >&2; exit 1; fi
    echo removed >> "$work/writes" ;;
  "api repos/o/r/pulls/7 "*)
    cat "$work/pull-size" ;;
  *"repos/o/r/actions/runs/41/jobs?filter=latest&per_page=100"*)
    # One file per read, reads/1, reads/2, ...; past the last, the last answers again.
    n=\$(( \$(cat "$work/read-count" 2>/dev/null || echo 0) + 1 ))
    echo "\$n" > "$work/read-count"
    if [ -e "$work/reads/fail.\$n" ]; then echo "HTTP 502" >&2; exit 1; fi
    last=\$(find "$work/reads" -name '[0-9]*' | wc -l)
    [ "\$n" -le "\$last" ] || n=\$last
    cat "$work/reads/\$n" ;;
  *) echo "unexpected gh \$*" >&2; exit 2 ;;
esac
GH
chmod +x "$work/bin/gh"

reset() { rm -rf "$work/calls" "$work/writes" "$work/write-fails" "$work/pull-size" "$work/reads" "$work/read-count"; }
writes() { [ -e "$work/writes" ] && tr '\n' ' ' < "$work/writes" || echo ""; }
call_count() { [ -e "$work/calls" ] && wc -l < "$work/calls" | tr -d ' ' || echo 0; }

# event <changed files> <additions> <deletions> [<label>...]: a pull_request payload of #7 from
# o/r; a size given as `-` is left out of the payload, as GitHub can.
event() {
  local changed=$1 additions=$2 deletions=$3
  shift 3
  jq -n --arg c "$changed" --arg a "$additions" --arg d "$deletions" --args \
    '{pull_request: ({number: 7, head: {repo: {full_name: "o/r"}},
                      labels: [$ARGS.positional[] | {name: .}]}
                     + (if $c == "-" then {} else {changed_files: ($c | tonumber)} end)
                     + (if $a == "-" then {} else {additions: ($a | tonumber)} end)
                     + (if $d == "-" then {} else {deletions: ($d | tonumber)} end))}' \
    "$@" > "$work/event.json"
}

run_label() {
  set +e
  out=$(PATH="$work/bin:$PATH" GH_TOKEN=t GITHUB_REPOSITORY=o/r GITHUB_EVENT_PATH="$work/event.json" \
    GH_RETRY_SECONDS=0 "$script_dir/large-pr-lanes.sh" label 2>&1)
  status=$?
  set -e
}

echo "case: 100 changed files is large"
reset; event 100 50 50
run_label
check "exits 0" "$(is "$status" 0)"
check "labels it" "$(is "$(writes)" "added ")"
check "says why" "$(contains "$out" '^#7 is large, 100 changed files and 100 changed lines (large is 100 files or 10000 lines): labelled large-pr')"

echo "case: 99 changed files and 9,999 lines is not"
reset; event 99 9000 999
run_label
check "exits 0" "$(is "$status" 0)"
check "writes nothing" "$(is "$(call_count)" 0)"
check "says so" "$(contains "$out" '^#7 is not large, 99 changed files and 9999 changed lines')"

echo "case: 10,000 changed lines in one file is large"
reset; event 1 6000 4000
run_label
check "labels it" "$(is "$(writes)" "added ")"

echo "case: a large pull request that carries the label already"
reset; event 300 1 1 full-ci LARGE-PR
run_label
check "exits 0" "$(is "$status" 0)"
check "writes nothing, reading the label without regard to case as GitHub does" "$(is "$(call_count)" 0)"
check "says so" "$(contains "$out" 'carries large-pr already$')"

echo "case: a pull request that shrank below both lines"
reset; event 3 10 10 large-pr
run_label
check "exits 0" "$(is "$status" 0)"
check "takes the label off" "$(is "$(writes)" "removed ")"

echo "case: the payload leaves the size out"
reset; event - - -
printf '20000\t0\t4\n' > "$work/pull-size"
run_label
check "reads it from the pull request" "$(contains "$(cat "$work/calls")" '^api repos/o/r/pulls/7 ')"
check "and labels by it" "$(is "$(writes)" "added ")"

echo "case: a fork's pull request"
reset; event 500 1 1
jq '.pull_request.head.repo.full_name = "someone/r"' "$work/event.json" > "$work/fork.json"
mv "$work/fork.json" "$work/event.json"
run_label
check "exits 0" "$(is "$status" 0)"
check "calls nothing, since its token cannot write a label" "$(is "$(call_count)" 0)"
check "says so" "$(contains "$out" '^#7 comes from someone/r, a fork')"

echo "case: GitHub refuses the label a large pull request needs"
reset; event 150 1 1
touch "$work/write-fails"
run_label
check "fails the step" "$(is "$status" 1)"
check "tried three times" "$(is "$(call_count)" 3)"
check "says what that costs" "$(contains "$out" '::error::GitHub refused the label large-pr on #7 at every attempt gh-read.sh made, so an approval of it would run no slow suite before the merge queue')"

echo "case: GitHub refuses to take the label off"
reset; event 2 1 1 large-pr
touch "$work/write-fails"
run_label
check "only warns, since a label left on blocks nothing" "$(is "$status" 0)"
check "names the cost" "$(contains "$out" '::warning::GitHub refused to take large-pr off #7')"

echo "case: a payload with no pull request"
reset; printf '{"review": {}}\n' > "$work/event.json"
run_label
check "fails the step" "$(is "$status" 1)"
check "says what it reads" "$(contains "$out" 'large-pr-lanes.sh label reads a pull_request event')"

# reads <file>...: the answers of successive job reads, each a list of rows
# name|status|conclusion|attempt, written as the script's --jq prints them.
reads() {
  local n=0 file row name status conclusion attempt
  mkdir -p "$work/reads"
  for file in "$@"; do
    n=$((n + 1))
    : > "$work/reads/$n"
    IFS=';' read -ra rows <<<"$file"
    for row in "${rows[@]}"; do
      [ -n "$row" ] || continue
      IFS='|' read -r name status conclusion attempt <<<"$row"
      printf '%s\t%s\t%s\t%s\thttps://github.com/o/r/actions/runs/41/job/%s\n' \
        "$name" "$status" "$conclusion" "$attempt" "$n" >> "$work/reads/$n"
    done
  done
}

run_hold() {
  set +e
  out=$(PATH="$work/bin:$PATH" GH_TOKEN=t GITHUB_REPOSITORY=o/r GITHUB_RUN_ID=41 \
    GITHUB_RUN_ATTEMPT=${1:-1} CALLER=approval LARGE_PR_HOLD_POLL_SECONDS=0 GH_RETRY_SECONDS=0 \
    timeout 30 "$script_dir/large-pr-lanes.sh" hold 2>&1)
  status=$?
  set -e
}
reads_made() { cat "$work/read-count"; }

self='ci-ok|in_progress|-|1'
echo "case: the verdict passes after the run's jobs end"
reset
reads "$self;approval / changes|in_progress|-|1" \
  "$self;approval / changes|completed|success|1;approval / pi-legion / pi-legion|in_progress|-|1" \
  "$self;approval / changes|completed|success|1;approval / pi-legion / pi-legion|completed|success|1;approval / ci-ok|queued|-|1" \
  "$self;approval / changes|completed|success|1;approval / pi-legion / pi-legion|completed|success|1;approval / ci-ok|completed|success|1"
run_hold
check "exits 0" "$(is "$status" 0)"
check "waited through the absent and the queued verdict" "$(is "$(reads_made)" 4)"
check "names the verdict" "$(contains "$out" '^approval / ci-ok concluded success on attempt 1: https://github.com/o/r/actions/runs/41/job/4$')"

echo "case: the verdict fails"
reset
reads "$self;approval / pi-legion / pi-legion|completed|failure|1;approval / ci-ok|completed|failure|1"
run_hold
check "fails, so the head's ci-ok is red" "$(is "$status" 1)"
check "says why" "$(contains "$out" '::error::approval / ci-ok concluded failure, so this ci-ok fails')"

echo "case: the verdict is cancelled"
reset
reads "$self;approval / ci-ok|completed|cancelled|1"
run_hold
check "fails: only success passes" "$(is "$status" 1)"

echo "case: a job named ci-ok elsewhere in the run, or a verdict named for another caller"
reset
reads "$self;other / ci-ok|completed|success|1;approval / lint / lint|in_progress|-|1" \
  "$self;other / ci-ok|completed|success|1;approval / lint / lint|completed|success|1;approval / ci-ok|completed|failure|1"
run_hold
check "reads only approval / ci-ok" "$(is "$status" 1)"
check "after waiting for it" "$(is "$(reads_made)" 2)"

echo "case: attempt 2 that re-ran failed jobs, while attempt 1's verdict is still the newest listed"
reset
reads "ci-ok|in_progress|-|2;approval / pi-legion / pi-legion|in_progress|-|2;approval / ci-ok|completed|failure|1" \
  "ci-ok|in_progress|-|2;approval / pi-legion / pi-legion|completed|success|2;approval / ci-ok|completed|success|2"
run_hold 2
check "waits for attempt 2's verdict" "$(is "$(reads_made)" 2)"
check "and concludes as it did" "$(is "$status" 0)"

echo "case: attempt 2 that re-ran this job alone"
reset
reads "ci-ok|in_progress|-|2;approval / pi-legion / pi-legion|completed|success|1;approval / ci-ok|completed|success|1"
run_hold 2
check "reads attempt 1's verdict, which nothing re-ran" "$(is "$status" 0)"
check "at once" "$(is "$(reads_made)" 1)"

echo "case: the run ends with no verdict"
reset
reads "$self;approval / changes|completed|failure|1;approval / lint / lint|completed|skipped|1"
run_hold
check "fails rather than hold the head pending to its timeout" "$(is "$status" 1)"
check "after three reads in a row" "$(is "$(reads_made)" 3)"
check "says why" "$(contains "$out" 'no job of this approval-time run is open and approval / ci-ok has not concluded on attempt 1, 3 reads in a row')"

echo "case: the caller has not started, waiting in its concurrency group for an older run to cancel"
# Test pull request #5347: a second approval's run (45392143357) listed no job of the caller for
# 39 s while the first one's run cancelled.
reset
reads "$self" "$self" "$self" "$self" "$self;approval / changes|in_progress|-|1" \
  "$self;approval / changes|completed|success|1;approval / ci-ok|completed|success|1"
run_hold
check "is waited for, not read as a run that ended" "$(is "$status" 0)"
check "through every read" "$(is "$(reads_made)" 6)"

echo "case: a moment with every job ended and the verdict not yet created"
reset
reads "$self;approval / changes|completed|success|1" "$self;approval / changes|completed|success|1" \
  "$self;approval / changes|completed|success|1;approval / ci-ok|completed|success|1"
run_hold
check "is waited through" "$(is "$status" 0)"

echo "case: GitHub refuses a read"
reset
reads "$self;approval / ci-ok|completed|success|1"
touch "$work/reads/fail.1" "$work/reads/fail.2" "$work/reads/fail.3"
run_hold
check "warns and reads again" "$(contains "$out" "::warning::GitHub refused this run's jobs at every attempt gh-read.sh made")"
check "then concludes" "$(is "$status" 0)"

echo "case: an input the step did not set"
reset
set +e
out=$(PATH="$work/bin:$PATH" GH_TOKEN=t GITHUB_REPOSITORY=o/r GITHUB_RUN_ID=41 GITHUB_RUN_ATTEMPT=1 \
  "$script_dir/large-pr-lanes.sh" hold 2>&1)
status=$?
set -e
check "fails" "$(is "$status" 1)"
check "names the input" "$(contains "$out" 'large-pr-lanes.sh hold needs CALLER in its environment')"

echo "case: no mode"
set +e
out=$("$script_dir/large-pr-lanes.sh" 2>&1)
status=$?
set -e
check "exits 2" "$(is "$status" 2)"

summary "large-pr-lanes.sh labels a large pull request and holds ci-ok to its approval-time run's verdict"
