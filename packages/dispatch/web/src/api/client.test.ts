import { expect, test } from "bun:test";
import { NO_ROUTE_ROLLING_RELEASE_MESSAGE, type Sleep, sleep } from "@legion/contracts";

import { ApiError, createApiClient, type FetchImplementation, isNoRoute } from "./client";

// While a release rolls out, two builds of Dispatch answer the page, and the older one answers a
// route only the newer build serves with a 404 that says no route serves it. No handler ran, so
// the page sends the request again, and the next attempt may reach the newer process. `/api/v2`
// says it with its own code (`NO_ROUTE`); a release before `/api/v2` says it of every path it
// does not serve - an `/api/v2` path among them - as `NOT_FOUND` "no route for …".
function noRoute(method: string, path: string): Response {
  return Response.json(
    {
      code: "NO_ROUTE",
      error: `no route for ${method} ${path}`,
      hint: "GET /api/v2 lists every route",
    },
    { status: 404 }
  );
}

/** A release before `/api/v2`'s answer for a path it serves no route for. */
function olderNoRoute(method: string, path: string): Response {
  return Response.json(
    {
      code: "NOT_FOUND",
      error: `no route for ${method} ${path}`,
      hint: "GET /api/v1 lists every route",
    },
    { status: 404 }
  );
}

/** A fetch that answers each request with the next of `answers`, recording what it was sent. */
function scriptedFetch(answers: readonly (() => Response)[]): {
  fetch: FetchImplementation;
  requests: { path: string; init: RequestInit | undefined }[];
} {
  const requests: { path: string; init: RequestInit | undefined }[] = [];
  return {
    fetch: async (input, init) => {
      requests.push({ path: input.toString(), init });
      const answer = answers[requests.length - 1];
      if (answer === undefined) throw new Error(`unexpected request ${requests.length}`);
      return answer();
    },
    requests,
  };
}

/** A sleep that returns at once and records each wait it was asked for. */
function recordedSleep(): { sleep: (ms: number) => Promise<void>; waits: number[] } {
  const waits: number[] = [];
  return {
    sleep: async (ms) => {
      waits.push(ms);
    },
    waits,
  };
}

test("a no-route answer is sent again, and the next answer resolves the request", async () => {
  const stub = scriptedFetch([
    () => olderNoRoute("GET", "/auth/whoami"),
    () => Response.json({ event_head: "0", kind: "user", login: "alice" }),
  ]);
  const wait = recordedSleep();
  const api = createApiClient(stub.fetch, undefined, wait.sleep);

  expect(await api.whoAmI()).toEqual({ event_head: "0", kind: "user", login: "alice" });
  expect(stub.requests.map((request) => request.path)).toEqual(["/auth/whoami", "/auth/whoami"]);
  // One wait of a second, jittered by a quarter either way.
  expect(wait.waits).toHaveLength(1);
  expect(wait.waits[0]).toBeGreaterThanOrEqual(750);
  expect(wait.waits[0]).toBeLessThanOrEqual(1_250);
});

test("four no-route answers throw the message that a release is in progress", async () => {
  const stub = scriptedFetch(
    Array.from({ length: 4 }, () => () => olderNoRoute("GET", "/auth/whoami"))
  );
  const wait = recordedSleep();
  const api = createApiClient(stub.fetch, undefined, wait.sleep);

  const error = await api.whoAmI().catch((thrown: unknown) => thrown);
  expect(error).toBeInstanceOf(ApiError);
  expect(error).toMatchObject({
    code: "NOT_FOUND",
    message: NO_ROUTE_ROLLING_RELEASE_MESSAGE,
    status: 404,
  });
  expect(NO_ROUTE_ROLLING_RELEASE_MESSAGE).toBe(
    "The Dispatch process that answered does not offer this action yet, because a new release " +
      "of Dispatch is in progress. Try again in one minute."
  );
  expect(isNoRoute(error)).toBe(false);
  expect(stub.requests).toHaveLength(4);
  // Waits of 1 s, 2 s and 4 s, each jittered by a quarter either way.
  expect(wait.waits).toHaveLength(3);
  for (const [index, base] of [1_000, 2_000, 4_000].entries()) {
    expect(wait.waits[index]).toBeGreaterThanOrEqual(base * 0.75);
    expect(wait.waits[index]).toBeLessThanOrEqual(base * 1.25);
  }
});

