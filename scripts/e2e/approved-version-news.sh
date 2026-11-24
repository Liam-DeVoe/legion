#!/usr/bin/env bash
# legion: tool - real Oh My Pi sessions on a real model, on this checkout's role prompts and the base's, answer an approved new version of a design; a person runs it for a decision on the prompts, and no job runs it.
#
# Part two of the approved version's news: BenchmarkRealSessionsTellAndAnswerANewVersion
# (packages/daemon/internal/daemon/approved_version_news_sessions_live_test.go), run once as
#   go test -run '^$' -bench '^BenchmarkRealSessionsTellAndAnswerANewVersion$' -benchtime=1x
# Part one, what the daemon sends with no agent, is TestAnApprovedVersionReachesItsHolderLive,
# which CI's daemon-live job runs. This script runs no part one and no control of it.
#
# Part two is real `omp --mode rpc` panes under the real `legion worker-shim`, this checkout's two
# plugins in a profile of the run's own, a real Envoy listener on natsd, both built from the
# checkout, the real Dispatch, and the real model behind the gateway ANTHROPIC_BASE_URL names. It
# runs the steps of the driver's step table, each as many times as the table says in each arm the
# table runs it in, the head's prompts and the base's (the base below): today
# a4-the-architect-tells-the-child-reviewer and
# answers-a-stale-reviewer-moves-back-a-current-one-comments three times in the head arm alone, and
# off-the-architect-tells-the-holders three times in each, which the driver's file describes. The
# plan check runs the benchmark first with -part-two-plan, which prints that plan (each step, its
# arms and the count of runs) and makes no run, and this script counts each step's arms and the
# run's deadline from it, keeping no copy of the steps, their arms or the count. The run prints
# each run's evidence and `run <step> <arm> <n>: <outcome> …`. PASS and FAIL are runs that reached
# the step's assertion. SETUP is a run that stopped before it, such as a holder that did not end
# its start turn in the run's bound, or an architect that did not write the version its task asks
# for. The a4 and off steps assert the telling of that version. HARNESS is a run the harness
# failed, before or after the step's own outcome, or one in which a session's `legion` tool could
# not read the daemon: the plugin refused the daemon's answer as a body the daemon API contract
# does not describe. The driver prints a run's line after it stops the run's daemon, so a fault in
# that stop makes the run HARNESS too. The driver ends the run at a HARNESS run in either arm.
# This script counts PASS and FAIL alone for an arm, prints `<step> <arm>: <p> of <n> runs that
# reached the assertion passed` for each step and arm it runs, and names each run it did not
# count. The run passes when every head run reached its assertion and passed: a head run that
# stopped before its assertion fails the run, and its FAIL line names it. The base arm is the
# control of each step it runs: it bites when a base run failed at its assertion. A base arm that
# passed every run that reached its assertion is printed as no control for that step, which fails
# nothing, and a setup failure is never counted as the control biting. The base arms of a4 and
# answers are retired, each by the measurement its entry of head_only below holds: each passed
# every one of its ten runs at the base, so the arms cannot be separated there, for a reason of
# its own. a4 runs through the child's own architect, which main's #4203 gives every started
# child, and main's own text meets R4 since #4203 (a child's own architect tells its workers of a
# version once its parent approves it). The rule answers tests is written in the daemon's own news
# task (holderRule), which both arms run, since the daemon is the head's in both. The last line
# says so in each one's place: `a4 head 3/3 (head only: <why>)`, and answers' the same. Those
# measurements are the one fact of part two's steps the driver does not know, and the plan check
# fails the run, naming the step, where the driver's plan makes a step in the head arm alone that
# head_only holds no measurement for, or makes a step head_only holds one for in both arms or not
# at all. off is the one step whose control can bite at its base. The driver stops at the first
# head run that does not pass, since the run then fails whatever the rest show. The model key is
# read from APPROVED_VERSION_NEWS_MODEL_KEY_FILE (default /var/run/model-gateway-key/key, where a
# role pod mounts the gateway's key) by the driver itself, which gives it to each pane's
# environment alone. This script exports no key, and it unsets the caller's ANTHROPIC_API_KEY
# before the run, so no build or service it starts holds one. Once the run ended, the key-leak
# check counts the key's first 8 characters in the evidence directory, without printing them, and
# fails the run on any.
#
# Three variables narrow the run to the runs one measurement needs, and the script passes them to
# the benchmark unchanged: APPROVED_VERSION_NEWS_STEP names one step by the word the last line
# names it by (a4, off or answers), APPROVED_VERSION_NEWS_ARM one arm (head or base), and
# APPROVED_VERSION_NEWS_RUNS how many runs each arm makes of each step, a whole number of 1 or more.
# Each left unset keeps every step, each step's own arms (the head arm alone for a4 and answers,
# both for off), or three runs; an arm the pick names is the one arm of every step it keeps, a
# retired base arm among them. The driver's plan says which runs a pick makes: the plan check
# refuses with exit 2, in the driver's own words, a value the driver refuses because it names none
# of these. The script then counts each arm against the pick's count of runs, prints no line for a
# step the pick leaves out, and prints a step whose base arm the pick leaves out as no control. A
# run with any of the three set is a measurement, a partial run: its last line reads MEASURED in
# place of PASS, and names the pick, each step and arm of a full run's plan that the pick leaves
# out, and the count of runs of each arm where it is not a full run's. A head run that the pick
# makes and that does not reach its assertion and pass still makes the last line FAIL. A
# measurement that reaches its end exits 0: what it is run for is the counts its last line prints,
# a base arm that failed at its assertion among them, which no exit status says. So the
# measurement that retired a base arm is repeated by one command, a4's here and answers' with
# APPROVED_VERSION_NEWS_STEP=answers:
#   APPROVED_VERSION_NEWS_STEP=a4 APPROVED_VERSION_NEWS_ARM=base APPROVED_VERSION_NEWS_RUNS=10 \
#     bash scripts/e2e/approved-version-news.sh
# with the variables of the usage below beside them.
#
# Run it from a checkout on a Linux host with go, git, jj 0.38 or later (the daemon's boot refuses
# to start without them), tar, bun and python3, as
#   LEGION_TEST_OMP=/opt/omp/bin/omp ANTHROPIC_BASE_URL=<model gateway> \
#     LEGION_TEST_PG_DSN=postgres://<user>@127.0.0.1:<port>/<database>?sslmode=disable \
#     [APPROVED_VERSION_NEWS_STEP=a4|off|answers] [APPROVED_VERSION_NEWS_ARM=head|base] \
#     [APPROVED_VERSION_NEWS_RUNS=<n>] \
#     bash scripts/e2e/approved-version-news.sh
# It takes no argument. LEGION_TEST_PG_DSN is required and names a loopback PostgreSQL with the
# pgvector extension: the driver creates its daemon runs' databases and Dispatch's on it and drops
# them on exit, and the arguments check refuses a DSN that is unset or whose host is not loopback
# with exit 2, before anything is built or extracted. LEGION_TEST_OMP (an executable, the Oh My
# Pi every pane runs), ANTHROPIC_BASE_URL and the key file are refused with exit 2 when unset or
# unreadable. APPROVED_VERSION_NEWS_MODEL names the model (default claude-opus-5-5, at the xhigh
# thinking level, the fleet's own: a cheaper model measures itself rather than the prompts).
# APPROVED_VERSION_NEWS_EVIDENCE_DIR (default a fresh directory under TMPDIR, or /tmp when it is
# unset, kept and printed) keeps this script's transcript and, in a fresh part-two.* directory of
# its own, the benchmark's output (proof.log) and, under the body's name
# (RealSessionsTellAndAnswerANewVersion, the directory LEGION_TEST_DAEMON_LOG_DIR gives it), the
# logs of natsd, the listener, Dispatch and each run's daemon, and each run's session transcripts
# (transcripts/). Nothing reaches github.com or a deployed Dispatch.
#
# The base is the commit before this change: `git merge-base HEAD origin/main`, main's tip as this
# branch last merged it, which holds everything this branch carries but its own change. That is the
# base because main moves under the branch and each forward merge brings its changes in: a fixed
# older commit would differ from the head by those too. The base arm takes that commit's role
# prompt files (`git archive <base> packages/daemon/internal/prompts/{roles,go}`), which the
# driver writes over the snapshot the daemon takes at boot. So the run needs the checkout's history
# down to the base, which a shallow clone lacks: the arguments check refuses a base it cannot find
# with exit 2.
#
# Exit 0 is a pass or a measurement that reached its end; exit 2 is an input of the caller's the
# run refuses, in the arguments check or the plan check; exit 1 is every other failure. The last
# line reads PASS or MEASURED only for a run that reached the script's end, which calls run_end
# (lib/run-verdict.sh): a signal that ends the script leaves bash's EXIT trap a status of 0, and
# its line reads FAIL at the check it stopped in, with exit 1, or the signal's status.
set -euo pipefail

