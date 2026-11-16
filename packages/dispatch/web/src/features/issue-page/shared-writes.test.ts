import { expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type RenderResult, renderHook, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createElement, type ReactNode } from "react";
import { MemoryRouter } from "react-router-dom";

import { settle } from "../../__tests__/fake-clock";
import { renderWithQueries } from "../../__tests__/query-provider";
import { answerIssuePatches } from "../../__tests__/send-answers";
import { ApiError, api } from "../../api/client";
import { landIssueWrite } from "../../api/issue-cache";
import type {
  ArchitectureSource,
  Artifact,
  ArtifactApproval,
  Issue,
  IssueDetails,
  UserIssueState,
} from "../../api/types";
import {
  pageStatusDoneText,
  pageStatusIdleText,
  pageStatusProgressText,
} from "../../theme/classes";
import { ApprovalSteps } from "../issue/ApprovalSteps";
import { AssigneeControl, assigneeControlName } from "../issue/AssigneeControl";
import type { StepEntry } from "../issue/gated-steps";
import { componentsControlName, IssueComponentsLine } from "../issue/IssueComponentsLine";
import { RepoControl, repoControlName } from "../issue/RepoControl";
import { addPriorityControlName, IssuePriorityLine } from "../priorities/IssuePriorityLine";
import { issueStatuses } from "../project/board-model";
import { writesOf } from "../writes/write-store";
import { statusGlyph, statusTone } from "./issue-status";
import { issueUpdateLabel, statusPickOnly, useIssueHeaderWrites } from "./useIssueHeaderWrites";

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

const state: UserIssueState = { dismissed: [], last_read_seq: 0, pinned: false, seq: 0 };

/** The server answers a PATCH with the narrow `Issue`: no artifacts, children or open asks. */
function narrowIssue(details: IssueDetails): Issue {
  const { artifacts: _artifacts, children: _children, open_asks: _openAsks, ...rest } = details;
  return rest;
}

/** The header and the Details panel of one issue page: two callers of the hook on one client. */
function useBothCallers() {
  const first = useIssueHeaderWrites(issue, state);
  const second = useIssueHeaderWrites(issue, state);
  return { first, second };
}

/** Both callers on a client of their own, signed in as alice, with every PATCH of the issue
 *  answered here: a title write is held open until the test answers `titleSave`, and a status
 *  write is answered at once with the status it set. `submitTitle` has the first caller save a
 *  new title and waits for its PATCH to go. */
function renderBothCallers() {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
  });
  client.setQueryData(["whoami"], { event_head: "0", kind: "user", login: "alice" });
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  const titleSave = Promise.withResolvers<Issue>();
  const patchIssue = answerIssuePatches().mockImplementation(async (_key, input) =>
    typeof input.title === "string"
      ? titleSave.promise
      : narrowIssue({ ...issue, status: input.status ?? issue.status })
  );
  const statusWrites = () =>
    patchIssue.mock.calls.filter(([, input]) => input.status === "in_progress").length;
  const hook = renderHook(useBothCallers, { wrapper });
  const submitTitle = async () => {
    act(() => {
      hook.result.current.first.drafts.writeTitle("New title");
      hook.result.current.first.drafts.requestTitleSubmit();
    });
    await waitFor(() => expect(patchIssue).toHaveBeenCalledTimes(1));
  };
  return { client, hook, patchIssue, statusWrites, submitTitle, titleSave };
}

// The source page held a status picked behind another write in one component's state, so Details
// went on showing the old status while the header showed the new one. The pick is a write of its
// own from the moment it is made, so every caller reads it as pending; it waits in the issue's
// line behind the title write, and goes once, after it.
test("a status picked behind a title write is the status both callers read, and it is sent once", async () => {
  const { hook, patchIssue, statusWrites, submitTitle, titleSave } = renderBothCallers();

  try {
    await submitTitle();

    act(() => hook.result.current.first.pickStatus("in_progress"));
    const { first, second } = hook.result.current;
    expect(first.pendingStatus).toBe("in_progress");
    expect(second.pendingStatus).toBe("in_progress");
    expect(first.statusSaving).toBe(true);
    expect(second.statusSaving).toBe(true);
    // Every promise the pick chained has run, and it still waits in the line: no status PATCH
    // has gone while the title write is out.
    await act(settle);
    expect(statusWrites()).toBe(0);
    expect(hook.result.current.second.pendingStatus).toBe("in_progress");

    await act(async () => {
      titleSave.resolve(narrowIssue({ ...issue, title: "New title" }));
    });
    await waitFor(() => expect(statusWrites()).toBe(1));
    // A second send would follow the first as soon as its promises ran: let them all run.
    for (let turn = 0; turn < 3; turn += 1) await act(settle);
    expect(statusWrites()).toBe(1);
  } finally {
    hook.unmount();
    patchIssue.mockRestore();
  }
});

