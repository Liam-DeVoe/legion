/** The Agents page's, the Delivery page's, the search bar's and the Artifacts tab's cases of
 *  `focus-landing.e2e.ts`: the rows each runs, and how each case stages its shape. Where a case
 *  follows a row of `table-controls.e2e.ts`, it names that row by its title. */

import type { Locator, Route } from "@playwright/test";

import { CONTROL } from "../web/src/components/control-names";
import {
  type FakeSession,
  plannerSession,
  reviewerSession,
  seedAgents,
  setLiveSessions,
} from "./agents";
import {
  createIssue,
  createIssueArtifact,
  createMessage,
  createProject,
  deleteAsReader,
  putAgentState,
  setNewIssueSettings,
} from "./api";
import { seedDeliveryFixture, seedSessionGroupsFixture } from "./delivery-seed";
import { seedFakeGithub } from "./fake-github-helpers";
import { control, controlRow, listTrigger, openFilterPanel } from "./filters";
import {
  type Arrange,
  type Arranged,
  type CaseContext,
  chipCase,
  pressEnter,
  type Scene,
  writeFromOtherDevice,
} from "./focus-landing-cases";
import { nextFrames } from "./frames";
import { seedAgentThread } from "./inbox-threads";
import { sessionIds } from "./session-ids";
import {
  closeControlPanel,
  openIssuesTab,
  openSurface,
  SURFACE_SEARCH_TERM,
  seedSurfaces,
} from "./table-controls";
import { expect, type Page } from "./test";
import { withAlice } from "./users";

