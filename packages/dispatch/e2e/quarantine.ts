/** The browser suite's quarantine (R4 of dispatch://INFRA-983): the rows that still run but gate
 *  nothing, each owned by an issue.
 *
 * A row the merge job of `.github/workflows/dispatch-browser.yaml` names flaky in two runs of 14
 * days is a defect, and while its owner works on it the row leaves the runs that gate a change -
 * the pull-request lane and the merge queue's full lane, both the shard job's - and keeps running
 * in the nightly's quarantine leg, whose result is reported and fails nothing. So the owner sees
 * whether a fix worked, and nobody else's change is held by it. `quarantine` below is the list,
 * and it holds at most `quarantineLimit` rows.
 *
 * Three readers hold the list to that:
 *
 * - `e2e/playwright.config.ts` passes every project through `quarantined`, which adds one
 *   `grepInvert` pattern to it: under `DISPATCH_E2E_QUARANTINE=exclude` the pattern drops the
 *   quarantined rows, under `only` it drops every other row, and with the variable unset the
 *   project is returned as it is, so a listing or a run that names no such mode reads the whole
 *   suite. The shard job's browser step sets `exclude`; the nightly's quarantine leg sets `only`.
 * - `e2e/__tests__/quarantine.test.ts` refuses a list of more than `quarantineLimit` rows, an
 *   entry with no issue or no date or a malformed one, and an entry whose row the suite does not
 *   run. It never compares a date with today: a pull request is no place for a check that turns
 *   red on a day nobody chose.
 * - `e2e/tools/quarantine-check.ts`, which the nightly runs, does what depends on the date: it
 *   names a row flaky in two runs of 14 days that is not here, and an entry held longer than 14
 *   days, each with the exact lines to add or to remove.
 *
 * This module imports nothing, so the tool reads it with no install and no build.
 */

/** One quarantined row, named as the merge job's `dispatch-browser flaky row` notice names it:
 *  `<file>:<line> [<project>] <title>`. The line is not part of it, because it moves with every
 *  edit above the row; the file, the project and the full title are the row. */
export interface QuarantineEntry {
  /** The spec file, relative to `e2e/`, as the notice prints it: `pictures.e2e.ts`. */
  readonly file: string;
  /** The project the row was flaky in, as the notice prints it between the brackets. */
  readonly project: string;
  /** The row's full title as the notice prints it: its `describe` titles first, each followed by
   *  ` › `, then its own title with its tags. */
  readonly title: string;
  /** The issue that owns the row, as `dispatch://<KEY>`. */
  readonly issue: string;
  /** The day the row came in, `YYYY-MM-DD`. */
  readonly added: string;
}

/** The quarantined rows. An entry is written as `entryLines` prints it, which is what
 *  `e2e/tools/quarantine-check.ts` names as the lines to add or to remove. */
export const quarantine: readonly QuarantineEntry[] = [];

/** How many rows the quarantine may hold (R4 of dispatch://INFRA-983). */
export const quarantineLimit = 10;

/** An entry as a file may hold one, before anything has checked it: every field may be missing
 *  or of any type. It is what `quarantineFaults` reads, so a fixture can plant the entry the
 *  type above would refuse to compile. */
export type UncheckedEntry = { readonly [Field in keyof QuarantineEntry]?: unknown };

/** What a run asks of the quarantine, from `DISPATCH_E2E_QUARANTINE`: `all` (unset) runs every
 *  row, `exclude` leaves the quarantined rows out, and `only` runs them and nothing else. */
export type QuarantineRun = "all" | "exclude" | "only";

/** What joins a row's `describe` titles to its own in a listing and in the notice:
 *  `titleSeparator` of `e2e/configurations.ts`, whose `rowsOfListing` reads a listing that way,
 *  and the merge job's "Reading two" step, which prints the notice. */
const titleSeparator = " › ";

const issuePattern = /^dispatch:\/\/[A-Z][A-Z0-9]*-[1-9][0-9]*$/;
const datePattern = /^(\d{4})-(\d{2})-(\d{2})$/;

/** The run `value` (`DISPATCH_E2E_QUARANTINE`) asks for, or a refusal naming the three values:
 *  a misspelled mode read as "every row" would put a quarantined row back into a gating run with
 *  nothing to say so. */
