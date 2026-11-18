import { expect, test } from "bun:test";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
// biome-ignore lint/style/noRestrictedImports: this file keeps fireEvent for the bare mouse, pointer and touch presses a draggable parent must not see, which user-event has no form for
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { keyedAttempt } from "../../__tests__/keyed-attempt";
import { pickNamed } from "../../__tests__/list-picks";
import { choiceOf, choicesOf, pickFromMenu } from "../../__tests__/menu-picks";
import { answerIssuePatches } from "../../__tests__/send-answers";
import type { InboxRow, Issue, IssueDetails, IssueSummary } from "../../api/types";
import { issuePriority } from "./issue-priority";
import { PriorityControl } from "./PriorityControl";

const issue: Issue = {
  advances: [],
  advances_revision: 0,
  closed_at: null,
  created_at: "2026-09-09T00:00:00Z",
  created_by: { id: "alice", kind: "user" },
  external_links: [],
  key: "CORE-1",
  labels: [],
  last_seq: 1,
  number: 1,
  parent: null,
  blocked_by: [],
  assignee: null,
  claim: null,
  components: { mode: "inherit", ids: [], unknown: [], reason: null, inherited_from: null },
  plan_artifact_id: "",
  decisions_artifact_id: "",
  primary_artifact_id: "artifact-1",
  verification_artifact_id: "",
  priority: 1,
  project: "CORE",
  rank: "U",
  route: null,
  repo: null,
  resolved_repo: null,
  status: "todo",
  title: "Review the spec",
  updated_at: "2026-09-09T00:00:00Z",
};
const details: IssueDetails = {
  route_status: null,
  route_holder: null,
  route_status_reason: null,
  holder_state: null,
  last_seen: null,
  takeable_at: null,
  ...issue,
  artifacts: [],
  children: [],
  design_gates: [],
  open_asks: [],
  progress: { tasks: null, children: null },
  referenced_by_count: 0,
};
const summary: IssueSummary = {
  advances: [],
  advances_revision: 0,
  route: null,
  repo: null,
  resolved_repo: null,
  route_status: null,
  route_holder: null,
  route_status_reason: null,
  key: issue.key,
  labels: [],
  last_seq: 1,
  open_asks: 0,
  progress: { tasks: null, children: null },
  parent: null,
  blocked_by: [],
  primary_artifact_id: "artifact-1",
  assignee: null,
  claim: null,
  components: { mode: "inherit", ids: [], unknown: [], reason: null, inherited_from: null },
  priority: issue.priority,
  rank: "U",
  status: issue.status,
  title: issue.title,
  updated_at: issue.updated_at,
  closed_at: issue.closed_at,
  created_by: issue.created_by,
  created_at: issue.created_at,
};
const otherSummary: IssueSummary = { ...summary, key: "CORE-2", priority: 3 };
const inboxRow: InboxRow = {
  anchor: null,
  answer: null,
  artifact_id: null,
  asked_of: null,
  asked_of_at: null,
  asked_of_by: null,
  author: { id: "session-1", kind: "session" },
  block_id: null,
  last_reply: null,
  created_at: "2026-09-09T00:00:00Z",
  edited_at: null,
  id: "ask-1",
  issue_key: issue.key,
  kind: "question",
  multiple: false,
  opened_event_id: 1,
  options: [],
  thread: {
    answers: [],
    claimant: null,
    edits: [],
    followers: [],
    replies: [],
    route: null,
    route_status: null,
    route_holder: null,
    route_status_reason: null,
  },
  priority: issue.priority,
  snoozed_until: null,
  question: "Which layout?",
  state: "open",
  urgency: "med",
};

const detailKey = ["issue", issue.key] as const;
const listKey = ["issues", "project", issue.project] as const;
const inboxKey = ["inbox"] as const;

/** Renders the control the way every surface does: with the priority it reads from the cache. */
function CachedControl(): ReactNode {
  const cached = useQuery<IssueDetails>({
    queryKey: detailKey,
    queryFn: () => Promise.reject(new Error("the test seeds this query")),
    staleTime: Number.POSITIVE_INFINITY,
  });
  if (cached.data === undefined) {
    return null;
  }
  return (
    <PriorityControl issueKey={cached.data.key} priority={issuePriority(cached.data.priority)} />
  );
}

function renderControl() {
  const queryClient = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
  });
  queryClient.setQueryData(detailKey, details);
  queryClient.setQueryData(listKey, [summary, otherSummary]);
  queryClient.setQueryData(inboxKey, [inboxRow]);
  const user = userEvent.setup();
  const view = render(
    <QueryClientProvider client={queryClient}>
      <CachedControl />
    </QueryClientProvider>
  );
  return { queryClient, unmount: view.unmount, user };
}

