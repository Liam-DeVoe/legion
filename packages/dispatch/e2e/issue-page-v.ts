import { expect, type Locator, type Page } from "@playwright/test";

import { CONTROL_LABELS } from "../web/src/features/issue-page/page-words";
import { issuePage } from "./issue-page";

/**
 * How part V's rows read the Verification view of the new issue page (dispatch://INFRA-1196
 * part V, `e2e/issue-page-v.e2e.ts`): the readers that D's `e2e/issue-page.ts`, G's
 * `e2e/issue-page-g.ts` and I's `e2e/issue-page-i.ts` do not hold (decision 2 of the parts). No
 * later part changes this file.
 *
 * What the view draws (`verification-filter/VerificationFilterBody.tsx`), its body
 * `[data-verification-body]`:
 *
 * - **A card** `[data-verification-card]` for each statement, with `data-node-id`,
 *   `data-statement-state` (met, not_met, unknown) and `data-node-context`. Inside it each check
 *   `[data-verification-check]` with `data-node-id` and `data-node-state`, its words the control
 *   `[data-verification-check-words]` that opens its facts, its latest result
 *   `[data-verification-result]` (`data-result-stands` on the one its state stands on), and the
 *   control `[data-verification-earlier]` that opens its earlier ones.
 * - **Over the cards** the summary `[data-verification-summary]` with
 *   `[data-verification-covered]`, `[data-verification-bar]`, `[data-verification-legend]`,
 *   `[data-verification-check-legend]` and `[data-verification-commit]`, the chips
 *   `[data-verification-chip]`, and the headings `[data-verification-head]`.
 *
 * Each measure over the cards fails when the view draws none, so a view that draws nothing
 * never passes a reading of what it draws.
 */

/** The selectors the readers pass into the browser. */
const SELECTORS = {
  card: "[data-verification-card]",
  commit: "[data-verification-commit]",
  issuePage: "[data-issue-page]",
} as const;

/** The message every measure fails with where the view draws no card. */
const NO_CARD = "the Verification view draws no card";

/** The view's cards, one for each statement it draws. */
export function verificationCardBoxes(page: Page): Locator {
  return issuePage(page).locator(SELECTORS.card);
}

/** A check's `run` command, drawn as code with its copy control beside it. */
export function nodeCommands(page: Page): Locator {
  return issuePage(page).locator("[data-node-command]");
}

export function copyCommandControls(page: Page): Locator {
  return issuePage(page).getByRole("button", { exact: true, name: CONTROL_LABELS.copy_command });
}

/** One check the view draws under a statement. */
export interface DrawnCheck {
  /** How many results the check's fold holds beside its latest, as its control says it. */
  readonly earlier: string | null;
  readonly id: string;
  /** How many results the check draws. */
  readonly results: number;
  /** The id of the latest result drawn under it, or null where it draws none. */
  readonly resultId: string | null;
  /** The `data-glyph` token of the shape its state is drawn with, or null where it draws none. */
  readonly shape: string | null;
  /** `data-node-state`, the model's own token for how the check stands. */
  readonly state: string | null;
  /** The word the page draws for its state. */
  readonly word: string | null;
  readonly words: string;
}

/** One statement the view draws, with the checks that serve it. */
export interface DrawnStatement {
  readonly checks: readonly DrawnCheck[];
  readonly context: boolean;
  readonly gapKinds: readonly string[];
  readonly id: string;
  /** `data-statement-state`: met, not_met or unknown. */
  readonly state: string | null;
  /** The word the page draws for that state. */
  readonly stateWord: string | null;
  readonly words: string;
}

/** Every statement the view draws, in document order, with its checks and their latest
 *  results. Fails where the view draws no card. */
export async function verificationCards(page: Page): Promise<DrawnStatement[]> {
  const cards = await page.evaluate(
    ({ card, issue }) => {
      const text = (element: Element | null): string =>
        (element?.textContent ?? "").replace(/\s+/g, " ").trim();
      return [...document.querySelectorAll(`${issue} ${card}`)].map((drawn) => ({
        checks: [...drawn.querySelectorAll("[data-verification-check]")].map((check) => ({
          earlier: text(check.querySelector("[data-verification-earlier]")) || null,
          id: check.getAttribute("data-node-id") ?? "",
          resultId:
            check.querySelector("[data-verification-result]")?.getAttribute("data-node-id") ?? null,
          results: check.querySelectorAll("[data-verification-result]").length,
          shape:
            check
              .querySelector("span[data-node-state]")
              ?.parentElement?.querySelector("svg[data-glyph]")
              ?.getAttribute("data-glyph") ?? null,
          state: check.getAttribute("data-node-state"),
          word: text(check.querySelector("span[data-node-state]")) || null,
          words: text(check.querySelector("[data-node-words]")),
        })),
        context: drawn.hasAttribute("data-node-context"),
        gapKinds: [...drawn.querySelectorAll("[data-gap-kind]")].map(
          (gap) => gap.getAttribute("data-gap-kind") ?? ""
        ),
        id: drawn.getAttribute("data-node-id") ?? "",
        state: drawn.getAttribute("data-statement-state"),
        stateWord: text(drawn.querySelector("span[data-statement-state]")) || null,
        words: text(drawn.querySelector("[data-node-words]")),
      }));
    },
    { card: SELECTORS.card, issue: SELECTORS.issuePage }
  );
  expect(cards.length, NO_CARD).toBeGreaterThan(0);
  return cards;
}

