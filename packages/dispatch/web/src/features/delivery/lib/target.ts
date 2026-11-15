// A delivery answer's target as the measures panel and the Pipeline section state it and judge a
// day by it. The answer carries each target as its eval holds it, a value and whether the value
// itself meets it, or null where a person turned the target off; which way is better is the
// measure's own (more deploys and run success, fewer failures, minutes and unowned P0 issues).
// The verdicts of the window are the server's (`status`); a day's tone, which the server judges
// no verdict of, is the same comparison here. Pure.

import type { DeliveryTargets } from "../../../api/types";

/** One measure's target: its value and inclusiveness, or null where it is off. */
export type Target = DeliveryTargets["deploys_per_day"];

/** Which way a measure is better. */
export type Better = "higher" | "lower";

/** How a target reads, `format` writing its value: at least (≥) or above (>) for a measure better
 *  higher, at most (≤) or under (<) for one better lower, by whether the target's own value meets
 *  it, as a sign or in words. Every measure better lower counts or times something and none goes
 *  below 0, so at most 0 is 0 itself, which reads as the value alone. */
function phrase(
  target: NonNullable<Target>,
  better: Better,
  words: boolean,
  format: (value: number) => string
): string {
  if (better === "lower" && target.inclusive && target.value === 0) return format(0);
  const [sign, word] =
    better === "higher"
      ? target.inclusive
        ? ["\u2265", "at least"]
        : [">", "above"]
      : target.inclusive
        ? ["\u2264", "at most"]
        : ["<", "under"];
  return `${words ? word : sign} ${format(target.value)}`;
}

/** A target as a card's caption names it: "≥ 20/day", or "off" where the target is off. */
export function targetLabel(
  target: Target,
  better: Better,
  format: (value: number) => string
): string {
  return target === null ? "off" : phrase(target, better, false, format);
}

/** A target as a chart's caption names it, in words: "under 45m", or "off" where it is off. */
export function targetWords(
  target: Target,
  better: Better,
  format: (value: number) => string
): string {
  return target === null ? "off" : phrase(target, better, true, format);
}

/** Whether value meets target, by the rule the server judges the window's figures by: at or past
 *  an inclusive target, past a strict one, in the direction the measure is better; null where the
 *  target is off. */
export function meetsTarget(value: number, target: Target, better: Better): boolean | null {
  if (target === null) return null;
  if (better === "higher") return target.inclusive ? value >= target.value : value > target.value;
  return target.inclusive ? value <= target.value : value < target.value;
}
