import { afterEach, expect, jest, setSystemTime, spyOn, test } from "bun:test";
import * as dndCore from "@dnd-kit/core";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
// biome-ignore lint/style/noRestrictedImports: a column's drag and drop, which user-event has no form for.
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import type { ReactNode } from "react";
import { MemoryRouter, useLocation } from "react-router-dom";
import { realClock, tick } from "../../__tests__/fake-clock";
import { afterRescue, RescuedMain } from "../../__tests__/focus-rescue";
import {
  errorFrame,
  type FakeLiveSockets,
  installFakeLiveSockets,
  snapshotFrame,
} from "../../__tests__/live-socket-harness";
import { installResizeObserver } from "../../__tests__/scroll-box";
import { ApiError, api } from "../../api/client";
import { boardMoveMutationKey, legionBoardQuery, projectIssuesQuery } from "../../api/queries";
import type {
  EvalItemRow,
  Issue,
  IssueSummary,
  LegionBoardCard,
  LegionBoardFacts,
  LegionBoardResponse,
  LegionSetting,
} from "../../api/types";
import { SORT_MARK_WIDTH } from "../../components/table-layout";
import { readLayout } from "../../components/table-storage";
import * as routeReach from "../issue/RouteReach";
import { readSelectedIssues } from "../search/bar-scope";
import { KeymapProvider } from "../shell/KeymapProvider";
import { appKeymap, useKeymapScope } from "../shell/keymap";
import { writesOf } from "../writes/write-store";
import { IssuesTable } from "./IssuesTable";
import { CLOSE_ITEMS_AGAIN_MS, type IssueListing } from "./issues-table-data";
import { ALL_ROWS, ISSUE_TABLE_COLUMNS, tableStorageKey } from "./issues-table-model";

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
/** An issue as a Dispatch process of the release before the one that added `created_by` and
 *  `created_at` answers it: neither member is there. While a release rolls out, a page of this
 *  build reads such a row ("Two builds at once", `packages/envoy/cmd/dispatch/README.md`). */
function issueFromOlderServer(key: string, overrides: Partial<IssueSummary> = {}): IssueSummary {
  const row: Partial<IssueSummary> = issue(key, overrides);
  delete row.created_at;
  delete row.created_by;
  return row as IssueSummary;
}
function savedIssue(key: string): Issue {
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
/** Draws the List over `issues`. `full` answers the listing of the closed issues, and `open` the
 *  one that holds the open ones alone, in place of `issues` itself; `rescued` draws the List in
 *  the shell's main content with the page's focus rescue mounted (`RescuedMain`), after a link
 *  that stands for the shell's navigation; `props` is what a caller other than the project page
 *  passes: a listing of its own, the priorities' titles and the prefix its view is written
 *  under. `daemon` is the project's Legion daemon setting, none by default. */
function table(
  issues: IssueSummary[],
  search = "",
  {
    full,
    open,
    rescued = false,
  }: { full?: Promise<IssueSummary[]>; open?: Promise<IssueSummary[]>; rescued?: boolean } = {},
  props: {
    listing?: IssueListing;
    priorityTitles?: ReadonlyMap<string, string>;
    stateKey?: string;
  } = {},
  daemon: Promise<LegionSetting | null> = Promise.resolve(null)
) {
  // The listing route as the server answers it: `open` leaves the closed rows out, `status`
  // keeps the rows in those statuses, and `advances` keeps the issues that carry that relation,
  // whichever project they are in.
  const answers =
    (options: Parameters<typeof api.listIssues>[0] = {}) =>
    (issue: IssueSummary) =>
      (!options.open || issue.status !== "done") &&
      (options.status === undefined || options.status.includes(issue.status)) &&
      (options.advances === undefined ||
        (issue.advances ?? []).some((relation) => options.advances?.includes(relation.id)));
  const listing = spyOn(api, "listIssues").mockImplementation(async (options = {}) => {
    if (!options.open && full) return (await full).filter(answers(options));
    if (options.open && open) return open;
    return issues.filter(answers(options));
  });
  // One page of it, as `limit` and `offset` ask, newest close first for `sort=-closed_at`.
  const page = spyOn(api, "listIssuePage").mockImplementation(async (options) => {
    const rows = issues.filter(answers(options));
    if (options.sort === "-closed_at")
      rows.sort((a, b) => (b.closed_at ?? "").localeCompare(a.closed_at ?? ""));
    const offset = options.offset ?? 0;
    const limit = options.limit ?? rows.length;
    return { issues: rows.slice(offset, offset + limit), limit, offset, total: rows.length };
  });
  const setting = spyOn(api, "getLegionDaemon").mockImplementation(() => daemon);
  const state = spyOn(api, "getMyState").mockResolvedValue({});
  const agents = spyOn(api, "listAgents").mockResolvedValue([]);
  const viewer = spyOn(api, "whoAmI").mockResolvedValue({
    event_head: "0",
    login: "alice",
    kind: "user",
  });
  // The unit DOM has no layout engine. Supply only the browser's viewport measurements,
  // leaving filtering, rendering, focus and navigation to the actual table.
  const descriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientHeight");
  Object.defineProperty(HTMLElement.prototype, "clientHeight", {
    configurable: true,
    get() {
      return this.dataset.testid === "issues-table-scroll"
        ? 600
        : (descriptor?.get?.call(this) ?? 0);
    },
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  client.setQueryData(["whoami"], { event_head: "0", login: "alice", kind: "user" });
  const list = <IssuesTable listing={{ project: "CORE" }} {...props} />;
  const view = render(
    <MemoryRouter initialEntries={[`/projects/CORE/issues${search}`]}>
      <QueryClientProvider client={client}>
        <KeymapProvider>
          <Route />
          {rescued ? (
            <>
              <a href="/">Inbox</a>
              <RescuedMain>{list}</RescuedMain>
            </>
          ) : (
            list
          )}
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
    if (descriptor) Object.defineProperty(HTMLElement.prototype, "clientHeight", descriptor);
    else Reflect.deleteProperty(HTMLElement.prototype, "clientHeight");
  });
  return { client, listing, page, setting, view };
}

/** Picks a grouping in the row's display menu, which is where **Group by** lives and which the
 *  Issues tab names Group by: the menu is opened if it is shut, and a choice leaves it open for
 *  the next one. */
async function groupRowsBy(user: UserEvent, option: string) {
  const menu = screen.getByRole("button", { name: "Group by" });
  if (menu.getAttribute("aria-expanded") === "false") await user.click(menu);
  await user.click(screen.getByRole("radio", { name: option }));
}

test("rows keep parent links, unread marks, asks, progress and route reach, with priority editable", async () => {
  table([
    issue("CORE-1", {
      parent: "CORE-9",
      open_asks: 2,
      progress: { tasks: { done: 3, total: 7 }, children: { done: 1, total: 2 } },
      route: "role:sre",
      route_status: "no_holder",
    }),
  ]);
  const row = await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  expect(within(row).getByRole("link", { name: "CORE-9" }).getAttribute("href")).toBe(
    "/issues/CORE-9"
  );
  expect(within(row).getByTestId("issue-progress-tasks").textContent).toBe("3/7 tasks");
  expect(within(row).getByTestId("issue-progress-children").textContent).toBe("1/2 children");
  expect(within(row).getAllByText("2", { exact: true }).length).toBeGreaterThan(0);
  expect(within(row).getByTestId("issue-route-unreachable").textContent).toContain(
    "Nobody holds it right now"
  );
  expect(within(row).getByTitle("Unread")).toBeDefined();
  expect(within(row).getByRole("combobox", { name: "Priority of CORE-1" })).toBeDefined();
});

test("j and k focus table issues and o and Enter open the issue rather than its parent", async () => {
  const user = userEvent.setup();
  table([issue("CORE-1", { parent: "CORE-9" }), issue("CORE-2")]);
  const first = await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  const second = screen.getByRole("row", { name: "CORE-2 Work CORE-2" });
  await user.keyboard("j");
  expect(first).toHaveFocus();
  await user.keyboard("j");
  expect(second).toHaveFocus();
  await user.keyboard("k");
  expect(first).toHaveFocus();
  await user.keyboard("o");
  await waitFor(() => expect(screen.getByTestId("route").textContent).toBe("/issues/CORE-1"));
  second.focus();
  await user.keyboard("{Enter}");
  await waitFor(() => expect(screen.getByTestId("route").textContent).toBe("/issues/CORE-2"));
});

test("typed title and every picked label match case-insensitively, as on Board", async () => {
  table(
    [
      issue("CORE-1", { title: "Search both", labels: ["FrontEnd", "BACKEND"] }),
      issue("CORE-2", { title: "Search backend", labels: ["backend"] }),
      issue("CORE-3", { title: "Other", labels: ["frontend", "backend"] }),
    ],
    "?label=frontend&label=backend&f.title=SEARCH"
  );
  await screen.findByRole("row", { name: "CORE-1 Search both" });
  expect(screen.queryByRole("row", { name: "CORE-2 Search backend" })).toBeNull();
  expect(screen.queryByRole("row", { name: "CORE-3 Other" })).toBeNull();
});

test("opening a Labels facet does not pass its values through another column's formatter", async () => {
  const user = userEvent.setup();
  table([issue("CORE-1", { labels: ["frontend"] })]);
  const trigger = await screen.findByRole("button", { name: "Filter Labels" });
  await user.click(trigger);
  expect(await screen.findByRole("combobox", { name: "Search Labels" })).toBeDefined();
  expect(screen.getByRole("option", { name: "frontend, 1 issue" })).toBeDefined();
});

test("Created sorts RFC3339 fractional timestamps by instant, not by text", async () => {
  table(
    [
      issue("CORE-1", { created_at: "2026-09-10T00:00:00.1Z" }),
      issue("CORE-2", { created_at: "2026-09-10T00:00:00Z" }),
    ],
    "?sort=created"
  );
  await screen.findByRole("row", { name: "CORE-2 Work CORE-2" });
  expect(
    screen
      .getAllByRole("row")
      .filter((row) => row.hasAttribute("data-issue-row"))
      .map((row) => row.getAttribute("data-issue-row"))
  ).toEqual(["CORE-2", "CORE-1"]);
});

test("a table rank write is held by the durable store on the issue's write line", async () => {
  const user = userEvent.setup();
  const response = Promise.withResolvers<Response>();
  const send = spyOn(globalThis, "fetch").mockReturnValue(response.promise);
  const { client } = table([
    issue("CORE-1", { labels: ["legion"] }),
    issue("CORE-2", { labels: ["legion"] }),
  ]);
  restores.push(() => send.mockRestore());
  await user.click(await screen.findByRole("checkbox", { name: "Select CORE-2" }));
  await user.click(within(screen.getByRole("toolbar")).getByRole("button", { name: "Start next" }));
  await waitFor(() => expect(writesOf(client).rows().length).toBe(1));
  expect(writesOf(client).rows()[0]?.target).toBe("issue:CORE-2");
  expect(writesOf(client).rows()[0]?.login).toBe("alice");
  response.resolve(
    new Response(JSON.stringify(savedIssue("CORE-2")), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    })
  );
  await waitFor(() =>
    expect(
      within(screen.getByRole("region", { name: "Project issues" })).getByRole("status").textContent
    ).toContain("now starts next")
  );
});

test("dismissing an interrupted action preserves layout and sends none of its future steps", async () => {
  const user = userEvent.setup();
  localStorage.setItem(
    "dispatch.project-table.CORE",
    JSON.stringify({
      sizing: { title: 360 },
      action: {
        id: "unfinished",
        kind: "startNext",
        project: "CORE",
        keys: ["CORE-1"],
        writes: {},
      },
    })
  );
  const send = spyOn(api, "send").mockResolvedValue(savedIssue("CORE-1") as never);
  restores.push(() => send.mockRestore());
  table([issue("CORE-1")]);
  const recovery = await screen.findByRole("region", { name: "Interrupted table action" });
  await user.click(within(recovery).getByRole("button", { name: "Dismiss" }));
  expect(screen.queryByRole("region", { name: "Interrupted table action" })).toBeNull();
  const saved = JSON.parse(localStorage.getItem("dispatch.project-table.CORE") ?? "{}");
  expect(saved.action).toBeUndefined();
  expect(saved.sizing.title).toBe(360);
  expect(send).not.toHaveBeenCalled();
});

test("the open table read waits behind an unanswered Board move", async () => {
  const { client, listing } = table([issue("CORE-1")]);
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  const gate = Promise.withResolvers<void>();
  const started = Promise.withResolvers<void>();
  const move = client
    .getMutationCache()
    .build(client, {
      mutationKey: boardMoveMutationKey("CORE"),
      mutationFn: () => {
        started.resolve();
        return gate.promise;
      },
    })
    .execute(undefined);
  await started.promise;
  const calls = listing.mock.calls.length;
  const reading = client.fetchQuery(projectIssuesQuery("CORE", [], true));
  await Promise.resolve();
  expect(listing.mock.calls.length).toBe(calls);
  gate.resolve();
  await move;
  await reading;
  expect(listing.mock.calls.length).toBe(calls + 1);
});

test("a failed project listing can be retried without navigating away", async () => {
  const user = userEvent.setup();
  const { client, listing } = table([issue("CORE-1")]);
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  listing.mockRejectedValueOnce(new Error("unreachable"));
  await act(async () => {
    await client.invalidateQueries({ queryKey: ["issues"] });
  });
  await screen.findByRole("alert");
  await user.click(screen.getByRole("button", { name: "Retry loading issues" }));
  expect(await screen.findByRole("row", { name: "CORE-1 Work CORE-1" })).toBeDefined();
});

// Every panel the table hangs off a control - a header's filter, Columns - and the shared
// MultiSelect dismiss by one set of rules: a press outside, or Escape, which puts focus back on
// the control that opened it.
test("Title and Columns popovers allow internal Tab, dismiss on an outside press and on Escape", async () => {
  const user = userEvent.setup();
  table([issue("CORE-1")]);
  const title = await screen.findByRole("button", { name: "Filter Title" });
  await user.click(title);
  const dialog = screen.getByRole("dialog", { name: "Filter Title" });
  expect(screen.getByTestId("issues-table-scroll").contains(dialog)).toBe(false);
  await user.keyboard("{Escape}");
  expect(screen.queryByRole("dialog")).toBeNull();
  expect(document.activeElement).toBe(title);
  const columns = screen.getByRole("button", { name: "Columns" });
  await user.click(columns);
  expect(screen.getByRole("dialog", { name: "Columns" })).toBeDefined();
  await user.tab();
  expect(screen.getByRole("dialog", { name: "Columns" })).toBeDefined();
  const controls = within(screen.getByRole("dialog", { name: "Columns" })).getAllByRole("checkbox");
  controls[1]?.focus();
  expect(screen.getByRole("dialog", { name: "Columns" })).toBeDefined();
  await user.click(screen.getByRole("searchbox", { name: "Search issues" }));
  await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
});

test("Clear filters also clears Needs you, Unread and the lapsed-claim filter", async () => {
  const user = userEvent.setup();
  table([issue("CORE-1", { open_asks: 2 })], "?needs-you=1&unread=1&unclaimed=1");
  await user.click(await screen.findByRole("button", { name: "Clear filters" }));
  expect(
    screen.getByRole("button", { name: /^Filter Claimed by/ }).getAttribute("aria-label")
  ).toBe("Filter Claimed by");
  // The flags are behind the row's one filter trigger now, so the reader opens it to read them.
  await user.click(screen.getByRole("button", { name: /^Filters · \d+$/ }));
  for (const name of ["Needs you", "Unread", "Unclaimed"]) {
    await waitFor(() =>
      expect(screen.getByRole("button", { name }).getAttribute("aria-pressed")).toBe("false")
    );
  }
});

test("the Done facet does not claim an unloaded count is zero", async () => {
  const user = userEvent.setup();
  table([issue("CORE-1"), issue("CORE-2", { status: "done" })]);
  await user.click(await screen.findByRole("button", { name: /^Filter Status/ }));
  expect(screen.getByRole("option", { name: "Done, not loaded" })).toBeDefined();
});

test("loading closed issues retains the open rows and controls, including after a refusal", async () => {
  const user = userEvent.setup();
  const { listing } = table([issue("CORE-1")]);
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  const closed = Promise.withResolvers<IssueSummary[]>();
  listing.mockReturnValueOnce(closed.promise);
  // Asking the Status filter for Done is what loads the closed issues; Clear filters goes back
  // to the open view, which needs no second listing.
  await user.click(screen.getByRole("button", { name: /^Filter Status/ }));
  await user.click(screen.getByRole("option", { name: /^Done/ }));
  expect(screen.getByRole("table", { name: "Project issues table" })).toBeDefined();
  expect(screen.getByRole("searchbox", { name: "Search issues" })).toBeDefined();
  closed.reject(new Error("closed listing refused"));
  await screen.findByRole("alert");
  expect(screen.getByRole("row", { name: "CORE-1 Work CORE-1" })).toBeDefined();
  expect(screen.getByRole("button", { name: "Retry loading closed issues" })).toBeDefined();
});

// The rows' cells are drawn by the column definitions, and a new definition is a new component
// to React: whether the closed issues are loaded is the table's, not a column's, so their arrival
// keeps every cell already drawn, and with it whatever a reader left focused there.
test("the closed issues arriving keeps the cells already drawn", async () => {
  const user = userEvent.setup();
  table([issue("CORE-1"), issue("CORE-2", { status: "done" })]);
  const select = await screen.findByRole("checkbox", { name: "Select CORE-1" });
  await user.click(screen.getByRole("button", { name: /^Filter Status/ }));
  await user.click(screen.getByRole("option", { name: /^Done/ }));
  await screen.findByRole("row", { name: "CORE-2 Work CORE-2" });
  expect(screen.getByRole("checkbox", { name: "Select CORE-1" })).toBe(select);
});

test("a late Done listing applies the requested URL page instead of clamping it early", async () => {
  const done = Promise.withResolvers<IssueSummary[]>();
  table([issue("CORE-1")], "?status=done&page=3&size=25", { full: done.promise });
  // Status Done alone reads no open row, so the table waits for the closed issues.
  await screen.findByRole("status", { name: "Loading issues" });
  done.resolve(
    Array.from({ length: 80 }, (_, index) => issue(`CORE-${index + 2}`, { status: "done" }))
  );
  // Page 3 of the 80 closed issues, 25 rows of them, rather than page 1.
  expect(await screen.findByText("Page 3 of 4")).toBeDefined();
  expect(listedKeys()).toHaveLength(25);
});

test("created-by sessions use their live name and state, and a null creator is Unknown", async () => {
  table([
    issue("CORE-1", {
      created_by: {
        kind: "session",
        id: "table-creator",
        origin: { session_title: "Table author" },
      },
    }),
    issue("CORE-2", { created_by: null }),
  ]);
  const row = await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  expect(
    within(row)
      .getByRole("link", { name: /Table author/ })
      .getAttribute("href")
  ).toContain("table-creator");
  expect(within(row).getByRole("img", { name: /running/i })).toBeDefined();
  expect(
    within(screen.getByRole("row", { name: "CORE-2 Work CORE-2" })).getByText("Unknown")
  ).toBeDefined();
});

/** The rows below give the issue a server sent without the two members the lower key, so neither
 *  the key order nor the order they are listed in can stand in for the ordering under test. */
test("a row a server sends without created_by or created_at renders, with no author and no time", async () => {
  table([issue("CORE-2"), issueFromOlderServer("CORE-1")], "?sort=created");
  const row = await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  expect(within(row).getByRole("link", { name: "CORE-1" }).getAttribute("href")).toContain(
    "CORE-1"
  );
  expect(within(row).getByText("Unknown")).toBeDefined();
  expect(row.querySelector("time")).toBeNull();
  expect(
    screen.getByRole("row", { name: "CORE-2 Work CORE-2" }).querySelector("time")
  ).not.toBeNull();
  // Created sorts without throwing on the absent time, and keeps that row after the one that
  // carries a time, although its key sorts first.
  expect(
    screen
      .getAllByRole("row")
      .filter((candidate) => candidate.hasAttribute("data-issue-row"))
      .map((candidate) => candidate.getAttribute("data-issue-row"))
  ).toEqual(["CORE-2", "CORE-1"]);
});

test("a row with no created_at stays last when Created is sorted the other way", async () => {
  // A sorting function alone cannot do this: the table orders an absent accessor value before it
  // calls one and then reverses that for a descending sort, so the column says `sortUndefined:
  // "last"`. Two rows without a time keep their own order beside the one that has one.
  table(
    [issue("CORE-3"), issueFromOlderServer("CORE-1"), issueFromOlderServer("CORE-2")],
    "?sort=-created"
  );
  await screen.findByRole("row", { name: "CORE-3 Work CORE-3" });
  expect(
    screen
      .getAllByRole("row")
      .filter((candidate) => candidate.hasAttribute("data-issue-row"))
      .map((candidate) => candidate.getAttribute("data-issue-row"))
  ).toEqual(["CORE-3", "CORE-1", "CORE-2"]);
});

test("the Created filter files a row a server sent with no created_at under Older", async () => {
  // The column's filter asks `createdAt` and files a row without a time under Older
  // (`issues-table-model.ts`), the bucket an issue old enough to come from a server of the
  // release before `created_at` would reach anyway, so a reader who picks that age still sees
  // the row. The row that carries a time is today's, so the filter is doing the choosing.
  table(
    [issue("CORE-1", { created_at: new Date().toISOString() }), issueFromOlderServer("CORE-2")],
    "?f.created=Older"
  );
  expect(await screen.findByRole("row", { name: "CORE-2 Work CORE-2" })).toBeDefined();
  expect(screen.queryByRole("row", { name: "CORE-1 Work CORE-1" })).toBeNull();
});

test("the Legion queue puts an issue with no created_at after one of the same rank that has one", async () => {
  // Queue is no default column of a project with no Legion daemon: the address shows it.
  table(
    [
      issueFromOlderServer("CORE-1", { labels: ["legion"], rank: "m" }),
      issue("CORE-2", { labels: ["legion"], rank: "m" }),
    ],
    "?cols=key,title,queue"
  );
  const older = await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  expect(
    within(screen.getByRole("row", { name: "CORE-2 Work CORE-2" })).getByText("#1")
  ).toBeDefined();
  expect(within(older).getByText("#2")).toBeDefined();
});

test("Start next stops at a refused predecessor and names every issue that did not move up", async () => {
  const user = userEvent.setup();
  const sent: string[] = [];
  const send = spyOn(api, "send").mockImplementation(async (request) => {
    sent.push(request.path);
    throw new ApiError(403, { error: "Rank refused" });
  });
  restores.push(() => send.mockRestore());
  const { client } = table([
    issue("CORE-1", { labels: ["legion"], rank: "A" }),
    issue("CORE-2", { labels: ["legion"], rank: "B" }),
    issue("CORE-3", { labels: ["legion"], rank: "C" }),
  ]);
  await user.click(await screen.findByRole("checkbox", { name: "Select CORE-2" }));
  await user.click(screen.getByRole("checkbox", { name: "Select CORE-3" }));
  await user.click(within(screen.getByRole("toolbar")).getByRole("button", { name: "Start next" }));
  expect((await screen.findByRole("alert")).textContent).toContain(
    "Did not move up: CORE-3, CORE-2"
  );
  expect(sent).toEqual(["/api/v1/issues/CORE-3"]);
  expect(writesOf(client).rows()).toHaveLength(1);
});

test("x selects the focused issue and the bar is told, in the order the table lists them", async () => {
  const user = userEvent.setup();
  table([issue("CORE-1"), issue("CORE-2")]);
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  await user.keyboard("j");
  await user.keyboard("x");
  expect(screen.getByRole<HTMLInputElement>("checkbox", { name: "Select CORE-1" }).checked).toBe(
    true
  );
  expect(readSelectedIssues()).toEqual(["CORE-1"]);
  await user.keyboard("j");
  await user.keyboard("x");
  expect(readSelectedIssues()).toEqual(["CORE-1", "CORE-2"]);
  // The same key on the same row takes the selection back off, as the row's own checkbox does.
  await user.keyboard("x");
  expect(screen.getByRole<HTMLInputElement>("checkbox", { name: "Select CORE-2" }).checked).toBe(
    false
  );
  expect(readSelectedIssues()).toEqual(["CORE-1"]);
});

test("an issue the table stops listing drops out of the selection and out of what the bar acts on", async () => {
  const user = userEvent.setup();
  table([issue("CORE-1", { title: "Sextant" }), issue("CORE-2", { title: "Astrolabe" })]);
  await user.click(await screen.findByRole("checkbox", { name: "Select CORE-1" }));
  await user.click(screen.getByRole("checkbox", { name: "Select CORE-2" }));
  expect(readSelectedIssues()).toEqual(["CORE-1", "CORE-2"]);
  await user.type(screen.getByRole("searchbox", { name: "Search issues" }), "sextant");
  await waitFor(() => expect(readSelectedIssues()).toEqual(["CORE-1"]));
  expect(screen.queryByRole("checkbox", { name: "Select CORE-2" })).toBeNull();
  // Listed again, the row comes back unselected rather than carrying a hidden mark.
  await user.clear(screen.getByRole("searchbox", { name: "Search issues" }));
  const second = await screen.findByRole<HTMLInputElement>("checkbox", { name: "Select CORE-2" });
  expect(second.checked).toBe(false);
  expect(readSelectedIssues()).toEqual(["CORE-1"]);
});

test("leaving the table clears what the bar says it will act on", async () => {
  const user = userEvent.setup();
  const { view } = table([issue("CORE-1")]);
  await user.click(await screen.findByRole("checkbox", { name: "Select CORE-1" }));
  expect(readSelectedIssues()).toEqual(["CORE-1"]);
  view.unmount();
  expect(readSelectedIssues()).toEqual([]);
});

// The Board in the table's place takes the selection with the table, as leaving the page does,
// and the table drawn again comes back with no row selected.
test("the Board in the table's place clears what the bar says it will act on", async () => {
  const user = userEvent.setup();
  table([issue("CORE-1"), issue("CORE-2")]);
  await user.click(await screen.findByRole("checkbox", { name: "Select CORE-1" }));
  await user.click(screen.getByRole("checkbox", { name: "Select CORE-2" }));
  expect(readSelectedIssues()).toEqual(["CORE-1", "CORE-2"]);
  await user.click(screen.getByRole("button", { name: "Board" }));
  await screen.findByRole("region", { name: "Project board" });
  expect(readSelectedIssues()).toEqual([]);
  await user.click(screen.getByRole("button", { name: "Table" }));
  const first = await screen.findByRole<HTMLInputElement>("checkbox", { name: "Select CORE-1" });
  expect(first.checked).toBe(false);
  expect(screen.getByRole<HTMLInputElement>("checkbox", { name: "Select CORE-2" }).checked).toBe(
    false
  );
  expect(readSelectedIssues()).toEqual([]);
});

// The Board's own keys answer while it shows: the shortcut help lists them and none of the table
// it hides, and the table's come back with the table.
test("the Board in the table's place takes the table's keys off the help, and the table brings them back", async () => {
  const user = userEvent.setup();
  table([issue("CORE-1")]);
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  const labels = () => appKeymap.describe().map((entry) => entry.label);
  expect(labels()).toContain("Next issue");
  await user.click(screen.getByRole("button", { name: "Board" }));
  await screen.findByRole("region", { name: "Project board" });
  expect(labels()).toContain("Next card");
  expect(labels()).not.toContain("Next issue");
  expect(labels()).not.toContain("Open the focused issue");
  await user.click(screen.getByRole("button", { name: "Table" }));
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  expect(labels()).toContain("Next issue");
  expect(labels()).not.toContain("Next card");
});

// Open's bar counts the rows the table lists. With closed issues unloaded it shows no Done
// segment at all, rather than a cell that reads as a count; the Status filter's own Done option
// says they load when it is picked.
test("the status bar has no Done cell while closed issues are unloaded", async () => {
  const user = userEvent.setup();
  table([issue("CORE-1")]);
  const bar = await screen.findByRole("region", { name: "Issues by status" });
  expect(within(bar).queryByText(/Done/)).toBeNull();
  await user.click(screen.getByRole("button", { name: /^Filter Status/ }));
  expect(screen.getByRole("option", { name: "Done, not loaded" })).toBeDefined();
});

test("Claimed by picks the issues nobody is working, and ?unclaimed=1 still opens that view", async () => {
  const user = userEvent.setup();
  // A person's claim is never takeable, whatever the agent registry says, so it is held here.
  const held: IssueSummary["claim"] = {
    actor: { kind: "user", id: "bob" },
    at: "2026-09-10T00:00:00Z",
  };
  table([issue("CORE-1", { claim: held }), issue("CORE-2")], "?unclaimed=1");
  await screen.findByRole("row", { name: "CORE-2 Work CORE-2" });
  expect(screen.queryByRole("row", { name: "CORE-1 Work CORE-1" })).toBeNull();
  // And no chip loose in the control row: the flag sits behind the row's filter trigger, and
  // the rule is still the Claimed by column's, picked like any other value.
  expect(screen.queryByRole("button", { name: "Unclaimed" })).toBeNull();
  await user.click(screen.getByRole("button", { name: /^Filter Claimed by/ }));
  const picked = screen.getByRole("option", { name: /^Nobody or lapsed/ });
  expect(picked.getAttribute("aria-selected")).toBe("true");
  await user.click(picked);
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
});

// R3: a narrowing that is no column of the table sits behind the row's one filter trigger.
// Unclaimed is `?unclaimed=1` there, which is the Claimed by column's "Nobody or lapsed".
test("the row's filter panel turns Unclaimed on and the table narrows to it", async () => {
  const user = userEvent.setup();
  const held: IssueSummary["claim"] = {
    actor: { kind: "user", id: "bob" },
    at: "2026-09-10T00:00:00Z",
  };
  table([issue("CORE-1", { claim: held }), issue("CORE-2")]);
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  await user.click(screen.getByRole("button", { name: "Filters · 0" }));
  await user.click(screen.getByRole("button", { name: "Unclaimed" }));
  await waitFor(() =>
    expect(screen.getByTestId("route-search").textContent).toContain("unclaimed=1")
  );
  await waitFor(() => expect(screen.queryByRole("row", { name: "CORE-1 Work CORE-1" })).toBeNull());
  expect(screen.getByRole("row", { name: "CORE-2 Work CORE-2" })).toBeDefined();
  expect(screen.getByRole("button", { name: "Remove Unclaimed filter" })).toBeDefined();
});

// R5, R9: no column, grouping, filter or action is about epics. An address of the build before
// that names the Epic column's filter or its grouping lists every row, with no heading.
test("no column, grouping or selection action is about epics, and an old epic address lists every row", async () => {
  const user = userEvent.setup();
  table([issue("CORE-1", { parent: "CORE-9" }), issue("CORE-2")], "?f.epic=No+epic&group=epic");
  const list = await screen.findByRole("table", { name: "Project issues table" });
  await within(list).findByRole("row", { name: "CORE-1 Work CORE-1" });
  expect(within(list).getByRole("row", { name: "CORE-2 Work CORE-2" })).toBeDefined();
  expect(headings()).toEqual([]);
  const headers = within(list)
    .getAllByRole("columnheader")
    .map((header) => header.textContent ?? "");
  expect(headers.filter((text) => /epic/i.test(text))).toEqual([]);
  await user.click(screen.getByRole("button", { name: "Columns" }));
  expect(screen.queryByRole("checkbox", { name: /epic/i })).toBeNull();
  await user.keyboard("{Escape}");
  await user.click(screen.getByRole("button", { name: "Group by" }));
  expect(screen.getByRole("radio", { name: "Status" })).toBeDefined();
  expect(screen.queryByRole("radio", { name: /epic/i })).toBeNull();
  await user.keyboard("{Escape}");
  await user.click(screen.getByRole("checkbox", { name: "Select CORE-2" }));
  const bar = screen.getByRole("toolbar");
  expect(within(bar).getByRole("button", { name: "Start next" })).toBeDefined();
  expect(within(bar).queryByRole("combobox")).toBeNull();
  expect(
    within(bar)
      .getAllByRole("button")
      .filter((button) => /epic/i.test(button.textContent ?? ""))
  ).toEqual([]);
});

test("Start next says so when every queued row selected already starts next", async () => {
  const user = userEvent.setup();
  const send = spyOn(api, "send").mockRejectedValue(new Error("Unexpected rank write"));
  restores.push(() => send.mockRestore());
  table([issue("CORE-1", { labels: ["legion"] }), issue("CORE-2", { labels: ["legion"] })]);
  await user.click(await screen.findByRole("checkbox", { name: "Select CORE-1" }));
  await user.click(screen.getByRole("checkbox", { name: "Select CORE-2" }));
  await user.click(within(screen.getByRole("toolbar")).getByRole("button", { name: "Start next" }));
  await waitFor(() =>
    expect(
      within(screen.getByRole("region", { name: "Project issues" })).getByRole("status").textContent
    ).toContain("already start next")
  );
  expect(send).not.toHaveBeenCalled();
});

/** Puts what `replacement` makes of the page storage's own `name` method in place of that
 *  method, and returns what puts the real one back. `replacement` is handed the real method, so
 *  it never reads `localStorage[name]` itself, which would find the replacement and call itself.
 *  happy-dom's `localStorage` is a Proxy that copies each method of `Storage.prototype` onto
 *  itself, bound, the first time anything reads it: a spy on the prototype reaches the storage
 *  only when nothing in the process has read that method yet, and then stays its method after
 *  `mockRestore`, a mock that no longer reaches the real method. `Object.defineProperty` passes
 *  the Proxy's trap onto the storage itself, where every later read finds it. */
function onLocalStorage<Name extends "getItem" | "setItem">(
  name: Name,
  replacement: (real: Storage[Name]) => Storage[Name]
): () => void {
  const real = localStorage[name];
  const descriptor = { configurable: true, writable: true };
  Object.defineProperty(localStorage, name, { ...descriptor, value: replacement(real) });
  return () => Object.defineProperty(localStorage, name, { ...descriptor, value: real });
}

// A browser that refuses local storage loses the saved layout and the interrupted-action
// record, and nothing else: no click may throw out of the table.
test("a browser that refuses storage still renders the table and runs an action", async () => {
  const user = userEvent.setup();
  const refuse = (key: string) => {
    if (key.startsWith("dispatch.project-table.")) throw new Error("Storage refused");
  };
  const restoreRead = onLocalStorage("getItem", (read) => (key) => {
    refuse(key);
    return read(key);
  });
  const restoreWrite = onLocalStorage("setItem", (write) => (key, value) => {
    refuse(key);
    write(key, value);
  });
  const send = spyOn(api, "send").mockResolvedValue(savedIssue("CORE-2"));
  restores.push(() => {
    restoreRead();
    restoreWrite();
    send.mockRestore();
  });
  table([
    issue("CORE-1", { labels: ["legion"], rank: "A" }),
    issue("CORE-2", { labels: ["legion"], rank: "B" }),
  ]);
  await user.click(await screen.findByRole("checkbox", { name: "Select CORE-2" }));
  await user.click(within(screen.getByRole("toolbar")).getByRole("button", { name: "Start next" }));
  await waitFor(() =>
    expect(
      within(screen.getByRole("region", { name: "Project issues" })).getByRole("status").textContent
    ).toContain("now starts next")
  );
});

// The box is measured when its contents change height. A scroll alone must read no layout: the
// virtualizer commits on every visible range, and a measurement there is a reflow per row.
test("a scroll alone does no layout reads for the table's own box", async () => {
  table([issue("CORE-1"), issue("CORE-2")]);
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  const box = screen.getByTestId("issues-table-scroll");
  const measured: string[] = [];
  const rect = spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (
    this: Element
  ) {
    measured.push(
      this instanceof HTMLElement ? (this.dataset.testid ?? this.tagName) : this.tagName
    );
    return new DOMRect(0, 0, 1000, 600);
  });
  restores.push(() => rect.mockRestore());
  act(() => {
    box.dispatchEvent(new Event("scroll", { bubbles: false }));
    window.dispatchEvent(new Event("scroll"));
  });
  expect(measured.filter((name) => name === "issues-table-scroll" || name === "THEAD")).toEqual([]);
});

