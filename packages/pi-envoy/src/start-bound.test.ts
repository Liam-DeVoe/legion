import { expect, test } from "bun:test";
import { PASS_REST_MS, startTradeBoundMs } from "./start-bound";

test("the start waits its pass less the rest of the pass, where the rest fits", () => {
  // The default 30 s pass leaves the start 20 s, the trade deadline itself.
  expect(startTradeBoundMs(30_000)).toBe(20_000);
  // A longer pass leaves more; `ready` still waits no longer than its trade deadline.
  expect(startTradeBoundMs(90_000)).toBe(90_000 - PASS_REST_MS);
  expect(startTradeBoundMs(20_000)).toBe(10_000);
});

test("a pass too short for the rest gives the start half of it, never 0 ms", () => {
  // ENVOY_HEARTBEAT_MS=5000 ENVOY_PASS_DEADLINE_MS=7500: before, 7.5 s less 10 s left 0 ms, and
  // the start failed "within 0 s" at once (the review of dc8dc9ea7a, F3).
  expect(startTradeBoundMs(7_500)).toBe(3_750);
  expect(startTradeBoundMs(1_000)).toBe(500);
  for (const pass of [1, 2_500, 7_500, 15_000, 19_999]) {
    expect(startTradeBoundMs(pass)).toBeGreaterThan(0);
  }
});
