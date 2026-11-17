import type { Locator, Page } from "@playwright/test";

import {
  type Body,
  type Op,
  type PathParams,
  type PathsOf,
  type Result,
  routeUrl,
} from "../web/src/api/paths";
import type { NodeType } from "../web/src/features/issue-page/page-words";
import { baseUrlOf, getIssueCoverage, getIssueResults, userHeaders } from "./api";
import { issuePage } from "./issue-page";

/**
 * What the rows of part W of dispatch://INFRA-1196 (dispatch://INFRA-4065,
 * `e2e/issue-page-w.e2e.ts`) write and read, and what part X writes its reserved nodes with:
 * the lenses, findings, risks and constraints of an issue, and the rows and lines the new issue
 * page draws for them (`web/src/features/issue-page/lenses/`).
 *
 * - **The writers** send one request each as alice, through the server's own routes
 *   (`packages/envoy/internal/dispatch/api/reasoning_routes.go`), each under an
 *   `Idempotency-Key` of its own, and answer the body the route answers. A refusal throws,
 *   naming the method, the path, the status and the body.
 * - **The seed** `seedLenses` writes, on an issue `seedWholeIssue` made, the lenses, risks and
 *   constraints the Design's "The rows" names, and takes every address it does not write from a
 *   read of the server.
 * - **The readers**: the rows `[data-node-row]` of the four types, each line `[data-lens-line]`
 *   under a row's words (its label `[data-page-chrome]`, its chips `[data-node-chip]` with their
 *   words `[data-chip-name]`, and its stored words `[data-author-text]`), the focused row's act
 *   `[data-row-lens]`, the menu it opens (`role="menu"`, each `role="menuitem"` holding a kind's
 *   words `[data-lens-kind-words]` and its standard `[data-lens-kind-standard]`), and the page's
 *   own words.
 */

/** What `POST /api/v1/issues/{key}/lenses` answers: the lens, with its findings. */
export type LensInvoked = Result<"/api/v1/issues/{key}/lenses", "post">;

/** What `POST /api/v1/issues/{key}/nodes/{id}/findings` answers: the finding. */
export type LensFinding = Result<"/api/v1/issues/{key}/nodes/{id}/findings", "post">;

/** What `POST /api/v1/issues/{key}/risks` answers: the risk. */
export type RiskWritten = Result<"/api/v1/issues/{key}/risks", "post">;

/** What `POST /api/v1/issues/{key}/constraints` answers: the constraint. */
export type ConstraintRead = Result<"/api/v1/issues/{key}/constraints", "post">;

/** What `POST /api/v1/issues/{key}/nodes/{id}/pointers` answers: both ends of the tuple. */
export type PointerWritten = Result<"/api/v1/issues/{key}/nodes/{id}/pointers", "post">;

/** Who a write is for: `project`, the default, or `author`, which keeps it to alice. */
type Visibility = Pick<Body<"/api/v1/issues/{key}/constraints", "post">, "visibility">;

/** A constraint that is a standard, with the reference to the part of it that applies. */
export interface StandardConstraint {
  readonly reference: string;
  readonly standard: string;
}

/** A constraint that is a measurement, with its value and where it was taken. */
export interface MeasurementConstraint {
  readonly measurement: string;
  readonly taken_where: string;
  readonly value: string;
}

/** One write as alice: `body` posted as JSON to `pattern` with `params` in its path, each value
 *  percent-encoded as one segment (`routeUrl`), under an `Idempotency-Key` of its own. */
async function write<P extends PathsOf<"post">>(
  pattern: P,
  params: PathParams<Op<P, "post">>,
  body: Body<P, "post">
): Promise<Result<P, "post">> {
  const path = routeUrl(pattern, params);
  const response = await fetch(new URL(path, baseUrlOf()), {
    body: JSON.stringify(body),
    headers: {
      ...(await userHeaders("alice")),
      "Content-Type": "application/json",
      "Idempotency-Key": crypto.randomUUID(),
    },
    method: "POST",
  });
  if (!response.ok) {
    throw new Error(`POST ${path} failed: ${response.status} ${await response.text()}`);
  }
  return (await response.json()) as Result<P, "post">;
}

