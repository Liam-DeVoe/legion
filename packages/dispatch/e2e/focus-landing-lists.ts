/** The project List's and the project Board's cases of `focus-landing.e2e.ts`: the rows each runs,
 *  and how each case stages its shape. Where a case follows a row of `table-controls.e2e.ts`, it
 *  names that row by its title. */

import type { APIResponse, Locator, Route } from "@playwright/test";

import { REQUEST_DEADLINE_MS } from "../web/src/api/client";
import { CONTROL } from "../web/src/components/control-names";
import { createIssue, patchIssue } from "./api";
import { control, controlRow, listTrigger, pickFromList } from "./filters";
import {
  type Arrange,
  type CaseContext,
  chipCase,
  freshIssue,
  pressEnter,
  type Scene,
  type SceneRow,
  writeFromOtherDevice,
} from "./focus-landing-cases";
import { nextFrames, settledScrollTop } from "./frames";
import { openIssuesTab, openSurface, seedSurfaces, setBoardLanes } from "./table-controls";
import { expect, type Page } from "./test";

/** The live and the device cases of a page that draws a control for one fresh issue: `open`
 *  makes the issue, opens the page and puts the focus on that control. Closing the issue takes
 *  the control away: through the API in the live case, and on the other device in the device
 *  case. */
export function closedIssueCases(
  open: (context: CaseContext) => Promise<{ control: Locator; key: string }>
): SceneRow["cases"] {
  const closedBy =
    (close: (context: CaseContext, key: string) => Promise<unknown>): Arrange =>
    async (context) => {
      const opened = await open(context);
      return {
        answered: () => expect(opened.control).toHaveCount(0),
        cause: () => close(context, opened.key),
        caused: false,
        control: opened.control,
      };
    };
  return {
    live: { arrange: closedBy((_context, key) => patchIssue(key, { status: "done" })) },
    device: {
      arrange: closedBy(({ browser }, key) =>
        writeFromOtherDevice(browser, "PATCH", `/api/v1/issues/${key}`, { status: "done" })
      ),
    },
  };
}

/** The List's selection bar. */
function listBar(page: Page): Locator {
  return page.getByRole("toolbar", { name: "Selected issues" });
}

/** The checkbox that marks issue `key`'s row on the List. */
function markOf(page: Page, key: string): Locator {
  return page.getByRole("checkbox", { exact: true, name: `Select ${key}` });
}

/** A fresh issue's row on the project List, with the focus on its checkbox, marked by Space
 *  where the case needs a selection. */
async function listRow(
  { n, page, scene }: CaseContext,
  marked: boolean
): Promise<{ box: Locator; key: string }> {
  const key = await freshIssue(`List row ${n}`);
  await openSurface(page, "project List", scene);
  const box = markOf(page, key);
  await box.focus();
  if (marked) {
    await page.keyboard.press("Space");
    await expect(box).toBeChecked();
  }
  return { box, key };
}

/** An epic of the case's own holding `count` issues, and the List open on it by its Epic filter,
 *  its table's box brought to just below the app's header and scrolled half way down its rows,
 *  as the tester's row "a bulk action that takes the marked rows out of the project List's view
 *  hands the focus to the row now at their place, never the body or the pager" stages it. Hands
 *  back the issues the table draws, top to bottom, and of them those whose checkbox the reader
 *  sees, with nothing drawn over its centre. */
async function epicView(
  { n, page }: CaseContext,
  count: number
): Promise<{ order: string[]; shown: string[] }> {
  const title = `Table epic ${n}`;
  // `force`: a case's rows are near copies of another case's by design, which the create's
  // near-copy gate refuses with 409 POSSIBLE_DUPLICATE otherwise.
  const epic = await createIssue({ force: true, project: "CORE", title });
  await patchIssue(epic.key, { labels: ["epic"] });
  for (let index = 0; index < count; index++) {
    await createIssue({
      force: true,
      parent: epic.key,
      project: "CORE",
      title: `Epic row ${index} (${n})`,
    });
  }
  await openIssuesTab(page, "CORE", "List");
  await page.goto(`/projects/CORE/issues?f.epic=${encodeURIComponent(`${epic.key} · ${title}`)}`);
  const rows = page.locator("[data-issue-row]");
  await expect.poll(() => rows.count()).toBeGreaterThanOrEqual(Math.min(count, 6));
  const box = page.getByTestId("issues-table-scroll");
  await box.evaluate((node) => {
    window.scrollBy(0, node.getBoundingClientRect().top - 60);
    node.scrollTop = (node.scrollHeight - node.clientHeight) / 2;
  });
  await settledScrollTop(page);
  await settledScrollTop(box);
  const drawn = () =>
    page.evaluate(() => {
      const scroller = document.querySelector('[data-testid="issues-table-scroll"]');
      const frame = scroller?.getBoundingClientRect();
      const listed = [...document.querySelectorAll<HTMLElement>("[data-issue-row]")];
      listed.sort(
        (one, other) => one.getBoundingClientRect().top - other.getBoundingClientRect().top
      );
      const seen = (row: HTMLElement) => {
        const mark = row.querySelector('input[type="checkbox"]');
        if (mark === null || frame === undefined) return false;
        const own = mark.getBoundingClientRect();
        if (
          own.top < Math.max(0, frame.top) ||
          own.bottom > Math.min(window.innerHeight, frame.bottom)
        ) {
          return false;
        }
        const hit = document.elementFromPoint(own.left + own.width / 2, own.top + own.height / 2);
        return hit !== null && row.contains(hit);
      };
      return {
        order: listed.map((row) => row.getAttribute("data-issue-row") ?? ""),
        shown: listed.filter(seen).map((row) => row.getAttribute("data-issue-row") ?? ""),
      };
    });
  await expect.poll(async () => (await drawn()).shown.length).toBeGreaterThanOrEqual(3);
  // One read of the rows: the poll above found them drawn and the boxes settled, and nothing
  // moves them before the case's marks.
  return drawn();
}

