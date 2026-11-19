import { expect, test } from "bun:test";
import type { LegionTask } from "@legion/contracts/legion-api";
import type { ClaimCaller } from "./daemon-client";
import { createHoldsRead } from "./holds-read";

// The hosts' own tests hold the rules of an answered read (H1 to H5 in
// `packages/pi-legion/extensions/legion.test.ts` and `packages/claude-envoy/tests/
// legion-phase-hook.test.ts`). These hold the two neither host's test reaches: a call with no
// caller, which each host gates out before it calls, and a read that fails.

const caller: ClaimCaller = {
  claimToken: "legion-legion-legion-209-implementer",
  sessionId: "ses_implementer_209",
  secret: "U2VjcmV0",
  generation: 3,
};

const HOLDING: LegionTask = { task: null, finished: 0, holds: [{ role: "tester", task: 7 }] };

test("a call with no caller returns nothing and starts no read", () => {
  let reads = 0;
  const holdsRead = createHoldsRead({
    daemon: {
      task: async () => {
        reads += 1;
        return HOLDING;
      },
    },
    caller: () => undefined,
    now: () => 0,
    failed: () => undefined,
  });
  expect(holdsRead.holds()).toEqual([]);
  expect(holdsRead.holds()).toEqual([]);
  expect(reads).toBe(0);
});

test("a read that fails is handed to failed and names nothing, and the next starts a minute after it started", async () => {
  let now = Date.parse("2026-11-19T20:00:00Z");
  const reads: unknown[] = [];
  // Each read's answer, which the holds read's own handler is on before the case awaits it.
  const readings: Promise<LegionTask>[] = [];
  const failures: unknown[] = [];
  const refused = new Error("the daemon is unreachable");
  const holdsRead = createHoldsRead({
    daemon: {
      task: (input) => {
        reads.push(input);
        const reading = reads.length === 1 ? Promise.reject(refused) : Promise.resolve(HOLDING);
        readings.push(reading);
        return reading;
      },
    },
    caller: () => caller,
    now: () => now,
    failed: (error) => {
      failures.push(error);
    },
  });

  expect(holdsRead.holds()).toEqual([]);
  await readings[0]?.catch(() => undefined);
  expect(failures).toEqual([refused]);
  now += 59_999;
  expect(holdsRead.holds()).toEqual([]);
  expect(reads).toEqual([caller]);

  now += 1;
  expect(holdsRead.holds()).toEqual([]);
  expect(reads).toEqual([caller, caller]);
  await readings[1];
  expect(holdsRead.holds()).toEqual([{ role: "tester", task: 7 }]);
  expect(failures).toEqual([refused]);
});
