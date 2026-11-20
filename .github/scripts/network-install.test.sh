#!/usr/bin/env bash
# Tests for network-install.sh: an install that waits on the network runs under a bound, is
# tried a fixed number of times, and fails by name; a command's own failure is not tried again and
# is not blamed on the network.
#
# Most cases run the real script with the test seam set (NETWORK_INSTALL_BOUND_SECONDS 2,
# NETWORK_INSTALL_KILL_AFTER_SECONDS 1, NETWORK_INSTALL_PAUSE_SECONDS 0), so a hung attempt costs
# about two seconds and not the kind's own bound. The last two cases unset the seam and observe
# the real defaults: a stand-in timeout(1) first on PATH writes down the arguments the script hands
# it and runs the real one, and a stand-in sleep writes down the pause. The stand-in commands are
# small scripts in this harness's own temporary directory: one that never ends, one that ignores
# SIGTERM, one that hangs once and then passes, and ones that exit by themselves.
#
# Each case waits for the script's own exit, under a deadline of its own (DEADLINE): a script that
# never ends (a bound or a kill lost) fails that case by name when the deadline passes, rather than
# holding the test job until its limit. The deadline guards against a hang alone; how long a case
# took is no assertion, so a slow runner cannot fail a correct script.
#
# Run from anywhere: .github/scripts/network-install.test.sh
# CI runs it in ci.yaml's test job.
set -euo pipefail
shopt -s inherit_errexit

script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
script="$script_dir/network-install.sh"
work=$(mktemp -d)
real_timeout=$(type -P timeout)
real_sleep=$(type -P sleep)
# The pids of the stand-ins that may outlive a case (one that hangs, or one that ignores SIGTERM
# after its script was cut at the deadline); each is a process this harness started, and each is
# killed only while /proc still names it the stand-in's own sleep.
pids="$work/pids"
: > "$pids"
stop_standins() {
  local pid
  while read -r pid; do
    if [ "$(tr '\0' ' ' < "/proc/$pid/cmdline" 2> /dev/null)" = "$real_sleep 1000 " ]; then
      kill -KILL "$pid" 2> /dev/null || true
    fi
  done < "$pids"
  : > "$pids"
}
trap 'stop_standins; rm -rf "$work"' EXIT

# shellcheck source=.github/scripts/test-lib.sh
source "$script_dir/test-lib.sh"

# seam_on and seam_off set and unset the test seam; every case but the last two runs with it on.
seam_on() { export NETWORK_INSTALL_BOUND_SECONDS=2 NETWORK_INSTALL_KILL_AFTER_SECONDS=1 NETWORK_INSTALL_PAUSE_SECONDS=0; }
seam_off() { unset NETWORK_INSTALL_BOUND_SECONDS NETWORK_INSTALL_KILL_AFTER_SECONDS NETWORK_INSTALL_PAUSE_SECONDS; }
seam_on
# Three attempts of 2 s each, killed 1 s after SIGTERM, take 9 s; the deadline leaves room for a
# loaded runner and still fails a hang in a minute.
DEADLINE=60

# run_install <case> <argument…>: the script with these arguments, waited for under DEADLINE;
# `out` is what it printed and `status` its exit. A script still running at the deadline fails the
# case by name.
run_install() {
  local case=$1
  shift
  set +e
  "$real_timeout" --kill-after=5 "$DEADLINE" "$script" "$@" > "$work/out" 2>&1
  status=$?
  set -e
  out=$(<"$work/out")
  check "$case: the script ended before the case's ${DEADLINE} s deadline" \
    "$(is "$([ "$status" -ne 124 ] && [ "$status" -ne 137 ] && echo yes)" yes)"
  stop_standins
}

# A stand-in that appends one line to its count file each time it starts, then does what its
# mode says: `hang` sleeps without end, `stubborn` ignores SIGTERM and sleeps, `hang-once` hangs
# the first time and passes the second, `fail` exits 3, `own124` and `own137` exit 124 and are
# killed by SIGKILL at once (as an install that exits 124 itself, or is killed by the kernel), `ok`
# exits 0. A stand-in that sleeps writes its pid first and sleeps in that same process.
standin="$work/standin"
cat > "$standin" <<SH
#!/usr/bin/env bash
echo start >> "\$1"
case "\$2" in
  hang) echo \$\$ >> "$pids"; exec "$real_sleep" 1000 ;;
  stubborn) echo \$\$ >> "$pids"; trap '' TERM; exec "$real_sleep" 1000 ;;
  hang-once) [ "\$(wc -l < "\$1")" -ge 2 ] && exit 0; echo \$\$ >> "$pids"; exec "$real_sleep" 1000 ;;
  fail) exit 3 ;;
  own124) exit 124 ;;
  own137) kill -KILL \$\$ ;;
  ok) exit 0 ;;
esac
SH
chmod +x "$standin"
count() { wc -l < "$1" | tr -d ' '; }

echo "case: an install that finishes"
run_install "finishes" apt -- "$standin" "$work/ok.count" ok
check "exits 0" "$(is "$status" 0)"
check "ran once" "$(is "$(count "$work/ok.count")" 1)"
check "says which attempt finished" "$(contains "$out" "^network-install apt: attempt 1 of 3 finished in [0-9]* s: $standin")"

