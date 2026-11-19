import { afterEach, expect, test } from "bun:test";
import { cleanup, render, screen } from "@testing-library/react";

import { chartBar } from "../../theme/classes";
import type { Judge, Target } from "./lib/target";
import { type SparkbarPoint, Sparkbars } from "./Sparkbars";
import { type SpreadBarDay, SpreadBars } from "./SpreadBars";

afterEach(cleanup);

/** The drawn bars of a chart: every `<rect>` but the transparent hover target each day carries. */
function bars(svg: Element): Element[] {
  return Array.from(svg.querySelectorAll("rect")).filter(
    (rect) => rect.getAttribute("fill") !== "transparent"
  );
}

function titles(svg: Element): string[] {
  return Array.from(svg.querySelectorAll("title")).map((title) => title.textContent ?? "");
}

/** What each drawn bar names: its eval, its statistic and its span. */
function named(svg: Element): (string | null)[][] {
  return bars(svg).map((bar) => [
    bar.getAttribute("data-eval"),
    bar.getAttribute("data-statistic"),
    bar.getAttribute("data-span"),
  ]);
}

const asNumber = (value: number) => `${value}/day`;

/** A target of `value`, at least it unless `over` says otherwise, judging `judges`. */
function target(
  value: number,
  judges: Judge[],
  over: Partial<NonNullable<Target>> = {}
): NonNullable<Target> {
  return { value, inclusive: true, direction: "higher_better", judges, ...over };
}

/** The deploys chart judged by deploy-frequency's target, at least 20 deploys a day. */
const deploysJudged = {
  evalSlug: "deploy-frequency",
  statistic: "deploys.per_day",
  target: target(20, ["deploys.per_day"]),
} as const;

const points: SparkbarPoint[] = [
  { label: "2026-09-25", value: 25, partial: false },
  { label: "2026-09-26", value: 10, partial: false },
  { label: "2026-09-27", value: null, partial: true },
];

test("Sparkbars draws a bar per day and one dashed target line", () => {
  render(<Sparkbars format={asNumber} judged={deploysJudged} max={0} points={points} />);
  const svg = screen.getByRole("img", { name: "per-day trend" });

  expect(svg.getAttribute("viewBox")).toBe("0 0 30 28");
  expect(svg.getAttribute("preserveAspectRatio")).toBe("none");
  expect(bars(svg)).toHaveLength(3);
  const lines = svg.querySelectorAll("line");
  expect(lines).toHaveLength(1);
  const line = lines[0];
  expect(line?.getAttribute("class")).toBe(chartBar.targetLine);
  expect(line?.getAttribute("stroke-dasharray")).toBe("3 2");
  // The scale's top is the largest of max, 1.25 x the target and the tallest bar: 25 here.
  expect(Number(line?.getAttribute("y1"))).toBeCloseTo(28 - (20 / 25) * 28);
});

test("Sparkbars colours a day whose value meets the target met and one whose value misses it missed", () => {
  render(<Sparkbars format={asNumber} judged={deploysJudged} max={0} points={points} />);
  const [meets, misses] = bars(screen.getByRole("img", { name: "per-day trend" }));

  expect(meets?.getAttribute("class")).toBe(chartBar.met);
  expect(meets?.getAttribute("height")).toBe("28");
  expect(misses?.getAttribute("class")).toBe(chartBar.missed);
  expect(Number(misses?.getAttribute("height"))).toBeCloseTo((10 / 25) * 28);
});

test("Sparkbars draws a one-pixel stub on a day with nothing to measure", () => {
  render(<Sparkbars format={asNumber} judged={deploysJudged} max={0} points={points} />);
  const svg = screen.getByRole("img", { name: "per-day trend" });
  const stub = bars(svg)[2];

  expect(stub?.getAttribute("class")).toBe(chartBar.empty);
  expect(stub?.getAttribute("height")).toBe("1");
  expect(stub?.getAttribute("y")).toBe("27");
  expect(titles(svg)).toEqual([
    "2026-09-25: 25/day",
    "2026-09-26: 10/day",
    "2026-09-27 (partial day): no runs",
  ]);
  expect(svg.querySelectorAll("g")[2]?.getAttribute("opacity")).toBe("0.4");
});

