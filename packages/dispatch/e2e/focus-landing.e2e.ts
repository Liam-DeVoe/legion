/** Where the keyboard focus lands when a control that holds it is taken away, on each of the
 *  eight pages of INFRA-3135's R2, by each of four causes: the reader's own press, a live update
 *  the server draws on the open page, the same reader's change made on another device, and the
 *  reader's press made while the browser is offline and left past the request deadline.
 *
 *  The rule each case holds:
 *
 *  - A loss the reader caused - their press, online or offline - lands in the region the control
 *    was lost from while that region stands: the nearest dialog, panel or selection bar around
 *    it, else the page's main content. A selection bar that goes hands the focus to the row
 *    marked last. Inside the region, the focus lands on what the press drew in the control's
 *    place, else on the same control in the row now at its place, else on the nearest control
 *    before that place that the reader can see, that is in the Tab order and that takes no typed
 *    text, else on the region's own stop. It never lands on the body, and never behind an open
 *    modal dialog. Each case names its landing by role and accessible name and asserts that name.
 *    It scrolls nothing, save the least scroll that shows a landing control that was partly out
 *    of sight: no box moves by more than that control's height, or its width sideways.
 *  - A loss the reader did not cause - a live update, or their other device - leaves the focus on
 *    the body, as the page did before it had a focus rescue, or on an open modal dialog's own stop
 *    where the loss was inside one. Nothing scrolls, and the focus never lands behind an open
 *    modal dialog. The body is the right landing for such a loss, and no defect.
 *
 *  A box that moved without the focus moving it is no scroll in either rule. The page clamps a
 *  box whose content grew shorter than its scroll: it now stands at the farthest it can scroll,
 *  and stood farther before. A box keeps its view when it moves by what the layout moved, as the
 *  List's anchor makes up for a row a live update removed: an element that was in view in it
 *  still stands where it stood on screen. Each case's line names such a move `(clamped)` or
 *  `(view kept)`, and fails only a move that is neither and is more than its allowance.
 *
 *  Each case prints one line, `LANDING <page> <cause> <width> <engine> expected=<...> got=<...>
 *  scrolled=<...>`, so a run reads case by case. Each page has two rows, one running its four
 *  cases at 1440x900 and one at 390x664, each setting the window itself as the three-engine rows
 *  of `table-controls.e2e.ts` do, so the phone project runs none of them.
 */

import type { Browser, ElementHandle, Locator } from "@playwright/test";

import { REQUEST_DEADLINE_MS } from "../web/src/api/client";
import { CONTROL } from "../web/src/components/control-names";
import { ROW_SELECTOR } from "../web/src/components/row-marker";
import { plannerSession, seedAgents } from "./agents";
import {
  baseUrl,
  createAsk,
  createIssue,
  createIssueArtifact,
  dashboardOrigin,
  deleteAsReader,
  markInboxRead,
  patchIssue,
  putAgentState,
} from "./api";
import { seedSessionGroupsFixture } from "./delivery-seed";
import { control, controlRow, openFilterPanel } from "./filters";
import { nextFrames } from "./frames";
import { seedAgentThread, seedCommentThread } from "./inbox-threads";
import {
  closeControlPanel,
  createSessionGroup,
  openSurface,
  REAL_STACK_SURFACES,
  STUBBABLE_SURFACES,
  SURFACE_SEARCH_TERM,
  SURFACE_SESSION,
  type SurfaceName,
  type SurfaceScene,
  seedSurfaces,
  setBoardLanes,
} from "./table-controls";
import { expect, type Page, test } from "./test";
import { asUser, withAlice } from "./users";

/** The two sizes a page's cases run at, a row each: a desktop window wide enough for the Inbox's
 *  reading column, and a phone's. */
const SIZES = [
  { height: 900, width: 1440 },
  { height: 664, width: 390 },
] as const;

/** The four causes, in the order each row runs them. The offline case is last: it installs the
 *  page's own clock, which stays for the rest of the row, runs that clock past the request
 *  deadline, and then waits for its write to land. */
const CAUSES = ["press", "live", "device", "offline"] as const;
type Cause = (typeof CAUSES)[number];

/** How far past the request deadline (`REQUEST_DEADLINE_MS`) an offline case reads the focus, on
 *  the page's own clock: room for the timers that fire at the deadline and for the frames, tasks
 *  and commits they start. A write goes late at the same mark (`SEND_DEADLINE_MS`,
 *  web/src/features/writes/keyed-write.ts), and a hold through the read after a write ends at it
 *  (`readWithinBound`, web/src/api/read-bound.ts). */
const PAST_THE_DEADLINE_MS = 2_000;

/** A day, for the brush window the Delivery page's press case opens on. */
const DAY_MS = 86_400_000;

/** A control the focus is to land on, found by its role and its accessible name, and that name as
 *  the case prints it. */
interface Named {
  readonly at: Locator;
  readonly name: string;
}

/** One case, with its page open and the focus put on the control the cause takes away. */
interface Arranged {
  /** The control that holds the focus when the cause comes. */
  readonly control: Locator;
  /** The cause: the reader's press, or the write made through the API or on the other device. */
  readonly cause: () => Promise<unknown>;
  /** Waits until the page shows what the cause did: the control gone, or held while its write is
   *  out. */
  readonly answered: () => Promise<unknown>;
  /** Where the focus is to land after a loss the reader caused. A case with none is a loss the
   *  reader did not cause. */
  readonly landing?: Named;
  /** Leaves the seed as the next case needs it, once the case is read. */
  readonly after?: () => Promise<unknown>;
}

/** What a case is arranged with. */
interface CaseContext {
  readonly browser: Browser;
  /** A number of the case's own, for the titles and names it seeds. */
  readonly n: number;
  readonly page: Page;
  /** What the row's seed handed back, which the page's opener reads. */
  readonly scene: SurfaceScene;
}

type Arrange = (context: CaseContext) => Promise<Arranged>;

/** A page's two rows, one at each size: the seed each row makes once, and each cause's case, or
 *  why the cause can take no control away on that page. */
interface Scene {
  readonly seed: () => Promise<SurfaceScene>;
  readonly cases: Readonly<Record<Cause, Arrange | { readonly none: string }>>;
}

/** The press every case of the reader's own makes, on the control that holds the focus. */
function pressEnter(page: Page): () => Promise<void> {
  return () => page.keyboard.press("Enter");
}

