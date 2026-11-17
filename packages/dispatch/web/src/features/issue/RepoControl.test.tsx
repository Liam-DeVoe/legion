import { expect, spyOn, test } from "bun:test";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
// biome-ignore lint/style/noRestrictedImports: this file keeps fireEvent for the focus that arms the lazy read of the project's mappings, which user-event has no form for
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { ReactNode } from "react";
import { keyedAttempt } from "../../__tests__/keyed-attempt";
import { listOf, openList, pick, rows, textBoxes } from "../../__tests__/list-picks";
import { answerIssuePatches } from "../../__tests__/send-answers";
import { ApiError, api } from "../../api/client";
import type { Issue, IssueDetails, IssueSummary, RepoProject } from "../../api/types";
import { RepoControl } from "./RepoControl";

const issue: Issue = {
  advances: [],
  advances_revision: 0,
  assignee: null,
  claim: null,
  components: { mode: "inherit", ids: [], unknown: [], reason: null, inherited_from: null },
  closed_at: null,
  created_at: "2026-10-01T00:00:00Z",
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
  repo: null,
  resolved_repo: "acme/widgets",
  route: null,
  status: "todo",
  title: "Repository work",
  updated_at: "2026-10-01T00:00:00Z",
};
const summary: IssueSummary = {
  advances: [],
  advances_revision: 0,
  assignee: null,
  blocked_by: [],
  claim: null,
  components: issue.components,
  key: issue.key,
  labels: [],
  last_seq: 1,
  open_asks: 0,
  parent: null,
  primary_artifact_id: "artifact-1",
  priority: null,
  progress: { tasks: null, children: null },
  rank: "U",
  repo: null,
  resolved_repo: "acme/widgets",
  route: null,
  route_holder: null,
  route_status: null,
  route_status_reason: null,
  status: "todo",
  title: issue.title,
  updated_at: issue.updated_at,
  closed_at: issue.closed_at,
  created_by: issue.created_by,
  created_at: issue.created_at,
};
const mappings: RepoProject[] = [
  {
    repo: "acme/widgets",
    project: "CORE",
    default: true,
    renamed_to: null,
    created_by: { kind: "user", id: "alice" },
    created_at: "2026-10-01T00:00:00Z",
  },
  {
    repo: "acme/gadgets",
    project: "CORE",
    default: false,
    renamed_to: null,
    created_by: { kind: "user", id: "alice" },
    created_at: "2026-10-01T00:00:00Z",
  },
  {
    repo: "other/thing",
    project: "OTHER",
    default: true,
    renamed_to: null,
    created_by: { kind: "user", id: "alice" },
    created_at: "2026-10-01T00:00:00Z",
  },
];

const detailKey = ["issue", issue.key] as const;
const listKey = ["issues", "project", issue.project] as const;

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
    <RepoControl
      issueKey={cached.data.key}
      project={cached.data.project}
      repo={cached.data.repo}
      resolvedRepo={cached.data.resolved_repo}
    />
  );
}

function renderControl(
  own: { repo: string | null; resolvedRepo: string | null },
  mapped: RepoProject[] = mappings,
  { failFirstRead = false }: { failFirstRead?: boolean } = {}
) {
  const listRepoProjects = spyOn(api, "listRepoProjects").mockResolvedValue(mapped);
  if (failFirstRead) {
    listRepoProjects.mockRejectedValueOnce(new Error("the network dropped the read"));
  }
  const queryClient = new QueryClient({
    defaultOptions: { mutations: { retry: false }, queries: { retry: false } },
  });
  queryClient.setQueryData<IssueDetails>(detailKey, {
    ...issue,
    repo: own.repo,
    resolved_repo: own.resolvedRepo,
    route_status: null,
    route_holder: null,
    route_status_reason: null,
    holder_state: null,
    last_seen: null,
    takeable_at: null,
    artifacts: [],
    children: [],
    design_gates: [],
    open_asks: [],
    progress: { tasks: null, children: null },
    referenced_by_count: 0,
  });
  queryClient.setQueryData(listKey, [
    { ...summary, repo: own.repo, resolved_repo: own.resolvedRepo },
  ]);
  const user = userEvent.setup();
  const view = render(
    <QueryClientProvider client={queryClient}>
      <CachedControl />
    </QueryClientProvider>
  );
  return {
    user,
    listRepoProjects,
    queryClient,
    unmount: () => {
      view.unmount();
      listRepoProjects.mockRestore();
    },
  };
}

