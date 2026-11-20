import { describe, expect, type Mock, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import {
  act,
  // biome-ignore lint/style/noRestrictedImports: the pointer drop drives dnd-kit with mouse presses and moves, which user-event cannot send
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";

import { answerIssuePatches } from "../../../__tests__/send-answers";
import { api, type WriteBounds } from "../../../api/client";
import type {
  Issue,
  IssueChild,
  IssueDetails,
  IssueSummary,
  StatusChangeAnswer,
  UpdateIssueInput,
  UserIssueState,
} from "../../../api/types";
import { useIssueHeaderWrites } from "../../issue-page/useIssueHeaderWrites";
import { issueStatuses } from "../../project/board-model";
import { IssueBoard } from "../../project/IssueBoard";
import { KeymapProvider } from "../../shell/KeymapProvider";
import { appKeymap, useKeymapScope } from "../../shell/keymap";
import { IssueHeader } from "../IssueHeader";
import { issuePriority } from "../issue-priority";
import { useIssuePriority } from "../useIssuePriority";
import { ParentStatusDialog } from "./ParentStatusDialog";
import { answerStatusChanges, type StatusChangeAnswerer } from "./status-change-fixtures";

// The inventory of every control of the page that writes an issue's status (INFRA-4731's plan,
// "Lists named before code"): each one, moving an issue with a parent or an open child, sends the
// rule's dry run before any write. A writer added to the page joins this list. The dry run is held,
// so what each case reads is the one request the move sent before it could write anything.

const openChild: IssueChild = {
  active_at: "2026-09-09T00:00:00Z",
  external_links: [],
  key: "CORE-2",
  status: "todo",
  subtree_done: 0,
  subtree_total: 1,
  title: "Child in work",
};

const issue: IssueDetails = {
  route_status: null,
  route_holder: null,
  route_status_reason: null,
  holder_state: null,
  last_seen: null,
  takeable_at: null,
  advances: [],
  advances_revision: 0,
  artifacts: [],
  children: [openChild],
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
  progress: { tasks: null, children: { done: 0, total: 1 } },
  parent: null,
  blocked_by: [],
  assignee: null,
  claim: null,
  components: { mode: "inherit", ids: [], unknown: [], reason: null, inherited_from: null },
  plan_artifact_id: "",
  decisions_artifact_id: "",
  primary_artifact_id: "",
  verification_artifact_id: "",
  project: "CORE",
  referenced_by_count: 0,
  route: null,
  repo: null,
  resolved_repo: null,
  status: "in_progress",
  priority: null,
  rank: "U",
  title: "Parent with a child in work",
  updated_at: "2026-09-09T00:00:00Z",
};

/** The same issue closed, under a parent: a reopen under a parent is a move the rule judges. */
const closedChild: IssueDetails = {
  ...issue,
  children: [],
  closed_at: "2026-09-10T00:00:00Z",
  parent: "CORE-9",
  progress: { tasks: null, children: null },
  status: "done",
};

const state: UserIssueState = { dismissed: [], last_read_seq: 0, pinned: false, seq: 0 };

function signedIn(): QueryClient {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
  });
  client.setQueryData(["whoami"], { event_head: "0", kind: "user", login: "alice" });
  return client;
}

/** The writes a case may send: the route's requests, and the issue's PATCH. */
interface Writes {
  readonly patches: Mock<
    (key: string, input: UpdateIssueInput, bounds: WriteBounds) => Promise<Issue>
  >;
  readonly statusChanges: Mock<StatusChangeAnswerer>;
}

/** Every request of the route, the dry run held, and every PATCH, which no case may reach. */
function answerWrites(): Writes {
  const statusChanges = answerStatusChanges().mockImplementation(
    () => new Promise<StatusChangeAnswer>(() => {})
  );
  const patches = answerIssuePatches();
  return { patches, statusChanges };
}

/** The move's dry run went out, and nothing was written before it. */
async function expectDryRunFirst(writes: Writes, key: string, status: string): Promise<void> {
  await waitFor(() => expect(writes.statusChanges).toHaveBeenCalledTimes(1));
  expect(writes.statusChanges.mock.calls[0]?.slice(0, 2)).toEqual([key, { dry_run: true, status }]);
  expect(writes.patches).not.toHaveBeenCalled();
}

function Header({ shown }: { shown: IssueDetails }): ReactNode {
  // The issue page pushes the scope its palette rows are offered under.
  useKeymapScope("issue");
  const priorityWrite = useIssuePriority(shown.key, issuePriority(shown.priority));
  return (
    <IssueHeader
      documentArtifact={undefined}
      isClosed={shown.closed_at !== null}
      issue={shown}
      priorityWrite={priorityWrite}
      state={state}
    />
  );
}

