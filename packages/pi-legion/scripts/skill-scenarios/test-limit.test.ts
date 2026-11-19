import { expect, test } from "bun:test";
import { REMOVAL_BOUND_MS } from "@legion/test-fixtures/temporary-directories";
import { ROW_MARGIN_MS, rowLimit, spawnWithin } from "./test-limit";

test("a row's limit is the bounds of what it waits for, the fixture's removal and the margin", () => {
  const parts = [
    { part: "one", boundMs: 1_000 },
    { part: "two", boundMs: 2_000 },
  ];
  expect(rowLimit(...parts)).toBe(3_000 + REMOVAL_BOUND_MS + ROW_MARGIN_MS);
  expect(rowLimit()).toBe(REMOVAL_BOUND_MS + ROW_MARGIN_MS);
});

// The process runs past its own bound, so the row fails naming the part, not at bun's limit.
test("a process past its own bound is killed, and the row fails naming its part", () => {
  expect(() => spawnWithin({ part: "a sleep of 30 s", boundMs: 1_000 }, ["sleep", "30"])).toThrow(
    "a sleep of 30 s did not end within its bound of 1000 ms"
  );
});

test("a process that ends within its bound answers as Bun.spawnSync does", () => {
  const result = spawnWithin({ part: "echo", boundMs: 10_000 }, ["echo", "here"]);
  expect({ code: result.exitCode, out: result.stdout.toString() }).toEqual({
    code: 0,
    out: "here\n",
  });
});
