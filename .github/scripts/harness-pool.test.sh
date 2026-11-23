#!/usr/bin/env bash
# Tests for `harness-pool.sh`: harnesses started by one step run beside each other, never more at
# once than the slots, each in its own session with the starting step's environment and working
# directory; a harness started with --minutes sees its deadline and is stopped at its bound; and
# the wait is the verdict on every one of them, a failure, a harness that signals its own process
# group, a recorder that was killed and a wait stopped by a TERM included, and on none started.
#
# Every case drives the script CI runs, in a pool directory of its own. The harnesses are small
# bash commands that wait on marker files the case writes, so no case decides anything by a
# clock: each wait for a file gives up after 20 s and fails its case by name rather than hanging.
# The two exceptions are bounded both ways: the deadline case holds the exported value between
# the clock read before the start and the one after the wait, and the bound case shortens a
# minute to one second through HARNESS_POOL_SECONDS_PER_MINUTE and requires only that a harness
# which would sleep 300 s is stopped.
#
# Run from anywhere: .github/scripts/harness-pool.test.sh
# CI runs it in ci.yaml's test job.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

pool="$script_dir/harness-pool.sh"
work=$(mktemp -d)
# Every harness a case starts has its case's directory in its command line, so ending each process
# that names $work ends what a failed case left running, and nothing of any other run.
trap 'pkill -KILL -f -- "$work/" || true; rm -rf "$work"' EXIT

# appears <file>: true once the file exists, false if it does not within 20 s.
appears() {
  local tries=0
  until [[ -e $1 ]]; do
    tries=$((tries + 1))
    if ((tries > 400)); then
      echo false
      return
    fi
    sleep 0.05
  done
  echo true
}

# fresh <case>: points the pool at a directory of the case's own.
fresh() {
  export HARNESS_POOL_DIR="$work/$1/pool"
  mkdir -p "$work/$1"
  m="$work/$1"
}

# run_wait: sets out (the wait's stdout and stderr) and status.
run_wait() {
  status=0
  out=$(timeout 60 "$pool" wait 2>&1) || status=$?
}

echo "case: two harnesses run at once, and a start returns while its harness still runs"
fresh beside
export HARNESS_POOL_SLOTS=2
# Each harness says it started and then waits for the other's start: they both end only if they
# ran at the same time. The case releases neither; a pool that ran a harness inside its start
# would leave that start waiting forever, so each start is bounded and a start cut off by its
# bound fails the case.
start_status=0
timeout 20 "$pool" start one bash -c "touch '$m/one'; until [[ -e '$m/two' ]]; do sleep 0.05; done; echo one saw two" || start_status=$?
check "the first start returned while its harness still waited" "$(is "$start_status" 0)"
timeout 20 "$pool" start two bash -c "touch '$m/two'; until [[ -e '$m/one' ]]; do sleep 0.05; done; echo two saw one" || start_status=$?
check "and so did the second" "$(is "$start_status" 0)"
check "the first harness started" "$(appears "$m/one")"
check "the second started while the first still waited for it" "$(appears "$m/two")"
run_wait
check "both ended, and the wait passes" "$(is "$status" 0)"
check "it prints the first's log in a group of its own name" "$(contains "$out" '^::group::one: exit 0, ')"
check "with the harness's own output" "$(contains "$out" '^one saw two$')"
check "and the second's after it" "$(contains "$out" '^::group::two: exit 0, ')"
check "and names the count" "$(contains "$out" '^harness-pool: 2 harnesses, 0 failed$')"

echo "case: no more harnesses run at once than the slots, a start returns at once, and they start in the order queued"
fresh slots
export HARNESS_POOL_SLOTS=1
"$pool" start held bash -c "touch '$m/held-started'; until [[ -e '$m/release' ]]; do sleep 0.05; done"
check "the first harness started" "$(appears "$m/held-started")"
# The second and third wait for the one slot; their starts must return all the same, so each is
# bounded and a start cut off by its bound fails the case. The second looks for its slot every
# 2 s and the third every 50 ms, so a pool that let the first to look take a free slot would start
# the third first.
start_status=0
HARNESS_POOL_POLL=2 timeout 20 "$pool" start second bash -c "touch '$m/second-started'; sleep 0.5; touch '$m/second-ended'" || start_status=$?
check "a start returns while its harness waits for the one slot" "$(is "$start_status" 0)"
HARNESS_POOL_POLL=0.05 timeout 20 "$pool" start third bash -c "if [[ -e '$m/second-ended' ]]; then touch '$m/third-after-second'; fi; touch '$m/third-started'" || start_status=$?
check "and so does the next" "$(is "$start_status" 0)"
# Long enough for a pool that ignored the slot to have started a harness.
sleep 1
check "neither has started while the first holds the one slot" "$(is "$([[ -e $m/second-started || -e $m/third-started ]] && echo ran || echo waiting)" waiting)"
touch "$m/release"
check "the second starts once the first ends" "$(appears "$m/second-started")"
check "the third starts too" "$(appears "$m/third-started")"
check "but only once the second, queued before it, had ended" "$(is "$([[ -e $m/third-after-second ]] && echo after || echo before)" after)"
run_wait
check "all three passed" "$(is "$status" 0)"
check "and the wait gives each its seconds waiting for a slot" "$(contains "$out" '^::group::third: exit 0, [0-9.]* s, [0-9.]* s of CPU, after [0-9.]* s waiting for a slot$')"