/** The control's name: its trigger's, and its listbox's with ` options` after it. */
const REPOSITORY = "Repository of CORE-1";

function trigger(): HTMLButtonElement {
  return screen.getByRole("button", { name: REPOSITORY }) as HTMLButtonElement;
}

/** The visible badge; the trigger's description repeats its text in a span of its own. */
const badge = 'span[aria-hidden="true"]';

test("RepoControl lists the project's mapped repositories, shows the resolved one, and patches repo", async () => {
  const patchIssue = answerIssuePatches().mockResolvedValue({
    ...issue,
    repo: "acme/gadgets",
    resolved_repo: "acme/gadgets",
    last_seq: 2,
  });
  const { listRepoProjects, queryClient, unmount, user } = renderControl({
    repo: null,
    resolvedRepo: "acme/widgets",
  });
  try {
    // The badge names the repository the issue resolves to; no mapping is read until the reader
    // reaches for the control.
    expect(screen.getByText("acme/widgets", { selector: badge })).toBeTruthy();
    expect(listRepoProjects).not.toHaveBeenCalled();
    // A focus with no press before it: user-event has no form for a bare focus.
    fireEvent.focus(trigger());
    await waitFor(() => expect(listRepoProjects).toHaveBeenCalled());
    // Inherit first, then CORE's mappings sorted; OTHER's absent.
    const list = await openList(user, REPOSITORY);
    await waitFor(() => expect(rows(list)).toEqual(["✓Inherit", "acme/gadgets", "acme/widgets"]));
    await user.click(within(list).getByRole("option", { name: "acme/gadgets" }));
    await waitFor(() =>
      expect(patchIssue).toHaveBeenCalledWith("CORE-1", { repo: "acme/gadgets" }, keyedAttempt)
    );
    await waitFor(() =>
      expect(queryClient.getQueryData<IssueDetails>(detailKey)?.last_seq).toBe(2)
    );
    expect(trigger()).toHaveAccessibleDescription("acme/gadgets");
    expect(screen.getByText("acme/gadgets", { selector: badge })).toBeTruthy();
    expect(queryClient.getQueryData<IssueSummary[]>(listKey)?.[0]?.repo).toBe("acme/gadgets");
  } finally {
    unmount();
    patchIssue.mockRestore();
  }
});

test("RepoControl sends null for Inherit", async () => {
  const patchIssue = answerIssuePatches().mockResolvedValue({
    ...issue,
    repo: null,
    resolved_repo: "acme/widgets",
  });
  const { unmount, user } = renderControl({ repo: "acme/gadgets", resolvedRepo: "acme/gadgets" });
  try {
    // The issue's own repository is selectable before the mapping list has loaded.
    expect(trigger()).toHaveAccessibleDescription("acme/gadgets");
    await pick(user, REPOSITORY, "Inherit");
    await waitFor(() =>
      expect(patchIssue).toHaveBeenCalledWith("CORE-1", { repo: null }, keyedAttempt)
    );
    await waitFor(() => expect(screen.getByText("acme/widgets", { selector: badge })).toBeTruthy());
  } finally {
    unmount();
    patchIssue.mockRestore();
  }
});

// The badge is decoration for sighted readers (aria-hidden); the trigger names the repository the
// issue resolves to in its description, so a screen reader hears the target as well as Inherit.
test("RepoControl describes the repository the issue resolves to", async () => {
  for (const [own, want] of [
    [
      { repo: null, resolvedRepo: "acme/widgets" },
      "acme/widgets, inherited or the project's default",
    ],
    [{ repo: "acme/gadgets", resolvedRepo: "acme/gadgets" }, "acme/gadgets"],
    [{ repo: null, resolvedRepo: null }, "No repository"],
  ] as const) {
    const { unmount } = renderControl({ ...own });
    try {
      expect(
        await screen.findByRole("button", { name: "Repository of CORE-1" })
      ).toHaveAccessibleDescription(want);
    } finally {
      unmount();
    }
  }
});

