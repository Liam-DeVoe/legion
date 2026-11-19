import {
  afterAll,
  beforeAll,
  beforeEach,
  describe,
  expect,
  setDefaultTimeout,
  test,
} from "bun:test";
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";
import { scriptFunctions } from "./script-functions";

// lib/wait-gone.sh: wait_gone, kill_gone and stop_gone, and the stops of the scripts that run
// them, each followed by the step that needs the process gone. Every stand-in is a real process
// of this test (holder.py below), and every check of what it held is a real read: a connect to
// its port, an flock of its lock, `rm -rf` of the directory it writes, the table run_processes
// reads. A stand-in leaves late in one of three ways: it ignores SIGTERM, or it goes on for a
// while after its SIGTERM, or it has threads and memory, so that the kernel releases its port and
// its lock only once the last of its threads has exited, which after a SIGKILL takes a
// measurable time, while /proc already shows its main thread as a zombie. A process that SIGKILL
// has not finished ending, as one in uninterruptible sleep stays, is a stand-in whose SIGKILL the
// harness's own `kill` drops (drop_kill): the wait must run to its deadline and name it. What
// /proc shows of a reused pid, a zombie whose other threads still run and an entry that cannot be
// read is a tree this test writes, the waits pointed at it by wait_gone_proc. A pid gone does not
// free what it held when a child it forked holds the same socket and lock: a stand-in that forks
// one (child=) shows that a wait on the pid alone returns with both still held, and that a wait on
// the port and the lock themselves (`port:N`, `lock:FILE`) does not.
const lib = import.meta.dir;
const e2e = join(lib, "..");
const root = join(e2e, "..", "..");
const dir = temporaryDirectory("wait-gone-test");
const holderPath = join(dir, "holder.py");
const pidsFile = join(dir, "started.pids");
// Each case's own timeout, the one fixed time this file sets, and what every wait a case needs an
// answer from runs to: END (caseEnd below) is that timeout counted from the case's start, less
// what the case does after the wait, so that a wait that never ends fails the case with its own
// line before bun stops it. A bound that is itself what a case checks (a 2 s deadline the wait must
// run to, a grace of 1 s) keeps its own value, and a mutant of wait-gone.sh shows the case fails
// when the bound is not kept.
const caseTimeoutMs = 300_000;
setDefaultTimeout(caseTimeoutMs);
let caseStart = Date.now();
beforeEach(() => {
  caseStart = Date.now();
});
// caseEnd() is END: the epoch second at which the case's own timeout leaves 60 s for what follows
// the wait (its later reads, the harness's exit, bun's own bookkeeping).
function caseEnd() {
  return String(Math.floor((caseStart + caseTimeoutMs - 60_000) / 1000));
}

