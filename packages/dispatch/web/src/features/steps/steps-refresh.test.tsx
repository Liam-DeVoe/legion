import { expect, spyOn, test } from "bun:test";
import { focusManager, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen } from "@testing-library/react";

import { onFakeClock, tick } from "../../__tests__/fake-clock";
import { ApiError, api } from "../../api/client";
import { eventRefreshTargets } from "../../api/event-refresh";
import { DispatchQueryClient, queryRetry, queryRetryDelay } from "../../api/query-client";
import { refreshQueries } from "../../api/query-refresh";
import type { DispatchEvent, IssueSteps } from "../../api/types";
import { StepsTab } from "./StepsTab";
import { stepRecord } from "./step-record-fixture";

// A step record leaves the daemon on a lane of its own, which neither waits for the issue's events
// nor sends one, so the event that comes with a phase moving can reach the page before its
// record. After each start - such an event, the tab shown, the page shown again - the shown tab
// reads at once and then 2, 5, 15, 45, 120 and 300 seconds later, and nothing more until the next
// start. Every row here runs on the fake clock, and each event takes the feed's own path
// (`eventRefreshTargets`, then the scheduler).

const NO_STEPS: IssueSteps = { issue: "CORE-1", tasks: [] };

/** The release that ends a role's run, as Legion's daemon writes it. */
const released: DispatchEvent = {
  actor: { id: "legion-daemon:CORE", kind: "session" },
  created_at: "2026-11-16T09:40:00Z",
  id: 12,
  issue_key: "CORE-1",
  notify: false,
  payload: {
    claim: null,
    key: "CORE-1",
    previous_claim: {
      actor: { id: "legion-core-core-1-implementer", kind: "session" },
      at: "2026-11-16T09:00:00Z",
    },
    reason: "released",
    repo: null,
    resolved_repo: null,
    status: "in_progress",
  },
  project: "CORE",
  seq: 4,
  type: "issue.released",
};

/** The Steps tab of CORE-1, shown or not, reading through `read`, which may hold its answer;
 *  `fetchedAt` holds the fake clock's time at each read. The client is the page's own, with its
 *  retry policy: a failed read is asked again twice, 500 ms apart, where nothing stops it
 *  (`main.tsx`). */
function renderLiveTab(read: () => IssueSteps | Promise<IssueSteps>, shown = true) {
  const fetchedAt: number[] = [];
  const listSteps = spyOn(api, "listSteps").mockImplementation(async () => {
    fetchedAt.push(Date.now());
    return read();
  });
  const client = new DispatchQueryClient({
    defaultOptions: { queries: { retry: queryRetry, retryDelay: queryRetryDelay } },
  });
  const tab = (active: boolean) => (
    <QueryClientProvider client={client}>
      <StepsTab active={active} issueKey="CORE-1" />
    </QueryClientProvider>
  );
  const view = render(tab(shown));
  return {
    deliver: () => refreshQueries(client, eventRefreshTargets(released)),
    /** The feed's refresh of the whole cache after a gap, which names no target. */
    deliverAll: () => refreshQueries(client),
    fetchedAt,
    listSteps,
    restore: () => {
      view.unmount();
      client.clear();
      listSteps.mockRestore();
    },
    show: (active: boolean) => view.rerender(tab(active)),
  };
}

test("a record written 30 seconds after its event shows with no other event and no reload", async () => {
  await onFakeClock(async () => {
    let answer = NO_STEPS;
    const tab = renderLiveTab(() => answer);
    try {
      // The catch-up of the tab being shown runs out first, so only the event's reads follow.
      await tick(301_000, 1_000);
      tab.deliver();
      await tick(0);
      expect(screen.getByRole("region", { name: "Steps empty state" })).toBeDefined();

      await tick(30_000, 1_000);
      answer = {
        issue: "CORE-1",
        tasks: [{ steps: [stepRecord({ id: "late-run" })], task: "default" }],
      };
      await tick(14_000, 1_000);
      expect(screen.queryByRole("table")).toBeNull();
      await tick(1_000);
      expect(screen.getByRole("table", { name: "Steps of task default" })).toBeDefined();
      expect(screen.getByRole("cell", { name: "passed" })).toBeDefined();
    } finally {
      tab.restore();
    }
  });
});

test("one event with nothing new makes seven reads in ten minutes and none after the 300th second", async () => {
  await onFakeClock(async () => {
    const tab = renderLiveTab(() => NO_STEPS);
    try {
      await tick(301_000, 1_000);
      const before = tab.listSteps.mock.calls.length;
      const start = Date.now();
      tab.deliver();
      await tick(300_000, 1_000);
      expect(tab.fetchedAt.slice(before).map((at) => at - start)).toEqual([
        0, 2_000, 5_000, 15_000, 45_000, 120_000, 300_000,
      ]);
      await tick(300_000, 1_000);
      expect(tab.listSteps).toHaveBeenCalledTimes(before + 7);
    } finally {
      tab.restore();
    }
  });
});