/** One read as alice of `pattern` with `params` in its path. */
async function read<P extends PathsOf<"get">>(
  pattern: P,
  params: PathParams<Op<P, "get">>
): Promise<Result<P, "get">> {
  const path = routeUrl(pattern, params);
  const response = await fetch(new URL(path, baseUrlOf()), {
    headers: await userHeaders("alice"),
  });
  if (!response.ok) {
    throw new Error(`GET ${path} failed: ${response.status} ${await response.text()}`);
  }
  return (await response.json()) as Result<P, "get">;
}

// ---------------------------------------------------------------------------------------------
// The writers.
// ---------------------------------------------------------------------------------------------

/** Invokes a lens of the catalogue's `kind` (a `lens_kind` id, such as `trade-off`) on the node
 *  of issue `key` that `on` addresses, with its verdict and the lens that triggered it where they
 *  are given. The answer's `address` is the lens's, `dispatch://lens/<uuid>`. */
export function invokeLens(
  key: string,
  input: Omit<Body<"/api/v1/issues/{key}/lenses", "post">, "actor" | "findings">
): Promise<LensInvoked> {
  return write("/api/v1/issues/{key}/lenses", { key }, input);
}

/** Adds one finding holding `text` to the lens `lens` addresses: the `address` `invokeLens`
 *  answers, which the route takes whole as its `{id}` (`FindNodeUnderIssue`). */
export function addFinding(
  key: string,
  lens: string,
  text: string,
  { visibility }: Visibility = {}
): Promise<LensFinding> {
  return write("/api/v1/issues/{key}/nodes/{id}/findings", { id: lens, key }, { text, visibility });
}

/** Writes one risk under the pre-mortem lens `input.lens` addresses, prevented by the check
 *  block `input.prevented_by` addresses where it is given. */
export function writeRisk(
  key: string,
  input: Omit<Body<"/api/v1/issues/{key}/risks", "post">, "actor">
): Promise<RiskWritten> {
  return write("/api/v1/issues/{key}/risks", { key }, input);
}

/** Writes one constraint under issue `key`: a standard with its reference, or a measurement
 *  with its value and where it was taken. */
export function writeConstraint(
  key: string,
  shape: StandardConstraint | MeasurementConstraint,
  { visibility }: Visibility = {}
): Promise<ConstraintRead> {
  return write("/api/v1/issues/{key}/constraints", { key }, { ...shape, visibility });
}

/** Appends one pointer tuple of `kind` out of the node `node` addresses to the node `to`
 *  addresses. Both take a node's `dispatch://` address: a statement's is the coverage read's
 *  `ref`, and a constraint's the `address` `writeConstraint` answers, or a catalogue
 *  constraint's `ref` from `GET /api/v1/schema/blocks`. */
export function pointAt(
  key: string,
  node: string,
  kind: Body<"/api/v1/issues/{key}/nodes/{id}/pointers", "post">["kind"],
  to: string
): Promise<PointerWritten> {
  return write(
    "/api/v1/issues/{key}/nodes/{id}/pointers",
    { id: node, key },
    { action: "append", kind, to }
  );
}

// ---------------------------------------------------------------------------------------------
// The seed.
// ---------------------------------------------------------------------------------------------

/** Every node `seedLenses` writes, by the server's answer for it, and every node it read the
 *  address of, by that address. */
export interface LensSeed {
  /** The constraint of the catalogue statement 6 is constrained by: the first source of the
   *  catalogue's first lens kind. */
  readonly catalogueConstraint: string;
  /** The first check of the Verification document, which prevents risk A. */
  readonly check1: string;
  /** The trade-off analysis's one finding. */
  readonly finding: LensFinding;
  /** The measurement constraint, which no statement points at. */
  readonly measurement: ConstraintRead;
  /** The pre-mortem on statement 1, triggered by the trade-off analysis, with a verdict. */
  readonly preMortem: LensInvoked;
  /** The risk under the pre-mortem that check 1 prevents. */
  readonly riskA: RiskWritten;
  /** The risk under the pre-mortem that nothing prevents. */
  readonly riskB: RiskWritten;
  /** The standard constraint, which statement 5 is constrained by. */
  readonly standard: ConstraintRead;
  /** The first, fifth and sixth statements of the spec, in document order. */
  readonly statements: { readonly five: string; readonly one: string; readonly six: string };
  /** The trade-off analysis on statement 1, with no verdict. */
  readonly tradeOff: LensInvoked;
}

