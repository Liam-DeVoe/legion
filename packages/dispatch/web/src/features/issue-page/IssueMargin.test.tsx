import { expect, spyOn, test } from "bun:test";
import { MAX_ISSUE_BLOCKERS } from "@legion/contracts";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { type ReactNode, useState } from "react";
import { MemoryRouter } from "react-router-dom";

import { commentDeliveryFields } from "../../__tests__/comment-fixture";
import { graphNode } from "../../__tests__/graph-fixture";
import { keyedAttempt } from "../../__tests__/keyed-attempt";
import { answerIssuePatches } from "../../__tests__/send-answers";
import { ApiError, api } from "../../api/client";
import { eventRefreshTargets } from "../../api/event-refresh";
import { primarySpec } from "../../api/issue-cache";
import { issueQuery, userStateQuery } from "../../api/queries";
import { refreshQueries } from "../../api/query-refresh";
import type {
  ArchitectureSource,
  CommentWithTrees,
  DispatchEvent,
  DisplayTree,
  GraphReferences,
  Issue,
  IssueDetails,
  RepoProject,
  UserState,
} from "../../api/types";
import { linkText, pageInkText } from "../../theme/classes";
import { componentsControlName } from "../issue/IssueComponentsLine";
import { labelsControlName } from "../issue/IssueLabels";
import { conversationItemId, pinnedItemMarker, stateForIssue } from "../issue/pins";
import { repoControlName } from "../issue/RepoControl";
import { type DocumentBridge, MarginProvider, useMargin } from "../margin/margin-context";
import { issue as marginIssue, specArtifact } from "../margin/margin-fixture";
import { addPriorityControlName } from "../priorities/IssuePriorityLine";
import { buildDispatchReference } from "../refs/routes";
import { KeymapProvider } from "../shell/KeymapProvider";
import { appKeymap, useKeymapScope } from "../shell/keymap";
import { userPreferenceStorageKey } from "../shell/userPreference";
import { blockedByControlName } from "./BlockedByControl";
import { DetailsFocusedNode, DetailsHistory, DetailsPeople } from "./details-more";
import { DETAILS_ROW_ATTRIBUTE, DETAILS_ROW_REQUEST_EVENT, type DetailsRow } from "./details-rows";
import type { IssueField } from "./IssueFieldForms";
import { IssueMarginPanel } from "./IssueMargin";
import {
  DETAILS_ATTRIBUTE,
  FilledMarginSlot,
  type MarginDrawer,
  MarginSlotProvider,
  useMarginSlotFill,
} from "./margin-slot";
import { NoTooltips } from "./NoTooltips";
import { MARGIN_SECTION_NAMES, PROPERTY_LABELS } from "./page-structure";
import {
  ANSWER_LINES,
  BLOCKED_BY_REFUSALS,
  CONTROL_LABELS,
  issueStatusWord,
  NODE_TYPE_NAMES,
} from "./page-words";
import { architecture } from "./shared-controls-fixture";
import { DetailsSlot } from "./slots/details";
import { blockedByRefusal } from "./useIssueBlockedBy";
import { type IssueHeaderWrites, useIssueHeaderWrites } from "./useIssueHeaderWrites";

const issue: IssueDetails = { ...marginIssue, last_seq: 250, status: "todo" };

/** The page's words the labels control and the components picker are named from inside
 *  `NoTooltips` (`labelsControlName`, `componentsControlName`). */
const CONTROL_NAME_WORDS = { ANSWER_LINES, CONTROL_LABELS };

/** Details as the details slot draws it (`slots/details.tsx`), alone and inside `NoTooltips`:
 *  the panel over its own set of the issue's writes, on the cached issue, so a write's landing
 *  is what the panel draws next. `beside` is drawn above it, over writes of its own. `drawer` is
 *  the Details drawer it is drawn in; with none, Details makes none of the reads it makes only
 *  while open. */
function renderDetails(beside?: (issue: IssueDetails) => ReactNode, drawer?: MarginDrawer) {
  const queryClient = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false, staleTime: Infinity } },
  });
  const view = render(
    <MemoryRouter initialEntries={["/issues/CORE-1"]}>
      <QueryClientProvider client={queryClient}>
        <MarginProvider>
          <MarginSlotProvider drawer={drawer}>
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
          </MarginSlotProvider>
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
    getMyState,
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

/** The row of Details a control elsewhere on the page opens it at (`details-rows.ts`). */
function rowAt(row: DetailsRow): HTMLElement {
  const found = document.querySelector(`[${DETAILS_ROW_ATTRIBUTE}="${row}"]`);
  if (!(found instanceof HTMLElement)) throw new Error(`Details draws no ${row} row`);
  return found;
}

/** Every Escape that reaches the window from now on, by whether a control marked it taken: the
 *  page's keys pass over a taken press (`shell/keymap.ts`), and the phone's Details sheet closes
 *  on an Escape no control took (`shell/useDialog.ts`). */
function watchEscapes(): { readonly stop: () => void; readonly taken: boolean[] } {
  const taken: boolean[] = [];
  const record = (event: KeyboardEvent) => {
    if (event.key === "Escape") taken.push(event.defaultPrevented);
  };
  window.addEventListener("keydown", record);
  return { stop: () => window.removeEventListener("keydown", record), taken };
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
    await user.click(screen.getByRole("button", { name: CONTROL_LABELS.save_blocked_by }));

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
    await user.click(screen.getByRole("button", { name: CONTROL_LABELS.save_blocked_by }));
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
    await user.click(screen.getByRole("button", { name: CONTROL_LABELS.save_blocked_by }));
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
  const saving = Promise.withResolvers<Issue>();
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

// A closed section unmounts its rows, so the blocked-by editor's state is the panel's and not
// its row's: a reader who closes Development while the save is out finds the editor, the list
// they typed and the refusal that landed meanwhile when they open it again.
test("closing Development keeps the blocked-by editor, its typed list and a refusal that lands after the close", async () => {
  const user = userEvent.setup();
  const { patchIssue, restore } = stubIssueApi();
  const saving = Promise.withResolvers<Issue>();
  patchIssue.mockImplementationOnce(() => saving.promise);
  const { queryClient, unmount } = renderDetails();

  try {
    await user.click(
      await screen.findByRole("button", { name: blockedByControlName("CORE-1", 0) })
    );
    await user.click(await screen.findByLabelText("Issues CORE-1 waits on"));
    await user.paste("CORE-2 CORE-9");
    await user.click(screen.getByRole("button", { name: CONTROL_LABELS.save_blocked_by }));
    await waitFor(() =>
      expect(patchIssue).toHaveBeenCalledWith(
        "CORE-1",
        { blocked_by: ["CORE-2", "CORE-9"] },
        keyedAttempt
      )
    );

    const development = screen.getByRole("button", { name: MARGIN_SECTION_NAMES.development });
    await user.click(development);
    expect(screen.queryByLabelText("Issues CORE-1 waits on")).not.toBeInTheDocument();
    saving.reject(
      new ApiError(400, { code: "BLOCKED_BY_INPUT", error: "blocked_by issue CORE-9 not found" })
    );
    await waitFor(() => expect(queryClient.isMutating()).toBe(0));

    await user.click(development);
    expect(
      screen.queryAllByLabelText("Issues CORE-1 waits on"),
      "closing Development threw away the open blocked-by editor"
    ).toHaveLength(1);
    expect(
      screen.getByLabelText<HTMLInputElement>("Issues CORE-1 waits on").value,
      "closing Development threw away the list typed into the editor"
    ).toBe("CORE-2 CORE-9");
    expect(
      screen.queryByRole("alert")?.textContent,
      "the refusal that landed while Development was closed is drawn nowhere"
    ).toBe("There is no issue CORE-9.");
  } finally {
    unmount();
    restore();
    window.localStorage.removeItem(userPreferenceStorageKey("alice", "issue.margin-sections"));
  }
});

// Escape in the blocked-by box is the editor's: it closes it and is taken there, so the phone's
// Details sheet, which closes on an Escape no control took (`useDialog.ts`), stays open.
test("Escape in the blocked-by box closes its editor and goes no further", async () => {
  const user = userEvent.setup();
  const { restore } = stubIssueApi();
  const escapes = watchEscapes();
  const { unmount } = renderDetails();

  try {
    await user.click(
      await screen.findByRole("button", { name: blockedByControlName("CORE-1", 0) })
    );
    const keys = await screen.findByLabelText("Issues CORE-1 waits on");
    await waitFor(() => expect(keys).toHaveFocus());
    await user.keyboard("{Escape}");
    expect(
      screen.queryByLabelText("Issues CORE-1 waits on"),
      "Escape left the blocked-by editor open"
    ).not.toBeInTheDocument();
    expect(
      escapes.taken,
      "Escape in the blocked-by box went on to the page's keys, which close Details on a phone"
    ).toEqual([true]);
  } finally {
    escapes.stop();
    unmount();
    restore();
  }
});

// The box goes with the editor, so leaving it - by Cancel or by a save that lands - gives the
// focus back to the control that opened it, not to the page.
test("leaving the blocked-by editor gives the focus back to its Add or Edit", async () => {
  const user = userEvent.setup();
  const { restore } = stubIssueApi();
  const { unmount } = renderDetails();

  try {
    await user.click(
      await screen.findByRole("button", { name: blockedByControlName("CORE-1", 0) })
    );
    await user.click(await screen.findByRole("button", { name: CONTROL_LABELS.cancel }));
    expect(
      document.activeElement,
      "Cancel in the blocked-by editor left the focus on the page"
    ).toBe(screen.getByRole("button", { name: blockedByControlName("CORE-1", 0) }));

    await user.click(screen.getByRole("button", { name: blockedByControlName("CORE-1", 0) }));
    await user.click(await screen.findByLabelText("Issues CORE-1 waits on"));
    await user.paste("CORE-2");
    await user.keyboard("{Enter}");
    await waitFor(() =>
      expect(
        document.activeElement,
        "a save of the blocked-by list that landed left the focus on the page"
      ).toBe(screen.queryByRole("button", { name: blockedByControlName("CORE-1", 1) }))
    );
  } finally {
    unmount();
    restore();
  }
});

// R21: each refusal of a blocked-by list is a sentence in the page's words, with the key the
// refusal is about inside it as the reader's own text: a key Dispatch knows no issue for, the
// issue itself, and a list longer than an issue waits on.
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
    "These issues wait on one another through more links than Dispatch follows, so it cannot check them for a loop.KEY",
    `An issue can wait on at most ${MAX_ISSUE_BLOCKERS} issues.KEY`,
  ]);
  for (const sentence of sentences) {
    expect(sentence).not.toMatch(/blocked_by|_/u);
  }
});

