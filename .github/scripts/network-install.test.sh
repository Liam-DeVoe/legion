#!/usr/bin/env bash
# Tests for network-install.sh: an install that waits on the network runs under a bound, is tried
# a fixed number of times, and fails by name; an attempt whose output names a network fault is
# tried again and blamed on the network; a command's own failure is not tried again and is not
# blamed on the network; and the command's own streams pass through.
#
# The cases run the real script with the test seam set: NETWORK_INSTALL_KILL_AFTER_SECONDS 1,
# NETWORK_INSTALL_PAUSE_SECONDS 0, and a NETWORK_INSTALL_BOUND_SECONDS derived from what the case
# waits for:
#   ENDS (30 s)  a stand-in that ends by itself (it finishes, fails, exits 124 or 137, names a
#                network fault, or is not there): far above any time it takes, so a slow or paused
#                runner cannot cut it, and the case still costs no time;
#   HANGS (2 s)  a stand-in that never ends, or ignores SIGTERM: cut at once, so the case costs
#                seconds;
#   ONCE (10 s)  a stand-in that hangs on its first attempt and ends at once on its second: its
#                one bound must cut the first and leave the second room.
# The last two cases unset the seam and observe the real defaults: a stand-in timeout(1) first on
# PATH writes down the arguments the script hands it and runs the real one, and a stand-in sleep
# writes down each wait. One case puts a stand-in date first on PATH that pauses 3 s on the
# script's second reading of the clock in an attempt, after the command has ended: whether the
# bound ended the attempt must come from the attempt, not from that reading. The stand-in commands
# are small scripts in this harness's own temporary directory.
#
# Each case waits for the script's own exit, under a deadline of its own (DEADLINE): a script that
# never ends (a bound or a kill lost) fails that case by name when the deadline passes, rather than
# holding the test job until its limit. The deadline guards against a hang alone; how long a case
# took is no assertion.
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
real_date=$(type -P date)
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

ENDS=30
HANGS=2
ONCE=10
# seam <bound> sets the test seam for the cases after it, seam_off unsets it.
seam() { export NETWORK_INSTALL_BOUND_SECONDS=$1 NETWORK_INSTALL_KILL_AFTER_SECONDS=1 NETWORK_INSTALL_PAUSE_SECONDS=0; }
seam_off() { unset NETWORK_INSTALL_BOUND_SECONDS NETWORK_INSTALL_KILL_AFTER_SECONDS NETWORK_INSTALL_PAUSE_SECONDS; }
# The longest case, hang-once, takes one ONCE bound and its kill, 11 s; a case still running at the
# deadline has lost a bound or a kill.
DEADLINE=60

# run_install <case> <argument…>: the script with these arguments, waited for under DEADLINE;
# `out` is what it printed on both streams, `stdout` what it printed on its standard output alone,
# and `status` its exit. A script still running at the deadline fails the case by name.
run_install() {
  local case=$1
  shift
  set +e
  "$real_timeout" --kill-after=5 "$DEADLINE" "$script" "$@" > "$work/stdout" 2> "$work/stderr" < "${INPUT:-/dev/null}"
  status=$?
  set -e
  stdout=$(<"$work/stdout")
  out=$(cat "$work/stdout" "$work/stderr")
  check "$case: the script ended before the case's ${DEADLINE} s deadline" \
    "$(is "$([ "$status" -ne 124 ] && [ "$status" -ne 137 ] && echo yes)" yes)"
  stop_standins
}

# A stand-in that appends one line to its count file each time it starts, then does what its
# mode says: `hang` sleeps without end, `stubborn` ignores SIGTERM and sleeps, `hang-once` hangs
# the first time and passes the second, `fail` exits 3, `own124` exits 124 and `own137` is killed
# by SIGKILL at once (as an install that exits 124 itself, or is killed by the kernel), `refused`
# prints Bun 1.4.4's error for a registry that refused the connection and exits 1, `stale` prints
# its error for a frozen lockfile that disagrees and exits 1, `streams` prints a line on each
# stream, `stdin` prints the line it reads, and `ok` exits 0. A stand-in that sleeps writes its pid
# first and sleeps in that same process.
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
  refused) echo "error: ConnectionRefused downloading package manifest left-pad" >&2; exit 1 ;;
  stale) echo "error: lockfile had changes, but lockfile is frozen" >&2; exit 1 ;;
  streams) echo "the command's own output"; echo "the command's own error" >&2 ;;
  stdin) read -r line; echo "read: \$line" ;;
  ok) exit 0 ;;
