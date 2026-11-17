import { expect, type Page } from "@playwright/test";

import { CONTROL_LABELS, GAP_LINES } from "../web/src/features/issue-page/page-words";
import { blockNode, focusThePage, nodeRows, rowBody } from "./issue-page-g";

/**
 * What part T of dispatch://INFRA-1196 (dispatch://INFRA-4062) reads of the rows of the new issue
 * page, and how it names each row so that two runs, and two heads, can be compared row by row.
 * This is T's own helper file (decision 2 of dispatch://INFRA-1196/artifact/part-decisions-md):
 * it reuses G's readers and locators (`e2e/issue-page-g.ts`) and copies none of them.
 *
 * `drawnRowReadings` reads every `[data-node-row]` of `[data-issue-page]` as G's `NodeRow.tsx`
 * draws it, in drawing order, with one `page.evaluate` for all the rows and, where it reads the
 * bodies, one more for each row once the focus is on it:
 *
 * - `id` is `data-node-id`, `rowKey` is `data-row-key` (null on a view's row, which draws none),
 *   `level` is `aria-level`, and `parentRowKey` is the `data-row-key` of the nearest row before
 *   it one level up, as G's `parentOfRow` finds the row a row sits under.
 * - `words` are `[data-node-words]` without their trail (`[data-node-trail]`: the chips, the gap
 *   words and a count), a child's facts and any control; a folded row's one line,
 *   `[data-node-first-line]`, where it draws no words.
 * - In the row's group `[data-node-margin]`: `kind` is the row's `data-node-type` and the text of
 *   its tag `[data-node-tag]`, `author` is `[data-node-author]`, `time` is `[data-node-time]` as
 *   drawn (the row draws its time in that span, and no `<time>` element), and `state` is the
 *   row's `data-node-state` with the word `[data-node-state]` draws and `[data-node-unsure]`.
 * - Each `[data-node-chip]`: the words drawn before its name (its tie), its name
 *   (`[data-chip-name]`, or the word for a target deleted) and the `href` of the link the name is.
 *   A chip whose tie is `CONTROL_LABELS.also_under` is one of the other parents the row lists, and
 *   is read into `alsoUnder` rather than `chips`.
 * - Each `[data-gap]`: its `data-gap-kind`, its word on the row, and the sentence the body says
 *   it in (`GAP_LINES`).
 * - `facts`: a child's facts on its row (`[data-child-facts]`), which carry no label, then the
 *   body's facts, each by the label its `<dt>` draws and the value its `<dd>` draws.
 * - `counts`: what a folded row counts (`[data-node-count]`), then the body's line of what the
 *   coverage read counts for a requirement.
 * - `body`: what the row draws while it has the focus beyond its line, read with the focus on it
 *   as G's keys put it there (`focusRow` does it by id, which a node drawn under two parents
 *   shares, so this moves it by place): who made the node where the line names nobody ("Author
 *   unknown") or names an agent by its role's word (its whole title), the words drawn whole, an
 *   ask's options with their own words and its answer, and every other line `[data-node-body]`
 *   draws. Never a control: not the row's acts (`[data-row-acts]`), not Keep and Change
 *   (`DecisionActions`), not the control that keeps the body open, nor any line that holds a
 *   control.
 *
 * `seedKeys` and `keyedRows` name each row by what the seed returned rather than by an id the
 * server minted (a comment's, an ask's, a result's, a block's UUID), and write each time as the
 * source it was drawn from rather than as an instant.
 */

/** A row's kind: the model's token for it, and the tag its group draws (a typed kind's word, or a
 *  child's key). */
export interface RowKind {
  readonly tag: string | null;
  readonly type: string | null;
}

/** A row's state: the model's token on the row, the word its group draws for it ("Kept by" and
 *  the person included), and the one word a decision the model marks unsure says of itself. */
export interface RowState {
  readonly token: string | null;
  readonly unsure: string | null;
  readonly word: string | null;
}