// R13: only a list of unbounded length folds after its first ten. An issue waits on at most
// `MAX_ISSUE_BLOCKERS` issues, so the Blocked by row draws every one of them and no Show all.
test("the blockers never fold", async () => {
  const { getIssue, restore } = stubIssueApi();
  const blockers = Array.from({ length: MAX_ISSUE_BLOCKERS }, (_, index) => `CORE-${index + 2}`);
  getIssue.mockResolvedValue({ ...issue, blocked_by: blockers });
  const { unmount } = renderDetails();

  try {
    await screen.findByRole("link", { name: "CORE-2" });
    const row = detailsRow(PROPERTY_LABELS.blocked_by);
    expect(
      within(row)
        .getAllByRole("link")
        .map((link) => link.textContent),
      "the Blocked by row folds the issues this one waits on"
    ).toEqual(blockers);
    expect(
      within(row).queryByRole("button", {
        name: (name) => name.startsWith(CONTROL_LABELS.show_all),
      }),
      "the Blocked by row offers to show a list it already draws whole"
    ).not.toBeInTheDocument();
  } finally {
    unmount();
    restore();
  }
});

// What waits on the issue is read from the references that point at it, which another issue's
// write moves: the Blocks row is read again on the event of an issue that starts waiting on this
// one, as the backlink panels are (`api/event-refresh.ts`'s rule for `blocked_by`).
test("Details' Blocks is read again when another issue starts waiting on this one", async () => {
  const { restore } = stubIssueApi();
  const reference = buildDispatchReference({ key: "CORE-1", kind: "issue" });
  const node = graphNode({ id: "CORE-1", issue_key: "CORE-1", kind: "issue", ref: reference });
  let edges: GraphReferences["edges"] = [];
  const getReferences = spyOn(api, "getReferences").mockImplementation(async () => ({
    edges,
    node,
  }));
  const blocksReads = () =>
    getReferences.mock.calls.filter(
      ([to, options]) => to === reference && options?.kind === "blocked_by"
    ).length;
  const drawer: MarginDrawer = { open: true, setOpen: () => undefined, setPage: () => undefined };
  const { queryClient, unmount } = renderDetails(undefined, drawer);

  try {
    await waitFor(() => expect(blocksReads()).toBe(1));
    await waitFor(() =>
      expect(detailsRow(PROPERTY_LABELS.blocks)).toHaveTextContent(CONTROL_LABELS.none)
    );

    // CORE-5 starts waiting on CORE-1, and its event is what the feed hands the refresh, as
    // `__tests__/event-feed.test.ts` drives a blocker change.
    edges = [
      {
        created_at: "2026-10-26T00:00:00Z",
        direction: "in",
        excerpt: { text: "Ship the release" },
        kind: "blocked_by",
        node: graphNode({
          id: "CORE-5",
          issue_key: "CORE-5",
          kind: "issue",
          ref: buildDispatchReference({ key: "CORE-5", kind: "issue" }),
        }),
        source_seq: 4,
      },
    ];
    // The payload as the server writes it - the issue after the write, flat, with what the write
    // moved - in the shape the feed's own test builds it (`event-feed.test.ts`'s `issue`).
    const payload: Record<string, unknown> = {
      ancestors: [],
      assignee: null,
      blocked_by: ["CORE-1"],
      changed: ["blocked_by"],
      closed_at: null,
      key: "CORE-5",
      parent: null,
      project: "CORE",
      references_changed: [{ id: "CORE-1", kind: "issue" }],
      status: "todo",
      title: "Ship the release",
    };
    const waits = {
      actor: { id: "planner", kind: "session" },
      created_at: "2026-10-26T00:00:00Z",
      id: 12,
      issue_key: "CORE-5",
      notify: true,
      payload,
      project: "CORE",
      seq: 4,
      type: "issue.updated",
    } as DispatchEvent;
    refreshQueries(queryClient, eventRefreshTargets(waits, "alice"));

    await waitFor(() =>
      expect(blocksReads(), "Blocks was not read again on CORE-5's event").toBe(2)
    );
    const row = detailsRow(PROPERTY_LABELS.blocks);
    expect(
      await within(row).findByRole("link", { name: "CORE-5" }),
      "Blocks does not draw the issue that started waiting on CORE-1"
    ).toHaveAttribute("href", "/issues/CORE-5");
    expect(row).toHaveTextContent("Ship the release");
  } finally {
    unmount();
    getReferences.mockRestore();
    restore();
  }
});

// Blocks says None only for an answer that holds nothing waiting on the issue: while that answer
// is still out the row says nothing, rather than read as an issue nothing waits on.
test("Details' Blocks says nothing while its read is out, and None once it answers with none", async () => {
  const { restore } = stubIssueApi();
  const reference = buildDispatchReference({ key: "CORE-1", kind: "issue" });
  const answer = Promise.withResolvers<GraphReferences>();
  const getReferences = spyOn(api, "getReferences").mockImplementation(() => answer.promise);
  const drawer: MarginDrawer = { open: true, setOpen: () => undefined, setPage: () => undefined };
  const { unmount } = renderDetails(undefined, drawer);

  try {
    await waitFor(() =>
      expect(getReferences).toHaveBeenCalledWith(reference, { kind: "blocked_by" })
    );
    expect(
      detailsRow(PROPERTY_LABELS.blocks),
      "Blocks reads None while its read is out"
    ).not.toHaveTextContent(CONTROL_LABELS.none);

    answer.resolve({
      edges: [],
      node: graphNode({ id: "CORE-1", issue_key: "CORE-1", kind: "issue", ref: reference }),
    });
    await waitFor(() =>
      expect(
        detailsRow(PROPERTY_LABELS.blocks),
        "Blocks does not read None for an answer that holds nothing waiting on the issue"
      ).toHaveTextContent(CONTROL_LABELS.none)
    );
  } finally {
    unmount();
    getReferences.mockRestore();
    restore();
  }
});

