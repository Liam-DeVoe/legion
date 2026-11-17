/** `bun e2e/tools/issue-page-t-record.ts --recorded-on <commit> [--part <branch>=<commit> ...]`:
 *  records the rows the new issue page draws from F's reads into
 *  `e2e/fixtures/issue-page-t-rows.json`, the fixture part T of dispatch://INFRA-1196
 *  (dispatch://INFRA-4062, task t2 of its plan) compares the tree read's rows with.
 *
 *  What it records. For each of the 14 seeds of `e2e/issue-layout-seed.ts`, in the order that
 *  file writes them, on an emptied database, read by alice with the new page on at 1440 by 900
 *  (G's `phase`):
 *
 *  - `loadRequests`: the requests under `/api/` the page sent from the navigation until it went
 *    quiet (`watchApiRequests`, `expectLoadSettled`);
 *  - `opened`: every row as the page opens, folded as it opens, with no body;
 *  - `rows`: every row once every branch is open (`unfoldEveryBranch`), each with the body it
 *    draws while it has the focus;
 *  - `filters`: for Spec, Verification, Plan and Decisions, the keys of the rows the filter
 *    draws, in drawing order, and the count its control draws.
 *
 *  Each row is named by what the seed returned (T's `seedKeys` and `keyedRows`), never by an id
 *  the server minted, and its time by the read it came from, never as an instant. The recorder
 *  fails rather than write a UUID or an instant, and it fails where two rows of the outline have
 *  the same key and the same parent key, since a comparison by key could not tell them apart.
 *
 *  On which commit. The recording base: a commit that holds every part T holds and neither the
 *  model (INFRA-1253's branch) nor any commit of T after R, the commit that adds this file. The
 *  first base is R. `--recorded-on` names the base, and each `--part` one part T holds, by its
 *  branch and the commit of that branch the base holds; give one `--part` for each such part,
 *  and none for a part `main` holds. The recorder runs no git, so it writes the names it is
 *  given. `ISSUE_PAGE_T_RECORDED_ON` and `ISSUE_PAGE_T_PARTS` (a comma-separated list of
 *  `<branch>=<commit>`) give the same values, and a flag wins over its variable.
 *
 *  How to run it, from `packages/dispatch` of a worktree at the base whose `packages/contracts`
 *  and dashboard are built (`bun run build:web`):
 *
 *    bun e2e/harness-up.ts > "$TMPDIR/t-harness.sh" &
 *    …wait for its last line, `export DISPATCH_E2E_REUSE_SERVERS=1`…
 *    . "$TMPDIR/t-harness.sh" && bun e2e/tools/issue-page-t-record.ts --recorded-on <commit> \
 *      --part <branch>=<commit> ...
 *    …then stop the harness by its own process id.
 *
 *  It empties the database `DATABASE_URL` names before each seed, so it runs only where
 *  `DISPATCH_E2E_REUSE_SERVERS=1` says that the variables are a stack's that `e2e/harness-up.ts`
 *  started; it starts no stack of its own.
 *
 *  When to run it again: at task t13, and whenever a part T holds changes what F draws, what the
 *  seeds write, what the page reads at load or what a filter draws. So that two runs on one base
 *  write the same bytes, every object's fields are sorted, and the file is written as Biome
 *  formats it, which is also what `bunx biome check .` requires of it.
 */
import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";
import { type Browser, chromium, expect, type Page } from "@playwright/test";

