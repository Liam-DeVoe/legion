import { expect, test } from "bun:test";

import type { BlockOf } from "./block-cells";
import { chartOption, chartPoints } from "./ChartBlock";

// A chart of issues counted by status (design v2, state 5): each place along the axis in
// Dispatch's words, and a tooltip that shows a bar's or a point's value on hover, named as the
// counted field is headed. The canvas itself is echarts', which the unit suite does not draw.
const byStatus: BlockOf<"chart"> = {
  kind: "chart",
  title: "DEMO issues by status",
  spec: { mark: "bar", params: {}, source: "run", table: "chain", x: "status", y: "issues" },
  rows: [
    { issues: 2, status: "in_progress" },
    { issues: 1, status: "todo" },
    { issues: "many", status: "done" },
  ],
};

const palette = { axis: "#111", label: "#222", split: "#333" };

test("a chart's points are its rows with a number, each place in Dispatch's words", () => {
  expect(chartPoints(byStatus)).toEqual([
    { label: "In progress", value: 2 },
    { label: "Todo", value: 1 },
  ]);
});

test("hovering a bar or a point shows its value, the series named as its field is headed", () => {
  for (const mark of ["bar", "line"] as const) {
    const option = chartOption(chartPoints(byStatus), mark, "Issues", palette);
    expect(option.tooltip).toEqual({ trigger: "axis" });
    expect(option.series).toEqual([
      expect.objectContaining({ data: [2, 1], name: "Issues", type: mark }),
    ]);
    expect(option.xAxis).toEqual(expect.objectContaining({ data: ["In progress", "Todo"] }));
    expect(option.animation).toBe(false);
  }
});