// A title Dispatch refuses (too long) stays refused, with its reason, after a status picked while
// it was out has gone: the pick is not `updateIssue`'s write, so it does not replace the refused
// one `titleRefusal` reads, and its landing drains no draft, so the refused title is not sent
// again. The refusal no longer holds the line, so the pick goes then.
test("a status picked behind a refused title write leaves the refusal shown and goes once the line is free", async () => {
  const { hook, patchIssue, statusWrites, submitTitle, titleSave } = renderBothCallers();

  try {
    await submitTitle();
    act(() => hook.result.current.first.pickStatus("in_progress"));
    await act(settle);
    expect(statusWrites()).toBe(0);

    await act(async () => {
      titleSave.reject(new ApiError(422, { code: "CAP_EXCEEDED", error: "Title too long" }));
    });
    await waitFor(() => expect(statusWrites()).toBe(1));
    await waitFor(() => expect(hook.result.current.first.statusSaving).toBe(false));
    for (let turn = 0; turn < 3; turn += 1) await act(settle);
    expect(hook.result.current.first.titleRefusal).toBe("Title too long");
    expect(patchIssue.mock.calls.filter(([, input]) => input.title !== undefined)).toHaveLength(1);
    expect(statusWrites()).toBe(1);
  } finally {
    hook.unmount();
    patchIssue.mockRestore();
  }
});

// The reader may leave the page while the title write is still out. The pick is one write of the
// page's store, which lives on the query client and not on the page, so with no caller drawn it
// still waits its turn, and goes once the title write is answered.
test("a status pick is kept in the write store when the page that made it goes", async () => {
  const { client, hook, patchIssue, statusWrites, submitTitle, titleSave } = renderBothCallers();

  try {
    await submitTitle();
    act(() => hook.result.current.first.pickStatus("in_progress"));
    await act(settle);
    expect(
      writesOf(client)
        .rows()
        .map((write) => write.status)
    ).toEqual(["sending", "queued"]);
    hook.unmount();

    await act(async () => {
      titleSave.resolve(narrowIssue({ ...issue, title: "New title" }));
    });
    await waitFor(() => expect(statusWrites()).toBe(1));
    for (let turn = 0; turn < 3; turn += 1) await act(settle);
    expect(statusWrites()).toBe(1);
    expect(writesOf(client).rows()).toEqual([]);
  } finally {
    hook.unmount();
    patchIssue.mockRestore();
  }
});

// R8 (dispatch://INFRA-1196 R21): the shell's indicator names a header write by the page's own
// name for each field it carries, never the schema's, and a status by the board's word for it.
test("issueUpdateLabel names each field by the page's word", () => {
  expect(issueUpdateLabel("CORE-1", { parent: "CORE-9" })).toBe("Parent of CORE-1 saved");
  expect(issueUpdateLabel("CORE-1", { status: "in_progress" })).toBe("CORE-1 set to In progress");
  expect(issueUpdateLabel("CORE-1", { status: "todo" })).toBe("CORE-1 set to Todo");
  expect(issueUpdateLabel("CORE-1", { parent: "CORE-9", title: "New title" })).toBe(
    "Parent and Title of CORE-1 saved"
  );
  expect(issueUpdateLabel("CORE-1", {})).toBe("CORE-1 saved");
});

// R8: the prototype's `.st-*` puts every status in one of three groups, each with its glyph and
// its colour: done, the work that moves, and the rest, a status the board does not know among
// them.
test("statusGlyph and statusTone give the prototype's three groups", () => {
  expect([statusGlyph("done"), statusTone("done")]).toEqual(["✓", pageStatusDoneText]);
  const moving = ["in_progress", "testing", "needs_review"];
  for (const status of moving) {
    expect([status, statusGlyph(status), statusTone(status)]).toEqual([
      status,
      "◐",
      pageStatusProgressText,
    ]);
  }
  const others = [
    ...issueStatuses.filter((status) => status !== "done" && !moving.includes(status)),
    "blocked",
  ];
  expect(others).toEqual(expect.arrayContaining(["triage", "icebox", "backlog", "todo", "retro"]));
  for (const status of others) {
    expect([status, statusGlyph(status), statusTone(status)]).toEqual([
      status,
      "○",
      pageStatusIdleText,
    ]);
  }
});

/** The project's one architecture source, so the components line draws its picker
 *  (`IssueComponentsLine.test.tsx`'s `source`). */
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

