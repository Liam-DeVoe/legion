// The prototype's web/src/components/Sparkbars.tsx, ported as it is; its fills are the `chartBar`
// tokens, since no component writes a colour utility of its own. One difference: each bar carries
// its own verdict (`met`), which its caller judges from the value the bar draws, in its target's
// direction and inclusiveness, and leaves null where the target does not judge that statistic of
// a day (`lib/target.ts`'s `ownVerdict`), rather than the prototype's comparison here of the bar
// against the line.
import { chartBar } from "../../theme/classes";
import { barScale } from "./lib/barScale";

export interface SparkbarPoint {
  label: string; // tooltip prefix, e.g. "2026-09-27"
  value: number | null; // null: nothing to measure that day
  partial: boolean;
  // Whether the bar's value meets the target, as its caller judges it; null or absent draws the
  // bar neutral, as every bar is where the target judges no statistic of a day the bar draws.
  met?: boolean | null;
}

interface Props {
  points: SparkbarPoint[];
  // Where a target may judge the bars: the eval whose target it is and the statistic each bar
  // draws a day's value of, which every bar names (`data-eval`, `data-statistic`, with
  // `data-span="day"`); omitted on a chart no eval judges.
  judged?: { eval: string; statistic: string };
  // Omitted where no target exists: no target line.
  target?: number;
  // Top of the scale; the target line always fits under it.
  max: number;
  format: (value: number) => string;
}

const BAR = 10;
const HEIGHT = 28;

/** One bar per day with a dashed target line where its caller draws one: a green bar's value meets
 * the target, a red bar's misses it, a neutral bar has no verdict, and faded bars are days the
 * window only partly covers. Plain SVG, no chart library, so it costs nothing on a filter change. */
export function Sparkbars({ points, judged, target, max, format }: Props) {
  const y = barScale(
    max,
    target,
    points.map((p) => p.value),
    HEIGHT
  );
  return (
    <svg
      viewBox={`0 0 ${points.length * BAR} ${HEIGHT}`}
      preserveAspectRatio="none"
      className="w-full h-7 block"
      role="img"
      aria-label="per-day trend"
    >
      {points.map((p, i) => (
        <g key={p.label} opacity={p.partial ? 0.4 : 1}>
          <title>{`${p.label}${p.partial ? " (partial day)" : ""}: ${p.value === null ? "no runs" : format(p.value)}`}</title>
          <rect x={i * BAR} y={0} width={BAR} height={HEIGHT} fill="transparent" />
          {p.value === null ? (
            <rect
              x={i * BAR + 1}
              y={HEIGHT - 1}
              width={BAR - 2}
              height={1}
              className={chartBar.empty}
            />
          ) : (
            <rect
              x={i * BAR + 1}
              y={y(p.value)}
              width={BAR - 2}
              height={Math.max(HEIGHT - y(p.value), 1)}
              className={p.met == null ? chartBar.neutral : p.met ? chartBar.met : chartBar.missed}
              data-eval={judged?.eval}
              data-span={judged === undefined ? undefined : "day"}
              data-statistic={judged?.statistic}
            />
          )}
        </g>
      ))}
      {target !== undefined && (
        <line
          x1={0}
          x2={points.length * BAR}
          y1={y(target)}
          y2={y(target)}
          className={chartBar.targetLine}
          strokeDasharray="3 2"
          vectorEffect="non-scaling-stroke"
        />
      )}
    </svg>
  );
}
