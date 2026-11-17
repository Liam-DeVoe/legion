import { beforeEach, expect, test } from "bun:test";
import { daemonWaitBegan, daemonWaitMs, forgetDaemonWaitsForTests } from "./daemon-wait";

const MINUTE = 60_000;
/** A wall-clock moment to count from, so every number a failure prints is a difference. */
const START = 1_793_000_000_000;

beforeEach(() => {
  forgetDaemonWaitsForTests();
});

test("a call that waited for no daemon has waited no time at all", () => {
  expect(daemonWaitMs("toolu_1", START, START + 60 * MINUTE)).toBe(0);
});

test("only the part of a wait inside the window asked about is counted", () => {
  daemonWaitBegan("toolu_1", START + 10 * MINUTE)(START + 40 * MINUTE);

  expect(daemonWaitMs("toolu_1", START, START + 60 * MINUTE)).toBe(30 * MINUTE);
  // A window that starts after the wait had begun counts only from its own start.
  expect(daemonWaitMs("toolu_1", START + 20 * MINUTE, START + 60 * MINUTE)).toBe(20 * MINUTE);
  // A window that ends inside the wait counts up to its own end.
  expect(daemonWaitMs("toolu_1", START, START + 20 * MINUTE)).toBe(10 * MINUTE);
  // A window beside the wait counts nothing.
  expect(daemonWaitMs("toolu_1", START + 40 * MINUTE, START + 60 * MINUTE)).toBe(0);
});

// The wait is the waiting call's alone. A stuck shell call in the same turn, while a `legion`
// call waits out the daemon beside it, is stuck for its own reasons, and the watch must still
// reach it; so is any call open while a wait no tool call owns runs in the background — the
// boot's, a role regain's `claims/ready`, a turn's outcome.
test("a wait another call recorded is no wait of this one", () => {
  daemonWaitBegan("toolu_legion", START)(START + 50 * MINUTE);

  expect(daemonWaitMs("toolu_legion", START, START + 60 * MINUTE)).toBe(50 * MINUTE);
  expect(daemonWaitMs("toolu_bash", START, START + 60 * MINUTE)).toBe(0);
});

// One call can wait more than once — `sign_off` mints a grant and then signs off, and each is
// retried in its own wait — and two waits of one call that overlap stopped it once, not twice.
test("two waits of one call that overlap are counted once", () => {
  daemonWaitBegan("toolu_1", START)(START + 30 * MINUTE);
  daemonWaitBegan("toolu_1", START + 10 * MINUTE)(START + 50 * MINUTE);

  expect(daemonWaitMs("toolu_1", START, START + 60 * MINUTE)).toBe(50 * MINUTE);
});

test("a wait that has not ended counts up to the moment asked about", () => {
  daemonWaitBegan("toolu_1", START + 5 * MINUTE);

  expect(daemonWaitMs("toolu_1", START, START + 20 * MINUTE)).toBe(15 * MINUTE);
  expect(daemonWaitMs("toolu_1", START, START + 65 * MINUTE)).toBe(60 * MINUTE);
});

// The record is bounded, since nothing clears it for the life of the process. What it may drop
// is an old wait that ended; a wait still running is what a call open now is waiting on.
test("the record keeps the newest waits and never drops one that has not ended", () => {
  daemonWaitBegan("toolu_open", START);
  for (let i = 1; i <= 40; i++) {
    daemonWaitBegan(`toolu_${i}`, START + i * MINUTE)(START + i * MINUTE + 30_000);
  }

  // The open wait is still there: the whole window is inside it.
  expect(daemonWaitMs("toolu_open", START, START + 60 * MINUTE)).toBe(60 * MINUTE);
});