echo "case: an install that never ends is cut at its bound, tried three times and named"
run_install "never ends" apt -- "$standin" "$work/hang.count" hang
check "exits 1" "$(is "$status" 1)"
check "ran three times" "$(is "$(count "$work/hang.count")" 3)"
check "says each attempt timed out" "$(contains "$out" "^network-install apt: attempt 3 timed out after 2 s: $standin")"
check "fails by name" \
  "$(contains "$out" "^::error::network-install: apt install \`$standin $work/hang.count hang\` did not finish (attempt 1 timed out after 2 s; attempt 2 timed out after 2 s; attempt 3 timed out after 2 s)")"
check "says the network failed" "$(contains "$out" "what failed is the network or the server it reads, not the change under test")"

echo "case: an install that ignores SIGTERM is killed"
run_install "ignores SIGTERM" apt -- "$standin" "$work/stubborn.count" stubborn
check "exits 1" "$(is "$status" 1)"
check "says it was killed" "$(contains "$out" "attempt 1 was killed 1 s after its 2 s bound, since it ignored SIGTERM")"
check "and tried it three times" "$(is "$(count "$work/stubborn.count")" 3)"

echo "case: an install that hangs once and then passes"
run_install "hangs once" apt -- "$standin" "$work/hang-once.count" hang-once
check "exits 0" "$(is "$status" 0)"
check "ran twice" "$(is "$(count "$work/hang-once.count")" 2)"
check "says the first attempt timed out" "$(contains "$out" "^network-install apt: attempt 1 timed out after 2 s")"
check "says the second finished" "$(contains "$out" "^network-install apt: attempt 2 of 3 finished")"

echo "case: an install that fails by itself is not tried again, and the network is not blamed"
run_install "fails by itself" bun -- "$standin" "$work/fail.count" fail
check "exits 1" "$(is "$status" 1)"
check "ran once" "$(is "$(count "$work/fail.count")" 1)"
check "names its own exit" \
  "$(contains "$out" "^::error::network-install: bun install \`$standin $work/fail.count fail\` failed (attempt 1 exited 3 after [0-9]* s, before its 2 s bound). Exit 3 is the command's own")"
check "says it is no network fault" "$(contains "$out" "is no network fault: its own error is in its output above")"
check "does not blame the network" "$(is "$(contains "$out" "what failed is the network")" false)"

echo "case: a command's own exit 124 or 137, before the bound, is neither a timeout nor a kill"
run_install "exits 124 itself" apt -- "$standin" "$work/own124.count" own124
check "exits 1" "$(is "$status" 1)"
check "ran once" "$(is "$(count "$work/own124.count")" 1)"
check "names exit 124 as the command's" "$(contains "$out" "attempt 1 exited 124 after [0-9]* s, before its 2 s bound")"
run_install "killed by the kernel" apt -- "$standin" "$work/own137.count" own137
check "exits 1" "$(is "$status" 1)"
check "ran once" "$(is "$(count "$work/own137.count")" 1)"
check "names exit 137 as the command's" "$(contains "$out" "attempt 1 exited 137 after [0-9]* s, before its 2 s bound")"

echo "case: a command that is not there is not tried again"
run_install "not there" apt -- "$work/no-such-command"
check "exits 1" "$(is "$status" 1)"
check "tried once" "$(is "$(grep -c '^network-install apt: attempt' <<<"$out")" 1)"
check "says it exited 127" "$(contains "$out" "attempt 1 exited 127")"

echo "case: a kind the script does not bound, and a call without --"
run_install "unknown kind" pip -- true
check "exits 1 for the kind" "$(is "$status" 1)"
check "names the kind" "$(contains "$out" "'pip' is no kind this script bounds; the kinds are: apt bun docker playwright")"
run_install "no --" apt true
check "exits 1 without --" "$(is "$status" 1)"
check "says the usage" "$(contains "$out" "usage: network-install.sh <kind> -- <command>")"

# A stand-in timeout(1) and a stand-in sleep, first on PATH for the two cases below alone: each
# writes down the arguments the script hands it, and the timeout then runs the real one.
stubs="$work/stubs"
mkdir "$stubs"
printf '#!/usr/bin/env bash\necho "$*" >> "%s"\nexec "%s" "$@"\n' "$work/timeout.args" "$real_timeout" > "$stubs/timeout"
printf '#!/usr/bin/env bash\necho "$*" >> "%s"\n' "$work/sleep.args" > "$stubs/sleep"
chmod +x "$stubs/timeout" "$stubs/sleep"

echo "case: the real bound of each kind, and the real wait for SIGKILL, with the seam unset"
expected="apt 60
bun 120
docker 120
playwright 60"
while read -r kind bound; do
  : > "$work/timeout.args"
  seam_off
  PATH="$stubs:$PATH" run_install "the real $kind bound" "$kind" -- "$standin" "$work/real-$kind.count" ok
  seam_on
  check "$kind waits $bound s an attempt, then 15 s for SIGTERM to end it" \
    "$(is "$(<"$work/timeout.args")" "--kill-after=15 $bound $standin $work/real-$kind.count ok")"
done <<<"$expected"
check "--bounds prints that table, with three attempts, the 15 s and the 10 s pause" \
  "$(is "$(seam_off && "$script" --bounds)" $'apt 60 3 15 10\nbun 120 3 15 10\ndocker 120 3 15 10\nplaywright 60 3 15 10')"

echo "case: the real pause between attempts, with the seam unset but for the bound"
: > "$work/sleep.args"
: > "$work/timeout.args"
seam_off
export NETWORK_INSTALL_BOUND_SECONDS=2
PATH="$stubs:$PATH" run_install "the real pause" apt -- "$standin" "$work/pause.count" hang
seam_on
check "exits 1" "$(is "$status" 1)"
check "pauses 10 s between attempts, twice" "$(is "$(<"$work/sleep.args")" $'10\n10')"
check "waits 15 s for SIGTERM to end each attempt" "$(is "$(sort -u "$work/timeout.args")" "--kill-after=15 2 $standin $work/pause.count hang")"

summary "network-install.sh"
