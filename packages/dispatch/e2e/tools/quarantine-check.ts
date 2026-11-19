/** `bun e2e/tools/quarantine-check.ts`: the half of the browser suite's quarantine that depends on
 *  the date (R4 of dispatch://INFRA-983), which `.github/workflows/dispatch-browser-nightly.yaml`
 *  runs and no pull request does. `e2e/quarantine.ts` holds the list and says what reads it.
 *
 *  It names two things, and exits 1 when it names either:
 *
 *  - a row flaky in `flakyRunsToQuarantine` or more runs of the last `windowDays` days that the
 *    quarantine does not hold, with the runs and the lines to add to `quarantine`;
 *  - an entry held longer than `windowDays` days, with its issue and the lines to remove.
 *
 *  The runs it reads are the scheduled runs of the nightly and the merge-group runs of ci.yaml,
 *  and no other: a pull request's head can carry its own defect, so a row that failed and then
 *  passed there says as much about that head as about the row. What it reads of each is the
 *  `dispatch-browser flaky row` notices of its merge job (the "Reading two" step of
 *  .github/workflows/dispatch-browser.yaml's merge job prints one per flaky row), through the
 *  check runs API: the run listing (`actions: read`), the merge job's check runs in the run's
 *  check suite and their annotations (`checks: read`).
 *
 *  The GitHub reads are `readHistory` alone, and they decide nothing; `decide` and `report` are
 *  pure over what was read, the quarantine and the moment given them, which is how
 *  `e2e/__tests__/quarantine-check.test.ts` drives them. Only the command line reads the clock.
 *
 *  Environment: GITHUB_REPOSITORY (`owner/name`), GH_TOKEN or GITHUB_TOKEN, GITHUB_API_URL
 *  (https://api.github.com when unset) and, where it is set, GITHUB_STEP_SUMMARY, which the
 *  summary is appended to as well as printed. It writes nothing to GitHub. A read GitHub refuses
 *  three times exits 1 too, and says it is a failed read rather than a finding. Usage errors exit
 *  2. From a workstation, with gh signed in:
 *
 *    GITHUB_REPOSITORY=cybermonklabs/legion GH_TOKEN="$(gh auth token)" \
 *      bun e2e/tools/quarantine-check.ts
 */
import { appendFileSync } from "node:fs";

import {
  entryLines,
  type QuarantineEntry,
  type QuarantineRow,
  quarantine,
  rowName,
} from "../quarantine";

/** How far back a flaky row counts, and how long an entry may be held: R4's 14 days. */
export const windowDays = 14;

/** How many runs a row must be flaky in for the quarantine to owe it an entry: R4's "twice". */
export const flakyRunsToQuarantine = 2;

/** The runs whose flaky rows count, each with the workflow that holds them and the name of its
 *  merge job's check run: a called workflow's job is named `<the caller's job> / <its job>`, and
 *  the nightly calls dispatch-browser.yaml from its `full` job, ci.yaml from `dispatch-browser`. */
export const countedRuns = [
  { event: "schedule", mergeJob: "full / merge", workflow: "dispatch-browser-nightly.yaml" },
  { event: "merge_group", mergeJob: "dispatch-browser / merge", workflow: "ci.yaml" },
] as const;

/** What one run's merge jobs said. */
export interface RunReading {
  readonly id: number;
  /** The run's event: `schedule`, `merge_group`, `pull_request`. */
  readonly event: string;
  /** When the run was created, as GitHub gives it. */
  readonly createdAt: string;
  /** The run's page. */
  readonly url: string;
  /** The message of every `dispatch-browser flaky row` notice of its merge jobs, one merge job
   *  per attempt: `<file>:<line> [<project>] <title>`. */
  readonly flakyRows: readonly string[];
  /** How many flaky rows its flake-rate notices counted beyond the row notices read: rows that
   *  are in that run's summary and not in its annotations. */
  readonly unread: number;
}

/** What the readings say of the quarantine. */
export interface Decision {
  /** The readings that count: of a counted event, and inside the window. */
  readonly counted: readonly RunReading[];
  /** Each row flaky in enough counted runs that the quarantine does not hold, with those runs. */
  readonly toQuarantine: readonly {
    readonly row: QuarantineRow;
    readonly runs: readonly RunReading[];
  }[];
  /** Each entry held longer than the window, with how many days it has been held. */
  readonly overdue: readonly { readonly entry: QuarantineEntry; readonly days: number }[];
  /** A flaky-row notice whose message names no row, which nothing above could count. */
  readonly unparsed: readonly { readonly run: RunReading; readonly message: string }[];
}

