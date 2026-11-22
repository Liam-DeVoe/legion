/** The nightly's half of the quarantine, `e2e/tools/quarantine-check.ts`, driven through its pure
 *  part with the planted history of `fixtures/quarantine-history.fixture.ts`: what `decide`
 *  names, the lines `report` gives for each, and how whether a run's head holds a row's own last
 *  change and its fix moves what counts, and the refusal `blame` gives an answer that holds no
 *  blame, read from a stand-in for GitHub. The moment of each of those readings is the fixture's.
 *
 *  Its reads are driven against `gitHubStandIn` below, a stand-in for GitHub's REST and GraphQL
 *  API on a local port: what `readHistory` reads of the answers GitHub gave for eleven runs
 *  (`fixtures/quarantine-answers.fixture.json`) and what `decide` and `report` say of it, what
 *  the reading costs, its pages, the compares `readHolds` reads and what it says of each answer,
 *  and the client's waits for GitHub's rate limit, which read the clock and wait a few real
 *  seconds. The deadline those waits are held to is read off a stand-in `/proc`
 *  (`readDeadline`). Nothing here reads GitHub itself. */
import { expect, test } from "bun:test";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { entryLines } from "../quarantine";
import {
  annotationsPerPage,
  blame,
  checkRunsPerPage,
  countedRuns,
  decide,
  type FixReading,
  gitHub,
  type Holds,
  lastChange,
  pairKey,
  type RowChange,
  type RunReading,
  readDeadline,
  readHistory,
  readHolds,
  report,
  rowLines,
  suitesPerCall,
  windowDays,
} from "../tools/quarantine-check";
import {
  blames,
  eighteenDays,
  fourteenDays,
  head,
  history,
  picturesEntry,
  picturesFix,
  picturesRow,
  readAt,
  stillRows,
} from "./fixtures/quarantine-history.fixture";

const pictures = "pictures.e2e.ts [chromium] a picture uploads @pr";

/** The rows of `stillRows` with the pictures row's last change read from `ranges`, as the
 *  adapter reads it: its own lines from the listing, their newest commit from the blame. */
function withPicturesBlame(ranges: (typeof blames)[string]): Map<string, RowChange> {
  const { end, start } = rowLines(picturesRow.line, picturesRow.fileRows, picturesRow.fileLength);
  const commit = lastChange(ranges, start, end);
  if (commit === undefined) throw new Error("the planted blame holds none of the row's lines");
  return new Map([
    ...stillRows,
    [pictures, { ...commit, lines: `pictures.e2e.ts:${start}-${end}` }],
  ]);
}

/** Whether each head of `history` holds each commit of `changes` and of `fixReadings`, by
 *  `pairKey`: every one does, but where `except` says otherwise. */
function held(
  changes: ReadonlyMap<string, RowChange>,
  fixReadings: readonly FixReading[] = [],
  except: Readonly<Record<string, boolean | string>> = {}
): Holds {
  const commits = [
    ...[...changes.values()].map((change) => change.oid),
    ...fixReadings.map((reading) => reading.entry.fixed),
  ];
  return new Map<string, boolean | string>([
    ...history.flatMap((run) =>
      commits.map((commit): [string, boolean] => [pairKey(commit, run.headSha), true])
    ),
    ...Object.entries(except),
  ]);
}

const named = (
  changes: ReadonlyMap<string, RowChange>,
  fixes: readonly FixReading[] = [],
  holds: Holds = held(changes, fixes)
) =>
  decide(history, [], fixes, changes, readAt, holds).toQuarantine.map(({ row, runs }) => [
    `${row.file} ${row.title}`,
    runs.map((run) => run.id),
  ]);

test("a row flaky in two runs of the fourteen days is named, with its runs and the lines to add", () => {
  const decision = decide(history, [], [], stillRows, readAt, held(stillRows));
  expect(decision.toQuarantine.map(({ row, runs }) => [row, runs.map((run) => run.id)])).toEqual([
    [{ file: "pictures.e2e.ts", project: "chromium", title: "a picture uploads @pr" }, [101, 102]],
  ]);
  expect(decision.overdue).toEqual([]);
  const { annotations, failed, summary } = report(decision, readAt);
  expect(failed).toBe(true);
  expect(summary).toContain(
    "- `pictures.e2e.ts [chromium] a picture uploads @pr`, flaky in 2 runs whose heads hold the " +
      "last change of its own lines (pictures.e2e.ts:1-40), " +
      "1111111111111111111111111111111111111111 at " +
      "2026-10-01T00:00:00Z: [run 101](https://github.com/cybermonklabs/legion/actions/runs/101) " +
      "of 2026-11-08 (schedule), [run 102](https://github.com/cybermonklabs/legion/actions/runs/102) " +
      "of 2026-11-12 (merge_group)."
  );
  const lines = summary.slice(summary.indexOf("  ```ts") + 1, summary.indexOf("  ```ts") + 8);
  expect(lines).toEqual([
    "    {",
    '      file: "pictures.e2e.ts",',
    '      project: "chromium",',
    '      title: "a picture uploads @pr",',
    '      issue: "dispatch://<KEY>",',
    '      added: "2026-11-19",',
    "    },",
  ]);
  expect(annotations).toEqual([
    "::error title=dispatch-browser quarantine::pictures.e2e.ts [chromium] a picture uploads " +
      "@pr was flaky in 2 runs of 14 days and is not quarantined: " +
      "https://github.com/cybermonklabs/legion/actions/runs/101 " +
      "https://github.com/cybermonklabs/legion/actions/runs/102",
  ]);
});

test("the same history names nothing once that row is quarantined", () => {
  const decision = decide(history, [picturesEntry], [], stillRows, readAt, held(stillRows));
  expect(decision.toQuarantine).toEqual([]);
  const { annotations, failed, summary } = report(decision, readAt);
  expect(failed).toBe(false);
  expect(annotations).toEqual([]);
  expect(summary).toContain("No entry has been held longer than 14 days.");
});

// `keyed-writes.e2e.ts`'s row is named by two attempts of run 102 and by no other run, and
// `mermaid.e2e.ts`'s by run 103 and by a nightly 18 days before the reading: each is one run.
test("a row flaky in one run is not named, however many attempts of it named the row", () => {
  const decision = decide(history, [picturesEntry], [], stillRows, readAt, held(stillRows));
  expect(decision.toQuarantine).toEqual([]);
  expect(decision.counted.map((run) => run.id)).toEqual([101, 102, 103, 106]);
});

// `deep-links.e2e.ts`'s row is flaky in merge group 103 and in pull request 104. The control is
// the same history with 104 a merge group, where the row is named: the event is what kept it out.
test("a pull request's flaky row does not count", () => {
  const asMergeGroup = history.map((run) =>
    run.id === 104 ? { ...run, event: "merge_group" } : run
  );
  expect(
    decide(asMergeGroup, [picturesEntry], [], stillRows, readAt, held(stillRows)).toQuarantine.map(
      ({ row, runs }) => [`${row.file} ${row.title}`, runs.map((run) => run.id)]
    )
  ).toEqual([["deep-links.e2e.ts a held chunk lands @firefox", [103, 104]]]);
});

