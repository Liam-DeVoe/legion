import { beforeEach, expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http } from "msw";
import type { ReactNode } from "react";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";

import { keyedAttempt } from "../../__tests__/keyed-attempt";
import { answerRequests } from "../../__tests__/network";
import { artifactRecordHandler } from "../../__tests__/proof-document";
import { answerIssuePatches } from "../../__tests__/send-answers";
import { api } from "../../api/client";
import { issueQuery } from "../../api/queries";
import type { Ask, IssueCoverage, IssueDetails, Project, UserIssueState } from "../../api/types";
import { buildDispatchReference, parseIssuePath } from "../refs/routes";
import { KeymapProvider } from "../shell/KeymapProvider";
import { HeaderSlot } from "./slots/header";
import type { TreeAnswer, TreeAnswerNode } from "./tree-read/tree-read";

/**
 * The header's cases of `features/issue/IssuePage.test.tsx` at INFRA-1196's checkpoint, each
 * mounting the header's slot alone (`slots/header.tsx`), as the new page's root mounts it: from
 * the issue's own read, with the address parsed.
 */

const issue = {
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
  verification_artifact_id: "",
  // `satisfies`, not an annotation: the tests below read `issue.artifacts[0]` and spread the
  // list, which the route table's `Artifact[] | null` would not let them.
} satisfies IssueDetails;

const openIssueAsk: Ask = {
  anchor: null,
  answer: null,
  artifact_id: null,
  asked_of: null,
  asked_of_at: null,
  asked_of_by: null,
  author: { id: "session-1", kind: "session" },
  block_id: null,
  created_at: "2026-09-10T00:00:00Z",
  edited_at: null,
  id: "ask-open",
  issue_key: "CORE-1",
  kind: "question",
  multiple: false,
  opened_event_id: 1,
  options: [],
  question: "Should this ship?",
  state: "open",
  waiting_on: "human",
  // The person the server resolved the question to: the reader these tests sign in as, so the
  // header's count of what waits on the reader counts it (`waiting.ts`).
  waiting_on_person: "alice",
  urgency: "med",
};

/** An open approval request on the issue's spec whose turn is a person's: it waits on the
 *  reader, so the header's approval control carries Approve and Request changes (R31). */
function approvalAsk(): NonNullable<IssueDetails["open_asks"]>[number] {
  return {
    ...openIssueAsk,
    artifact_id: issue.artifacts[0]?.id ?? null,
    id: "ask-approval",
    kind: "approval",
    last_reply: null,
    question: "Approve the spec?",
  };
}

/** The issue the file's stub is answering for, so the document's own record is the one the test
 *  stubbed. */
let stubbedIssue: IssueDetails = issue;

/** Answers the header's reads through `api`: the issue, the reader's state of it, its comments
 *  (which the count reads a Change from) and the projects, whose gates only a root issue reads.
 *  A list without this issue's project gates nothing, which is the one chip these tests assert. */
function stubHeader(
  nextIssue: IssueDetails,
  state: UserIssueState = { dismissed: [], last_read_seq: 0, pinned: false, seq: 0 }
): () => void {
  const originalGetIssue = api.getIssue;
  const originalGetMyState = api.getMyState;
  const originalListComments = api.listComments;
  const originalListProjects = api.listProjects;
  stubbedIssue = nextIssue;
  api.getIssue = async () => nextIssue;
  api.getMyState = async () => ({ "CORE-1": state });
  api.listComments = async () => [];
  api.listProjects = async () => [];
  return () => {
    api.getIssue = originalGetIssue;
    api.getMyState = originalGetMyState;
    api.listComments = originalListComments;
    api.listProjects = originalListProjects;
    stubbedIssue = issue;
  };
}

/** The header's other reads, each answered with the API's empty shape: the coverage, whose
 *  statements the count's decisions are read against, and the document's record. */
function answerHeaderReads(): void {
  answerRequests(
    http.get("/api/v1/issues/:key/coverage", ({ params }) =>
      Response.json({
        gaps: [],
        has_plan: false,
        issue: String(params.key),
        requirements: [],
        spec_artifact_id: "",
      } satisfies IssueCoverage)
    ),
    // The tree read, whose decision nodes the count reads a Change under (`waiting.ts`): an
    // issue with no node, as the coverage above holds no statement.
    http.get("/api/v1/issues/:key/tree", () =>
      Response.json({
        converted: false,
        cursor: null,
        nodes: [],
        tree: null,
        tuples: [],
        view_of: null,
      })
    ),
    artifactRecordHandler((id) => (stubbedIssue.artifacts ?? []).find((each) => each.id === id))
  );
}

