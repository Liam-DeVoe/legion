import { afterEach, expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createElement } from "react";
import { MemoryRouter } from "react-router-dom";

import { api } from "../api/client";
import { whoAmIQuery } from "../api/queries";
import type { CredentialPendingRow, IssueSummary, PrioritiesV2, ProjectV2 } from "../api/types";
import { issueAsk } from "../features/inbox/inbox-fixture";
import { Sidebar } from "../features/sidebar/Sidebar";

/** The summary read the rail makes (`limit=0`): the two fault counts beside no priority row, and
 *  `empty` where the deployment has written no priority at all. */
function summary(parked: number, unplaced: number, written = true): PrioritiesV2 {
  return {
    ...(written
      ? {}
      : {
          empty:
            "No priority is live now, so the list is empty. Write one, then say which work advances it.",
        }),
    items: [],
    limit: 0,
    offset: 0,
    priority_work_parked: { by_priority: {}, count: parked, issues: [] },
    total: written ? 2 : 0,
    work_under_no_priority: { by_project: {}, count: unplaced, issues: [] },
  };
}

/** The spies a test of this file makes through `renderSidebar` that its own `finally` does not
 *  put back: a spy on `api` outlives its test, so the rail's own read is let go here. */
const railSpies: { mockRestore: () => void }[] = [];

afterEach(() => {
  for (const spy of railSpies.splice(0)) spy.mockRestore();
});

function issue(overrides: Partial<IssueSummary> = {}): IssueSummary {
  return {
    advances: [],
    advances_revision: 0,
    route: null,
    repo: null,
    resolved_repo: null,
    route_status: null,
    route_holder: null,
    route_status_reason: null,
    key: "CORE-1",
    labels: [],
    last_seq: 0,
    open_asks: 1,
    progress: { tasks: null, children: null },
    parent: null,
    blocked_by: [],
    primary_artifact_id: "artifact-1",
    assignee: null,
    claim: null,
    components: { mode: "inherit", ids: [], unknown: [], reason: null, inherited_from: null },
    status: "todo",
    priority: null,
    rank: "U",
    title: "Pinned work",
    updated_at: "2026-09-10T00:00:00Z",
    closed_at: null,
    created_by: null,
    created_at: "2026-09-10T00:00:00Z",
    ...overrides,
  };
}

function project(overrides: Partial<ProjectV2> = {}): ProjectV2 {
  return {
    created_at: "2026-09-10T00:00:00Z",
    key: "CORE",
    name: "Core",
    open_asks: 3,
    gated_steps: [],
    ...overrides,
  };
}

/** `pending` is the broker's list of credential requests waiting on the viewer; without it this
 *  Dispatch has no broker (the list answers `null`). `priorities` is what the rail's summary
 *  call answers; by default this deployment has written none. */
function renderSidebar(
  pathname = "/",
  pending?: CredentialPendingRow[],
  priorities: PrioritiesV2 = summary(0, 0, false)
) {
  const getPrioritySummary = spyOn(api, "getPrioritySummary").mockResolvedValue(priorities);
  railSpies.push(getPrioritySummary);
  // Needs you counts the questions waiting on the reader, which is what the server answers in
  // `waiting_on_person`: nobody has handed ask-1 or ask-3 on, so it is their issue's assignee.
  // ask-2's issue is nobody's, so it waits on its project's default assignee, who is not alice.
  const getInbox = spyOn(api, "getInbox").mockResolvedValue([
    issueAsk({
      author: { id: "alice", kind: "user" },
      created_at: "2026-09-10T00:00:00Z",
      id: "ask-1",
      issue: { assignee: "alice", key: "CORE-1", title: "Pinned work" },
      question: "Ship?",
      waiting_on_person: "alice",
    }),
    issueAsk({
      author: { id: "alice", kind: "user" },
      created_at: "2026-09-10T00:00:00Z",
      id: "ask-2",
      issue: null,
      issue_key: "OPS-1",
      last_reply: { author: { id: "alice", kind: "user" }, created_at: "2026-09-10T01:00:00Z" },
      opened_event_id: 2,
      question: "Review?",
      waiting_on: "agent",
    }),
    issueAsk({
      author: { id: "agent-2", kind: "session" },
      created_at: "2026-09-10T00:00:00Z",
      id: "ask-3",
      issue: { assignee: "alice", key: "OPS-2", title: "Operations work" },
      issue_key: "OPS-2",
      last_reply: {
        author: { id: "agent-3", kind: "session" },
        created_at: "2026-09-10T01:00:00Z",
      },
      opened_event_id: 3,
      question: "Confirm?",
      waiting_on_person: "alice",
    }),
  ]);
  // Without a broker the credential list answers `null`; with one, the requests themselves.
  const getCredentialPending = spyOn(api, "getCredentialPending").mockResolvedValue(
    pending ?? null
  );
  const getIssue = spyOn(api, "getIssue").mockResolvedValue(undefined as never);
  const listIssues = spyOn(api, "listIssues").mockResolvedValue([issue()]);
  const listProjects = spyOn(api, "listProjects").mockResolvedValue([
    project(),
    project({ key: "OPS", name: "Operations", open_asks: 0 }),
  ]);
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Number.POSITIVE_INFINITY } },
  });
  // Needs you counts the reader's own questions, so the badge waits on `/auth/whoami`; the
  // sidebar is handed the same person below.
  queryClient.setQueryData(whoAmIQuery().queryKey, {
    event_head: "0",
    kind: "user",
    login: "alice",
  });
  const view = render(
    createElement(
      MemoryRouter,
      { initialEntries: [pathname] },
      createElement(
        QueryClientProvider,
        { client: queryClient },
        createElement(Sidebar, { user: { event_head: "0", kind: "user", login: "alice" } })
      )
    )
  );
  return {
    getCredentialPending,
    getInbox,
    getIssue,
    getPrioritySummary,
    listIssues,
    listProjects,
    view,
  };
}

