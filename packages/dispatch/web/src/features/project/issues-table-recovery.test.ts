import { afterEach, expect, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook, waitFor } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
import { settle } from "../../__tests__/fake-clock";
import { readLayout, readStore } from "../../components/table-storage";
import { ISSUE_TABLE_COLUMNS, tableStorageKey } from "./issues-table-model";
import {
  type Begun,
  readTableAction,
  type TableAction,
  useTableRecovery,
} from "./issues-table-recovery";

const LAYOUT = { sizing: { title: 360 }, order: ["status", "key"], visibility: { labels: false } };

const undraws: (() => void)[] = [];
afterEach(() => {
  for (const undraw of undraws.splice(0)) undraw();
  localStorage.clear();
});

/** One table's recovery in the tab `tab`. Every tab drawn in one test shares the window's
 *  `localStorage`, as the tabs of one browser do, the query client its writes go through, and
 *  `locks`, the browser's lock manager where the test gives one. */
function tableIn(tab: string, client: QueryClient, locks?: LockManager) {
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  const drawn = renderHook(() => useTableRecovery("CORE", () => undefined, tab, locks), {
    wrapper,
  });
  undraws.push(() => drawn.unmount());
  return drawn.result;
}

/** A browser's Web Locks, shared by every tab of one test, as `navigator.locks` grants them: one
 *  holder at a time per name, granted a task after the request, every other request for that
 *  name queued in the order it was made, an `ifAvailable` request answered with no lock while
 *  the name is held or asked for, a request called off through its `signal` taken out of the
 *  queue, and `query` naming each lock held. */
function browserLocks(): LockManager {
  const held = new Set<string>();
  const queues = new Map<string, (() => void)[]>();
  const grant = (name: string) => {
    if (held.has(name)) return;
    const next = queues.get(name)?.shift();
    if (next === undefined) return;
    held.add(name);
    next();
  };
  const request = (name: string, first: unknown, second?: unknown) => {
    const options = (typeof first === "function" ? {} : first) as LockOptions;
    const granted = (typeof first === "function" ? first : second) as LockGrantedCallback<unknown>;
    if (options.ifAvailable && (held.has(name) || (queues.get(name)?.length ?? 0) > 0))
      return Promise.resolve().then(() => granted(null));
    const { promise, reject, resolve } = Promise.withResolvers<unknown>();
    const run = () => {
      const done = (end: () => void) => {
        held.delete(name);
        grant(name);
        end();
      };
      void Promise.resolve()
        .then(() => granted({ mode: "exclusive", name } as Lock))
        .then(
          (value) => done(() => resolve(value)),
          (error: unknown) => done(() => reject(error))
        );
    };
    options.signal?.addEventListener("abort", () => {
      const queue = queues.get(name) ?? [];
      const at = queue.indexOf(run);
      if (at === -1) return;
      queue.splice(at, 1);
      reject(new Error("the lock request was called off"));
    });
    queues.set(name, [...(queues.get(name) ?? []), run]);
    grant(name);
    return promise;
  };
  return {
    query: async () => ({ held: [...held].map((name) => ({ name, mode: "exclusive" })) }),
    request,
  } as unknown as LockManager;
}

const OTHER_TAB_ACTION =
  "Another tab has not finished moving these issues. Finish or Dismiss that move first.";
const OWN_ACTION = "Finish or Dismiss the interrupted action before starting another one.";
const MOVING_ACTION = "Another tab is still moving these issues. Wait until that move is complete.";

