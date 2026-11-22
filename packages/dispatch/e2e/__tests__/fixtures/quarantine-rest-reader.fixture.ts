/** The REST reader `e2e/tools/quarantine-check.ts` read the counted runs with before it read their
 *  check runs through GraphQL: lines 421-600 of that file at 8f94528d
 *  (`git show 8f94528d:packages/dispatch/e2e/tools/quarantine-check.ts | sed -n '421,600p'`),
 *  verbatim but for the `export` on `gitHub` and `readHistory` (whose signature Biome then wraps)
 *  and the constants above them, which that file held privately. It reads each counted run's
 *  check runs through `check-suites/<id>/check-runs` and each check run's notices through
 *  `check-runs/<id>/annotations`, one REST call each, and `e2e/__tests__/quarantine-check.test.ts`
 *  runs it and the GraphQL reader over the same answers and holds their findings equal. Nothing
 *  else imports it. */
import { countedRuns, type RunReading, windowDays } from "../../tools/quarantine-check";

const dayMs = 86_400_000;
const flakyRowTitle = "dispatch-browser flaky row";
const flakeRateTitle = "dispatch-browser flake rate";

/** The reads `readHistory` and the rest make, and what they cost. */
interface GitHub {
  /** Every page of `path`, the list of each page taken from `key` of its body (or the body itself
   *  where it is a list), with the `total_count` the last page gave. */
  everyPage<Item>(path: string, key: string): Promise<{ items: Item[]; total: number }>;
  /** The body of `path`, or `undefined` where GitHub has no such thing (404 or 422). */
  one(path: string): Promise<unknown>;
  /** The body GitHub's GraphQL endpoint answers `query` with, on the same ladder. */
  graphql(query: string, variables: Record<string, string>): Promise<unknown>;
  /** The calls made so far, retries included, and the rate limit's remaining calls last read. */
  spent(): { calls: number; remaining: string | null };
}

/** The GitHub REST API at `api`, read with `token`: one call is three attempts at most, 10 s and
 *  then 20 s apart, each bounded at 30 s, the ladder `.github/scripts/gh-read.sh` gives the
 *  scripts beside it, since GitHub refuses a call now and then (a 502, a secondary rate limit)
 *  and one refusal must decide nothing. A call refused three times throws, naming the call. */
export function gitHub(api: string, token: string): GitHub {
  let calls = 0;
  let remaining: string | null = null;
  async function page(
    url: string,
    post?: unknown
  ): Promise<{ body: unknown; next: string | undefined }> {
    let last = "";
    for (const attempt of [1, 2, 3]) {
      calls += 1;
      try {
        const response = await fetch(url, {
          body: post === undefined ? undefined : JSON.stringify(post),
          method: post === undefined ? "GET" : "POST",
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
        if (response.status === 404 || response.status === 422) {
          return { body: undefined, next: undefined };
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
      if (body === undefined) throw new Error(`GitHub has no ${url}`);
      const object = body as Record<string, unknown>;
      total = typeof object.total_count === "number" ? object.total_count : total;
      items.push(...((Array.isArray(body) ? body : object[key]) as Item[]));
      url = next;
    }
    return { items, total };
  }
  return {
    everyPage,
    graphql: async (query, variables) => (await page(`${api}/graphql`, { query, variables })).body,
    one: async (path) => (await page(`${api}${path}`)).body,
    spent: () => ({ calls, remaining }),
  };
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
 *  rather than read short. The days of both workflows are listed `inTurn`, and each workflow's
 *  runs are then taken a day at a time, oldest first, which is the order its readings keep. */
export async function readHistory(
  github: GitHub,
  repository: string,
  now: Date
): Promise<RunReading[]> {
  const slices = countedRuns.flatMap((counted) =>
    Array.from({ length: windowDays }, (_, index) => ({ counted, day: windowDays - index }))
  );
  const listed = await inTurn(slices, 4, async ({ counted, day }) => {
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
    return items;
  });
  const readings: RunReading[] = [];
  for (const counted of countedRuns) {
    const runs = new Map<number, ListedRun>();
    slices.forEach((slice, index) => {
      if (slice.counted !== counted) return;
      for (const run of listed[index]) runs.set(run.id, run);
    });
    const read = await inTurn([...runs.values()], 4, async (run): Promise<RunReading> => {
      const { items: checkRuns } = await github.everyPage<{
        id: number;
        output: { annotations_count: number };
      }>(
        `/repos/${repository}/check-suites/${run.check_suite_id}/check-runs` +
          `?check_name=${encodeURIComponent(counted.mergeJob)}&filter=all&per_page=100`,
        "check_runs"
      );
      const flakyMessages: string[] = [];
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
        flakyMessages.push(...rows);
        unread += Math.max(0, flaky - rows.length);
      }
      return {
        createdAt: run.created_at,
        event: run.event,
        flakyRows: flakyMessages,
        id: run.id,
        unread,
        url: run.html_url,
      };
    });
    readings.push(...read);
  }
  return readings;
}