// R3-Q1: the chart judges each bar itself, from the target and the statistic it draws, in the
// target's own direction, rather than by where the bar stands against the line.
test("Sparkbars judges each bar by its own value in its target's direction: under 'at most 20' a tall bar misses and a short one meets, and each bar names its eval and statistic", () => {
  render(
    <Sparkbars
      format={asNumber}
      judged={{
        ...deploysJudged,
        target: target(20, ["deploys.per_day"], { direction: "lower_better" }),
      }}
      max={0}
      points={[
        { label: "2026-09-25", value: 25, partial: false },
        { label: "2026-09-26", value: 10, partial: false },
        { label: "2026-09-27", value: 20, partial: false },
      ]}
    />
  );
  const svg = screen.getByRole("img", { name: "per-day trend" });

  expect(bars(svg).map((bar) => bar.getAttribute("class"))).toEqual([
    chartBar.missed,
    chartBar.met,
    chartBar.met,
  ]);
  expect(named(svg)).toEqual([
    ["deploy-frequency", "deploys.per_day", "day"],
    ["deploy-frequency", "deploys.per_day", "day"],
    ["deploy-frequency", "deploys.per_day", "day"],
  ]);
});

test("Sparkbars no eval judges, or judged by a target of another statistic or none, draws every bar neutral and no line", () => {
  render(<Sparkbars format={asNumber} max={30} points={points.slice(0, 2)} />);
  const svg = screen.getByRole("img", { name: "per-day trend" });

  expect(bars(svg).map((bar) => bar.getAttribute("class"))).toEqual([
    chartBar.neutral,
    chartBar.neutral,
  ]);
  expect(svg.querySelectorAll("line")).toHaveLength(0);
  // With no target the top of the scale is max (30), above the tallest bar (25).
  expect(Number(bars(svg)[0]?.getAttribute("height"))).toBeCloseTo((25 / 30) * 28);
  // A chart no eval judges names no statistic.
  expect(svg.querySelectorAll("[data-statistic]")).toHaveLength(0);
  cleanup();

  // The window's count of deploys judges no day, and an off target judges nothing.
  for (const judging of [target(20, ["deploys.deploys"]), null]) {
    render(
      <Sparkbars
        format={asNumber}
        judged={{ ...deploysJudged, target: judging }}
        points={points.slice(0, 2)}
      />
    );
    const judged = screen.getByRole("img", { name: "per-day trend" });
    expect(bars(judged).map((bar) => bar.getAttribute("class"))).toEqual([
      chartBar.neutral,
      chartBar.neutral,
    ]);
    expect(judged.querySelectorAll("line")).toHaveLength(0);
    cleanup();
  }
});

test("Sparkbars with no max scales a judged chart to its target line, and one with no line to 1 or its tallest bar", () => {
  const low: SparkbarPoint[] = [{ label: "2026-09-25", value: 0.5, partial: false }];
  render(
    <Sparkbars
      format={asNumber}
      judged={{ ...deploysJudged, target: target(0.4, ["deploys.per_day"]) }}
      points={low}
    />
  );
  // The top is the line, 0.4, raised to 1.25 x it, 0.5, which the bar reaches.
  expect(bars(screen.getByRole("img", { name: "per-day trend" }))[0]?.getAttribute("height")).toBe(
    "28"
  );
  cleanup();

  render(<Sparkbars format={asNumber} points={low} />);
  // No line: the top is 1, which the bar reaches half of.
  expect(
    Number(bars(screen.getByRole("img", { name: "per-day trend" }))[0]?.getAttribute("height"))
  ).toBeCloseTo(14);
});