// k18 Two (R14, D12): two tabs keep one record for a listing. A bulk action in a second tab while
// the first tab's move is in the store is refused with a text that names that move, and the
// first tab's record stays as it was.
test("a second tab's bulk action is refused while the first tab's record is live, and that record stays", async () => {
  const client = new QueryClient();
  undraws.push(() => client.clear());
  const first = tableIn("tab-one", client);
  const second = tableIn("tab-two", client);
  const { promise: moving, resolve: stop } = Promise.withResolvers<void>();
  undraws.push(stop);

  act(() => {
    void first.current.begin({ kind: "startNext", keys: ["CORE-1", "CORE-2"] }, () => moving);
  });
  const record = readTableAction("CORE");
  expect(record?.keys).toEqual(["CORE-1", "CORE-2"]);

  let begun: Begun<void> | undefined;
  await act(async () => {
    begun = await second.current.begin({ kind: "startNext", keys: ["CORE-3"] }, async () => {});
  });
  expect(readTableAction("CORE")).toEqual(record);
  expect(begun).toEqual({ kind: "refused", text: OTHER_TAB_ACTION });
  expect(record?.tab).toBe("tab-one");
  expect(first.current.current.current).toEqual(record);
  // The second tab takes the first tab's record into its table, which its refusal names.
  expect(second.current.action).toEqual(record);
});

// k18 (R14), the architect's decision: a second tab offers nothing that ends a move the first
// tab still runs. The tab that runs Start next holds a lock named for its record for as long as
// the move runs, and another tab offers Finish and Dismiss for that record only once no tab
// holds the lock: the move stopped with its record still kept, as a closed or crashed tab
// leaves it.
test("a second tab offers no Finish or Dismiss while the first tab's move holds its lock, and offers them once the lock goes", async () => {
  const client = new QueryClient();
  undraws.push(() => client.clear());
  const locks = browserLocks();
  const first = tableIn("tab-one", client, locks);
  const { promise: moving, resolve: stop } = Promise.withResolvers<void>();
  undraws.push(stop);
  let begun: Promise<Begun<void>> | undefined;
  act(() => {
    begun = first.current.begin({ kind: "startNext", keys: ["CORE-1", "CORE-2"] }, () => moving);
  });
  await waitFor(() => expect(readTableAction("CORE")).toBeDefined());
  const record = readTableAction("CORE");
  if (record === undefined) throw new Error("the first tab kept no record");

  // A second tab opened while the move runs reads its record, and offers nothing for it: its
  // Finish runs nothing, and its Dismiss leaves the record where it is.
  const second = tableIn("tab-two", client, locks);
  expect(second.current.action).toEqual(record);
  let resumed = false;
  await act(async () => {
    await second.current.resume(record.id, async () => {
      resumed = true;
    });
  });
  act(() => second.current.dismiss());
  // Every lock request and query the tabs made is answered and drawn before the reading.
  await act(settle);
  expect(resumed).toBe(false);
  expect(readTableAction("CORE")).toEqual(record);
  expect(second.current.leftBehind).toBeUndefined();
  expect(first.current.leftBehind).toBeUndefined();
  expect(second.current.refusal()).toBe(MOVING_ACTION);

  // The move stops with its record kept, and the browser lets the lock go: the record is left
  // behind, and every tab offers to Finish or Dismiss it.
  await act(async () => {
    stop();
    await begun;
  });
  await waitFor(() => expect(second.current.leftBehind).toEqual(record));
  expect(first.current.leftBehind).toEqual(record);
  expect(second.current.refusal()).toBe(OTHER_TAB_ACTION);
  expect(first.current.refusal()).toBe(OWN_ACTION);

  act(() => second.current.dismiss());
  await waitFor(() => expect(readTableAction("CORE")).toBeUndefined());
});

