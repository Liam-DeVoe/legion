import type { Locator, Page } from "@playwright/test";

import { CONTROL_LABELS } from "../web/src/features/issue-page/page-words";

/**
 * How part J's rows read the new issue page's Spec view and its Contents slot
 * (dispatch://INFRA-3939, part J of dispatch://INFRA-1196, `e2e/issue-page-j.e2e.ts`): the
 * readers that D's `e2e/issue-page.ts` (the page's root and its frame), G's `e2e/issue-page-g.ts`
 * (one node's row, and the tree as rows) and I's `e2e/issue-page-i.ts` (the outline) do not hold
 * (decision 2 of the parts). No later part changes this file.
 *
 * What part J draws, beyond what those files name:
 *
 * - **The issue's three sentences** `[data-testid="issue-three-things"]`, each an `li` whose
 *   label is `[data-page-chrome]` and whose sentence is `[data-author-text]`: in the Contents
 *   slot (part D's pane and Outline sheet), and as the Spec view's first group.
 * - **The Contents list**, one `[data-contents-entry="<node id>"]` per entry, its line
 *   `[data-contents-line]`, and above it the toggle named `CONTROL_LABELS.focus_branch`.
 * - **The Spec view** `[data-spec-body]`: its decision cells `[data-spec-cell]` (`data-said`,
 *   `data-spec-statements`), its statements `[data-spec-statement]` (`data-spec-mark`), and
 *   under each statement the line `[data-spec-because]` naming its decision.
 */

/** The family `web/src/styles.css` declares for the typeface the new page ships and draws in
 *  (part B, decision 10). */
export const TYPEFACE = "Dispatch Inter";

/** The iPhone 13's screen less the browser's own bars, the smallest phone the page is read on. */
export const IPHONE_13 = { height: 664, width: 390 } as const;

/** The issue's three sentences under "In short", wherever they are drawn. */
export function threeThings(page: Page): Locator {
  return page.getByTestId("issue-three-things");
}

/** The author named inside `scope`, where the page names one. */
export function rowAuthor(scope: Locator): Locator {
  return scope.locator("[data-node-author]");
}

/** The rows of the Contents list, wherever it is drawn: the pane, or the Outline sheet. */
export function contentsEntries(scope: Locator): Locator {
  return scope.locator("[data-contents-entry]");
}

/** The Focus branch toggle above the Contents list in `scope`. */
export function focusBranchControl(scope: Locator): Locator {
  return scope.getByRole("button", { exact: true, name: CONTROL_LABELS.focus_branch });
}

/**
 * What is wrong with the line a Contents entry draws of `text`, read from the browser's own
 * layout in the face the page draws in now: the line must fit the box the entry leaves it, and
 * must hold the next word of `text` too wherever that word and the ellipsis fit there (R1's
 * first-line rule, cut at a word to the entry's width). Empty where the line is the right one.
 */
export function contentsLineFaults(entry: Locator, text: string): Promise<string[]> {
  return entry.evaluate((element, text) => {
    const line = element.querySelector<HTMLElement>("[data-contents-line]");
    if (line === null) return ["the entry draws no line"];
    const shown = line.textContent ?? "";
    const words = shown.endsWith("…") ? shown.slice(0, -1) : shown;
    if (!text.startsWith(words)) {
      return [`the entry draws "${shown}", which "${text}" does not begin with`];
    }
    const room = line.getBoundingClientRect().width;
    const drawn = document.createRange();
    drawn.selectNodeContents(line);
    const width = drawn.getBoundingClientRect().width;
    const faults: string[] = [];
    if (width > room + 1) {
      faults.push(`"${shown}" is ${width.toFixed(1)} px, past its ${room.toFixed(1)} px box`);
    }
    const next = text.slice(words.length).trim().split(" ")[0] ?? "";
    if (next === "") return faults;
    // The line with its next word, laid out beside it in the same face and size.
    const probe = line.cloneNode(false) as HTMLElement;
    probe.textContent = `${words} ${next}…`;
    probe.style.cssText =
      "position: absolute; visibility: hidden; flex: none; width: auto; white-space: nowrap";
    line.after(probe);
    const longer = probe.getBoundingClientRect().width;
    probe.remove();
    if (longer <= room - 2) {
      faults.push(
        `"${shown}" leaves out "${next}", though "${words} ${next}…" is ${longer.toFixed(1)} px, inside its ${room.toFixed(1)} px box`
      );
    }
    return faults;
  }, text);
}

/** The address the page's stylesheet loads the typeface from, read from its own `@font-face`
 *  rule; null when no rule declares the family. c-typeface. */
export function typefaceAddress(page: Page): Promise<string | null> {
  return page.evaluate((family) => {
    const find = (rules: CSSRuleList, base: string): string | null => {
      for (const rule of rules) {
        if (rule instanceof CSSFontFaceRule) {
          const named = rule.style.getPropertyValue("font-family").replace(/["']/g, "").trim();
          const source = /url\(["']?([^"')]+)["']?\)/.exec(rule.style.getPropertyValue("src"));
          if (named === family && source?.[1] !== undefined) return new URL(source[1], base).href;
        } else if (rule instanceof CSSGroupingRule) {
          const found = find(rule.cssRules, base);
          if (found !== null) return found;
        }
      }
      return null;
    };
    for (const sheet of document.styleSheets) {
      const found = find(sheet.cssRules, sheet.href ?? document.baseURI);
      if (found !== null) return found;
    }
    return null;
  }, TYPEFACE);
}

/** How each face of the typeface's family stands in the document's font set: `loaded` once the
 *  file arrived, `error` once its request failed. c-typeface. */
export function typefaceStatuses(page: Page): Promise<string[]> {
  return page.evaluate(
    (family) =>
      [...document.fonts]
        .filter((face) => face.family.replace(/["']/g, "") === family)
        .map((face) => face.status),
    TYPEFACE
  );
}

/** One Decided by cell of the Spec view: whose words it draws, the statements its bracket
 *  spans, and its drawn text. */
export interface DecidedByCell {
  readonly ids: readonly string[];
  readonly said: string | null;
  readonly text: string;
}

/** Each Decided by cell of the Spec view, in the order they are drawn. */
export function decidedByCells(page: Page): Promise<DecidedByCell[]> {
  return page.evaluate(() =>
    [...document.querySelectorAll("[data-spec-cell]")].map((cell) => ({
      ids: (cell.getAttribute("data-spec-statements") ?? "").split(" ").filter(Boolean),
      said: cell.getAttribute("data-said"),
      text: (cell.textContent ?? "").replace(/\s+/g, " ").trim(),
    }))
  );
}

/** The one line under each statement of the Spec view that names the decision that shaped it,
 *  by statement id; `""` for a statement that draws none. */
export function becauseLines(page: Page): Promise<Record<string, string>> {
  return page.evaluate(() =>
    Object.fromEntries(
      [...document.querySelectorAll("[data-spec-statement]")].map((row) => [
        row.getAttribute("data-node-id") ?? "",
        (row.querySelector("[data-spec-because]")?.textContent ?? "").replace(/\s+/g, " ").trim(),
      ])
    )
  );
}
