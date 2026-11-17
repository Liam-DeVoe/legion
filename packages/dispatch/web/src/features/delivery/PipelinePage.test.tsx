import { afterEach, beforeEach, expect, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";

import {
  errorFrame,
  type FakeLiveSocket,
  type FakeLiveSockets,
  installFakeLiveSockets,
  snapshotFrame,
} from "../../__tests__/live-socket-harness";
import type {
  DeliveryLeadTimeTiming,
  DeliveryPipelineResponse,
  DeliveryTiming,
  DeliveryTimingCell,
  LiveSubscribe,
} from "../../api/types";
import { chartBar } from "../../theme/classes";
import { PipelinePage, PipelineView } from "./PipelinePage";

let sockets: FakeLiveSockets;
beforeEach(() => {
  sockets = installFakeLiveSockets();
});
afterEach(() => {
  cleanup();
  sockets.restore();
});

// The prototype's web/src/lib/pipeline.fixture.ts, its cells as the server answers them, with the
// deployment's job names replaced by placeholders: seven days, Sep 26 to Oct 2, today Oct 2.
const DAYS = [
  "2026-09-26",
  "2026-09-27",
  "2026-09-28",
  "2026-09-29",
  "2026-09-30",
  "2026-10-01",
  "2026-10-02",
];

const EMPTY: DeliveryTimingCell = {
  n: 0,
  median_minutes: null,
  p90_minutes: null,
  success: 0,
  failure: 0,
  cancelled: 0,
  success_rate: null,
};

function timing(
  cells: Record<string, Partial<DeliveryTimingCell>>,
  total: Partial<DeliveryTimingCell> = {}
): DeliveryTiming {
  return {
    days: DAYS.map((day, index) => ({
      day,
      partial: index === DAYS.length - 1,
      ...EMPTY,
      ...cells[day],
    })),
    total: { ...EMPTY, ...total },
  };
}

function leadTimes(
  today: { n: number; median: number; p90: number; max: number } | null,
  total: { n: number; median: number; p90: number; max: number } | null
): DeliveryLeadTimeTiming {
  const cell = (v: typeof today) =>
    v === null
      ? { n: 0, median_minutes: null, p90_minutes: null, max_minutes: null }
      : { n: v.n, median_minutes: v.median, p90_minutes: v.p90, max_minutes: v.max };
  return {
    days: DAYS.map((day, index) => ({
      day,
      partial: index === DAYS.length - 1,
      ...(index === DAYS.length - 1 ? cell(today) : cell(null)),
    })),
    total: cell(total),
  };
}

const RUN_URL = "https://github.com/acme/widgets/actions/runs/37000000001";

type Targets = DeliveryPipelineResponse["targets"];

/** The six targets as the catalogue's evals hold them, which the answer states until a person
 *  changes one: each with its eval's direction and the statistics its verdict judges. */
const TARGETS: { [K in keyof Targets]: NonNullable<Targets[K]> } = {
  deploys_per_day: {
    value: 20,
    inclusive: true,
    direction: "higher_better",
    judges: ["deploys.per_day"],
  },
  change_failure_rate: {
    value: 0.05,
    inclusive: false,
    direction: "lower_better",
    judges: ["per_deploy.rate"],
  },
  merge_to_production_minutes: {
    value: 45,
    inclusive: false,
    direction: "lower_better",
    judges: ["minutes.max", "minutes.median"],
  },
  opened_to_merge_median_minutes: {
    value: 60,
    inclusive: false,
    direction: "lower_better",
    judges: ["minutes.median"],
  },
  deploy_run_success_rate: {
    value: 0.9,
    inclusive: true,
    direction: "higher_better",
    judges: ["runs.rate"],
  },
  unowned_p0: { value: 0, inclusive: true, direction: "lower_better", judges: ["issues.value"] },
};

const PIPELINE: DeliveryPipelineResponse = {
  days: DAYS,
  computed_at: "2026-10-02T12:00:00Z",
  pr_checks: {
    run: timing(
      {
        "2026-10-01": {
          n: 300,
          median_minutes: 24.2,
          p90_minutes: 97.1,
          success: 260,
          failure: 40,
          cancelled: 410,
          success_rate: 0.8667,
        },
        "2026-10-02": {
          n: 120,
          median_minutes: 21.5,
          p90_minutes: 93.4,
          success: 102,
          failure: 18,
          cancelled: 150,
          success_rate: 0.85,
        },
      },
      {
        n: 1800,
        median_minutes: 22.8,
        p90_minutes: 95,
        success: 1550,
        failure: 250,
        cancelled: 2400,
        success_rate: 0.8611,
      }
    ),
    slowest_min_results: 50,
    slowest_jobs: [
      {
        name: "build / unit",
        ...timing(
          {
            "2026-10-02": {
              n: 40,
              median_minutes: 92.6,
              p90_minutes: 110.2,
              success: 38,
              failure: 2,
              success_rate: 0.95,
            },
          },
          { n: 400, median_minutes: 93.1, p90_minutes: 112, success: 380, failure: 20 }
        ),
      },
      {
        name: "infra / plan",
        ...timing({}, { n: 900, median_minutes: 17.3, p90_minutes: 21, success: 890, failure: 10 }),
      },
    ],
    failing_jobs: [
      {
        name: "build / unit",
        failures: 6,
        results: 48,
        last_failed_at: "2026-10-02T11:40:00Z",
        last_run_url: RUN_URL,
      },
    ],
  },
  staging_e2e: {
    job: "staging-e2e / run",
    ...timing(
      {
        "2026-10-02": {
          n: 9,
          median_minutes: 41.3,
          p90_minutes: 58.9,
          success: 6,
          failure: 3,
          cancelled: 2,
          success_rate: 0.6667,
        },
      },
      {
        n: 90,
        median_minutes: 40.2,
        p90_minutes: 61,
        success: 63,
        failure: 27,
        cancelled: 15,
        success_rate: 0.7,
      }
    ),
  },
  staging_apply: {
    job: "staging-release / apply",
    ...timing(
      {
        "2026-10-02": {
          n: 12,
          median_minutes: 7.4,
          p90_minutes: 9.8,
          success: 12,
          success_rate: 1,
        },
      },
      { n: 120, median_minutes: 7.6, p90_minutes: 10.5, success: 118, failure: 2 }
    ),
  },
  production_apply: {
    job: "widgets-release / widgets-release",
    ...timing(
      {},
      {
        n: 60,
        median_minutes: 9.1,
        p90_minutes: 12.4,
        success: 59,
        failure: 1,
        success_rate: 0.9833,
      }
    ),
  },
  // The prototype's two PRs: deployed 38 and 52 minutes after merging, one today.
  merge_to_production: leadTimes(
    { n: 1, median: 38, p90: 38, max: 38 },
    { n: 2, median: 45, p90: 50.6, max: 52 }
  ),
  opened_to_merge: leadTimes(
    { n: 1, median: 59.9, p90: 59.9, max: 59.9 },
    { n: 2, median: 59.95, p90: 59.99, max: 60 }
  ),
  targets: TARGETS,
  // The server's verdicts of the two cards: the longest change, 52m, misses "under 45m"; the
  // median from open to merge, 59.95m, meets "under 60m".
  status: { merge_to_production: false, opened_to_merge: true },
  freshness: {
    last_event_at: "2026-10-02T11:59:00Z",
    last_reconcile_at: "2026-10-02T11:58:00Z",
    last_error: null,
    unfetchable_count: 0,
  },
};

function view(pipeline: DeliveryPipelineResponse = PIPELINE): HTMLElement {
  const at = Date.parse("2026-10-02T12:00:00Z");
  // The freshness row reads the live connection's state from the query client.
  render(
    <QueryClientProvider client={new QueryClient()}>
      <PipelineView nowMs={at} pipeline={pipeline} readAtMs={at} />
    </QueryClientProvider>
  );
  return document.body;
}

function card(metric: string): HTMLElement {
  const element = document.querySelector<HTMLElement>(`[data-metric="${metric}"]`);
  if (element === null) throw new Error(`no ${metric} card`);
  return element;
}

test("each card shows today's and the seven days' median and p90 with the success share", () => {
  view();
  const ci = card("pr_ci").innerHTML;
  expect(ci).toContain("PR CI");
  expect(ci).toContain(">22m<"); // today's median, 21.5
  expect(ci).toContain("p90 93m · 120 runs");
  expect(ci).toContain(">23m<"); // the week's median, 22.8
  expect(ci).toContain("p90 95m · 1800 runs");
  expect(ci).toContain(">85%<");
  expect(ci).toContain("(102 of 120)");
  expect(ci).toContain("2400 cancelled in 7 days, not counted");

  const e2e = card("staging_e2e").innerHTML;
  expect(e2e).toContain("staging-e2e / run");
  expect(e2e).toContain("p90 59m · 9 runs");
  expect(e2e).toContain(">67%<");
  expect(e2e).toContain("(6 of 9)");
});

test("a stage with nothing timed today says n/a rather than zero", () => {
  view();
  const production = card("production_apply").innerHTML;
  expect(production).toContain(">n/a<");
  expect(production).toContain("p90 n/a · 0 runs");
  expect(production).toContain("p90 12m · 60 runs");
});

test("a pipeline with no staging end-to-end gate omits that card and keeps the other five", () => {
  view({ ...PIPELINE, staging_e2e: null });
  expect(document.querySelector('[data-metric="staging_e2e"]')).toBeNull();
  for (const metric of [
    "pr_ci",
    "staging_apply",
    "production_apply",
    "merge_to_production",
    "opened_to_merge",
  ]) {
    expect(document.querySelector(`[data-metric="${metric}"]`)).not.toBeNull();
  }
});

/** What a lead-time card says of each statistic its target judges, one line a judge. */
function judged(metric: string): (string | null)[] {
  return [...card(metric).querySelectorAll("[data-judge]")].map((line) => line.textContent);
}

test("merge to production shows the response's verdict and its target as its eval holds it", () => {
  view();
  const merge = card("merge_to_production");
  expect(merge.querySelector("[data-target-met]")?.getAttribute("data-target-met")).toBe("false");
  expect(merge.innerHTML).toContain("target missed");
  expect(merge.innerHTML).toContain("p90 38m · 1 PR<");
  // The catalogue judges the longest change and the median, and the longest change under the
  // target puts the median under it: the card names and shows the longest change alone, as it
  // did before the target carried its judges.
  expect(merge.innerHTML).toContain("target under 45m for every change (the dashed line)");
  expect(judged("merge_to_production")).toEqual(["Longest today 38m, 7 days 52m"]);
  expect(card("opened_to_merge").innerHTML).toContain("target median under 60m (the dashed line)");
  expect(judged("opened_to_merge")).toEqual(["Median today 60m, 7 days 60m"]);
  for (const metric of ["pr_ci", "staging_e2e", "staging_apply", "production_apply"]) {
    expect(card(metric).innerHTML).not.toContain("target");
  }
  cleanup();

  // The verdict is the server's, its eval's own (a person may have moved the figures it judges),
  // and never the page's: the seven days' longest change, 52m, is over "under 45m", which a page
  // judging the card itself would call missed, and the server's met is what the card says.
  view({ ...PIPELINE, status: { ...PIPELINE.status, merge_to_production: true } });
  const met = card("merge_to_production");
  expect(met.querySelector("[data-target-met]")?.getAttribute("data-target-met")).toBe("true");
  expect(met.innerHTML).toContain("target met");
  cleanup();

  view({
    ...PIPELINE,
    targets: {
      ...TARGETS,
      merge_to_production_minutes: {
        ...TARGETS.merge_to_production_minutes,
        value: 52,
        inclusive: true,
      },
    },
    status: { ...PIPELINE.status, merge_to_production: true },
  });
  expect(card("merge_to_production").innerHTML).toContain("target at most 52m for every change");
  cleanup();

  view({
    ...PIPELINE,
    targets: { ...TARGETS, merge_to_production_minutes: null },
    status: { ...PIPELINE.status, merge_to_production: null },
  });
  const off = card("merge_to_production");
  expect(off.querySelector("[data-target-met]")?.getAttribute("data-target-met")).toBe("none");
  expect(off.innerHTML).toContain("target off");
  // An off target judges nothing, so the card takes its verdict away and keeps the figure it
  // showed before targets carried their judges: merge to production's longest change.
  expect(judged("merge_to_production")).toEqual(["Longest today 38m, 7 days 52m"]);
});

// R5: a person may judge a lead-time eval by any statistic the Pipeline states, and the verdict
// follows the judges, so the card names and shows the statistic its badge is of, in the target's
// own direction, rather than the one the catalogue judges.
test("a lead-time card names and shows the statistics its target judges, whichever they are", () => {
  view({
    ...PIPELINE,
    targets: {
      ...TARGETS,
      opened_to_merge_median_minutes: {
        value: 720,
        inclusive: true,
        direction: "lower_better",
        judges: ["minutes.p90"],
      },
    },
  });
  const opened = card("opened_to_merge");
  expect(opened.innerHTML).toContain("target p90 at most 720m (the dashed line)");
  expect(opened.innerHTML).not.toContain("target median");
  expect(judged("opened_to_merge")).toEqual(["P90 today 60m, 7 days 60m"]);
  cleanup();

  // Judged by its count, better higher: a number of pull requests, which the chart's minutes
  // draw no line for.
  view({
    ...PIPELINE,
    targets: {
      ...TARGETS,
      opened_to_merge_median_minutes: {
        value: 2,
        inclusive: true,
        direction: "higher_better",
        judges: ["minutes.n"],
      },
    },
    status: { ...PIPELINE.status, opened_to_merge: true },
  });
  const counted = card("opened_to_merge");
  expect(counted.innerHTML).toContain("target count at least 2<");
  expect(judged("opened_to_merge")).toEqual(["Count today 1, 7 days 2"]);
  expect(counted.querySelectorAll('svg[aria-label="per-day median and p90"] line')).toHaveLength(0);
  // Today's one pull request misses "at least 2", whatever its minutes.
  expect(todaysBars("opened_to_merge").map((bar) => bar.getAttribute("class"))).toEqual([
    chartBar.missedFaint,
    chartBar.missed,
  ]);
  cleanup();

  // The longest change and the median judged better higher: neither implies the other, so the
  // card names both and shows a line of each, where better lower it shows the longest alone.
  view({
    ...PIPELINE,
    targets: {
      ...TARGETS,
      merge_to_production_minutes: {
        ...TARGETS.merge_to_production_minutes,
        direction: "higher_better",
      },
    },
  });
  expect(card("merge_to_production").innerHTML).toContain(
    "target longest and median above 45m (the dashed line)"
  );
  expect(judged("merge_to_production")).toEqual([
    "Longest today 38m, 7 days 52m",
    "Median today 38m, 7 days 45m",
  ]);
});

test("PR open to merge shows the response's verdict of its median", () => {
  view();
  const opened = card("opened_to_merge");
  expect(opened.innerHTML).toContain("PR open to merge");
  expect(opened.querySelector("[data-target-met]")?.getAttribute("data-target-met")).toBe("true");
  expect(opened.innerHTML).toContain("target met");
  expect(opened.innerHTML).toContain("target median under 60m");
  cleanup();

  // The seven days' median, 59.95m, is under 60m, which a page judging the card itself would call
  // met; the server's verdict is missed, and the card says that.
  view({ ...PIPELINE, status: { ...PIPELINE.status, opened_to_merge: false } });
  const missed = card("opened_to_merge");
  expect(missed.querySelector("[data-target-met]")?.getAttribute("data-target-met")).toBe("false");
  expect(missed.innerHTML).toContain("target missed");
  cleanup();

  view({
    ...PIPELINE,
    opened_to_merge: leadTimes(null, null),
    status: { ...PIPELINE.status, opened_to_merge: null },
  });
  const none = card("opened_to_merge");
  expect(none.querySelector("[data-target-met]")?.getAttribute("data-target-met")).toBe("none");
  expect(none.innerHTML).toContain("no data");
});

/** The drawn bars of a lead-time card's last day, today: its p90 and its median, behind the
 *  transparent hover target the day carries. */
function todaysBars(metric: string): Element[] {
  const days = card(metric).querySelectorAll('svg[aria-label="per-day median and p90"] g');
  return [...(days[days.length - 1]?.querySelectorAll("rect") ?? [])].filter(
    (rect) => rect.getAttribute("fill") !== "transparent"
  );
}

test("a lead-time card colours a day by its eval's rule, so a day at an inclusive target meets it", () => {
  const atSixty = leadTimes(
    { n: 1, median: 60, p90: 60, max: 60 },
    { n: 1, median: 60, p90: 60, max: 60 }
  );
  view({
    ...PIPELINE,
    opened_to_merge: atSixty,
    targets: {
      ...TARGETS,
      opened_to_merge_median_minutes: {
        ...TARGETS.opened_to_merge_median_minutes,
        inclusive: true,
      },
    },
    status: { ...PIPELINE.status, opened_to_merge: true },
  });
  expect(todaysBars("opened_to_merge").map((bar) => bar.getAttribute("class"))).toEqual([
    chartBar.metFaint,
    chartBar.met,
  ]);
  cleanup();

  // The same day at a strict target of the same value misses it.
  view({
    ...PIPELINE,
    opened_to_merge: atSixty,
    status: { ...PIPELINE.status, opened_to_merge: false },
  });
  expect(todaysBars("opened_to_merge").map((bar) => bar.getAttribute("class"))).toEqual([
    chartBar.missedFaint,
    chartBar.missed,
  ]);
});

test("a lead-time card colours a day by that day's value of the judged statistics, not by the bars it draws", () => {
  // Today's median and p90 are under 45m, both bars under the line, and its longest change is
  // over: judged by the longest change and the median, the day misses.
  const longTail = leadTimes(
    { n: 3, median: 30, p90: 40, max: 52 },
    { n: 3, median: 30, p90: 40, max: 52 }
  );
  view({ ...PIPELINE, merge_to_production: longTail });
  expect(todaysBars("merge_to_production").map((bar) => bar.getAttribute("class"))).toEqual([
    chartBar.missedFaint,
    chartBar.missed,
  ]);
  cleanup();

  // Judged by its median alone, the same day meets, and under the other direction it misses.
  const byMedian = { ...TARGETS.merge_to_production_minutes, judges: ["minutes.median"] };
  view({
    ...PIPELINE,
    merge_to_production: longTail,
    targets: { ...TARGETS, merge_to_production_minutes: byMedian },
  });
  expect(todaysBars("merge_to_production").map((bar) => bar.getAttribute("class"))).toEqual([
    chartBar.metFaint,
    chartBar.met,
  ]);
  expect(card("merge_to_production").innerHTML).toContain("target median under 45m");
  cleanup();

  view({
    ...PIPELINE,
    merge_to_production: longTail,
    targets: {
      ...TARGETS,
      merge_to_production_minutes: { ...byMedian, direction: "higher_better" },
    },
  });
  expect(todaysBars("merge_to_production").map((bar) => bar.getAttribute("class"))).toEqual([
    chartBar.missedFaint,
    chartBar.missed,
  ]);
  expect(card("merge_to_production").innerHTML).toContain("target median above 45m");
});

test("lists the slowest jobs in the server's order and the jobs that failed in the last day", () => {
  view();
  const slowest = document.querySelector<HTMLElement>('[data-section="slowest-jobs"]');
  const rows = [...(slowest?.querySelectorAll("[data-job]") ?? [])].map((row) =>
    row.getAttribute("data-job")
  );
  expect(rows).toEqual(["build / unit", "infra / plan"]);
  const unit = slowest?.querySelector('[data-job="build / unit"]');
  expect(unit?.innerHTML).toContain(">93m<"); // today's median 92.6 and the week's 93.1

  const failing = document.querySelector<HTMLElement>('[data-section="failing-jobs"]');
  const row = failing?.querySelector('[data-failing-job="build / unit"]');
  expect(row?.innerHTML).toContain(">6<");
  expect(row?.innerHTML).toContain(">13%<"); // 6 of 48
  const link = row?.querySelector("a");
  expect(link?.getAttribute("href")).toBe(RUN_URL);
  expect(link?.textContent).toBe("20 min ago");
});

test("says when no job has enough results and when no job failed", () => {
  view({ ...PIPELINE, pr_checks: { ...PIPELINE.pr_checks, failing_jobs: [], slowest_jobs: [] } });
  expect(document.body).toHaveTextContent("No job has 50 timed results yet.");
  expect(document.body).toHaveTextContent("No job failed in the last 24 hours.");
});

test("the slowest jobs say the threshold the server ranked by, not 50 alone", () => {
  view({
    ...PIPELINE,
    pr_checks: { ...PIPELINE.pr_checks, slowest_jobs: [], slowest_min_results: 120 },
  });
  const slowest = document.querySelector<HTMLElement>('[data-section="slowest-jobs"]');
  expect(slowest).toHaveTextContent("a job needs 120 timed results in the 7 days to be ranked.");
  expect(slowest).toHaveTextContent("No job has 120 timed results yet.");
  expect(slowest?.textContent).not.toContain("50 timed");
  cleanup();

  // A min_n of 0 still ranks only a job with a median, so the page says one result is needed.
  view({
    ...PIPELINE,
    pr_checks: { ...PIPELINE.pr_checks, slowest_jobs: [], slowest_min_results: 0 },
  });
  const zero = document.querySelector<HTMLElement>('[data-section="slowest-jobs"]');
  expect(zero).toHaveTextContent("a job needs 1 timed result in the 7 days to be ranked.");
  expect(zero).toHaveTextContent("No job has 1 timed result yet.");
});

test("the failing-jobs note names the roll-up the server leaves out only by its role", () => {
  view();
  const note = document.querySelector('[data-section="failing-jobs"] p')?.textContent ?? "";
  expect(note).toContain("Every failed job but the roll-up job the settings name");
});

test("names the shown days in the header", () => {
  view();
  expect(document.body).toHaveTextContent("Last 7 days, Sep 26 to Oct 2 (UTC)");
});

function renderPage(path = "/pipeline") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[path]}>
        <PipelinePage />
      </MemoryRouter>
    </QueryClientProvider>
  );
}

