#!/bin/sh
# fetch.sh <stage> <seconds> <url> [curl argument…]
#
# One download with curl, bounded, retried and failed by name: the one form every curl download of
# packages/daemon/docker/worker.Dockerfile and .github/docker/browser.Dockerfile takes. Each RUN
# that downloads bind-mounts this file into /usr/local/bin and calls it, so a fix to the form is
# made here once and reaches every one of them. The curl arguments after the address are curl's
# own (`-o <file>` for a download, `-I -o /dev/null` for a check that the address answers), and
# <stage> names the part of the build that fetches (a stage, or what it installs: tools, toolchain,
# gcloud, notices, postgres) in the failure line. Any checksum check is the caller's, after the
# fetch.
#
# WHY. On 2026-11-23 a single 503 from raw.githubusercontent.com failed the worker image's build
# in job 137719519443: its notices RUN read each file with a bare `curl -fsSL`, which tries once.
# The image's downloads then each carried a form of their own, pasted into five RUNs and written
# out a sixth time for the PostgreSQL key, and the browser image's key had a third, so a fix to one
# reached none of the others.
#
# WHAT IT DOES.
#   * Each attempt runs under curl's --max-time <seconds>, which bounds the attempt's whole transfer,
#     its body included, so a host that answers and then all but stops is cut at the bound.
#   * Any failure is tried again, twice (--retry 2 --retry-all-errors): a 429, a 5xx, a 404, a
#     refused or reset connection and a stall the bound cut each get three attempts. curl pauses
#     1 s and then 2 s between them, or what the server's Retry-After asks.
#   * No attempt starts once twice the bound has passed since the first (--retry-max-time), and a
#     Retry-After that would wait past that ends the retries at once. So a fetch ends inside three
#     bounds and curl's two pauses, and a job limit counts each fetch at 3 × <seconds> + 3 s.
#   * A retry truncates the file -o names and fetches it whole, so no partial body survives into
#     the file a checksum then reads.
#   * When no attempt succeeded, curl's own error comes first, then one line names the stage, the
#     address, the bound and curl's exit status:
#       <stage>: <url> failed after up to 3 attempts of <seconds> s (curl exit <status>)
#     and the script exits 1, which fails the RUN under `set -eu` or an `&&` chain.
#   * It is a POSIX sh script that needs only curl, so a RUN runs it on Debian's dash as on Ubuntu's.
#
# Run from anywhere: .github/scripts/fetch.sh tools 300 https://example.com/a.tar.gz -o a.tar.gz
# Its tests: .github/scripts/fetch.test.sh, in ci.yaml's test job.
set -eu

if [ "$#" -lt 3 ]; then
  echo "::error::fetch: usage: fetch.sh <stage> <seconds> <url> [curl argument…], and this call gave: $*" >&2
  exit 2
fi
stage=$1
bound=$2
url=$3
shift 3
case $bound in
  '' | *[!0-9]* | 0*)
    echo "::error::fetch: $stage: the bound '$bound' is no whole number of seconds above 0, so $url is not fetched" >&2
    exit 2
    ;;
esac

status=0
curl -fsSL --max-time "$bound" --retry 2 --retry-all-errors --retry-max-time "$((bound * 2))" "$@" "$url" || status=$?
if [ "$status" -ne 0 ]; then
  echo "$stage: $url failed after up to 3 attempts of $bound s (curl exit $status)" >&2
  exit 1
fi
