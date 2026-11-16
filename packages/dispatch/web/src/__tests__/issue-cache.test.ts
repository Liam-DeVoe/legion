import { expect, spyOn, test } from "bun:test";
import { QueryClient, QueryObserver } from "@tanstack/react-query";

import { api } from "../api/client";
import { mergeIssue } from "../api/issue-cache";
import { issueQuery, projectDonePageQuery, projectIssuesQuery } from "../api/queries";
import type { IssueDetails, IssueSummary } from "../api/types";
import { issue as fixture } from "../features/margin/margin-fixture";

function cachedAfterPatch(before: IssueDetails, patch: Partial<IssueDetails>): IssueDetails {
  const queryClient = new QueryClient();
  queryClient.setQueryData(["issue", before.key], before);
  // A PATCH answers the `Issue` fields alone; the route's reach is never among them.
  const {
    route_status: _status,
    route_holder: _holder,
    route_status_reason: _reason,
    ...response
  } = { ...before, ...patch };
  mergeIssue(queryClient, response);
  const cached = queryClient.getQueryData<IssueDetails>(["issue", before.key]);
  if (cached === undefined) throw new Error("the merge dropped the cached issue");
  return cached;
}

const unheld: IssueDetails = { ...fixture, route: "role:sre", route_status: "no_holder" };

test("a PATCH that moves the route drops the old route's status instead of wearing it", () => {
  const cached = cachedAfterPatch(unheld, { route: "role:platform-po" });
  expect(cached.route).toBe("role:platform-po");
  expect(cached.route_status).toBeNull();
  expect(cached.route_holder).toBeNull();
});

test("a PATCH that leaves the route alone keeps its status until the next read", () => {
  const cached = cachedAfterPatch(unheld, { title: "Renamed" });
  expect(cached.title).toBe("Renamed");
  expect(cached.route_status).toBe("no_holder");
});

const refilling: IssueDetails = {
  ...fixture,
  route: "role:sre",
  route_status: "unknown",
  route_status_reason: "refilling",
};

test("a PATCH that moves the route drops why the old route was unknown", () => {
  const cached = cachedAfterPatch(refilling, { route: "role:platform-po" });
  expect(cached.route_status).toBeNull();
  expect(cached.route_status_reason).toBeNull();
});

test("a PATCH that leaves the route alone keeps why it is unknown", () => {
  const cached = cachedAfterPatch(refilling, { title: "Renamed" });
  expect(cached.route_status).toBe("unknown");
  expect(cached.route_status_reason).toBe("refilling");
});

// W35. A read already out when a write changed the issue - a slow GET started before a PATCH - can
// be answered after the PATCH's own answer was put in its place. Its issue is the older one (an
// earlier `updated_at`, read whole: `…00.1Z` is before `…00.123456Z`), so the cache keeps the
// PATCH's; a read that is not older replaces the cached issue as any read does.
test("a slow GET answered after a PATCH leaves the PATCH's answer in place", async () => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const read: IssueDetails = { ...fixture, status: "todo", updated_at: "2026-10-01T00:00:00.1Z" };
  const slow = Promise.withResolvers<IssueDetails>();
  const getIssue = spyOn(api, "getIssue")
    .mockResolvedValueOnce(read)
    .mockImplementationOnce(() => slow.promise);
  const observer = new QueryObserver(queryClient, issueQuery(read.key));
  const unsubscribe = observer.subscribe(() => {});
  const cached = () => queryClient.getQueryData<IssueDetails>(["issue", read.key]);
  try {
    await observer.refetch();
    expect(cached()?.status).toBe("todo");

    const refetch = observer.refetch();
    const { artifacts: _artifacts, children: _children, open_asks: _asks, ...issue } = read;
    mergeIssue(queryClient, {
      ...issue,
      status: "done",
      updated_at: "2026-10-01T00:00:00.123456Z",
    });
    slow.resolve(read);
    await refetch;
    expect(cached()).toMatchObject({ status: "done", updated_at: "2026-10-01T00:00:00.123456Z" });

    getIssue.mockResolvedValueOnce({
      ...read,
      status: "done",
      updated_at: "2026-10-01T00:00:00.2Z",
    });
    await observer.refetch();
    expect(cached()).toMatchObject({ status: "done", updated_at: "2026-10-01T00:00:00.2Z" });
  } finally {
    unsubscribe();
    getIssue.mockRestore();
  }
});

