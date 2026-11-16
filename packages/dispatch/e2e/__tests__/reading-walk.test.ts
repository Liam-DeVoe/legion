import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

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
 *
 * Two more rules hold what a driver reads and where it runs. A driver stores every form its
 * row's surface carries (`cannotCarry`), so a rendered row whose text is no record must say it
 * carries no question id, or its driver would hold it to a `#id` that names nothing there; and
 * a row that names a form it cannot carry must say why, or the list would let a surface off a
 * form with nothing a reader could check. And the references lane names its spec files one by
 * one, so a reading spec it does not name would pass every rule above and run on no pull
 * request.
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

test.each(
  DISPATCH_READING_SURFACES.filter((row) => row.rendered && !row.record).map(
    (row) => [row.surface, row] as const
  )
)("the rendered surface %s, whose text is no record, says it carries no question id", (surface, row) => {
  expect(
    row.cannotCarry?.forms.includes("question-id") === true
      ? surface
      : `${surface} draws no record and does not say it cannot carry a question id (cannotCarry)`
  ).toBe(surface);
});

test("every row that names a form it cannot carry says why", () => {
  const silent = DISPATCH_READING_SURFACES.filter(
    (row) =>
      row.cannotCarry !== undefined &&
      (row.cannotCarry.forms.length === 0 || row.cannotCarry.why.trim() === "")
  ).map((row) => row.surface);
  expect(silent).toEqual([]);
});

/** The workflow whose `references` lane runs the reading specs on a pull request. */
const BROWSER_WORKFLOW = fileURLToPath(
  new URL("../../../../.github/workflows/dispatch-browser.yaml", import.meta.url)
);

/**
 * The spec files the references lane runs: the entries of the `lane_files="..."` a step of the
 * workflow sets in the `references)` arm of its script, read as
 * `.github/scripts/ci_filter_coverage.py`'s `check_references_lane` reads them, with the
 * script's comments and blank lines aside, so a comment that spells an assignment is no entry.
 */
function referencesLaneFiles(): string[] {
  const workflow = Bun.YAML.parse(readFileSync(BROWSER_WORKFLOW, "utf8")) as {
    jobs?: Record<string, { steps?: { run?: string }[] } | null>;
  };
  return Object.values(workflow.jobs ?? {})
    .flatMap((job) => job?.steps ?? [])
    .flatMap((step) => {
      const script = (step.run ?? "")
        .split("\n")
        .map((line) => line.trim())
        .filter((line) => line !== "" && !line.startsWith("#"))
        .join("\n");
      const arm = /^references\)$(.*?)^;;$/msu.exec(script)?.[1] ?? "";
      return (/^lane_files="([^"]*)"/mu.exec(arm)?.[1] ?? "").split(/\s+/u);
    })
    .filter((entry) => entry !== "");
}

test(
  "the references lane runs every reading spec",
  async () => {
    const lane = new Set(referencesLaneFiles());
    const unrun = [...new Set((await drivers).map((driver) => `e2e/${driver.file}`))].filter(
      (file) => !lane.has(file)
    );
    expect(unrun).toEqual([]);
  },
  listingTimeout
);
