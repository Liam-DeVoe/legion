/** What stops the harness when the process that started it dies without stopping it.
 *
 *  Every server of a stack outlives its starter by design: each is the leader of a process group
 *  of its own (`Supervisor`, e2e/local-instance-support.ts), so a stop can reach what it starts in
 *  turn, and a Postgres is a daemon `pg_ctl` starts (e2e/harness-postgres.ts) or a container
 *  dockerd runs. `stopStacks` (e2e/harness-pool.ts) puts all of them down on every exit the
 *  starter lives through: the end of a run, a Ctrl-C, a SIGTERM. A SIGKILL is the one it does not
 *  live through - a `kill -9`, the OOM killer, a crash of Playwright's runner - and before this
 *  module every server of the run kept serving afterwards: measured on main 6977266e41, a SIGKILL
 *  of `bunx` and the runner mid-row left 7 processes running 15 s later (the fake Envoy, GitHub
 *  and broker, the plain-HTTP proxy, the real broker and its dev issuer, and the Postgres), each
 *  holding its port, its memory and its share of the CPU against the runs that came after it.
 *
 *  So each starter hands this module what it would have to stop, the moment it starts it, and
 *  takes it back the moment it has stopped it (`reapIfAbandoned`, `stopReaping`). The module
 *  keeps that list in a file, rewritten whole and renamed into place on every change, so the file
 *  holds the list as it stood at whatever instant the starter dies. Beside it runs one process,
 *  `bun e2e/reaper.ts`, started once per process that starts anything (`reaperStarted`), detached
 *  into a session of its own so a Ctrl-C to the terminal's process group or a kill of that group
 *  does not reach it, and holding the read end of a pipe whose write end only this process holds.
 *  The kernel closes that end when this process dies, however it dies, so the reaper reads end of
 *  file at that instant and no sooner: it then reads the list and stops what is on it (the reaper
 *  says how). After an exit that stopped everything, the list it reads is empty and it exits at
 *  once.
 *
 *  WHY A PROCESS OF ITS OWN, AND NOT A PARENT-DEATH SIGNAL. Linux can send a child a signal when
 *  its parent dies (`PR_SET_PDEATHSIG`), and neither node nor Bun can set it on a spawn. More to
 *  the point, it reaches only the process it is set on: not `run-server.sh`'s `go build`, not a
 *  postmaster `pg_ctl` started and left, and not a container, which is dockerd's. A list read by
 *  one process reaches all three, and is the same on every machine the harness runs on.
 *
 *  The pipe is never written to. The list travels in the file because a write to a pipe can wait
 *  in the runtime's buffer and be lost with the process, which would leave the reaper stopping a
 *  server the starter had already stopped, or never hearing of one it had started; a file written
 *  with `writeFileSync` and renamed is on disk before the call returns. */
import { type ChildProcess, spawn } from "node:child_process";
import { mkdtempSync, renameSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

/** One thing the reaper stops, and how. Its fields act in the order they are listed here. */
export interface Cleanup {
  /** A process group, which gets SIGKILL: a server and everything it started in turn. */
  readonly group?: number;
  /** A process the reaper waits for, sending it nothing, before it runs `commands`: a start that
   *  was still under way, such as `pg_ctl -w start`, whose server `commands` must find started. */
  readonly awaits?: number;
  /** Commands run one after the other, once every group has had its SIGKILL and every awaited
   *  process has ended: a `pg_ctl stop`, a `docker rm`, the removal of a data directory. */
  readonly commands?: readonly (readonly string[])[];
}

/** The list, by key, in the order its keys were first added. The reaper runs the commands of the
 *  last-added first, as a stack unwinds, so a run's directory, added before its databases, is
 *  removed after they are stopped. */
const held = new Map<string, Cleanup>();

const reaperScript = fileURLToPath(new URL("./reaper.ts", import.meta.url));

interface Reaper {
  /** Held for as long as this process runs, since it holds the pipe's end. */
  readonly child: ChildProcess;
  /** The list's file. */
  readonly list: string;
  /** Settles with the reaper's pid once it runs, or rejects with the reason it could not start. */
  readonly started: Promise<number>;
}

let reaper: Reaper | undefined;

/** Writes the list, whole, where the reaper reads it. */
function writeList(list: string): void {
  const next = `${list}.next`;
  writeFileSync(next, JSON.stringify([...held]));
  renameSync(next, list);
}

/** Starts this process's reaper, once, and answers it. */
function ensureReaper(): Reaper {
  if (reaper !== undefined) return reaper;
  const list = join(mkdtempSync(join(tmpdir(), "dispatch-e2e-reaper-")), "list.json");
  writeList(list);
  // stdin is the pipe whose end is the signal; stdout is not read; stderr is this process's own,
  // so the reaper's account of what it stopped reaches whoever reads this run's output.
  const child = spawn("bun", [reaperScript, list, String(process.pid)], {
    detached: true,
    stdio: ["pipe", "ignore", "inherit"],
  });
  const started = Promise.withResolvers<number>();
  child.once("spawn", () => started.resolve(child.pid ?? 0));
  child.once("error", (error) =>
    started.reject(
      new Error(
        `The Dispatch e2e harness cannot start: its reaper (bun ${reaperScript}) did not start, ` +
          `so a run killed now would leave every server it started running: ${error.message}`
      )
    )
  );
  // The rejection is the caller's to see wherever it awaits `reaperStarted`; this keeps it from
  // being reported as unhandled before then.
  started.promise.catch(() => {});
  // Neither the reaper nor the pipe to it keeps this process's event loop alive: the process ends
  // when its own work does, and its end is what the reaper waits for. Node's pipe has `unref`;
  // Bun's does not and keeps no loop alive.
  child.unref();
  (child.stdin as { unref?: () => void } | null)?.unref?.();
  reaper = { child, list, started: started.promise };
  return reaper;
}

/** Starts this process's reaper if it is not running yet, and settles with its pid once it runs.
 *  A starter awaits this before it starts its first server, so a reaper that cannot start refuses
 *  the run rather than leaving it unguarded. */
export function reaperStarted(): Promise<number> {
  return ensureReaper().started;
}

/** Hands the reaper `cleanup` under `key`, replacing what `key` held. Call it the moment the thing
 *  it stops exists - a process group right after its spawn, a database before its `pg_ctl start`. */
export function reapIfAbandoned(key: string, cleanup: Cleanup): void {
  const { list } = ensureReaper();
  held.set(key, cleanup);
  writeList(list);
}

/** Takes `key` back from the reaper: this process has stopped what it named. */
export function stopReaping(key: string): void {
  if (reaper === undefined || !held.delete(key)) return;
  writeList(reaper.list);
}

/** The key of a process group a `Supervisor` started. */
export function groupKey(pgid: number): string {
  return `process group ${pgid}`;
}

/** The key of a directory the reaper removes. */
export function directoryKey(path: string): string {
  return `directory ${path}`;
}