// The row's own lines are 10 to 30: from its line to the line before the file's next row. Their
// last change, 2222…, is of 2026-11-13, after both runs that named the row.
test("a row flaky twice in runs whose heads do not hold its own lines' last change is not named, and the runs left out say why", () => {
  const changes = withPicturesBlame(blames.ownLinesAfter);
  const holds = held(changes, [], {
    [pairKey("2".repeat(40), head(101))]: false,
    [pairKey("2".repeat(40), head(102))]: false,
  });
  expect(named(changes, [], holds)).toEqual([]);
  const decision = decide(history, [], [], changes, readAt, holds);
  expect(decision.leftOut.map(({ missing, run, why }) => [run.id, missing, why])).toEqual([
    [
      101,
      "change",
      `its head ${head(101)} does not hold its own lines' last change, ${"2".repeat(40)} at ` +
        "2026-11-13T09:00:00Z",
    ],
    [
      102,
      "change",
      `its head ${head(102)} does not hold its own lines' last change, ${"2".repeat(40)} at ` +
        "2026-11-13T09:00:00Z",
    ],
  ]);
  expect(report(decision, readAt).summary).toContain(
    "- `pictures.e2e.ts [chromium] a picture uploads @pr` in " +
      "[run 101](https://github.com/cybermonklabs/legion/actions/runs/101) of 2026-11-08 " +
      `(schedule): its head ${head(101)} does not hold its own lines' last change, ` +
      `${"2".repeat(40)} at 2026-11-13T09:00:00Z.`
  );
});

// The same change of 2026-11-13, after both runs, and both heads hold it: a run's time decides
// nothing.
test("a row flaky twice in runs created before its own lines last changed, whose heads hold that change, is named", () => {
  expect(named(withPicturesBlame(blames.ownLinesAfter))).toEqual([
    ["pictures.e2e.ts a picture uploads @pr", [101, 102]],
  ]);
});

test("a row flaky twice after a change elsewhere in its file is named", () => {
  expect(named(withPicturesBlame(blames.elsewhereAfter))).toEqual([
    ["pictures.e2e.ts a picture uploads @pr", [101, 102]],
  ]);
});

// The change is of 2026-11-10, between run 101 of 11-08 and run 102 of 11-12, and the heads say
// the opposite of the times: 101's holds the change, and 102's does not.
test("a row flaky in one run whose head holds its own lines' last change, and in one whose head does not, is not named", () => {
  const changes = withPicturesBlame(blames.ownLinesBetween);
  const holds = held(changes, [], { [pairKey("4".repeat(40), head(102))]: false });
  expect(named(changes, [], holds)).toEqual([]);
  expect(decide(history, [], [], changes, readAt, holds).leftOut.map(({ run }) => run.id)).toEqual([
    102,
  ]);
});

// The fix's commit time says the opposite of the heads: it is before both runs where their heads
// do not hold it, and after both where they do.
test("a row flaky twice in runs whose heads do not hold its fix is not named, and flaky twice in runs whose heads hold it is", () => {
  const fix = "5".repeat(40);
  const before = [{ committedDate: "2026-11-06T09:00:00Z", entry: picturesFix }];
  const holds = held(stillRows, before, {
    [pairKey(fix, head(101))]: false,
    [pairKey(fix, head(102))]: false,
  });
  expect(named(stillRows, before, holds)).toEqual([]);
  expect(
    decide(history, [], before, stillRows, readAt, holds).leftOut.map(({ missing, run, why }) => [
      run.id,
      missing,
      why,
    ])
  ).toEqual([
    [101, "fix", `its head ${head(101)} does not hold its fix, ${fix}`],
    [102, "fix", `its head ${head(102)} does not hold its fix, ${fix}`],
  ]);
  const after = [{ committedDate: "2026-11-13T09:00:00Z", entry: picturesFix }];
  expect(named(stillRows, after)).toEqual([["pictures.e2e.ts a picture uploads @pr", [101, 102]]]);
});

// Nightly 45045453036, started by hand at this branch's head ff963910, blamed the row's own lines
// (96-147) there: their last change is c2a88d81, this branch's own commit of 2026-11-20T04:41:21Z.
// The two merge groups that named the row were created after that time, but their heads, on main,
// do not hold c2a88d81 (GitHub's compare of each with it answers 2bec2c65 as the merge base), so
// both ran the old lines. Their ids, times and heads are GitHub's; the line in each notice is the
// row's line at ff963910.
test("two merge groups created after a row's own lines last changed, whose heads do not hold that change, do not name the row, and say why", () => {
  const title =
    "reference: a title written while the reference's read is in flight shows after leaving the " +
    "page and coming back";
  const change = {
    committedDate: "2026-11-20T04:41:21Z",
    lines: "refresh-freshness.e2e.ts:96-147",
    oid: "c2a88d81460fd2aad180969d9a04dc1515076732",
  };
  const runs: RunReading[] = [
    {
      createdAt: "2026-11-22T03:44:29Z",
      event: "merge_group",
      flakyRows: [`refresh-freshness.e2e.ts:96 [chromium] ${title}`],
      headSha: "2cdf3b82dbd479d6ab47492eab168339b9cd7ea4",
      id: 44981988875,
      unread: 0,
      url: "https://github.com/cybermonklabs/legion/actions/runs/44981988875",
    },
    {
      createdAt: "2026-11-22T14:51:48Z",
      event: "merge_group",
      flakyRows: [`refresh-freshness.e2e.ts:96 [chromium] ${title}`],
      headSha: "bdca57750421d2a68cafebf29bcdbb7cd3d107f9",
      id: 45035113291,
      unread: 0,
      url: "https://github.com/cybermonklabs/legion/actions/runs/45035113291",
    },
  ];
  const at = new Date("2026-11-22T16:43:50Z");
  const decision = decide(
    runs,
    [],
    [],
    new Map([[`refresh-freshness.e2e.ts [chromium] ${title}`, change]]),
    at,
    new Map(runs.map((run) => [pairKey(change.oid, run.headSha), false]))
  );
  expect(decision.toQuarantine).toEqual([]);
  expect(decision.leftOut.map(({ missing, run, why }) => [run.id, missing, why])).toEqual([
    [
      44981988875,
      "change",
      "its head 2cdf3b82dbd479d6ab47492eab168339b9cd7ea4 does not hold its own lines' last " +
        "change, c2a88d81460fd2aad180969d9a04dc1515076732 at 2026-11-20T04:41:21Z",
    ],
    [
      45035113291,
      "change",
      "its head bdca57750421d2a68cafebf29bcdbb7cd3d107f9 does not hold its own lines' last " +
        "change, c2a88d81460fd2aad180969d9a04dc1515076732 at 2026-11-20T04:41:21Z",
    ],
  ]);
  const { failed, summary } = report(decision, at);
  expect(failed).toBe(false);
  expect(summary).toContain(
    `- \`refresh-freshness.e2e.ts [chromium] ${title}\` in ` +
      "[run 45035113291](https://github.com/cybermonklabs/legion/actions/runs/45035113291) of " +
      "2026-11-22 (merge_group): its head bdca57750421d2a68cafebf29bcdbb7cd3d107f9 does not " +
      "hold its own lines' last change, c2a88d81460fd2aad180969d9a04dc1515076732 at " +
      "2026-11-20T04:41:21Z."
  );
});

// GitHub's compare of run 101's head with the row's last change answered nothing, so whether the
// run ran the row's current lines cannot be told.
test("a run whose head cannot be placed against its row's last change is counted, and the summary names it with the reason", () => {
  const reason =
    "GitHub has no compare of the two (a 404 or a 422), so one of them is a commit it does not have";
  const holds = held(stillRows, [], { [pairKey("1".repeat(40), head(101))]: reason });
  const decision = decide(history, [], [], stillRows, readAt, holds);
  expect(decision.toQuarantine.map(({ runs }) => runs.map((run) => run.id))).toEqual([[101, 102]]);
  const because =
    `counted, though it cannot be told whether its head ${head(101)} holds its own lines' last ` +
    `change, ${"1".repeat(40)} at 2026-10-01T00:00:00Z: ${reason}`;
  const { summary } = report(decision, readAt);
  const section = summary.indexOf("#### Flakes counted from runs that could not be placed");
  expect(summary.slice(section, section + 4)).toEqual([
    "#### Flakes counted from runs that could not be placed",
    "",
    "- `pictures.e2e.ts [chromium] a picture uploads @pr` in " +
      "[run 101](https://github.com/cybermonklabs/legion/actions/runs/101) of 2026-11-08 " +
      `(schedule): ${because}.`,
    "",
  ]);
  expect(decision.unplaced.map(({ run, why }) => [run.id, why])).toEqual([[101, because]]);
});