/**
 * Writes, on the issue `key` that `seedWholeIssue` made, as the Design's "The rows" names it: a
 * trade-off analysis on statement 1 with one finding and no verdict; a pre-mortem on statement 1
 * triggered by it, with a verdict, and two risks under it, A prevented by check 1 and B by
 * nothing; a standard and a measurement constraint; and statement 5 constrained by the standard
 * and statement 6 by the catalogue's `lens_kinds[0].sources[0].ref`. Each statement's and the
 * check's address is read from the server (the coverage read's `ref`, the results read's check
 * `ref`), never built. The lens kinds are the catalogue's ids (migration
 * `20261116123354_lens_catalogue.up.sql`). The writes go one after another, so the lenses of
 * statement 1 stand in the order written.
 */
export async function seedLenses(key: string): Promise<LensSeed> {
  const [coverage, results, catalogue] = await Promise.all([
    getIssueCoverage(key),
    getIssueResults(key),
    read("/api/v1/schema/blocks", {}),
  ]);
  const statement = (place: number): string => {
    const ref = coverage.requirements?.[place - 1]?.ref;
    if (ref === undefined)
      throw new Error(`the coverage read of ${key} holds no statement ${place}`);
    return ref;
  };
  const statements = { five: statement(5), one: statement(1), six: statement(6) };
  const check1 = results.checks?.[0]?.ref;
  if (check1 === undefined) throw new Error(`the results read of ${key} holds no check`);
  const catalogueConstraint = catalogue.lens_kinds?.[0]?.sources?.[0]?.ref;
  if (catalogueConstraint === undefined) {
    throw new Error("the catalogue's first lens kind holds no source");
  }

  const tradeOff = await invokeLens(key, { kind: "trade-off", on: statements.one });
  const finding = await addFinding(
    key,
    tradeOff.address,
    "Folding the tree to the frontier saves a screen and costs a press for each branch."
  );
  const preMortem = await invokeLens(key, {
    kind: "pre-mortem",
    on: statements.one,
    triggered_by: tradeOff.address,
    verdict: "Ship it once every folded branch says how many rows it holds.",
  });
  const riskA = await writeRisk(key, {
    early_warning: "Questions are answered late on issues whose branches stay folded.",
    heading: "A reader misses a question folded under a closed branch.",
    lens: preMortem.address,
    prevented_by: check1,
    prevention: "The fold budget never folds a branch that holds an open question.",
  });
  const riskB = await writeRisk(key, {
    early_warning: "The tree read takes longer on issues that hold many lenses.",
    heading: "The page grows slower with every lens written under a statement.",
    lens: preMortem.address,
    prevention: "The tree read answers every lens of the issue in one request.",
  });
  const standard = await writeConstraint(key, {
    reference: "Success Criterion 2.4.11, Focus Not Obscured (Minimum)",
    standard: "Web Content Accessibility Guidelines 2.2",
  });
  const measurement = await writeConstraint(key, {
    measurement: "Rows drawn on the first screen",
    taken_where: "a 1440 by 900 desktop window on the seeded issue",
    value: "42",
  });
  await pointAt(key, statements.five, "constrained_by", standard.address);
  await pointAt(key, statements.six, "constrained_by", catalogueConstraint);

  return {
    catalogueConstraint,
    check1,
    finding,
    measurement,
    preMortem,
    riskA,
    riskB,
    standard,
    statements,
    tradeOff,
  };
}

// ---------------------------------------------------------------------------------------------
// The readers.
// ---------------------------------------------------------------------------------------------

/** The types of the rows part W draws. */
const LENS_ROW_TYPES = [
  "lens",
  "finding",
  "risk",
  "constraint",
] as const satisfies readonly NodeType[];

/** The selectors the readers pass into the browser. */
const SELECTORS = {
  authorText: "[data-author-text]",
  chip: "[data-node-chip]",
  chipName: "[data-chip-name]",
  kindStandard: "[data-lens-kind-standard]",
  kindWords: "[data-lens-kind-words]",
  lensLine: "[data-lens-line]",
  nodeRow: "[data-node-row]",
  pageChrome: "[data-page-chrome]",
} as const;

