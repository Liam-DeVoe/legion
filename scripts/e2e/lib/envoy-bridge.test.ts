import { describe, expect, test } from "bun:test";
import { nkeys } from "nats";
import {
  bridgeConfigFromEnvironment,
  bridgeFailureLine,
  bridgeReadyLine,
  envelopeValidation,
  redactUpstream,
} from "./envoy-bridge";

const currentEnvelope = (overrides: Record<string, unknown> = {}) => ({
  event_id: "evt-1",
  source: "github",
  source_event_id: "delivery-1",
  topic: "notifications.github.sjawhar.legion-smoke.pr.1",
  dedupe_key: "github.delivery-1",
  issued_at: 1_700_000_000_000,
  payload_summary: "pull request #1 opened",
  payload: '{"action":"opened"}',
  trace_id: "trace-1",
  ...overrides,
});

describe("bridgeConfigFromEnvironment", () => {
  test("relays only the sandbox repository's GitHub subjects", () => {
    const config = bridgeConfigFromEnvironment({
      SMOKE_REPO: "sjawhar/legion-smoke",
      SMOKE_RIG_NATS: "nats://172.30.0.1:31000",
      SMOKE_UPSTREAM_NATS: "nats://envoy-nats.tailnet.example:4222",
    });
    expect(config).toEqual({
      repository: "sjawhar/legion-smoke",
      subjects: ["notifications.github.sjawhar.legion-smoke.>"],
      upstreamUrl: "nats://envoy-nats.tailnet.example:4222",
      downstreamUrl: "nats://172.30.0.1:31000",
    });
  });

  // Envoy writes a dot in a repository's name as `_`, so the name stays one subject segment.
  test("relays a dotted repository's subjects as Envoy spells them", () => {
    const config = bridgeConfigFromEnvironment({
      SMOKE_REPO: "acme/wid.gets",
      SMOKE_RIG_NATS: "nats://x:1",
      SMOKE_UPSTREAM_NATS: "nats://y.example:1",
    });
    expect(config.subjects).toEqual(["notifications.github.acme.wid_gets.>"]);
  });

  test("never subscribes to Dispatch issue subjects (two rigs on one project cross-admit)", () => {
    const config = bridgeConfigFromEnvironment({
      SMOKE_REPO: "a/b",
      SMOKE_RIG_NATS: "nats://x:1",
      SMOKE_UPSTREAM_NATS: "nats://y.example:1",
    });
    expect(config.subjects.some((s) => s.startsWith("notifications.dispatch"))).toBe(false);
  });

  test("refuses a repository that could broaden the NATS subscription", () => {
    for (const repo of ["example-org/>", "a/*", "a.b", "", "a/b/c"]) {
      expect(() =>
        bridgeConfigFromEnvironment({
          SMOKE_REPO: repo,
          SMOKE_RIG_NATS: "nats://x:1",
          SMOKE_UPSTREAM_NATS: "nats://y.example:1",
        })
      ).toThrow(/SMOKE_REPO/);
    }
  });

  test("requires SMOKE_RIG_NATS", () => {
    expect(() =>
      bridgeConfigFromEnvironment({ SMOKE_REPO: "a/b", SMOKE_UPSTREAM_NATS: "nats://y.example:1" })
    ).toThrow(/SMOKE_RIG_NATS/);
  });

  test("requires SMOKE_UPSTREAM_NATS: the operator names the production NATS, it has no default", () => {
    for (const upstream of [undefined, "", "  "]) {
      expect(() =>
        bridgeConfigFromEnvironment({
          SMOKE_REPO: "a/b",
          SMOKE_RIG_NATS: "nats://x:1",
          SMOKE_UPSTREAM_NATS: upstream,
        })
      ).toThrow("SMOKE_UPSTREAM_NATS is required");
    }
  });

  test("refuses an upstream that is not one NATS URL naming a fully-qualified host, never echoing it", () => {
    for (const [upstream, canary] of [
      ["nats://canary-a:4222", "canary-a"],
      ["canary-b:4222", "canary-b"],
      ["nats://user:canary-c@canary-d", "canary-"],
      // the client dials what follows the last "://", so nothing may follow the authority
      ["nats://ok.example/nats://canary-e:4222", "canary-e"],
      ["nats://ok.example:4222?x=nats://canary-f:4222", "canary-f"],
      ["nats://ok.example:4222#nats://canary-g:4222", "canary-g"],
      ["nats://canary-h.example:4222,nats://canary-i.example:4222", "canary-"],
      ["nats://canary%zz.example:4222", "canary"],
      // nats.js ends the host at a `\` (it reads the URL as an http: one), so this dials the bare
      // name canary-j, which only a resolver's search domain completes
      ["nats://canary-j\\en@envoy-nats.tailnet.example:4222", "canary-j"],
      ["::", "::"],
    ] as const) {
      const refusal = (() => {
        try {
          bridgeConfigFromEnvironment({
            SMOKE_REPO: "a/b",
            SMOKE_RIG_NATS: "nats://x:1",
            SMOKE_UPSTREAM_NATS: upstream,
          });
        } catch (error) {
          return error instanceof Error ? error.message : String(error);
        }
        return "accepted";
      })();
      expect(refusal).toStartWith(
        "SMOKE_UPSTREAM_NATS is not one NATS URL naming a fully-qualified host"
      );
      expect(refusal).not.toContain(canary);
    }
  });

  test("accepts one fully-qualified upstream, trimmed, with or without a scheme", () => {
    for (const [upstream, want] of [
      [" nats://envoy-nats.tailnet.example:4222 ", "nats://envoy-nats.tailnet.example:4222"],
      ["\tnats://envoy-nats.tailnet.example:4222/\n", "nats://envoy-nats.tailnet.example:4222/"],
      ["envoy-nats.tailnet.example:4222", "envoy-nats.tailnet.example:4222"],
      ["tls://user:pw@envoy-nats.tailnet.example", "tls://user:pw@envoy-nats.tailnet.example"],
      ["nats://127.0.0.1:4222", "nats://127.0.0.1:4222"],
    ]) {
      const config = bridgeConfigFromEnvironment({
        SMOKE_REPO: "a/b",
        SMOKE_RIG_NATS: "nats://x:1",
        SMOKE_UPSTREAM_NATS: upstream,
      });
      expect(config.upstreamUrl).toBe(want);
    }
  });
});

