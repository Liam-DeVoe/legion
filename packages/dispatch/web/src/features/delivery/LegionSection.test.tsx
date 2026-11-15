import { afterEach, expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";

import { api } from "../../api/client";
import type {
  EvalItemRow,
  EvalListItem,
  EvalListResponse,
  EvalSeriesValues,
  LegionSetting,
} from "../../api/types";
import type { ChartRange } from "./Chart";
import { LegionSection, weekOf } from "./LegionSection";
import { DAY_MS, type WindowChoice } from "./lib/window";

/** Every spy a test installed, put back when it ends. */
const spies: { mockRestore(): void }[] = [];
afterEach(() => {
  cleanup();
  for (const spy of spies.splice(0)) spy.mockRestore();
});

const WINDOW = { from: "2026-10-06T00:00:00.000Z", to: "2026-11-03T00:00:00.000Z" };

function setting(project: string): LegionSetting {
  return {
    created_by: { kind: "user", id: "alice" },
    project,
    state_url: `http://legion-${project}.internal:13370`,
    updated_at: "2026-11-01T00:00:00Z",
  };
}

type Stats = Readonly<Record<string, number | null>>;

/** One listed series of an eval as the list read states it (`evalSeriesValues`), its window's
 *  statistics and its per-day array. */
function series(
  name: string,
  window: Stats,
  days: readonly { day: string; statistics: Stats }[] = [],
  dataSince: string | null = "2026-09-01"
): EvalSeriesValues {
  return {
    agg: "sum",
    change: null,
    compare: null,
    created_at: "2026-11-01T00:00:00Z",
    data_since: dataSince,
    days: days.map((day) => ({ day: day.day, statistics: { ...day.statistics } })),
    eval_id: name,
    is_primary: false,
    links: [],
    listed: true,
    min_n: null,
    role: "headline",
    series: name,
    unit: "hours",
    version: 1,
    via_repository: 0,
    window: { ...window },
  };
}

/** One eval of the cross-project read: its current definition, by project and slug, and its
 *  listed series. */
function evalOf(project: string, slug: string, listed: EvalSeriesValues[]): EvalListItem {
  return {
    definition: {
      created_at: "2026-11-01T00:00:00Z",
      decision: "",
      direction: "lower_better",
      doc_block: "",
      eval_id: `${project}/${slug}`,
      failure_modes: [],
      issue_key: "",
      kind: "legion",
      links: [],
      name: slug,
      number_rule: "",
      owner_role: "",
      person: "",
      project_key: project,
      question: "",
      replaced_by: "",
      slug,
      source: `computed:legion/metrics.${slug}`,
      state: "active",
      target: null,
      target_inclusive: false,
      target_judges: [],
      unit: "hours",
      version: 1,
      written_by: { kind: "user", id: "alice" },
      written_via: "seed",
    },
    id: `${project}/${slug}`,
    owner: { person: "", role: "", written_by: { kind: "user", id: "alice" } },
    series: listed,
    verdict: null,
  };
}

const ROLES = ["architect", "planner", "implementer", "tester", "reviewer", "merger", "no_role"];

/** A bar's statistics as legion-phase-time states them (`phaseTimeStatistics`). */
function bar(overrides: Stats = {}): Stats {
  return {
    ci_wait: 1,
    hours: 8,
    idle: 3,
    idle_share: 0.125,
    in_a_turn: 3,
    issues: 2,
    median_per_issue: 4,
    n: 2,
    waiting_on_children: 0,
    waiting_on_person: 1,
    ...overrides,
  };
}

/** The four Legion evals of one project the section draws, each with its series in every cut it
 *  is stored in: two issues finished, a root and its child. */
function legionEvals(project: string, bars: Readonly<Record<string, Stats>> = {}): EvalListItem[] {
  const classes = ["in_tree_at_design", "in_tree_later", "out_of_tree", "by_the_controller"];
  return [
    evalOf(project, "legion-work-time", [
      series("hours", { median: 24, n: 2, p90: 36.8 }),
      series("hours:roots", { median: 30, n: 1, p90: 30 }),
      series("hours:children", { median: 18, n: 1, p90: 18 }),
    ]),
    evalOf(
      project,
      "legion-phase-time",
      ROLES.flatMap((role) => [
        series(role, bars[role] ?? bar()),
        series(`${role}:roots`, bar({ hours: 5, idle_share: 0.2 })),
        series(`${role}:children`, bar({ hours: 3, idle_share: 0.1 })),
      ])
    ),
    evalOf(project, "legion-rework", [
      series("review_rounds", { median: 1, n: 2, p90: 1.8 }),
      series("sent_back", { n: 2, sent_back: 1, share: 0.5 }),
      series("hours_after_first_review", { hours: 4, median: 2, n: 2, p90: 3.6 }),
      series("review_rounds:roots", { median: 0, n: 1, p90: 0 }),
      series("review_rounds:children", { median: 2, n: 1, p90: 2 }),
    ]),
    evalOf(project, "legion-splitting", [
      ...classes.map((filing) =>
        series(`filed:${filing}`, { issues: filing === "in_tree_at_design" ? 1 : 0 })
      ),
      ...classes.map((filing) => series(`filed:${filing}:roots`, { issues: 0 })),
      ...classes.map((filing) =>
        series(`filed:${filing}:children`, { issues: filing === "in_tree_at_design" ? 1 : 0 })
      ),
      // 2026-10-27 and 2026-10-29 are a Tuesday and a Thursday of the week that begins on Monday
      // 2026-10-26, and 2026-11-02 is the Monday of the next one.
      series("finished", { issues: 3 }, [
        { day: "2026-10-27", statistics: { issues: 1 } },
        { day: "2026-10-29", statistics: { issues: 1 } },
        { day: "2026-11-02", statistics: { issues: 1 } },
      ]),
      series("finished:roots", { issues: 1 }),
      series("finished:children", { issues: 2 }),
      series("roots", { children: 1, children_per_root: 1, issues: 1 }),
      series("hours_with_children", { median: 30, n: 1, p90: 30 }),
      series("hours_without_children", { median: null, n: 0, p90: null }),
    ]),
  ];
}

/** One answer of `GET /api/v1/evals` over the section's window. */
function answer(items: EvalListItem[], total = items.length): EvalListResponse {
  return {
    compare: null,
    freshness: { refresher_age_seconds: 4, rows_updated_at: "2026-11-02T23:59:00Z" },
    items,
    limit: 200,
    offset: 0,
    total,
    window: { days: 28, from: "2026-10-06T00:00:00Z", to: "2026-11-03T00:00:00Z" },
  };
}

/** One issue a Legion eval counts, as its items route answers it (`legionfacts.ItemRows`). */
function issueRow(key: string, cols: Readonly<Record<string, number>>): EvalItemRow {
  return {
    cols: {
      implementing_after_first_review: 0,
      parked_hours: 0,
      queued_hours: 0,
      review_rounds: 0,
      testing_after_first_review: 0,
      title: `${key} title`,
      work_hours: 0,
      ...cols,
    },
    href: `/issues/${key}`,
    links: [],
    measured_at: "2026-11-02T00:00:00Z",
    range: "window",
    row_key: key,
    row_kind: "issue",
    source: "legionfacts",
  };
}

/** A reading someone logged under the eval's slug with row kind `issue`: the items route answers
 *  it beside the stored issues, and it carries none of their facts. */
const LOGGED: EvalItemRow = {
  cols: { id: "reading-1", series: "hours", value: 3 },
  href: "",
  links: [],
  measured_at: "2026-11-02T00:00:00Z",
  range: "window",
  row_key: "LOGGED-1",
  row_kind: "issue",
  source: "a-notebook",
};

/** The section as the page holds it: the page's window, and its one compare range, which starts
 *  as `initial` and which a chart's compare control names. */
function Page({ initial, window }: { initial: ChartRange | null; window: WindowChoice }) {
  const [compare, setCompare] = useState(initial);
  return <LegionSection compare={compare} onCompare={setCompare} window={window} />;
}

/** The section over `window` (the custom range of WINDOW, which the page already resolved, by
 *  default), and beside it the compare range `compare` names (none by default). */
async function renderSection(
  settings: LegionSetting[],
  evals: (query: {
    from: string;
    offset: number;
    projects: readonly string[];
    to: string;
  }) => EvalListResponse,
  {
    compare = null,
    items = (project) => [
      issueRow(`${project}-1`, { parked_hours: 2, queued_hours: 1, work_hours: 30 }),
      issueRow(`${project}-2`, {
        implementing_after_first_review: 1,
        review_rounds: 2,
        testing_after_first_review: 1,
        work_hours: 18,
      }),
      LOGGED,
    ],
    window = { kind: "custom", ...WINDOW },
  }: {
    compare?: ChartRange | null;
    items?: (project: string, slug: string) => EvalItemRow[];
    window?: WindowChoice;
  } = {}
) {
  const list = spyOn(api, "listLegionSettings").mockResolvedValue(settings);
  const listEvals = spyOn(api, "listEvals").mockImplementation((query) =>
    Promise.resolve(
      evals({
        from: query.from ?? "",
        offset: query.offset ?? 0,
        projects: query.project ?? [],
        to: query.to ?? "",
      })
    )
  );
  const listItems = spyOn(api, "listProjectEvalItems").mockImplementation((project, slug) => {
    const rows = items(project, slug);
    return Promise.resolve({
      compare: null,
      items: rows,
      limit: 200,
      offset: 0,
      total: rows.length,
      window: { days: 28, from: "2026-10-06T00:00:00Z", to: "2026-11-03T00:00:00Z" },
    });
  });
  spies.push(list, listEvals, listItems);
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <Page initial={compare} window={window} />
    </QueryClientProvider>
  );
  return { listEvals, listItems };
}

