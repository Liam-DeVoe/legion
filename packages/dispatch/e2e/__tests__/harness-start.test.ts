import { beforeAll, expect, test } from "bun:test";
import { chmodSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";

import { rowLimit, runBounded } from "./bounded-subprocess";

// What e2e/harness-start.ts's start does when its caller aborts it while a listener has not
// reported its port. The start's wait has no bound of its own (`serving`), so the caller's abort
// is its hang guard, and the Interrupted the start rejects with names the listener it waited for
// rather than saying "interrupted" alone. The start runs in a child of its own, as a deployed
// run's (`PLAYWRIGHT_BASE_URL`, read as the table is built), which starts the fake Envoy first and
// no server or database. A stand-in `bun` first on the child's PATH holds the fake Envoy's start
// for good, after it sends its parent, the child, SIGUSR2, and runs every other bun command as the
// real bun; the child aborts the start on that signal, so the abort lands while the fake Envoy is
// held.
const module = (name: string) => fileURLToPath(new URL(`../${name}`, import.meta.url));
const stubs = temporaryDirectory("harness-start-");

beforeAll(() => {
  writeFileSync(
    join(stubs, "bun"),
    `#!/bin/sh
case "$1" in
  */e2e/fake-envoy.ts) kill -USR2 "$PPID"; exec sleep 600 ;;
esac
exec "$REAL_BUN" "$@"
`
  );
  chmodSync(join(stubs, "bun"), 0o755);
});

test(
  "a start its caller aborts while a listener has not reported names that listener",
  async () => {
    const script =
      `const { startHarness } = await import(${JSON.stringify(module("harness-start.ts"))});` +
      `const { Interrupted } = await import(${JSON.stringify(module("local-instance-support.ts"))});` +
      "const stopping = new AbortController();" +
      'process.once("SIGUSR2", () => stopping.abort());' +
      "try { await startHarness({ signal: stopping.signal }); console.log('{\"started\":true}'); }" +
      "catch (error) { console.log(JSON.stringify({ interrupted: error instanceof Interrupted, " +
      "message: error.message })); }";
    const { code, named, stderr, stdout } = await runBounded({
      argv: [process.execPath, "--eval", script],
      bound: "short",
      env: {
        PATH: `${stubs}:${process.env.PATH ?? ""}`,
        PLAYWRIGHT_BASE_URL: "http://127.0.0.1:9/",
        REAL_BUN: process.execPath,
        TMPDIR: stubs,
      },
      from: import.meta.url,
    });
    expect(code, `${named} exited ${code}:\n${stderr}`).toBe(0);
    const read = JSON.parse(stdout) as { interrupted?: boolean; message?: string };
    expect(read.interrupted, `${named} printed ${stdout}`).toBe(true);
    expect(read.message, stderr).toMatch(
      /^interrupted \d+\.\d s into the wait, before the fake Envoy reported its port$/
    );
  },
  rowLimit("short")
);