/** One chip at the end of a row's words. */
export interface RowChip {
  /** The `href` of the link the chip's name is, or null where its target is drawn as words. */
  readonly address: string | null;
  /** The words drawn before the name: the tie's word, and the line a stale target carries. */
  readonly label: string | null;
  readonly name: string;
}

/** One other parent a row lists ("Also under"). */
export interface RowParent {
  readonly address: string | null;
  readonly name: string;
}

/** One gap a row names: its kind, its word on the row, and the body's sentence for it, null
 *  where the body is not read or does not say it. */
export interface RowGap {
  readonly kind: string;
  readonly line: string | null;
  readonly word: string;
}

/** One fact: the label the page draws for it, null for a child's facts on its row, and its
 *  value as drawn. */
export interface RowFact {
  readonly name: string | null;
  readonly value: string;
}

/** One choice an ask offers, as the focused row draws it with its own words. */
export interface RowOption {
  readonly chosen: boolean;
  readonly words: string;
}

/** A person's answer an ask's row draws: who answered, what they chose where the ask lists no
 *  options, and what they wrote, every character kept. */
export interface RowAnswer {
  readonly author: string | null;
  readonly choices: readonly string[];
  readonly text: string | null;
}

/** What a row draws while it has the focus, beyond its line (the facts and counts are read into
 *  the row's own `facts` and `counts`, and the gap sentences into its `gaps`). */
export interface RowBody {
  readonly answer: RowAnswer | null;
  /** "Author unknown", or an agent's whole title; null where the line names the author. */
  readonly author: string | null;
  /** Each other line the body draws that holds no control. */
  readonly lines: readonly string[];
  readonly options: readonly RowOption[];
  readonly words: string | null;
}

/** One row of the page, as `drawnRowReadings` reads it. */
export interface RowReading {
  readonly alsoUnder: readonly RowParent[];
  readonly author: string | null;
  /** Null where the bodies were not read. */
  readonly body: RowBody | null;
  readonly chips: readonly RowChip[];
  readonly counts: readonly string[];
  readonly facts: readonly RowFact[];
  readonly gaps: readonly RowGap[];
  readonly id: string | null;
  readonly kind: RowKind;
  readonly level: number | null;
  readonly parentRowKey: string | null;
  readonly rowKey: string | null;
  readonly state: RowState | null;
  readonly time: string | null;
  readonly words: string | null;
}

/** What the focused row's body holds, as the browser reads it, before the facts and the lines are
 *  sorted into the row's own fields. */
interface PageBody extends RowBody {
  readonly facts: readonly RowFact[];
}

/** One row as the browser reads it: the gaps without their sentences, which the body says. */
interface PageRow extends Omit<RowReading, "body" | "gaps"> {
  readonly body: PageBody | null;
  readonly gaps: readonly Omit<RowGap, "line">[];
}

/** What `readPageRows` is handed. */
interface PageRead {
  /** The tie word of a chip naming another parent of the row (`CONTROL_LABELS.also_under`). */
  readonly alsoUnder: string;
  /** The place of the one row to read with its body, or null to read every row and no body. */
  readonly focused: number | null;
}

/** How long a row has to take the focus and draw its body: the suite's own expect timeout
 *  (`e2e/playwright.config.ts`), which a reader run outside the test runner does not inherit. */
const FOCUS_MS = 15_000;

/** The sentence a body says each gap in, by the gap's kind. */
const GAP_LINE_OF: Readonly<Partial<Record<string, string>>> = GAP_LINES;

/** How the body's line of the coverage read's two counts begins (`NodeRow.tsx`'s `NodeBody`). */
const COUNTS_LEAD = `${CONTROL_LABELS.serving_checks}:`;

/**
 * Every row of the page in drawing order (the head comment says what each field is read from).
 * With `bodies` (the default) it then puts the focus on each row in turn and reads what its body
 * draws, and ends with the focus on no row, as G's `unfoldEveryBranch` does. A row that is not
 * the row read before the focus moved fails the reading, since the bodies would then be another
 * page's.
 */