root=$(cd "$(dirname "$0")/../.." && pwd)
bench=BenchmarkRealSessionsTellAndAnswerANewVersion
# The benchmark's body, which it runs as one test through testing.RunTests: its `--- PASS` line
# under -v is the one sign that the body ran to its end rather than skipping.
body=RealSessionsTellAndAnswerANewVersion

# Part two's steps, the arms each runs in and how many runs each arm makes are the driver's step
# table's alone (sessionsSteps and pickSessions in
# packages/daemon/internal/daemon/approved_version_news_transcript_test.go), and this script keeps
# no copy of them: the plan check reads the benchmark's plan (driver_plan), once with no pick, a
# full run's plan (full_plan), and, for a run that picks, once more with the run's pick
# (part_two_plan, which is full_plan for a run that picks nothing). The run's deadline is
# part_two_minutes for each run this run's plan makes, the eight minutes the driver bounds a run at
# and one for its daemon to boot and stop, with part_two_setup_minutes more for what the driver
# builds and starts before its first run: eighteen runs of real sessions took 44 minutes in a
# measured run.
part_two_minutes=9
part_two_setup_minutes=20
# The measurement that retired a step's base arm, as `<word>:<why>`, which the last line prints in
# the step's place (head_only_why): the one fact of part two's steps the driver does not know. The
# driver's plan of a full run makes each such step in the head arm alone, and the plan check fails
# the run, naming the step, where that plan makes a step in the head arm alone that has no entry
# here, or makes the step of an entry here in both arms or not at all (check_plan). Each was
# measured by APPROVED_VERSION_NEWS_STEP=<word> APPROVED_VERSION_NEWS_ARM=base
# APPROVED_VERSION_NEWS_RUNS=10, then ARM=head RUNS=3, alone; that command repeats it, and a base
# arm that passed 3 or more of the 10 cannot be told from the head arm at that base.
#   - a4 drives R4 through the child's own architect, which main's #4203 gives every started
#     child, and the base commit's own prompts tell that architect to tell its workers of a
#     version once its parent approves it. At 26392323 against its merge base, main 578c6abb, the
#     base arm passed 10 of 10 runs (dispatch://INFRA-175/artifact/a4-base10-2639232-txt) and the
#     head arm 3 of 3 (dispatch://INFRA-175/artifact/a4-head3-2639232-txt).
#   - answers tests the rule that a holder whose work a new version makes stale moves back with a
#     reason naming the version and the change, and one whose work it leaves current writes a
#     comment naming the version and why. The daemon's own news task states that rule
#     (holderRule, packages/daemon/internal/workflow/design_news.go), and both arms run the head's
#     daemon, so the base's role prompts need not carry it. At e5081eb2 against its merge base,
#     main 578c6abb, the base arm passed 10 of 10 runs
#     (dispatch://INFRA-175/artifact/answers-base10-e5081eb-txt) and the head arm 3 of 3
#     (dispatch://INFRA-175/artifact/answers-head3-e5081eb-txt).
# off is the one step left whose base arm the full run makes: its base arm passed 0 of 3 runs.
head_only=(
  "a4:the base arm passed 10 of 10 at 578c6abb, main meets R4 since #4203"
  "answers:the base arm passed 10 of 10 at 578c6abb, the daemon's news task states the rule in both arms"
)