/** A day, for the brush window the Delivery page's press cases open on. */
const DAY_MS = 86_400_000;

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
    caused: true,
    control: clear,
    named: {
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

/** A live agent the reader pins, with no Dispatch activity, so the page lists it after the seed's
 *  two and, unpinned, by its title. */
function pinnable(sessionId: string, title: string): FakeSession {
  return {
    capabilities: ["aside", "btw", "steer"],
    dir: `/workspaces/${title.toLowerCase().replace(/\s+/g, "-")}`,
    last_seen: Date.now() - 5_000,
    machine_id: "pin-host",
    roles: [],
    session_id: sessionId,
    title,
  };
}

const ALPHA = pinnable("01a0e200-4848-7473-acc5-0000000000a5", "Alpha");
const BETA = pinnable("01a0e200-4848-7473-acc5-0000000000b5", "Beta");
const GAMMA = pinnable("01a0e200-4848-7473-acc5-0000000000c5", "Gamma");

/** Twenty live agents whose titles sort before Alpha's, so Alpha, once unpinned, is listed after
 *  them all: below the window at either width. */
const AARDVARKS = Array.from({ length: 20 }, (_, index) =>
  pinnable(
    `01a0e200-4848-7473-acc5-${String(index).padStart(12, "0")}`,
    `Aardvark ${String(index + 1).padStart(2, "0")}`
  )
);

/**
 * The tester's row "Unpin on a pinned agent keeps the keyboard focus on a control in sight, on
 * the Agents page at 1440 and 390": Alpha, Beta and Gamma live and pinned in that order, Alpha
 * first, and the focus put on Unpin Alpha, which scrolls it into view as each engine does. The
 * press moves Alpha's row out of the pinned list's place to its place among the unpinned rows.
 * With `farAway`, twenty more agents are listed before Alpha's unpinned place, so Pin Alpha lands
 * out of sight, or out of the list's window, while Unpin Alpha stood in sight.
 */
async function unpinAlpha(
  { page, scene }: CaseContext,
  farAway: boolean
): Promise<{ beta: Locator; unpin: Locator }> {
  await setLiveSessions([
    plannerSession,
    reviewerSession,
    ALPHA,
    BETA,
    GAMMA,
    ...(farAway ? AARDVARKS : []),
  ]);
  for (const agent of [ALPHA, BETA, GAMMA])
    await putAgentState(agent.session_id, { pinned: false });
  for (const agent of [ALPHA, BETA, GAMMA]) await putAgentState(agent.session_id, { pinned: true });
  await openSurface(page, "Agents page", scene);
  const unpin = page
    .locator(`[data-agent-row="${ALPHA.session_id}"]`)
    .getByRole("button", { exact: true, name: "Unpin Alpha" });
  await expect(unpin).toBeVisible();
  const beta = page
    .locator(`[data-agent-row="${BETA.session_id}"]`)
    .getByRole("button", { exact: true, name: "Unpin Beta" });
  await expect(beta).toBeVisible();
  await unpin.focus();
  await nextFrames(page);
  return { beta, unpin };
}

/** Unpin Alpha, pressed. Its write has landed once no Unpin Alpha is drawn: Alpha's row offers
 *  Pin Alpha where the page draws it, which a long list may not. */
const unpinCase =
  (farAway: boolean): Arrange =>
  async (context) => {
    const { beta, unpin } = await unpinAlpha(context, farAway);
    return {
      answered: () => expect(unpin).toHaveCount(0),
      cause: pressEnter(context.page),
      caused: true,
      control: unpin,
      ...(farAway
        ? { named: { at: beta, name: 'button "Unpin Beta"' }, staged: { rule: "moved-up" } }
        : {}),
    };
  };

/** Unpin Alpha with the pin's write held, then let through. */
const heldUnpin: Arrange = async (context) => {
  const { page } = context;
  const { unpin } = await unpinAlpha(context, false);
  const held: Route[] = [];
  const path = `**/api/v1/me/agents/${ALPHA.session_id}/state`;
  const hold = async (route: Route) => {
    if (route.request().method() !== "PUT") return route.fallback();
    held.push(route);
  };
  await page.route(path, hold);
  const pin = page
    .locator(`[data-agent-row="${ALPHA.session_id}"]`)
    .getByRole("button", { exact: true, name: "Pin Alpha" });
  return {
    after: () => page.unroute(path, hold),
    answered: () => expect.poll(() => held.length, "Unpin sent no write").toBe(1),
    cause: pressEnter(page),
    caused: true,
    control: unpin,
    held: {
      landed: () => expect(pin).toHaveCount(1),
      release: async () => {
        for (const route of held.splice(0)) await route.continue();
      },
    },
  };
};

/** Unpin Alpha pressed offline: the pin's write is held past the request deadline. */
const offlineUnpin: Arrange = async (context) => {
  const { unpin } = await unpinAlpha(context, false);
  return {
    answered: () => expect(context.page.getByTestId("pending-writes")).toBeVisible(),
    cause: pressEnter(context.page),
    caused: true,
    control: unpin,
  };
};

/** One agent marked for broadcast and the list then narrowed by the search box to no agent, so the
 *  header's select-all checkbox is disabled while Clear selection still stands after it: Clear
 *  selection's press takes it away, and the walk back passes over that disabled checkbox. */
const walkPastDisabled: Arrange = async ({ page, scene }) => {
  const { root } = await openSurface(page, "Agents page", scene);
  await root
    .getByRole("checkbox", { name: /^Select .* for broadcast$/ })
    .first()
    .check();
  await control(controlRow(root), CONTROL.searchBox).fill("matches no agent at all");
  await expect(
    root.getByRole("checkbox", { exact: true, name: "Select all matching agents" })
  ).toBeDisabled();
  const clear = root.getByRole("button", { exact: true, name: "Clear selection" });
  await clear.focus();
  return {
    answered: () => expect(clear).toHaveCount(0),
    cause: pressEnter(page),
    caused: true,
    control: clear,
    staged: { passes: ["untabbed"], rule: "walk-back" },
  };
};

/** The Agents page's rows. */
export const AGENTS_SCENE: Scene = {
  seed: async () => ({ issue: await seedAgents(), project: "CORE" }),
  rows: [
    {
      what: "a press, a live update or another device",
      cases: {
        press: { arrange: agentsSelection },
        "walk-untabbed": { arrange: walkPastDisabled },
        live: {
          arrange: async (context) => {
            const pill = await agentsPill(context);
            return {
              answered: () => expect(pill).toHaveCount(0),
              cause: () =>
                putAgentState(plannerSession.session_id, {
                  read_through: new Date().toISOString(),
                }),
              caused: false,
              control: pill,
            };
          },
        },
        device: {
          arrange: async (context) => {
            const pill = await agentsPill(context);
            return {
              answered: () => expect(pill).toHaveCount(0),
              // The other device opens the conversation by its own pill, which reads the reply.
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
              caused: false,
              control: pill,
            };
          },
        },
      },
    },
    {
      what: "Unpin",
      cases: {
        unpin: { arrange: unpinCase(false) },
        "unpin-landing-unseen": { arrange: unpinCase(true) },
        "held-unpin": { arrange: heldUnpin },
      },
    },
    {
      what: "an offline press past the request deadline",
      cases: { "offline-unpin": { arrange: offlineUnpin, offline: true } },
    },
  ],
};

/** The Delivery page's header, whose generated line draws clear brush window. */
function deliveryHeader(page: Page): Locator {
  return page.locator("header", {
    has: page.getByRole("heading", { level: 1, name: "Delivery timeline" }),
  });
}

/** The Delivery page open on a brush window inside the fixture's week, as a drag along the
 *  chart's time axis sets it, which draws clear brush window in the header's generated line. */
async function brushed({ page, scene }: CaseContext): Promise<Locator> {
  const now = Date.now();
  const from = encodeURIComponent(new Date(now - 6 * DAY_MS).toISOString());
  const to = encodeURIComponent(new Date(now).toISOString());
  await openSurface(page, "Delivery page", { ...scene, deliveryQuery: `ws=${from}&we=${to}` });
  const clear = deliveryHeader(page).getByRole("button", {
    exact: true,
    name: "clear brush window",
  });
  await expect(clear).toBeVisible();
  return clear;
}

/** The Session groups dialog the Delivery page's filter panel opens. */
async function groupsDialog({ page, scene }: CaseContext): Promise<Locator> {
  await openSurface(page, "Delivery page", scene);
  await expect(page.getByText("3 PRs in current filter/window")).toBeVisible();
  await openFilterPanel(page);
  await page.getByRole("button", { name: "Edit groups" }).click();
  return page.getByRole("dialog", { name: "Session groups" });
}

/** A group named `name` of the fixture's session `session`, made in the open dialog, once its
 *  create has ended: the card, and its address for a delete through the API. */
async function makeGroup(
  page: Page,
  editor: Locator,
  session: string,
  name: string
): Promise<{ card: Locator; path: string }> {
  await editor.getByRole("checkbox", { exact: true, name: session }).check();
  await editor.getByLabel("New group name").fill(name);
  const created = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      response.url().endsWith("/api/v1/delivery/session-groups")
  );
  await editor.getByRole("button", { name: "Create group" }).click();
  const { id } = (await (await created).json()) as { id: string };
  const card = editor.getByRole("group", { name });
  // Every button of a card is held while a write of the editor is out, and Delete group by
  // nothing else, so its coming back is the create's end.
  await expect(card.getByRole("button", { name: "Delete group" })).toBeEnabled();
  return { card, path: `/api/v1/delivery/session-groups/${id}` };
}

