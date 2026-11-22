/** The nightly's half of the quarantine, `e2e/tools/quarantine-check.ts`, driven through its pure
 *  part with the planted history of `fixtures/quarantine-history.fixture.ts`: what `decide`
 *  names, the lines `report` gives for each, and how a row's own last change and its fix move
 *  what counts, and the refusal `blame` gives an answer that holds no blame, read from a
 *  stand-in for GitHub. The moment of each of those readings is the fixture's.
 *
 *  Its reads are driven against `gitHubStandIn` below, a stand-in for GitHub's REST and GraphQL
 *  API on a local port: what `readHistory` reads of the answers GitHub gave for eleven runs
 *  (`fixtures/quarantine-answers.fixture.json`) and what `decide` and `report` say of it, what
 *  the reading costs, its pages, and the client's waits for GitHub's rate limit, which read the
 *  clock and wait a few real seconds. Nothing here reads GitHub itself. */
import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";

import { entryLines } from "../quarantine";
import {
  annotationsPerPage,
  blame,
  checkRunsPerPage,
  countedRuns,
  decide,
  type FixReading,
  gitHub,
  lastChange,
  type RowChange,
  type RunReading,
  readHistory,
  report,
  rowLines,
  suitesPerCall,
  windowDays,
} from "../tools/quarantine-check";
import {
  blames,
  eighteenDays,
  fourteenDays,
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

const named = (changes: ReadonlyMap<string, RowChange>, fixes: readonly FixReading[] = []) =>
  decide(history, [], fixes, changes, readAt).toQuarantine.map(({ row, runs }) => [
    `${row.file} ${row.title}`,
    runs.map((run) => run.id),
  ]);

test("a row flaky in two runs of the fourteen days is named, with its runs and the lines to add", () => {
  const decision = decide(history, [], [], stillRows, readAt);
  expect(decision.toQuarantine.map(({ row, runs }) => [row, runs.map((run) => run.id)])).toEqual([
    [{ file: "pictures.e2e.ts", project: "chromium", title: "a picture uploads @pr" }, [101, 102]],
  ]);
  expect(decision.overdue).toEqual([]);
  const { annotations, failed, summary } = report(decision, readAt);
  expect(failed).toBe(true);
  expect(summary).toContain(
    "- `pictures.e2e.ts [chromium] a picture uploads @pr`, flaky in 2 runs after its own lines " +
      "(pictures.e2e.ts:1-40) last changed, in 1111111111111111111111111111111111111111 at " +
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
  const decision = decide(history, [picturesEntry], [], stillRows, readAt);
  expect(decision.toQuarantine).toEqual([]);
  const { annotations, failed, summary } = report(decision, readAt);
  expect(failed).toBe(false);
  expect(annotations).toEqual([]);
  expect(summary).toContain("No entry has been held longer than 14 days.");
});

// `keyed-writes.e2e.ts`'s row is named by two attempts of run 102 and by no other run, and
// `mermaid.e2e.ts`'s by run 103 and by a nightly 18 days before the reading: each is one run.
test("a row flaky in one run is not named, however many attempts of it named the row", () => {
  const decision = decide(history, [picturesEntry], [], stillRows, readAt);
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
    decide(asMergeGroup, [picturesEntry], [], stillRows, readAt).toQuarantine.map(
      ({ row, runs }) => [`${row.file} ${row.title}`, runs.map((run) => run.id)]
    )
  ).toEqual([["deep-links.e2e.ts a held chunk lands @firefox", [103, 104]]]);
});

// The row's own lines are 10 to 30: from its line to the line before the file's next row.
test("a row flaky twice before its own lines changed is not named, and the runs left out say why", () => {
  const changes = withPicturesBlame(blames.ownLinesAfter);
  expect(named(changes)).toEqual([]);
  const decision = decide(history, [], [], changes, readAt);
  expect(decision.leftOut.map(({ run, why }) => [run.id, why])).toEqual([
    [
      101,
      `created before its own lines last changed, in ${"2".repeat(40)} at 2026-11-13T09:00:00Z`,
    ],
    [
      102,
      `created before its own lines last changed, in ${"2".repeat(40)} at 2026-11-13T09:00:00Z`,
    ],
  ]);
  expect(report(decision, readAt).summary).toContain(
    "- `pictures.e2e.ts [chromium] a picture uploads @pr` in " +
      "[run 101](https://github.com/cybermonklabs/legion/actions/runs/101) of 2026-11-08 " +
      `(schedule): created before its own lines last changed, in ${"2".repeat(40)} at ` +
      "2026-11-13T09:00:00Z."
  );
});

test("a row flaky twice after a change elsewhere in its file is named", () => {
  expect(named(withPicturesBlame(blames.elsewhereAfter))).toEqual([
    ["pictures.e2e.ts a picture uploads @pr", [101, 102]],
  ]);
});

test("a row flaky once before its own lines changed and once after is not named", () => {
  const changes = withPicturesBlame(blames.ownLinesBetween);
  expect(named(changes)).toEqual([]);
  expect(decide(history, [], [], changes, readAt).leftOut.map(({ run }) => run.id)).toEqual([101]);
});

test("a row flaky twice before its fix is not named, and flaky twice after it is", () => {
  expect(named(stillRows, [{ committedDate: "2026-11-13T09:00:00Z", entry: picturesFix }])).toEqual(
    []
  );
  expect(named(stillRows, [{ committedDate: "2026-11-06T09:00:00Z", entry: picturesFix }])).toEqual(
    [["pictures.e2e.ts a picture uploads @pr", [101, 102]]]
  );
});

test("a fix whose commit is not on main fails the check", () => {
  const decision = decide(
    history,
    [picturesEntry],
    [{ committedDate: undefined, entry: picturesFix }],
    stillRows,
    readAt
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
  const decision = decide(
    history,
    [picturesEntry],
    [{ committedDate: "2026-10-20T09:00:00Z", entry: picturesFix }],
    stillRows,
    readAt
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
    readAt
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
  const { annotations } = report(decide(short, [picturesEntry], [], stillRows, readAt), readAt);
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

/** A stand-in for GitHub's API on a local port, answering from `runs`:
 *
 *  - `GET /repos/<repo>/actions/workflows/<workflow>/runs?event=<e>&created=<from>..<to>`: the
 *    runs of that workflow and event created in that range, both ends in it;
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
  refuse: { rest?: Refusal[]; graphql?: Refusal[] } = {}
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
 *  the fields the reading reads. */
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
    id: 42465305880,
    unread: 0,
    url: "https://github.com/cybermonklabs/legion/actions/runs/42465305880",
  },
  {
    createdAt: "2026-11-10T20:36:01Z",
    event: "schedule",
    flakyRows: [],
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
    id: 42446257887,
    unread: 0,
    url: "https://github.com/cybermonklabs/legion/actions/runs/42446257887",
  },
  {
    createdAt: "2026-11-08T09:45:24Z",
    event: "merge_group",
    flakyRows: [],
    id: 42468519197,
    unread: 0,
    url: "https://github.com/cybermonklabs/legion/actions/runs/42468519197",
  },
  {
    createdAt: "2026-11-08T22:52:45Z",
    event: "merge_group",
    flakyRows: [],
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
    const decision = decide(readings, [], [], new Map(), recordedAt);
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
    const { absent, leftOut, offMain, overdue, spent, toQuarantine, unparsed } = decision;
    expect({ absent, leftOut, offMain, overdue, spent, toQuarantine, unparsed }).toEqual({
      absent: [],
      leftOut: [],
      offMain: [],
      overdue: [],
      spent: [],
      toQuarantine: [],
      unparsed: [],
    });
    expect(report(decision, recordedAt)).toEqual({
      annotations: [],
      failed: false,
      summary: [
        "### The quarantine on 2026-11-22",
        "",
        "Read the merge jobs of 11 runs of the 14 days before 2026-11-22T02:17:00.000Z: 2 schedule runs of dispatch-browser-nightly.yaml and 9 merge_group runs of ci.yaml. 7 of them named a flaky row. A pull request's runs are not read: its head can carry its own defect.",
        "",
        "#### Flaky in 2 or more runs, and not quarantined",
        "",
        "No row was flaky in 2 runs after its last change that the quarantine does not hold.",
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

const listingPath =
  "/repos/o/r/actions/workflows/ci.yaml/runs?event=merge_group" +
  "&created=2026-11-21T00:00:00Z..2026-11-22T00:00:00Z";
const emptyListing = { total_count: 0, workflow_runs: [] };

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
  try {
    const github = gitHub(server.url, "t", { log: (line) => lines.push(line), waitCapSeconds: 60 });
    expect(await github.one(listingPath)).toEqual(emptyListing);
    expect(server.seen).toHaveLength(2);
    expect(server.seen[1].at).toBeGreaterThanOrEqual(reset * 1000);
    expect(lines).toHaveLength(1);
    expect(lines[0]).toStartWith(
      `quarantine-check: GitHub refused ${server.url}${listingPath} with HTTP 403, so it waits ` +
        `until ${new Date((reset + 3) * 1000).toISOString()}, by its x-ratelimit-reset: ${reset} ` +
        "(x-ratelimit-remaining: 0, x-ratelimit-resource: core), plus 3 s, and asks again. The " +
        "cap on this reading's waits is 60 s, "
    );
    // The wait is the 2 s to the reset and the 3 s past it, less what the refusal took.
    expect(lines[0]).toMatch(/, [3-5] s of it spent with this wait\.$/u);
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
    const github = gitHub(server.url, "t", { log: (line) => lines.push(line), waitCapSeconds: 60 });
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
    const github = gitHub(server.url, "t", { log: (line) => lines.push(line), waitCapSeconds: 60 });
    await github.graphql("query { rateLimit { cost remaining resetAt } }", {});
    expect(server.seen[1].at - server.seen[0].at).toBeGreaterThanOrEqual(1000);
    expect(lines).toHaveLength(1);
    expect(lines[0]).toContain("with HTTP 403, so it waits until ");
    expect(lines[0]).toContain(", by its retry-after: 1, and asks again.");
  } finally {
    server.stop();
  }
});

test("GitHub's rate limit whose reset is past the cap fails at once, naming the refusal", async () => {
  const reset = Math.floor(Date.now() / 1000) + 600;
  const server = gitHubStandIn([], {
    rest: [
      {
        body: { message: "API rate limit exceeded for installation." },
        headers: { "x-ratelimit-remaining": "0", "x-ratelimit-reset": String(reset) },
        status: 403,
      },
    ],
  });
  const lines: string[] = [];
  try {
    const github = gitHub(server.url, "t", { log: (line) => lines.push(line), waitCapSeconds: 60 });
    const started = Date.now();
    await expect(github.one(listingPath)).rejects.toThrow(
      `GitHub refused ${server.url}${listingPath} with HTTP 403: ` +
        '{"message":"API rate limit exceeded for installation."}, and the time its rate limit ' +
        `names, ${new Date((reset + 3) * 1000).toISOString()} by its x-ratelimit-reset: ${reset} ` +
        "(x-ratelimit-remaining: 0), plus 3 s, is past the cap of 60 s on this reading's waits " +
        "(0 s of it spent), so it was not waited for. This is a failed read of the runs and not " +
        "a finding: the quarantine was not checked."
    );
    // At once: the ladder's first wait for a refusal it asks again is 10 s.
    expect(Date.now() - started).toBeLessThan(9000);
    expect(server.seen).toHaveLength(1);
    expect(lines).toEqual([]);
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
    const github = gitHub(server.url, "t", { log: (line) => lines.push(line), waitCapSeconds: 60 });
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
