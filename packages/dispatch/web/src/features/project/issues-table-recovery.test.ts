import { afterEach, expect, test } from "bun:test";
import { readLayout, readStore } from "../../components/table-storage";
import { ISSUE_TABLE_COLUMNS, tableStorageKey } from "./issues-table-model";
import { readTableAction, type TableAction } from "./issues-table-recovery";

afterEach(() => localStorage.clear());

const LAYOUT = { sizing: { title: 360 }, order: ["status", "key"], visibility: { labels: false } };

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
