#!/usr/bin/env bash
# Tests for network-install.sh: an install that waits on the network runs under a bound, is
# tried a fixed number of times, and fails by name.
#
# Each case runs the real script with the test seam set (NETWORK_INSTALL_BOUND_SECONDS 2,
# NETWORK_INSTALL_KILL_AFTER_SECONDS 1, NETWORK_INSTALL_PAUSE_SECONDS 0), so a hung attempt costs
# about two seconds and not the kind's own bound. The stand-in commands are small scripts in this
# harness's own temporary directory: one that never ends, one that ignores SIGTERM, one that fails
# once and then passes, and one that counts its attempts.
#
# Run from anywhere: .github/scripts/network-install.test.sh
# CI runs it in ci.yaml's test job.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
script="$script_dir/network-install.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

export NETWORK_INSTALL_BOUND_SECONDS=2 NETWORK_INSTALL_KILL_AFTER_SECONDS=1 NETWORK_INSTALL_PAUSE_SECONDS=0

# run_install <argument…>: the script with these arguments; `out` is what it printed, `status` its
# exit and `took` the whole run's seconds.
run_install() {
  local started
  started=$(date +%s)
  set +e
  out=$("$script" "$@" 2>&1)
  status=$?
  set -e
  took=$(($(date +%s) - started))
}

# A stand-in that appends one line to its count file each time it starts, then does what its
# mode says: `hang` sleeps without end, `stubborn` ignores SIGTERM and sleeps, `flaky` fails the
# first time and passes the second, `fail` exits 3, `ok` exits 0.
standin="$work/standin"
cat > "$standin" <<'SH'
#!/usr/bin/env bash
echo start >> "$1"
case "$2" in
  hang) exec sleep 1000 ;;
  stubborn) trap '' TERM; sleep 1000 ;;
  flaky) [ "$(wc -l < "$1")" -ge 2 ] && exit 0; exit 1 ;;
  fail) exit 3 ;;
  ok) exit 0 ;;
esac
SH
chmod +x "$standin"
count() { wc -l < "$1" | tr -d ' '; }

echo "case: an install that finishes"
run_install apt -- "$standin" "$work/ok.count" ok
check "exits 0" "$(is "$status" 0)"
check "ran once" "$(is "$(count "$work/ok.count")" 1)"
check "says which attempt finished" "$(contains "$out" "^network-install apt: attempt 1 of 3 finished in [0-9]* s: $standin")"

echo "case: an install that never ends is cut at its bound, tried three times and named"
run_install apt -- "$standin" "$work/hang.count" hang
check "exits 1" "$(is "$status" 1)"
check "ran three times" "$(is "$(count "$work/hang.count")" 3)"
check "ended within three bounds and a margin" "$(is "$([ "$took" -le 12 ] && echo yes)" yes)"
check "says each attempt timed out" "$(contains "$out" "^network-install apt: attempt 3 timed out after 2 s: $standin")"
check "fails by name" \
  "$(contains "$out" "^::error::network-install: apt install \`$standin $work/hang.count hang\` did not finish (attempt 1 timed out after 2 s; attempt 2 timed out after 2 s; attempt 3 timed out after 2 s)")"
check "says what failed" "$(contains "$out" "what failed is the network or the server it reads, not the change under test")"

echo "case: an install that ignores SIGTERM is killed"
run_install apt -- "$standin" "$work/stubborn.count" stubborn
check "exits 1" "$(is "$status" 1)"
check "says it was killed" "$(contains "$out" "attempt 1 was killed 1 s after its 2 s bound, since it ignored SIGTERM")"
check "and tried it three times" "$(is "$(count "$work/stubborn.count")" 3)"

echo "case: an install that fails once and then passes"
run_install apt -- "$standin" "$work/flaky.count" flaky
check "exits 0" "$(is "$status" 0)"
check "ran twice" "$(is "$(count "$work/flaky.count")" 2)"
check "says the first attempt's exit" "$(contains "$out" "^network-install apt: attempt 1 exited 1 after [0-9]* s")"
check "says the second finished" "$(contains "$out" "^network-install apt: attempt 2 of 3 finished")"

echo "case: an install that keeps failing"
run_install apt -- "$standin" "$work/fail.count" fail
check "exits 1" "$(is "$status" 1)"
check "ran three times" "$(is "$(count "$work/fail.count")" 3)"
check "names every exit" "$(contains "$out" "(attempt 1 exited 3 after [0-9]* s; attempt 2 exited 3 after [0-9]* s; attempt 3 exited 3 after [0-9]* s)")"

echo "case: a command that is not there is not tried again"
run_install apt -- "$work/no-such-command"
check "exits 1" "$(is "$status" 1)"
check "tried once" "$(is "$(grep -c '^network-install apt: attempt' <<<"$out")" 1)"
check "says it exited 127" "$(contains "$out" "attempt 1 exited 127")"

echo "case: a kind the script does not bound, and a call without --"
run_install pip -- true
check "exits 1 for the kind" "$(is "$status" 1)"
check "names the kind" "$(contains "$out" "'pip' is no kind this script bounds; the kinds are: apt, bun")"
run_install apt true
check "exits 1 without --" "$(is "$status" 1)"
check "says the usage" "$(contains "$out" "usage: network-install.sh <kind> -- <command>")"

echo "case: the real bounds, with the seam unset"
bound_of() { sed -n "s/^  $1) bound=\\([0-9]*\\) ;;\$/\\1/p" "$script"; }
check "apt waits 60 s an attempt" "$(is "$(bound_of apt)" 60)"
check "bun waits 120 s an attempt" "$(is "$(bound_of bun)" 120)"

summary "network-install.sh"
