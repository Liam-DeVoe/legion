/** The `test` every row of the real-stack browser suite takes: Playwright's own, with the two
 *  things every one of those rows needs put on it once rather than written out in every file.
 *
 *  **The reset runs as an automatic fixture, not as a hook each file copies.** A row of this
 *  suite seeds what it reads and must read nothing the row before it left, so it starts from an
 *  emptied database (`resetDatabase`, e2e/seed.ts). That used to be four lines at the top of
 *  every spec file -
 *
 *      test.beforeEach(async () => {
 *        await resetDatabase();
 *      });
 *
 *  - one copy of one hook in each spec file: as many places to read it, and one chance to
 *  forget it in every spec written since. As an automatic fixture it runs for every row of
 *  every file that imports this
 *  `test`, so a new spec resets by importing from here and a row cannot be written without one.
 *  A file whose setup does more than the reset still writes a hook, for that extra work alone.
 *
 *  **The sign-in is the default context's, not the row's.** Most rows of this suite open contexts
 *  of their own - `asUser(browser, "alice")`, `withAlice(browser, ...)` (e2e/users.ts) - and those
 *  are untouched: they take `{ browser }`, never instantiate the `context` fixture, and so never
 *  reach the override below or pay for it. What the override replaces is the other shape, the
 *  rows that take Playwright's own `page` or `context`, whose files each wrote the reset and the
 *  sign-in into one hook:
 *
 *      test.beforeEach(async ({ context }) => {
 *        await resetDatabase();
 *        await signIn(context, "alice");
 *      });
 *
 *  The order inside that hook is load-bearing, which is why the override names `freshDatabase`
 *  among its own fixtures: the sign-in carries the cookie e2e/api.ts holds for alice, and that
 *  cookie authenticates only because the reset put her `user_sessions` row back at the generation
 *  it names (the head comment of e2e/users.ts). Naming the reset fixture is what makes Playwright
 *  set it up first, so the order is kept here once instead of in each file that happened to
 *  write it.
 *
 *  **How a row opts out.** The `signedIn` option is the login the default context is signed in
 *  as, `"alice"` unless a file says otherwise, and `false` leaves that context signed out;
 *  `own(login)` signs it in as the row's own person for that login (`rowPerson`, below), a
 *  person no other row signs in as and one the row's end deletes again (`ownPeople`):
 *
 *      test.use({ signedIn: false });          // the reader nobody signed in
 *      test.use({ signedIn: "bob" });          // the default context is bob's
 *      test.use({ signedIn: own("alice") });   // the default context is rowPerson("alice")'s
 *
 *  It is read where Playwright reads any option - at file scope, or inside a `test.describe` -
 *  and it governs the default context alone. A row that opens its own context signs that one in
 *  itself, as it always has: `asUser(browser, rowPerson("alice"))` for a person of its own.
 *
 *  **What a worker keeps.** Two datasets are the worker's, not a row's: the delivery settings the
 *  delivery rows read (`deliverySettings`, handed to a row as its `delivery` place) and the review
 *  corpus the accessibility rows scan (`reviewCorpus`, handed to a row as its `reviewSeed`). Each
 *  is written once for each database the worker empties - before every row while the per-row
 *  reset runs, so once a row there, and once a worker without it - and a row that changes one
 *  puts it back when it ends.
 *
 *  The stubbed layer takes no part in any of this: it reaches no server, no database and no
 *  sign-in, and declares a `test` of its own (e2e/stub/stub-test.ts). So does the containerised
 *  visual review (e2e/visual.e2e.ts), whose rows seed outside Playwright.
 */

import { createHash, randomBytes } from "node:crypto";

import { test as base } from "@playwright/test";

