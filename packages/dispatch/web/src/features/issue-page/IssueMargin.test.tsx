import { expect, spyOn, test } from "bun:test";
import { MAX_ISSUE_BLOCKERS } from "@legion/contracts";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ReactNode, useState } from "react";
import { MemoryRouter } from "react-router-dom";

import { keyedAttempt } from "../../__tests__/keyed-attempt";
import { answerIssuePatches } from "../../__tests__/send-answers";
import { ApiError, api } from "../../api/client";
import { primarySpec } from "../../api/issue-cache";
import { issueQuery, userStateQuery } from "../../api/queries";
import type { Issue, IssueDetails, UserState } from "../../api/types";
import { stateForIssue } from "../issue/pins";
import { type DocumentBridge, MarginProvider, useMargin } from "../margin/margin-context";
import { blockedByControlName } from "./BlockedByControl";
import { DetailsFocusedNode, DetailsHistory, DetailsPeople } from "./details-more";
import { DETAILS_ROW_ATTRIBUTE } from "./details-rows";
import type { IssueField } from "./IssueFieldForms";
import { IssueMarginPanel } from "./IssueMargin";
import { FilledMarginSlot, MarginSlotProvider, useMarginSlotFill } from "./margin-slot";
import { NoTooltips } from "./NoTooltips";
import { PROPERTY_LABELS } from "./page-structure";
import { BLOCKED_BY_REFUSALS, CONTROL_LABELS, issueStatusWord } from "./page-words";
import { DetailsSlot } from "./slots/details";
import { blockedByRefusal } from "./useIssueBlockedBy";
import { type IssueHeaderWrites, useIssueHeaderWrites } from "./useIssueHeaderWrites";

const issue: IssueDetails = {
  route_status: null,
  route_holder: null,
  route_status_reason: null,
  holder_state: null,
  last_seen: null,
  takeable_at: null,
  advances: [],
  advances_revision: 0,
  artifacts: [
    {
      created_at: "2026-09-09T00:00:00Z",
      created_by: { id: "alice", kind: "user" },
      id: "artifact-1",
      issue_key: "CORE-1",
      project: "CORE",
      kind: "doc",
      name: "spec.md",
      primary: true,
      ref_key: "CORE-1/spec",
      session_id: null,
      slug: "spec",
      versions: [],
    },
  ],
  children: [],
  closed_at: null,
  created_at: "2026-09-09T00:00:00Z",
  created_by: { id: "alice", kind: "user" },
  design_gates: [],
  external_links: [],
  key: "CORE-1",
  labels: [],
  last_seq: 250,
  number: 1,
  open_asks: [],
  progress: { tasks: null, children: null },
  parent: null,
  blocked_by: [],
  assignee: null,
  claim: null,
  components: { mode: "inherit", ids: [], unknown: [], reason: null, inherited_from: null },
  plan_artifact_id: "",
  decisions_artifact_id: "",
  primary_artifact_id: "artifact-1",
  verification_artifact_id: "",
  project: "CORE",
  referenced_by_count: 0,
  route: null,
  repo: null,
  resolved_repo: null,
  status: "todo",
  priority: null,
  rank: "U",
  title: "Review the spec",
  updated_at: "2026-09-09T00:00:00Z",
};
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((completion) => {
    resolve = completion;
  });
  return { promise, resolve };
}

/** Details as the details slot draws it (`slots/details.tsx`), alone and inside `NoTooltips`:
 *  the panel over its own set of the issue's writes, on the cached issue, so a write's landing
 *  is what the panel draws next. `beside` is drawn above it, over writes of its own. */
function renderDetails(beside?: (issue: IssueDetails) => ReactNode) {
  const queryClient = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false, staleTime: Infinity } },
  });
  const view = render(
    <MemoryRouter initialEntries={["/issues/CORE-1"]}>
      <QueryClientProvider client={queryClient}>
        <MarginProvider>
          <NoTooltips>
            <CachedIssue>
              {(details, state) => (
                <>
                  {beside?.(details)}
                  <Details issue={details} state={state} />
                </>
              )}
            </CachedIssue>
          </NoTooltips>
        </MarginProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  return { queryClient, unmount: view.unmount };
}

