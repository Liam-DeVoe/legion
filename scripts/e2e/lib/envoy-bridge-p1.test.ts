// A failing test for #4140 at 0e663ccef8, found by the acceptance (AcceptE2-2): the bridge's
// failure line prints the digits of SMOKE_UPSTREAM_NATS's password. nats.js reads the first `:`
// followed by digits as the port, so a password of six digits before an IP host is a port Bun
// refuses, and its text names the password ("Port should be >= 0 and < 65536. Received 547204").
// redactUpstream replaces the user info whole (`user:547204`), never the password alone. Copy it
// to scripts/e2e/lib/ and run `bun test scripts/e2e/lib/envoy-bridge-p1.test.ts` from the root.
// At 0e663ccef8 both rows fail: the line holds the password.
import { describe, expect, test } from "bun:test";
import { randomBytes } from "node:crypto";
import { connect } from "nats";
import { bridgeConfigFromEnvironment, bridgeFailureLine } from "./envoy-bridge";

const sixDigits = () => String(100000 + (randomBytes(4).readUInt32BE() % 900000));

async function realError(server: string): Promise<unknown> {
  try {
    const connection = await connect({ servers: server, reconnect: false, timeout: 5_000 });
    await connection.close();
  } catch (error) {
    return error;
  }
  throw new Error(`${server} connected, which this row needs it not to`);
}

describe("the bridge's failure line names no digit of the upstream's password", () => {
  test.each([
    ["an IP host with a port", (password: string) => `nats://operator:${password}@127.0.0.1:4222`],
    ["an IP host with no port", (password: string) => `nats://operator:${password}@10.255.255.1`],
  ])(
    "%s",
    async (_, spelling) => {
      const password = sixDigits();
      const upstream = spelling(password);
      const config = bridgeConfigFromEnvironment({
        SMOKE_REPO: "acme/widgets",
        SMOKE_RIG_NATS: "nats://127.0.0.1:1",
        SMOKE_UPSTREAM_NATS: upstream,
      });
      const error = await realError(upstream);
      // The row can fail for the leak: nats.js's own text carries the password.
      expect(error instanceof Error ? error.message : String(error)).toContain(password);
      expect(bridgeFailureLine(error, config)).not.toMatch(
        new RegExp(`(?<!\\d)${password}(?!\\d)`)
      );
    },
    30_000
  );
});