function block(project: string): HTMLElement {
  const found = document.querySelector(`[data-legion-project="${project}"]`);
  if (found === null) throw new Error(`no block of ${project}`);
  return found as HTMLElement;
}

function card(project: string, metric: string): HTMLElement {
  const found = block(project).querySelector(`[data-legion-metric="${metric}"]`);
  if (found === null) throw new Error(`no ${metric} card in ${project}'s block`);
  return found as HTMLElement;
}

function figure(project: string, metric: string, label: string): string {
  return card(project, metric).querySelector(`[data-legion-figure="${label}"]`)?.textContent ?? "";
}

/** The text of one cell of a chart's table: the row whose first cell reads `row`, in the column
 *  headed `header`. */
function cell(table: Element, row: string, header: string): string | null {
  const headers = Array.from(table.querySelectorAll("thead th")).map((th) => th.textContent);
  const column = headers.indexOf(header);
  if (column < 0) throw new Error(`the table has no column ${header}: ${headers.join(", ")}`);
  const line = Array.from(table.querySelectorAll("tbody tr")).find(
    (tr) => tr.querySelector("td")?.textContent === row
  );
  if (line === undefined) throw new Error(`the table has no row ${row}`);
  return line.querySelectorAll("td")[column]?.textContent ?? null;
}