# shellcheck source-path=SCRIPTDIR source=lib/transcript.sh
. "$root/scripts/e2e/lib/transcript.sh"
# lib/run-verdict.sh holds the end mark the script's last statement sets (run_end) and the last
# line finish prints from it (run_verdict).
# shellcheck source-path=SCRIPTDIR source=lib/run-verdict.sh
. "$root/scripts/e2e/lib/run-verdict.sh"
# lib/loopback-dsn.sh holds require_loopback_dsn, the guard the arguments check reads
# LEGION_TEST_PG_DSN with.
# shellcheck source-path=SCRIPTDIR source=lib/loopback-dsn.sh
. "$root/scripts/e2e/lib/loopback-dsn.sh"

# Each check prints `== <check>`, what it observed and `ok <check> (<s>s)`; the first that fails
# prints `FAIL <check>: <why>` and ends the run. refuse is for an input of the caller's (exit 2);
# fail is for the box the run is on, or a check that did not hold (exit 1).
begin() { check=$1; check_started=$SECONDS; echo "== $check"; }
note() { echo "   $*"; }
pass() { echo "ok $check ($((SECONDS - check_started))s)"; }
refuse() { echo "FAIL $check: $*" >&2; exit 2; }
fail() { echo "FAIL $check: $*" >&2; exit 1; }

