// The built-in renderers of `EvalChart` (dispatch://INFRA-2918/artifact/plan-md, D4): per-day bars
// through the delivery pages' `Sparkbars`, the per-day spread of a median and its p90 through
// `SpreadBars`, a per-day line, and a job's timing (its median and p90 a day, and how often it
// succeeded). Each draws every series of its model, names every value it draws in the DOM
// (`data-chart`, `data-series`, `data-x`, `data-value`), and, while the compare range is shown,
// draws it as a muted second series at the same places, empty where the range has no data.
import type { ReactNode } from "react";

import { chartLine } from "../../theme/classes";
import { barScale } from "../delivery/lib/barScale";
import { Sparkbars, type ValueMarks, valueMarks } from "../delivery/Sparkbars";
import { SpreadBars } from "../delivery/SpreadBars";
import {
  type ChartModel,
  type ChartRendererProps,
  type ChartSeries,
  judgeOf,
  writerOf,
} from "./chart-model";

/** The DOM names of `series`' values of `statistic` (`ValueMarks`). */
function marksOf(model: ChartModel, series: ChartSeries, statistic: string): ValueMarks {
  return {
    chart: model.name,
    series: series.name,
    statistic,
    x: series.points.map((point) => point.x),
  };
}

/** `series`' values of `statistic`, a day at a time, the window's or the compare range's. */
function valuesOf(
  series: ChartSeries,
  statistic: string,
  range: "values" | "compare"
): (number | null)[] {
  return series.points.map((point) => point[range]?.[statistic]?.value ?? null);
}

/** One bar per day of each series' first statistic, toned by the series' target where it judges a
 *  day's value of it. A share is drawn on a scale up to 100%. */
export function PerDayBars({ model, compare }: ChartRendererProps): ReactNode {
  return model.series.map((series) => {
    const statistic = series.statistics[0]?.id;
    if (statistic === undefined) return null;
    return (
      <Sparkbars
        compare={compare ? valuesOf(series, statistic, "compare") : undefined}
        judged={
          series.target === null
            ? undefined
            : {
                evalSlug: model.name,
                statistic: judgeOf(series.name, statistic),
                target: series.target,
              }
        }
        key={series.name}
        marks={marksOf(model, series, statistic)}
        max={series.unit === "ratio" ? 1 : undefined}
        points={series.points.map((point) => ({
          label: point.x,
          partial: point.partial,
          value: point.values[statistic]?.value ?? null,
        }))}
        write={writerOf(series.unit, statistic)}
      />
    );
  });
}

/** Each series' median and p90 a day (`SpreadBars`), its days' counts in each day's title. */
export function PerDaySpread({ model, compare }: ChartRendererProps): ReactNode {
  return model.series.map((series) => (
    <SpreadBars
      compare={
        compare
          ? series.points.map((point) => ({
              median: point.compare?.median?.value ?? null,
              p90: point.compare?.p90?.value ?? null,
            }))
          : undefined
      }
      days={series.points.map((point) => ({
        day: point.x,
        median_minutes: point.values.median?.value ?? null,
        n: point.values.n?.value ?? 0,
        p90_minutes: point.values.p90?.value ?? null,
        partial: point.partial,
      }))}
      judged={series.target === null ? undefined : { evalSlug: model.name, target: series.target }}
      key={series.name}
      marks={marksOf(model, series, "median")}
      noun={series.noun}
      write={writerOf(series.unit, "median")}
    />
  ));
}

const STEP = 10;
const HEIGHT = 28;

/** The runs of consecutive values a line joins: a day with no value breaks it. */
function segments(values: readonly (number | null)[]): { index: number; value: number }[][] {
  const runs: { index: number; value: number }[][] = [];
  let run: { index: number; value: number }[] = [];
  values.forEach((value, index) => {
    if (value === null) {
      if (run.length > 0) runs.push(run);
      run = [];
      return;
    }
    run.push({ index, value });
  });
  if (run.length > 0) runs.push(run);
  return runs;
}

/** One series' line: a stroke through each run of days with a value, and a point on each. */
function Line(props: {
  className: { stroke: string; point: string };
  dashed: boolean;
  marks: ValueMarks;
  range: "window" | "compare";
  values: readonly (number | null)[];
  write: (value: number) => string;
  y: (value: number) => number;
}): ReactNode {
  const { values, y } = props;
  const at = (index: number) => index * STEP + STEP / 2;
  return (
    <>
      {segments(values).map((run) => (
        <polyline
          className={`fill-none ${props.className.stroke}`}
          key={run[0]?.index}
          points={run.map(({ index, value }) => `${at(index)},${y(value)}`).join(" ")}
          strokeDasharray={props.dashed ? "2 2" : undefined}
          strokeWidth={1.5}
          vectorEffect="non-scaling-stroke"
        />
      ))}
      {values.map((value, index) =>
        value === null ? null : (
          <circle
            className={props.className.point}
            cx={at(index)}
            cy={y(value)}
            key={props.marks.x[index]}
            r={1.5}
            {...valueMarks(props.marks, index, value, props.range)}
          >
            <title>{`${props.marks.x[index]}${props.range === "compare" ? ", compare" : ""}: ${props.write(value)}`}</title>
          </circle>
        )
      )}
    </>
  );
}

/** A line through each series' first statistic a day, a day with no value breaking it; the
 *  compare range's line muted and dashed under it. */
export function PerDayLine({ model, compare }: ChartRendererProps): ReactNode {
  return model.series.map((series) => {
    const statistic = series.statistics[0]?.id;
    if (statistic === undefined) return null;
    const values = valuesOf(series, statistic, "values");
    const compared = compare ? valuesOf(series, statistic, "compare") : undefined;
    const y = barScale(1, undefined, [...values, ...(compared ?? [])], HEIGHT - 4);
    const write = writerOf(series.unit, statistic);
    const marks = marksOf(model, series, statistic);
    return (
      <svg
        aria-label="per-day line"
        className="block h-7 w-full"
        key={series.name}
        preserveAspectRatio="none"
        role="img"
        viewBox={`0 -2 ${values.length * STEP} ${HEIGHT}`}
      >
        {compared === undefined ? null : (
          <g data-series-range="compare">
            <Line
              className={{ point: chartLine.comparePoint, stroke: chartLine.compareStroke }}
              dashed
              marks={marks}
              range="compare"
              values={compared}
              write={(value) => write(value)(0)}
              y={y}
            />
          </g>
        )}
        <Line
          className={{ point: chartLine.point, stroke: chartLine.stroke }}
          dashed={false}
          marks={marks}
          range="window"
          values={values}
          write={(value) => write(value)(0)}
          y={y}
        />
      </svg>
    );
  });
}

/** A job's timing (the Pipeline page's timing card): each day's median and p90 with its count of
 *  runs in the day's title, and each day's success rate under them. */
export function Timing({ model, compare }: ChartRendererProps): ReactNode {
  return model.series.map((series) => (
    <div className="flex flex-col gap-1" key={series.name}>
      <PerDaySpread compare={compare} model={{ ...model, series: [series] }} />
      <Sparkbars
        compare={compare ? valuesOf(series, "success_rate", "compare") : undefined}
        marks={marksOf(model, series, "success_rate")}
        max={1}
        points={series.points.map((point) => ({
          label: point.x,
          partial: point.partial,
          value: point.values.success_rate?.value ?? null,
        }))}
        write={writerOf(series.unit, "success_rate")}
      />
    </div>
  ));
}
