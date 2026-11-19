// nativesEnvironment (@legion/test-fixtures/natives-environment) holds a process a test starts
// that loads @oh-my-pi/pi-natives to one Tokio worker. This row starts such a process, as
// extensions/envoy.test.ts's entry row and extensions/envoy-dial-rows-concurrent.test.ts do, and
// the process counts its own threads named tokio-rt-worker once the addon has loaded. Without the
// variable it holds one for each CPU of the machine: 288 on a shared Linux machine on 2026-11-19,
// where a second such process could not start its threads under a limit of 512 tasks. The count
// is read from /proc, so the row runs on Linux alone.
//
// A thread takes its name only once it first runs, and until then carries the name of the thread
// that started it, this process's main thread: in 1 of 20 runs on that machine, under a load of
// 10,768, the count read at once was 0. The runtime starts its workers as the addon loads, so the
// process counts once a worker holds its name and no thread but the main one still holds the main
// one's, which is the moment every worker it started has its name. No event says that a thread of
// the kernel took its name, so the process reads /proc again every 10 ms until then.
import { expect, test } from "bun:test";
import { nativesEnvironment } from "@legion/test-fixtures/natives-environment";
import { command, rowMs, rowOf } from "@legion/test-fixtures/row-waits";

/** What the row waits for, which its limit is the sum of: one process of its own that loads the
 *  addon and exits. */
const theChild = command("the process that loads @oh-my-pi/pi-natives");
const oneChild = [theChild];
/** How long the process waits for its threads to take their names, inside `theChild`'s bound. */
const namedMs = theChild.ms / 2;

const marker = "tokio-rt-worker threads: ";
const script = `
import "@oh-my-pi/pi-utils";
import { readdirSync, readFileSync } from "node:fs";
const main = readFileSync("/proc/self/comm", "utf8").trim();
const names = () =>
  readdirSync("/proc/self/task").map((task) => {
    try {
      return readFileSync("/proc/self/task/" + task + "/comm", "utf8").trim();
    } catch {
      return main;
    }
  });
const deadline = Date.now() + ${namedMs};
for (;;) {
  const now = names();
  const workers = now.filter((name) => name === "tokio-rt-worker").length;
  const unnamed = now.filter((name) => name === main).length - 1;
  if (workers > 0 && unnamed === 0) {
    console.log(${JSON.stringify(marker)} + workers);
    break;
  }
  if (Date.now() > deadline) {
    console.error(workers + " tokio-rt-worker threads and " + unnamed + " threads with no name of their own after ${namedMs / 1000} s");
    process.exit(2);
  }
  await Bun.sleep(10);
}
`;

test.skipIf(process.platform !== "linux")(
  "a process a test starts with nativesEnvironment, which loads pi-natives, holds one Tokio worker",
  () => {
    // The caller's own count, where the shell running this file sets one, would be kept: the row
    // reads the count the helper gives where the caller gives none.
    const { TOKIO_WORKER_THREADS: _given, ...rest } = process.env;
    rowOf(oneChild).charge(theChild);
    const run = Bun.spawnSync([process.execPath, "-e", script], {
      cwd: import.meta.dir,
      env: nativesEnvironment(rest),
      stdout: "pipe",
      stderr: "pipe",
      timeout: theChild.ms,
    });
    const said = run.stdout
      .toString()
      .split("\n")
      .find((line) => line.startsWith(marker));
    if (run.exitCode !== 0 || said === undefined) {
      throw new Error(
        `the process that loaded pi-natives exited ${run.exitCode}${run.signalCode ? ` on ${run.signalCode}` : ""} and counted nothing: ${run.stderr.toString()}`
      );
    }
    expect({ tokioWorkers: Number(said.slice(marker.length)) }).toEqual({ tokioWorkers: 1 });
  },
  rowMs(oneChild)
);