// A Finish takes up a record left behind under its lock, and stamps the record as taken up now.
// Every other tab hears of the stamp through the `storage` event, finds the lock held, and offers
// the record no more until that Finish lets the lock go.
test("a tab that finishes a record left behind holds its lock, and the other tabs stop offering it", async () => {
  const client = new QueryClient();
  undraws.push(() => client.clear());
  const locks = browserLocks();
  const left: TableAction = {
    id: "left-behind",
    kind: "startNext",
    project: "CORE",
    keys: ["CORE-1"],
    tab: "tab-closed",
    startedAt: Date.now() - 60_000,
    writes: {},
  };
  localStorage.setItem(tableStorageKey("CORE"), JSON.stringify({ ...LAYOUT, action: left }));
  const first = tableIn("tab-one", client, locks);
  const second = tableIn("tab-two", client, locks);
  await waitFor(() => expect(first.current.leftBehind).toEqual(left));
  await waitFor(() => expect(second.current.leftBehind).toEqual(left));

  const { promise: finishing, resolve: finished } = Promise.withResolvers<void>();
  undraws.push(finished);
  let resumed: Promise<unknown> | undefined;
  act(() => {
    resumed = first.current.resume(left.id, () => finishing);
  });
  await waitFor(() =>
    expect(readTableAction("CORE")?.startedAt).toBeGreaterThan(left.startedAt ?? 0)
  );
  const key = tableStorageKey("CORE");
  act(() => {
    window.dispatchEvent(new StorageEvent("storage", { key, newValue: localStorage.getItem(key) }));
  });
  await waitFor(() => expect(second.current.leftBehind).toBeUndefined());
  expect(second.current.refusal()).toBe(MOVING_ACTION);

  // That Finish stops with the record still kept: the record is left behind again.
  await act(async () => {
    finished();
    await resumed;
  });
  await waitFor(() => expect(second.current.leftBehind?.id).toBe(left.id));
});

// D12: the table hears another tab's record, and its end, through the window's `storage` event,
// which reaches every tab but the one that wrote.
test("a storage event brings another tab's record into the table, and then its end", () => {
  const client = new QueryClient();
  undraws.push(() => client.clear());
  const table = tableIn("tab-two", client);
  expect(table.current.action).toBeUndefined();
  const key = tableStorageKey("CORE");
  const elsewhere: TableAction = {
    id: "elsewhere",
    kind: "startNext",
    project: "CORE",
    keys: ["CORE-1"],
    tab: "tab-one",
    startedAt: Date.now(),
    writes: {},
  };
  const written = (value: object) => {
    localStorage.setItem(key, JSON.stringify(value));
    act(() => {
      window.dispatchEvent(new StorageEvent("storage", { key, newValue: JSON.stringify(value) }));
    });
  };

  written({ ...LAYOUT, action: elsewhere });
  expect(table.current.action).toEqual(elsewhere);
  expect(table.current.refusal()).toBe(OTHER_TAB_ACTION);

  written(LAYOUT);
  expect(table.current.action).toBeUndefined();
  expect(table.current.refusal()).toBeUndefined();
});

// D11: the epic tools are gone, and with them the two kinds of action they recorded. A record of
// one that the build before left in the store is dropped when it is read. The reader's layout,
// which the same key holds, stays as it was.
test.each([
  "newEpic",
  "merge",
])("a stored %s action is dropped when it is read, and the layout beside it stays", (kind) => {
  const key = tableStorageKey("CORE");
  localStorage.setItem(
    key,
    JSON.stringify({
      ...LAYOUT,
      action: {
        id: "left-behind",
        kind,
        project: "CORE",
        keys: ["CORE-3"],
        source: "CORE-1",
        target: "CORE-2",
        startedAt: Date.now(),
        writes: {},
      },
    })
  );
  expect(readTableAction("CORE")).toBeUndefined();
  expect(readStore(key)).toEqual(LAYOUT);
  expect(readLayout(key, ISSUE_TABLE_COLUMNS)).toEqual({
    sizing: { title: 360 },
    order: ["status", "key"],
    visibility: { updated: false, closed: false, labels: false },
  });
});

// The drop is of the removed kinds alone: a Start next a reload cut short is still offered.
test("a stored Start next action is still read back, and stays in the store", () => {
  const key = tableStorageKey("CORE");
  const action: TableAction = {
    id: "unfinished",
    kind: "startNext",
    project: "CORE",
    keys: ["CORE-1"],
    startedAt: Date.now(),
    writes: {},
  };
  localStorage.setItem(key, JSON.stringify({ ...LAYOUT, action }));
  expect(readTableAction("CORE")).toEqual(action);
  expect(readStore(key)).toEqual({ ...LAYOUT, action });
});
