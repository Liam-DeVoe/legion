import type { ConnectionOptions } from "nats";
import { natsAuthOptions } from "./nats-auth";
import type { NatsCredential } from "./nats-credential";

/**
 * How often a session pings its NATS server, and how many pings may go unanswered. nats.js 2.29.3
 * sends one ping per tick and drops the connection on the tick that finds `maxPingOut` pings still
 * unanswered, so a path that freezes (packets stop, neither side closes) is left within
 * 3 x 10 s = 30 s and redialed at once. A redial that also hangs costs nats.js's 20 s dial timeout
 * and the 2 s reconnect wait: 30 + 20 + 2 = 52 s, under a minute. nats.js's default 120 s interval
 * left a frozen path for up to 6 minutes.
 */
export const NATS_PING_INTERVAL_MS = 10_000;
export const NATS_MAX_PINGS_OUT = 2;

/** The `connect` options every session plugin dials NATS with: its credential (`natsAuthOptions`:
 * a seed's nkey user, the session's broker token, or none), reconnecting forever
 * 2 s apart (so the connection never closes on its own and only the plugin's shutdown ends it),
 * the ping check above, and never giving up over a refused credential.
 *
 * That last one matters on a hub whose connections an auth service admits
 * (packages/envoy/internal/broker/natscallout): while that service is away, which a restart of the
 * hub itself causes, the server refuses every client it would have asked about. nats.js 2.29.3
 * closes a connection for good on a repeated authorization error unless `ignoreAuthErrorAbort` is
 * set (`protocol.js:657-661`; the default is false, `options.js:50`), and it clears `lastError`
 * after a successful reconnect only for an expired authentication (`protocol.js:397-399`), so a
 * client refused once in any earlier outage would abort on its first refusal in the next one. The
 * Go clients take the same option for the same reason (packages/envoy/internal/bus). */
export function natsConnectionOptions(input: {
  readonly servers: readonly string[];
  readonly name: string;
  readonly credential: NatsCredential;
}): ConnectionOptions {
  return {
    servers: [...input.servers],
    name: input.name,
    ...natsAuthOptions(input.credential),
    reconnect: true,
    maxReconnectAttempts: -1,
    reconnectTimeWait: 2_000,
    pingInterval: NATS_PING_INTERVAL_MS,
    maxPingOut: NATS_MAX_PINGS_OUT,
    ignoreAuthErrorAbort: true,
  };
}