/** The issue page's header over `shown`, with the reads it makes answered empty. */
function renderHeader(shown: IssueDetails) {
  const reads = [
    spyOn(api, "getIssueSubscribers").mockResolvedValue([]),
    spyOn(api, "listRepoProjects").mockResolvedValue([]),
    spyOn(api, "listProjectArchitectureSources").mockResolvedValue([]),
  ];
  const view = render(
    <MemoryRouter>
      <QueryClientProvider client={signedIn()}>
        <KeymapProvider>
          <Header shown={shown} />
        </KeymapProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  return () => {
    view.unmount();
    for (const read of reads) read.mockRestore();
  };
}

/** The new issue page's Status pick (`useIssueHeaderWrites`' `pickStatus`), made with `status` by
 *  a press of its own, with the question the pick asks drawn as that page's control draws it. */
function NewPagePick({ shown, status }: { shown: IssueDetails; status: string }): ReactNode {
  const writes = useIssueHeaderWrites(shown, state);
  return (
    <>
      <button onClick={() => writes.pickStatus(status)} type="button">
        Pick {status}
      </button>
      {writes.statusQuestion === undefined ? null : (
        <ParentStatusDialog question={writes.statusQuestion} />
      )}
    </>
  );
}

/** The palette's row labelled `label`, as `$mod+k` offers it now. */
function paletteRow(label: string) {
  const row = appKeymap.actions().find((action) => action.label === label);
  if (row === undefined) throw new Error(`the palette offers no row "${label}"`);
  return row;
}

function card(overrides: Partial<IssueSummary>): IssueSummary {
  const {
    artifacts: _artifacts,
    children: _children,
    design_gates: _gates,
    holder_state: _holder,
    last_seen: _seen,
    open_asks: _asks,
    referenced_by_count: _references,
    takeable_at: _takeable,
    ...fields
  } = issue;
  return { ...fields, open_asks: 0, ...overrides };
}

/** The project board over `list`, with the reads it makes answered. */
function renderBoard(list: IssueSummary[], options: { lanes?: boolean; showEdges?: boolean } = {}) {
  const reads = [
    spyOn(api, "listIssues").mockResolvedValue(list),
    spyOn(api, "getMyState").mockResolvedValue({}),
  ];
  const view = render(
    <MemoryRouter>
      <QueryClientProvider client={signedIn()}>
        <KeymapProvider>
          <IssueBoard lanes={options.lanes} project="CORE" showEdges={options.showEdges} />
        </KeymapProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
  return () => {
    view.unmount();
    for (const read of reads) read.mockRestore();
  };
}

/**
 * Lays the board out as a browser would for dnd-kit, which reads where each droppable is: each
 * column 288 px wide, one after another in the order of the statuses, and each card 90 px tall
 * down its column. Nothing else on the page has a size.
 */
function layOutBoard() {
  return spyOn(Element.prototype, "getBoundingClientRect").mockImplementation(function (
    this: Element
  ) {
    const column = this.closest<HTMLElement>("[data-board-column]");
    const status = column?.dataset.boardColumn;
    if (column === null || status === undefined) return new DOMRect(0, 0, 0, 0);
    const left = issueStatuses.indexOf(status as (typeof issueStatuses)[number]) * 300;
    if (this === column) return new DOMRect(left, 0, 288, 2000);
    const cards = [...column.querySelectorAll("[data-board-card]")];
    const index = cards.indexOf(this);
    return index === -1 ? new DOMRect(0, 0, 0, 0) : new DOMRect(left, 60 + index * 100, 288, 90);
  });
}

/** Drags `article` with the mouse and drops it in the empty part of the `status` column. */
async function dropInColumn(article: HTMLElement, status: string): Promise<void> {
  const box = article.getBoundingClientRect();
  const from = { clientX: box.x + 10, clientY: box.y + 10 };
  const to = {
    clientX: issueStatuses.indexOf(status as (typeof issueStatuses)[number]) * 300 + 100,
    clientY: 1500,
  };
  await act(async () => {
    fireEvent.mouseDown(article, { button: 0, ...from });
  });
  await act(async () => {
    // The move that lifts the card, past dnd-kit's 8 px.
    fireEvent.mouseMove(document, { clientX: from.clientX, clientY: from.clientY + 40 });
  });
  await act(async () => {
    fireEvent.mouseMove(document, to);
  });
  await act(async () => {
    fireEvent.mouseUp(document, to);
  });
}

describe("every status writer of the page asks", () => {
  test("the header's status select", async () => {
    const writes = answerWrites();
    const user = userEvent.setup();
    const cleanup = renderHeader(issue);
    try {
      // An open issue's select offers no Done, so the move is to Backlog, which a parent with a
      // child in work cannot take either.
      await user.selectOptions(await screen.findByRole("combobox", { name: "Status" }), "backlog");
      await expectDryRunFirst(writes, "CORE-1", "backlog");
      expect(screen.getByRole("combobox", { name: "Status" })).toHaveValue("backlog");
    } finally {
      cleanup();
    }
  });

  test("the header's Close", async () => {
    const writes = answerWrites();
    const user = userEvent.setup();
    const cleanup = renderHeader(issue);
    try {
      await user.click(await screen.findByRole("button", { name: "Close issue" }));
      await expectDryRunFirst(writes, "CORE-1", "done");
    } finally {
      cleanup();
    }
  });

  test("the header's Reopen", async () => {
    const writes = answerWrites();
    const user = userEvent.setup();
    const cleanup = renderHeader(closedChild);
    try {
      await user.click(await screen.findByRole("button", { name: "Reopen issue" }));
      await expectDryRunFirst(writes, "CORE-1", "backlog");
    } finally {
      cleanup();
    }
  });

  test("the palette's Close issue", async () => {
    const writes = answerWrites();
    const cleanup = renderHeader(issue);
    try {
      await screen.findByRole("button", { name: "Close issue" });
      await act(async () => paletteRow("Close issue").run());
      await expectDryRunFirst(writes, "CORE-1", "done");
    } finally {
      cleanup();
    }
  });

  test("the palette's Reopen issue", async () => {
    const writes = answerWrites();
    const cleanup = renderHeader(closedChild);
    try {
      await screen.findByRole("button", { name: "Reopen issue" });
      await act(async () => paletteRow("Reopen issue").run());
      await expectDryRunFirst(writes, "CORE-1", "backlog");
    } finally {
      cleanup();
    }
  });

  test("the new issue page's Status pick", async () => {
    const writes = answerWrites();
    const user = userEvent.setup();
    const view = render(
      <QueryClientProvider client={signedIn()}>
        <KeymapProvider>
          <NewPagePick shown={issue} status="done" />
        </KeymapProvider>
      </QueryClientProvider>
    );
    try {
      await user.click(screen.getByRole("button", { name: "Pick done" }));
      await expectDryRunFirst(writes, "CORE-1", "done");
    } finally {
      view.unmount();
    }
  });

  test("the board's pointer drop", async () => {
    const writes = answerWrites();
    const layout = layOutBoard();
    const cleanup = renderBoard([card({ status: "retro" })], { showEdges: true });
    try {
      const board = await screen.findByRole("region", { name: "Project board" });
      await dropInColumn(within(board).getByRole("article", { name: /^CORE-1 / }), "done");
      await expectDryRunFirst(writes, "CORE-1", "done");
      // The card stays where it is until the person confirms.
      expect(
        within(within(board).getByRole("region", { name: "Retro" })).getByRole("article")
      ).toBeInTheDocument();
    } finally {
      cleanup();
      layout.mockRestore();
    }
  });

  test("the board's Shift+L", async () => {
    const writes = answerWrites();
    const user = userEvent.setup();
    const cleanup = renderBoard([card({ status: "retro" })]);
    try {
      const board = await screen.findByRole("region", { name: "Project board" });
      const article = within(board).getByRole("article", { name: /^CORE-1 / });
      act(() => article.focus());
      await user.keyboard("{Shift>}L{/Shift}");
      await expectDryRunFirst(writes, "CORE-1", "done");
    } finally {
      cleanup();
    }
  });

  test("the board's Shift+H", async () => {
    const writes = answerWrites();
    const user = userEvent.setup();
    const cleanup = renderBoard([card({ status: "todo" })]);
    try {
      const board = await screen.findByRole("region", { name: "Project board" });
      const article = within(board).getByRole("article", { name: /^CORE-1 / });
      act(() => article.focus());
      // Back to Backlog, which a parent with a child in work cannot take.
      await user.keyboard("{Shift>}H{/Shift}");
      await expectDryRunFirst(writes, "CORE-1", "backlog");
    } finally {
      cleanup();
    }
  });

  test("the board's lane move", async () => {
    const writes = answerWrites();
    const user = userEvent.setup();
    const cleanup = renderBoard([card({ priority: 0, status: "retro" })], { lanes: true });
    try {
      const lane = await screen.findByRole("region", { name: "P0 lane" });
      const article = within(lane).getByRole("article", { name: /^CORE-1 / });
      act(() => article.focus());
      await user.keyboard("{Shift>}L{/Shift}");
      await expectDryRunFirst(writes, "CORE-1", "done");
    } finally {
      cleanup();
    }
  });

  // The issues table's parent writers - New epic over an issue in work, Move to epic of an issue
  // in work to an epic on the backlog, and Merge of an epic holding an issue in work into an epic
  // on the backlog - each ask before any write, New epic before its create. They are plan task
  // t10's, and join this list as three cases here.
});