import { PRESET_NAMES, type Preset } from "../../web/src/features/issue-page/page-words";
import { baseUrl, getIssue } from "../api";
import { nextFrames } from "../frames";
import {
  seedConversationState,
  seedDecisionsFilter,
  seedNoChildren,
  seedNoSummary,
  seedPlanOrder,
  seedReplyNesting,
  seedRequestBaseline,
  seedReviewList,
  seedSpineCases,
  seedTwiceTheNodes,
  seedUnbrokenChildTitle,
  seedUnbrokenWords,
  seedWholeIssue,
  seedWholeIssueAdvancing,
} from "../issue-layout-seed";
import { gotoIssue, presetCount, presetToggle } from "../issue-page";
import { expectIssuePageDrawn, phase, unfoldEveryBranch } from "../issue-page-g";
import { activePreset, expectActivePreset, pickPreset } from "../issue-page-i";
import {
  drawnRowReadings,
  type KeyedRow,
  keyedRows,
  type RowReading,
  seedKeys,
} from "../issue-page-t";
import { resetDatabase } from "../seed";
import { type ApiRequests, expectLoadSettled, watchApiRequests } from "../traffic";

const usage =
  "usage: bun e2e/tools/issue-page-t-record.ts --recorded-on <commit> " +
  "[--part <branch>=<commit> ...]\n" +
  "  --recorded-on  the commit the stack was built from, which holds every part T holds and no\n" +
  "                 model (else ISSUE_PAGE_T_RECORDED_ON)\n" +
  "  --part         one part T holds, by its branch and the commit of it the base holds; once\n" +
  "                 for each part main does not hold (else ISSUE_PAGE_T_PARTS, a comma-separated\n" +
  "                 list of <branch>=<commit>)";

/** What every seed returns: the key of the issue the page opens. */
interface SeededIssue {
  readonly key: string;
}

/** The 14 seeds, by the name the fixture records each under, in the order the seed file writes
 *  them. */
const SEEDS = {
  seedWholeIssue,
  seedWholeIssueAdvancing,
  seedSpineCases,
  seedNoChildren,
  seedNoSummary,
  seedReplyNesting,
  seedUnbrokenWords,
  seedUnbrokenChildTitle,
  seedRequestBaseline,
  seedTwiceTheNodes,
  seedConversationState,
  seedDecisionsFilter,
  seedReviewList,
  seedPlanOrder,
} satisfies Readonly<Record<string, () => Promise<SeededIssue>>>;

/** The four filters, in the order the view row draws them. Conversation, the fifth control, is
 *  the page with no filter on. */
const FILTERS = ["spec", "verification", "plan", "decisions"] as const satisfies readonly Preset[];
type Filter = (typeof FILTERS)[number];

/** How long one action may take: the suite's own `actionTimeout` (`e2e/playwright.config.ts`),
 *  which a page opened outside Playwright's test runner does not inherit. */
const ACTION_MS = 45_000;

/** How long the view row may take to show the view a press asked for: the suite's own expect
 *  timeout. An `expect` outside the test runner waits 5 s unless it is told otherwise, and I's
 *  `expectActivePreset` names no timeout, so the recorder waits this long before calling it. */
const VIEW_MS = 15_000;

/** How many readings a steady reading takes before it gives up on the page settling. */
const STEADY_TRIES = 20;

/** A commit: the short or the full hash. */
const COMMIT = /^[0-9a-f]{7,64}$/;

/** What the recorder refuses to write: an id the server minted, or a moment in time, both of
 *  which belong to one run, so the next run would write them differently. */
const MINTED: readonly { readonly pattern: RegExp; readonly what: string }[] = [
  { pattern: /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i, what: "a UUID" },
  { pattern: /\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}/, what: "an instant" },
];

const repoRoot = fileURLToPath(new URL("../../../../", import.meta.url));
const fixturePath = fileURLToPath(new URL("../fixtures/issue-page-t-rows.json", import.meta.url));

/** The commits the fixture says it was recorded on. */
interface RecordingBase {
  readonly parts: Readonly<Record<string, string>>;
  readonly recordedOn: string;
}

/** What one filter drew, before its rows are named. */
interface FilterReading {
  readonly count: string | null;
  readonly filter: Filter;
  readonly rows: readonly RowReading[];
}

/** What one seed's page drew, before its rows are named. */
interface PageReading {
  readonly filters: readonly FilterReading[];
  readonly loadRequests: number;
  readonly opened: readonly RowReading[];
  readonly rows: readonly RowReading[];
}