/** The table of one chart, once the chart is drawn and its view control was switched to it. */
async function tableOf(label: string): Promise<Element> {
  const user = userEvent.setup();
  const view = await screen.findByRole("group", { name: `${label} view` });
  await user.click(within(view).getByRole("button", { name: "Table" }));
  return waitFor(() => {
    const found = document.querySelector(`[data-chart-table="${label}"]`);
    if (found === null) throw new Error(`no table of ${label}`);
    return found;
  });
}

const twoProjects = (query: { projects: readonly string[] }) =>
  answer(query.projects.flatMap((project) => legionEvals(project)));

test("one block for each project with a Legion setting, every one from one read across them", async () => {
  const { listEvals } = await renderSection([setting("ACME"), setting("WIDGET")], twoProjects);

  await waitFor(() => expect(document.querySelectorAll("[data-legion-project]")).toHaveLength(2));
  expect(block("ACME").textContent).toContain("ACME");
  expect(block("WIDGET").textContent).toContain("WIDGET");
  // The one read names every project, the Legion kind and the page's window: no project is
  // picked for the reader, and no other project is read.
  expect(listEvals).toHaveBeenCalledTimes(1);
  expect(listEvals).toHaveBeenCalledWith({
    from: WINDOW.from,
    kind: "legion",
    limit: 200,
    offset: 0,
    project: ["ACME", "WIDGET"],
    to: WINDOW.to,
  });
  // Each block's own numbers: WIDGET's are drawn although ACME is the first project.
  await waitFor(() => expect(figure("WIDGET", "work_time", "Issues finished")).toBe("2"));
});

