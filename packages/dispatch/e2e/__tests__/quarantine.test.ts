/** The quarantine's rules that hold on every pull request (e2e/quarantine.ts): at most ten rows,
 *  each with an issue that owns it and the day it came in, each a row the suite runs in a project
 *  that can report it flaky. What depends on the date - an entry held longer than 14 days, a row
 *  flaky twice that is not here - is the nightly's (`e2e/tools/quarantine-check.ts`), never this
 *  file's: a pull request is no place for a check that turns red on a day nobody chose.
 *
 *  The faults are read from the module's own list and from the planted ones of
 *  `fixtures/quarantine-entries.fixture.ts`, each refused for the one fault its name says. The
 *  rows an entry names are read from a listing of e2e/playwright.config.ts under
 *  DISPATCH_E2E_QUARANTINE=only, which is the nightly's quarantine leg: what it selects is
 *  exactly what the config's own pattern selects, so an entry whose row it does not hold names
 *  no row of the suite. `fixtures/quarantine.config.ts` lists planted rows the same way, under
 *  each of the three selections, which is what shows the pattern takes the rows its entries name
 *  and nothing that only looks like one.
 */
import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { rowsOfListing } from "../configurations";
import realConfig from "../playwright.config";
import {
  entryLines,
  fixes,
  fixFaults,
  quarantine,
  quarantineFaults,
  rowName,
  selectionFaults,
} from "../quarantine";
import { rowLimit } from "./bounded-subprocess";
import { plantedQuarantine } from "./fixtures/quarantine.config";
import {
  elevenRows,
  looseFix,
  malformed,
  withoutDate,
  withoutIssue,
} from "./fixtures/quarantine-entries.fixture";
import { listing, retained } from "./playwright-listing";

// Started when the file loads, so the four collect at once. The first lists the real suite under
// the nightly's quarantine leg, the other three the planted config under each selection.
const quarantinedRows = retained(
  listing(import.meta.url, "e2e/playwright.config.ts", { DISPATCH_E2E_QUARANTINE: "only" }, [
    "--pass-with-no-tests",
  ])
);
const plantedConfig = "e2e/__tests__/fixtures/quarantine.config.ts";
const plantedAll = retained(listing(import.meta.url, plantedConfig, {}));
const plantedExcluded = retained(
  listing(import.meta.url, plantedConfig, { DISPATCH_E2E_QUARANTINE: "exclude" })
);
const plantedOnly = retained(
  listing(import.meta.url, plantedConfig, { DISPATCH_E2E_QUARANTINE: "only" }, [
    "--pass-with-no-tests",
  ])
);

/** The projects a quarantined row can belong to: those that take the config's one retry on CI,
 *  which is what makes a row reportable as flaky, and that a run naming no project selects, which
 *  is how the nightly's quarantine leg runs. The `-alone`, `visual` and stubbed projects set
 *  `retries: 0`, so no row of theirs is ever flaky, and the `-alone` ones carry `default: false`. */
const retryingProjects = (realConfig.projects ?? [])
  .filter((project) => project.retries === undefined && project.default !== false)
  .map((project) => project.name ?? "");

test("the quarantine holds at most ten rows, each with its issue and its date", () => {
  expect(retryingProjects).toContain("chromium");
  expect(quarantineFaults(quarantine, retryingProjects)).toEqual([]);
});

// A fix entry's shape: its row, a dispatch:// issue and a full lower-case sha. Whether the sha
// is on main is the nightly's to read; the ten-row bound is the quarantine's alone.
test("every fix names its row, its issue and the full sha of its commit", () => {
  expect(fixFaults(fixes, retryingProjects, quarantine)).toEqual([]);
  expect(fixFaults(looseFix, retryingProjects, quarantine)).toEqual([
    "fix 1 (planted.e2e.ts [chromium] a planted row fixed loosely) names its issue as " +
      "undefined, which is no dispatch://<KEY> reference such as dispatch://INFRA-983",
    'fix 1 (planted.e2e.ts [chromium] a planted row fixed loosely) names its fix as "708CA88", ' +
      "which is no full 40-character lower-case commit sha",
  ]);
});

// The nightly's check names the lines to remove for an entry held too long, as `entryLines`
// prints them, and a person removes those lines: an entry written any other way would be lines
// the file does not hold.
test("every entry is written as the nightly's check prints it", () => {
  const source = readFileSync(fileURLToPath(new URL("../quarantine.ts", import.meta.url)), "utf8");
  expect(
    [...quarantine, ...fixes]
      .filter((entry) => !source.includes(entryLines(entry).join("\n")))
      .map(rowName),
    "Write each entry of e2e/quarantine.ts as `entryLines` prints it: one field per line, in " +
      "the order file, project, title, issue, added, and a value that would pass 100 columns " +
      "on the line after its key, which is what Biome keeps."
  ).toEqual([]);
});