import { setLiveSessions } from "./agents";
import {
  type DeliveryPlace,
  deleteDeliveryRowsBut,
  deliveryPlace,
  deliveryRowsHeld,
  putDeliverySettings,
  ROW_DAYS_EARLIER,
  readDeliverySettings,
  writeDeliverySettings,
} from "./delivery-seed";
import { expectTimeout } from "./expect-timeout";
import { sql } from "./psql";
import type { ReviewSeed } from "./review-routes";
import { seedReviewRoutes } from "./review-seed";
import { broadcastIds, deleteBroadcastsBut, resetDatabase, sqlLiteral } from "./seed";
import { signIn } from "./users";
import { workspaceSessions } from "./workspace";

/** Bytes this process drew once, when it loaded this module, and folds into every `rowKey`: a
 *  second run of the suite against the same database, or a retry in a new worker, draws others,
 *  so it never meets a key the first one made. */
const processNonce = randomBytes(16).toString("hex");

/** How many keys the five-letter suffix of `rowKey` tells apart: 26 ** 5 (A-Z). */
const SUFFIXES = 26 ** 5;

/** A SHA-256 of the running row (`testId`, `repeatEachIndex`, `retry`), of this process's nonce
 *  and of `logical`: what `rowKey`, `rowPerson` and `rowSessionId` derive their names from.
 *  Outside a running row it throws, as `test.info()` does. */
function rowDigest(logical: string): Buffer {
  const row = base.info();
  return createHash("sha256")
    .update([row.testId, row.repeatEachIndex, row.retry, processNonce, logical].join("\n"))
    .digest();
}

/** The five letters A-Z, in base 26, of `rowDigest(logical)`: the suffix `rowKey` and
 *  `rowPerson` put after the name a spec writes. */
function rowSuffix(logical: string): string {
  let rest = rowDigest(logical).readUIntBE(0, 6) % SUFFIXES;
  let suffix = "";
  for (let place = 0; place < 5; place += 1) {
    suffix = String.fromCharCode(65 + (rest % 26)) + suffix;
    rest = Math.floor(rest / 26);
  }
  return suffix;
}

/** The project key the running row makes and names for the key `logical` its spec writes, so
 *  that no two rows share a project and a row reads none another row left.
 *
 *  It is the first five characters of `logical` (all of it when it is shorter) and five more,
 *  the letters A-Z in base 26, from a SHA-256 of the row (`testId`, `repeatEachIndex`, `retry`),
 *  of this process's nonce and of `logical` itself, so two logical keys of one row differ even
 *  when their first five characters agree. A key is at most ten characters, and starts with A-Z,
 *  as the server's ProjectKeyPattern requires (`^[A-Z][A-Z0-9]{1,9}$`,
 *  packages/envoy/internal/contracts/generated.go).
 *
 *  The suffix is letters, never digits, so a key holds a digit only where its logical key does:
 *  a count drawn beside a key (`toContainText("2")`), or a digit a row says is not there
 *  (`not.toContainText("1")`), never meets a digit the key brought, and reads the same text it
 *  read beside "CORE".
 *
 *  Within a row every call with the same `logical` answers the same key, so a row, its hooks and
 *  the helpers it calls agree without passing the key around. Outside a running row it throws,
 *  as `test.info()` does: a key held at module scope belongs to no row.
 *
 *      const core = rowKey("CORE");
 *      await createProject({ key: core, name: "Core" });
 *      await page.goto(`/projects/${core}`); */
export function rowKey(logical: string): string {
  if (!/^[A-Z][A-Z0-9]*$/.test(logical)) {
    throw new Error(
      `rowKey: the logical key ${JSON.stringify(logical)} must start with A-Z and hold only A-Z and 0-9`
    );
  }
  return `${logical.slice(0, 5)}${rowSuffix(logical)}`;
}

/** Whether `logical` is a name `rowPerson`, `own`, `rowSessionId` and `rowNumber` take: a-z,
 *  then a-z and 0-9. */
function checkLogicalLogin(caller: string, logical: string): void {
  if (!/^[a-z][a-z0-9]*$/.test(logical)) {
    throw new Error(
      `${caller}: the logical name ${JSON.stringify(logical)} must start with a-z and hold only a-z and 0-9`
    );
  }
}