// A folded list's Show all goes with its press, so the focus goes on to the first entry it
// opened, the eleventh, rather than falling to the page: in Files and documents, and in Blocks.
test("Show all gives the focus to the first entry it opens", async () => {
  const user = userEvent.setup();
  const { getIssue, restore } = stubIssueApi();
  const notes = Array.from({ length: 12 }, (_, index) => {
    const slug = `notes-${String(index + 1).padStart(2, "0")}`;
    return {
      ...specArtifact,
      id: slug,
      name: `${slug}.md`,
      primary: false,
      ref_key: `CORE-1/${slug}`,
      slug,
    };
  });
  getIssue.mockResolvedValue({ ...issue, artifacts: [specArtifact, ...notes] });
  const reference = buildDispatchReference({ key: "CORE-1", kind: "issue" });
  const waiting = Array.from({ length: 12 }, (_, index) => `CORE-${index + 2}`);
  const getReferences = spyOn(api, "getReferences").mockResolvedValue({
    edges: waiting.map((key) => ({
      created_at: "2026-10-26T00:00:00Z",
      direction: "in",
      excerpt: { text: `Waits for ${key}` },
      kind: "blocked_by",
      node: graphNode({
        id: key,
        issue_key: key,
        kind: "issue",
        ref: buildDispatchReference({ key, kind: "issue" }),
      }),
      source_seq: 4,
    })),
    node: graphNode({ id: "CORE-1", issue_key: "CORE-1", kind: "issue", ref: reference }),
  });
  const drawer: MarginDrawer = { open: true, setOpen: () => undefined, setPage: () => undefined };
  const { unmount } = renderDetails(undefined, drawer);

  try {
    // Files and documents: the spec and twelve notes, so the eleventh entry is notes-10.md.
    const showFiles = await screen.findByRole("button", {
      name: `${CONTROL_LABELS.show_all} ${notes.length + 1}`,
    });
    act(() => showFiles.focus());
    await user.keyboard("{Enter}");
    expect(
      document.activeElement,
      "Show all of Files and documents left the focus off the first entry it opened"
    ).toBe(within(screen.getByTestId("issue-files")).getByRole("link", { name: /notes-10\.md/ }));

    const blocks = detailsRow(PROPERTY_LABELS.blocks);
    const showBlocks = await within(blocks).findByRole("button", {
      name: `${CONTROL_LABELS.show_all} ${waiting.length}`,
    });
    act(() => showBlocks.focus());
    await user.keyboard("{Enter}");
    expect(
      document.activeElement,
      "Show all of Blocks left the focus off the first entry it opened"
    ).toBe(within(blocks).getByRole("link", { name: "CORE-12" }));
  } finally {
    unmount();
    getReferences.mockRestore();
    restore();
  }
});