// What moves the box's top edge is the content above it, which no row count reports: the box
// and the block above it are observed instead. happy-dom has no `ResizeObserver`, so the stub
// is the geometry — what the table observes, and what a notice appearing there fires.
test("the box is measured through an observer over itself and the content above it", async () => {
  const sizes = installResizeObserver();
  restores.push(() => sizes.restore());
  table([issue("CORE-1")]);
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  const box = screen.getByTestId("issues-table-scroll");
  const above = screen.getByRole("region", { name: "Project issues" }).firstElementChild;
  if (!above) throw new Error("The table has no content above it");
  const measured: string[] = [];
  const rect = spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (
    this: Element
  ) {
    measured.push(
      this instanceof HTMLElement ? (this.dataset.testid ?? this.tagName) : this.tagName
    );
    return new DOMRect(0, 0, 1000, 600);
  });
  restores.push(() => rect.mockRestore());
  // A notice appearing above the box changes only that block's size.
  sizes.resize(above);
  expect(measured).toContain("issues-table-scroll");
  measured.length = 0;
  sizes.resize(box);
  expect(measured).toContain("issues-table-scroll");
});

/** The group headings on the page, in the order they are drawn. */
function headings(): string[] {
  return [...document.querySelectorAll("tbody tr")]
    .filter((row) => row.querySelector("td[colspan]"))
    .map((row) => (row.textContent ?? "").trim());
}

// A group's place is its column's, not the row sort's: the registry lists the values in the
// order the filter offers them, and the headings read the same way.
test.each([
  ["the default Queue sort", ""],
  ["Created ascending", "&sort=created"],
  ["Key descending", "&sort=-key"],
])("grouped by Priority, the headings run P0, P1, P2, P3 under %s", async (_name, sort) => {
  // Queued rows give each group a number to sum and a rank to sort by, which is what let the
  // row sort carry the headings along with it.
  table(
    [
      issue("CORE-1", { labels: ["legion"], priority: 0, rank: "a" }),
      issue("CORE-2", { labels: ["legion"], priority: 1, rank: "b" }),
      issue("CORE-3", { labels: ["legion"], priority: 3, rank: "c" }),
      issue("CORE-4", { labels: ["legion"], priority: 2, rank: "d" }),
    ],
    `?group=priority${sort}`
  );
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  await waitFor(() =>
    expect(headings().map((text) => text.match(/P\d/)?.[0])).toEqual(["P0", "P1", "P2", "P3"])
  );
});

test.each([
  ["the default Queue sort", ""],
  ["Created ascending", "&sort=created"],
  ["Key descending", "&sort=-key"],
])("grouped by Status, the headings run in workflow order under %s", async (_name, sort) => {
  table(
    [
      issue("CORE-1", { labels: ["legion"], status: "needs_review", rank: "a" }),
      issue("CORE-2", { labels: ["legion"], status: "todo", rank: "b" }),
      issue("CORE-3", { labels: ["legion"], status: "in_progress", rank: "c" }),
    ],
    `?group=status&nofilter=1${sort}`
  );
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  await waitFor(() =>
    expect(
      headings().map((text) =>
        ["Todo", "In progress", "Needs review"].find((name) => text.includes(name))
      )
    ).toEqual(["Todo", "In progress", "Needs review"])
  );
});