esac
SH
chmod +x "$standin"
count() { wc -l < "$1" | tr -d ' '; }

seam "$ENDS"
echo "case: an install that finishes"
run_install "finishes" apt -- "$standin" "$work/ok.count" ok
check "exits 0" "$(is "$status" 0)"
check "ran once" "$(is "$(count "$work/ok.count")" 1)"
check "says which attempt finished" "$(contains "$out" "^network-install apt: attempt 1 of 3 finished in [0-9]* s: $standin")"

echo "case: the command's own standard output, standard error and standard input are its own"
run_install "streams" apt -- "$standin" "$work/streams.count" streams
check "its standard output is the command's alone" "$(is "$stdout" "the command's own output")"
check "its standard error holds the command's error" "$(contains "$(<"$work/stderr")" "^the command's own error$")"
printf 'from the caller\n' > "$work/input"
INPUT=$work/input run_install "stdin" apt -- "$standin" "$work/stdin.count" stdin
check "the command reads the caller's standard input" "$(is "$stdout" "read: from the caller")"

seam "$HANGS"
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

seam "$ONCE"
echo "case: an install that hangs once and then passes"
run_install "hangs once" apt -- "$standin" "$work/hang-once.count" hang-once
check "exits 0" "$(is "$status" 0)"
check "ran twice" "$(is "$(count "$work/hang-once.count")" 2)"
check "says the first attempt timed out" "$(contains "$out" "^network-install apt: attempt 1 timed out after 10 s")"
check "says the second finished" "$(contains "$out" "^network-install apt: attempt 2 of 3 finished")"