// Details draws its own control of Referenced by: the application's `ReferencedByToggle` is in
// its link blue, one sky-blue control among values in the page's ink.
test("Referenced by's control is in the page's ink, opens the list and reads the count", async () => {
  const user = userEvent.setup();
  const { getIssue, restore } = stubIssueApi();
  getIssue.mockResolvedValue({ ...issue, referenced_by_count: 3 });
  const reference = buildDispatchReference({ key: "CORE-1", kind: "issue" });
  const getReferences = spyOn(api, "getReferences").mockResolvedValue({
    edges: [],
    node: graphNode({ id: "CORE-1", issue_key: "CORE-1", kind: "issue", ref: reference }),
  });
  const { unmount } = renderDetails();

  try {
    const toggle = await screen.findByRole("button", { name: "Referenced by (3)" });
    expect(toggle, "Referenced by's control is not in the page's ink").toHaveClass(pageInkText);
    for (const token of linkText.split(" ")) {
      expect(toggle, "Referenced by's control is in the application's link blue").not.toHaveClass(
        token
      );
    }
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    await user.click(toggle);
    expect(toggle, "Referenced by's control does not say the list is open").toHaveAttribute(
      "aria-expanded",
      "true"
    );
    const list = await screen.findByRole("region", { name: "Referenced by" });
    expect(toggle, "Referenced by's control names no list it opens").toHaveAttribute(
      "aria-controls",
      list.id
    );
  } finally {
    unmount();
    getReferences.mockRestore();
    restore();
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

// The add menu is a menu a reader on the keyboard can use: it opens with the focus on its first
// entry, the arrow keys walk it, and Escape closes it with the focus back on Add and is taken
// there, so the phone's Details sheet, which closes on an Escape no control took, stays open.
test("the add menu opened by the keyboard takes the focus, walks by the arrows and gives it back on Escape", async () => {
  const user = userEvent.setup();
  const { restore } = stubIssueApi();
  const escapes = watchEscapes();
  const { unmount } = renderDetails();

  try {
    const add = await screen.findByRole("button", { name: CONTROL_LABELS.add });
    add.focus();
    await user.keyboard("{Enter}");
    const entries = await screen.findAllByRole("menuitem");
    expect(entries.length, "the add menu lists fewer than two entries to walk").toBeGreaterThan(1);
    await waitFor(() =>
      expect(document.activeElement, "the add menu opened with the focus left on Add").toBe(
        entries[0]
      )
    );
    await user.keyboard("{ArrowDown}");
    expect(document.activeElement, "ArrowDown did not move to the next entry").toBe(entries[1]);
    await user.keyboard("{ArrowUp}");
    expect(document.activeElement, "ArrowUp did not move back to the first entry").toBe(entries[0]);
    await user.keyboard("{Escape}");
    expect(screen.queryByRole("menu"), "Escape left the add menu open").not.toBeInTheDocument();
    expect(document.activeElement, "Escape did not give the focus back to Add").toBe(add);
    expect(
      escapes.taken,
      "Escape in the add menu went on to the page's keys, which close Details on a phone"
    ).not.toContain(false);

    // An entry chosen by the keyboard opens its control, as one chosen by the pointer does.
    await user.keyboard("{Enter}");
    await waitFor(() => expect(document.activeElement).toBe(screen.getAllByRole("menuitem")[0]));
    await user.keyboard("{Enter}");
    await waitFor(() =>
      expect(
        screen.getByLabelText(PROPERTY_LABELS.parent),
        "the menu's Parent opened no parent's editor"
      ).toHaveFocus()
    );
  } finally {
    escapes.stop();
    unmount();
    restore();
  }
});

// R13: the add control lists what has no value yet, so where nothing can be added - a closed
// issue, or one with a value for every property the menu lists - it is not drawn at all.
test("the add control is not drawn on a closed issue, nor when every property has a value", async () => {
  const { getIssue, restore } = stubIssueApi();
  // The Advances row reads the priorities, to name the one the issue advances.
  const priorities = spyOn(api, "listPriorities").mockResolvedValue({
    empty: "No priority is live now, so the list is empty.",
    priorities: [],
    priority_work_parked: { by_priority: {}, count: 0, issues: [] },
    work_under_no_priority: { by_project: {}, count: 0, issues: [] },
  });
  getIssue.mockResolvedValue({ ...issue, closed_at: "2026-09-10T00:00:00Z", status: "done" });
  let view = renderDetails();

  try {
    await screen.findByText(PROPERTY_LABELS.parent, { selector: "[data-page-chrome]" });
    expect(
      screen.queryByRole("button", { name: CONTROL_LABELS.add }),
      "the add control is drawn on a closed issue"
    ).not.toBeInTheDocument();
    view.unmount();

    // A value for each of the eight properties the menu lists: the components are the parent's.
    getIssue.mockResolvedValue({
      ...issue,
      advances: [{ id: "PRI-7", inherited: false }],
      assignee: "alice",
      blocked_by: ["CORE-3"],
      components: {
        mode: "inherit",
        ids: ["api"],
        unknown: [],
        reason: null,
        inherited_from: "CORE-2",
      },
      labels: ["bug"],
      parent: "CORE-2",
      repo: "acme/widgets",
      resolved_repo: "acme/widgets",
      route: "role:legion-controller-core",
    });
    view = renderDetails();
    await screen.findByRole("link", { name: "CORE-3" });
    await waitFor(() => expect(priorities).toHaveBeenCalled());
    expect(
      screen.queryByRole("button", { name: CONTROL_LABELS.add }),
      "the add control is drawn on an issue with a value for every property it lists"
    ).not.toBeInTheDocument();
  } finally {
    view.unmount();
    priorities.mockRestore();
    restore();
  }
});

// R5 and the review of 5f46a483: Add is drawn while a property it lists has no value and its
// control is on the page. An issue with a value for every other property loses its last label;
// while the save is out the labels' control is named "Saving labels of CORE-1", and in the render
// where the save landed Add looked for "Add a label to CORE-1", which that render's commit had not
// drawn yet, so it drew nothing and nothing drew it again.
test("removing the last label draws Add, whose Labels the row's control now answers", async () => {
  const user = userEvent.setup();
  const { getIssue, patchIssue, restore } = stubIssueApi();
  const listIssues = spyOn(api, "listIssues").mockResolvedValue([]);
  const priorities = spyOn(api, "listPriorities").mockResolvedValue({
    empty: "No priority is live now, so the list is empty.",
    priorities: [],
    priority_work_parked: { by_priority: {}, count: 0, issues: [] },
    work_under_no_priority: { by_project: {}, count: 0, issues: [] },
  });
  const full: IssueDetails = {
    ...issue,
    advances: [{ id: "PRI-7", inherited: false }],
    assignee: "alice",
    blocked_by: ["CORE-3"],
    components: {
      mode: "inherit",
      ids: ["api"],
      unknown: [],
      reason: null,
      inherited_from: "CORE-2",
    },
    labels: ["bug"],
    parent: "CORE-2",
    repo: "acme/widgets",
    resolved_repo: "acme/widgets",
    route: "role:legion-controller-core",
  };
  getIssue.mockResolvedValue(full);
  patchIssue.mockImplementation(async (_key, update) =>
    narrowIssue({ ...full, labels: [...(update.labels ?? full.labels ?? [])] })
  );
  const view = renderDetails();
  try {
    await screen.findByRole("link", { name: "CORE-3" });
    await waitFor(() => expect(priorities).toHaveBeenCalled());
    expect(screen.queryByRole("button", { name: CONTROL_LABELS.add })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Edit labels" }));
    await user.click(await screen.findByRole("option", { name: "bug" }));
    await user.keyboard("{Escape}");
    await waitFor(() => expect(patchIssue).toHaveBeenCalledTimes(1));
    expect(
      await screen.findByRole("button", {
        name: labelsControlName("CORE-1", {
          labels: [],
          saving: false,
          words: CONTROL_NAME_WORDS,
        }),
      }),
      "the label was not removed"
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(
        screen.queryByRole("button", { name: CONTROL_LABELS.add }),
        "Labels has no value and its control is drawn, but Details draws no Add"
      ).toBeInTheDocument()
    );
    await user.click(screen.getByRole("button", { name: CONTROL_LABELS.add }));
    expect(
      await screen.findByRole("menuitem", { name: PROPERTY_LABELS.labels }),
      "the add menu lists no Labels for the control the row draws"
    ).toBeInTheDocument();
  } finally {
    view.unmount();
    listIssues.mockRestore();
    priorities.mockRestore();
    restore();
  }
});

// R5 and the review of 5906c38b: Add is drawn again when a control it reaches comes back in a
// commit of the component that draws it. On an issue with a value for every property but the
// priorities it advances, Add lists Advances alone. Picking it presses "Add a priority to
// CORE-1", whose form replaces that control in the same commit, so Add, finding no control, went;
// Cancel drew the control again in a commit of the priority line alone, which Add never read, so
// Add stayed gone beside the control it reaches.
test("Add is drawn again after the Advances form it opened is cancelled", async () => {
  const user = userEvent.setup();
  const { getIssue, restore } = stubIssueApi();
  const priorities = spyOn(api, "listPriorities").mockResolvedValue({
    empty: "No priority is live now, so the list is empty.",
    priorities: [],
    priority_work_parked: { by_priority: {}, count: 0, issues: [] },
    work_under_no_priority: { by_project: {}, count: 0, issues: [] },
  });
  getIssue.mockResolvedValue({
    ...issue,
    advances: [],
    assignee: "alice",
    blocked_by: ["CORE-3"],
    components: {
      mode: "inherit",
      ids: ["api"],
      unknown: [],
      reason: null,
      inherited_from: "CORE-2",
    },
    labels: ["bug"],
    parent: "CORE-2",
    repo: "acme/widgets",
    resolved_repo: "acme/widgets",
    route: "role:legion-controller-core",
  });
  const view = renderDetails();
  try {
    await screen.findByRole("link", { name: "CORE-3" });
    await user.click(await screen.findByRole("button", { name: CONTROL_LABELS.add }));
    await user.click(await screen.findByRole("menuitem", { name: PROPERTY_LABELS.advances }));
    const cancel = await screen.findByRole("button", { name: CONTROL_LABELS.cancel });
    await user.click(cancel);
    expect(
      await screen.findByRole("button", { name: addPriorityControlName("CORE-1") }),
      "the priority's add control is not back"
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(
        screen.queryByRole("button", { name: CONTROL_LABELS.add }),
        "Advances has no value and its control is drawn, but Details draws no Add"
      ).toBeInTheDocument()
    );
  } finally {
    view.unmount();
    priorities.mockRestore();
    restore();
  }
});

// The review of 5906c38b: with Details open, the repository's control draws its select only once
// its own read of the project's mappings answers, in a commit of that control alone. Add, read
// after its own commits only, never saw the select, so on an issue with a value for every other
// property Details drew no Add beside the control it would press.
test("Add is drawn once the repository's control draws its select", async () => {
  const user = userEvent.setup();
  const { getIssue, restore } = stubIssueApi();
  const priorities = spyOn(api, "listPriorities").mockResolvedValue({
    empty: "No priority is live now, so the list is empty.",
    priorities: [],
    priority_work_parked: { by_priority: {}, count: 0, issues: [] },
    work_under_no_priority: { by_project: {}, count: 0, issues: [] },
  });
  // Open, Details reads what waits on the issue.
  const reference = buildDispatchReference({ key: "CORE-1", kind: "issue" });
  const getReferences = spyOn(api, "getReferences").mockResolvedValue({
    edges: [],
    node: graphNode({ id: "CORE-1", issue_key: "CORE-1", kind: "issue", ref: reference }),
  });
  // The issue resolves to no repository, so its control reads the mappings at once and draws
  // its select once they answer with one for the project.
  const mappings = Promise.withResolvers<RepoProject[]>();
  const listRepoProjects = spyOn(api, "listRepoProjects").mockImplementation(
    () => mappings.promise
  );
  getIssue.mockResolvedValue({
    ...issue,
    advances: [{ id: "PRI-7", inherited: false }],
    assignee: "alice",
    blocked_by: ["CORE-3"],
    components: {
      mode: "inherit",
      ids: ["api"],
      unknown: [],
      reason: null,
      inherited_from: "CORE-2",
    },
    labels: ["bug"],
    parent: "CORE-2",
    route: "role:legion-controller-core",
  });
  const drawer: MarginDrawer = { open: true, setOpen: () => undefined, setPage: () => undefined };
  const { unmount } = renderDetails(undefined, drawer);

  try {
    // Every other read of the panel has answered, so the select is drawn in a commit of the
    // repository's control alone.
    await screen.findByRole("link", { name: "CORE-3" });
    await waitFor(() => expect(priorities).toHaveBeenCalled());
    await waitFor(() =>
      expect(detailsRow(PROPERTY_LABELS.blocks)).toHaveTextContent(CONTROL_LABELS.none)
    );
    expect(listRepoProjects).toHaveBeenCalled();
    expect(
      screen.queryByRole("button", { name: CONTROL_LABELS.add }),
      "Add is drawn before the one control it reaches is"
    ).not.toBeInTheDocument();

    mappings.resolve([
      {
        created_at: "2026-09-10T00:00:00Z",
        created_by: { id: "alice", kind: "user" },
        default: true,
        project: "CORE",
        renamed_to: null,
        repo: "acme/widgets",
      },
    ]);
    expect(
      await screen.findByRole("combobox", { name: repoControlName("CORE-1") }),
      "the repository's control drew no select for the mapping"
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(
        screen.queryByRole("button", { name: CONTROL_LABELS.add }),
        "Repository has no value and its control is drawn, but Details draws no Add"
      ).toBeInTheDocument()
    );
    await user.click(screen.getByRole("button", { name: CONTROL_LABELS.add }));
    expect(
      await screen.findByRole("menuitem", { name: PROPERTY_LABELS.repository }),
      "the add menu lists no Repository for the select the row draws"
    ).toBeInTheDocument();
  } finally {
    unmount();
    listRepoProjects.mockRestore();
    getReferences.mockRestore();
    priorities.mockRestore();
    restore();
  }
});

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
  const saving = Promise.withResolvers<Issue>();
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

/** A second caller that saves a title and picks a status while that title is out, as the header
 *  does when the reader picks a status before the title they saved has landed. */
function RetitleThenStart({ issue: details }: { issue: IssueDetails }): ReactNode {
  const writes = useIssueHeaderWrites(details, stateForIssue(undefined, details.key));
  return (
    <>
      <button
        onClick={() => {
          writes.drafts.writeTitle("Review the spec again");
          writes.drafts.requestTitleSubmit();
        }}
        type="button"
      >
        Retitle it from the header
      </button>
      <button onClick={() => writes.pickStatus("in_progress")} type="button">
        Start it from the header
      </button>
    </>
  );
}

// A status picked while another write of the issue is out is held for every caller by part E's
// `pickStatus` (`useIssueHeaderWrites.ts`), so Details draws it at once, and it is sent once,
// after that write.
test("Details' Status reads a status picked behind another write", async () => {
  const user = userEvent.setup();
  const { patchIssue, restore } = stubIssueApi();
  const retitling = Promise.withResolvers<Issue>();
  patchIssue
    .mockImplementationOnce(() => retitling.promise)
    .mockImplementationOnce(async () =>
      narrowIssue({ ...issue, status: "in_progress", title: "Review the spec again" })
    );
  const { queryClient, unmount } = renderDetails((details) => <RetitleThenStart issue={details} />);

  try {
    await screen.findByText(PROPERTY_LABELS.status, { selector: "[data-page-chrome]" });
    expect(detailsRow(PROPERTY_LABELS.status)).toHaveTextContent(issueStatusWord("todo"));
    await user.click(screen.getByRole("button", { name: "Retitle it from the header" }));
    await waitFor(() =>
      expect(patchIssue).toHaveBeenCalledWith(
        "CORE-1",
        { title: "Review the spec again" },
        keyedAttempt
      )
    );

    await user.click(screen.getByRole("button", { name: "Start it from the header" }));
    await waitFor(() =>
      expect(
        detailsRow(PROPERTY_LABELS.status),
        "Details' Status kept the saved status while a pick held behind the header's title stood"
      ).toHaveTextContent(issueStatusWord("in_progress"))
    );
    expect(
      patchIssue,
      "the pick went out while the title write before it was out"
    ).toHaveBeenCalledTimes(1);

    retitling.resolve(narrowIssue({ ...issue, title: "Review the spec again" }));
    await waitFor(() => expect(patchIssue).toHaveBeenCalledTimes(2));
    expect(
      patchIssue.mock.calls.map(([, input]) => input),
      "the held status did not go out after the title write it was held behind"
    ).toEqual([{ title: "Review the spec again" }, { status: "in_progress" }]);

    // Once no write is out, the row reads the status the server answered with.
    await waitFor(() => {
      expect(queryClient.isMutating()).toBe(0);
      expect(queryClient.getQueryData(issueQuery("CORE-1").queryKey)?.status).toBe("in_progress");
    });
    expect(detailsRow(PROPERTY_LABELS.status)).toHaveTextContent(issueStatusWord("in_progress"));
    expect(patchIssue, "the held status was sent more than once").toHaveBeenCalledTimes(2);
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
    const development = await screen.findByRole("button", {
      name: MARGIN_SECTION_NAMES.development,
    });
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
      screen.getByRole("button", { name: MARGIN_SECTION_NAMES.development }),
      "the panel was drawn again when the margin's cards went"
    ).toBe(development);
  } finally {
    view.unmount();
    restore();
  }
});

// A control elsewhere on the page that opens Details at one of its rows asks the drawer for that
// row first (decision 69 of the parts): the section that holds the row opens for it as its
// heading opens it, so the reader's kept choice follows, and a section already open stays open.
test("a request for the Labels row opens a closed Development in the drawer that holds the panel", async () => {
  const { restore } = stubIssueApi();
  const kept = userPreferenceStorageKey("alice", "issue.margin-sections");
  window.localStorage.setItem(kept, "development");
  const queryClient = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false, staleTime: Infinity } },
  });
  const view = render(
    <MemoryRouter initialEntries={["/issues/CORE-1"]}>
      <QueryClientProvider client={queryClient}>
        <MarginProvider>
          <MarginSlotProvider drawsSlot>
            <DetailsSlot issue={issue} route={{ key: "CORE-1", kind: "issue" }} />
            <aside {...{ [DETAILS_ATTRIBUTE]: "" }}>
              <MarginBody />
            </aside>
          </MarginSlotProvider>
        </MarginProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );

  try {
    const development = await screen.findByRole("button", {
      name: MARGIN_SECTION_NAMES.development,
    });
    await waitFor(() => expect(development).toHaveAttribute("aria-expanded", "false"));
    const drawer = development.closest(`aside[${DETAILS_ATTRIBUTE}]`);
    expect(drawer, "the panel is not in the drawer's aside").not.toBeNull();
    const request = (row: DetailsRow) =>
      act(() => {
        drawer?.dispatchEvent(new CustomEvent(DETAILS_ROW_REQUEST_EVENT, { detail: row }));
      });

    request("labels");
    expect(development, "a request for the Labels row left Development closed").toHaveAttribute(
      "aria-expanded",
      "true"
    );
    expect(
      document.querySelector(`[${DETAILS_ROW_ATTRIBUTE}="labels"]`),
      "Development opened without its Labels row"
    ).not.toBeNull();
    expect(window.localStorage.getItem(kept), "the reader's kept choice did not follow").toBe("");

    request("labels");
    expect(development, "a request for a row of an open Development closed it").toHaveAttribute(
      "aria-expanded",
      "true"
    );
  } finally {
    view.unmount();
    restore();
    window.localStorage.removeItem(kept);
  }
});