# The run's own state, which finish reads from the EXIT trap.
version_news_evidence=${APPROVED_VERSION_NEWS_EVIDENCE_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/legion-approved-version-news-evidence.XXXXXXXX")}
work=''
prompts_tree=''
full_plan=''
part_two_plan=''
verdict=''
part_two_said=''
check=setup
check_started=$SECONDS
version_news_started=$SECONDS

# finish removes the scratch trees the run made, by their exact paths, and prints the run's last
# line through run_verdict: what the run counted in each arm, or that it did not run, and, for a
# run that picks its runs, MEASURED in place of PASS (pick_said). PASS or MEASURED needs status 0
# and a run that reached its end; a run that ended before that with status 0, as a signal leaves
# the EXIT trap, reads FAIL at the check it stopped in, and says so.
finish() {
  local status=$? said
  for tree in "$work" "$prompts_tree"; do
    [ -z "$tree" ] || rm -rf "$tree"
  done
  said=${part_two_said:-the sessions did not run}
  run_verdict "$status" \
    "approved version news: ${verdict:-PASS} in $((SECONDS - version_news_started))s ($said, evidence: $version_news_evidence)" \
    "approved version news: FAIL at $check after $((SECONDS - version_news_started))s ($said, evidence: $version_news_evidence)"
}

# check_jj fails the run unless the jj the daemon runs is 0.38 or later: LEGION_JJ_PATH where it is
# set, which the daemon reads first (packages/daemon/internal/daemon/tools.go, toolEnv), and the jj
# on PATH otherwise. Each run boots the whole daemon over the tmux runtime with a project, and its
# boot refuses a jj older than that (tools.go, minimumJJ), deep inside a benchmark this run has
# built by then. The release is read as the daemon reads it: `jj <major>.<minor>.<patch>` opening
# the first line of `jj --version`, a build's suffix after it ignored.
check_jj() {
  local jj=${LEGION_JJ_PATH:-jj} said
  said=$("$jj" --version 2>/dev/null) || fail "$jj --version failed, so this run cannot tell its jj is 0.38 or later"
  [[ "$said" =~ ^jj\ ([0-9]+)\.([0-9]+)\.[0-9]+ ]] ||
    fail "$jj --version printed '${said%%$'\n'*}', which names no release, so this run cannot tell its jj is 0.38 or later"
  ((BASH_REMATCH[1] > 0 || BASH_REMATCH[2] >= 38)) ||
    fail "$jj is jj ${BASH_REMATCH[1]}.${BASH_REMATCH[2]}, below 0.38, the oldest jj the daemon's boot starts with"
}

# head_only_why WORD prints the why of WORD's entry of head_only, and answers 1 for a step that
# has none, whose base arm the driver's plan of a full run makes (check_plan).
head_only_why() {
  local entry
  for entry in "${head_only[@]}"; do
    if [ "${entry%%:*}" = "$1" ]; then
      echo "${entry#*:}"
      return 0
    fi
  done
  return 1
}

# A plan is the driver's lines `plan step <word> <name> <arm>…`, one for each step it makes, in
# the order it makes them, and `plan runs <n>`, how many runs each arm makes of each step
# (sessionsPick.plan in the driver's step table). plan_words PLAN prints the word of each step of
# the plan PLAN, in its order, one a line; plan_step PLAN WORD prints the name of WORD's step and
# each arm PLAN makes it in, `<name> <arm>…`, and nothing for a step PLAN does not make; plan_runs
# PLAN prints its count of runs.
plan_words() {
  local tag kind word rest
  while read -r tag kind word rest; do
    [ "$tag $kind" != "plan step" ] || echo "$word"
  done <<<"$1"
}
plan_step() {
  local tag kind word rest
  while read -r tag kind word rest; do
    [ "$tag $kind $word" != "plan step $2" ] || echo "$rest"
  done <<<"$1"
}
plan_runs() {
  local tag kind runs rest
  while read -r tag kind runs rest; do
    [ "$tag $kind" != "plan runs" ] || echo "$runs"
  done <<<"$1"
}

# driver_plan STEP ARM RUNS prints the plan the benchmark makes of the pick that
# APPROVED_VERSION_NEWS_STEP, APPROVED_VERSION_NEWS_ARM and APPROVED_VERSION_NEWS_RUNS name as STEP,
# ARM and RUNS, each empty for none: the `plan …` lines it prints under -part-two-plan, which makes
# no run. The benchmark runs with no LEGION_TEST_PG_DSN, so its package's tests make no database.
# A pick the driver refuses (its line `plan refused: <why>`) is the caller's input, refused with
# exit 2 in the driver's words. A build or a benchmark that fails otherwise, or prints a plan of no
# step, a step of no arm or of an arm other than head and base, or no count of runs, fails the
# run. Its caller reads the plan through a command substitution, whose failure ends the run under
# set -e with the status of the refusal or the failure here.
driver_plan() {
  local out status=0 refused plan tag kind word name arms arm steps=0 runs=''
  out=$(env -u LEGION_TEST_PG_DSN APPROVED_VERSION_NEWS_STEP="$1" APPROVED_VERSION_NEWS_ARM="$2" \
    APPROVED_VERSION_NEWS_RUNS="$3" go -C "$root/packages/daemon" test -count=1 -run '^$' \
    -bench "^$bench\$" -benchtime=1x ./internal/daemon -args -part-two-plan 2>&1) ||
    status=$?
  refused=$(grep -m1 '^plan refused: ' <<<"$out" || true)
  [ -z "$refused" ] || refuse "the driver refuses the pick: ${refused#plan refused: }"
  [ "$status" -eq 0 ] ||
    fail "the benchmark's plan ended with status $status and printed no plan: $(tail -n 5 <<<"$out" | tr '\n' ' ')"
  plan=$(grep '^plan ' <<<"$out" || true)
  while read -r tag kind word name arms; do
    [ -n "$tag" ] || continue
    case "$tag $kind" in
      'plan step')
        steps=$((steps + 1))
        [ -n "$arms" ] || fail "the driver planned $word in no arm: $tag $kind $word $name"
        for arm in $arms; do
          [ "$arm" = head ] || [ "$arm" = base ] ||
            fail "the driver planned $word in the arm '$arm', which is neither head nor base"
        done
        ;;
      'plan runs')
        [[ "$word" =~ ^[1-9][0-9]*$ ]] && [ -z "$name$arms" ] ||
          fail "the driver planned '$word $name $arms' runs, which is no count of runs"
        runs=$word
        ;;
      *) fail "the driver printed a plan line this script does not read: $tag $kind $word $name $arms" ;;
    esac
  done <<<"$plan"
  [ "$steps" -gt 0 ] && [ -n "$runs" ] ||
    fail "the driver printed no plan of steps and runs: $(tail -n 5 <<<"$out" | tr '\n' ' ')"
  echo "$plan"
}

