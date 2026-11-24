import { expect, test } from "bun:test";
import { tmpdir } from "node:os";
import { fileURLToPath } from "node:url";
import { hostTool, requireOrSkip } from "@legion/test-fixtures/host-tools";
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories";

import { stacksVariable } from "../harness-ports";
import { rowLimit, runBounded } from "./bounded-subprocess";

/** The row of the harness's stacks that needs a PostgreSQL, in a file of its own so that
 *  harness-stacks.test.ts, whose rows need none, runs where the dashboard's other rows run
 *  (`bun run test`), and this file runs where a server is (`bun run test:postgres`, which
 *  dispatch-browser.yaml's postgres-rows job runs in its full lane). */

const psqlModule = fileURLToPath(new URL("../psql.ts", import.meta.url));
const dispatchPackage = fileURLToPath(new URL("../..", import.meta.url));

// Every child this file starts reads this home rather than the caller's, so no cache,
// configuration or credential of the developer's own can change what a case here proves.
const home = temporaryDirectory("harness-stacks-postgres-home");

function run(argv: string[], env: Record<string, string>) {
  return runBounded({
    argv,
    bound: "short",
    cwd: dispatchPackage,
    env: {
      PATH: process.env.PATH ?? "",
      HOME: home,
      // The child writes under its temporary directory, which on a machine whose TMPDIR names
      // the only writable volume is not /tmp.
      TMPDIR: tmpdir(),
      ...env,
    },
    from: import.meta.url,
  });
}

// The database a stack reads is the psql it starts, so the case that proves which one it picked
// needs psql and a live database to answer; without either it would prove nothing and is
// skipped, as e2e/__tests__/psql-session.test.ts's cases are, and on GitHub Actions it fails, and
// its two halves are composed into one line for the reason that file gives. The case runs
// `select 1` and writes nothing, so it runs on the server's own database rather than one of its
// own.
const live = (process.env.LEGION_TEST_PG_DSN ?? "").trim();
const missingForDatabase = [
  ...(hostTool("psql") === null ? ["psql on this host's PATH"] : []),
  ...(live === "" ? ["LEGION_TEST_PG_DSN, naming a server this run may read"] : []),
];
const withoutDatabase = requireOrSkip(
  missingForDatabase.join("; "),
  missingForDatabase.length === 0,
  "dispatch-browser"
);

test.skipIf(withoutDatabase)(
  "psql reads its own stack's database",
  async () => {
    // A process of its own per case, which imports e2e/psql.ts in the environment under test: it
    // resolves the database once, when it starts its psql.
    const resolve = [
      "bun",
      "-e",
      `await (await import(${JSON.stringify(psqlModule)})).sql("select 1")`,
    ];
    const dead = "postgres://postgres@127.0.0.1:1/never-opened?sslmode=disable";
    const stacks = JSON.stringify([{ DATABASE_URL: dead }, { DATABASE_URL: live }]);
    // Stack 1's database answers; the plain name, which stack 1 must not read, is a dead port.
    const stack = await run(resolve, {
      DATABASE_URL: dead,
      [stacksVariable]: stacks,
      TEST_PARALLEL_INDEX: "1",
    });
    expect(`${stack.stderr}${stack.stdout}`).toBe("");
    expect(stack.code).toBe(0);
    // The same environment without the worker's index is stack 0, whose record names the dead one.
    const plain = await run(resolve, { DATABASE_URL: live, [stacksVariable]: stacks });
    expect(plain.code).not.toBe(0);
  },
  rowLimit("short", "short")
);
