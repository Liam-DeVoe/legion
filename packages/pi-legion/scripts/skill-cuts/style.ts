/**
 * The rules of a style text, each with its id, as the craft reader judges them (README.md, "The
 * style text's rule ids").
 *
 * The ids are `S1`, `S2`, … in the text's order:
 * - each body row of the text's first table, the parts of a SKILL.md in their order (not its header
 *   row or its separator row), read outside the two sections below;
 * - each top-level list item (`-`, `*`, `+` or `1.`) under a line or a heading named Rules
 *   (`**Rules:**`, `## Rules`);
 * - each top-level list item under `**Check before you commit:**` (or a heading of that name).
 *
 * A section runs to the next heading or the next line that is only a bold label ending in a colon.
 * An item's indented lines (its `- Example:` and `- Source:` sub-items) are part of its text and get
 * no id; a paragraph of a section is no rule.
 */
import { textLines } from "./cases";

/** Where a rule comes from: the order table, the Rules section, or the check before commit. */
export const STYLE_PARTS = ["order", "rules", "check"] as const;
export type StylePart = (typeof STYLE_PARTS)[number];

/** One rule of a style text: its id, its first line in the text (from 1), its part, and its text
 *  (a table row, or an item with its indented lines). */
export interface StyleRule {
  readonly id: string;
  readonly line: number;
  readonly part: StylePart;
  readonly text: string;
}

const HEADING = /^#{1,6}\s/;
/** A line that is only a bold label ending in a colon: `**Rules:**`, `**Order of a SKILL.md** (…):`. */
const LABEL = /^\*\*[^*]+(?::\*\*|\*\*[^*]*:)\s*$/;
const RULES = /^(?:#{1,6}\s+Rules:?|\*\*Rules:?\*\*:?)\s*$/i;
const CHECK = /^(?:#{1,6}\s+|\*\*)Check before you commit:?(?:\*\*)?:?\s*$/i;
const ITEM = /^(?:[-*+]|\d+[.)])\s+\S/;
const FENCE = /^(?:```|~~~)/;
const TABLE_ROW = /^\|/;
const SEPARATOR = /^\|?\s*:?-{3,}:?\s*(?:\|\s*:?-{3,}:?\s*)*\|?\s*$/;

/** The rules of the style text `text`, with their ids. Throws when it holds none. */
export function styleRules(text: string): StyleRule[] {
  const lines = textLines(text);
  const found: { line: number; part: StylePart; lines: string[] }[] = [];
  let section: "rules" | "check" | undefined;
  let item: { lines: string[] } | undefined;
  let tableRead = false;
  let fenced = false;
  for (let index = 0; index < lines.length; index++) {
    const line = lines[index] ?? "";
    if (fenced) {
      if (FENCE.test(line)) fenced = false;
      continue;
    }
    if (FENCE.test(line)) {
      item = undefined;
      fenced = true;
      continue;
    }
    if (HEADING.test(line) || LABEL.test(line)) {
      item = undefined;
      section = RULES.test(line) ? "rules" : CHECK.test(line) ? "check" : undefined;
      continue;
    }
    if (section === undefined) {
      if (!tableRead && TABLE_ROW.test(line) && SEPARATOR.test(lines[index + 1] ?? "")) {
        tableRead = true;
        for (index += 2; TABLE_ROW.test(lines[index] ?? ""); index++) {
          found.push({ line: index + 1, part: "order", lines: [(lines[index] ?? "").trim()] });
        }
        index--;
      }
      continue;
    }
    if (ITEM.test(line)) {
      item = { lines: [line] };
      found.push({ line: index + 1, part: section, lines: item.lines });
    } else if (item !== undefined && (line.trim() === "" || /^\s/.test(line))) {
      item.lines.push(line);
    } else {
      item = undefined;
    }
  }
  if (found.length === 0) {
    throw new Error(
      "the style text holds no rule: no row of an order table, and no list item under Rules or under Check before you commit"
    );
  }
  return found.map((rule, index) => ({
    id: `S${index + 1}`,
    line: rule.line,
    part: rule.part,
    text: rule.lines.join("\n").trimEnd(),
  }));
}