// A listing's rows are shared with the cache by issue key, not by place: an issue that moves up
// the listing (its `updated_at` changed) leaves every row it passed with the object the page
// already holds, so a memoised row or Board card of those draws nothing again. TanStack's own
// sharing pairs rows by index and handed each of them a new object.
const row = (key: string, updated_at: string): IssueSummary => ({
  key,
  advances: [],
  advances_revision: 0,
  title: `Work ${key}`,
  status: "todo",
  priority: null,
  rank: key,
  labels: [],
  parent: null,
  blocked_by: [],
  assignee: null,
  claim: null,
  components: { mode: "inherit", ids: [], unknown: [], reason: null, inherited_from: null },
  route: null,
  repo: null,
  resolved_repo: null,
  route_status: null,
  route_holder: null,
  route_status_reason: null,
  last_seq: 2,
  open_asks: 0,
  progress: { tasks: null, children: null },
  primary_artifact_id: "spec",
  created_by: { kind: "user", id: "alice" },
  created_at: "2026-09-10T00:00:00Z",
  updated_at,
  closed_at: null,
});

test("a listing read keeps the object of every issue it did not change, wherever it moved", async () => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const first = [
    row("CORE-1", "2026-10-03"),
    row("CORE-2", "2026-10-02"),
    row("CORE-3", "2026-10-01"),
  ];
  // CORE-3 changed and moved to the top; CORE-1 and CORE-2 each moved down one place.
  const second = [
    row("CORE-3", "2026-10-04"),
    row("CORE-1", "2026-10-03"),
    row("CORE-2", "2026-10-02"),
  ];
  const listIssues = spyOn(api, "listIssues")
    .mockResolvedValueOnce(first)
    .mockResolvedValueOnce(second)
    .mockResolvedValueOnce(structuredClone(second));
  const observer = new QueryObserver(queryClient, projectIssuesQuery("CORE", [], true));
  const unsubscribe = observer.subscribe(() => {});
  try {
    const before = (await observer.refetch()).data ?? [];
    const after = (await observer.refetch()).data ?? [];
    expect(after.map((issue) => issue.key)).toEqual(["CORE-3", "CORE-1", "CORE-2"]);
    expect(after[1]).toBe(before[0]);
    expect(after[2]).toBe(before[1]);
    expect(after[0]).not.toBe(before[2]);
    expect(after[0]?.updated_at).toBe("2026-10-04");
    // A read that changed nothing keeps the cached listing itself.
    expect((await observer.refetch()).data).toBe(after);
  } finally {
    unsubscribe();
    listIssues.mockRestore();
  }
});

test("a page of a listing keeps its unchanged issues by key, and its count", async () => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const page = (issues: IssueSummary[], total: number) => ({ issues, limit: 2, offset: 0, total });
  const listIssuePage = spyOn(api, "listIssuePage")
    .mockResolvedValueOnce(page([row("CORE-1", "2026-10-02"), row("CORE-2", "2026-10-01")], 2))
    .mockResolvedValueOnce(page([row("CORE-3", "2026-10-03"), row("CORE-1", "2026-10-02")], 3))
    .mockResolvedValueOnce(page([row("CORE-3", "2026-10-03"), row("CORE-1", "2026-10-02")], 3));
  const observer = new QueryObserver(
    queryClient,
    projectDonePageQuery("CORE", {}, { sort: "-closed_at", limit: 2, offset: 0 })
  );
  const unsubscribe = observer.subscribe(() => {});
  try {
    const before = (await observer.refetch()).data;
    const after = (await observer.refetch()).data;
    expect(after?.total).toBe(3);
    expect(after?.issues?.[1]).toBe(before?.issues?.[0]);
    expect((await observer.refetch()).data).toBe(after);
  } finally {
    unsubscribe();
    listIssuePage.mockRestore();
  }
});
