#!/usr/bin/env bash
# packages/envoy/loadtest/restart-step.test.sh
#
# Proves restart-step.sh's end_step, its stop of the step it started when the script itself fails:
# the SIGTERM goes to that step's own `loadgen run`, found below the run-step.sh the script
# started, and to no other process, whatever its command line; and the stop returns only once
# that loadgen has ended, every thread of it. It needs no stack. The step is a shell that runs a
# stand-in loadgen below an intermediate shell, as run-step.sh runs stack.sh's loadgen. The
# stand-in is a Python process whose main thread leaves first on SIGTERM while a second thread holds
# its port one second more, as the threads of a Go loadgen end one by one. A second stand-in with
# command line runs in a session of its own, as another run's loadgen would, and the stop must
# send it nothing. The functions under test are restart-step.sh's own text, read out of the
# script and run with common.sh, which the script sources.
set -euo pipefail

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=packages/envoy/loadtest/test-lib.sh
source "$here/test-lib.sh"
tmp=$(mktemp -d "${TMPDIR:-/tmp}/restart-step-test.XXXXXX")
readonly tmp
# Every process this suite starts is recorded, and its exit ends them by their pids.
cleanup() {
  end_recorded "$tmp/started" || true
  rm -rf -- "$tmp"
}
trap cleanup EXIT
fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}
pass() { printf 'PASS: %s\n' "$*"; }

command -v python3 >/dev/null || fail "restart-step.test.sh needs python3 on PATH for its stand-in loadgen"

# The functions under test, each from its `NAME() {` line to the `}` that closes it.
: >"$tmp/functions.sh"
for fn in listener_wait_ended step_loadgen end_step; do
  awk -v open="$fn() {" '$0 == open { on = 1 } on { print } on && $0 == "}" { exit }' "$here/restart-step.sh" >>"$tmp/functions.sh"
done
grep -qx 'listener_wait_ended() {' "$tmp/functions.sh" || fail "restart-step.sh holds no function listener_wait_ended"
grep -qx 'end_step() {' "$tmp/functions.sh" || fail "restart-step.sh holds no function end_step"

mkdir -p "$tmp/bin"
cat >"$tmp/bin/loadgen" <<'EOF'
#!/usr/bin/env python3
# The stand-in loadgen: `loadgen run -name NAME -out OUT`. It holds a listening port in a second
# thread, writes that port and then its pid next to PIDFILE, and on SIGTERM writes PIDFILE.term
# and ends its main thread alone, by the exit system call of that thread; the second thread ends
# the process a second later, the port still held until then.
import ctypes, os, platform, signal, socket, threading, time

pidfile = os.environ["PIDFILE"]
listener = socket.socket()
listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
listener.bind(("127.0.0.1", 0))
listener.listen(4)
stop = threading.Event()


def hold():
    stop.wait()
    time.sleep(1)
    os._exit(0)


def on_term(signum, frame):
    with open(pidfile + ".term", "a") as f:
        f.write("TERM\n")
    stop.set()
    ctypes.CDLL(None).syscall({"x86_64": 60, "aarch64": 93}[platform.machine()], 0)


threading.Thread(target=hold).start()
signal.signal(signal.SIGTERM, on_term)
with open(pidfile + ".port", "w") as f:
    f.write("%d\n" % listener.getsockname()[1])
with open(pidfile + ".tmp", "w") as f:
    f.write("%d\n" % os.getpid())
os.rename(pidfile + ".tmp", pidfile)
while True:
    time.sleep(0.1)
EOF
chmod +x "$tmp/bin/loadgen"

# ---- listener_wait_ended: a zombie main thread is not an ended listener ------------------------
source "$here/common.sh"
source "$tmp/functions.sh"
PIDFILE=$tmp/listener "$tmp/bin/loadgen" run -name listener-wait -out "$tmp/listener-out" &
listener=$!
record_pid "$tmp/started" "$listener"
t0=$EPOCHSECONDS
until [ -s "$tmp/listener.port" ]; do
  ((EPOCHSECONDS - t0 <= 10)) || fail "the listener stand-in wrote no port within 10 s"
  sleep 0.05