// Run 101's head does not hold the row's last change, and run 102's does not hold its fix.
test("the summary's count of the runs read says how many flakes it set aside for a head without the row's last change, and for one without its fix", () => {
  const fixes = [{ committedDate: "2026-11-06T09:00:00Z", entry: picturesFix }];
  const holds = held(stillRows, fixes, {
    [pairKey("1".repeat(40), head(101))]: false,
    [pairKey("5".repeat(40), head(102))]: false,
  });
  const { summary } = report(decide(history, [], fixes, stillRows, readAt, holds), readAt);
  expect(summary[2]).toBe(
    "Read the merge jobs of 4 runs of the 14 days before 2026-11-19T02:17:00.000Z: 1 schedule runs of dispatch-browser-nightly.yaml and 3 merge_group runs of ci.yaml. 3 of them named a flaky row. Flakes set aside because the run's head did not hold the row's last change: 1; because it did not hold the row's fix: 1. A pull request's runs are not read: its head can carry its own defect."
  );
});

test("a fix whose commit is not on main fails the check", () => {
  const fixReadings = [{ committedDate: undefined, entry: picturesFix }];
  const decision = decide(
    history,
    [picturesEntry],
    fixReadings,
    stillRows,
    readAt,
    held(stillRows, fixReadings)
  );
  expect(decision.offMain).toEqual([picturesFix]);
  const { annotations, failed } = report(decision, readAt);
  expect(failed).toBe(true);
  expect(annotations).toEqual([
    "::error title=dispatch-browser quarantine::the fix of pictures.e2e.ts [chromium] a picture " +
      `uploads @pr, ${"5".repeat(40)}, is not on main`,
  ]);
});

test("a fix older than fourteen days is named for its owner to delete, and fails nothing", () => {
  const fixReadings = [{ committedDate: "2026-10-20T09:00:00Z", entry: picturesFix }];
  const decision = decide(
    history,
    [picturesEntry],
    fixReadings,
    stillRows,
    readAt,
    held(stillRows, fixReadings)
  );
  expect(decision.spent).toEqual([{ committedDate: "2026-10-20T09:00:00Z", entry: picturesFix }]);
  const { failed, summary } = report(decision, readAt);
  expect(failed).toBe(false);
  expect(summary).toContain('      fixed: "5555555555555555555555555555555555555555",');
});

test("an entry held longer than fourteen days is named with its issue and the lines to remove", () => {
  const decision = decide(
    history,
    [picturesEntry, fourteenDays, eighteenDays],
    [],
    stillRows,
    readAt,
    held(stillRows)
  );
  expect(decision.overdue).toEqual([{ days: 18, entry: eighteenDays }]);
  const { annotations, failed, summary } = report(decision, readAt);
  expect(failed).toBe(true);
  const lines = summary.slice(summary.indexOf("  ```ts") + 1, summary.indexOf("  ```ts") + 8);
  expect(lines).toEqual([
    "    {",
    '      file: "offline-edits.e2e.ts",',
    '      project: "firefox",',
    '      title: "an edit is restored across a reload @firefox",',
    '      issue: "dispatch://INFRA-2003",',
    '      added: "2026-11-01",',
    "    },",
  ]);
  expect(annotations).toEqual([
    "::error title=dispatch-browser quarantine::offline-edits.e2e.ts [firefox] an edit is " +
      "restored across a reload @firefox has been quarantined 18 days, more than 14; its issue " +
      "is dispatch://INFRA-2003",
  ]);
});

test("flaky rows a run counted and no row notice named are reported as unread", () => {
  const short = history.map((run) => (run.id === 106 ? { ...run, unread: 3 } : run));
  const { annotations } = report(
    decide(short, [picturesEntry], [], stillRows, readAt, held(stillRows)),
    readAt
  );
  expect(annotations).toEqual([
    "::warning title=dispatch-browser quarantine::run 106 counts 3 flaky rows that no row " +
      "notice names, and they are not counted",
  ]);
});

// Biome breaks a property after its key where the line would pass 100 columns, so a long title
// is two lines of e2e/quarantine.ts. The lines `report` names to add or to remove are the lines
// the file holds, which `e2e/__tests__/quarantine.test.ts` checks of every real entry.
test("an entry's lines are the layout Biome keeps, a long title on the line after its key", () => {
  const title =
    "a person's Close withdraws the issue's asks, an earlier version keeps its open block, and " +
    "Reopen brings the asks back @pr";
  expect(entryLines({ ...picturesEntry, title })).toEqual([
    "  {",
    '    file: "pictures.e2e.ts",',
    '    project: "chromium",',
    "    title:",
    `      "${title}",`,
    '    issue: "dispatch://INFRA-2001",',
    '    added: "2026-11-12",',
    "  },",
  ]);
});

// GitHub answers a 404 or a 422 with no body, which the client reads as no answer at all
// (`undefined`), and the refusal must still name the file and what was answered.
test("a blame GitHub gives no answer for is refused, naming the file", async () => {
  await expect(
    blame({ graphql: async () => undefined }, "cybermonklabs/legion", "main", "e2e/pictures.e2e.ts")
  ).rejects.toThrow(
    "GitHub answered no blame of e2e/pictures.e2e.ts at main (null), so the rows of that file " +
      "cannot be read as they stand: the quarantine was not checked."
  );
});

/** A notice of a check run, as GraphQL's `CheckAnnotation` gives it. */
interface Note {
  readonly title: string | null;
  readonly message: string;
}

/** One run as the stand-in answers for it: the fields of the run listing the reading reads, and
 *  its merge job's check runs as GraphQL's `CheckSuite.checkRuns` gives them. */
interface StoodRun {
  readonly workflow: string;
  readonly run: {
    readonly id: number;
    readonly event: string;
    readonly created_at: string;
    readonly head_sha: string;
    readonly html_url: string;
    readonly check_suite_node_id: string;
  };
  readonly checkRuns: readonly { readonly id: string; readonly annotations: readonly Note[] }[];
}

/** An answer the stand-in gives in place of the one asked for. */
interface Refusal {
  readonly status: number;
  readonly headers?: Record<string, string>;
  readonly body: unknown;
}

/** A stand-in for GitHub's API on a local port, answering from `runs` and `compares`:
 *
 *  - `GET /repos/<repo>/actions/workflows/<workflow>/runs?event=<e>&created=<from>..<to>`: the
 *    runs of that workflow and event created in that range, both ends in it;
 *  - `GET /repos/<repo>/compare/<range>`: the status and body `compares` holds for that range,
 *    and a 404 for a range it does not hold;
 *  - `POST /graphql`: told apart by its variables - `ids` the suites of one call, `id` with
 *    `checkName` a suite's further check runs, `id` alone a check run's further notices, neither
 *    only `rateLimit` - a suite answering the check runs of its run's merge job alone, paged at
 *    `checkRunsPerPage` and `annotationsPerPage` with the offset as the cursor, each answer
 *    costing 1 point of 4,999 left.
 *
 *  Every request is recorded in `seen` with the moment it arrived, and the first requests of
 *  each kind are answered from `refuse.rest` and `refuse.graphql`, in turn, where those hold any. */