function CachedIssue({
  children,
}: {
  children: (issue: IssueDetails, state: UserState | undefined) => ReactNode;
}): ReactNode {
  const details = useQuery(issueQuery("CORE-1")).data;
  const state = useQuery(userStateQuery()).data;
  return details === undefined ? null : children(details, state);
}

function Details({ issue, state }: { issue: IssueDetails; state: UserState | undefined }) {
  const writes = useIssueHeaderWrites(issue, stateForIssue(state, issue.key));
  const [editingField, setEditingField] = useState<IssueField | null>(null);
  return (
    <IssueMarginPanel
      documentArtifact={primarySpec(issue)}
      editingField={editingField}
      isClosed={issue.closed_at !== null}
      issue={issue}
      onEditingField={setEditingField}
      state={state}
      writes={writes}
    />
  );
}

function narrowIssue(details: IssueDetails): Issue {
  const { artifacts: _artifacts, children: _children, open_asks: _openAsks, ...rest } = details;
  return rest;
}

/** The reads Details makes of a fixture's issue, each answering the API's empty shape, and the
 *  issue's PATCH answered as the server does, with the narrow `Issue`. A read that reached the
 *  network would fail the file (`__tests__/network-guard.ts`). */
function stubIssueApi() {
  const getIssue = spyOn(api, "getIssue").mockResolvedValue(issue);
  const getMyState = spyOn(api, "getMyState").mockResolvedValue({
    "CORE-1": { dismissed: [], last_read_seq: 0, pinned: false, seq: 0 },
  });
  const reads = [
    spyOn(api, "whoAmI").mockResolvedValue({ login: "alice" } as Awaited<
      ReturnType<typeof api.whoAmI>
    >),
    spyOn(api, "listProjects").mockResolvedValue([]),
    spyOn(api, "listAgents").mockResolvedValue([]),
    // The repository's control reads the project's repository mappings at once for an issue
    // that resolves to none, as this one does; a project that maps none shows no repository.
    spyOn(api, "listRepoProjects").mockResolvedValue([]),
    spyOn(api, "listProjectArchitectureSources").mockResolvedValue([]),
    spyOn(api, "getReferenceTargets").mockResolvedValue({ targets: [] }),
  ];
  const pinReads = {
    asks: spyOn(api, "listIssueAsks").mockResolvedValue([]),
    comments: spyOn(api, "listComments").mockResolvedValue([]),
    messages: spyOn(api, "listIssueMessages").mockResolvedValue([]),
  };
  // A null `status` or `title` in the body means "leave it as it is", which is what the server
  // does with it; every other field the body names is written through.
  const patchIssue = answerIssuePatches().mockImplementation(
    async (
      _key,
      {
        rank: _rank,
        components: _components,
        advances: _advances,
        advances_revision: _advancesRevision,
        status,
        title,
        ...update
      }
    ) =>
      narrowIssue({
        ...issue,
        ...update,
        status: status ?? issue.status,
        title: title ?? issue.title,
      })
  );

  return {
    getIssue,
    patchIssue,
    pinReads,
    restore: () => {
      getIssue.mockRestore();
      getMyState.mockRestore();
      for (const spy of reads) spy.mockRestore();
      for (const spy of Object.values(pinReads)) spy.mockRestore();
      patchIssue.mockRestore();
    },
  };
}

/** One row of Details, by the name in its label column (`IssueMargin.tsx`'s `Row`). */
function detailsRow(name: string): HTMLElement {
  const row = screen
    .getByText(name, { exact: true, selector: "[data-page-chrome]" })
    .closest(".grid");
  if (!(row instanceof HTMLElement)) throw new Error(`Details draws no ${name} row`);
  return row;
}