export function quarantineRun(value: string | undefined): QuarantineRun {
  if (value === undefined || value === "") return "all";
  if (value === "exclude" || value === "only") return value;
  throw new Error(
    `DISPATCH_E2E_QUARANTINE must be "exclude", "only" or unset, not ${JSON.stringify(value)}. ` +
      "Unset runs every row; `exclude` leaves the rows of e2e/quarantine.ts out, which is what " +
      "the gating lanes run; `only` runs those rows and no other, which is the nightly's " +
      "quarantine leg."
  );
}

/** A row as the quarantine names it: the fields of `QuarantineEntry` that are the row. */
export type QuarantineRow = Pick<QuarantineEntry, "file" | "project" | "title">;

/** `row` as the merge job's notice prints it, without the line: `<file> [<project>] <title>`.
 *  It is what an entry, a listed row and a notice are compared by. */
export function rowName(row: QuarantineRow): string {
  return `${row.file} [${row.project}] ${row.title}`;
}

/** The string Playwright 1.64.1 matches a project's `grep` and `grepInvert` against, for one row:
 *  the project's name, the file's path relative to the config's root, each `describe` title and
 *  the row's own title, joined by single spaces. `createProjectSuite`
 *  (node_modules/.bun/playwright@1.64.1/node_modules/playwright/lib/runner/index.js:2644-2657)
 *  matches `test._grepTitleWithTags()`, which `_grepBaseTitlePath` builds from
 *  `Suite._collectGrepTitlePath` (playwright/lib/common/index.js:2623-2629 and 2845-2855) over
 *  a project suite that has no parent yet. A tag given in a row's `{ tag }` option would follow
 *  its title there; this suite writes its tags into the title, so the title carries them. */
function grepTitle(row: QuarantineRow): string {
  return [row.project, row.file, ...row.title.split(titleSeparator)].join(" ");
}

/** `project` with the quarantine applied for `run`: under `exclude` one more `grepInvert` pattern
 *  that matches the grep title of each entry's row and nothing else - anchored at both ends, so a
 *  row whose title ends with an entry's, a row of another project and a row of another file all
 *  stay - and under `only` one that matches every grep title but those. The project's own `grep`
 *  and `grepInvert` stay in force, so `only` runs a quarantined row only in a project that
 *  selects it. Under `all`, and under `exclude` with no entry, it is `project` itself. */
export function quarantined<
  Project extends { readonly name: string; readonly grepInvert?: RegExp | RegExp[] },
>(project: Project, entries: readonly QuarantineEntry[], run: QuarantineRun): Project {
  if (run === "all" || (run === "exclude" && entries.length === 0)) return project;
  const rows = entries
    .map((entry) => grepTitle(entry).replace(/[.*+?^${}()|[\]\\/]/g, "\\$&"))
    .join("|");
  // With no entry, `only` runs nothing: the empty pattern matches every grep title.
  const invert =
    run === "exclude"
      ? new RegExp(`^(?:${rows})$`)
      : entries.length === 0
        ? /(?:)/
        : new RegExp(`^(?!(?:${rows})$)`);
  const own =
    project.grepInvert === undefined
      ? []
      : Array.isArray(project.grepInvert)
        ? project.grepInvert
        : [project.grepInvert];
  return { ...project, grepInvert: [...own, invert] };
}

function realDay(value: string): boolean {
  const parts = datePattern.exec(value);
  if (parts === null) return false;
  const [year, month, day] = parts.slice(1).map(Number);
  const date = new Date(Date.UTC(year, month - 1, day));
  return (
    date.getUTCFullYear() === year && date.getUTCMonth() === month - 1 && date.getUTCDate() === day
  );
}

/** What is wrong with `entries` as a quarantine, one sentence per fault, each naming the entry:
 *  more than `quarantineLimit` of them, a missing or empty file, project or title, a missing or
 *  malformed issue or date, an entry repeated, and a project outside `projects` - the projects a
 *  flaky row can come from, which are the ones that retry a failed row on CI and run in the
 *  nightly's quarantine leg (`e2e/__tests__/quarantine.test.ts` reads them from the config). It
 *  reads no date against today. */
