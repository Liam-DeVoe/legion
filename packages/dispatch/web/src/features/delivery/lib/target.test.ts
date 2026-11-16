import { expect, test } from "bun:test";
import { judgedBy, meetsTarget, targetLabel, targetWords } from "./target";

test("targetLabel: at least or above for a measure better higher, at most or under for one better lower, off where the target is", () => {
  const minutes = (v: number) => `${v}m`;
  expect(targetLabel({ value: 20, inclusive: true }, "higher", (v) => `${v}/day`)).toBe(
    "\u2265 20/day"
  );
  expect(targetLabel({ value: 20, inclusive: false }, "higher", (v) => `${v}/day`)).toBe(
    "> 20/day"
  );
  expect(targetLabel({ value: 45, inclusive: true }, "lower", minutes)).toBe("\u2264 45m");
  expect(targetLabel({ value: 45, inclusive: false }, "lower", minutes)).toBe("< 45m");
  expect(targetLabel(null, "lower", minutes)).toBe("off");
});

test("targetWords: the same targets in words, as a chart's caption names them", () => {
  const minutes = (v: number) => `${v}m`;
  expect(targetWords({ value: 20, inclusive: true }, "higher", (v) => `${v}/day`)).toBe(
    "at least 20/day"
  );
  expect(targetWords({ value: 20, inclusive: false }, "higher", (v) => `${v}/day`)).toBe(
    "above 20/day"
  );
  expect(targetWords({ value: 45, inclusive: true }, "lower", minutes)).toBe("at most 45m");
  expect(targetWords({ value: 45, inclusive: false }, "lower", minutes)).toBe("under 45m");
  expect(targetWords(null, "lower", minutes)).toBe("off");
});

test("an inclusive target of 0 on a measure better lower reads as the value alone, since none goes below 0", () => {
  expect(targetLabel({ value: 0, inclusive: true }, "lower", (v) => `${v}`)).toBe("0");
  expect(targetWords({ value: 0, inclusive: true }, "lower", (v) => `${v}m`)).toBe("0m");
  expect(targetLabel({ value: 0, inclusive: false }, "lower", (v) => `${v}`)).toBe("< 0");
  expect(targetLabel({ value: 0, inclusive: true }, "higher", (v) => `${v}`)).toBe("\u2265 0");
});

test("meetsTarget: a value at an inclusive target meets it, at a strict one misses it, and an off target judges nothing", () => {
  expect(meetsTarget(20, { value: 20, inclusive: true }, "higher")).toBe(true);
  expect(meetsTarget(20, { value: 20, inclusive: false }, "higher")).toBe(false);
  expect(meetsTarget(21, { value: 20, inclusive: false }, "higher")).toBe(true);
  expect(meetsTarget(45, { value: 45, inclusive: true }, "lower")).toBe(true);
  expect(meetsTarget(45, { value: 45, inclusive: false }, "lower")).toBe(false);
  expect(meetsTarget(44.9, { value: 45, inclusive: false }, "lower")).toBe(true);
  expect(meetsTarget(1, null, "higher")).toBeNull();
});

test("judgedBy: a chart's day meets an inclusive target at its value, a strict one past it, and an off target judges no day", () => {
  const atMost = judgedBy({ value: 45, inclusive: true }, "lower");
  const under = judgedBy({ value: 45, inclusive: false }, "lower");
  expect([atMost?.(45), atMost?.(46)]).toEqual([true, false]);
  expect([under?.(45), under?.(44.9)]).toEqual([false, true]);
  expect(judgedBy({ value: 20, inclusive: true }, "higher")?.(20)).toBe(true);
  expect(judgedBy(null, "lower")).toBeUndefined();
});
