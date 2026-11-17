import { expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
// biome-ignore lint/style/noRestrictedImports: this file keeps fireEvent for the focus that arms the lazy read of who has signed in, which user-event has no form for
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import userEvent, { type UserEvent } from "@testing-library/user-event";
import type { ReactNode } from "react";
import { keyedAttempt } from "../../__tests__/keyed-attempt";
import { answerIssuePatches } from "../../__tests__/send-answers";
import { ApiError, api } from "../../api/client";
import { projectDefaultAssigneesQuery, usersQuery } from "../../api/queries";
import type { InboxRow, Issue, IssueDetails, IssueSummary } from "../../api/types";
import { AssigneeControl } from "./AssigneeControl";

const issue: Issue = {
  advances: [],
  advances_revision: 0,
  assignee: "alice",
  claim: null,
  components: { mode: "inherit", ids: [], unknown: [], reason: null, inherited_from: null },
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
  plan_artifact_id: "",
  decisions_artifact_id: "",
  primary_artifact_id: "artifact-1",
  verification_artifact_id: "",
  priority: null,
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
  assignee: issue.assignee,
  claim: null,
  components: { mode: "inherit", ids: [], unknown: [], reason: null, inherited_from: null },
  key: issue.key,
  labels: [],
  last_seq: 1,
  open_asks: 0,
  progress: { tasks: null, children: null },
  parent: null,
  blocked_by: [],
  primary_artifact_id: "artifact-1",
  priority: null,
  rank: "U",
  status: issue.status,
  title: issue.title,
  updated_at: issue.updated_at,
  closed_at: issue.closed_at,
  created_by: issue.created_by,
  created_at: issue.created_at,
};
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
  issue: { assignee: issue.assignee, key: issue.key, title: issue.title },
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
  priority: null,
  snoozed_until: null,
  question: "Which layout?",
  state: "open",
  urgency: "med",
};

const detailKey = ["issue", issue.key] as const;
const listKey = ["issues", "project", issue.project] as const;
const inboxKey = ["inbox"] as const;

function CachedControl({ held }: { held: boolean }): ReactNode {
  const cached = useQuery<IssueDetails>({
    queryKey: detailKey,
    queryFn: () => Promise.reject(new Error("the test seeds this query")),
    staleTime: Number.POSITIVE_INFINITY,
  });
  if (cached.data === undefined) {
    return null;
  }
  return <AssigneeControl assignee={cached.data.assignee} held={held} issueKey={cached.data.key} />;
}

function renderControl({ held = false }: { held?: boolean } = {}) {
  const listUsers = spyOn(api, "listUsers").mockResolvedValue([
    { login: "alice" },
    { login: "bob" },
  ]);
  const queryClient = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
  });
  queryClient.setQueryData(detailKey, details);
  queryClient.setQueryData(listKey, [summary]);
  queryClient.setQueryData(inboxKey, [inboxRow]);
  const user = userEvent.setup();
  const view = render(
    <QueryClientProvider client={queryClient}>
      <CachedControl held={held} />
    </QueryClientProvider>
  );
  return {
    user,
    listUsers,
    queryClient,
    unmount: () => {
      view.unmount();
      listUsers.mockRestore();
    },
  };
}

/** The list's trigger: the badge, named for the list and described by the person it shows. */
function trigger(): HTMLElement {
  return screen.getByRole("button", { name: "Assignee of CORE-1" });
}

/** The open list's people, in its order. The list is portalled to the body. */
function offered(): string[] {
  return screen.getAllByRole("option").map((option) => option.textContent?.replace("✓", "") ?? "");
}

/** The person the open list's text box highlights. */
function highlighted(): string | undefined {
  const id = screen
    .getByRole("combobox", { name: "Search people" })
    .getAttribute("aria-activedescendant");
  return document.getElementById(id ?? "")?.textContent?.replace("✓", "");
}