// The stand-in, written once the suite's directory exists (temporaryDirectory makes it before the
// first test).
const holder = `import ctypes, fcntl, os, signal, socket, sys, threading, time

# holder.py KEY=VALUE...: a stand-in for a service a script stops. It listens on a loopback port
# the kernel picks and writes the number to port=FILE once all of the below is set up.
#   term=default|ignore|slow:SECONDS  what SIGTERM does: end it, nothing, or end it SECONDS later
#   write=DIR   with term=slow, it creates a file in DIR every 10 ms until it ends, as a server's
#               drain writes (a few hundred files, never so many that removing them takes minutes)
#   log=FILE    it appends "serving" to FILE, and with term=slow "shutdown-complete" as it ends
#   lock=FILE   it holds an flock on FILE
#   child=SECONDS  it forks a child, once the port and the lock are set up, that holds both (the
#               same socket and the same open lock file) until SECONDS after this process is gone
#   release=FILE   with child=, the child holds on until FILE is there instead
#   pids=FILE   the child appends its pid to FILE, for the suite's last cleanup
#   threads=N   it starts N threads that sleep, as every Go binary and Bun has threads
#   mib=M       it touches M MiB, which its exit after SIGKILL takes a measurable time to release
#   leader=exit its main thread exits once it is set up (pthread_exit), and its other threads keep
#               the port and the lock: /proc shows the process's main thread as a zombie
#   cwd=DIR     it works in DIR
args = dict(arg.split("=", 1) for arg in sys.argv[1:])
if "cwd" in args:
    os.chdir(args["cwd"])
term = args.get("term", "default")
if term == "ignore":
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
elif term.startswith("slow:"):
    delay = float(term[5:])

    def on_term(signum, frame):
        end, i = time.monotonic() + delay, 0
        while time.monotonic() < end:
            if "write" in args:
                open(os.path.join(args["write"], "drain.%d" % i), "w").close()
                i += 1
            time.sleep(0.01)
        if "log" in args:
            with open(args["log"], "a") as log:
                log.write("shutdown-complete\\n")
        os._exit(0)

    signal.signal(signal.SIGTERM, on_term)
listener = socket.socket()
listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
listener.bind(("127.0.0.1", 0))
listener.listen()
if "lock" in args:
    lock = open(args["lock"], "a")
    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
if "child" in args or "release" in args:
    parent = os.getpid()
    if os.fork() == 0:
        if "pids" in args:
            with open(args["pids"], "a") as pids:
                stat = open("/proc/self/stat").read()
                pids.write("%d %s\\n" % (os.getpid(), stat[stat.rindex(")") + 2:].split()[19]))
        while os.getppid() == parent:
            time.sleep(0.01)
        if "release" in args:
            while not os.path.exists(args["release"]):
                time.sleep(0.01)
        else:
            time.sleep(float(args["child"]))
        os._exit(0)
for _ in range(int(args.get("threads", "0"))):
    threading.Thread(target=time.sleep, args=(3600,), daemon=True).start()
memory = bytearray(int(args.get("mib", "0")) << 20)
for i in range(0, len(memory), 4096):
    memory[i] = 1
if "log" in args:
    with open(args["log"], "a") as log:
        log.write("serving\\n")
with open(args["port"] + ".new", "w") as port:
    port.write(str(listener.getsockname()[1]))
os.rename(args["port"] + ".new", args["port"])
if args.get("leader") == "exit":
    ctypes.CDLL(None).pthread_exit(None)
time.sleep(3600)
`;
beforeAll(() => writeFileSync(holderPath, holder));

// Every stand-in a harness starts is recorded in started.pids with its start time (stat field 22);
// one a harness could not end is ended here, by that pid, while the pid still has that start time,
// so a pid the kernel gave to another process since is never signalled. Only /proc/PID/stat is
// read, which waits on nothing of the process it describes.
afterAll(() => {
  if (!existsSync(pidsFile)) return;
  for (const line of readFileSync(pidsFile, "utf8").split("\n")) {
    const [pid, start] = line.split(" ").map(Number);
    if (!pid || !start) continue;
    try {
      const stat = readFileSync(`/proc/${pid}/stat`, "utf8");
      if (Number(stat.slice(stat.lastIndexOf(")") + 2).split(" ")[19]) !== start) continue;
      process.kill(pid, "SIGKILL");
    } catch {}
  }
});

// The shell every harness runs above its body. Paths come in through the environment, so no
// harness's own command line names a run's work directory (run_processes reads command lines).
// Its EXIT trap ends what it started; a TERM (bun's, on a timeout) runs that trap too.
const prelude = `set -u
. "$ROOT/scripts/e2e/lib/wait-gone.sh"
started=()
trap 'for p in \${started[@]+"\${started[@]}"}; do builtin kill -KILL "$p" 2>/dev/null; done; wait 2>/dev/null' EXIT
trap 'exit 143' TERM
# holder NAME KEY=VALUE...: holder.py with those settings, in the background; NAME is its pid and
# NAME_port its port once it has written it.
holder() {
  local name=$1 file
  shift
  file=$(mktemp -u "$DIR/port.XXXXXX")
  python3 "$HOLDER" port="$file" "$@" &
  started+=("$!")
  local start=''
  process_start "$!" start || true
  echo "$! $start" >>"$PIDS"
  printf -v "$name" '%s' "$!"
  for _ in $(seq 1 600); do [ -s "$file" ] && break; sleep 0.05; done
  [ -s "$file" ] || { echo "the stand-in $name wrote no port" >&2; exit 2; }
  printf -v "\${name}_port" '%s' "$(<"$file")"
}
now_ms() { echo $((\${EPOCHREALTIME//[!0-9]/} / 1000)); }
listening() { (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; }
held() { ! flock -n "$1" true; }
# drop_kill PID: a kill that drops every SIGKILL for PID and passes every other call on, so PID
# stays in the process table as a process SIGKILL has not finished ending does.
drop_kill() {
  spare=$1
  kill() {
    case " $* " in *" KILL "* | *" -KILL "* | *" -9 "*) [[ " $* " == *" $spare "* ]] && return 0 ;; esac
    builtin kill "$@"
  }
}
`;