function trigger(): HTMLElement {
  return screen.getByRole("button", { name: pickNamed("Priority of CORE-1") });
}

test("PriorityControl shows the issue's priority and offers Unset and P0-P3, the current one checked", async () => {
  const { unmount, user } = renderControl();
  try {
    expect(trigger()).toHaveTextContent("P1");
    expect(trigger()).toHaveAttribute("data-priority-menu");
    expect(await choicesOf(user, trigger())).toEqual(["Unset", "P0", "P1", "P2", "P3"]);
    await user.click(trigger());
    expect(choiceOf(trigger(), "P1")).toHaveAttribute("aria-checked", "true");
    expect(choiceOf(trigger(), "P1")).toHaveFocus();
    expect(choiceOf(trigger(), "Unset")).toHaveAttribute("aria-checked", "false");
    // Unset has no digit; P0 to P3 write on 0 to 3.
    expect(choiceOf(trigger(), "Unset")).not.toHaveAttribute("aria-keyshortcuts");
    expect(choiceOf(trigger(), "P3")).toHaveAttribute("aria-keyshortcuts", "3");
  } finally {
    unmount();
  }
});

test("PriorityControl saves the picked priority, shows it before the server answers, and keeps focus", async () => {
  const save = Promise.withResolvers<Issue>();
  const patchIssue = answerIssuePatches().mockImplementation(() => save.promise);
  const { queryClient, unmount, user } = renderControl();
  try {
    await pickFromMenu(user, trigger(), "P2");
    await waitFor(() =>
      expect(patchIssue).toHaveBeenLastCalledWith("CORE-1", { priority: 2 }, keyedAttempt)
    );
    expect(trigger()).toHaveTextContent("P2");
    // A disabled control drops focus and leaves the tab order for the length of the save, so
    // the trigger stays enabled while the request is in flight.
    expect(trigger()).not.toBeDisabled();
    expect(trigger()).toHaveFocus();
    expect(queryClient.getQueryData<IssueSummary[]>(listKey)).toEqual([
      { ...summary, priority: 2 },
      otherSummary,
    ]);
    expect(queryClient.getQueryData<InboxRow[]>(inboxKey)).toEqual([{ ...inboxRow, priority: 2 }]);

    await act(async () => {
      save.resolve({ ...issue, priority: 2 });
      await save.promise;
    });
    await waitFor(() =>
      expect(queryClient.getQueryData<IssueDetails>(detailKey)?.priority).toBe(2)
    );
    expect(trigger()).toHaveFocus();
  } finally {
    unmount();
    patchIssue.mockRestore();
  }
});

test("PriorityControl writes a priority on its digit, and nothing for the one it already holds", async () => {
  const patchIssue = answerIssuePatches().mockResolvedValue({ ...issue, priority: 3 });
  const { unmount, user } = renderControl();
  try {
    await user.click(trigger());
    await user.keyboard("{Enter}");
    expect(patchIssue).not.toHaveBeenCalled();
    await user.click(trigger());
    await user.keyboard("3");
    await waitFor(() =>
      expect(patchIssue).toHaveBeenLastCalledWith("CORE-1", { priority: 3 }, keyedAttempt)
    );
    expect(patchIssue).toHaveBeenCalledTimes(1);
  } finally {
    unmount();
    patchIssue.mockRestore();
  }
});

test("PriorityControl sends null for Unset and shows the muted tag", async () => {
  const patchIssue = answerIssuePatches().mockResolvedValue({ ...issue, priority: null });
  const { unmount, user } = renderControl();
  try {
    await pickFromMenu(user, trigger(), "Unset");
    await waitFor(() =>
      expect(patchIssue).toHaveBeenLastCalledWith("CORE-1", { priority: null }, keyedAttempt)
    );
    expect(trigger()).toHaveTextContent("Priority");
    expect(screen.queryByText("P1", { selector: "span" })).not.toBeInTheDocument();
  } finally {
    unmount();
    patchIssue.mockRestore();
  }
});

test("PriorityControl rolls every surface back and offers Retry when the save fails", async () => {
  const save = Promise.withResolvers<Issue>();
  const patchIssue = answerIssuePatches().mockImplementation(() => save.promise);
  const { queryClient, unmount, user } = renderControl();
  try {
    await pickFromMenu(user, trigger(), "P0");
    await waitFor(() => expect(trigger()).toHaveTextContent("P0"));

    act(() => {
      save.reject(new Error("offline"));
    });
    await screen.findByRole("alert");
    expect(trigger()).toHaveTextContent("P1");
    expect(trigger()).not.toBeDisabled();
    expect(queryClient.getQueryData<IssueSummary[]>(listKey)).toEqual([summary, otherSummary]);
    expect(queryClient.getQueryData<InboxRow[]>(inboxKey)).toEqual([inboxRow]);

    patchIssue.mockResolvedValue({ ...issue, priority: 0 });
    await user.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(patchIssue).toHaveBeenCalledTimes(2));
    expect(patchIssue).toHaveBeenLastCalledWith("CORE-1", { priority: 0 }, keyedAttempt);
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
    expect(trigger()).toHaveTextContent("P0");
  } finally {
    unmount();
    patchIssue.mockRestore();
  }
});