function gitHubStandIn(
  runs: readonly StoodRun[],
  refuse: { rest?: Refusal[]; graphql?: Refusal[] } = {},
  compares: Readonly<Record<string, { readonly status: number; readonly body: unknown }>> = {}
) {
  const seen: { at: number; path: string; variables?: Record<string, unknown> }[] = [];
  const mergeJob = (workflow: string) =>
    countedRuns.find((counted) => counted.workflow === workflow)?.mergeJob;
  const page = <Node>(nodes: readonly Node[], from: number, size: number) => ({
    nodes: nodes.slice(from, from + size),
    pageInfo: {
      endCursor: String(Math.min(from + size, nodes.length)),
      hasNextPage: from + size < nodes.length,
    },
  });
  const checkRunsOf = (run: StoodRun, checkName: unknown) =>
    (mergeJob(run.workflow) === checkName ? run.checkRuns : []).map((checkRun) => ({
      annotations: page(checkRun.annotations, 0, annotationsPerPage),
      id: checkRun.id,
    }));
  const rateLimit = { cost: 1, remaining: 4999, resetAt: "2026-11-22T03:00:00Z" };
  const server = Bun.serve({
    hostname: "127.0.0.1",
    port: 0,
    async fetch(request) {
      const url = new URL(request.url);
      if (request.method === "POST" && url.pathname === "/graphql") {
        const { variables } = (await request.json()) as { variables: Record<string, unknown> };
        seen.push({ at: Date.now(), path: url.pathname, variables });
        const refusal = refuse.graphql?.shift();
        if (refusal !== undefined) {
          return Response.json(refusal.body, { headers: refusal.headers, status: refusal.status });
        }
        const after = Number(variables.after ?? 0);
        if (Array.isArray(variables.ids)) {
          const nodes = variables.ids.map((id) => {
            const run = runs.find((stood) => stood.run.check_suite_node_id === id);
            return run === undefined
              ? null
              : { checkRuns: page(checkRunsOf(run, variables.checkName), 0, checkRunsPerPage), id };
          });
          return Response.json({ data: { nodes, rateLimit } });
        }
        if (typeof variables.id === "string" && typeof variables.checkName === "string") {
          const run = runs.find((stood) => stood.run.check_suite_node_id === variables.id);
          const checkRuns = run === undefined ? [] : checkRunsOf(run, variables.checkName);
          return Response.json({
            data: {
              node: { checkRuns: page(checkRuns, after, checkRunsPerPage), id: variables.id },
              rateLimit,
            },
          });
        }
        if (typeof variables.id === "string") {
          const checkRun = runs
            .flatMap((stood) => stood.checkRuns)
            .find((stoodCheckRun) => stoodCheckRun.id === variables.id);
          return Response.json({
            data: {
              node: {
                annotations: page(checkRun?.annotations ?? [], after, annotationsPerPage),
                id: variables.id,
              },
              rateLimit,
            },
          });
        }
        return Response.json({ data: { rateLimit } });
      }
      seen.push({ at: Date.now(), path: `${url.pathname}${url.search}` });
      const refusal = refuse.rest?.shift();
      if (refusal !== undefined) {
        return Response.json(refusal.body, { headers: refusal.headers, status: refusal.status });
      }
      const listing = /^\/repos\/[^/]+\/[^/]+\/actions\/workflows\/([^/]+)\/runs$/.exec(
        url.pathname
      );
      if (listing !== null) {
        const [from, to] = (url.searchParams.get("created") ?? "..").split("..").map(Date.parse);
        const listed = runs
          .filter(
            ({ run, workflow }) =>
              workflow === listing[1] &&
              run.event === url.searchParams.get("event") &&
              Date.parse(run.created_at) >= from &&
              Date.parse(run.created_at) <= to
          )
          .map(({ run }) => run);
        return Response.json({ total_count: listed.length, workflow_runs: listed });
      }
      const compared = /^\/repos\/[^/]+\/[^/]+\/compare\/([^/]+)$/.exec(url.pathname);
      const answer = compared === null ? undefined : compares[compared[1]];
      if (answer !== undefined) return Response.json(answer.body, { status: answer.status });
      return Response.json({ message: "Not Found" }, { status: 404 });
    },
  });
  return { seen, stop: () => server.stop(true), url: `http://127.0.0.1:${server.port}` };
}

/** Run `id` of `workflow`, created at `created`, whose merge job left one check run for each list
 *  of `checkRuns`, holding those notices. */
function stoodRun(
  workflow: string,
  id: number,
  created: Date,
  checkRuns: readonly (readonly Note[])[]
): StoodRun {
  return {
    checkRuns: checkRuns.map((annotations, index) => ({ annotations, id: `CR_${id}_${index}` })),
    run: {
      check_suite_node_id: `CS_${id}`,
      created_at: created.toISOString().replace(/\.\d{3}Z$/u, "Z"),
      event: countedRuns.find((counted) => counted.workflow === workflow)?.event ?? "",
      head_sha: id.toString(16).padStart(40, "0"),
      html_url: `https://github.com/cybermonklabs/legion/actions/runs/${id}`,
      id,
    },
    workflow,
  };
}

const repository = "cybermonklabs/legion";
const flakyRow = (message: string): Note => ({ message, title: "dispatch-browser flaky row" });

/** The answers GitHub gave on 2026-11-22 near 03:05Z, to the pod's token, for eleven runs of the
 *  fourteen days before `recordedAt`: seven merge groups whose merge job named a flaky row, one
 *  of them two, two of the nightly's scheduled runs that named none, and two merge groups whose
 *  merge job left no check run. Each run holds the run listing's fields the reading reads, and its
 *  merge job's check runs and their notices as GraphQL's `nodes(ids:)` answered them, trimmed to
 *  the fields the reading reads. Each run's `head_sha`, which the reading reads too now, was read
 *  later on 2026-11-22 from that run's page of the REST API: a run's head does not change. */
const answers = JSON.parse(
  readFileSync(new URL("./fixtures/quarantine-answers.fixture.json", import.meta.url), "utf8")
) as { runs: StoodRun[] };
const recordedAt = new Date("2026-11-22T02:17:00Z");

/** What `readHistory` reads of `answers` at `recordedAt`: the nightly's runs first, then the
 *  merge groups, each workflow's a day at a time, oldest day first. */
