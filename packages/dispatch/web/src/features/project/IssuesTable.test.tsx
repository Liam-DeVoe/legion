import { afterEach, expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import type { ReactNode } from "react";
import { MemoryRouter, useLocation } from "react-router-dom";
import { ApiError, api } from "../../api/client";
import { boardMoveMutationKey, projectIssuesQuery } from "../../api/queries";
import type { Issue, IssueSummary } from "../../api/types";
import { readLayout } from "../../components/table-storage";
import { readSelectedIssues } from "../search/bar-scope";
import { KeymapProvider } from "../shell/KeymapProvider";
import { useKeymapScope } from "../shell/keymap";
import { writesOf } from "../writes/write-store";
import { IssuesTable } from "./IssuesTable";
import type { IssueListing } from "./issues-table-data";
import { ISSUE_TABLE_COLUMNS, tableStorageKey } from "./issues-table-model";

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
function table(
  issues: IssueSummary[],
  search = "",
  full?: Promise<IssueSummary[]>,
  props: {
    listing?: IssueListing;
    priorityTitles?: ReadonlyMap<string, string>;
    stateKey?: string;
  } = {}
) {
  const parents = [
    ...new Set(
      issues.flatMap((row) =>
        row.parent && !issues.some((candidate) => candidate.key === row.parent) ? [row.parent] : []
      )
    ),
  ].map((key) => issue(key, { status: "triage" }));
  // The listing route as the server answers it: `open` leaves the closed rows out, and
  // `advances` keeps the issues that carry that relation, whichever project they are in.
  const listing = spyOn(api, "listIssues").mockImplementation(async (options = {}) =>
    !options.open && full
      ? full
      : [...issues, ...parents].filter(
          (issue) =>
            (!options.open || issue.status !== "done") &&
            (options.advances === undefined ||
              (issue.advances ?? []).some((relation) => options.advances?.includes(relation.id)))
        )
  );
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
  const view = render(
    <MemoryRouter initialEntries={[`/projects/CORE/issues${search}`]}>
      <QueryClientProvider client={client}>
        <KeymapProvider>
          <Route />
          <IssuesTable listing={{ project: "CORE" }} {...props} />
        </KeymapProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  restores.push(() => {
    view.unmount();
    client.clear();
    listing.mockRestore();
    state.mockRestore();
    agents.mockRestore();
    viewer.mockRestore();
    if (descriptor) Object.defineProperty(HTMLElement.prototype, "clientHeight", descriptor);
    else Reflect.deleteProperty(HTMLElement.prototype, "clientHeight");
  });
  return { client, listing, view };
}

/** Picks a grouping in the row's Group by menu: the menu is opened if it is shut, and a choice
 *  leaves it open for the next one. */
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
  await client.invalidateQueries({ queryKey: ["issues"] });
  await screen.findByRole("alert");
  await user.click(screen.getByRole("button", { name: "Retry loading issues" }));
  expect(await screen.findByRole("row", { name: "CORE-1 Work CORE-1" })).toBeDefined();
});

// Every panel the table hangs off a control - a header's filter, Columns, Group by - and
// the shared MultiSelect dismiss by one set of rules: a press outside, or Escape, which puts
// focus back on the control that opened it.
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
  // Claimed by is in the Columns menu, not Open's defaults, so the address shows it.
  table(
    [issue("CORE-1", { open_asks: 2 })],
    "?needs-you=1&unread=1&unclaimed=1&cols=key,title,claim"
  );
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
  table([issue("CORE-1")], "?status=done&page=3&size=25", done.promise);
  await screen.findByText("Loading closed issues…");
  done.resolve(
    Array.from({ length: 80 }, (_, index) => issue(`CORE-${index + 2}`, { status: "done" }))
  );
  await screen.findByRole("row", { name: "CORE-52 Work CORE-52" });
  expect(screen.getByText("Page 3 of 4")).toBeDefined();
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

