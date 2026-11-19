import { afterEach, expect, spyOn, test } from "bun:test";
import { ISSUE_TITLE_MAX } from "@legion/contracts";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Link, MemoryRouter, Route, Routes } from "react-router-dom";
import { onFakeClock } from "../../__tests__/fake-clock";
import { recordedEditors, selectText, writeAtEnd } from "../../__tests__/recorded-editors";
import { answerPriorityPatches } from "../../__tests__/send-answers";
import { ApiError, api, priorityPatchRequest } from "../../api/client";
import { prioritiesQuery } from "../../api/queries";
import type { IssueSummary, Priorities, Priority, PriorityRow } from "../../api/types";
import { DocumentRuntime } from "../doc/runtime";
import { PendingWritesIndicator } from "../writes/PendingWritesIndicator";
import { writesOf } from "../writes/write-store";
import { PriorityPage } from "./PriorityPage";
import { PRIORITIES_PATH } from "./priority-routes";

function priority(id: string, overrides: Partial<PriorityRow> = {}): PriorityRow {
  return {
    id,
    title: `Priority ${id}`,
    description: "",
    owner: "BET and LINUX controllers",
    rank: id,
    state: "live",
    made_at: "2026-11-10T09:00:00Z",
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

function issue(key: string, advances: string[] = []): IssueSummary {
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
  };
}

const ROWS: PriorityRow[] = [
  priority("legion-first", { title: "Legion works reliably first", owner: "INFRA controller" }),
  priority("rocq-lean", {
    title: "Rocq and Lean speed work restarted",
    description: "# Rocq and Lean speed work restarted\n\nSpeedruns run again.",
    work: {
      by_status: { in_progress: 2, testing: 1, todo: 11, backlog: 4, icebox: 1 },
      claimed: 1,
      closed_last_7_days: 9,
      newest_event_at: "2026-11-11T08:00:00Z",
      open_asks: 0,
    },
  }),
  priority("runbook", {
    title: "The fleet has a runbook",
    description: "Read [the runbook](https://example.com/runbook) before a roll-out.",
  }),
];

const BODY: Priorities = {
  priorities: ROWS,
  work_under_no_priority: { by_project: null, count: 0, issues: null },
  priority_work_parked: { by_priority: null, count: 0, issues: null },
};

const saved = (id: string): Priority => ({
  id,
  title: "Rocq and Lean speed work restarted",
  description: "",
  owner: "",
  rank: "n",
  state: "live",
  made_at: "2026-11-10T09:00:00Z",
  made_by: { kind: "user", id: "alice" },
});

const restores: (() => void)[] = [];
afterEach(() => {
  for (const restore of restores.splice(0)) restore();
  localStorage.clear();
});

function page(id = "rocq-lean", issues: IssueSummary[] = [], writer = true) {
  const priorities = spyOn(api, "listPriorities").mockResolvedValue(BODY);
  const listing = spyOn(api, "listIssues").mockImplementation(async (options = {}) =>
    issues.filter(
      (row) =>
        options.advances === undefined ||
        (row.advances ?? []).some((relation) => options.advances?.includes(relation.id))
    )
  );
  const state = spyOn(api, "getMyState").mockResolvedValue({});
  const agents = spyOn(api, "listAgents").mockResolvedValue([]);
  // A reader who may not write a priority is any principal that is not a person
  // (`useIsHuman`): the contract types only the person's answer, so the other is cast.
  const whoami = writer
    ? { event_head: "0", kind: "user" as const, login: "alice" }
    : ({ event_head: "0", kind: "token", login: "ci" } as never);
  const viewer = spyOn(api, "whoAmI").mockResolvedValue(whoami);
  // A change of a priority goes on its priority's line, made from its request alone
  // (`priorityPatchRequest`), so it is answered on its route.
  const patch = answerPriorityPatches().mockImplementation(async (id) => saved(id));
  // The margin reads the priority's own history as soon as the page opens.
  const history = spyOn(api, "getPriorityHistory").mockResolvedValue([]);
  const descriptor = Object.getOwnPropertyDescriptor(HTMLElement.prototype, "clientHeight");
  Object.defineProperty(HTMLElement.prototype, "clientHeight", {
    configurable: true,
    get() {
      return this.dataset.testid?.endsWith("-scroll") ? 600 : (descriptor?.get?.call(this) ?? 0);
    },
  });
  const signedIn = () => {
    const app = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    app.setQueryData(["whoami"], whoami);
    return app;
  };
  // The description is drawn in the documents' own editor, whose handles a test edits through.
  const editors = recordedEditors();
  // The page, the priorities page its link leads to - a stand-in with a way back - and the
  // shell's line of pending writes, which stands above the router as the app's does.
  const draw = (app: QueryClient) =>
    render(
      <MemoryRouter initialEntries={[`/priorities/${id}`]}>
        <QueryClientProvider client={app}>
          <DocumentRuntime.Provider value={editors.runtime}>
            <Routes>
              <Route element={<PriorityPage />} path="/priorities/:id" />
              <Route
                element={<Link to={`/priorities/${id}`}>Back to the priority</Link>}
                path={PRIORITIES_PATH}
              />
            </Routes>
            <PendingWritesIndicator />
          </DocumentRuntime.Provider>
        </QueryClientProvider>
      </MemoryRouter>
    );
  let client = signedIn();
  let view = draw(client);
  restores.push(() => {
    view.unmount();
    client.clear();
    for (const spy of [priorities, listing, state, agents, viewer, patch, history])
      spy.mockRestore();
    if (descriptor) Object.defineProperty(HTMLElement.prototype, "clientHeight", descriptor);
    else Reflect.deleteProperty(HTMLElement.prototype, "clientHeight");
  });
  /** Another person's write of the priority on the page reaching it, as a refresh of its read
   *  does. */
  const writtenElsewhere = (overrides: Partial<PriorityRow>) => {
    const written = {
      ...BODY,
      priorities: ROWS.map((row) => (row.id === id ? { ...row, ...overrides } : row)),
    };
    priorities.mockResolvedValue(written);
    act(() => client.setQueryData(prioritiesQuery().queryKey, written));
  };
  /** The page reloaded on the tab it was on: a new app, with a client and a store of its own,
   *  which takes up the writes the tab kept, as the app does once signed in. */
  const reload = () => {
    view.unmount();
    client.clear();
    client = signedIn();
    view = draw(client);
    act(() => writesOf(client).resume("alice"));
    return client;
  };
  return { client, editors, listing, patch, priorities, reload, view, writtenElsewhere };
}

test("the header carries the order, the title in place, the owner and the three counts", async () => {
  page();
  const title = await screen.findByRole("textbox", { name: "Title" });
  expect((title as HTMLInputElement).value).toBe("Rocq and Lean speed work restarted");
  expect(screen.getByText("P2")).toBeDefined();
  // The owner is the picker the page is drawn as: it offers the owners the deployment already
  // uses and takes one nobody has written yet, so it is a combobox and not a plain field.
  expect(screen.getByRole("combobox", { name: "Owner" })).toBeDefined();
  expect(screen.getByText("Order P2, dragged on the priorities page")).toBeDefined();
  expect(screen.getByText(/In flight 3 · waiting 11 · in backlog or icebox 5/)).toBeDefined();
  expect(screen.getByRole("link", { name: "‹ Priorities" }).getAttribute("href")).toBe(
    "/priorities"
  );
});

test("the description is a disclosure that closes, and the work fills the page below it", async () => {
  const user = userEvent.setup();
  page("rocq-lean", [issue("CORE-1", ["rocq-lean"])]);
  const described = await screen.findByRole("textbox", { name: "Description of P2" });
  expect(await within(described).findByText("Speedruns run again.")).toBeDefined();
  await user.click(screen.getByRole("button", { name: "Description" }));
  await waitFor(() =>
    expect(screen.queryByRole("textbox", { name: "Description of P2" })).toBeNull()
  );
  expect(screen.getByRole("button", { name: "Description, closed" })).toBeDefined();
  // The work fills the page with the description closed.
  expect(
    await screen.findByRole("row", { name: "CORE-1 Work CORE-1" }, { timeout: 3000 })
  ).toBeDefined();
  await user.click(screen.getByRole("button", { name: "Description, closed" }));
  expect(await screen.findByRole("textbox", { name: "Description of P2" })).toBeDefined();
});

test("the description is the documents' editor, with no action on a selection, and its write is the priority's, as markdown", async () => {
  const user = userEvent.setup();
  const { editors, patch } = page();
  const { box, handle } = await editors.named("Description of P2");
  // The editor Dispatch's documents are written in, holding the markdown as it reads: the
  // heading is a heading, and no source is shown.
  expect(box.classList.contains("ProseMirror")).toBe(true);
  expect(box.getAttribute("contenteditable")).toBe("true");
  expect(
    within(box).getByRole("heading", { name: "Rocq and Lean speed work restarted" })
  ).toBeDefined();
  handle.view.focus();
  selectText(handle, "Speedruns");
  expect(document.querySelector(".dispatch-action-bar")).toBeNull();
  for (const name of ["Comment", "Suggest", "Ask"]) {
    expect(screen.queryByRole("button", { name })).toBeNull();
  }
  writeAtEnd(handle, " More.", "More");
  await waitFor(() => expect(patch).toHaveBeenCalledTimes(1), { timeout: 3000 });
  expect(patch.mock.calls[0]?.[0]).toBe("rocq-lean");
  expect(patch.mock.calls[0]?.[1]).toEqual({
    description: "# Rocq and Lean speed work restarted\n\nSpeedruns run again. **More**.\n",
    // Named with the description the edit started from, as the server stores it.
    expected: { description: "# Rocq and Lean speed work restarted\n\nSpeedruns run again." },
  });
  expect(screen.queryByRole("button", { name: /save/i })).toBeNull();
  // Leaving the editor writes nothing more: what it holds is written already.
  await user.tab();
  expect(document.activeElement).not.toBe(box);
  expect(patch).toHaveBeenCalledTimes(1);
});

test("the reader's description kept beside the editor outlives the disclosure closing, and is written when they use it", async () => {
  const user = userEvent.setup();
  const { editors, patch, writtenElsewhere } = page();
  const { handle } = await editors.named("Description of P2");
  handle.view.focus();
  writeAtEnd(handle, " Mine.");
  // Another person's description reaches the page before the typing rests.
  writtenElsewhere({ description: "Written elsewhere." });
  const notice = () =>
    screen.queryByRole("alert", { name: "Description of P2, changed by another person" });
  expect(notice()).not.toBeNull();
  // Closed while it keeps the reader's text, the description stays drawn with its notice: the
  // reader's text is on the page until they choose.
  await user.click(screen.getByRole("button", { name: "Description" }));
  expect(screen.getByRole("button", { name: "Description, closed" })).toBeDefined();
  await editors.named("Description of P2");
  const kept = notice();
  if (kept === null) throw new Error("no notice beside the closed description");
  expect(within(kept).getByText(/Speedruns run again\. Mine\./)).toBeDefined();
  expect(patch).not.toHaveBeenCalled();
  await user.click(within(kept).getByRole("button", { name: "Use my text" }));
  await waitFor(() => expect(patch).toHaveBeenCalledTimes(1));
  expect(patch.mock.calls[0]?.[1]).toEqual({
    description: "# Rocq and Lean speed work restarted\n\nSpeedruns run again. Mine.\n",
    expected: { description: "Written elsewhere." },
  });
  // With nothing kept, the description is closed, as the reader asked.
  await waitFor(() =>
    expect(screen.queryByRole("textbox", { name: "Description of P2" })).toBeNull()
  );
});

test("a title the reader empties writes nothing, and left, shows the title again", async () => {
  const user = userEvent.setup();
  const { patch } = page();
  const title = (await screen.findByRole("textbox", { name: "Title" })) as HTMLInputElement;
  await user.clear(title);
  await user.tab();
  expect(title.value).toBe("Rocq and Lean speed work restarted");
  expect(patch).not.toHaveBeenCalled();
});

// Dispatch refuses a priority title longer than an issue title may be, so the field holds no
// more than that: a longer text pasted in is cut at the bound, and no longer title is sent.
test("the title holds no more than an issue title may, so no longer title is sent", async () => {
  const user = userEvent.setup();
  const { patch } = page();
  const title = (await screen.findByRole("textbox", { name: "Title" })) as HTMLInputElement;
  expect(title.maxLength).toBe(ISSUE_TITLE_MAX);
  await user.clear(title);
  await user.paste("t".repeat(ISSUE_TITLE_MAX + 1));
  await user.tab();
  await waitFor(() => expect(patch).toHaveBeenCalledTimes(1));
  expect(patch.mock.calls[0]?.[1]).toEqual({
    expected: { title: "Rocq and Lean speed work restarted" },
    title: "t".repeat(ISSUE_TITLE_MAX),
  });
});

test("the title's notice stands outside the heading, which is named by the title alone, and the field names the notice", async () => {
  // The clock is held, so the typing has not rested when the other person's title arrives,
  // however long the typing takes here.
  const user = userEvent.setup({ delay: null });
  const { patch, writtenElsewhere } = page();
  const title = (await screen.findByRole("textbox", { name: "Title" })) as HTMLInputElement;
  await onFakeClock(async () => {
    await user.type(title, ", mine");
    writtenElsewhere({ title: "Written elsewhere" });
  });
  const notice = screen.getByRole("alert", { name: "Title, changed by another person" });
  expect(within(notice).getByText("Rocq and Lean speed work restarted, mine")).toBeDefined();
  expect(title.value).toBe("Written elsewhere");
  expect(patch).not.toHaveBeenCalled();
  // The heading holds the field, and its name is the title the field shows: nothing of the
  // notice, which is drawn outside it.
  const heading = screen.getByRole("heading", { level: 1, name: "Written elsewhere" });
  expect(heading.contains(title)).toBe(true);
  // The field and its notice stay tied all the same.
  expect(notice.id).not.toBe("");
  expect(title.getAttribute("aria-describedby")).toBe(notice.id);
});

test("the keyboard reaches the description's editor from the disclosure above it", async () => {
  const user = userEvent.setup();
  const { editors } = page();
  const { box } = await editors.named("Description of P2");
  screen.getByRole("button", { name: "Description" }).focus();
  await user.tab();
  expect(document.activeElement).toBe(box);
});

test("a reader who may not write reads the description in the same editor, read-only", async () => {
  const { editors, patch } = page("rocq-lean", [], false);
  const { box } = await editors.named("Description of P2");
  expect(within(box).getByText("Speedruns run again.")).toBeDefined();
  expect(box.getAttribute("contenteditable")).toBe("false");
  expect(box.getAttribute("aria-readonly")).toBe("true");
  expect(patch).not.toHaveBeenCalled();
});

test("an empty description says none is written to a reader who may not write it", async () => {
  page("legion-first", [], false);
  expect(await screen.findByText("No description is written.")).toBeDefined();
  expect(screen.queryByRole("textbox", { name: "Description of P1" })).toBeNull();
});

test("an empty description is, for a writer, the prompt above the empty editor", async () => {
  const { editors } = page("legion-first");
  const { box } = await editors.named("Description of P1");
  expect(screen.getByText("No description is written. Write one below.")).toBeDefined();
  expect(box.textContent).toBe("");
  expect(box.getAttribute("contenteditable")).toBe("true");
});

test("the owner is a control beside the title and writes the owner", async () => {
  const user = userEvent.setup();
  const { patch } = page();
  const owner = await screen.findByRole("combobox", { name: "Owner" });
  await user.clear(owner);
  await user.type(owner, "LINUX controller");
  await user.tab();
  await waitFor(() => expect(patch).toHaveBeenCalledTimes(1));
  expect(patch.mock.calls[0]?.[1]).toEqual({ owner: "LINUX controller" });
});

test("the page carries no tab strip, no version control and no delete", async () => {
  page();
  await screen.findByRole("textbox", { name: "Title" });
  expect(screen.queryByRole("tablist")).toBeNull();
  expect(screen.queryAllByRole("tab")).toEqual([]);
  expect(screen.queryByRole("button", { name: /delete|retire|remove/i })).toBeNull();
  expect(screen.queryByRole("button", { name: /version|history of this|restore/i })).toBeNull();
  const text = document.body.textContent ?? "";
  expect(/\bv\d+\b|Versions|Comments on this description/.test(text)).toBe(false);
});

test("one link out leaves the scope, naming the issues and why", async () => {
  page("rocq-lean", [issue("CORE-1", ["rocq-lean"]), issue("CORE-4", ["rocq-lean"])]);
  const out = await screen.findByRole("link", { name: "CORE’s issues table" });
  expect(out.getAttribute("href")).toBe(
    "/projects/CORE/issues?only=CORE-1%2CCORE-4&onlyLabel=Rocq+and+Lean+speed+work+restarted&nofilter=1"
  );
});

test("the work under the priority is the issues table, scoped and under its own prefix", async () => {
  const { listing } = page("rocq-lean", [issue("CORE-1", ["rocq-lean"]), issue("OPS-2")]);
  const work = await screen.findByRole("region", { name: "Work advancing this priority" });
  expect(
    await within(work).findByRole("row", { name: "CORE-1 Work CORE-1" }, { timeout: 3000 })
  ).toBeDefined();
  expect(within(work).queryByRole("row", { name: "OPS-2 Work OPS-2" })).toBeNull();
  expect(listing).toHaveBeenCalledWith({ advances: ["rocq-lean"], open: true });
});

test("the margin stands beside the page, with no tabs and no control of its own", async () => {
  page();
  const margin = await screen.findByRole("complementary", { name: "Margin" });
  expect(within(margin).queryAllByRole("combobox")).toEqual([]);
  expect(within(margin).queryAllByRole("tab")).toEqual([]);
});

test("an id no priority carries says so and gives the way back", async () => {
  page("not-written");
  expect(await screen.findByText("No priority of that id is written.")).toBeDefined();
  expect(screen.getByRole("link", { name: "‹ Priorities" })).toBeDefined();
});

// A write a reload took up is the page's to settle with the reader as much as one pressed on it:
// refused because another person wrote the description since the reader's edit started, it
// stands on no line with a Retry that would send it again. The description shows it beside the
// other person's text, drawn though the reader had closed it, and nothing of it is sent again;
// the store holds it for the page until the reader settles it.
test("a description a reload took up and another person's change refused is kept beside the description, closed or not", async () => {
  const user = userEvent.setup();
  const { client, editors, patch } = page();
  await editors.named("Description of P2");
  await user.click(screen.getByRole("button", { name: "Description" }));
  await waitFor(() =>
    expect(screen.queryByRole("textbox", { name: "Description of P2" })).toBeNull()
  );
  const mine = "Speedruns run again, every night.\n\nMine.";
  const body = { description: mine, expected: { description: "Speedruns run." } };
  const request = priorityPatchRequest("rocq-lean", body);
  const key = "resumed-description";
  sessionStorage.setItem(
    `dispatch.write:${key}`,
    JSON.stringify({
      carried: "PATCH",
      detail: { attribute: "description", expected: "Speedruns run.", id: "rocq-lean", text: mine },
      key,
      kind: "priority-text",
      label: "Description of the priority “Rocq and Lean speed work restarted”",
      login: "alice",
      presentation: "indicator",
      pressedAt: Date.now(),
      request,
      sent: true,
      state: { status: "sending" },
      target: "priority:rocq-lean",
      version: 1,
    })
  );
  patch.mockRejectedValueOnce(
    new ApiError(409, { code: "PRIORITY_CHANGED", error: "the description was written meanwhile" })
  );
  act(() => writesOf(client).resume("alice"));
  const notice = await screen.findByRole("alert", {
    name: "Description of P2, changed by another person",
  });
  expect(within(notice).getByText(/Speedruns run again, every night\./)).toBeDefined();
  expect(within(notice).getByRole("button", { name: "Use my text" })).toBeDefined();
  expect(writesOf(client).rows()).toMatchObject([{ claimed: true, status: "refused" }]);
  expect(screen.queryByTestId("pending-writes")).toBeNull();
  expect(patch).toHaveBeenCalledTimes(1);
  expect(patch.mock.calls[0]?.[1]).toEqual(body);
});

/** Dispatch's refusal of a title another person wrote since the reader's edit started. */
const TITLE_CHANGED = new ApiError(409, {
  code: "PRIORITY_CHANGED",
  error: "the title was written meanwhile",
});

/** The priority's read once another person wrote its title. */
const RETITLED: Priorities = {
  ...BODY,
  priorities: ROWS.map((row) => (row.id === "rocq-lean" ? { ...row, title: "Theirs" } : row)),
};

/** The names of the writes this tab keeps across a reload. */
function keptWrites(): string[] {
  return Array.from({ length: window.sessionStorage.length }, (_, index) =>
    window.sessionStorage.key(index)
  ).filter((name): name is string => name?.startsWith("dispatch.write:") === true);
}

/** The reader writes `, mine` at the end of the title, and leaves the field: Dispatch refuses it
 *  (`patch`), another person having written `Theirs` since, which the page reads (`priorities`)
 *  once it takes the refusal. Answers the notice the page then draws, holding the reader's
 *  text. */
async function refusedTitle(
  priorities: { mockResolvedValue: (body: Priorities) => unknown },
  patch: { mockRejectedValueOnce: (refusal: Error) => unknown }
) {
  const user = userEvent.setup();
  const title = await screen.findByRole("textbox", { name: "Title" });
  priorities.mockResolvedValue(RETITLED);
  patch.mockRejectedValueOnce(TITLE_CHANGED);
  await user.type(title, ", mine");
  await user.tab();
  const notice = await screen.findByRole("alert", { name: "Title, changed by another person" });
  expect(within(notice).getByText(`${ROWS[1]?.title}, mine`)).toBeDefined();
  expect(title).toHaveValue("Theirs");
  return { notice, user };
}

// The reader's text a refusal brought back is the store's, not the page's: leaving the page
// hands the write back to the shell's line, which offers the text to copy with no Retry, and
// the page drawn again takes it from there and offers it beside the title once more. Using it
// then writes it over the other person's title, as a press of its own, and the refused write
// goes, from the store and from what the tab keeps.
test("a title another person's change refused outlives leaving the page, and is offered again on the way back", async () => {
  const { client, patch, priorities } = page();
  const { user } = await refusedTitle(priorities, patch);
  expect(screen.queryByTestId("pending-writes")).toBeNull();

  await user.click(screen.getByRole("link", { name: "‹ Priorities" }));
  const line = await screen.findByTestId("pending-writes");
  expect(
    within(line).getByText(
      "Not saved because another person changed it: Rename the priority “Rocq and Lean speed work restarted” to “Rocq and Lean speed work restarted, mine”"
    )
  ).toBeDefined();
  expect(within(line).getByRole("note", { name: "Your text:" }).textContent).toBe(
    `${ROWS[1]?.title}, mine`
  );
  expect(within(line).queryByRole("button", { name: "Retry" })).toBeNull();

  await user.click(screen.getByRole("link", { name: "Back to the priority" }));
  const notice = await screen.findByRole("alert", { name: "Title, changed by another person" });
  expect(within(notice).getByText(`${ROWS[1]?.title}, mine`)).toBeDefined();
  expect(screen.queryByTestId("pending-writes")).toBeNull();
  expect(patch).toHaveBeenCalledTimes(1);

  await user.click(within(notice).getByRole("button", { name: "Use my text" }));
  await waitFor(() => expect(patch).toHaveBeenCalledTimes(2));
  expect(patch.mock.calls[1]?.slice(0, 2)).toEqual([
    "rocq-lean",
    { expected: { title: "Theirs" }, title: `${ROWS[1]?.title}, mine` },
  ]);
  await waitFor(() => expect(writesOf(client).rows()).toEqual([]));
  expect(keptWrites()).toEqual([]);
});

// A reload takes the refused write up again from what the tab kept, and the page drawn on it
// offers the reader's text as the page before did. Keeping the other person's title lets the
// write go, and nothing of it is kept or sent.
test("a title another person's change refused outlives a reload, and keeping theirs lets it go", async () => {
  const { patch, priorities, reload } = page();
  await refusedTitle(priorities, patch);
  expect(keptWrites()).toHaveLength(1);
  const client = reload();
  const notice = await screen.findByRole("alert", { name: "Title, changed by another person" });
  expect(within(notice).getByText(`${ROWS[1]?.title}, mine`)).toBeDefined();
  expect(writesOf(client).rows()).toMatchObject([{ claimed: true, status: "refused" }]);
  expect(screen.queryByTestId("pending-writes")).toBeNull();

  await userEvent.setup().click(within(notice).getByRole("button", { name: "Keep their text" }));
  expect(screen.queryByRole("alert", { name: /changed by another person/ })).toBeNull();
  expect(writesOf(client).rows()).toEqual([]);
  expect(keptWrites()).toEqual([]);
  expect(screen.getByRole("textbox", { name: "Title" })).toHaveValue("Theirs");
  expect(patch).toHaveBeenCalledTimes(1);
});