function bash(body: string, env: Record<string, string> = {}) {
  const ran = Bun.spawnSync(["bash", "-c", `${prelude}\n${body}`], {
    env: {
      PATH: process.env.PATH ?? "",
      ROOT: root,
      DIR: dir,
      HOLDER: holderPath,
      PIDS: pidsFile,
      END: caseEnd(),
      ...env,
    },
    stdout: "pipe",
    stderr: "pipe",
    // Stopped 30 s before the case's own timeout, so the case reports what it read.
    timeout: Math.max(1_000, caseStart + caseTimeoutMs - 30_000 - Date.now()),
  });
  return { code: ran.exitCode, stdout: ran.stdout.toString(), stderr: ran.stderr.toString() };
}

// The lines KEY=VALUE a harness prints, as a map.
function values(out: string) {
  return Object.fromEntries(
    out
      .split("\n")
      .filter((line) => /^[a-z_]+=/.test(line))
      .map((line) => [line.slice(0, line.indexOf("=")), line.slice(line.indexOf("=") + 1)])
  );
}

let dirs = 0;
function freshDir(name: string) {
  const made = join(dir, `${name}-${++dirs}`);
  mkdirSync(made, { recursive: true });
  return made;
}

describe("wait_gone and kill_gone", () => {
  test("a sleep given SIGTERM is gone at once", () => {
    const r = bash(`sleep 30 & started+=("$!")
kill_gone TERM "$END" "$!"; echo "rc=$?"`);
    expect(values(r.stdout).rc).toBe("0");
  });

  test("a process that ignores SIGTERM is still there at the deadline, named, and gone given SIGKILL", () => {
    const r = bash(`bash -c 'trap "" TERM; exec sleep 30' & b=$!; started+=("$b")
sleep 0.3
deadline=$((EPOCHSECONDS + 2))
kill_gone TERM "$deadline" "$b"; echo "rc=$?"
echo "ended_ms=$(now_ms)"; echo "deadline=$deadline"; echo "pid=$b"
kill_gone KILL "$END" "$b"; echo "killed=$?"`);
    const v = values(r.stdout);
    expect(v.rc).toBe("1");
    expect(r.stderr).toMatch(
      new RegExp(
        `^pid ${v.pid} \\(sleep 30\\) is still in the process table at its deadline, [0-9]+\\.[0-9] s after its SIGTERM$`,
        "m"
      )
    );
    // It never gives up before the deadline, and it ends at it, not a bound later.
    const ended = Number(v.ended_ms);
    expect(ended).toBeGreaterThanOrEqual(Number(v.deadline) * 1000);
    expect(ended).toBeLessThan(Number(v.deadline) * 1000 + 4000);
    expect(v.killed).toBe("0");
  });

  test("a zombie whose parent is stopped counts as gone, before the deadline", () => {
    const r = bash(`zpid=$(mktemp -u "$DIR/zpid.XXXXXX")
bash -c 'sleep 30 & echo $! >"$1"; wait' zombie-parent "$zpid" & parent=$!; started+=("$parent")
while [ ! -s "$zpid" ]; do sleep 0.05; done
z=$(<"$zpid")
kill -STOP "$parent"
# Stopped before the kill: a parent the stop has not reached yet would reap the zombie at once.
until [ "$(awk '{print $3}' "/proc/$parent/stat")" = T ]; do sleep 0.01; done
start=$(now_ms)
kill_gone KILL "$END" "$z"; echo "rc=$?"
echo "ms=$(($(now_ms) - start))"
echo "state=$(awk '{print $3}' "/proc/$z/stat" 2>/dev/null)"
kill -CONT "$parent"`);
    const v = values(r.stdout);
    expect(v.rc).toBe("0");
    expect(v.state).toBe("Z");
    // Its parent stays stopped until after: a wait that did not count the zombie gone would have run
    // to END and returned 1.
  });

  test("a whole process group given SIGKILL is gone", () => {
    const r = bash(`setsid bash -c 'sleep 30 & sleep 30 & wait' & g=$!; started+=("$g")
sleep 0.3
kill_gone KILL "$END" "-$g"; echo "rc=$?"`);
    expect(values(r.stdout).rc).toBe("0");
  });

  test("a pid with no process is gone", () => {
    const r = bash(`wait_gone "$END" 999999999; echo "rc=$?"`);
    expect(values(r.stdout).rc).toBe("0");
  });

  test("a deadline that is no time since the epoch is refused, naming it", () => {
    const r = bash(`sleep 30 & started+=("$!")
wait_gone 10 "$!"; echo "wait=$?"
kill_gone TERM 10 "$!"; echo "kill=$?"
stop_gone 1 10 "$!"; echo "stop=$?"
kill -0 "$!" && echo alive=yes`);
    const v = values(r.stdout);
    expect([v.wait, v.kill, v.stop, v.alive]).toEqual(["2", "2", "2", "yes"]);
    expect(r.stderr).toContain(
      "wait_gone: the deadline '10' is no time in seconds since the epoch"
    );
  });
});