// After a gap in the feed the page refreshes its whole cache, since no event says what moved. The
// Steps query never fetches on its own, so the tab hears that refresh as it hears an event's, and
// catches up.
test("a refresh of the whole cache, as after a gap in the feed, starts a catch-up", async () => {
  await onFakeClock(async () => {
    const tab = renderLiveTab(() => NO_STEPS);
    try {
      await tick(301_000, 1_000);
      const before = tab.listSteps.mock.calls.length;
      const start = Date.now();
      tab.deliverAll();
      await tick(300_000, 1_000);
      expect(tab.fetchedAt.slice(before).map((at) => at - start)).toEqual([
        0, 2_000, 5_000, 15_000, 45_000, 120_000, 300_000,
      ]);
    } finally {
      tab.restore();
    }
  });
});

test("a second event during a catch-up stops the first list and runs its own whole", async () => {
  await onFakeClock(async () => {
    const tab = renderLiveTab(() => NO_STEPS);
    try {
      await tick(301_000, 1_000);
      const before = tab.listSteps.mock.calls.length;
      const firstAt = Date.now();
      tab.deliver();
      await tick(8_000, 1_000);
      const secondAt = Date.now();
      tab.deliver();
      await tick(600_000, 1_000);
      const reads = tab.fetchedAt.slice(before);
      // The first list read at 0, 2 and 5 s; its 15, 45, 120 and 300 s reads never come.
      expect(reads.filter((at) => at < secondAt).map((at) => at - firstAt)).toEqual([
        0, 2_000, 5_000,
      ]);
      expect(reads.filter((at) => at >= secondAt).map((at) => at - secondAt)).toEqual([
        0, 2_000, 5_000, 15_000, 45_000, 120_000, 300_000,
      ]);
    } finally {
      tab.restore();
    }
  });
});

// The tab's reads keep the event refresh's bound of one read of a query a second
// (`MIN_REFETCH_INTERVAL_MS`) however its starts fall: a start inside the second after a read
// reads when that second ends, and its own list counts from the start. The feed sends a batch
// every 100 ms at most (`BATCH_WINDOW_MS` in `api/event-feed.ts`).
test("two events 100 ms apart read once, and again when the second ends with what the second brought", async () => {
  await onFakeClock(async () => {
    let answer = NO_STEPS;
    const tab = renderLiveTab(() => answer);
    try {
      await tick(301_000, 1_000);
      const before = tab.fetchedAt.length;
      const firstAt = Date.now();
      tab.deliver();
      await tick(100, 100);
      answer = {
        issue: "CORE-1",
        tasks: [{ steps: [stepRecord({ id: "second-run" })], task: "default" }],
      };
      const secondAt = Date.now();
      tab.deliver();
      await tick(800, 100);
      expect(tab.fetchedAt.slice(before).map((at) => at - firstAt)).toEqual([0]);
      expect(screen.queryByRole("table")).toBeNull();

      // The read when the second ends draws what the second event brought, before the second
      // list's first read at 2 s.
      await tick(200, 100);
      expect(tab.fetchedAt.slice(before).map((at) => at - firstAt)).toEqual([0, 1_000]);
      expect(screen.getByRole("table", { name: "Steps of task default" })).toBeDefined();

      await tick(300_000, 100);
      const reads = tab.fetchedAt.slice(before);
      expect(reads.slice(2).map((at) => at - secondAt)).toEqual([
        2_000, 5_000, 15_000, 45_000, 120_000, 300_000,
      ]);
    } finally {
      tab.restore();
    }
  });
});

test("an event 100 ms after a read of the catch-up reads when that read's second ends", async () => {
  await onFakeClock(async () => {
    const tab = renderLiveTab(() => NO_STEPS);
    try {
      await tick(301_000, 1_000);
      const before = tab.fetchedAt.length;
      const firstAt = Date.now();
      tab.deliver();
      await tick(2_100, 100);
      const secondAt = Date.now();
      tab.deliver();
      await tick(302_000, 100);
      const reads = tab.fetchedAt.slice(before);
      expect(reads.slice(0, 3).map((at) => at - firstAt)).toEqual([0, 2_000, 3_000]);
      expect(reads.slice(3).map((at) => at - secondAt)).toEqual([
        2_000, 5_000, 15_000, 45_000, 120_000, 300_000,
      ]);
    } finally {
      tab.restore();
    }
  });
});