export async function drawnRowReadings(
  page: Page,
  { bodies = true }: { readonly bodies?: boolean } = {}
): Promise<RowReading[]> {
  const drawn = await page.evaluate(readPageRows, {
    alsoUnder: CONTROL_LABELS.also_under,
    focused: null,
  });
  if (!bodies) return drawn.map((row) => readingOf(row, null));
  const readings: RowReading[] = [];
  for (const [index, row] of drawn.entries()) {
    readings.push(readingOf(row, await focusedBody(page, index, row)));
  }
  await focusThePage(page);
  return readings;
}

/** The body row `index` draws once it holds the focus. */
async function focusedBody(page: Page, index: number, row: PageRow): Promise<PageBody> {
  const where = `row ${index} (${row.id ?? "no id"})`;
  const target = nodeRows(page).nth(index);
  await target.focus();
  await expect(target, `${where} never took the focus`).toBeFocused({ timeout: FOCUS_MS });
  await expect(rowBody(target), `${where} drew no body with the focus`).toHaveCount(1, {
    timeout: FOCUS_MS,
  });
  const [focused] = await page.evaluate(readPageRows, {
    alsoUnder: CONTROL_LABELS.also_under,
    focused: index,
  });
  if (focused === undefined || focused.id !== row.id || focused.rowKey !== row.rowKey) {
    throw new Error(
      `${where} is ${focused?.id ?? "no row"} once it has the focus: the rows moved while the ` +
        "reader read their bodies"
    );
  }
  if (focused.body === null) throw new Error(`${where} drew no body with the focus`);
  return focused.body;
}

/** One row's reading, with what its body said sorted into its gaps, facts and counts. */
function readingOf(row: PageRow, body: PageBody | null): RowReading {
  if (body === null) {
    return { ...row, body: null, gaps: row.gaps.map((gap) => ({ ...gap, line: null })) };
  }
  const gaps = row.gaps.map((gap) => {
    const line = GAP_LINE_OF[gap.kind];
    return { ...gap, line: line !== undefined && body.lines.includes(line) ? line : null };
  });
  const said = new Set(gaps.flatMap((gap) => (gap.line === null ? [] : [gap.line])));
  return {
    ...row,
    body: {
      answer: body.answer,
      author: body.author,
      lines: body.lines.filter((line) => !said.has(line) && !line.startsWith(COUNTS_LEAD)),
      options: body.options,
      words: body.words,
    },
    counts: [...row.counts, ...body.lines.filter((line) => line.startsWith(COUNTS_LEAD))],
    facts: [...row.facts, ...body.facts],
    gaps,
  };
}

/**
 * Reads the rows in the browser: every row with no body, or the one row `focused` names with its
 * body. It runs inside the page, so it names nothing outside itself.
 */