/** A write made from a second context signed in as the reader: another device of theirs, whose
 *  change reaches the first page only by that page's own read. */
async function writeFromOtherDevice(
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
async function freshIssue(title: string): Promise<string> {
  const issue = await createIssue({ project: "CORE", title });
  await patchIssue(issue.key, { status: "todo" });
  return issue.key;
}

/** Two unread comment threads and, newer than both, a question waiting on the reader. The
 *  threads keep Mark all read offered. The question is the newest row, so the Inbox selects it
 *  first, and its reading column shows no thread: a thread on screen there for a second is marked
 *  read, which would take Mark all read away before the cause. */
async function seedUnread(n: number): Promise<void> {
  for (const word of ["mirror", "ledger"]) {
    const issue = await createIssue({ project: "CORE", title: `Unread ${word} thread ${n}` });
    await seedCommentThread(issue.key, `Is the ${word} caught up? (${n})`, "It is.");
  }
  const asking = await createIssue({ project: "CORE", title: `Waiting question ${n}` });
  await createAsk(asking.key, { question: `Which goes first? (${n})` }, SURFACE_SESSION);
}

/** The Inbox's and the drawer's cases. The control is the row's Mark all read, which goes once no
 *  thread the list holds is unread, and whose press takes it away at once, before its write
 *  lands. */
function markAllCases(surface: "Inbox" | "Inbox drawer"): Scene["cases"] {
  const open = async ({ n, page, scene }: CaseContext) => {
    await seedUnread(n);
    const { row } = await openSurface(page, surface, scene);
    const markAll = row.getByRole("button", { exact: true, name: "Mark all read" });
    await expect(markAll).toBeVisible();
    await markAll.focus();
    return { markAll, row };
  };
  const pressed: Arrange = async (context) => {
    const { markAll, row } = await open(context);
    return {
      answered: () => expect(markAll).toHaveCount(0),
      cause: pressEnter(context.page),
      control: markAll,
      landing: {
        at: row.getByRole("button", { name: /^Blocked on you/ }),
        name: 'button "Blocked on you…"',
      },
    };
  };
  return {
    press: pressed,
    live: async (context) => {
      const { markAll } = await open(context);
      return {
        answered: () => expect(markAll).toHaveCount(0),
        cause: () => markInboxRead({ read_through: new Date().toISOString() }),
        control: markAll,
      };
    },
    device: async (context) => {
      const { markAll } = await open(context);
      return {
        answered: () => expect(markAll).toHaveCount(0),
        cause: () =>
          withAlice(context.browser, async (other) => {
            await other.goto("/");
            const theirs = controlRow(other.getByTestId("main-content")).getByRole("button", {
              exact: true,
              name: "Mark all read",
            });
            await theirs.click();
            await expect(theirs).toHaveCount(0);
          }),
        control: markAll,
      };
    },
    offline: pressed,
  };
}

/** A fresh issue's row on the project List, with the focus on its checkbox, marked by Space
 *  where the case needs a selection. */
async function listRow(
  { n, page, scene }: CaseContext,
  marked: boolean
): Promise<{ box: Locator; key: string }> {
  const key = await freshIssue(`List row ${n}`);
  const { root } = await openSurface(page, "project List", scene);
  const box = root.getByRole("checkbox", { exact: true, name: `Select ${key}` });
  await box.focus();
  if (marked) {
    await page.keyboard.press("Space");
    await expect(box).toBeChecked();
  }
  return { box, key };
}

/** The live and the device cases of a page that draws a control for one fresh issue: `open`
 *  makes the issue, opens the page and puts the focus on that control. Closing the issue takes
 *  the control away: through the API in the live case, and on the other device in the device
 *  case. */
function closedIssueCases(
  open: (context: CaseContext) => Promise<{ control: Locator; key: string }>
): Record<"device" | "live", Arrange> {
  const closedBy =
    (close: (context: CaseContext, key: string) => Promise<unknown>): Arrange =>
    async (context) => {
      const opened = await open(context);
      return {
        answered: () => expect(opened.control).toHaveCount(0),
        cause: () => close(context, opened.key),
        control: opened.control,
      };
    };
  return {
    live: closedBy((_context, key) => patchIssue(key, { status: "done" })),
    device: closedBy(({ browser }, key) =>
      writeFromOtherDevice(browser, "PATCH", `/api/v1/issues/${key}`, { status: "done" })
    ),
  };
}

const LIST_CASES: Scene["cases"] = {
  press: async (context) => {
    const { box, key } = await listRow(context, true);
    const bar = context.page.getByRole("toolbar", { name: "Selected issues" });
    const clear = bar.getByRole("button", { exact: true, name: "Clear selection" });
    await clear.focus();
    return {
      answered: () => expect(bar).toHaveCount(0),
      cause: pressEnter(context.page),
      control: clear,
      landing: { at: box, name: `checkbox "Select ${key}"` },
    };
  },
  ...closedIssueCases(async (context) => {
    const { box, key } = await listRow(context, false);
    return { control: box, key };
  }),
  // Take out of epic holds the bar busy, by `aria-disabled`, while its writes are out, so it
  // keeps the focus: offline its writes stay out past the deadline, and nothing takes it away.
  offline: async (context) => {
    await listRow(context, true);
    const takeOut = context.page
      .getByRole("toolbar", { name: "Selected issues" })
      .getByRole("button", { exact: true, name: "Take out of epic" });
    await takeOut.focus();
    return {
      answered: () => expect(takeOut).toHaveAttribute("aria-disabled", "true"),
      cause: pressEnter(context.page),
      control: takeOut,
      landing: { at: takeOut, name: 'button "Take out of epic"' },
    };
  },
};

/** The first of the two chips a narrowing draws in `row`, taken off by the reader's own press,
 *  with the focus put on it first. The focus is to go to the other chip, which then stands at
 *  its place. A chip is named by its `aria-label`, else by its text without the × it draws. */
async function chipCase(page: Page, row: Locator): Promise<Arranged> {
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
    control: first,
    landing: { at: row.getByRole("button", { exact: true, name: next }), name: `button "${next}"` },
  };
}

