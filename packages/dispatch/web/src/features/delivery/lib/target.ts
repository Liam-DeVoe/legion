// A delivery answer's target as the measures panel and the Pipeline page state it and judge their
// figures by it. The answer carries each target as its eval holds it: its value, whether the value
// itself meets it, the eval's direction, and the statistics its verdict judges (`judges`, each
// `<series>.<stat>`), or null where a person turned the target off. A card's badge is the server's
// verdict of the whole window (`status`). Every other figure a page tones, a headline figure, a
// count, a day's bar or per-day cell, a lead-time line's figure, takes a tone only where the
// target judges the statistic it shows, and then by its own value of that statistic
// (`ownVerdict`, dispatch://INFRA-2916, R5). Pure.

import type { DeliveryLeadTimeCell, DeliveryTargets } from "../../../api/types";
import { pct } from "./format";

/** One measure's target, or null where it is off. */
export type Target = DeliveryTargets["deploys_per_day"];

/** One of the six delivery measures a target belongs to, as an answer's `targets` keys it. */
export type Measure = keyof DeliveryTargets;

/** Each measure's eval, by its slug in the eval register (envoy's evals.Slug*): every element a
 *  page tones by a target names it (`data-eval`), so a reader can tell which target judges it. */
export const EVAL_SLUGS: Readonly<Record<Measure, string>> = {
  deploys_per_day: "deploy-frequency",
  change_failure_rate: "change-fail-rate",
  merge_to_production_minutes: "merge-to-production",
  opened_to_merge_median_minutes: "pr-open-to-merge",
  deploy_run_success_rate: "deploy-run-success",
  unowned_p0: "unowned-p0",
};

/** One statistic a target may judge, `<series>.<stat>`: every judge the server's register write
 *  takes of the six delivery evals, as the contract types it. */
export type Judge = NonNullable<NonNullable<Target>["judges"]>[number];

/** How a caption names one statistic a target may judge, and writes a value of it. */
interface Statistic {
  /** The statistic as a caption names it, "median" or "deploys with a PR". */
  readonly word: string;
  readonly format: (value: number) => string;
  /** How the value reads where the target judges this statistic alone, with no word before it:
   *  the card's own figure, which its title names, or a phrase after the value that names the
   *  statistic, "45m for every change" for a lead time's longest change. */
  readonly alone?: (value: number) => string;
}

const minutes = (value: number) => `${value}m`;
const everyChange = (value: number) => `${value}m for every change`;
const count = (value: number) => `${value}`;
const perDay = (value: number) => `${value}/day`;
const share = (value: number) => pct(value, 0);
const perDeploy = (value: number) => `${pct(value, 0)} per deploy`;
const perPR = (value: number) => `${pct(value, 0)} per PR`;

/** Every statistic a delivery eval's target may judge: the judges the server's register write
 *  takes of the six delivery evals (packages/envoy/internal/dispatch/evals/delivery_targets.go,
 *  `deliveryJudges`), each figure the measures answer states of them. The contract's enum of a
 *  target's judges is that list (`Judge`), so a judge the server comes to take and this record
 *  does not name fails the compile. */
const STATISTICS: Readonly<Record<Judge, Statistic>> = {
  "deploys.per_day": { word: "deploys", format: perDay, alone: perDay },
  "deploys.with_prs_per_day": { word: "deploys with a PR", format: perDay },
  "deploys.deploys": { word: "deploys", format: count },
  "deploys.with_prs": { word: "deploys with a PR", format: count },
  "per_deploy.rate": { word: "failure rate", format: perDeploy, alone: perDeploy },
  "per_deploy.confirmed": { word: "deploys with a confirmed flag", format: count },
  "per_deploy.pending": { word: "deploys with a pending flag", format: count },
  "per_deploy.rejected": { word: "deploys with a rejected flag", format: count },
  "per_deploy.total": { word: "deploys", format: count },
  "per_pr.rate": { word: "failure rate", format: perPR },
  "per_pr.confirmed": { word: "PRs with a confirmed flag", format: count },
  "per_pr.pending": { word: "PRs with a pending flag", format: count },
  "per_pr.rejected": { word: "PRs with a rejected flag", format: count },
  "per_pr.reverts": { word: "reverted PRs", format: count },
  "per_pr.total": { word: "PRs", format: count },
  "upper_bound.rate": { word: "failure rate's upper bound", format: perDeploy },
  "upper_bound.confirmed_or_pending": {
    word: "deploys with a confirmed or pending flag",
    format: count,
  },
  "minutes.n": { word: "count", format: count },
  "minutes.median": { word: "median", format: minutes },
  "minutes.p90": { word: "p90", format: minutes },
  "minutes.max": { word: "longest", format: minutes, alone: everyChange },
  "runs.rate": { word: "success", format: share, alone: share },
  "runs.concluded": { word: "concluded runs", format: count },
  "runs.reached_production": { word: "runs reaching production", format: count },
  "runs.cancelled": { word: "cancelled runs", format: count },
  "issues.value": { word: "unowned P0 issues", format: count, alone: count },
};

