// packages/envoy-client/src/__tests__/credential-deadline.test.ts
//
// A request's credential waits on the request's time less a reserve for the request itself,
// `min(1 s, deadlineMs / 2)` (`credentialDeadline`): a broker credential that hands over its live
// token when its wait ends leaves the request that reserve to be sent and answered, and the
// credential's wait ends no later than the request's.

import { expect, test } from "bun:test";
import { credentialDeadline } from "../bearer";

/** How long after its making `signal` aborted. */
async function abortedAfter(signal: AbortSignal): Promise<number> {
  const started = Date.now();
  await new Promise<void>((resolve) => signal.addEventListener("abort", () => resolve()));
  return Date.now() - started;
}

test("a request of 3 s leaves itself a 1 s reserve: the credential's wait ends at 2 s", async () => {
  const deadline = AbortSignal.timeout(3_000);
  const at = await abortedAfter(credentialDeadline(deadline, 3_000));
  expect(at).toBeGreaterThanOrEqual(1_900);
  expect(at).toBeLessThan(2_600);
  expect(deadline.aborted).toBe(false);
});

test("a request shorter than 2 s leaves itself half: the credential's wait ends at 200 ms of 400", async () => {
  const deadline = AbortSignal.timeout(400);
  const at = await abortedAfter(credentialDeadline(deadline, 400));
  expect(at).toBeGreaterThanOrEqual(150);
  expect(at).toBeLessThan(350);
  expect(deadline.aborted).toBe(false);
});

test("the credential's wait ends at once where the request's has", () => {
  const ended = new AbortController();
  ended.abort(new Error("the request's own deadline"));
  const waited = credentialDeadline(ended.signal, 60_000);
  expect(waited.aborted).toBe(true);
  expect((waited.reason as Error).message).toBe("the request's own deadline");
});