/** A fresh issue's card on the Board, drawn with no lanes, with the focus on its priority. */
async function boardCard({ n, page, scene }: CaseContext): Promise<{
  key: string;
  priority: Locator;
}> {
  const key = await freshIssue(`Board card ${n}`);
  const { root } = await openSurface(page, "project Board", scene);
  await setBoardLanes(page, "hidden");
  const priority = root.getByRole("combobox", { exact: true, name: `Priority of ${key}` });
  await priority.focus();
  return { key, priority };
}

const BOARD_CASES: Scene["cases"] = {
  // Every seeded issue holds an open ask and none is claimed, so both narrowings keep every
  // seeded card and draw a chip each.
  press: async ({ page, scene }) => {
    const { row } = await openSurface(page, "project Board", scene);
    await page.goto(`/projects/${scene.project}/issues?needs-you=1&unclaimed=1`);
    return chipCase(page, row);
  },
  // A done issue's card is drawn in the Done column, which the Board draws folded.
  ...closedIssueCases(async (context) => {
    const { key, priority } = await boardCard(context);
    return { control: priority, key };
  }),
  // A pick draws the new priority at once and the select stays enabled while its write is out,
  // so with no lanes the card stays where it is and the select keeps the focus.
  offline: async (context) => {
    const { key, priority } = await boardCard(context);
    return {
      answered: () => expect(context.page.getByTestId("pending-writes")).toBeVisible(),
      cause: () => priority.selectOption("1"),
      control: priority,
      landing: { at: priority, name: `combobox "Priority of ${key}"` },
    };
  },
};

/** The Agents page with one agent marked for broadcast, and the focus on the select-all
 *  header's Clear selection, which goes with the selection. */
async function agentsSelection({ page, scene }: CaseContext): Promise<Arranged> {
  const { root } = await openSurface(page, "Agents page", scene);
  await root
    .getByRole("checkbox", { name: /^Select .* for broadcast$/ })
    .first()
    .check();
  const clear = root.getByRole("button", { exact: true, name: "Clear selection" });
  await clear.focus();
  return {
    answered: () => expect(clear).toHaveCount(0),
    cause: pressEnter(page),
    control: clear,
    landing: {
      at: root.getByRole("checkbox", { exact: true, name: "Select all matching agents" }),
      name: 'checkbox "Select all matching agents"',
    },
  };
}

/** The name of the pill an unread reply from Planner draws on its row. */
const PLANNER_PILL = /^New repl.* from Planner$/;

/** A fresh reply from Planner, and the Agents page with the focus on the pill it draws, which
 *  goes once the reader has read the reply anywhere. */
async function agentsPill({ n, page, scene }: CaseContext): Promise<Locator> {
  await seedAgentThread({
    body: `How far along, case ${n}?`,
    delivery: "aside",
    replies: ["Halfway."],
    session: plannerSession,
  });
  const { root } = await openSurface(page, "Agents page", scene);
  const pill = root.getByRole("button", { name: PLANNER_PILL });
  await pill.focus();
  return pill;
}

const AGENTS_CASES: Scene["cases"] = {
  press: agentsSelection,
  live: async (context) => {
    const pill = await agentsPill(context);
    return {
      answered: () => expect(pill).toHaveCount(0),
      cause: () =>
        putAgentState(plannerSession.session_id, { read_through: new Date().toISOString() }),
      control: pill,
    };
  },
  device: async (context) => {
    const pill = await agentsPill(context);
    return {
      answered: () => expect(pill).toHaveCount(0),
      // The other device opens the conversation by its own pill, which reads the reply there.
      cause: () =>
        withAlice(context.browser, async (other) => {
          await other.goto("/agents");
          const theirs = other
            .getByTestId("main-content")
            .getByRole("button", { name: PLANNER_PILL });
          await theirs.focus();
          await other.keyboard.press("Enter");
          await expect(theirs).toHaveCount(0);
        }),
      control: pill,
    };
  },
  offline: agentsSelection,
};

/** A session group made in the Session groups dialog, with a new name typed in its card and the
 *  focus on its Rename. The fixture's sessions are free for it: every case that makes a group
 *  deletes it. */
async function groupCard({ n, page, scene }: CaseContext): Promise<{
  path: string;
  rename: Locator;
}> {
  await openSurface(page, "Delivery page", scene);
  const name = `Crew ${n}`;
  const { card, editor, id } = await createSessionGroup(page, name);
  await card.getByLabel("Name", { exact: true }).fill(`${name}, renamed`);
  // The dialog holds this one card, so its Rename is found there whatever name the card wears.
  const rename = editor.getByRole("button", { exact: true, name: "Rename" });
  await rename.focus();
  return { path: `/api/v1/delivery/session-groups/${id}`, rename };
}

const DELIVERY_CASES: Scene["cases"] = {
  // A brush window inside the fixture's week, as a drag along the chart's time axis sets it,
  // draws clear brush window in the header's generated line. That line holds no other control,
  // so its press hands the focus to the Window select above it, in sight at the page's top.
  press: async ({ page, scene }) => {
    const now = Date.now();
    const from = encodeURIComponent(new Date(now - 6 * DAY_MS).toISOString());
    const to = encodeURIComponent(new Date(now).toISOString());
    const deliveryQuery = `ws=${from}&we=${to}`;
    const { root } = await openSurface(page, "Delivery page", { ...scene, deliveryQuery });
    const header = root.locator("header", {
      has: page.getByRole("heading", { level: 1, name: "Delivery timeline" }),
    });
    const clear = header.getByRole("button", { exact: true, name: "clear brush window" });
    await expect(clear).toBeVisible();
    await clear.focus();
    return {
      answered: () => expect(clear).toHaveCount(0),
      cause: pressEnter(page),
      control: clear,
      landing: {
        at: header.getByRole("combobox", { exact: true, name: "Window" }),
        name: 'combobox "Window"',
      },
    };
  },
  live: async (context) => {
    const { path, rename } = await groupCard(context);
    return {
      answered: () => expect(rename).toHaveCount(0),
      cause: () => deleteAsReader(path, "the session group"),
      control: rename,
    };
  },
  device: async (context) => {
    const { path, rename } = await groupCard(context);
    return {
      answered: () => expect(rename).toHaveCount(0),
      cause: () => writeFromOtherDevice(context.browser, "DELETE", path),
      control: rename,
    };
  },
  // Rename is held, by `aria-disabled`, while its write is out, so it keeps the focus: offline
  // its write stays out past the deadline, and nothing takes the card away.
  offline: async (context) => {
    const { path, rename } = await groupCard(context);
    return {
      after: () => deleteAsReader(path, "the session group"),
      answered: () => expect(rename).toHaveAttribute("aria-disabled", "true"),
      cause: pressEnter(context.page),
      control: rename,
      landing: { at: rename, name: 'button "Rename"' },
    };
  },
};

