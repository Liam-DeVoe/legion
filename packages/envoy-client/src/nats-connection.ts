import type { ConnectionOptions } from "nats";
import type { NatsSeed } from "./envoy-settings";
import { natsAuthOptions } from "./nats-auth";

/**
 * How often a session pings its NATS server, and how many pings may go unanswered. nats.js 2.29.3
 * sends one ping per tick and drops the connection on the tick that finds `maxPingOut` pings still
 * unanswered, so a path that freezes (packets stop, neither side closes) is left within
 * 3 x 10 s = 30 s and redialed at once. A redial that also hangs costs the 20 s dial timeout, the
 * 2 s reconnect wait and its 0.1 s jitter: 30 + 20 + 2 + 0.1 = 52.1 s, under a minute. nats.js's
 * default 120 s interval left a frozen path for up to 6 minutes.
 */
export const NATS_PING_INTERVAL_MS = 10_000;
export const NATS_MAX_PINGS_OUT = 2;

/** How long a dial may take before nats.js abandons it and dials again, and the most it adds to the
 *  reconnect wait at random between two dials (non-TLS servers). Both are nats.js 2.29.3's own
 *  defaults, set here so the recovery a session is held to is priced from this module's settings
 *  and a change of nats.js's defaults cannot move it. */
export const NATS_DIAL_TIMEOUT_MS = 20_000;
export const NATS_RECONNECT_JITTER_MS = 100;

/** The `connect` options every session plugin dials NATS with: its credential, reconnecting forever
 * 2 s apart (so the connection never closes on its own and only the plugin's shutdown ends it), and
 * the ping check, dial timeout and jitter above. */
export function natsConnectionOptions(input: {
  readonly servers: readonly string[];
  readonly name: string;
  readonly seed: NatsSeed | undefined;
}): ConnectionOptions {
  return {
    servers: [...input.servers],
    name: input.name,
    ...natsAuthOptions(input.seed),
    reconnect: true,
    maxReconnectAttempts: -1,
    reconnectTimeWait: 2_000,
    pingInterval: NATS_PING_INTERVAL_MS,
    maxPingOut: NATS_MAX_PINGS_OUT,
    timeout: NATS_DIAL_TIMEOUT_MS,
    reconnectJitter: NATS_RECONNECT_JITTER_MS,
  };
}
