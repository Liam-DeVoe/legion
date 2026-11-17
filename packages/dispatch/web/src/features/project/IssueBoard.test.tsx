import { afterEach, expect, type Mock, setSystemTime, spyOn, test } from "bun:test";
import * as dndCore from "@dnd-kit/core";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  act,
  // biome-ignore lint/style/noRestrictedImports: this file keeps fireEvent for the mouse and pointer presses that drive dnd-kit, the key inside one of those drags, and the clicks whose boolean return it reads
  fireEvent,
  render,
  renderHook,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { MemoryRouter, useLocation } from "react-router-dom";

import { installCountingStorage } from "../../__tests__/counting-storage";
import { keyedAttempt } from "../../__tests__/keyed-attempt";
import {
  type FakeLiveSockets,
  installFakeLiveSockets,
  snapshotFrame,
} from "../../__tests__/live-socket-harness";
import { answerIssuePatches } from "../../__tests__/send-answers";
import { ApiError, api } from "../../api/client";
import type {
  Issue,
  IssueClaim,
  IssueSummary,
  LegionBoardCard,
  LegionBoardFacts,
  LegionBoardResponse,
  LegionSetting,
  UpdateIssueInput,
} from "../../api/types";
import * as routeReach from "../issue/RouteReach";
import { KeymapProvider } from "../shell/KeymapProvider";
import { useKeymapScope } from "../shell/keymap";
import { userPreferenceStorageKey } from "../shell/userPreference";
import { DAEMON_SETS_PHASE } from "./board-model";
import { staleBoardMessage, useBoardMoves } from "./board-moves";
import { IssuesTable } from "./IssuesTable";
import { LEGION_PHASES, type LegionPhase } from "./issues-legion";
import { MEASURE_METHODS } from "./issues-table-model";

function issue(key: string, overrides: Partial<IssueSummary> = {}): IssueSummary {
  return {
    key,
    advances: [],
    advances_revision: 0,
    title: `Work ${key}`,
    status: "todo",
    priority: null,
    rank: key,
    labels: [],
    parent: null,
    blocked_by: [],
    assignee: null,
    claim: null,
    components: { mode: "inherit", ids: [], unknown: [], reason: null, inherited_from: null },
    route: null,
    repo: null,
    resolved_repo: null,
    route_status: null,
    route_holder: null,
    route_status_reason: null,
    last_seq: 2,
    open_asks: 0,
    progress: { tasks: null, children: null },
    primary_artifact_id: "spec",
    created_by: { kind: "user", id: "alice" },
    created_at: "2026-09-10T00:00:00Z",
    updated_at: "2026-09-10T00:00:00Z",
    closed_at: overrides.status === "done" ? "2026-09-10T00:00:00Z" : null,
    ...overrides,
  };
}

/** What a PATCH answers with: the issue `key` as the server saved it. */
function saved(key: string, overrides: Partial<Issue> = {}): Issue {
  return {
    ...issue(key),
    labels: [],
    project: "CORE",
    number: Number(key.split("-")[1]),
    closed_at: null,
    external_links: [],
    decisions_artifact_id: "",
    plan_artifact_id: "",
    verification_artifact_id: "",
    ...overrides,
  };
}

function Route(): ReactNode {
  useKeymapScope("project");
  const location = useLocation();
  return (
    <>
      <output data-testid="route">{location.pathname}</output>
      <output data-testid="route-search">{location.search}</output>
    </>
  );
}

const restores: (() => void)[] = [];
afterEach(() => {
  for (const restore of restores.splice(0)) restore();
  localStorage.clear();
});

/** A project's daemon setting that names a daemon. */
const DAEMON: LegionSetting = {
  created_by: { kind: "user", id: "alice" },
  project: "CORE",
  state_url: "http://legion.test",
  updated_at: "2026-11-01T00:00:00Z",
};

/**
 * Draws the Issues tab of CORE at `/projects/CORE/issues<search>` over `issues`, as the listing
 * route answers them: `open` leaves the closed rows out and `status` keeps the rows in those
 * statuses. `daemon` is the project's Legion daemon setting, none by default.
 */