// The reader usually arrives ungrouped and picks the grouping from the toolbar, which the
// table must answer as it answers a `?group=` link: TanStack asks for its sorted rows once and
// keeps that answer for the table's life, so the ordering has to read the grouping of the day.
test.each([
  // Ungrouped, the rows are the reader's sort: Queue puts the two queued rows first, and
  // Created leaves rows of one timestamp as they came.
  ["the default Queue sort", "", ["CORE-2", "CORE-4", "CORE-1", "CORE-3"]],
  ["Created ascending", "?sort=created", ["CORE-1", "CORE-2", "CORE-3", "CORE-4"]],
])("a grouping picked from the toolbar orders its headings under %s", async (_name, search, ungrouped) => {
  const user = userEvent.setup();
  table(
    [
      issue("CORE-1", { labels: ["legion"], priority: 0, rank: "a", status: "needs_review" }),
      issue("CORE-2", { labels: ["legion"], priority: 1, rank: "b", status: "todo" }),
      issue("CORE-3", { labels: ["legion"], priority: 3, rank: "c", status: "in_progress" }),
      issue("CORE-4", { labels: ["legion"], priority: 2, rank: "d", status: "todo" }),
    ],
    search
  );
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  await groupRowsBy(user, "Priority");
  await waitFor(() =>
    expect(headings().map((text) => text.match(/P\d/)?.[0])).toEqual(["P0", "P1", "P2", "P3"])
  );
  await groupRowsBy(user, "Status");
  await waitFor(() =>
    expect(
      headings().map((text) =>
        ["Todo", "In progress", "Needs review"].find((name) => text.includes(name))
      )
    ).toEqual(["Todo", "In progress", "Needs review"])
  );
  // Ungrouped again, the rows follow the reader's sort alone: no grouping order survives it.
  await groupRowsBy(user, "Nothing");
  await waitFor(() => expect(headings()).toEqual([]));
  await waitFor(() =>
    expect(
      [...document.querySelectorAll("[data-issue-row]")].map((row) =>
        row.getAttribute("data-issue-row")
      )
    ).toEqual(ungrouped)
  );
});

// TanStack hands a resize to the state the reader owns, not to the widths a narrow window
// derives from it, so a drag on a phone leaves the saved desktop key and title widths alone.
test("resizing a column on a phone leaves the saved key and title widths alone", async () => {
  const user = userEvent.setup();
  localStorage.setItem(
    "dispatch.project-table.CORE",
    JSON.stringify({ sizing: { key: 200, title: 500 } })
  );
  stubScrollBoxWidth(390);
  table([issue("CORE-1")]);
  const header = await screen.findByRole("columnheader", { name: /Status/ });
  const handle = header.querySelector(".cursor-col-resize");
  if (!handle) throw new Error("The Status column has no resize edge");
  await user.pointer([
    { keys: "[MouseLeft>]", target: handle, coords: { clientX: 100 } },
    { target: document.body, coords: { clientX: 160 } },
    { keys: "[/MouseLeft]", target: document.body, coords: { clientX: 160 } },
  ]);
  await waitFor(() => {
    const saved = JSON.parse(localStorage.getItem("dispatch.project-table.CORE") ?? "{}");
    expect(saved.sizing.status).toBeGreaterThan(128);
  });
  const saved = JSON.parse(localStorage.getItem("dispatch.project-table.CORE") ?? "{}");
  expect(saved.sizing.key).toBe(200);
  expect(saved.sizing.title).toBe(500);
});

/** What Open keeps hidden until a reader shows it, in its stored record's words: every column
 *  but its defaults, the Legion ones of a project with a daemon among them. */
const OPEN_HIDDEN = [
  "updated",
  "closed",
  "roles",
  "activity",
  "waited",
  "waiting_reason",
  "time_in_phase",
  "work_hours",
  "review_rounds",
  "legion",
  "phase_hours",
].map((id) => `"${id}":false`);

// A reader's saved layout is read and written back as it always was: under the same key, which
// is Open's now, in the same JSON, with a column the table does not have, a pinned column's
// visibility and a width out of bounds dropped, and every other field of the record left where
// it was. A project's table has none of the listing columns, so it writes none of them. Open
// keeps hidden the columns it does not open with, Updated among them, until a reader shows one.
test("the List writes a reader's saved layout back under the same key in the same JSON", async () => {
  localStorage.setItem(
    "dispatch.project-table.CORE",
    JSON.stringify({
      sizing: { title: 360, name: 200, key: 10, status: 3000, project: 140 },
      order: ["status", "name", "key", "status", "moved"],
      visibility: { labels: false, name: false, key: false, updated: true, advances: true },
      kept: 1,
    })
  );
  table([issue("CORE-1")]);
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  expect(localStorage.getItem("dispatch.project-table.CORE")).toBe(
    `{"sizing":{"title":360},"order":["status","key"],"visibility":{"updated":true,${OPEN_HIDDEN.slice(1).join(",")},"labels":false},"kept":1}`
  );
});

test("the List stores a reader's first layout with Updated hidden and no listing column", async () => {
  table([issue("CORE-1")]);
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  expect(localStorage.getItem("dispatch.project-table.CORE")).toBe(
    `{"sizing":{},"order":[],"visibility":{${OPEN_HIDDEN.join(",")}}}`
  );
});

// What `in` read on main as a column: a name every object inherits. No app path writes one, so
// only a corrupt record holds it, and it is dropped like any other name the table has no column for.
test("a stored width under an inherited name such as toString is dropped", () => {
  localStorage.setItem("dispatch.project-table.CORE", '{"sizing":{"toString":200,"title":360}}');
  expect(readLayout(tableStorageKey("CORE"), ISSUE_TABLE_COLUMNS).sizing).toEqual({ title: 360 });
});

/** Makes `getBoundingClientRect` report an issue table's scroll box `width` pixels wide and
 *  every other element 100, each 40 high, until the row ends. The spy is on
 *  `Element.prototype`, which owns the method: one on `HTMLElement` would leave an own copy
 *  behind that shadows every later spy. */
function stubScrollBoxWidth(width: number): void {
  const rect = spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (
    this: Element
  ) {
    const box = this.getAttribute("data-testid") === "issues-table-scroll";
    return new DOMRect(0, 0, box ? width : 100, 40);
  });
  restores.push(() => rect.mockRestore());
}

/** Each header cell of the table named `name`, left to right, as `<its column's words> <width>
 *  <the offset a pinned cell is held at, or ->`. */
function headerCells(name: string): string[] {
  const head = screen.getByRole("table", { name }).querySelector("thead");
  return [...(head?.querySelectorAll("th") ?? [])].map((cell) =>
    [
      (cell.textContent ?? "").replace(/[▲▼]|Filter$/g, "") || "-",
      cell.style.width,
      cell.style.left || "-",
    ].join(" ")
  );
}

/** The header cells of an issue table another page draws (the work under a priority), drawn
 *  with the scroll box `width` pixels wide, at the address's `search`. */
async function headerLayout(width: number, search = ""): Promise<string[]> {
  stubScrollBoxWidth(width);
  table([issue("CORE-1", { advances: [{ id: "ship-it", inherited: false }] })], search, undefined, {
    listing: { advances: "ship-it" },
    stateKey: "w",
  });
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  return headerCells("Priority issues table");
}

/** The columns after the pinned ones, at the registry's widths, then the Start next column. */
const UNPINNED_REST = [
  "Status 132px -",
  "Assignee 180px -",
  "Created by 200px -",
  "Labels 200px -",
  "Claimed by 220px -",
  "Open asks 148px -",
  "Created 148px -",
  "- 104px -",
];

// A table another page draws keeps today's widths and pins at a desktop box, a box too narrow for
// every pin, and a phone: the registry's starting widths and minimums, Select, Priority, Key and
// Title pinned within 60% of a wide box with Title narrowing first and the rightmost unpinning,
// and Select, Key and Title within 85% of a phone's, Key at its phone width and Title taking what
// is left.
test("an embedded table draws its columns at today's widths and pins at 1400 px", async () => {
  expect(await headerLayout(1400)).toEqual([
    "- 44px 0px",
    "Priority 140px 44px",
    "Key 108px 184px",
    "Title 380px 292px",
    "Queue 124px -",
    ...UNPINNED_REST,
  ]);
});

test("an embedded table narrows Title and unpins from the right in a 700 px box", async () => {
  localStorage.setItem(
    "dispatch.project-table.priority-work",
    JSON.stringify({ sizing: { key: 200, title: 500 } })
  );
  expect(await headerLayout(700)).toEqual([
    "- 44px 0px",
    "Priority 140px 44px",
    "Key 200px 184px",
    "Queue 124px -",
    "Title 116px -",
    ...UNPINNED_REST,
  ]);
});

test("an embedded table pins Select, Key and Title on a 390 px phone", async () => {
  expect(await headerLayout(390)).toEqual([
    "- 44px 0px",
    "Key 100px 44px",
    "Title 187.5px 144px",
    "Priority 140px -",
    "Queue 124px -",
    ...UNPINNED_REST,
  ]);
});

// dispatch://INFRA-3221/comment/141bbad2-b75e-4bb4-b0a7-b5b1af541a3c, with R13's leave to widen a
// width that cuts its header: on a phone the embedded tables' Key holds its header whole, sorted
// by it and not. Measured in Chromium (builds Job infra-3221-t16c-accept-2784l): its sort button
// is a 44 px touch target, which holds Key with its mark (38 px), its filter button 44 px, and the
// cell's padding 12 px below `md`; 88 px left the filter 12 px past the cell.
test("on a phone an embedded table's Key holds its header whole, sorted by it and not", async () => {
  const needs = 44 + 44 + 12;
  const unsorted = await headerLayout(390);
  expect(widthOf(unsorted, "Key")).toBeGreaterThanOrEqual(needs);
  expect(unsorted).toContain("Key 100px 44px");
  for (const restore of restores.splice(0)) restore();

  const sorted = await headerLayout(390, "?w.sort=key");
  const table = screen.getByRole("table", { name: "Priority issues table" });
  expect(within(table).getByRole("columnheader", { name: /^Key/ })).toHaveAttribute(
    "aria-sort",
    "ascending"
  );
  expect(widthOf(sorted, "Key")).toBeGreaterThanOrEqual(needs);
  expect(sorted).toContain("Key 100px 44px");
});

// A width the reader dragged on a desktop is the width the header is drawn at, and a stored one
// below a column's minimum is drawn at the minimum.
test("a stored desktop width reaches an embedded table's header", async () => {
  localStorage.setItem(
    "dispatch.project-table.priority-work",
    JSON.stringify({ sizing: { assignee: 300, status: 50 } })
  );
  const cells = await headerLayout(1400);
  expect(cells).toContain("Assignee 300px -");
  expect(cells).toContain("Status 132px -");
});

// R13: the priority pages keep today's rule. A Title the reader widened by 300 px would take the
// pins past 60% of a 1400 px box, so it narrows by what they would pass it by, and stays pinned.
test("an embedded table narrows a Title the reader widened to keep its pins within their share", async () => {
  localStorage.setItem(
    "dispatch.project-table.priority-work",
    JSON.stringify({ sizing: { title: 380 + 300 } })
  );
  expect(await headerLayout(1400)).toEqual([
    "- 44px 0px",
    "Priority 140px 44px",
    "Key 108px 184px",
    "Title 548px 292px",
    "Queue 124px -",
    ...UNPINNED_REST,
  ]);
});

// dispatch://INFRA-3221/comment/141bbad2-b75e-4bb4-b0a7-b5b1af541a3c: today's minimums of the
// priority pages' tables hold each header with its sort mark (Open asks' 148 px its 83 px label
// and mark, measured), so a sort by Open asks widens it not at all.
test("an embedded table's Open asks is 148 px sorted by it and not", async () => {
  expect(await headerLayout(1400)).toContain("Open asks 148px -");
  for (const restore of restores.splice(0)) restore();
  expect(await headerLayout(1400, "?w.sort=asks")).toContain("Open asks 148px -");
});

/** The header cells of the Issues tab's Open, in a project with no daemon, drawn with the
 *  scroll box `width` pixels wide, at the address's `search`. */
async function openHeaderLayout(width: number, search = ""): Promise<string[]> {
  stubScrollBoxWidth(width);
  table([issue("CORE-1")], search);
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  return headerCells("Project issues table");
}

/** The width the Issues tab draws the header `name` at, in px. */
const widthOf = (cells: readonly string[], name: string): number =>
  Number.parseFloat(
    cells
      .find((cell) => cell.startsWith(`${name} `))
      ?.split(" ")
      .at(-2) ?? ""
  );

// dispatch://INFRA-3221/comment/141bbad2-b75e-4bb4-b0a7-b5b1af541a3c: in a 1000 px box every
// column of Open is at its minimum. Open asks' 140 px holds its header and its 44 px filter
// button, and while Open is sorted by it the sort mark too, at least 148 px (its 83 px label with
// the mark, measured). A sort by another column gives the mark's width back, and Created by,
// whose minimum already holds its own mark, keeps it.
test("on the Issues tab Open asks is 140 px, and takes its sort mark on only while sorted by it", async () => {
  const user = userEvent.setup();
  expect(widthOf(await openHeaderLayout(1000), "Open asks")).toBe(140);
  for (const restore of restores.splice(0)) restore();

  const sorted = await openHeaderLayout(1000, "?sort=asks");
  expect(widthOf(sorted, "Open asks")).toBeGreaterThanOrEqual(148);
  expect(widthOf(sorted, "Open asks")).toBe(140 + SORT_MARK_WIDTH);
  const author = widthOf(sorted, "Created by");

  const tableNamed = screen.getByRole("table", { name: "Project issues table" });
  await user.click(within(tableNamed).getByRole("button", { name: /^Created by/ }));
  await waitFor(() => expect(routeSearch()).toContain("sort=author"));
  const resorted = headerCells("Project issues table");
  expect(widthOf(resorted, "Open asks")).toBe(140);
  expect(widthOf(resorted, "Created by")).toBe(author);
});

// k12 (R10): on the Issues tab a Title the reader widened by 300 px is drawn at its stored width.
// With it the pins would cover 980 px of a 1400 px box, past their 840, so Title scrolls with the
// table after them rather than drawing narrower.
test("on the Issues tab a Title the reader widened keeps its width, and scrolls past the pins' share", async () => {
  localStorage.setItem(
    "dispatch.project-table.CORE",
    JSON.stringify({ sizing: { title: 380 + 300 } })
  );
  expect((await openHeaderLayout(1400)).slice(0, 5)).toEqual([
    "- 44px 0px",
    "Priority 132px 44px",
    "Key 124px 176px",
    "Title 680px -",
    "Status 150px -",
  ]);
  expect(JSON.parse(localStorage.getItem("dispatch.project-table.CORE") ?? "{}").sizing).toEqual({
    title: 680,
  });
});

// k12 (R10): on a phone Select and Key are pinned, and Title follows them, not pinned, and fills
// the rest of the row: 99% of the box, whose two border pixels are not the rows'.
test("on a 390 px phone the Issues tab pins Select and Key, and Title follows them and fills the row", async () => {
  const [select, key, title, next] = await openHeaderLayout(390);
  expect([select, key, next]).toEqual(["- 44px 0px", "Key 124px 44px", "Priority 132px -"]);
  const [name, width, left] = (title ?? "").split(" ");
  expect([name, left]).toEqual(["Title", "-"]);
  expect(Number.parseFloat(width ?? "")).toBeCloseTo(390 * 0.99 - 44 - 124);
});

// k12 (R10): on a phone a header dropped onto the one after Title moves there, and one dropped
// onto Title lands right after it, since Title stays after Key. The order drawn is the one
// stored, so a reload draws it again.
test("on a phone a header dropped after Title or onto Title moves there, and a reload keeps the order", async () => {
  /** The words of the first six headers, left to right. */
  const drawn = () =>
    headerCells("Project issues table")
      .slice(0, 6)
      .map((cell) => cell.replace(/ \S+ \S+$/, ""));
  await openHeaderLayout(390);
  expect(drawn()).toEqual(["-", "Key", "Title", "Priority", "Status", "Created by"]);
  const header = (name: RegExp) =>
    within(screen.getByRole("table", { name: "Project issues table" })).getByRole("columnheader", {
      name,
    });
  const drag = (dragged: RegExp, onto: RegExp) => {
    const handle = header(dragged).querySelector('button[draggable="true"]');
    if (handle === null) throw new Error(`The ${dragged} header has no drag handle`);
    fireEvent.dragStart(handle);
    fireEvent.dragOver(header(onto));
    fireEvent.drop(header(onto));
    fireEvent.dragEnd(handle);
  };

  drag(/^Status/, /^Priority/);
  await waitFor(() =>
    expect(drawn()).toEqual(["-", "Key", "Title", "Status", "Priority", "Created by"])
  );
  drag(/^Created by/, /^Title/);
  const moved = ["-", "Key", "Title", "Created by", "Status", "Priority"];
  await waitFor(() => expect(drawn()).toEqual(moved));
  // Stored as drawn: Title after Key, the column dropped onto it right after it.
  const stored = JSON.parse(localStorage.getItem("dispatch.project-table.CORE") ?? "{}");
  expect(stored.order.slice(0, 6)).toEqual([
    "select",
    "key",
    "title",
    "author",
    "status",
    "priority",
  ]);

  // A reload: the page drawn again from what the browser kept.
  for (const restore of restores.splice(0)) restore();
  await openHeaderLayout(390);
  expect(drawn()).toEqual(moved);
});

/** What every sortable header's tooltip says of its sort, and what a header that a drag moves
 *  adds to it (`DataTable`). */
const SORT_HINT = "Sort (shift-click to add a second sort)";
const DRAG_HINT = `${SORT_HINT}; drag to move the column`;

// R10: on a phone Title stays right after Key, so it is no drag handle and its sort hint says
// nothing of a drag; Priority, after it, is dragged as before. On a desktop the rule is today's:
// a Title the reader widened past the pins' share scrolls with the table and is dragged as any
// other column there.
test("on a phone Title is no drag handle and its hint names no drag, and on a desktop it is unchanged", async () => {
  const user = userEvent.setup();
  const sort = (name: string) =>
    within(screen.getByRole("table", { name: "Project issues table" })).getByRole("button", {
      name,
    });
  const hintOf = async (name: string) => {
    await user.hover(sort(name));
    const hint = screen.getByRole("tooltip").textContent;
    await user.unhover(sort(name));
    return hint;
  };
  await openHeaderLayout(390);
  expect(sort("Title")).toHaveAttribute("draggable", "false");
  expect(await hintOf("Title")).toBe(SORT_HINT);
  expect(sort("Priority")).toHaveAttribute("draggable", "true");
  expect(await hintOf("Priority")).toBe(DRAG_HINT);

  for (const restore of restores.splice(0)) restore();
  localStorage.setItem(
    "dispatch.project-table.CORE",
    JSON.stringify({ sizing: { title: 380 + 300 } })
  );
  await openHeaderLayout(1400);
  expect(sort("Title")).toHaveAttribute("draggable", "true");
  expect(await hintOf("Title")).toBe(DRAG_HINT);
});

test("the Status filter counts Done once the closed issues are loaded", async () => {
  const user = userEvent.setup();
  table([issue("CORE-1"), issue("CORE-2", { status: "done" })], "?status=done");
  await screen.findByRole("row", { name: "CORE-2 Work CORE-2" });
  await user.click(screen.getByRole("button", { name: /^Filter Status/ }));
  expect(screen.getByRole("option", { name: "Done, 1 issue" })).toBeDefined();
});