test("the blocked-by editor stays open on a refusal and says what Dispatch refused", async () => {
  const user = userEvent.setup();
  const { patchIssue, restore } = stubIssueApi();
  patchIssue.mockImplementationOnce(() =>
    Promise.reject(
      new ApiError(400, { code: "BLOCKED_BY_INPUT", error: "blocked_by issue CORE-9 not found" })
    )
  );
  const { unmount } = renderDetails();

  try {
    await user.click(await screen.findByRole("button", { name: "Add issues CORE-1 waits on" }));
    const keys = (await screen.findByLabelText("Issues CORE-1 waits on")) as HTMLInputElement;
    await user.click(keys);
    await user.paste("CORE-9");
    await user.click(screen.getByRole("button", { name: "Save blocked by" }));

    await waitFor(() =>
      expect(patchIssue).toHaveBeenLastCalledWith(
        "CORE-1",
        { blocked_by: ["CORE-9"] },
        keyedAttempt
      )
    );
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("There is no issue CORE-9.");
    expect(alert).not.toHaveTextContent("blocked_by");
    // The key is the reader's own text inside the page's sentence (c-plain-words).
    expect(within(alert).getByText("CORE-9")).toHaveAttribute("data-author-text");
    expect(
      (screen.getByLabelText("Issues CORE-1 waits on") as HTMLInputElement).value,
      "the refused list is no longer in the box for the reader to mend"
    ).toBe("CORE-9");
    expect(screen.queryByText("Could not update this issue.")).not.toBeInTheDocument();
  } finally {
    unmount();
    restore();
  }
});

test("a blocked-by that is set can be changed and cleared from the panel", async () => {
  const user = userEvent.setup();
  const { getIssue, patchIssue, restore } = stubIssueApi();
  getIssue.mockResolvedValue({ ...issue, blocked_by: ["CORE-2"] });
  const { unmount } = renderDetails();

  try {
    expect(await screen.findByRole("link", { name: "CORE-2" })).toBeInTheDocument();
    await user.click(await screen.findByRole("button", { name: "Edit issues CORE-1 waits on" }));
    const keys = (await screen.findByLabelText("Issues CORE-1 waits on")) as HTMLInputElement;
    expect(keys.value).toBe("CORE-2");

    await user.clear(keys);
    await user.paste("core-3");
    await user.click(screen.getByRole("button", { name: "Save blocked by" }));
    await waitFor(() =>
      expect(patchIssue).toHaveBeenLastCalledWith(
        "CORE-1",
        { blocked_by: ["CORE-3"] },
        keyedAttempt
      )
    );
    // The editor closes on a landing, and the written list is what the panel draws.
    await waitFor(() =>
      expect(screen.queryByLabelText("Issues CORE-1 waits on")).not.toBeInTheDocument()
    );
    expect(await screen.findByRole("link", { name: "CORE-3" })).toBeInTheDocument();

    await user.click(await screen.findByRole("button", { name: "Edit issues CORE-1 waits on" }));
    await user.clear(await screen.findByLabelText("Issues CORE-1 waits on"));
    await user.click(screen.getByRole("button", { name: "Save blocked by" }));
    await waitFor(() =>
      expect(patchIssue).toHaveBeenLastCalledWith("CORE-1", { blocked_by: [] }, keyedAttempt)
    );
  } finally {
    unmount();
    restore();
  }
});