function board(issues: IssueSummary[], search: string, daemon: LegionSetting | null = null) {
  const listing = spyOn(api, "listIssues").mockImplementation(async (options = {}) =>
    issues.filter(
      (row) =>
        (!options.open || row.status !== "done") &&
        (options.status === undefined || options.status.includes(row.status))
    )
  );
  const page = spyOn(api, "listIssuePage").mockImplementation(async (options) => {
    const rows = issues.filter(
      (row) =>
        (!options.open || row.status !== "done") &&
        (options.status === undefined || options.status.includes(row.status))
    );
    return {
      issues: rows.slice(0, options.limit),
      limit: options.limit,
      offset: 0,
      total: rows.length,
    };
  });
  const setting = spyOn(api, "getLegionDaemon").mockResolvedValue(daemon);
  const state = spyOn(api, "getMyState").mockResolvedValue({});
  const agents = spyOn(api, "listAgents").mockResolvedValue([]);
  const viewer = spyOn(api, "whoAmI").mockResolvedValue({
    event_head: "0",
    login: "alice",
    kind: "user",
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  client.setQueryData(["whoami"], { event_head: "0", login: "alice", kind: "user" });
  const view = render(
    <MemoryRouter initialEntries={[`/projects/CORE/issues${search}`]}>
      <QueryClientProvider client={client}>
        <KeymapProvider>
          <Route />
          <IssuesTable listing={{ project: "CORE" }} />
        </KeymapProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  restores.push(() => {
    view.unmount();
    client.clear();
    listing.mockRestore();
    page.mockRestore();
    setting.mockRestore();
    state.mockRestore();
    agents.mockRestore();
    viewer.mockRestore();
  });
  return { client, listing };
}

/** The address the page is at now. */
const routeSearch = () => new URLSearchParams(screen.getByTestId("route-search").textContent ?? "");

/** The Board, once it is drawn. */
const boardRegion = () => screen.findByRole("region", { name: "Project board" });

/** The ids of the columns and rails the Board draws, in its order. */
function columnIds(region: HTMLElement): (string | null)[] {
  return [...region.querySelectorAll("[data-board-column]")].map((column) =>
    column.getAttribute("data-board-column")
  );
}

/** The keys of the cards a column or a region draws, in their order. */
function cardKeys(region: HTMLElement): (string | null)[] {
  return [...region.querySelectorAll("[data-board-card]")].map((card) =>
    card.getAttribute("data-board-card")
  );
}

const liveRegion = () => screen.getByRole("status", { name: "Board announcements" });

/** Lets `ms` of real time pass, and React draw whatever it set off. */
async function waitMs(ms: number): Promise<void> {
  await act(async () => {
    const { promise, resolve } = Promise.withResolvers<void>();
    setTimeout(resolve, ms);
    await promise;
  });
}

/** A PATCH that never answers: the Board stays as the move placed it. */
function patchInFlight() {
  const patch = answerIssuePatches().mockImplementation(
    () => Promise.withResolvers<Issue>().promise
  );
  restores.push(() => patch.mockRestore());
  return patch;
}

/** A card of the Legion board, with no fact but its key. */
function legionCard(key: string, overrides: Partial<LegionBoardCard> = {}): LegionBoardCard {
  return {
    blocked_by: null,
    children: 0,
    key,
    phase: "implementing",
    phase_since: null,
    priority: null,
    roles: null,
    slot: null,
    slot_since: null,
    slot_use: null,
    title: `Work ${key}`,
    tree: key,
    tree_slot: null,
    waiting: null,
    waiting_on: null,
    waiting_place: null,
    ...overrides,
  };
}

/** The facts the board's map holds for an issue with `workHours` of work. */
function boardFacts(workHours: number): LegionBoardFacts {
  return {
    closed_at: null,
    hours_by_phase: { implementing: workHours },
    opened_at: "2026-11-01T00:00:00Z",
    review_rounds: 0,
    root_key: "CORE-1",
    slot_hours: workHours,
    tree_depth: 0,
    updated_at: "2026-11-17T08:00:00Z",
    waiting_reason: "",
    work_first_at: "2026-11-02T00:00:00Z",
    work_hours: workHours,
    work_last_at: "2026-11-10T00:00:00Z",
  };
}

/** The board route's answer: its eleven columns with `cards` in them, and the facts its map
 *  holds. */
function legionBoard(
  cards: Partial<Record<LegionPhase, LegionBoardCard[]>>,
  issues: Record<string, LegionBoardFacts> = {}
): LegionBoardResponse {
  return {
    columns: LEGION_PHASES.map((phase) => ({
      id: phase.id,
      label: phase.title,
      cards: cards[phase.id] ?? [],
    })),
    computed_at: "2026-11-17T08:00:00Z",
    header: {
      facts_cursor_age_seconds: null,
      facts_updated_at: null,
      queue_length: (cards.queue ?? []).length,
      read_at: "2026-11-17T08:00:00Z",
      read_error: null,
      read_ok: true,
      slot_cap: 4,
      slots_in_use: 1,
      trees: 1,
    },
    issues,
    project: "CORE",
    settings: null,
  };
}

/** The live sockets the page opens, for the rest of the test. */
function liveSockets(): FakeLiveSockets {
  const sockets = installFakeLiveSockets();
  restores.push(() => sockets.restore());
  return sockets;
}

/** Dispatch answers the page's board subscription with `answer`. */
async function answerBoard(sockets: FakeLiveSockets, answer: LegionBoardResponse): Promise<void> {
  await waitFor(() => expect(sockets.sockets.length).toBeGreaterThan(0));
  const socket = sockets.latest();
  if (socket.readyState !== 1) act(() => socket.open());
  const { sub } = await waitFor(() => socket.subscribed("legion"));
  act(() => socket.receive(snapshotFrame(sub, answer)));
}

/** Every listing read the page made: none of them is the full listing, which names neither
 *  `open` nor `status`. */
function expectNoFullListing(listing: Mock<typeof api.listIssues>) {
  expect(listing).toHaveBeenCalled();
  for (const [options] of listing.mock.calls) {
    expect(options?.project).toBe("CORE");
    expect(options?.open === true || options?.status !== undefined).toBe(true);
    expect(options?.label).toBeUndefined();
  }
}

/** A row of each status, Done among them. */
const STATUS_ROWS = () => [
  issue("CORE-1", { status: "triage" }),
  issue("CORE-2", { status: "icebox" }),
  issue("CORE-3", { status: "todo" }),
  issue("CORE-4", { status: "in_progress" }),
  issue("CORE-5", { status: "done" }),
];

// By Status, Open picks the eight open statuses: a column for each, an empty one too, with the
// Icebox rail and its count after Triage, and the Done rail, with no count, since Open lists no
// done issue and so reads none.
test("Open's Board draws the open statuses, the Icebox rail with its count and the Done rail", async () => {
  const user = userEvent.setup();
  const { listing } = board(STATUS_ROWS(), "?layout=board");
  const region = await boardRegion();
  await waitFor(() => expect(cardKeys(region)).toEqual(["CORE-1", "CORE-3", "CORE-4"]));
  expect(columnIds(region)).toEqual([
    "triage",
    "icebox",
    "backlog",
    "todo",
    "in_progress",
    "testing",
    "needs_review",
    "retro",
    "done",
  ]);
  const icebox = within(region).getByRole("region", { name: "Icebox (collapsed)" });
  expect(icebox.textContent).toBe("1Icebox");
  expect(within(region).getByRole("region", { name: "Done (collapsed)" }).textContent).toBe("Done");
  expect(
    within(within(region).getByRole("region", { name: "Backlog" })).getByText("Nothing here")
  ).toBeDefined();
  expect(region.querySelector("[data-testid=board-column-header]")?.textContent).toBe("Triage1");
  expectNoFullListing(listing);
  expect(listing.mock.calls.every(([options]) => options?.open === true)).toBe(true);

  // Show Icebox turns the rail into the Icebox column, with its cards.
  await user.click(screen.getByRole("button", { name: "Show Icebox" }));
  expect(cardKeys(within(region).getByRole("region", { name: "Icebox" }))).toEqual(["CORE-2"]);
  expect(within(region).queryByRole("region", { name: "Icebox (collapsed)" })).toBeNull();
});

test.each([
  // A Status filter's picks are the columns, with the Done rail.
  ["status=triage", "?layout=board&status=triage", ["triage", "done"], ["CORE-1"]],
  // No Status filter: each status a row is in, and the Done column with the done cards, since
  // the view lists done issues.
  [
    "nofilter=1",
    "?layout=board&nofilter=1",
    ["triage", "icebox", "todo", "in_progress", "done"],
    ["CORE-1", "CORE-3", "CORE-4", "CORE-5"],
  ],
  // Done: its done cards in the Done column.
  ["view=done", "?view=done&layout=board", ["done"], ["CORE-5"]],
])("by Status with %s the Board draws its columns and cards", async (_name, search, columns, cards) => {
  const { listing } = board(STATUS_ROWS(), search);
  const region = await boardRegion();
  await waitFor(() => expect(cardKeys(region)).toEqual(cards));
  expect(columnIds(region)).toEqual(columns);
  expectNoFullListing(listing);
  if (columns.includes("done") && cards.includes("CORE-5"))
    expect(cardKeys(within(region).getByRole("region", { name: "Done" }))).toEqual(["CORE-5"]);
});

/** Open's rows on a project with a daemon: an Implementing card, a Queue card, and two rows with
 *  no card. */
const PHASE_ROWS = () => [
  issue("CORE-1", { status: "in_progress" }),
  issue("CORE-2", { status: "todo" }),
  issue("CORE-3", { status: "todo" }),
  issue("CORE-4", { status: "backlog" }),
];
const PHASE_BOARD = () =>
  legionBoard({
    implementing: [legionCard("CORE-1")],
    queue: [legionCard("CORE-2", { phase: "admitted", waiting_place: 1 })],
  });

// By Legion phase: one column for each of the board's eleven columns in its order, empty ones
// included, then the None rail with the count of the rows with no card; no Show Icebox and no
// Done rail.
test("by Legion phase the Board draws the eleven phases and the None rail, and no Show Icebox", async () => {
  const sockets = liveSockets();
  const { listing } = board(PHASE_ROWS(), "?layout=board&group=legion_phase", DAEMON);
  // The view's columns read the board, so it waits for it.
  expect(await screen.findByRole("status", { name: "Loading issues" })).toBeDefined();
  await answerBoard(sockets, PHASE_BOARD());
  const region = await boardRegion();
  expect(columnIds(region)).toEqual([...LEGION_PHASES.map((phase) => phase.id), "none"]);
  expect(
    [...region.querySelectorAll("[data-testid=board-column-header]")].map(
      (header) => header.firstElementChild?.firstElementChild?.textContent
    )
  ).toEqual(LEGION_PHASES.map((phase) => phase.title));
  expect(cardKeys(within(region).getByRole("region", { name: "Queue" }))).toEqual(["CORE-2"]);
  expect(cardKeys(within(region).getByRole("region", { name: "Implementing" }))).toEqual([
    "CORE-1",
  ]);
  expect(within(region).getByRole("region", { name: "None (collapsed)" }).textContent).toBe(
    "2None"
  );
  expect(within(region).queryByRole("region", { name: "Done (collapsed)" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Show Icebox" })).toBeNull();
  expect(screen.getByRole("button", { name: "Lanes" })).toBeDefined();
  expectNoFullListing(listing);
});

test.each([
  ["implementing", ["implementing"], ["CORE-1"]],
  ["none", ["none"], []],
])("a Legion phase filter of %s keeps only its columns", async (pick, columns, cards) => {
  const sockets = liveSockets();
  board(PHASE_ROWS(), `?layout=board&group=legion_phase&f.legion_phase=${pick}`, DAEMON);
  await answerBoard(sockets, PHASE_BOARD());
  const region = await boardRegion();
  expect(columnIds(region)).toEqual(columns);
  expect(cardKeys(region)).toEqual(cards);
});

// Group by on the Board: Status and Legion phase where the project has a daemon, kept in the
// address as `group=legion_phase`, and no menu with no daemon.
test("the Board's Group by picks Status or Legion phase, and a project with no daemon draws none", async () => {
  const user = userEvent.setup();
  const sockets = liveSockets();
  board(PHASE_ROWS(), "?layout=board", DAEMON);
  await answerBoard(sockets, PHASE_BOARD());
  await boardRegion();
  await user.click(screen.getByRole("button", { name: "Group by" }));
  expect(screen.getAllByRole("radio")).toHaveLength(2);
  expect(screen.getByRole("radio", { name: "Legion phase" })).not.toBeChecked();
  expect(screen.getByRole("radio", { name: "Status" })).toBeChecked();
  await user.click(screen.getByRole("radio", { name: "Legion phase" }));
  await waitFor(() => expect(routeSearch().get("group")).toBe("legion_phase"));
  await waitFor(async () => expect(columnIds(await boardRegion())).toContain("none"));
  await user.click(screen.getByRole("radio", { name: "Status" }));
  await waitFor(() => expect(routeSearch().has("group")).toBe(false));
  await waitFor(async () => expect(columnIds(await boardRegion())).toContain("done"));
});

test("a Board with no Legion daemon draws no Group by menu", async () => {
  board(STATUS_ROWS(), "?layout=board");
  await boardRegion();
  expect(screen.queryByRole("button", { name: "Group by" })).toBeNull();
});

const CLAIM: IssueClaim = {
  actor: { kind: "session", id: "session-one", origin: { session_title: "Implementer" } },
  at: "2026-09-24T06:00:00Z",
};

// A card shows today's card and, for an issue with a card on the Legion board, its Legion fields
// from the board route, each left out where the board gives it no value.
test("a card draws its Legion fields and leaves out every one the board gives no value", async () => {
  const user = userEvent.setup();
  const sockets = liveSockets();
  const since = new Date(Date.now() - (2 * 60 + 5) * 60_000).toISOString();
  board(
    [
      issue("CORE-1", { status: "in_progress", claim: CLAIM, open_asks: 2 }),
      issue("CORE-2", { status: "todo", open_asks: 1 }),
      issue("CORE-3", { status: "todo" }),
    ],
    "?layout=board",
    DAEMON
  );
  await answerBoard(
    sockets,
    legionBoard(
      {
        implementing: [
          legionCard("CORE-1", {
            phase_since: since,
            roles: ["implementer", "tester"],
            slot: 2,
            waiting: { reason: "blocked", since, blocked_by: ["CORE-9"] },
            waiting_on: {
              kind: "ask",
              since: "2026-11-16T10:00:00Z",
              answered_at: "2026-11-16T11:00:00Z",
            },
            children: 1,
          }),
        ],
        queue: [legionCard("CORE-2", { phase: "admitted", slot: 1, waiting_place: 2 })],
      },
      { "CORE-1": boardFacts(3) }
    )
  );
  const region = await boardRegion();
  const first = await waitFor(() => {
    const card = within(region).getByRole("article", { name: "CORE-1 Work CORE-1" });
    expect(card.querySelector("[data-legion-fact=phase-time]")).not.toBeNull();
    return card;
  });
  const fact = (card: HTMLElement, hook: string) =>
    card.querySelector<HTMLElement>(`[data-legion-fact=${hook}]`);
  expect(fact(first, "phase-time")?.textContent).toBe("Time in Legion phase2 h 5 m");
  expect(fact(first, "work-time")?.textContent).toBe("Work time3 h 0 m");
  expect(fact(first, "roles")?.textContent).toBe("WorkersImplementer, Tester");
  // `blocked_by` names no blocker, so the wait's blockers do.
  expect(fact(first, "blocked-by")?.textContent).toBe("Blocked by CORE-9");
  expect(fact(first, "waiting-on")?.textContent).toBe(
    `Waiting on ask since ${new Date("2026-11-16T10:00:00Z").toLocaleString()}, answered`
  );
  expect(fact(first, "children")?.textContent).toBe("1 issue of this tree not started");
  // The claim chip writes the holder's whole name over its age and state.
  expect(first.querySelector("[data-claim-stacked]")).not.toBeNull();
  // No slot number, and no word "slot"; and no Place, which the board gives this card none of.
  expect(first.textContent).not.toMatch(/slot|#/i);
  expect(first.querySelector("[data-queue-place]")).toBeNull();

  // Each measure opens its column's method in the shared tooltip, from the keyboard and the
  // pointer, and carries no native title.
  const phaseTime = fact(first, "phase-time") as HTMLElement;
  expect(phaseTime).toHaveAttribute("tabindex", "0");
  expect(phaseTime.hasAttribute("title")).toBe(false);
  act(() => phaseTime.focus());
  expect((await screen.findByRole("tooltip")).textContent).toBe(MEASURE_METHODS.time_in_phase);
  act(() => phaseTime.blur());
  await user.hover(fact(first, "work-time") as HTMLElement);
  expect((await screen.findByRole("tooltip")).textContent).toBe(MEASURE_METHODS.work_hours);
  await user.unhover(fact(first, "work-time") as HTMLElement);

  // The open asks badge says what it counts in the tooltip, is a tab stop, and has no title.
  const asks = first.querySelector<HTMLElement>("[data-open-asks]") as HTMLElement;
  expect(asks).toHaveAttribute("tabindex", "0");
  expect(asks.hasAttribute("title")).toBe(false);
  expect(asks.textContent).toBe("2 open asks");
  act(() => asks.focus());
  expect((await screen.findByRole("tooltip")).textContent).toBe("2 open asks");
  act(() => asks.blur());
  const second = within(region).getByRole("article", { name: "CORE-2 Work CORE-2" });
  expect(second.querySelector("[data-open-asks]")?.textContent).toBe("1 open ask");

  // A card with a card on the board and no value for any fact draws none: no Workers with no
  // worker, no time with no phase entry, no Work time with no facts, and never a dash.
  expect(second.querySelector("[data-legion-fact]")).toBeNull();
  expect(second.textContent).not.toContain("—");
  // A Queue card draws its Place in the Legion queue beside its key.
  expect(second.querySelector("[data-queue-place]")?.textContent).toBe("#2");
  expect(second.querySelector("[data-issue-link]")?.firstElementChild?.textContent).toBe(
    "CORE-2#2"
  );
  // A row with no card draws today's card alone, with no Place.
  const third = within(region).getByRole("article", { name: "CORE-3 Work CORE-3" });
  expect(third.querySelector("[data-legion-fact]")).toBeNull();
  expect(third.querySelector("[data-queue-place]")).toBeNull();
  expect(third.textContent).not.toContain("#");
});

// Todo: Alpha, Bravo, Charlie; In progress: Delta - the keyboard rows' seed. No row is in the
// Legion queue, so Open's sort, Queue, orders them by rank.
const keyboardIssues = () => [
  issue("CORE-1", {
    rank: "a",
    status: "todo",
    title: "Alpha",
    created_at: "2026-09-01T00:00:00Z",
  }),
  issue("CORE-2", {
    rank: "b",
    status: "todo",
    title: "Bravo",
    created_at: "2026-09-03T00:00:00Z",
  }),
  issue("CORE-3", {
    rank: "c",
    status: "todo",
    title: "Charlie",
    created_at: "2026-09-02T00:00:00Z",
  }),
  issue("CORE-4", { rank: "d", status: "in_progress", title: "Delta" }),
];

test("Shift+J under the Place sort writes the rank, reads the card's place out and keeps focus; Shift+K at the top does nothing", async () => {
  const user = userEvent.setup();
  const patch = patchInFlight();
  board(keyboardIssues(), "?layout=board");
  const region = await boardRegion();
  const alpha = await within(region).findByRole("article", { name: "CORE-1 Alpha" });
  expect(liveRegion()).toHaveClass("sr-only");
  act(() => alpha.focus());

  await user.keyboard("{Shift>}K{/Shift}");
  expect(patch).not.toHaveBeenCalled();
  expect(liveRegion().textContent).toBe("");

  await user.keyboard("{Shift>}J{/Shift}");
  expect(patch).toHaveBeenCalledWith(
    "CORE-1",
    { rank: { after: "CORE-2", before: "CORE-3" } },
    keyedAttempt
  );
  expect(liveRegion().textContent).toBe("CORE-1 → Todo, position 2 of 3");
  const todo = within(region).getByRole("region", { name: "Todo" });
  await waitFor(() => expect(cardKeys(todo)).toEqual(["CORE-2", "CORE-1", "CORE-3"]));
  expect(within(region).getByRole("article", { name: "CORE-1 Alpha" })).toHaveFocus();
});

test("Shift+L sets the next status, to the top where the rank rule lets it, and focus follows the card", async () => {
  const user = userEvent.setup();
  const patch = patchInFlight();
  board(keyboardIssues(), "?layout=board");
  const region = await boardRegion();
  act(() => within(region).getByRole("article", { name: "CORE-2 Bravo" }).focus());
  await user.keyboard("{Shift>}L{/Shift}");
  expect(patch).toHaveBeenCalledWith(
    "CORE-2",
    { status: "in_progress", rank: { before: "CORE-4" } },
    keyedAttempt
  );
  expect(liveRegion().textContent).toBe("CORE-2 → In progress, position 1 of 2");
  const inProgress = within(region).getByRole("region", { name: "In progress" });
  await waitFor(() => expect(cardKeys(inProgress)).toEqual(["CORE-2", "CORE-4"]));
  await waitFor(() =>
    expect(within(inProgress).getByRole("article", { name: "CORE-2 Bravo" })).toHaveFocus()
  );
});

// Shift+L through every column that takes a drop, to the Done rail, which closes the issue: the
// card leaves Open, and the focus lands on the rail.
test("Shift+L walks a card to the Done rail, which closes it", async () => {
  const user = userEvent.setup();
  const patch = patchInFlight();
  board([issue("CORE-1", { status: "retro", title: "Last" })], "?layout=board");
  const region = await boardRegion();
  act(() => within(region).getByRole("article", { name: "CORE-1 Last" }).focus());
  await user.keyboard("{Shift>}L{/Shift}");
  expect(patch).toHaveBeenCalledWith("CORE-1", { status: "done" }, keyedAttempt);
  expect(liveRegion().textContent).toBe("CORE-1 → Done, closed");
  await waitFor(() => expect(cardKeys(region)).toEqual([]));
  await waitFor(() =>
    expect(within(region).getByRole("region", { name: "Done (collapsed)" })).toHaveFocus()
  );
  // At the Board's edge a move does nothing.
  patch.mockClear();
  await user.keyboard("{Shift>}L{/Shift}");
  expect(patch).not.toHaveBeenCalled();
});

// A sorted Board takes no move within a column, at its end too, since the sort would put the
// card back; a move into another column sets the status and writes no rank, the card landing
// where the sort puts it.
test("under another sort a move within a column is refused with the sort named, and a status move writes no rank", async () => {
  const user = userEvent.setup();
  const patch = patchInFlight();
  board(keyboardIssues(), "?layout=board&sort=-created");
  const region = await boardRegion();
  const todo = within(region).getByRole("region", { name: "Todo" });
  await waitFor(() => expect(cardKeys(todo)).toEqual(["CORE-2", "CORE-3", "CORE-1"]));
  const refusal = "Sorted by Created. Sort by Queue to move cards in a column.";
  act(() => within(region).getByRole("article", { name: "CORE-1 Alpha" }).focus());
  await user.keyboard("{Shift>}J{/Shift}");
  expect(liveRegion().textContent).toBe(refusal);
  expect(screen.getByTestId("board-refusal").textContent).toBe(refusal);
  await user.keyboard("{Shift>}K{/Shift}");
  expect(patch).not.toHaveBeenCalled();

  await user.keyboard("{Shift>}L{/Shift}");
  expect(patch).toHaveBeenCalledWith("CORE-1", { status: "in_progress" }, keyedAttempt);
  expect(screen.queryByTestId("board-refusal")).toBeNull();
  const inProgress = within(region).getByRole("region", { name: "In progress" });
  // Where the sort put it: newest first, after CORE-4, which was created after it.
  await waitFor(() => expect(cardKeys(inProgress)).toEqual(["CORE-4", "CORE-1"]));
  await waitFor(() =>
    expect(liveRegion().textContent).toBe("CORE-1 → In progress, position 2 of 2")
  );
});

// Under the Place sort a card with a place in the Legion queue (here Start next's, the Todo
// issues labelled `legion`, with no daemon) sorts before every card with none, so the rank
// decides a move only among cards that all have a place or all have none.
test("a move between cards with a place and cards with none is refused with why", async () => {
  const user = userEvent.setup();
  const patch = patchInFlight();
  board(
    [
      issue("CORE-1", { rank: "a", labels: ["legion"], title: "First" }),
      issue("CORE-2", { rank: "b", labels: ["legion"], title: "Second" }),
      issue("CORE-3", { rank: "c", title: "Unplaced" }),
    ],
    "?layout=board"
  );
  const region = await boardRegion();
  act(() => within(region).getByRole("article", { name: "CORE-3 Unplaced" }).focus());
  await user.keyboard("{Shift>}K{/Shift}");
  expect(liveRegion().textContent).toBe(
    "A card with no place in the Legion queue stays after the cards with a place."
  );
  act(() => within(region).getByRole("article", { name: "CORE-2 Second" }).focus());
  await user.keyboard("{Shift>}J{/Shift}");
  expect(liveRegion().textContent).toBe(
    "A card with a place in the Legion queue stays before the cards with no place."
  );
  expect(patch).not.toHaveBeenCalled();
  await user.keyboard("{Shift>}K{/Shift}");
  expect(patch).toHaveBeenCalledWith("CORE-2", { rank: { before: "CORE-1" } }, keyedAttempt);
  expect(liveRegion().textContent).toBe("CORE-2 → Todo, position 1 of 3");
});

// By Legion phase the daemon sets each card's column: a move within Queue writes the rank, and
// a Queue card takes its new place on the daemon's next read; every other key move is refused,
// at the column's end too.
test("by Legion phase only a move within Queue is taken, and every other key move is refused", async () => {
  const user = userEvent.setup();
  const patch = patchInFlight();
  const sockets = liveSockets();
  board(
    [
      issue("CORE-1", { status: "in_progress", title: "Building" }),
      issue("CORE-2", { status: "in_progress", title: "Testing" }),
      issue("CORE-3", { status: "todo", title: "Next" }),
      issue("CORE-4", { status: "todo", title: "Later" }),
    ],
    "?layout=board&group=legion_phase",
    DAEMON
  );
  await answerBoard(
    sockets,
    legionBoard({
      implementing: [legionCard("CORE-1"), legionCard("CORE-2")],
      queue: [
        legionCard("CORE-3", { phase: "admitted", waiting_place: 1 }),
        legionCard("CORE-4", { phase: "admitted", waiting_place: 2 }),
      ],
    })
  );
  const region = await boardRegion();
  act(() => within(region).getByRole("article", { name: "CORE-2 Testing" }).focus());
  await user.keyboard("{Shift>}J{/Shift}");
  expect(liveRegion().textContent).toBe(DAEMON_SETS_PHASE);
  await user.keyboard("{Shift>}K{/Shift}");
  await user.keyboard("{Shift>}L{/Shift}");
  await user.keyboard("{Shift>}H{/Shift}");
  expect(screen.getByTestId("board-refusal").textContent).toBe(DAEMON_SETS_PHASE);
  expect(patch).not.toHaveBeenCalled();

  act(() => within(region).getByRole("article", { name: "CORE-3 Next" }).focus());
  await user.keyboard("{Shift>}J{/Shift}");
  expect(patch).toHaveBeenCalledWith("CORE-3", { rank: { after: "CORE-4" } }, keyedAttempt);
  // The place the card takes once the daemon reads the rank.
  expect(liveRegion().textContent).toBe("CORE-3 → Queue, position 2 of 2");
  await user.keyboard("{Shift>}L{/Shift}");
  expect(liveRegion().textContent).toBe(DAEMON_SETS_PHASE);
  expect(patch).toHaveBeenCalledTimes(1);
});

test("j/k/h/l rove real focus over cards, columns and rails", async () => {
  const user = userEvent.setup();
  board(keyboardIssues(), "?layout=board");
  const region = await boardRegion();
  const alpha = await within(region).findByRole("article", { name: "CORE-1 Alpha" });
  const bravo = within(region).getByRole("article", { name: "CORE-2 Bravo" });
  const delta = within(region).getByRole("article", { name: "CORE-4 Delta" });
  await user.keyboard("j");
  expect(alpha).toHaveFocus();
  await user.keyboard("j");
  expect(bravo).toHaveFocus();
  await user.keyboard("{ArrowUp}");
  expect(alpha).toHaveFocus();
  await user.keyboard("l");
  expect(delta).toHaveFocus();
  await user.keyboard("l");
  expect(within(region).getByRole("region", { name: "Testing" })).toHaveFocus();
  await user.keyboard("h");
  expect(delta).toHaveFocus();
  for (const _ of ["testing", "needs_review", "retro", "done"]) await user.keyboard("l");
  const doneRail = within(region).getByRole("region", { name: "Done (collapsed)" });
  expect(doneRail).toHaveFocus();
  await user.keyboard("l");
  expect(doneRail).toHaveFocus();
  await user.keyboard("{Escape}");
  expect(document.body).toHaveFocus();
  await user.keyboard("k");
  expect(delta).toHaveFocus();
  // Back to Todo, and h onto the Icebox rail.
  await user.keyboard("h");
  await user.keyboard("h");
  await user.keyboard("h");
  expect(within(region).getByRole("region", { name: "Icebox (collapsed)" })).toHaveFocus();
});

test("Enter and o open the focused card once, p focuses its priority select, Escape returns to the card and then out", async () => {
  const user = userEvent.setup();
  board(keyboardIssues(), "?layout=board");
  const region = await boardRegion();
  const alpha = await within(region).findByRole("article", { name: "CORE-1 Alpha" });
  const link = within(alpha).getByRole("link", { name: /CORE-1/ });
  let clicks = 0;
  link.addEventListener("click", () => {
    clicks += 1;
  });
  act(() => alpha.focus());
  await user.keyboard("p");
  const select = within(alpha).getByRole("combobox", { name: "Priority of CORE-1" });
  expect(select).toHaveFocus();
  await user.keyboard("j");
  expect(select).toHaveFocus();
  await user.keyboard("{Escape}");
  expect(alpha).toHaveFocus();
  await user.keyboard("{Escape}");
  expect(document.body).toHaveFocus();
  act(() => alpha.focus());
  await user.keyboard("{Enter}");
  expect(clicks).toBe(1);
  expect(screen.getByTestId("route").textContent).toBe("/issues/CORE-1");
});

test("Space on a focused card lifts nothing, and the card's instructions name the registry keys, not dnd-kit's grammar", async () => {
  const user = userEvent.setup();
  const patch = patchInFlight();
  board(keyboardIssues(), "?layout=board");
  const region = await boardRegion();
  const alpha = await within(region).findByRole("article", { name: "CORE-1 Alpha" });
  act(() => alpha.focus());
  await user.keyboard("[Space]");
  await user.keyboard("{ArrowDown}");
  await user.keyboard("[Space]");
  await act(async () => {});
  expect(patch).not.toHaveBeenCalled();
  expect(region.querySelector(".opacity-50")).toBeNull();
  const describedBy = alpha.getAttribute("aria-describedby");
  expect(describedBy).not.toBeNull();
  const instructions = document.getElementById(describedBy as string)?.textContent ?? "";
  expect(instructions).toContain("Shift with J, K, H or L");
  expect(instructions).not.toContain("space bar");
});

// The click the browser fires after a drop is the drag's, however late the main thread delivers
// it: a busy tab can take far longer than any window of milliseconds between the drop and the
// click, and a drop that navigates to the issue page loses the Board the reader was arranging.
/** Press a card and move far enough to lift it, then release it where it was picked up. */
async function dropInPlace(card: HTMLElement): Promise<void> {
  await act(async () => {
    // dnd-kit's mouse sensor is driven by this press; user-event's pointer API sends a fixed
    // sequence that never lifts the card.
    fireEvent.mouseDown(card, { button: 0, clientX: 10, clientY: 10 });
  });
  await act(async () => {
    // The move that lifts the card: user-event sends no mousemove of its own.
    fireEvent.mouseMove(document, { clientX: 10, clientY: 40 });
  });
  expect(card.className).toContain("opacity-50");
  await act(async () => {
    // The release that drops it, the other half of a gesture user-event cannot drive.
    fireEvent.mouseUp(document, { clientX: 10, clientY: 40 });
  });
}

/** The card as the drop drew it. */
async function droppedCard(): Promise<HTMLElement> {
  return within(await boardRegion()).getByRole("article", { name: "CORE-1 Alpha" });
}

/** A PATCH that answers at once with the issue as it was. */
function patchAnswered() {
  const patch = answerIssuePatches().mockImplementation(async (key) => saved(key));
  restores.push(() => patch.mockRestore());
  return patch;
}

test("the click that trails a drop never follows the card's link, however late it arrives", async () => {
  patchAnswered();
  board(keyboardIssues(), "?layout=board");
  const region = await boardRegion();
  await dropInPlace(await within(region).findByRole("article", { name: "CORE-1 Alpha" }));
  const link = within(await droppedCard()).getByRole("link", { name: /CORE-1/ });
  const now = spyOn(performance, "now").mockReturnValue(performance.now() + 1000);
  let trailingFollowed = true;
  try {
    await act(async () => {
      // The assertion reads fireEvent's boolean: whether the click was cancelled.
      trailingFollowed = fireEvent.click(link, { button: 0 });
    });
  } finally {
    now.mockRestore();
  }
  expect(trailingFollowed).toBe(false);
  expect(screen.getByTestId("route").textContent).toBe("/projects/CORE/issues");
});

// Only the drag's own click is the drag's: the mark a drop leaves does not outlive the gesture
// that follows it, by pointer or by key.
test("a drop that draws no click leaves the next press on the Board working", async () => {
  const user = userEvent.setup();
  patchAnswered();
  board(keyboardIssues(), "?layout=board");
  const region = await boardRegion();
  await dropInPlace(await within(region).findByRole("article", { name: "CORE-1 Alpha" }));
  // dnd-kit stops click propagation for 50 ms after a drop.
  await waitMs(80);
  act(() => within(region).getByRole("article", { name: "CORE-1 Alpha" }).focus());
  await user.keyboard("{Enter}");
  expect(screen.getByTestId("route").textContent).toBe("/issues/CORE-1");
});

test("a drag cancelled with Escape keeps its own release off the card's link", async () => {
  patchAnswered();
  board(keyboardIssues(), "?layout=board");
  const region = await boardRegion();
  const alpha = await within(region).findByRole("article", { name: "CORE-1 Alpha" });
  await act(async () => {
    fireEvent.mouseDown(alpha, { button: 0, clientX: 10, clientY: 10 });
  });
  await act(async () => {
    fireEvent.mouseMove(document, { clientX: 10, clientY: 40 });
  });
  await act(async () => {
    // dnd-kit's own cancel reads `code`, not `key`, and this key is one step of the mouse drag
    // above, which user-event cannot drive.
    fireEvent.keyDown(document, { code: "Escape", key: "Escape" });
  });
  await act(async () => {
    fireEvent.mouseUp(document, { clientX: 10, clientY: 40 });
  });
  expect(region.querySelector(".opacity-50")).toBeNull();
  let followed = true;
  await act(async () => {
    followed = fireEvent.click(within(region).getByRole("link", { name: /CORE-1/ }), {
      button: 0,
    });
  });
  expect(followed).toBe(false);
  expect(screen.getByTestId("route").textContent).toBe("/projects/CORE/issues");
});

test("a drop's mark never cancels a click outside the Board", async () => {
  patchAnswered();
  board(keyboardIssues(), "?layout=board");
  const elsewhere = document.createElement("a");
  elsewhere.href = "/issues/CORE-2";
  document.body.append(elsewhere);
  restores.push(() => elsewhere.remove());
  const region = await boardRegion();
  await dropInPlace(await within(region).findByRole("article", { name: "CORE-1 Alpha" }));
  let followed = false;
  await act(async () => {
    followed = fireEvent.click(elsewhere, { button: 0 });
  });
  expect(followed).toBe(true);
});

// A listing read asked for while a move is unanswered waits for the answer: the server may not
// have applied the move, and its list would put the card back.
test("a listing read asked for while a move is unanswered waits for the answer, then reads the moved Board", async () => {
  const user = userEvent.setup();
  const answer = Promise.withResolvers<Issue>();
  const patch = answerIssuePatches().mockImplementation(() => answer.promise);
  restores.push(() => patch.mockRestore());
  const rows = keyboardIssues();
  const { client, listing } = board(rows, "?layout=board");
  const region = await boardRegion();
  act(() => within(region).getByRole("article", { name: "CORE-2 Bravo" }).focus());
  await user.keyboard("{Shift>}L{/Shift}");
  const inProgress = within(region).getByRole("region", { name: "In progress" });
  await waitFor(() => expect(cardKeys(inProgress)).toEqual(["CORE-2", "CORE-4"]));
  const reads = listing.mock.calls.length;
  await act(async () => {
    void client.invalidateQueries({ queryKey: ["issues"] });
  });
  expect(listing.mock.calls.length).toBe(reads);
  expect(cardKeys(inProgress)).toEqual(["CORE-2", "CORE-4"]);

  rows[1] = { ...(rows[1] as IssueSummary), status: "in_progress", rank: "0" };
  await act(async () => {
    answer.resolve(saved("CORE-2", { status: "in_progress" }));
  });
  await waitFor(() => expect(listing.mock.calls.length).toBeGreaterThan(reads));
  await waitFor(() => expect(cardKeys(inProgress)).toEqual(["CORE-2", "CORE-4"]));
  expect(within(inProgress).getByRole("article", { name: "CORE-2 Bravo" })).toHaveFocus();
});

/** The move hook alone, over a client that holds `open` as the open listing. */
function renderMoves(open: IssueSummary[]) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const key = ["issues", "project", "CORE", "open"] as const;
  client.setQueryData(key, open);
  const hook = renderHook(() => useBoardMoves("CORE"), {
    wrapper: ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    ),
  });
  restores.push(() => hook.unmount());
  return { client, hook, key };
}

const move = (input: UpdateIssueInput, rank?: string) => ({
  input,
  ...(rank === undefined ? {} : { rank }),
});

test("a failed move puts the listing back, reads it again and tells the truth about a stale rank", async () => {
  const open = keyboardIssues();
  const { client, hook, key } = renderMoves(open);
  const patch = answerIssuePatches().mockRejectedValue(
    new ApiError(400, { code: "RANK_INPUT", error: "rank neighbors are not ordered" })
  );
  restores.push(() => patch.mockRestore());
  await act(() =>
    hook.result.current.move(
      "CORE-1",
      move({ status: "in_progress", rank: { before: "CORE-4" } }, "")
    )
  );
  expect(patch).toHaveBeenCalledWith(
    "CORE-1",
    { status: "in_progress", rank: { before: "CORE-4" } },
    keyedAttempt
  );
  expect(hook.result.current.error).toBe(staleBoardMessage);
  expect(client.getQueryData(key)).toEqual(open);
  expect(client.getQueryState(key)?.isInvalidated).toBe(true);
});

test("any other failed move shows the server's message and reads the listing again", async () => {
  const { client, hook, key } = renderMoves(keyboardIssues());
  const patch = answerIssuePatches().mockRejectedValue(
    new ApiError(403, { code: "FORBIDDEN", error: "not your project" })
  );
  restores.push(() => patch.mockRestore());
  await act(() => hook.result.current.move("CORE-1", move({ status: "backlog" })));
  expect(hook.result.current.error).toBe("not your project");
  expect(client.getQueryState(key)?.isInvalidated).toBe(true);
});

// Every listing a view reads that holds the card takes the move at once, its other issues kept
// as they were, and two moves on one Board go out one after the other.
test("a move writes every listing that holds the card at once, and two moves go out one after the other", async () => {
  const { client, hook, key } = renderMoves(keyboardIssues());
  const doneKey = ["issues", "project", "CORE", "done", "", ""] as const;
  const closed = [issue("CORE-7", { status: "done" })];
  client.setQueryData(doneKey, closed);
  const settle: ((issue: Issue) => void)[] = [];
  const patch = answerIssuePatches().mockImplementation(() => {
    const { promise, resolve } = Promise.withResolvers<Issue>();
    settle.push(resolve);
    return promise;
  });
  restores.push(() => patch.mockRestore());
  const before = client.getQueryData<IssueSummary[]>(key) ?? [];
  let first: Promise<void> = Promise.resolve();
  let second: Promise<void> = Promise.resolve();
  act(() => {
    first = hook.result.current.move(
      "CORE-1",
      move({ status: "in_progress", rank: { before: "CORE-4" } }, "")
    );
    second = hook.result.current.move("CORE-7", move({ status: "todo" }));
  });
  const after = client.getQueryData<IssueSummary[]>(key) ?? [];
  expect(after.find((row) => row.key === "CORE-1")).toMatchObject({
    status: "in_progress",
    rank: "",
  });
  // An issue the move left alone keeps its object.
  expect(after[1]).toBe(before[1] as IssueSummary);
  expect(client.getQueryData<IssueSummary[]>(doneKey)?.[0]?.status).toBe("todo");
  await waitFor(() => expect(patch).toHaveBeenCalledTimes(1));
  await act(async () => {
    settle[0]?.(saved("CORE-1"));
    await first;
  });
  await waitFor(() => expect(patch).toHaveBeenCalledTimes(2));
  expect(patch.mock.calls[1]?.[0]).toBe("CORE-7");
  await act(async () => {
    settle[1]?.(saved("CORE-7"));
    await second;
  });
  expect(hook.result.current.error).toBeUndefined();
});

// The control row above the Board: the view's search and the Filters panel, with the Labels
// picker and the three flags, and a chip for every narrowing that differs from the view's own.
test("every narrowing that differs from the view's own is a chip, and Clear filters takes them all off", async () => {
  const user = userEvent.setup();
  board(
    [
      issue("CORE-1", { labels: ["frontend"], title: "core work", priority: 0 }),
      issue("CORE-2", { labels: ["frontend"], title: "other work", priority: 0 }),
      issue("CORE-3", { title: "core work", priority: 1 }),
    ],
    "?layout=board&label=frontend&q=core&status=todo&f.priority=0&needs-you=1"
  );
  await boardRegion();
  expect(screen.getByRole("button", { name: "Filters · 5" })).toBeDefined();
  for (const chip of [
    "Label: frontend",
    "Search: core",
    "Needs you",
    "Status: Todo",
    "Priority: P0",
  ])
    expect(screen.getByRole("button", { name: `Remove ${chip} filter` })).toBeDefined();
  await user.click(screen.getByRole("button", { name: "Remove Priority: P0 filter" }));
  await waitFor(() => expect(routeSearch().has("f.priority")).toBe(false));
  expect(screen.getByRole("button", { name: "Filters · 4" })).toBeDefined();
  await user.click(screen.getByRole("button", { name: "Clear filters" }));
  await waitFor(() => expect(routeSearch().toString()).toBe("layout=board"));
  expect(screen.getByRole("button", { name: "Filters · 0" })).toBeDefined();
  expect(screen.queryByRole("button", { name: "Clear filters" })).toBeNull();
});

test("the Labels picker writes label=, shows its chip, and the cards are the view's rows of that address", async () => {
  const user = userEvent.setup();
  const { listing } = board(
    [
      issue("CORE-1", { labels: ["frontend", "api"], title: "Navigation" }),
      issue("CORE-2", { labels: ["docs"], title: "Guidance" }),
    ],
    "?layout=board"
  );
  const region = await boardRegion();
  await waitFor(() => expect(cardKeys(region)).toEqual(["CORE-1", "CORE-2"]));
  await user.click(screen.getByRole("button", { name: "Filters · 0" }));
  await user.click(screen.getByRole("button", { name: "Labels" }));
  await user.click(screen.getByRole("option", { name: "docs" }));
  await waitFor(() => expect(routeSearch().getAll("label")).toEqual(["docs"]));
  await waitFor(() => expect(cardKeys(region)).toEqual(["CORE-2"]));
  expect(screen.getByRole("button", { name: "Remove Label: docs filter" })).toBeDefined();
  // The view narrows the listing it read: no read names the label.
  expectNoFullListing(listing);
});

test("the search box is the view's search, and a search the address brings is a chip", async () => {
  const user = userEvent.setup();
  board(
    [issue("CORE-1", { title: "Guidance" }), issue("CORE-2", { title: "Navigation" })],
    "?layout=board&q=guid"
  );
  const region = await boardRegion();
  await waitFor(() => expect(cardKeys(region)).toEqual(["CORE-1"]));
  expect(screen.getByRole("button", { name: "Remove Search: guid filter" })).toBeDefined();
  const search = screen.getByRole("searchbox", { name: "Search issues" });
  expect(search).toHaveValue("guid");
  await user.clear(search);
  await waitFor(() => expect(cardKeys(region)).toEqual(["CORE-1", "CORE-2"]));
  expect(routeSearch().has("q")).toBe(false);
});

const PANEL_CHOICE = userPreferenceStorageKey("alice", "project.issue-filters");

// The stored choice opens or shuts the Filters panel as the Board loads and takes no focus, and
// only the reader's own open or close writes it.
test("a Filters panel the stored choice opens takes no focus, and only the reader's open and close write it", async () => {
  const user = userEvent.setup();
  const storage = installCountingStorage();
  restores.push(() => storage.restore());
  board([issue("CORE-1", { labels: ["bug"], title: "Core work" })], "?layout=board&label=bug");
  const region = await boardRegion();
  await waitFor(() => expect(cardKeys(region)).toEqual(["CORE-1"]));
  const trigger = screen.getByRole("button", { name: "Filters · 1" });
  // Nothing is stored and a narrowing is on: the panel stands open, with no focus in it.
  expect(trigger).toHaveAttribute("aria-expanded", "true");
  expect(document.activeElement).toBe(document.body);
  expect(storage.writes.filter(([key]) => key === PANEL_CHOICE)).toEqual([]);
  await user.click(trigger);
  await user.click(trigger);
  expect(
    within(screen.getByRole("dialog", { name: "Filters" })).getByRole("button", {
      name: "Labels · 1",
    })
  ).toHaveFocus();
  await user.keyboard("{Escape}");
  expect(trigger).toHaveAttribute("aria-expanded", "false");
  expect(storage.writes.filter(([key]) => key === PANEL_CHOICE)).toEqual([
    [PANEL_CHOICE, "collapsed"],
    [PANEL_CHOICE, "expanded"],
    [PANEL_CHOICE, "collapsed"],
  ]);
});

/** Open's rows for the redraw rows: `count` cards, a third of them In progress. */
const manyRows = (count: number) =>
  Array.from({ length: count }, (_, index) =>
    issue(`CORE-${1000 + index}`, {
      rank: String(1000 + index),
      status: index % 3 === 0 ? "in_progress" : "todo",
    })
  );

/** Counts the card bodies and card shells a change draws: every card body draws
 *  `UnreachableRouteMarker` once, and every card shell calls `useDraggable` once, and on the
 *  Board nothing else does either. */
function countCards() {
  const bodies = spyOn(routeReach, "UnreachableRouteMarker");
  const shells = spyOn(dndCore, "useDraggable");
  restores.push(() => {
    bodies.mockRestore();
    shells.mockRestore();
  });
  return {
    clear() {
      bodies.mockClear();
      shells.mockClear();
    },
    bodies: () => bodies.mock.calls.length,
    shells: () => shells.mock.calls.length,
  };
}

// R11: a live change to one issue draws that issue's card, not the Board's others.
test("a live read that moves one card of 120 draws that card alone", async () => {
  const rows = manyRows(120);
  const { client } = board(rows, "?layout=board");
  const region = await boardRegion();
  await waitFor(() => expect(cardKeys(region)).toHaveLength(120));
  const counts = countCards();
  counts.clear();
  const index = rows.findIndex((row) => row.status === "todo");
  const key = rows[index]?.key ?? "";
  rows[index] = {
    ...(rows[index] as IssueSummary),
    status: "in_progress",
    updated_at: "2026-11-17T09:00:00Z",
  };
  await act(async () => {
    await client.invalidateQueries({ queryKey: ["issues"] });
  });
  await waitFor(() =>
    expect(
      region.querySelector(`[data-board-column="in_progress"] [data-board-card="${key}"]`)
    ).not.toBeNull()
  );
  // Whatever the change still draws after the card lands is counted too.
  await waitMs(100);
  expect(counts.bodies()).toBeGreaterThan(0);
  expect(counts.bodies()).toBeLessThanOrEqual(2);
  expect(counts.shells()).toBeLessThanOrEqual(2);
});

// A card compares the Time in Legion phase text it draws, not the clock: a draw in a new minute
// draws again only the cards whose time reads differently.
test("a draw in a new minute draws again only the cards whose Time in Legion phase text changed", async () => {
  const start = new Date("2026-11-17T12:00:30Z");
  setSystemTime(start);
  restores.push(() => setSystemTime());
  const sockets = liveSockets();
  const rows = manyRows(30);
  const minutes = (count: number) => new Date(start.getTime() - count * 60_000).toISOString();
  board(rows, "?layout=board", DAEMON);
  const cards = rows.map((row, index) =>
    // One card counts minutes; every other card has been in its phase for days, which reads the
    // same a minute on.
    legionCard(row.key, { phase_since: index === 1 ? minutes(30) : minutes(3 * 24 * 60 + 7) })
  );
  await answerBoard(sockets, legionBoard({ implementing: cards }));
  const region = await boardRegion();
  await waitFor(() => expect(cardKeys(region)).toHaveLength(30));
  const ticking = rows[1]?.key ?? "";
  expect(
    region.querySelector(`[data-board-card="${ticking}"] [data-legion-fact=phase-time]`)
      ?.textContent
  ).toBe("Time in Legion phase30 m");
  const counts = countCards();
  counts.clear();
  setSystemTime(new Date(start.getTime() + 60_000));
  // Another reader's change to one other issue draws the Board again.
  const changed = rows[5]?.key ?? "";
  await answerBoard(
    sockets,
    legionBoard({
      implementing: cards.map((card) =>
        card.key === changed ? { ...card, roles: ["implementer"] } : card
      ),
    })
  );
  await waitFor(() =>
    expect(
      region.querySelector(`[data-board-card="${changed}"] [data-legion-fact=roles]`)
    ).not.toBeNull()
  );
  expect(
    region.querySelector(`[data-board-card="${ticking}"] [data-legion-fact=phase-time]`)
      ?.textContent
  ).toBe("Time in Legion phase31 m");
  expect(counts.bodies()).toBeGreaterThanOrEqual(2);
  expect(counts.bodies()).toBeLessThanOrEqual(4);
});

/** The height of each card in the geometry below, and where a column's first card stands. */
const CARD_PX = 120;
const FIRST_CARD_TOP = 200;

/**
 * The geometry the Board's anchor reads, which the unit DOM has no layout engine for: the
 * columns 288 px wide and 304 px apart, In progress in the middle of the window, each card
 * CARD_PX tall from FIRST_CARD_TOP in its column, all scrolled by `window.scrollY`, which the
 * test and `scrollBy` move; a hit test answers the card drawn at that point. Returns the scroll
 * a reader makes, as the browser reports it.
 */
function boardGeometry(): (top: number) => void {
  const scrollY = Object.getOwnPropertyDescriptor(window, "scrollY");
  const setScrollY = (value: number) =>
    Object.defineProperty(window, "scrollY", { configurable: true, value, writable: true });
  const scrollBy = window.scrollBy;
  window.scrollBy = ((x: number | ScrollToOptions, y?: number) => {
    const top = typeof x === "number" ? y : x.top;
    if (top !== undefined) setScrollY(window.scrollY + top);
  }) as typeof window.scrollBy;
  setScrollY(0);
  const columns = () => [...document.querySelectorAll<HTMLElement>("[data-board-column]")];
  const middle = () =>
    columns().findIndex((column) => column.dataset.boardColumn === "in_progress");
  const leftOf = (index: number) => window.innerWidth / 2 - 144 + (index - middle()) * 304;
  // On `Element.prototype`, which owns the method: bun's `mockRestore` leaves a copy on a
  // prototype that only inherits it.
  const rect = spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (
    this: Element
  ) {
    const column = this.closest<HTMLElement>("[data-board-column]");
    if (this.hasAttribute("data-board-card") && column !== null) {
      const index = [...column.querySelectorAll("[data-board-card]")].indexOf(this);
      return new DOMRect(
        leftOf(columns().indexOf(column)),
        FIRST_CARD_TOP + index * CARD_PX - window.scrollY,
        288,
        CARD_PX
      );
    }
    return new DOMRect(0, -window.scrollY, window.innerWidth, 3000);
  });
  const point = spyOn(document, "elementFromPoint").mockImplementation((x, y) => {
    const at = middle() + Math.floor((x - leftOf(middle())) / 304);
    const column = columns()[at];
    if (column === undefined || x - leftOf(at) > 288) return null;
    const index = Math.floor((y + window.scrollY - FIRST_CARD_TOP) / CARD_PX);
    return index < 0
      ? null
      : (column.querySelectorAll<HTMLElement>("[data-board-card]")[index] ?? null);
  });
  restores.push(() => {
    rect.mockRestore();
    point.mockRestore();
    window.scrollBy = scrollBy;
    if (scrollY === undefined) Reflect.deleteProperty(window, "scrollY");
    else Object.defineProperty(window, "scrollY", scrollY);
  });
  return (top) => {
    setScrollY(top);
    document.dispatchEvent(new Event("scroll"));
  };
}

/** A card's top on screen, by its key. */
const cardTop = (key: string) =>
  document.querySelector(`[data-board-card="${key}"]`)?.getBoundingClientRect().top;

/** The keys of a column's cards, in its order. */
const columnCards = (column: string) =>
  cardKeys(document.querySelector<HTMLElement>(`[data-board-column="${column}"]`) as HTMLElement);

/** Waits until the frame the Board's last draw went into is painted. */
const painted = () => waitMs(50);

// R11: the Board keeps the reader's place as a table does. A card another reader adds above the
// card the reader's hand is on does not move that card on screen: the window scrolls by the added
// card instead. A card that itself leaves its column is not followed, and the Board's scroll boxes
// leave the window to the anchor alone.
test("a card added above the card in hand keeps its top, and a card that leaves its column is not followed", async () => {
  boardGeometry();
  const rows = [issue("CORE-2"), issue("CORE-3")];
  const { client } = board(rows, "?layout=board");
  const region = await boardRegion();
  const reading = await within(region).findByRole("article", { name: "CORE-3 Work CORE-3" });
  act(() => reading.focus());
  const before = cardTop("CORE-3");
  expect(before).toBe(FIRST_CARD_TOP + CARD_PX);

  rows.unshift(issue("CORE-1"));
  await act(() => client.invalidateQueries({ queryKey: ["issues"] }));
  await waitFor(() => expect(columnCards("todo")).toEqual(["CORE-1", "CORE-2", "CORE-3"]));
  expect(cardTop("CORE-3")).toBe(before);
  expect(window.scrollY).toBe(CARD_PX);

  rows[2] = issue("CORE-3", { status: "backlog" });
  await act(() => client.invalidateQueries({ queryKey: ["issues"] }));
  await waitFor(() => expect(columnCards("backlog")).toEqual(["CORE-3"]));
  expect(window.scrollY).toBe(CARD_PX);
  for (const box of region.querySelectorAll('[data-testid="board-scroll-container"]'))
    expect(box.className).toContain("[overflow-anchor:none]");
});

// R11: with no hand on a card and the window at its top, the anchor holds the first card of the
// column nearest the middle once the reader has seen it: a card added above it in that column
// stands above it, the window scrolling by its height, and the counts follow.
test("a card added above the first card of the middle column keeps that card's top at the top", async () => {
  boardGeometry();
  const rows = [
    issue("CORE-2"),
    issue("CORE-5", { status: "in_progress" }),
    issue("CORE-6", { status: "in_progress" }),
    issue("CORE-8", { status: "needs_review" }),
  ];
  const { client } = board(rows, "?layout=board");
  await within(await boardRegion()).findByRole("article", { name: "CORE-5 Work CORE-5" });
  await painted();
  expect(cardTop("CORE-5")).toBe(FIRST_CARD_TOP);

  rows.unshift(issue("CORE-4", { status: "in_progress" }));
  await act(() => client.invalidateQueries({ queryKey: ["issues"] }));
  await waitFor(() => expect(columnCards("in_progress")).toEqual(["CORE-4", "CORE-5", "CORE-6"]));
  expect(cardTop("CORE-5")).toBe(FIRST_CARD_TOP);
  expect(window.scrollY).toBe(CARD_PX);
  expect(screen.getByRole("tab", { name: "Open, 5 issues" })).toBeDefined();
});

// R11: the Board draws nothing when the window scrolls, so a reader who scrolled down, saw a
// change land there and scrolled back to the top is held by the record of their own scroll: the
// middle column's first card keeps its top when a card is added above it.
test("after a change while scrolled and a scroll back to the top, the middle column's first card keeps its top", async () => {
  const scrollTo = boardGeometry();
  const rows = [
    issue("CORE-5", { status: "in_progress" }),
    issue("CORE-6", { status: "in_progress" }),
    issue("CORE-7", { status: "in_progress" }),
  ];
  const { client } = board(rows, "?layout=board");
  await within(await boardRegion()).findByRole("article", { name: "CORE-5 Work CORE-5" });
  await painted();
  scrollTo(2 * CARD_PX + FIRST_CARD_TOP);
  rows[2] = issue("CORE-7", { status: "in_progress", title: "Work CORE-7, reworded" });
  await act(() => client.invalidateQueries({ queryKey: ["issues"] }));
  await screen.findByRole("article", { name: "CORE-7 Work CORE-7, reworded" });
  await painted();
  scrollTo(0);
  await painted();
  expect(cardTop("CORE-5")).toBe(FIRST_CARD_TOP);

  rows.unshift(issue("CORE-4", { status: "in_progress" }));
  await act(() => client.invalidateQueries({ queryKey: ["issues"] }));
  await waitFor(() => expect(columnCards("in_progress")).toHaveLength(4));
  expect(cardTop("CORE-5")).toBe(FIRST_CARD_TOP);
  expect(window.scrollY).toBe(CARD_PX);
});

// R11: the Board's anchor is off while a card is dragged, until the drop is drawn: the dragged
// card moves under the reader's hand, and a change landing then is not held against it.
test("a card added while another is dragged is not held, and the anchor holds again after the drop", async () => {
  boardGeometry();
  patchAnswered();
  const rows = [
    issue("CORE-5", { status: "in_progress" }),
    issue("CORE-6", { status: "in_progress" }),
  ];
  const { client } = board(rows, "?layout=board");
  const dragged = await within(await boardRegion()).findByRole("article", {
    name: "CORE-6 Work CORE-6",
  });
  await painted();
  await act(async () => {
    fireEvent.mouseDown(dragged, { button: 0, clientX: 10, clientY: 10 });
  });
  await act(async () => {
    fireEvent.mouseMove(document, { clientX: 10, clientY: 40 });
  });
  expect(dragged.className).toContain("opacity-50");

  rows.unshift(issue("CORE-4", { status: "in_progress" }));
  await act(() => client.invalidateQueries({ queryKey: ["issues"] }));
  await waitFor(() => expect(columnCards("in_progress")).toHaveLength(3));
  expect(window.scrollY).toBe(0);

  await act(async () => {
    fireEvent.keyDown(document, { code: "Escape", key: "Escape" });
  });
  await act(async () => {
    fireEvent.mouseUp(document, { clientX: 10, clientY: 40 });
  });
  await painted();
  expect(cardTop("CORE-4")).toBe(FIRST_CARD_TOP);
  rows.unshift(issue("CORE-3", { status: "in_progress" }));
  await act(() => client.invalidateQueries({ queryKey: ["issues"] }));
  await waitFor(() => expect(columnCards("in_progress")).toHaveLength(4));
  expect(cardTop("CORE-4")).toBe(FIRST_CARD_TOP);
  expect(window.scrollY).toBe(CARD_PX);
});