/** Opens the list with a click on the badge and picks `name` from it with a click. */
async function pick(user: UserEvent, name: string): Promise<void> {
  await user.click(trigger());
  await user.click(await screen.findByRole("option", { name }));
}

/** Resolves once who has signed in has been read. */
async function peopleRead(queryClient: QueryClient): Promise<void> {
  await waitFor(() =>
    expect(queryClient.getQueryState(usersQuery().queryKey)?.status).toBe("success")
  );
}

test("AssigneeControl reads who has signed in only once the reader reaches for it, then lists Unassigned first", async () => {
  const { listUsers, queryClient, unmount, user } = renderControl();
  try {
    // Mounted with the badge showing, nothing has been requested: a fresh issue load costs no
    // read of who has signed in.
    expect(screen.getByText("alice", { selector: "span" })).toBeTruthy();
    expect(trigger()).toHaveAccessibleDescription("alice");
    expect(listUsers).not.toHaveBeenCalled();

    // A focus with no press before it: user-event has no form for a bare focus.
    fireEvent.focus(trigger());
    await peopleRead(queryClient);
    expect(listUsers).toHaveBeenCalledTimes(1);
    await user.click(trigger());
    expect(offered()).toEqual(["Unassigned", "alice", "bob"]);
    expect(screen.getByRole("option", { name: "alice" })).toHaveAttribute("aria-selected", "true");
  } finally {
    unmount();
  }
});

test("AssigneeControl reassigns optimistically on every surface and merges the server's answer", async () => {
  const save = Promise.withResolvers<Issue>();
  const patchIssue = answerIssuePatches().mockImplementation(() => save.promise);
  const { queryClient, unmount, user } = renderControl();
  try {
    await user.hover(trigger());
    await peopleRead(queryClient);
    await pick(user, "bob");
    await waitFor(() => expect(trigger()).toHaveAccessibleDescription("bob"));
    expect(patchIssue).toHaveBeenCalledWith("CORE-1", { assignee: "bob" }, keyedAttempt);
    expect(queryClient.getQueryData<IssueSummary[]>(listKey)?.[0]?.assignee).toBe("bob");
    expect(queryClient.getQueryData<InboxRow[]>(inboxKey)?.[0]?.issue?.assignee).toBe("bob");

    act(() => {
      save.resolve({ ...issue, assignee: "bob", last_seq: 2 });
    });
    await waitFor(() =>
      expect(queryClient.getQueryData<IssueDetails>(detailKey)?.last_seq).toBe(2)
    );
  } finally {
    unmount();
    patchIssue.mockRestore();
  }
});

test("AssigneeControl sends null for Unassigned", async () => {
  const patchIssue = answerIssuePatches().mockResolvedValue({ ...issue, assignee: null });
  const { queryClient, unmount, user } = renderControl();
  try {
    // A focus with no press before it: user-event has no form for a bare focus.
    fireEvent.focus(trigger());
    await peopleRead(queryClient);
    await pick(user, "Unassigned");
    await waitFor(() =>
      expect(patchIssue).toHaveBeenCalledWith("CORE-1", { assignee: null }, keyedAttempt)
    );
    await waitFor(() => expect(trigger()).toHaveAccessibleDescription("Unassigned"));
    expect(screen.getByText("Unassigned", { selector: "span" })).toBeTruthy();
  } finally {
    unmount();
    patchIssue.mockRestore();
  }
});

