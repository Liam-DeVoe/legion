import { describe, expect, test } from "bun:test";
import { randomBytes } from "node:crypto";
import { newDpopKey } from "@legion/broker-client/dpop";
import { BrokerTokenClient } from "@legion/broker-client/token-client";
import { bearerCredential } from "../bearer";
import { DispatchClient } from "../dispatch-http";
import { messageFor } from "../errors";
import { tradeFailureReason } from "../nats-credential";
import { createEnvoyClient } from "../transport";

// The backstop under every rule that names no peer's text: each line the clients print passes the
// scrub of the secrets this process holds (`@legion/contracts/held-secrets`). A server at the wrong
// URL, Dispatch's, the listener's or the broker's, can echo what it was sent inside a refusal the
// client prints as guidance; the echo, whole or a run of 10 or more of its characters, prints as
// `<withheld>` (#4140, the scrub).
const secret = (prefix: string) => `${prefix}${randomBytes(24).toString("base64url")}`;
const echoes = (held: string) =>
  [
    ["whole", held],
    ["a run of it", held.slice(4, 18)],
  ] as const;

describe("a server that echoes what it was sent", () => {
  test.each(
    echoes(secret("dsp_"))
  )("Dispatch's refusal echoing the bearer (%s) prints none of it", async (part) => {
    const token = secret("dsp_");
    const shown = part === "whole" ? token : token.slice(4, 18);
    const fetchImpl = (async () =>
      Response.json(
        { error: `the bearer ${shown} is not one this Dispatch issued`, code: "UNAUTHENTICATED" },
        { status: 401 }
      )) as unknown as typeof fetch;
    const client = new DispatchClient("http://dispatch.test", bearerCredential(token), fetchImpl, {
      retryNoRoute: false,
    });
    const line = await client.getIssue("DSP-1").then(
      () => "",
      (error: unknown) => messageFor(error)
    );
    expect(line).toContain("<withheld>");
    expect(line).not.toContain(shown);
  });

  test.each(
    echoes(secret("env_"))
  )("the listener's refusal echoing the bearer (%s) prints none of it", async (part) => {
    const token = secret("env_");
    const shown = part === "whole" ? token : token.slice(4, 18);
    const fetchImpl = (async () =>
      new Response(`no session holds ${shown}`, { status: 404 })) as unknown as typeof fetch;
    const client = createEnvoyClient({
      baseUrl: "http://listener.test",
      fetch: fetchImpl,
      credential: bearerCredential(token),
      retry: false,
    });
    const line = await client.getInterest("ses_scrub").then(
      () => "",
      (error: unknown) => messageFor(error)
    );
    expect(line).toContain("<withheld>");
    expect(line).not.toContain(shown);
  });

  test.each(
    echoes(secret("eyJ"))
  )("the broker's refusal echoing the subject token (%s) prints none of it", async (part) => {
    const subject = secret("eyJ");
    const shown = part === "whole" ? subject : subject.slice(4, 18);
    const broker = Bun.serve({
      hostname: "127.0.0.1",
      port: 0,
      fetch: () =>
        Response.json(
          {
            error: "invalid_request",
            error_description: `The request is missing a required parameter, includes an invalid parameter value, includes a parameter more than once, or is otherwise malformed. The subject token was refused: ${shown} is not this broker's audience.`,
          },
          { status: 400 }
        ),
    });
    try {
      const client = new BrokerTokenClient({
        brokerUrl: `http://127.0.0.1:${broker.port}`,
        key: await newDpopKey(),
        subject: {
          read: async () => ({ token: subject, tokenType: "urn:ietf:params:oauth:token-type:jwt" }),
          describe: () => "probe",
        },
      });
      const failure = await client.token().then(
        () => undefined,
        (error: unknown) => error
      );
      for (const line of [tradeFailureReason(failure), messageFor(failure)]) {
        expect(line).toContain("The subject token was refused: <withheld>");
        expect(line).not.toContain(shown);
      }
    } finally {
      broker.stop(true);
    }
  });
});