/** The search bar, opened by its key, with the seed's word typed and the project and the status
 *  every seeded issue holds picked from its Filter panel: a chip each in the bar's row. */
async function searchChips({ page }: CaseContext): Promise<Arranged> {
  await page.goto("/");
  await expect(page.getByRole("heading", { level: 1, name: "Inbox" })).toBeVisible();
  await page.locator("body").focus();
  await page.keyboard.press("Control+k");
  const bar = page.getByRole("dialog", { name: "Search" });
  await expect(bar).toBeVisible();
  const row = controlRow(bar);
  await control(row, CONTROL.searchBox).fill(SURFACE_SEARCH_TERM);
  const filters = await openFilterPanel(page, bar);
  for (const [menu, option] of [
    ["Project", "CORE"],
    ["Status", "Todo"],
  ] as const) {
    await filters.getByRole("button", { exact: true, name: menu }).click();
    await page
      .getByRole("listbox", { name: `${menu} options` })
      .getByRole("option", { exact: true, name: option })
      .click();
  }
  await closeControlPanel(bar);
  return chipCase(page, row);
}

const SEARCH_CASES: Scene["cases"] = {
  press: searchChips,
  live: {
    none: "the bar's hits are the answer of one search, which no live update reads again, and its chips are the reader's own narrowing, so a live update takes no control of the bar away",
  },
  device: {
    none: "a change on another device reaches this page only by a live update, which takes no control of the bar away",
  },
  offline: searchChips,
};

/** A fresh issue with three empty documents, which a reader may remove, and its Artifacts tab
 *  with the focus on the first listed document's Remove. */
async function documentRows({ n, page, scene }: CaseContext): Promise<{
  issue: string;
  next: string;
  remove: Locator;
  root: Locator;
  target: string;
  /** The id of `target`'s document, as its create answered. */
  targetId: string | undefined;
}> {
  const issue = await createIssue({ project: "CORE", title: `Documents ${n}` });
  // A document made with a name alone holds nothing, which Dispatch removes; one that holds
  // writing it refuses. Each row's test id names its document's slug.
  const ids = new Map<string, string>();
  for (const word of ["alpha", "bravo", "charlie"]) {
    const { artifact } = await createIssueArtifact(issue.key, { name: `Landing ${word} ${n}` });
    ids.set(`artifact-${artifact.slug}`, artifact.id);
  }
  const { root } = await openSurface(page, "Artifacts tab", { ...scene, issue: issue.key });
  const removable = root
    .locator('li[data-testid^="artifact-"]')
    .filter({ has: page.getByRole("button", { exact: true, name: "Remove" }) });
  await expect(removable).toHaveCount(3);
  const rows = await removable.evaluateAll((all) =>
    all.map((row) => row.getAttribute("data-testid") ?? "")
  );
  const target = rows[0] ?? "";
  const remove = root.getByTestId(target).getByRole("button", { exact: true, name: "Remove" });
  await remove.focus();
  return {
    issue: issue.key,
    next: rows[1] ?? "",
    remove,
    root,
    target,
    targetId: ids.get(target),
  };
}

const ARTIFACTS_CASES: Scene["cases"] = {
  // Remove is held through the list's read after its write, and its row then goes. The next
  // document's row moves up into its place, so the focus goes to that row's Remove.
  press: async (context) => {
    const { next, remove, root, target } = await documentRows(context);
    return {
      answered: () => expect(root.getByTestId(target)).toHaveCount(0),
      cause: pressEnter(context.page),
      control: remove,
      landing: {
        at: root.getByTestId(next).getByRole("button", { exact: true, name: "Remove" }),
        name: `button "Remove" in ${next}`,
      },
    };
  },
  live: async (context) => {
    const { remove, root, target, targetId } = await documentRows(context);
    return {
      answered: () => expect(root.getByTestId(target)).toHaveCount(0),
      cause: () => deleteAsReader(`/api/v1/artifacts/${targetId}`, `the document ${target}`),
      control: remove,
    };
  },
  device: async (context) => {
    const { issue, remove, root, target } = await documentRows(context);
    return {
      answered: () => expect(root.getByTestId(target)).toHaveCount(0),
      cause: () =>
        withAlice(context.browser, async (other) => {
          await other.goto(`/issues/${issue}/artifacts`);
          await other
            .getByTestId(target)
            .getByRole("button", { exact: true, name: "Remove" })
            .click();
          await expect(other.getByTestId(target)).toHaveCount(0);
        }),
      control: remove,
    };
  },
  // Remove is held, by `aria-disabled` and its label Removing…, while its write is out, so it
  // keeps the focus: offline its write stays out past the deadline, and its row stays. The
  // landing is that same button under the name it takes once held, which no element carries
  // before the press.
  offline: async (context) => {
    const { remove, root, target } = await documentRows(context);
    const removing = root.getByTestId(target).getByRole("button", {
      exact: true,
      name: "Removing…",
    });
    return {
      answered: () => expect(removing).toHaveAttribute("aria-disabled", "true"),
      cause: pressEnter(context.page),
      control: remove,
      landing: { at: removing, name: 'button "Removing…"' },
    };
  },
};

