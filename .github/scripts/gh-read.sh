#!/usr/bin/env bash
# The wait the scripts beside this file, and the workflow steps that source it, put in front of a
# GitHub call made with the run's token: gh_wait_reset. It is no retry ladder. The one ladder of
# this repository's CI is retry_fetch, in .github/scripts/ci-retry.sh (dispatch://INFRA-1545, R2),
# and a call that retries runs that ladder through this wait, in one form:
#
#     gh_wait_reset [--stderr <file>] retry_fetch "<what>" <bound> -- gh ...
#
# which .github/scripts/check-download-retry.sh reads as one ladder, as it reads a bare
# retry_fetch, because of the margin below. The two artifact-list reads of
# ci-job-download-retry.yaml are the exception: they drive retry_fetch under a fault, and main
# never ran them through this wait, so they stay bare.
#
# Sourced, never run: `source "$(dirname "${BASH_SOURCE[0]}")/gh-read.sh"`. It sources
# ci-log-guard.sh, the file beside it, as ci-retry.sh does, for the block below.
#
# gh_wait_reset [--stderr <file>] <command>...: runs the command once. When it fails on one of
# GitHub's rate limits, it works out when GitHub will answer again and, when that time plus the
# command's margin (below) falls before the job's limit (or the step's, below), logs one
# ::warning:: line naming the wait and the limit, sleeps until then and runs the command once
# more; when that time has passed by the moment it would sleep (a slow host), it runs the command
# once more at once and logs no wait. When it falls later, or no limit can be read, it runs
# nothing more and fails by name at once, without sleeping. Any other failure is the command's
# own: its stdout, its stderr and its exit status reach the caller as a bare call's would. It
# returns the status of the run it ended on, or 75 when it failed by name: a wait past the limit,
# no limit to wait within, a reset it could not read, or a refusal again after the wait. Every
# line of its own goes to stderr; stdout holds the stdout of the run it ended on and nothing else,
# so a run it repeats leaves no half page in the caller's hands.
#
# The margin is the time this call may still take after the wait. For `retry_fetch <what> <bound>
# -- ...` it is that ladder's whole run, five attempts of <bound> seconds and its sleeps of 10,
# 20, 40 and 80 s, 5 x <bound> + 150 s, plus GH_WAIT_MARGIN's 30 s; for any other command it is
# GH_WAIT_MARGIN alone. So the ladder this runs again never outlives the nearer of the job's and
# the step's limit: it runs it again only when the wait, the whole ladder and GH_WAIT_MARGIN end
# before that limit, with no wait (the budget back already) as with one.
# A STATED LIMIT: the margin does not cover the later calls of the same step, and each of them is
# a ladder of its own. So a wait followed by 5xx answers on those later calls can run the step to
# its `timeout-minutes`, which the runner reports as the step's timeout, never as a pass. Main put
# no time bound on those calls.
# A STATED LIMIT: in the composed form each attempt's log lines, retry_fetch's blocks and gh's
# words in them, go to this helper's file (_gh_read_run) and reach the log only when that run of
# the ladder has ended, before the wait for a refused run and when gh_wait_reset returns for the
# run it ends on. So a step killed at its key in the middle of a ladder shows none of that
# ladder's lines.
#
# Where the caller sent stdout to stderr (`gh_wait_reset ... >&2`, read as
# `[ /dev/fd/1 -ef /dev/fd/2 ]`), the run's stdout is text for the log, and it is written inside
# a stop-commands block of its own (ci-log-guard.sh), as retry_fetch writes such a copy inside its
# attempt's block. retry_fetch cannot do that here: its stdout is this helper's file, and it reads
# that as a caller's capture. Anywhere else stdout is the caller's value, and is copied plain.
#
# A run it repeats leaves its stderr in the log inside a stop-commands block of its own, written
# before the wait: that text is the host's, and a ladder's `::error::` of exhaustion in it must not
# annotate a step whose call the run after the wait still answers. The stderr of the run it ends
# on is printed bare, as a bare call's would be, so a refusal it ends on annotates the step.
#
# --stderr <file> writes the stderr of the run it ended on to <file> instead of stderr, for a
# caller that reads gh's words (a 404 told apart from a refusal); an earlier run's stderr, and
# every line of the helper's own, still reach stderr, where the job log holds them.
#
# The two limits, told apart by the API's own message, which gh prints for a 403 or a 429:
#   * The hourly budget ("API rate limit exceeded for installation ID <n>", or "already
#     exceeded" from GraphQL, which answers a spent budget with HTTP 200 and an error of type
#     RATE_LIMITED, read from gh's stdout as well, where gh prints that answer, and in the
#     composed form from the copy of it retry_fetch prints on stderr, the last attempt's output,
#     since retry_fetch hands no failed attempt's output to stdout). The refusal's
#     x-ratelimit-reset header names its reset, and the
#     helper reads the same reset off GET /rate_limit, which GitHub serves free of the budget it
#     reports ("Accessing this endpoint does not count against your REST API rate limit",
#     docs.github.com, REST API, "Rate limit"), so a command it does not run as gh itself has one
#     too. The core and graphql budgets are read, the two the callers spend, and the latest reset
#     of those at 0 is waited for; neither at 0 means the window turned over since the refusal,
#     and the command runs once more at once.
#   * A secondary rate limit ("You have exceeded a secondary rate limit", or GitHub's older "abuse
#     detection mechanism"), which spends no budget. GitHub's documentation (REST API, "Rate
#     limits for the REST API", "Exceeding the rate limit"): wait the seconds a retry-after header
#     names, and where the answer names none, at least one minute. gh prints no header of an
#     answer but under GH_DEBUG=api, which logs each request and its answer, body and all, from a
#     `* Request at` line to a `* Request took` line, so the helper runs `gh api` under
#     GH_DEBUG=api, reads the answer's headers out of that log, and hands the caller gh's stderr
#     without it. Only a `gh api` without --paginate: one request, whose log ends at the last
#     `took` line, which a line of the body cannot move (_gh_read_run). gh release view, edit and
#     upload send a GraphQL lookup and a REST request together, and a paginated read sends one per
#     page, so a log of theirs holds more than one answer's body (a draft release's name and body,
#     ahead of release-push.sh's leak guard, or a raw page). Every other command, gh's own or a
#     wrapper around it (retry_fetch among them), runs as it is, leaves no headers to read, and
#     its secondary limit takes the one-minute minimum.
#   A STATED LIMIT: _gh_read_refusal decides a rate limit from the text of gh's stderr, which gh
#   prints from the answer's body, and in the composed form no headers are traced, so a 5xx whose
#   body names a rate limit is taken for one: it gets one more ladder, after the wait where it
#   names a secondary limit, and fails by name as a rate limit, exit 75. It never passes wrongly.
#   The same holds in the composed form for the text retry_fetch prints from the last failed
#   attempt of a GraphQL read, the answer gh printed, which can hold a pull request's own text
#   (check-pr-text.sh's reads): a planted "API rate limit exceeded" there costs one more ladder
#   and never a pass. A read of the answer's status and headers in place of its text is the
#   INFRA-3862 lane's (dispatch://INFRA-5224).
#
# Where the limit comes from. GitHub enforces a job's timeout-minutes from the job's start, and
# a step's from the step's, and the runner exposes neither the limit nor the start to a step.
# So both limits are passed in: the job's env sets JOB_TIMEOUT_MINUTES, and a step that carries a
# timeout-minutes of its own sets STEP_TIMEOUT_MINUTES, each the number it stands for, which
# .github/scripts/check-workflow-files.sh holds equal (and it refuses a step with a limit of its
# own that reaches this file and passes none):
#   JOB_TIMEOUT_MINUTES   the job's timeout-minutes, measured from the job's start;
#   STEP_TIMEOUT_MINUTES  optional: the step's own timeout-minutes, measured from the step's.
# and both starts are read off the process tree: GitHub's runner starts one Runner.Worker process
# for each job (actions/runner src/Runner.Listener/JobDispatcher.cs, the worker it spawns per job
# request), before the job's first step, and each step's shell is that process's child. The
# nearest Runner.Worker among this shell's parents is the job's start, and its child on the way
# down is the step's. The earlier of the two limits set is the deadline. With neither set, with
# no Runner.Worker among the parents (a container job, whose steps run in another process
# namespace, or a run outside Actions), the helper has no deadline and does not wait.
#
# Its callers: every GitHub call of cancel-started-act.sh, check-pr-text.sh, find-pr-payload.sh,
# ci-ok-report.sh, ci-ok-reconcile.sh, post-check-run.sh and runner-shutdown.sh, ci-ok-hold.sh's
# run read and `git ls-remote` ref reads, dispatch-browser-run-reads.sh's jobs read, and the
# steps of ci-caches.yaml, dequeued.yaml and main-bypass-check.yaml, each in the form above; and,
# with no ladder, the release calls of release-push.sh, which R2's exception (d) leaves unretried
# in the credentialed publish jobs, and check-daemon-api-contract.sh's reads.

