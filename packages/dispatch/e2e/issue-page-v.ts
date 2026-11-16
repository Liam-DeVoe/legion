import { expect, type Locator, type Page } from "@playwright/test";

import { CONTROL_LABELS, type StatementState } from "../web/src/features/issue-page/page-words";
import { blockNodeId } from "../web/src/features/issue-page/tree-drafts";
import {
  createIssue,
  createIssueArtifact,
  createProject,
  getIssue,
  getIssueCoverage,
  recordCheckResult,
} from "./api";
import { HEAD_SHA, PROJECT, pause, recorderActor } from "./issue-layout-seed-support";
import { specBlockRef } from "./issue-layout-seed-text";
import { issuePage } from "./issue-page";
import { longWords } from "./issue-page-g";

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
 * - **Over the cards** the issue's own row `[data-verification-issue]`, the tree's first row,
 *   holding the summary `[data-verification-summary]` with `[data-verification-covered]`,
 *   `[data-verification-bar]`, `[data-verification-legend]`, `[data-verification-check-legend]`
 *   (one `[data-check-legend-state]` for each of the seven states) and
 *   `[data-verification-commit]`, and the chips `[data-verification-chip]` with `aria-pressed`;
 *   then the table `[data-verification-table]` and its headings `[data-verification-head]`.
 * - **Under a statement's checks** the line `[data-verification-elsewhere]` that counts the
 *   checks of the issues under this one that serve it.
 *
 * Each measure over the cards fails when the view draws none, so a view that draws nothing
 * never passes a reading of what it draws. Row 3's issue is `seedLinkedChecks`, at the end.
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

/** The view's body, which holds its one tree. */
export function verificationBody(page: Page): Locator {
  return issuePage(page).locator("[data-verification-body]");
}

/** The issue's own row, the first row of the view's tree, which holds the summary and the
 *  chips. */
export function verificationIssueRow(page: Page): Locator {
  return issuePage(page).locator("[data-verification-issue]");
}

/** The card of the statement whose node id is `id`. */
export function verificationCard(page: Page, id: string): Locator {
  return issuePage(page).locator(`${SELECTORS.card}[data-node-id="${id}"]`);
}

/** Each row the view draws for the check whose node id is `id`: one under each statement it
 *  serves. */
export function verificationCheck(page: Page, id: string): Locator {
  return issuePage(page).locator(`[data-verification-check][data-node-id="${id}"]`);
}