// The refusal is drawn in the editor and nowhere else, so a reader who closed the editor while
// the save was out was shown no refusal at all. Cancel and Escape wait for the answer, and
// Cancel says so: it is drawn and announced as held for as long as the save is out, rather than
// as a control whose press does nothing.
test("the blocked-by editor refuses Cancel and Escape while the save is out", async () => {
  const user = userEvent.setup();
  const { patchIssue, restore } = stubIssueApi();
  const saving = deferred<Issue>();
  patchIssue.mockImplementationOnce(() => saving.promise);
  const { unmount } = renderDetails();

  try {
    await user.click(
      await screen.findByRole("button", { name: blockedByControlName("CORE-1", 0) })
    );
    const keys = (await screen.findByLabelText("Issues CORE-1 waits on")) as HTMLInputElement;
    await user.click(keys);
    await user.paste("CORE-9");
    await user.click(screen.getByRole("button", { name: CONTROL_LABELS.save_blocked_by }));
    await waitFor(() => expect(patchIssue).toHaveBeenCalledTimes(1));

    const cancel = screen.getByRole("button", { name: CONTROL_LABELS.cancel });
    expect(
      cancel,
      "Cancel is drawn as a control while the save it would leave is out"
    ).toBeDisabled();
    await user.click(cancel);
    expect(
      screen.getByLabelText("Issues CORE-1 waits on"),
      "Cancel closed the editor over a save whose refusal it would have drawn"
    ).toBeInTheDocument();
    await user.click(keys);
    await user.keyboard("{Escape}");
    expect(
      screen.getByLabelText("Issues CORE-1 waits on"),
      "Escape closed the editor over a save whose refusal it would have drawn"
    ).toBeInTheDocument();

    // The answer closes it, which is the one thing that may.
    saving.resolve(narrowIssue({ ...issue, blocked_by: ["CORE-9"] }));
    await waitFor(() =>
      expect(screen.queryByLabelText("Issues CORE-1 waits on")).not.toBeInTheDocument()
    );
  } finally {
    unmount();
    restore();
  }
});

// A refusal belongs to the attempt that earned it. The editor held the last one until the next
// save started, so opening it again drew a refusal of a list the reader had already dropped.
test("the blocked-by editor opened again draws no refusal from the attempt before", async () => {
  const user = userEvent.setup();
  const { patchIssue, restore } = stubIssueApi();
  patchIssue.mockImplementationOnce(() =>
    Promise.reject(
      new ApiError(400, { code: "BLOCKED_BY_INPUT", error: "blocked_by issue CORE-9 not found" })
    )
  );
  const { unmount } = renderDetails();

  try {
    await user.click(
      await screen.findByRole("button", { name: blockedByControlName("CORE-1", 0) })
    );
    await user.click(await screen.findByLabelText("Issues CORE-1 waits on"));
    await user.paste("CORE-9");
    await user.click(screen.getByRole("button", { name: CONTROL_LABELS.save_blocked_by }));
    expect(await screen.findByRole("alert")).toHaveTextContent("There is no issue CORE-9.");
    // The answer gives Cancel back: the editor is the reader's to leave again.
    expect(screen.getByRole("button", { name: CONTROL_LABELS.cancel })).toBeEnabled();

    await user.click(screen.getByRole("button", { name: CONTROL_LABELS.cancel }));
    await waitFor(() =>
      expect(screen.queryByLabelText("Issues CORE-1 waits on")).not.toBeInTheDocument()
    );
    await user.click(screen.getByRole("button", { name: blockedByControlName("CORE-1", 0) }));
    expect(await screen.findByLabelText("Issues CORE-1 waits on")).toBeInTheDocument();
    expect(
      screen.queryByRole("alert"),
      "the editor draws the refusal of a save this reader never made"
    ).not.toBeInTheDocument();
  } finally {
    unmount();
    restore();
  }
});

// R21: each refusal of a blocked-by list is a sentence in the page's words, with the key the
// refusal is about inside it as the reader's own text: a key Dispatch knows no issue for, the
// issue itself, an issue of another project, and a list longer than an issue waits on.
test("each refusal of a blocked-by list is the page's sentence about the key it names", () => {
  const refused = (code: string, error: string) => new ApiError(400, { code, error });
  expect(
    blockedByRefusal(refused("BLOCKED_BY_INPUT", "blocked_by issue CORE-9 not found"), "CORE-1", [
      "CORE-2",
      "CORE-9",
    ])
  ).toEqual({ key: "CORE-9", words: "not_found" });
  expect(
    blockedByRefusal(refused("BLOCKED_BY_INPUT", "CORE-1 cannot be blocked by itself"), "CORE-1", [
      "CORE-1",
    ])
  ).toEqual({ key: "CORE-1", words: "itself" });
  expect(
    blockedByRefusal(
      refused("BLOCKED_BY_OUTSIDE_PROJECT", "blocked_by issue OPS-3 must be in the same project"),
      "CORE-1",
      ["OPS-3"]
    )
  ).toEqual({ key: "OPS-3", words: "outside_project" });
  const many = Array.from({ length: MAX_ISSUE_BLOCKERS + 1 }, (_, index) => `CORE-${index + 2}`);
  expect(
    blockedByRefusal(
      refused("BLOCKED_BY_INPUT", "blocked_by is 1 over the 20-item limit (21/20)"),
      "CORE-1",
      many
    )
  ).toEqual({ words: "too_many" });
  // Any other answer is none of these refusals, and is said as it was before: the server's own
  // words, or the page's line for a save that never reached it.
  expect(
    blockedByRefusal(new ApiError(500, { code: "INTERNAL", error: "boom" }), "CORE-1", ["CORE-2"])
  ).toBeUndefined();
});

