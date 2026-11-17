// packages/envoy-client/src/__tests__/dispatch-credential-deadline.test.ts
//
// A Dispatch call hands its credential the client's own deadline less the request's reserve
// (`credentialDeadline`), as an Envoy call hands it its attempt's
// (transport-credential-deadline.test.ts). A broker credential whose renewal the broker leaves
// unanswered then answers with the live token in hand at the end of its share, and the request
// still has its reserve to be sent and answered, rather than going out on a deadline the
// credential has already spent.

import { expect, test } from "bun:test";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import { brokerCredential } from "../broker-credential";
import { DispatchClient } from "../dispatch-http";
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
