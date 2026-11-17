import { expect, test } from "bun:test";
import type { DeliveryDailyPoint, DeliveryLeadTimeCell } from "../../../api/types";
import {
  dailyValue,
  dayMeets,
  judgeWord,
  leadTimeValue,
  meetsTarget,
  shownJudges,
  type Target,
  targetLabel,
  targetLine,
  targetWords,
} from "./target";

/** deploy-frequency's target as the catalogue seeds it, at least 20 deploys a day, with `over`. */
function deploys(over: Partial<NonNullable<Target>> = {}): NonNullable<Target> {
  return {
    value: 20,
    inclusive: true,
    direction: "higher_better",
    judges: ["deploys.per_day"],
    ...over,
  };
}

/** A lead-time target, under 45 minutes, judging `judges`, with `over`. */
function minutes(judges: string[], over: Partial<NonNullable<Target>> = {}): NonNullable<Target> {
  return { value: 45, inclusive: false, direction: "lower_better", judges, ...over };
}

test("targetLabel: the target's own direction phrases it, at least or above for higher_better, at most or under for lower_better, and off where the target is", () => {
  expect(targetLabel(deploys())).toBe("\u2265 20/day");
  expect(targetLabel(deploys({ inclusive: false }))).toBe("> 20/day");
  // The same value and judge under the other direction read the other way, with no argument.
  expect(targetLabel(deploys({ direction: "lower_better" }))).toBe("\u2264 20/day");
  expect(targetLabel(deploys({ direction: "lower_better", inclusive: false }))).toBe("< 20/day");
  expect(targetLabel(minutes(["minutes.median"], { inclusive: true }))).toBe("median \u2264 45m");
  expect(targetLabel(minutes(["minutes.median"]))).toBe("median < 45m");
  expect(targetLabel(null)).toBe("off");
});

test("targetWords: the same targets in words, as a chart's caption names them", () => {
  expect(targetWords(deploys())).toBe("at least 20/day");
  expect(targetWords(deploys({ inclusive: false }))).toBe("above 20/day");
  expect(targetWords(minutes(["minutes.median"], { inclusive: true }))).toBe("median at most 45m");
  expect(targetWords(minutes(["minutes.median"]))).toBe("median under 45m");
  expect(targetWords(minutes(["minutes.median"], { direction: "higher_better" }))).toBe(
    "median above 45m"
  );
  expect(targetWords(null)).toBe("off");
});

test("a caption names the statistics the target judges, in the order the server judges them, each value in its statistic's unit", () => {
  expect(targetLabel(minutes(["minutes.p90"], { value: 720, inclusive: true }))).toBe(
    "p90 \u2264 720m"
  );
  // Two statistics neither of which implies the other are both named.
  expect(targetLabel(minutes(["minutes.median", "minutes.p90"]))).toBe("median and p90 < 45m");
  expect(targetWords(minutes(["minutes.n", "minutes.max"]))).toBe("count and longest under 45");
  // A count is no duration, and a deploy with a PR is no deploy.
  expect(targetLabel(minutes(["minutes.n"], { value: 5, direction: "higher_better" }))).toBe(
    "count > 5"
  );
  expect(targetLabel(deploys({ judges: ["deploys.with_prs_per_day"] }))).toBe(
    "deploys with a PR \u2265 20/day"
  );
  expect(targetLabel(deploys({ judges: ["deploys.with_prs"] }))).toBe(
    "deploys with a PR \u2265 20"
  );
  expect(
    targetLabel(deploys({ value: 0.05, direction: "lower_better", judges: ["per_pr.rate"] }))
  ).toBe("failure rate \u2264 5% per PR");
});

test("a target judging its card's own figure alone names no statistic, and names it beside another", () => {
  expect(targetLabel(deploys({ value: 0.9, judges: ["runs.rate"] }))).toBe("\u2265 90%");
  expect(
    targetLabel(
      deploys({
        value: 0.05,
        inclusive: false,
        direction: "lower_better",
        judges: ["per_deploy.rate"],
      })
    )
  ).toBe("< 5% per deploy");
  expect(targetLabel(deploys({ judges: ["deploys.per_day", "deploys.with_prs_per_day"] }))).toBe(
    "deploys and deploys with a PR \u2265 20/day"
  );
});