# check_plan fails the run, naming the step, where the driver's plan of a full run (full_plan) and
# head_only disagree: a step the plan makes in the head arm alone that head_only holds no
# measurement for, whose control would be gone with nothing to say why; a step the plan makes in
# both arms that head_only holds a measurement for, whose control the last line would print as
# retired where it can bite; and an entry of head_only for a step the plan does not make.
check_plan() {
  local word name arms entry
  for word in $(plan_words "$full_plan"); do
    read -r name arms <<<"$(plan_step "$full_plan" "$word")"
    if [[ " $arms " == *" base "* ]]; then
      ! head_only_why "$word" >/dev/null ||
        fail "the driver's plan of a full run makes $word in both arms ($arms), and head_only holds a measurement that retired its base arm"
    else
      head_only_why "$word" >/dev/null ||
        fail "the driver's plan of a full run makes $word in the head arm alone ($arms), and head_only holds no measurement that retired its base arm"
    fi
  done
  for entry in "${head_only[@]}"; do
    [ -n "$(plan_step "$full_plan" "${entry%%:*}")" ] ||
      fail "head_only holds a measurement that retired the base arm of ${entry%%:*}, which the driver's plan of a full run does not make"
  done
}

# part_two_deadline prints the run's deadline, a Go duration: part_two_minutes for each run this
# run's plan (part_two_plan) makes, its count of runs in each arm of each step, and
# part_two_setup_minutes more.
part_two_deadline() {
  local word name arms list armed=0
  for word in $(plan_words "$part_two_plan"); do
    read -r name arms <<<"$(plan_step "$part_two_plan" "$word")"
    read -ra list <<<"$arms"
    armed=$((armed + ${#list[@]}))
  done
  echo "$((armed * $(plan_runs "$part_two_plan") * part_two_minutes + part_two_setup_minutes))m"
}

# pick_said prints what a run that picks its runs made of a full run, which opens its part of the
# last line: that it is a partial run; the pick, by each variable it set; each step and arm the
# driver's plan of a full run (full_plan) makes and this run's plan (part_two_plan) does not; and
# this run's count of runs of each arm, where it is not a full run's. The plan check prints it
# once, into picked.
pick_said() {
  local said="a partial run picked by" word name arms arm made left='' runs full
  [ -z "$pick_step" ] || said="$said APPROVED_VERSION_NEWS_STEP=$pick_step"
  [ -z "$pick_arm" ] || said="$said APPROVED_VERSION_NEWS_ARM=$pick_arm"
  [ -z "$pick_runs" ] || said="$said APPROVED_VERSION_NEWS_RUNS=$pick_runs"
  for word in $(plan_words "$full_plan"); do
    read -r name arms <<<"$(plan_step "$full_plan" "$word")"
    read -r name made <<<"$(plan_step "$part_two_plan" "$word")"
    for arm in $arms; do
      [[ " $made " == *" $arm "* ]] || left="${left:+$left, }$word $arm"
    done
  done
  [ -z "$left" ] || said="$said, leaving out $left"
  runs=$(plan_runs "$part_two_plan") full=$(plan_runs "$full_plan")
  [ "$runs" = "$full" ] || said="$said, with $runs run$([ "$runs" -eq 1 ] || echo s) of each arm in place of $full"
  echo "$said"
}

# part_two_step PROOF WORD reads the `run <step> <arm> <n>: <outcome> …` lines of WORD's step in
# the benchmark's output PROOF, for each arm this run's plan (part_two_plan) makes the step in,
# each of which the driver ran as many times as the plan says. A PASS or FAIL run reached the
# step's assertion, and it counts for its arm. A SETUP run stopped before the assertion, and a
# HARNESS run is a fault of the harness before or after it. Neither counts, and each is noted by
# its line. A line of an arm the plan does not make the step in is not read. It sets step_said,
# the step's part of the last line: WORD, each arm's passed runs of those that reached the
# assertion, the runs not counted, and the control's verdict. The control bites only when a base
# run failed at its assertion. A step that runs in the head arm alone has none: a step of
# head_only in a run that picks no arm says why (head_only_why), and a step whose base arm the
# pick left out says so. It also sets head_missed: each head run that did not reach its assertion
# and pass, by its line, each after `; `.
part_two_step() {
  local proof=$1 word=$2 step arms runs arm lines ran passed failed line count counts='' base_ran=0 base_passed=0 base_failed=0 why
  head_missed=''
  read -r step arms <<<"$(plan_step "$part_two_plan" "$word")"
  runs=$(plan_runs "$part_two_plan")
  for arm in $arms; do
    lines=$(grep -E "^run $step $arm [0-9]+: " "$proof" 2>/dev/null || true)
    ran=$(grep -c . <<<"$lines" || true)
    passed=$(grep -cE "^run $step $arm [0-9]+: PASS " <<<"$lines" || true)
    failed=$(grep -cE "^run $step $arm [0-9]+: FAIL: " <<<"$lines" || true)
    line="$step $arm: $passed of $((passed + failed)) runs that reached the assertion passed"
    count="$arm $passed/$((passed + failed))"
    if [ "$ran" -ne $((passed + failed)) ]; then
      line="$line; $((ran - passed - failed)) not counted (SETUP or HARNESS)"
      count="$count with $((ran - passed - failed)) not counted"
    fi
    [ "$ran" -eq "$runs" ] || line="$line ($ran of $runs ran)"
    note "$line"
    counts="${counts:+$counts, }$count"
    while IFS= read -r line; do
      [ -z "$line" ] || note "not counted: $line"
    done < <(grep -vE "^run $step $arm [0-9]+: (PASS |FAIL: )" <<<"$lines" || true)
    if [ "$arm" = head ]; then
      [ "$ran" -eq "$runs" ] || head_missed="$head_missed; $word: $ran of $runs head runs ran"
      while IFS= read -r line; do
        [ -z "$line" ] || head_missed="$head_missed; $line"
      done < <(grep -vE "^run $step head [0-9]+: PASS " <<<"$lines" || true)
    else
      base_ran=$ran base_passed=$passed base_failed=$failed
    fi
  done
  step_said="$word $counts"
  if [[ " $arms " != *" base "* ]]; then
    if [ -z "$pick_arm" ] && why=$(head_only_why "$word"); then
      step_said="$step_said (head only: $why)"
    else
      step_said="$step_said (no control: the pick leaves the base arm out)"
    fi
  elif [ "$base_ran" -eq 0 ]; then
    step_said="$step_said (base did not run)"
  elif [ "$base_failed" -gt 0 ]; then
    step_said="$step_said (control bites)"
  elif [ "$base_passed" -eq 0 ]; then
    step_said="$step_said (no control: no base run reached its assertion)"
  else
    step_said="$step_said (no control: the base arm passed every run that reached its assertion)"
  fi
}

mkdir -p "$version_news_evidence"
transcript_to "$version_news_evidence/transcript.log"
trap finish EXIT

begin arguments
[ $# -eq 0 ] ||
  refuse "this script takes no argument, and was given '$*': APPROVED_VERSION_NEWS_STEP, APPROVED_VERSION_NEWS_ARM and APPROVED_VERSION_NEWS_RUNS pick its runs"
# The pick of the runs, which the driver reads from the same three variables: the plan check reads
# what the pick makes from the driver's plan of it (driver_plan), and the driver refuses a value
# that names no step, no arm or no count. pick is the three together, empty for a run that picks
# none.
pick_step=${APPROVED_VERSION_NEWS_STEP:-} pick_arm=${APPROVED_VERSION_NEWS_ARM:-} pick_runs=${APPROVED_VERSION_NEWS_RUNS:-}
pick=$pick_step$pick_arm$pick_runs
for tool in go git tar python3 bun; do
  command -v "$tool" >/dev/null || fail "$tool is not on PATH"
done
check_jj
[ -n "${LEGION_TEST_PG_DSN:-}" ] ||
  refuse "LEGION_TEST_PG_DSN is required: it names the PostgreSQL the daemon migrates and runs on (postgres://<user>@<host>:<port>/<database>?sslmode=disable). This script starts no database."
# require_loopback_dsn in lib/loopback-dsn.sh says why a caller of the loopback guard clears
# these three.
unset PGHOSTADDR PGSERVICE PGSERVICEFILE
# A caller's own model key reaches nothing the run starts: no build, no Dispatch and no daemon
# holds it, and the driver reads the key file and gives the key to each pane alone.
unset ANTHROPIC_API_KEY
db_host=''
require_loopback_dsn db_host LEGION_TEST_PG_DSN "$LEGION_TEST_PG_DSN" "this run creates and drops databases there" ||
  refuse "$db_host"
note "store LEGION_TEST_PG_DSN on $db_host (not printed)"
[ -n "${LEGION_TEST_OMP:-}" ] ||
  refuse "LEGION_TEST_OMP is required: it names the Oh My Pi every pane runs (an absolute executable path). The run launches real agents and guesses no build"
[ -x "$LEGION_TEST_OMP" ] || refuse "LEGION_TEST_OMP ($LEGION_TEST_OMP) is not an executable file"
[ -n "${ANTHROPIC_BASE_URL:-}" ] || refuse "ANTHROPIC_BASE_URL is required: it names the model gateway the sessions reach"
key_file=${APPROVED_VERSION_NEWS_MODEL_KEY_FILE:-/var/run/model-gateway-key/key}
[ -r "$key_file" ] && [ -s "$key_file" ] ||
  refuse "the model key file $key_file is not a readable file with a key in it: the sessions run their turns on it (APPROVED_VERSION_NEWS_MODEL_KEY_FILE names another)"
base_sha=$(git -C "$root" merge-base HEAD origin/main 2>/dev/null) ||
  refuse "no merge base of HEAD and origin/main, the base the base arm runs against: fetch main"
[ -n "$(git -C "$root" ls-tree --name-only "$base_sha" packages/daemon/internal/prompts/roles)" ] ||
  refuse "the base $base_sha holds no packages/daemon/internal/prompts/roles, so the base arm has no prompts"
note "the base $base_sha's prompts, Oh My Pi $LEGION_TEST_OMP, model key file $key_file (read by the driver, never printed)"
note "evidence: $version_news_evidence"
pass

# The plan is the driver's: the plan check runs the benchmark from this checkout under
# -part-two-plan and reads its plan of a full run and, for a run that picks, of its pick
# (driver_plan) before any session starts, so that a pick the driver refuses costs no run. It
# holds head_only to the plan of a full run (check_plan), and counts the run's deadline from this
# run's plan.
begin plan
picked=''
full_plan=$(driver_plan '' '' '')
part_two_plan=$full_plan
[ -z "$pick" ] || part_two_plan=$(driver_plan "$pick_step" "$pick_arm" "$pick_runs")
note "the driver's plan of a full run:"
while IFS= read -r line; do note "  $line"; done <<<"$full_plan"
if [ -n "$pick" ]; then
  picked=$(pick_said)
  note "the driver's plan of this run, $picked:"
  while IFS= read -r line; do note "  $line"; done <<<"$part_two_plan"
fi
check_plan
deadline=$(part_two_deadline)
note "the run's deadline is $deadline"
pass

# The run: the benchmark once, over this checkout, with the base commit's prompt files extracted
# for its base arm into a scratch directory of their own. Its output and its logs go in a fresh
# part-two.* directory of the evidence directory, so no proof.log an earlier run left there is
# read as this run's, and its temporary files in a work directory removed on any exit. Under -v
# its body prints `--- PASS: <body>` only once it ran to its end; a body that skipped, for a
# prerequisite the driver reads, prints `--- SKIP` and leaves the benchmark passing, so the check
# holds that line too.
begin sessions
prompts_tree=$(mktemp -d "${TMPDIR:-/tmp}/legion-approved-version-news-prompts.XXXXXXXX")
git -C "$root" archive "$base_sha" packages/daemon/internal/prompts/roles packages/daemon/internal/prompts/go |
  tar -x -C "$prompts_tree" || fail "git archive of $base_sha's prompts into $prompts_tree failed"
work=$(mktemp -d "${TMPDIR:-/tmp}/legion-approved-version-news.XXXXXXXX")
part_two=$(mktemp -d "$version_news_evidence/part-two.XXXXXX")
proof=$part_two/proof.log
status=0
APPROVED_VERSION_NEWS_MODEL_KEY_FILE=$key_file APPROVED_VERSION_NEWS_BASE_PROMPTS=$prompts_tree \
  APPROVED_VERSION_NEWS_BASE_COMMIT=$base_sha LEGION_TEST_DAEMON_LOG_DIR=$part_two TMPDIR=$work \
  go -C "$root/packages/daemon" test -count=1 -v -timeout "$deadline" -run '^$' -bench "^$bench\$" \
  -benchtime=1x ./internal/daemon 2>&1 | tee "$proof" || status=$?
note "the benchmark ended with status $status; $proof keeps its output"
pass

# The key's first 8 characters, counted in every file the run kept and printed nowhere: grep reads
# them from a pipe, and only the count of matches leaves it.
begin key-leak
key=$(<"$key_file")
leaks=$({ grep -rFoa -f <(printf '%s\n' "${key:0:8}") "$version_news_evidence" || true; } | wc -l)
unset key
note "the key's first 8 characters occur $leaks times in $version_news_evidence"
[ "$leaks" -eq 0 ] || fail "the model key's first 8 characters are in the evidence directory $leaks times"
pass

# A run that picks its runs is a measurement, a partial run, and never a pass: its part of the
# last line opens with the pick and what it left out of a full run (picked, which the plan check
# made with pick_said), and, once every head run the pick makes reached its assertion and passed,
# its verdict is MEASURED.
begin counts
head_short=''
for word in $(plan_words "$part_two_plan"); do
  part_two_step "$proof" "$word"
  part_two_said="${part_two_said:+$part_two_said; }$step_said"
  head_short="$head_short$head_missed"
done
[ -z "$pick" ] || part_two_said="$picked: $part_two_said"
calls=$(grep -m1 -E '^model calls: ' "$proof" 2>/dev/null || true)
[ -z "$calls" ] || part_two_said="$part_two_said; ${calls%% in all*}"
[ -z "$head_short" ] || fail "the head arm did not reach its assertion and pass in every run: ${head_short#; }"
[ "$status" -eq 0 ] || fail "the benchmark failed (status $status); $proof keeps each run's evidence"
grep -q "^--- PASS: $body " "$proof" ||
  fail "the benchmark's body did not run to its end: it skipped, or the benchmark ran nothing; $proof says why"
[ -z "$pick" ] || verdict=MEASURED
pass
run_end