done
listener_start=$(process_start "$listener")
[ -n "$listener_start" ] || fail "the listener stand-in had no start time"
listener_port=$(<"$tmp/listener.port")
kill -TERM "$listener"
die() { return 1; }
listener_wait_ended "$listener" "$listener_start" ||
  fail "the listener wait did not wait for every thread of the listener to end"
if : 2>/dev/null 3<>"/dev/tcp/127.0.0.1/$listener_port"; then
  fail "the listener wait returned while the listener's port still took a connection"
fi
wait "$listener" 2>/dev/null || true
pass "the listener wait returns only once every thread of the listener has ended and its port is free"

# ---- end_step: the step's own loadgen, and no other process of that command line ---------------
# The shell that runs end_step starts the step itself, as restart-step.sh does, so the step is its
# child and end_step's wait on it is the script's own. The other loadgen is started once the step's
# is up, in a session of its own. Every pid goes to a file of this suite's and to its record.
name=restart-test-${tmp##*.} out=$tmp/out
end_out=$tmp/end.out end_rc=0
# shellcheck disable=SC2016 # expanded by the bash that sources common.sh and the functions
timeout 150 bash -c '
  source "$1"
  source "$2"
  source "$3"
  tmp=$4 name=$5 out=$6
  up() { local t0=$EPOCHSECONDS; until [ -s "$1" ]; do ((EPOCHSECONDS - t0 <= 10)) || { echo "no $1 within 10 s"; exit 1; }; sleep 0.05; done; }
  PIDFILE=$tmp/own bash -c "( \"\$1\" run -name \"\$2\" -out \"\$3\"; : ); wait" _ "$tmp/bin/loadgen" "$name" "$out" &
  step=$!
  echo "$step" >"$tmp/step"
  record_pid "$tmp/started" "$step"
  up "$tmp/own"
  record_pid "$tmp/started" "$(<"$tmp/own")"
  PIDFILE=$tmp/planted setsid -f "$tmp/bin/loadgen" run -name "$name" -out "$out" </dev/null >/dev/null 2>&1
  up "$tmp/planted"
  record_pid "$tmp/started" "$(<"$tmp/planted")"
  false || end_step
  ended=no took=no
  ! process_ended "$(<"$tmp/own")" || ended=yes
  ! : 2>/dev/null 3<>"/dev/tcp/127.0.0.1/$(<"$tmp/own.port")" || took=yes
  echo "the moment end_step returned: the step'"'"'s loadgen had ended: $ended, its port took a connection: $took"
' _ "$here/common.sh" "$here/test-lib.sh" "$tmp/functions.sh" "$tmp" "$name" "$out" >"$end_out" 2>&1 || end_rc=$?
((end_rc == 0)) || fail "end_step's shell ended with $end_rc: $(cat "$end_out")"
step=$(<"$tmp/step") own=$(<"$tmp/own") planted=$(<"$tmp/planted")
# A SIGTERM the other loadgen took is written within its next 0.1 s nap; it is given 2 s.
t0=$EPOCHSECONDS
until [ -e "$tmp/planted.term" ] || ((EPOCHSECONDS - t0 > 2)); do sleep 0.1; done
[ -e "$tmp/planted.term" ] &&
  fail "end_step sent SIGTERM to a loadgen with the step's command line in another session (pid $planted), which this script never started: $(cat "$end_out")"
process_ended "$planted" &&
  fail "the loadgen in another session (pid $planted) has gone, though nothing of this suite signals it: $(cat "$end_out")"
[ -e "$tmp/own.term" ] || fail "end_step sent the step's own loadgen (pid $own) no SIGTERM: $(cat "$end_out")"
grep -qx "the moment end_step returned: the step's loadgen had ended: yes, its port took a connection: no" "$end_out" ||
  fail "end_step returned before the step's loadgen (pid $own) had ended, every thread of it, its port free: $(cat "$end_out")"
process_ended "$step" || fail "end_step returned with the step (pid $step) still there: $(cat "$end_out")"
end_process "$planted" || fail "the loadgen in another session (pid $planted) still runs 10 s after its SIGKILL"
pass "end_step sends its SIGTERM to the step's own loadgen run alone, not to one of the same command line in another session, and returns once that loadgen has ended, every thread of it, and the step with it"

printf 'PASS: packages/envoy/loadtest/restart-step.test.sh\n'