describe("redactUpstream", () => {
  test("puts the variable's name in place of the value, its user info, and its host", () => {
    const upstream = "nats://user:canary-pw@Canary-Host.tailnet.example:4222";
    for (const message of [
      `"http://user:canary-pw@canary-host.tailnet.example:4222" cannot be parsed as a URL.`,
      "getaddrinfo ENOTFOUND canary-host.tailnet.example",
      `connect to ${upstream} failed`,
    ]) {
      const redacted = redactUpstream(message, upstream);
      expect(redacted).toContain("SMOKE_UPSTREAM_NATS");
      expect(redacted.toLowerCase()).not.toContain("canary");
    }
  });

  test("puts the variable's name in place of the host nats.js dials, where it differs", () => {
    // The bridge refuses such an upstream (above); a failure line that quotes it all the same
    // names none of it.
    const upstream = "nats://canary-q\\en@envoy-nats.tailnet.example:4222";
    expect(redactUpstream("getaddrinfo EAI_AGAIN canary-q", upstream)).toBe(
      "getaddrinfo EAI_AGAIN SMOKE_UPSTREAM_NATS"
    );
  });

  test("leaves a message that does not quote the upstream as it is", () => {
    expect(redactUpstream("CONNECTION_REFUSED", "nats://envoy-nats.tailnet.example:4222")).toBe(
      "CONNECTION_REFUSED"
    );
  });

  test("never searches the variable's name it put in", () => {
    // A user `NATS` is a word of the stand-in itself.
    const upstream = "nats://NATS@envoy-nats.tailnet.example:4222";
    expect(redactUpstream(`connect to ${upstream} failed`, upstream)).toBe(
      "connect to SMOKE_UPSTREAM_NATS failed"
    );
  });
});