/** One filter in the fixture: the keys of the rows it draws, and its control's count. */
interface FilterRecord {
  readonly count: number;
  readonly rows: readonly string[];
}

/** One seed in the fixture. */
interface SeedRecord {
  readonly filters: Readonly<Record<string, FilterRecord>>;
  readonly loadRequests: number;
  readonly opened: readonly KeyedRow[];
  readonly rows: readonly KeyedRow[];
}

function refuse(message: string): never {
  console.error(`${message}\n\n${usage}`);
  process.exit(2);
}

function givenArguments(): { readonly part?: string[]; readonly "recorded-on"?: string } {
  try {
    return parseArgs({
      args: process.argv.slice(2),
      options: { part: { multiple: true, type: "string" }, "recorded-on": { type: "string" } },
      strict: true,
    }).values;
  } catch (error) {
    return refuse(error instanceof Error ? error.message : String(error));
  }
}

/** The base and its parts, from the flags, else from the variables. */
function recordingBase(): RecordingBase {
  const given = givenArguments();
  const recordedOn = given["recorded-on"] ?? process.env.ISSUE_PAGE_T_RECORDED_ON ?? "";
  if (recordedOn === "") {
    refuse("Name the commit the stack was built from: --recorded-on <commit>.");
  }
  if (!COMMIT.test(recordedOn)) {
    refuse(`The recording base ${JSON.stringify(recordedOn)} is not a commit's hash.`);
  }
  const named =
    given.part ??
    (process.env.ISSUE_PAGE_T_PARTS ?? "")
      .split(",")
      .map((entry) => entry.trim())
      .filter((entry) => entry !== "");
  const parts = new Map<string, string>();
  for (const entry of named) {
    // A branch name may hold `=`, and a hash never does, so the last one divides the two.
    const at = entry.lastIndexOf("=");
    const branch = entry.slice(0, at);
    const commit = entry.slice(at + 1);
    if (at <= 0 || !COMMIT.test(commit)) {
      refuse(`The part ${JSON.stringify(entry)} is not <branch>=<commit>.`);
    }
    if (parts.has(branch)) refuse(`The part ${branch} is named twice.`);
    parts.set(branch, commit);
  }
  return { parts: Object.fromEntries(parts), recordedOn };
}

/**
 * Reads `read` until two readings in a row are the same. Before each reading it waits for the
 * page to go quiet, and after it for two frames, so a row or a count that is still arriving is
 * not recorded half drawn. A page that never settles fails the recording rather than leave a
 * half-drawn moment of it in the fixture.
 */
async function steadily<T>(
  page: Page,
  traffic: ApiRequests,
  what: string,
  read: () => Promise<T>
): Promise<T> {
  let last: string | undefined;
  for (let tries = 0; tries < STEADY_TRIES; tries += 1) {
    await expectLoadSettled(traffic);
    const reading = await read();
    const said = JSON.stringify(reading);
    if (said === last) return reading;
    last = said;
    await nextFrames(page);
  }
  throw new Error(`${what} changed at each of ${STEADY_TRIES} readings, so it never settled`);
}

/** Waits until the view row has the view `name` on, then holds the page to it with I's own
 *  check. */
async function expectViewOn(page: Page, name: string): Promise<void> {
  await expect
    .poll(() => activePreset(page), {
      message: `the view row never turned ${name} on`,
      timeout: VIEW_MS,
    })
    .toBe(name);
  await expectActivePreset(page, name);
}

/** Turns `filter` on, reads what it draws once it is steady, and turns it off again, which puts
 *  the page back on Conversation. */
async function readFilter(
  page: Page,
  traffic: ApiRequests,
  filter: Filter
): Promise<FilterReading> {
  const name = PRESET_NAMES[filter];
  await pickPreset(page, name);
  await expectViewOn(page, name);
  const drawn = await steadily(page, traffic, `the ${name} filter`, async () => ({
    count: await presetCount(page, filter).textContent(),
    rows: await drawnRowReadings(page, { bodies: false }),
  }));
  // Pressing the control that is on turns it off (`IssuePresets.tsx`'s `press`).
  await presetToggle(page, filter).click();
  await expectViewOn(page, PRESET_NAMES.conversation);
  return { ...drawn, filter };
}