const asMinutes = (minutes: number) => `${minutes}m`;

/** A full day with `median` and `p90` minutes, which a test dates. */
function dayOf(median: number, p90: number): SpreadBarDay {
  return { day: "", n: 3, median_minutes: median, p90_minutes: p90, partial: false };
}

/** A lead-time chart judged by merge-to-production's target, under 45m, of `judges`. */
function leadJudged(
  judges: Judge[],
  over: Partial<NonNullable<Target>> = {}
): { evalSlug: string; target: Target } {
  return {
    evalSlug: "merge-to-production",
    target: target(45, judges, { inclusive: false, direction: "lower_better", ...over }),
  };
}

const days: SpreadBarDay[] = [
  { day: "2026-09-25", n: 4, median_minutes: 30, p90_minutes: 40, partial: false },
  { day: "2026-09-26", n: 3, median_minutes: 50, p90_minutes: 80, partial: false },
  { day: "2026-09-27", n: 0, median_minutes: null, p90_minutes: null, partial: true },
];

/** The median and the p90 under 45m. */
const bothUnder = leadJudged(["minutes.median", "minutes.p90"]);

test("SpreadBars draws the p90 behind the median each day and one dashed target line", () => {
  render(<SpreadBars days={days} format={asMinutes} judged={bothUnder} noun="runs" />);
  const svg = screen.getByRole("img", { name: "per-day median and p90" });

  expect(svg.getAttribute("viewBox")).toBe("0 0 36 36");
  expect(svg.getAttribute("preserveAspectRatio")).toBe("none");
  // Two bars on each measured day and the stub on the empty one.
  expect(bars(svg)).toHaveLength(5);
  const [p90, median] = bars(svg);
  // Drawn later is drawn in front: the p90 first, the median over it.
  expect(Number(p90?.getAttribute("height"))).toBeCloseTo((40 / 80) * 36);
  expect(Number(median?.getAttribute("height"))).toBeCloseTo((30 / 80) * 36);
  const lines = svg.querySelectorAll("line");
  expect(lines).toHaveLength(1);
  expect(lines[0]?.getAttribute("class")).toBe(chartBar.targetLine);
  expect(Number(lines[0]?.getAttribute("y1"))).toBeCloseTo(36 - (45 / 80) * 36);
});

test("SpreadBars colours each bar met where its own value meets the target and missed where it misses it", () => {
  render(<SpreadBars days={days} format={asMinutes} judged={bothUnder} noun="runs" />);
  const [p90Under, medianUnder, p90Over, medianOver] = bars(
    screen.getByRole("img", { name: "per-day median and p90" })
  );

  expect(p90Under?.getAttribute("class")).toBe(chartBar.metFaint);
  expect(medianUnder?.getAttribute("class")).toBe(chartBar.met);
  expect(p90Over?.getAttribute("class")).toBe(chartBar.missedFaint);
  expect(medianOver?.getAttribute("class")).toBe(chartBar.missed);
});

// R3-Q1: the chart judges each bar itself, in the target's own direction and inclusiveness,
// rather than by where the bar stands against the line.
test("SpreadBars judges each bar in its target's direction: under 'above 45m' bars under the line miss, bars over it meet, and bars at a strict target miss", () => {
  render(
    <SpreadBars
      days={[
        { ...dayOf(30, 40), day: "2026-09-25" },
        { ...dayOf(50, 80), day: "2026-09-26" },
        { ...dayOf(45, 45), day: "2026-09-27" },
      ]}
      format={asMinutes}
      judged={leadJudged(["minutes.median", "minutes.p90"], { direction: "higher_better" })}
      noun="runs"
    />
  );

  expect(
    bars(screen.getByRole("img", { name: "per-day median and p90" })).map((bar) =>
      bar.getAttribute("class")
    )
  ).toEqual([
    chartBar.missedFaint,
    chartBar.missed,
    chartBar.metFaint,
    chartBar.met,
    chartBar.missedFaint,
    chartBar.missed,
  ]);
});