if ! command -v ci_log_block_open > /dev/null 2>&1; then
  if [ -r "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ci-log-guard.sh" ]; then
    # shellcheck source=.github/scripts/ci-log-guard.sh
    . "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/ci-log-guard.sh"
  else
    echo "::error::gh-read.sh: ci_log_block_open is not defined and no ci-log-guard.sh can be read beside this file" >&2
    return 2
  fi
fi

# GH_WAIT_MARGIN, the 30 s of the margin above: for a call with a ladder it follows the ladder's
# 5 x <bound> + 150 s, so the deadline must leave that whole ladder and these 30 s after the wait;
# for a call with no ladder it is the whole margin, the one run included (the deadline check is
# `wake + margin`), where a gh call takes about a second and a paginated read a few. It covers no
# later call of the same step, each a ladder of its own (the header's stated limit on those calls).
GH_WAIT_MARGIN=30

# Every status is taken with `|| status=$?` and every test sits in an `if`, so a caller under
# `set -e` (and inherit_errexit) reaches the decision below rather than leaving at the first
# refused run.
gh_wait_reset() {
  local into='' dir status=0 kind reset=0 seconds named until='' wake now pause limit deadline why
  local failure='' margin=$GH_WAIT_MARGIN ladder='' again token
  if [ "${1:-}" = --stderr ]; then
    into=$2
    shift 2
  fi
  # The ladder's own arithmetic, read off the call as check-download-retry.sh reads it: the bound
  # is the word after <what>, and `--` the word after that. A bound retry_fetch refuses runs no
  # attempt, so it meets no rate limit and needs no margin.
  if [ "${1:-}" = retry_fetch ] && [ "${4:-}" = -- ] && [[ ${3:-} =~ ^[0-9]+$ ]]; then
    ladder=$((5 * 10#$3 + 150))
    margin=$((ladder + GH_WAIT_MARGIN))
    again="one more run of its ladder, 5 x $((10#$3)) + 150 = $ladder s, and $GH_WAIT_MARGIN s after it"
  fi
  dir=$(mktemp -d) || return 1
  _gh_read_run "$dir" "$@" || status=$?
  kind=$(_gh_read_refusal "$status" "$dir/err" "$dir/out")
  if [ -n "$kind" ]; then
    if [ "$kind" = budget ] && ! reset=$(_gh_read_reset 2> "$dir/probe"); then
      failure="GitHub refused this call for its API rate limit, and its rate_limit route did not say when the budget resets ($(head -n 1 "$dir/probe"))"
      reset=0
    fi
    # Read after the rate_limit read, which can take its whole 30 s bound: a clock from before it
    # would sleep that long past the reset, into the margin the deadline check left.
    now=$(date +%s)
    wake=$now
    if [ "$kind" = budget ] && [ "$reset" -gt 0 ]; then
      wake=$((reset + 1 > now ? reset + 1 : now))
      until="the API budget is spent until $(_gh_read_utc "$reset")"
    elif [ "$kind" = secondary ]; then
      seconds=$(awk -F': *' 'tolower($1) == "retry-after" { value = $2 } END { print value }' "$dir/headers")
      seconds=${seconds%$'\r'}
      if [[ $seconds =~ ^[0-9]+$ ]]; then
        named="its retry-after"
      else
        seconds=60 named="GitHub's documented minimum, as the answer named no retry-after"
      fi
      # A second past it, as for a reset: `now` is whole seconds, up to one behind the clock, so
      # now + seconds can fall up to a second short of the wait GitHub asked for.
      wake=$((now + seconds + 1))
      until="GitHub's secondary rate limit asks this call to wait $seconds s ($named), until $(_gh_read_utc "$wake")"
    fi
    # A ladder is held to the limit even where there is nothing to wait for: run again at once, it
    # still takes up to its whole margin.
    if [ -z "$failure" ] && { [ "$wake" -gt "$now" ] || [ -n "$ladder" ]; }; then
      [ -n "$until" ] || until="GitHub refused this call for its API rate limit, and its rate_limit route shows the budget back"
      if ! limit=$(_gh_read_deadline); then
        failure="$until, and $limit, so this step does not wait for it; re-run the job after that time"
      else
        deadline=${limit%% *} why=${limit#* }
        if [ $((wake + margin)) -gt "$deadline" ]; then
          if [ -n "$ladder" ]; then
            failure="$until, and that time with $again ends at $(_gh_read_utc $((wake + margin))), past this $why's limit at $(_gh_read_utc "$deadline"); re-run the job after that time"
          else
            failure="$until, past this $why's limit at $(_gh_read_utc "$deadline"); re-run the job after that time"
          fi
        fi
      fi
    fi
    if [ -z "$failure" ]; then
      # The refused run's words stay in the log, inside a stop-commands block of their own: they
      # are the host's text, and where the command is a ladder they hold its `::error::` of
      # exhaustion, which must not annotate a step whose call the run below may still answer.
      # The run below is the one the caller gets, printed as any run this ends on is, so a
      # refusal it ends on still annotates the step.
      token=$(ci_log_token) || token=
      if [ -n "$token" ]; then
        ci_log_block_open "$token"
        cat "$dir/err" >&2 || true
        ci_log_block_close "$token"
      else
        echo "gh-read.sh: no stop-commands token could be made, so the refused run's words are not printed" >&2
      fi
      now=$(date +%s)
      pause=$((wake > now ? wake - now : 0))
      if [ "$pause" -gt 0 ]; then
        if [ -n "$ladder" ]; then
          echo "::warning::$until; waiting $pause s for it, then $again, within this $why's limit at $(_gh_read_utc "$deadline")" >&2
        else
          echo "::warning::$until; waiting $pause s for it, within this $why's limit at $(_gh_read_utc "$deadline"), then calling once more" >&2
        fi
        sleep "$pause"
      fi
      status=0
      _gh_read_run "$dir" "$@" || status=$?
      if [ -n "$(_gh_read_refusal "$status" "$dir/err" "$dir/out")" ]; then
        if [ "$kind" = budget ] && [ "$reset" -gt 0 ]; then
          failure="the API budget is still spent after the wait for its reset at $(_gh_read_utc "$reset"); re-run the job once GitHub's rate_limit route shows it back"
        elif [ "$kind" = secondary ]; then
          failure="GitHub refused this call for its rate limit again after the wait ($until); re-run the job"
        else
          failure="GitHub refused this call for its API rate limit twice, and its rate_limit route showed neither the core nor the graphql budget at 0 between the two; re-run the job"
        fi
      fi
    fi
  fi
  # The run it ended on: its words, then the helper's own refusal or its stdout.
  if [ -n "$into" ]; then
    cat "$dir/err" > "$into"
  else
    cat "$dir/err" >&2
  fi
  if [ -n "$failure" ]; then
    echo "::error::$failure" >&2
    status=75
  elif [ /dev/fd/1 -ef /dev/fd/2 ] && [ -s "$dir/out" ]; then
    # Text for the log, the host's as much as the command's stderr is: inside a block of its own,
    # whose markers go on stderr with it (the stream they share here). `|| token=` keeps a token
    # that cannot be made from ending a caller under errexit; the output then stays unwritten, and
    # the status says so.
    token=$(ci_log_token) || token=
    if [ -z "$token" ]; then
      echo "::error::gh-read.sh: no stop-commands token could be made, so this call's output is not printed" >&2
      status=2
    else
      ci_log_block_open "$token"
      cat "$dir/out" >&2 || status=$?
      ci_log_block_close "$token"
    fi
  else
    cat "$dir/out" || status=$?
  fi
  rm -rf "$dir"
  return "$status"
}

# _gh_read_run <dir> <command>...: one run, its stdout kept in <dir>/out and its stderr in
# <dir>/err. `gh api` without --paginate, one request, runs under GH_DEBUG=api. Its log of that
# request and its answer runs from the first `* Request at` line to the last `* Request took`
# line, and is left out of <dir>/err whole: the answer's body is printed inside it as it came, so
# a raw body's line can read like any of gh's own markers, and only the last `took` is surely
# gh's (the body precedes it). The answer's headers, from the first `< HTTP/` line to the first
# line after it that is no header, go to <dir>/headers, one `Name: value` a line; the body comes
# after that line, so nothing in it is read as a header. Lines before the request and after its
# end are gh's own and are kept as they came (gh closes a request on a failed connection too,
# then prints its error), and a request gh never closed (gh killed mid-request) ends the log with
# nothing of gh's own after it. Every other command, a paginated `gh api` among them (several
# requests, each page's body inside the log), runs as it is, its stderr whole in <dir>/err.
_gh_read_run() {
  local dir=$1 status=0 arg traced=''
  shift
  # Emptied here, not by the awk below, which opens its file only when gh printed a line of its
  # own: a repeated run that printed none would otherwise hand on the refused run's words.
  : > "$dir/headers"
  : > "$dir/err"
  if [ "$1" = gh ] && [ "${2:-}" = api ]; then
    traced=yes
    for arg in "$@"; do
      [ "$arg" != --paginate ] || traced=''
    done
  fi
  if [ -z "$traced" ]; then
    "$@" > "$dir/out" 2> "$dir/err" || status=$?
    return "$status"
  fi
  GH_DEBUG=api "$@" > "$dir/out" 2> "$dir/trace" || status=$?
  awk -v err="$dir/err" -v headers="$dir/headers" '
    NR == FNR {
      if (!first && /^\* Request at /) first = FNR
      if (/^\* Request took /) last = FNR
      next
    }
    !first || FNR < first || (last >= first && FNR > last) { print > err; next }
    !answer && /^< HTTP\// { answer = 1; next }
    answer == 1 && /^< / { printf "%s\n", substr($0, 3) > headers; next }
    answer == 1 { answer = 2 }
  ' "$dir/trace" "$dir/trace"
  return "$status"
}

# _gh_read_refusal <status> <stderr file> <stdout file>: `budget` when a failed run's stderr
# carries GitHub's refusal for the hourly API budget, as gh prints the API's message for REST ("API
# rate limit exceeded for ...") and GraphQL ("API rate limit already exceeded for ..."), or when a
# GraphQL answer with an error of type RATE_LIMITED, which GraphQL answers with HTTP 200 and whose
# words gh prints as they come, is its stdout or a line of its stderr. The stderr line is the
# composed form's: retry_fetch puts no failed attempt's output on stdout, and copies the last
# attempt's to stderr, inside that attempt's block, after a line of its own and before the block's
# close, which opens with a newline, so gh's one-line answer is a line there (cut at 4096 bytes,
# far past the length of such an answer). `secondary` for a secondary rate limit ("You have
# exceeded a secondary rate limit", or the older "abuse detection mechanism"); nothing otherwise.
_gh_read_refusal() {
  [ "$1" -ne 0 ] || return 0
  if grep -qiE 'rate limit (already )?exceeded' "$2" ||
    jq -e 'try any(.errors[]?; .type == "RATE_LIMITED") catch false' "$3" > /dev/null 2>&1 ||
    jq -nRe 'any(inputs | try fromjson catch null; try any(.errors[]?; .type == "RATE_LIMITED") catch false)' \
      "$2" > /dev/null 2>&1; then
    echo budget
  elif grep -qiE 'secondary rate limit|abuse detection mechanism' "$2"; then
    echo secondary
  fi
}

# _gh_read_reset: prints the latest reset (epoch seconds) of the core and graphql budgets that
# GET /rate_limit reports at 0, or 0 when neither is. Fails with gh's words on stderr when the
# route does not answer, bounded at the 30 s a `gh api` read of this repository's CI carries.
_gh_read_reset() {
  local reset
  reset=$(timeout 30 gh api rate_limit \
    --jq '[.resources.core, .resources.graphql | select(. != null and .remaining == 0) | .reset] | max // 0') ||
    return 1
  if ! [[ $reset =~ ^[0-9]+$ ]]; then
    echo "it answered '$reset', not an epoch" >&2
    return 1
  fi
  echo "$reset"
}

# _gh_read_deadline: prints `<epoch> job` or `<epoch> step`, the earlier of the limits
# JOB_TIMEOUT_MINUTES and STEP_TIMEOUT_MINUTES set and which of the two it is; or prints why there
# is none and fails.
_gh_read_deadline() {
  local job=${JOB_TIMEOUT_MINUTES:-} step=${STEP_TIMEOUT_MINUTES:-} name value
  local pid=$$ child='' line comm worker='' boot ticks job_end step_end
  local -a fields
  if [ -z "$job" ] && [ -z "$step" ]; then
    echo "this step names no time limit to wait within (JOB_TIMEOUT_MINUTES is not set)"
    return 1
  fi
  for name in JOB_TIMEOUT_MINUTES STEP_TIMEOUT_MINUTES; do
    value=${!name:-}
    if [ -n "$value" ] && ! [[ $value =~ ^[0-9]+$ ]]; then
      echo "$name is '$value', not a whole number of minutes"
      return 1
    fi
  done
  # /proc/<pid>/stat: `<pid> (<comm>) <state> <ppid> ...`, the start time 20 fields after the
  # comm, in clock ticks since boot. The comm can hold spaces and parentheses, so the fields are
  # read after its last `)`.
  while [ "$pid" -gt 1 ] && read -r line 2> /dev/null < "/proc/$pid/stat"; do
    comm=${line#*\(}
    comm=${comm%)*}
    read -ra fields <<< "${line##*) }"
    if [ "$comm" = Runner.Worker ]; then
      worker=$pid
      break
    fi
    child=$pid
    pid=${fields[1]}
  done
  if [ -z "$worker" ] || [ -z "$child" ]; then
    echo "this step's start cannot be read: no Runner.Worker process is among its parents"
    return 1
  fi
  boot=$(awk '$1 == "btime" { print $2 }' /proc/stat)
  ticks=$(getconf CLK_TCK)
  read -ra fields <<< "$(_gh_read_stat_after_comm "$worker")"
  job_end=$((boot + fields[19] / ticks + ${job:-0} * 60))
  read -ra fields <<< "$(_gh_read_stat_after_comm "$child")"
  step_end=$((boot + fields[19] / ticks + ${step:-0} * 60))
  if [ -n "$job" ] && { [ -z "$step" ] || [ "$job_end" -le "$step_end" ]; }; then
    echo "$job_end job"
  else
    echo "$step_end step"
  fi
}

# _gh_read_stat_after_comm <pid>: the fields of /proc/<pid>/stat after its comm.
_gh_read_stat_after_comm() {
  local line
  read -r line < "/proc/$1/stat"
  echo "${line##*) }"
}

_gh_read_utc() { date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ; }
