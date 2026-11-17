// Red test of acceptance round 5 of PR #4236 (head c6b0c888d4), finding D1 of Acc5Class.
// It belongs at packages/pi-envoy/extensions/envoy-dial-rows-concurrent.test.ts.
//
// Two runs of one test file on one machine are an honest setup: two agents on a shared dev box, or a
// developer's watch run beside a full `bun run test`. The two rows of extensions/envoy.test.ts that
// count what dials nats.js's default 127.0.0.1:4222 must count in each run: no run's rows fail because
// another program (the other run of the same file, a local NATS) holds 127.0.0.1:4222, and none skips.
// At c6b0c888d4 the file binds 127.0.0.1:4222 while it collects its rows and holds it to afterAll, so
// the second run's two rows fail ("127.0.0.1:4222 is held by ..."); before 7ca0c3ec64 they skipped.
//
// On a shared machine, run this file in a private network namespace: the second row holds
// 127.0.0.1:4222 for its own child run, which would make another user's run of envoy.test.ts fail.
import { expect, test } from "bun:test";
import { join } from "node:path";

const packageDirectory = join(import.meta.dir, "..");
const rows = "nothing dials nats.js's default 127.0.0.1:4222";
const rowNames = [
  `(pass) envoy OMP extension > with its NATS seed refused (envoy.natsUrls other than ENVOY_NATS_URL's), ${rows}`,
  `(pass) envoy OMP extension > with no NATS server, ${rows}`,
];

/** One run of the two dial rows, as a developer runs them, in a process of its own. */
async function runDialRows(): Promise<{ readonly code: number; readonly output: string }> {
  const child = Bun.spawn([process.execPath, "test", "extensions/envoy.test.ts", "-t", rows], {
    cwd: packageDirectory,
    env: process.env,
    stdout: "pipe",
    stderr: "pipe",
  });
  const [stdout, stderr, code] = await Promise.all([
    new Response(child.stdout).text(),
    new Response(child.stderr).text(),
    child.exited,
  ]);
  return { code, output: stdout + stderr };
}

function expectBothRowsCounted(run: { readonly code: number; readonly output: string }): void {
  for (const name of rowNames) expect(run.output).toContain(name);
  expect(run.output).not.toMatch(
    /\((fail|skip)\) envoy OMP extension > .*nothing dials nats\.js's default/
  );
  expect(run.code).toBe(0);
}

test("two runs of the dial rows started at the same moment both count dials: neither fails nor skips", async () => {
  const [first, second] = await Promise.all([runDialRows(), runDialRows()]);
  expectBothRowsCounted(first);
  expectBothRowsCounted(second);
}, 120_000);

test("the dial rows count dials while another program holds 127.0.0.1:4222", async () => {
  // The other program: a listener of this test's own, or whatever already holds the port.
  let held: { stop(closeActiveConnections?: boolean): void } | undefined;
  try {
    held = Bun.listen({ hostname: "127.0.0.1", port: 4222, socket: { data() {} } });
  } catch (error) {
    if (!(error instanceof Error && "code" in error && error.code === "EADDRINUSE")) throw error;
  }
  try {
    expectBothRowsCounted(await runDialRows());
  } finally {
    held?.stop(true);
  }
}, 120_000);