describe("a process with threads", () => {
  test("is not gone while its main thread is a zombie and another thread holds its port and lock", () => {
    const lock = join(freshDir("threads"), "lock");
    const r = bash(
      `holder h threads=1 leader=exit lock="$LOCK"
for _ in $(seq 1 100); do [ "$(awk '{print $3}' "/proc/$h/stat")" = Z ] && break; sleep 0.05; done
echo "state=$(awk '{print $3}' "/proc/$h/stat")"
wait_gone "$((EPOCHSECONDS + 1))" "$h"; echo "rc=$?"
echo "left=\${wait_gone_left[*]}"
echo "pid=$h"
listening "$h_port" && echo listening=yes
held "$LOCK" && echo held=yes
kill_gone KILL "$END" "$h" "port:$h_port" "lock:$LOCK"; echo "killed=$?"
listening "$h_port" || echo listening_after=no
held "$LOCK" || echo held_after=no`,
      { LOCK: lock }
    );
    const v = values(r.stdout);
    expect(v.state).toBe("Z");
    expect([v.rc, v.left]).toEqual(["1", v.pid]);
    expect([v.listening, v.held]).toEqual(["yes", "yes"]);
    expect([v.killed, v.listening_after, v.held_after]).toEqual(["0", "no", "no"]);
  });

  test("given SIGKILL with its port and lock as targets, is waited for until both are free", () => {
    const lock = join(freshDir("threads-kill"), "lock");
    const r = bash(
      `for t in 1 2 3; do
  holder h threads=8 mib=1024 lock="$LOCK.$t"
  kill_gone KILL "$END" "$h" "port:$h_port" "lock:$LOCK.$t"; rc=$?
  listening "$h_port" && echo "trial $t: port held after kill_gone returned $rc"
  held "$LOCK.$t" && echo "trial $t: lock held after kill_gone returned $rc"
done
echo done=yes`,
      { LOCK: lock }
    );
    expect(r.stdout).toBe("done=yes\n");
  });
});

describe("a port or a lock a forked child of the process still holds", () => {
  test("a wait on the pid alone returns while the child still holds the port and the lock", () => {
    const d = freshDir("child-control");
    const r = bash(
      `holder h release="$D/release" lock="$D/lock" pids="$PIDS"
kill_gone KILL "$END" "$h"; echo "rc=$?"
listening "$h_port" && echo port=held
held "$D/lock" && echo lock=held
touch "$D/release"
kill_gone KILL "$END" "port:$h_port" "lock:$D/lock"; echo "freed=$?"`,
      { D: d }
    );
    expect(values(r.stdout)).toMatchObject({ rc: "0", port: "held", lock: "held", freed: "0" });
  });

  test("kill_gone given the port and the lock returns only once the child has let both go", () => {
    const d = freshDir("child");
    const r = bash(
      `for t in 1 2 3; do
  holder h child=1 lock="$D/lock.$t" pids="$PIDS"
  kill_gone KILL "$END" "$h" "port:$h_port" "lock:$D/lock.$t"; rc=$?
  listening "$h_port" && echo "trial $t: port held after kill_gone returned $rc"
  held "$D/lock.$t" && echo "trial $t: lock held after kill_gone returned $rc"
done
echo done=yes`,
      { D: d }
    );
    expect(r.stdout).toBe("done=yes\n");
  });

  test("a port and a lock still held at the deadline are named", () => {
    const d = freshDir("child-named");
    const r = bash(
      `holder h release="$D/release" lock="$D/lock" pids="$PIDS"
kill_gone KILL "$((EPOCHSECONDS + 2))" "$h" "port:$h_port" "lock:$D/lock"; echo "rc=$?"
echo "port=$h_port"
touch "$D/release"`,
      { D: d }
    );
    const v = values(r.stdout);
    expect(v.rc).toBe("1");
    expect(r.stderr).toMatch(
      new RegExp(`^port ${v.port} is still listened on \\(.*\\) at its deadline`, "m")
    );
    expect(r.stderr).toContain(`lock ${d}/lock is still held at its deadline`);
  });
});

