// A delivery answer's target as the measures panel and the Pipeline page state it and judge a
// day by it. The answer carries each target as its eval holds it: its value, whether the value
// itself meets it, the eval's direction, and the statistics its verdict judges (`judges`, each
// `<series>.<stat>`), or null where a person turned the target off. The verdicts of the window
// are the server's (`status`); a day's tone, which the server judges no verdict of, is the same
// comparison here, of that day's value of each judged statistic. Pure.

import type { DeliveryDailyPoint, DeliveryLeadTimeCell, DeliveryTargets } from "../../../api/types";
import { pct } from "./format";

/** One measure's target, or null where it is off. */
export type Target = DeliveryTargets["deploys_per_day"];

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
 *  list, which a day is judged by (`dayMeets`). */
function judgesOf(target: Target): readonly string[] {
  return target?.judges ?? [];
}

/** The judges a caption names, a card shows a line or a toned figure of and a chart draws its
 *  line for: target's judges less those another judge implies. Under a lower_better target a
 *  series' longest value meeting the target means its median and its p90 meet it (median <= p90
 *  <= max), its longest value missing it misses the target whatever they do, and the three have
 *  a value or none together. So the server's verdict over the eval's whole list
 *  (`evals.VerdictOf`) is its verdict over this one, and leaving them out changes what the page
 *  says and never what is judged: the catalogue's merge to production, judged by its longest
 *  change and its median, reads "for every change". A higher_better target implies nothing, since
 *  no eval judges a minimum. The answer carries the eval's whole list, and so does a day's
 *  verdict. */
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
 *  rule every eval read judges by (`evals.VerdictOf`); the server judges no day, and this is the
 *  same comparison for one. */
export function meetsTarget(value: number, target: Target): boolean | null {
  if (target === null) return null;
  if (target.direction === "higher_better") {
    return target.inclusive ? value >= target.value : value > target.value;
  }
  return target.inclusive ? value <= target.value : value < target.value;
}

/** One day's verdict against target for a chart's bars, combined as `evals.VerdictOf` combines a
 *  window's judges: missed where the day's value of any judged statistic misses, met where the
 *  day states a value of every one and each meets, and null otherwise (the target off, no judge,
 *  or a judged statistic the day states no value of), which draws the day neutral. `dayValue` is
 *  that day's value of one judge, null where its row states none. */
export function dayMeets(
  target: Target,
  dayValue: (judge: string) => number | null
): boolean | null {
  const judges = judgesOf(target);
  if (target === null || judges.length === 0) return null;
  let unknown = false;
  for (const judge of judges) {
    const value = dayValue(judge);
    const met = value === null ? null : meetsTarget(value, target);
    if (met === false) return false;
    if (met === null) unknown = true;
  }
  return unknown ? null : true;
}

/** Whether target shows (`shownJudges`) any of `judges`: false where it is off, which shows none. */
export function showsAnyJudge(target: Target, judges: readonly string[]): boolean {
  return shownJudges(target).some((judge) => judges.includes(judge));
}

/** Where a chart draws target's dashed line: the target's value where one of its shown judges
 *  (`shownJudges`) is one of `drawn`, the statistics the chart's axis measures, and undefined
 *  where it is off or shows none of them, since its value is then of another figure than the
 *  bars'. */
export function targetLine(target: Target, drawn: readonly string[]): number | undefined {
  return showsAnyJudge(target, drawn) ? target?.value : undefined;
}

/** The statistics a day's deploys and a day's run success are a value of (`dailyValue`): the
 *  judges the two charts' bars, their dashed target line and the per-day table's two toned
 *  columns show a figure of. A target judging another statistic colours a day by that one where
 *  the day states it, and draws no line over bars of another figure. */
export const DEPLOY_BARS: readonly Judge[] = ["deploys.per_day", "deploys.deploys"];
export const RUN_SUCCESS_BARS: readonly Judge[] = ["runs.rate"];

/** A measures answer's day row's value of a judge of deploy-frequency or deploy-run-success, null
 *  where the row states none (a deploy with a PR, a failure rate). A day's deploys are that day's
 *  deploys a day. */
export function dailyValue(day: DeliveryDailyPoint, judge: string): number | null {
  switch (judge) {
    case "deploys.per_day":
    case "deploys.deploys":
      return day.deploys;
    case "runs.rate":
      return day.run_success_rate;
    case "runs.concluded":
      return day.concluded;
    case "runs.reached_production":
      return day.reached_production;
    case "runs.cancelled":
      return day.cancelled;
    default:
      return null;
  }
}

/** The statistics a lead-time chart's axis measures, minutes (`leadTimeValue`): a target judging
 *  one of them is drawn on it as the dashed line, and one judging the count alone is not. */
export const LEAD_TIME_MINUTES: readonly Judge[] = ["minutes.median", "minutes.p90", "minutes.max"];

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
