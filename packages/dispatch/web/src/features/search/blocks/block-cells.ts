import type { GoingBlockData } from "../../../api/types";
import { issuePriority } from "../../issue/issue-priority";
import { PROPERTY_LABELS } from "../../issue-page/page-structure";
import { issueStatusWord } from "../../issue-page/page-words";
import { laneLabel } from "../../project/board-model";

/** One kind's block frame, as `GoingBlockData` carries it. */
export type BlockOf<K extends GoingBlockData["kind"]> = Extract<GoingBlockData, { kind: K }>;

/**
 * A row's value as a block draws it: a number, a string or a boolean as itself, and nothing for a
 * value the read left empty. A block shows exactly what the server's read returned, so a number
 * is written in full as the read wrote it, never rounded or grouped into another number.
 */
export function cellText(value: unknown): string {
  if (value === null || value === undefined) {
    return "";
  }
  if (typeof value === "string" || typeof value === "number" || typeof value === "boolean") {
    return String(value);
  }
  return JSON.stringify(value);
}

/**
 * A value of the column named `column` as a block draws it, in Dispatch's words where the column
 * is one of an issue's fields (design v2, state 5): a priority as the issue page writes one, P0 to
 * P3, and "No priority" for the group of issues with none ("none"); a status by its word ("In
 * progress"); and every other value as `cellText` does. The server's reads answer an issue's
 * priority as the number 0 to 3 and its status as its token, and a chain that groups by either
 * keeps that value.
 */
export function columnText(column: string, value: unknown): string {
  if (column === "priority") {
    if (typeof value === "number" && Number.isInteger(value) && value >= 0) return `P${value}`;
    if (value === "none" || value === null) return laneLabel(issuePriority(null));
  }
  if (column === "status" && typeof value === "string" && value !== "") {
    return issueStatusWord(value);
  }
  return cellText(value);
}

/** The name of a column as a block heads it: an issue's field as the issue page names it
 *  ("Priority", "Status"), and any other field with its first letter a capital and its
 *  underscores spaces ("open_asks" reads "Open asks"). */
export function columnWords(column: string): string {
  const named = (PROPERTY_LABELS as Readonly<Record<string, string>>)[column];
  if (named !== undefined) return named;
  const words = column.replaceAll("_", " ");
  return `${words.charAt(0).toUpperCase()}${words.slice(1)}`;
}

/** A row's value as the number a bar or a chart plots, or undefined where it holds none. */
export function cellNumber(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

/** A time of day as the person's locale writes one: "16:40", or "4:40 PM". */
const CLOCK = new Intl.DateTimeFormat(undefined, { hour: "numeric", minute: "2-digit" });

/** The time of day of `at`, a time in milliseconds, as the person's locale writes one: what a
 *  block's caption says it is "as of", and when a plan's changes were applied. */
export function clockWords(at: number): string {
  return CLOCK.format(at);
}