const dayMs = 86_400_000;
const noticePattern = /^(?<file>[^\s:]+):(?<line>\d+) \[(?<project>[^\]]+)\] (?<title>.+)$/u;
const flakyRowTitle = "dispatch-browser flaky row";
const flakeRateTitle = "dispatch-browser flake rate";
const quarantinePath = "packages/dispatch/e2e/quarantine.ts";

/** What `runs` say of `entries` at `now`. A run counts when its event is one of `countedRuns`'
 *  and it was created in the `windowDays` days before `now`; a row counts once per run, however
 *  many attempts of that run named it, since a re-run's merge job joins the blob reports of the
 *  legs it did not re-run and names their flaky rows again. An entry is overdue when more than
 *  `windowDays` calendar days (UTC) lie between the day it was added and the day of `now`. */
export function decide(
  runs: readonly RunReading[],
  entries: readonly QuarantineEntry[],
  now: Date
): Decision {
  const end = now.getTime();
  const start = end - windowDays * dayMs;
  const events = new Set<string>(countedRuns.map((counted) => counted.event));
  const counted = runs.filter((run) => {
    const created = Date.parse(run.createdAt);
    return events.has(run.event) && created >= start && created <= end;
  });
  const flaky = new Map<string, { row: QuarantineRow; runs: RunReading[] }>();
  const unparsed: { run: RunReading; message: string }[] = [];
  for (const run of counted) {
    const named = new Set<string>();
    for (const message of run.flakyRows) {
      const groups = noticePattern.exec(message)?.groups;
      if (groups === undefined) {
        unparsed.push({ message, run });
        continue;
      }
      const row = { file: groups.file, project: groups.project, title: groups.title };
      const name = rowName(row);
      if (named.has(name)) continue;
      named.add(name);
      const seen = flaky.get(name) ?? { row, runs: [] };
      seen.runs.push(run);
      flaky.set(name, seen);
    }
  }
  const held = new Set(entries.map(rowName));
  const toQuarantine = [...flaky.entries()]
    .filter(([name, seen]) => seen.runs.length >= flakyRunsToQuarantine && !held.has(name))
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([, seen]) => seen);
  const today = Date.parse(`${now.toISOString().slice(0, 10)}T00:00:00Z`);
  const overdue = entries
    .map((entry) => ({
      days: Math.round((today - Date.parse(`${entry.added}T00:00:00Z`)) / dayMs),
      entry,
    }))
    .filter(({ days }) => days > windowDays);
  return { counted, overdue, toQuarantine, unparsed };
}

/** The decision for a person and for the log: `summary` is Markdown, the step summary's;
 *  `annotations` are workflow commands, one per finding; `failed` is whether the run must fail. */
export function report(
  decision: Decision,
  now: Date
): { annotations: string[]; failed: boolean; summary: string[] } {
  const today = now.toISOString().slice(0, 10);
  const runLink = (run: RunReading) =>
    `[run ${run.id}](${run.url}) of ${run.createdAt.slice(0, 10)} (${run.event})`;
  const flakyRuns = decision.counted.filter((run) => run.flakyRows.length > 0);
  const summary = [
    `### The quarantine on ${today}`,
    "",
    `Read the merge jobs of ${decision.counted.length} runs of the ${windowDays} days before ` +
      `${now.toISOString()}: ` +
      countedRuns
        .map(
          (counted) =>
            `${decision.counted.filter((run) => run.event === counted.event).length} ` +
            `${counted.event} runs of ${counted.workflow}`
        )
        .join(" and ") +
      `. ${flakyRuns.length} of them named a flaky row. A pull request's runs are not read: its ` +
      "head can carry its own defect.",
    "",
    `#### Flaky in ${flakyRunsToQuarantine} or more runs, and not quarantined`,
    "",
  ];
  const annotations: string[] = [];
  if (decision.toQuarantine.length === 0) {
    summary.push(
      `No row was flaky in ${flakyRunsToQuarantine} runs that the quarantine does not hold.`
    );
  }
  for (const { row, runs } of decision.toQuarantine) {
    summary.push(
      `- \`${rowName(row)}\`, flaky in ${runs.length} runs: ${runs.map(runLink).join(", ")}.`,
      "",
      `  The controller files the issue that owns it. Add these lines to \`quarantine\` in ` +
        `\`${quarantinePath}\`, with that issue's key for \`<KEY>\`:`,
      "",
      "  ```ts",
      ...entryLines({ ...row, added: today, issue: "dispatch://<KEY>" }).map((line) => `  ${line}`),
      "  ```",
      ""
    );
    annotations.push(
      `::error title=dispatch-browser quarantine::${rowName(row)} was flaky in ${runs.length} ` +
        `runs of ${windowDays} days and is not quarantined: ${runs.map((run) => run.url).join(" ")}`
    );
  }
  summary.push("", `#### Held longer than ${windowDays} days`, "");
  if (decision.overdue.length === 0) {
    summary.push(`No entry has been held longer than ${windowDays} days.`);
  }
  for (const { days, entry } of decision.overdue) {
    summary.push(
      `- \`${rowName(entry)}\`, added ${entry.added}, ${days} days ago, owned by ${entry.issue}. ` +
        `Once that issue's fix lands, remove these lines from \`${quarantinePath}\`:`,
      "",
      "  ```ts",
      ...entryLines(entry).map((line) => `  ${line}`),
      "  ```",
      ""
    );
    annotations.push(
      `::error title=dispatch-browser quarantine::${rowName(entry)} has been quarantined ` +
        `${days} days, more than ${windowDays}; its issue is ${entry.issue}`
    );
  }
  const unread = decision.counted.filter((run) => run.unread > 0);
  if (unread.length > 0 || decision.unparsed.length > 0) {
    summary.push("", "#### What this reading could not count", "");
  }
  for (const run of unread) {
    summary.push(
      `- ${runLink(run)}: its flake rate counts ${run.unread} more flaky rows than its row ` +
        "notices name, so those rows are in its summary alone and are not counted here."
    );
    annotations.push(
      `::warning title=dispatch-browser quarantine::run ${run.id} counts ${run.unread} flaky ` +
        "rows that no row notice names, and they are not counted"
    );
  }
  for (const { message, run } of decision.unparsed) {
    summary.push(`- ${runLink(run)}: a flaky-row notice that names no row: \`${message}\`.`);
  }
  return {
    annotations,
    failed: decision.toQuarantine.length > 0 || decision.overdue.length > 0,
    summary,
  };
}