export function quarantineFaults(
  entries: readonly UncheckedEntry[],
  projects: readonly string[]
): string[] {
  const faults: string[] = [];
  if (entries.length > quarantineLimit) {
    faults.push(
      `the quarantine holds ${entries.length} rows, and R4 of dispatch://INFRA-983 allows ` +
        `${quarantineLimit}: fix a quarantined row and take its entry out before another goes in`
    );
  }
  const seen = new Set<string>();
  entries.forEach((entry, index) => {
    const { file, project, title } = entry;
    const name =
      typeof file === "string" && typeof project === "string" && typeof title === "string"
        ? `entry ${index + 1} (${rowName({ file, project, title })})`
        : `entry ${index + 1}`;
    for (const field of ["file", "project", "title"] as const) {
      if (typeof entry[field] !== "string" || entry[field] === "") {
        faults.push(`${name} names no ${field}, and the row is its file, project and title`);
      }
    }
    if (entry.issue === undefined) {
      faults.push(`${name} has no issue: every quarantined row is owned by one, dispatch://<KEY>`);
    } else if (typeof entry.issue !== "string" || !issuePattern.test(entry.issue)) {
      faults.push(
        `${name} names its issue as ${JSON.stringify(entry.issue)}, which is no ` +
          "dispatch://<KEY> reference such as dispatch://INFRA-983"
      );
    }
    if (entry.added === undefined) {
      faults.push(`${name} has no date: write the day the row came in as added: "YYYY-MM-DD"`);
    } else if (typeof entry.added !== "string" || !realDay(entry.added)) {
      faults.push(
        `${name} gives the day it came in as ${JSON.stringify(entry.added)}, which is no ` +
          "calendar day written YYYY-MM-DD"
      );
    }
    if (typeof project === "string" && project !== "" && !projects.includes(project)) {
      faults.push(
        `${name} names the project ${project}, whose rows CI never reports flaky and the ` +
          `nightly's quarantine leg does not run; a quarantined row is one of ${projects.join(", ")}`
      );
    }
    const key = JSON.stringify([file, project, title]);
    if (seen.has(key)) faults.push(`${name} repeats an entry above it`);
    seen.add(key);
  });
  return faults;
}

/** What is wrong with the rows a listing under `DISPATCH_E2E_QUARANTINE=only` selected, against
 *  the entries it was listed with: an entry whose row it did not select - the suite has no such
 *  row in that project, under that file and title - and a selected row no entry names, which is a
 *  row whose grep title another row shares (Playwright joins a row's titles with spaces, so
 *  `a › b c` and `a b › c` read alike there). */
export function selectionFaults(
  entries: readonly QuarantineEntry[],
  selected: readonly QuarantineRow[]
): string[] {
  const named = new Set(entries.map(rowName));
  const listed = new Set(selected.map(rowName));
  const faults: string[] = [];
  for (const entry of entries) {
    if (!listed.has(rowName(entry))) {
      faults.push(
        `${rowName(entry)} is a row the suite does not run: no row of ${entry.project} has ` +
          "that file and that full title, so it was renamed, moved or deleted, or its project " +
          "no longer selects it; take the entry out, or write the row's title as the listing " +
          "names it"
      );
    }
  }
  for (const row of selected) {
    if (!named.has(rowName(row))) {
      faults.push(
        `the quarantine selects ${rowName(row)}, which no entry names: its grep title is a ` +
          "quarantined row's, so Playwright cannot tell the two apart"
      );
    }
  }
  return faults;
}

/** `entry` as `quarantine` above holds it, in the layout Biome keeps: one field per line, and a
 *  field whose line would pass biome.json's `lineWidth` of 100 with its value on the line after
 *  its key, which is where Biome breaks such a property. So the lines
 *  `e2e/tools/quarantine-check.ts` names to add or to remove are the lines the file holds, and
 *  `e2e/__tests__/quarantine.test.ts` holds every real entry to them. */
export function entryLines(entry: QuarantineEntry): string[] {
  const fields = (["file", "project", "title", "issue", "added"] as const).flatMap((field) => {
    const line = `    ${field}: ${JSON.stringify(entry[field])},`;
    return line.length <= 100
      ? [line]
      : [`    ${field}:`, `      ${JSON.stringify(entry[field])},`];
  });
  return ["  {", ...fields, "  },"];
}
