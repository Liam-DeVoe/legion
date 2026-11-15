import { expect, type Locator } from "@playwright/test";

import type { Issue } from "../web/src/api/types";
import { type FakeSession, setLiveSessions } from "./agents";
import { createIssue, createProject } from "./api";
import { sessionIds } from "./session-ids";

/**
 * The reading list's walk (INFRA-1702's R9): each rendered row of `DISPATCH_READING_SURFACES`
 * has a driver, a row of a `reading-*.e2e.ts` spec titled with the row's own id followed by a
 * colon, that reaches the surface the way the row says and reads there a stored text holding a
 * bare issue key, a `dispatch://` address and a picked party (`readingText`). Each of the three
 * must draw through the one renderer: the key and the address as their issue's current name,
 * the party as `@` and its current name, and no address left as raw text (R6, R8).
 *
 * `e2e/__tests__/reading-walk.test.ts` is the gate: it reads Playwright's own listing of the
 * reading specs and fails on a rendered row with no driver, two drivers for one row, a driver
 * for a row the list does not hold or does not render, and a driver Playwright would skip. A row
 * the list excludes carries its reason, which the dashboard's own source test holds
 * (`features/refs/surface-lists.test.ts`).
 *
 * Each world spec seeds this world in its own rows (`seedReadingWorld`), writes `readingText`
 * into the record its surfaces show, and reads it with `expectReadsReferences`.
 */

/** The live session the text names as a party, with the title its name reads as. */
export const READING_SESSION_ID = sessionIds.tester;
export const READING_SESSION_TITLE = "Reading tester";
export const READING_SESSION_REFERENCE = `dispatch://agent/${READING_SESSION_ID}`;

/** The words the text writes for the party: the name the picker wrote when the party was picked,
 *  which the session no longer holds. A surface that drew a mention as its author's words would
 *  show these, so a surface that shows `READING_SESSION_TITLE` instead has read the party's
 *  current name (R8). */
const READING_SESSION_PICKED_NAME = "Old reading name";

const readingSession: FakeSession = {
  capabilities: ["aside", "btw", "steer"],
  dir: "/srv/reading",
  machine_id: "box-reading",
  roles: ["reading-reviewer"],
  session_id: READING_SESSION_ID,
  title: READING_SESSION_TITLE,
};

/** The issues the text names and the one that hosts what a world writes. */
export interface ReadingWorld {
  /** CORE-1, where a world writes the records it reads. */
  readonly host: Issue;
  /** CORE-2, named by its bare key. */
  readonly keyed: Issue;
  /** CORE-3, named by its `dispatch://` address. */
  readonly addressed: Issue;
}

/** The three keys the world makes, in the order it makes them; the seeding refuses any other. */
const KEYS = ["CORE-1", "CORE-2", "CORE-3"] as const;

/**
 * Seeds project CORE with the host and the two issues the text names, and lists the session the
 * text names as a party. A row resets the database before it runs (`e2e/test.ts`), so every
 * row that reads the text seeds it first.
 */
export async function seedReadingWorld(): Promise<ReadingWorld> {
  await createProject({ key: "CORE", name: "Core" });
  await setLiveSessions([readingSession]);
  const host = await createIssue({ project: "CORE", title: "Reading host" });
  const keyed = await createIssue({ project: "CORE", title: "Warm the cache" });
  const addressed = await createIssue({ project: "CORE", title: "Drain the queue" });
  const made = [host.key, keyed.key, addressed.key];
  if (made.join() !== KEYS.join()) {
    throw new Error(`setup: the reading world made ${made.join(", ")}, not ${KEYS.join(", ")}`);
  }
  return { addressed, host, keyed };
}

/** The text every driver stores: a bare key, an address and a party picked as the picker writes
 *  one, `[@<name>](<address>)`, under the name it held when it was picked
 *  (`READING_SESSION_PICKED_NAME`). One line, so a surface that shows a line of a record shows it
 *  whole. */
export function readingText(world: ReadingWorld): string {
  return `Read ${world.keyed.key}, then dispatch://${world.addressed.key}, and ask [@${READING_SESSION_PICKED_NAME}](${READING_SESSION_REFERENCE}).`;
}

/**
 * How a surface's host shows a reference (R6): `link`, a record a reader reads, where each
 * reference is a link; `inert`, the inside of a link or a button, where each reference draws as
 * its name and links nothing; `written`, a diff, where the text is the characters as written.
 */
export type ReadingShape = "link" | "inert" | "written";

/**
 * Holds `scope` to what R6 and R8 say a surface of `shape` shows for `readingText(world)`.
 * `link` and `inert`: the key and the address read as their issue's key and current title, the
 * party as `@` and the session's current title rather than the name the text wrote for it, and
 * no `dispatch://` is left as text; `link` makes each of the three a link, `inert` none.
 * `written`: the text as stored.
 */
export async function expectReadsReferences(
  scope: Locator,
  world: ReadingWorld,
  shape: ReadingShape
): Promise<void> {
  if (shape === "written") {
    await expect(scope).toContainText(readingText(world));
    return;
  }
  const drawn = (reference: string) => scope.locator(`[data-dispatch-ref="${reference}"]`);
  const keyed = drawn(`dispatch://${world.keyed.key}`);
  const addressed = drawn(`dispatch://${world.addressed.key}`);
  const party = drawn(READING_SESSION_REFERENCE);
  await expect(keyed.first()).toHaveText(`${world.keyed.key} ${world.keyed.title}`);
  await expect(addressed.first()).toHaveText(`${world.addressed.key} ${world.addressed.title}`);
  await expect(party.first()).toHaveText(`@${READING_SESSION_TITLE}`);
  await expect(scope).not.toContainText(READING_SESSION_PICKED_NAME);
  await expect(scope).not.toContainText("dispatch://");
  const links = scope.locator("a[href][data-dispatch-ref]");
  if (shape === "link") {
    for (const reference of [keyed, addressed, party]) {
      await expect(reference.first()).toHaveAttribute("href", /.+/);
    }
  } else {
    await expect(links).toHaveCount(0);
  }
}