test("a POST answered with no route is sent again with the same body", async () => {
  const stub = scriptedFetch([
    () => noRoute("POST", "/api/v2/issues"),
    () => Response.json({ key: "CORE-1" }, { status: 201 }),
  ]);
  const api = createApiClient(stub.fetch, undefined, recordedSleep().sleep);

  await api.createIssue({ project: "CORE", title: "Ship the widgets" }, { key: "write-1" });
  expect(stub.requests).toHaveLength(2);
  const [first, second] = stub.requests;
  expect(second?.path).toBe("/api/v2/issues");
  expect(second?.init?.method).toBe("POST");
  // The same intent, so the same key: no handler ran under the first attempt's no-route answer.
  expect(second?.init?.headers).toEqual(first?.init?.headers as HeadersInit);
  // The client sends its headers as pairs, and a header name is case-insensitive - Bun's own
  // Headers lower-cases every name while the DOM this suite preloads keeps the case it was given -
  // so the names are folded here rather than written as one runtime spells them.
  const headersOf = (init: RequestInit | undefined): Record<string, string> =>
    Object.fromEntries(
      [...new Headers(init?.headers).entries()].map(([name, value]) => [name.toLowerCase(), value])
    );
  expect(headersOf(second?.init)).toMatchObject({
    "content-type": "application/json",
    "idempotency-key": "write-1",
  });
  expect(JSON.parse(String(second?.init?.body))).toEqual({
    project: "CORE",
    title: "Ship the widgets",
  });
  expect(second?.init?.body).toBe(first?.init?.body);
});

// Mid-roll, the older build answers the page's `/api/v2` path as it answers every path it serves
// no route for. Read as a refusal, every page would fail for as long as the roll lasts; it is the
// same no-route answer, so it is sent again, as `NO_ROUTE` is.
test("an /api/v2 path a release before /api/v2 answers with no route is sent again", async () => {
  const stub = scriptedFetch([
    () => olderNoRoute("PUT", "/api/v2/asks/ask-1/state"),
    () => Response.json({ id: "ask-1", state: "answered" }),
  ]);
  const api = createApiClient(stub.fetch, undefined, recordedSleep().sleep);

  expect(
    await api.answerAsk(
      "ask-1",
      { expected_edited_at: null, selected: ["Ship"] },
      { key: "answer-1" }
    )
  ).toMatchObject({ id: "ask-1", state: "answered" });
  expect(stub.requests.map(({ init, path }) => [init?.method, path])).toEqual([
    ["PUT", "/api/v2/asks/ask-1/state"],
    ["PUT", "/api/v2/asks/ask-1/state"],
  ]);
  expect(
    isNoRoute(new ApiError(404, { code: "NO_ROUTE", error: "no route for GET /api/v2/x" }))
  ).toBe(true);
  expect(
    isNoRoute(new ApiError(404, { code: "NOT_FOUND", error: "no route for GET /api/v2/x" }))
  ).toBe(true);
});

test("a 404 for an object Dispatch does not hold is not sent again", async () => {
  for (const refusal of [
    { code: "ISSUE_NOT_FOUND", error: "issue CORE-1 not found" },
    // A release before `/api/v2` says it of every object with the one code, in its own words.
    { code: "NOT_FOUND", error: "issue not found" },
  ]) {
    const stub = scriptedFetch([() => Response.json(refusal, { status: 404 })]);
    const wait = recordedSleep();
    const api = createApiClient(stub.fetch, undefined, wait.sleep);

    await expect(api.getIssue("CORE-1")).rejects.toMatchObject({
      code: refusal.code,
      message: refusal.error,
      status: 404,
    });
    expect(stub.requests).toHaveLength(1);
    expect(wait.waits).toEqual([]);
  }
});