// A question on an issue nobody holds reaches the project's default assignee, so the unassign's
// optimistic write must leave the issue's rows that nobody handed on waiting on that person: a
// row that named nobody would drop out of the default person's own (`assignee=me`) list until
// the PATCH settled. Where the client holds no defaults read, the row keeps the person it waited
// on rather than naming nobody.
test.each([
  ["names the project's default where the defaults read holds it", true, "carol"],
  ["keeps the person it waited on where the client has no defaults read", false, "alice"],
])("AssigneeControl's unassign leaves an inbox row nobody handed on waiting on a person: it %s", async (_name, defaultsRead, person) => {
  const save = Promise.withResolvers<Issue>();
  const patchIssue = answerIssuePatches().mockImplementation(() => save.promise);
  const { queryClient, unmount, user } = renderControl();
  const handedOn: InboxRow = {
    ...inboxRow,
    asked_of: "bob",
    asked_of_at: "2026-09-09T01:00:00Z",
    asked_of_by: "alice",
    id: "ask-2",
    waiting_on_person: "bob",
  };
  queryClient.setQueryData(inboxKey, [{ ...inboxRow, waiting_on_person: "alice" }, handedOn]);
  if (defaultsRead) {
    queryClient.setQueryData(projectDefaultAssigneesQuery().queryKey, [
      { project: "OPS", assignee: "dana" },
      { project: "CORE", assignee: "carol" },
    ]);
  }
  try {
    // A focus with no press before it: user-event has no form for a bare focus.
    fireEvent.focus(trigger());
    await peopleRead(queryClient);
    await pick(user, "Unassigned");
    await waitFor(() =>
      expect(patchIssue).toHaveBeenCalledWith("CORE-1", { assignee: null }, keyedAttempt)
    );

    // The save is still in flight: what the rows hold is the optimistic write alone.
    const rows = queryClient.getQueryData<InboxRow[]>(inboxKey) ?? [];
    expect(rows.map((row) => [row.id, row.issue?.assignee, row.waiting_on_person])).toEqual([
      ["ask-1", null, person],
      ["ask-2", null, "bob"],
    ]);

    await act(async () => {
      save.resolve({ ...issue, assignee: null });
      await save.promise;
    });
    await waitFor(() =>
      expect(queryClient.getQueryData<IssueDetails>(detailKey)?.assignee).toBeNull()
    );
  } finally {
    unmount();
    patchIssue.mockRestore();
  }
});

test("AssigneeControl rolls back and shows the server's refusal when the person has not signed in", async () => {
  const patchIssue = answerIssuePatches().mockRejectedValue(
    new ApiError(400, {
      code: "ASSIGNEE_NOT_ALLOWED",
      error: '"bob" has not signed in to Dispatch',
    })
  );
  const { queryClient, unmount, user } = renderControl();
  try {
    // A focus with no press before it: user-event has no form for a bare focus.
    fireEvent.focus(trigger());
    await peopleRead(queryClient);
    await pick(user, "bob");
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent('"bob" has not signed in to Dispatch');
    expect(trigger()).toHaveAccessibleDescription("alice");
    expect(queryClient.getQueryData<IssueSummary[]>(listKey)).toEqual([summary]);
    expect(queryClient.getQueryData<InboxRow[]>(inboxKey)).toEqual([inboxRow]);
  } finally {
    unmount();
    patchIssue.mockRestore();
  }
});

test("AssigneeControl keeps focus while a save is in flight and saves a pick made meanwhile after it", async () => {
  const first = Promise.withResolvers<Issue>();
  const second = Promise.withResolvers<Issue>();
  const patchIssue = answerIssuePatches()
    .mockImplementationOnce(() => first.promise)
    .mockImplementationOnce(() => second.promise);
  const { queryClient, unmount, user } = renderControl();
  try {
    act(() => trigger().focus());
    await peopleRead(queryClient);
    // From the keyboard: Enter opens the list on alice, ArrowDown highlights bob, Enter picks him.
    await user.keyboard("{Enter}{ArrowDown}{Enter}");
    await waitFor(() =>
      expect(patchIssue).toHaveBeenLastCalledWith("CORE-1", { assignee: "bob" }, keyedAttempt)
    );
    // A disabled control drops focus and leaves the tab order for the length of the save.
    expect(trigger()).not.toBeDisabled();
    expect(trigger()).not.toHaveAttribute("aria-disabled");
    expect(trigger()).toHaveFocus();

    // Home on the closed badge opens the list on Unassigned, and Enter picks it.
    await user.keyboard("{Home}{Enter}");
    expect(trigger()).toHaveAccessibleDescription("Unassigned");
    expect(patchIssue).toHaveBeenCalledTimes(1);
    await act(async () => {
      first.resolve({ ...issue, assignee: "bob" });
      await first.promise;
    });
    await waitFor(() => expect(patchIssue).toHaveBeenCalledTimes(2));
    expect(patchIssue).toHaveBeenLastCalledWith("CORE-1", { assignee: null }, keyedAttempt);
    expect(trigger()).toHaveAccessibleDescription("Unassigned");
    await act(async () => {
      second.resolve({ ...issue, assignee: null });
      await second.promise;
    });
    await waitFor(() =>
      expect(queryClient.getQueryData<IssueDetails>(detailKey)?.assignee).toBeNull()
    );
    expect(trigger()).toHaveFocus();
  } finally {
    unmount();
    patchIssue.mockRestore();
  }
});