/** A group made in the dialog, with a new name typed in its card and the focus on its Rename. */
async function groupCard(context: CaseContext): Promise<{
  card: Locator;
  path: string;
  rename: Locator;
}> {
  const editor = await groupsDialog(context);
  const name = `Crew ${context.n}`;
  const { card, path } = await makeGroup(context.page, editor, "Planner, first run", name);
  await card.getByLabel("Name", { exact: true }).fill(`${name}, renamed`);
  // The dialog holds this one card, so its Rename is found there whatever name the card wears.
  const rename = editor.getByRole("button", { exact: true, name: "Rename" });
  await rename.focus();
  return { card, path, rename };
}

/** A group's card deleted while its Rename holds the focus, a loss the reader did not cause: by
 *  the API, or on the other device; with `renamed`, once a Rename by Enter has landed on the card
 *  and its hold has ended, as the tester's row "a group card deleted on another device while it
 *  holds the focus leaves the focus on the Session groups dialog" stages its second pass. */
function groupDeleted(how: "api" | "device", renamed = false): Arrange {
  return async (context) => {
    const { page } = context;
    const { path, rename } = await groupCard(context);
    if (renamed) {
      await page.keyboard.press("Enter");
      const editor = page.getByRole("dialog", { name: "Session groups" });
      await expect(editor.getByRole("group", { name: `Crew ${context.n}, renamed` })).toHaveCount(
        1
      );
      await expect(editor.getByRole("button", { name: "Delete group" })).toBeEnabled();
      await expect(rename).toBeFocused();
    }
    return {
      answered: () => expect(rename).toHaveCount(0),
      cause: () =>
        how === "api"
          ? deleteAsReader(path, "the session group")
          : writeFromOtherDevice(context.browser, "DELETE", path),
      caused: false,
      control: rename,
    };
  };
}

