/** What `e2e/acceptance/node-projects.sh` takes for a transaction of its own database holding the
 *  event horizon back, on which its wait for the server's object pass fails at once: a holder of
 *  the observation `observe_horizon` takes after the driver's event commits, which
 *  `own_horizon_hold` names once it has held that observation back for 5 s
 *  (`horizontest.OwnHoldBound`). Both are functions of `e2e/acceptance/lib.sh`, and each row runs
 *  them in a bash that sourced it, against a database of this file alone.
 *
 *  The rule is horizontest's own (`Warning.heldFor`, docs/testing.md, "A test that waits for the
 *  event feed's first frame"): a hold lasts the shorter of the pending observation's age and the
 *  holder's own. A transaction holds the observation back only while it holds a transaction id
 *  drawn before the observer's, since the horizon's proof waits only on those
 *  (`events/horizon.go`, "Safe horizon"). So a transaction that read for longer than the bound and
 *  drew its first id after the observation holds it back not at all, however old its xact_start;
 *  a transaction that drew its id before the observation is named only once the observation, too,
 *  has waited the bound.
 *
 *  Each row holds its transaction on this process's `sql` session (`e2e/psql.ts`), pointed at the
 *  file's database. The functions read the hold on the database's own clock (xact_start, now()),
 *  which no fake timer of this process can move, so a row waits on that clock, with
 *  `pg_sleep_until`, until the moment the bound has passed for what it holds, and no longer. The
 *  rows need psql and a server they may make a database on, and skip where LEGION_TEST_PG_DSN
 *  names none, as in CI's `dispatch-ui` job (psql-session.test.ts says the same). */

import { afterAll, beforeAll, expect, test } from "bun:test";
import { randomUUID } from "node:crypto";
import { fileURLToPath } from "node:url";
import { captureEnvironment } from "@legion/test-fixtures/environment";
import { hostTool } from "@legion/test-fixtures/host-tools";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";

import { dropDatabase, resetSession, sql, sqlAt } from "../psql";
import { rowLimit, runBounded } from "./bounded-subprocess";

const dispatchPackage = fileURLToPath(new URL("../..", import.meta.url));

/** The server this run may make a database on, as psql-session.test.ts reads it. */
const server = process.env.LEGION_TEST_PG_DSN ?? "";
const missing = [
  ...(hostTool("psql") === null ? ["psql on this host's PATH"] : []),
  ...(server === ""
    ? ["LEGION_TEST_PG_DSN, naming a server this run may create a database on"]
    : []),
];
const live = missing.length === 0;
if (!live) {
  console.warn(
    `Skipped the tests in ${import.meta.path} that need a database. This run does not have: ` +
      `${missing.join("; ")}. Supply what is missing and run the tests again.`
  );
}

/** The database of this file alone: `own_horizon_hold` reads the holders of the database it is
 *  connected to, so no other run's transaction is among them. */
const database = `node_projects_hold_${randomUUID().replaceAll("-", "_")}`;
const databaseUrl = (): string => {
  const url = new URL(server);
  url.pathname = `/${database}`;
  return url.href;
};

/** The bound `own_horizon_hold` holds a transaction to, `horizontest.OwnHoldBound`, as SQL writes
 *  an interval. */
const bound = "5 seconds";

let restoreNames: (() => void) | undefined;

beforeAll(
  async () => {
    if (!live) return;
    await sqlAt(server, [`create database ${database}`]);
    // `sql` resolves its database from the environment when it starts its psql, as
    // psql-session.test.ts says, so the file's database replaces the caller's here.
    restoreNames = captureEnvironment([
      "DATABASE_URL",
      "PLAYWRIGHT_BASE_URL",
      "PLAYWRIGHT_DATABASE_URL",
    ]);
    process.env.DATABASE_URL = databaseUrl();
    delete process.env.PLAYWRIGHT_BASE_URL;
    delete process.env.PLAYWRIGHT_DATABASE_URL;
  },
  // `sqlAt`'s psql is bounded nowhere (bounded-subprocess.ts says why); the minute is what stops
  // a create that hangs.
  60_000
);

afterAll(
  async () => {
    restoreNames?.();
    // The session the rows held their transactions on is connected to the database dropped
    // below, so it ends here, before the next `sql` call of this process reaches it.
    resetSession();
    if (!live) return;
    await dropDatabase(server, database);
  },
  // `dropDatabase`'s psql is `sqlAt`'s; the minute is what stops a drop that waits forever.
  60_000
);

const works = temporaryDirectories("node-projects-hold");

/** Runs one function of lib.sh, `call` with its arguments, in a bash that sourced lib.sh with the
 *  file's database as `database_url` and a directory of the row's own as `work`, and answers what
 *  the function set `$answer` to. A function that fails exits that bash 1 with its FAIL line on
 *  stderr, which fails the row. */