const recordedReadings: RunReading[] = [
  {
    createdAt: "2026-11-08T09:00:31Z",
    event: "schedule",
    flakyRows: [],
    headSha: "7c7e6e9ba158eb4496b20c3fcdadf70f66abae43",
    id: 42465305880,
    unread: 0,
    url: "https://github.com/cybermonklabs/legion/actions/runs/42465305880",
  },
  {
    createdAt: "2026-11-10T20:36:01Z",
    event: "schedule",
    flakyRows: [],
    headSha: "f15619830b5a560c677c2470dd12f5abbd362088",
    id: 42896887674,
    unread: 0,
    url: "https://github.com/cybermonklabs/legion/actions/runs/42896887674",
  },
  {
    createdAt: "2026-11-08T04:18:13Z",
    event: "merge_group",
    flakyRows: [
      "close-withdraws-asks.e2e.ts:153 [chromium] a person's Close withdraws the issue's asks, an earlier version keeps its open block, and Reopen brings the asks back",
    ],
    headSha: "dc24fe189dba3a1dda7aab28673db0edc5ba335f",
    id: 42446257887,
    unread: 0,
    url: "https://github.com/cybermonklabs/legion/actions/runs/42446257887",
  },
  {
    createdAt: "2026-11-08T09:45:24Z",
    event: "merge_group",
    flakyRows: [],
    headSha: "c23e213ad7a94a71e9c963cfc99187497460aee7",
    id: 42468519197,
    unread: 0,
    url: "https://github.com/cybermonklabs/legion/actions/runs/42468519197",
  },
  {
    createdAt: "2026-11-08T22:52:45Z",
    event: "merge_group",
    flakyRows: [],
    headSha: "667a74db44fca8f2f405ced10505284b653bf9ef",
    id: 42532405516,
    unread: 0,
    url: "https://github.com/cybermonklabs/legion/actions/runs/42532405516",
  },
  {
    createdAt: "2026-11-15T10:33:40Z",
    event: "merge_group",
    flakyRows: [
      "agent-live-views.e2e.ts:251 [iphone] the rail reads live, live 30s and asleep for the views the person has open @phone",
    ],
    headSha: "bfa772d9c8ee945da6382bd34e7e25a96a8052a0",
    id: 43704669128,
    unread: 0,
    url: "https://github.com/cybermonklabs/legion/actions/runs/43704669128",
  },
  {
    createdAt: "2026-11-16T05:39:23Z",
    event: "merge_group",
    flakyRows: [
      "agent-rail.e2e.ts:184 [iphone] the rail row, the Agents row and the live view each unpin @phone",
      "agent-rail.e2e.ts:152 [iphone] a pin from the Agents page and a pin from a live view are both under the heading @phone",
    ],
    headSha: "a45e5b588d0ec9ff16de5bddd1123a65135fa685",
    id: 43811655596,
    unread: 0,
    url: "https://github.com/cybermonklabs/legion/actions/runs/43811655596",
  },
  {
    createdAt: "2026-11-19T04:08:54Z",
    event: "merge_group",
    flakyRows: [
      "threads.e2e.ts:237 [chromium] resolve, reopen, then edit leaves one comment turn in its final state",
    ],
    headSha: "5076299e506d64981a56bfad5dcfe42f3f567bdf",
    id: 44456291758,
    unread: 0,
    url: "https://github.com/cybermonklabs/legion/actions/runs/44456291758",
  },
  {
    createdAt: "2026-11-21T17:22:25Z",
    event: "merge_group",
    flakyRows: [
      "board.e2e.ts:1184 [chromium] picker keeps a pick on screen: a card dragged to another priority lane while its pick is out stays in that lane",
    ],
    headSha: "de5e0974062c5412453919c3895f254f3dfa1be5",
    id: 44929688393,
    unread: 0,
    url: "https://github.com/cybermonklabs/legion/actions/runs/44929688393",
  },
  {
    createdAt: "2026-11-22T01:59:10Z",
    event: "merge_group",
    flakyRows: [
      "delivery-live.e2e.ts:484 [webkit-iphone] on a phone the delivery page shows a merge without a reload",
    ],
    headSha: "30329f7a459fea33d910e54149b7a44237f5fff6",
    id: 44973945438,
    unread: 0,
    url: "https://github.com/cybermonklabs/legion/actions/runs/44973945438",
  },
  {
    createdAt: "2026-11-22T01:24:38Z",
    event: "merge_group",
    flakyRows: [
      "issue-page-p.e2e.ts:3120 [chromium] a change request sent from the header leaves the focus in the header",
    ],
    headSha: "b51ed995546544a42280a5e370cc04bdb882a993",
    id: 44971330353,
    unread: 0,
    url: "https://github.com/cybermonklabs/legion/actions/runs/44971330353",
  },
];

// No entries, no fixes and no changes: no row of these runs is flaky in two of them, so nothing
// is named, and every row read is still counted, each by the one run that named it.
test("the recorded answers read as these eleven runs, and decide and report nothing to quarantine", async () => {
  const server = gitHubStandIn(answers.runs);
  try {
    const readings = await readHistory(gitHub(server.url, "t"), repository, recordedAt);
    expect(readings).toEqual(recordedReadings);
    const decision = decide(readings, [], [], new Map(), recordedAt, new Map());
    expect(decision.counted).toEqual(recordedReadings);
    expect([...decision.rows].map(([name, { runs }]) => [name, runs.map((run) => run.id)])).toEqual(
      [
        [
          "close-withdraws-asks.e2e.ts [chromium] a person's Close withdraws the issue's asks, an earlier version keeps its open block, and Reopen brings the asks back",
          [42446257887],
        ],
        [
          "agent-live-views.e2e.ts [iphone] the rail reads live, live 30s and asleep for the views the person has open @phone",
          [43704669128],
        ],
        [
          "agent-rail.e2e.ts [iphone] the rail row, the Agents row and the live view each unpin @phone",
          [43811655596],
        ],
        [
          "agent-rail.e2e.ts [iphone] a pin from the Agents page and a pin from a live view are both under the heading @phone",
          [43811655596],
        ],
        [
          "threads.e2e.ts [chromium] resolve, reopen, then edit leaves one comment turn in its final state",
          [44456291758],
        ],
        [
          "board.e2e.ts [chromium] picker keeps a pick on screen: a card dragged to another priority lane while its pick is out stays in that lane",
          [44929688393],
        ],
        [
          "delivery-live.e2e.ts [webkit-iphone] on a phone the delivery page shows a merge without a reload",
          [44973945438],
        ],
        [
          "issue-page-p.e2e.ts [chromium] a change request sent from the header leaves the focus in the header",
          [44971330353],
        ],
      ]
    );
    const { absent, leftOut, offMain, overdue, spent, toQuarantine, unparsed, unplaced } = decision;
    expect({ absent, leftOut, offMain, overdue, spent, toQuarantine, unparsed, unplaced }).toEqual({
      absent: [],
      leftOut: [],
      offMain: [],
      overdue: [],
      spent: [],
      toQuarantine: [],
      unparsed: [],
      unplaced: [],
    });
    expect(report(decision, recordedAt)).toEqual({
      annotations: [],
      failed: false,
      summary: [
        "### The quarantine on 2026-11-22",
        "",
        "Read the merge jobs of 11 runs of the 14 days before 2026-11-22T02:17:00.000Z: 2 schedule runs of dispatch-browser-nightly.yaml and 9 merge_group runs of ci.yaml. 7 of them named a flaky row. Flakes set aside because the run's head did not hold the row's last change: 0; because it did not hold the row's fix: 0. A pull request's runs are not read: its head can carry its own defect.",
        "",
        "#### Flaky in 2 or more runs, and not quarantined",
        "",
        "No row the quarantine does not hold was flaky in 2 runs whose heads hold its last change.",
        "",
        "#### Held longer than 14 days",
        "",
        "No entry has been held longer than 14 days.",
      ],
    });
  } finally {
    server.stop();
  }
});

// 253 runs, a few a day: 3 of the nightly and 250 merge groups, every other one with a flaky row.
test("a reading costs the listings and a GraphQL call per hundred runs of a workflow, not a call a run", async () => {
  const runs = Array.from({ length: 253 }, (_, index) =>
    stoodRun(
      index < 3 ? "dispatch-browser-nightly.yaml" : "ci.yaml",
      1000 + index,
      new Date(recordedAt.getTime() - ((index % windowDays) + 0.5) * 86_400_000),
      [index % 2 === 0 ? [flakyRow(`pictures.e2e.ts:${index} [chromium] row ${index}`)] : []]
    )
  );
  const server = gitHubStandIn(runs);
  try {
    const github = gitHub(server.url, "t");
    const readings = await readHistory(github, repository, recordedAt);
    expect(readings).toHaveLength(253);
    expect(github.spent()).toEqual({
      calls: 2 * windowDays,
      graphqlCalls: Math.ceil(3 / suitesPerCall) + Math.ceil(250 / suitesPerCall),
      graphqlCost: 4,
      graphqlRemaining: 4999,
      remaining: null,
    });
    const batches = server.seen
      .filter((request) => request.path === "/graphql")
      .map((request) => (request.variables?.ids as unknown[]).length);
    expect(batches.sort((a, b) => a - b)).toEqual([3, 50, 100, 100]);
  } finally {
    server.stop();
  }
});