test("each block draws the four metrics, each through the one chart component, and no scope toggle", async () => {
  await renderSection([setting("ACME"), setting("WIDGET")], twoProjects);

  await waitFor(() => expect(document.querySelectorAll("[data-legion-metric]")).toHaveLength(8));
  for (const project of ["ACME", "WIDGET"]) {
    for (const [metric, title] of [
      ["work_time", "Work time to merge"],
      ["phase_time", "Phase time by worker type"],
      ["rework", "Rework"],
      ["splitting", "Splitting and follow-ups"],
    ] as const) {
      const label = `${project}: ${title}`;
      await waitFor(() =>
        expect(card(project, metric).querySelector(`[data-chart="${label}"]`)).not.toBeNull()
      );
      // Each chart has the two controls every chart of a measure over a range has.
      expect(
        card(project, metric).querySelector(`select[data-chart-compare="${label}"]`)
      ).not.toBeNull();
      expect(screen.getByRole("group", { name: `${label} view` })).toBeDefined();
    }
  }
  // The cuts are series and figures beside every issue, so nothing picks one.
  expect(screen.queryByRole("group", { name: "Legion issues" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Roots" })).toBeNull();
  expect(screen.queryByLabelText("Legion project")).toBeNull();
});

test("each card's headline numbers are the answer's own, with the roots and the children cuts beside them", async () => {
  await renderSection([setting("ACME")], twoProjects);

  await waitFor(() => expect(figure("ACME", "work_time", "Median")).toBe("24.0h"));
  expect(figure("ACME", "work_time", "p90")).toBe("36.8h");
  expect(figure("ACME", "work_time", "Issues finished")).toBe("2");
  expect(figure("ACME", "work_time", "Median, roots")).toBe("30.0h");
  expect(figure("ACME", "work_time", "Median, children")).toBe("18.0h");
  expect(figure("ACME", "phase_time", "Idle share")).toBe("12.5%");
  expect(figure("ACME", "phase_time", "Idle share, roots")).toBe("20.0%");
  expect(figure("ACME", "phase_time", "Idle share, children")).toBe("10.0%");
  expect(figure("ACME", "rework", "Median review rounds")).toBe("1.0");
  expect(figure("ACME", "rework", "Sent back at least once")).toBe("50.0%");
  expect(figure("ACME", "rework", "Median hours after the first review")).toBe("120m");
  expect(figure("ACME", "rework", "Median review rounds, roots")).toBe("0.0");
  expect(figure("ACME", "rework", "Median review rounds, children")).toBe("2.0");
  expect(figure("ACME", "splitting", "Issues filed")).toBe("1");
  expect(figure("ACME", "splitting", "Issues filed, roots")).toBe("0");
  expect(figure("ACME", "splitting", "Issues filed, children")).toBe("1");
  expect(figure("ACME", "splitting", "Roots finished")).toBe("1");
  expect(figure("ACME", "splitting", "Children per root")).toBe("1.0");
  expect(figure("ACME", "splitting", "Median work time, roots with children")).toBe("30.0h");
  // No root without children finished: no value, never a zero.
  expect(figure("ACME", "splitting", "Median work time, roots without children")).toBe("\u2014");
});

test("the work-time and the rework lists are one read of the range's finished issues, and a logged reading is no issue of them", async () => {
  const { listItems } = await renderSection([setting("ACME")], twoProjects);

  const table = await tableOf("ACME: Work time to merge");
  // Over the section's range, the whole UTC days the evals answer folded, so a list holds the
  // issues the card's numbers count. legion-rework's items are the same finished issues as
  // legion-work-time's, so the one read serves both lists.
  expect(listItems).toHaveBeenCalledTimes(1);
  expect(listItems).toHaveBeenCalledWith("ACME", "legion-work-time", {
    ...WINDOW,
    limit: 200,
    offset: 0,
    row_kind: "issue",
    series: "hours",
  });
  // One row per issue, with its work time and what was taken out of it.
  const row = Array.from(table.querySelectorAll("tr")).find((line) =>
    line.textContent?.startsWith("ACME-1")
  );
  if (row === undefined) throw new Error("the table holds no ACME-1 row");
  expect(row.textContent).toContain("30.0h");
  expect(row.textContent).toContain("120m");
  expect(table.textContent).toContain("ACME-2");
  expect(table.textContent).not.toContain("LOGGED-1");
  // The rework list draws the same read's issues, by their review rounds.
  const rework = await tableOf("ACME: Rework");
  expect(cell(rework, "ACME-2", "Review rounds")).toBe("2");
  expect(cell(rework, "ACME-1", "Review rounds")).toBe("0");
  expect(rework.textContent).not.toContain("LOGGED-1");
  expect(listItems).toHaveBeenCalledTimes(1);
});

/** The whole UTC days [from, to) touches, as the eval routes answer them (`evalDaysOf` in
 *  `packages/envoy/internal/dispatch/api/evals.go`): from the midnight of from's day to the
 *  midnight after to's last instant. */
function daysOf(from: string, to: string): EvalListResponse["window"] {
  const first = Math.floor(Date.parse(from) / DAY_MS) * DAY_MS;
  const end = (Math.floor((Date.parse(to) - 1) / DAY_MS) + 1) * DAY_MS;
  return {
    days: (end - first) / DAY_MS,
    from: new Date(first).toISOString(),
    to: new Date(end).toISOString(),
  };
}

// The eval routes answer the whole UTC days a range touches, and the server refuses a range of
// more than 84 days (`maxDeliveryWindow`, api/delivery.go) on every read: the window's, a list of
// issues', and a compare range's. The 84 days before the present touch 85, so a preset is read as
// its own whole days, and no list or compare range is read over the 85 days an answer of those
// instants would name.
test("with the last 84 days, every read the section makes covers 84 whole UTC days, and so does the previous 84 days a chart offers", async () => {
  // Mid-afternoon, so the 84 days before the present start inside a day.
  spies.push(spyOn(Date, "now").mockReturnValue(Date.parse("2026-11-15T14:37:00Z")));
  const user = userEvent.setup();
  const { listEvals, listItems } = await renderSection(
    [setting("ACME")],
    (query) => ({ ...answer(legionEvals("ACME")), window: daysOf(query.from, query.to) }),
    { window: { kind: "preset", preset: "84d" } }
  );
  // The 84 UTC days ending with today, and the 84 before them, which share no day with them.
  const window = { from: "2026-08-24T00:00:00.000Z", to: "2026-11-16T00:00:00.000Z" };
  const previous = { from: "2026-06-01T00:00:00.000Z", to: window.from };
  // Every range read so far: the evals reads', then the lists of issues'.
  const ranges = () => [
    ...listEvals.mock.calls.map(([query]) => ({ from: query.from, to: query.to })),
    ...listItems.mock.calls.map(([, , query]) => ({ from: query.from, to: query.to })),
  ];

  // The window's evals and its list of issues are read over the same 84 days.
  await waitFor(() => expect(listItems).toHaveBeenCalledTimes(1));
  expect(ranges()).toEqual([window, window]);

  const picker = await screen.findByRole("combobox", {
    name: "ACME: Phase time by worker type comparison",
  });
  expect(picker.querySelector('option[value="preceding"]')?.textContent).toBe("Previous 84 days");
  await user.selectOptions(picker, "preceding");

  // The compare range's evals and list of issues are read over the 84 days before them.
  await waitFor(() => expect(listItems).toHaveBeenCalledTimes(2));
  expect(ranges()).toEqual([window, previous, window, previous]);
  for (const range of ranges()) {
    expect(Date.parse(range.to ?? "") - Date.parse(range.from ?? "")).toBeLessThanOrEqual(
      84 * DAY_MS
    );
  }
});

// The median of a bar runs over every finished issue, one the role never worked counting as
// zero, so a role most issues never reach reads 0 with its hours sitting in the bar beside it.
// What makes that readable is the count of issues the bar holds hours for, which the server
// sends as `issues`: without it on the card a median of 0 reads as a false number rather than
// as a population most issues are not in.
test("the phase-time table names how many issues each bar holds hours for, and each bar's hours in the two cuts", async () => {
  await renderSection([setting("ACME")], () =>
    answer(legionEvals("ACME", { architect: bar({ hours: 12, issues: 3, median_per_issue: 0 }) }))
  );

  await waitFor(() => expect(figure("ACME", "phase_time", "Idle share")).toBe("12.5%"));
  const table = await tableOf("ACME: Phase time by worker type");
  // One row per worker type, named as the card draws it, with a column per segment.
  expect(table.textContent).toContain("Worker type");
  expect(table.textContent).toContain("no role");
  for (const segment of ["Waiting on a person", "In a turn", "CI wait", "Idle"]) {
    expect(table.textContent).toContain(segment);
  }
  expect(table.textContent).toContain("Issues with hours");
  expect(table.textContent).toContain("Hours, roots");
  expect(table.textContent).toContain("Hours, children");
  // The architect's count of issues with hours, and its five hours in the roots cut.
  expect(cell(table, "architect", "Issues with hours")).toBe("3");
  expect(cell(table, "architect", "Hours, roots")).toBe("5.0h");
});

// A project the compare range's read holds no eval of has nothing to compare: its compare
// columns read no value, as a measure with no data does, and never the mark of a read still out.
test("a compare range whose read holds no eval of a project reads no value in that project's compare columns", async () => {
  const compare = { kind: "custom", from: "2026-09-08T00:00:00.000Z", to: WINDOW.from } as const;
  await renderSection(
    [setting("ACME")],
    (query) => (query.from === compare.from ? answer([]) : answer(legionEvals("ACME"))),
    { compare }
  );

  await waitFor(() => expect(figure("ACME", "phase_time", "Idle share")).toBe("12.5%"));
  const table = await tableOf("ACME: Phase time by worker type");
  await waitFor(() => expect(cell(table, "architect", "Issues with hours compare")).toBe("\u2014"));
  expect(cell(table, "architect", "Issues with hours")).toBe("2");
});

test("the splitting chart draws the filings and the finished issues by UTC week, as the card always has", async () => {
  await renderSection([setting("ACME")], twoProjects);

  await waitFor(() => expect(figure("ACME", "splitting", "Issues filed")).toBe("1"));
  const table = await tableOf("ACME: Splitting and follow-ups");
  expect(table.textContent).toContain("Week");
  const rows = Array.from(table.querySelectorAll("tbody tr")).map((line) =>
    Array.from(line.querySelectorAll("td")).map((cell) => cell.textContent)
  );
  // The Tuesday and the Thursday add up under their Monday, and the next Monday is a week of its
  // own. "Issues finished" is the fifth series, after the four classes.
  expect(rows.map((cells) => cells[0])).toEqual(["2026-10-26", "2026-11-02"]);
  expect(rows.map((cells) => cells[5])).toEqual(["2", "1"]);
});

test("a week begins on the UTC Monday, as the server's weekStart reads it", () => {
  expect(weekOf("2026-11-02")).toBe("2026-11-02");
  expect(weekOf("2026-11-01")).toBe("2026-10-26");
  expect(weekOf("2026-10-29")).toBe("2026-10-26");
  expect(weekOf("2026-10-26")).toBe("2026-10-26");
});

test("a window that starts before a measure's first day says from when it has data", async () => {
  await renderSection([setting("ACME")], () =>
    answer(
      legionEvals("ACME").map((item) =>
        item.definition.slug === "legion-work-time"
          ? {
              ...item,
              series: (item.series ?? []).map((one) => ({
                ...one,
                data_since: "2026-10-20",
                window: Object.fromEntries(Object.keys(one.window ?? {}).map((key) => [key, null])),
              })),
            }
          : item
      )
    )
  );

  await waitFor(() => expect(figure("ACME", "work_time", "Median")).toBe("\u2014"));
  expect(
    card("ACME", "work_time")
      .querySelector("[data-legion-since]")
      ?.getAttribute("data-legion-since")
  ).toBe("2026-10-20");
  expect(card("ACME", "rework").querySelector("[data-legion-since]")).toBeNull();
});

test("every page of the read across the projects is read, so no project's block is left without its numbers", async () => {
  const { listEvals } = await renderSection([setting("ACME"), setting("WIDGET")], (query) =>
    // A page holding ACME's evals alone, of the eight the read holds in all.
    query.offset === 0
      ? answer(legionEvals("ACME"), 8)
      : { ...answer(legionEvals("WIDGET"), 8), offset: query.offset }
  );

  await waitFor(() => expect(figure("WIDGET", "work_time", "Issues finished")).toBe("2"));
  expect(listEvals).toHaveBeenCalledTimes(2);
  expect(listEvals.mock.calls[1]?.[0].offset).toBe(4);
});

test("a deployment where no project names a daemon says so and reads no evals", async () => {
  const { listEvals } = await renderSection([], twoProjects);

  await waitFor(() =>
    expect(document.querySelector("[data-legion-metrics-unconfigured]")).not.toBeNull()
  );
  expect(document.querySelectorAll("[data-legion-project]")).toHaveLength(0);
  expect(listEvals).not.toHaveBeenCalled();
});