beforeEach(answerHeaderReads);

/** The header's slot as the new page's root mounts it: from the issue's own read, once it has
 *  loaded, with the address parsed. */
function HeaderAlone(): ReactNode {
  const { pathname, search } = useLocation();
  const route = parseIssuePath(pathname, search);
  const read = useQuery(issueQuery(route?.key));
  if (route === undefined || read.data === undefined) return null;
  return <HeaderSlot issue={read.data} route={route} />;
}

/** The client each case renders with: a failed read is not retried, and an answer never goes
 *  stale. */
function headerClient(): QueryClient {
  return new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
}

function renderHeader(path = "/issues/CORE-1", queryClient = headerClient()) {
  queryClient.setQueryData(["whoami"], { kind: "user", login: "alice" });
  return render(
    <MemoryRouter initialEntries={[path]}>
      <QueryClientProvider client={queryClient}>
        <KeymapProvider>
          <Routes>
            <Route path="/issues/:key/*" element={<HeaderAlone />} />
          </Routes>
        </KeymapProvider>
      </QueryClientProvider>
    </MemoryRouter>
  );
}

/** Opens the header's one actions control, which is where the pin, close, reopen and the control
 *  that sets the parent live (R13: row one holds the actions, never a property). */
async function openIssueActions(): Promise<void> {
  const user = userEvent.setup();
  const control = await screen.findByRole("button", { name: "Issue actions" });
  // The control is a toggle and a press inside it closes the group, so this opens it only while
  // it is closed: two calls in a row leave the actions on screen, not hidden again.
  if (control.getAttribute("aria-expanded") !== "true") {
    await user.click(control);
  }
}

test("the header updates whose turn when a human clarification is latest", async () => {
  const waitingIssue = {
    ...issue,
    open_asks: [{ ...openIssueAsk, last_reply: null, waiting_on: "human" as const }],
  };
  const restore = stubHeader(waitingIssue);
  const queryClient = headerClient();
  const view = renderHeader("/issues/CORE-1", queryClient);

  try {
    // The marker's words are the page's and the count beside them is the issue's, each marked
    // as what it is (dispatch://INFRA-1196 R21), so the marker is read whole.
    expect((await screen.findByTestId("issue-whose-turn")).textContent).toBe("Waiting on you (1)");
    queryClient.setQueryData<IssueDetails>(["issue", "CORE-1"], {
      ...waitingIssue,
      open_asks: [
        {
          ...openIssueAsk,
          last_reply: {
            author: { id: "alice", kind: "user" },
            created_at: "2026-09-11T01:00:00Z",
          },
          waiting_on: "agent",
        },
      ],
    });
    await waitFor(() =>
      expect(screen.getByTestId("issue-whose-turn").textContent).toBe("Waiting on agents (1)")
    );
  } finally {
    view.unmount();
    restore();
  }
});

/** One node of the tree read's answer, under the issue, with what the case gives it. */
function treeNode(id: string, supertag: string, extra: Partial<TreeAnswerNode>): TreeAnswerNode {
  return {
    attributes: {},
    author: null,
    id,
    made_at: "2026-09-10T00:00:00Z",
    parent: issue.key,
    rank: null,
    state: "live",
    supertag,
    text: `Words of ${id}.`,
    visibility: "project",
    ...extra,
  };
}

