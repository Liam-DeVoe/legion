// packages/envoy-client/src/__tests__/transport-credential-deadline.test.ts
//
// An Envoy call hands its credential the call's own deadline, so a credential that is still
// waiting (a pod whose session's registration has not landed) fails the call at that deadline
// instead of holding it. broker-credential-registration.test.ts covers what the broker credential
// does with the deadline; this covers that the transport gives it one, and that a real broker
// credential fails fast where its broker cannot answer, so a listener that asks for no credential
// keeps its calls (`transport.ts`, `attemptOnce`).

import { expect, test } from "bun:test";
import { brokerCredential } from "../broker-credential";
import { createEnvoyClient } from "../transport";
import { brokerStandIns } from "./broker-stand-ins";

const { podEnvironment, servers } = brokerStandIns("transport-credential-deadline");

/** The session the credential's refusal names. */
const POD_SESSION = "implementer-g1";

/** Turns of the event loop until `ready()` holds: no timer, so the test runs at the client's pace. */
async function until(ready: () => boolean): Promise<void> {
  while (!ready()) await new Promise((resolve) => setImmediate(resolve));
}

test("an Envoy call presents its own deadline to the credential, and fails with what the credential said at it", async () => {
  const fetched: string[] = [];
  const given: (AbortSignal | undefined)[] = [];
  const client = createEnvoyClient({
    baseUrl: "http://listener.test",
    retry: false,
    timeoutMs: 20,
    fetch: async (url) => {
      fetched.push(String(url));
      return Response.json({});
    },
    credential: {
      scheme: "DPoP",
      headers: (_method, _url, _session, signal) => {
        given.push(signal);
        const { promise, reject } = Promise.withResolvers<Readonly<Record<string, string>>>();
        signal?.addEventListener("abort", () => reject(new Error(`pending: ${POD_SESSION}`)), {
          once: true,
        });
        return promise;
      },
      stale: () => {},
      secret: () => "",
    },
  });
  const call = client.getRole("implementer");
  await until(() => given.length === 1);
  expect(given[0], "the call handed its credential no deadline").toBeInstanceOf(AbortSignal);
  // The transport's own deadline timer ends the signal; the transport takes no clock of a test's.
  const deadline = given[0] as AbortSignal;
  expect(deadline.aborted).toBe(false);
  await expect(call).rejects.toThrow(`pending: ${POD_SESSION}`);
  expect(deadline.aborted).toBe(true);
  // The call asked the listener nothing: past its deadline there is nothing to send.
  expect(fetched).toEqual([]);
});

test.each([
  ["refuses connections", (): string => "http://127.0.0.1:1"],
  [
    "answers 503",
    (): string => {
      const away = Bun.serve({
        port: 0,
        hostname: "127.0.0.1",
        fetch: () => new Response("the broker is away", { status: 503 }),
      });
      servers.push(away);
      return `http://127.0.0.1:${away.port}`;
    },
  ],
])("a listener that asks for no credential keeps its calls while the broker %s", async (_, broker) => {
  // A pod with no Envoy bearer presents its broker credential on every listener call. The
  // credential fails fast where the broker cannot answer, so the call goes without it and stands
  // on the listener's own answer, at once, rather than waiting out its 5 s attempt.
  const fetched: { readonly url: string; readonly authorization: string | null }[] = [];
  const client = createEnvoyClient({
    baseUrl: "http://listener.test",
    retry: false,
    fetch: async (url, init) => {
      fetched.push({
        url: String(url),
        authorization: new Headers(init?.headers).get("Authorization"),
      });
      return Response.json({
        role: "implementer",
        holder: "ses_holder",
        last_seen: 1,
        state: "live",
      });
    },
    credential: brokerCredential(podEnvironment(broker(), "http://dispatch.test"), POD_SESSION),
  });
  const started = Date.now();
  await client.getRole("implementer");
  expect(Date.now() - started).toBeLessThan(2_000);
  expect(fetched).toHaveLength(1);
  expect(fetched[0]?.authorization).toBeNull();
});