/** The page's live socket, accepted, and the pipeline subscription it sent on it. */
async function subscribed(): Promise<{ socket: FakeLiveSocket; frame: LiveSubscribe }> {
  await waitFor(() => expect(sockets.sockets.length).toBeGreaterThan(0));
  const socket = sockets.latest();
  act(() => socket.open());
  const frame = await waitFor(() => socket.subscribed("pipeline"));
  return { socket, frame };
}

test("the page subscribes the pipeline, forwarding a pinned at from its URL, and titles the tab", async () => {
  renderPage("/pipeline?at=2026-10-02T12:00:00Z");
  expect(await screen.findByRole("heading", { level: 1, name: "Pipeline" })).toBeDefined();
  const { socket, frame } = await subscribed();
  expect(frame.view).toEqual({ kind: "pipeline", at: "2026-10-02T12:00:00Z" });
  act(() => socket.receive(snapshotFrame(frame.sub, PIPELINE)));
  await screen.findByText("PR CI");
  // A failure's age is measured from the pinned at (11:40 against 12:00), not the clock.
  expect(document.querySelector('[data-failing-job="build / unit"] a')?.textContent).toBe(
    "20 min ago"
  );
  expect(document.title).toBe("Pipeline · Dispatch");
});

test("without at in its URL the page subscribes today's pipeline", async () => {
  renderPage();
  const { socket, frame } = await subscribed();
  expect(frame.view).toEqual({ kind: "pipeline" });
  act(() => socket.receive(snapshotFrame(frame.sub, PIPELINE)));
  await screen.findByText("PR CI");
  expect(socket.subscribes()).toHaveLength(1);
});