/** Two groups made in the dialog, and the second one's Delete group pressed: the dialog's last
 *  card goes, and no card moves up into its place. */
const lastCardDeleted: Arrange = async (context) => {
  const { n, page } = context;
  const editor = await groupsDialog(context);
  const first = await makeGroup(page, editor, "Planner, first run", `First crew ${n}`);
  const last = await makeGroup(page, editor, "Planner, second run", `Last crew ${n}`);
  const remove = last.card.getByRole("button", { name: "Delete group" });
  await remove.focus();
  return {
    after: () => deleteAsReader(first.path, "the first session group"),
    answered: () => expect(last.card).toHaveCount(0),
    cause: pressEnter(page),
    caused: true,
    control: remove,
    staged: { rule: "walk-back" },
  };
};

/** One group made in the dialog, and its Delete group pressed: the groups' section goes with it,
 *  and the walk back passes over the new group's name box to Create group. */
const onlyCardDeleted: Arrange = async (context) => {
  const { n, page } = context;
  const editor = await groupsDialog(context);
  const { card } = await makeGroup(page, editor, "Planner, first run", `Only crew ${n}`);
  const remove = card.getByRole("button", { name: "Delete group" });
  await remove.focus();
  return {
    answered: () => expect(card).toHaveCount(0),
    cause: pressEnter(page),
    caused: true,
    control: remove,
    named: {
      at: editor.getByRole("button", { exact: true, name: "Create group" }),
      name: 'button "Create group"',
    },
    staged: { rule: "walk-back" },
  };
};

/** Rename pressed with its write held, then let through: the card stays, renamed. */
const heldRename: Arrange = async (context) => {
  const { page } = context;
  const { path, rename } = await groupCard(context);
  const held: Route[] = [];
  const matches = (url: URL) => url.pathname === path;
  const hold = async (route: Route) => {
    if (route.request().method() !== "PATCH") return route.fallback();
    held.push(route);
  };
  await page.route(matches, hold);
  return {
    after: async () => {
      await page.unroute(matches, hold);
      await deleteAsReader(path, "the session group");
    },
    answered: () => expect.poll(() => held.length, "Rename sent no write").toBe(1),
    cause: pressEnter(page),
    caused: true,
    control: rename,
    held: {
      landed: () =>
        expect(
          page
            .getByRole("dialog", { name: "Session groups" })
            .getByRole("group", { name: `Crew ${context.n}, renamed` })
        ).toHaveCount(1),
      release: async () => {
        for (const route of held.splice(0)) await route.continue();
      },
    },
    staged: { rule: "kept" },
  };
};

/**
 * The tester's row "clear brush window pressed with the Window control scrolled out of sight
 * scrolls the Delivery page nothing and hands the focus to no control out of sight": the reader's
 * own scroll leaves the Window control's foot 2 px above the window, clear brush window on the line
 * under it in sight, and the focus is put on that button with no scroll. Its press takes it away.
 */
const brushWindowUnseen: Arrange = async (context) => {
  const { page } = context;
  const clear = await brushed(context);
  const windowControl = listTrigger(deliveryHeader(page), "Window");
  // The page is drawn whole below the header, so it scrolls as far as the case needs: its
  // height holds across two frames.
  await expect
    .poll(async () => {
      const height = await page.evaluate(() => document.documentElement.scrollHeight);
      await nextFrames(page);
      return height === (await page.evaluate(() => document.documentElement.scrollHeight));
    })
    .toBe(true);
  await windowControl.evaluate((node) => {
    window.scrollTo(0, window.scrollY + node.getBoundingClientRect().bottom + 2);
  });
  await clear.evaluate((node) => {
    if (node instanceof HTMLElement) node.focus({ preventScroll: true });
  });
  return {
    answered: () => expect(clear).toHaveCount(0),
    cause: pressEnter(page),
    caused: true,
    control: clear,
    staged: { passes: ["unseen"] },
  };
};