/** A judge as a caption names it; one this page does not know, from a server newer than its
 *  contract, reads as the server wrote it. */
function statistic(judge: string): Statistic {
  const known: Readonly<Partial<Record<string, Statistic>>> = STATISTICS;
  return known[judge] ?? { word: judge, format: count };
}

/** The statistics target's verdict judges, in the order the server judges them: the eval's whole
 *  list, which a figure is judged by (`judgesStatistic`). */
function judgesOf(target: Target): readonly string[] {
  return target?.judges ?? [];
}

/** The judges a caption names and a lead-time card shows a line of: target's judges less those
 *  another judge implies. Under a lower_better target a series' longest value meeting the target
 *  means its median and its p90 meet it (median <= p90 <= max), its longest value missing it
 *  misses the target whatever they do, and the three have a value or none together. So the
 *  server's verdict over the eval's whole list (`evals.VerdictOf`) is its verdict over this one,
 *  and leaving them out changes what a caption says and never what is judged: the catalogue's
 *  merge to production, judged by its longest change and its median, reads "for every change". A
 *  higher_better target implies nothing, since no eval judges a minimum. A figure is toned by the
 *  eval's whole list (`judgesStatistic`), so the median a judged longest change implies is still
 *  toned by its own value. */
export function shownJudges(target: Target): readonly string[] {
  const judges = judgesOf(target);
  if (target?.direction !== "lower_better") return judges;
  return judges.filter((judge) => {
    const dot = judge.indexOf(".");
    const stat = judge.slice(dot + 1);
    return !(
      (stat === "median" || stat === "p90") &&
      judges.includes(`${judge.slice(0, dot)}.max`)
    );
  });
}

/** A judge's word as a sentence starts it, "Longest". */
export function judgeWord(judge: string): string {
  const { word } = statistic(judge);
  return word.charAt(0).toUpperCase() + word.slice(1);
}

/** How a target reads, as a sign or in words: at least (≥) or above (>) for an eval better
 *  higher, at most (≤) or under (<) for one better lower, by whether the target's own value meets
 *  it. One shown judge that reads alone (`Statistic.alone`) is the sign and the value as it
 *  writes it; otherwise every shown judge's word comes first, joined by "and", and the value is
 *  written as the first of them writes one (every judge is held to one value). Every measure
 *  better lower counts or times something and none goes below 0, so at most 0 is 0 itself, which
 *  reads as the value alone. */
function phrase(target: NonNullable<Target>, words: boolean): string {
  const judges = shownJudges(target);
  const [first] = judges;
  const alone = judges.length === 1 && first !== undefined ? statistic(first).alone : undefined;
  const format = alone ?? (first === undefined ? count : statistic(first).format);
  const named =
    alone === undefined ? judges.map((judge) => statistic(judge).word).join(" and ") : "";
  const lead = named === "" ? "" : `${named} `;
  if (target.direction === "lower_better" && target.inclusive && target.value === 0) {
    return `${lead}${format(0)}`;
  }
  const [sign, word] =
    target.direction === "higher_better"
      ? target.inclusive
        ? ["\u2265", "at least"]
        : [">", "above"]
      : target.inclusive
        ? ["\u2264", "at most"]
        : ["<", "under"];
  return `${lead}${words ? word : sign} ${format(target.value)}`;
}

/** A target as a card's caption names it: "≥ 20/day", "p90 ≤ 720m", "< 45m for every change",
 *  or "off" where it is off. */
