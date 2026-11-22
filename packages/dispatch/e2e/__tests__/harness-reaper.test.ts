/** The reaper of e2e/harness-reaper.ts and e2e/reaper.ts: what a starter killed with SIGKILL
 *  leaves running, what one that stopped everything leaves, and the order the reaper works in.
 *
 *  The first three rows each drive a starter of its own, a `bun -e` over this checkout's modules,
 *  the way Playwright's runner and `e2e/harness-up.ts` start the harness: each reads the
 *  starter's first line, then kills it or lets it end. A starter that is to be killed stays up
 *  until its stdin, which this process holds, ends, so one that a failed row never killed ends
 *  with this process. The starter's stderr is its reaper's too, so reading it to its end waits
 *  for both, and holds what the reaper said. Every process a row started is killed by its pid in
 *  the row's `finally`, whatever the row found.
 *
 *  The last two rows drive `reap` over a scripted machine, since the order they read - no command
 *  while a start is still under way - is one a real reaper meets only inside a window no row can
 *  hold open: a start that ends by itself either ends inside it or does not. */
import { afterEach, expect, test } from "bun:test";
import { existsSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";
import type { Subprocess } from "bun";

import type { Cleanup } from "../harness-reaper";
import { type Collaborators, reap } from "../reaper";
import { beforeDeadline, bounds, eventually, pastDeadline, rowLimit } from "./bounded-subprocess";

const makeDirectory = temporaryDirectories("harness-reaper");

const supervisorModule = fileURLToPath(new URL("../local-instance-support.ts", import.meta.url));
const reaperModule = fileURLToPath(new URL("../harness-reaper.ts", import.meta.url));

/** How long each `sleep` a row starts runs, in seconds: longer than the row, so a sleep the row
 *  finds gone was ended by what the row tests, never by its own end. */
const outlastsRow = rowLimit("short", "short", "short", "short") / 1000;

/** True while any process of group `pgid` is alive: signal 0 to the group reaches every member. */
function groupAlive(pgid: number): boolean {
  try {
    process.kill(-pgid, 0);
    return true;
  } catch {
    return false;
  }
}

function processAlive(pid: number): boolean {
  try {
    process.kill(pid, 0);
    return true;
  } catch {
    return false;
  }
}

/** Sends SIGKILL to `pid`, a process or, when negative, a process group, where it is still alive:
 *  what a row leaves behind when it fails. ESRCH is a process already gone. */
function killLeft(pid: number | undefined): void {
  if (pid === undefined) return;
  try {
    process.kill(pid, "SIGKILL");
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ESRCH") throw error;
  }
}

type Starter = Subprocess<"pipe", "pipe", "pipe">;

const starters: Starter[] = [];
afterEach(() => {
  for (const starter of starters.splice(0)) killLeft(starter.pid);
});

/** What a starter that is to be killed runs last: it stays up until its stdin ends. */
const untilKilled = "for await (const _ of process.stdin) {}";

/** Starts a starter that has `spawn`, `readFileSync`, `Supervisor` and the reaper's module as
 *  `reaper` in scope, runs `body`, which prints one line of JSON first, and answers that line
 *  with the starter. */
async function startStarter(
  body: string
): Promise<{ readonly starter: Starter; readonly said: Record<string, number | string> }> {
  const script = [
    `import { spawn } from "node:child_process";`,
    `import { readFileSync } from "node:fs";`,
    `import { Supervisor } from ${JSON.stringify(supervisorModule)};`,
    `import * as reaper from ${JSON.stringify(reaperModule)};`,
    body,
  ].join("\n");
  const starter = Bun.spawn(["bun", "-e", script], {
    stderr: "pipe",
    stdin: "pipe",
    stdout: "pipe",
  });
  starters.push(starter);
  const reader = starter.stdout.getReader();
  const decoder = new TextDecoder();
  let text = "";
  const deadline = performance.now() + bounds.short;
  while (!text.includes("\n")) {
    const chunk = await beforeDeadline(reader.read(), deadline);
    if (chunk === pastDeadline || chunk.done) {
      const stderr = await beforeDeadline(new Response(starter.stderr).text(), deadline);
      throw new Error(
        `the starter printed no line within the short bound: ${JSON.stringify(text)}; its ` +
          `stderr: ${stderr === pastDeadline ? "(unread)" : stderr}`
      );
    }
    text += decoder.decode(chunk.value, { stream: true });
  }
  reader.releaseLock();
  return { said: JSON.parse(text.slice(0, text.indexOf("\n"))), starter };
}

/** The starter's stderr to its end, which comes once the starter and its reaper have both ended:
 *  the reaper holds it too. */
async function stderrToEnd(starter: Starter): Promise<string> {
  const text = await beforeDeadline(
    new Response(starter.stderr).text(),
    performance.now() + bounds.short
  );
  if (text === pastDeadline) {
    throw new Error("the starter's stderr did not end within the short bound: its reaper runs on");
  }
  return text;
}

/** The pid a child process writes to `path` as one whole line. */
async function pidFrom(path: string): Promise<number> {
  const read = () => (existsSync(path) ? readFileSync(path, "utf8") : "");
  await eventually(`a pid in ${path}`, () => /^\d+\n$/u.test(read()));
  return Number(read());
}

test(
  "a starter killed with SIGKILL leaves no group it started running, a grandchild included, " +
    "while a process it never handed over lives on",
  async () => {
    const directory = makeDirectory();
    const grandchildFile = join(directory, "grandchild.pid");
    const { said, starter } = await startStarter(`
      await reaper.reaperStarted();
      const made = new Supervisor();
      const leader = made.startPiped("leader", ["bash", "-c", "sleep ${outlastsRow} & echo $! > ${grandchildFile}; wait"], "/", process.env);
      // The control: the same kind of group, started beside the Supervisor's and handed to no one.
      const stray = spawn("sleep", ["${outlastsRow}"], { detached: true, stdio: "ignore" });
      console.log(JSON.stringify({ leader: leader.child.pid, stray: stray.pid }));
      ${untilKilled}
    `);
    const leader = Number(said.leader);
    const stray = Number(said.stray);
    let grandchild: number | undefined;
    try {
      grandchild = await pidFrom(grandchildFile);
      const alone = grandchild;
      expect(groupAlive(leader)).toBe(true);
      expect(processAlive(stray)).toBe(true);

      process.kill(starter.pid, "SIGKILL");
      await eventually(
        `the group of the leader ${leader}, its grandchild ${alone} included, to end after its ` +
          `starter ${starter.pid} was killed`,
        () => !groupAlive(leader) && !processAlive(alone)
      );
      const reported = await stderrToEnd(starter);
      expect(reported).toContain(
        `e2e/reaper.ts: process ${starter.pid} ended without stopping what it started`
      );
      expect(reported).toContain(`process group ${leader}`);
      // The same kill and the same read find the group nobody handed over still running: the
      // group above ended by the reaper's hand, not by the kill of its starter.
      expect(processAlive(stray), `the stray process ${stray} ended with its starter`).toBe(true);
    } finally {
      killLeft(-leader);
      killLeft(grandchild);
      killLeft(stray);
    }
  },
  // Four waits the short bound ends: the starter's line, the grandchild's pid, the group's end,
  // the end of the starter's stderr.
  rowLimit("short", "short", "short", "short")
);

test(
  "a starter that stopped what it started ends by itself, and its reaper ends then, stopping nothing",
  async () => {
    const { said, starter } = await startStarter(`
      const reaperPid = await reaper.reaperStarted();
      const list = readFileSync("/proc/" + reaperPid + "/cmdline", "utf8").split("\\0")[2];
      const made = new Supervisor();
      const leader = made.startPiped("leader", ["sleep", "${outlastsRow}"], "/", process.env);
      console.log(JSON.stringify({ leader: leader.child.pid, list, reaper: reaperPid }));
      await made.stop();
    `);
    const leader = Number(said.leader);
    const reaperPid = Number(said.reaper);
    try {
      // The starter's end is its own: neither the reaper nor the pipe to it holds it open.
      const code = await beforeDeadline(starter.exited, performance.now() + bounds.short);
      expect(code, "the starter did not end within the short bound").toBe(0);
      const stderr = await stderrToEnd(starter);
      expect(stderr).not.toContain("e2e/reaper.ts");
      await eventually(`the reaper ${reaperPid} to end`, () => !processAlive(reaperPid));
      expect(existsSync(dirname(String(said.list))), "the reaper left its list behind").toBe(false);
      expect(groupAlive(leader)).toBe(false);
    } finally {
      killLeft(-leader);
      killLeft(reaperPid);
    }
  },
  // Four waits the short bound ends: the starter's line, its end, the end of its stderr, and the
  // reaper's own end.
  rowLimit("short", "short", "short", "short")
);

test(
  "the reaper runs the commands of what is still handed over, the last handed over first",
  async () => {
    const directory = makeDirectory();
    const order = join(directory, "order");
    const append = (word: string) => JSON.stringify(["sh", "-c", `echo ${word} >> "$0"`, order]);
    const { starter } = await startStarter(`
      await reaper.reaperStarted();
      reaper.reapIfAbandoned("first", { commands: [${append("first")}] });
      reaper.reapIfAbandoned("second", { commands: [${append("second")}] });
      reaper.reapIfAbandoned("dropped", { commands: [${append("dropped")}] });
      reaper.reapIfAbandoned("third", { commands: [${append("third")}, ${append("third-again")}] });
      // A key handed over again keeps its place and takes its new commands.
      reaper.reapIfAbandoned("first", { commands: [${append("first-again")}] });
      reaper.stopReaping("dropped");
      console.log("{}");
      ${untilKilled}
    `);
    process.kill(starter.pid, "SIGKILL");
    const stderr = await stderrToEnd(starter);
    expect(stderr).toContain("waited for 0 starts under way and ran 4 commands, for 3 entries");
    expect(readFileSync(order, "utf8")).toBe("third\nthird-again\nsecond\nfirst-again\n");
  },
  // Two waits the short bound ends: the starter's line and the end of its stderr.
  rowLimit("short", "short")
);

/** A machine `reap` drives in place of this one: every call it makes, in order, in `events`.
 *  `running` answers for each pid from `runs`, the number of reads that find it still running;
 *  a pid absent from it has ended. `kill` of a group named in `gone` answers ESRCH. Each `pause`
 *  moves the clock 50 ms on, so a wait that never ends reaches the bound with no real time. */
function scripted(runs: Record<number, number>, gone: readonly number[] = []) {
  const events: string[] = [];
  const reads = new Map<number, number>();
  let clock = 0;
  const machine: Collaborators = {
    kill: (pid, signal) => {
      events.push(`kill ${pid} ${signal}`);
      if (gone.includes(-pid)) throw Object.assign(new Error("no such process"), { code: "ESRCH" });
    },
    now: () => clock,
    pause: async () => {
      clock += 50;
    },
    run: (command) => {
      events.push(`run ${command.join(" ")}`);
      return command[0] === "docker" ? "exited 1: Error response from daemon" : undefined;
    },
    running: (pid) => {
      const read = (reads.get(pid) ?? 0) + 1;
      reads.set(pid, read);
      const still = read <= (runs[pid] ?? 0);
      events.push(`read ${pid} ${still ? "running" : "ended"}`);
      return still;
    },
  };
  return { events, machine };
}

const entries: readonly (readonly [string, Cleanup])[] = [
  ["run directory", { commands: [["rm", "-rf", "dir"]] }],
  ["group 10", { group: 10 }],
  ["Postgres a", { awaits: 40, commands: [["pg_ctl", "stop", "a"]] }],
  ["group 30", { group: 30 }],
  ["container b", { awaits: 41, commands: [["docker", "rm", "b"], ["true"]] }],
];

test("reap sends every SIGKILL first, runs no command while a start is under way, and unwinds the entries", async () => {
  // Process 40 is found running on its first two reads; 41 has ended; group 30 has no member.
  const { events, machine } = scripted({ 40: 2 }, [30]);
  const reaped = await reap(entries, machine);
  expect(events).toEqual([
    "kill -10 SIGKILL",
    "kill -30 SIGKILL",
    "read 40 running",
    "read 40 running",
    "read 40 ended",
    "read 41 ended",
    "read 40 ended",
    "read 41 ended",
    "run docker rm b",
    "run true",
    "run pg_ctl stop a",
    "run rm -rf dir",
  ]);
  expect(reaped).toEqual({
    awaited: 2,
    failures: ["container b: `docker rm b` exited 1: Error response from daemon"],
    killed: 1,
    ran: 4,
  });
});

test("reap gives a start that never ends up at its bound, names it, and still runs every command", async () => {
  const { events, machine } = scripted({ 40: Number.POSITIVE_INFINITY });
  const reaped = await reap(entries, machine);
  expect(reaped.failures).toEqual([
    "Postgres a: process 40 still runs after 120 s",
    "container b: `docker rm b` exited 1: Error response from daemon",
  ]);
  expect(events.filter((event) => event.startsWith("run "))).toEqual([
    "run docker rm b",
    "run true",
    "run pg_ctl stop a",
    "run rm -rf dir",
  ]);
  // 120 s of 50 ms pauses, each followed by one read of process 40, and the read that names it.
  expect(events.filter((event) => event === "read 40 running")).toHaveLength(2_400 + 2);
});
