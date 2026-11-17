import { expect, spyOn, test } from "bun:test";
import { focusManager, QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, screen } from "@testing-library/react";

import { onFakeClock, tick } from "../../__tests__/fake-clock";
import { renderWithQueries } from "../../__tests__/query-provider";
import { api } from "../../api/client";
import { eventRefreshTargets } from "../../api/event-refresh";
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

/** The Steps tab of CORE-1, shown or not, reading through `read`; `fetchedAt` holds the fake
 *  clock's time at each read. */
function renderLiveTab(read: () => IssueSteps, shown = true) {
  const fetchedAt: number[] = [];
  const listSteps = spyOn(api, "listSteps").mockImplementation(async () => {
    fetchedAt.push(Date.now());
    return read();
  });
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const tab = (active: boolean) => <StepsTab active={active} issueKey="CORE-1" />;
  const view = renderWithQueries(tab(shown), {
    wrapper: ({ children }) => (
      <QueryClientProvider client={client}>{children}</QueryClientProvider>
    ),
  });
  return {
    deliver: () => refreshQueries(client, eventRefreshTargets(released)),
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
