/** `bun e2e/reaper.ts <list> <pid>`: stops what process `<pid>` started and did not stop, once it
 *  has died. e2e/harness-reaper.ts starts it, says why it exists, and keeps `<list>`.
 *
 *  It waits for the end of its stdin, which comes when `<pid>`, the only holder of the pipe's other
 *  end, exits or is killed. It then reads the list and `reap`s it. Then it says on stderr what it
 *  did, removes the list's directory, and exits 0. A list with nothing on it - the starter stopped
 *  everything before it ended, which is every run that was not killed - ends it at once, saying
 *  nothing.
 *
 *  Its waits are bounded so it always ends, and no verdict reads them: whatever run this reaper
 *  serves has already ended. */
import { spawnSync } from "node:child_process";
import { existsSync, readFileSync, rmSync } from "node:fs";
import { dirname } from "node:path";
import { setTimeout as sleep } from "node:timers/promises";

import type { Cleanup } from "./harness-reaper";

const named = "e2e/reaper.ts";
/** How long `reap` waits for every awaited process. A `pg_ctl -w start` gives up after its own
 *  PGCTLTIMEOUT, 60 s by default, and this outlasts that. */
const awaitBoundMs = 120_000;
/** How long one command may run: a `pg_ctl -w stop` waits up to that same PGCTLTIMEOUT. */
const commandBoundMs = 120_000;

/** What `reap` signals, reads and runs with. `realCollaborators` are the machine's; the rows of
 *  e2e/__tests__/harness-reaper.test.ts that drive `reap` over a scripted machine pass their own. */
export interface Collaborators {
  /** `process.kill`: throws ESRCH for a process or group that has ended. */
  readonly kill: (pid: number, signal: NodeJS.Signals) => void;
  /** Whether a process still runs. */
  readonly running: (pid: number) => boolean;
  /** Runs one command to its end, and answers why it failed, or undefined when it exited 0. */
  readonly run: (command: readonly string[]) => string | undefined;
  /** One interval between two reads of whether an awaited process still runs. */
  readonly pause: () => Promise<void>;
  /** Milliseconds, as `Date.now()` reads them. */
  readonly now: () => number;
}

/** What `reap` did, as its account says it. */
export interface Reaped {
  /** Process groups it sent SIGKILL to that still had a member. */
  readonly killed: number;
  /** Processes it waited for. */
  readonly awaited: number;
  /** Commands it ran. */
  readonly ran: number;
  /** One line for each thing that went wrong. */
  readonly failures: readonly string[];
}

/** Stops what `entries` hold, in this order:
 *
 *  1. sends SIGKILL to every process group. The process that would have stopped them gracefully
 *     is gone, and SIGKILL is the one signal no server can wait out;
 *  2. waits for every awaited process to end - a `pg_ctl -w start` or a `docker run` that was
 *     still under way - so the commands after it find the server it started, rather than racing
 *     it to a data directory or a container name;
 *  3. runs the commands of each entry, the entries last-added first, as a stack unwinds: a
 *     `pg_ctl stop` before the removal of the run's directory, which was added before any
 *     database in it. */
export async function reap(
  entries: readonly (readonly [string, Cleanup])[],
  machine: Collaborators
): Promise<Reaped> {
  const failures: string[] = [];

  let killed = 0;
  for (const [key, { group }] of entries) {
    if (group === undefined) continue;
    try {
      machine.kill(-group, "SIGKILL");
      killed += 1;
    } catch (error) {
      // ESRCH: every process of the group had already ended.
      if ((error as NodeJS.ErrnoException).code !== "ESRCH") {
        failures.push(`${key}: SIGKILL failed: ${(error as Error).message}`);
      }
    }
  }

  const awaited = entries.flatMap(([key, { awaits }]) =>
    awaits === undefined ? [] : [{ key, pid: awaits }]
  );
  const deadline = machine.now() + awaitBoundMs;
  while (awaited.some(({ pid }) => machine.running(pid)) && machine.now() < deadline) {
    await machine.pause();
  }
  for (const { key, pid } of awaited) {
    if (machine.running(pid)) {
      failures.push(`${key}: process ${pid} still runs after ${awaitBoundMs / 1000} s`);
    }
  }

  let ran = 0;
  for (const [key, { commands = [] }] of [...entries].reverse()) {
    for (const command of commands) {
      const why = machine.run(command);
      ran += 1;
      if (why !== undefined) failures.push(`${key}: \`${command.join(" ")}\` ${why}`);
    }
  }
  return { awaited: awaited.length, failures, killed, ran };
}