/**
 * The tester's row "Save delivery settings keeps the keyboard focus through a save whose re-read
 * the browser cannot send": the reader presses Enter on Save delivery settings, the save lands,
 * and the browser is offline before the page reads its answer, so the re-read that draws the
 * stored record cannot be sent. The hold ends at the request deadline, on the wall clock.
 */
const settingsSaveOffline: Arrange = async ({ page }) => {
  // The save proves the GitHub App can read the deploy repository; the fake answers it.
  await seedFakeGithub({ "acme/widgets": { contents: "read", installation_id: 301 } });
  await page.goto("/settings");
  const save = page.getByRole("button", { name: "Save delivery settings" });
  await expect(save).toBeEnabled();
  await page.getByLabel("Deploy workflow").fill(".github/workflows/deploy-two.yml");
  const context = page.context();
  const pattern = "**/api/v1/settings/delivery";
  const answerOffline = async (route: Route) => {
    if (route.request().method() !== "PUT") return route.fallback();
    const response = await route.fetch();
    await context.setOffline(true);
    await page.waitForFunction(() => !navigator.onLine);
    await route.fulfill({ response });
  };
  await page.route(pattern, answerOffline);
  await save.focus();
  return {
    after: async () => {
      await context.setOffline(false);
      await page.unroute(pattern, answerOffline);
    },
    answered: async () => {
      await expect(save).toHaveAttribute("aria-disabled", "true");
      // The hold's own contract is the request deadline, 30 s from the save's answer; 45 s holds
      // it with room to spare.
      await expect(save).not.toHaveAttribute("aria-disabled", "true", { timeout: 45_000 });
    },
    cause: pressEnter(page),
    caused: true,
    control: save,
    named: { at: save, name: 'button "Save delivery settings"' },
    staged: { rule: "kept" },
  };
};

