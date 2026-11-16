import { expect, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";

import { settle } from "../../__tests__/fake-clock";
import { answerIssuePatches } from "../../__tests__/send-answers";
import { ApiError } from "../../api/client";
import type { Issue, IssueDetails, UserIssueState } from "../../api/types";
import { writesOf } from "../writes/write-store";
import { useIssueHeaderWrites } from "./useIssueHeaderWrites";

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