/** Two adjacent rows in the middle of what the reader sees, marked by Space, the lower one first,
 *  so the row marked last is the upper one; and the row after them. */
async function twoMarkedMidView(
  page: Page,
  { order, shown }: { order: string[]; shown: string[] }
): Promise<{ after: string; taken: string[] }> {
  const at = order.indexOf(shown[Math.floor(shown.length / 2) - 1] ?? "");
  expect(
    at !== -1 && shown.includes(order[at + 1] ?? "") && at + 2 < order.length,
    `no two adjacent rows in sight mid-list (${shown.join(", ")})`
  ).toBe(true);
  const taken = [order[at] ?? "", order[at + 1] ?? ""];
  for (const key of [...taken].reverse()) {
    await markOf(page, key).focus();
    await page.keyboard.press("Space");
    await expect(markOf(page, key)).toBeChecked();
  }
  return { after: order[at + 2] ?? "", taken };
}

/** The tester's row "a bulk action that takes the marked rows out of the project List's view hands
 *  the focus to the row now at their place, never the body or the pager": Take out of epic on two
 *  rows marked mid-view, under the Epic filter, takes both out of the view. */
const takeOutOfEpic: Arrange = async (context) => {
  const { page } = context;
  const { after, taken } = await twoMarkedMidView(page, await epicView(context, 44));
  const takeOut = listBar(page).getByRole("button", { exact: true, name: "Take out of epic" });
  await takeOut.focus();
  return {
    answered: async () => {
      await expect(listBar(page)).toHaveCount(0);
      for (const key of taken) await expect(markOf(page, key)).toHaveCount(0);
    },
    cause: pressEnter(page),
    caused: true,
    control: takeOut,
    named: { at: markOf(page, after), name: `checkbox "Select ${after}"` },
    staged: { rule: "bar-marked" },
  };
};

/**
 * The tester's row "offline, Take out of epic hands the focus to the marked row's checkbox in the
 * render the bar goes, never the body or the pager": two rows marked mid-view under the Epic
 * filter, the upper one last, and Take out of epic pressed. Both writes land at Dispatch, and the
 * browser goes offline before the page has their answers, so the List's read after them cannot be
 * sent and both rows stay listed; the bar stays, its action held, until the read's bound ends at
 * the request deadline, on the wall clock, and then goes with its hand-off.
 */
const takeOutOffline: Arrange = async (context) => {
  const { page } = context;
  const { taken } = await twoMarkedMidView(page, await epicView(context, 24));
  const takeOut = listBar(page).getByRole("button", { exact: true, name: "Take out of epic" });
  await takeOut.focus();
  const browserContext = page.context();
  const written = new RegExp(`^/api/v1/issues/(${taken.join("|")})$`);
  const answers: { response: APIResponse; route: Route }[] = [];
  const heldReads: Route[] = [];
  let writing = false;
  const matches = (url: URL) => url.pathname === "/api/v1/issues" || written.test(url.pathname);
  const answerOffline = async (route: Route) => {
    const request = route.request();
    if (request.method() === "GET" && new URL(request.url()).pathname === "/api/v1/issues") {
      if (!writing) return route.fallback();
      heldReads.push(route);
      return;
    }
    if (request.method() !== "PATCH") return route.fallback();
    writing = true;
    answers.push({ response: await route.fetch(), route });
    // The action sends its writes together, so the second answer is the last one.
    if (answers.length < taken.length) return;
    await browserContext.setOffline(true);
    await page.waitForFunction(() => !navigator.onLine);
    for (const answer of answers) await answer.route.fulfill({ response: answer.response });
  };
  await page.route(matches, answerOffline);
  return {
    after: async () => {
      await browserContext.setOffline(false);
      for (const route of heldReads) await route.abort("internetdisconnected").catch(() => {});
      await page.unroute(matches, answerOffline);
    },
    answered: () => expect(takeOut).toHaveAttribute("aria-disabled", "true"),
    cause: pressEnter(page),
    caused: true,
    control: takeOut,
    named: { at: markOf(page, taken[0] ?? ""), name: `checkbox "Select ${taken[0]}"` },
    // The bar goes at the request deadline, on the wall clock, and the focus is read after it.
    settled: () => expect(listBar(page)).toHaveCount(0, { timeout: REQUEST_DEADLINE_MS + 15_000 }),
    staged: { rule: "bar-marked" },
  };
};