// A write of an action the reader dismissed is nobody's interrupted action. Retried after, it
// reports like any other change rather than telling the reader to finish steps that are gone.
test("a write retried after its action was dismissed reports in the ordinary wording", async () => {
  const user = userEvent.setup();
  let refused = false;
  const send = spyOn(api, "send").mockImplementation(async (request) => {
    if (!refused) {
      refused = true;
      throw new ApiError(403, { error: "refused", code: "FORBIDDEN" });
    }
    return savedIssue(request.path.split("/").at(-1) ?? "CORE-2") as never;
  });
  restores.push(() => send.mockRestore());
  const { client } = table([
    issue("CORE-1", { labels: ["legion"], rank: "a" }),
    issue("CORE-2", { labels: ["legion"], rank: "b" }),
  ]);
  await user.click(await screen.findByRole("checkbox", { name: "Select CORE-2" }));
  await user.click(within(screen.getByRole("toolbar")).getByRole("button", { name: "Start next" }));
  const recovery = await screen.findByRole("region", { name: "Interrupted table action" });
  await user.click(within(recovery).getByRole("button", { name: "Dismiss" }));
  const retry = writesOf(client)
    .rows()
    .find((write) => write.status === "refused");
  if (!retry) throw new Error("The refused write was lost");
  writesOf(client).retry(retry.id);
  await waitFor(() =>
    expect(
      within(screen.getByRole("region", { name: "Project issues" })).getByRole("status").textContent
    ).toContain("All changes saved")
  );
  expect(screen.queryByRole("region", { name: "Interrupted table action" })).toBeNull();
});

// B1: a status the bundle does not know — a bookmark in another casing, a deploy window — is a
// filter that matches nothing, not a page the reader loses.
test.each([
  "bogus",
  "Done",
  "in-progress",
])("an unknown ?status=%s leaves the table standing, with the value in the filter", async (status) => {
  const user = userEvent.setup();
  table([issue("CORE-1"), issue("CORE-2")], `?status=${status}`);
  expect(await screen.findByRole("table", { name: "Project issues table" })).toBeDefined();
  expect(screen.getByRole("searchbox", { name: "Search issues" })).toBeDefined();
  await user.click(screen.getByRole("button", { name: /^Filter Status/ }));
  expect(screen.getAllByRole("option", { name: new RegExp(`^${status}`) }).length).toBeGreaterThan(
    0
  );
});

// B2: Start next over a selection the reader cannot see all of says how many it will move.
test("Start next on a selection larger than the page asks first", async () => {
  const user = userEvent.setup();
  const send = spyOn(api, "send").mockResolvedValue(savedIssue("CORE-1") as never);
  restores.push(() => send.mockRestore());
  const rows = [
    // Ahead of the queue and out of this view's filter, so the selection has somewhere to move.
    issue("CORE-0", { labels: ["legion"], priority: 0, rank: "000" }),
    ...Array.from({ length: 30 }, (_, index) =>
      issue(`CORE-${index + 1}`, {
        labels: ["legion"],
        priority: 1,
        rank: String(index + 1).padStart(3, "0"),
      })
    ),
  ];
  table(rows, "?f.priority=1&size=25");
  await user.click(await screen.findByRole("checkbox", { name: "Select every row on this page" }));
  await user.click(screen.getByRole("button", { name: "Select all 30 matching" }));
  await user.click(
    within(screen.getByRole("toolbar")).getByRole("button", { name: /^Start next$/ })
  );
  const asked = await screen.findByRole("dialog", { name: "Start these next?" });
  expect(asked.textContent).toContain("30 issues");
  expect(send).not.toHaveBeenCalled();
  await user.click(within(asked).getByRole("button", { name: /^Start next$/ }));
  await waitFor(() => expect(send).toHaveBeenCalled());
});

// R4: Start next keeps its own set, the Todo issues labelled legion, and its texts name it. A
// selection with none of the set is refused, and nothing is sent.
test("Start next on a selection with no Todo issue labelled legion is refused with a text that names the set", async () => {
  const user = userEvent.setup();
  const send = spyOn(api, "send").mockResolvedValue(savedIssue("CORE-1") as never);
  restores.push(() => send.mockRestore());
  table([
    issue("CORE-1", { labels: ["legion"], rank: "a" }),
    issue("CORE-2", { rank: "b" }),
    issue("CORE-3", { labels: ["legion"], status: "testing", rank: "c" }),
  ]);
  await user.click(await screen.findByRole("checkbox", { name: "Select CORE-2" }));
  await user.click(screen.getByRole("checkbox", { name: "Select CORE-3" }));
  await user.click(
    within(screen.getByRole("toolbar")).getByRole("button", { name: /^Start next$/ })
  );
  const region = screen.getByRole("region", { name: "Project issues" });
  expect((await within(region).findByRole("alert")).textContent).toBe(
    "Start next moves only Todo issues labelled legion."
  );
  expect(screen.queryByRole("dialog", { name: "Start these next?" })).toBeNull();
  expect(send).not.toHaveBeenCalled();
});

// R4: in a mixed selection the confirm says how many selected issues are outside Start next's set
// and stay where they are, and the issues of the set move.
test("Start next on a mixed selection names how many selected issues stay where they are", async () => {
  const user = userEvent.setup();
  const send = spyOn(api, "send").mockResolvedValue(savedIssue("CORE-2") as never);
  restores.push(() => send.mockRestore());
  table([
    issue("CORE-1", { labels: ["legion"], rank: "a" }),
    issue("CORE-2", { labels: ["legion"], rank: "b" }),
    issue("CORE-3", { rank: "c" }),
    issue("CORE-4", { labels: ["legion"], status: "testing", rank: "d" }),
  ]);
  for (const key of ["CORE-2", "CORE-3", "CORE-4"])
    await user.click(await screen.findByRole("checkbox", { name: `Select ${key}` }));
  await user.click(
    within(screen.getByRole("toolbar")).getByRole("button", { name: /^Start next$/ })
  );
  const asked = await screen.findByRole("dialog", { name: "Start these next?" });
  expect(asked.textContent).toContain(
    "1 issue moves ahead of the other Todo issues labelled legion. Dispatch sends one change for each issue. 2 selected issues are not Todo issues labelled legion. They stay where they are."
  );
  expect(send).not.toHaveBeenCalled();
  await user.click(within(asked).getByRole("button", { name: /^Start next$/ }));
  await waitFor(() => expect(send).toHaveBeenCalledTimes(1));
});

// F1: the button named Clear filters goes back to the view the table opens with, which is not
// "every closed issue in the project".
test("Clear filters restores the default view rather than loading every closed issue", async () => {
  const user = userEvent.setup();
  const { listing } = table([issue("CORE-1")], "?q=rank&status=done");
  await user.click(await screen.findByRole("button", { name: "Clear filters" }));
  await waitFor(() =>
    expect(screen.getByTestId("route-search")?.textContent ?? "").not.toContain("nofilter")
  );
  expect(screen.getByTestId("route-search").textContent).not.toContain("status=");
  expect(listing.mock.calls.every(([options]) => options?.open !== false)).toBe(true);
});

// The List draws its toolbar while it reads issues it has not loaded, as the Board draws its
// controls: a press in the toolbar that starts that read leaves the toolbar where it was, with
// the keyboard focus in it, and the skeleton stands in for the table alone. **Clear filters**
// takes every chip off with it and goes itself, since nothing then differs from the view's own,
// and hands its focus to the row's filter trigger, which is the rule for it on every page
// (`components/ControlRow.tsx`); what must not happen is the focus dropping to the page's body.
test("a press in the toolbar that sends the List off to read its issues keeps the toolbar, and the focus in it", async () => {
  const user = userEvent.setup();
  // Opened on the closed issues, whose listing answers. The open issues' listing is still out
  // when Clear filters goes back to them.
  const open = Promise.withResolvers<IssueSummary[]>();
  table([issue("CORE-1"), issue("CORE-2", { status: "done" })], "?status=done", {
    open: open.promise,
  });
  await screen.findByRole("row", { name: "CORE-2 Work CORE-2" });
  await user.click(screen.getByRole("button", { name: "Clear filters" }));
  expect(await screen.findByRole("status", { name: "Loading issues" })).toBeDefined();
  expect(screen.queryByRole("table", { name: "Project issues table" })).toBeNull();
  expect(screen.getByRole("searchbox", { name: "Search issues" })).toBeDefined();
  expect(screen.queryByRole("button", { name: "Clear filters" })).toBeNull();
  const trigger = screen.getByRole("button", { name: /^Filters · \d+$/ });
  // Not the page's body, from which a keyboard reader would start again at its first link.
  expect(trigger).toHaveFocus();
  open.resolve([issue("CORE-1")]);
  expect(await screen.findByRole("row", { name: "CORE-1 Work CORE-1" })).toBeDefined();
  expect(trigger).toHaveFocus();
});

// A column filter goes with the table it heads when its press sends the List off to read its
// issues: the focus in its list, a panel of its own, goes to the toolbar's search box. The pick is
// the pointer's, whose option keeps the focus in the list's text box, and the List stands in the
// shell's main content with the page's focus rescue mounted: the press inside the list's panel is
// the reader's own, so the List's hand-off answers it rather than the rescue sending the focus out
// of the main content, to the shell's navigation.
test("a column filter press that sends the List off to read its issues leaves the focus on the toolbar", async () => {
  const user = userEvent.setup();
  // Opened on Done, whose listing answers. Ticking Todo asks for the open issues too, whose
  // listing is still out.
  const open = Promise.withResolvers<IssueSummary[]>();
  table([issue("CORE-1"), issue("CORE-2", { status: "done" })], "?status=done", {
    open: open.promise,
    rescued: true,
  });
  await screen.findByRole("row", { name: "CORE-2 Work CORE-2" });
  await user.click(screen.getByRole("button", { name: /^Filter Status/ }));
  expect(await screen.findByRole("combobox", { name: "Search Status" })).toHaveFocus();
  await user.click(screen.getByRole("option", { name: /^Todo/ }));
  expect(await screen.findByRole("status", { name: "Loading issues" })).toBeDefined();
  expect(screen.queryByRole("table", { name: "Project issues table" })).toBeNull();
  await afterRescue();
  // Not the page's body, from which a keyboard reader would start again at its first link, nor
  // that link itself, outside the main content.
  expect(screen.getByRole("searchbox", { name: "Search issues" })).toHaveFocus();
  expect(screen.getByRole("link", { name: "Inbox" })).not.toHaveFocus();
  open.resolve([issue("CORE-1")]);
  expect(await screen.findByRole("row", { name: "CORE-1 Work CORE-1" })).toBeDefined();
  expect(screen.getByRole("searchbox", { name: "Search issues" })).toHaveFocus();
});

// A row's own Start next is held (`aria-disabled`, `heldPress`) from its press until the list
// read again moves its issue up the queue and takes the Start next away: it keeps the focus while
// its write is out, and its going is the reader's doing, so the focus goes to a control of that
// row, never to the table's box. With the hold ended at the write's answer, before that read, the
// List's rescue read the Start next's going as a live update's and sent the focus to the box.
test("a row's Start next stays held until the list read again takes it away, and its row keeps the focus", async () => {
  const user = userEvent.setup();
  const send = spyOn(api, "send").mockResolvedValue(savedIssue("CORE-2") as never);
  restores.push(() => send.mockRestore());
  const { listing } = table(
    [
      issue("CORE-1", { labels: ["legion"], rank: "B" }),
      issue("CORE-2", { labels: ["legion"], rank: "C" }),
    ],
    "",
    { rescued: true }
  );
  const row = await screen.findByRole("row", { name: "CORE-2 Work CORE-2" });
  const startNext = within(row).getByRole("button", { name: "Start next" });
  // The read after the write, held until the test answers it with CORE-2 at the queue's head.
  const reread = Promise.withResolvers<IssueSummary[]>();
  const reads = listing.mock.calls.length;
  listing.mockImplementation(() => reread.promise);
  act(() => startNext.focus());
  await user.keyboard("{Enter}");
  await waitFor(() => expect(send).toHaveBeenCalledTimes(1));
  await waitFor(() => expect(listing.mock.calls.length).toBeGreaterThan(reads));
  await afterRescue();
  expect(startNext).toHaveAttribute("aria-disabled", "true");
  expect(startNext).toHaveFocus();

  await act(async () =>
    reread.resolve([
      issue("CORE-1", { labels: ["legion"], rank: "B" }),
      issue("CORE-2", { labels: ["legion"], rank: "A" }),
    ])
  );
  const moved = () => screen.getByRole("row", { name: "CORE-2 Work CORE-2" });
  await waitFor(() =>
    expect(within(moved()).queryByRole("button", { name: "Start next" })).toBeNull()
  );
  await afterRescue();
  expect(moved().contains(document.activeElement)).toBe(true);
  expect(screen.getByTestId("issues-table-scroll")).not.toHaveFocus();
});

// A row's Start next whose write Dispatch refuses changed nothing, so the list read again after it
// has nothing to take away: the Start next is free again at the refusal, while that read is still
// out, rather than held through it.
test("a row's Start next that Dispatch refuses is free again before the list read again answers", async () => {
  const user = userEvent.setup();
  const send = spyOn(api, "send").mockRejectedValue(
    new ApiError(403, { error: "Rank refused", code: "FORBIDDEN" })
  );
  restores.push(() => send.mockRestore());
  const { listing } = table([
    issue("CORE-1", { labels: ["legion"], rank: "B" }),
    issue("CORE-2", { labels: ["legion"], rank: "C" }),
  ]);
  const row = await screen.findByRole("row", { name: "CORE-2 Work CORE-2" });
  const startNext = within(row).getByRole("button", { name: "Start next" });
  // The read after the refusal, held until the test answers it with the queue unchanged.
  const reread = Promise.withResolvers<IssueSummary[]>();
  const reads = listing.mock.calls.length;
  listing.mockImplementation(() => reread.promise);
  act(() => startNext.focus());
  await user.keyboard("{Enter}");
  expect((await screen.findByRole("alert")).textContent).toContain("Rank refused");
  await waitFor(() => expect(listing.mock.calls.length).toBeGreaterThan(reads));
  expect(send).toHaveBeenCalledTimes(1);
  expect(startNext).not.toHaveAttribute("aria-disabled");

  await act(async () =>
    reread.resolve([
      issue("CORE-1", { labels: ["legion"], rank: "B" }),
      issue("CORE-2", { labels: ["legion"], rank: "C" }),
    ])
  );
  expect(
    within(screen.getByRole("row", { name: "CORE-2 Work CORE-2" })).getByRole("button", {
      name: "Start next",
    })
  ).not.toHaveAttribute("aria-disabled");
});

// A notice's button that takes the notice away hands the focus on, never to the page's body
// (ruling ff1a60f5): to the control now at its place among the List's notices, else the one
// before it, else the toolbar's search box.
test.each([
  "Finish",
  "Dismiss",
])("the interrupted action's %s hands its focus to the search box", async (press) => {
  const user = userEvent.setup();
  localStorage.setItem(
    "dispatch.project-table.CORE",
    JSON.stringify({
      action: {
        id: "unfinished",
        kind: "startNext",
        project: "CORE",
        keys: ["CORE-1"],
        writes: {},
      },
    })
  );
  const send = spyOn(api, "send").mockResolvedValue(savedIssue("CORE-1") as never);
  restores.push(() => send.mockRestore());
  table([issue("CORE-1")]);
  const recovery = await screen.findByRole("region", { name: "Interrupted table action" });
  within(recovery).getByRole("button", { name: press }).focus();
  await user.keyboard("{Enter}");
  await waitFor(() =>
    expect(screen.queryByRole("region", { name: "Interrupted table action" })).toBeNull()
  );
  expect(screen.getByRole("searchbox", { name: "Search issues" })).toHaveFocus();
});

// F6: a page never ends with a heading whose rows all start on the next one, and a heading
// that shows part of its group says how much of it is here.
test.each([
  ["a heading with no rows left on the page is dropped", 24, "bob"],
  ["a heading showing part of its group counts what is here", 20, "3 of 20 issues"],
])("%s", async (_name, first, expected) => {
  const leaves = [
    ...Array.from({ length: first }, (_, index) =>
      issue(`CORE-1${String(index).padStart(2, "0")}`, { assignee: "alice" })
    ),
    ...Array.from({ length: 20 }, (_, index) =>
      issue(`CORE-2${String(index).padStart(2, "0")}`, { assignee: "bob" })
    ),
  ];
  table(leaves, "?group=assignee&size=25");
  await screen.findByRole("row", { name: "CORE-100 Work CORE-100" });
  const rows = [...document.querySelectorAll("tbody tr")].filter(
    (row) => row.querySelector("td[colspan]") !== null || row.hasAttribute("data-issue-row")
  );
  // No heading stands last, and none is followed straight by another heading.
  const isHeading = (row: Element | undefined) =>
    row !== undefined && row.querySelector("td[colspan]") !== null;
  expect(isHeading(rows.at(-1))).toBe(false);
  for (const [index, row] of rows.entries())
    if (isHeading(row)) expect(isHeading(rows[index + 1])).toBe(false);
  if (expected === "bob") expect(headings().join(" | ")).not.toContain(expected);
  else expect(headings().join(" | ")).toContain(expected);
});

// F7: the steps of a start-next are known before the first write, so the store is written once.
// Three issues move up, because with one a record written once per issue is the same two writes.
test("Start next writes the store once for the whole batch", async () => {
  const user = userEvent.setup();
  const send = spyOn(api, "send").mockResolvedValue(savedIssue("CORE-1") as never);
  restores.push(() => send.mockRestore());
  const rows = Array.from({ length: 10 }, (_, index) =>
    issue(`CORE-${index + 1}`, { labels: ["legion"], rank: String(index).padStart(3, "0") })
  );
  table(rows);
  await screen.findByRole("row", { name: "CORE-10 Work CORE-10" });
  for (const key of ["CORE-8", "CORE-9", "CORE-10"])
    await user.click(screen.getByRole("checkbox", { name: `Select ${key}` }));
  const written: string[] = [];
  restores.push(
    onLocalStorage("setItem", (write) => (key, value) => {
      written.push(value);
      write(key, value);
    })
  );
  await user.click(
    within(screen.getByRole("toolbar")).getByRole("button", { name: /^Start next$/ })
  );
  // The notice follows the last rank write and the record's dismissal, so no write of the
  // action lands after it.
  await waitFor(() =>
    expect(
      within(screen.getByRole("region", { name: "Project issues" })).getByRole("status").textContent
    ).toContain("3 issues now start next")
  );
  expect(send).toHaveBeenCalledTimes(3);
  const records = written.filter((value) => value.includes('"kind":"startNext"'));
  // One write to begin the action and one for its whole batch of keys, not one per issue.
  expect(records.length).toBe(2);
});

// O8: a record a tab left behind a day ago is not this reader's unfinished work.
test("an action record older than a day is gone, and a new action may begin", async () => {
  const user = userEvent.setup();
  const send = spyOn(api, "send").mockResolvedValue(savedIssue("CORE-1") as never);
  restores.push(() => send.mockRestore());
  localStorage.setItem(
    "dispatch.project-table.CORE",
    JSON.stringify({
      action: {
        id: "yesterday",
        kind: "startNext",
        project: "CORE",
        keys: ["CORE-1"],
        startedAt: Date.now() - 25 * 60 * 60 * 1000,
        writes: {},
      },
    })
  );
  table([
    issue("CORE-1", { labels: ["legion"], rank: "b" }),
    issue("CORE-2", { labels: ["legion"], rank: "a" }),
  ]);
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  expect(screen.queryByRole("region", { name: "Interrupted table action" })).toBeNull();
  await user.click(screen.getByRole("checkbox", { name: "Select CORE-1" }));
  await user.click(
    within(screen.getByRole("toolbar")).getByRole("button", { name: /^Start next$/ })
  );
  await waitFor(() => expect(send).toHaveBeenCalled());
});

