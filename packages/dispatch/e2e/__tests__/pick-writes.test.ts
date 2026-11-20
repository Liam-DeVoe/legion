import { expect, spyOn, test } from "bun:test";

import type { Page, Route } from "@playwright/test";

import { holdIssuePatches, PICK_HOLD_MS } from "../pick-writes";

// The picker rows read each save's hold with `Date.now()`, the wall clock, and assert it lasted at
// least `PICK_HOLD_MS`; a timer counts on the monotonic clock. Merge-group runs 44424277368,
// 44632529216 and 44650607607 read a hold of 2,999 ms there and failed board.e2e.ts's lane-drag
// row on its first attempt. Here the wall clock runs 5 per cent slow against the timers, far more
// than a runner slews it, so a hold that only sleeps `PICK_HOLD_MS` reads short every time: the
// hold must last until `Date.now()` itself has moved that far. The row waits real time, since the
// timer is the thing under test: about 3.2 s.
test("a held save lasts PICK_HOLD_MS by Date.now(), however slow the wall clock runs", async () => {
  let handler: ((route: Route) => Promise<void>) | undefined;
  const page = {
    route: async (_pattern: string, handle: (route: Route) => Promise<void>) => {
      handler = handle;
    },
  } as unknown as Page;
  let fallbacks = 0;
  const route = {
    fallback: async () => {
      fallbacks += 1;
    },
    request: () => ({ method: () => "PATCH", postDataJSON: () => ({ priority: 1 }) }),
  } as unknown as Route;

  const wallStart = Date.now();
  const monotonicStart = performance.now();
  const slowWall = spyOn(Date, "now").mockImplementation(() =>
    Math.floor(wallStart + (performance.now() - monotonicStart) * 0.95)
  );
  try {
    const held = await holdIssuePatches(page, "CORE-1");
    held.release();
    if (handler === undefined) throw new Error("holdIssuePatches routed nothing");
    await handler(route);

    const [patch] = held.sent;
    expect(held.sent).toHaveLength(1);
    expect(
      (patch?.endedAt ?? Number.NaN) - (patch?.arrivedAt ?? Number.NaN)
    ).toBeGreaterThanOrEqual(PICK_HOLD_MS);
    expect(fallbacks).toBe(1);
    expect(held.forwarded).toEqual([{ priority: 1 }]);
    expect(held.holding()).toBe(0);
  } finally {
    slowWall.mockRestore();
  }
});
