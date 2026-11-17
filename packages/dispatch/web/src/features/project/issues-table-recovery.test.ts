import { afterEach, expect, test } from "bun:test";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, renderHook } from "@testing-library/react";
import { createElement, type ReactNode } from "react";
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
 *  `localStorage`, as the tabs of one browser do, and the query client its writes go through. */
function tableIn(tab: string, client: QueryClient) {
  const wrapper = ({ children }: { children: ReactNode }) =>
    createElement(QueryClientProvider, { client }, children);
  const drawn = renderHook(() => useTableRecovery("CORE", () => undefined, tab), { wrapper });
  undraws.push(() => drawn.unmount());
  return drawn.result;
}

const OTHER_TAB_ACTION =
  "Another tab has not finished moving these issues. Finish or Dismiss that move first.";

// k18 Two (R14, D12): two tabs keep one record for a listing. A bulk action in a second tab while
// the first tab's move is in the store is refused with a text that names that move, and the
// first tab's record stays as it was.
test("a second tab's bulk action is refused while the first tab's record is live, and that record stays", () => {
  const client = new QueryClient();
  undraws.push(() => client.clear());
  const first = tableIn("tab-one", client);
  const second = tableIn("tab-two", client);

  act(() => {
    first.current.begin({ kind: "startNext", keys: ["CORE-1", "CORE-2"] });
  });
  const record = readTableAction("CORE");
  expect(record?.keys).toEqual(["CORE-1", "CORE-2"]);

  let begun: Begun | undefined;
  act(() => {
    begun = second.current.begin({ kind: "startNext", keys: ["CORE-3"] });
  });
  expect(readTableAction("CORE")).toEqual(record);
  expect(begun).toEqual({ kind: "refused", text: OTHER_TAB_ACTION });
  expect(record?.tab).toBe("tab-one");
  expect(first.current.current.current).toEqual(record);
  // The second tab offers to Finish or Dismiss the first tab's move, as it would after a reload.
  expect(second.current.action).toEqual(record);
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