echo "case: a harness takes the starting step's environment and working directory"
fresh environment
export HARNESS_POOL_SLOTS=2
mkdir -p "$m/workdir"
# shellcheck disable=SC2016 # the harness, not this shell, expands $STEP_VALUE and $PWD
(cd "$m/workdir" && STEP_VALUE=from-the-step "$pool" start env bash -c 'echo "value=$STEP_VALUE dir=$PWD"')
run_wait
check "it passes" "$(is "$status" 0)"
check "it read the step's env" "$(contains "$out" '^value=from-the-step ')"
check "in the step's directory" "$(contains "$out" "dir=$m/workdir\$")"

echo "case: a harness starts with SIGINT and SIGQUIT at their defaults, as a step's shell does"
fresh signals
# The step's shell queues it with `&` and no job control, which starts a command with both
# ignored; a bash harness could then not trap the INT it sends what it tests. The harness reads
# its own ignored set and traps an INT it sends itself.
# shellcheck disable=SC2016 # the harness, not this shell, expands these
"$pool" start ignores bash -c 'echo "ignored=$(awk "/^SigIgn:/ { print \$2 }" /proc/self/status)"; trap "echo trapped INT; exit 0" INT; kill -INT $$; sleep 5; echo no trap ran; exit 1'
run_wait
check "the harness's INT trap ran" "$(contains "$out" '^trapped INT$')"
ignored=$(sed -n 's/^ignored=//p' <<< "$out")
check "and neither INT nor QUIT (signals 2 and 3, mask 0x6) is in its ignored set" "$(is "$([[ $ignored =~ ^[0-9a-f]+$ ]] && (( (16#$ignored & 0x6) == 0 )) && echo default || echo "ignored '$ignored'")" default)"
check "it passes" "$(is "$status" 0)"

echo "case: a harness's output reaches the step only through the wait"
fresh output
start_out=$("$pool" start talker bash -c 'echo said-by-the-harness; echo on-stderr >&2' 2>&1)
check "the start says what it queued and nothing of the harness" "$(is "$start_out" 'harness-pool: queued talker (000)')"
run_wait
check "the wait prints the harness's standard output" "$(contains "$out" '^said-by-the-harness$')"
check "and its standard error" "$(contains "$out" '^on-stderr$')"

echo "case: a failing harness fails the wait, which names it, and the others still report"
fresh failure
export GITHUB_STEP_SUMMARY="$m/summary" GITHUB_OUTPUT="$m/step-output"
# The harness writes to the runner's per-step files as a step would; the queuing step has ended
# by then, so its writes go to its own entry and the summary reaches the wait's.
# shellcheck disable=SC2016 # the harness, not this shell, expands the variables
"$pool" start passes bash -c 'echo "summary line of passes" >> "$GITHUB_STEP_SUMMARY"; echo "value=1" >> "$GITHUB_OUTPUT"'
"$pool" start fails bash -c 'echo why it failed; exit 3'
run_wait
check "the wait fails" "$(is "$status" 1)"
check "it names the harness, its status and where its log is" "$(contains "$out" '^::error title=fails::exited 3 after [0-9.]* s; its log is in the group of that name above$')"
check "and prints that log" "$(contains "$out" '^why it failed$')"
check "the passing harness is still reported" "$(contains "$out" '^::group::passes: exit 0, ')"
check "and the count names the failure" "$(contains "$out" '^harness-pool: 2 harnesses, 1 failed$')"
summary_text=$(cat "$m/summary")
check "the summary has a row per harness" "$(contains "$summary_text" '^| fails | [0-9.]* | [0-9.]* | [0-9.]* | fail (3) |$')"
check "the passing one too" "$(contains "$summary_text" '^| passes | [0-9.]* | [0-9.]* | [0-9.]* | pass |$')"
check "what a harness wrote to its step summary reaches the wait's, under its name" "$(contains "$summary_text" '^### passes$')"
check "with its own text" "$(contains "$summary_text" '^summary line of passes$')"
check "and its output write does not reach the queuing step's file" "$(is "$([[ -e $m/step-output ]] && echo written || echo untouched)" untouched)"
unset GITHUB_STEP_SUMMARY GITHUB_OUTPUT

echo "case: a harness that signals its own process group is judged by its status"
fresh group
"$pool" start group-kill bash -c 'sleep 30 & kill -TERM 0; wait'
run_wait
check "the wait fails" "$(is "$status" 1)"
check "with the status the signal gave the harness, not with none" "$(contains "$out" '^::group::group-kill: exit 143, ')"

echo "case: a recorder killed before the harness ended is a failure, and the wait does not hang"
fresh killed
"$pool" start orphan bash -c "touch '$m/orphan-started'; sleep 30"
check "the harness started" "$(appears "$m/orphan-started")"
kill -KILL "$(< "$HARNESS_POOL_DIR/000.pid")"
run_wait
check "the wait fails rather than waiting out the harness" "$(is "$status" 1)"
check "and says the harness left no status" "$(contains "$out" '^::error title=orphan::exited none: its process ended without writing one after ? s')"

echo "case: a wait over no harness judges nothing, and says so"
fresh empty
run_wait
check "it exits 2" "$(is "$status" 2)"
check "and names the directory it found empty" "$(contains "$out" "^::error::harness-pool: no harness was queued under $HARNESS_POOL_DIR, so there is nothing to judge\$")"

echo "case: a slot count that is no positive whole number is refused"
fresh refused
status=0
out=$(HARNESS_POOL_SLOTS=0 "$pool" start never true 2>&1) || status=$?
check "it exits 2" "$(is "$status" 2)"
check "and names the value" "$(contains "$out" "^::error::harness-pool: HARNESS_POOL_SLOTS is '0', not a positive whole number\$")"
check "having started nothing" "$(is "$([[ -e $HARNESS_POOL_DIR ]] && echo started || echo none)" none)"

echo "case: a harness started with --minutes sees its deadline, its start plus the bound less 30 s"
fresh deadline
unset HARNESS_POOL_DEADLINE_MS
before=$(date +%s)
# shellcheck disable=SC2016 # the harness, not this shell, expands the variable
"$pool" start --minutes 2 bounded bash -c 'echo "deadline=${HARNESS_POOL_DEADLINE_MS:-unset}"'
# shellcheck disable=SC2016
"$pool" start unbounded bash -c 'echo "unbounded deadline=${HARNESS_POOL_DEADLINE_MS:-unset}"'
run_wait
after=$(date +%s)
check "both pass" "$(is "$status" 0)"
deadline=$(sed -n 's/^deadline=//p' <<< "$out")
check "the value is epoch milliseconds" "$(is "$([[ $deadline =~ ^[0-9]+000$ ]] && echo milliseconds || echo "'$deadline'")" milliseconds)"
in_range=false
if [[ $deadline =~ ^[0-9]+$ ]] && ((deadline >= (before + 90) * 1000 && deadline <= (after + 90) * 1000)); then
  in_range=true
fi
check "it is 120 s less 30 s past the start, between the clock before the start and after the wait" "$in_range"
check "a harness started without --minutes sees none" "$(contains "$out" '^unbounded deadline=unset$')"

echo "case: a harness past its --minutes bound is stopped, and the wait names the bound"
fresh bound
# A minute of one second: the bound is 2 s, and the harness would sleep 300.
HARNESS_POOL_SECONDS_PER_MINUTE=1 "$pool" start --minutes 2 overrun bash -c "touch '$m/overrun-started'; sleep 300; touch '$m/overrun-finished'"
# A harness that exits 124 itself, inside its bound, is a plain failure, not a stop.
HARNESS_POOL_SECONDS_PER_MINUTE=1 "$pool" start --minutes 30 says-124 bash -c 'exit 124'
check "the harness started" "$(appears "$m/overrun-started")"
run_wait
check "the wait fails" "$(is "$status" 1)"
check "and says the harness ran past its bound in the minutes it was given" "$(contains "$out" '^::error title=overrun::ran past its bound of 2 minutes and was stopped (exit 124); its log is in the group of that name above$')"
check "the harness was stopped, not let run to its end" "$(is "$([[ -e $m/overrun-finished ]] && echo finished || echo stopped)" stopped)"
check "a harness that exited 124 inside its bound is named by its status" "$(contains "$out" '^::error title=says-124::exited 124 after [0-9.]* s; ')"

echo "case: a --minutes that is no positive whole number is refused"
fresh minutes
status=0
out=$("$pool" start --minutes soon never true 2>&1) || status=$?
check "it exits 2" "$(is "$status" 2)"
check "and names the value" "$(contains "$out" "^::error::harness-pool: --minutes is 'soon', not a positive whole number\$")"
check "having started nothing" "$(is "$([[ -e $HARNESS_POOL_DIR ]] && echo started || echo none)" none)"

echo "case: --allow-none lets a wait over no harness pass, and judges a started one as ever"
fresh allow-none
status=0
out=$(timeout 60 "$pool" wait --allow-none 2>&1) || status=$?
check "over none it exits 0" "$(is "$status" 0)"
check "and says it judged nothing" "$(contains "$out" "^harness-pool: no harness was queued under $HARNESS_POOL_DIR, and --allow-none: nothing to judge\$")"
"$pool" start fails bash -c 'exit 5'
status=0
out=$(timeout 60 "$pool" wait --allow-none 2>&1) || status=$?
check "over a failing harness it still fails" "$(is "$status" 1)"
check "naming it" "$(contains "$out" '^::error title=fails::exited 5 after ')"
status=0
out=$(timeout 60 "$pool" wait --none 2>&1) || status=$?
check "a wait refuses any other argument" "$(is "$status" 2)"

echo "case: a wait stopped as the runner cancels a step names each harness still running and prints its log so far"
fresh stopped
"$pool" start finished bash -c 'echo said before the stop'
"$pool" start hung bash -c "echo before the hang; touch '$m/hung-started'; until [[ -e '$m/release-hung' ]]; do sleep 0.05; done"
check "the hanging harness started" "$(appears "$m/hung-started")"
check "the other one ended" "$(appears "$HARNESS_POOL_DIR/000.exit")"
# One slot for this one, held by the hanging harness: it is still queued when the wait is stopped.
HARNESS_POOL_SLOTS=1 "$pool" start behind bash -c "touch '$m/behind-started'"
# In a process group of its own, so the signals below reach the wait's group: the poll's sleep and
# every process the report starts take them too. A background job of this script is no group
# leader, so setsid runs the wait in its own pid, which is the group's id.
setsid "$pool" wait > "$m/wait.out" 2>&1 &
waiter=$!
# The wait says it is waiting once its trap is set; a signal before that would end it unheard.
waiting=false
for _ in $(seq 400); do
  if grep -q '^harness-pool: waiting for ' "$m/wait.out"; then
    waiting=true
    break
  fi
  sleep 0.05
done
check "the wait says it is waiting" "$waiting"
# A cancel's INT, then TERMs, as the runner sends one 7.5 s after the INT, here one every 10 ms
# until the wait has ended, so some land while it writes its report.
kill -INT -- "-$waiter"
while kill -0 "$waiter" 2> /dev/null; do
  kill -TERM -- "-$waiter" 2> /dev/null || true
  sleep 0.01
done
status=0
wait "$waiter" || status=$?
out=$(cat "$m/wait.out")
touch "$m/release-hung"
check "the stopped wait fails" "$(is "$status" 1)"
check "it names the harness still running, with its seconds" "$(contains "$out" '^::group::hung: still running after [0-9.]* s when the wait was stopped; its log so far$')"
check "and prints its log so far" "$(contains "$out" '^before the hang$')"
check "and an error naming it" "$(contains "$out" '^::error title=hung::was still running after [0-9.]* s when the wait was stopped; ')"
check "it names the harness still waiting for a slot as never run" "$(contains "$out" '^::error title=behind::was still waiting for a slot when the wait was stopped, and never ran$')"
check "the harness that had ended is reported as ever" "$(contains "$out" '^::group::finished: exit 0, ')"
check "and the count names the two unfinished" "$(contains "$out" '^harness-pool: 3 harnesses, 0 failed, 2 unfinished when the wait was stopped$')"
check "the released harness ends" "$(appears "$HARNESS_POOL_DIR/001.exit")"
check "and the one behind it then runs" "$(appears "$m/behind-started")"

summary "the harness pool"