describe("what wait_gone reads of /proc", () => {
  // A tree of the test's own, so the cases do not depend on a reused pid or a slow exit: each
  // entry is a stat line whose fields 3 (state) and 22 (start time) are what the case sets, and a
  // task directory. The harness's kill drops every signal, so no pid of this tree reaches a real
  // process.
  const stat = (pid: number, state: string, start: number) =>
    `${pid} (stand in) ${state} 1 ${pid} ${pid} 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 ${start} 0 0\n`;
  function fakeProc(entries: Record<number, { state?: string; start?: number; tasks?: number[] }>) {
    const proc = freshDir("proc");
    mkdirSync(join(proc, "self"));
    writeFileSync(join(proc, "self", "stat"), stat(1, "S", 1));
    for (const [pid, e] of Object.entries(entries)) {
      mkdirSync(join(proc, pid, "task"), { recursive: true });
      writeFileSync(join(proc, pid, "stat"), stat(Number(pid), e.state ?? "S", e.start ?? 100));
      for (const task of e.tasks ?? [Number(pid)]) mkdirSync(join(proc, pid, "task", String(task)));
    }
    return proc;
  }
  // wait_gone in the background against PROC with DEADLINE (a bash word), CHANGE run 0.5 s in, and
  // what it returned and when. A case that expects the wait to end once CHANGE lands gives it END,
  // so a wait that missed the change runs to END and returns 1; a case that expects it to run to the
  // deadline gives it 3 s, the bound it checks.
  function waitAcross(proc: string, pid: number, change: string, deadline: string) {
    return bash(
      `wait_gone_proc=$PROC
kill() { return 0; }
deadline=${deadline}
start=$(now_ms)
( wait_gone "$deadline" ${pid}; echo "rc=$? ms=$(($(now_ms) - start))" ) &
sleep 0.5
${change}
wait $!`,
      { PROC: proc }
    );
  }

  test("a pid whose start time changes is a new process, and the one waited on is gone", () => {
    const proc = fakeProc({ 4242: { start: 100 } });
    const r = waitAcross(
      proc,
      4242,
      `printf '%s\\n' "$(sed 's/ 100 0 0$/ 200 0 0/' "$PROC/4242/stat")" >"$PROC/4242/stat.new" && mv "$PROC/4242/stat.new" "$PROC/4242/stat"`,
      '"$END"'
    );
    expect(r.stdout.trim().split(" ")[0]).toBe("rc=0");
  });

  test("a pid whose start time stays is the same process, still there at the deadline", () => {
    const proc = fakeProc({ 4242: { start: 100 } });
    const r = waitAcross(proc, 4242, ":", '"$((EPOCHSECONDS + 3))"');
    expect(r.stdout.trim().split(" ")[0]).toBe("rc=1");
  });

  test("a zombie whose task directory lists another thread is not gone until that thread is", () => {
    const proc = fakeProc({ 4244: { state: "Z", tasks: [4244, 4245] } });
    const kept = waitAcross(proc, 4244, ":", '"$((EPOCHSECONDS + 3))"');
    expect(kept.stdout.trim().split(" ")[0]).toBe("rc=1");
    const r = waitAcross(proc, 4244, `rmdir "$PROC/4244/task/4245"`, '"$END"');
    expect(r.stdout.trim().split(" ")[0]).toBe("rc=0");
  });

  test("a zombie with no thread but its main one is gone at once", () => {
    const proc = fakeProc({ 4246: { state: "Z" } });
    const r = waitAcross(proc, 4246, ":", '"$END"');
    expect(r.stdout.trim().split(" ")[0]).toBe("rc=0");
  });

  test("an entry whose stat cannot be read is not gone, and kill_gone says why it names it", () => {
    const proc = fakeProc({});
    mkdirSync(join(proc, "4243", "stat"), { recursive: true });
    const r = bash(
      `wait_gone_proc=$PROC
kill() { return 0; }
kill_gone TERM "$((EPOCHSECONDS + 1))" 4243; echo "rc=$?"`,
      { PROC: proc }
    );
    expect(values(r.stdout).rc).toBe("1");
    expect(r.stderr).toContain(
      `pid 4243: ${proc}/4243/stat could not be read, so it is not known to be gone at its deadline`
    );
    rmSync(join(proc, "4243"), { recursive: true });
  });
});