// A read still out when the next read of the list comes due is neither cancelled nor doubled:
// its answer lands and is drawn, and the read that came due follows it, so what the records hold
// after that deadline is read too.
test("a read still out at the next deadline lands and is drawn, and the deadline's read follows it", async () => {
  await onFakeClock(async () => {
    const held = Promise.withResolvers<IssueSteps>();
    let read: () => IssueSteps | Promise<IssueSteps> = () => NO_STEPS;
    const tab = renderLiveTab(() => read());
    try {
      await tick(301_000, 1_000);
      const before = tab.fetchedAt.length;
      const firstAt = Date.now();
      read = () => held.promise;
      tab.deliver();
      await tick(3_000, 100);
      expect(tab.fetchedAt.slice(before).map((at) => at - firstAt)).toEqual([0]);
      expect(screen.getByRole("region", { name: "Steps empty state" })).toBeDefined();

      read = () => ({
        issue: "CORE-1",
        tasks: [{ steps: [stepRecord({ id: "implement-1" })], task: "default" }],
      });
      const landedAt = Date.now();
      held.resolve({
        issue: "CORE-1",
        tasks: [
          {
            steps: [stepRecord({ ended_at: null, id: "implement-1", result: "" })],
            task: "default",
          },
        ],
      });
      await tick(0);
      expect(screen.getByRole("cell", { name: "running" })).toBeDefined();

      await tick(100, 100);
      const reads = tab.fetchedAt.slice(before);
      expect(reads).toHaveLength(2);
      expect(reads[1]).toBeGreaterThanOrEqual(landedAt);
      expect(screen.getByRole("cell", { name: "passed" })).toBeDefined();
    } finally {
      held.resolve(NO_STEPS);
      tab.restore();
    }
  });
});

// A timer of the list only ever reads, so ten minutes with no read - twice the list's length -
// is ten minutes with none of its timers held. (`jest.getTimerCount()` cannot say it: one timer
// that is not the tab's comes and goes with the renders, the tab never shown included.)
test("a tab not shown and a hidden page read nothing and hold no timer, and each catches up once shown", async () => {
  await onFakeClock(async () => {
    const tab = renderLiveTab(() => NO_STEPS, false);
    try {
      // Not shown: no read, and an event starts none.
      await tick(0);
      tab.deliver();
      await tick(600_000, 1_000);
      expect(tab.listSteps).not.toHaveBeenCalled();

      // Shown: one catch-up.
      tab.show(true);
      const shownAt = Date.now();
      await tick(600_000, 1_000);
      expect(tab.fetchedAt.map((at) => at - shownAt)).toEqual([
        0, 2_000, 5_000, 15_000, 45_000, 120_000, 300_000,
      ]);

      // A catch-up the page is hidden in stops, and an event while hidden starts none.
      tab.deliver();
      await tick(3_000, 1_000);
      const beforeHiding = tab.listSteps.mock.calls.length;
      act(() => focusManager.setFocused(false));
      tab.deliver();
      await tick(600_000, 1_000);
      expect(tab.listSteps).toHaveBeenCalledTimes(beforeHiding);

      // The page shown again: one catch-up.
      act(() => focusManager.setFocused(true));
      const visibleAt = Date.now();
      await tick(600_000, 1_000);
      expect(tab.fetchedAt.slice(beforeHiding).map((at) => at - visibleAt)).toEqual([
        0, 2_000, 5_000, 15_000, 45_000, 120_000, 300_000,
      ]);
    } finally {
      tab.restore();
      focusManager.setFocused(undefined);
    }
  });
});

// The tab's reads are its catch-up's and Retry's alone: a read that failed is asked again by the
// catch-up's next read, never by the query on its own, so a tab the reader left - still mounted,
// as the issue page keeps it - has no request left to make.
test("a tab left after a failed read reads nothing more, and reads at once when shown again", async () => {
  await onFakeClock(async () => {
    let failing = true;
    const tab = renderLiveTab(() => {
      if (failing) {
        throw new ApiError(503, { code: "DATABASE_BUSY", error: "the database is busy" });
      }
      return NO_STEPS;
    });
    try {
      await tick(0);
      expect(tab.listSteps).toHaveBeenCalledTimes(1);
      await tick(100);
      tab.show(false);
      await tick(600_000, 1_000);
      expect(tab.listSteps).toHaveBeenCalledTimes(1);

      failing = false;
      tab.show(true);
      await tick(0);
      expect(tab.listSteps).toHaveBeenCalledTimes(2);
      expect(screen.getByRole("region", { name: "Steps empty state" })).toBeDefined();
    } finally {
      tab.restore();
    }
  });
});
