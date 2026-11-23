/** What a case of `focus-landing.e2e.ts` is made of, and the helpers its pages' cases share. Each
 *  page's cases are in a module of their own (`focus-landing-inbox.ts`, `focus-landing-lists.ts`,
 *  `focus-landing-pages.ts`); the row runs them and reads them through `focus-landing-oracle.ts`. */

import type { Browser, Locator } from "@playwright/test";

import { CONTROL } from "../web/src/components/control-names";
import { baseUrl, createIssue, dashboardOrigin, patchIssue } from "./api";
import { control } from "./filters";
import type { Rule } from "./focus-landing-oracle";
import type { SurfaceScene } from "./table-controls";
import { expect, type Page } from "./test";
import { asUser } from "./users";

/** A control a case names its landing by, by hand, beside the computed one: its role and its
 *  accessible name, and that name as the case prints it. Both must agree. */
export interface Named {
  readonly at: Locator;
  readonly name: string;
}

/** A press whose write or read the case holds, so the page is read while it is out: the focus is
 *  to stay on the control the reader pressed, or inside its open modal dialog, and then to land by
 *  the rules once the write or read is let through. */
export interface Held {
  /** What the reader does while the write is out, such as a Tab inside the held dialog. */
  readonly during?: () => Promise<unknown>;
  /** Lets the held write or read through. */
  readonly release: () => Promise<unknown>;
  /** Waits until the page shows what the write or read did once let through. */
  readonly landed: () => Promise<unknown>;
}

/** One case, with its page open and the focus put on the control the cause takes away. */
export interface Arranged {
  /** The control that holds the focus when the cause comes. */
  readonly control: Locator;
  /** The cause: the reader's press, or the write made through the API or on the other device. */
  readonly cause: () => Promise<unknown>;
  /** Waits until the page shows what the cause did: the control gone, or held while its write is
   *  out. */
  readonly answered: () => Promise<unknown>;
  /** Whether the reader caused the loss: their own press, online or offline. */
  readonly caused: boolean;
  readonly named?: Named;
  readonly held?: Held;
  /** Waits, once the request deadline has passed or the held write has landed, until the page
   *  shows the end of what the cause started, such as a selection bar's going at the deadline,
   *  so the focus is read after it. */
  readonly settled?: () => Promise<unknown>;
  /** The shape the case stages, which the computation must find: the step of the rules that is to
   *  choose the landing, and the kinds of control the walk back is to pass over, each at least once
   *  (`Verdict.passed`). A case whose page did not stage its shape fails for that, by name. */
  readonly staged?: {
    readonly rule?: Rule;
    readonly passes?: readonly ("other-item" | "text" | "unseen" | "untabbed")[];
  };
  /** Leaves the seed as the next case needs it, once the case is read. */
  readonly after?: () => Promise<unknown>;
}

/** What a case is arranged with. */
export interface CaseContext {
  readonly browser: Browser;
  /** A number of the case's own, for the titles and names it seeds. */
  readonly n: number;
  readonly page: Page;
  /** What the row's seed handed back, which the page's opener reads. */
  readonly scene: SurfaceScene;
  /** The window's width, 1440 or 390. */
  readonly width: number;
}

export type Arrange = (context: CaseContext) => Promise<Arranged>;

/** A case a row runs. `offline` makes it the reader's press made with the browser offline and read
 *  once the request deadline has passed; such a case installs the page's own clock, which stays for
 *  the rest of the row, so a row runs its offline cases last. `only` runs it at one width alone and
 *  names why the other width has no case. */
export interface Case {
  readonly arrange: Arrange;
  readonly offline?: boolean;
  readonly only?: { readonly width: number; readonly none: string };
}

/** A case a page cannot reach, with why. */
export interface NoCase {
  readonly none: string;
}

/** One row of a page at each width: what its title names, its cases in the order it runs them,
 *  and the seed it makes in place of the page's own, where it needs another. */
export interface SceneRow {
  readonly what: string;
  readonly cases: Readonly<Record<string, Case | NoCase>>;
  readonly seed?: () => Promise<SurfaceScene>;
}

/** A page's rows: the seed each row makes once, and the rows. */
export interface Scene {
  readonly seed: () => Promise<SurfaceScene>;
  readonly rows: readonly SceneRow[];
}

/** The press every case of the reader's own makes, on the control that holds the focus. */
export function pressEnter(page: Page): () => Promise<void> {
  return () => page.keyboard.press("Enter");
}

/** A write made from a second context signed in as the reader: another device of theirs, whose
 *  change reaches the first page only by that page's own read. */
export async function writeFromOtherDevice(
  browser: Browser,
  method: "DELETE" | "PATCH",
  path: string,
  data?: object
): Promise<void> {
  const other = await asUser(browser, "alice");
  try {
    const response = await other.request.fetch(new URL(path, baseUrl).toString(), {
      data,
      headers: { Origin: dashboardOrigin },
      method,
    });
    expect(response.ok(), `${method} ${path} from the other device: ${response.status()}`).toBe(
      true
    );
  } finally {
    await other.close();
  }
}

/** An open issue of CORE with a row or a card of its own, so a case's control is its own. */
export async function freshIssue(title: string): Promise<string> {
  // `force`: cases make near copies of each other's issues by design, which the create's
  // near-copy gate refuses with 409 POSSIBLE_DUPLICATE otherwise.
  const issue = await createIssue({ force: true, project: "CORE", title });
  await patchIssue(issue.key, { status: "todo" });
  return issue.key;
}

/** The first of the chips a narrowing draws in `row`, taken off by the reader's own press, with
 *  the focus put on it first. The chip after it then stands at its place, so the case names that
 *  chip by hand. A chip is named by its `aria-label`, else by its text without the × it draws. */
export async function chipCase(page: Page, row: Locator): Promise<Arranged> {
  const chips = control(row, CONTROL.chip);
  await expect(chips).toHaveCount(2);
  const names = await chips.evaluateAll((all) =>
    all.map((chip) =>
      (chip.getAttribute("aria-label") ?? chip.textContent ?? "").replace("×", "").trim()
    )
  );
  const first = row.getByRole("button", { exact: true, name: names[0] ?? "" });
  const next = names[1] ?? "";
  await first.focus();
  return {
    answered: () => expect(chips).toHaveCount(1),
    cause: pressEnter(page),
    caused: true,
    control: first,
    named: { at: row.getByRole("button", { exact: true, name: next }), name: `button "${next}"` },
  };
}

/** Puts the window, or the box that scrolls around `element`, so that `element`'s top stands at
 *  `fraction` of what that box shows: the reader's own scroll, made before the case's press. */
export async function scrollTo(element: Locator, fraction: number): Promise<void> {
  await element.evaluate((node, at) => {
    const by = node.getBoundingClientRect().top;
    for (let box = node.parentElement; box !== null; box = box.parentElement) {
      const { overflowY } = getComputedStyle(box);
      if ((overflowY === "auto" || overflowY === "scroll") && box.scrollHeight > box.clientHeight) {
        const view = box.getBoundingClientRect();
        box.scrollTop += by - (view.top + view.height * at);
        return;
      }
    }
    window.scrollBy(0, by - window.innerHeight * at);
  }, fraction);
}
