#!/usr/bin/env bash
# network-install.sh <kind> -- <command> [argument…]
#
# Runs one install that waits on the network, bounded, retried and failed by name. A required job
# of .github/workflows/ci.yaml runs every such install through this script, and
# .github/scripts/check-ci-filters.sh (rule 21, in .github/scripts/ci_workflow_rules.py) refuses
# one that does not.
#
# WHY. On 20 November 2026 the daemon-go job of run 44665181416 waited in its "Install tmux" step,
# `sudo apt-get update && sudo apt-get install -y --no-install-recommends tmux`, from 03:05:59Z
# until the job's 70-minute limit cancelled it at 04:14:57Z. Its last line was `Get:5
# https://archive.ubuntu.com/ubuntu noble-security InRelease [126 kB]`: the mirror had sent the
# answer's headers and then all but stopped. apt's own `Acquire::http::Timeout` is a wait for the
# next byte, so a mirror that keeps sending a byte now and then is never timed out, and the job
# failed for a cause outside the change it tested. A required job that waits on the network
# therefore waits under a bound of its own.
#
# WHAT IT DOES.
#   * Each attempt runs the command under timeout(1) for the kind's bound per attempt (the table
#     below), then SIGKILL 15 s after the SIGTERM where the command has not ended by then.
#   * At most ATTEMPTS attempts, with PAUSE seconds between them. A command that exits 126 or 127
#     (it cannot run, or is not there) or that timeout(1) itself refuses (125) is not tried again,
#     since no second attempt changes that.
#   * Every attempt prints one line: its number, how it ended and how long it took. When none
#     succeeded, one `::error::` names the kind, the command and how each attempt ended, and the
#     script exits 1.
#
# THE KINDS. A kind's bound is what the installs it guards took at worst, with room: the slowest
# green run of those installs in the 20 newest merge-group runs of ci.yaml (44633135919 to
# 44668680855, 2026-11-19 22:55Z to 2026-11-20 03:34Z), times two, rounded up to the next
# minute. Two, because an install that is slow and not stuck must still pass; no more, because a
# stuck one costs ATTEMPTS bounds of its job's limit, and the jobs that run these have limits of
# 10 to 70 minutes. A kind's line names what it measured.
#   apt   apt-get update and apt-get install of Ubuntu packages. Slowest of 54 green steps (median
#         12 s): 25 s, claude-envoy's "Install zsh" in job 135990304720 of run 44646446447, whose
#         update read 15.2 MB of package lists. 2 × 25 s is 50 s, so 60 s an attempt.
#   bun   bun install and bun add from the npm registry. Slowest of 499 green steps (median
#         6 s): 55 s, proof-sdk-offline's cold install behind its fault proxy in job 135990304816
#         of the same run. 2 × 55 s is 110 s, so 120 s an attempt. Bun's own registry client
#         does end a request that gets no answer, but only after minutes and with retries of its
#         own: an install pointed at a registry that accepts and never answers had not ended
#         after 20 minutes, longer than every job that installs with it may run.
#   A go command that names a fetch (`go mod download`, `go get`, `go install <module>@<version>`)
#   has no kind yet: no required job named one in those 20 runs, so no run measures its bound. The
#   change that adds the first such step adds its line here, measured from that step's own runs;
#   pull request #4434's fetch step is the one in sight.
#
# TEST SEAM. NETWORK_INSTALL_BOUND_SECONDS, NETWORK_INSTALL_KILL_AFTER_SECONDS and
# NETWORK_INSTALL_PAUSE_SECONDS replace the kind's bound, the 15 s before SIGKILL and the pause,
# for .github/scripts/network-install.test.sh alone, so its cases end in seconds. No workflow sets
# them.
#
# Run from anywhere: .github/scripts/network-install.sh apt -- sudo apt-get update
# Its tests: .github/scripts/network-install.test.sh, in ci.yaml's test job.
set -euo pipefail

ATTEMPTS=3
PAUSE=${NETWORK_INSTALL_PAUSE_SECONDS:-10}
KILL_AFTER=${NETWORK_INSTALL_KILL_AFTER_SECONDS:-15}

die() {
  echo "::error::network-install: $*" >&2
  exit 1
}

[ "$#" -ge 3 ] && [ "$2" = "--" ] ||
  die "usage: network-install.sh <kind> -- <command> [argument…], and this call gave: $*"
kind=$1
shift 2

case "$kind" in
  apt) bound=60 ;;
  bun) bound=120 ;;
  *) die "'$kind' is no kind this script bounds; the kinds are: apt, bun. A new kind is a line of the table above with its own measured bound" ;;
esac
bound=${NETWORK_INSTALL_BOUND_SECONDS:-$bound}

command_text=$*
endings=()
for attempt in $(seq 1 "$ATTEMPTS"); do
  started=$(date +%s)
  status=0
  timeout --kill-after="$KILL_AFTER" "$bound" "$@" || status=$?
  took=$(($(date +%s) - started))
  case "$status" in
    0)
      echo "network-install $kind: attempt $attempt of $ATTEMPTS finished in ${took} s: $command_text"
      exit 0
      ;;
    124) ending="attempt $attempt timed out after ${bound} s" ;;
    137) ending="attempt $attempt was killed ${KILL_AFTER} s after its ${bound} s bound, since it ignored SIGTERM" ;;
    *) ending="attempt $attempt exited $status after ${took} s" ;;
  esac
  echo "network-install $kind: $ending: $command_text"
  endings+=("$ending")
  case "$status" in
    125 | 126 | 127) break ;;
  esac
  if [ "$attempt" -lt "$ATTEMPTS" ]; then
    sleep "$PAUSE"
  fi
done

summary=$(printf '%s; ' "${endings[@]}")
die "$kind install \`$command_text\` did not finish (${summary%; }). It waits on the network, under a bound of ${bound} s an attempt, so what failed is the network or the server it reads, not the change under test."