// R2 and the part's source note: each row draws its name one time. The Labels row of an issue
// with no label reads its name, None, and a control that adds the first label; that control's
// visible word is not the row's name again (found by the tester on the preview of d4344116, where
// INFRA-3570's row read "Labels None Labels").
test("the Labels row of an issue with no label draws its name one time", async () => {
  const { restore } = stubIssueApi();
  const { unmount } = renderDetails();

  try {
    const row = await waitFor(() => rowAt("labels"));
    expect(row).toHaveTextContent(CONTROL_LABELS.none);
    expect(
      within(row).getByRole("button", {
        name: labelsControlName("CORE-1", {
          labels: [],
          saving: false,
          words: CONTROL_NAME_WORDS,
        }),
      }),
      "the Labels row of an issue with no label draws no Edit labels"
    ).toBeEnabled();
    expect(
      (row.textContent ?? "").split(PROPERTY_LABELS.labels).length - 1,
      "the Labels row draws its name two times on an issue with no label"
    ).toBe(1);
  } finally {
    unmount();
    restore();
  }
});

/** The issue page's scope of keys, pushed as the page pushes it (`useIssueKeymap.ts`), so a key
 *  the details slot binds in that scope is read. */
function IssueKeys(): null {
  useKeymapScope("issue");
  return null;
}

// R4, R5 and decision 112 of the parts: on an issue with no label the labels' control is named
// for adding one (`labelsControlName`), and the add menu's Labels and the key `l` find it by that
// name. While both looked for "Edit labels", the menu listed no Labels on such an issue and `l`
// did nothing.
test("on an issue with no label, the Add menu's Labels and the key l reach the labels control", async () => {
  const user = userEvent.setup();
  const { restore } = stubIssueApi();
  // The labels' popover reads the project's issues for the labels it offers.
  const listIssues = spyOn(api, "listIssues").mockResolvedValue([]);
  const queryClient = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false, staleTime: Infinity } },
  });
  const view = render(
    <MemoryRouter initialEntries={["/issues/CORE-1"]}>
      <QueryClientProvider client={queryClient}>
        <KeymapProvider>
          <IssueKeys />
          <MarginProvider>
            <MarginSlotProvider drawsSlot>
              <DetailsSlot issue={issue} route={{ key: "CORE-1", kind: "issue" }} />
              <MarginBody />
            </MarginSlotProvider>
          </MarginProvider>
        </KeymapProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );

  try {
    const control = within(await waitFor(() => rowAt("labels"))).getByRole("button", {
      name: labelsControlName("CORE-1", { labels: [], saving: false, words: CONTROL_NAME_WORDS }),
    });
    expect(control).toHaveAttribute("aria-expanded", "false");

    await user.click(screen.getByRole("button", { name: CONTROL_LABELS.add }));
    await user.click(await screen.findByRole("menuitem", { name: PROPERTY_LABELS.labels }));
    await waitFor(() =>
      expect(control, "the add menu's Labels did not open the labels control").toHaveAttribute(
        "aria-expanded",
        "true"
      )
    );
    await user.keyboard("{Escape}");
    await waitFor(() => expect(control).toHaveAttribute("aria-expanded", "false"));

    await user.keyboard("l");
    await waitFor(() =>
      expect(control, "l did not open the labels control").toHaveAttribute("aria-expanded", "true")
    );
  } finally {
    view.unmount();
    listIssues.mockRestore();
    restore();
  }
});

