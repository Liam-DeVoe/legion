#!/usr/bin/env bash
# network-install.sh <kind> -- <command> [argument…]
# network-install.sh --bounds
#
# Runs one install that waits on the network, bounded, retried and failed by name. A required job
# of .github/workflows/ci.yaml runs every such install through this script, and
# .github/scripts/check-ci-filters.sh (rule 22, in .github/scripts/ci_workflow_rules.py) refuses
# one that does not. `--bounds` prints the table below, one line per kind (`<kind> <bound s>
# <attempts> <s before SIGKILL> <pause s>`), which rule 22 reads to hold each job's limit to the
# worst case of the installs it runs.
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
#     below), then SIGKILL KILL_AFTER seconds after the SIGTERM where the command has not ended by
#     then.
#   * An attempt that timeout(1) ended at its bound is the wait this script exists for, so it is
#     tried again, at most ATTEMPTS attempts in all, with PAUSE seconds between them. timeout(1)
#     exits 124 when it sent SIGTERM and 137 when it had to send SIGKILL; a command that exits 124
#     or 137 itself (an OOM kill is 137) does so before the bound, so the time an attempt took is
#     what says which it was.
#   * Any other exit is the command's own, and it is not tried again: the client of every kind
#     below exits with one status for an answer it could not read and for a fault of the change
#     under test, so no status says "network". Measured on 2026-11-20: bun 1.4.4 exits 1 both
#     when the registry refuses the connection and when bun.lock disagrees with package.json;
#     docker pull exits 1 both for a refused connection and for a digest the registry does not
#     hold; apt-get update exits 0 when the mirror refuses it (it warns and keeps the lists it
#     had), and apt-get install then exits 100, as it does for a package no index lists. A
#     second attempt of such a failure fails the same way, and an error that blamed the network
#     would send its reader to re-run a change that needs a fix.
#   * Every attempt prints one line: its number, how it ended and how long it took. When none
#     succeeded, one `::error::` names the kind, the command and how each attempt ended, and says
#     whether the network or the command itself is what failed; the script exits 1.
#
# THE KINDS. A kind's bound is what the installs it guards took at worst, with room: the slowest
# green run of those installs in the 20 newest merge-group runs of ci.yaml (44633135919 to
# 44668680855, 2026-11-19 22:55Z to 2026-11-20 03:34Z), times two, rounded up to the next
# minute. Two, because an install that is slow and not stuck must still pass; no more, because a
# stuck one costs ATTEMPTS bounds of its job's limit, and the jobs that run these have limits of
# 10 to 70 minutes. A kind's line names what it measured.
#   apt         apt-get update and apt-get install of Ubuntu packages. Slowest of 54 green steps
#               (median 12 s): 25 s, claude-envoy's "Install zsh" in job 135990304720 of run
#               44646446447, whose update read 15.2 MB of package lists. 2 × 25 s is 50 s, so 60 s
#               an attempt.
#   bun         bun install and bun add from the npm registry. Slowest of 499 green steps (median
#               6 s): 55 s, proof-sdk-offline's cold install behind its fault proxy in job
#               135990304816 of the same run. 2 × 55 s is 110 s, so 120 s an attempt. Bun's own
#               registry client does end a request that gets no answer, but only after minutes and
#               with retries of its own: an install pointed at a registry that accepts and never
#               answers had not ended after 20 minutes, longer than every job that installs with it
#               may run.
#   docker      docker pull of an image from ghcr.io. Slowest of 127 green steps: 41 s, the visual
#               job's "Pull the pinned Playwright copy" in job 136044109917. 2 × 41 s is 82 s, so
#               120 s an attempt. docker gives up after 40 s on a registry that sends no headers,
#               but against one that sends them and then one byte every 5 s a pull had not ended
#               after 900 s: that wait for the body has no bound of docker's own.
#   playwright  Playwright's download of its Chromium (`bunx playwright install chromium`).
#               Slowest of 64 green steps: 10 s, the docs job's step in job 136019959029. 2 × 10 s
#               is 20 s, so 60 s an attempt. Playwright 1.64.1's own timeout is a socket's idle
#               time, 30 s, which a server that sends one byte every 5 s never reaches: such a
#               download had not ended after 900 s.
#   A go command that names a fetch (`go mod download`, `go get`, `go install <module>@<version>`)
#   has no kind yet: no required job named one in those 20 runs, so no run measures its bound. The
#   change that adds the first such step adds its line here, measured from that step's own runs;
#   pull request #4434's fetch step is the one in sight. mise needs no kind: its own 30 s bound on
#   each request covers the body too, so a stalled `mise install` ends by itself (rule 22's
#   docstring gives the measurement).
#
# TEST SEAM. NETWORK_INSTALL_BOUND_SECONDS, NETWORK_INSTALL_KILL_AFTER_SECONDS and
# NETWORK_INSTALL_PAUSE_SECONDS replace the kind's bound, the 15 s before SIGKILL and the pause,
# for .github/scripts/network-install.test.sh alone, so its cases end in seconds. No workflow sets
# them, and `--bounds` prints the table's own values whatever they say.
#
# Run from anywhere: .github/scripts/network-install.sh apt -- sudo apt-get update
# Its tests: .github/scripts/network-install.test.sh, in ci.yaml's test job.
set -euo pipefail