// R21: a list that would make the issue wait on itself through the issues it waits on, its
// parent or its children is refused as `DEPENDENCY_CYCLE`, whose message names the field by its
// wire token (`CORE-1 would wait on itself through blocked_by and parent links`). It is said in
// the page's words as the other refusals are, with the issue the server names as the reader's
// own text; a chain too long for the server to follow names none.
test("a blocked-by list that would make a loop is the page's sentence about the issue it names", () => {
  const refused = (error: string) => new ApiError(409, { code: "DEPENDENCY_CYCLE", error });
  expect(
    blockedByRefusal(
      refused("CORE-1 would wait on itself through blocked_by and parent links"),
      "CORE-1",
      ["CORE-7"]
    )
  ).toEqual({ key: "CORE-1", words: "cycle" });
  expect(
    blockedByRefusal(refused("the dependency graph is deeper than 64 links"), "CORE-1", ["CORE-7"])
  ).toEqual({ words: "too_deep" });

  // Each sentence is the page's own: no wire token, and the key in it only where one goes.
  const sentences = Object.values(BLOCKED_BY_REFUSALS).map(
    ({ after, before }) => `${before}KEY${after}`
  );
  expect(sentences).toEqual([
    "KEY would then wait on itself, through the issues it waits on, its parent or its children.",
    "KEY is this issue, and an issue cannot wait on itself.",
    "There is no issue KEY.",
    "KEY is in another project, and an issue can wait only on issues of its own project.",
    "These issues wait on one another through more links than Dispatch follows, so it cannot check them for a loop.KEY",
    `An issue can wait on at most ${MAX_ISSUE_BLOCKERS} issues.KEY`,
  ]);
  for (const sentence of sentences) {
    expect(sentence).not.toMatch(/blocked_by|_/u);
  }
});

// R11: the menu lists the properties with no value and presses the control that sets each one,
// rather than drawing a second copy of it. It finds that control by the name the control
// exports (`blockedByControlName`), so the two sides cannot spell it apart: while each typed
// its own, a word changed in one made the entry disappear with no error anywhere.
test("the add menu opens the control the panel draws, under the name that control exports", async () => {
  const user = userEvent.setup();
  const { restore } = stubIssueApi();
  const { unmount } = renderDetails();

  try {
    expect(
      await screen.findByRole("button", { name: blockedByControlName("CORE-1", 0) }),
      "the panel draws no blocked-by control under the name it exports"
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: CONTROL_LABELS.add }));
    await user.click(await screen.findByRole("menuitem", { name: PROPERTY_LABELS.blocked_by }));
    expect(
      await screen.findByLabelText("Issues CORE-1 waits on"),
      "the menu entry reached no control, so picking it opened nothing"
    ).toBeInTheDocument();
  } finally {
    unmount();
    restore();
  }
});

// R21: the indicator says what the page calls each field the write carries, and what it calls
// the status the write sets, never the schema's token for either. It read the first key of the
// write ("CORE-1: blocked_by saved"), so a write of two named one, and the status as the wire
// spells it ("CORE-1 set to in_progress").
//
// The shell's indicator draws this line, but the issue page's own write hook composes it, so its
// words are this page's (`useIssueHeaderWrites.ts`, `e2e/keyed-writes.e2e.ts`).