// R8: the new issue page's add menu reaches four of `main`'s controls by name, so each name is
// `main`'s, and each of `main`'s controls answers to it.
test("the four control names are main's", async () => {
  expect(assigneeControlName("CORE-1")).toBe("Assignee of CORE-1");
  expect(repoControlName("CORE-1")).toBe("Repository of CORE-1");
  expect(componentsControlName("CORE-1")).toBe("Edit components of CORE-1");
  expect(addPriorityControlName("CORE-1")).toBe("Add a priority to CORE-1");

  const sources = spyOn(api, "listProjectArchitectureSources").mockResolvedValue([
    architectureSource,
  ]);
  const view = renderWithQueries(
    createElement(
      MemoryRouter,
      null,
      createElement(AssigneeControl, { assignee: null, issueKey: "CORE-1" }),
      createElement(RepoControl, {
        issueKey: "CORE-1",
        project: "CORE",
        repo: "acme/api",
        resolvedRepo: "acme/api",
      }),
      createElement(IssueComponentsLine, {
        issue: {
          components: { ids: [], inherited_from: null, mode: "inherit", reason: null, unknown: [] },
          key: "CORE-1",
          project: "CORE",
          status: "todo",
          title: "Ship",
        },
      }),
      createElement(IssuePriorityLine, {
        disabled: false,
        issue: { advances: [], advances_revision: 1, key: "CORE-1" },
      })
    )
  );
  try {
    const scope = within(view.container);
    expect(scope.getByRole("combobox", { name: "Assignee of CORE-1" })).toBeInTheDocument();
    expect(scope.getByRole("combobox", { name: "Repository of CORE-1" })).toBeInTheDocument();
    expect(
      await scope.findByRole("button", { name: "Edit components of CORE-1" })
    ).toBeInTheDocument();
    expect(scope.getByRole("button", { name: "Add a priority to CORE-1" })).toBeInTheDocument();
  } finally {
    view.unmount();
    sources.mockRestore();
  }
});

// R8: the header and Details are two callers of the hook on one page, and each reads the status
// the other is saving, from the first moment of its write to its answer.
test("two callers of useIssueHeaderWrites read the status either one is saving", async () => {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
  });
  client.setQueryData(["whoami"], { event_head: "0", kind: "user", login: "alice" });
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  const statusSave = Promise.withResolvers<Issue>();
  const patchIssue = answerIssuePatches().mockImplementation(async () => statusSave.promise);
  const hook = renderHook(useBothCallers, { wrapper });
  try {
    expect(hook.result.current.second.pendingStatus).toBeUndefined();
    expect(hook.result.current.second.statusSaving).toBe(false);

    act(() => hook.result.current.first.pickStatus("in_progress"));
    // The first caller's write has gone, and its answer is held.
    await waitFor(() => expect(patchIssue).toHaveBeenCalledTimes(1));
    expect(patchIssue.mock.calls[0]?.[1]).toEqual({ status: "in_progress" });
    await act(settle);
    const { first, second } = hook.result.current;
    expect(second.pendingStatus).toBe("in_progress");
    expect(second.statusSaving).toBe(true);
    expect(first.pendingStatus).toBe("in_progress");
    expect(first.statusSaving).toBe(true);

    await act(async () => {
      statusSave.resolve(narrowIssue({ ...issue, status: "in_progress" }));
    });
    await waitFor(() => expect(hook.result.current.second.statusSaving).toBe(false));
    expect(hook.result.current.first.statusSaving).toBe(false);
    expect(hook.result.current.first.pendingStatus).toBeUndefined();
    expect(hook.result.current.second.pendingStatus).toBeUndefined();
  } finally {
    hook.unmount();
    patchIssue.mockRestore();
  }
});

// R8: a write of the issue's fields lands one way whichever row made it: its answer merged over
// the cached details, or the issue read again when Dispatch took the write with no answer, and
// the issue lists refreshed either way.
test("landIssueWrite merges an answer and reads the issue again when none came", () => {
  const land = (next: Issue | null) => {
    const client = new QueryClient();
    client.setQueryData(["issue", issue.key], issue);
    client.setQueryData(["issues", "pinned"], []);
    landIssueWrite(client, issue.key, next);
    const landed = {
      details: client.getQueryData<IssueDetails>(["issue", issue.key]),
      issueReadAgain: client.getQueryState(["issue", issue.key])?.isInvalidated,
      listsReadAgain: client.getQueryState(["issues", "pinned"])?.isInvalidated,
    };
    client.clear();
    return landed;
  };

  const answered = land(narrowIssue({ ...issue, title: "New title" }));
  expect(answered.details?.title).toBe("New title");
  // The answer is the narrow `Issue`: what the details alone hold stays as it was.
  expect(answered.details?.artifacts).toEqual(issue.artifacts);
  expect(answered.issueReadAgain).toBe(false);
  expect(answered.listsReadAgain).toBe(true);

  const unanswered = land(null);
  expect(unanswered.details?.title).toBe(issue.title);
  expect(unanswered.issueReadAgain).toBe(true);
  expect(unanswered.listsReadAgain).toBe(true);
});