// With nothing resolved the control cannot tell from the issue whether its project maps any
// repository, so it reads the mappings at once: one is there to pick, so the badge says so.
test("RepoControl says No repository when nothing resolves in a project that maps one", async () => {
  const { listRepoProjects, unmount } = renderControl({ repo: null, resolvedRepo: null });
  try {
    expect(await screen.findByText("No repository", { selector: badge })).toBeTruthy();
    expect(screen.getByText("Repository:")).toBeTruthy();
    expect(listRepoProjects).toHaveBeenCalledTimes(1);
  } finally {
    unmount();
  }
});

// A project that maps no repository offers nothing to pick, so the details rail carries no
// repository item at all.
test("RepoControl shows nothing for an issue whose project maps no repository", async () => {
  const others = mappings.filter((mapping) => mapping.project !== "CORE");
  const { queryClient, unmount } = renderControl({ repo: null, resolvedRepo: null }, others);
  try {
    // The mappings are read at once; once they are in and React has rendered them, the control
    // has decided.
    await waitFor(() =>
      expect(queryClient.getQueryData<RepoProject[]>(["repo-projects"])).toEqual(others)
    );
    await act(async () => {});
    expect(screen.queryByRole("button", { name: "Repository of CORE-1" })).not.toBeInTheDocument();
    expect(screen.queryByText("Repository:")).not.toBeInTheDocument();
  } finally {
    unmount();
  }
});

// A failed mappings read is not an empty one: with nothing resolved the control cannot tell
// whether the project maps a repository, so it says the read failed and offers it again rather
// than hiding the item.
test("RepoControl shows a failed mappings read with a retry instead of hiding the item", async () => {
  const { listRepoProjects, unmount, user } = renderControl(
    { repo: null, resolvedRepo: null },
    mappings,
    { failFirstRead: true }
  );
  try {
    expect(await screen.findByText("Could not load the repositories mapped to CORE.")).toBeTruthy();
    expect(screen.getByText("Repository:")).toBeTruthy();
    await user.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(listRepoProjects).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(trigger()).toBeTruthy());
    expect(rows(await openList(user, REPOSITORY))).toHaveLength(3);
    expect(
      screen.queryByText("Could not load the repositories mapped to CORE.")
    ).not.toBeInTheDocument();
    expect(screen.getByText("No repository", { selector: badge })).toBeTruthy();
  } finally {
    unmount();
  }
});

// With a repository resolved the picker still shows it, and a failed read says so beside it.
test("RepoControl shows a failed mappings read beside a resolved repository", async () => {
  const { unmount } = renderControl({ repo: null, resolvedRepo: "acme/widgets" }, mappings, {
    failFirstRead: true,
  });
  try {
    // A focus with no press before it: user-event has no form for a bare focus.
    fireEvent.focus(trigger());
    expect(await screen.findByText("Could not load the repositories mapped to CORE.")).toBeTruthy();
    expect(screen.getByText("acme/widgets", { selector: badge })).toBeTruthy();
    expect(screen.getByRole("button", { name: "Retry" })).toBeTruthy();
  } finally {
    unmount();
  }
});

test("RepoControl rolls back and shows the server's refusal", async () => {
  const patchIssue = answerIssuePatches().mockRejectedValue(
    new ApiError(400, {
      code: "REPO_NOT_MAPPED",
      error: "acme/gadgets is not mapped to project CORE (Settings → Repositories → Projects)",
    })
  );
  const { unmount, user } = renderControl({ repo: null, resolvedRepo: "acme/widgets" });
  try {
    // A focus with no press before it: user-event has no form for a bare focus.
    fireEvent.focus(trigger());
    await pick(user, REPOSITORY, "acme/gadgets");
    expect(
      await screen.findByText(
        "acme/gadgets is not mapped to project CORE (Settings → Repositories → Projects)"
      )
    ).toBeTruthy();
    expect(trigger()).toHaveAccessibleDescription(
      "acme/widgets, inherited or the project's default"
    );
    expect(screen.getByText("acme/widgets", { selector: badge })).toBeTruthy();
  } finally {
    unmount();
    patchIssue.mockRestore();
  }
});

