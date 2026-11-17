#!/usr/bin/env bash
# Sourced by the `.test.sh` harnesses beside it (release-push, check-bun-version): the counters,
# the per-case reporter, and the two predicates they both spell the same way. Each harness keeps
# its own fixtures — what they share is only how a case is reported and counted.
#
# A harness sources this, runs its cases with `check <description> <true|false>`, and ends with
# `summary <what was tested>`, which prints the tally and exits non-zero if anything failed.
#
# Not executable: `source "$(dirname "${BASH_SOURCE[0]}")/test-lib.sh"`.

PASS=0
FAIL=0

check() { # check DESCRIPTION true|false
  local desc="$1" result="$2"
  if [ "$result" = "true" ]; then
    echo "  PASS: $desc"
    PASS=$((PASS + 1))
  else
    echo "  FAIL: $desc"
    FAIL=$((FAIL + 1))
  fi
}

# is <actual> <expected>: true/false for check.
is() { [ "$1" = "$2" ] && echo true || echo false; }

# contains <text> <regex>: true/false for check; greps the whole text.
#
# A here-string, not a pipe. `printf … | grep -q` makes grep exit at the first match while
# printf is still writing, so printf takes SIGPIPE and, under the harnesses' `set -o pipefail`,
# the pipeline reports 141 and a matching text is reported as a MISS. It is a race, so it shows
# up as one assertion failing in maybe one run of six, a different assertion each time, with
# the case count unchanged — which is what makes it look like a mystery rather than a bug.
# Measured on a 4000-line text with a match on line 1: 300/300 spurious with the pipe, 0/300
# with the here-string.
contains() { grep -q -- "$2" <<<"$1" && echo true || echo false; }

summary() { # summary WHAT-WAS-TESTED
  echo
  echo "---"
  echo "Results: $PASS passed, $FAIL failed"
  if [ "$FAIL" -gt 0 ]; then
    exit 1
  fi
  echo "PASS: $1"
}

# stalled_proxy <directory>: starts, in the background, a proxy that accepts every connection and
# answers none, as a module or toolchain download that stalls meets it, and sets proxy to its
# process and port to the port it listens on. Its port file is <directory>/port. The caller stops
# it (kill "$proxy"). check-ci-filters.test.sh and go-test-step-expansion.test.sh point a go at it.
# shellcheck disable=SC2034 # proxy and port are set for the caller, which reads them
stalled_proxy() {
  python3 -c 'import socket, sys
s = socket.socket()
s.bind(("127.0.0.1", 0))
s.listen(16)
with open(sys.argv[1], "w") as f:
    f.write(str(s.getsockname()[1]))
held = []
while True:
    held.append(s.accept()[0])' "$1/port" &
  proxy=$!
  for _ in $(seq 1 100); do
    [ -s "$1/port" ] && break
    sleep 0.1
  done
  port=$(<"$1/port")
}