describe("stop_gone: one deadline for the SIGTERM's wait and the SIGKILL's", () => {
  test("a process that ends on SIGTERM is gone, given no SIGKILL", () => {
    const r = bash(`holder h term=slow:0.5
stop_gone 10 "$END" "$h"; echo "rc=$?"
wait "$h"; echo "status=$?"`);
    expect(values(r.stdout)).toMatchObject({ rc: "0", status: "0" });
  });

  test("a process that ignores SIGTERM gets its SIGKILL at the grace and is gone", () => {
    const r = bash(`holder h term=ignore
start=$(now_ms)
stop_gone 1 "$END" "$h"; echo "rc=$?"
echo "ms=$(($(now_ms) - start))"
wait "$h"; echo "status=$?"`);
    const v = values(r.stdout);
    expect([v.rc, v.status]).toEqual(["0", String(128 + 9)]);
    expect(Number(v.ms)).toBeLessThan(15_000);
  });

  test("the grace is GRACE seconds from the call, whatever fraction of a second the call starts at", () => {
    // The stop starts in the last tenth of a second, where a grace counted from EPOCHSECONDS, a
    // whole second, would end almost at once and SIGKILL a stand-in that needs half of it.
    const r = bash(`holder h term=slow:0.5
while [ "\${EPOCHREALTIME#*.}" -lt 900000 ]; do sleep 0.01; done
stop_gone 1 "$END" "$h"; echo "rc=$?"
wait "$h"; echo "status=$?"`);
    expect(values(r.stdout)).toMatchObject({ rc: "0", status: "0" });
  });

  test("a SIGTERM wait that takes the caller's time leaves the SIGKILL's wait none past the deadline", () => {
    const r = bash(`holder h term=ignore
drop_kill "$h"
deadline=$((EPOCHSECONDS + 2))
stop_gone 10 "$deadline" "$h"; echo "rc=$?"
echo "ended_ms=$(now_ms)"; echo "deadline=$deadline"; echo "pid=$h"`);
    const v = values(r.stdout);
    expect(v.rc).toBe("1");
    expect(Number(v.ended_ms)).toBeGreaterThanOrEqual(Number(v.deadline) * 1000);
    expect(Number(v.ended_ms)).toBeLessThan(Number(v.deadline) * 1000 + 4000);
    expect(r.stderr).toMatch(
      new RegExp(
        `^pid ${v.pid} \\(python3 .*holder\\.py.*\\) is still in the process table at its deadline, [0-9.]+ s after its SIGKILL$`,
        "m"
      )
    );
  });
});