/** Everything the recorder reads of issue `key`'s page, in the order a reader reaches it. */
async function readPage(page: Page, key: string): Promise<PageReading> {
  const traffic = watchApiRequests(page);
  // The page has no base URL outside the test runner, so the address is a whole one.
  await gotoIssue(page, new URL(`/issues/${key}`, baseUrl).href);
  await expectLoadSettled(traffic);
  const loadRequests = traffic.total();
  await expectIssuePageDrawn(page);
  const opened = await steadily(page, traffic, `${key} as the page opens`, () =>
    drawnRowReadings(page, { bodies: false })
  );
  await unfoldEveryBranch(page);
  await steadily(page, traffic, `${key} with every branch open`, () =>
    drawnRowReadings(page, { bodies: false })
  );
  const rows = await drawnRowReadings(page);
  const filters: FilterReading[] = [];
  for (const filter of FILTERS) filters.push(await readFilter(page, traffic, filter));
  return { filters, loadRequests, opened, rows };
}

/** The number a filter's control draws as its count. */
function countOf(drawn: string | null, where: string): number {
  const count = (drawn ?? "").trim();
  if (!/^\d+$/.test(count)) {
    throw new Error(`${where} draws ${JSON.stringify(drawn)} as its count, which is no number`);
  }
  return Number(count);
}

/** Fails where two rows have the same key under the same parent key: a comparison by key could
 *  not tell them apart. */
function expectRowsNamedApart(rows: readonly KeyedRow[], where: string): void {
  const named = new Set<string>();
  for (const row of rows) {
    const name = JSON.stringify([row.parentKey, row.key]);
    if (named.has(name)) {
      throw new Error(
        `${where}, two rows are named ${row.key} under ${row.parentKey ?? "the issue"}, so a ` +
          "comparison by key and parent key could not tell them apart"
      );
    }
    named.add(name);
  }
}

/** One seed's readings, each row named by what the seed returned (`seedKeys`, `keyedRows`). */
function seedRecord(
  drawn: PageReading,
  keys: ReadonlyMap<string, string>,
  seed: string
): SeedRecord {
  const opened = keyedRows(drawn.opened, keys);
  const rows = keyedRows(drawn.rows, keys);
  expectRowsNamedApart(opened, `In ${seed} as the page opens`);
  expectRowsNamedApart(rows, `In ${seed} with every branch open`);
  const filters = drawn.filters.map(({ count, filter, rows: filterRows }) => {
    const record: FilterRecord = {
      count: countOf(count, `${seed}'s ${PRESET_NAMES[filter]} control`),
      rows: keyedRows(filterRows, keys).map((row) => row.key),
    };
    return [filter, record] as const;
  });
  return { filters: Object.fromEntries(filters), loadRequests: drawn.loadRequests, opened, rows };
}

/** Empties the database, seeds it with `seed`, and records what the page draws of the issue. */
async function recordSeed(
  browser: Browser,
  name: string,
  seed: () => Promise<SeededIssue>
): Promise<SeedRecord> {
  await resetDatabase();
  const recorded: SeedRecord[] = [];
  await phase(browser, seed, async (page, seeded) => {
    page.setDefaultTimeout(ACTION_MS);
    const drawn = await readPage(page, seeded.key);
    // A block's node is named under its document's slug (`seedKeys`), and the seeds return the
    // documents' ids and not their slugs. The read comes after the page has drawn everything it
    // records, so it cannot change any of it.
    const slugs = ((await getIssue(seeded.key)).artifacts ?? []).map((artifact) => artifact.slug);
    recorded.push(seedRecord(drawn, seedKeys(seeded, slugs), name));
  });
  const [record] = recorded;
  if (record === undefined) throw new Error(`${name} recorded nothing`);
  return record;
}