// INFRA-3135, R3: "Every list of options a reader picks from holds a text box that narrows the
// options." The issue header's Repository is such a list: its options are the repositories
// mapped to the issue's project, which grow with the project. Opened, it holds a text box of its
// own, a word typed into it leaves only the repositories that match, and a pick writes the
// issue's own repository and puts the focus back on the control. Opened again it stands on the
// value it holds, and Enter there writes nothing.
test("the issue header's Repository list holds a text box that narrows the repositories, and a pick writes the issue's own", async () => {
  const patchIssue = answerIssuePatches().mockResolvedValue({
    ...issue,
    repo: "acme/gadgets",
    resolved_repo: "acme/gadgets",
  });
  const { unmount, user } = renderControl({ repo: null, resolvedRepo: "acme/widgets" });
  try {
    const before = new Set(textBoxes());
    await user.click(screen.getByRole("button", { name: "Repository of CORE-1" }));
    // The mappings are read once the reader reaches for the control.
    await waitFor(() =>
      expect(screen.queryAllByRole("option", { name: "acme/gadgets" }).length).toBeGreaterThan(0)
    );
    const own = textBoxes().filter((box) => !before.has(box));
    expect(own, "the Repository list holds no text box of its own").toHaveLength(1);
    const list = listOf(own[0] as HTMLElement);
    expect(within(list).getByRole("option", { name: "Inherit" })).toHaveAttribute(
      "aria-selected",
      "true"
    );
    await user.type(own[0] as HTMLElement, "gadg");
    await waitFor(() =>
      expect(
        within(list).queryAllByRole("option", { name: "acme/widgets" }),
        `"gadg" leaves acme/widgets in the Repository list`
      ).toHaveLength(0)
    );
    expect(
      within(list).queryAllByRole("option", { name: "Inherit" }),
      `"gadg" leaves Inherit in the Repository list`
    ).toHaveLength(0);
    await user.click(within(list).getByRole("option", { name: "acme/gadgets" }));
    await waitFor(() =>
      expect(patchIssue).toHaveBeenCalledWith("CORE-1", { repo: "acme/gadgets" }, keyedAttempt)
    );
    expect(screen.queryByRole("listbox", { name: "Repository of CORE-1 options" })).toBeNull();
    expect(trigger()).toHaveFocus();
    await waitFor(() => expect(trigger()).toHaveAccessibleDescription("acme/gadgets"));

    await user.keyboard("{Enter}");
    await screen.findByRole("listbox", { name: "Repository of CORE-1 options" });
    await user.keyboard("{Enter}");
    expect(screen.queryByRole("listbox", { name: "Repository of CORE-1 options" })).toBeNull();
    await act(async () => {});
    expect(patchIssue).toHaveBeenCalledTimes(1);
  } finally {
    unmount();
    patchIssue.mockRestore();
  }
});

// INFRA-3135, round 12: a list whose read failed says so. Beside a resolved repository the rail
// shows the failure with a Retry, and the list the reader opens says it too, over the Inherit it
// still offers.
test("the issue header's Repository list says the repositories could not be loaded when their read fails", async () => {
  const { unmount, user } = renderControl({ repo: null, resolvedRepo: "acme/widgets" }, mappings, {
    failFirstRead: true,
  });
  try {
    const list = await openList(user, REPOSITORY);
    const panel = list.parentElement as HTMLElement;
    expect(
      await within(panel).findByText("Could not load the repositories mapped to CORE.")
    ).toBeTruthy();
    expect(rows(list)).toEqual(["✓Inherit"]);
  } finally {
    unmount();
  }
});
