import type { ReactNode } from "react";
import { useEffect, useMemo, useRef, useState } from "react";

import { CHART_AXIS_COLORS } from "../../../theme/chart-colors";
import { borderDefault } from "../../../theme/classes";
import { useMediaQuery } from "../../shell/useDialog";
import { type BlockOf, cellNumber, columnText, columnWords } from "./block-cells";
import { blockCellClassName, blockHeadClassName } from "./TableBlock";

// echarts is loaded here as the delivery timeline loads it (features/delivery/Timeline.tsx): by
// the one dynamic import, so its chunk arrives with the first chart an answer draws and nothing
// the shell loads earlier pulls it in. The bar is part of the shell; a static import here would
// put echarts in the entry chunk every page pays for.

/** The chart draws on a canvas, so its colours are values: the axis colours every chart shares,
 *  and one series colour that reads on either scheme. */
const SERIES_COLOR = "#0284c7";

/** One point of a chart: its place along the axis in Dispatch's words, and its value. */
export interface ChartPoint {
  readonly label: string;
  readonly value: number;
}

/** The points of a chart's block: one for each row with a number in the spec's `y`, its `x` in
 *  Dispatch's words ("In progress", "P1"), as `columnText` writes a table's cell. */
export function chartPoints(block: BlockOf<"chart">): readonly ChartPoint[] {
  const { x, y } = block.spec;
  return block.rows.flatMap((row) => {
    const value = cellNumber(row[y]);
    return value === undefined ? [] : [{ label: columnText(x, row[x]), value }];
  });
}

/**
 * The echarts option of a chart (design v2, state 5): one series of `points` drawn as `mark`,
 * named as the spec's `y` field is headed, so hovering a bar or a point shows a tooltip with that
 * point's place and its value; the axes in `palette`'s colours, and no animation.
 */
export function chartOption(
  points: readonly ChartPoint[],
  mark: BlockOf<"chart">["spec"]["mark"],
  name: string,
  palette: { readonly axis: string; readonly label: string; readonly split: string }
): Record<string, unknown> {
  return {
    animation: false,
    grid: { bottom: 8, containLabel: true, left: 8, right: 16, top: 16 },
    series: [
      {
        data: points.map((point) => point.value),
        itemStyle: { color: SERIES_COLOR },
        lineStyle: { color: SERIES_COLOR },
        name,
        type: mark,
      },
    ],
    tooltip: { trigger: "axis" },
    xAxis: {
      axisLabel: { color: palette.label },
      axisLine: { lineStyle: { color: palette.axis } },
      data: points.map((point) => point.label),
      type: "category",
    },
    yAxis: {
      axisLabel: { color: palette.label },
      splitLine: { lineStyle: { color: palette.split } },
      type: "value",
    },
  };
}

/**
 * A chart: one series, the spec's `x` along the axis and its `y` plotted, as bars or a line, each
 * place in Dispatch's words; hovering a bar or a point shows its value. Every point is a row the
 * server's read returned, and the same rows are written out beneath the canvas as a table a screen
 * reader reads, so the numbers drawn are the numbers a reader can check. Nothing of the chart is
 * stored: it is drawn from the answer's own block frame, and drawn again from the same frame when
 * the run is replayed.
 */
export function ChartBlock({ block }: { readonly block: BlockOf<"chart"> }): ReactNode {
  const container = useRef<HTMLDivElement>(null);
  const [drawn, setDrawn] = useState(false);
  const dark = useMediaQuery("(prefers-color-scheme: dark)");
  const palette = dark ? CHART_AXIS_COLORS.dark : CHART_AXIS_COLORS.light;
  const { mark, x, y } = block.spec;
  const points = useMemo(() => chartPoints(block), [block]);
  const xWords = columnWords(x);
  const yWords = columnWords(y);

  useEffect(() => {
    let disposed = false;
    let cleanup: (() => void) | undefined;
    void import("echarts").then((echarts) => {
      const element = container.current;
      if (disposed || element === null) {
        return;
      }
      const chart = echarts.init(element, undefined, { renderer: "canvas" });
      chart.setOption(chartOption(points, mark, yWords, palette));
      const observer = new ResizeObserver(() => chart.resize());
      observer.observe(element);
      setDrawn(true);
      cleanup = () => {
        observer.disconnect();
        chart.dispose();
      };
    });
    return () => {
      disposed = true;
      cleanup?.();
      setDrawn(false);
    };
  }, [mark, palette, points, yWords]);

  return (
    <div>
      <div
        aria-hidden="true"
        className={`h-48 w-full rounded border ${borderDefault}`}
        data-chart-drawn={drawn ? "true" : "false"}
        data-testid="going-block-chart"
        ref={container}
      />
      <table className="sr-only" data-testid="going-block-chart-data">
        <caption>{block.title === "" ? `${yWords} by ${xWords}` : block.title}</caption>
        <thead>
          <tr>
            <th className={blockHeadClassName} scope="col">
              {xWords}
            </th>
            <th className={blockHeadClassName} scope="col">
              {yWords}
            </th>
          </tr>
        </thead>
        <tbody>
          {points.map((point, index) => (
            <tr
              data-testid="going-block-chart-point"
              // biome-ignore lint/suspicious/noArrayIndexKey: a block's rows never reorder.
              key={index}
            >
              <td className={blockCellClassName}>{point.label}</td>
              <td className={blockCellClassName}>{point.value}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