// The stops of the scripts, each cut out of its script by name, and the step after it that needs
// the process gone: the five kinds this change waits for (a port, a removed directory, a file the
// process writes, the process table, a lock).
describe("each stop of the scripts, then the step that needs the process gone", () => {
  const stage4b = scriptFunctions(join(e2e, "stage4b-sandbox-tree.sh"));
  const stage2 = scriptFunctions(join(e2e, "stage2-tmux-supervision.sh"));
  const verifiers = scriptFunctions(join(e2e, "verifiers-staging-token.sh"));
  const stage1 = scriptFunctions(join(e2e, "stage1-skeleton.sh"));
  const rig = `root=$ROOT work=$WORK evidence=$WORK timeout_hook=
note() { :; }
fail() { echo "FAIL $*"; exit 1; }
. "$ROOT/scripts/e2e/lib/rig.sh"`;

  test("port: lib/rig.sh's stop_pid, given the port as Stage 4b's restarts give it, of a daemon that ignores SIGTERM, then Stage 4b's ports_free", () => {
    const work = freshDir("port");
    const r = bash(
      `${rig}
${stage4b("ports_free")}
stop_pid_grace=1
for t in 1 2 3; do
  holder d term=ignore threads=8 mib=1024
  stop_pid "$d" "$END" "port:$d_port" || echo "trial $t: stop_pid returned 1"
  ports_free "$d_port"
done
echo free=yes`,
      { WORK: work }
    );
    expect(r.stdout).toBe("free=yes\n");
  });

  test("port: Stage 4b's restart_daemon waits for the ports a child of the daemon still holds before it starts the new one", () => {
    const work = freshDir("restart-child");
    const r = bash(
      `${rig}
${stage4b("ports_free")}
${stage4b("restart_daemon")}
write_legion_config() { :; }
start_daemon() { ports_free "$port_daemon" "$port_worker_stream" && echo started=yes; }
stop_pid_grace=1
holder daemon_pid child=1 pids="$PIDS"
port_daemon=$daemon_pid_port port_worker_stream=$daemon_pid_port
restart_daemon`,
      { WORK: work }
    );
    expect(r.stdout).toBe("started=yes\n");
  });

  test("process table: Stage 2's stop_pid of a daemon that drains for a second on SIGTERM, then the next run's refuse_leftovers reads the table", () => {
    const work = freshDir("table-stage2");
    const r = bash(
      `. "$ROOT/scripts/e2e/lib/leftovers.sh"
${stage2("stop_pid")}
fail() { echo "FAIL $*"; exit 1; }
for t in 1 2 3; do
  holder d term=slow:1 threads=8 mib=256
  stop_pid "$d" "$END" || echo "trial $t: stop_pid returned 1"
  leftover_started "$d" >/dev/null && echo "trial $t: refuse_leftovers would read pid $d as running"
done
echo gone=yes`,
      { WORK: work }
    );
    expect(r.stdout).toBe("gone=yes\n");
  });

  test("lock: a run's stop_box_login_processes of a pane whose child holds the inherited lock, then the next run's take_box_login_lock", () => {
    // As a stage proof holds it: the run takes the lock, so every pane it starts, and every process
    // a pane starts, inherits the locked descriptor. The run ends after its teardown (the subshell
    // below), and the next run takes the lock.
    const d = freshDir("lock");
    const r = bash(
      `. "$ROOT/scripts/e2e/lib/box-login.sh"
box_login_stop_grace=1
for t in 1 2 3; do
  (
    box_login_lock_file=$D/lock.$t
    take_box_login_lock || exit 1
    holder pane term=ignore threads=8 mib=256 child=1 pids="$PIDS"
    stop_box_login_processes "$pane" || echo "trial $t: the stop returned 1"
  )
  box_login_lock_file=$D/lock.$t bash -c '. "$ROOT/scripts/e2e/lib/box-login.sh"; take_box_login_lock' 2>/dev/null || echo "trial $t: refused"
done
echo took=yes`,
      { D: d }
    );
    expect(r.stdout).toBe("took=yes\n");
  });

  test("lock: a pane SIGKILL has not ended is waited for to the deadline and named, never left holding the lock unsaid", () => {
    const lock = join(freshDir("lock-stays"), "box-login.lock");
    const r = bash(
      `. "$ROOT/scripts/e2e/lib/box-login.sh"
box_login_stop_grace=1
holder pane term=ignore lock="$LOCK"
drop_kill "$pane"
start=$(now_ms)
stop_box_login_processes "$pane"; echo "rc=$?"
echo "ms=$(($(now_ms) - start))"
echo "pid=$pane"`,
      { LOCK: lock }
    );
    const v = values(r.stdout);
    expect(v.rc).toBe("1");
    expect(Number(v.ms)).toBeGreaterThanOrEqual(10_000);
    const pid = v.pid;
    expect(r.stderr).toMatch(
      new RegExp(
        `^pid ${pid} \\(python3 .*holder\\.py.*\\) is still in the process table at its deadline`,
        "m"
      )
    );
  });

  test("directory: the verifiers proof's cleanup of a Dispatch that drains into $work for 2 s, then the next run's rm -rf", () => {
    const work = join(freshDir("verifiers"), "verifiers-e2e");
    mkdirSync(join(work, "home", "data"), { recursive: true });
    for (let i = 0; i < 100; i++) writeFileSync(join(work, "home", "data", `record.${i}`), "");
    // Once the cleanup returns nothing may write into $work: the directory's entries, counted twice
    // half a second apart, are the same, and the next run's rm -rf removes it. A cleanup that does
    // not wait leaves the stand-in writing a file every 10 ms for 2 s.
    const r = bash(
      `docker() { :; }
pg_name=none nats_name=none listener_pid=
${verifiers("cleanup")}
holder dispatch_pid term=slow:2 write="$WORK/home/data"
cleanup
entries() { local e=("$WORK/home/data"/*); echo "\${#e[@]}"; }
echo "before=$(entries)"
sleep 0.5
echo "after=$(entries)"
rm -rf "$WORK" && echo removed=yes`,
      { WORK: work }
    );
    const v = values(r.stdout);
    expect(Number(v.before)).toBeGreaterThanOrEqual(100);
    expect(v.after).toBe(v.before);
    expect(v.removed).toBe("yes");
    expect(existsSync(work)).toBe(false);
  });

  test("file: Stage 1's cleanup of a failed run prints the daemon's log with the line it writes 1 s after its SIGTERM", () => {
    const work = freshDir("stage1");
    mkdirSync(join(work, "logs"));
    const r = bash(
      `${rig}
docker() { :; }
container=none ok= daemon_keeper_pid=
${stage1("cleanup")}
holder pid term=slow:1 log="$WORK/logs/daemon.log"
cleanup`,
      { WORK: work }
    );
    expect(r.stdout).toContain("serving\nshutdown-complete\n");
  });

  test("process table: Stage 3's sweep of a run's process with threads and memory, then its read of the table", () => {
    const work = freshDir("table");
    // The checkpoint's own lines, from its sweep to its read of the table.
    const script = readFileSync(join(e2e, "stage3-devbox-workflow.sh"), "utf8");
    const block =
      /^kill_gone KILL .*\$\(run_processes\)[^\n]*\n[\s\S]*?a proof process remains"\n/m.exec(
        script
      );
    expect(block).not.toBeNull();
    const r = bash(
      `${rig}
docker() { :; }
tmux() { return 1; }
pg_container=none nats_container=none ptoken=none
for t in 1 2 3; do
  holder p cwd="$WORK" term=ignore threads=8 mib=1024
  ${block?.[0]}
done
echo swept=yes`,
      { WORK: work }
    );
    expect(r.stdout).toBe("swept=yes\n");
  });

  test("Stage 4b's restart_daemon does not start a second daemon beside one still there at its stop's deadline", () => {
    const work = freshDir("restart");
    const run = (stays: boolean) =>
      bash(
        `${rig}
write_legion_config() { :; }
start_daemon() { echo started=yes; }
${stage4b("restart_daemon")}
stop_pid_grace=0
holder daemon_pid term=${stays ? "ignore" : "default"}
${stays ? 'drop_kill "$daemon_pid"' : ""}
port_daemon=$daemon_pid_port port_worker_stream=$daemon_pid_port
echo "pid=$daemon_pid"
restart_daemon`,
        { WORK: work }
      );
    const stays = run(true);
    const pid = values(stays.stdout).pid;
    expect(values(stays.stdout).started).toBeUndefined();
    expect(stays.stdout).toContain(
      `FAIL the daemon (pid ${pid}) did not stop or its ports are still held (named above); its replacement would meet its ports`
    );
    expect(stays.stderr).toMatch(
      new RegExp(
        `^pid ${pid} \\(python3 .*holder\\.py.*\\) is still in the process table at its deadline`,
        "m"
      )
    );
    // The control: a daemon that leaves in time lets the restart run.
    expect(values(run(false).stdout).started).toBe("yes");
  });
});