test("a suite's check runs past one page, and a check run's notices past one, are read to the last", async () => {
  const checkRuns: Note[][] = Array.from({ length: checkRunsPerPage + 1 }, () => []);
  checkRuns[0] = [flakyRow("a.e2e.ts:1 [chromium] on the first page")];
  checkRuns[checkRunsPerPage] = [
    ...Array.from({ length: annotationsPerPage }, (_, index) => ({
      message: `row ${index} took 1 s`,
      title: "dispatch-browser row durations",
    })),
    flakyRow("b.e2e.ts:2 [webkit] on the second page of both"),
    { message: "3 flaky rows of 1300, 0.23 per cent", title: "dispatch-browser flake rate" },
  ];
  const server = gitHubStandIn([
    stoodRun("ci.yaml", 7, new Date(recordedAt.getTime() - 86_400_000), checkRuns),
  ]);
  try {
    const github = gitHub(server.url, "t");
    const readings = await readHistory(github, repository, recordedAt);
    expect(readings.map(({ flakyRows, unread }) => ({ flakyRows, unread }))).toEqual([
      {
        flakyRows: [
          "a.e2e.ts:1 [chromium] on the first page",
          "b.e2e.ts:2 [webkit] on the second page of both",
        ],
        unread: 2,
      },
    ]);
    expect(
      server.seen
        .filter((request) => request.path === "/graphql")
        .map((request) => request.variables?.after ?? null)
    ).toEqual([null, String(checkRunsPerPage), String(annotationsPerPage)]);
    expect(github.spent().graphqlCalls).toBe(3);
  } finally {
    server.stop();
  }
});

// Rows a and b of one file are each flaky in runs 1 and 2, and row a in run 3 too. The last
// change of both rows' lines is one commit, `change`, and row a's fix is `fix`. Row c is flaky in
// run 3 alone, so it is no candidate and its change is never compared. GitHub answers the pairs
// each its own way: the commit as the merge base, another commit as it, no compare at all (404),
// an answer that names no merge base, and a refusal.
test("readHolds compares each commit of a candidate row with each head once, and says why where a compare cannot tell", async () => {
  const change = "c".repeat(40);
  const fix = "d".repeat(40);
  const heads = ["1", "2", "3"].map((digit) => digit.repeat(40));
  const rowA = "a.e2e.ts:1 [chromium] row a";
  const rowC = "c.e2e.ts:1 [chromium] row c";
  const runs = [
    [rowA, "a.e2e.ts:9 [chromium] row b"],
    [rowA, "a.e2e.ts:9 [chromium] row b"],
    [rowA, rowC],
  ].map(
    (flakyRows, index): RunReading => ({
      createdAt: new Date(recordedAt.getTime() - (index + 1) * 3_600_000).toISOString(),
      event: "merge_group",
      flakyRows,
      headSha: heads[index],
      id: index + 1,
      unread: 0,
      url: `https://github.com/cybermonklabs/legion/actions/runs/${index + 1}`,
    })
  );
  const changes = new Map<string, RowChange>([
    [
      "a.e2e.ts [chromium] row a",
      { committedDate: "2026-11-01T00:00:00Z", lines: "a.e2e.ts:1-8", oid: change },
    ],
    [
      "a.e2e.ts [chromium] row b",
      { committedDate: "2026-11-01T00:00:00Z", lines: "a.e2e.ts:9-20", oid: change },
    ],
    [
      "c.e2e.ts [chromium] row c",
      { committedDate: "2026-11-01T00:00:00Z", lines: "c.e2e.ts:1-20", oid: "e".repeat(40) },
    ],
  ]);
  const fixes = [
    {
      file: "a.e2e.ts",
      project: "chromium",
      title: "row a",
      issue: "dispatch://INFRA-2001",
      fixed: fix,
    },
  ];
  const server = gitHubStandIn(
    [],
    {},
    {
      [pairKey(change, heads[0])]: { body: { merge_base_commit: { sha: change } }, status: 200 },
      [pairKey(change, heads[1])]: {
        body: { merge_base_commit: { sha: "0".repeat(40) } },
        status: 200,
      },
      [pairKey(fix, heads[0])]: { body: {}, status: 200 },
      [pairKey(fix, heads[1])]: {
        body: { message: "Resource not accessible by integration" },
        status: 403,
      },
      [pairKey(fix, heads[2])]: { body: { merge_base_commit: { sha: fix } }, status: 200 },
    }
  );
  try {
    const github = gitHub(server.url, "t");
    const holds = await readHolds(github, repository, runs, changes, fixes, recordedAt);
    expect(Object.fromEntries(holds)).toEqual({
      [pairKey(change, heads[0])]: true,
      [pairKey(change, heads[1])]: false,
      [pairKey(change, heads[2])]:
        "GitHub has no compare of the two (a 404 or a 422), so one of them is a commit it does " +
        "not have",
      [pairKey(fix, heads[0])]: "GitHub's compare of the two names no merge base",
      [pairKey(fix, heads[1])]:
        `GitHub refused ${server.url}/repos/${repository}/compare/${pairKey(fix, heads[1])} ` +
        'with HTTP 403: {"message":"Resource not accessible by integration"}, which names no ' +
        "time it may be asked again (no retry-after, and x-ratelimit-remaining not 0), so it was " +
        "not asked again",
      [pairKey(fix, heads[2])]: true,
    });
    expect(server.seen.map((request) => request.path).sort()).toEqual(
      [change, fix]
        .flatMap((commit) =>
          heads.map((head) => `/repos/${repository}/compare/${pairKey(commit, head)}`)
        )
        .sort()
    );
    // A day whose rows are each flaky in one run has no candidate, and compares nothing.
    const quiet = [runs[0], { ...runs[2], flakyRows: [rowC] }];
    expect(await readHolds(github, repository, quiet, changes, fixes, recordedAt)).toEqual(
      new Map()
    );
    expect(server.seen).toHaveLength(6);
  } finally {
    server.stop();
  }
});

const listingPath =
  "/repos/o/r/actions/workflows/ci.yaml/runs?event=merge_group" +
  "&created=2026-11-21T00:00:00Z..2026-11-22T00:00:00Z";
const emptyListing = { total_count: 0, workflow_runs: [] };

/** A step's limit five minutes off, which every wait below fits in with its 30 s margin. */
const fiveMinutesOff = () => ({ at: Date.now() + 300_000, of: "step" }) as const;

test("GitHub's rate limit, its reset 2 s ahead, is waited out, and the call then answers", async () => {
  const reset = Math.floor(Date.now() / 1000) + 2;
  const server = gitHubStandIn([], {
    rest: [
      {
        body: { message: "API rate limit exceeded for installation." },
        headers: {
          "x-ratelimit-remaining": "0",
          "x-ratelimit-reset": String(reset),
          "x-ratelimit-resource": "core",
        },
        status: 403,
      },
    ],
  });
  const lines: string[] = [];
  const deadline = fiveMinutesOff();
  try {
    const github = gitHub(server.url, "t", { deadline, log: (line) => lines.push(line) });
    expect(await github.one(listingPath)).toEqual(emptyListing);
    expect(server.seen).toHaveLength(2);
    expect(server.seen[1].at).toBeGreaterThanOrEqual(reset * 1000);
    expect(lines).toEqual([
      `quarantine-check: GitHub refused ${server.url}${listingPath} with HTTP 403, so it waits ` +
        `until ${new Date((reset + 3) * 1000).toISOString()}, by its x-ratelimit-reset: ${reset} ` +
        "(x-ratelimit-remaining: 0, x-ratelimit-resource: core), plus 3 s, within this step's " +
        `limit at ${new Date(deadline.at).toISOString()}, and asks again.`,
    ]);
  } finally {
    server.stop();
  }
});

