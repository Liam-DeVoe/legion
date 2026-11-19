// The prototype's web/src/components/SpreadBars.tsx, ported as it is; its fills are the
// `chartBar` tokens, since no component writes a colour utility of its own. One difference: where
// a lead-time eval may judge the chart (`judged`), each of a day's two bars takes its own verdict,
// the median bar by the day's median and the p90 bar by its p90, in the target's direction and
// inclusiveness, and none where the target does not judge that statistic (`lib/target.ts`'s
// `ownVerdict`), and the dashed line is drawn exactly where the bars take one (`targetLine`),
// rather than the prototype's strict "under the target" of each bar's figure, since a person may
// make a target inclusive, turn its direction, and judge one of the two statistics and not the
// other.
import { chartBar } from "../../theme/classes";
import { barScale } from "./lib/barScale";
import { type Judge, ownVerdict, type Target, targetLine } from "./lib/target";

export interface SpreadBarDay {
  day: string; // YYYY-MM-DD, the tooltip's prefix
  n: number;
  median_minutes: number | null;
  p90_minutes: number | null;
  partial: boolean;
}

const MEDIAN: Judge = "minutes.median";
const P90: Judge = "minutes.p90";

/** The statistics a judged chart's bars draw, a day's value of each: a lead time's median and its
 *  p90. A lead-time card's caption names its dashed line where `targetLine` draws one over them. */
export const SPREAD_STATISTICS: readonly Judge[] = [MEDIAN, P90];

interface Props {
  days: readonly SpreadBarDay[];
  // Where a lead-time eval may judge the bars: its slug and its target, which each bar names
  // (`data-eval`, and `data-statistic` `minutes.median` or `minutes.p90`, with
  // `data-span="day"`) and is judged by. Omitted on a chart no eval judges, whose bars read
  // neutral under no line.
  judged?: { evalSlug: string; target: Target };
  noun: string; // what `n` counts, "runs"
  format: (minutes: number) => string;
}

const BAR = 12;
const HEIGHT = 36;

/** One bar per day: the p90 drawn faint behind the median. Where an eval judges the chart, a dashed
 * line marks its target where the bars take a tone, and each bar is green where its own value
 * meets the target, red where it misses it and neutral where it has no verdict, so a day's two
 * bars may differ. Today's partial day is faded. Plain SVG, like `Sparkbars`. */
export function SpreadBars({ days, judged, noun, format }: Props) {
  const line = judged === undefined ? undefined : targetLine(judged.target, SPREAD_STATISTICS);
  const y = barScale(
    1,
    line,
    days.map((d) => d.p90_minutes),
    HEIGHT
  );
  const verdict = (statistic: Judge, value: number | null): boolean | null =>
    judged === undefined ? null : ownVerdict(judged.target, statistic, "day", value);
  return (
    <svg
      viewBox={`0 0 ${days.length * BAR} ${HEIGHT}`}
      preserveAspectRatio="none"
      className="block h-9 w-full"
      role="img"
      aria-label="per-day median and p90"
    >
      {days.map((d, i) => {
        const medianMet = verdict(MEDIAN, d.median_minutes);
        const p90Met = verdict(P90, d.p90_minutes);
        return (
          <g key={d.day} opacity={d.partial ? 0.55 : 1}>
            <title>
              {`${d.day}${d.partial ? " (today so far)" : ""}: ${
                d.median_minutes === null || d.p90_minutes === null
                  ? `no ${noun}`
                  : `median ${format(d.median_minutes)}, p90 ${format(d.p90_minutes)}, ${d.n} ${noun}`
              }`}
            </title>
            <rect x={i * BAR} y={0} width={BAR} height={HEIGHT} fill="transparent" />
            {d.median_minutes === null || d.p90_minutes === null ? (
              <rect
                x={i * BAR + 1}
                y={HEIGHT - 1}
                width={BAR - 2}
                height={1}
                className={chartBar.empty}
              />
            ) : (
              <>
                <rect
                  x={i * BAR + 1}
                  y={y(d.p90_minutes)}
                  width={BAR - 2}
                  height={Math.max(HEIGHT - y(d.p90_minutes), 1)}
                  className={
                    p90Met === null
                      ? chartBar.neutralFaint
                      : p90Met
                        ? chartBar.metFaint
                        : chartBar.missedFaint
                  }
                  data-eval={judged?.evalSlug}
                  data-span={judged === undefined ? undefined : "day"}
                  data-statistic={judged === undefined ? undefined : P90}
                />
                <rect
                  x={i * BAR + 1}
                  y={y(d.median_minutes)}
                  width={BAR - 2}
                  height={Math.max(HEIGHT - y(d.median_minutes), 1)}
                  className={
                    medianMet === null
                      ? chartBar.neutral
                      : medianMet
                        ? chartBar.met
                        : chartBar.missed
                  }
                  data-eval={judged?.evalSlug}
                  data-span={judged === undefined ? undefined : "day"}
                  data-statistic={judged === undefined ? undefined : MEDIAN}
                />
              </>
            )}
          </g>
        );
      })}
      {line !== undefined && (
        <line
          x1={0}
          x2={days.length * BAR}
          y1={y(line)}
          y2={y(line)}
          className={chartBar.targetLine}
          strokeDasharray="3 2"
          vectorEffect="non-scaling-stroke"
        />
      )}
    </svg>
  );
}