/** The one commit the view names for the issue's results, or null where it names none or more
 *  than one. Fails where the view draws no card. */
export async function namedCommit(page: Page): Promise<string | null> {
  const read = await page.evaluate(
    ({ card, commit, issue }) => {
      const drawn = document.querySelectorAll(`${issue} ${commit}`);
      return {
        cards: document.querySelectorAll(`${issue} ${card}`).length,
        commit:
          drawn.length === 1 ? (drawn[0]?.querySelector("code")?.textContent ?? "").trim() : null,
      };
    },
    { card: SELECTORS.card, commit: SELECTORS.commit, issue: SELECTORS.issuePage }
  );
  expect(read.cards, NO_CARD).toBeGreaterThan(0);
  return read.commit;
}

/** One element that reaches past the card it is drawn in. */
export interface Overflowing {
  /** The card's own description: its tag and its node id. */
  readonly card: string;
  /** How far past the card the element reaches, in CSS pixels. */
  readonly over: number;
  readonly what: string;
}

/** Every element whose painted box reaches more than a pixel past the view's card it sits in.
 *  Fails where the view draws no card. */
export async function boxesPastTheirCard(page: Page): Promise<Overflowing[]> {
  const read = await page.evaluate(
    ({ card, issue }) => {
      const describe = (element: Element): string => {
        const id = element.closest(card)?.getAttribute("data-node-id") ?? "";
        const own = (element.textContent ?? "").replace(/\s+/g, " ").trim().slice(0, 40);
        return `${element.tagName.toLowerCase()}${id === "" ? "" : ` in ${id}`} "${own}"`;
      };
      const cards = [...document.querySelectorAll(`${issue} ${card}`)];
      const found: { card: string; over: number; what: string }[] = [];
      for (const drawn of cards) {
        const bounds = drawn.getBoundingClientRect();
        if (bounds.width === 0) continue;
        for (const element of drawn.querySelectorAll("*")) {
          const rect = element.getBoundingClientRect();
          if (rect.width === 0 && rect.height === 0) continue;
          const over = Math.max(bounds.left - rect.left, rect.right - bounds.right);
          if (over > 1) found.push({ card: describe(drawn), over, what: describe(element) });
        }
      }
      return { cards: cards.length, found };
    },
    { card: SELECTORS.card, issue: SELECTORS.issuePage }
  );
  expect(read.cards, NO_CARD).toBeGreaterThan(0);
  return read.found;
}

/** Two pieces of text drawn over each other. */
export interface Overlap {
  readonly first: string;
  readonly second: string;
}

/**
 * Every pair of text boxes inside the view's cards that share a point on screen. Only a leaf
 * that carries text of its own is measured, so a box that holds another is not read as
 * overlapping it. Text kept for a screen reader alone (inside `.sr-only`, whose box clips it to
 * a pixel while its own spans keep their laid-out width) is no box the page draws, and is left
 * out. Fails where the view draws no card.
 */
export async function overlappingTexts(page: Page): Promise<Overlap[]> {
  const read = await page.evaluate(
    ({ card, issue }) => {
      const cards = [...document.querySelectorAll(`${issue} ${card}`)];
      const boxes: { label: string; rect: DOMRect }[] = [];
      for (const root of cards) {
        for (const element of root.querySelectorAll("*")) {
          if (element.children.length > 0 || element.tagName === "OPTION") continue;
          if (element.closest(".sr-only") !== null) continue;
          const text = (element.textContent ?? "").replace(/\s+/g, " ").trim();
          if (text === "") continue;
          const rect = element.getBoundingClientRect();
          if (rect.width === 0 || rect.height === 0) continue;
          boxes.push({ label: `${element.tagName.toLowerCase()} "${text.slice(0, 40)}"`, rect });
        }
      }
      const found: { first: string; second: string }[] = [];
      for (let i = 0; i < boxes.length; i += 1) {
        for (let j = i + 1; j < boxes.length; j += 1) {
          const a = boxes[i];
          const b = boxes[j];
          if (a === undefined || b === undefined) continue;
          const apart =
            a.rect.right <= b.rect.left + 0.5 ||
            b.rect.right <= a.rect.left + 0.5 ||
            a.rect.bottom <= b.rect.top + 0.5 ||
            b.rect.bottom <= a.rect.top + 0.5;
          if (!apart) found.push({ first: a.label, second: b.label });
        }
      }
      return { cards: cards.length, found };
    },
    { card: SELECTORS.card, issue: SELECTORS.issuePage }
  );
  expect(read.cards, NO_CARD).toBeGreaterThan(0);
  return read.found;
}