export function targetLabel(target: Target): string {
  return target === null ? "off" : phrase(target, false);
}

/** A target as a chart's caption names it, in words: "median under 60m", or "off" where it is
 *  off. */
export function targetWords(target: Target): string {
  return target === null ? "off" : phrase(target, true);
}

/** Whether value meets target in its eval's direction: at or past an inclusive target, past a
 *  strict one; null where the target is off. The window's verdicts are the server's, by the one
 *  rule every eval read judges by (`evals.VerdictOf`); the server judges no single figure, and
 *  this is the same comparison for one. */
export function meetsTarget(value: number, target: Target): boolean | null {
  if (target === null) return null;
  if (target.direction === "higher_better") {
    return target.inclusive ? value >= target.value : value > target.value;
  }
  return target.inclusive ? value <= target.value : value < target.value;
}

/** Whether target judges statistic: one of the eval's whole list of judges, one another judge
 *  implies included (`shownJudges` leaves those out of a caption alone); false where the target
 *  is off. */
export function judgesStatistic(target: Target, statistic: string): boolean {
  return judgesOf(target).includes(statistic);
}

/** What a figure shows a value of: one day (a chart's bar, a per-day cell, a lead-time card's
 *  today) or the whole window (a headline figure, a count, a lead-time card's seven days). */
export type Span = "day" | "window";

/** The statistics a day's value is judged of, each a rate or a quantile of that day's own
 *  samples. A count or a sum (`minutes.n`, `runs.concluded`, `deploys.deploys`) judges the
 *  window's total, which no day is a share of, so no day's value of it is judged. */
const DAY_STATISTICS: Readonly<Partial<Record<string, true>>> = {
  "deploys.per_day": true,
  "runs.rate": true,
  "minutes.median": true,
  "minutes.p90": true,
  "minutes.max": true,
} satisfies Partial<Record<Judge, true>>;

/** A figure's own verdict, which its tone follows: whether value, the figure's value of
 *  statistic over span, meets target (`meetsTarget`). Null, which tones the figure nothing, where
 *  the target is off or judges no such statistic, where the figure has no value, and for a day's
 *  value of a statistic no day is judged of (`DAY_STATISTICS`). No figure takes the card's
 *  badge, the server's verdict of every judged statistic at once: under a target of the longest
 *  change and the median a median figure is toned by its own median, and a p90 figure by
 *  nothing. */
export function ownVerdict(
  target: Target,
  statistic: string,
  span: Span,
  value: number | null
): boolean | null {
  if (value === null || !judgesStatistic(target, statistic)) return null;
  if (span === "day" && DAY_STATISTICS[statistic] !== true) return null;
  return meetsTarget(value, target);
}

/** Where a chart draws target's dashed line: exactly where its bars take a tone, the target's
 *  value where it judges one of `drawn`, the statistics the chart's bars draw, that a day's value
 *  is judged of (`ownVerdict`), whether or not any day has a value; undefined where it is off or
 *  judges none of them, since its value is then of a figure no bar draws. */
export function targetLine(target: Target, drawn: readonly Judge[]): number | undefined {
  return drawn.some(
    (statistic) => DAY_STATISTICS[statistic] === true && judgesStatistic(target, statistic)
  )
    ? target?.value
    : undefined;
}

/** The statistics each chart's bars draw, a day's value of each: a day's deploys, which are its
 *  deploys a day, a day's run success, and a lead time's median and p90 (`SpreadBars`). A target
 *  judging another statistic, the window's count of deploys among them, tones none of their bars
 *  and draws no line over them. */
export const DEPLOY_BARS: readonly Judge[] = ["deploys.per_day"];
export const RUN_SUCCESS_BARS: readonly Judge[] = ["runs.rate"];
export const LEAD_TIME_BARS: readonly Judge[] = ["minutes.median", "minutes.p90"];

/** A Pipeline lead-time cell's value of a lead-time eval's judge, a day's or the seven days',
 *  null where it states none. */
export function leadTimeValue(cell: DeliveryLeadTimeCell, judge: string): number | null {
  switch (judge) {
    case "minutes.n":
      return cell.n;
    case "minutes.median":
      return cell.median_minutes;
    case "minutes.p90":
      return cell.p90_minutes;
    case "minutes.max":
      return cell.max_minutes;
    default:
      return null;
  }
}
