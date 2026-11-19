import { expect, test } from "bun:test";
import type { DeliveryLeadTimeCell } from "../../../api/types";
import {
  DEPLOY_BARS,
  type Judge,
  judgesStatistic,
  judgeWord,
  LEAD_TIME_BARS,
  leadTimeValue,
  meetsTarget,
  ownVerdict,
  RUN_SUCCESS_BARS,
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
function minutes(judges: Judge[], over: Partial<NonNullable<Target>> = {}): NonNullable<Target> {
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
  // A server newer than the page's contract can judge a statistic the contract does not name.
  const newer = "latency.p99" as string as Judge;
  expect(targetLabel(deploys({ judges: [newer] }))).toBe("latency.p99 \u2265 20");
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
  // The answer's own list is the eval's whole list, and every figure is judged by all of it.
  expect(both.judges).toEqual(["minutes.max", "minutes.median"]);
  expect(judgesStatistic(both, "minutes.median")).toBe(true);
  // No max is judged, or the max is another series': nothing is implied.
  expect(shownJudges(minutes(["minutes.median", "minutes.p90"]))).toEqual([
    "minutes.median",
    "minutes.p90",
  ]);
  // Another series' max, which the contract names for no delivery target: the rule reads the
  // series a judge is of, not its statistic alone.
  const otherMax = "job:lint.max" as string as Judge;
  expect(shownJudges(minutes([otherMax, "minutes.median"]))).toEqual([otherMax, "minutes.median"]);
  // Better higher, a longest value over the target says nothing of the median.
  const higher = minutes(["minutes.max", "minutes.median"], { direction: "higher_better" });
  expect(shownJudges(higher)).toEqual(["minutes.max", "minutes.median"]);
  expect(targetLabel(higher)).toBe("longest and median > 45m");

  // The caption follows the reduced list; the dashed line, as every tone, the whole list: the
  // median bars a target of the longest change and the median judges carry its line.
  expect(targetLabel(both)).toBe("< 45m for every change");
  expect(targetWords(both)).toBe("under 45m for every change");
  expect(targetLine(both, ["minutes.median"])).toBe(45);
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

// R5 (1): an eval with no target, which a person turned off, judges nothing.
test("an off target judges no statistic: no figure, day or bar takes a verdict of it, and no chart draws its line", () => {
  for (const statistic of ["deploys.per_day", "minutes.median", "minutes.n", "issues.value"]) {
    expect(judgesStatistic(null, statistic)).toBe(false);
    expect(ownVerdict(null, statistic, "window", 1)).toBeNull();
    expect(ownVerdict(null, statistic, "day", 1)).toBeNull();
  }
  expect(
    [DEPLOY_BARS, RUN_SUCCESS_BARS, LEAD_TIME_BARS].map((drawn) => targetLine(null, drawn))
  ).toEqual([undefined, undefined, undefined]);
  // A target with no judges, as a server older than them answers, judges nothing either.
  expect(ownVerdict(deploys({ judges: null }), "deploys.per_day", "window", 25)).toBeNull();
  expect(targetLine(deploys({ judges: null }), DEPLOY_BARS)).toBeUndefined();
});

// R5 (3): a figure takes a verdict only where the target's whole list of judges names its
// statistic, and then of its own value; a statistic no judge names takes none.
test("ownVerdict: a figure is judged by its own value where the target's whole list of judges names its statistic, and by nothing where no judge names it or it has no value", () => {
  const both = minutes(["minutes.max", "minutes.median"]);
  // The caption names the longest change alone; the median is judged all the same, by its own
  // value, whatever the longest change does.
  expect(shownJudges(both)).toEqual(["minutes.max"]);
  expect(ownVerdict(both, "minutes.median", "window", 30)).toBe(true);
  expect(ownVerdict(both, "minutes.median", "window", 45)).toBe(false);
  expect(ownVerdict(both, "minutes.max", "window", 52)).toBe(false);
  expect(ownVerdict(both, "minutes.max", "window", 40)).toBe(true);
  // No judge names the p90: no verdict, under the target or over it.
  expect(judgesStatistic(both, "minutes.p90")).toBe(false);
  expect(ownVerdict(both, "minutes.p90", "window", 10)).toBeNull();
  expect(ownVerdict(both, "minutes.p90", "day", 90)).toBeNull();
  expect(ownVerdict(both, "minutes.median", "window", null)).toBeNull();
  // In the target's own direction and inclusiveness.
  expect(ownVerdict(deploys(), "deploys.per_day", "window", 20)).toBe(true);
  expect(ownVerdict(deploys({ inclusive: false }), "deploys.per_day", "window", 20)).toBe(false);
  expect(ownVerdict(deploys({ direction: "lower_better" }), "deploys.per_day", "day", 25)).toBe(
    false
  );
});

// R5 (4): a day's value is judged only of a rate or a quantile of that day's samples; a count or
// a sum over the window judges the window's total, which no day is a share of.
test("ownVerdict: a day is judged only by a rate or a quantile of its samples, and a count or a sum only over the window", () => {
  const rates: [Judge, NonNullable<Target>, number, boolean][] = [
    ["deploys.per_day", deploys({ value: 1 }), 0, false],
    ["runs.rate", deploys({ value: 0.9, judges: ["runs.rate"] }), 1, true],
    ["minutes.median", minutes(["minutes.median"]), 30, true],
    ["minutes.p90", minutes(["minutes.p90"]), 50, false],
    ["minutes.max", minutes(["minutes.max"]), 30, true],
  ];
  for (const [statistic, target, value, met] of rates) {
    expect([statistic, ownVerdict(target, statistic, "day", value)]).toEqual([statistic, met]);
    expect([statistic, ownVerdict(target, statistic, "window", value)]).toEqual([statistic, met]);
  }
  const counts: [Judge, NonNullable<Target>, number, boolean][] = [
    ["deploys.deploys", deploys({ value: 1, judges: ["deploys.deploys"] }), 0, false],
    ["runs.concluded", deploys({ value: 2, judges: ["runs.concluded"] }), 3, true],
    [
      "runs.reached_production",
      deploys({ value: 2, judges: ["runs.reached_production"] }),
      1,
      false,
    ],
    ["runs.cancelled", minutes(["runs.cancelled"], { value: 2 }), 1, true],
    ["minutes.n", minutes(["minutes.n"], { value: 2, direction: "higher_better" }), 1, false],
    ["per_deploy.confirmed", minutes(["per_deploy.confirmed"], { value: 1 }), 0, true],
  ];
  for (const [statistic, target, value, met] of counts) {
    expect([statistic, ownVerdict(target, statistic, "day", value)]).toEqual([statistic, null]);
    expect([statistic, ownVerdict(target, statistic, "window", value)]).toEqual([statistic, met]);
  }
});

// R5 (5): a chart draws its dashed line exactly where its bars take a tone.
test("targetLine: a chart draws the target's value as its line exactly where its bars take a tone, where the target judges a day's statistic its bars draw", () => {
  expect(targetLine(deploys(), DEPLOY_BARS)).toBe(20);
  expect(targetLine(deploys({ judges: ["deploys.per_day", "deploys.deploys"] }), DEPLOY_BARS)).toBe(
    20
  );
  // The window's count of deploys and its deploys with a PR a day: no bar draws either.
  expect(targetLine(deploys({ judges: ["deploys.deploys"] }), DEPLOY_BARS)).toBeUndefined();
  expect(
    targetLine(deploys({ judges: ["deploys.with_prs_per_day"] }), DEPLOY_BARS)
  ).toBeUndefined();
  expect(targetLine(deploys({ value: 0.9, judges: ["runs.rate"] }), RUN_SUCCESS_BARS)).toBe(0.9);
  expect(targetLine(deploys({ judges: ["runs.concluded"] }), RUN_SUCCESS_BARS)).toBeUndefined();
  // A lead time's bars draw its median and its p90, and no bar its longest change or its count.
  expect(targetLine(minutes(["minutes.max", "minutes.median"]), LEAD_TIME_BARS)).toBe(45);
  expect(targetLine(minutes(["minutes.p90"]), LEAD_TIME_BARS)).toBe(45);
  expect(targetLine(minutes(["minutes.max"]), LEAD_TIME_BARS)).toBeUndefined();
  expect(targetLine(minutes(["minutes.n"]), LEAD_TIME_BARS)).toBeUndefined();
  expect(targetLine(minutes(["minutes.n", "minutes.max"]), LEAD_TIME_BARS)).toBeUndefined();
  // A judge the charts' list names that no day is judged of draws no line either.
  expect(targetLine(minutes(["minutes.n"]), ["minutes.n"])).toBeUndefined();
});

test("leadTimeValue reads a lead-time cell's value of a judge, null where the cell states none", () => {
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