// R13: the count of what waits on the reader counts each decision of the review list, which the
// header reads off the tree read's decision nodes: an agent's decision that waits for review,
// tied to a statement the coverage read holds, counts beside the open question whose turn is the
// reader's.
test("the header's count counts a waiting decision node tied to a statement", async () => {
  const statement = buildDispatchReference({ block: { id: "r1" }, key: issue.key, kind: "spec" });
  const spec = { artifact_id: "artifact-1", slug: "spec" };
  answerRequests(
    http.get("/api/v1/issues/:key/coverage", ({ params }) =>
      Response.json({
        gaps: [],
        has_plan: false,
        issue: String(params.key),
        requirements: [
          { block_id: "r1", checks: 1, gaps: [], plan_tasks: 0, ref: statement, text: "R1." },
        ],
        spec_artifact_id: "artifact-1",
      } satisfies IssueCoverage)
    ),
    http.get("/api/v1/issues/:key/tree", () =>
      Response.json({
        converted: false,
        cursor: null,
        nodes: [
          treeNode("r1", "requirement", { document: { ...spec, position: 0 } }),
          treeNode("d1", "decision", {
            asks: [],
            changed_at: "2026-09-10T00:00:00Z",
            changed_version: 1,
            document: { ...spec, position: 1 },
            kept: null,
            kind: "agent",
            links: [
              {
                created_at: "2026-09-10T00:00:00Z",
                created_version: 1,
                from_block: "d1",
                stale: false,
                to: {
                  changed_at: null,
                  changed_by: null,
                  id: "r1",
                  kind: "block",
                  made_at: null,
                  made_by: null,
                  parent: null,
                  parent_version: null,
                  ref: statement,
                },
              },
            ],
            replaced: false,
            text_sha256: "sum-d1",
            unsure: false,
            waiting_review: true,
          }),
        ],
        tree: null,
        tuples: [{ from: "d1", kind: "shaped", to: "r1" }],
        view_of: null,
      } satisfies TreeAnswer)
    )
  );
  const restore = stubHeader({ ...issue, open_asks: [{ ...openIssueAsk, last_reply: null }] });
  const view = renderHeader();

  try {
    await waitFor(() =>
      expect(screen.getByTestId("issue-whose-turn").textContent).toBe("Waiting on you (2)")
    );
  } finally {
    view.unmount();
    restore();
  }
});

test("the header hides whose turn when the server sent open asks without waiting_on", async () => {
  const restore = stubHeader({
    ...issue,
    open_asks: [{ ...openIssueAsk, last_reply: null, waiting_on: undefined }],
  });
  const view = renderHeader("/issues/CORE-1");

  try {
    await screen.findByRole("heading", { level: 1, name: issue.title });
    expect(screen.queryByText(/^Waiting on (you|agents)/)).not.toBeInTheDocument();
  } finally {
    view.unmount();
    restore();
  }
});

test("the header keeps state actions available while approval awaits", async () => {
  // The request waits on the reader, so its chip carries Approve in the header (R31).
  const restore = stubHeader({
    ...issue,
    artifacts: [
      {
        ...issue.artifacts[0],
        approval: { latest_version: 1, state: "awaiting" },
      },
    ],
    open_asks: [approvalAsk()],
  });
  const view = renderHeader("/issues/CORE-1/conversation");

  try {
    await screen.findByRole("heading", { level: 1, name: issue.title });
    // The pin is one of the overflow menu's actions (dispatch://INFRA-1196 R13).
    await openIssueActions();
    const pin = screen.getByRole("button", { name: "Pin issue" });
    expect(pin).toHaveAttribute("aria-pressed", "false");

    const stateActions = screen.getByTestId("issue-state-actions");
    expect(within(stateActions).getByRole("combobox", { name: "Status" })).not.toBeNull();
    expect(
      within(stateActions).getByRole("combobox", { name: "Priority of CORE-1" })
    ).not.toBeNull();
    expect(within(stateActions).getByRole("button", { name: "Awaiting approval" })).not.toBeNull();
    expect(within(stateActions).getByRole("button", { name: "Approve" })).not.toBeNull();
    // Close is an action, not a property, so it sits behind row one's one actions control.
    await openIssueActions();
    expect(screen.getByRole("button", { name: "Close issue" })).not.toBeNull();
  } finally {
    view.unmount();
    restore();
  }
});

test("the header draws a gated root issue's steps where the header's one chip sat", async () => {
  const restore = stubHeader({
    ...issue,
    artifacts: [
      {
        ...issue.artifacts[0],
        approval: { latest_version: 1, state: "awaiting" },
      },
    ],
    open_asks: [approvalAsk()],
  });
  const gated: Project = {
    created_at: "2026-09-09T00:00:00Z",
    gated_steps: ["decisions", "criteria", "spec"],
    open_asks: 0,
    key: "CORE",
    name: "Core",
  };
  const listProjects = spyOn(api, "listProjects").mockResolvedValue([gated]);
  const view = renderHeader("/issues/CORE-1/conversation");

  try {
    await screen.findByRole("heading", { level: 1, name: issue.title });
    const stateActions = screen.getByTestId("issue-state-actions");
    const steps = await within(stateActions).findByTestId("issue-approval-steps");
    // One line per gated step, in the order the deployment published them.
    expect(
      Array.from(steps.querySelectorAll("[data-approval-step]")).map((step) =>
        step.getAttribute("data-approval-step")
      )
    ).toEqual(["decisions", "criteria", "spec"]);
    // The primary document keeps today's chip and its review actions, inside its own step.
    expect(within(steps).getByRole("button", { name: "Awaiting approval" })).not.toBeNull();
    expect(within(steps).getByRole("button", { name: "Approve" })).not.toBeNull();
    // The two steps nobody has written say so, rather than borrowing the spec's state.
    expect(within(steps).getAllByText("Not requested").length).toBe(2);
    // The steps take the chip's place when one of them holds the primary document: the row
    // draws one approval area, not both, so it carries one pair of controls.
    expect(
      within(stateActions).queryAllByRole("button", { name: "Awaiting approval" }).length
    ).toBe(1);
    expect(within(stateActions).queryAllByRole("button", { name: "Approve" }).length).toBe(1);
  } finally {
    view.unmount();
    listProjects.mockRestore();
    restore();
  }
});