function readPageRows({ alsoUnder, focused }: PageRead): PageRow[] {
  // A control's words are no reading of the node: its label is the page's, and what it says
  // after a press (a copy's "Copied") is the press's.
  const controls = "button, input, select, textarea, form, [role='alert'], [aria-live]";
  const text = (element: Element | null | undefined): string | null =>
    element === null || element === undefined
      ? null
      : (element.textContent ?? "").replace(/\s+/g, " ").trim();
  const textWithout = (element: Element | null | undefined, left: string): string | null => {
    if (element === null || element === undefined) return null;
    const copy = element.cloneNode(true) as Element;
    for (const part of copy.querySelectorAll(left)) part.remove();
    return text(copy);
  };
  // The body of a row the focus left stays drawn until another row takes it, so what a row
  // draws on its line is read outside its body.
  const outsideBody = (row: Element, selector: string): Element[] =>
    [...row.querySelectorAll(selector)].filter(
      (found) => found.closest("[data-node-body]") === null
    );
  const wordsOf = (row: Element): string | null => {
    const words = row.querySelector("[data-node-words]");
    return words === null
      ? text(row.querySelector("[data-node-first-line]"))
      : textWithout(words, `[data-node-trail], [data-child-facts], ${controls}`);
  };
  const rows = [...document.querySelectorAll("[data-issue-page] [data-node-row]")];
  const levels = rows.map((row) => {
    const level = Number(row.getAttribute("aria-level") ?? "");
    return Number.isInteger(level) && level > 0 ? level : null;
  });
  const parentRowKey = (index: number): string | null => {
    const level = levels[index] ?? null;
    if (level === null) return null;
    for (let before = index - 1; before >= 0; before -= 1) {
      if (levels[before] === level - 1) return rows[before]?.getAttribute("data-row-key") ?? null;
    }
    return null;
  };
  const bodyOf = (row: Element): PageBody | null => {
    const body = row.querySelector("[data-node-body]");
    if (body === null) return null;
    const lines: string[] = [];
    for (const part of body.children) {
      if (part.matches("[data-node-author], [data-node-author-title], dl")) continue;
      if (part.matches(controls) || part.querySelector(controls) !== null) continue;
      const line = text(part);
      if (line !== null && line !== "") lines.push(line);
    }
    const answer = row.querySelector("[data-node-answer]");
    return {
      answer:
        answer === null
          ? null
          : {
              author: text(answer.querySelector("[data-answer-author]")),
              choices: [...answer.querySelectorAll("[data-answer-choice]")].map(
                (choice) => text(choice) ?? ""
              ),
              text: answer.querySelector("[data-answer-text]")?.textContent ?? null,
            },
      author: text(
        body.querySelector(":scope > [data-node-author], :scope > [data-node-author-title]")
      ),
      facts: [...body.querySelectorAll(":scope > dl > dt")].map((label) => ({
        name: text(label),
        value: textWithout(label.nextElementSibling, controls) ?? "",
      })),
      lines,
      options: [...row.querySelectorAll("[data-node-options] > li")].map((option) => ({
        chosen: option.getAttribute("data-node-option") === "chosen",
        words: textWithout(option, "[aria-hidden='true']") ?? "",
      })),
      words: wordsOf(row),
    };
  };
  const read = (row: Element, index: number, withBody: boolean): PageRow => {
    const margin = row.querySelector("[data-node-margin]");
    const chips = outsideBody(row, "[data-node-chip]").map((chip) => {
      const parts = [...chip.children];
      const name = parts.at(-1);
      const label = parts
        .slice(0, -1)
        .map((part) => text(part) ?? "")
        .join(" ")
        .trim();
      return {
        address: name?.matches("a") === true ? name.getAttribute("href") : null,
        label: label === "" ? null : label,
        name: text(name) ?? "",
      };
    });
    const token = row.getAttribute("data-node-state");
    const word = text(margin?.querySelector("[data-node-state]"));
    const unsure = text(margin?.querySelector("[data-node-unsure]"));
    return {
      alsoUnder: chips
        .filter((chip) => chip.label === alsoUnder)
        .map((chip) => ({ address: chip.address, name: chip.name })),
      author: text(margin?.querySelector("[data-node-author]")),
      body: withBody ? bodyOf(row) : null,
      chips: chips.filter((chip) => chip.label !== alsoUnder),
      counts: outsideBody(row, "[data-node-count]").map((count) => text(count) ?? ""),
      facts: outsideBody(row, "[data-child-facts]").map((facts) => ({
        name: null,
        value: text(facts) ?? "",
      })),
      gaps: outsideBody(row, "[data-gap]").map((gap) => ({
        kind: gap.getAttribute("data-gap-kind") ?? "",
        word: text(gap) ?? "",
      })),
      id: row.getAttribute("data-node-id"),
      kind: {
        tag: text(margin?.querySelector("[data-node-tag]")),
        type: row.getAttribute("data-node-type"),
      },
      level: levels[index] ?? null,
      parentRowKey: parentRowKey(index),
      rowKey: row.getAttribute("data-row-key"),
      state: token === null && word === null && unsure === null ? null : { token, unsure, word },
      time: text(margin?.querySelector("[data-node-time]")),
      words: wordsOf(row),
    };
  };
  if (focused === null) return rows.map((row, index) => read(row, index, false));
  const row = rows[focused];
  return row === undefined ? [] : [read(row, focused, true)];
}

