/** The nightly's half of the quarantine, `e2e/tools/quarantine-check.ts`, driven through its pure
 *  part with the planted history of `fixtures/quarantine-history.fixture.ts`: what `decide`
 *  names, the lines `report` gives for each, and how a row's own last change and its fix move
 *  what counts, and the refusal `blame` gives an answer that holds no blame, read from a
 *  stand-in for GitHub. The moment of each of those readings is the fixture's.
 *
 *  Its reads are driven against `gitHubStandIn` below, a stand-in for GitHub's REST and GraphQL
 *  API on a local port: the GraphQL reader beside the REST reader it replaced
 *  (`fixtures/quarantine-rest-reader.fixture.ts`) over the answers GitHub gave both
 *  (`fixtures/quarantine-answers.fixture.json`), what the GraphQL reader costs, its pages, and
 *  the client's waits for GitHub's rate limit, which read the clock and wait a few real seconds.
 *  Nothing here reads GitHub itself. */
import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";

import { entryLines, quarantine } from "../quarantine";
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
import {
  gitHub as restGitHub,
  readHistory as restReadHistory,
} from "./fixtures/quarantine-rest-reader.fixture";

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

/** A notice of a check run, as both of GitHub's APIs give it. */
interface Note {
  readonly title: string | null;
  readonly message: string;
}

/** One run as the stand-in answers for it: the fields the listing gives, and its merge job's
 *  check runs, as REST's check-runs and annotations routes answer them and as GraphQL's
 *  `CheckSuite.checkRuns` does. */
interface StoodRun {
  readonly workflow: string;
  readonly run: {
    readonly id: number;
    readonly event: string;
    readonly created_at: string;
    readonly html_url: string;
    readonly check_suite_id: number;
    readonly check_suite_node_id: string;
  };
  readonly rest: {
    readonly checkRuns: readonly {
      readonly id: number;
      readonly output: { readonly annotations_count: number };
    }[];
    readonly annotations: Readonly<Record<string, readonly Note[]>>;
  };
  readonly graphql: readonly { readonly id: string; readonly annotations: readonly Note[] }[];
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
 *  - `GET .../check-suites/<id>/check-runs?check_name=<name>` and
 *    `GET .../check-runs/<id>/annotations`: REST's answers, for the run's merge job's name alone;
 *  - `POST /graphql`: GraphQL's, told apart by their variables - `ids` the suites of one call,
 *    `id` with `checkName` a suite's further check runs, `id` alone a check run's further notices,
 *    neither only `rateLimit` - paged at `checkRunsPerPage` and `annotationsPerPage` with the
 *    offset as the cursor, each answer costing 1 point of 4,999 left.
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
    (mergeJob(run.workflow) === checkName ? run.graphql : []).map((checkRun) => ({
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
            .flatMap((stood) => stood.graphql)
            .find((graphqlRun) => graphqlRun.id === variables.id);
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
      const checkRuns = /^\/repos\/[^/]+\/[^/]+\/check-suites\/(\d+)\/check-runs$/.exec(
        url.pathname
      );
      if (checkRuns !== null) {
        const run = runs.find((stood) => String(stood.run.check_suite_id) === checkRuns[1]);
        const listed =
          run !== undefined && mergeJob(run.workflow) === url.searchParams.get("check_name")
            ? run.rest.checkRuns
            : [];
        return Response.json({ check_runs: listed, total_count: listed.length });
      }
      const notes = /^\/repos\/[^/]+\/[^/]+\/check-runs\/(\d+)\/annotations$/.exec(url.pathname);
      if (notes !== null) {
        const run = runs.find((stood) => stood.rest.annotations[notes[1]] !== undefined);
        return Response.json(run?.rest.annotations[notes[1]] ?? []);
      }
      return Response.json({ message: "Not Found" }, { status: 404 });
    },
  });
  return { seen, stop: () => server.stop(true), url: `http://127.0.0.1:${server.port}` };
}

/** Run `id` of `workflow`, created at `created`, whose merge job left one check run for each list
 *  of `checkRuns`, holding those notices, and which both APIs answer alike. */
function stoodRun(
  workflow: string,
  id: number,
  created: Date,
  checkRuns: readonly (readonly Note[])[]
): StoodRun {
  return {
    graphql: checkRuns.map((annotations, index) => ({ annotations, id: `CR_${id}_${index}` })),
    rest: {
      annotations: Object.fromEntries(checkRuns.map((notes, index) => [`${id}0${index}`, notes])),
      checkRuns: checkRuns.map((notes, index) => ({
        id: Number(`${id}0${index}`),
        output: { annotations_count: notes.length },
      })),
    },
    run: {
      check_suite_id: id,
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
 *  merge job left no check run. REST's answers are those of the routes the REST reader reads
 *  (`check-suites/<id>/check-runs?check_name=...&filter=all`, `check-runs/<id>/annotations`) and
 *  GraphQL's those of `nodes(ids:)`, each read apart and trimmed to the fields either reader
 *  reads; the listing's fields are the run listing's. */
const answers = JSON.parse(
  readFileSync(new URL("./fixtures/quarantine-answers.fixture.json", import.meta.url), "utf8")
) as { runs: StoodRun[] };
const recordedAt = new Date("2026-11-22T02:17:00Z");

test("the GraphQL reader reads what the REST reader read, over the answers GitHub gave both", async () => {
  const server = gitHubStandIn(answers.runs);
  try {
    const rest = await restReadHistory(restGitHub(server.url, "t"), repository, recordedAt);
    const graphql = await readHistory(gitHub(server.url, "t"), repository, recordedAt);
    expect(graphql).toHaveLength(answers.runs.length);
    expect(graphql.filter((run) => run.flakyRows.length > 0)).toHaveLength(7);
    expect(graphql).toEqual(rest);
    const findings = (runs: readonly RunReading[]) => {
      const decision = decide(runs, quarantine, [], new Map(), recordedAt);
      return { decision, report: report(decision, recordedAt) };
    };
    expect(findings(graphql)).toEqual(findings(rest));
  } finally {
    server.stop();
  }
});

// 253 runs, a few a day: 3 of the nightly and 250 merge groups, every other one with a flaky row.
// The REST reader read each run's check runs, and each check run's notices where it had any.
test("the GraphQL reader reads a hundred suites a call where the REST reader read each run apart", async () => {
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
    const rest = restGitHub(server.url, "t");
    expect(await restReadHistory(rest, repository, recordedAt)).toEqual(readings);
    expect(rest.spent().calls).toBe(2 * windowDays + 253 + 127);
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
    expect(await restReadHistory(restGitHub(server.url, "t"), repository, recordedAt)).toEqual(
      readings
    );
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