test("sidebar lists Inbox, Pinned, Projects, and Settings with a truthful Inbox count", async () => {
  const sidebar = renderSidebar();

  try {
    await screen.findByRole("link", { name: /CORE.*Core/ });
    expect(screen.getByRole("link", { name: "Inbox 2" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Pinned" })).toBeTruthy();
    expect(screen.getByRole("link", { name: /CORE-1.*Pinned work/ })).toHaveTextContent("1");
    expect(screen.getByRole("heading", { name: "Projects" })).toBeTruthy();
    expect(screen.getByRole("link", { name: /CORE.*Core/ })).toHaveTextContent("3");
    expect(screen.getByRole("link", { name: /OPS.*Operations/ })).toBeTruthy();
    expect(screen.getByRole("link", { name: "Settings" })).toBeTruthy();
  } finally {
    sidebar.view.unmount();
    sidebar.getCredentialPending.mockRestore();
    sidebar.getInbox.mockRestore();
    sidebar.getIssue.mockRestore();
    sidebar.listIssues.mockRestore();
    sidebar.listProjects.mockRestore();
  }
});

test("sidebar counts a pending credential request in the Inbox count, beside the asks waiting on you", async () => {
  const sidebar = renderSidebar("/", [
    {
      identifiers: ["DEMO_API_KEY"],
      kind: "agent_secret",
      record_id: "record-1",
      requested_at: "2026-09-10T00:00:00Z",
      session: { enrollment: null, request: null },
    },
  ]);

  try {
    expect(await screen.findByRole("link", { name: "Inbox 3" })).toBeTruthy();
  } finally {
    sidebar.view.unmount();
    sidebar.getCredentialPending.mockRestore();
    sidebar.getInbox.mockRestore();
    sidebar.getIssue.mockRestore();
    sidebar.listIssues.mockRestore();
    sidebar.listProjects.mockRestore();
  }
});

test("sidebar marks the current project", async () => {
  const sidebar = renderSidebar("/projects/CORE/documents");

  try {
    const core = await screen.findByRole("link", { name: /CORE.*Core/ });
    expect(core).toHaveAttribute("aria-current", "page");
  } finally {
    sidebar.view.unmount();
    sidebar.getCredentialPending.mockRestore();
    sidebar.getInbox.mockRestore();
    sidebar.getIssue.mockRestore();
    sidebar.listIssues.mockRestore();
    sidebar.listProjects.mockRestore();
  }
});

test("sidebar never requests the full issue list", async () => {
  const sidebar = renderSidebar();

  try {
    await screen.findByRole("link", { name: /CORE.*Core/ });
    expect(sidebar.listIssues).toHaveBeenCalledTimes(1);
    expect(sidebar.listIssues).toHaveBeenCalledWith({ pinned: true });
    expect(sidebar.getIssue).not.toHaveBeenCalled();
  } finally {
    sidebar.view.unmount();
    sidebar.getCredentialPending.mockRestore();
    sidebar.getInbox.mockRestore();
    sidebar.getIssue.mockRestore();
    sidebar.listIssues.mockRestore();
    sidebar.listProjects.mockRestore();
  }
});

// The Inbox lists the threads beside the asks now, so the rail has one row and one number for
// both: the work waiting on the reader plus the threads they have not read.
for (const [unread, name] of [
  [2, "Inbox4"],
  [0, "Inbox2"],
] as const) {
  test(`sidebar adds ${unread} unread threads to the Inbox count`, async () => {
    const whoAmI = spyOn(api, "whoAmI").mockResolvedValue({
      event_head: "0",
      kind: "user",
      login: "alice",
    });
    const listMyThreads = spyOn(api, "listMyThreads").mockResolvedValue({
      as_of: "2026-10-12T00:00:00Z",
      items: [],
      limit: 1,
      offset: 0,
      total: unread,
    });
    const sidebar = renderSidebar("/");

    try {
      await waitFor(() =>
        expect(listMyThreads).toHaveBeenCalledWith({ kind: ["reply", "agent"], limit: 1 })
      );
      await waitFor(() =>
        expect(screen.getByRole("link", { name: /^Inbox/ }).textContent).toBe(name)
      );
      expect(screen.queryByRole("link", { name: /^Replies/ })).not.toBeInTheDocument();
    } finally {
      sidebar.view.unmount();
      sidebar.getCredentialPending.mockRestore();
      sidebar.getInbox.mockRestore();
      sidebar.getIssue.mockRestore();
      sidebar.listIssues.mockRestore();
      sidebar.listProjects.mockRestore();
      whoAmI.mockRestore();
      listMyThreads.mockRestore();
    }
  });
}

test("sidebar keeps its hide control available while navigation loads", async () => {
  const user = userEvent.setup();
  const getInbox = spyOn(api, "getInbox").mockImplementation(() => new Promise<never>(() => {}));
  const getCredentialPending = spyOn(api, "getCredentialPending").mockImplementation(
    () => new Promise<never>(() => {})
  );
  const listIssues = spyOn(api, "listIssues").mockResolvedValue([]);
  const listProjects = spyOn(api, "listProjects").mockResolvedValue([]);
  let hideCount = 0;
  const view = render(
    createElement(
      MemoryRouter,
      { initialEntries: ["/"] },
      createElement(
        QueryClientProvider,
        { client: new QueryClient({ defaultOptions: { queries: { retry: false } } }) },
        createElement(Sidebar, {
          onHide: () => {
            hideCount += 1;
          },
          user: { event_head: "0", kind: "user", login: "alice" },
        })
      )
    )
  );

  try {
    expect(screen.getByText("Loading navigation…")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Hide sidebar" }));
    expect(hideCount).toBe(1);
  } finally {
    view.unmount();
    getInbox.mockRestore();
    getCredentialPending.mockRestore();
    listIssues.mockRestore();
    listProjects.mockRestore();
  }
});

// The rail's Priorities entry (INFRA-2947, R8): the way to the page from every other page, with
// the number of faults now beside it, which is the two fault counts added.
test("the rail's Priorities entry opens the page and counts both faults as one number", async () => {
  const sidebar = renderSidebar("/", undefined, summary(5, 7));

  try {
    const entry = await screen.findByRole("link", { name: /^Priorities/ });
    expect(entry).toHaveAttribute("href", "/priorities");
    await waitFor(() => expect(entry).toHaveTextContent("12"));
  } finally {
    sidebar.view.unmount();
    sidebar.getCredentialPending.mockRestore();
    sidebar.getInbox.mockRestore();
    sidebar.getIssue.mockRestore();
    sidebar.listIssues.mockRestore();
    sidebar.listProjects.mockRestore();
  }
});

// Where no priority has been written there is nothing for work to be off, so the entry carries
// no number rather than a zero the reader would read as a state of the board.
test("the rail's Priorities entry carries no number where no priority exists", async () => {
  const sidebar = renderSidebar();

  try {
    const entry = await screen.findByRole("link", { name: /^Priorities/ });
    await waitFor(() => expect(sidebar.getPrioritySummary).toHaveBeenCalledWith([]));
    expect(entry.textContent).toBe("PrioritiesEvery project's priorities, and the work on none");
  } finally {
    sidebar.view.unmount();
    sidebar.getCredentialPending.mockRestore();
    sidebar.getInbox.mockRestore();
    sidebar.getIssue.mockRestore();
    sidebar.listIssues.mockRestore();
    sidebar.listProjects.mockRestore();
  }
});
