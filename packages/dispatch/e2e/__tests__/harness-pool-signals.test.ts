import { afterAll, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { constants, tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import { rowLimit, runBounded } from "./bounded-subprocess";

// What e2e/harness-pool.ts does with a signal no Playwright runner handles, once its stack is up:
// a SIGTERM, a closing terminal's SIGHUP, or its quit key's SIGQUIT. Each stops the stack, once,
// and then ends the process with that signal's own disposition, where without the pool's listener
// its default action ended the process at once and left the stack's detached processes serving.
// A SIGHUP or a SIGQUIT that comes again while the stack stops changes nothing. The pool runs in a
// script of its own, under Bun, with a stand-in for e2e/harness-start.ts loaded in that module's
// place by a preload: the stand-in harness's stop sends STOP_SIGNAL, where one is named, waits until
// that signal is handled, and prints a line. The script starts one stack on a DATABASE_URL it names,
// so no Postgres starts, and then sends itself UP_SIGNAL. It runs under a shell that sets the core
// size to 0, so a SIGQUIT that meets its default action writes no core into the tree.
const dispatchPackage = fileURLToPath(new URL("../..", import.meta.url));
const stubs = mkdtempSync(join(tmpdir(), "harness-pool-signals-"));
afterAll(() => rmSync(stubs, { force: true, recursive: true }));

const start = join(stubs, "harness-start.ts");
writeFileSync(
  start,
  `export async function startHarness() {
  return {
    env: { STAND_IN_PORT: "5433" },
    processes: [],
    firstExit: () => new Promise<never>(() => {}),
    async stop() {
      const again = process.env.STOP_SIGNAL as NodeJS.Signals | undefined;
      if (again) {
        const handled = Promise.withResolvers<void>();
        process.kill(process.pid, again);
        process.once(again, () => handled.resolve());
        await handled.promise;
      }
      console.log("stand-in harness: stopped");
    },
  };
}
`
);
const preload = join(stubs, "preload.ts");
writeFileSync(
  preload,
  `import { plugin } from "bun";
import { readFileSync } from "node:fs";

plugin({
  name: "harness-start stand-in",
  setup(build) {
    build.onLoad({ filter: /\\/e2e\\/harness-start\\.ts$/ }, () => ({
      contents: readFileSync(${JSON.stringify(start)}, "utf8"),
      loader: "ts",
    }));
  },
});
`
);
const script = join(stubs, "pool.ts");
writeFileSync(
  script,
  `import { startStacks } from ${JSON.stringify(join(dispatchPackage, "e2e/harness-pool.ts"))};
await startStacks(1, { env: { DATABASE_URL: "postgres://stand-in.invalid/none" } });
console.log("stand-in: stack up");
process.kill(process.pid, process.env.UP_SIGNAL as NodeJS.Signals);
await new Promise(() => {});
`
);

for (const row of [
  { up: "SIGTERM" },
  { up: "SIGHUP" },
  { up: "SIGQUIT" },
  { up: "SIGHUP", stop: "SIGHUP" },
  { up: "SIGQUIT", stop: "SIGHUP" },
  { up: "SIGTERM", stop: "SIGQUIT" },
] as const) {
  test(
    `a ${row.up}${"stop" in row ? ` and a ${row.stop} while the stack stops` : ""} stops the pool's stack once and ends the process by the ${row.up}`,
    async () => {
      const run = await runBounded({
        argv: [
          "/bin/sh",
          "-c",
          'ulimit -c 0 && exec "$@"',
          "sh",
          process.execPath,
          "--preload",
          preload,
          script,
        ],
        bound: "short",
        cwd: dispatchPackage,
        env: {
          HOME: process.env.HOME,
          PATH: process.env.PATH,
          STOP_SIGNAL: "stop" in row ? row.stop : undefined,
          TMPDIR: stubs,
          UP_SIGNAL: row.up,
        },
        from: import.meta.url,
      });
      expect(run.stdout, `${run.named}:\n${run.stderr}`).toContain("stand-in: stack up");
      expect(
        run.stdout.split("\n").filter((line) => line === "stand-in harness: stopped"),
        `${run.named} did not stop the stack exactly once:\n${run.stderr}`
      ).toHaveLength(1);
      expect(run.code, `${run.named} ended ${run.code}:\n${run.stderr}`).toBe(
        128 + constants.signals[row.up]
      );
    },
    rowLimit("short")
  );
}