/**
 * Each page's four cases: the control each cause takes away, and where the focus is to land.
 *
 * Inbox, at `/`:
 * - press: the row's Mark all read, by Enter. Lands on button "Blocked on you…", now at its place.
 * - live: Mark all read, by a Mark all read made through the API. Lands on the body.
 * - device: Mark all read, by Mark all read pressed on the other device. Lands on the body.
 * - offline: Mark all read, by Enter offline, which takes it away at once. Lands on button
 *   "Blocked on you…".
 *
 * Inbox drawer, over the project List:
 * - press: the drawer row's Mark all read, by Enter. Lands on the drawer's "Blocked on you…".
 * - live: the same, by a Mark all read made through the API. Lands on the drawer's own stop,
 *   dialog "Inbox", since the drawer is an open modal dialog.
 * - device: the same, by Mark all read pressed on the other device. Lands on dialog "Inbox".
 * - offline: the same, by Enter offline. Lands on the drawer's "Blocked on you…".
 *
 * project List:
 * - press: the selection bar's Clear selection, with one row marked by Space. Lands on that row's
 *   checkbox "Select <key>".
 * - live: a row's checkbox, its issue closed through the API. Lands on the body.
 * - device: a row's checkbox, its issue closed by the other device. Lands on the body.
 * - offline: the bar's Take out of epic, by Enter offline. It is held while its writes are out
 *   and is never taken away, so it lands on button "Take out of epic" itself.
 *
 * project Board:
 * - press: the first of two chips, Needs you and Unclaimed, by Enter. Lands on the other chip,
 *   now at its place.
 * - live: a card's priority select, its issue closed through the API. Lands on the body.
 * - device: a card's priority select, its issue closed by the other device. Lands on the body.
 * - offline: a card's priority, picked offline. The select stays, so it lands on combobox
 *   "Priority of <key>" itself.
 *
 * Agents page:
 * - press: the select-all header's Clear selection, by Enter. Lands on checkbox "Select all
 *   matching agents".
 * - live: Planner's New reply pill, the reply read through the API. Lands on the body.
 * - device: the same pill, the reply read by its own pill on the other device. Lands on the body.
 * - offline: Clear selection, by Enter offline, which takes it away at once. Lands on checkbox
 *   "Select all matching agents".
 *
 * Delivery page:
 * - press: clear brush window, by Enter. Lands on combobox "Window", on the line above it.
 * - live: a session group's Rename in the Session groups dialog, the group deleted through the
 *   API. Lands on the dialog's own stop, dialog "Session groups".
 * - device: the same, the group deleted by the other device. Lands on dialog "Session groups".
 * - offline: Rename, by Enter offline. It is held while its write is out and is never taken
 *   away, so it lands on button "Rename" itself.
 *
 * search bar:
 * - press: the first of two chips, Project and Status, by Enter. Lands on the other chip, now at
 *   its place.
 * - live: no case. The bar's hits are the answer of one search, which no live update reads
 *   again, and its chips are the reader's own narrowing.
 * - device: no case. A change on another device reaches the bar only by a live update.
 * - offline: the first chip, by Enter offline, which takes it away at once. Lands on the other
 *   chip.
 *
 * Artifacts tab:
 * - press: the first listed document's Remove, by Enter; its row goes once its write and the
 *   list's read after it have landed. Lands on button "Remove" of the row now at its place.
 * - live: the same Remove, the document removed through the API. Lands on the body.
 * - device: the same Remove, the document removed on the other device. Lands on the body.
 * - offline: Remove, by Enter offline. It is held, named Removing…, while its write is out, and
 *   is never taken away, so it lands on button "Removing…" itself.
 */
const SCENES: Readonly<Record<SurfaceName, Scene>> = {
  Inbox: { cases: markAllCases("Inbox"), seed: seedSurfaces },
  "Inbox drawer": { cases: markAllCases("Inbox drawer"), seed: seedSurfaces },
  "project List": { cases: LIST_CASES, seed: seedSurfaces },
  "project Board": { cases: BOARD_CASES, seed: seedSurfaces },
  "Agents page": {
    cases: AGENTS_CASES,
    seed: async () => ({ issue: await seedAgents(), project: "CORE" }),
  },
  "Delivery page": {
    cases: DELIVERY_CASES,
    seed: async () => {
      // Once: the fixture's rows carry no ON CONFLICT.
      await seedSessionGroupsFixture();
      return { issue: "ACME-7", project: "ACME" };
    },
  },
  "search bar": { cases: SEARCH_CASES, seed: seedSurfaces },
  "Artifacts tab": { cases: ARTIFACTS_CASES, seed: seedSurfaces },
};

/** The regions of their own a lost control is answered in: a dialog, a panel, or the selection
 *  bar. */
const REGION_SELECTOR = `[role="dialog"], [data-anchored-panel], [data-control="${CONTROL.selectionBar}"]`;

/** An open modal dialog: a `<dialog>` shown modally, or an element named a dialog that says it
 *  is modal. */
const MODAL_SELECTOR = 'dialog:modal, [role="dialog"][aria-modal="true"]';

/** What holds the focus just before the cause: the focused control, the region it is in - the
 *  nearest dialog, panel or selection bar around it, else the page's main content - and the open
 *  modal dialog that holds it, if one does. The region, that dialog, the window, every box around
 *  the control that scrolls and every box that scrolls around one of `landings`, the elements the
 *  case's landing matches now, are marked in the page with where each stands, so the reading
 *  after the cause finds the same elements. A box around both is marked once.
 *
 *  In each of those boxes, the window included, up to twelve elements in view are marked too,
 *  with where each stands on screen: the rows (`ROW_SELECTOR`) nearest the focused control, else
 *  any elements nearest it. An element in a table's head or under a sticky or fixed element is
 *  left out, since it stays where it is however the box scrolls. `readAfter` reads these marks to
 *  tell a box that moved to keep its view from a box that the focus scrolled. */
