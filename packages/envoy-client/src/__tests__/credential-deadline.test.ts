// packages/envoy-client/src/__tests__/credential-deadline.test.ts
//
// A request's credential waits on the first half of the request's time (`credentialDeadline`):
// a broker credential that hands over its live token when its wait ends leaves the request the
// other half to be sent and answered, and the credential's wait ends no later than the request's.

import { expect, test } from "bun:test";
import { credentialDeadline } from "../bearer";

test("the credential's wait ends at half the request's time, while the request's runs on", async () => {
  const deadline = AbortSignal.timeout(400);
  const waited = credentialDeadline(deadline, 400);
  const started = Date.now();
  await new Promise<void>((resolve) => waited.addEventListener("abort", () => resolve()));
  const at = Date.now() - started;
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