/** The GitHub REST API at `api`, read with `token`: one call is three attempts at most, 10 s and
 *  then 20 s apart, each bounded at 30 s, the ladder `.github/scripts/gh-read.sh` gives the
 *  scripts beside it, since GitHub refuses a call now and then (a 502, a secondary rate limit)
 *  and one refusal must decide nothing. A call refused three times throws, naming the call. */
function gitHub(api: string, token: string): GitHub {
  let calls = 0;
  let remaining: string | null = null;
  async function page(url: string): Promise<{ body: unknown; next: string | undefined }> {
    let last = "";
    for (const attempt of [1, 2, 3]) {
      calls += 1;
      try {
        const response = await fetch(url, {
          headers: {
            accept: "application/vnd.github+json",
            authorization: `Bearer ${token}`,
            "x-github-api-version": "2022-11-28",
          },
          signal: AbortSignal.timeout(30_000),
        });
        remaining = response.headers.get("x-ratelimit-remaining") ?? remaining;
        if (response.ok) {
          const next = /<([^>]+)>;\s*rel="next"/.exec(response.headers.get("link") ?? "")?.[1];
          return { body: await response.json(), next };
        }
        last = `HTTP ${response.status}: ${(await response.text()).slice(0, 200)}`;
      } catch (error) {
        last = error instanceof Error ? error.message : String(error);
      }
      if (attempt < 3) await Bun.sleep(attempt * 10_000);
    }
    throw new Error(
      `GitHub refused ${url} three times, the last with ${last}. This is a failed read of the ` +
        "runs and not a finding: the quarantine was not checked."
    );
  }
  async function everyPage<Item>(
    path: string,
    key: string
  ): Promise<{ items: Item[]; total: number }> {
    const items: Item[] = [];
    let total = 0;
    let url: string | undefined = `${api}${path}`;
    while (url !== undefined) {
      const { body, next }: { body: unknown; next: string | undefined } = await page(url);
      const object = body as Record<string, unknown>;
      total = typeof object.total_count === "number" ? object.total_count : total;
      items.push(...((Array.isArray(body) ? body : object[key]) as Item[]));
      url = next;
    }
    return { items, total };
  }
  return { everyPage, spent: () => ({ calls, remaining }) };
}

/** The reads `readHistory` makes, and what they cost. */
interface GitHub {
  /** Every page of `path`, the list of each page taken from `key` of its body (or the body itself
   *  where it is a list), with the `total_count` the last page gave. */
  everyPage<Item>(path: string, key: string): Promise<{ items: Item[]; total: number }>;
  /** The calls made so far, retries included, and the rate limit's remaining calls last read. */
  spent(): { calls: number; remaining: string | null };
}

interface ListedRun {
  readonly check_suite_id: number;
  readonly created_at: string;
  readonly event: string;
  readonly html_url: string;
  readonly id: number;
}

/** Each of `items` through `work`, `width` at a time, in order. GitHub asks a client not to
 *  make its calls all at once (its secondary rate limits), and four is a few. */