test("a judge this page has no word for reads as the server wrote it, and a target with no judges as its value", () => {
  expect(targetLabel(deploys({ judges: ["latency.p99"] }))).toBe("latency.p99 \u2265 20");
  expect(targetLabel(deploys({ judges: null }))).toBe("\u2265 20");
  expect(shownJudges(deploys({ judges: null }))).toEqual([]);
  expect(shownJudges(null)).toEqual([]);
  expect(judgeWord("minutes.max")).toBe("Longest");
  expect(judgeWord("latency.p99")).toBe("Latency.p99");
});

// Under lower_better a series' longest value meeting the target means its median and p90 meet it,
// its missing it misses the target whatever they do, and the three have a value or none together:
// the server's verdict over [max, median] is its verdict over [max], so the page names the max.
test("a judged max implies the same series' median and p90 under lower_better, and nothing under higher_better", () => {
  const both = minutes(["minutes.max", "minutes.median"]);
  expect(shownJudges(both)).toEqual(["minutes.max"]);
  expect(
    shownJudges(minutes(["minutes.p90", "minutes.n", "minutes.max", "minutes.median"]))
  ).toEqual(["minutes.n", "minutes.max"]);
  // The answer's own list is the eval's whole list, and a day is judged by all of it.
  expect(both.judges).toEqual(["minutes.max", "minutes.median"]);
  // No max is judged, or the max is another series': nothing is implied.
  expect(shownJudges(minutes(["minutes.median", "minutes.p90"]))).toEqual([
    "minutes.median",
    "minutes.p90",
  ]);
  expect(shownJudges(minutes(["job:lint.max", "minutes.median"]))).toEqual([
    "job:lint.max",
    "minutes.median",
  ]);
  // Better higher, a longest value over the target says nothing of the median.
  const higher = minutes(["minutes.max", "minutes.median"], { direction: "higher_better" });
  expect(shownJudges(higher)).toEqual(["minutes.max", "minutes.median"]);
  expect(targetLabel(higher)).toBe("longest and median > 45m");

  // The caption and the dashed line follow the reduced list.
  expect(targetLabel(both)).toBe("< 45m for every change");
  expect(targetWords(both)).toBe("under 45m for every change");
  expect(
    targetLine(minutes(["minutes.max", "minutes.median"]), ["minutes.median"])
  ).toBeUndefined();
  expect(targetLine(both, ["minutes.max"])).toBe(45);
});

// The strings the page wrote at 3140ce34, before a target carried its judges, where
// MeasuresPanel.tsx and PipelinePage.tsx fixed each card's statistic by hand.
test("the catalogue's six targets read as the page wrote them before targets carried their judges", () => {
  const lower = { inclusive: false, direction: "lower_better" } as const;
  const merge = minutes(["minutes.max", "minutes.median"]);
  const opened = minutes(["minutes.median"], { value: 60 });
  expect([
    targetLabel(deploys()),
    targetLabel({ ...lower, value: 0.05, judges: ["per_deploy.rate"] }),
    targetLabel(merge),
    targetLabel(opened),
    targetLabel(deploys({ value: 0.9, judges: ["runs.rate"] })),
    targetLabel({ ...lower, value: 0, inclusive: true, judges: ["issues.value"] }),
  ]).toEqual([
    "\u2265 20/day",
    "< 5% per deploy",
    "< 45m for every change",
    "median < 60m",
    "\u2265 90%",
    "0",
  ]);
  // The Pipeline's two lead-time cards: `target … (the dashed line)` and one line a judge.
  expect([targetWords(merge), targetWords(opened)]).toEqual([
    "under 45m for every change",
    "median under 60m",
  ]);
  expect([shownJudges(merge), shownJudges(opened)].map((judges) => judges.map(judgeWord))).toEqual([
    ["Longest"],
    ["Median"],
  ]);
});

test("an inclusive target of 0 on an eval better lower reads as the value alone, since none goes below 0", () => {
  const unowned = deploys({ value: 0, direction: "lower_better", judges: ["issues.value"] });
  expect(targetLabel(unowned)).toBe("0");
  expect(targetWords(minutes(["minutes.median"], { value: 0, inclusive: true }))).toBe("median 0m");
  expect(targetLabel({ ...unowned, inclusive: false })).toBe("< 0");
  expect(targetLabel({ ...unowned, direction: "higher_better" })).toBe("\u2265 0");
});