/** A string a seed returns that can name a node: an id, a key, a block id or a slug. A seed's
 *  own words hold spaces, and name nothing. */
const ID_SHAPE = /^\S+$/;

/**
 * Each id the seed returned, by the path of its field in what the seed returned:
 * `requirements[1]`, `comments.threads[0]`, `openQuestion.askId`. The walk reads every string of
 * the object and of the objects and lists inside it, each object's fields in sorted order, so
 * the paths do not depend on the order the seed wrote its fields in.
 *
 * One id may stand in several fields: a list of every check and a field for the one that passed,
 * a list of the children and a field for the blocked one, the list of the requirements and the
 * field for the one a comment is anchored to. The id is named by its place in a list where a
 * list holds it, since a list is the seed's account of the nodes it wrote and a single field
 * says what a node is for; among lists, or among single fields, by the first path of the walk.
 *
 * A block of a document is drawn under F's node id `<slug>#b-<block id>` (G's `blockNode`), so
 * each id is named in that form too for each of `slugs`, the slugs of the issue's documents
 * (the seeds of `e2e/issue-layout-seed.ts` return the documents' ids and not their slugs). An id
 * no document holds a block of gets names no row draws, which name nothing.
 */
export function seedKeys(
  seeded: object,
  slugs: readonly string[] = []
): ReadonlyMap<string, string> {
  const found: { readonly id: string; readonly listed: boolean; readonly path: string }[] = [];
  const walk = (value: unknown, path: string, listed: boolean): void => {
    if (typeof value === "string") {
      if (ID_SHAPE.test(value)) found.push({ id: value, listed, path });
    } else if (Array.isArray(value)) {
      for (const [index, item] of value.entries()) walk(item, `${path}[${index}]`, true);
    } else if (typeof value === "object" && value !== null) {
      const fields: Readonly<Record<string, unknown>> = { ...value };
      for (const field of Object.keys(fields).sort()) {
        walk(fields[field], path === "" ? field : `${path}.${field}`, listed);
      }
    }
  };
  walk(seeded, "", false);
  const keys = new Map<string, string>();
  for (const { id, path } of [
    ...found.filter((one) => one.listed),
    ...found.filter((one) => !one.listed),
  ]) {
    for (const form of [id, ...slugs.map((slug) => blockNode(slug, id))]) {
      if (!keys.has(form)) keys.set(form, path);
    }
  }
  return keys;
}

/** Where a row's time comes from: when its node was made, or when a decision last changed. */
export type TimeSource = "changed" | "made";

/** The source of a drawn time, by the kind of row (F's rule, plan-md's "The tree read's
 *  members": `made_at` for an ask, a comment, a message and a result, and the decisions read's
 *  `changed_at` for a decision). F draws no time for a document block or a child issue. */
const TIME_SOURCES: Readonly<Partial<Record<string, TimeSource>>> = {
  ask: "made",
  comment: "made",
  decision: "changed",
  message: "made",
  result: "made",
};

/** A row by the names `keyedRows` gives: no id the server minted, and its time by its source. */
export interface KeyedRow {
  readonly alsoUnder: readonly RowParent[];
  readonly author: string | null;
  readonly body: RowBody | null;
  readonly chips: readonly RowChip[];
  readonly counts: readonly string[];
  readonly facts: readonly RowFact[];
  readonly gaps: readonly RowGap[];
  /** What the seed calls the row's node, else the row's path. */
  readonly key: string;
  readonly kind: RowKind;
  readonly level: number | null;
  /** The key of the row this one is drawn under, null at the top. */
  readonly parentKey: string | null;
  readonly state: RowState | null;
  /** The source of the time the row draws, null where it draws none. */
  readonly time: TimeSource | null;
  readonly words: string | null;
}