// The rig's NATS (SMOKE_RIG_NATS) may hold a credential too: neither the readiness line nor the
// failure line prints it, while the rig's server is still named and the upstream still is not.
// Each URL's user info is joined here, so the source holds no credential-bearing URL for the push
// leak check to find.
describe("the bridge's output lines", () => {
  const userinfo = (...parts: string[]) => `${parts.join(":")}@`;
  const config = bridgeConfigFromEnvironment({
    SMOKE_REPO: "a/b",
    SMOKE_RIG_NATS: `nats://${userinfo("rig", "canary-rig-pw")}172.30.0.1:31000`,
    SMOKE_UPSTREAM_NATS: `nats://${userinfo("user", "canary-up-pw")}canary-host.tailnet.example:4222`,
  });

  test("the readiness line names the rig's server without its credential", () => {
    const line = bridgeReadyLine(config);
    expect(line).toContain("upstream=SMOKE_UPSTREAM_NATS downstream=nats://172.30.0.1:31000");
    expect(line.toLowerCase()).not.toContain("canary");
  });

  test("the failure line quotes neither connection's credential", () => {
    for (const message of [
      `"http://${userinfo("rig", "canary-rig-pw")}172.30.0.1:99999" cannot be parsed as a URL.`,
      `connect to ${config.downstreamUrl} failed`,
      `connect to ${config.upstreamUrl} failed`,
    ]) {
      const line = bridgeFailureLine(new Error(message), config);
      expect(line.startsWith("BRIDGE UNHEALTHY ")).toBe(true);
      expect(line.toLowerCase()).not.toContain("canary");
    }
  });

  test("a rig credential that is a label of the upstream's host leaves none of that host", () => {
    // Replaced the rig's first, `private` broke the whole-host match the upstream's needs, and
    // the rest of the host was printed. A rig user that is no part of the host is the control.
    for (const user of ["private", "unrelated"]) {
      const overlapping = bridgeConfigFromEnvironment({
        SMOKE_REPO: "a/b",
        SMOKE_RIG_NATS: `nats://${userinfo(user, "pw")}172.30.0.1:31000`,
        SMOKE_UPSTREAM_NATS: "nats://private.e2-proof-does-not-exist.invalid:4222",
      });
      const failure = new Error("getaddrinfo ENOTFOUND private.e2-proof-does-not-exist.invalid");
      expect(bridgeFailureLine(failure, overlapping)).toBe(
        "BRIDGE UNHEALTHY getaddrinfo ENOTFOUND SMOKE_UPSTREAM_NATS"
      );
    }
  });

  // The two rig forms nats.js's own text carries: a seed pasted as the host, which its resolver
  // names lowercased, and a token holding an unencoded `/`, whose first segment the URL makes the
  // host it then resolves.
  test("a rig seed pasted as the host, or a rig token with an unencoded /, is in neither line", () => {
    const seed = new TextDecoder().decode(nkeys.createUser().getSeed());
    for (const [rig, resolved] of [
      [`nats://${seed}:4222`, seed.toLowerCase()],
      [`nats://${userinfo("canarytok/def+gh==")}172.30.0.1:31000`, "canarytok"],
    ] as const) {
      const config = bridgeConfigFromEnvironment({
        SMOKE_REPO: "a/b",
        SMOKE_RIG_NATS: rig,
        SMOKE_UPSTREAM_NATS: "nats://envoy-nats.tailnet.example:4222",
      });
      const lines = [
        bridgeReadyLine(config),
        bridgeFailureLine(new Error(`getaddrinfo EAI_AGAIN ${resolved}`), config),
      ].join("\n");
      expect(lines.toLowerCase()).not.toContain(resolved);
      expect(lines).toContain("downstream=a server address that does not read as a host");
    }
  });
});

describe("envelopeValidation", () => {
  test("accepts a current envelope as bytes or text", () => {
    const text = JSON.stringify(currentEnvelope());
    expect(envelopeValidation(new TextEncoder().encode(text))).toEqual({
      ok: true,
      shape: "current",
    });
    expect(envelopeValidation(text)).toEqual({ ok: true, shape: "current" });
  });

  test("names every missing and extra field of a legacy envelope", () => {
    const legacy: Record<string, unknown> = currentEnvelope({ legacy_payload: "old field" });
    delete legacy.event_id;
    delete legacy.payload_summary;
    expect(envelopeValidation(JSON.stringify(legacy))).toMatchObject({
      ok: false,
      missing: ["event_id", "payload_summary"],
      extra: ["legacy_payload"],
    });
  });

  test("rejects malformed JSON", () => {
    expect(envelopeValidation(new TextEncoder().encode("{not json"))).toMatchObject({
      ok: false,
      missing: [],
      extra: [],
      errors: [expect.stringContaining("invalid JSON")],
    });
  });
});