// GraphQL's primary limit answers 200, with a RATE_LIMITED error and the reset in its headers.
test("GraphQL's own rate limit, its RATE_LIMITED answer, is waited out the same way", async () => {
  const reset = Math.floor(Date.now() / 1000) + 1;
  const server = gitHubStandIn([], {
    graphql: [
      {
        body: { errors: [{ message: "API rate limit exceeded", type: "RATE_LIMITED" }] },
        headers: {
          "x-ratelimit-remaining": "0",
          "x-ratelimit-reset": String(reset),
          "x-ratelimit-resource": "graphql",
        },
        status: 200,
      },
    ],
  });
  const lines: string[] = [];
  try {
    const github = gitHub(server.url, "t", {
      deadline: fiveMinutesOff(),
      log: (line) => lines.push(line),
    });
    const answer = await github.graphql("query { rateLimit { cost remaining resetAt } }", {});
    expect(answer).toEqual({
      data: { rateLimit: { cost: 1, remaining: 4999, resetAt: "2026-11-22T03:00:00Z" } },
    });
    expect(server.seen[1].at).toBeGreaterThanOrEqual(reset * 1000);
    expect(lines).toHaveLength(1);
    expect(lines[0]).toContain(
      "with GraphQL's RATE_LIMITED error, so it waits until " +
        `${new Date((reset + 3) * 1000).toISOString()}, by its x-ratelimit-reset: ${reset} ` +
        "(x-ratelimit-remaining: 0, x-ratelimit-resource: graphql), plus 3 s"
    );
    expect(github.spent()).toMatchObject({ graphqlCalls: 2, graphqlCost: 1 });
  } finally {
    server.stop();
  }
});

test("a secondary rate limit's retry-after is waited out", async () => {
  const server = gitHubStandIn([], {
    graphql: [
      {
        body: { message: "You have exceeded a secondary rate limit." },
        headers: { "retry-after": "1" },
        status: 403,
      },
    ],
  });
  const lines: string[] = [];
  try {
    const github = gitHub(server.url, "t", {
      deadline: fiveMinutesOff(),
      log: (line) => lines.push(line),
    });
    await github.graphql("query { rateLimit { cost remaining resetAt } }", {});
    expect(server.seen[1].at - server.seen[0].at).toBeGreaterThanOrEqual(1000);
    expect(lines).toHaveLength(1);
    expect(lines[0]).toContain("with HTTP 403, so it waits until ");
    expect(lines[0]).toContain(", by its retry-after: 1, within this step's limit at ");
    expect(lines[0]).toEndWith(", and asks again.");
  } finally {
    server.stop();
  }
});

/** A REST answer refusing the call for its spent budget until `reset`, with no resource named. */
const spentUntil = (reset: number): Refusal => ({
  body: { message: "API rate limit exceeded for installation." },
  headers: { "x-ratelimit-remaining": "0", "x-ratelimit-reset": String(reset) },
  status: 403,
});

test("GitHub's rate limit whose reset falls past the step's limit fails at once, naming both", async () => {
  const reset = Math.floor(Date.now() / 1000) + 600;
  const server = gitHubStandIn([], { rest: [spentUntil(reset)] });
  const lines: string[] = [];
  const deadline = { at: Date.now() + 60_000, of: "step" } as const;
  try {
    const github = gitHub(server.url, "t", { deadline, log: (line) => lines.push(line) });
    const started = Date.now();
    await expect(github.one(listingPath)).rejects.toThrow(
      `GitHub refused ${server.url}${listingPath} with HTTP 403: ` +
        '{"message":"API rate limit exceeded for installation."}, and the time its rate limit ' +
        `names, ${new Date((reset + 3) * 1000).toISOString()} by its x-ratelimit-reset: ${reset} ` +
        `(x-ratelimit-remaining: 0), plus 3 s, falls past this step's limit at ` +
        `${new Date(deadline.at).toISOString()} less the 30 s the call after the wait needs, so ` +
        "it was not waited for. This is a failed read of the runs and not a finding: the " +
        "quarantine was not checked."
    );
    // At once: the ladder's first wait for a refusal it asks again is 10 s.
    expect(Date.now() - started).toBeLessThan(9000);
    expect(server.seen).toHaveLength(1);
    expect(lines).toEqual([]);
  } finally {
    server.stop();
  }
});

// gh-read.sh's GH_WAIT_MARGIN: the call after the wait, and the reading's work after it, need
// time inside the limit, so a wait that would end 29 s before it is not made. Without the margin
// this row waits the 5 s and reads on.
test("a wait that would end inside the 30 s margin before the job's limit is not made", async () => {
  const reset = Math.floor(Date.now() / 1000) + 2;
  const server = gitHubStandIn([], { rest: [spentUntil(reset)] });
  const deadline = { at: (reset + 3 + 29) * 1000, of: "job" } as const;
  try {
    const github = gitHub(server.url, "t", { deadline, log: () => {} });
    await expect(github.one(listingPath)).rejects.toThrow(
      `falls past this job's limit at ${new Date(deadline.at).toISOString()} less the 30 s the ` +
        "call after the wait needs, so it was not waited for."
    );
    expect(server.seen).toHaveLength(1);
  } finally {
    server.stop();
  }
});

test("with no limit to wait within, the wait is not made, and the refusal says why", async () => {
  const reset = Math.floor(Date.now() / 1000) + 2;
  const server = gitHubStandIn([], { rest: [spentUntil(reset)] });
  try {
    const github = gitHub(server.url, "t", { deadline: readDeadline({}), log: () => {} });
    await expect(github.one(listingPath)).rejects.toThrow(
      `by its x-ratelimit-reset: ${reset} (x-ratelimit-remaining: 0), plus 3 s, was not waited ` +
        "for: this step names no time limit to wait within (JOB_TIMEOUT_MINUTES is not set). " +
        "This is a failed read of the runs and not a finding"
    );
    expect(server.seen).toHaveLength(1);
  } finally {
    server.stop();
  }
});

// A reset whose 3 s past it have gone by names no time to wait for. The client once read it as a
// second from now and asked again once a second until its cap, so the stand-in refuses more calls
// than the ladder's three attempts: a client that spun would ask a fourth time and a fifth, 1 s
// apart. `retrySeconds: 1` makes the ladder's 10 s and 20 s 1 s and 2 s.
test("GitHub's rate limit whose reset is already past takes the ladder's three attempts, without a wait loop", async () => {
  const reset = Math.floor(Date.now() / 1000) - 60;
  const limited: Refusal = {
    body: { message: "API rate limit exceeded for installation." },
    headers: {
      "x-ratelimit-remaining": "0",
      "x-ratelimit-reset": String(reset),
      "x-ratelimit-resource": "core",
    },
    status: 403,
  };
  const server = gitHubStandIn([], { rest: Array.from({ length: 9 }, () => limited) });
  const lines: string[] = [];
  try {
    const github = gitHub(server.url, "t", {
      deadline: fiveMinutesOff(),
      log: (line) => lines.push(line),
      retrySeconds: 1,
    });
    await expect(github.one(listingPath)).rejects.toThrow(
      `GitHub refused ${server.url}${listingPath} three times, the last with HTTP 403: ` +
        '{"message":"API rate limit exceeded for installation."}. This is a failed read of the ' +
        "runs and not a finding: the quarantine was not checked."
    );
    expect(server.seen).toHaveLength(3);
    expect(github.spent()).toMatchObject({ calls: 3, graphqlCalls: 0 });
    // The ladder's own waits, 1 s and then 2 s, and no wait of the rate limit's between them.
    expect(server.seen[1].at - server.seen[0].at).toBeGreaterThanOrEqual(1000);
    expect(server.seen[2].at - server.seen[1].at).toBeGreaterThanOrEqual(2000);
    const line =
      `quarantine-check: GitHub refused ${server.url}${listingPath} with HTTP 403, and the time ` +
      `its rate limit names, ${new Date((reset + 3) * 1000).toISOString()} by its ` +
      `x-ratelimit-reset: ${reset} (x-ratelimit-remaining: 0, x-ratelimit-resource: core), plus ` +
      "3 s, has already passed, so it names no time to wait for, and the call takes the ladder's " +
      "own three attempts, 1 s and then 2 s apart.";
    expect(lines).toEqual([line, line, line]);
  } finally {
    server.stop();
  }
});