ATTEMPTS=3
DEFAULT_PAUSE=10
DEFAULT_KILL_AFTER=15
KINDS=(apt bun docker playwright)

# bound_of <kind>: the kind's bound per attempt in seconds, from THE KINDS above, or nothing.
bound_of() {
  case "$1" in
    apt) echo 60 ;;
    bun) echo 120 ;;
    docker) echo 120 ;;
    playwright) echo 60 ;;
  esac
}

die() {
  echo "::error::network-install: $*" >&2
  exit 1
}

if [ "$#" -eq 1 ] && [ "$1" = --bounds ]; then
  for kind in "${KINDS[@]}"; do
    echo "$kind $(bound_of "$kind") $ATTEMPTS $DEFAULT_KILL_AFTER $DEFAULT_PAUSE"
  done
  exit 0
fi

[ "$#" -ge 3 ] && [ "$2" = "--" ] ||
  die "usage: network-install.sh <kind> -- <command> [argument…], and this call gave: $*"
kind=$1
shift 2

bound=$(bound_of "$kind")
[ -n "$bound" ] ||
  die "'$kind' is no kind this script bounds; the kinds are: ${KINDS[*]}. A new kind is a line of the table above with its own measured bound"
bound=${NETWORK_INSTALL_BOUND_SECONDS:-$bound}
PAUSE=${NETWORK_INSTALL_PAUSE_SECONDS:-$DEFAULT_PAUSE}
KILL_AFTER=${NETWORK_INSTALL_KILL_AFTER_SECONDS:-$DEFAULT_KILL_AFTER}

command_text=$*
endings=()
for attempt in $(seq 1 "$ATTEMPTS"); do
  started=$(date +%s)
  status=0
  timeout --kill-after="$KILL_AFTER" "$bound" "$@" || status=$?
  took=$(($(date +%s) - started))
  if [ "$status" -eq 0 ]; then
    echo "network-install $kind: attempt $attempt of $ATTEMPTS finished in ${took} s: $command_text"
    exit 0
  elif [ "$status" -eq 124 ] && [ "$took" -ge "$bound" ]; then
    ending="attempt $attempt timed out after ${bound} s"
  elif [ "$status" -eq 137 ] && [ "$took" -ge $((bound + KILL_AFTER)) ]; then
    ending="attempt $attempt was killed ${KILL_AFTER} s after its ${bound} s bound, since it ignored SIGTERM"
  else
    ending="attempt $attempt exited $status after ${took} s, before its ${bound} s bound"
    echo "network-install $kind: $ending: $command_text"
    endings+=("$ending")
    summary=$(printf '%s; ' "${endings[@]}")
    die "$kind install \`$command_text\` failed (${summary%; }). Exit $status is the command's own, not the wait on the network this script bounds, so it was not tried again and is no network fault: its own error is in its output above."
  fi
  echo "network-install $kind: $ending: $command_text"
  endings+=("$ending")
  if [ "$attempt" -lt "$ATTEMPTS" ]; then
    sleep "$PAUSE"
  fi
done

summary=$(printf '%s; ' "${endings[@]}")
die "$kind install \`$command_text\` did not finish (${summary%; }). Each attempt waited on the network past its bound of ${bound} s, so what failed is the network or the server it reads, not the change under test."