/** A second caller of the issue's writes on the page, as the header slot is: slots share no
 *  state, so each makes its own set (`slots/details.tsx`). */
function OtherWrites({ issue: details }: { issue: IssueDetails }): ReactNode {
  const writes: IssueHeaderWrites = useIssueHeaderWrites(
    details,
    stateForIssue(undefined, details.key)
  );
  return (
    <button onClick={() => writes.updateIssue.mutate({ status: "in_progress" })} type="button">
      Start it from the header
    </button>
  );
}

// R2: the page never says two statuses. The header and Details each call the writes, so Details
// reads every pending write of the issue's fields by their one mutation key, whichever caller
// made it (`useIssueHeaderWrites.ts`'s `pendingStatus`).
test("Details' Status reads the status another caller of the writes is saving", async () => {
  const user = userEvent.setup();
  const { patchIssue, restore } = stubIssueApi();
  const saving = deferred<Issue>();
  patchIssue.mockImplementationOnce(() => saving.promise);
  const { unmount } = renderDetails((details) => <OtherWrites issue={details} />);

  try {
    await screen.findByText(PROPERTY_LABELS.status, { selector: "[data-page-chrome]" });
    expect(detailsRow(PROPERTY_LABELS.status)).toHaveTextContent(issueStatusWord("todo"));
    await user.click(screen.getByRole("button", { name: "Start it from the header" }));
    await waitFor(() => expect(patchIssue).toHaveBeenCalledTimes(1));
    await waitFor(() =>
      expect(
        detailsRow(PROPERTY_LABELS.status),
        "Details' Status kept the saved status while the header's write of another was out"
      ).toHaveTextContent(issueStatusWord("in_progress"))
    );

    saving.resolve(narrowIssue({ ...issue, status: "in_progress" }));
    await waitFor(() =>
      expect(detailsRow(PROPERTY_LABELS.status)).toHaveTextContent(issueStatusWord("in_progress"))
    );
  } finally {
    unmount();
    restore();
  }
});

/** The margin's own content, drawn through the fill the details slot put in its slot, as the
 *  shell's Details drawer draws it (`MarginSheet.tsx`'s `SheetBody`). */
function MarginBody(): ReactNode {
  return (
    <FilledMarginSlot fill={useMarginSlotFill()}>
      <p>A thread anchored in the document</p>
    </FilledMarginSlot>
  );
}

/** What a document's editor does to the margin while it is mounted (`ProofDocument.tsx`). */
function DocumentEditor(): ReactNode {
  const { registerDocument } = useMargin();
  return (
    <>
      <button
        onClick={() => registerDocument({ artifactId: "artifact-1" } as unknown as DocumentBridge)}
        type="button"
      >
        Open the document
      </button>
      <button onClick={() => registerDocument(undefined)} type="button">
        Close the document
      </button>
    </>
  );
}

// R1: Details holds no card of the margin while no document's editor is on the page, and while
// one is, the margin's own cards stand above the sections, for a comment on a selection.
test("the margin's own cards are drawn only while a document's editor is on the page", async () => {
  const user = userEvent.setup();
  const { restore } = stubIssueApi();
  const queryClient = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false, staleTime: Infinity } },
  });
  const view = render(
    <MemoryRouter initialEntries={["/issues/CORE-1"]}>
      <QueryClientProvider client={queryClient}>
        <MarginProvider>
          <MarginSlotProvider drawsSlot>
            <DetailsSlot issue={issue} route={{ key: "CORE-1", kind: "issue" }} />
            <MarginBody />
            <DocumentEditor />
          </MarginSlotProvider>
        </MarginProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );

  try {
    const development = await screen.findByRole("button", { name: "Development" });
    expect(
      screen.queryByText("A thread anchored in the document"),
      "Details draws a card of the margin with no document's editor on the page"
    ).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "Open the document" }));
    const card = await screen.findByText("A thread anchored in the document");
    expect(
      card.compareDocumentPosition(development) & Node.DOCUMENT_POSITION_FOLLOWING,
      "the margin's own cards are not above Details' sections"
    ).toBeTruthy();

    await user.click(screen.getByRole("button", { name: "Close the document" }));
    await waitFor(() =>
      expect(screen.queryByText("A thread anchored in the document")).not.toBeInTheDocument()
    );
    expect(
      screen.getByRole("button", { name: "Development" }),
      "the panel was drawn again when the margin's cards went"
    ).toBe(development);
  } finally {
    view.unmount();
    restore();
  }
});

