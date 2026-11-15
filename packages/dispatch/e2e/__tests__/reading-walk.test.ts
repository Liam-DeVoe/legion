import { expect, test } from "bun:test";

import { DISPATCH_READING_SURFACES } from "@legion/contracts";

import { type ListedRow, rowsOfListing } from "../configurations";
import { listing, projectArguments, retained } from "./playwright-listing";

/**
 * The gate of the reading list's walk (INFRA-1702's R9, `e2e/reading-rows.ts`): every rendered
 * row of `DISPATCH_READING_SURFACES` has exactly one driver among the `reading-*.e2e.ts` specs,
 * a row titled with the surface's id followed by a colon, Playwright runs every driver rather
 * than skipping it, and no driver names a row the list does not hold or does not render. A row
 * the list gains with no driver fails here, by name, before any browser runs.
 *
 * The drivers are the rows Playwright itself lists, not titles read out of the specs' text: a
 * title in a comment declares no row, and a row marked `test.fixme` or `test.skip` declares one
 * Playwright never runs, so neither can stand in for a driver here.
 */

/** The project the references lane runs the reading specs in (`.github/workflows/
 *  dispatch-browser.yaml`, `lane: references`), and the file filter that names those specs and
 *  no other: Playwright matches it against each spec's path, where `inbox-reading-pane.e2e.ts`
 *  holds `reading-` but not `e2e/reading-`. */
const READING_LISTING_ARGUMENTS = [...projectArguments(["chromium"]), "e2e/reading-"];

/** The ceiling of each test that awaits the listing: where something is wrong rather than slow,
 *  as e2e/__tests__/no-browser-rows.test.ts sets its own. A listing that names one project and
 *  one group of files takes a few seconds on this pod. */
const listingTimeout = 180_000;

/** A driver's surface: the id its title starts with, before the colon. A row of a reading spec
 *  whose title starts with no id is no driver of any row, so it is answered by its whole title,
 *  which names no surface of the list and is reported as one. */
const SURFACE = /^([a-z0-9-]+):/u;

/** One row Playwright lists in the reading specs: the surface it drives, where it is declared,
 *  and whether Playwright runs it. */
interface Driver {
  readonly surface: string;
  readonly file: string;
  readonly title: string;
  /** What Playwright expects of the row: `passed` for a row it runs, `skipped` for one marked
   *  `fixme` or `skip`, `failed` for one marked `fail`. */
  readonly expectedStatus: string;
}

/** Every row of the reading specs, read from the listing. A spec's rows are one level under its
 *  file, so a row's title is its own; a row inside a `describe` block reads as the block's title
 *  first and is answered by its own title, the last part. */
function driversOf(rows: readonly ListedRow[]): Driver[] {
  return rows
    .filter((row) => /^reading-[^/]*\.e2e\.ts$/u.test(row.file))
    .map((row) => {
      const title = row.title.split(" › ").at(-1) ?? row.title;
      return {
        expectedStatus: row.expectedStatus,
        file: row.file,
        surface: SURFACE.exec(title)?.[1] ?? title,
        title,
      };
    });
}

// The listing is a read-only subprocess that starts no server and no browser, so it starts here
// rather than in the first test that awaits it, and its rejection waits for that test rather
// than being reported as unhandled before it runs (`retained`).
const drivers = retained(
  listing("e2e/playwright.config.ts", {}, READING_LISTING_ARGUMENTS).then((report) =>
    driversOf(rowsOfListing(report))
  )
);

// A listing that read no reading spec would leave every rule below over nothing: the rendered
// rows would each report no driver, but the rules that read the drivers would pass.
test(
  "the listing reads the reading specs",
  async () => {
    expect((await drivers).length).toBeGreaterThan(0);
  },
  listingTimeout
);

test.each(
  DISPATCH_READING_SURFACES.filter((row) => row.rendered).map((row) => [row.surface] as const)
)(
  "the rendered surface %s has one driver",
  async (surface) => {
    const files = (await drivers)
      .filter((driver) => driver.surface === surface)
      .map((driver) => driver.file);
    expect(
      files.length === 1
        ? surface
        : `${surface} has ${files.length} drivers in the reading specs: ${files.join(", ") || "none"}`
    ).toBe(surface);
  },
  listingTimeout
);

test(
  "every driver names a rendered row of the reading list",
  async () => {
    const rendered = new Set(
      DISPATCH_READING_SURFACES.filter((row) => row.rendered).map((row) => row.surface as string)
    );
    const stray = (await drivers)
      .filter((driver) => !rendered.has(driver.surface))
      .map((driver) => `${driver.file}: ${driver.title}`);
    expect(stray).toEqual([]);
  },
  listingTimeout
);

test(
  "Playwright runs every driver",
  async () => {
    // A driver marked `fixme` or `skip` is listed and never run, and one marked `fail` passes
    // only while its surface is wrong: none of them reads its surface.
    const unrun = (await drivers)
      .filter((driver) => driver.expectedStatus !== "passed")
      .map((driver) => `${driver.file}: ${driver.title} (expected ${driver.expectedStatus})`);
    expect(unrun).toEqual([]);
  },
  listingTimeout
);

test("every row the list does not render says why", () => {
  const silent = DISPATCH_READING_SURFACES.filter(
    (row) => !row.rendered && (row.reason ?? "").trim() === ""
  ).map((row) => row.surface);
  expect(silent).toEqual([]);
});