// R4 and decision 145 of the parts: while a save of the labels is out, the labels' control is
// named for it ("Saving labels of CORE-1", `labelsControlName` given the page's `ANSWER_LINES`),
// and the key `l` finds it by that name; the control is then busy, `aria-disabled` and refusing
// to open, so `l` is off until the save lands. While `l` read only `disabled`, the list of keys
// named it enabled for the length of the save and the key pressed a control that opened nothing.
test("while the labels are saving, the key l is off, and on again once the save lands", async () => {
  const user = userEvent.setup();
  const { patchIssue, restore } = stubIssueApi();
  // The labels' popover reads the project's issues for the labels it offers.
  const listIssues = spyOn(api, "listIssues").mockResolvedValue([]);
  const labelled: IssueDetails = { ...issue, labels: ["bug"] };
  const saving = Promise.withResolvers<Issue>();
  patchIssue.mockImplementationOnce(() => saving.promise);
  const queryClient = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false, staleTime: Infinity } },
  });
  const view = render(
    <MemoryRouter initialEntries={["/issues/CORE-1"]}>
      <QueryClientProvider client={queryClient}>
        <KeymapProvider>
          <IssueKeys />
          <MarginProvider>
            <MarginSlotProvider drawsSlot>
              <DetailsSlot issue={labelled} route={{ key: "CORE-1", kind: "issue" }} />
              <MarginBody />
            </MarginSlotProvider>
          </MarginProvider>
        </KeymapProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  const savingName = labelsControlName("CORE-1", {
    labels: [],
    saving: true,
    words: CONTROL_NAME_WORDS,
  });

  try {
    const row = await waitFor(() => rowAt("labels"));
    await user.click(
      within(row).getByRole("button", {
        name: labelsControlName("CORE-1", {
          labels: ["bug"],
          saving: false,
          words: CONTROL_NAME_WORDS,
        }),
      })
    );
    await user.click(await screen.findByRole("option", { name: "bug" }));
    await user.keyboard("{Escape}");
    await waitFor(() => expect(patchIssue).toHaveBeenCalledTimes(1));
    expect(
      await within(row).findByRole("button", { name: savingName }),
      "the labels control is not named for its save while the save is out"
    ).toBeInTheDocument();
    expect(
      appKeymap.describe().find((entry) => entry.id === "labels")?.enabled,
      "l is enabled while the labels control it presses is busy with the save"
    ).toBe(false);

    saving.resolve(narrowIssue({ ...labelled, labels: [] }));
    await waitFor(() =>
      expect(within(row).queryByRole("button", { name: savingName })).not.toBeInTheDocument()
    );
    expect(
      appKeymap.describe().find((entry) => entry.id === "labels")?.enabled,
      "l is not on again once the labels' save has landed"
    ).toBe(true);
  } finally {
    view.unmount();
    listIssues.mockRestore();
    restore();
  }
});

/** The project's one architecture source, so the components line draws its picker
 *  (`shared-writes.test.ts`'s `architectureSource`). */
const architectureSource: ArchitectureSource = {
  branch: "main",
  components: 1,
  created_at: "2026-09-10T00:00:00Z",
  created_by: { id: "alice", kind: "user" },
  enabled: true,
  last_commit: "3be61df5be90",
  last_error: null,
  last_sync_at: "2026-09-17T00:00:00Z",
  project: "CORE",
  repo: "legion/legion",
};