/** Whether `pid` is still a running process. A zombie - dead, waiting for the parent it was handed
 *  to after its own died to reap it - has ended, and only `/proc` tells one apart; where there is
 *  no `/proc`, signal 0 answers. A process of another user under the same pid is not ours. */
function running(pid: number): boolean {
  if (existsSync("/proc/self/stat")) {
    let stat: string;
    try {
      stat = readFileSync(`/proc/${pid}/stat`, "utf8");
    } catch {
      return false;
    }
    // The state is the first field after the command name, which ends at the last `)`.
    const state = stat.slice(stat.lastIndexOf(")") + 2, stat.lastIndexOf(")") + 3);
    return state !== "Z" && state !== "X";
  }
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

const realCollaborators: Collaborators = {
  kill: (pid, signal) => process.kill(pid, signal),
  now: () => Date.now(),
  pause: async () => {
    // An orphaned process raises no event in this one, so its end is read again each interval.
    await sleep(50);
  },
  run: ([file, ...args]) => {
    if (file === undefined) return "is empty";
    const result = spawnSync(file, args, {
      encoding: "utf8",
      killSignal: "SIGKILL",
      stdio: ["ignore", "ignore", "pipe"],
      timeout: commandBoundMs,
    });
    if (result.error !== undefined) return result.error.message;
    if (result.status === 0) return undefined;
    return `exited ${result.status ?? result.signal}: ${result.stderr.trim()}`;
  },
  running,
};

/** Reads the list, reaps it, and says what it did. */
async function reapList(list: string, starter: string): Promise<void> {
  let entries: [string, Cleanup][] = [];
  try {
    entries = JSON.parse(readFileSync(list, "utf8")) as [string, Cleanup][];
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
  }
  if (entries.length === 0) return;
  const started = Date.now();
  const { awaited, failures, killed, ran } = await reap(entries, realCollaborators);
  process.stderr.write(
    `${named}: process ${starter} ended without stopping what it started; in ` +
      `${Date.now() - started} ms this sent SIGKILL to ${killed} process groups, waited for ` +
      `${awaited} starts under way and ran ${ran} commands, for ${entries.length} entries ` +
      `(${entries.map(([key]) => key).join(", ")})\n` +
      failures.map((line) => `${named}: ${line}\n`).join("")
  );
}

if (import.meta.main) {
  const [list, starter] = process.argv.slice(2);
  if (list === undefined || starter === undefined) {
    process.stderr.write(`${named}: usage: bun e2e/reaper.ts <list> <pid>\n`);
    process.exit(2);
  }
  // A write to a stderr whose reader has gone must not end this process before its work is done.
  process.on("SIGPIPE", () => {});
  process.stderr.on("error", () => {});
  let ending = false;
  const end = (): void => {
    if (ending) return;
    ending = true;
    reapList(list, starter)
      .catch((error: unknown) => {
        process.stderr.write(
          `${named}: ${error instanceof Error ? (error.stack ?? error.message) : String(error)}\n`
        );
      })
      .finally(() => {
        rmSync(dirname(list), { force: true, recursive: true });
        process.exit(0);
      });
  };
  // Nothing is ever written to stdin; its end, or its failure, is the starter's death.
  process.stdin.on("data", () => {});
  process.stdin.once("end", end);
  process.stdin.once("close", end);
  process.stdin.once("error", end);
}
