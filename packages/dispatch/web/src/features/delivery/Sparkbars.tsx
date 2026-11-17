// The prototype's web/src/components/Sparkbars.tsx, ported as it is; its fills are the `chartBar`
// tokens, since no component writes a colour utility of its own. One difference: each day carries
// its own verdict (`met`), which its caller judges from that day's row (`lib/target.ts`'s
// `dayMeets`), rather than the bar's figure judged here, since a target may judge a statistic the
// bar does not draw.
import { chartBar } from "../../theme/classes";
import { barScale } from "./lib/barScale";

export interface SparkbarPoint {
  label: string; // tooltip prefix, e.g. "2026-09-27"
  value: number | null; // null: nothing to measure that day
  partial: boolean;
  // Whether the day meets the target; null draws its bar neutral, as every bar is with no target.
  met: boolean | null;
}

interface Props {
  points: SparkbarPoint[];
  // Omitted where no target exists: no target line.
  target?: number;
  // Top of the scale; the target line always fits under it.
  max: number;
  format: (value: number) => string;
}

const BAR = 10;
const HEIGHT = 28;

/** One bar per day with a dashed target line: green bars are days that meet the target, red bars
 * days that miss it, neutral ones days with no verdict, and faded bars days the window only partly
 * covers. Plain SVG, no chart library, so it costs nothing on a filter change. */
export function Sparkbars({ points, target, max, format }: Props) {
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
              className={p.met === null ? chartBar.neutral : p.met ? chartBar.met : chartBar.missed}
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