// I5: a refused write the reader retried in Pending changes answers no second retry, and Finish
// must not sit waiting for an outcome that will never come.
test("Finish completes when a refused write was retried from Pending changes", async () => {
  const user = userEvent.setup();
  let refuse = true;
  const send = spyOn(api, "send").mockImplementation(async () => {
    if (refuse) throw new ApiError(403, { error: "refused", code: "FORBIDDEN" });
    return savedIssue("CORE-2") as never;
  });
  restores.push(() => send.mockRestore());
  const { client } = table([
    issue("CORE-2", { labels: ["legion"], rank: "b" }),
    issue("CORE-3", { labels: ["legion"], rank: "a" }),
  ]);
  await user.click(await screen.findByRole("checkbox", { name: "Select CORE-2" }));
  await user.click(
    within(screen.getByRole("toolbar")).getByRole("button", { name: /^Start next$/ })
  );
  await screen.findByRole("alert");
  refuse = false;
  const refused = writesOf(client)
    .rows()
    .find((write) => write.status === "refused");
  if (!refused) throw new Error("The rank write was not refused");
  // The reader retries it themselves, so Finish finds a write already on its way.
  act(() => {
    writesOf(client).retry(refused.id);
  });
  const recovery = await screen.findByRole("region", { name: "Interrupted table action" });
  await user.click(within(recovery).getByRole("button", { name: "Finish" }));
  await waitFor(() =>
    expect(screen.queryByRole("region", { name: "Interrupted table action" })).toBeNull()
  );
});

// A collapsed group keeps its heading however few of its rows the page holds: the heading is
// the only way back to them.
test("a collapsed group keeps its heading and reopens", async () => {
  const user = userEvent.setup();
  table(
    [
      issue("CORE-1", { status: "backlog", title: "Alpha" }),
      issue("CORE-2", { status: "todo", title: "Bravo" }),
    ],
    "?group=status"
  );
  await screen.findByRole("row", { name: "CORE-2 Bravo" });
  const todo = screen.getByRole("button", { name: /Todo.*1 issue/ });
  await user.click(todo);
  await waitFor(() => expect(screen.queryByRole("row", { name: "CORE-2 Bravo" })).toBeNull());
  // The heading is still there, and still reads the group's own count.
  const collapsed = screen.getByRole("button", { name: /Todo.*1 issue/ });
  expect(collapsed.textContent).not.toContain("0 of");
  await user.click(collapsed);
  expect(await screen.findByRole("row", { name: "CORE-2 Bravo" })).toBeDefined();
});

/** The issue keys the table is listing, in the order the rows stand in. */
function listedKeys(): string[] {
  return screen
    .getAllByRole("row")
    .filter((row) => row.hasAttribute("data-issue-row"))
    .map((row) => row.getAttribute("data-issue-row") ?? "");
}

test("a scoped table lists the issues the server answers for the priority, over every project", async () => {
  const { listing } = table(
    [
      issue("CORE-1"),
      issue("CORE-2", { advances: [{ id: "PRI-7", inherited: false }] }),
      issue("OPS-3", { advances: [{ id: "PRI-7", inherited: true }] }),
    ],
    "",
    undefined,
    // What both callers pass: the priority's own listing, no project and the priorities' titles,
    // under the prefix the page keeps this table's view in (`priorities/PriorityPage.tsx`).
    {
      listing: { advances: "PRI-7" },
      priorityTitles: new Map([["PRI-7", "Ship it"]]),
      stateKey: "w",
    }
  );
  await screen.findByRole("row", { name: "CORE-2 Work CORE-2" });
  expect(listedKeys().sort()).toEqual(["CORE-2", "OPS-3"]);
  expect(screen.queryByRole("row", { name: "CORE-1 Work CORE-1" })).toBeNull();
  // The narrowing is the server's, and it names no project: a priority belongs to none.
  expect(listing).toHaveBeenCalledWith({ advances: ["PRI-7"], open: true });
  expect(listing.mock.calls.every(([options]) => options?.project === undefined)).toBe(true);
  // The table is named for what it lists rather than for a project it does not stand on.
  expect(screen.getByRole("table", { name: "Priority issues table" })).toBeDefined();
  expect(screen.queryByRole("table", { name: "Project issues table" })).toBeNull();
  // One stored layout for every scoped table, under no key that names a priority: the work
  // advancing a priority is one table, drawn in two places. It has Project and Advances, both
  // hidden until the reader shows them, and no Moved there or Reason, since its listing asks for
  // no move.
  expect(localStorage.getItem("dispatch.project-table.priority-work")).toBe(
    '{"sizing":{},"order":[],"visibility":{"updated":false,"closed":false,"project":false,"advances":false}}'
  );
  expect(localStorage.getItem("dispatch.project-table.priority:PRI-7")).toBeNull();
});

test("a stateKey puts the table's own view under its prefix, and none keeps the bare keys", async () => {
  const user = userEvent.setup();
  const { view } = table([issue("CORE-1", { title: "Sextant" })], "", undefined, {
    stateKey: "advances",
  });
  await user.type(await screen.findByRole("searchbox", { name: "Search issues" }), "sextant");
  await waitFor(() =>
    expect(screen.getByTestId("route-search").textContent).toContain("advances.q=sextant")
  );
  expect(screen.getByTestId("route-search").textContent).not.toContain("&q=");
  view.unmount();
  // The project page passes no stateKey, so the address it publishes is the bare one.
  table([issue("CORE-2", { title: "Astrolabe" })]);
  await user.type(await screen.findByRole("searchbox", { name: "Search issues" }), "astrolabe");
  await waitFor(() =>
    expect(screen.getByTestId("route-search").textContent).toContain("q=astrolabe")
  );
  expect(screen.getByTestId("route-search").textContent).not.toContain("advances.");
});

test("the narrowing chip shows the picked issues alone, and its remove gives the table back", async () => {
  const user = userEvent.setup();
  table(
    [issue("CORE-1"), issue("CORE-2"), issue("CORE-3")],
    "?only=CORE-1,CORE-3&onlyLabel=Finished+last+week&nofilter=1"
  );
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  expect(listedKeys().sort()).toEqual(["CORE-1", "CORE-3"]);
  // Named as every other chip of the row is, and wearing the selected fill they wear.
  const chip = screen.getByRole("button", { name: "Remove Showing: Finished last week filter" });
  expect(chip).toHaveAttribute("aria-pressed", "true");
  expect(chip.textContent).toContain("Finished last week");
  expect(chip.textContent).toContain("2 of 2 issues");
  await user.click(chip);
  await waitFor(() => expect(listedKeys().sort()).toEqual(["CORE-1", "CORE-2", "CORE-3"]));
  expect(screen.getByTestId("route-search").textContent).not.toContain("only=");
  expect(screen.queryByRole("button", { name: /^Remove Showing: / })).toBeNull();
});

// The toolbar is drawn while the issues load and after their read fails, so the chip counts what
// it shows only once the table holds every row it waits on: a count of the empty listing, or of
// the open issues alone while the closed ones are out, would read as picked issues that are not
// there.
test("the narrowing chip counts the picked issues only once the table has loaded them", async () => {
  const picked = "?only=CORE-1,CORE-3&onlyLabel=Finished+last+week&nofilter=1";
  const count = () => {
    const chip = screen.getByRole("button", { name: "Remove Showing: Finished last week filter" });
    expect(chip.textContent).toContain("Showing: Finished last week");
    return chip.textContent?.match(/\d+ of \d+ issues?/)?.[0];
  };
  const open = Promise.withResolvers<IssueSummary[]>();
  const full = Promise.withResolvers<IssueSummary[]>();
  const { view } = table([], picked, { full: full.promise, open: open.promise });
  expect(await screen.findByRole("status", { name: "Loading issues" })).toBeDefined();
  expect(count()).toBeUndefined();
  // The open issues are in and the closed ones still out. One picked issue is closed, so a count
  // now would say one of two.
  open.resolve([issue("CORE-1"), issue("CORE-2")]);
  await screen.findByText("Loading closed issues…");
  expect(count()).toBeUndefined();
  full.resolve([issue("CORE-1"), issue("CORE-2"), issue("CORE-3", { status: "done" })]);
  await waitFor(() => expect(count()).toBe("2 of 2 issues"));
  view.unmount();

  // A read that fails leaves the chip naming its narrowing beside the error, with no count.
  const failing = Promise.withResolvers<IssueSummary[]>();
  table([], picked, {
    full: Promise.withResolvers<IssueSummary[]>().promise,
    open: failing.promise,
  });
  failing.reject(new Error("the listing is down"));
  expect(await screen.findByRole("button", { name: "Retry loading issues" })).toBeDefined();
  expect(count()).toBeUndefined();
});

// R1, the architect's chip rule: every narrowing that is on is visible and removable from the
// control row, whatever set it. A column filter is set on that column's header, and the reader
// may hide the column with the columns menu - which leaves the filter narrowing the list with no
// header to show it - so the row draws a chip for it either way.
test("a column filter is a chip of the row, counted by the trigger, while its column is hidden", async () => {
  const user = userEvent.setup();
  table(
    [
      issue("CORE-1", { labels: ["frontend"] }),
      issue("CORE-2", { labels: ["backend"] }),
      issue("CORE-3", { labels: ["frontend"] }),
    ],
    "?label=frontend"
  );
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  expect(listedKeys().sort()).toEqual(["CORE-1", "CORE-3"]);
  // One chip for the one narrowing, in the words the Board gives `?label=` too, since both
  // views read that parameter.
  const chip = () => screen.getByRole("button", { name: "Remove Label: frontend filter" });
  expect(screen.getAllByRole("button", { name: /^Remove .* filter$/ }).length).toBe(1);
  expect(chip()).toBeDefined();
  expect(screen.getByRole("button", { name: "Filters · 1" })).toBeDefined();

  // Hidden, the column takes its header and its filter control off the page; the chip is then
  // the one place the narrowing shows, and it still takes it off.
  await user.click(screen.getByRole("button", { name: "Columns" }));
  await user.click(screen.getByRole("checkbox", { name: "Labels" }));
  await user.keyboard("{Escape}");
  await waitFor(() => expect(screen.queryByRole("button", { name: /^Filter Labels/ })).toBeNull());
  expect(listedKeys().sort()).toEqual(["CORE-1", "CORE-3"]);
  expect(screen.getByRole("button", { name: "Filters · 1" })).toBeDefined();
  await user.click(chip());
  await waitFor(() => expect(listedKeys().sort()).toEqual(["CORE-1", "CORE-2", "CORE-3"]));
  expect(screen.getByTestId("route-search").textContent).not.toContain("label=");
  expect(screen.getByRole("button", { name: "Filters · 0" })).toBeDefined();
});

// The statuses the List opens with are no narrowing of the reader's: a link leaves them out and
// Clear filters goes back to them, so they draw no chip. A status the reader picked does, and
// taking that chip off goes back to the open statuses rather than asking the server for every
// closed issue in the project.
test("the open statuses draw no chip, and taking a Status chip off goes back to them", async () => {
  const user = userEvent.setup();
  const { listing } = table([issue("CORE-1"), issue("CORE-2", { status: "done" })], "?status=done");
  await screen.findByRole("row", { name: "CORE-2 Work CORE-2" });
  const chip = screen.getByRole("button", { name: "Remove Status: Done filter" });
  await user.click(chip);
  await waitFor(() =>
    expect(screen.getByTestId("route-search").textContent).not.toContain("status=")
  );
  expect(screen.getByTestId("route-search").textContent).not.toContain("nofilter");
  expect(screen.queryByRole("button", { name: /^Remove Status: / })).toBeNull();
  expect(listing.mock.calls.every(([options]) => options?.open !== false)).toBe(true);
});

// The listing columns exist only where they mean something: a project's own table would name its
// own project on every row, it is given no priority's title, and its listing carries no move, so
// it neither shows nor offers any of the four.
test("a project's table offers none of the listing columns", async () => {
  const user = userEvent.setup();
  table([issue("CORE-1")]);
  const list = await screen.findByRole("table", { name: "Project issues table" });
  await within(list).findByRole("row", { name: "CORE-1 Work CORE-1" });
  const headers = within(list)
    .getAllByRole("columnheader")
    .map((header) => header.textContent ?? "");
  for (const name of ["Project", "Advances", "Moved there", "Reason"])
    expect(headers.some((text) => text.startsWith(name))).toBe(false);
  // Nor does it show Updated, which the fault lists open with shown: a project's table opens
  // with it hidden, as it always has.
  expect(headers.some((text) => text.startsWith("Updated"))).toBe(false);
  await user.click(screen.getByRole("button", { name: "Columns" }));
  expect(screen.getByRole("checkbox", { name: "Updated" })).not.toBeChecked();
  for (const name of ["Project", "Advances", "Moved there", "Reason"])
    expect(screen.queryByRole("checkbox", { name })).toBeNull();
});

// The work under one priority is every project's, so its table offers the Project column; given
// the priorities' titles it offers Advances, which names each by its title; and its listing asks
// for no move, so it never offers Moved there, nor Reason, which says why a move was made. Both
// open hidden, for the reader to show.
test("a priority's work table offers Project and, given the titles, Advances, and never Moved there or Reason", async () => {
  const user = userEvent.setup();
  table([issue("CORE-1", { advances: [{ id: "ship-it", inherited: false }] })], "", undefined, {
    listing: { advances: "ship-it" },
    priorityTitles: new Map([["ship-it", "Ship it everywhere"]]),
    stateKey: "w",
  });
  const list = await screen.findByRole("table", { name: "Priority issues table" });
  await within(list).findByRole("row", { name: "CORE-1 Work CORE-1" });
  const headers = () =>
    within(list)
      .getAllByRole("columnheader")
      .map((header) => header.textContent ?? "");
  await user.click(screen.getByRole("button", { name: "Columns" }));
  for (const name of ["Project", "Advances"])
    expect(screen.getByRole("checkbox", { name })).not.toBeChecked();
  for (const name of ["Moved there", "Reason"])
    expect(screen.queryByRole("checkbox", { name })).toBeNull();
  await user.click(screen.getByRole("checkbox", { name: "Project" }));
  await user.click(screen.getByRole("checkbox", { name: "Advances" }));
  await waitFor(() => expect(headers()).toContain("Advances"));
  for (const name of ["Moved there", "Reason"])
    expect(headers().some((text) => text.startsWith(name))).toBe(false);
  // A listing column names a row's value and does not sort, filter or group by it: the
  // address keeps only the registry's columns (`issues-table-model.ts`).
  expect(within(list).queryByRole("button", { name: "Filter Project" })).toBeNull();
  const row = within(list).getByRole("row", { name: "CORE-1 Work CORE-1" });
  const cells = within(row).getAllByRole("cell");
  expect(cells[headers().indexOf("Project")]?.textContent).toBe("CORE");
  expect(
    within(cells[headers().indexOf("Advances")] as HTMLElement)
      .getByRole("link", { name: "Ship it everywhere" })
      .getAttribute("href")
  ).toBe("/priorities/ship-it");
});

// Without the titles a priority's work table has no Advances column: it could name each
// priority only by its id, which is no name to a reader.
test("a priority's work table given no titles offers no Advances column", async () => {
  const user = userEvent.setup();
  table([issue("CORE-1", { advances: [{ id: "ship-it", inherited: false }] })], "", undefined, {
    listing: { advances: "ship-it" },
    stateKey: "w",
  });
  const list = await screen.findByRole("table", { name: "Priority issues table" });
  await within(list).findByRole("row", { name: "CORE-1 Work CORE-1" });
  await user.click(screen.getByRole("button", { name: "Columns" }));
  expect(screen.getByRole("checkbox", { name: "Project" })).toBeDefined();
  for (const name of ["Advances", "Moved there", "Reason"])
    expect(screen.queryByRole("checkbox", { name })).toBeNull();
});

// A fault list names the statuses it holds, and the server answers every row of it in one read:
// even a Status filter that picks Done reads no closed issue beside it.
test("a listing that names its statuses is read with them alone, never with every closed issue", async () => {
  const { listing } = table([issue("CORE-1")], "?k.status=done", undefined, {
    listing: { advances: "any", statuses: ["backlog", "icebox"], moved: true },
    stateKey: "k",
  });
  await screen.findByRole("table", { name: "Priority issues table" });
  expect(listing).toHaveBeenCalledWith({
    advances: ["any"],
    status: ["backlog", "icebox"],
    moved: true,
    open: true,
  });
  expect(listing.mock.calls.every(([options]) => options?.status !== undefined)).toBe(true);
  expect(screen.queryByText("Loading closed issues…")).toBeNull();
});

// The Issues tab's views: the reads each one makes, the tab counts, the daemon setting, the
// facts of the done rows, Save view and Clear filters.

/** The address the page is on, as the route shows it. */
const routeSearch = () => screen.getByTestId("route-search").textContent ?? "";

/** Open, Done, and the rows each lists: two open issues and two closed ones. */
const VIEW_ROWS = (): IssueSummary[] => [
  issue("CORE-1", { labels: ["legion"] }),
  issue("CORE-2", { status: "in_progress" }),
  issue("CORE-3", { status: "done", closed_at: "2026-11-10T10:00:00Z" }),
  issue("CORE-5", { status: "done", closed_at: "2026-11-11T10:00:00Z" }),
];

/** Every request of the project's listing, page reads included, in one order. */
function listingReads(
  listing: ReturnType<typeof table>["listing"],
  page: ReturnType<typeof table>["page"]
): string[] {
  return [
    ...listing.mock.calls.map(([options]) => `list ${JSON.stringify(options)}`),
    ...page.mock.calls.map(([options]) => `page ${JSON.stringify(options)}`),
  ].sort();
}

const OPEN_COUNT_READ = `page ${JSON.stringify({ project: "CORE", open: true, limit: 1 })}`;

