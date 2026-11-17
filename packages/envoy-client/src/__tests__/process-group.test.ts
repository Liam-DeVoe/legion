import { afterAll, expect, test } from "bun:test";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";

const scratch = mkdtempSync(path.join(tmpdir(), "process-group-"));
afterAll(() => rmSync(scratch, { recursive: true, force: true }));

// The watch kills the group its command leads. A caller that forgot `detached: true` runs the
// command in its own group, and a watch that killed "its own group" would kill that caller once it
// closed the pipe after a normal run. Run in a process of its own, in a group of its own, so a
// wrong watch kills that process and not this test's runner.
test("a watched command spawned without a group of its own kills nothing of its caller's", async () => {
  const caller = `
import { groupWatchedBy } from ${JSON.stringify(path.resolve(import.meta.dir, "../process-group.ts"))};
const child = Bun.spawn(groupWatchedBy(["/bin/sh", "-c", "exit 0"]), {
  stdin: "pipe",
  stdout: "ignore",
  stderr: "ignore",
});
await child.exited;
void child.stdin.end();
// The watch sees the pipe close now; give it the moment it needs to act.
await Bun.sleep(500);
console.log("caller survived");
`;
  const run = Bun.spawn([process.execPath, "-e", caller], {
    cwd: scratch,
    env: { PATH: process.env.PATH ?? "" },
    stdout: "pipe",
    stderr: "pipe",
    detached: true,
  });
  const [out] = await Promise.all([new Response(run.stdout).text(), run.exited]);
  expect(run.signalCode).toBeNull();
  expect(out).toBe("caller survived\n");
}, 20_000);