test("a quarantine of eleven rows is refused for its size", () => {
  expect(quarantineFaults(elevenRows, retryingProjects)).toEqual([
    "the quarantine holds 11 rows, and R4 of dispatch://INFRA-983 allows 10: fix a quarantined " +
      "row and take its entry out before another goes in",
  ]);
});

test("an entry with no issue is refused, and so is an entry with no date", () => {
  expect(quarantineFaults(withoutIssue, retryingProjects)).toEqual([
    "entry 1 (planted.e2e.ts [chromium] a planted row with no owner) has no issue: every " +
      "quarantined row is owned by one, dispatch://<KEY>",
  ]);
  expect(quarantineFaults(withoutDate, retryingProjects)).toEqual([
    "entry 1 (planted.e2e.ts [chromium] a planted row with no date) has no date: write the day " +
      'the row came in as added: "YYYY-MM-DD"',
  ]);
});

test("an issue that is no dispatch:// reference and a day that is no calendar day are refused", () => {
  expect(quarantineFaults(malformed, retryingProjects)).toEqual([
    "entry 1 (planted.e2e.ts [chromium] a planted row written loosely) names its issue as " +
      '"INFRA-983", which is no dispatch://<KEY> reference such as dispatch://INFRA-983',
    "entry 1 (planted.e2e.ts [chromium] a planted row written loosely) gives the day it came in " +
      'as "2026-02-30", which is no calendar day written YYYY-MM-DD',
  ]);
});

test("an entry of a project that never retries, and an entry written twice, are refused", () => {
  const entry = {
    file: "heavy-bodies.e2e.ts",
    project: "chromium-alone",
    title: "a planted row",
    issue: "dispatch://INFRA-983",
    added: "2026-11-19",
  };
  expect(quarantineFaults([entry, entry], retryingProjects)).toEqual([
    "entry 1 (heavy-bodies.e2e.ts [chromium-alone] a planted row) names the project " +
      "chromium-alone, whose rows CI never reports flaky and the nightly's quarantine leg does " +
      `not run; a quarantined row is one of ${retryingProjects.join(", ")}`,
    "entry 2 (heavy-bodies.e2e.ts [chromium-alone] a planted row) names the project " +
      "chromium-alone, whose rows CI never reports flaky and the nightly's quarantine leg does " +
      `not run; a quarantined row is one of ${retryingProjects.join(", ")}`,
    "entry 2 (heavy-bodies.e2e.ts [chromium-alone] a planted row) repeats an entry above it",
  ]);
});

test(
  "every quarantined row is a row the suite runs, and the quarantine selects no other",
  async () => {
    expect(selectionFaults(quarantine, rowsOfListing(await quarantinedRows))).toEqual([]);
  },
  rowLimit({ atOnce: ["listing", "listing", "listing", "listing"] })
);

test(
  "the pattern leaves out the rows its entries name and no row that only looks like one",
  async () => {
    const every = rowsOfListing(await plantedAll).map(rowName);
    const left = rowsOfListing(await plantedExcluded).map(rowName);
    const only = rowsOfListing(await plantedOnly);
    const quarantinedNames = [
      "quarantine-rows.fixture.ts [alpha-phone] offline › costs $5 (or more)? @phone",
      "quarantine-rows.fixture.ts [alpha] saves the draft @pr",
    ];
    // The control: with no selection asked for, both quarantined rows are listed with the rest.
    expect(every.sort()).toEqual(
      [
        ...quarantinedNames,
        "quarantine-rows.fixture.ts [alpha] autosave saves the draft @pr",
        "quarantine-rows.fixture.ts [alpha] offline › costs $5 (or more)? @phone",
        "quarantine-rows.fixture.ts [alpha] offline › saves the draft @pr",
        "quarantine-rows.fixture.ts [alpha] saves the draft @pr twice",
      ].sort()
    );
    expect(left.sort()).toEqual(every.filter((name) => !quarantinedNames.includes(name)).sort());
    expect(only.map(rowName).sort()).toEqual(quarantinedNames);
    // The third planted entry names a row the file does not hold, which is the fault the row
    // above reads for the real list.
    expect(selectionFaults(plantedQuarantine, only)).toEqual([
      "quarantine-rows.fixture.ts [alpha] saves the draft is a row the suite does not run: no " +
        "row of alpha has that file and that full title, so it was renamed, moved or deleted, " +
        "or its project no longer selects it; take the entry out, or write the row's title as " +
        "the listing names it",
    ]);
  },
  rowLimit({ atOnce: ["listing", "listing", "listing", "listing"] })
);