test("the header keeps the primary document's chip when no gated step reviews it", async () => {
  const restore = stubHeader({
    ...issue,
    artifacts: [
      {
        ...issue.artifacts[0],
        approval: { latest_version: 1, state: "awaiting" },
      },
    ],
    open_asks: [approvalAsk()],
  });
  // A deployment may gate the decisions and the Verification document and not the spec; the
  // spec still has an approval to answer, and only the header's own chip can answer it.
  const gated: Project = {
    created_at: "2026-09-09T00:00:00Z",
    gated_steps: ["decisions", "criteria"],
    open_asks: 0,
    key: "CORE",
    name: "Core",
  };
  const listProjects = spyOn(api, "listProjects").mockResolvedValue([gated]);
  const view = renderHeader("/issues/CORE-1/conversation");

  try {
    await screen.findByRole("heading", { level: 1, name: issue.title });
    const stateActions = screen.getByTestId("issue-state-actions");
    const steps = await within(stateActions).findByTestId("issue-approval-steps");
    expect(
      Array.from(steps.querySelectorAll("[data-approval-step]")).map((step) =>
        step.getAttribute("data-approval-step")
      )
    ).toEqual(["decisions", "criteria"]);
    // Neither step holds the primary document, so neither carries its state or its actions.
    expect(within(steps).getAllByText("Not requested").length).toBe(2);
    expect(within(steps).queryByRole("button", { name: "Awaiting approval" })).toBeNull();
    // The primary document's own chip stays in the state row, with both of its controls.
    expect(within(stateActions).getByRole("button", { name: "Awaiting approval" })).not.toBeNull();
    expect(within(stateActions).getByRole("button", { name: "Approve" })).not.toBeNull();
    expect(within(stateActions).getByRole("button", { name: "Request changes" })).not.toBeNull();
  } finally {
    view.unmount();
    listProjects.mockRestore();
    restore();
  }
});

test("the header marks the issue pin when it is pinned", async () => {
  const restore = stubHeader(issue, {
    dismissed: [],
    last_read_seq: 0,
    pinned: true,
    seq: 0,
  });
  const view = renderHeader("/issues/CORE-1/conversation");

  try {
    await screen.findByRole("heading", { level: 1, name: issue.title });
    await openIssueActions();
    const pin = await screen.findByRole("button", { name: "Unpin issue" });
    expect(pin).toHaveAttribute("aria-pressed", "true");
  } finally {
    view.unmount();
    restore();
  }
});

test("the header's pin refreshes the sidebar's pinned list once the pin is saved", async () => {
  const user = userEvent.setup();
  const restore = stubHeader(issue);
  const saved = { dismissed: [], last_read_seq: 0, pinned: true, seq: 1 };
  const putIssueState = spyOn(api, "putIssueState").mockResolvedValue(saved);
  const queryClient = headerClient();
  // The sidebar's read of the pinned issues, as it is cached beside the page.
  queryClient.setQueryData(["issues", "pinned"], []);
  const view = renderHeader("/issues/CORE-1/conversation", queryClient);

  try {
    await within(view.container).findByRole("heading", { level: 1, name: issue.title });
    expect(queryClient.getQueryState(["issues", "pinned"])?.isInvalidated).toBe(false);
    await openIssueActions();
    await user.click(within(view.container).getByRole("button", { name: "Pin issue" }));
    await waitFor(() =>
      expect(queryClient.getQueryState(["issues", "pinned"])?.isInvalidated).toBe(true)
    );
    // The pin is a keyed write of the page's store, so its bounds name the key it goes under.
    expect(putIssueState).toHaveBeenCalledWith(
      "CORE-1",
      { pinned: true },
      expect.objectContaining({ key: expect.any(String) })
    );
  } finally {
    view.unmount();
    putIssueState.mockRestore();
    restore();
  }
});