/** No row marked: every row on the page marked by the head's checkbox, which marks no row of its
 *  own, with the table's box scrolled half way, and the bar's Clear selection pressed. */
const noRowMarked: Arrange = async (context) => {
  const { page } = context;
  await epicView(context, 44);
  const every = page.getByRole("checkbox", { exact: true, name: "Select every row on this page" });
  await every.focus();
  await page.keyboard.press("Space");
  await expect(every).toBeChecked();
  const clear = listBar(page).getByRole("button", { exact: true, name: "Clear selection" });
  await clear.focus();
  return {
    answered: () => expect(listBar(page)).toHaveCount(0),
    cause: pressEnter(page),
    caused: true,
    control: clear,
    staged: { rule: "bar-first" },
  };
};

/** Nineteen short labels and one as long as a label may be, 40 characters, too long for a chips
 *  line to hold beside the short ones it holds: twenty, as many as an issue may carry, every one
 *  of which one issue of the case's own carries. */
const CHIP_LABELS = [
  ...Array.from({ length: 19 }, (_, index) => `chip-${String(index + 1).padStart(2, "0")}-label`),
  "chip-20-a-label-wider-than-a-line-leaves",
];

/** The List narrowed by `labels`, of those an issue of the case's own carries, a chip each, in
 *  that order, once the chips line has drawn and measured itself (`components/ControlRow.tsx`).
 *  Hands back the line's chips, `+N more`, and the panel `+N more` opens. */
async function chipsFor(
  page: Page,
  labels: readonly string[]
): Promise<{ line: Locator; more: Locator; panel: Locator }> {
  await page.goto(`/projects/CORE/issues?${labels.map((label) => `label=${label}`).join("&")}`);
  const row = controlRow(page.getByTestId("main-content"));
  await expect(control(row, CONTROL.chip).first()).toBeVisible();
  // The line reads its chips' widths after the commit that drew them and draws again by that
  // reading: two frames hold both.
  await nextFrames(page);
  await nextFrames(page);
  return {
    line: row.locator("[data-chip-strip] > *"),
    more: row.getByRole("button", { name: /^\d+ more filters?$/ }),
    panel: page.getByRole("dialog", { name: /^\d+ more filters?$/ }),
  };
}

/** An issue of the case's own carrying every label, and the List open on all twenty: more chips
 *  than the line holds at either width. Hands back how many chips the line holds then. */
async function lineHolds(context: CaseContext): Promise<number> {
  const { n, page } = context;
  const key = await freshIssue(`Chips ${n}`);
  await patchIssue(key, { labels: CHIP_LABELS });
  await openIssuesTab(page, "CORE", "List");
  const { line, more } = await chipsFor(page, CHIP_LABELS);
  await expect(more).toBeVisible();
  // One read of the line: it has drawn and measured itself, and nothing narrows the List now.
  return line.count();
}

/** The last chip the line draws, taken off by Enter: a chip of the panel may move into its place. */
const chipLineLast: Arrange = async (context) => {
  const { page } = context;
  await lineHolds(context);
  const { line } = await chipsFor(page, CHIP_LABELS);
  // One read of the chip's name: the line has drawn and measured itself with `+N more` after it.
  const name = (await line.last().getAttribute("aria-label")) ?? "";
  const chip = page.getByRole("button", { exact: true, name });
  await chip.focus();
  return {
    answered: () => expect(chip).toHaveCount(0),
    cause: pressEnter(page),
    caused: true,
    control: chip,
  };
};

/** The panel's one chip, taken off by Enter: the panel closes, and `+N more` goes with it. The
 *  List is narrowed by as many short labels as the line holds beside `+N more`, and the long one
 *  after them, which no line holds: `+1 more` stands at the line's end, and opens the panel with
 *  the focus on that chip. */
