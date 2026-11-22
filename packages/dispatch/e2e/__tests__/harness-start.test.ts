import { beforeAll, expect, test } from "bun:test";
import { chmodSync, mkdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";

import { rowLimit, runBounded } from "./bounded-subprocess";

// How e2e/harness-start.ts's start ends. Its wait for each listener has no bound of its own
// (`serving`): it ends on the listener's report, on the listener's exit or its failure to start
// at all, or on the caller's abort, whose Interrupted names the listener it waited for. Each row
// runs the start in a child of its own, as a deployed run's (`PLAYWRIGHT_BASE_URL`, read as the
// table is built), which starts the fake Envoy first, then the plain-HTTP proxy, and no server or
// database. The child prints what the start answered or threw.
const module = (name: string) => fileURLToPath(new URL(`../${name}`, import.meta.url));
const stubs = temporaryDirectory("harness-start-");
/** A `bun` that holds the fake Envoy's start for good, once it has sent its parent, the child,
 *  SIGUSR2, and runs every other bun command as the real bun. */
const heldBun = join(stubs, "held");
/** A directory holding nothing, for a PATH on which no `bun` is found. */
const noBun = join(stubs, "empty");
/** A preload under which every read of `Date.now()` is ten minutes later than the one before, as
 *  on a machine so loaded that each step of the start takes that long. */
const leapingClock = join(stubs, "leaping-clock.ts");

beforeAll(() => {
  mkdirSync(heldBun);
  mkdirSync(noBun);
  writeFileSync(
    join(heldBun, "bun"),
    `#!/bin/sh
case "$1" in
  */e2e/fake-envoy.ts) kill -USR2 "$PPID"; exec sleep 600 ;;
esac
exec "$REAL_BUN" "$@"
`
  );
  chmodSync(join(heldBun, "bun"), 0o755);
  writeFileSync(
    leapingClock,
    `const real = Date.now.bind(Date);
let ahead = 0;
Date.now = () => real() + (ahead += 600_000);
`
  );
});

/** Runs `startHarness` in a child under `path`, with `preload` where one is named, aborting the
 *  start on SIGUSR2, and answers what the start answered or threw. */
async function started(path: string, preload?: string) {
  const script =
    `const { startHarness } = await import(${JSON.stringify(module("harness-start.ts"))});` +
    `const { Interrupted } = await import(${JSON.stringify(module("local-instance-support.ts"))});` +
    "const stopping = new AbortController();" +
    'process.once("SIGUSR2", () => stopping.abort());' +
    "try { const harness = await startHarness({ signal: stopping.signal });" +
    " console.log(JSON.stringify({ started: Object.keys(harness.env).sort() })); await harness.stop(); }" +
    "catch (error) { console.log(JSON.stringify({ interrupted: error instanceof Interrupted, " +
    "message: error.message })); }";
  const run = await runBounded({
    argv: [process.execPath, ...(preload ? ["--preload", preload] : []), "--eval", script],
    bound: "short",
    env: {
      PATH: path,
      PLAYWRIGHT_BASE_URL: "http://127.0.0.1:9/",
      REAL_BUN: process.execPath,
      TMPDIR: stubs,
    },
    from: import.meta.url,
  });
  expect(run.code, `${run.named} exited ${run.code}:\n${run.stderr}`).toBe(0);
  return {
    ...run,
    read: JSON.parse(run.stdout) as { started?: string[]; interrupted?: boolean; message?: string },
  };
}

test(
  "a start its caller aborts while a listener has not reported names that listener",
  async () => {
    const { named, read, stderr, stdout } = await started(`${heldBun}:${process.env.PATH ?? ""}`);
    expect(read.interrupted, `${named} printed ${stdout}`).toBe(true);
    expect(read.message, stderr).toMatch(
      /^interrupted \d+\.\d s into the wait, before the fake Envoy reported its port$/
    );
  },
  rowLimit("short")
);

// Under Bun a command that cannot be started leaves `exitCode` and `signalCode` null, so a wait
// that read only those would wait for ever; the start is refused at once, naming the error.
test(
  "a listener whose command is not on PATH is refused at once, naming why it could not start",
  async () => {
    const { named, read, stdout } = await started(noBun);
    expect(read.interrupted, `${named} printed ${stdout}`).toBe(false);
    expect(read.message).toStartWith(
      "The Dispatch e2e harness cannot start: the fake Envoy could not be started: "
    );
    // Bun says `Executable not found in $PATH: "bun"`, and Node `spawn bun ENOENT`.
    expect(read.message).toMatch(/Executable not found in \$PATH: "bun"|spawn bun ENOENT/);
  },
  rowLimit("short")
);

// The waits keep no clock: a start whose every step reads ten minutes later than the last, which
// failed a bound of 30 s at its first read, still waits for each listener's report.
test(
  "a start under a clock ten minutes later at every read still waits for each listener's report",
  async () => {
    const { named, read, stderr, stdout } = await started(process.env.PATH ?? "", leapingClock);
    expect(read.message, `${named} printed ${stdout}:\n${stderr}`).toBeUndefined();
    expect(read.started).toEqual(["FAKE_ENVOY_PORT", "PLAIN_HTTP_PORT"]);
  },
  rowLimit("short")
);