async function libCall(...call: string[]): Promise<string> {
  const { code, stdout, stderr, named } = await runBounded({
    argv: [
      "bash",
      "-c",
      'database_url=$1 work=$2 && shift 2 && . e2e/acceptance/lib.sh && "$@" && printf %s "$answer"',
      "lib.sh",
      databaseUrl(),
      works(),
      ...call,
    ],
    bound: "short",
    cwd: dispatchPackage,
    env: { PATH: process.env.PATH ?? "", HOME: process.env.HOME ?? "" },
    from: import.meta.url,
  });
  expect(code, `${named} exited ${code}: ${stderr}`).toBe(0);
  return stdout;
}

/** The observation `observe_horizon` takes, as the two arguments `own_horizon_hold` reads. */
async function observe(): Promise<[string, string]> {
  const [observer, observedAt, ...rest] = (await libCall("observe_horizon", "hold")).split(" ");
  expect(rest, "observe_horizon answers the observer's id and its time alone").toEqual([]);
  return [observer, observedAt];
}

/** Waits until the database's clock has passed `since`, a timestamptz expression, by the bound,
 *  on a connection of its own. A `since` that reads null waits not at all, and the row's next
 *  check then fails on the state it reads. */
async function untilPastTheBound(since: string): Promise<void> {
  await sqlAt(databaseUrl(), [`select pg_sleep_until((${since}) + interval '${bound}')`]);
}

/** Whether backend `pid` holds a transaction id in a transaction open past the bound, read on a
 *  connection of its own: the condition the driver's check named a holder on before it read any
 *  observation, so a row that expects no holder shows its holder is one that condition saw. */
async function heldPastTheBound(pid: string): Promise<string> {
  return sqlAt(databaseUrl(), [
    `select backend_xid is not null and xact_start < now() - interval '${bound}' ` +
      `from pg_stat_activity where pid = ${pid}`,
  ]);
}

// The observation has waited the bound too when the row reads it, so only the order of the two
// transaction ids can keep the holder unnamed.
test.skipIf(!live)(
  "a transaction open past the bound whose first id comes after the observation holds it back not at all",
  async () => {
    const pid = await sql("SELECT pg_backend_pid()");
    await sql("BEGIN", "SELECT 1");
    try {
      await untilPastTheBound(`(select xact_start from pg_stat_activity where pid = ${pid})`);
      const observation = await observe();
      await sql("SELECT pg_current_xact_id()");
      await untilPastTheBound(`to_timestamp(${observation[1]})`);
      expect(await heldPastTheBound(pid)).toBe("t");
      expect(
        await libCall("own_horizon_hold", "hold", ...observation),
        `backend ${pid} drew its first transaction id after the observation, so no proof of it waits on that id`
      ).toBe("");
    } finally {
      await sql("ROLLBACK");
    }
  },
  rowLimit("short", "short")
);

/** How many observations the row below takes, at most, before it gives up on a machine too loaded
 *  to read one inside the bound. */
const youngTries = 3;

// `own_horizon_hold` reads the observation's age on the database's clock as its statement starts,
// and a loaded machine can put more than the bound between the observation and that statement:
// the whole e2e lane, run in parallel, once did. The age read after the check is no less than the
// one the check read, so only a check followed by an age under the bound read a young
// observation, and only such a check decides the row; after any other, the row observes again.
test.skipIf(!live)(
  "a transaction that drew its id before the observation is no holder until the observation has waited the bound",
  async () => {
    const pid = await sql("SELECT pg_backend_pid()");
    await sql("BEGIN", "SELECT pg_current_xact_id()");
    try {
      await untilPastTheBound(`(select xact_start from pg_stat_activity where pid = ${pid})`);
      expect(await heldPastTheBound(pid)).toBe("t");
      for (let tried = 1; ; tried++) {
        const observation = await observe();
        const holder = await libCall("own_horizon_hold", "hold", ...observation);
        const young = await sqlAt(databaseUrl(), [
          `select now() - to_timestamp(${observation[1]}) < interval '${bound}'`,
        ]);
        if (young === "t") {
          expect(
            holder,
            `backend ${pid} has held the observation back for less than the bound, however long it has been open`
          ).toBe("");
          break;
        }
        if (tried === youngTries) {
          throw new Error(
            `each of ${youngTries} observations was ${bound} old or more once its check had run, so no check read a young one`
          );
        }
      }
    } finally {
      await sql("ROLLBACK");
    }
  },
  rowLimit("short", "short", "short", "short", "short", "short")
);

test.skipIf(!live)(
  "a transaction that drew its id before the observation and has held it back the bound is named",
  async () => {
    const pid = await sql("SELECT pg_backend_pid()");
    await sql("BEGIN", "SELECT pg_current_xact_id()");
    try {
      const observation = await observe();
      await untilPastTheBound(`to_timestamp(${observation[1]})`);
      expect(await libCall("own_horizon_hold", "hold", ...observation)).toStartWith(
        `backend ${pid} (application `
      );
    } finally {
      await sql("ROLLBACK");
    }
  },
  rowLimit("short", "short")
);