function readBefore(
  page: Page,
  landings: readonly ElementHandle[]
): Promise<{ control: string; modal: string | null; region: string }> {
  return page.evaluate(
    ({ landings, modalSelector, regionSelector, rowSelector }) => {
      const nameOf = (element: Element): string => {
        if (element === document.body) return "the body";
        const role = element.getAttribute("role") ?? element.tagName.toLowerCase();
        const labelledBy = element.getAttribute("aria-labelledby");
        const label =
          element.getAttribute("aria-label") ||
          (labelledBy === null ? "" : (document.getElementById(labelledBy)?.textContent ?? "")) ||
          ((element as HTMLInputElement).labels?.[0]?.textContent ?? "") ||
          (element.textContent ?? "");
        return `${role} "${label.replace(/\s+/g, " ").trim().slice(0, 48)}"`;
      };
      for (const attribute of [
        "data-landing-anchor",
        "data-landing-anchor-at",
        "data-landing-box",
        "data-landing-box-id",
        "data-landing-modal",
        "data-landing-region",
      ]) {
        for (const marked of document.querySelectorAll(`[${attribute}]`)) {
          marked.removeAttribute(attribute);
        }
      }
      const active = document.activeElement ?? document.body;
      const main = document.querySelector('[data-testid="main-content"]');
      const region =
        active.closest(regionSelector) ?? (main?.contains(active) ? main : document.body);
      region.setAttribute("data-landing-region", "");
      const modal =
        [...document.querySelectorAll(modalSelector)]
          .filter((open) => open.getClientRects().length > 0 && open.contains(active))
          .at(-1) ?? null;
      modal?.setAttribute("data-landing-modal", "");
      document.documentElement.setAttribute(
        "data-landing-window",
        `${window.scrollX},${window.scrollY}`
      );
      const view = { bottom: window.innerHeight, left: 0, right: window.innerWidth, top: 0 };
      const boxes = [{ clip: view, id: "window", root: document.documentElement as Element }];
      for (const start of [active, ...landings]) {
        for (
          let box = start.parentElement;
          box !== null && box !== document.body;
          box = box.parentElement
        ) {
          if (box.hasAttribute("data-landing-box")) continue;
          const style = getComputedStyle(box);
          const clips = style.overflowX !== "visible" || style.overflowY !== "visible";
          if (clips && (box.scrollHeight > box.clientHeight || box.scrollWidth > box.clientWidth)) {
            const id = `box-${boxes.length}`;
            box.setAttribute("data-landing-box", `${box.scrollLeft},${box.scrollTop}`);
            box.setAttribute("data-landing-box-id", id);
            const own = box.getBoundingClientRect();
            const clip = {
              bottom: Math.min(view.bottom, own.bottom),
              left: Math.max(view.left, own.left),
              right: Math.min(view.right, own.right),
              top: Math.max(view.top, own.top),
            };
            boxes.push({ clip, id, root: box });
          }
        }
      }
      const activeTop = active.getBoundingClientRect().top;
      // Whether each element read so far stays where it is however a box scrolls - a table's
      // head, or a sticky or fixed element - read once for every box.
      const pinning = new Map<Element, boolean>();
      for (const { clip, id, root } of boxes) {
        const pinned = (element: Element): boolean => {
          for (
            let node: Element | null = element;
            node !== null && node !== root;
            node = node.parentElement
          ) {
            let pins = pinning.get(node);
            if (pins === undefined) {
              const { position } = getComputedStyle(node);
              pins = node.tagName === "THEAD" || position === "sticky" || position === "fixed";
              pinning.set(node, pins);
            }
            if (pins) return true;
          }
          return false;
        };
        const shown = (selector: string) =>
          [...root.querySelectorAll(selector)].filter((element) => {
            const own = element.getBoundingClientRect();
            const inView =
              own.height > 0 &&
              own.top >= clip.top - 0.5 &&
              own.bottom <= clip.bottom + 0.5 &&
              own.right > clip.left &&
              own.left < clip.right;
            return inView && !pinned(element);
          });
        const rows = shown(rowSelector);
        const anchors = (rows.length > 0 ? rows : shown("*"))
          .map((element) => ({
            distance: Math.abs(element.getBoundingClientRect().top - activeTop),
            element,
          }))
          .sort((one, other) => one.distance - other.distance)
          .slice(0, 12);
        for (const { element: anchor } of anchors) {
          const at = anchor.getBoundingClientRect();
          const already = anchor.getAttribute("data-landing-anchor");
          anchor.setAttribute("data-landing-anchor", already === null ? id : `${already} ${id}`);
          anchor.setAttribute("data-landing-anchor-at", `${at.left},${at.top}`);
        }
      }
      return {
        control: nameOf(active),
        modal: modal === null ? null : nameOf(modal),
        region: region === main ? "the main content" : nameOf(region),
      };
    },
    {
      landings,
      modalSelector: MODAL_SELECTOR,
      regionSelector: REGION_SELECTOR,
      rowSelector: ROW_SELECTOR,
    }
  );
}

/** Why a box that moved is no scroll the focus made: the page clamped it, it kept its view, it
 *  showed a landing control that was partly out of sight, or it moved by less than a pixel. */
type MoveReason = "clamped" | "view kept" | "nearest edge" | "under 1 px";

/** How far one box moved across the cause, and why that move is no scroll the focus made, or
 *  null where nothing says so. */
interface Move {
  readonly box: string;
  readonly reason: MoveReason | null;
  readonly x: number;
  readonly y: number;
}

/** Where the focus rests after the cause, read against what `readBefore` marked: the focused
 *  element, named; whether it is the body, the open modal dialog that held the lost control, or
 *  an element behind the topmost open modal dialog; whether it is inside the lost control's
 *  region, or null where that region has left the page; and the focused element's size.
 *
 *  It also reads how far the window and each marked box still in the page moved. A move is named
 *  `clamped` where the box now stands at the farthest it can scroll and stood farther before:
 *  the page grew shorter than the box's scroll. It is named `view kept` where an element marked
 *  in view in that box before is still in the page and stands where it stood on screen, within
 *  1 px on each axis that moved: the box moved by what the page's layout moved, as a list's
 *  anchor makes up for a removed row. A focus scroll moves every element of the box that the
 *  layout did not move, so it keeps no such element where it stood. */
