import { afterEach, expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
// biome-ignore lint/style/noRestrictedImports: the drag of a row onto another below is a drag and a drop, which user-event has no form for
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { api } from "../../api/client";
import type { IssueSummary, Priorities, Priority, PriorityRow } from "../../api/types";
import { SORT_MARK_WIDTH } from "../../components/table-layout";
import { KeymapProvider } from "../shell/KeymapProvider";
import { appKeymap } from "../shell/keymap";
import { PrioritiesPage } from "./PrioritiesPage";

function priority(id: string, overrides: Partial<PriorityRow> = {}): PriorityRow {
  return {
    id,
    title: `Priority ${id}`,
    description: "",
    owner: "INFRA controller",
    rank: id,
    state: "live",
    made_at: "2026-11-01T00:00:00Z",
    made_by: { kind: "user", id: "alice" },
    work: {
      by_status: null,
      claimed: 0,
      closed_last_7_days: 0,
      newest_event_at: null,
      open_asks: 0,
    },
    ...overrides,
  };
}

function body(overrides: Partial<Priorities> = {}): Priorities {
  return {
    priorities: [],
    work_under_no_priority: { by_project: null, count: 0, issues: null },
    priority_work_parked: { by_priority: null, count: 0, issues: null },
    ...overrides,
  };
}

/** The deployment's issues as the listing route answers them, standing in whatever status and
 *  advancing whatever priorities `advances` and `overrides` give. */
function issue(
  key: string,
  advances: string[] = [],
  overrides: Partial<IssueSummary> = {}
): IssueSummary {
  return {
    key,
    advances: advances.map((id) => ({ id, inherited: false })),
    advances_revision: 0,
    title: `Work ${key}`,
    status: "in_progress",
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
    closed_at: null,
    created_by: { kind: "user", id: "alice" },
    created_at: "2026-11-01T00:00:00Z",
    updated_at: "2026-11-01T00:00:00Z",
    ...overrides,
  };
}

const saved = (id: string): Priority => ({
  id,
  title: `Priority ${id}`,
  description: "",
  owner: "",
  rank: "n",
  state: "live",
  made_at: "2026-11-01T00:00:00Z",
  made_by: { kind: "user", id: "alice" },
});

const restores: (() => void)[] = [];
afterEach(() => {
  for (const restore of restores.splice(0)) restore();
  localStorage.clear();
});

/** The listing route as the server answers it: `advances` keeps the issues advancing any of the
 *  ids it names, `none` those advancing none and `any` those advancing one; `status` keeps the
 *  issues standing in one of its statuses; `open` leaves the closed out; and the move into
 *  backlog or icebox is carried only when `moved` asks for it. */
function answerListing(issues: readonly IssueSummary[]) {
  return async (options: Parameters<typeof api.listIssues>[0] = {}) =>
    issues
      .filter((row) => {
        const ids = (row.advances ?? []).map((relation) => relation.id);
        return (
          (options.advances === undefined ||
            options.advances.some((asked) =>
              asked === "none"
                ? ids.length === 0
                : asked === "any"
                  ? ids.length > 0
                  : ids.includes(asked)
            )) &&
          (options.status === undefined || options.status.includes(row.status)) &&
          (!options.open || row.status !== "done")
        );
      })
      .map(({ moved_at, moved_by, ...row }) =>
        options.moved ? { ...row, moved_at, moved_by } : row
      );
}

/** The page as a signed-in person sees it, with the reads it makes answered from `answer`; a
 *  reader who is not a person, and so may not write a priority, where `writer` is false. The
 *  page opens on `/priorities` with the address's `search`. */
function page(answer: Priorities, issues: IssueSummary[] = [], writer = true, search = "") {
  const priorities = spyOn(api, "listPriorities").mockResolvedValue(answer);
  const listing = spyOn(api, "listIssues").mockImplementation(answerListing(issues));
  const state = spyOn(api, "getMyState").mockResolvedValue({});
  const agents = spyOn(api, "listAgents").mockResolvedValue([]);
  // `useIsHuman`: the contract types only a person's answer, so the other principal is cast.
  const whoami = writer
    ? { event_head: "0", kind: "user" as const, login: "alice" }
    : ({ event_head: "0", kind: "token", login: "ci" } as never);
  const viewer = spyOn(api, "whoAmI").mockResolvedValue(whoami);
  const create = spyOn(api, "createPriority").mockResolvedValue(saved("new"));
  const patch = spyOn(api, "patchPriority").mockImplementation(async (id) => saved(id));
  // The unit DOM lays nothing out: give every table's box the height a window would.
  const descriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientHeight");
  Object.defineProperty(HTMLElement.prototype, "clientHeight", {
    configurable: true,
    get() {
      return this.dataset.testid?.endsWith("-scroll") ? 600 : (descriptor?.get?.call(this) ?? 0);
    },
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  client.setQueryData(["whoami"], whoami);
  const view = render(
    <MemoryRouter initialEntries={[`/priorities${search}`]}>
      <QueryClientProvider client={client}>
        <KeymapProvider>
          <PrioritiesPage />
        </KeymapProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  restores.push(() => {
    view.unmount();
    client.clear();
    for (const spy of [priorities, listing, state, agents, viewer, create, patch])
      spy.mockRestore();
    if (descriptor) Object.defineProperty(HTMLElement.prototype, "clientHeight", descriptor);
    else Reflect.deleteProperty(HTMLElement.prototype, "clientHeight");
  });
  return { create, listing, patch, view };
}

const FIFTEEN = body({
  priorities: [
    priority("legion-first", {
      title: "Legion works reliably first",
      description: "The first line.\nThe rest of it, which the row shows when it is open.",
      owner: "INFRA controller",
      work: {
        by_status: { in_progress: 5, testing: 1, needs_review: 1, retro: 0, todo: 9, triage: 3 },
        claimed: 2,
        closed_last_7_days: 22,
        newest_event_at: "2026-11-11T10:00:00Z",
        open_asks: 1,
      },
    }),
    priority("linkage-closure", {
      title: "Linkage closure eval",
      owner: "BET controller",
      work: {
        by_status: { in_progress: 1, backlog: 2, icebox: 1 },
        claimed: 0,
        closed_last_7_days: 2,
        newest_event_at: null,
        open_asks: 0,
      },
    }),
    priority("oss-verify", { title: "OSS Verify", owner: "OSSV controller" }),
  ],
});

// R13: the page has no subtitle, and each section heading reads its name and one count, with no
// rule and no line of counts under it.
test("the page draws the priorities table above both fault lists, each heading with one count", async () => {
  page(
    body({
      ...FIFTEEN,
      work_under_no_priority: { by_project: { CORE: 1, OPS: 1 }, count: 2, issues: null },
      priority_work_parked: { by_priority: { "linkage-closure": 1 }, count: 1, issues: null },
    }),
    [issue("CORE-1"), issue("OPS-1"), issue("CORE-9", ["linkage-closure"], { status: "backlog" })]
  );
  const priorities = await screen.findByRole("table", { name: "Priorities table" });
  const faults = await screen.findByRole("table", {
    name: "Issues active under no priority table",
  });
  const parked = await screen.findByRole("table", { name: "Parked priority issues table" });
  const headings = screen.getAllByRole("heading").map((heading) => heading.textContent);
  expect(headings).toEqual(["Priorities", "Active under no priority 2", "Parked priority work 1"]);
  // The page's name stands alone, with no subtitle under it.
  const title = screen.getByRole("heading", { level: 1, name: "Priorities" });
  expect(title.nextElementSibling).toBe(screen.getByRole("region", { name: "Priorities" }));
  expect(screen.queryByText(/Every project, read now/)).toBeNull();
  // Each heading carries its one count, and the issue table follows it with nothing between.
  for (const [region, count, list] of [
    ["Active under no priority", "2", "Issues active under no priority"],
    ["Parked priority work", "1", "Parked priority issues"],
  ] as const) {
    const section = screen.getByRole("region", { name: region });
    const heading = within(section).getByRole("heading", { level: 2 });
    expect(heading).toHaveAccessibleName(`${region} ${count}`);
    expect(heading.querySelector("span")?.textContent).toBe(count);
    expect(heading.nextElementSibling).toBe(within(section).getByRole("region", { name: list }));
  }
  // Neither the rules nor the counts by project or by priority are drawn.
  const text = document.body.textContent ?? "";
  expect(text).not.toMatch(/advances no priority|stands in the backlog|No reason is recorded/);
  expect(text).not.toMatch(/CORE 1|OPS 1|\d+ issues? ·/);
  // The priorities come first, which is what the page's own name says it is about.
  expect(priorities.compareDocumentPosition(faults) & Node.DOCUMENT_POSITION_FOLLOWING).toBe(
    Node.DOCUMENT_POSITION_FOLLOWING
  );
  expect(faults.compareDocumentPosition(parked) & Node.DOCUMENT_POSITION_FOLLOWING).toBe(
    Node.DOCUMENT_POSITION_FOLLOWING
  );
  // Each list holds its own fault's rows and no other's.
  expect(await within(faults).findByRole("row", { name: "CORE-1 Work CORE-1" })).toBeDefined();
  expect(within(faults).queryByRole("row", { name: "CORE-9 Work CORE-9" })).toBeNull();
  expect(await within(parked).findByRole("row", { name: "CORE-9 Work CORE-9" })).toBeDefined();
  expect(within(parked).queryByRole("row", { name: "CORE-1 Work CORE-1" })).toBeNull();
});

test("each fault table asks the issue listing for its statuses, and the parked one for the move", async () => {
  const user = userEvent.setup();
  const { listing } = page(FIFTEEN, [
    issue("CORE-1"),
    issue("OPS-12", [], { status: "needs_review" }),
    issue("CORE-9", ["linkage-closure"], {
      status: "backlog",
      moved_at: "2026-11-09T12:00:00Z",
      moved_by: { kind: "user", id: "bob" },
    }),
    issue("OPS-4", ["legion-first", "oss-verify"], { status: "icebox" }),
  ]);
  const faults = await screen.findByRole("table", {
    name: "Issues active under no priority table",
  });
  const parked = await screen.findByRole("table", { name: "Parked priority issues table" });
  // The narrowing is the server's: the no-priority list names the statuses work is done in, and
  // the parked list backlog and icebox with the move into them. Neither reads a closed issue.
  expect(listing).toHaveBeenCalledWith({
    advances: ["none"],
    status: ["in_progress", "testing", "needs_review", "retro"],
    open: true,
  });
  expect(listing).toHaveBeenCalledWith({
    advances: ["any"],
    status: ["backlog", "icebox"],
    moved: true,
    open: true,
  });
  // A fault read names its statuses every time it is made: no read of every closed issue goes
  // out beside it.
  const faultReads = listing.mock.calls.filter(([options]) =>
    ["none", "any"].includes(options?.advances?.[0] ?? "")
  );
  expect(faultReads.length).toBeGreaterThan(0);
  expect(faultReads.every(([options]) => options?.status !== undefined)).toBe(true);

  const cellsOf = async (table: HTMLElement, name: string) => {
    const headers = within(table)
      .getAllByRole("columnheader")
      .map((header) => header.textContent ?? "");
    const row = await within(table).findByRole("row", { name });
    const cells = within(row).getAllByRole("cell");
    return (header: string) => {
      const at = headers.findIndex((text) => text.startsWith(header));
      if (at === -1) throw new Error(`no ${header} column`);
      const cell = cells[at];
      if (cell === undefined) throw new Error(`no ${header} cell`);
      return cell;
    };
  };

  // The no-priority list names each row's project and when it last changed, which is how long it
  // has stood as it is (R6), and shows none of the parked columns.
  const unprioritized = await cellsOf(faults, "OPS-12 Work OPS-12");
  expect(unprioritized("Project").textContent).toBe("OPS");
  expect(within(unprioritized("Updated")).getByRole("time").getAttribute("datetime")).toBe(
    "2026-11-01T00:00:00Z"
  );
  const faultHeaders = within(faults)
    .getAllByRole("columnheader")
    .map((header) => header.textContent ?? "");
  expect(faultHeaders.some((text) => text.startsWith("Advances"))).toBe(false);
  expect(faultHeaders.some((text) => text.startsWith("Moved there"))).toBe(false);
  expect(faultHeaders.some((text) => text.startsWith("Reason"))).toBe(false);

  // The parked list names the project, each priority advanced by its title, linking to it, and
  // when the work was moved there and by whom. A row answered with no move draws an empty Moved
  // there cell: absent is absent, and that cell does not say a record is missing.
  const moved = await cellsOf(parked, "CORE-9 Work CORE-9");
  expect(moved("Project").textContent).toBe("CORE");
  expect(within(moved("Updated")).getByRole("time").getAttribute("datetime")).toBe(
    "2026-11-01T00:00:00Z"
  );
  expect(
    within(moved("Advances"))
      .getByRole("link", { name: "Linkage closure eval" })
      .getAttribute("href")
  ).toBe("/priorities/linkage-closure");
  expect(within(moved("Moved there")).getByRole("time").getAttribute("datetime")).toBe(
    "2026-11-09T12:00:00Z"
  );
  expect(moved("Moved there").textContent).toContain("bob");
  const unrecorded = await cellsOf(parked, "OPS-4 Work OPS-4");
  expect(
    within(unrecorded("Advances"))
      .getAllByRole("link")
      .map((link) => link.textContent)
  ).toEqual(["Legion works reliably first", "OSS Verify"]);
  expect(unrecorded("Moved there").textContent).toBe("");
  expect(within(unrecorded("Moved there")).queryByRole("time")).toBeNull();
  // Why it was moved there: nothing records that yet, so every row says so, naming the work that
  // records one as a link to it (R6).
  for (const reason of [moved("Reason"), unrecorded("Reason")]) {
    expect(reason.textContent).toBe("Not recorded until INFRA-2934");
    expect(within(reason).getByRole("link", { name: "INFRA-2934" }).getAttribute("href")).toBe(
      "/issues/INFRA-2934"
    );
  }

  // Nor does the list under no priority offer the parked columns among those a reader may show.
  await user.click(
    within(screen.getByRole("region", { name: "Active under no priority" })).getByRole("button", {
      name: "Columns",
    })
  );
  const menu = screen.getByRole("dialog", { name: "Columns" });
  expect(within(menu).getByRole("checkbox", { name: "Project" })).toBeChecked();
  for (const name of ["Advances", "Moved there", "Reason"])
    expect(within(menu).queryByRole("checkbox", { name })).toBeNull();
});

test("with no priority the page says so, and offers a writer the way to add the first", async () => {
  const user = userEvent.setup();
  const { create } = page(body({ empty: "No priority has been written yet." }));
  expect(await screen.findByText("No priority has been written yet.")).toBeDefined();
  expect(screen.queryByRole("table", { name: "Priorities table" })).toBeNull();
  await user.click(screen.getByRole("button", { name: "New priority" }));
  await user.type(screen.getByRole("textbox", { name: "Title of the new priority" }), "First");
  await user.click(screen.getByRole("button", { name: "Add priority" }));
  await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
  expect(create.mock.calls[0]?.[0]).toEqual({ title: "First" });
});

test("with no priority a reader who may not write one is shown only the sentence", async () => {
  page(body({ empty: "No priority has been written yet." }), [], false);
  expect(await screen.findByText("No priority has been written yet.")).toBeDefined();
  expect(screen.queryByRole("button", { name: "New priority" })).toBeNull();
});

/** The words of each column header of `table`, left to right: its sort button's, without the
 *  sort mark. */
function headerWords(table: HTMLElement): string[] {
  return within(table)
    .getAllByRole("columnheader")
    .map((header) => (header.querySelector("button")?.textContent ?? "").replace(/[▲▼]/g, ""));
}

// R13: "In flight", "Waiting" and "In backlog or icebox" read "Active", "Triage, Todo" and
// "Backlog, icebox", and count as they did; no header is longer than 16 characters.
test("a row counts the work Active, in Triage, Todo, in Backlog, icebox and closed in 7 days", async () => {
  page(FIFTEEN);
  const row = await screen.findByRole("row", { name: "P1 Legion works reliably first" });
  const headers = headerWords(screen.getByRole("table", { name: "Priorities table" }));
  expect(headers).toEqual([
    "Order",
    "Priority",
    "Owner",
    "Active",
    "Triage, Todo",
    "Backlog, icebox",
    "Closed, 7 days",
    "Newest event",
  ]);
  expect(headers.filter((header) => header.length > 16)).toEqual([]);
  const cells = within(row)
    .getAllByRole("cell")
    .map((cell) => cell.textContent);
  // Active is in_progress, testing, needs_review and retro; Triage, Todo is those two; Backlog,
  // icebox is those two; the fourth is what the read says closed in 7 days.
  expect(cells.slice(3, 7)).toEqual(["7", "12", "0", "22"]);
  // The newest event is read as a time, whose words follow the clock.
  expect(within(row).getByRole("time").getAttribute("datetime")).toBe("2026-11-11T10:00:00Z");
  const second = screen.getByRole("row", { name: "P2 Linkage closure eval" });
  expect(
    within(second)
      .getAllByRole("cell")
      .map((cell) => cell.textContent)
      .slice(3)
  ).toEqual(["1", "0", "3", "2", "Never"]);
});

// R13: Active names the four statuses In progress, Testing, Needs review and Retro, and no other
// label of the page uses the word: not the strip's status names, not the claim chip, not another
// heading. No heading, column header or strip entry says "In flight" or "running" for that set.
test("no label of the page but the four-status set's uses Active, and none says In flight", async () => {
  page(
    body({
      ...FIFTEEN,
      work_under_no_priority: { by_project: { CORE: 1 }, count: 1, issues: null },
    }),
    [
      issue("CORE-1", [], {
        claim: { actor: { kind: "user", id: "bob" }, at: "2026-11-10T00:00:00Z" },
      }),
    ]
  );
  const section = await screen.findByRole("region", { name: "Active under no priority" });
  const faults = await within(section).findByRole("table", {
    name: "Issues active under no priority table",
  });
  const row = await within(faults).findByRole("row", { name: "CORE-1 Work CORE-1" });
  // The strip names the status itself, and the claim chip names its holder.
  const strip = within(section).getByRole("group", { name: "Issues by status" });
  expect(within(strip).getByRole("button").textContent).toBe("In progress 1");
  expect(row.textContent).toContain("Claimed by");
  // Every text the page draws, and every name it gives an element.
  const texts: string[] = [];
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
  for (let node = walker.nextNode(); node !== null; node = walker.nextNode())
    texts.push((node.textContent ?? "").trim());
  const labels = [...document.body.querySelectorAll("[aria-label]")].map(
    (element) => element.getAttribute("aria-label") ?? ""
  );
  // Active is the priorities table's column, its filter and the heading of the work in those
  // statuses under no priority, with that heading's table: nothing else.
  expect(texts.filter((text) => /active/i.test(text))).toEqual([
    "Active",
    "Active under no priority",
  ]);
  expect(labels.filter((label) => /active/i.test(label))).toEqual([
    "Filter Active",
    "Active under no priority",
    "Issues active under no priority",
    "Issues active under no priority table",
  ]);
  const named = [
    ...screen.getAllByRole("heading"),
    ...screen.getAllByRole("columnheader"),
    ...within(strip).getAllByRole("button"),
  ].map((element) => element.textContent ?? "");
  expect([...named, ...labels].filter((text) => /in flight|running/i.test(text))).toEqual([]);
});

test("the first line of a description shows on a closed row and the whole of it when it opens", async () => {
  const user = userEvent.setup();
  page(FIFTEEN, [issue("CORE-1", ["legion-first"]), issue("CORE-2")]);
  await screen.findByRole("row", { name: "P1 Legion works reliably first" });
  expect(screen.getByText("The first line.")).toBeDefined();
  await user.click(screen.getByRole("button", { name: "Open P1 Legion works reliably first" }));
  // Open, the row reads the whole description rendered, and a click on its text opens its source.
  const rendered = await screen.findByRole("group", { name: "Description of P1" });
  expect(await within(rendered).findByText(/The rest of it/)).toBeDefined();
  await user.click(within(rendered).getByText(/The rest of it/));
  const whole = await screen.findByRole("textbox", { name: "Description of P1" });
  expect((whole as HTMLTextAreaElement).value).toBe(
    "The first line.\nThe rest of it, which the row shows when it is open."
  );
});

test("opening a row lists the work advancing that priority, over every project", async () => {
  const user = userEvent.setup();
  const { listing } = page(FIFTEEN, [issue("CORE-1", ["legion-first"]), issue("OPS-3")]);
  await screen.findByRole("row", { name: "P1 Legion works reliably first" });
  await user.click(screen.getByRole("button", { name: "Open P1 Legion works reliably first" }));
  const work = await screen.findByRole("region", {
    name: "Work advancing Legion works reliably first",
  });
  expect(await within(work).findByRole("row", { name: "CORE-1 Work CORE-1" })).toBeDefined();
  expect(within(work).queryByRole("row", { name: "OPS-3 Work OPS-3" })).toBeNull();
  // The narrowing is the server's, and it names no project: a priority belongs to none.
  expect(listing).toHaveBeenCalledWith({ advances: ["legion-first"], open: true });
});

test("a person who may write adds a priority", async () => {
  const user = userEvent.setup();
  const { create } = page(FIFTEEN);
  await screen.findByRole("row", { name: "P1 Legion works reliably first" });
  await user.click(screen.getByRole("button", { name: "New priority" }));
  await user.type(
    screen.getByRole("textbox", { name: "Title of the new priority" }),
    "Spend ruthlessly"
  );
  await user.click(screen.getByRole("button", { name: "Add priority" }));
  await waitFor(() => expect(create).toHaveBeenCalledTimes(1));
  expect(create.mock.calls[0]?.[0]).toEqual({ title: "Spend ruthlessly" });
});

test("a title is renamed where it is read, with no edit mode and no Save button", async () => {
  const user = userEvent.setup();
  const { patch } = page(FIFTEEN);
  const field = await screen.findByRole("textbox", { name: "Title of P2" });
  await user.clear(field);
  await user.type(field, "Linkage closure, restarted");
  await user.tab();
  await waitFor(() => expect(patch).toHaveBeenCalledTimes(1));
  expect(patch.mock.calls[0]?.slice(0, 2)).toEqual([
    "linkage-closure",
    { title: "Linkage closure, restarted" },
  ]);
  expect(screen.queryByRole("button", { name: /^Save/ })).toBeNull();
});

test("a description is written on the open row: the empty block is the prompt that opens it", async () => {
  const user = userEvent.setup();
  const { patch } = page(FIFTEEN);
  await screen.findByRole("row", { name: "P3 OSS Verify" });
  await user.click(screen.getByRole("button", { name: "Open P3 OSS Verify" }));
  const rendered = await screen.findByRole("group", { name: "Description of P3" });
  const prompt = within(rendered).getByText(/No description is written\./);
  expect(prompt.textContent).toBe("No description is written. Click here to write one.");
  await user.click(prompt);
  const field = await screen.findByRole("textbox", { name: "Description of P3" });
  await user.type(field, "What it is.");
  await user.tab();
  await waitFor(() => expect(patch).toHaveBeenCalledTimes(1));
  expect(patch.mock.calls[0]?.slice(0, 2)).toEqual(["oss-verify", { description: "What it is." }]);
  // Left, the description reads rendered again, with what was written.
  const back = await screen.findByRole("group", { name: "Description of P3" });
  expect(await within(back).findByText("What it is.")).toBeDefined();
});

test("dragging a row onto another writes the place it landed", async () => {
  const { patch } = page(FIFTEEN);
  const third = await screen.findByRole("row", { name: "P3 OSS Verify" });
  const first = screen.getByRole("row", { name: "P1 Legion works reliably first" });
  // user-event has no form for a drag: it drives pointer events, and the order is moved by the
  // HTML drag-and-drop events the handle listens for.
  fireEvent.dragStart(
    within(third).getByRole("button", {
      name: "Move P3 OSS Verify: drag it, or press the up and down arrows",
    })
  );
  // user-event has no form for a drag.
  fireEvent.dragOver(first);
  // user-event has no form for a drop.
  fireEvent.drop(first);
  await waitFor(() => expect(patch).toHaveBeenCalledTimes(1));
  // Dragged up, it lands above the row it was dropped on.
  expect(patch.mock.calls[0]?.slice(0, 2)).toEqual([
    "oss-verify",
    { rank: { before: "legion-first" } },
  ]);
});

test("the keyboard moves a priority one place, as the drag does", async () => {
  const user = userEvent.setup();
  const { patch } = page(FIFTEEN);
  const first = await screen.findByRole("row", { name: "P1 Legion works reliably first" });
  within(first)
    .getByRole("button", {
      name: "Move P1 Legion works reliably first: drag it, or press the up and down arrows",
    })
    .focus();
  await user.keyboard("{ArrowDown}");
  await waitFor(() => expect(patch).toHaveBeenCalledTimes(1));
  expect(patch.mock.calls[0]?.slice(0, 2)).toEqual([
    "legion-first",
    { rank: { after: "linkage-closure" } },
  ]);
});

test("the table's own view is in the address under its prefix", async () => {
  const user = userEvent.setup();
  page(FIFTEEN);
  await screen.findByRole("row", { name: "P1 Legion works reliably first" });
  await user.type(screen.getByRole("searchbox", { name: "Search priorities" }), "linkage");
  await waitFor(() =>
    expect(screen.queryByRole("row", { name: "P1 Legion works reliably first" })).toBeNull()
  );
  expect(screen.getByRole("row", { name: "P2 Linkage closure eval" })).toBeDefined();
});

test("the page carries no estimate, no forecast, no percentage and no written status", async () => {
  page(FIFTEEN);
  await screen.findByRole("row", { name: "P1 Legion works reliably first" });
  const text = document.body.textContent ?? "";
  expect(/\d+%/.test(text)).toBe(false);
  expect(/estimate|forecast|on track|at risk/i.test(text)).toBe(false);
});

/** The page with two rows in each of its tables: the priorities, the work active under no
 *  priority, the parked priority work, and the work advancing P1 once that row is opened. */
function everyTableTwice(): void {
  page(
    body({
      priorities: [
        priority("legion-first", { title: "Legion works reliably first" }),
        priority("linkage-closure", { title: "Linkage closure eval" }),
      ],
      work_under_no_priority: { by_project: { CORE: 2 }, count: 2, issues: null },
      priority_work_parked: { by_priority: { "linkage-closure": 2 }, count: 2, issues: null },
    }),
    [
      issue("CORE-1"),
      issue("CORE-2"),
      issue("CORE-8", ["linkage-closure"], { status: "backlog" }),
      issue("CORE-9", ["linkage-closure"], { status: "icebox" }),
      issue("OPS-1", ["legion-first"]),
      issue("OPS-2", ["legion-first"]),
    ]
  );
}

/** Opens P1, and gives back the region of the work advancing it once its rows are drawn. */
async function openFirst(user: UserEvent): Promise<HTMLElement> {
  await user.click(
    await screen.findByRole("button", { name: "Open P1 Legion works reliably first" })
  );
  const work = await screen.findByRole("region", {
    name: "Work advancing Legion works reliably first",
  });
  await within(work).findByRole("row", { name: "OPS-2 Work OPS-2" });
  return work;
}

// R13: the pager under every table of the page draws the range of rows, the page, its steps and
// the page size, and no total, as on the Issues tab.
test("every table's pager draws no total", async () => {
  const user = userEvent.setup();
  everyTableTwice();
  await screen.findByRole("row", { name: "CORE-2 Work CORE-2" });
  await screen.findByRole("row", { name: "CORE-9 Work CORE-9" });
  const work = await openFirst(user);
  const pagerOf = (name: string) =>
    within(screen.getByRole("region", { name })).getByTestId("issues-table-pager");
  // Each pager with the lines it draws: the work under an opened row opens grouped by status, so
  // its two rows stand under one heading.
  const pagers: [HTMLElement, string][] = [
    [screen.getByTestId("priorities-table-pager"), "Rows 1–2"],
    [pagerOf("Issues active under no priority"), "Rows 1–2"],
    [pagerOf("Parked priority issues"), "Rows 1–2"],
    [within(work).getByTestId("issues-table-pager"), "Rows 1–3"],
  ];
  for (const [pager, range] of pagers) {
    expect(within(pager).getByText(range)).toBeDefined();
    expect(within(pager).getByText("Page 1 of 1")).toBeDefined();
    expect(pager.textContent).not.toMatch(/of [23]|priorit|issue/);
  }
});

// R13: each table's display menu reads Group by, the priorities table's own and every issue
// table's beside it; no table draws view tabs; and each sortable header's hint is in the shared
// tooltip, with no native title on its sort button (R11).
test("every table names its display menu Group by, draws no view tab, and its sort buttons no title", async () => {
  const user = userEvent.setup();
  everyTableTwice();
  await screen.findByRole("row", { name: "CORE-2 Work CORE-2" });
  await screen.findByRole("row", { name: "CORE-9 Work CORE-9" });
  for (const region of ["Priorities", "Active under no priority", "Parked priority work"]) {
    const menu = within(screen.getByRole("region", { name: region })).getByRole("button", {
      name: "Group by",
    });
    await user.click(menu);
    const panel = screen.getByRole("dialog", { name: "Group by" });
    expect(within(panel).getByRole("radio", { name: "Nothing" })).toBeDefined();
    await user.keyboard("{Escape}");
  }
  const work = await openFirst(user);
  expect(within(work).getByRole("button", { name: "Group by" })).toBeDefined();
  expect(screen.getAllByRole("button", { name: "Group by" })).toHaveLength(4);
  expect(screen.queryByRole("button", { name: "View" })).toBeNull();
  // No table draws the Issues tab's views: no tab row, no tab, no Table and Board switch.
  expect(screen.queryByRole("tablist")).toBeNull();
  expect(screen.queryAllByRole("tab")).toEqual([]);
  expect(screen.queryByRole("group", { name: "Layout" })).toBeNull();
  expect(screen.queryByRole("button", { name: "Board" })).toBeNull();
  // A sort button is every header button that is no control of its own, such as a filter.
  const tables = screen.getAllByRole("table");
  expect(tables).toHaveLength(4);
  for (const table of tables) {
    const sorts = [...table.querySelectorAll("th button:not([data-control])")];
    expect(sorts.length, table.getAttribute("aria-label") ?? "").toBeGreaterThan(3);
    expect(sorts.filter((button) => button.hasAttribute("title"))).toEqual([]);
  }
  await user.hover(
    within(screen.getByRole("table", { name: "Priorities table" })).getByRole("button", {
      name: "Active",
    })
  );
  expect(screen.getByRole("tooltip").textContent).toBe(
    "Sort (shift-click to add a second sort); drag to move the column"
  );
});

// R13: the embedded issue tables' Open asks filter picks With open asks or No open asks.
test("each issue table's Open asks filter picks With open asks or No open asks", async () => {
  const user = userEvent.setup();
  everyTableTwice();
  await screen.findByRole("row", { name: "CORE-2 Work CORE-2" });
  await screen.findByRole("row", { name: "CORE-9 Work CORE-9" });
  const work = await openFirst(user);
  for (const table of [
    screen.getByRole("table", { name: "Issues active under no priority table" }),
    screen.getByRole("table", { name: "Parked priority issues table" }),
    within(work).getByRole("table"),
  ]) {
    await user.click(within(table).getByRole("button", { name: "Filter Open asks" }));
    const picks = screen.getByRole("listbox", { name: "Open asks options" });
    expect(
      within(picks)
        .getAllByRole("option")
        .map((option) => option.textContent?.replace(/\d+$/, ""))
    ).toEqual(["With open asks", "No open asks"]);
    await user.keyboard("{Escape}");
  }
});

test("a fault row tells no reader that an issue nobody claims is claimed", async () => {
  page(FIFTEEN, [
    issue("CORE-1", [], { assignee: "alice" }),
    issue("CORE-2", [], {
      assignee: "alice",
      claim: { actor: { kind: "user", id: "bob" }, at: "2026-11-10T00:00:00Z" },
    }),
  ]);
  const faults = await screen.findByRole("table", {
    name: "Issues active under no priority table",
  });
  await within(faults).findByRole("row", { name: "CORE-1 Work CORE-1" });
  const headers = within(faults)
    .getAllByRole("columnheader")
    .map((header) => header.textContent ?? "");
  const claimedBy = headers.findIndex((header) => header.includes("Claimed by"));
  expect(claimedBy).not.toBe(-1);
  const cellUnder = (key: string) =>
    within(within(faults).getByRole("row", { name: `${key} Work ${key}` })).getAllByRole("cell")[
      claimedBy
    ]?.textContent ?? "";
  // The Issues table reads "Claimed by" as the claim alone (its ClaimChip). Under that header
  // an unclaimed issue names nobody, whoever it is assigned to, and a claimed one its claimant.
  expect(cellUnder("CORE-1")).not.toContain("alice");
  expect(cellUnder("CORE-2")).toContain("bob");
});

test("a grouped table counts each group in the plural its rows are called by", async () => {
  const user = userEvent.setup();
  page(
    body({
      priorities: [
        priority("legion-first", { title: "Legion works reliably first" }),
        priority("oss-verify", { title: "OSS Verify" }),
      ],
      work_under_no_priority: { by_project: { CORE: 2 }, count: 2, issues: null },
    }),
    [issue("CORE-1"), issue("CORE-2")]
  );
  await screen.findByRole("row", { name: "P1 Legion works reliably first" });
  // Group by is each table's own display menu (`components/DisplayMenu.tsx`), named for the one
  // section it holds, the same menu the project List draws.
  const groupBy = async (region: string, column: string) => {
    await user.click(
      within(screen.getByRole("region", { name: region })).getByRole("button", {
        name: "Group by",
      })
    );
    await user.click(
      within(screen.getByRole("dialog", { name: "Group by" })).getByRole("radio", {
        name: column,
      })
    );
  };
  // No rule makes a plural from a singular: adding "s" to "priority" wrote "2 prioritys", so each
  // table names both words, as its pager does. The priorities table's heading is the one group
  // row every table draws (`DataTableGroupRow`).
  await groupBy("Priorities", "Owner");
  expect(
    await screen.findByRole("button", { name: "Collapse INFRA controller, 2 priorities" })
  ).toBeDefined();
  await screen.findByRole("row", { name: "CORE-1 Work CORE-1" });
  await groupBy("Active under no priority", "Status");
  expect(
    await screen.findByRole("button", { name: "Collapse In progress, 2 issues" })
  ).toBeDefined();
});

// Under 640 px a header with its sort and its filter side by side leaves a narrow column's name
// unreadable, so the title's header stacks the two. The core stacks only the column its caller
// names, and this table names its `title`, as the project List does.
test("the priorities table stacks its title's header in a box under 640 px", async () => {
  // On `Element.prototype`, which owns the method: bun's `mockRestore` leaves a copy on a
  // prototype that only inherits it, which would hide every later test's spy on `Element`.
  const rect = spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (
    this: HTMLElement
  ) {
    return new DOMRect(0, 0, this.dataset.testid?.endsWith("-scroll") ? 390 : 100, 40);
  });
  restores.push(() => rect.mockRestore());
  page(FIFTEEN);
  const table = await screen.findByRole("table", { name: "Priorities table" });
  await waitFor(() =>
    expect(
      within(table)
        .getByRole("columnheader", { name: /^Priority/ })
        .querySelector("span")
    ).toHaveClass("flex-col")
  );
  expect(
    within(table)
      .getByRole("columnheader", { name: /^Owner/ })
      .querySelector("span")
  ).not.toHaveClass("flex-col");
});

// dispatch://INFRA-3221/comment/141bbad2-b75e-4bb4-b0a7-b5b1af541a3c: Order's minimum holds its
// header whole, its label and its filter button, and while the table is sorted by it, as the
// table opens, its sort mark too. Measured in Chromium (builds Job infra-3221-t16c-measure-rp2nn):
// the label is 43 px, in a 44 px button below 1280 px, and 52 px with the mark; the filter button
// is 44 px there, the gap between them 4 px and the cell's padding 16 px.
test("the priorities table draws its Order header whole, sorted by it and not", async () => {
  const user = userEvent.setup();
  // On `Element.prototype`, which owns the method (see above).
  const rect = spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (
    this: HTMLElement
  ) {
    return new DOMRect(0, 0, this.dataset.testid?.endsWith("-scroll") ? 1400 : 100, 40);
  });
  restores.push(() => rect.mockRestore());
  page(FIFTEEN);
  const table = await screen.findByRole("table", { name: "Priorities table" });
  const header = (name: RegExp) => within(table).getByRole("columnheader", { name });
  const order = () => Number.parseFloat(header(/^Order/).style.width);
  expect(header(/^Order/)).toHaveAttribute("aria-sort", "ascending");
  expect(order()).toBeGreaterThanOrEqual(52 + 4 + 44 + 16);
  expect(order()).toBe(108 + SORT_MARK_WIDTH);

  await user.click(within(table).getByRole("button", { name: /^Priority/ }));
  await waitFor(() => expect(header(/^Priority/)).toHaveAttribute("aria-sort", "ascending"));
  expect(header(/^Order/)).not.toHaveAttribute("aria-sort");
  expect(order()).toBeGreaterThanOrEqual(44 + 4 + 44 + 16);
  expect(order()).toBe(108);
});

// INFRA-3135's review round 1, R10: the priorities table registered `x`, "Select or deselect the
// priority", though the table selects nothing, so the shortcut help listed a key that did
// nothing. It registers no select key, as the Delivery list does.
test("the priorities table registers no select key, so the shortcut help lists no x for it", async () => {
  page(FIFTEEN);
  await screen.findByRole("row", { name: "P1 Legion works reliably first" });
  expect(
    appKeymap
      .describe()
      .filter((entry) => entry.id.startsWith("priorities-"))
      .map((entry) => [entry.id, entry.keys])
  ).toEqual([
    ["priorities-next", ["j"]],
    ["priorities-previous", ["k"]],
    ["priorities-open", ["o"]],
    ["priorities-open-enter", ["Enter"]],
  ]);
});

/** The rows `table` draws under `attribute`, in the order it draws them. */
function rowsOf(table: HTMLElement, attribute: string): HTMLElement[] {
  return [...table.querySelectorAll<HTMLElement>(`[${attribute}]`)];
}

// INFRA-3135's review round 1, R10: the page draws the priorities table and an issue table for
// each fault, and one for an opened row's work, and every one of them registered `j` and `k`
// page-wide in the `project` scope, a scope this page never pushed: measured here before the
// change, `j` moved the focus nowhere at all. The page pushes the scope its tables register in;
// the priorities table, the page's main one, walks its rows from anywhere the focus is in no
// table; and each issue table walks its own rows from one of them.
test("j and k walk the priorities from outside every table, and each issue table's rows from one of its own", async () => {
  const user = userEvent.setup();
  page(
    body({
      ...FIFTEEN,
      work_under_no_priority: { by_project: { CORE: 2 }, count: 2, issues: null },
    }),
    [
      issue("CORE-1"),
      issue("CORE-2"),
      issue("OPS-1", ["legion-first"]),
      issue("OPS-2", ["legion-first"]),
    ]
  );
  const priorities = await screen.findByRole("table", { name: "Priorities table" });
  const faults = await screen.findByRole("table", {
    name: "Issues active under no priority table",
  });
  await within(faults).findByRole("row", { name: "CORE-2 Work CORE-2" });
  const priorityRows = rowsOf(priorities, "data-priority-row");
  const faultRows = rowsOf(faults, "data-issue-row");

  // From the page, where no table holds the focus: the priorities.
  await user.keyboard("j");
  expect(priorityRows[0], "j from the page").toHaveFocus();
  await user.keyboard("j");
  expect(priorityRows[1], "a second j").toHaveFocus();
  // From a fault list's row: that list's own rows, not the priorities.
  act(() => faultRows[0]?.focus());
  await user.keyboard("j");
  expect(faultRows[1], "j from a fault row").toHaveFocus();
  await user.keyboard("k");
  expect(faultRows[0], "k from a fault row").toHaveFocus();

  // The work under an opened row is a table of its own: its rows walk it.
  await user.click(screen.getByRole("button", { name: "Open P1 Legion works reliably first" }));
  const work = await screen.findByRole("region", {
    name: "Work advancing Legion works reliably first",
  });
  await within(work).findByRole("row", { name: "OPS-2 Work OPS-2" });
  const workRows = rowsOf(work, "data-issue-row");
  act(() => workRows[0]?.focus());
  await user.keyboard("j");
  expect(workRows[1], "j from a row of the opened work").toHaveFocus();
});

// An issue table's toolbar and pager stand outside its box but are that table's, so the
// priorities' page-wide j and k leave a focus on them where it is, as the merge base's page did.
test("j and k pressed on an issue table's toolbar or pager walk no priority and leave the focus there", async () => {
  const user = userEvent.setup();
  page(
    body({
      ...FIFTEEN,
      work_under_no_priority: { by_project: { CORE: 2 }, count: 2, issues: null },
    }),
    [issue("CORE-1"), issue("CORE-2")]
  );
  const faults = await screen.findByRole("table", {
    name: "Issues active under no priority table",
  });
  await within(faults).findByRole("row", { name: "CORE-2 Work CORE-2" });
  const section = screen.getByRole("region", { name: "Issues active under no priority" });
  const controls = ["Columns", "Group by", "Next ›"].map((name) =>
    within(section).getByRole("button", { name })
  );
  for (const control of controls) {
    for (const key of ["j", "k"]) {
      act(() => control.focus());
      await user.keyboard(key);
      expect(control, `${key} on ${control.textContent}`).toHaveFocus();
    }
  }
});

// R1, the architect's chip rule: every narrowing that is on is visible and removable from the
// control row, whatever set it. This table's narrowings are all its columns', set on their own
// headers, and the reader may hide a column with the columns menu, which leaves its filter
// narrowing the table with no header to show it. So the row draws a chip for each.
test("a column filter of the priorities table is a chip of its row, even while its column is hidden", async () => {
  const user = userEvent.setup();
  page(FIFTEEN);
  // The page draws a table for each fault list beside this one, each with a Columns menu and a
  // row of its own, so every control here is read inside the priorities section.
  const section = await screen.findByRole("region", { name: "Priorities" });
  const table = await within(section).findByRole("table", { name: "Priorities table" });
  /** How many priorities the table lists, its header row left out. */
  const listed = () => within(table).getAllByRole("row").length - 1;
  const before = listed();

  await user.click(within(table).getByRole("button", { name: "Filter Owner" }));
  await user.click(await screen.findByRole("option", { name: /^BET controller/ }));
  await user.keyboard("{Escape}");
  await waitFor(() => expect(listed()).toBe(1));
  expect(within(table).getByRole("row", { name: /Linkage closure eval/ })).toBeDefined();
  const chip = () =>
    within(section).getByRole("button", { name: "Remove Owner: BET controller filter" });
  expect(chip()).toBeDefined();

  // Hidden, the column takes its header and its filter control off the page; the chip is then
  // the one place the narrowing shows, and its press gives every row back.
  await user.click(within(section).getByRole("button", { name: "Columns" }));
  await user.click(screen.getByRole("checkbox", { name: "Owner" }));
  await user.keyboard("{Escape}");
  await waitFor(() =>
    expect(within(table).queryByRole("button", { name: "Filter Owner" })).toBeNull()
  );
  expect(listed()).toBe(1);
  await user.click(chip());
  await waitFor(() => expect(listed()).toBe(before));
  expect(screen.queryByRole("button", { name: /^Remove Owner: / })).toBeNull();
});

// R14: a pick in another case than its option's own is that option, ticked once, and it lists
// that option's rows: the table matches a pick without regard to case, as its filter ticks it.
test("a priorities filter pick in another case lists its option's rows, ticked once", async () => {
  const user = userEvent.setup();
  page(FIFTEEN, [], true, "?p.f.owner=bet%20controller");
  const section = await screen.findByRole("region", { name: "Priorities" });
  const table = await within(section).findByRole("table", { name: "Priorities table" });
  await waitFor(() => expect(within(table).getAllByRole("row")).toHaveLength(2));
  expect(within(table).getByRole("row", { name: /Linkage closure eval/ })).toBeDefined();

  await user.click(within(table).getByRole("button", { name: "Filter Owner: 1 picked" }));
  const offered = await screen.findAllByRole("option", { name: /^BET controller/i });
  expect(offered).toHaveLength(1);
  expect(offered[0]).toHaveAttribute("aria-selected", "true");
});