test("PriorityControl saves a pick made during the first save once that save settles, newest first shown", async () => {
  const first = Promise.withResolvers<Issue>();
  const second = Promise.withResolvers<Issue>();
  const patchIssue = answerIssuePatches()
    .mockImplementationOnce(() => first.promise)
    .mockImplementationOnce(() => second.promise);
  const { queryClient, unmount, user } = renderControl();
  try {
    await pickFromMenu(user, trigger(), "P0");
    await waitFor(() =>
      expect(patchIssue).toHaveBeenLastCalledWith("CORE-1", { priority: 0 }, keyedAttempt)
    );
    await pickFromMenu(user, trigger(), "P3");
    await pickFromMenu(user, trigger(), "P2");
    // One request at a time: the later picks wait for the first answer, but the badge already
    // shows the newest one and never snaps back to the value still being saved.
    expect(trigger()).toHaveTextContent("P2");
    expect(patchIssue).toHaveBeenCalledTimes(1);

    await act(async () => {
      first.resolve({ ...issue, priority: 0 });
      await first.promise;
    });
    await waitFor(() => expect(patchIssue).toHaveBeenCalledTimes(2));
    expect(patchIssue).toHaveBeenLastCalledWith("CORE-1", { priority: 2 }, keyedAttempt);
    expect(trigger()).toHaveTextContent("P2");

    await act(async () => {
      second.resolve({ ...issue, priority: 2 });
      await second.promise;
    });
    await waitFor(() =>
      expect(queryClient.getQueryData<IssueDetails>(detailKey)?.priority).toBe(2)
    );
    expect(patchIssue).toHaveBeenCalledTimes(2);
  } finally {
    unmount();
    patchIssue.mockRestore();
  }
});

test("a queued pick that fails rolls back to the priority the server last confirmed, not the pick", async () => {
  const first = Promise.withResolvers<Issue>();
  const second = Promise.withResolvers<Issue>();
  const patchIssue = answerIssuePatches()
    .mockImplementationOnce(() => first.promise)
    .mockImplementationOnce(() => second.promise);
  const { queryClient, unmount, user } = renderControl();
  try {
    await pickFromMenu(user, trigger(), "P0");
    await waitFor(() =>
      expect(patchIssue).toHaveBeenLastCalledWith("CORE-1", { priority: 0 }, keyedAttempt)
    );
    await pickFromMenu(user, trigger(), "P2");
    expect(trigger()).toHaveTextContent("P2");

    await act(async () => {
      first.resolve({ ...issue, priority: 0 });
      await first.promise;
    });
    await waitFor(() =>
      expect(patchIssue).toHaveBeenLastCalledWith("CORE-1", { priority: 2 }, keyedAttempt)
    );
    act(() => {
      second.reject(new Error("offline"));
    });
    await screen.findByRole("alert");
    expect(trigger()).toHaveTextContent("P0");
    expect(queryClient.getQueryData<IssueSummary[]>(listKey)).toEqual([
      { ...summary, priority: 0 },
      otherSummary,
    ]);
    expect(queryClient.getQueryData<InboxRow[]>(inboxKey)).toEqual([{ ...inboxRow, priority: 0 }]);
  } finally {
    unmount();
    patchIssue.mockRestore();
  }
});

test("PriorityControl keeps mouse, pointer and touch presses on its trigger out of a draggable parent", () => {
  const queryClient = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
  });
  const reached: string[] = [];
  const view = render(
    <QueryClientProvider client={queryClient}>
      <article
        aria-label="CORE-1 Review the spec"
        onMouseDown={() => reached.push("mousedown")}
        onPointerDown={() => reached.push("pointerdown")}
        onTouchStart={() => reached.push("touchstart")}
      >
        <PriorityControl issueKey="CORE-1" priority={1} />
      </article>
    </QueryClientProvider>
  );
  try {
    // Bare mouse, pointer and touch presses: user-event sends a whole press sequence and cannot
    // deliver one of these events on its own.
    fireEvent.mouseDown(trigger());
    fireEvent.pointerDown(trigger());
    fireEvent.touchStart(trigger());
    expect(reached).toEqual([]);
  } finally {
    view.unmount();
  }
});