// dispatch://INFRA-2160, R6: a key on the closed badge only moves the list's highlight - a
// native select would write the next person on ArrowDown - and Escape and Tab write nothing and
// leave the badge on the issue's assignee. Enter writes the person highlighted.
test("AssigneeControl's keys on its closed badge only move the list's highlight, and only Enter writes", async () => {
  const patchIssue = answerIssuePatches().mockResolvedValue({ ...issue, assignee: "bob" });
  const { queryClient, unmount, user } = renderControl();
  try {
    act(() => trigger().focus());
    await peopleRead(queryClient);
    for (const [keys, to] of [
      ["{ArrowDown}", "bob"],
      ["{ArrowUp}", "Unassigned"],
      ["{Home}", "Unassigned"],
      ["{End}", "bob"],
      ["{PageDown}", "bob"],
      ["{PageUp}", "Unassigned"],
      ["b", "bob"],
    ] as const) {
      await user.keyboard(keys);
      expect(highlighted(), `${keys} highlights ${to}`).toBe(to);
      await user.keyboard("{Escape}");
      expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
      expect(trigger()).toHaveFocus();
      expect(trigger()).toHaveAccessibleDescription("alice");
    }
    await user.keyboard("{ArrowDown}");
    await user.tab();
    expect(screen.queryByRole("listbox")).not.toBeInTheDocument();
    expect(trigger()).toHaveFocus();
    expect(trigger()).toHaveAccessibleDescription("alice");
    expect(patchIssue).not.toHaveBeenCalled();

    await user.keyboard("{ArrowDown}{Enter}");
    await waitFor(() => expect(patchIssue).toHaveBeenCalledTimes(1));
    expect(patchIssue).toHaveBeenCalledWith("CORE-1", { assignee: "bob" }, keyedAttempt);
    // A pick of the person it already names writes nothing, as a `<select>` set to its own value.
    await waitFor(() => expect(trigger()).toHaveAccessibleDescription("bob"));
    await user.keyboard("{Enter}{Enter}");
    expect(patchIssue).toHaveBeenCalledTimes(1);
  } finally {
    unmount();
    patchIssue.mockRestore();
  }
});

// `held` is the issue page's other field writes: the badge keeps the focus and its Tab stop and
// says it is busy, and a pick made then is dropped rather than queued.
test("a held AssigneeControl keeps its focus and Tab stop and drops a pick", async () => {
  const patchIssue = answerIssuePatches().mockResolvedValue({ ...issue, assignee: "bob" });
  const { queryClient, unmount, user } = renderControl({ held: true });
  try {
    act(() => trigger().focus());
    await peopleRead(queryClient);
    expect(trigger()).toHaveAttribute("aria-disabled", "true");
    expect(trigger()).not.toBeDisabled();
    await user.keyboard("{ArrowDown}{Enter}");
    expect(trigger()).toHaveFocus();
    expect(trigger()).toHaveAccessibleDescription("alice");
    expect(patchIssue).not.toHaveBeenCalled();
  } finally {
    unmount();
    patchIssue.mockRestore();
  }
});