test("the header keeps an unrequested draft approval passive", async () => {
  const restore = stubHeader({
    ...issue,
    artifacts: [
      {
        ...issue.artifacts[0],
        approval: { latest_version: 1, state: "draft" },
      },
    ],
  });
  const view = renderHeader("/issues/CORE-1/conversation");

  try {
    await screen.findByRole("heading", { level: 1, name: issue.title });
    expect(screen.queryByRole("button", { name: "Draft" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Approve$/ })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Request changes" })).not.toBeInTheDocument();
  } finally {
    view.unmount();
    restore();
  }
});

test("the header closes an issue and reopens it into Backlog", async () => {
  const user = userEvent.setup();
  const restore = stubHeader(issue);
  const closeSave = Promise.withResolvers<IssueDetails>();
  const reopenSave = Promise.withResolvers<IssueDetails>();
  const patchIssue = answerIssuePatches().mockImplementation(async (_key, input) => {
    if (input.status === "done") return closeSave.promise;
    if (input.status === "backlog") return reopenSave.promise;
    throw new Error(`Unexpected issue update: ${JSON.stringify(input)}`);
  });
  const view = renderHeader();

  try {
    const openStatus = (await screen.findByLabelText("Status")) as HTMLSelectElement;
    expect([...openStatus.options].map((option) => option.value)).not.toContain("done");

    await openIssueActions();
    await user.click(screen.getByRole("button", { name: "Close issue" }));
    await waitFor(() =>
      expect(patchIssue).toHaveBeenCalledWith("CORE-1", { status: "done" }, keyedAttempt)
    );
    // While the write is in flight the control is disabled and says so: no second close can be
    // queued, and the words it shows are its name.
    await openIssueActions();
    expect((screen.getByRole("button", { name: "Closing…" }) as HTMLButtonElement).disabled).toBe(
      true
    );
    await act(async () => {
      closeSave.resolve({ ...issue, closed_at: "2026-09-12T10:00:00Z", status: "done" });
      await closeSave.promise;
    });

    expect(await screen.findByText("This issue is closed.")).toBeTruthy();
    const closedStatus = screen.getByLabelText("Status") as HTMLSelectElement;
    expect(closedStatus.disabled).toBe(true);
    expect(closedStatus.value).toBe("done");

    await openIssueActions();
    // The control is named by the words it shows, so a voice user who says what it reads
    // presses it (WCAG 2.5.3): while the write is out that is "Reopening…".
    await user.click(screen.getByRole("button", { name: "Reopen into Backlog" }));
    await waitFor(() =>
      expect(patchIssue).toHaveBeenLastCalledWith("CORE-1", { status: "backlog" }, keyedAttempt)
    );
    await openIssueActions();
    expect((screen.getByRole("button", { name: "Reopening…" }) as HTMLButtonElement).disabled).toBe(
      true
    );
    await act(async () => {
      reopenSave.resolve({ ...issue, status: "backlog" });
      await reopenSave.promise;
    });

    await waitFor(() =>
      expect(screen.queryByText("This issue is closed.")).not.toBeInTheDocument()
    );
    expect(((await screen.findByLabelText("Status")) as HTMLSelectElement).value).toBe("backlog");
  } finally {
    view.unmount();
    patchIssue.mockRestore();
    restore();
  }
});

test("the header lets a human move a Todo issue to In progress", async () => {
  const user = userEvent.setup();
  const restore = stubHeader(issue);
  const patchIssue = answerIssuePatches().mockResolvedValue({
    ...issue,
    status: "in_progress",
  });
  const view = renderHeader("/issues/CORE-1/conversation");

  try {
    const status = (await screen.findByLabelText("Status")) as HTMLSelectElement;
    expect(status.value).toBe("todo");
    expect(
      [...status.options].map((option) => ({ text: option.text, value: option.value }))
    ).toEqual([
      { text: "Triage", value: "triage" },
      { text: "Icebox", value: "icebox" },
      { text: "Backlog", value: "backlog" },
      { text: "Todo", value: "todo" },
      { text: "In progress", value: "in_progress" },
      { text: "Testing", value: "testing" },
      { text: "Needs review", value: "needs_review" },
      { text: "Retro", value: "retro" },
    ]);

    await user.selectOptions(status, "in_progress");
    await waitFor(() =>
      expect(patchIssue).toHaveBeenCalledWith("CORE-1", { status: "in_progress" }, keyedAttempt)
    );
  } finally {
    view.unmount();
    patchIssue.mockRestore();
    restore();
  }
});
