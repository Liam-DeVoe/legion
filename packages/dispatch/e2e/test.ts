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
 *  as, `"alice"` unless a file says otherwise, and `false` leaves that context signed out:
 *
 *      test.use({ signedIn: false });   // the reader nobody signed in
 *      test.use({ signedIn: "bob" });   // the default context is bob's
 *
 *  It is read where Playwright reads any option - at file scope, or inside a `test.describe` -
 *  and it governs the default context alone. A row that opens its own context signs that one in
 *  itself, as it always has.
 *
 *  The stubbed layer takes no part in any of this: it reaches no server, no database and no
 *  sign-in, and declares a `test` of its own (e2e/stub/stub-test.ts). So does the containerised
 *  visual review (e2e/visual.e2e.ts), whose rows seed outside Playwright.
 */

import { createHash, randomBytes } from "node:crypto";

import { test as base } from "@playwright/test";

import { resetDatabase } from "./seed";
import { signIn } from "./users";

/** Bytes this process drew once, when it loaded this module, and folds into every `rowKey`: a
 *  second run of the suite against the same database, or a retry in a new worker, draws others,
 *  so it never meets a key the first one made. */
const processNonce = randomBytes(16).toString("hex");

/** How many keys the five-letter suffix of `rowKey` tells apart: 26 ** 5 (A-Z). */
const SUFFIXES = 26 ** 5;

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
  const row = base.info();
  const digest = createHash("sha256")
    .update([row.testId, row.repeatEachIndex, row.retry, processNonce, logical].join("\n"))
    .digest();
  let rest = digest.readUIntBE(0, 6) % SUFFIXES;
  let suffix = "";
  for (let place = 0; place < 5; place += 1) {
    suffix = String.fromCharCode(65 + (rest % 26)) + suffix;
    rest = Math.floor(rest / 26);
  }
  return `${logical.slice(0, 5)}${suffix}`;
}

/** What a spec may set with `test.use`. */
type DispatchOptions = {
  /** The login Playwright's own `context` is signed in as, or `false` for a context nobody
   *  signed in. */
  signedIn: string | false;
};

/** What this `test` adds beyond the options. */
type DispatchFixtures = {
  /** The emptied database every row starts from. It is automatic, so no row asks for it, and it
   *  carries no value: what a row takes from it is that it ran. */
  freshDatabase: undefined;
};

export const test = base.extend<DispatchOptions & DispatchFixtures>({
  signedIn: ["alice", { option: true }],
  freshDatabase: [
    // Playwright refuses a first argument that is not a destructuring pattern.
    // biome-ignore lint/correctness/noEmptyPattern: the reset takes no other fixture.
    async ({}, use) => {
      await resetDatabase();
      await use(undefined);
    },
    { auto: true },
  ],
  // `freshDatabase` is in this list for its order rather than for its value, hence the unused
  // binding: naming it is what makes Playwright empty the database before this fixture signs
  // anyone in, which is the order the hooks this replaces were written in.
  context: async ({ context, signedIn, freshDatabase: _resetFirst }, use) => {
    if (signedIn !== false) {
      await signIn(context, signedIn);
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