test("a request aborted during a no-route wait rejects at once with the abort and is not sent again", async () => {
  const stub = scriptedFetch([
    () => olderNoRoute("GET", "/api/github/rest/user"),
    () => Response.json({ login: "alice" }),
  ]);
  const controller = new AbortController();
  const reason = new Error("a newer read replaced this one");
  // The shared wait, with the caller's abort arriving once the wait has begun.
  const wait: Sleep = (ms, signal) => {
    const waiting = sleep(ms, signal);
    controller.abort(reason);
    return waiting;
  };
  // `githubRest` hands the caller's init to the one request path every method shares, so the
  // caller's signal is the one the wait is given.
  const api = createApiClient(stub.fetch, undefined, wait);

  await expect(api.githubRest("user", { signal: controller.signal })).rejects.toBe(reason);
  expect(stub.requests).toHaveLength(1);
});

/** An issue's comments route holding `held` comments, paged at its default of 50 as `/api/v2`
 *  pages a list: each page is the rows from the request's `offset`, beside a `total` of `counted`
 *  - the rows held, unless a test has the count and the rows disagree, as they do when rows go
 *  while the list is read. Records the path and query of each request. */
function pagedComments(
  held: number,
  counted = held
): { asked: string[]; fetch: FetchImplementation } {
  const asked: string[] = [];
  return {
    asked,
    fetch: async (input) => {
      const url = new URL(input.toString(), "http://dispatch.test");
      asked.push(`${url.pathname}${url.search}`);
      const offset = Number(url.searchParams.get("offset") ?? 0);
      const items = Array.from({ length: Math.max(0, Math.min(50, held - offset)) }, (_, at) => ({
        id: `comment-${offset + at}`,
      }));
      return Response.json({ items, limit: 50, offset, total: counted });
    },
  };
}

// Decision 15: a list the page reads whole is read at the route's own default limit - no `limit`
// asked for, so 50 comments a page - each page starting where the rows read so far end.
test("a whole list is read a page of the route's default at a time, every row in order", async () => {
  const stub = pagedComments(120);
  const api = createApiClient(stub.fetch);

  const comments = await api.listComments("CORE-1");

  expect(stub.asked).toEqual([
    "/api/v2/issues/CORE-1/comments",
    "/api/v2/issues/CORE-1/comments?offset=50",
    "/api/v2/issues/CORE-1/comments?offset=100",
  ]);
  expect(comments.map(({ id }) => id)).toEqual(
    Array.from({ length: 120 }, (_, at) => `comment-${at}`)
  );
});

// A list whose rows fill its last page exactly holds every row once the total is read: one more
// request would only fetch an empty page.
test("a whole list stops once it holds the route's total", async () => {
  const stub = pagedComments(100);
  const api = createApiClient(stub.fetch);

  expect(await api.listComments("CORE-1")).toHaveLength(100);
  expect(stub.asked).toEqual([
    "/api/v2/issues/CORE-1/comments",
    "/api/v2/issues/CORE-1/comments?offset=50",
  ]);
});

// Rows can go while a list is read, so the total the first page counted may never be reached: a
// page shorter than the limit is the list's end, whatever the count said, and the read stops
// there rather than asking for pages that hold nothing.
test("a whole list stops at a page shorter than the limit", async () => {
  const stub = pagedComments(80, 200);
  const api = createApiClient(stub.fetch);

  expect(await api.listComments("CORE-1")).toHaveLength(80);
  expect(stub.asked).toEqual([
    "/api/v2/issues/CORE-1/comments",
    "/api/v2/issues/CORE-1/comments?offset=50",
  ]);
});