// R5: no view reads the full listing. Open reads the open listing alone; Done reads the page on
// screen while it is sorted by Closed and narrowed by nothing but a Closed window, and every done
// issue its window selects otherwise; a view with no status picked reads the open listing and the
// done issues. On Done, Open's count is the total of a one-row page of the open listing.
test.each([
  ["Open", "", [`list ${JSON.stringify({ project: "CORE", open: true })}`]],
  [
    "Done",
    "?view=done",
    [
      `page ${JSON.stringify({ project: "CORE", status: ["done"], sort: "-closed_at", limit: 50, offset: 0 })}`,
      OPEN_COUNT_READ,
    ],
  ],
  [
    "Done's third page of 25 rows",
    "?view=done&size=25&page=3",
    [
      `page ${JSON.stringify({ project: "CORE", status: ["done"], sort: "-closed_at", limit: 25, offset: 50 })}`,
      OPEN_COUNT_READ,
    ],
  ],
  [
    "Done in a Closed window",
    "?view=done&f.closed=2026-11-03..2026-11-12",
    [
      `page ${JSON.stringify({ project: "CORE", status: ["done"], sort: "-closed_at", limit: 50, offset: 0, closed_since: "2026-11-03T00:00:00.000Z", closed_before: "2026-11-12T00:00:00.000Z" })}`,
      OPEN_COUNT_READ,
    ],
  ],
  [
    "Done with a search",
    "?view=done&q=work",
    [`list ${JSON.stringify({ project: "CORE", status: ["done"] })}`, OPEN_COUNT_READ],
  ],
  [
    "Done in a Closed window, sorted by Key",
    "?view=done&sort=key&f.closed=2026-11-03..2026-11-12",
    [
      `list ${JSON.stringify({ project: "CORE", status: ["done"], closed_since: "2026-11-03T00:00:00.000Z", closed_before: "2026-11-12T00:00:00.000Z" })}`,
      OPEN_COUNT_READ,
    ],
  ],
  [
    "Done grouped by Assignee",
    "?view=done&group=assignee",
    [`list ${JSON.stringify({ project: "CORE", status: ["done"] })}`, OPEN_COUNT_READ],
  ],
  [
    "Done with every row on one page",
    `?view=done&size=${ALL_ROWS}`,
    [`list ${JSON.stringify({ project: "CORE", status: ["done"] })}`, OPEN_COUNT_READ],
  ],
  [
    "a Status filter with no pick",
    "?nofilter=1",
    [
      `list ${JSON.stringify({ project: "CORE", open: true })}`,
      `list ${JSON.stringify({ project: "CORE", status: ["done"] })}`,
    ],
  ],
  [
    "a Status filter of Todo and Done",
    "?status=todo&status=done",
    [
      `list ${JSON.stringify({ project: "CORE", open: true })}`,
      `list ${JSON.stringify({ project: "CORE", status: ["done"] })}`,
    ],
  ],
])("%s makes its own reads and never reads the full listing", async (_name, search, reads) => {
  // Enough closed issues for a third page of 25, so no page asked for is past the last one.
  const closed = Array.from({ length: 60 }, (_, index) =>
    issue(`CORE-${index + 10}`, { status: "done", closed_at: "2026-11-04T10:00:00Z" })
  );
  const { listing, page } = table([...VIEW_ROWS(), ...closed], search);
  await screen.findByRole("table", { name: "Project issues table" });
  await waitFor(() => expect(listingReads(listing, page)).toEqual([...reads].sort()));
  // Every read names `open` or `status`: none is the project's full listing.
  for (const [options] of [...listing.mock.calls, ...page.mock.calls])
    expect(options?.open === true || options?.status !== undefined).toBe(true);
});

/** The saved views alice keeps for CORE: Open narrowed to Todo, Done, and every status. */
function keepSavedViews(views: readonly { name: string; query: string }[]) {
  localStorage.setItem(
    "dispatch.project.saved-views:alice",
    JSON.stringify({ version: 1, projects: { CORE: views } })
  );
}

/** alice's saved views of CORE, as stored. */
const savedViews = (): { name: string; query: string }[] =>
  JSON.parse(localStorage.getItem("dispatch.project.saved-views:alice") ?? "{}").projects?.CORE ??
  [];

// R2: each tab counts its view where the page can. Open counts from the open listing, and on
// Done from the total of a one-row page of it; Done counts once it has been opened; a saved view
// counts from the open listing, so on Done it has no count, and one that lists closed issues
// counts only while it is open. The open tab counts the rows the table lists now.
test("the tabs count Open, Done once opened, and the saved views the open listing can count", async () => {
  const user = userEvent.setup();
  // Each query as Save view writes one, with its columns and its layout.
  const drawn = "&cols=key,title,status&layout=table";
  keepSavedViews([
    { name: "Todo", query: `status=todo${drawn}` },
    { name: "Closed", query: `view=done${drawn}` },
    { name: "Everything", query: `nofilter=1${drawn}` },
  ]);
  table(VIEW_ROWS());
  const tab = (name: string) => screen.findByRole("tab", { name });
  expect(await tab("Open, 2 issues")).toHaveAttribute("aria-selected", "true");
  expect(await tab("Done")).toBeDefined();
  expect(await tab("Todo, 1 issue")).toBeDefined();
  expect(await tab("Closed")).toBeDefined();
  expect(await tab("Everything")).toBeDefined();

  // The reader's own search narrows the open tab's count, and marks the view changed.
  await user.type(screen.getByRole("searchbox", { name: "Search issues" }), "CORE-2");
  expect(await tab("Open, 1 issue, changed")).toBeDefined();
  await user.clear(screen.getByRole("searchbox", { name: "Search issues" }));
  expect(await tab("Open, 2 issues")).toBeDefined();

  // Done reads no open row: Open counts by the open listing's total, and no saved view counts.
  await user.click(await tab("Done"));
  expect(await tab("Done, 2 issues")).toHaveAttribute("aria-selected", "true");
  expect(await tab("Open, 2 issues")).toBeDefined();
  expect(await tab("Todo")).toBeDefined();

  // Back on Open, Done keeps the count of its own read.
  await user.click(await tab("Open, 2 issues"));
  expect(await tab("Open, 2 issues")).toHaveAttribute("aria-selected", "true");
  expect(await tab("Done, 2 issues")).toBeDefined();
  expect(await tab("Todo, 1 issue")).toBeDefined();

  // A saved view that lists closed issues counts while it is the open tab.
  await user.click(await tab("Everything"));
  expect(await tab("Everything, 4 issues")).toHaveAttribute("aria-selected", "true");
  await user.click(await tab("Closed"));
  expect(await tab("Closed, 2 issues")).toHaveAttribute("aria-selected", "true");
  expect(new URLSearchParams(routeSearch()).toString()).toBe(
    new URLSearchParams(`view=done${drawn}`).toString()
  );
});

// An older link in today's spelling: `view=queue` opens Open with the Queue filter, written over
// the address the page opened on, and the tab marks the change Clear filters takes back.
test("an older view address is written over in today's spelling, and Clear filters takes its narrowing off", async () => {
  const user = userEvent.setup();
  table(VIEW_ROWS(), "?view=queue");
  await waitFor(() => expect(routeSearch()).toBe("?f.queue=Queued"));
  expect(await screen.findByRole("tab", { name: "Open, 1 issue, changed" })).toBeDefined();
  await user.click(screen.getByRole("button", { name: "Clear filters" }));
  await waitFor(() => expect(routeSearch()).toBe(""));
  expect(await screen.findByRole("tab", { name: "Open, 2 issues" })).toBeDefined();
});

// A press on the tab of the view the reader changed opens the view as it is its own: its sort,
// its filters and its search all go back.
test("a press on the changed tab gives the view back", async () => {
  const user = userEvent.setup();
  table(VIEW_ROWS(), "?q=CORE-2&sort=key&f.priority=unset");
  const changed = await screen.findByRole("tab", { name: /^Open, .*changed$/ });
  await user.click(changed);
  await waitFor(() => expect(routeSearch()).toBe(""));
  expect(await screen.findByRole("tab", { name: "Open, 2 issues" })).toBeDefined();
});

// Clear filters shows while the narrowings differ from the view's own and takes them back to
// it, leaving the sort, the grouping and the page size as the reader set them.
test("Clear filters shows while the view is narrowed and gives its own narrowings back", async () => {
  const user = userEvent.setup();
  const { view } = table(VIEW_ROWS(), "?sort=key");
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  // Sorted but not narrowed: the view is changed, so Save view shows, and Clear filters does not.
  expect(screen.getByRole("button", { name: "Save view" })).toBeDefined();
  expect(screen.queryByRole("button", { name: "Clear filters" })).toBeNull();
  await user.type(screen.getByRole("searchbox", { name: "Search issues" }), "CORE-2");
  await user.click(await screen.findByRole("button", { name: "Clear filters" }));
  await waitFor(() => expect(routeSearch()).toBe("?sort=key"));
  expect(screen.queryByRole("button", { name: "Clear filters" })).toBeNull();
  view.unmount();

  // Done's own narrowing is its own: a Closed window is the reader's, and goes.
  table(VIEW_ROWS(), "?view=done&f.closed=7d&size=25");
  await user.click(await screen.findByRole("button", { name: "Clear filters" }));
  await waitFor(() => expect(routeSearch()).toBe("?view=done&size=25"));
});

// R3: Save view saves the view under a name of at most 14 characters, unique in the project and
// none of the built-in views' names; a name in use asks to replace that view first. The saved
// view is a tab after the built-in ones, and its delete button takes it away.
test("Save view checks the name, saves the view as a tab, replaces it on asking, and its tab deletes it", async () => {
  const user = userEvent.setup();
  table(VIEW_ROWS());
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  // A view as it opens is no change to save.
  expect(screen.queryByRole("button", { name: "Save view" })).toBeNull();
  await user.type(screen.getByRole("searchbox", { name: "Search issues" }), "CORE-1");
  const saveView = await screen.findByRole("button", { name: "Save view" });
  const named = async (name: string) => {
    const box = screen.getByRole("textbox", { name: "Name of the view" });
    await user.clear(box);
    if (name !== "") await user.type(box, name);
    await user.click(screen.getByRole("button", { name: "Save" }));
  };

  await user.click(saveView);
  await named("Done");
  expect(screen.getByRole("alert").textContent).toBe(
    "Done is a built-in view. Type a different name."
  );
  await named("A view over the limit");
  expect(screen.getByRole("alert").textContent).toBe("Use 14 characters or fewer.");
  await named("");
  expect(screen.getByRole("alert").textContent).toBe("Type a name.");
  await named("Mine");
  expect(await screen.findByRole("tab", { name: "Mine, 1 issue" })).toHaveAttribute(
    "aria-selected",
    "true"
  );
  const [mine] = savedViews();
  expect(mine?.name).toBe("Mine");
  const query = new URLSearchParams(mine?.query);
  expect(query.get("q")).toBe("CORE-1");
  expect(query.get("layout")).toBe("table");
  // `cols=` names the whole set of columns shown: Open's own in a project with no daemon.
  expect(query.get("cols")?.split(",").sort()).toEqual([
    "asks",
    "assignee",
    "author",
    "claim",
    "created",
    "key",
    "labels",
    "priority",
    "status",
    "title",
  ]);
  // The address is the saved view's query, so its tab shows no mark.
  expect(routeSearch()).toBe(`?${mine?.query}`);

  // Changed, the saved view's tab shows its mark; saving it under its own name asks first.
  await user.clear(screen.getByRole("searchbox", { name: "Search issues" }));
  expect(await screen.findByRole("tab", { name: "Mine, 2 issues, changed" })).toBeDefined();
  await user.click(screen.getByRole("button", { name: "Save view" }));
  expect(screen.getByRole("textbox", { name: "Name of the view" })).toHaveValue("Mine");
  await user.click(screen.getByRole("button", { name: "Save" }));
  expect(screen.getByRole("alert").textContent).toBe("A saved view has the name Mine. Replace it?");
  expect(savedViews()[0]?.query).toBe(mine?.query);
  await user.click(screen.getByRole("button", { name: "Replace" }));
  await waitFor(() => expect(new URLSearchParams(savedViews()[0]?.query).has("q")).toBe(false));
  expect(savedViews()).toHaveLength(1);
  expect(await screen.findByRole("tab", { name: "Mine, 2 issues" })).toBeDefined();

  const remove = screen.getByRole("button", { name: "Delete saved view Mine" });
  expect(remove).toHaveAttribute("title", "Delete this saved view");
  await user.click(remove);
  await waitFor(() => expect(screen.queryByRole("tab", { name: /^Mine/ })).toBeNull());
  expect(savedViews()).toEqual([]);
});

/** A project's daemon setting that names a daemon. */
const DAEMON: LegionSetting = {
  created_by: { kind: "user", id: "alice" },
  project: "CORE",
  state_url: "http://legion.test",
  updated_at: "2026-11-01T00:00:00Z",
};

/** The board route's answer: the facts its `issues` map holds, and its header. */
function legionBoard(
  issues: Record<string, LegionBoardFacts> = {},
  header: Partial<LegionBoardResponse["header"]> = {}
): LegionBoardResponse {
  return {
    columns: null,
    computed_at: "2026-11-17T08:00:00Z",
    header: {
      facts_cursor_age_seconds: null,
      facts_updated_at: null,
      queue_length: 0,
      read_at: "2026-11-17T08:00:00Z",
      read_error: null,
      read_ok: true,
      slot_cap: 4,
      slots_in_use: 1,
      trees: 1,
      ...header,
    },
    issues,
    project: "CORE",
    settings: null,
  };
}

/** The facts the board's map holds for an issue. */
function boardFacts(workHours: number): LegionBoardFacts {
  return {
    as_of: "2026-11-17T08:00:00Z",
    closed_at: "2026-11-10T10:00:00Z",
    growth: { hours_by_phase: {}, slot_hours: 0, work_hours: 0, work_last_at: 0 },
    hours_by_phase: { implementing: workHours },
    opened_at: "2026-11-01T00:00:00Z",
    review_rounds: 1,
    root_key: "CORE-3",
    slot_hours: workHours,
    tree_depth: 0,
    updated_at: "2026-11-17T08:00:00Z",
    waiting_reason: "",
    work_first_at: "2026-11-02T00:00:00Z",
    work_hours: workHours,
    work_last_at: "2026-11-10T00:00:00Z",
  };
}

/** The facts the board's map holds for an open issue at work: 1 hour of work and 1 hour in
 *  Implementing at `asOf`, each of which grows an hour an hour from then. */
function openFacts(asOf: string): LegionBoardFacts {
  return {
    ...boardFacts(1),
    as_of: asOf,
    closed_at: null,
    growth: { hours_by_phase: { implementing: 1 }, slot_hours: 1, work_hours: 1, work_last_at: 1 },
    work_last_at: asOf,
  };
}

/** A closed issue's row of `legion-work-time`'s items. */
function factsRow(key: string, closedAt: string, workHours: number): EvalItemRow {
  return {
    cols: {
      closed_at: closedAt,
      hours_implementing: workHours,
      review_rounds: 1,
      root_key: key,
      tree_depth: 0,
      work_first_at: closedAt,
      work_hours: workHours,
    },
    href: `/issues/${key}`,
    links: [],
    measured_at: closedAt,
    range: "window",
    row_key: key,
    row_kind: "issue",
    source: "legionfacts",
  };
}

/** The items route, answering what `answer` gives at each read: the rows, or a refusal. */
function answerItems(answer: () => Promise<readonly EvalItemRow[]>) {
  const items = spyOn(api, "listEvalItems").mockImplementation(async (_project, _slug, query) => {
    const rows = await answer();
    const offset = query.offset ?? 0;
    return {
      compare: null,
      items: rows.slice(offset),
      limit: 200,
      offset,
      total: rows.length,
      window: { days: 1, from: String(query.from), to: String(query.to) },
    };
  });
  restores.push(() => items.mockRestore());
  return items;
}

/** The live sockets the page opens, for the rest of the test. */
function liveSockets(): FakeLiveSockets {
  const sockets = installFakeLiveSockets();
  restores.push(() => sockets.restore());
  return sockets;
}

/** Dispatch answers the page's board subscription with `board`, or refuses it. */
async function answerBoard(
  sockets: FakeLiveSockets,
  answer: LegionBoardResponse | { readonly code: string; readonly message: string }
): Promise<void> {
  await waitFor(() => expect(sockets.sockets.length).toBeGreaterThan(0));
  const socket = sockets.latest();
  if (socket.readyState !== 1) act(() => socket.open());
  const { sub } = await waitFor(() => socket.subscribed("legion"));
  act(() =>
    socket.receive(
      "code" in answer ? errorFrame(sub, answer.code, answer.message) : snapshotFrame(sub, answer)
    )
  );
}

// While the daemon setting's first read is out, the page shows its loading state and reads no
// listing: what it reads and draws turns on the answer.
test("while the daemon setting is read, the tab shows its loading state and reads nothing more", async () => {
  const { listing, page, setting } = table(
    VIEW_ROWS(),
    "",
    {},
    {},
    Promise.withResolvers<LegionSetting | null>().promise
  );
  expect(await screen.findByRole("status", { name: "Loading issues" })).toBeDefined();
  expect(setting).toHaveBeenCalledWith("CORE");
  expect(screen.queryByRole("tablist", { name: "Views" })).toBeNull();
  expect(screen.queryByRole("table")).toBeNull();
  expect(listing).not.toHaveBeenCalled();
  expect(page).not.toHaveBeenCalled();
});

// A failed read of the daemon setting is an alert under the tabs with its error and Retry, and
// until a read answers the project is drawn as one with no daemon: no board-fed column, no
// Legion phase in Group by, no board read, and `f.queue=Queued` lists the Todo issues labelled
// `legion`.
test("a failed daemon setting read names its error with Retry, and the project is drawn with no daemon until it answers", async () => {
  const user = userEvent.setup();
  const sockets = liveSockets();
  const { setting } = table(
    [...VIEW_ROWS(), issue("CORE-4", { status: "in_progress", labels: ["legion"] })],
    "?f.queue=Queued",
    {},
    {},
    Promise.reject(new ApiError(503, { code: "UNAVAILABLE", error: "The store did not answer." }))
  );
  const alert = await screen.findByText(/^Could not read the Legion daemon setting/);
  expect(alert.textContent).toBe(
    "Could not read the Legion daemon setting: The store did not answer. Retry"
  );
  expect(listedKeys()).toEqual(["CORE-1"]);
  const headers = () =>
    screen.getAllByRole("columnheader").map((header) => header.textContent ?? "");
  expect(headers().some((text) => text.startsWith("Legion phase"))).toBe(false);
  await user.click(screen.getByRole("button", { name: "Group by" }));
  expect(screen.queryByRole("radio", { name: "Legion phase" })).toBeNull();
  await user.keyboard("{Escape}");
  expect(sockets.sockets).toEqual([]);

  setting.mockImplementation(async () => DAEMON);
  await user.click(screen.getByRole("button", { name: "Retry reading the Legion daemon setting" }));
  await waitFor(() =>
    expect(screen.queryByText(/^Could not read the Legion daemon setting/)).toBeNull()
  );
  await answerBoard(sockets, legionBoard());
  await waitFor(() => expect(headers().some((text) => text.startsWith("Legion phase"))).toBe(true));
});

// A project whose setting names a daemon holds the board, draws its Legion columns and its
// Legion phase grouping, and says under the tabs when the daemon's last read failed. A later
// failed read of the setting keeps the page as the answered read drew it.
test("a daemon setting that names a daemon draws the Legion columns, says a failed daemon read, and outlasts a later failed read", async () => {
  const user = userEvent.setup();
  const sockets = liveSockets();
  const { client, setting } = table(VIEW_ROWS(), "", {}, {}, Promise.resolve(DAEMON));
  await answerBoard(
    sockets,
    legionBoard({}, { read_ok: false, read_error: "dial tcp 10.0.0.9:7400: connection refused" })
  );
  expect(sockets.latest().subscribed("legion").view).toEqual({ kind: "legion", project: "CORE" });
  expect(
    await screen.findByText(
      "The last read of the Legion daemon failed: dial tcp 10.0.0.9:7400: connection refused"
    )
  ).toBeDefined();
  const legionPhase = () =>
    screen
      .getAllByRole("columnheader")
      .some((header) => (header.textContent ?? "").startsWith("Legion phase"));
  await waitFor(() => expect(legionPhase()).toBe(true));
  await user.click(screen.getByRole("button", { name: "Group by" }));
  expect(screen.getByRole("radio", { name: "Legion phase" })).toBeDefined();
  await user.keyboard("{Escape}");

  setting.mockImplementation(() => Promise.reject(new ApiError(503, { error: "down" })));
  await act(() => client.invalidateQueries({ queryKey: ["legion-daemon", "CORE"] }));
  await waitFor(() => expect(setting).toHaveBeenCalledTimes(2));
  expect(legionPhase()).toBe(true);
  expect(screen.queryByText(/^Could not read the Legion daemon setting/)).toBeNull();
});

