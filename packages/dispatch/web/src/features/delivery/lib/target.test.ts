import { expect, test } from "bun:test";
import { meetsTarget, targetLabel } from "./target";

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

test("meetsTarget: a value at an inclusive target meets it, at a strict one misses it, and an off target judges nothing", () => {
  expect(meetsTarget(20, { value: 20, inclusive: true }, "higher")).toBe(true);
  expect(meetsTarget(20, { value: 20, inclusive: false }, "higher")).toBe(false);
  expect(meetsTarget(21, { value: 20, inclusive: false }, "higher")).toBe(true);
  expect(meetsTarget(45, { value: 45, inclusive: true }, "lower")).toBe(true);
  expect(meetsTarget(45, { value: 45, inclusive: false }, "lower")).toBe(false);
  expect(meetsTarget(44.9, { value: 45, inclusive: false }, "lower")).toBe(true);
  expect(meetsTarget(1, null, "higher")).toBeNull();
});