/** One chip of a lens line: the words it names its target by, and the link to the target's row
 *  (`/issues/<key>?node=<id>`), or null where it draws the words alone. */
export interface LensLineChip {
  readonly href: string | null;
  readonly words: string;
}

/** One line a row draws under its words from its lens facts. */
export interface LensLine {
  /** Its chips, in order. */
  readonly chips: readonly LensLineChip[];
  /** Its label, the page's own words; empty on a line that draws none (`kind`, `reference`). */
  readonly label: string;
  /** Its `data-lens-line`: the `LENS_WORDS` key of its label, or `kind` or `reference`. */
  readonly line: string;
  /** The stored words it draws outside its chips; empty on a line that draws none. */
  readonly words: string;
}

/** One kind of the lens menu, as its item draws it. */
export interface LensMenuItem {
  /** The kind's primary source (its standard). */
  readonly standard: string;
  /** The kind's words. */
  readonly words: string;
}

/** Every row of the page that stands for a lens, a finding, a risk or a constraint. */
export function lensRows(page: Page): Locator {
  return issuePage(page).locator(
    LENS_ROW_TYPES.map((type) => `${SELECTORS.nodeRow}[data-node-type="${type}"]`).join(", ")
  );
}

/** The lines one row draws under its words, in order, read from the row `row` locates. A line
 *  of a row drawn inside it is that row's, and is left out. */
export function lensLines(row: Locator): Promise<LensLine[]> {
  return row.evaluate((rowElement, selectors) => {
    const text = (elements: readonly Element[]): string =>
      elements
        .map((element) => element.textContent ?? "")
        .join(" ")
        .replace(/\s+/g, " ")
        .trim();
    return [...rowElement.querySelectorAll(selectors.lensLine)]
      .filter((line) => line.closest(selectors.nodeRow) === rowElement)
      .map((line) => {
        // The label and the stored words stand outside the chips, whose names are stored words.
        const loose = [
          ...line.querySelectorAll(`${selectors.pageChrome}, ${selectors.authorText}`),
        ].filter((element) => element.closest(selectors.chip) === null);
        return {
          chips: [...line.querySelectorAll(selectors.chip)].map((chip) => {
            const name = chip.querySelector(selectors.chipName);
            return {
              href: name?.closest("a[href]")?.getAttribute("href") ?? null,
              words: text(name === null ? [] : [name]),
            };
          }),
          label: text(loose.filter((element) => element.matches(selectors.pageChrome))),
          line: line.getAttribute("data-lens-line") ?? "",
          words: text(loose.filter((element) => element.matches(selectors.authorText))),
        };
      });
  }, SELECTORS);
}

/** The focused row's Lens act, which opens the menu of lens kinds. */
export function lensAct(row: Locator): Locator {
  return row.locator("[data-row-lens]");
}

/** The menu of lens kinds a row's Lens act opened. */
export function lensMenu(page: Page): Locator {
  return page.locator('[data-node-row] [role="menu"]');
}

/** Each item of the menu `menu` locates, in order, by the kind's words and its standard. */
export function menuItems(menu: Locator): Promise<LensMenuItem[]> {
  return menu.locator('[role="menuitem"]').evaluateAll(
    (items, selectors) =>
      items.map((item) => ({
        standard: (item.querySelector(selectors.kindStandard)?.textContent ?? "").trim(),
        words: (item.querySelector(selectors.kindWords)?.textContent ?? "").trim(),
      })),
    SELECTORS
  );
}

/** The text of each element of the page marked as its own words (`[data-page-chrome]`), less
 *  what an author wrote inside it (`[data-author-text]`), as `page-words.ts` reads the page's
 *  words against `DECLARED_WORDS`. */
export function chromeTexts(page: Page): Promise<string[]> {
  return issuePage(page)
    .locator(SELECTORS.pageChrome)
    .evaluateAll(
      (elements, selectors) =>
        elements.map((element) => {
          const own = element.cloneNode(true) as Element;
          for (const authored of own.querySelectorAll(selectors.authorText)) authored.remove();
          return (own.textContent ?? "").replace(/\s+/g, " ").trim();
        }),
      SELECTORS
    );
}
