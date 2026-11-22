#!/usr/bin/env bash
# The version npm's registry publishes for one package: release-push.sh's `version` reads it in
# place of a tag when a release line has no tag yet and --first-npm names the package.
#
# Usage: npm-published-version.sh <package>
#
# One `npm view <package> version --json`, and three outcomes:
#   npm answers       the version, read as the JSON string npm prints on standard output, npm's
#                     own standard error passed through, exit 0.
#   npm answers E404  the registry has no such package, which is a release line's first release:
#                     0.0.0 on standard output, one line on standard error saying npm has no such
#                     package, exit 0.
#   anything else     a 5xx, a timeout, a refused connection: npm's standard error passed through,
#                     nothing on standard output, and npm's exit status.
#
# The code is read from the JSON error object npm prints on standard output with --json, its
# `error.code`, which npm sets from the registry's HTTP status (E404 for a 404, E504 for a 504)
# or from its own failure (FETCH_ERROR for a timeout): measured with npm 11.19.1 against a local
# registry and the real one (dispatch://INFRA-1545, round 52). No text a registry writes is read.
# npm copies a registry body's `error` field into its messages, `summary` here and every line it
# writes on standard error, so a 504 whose body holds a newline and `npm error code E404` puts an
# `npm error code E404` line on standard error; a match on those lines read it as no such package.
#
# It is a program, and E404 is a success here, because release-push.sh runs it under retry_fetch
# (.github/scripts/ci-retry.sh): that helper runs its command under `timeout`, which runs a
# program and cannot run a shell function, and it retries every failure. An E404 is the
# registry's answer and not a fault, so it must succeed on the first attempt, with no retry and
# no `::error::`; every other failure is left to the retry.
#
# release-push.test.sh's `version` cases run it, against a stub npm first on PATH.
set -euo pipefail

package=${1:?usage: npm-published-version.sh <package>}
# npm's standard error is kept, then passed on. An interrupt, such as retry_fetch's `timeout` at
# its bound, still runs the trap: bash runs EXIT on SIGTERM.
err=$(mktemp)
trap 'rm -f "$err"' EXIT
status=0
answer=$(npm view "$package" version --json 2> "$err") || status=$?
if [ "$status" -ne 0 ]; then
  code=$(jq -r '.error.code?' <<< "$answer" 2> /dev/null) || code=''
  if [ "$code" = E404 ]; then
    echo "npm-published-version.sh: npm has no package $package (E404), so its published version is 0.0.0" >&2
    echo 0.0.0
    exit 0
  fi
  cat "$err" >&2
  exit "$status"
fi
cat "$err" >&2
version=$(jq -r 'strings' <<< "$answer" 2> /dev/null) || version=''
if [ -z "$version" ]; then
  echo "npm-published-version.sh: npm answered no version of $package: $answer" >&2
  exit 1
fi
printf '%s\n' "$version"