seam "$ENDS"
echo "case: an install that fails by itself is not tried again, and the network is not blamed"
run_install "fails by itself" bun -- "$standin" "$work/fail.count" fail
check "exits 1" "$(is "$status" 1)"
check "ran once" "$(is "$(count "$work/fail.count")" 1)"
check "names its own exit" \
  "$(contains "$out" "^::error::network-install: bun install \`$standin $work/fail.count fail\` failed (attempt 1 exited 3 after [0-9]* s, and its 30 s bound did not end it). Exit 3 is the command's own")"
check "says why it is the command's own" "$(contains "$out" "its output names no network fault and its bound did not end it, so it was not tried again")"
check "does not blame the network" "$(is "$(contains "$out" "what failed is the network")" false)"

echo "case: an install whose output names a network fault is tried again and blamed on the network"
run_install "refused" bun -- "$standin" "$work/refused.count" refused
check "exits 1" "$(is "$status" 1)"
check "ran three times" "$(is "$(count "$work/refused.count")" 3)"
check "names the fault each attempt printed" \
  "$(contains "$out" "^network-install bun: attempt 1 exited 1 after [0-9]* s, and its output names a network fault (ConnectionRefused)")"
check "says the network failed" "$(contains "$out" "what failed is the network or the server it reads, not the change under test")"
check "does not call it the command's own" "$(is "$(contains "$out" "is the command's own")" false)"

echo "case: an install whose own error names no network fault is not tried again"
run_install "stale lockfile" bun -- "$standin" "$work/stale.count" stale
check "exits 1" "$(is "$status" 1)"
check "ran once" "$(is "$(count "$work/stale.count")" 1)"
check "says it is the command's own" "$(contains "$out" "Exit 1 is the command's own")"

echo "case: a command's own exit 124 or 137, before the bound, is neither a timeout nor a kill"
run_install "exits 124 itself" apt -- "$standin" "$work/own124.count" own124
check "exits 1" "$(is "$status" 1)"
check "ran once" "$(is "$(count "$work/own124.count")" 1)"
check "names exit 124 as the command's" "$(contains "$out" "attempt 1 exited 124 after [0-9]* s, and its 30 s bound did not end it")"
run_install "killed by the kernel" apt -- "$standin" "$work/own137.count" own137
check "exits 1" "$(is "$status" 1)"
check "ran once" "$(is "$(count "$work/own137.count")" 1)"
check "names exit 137 as the command's" "$(contains "$out" "attempt 1 exited 137 after [0-9]* s, and its 30 s bound did not end it")"

# A stand-in date first on PATH: it pauses 3 s on every second reading, the one the script makes
# after an attempt has ended, as a busy runner can pause the script there. The bound is 2 s, so a
# script that read the end of its deadline off that clock would take the command's own exit 124
# for a timeout and try it again.
stubs_date="$work/stubs-date"
mkdir "$stubs_date"
printf '#!/usr/bin/env bash\necho x >> "%s"\n[ $(( $(wc -l < "%s") %% 2 )) -eq 0 ] && "%s" 3\nexec "%s" "$@"\n' \
  "$work/date.count" "$work/date.count" "$real_sleep" "$real_date" > "$stubs_date/date"
chmod +x "$stubs_date/date"
seam "$HANGS"
echo "case: a command's own exit 124 stays its own when the script is paused before it reads the clock"
PATH="$stubs_date:$PATH" run_install "exits 124 itself, paused" apt -- "$standin" "$work/own124-paused.count" own124
check "the pause came after the attempt" "$(is "$([ "$(count "$work/date.count")" -ge 2 ] && echo yes)" yes)"
check "ran once" "$(is "$(count "$work/own124-paused.count")" 1)"
check "names exit 124 as the command's" "$(contains "$out" "attempt 1 exited 124 after [0-9]* s, and its 2 s bound did not end it")"
check "does not call it a timeout" "$(is "$(contains "$out" "timed out")" false)"

seam "$ENDS"
echo "case: a command that is not there is not tried again"
run_install "not there" apt -- "$work/no-such-command"
check "exits 1" "$(is "$status" 1)"
check "tried once" "$(is "$(grep -c '^network-install apt: attempt' <<<"$out")" 1)"
check "says it exited 127" "$(contains "$out" "attempt 1 exited 127")"

echo "case: a kind the script does not bound, and a call without --"
run_install "unknown kind" pip3 -- true
check "exits 1 for the kind" "$(is "$status" 1)"
check "names the kind" "$(contains "$out" "'pip3' is no kind this script bounds; the kinds are: apt bun bun-image docker playwright go elan apk npm uv gcloud")"
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
# bounds_of <file>: the bound and the wait before SIGKILL of each attempt the stand-in timeout wrote
# down, and the command it ran, the script's own attempt runner and its marker left out.
bounds_of() { sed -E 's/^-k ([0-9]+) ([0-9]+) sh .* --attempt [^ ]+ [0-9]+ -- (.*)$/kill-after \1, bound \2: \3/' "$1"; }

echo "case: the real bound of each kind, and the real wait for SIGKILL, with the seam unset"
expected=$("$script" --bounds | cut -d' ' -f1,2)
while read -r kind bound; do
  : > "$work/timeout.args"
  seam_off
  PATH="$stubs:$PATH" run_install "the real $kind bound" "$kind" -- "$standin" "$work/real-$kind.count" ok
  seam "$ENDS"
  check "$kind waits $bound s an attempt, then 15 s for SIGTERM to end it" \
    "$(is "$(bounds_of "$work/timeout.args")" "kill-after 15, bound $bound: $standin $work/real-$kind.count ok")"
done <<<"$expected"
check "--bounds prints the table, with three attempts, the 15 s and the 10 s pause" \
  "$(is "$(seam_off && "$script" --bounds)" "$(printf '%s 3 15 10\n' 'apt 60' 'bun 120' 'bun-image 300' 'docker 120' 'playwright 60' 'go 120' 'elan 60' 'apk 60' 'npm 120' 'uv 120' 'gcloud 120')")"

echo "case: the real pause between attempts and the real wait before SIGKILL, with the seam unset but for the bound"
: > "$work/sleep.args"
: > "$work/timeout.args"
seam_off
export NETWORK_INSTALL_BOUND_SECONDS=2
PATH="$stubs:$PATH" run_install "the real pause" apt -- "$standin" "$work/pause.count" hang
seam "$ENDS"
check "exits 1" "$(is "$status" 1)"
check "pauses 10 s between attempts, twice" "$(is "$(grep -c '^10$' "$work/sleep.args")" 2)"
check "waits 15 s after each attempt's SIGTERM before it sends SIGKILL" \
  "$(is "$(bounds_of "$work/timeout.args" | sort -u)" "kill-after 15, bound 2: $standin $work/pause.count hang")"

summary "network-install.sh"