// R11 and decision 114 of the parts: on an issue no component is attached to, the components
// picker's trigger draws "Set components" and is named for it inside `NoTooltips`
// (`componentsControlName` given the attachment the line draws), and the add menu's Components
// finds it by that name. While the menu looked for `main`'s "Edit components of CORE-1", it
// listed no Components on such an issue.
test("on an issue with no component, the Add menu's Components reaches the picker", async () => {
  const user = userEvent.setup();
  const { restore } = stubIssueApi();
  // The fixture's issue is not attached. A source draws the picker, and the open picker reads
  // the project's architecture for the components it offers.
  const sources = spyOn(api, "listProjectArchitectureSources").mockResolvedValue([
    architectureSource,
  ]);
  const tree = spyOn(api, "getArchitecture").mockResolvedValue(architecture);
  const { unmount } = renderDetails();

  try {
    const picker = await screen.findByRole("button", {
      name: componentsControlName("CORE-1", {
        components: issue.components,
        saving: false,
        words: CONTROL_NAME_WORDS,
      }),
    });
    expect(picker).toHaveAttribute("aria-expanded", "false");

    await user.click(screen.getByRole("button", { name: CONTROL_LABELS.add }));
    await user.click(await screen.findByRole("menuitem", { name: PROPERTY_LABELS.components }));
    await waitFor(() =>
      expect(picker, "the add menu's Components did not open the picker").toHaveAttribute(
        "aria-expanded",
        "true"
      )
    );
  } finally {
    unmount();
    tree.mockRestore();
    sources.mockRestore();
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

// R3: the parent is set, changed and cleared in its own row, from the control that row draws
// first, where the old page opened its editor from the overflow menu.
test("Details saves a parent from the Parent row's editor", async () => {
  const user = userEvent.setup();
  const { patchIssue, restore } = stubIssueApi();
  const { unmount } = renderDetails();

  try {
    await screen.findByText(PROPERTY_LABELS.parent, { selector: "[data-page-chrome]" });
    await user.click(
      within(detailsRow(PROPERTY_LABELS.parent)).getByRole("button", {
        name: CONTROL_LABELS.set_parent,
      })
    );
    await waitFor(() =>
      expect(
        screen.queryAllByLabelText(PROPERTY_LABELS.parent),
        "the Parent row's control opened no editor"
      ).toHaveLength(1)
    );
    await user.click(screen.getByLabelText(PROPERTY_LABELS.parent));
    await user.paste("CORE-9");
    await user.click(screen.getByRole("button", { name: CONTROL_LABELS.save_parent }));

    await waitFor(() =>
      expect(patchIssue).toHaveBeenLastCalledWith("CORE-1", { parent: "CORE-9" }, keyedAttempt)
    );
    // The row draws the parent it saved as the way to it.
    const link = await within(detailsRow(PROPERTY_LABELS.parent)).findByRole("link", {
      name: "CORE-9",
    });
    expect(link).toHaveAttribute("href", "/issues/CORE-9");
  } finally {
    unmount();
    restore();
  }
});

test("Details clears the parent with an empty editor save", async () => {
  const user = userEvent.setup();
  const { getIssue, patchIssue, restore } = stubIssueApi();
  getIssue.mockResolvedValue({ ...issue, parent: "CORE-9" });
  const { unmount } = renderDetails();

  try {
    await screen.findByRole("link", { name: "CORE-9" });
    await user.click(
      within(detailsRow(PROPERTY_LABELS.parent)).getByRole("button", {
        name: CONTROL_LABELS.edit_parent,
      })
    );
    const parent = (await screen.findByLabelText(PROPERTY_LABELS.parent)) as HTMLInputElement;
    expect(parent.value).toBe("CORE-9");
    await user.clear(parent);
    await user.click(screen.getByRole("button", { name: CONTROL_LABELS.save_parent }));

    await waitFor(() =>
      expect(patchIssue).toHaveBeenLastCalledWith("CORE-1", { parent: null }, keyedAttempt)
    );
    // The row's first control is the one the header's Set parent issue presses.
    await waitFor(() =>
      expect(
        detailsRow(PROPERTY_LABELS.parent).querySelector(
          "input, textarea, select, button:not(:disabled), a[href]"
        ),
        "the Parent row still offers to edit a parent the issue no longer has"
      ).toHaveTextContent(CONTROL_LABELS.set_parent)
    );
    expect(screen.queryByRole("link", { name: "CORE-9" })).not.toBeInTheDocument();
  } finally {
    unmount();
    restore();
  }
});

// The editor closes as the save goes out, so a parent Dispatch refuses is said in the row the
// reader saved it from, in Dispatch's own words, and not as the line for a write that failed. The
// focus is back on the row's control by then, so the line is an alert: it is announced as it
// arrives.
test("a parent Dispatch refuses is said in the Parent row with Dispatch's reason", async () => {
  const user = userEvent.setup();
  const { patchIssue, restore } = stubIssueApi();
  const reason = "would create a cycle: CORE-1 → CORE-9 → CORE-1";
  patchIssue.mockImplementationOnce(() =>
    Promise.reject(new ApiError(409, { code: "PARENT_INPUT", error: reason }))
  );
  const { unmount } = renderDetails();

  try {
    await screen.findByText(PROPERTY_LABELS.parent, { selector: "[data-page-chrome]" });
    await user.click(
      within(detailsRow(PROPERTY_LABELS.parent)).getByRole("button", {
        name: CONTROL_LABELS.set_parent,
      })
    );
    await user.click(await screen.findByLabelText(PROPERTY_LABELS.parent));
    await user.paste("CORE-9");
    await user.click(screen.getByRole("button", { name: CONTROL_LABELS.save_parent }));

    await waitFor(() => expect(patchIssue).toHaveBeenCalledTimes(1));
    await waitFor(() =>
      expect(
        detailsRow(PROPERTY_LABELS.parent).querySelector("#issue-parent-error")?.textContent,
        "the Parent row does not say why Dispatch refused the parent"
      ).toBe(reason)
    );
    expect(
      within(detailsRow(PROPERTY_LABELS.parent)).queryByRole("alert")?.textContent,
      "the Parent row does not announce why Dispatch refused the parent"
    ).toBe(reason);
    expect(screen.queryByText("Could not update this issue.")).not.toBeInTheDocument();
  } finally {
    unmount();
    restore();
  }
});

// The route's save is Details' own write, and inline, so the write store leaves its refusal to
// the row: a route Dispatch refuses is said where it was saved, in Dispatch's words and with
// Retry, as a refused parent is in the Parent row.
test("a route Dispatch refuses is said in the Messages go to row with Dispatch's reason and Retry", async () => {
  const user = userEvent.setup();
  const { patchIssue, restore } = stubIssueApi();
  const route = "role:legion-controller-core";
  const reason = "invalid route";
  patchIssue.mockImplementationOnce(() =>
    Promise.reject(new ApiError(400, { code: "ROUTE_INVALID", error: reason }))
  );
  const { unmount } = renderDetails();

  try {
    await user.click(await screen.findByRole("button", { name: "Messages default to no route" }));
    await user.click(await screen.findByLabelText(PROPERTY_LABELS.route));
    await user.paste(route);
    await user.click(screen.getByRole("button", { name: CONTROL_LABELS.save_route }));
    await waitFor(() => expect(patchIssue).toHaveBeenCalledWith("CORE-1", { route }, keyedAttempt));

    await waitFor(() =>
      expect(
        within(rowAt("route")).queryByRole("alert")?.textContent,
        "the Messages go to row does not say why Dispatch refused the route"
      ).toBe(reason)
    );
    await user.click(within(rowAt("route")).getByRole("button", { name: CONTROL_LABELS.retry }));
    await waitFor(() => expect(patchIssue).toHaveBeenCalledTimes(2));
    expect(
      patchIssue.mock.calls.map(([, input]) => input),
      "Retry did not send the refused route again"
    ).toEqual([{ route }, { route }]);
    await waitFor(() =>
      expect(
        within(rowAt("route")).queryByRole("alert"),
        "the refusal is still said after the route landed"
      ).not.toBeInTheDocument()
    );
  } finally {
    unmount();
    restore();
  }
});

// A refused route stays the draft until the reader leaves the editor, which puts the saved route
// back: the refusal is said while the draft still holds the route Dispatch refused, and not once
// it no longer does, when its Retry would send nothing.
test("a refused route is said only while the draft still holds it", async () => {
  const user = userEvent.setup();
  const { patchIssue, restore } = stubIssueApi();
  const route = "role:legion-controller-core";
  patchIssue.mockImplementationOnce(() =>
    Promise.reject(new ApiError(400, { code: "ROUTE_INVALID", error: "invalid route" }))
  );
  const { unmount } = renderDetails();

  try {
    await user.click(await screen.findByRole("button", { name: "Messages default to no route" }));
    await user.click(await screen.findByLabelText(PROPERTY_LABELS.route));
    await user.paste(route);
    await user.click(screen.getByRole("button", { name: CONTROL_LABELS.save_route }));
    await waitFor(() =>
      expect(within(rowAt("route")).queryByRole("alert")?.textContent).toBe("invalid route")
    );

    await user.click(
      within(rowAt("route")).getByRole("button", { name: `Messages default to ${route}` })
    );
    await user.click(within(rowAt("route")).getByRole("button", { name: CONTROL_LABELS.cancel }));
    expect(
      within(rowAt("route")).queryByRole("button", { name: "Messages default to no route" }),
      "Cancel did not put the saved route back"
    ).toBeInTheDocument();
    expect(
      within(rowAt("route")).queryByRole("alert"),
      "the row still says the refusal of a route the reader left, with a Retry that sends nothing"
    ).not.toBeInTheDocument();
  } finally {
    unmount();
    restore();
  }
});

// A route still out as the issue closes is refused (`ISSUE_CLOSED`): Dispatch saves no route of
// a closed issue, so the refusal is said with no Retry, which would send nothing, and the row's
// control, disabled now, names the route the issue holds rather than the one refused.
test("a route refused because the issue closed is said without Retry, and the row names the saved route", async () => {
  const user = userEvent.setup();
  const { getIssue, patchIssue, restore } = stubIssueApi();
  const saved = "role:legion-controller-core";
  const refused = "role:legion-reviewer";
  const reason = "CORE-1 is closed; reopen it first";
  getIssue.mockResolvedValue({ ...issue, route: saved });
  const saving = Promise.withResolvers<Issue>();
  patchIssue.mockImplementationOnce(() => saving.promise);
  const { queryClient, unmount } = renderDetails();

  try {
    await user.click(await screen.findByRole("button", { name: `Messages default to ${saved}` }));
    await user.clear(await screen.findByLabelText(PROPERTY_LABELS.route));
    await user.paste(refused);
    await user.click(screen.getByRole("button", { name: CONTROL_LABELS.save_route }));
    await waitFor(() =>
      expect(patchIssue).toHaveBeenCalledWith("CORE-1", { route: refused }, keyedAttempt)
    );

    // The issue closes while the save is out, and Dispatch refuses the save for it.
    const closed: IssueDetails = {
      ...issue,
      closed_at: "2026-09-10T00:00:00Z",
      route: saved,
      status: "done",
    };
    getIssue.mockResolvedValue(closed);
    act(() => {
      queryClient.setQueryData(issueQuery("CORE-1").queryKey, closed);
    });
    saving.reject(new ApiError(409, { code: "ISSUE_CLOSED", error: reason }));

    await waitFor(() =>
      expect(
        within(rowAt("route")).queryByRole("alert")?.textContent,
        "the Messages go to row does not say why Dispatch refused the route"
      ).toBe(reason)
    );
    expect(
      within(rowAt("route")).queryByRole("button", { name: CONTROL_LABELS.retry }),
      "the refusal of a route on a closed issue offers a Retry that sends nothing"
    ).not.toBeInTheDocument();
    expect(
      within(rowAt("route"))
        .getByRole("button", { name: /^Messages default to / })
        .getAttribute("aria-label"),
      "the route's control does not name the route the closed issue holds"
    ).toBe(`Messages default to ${saved}`);
  } finally {
    unmount();
    restore();
  }
});

// The box an editor of a row holds the focus in goes with the editor, so leaving it - by a save,
// by Cancel or by Escape - gives the focus back to the row's own control, not to the page.
test("leaving the parent's or the route's editor gives the focus back to its row's control", async () => {
  const user = userEvent.setup();
  const { restore } = stubIssueApi();
  const { unmount } = renderDetails();
  const control = (row: DetailsRow) =>
    rowAt(row).querySelector("input, textarea, select, button:not(:disabled), a[href]");

  try {
    await user.click(await screen.findByRole("button", { name: CONTROL_LABELS.set_parent }));
    await user.click(await screen.findByLabelText(PROPERTY_LABELS.parent));
    await user.paste("CORE-9");
    await user.keyboard("{Enter}");
    await waitFor(() =>
      expect(document.activeElement, "Save parent by Enter left the focus on the page").toBe(
        control("parent")
      )
    );

    await within(rowAt("parent")).findByRole("link", { name: "CORE-9" });
    await user.click(screen.getByRole("button", { name: CONTROL_LABELS.edit_parent }));
    await user.click(within(rowAt("parent")).getByRole("button", { name: CONTROL_LABELS.cancel }));
    expect(document.activeElement, "Cancel of the parent's editor left the focus on the page").toBe(
      control("parent")
    );
    expect(control("parent")).toHaveTextContent(CONTROL_LABELS.edit_parent);

    await user.click(screen.getByRole("button", { name: "Messages default to no route" }));
    await user.click(within(rowAt("route")).getByRole("button", { name: CONTROL_LABELS.cancel }));
    expect(document.activeElement, "Cancel of the route's editor left the focus on the page").toBe(
      control("route")
    );
  } finally {
    unmount();
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

/** The reader's state with the conversation `id` (`conversationItemId`'s `<kind>:<id>`) of
 *  CORE-1 pinned, as `pins.ts` writes a pin into `dismissed`. */
function pinnedState(id: string): UserState {
  return {
    "CORE-1": { dismissed: [pinnedItemMarker(id)], last_read_seq: 0, pinned: false, seq: 0 },
  };
}

/** A comment of CORE-1 as the comments read answers it, with the server's reading of its body
 *  where `tree` is given. */
function commentOf(id: string, body: string, tree?: DisplayTree): CommentWithTrees {
  return {
    ...commentDeliveryFields(),
    anchor: null,
    ask_id: null,
    author: { id: "alice", kind: "user" },
    body,
    ...(tree === undefined ? {} : { body_tree: tree }),
    created_at: "2026-09-09T00:00:00Z",
    edited_at: null,
    id,
    issue_key: "CORE-1",
    reply_to: null,
    resolved: false,
    resolved_at: null,
    resolved_by: null,
    suggestion: null,
    turn: null,
  };
}

// R17: until the read that holds a pin's words answers, the pin is drawn by the page's word for
// its kind, never by the id it was pinned under; once it answers, by its first line.
test("a pin whose words are not read yet is drawn by its kind's word, never its id", async () => {
  const id = "5b0f9e64-2c1d-4a7e-8f3b-91d2c6a4e870";
  const { getMyState, pinReads, restore } = stubIssueApi();
  getMyState.mockResolvedValue(pinnedState(conversationItemId("comment", id)));
  const comments = Promise.withResolvers<CommentWithTrees[]>();
  pinReads.comments.mockImplementation(() => comments.promise);
  const { unmount } = renderDetails();

  try {
    const pins = await screen.findByRole("region", { name: CONTROL_LABELS.pinned });
    const link = await within(pins).findByRole("link");
    await waitFor(() => expect(pinReads.comments).toHaveBeenCalled());
    expect(
      link.textContent,
      "the pin is not drawn by its kind's word while its words are unread"
    ).toBe(NODE_TYPE_NAMES.comment);
    for (const part of id.split("-")) {
      expect(link.textContent, "the pin is drawn by its comment's id").not.toContain(part);
    }

    comments.resolve([commentOf(id, "Ship the fix tonight. Then watch the board.")]);
    await waitFor(() =>
      expect(
        within(pins).getByRole("link").textContent,
        "the pin is not drawn by its comment's first line once the comments are read"
      ).toBe("Ship the fix tonight.")
    );
  } finally {
    unmount();
    restore();
  }
});

// R17: no read holds a handoff's words, so its pin is drawn by the page's word for it, and it
// opens the history, where the handoff is.
test("a pinned handoff is drawn by its kind's word and opens the history", async () => {
  const { getMyState, restore } = stubIssueApi();
  getMyState.mockResolvedValue(
    pinnedState(conversationItemId("handoff", "8e2d4c1a-7b3f-4e9d-a6c5-0f1b2d3e4a5c"))
  );
  const { unmount } = renderDetails();

  try {
    const pins = await screen.findByRole("region", { name: CONTROL_LABELS.pinned });
    const link = await within(pins).findByRole("link");
    expect(link.textContent).toBe(NODE_TYPE_NAMES.handoff);
    expect(link, "the handoff's pin does not open the history").toHaveAttribute(
      "href",
      "/issues/CORE-1/activity"
    );
  } finally {
    unmount();
    restore();
  }
});

// R14: a pin's line is its record's words as its row in the outline reads them, from the
// server's reading of the text, so the symbols that format the body are not drawn as text.
test("a pinned comment written in markdown is drawn by its words, without the marks", async () => {
  const id = "3c9a7e15-6d2b-4f80-b1e4-7a5c9d0e2f36";
  const { getMyState, pinReads, restore } = stubIssueApi();
  getMyState.mockResolvedValue(pinnedState(conversationItemId("comment", id)));
  const reading: DisplayTree = {
    content: [
      {
        content: [
          { marks: [{ type: "strong" }], text: "Bold", type: "text" },
          { text: " words. Then more.", type: "text" },
        ],
        type: "paragraph",
      },
    ],
    type: "doc",
  };
  pinReads.comments.mockResolvedValue([commentOf(id, "**Bold** words. Then more.", reading)]);
  const { unmount } = renderDetails();

  try {
    const pins = await screen.findByRole("region", { name: CONTROL_LABELS.pinned });
    await waitFor(() =>
      expect(
        within(pins).getByRole("link").textContent,
        "the pin is not drawn by its comment's words"
      ).toBe("Bold words.")
    );
    expect(within(pins).getByRole("link").textContent).not.toContain("*");
  } finally {
    unmount();
    restore();
  }
});

// An Unpin takes its pin's row away, so the focus goes on to the next pin's link, else the one
// before it, else the Pinned heading, rather than falling to the page.
test("Unpin gives the focus to the next pin, else the one before, else the Pinned heading", async () => {
  const user = userEvent.setup();
  const { getMyState, pinReads, restore } = stubIssueApi();
  const [first, second, third] = [
    "1d4f0a6e-3b2c-4e8d-9a7f-5c6b1e2d3f40",
    "2e5a1b7f-4c3d-4f9e-8b6a-6d7c2f3e4a51",
    "3f6b2c8a-5d4e-4a0f-9c7b-7e8d3a4f5b62",
  ] as const;
  getMyState.mockResolvedValue({
    "CORE-1": {
      dismissed: [first, second, third].map((id) =>
        pinnedItemMarker(conversationItemId("comment", id))
      ),
      last_read_seq: 0,
      pinned: false,
      seq: 0,
    },
  });
  pinReads.comments.mockResolvedValue([
    commentOf(first, "First pin."),
    commentOf(second, "Second pin."),
    commentOf(third, "Third pin."),
  ]);
  // Each unpin is held out, so the page shows its own state of the pins throughout.
  const putIssueState = spyOn(api, "putIssueState").mockReturnValue(new Promise(() => undefined));
  const { unmount } = renderDetails();
  const unpin = async (id: string) => {
    const row = document.querySelector(`[data-pin="${conversationItemId("comment", id)}"]`);
    if (!(row instanceof HTMLElement)) throw new Error(`Pinned draws no row for ${id}`);
    act(() => within(row).getByRole("button", { name: CONTROL_LABELS.unpin }).focus());
    await user.keyboard("{Enter}");
  };

  try {
    const pins = await screen.findByRole("region", { name: CONTROL_LABELS.pinned });
    await within(pins).findByRole("link", { name: "Third pin." });

    await unpin(second);
    await waitFor(() => expect(within(pins).getAllByRole("link")).toHaveLength(2));
    expect(document.activeElement, "Unpin did not give the focus to the next pin").toBe(
      within(pins).getByRole("link", { name: "Third pin." })
    );

    await unpin(third);
    await waitFor(() => expect(within(pins).getAllByRole("link")).toHaveLength(1));
    expect(
      document.activeElement,
      "Unpin of the last pin did not give the focus to the one before it"
    ).toBe(within(pins).getByRole("link", { name: "First pin." }));

    await unpin(first);
    await waitFor(() => expect(pins).toHaveTextContent(CONTROL_LABELS.none));
    expect(
      document.activeElement,
      "Unpin of the only pin did not give the focus to the Pinned heading"
    ).toBe(within(pins).getByRole("heading", { name: CONTROL_LABELS.pinned }));
  } finally {
    unmount();
    putIssueState.mockRestore();
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