function readAfter(page: Page): Promise<{
  behindModal: boolean;
  got: string;
  height: number;
  inRegion: boolean | null;
  moved: Move[];
  onBody: boolean;
  onModal: boolean;
  width: number;
}> {
  return page.evaluate((modalSelector) => {
    const nameOf = (element: Element): string => {
      if (element === document.body) return "the body";
      const role = element.getAttribute("role") ?? element.tagName.toLowerCase();
      const labelledBy = element.getAttribute("aria-labelledby");
      const label =
        element.getAttribute("aria-label") ||
        (labelledBy === null ? "" : (document.getElementById(labelledBy)?.textContent ?? "")) ||
        ((element as HTMLInputElement).labels?.[0]?.textContent ?? "") ||
        (element.textContent ?? "");
      return `${role} "${label.replace(/\s+/g, " ").trim().slice(0, 48)}"`;
    };
    const pair = (value: string | null): [number, number] => {
      const [x = 0, y = 0] = (value ?? "0,0").split(",").map(Number);
      return [x, y];
    };
    const judge = (
      box: string,
      id: string,
      before: [number, number],
      now: [number, number],
      farthest: [number, number]
    ) => {
      const shift: [number, number] = [now[0] - before[0], now[1] - before[1]];
      const axes = ([0, 1] as const).filter((axis) => Math.abs(shift[axis]) > 0.5);
      const clamped = axes.every(
        (axis) => before[axis] > farthest[axis] + 0.5 && Math.abs(now[axis] - farthest[axis]) <= 1
      );
      const kept = [...document.querySelectorAll(`[data-landing-anchor~="${id}"]`)].some(
        (anchor) => {
          const stood = pair(anchor.getAttribute("data-landing-anchor-at"));
          const own = anchor.getBoundingClientRect();
          const stands: [number, number] = [own.left, own.top];
          return own.height > 0 && axes.every((axis) => Math.abs(stands[axis] - stood[axis]) <= 1);
        }
      );
      const reason: MoveReason | null = clamped ? "clamped" : kept ? "view kept" : null;
      return { axes, move: { box, reason, x: shift[0], y: shift[1] } };
    };
    const active = document.activeElement ?? document.body;
    const onBody = active === document.body;
    const topmost = [...document.querySelectorAll(modalSelector)]
      .filter((open) => open.getClientRects().length > 0)
      .at(-1);
    const region = document.querySelector("[data-landing-region]");
    const scroller = document.scrollingElement ?? document.documentElement;
    const judged = [
      judge(
        "window",
        "window",
        pair(document.documentElement.getAttribute("data-landing-window")),
        [window.scrollX, window.scrollY],
        [scroller.scrollWidth - scroller.clientWidth, scroller.scrollHeight - scroller.clientHeight]
      ),
    ];
    for (const box of document.querySelectorAll<HTMLElement>("[data-landing-box]")) {
      const named =
        box.getAttribute("data-testid") ?? box.getAttribute("aria-label") ?? box.tagName;
      judged.push(
        judge(
          named.toLowerCase(),
          box.getAttribute("data-landing-box-id") ?? "",
          pair(box.getAttribute("data-landing-box")),
          [box.scrollLeft, box.scrollTop],
          [box.scrollWidth - box.clientWidth, box.scrollHeight - box.clientHeight]
        )
      );
    }
    const own = active.getBoundingClientRect();
    return {
      behindModal: topmost !== undefined && !onBody && !topmost.contains(active),
      got: nameOf(active),
      height: own.height,
      inRegion: region === null ? null : region.contains(active),
      moved: judged.filter(({ axes }) => axes.length > 0).map(({ move }) => move),
      onBody,
      onModal: active === document.querySelector("[data-landing-modal]"),
      width: own.width,
    };
  }, MODAL_SELECTOR);
}

/** Whether `control` is partly out of the reader's sight: some of its box is inside the window
 *  and every box around it that clips, and some is outside. Read over every element `control`
 *  matches now, waiting for none, so a landing the page has not drawn yet reads as false. */
function partlyOutOfSight(control: Locator): Promise<boolean> {
  return control.evaluateAll((elements) =>
    elements.some((element) => {
      const own = element.getBoundingClientRect();
      let top = 0;
      let left = 0;
      let bottom = window.innerHeight;
      let right = window.innerWidth;
      for (
        let box = element.parentElement;
        box !== null && box !== document.body;
        box = box.parentElement
      ) {
        const style = getComputedStyle(box);
        if (style.overflowX === "visible" && style.overflowY === "visible") continue;
        const clip = box.getBoundingClientRect();
        top = Math.max(top, clip.top);
        left = Math.max(left, clip.left);
        bottom = Math.min(bottom, clip.bottom);
        right = Math.min(right, clip.right);
      }
      const meets = own.bottom > top && own.top < bottom && own.right > left && own.left < right;
      const whole =
        own.top >= top - 0.5 &&
        own.bottom <= bottom + 0.5 &&
        own.left >= left - 0.5 &&
        own.right <= right + 0.5;
      return meets && !whole;
    })
  );
}

/** The moves a reading names, as a line prints them, each with its reason where it has one. */
function scrolledText(moved: readonly Move[]): string {
  const signed = (value: number) => `${value > 0 ? "+" : ""}${Math.round(value)}`;
  return moved.length === 0
    ? "none"
    : moved
        .map(
          ({ box, reason, x, y }) =>
            `${box} ${signed(x)},${signed(y)}px${reason === null ? "" : ` (${reason})`}`
        )
        .join("; ");
}

/** What names one case: its page, its cause, its window and its engine. */
interface CaseName {
  readonly browserName: string;
  readonly cause: Cause;
  readonly size: { readonly height: number; readonly width: number };
  readonly surface: SurfaceName;
}

/** One case of a row: what it runs on, and how it is named. */
type Run = CaseContext & CaseName;

/** A case as a failure names it: its page, its cause and its window. */
function where({ cause, size, surface }: CaseName): string {
  return `${surface}, ${cause}, ${size.width}x${size.height}`;
}

/** Prints a case's one line, `LANDING <page> <cause> <width> <engine> expected=<...> got=<...>
 *  scrolled=<...>`. */
function print(
  { browserName, cause, size, surface }: CaseName,
  expected: string,
  got: string,
  scrolled: string
): void {
  console.log(
    `LANDING "${surface}" ${cause} ${size.width} ${browserName} expected=${expected} got=${got} scrolled=${scrolled}`
  );
}

/**
 * Runs one case and adds what it found wrong to `wrong`. It arranges the case, which seeds, opens
 * the page and puts the focus on the control; reads the page; makes the cause - offline, with the
 * browser offline from just before the press - and waits for the page to answer it, and offline
 * runs the page's own clock on until the request deadline has passed since the press; then reads
 * where the focus rests, and prints the case's line. Whatever stops the case, it leaves the seed
 * as the next case needs it (`Arranged.after`).
 */
