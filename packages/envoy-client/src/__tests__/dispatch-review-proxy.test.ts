// packages/envoy-client/src/__tests__/dispatch-review-proxy.test.ts
//
// A review sent through Cipher's proxy: where the credential is a person's sign-in on this
// machine, it has an approve route (`RequestCredential.approveRoute`), and `reviewArtifact` sends
// the review there - to the Dispatch URL's own host, through the proxy the route names, with the
// placeholder as the proxy's password and as `Authorization: DPoP <placeholder>`, trusting the
// route's authority, and with no proof of any key of this process. A credential with no approve
// route sends the review as before, and a Dispatch URL the route does not reach sends nothing.

import { expect, test } from "bun:test";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import type { ApproveRoute, RequestCredential } from "../bearer";
import { DispatchClient } from "../dispatch-http";

const PLACEHOLDER = `agent-secrets-placeholder-approve-${"B".repeat(32)}`;
const AUTHORITY = "-----BEGIN CERTIFICATE-----\nproxy\n-----END CERTIFICATE-----\n";
const ROUTE: ApproveRoute = {
  placeholder: PLACEHOLDER,
  proxyUrl: "http://proxy.example:3128",
  caCertificate: AUTHORITY,
  hosts: ["dispatch.example"],
};
const REVIEW = {
  id: "review-1",
  artifact_id: "artifact-1",
  version_number: 3,
  state: "approved",
  actor: "ada@example.com",
  created_at: "2026-11-23T12:00:00Z",
};

/** A credential whose own headers are a bearer's, with `route` as its approve route. */
function credential(
  route: ApproveRoute | undefined
): RequestCredential & { readonly asked: number[] } {
  const asked: number[] = [];
  const token = fixtureSecret("box");
  return {
    asked,
    scheme: "DPoP",
    headers: () => ({ Authorization: `DPoP ${token}`, DPoP: "a-proof-of-the-box-key" }),
    approveRoute: async () => {
      asked.push(1);
      return route;
    },
    secret: () => token,
  };
}

interface Sent {
  readonly url: string;
  readonly init: RequestInit & { readonly proxy?: string; readonly tls?: { readonly ca?: string } };
}

/** A fetch that answers `status` with `body` and keeps what it was sent. */
function recordingFetch(status: number, body: unknown): typeof fetch & { readonly sent: Sent[] } {
  const sent: Sent[] = [];
  const impl = async (
    input: Parameters<typeof fetch>[0],
    init?: RequestInit
  ): Promise<Response> => {
    sent.push({ url: String(input), init: (init ?? {}) as Sent["init"] });
    return Response.json(body, { status });
  };
  return Object.assign(impl, { sent, preconnect: () => {} }) as typeof fetch & {
    readonly sent: Sent[];
  };
}

test("a person's review goes through Cipher's proxy with the placeholder and no proof", async () => {
  const fetchImpl = recordingFetch(201, REVIEW);
  const client = new DispatchClient("https://dispatch.example", credential(ROUTE), fetchImpl);
  const review = await client.reviewArtifact("artifact-1", 3, { state: "approved" });
  expect(review as unknown).toEqual(REVIEW);
  expect(fetchImpl.sent).toHaveLength(1);
  const [sent] = fetchImpl.sent;
  expect(sent?.url).toBe("https://dispatch.example/api/v1/artifacts/artifact-1/versions/3/reviews");
  expect(sent?.init.method).toBe("POST");
  const proxy = new URL(sent?.init.proxy ?? "");
  expect(`${proxy.protocol}//${proxy.host}`).toBe("http://proxy.example:3128");
  expect(decodeURIComponent(proxy.password)).toBe(PLACEHOLDER);
  expect(sent?.init.tls?.ca).toBe(AUTHORITY);
  const headers = new Headers(sent?.init.headers);
  expect(headers.get("Authorization")).toBe(`DPoP ${PLACEHOLDER}`);
  expect(headers.get("DPoP")).toBeNull();
  expect(JSON.parse(String(sent?.init.body))).toEqual({ state: "approved" });
});

test("a credential with no approve route sends the review as before, with its own headers", async () => {
  const fetchImpl = recordingFetch(201, REVIEW);
  const box = credential(undefined);
  const client = new DispatchClient("https://dispatch.example", box, fetchImpl);
  await client.reviewArtifact("artifact-1", 3, { state: "approved" });
  expect(box.asked).toHaveLength(1);
  expect(fetchImpl.sent).toHaveLength(1);
  const [sent] = fetchImpl.sent;
  expect(sent?.init.proxy).toBeUndefined();
  expect(new Headers(sent?.init.headers).get("DPoP")).toBe("a-proof-of-the-box-key");
});

test("a Dispatch URL the approve route does not reach sends nothing", async () => {
  for (const base of [
    "https://other.example",
    "http://dispatch.example",
    "https://dispatch.example:8443",
  ]) {
    const fetchImpl = recordingFetch(201, REVIEW);
    const client = new DispatchClient(base, credential(ROUTE), fetchImpl);
    const refused = await client.reviewArtifact("artifact-1", 3, { state: "approved" }).then(
      () => null,
      (error: unknown) => error as Error
    );
    expect(refused?.message ?? "").toContain("dispatch.example");
    expect(refused?.message ?? "").not.toContain(PLACEHOLDER);
    expect(fetchImpl.sent).toHaveLength(0);
  }
});

test("the proxy's refusal is thrown as Dispatch's client throws an answer, without the placeholder", async () => {
  const fetchImpl = recordingFetch(403, { code: "NOT_ALLOWED", error: "not allowed by rule" });
  const client = new DispatchClient("https://dispatch.example", credential(ROUTE), fetchImpl);
  const refused = await client.reviewArtifact("artifact-1", 3, { state: "approved" }).then(
    () => null,
    (error: unknown) => error as Error
  );
  expect(refused).not.toBeNull();
  expect(refused?.message ?? "").not.toContain(PLACEHOLDER);
});
