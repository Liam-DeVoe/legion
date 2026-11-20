#!/usr/bin/env bash
# Installs the Ubuntu packages a CI step needs and the GitHub-hosted runner image lacks, with every
# apt call bounded: tmux, zsh, the PostgreSQL client.
#
# Those steps ran `sudo apt-get update && sudo apt-get install ...` with nothing around it, which
# waits as long as a mirror makes it wait. In job 136053067213 of run 44665276896 `apt-get update`
# printed its last line at 03:06:14, read nothing more for nine and a half minutes, and was
# cancelled with the job at its 10-minute timeout: no test ran, and the log said only "The
# operation was canceled". apt's own inactivity timeout did not end it, so here `timeout` ends each
# call, and the step retries by count and fails naming what it was doing after the last attempt.
#
# Usage: apt-install.sh <package>...
#
# A package the image carries already - dpkg-query reads it `installed` - takes no apt call at all:
# the x64 ubuntu-24.04 image carries tmux (job 136078635450 of run 44673073324 printed `tmux is
# already the newest version (3.4-1ubuntu0.2)`), so a step that needs only tmux fetches nothing.
# For the packages it lacks:
#
# Each attempt runs `apt-get update` and then `apt-get install -y --no-install-recommends
# <package>...`, each call under `timeout`, so a call that hangs is ended - and with it the
# acquire methods apt starts, which share its process group - and counts as a failed attempt. Two
# attempts, 10 s apart; after the second the script prints an ::error:: line naming the job, the
# packages, the attempts and how the last call ended, and exits 1.
#
# apt's own knobs are passed on every call, so a runner image of any age acquires the same way.
# An x64 runner image points the archive at a list of mirrors, azure.archive.ubuntu.com first, and
# apt tries an item on a mirror Acquire::Retries more times, each waiting up to
# Acquire::http::Timeout for a byte, before it moves to the next mirror; it does that for every
# item, one item at a time. Measured in the pinned ubuntu:24.04 image with that mirror list and a
# first mirror that accepts the connection and never answers, `apt-get update` from no index at
# all took more than 20 minutes with apt's defaults, 19 minutes with the image's own values (one
# retry, 15 s, actions/runner-images' images/ubuntu/scripts/build/configure-apt.sh), 571 s with no
# retry and 15 s, and 192 s with no retry and 5 s, after which `apt-get install tmux` took 34 s. A
# healthy mirror answers within a second, so on x64 no retry and 5 s it is. An arm64 runner reads
# ports.ubuntu.com, which that list does not cover, so a retry is its only way past a refused item,
# and it keeps the image's values for it: three retries, 30 s.
#
# A call's bound is 240 s, above those 192 s; a runner starts with its indexes and fetches fewer.
# `timeout` sends TERM at the bound and KILL 10 s later. Two attempts, so two updates that hang
# end by name by 510 s, inside the 10 minutes the shortest job that runs this step is given.
#
# Inputs, from the environment:
#   GITHUB_JOB          the job, as Actions sets it, named in the error.
#   APT_CALL_SECONDS    a call's bound, 240 by default; the tests set 1.
#   APT_RETRY_SECONDS   the wait before the second attempt, 10 by default; the tests set 0.
#
# Its tests are in apt-install.test.sh, which ci.yaml's test job runs.
set -euo pipefail
shopt -s inherit_errexit

if [ "$#" -eq 0 ]; then
  echo "::error::apt-install.sh needs the packages to install" >&2
  exit 2
fi

readonly ATTEMPTS=2
call_seconds=${APT_CALL_SECONDS:-240}
retry_seconds=${APT_RETRY_SECONDS:-10}
case $(uname -m) in
  x86_64) retries=0 timeout=5 ;;
  *) retries=3 timeout=30 ;;
esac
options=(-o "Acquire::Retries=$retries" -o "Acquire::http::Timeout=$timeout" -o "Acquire::https::Timeout=$timeout")
packages=()
for package in "$@"; do
  carried=$(dpkg-query -W -f='${db:Status-Status} ${Version}' "$package" 2> /dev/null || true)
  if [ "${carried%% *}" = installed ]; then
    echo "$package ${carried#* } is on the runner image already: no apt call for it"
  else
    packages+=("$package")
  fi
done
[ "${#packages[@]}" -gt 0 ] || exit 0

# bounded <call> <apt-get argument>...: runs one apt-get call under its bound; on a failure sets
# `ended` to how it ended and returns non-zero.
bounded() {
  local call=$1 status=0
  shift
  sudo timeout -k 10 "$call_seconds" apt-get "${options[@]}" "$@" || status=$?
  case $status in
    0) return 0 ;;
    124 | 137) ended="apt-get $call hung past its $call_seconds s bound and was killed" ;;
    *) ended="apt-get $call exited $status" ;;
  esac
  return 1
}

ended=""
for ((attempt = 1; attempt <= ATTEMPTS; attempt++)); do
  if bounded update update && bounded install install -y --no-install-recommends "${packages[@]}"; then
    exit 0
  fi
  if [ "$attempt" -lt "$ATTEMPTS" ]; then
    wait_seconds=$((attempt * retry_seconds))
    echo "attempt $attempt of $ATTEMPTS to install ${packages[*]} failed: $ended; trying again in $wait_seconds s" >&2
    sleep "$wait_seconds"
  fi
done
echo "::error::job ${GITHUB_JOB:-?} could not install ${packages[*]} with apt-get: $ATTEMPTS attempts of \`apt-get update\` and \`apt-get install\`, each call bounded at $call_seconds s, all failed; the last: $ended" >&2
exit 1