test("an unconfigured delivery points at the settings that set it up", async () => {
  renderPage();
  const { socket, frame } = await subscribed();
  act(() =>
    socket.receive(errorFrame(frame.sub, "DELIVERY_NOT_CONFIGURED", "delivery is not configured"))
  );
  const link = await screen.findByRole("link", { name: "Set up the delivery timeline" });
  expect(link).toHaveAttribute("href", "/delivery");
});

test("while the snapshot is out the page says it is loading, and shows nothing else", async () => {
  renderPage();
  await subscribed();
  expect(await screen.findByText("Loading pipeline…")).toBeDefined();
  expect(screen.queryByText("PR CI")).not.toBeInTheDocument();
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
});

test("a refused subscription says so with Retry, and Retry subscribes again", async () => {
  const user = userEvent.setup();
  renderPage();
  const { socket, frame } = await subscribed();
  act(() => socket.receive(errorFrame(frame.sub, "UNAVAILABLE", "try later")));
  const alert = await screen.findByRole("alert");
  expect(alert).toHaveTextContent("Couldn't load the pipeline.");
  expect(
    screen.queryByRole("link", { name: "Set up the delivery timeline" })
  ).not.toBeInTheDocument();

  await user.click(screen.getByRole("button", { name: "Retry" }));
  const retried = await waitFor(() => {
    const again = socket.subscribed("pipeline");
    expect(again.sub).not.toBe(frame.sub);
    return again;
  });
  act(() => socket.receive(snapshotFrame(retried.sub, PIPELINE)));
  await screen.findByText("PR CI");
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
});
