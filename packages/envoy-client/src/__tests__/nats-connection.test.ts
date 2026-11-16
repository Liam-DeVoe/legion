import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { createRequire } from "node:module";
import { dirname, join } from "node:path";
import {
  NATS_MAX_PINGS_OUT,
  NATS_PING_INTERVAL_MS,
  natsConnectionOptions,
} from "../nats-connection";

describe("natsConnectionOptions", () => {
  test("never gives up over a refused credential", () => {
    // A hub whose connections an auth service admits refuses every client while that service is
    // away, which a restart of the hub itself causes. nats.js 2.29.3 closes a connection for good
    // on a repeated authorization error unless this is set (protocol.js:657-661, default false in
    // options.js:50), and it clears lastError after a successful reconnect only for an expired
    // authentication (protocol.js:397-399) — so without it one refusal in any earlier outage is
    // enough to abort the next one on its first.
    const options = natsConnectionOptions({
      servers: ["nats://127.0.0.1:4222"],
      name: "test",
      seed: undefined,
    });
    expect(options.ignoreAuthErrorAbort).toBe(true);
  });

  test("the option this file sets is the one nats.js reads", () => {
    // The control for the row above. `ConnectionOptions` would take a misspelling without a word,
    // and nats.js does not refuse an option it does not know: it dials with it and ignores it. So
    // this reads the installed nats.js itself — the default it carries and the line that acts on
    // it — and fails if either name moves, which is the only way the row above could pass while
    // meaning nothing.
    const nats = createRequire(import.meta.url).resolve("nats");
    const base = join(dirname(nats), nats.includes("/lib/") ? "" : "lib", "nats-base-client");
    expect(readFileSync(join(base, "options.js"), "utf8")).toContain("ignoreAuthErrorAbort: false");
    expect(readFileSync(join(base, "protocol.js"), "utf8")).toContain(
      "this.options.ignoreAuthErrorAbort === false"
    );
  });

  test("keeps reconnecting forever, with the ping check", () => {
    const options = natsConnectionOptions({
      servers: ["nats://127.0.0.1:4222"],
      name: "test",
      seed: undefined,
    });
    expect(options.reconnect).toBe(true);
    expect(options.maxReconnectAttempts).toBe(-1);
    expect(options.pingInterval).toBe(NATS_PING_INTERVAL_MS);
    expect(options.maxPingOut).toBe(NATS_MAX_PINGS_OUT);
  });
});
