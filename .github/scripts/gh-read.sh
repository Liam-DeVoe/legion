#!/usr/bin/env bash
# The one retry ladder the scripts beside this file use for a GitHub call: the jobs list of
# ci-ok-report.sh, the check-run post of post-check-run.sh, the check-run list and each PATCH
# of ci-ok-reconcile.sh, and the jobs read of dispatch-browser-run-reads.sh, which bounds each
# attempt with `timeout 30`. GitHub refuses a call now and then — a 502, a rate limit — and a
# single refusal must decide nothing here: ci-ok is the check main's ruleset requires, the
# scripts that post it act on what one API call answered, and the browser lane's merge job
# reads its own attempt's jobs through this ladder.
#
# Sourced, never run: `source "$(dirname "${BASH_SOURCE[0]}")/gh-read.sh"`.
#
# gh_read <command>...: runs the command, and on a failure runs it again twice, GH_RETRY_SECONDS
# (10 by default; the tests set 0) and then twice as many seconds apart. It prints the stdout of
# the attempt that succeeded and nothing else, so a `--paginate` read that failed halfway leaves
# no half page in the caller's hands; every attempt's stderr goes to the caller's stderr, where
# the job log holds it. It returns 0 for the attempt that succeeded and non-zero when all three
# were refused, which is the caller's to answer: a refused jobs list leaves ci-ok-report.sh with
# no result to name for the jobs that have not ended, a refused post leaves no ci-ok at all, and
# a refused PATCH leaves a stale ci-ok on the head.
#
# GH_READ_WAIT_CAP_SECONDS, 0 or unset by default, is a caller's opt-in to waiting out GitHub's
# rate limit, and that ladder is gh_read_waiting's below. At 0 gh_read is the ladder above and
# nothing else, which is what every caller but dispatch-browser-run-reads.sh's jobs read runs.
gh_read() {
  local attempt output
  if [ "${GH_READ_WAIT_CAP_SECONDS:-0}" != 0 ]; then
    gh_read_waiting "$@"
    return
  fi
  for attempt in 1 2 3; do
    if output=$("$@"); then
      printf '%s' "$output"
      return 0
    fi
    [ "$attempt" -eq 3 ] || sleep $((attempt * ${GH_RETRY_SECONDS:-10}))
  done
  return 1
}