/**
 * Names each reading: by what `keys` (`seedKeys`) calls its node, and a row whose node no field
 * names by its row path, `<parent key>/<kind>#<n>`, the `n`th (from 0) of the rows of that kind
 * under the same parent row; a row at the top has an empty parent key, so its path starts with
 * `/`. A node drawn under two parents (a check that serves two requirements) is two rows with one
 * key, so a row is named by its key and its parent's key together. The parent is the nearest row
 * before it one level up, as `parentRowKey` is read, so a view's rows, which draw no
 * `data-row-key`, are named the same way.
 *
 * What a row reads that names a node by its id is written with the node's key in its place,
 * `{<key>}`: each chip's and other parent's address, the tag (a child's is its key) and a child's
 * facts, which name its blockers. The row's own id, key and parent's key are dropped, since they
 * are made of ids, and the time is written as its source. A row that draws a time on a kind no
 * source is known for fails the naming rather than write the instant.
 */
export function keyedRows(
  readings: readonly RowReading[],
  keys: ReadonlyMap<string, string>
): KeyedRow[] {
  const ids = idPattern(keys);
  const keyedText = (text: string): string =>
    ids === undefined ? text : text.replace(ids, (id) => `{${keys.get(id) ?? id}}`);
  const keyed: KeyedRow[] = [];
  const ordinals = new Map<string, number>();
  for (const [index, reading] of readings.entries()) {
    const parent = parentIndex(readings, index);
    const parentKey = parent === undefined ? null : (keyed[parent]?.key ?? null);
    const kind = reading.kind.type ?? "node";
    const siblings = `${parent ?? -1} ${kind}`;
    const ordinal = ordinals.get(siblings) ?? 0;
    ordinals.set(siblings, ordinal + 1);
    const named = reading.id === null ? undefined : keys.get(reading.id);
    const key = named ?? `${parentKey ?? ""}/${kind}#${ordinal}`;
    keyed.push({
      alsoUnder: reading.alsoUnder.map((other) => ({
        ...other,
        address: other.address === null ? null : keyedText(other.address),
      })),
      author: reading.author,
      body: reading.body,
      chips: reading.chips.map((chip) => ({
        ...chip,
        address: chip.address === null ? null : keyedText(chip.address),
      })),
      counts: reading.counts,
      facts: reading.facts.map((fact) =>
        fact.name === null ? { ...fact, value: keyedText(fact.value) } : fact
      ),
      gaps: reading.gaps,
      key,
      kind: {
        tag: reading.kind.tag === null ? null : keyedText(reading.kind.tag),
        type: reading.kind.type,
      },
      level: reading.level,
      parentKey,
      state: reading.state,
      time: timeSourceOf(reading, key),
      words: reading.words,
    });
  }
  return keyed;
}

/** The place of the row `index` is drawn under: the nearest row before it one level up. */
function parentIndex(readings: readonly RowReading[], index: number): number | undefined {
  const level = readings[index]?.level ?? null;
  if (level === null) return undefined;
  for (let before = index - 1; before >= 0; before -= 1) {
    if (readings[before]?.level === level - 1) return before;
  }
  return undefined;
}

/** Every id `keys` names, as a whole word of an address: not inside a longer id, except after
 *  `#b-`, where a block's id follows its document's slug. The longest first, so an id is never
 *  taken for a shorter one it starts with. */
function idPattern(keys: ReadonlyMap<string, string>): RegExp | undefined {
  const ids = [...keys.keys()].sort((a, b) => b.length - a.length || (a < b ? -1 : a > b ? 1 : 0));
  if (ids.length === 0) return undefined;
  const alternatives = ids.map((id) => id.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("|");
  return new RegExp(`(?:(?<![\\w-])|(?<=#b-))(?:${alternatives})(?![\\w-])`, "g");
}

/** The source of the time `reading` draws, or null where it draws none. */
function timeSourceOf(reading: RowReading, key: string): TimeSource | null {
  if (reading.time === null) return null;
  const source = reading.kind.type === null ? undefined : TIME_SOURCES[reading.kind.type];
  if (source === undefined) {
    throw new Error(
      `the row ${key} draws a time on a ${reading.kind.type ?? "row of no kind"}, and no read ` +
        "F draws a time from is known for that kind, so the time cannot be written as its source"
    );
  }
  return source;
}