test("GraphQL's RATE_LIMITED answer whose reset is already past takes the ladder's three attempts the same way", async () => {
  const reset = Math.floor(Date.now() / 1000) - 60;
  const limited: Refusal = {
    body: { errors: [{ message: "API rate limit exceeded", type: "RATE_LIMITED" }] },
    headers: {
      "x-ratelimit-remaining": "0",
      "x-ratelimit-reset": String(reset),
      "x-ratelimit-resource": "graphql",
    },
    status: 200,
  };
  const server = gitHubStandIn([], { graphql: Array.from({ length: 9 }, () => limited) });
  const lines: string[] = [];
  try {
    const github = gitHub(server.url, "t", {
      deadline: fiveMinutesOff(),
      log: (line) => lines.push(line),
      retrySeconds: 1,
    });
    await expect(
      github.graphql("query { rateLimit { cost remaining resetAt } }", {})
    ).rejects.toThrow(
      `GitHub refused ${server.url}/graphql three times, the last with HTTP 200 with GraphQL's ` +
        "RATE_LIMITED error: API rate limit exceeded. This is a failed read of the runs and not " +
        "a finding: the quarantine was not checked."
    );
    expect(server.seen).toHaveLength(3);
    expect(github.spent()).toMatchObject({ calls: 0, graphqlCalls: 3 });
    expect(server.seen[1].at - server.seen[0].at).toBeGreaterThanOrEqual(1000);
    expect(server.seen[2].at - server.seen[1].at).toBeGreaterThanOrEqual(2000);
    const line =
      `quarantine-check: GitHub refused ${server.url}/graphql with GraphQL's RATE_LIMITED error, ` +
      `and the time its rate limit names, ${new Date((reset + 3) * 1000).toISOString()} by its ` +
      `x-ratelimit-reset: ${reset} (x-ratelimit-remaining: 0, x-ratelimit-resource: graphql), ` +
      "plus 3 s, has already passed, so it names no time to wait for, and the call takes the " +
      "ladder's own three attempts, 1 s and then 2 s apart.";
    expect(lines).toEqual([line, line, line]);
  } finally {
    server.stop();
  }
});

test("a 403 that names no rate limit fails at once, without a wait", async () => {
  const server = gitHubStandIn([], {
    rest: [{ body: { message: "Resource not accessible by integration" }, status: 403 }],
  });
  const lines: string[] = [];
  try {
    const github = gitHub(server.url, "t", {
      deadline: fiveMinutesOff(),
      log: (line) => lines.push(line),
    });
    const started = Date.now();
    await expect(github.one(listingPath)).rejects.toThrow(
      `GitHub refused ${server.url}${listingPath} with HTTP 403: ` +
        '{"message":"Resource not accessible by integration"}, which names no time it may be ' +
        "asked again (no retry-after, and x-ratelimit-remaining not 0), so it was not asked again."
    );
    expect(Date.now() - started).toBeLessThan(9000);
    expect(server.seen).toHaveLength(1);
    expect(lines).toEqual([]);
  } finally {
    server.stop();
  }
});

test("a GraphQL read GitHub refuses fails the reading, naming the refusal", async () => {
  const server = gitHubStandIn(
    [stoodRun("ci.yaml", 7, new Date(recordedAt.getTime() - 86_400_000), [[]])],
    { graphql: [{ body: { message: "Resource not accessible by integration" }, status: 403 }] }
  );
  try {
    await expect(readHistory(gitHub(server.url, "t"), repository, recordedAt)).rejects.toThrow(
      `GitHub refused ${server.url}/graphql with HTTP 403: ` +
        '{"message":"Resource not accessible by integration"}, which names no time it may be ' +
        "asked again"
    );
  } finally {
    server.stop();
  }
});

// `readDeadline` reads the job's start and the step's off the process tree, as gh-read.sh's
// `_gh_read_deadline` does: the Runner.Worker process GitHub's runner starts for each job, and
// that process's child, the step's shell. These rows read a stand-in `/proc`: the boot time, and
// each process's stat line with its start 20 fields after its comm, in clock ticks since boot.
const boot = 1_800_000_000;

/** A stand-in `/proc` holding each process of `processes`, `[pid, comm, ppid, start in ticks]`;
 *  the row removes it. */
function standInProc(processes: readonly (readonly [number, string, number, number])[]): string {
  const proc = mkdtempSync(join(tmpdir(), "quarantine-check-proc-"));
  writeFileSync(join(proc, "stat"), `cpu  1 2 3 4\nbtime ${boot}\nprocesses 900\n`);
  for (const [pid, comm, ppid, start] of processes) {
    mkdirSync(join(proc, String(pid)));
    // state, ppid, pgrp, session, tty_nr, tpgid, flags, twelve counters, start, vsize, rss
    const fields = ["S", ppid, pid, pid, 0, -1, 4194560, ...Array(12).fill(0), start, 0, 0];
    writeFileSync(join(proc, String(pid), "stat"), `${pid} (${comm}) ${fields.join(" ")}\n`);
  }
  return proc;
}

/** A step's process tree: the runner's listener, the job's Runner.Worker started 10 s after boot,
 *  the step's shell 46 s after it, and this process under that shell, with a comm that holds a
 *  space and both parentheses, as a comm may. */
const stepTree = [
  [200, "Runner.Listener", 1, 100],
  [300, "Runner.Worker", 200, 1000],
  [400, "bash", 300, 4600],
  [500, "bun (a) b", 400, 4700],
] as const;

test("the deadline is the earlier of the job's limit from its start and the step's from its own", () => {
  const proc = standInProc(stepTree);
  const at = { pid: 500, proc, ticksPerSecond: 100 };
  try {
    expect(readDeadline({ JOB_TIMEOUT_MINUTES: "6", STEP_TIMEOUT_MINUTES: "5" }, at)).toEqual({
      at: (boot + 46 + 300) * 1000,
      of: "step",
    });
    expect(readDeadline({ JOB_TIMEOUT_MINUTES: "1", STEP_TIMEOUT_MINUTES: "5" }, at)).toEqual({
      at: (boot + 10 + 60) * 1000,
      of: "job",
    });
  } finally {
    rmSync(proc, { force: true, recursive: true });
  }
});

test("one limit alone is the deadline, and a step's shell that ran this process with exec is the step", () => {
  const proc = standInProc(stepTree);
  const exec = standInProc([
    [300, "Runner.Worker", 200, 1000],
    [500, "bun", 300, 4600],
  ]);
  try {
    expect(
      readDeadline({ JOB_TIMEOUT_MINUTES: "6" }, { pid: 500, proc, ticksPerSecond: 100 })
    ).toEqual({ at: (boot + 10 + 360) * 1000, of: "job" });
    expect(
      readDeadline({ STEP_TIMEOUT_MINUTES: "5" }, { pid: 500, proc: exec, ticksPerSecond: 100 })
    ).toEqual({ at: (boot + 46 + 300) * 1000, of: "step" });
  } finally {
    rmSync(proc, { force: true, recursive: true });
    rmSync(exec, { force: true, recursive: true });
  }
});

test("no limit set, a limit that is no whole number of minutes, or no Runner.Worker is no deadline", () => {
  const proc = standInProc(stepTree);
  const bare = standInProc([
    [400, "bash", 1, 4600],
    [500, "bun", 400, 4700],
  ]);
  try {
    expect(readDeadline({}, { pid: 500, proc, ticksPerSecond: 100 })).toEqual({
      none: "this step names no time limit to wait within (JOB_TIMEOUT_MINUTES is not set)",
    });
    expect(readDeadline({ JOB_TIMEOUT_MINUTES: "ten" }, { pid: 500, proc })).toEqual({
      none: "JOB_TIMEOUT_MINUTES is 'ten', not a whole number of minutes",
    });
    expect(readDeadline({ JOB_TIMEOUT_MINUTES: "6" }, { pid: 500, proc: bare })).toEqual({
      none: "this step's start cannot be read: no Runner.Worker process is among its parents",
    });
  } finally {
    rmSync(proc, { force: true, recursive: true });
    rmSync(bare, { force: true, recursive: true });
  }
});
