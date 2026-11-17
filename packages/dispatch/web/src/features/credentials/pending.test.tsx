import { expect, spyOn, test } from "bun:test";
import { type Query, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import { http } from "msw";
import type { ReactNode } from "react";

import { answerRequests } from "../../__tests__/network";
import { api } from "../../api/client";
import type { CredentialPendingRow } from "../../api/types";
import { credentialPendingQuery, useCredentialRequests } from "./pending";

function renderPending() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = renderHook(() => useCredentialRequests(), {
    wrapper: ({ children }: { children: ReactNode }) => (
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    ),
  });
  return { queryClient, view };
}

// A `null`-holding query (no secrets broker) can neither fetch again on its own nor have the poll
// armed: `enabled` and `refetchInterval` both read the same `query.state.data !== null` fact, so
// if one ever answered differently from the other a brokerless Dispatch would start polling.
test("credentialPendingQuery's enabled and refetchInterval agree: armed for a list, off for null", () => {
  const query = credentialPendingQuery();
  const heldNull = { state: { data: null } } as unknown as Query<CredentialPendingRow[] | null>;
  const heldList = { state: { data: [] } } as unknown as Query<CredentialPendingRow[] | null>;

  expect(query.enabled).toBeInstanceOf(Function);
  expect(query.refetchInterval).toBeInstanceOf(Function);
  const enabled = query.enabled as (query: typeof heldNull) => boolean;
  const refetchInterval = query.refetchInterval as (query: typeof heldNull) => number | false;

  expect(enabled(heldNull)).toBe(false);
  expect(refetchInterval(heldNull)).toBe(false);
  expect(enabled(heldList)).toBe(true);
  expect(typeof refetchInterval(heldList)).toBe("number");
  expect(refetchInterval(heldList)).toBeGreaterThan(0);
});

// A Dispatch with no secrets broker refuses the pending list `404 FEATURE_OFF`, as it refuses
// every credential route. That is an ordinary answer for the list every page reads: the Inbox
// lists no request and says nothing failed, and the list is not asked again on its own.
test("a Dispatch with no secrets broker lists no credential request and reports no failure", async () => {
  let asked = 0;
  answerRequests(
    http.get("/api/v2/credential-requests", () => {
      asked += 1;
      return Response.json(
        { code: "FEATURE_OFF", error: "the secrets broker is not configured" },
        { status: 404 }
      );
    })
  );
  const { queryClient, view } = renderPending();
  try {
    await waitFor(() => expect(view.result.current.status).toBe("listed"));
    expect(view.result.current.requests).toEqual([]);
    expect(queryClient.getQueryData(credentialPendingQuery().queryKey)).toBeNull();
    expect(asked).toBe(1);
  } finally {
    view.unmount();
  }
});

// LEGION-575 round 2. A background poll that fails after a list already loaded must not blank a
// good list: `useCredentialRequests` used to key `status: "failed"` off `isError` alone, which
// TanStack also sets on a *refetch* error with the last good `data` still attached - so the
// Inbox's section, its empty state and the Needs-you count would all drop a real pending request
// for as long as one poll tick failed, self-healing only on the next successful tick.
test("a poll that fails after a list already loaded keeps showing that list, not a failure", async () => {
  const getCredentialPending = spyOn(api, "getCredentialPending")
    .mockResolvedValueOnce([
      {
        identifiers: ["DEMO_API_KEY"],
        kind: "agent_secret",
        record_id: "req-1",
        requested_at: "2026-09-27T00:00:00Z",
        session: { enrollment: null, request: null },
      },
    ])
    .mockRejectedValueOnce(new Error("network"));
  const { queryClient, view } = renderPending();
  try {
    await waitFor(() => expect(view.result.current.status).toBe("listed"));
    expect(view.result.current.requests).toHaveLength(1);

    await queryClient.refetchQueries({ queryKey: ["credential-pending"] });

    expect(getCredentialPending).toHaveBeenCalledTimes(2);
    expect(view.result.current.status).toBe("listed");
    expect(view.result.current.requests).toHaveLength(1);
    expect(view.result.current.requests[0]?.record_id).toBe("req-1");
  } finally {
    view.unmount();
    getCredentialPending.mockRestore();
  }
});