async function runCase(run: Run, arrange: Arrange, wrong: string[]): Promise<void> {
  const { cause, page } = run;
  const caseName = where(run);
  const passes = (check: Promise<unknown>): Promise<boolean> =>
    check.then(
      () => true,
      () => false
    );
  const offline = cause === "offline";
  // The offline case installs the page's own clock before it opens its page, so every timer the
  // page it opens sets follows that clock: the request and write deadlines, the read bound, the
  // press's bounds and the rescue's frames and tasks. The clock keeps the wall clock's pace until
  // the case runs it on below, and it stays installed for the rest of the row.
  if (offline) await page.clock.install();
  const arranged = await arrange(run);
  try {
    const holds = await passes(expect(arranged.control).toBeFocused());
    if (!holds) {
      wrong.push(`${caseName}: the control the cause takes away never held the focus`);
      print(run, "(not run)", "(the control never held the focus)", "(not run)");
      return;
    }
    const { landing } = arranged;
    // The landing's elements as the page draws them now, waiting for none: the read below marks
    // the boxes around them too, so a hand-off that scrolls one the lost control is not in is read.
    const landingElements = landing === undefined ? [] : await landing.at.elementHandles();
    // One read just before the cause is the record: the focus was waited for above, and nothing
    // moves it or scrolls the page until the cause.
    const before = await readBefore(page, landingElements);
    // Read before the cause as well: a landing control partly out of sight is the one case where
    // the landing may scroll, by no more than that control's own size. A landing not drawn yet -
    // a held control under the name it takes once held - is not waited for, and allows no scroll.
    const partly = landing === undefined ? false : await partlyOutOfSight(landing.at);
    const browserContext = page.context();
    try {
      if (offline) {
        await browserContext.setOffline(true);
        await page.waitForFunction(() => !navigator.onLine);
      }
      // The press's time on the page's own clock, which the offline case installed.
      const pressed = offline ? await page.evaluate(() => Date.now()) : 0;
      await arranged.cause();
      const answered = await passes(arranged.answered());
      if (!answered) wrong.push(`${caseName}: the page never showed what the cause does`);
      if (offline) {
        // The offline case's claim is a span of time: the focus is read once the request deadline
        // has passed since the press, whatever the page does at that deadline. The page's clock
        // is run on to that mark rather than waited out, and every timer due on the way fires in
        // the order it falls due.
        const spent = (await page.evaluate(() => Date.now())) - pressed;
        const left = REQUEST_DEADLINE_MS + PAST_THE_DEADLINE_MS - spent;
        if (left > 0) await page.clock.runFor(left);
      }
      // The page's rescue runs a frame and a task after a loss, and a component's hand-off in the
      // commit that took the control away, so both are done after these frames.
      await nextFrames(page);
      await nextFrames(page);
      const landed = landing === undefined || (await passes(expect(landing.at).toBeFocused()));
      // One read once the focus has rested: after a loss the reader caused, the wait above ran
      // until the focus was on the landing or the expect timeout ended; after one they did not,
      // every hand-off ran in the frames above. Nothing else moves the focus or scrolls the page.
      const after = await readAfter(page);
      const expected = landing?.name ?? before.modal ?? "the body";
      // A move the page made - clamped, or keeping its view - is no scroll of the focus. Of the
      // rest, only the landing of a loss the reader caused may scroll, and only to show a control
      // that was partly out of sight: by no more than its own height, or its width sideways. A
      // move under a pixel is the rounding of a box's place.
      const allowX = partly ? after.width + 1 : 1;
      const allowY = partly ? after.height + 1 : 1;
      const moves = after.moved.map((move) => {
        const allowed = Math.abs(move.x) <= allowX && Math.abs(move.y) <= allowY;
        const own: MoveReason | null = allowed ? (partly ? "nearest edge" : "under 1 px") : null;
        return { ...move, reason: move.reason ?? own };
      });
      print(run, expected, after.got, scrolledText(moves));
      const right =
        landing !== undefined ? landed : before.modal === null ? after.onBody : after.onModal;
      if (!right) {
        wrong.push(
          `${caseName}: after ${before.control} went, the focus is on ${after.got}, not ${expected}`
        );
      }
      if (landing !== undefined && after.onBody) {
        wrong.push(`${caseName}: a loss the reader caused left the focus on the body`);
      }
      if (landing !== undefined && after.inRegion === false) {
        wrong.push(`${caseName}: the focus is on ${after.got}, outside ${before.region}`);
      }
      if (after.behindModal) {
        wrong.push(`${caseName}: the focus is on ${after.got}, behind an open modal dialog`);
      }
      for (const move of moves) {
        if (move.reason === null) {
          wrong.push(`${caseName}: ${move.box} scrolled by ${move.x},${move.y} px`);
        }
      }
    } finally {
      if (offline) {
        await browserContext.setOffline(false);
        // The write the press left out lands once the browser is online again, so the row ends
        // with nothing pending, and the case's clean-up (`Arranged.after`) runs after that write.
        await expect(page.getByTestId("pending-writes")).toHaveCount(0, { timeout: 60_000 });
      }
    }
  } finally {
    await arranged.after?.();
  }
}

for (const surface of [...STUBBABLE_SURFACES, ...REAL_STACK_SURFACES]) {
  for (const size of SIZES) {
    // SLOW(>=2s): one page seeded and opened up to four times, its device case opening a second
    // signed-in context, and its offline case waiting for the write it left out to land once the
    // browser is online again.
    // `@firefox` and `@webkit`: a focused control taken away fires no `focusout` in these engines,
    // so the page's focus rescue sees that loss only by watching the page (`playwright.config.ts`).
    // The body is the landing of a loss the reader did not cause, the live and the device cases
    // outside a modal dialog, as on the page before it had a focus rescue: it is no defect.
    test(`a control holding the focus on the ${surface}, taken away by a press, a live update, another device or an offline press past the request deadline, hands the focus where its cause says, at ${size.width} @firefox @webkit`, async ({
      browser,
      browserName,
      page,
    }) => {
      // Up to four cases, each opening the page once and the device case a second context too,
      // and the offline case then waiting for its write to land, at most 60 s. The offline case's
      // 32 s past its press are run on the page's own clock at once, not waited out.
      test.setTimeout(150_000);
      const scene = SCENES[surface];
      const seeded = await scene.seed();
      const wrong: string[] = [];
      await page.setViewportSize(size);
      let n = 0;
      for (const cause of CAUSES) {
        n += 1;
        const named: CaseName = { browserName, cause, size, surface };
        const arrange = scene.cases[cause];
        if ("none" in arrange) {
          print(named, `(no case: ${arrange.none})`, "(not run)", "(not run)");
          continue;
        }
        // A case that cannot be arranged or caused is named, and the row goes on to the next
        // case, which opens its page afresh: each case's line is what the run is read by.
        await runCase({ ...named, browser, n, page, scene: seeded }, arrange, wrong).catch(
          (error: unknown) => {
            const first = String(error instanceof Error ? error.message : error).split("\n")[0];
            wrong.push(`${where(named)}: could not run: ${first}`);
            print(named, "(not read)", `(could not run: ${first})`, "(not read)");
          }
        );
      }
      expect(wrong, wrong.join("; ")).toEqual([]);
    });
  }
}