// R8: only the Status pill's pick alone is a control's write, shown in the shell's indicator and
// rolled back on a refusal; a write that carries any other field is the header's.
test("statusPickOnly is true for a status alone", () => {
  expect(statusPickOnly({ status: "done" })).toBe(true);
  expect(statusPickOnly({ status: "done", title: "New title" })).toBe(false);
  expect(statusPickOnly({ title: "New title" })).toBe(false);
  expect(statusPickOnly({})).toBe(false);
  expect(statusPickOnly(undefined)).toBe(false);
});

const SPEC_ID = "artifact-spec";
const VERIFICATION_ID = "artifact-verification";

/** A document of the issue (`ApprovalSteps.test.tsx`'s `doc`). */
function stepDocument(id: string, name: string, approval: ArtifactApproval): Artifact {
  return {
    approval,
    created_at: "2026-09-09T00:00:00Z",
    created_by: { id: "alice", kind: "user" },
    id,
    issue_key: "CORE-1",
    kind: "doc",
    name,
    primary: id === SPEC_ID,
    project: "CORE",
    ref_key: `CORE-1/${name}`,
    session_id: null,
    slug: name.replace(".", "-"),
    versions: [],
  };
}

// dispatch://INFRA-3935 R10: given `actionsOn`, each entry whose document's approval request
// waits on the reader carries its own Approve and Request changes, which review that entry's
// document, and no other entry carries any; not given, the primary document's entry alone
// carries them, as on `main`.
test("ApprovalSteps draws Approve and Request changes on each entry of actionsOn and on no other", async () => {
  const entries: StepEntry[] = [
    {
      document: stepDocument(SPEC_ID, "spec.md", {
        by: { id: "bob", kind: "user" },
        latest_version: 1,
        reason: "The second requirement names no owner.",
        state: "changes_requested",
        version: 1,
      }),
      label: "Spec",
      step: "spec",
    },
    {
      document: stepDocument(VERIFICATION_ID, "verification.md", {
        latest_version: 1,
        state: "awaiting",
        waiting_on: "human",
      }),
      label: "Verification",
      step: "criteria",
    },
  ];
  const createArtifactReview = spyOn(api, "createArtifactReview").mockResolvedValue({
    actor: { id: "alice", kind: "user" },
    artifact_id: VERIFICATION_ID,
    ask_id: null,
    content_sha256: null,
    content_sha256_source: null,
    created_at: "2026-09-11T00:00:00Z",
    id: "review-1",
    kind: "person",
    reason: null,
    state: "approved",
    version: 1,
  });
  const user = userEvent.setup();
  const drawSteps = (actionsOn: readonly string[] | undefined) =>
    renderWithQueries(
      createElement(
        MemoryRouter,
        null,
        createElement(ApprovalSteps, { actionsOn, entries, primaryArtifactId: SPEC_ID })
      )
    );
  const entry = (view: RenderResult, step: string) => {
    const item = view.container.querySelector<HTMLElement>(`[data-approval-step="${step}"]`);
    if (item === null) throw new Error(`no entry for ${step}`);
    return item;
  };
  const buttonsOf = (view: RenderResult, step: string) =>
    within(entry(view, step))
      .queryAllByRole("button")
      .map((button) => button.textContent);

  const waitingOnReader = drawSteps([VERIFICATION_ID]);
  try {
    expect(buttonsOf(waitingOnReader, "criteria")).toEqual([
      "Awaiting approval",
      "Approve",
      "Request changes",
    ]);
    expect(buttonsOf(waitingOnReader, "spec")).toEqual(["Changes requested"]);

    await user.click(
      within(entry(waitingOnReader, "criteria")).getByRole("button", { name: "Approve" })
    );
    await waitFor(() => expect(createArtifactReview).toHaveBeenCalledTimes(1));
    expect(createArtifactReview.mock.calls[0]?.[0]).toBe(VERIFICATION_ID);
    expect(createArtifactReview.mock.calls[0]?.[1]).toEqual({ state: "approved" });
  } finally {
    waitingOnReader.unmount();
  }

  const primaryAlone = drawSteps(undefined);
  try {
    expect(buttonsOf(primaryAlone, "spec")).toEqual([
      "Changes requested",
      "Approve",
      "Request changes",
    ]);
    expect(buttonsOf(primaryAlone, "criteria")).toEqual(["Awaiting approval"]);
  } finally {
    primaryAlone.unmount();
    createArtifactReview.mockRestore();
  }
});
