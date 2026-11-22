import { beforeAll, expect, test } from "bun:test";
import { chmodSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";

import { rowLimit, runBounded } from "./bounded-subprocess";

// What e2e/harness-postgres.ts's wait for a stack's Postgres (`ready`) does while the server
// refuses every `select 1`, as one still starting up does. The wait has no bound of its own, so it
// ends on the caller's abort, with an Interrupted that names the server it waited for, or on the
// server's end, which the caller hands it (`gone`), with a refusal that says so. Each row runs the
// wait in a child of its own, with a stand-in `psql` first on its PATH that answers every query
// as a server starting up does and exits 2. The stand-in sends its parent, the child, SIGUSR2 the
// first time it runs, so the abort lands while the wait is under way.
const module = (name: string) => fileURLToPath(new URL(`../${name}`, import.meta.url));
const stubs = temporaryDirectory("harness-postgres-");

beforeAll(() => {
  writeFileSync(
    join(stubs, "psql"),
    `#!/bin/sh
[ -e "$PSQL_MARK" ] || { : > "$PSQL_MARK"; kill -USR2 "$PPID"; }
echo 'psql: error: connection to server at "127.0.0.1", port 9 failed: FATAL:  the database system is starting up' >&2
exit 2
`
  );
  chmodSync(join(stubs, "psql"), 0o755);
});

/** Runs `ready` against the stand-in in a child, `gone` the body of the function it is handed,
 *  aborting on the stand-in's first call where `abort` is set, and answers what it threw. */
async function waited(gone: string, abort: boolean, mark: string) {
  const script =
    `const { ready } = await import(${JSON.stringify(module("harness-postgres.ts"))});` +
    `const { Interrupted } = await import(${JSON.stringify(module("local-instance-support.ts"))});` +
    "const stopping = new AbortController();" +
    (abort
      ? 'process.once("SIGUSR2", () => stopping.abort());'
      : 'process.on("SIGUSR2", () => {});') +
    'try { await ready("postgres://postgres@127.0.0.1:9/none?sslmode=disable", ' +
    `"the stand-in's Postgres", () => ${gone}, stopping.signal); console.log('{"answered":true}'); }` +
    "catch (error) { console.log(JSON.stringify({ interrupted: error instanceof Interrupted, " +
    "message: error.message })); }";
  const run = await runBounded({
    argv: [process.execPath, "--eval", script],
    bound: "short",
    env: {
      PATH: `${stubs}:${process.env.PATH ?? ""}`,
      PSQL_MARK: join(stubs, mark),
      TMPDIR: stubs,
    },
    from: import.meta.url,
  });
  expect(run.code, `${run.named} exited ${run.code}:\n${run.stderr}`).toBe(0);
  return {
    ...run,
    read: JSON.parse(run.stdout) as { answered?: true; interrupted?: boolean; message?: string },
  };
}

test(
  "a wait its caller aborts while the server refuses names the server it waited for",
  async () => {
    const { named, read, stdout } = await waited("false", true, "aborted");
    expect(read.interrupted, `${named} printed ${stdout}`).toBe(true);
    expect(read.message).toMatch(
      /^interrupted \d+\.\d s into the wait, before the stand-in's Postgres answered select 1$/
    );
  },
  rowLimit("short")
);

test(
  "a wait whose server has ended stops at the next refused try, naming it and the refusal",
  async () => {
    const { named, read, stdout } = await waited("true", false, "gone");
    expect(read.interrupted, `${named} printed ${stdout}`).toBe(false);
    expect(read.message).toStartWith(
      "The Dispatch e2e harness cannot start: the stand-in's Postgres stopped before it answered select 1: "
    );
    expect(read.message).toContain("the database system is starting up");
  },
  rowLimit("short")
);