/** The chip All, or the chip of one state a statement can be in. */
export function verificationChip(page: Page, chip: "all" | StatementState): Locator {
  return issuePage(page).locator(`[data-verification-chip="${chip}"]`);
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

/** How the view's body and its table stand at one width (part V's R6), in CSS pixels. */
export interface VerificationFrame {
  /** `[data-verification-body]`: its width, its computed top and bottom margins, and the width
   *  of the element around it, which is the stage's column. */
  readonly body: {
    readonly bottom: number;
    readonly parent: number;
    readonly top: number;
    readonly width: number;
  };
  /** The width of each column heading, in order, and of `[data-verification-table]`. A heading
   *  the width does not draw is 0 wide. */
  readonly columns: { readonly heads: readonly number[]; readonly table: number };
  /** The first card's computed left edge and top left corner, and the space between it and the
   *  second card, null where the view draws one card. */
  readonly firstCard: {
    readonly borderLeft: string;
    readonly gap: number | null;
    readonly radius: string;
  };
  /** The document's whole width, and the viewport's. */
  readonly page: { readonly scroll: number; readonly viewport: number };
}

/** The view's measure at the width the page is at. Fails where the view draws no card. */
export async function verificationFrame(page: Page): Promise<VerificationFrame> {
  const read = await page.evaluate(
    ({ card, issue }) => {
      const width = (element: Element | null | undefined): number =>
        element == null ? 0 : element.getBoundingClientRect().width;
      const body = document.querySelector(`${issue} [data-verification-body]`);
      const margins = body === null ? null : getComputedStyle(body);
      const cards = [...document.querySelectorAll(`${issue} ${card}`)];
      const [first, second] = cards;
      const edge = first === undefined ? null : getComputedStyle(first);
      // The headings are the cells of the head that hold no cell of their own: Statement, then
      // Checks and Latest result inside the rest of the row.
      const heads = [
        ...(document.querySelector(`${issue} [data-verification-head]`)?.querySelectorAll("span") ??
          []),
      ].filter((cell) => cell.children.length === 0);
      return {
        cards: cards.length,
        frame: {
          body: {
            bottom: Number.parseFloat(margins?.marginBottom ?? ""),
            parent: width(body?.parentElement),
            top: Number.parseFloat(margins?.marginTop ?? ""),
            width: width(body),
          },
          columns: {
            heads: heads.map((cell) => width(cell)),
            table: width(document.querySelector(`${issue} [data-verification-table]`)),
          },
          firstCard: {
            borderLeft: edge?.borderLeftWidth ?? "",
            gap:
              first === undefined || second === undefined
                ? null
                : second.getBoundingClientRect().top - first.getBoundingClientRect().bottom,
            radius: edge?.borderTopLeftRadius ?? "",
          },
          page: { scroll: document.documentElement.scrollWidth, viewport: window.innerWidth },
        },
      };
    },
    { card: SELECTORS.card, issue: SELECTORS.issuePage }
  );
  expect(read.cards, NO_CARD).toBeGreaterThan(0);
  return read.frame;
}

/** How many checks the summary's legend counts in each of the seven states, by the state's
 *  token (`data-check-legend-state`); a state whose count it does not draw reads NaN. Fails
 *  where the view draws no card. */
export async function checkLegendCounts(page: Page): Promise<Record<string, number>> {
  const read = await page.evaluate(
    ({ card, issue }) => ({
      cards: document.querySelectorAll(`${issue} ${card}`).length,
      counts: [...document.querySelectorAll(`${issue} [data-check-legend-state]`)].map(
        (item): [string, number] => {
          // The count is the state's first span, after its shape and before its word.
          const count = item.querySelector(":scope > span")?.textContent;
          return [
            item.getAttribute("data-check-legend-state") ?? "",
            count == null ? Number.NaN : Number(count.replace(/\D/g, "")),
          ];
        }
      ),
    }),
    { card: SELECTORS.card, issue: SELECTORS.issuePage }
  );
  expect(read.cards, NO_CARD).toBeGreaterThan(0);
  return Object.fromEntries(read.counts);
}

/** How many shapes the view's body draws, and the token of each one it draws with no box,
 *  which no reader sees (R2: with forced colours each state keeps its shape). Fails where the
 *  view draws no card. */
export async function glyphsWithNoBox(
  page: Page
): Promise<{ readonly drawn: number; readonly empty: readonly string[] }> {
  const read = await page.evaluate(
    ({ card, issue }) => {
      const glyphs = [
        ...document.querySelectorAll(`${issue} [data-verification-body] svg[data-glyph]`),
      ];
      return {
        cards: document.querySelectorAll(`${issue} ${card}`).length,
        drawn: glyphs.length,
        empty: glyphs
          .filter((glyph) => {
            const box = glyph.getBoundingClientRect();
            return box.width <= 0 || box.height <= 0;
          })
          .map((glyph) => glyph.getAttribute("data-glyph") ?? ""),
      };
    },
    { card: SELECTORS.card, issue: SELECTORS.issuePage }
  );
  expect(read.cards, NO_CARD).toBeGreaterThan(0);
  return { drawn: read.drawn, empty: read.empty };
}

/** How a link in a check's words is drawn: its colour and the colour of the words it stands
 *  in, and its underline's line and colour. */
export interface LinkLook {
  readonly color: string;
  readonly line: string;
  readonly underline: string;
  readonly words: string;
}

export function linkLook(link: Locator): Promise<LinkLook> {
  return link.evaluate((element) => {
    const words = element.closest("[data-verification-check-words]") ?? element.parentElement;
    const drawn = getComputedStyle(element);
    return {
      color: drawn.color,
      line: drawn.textDecorationLine,
      underline: drawn.textDecorationColor,
      words: words === null ? "" : getComputedStyle(words).color,
    };
  });
}

/**
 * A walk by Tab alone (part V's R8): each call presses Tab until the control it names holds the
 * keyboard focus and answers how many presses that took, and the walk presses at most `presses`
 * times over all its calls. So a row names the controls in the order Tab must reach them, and
 * fails with the message of the first one Tab does not reach in what is left of the presses.
 */
export function tabWalk(
  page: Page,
  presses: number
): (target: Locator, message: string) => Promise<number> {
  let left = presses;
  return async (target, message) => {
    for (let pressed = 0; ; pressed += 1) {
      const reached = await target.evaluateAll((found) =>
        found.some((element) => element === document.activeElement)
      );
      if (reached) return pressed;
      expect(left, message).toBeGreaterThan(0);
      left -= 1;
      await page.keyboard.press("Tab");
    }
  };
}

// ---------------------------------------------------------------------------------------------
// Row 3's issue: a check with links serving two statements, and a child's check serving a third.
// ---------------------------------------------------------------------------------------------

/** The block ids of statements A, B and C of row 3's spec, and of the issue's one check. */
const LINKED_BLOCKS = { a: "r-a", b: "r-b", c: "r-c", check: "c-linked" } as const;

/** Statement A: 200 words, which the view cuts at six lines (part V's R5). */
export const STATEMENT_A_WORDS = longWords("stated", 200);

/** Statement B: more than 32 characters, so the line that names it by its first words cuts
 *  them at a word (part V's R7, G's `chipWords`). */
export const STATEMENT_B_WORDS =
  "The check that serves the first statement serves this one as well.";

/** Statement C, which only the child's check serves. */
const STATEMENT_C_WORDS = "Only a check of the issue under this one serves this statement.";

/** The address the check's words link to. */
export const RUNBOOK = "https://example.com/runbook";

/** What `seedLinkedChecks` wrote, by the node ids the view draws its rows under. */
export interface LinkedChecksSeed {
  /** The issue under this one, whose own Verification document holds the check that serves C. */
  readonly child: string;
  readonly key: string;
  /** Statements A, B and C of the issue's spec, and the issue's one check. */
  readonly nodes: {
    readonly a: string;
    readonly b: string;
    readonly c: string;
    readonly check: string;
  };
}

/**
 * Row 3's issue (c-view of dispatch://INFRA-3936): a spec of statements A (200 words), B and C;
 * a Verification document with one check that serves A and B, whose words hold a markdown link
 * to the runbook and B's own `dispatch://` address, with one result an agent's session
 * recorded; and a child whose Verification document holds one check that serves C. A `serves`
 * is written as `verificationDocument` writes one, `dispatch://<key>/spec#b-<id>`. The seed
 * waits until the coverage read counts one check for each statement, the child's included, so
 * the row reads the view and not the indexing.
 */
export async function seedLinkedChecks(): Promise<LinkedChecksSeed> {
  await createProject({ key: PROJECT, name: "Issue layout" });
  const issue = await createIssue({
    force: true,
    project: PROJECT,
    spec: `${[
      [LINKED_BLOCKS.a, STATEMENT_A_WORDS],
      [LINKED_BLOCKS.b, STATEMENT_B_WORDS],
      [LINKED_BLOCKS.c, STATEMENT_C_WORDS],
    ]
      .map(([block, words]) => `:::requirement{#${block}}\n${words}\n:::`)
      .join("\n\n")}\n`,
    title: "A check's words draw their links in the Verification view",
  });
  const key = issue.key;
  const statement = (block: string) => specBlockRef(key, block);
  const verification = await createIssueArtifact(key, {
    content:
      "How the links are proved.\n\n" +
      `:::check{#${LINKED_BLOCKS.check} serves="${statement(LINKED_BLOCKS.a)} ${statement(LINKED_BLOCKS.b)}"` +
      ' surface="browser" run="bun run e2e -- e2e/issue-page-v.e2e.ts" when="before_merge"}\n' +
      `Read [the runbook](${RUNBOOK}) before the run, and then ${statement(LINKED_BLOCKS.b)}.\n:::\n`,
    name: "verification.md",
  });
  const child = await createIssue({
    force: true,
    parent: key,
    project: PROJECT,
    title: "Serve the last statement from the issue under it",
  });
  await createIssueArtifact(child.key, {
    content:
      "How the child is proved.\n\n" +
      `:::check{#c-below serves="${statement(LINKED_BLOCKS.c)}" surface="browser"` +
      ' run="bun run e2e" when="before_merge"}\n' +
      "The child's own check serves its parent's last statement.\n:::\n",
    name: "verification.md",
  });

  const deadline = Date.now() + 20_000;
  for (;;) {
    const counted = new Map(
      ((await getIssueCoverage(key)).requirements ?? []).map((one) => [one.block_id, one.checks])
    );
    if (
      [LINKED_BLOCKS.a, LINKED_BLOCKS.b, LINKED_BLOCKS.c].every((one) => counted.get(one) === 1)
    ) {
      break;
    }
    if (Date.now() > deadline) {
      throw new Error(
        `${key}'s coverage counts ${JSON.stringify([...counted])}, want one check for each statement`
      );
    }
    await pause(100);
  }
  await recordCheckResult(
    key,
    {
      check: LINKED_BLOCKS.check,
      commit: HEAD_SHA,
      observed: "1 passed",
      role: "tester",
      run: "bun run e2e -- e2e/issue-page-v.e2e.ts",
      surface: "browser",
      verdict: "pass",
    },
    { actor: recorderActor, as: "agent" }
  );

  const spec = ((await getIssue(key)).artifacts ?? []).find((artifact) => artifact.primary);
  expect(spec, `${key} carries no primary document`).toBeDefined();
  const specSlug = spec?.slug ?? "";
  return {
    child: child.key,
    key,
    nodes: {
      a: blockNodeId(specSlug, LINKED_BLOCKS.a),
      b: blockNodeId(specSlug, LINKED_BLOCKS.b),
      c: blockNodeId(specSlug, LINKED_BLOCKS.c),
      check: blockNodeId(verification.artifact.slug, LINKED_BLOCKS.check),
    },
  };
}