const chipPanelLast: Arrange = async (context) => {
  const { page } = context;
  // One read of the line's chips: it has drawn and measured itself, and the List is narrowed by
  // that many short labels.
  const holds = await lineHolds(context);
  const shown = await chipsFor(page, [...CHIP_LABELS.slice(0, holds), CHIP_LABELS.at(-1) ?? ""]);
  await expect(shown.more).toHaveAccessibleName("1 more filter");
  await shown.more.focus();
  await page.keyboard.press("Enter");
  const chip = shown.panel.getByRole("button");
  await expect(chip).toBeFocused();
  return {
    answered: () => expect(shown.panel).toHaveCount(0),
    cause: pressEnter(page),
    caused: true,
    control: chip,
  };
};

/** The List's rows. */
export const LIST_SCENE: Scene = {
  seed: seedSurfaces,
  rows: [
    {
      what: "a press, a live update or another device",
      cases: {
        // Clear selection with one row marked: the bar goes, and the row marked last stands.
        press: {
          arrange: async (context) => {
            const { box, key } = await listRow(context, true);
            const clear = listBar(context.page).getByRole("button", {
              exact: true,
              name: "Clear selection",
            });
            await clear.focus();
            return {
              answered: () => expect(listBar(context.page)).toHaveCount(0),
              cause: pressEnter(context.page),
              caused: true,
              control: clear,
              named: { at: box, name: `checkbox "Select ${key}"` },
              staged: { rule: "bar-marked" },
            };
          },
        },
        ...closedIssueCases(async (context) => {
          const { box, key } = await listRow(context, false);
          return { control: box, key };
        }),
      },
    },
    {
      what: "the selection bar's going",
      cases: {
        "bar-marked-leaves": { arrange: takeOutOfEpic },
        "bar-none-marked": { arrange: noRowMarked },
        "bar-emptied": {
          none: "every List view a bar action takes rows out of is an Epic filter's, which lists the epic itself beside its issues, and the bar takes no epic out of that view, so the view keeps a row",
        },
        "bar-marked-taken": {
          none: "every action of the List's bar changes the issues it marks and none deletes one, so a marked row leaves the view rather than being taken away (the case above)",
        },
      },
    },
    {
      what: "the reader taking a chip off the line or off its +N more panel",
      cases: {
        "chip-line-last": { arrange: chipLineLast },
        "chip-panel-last": { arrange: chipPanelLast },
        "chip-panel-fits": {
          none: "a chip taken off in the panel frees the line only the one digit `+N more` may lose, a few pixels, so a panel chip moves into the line only where the line's slack is already that small, and nothing a case sets decides the chips' widths in each engine",
        },
      },
    },
    {
      what: "Take out of epic whose re-read is offline past the request deadline",
      cases: { "offline-take-out": { arrange: takeOutOffline } },
    },
  ],
};

/** A fresh issue's card on the Board, drawn with no lanes, with the focus on its priority. */
async function boardCard({ n, page, scene }: CaseContext): Promise<{
  key: string;
  priority: Locator;
}> {
  const key = await freshIssue(`Board card ${n}`);
  const { root } = await openSurface(page, "project Board", scene);
  await setBoardLanes(page, "hidden");
  const priority = listTrigger(root, `Priority of ${key}`);
  await priority.focus();
  return { key, priority };
}

/** The Board's rows. */
export const BOARD_SCENE: Scene = {
  seed: seedSurfaces,
  rows: [
    {
      what: "a press, a live update or another device",
      cases: {
        // Every seeded issue holds an open ask and none is claimed, so both narrowings keep every
        // seeded card and draw a chip each.
        press: {
          arrange: async ({ page, scene }) => {
            const { row } = await openSurface(page, "project Board", scene);
            await page.goto(`/projects/${scene.project}/issues?needs-you=1&unclaimed=1`);
            return chipCase(page, row);
          },
        },
        // A done issue's card is drawn in the Done column, which the Board draws folded.
        ...closedIssueCases(async (context) => {
          const { key, priority } = await boardCard(context);
          return { control: priority, key };
        }),
      },
    },
    {
      what: "an offline press past the request deadline",
      cases: {
        // A pick draws the new priority at once and the list's trigger takes a press while its
        // write is out, so with no lanes the card stays where it is, and the pick hands the focus
        // back to the trigger.
        "offline-priority": {
          arrange: async (context) => {
            const { key, priority } = await boardCard(context);
            return {
              answered: () => expect(context.page.getByTestId("pending-writes")).toBeVisible(),
              cause: () => pickFromList(priority, "P1"),
              caused: true,
              control: priority,
              named: { at: priority, name: `button "Priority of ${key} P1"` },
              staged: { rule: "kept" },
            };
          },
          offline: true,
        },
      },
    },
  ],
};