test("meetsTarget: a value at an inclusive target meets it, at a strict one misses it, in the target's own direction, and an off target judges nothing", () => {
  expect(meetsTarget(20, deploys())).toBe(true);
  expect(meetsTarget(20, deploys({ inclusive: false }))).toBe(false);
  expect(meetsTarget(21, deploys({ inclusive: false }))).toBe(true);
  expect(meetsTarget(45, minutes(["minutes.median"], { inclusive: true }))).toBe(true);
  expect(meetsTarget(45, minutes(["minutes.median"]))).toBe(false);
  expect(meetsTarget(44.9, minutes(["minutes.median"]))).toBe(true);
  // One value against one target value: the direction alone decides which side meets it.
  expect(meetsTarget(10, deploys())).toBe(false);
  expect(meetsTarget(10, deploys({ direction: "lower_better" }))).toBe(true);
  expect(meetsTarget(1, null)).toBeNull();
});

test("dayMeets: a day is judged by its value of each judged statistic, missed where any misses, met where every one meets, and neutral where it states none", () => {
  const day = (values: Record<string, number | null>) => (judge: string) => values[judge] ?? null;
  const p90 = minutes(["minutes.p90"]);
  // The p90 is judged and the median is not: a median over the target changes nothing.
  expect(dayMeets(p90, day({ "minutes.p90": 44, "minutes.median": 90 }))).toBe(true);
  expect(dayMeets(p90, day({ "minutes.p90": 45, "minutes.median": 10 }))).toBe(false);
  expect(dayMeets(p90, day({ "minutes.median": 10 }))).toBeNull();

  const both = minutes(["minutes.max", "minutes.median"]);
  expect(dayMeets(both, day({ "minutes.max": 40, "minutes.median": 30 }))).toBe(true);
  expect(dayMeets(both, day({ "minutes.max": 52, "minutes.median": 30 }))).toBe(false);
  // One judge with no value: missed where the other misses, no verdict where it meets.
  expect(dayMeets(both, day({ "minutes.median": 50 }))).toBe(false);
  expect(dayMeets(both, day({ "minutes.median": 30 }))).toBeNull();

  expect(dayMeets(null, day({ "minutes.p90": 44 }))).toBeNull();
  expect(dayMeets(minutes([]), day({ "minutes.p90": 44 }))).toBeNull();
});

test("targetLine: the dashed line is the target's value where it judges a statistic the chart's axis measures, and none where it is off or judges another", () => {
  const axis = ["minutes.median", "minutes.p90", "minutes.max"];
  expect(targetLine(minutes(["minutes.p90"]), axis)).toBe(45);
  expect(targetLine(minutes(["minutes.n", "minutes.max"]), axis)).toBe(45);
  expect(targetLine(minutes(["minutes.n"]), axis)).toBeUndefined();
  expect(targetLine(null, axis)).toBeUndefined();
});

test("dailyValue and leadTimeValue read a day row's value of a judge, null where the row states none", () => {
  const daily: DeliveryDailyPoint = {
    day: "2026-09-05",
    partial: false,
    deploys: 3,
    concluded: 5,
    reached_production: 4,
    cancelled: 2,
    run_success_rate: 0.8,
  };
  expect(
    [
      "deploys.per_day",
      "deploys.deploys",
      "deploys.with_prs",
      "runs.rate",
      "runs.concluded",
      "runs.reached_production",
      "runs.cancelled",
      "per_deploy.rate",
    ].map((judge) => dailyValue(daily, judge))
  ).toEqual([3, 3, null, 0.8, 5, 4, 2, null]);
  expect(dailyValue({ ...daily, run_success_rate: null }, "runs.rate")).toBeNull();

  const cell: DeliveryLeadTimeCell = {
    n: 2,
    median_minutes: 45,
    p90_minutes: 50.6,
    max_minutes: 52,
  };
  expect(
    ["minutes.n", "minutes.median", "minutes.p90", "minutes.max", "runs.rate"].map((judge) =>
      leadTimeValue(cell, judge)
    )
  ).toEqual([2, 45, 50.6, 52, null]);
});