/** The Envoy session id the running row seeds and names for the session `logical` its spec
 *  writes (`planner`, `reviewer`), so that no two rows share a session and a row reads no message,
 *  ask, read mark or pin another row left on it: Dispatch keeps what a session said and what was
 *  said to it by the session's id, for every person at once (`listAgentMessages`).
 *
 *  It is a UUID, since Dispatch's agent routes refuse any other id (`agentStreamSessionID`): the
 *  first sixteen bytes of `rowKey`'s SHA-256, taken over `session:` and `logical`, with RFC
 *  9562's version 8 and its variant written into them. Within a row every call with the same
 *  `logical` answers the same id; outside a running row it throws, as `rowKey` does.
 *
 *      const planner = { session_id: rowSessionId("planner"), title: "Planner" };
 *      await setLiveSessions([planner]); */
export function rowSessionId(logical: string): string {
  checkLogicalLogin("rowSessionId", logical);
  const bytes = rowDigest(`session:${logical}`).subarray(0, 16);
  bytes.writeUInt8((bytes.readUInt8(6) & 0x0f) | 0x80, 6);
  bytes.writeUInt8((bytes.readUInt8(8) & 0x3f) | 0x80, 8);
  const hex = bytes.toString("hex");
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
}

/** The first number `rowNumber` answers. Every number the suite writes as a fixed one - the
 *  review corpus's pull requests and runs (e2e/delivery-seed.ts), a pull request a spec names -
 *  is below it, so a row's number is never one of them. */
const FIRST_ROW_NUMBER = 10_000;

/** A number of the running row's own for the number `logical` its spec writes - a pull
 *  request's, a run's - so that no two rows share one a table holds unique (a pull request's head
 *  in `pull_request_heads`, by repository and number; the address of an external link, which
 *  names one) and a row reads none another row left.
 *
 *  It is `rowKey`'s suffix read as a number rather than spelled in letters, from the same SHA-256
 *  of the row, of this process's nonce and of `number:` and `logical`, plus `FIRST_ROW_NUMBER`: a
 *  whole number from 10,000 up and below 10,000 + 26 ** 5, so it fits a Postgres `integer`, which
 *  a pull request's number is. Within a row every call with the same `logical` answers the same
 *  number; outside a running row it throws, as `rowKey` does.
 *
 *      const pull = rowNumber("pull");
 *      const url = `https://github.com/acme/widgets/pull/${pull}`; */
export function rowNumber(logical: string): number {
  checkLogicalLogin("rowNumber", logical);
  return FIRST_ROW_NUMBER + (rowDigest(`number:${logical}`).readUIntBE(0, 6) % SUFFIXES);
}

/** Every login `rowPerson` has named in the running row, which `ownPeople` deletes when the row
 *  ends. A worker runs one row at a time, so one set serves the worker. */
const namedPeople = new Set<string>();

/** The person the running row signs in as and names for the login `logical` its spec writes, so
 *  that a row reading one person's list - an Inbox, its counts, read and unread state, the pins
 *  a person keeps - reads a list no other row added to.
 *
 *  It is `logical` whole and five more letters, a-z: `rowKey`'s suffix, from the same SHA-256 of
 *  the row, of this process's nonce and of `logical`, lower-cased, since the server records a
 *  login trimmed and lower-cased (`authDevSignIn`, packages/envoy/internal/dispatch/routes/
 *  devsignin.go): `alice` -> `alicekqzvm`. Two logical logins of one row differ, and so does one
 *  logical login in two rows. Within a row every call with the same `logical` answers the same
 *  login; outside a running row it throws, as `rowKey` does.
 *
 *  The person needs no seeding: a dev sign-in records any login it signs in (`People.Record`
 *  in the same route), which is what makes the login one a project, an issue or an ask can name.
 *  So the first thing a row does as this person is sign it in - the default context under
 *  `own()`, or an api.ts call with `{ login: rowPerson("alice") }` - before a seed names it. It
 *  pays a dev sign-in where alice and bob carry a kept cookie (`reusableSessionCookie`,
 *  e2e/api.ts), since the reset re-creates neither the person nor a session for it.
 *
 *  The person is the row's to the end: when the row ends, pass or fail, `ownPeople` deletes the
 *  `people` and `user_sessions` rows the sign-in made for every login this named in the row.
 *  Everyone who has signed in is one list (`GET /api/v1/users`, the options of every assignee
 *  control), and a row that reads that list whole - Settings' default assignee offering exactly
 *  alice and bob - reads it back at its start only if each row takes its own people out again.
 *
 *      const me = rowPerson("alice");
 *      await createProject({ key: rowKey("CORE"), name: "Core" }, { login: me });
 *      const context = await asUser(browser, me); */
