import { expect, spyOn, test } from "bun:test";
import { QueryClient, QueryObserver } from "@tanstack/react-query";

import { api } from "../api/client";
import { mergeIssue } from "../api/issue-cache";
import { issueQuery } from "../api/queries";
import type { IssueDetails, IssueReachV2 } from "../api/types";
import { issue as fixture } from "../features/margin/margin-fixture";

function cachedAfterPatch(before: IssueDetails, patch: Partial<IssueDetails>): IssueDetails {
  const queryClient = new QueryClient();
  queryClient.setQueryData(["issue", before.key], before);
  // A PATCH answers the `Issue` fields alone; the route's reach is never among them.
  const { reach: _reach, ...response } = { ...before, ...patch };
  mergeIssue(queryClient, response);
  const cached = queryClient.getQueryData<IssueDetails>(["issue", before.key]);
  if (cached === undefined) throw new Error("the merge dropped the cached issue");
  return cached;
}

/** Where the issue's route reaches, as `include=reach` answers it: nothing known but `fields`. */
function reachOf(fields: Partial<IssueReachV2>): IssueReachV2 {
  return {
    holder_state: null,
    last_seen: null,
    route_holder: null,
    route_status: null,
    route_status_reason: null,
    takeable_at: null,
    ...fields,
  };
}

const unheld: IssueDetails = {
  ...fixture,
  reach: reachOf({ route_status: "no_holder" }),
  route: "role:sre",
};

test("a PATCH that moves the route drops the old route's status instead of wearing it", () => {
  const cached = cachedAfterPatch(unheld, { route: "role:platform-po" });
  expect(cached.route).toBe("role:platform-po");
  expect(cached.reach.route_status).toBeNull();
  expect(cached.reach.route_holder).toBeNull();
});

test("a PATCH that leaves the route alone keeps its status until the next read", () => {
  const cached = cachedAfterPatch(unheld, { title: "Renamed" });
  expect(cached.title).toBe("Renamed");
  expect(cached.reach.route_status).toBe("no_holder");
});

const refilling: IssueDetails = {
  ...fixture,
  reach: reachOf({ route_status: "unknown", route_status_reason: "refilling" }),
  route: "role:sre",
};

test("a PATCH that moves the route drops why the old route was unknown", () => {
  const cached = cachedAfterPatch(refilling, { route: "role:platform-po" });
  expect(cached.reach.route_status).toBeNull();
  expect(cached.reach.route_status_reason).toBeNull();
});

test("a PATCH that leaves the route alone keeps why it is unknown", () => {
  const cached = cachedAfterPatch(refilling, { title: "Renamed" });
  expect(cached.reach.route_status).toBe("unknown");
  expect(cached.reach.route_status_reason).toBe("refilling");
});

// `reach` holds two things: where the route reaches (`route_status`, `route_holder`,
// `route_status_reason`) and the liveness of the session holding the issue's claim
// (`holder_state`, `last_seen`, `takeable_at`; `issueReachV2` is `IssueRouteReach` beside
// `IssueClaimLiveness`). A PATCH that moves the route says nothing of the claim, so the claim
// chip keeps its holder's liveness while the old route's reach goes.
const held: IssueDetails = {
  ...fixture,
  reach: reachOf({
    holder_state: "unresponsive",
    last_seen: "2026-10-01T00:00:00Z",
    route_holder: "session-sre",
    route_status: "live",
    takeable_at: "2026-10-01T00:10:00Z",
  }),
  route: "role:sre",
};

test("a PATCH that moves the route drops the old route's reach and keeps the claim's liveness", () => {
  expect(cachedAfterPatch(held, { route: "role:platform-po" }).reach).toEqual(
    reachOf({
      holder_state: "unresponsive",
      last_seen: "2026-10-01T00:00:00Z",
      takeable_at: "2026-10-01T00:10:00Z",
    })
  );
  expect(cachedAfterPatch(held, { title: "Renamed" }).reach).toEqual(held.reach);
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