/** The Delivery page's rows. */
export const DELIVERY_SCENE: Scene = {
  seed: async () => {
    // Once: the fixture's rows carry no ON CONFLICT.
    await seedSessionGroupsFixture();
    return { issue: "ACME-7", project: "ACME" };
  },
  rows: [
    {
      what: "a press, a live update or another device",
      cases: {
        // The header's generated line holds no other control, so the press hands the focus to
        // the Window list's trigger above it, in sight at the page's top.
        press: {
          arrange: async (context) => {
            const clear = await brushed(context);
            await clear.focus();
            return {
              answered: () => expect(clear).toHaveCount(0),
              cause: pressEnter(context.page),
              caused: true,
              control: clear,
              named: {
                at: listTrigger(deliveryHeader(context.page), "Window"),
                name: "the Window list's trigger",
              },
            };
          },
        },
        live: { arrange: groupDeleted("api") },
        device: { arrange: groupDeleted("device") },
        "device-after-rename": { arrange: groupDeleted("device", true) },
      },
    },
    {
      what: "a press in the Session groups dialog, or with the Window control out of sight",
      cases: {
        "dialog-last-card": { arrange: lastCardDeleted },
        "dialog-only-card": { arrange: onlyCardDeleted },
        "held-rename": { arrange: heldRename },
        "walk-unseen": { arrange: brushWindowUnseen },
      },
    },
    {
      what: "Save delivery settings offline past the request deadline",
      seed: async () => {
        await seedDeliveryFixture();
        return { issue: "ACME-1", project: "ACME" };
      },
      cases: { "settings-save-offline": { arrange: settingsSaveOffline } },
    },
    {
      what: "an offline press past the request deadline",
      cases: {
        // Rename is held, by `aria-disabled`, while its write is out: offline its write stays
        // out past the deadline, and nothing takes the card away.
        "offline-rename": {
          arrange: async (context) => {
            const { path, rename } = await groupCard(context);
            return {
              after: () => deleteAsReader(path, "the session group"),
              answered: () => expect(rename).toHaveAttribute("aria-disabled", "true"),
              cause: pressEnter(context.page),
              caused: true,
              control: rename,
              named: { at: rename, name: 'button "Rename"' },
              staged: { rule: "kept" },
            };
          },
          offline: true,
        },
      },
    },
  ],
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

/** The tester's row "a date the reader gives the search bar's Updated since is their press, so the
 *  focus stays in the Filter panel": the Filter panel draws its Updated since box only while no
 *  date is set, so the date the reader sets takes the focused box away. The walk back passes over
 *  Author, which takes typed text. */
const updatedSince: Arrange = async ({ page }) => {
  await page.goto("/");
  await expect(page.getByRole("heading", { level: 1, name: "Inbox" })).toBeVisible();
  await page.locator("body").focus();
  await page.keyboard.press("Control+k");
  const bar = page.getByRole("dialog", { name: "Search" });
  await expect(bar).toBeVisible();
  await control(controlRow(bar), CONTROL.searchBox).fill(SURFACE_SEARCH_TERM);
  const filters = await openFilterPanel(page, bar);
  const since = filters.getByLabel("Updated since");
  await expect(since).toBeVisible();
  // Tomorrow's date, in the date box's own format, read on the page's own clock.
  const day = await page.evaluate(() => {
    const next = new Date();
    next.setDate(next.getDate() + 1);
    const pad = (value: number) => String(value).padStart(2, "0");
    return `${next.getFullYear()}-${pad(next.getMonth() + 1)}-${pad(next.getDate())}`;
  });
  await since.focus();
  return {
    answered: () => expect(since).toHaveCount(0),
    cause: () => since.fill(day),
    caused: true,
    control: since,
    staged: { passes: ["text"], rule: "walk-back" },
  };
};

/** Words no seeded item holds, which the bar offers as a new issue. */
const UNMATCHED_WORDS = "the sextant rota is nowhere written down";

/** The search bar's offer to make a new issue of words that match nothing, pressed offline from
 *  its search box: the issue's create is held past the request deadline. The bar offers it only
 *  over a page of a project, which the new issue is made in, so it is opened over the project's
 *  List, as the row "the bar offers unmatched words as a new issue, and accepting makes it and
 *  starts the conversation" opens it. The deployment's label and session for such issues are set
 *  through the server's test hook. */
const newIssueOffline: Arrange = async ({ page }) => {
  await setLiveSessions([plannerSession, reviewerSession]);
  await setNewIssueSettings({ label: "from-the-bar", session: sessionIds.planner });
  await openIssuesTab(page, "CORE", "List");
  await expect(page.locator("[data-issue-row]").first()).toBeVisible();
  await page.locator("body").focus();
  await page.keyboard.press("/");
  const bar = page.getByRole("dialog", { name: "Search" });
  await expect(bar).toBeVisible();
  const box = page.getByRole("combobox", { name: "Search" });
  await box.fill(UNMATCHED_WORDS);
  const offer = bar.getByRole("option", { name: `Make a new issue from “${UNMATCHED_WORDS}”` });
  await expect(offer).toBeVisible();
  await box.focus();
  await page.keyboard.press("ArrowDown");
  await expect(offer).toHaveAttribute("aria-selected", "true");
  return {
    answered: () => expect(page.getByTestId("pending-writes")).toBeVisible(),
    cause: pressEnter(page),
    caused: true,
    control: box,
  };
};

/** The search bar's rows. */
export const SEARCH_SCENE: Scene = {
  seed: seedSurfaces,
  rows: [
    {
      what: "a press",
      cases: {
        press: { arrange: searchChips },
        live: {
          none: "the bar's hits are the answer of one search, which no live update reads again, and its chips are the reader's own narrowing, so a live update takes no control of the bar away",
        },
        device: {
          none: "a change on another device reaches this page only by a live update, which takes no control of the bar away",
        },
        "walk-text": { arrange: updatedSince },
      },
    },
    {
      what: "an offline press past the request deadline",
      cases: {
        "offline-chip": { arrange: searchChips, offline: true },
        "offline-new-issue": { arrange: newIssueOffline, offline: true },
      },
    },
  ],
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
  // One read of the rows: the wait above found the three removable documents drawn.
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

/**
 * The tester's row "a references Retry on the Artifacts tab keeps the keyboard focus while its read
 * is out": the tab has read the issue's references once, a message on another issue that mentions
 * it makes the live feed read them again, which fails, and the reader presses the failed line's
 * Retry with the read it sends held. The read answers, and the line goes with Retry.
 */
const referencesRetry: Arrange = async ({ n, page, scene }) => {
  await createProject({ key: "OPS", name: "Operations" }).catch((cause: unknown) => {
    if (!String(cause).includes("PROJECT_EXISTS")) throw cause;
  });
  const issue = await createIssue({ project: "CORE", title: `Lease format ${n}` });
  const source = await createIssue({ project: "OPS", title: `Mirror plan ${n}` });
  await createMessage(source.key, { body: `Depends on dispatch://${issue.key}.` });
  const reads = { held: [] as Route[], mode: "answer" as "answer" | "fail" | "hold" };
  const path = `/api/v1/issues/${issue.key}/references`;
  const matches = (url: URL) => url.pathname === path;
  const answer = async (route: Route) => {
    if (route.request().method() !== "GET") return route.fallback();
    if (reads.mode === "hold") {
      reads.held.push(route);
      return;
    }
    if (reads.mode === "answer") return route.continue();
    await route.fulfill({
      body: JSON.stringify({ code: "INTERNAL", error: "The references read failed." }),
      contentType: "application/json",
      status: 500,
    });
  };
  await page.route(matches, answer);
  await openSurface(page, "Artifacts tab", { ...scene, issue: issue.key });
  await expect(page.locator("summary").filter({ hasText: /^Referenced by \(1\)$/ })).toBeVisible();
  reads.mode = "fail";
  await createMessage(source.key, { body: `Also dispatch://${issue.key}.` });
  const alert = page.getByRole("alert").filter({ hasText: "References unavailable" });
  await expect(alert).toBeVisible();
  const retry = alert.getByRole("button", { exact: true, name: "Retry" });
  await retry.focus();
  reads.mode = "hold";
  return {
    after: () => page.unroute(matches, answer),
    answered: () => expect.poll(() => reads.held.length, "Retry sent no read").toBe(1),
    cause: pressEnter(page),
    caused: true,
    control: retry,
    held: {
      landed: () => expect(alert).toHaveCount(0),
      release: async () => {
        reads.mode = "answer";
        for (const route of reads.held.splice(0)) await route.continue();
      },
    },
  };
};

/** The Artifacts tab's rows. */
export const ARTIFACTS_SCENE: Scene = {
  seed: seedSurfaces,
  rows: [
    {
      what: "a press, a live update or another device",
      cases: {
        // Remove is held through the list's read after its write, and its row then goes. The next
        // document's row moves up into its place, so the focus goes to that row's Remove.
        press: {
          arrange: async (context) => {
            const { next, remove, root, target } = await documentRows(context);
            return {
              answered: () => expect(root.getByTestId(target)).toHaveCount(0),
              cause: pressEnter(context.page),
              caused: true,
              control: remove,
              named: {
                at: root.getByTestId(next).getByRole("button", { exact: true, name: "Remove" }),
                name: `button "Remove" in ${next}`,
              },
              staged: { rule: "moved-up" },
            };
          },
        },
        live: {
          arrange: async (context) => {
            const { remove, root, target, targetId } = await documentRows(context);
            return {
              answered: () => expect(root.getByTestId(target)).toHaveCount(0),
              cause: () =>
                deleteAsReader(`/api/v1/artifacts/${targetId}`, `the document ${target}`),
              caused: false,
              control: remove,
            };
          },
        },
        device: {
          arrange: async (context) => {
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
              caused: false,
              control: remove,
            };
          },
        },
        "held-references-retry": { arrange: referencesRetry },
      },
    },
    {
      what: "an offline press past the request deadline",
      cases: {
        // Remove is held, by `aria-disabled` and its label Removing…, while its write is out, so
        // it keeps the focus: offline its write stays out past the deadline, and its row stays.
        "offline-remove": {
          arrange: async (context) => {
            const { remove, root, target } = await documentRows(context);
            return {
              answered: () =>
                expect(
                  root.getByTestId(target).getByRole("button", { exact: true, name: "Removing…" })
                ).toHaveAttribute("aria-disabled", "true"),
              cause: pressEnter(context.page),
              caused: true,
              control: remove,
              staged: { rule: "kept" },
            };
          },
          offline: true,
        },
      },
    },
  ],
};