/** `value` with every object's fields in sorted order at every depth, so the bytes of the file
 *  do not depend on the order a reader or a seed wrote its fields in. A list keeps its order,
 *  which is the page's. */
function sortedFields(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(sortedFields);
  if (typeof value !== "object" || value === null) return value;
  const fields: Readonly<Record<string, unknown>> = { ...value };
  return Object.fromEntries(
    Object.keys(fields)
      .sort()
      .map((field) => [field, sortedFields(fields[field])])
  );
}

/** Fails at the first string of `value`, a field's name included, that holds a UUID or an
 *  instant, naming where it stands. */
function expectNothingMinted(value: unknown, path: string): void {
  if (typeof value === "string") {
    for (const { pattern, what } of MINTED) {
      const found = pattern.exec(value);
      if (found !== null) {
        throw new Error(
          `${path} holds ${what}, ${found[0]}, which belongs to this run alone: the next run ` +
            "would write another, so the fixture cannot hold it"
        );
      }
    }
  } else if (Array.isArray(value)) {
    for (const [index, item] of value.entries()) expectNothingMinted(item, `${path}[${index}]`);
  } else if (typeof value === "object" && value !== null) {
    for (const [field, item] of Object.entries(value)) {
      expectNothingMinted(field, `the name of a field of ${path}`);
      expectNothingMinted(item, `${path}.${field}`);
    }
  }
}

/** `json` as Biome formats it, which is what `bunx biome check .` holds a committed file to:
 *  Biome writes a short list on one line, where `JSON.stringify` gives each item a line. */
function biomeFormatted(json: string): string {
  const biome = Bun.which("biome", {
    PATH: [join(repoRoot, "node_modules/.bin"), process.env.PATH ?? ""].join(":"),
  });
  if (biome === null) {
    throw new Error("The recorder writes the fixture through Biome: run `bun install` first.");
  }
  const formatted = Bun.spawnSync({
    cmd: [biome, "format", `--stdin-file-path=${relative(repoRoot, fixturePath)}`],
    cwd: repoRoot,
    stderr: "inherit",
    stdin: new TextEncoder().encode(json),
  });
  if (formatted.exitCode !== 0) throw new Error("biome format refused the fixture");
  const source = formatted.stdout.toString();
  // Biome answers nothing for a file over its size limit (`files.maxSize`, 1 MiB unless the
  // configuration says otherwise), and `biome check` passes over such a file in the same way,
  // so the file is then written as `JSON.stringify` wrote it.
  return source === "" ? json : source;
}

if (process.env.DISPATCH_E2E_REUSE_SERVERS !== "1") {
  console.error(
    "issue-page-t-record.ts reads a stack that is already running, and empties its database.\n" +
      "Start one from packages/dispatch with\n" +
      '  bun e2e/harness-up.ts > "$TMPDIR/t-harness.sh" &\n' +
      "wait for its last line, `export DISPATCH_E2E_REUSE_SERVERS=1`, then run\n" +
      '  . "$TMPDIR/t-harness.sh" && bun e2e/tools/issue-page-t-record.ts ...'
  );
  process.exit(2);
}

const base = recordingBase();
const seeds: Record<string, SeedRecord> = {};
const browser = await chromium.launch();
try {
  for (const [name, seed] of Object.entries(SEEDS)) {
    const record = await recordSeed(browser, name, seed);
    seeds[name] = record;
    console.error(
      `${name}: ${record.loadRequests} requests at load, ${record.opened.length} rows as the ` +
        `page opens, ${record.rows.length} with every branch open`
    );
  }
} finally {
  await browser.close();
}

const fixture = sortedFields({ parts: base.parts, recordedOn: base.recordedOn, seeds });
expectNothingMinted(fixture, "the fixture");
mkdirSync(dirname(fixturePath), { recursive: true });
writeFileSync(fixturePath, biomeFormatted(`${JSON.stringify(fixture, null, 2)}\n`));
console.error(`wrote ${relative(process.cwd(), fixturePath)}`);
