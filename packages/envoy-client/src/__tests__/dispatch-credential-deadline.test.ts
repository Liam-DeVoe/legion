// packages/envoy-client/src/__tests__/dispatch-credential-deadline.test.ts
//
// A Dispatch call hands its credential the client's own deadline less the request's reserve
// (`credentialDeadline`), as an Envoy call hands it its attempt's
// (transport-credential-deadline.test.ts). A broker credential whose renewal the broker leaves
// unanswered then answers with the live token in hand at the end of its share, and the request
// still has its reserve to be sent and answered, rather than going out on a deadline the
// credential has already spent.

import { expect, test } from "bun:test";
import {
  RegistrationPendingError,
  TradeRetryingError,
  TradeUnansweredError,
} from "@legion/broker-client/token-errors";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import { brokerCredential } from "../broker-credential";
import { DispatchClient, forgetKeyedLayerMemory } from "../dispatch-http";
import { brokerStandIns } from "./broker-stand-ins";

const { podEnvironment, servers, stubDispatch } = brokerStandIns("dispatch-credential-deadline");

/** The session the credential trades under. */
const POD_SESSION = "implementer-g1";

/** A client's own deadline, short so the row shows its share without waiting a whole minute. */
const DEADLINE_MS = 3_000;

test("inside the renewal window, a broker that hangs leaves a Dispatch call its live token and the time to send it", async () => {
  let trades = 0;
  const broker = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    fetch: async () => {
      trades += 1;
      if (trades > 1) return await new Promise<Response>(() => {});
      // Good for 200 s: inside the 5-minute renewal window from the first.
      return Response.json({
        access_token: fixtureSecret("live"),
        issued_token_type: "urn:ietf:params:oauth:token-type:access_token",
        token_type: "DPoP",
        expires_in: 200,
      });
    },
  });
  servers.push(broker);
  const dispatch = stubDispatch();
  const credential = brokerCredential(
    podEnvironment(`http://127.0.0.1:${broker.port}`, dispatch.url),
    POD_SESSION
  );
  const call = () =>
    new DispatchClient(dispatch.url, credential, fetch, { deadlineMs: DEADLINE_MS }).listAllIssues(
      {}
    );
  await call();
  const started = performance.now();
  await call();
  // The second call waited the credential's share, 2 s of the client's 3 s, and went with the
  // live token inside the reserve.
  expect(performance.now() - started).toBeLessThan(DEADLINE_MS);
  expect(trades).toBe(2);
  const live = dispatch.asked[0]?.authorization;
  expect(live).toStartWith("DPoP ");
  expect(dispatch.asked.map((asked) => asked.authorization)).toEqual([live, live]);
}, 20_000);

// R3-99b1 (the review of 99b1e438db): a write whose credential cannot present ends with the
// credential's own error, within the client's deadline, whatever that deadline is. A registration
// still pending and a trade still unanswered are told only once the credential's share of the
// deadline has ended, so the write ends there; the fail-fast 503 is presented again on the
// write's pacing, and the deadline that ends the pause ends the write with the credential's
// failure, never the pause's own "The operation timed out.". Nothing reaches Dispatch.
test.each([
  ["a registration still pending", "pending", RegistrationPendingError],
  ["a trade the broker never answers", "silent", TradeUnansweredError],
  ["a broker that answers 503", "503", TradeRetryingError],
] as const)(
  "a write whose credential meets %s ends with the credential's error inside a deadline shorter than its pacing",
  async (_, mode, failure) => {
    forgetKeyedLayerMemory();
    const broker = Bun.serve({
      port: 0,
      hostname: "127.0.0.1",
      fetch: async () => {
        if (mode === "503") return new Response("away", { status: 503 });
        if (mode === "silent") return await new Promise<Response>(() => {});
        return Response.json(
          {
            error: "invalid_grant",
            error_description: `pod_session_not_registered: No registration says this pod may write under the session name '${POD_SESSION}'. A pod writes under a session name its deployment registered for it.`,
          },
          { status: 400 }
        );
      },
    });
    servers.push(broker);
    const dispatch = stubDispatch();
    const credential = brokerCredential(
      podEnvironment(`http://127.0.0.1:${broker.port}`, dispatch.url),
      POD_SESSION
    );
    const deadlineMs = 2_500;
    const started = performance.now();

    const error = await new DispatchClient(dispatch.url, credential, fetch, { deadlineMs })
      .comment("DSP-1", { body: "Shipped.", actor: { kind: "session", id: "session-1" } })
      .then(
        () => undefined,
        (caught: unknown) => caught
      );

    expect(error).toBeInstanceOf(failure);
    expect(performance.now() - started).toBeLessThan(deadlineMs + 500);
    expect(dispatch.asked).toHaveLength(0);
  },
  20_000
);