# gh_read_waiting <command>...: gh_read's ladder where the caller set GH_READ_WAIT_CAP_SECONDS,
# a whole number of seconds above 0. It reads each attempt's stderr, so it holds that stderr in a
# file until the attempt ends and then puts it on the caller's stderr, in the same order; stdout
# is what gh_read prints. Three kinds of refusal are answered apart, read from the last
# `HTTP <n>` gh printed and the words of its message:
#
# - GitHub's rate limit: HTTP 403 or 429 with a message naming a rate limit, as gh prints the
#   body's message (`gh: API rate limit exceeded for installation. ... (HTTP 403)`, the 17:22Z
#   2026-11-21 refusal of job 136922567868 of run 44926842882, and the 02:21Z 2026-11-22 one of
#   job 137064876909 of run 44974625499). gh prints no response header, and `gh api -i` would put
#   the headers on the stdout the caller reads, so the time GitHub names is read from
#   `gh api rate_limit`, whose answer costs nothing of the limit, as x-ratelimit-reset (core's
#   `reset`) and x-ratelimit-remaining (core's `remaining`): the REST limit every caller's route
#   spends. Where remaining is 0, the ladder prints one line naming that header, the time (UTC)
#   and the cap, waits until GH_READ_RESET_MARGIN seconds past the reset, and asks again; such a
#   wait takes none of the three attempts. A wait that would take the ladder's waits past the cap
#   is not made: the ladder says so, naming the refusal and the reset, and fails. A reset whose
#   GH_READ_RESET_MARGIN seconds past it have already gone by names no time to wait for, so the
#   refusal keeps the ladder's own waits and a line says so: a wait of its own there would ask
#   again once a second until the cap. A rate limit with remaining above 0 is the secondary
#   limit, whose `retry-after` header gh does not show, so it keeps the ladder's own waits and a
#   line says so.
# - Any other HTTP 403 - a permission refusal, a token without the scope - fails at once, with a
#   line that says so: asking again is refused the same way.
# - Anything else - a 5xx, a connection refused, the caller's own bound ending the attempt -
#   takes the ladder's 10 s and 20 s waits, as at cap 0.
#
# The probe is bounded by `timeout 30`, as the jobs read is, and a probe that fails or answers
# no reset leaves the refusal on the ladder's own waits.
GH_READ_RESET_MARGIN=3
gh_read_waiting() {
  local cap=$GH_READ_WAIT_CAP_SECONDS refusals=0 waited=0 err output status remaining reset now
  local seconds
  case $cap in
    '' | *[!0-9]*)
      echo "gh_read: GH_READ_WAIT_CAP_SECONDS is '$cap', not a whole number of seconds" >&2
      return 1
      ;;
  esac
  err=$(mktemp)
  while :; do
    if output=$("$@" 2>"$err"); then
      cat "$err" >&2
      rm -f "$err"
      printf '%s' "$output"
      return 0
    fi
    cat "$err" >&2
    status=$(grep -o 'HTTP [0-9][0-9][0-9]' "$err" | tail -n 1 || true)
    if [[ $status =~ ^HTTP\ (403|429)$ ]] && grep -qi 'rate limit' "$err"; then
      remaining='' reset=''
      read -r remaining reset < <(timeout 30 gh api rate_limit \
        --jq '.resources.core | "\(.remaining) \(.reset)"' || true) || true
      if [ "$remaining" = 0 ] && [[ $reset =~ ^[0-9]+$ ]]; then
        now=$(date +%s)
        seconds=$((reset + GH_READ_RESET_MARGIN - now))
        if [ "$seconds" -lt 1 ]; then
          echo "gh_read: GitHub's rate limit refused this call ($status), and gh api rate_limit" \
            "reads x-ratelimit-remaining 0 and x-ratelimit-reset $reset" \
            "($(date -u -d "@$reset" +%Y-%m-%dT%H:%M:%SZ)), whose $GH_READ_RESET_MARGIN s past it" \
            "have already gone by, so it names no time to wait for and the ladder keeps its own" \
            "waits" >&2
        elif [ $((waited + seconds)) -gt "$cap" ]; then
          echo "gh_read: GitHub's rate limit refused this call ($status), and its reset," \
            "x-ratelimit-reset $reset ($(date -u -d "@$reset" +%Y-%m-%dT%H:%M:%SZ)) with" \
            "x-ratelimit-remaining 0 as gh api rate_limit reads them, is $seconds s away, past" \
            "the cap of $cap s (GH_READ_WAIT_CAP_SECONDS) with the $waited s already waited, so" \
            "the ladder does not wait for it" >&2
          rm -f "$err"
          return 1
        else
          echo "gh_read: GitHub's rate limit refused this call ($status); gh api rate_limit reads" \
            "x-ratelimit-remaining 0 and x-ratelimit-reset $reset, so the ladder waits $seconds s," \
            "until $(date -u -d "@$((now + seconds))" +%Y-%m-%dT%H:%M:%SZ), $GH_READ_RESET_MARGIN" \
            "s past that reset, and asks again; the cap is $cap s (GH_READ_WAIT_CAP_SECONDS)," \
            "$waited s of it waited before this" >&2
          sleep "$seconds"
          waited=$((waited + seconds))
          continue
        fi
      elif [[ $remaining =~ ^[1-9][0-9]*$ ]]; then
        echo "gh_read: GitHub's rate limit refused this call ($status), and gh api rate_limit" \
          "reads x-ratelimit-remaining '$remaining', not 0: a secondary limit, whose retry-after" \
          "gh does not show, so the ladder keeps its own waits" >&2
      else
        echo "gh_read: GitHub's rate limit refused this call ($status), and gh api rate_limit" \
          "gave no reset to wait for, so the ladder keeps its own waits" >&2
      fi
    elif [ "$status" = "HTTP 403" ]; then
      echo "gh_read: GitHub refused this call with HTTP 403 and named no rate limit, a refusal" \
        "asking again is refused the same way, so the ladder does not ask again" >&2
      rm -f "$err"
      return 1
    fi
    refusals=$((refusals + 1))
    if [ "$refusals" -eq 3 ]; then
      rm -f "$err"
      return 1
    fi
    sleep $((refusals * ${GH_RETRY_SECONDS:-10}))
  done
}