// A view whose filter reads facts shows the table's loading state until the board and the items
// of its done rows answer, and an error with Retry when the items read fails: never an empty
// list, or a count, from a read that has not answered.
test("a view that filters by a fact waits for the facts, and a failed read of them is an error with Retry", async () => {
  const user = userEvent.setup();
  const sockets = liveSockets();
  let answer = Promise.withResolvers<readonly EvalItemRow[]>();
  const items = answerItems(() => answer.promise);
  table(VIEW_ROWS(), "?view=done&f.work_hours=over-7d", {}, {}, Promise.resolve(DAEMON));
  expect(await screen.findByRole("status", { name: "Loading issues" })).toBeDefined();
  expect(await screen.findByRole("tab", { name: "Done, changed" })).toBeDefined();
  await answerBoard(sockets, legionBoard());
  await waitFor(() => expect(items).toHaveBeenCalled());
  expect(screen.getByRole("status", { name: "Loading issues" })).toBeDefined();
  expect(screen.queryByText("No issues match these filters.")).toBeNull();

  answer.reject(new ApiError(404, { code: "EVAL_NOT_FOUND", error: "No eval legion-work-time." }));
  expect(
    await screen.findByText("Could not read the Legion columns, which this view needs.")
  ).toBeDefined();
  expect(
    screen.getByText(
      "Could not read the Legion columns of the closed issues: No eval legion-work-time."
    )
  ).toBeDefined();
  expect(screen.getByRole("tab", { name: "Done, changed" })).toBeDefined();

  answer = Promise.withResolvers<readonly EvalItemRow[]>();
  answer.resolve([factsRow("CORE-5", "2026-11-11T10:00:00Z", 200)]);
  await user.click(screen.getByRole("button", { name: "Retry" }));
  await screen.findByRole("row", { name: "CORE-5 Work CORE-5" });
  expect(listedKeys()).toEqual(["CORE-5"]);
  expect(screen.getByRole("tab", { name: "Done, 1 issue, changed" })).toBeDefined();
});

// A view with no facts filter draws its rows before the facts answer; a failed items read marks
// each facts cell of a done row with the error mark, never the empty cell of no Legion work.
test("a failed items read marks the done rows' facts cells and names the error under the tabs", async () => {
  const sockets = liveSockets();
  answerItems(() =>
    Promise.reject(new ApiError(500, { error: "The facts store did not answer." }))
  );
  table(VIEW_ROWS(), "?view=done", {}, {}, Promise.resolve(DAEMON));
  await screen.findByRole("row", { name: "CORE-5 Work CORE-5" });
  await answerBoard(sockets, legionBoard());
  expect(
    await screen.findByText(
      "Could not read the Legion columns of the closed issues: The facts store did not answer."
    )
  ).toBeDefined();
  const row = screen.getByRole("row", { name: "CORE-5 Work CORE-5" });
  expect(row.querySelectorAll('[data-facts-read="error"]').length).toBeGreaterThan(0);
  expect(
    screen.getByRole("button", { name: "Retry reading the Legion columns" })
  ).toBeInTheDocument();
});

// `exclude_tree` leaves out each tree it names: the root itself, and every row whose facts name
// that root. A row's tree is in its facts, so the view shows its loading state until the board
// and the items of its done rows answer, never a list from a read that has not answered.
test("exclude_tree leaves out the rows of each tree it names, and shows the loading state until the facts that name a row's tree answer", async () => {
  const sockets = liveSockets();
  const answer = Promise.withResolvers<readonly EvalItemRow[]>();
  const items = answerItems(() => answer.promise);
  const child = factsRow("CORE-6", "2026-11-12T10:00:00Z", 1);
  table(
    [...VIEW_ROWS(), issue("CORE-6", { status: "done", closed_at: "2026-11-12T10:00:00Z" })],
    "?view=done&exclude_tree=CORE-5",
    {},
    {},
    Promise.resolve(DAEMON)
  );
  expect(await screen.findByRole("status", { name: "Loading issues" })).toBeDefined();
  await answerBoard(sockets, legionBoard({ "CORE-3": boardFacts(2) }));
  await waitFor(() => expect(items).toHaveBeenCalled());
  expect(screen.getByRole("status", { name: "Loading issues" })).toBeDefined();
  expect(screen.queryByText("No issues match these filters.")).toBeNull();

  answer.resolve([
    factsRow("CORE-5", "2026-11-11T10:00:00Z", 3),
    { ...child, cols: { ...child.cols, root_key: "CORE-5", tree_depth: 1 } },
  ]);
  await screen.findByRole("row", { name: "CORE-3 Work CORE-3" });
  expect(listedKeys()).toEqual(["CORE-3"]);
});

// Each tree left out is a chip of the control row and a line of the Filters panel, each with the
// remove that gives its rows back and leaves the other tree out.
test("each tree exclude_tree leaves out is a chip of the row and a line of the Filters panel, and either remove gives its rows back", async () => {
  const user = userEvent.setup();
  const sockets = liveSockets();
  answerItems(async () => [factsRow("CORE-5", "2026-11-11T10:00:00Z", 3)]);
  table(VIEW_ROWS(), "?exclude_tree=CORE-3&exclude_tree=CORE-5", {}, {}, Promise.resolve(DAEMON));
  expect(await screen.findByRole("status", { name: "Loading issues" })).toBeDefined();
  await answerBoard(
    sockets,
    legionBoard({ "CORE-2": { ...boardFacts(2), root_key: "CORE-3", tree_depth: 1 } })
  );
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  expect(listedKeys()).toEqual(["CORE-1"]);
  const chip = screen.getByRole("button", { name: "Remove Not in tree CORE-3 filter" });
  expect(chip).toHaveAttribute("aria-pressed", "true");
  expect(screen.getByRole("button", { name: "Remove Not in tree CORE-5 filter" })).toBeDefined();

  await user.click(screen.getByRole("button", { name: /^Filters/ }));
  const panel = screen.getByRole("list", { name: "Other filters" });
  expect(within(panel).getByText("Not in tree CORE-3")).toBeDefined();
  await user.click(within(panel).getByRole("button", { name: "Remove Not in tree CORE-5" }));
  await waitFor(() => expect(routeSearch()).not.toContain("exclude_tree=CORE-5"));
  expect(routeSearch()).toContain("exclude_tree=CORE-3");
  expect(listedKeys()).toEqual(["CORE-1"]);

  await user.click(screen.getByRole("button", { name: "Remove Not in tree CORE-3 filter" }));
  await waitFor(() => expect(listedKeys().sort()).toEqual(["CORE-1", "CORE-2"]));
  expect(routeSearch()).not.toContain("exclude_tree");
  expect(screen.queryByRole("button", { name: /^Remove Not in tree / })).toBeNull();
});

/** Done, in a project with a daemon whose board's map holds CORE-3 and not CORE-5: the items read
 *  of CORE-5's close day, and the counts of each read made. */
async function doneWithFacts(rows = VIEW_ROWS()) {
  const sockets = liveSockets();
  const items = answerItems(async () => [factsRow("CORE-5", "2026-11-11T10:00:00Z", 3)]);
  const drawn = table(rows, "?view=done", {}, {}, Promise.resolve(DAEMON));
  await screen.findByRole("row", { name: "CORE-5 Work CORE-5" });
  // The board's map decides which rows need the items, so they are read once it has answered.
  expect(items).not.toHaveBeenCalled();
  await answerBoard(sockets, legionBoard({ "CORE-3": boardFacts(2) }));
  await waitFor(() => expect(items).toHaveBeenCalledTimes(1));
  const pageReads = () =>
    drawn.page.mock.calls.filter(([options]) => options.sort === "-closed_at").length;
  const openCountReads = () =>
    drawn.page.mock.calls.filter(([options]) => options.open === true).length;
  return { ...drawn, items, openCountReads, pageReads };
}

test("Done reads the items of the close days of the rows the board's map does not hold", async () => {
  const { items } = await doneWithFacts();
  expect(items.mock.calls[0]?.[1]).toBe("legion-work-time");
  expect(items.mock.calls[0]?.[2]).toEqual({
    from: "2026-11-11T00:00:00Z",
    to: "2026-11-12T00:00:00Z",
    row_kind: "issue",
    limit: 200,
    offset: 0,
  });
});

// The first rule: a live event on an issue reads the view's listing again, with its items, and
// on Done Open's count too.
test("a live event reads Done's page again, with its items and Open's count", async () => {
  const { client, items, openCountReads, pageReads } = await doneWithFacts();
  expect(pageReads()).toBe(1);
  expect(openCountReads()).toBe(1);
  // What the event feed refreshes for an event of one of the project's issues.
  await act(() => client.invalidateQueries({ queryKey: ["issues", "project", "CORE"] }));
  await waitFor(() => expect(items).toHaveBeenCalledTimes(2));
  expect(pageReads()).toBe(2);
  expect(openCountReads()).toBe(2);
});

// The second rule: a close whose items answer holds no row for the closed issue reads the items
// once more 60 s later, since a stored facts row is never more than 60 s behind its event.
test("a close whose items answer lacks its row reads the items once more 60 s later", async () => {
  const rows = VIEW_ROWS();
  const { client, items } = await doneWithFacts(rows);
  // The wait is the product's 60 s, run on the test's clock from the close on.
  jest.useFakeTimers();
  try {
    // CORE-1 closes elsewhere; the event reads Done's page again, and the items read it starts
    // answers no row for CORE-1 yet.
    rows[0] = issue("CORE-1", { status: "done", closed_at: "2026-11-12T10:00:00Z" });
    await act(async () => {
      void client.invalidateQueries({ queryKey: ["issues", "project", "CORE"] });
    });
    await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
    await waitFor(() => expect(items.mock.calls.length).toBeGreaterThanOrEqual(2));
    await tick(0);
    const reads = items.mock.calls.length;
    await tick(CLOSE_ITEMS_AGAIN_MS - 1_000);
    expect(items).toHaveBeenCalledTimes(reads);
    await tick(2_000);
    expect(items).toHaveBeenCalledTimes(reads + 1);
    // Once: the answer of that read is no close.
    await tick(CLOSE_ITEMS_AGAIN_MS + 1_000);
    expect(items).toHaveBeenCalledTimes(reads + 1);
  } finally {
    realClock();
  }
});

// The third rule: a board push that changes or removes the key of a done row the view lists
// reads its items again; a push that leaves every listed key as it was reads nothing.
test("a board push that changes or removes a listed done row's facts reads the items again", async () => {
  const { client, items } = await doneWithFacts();
  const push = (board: LegionBoardResponse) =>
    act(() => client.setQueryData(legionBoardQuery("CORE").queryKey, board));
  push(legionBoard({ "CORE-3": boardFacts(2), "CORE-1": boardFacts(1) }));
  await new Promise((resolve) => setTimeout(resolve, 50));
  expect(items).toHaveBeenCalledTimes(1);
  push(legionBoard({ "CORE-3": boardFacts(4), "CORE-1": boardFacts(1) }));
  await waitFor(() => expect(items).toHaveBeenCalledTimes(2));
  push(legionBoard({ "CORE-1": boardFacts(1) }));
  await waitFor(() => expect(items).toHaveBeenCalledTimes(3));
  // With CORE-3 out of the map, its close day is read too.
  expect(items.mock.calls[2]?.[2]).toMatchObject({ from: "2026-11-10T00:00:00Z" });
});

// The bar under the tabs, the pager and the (i) hint.

/** Open's rows: three in progress, two in Todo and one in Triage, given out of the status order,
 *  and a closed one Open does not list. */
const BAR_ROWS = (): IssueSummary[] => [
  issue("CORE-1", { status: "in_progress" }),
  issue("CORE-2", { status: "todo" }),
  issue("CORE-3", { status: "triage" }),
  issue("CORE-4", { status: "in_progress" }),
  issue("CORE-5", { status: "todo" }),
  issue("CORE-6", { status: "in_progress" }),
  issue("CORE-7", { status: "done", closed_at: "2026-11-10T10:00:00Z" }),
];

/** The bar's segments, each with its status and its width in percent of the bar. */
const barSegments = (): [string | undefined, number][] =>
  [...document.querySelectorAll<HTMLElement>("[data-bar-segment]")].map((segment) => [
    segment.dataset.barSegment,
    Number.parseFloat(segment.style.width),
  ]);

/** The bar's legend entries, as they read. */
const legendEntries = () =>
  within(screen.getByRole("region", { name: "Issues by status" }))
    .getAllByRole("button")
    .map((entry) => entry.textContent);

// R6: Open's bar counts the rows the table lists, by status, in the status order and from the
// left edge, each segment as wide as its share, with no segment for a status with no row.
test("Open's bar counts the listed rows by status in the status order, and follows the search", async () => {
  const user = userEvent.setup();
  table(BAR_ROWS());
  await screen.findByRole("region", { name: "Issues by status" });
  expect(legendEntries()).toEqual(["Triage 1", "Todo 2", "In progress 3"]);
  const segments = barSegments();
  expect(segments.map(([status]) => status)).toEqual(["triage", "todo", "in_progress"]);
  expect(segments[0]?.[1]).toBeCloseTo(100 / 6);
  expect(segments[2]?.[1]).toBeCloseTo(50);
  expect(segments.reduce((sum, [, width]) => sum + width, 0)).toBeCloseTo(100);
  // No count of the strip it replaces: no Legion queue, no running issues, no epics.
  const bar = screen.getByRole("region", { name: "Issues by status" });
  expect(bar.textContent).not.toMatch(/queue|running|epic/i);

  await user.type(screen.getByRole("searchbox", { name: "Search issues" }), "CORE-1");
  await waitFor(() => expect(legendEntries()).toEqual(["In progress 1"]));
  expect(barSegments()).toEqual([["in_progress", 100]]);
});

test("with a Status filter of In progress the bar is one segment of every listed row", async () => {
  table(BAR_ROWS(), "?status=in_progress");
  await screen.findByRole("region", { name: "Issues by status" });
  expect(listedKeys().sort()).toEqual(["CORE-1", "CORE-4", "CORE-6"]);
  expect(legendEntries()).toEqual(["In progress 3"]);
  expect(barSegments()).toEqual([["in_progress", 100]]);
  expect(screen.getByRole("tab", { name: "Open, 3 issues, changed" })).toBeDefined();
});

test("a press on a legend entry narrows the view to that status, and a second press gives Open's statuses back", async () => {
  const user = userEvent.setup();
  table(BAR_ROWS());
  await user.click(await screen.findByRole("button", { name: "Todo 2" }));
  await waitFor(() => expect(routeSearch()).toBe("?status=todo"));
  expect(listedKeys().sort()).toEqual(["CORE-2", "CORE-5"]);
  expect(legendEntries()).toEqual(["Todo 2"]);
  const todo = screen.getByRole("button", { name: "Todo 2" });
  expect(todo).toHaveAttribute("aria-pressed", "true");

  await user.click(todo);
  await waitFor(() => expect(routeSearch()).toBe(""));
  expect(legendEntries()).toEqual(["Triage 1", "Todo 2", "In progress 3"]);
});

/** The saved view "Busy": Todo and In progress, as Save view writes one. */
const BUSY = "status=todo&status=in_progress&cols=key,title,status&layout=table";

// A saved view the reader opened, by its tab or by an address that is its query, stays the open
// tab while they change it, with the dot of a changed view, so the second press gives back its
// statuses, not Open's.
test.each([
  [
    "by its tab",
    "",
    async (user: UserEvent) =>
      user.click(await screen.findByRole("tab", { name: "Busy, 5 issues" })),
  ],
  ["by its address", `?${BUSY}`, async () => {}],
])("a saved view opened %s stays open while changed, and the second press gives its statuses back", async (_name, search, open) => {
  const user = userEvent.setup();
  keepSavedViews([{ name: "Busy", query: BUSY }]);
  table(BAR_ROWS(), search);
  await open(user);
  const busy = await screen.findByRole("tab", { name: "Busy, 5 issues" });
  expect(busy).toHaveAttribute("aria-selected", "true");
  expect(busy.querySelector("[data-view-changed]")).toBeNull();
  await waitFor(() => expect(legendEntries()).toEqual(["Todo 2", "In progress 3"]));

  await user.click(screen.getByRole("button", { name: "In progress 3" }));
  await waitFor(() =>
    expect(new URLSearchParams(routeSearch()).getAll("status")).toEqual(["in_progress"])
  );
  expect(legendEntries()).toEqual(["In progress 3"]);
  const changed = await screen.findByRole("tab", { name: "Busy, 3 issues, changed" });
  expect(changed).toHaveAttribute("aria-selected", "true");
  expect(changed.querySelector("[data-view-changed]")).not.toBeNull();
  expect(screen.getByRole("tab", { name: /^Open/ })).toHaveAttribute("aria-selected", "false");

  await user.click(screen.getByRole("button", { name: "In progress 3" }));
  await waitFor(() =>
    expect(new URLSearchParams(routeSearch()).getAll("status")).toEqual(["todo", "in_progress"])
  );
  expect(legendEntries()).toEqual(["Todo 2", "In progress 3"]);
  const restored = await screen.findByRole("tab", { name: "Busy, 5 issues" });
  expect(restored).toHaveAttribute("aria-selected", "true");
  expect(restored.querySelector("[data-view-changed]")).toBeNull();
});

// R6: Done draws no bar and keeps no room for one, and the Board draws none in either grouping.
test.each([
  ["Done", "?view=done", () => screen.findByRole("row", { name: "CORE-7 Work CORE-7" })],
  ["Open's Board", "?layout=board", () => screen.findByRole("region", { name: "Project board" })],
])("%s draws no bar", async (_name, search, drawn) => {
  table(BAR_ROWS(), search);
  await drawn();
  expect(screen.queryByRole("region", { name: "Issues by status" })).toBeNull();
  expect(document.querySelector("[data-bar-segment], [data-bar-legend]")).toBeNull();
});

// R13: a priority's table keeps its status strip, without the strip's numbers.
test("a priority's table keeps its status strip with no queue or running number", async () => {
  const advances = [{ id: "PRI-7", inherited: false }];
  table(
    [
      issue("CORE-2", { advances, labels: ["legion"] }),
      issue("OPS-3", { advances, status: "in_progress" }),
    ],
    "",
    undefined,
    { listing: { advances: "PRI-7" }, stateKey: "w" }
  );
  const strip = await screen.findByRole("group", { name: "Issues by status" });
  expect(
    within(strip)
      .getAllByRole("button")
      .map((cell) => cell.textContent)
  ).toEqual(["Todo 1", "In progress 1"]);
  expect(strip.parentElement?.textContent).toBe("Todo 1In progress 1");
  expect(screen.queryByRole("region", { name: "Issues by status" })).toBeNull();
  const pager = screen.getByTestId("issues-table-pager");
  expect(within(pager).getByText("Rows 1–2")).toBeDefined();
  expect(pager.textContent).not.toMatch(/of 2|issues/);
});

// The List's pager draws the range of rows, the page, its steps and the page size, and no total:
// the tab says how many rows the view lists.
test("the pager reads Rows and Page, its steps and the page size, and no total", async () => {
  const user = userEvent.setup();
  table(Array.from({ length: 120 }, (_, index) => issue(`CORE-${index + 1}`)));
  expect(await screen.findByRole("tab", { name: "Open, 120 issues" })).toBeDefined();
  const pager = screen.getByTestId("issues-table-pager");
  expect(within(pager).getByText("Rows 1–50")).toBeDefined();
  expect(within(pager).getByText("Page 1 of 3")).toBeDefined();
  expect(within(pager).getByRole("button", { name: "‹ Previous" })).toBeDefined();
  expect(within(pager).getByRole("button", { name: "Next ›" })).toBeDefined();
  const sizes = within(pager).getByRole("combobox", { name: "Rows per page" });
  expect([...sizes.querySelectorAll("option")].map((option) => option.textContent)).toContain(
    "All rows"
  );
  expect(pager.textContent).not.toMatch(/120|issues/);

  await user.selectOptions(sizes, String(ALL_ROWS));
  await waitFor(() => expect(pager.querySelector(".ml-auto")?.textContent).toBe("All rows"));
  expect(pager.textContent).not.toMatch(/120|issues/);
});

