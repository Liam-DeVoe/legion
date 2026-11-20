import { afterEach, expect, test } from "bun:test";
import { existsSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { Subprocess } from "bun";
import { processEnded, processState } from "./process-state";

/** How long a row waits for a process it started to reach the state it is about: a sleep to
 *  start sleeping, a shell to exec, a process to end. Each such wait polls, since another
 *  process's state raises no event here; the rows' own limit is above it. */
const WAIT_MS = 10_000;

/** Polls `holds` until it is true, and fails naming `what` once `WAIT_MS` has passed first. */
async function until(what: string, holds: () => boolean): Promise<void> {
  const deadline = Date.now() + WAIT_MS;
  while (!holds()) {
    if (Date.now() > deadline)
      throw new Error(`waited ${WAIT_MS} ms for ${what}, and it never came`);
    await Bun.sleep(10);
  }
}

const started: Subprocess[] = [];
const directories: string[] = [];
afterEach(() => {
  for (const child of started.splice(0)) child.kill("SIGKILL");
  for (const directory of directories.splice(0))
    rmSync(directory, { force: true, recursive: true });
});

function scratch(): string {
  const directory = mkdtempSync(join(tmpdir(), "process-state-"));
  directories.push(directory);
  return directory;
}

/** The pid a process wrote to `file` as one whole line, once it has. */
async function pidIn(file: string): Promise<number> {
  const read = () => (existsSync(file) ? readFileSync(file, "utf8") : "");
  await until(`a pid in ${file}`, () => /^\d+\n$/.test(read()));
  return Number(read());
}

test(
  "a process that runs has not ended, and one its parent reaped has",
  async () => {
    const sleep = Bun.spawn(["sleep", "30"]);
    started.push(sleep);
    expect(processEnded(sleep.pid)).toBe(false);
    sleep.kill("SIGKILL");
    // The runtime reaps its own child before `exited` settles, so /proc lists it no more.
    await sleep.exited;
    expect(processState(sleep.pid)).toBeUndefined();
    expect(processEnded(sleep.pid)).toBe(true);
  },
  2 * WAIT_MS
);

// The case the predicate exists for: a process that has exited and that nothing reaps, which
// signal 0 still reaches. Its parent here is a `sleep` that never waits for a child: the shell
// starts the child and then becomes that `sleep` by `exec`, and the child exits only once the row
// has seen that exec, so no shell is left to reap it first.
test(
  "a zombie has ended, though signal 0 still reaches it",
  async () => {
    const directory = scratch();
    const go = join(directory, "go");
    const pidFile = join(directory, "child.pid");
    const parent = Bun.spawn([
      "bash",
      "-c",
      `sh -c 'until [ -e "$0" ]; do sleep 0.01; done' "$1" & echo $! > "$2"; exec sleep 30`,
      "parent",
      go,
      pidFile,
    ]);
    started.push(parent);
    const child = await pidIn(pidFile);
    await until(`the shell ${parent.pid} to exec sleep`, () => {
      try {
        return readFileSync(`/proc/${parent.pid}/comm`, "utf8").trim() === "sleep";
      } catch {
        return false;
      }
    });
    expect(processEnded(child)).toBe(false);
    writeFileSync(go, "");
    await until(`the child ${child} to become a zombie`, () => processState(child) === "Z");
    expect(() => process.kill(child, 0), "signal 0 no longer reaches the zombie").not.toThrow();
    expect(processEnded(child)).toBe(true);
  },
  3 * WAIT_MS
);

// Field 3 is read after the command name's last `)`: a name that holds `) R (` would read as
// running, state `R`, to a reader that took the first `)`, while the sleep is asleep, `S`.
test(
  "the state is read after the last `)` of the command name, which may hold one of its own",
  async () => {
    const sleepPath = Bun.which("sleep");
    if (sleepPath === null) throw new Error("this machine has no sleep on its PATH");
    const named = join(scratch(), "a) R (b");
    symlinkSync(sleepPath, named);
    const sleep = Bun.spawn([named, "30"]);
    started.push(sleep);
    await until(`the sleep ${sleep.pid} to read as asleep`, () => processState(sleep.pid) === "S");
    expect(readFileSync(`/proc/${sleep.pid}/stat`, "utf8")).toContain("(a) R (b) S ");
  },
  2 * WAIT_MS
);

test(
  "where /proc lists nothing, signal 0 answers: a process that runs, or none",
  async () => {
    const none = () => undefined;
    expect(processEnded(process.pid, none)).toBe(false);
    const exited = Bun.spawn(["true"]);
    await exited.exited;
    expect(processEnded(exited.pid, none)).toBe(true);
  },
  WAIT_MS
);

test("a pid that is no positive integer is refused, not read as a process", () => {
  for (const pid of [0, -1, 1.5, Number.NaN]) {
    expect(() => processEnded(pid), String(pid)).toThrow("is no process id");
  }
});