// R5: a target may judge a day's median and not its p90, or judge both and see one meet and the
// other miss, so each of a day's two bars takes its own verdict.
test("SpreadBars gives each of a day's two bars its own verdict: a neutral p90 beside a met median, a missed p90 beside a met median", () => {
  const apart = [
    { ...dayOf(30, 40), day: "2026-09-25" },
    { ...dayOf(30, 50), day: "2026-09-26" },
  ];
  render(
    <SpreadBars
      days={apart}
      format={asMinutes}
      judged={leadJudged(["minutes.median"])}
      noun="PRs"
    />
  );
  const medianOnly = screen.getByRole("img", { name: "per-day median and p90" });
  expect(bars(medianOnly).map((bar) => bar.getAttribute("class"))).toEqual([
    chartBar.neutralFaint,
    chartBar.met,
    chartBar.neutralFaint,
    chartBar.met,
  ]);
  // Each bar names the eval whose target may judge it and the statistic it draws, a day's value
  // of it.
  expect(named(medianOnly)).toEqual([
    ["merge-to-production", "minutes.p90", "day"],
    ["merge-to-production", "minutes.median", "day"],
    ["merge-to-production", "minutes.p90", "day"],
    ["merge-to-production", "minutes.median", "day"],
  ]);
  cleanup();

  render(<SpreadBars days={apart} format={asMinutes} judged={bothUnder} noun="PRs" />);
  expect(
    bars(screen.getByRole("img", { name: "per-day median and p90" })).map((bar) =>
      bar.getAttribute("class")
    )
  ).toEqual([chartBar.metFaint, chartBar.met, chartBar.missedFaint, chartBar.met]);
});

test("SpreadBars draws a stub on a day with nothing to measure, naming the noun and today", () => {
  render(<SpreadBars days={days} format={asMinutes} judged={bothUnder} noun="runs" />);
  const svg = screen.getByRole("img", { name: "per-day median and p90" });
  const stub = bars(svg)[4];

  expect(stub?.getAttribute("class")).toBe(chartBar.empty);
  expect(stub?.getAttribute("height")).toBe("1");
  expect(stub?.getAttribute("y")).toBe("35");
  expect(titles(svg)).toEqual([
    "2026-09-25: median 30m, p90 40m, 4 runs",
    "2026-09-26: median 50m, p90 80m, 3 runs",
    "2026-09-27 (today so far): no runs",
  ]);
  expect(svg.querySelectorAll("g")[2]?.getAttribute("opacity")).toBe("0.55");
});

test("SpreadBars no eval judges, or judged by a target of a statistic no bar draws, draws the neutral pair and no line", () => {
  render(<SpreadBars days={days.slice(0, 1)} format={asMinutes} noun="merges" />);
  const svg = screen.getByRole("img", { name: "per-day median and p90" });

  expect(bars(svg).map((bar) => bar.getAttribute("class"))).toEqual([
    chartBar.neutralFaint,
    chartBar.neutral,
  ]);
  expect(svg.querySelectorAll("line")).toHaveLength(0);
  expect(titles(svg)).toEqual(["2026-09-25: median 30m, p90 40m, 4 merges"]);
  // A chart no eval judges names no statistic.
  expect(svg.querySelectorAll("[data-statistic]")).toHaveLength(0);
  cleanup();

  // The longest change, which no bar draws.
  render(
    <SpreadBars
      days={days.slice(0, 1)}
      format={asMinutes}
      judged={leadJudged(["minutes.max"])}
      noun="merges"
    />
  );
  const longest = screen.getByRole("img", { name: "per-day median and p90" });
  expect(bars(longest).map((bar) => bar.getAttribute("class"))).toEqual([
    chartBar.neutralFaint,
    chartBar.neutral,
  ]);
  expect(longest.querySelectorAll("line")).toHaveLength(0);
});