// Done's page is the server's: its range and page count follow the server's count of the done
// rows, not the one page of them the table holds.
test("Done's second server page reads Rows 51–100 of the server's count", async () => {
  const closed = Array.from({ length: 120 }, (_, index) =>
    issue(`CORE-${index + 10}`, { status: "done", closed_at: "2026-11-04T10:00:00Z" })
  );
  const { page } = table(closed, "?view=done&page=2");
  expect(await screen.findByRole("tab", { name: "Done, 120 issues" })).toBeDefined();
  expect(page).toHaveBeenCalledWith({
    project: "CORE",
    status: ["done"],
    sort: "-closed_at",
    limit: 50,
    offset: 50,
  });
  const pager = screen.getByTestId("issues-table-pager");
  expect(within(pager).getByText("Rows 51–100")).toBeDefined();
  expect(within(pager).getByText("Page 2 of 3")).toBeDefined();
  expect(pager.textContent).not.toMatch(/120|issues/);
});

/** A card of the board's Queue column that waits for capacity, its worker at work. */
function waitingCard(key: string): LegionBoardCard {
  return {
    blocked_by: null,
    children: 0,
    key,
    phase: "admitted",
    phase_since: "2026-11-17T07:00:00Z",
    priority: null,
    roles: ["implementer"],
    slot: 2,
    slot_since: "2026-11-17T07:00:00Z",
    slot_use: "working",
    title: `Work ${key}`,
    tree: key,
    tree_slot: 1,
    waiting: { reason: "slot", since: "2026-11-17T07:00:00Z", blocked_by: null },
    waiting_on: null,
    waiting_place: 1,
  };
}

/** Every text the page draws and every name it gives a control or a mark. */
function pageWords(): string[] {
  const words = [document.body.textContent ?? ""];
  for (const element of document.body.querySelectorAll("*"))
    for (const attribute of ["aria-label", "aria-description", "title", "placeholder", "alt"]) {
      const value = element.getAttribute(attribute);
      if (value !== null) words.push(value);
    }
  return words;
}

// R1, R2, R9: the (i) hint follows Save view in a project with a daemon, and holds the capacity,
// the trees and the last read. No text or name on the tab says "slot".
test("the (i) hint follows Save view in a project with a daemon, and nothing on the tab says slot", async () => {
  const user = userEvent.setup();
  const sockets = liveSockets();
  table(
    VIEW_ROWS(),
    "?cols=key,title,status,legion_phase,waiting_reason,activity",
    {},
    {},
    Promise.resolve(DAEMON)
  );
  await answerBoard(sockets, {
    ...legionBoard({}, { slot_cap: null, slots_in_use: 4, trees: 0 }),
    columns: [{ id: "queue", label: "Queue", cards: [waitingCard("CORE-1")] }],
  });
  await waitFor(() =>
    expect(
      within(screen.getByRole("row", { name: "CORE-1 Work CORE-1" })).getByText("Capacity to go on")
    ).toBeDefined()
  );
  const hint = screen.getByRole("button", { name: "Legion daemon" });
  const save = screen.getByRole("button", { name: "Save view", hidden: true });
  expect(save.compareDocumentPosition(hint) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  expect(pageWords().filter((words) => /slot/i.test(words))).toEqual([]);

  await user.click(hint);
  const panel = screen.getByRole("dialog", { name: "Legion daemon" });
  expect(
    [...panel.querySelectorAll("dt")].map((name) => [
      name.textContent,
      name.nextElementSibling?.textContent,
    ])
  ).toEqual([
    ["Capacity", "4"],
    ["Trees", "0"],
    ["Last read", expect.any(String)],
  ]);
  expect(pageWords().filter((words) => /slot/i.test(words))).toEqual([]);

  await user.click(screen.getByRole("button", { name: "Columns" }));
  expect(pageWords().filter((words) => /slot/i.test(words))).toEqual([]);
});

test("a project with no daemon has no (i) hint", async () => {
  table(VIEW_ROWS());
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  expect(screen.queryByRole("button", { name: "Legion daemon" })).toBeNull();
});

/** Lets `ms` of real time pass, and React draw whatever it set off. */
async function waitMs(ms: number): Promise<void> {
  await act(async () => {
    const { promise, resolve } = Promise.withResolvers<void>();
    setTimeout(resolve, ms);
    await promise;
  });
}

/** The height of each row of the table in the geometry below. */
const ROW_PX = 64;

/**
 * The geometry the table and its anchor read, which the unit DOM has no layout engine for: the
 * box `width` px wide at `top()`, its header `head()` tall, each row ROW_PX tall under it, all
 * scrolled by the box's `scrollTop`; a hit test answers the row drawn at that height, and none
 * over the header.
 */
function tableGeometry({
  head,
  top,
  width = 1000,
}: {
  head: () => number;
  top: () => number;
  width?: number;
}) {
  const scrolled = () => screen.queryByTestId("issues-table-scroll")?.scrollTop ?? 0;
  const rows = () => [...document.querySelectorAll<HTMLElement>("tr[data-issue-row]")];
  // On `Element.prototype`, which owns the method (see above).
  const rect = spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (
    this: Element
  ) {
    if (this instanceof HTMLElement && this.dataset.testid === "issues-table-scroll")
      return new DOMRect(0, top(), width, 600);
    if (this.tagName === "THEAD") return new DOMRect(0, top(), width, head());
    const index = rows().indexOf(this as HTMLElement);
    if (index >= 0)
      return new DOMRect(0, top() + head() + index * ROW_PX - scrolled(), width, ROW_PX);
    return new DOMRect(0, top() - scrolled(), width, 2000);
  });
  const point = spyOn(document, "elementFromPoint").mockImplementation((_x, y) =>
    y < top() + head()
      ? null
      : (rows()[Math.floor((y - top() - head() + scrolled()) / ROW_PX)] ?? null)
  );
  restores.push(() => {
    rect.mockRestore();
    point.mockRestore();
  });
  return {
    rowTop: (key: string) =>
      document.querySelector(`tr[data-issue-row="${key}"]`)?.getBoundingClientRect().top,
    scrolled,
  };
}

// R11: content that others add does not move a reader's place while it stays on screen. With no
// hand on a row and the box at its top, the anchor holds the first row on screen once the reader
// has seen it, so a row added above it lands out of view above, under the header, and the Open
// tab counts it at once.
test("a row added above the first row on screen lands out of view for a reader at the top", async () => {
  const geometry = tableGeometry({ head: () => 48, top: () => 300 });
  const rows = [issue("CORE-2"), issue("CORE-3")];
  const { client } = table(rows);
  await screen.findByRole("row", { name: "CORE-2 Work CORE-2" });
  // The rows painted: the reader has seen the first row on screen.
  await waitMs(50);
  const before = geometry.rowTop("CORE-2");
  expect(before).toBe(300 + 48);
  expect(geometry.scrolled()).toBe(0);

  rows.unshift(issue("CORE-1"));
  await act(() => client.invalidateQueries({ queryKey: ["issues"] }));
  await waitFor(() => expect(listedKeys()).toEqual(["CORE-1", "CORE-2", "CORE-3"]));

  expect(geometry.rowTop("CORE-2")).toBe(before);
  expect(geometry.scrolled()).toBe(ROW_PX);
  expect(geometry.rowTop("CORE-1")).toBe((before ?? 0) - ROW_PX);
  expect(screen.getByRole("tab", { name: "Open, 3 issues" })).toBeDefined();
});

// R11: in a project with a Legion daemon the board's first snapshot is part of Open's first draw.
// Open sorts by Queue and draws its rows before the board answers; the snapshot sorts them again,
// and the anchor holds nothing for it, even once the rows were painted: Open starts at its top
// with the Queue sort's first row, the issue with Place 1, whole under the header.
test("the board's first snapshot after Open's rows are painted starts Open at its top with the Queue sort's first row", async () => {
  const sockets = liveSockets();
  const geometry = tableGeometry({ head: () => 48, top: () => 300 });
  table([issue("CORE-1"), issue("CORE-2"), issue("CORE-3")], "", {}, {}, Promise.resolve(DAEMON));
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  expect(listedKeys()).toEqual(["CORE-1", "CORE-2", "CORE-3"]);
  // The rows painted before the board answers.
  await waitMs(50);
  await answerBoard(sockets, {
    ...legionBoard(),
    columns: [
      {
        id: "queue",
        label: "Queue",
        cards: [
          { ...waitingCard("CORE-3"), waiting_place: 1 },
          { ...waitingCard("CORE-2"), waiting_place: 2 },
        ],
      },
    ],
  });
  await waitFor(() => expect(listedKeys()).toEqual(["CORE-3", "CORE-2", "CORE-1"]));
  await waitMs(50);
  expect(geometry.scrolled()).toBe(0);
  expect(geometry.rowTop("CORE-3")).toBe(300 + 48);
});

// k13 (R11): a notice above the table moves the reader's place no more than an arrival does.
// With the hand on a row, a failed read's alert that appears above the table while its rows stay
// leaves that row's top where it was, and the box scrolls by the alert's height instead.
test("an alert above the table with the hand on a row leaves that row's top in place", async () => {
  const ALERT = 40;
  const geometry = tableGeometry({
    head: () => 48,
    top: () => 300 + (document.querySelector('[role="alert"]') === null ? 0 : ALERT),
  });
  const { client, listing } = table([issue("CORE-1"), issue("CORE-2"), issue("CORE-3")]);
  await screen.findByRole("row", { name: "CORE-2 Work CORE-2" });
  act(() => screen.getByRole("checkbox", { name: "Select CORE-2" }).focus());
  const before = geometry.rowTop("CORE-2");
  expect(before).toBe(300 + 48 + ROW_PX);

  listing.mockRejectedValueOnce(new Error("unreachable"));
  await act(() => client.invalidateQueries({ queryKey: ["issues"] }));
  expect((await screen.findByRole("alert")).textContent).toContain("Could not load issues.");

  expect(listedKeys()).toEqual(["CORE-1", "CORE-2", "CORE-3"]);
  expect(geometry.rowTop("CORE-2")).toBe(before);
  expect(geometry.scrolled()).toBe(ALERT);
});

// k13 (R11): the first draw of a view's rows starts at the top. On a 390 px phone the table
// first draws at the width it starts from, then measures its box and draws again before any frame
// is painted, and the Title header stacks its sort over its filter in a box under 640 px, 44 px
// taller. The rows move down under it; no reader has seen them yet, so nothing holds the first
// row where it was, and the box stays at its top with the first row whole under the header.
test("a table that draws its rows after a loading state opens at its top with its first row whole on a phone", async () => {
  const HEAD = 49;
  const STACKED = 44;
  const titleStacked = () =>
    ([...document.querySelectorAll<HTMLElement>("thead th")]
      .find((cell) => (cell.textContent ?? "").startsWith("Title"))
      ?.querySelector(".flex-col") ?? null) !== null;
  const geometry = tableGeometry({
    head: () => HEAD + (titleStacked() ? STACKED : 0),
    top: () => 300,
    width: 390,
  });
  table(
    ["CORE-1", "CORE-2", "CORE-3"].map((key) => issue(key, { status: "done" })),
    "?view=done"
  );
  expect(await screen.findByRole("status", { name: "Loading issues" })).toBeDefined();
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  await waitMs(50);
  expect(titleStacked()).toBe(true);
  expect(geometry.scrolled()).toBe(0);
  expect(geometry.rowTop(listedKeys()[0] ?? "")).toBe(300 + HEAD + STACKED);
});

/** A card of the board's Implementing column, in its phase since `since`. */
function phaseCard(key: string, since: string): LegionBoardCard {
  return {
    ...waitingCard(key),
    phase: "implementing",
    phase_since: since,
    waiting: null,
    waiting_place: null,
  };
}

// k13 (R11), by a count and not by a clock: a live change to one issue draws that issue's card,
// not the Board's hundreds, and a card whose drawn time changed since the Board last drew. Every
// card body draws `UnreachableRouteMarker` once per render and every card shell calls
// `useDraggable` once, and on the Board nothing else does either, so their calls name the bodies
// and the shells a change drew. Of the 100 cards with Legion facts, 50 have been in their phase
// under a day, which reads anew each minute, and 50 between one and two days, ten minutes past
// an hour, which reads the same 61 s on.
test("a live read that moves one card of 600 draws at most 5 card bodies and 5 card shells", async () => {
  const start = new Date("2026-11-17T12:00:30Z");
  setSystemTime(start);
  restores.push(() => setSystemTime());
  const sockets = liveSockets();
  const rows = Array.from({ length: 600 }, (_, index) =>
    issue(`CORE-${1000 + index}`, { status: index % 3 === 0 ? "in_progress" : "todo" })
  );
  const ago = (minutes: number) => new Date(start.getTime() - minutes * 60_000).toISOString();
  const ticking = rows.slice(0, 50).map((row) => row.key);
  const steady = rows.slice(50, 100).map((row) => row.key);
  const { client } = table(rows, "?layout=board", {}, {}, Promise.resolve(DAEMON));
  await answerBoard(sockets, {
    ...legionBoard(Object.fromEntries([...ticking, ...steady].map((key) => [key, boardFacts(3)]))),
    columns: [
      {
        id: "implementing",
        label: "Implementing",
        cards: [
          ...ticking.map((key, index) => phaseCard(key, ago(30 + index))),
          ...steady.map((key, index) => phaseCard(key, ago(24 * 60 + (index % 20) * 60 + 10))),
        ],
      },
    ],
  });
  await waitFor(
    () => {
      expect(document.querySelectorAll("[data-board-card]")).toHaveLength(600);
      expect(document.querySelectorAll("[data-legion-fact=phase-time]")).toHaveLength(100);
    },
    { timeout: 30_000 }
  );
  const bodies = spyOn(routeReach, "UnreachableRouteMarker");
  const shells = spyOn(dndCore, "useDraggable");
  restores.push(() => {
    bodies.mockRestore();
    shells.mockRestore();
  });
  /** Moves the `nth` Todo card with no Legion card to In progress, as another reader's change
   *  read again, and gives its key and the cards whose bodies and shells drew for it. */
  const move = async (nth: number, updatedAt: string) => {
    bodies.mockClear();
    shells.mockClear();
    const index =
      rows.flatMap((row, at) => (at >= 100 && row.status === "todo" ? [at] : []))[nth] ?? 0;
    const key = rows[index]?.key ?? "";
    rows[index] = issue(key, { status: "in_progress", updated_at: updatedAt });
    await act(() => client.invalidateQueries({ queryKey: ["issues"] }));
    await waitFor(
      () =>
        expect(
          document.querySelector(`[data-board-column="in_progress"] [data-board-card="${key}"]`)
        ).not.toBeNull(),
      { timeout: 30_000 }
    );
    // Whatever the change still draws after the card lands is counted too.
    await waitMs(200);
    return {
      bodies: new Set(
        bodies.mock.calls.map(([props]) => {
          // The marker's props type names the route fields alone; a card body hands it the
          // card's whole issue, whose key names the body.
          const routed: object = props.issue;
          if (!("key" in routed) || typeof routed.key !== "string")
            throw new Error("A card body drew its route marker for an issue with no key");
          return routed.key;
        })
      ),
      key,
      shells: new Set(shells.mock.calls.map(([options]) => String(options.id))),
    };
  };

  const sameMinute = await move(0, "2026-11-17T12:00:40Z");
  for (const drawn of [sameMinute.bodies, sameMinute.shells]) {
    expect(drawn.size).toBeGreaterThanOrEqual(1);
    expect(drawn.size).toBeLessThanOrEqual(5);
    expect(drawn.has(sameMinute.key)).toBe(true);
  }
  expect(bodies.mock.calls.length).toBeLessThanOrEqual(5);
  expect(shells.mock.calls.length).toBeLessThanOrEqual(5);

  setSystemTime(new Date(start.getTime() + 61_000));
  const nextMinute = await move(1, "2026-11-17T12:01:35Z");
  for (const drawn of [nextMinute.bodies, nextMinute.shells]) {
    for (const key of ticking) expect(drawn.has(key)).toBe(true);
    for (const key of steady) expect(drawn.has(key)).toBe(false);
    const others = [...drawn].filter((key) => !ticking.includes(key));
    expect(others).toContain(nextMinute.key);
    expect(others.length).toBeLessThanOrEqual(5);
  }
}, 120_000);

// The live view sends a board entry again only when its stored row moves, so the page grows an
// open issue's values itself: the value plus its rate times the hours from as_of to its clock.
test("an open row's Work time is its board entry grown from its as_of to the page's clock", async () => {
  const now = new Date("2026-11-17T12:00:00Z");
  setSystemTime(now);
  restores.push(() => setSystemTime());
  const sockets = liveSockets();
  table(
    [issue("CORE-1", { status: "in_progress" })],
    "?cols=key,title,work_hours",
    {},
    {},
    Promise.resolve(DAEMON)
  );
  // 1 hour of work 90 minutes before the clock, at 1 hour an hour.
  const asOf = new Date(now.getTime() - 90 * 60_000).toISOString();
  await answerBoard(sockets, legionBoard({ "CORE-1": openFacts(asOf) }));
  await waitFor(() =>
    expect(
      within(screen.getByRole("row", { name: "CORE-1 Work CORE-1" })).getByText("2 h 30 m")
    ).toBeDefined()
  );
});

// One build of the rows gives them one clock (`Item.at`): a row's Time in Legion phase and Work
// time, and those of its card on the Board, are of that moment. A clock that moves on with no new
// build draws them as they were: on the Board, and in the table drawn again.
test("a row's and its card's Time in Legion phase and Work time are of the clock the rows were built at", async () => {
  const user = userEvent.setup();
  const start = new Date("2026-11-17T12:00:00Z");
  setSystemTime(start);
  restores.push(() => setSystemTime());
  const sockets = liveSockets();
  const ago = (minutes: number) => new Date(start.getTime() - minutes * 60_000).toISOString();
  table(
    [issue("CORE-1", { status: "in_progress" })],
    "?cols=key,title,time_in_phase,work_hours",
    {},
    {},
    Promise.resolve(DAEMON)
  );
  await answerBoard(sockets, {
    ...legionBoard({ "CORE-1": openFacts(ago(90)) }),
    columns: [{ id: "implementing", label: "Implementing", cards: [phaseCard("CORE-1", ago(30))] }],
  });
  /** The row's Time in Legion phase and Work time cells, found by their column's header. */
  const cellTimes = () => {
    const headers = screen.getAllByRole("columnheader").map((header) => header.textContent ?? "");
    const row = screen.getByRole("row", { name: "CORE-1 Work CORE-1" });
    const cells = within(row).getAllByRole("cell");
    return ["Time in Legion phase", "Work time"].map(
      (name) => cells[headers.findIndex((text) => text.startsWith(name))]?.textContent
    );
  };
  await waitFor(() => expect(cellTimes()).toEqual(["30 m", "2 h 30 m"]));

  setSystemTime(new Date(start.getTime() + 10 * 60_000));
  await user.click(screen.getByRole("button", { name: "Board" }));
  const region = await screen.findByRole("region", { name: "Project board" });
  const fact = (hook: string) =>
    region.querySelector(`[data-board-card="CORE-1"] [data-legion-fact=${hook}]`)?.textContent;
  await waitFor(() => expect(fact("phase-time")).toBe("Time in Legion phase30 m"));
  expect(fact("work-time")).toBe("Work time2 h 30 m");

  await user.click(screen.getByRole("button", { name: "Table" }));
  await waitFor(() => expect(cellTimes()).toEqual(["30 m", "2 h 30 m"]));
});
