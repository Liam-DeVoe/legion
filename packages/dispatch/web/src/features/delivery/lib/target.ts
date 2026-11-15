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

/** The sign a target reads with: at least (≥) or above (>) for a measure better higher, at most
 *  (≤) or under (<) for one better lower, by whether the target's own value meets it. */
export function targetSign(target: NonNullable<Target>, better: Better): string {
  if (better === "higher") return target.inclusive ? "\u2265" : ">";
  return target.inclusive ? "\u2264" : "<";
}

/** A target as a card's caption names it, `format` writing its value: "≥ 20/day", or "off"
 *  where the target is off. */
export function targetLabel(
  target: Target,
  better: Better,
  format: (value: number) => string
): string {
  return target === null ? "off" : `${targetSign(target, better)} ${format(target.value)}`;
}

/** Whether value meets target, by the rule the server judges the window's figures by: at or past
 *  an inclusive target, past a strict one, in the direction the measure is better; null where the
 *  target is off. */
export function meetsTarget(value: number, target: Target, better: Better): boolean | null {
  if (target === null) return null;
  if (better === "higher") return target.inclusive ? value >= target.value : value > target.value;
  return target.inclusive ? value <= target.value : value < target.value;
}