async function inTurn<Item, Result>(
  items: readonly Item[],
  width: number,
  work: (item: Item) => Promise<Result>
): Promise<Result[]> {
  const results: Result[] = new Array(items.length);
  let next = 0;
  await Promise.all(
    Array.from({ length: width }, async () => {
      while (next < items.length) {
        const index = next++;
        results[index] = await work(items[index]);
      }
    })
  );
  return results;
}

/** The readings of every run of `countedRuns` created in the `windowDays` days before `now`
 *  that has completed. A filtered run listing answers at most 1,000 runs (GitHub's REST
 *  reference, "List workflow runs for a workflow"), and ci.yaml makes more merge-group runs than
 *  that in 14 days, so the window is read a day at a time, and a day that holds more is refused
 *  rather than read short. */
async function readHistory(github: GitHub, repository: string, now: Date): Promise<RunReading[]> {
  const readings: RunReading[] = [];
  for (const counted of countedRuns) {
    const runs = new Map<number, ListedRun>();
    for (let day = windowDays; day > 0; day--) {
      const from = new Date(now.getTime() - day * dayMs).toISOString();
      const to = new Date(now.getTime() - (day - 1) * dayMs).toISOString();
      const { items, total } = await github.everyPage<ListedRun>(
        `/repos/${repository}/actions/workflows/${counted.workflow}/runs?event=${counted.event}` +
          `&status=completed&created=${from}..${to}&per_page=100`,
        "workflow_runs"
      );
      if (total > 1000) {
        throw new Error(
          `${counted.workflow} made ${total} ${counted.event} runs between ${from} and ${to}, ` +
            "and GitHub lists 1,000 of a filtered listing at most; read a shorter slice"
        );
      }
      for (const run of items) runs.set(run.id, run);
    }
    const read = await inTurn([...runs.values()], 4, async (run): Promise<RunReading> => {
      const { items: checkRuns } = await github.everyPage<{
        id: number;
        output: { annotations_count: number };
      }>(
        `/repos/${repository}/check-suites/${run.check_suite_id}/check-runs` +
          `?check_name=${encodeURIComponent(counted.mergeJob)}&filter=all&per_page=100`,
        "check_runs"
      );
      const flakyRows: string[] = [];
      let unread = 0;
      for (const checkRun of checkRuns.filter((check) => check.output.annotations_count > 0)) {
        const { items: notes } = await github.everyPage<{ message: string; title: string | null }>(
          `/repos/${repository}/check-runs/${checkRun.id}/annotations?per_page=100`,
          "annotations"
        );
        const rows = notes
          .filter((note) => note.title === flakyRowTitle)
          .map((note) => note.message);
        const rate = notes.find((note) => note.title === flakeRateTitle);
        const flaky = Number(/^(\d+) flaky rows of /.exec(rate?.message ?? "")?.[1] ?? rows.length);
        flakyRows.push(...rows);
        unread += Math.max(0, flaky - rows.length);
      }
      return {
        createdAt: run.created_at,
        event: run.event,
        flakyRows,
        id: run.id,
        unread,
        url: run.html_url,
      };
    });
    readings.push(...read);
  }
  return readings;
}

if (import.meta.main) {
  const repository = process.env.GITHUB_REPOSITORY ?? "";
  const token = process.env.GH_TOKEN || process.env.GITHUB_TOKEN || "";
  if (process.argv.length > 2 || !/^[^/\s]+\/[^/\s]+$/.test(repository) || token === "") {
    console.error(
      "usage: GITHUB_REPOSITORY=<owner>/<name> GH_TOKEN=<token> bun e2e/tools/quarantine-check.ts\n" +
        "  reads the merge jobs' flaky-row notices of the last 14 days and the quarantine's dates"
    );
    process.exit(2);
  }
  const now = new Date();
  const started = performance.now();
  const github = gitHub(process.env.GITHUB_API_URL ?? "https://api.github.com", token);
  let runs: RunReading[];
  try {
    runs = await readHistory(github, repository, now);
  } catch (error) {
    console.error(`::error title=dispatch-browser quarantine::${(error as Error).message}`);
    process.exit(1);
  }
  const { annotations, failed, summary } = report(decide(runs, quarantine, now), now);
  console.log(summary.join("\n"));
  if (process.env.GITHUB_STEP_SUMMARY) {
    appendFileSync(process.env.GITHUB_STEP_SUMMARY, `${summary.join("\n")}\n`);
  }
  for (const annotation of annotations) console.log(annotation);
  const { calls, remaining } = github.spent();
  console.error(
    `read ${runs.length} runs in ${calls} GitHub calls and ${Math.round(performance.now() - started)} ms; ` +
      `the token has ${remaining ?? "an unknown number of"} calls left this hour`
  );
  process.exit(failed ? 1 : 0);
}