export function rowPerson(logical: string): string {
  checkLogicalLogin("rowPerson", logical);
  const login = `${logical}${rowSuffix(logical).toLowerCase()}`;
  namedPeople.add(login);
  return login;
}

/** A `signedIn` value that signs the default context in as the row's own person for `login`. */
export interface OwnPerson {
  readonly own: string;
}

/** The `signedIn` value for a file or a `test.describe` whose default context is the row's own
 *  person for `login`, `rowPerson(login)`: `test.use({ signedIn: own("alice") })`. The person is
 *  named when the row runs, since `test.use` is read before any row does. */
export function own(login: string): OwnPerson {
  checkLogicalLogin("own", login);
  return { own: login };
}

/** How many times this worker has emptied its database (`freshDatabase`): the database a dataset
 *  the worker keeps was written into, so that it is written again once that database is gone,
 *  and never twice into one. */
let emptied = 0;

/** What a spec may set with `test.use`. */
type DispatchOptions = {
  /** The login Playwright's own `context` is signed in as, `own(login)` for the row's own
   *  person for that login, or `false` for a context nobody signed in. */
  signedIn: string | OwnPerson | false;
};

/** What this `test` adds beyond the options. */
type DispatchFixtures = {
  /** The emptied database every row starts from. It is automatic, so no row asks for it, and it
   *  carries no value: what a row takes from it is that it ran. */
  freshDatabase: undefined;
  /** The row's own people taken out again once it ends (`rowPerson`). It is automatic and
   *  carries no value: its work is its teardown. */
  ownPeople: undefined;
  /** The broadcasts the row sent taken out again once it ends. It is automatic and carries no
   *  value: its work is its teardown. */
  ownBroadcasts: undefined;
  /** The row's own place in the delivery tables (`DeliveryPlace`, e2e/delivery-seed.ts), on the
   *  delivery settings the worker wrote: what a delivery row hands its fixtures, and reads its
   *  numbers, its day and its window from. What the row wrote there is taken out again once it
   *  ends, and the settings are put back. */
  delivery: DeliveryPlace;
  /** The review corpus (e2e/review-seed.ts), as the worker seeded it, for a row that reads the
   *  review layers' pages; the delivery settings are put back once the row ends. */
  reviewSeed: ReviewSeed;
};

/** What this `test` keeps for a whole worker. */
type DispatchWorkerFixtures = {
  /** Writes the delivery settings the delivery rows read (`writeDeliverySettings`), unless the
   *  worker has written them into the database it holds now. */
  deliverySettings: () => Promise<void>;
  /** The review corpus (`seedReviewRoutes`), seeded unless the worker has seeded it into the
   *  database it holds now, with what a row before this one changed of it put back. */
  reviewCorpus: () => Promise<ReviewSeed>;
};

