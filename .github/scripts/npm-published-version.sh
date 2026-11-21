#!/usr/bin/env bash
# The version npm's registry publishes for one package: release-push.sh's `version` reads it in
# place of a tag when a release line has no tag yet and --first-npm names the package.
#
# Usage: npm-published-version.sh <package>
#
# One `npm view <package> version`, and three outcomes:
#   npm answers       its answer on standard output, npm's own standard error passed through,
#                     exit 0.
#   npm answers E404  the registry has no such package, which is a release line's first release:
#                     0.0.0 on standard output, one line on standard error saying npm has no such
#                     package, exit 0. npm's own error-code line names the code, and only that
#                     line is matched: `npm error code E404` from npm 11, `npm ERR! code E404`
#                     from older npm, the whole line. npm appends a registry body's `error` field
#                     to its message, so a 504 whose body says `npm error code E404` carries that
#                     text inside its message line; it is a 504 and no E404 (dispatch://INFRA-1545,
#                     round 50).
#   anything else     a 5xx, a timeout, a refused connection: npm's standard error passed through,
#                     nothing on standard output, and npm's exit status.
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
# npm's standard error is kept to be read for its error-code line, then passed on. An interrupt,
# such as retry_fetch's `timeout` at its bound, still runs the trap: bash runs EXIT on SIGTERM.
err=$(mktemp)
trap 'rm -f "$err"' EXIT
status=0
version=$(npm view "$package" version 2> "$err") || status=$?
if [ "$status" -ne 0 ] && grep -qE '^npm (error|ERR!) code E404$' "$err"; then
  echo "npm-published-version.sh: npm has no package $package (E404), so its published version is 0.0.0" >&2
  echo 0.0.0
  exit 0
fi
cat "$err" >&2
if [ "$status" -ne 0 ]; then
  exit "$status"
fi
printf '%s\n' "$version"
