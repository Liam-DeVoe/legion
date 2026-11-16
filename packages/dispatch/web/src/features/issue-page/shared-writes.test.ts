import { expect, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";

import { settle } from "../../__tests__/fake-clock";
import { answerIssuePatches } from "../../__tests__/send-answers";
import type { Issue, IssueDetails, UserIssueState } from "../../api/types";
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

// The source page held a status picked behind another write in one component's state, so Details
// went on showing the old status while the header showed the new one. The page holds it once for
// every caller, and the caller that sends it takes it first, so no other caller sends it again.
test("a status picked behind a title write is the status both callers read, and it is sent once", async () => {
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
  });
  client.setQueryData(["whoami"], { event_head: "0", kind: "user", login: "alice" });
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  const titleSave = Promise.withResolvers<Issue>();
  // The title write is held open, and the first status write is answered at once with the status
  // it set. Any later status write is left unanswered: a page that sent the held pick twice would
  // go on sending it each time its writes settled, faster than the test's timers run, so the
  // count below would never be read; held out, that page stops at its second.
  let statusAnswered = false;
  const patchIssue = answerIssuePatches().mockImplementation(async (_key, input) => {
    if (typeof input.title === "string") return titleSave.promise;
    if (statusAnswered) return Promise.withResolvers<Issue>().promise;
    statusAnswered = true;
    return narrowIssue({ ...issue, status: input.status ?? issue.status });
  });
  const statusWrites = () =>
    patchIssue.mock.calls.filter(([, input]) => input.status === "in_progress").length;
  const hook = renderHook(useBothCallers, { wrapper });

  try {
    act(() => {
      hook.result.current.first.drafts.writeTitle("New title");
      hook.result.current.first.drafts.requestTitleSubmit();
    });
    await waitFor(() => expect(patchIssue).toHaveBeenCalledTimes(1));

    act(() => hook.result.current.first.pickStatus("in_progress"));
    const { first, second } = hook.result.current;
    expect(first.pendingStatus).toBe("in_progress");
    expect(second.pendingStatus).toBe("in_progress");
    expect(first.statusSaving).toBe(true);
    expect(second.statusSaving).toBe(true);
    expect(statusWrites()).toBe(0);

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
