import { afterEach, expect, test } from "bun:test";
import { cleanup, render, screen } from "@testing-library/react";

import { chartBar } from "../../theme/classes";
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

const asNumber = (value: number) => `${value}/day`;

// Each bar's verdict is its caller's, judged from the bar's own value: here "at least 20".
const points: SparkbarPoint[] = [
  { label: "2026-09-25", value: 25, partial: false, met: true },
  { label: "2026-09-26", value: 10, partial: false, met: false },
  { label: "2026-09-27", value: null, partial: true, met: null },
];

test("Sparkbars draws a bar per day and one dashed target line", () => {
  render(<Sparkbars format={asNumber} max={0} points={points} target={20} />);
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

test("Sparkbars colours a day that meets the target met and one that misses it missed", () => {
  render(<Sparkbars format={asNumber} max={0} points={points} target={20} />);
  const [meets, misses] = bars(screen.getByRole("img", { name: "per-day trend" }));

  expect(meets?.getAttribute("class")).toBe(chartBar.met);
  expect(meets?.getAttribute("height")).toBe("28");
  expect(misses?.getAttribute("class")).toBe(chartBar.missed);
  expect(Number(misses?.getAttribute("height"))).toBeCloseTo((10 / 25) * 28);
});

test("Sparkbars draws a one-pixel stub on a day with nothing to measure", () => {
  render(<Sparkbars format={asNumber} max={0} points={points} target={20} />);
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

test("Sparkbars colours a bar by the verdict its caller hands it and not by where it stands against the line: a tall bar may miss, a short one meet, and one with no verdict is neutral", () => {
  const judgedElsewhere: SparkbarPoint[] = [
    { label: "2026-09-25", value: 25, partial: false, met: false },
    { label: "2026-09-26", value: 10, partial: false, met: true },
    { label: "2026-09-27", value: 30, partial: false, met: null },
  ];
  render(
    <Sparkbars
      format={asNumber}
      judged={{ eval: "deploy-frequency", statistic: "deploys.per_day" }}
      max={0}
      points={judgedElsewhere}
      target={20}
    />
  );
  const drawn = bars(screen.getByRole("img", { name: "per-day trend" }));

  expect(drawn.map((bar) => bar.getAttribute("class"))).toEqual([
    chartBar.missed,
    chartBar.met,
    chartBar.neutral,
  ]);
  // Each bar names the eval whose target may judge it and the statistic it draws a day's value of.
  expect(
    drawn.map((bar) => [
      bar.getAttribute("data-eval"),
      bar.getAttribute("data-statistic"),
      bar.getAttribute("data-span"),
    ])
  ).toEqual([
    ["deploy-frequency", "deploys.per_day", "day"],
    ["deploy-frequency", "deploys.per_day", "day"],
    ["deploy-frequency", "deploys.per_day", "day"],
  ]);
});

test("Sparkbars without a target draws every bar neutral and no line", () => {
  const unjudged = points.slice(0, 2).map((point) => ({ ...point, met: null }));
  render(<Sparkbars format={asNumber} max={30} points={unjudged} />);
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
});

const asMinutes = (minutes: number) => `${minutes}m`;

/** A full day with `median` and `p90` minutes, which a test dates and judges. */
function dayOf(median: number, p90: number): SpreadBarDay {
  return { day: "", n: 3, median_minutes: median, p90_minutes: p90, partial: false };
}

// Each bar's verdict is its caller's, judged from that bar's own value: here the median's and the
// p90's against "under 45m".
const days: SpreadBarDay[] = [
  {
    day: "2026-09-25",
    n: 4,
    median_minutes: 30,
    p90_minutes: 40,
    partial: false,
    median_met: true,
    p90_met: true,
  },
  {
    day: "2026-09-26",
    n: 3,
    median_minutes: 50,
    p90_minutes: 80,
    partial: false,
    median_met: false,
    p90_met: false,
  },
  {
    day: "2026-09-27",
    n: 0,
    median_minutes: null,
    p90_minutes: null,
    partial: true,
    median_met: null,
    p90_met: null,
  },
];

test("SpreadBars draws the p90 behind the median each day and one dashed target line", () => {
  render(<SpreadBars days={days} format={asMinutes} noun="runs" target={45} />);
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
  render(<SpreadBars days={days} format={asMinutes} noun="runs" target={45} />);
  const [p90Under, medianUnder, p90Over, medianOver] = bars(
    screen.getByRole("img", { name: "per-day median and p90" })
  );

  expect(p90Under?.getAttribute("class")).toBe(chartBar.metFaint);
  expect(medianUnder?.getAttribute("class")).toBe(chartBar.met);
  expect(p90Over?.getAttribute("class")).toBe(chartBar.missedFaint);
  expect(medianOver?.getAttribute("class")).toBe(chartBar.missed);
});

test("SpreadBars colours each bar by the verdict its caller hands it and not by where it stands against the line: bars under the line may miss, bars over it meet, and bars with no verdict are neutral", () => {
  const judgedElsewhere: SpreadBarDay[] = [
    { ...dayOf(30, 40), day: "2026-09-25", median_met: false, p90_met: false },
    { ...dayOf(50, 80), day: "2026-09-26", median_met: true, p90_met: true },
    { ...dayOf(45, 45), day: "2026-09-27", median_met: null, p90_met: null },
  ];
  render(<SpreadBars days={judgedElsewhere} format={asMinutes} noun="runs" target={45} />);

  expect(
    bars(screen.getByRole("img", { name: "per-day median and p90" })).map((bar) =>
      bar.getAttribute("class")
    )
  ).toEqual([
    chartBar.missedFaint,
    chartBar.missed,
    chartBar.metFaint,
    chartBar.met,
    chartBar.neutralFaint,
    chartBar.neutral,
  ]);
});

// R5: a target may judge a day's median and not its p90, or judge both and see one meet and the
// other miss, so each of a day's two bars takes its own verdict.
test("SpreadBars gives each of a day's two bars its own verdict: a neutral p90 beside a met median, a missed p90 beside a met median", () => {
  const apart: SpreadBarDay[] = [
    { ...dayOf(30, 40), day: "2026-09-25", median_met: true, p90_met: null },
    { ...dayOf(30, 50), day: "2026-09-26", median_met: true, p90_met: false },
  ];
  render(
    <SpreadBars
      days={apart}
      format={asMinutes}
      judged={{ eval: "merge-to-production", series: "minutes" }}
      noun="PRs"
      target={45}
    />
  );
  const drawn = bars(screen.getByRole("img", { name: "per-day median and p90" }));

  expect(drawn.map((bar) => bar.getAttribute("class"))).toEqual([
    chartBar.neutralFaint,
    chartBar.met,
    chartBar.missedFaint,
    chartBar.met,
  ]);
  // Each bar names the eval whose target may judge it and the statistic of the series it draws, a
  // day's value of it.
  expect(
    drawn.map((bar) => [
      bar.getAttribute("data-eval"),
      bar.getAttribute("data-statistic"),
      bar.getAttribute("data-span"),
    ])
  ).toEqual([
    ["merge-to-production", "minutes.p90", "day"],
    ["merge-to-production", "minutes.median", "day"],
    ["merge-to-production", "minutes.p90", "day"],
    ["merge-to-production", "minutes.median", "day"],
  ]);
});

test("SpreadBars draws a stub on a day with nothing to measure, naming the noun and today", () => {
  render(<SpreadBars days={days} format={asMinutes} noun="runs" target={45} />);
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

test("SpreadBars without a target draws the neutral pair and no line", () => {
  const unjudged = days.slice(0, 1).map((day) => ({ ...day, median_met: null, p90_met: null }));
  render(<SpreadBars days={unjudged} format={asMinutes} noun="merges" />);
  const svg = screen.getByRole("img", { name: "per-day median and p90" });

  expect(bars(svg).map((bar) => bar.getAttribute("class"))).toEqual([
    chartBar.neutralFaint,
    chartBar.neutral,
  ]);
  expect(svg.querySelectorAll("line")).toHaveLength(0);
  expect(titles(svg)).toEqual(["2026-09-25: median 30m, p90 40m, 4 merges"]);
  // A chart no eval judges names no statistic.
  expect(svg.querySelectorAll("[data-statistic]")).toHaveLength(0);
});
