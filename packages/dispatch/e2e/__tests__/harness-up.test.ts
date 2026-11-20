import { afterAll, expect, test } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

import { runBounded } from "./bounded-subprocess";

// What e2e/harness-up.ts does with a Ctrl-C that lands the moment its stack is up, after the pool
// has taken its own SIGINT listener off (`unwatchInterrupt`) and before anything else has run:
// it stops the stack, rather than dying of the signal's default action and leaving the stack's
// detached processes serving. The script runs whole, under Bun, with a stand-in for
// e2e/harness-pool.ts loaded in that module's place by a preload: the stand-in's start arms and
// takes off a listener of its own, as the pool's does, then sends this process SIGINT before it
// returns the stack, and its stop prints a line. A script that arms its own listener only after
// the start returns has none at that moment, so the signal's default ends it and the stop never
// runs.
const dispatchPackage = fileURLToPath(new URL("../..", import.meta.url));
const stubs = mkdtempSync(join(tmpdir(), "harness-up-"));
afterAll(() => rmSync(stubs, { force: true, recursive: true }));

const pool = join(stubs, "harness-pool.ts");
writeFileSync(
  pool,
  `export async function startStacks(count: number) {
  const onInterrupt = (): void => {};
  process.on("SIGINT", onInterrupt);
  await Promise.resolve();
  process.off("SIGINT", onInterrupt);
  console.error(\`stand-in: \${count} stack up, sending SIGINT\`);
  process.kill(process.pid, "SIGINT");
  return [{ env: { STAND_IN_PORT: "5433" } }];
}

export async function stopStacks(keepLogs: boolean) {
  console.log(\`stand-in: stopped, keepLogs=\${keepLogs}\`);
}
`
);
const preload = join(stubs, "preload.ts");
writeFileSync(
  preload,
  `import { plugin } from "bun";
import { readFileSync } from "node:fs";

plugin({
  name: "harness-pool stand-in",
  setup(build) {
    build.onLoad({ filter: /\\/e2e\\/harness-pool\\.ts$/ }, () => ({
      contents: readFileSync(${JSON.stringify(pool)}, "utf8"),
      loader: "ts",
    }));
  },
});
`
);

test("a Ctrl-C the moment the stack is up stops the stack, not the script alone", async () => {
  const run = await runBounded({
    argv: [process.execPath, "--preload", preload, "e2e/harness-up.ts"],
    bound: "short",
    cwd: dispatchPackage,
    env: { HOME: process.env.HOME, PATH: process.env.PATH, TMPDIR: stubs },
    from: import.meta.url,
  });
  expect(run.stderr, "the stand-in pool was not the one loaded").toContain(
    "stand-in: 1 stack up, sending SIGINT"
  );
  expect(run.stdout).toContain("export STAND_IN_PORT=5433");
  expect(run.stdout).toContain("stand-in: stopped, keepLogs=false");
  expect(run.code, `${run.named} ended ${run.code}:\n${run.stderr}`).toBe(0);
});