// R3: the header's Set parent issue presses the Parent row's first control (`useOpenDetailsAt`,
// which presses a row's first control), so that control is the one that opens the parent's
// editor, and it stands before the parent's link.
test("the Parent row's first control opens the parent's editor, before the parent's link", async () => {
  const user = userEvent.setup();
  const { getIssue, restore } = stubIssueApi();
  const firstControl = (row: Element | null) =>
    row?.querySelector<HTMLElement>("input, textarea, select, button:not(:disabled), a[href]");

  try {
    const empty = renderDetails();
    await screen.findByText(PROPERTY_LABELS.parent, { selector: "[data-page-chrome]" });
    const none = document.querySelector(`[${DETAILS_ROW_ATTRIBUTE}="parent"]`);
    expect(firstControl(none)).toHaveTextContent(CONTROL_LABELS.set_parent);
    expect(none).toHaveTextContent(CONTROL_LABELS.none);
    empty.unmount();

    getIssue.mockResolvedValue({ ...issue, parent: "CORE-2" });
    const { unmount } = renderDetails();
    const link = await screen.findByRole("link", { name: "CORE-2" });
    const row = link.closest(`[${DETAILS_ROW_ATTRIBUTE}="parent"]`);
    expect(row, "the parent's link is outside the Parent row").not.toBeNull();
    const control = firstControl(row);
    expect(control).toHaveTextContent(CONTROL_LABELS.edit_parent);
    expect(
      (control?.compareDocumentPosition(link) ?? 0) & Node.DOCUMENT_POSITION_FOLLOWING,
      "the parent's link stands before the control that edits it"
    ).toBeTruthy();

    await user.click(control as HTMLElement);
    const box = await screen.findByLabelText(PROPERTY_LABELS.parent);
    await waitFor(() => expect(box, "the parent's editor took no focus").toHaveFocus());
    unmount();
  } finally {
    restore();
  }
});

// R7: the pins are read for their words only where there is a pin, so an issue with none costs
// no request of the questions, comments and messages.
test("with no pin, Pinned reads None and sends no request", async () => {
  const { pinReads, restore } = stubIssueApi();
  const { unmount } = renderDetails();

  try {
    const pins = await screen.findByRole("region", { name: CONTROL_LABELS.pinned });
    expect(pins).toHaveTextContent(CONTROL_LABELS.none);
    expect(within(pins).queryAllByRole("link")).toHaveLength(0);
    // Every other read of the panel has answered by the time the repository's has.
    await waitFor(() => expect(api.listRepoProjects).toHaveBeenCalled());
    expect(pinReads.asks, "Pinned read the issue's questions with no pin").not.toHaveBeenCalled();
    expect(
      pinReads.comments,
      "Pinned read the issue's comments with no pin"
    ).not.toHaveBeenCalled();
    expect(
      pinReads.messages,
      "Pinned read the issue's messages with no pin"
    ).not.toHaveBeenCalled();
  } finally {
    unmount();
    restore();
  }
});

// R9: part M fills the three places Details keeps for it (`details-more.tsx`); until then each
// draws nothing.
test("the three places of part M draw nothing", () => {
  for (const Place of [DetailsPeople, DetailsFocusedNode, DetailsHistory]) {
    const view = render(<Place issue={issue} state={undefined} />);
    expect(view.container.innerHTML, `${Place.name} drew something`).toBe("");
    view.unmount();
  }
});
