import { afterEach, expect, test } from "bun:test";
import { cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";

import type { DeliveryDailyPoint, DeliveryMeasuresResponse } from "../../api/types";
import { chartBar, dangerText, successText, textPrimaryOnSurface } from "../../theme/classes";
import { MeasuresPanel } from "./MeasuresPanel";

afterEach(cleanup);

/** Every UTC day of the fixture window, 2026-08-30 to 2026-09-27, with the reference request's
 *  per-day counts; the last day is partial (the window ends 20:00). */
function fixtureDaily(): DeliveryDailyPoint[] {
  const counts: Record<string, Partial<DeliveryDailyPoint>> = {
    "2026-09-05": { deploys: 1, concluded: 1, reached_production: 1, run_success_rate: 1 },
    "2026-09-06": { deploys: 1, concluded: 1, reached_production: 1, run_success_rate: 1 },
    "2026-09-09": { deploys: 1, concluded: 1, reached_production: 1, run_success_rate: 1 },
    "2026-09-11": { concluded: 1, run_success_rate: 0 },
    "2026-09-13": { deploys: 1, concluded: 1, reached_production: 1, run_success_rate: 1 },
    "2026-09-14": { concluded: 1, run_success_rate: 0 },
    "2026-09-17": { deploys: 1, concluded: 1, reached_production: 1, run_success_rate: 1 },
    "2026-09-19": { cancelled: 1 },
  };
  const days: DeliveryDailyPoint[] = [];
  for (let ms = Date.UTC(2026, 7, 30); ms <= Date.UTC(2026, 8, 27); ms += 86_400_000) {
    const day = new Date(ms).toISOString().slice(0, 10);
    days.push({
      day,
      partial: day === "2026-09-27",
      deploys: 0,
      concluded: 0,
      reached_production: 0,
      cancelled: 0,
      run_success_rate: null,
      ...counts[day],
    });
  }
  return days;
}

type Targets = DeliveryMeasuresResponse["targets"];

/** The six targets as the catalogue's evals hold them, which the answer states until a person
 *  changes one: each with its eval's direction and the statistics its verdict judges. */
const catalogueTargets: { [K in keyof Targets]: NonNullable<Targets[K]> } = {
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

/** GET /api/v1/delivery/measures for the fixture window, as the server answers it with no flags
 *  stored (the reference request recorded on the slice 2 pull request): no failure source exists,
 *  so the change failure rate's counts are 0 and its rates null, and neither of its verdicts is
 *  reached. */
const fixtureMeasures: DeliveryMeasuresResponse = {
  window: { from: "2026-08-30T00:00:00Z", to: "2026-09-27T20:00:00Z" },
  computed_at: "2026-10-09T04:33:22Z",
  measures: {
    deploy_frequency: {
      successful_deploys: 5,
      deploys_with_prs: 5,
      per_day: 0.17341040462427745,
      with_prs_per_day: 0.17341040462427745,
    },
    lead_time: {
      merge_to_production: { median_minutes: 510, p90_minutes: 2736, max_minutes: 3840 },
      first_commit_to_production: { median_minutes: 1380, p90_minutes: 2988, max_minutes: 4380 },
      opened_to_production: { median_minutes: 1260, p90_minutes: 2874, max_minutes: 4320 },
      opened_to_merge: { median_minutes: 330, p90_minutes: 462.00000000000006, max_minutes: 1080 },
    },
    change_failure_rate: {
      per_pr: { confirmed: 0, pending: 0, rejected: 0, reverts: 0, total: 0, rate: null },
      per_deploy: {
        confirmed: 0,
        pending: 0,
        rejected: 0,
        reverts: 0,
        total: 0,
        rate: null,
        confirmed_or_pending: 0,
        upper_bound_rate: null,
      },
    },
    time_to_restore: { median_minutes: null },
    rework_share: 0.2857142857142857,
    deploy_run_success: {
      concluded: 7,
      reached_production: 5,
      cancelled: 1,
      rate: 0.7142857142857143,
    },
    daily: fixtureDaily(),
  },
  flags_source: "none",
  unowned_p0: [{ key: "ACME-103", title: "Production deploy gate flakes" }],
  targets: catalogueTargets,
  status: {
    deploys_per_day: false,
    change_failure_rate: null,
    change_failure_rate_upper_bound: null,
    merge_to_production_median: false,
    merge_to_production: false,
    opened_to_merge: false,
    deploy_run_success: false,
    unowned_p0: false,
  },
  freshness: {
    last_event_at: null,
    last_reconcile_at: null,
    last_error: null,
    unfetchable_count: 0,
  },
};

function renderPanel(data: DeliveryMeasuresResponse | undefined) {
  render(
    <MemoryRouter>
      <MeasuresPanel data={data} />
    </MemoryRouter>
  );
}

function card(title: string): HTMLElement {
  const found = document.querySelector<HTMLElement>(`[data-kpi="${title}"]`);
  if (found === null) throw new Error(`no [data-kpi="${title}"] card`);
  return found;
}

/** The per-day chart of a card: its dashed target lines, and each day's drawn bar or stub in day
 *  order, the fixture's 29 days from 2026-08-30. */
function chart(title: string): { bars: (string | null)[]; lines: number } {
  const svg = within(card(title)).getByRole("img", { name: "per-day trend" });
  return {
    bars: Array.from(svg.querySelectorAll("rect"))
      .filter((rect) => rect.getAttribute("fill") !== "transparent")
      .map((rect) => rect.getAttribute("class")),
    lines: svg.querySelectorAll("line").length,
  };
}

test("the panel shows the six KPI cards, each with its badge, number and target", () => {
  renderPanel(fixtureMeasures);

  const grid = screen.getByLabelText("KPI targets");
  expect(grid.querySelectorAll("[data-kpi]")).toHaveLength(6);

  const deploys = within(card("Deploys a day"));
  expect(deploys.getByText("0.2/day")).toBeDefined();
  expect(deploys.getByText("5 deploys")).toBeDefined();
  expect(deploys.getByText("\u2717 missed")).toBeDefined();
  expect(deploys.getByText("target \u2265 20/day")).toBeDefined();
  expect(deploys.getByRole("img", { name: "per-day trend" })).toBeDefined();

  const cfr = within(card("Change failure rate"));
  expect(cfr.getByText("n/a")).toBeDefined();
  expect(cfr.getByText("no data")).toBeDefined();
  expect(cfr.queryByText("\u2713 met")).not.toBeInTheDocument();
  expect(cfr.queryByText("\u2717 missed")).not.toBeInTheDocument();
  expect(cfr.queryByText("confirmed")).not.toBeInTheDocument();
  expect(cfr.getByText("Flags arrive with flag confirmation")).toBeDefined();
  expect(cfr.queryByText(/if every pending flag is confirmed/)).not.toBeInTheDocument();
  expect(cfr.getByText("target < 5% per deploy")).toBeDefined();

  const merge = within(card("Merge \u2192 production"));
  expect(merge.getByText("8.5h")).toBeDefined();
  expect(merge.getByText("2.7d")).toBeDefined();
  // The catalogue judges the longest change and the median, and the longest change under the
  // target puts the median under it: the caption names the longest change alone, as before.
  expect(merge.getByText("target < 45m for every change")).toBeDefined();

  // The median is the judged statistic, so it takes the verdict's tone, and the p90 reads plain.
  const opened = within(card("PR open \u2192 merge"));
  expect(opened.getByText("5.5h").className).toContain(dangerText);
  expect(opened.getByText("7.7h").className).toContain(textPrimaryOnSurface);
  expect(opened.getByText("target median < 60m")).toBeDefined();

  const runs = within(card("Deploy runs reaching production"));
  expect(runs.getByText("71.4%")).toBeDefined();
  expect(runs.getByText("5 of 7 concluded")).toBeDefined();
  expect(runs.getByText("1 cancelled runs not counted")).toBeDefined();
  expect(runs.getByText("target \u2265 90%")).toBeDefined();

  const p0 = within(card("P0 issues with no owner"));
  expect(p0.getByText("1")).toBeDefined();
  const link = p0.getByRole("link", { name: "ACME-103" });
  expect(link).toHaveAttribute("href", "/issues/ACME-103");
  expect(link).toHaveAttribute("title", "Production deploy gate flakes");
  expect(p0.getByText(/^no claim or route, as of /)).toBeDefined();
  expect(p0.getByText("target 0")).toBeDefined();
});

test("met, missed and no-data badges follow the response's status", () => {
  renderPanel({
    ...fixtureMeasures,
    status: { ...fixtureMeasures.status, deploys_per_day: true, deploy_run_success: null },
  });
  expect(within(card("Deploys a day")).getByText("\u2713 met")).toBeDefined();
  expect(within(card("Deploy runs reaching production")).getByText("no data")).toBeDefined();
  expect(within(card("Change failure rate")).getByText("no data")).toBeDefined();
});

test("each target reads as its eval holds it: its sign follows inclusive, and one turned off reads off", () => {
  renderPanel({
    ...fixtureMeasures,
    targets: {
      ...catalogueTargets,
      deploys_per_day: { ...catalogueTargets.deploys_per_day, value: 4, inclusive: false },
      merge_to_production_minutes: {
        ...catalogueTargets.merge_to_production_minutes,
        inclusive: true,
      },
      deploy_run_success_rate: null,
    },
    status: { ...fixtureMeasures.status, deploy_run_success: null },
  });
  expect(within(card("Deploys a day")).getByText("target > 4/day")).toBeDefined();
  expect(
    within(card("Merge \u2192 production")).getByText("target \u2264 45m for every change")
  ).toBeDefined();
  // At most 0 unowned P0 issues is exactly 0, so the inclusive target reads as the value alone.
  expect(within(card("P0 issues with no owner")).getByText("target 0")).toBeDefined();
  const runs = within(card("Deploy runs reaching production"));
  expect(runs.getByText("target off")).toBeDefined();
  expect(runs.getByText("no data")).toBeDefined();
});

test("a judge another judge implies is neither named nor toned: PR open to merge judged by its longest change and its median", () => {
  renderPanel({
    ...fixtureMeasures,
    targets: {
      ...catalogueTargets,
      opened_to_merge_median_minutes: {
        ...catalogueTargets.opened_to_merge_median_minutes,
        judges: ["minutes.max", "minutes.median"],
      },
    },
  });
  const opened = within(card("PR open \u2192 merge"));
  expect(opened.getByText("target < 60m for every change")).toBeDefined();
  // The verdict, missed, is the longest change's, which the card shows no figure of: the median
  // it implies is not coloured by it.
  expect(opened.getByText("5.5h").className).toContain(textPrimaryOnSurface);
  expect(opened.getByText("5.5h").className).not.toContain(dangerText);
  cleanup();

  // Better higher nothing is implied, so both are named and the median takes the verdict's tone.
  renderPanel({
    ...fixtureMeasures,
    targets: {
      ...catalogueTargets,
      opened_to_merge_median_minutes: {
        ...catalogueTargets.opened_to_merge_median_minutes,
        direction: "higher_better",
        judges: ["minutes.max", "minutes.median"],
      },
    },
  });
  const higher = within(card("PR open \u2192 merge"));
  expect(higher.getByText("target longest and median > 60m")).toBeDefined();
  expect(higher.getByText("5.5h").className).toContain(dangerText);
});

// R5: a person may judge a delivery eval by any statistic the answer states, and the verdict
// follows the judges. The card names what the verdict is of, rather than the statistic the
// catalogue judges: "median" under a card whose badge judges the p90 would be a wrong caption.
test("a lead-time card names the statistics its target judges and tones those figures: PR open to merge judged by its p90", () => {
  renderPanel({
    ...fixtureMeasures,
    targets: {
      ...catalogueTargets,
      opened_to_merge_median_minutes: {
        value: 720,
        inclusive: true,
        direction: "lower_better",
        judges: ["minutes.p90"],
      },
      merge_to_production_minutes: {
        ...catalogueTargets.merge_to_production_minutes,
        judges: ["minutes.p90"],
      },
    },
    status: { ...fixtureMeasures.status, opened_to_merge: true },
  });
  const opened = within(card("PR open \u2192 merge"));
  expect(opened.getByText("target p90 \u2264 720m")).toBeDefined();
  expect(opened.queryByText(/^target median/)).not.toBeInTheDocument();
  // The verdict, met, is the p90's: the p90 takes its tone, and the median reads plain.
  expect(opened.getByText("7.7h").className).toContain(successText);
  expect(opened.getByText("5.5h").className).toContain(textPrimaryOnSurface);
  expect(opened.getByText("5.5h").className).not.toContain(successText);

  // Merge to production judged by a statistic the card shows no figure of: the caption names it,
  // and the longest change is no longer toned by a verdict that is not of it.
  const merge = within(card("Merge \u2192 production"));
  expect(merge.getByText("target p90 < 45m")).toBeDefined();
  expect(merge.getByText("2.7d").className).toContain(textPrimaryOnSurface);
  expect(merge.getByText("2.7d").className).not.toContain(dangerText);
});

// R5, and review v2's R2-3: a figure takes a verdict's tone only where the card's caption names the
// statistic that verdict is of. The answer also carries a verdict of the merge median and one of
// the failure rate's upper bound, each judged against the target whatever the person's judges
// (api/delivery_measures.go measuresStatus); where the judges leave that figure out, the caption
// does not name it, and the figure reads plain (dispatch://INFRA-2916, the testing round of
// 2026-11-18 at 0ea9989f, which drove the same answer from the server: judges-median-W.r10.out).
test("a figure the caption does not name is toned by no verdict: the merge median judged by its longest change, the upper bound judged by the per-PR rate", () => {
  renderPanel({
    ...fixtureMeasures,
    targets: {
      ...catalogueTargets,
      merge_to_production_minutes: {
        value: 1000,
        inclusive: false,
        direction: "lower_better",
        judges: ["minutes.max"],
      },
    },
    status: {
      ...fixtureMeasures.status,
      merge_to_production: false,
      merge_to_production_median: true,
    },
  });
  const merge = within(card("Merge \u2192 production"));
  expect(merge.getByText(/^target /).textContent ?? "").not.toContain("median");
  expect(merge.getByText("2.7d").className).toContain(dangerText);
  expect(merge.getByText("8.5h").className).toContain(textPrimaryOnSurface);
  expect(merge.getByText("8.5h").className).not.toContain(successText);
  cleanup();

  renderPanel({
    ...fixtureMeasures,
    flags_source: "flags",
    measures: {
      ...fixtureMeasures.measures,
      change_failure_rate: {
        per_pr: { confirmed: 0, pending: 0, rejected: 0, reverts: 0, total: 5, rate: 0 },
        per_deploy: {
          confirmed: 0,
          pending: 2,
          rejected: 0,
          reverts: 0,
          total: 5,
          rate: 0,
          confirmed_or_pending: 2,
          upper_bound_rate: 0.4,
        },
      },
    },
    targets: {
      ...catalogueTargets,
      change_failure_rate: {
        value: 0.05,
        inclusive: false,
        direction: "lower_better",
        judges: ["per_pr.rate"],
      },
    },
    status: {
      ...fixtureMeasures.status,
      change_failure_rate: true,
      change_failure_rate_upper_bound: false,
    },
  });
  const failures = within(card("Change failure rate"));
  expect(failures.getByText(/^target /).textContent ?? "").not.toContain("upper");
  expect(failures.getByText("40.00%").className).not.toContain(dangerText);
  expect(failures.getByText("40.00%").className).not.toContain(successText);
});

// R5: every figure a card tones by a verdict takes that verdict's tone only where the card's target
// judges the statistic the figure shows, as the lead-time figures and the upper bound above do. A
// card's headline figure is one statistic of its eval and its badge the verdict of every statistic
// the target judges: where a person judges another statistic, the badge follows that verdict and
// the headline figure reads plain. Under the catalogue's judges each headline figure is the
// statistic judged, and takes its verdict's tone as before.
test("a headline figure its target does not judge is toned by no verdict: the per-deploy rate judged by the per-PR rate, deploys a day judged by the deploys with a PR", () => {
  const failureRate: DeliveryMeasuresResponse["measures"]["change_failure_rate"] = {
    per_pr: { confirmed: 2, pending: 0, rejected: 0, reverts: 0, total: 5, rate: 0.4 },
    per_deploy: {
      confirmed: 1,
      pending: 1,
      rejected: 0,
      reverts: 0,
      total: 50,
      rate: 0.02,
      confirmed_or_pending: 2,
      upper_bound_rate: 0.04,
    },
  };
  renderPanel({
    ...fixtureMeasures,
    flags_source: "stored",
    measures: { ...fixtureMeasures.measures, change_failure_rate: failureRate },
    targets: {
      ...catalogueTargets,
      change_failure_rate: { ...catalogueTargets.change_failure_rate, judges: ["per_pr.rate"] },
      deploys_per_day: {
        ...catalogueTargets.deploys_per_day,
        judges: ["deploys.with_prs_per_day"],
      },
    },
    // The per-PR rate, 40%, misses under 5%, and 0.17 deploys with a PR a day miss at least 20.
    status: { ...fixtureMeasures.status, change_failure_rate: false, deploys_per_day: false },
  });
  const failures = within(card("Change failure rate"));
  expect(failures.getByText("\u2717 missed")).toBeDefined();
  expect(failures.getByText("2.00%").className).toContain(textPrimaryOnSurface);
  expect(failures.getByText("2.00%").className).not.toContain(dangerText);
  const deploys = within(card("Deploys a day"));
  expect(deploys.getByText("\u2717 missed")).toBeDefined();
  expect(deploys.getByText("0.2/day").className).toContain(textPrimaryOnSurface);
  expect(deploys.getByText("0.2/day").className).not.toContain(dangerText);
  cleanup();

  // The catalogue's judges: the per-deploy rate, 2%, meets under 5%, and each headline figure
  // takes its card's verdict's tone.
  renderPanel({
    ...fixtureMeasures,
    flags_source: "stored",
    measures: { ...fixtureMeasures.measures, change_failure_rate: failureRate },
    status: { ...fixtureMeasures.status, change_failure_rate: true },
  });
  expect(within(card("Change failure rate")).getByText("2.00%").className).toContain(successText);
  expect(within(card("Deploys a day")).getByText("0.2/day").className).toContain(dangerText);
  expect(within(card("Deploy runs reaching production")).getByText("71.4%").className).toContain(
    dangerText
  );
  expect(within(card("P0 issues with no owner")).getByText("1").className).toContain(dangerText);
});

test("a target is phrased and its days are judged in the direction it carries: the same value reads and colours the other way under lower_better", () => {
  const halfADay = { ...catalogueTargets.deploys_per_day, value: 0.5 };
  renderPanel({
    ...fixtureMeasures,
    targets: { ...catalogueTargets, deploys_per_day: halfADay },
  });
  expect(within(card("Deploys a day")).getByText("target \u2265 0.5/day")).toBeDefined();
  // 2026-09-04 deployed nothing and 2026-09-05 once: the sixth and seventh day of the window.
  expect(chart("Deploys a day").bars.slice(5, 7)).toEqual([chartBar.missed, chartBar.met]);
  cleanup();

  renderPanel({
    ...fixtureMeasures,
    targets: { ...catalogueTargets, deploys_per_day: { ...halfADay, direction: "lower_better" } },
  });
  expect(within(card("Deploys a day")).getByText("target \u2264 0.5/day")).toBeDefined();
  expect(chart("Deploys a day").bars.slice(5, 7)).toEqual([chartBar.met, chartBar.missed]);
});

test("a day's bar is coloured by that day's value of the judged statistic, and is neutral with no target line where the day states none", () => {
  renderPanel({
    ...fixtureMeasures,
    targets: {
      ...catalogueTargets,
      // Run success judged by its concluded runs, at least one a day.
      deploy_run_success_rate: {
        value: 1,
        inclusive: true,
        direction: "higher_better",
        judges: ["runs.concluded"],
      },
      // Deploys judged by those that shipped a pull request, which no day row states.
      deploys_per_day: { ...catalogueTargets.deploys_per_day, judges: ["deploys.with_prs"] },
    },
  });
  const runs = chart("Deploy runs reaching production");
  expect(
    within(card("Deploy runs reaching production")).getByText("target concluded runs \u2265 1")
  ).toBeDefined();
  // 2026-09-11, the thirteenth day: one run concluded and none reached production. Its success,
  // 0%, would miss any success target; its one concluded run meets "at least 1".
  expect(runs.bars[12]).toBe(chartBar.met);
  // The bars draw the success rate, which the target is not of: no line is drawn over them.
  expect(runs.lines).toBe(0);

  const deploys = chart("Deploys a day");
  expect(
    within(card("Deploys a day")).getByText("target deploys with a PR \u2265 20")
  ).toBeDefined();
  expect(new Set(deploys.bars)).toEqual(new Set([chartBar.neutral]));
  expect(deploys.lines).toBe(0);
  cleanup();

  // The catalogue's judges: each bar is its own figure's verdict, under its target line.
  renderPanel(fixtureMeasures);
  expect(chart("Deploy runs reaching production").bars[12]).toBe(chartBar.missed);
  expect(chart("Deploy runs reaching production").lines).toBe(1);
  expect(chart("Deploys a day").lines).toBe(1);
});

test("a lead-time card whose target is off reads off alone, with no verdict", () => {
  renderPanel({
    ...fixtureMeasures,
    targets: {
      ...fixtureMeasures.targets,
      merge_to_production_minutes: null,
      opened_to_merge_median_minutes: null,
    },
    status: {
      ...fixtureMeasures.status,
      merge_to_production: null,
      merge_to_production_median: null,
      opened_to_merge: null,
    },
  });
  for (const title of ["Merge \u2192 production", "PR open \u2192 merge"]) {
    const lead = within(card(title));
    expect(lead.getByText("target off")).toBeDefined();
    expect(lead.getByText("no data")).toBeDefined();
  }
});

test("with stored flags the change failure rate card shows the upper bound and the breakdown", () => {
  renderPanel({
    ...fixtureMeasures,
    flags_source: "stored",
    status: {
      ...fixtureMeasures.status,
      change_failure_rate: false,
      change_failure_rate_upper_bound: false,
    },
    measures: {
      ...fixtureMeasures.measures,
      change_failure_rate: {
        per_pr: { confirmed: 2, pending: 1, rejected: 1, reverts: 1, total: 14, rate: 2 / 14 },
        per_deploy: {
          confirmed: 2,
          pending: 0,
          rejected: 1,
          reverts: 1,
          total: 5,
          rate: 0.4,
          confirmed_or_pending: 2,
          upper_bound_rate: 0.4,
        },
      },
    },
  });
  const cfr = within(card("Change failure rate"));
  expect(cfr.getByText("40.00%", { selector: "span.text-lg" })).toBeDefined();
  expect(cfr.getByText("\u2717 missed")).toBeDefined();
  expect(cfr.getByText("confirmed")).toBeDefined();
  expect(
    cfr.getByText(
      (_, element) =>
        element?.tagName === "DIV" &&
        element.textContent === "up to 40.00% if every pending flag is confirmed"
    )
  ).toBeDefined();
  expect(
    cfr.getByText("2 deploys with a confirmed break; none with only unreviewed flags")
  ).toBeDefined();
  expect(cfr.queryByText("Flags arrive with flag confirmation")).not.toBeInTheDocument();
});

test("more than six unowned P0 issues list six and name the rest", () => {
  const unowned = Array.from({ length: 8 }, (_, i) => ({
    key: `ACME-${i + 1}`,
    title: `P0 ${i + 1}`,
  }));
  renderPanel({ ...fixtureMeasures, unowned_p0: unowned });
  const p0 = within(card("P0 issues with no owner"));
  expect(p0.getByText("8")).toBeDefined();
  expect(p0.getAllByRole("link")).toHaveLength(6);
  const more = p0.getByText("+2 more");
  expect(more).toHaveAttribute("title", "ACME-7, ACME-8");
});

test("the strip shows the five headline numbers", () => {
  renderPanel(fixtureMeasures);
  const strip = screen.getByTestId("measures-strip");
  for (const [value, label] of [
    ["0.17/day", "Deploy freq"],
    ["8.5h", "Lead time (merge\u2192prod)"],
    ["n/a", "CFR (per PR)"],
    ["n/a", "Time to restore"],
    ["28.6%", "Rework share"],
  ]) {
    const stat = within(strip).getByText(label).parentElement;
    expect(stat?.textContent).toBe(`${value}${label}`);
  }
});

test("the definitions fold opens to the definition cards and the per-day table, newest first", async () => {
  const user = userEvent.setup();
  renderPanel(fixtureMeasures);
  const toggle = screen.getByRole("button", { name: "Definitions & alternatives" });
  expect(toggle).toHaveAttribute("aria-expanded", "false");
  expect(screen.queryByText("Lead time (first commit \u2192 prod)")).not.toBeInTheDocument();
  expect(screen.queryByRole("table")).not.toBeInTheDocument();

  await user.click(toggle);

  expect(toggle).toHaveAttribute("aria-expanded", "true");
  expect(screen.getByText("Lead time (first commit \u2192 prod)")).toBeDefined();
  expect(screen.getByText("23.0h")).toBeDefined();
  const perPR = screen.getByText("Change failure rate (per PR)").parentElement;
  expect(perPR?.textContent).toBe(
    "Change failure rate (per PR)n/aNo failure source yet: flags arrive with flag confirmation. Incidents: no source yet."
  );
  const table = screen.getByRole("table");
  const rows = within(table).getAllByRole("row");
  // A header row, then every one of the window's 29 days.
  expect(rows).toHaveLength(30);
  expect(rows[1]?.textContent?.startsWith("2026-09-27 (partial)")).toBe(true);
  expect(rows[29]?.textContent?.startsWith("2026-08-30")).toBe(true);
  const sept5 = rows.find((row) => row.textContent?.startsWith("2026-09-05"));
  expect(Array.from(sept5?.querySelectorAll("td") ?? [], (cell) => cell.textContent)).toEqual([
    "2026-09-05",
    "1",
    "1",
    "1",
    "100.0%",
    "0",
  ]);
});

test("a pending read shows the six cards' frame with an ellipsis for each number", () => {
  renderPanel(undefined);
  const grid = screen.getByLabelText("KPI targets");
  const cards = grid.querySelectorAll("[data-kpi]");
  expect(cards).toHaveLength(6);
  for (const kpi of cards) {
    expect(within(kpi as HTMLElement).getAllByText("\u2026").length).toBeGreaterThan(0);
  }
});