// The status bar counts what is loaded. With closed issues unloaded it shows no Done segment at
// all, rather than a cell that reads as a count; the Status filter's own Done option says they
// load when it is picked.
test("the status bar has no Done cell while closed issues are unloaded", async () => {
  const user = userEvent.setup();
  table([issue("CORE-1")]);
  const bar = await screen.findByRole("group", { name: "Issues by status" });
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
  table([issue("CORE-1", { claim: held }), issue("CORE-2")], "?unclaimed=1&cols=key,title,claim");
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

// A browser that refuses local storage loses the saved layout and the interrupted-action
// record, and nothing else: no click may throw out of the table.
test("a browser that refuses storage still renders the table and runs an action", async () => {
  const user = userEvent.setup();
  const refuse = (key: string) => {
    if (key.startsWith("dispatch.project-table.")) throw new Error("Storage refused");
  };
  const read = Storage.prototype.getItem;
  const write = Storage.prototype.setItem;
  const getItem = spyOn(Storage.prototype, "getItem").mockImplementation(function (
    this: Storage,
    key: string
  ) {
    refuse(key);
    return read.call(this, key);
  });
  const setItem = spyOn(Storage.prototype, "setItem").mockImplementation(function (
    this: Storage,
    key: string,
    value: string
  ) {
    refuse(key);
    write.call(this, key, value);
  });
  const send = spyOn(api, "send").mockResolvedValue(savedIssue("CORE-2"));
  restores.push(() => {
    getItem.mockRestore();
    setItem.mockRestore();
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
  box.dispatchEvent(new Event("scroll", { bubbles: false }));
  window.dispatchEvent(new Event("scroll"));
  expect(measured.filter((name) => name === "issues-table-scroll" || name === "THEAD")).toEqual([]);
});

// What moves the box's top edge is the content above it, which no row count reports: the box
// and the block above it are observed instead. happy-dom has no `ResizeObserver`, so the stub
// is the geometry — what the table observes, and what a notice appearing there fires.
test("the box is measured through an observer over itself and the content above it", async () => {
  const live: { elements: Element[]; fire: () => void }[] = [];
  const original = globalThis.ResizeObserver;
  globalThis.ResizeObserver = class {
    readonly elements: Element[] = [];
    constructor(callback: ResizeObserverCallback) {
      live.push({
        elements: this.elements,
        fire: () => callback([], this as unknown as ResizeObserver),
      });
    }
    observe(element: Element) {
      this.elements.push(element);
    }
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver;
  restores.push(() => {
    globalThis.ResizeObserver = original;
  });
  table([issue("CORE-1")]);
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  const box = screen.getByTestId("issues-table-scroll");
  const above = screen.getByRole("region", { name: "Project issues" }).firstElementChild;
  if (!above) throw new Error("The table has no content above it");
  const watching = [...live].reverse().find((entry) => entry.elements.includes(box));
  if (!watching) throw new Error("Nothing observes the table's box");
  expect(watching.elements).toContain(above);
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
  watching.fire();
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
  const rect = spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (
    this: HTMLElement
  ) {
    return new DOMRect(0, 0, this.dataset.testid === "issues-table-scroll" ? 390 : 100, 40);
  });
  restores.push(() => rect.mockRestore());
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

// A reader's saved layout is read and written back as it always was: under the same key, in the
// same JSON, with a column the table does not have, a pinned column's visibility and a width out
// of bounds dropped, and every other field of the record left where it was. A project's table has
// none of the listing columns, so it writes none of them.
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
    '{"sizing":{"title":360},"order":["status","key"],"visibility":{"updated":true,"labels":false},"kept":1}'
  );
});

test("the List stores a reader's first layout with Updated hidden and no listing column", async () => {
  table([issue("CORE-1")]);
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  expect(localStorage.getItem("dispatch.project-table.CORE")).toBe(
    '{"sizing":{},"order":[],"visibility":{"updated":false}}'
  );
});

// What `in` read on main as a column: a name every object inherits. No app path writes one, so
// only a corrupt record holds it, and it is dropped like any other name the table has no column for.
test("a stored width under an inherited name such as toString is dropped", () => {
  localStorage.setItem("dispatch.project-table.CORE", '{"sizing":{"toString":200,"title":360}}');
  expect(readLayout(tableStorageKey("CORE"), ISSUE_TABLE_COLUMNS).sizing).toEqual({ title: 360 });
});

/** Each header cell of the List, left to right, as `<its column's words> <width> <the offset a
 *  pinned cell is held at, or ->`, drawn with the scroll box `width` pixels wide. */
async function headerLayout(width: number): Promise<string[]> {
  const rect = spyOn(HTMLElement.prototype, "getBoundingClientRect").mockImplementation(function (
    this: HTMLElement
  ) {
    return new DOMRect(0, 0, this.dataset.testid === "issues-table-scroll" ? width : 100, 40);
  });
  restores.push(() => rect.mockRestore());
  table([issue("CORE-1")]);
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  const head = screen.getByRole("table", { name: "Project issues table" }).querySelector("thead");
  return [...(head?.querySelectorAll("th") ?? [])].map((cell) =>
    [
      (cell.textContent ?? "").replace(/[▲▼]|Filter$/g, "") || "-",
      cell.style.width,
      cell.style.left || "-",
    ].join(" ")
  );
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

// The List's widths and pins at a desktop box, a box too narrow for every pin, and a phone: the
// registry's starting widths and minimums, Select, Priority, Key and Title pinned within 60% of a
// wide box with Title narrowing first and the rightmost unpinning, and Select, Key and Title
// within 85% of a phone's, Key at its phone width and Title taking what is left.
test("the List draws its columns at the registry's widths and pins at 1400 px", async () => {
  expect(await headerLayout(1400)).toEqual([
    "- 44px 0px",
    "Priority 140px 44px",
    "Key 108px 184px",
    "Title 380px 292px",
    "Queue 124px -",
    ...UNPINNED_REST,
  ]);
});

test("the List narrows Title and unpins from the right in a 700 px box", async () => {
  localStorage.setItem(
    "dispatch.project-table.CORE",
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

test("the List pins Select, Key and Title on a 390 px phone", async () => {
  expect(await headerLayout(390)).toEqual([
    "- 44px 0px",
    "Key 88px 44px",
    "Title 199.5px 132px",
    "Priority 140px -",
    "Queue 124px -",
    ...UNPINNED_REST,
  ]);
});

// A width the reader dragged on a desktop is the width the header is drawn at, and a stored one
// below a column's minimum is drawn at the minimum.
test("a stored desktop width reaches the List's header", async () => {
  localStorage.setItem(
    "dispatch.project-table.CORE",
    JSON.stringify({ sizing: { assignee: 300, status: 50 } })
  );
  const cells = await headerLayout(1400);
  expect(cells).toContain("Assignee 300px -");
  expect(cells).toContain("Status 150px -");
});

test("the Status filter counts Done once the closed issues are loaded", async () => {
  const user = userEvent.setup();
  table([issue("CORE-1"), issue("CORE-2", { status: "done" })], "?status=done");
  await screen.findByRole("row", { name: "CORE-2 Work CORE-2" });
  await user.click(screen.getByRole("button", { name: /^Filter Status/ }));
  expect(screen.getByRole("option", { name: "Done, 1 issue" })).toBeDefined();
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

// F7: the steps of a start-next are known before the first write, so the store is written once.
test("Start next writes the store once for the whole batch", async () => {
  const user = userEvent.setup();
  const send = spyOn(api, "send").mockResolvedValue(savedIssue("CORE-1") as never);
  restores.push(() => send.mockRestore());
  const rows = Array.from({ length: 10 }, (_, index) =>
    issue(`CORE-${index + 1}`, { labels: ["legion"], rank: String(index).padStart(3, "0") })
  );
  table(rows);
  await screen.findByRole("row", { name: "CORE-10 Work CORE-10" });
  await user.click(screen.getByRole("checkbox", { name: "Select CORE-10" }));
  const setItem = spyOn(Storage.prototype, "setItem");
  restores.push(() => setItem.mockRestore());
  await user.click(
    within(screen.getByRole("toolbar")).getByRole("button", { name: /^Start next$/ })
  );
  await waitFor(() => expect(send).toHaveBeenCalled());
  const records = setItem.mock.calls.filter(([, value]) =>
    String(value).includes('"kind":"startNext"')
  );
  // One write to begin the action and one for its whole batch of keys, not one per issue.
  expect(records.length).toBeLessThanOrEqual(2);
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
    issue("CORE-1", { labels: ["epic"], title: "Epic" }),
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
  writesOf(client).retry(refused.id);
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
    '{"sizing":{},"order":[],"visibility":{"updated":false,"project":false,"advances":false}}'
  );
  expect(localStorage.getItem("dispatch.project-table.priority:PRI-7")).toBeNull();
});

test("a scoped table offers Start next over a selection of two projects", async () => {
  const user = userEvent.setup();
  table(
    [
      issue("CORE-2", { advances: [{ id: "PRI-7", inherited: false }] }),
      issue("OPS-3", { advances: [{ id: "PRI-7", inherited: false }] }),
    ],
    "",
    undefined,
    { listing: { advances: "PRI-7" }, stateKey: "w" }
  );
  await user.click(await screen.findByRole("checkbox", { name: "Select CORE-2" }));
  await user.click(screen.getByRole("checkbox", { name: "Select OPS-3" }));
  // The selection is of two projects; what each issue can be written one at a time still is.
  expect(screen.getByRole("button", { name: "Start next" })).toBeDefined();
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
  const chip = screen.getByRole("button", { name: "Stop showing only Finished last week" });
  expect(chip.textContent).toContain("Finished last week");
  expect(chip.textContent).toContain("2 of 2 issues");
  await user.click(chip);
  await waitFor(() => expect(listedKeys().sort()).toEqual(["CORE-1", "CORE-2", "CORE-3"]));
  expect(screen.getByTestId("route-search").textContent).not.toContain("only=");
  expect(screen.queryByRole("button", { name: /^Stop showing only/ })).toBeNull();
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