export const test = base.extend<DispatchOptions & DispatchFixtures, DispatchWorkerFixtures>({
  signedIn: ["alice", { option: true }],
  freshDatabase: [
    // Playwright refuses a first argument that is not a destructuring pattern.
    // biome-ignore lint/correctness/noEmptyPattern: the reset takes no other fixture.
    async ({}, use) => {
      await resetDatabase();
      emptied += 1;
      await use(undefined);
    },
    { auto: true },
  ],
  // The teardown is the `finally` of every row that named a person of its own: the `people` row
  // a dev sign-in records and the `user_sessions` row it starts, of every login `rowPerson`
  // answered in the row, one statement whatever the row did with them. Rows of one worker run
  // one at a time on that worker's own stack, so the next row starts on the people list this
  // row started on. No foreign key reaches either table, and nothing records the person again:
  // only a sign-in does, and the row's own contexts are closed by now (the default one by the
  // `context` fixture, which names this one so that it is torn down first).
  ownPeople: [
    // biome-ignore lint/correctness/noEmptyPattern: the cleanup takes no other fixture.
    async ({}, use) => {
      await use(undefined);
      if (namedPeople.size === 0) return;
      const logins = [...namedPeople].map(sqlLiteral).join(", ");
      namedPeople.clear();
      await sql(
        `WITH sessions AS (DELETE FROM user_sessions WHERE login IN (${logins})) DELETE FROM people WHERE email IN (${logins})`
      );
    },
    { auto: true },
  ],
  // The teardown is the `finally` of every row that sends a broadcast, through the page or
  // through e2e/api.ts alike: every broadcast the stack holds at the row's end that it did not
  // hold at its start, with the copies, replies, attempts and events each wrote
  // (`deleteBroadcastsBut`). The list of every broadcast (`GET /api/v1/broadcasts`, the Agents
  // page's Broadcasts region) is the stack's, not a person's, and a row that reads it whole -
  // that it holds exactly the broadcast the row sent - reads it back at its start only if each
  // row takes its own out again. Rows of one worker run one at a time on that worker's own
  // stack, so what the end holds beyond the start is this row's. That includes the review
  // corpus's broadcast when this row was the first to read the corpus: the list stays whole for
  // the rows after it, and no page the accessibility rows scan draws it (`reviewCorpus`). It is
  // read after the reset and torn down after the default context is closed, which names it as
  // it names `ownPeople`.
  ownBroadcasts: [
    async ({ freshDatabase: _resetFirst }, use) => {
      const kept = await broadcastIds();
      await use(undefined);
      await deleteBroadcastsBut(kept);
    },
    { auto: true },
  ],
  // The delivery settings are one row for the whole deployment (`delivery_settings.singleton`),
  // which every delivery row reads and none owns, so the worker writes them rather than each row:
  // once into each database it empties - before every row while the per-row reset runs, and
  // once for the worker without it - with an upsert, which writes them whether or not a settings
  // row stands and empties nothing. A row that changes them - the unconfigured page's row, which
  // starts with none, a row that sets a staging job or a reconcile error - gets them back from
  // the `delivery` fixture when it ends, as the fixture left them. No lock orders those rows:
  // rows of one worker run one at a time on that worker's own stack, so the teardown is what
  // keeps the next row's start, and Playwright has no lock that orders rows of one worker; rows
  // of two workers never share a stack, so there is nothing for a lock between workers to guard.
  deliverySettings: [
    // biome-ignore lint/correctness/noEmptyPattern: the settings take no other fixture.
    async ({}, use) => {
      let writtenInto: number | undefined;
      await use(async () => {
        if (writtenInto === emptied) return;
        await writeDeliverySettings();
        writtenInto = emptied;
      });
    },
    { scope: "worker" },
  ],
  // A delivery row's own place: the row's project (`rowKey("ACME")`), numbers of its own for
  // its pull requests and runs, a multiple of 10,000 from `rowNumber` between 10,000 and
  // 1,999,990,000, which keeps every sum inside a Postgres `integer`, which a pull request's
  // number is, and the timeline and the Pipeline written `ROW_DAYS_EARLIER` days before the
  // review corpus's, which the worker keeps beside them (`reviewCorpus`). The teardown is the
  // `finally` of every delivery row: the timeline, the measures and the Pipeline read every pull
  // request, run and session group of their window whole ("2 PRs in current filter/window", the
  // four measures, "p90 30m · 2 runs"), so each row deletes what it wrote there
  // (`deleteDeliveryRowsBut`) and puts the settings back, and the next row of the worker starts
  // on the tables and the settings this one started on.
  delivery: async ({ deliverySettings, freshDatabase: _resetFirst }, use) => {
    await deliverySettings();
    const held = await deliveryRowsHeld();
    const place = deliveryPlace(
      rowKey("ACME"),
      10_000 * (1 + (rowNumber("delivery") % 199_999)),
      ROW_DAYS_EARLIER
    );
    await use(place);
    await deleteDeliveryRowsBut(held, place.project);
    await writeDeliverySettings();
  },
  // The review corpus is a dataset every accessibility row reads alike, so the worker seeds it
  // once into each database it empties, as the delivery settings are, and hands each row the
  // seed it returned. Its keys are the corpus's own (`CORE`, `ACME`), which the stubbed layer's
  // recordings were made under and no other row writes, since every other row writes keys of
  // its own. Two things of it a row before can change without touching those keys, and both are
  // put back before a row reads it: the delivery settings, which a delivery row puts back to
  // `writeDeliverySettings`'s and the corpus seeds with its Pipeline job names and its reconcile
  // error, and the fake Envoy's sessions, which a row may set and a reset of the fakes empties.
  // Its broadcast goes at the end of the row that seeded it (`ownBroadcasts`): every broadcast
  // is one list a row reads whole, and no page these rows scan draws it. The corpus is seeded
  // into a database that holds none of it, which is every database a worker has just emptied.
  reviewCorpus: [
    // biome-ignore lint/correctness/noEmptyPattern: the corpus takes no other fixture.
    async ({}, use) => {
      let seeded: { into: number; seed: ReviewSeed; settings: string } | undefined;
      await use(async () => {
        if (seeded?.into === emptied) {
          await putDeliverySettings(seeded.settings);
          await setLiveSessions(workspaceSessions);
          return seeded.seed;
        }
        // The seed's waits give up at the bound a row's own assertions use, inside its clock.
        const seed = await seedReviewRoutes({ boundMs: expectTimeout });
        seeded = { into: emptied, seed, settings: await readDeliverySettings() };
        return seed;
      });
    },
    { scope: "worker" },
  ],
  // The teardown puts the delivery settings back to the ones a delivery row starts on: the
  // corpus's Pipeline job names and reconcile error are its own, and no other row reads them.
  reviewSeed: async ({ freshDatabase: _resetFirst, reviewCorpus }, use) => {
    await use(await reviewCorpus());
    await writeDeliverySettings();
  },
  // `freshDatabase` is in this list for its order rather than for its value, hence the unused
  // binding: naming it is what makes Playwright empty the database before this fixture signs
  // anyone in, which is the order the hooks this replaces were written in. `ownPeople` and
  // `ownBroadcasts` are here for their order too: set up before this context, each is torn down
  // after it is closed.
  context: async (
    {
      context,
      signedIn,
      freshDatabase: _resetFirst,
      ownPeople: _deletedAfter,
      ownBroadcasts: _broadcastsDeletedAfter,
    },
    use
  ) => {
    if (signedIn !== false) {
      await signIn(context, typeof signedIn === "string" ? signedIn : rowPerson(signedIn.own));
    }
    await use(context);
  },
});

/** What a spec takes from Playwright beside this `test`, re-exported so a converted file has one
 *  import line rather than two: `devices` and `expect`, and the types the suite's specs name. A
 *  type Playwright exports and no spec names yet is not here; add it when a spec needs it. */
export type {
  APIResponse,
  Browser,
  BrowserContext,
  Locator,
  Page,
  Request,
  Response,
  Route,
  TestInfo,
  WebSocket,
  WebSocketRoute,
} from "@playwright/test";
export { devices, expect } from "@playwright/test";
