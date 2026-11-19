import { expect } from "bun:test";
import { connect, DebugEvents, Events, type KV } from "nats";
import { z } from "zod";
import { deliverSubject } from "../src/consumer-delivery";
import { STORED_WHILE_AWAY_SUFFIX } from "../src/delivery-ledger";
import type { EnvoyStack } from "./envoy-stack";
import type { StallProxy } from "./stall-proxy";

/**
 * The spec's bound: a session finds a path that answers within a minute of the freeze. The
 * recovery runs on nats.js's own timers, so it is held to the minute without a clock (R4): the
 * settings a connection was dialled with price the recovery's steps (`recoveryBoundMs`), and the
 * connection's own status events show it took exactly those steps (`expectRecoveredByPingOut`).
 * src/__tests__/nats-stall.test.ts holds `natsConnectionOptions` to it that way, and the scenario
 * below holds each session plugin's own connection to it, then asserts what the recovery does, in
 * the order it must happen.
 */
export const RECOVERY_BOUND_MS = 60_000;

/** How long nats.js 2.29.3 gives a dial before it abandons it, where the options set no `timeout`
 *  (`this.options.timeout || 20000`, lib/nats-base-client/protocol.js). */
export const NATS_JS_DIAL_TIMEOUT_MS = 20_000;
/** nats.js 2.29.3's `reconnectJitter`, where the options set none (lib/nats-base-client/options.js). */
export const NATS_JS_RECONNECT_JITTER_MS = 100;

/** The settings of a NATS dial that pace its recovery from a frozen path (nats.js's
 *  `ConnectionOptions` of these names). */
export interface NatsPacing {
  readonly pingInterval?: number | undefined;
  readonly maxPingOut?: number | undefined;
  readonly timeout?: number | undefined;
  readonly reconnectTimeWait?: number | undefined;
  readonly reconnectJitter?: number | undefined;
}

/** One status event nats.js reported on a connection, as its `status()` iterator gave it. */
export interface NatsStatusEvent {
  readonly type: string;
  readonly data: unknown;
}

/** A NATS connection as a session dialled it: the settings it chose, and every status event
 *  nats.js reported on the connection since, in order. */
export interface NatsDial {
  readonly options: NatsPacing;
  readonly statuses: readonly NatsStatusEvent[];
}

/**
 * The longest a recovery from a frozen path can take on `options`, by the timers that pace it:
 * nats.js 2.29.3 sends a ping each `pingInterval` and leaves the path on the tick that finds
 * `maxPingOut` pings unanswered (lib/nats-base-client/heartbeats.js), the `maxPingOut + 1`th
 * since the last answered one, and redials at once; each of `hungRedials` redials that hangs as
 * well costs its dial timeout, the reconnect wait and its jitter before the next dial.
 */
export function recoveryBoundMs(options: NatsPacing, hungRedials: number): number {
  const left = (options.pingInterval ?? Number.NaN) * ((options.maxPingOut ?? Number.NaN) + 1);
  const redial =
    (options.timeout ?? NATS_JS_DIAL_TIMEOUT_MS) +
    (options.reconnectTimeWait ?? Number.NaN) +
    (options.reconnectJitter ?? NATS_JS_RECONNECT_JITTER_MS);
  return left + hungRedials * redial;
}

/** The status events of a recovery: the stale close, and each dial up to the reconnect. */
const RECOVERY_EVENTS = new Set<string>([
  DebugEvents.StaleConnection,
  Events.Disconnect,
  DebugEvents.Reconnecting,
  Events.Reconnect,
]);

/**
 * Holds a connection's recovery to the steps `recoveryBoundMs` prices, read from the status
 * events nats.js reported rather than from a clock: the connection went stale once, on the ping
 * tick numbered `maxPingOut + 1`, then dialled `hungRedials + 1` times, the last of them the
 * reconnect. A nats.js that leaves a frozen path on a later tick, or redials on rules of its
 * own, fails here, whatever the time it took.
 */
export function expectRecoveredByPingOut(dial: NatsDial, hungRedials: number): void {
  const { statuses, options } = dial;
  const types = statuses.map((status) => status.type);
  const stale = types.indexOf(DebugEvents.StaleConnection);
  expect(stale, `the connection's status events: ${types.join(", ")}`).toBeGreaterThan(0);
  expect(statuses[stale - 1]).toEqual({
    type: DebugEvents.PingTimer,
    data: `${(options.maxPingOut ?? Number.NaN) + 1}`,
  });
  const recovery = types.slice(stale).filter((type) => RECOVERY_EVENTS.has(type));
  expect(recovery.slice(0, recovery.indexOf(Events.Reconnect) + 1)).toEqual([
    DebugEvents.StaleConnection,
    Events.Disconnect,
    ...Array<string>(hungRedials + 1).fill(DebugEvents.Reconnecting),
    Events.Reconnect,
  ]);
}

/** A session plugin under test, registered, whose NATS path `proxy` fronts. */
export interface FreezeScenarioHost {
  /** What the timing log line starts with, such as `pi-envoy stall`. */
  readonly label: string;
  /** The natsd and listener the session runs against. */
  readonly stack: EnvoyStack;
  /** The session the direct messages go to. */
  readonly sessionID: string;
  /** The proxy the session's only NATS connection goes through. */
  readonly proxy: StallProxy;
  /** The text of every message the host showed the session that holds `marker`, in order. */
  shown(marker: string): readonly string[];
  /** The session's NATS connection as the plugin itself dialled it, read through a seam the
   *  plugin leaves unset in production; undefined until it dialled. */
  natsDial(): NatsDial | undefined;
  /** Resolves once `condition` holds, waiting through `@legion/test-fixtures/wait`; throws,
   *  naming `what`, when the wait gives up or the host's session can no longer make it hold. */
  waitFor(condition: () => boolean | Promise<boolean>, what: string): Promise<void>;
}

/** The fields of the listener's session entry (`envoy_sessions`) the scenario reads. */
const SessionEntrySchema = z.object({
  delivery: z.string().optional(),
  epoch: z.string().optional(),
});

/** natsd's `/connz?subs=1`: each connection, when it started and what it subscribes. */
const ConnzSchema = z.object({
  connections: z
    .array(
      z.object({
        start: z.string(),
        subscriptions_list: z.array(z.string()).optional(),
      })
    )
    .optional(),
});

/** The session's entry as the listener stored it; undefined while it has none. */
async function sessionEntry(
  sessions: KV,
  sessionID: string
): Promise<z.infer<typeof SessionEntrySchema> | undefined> {
  const entry = await sessions.get(sessionID);
  if (entry === null || entry.operation !== "PUT") return undefined;
  return SessionEntrySchema.parse(entry.json());
}

/** Whether a connection natsd accepted at or after `since` (`Date.now()` time) subscribes `subject`. */
async function subscribedSince(monitorUrl: string, subject: string, since: number) {
  const response = await fetch(`${monitorUrl}/connz?subs=1`);
  if (!response.ok) return false;
  const connz = ConnzSchema.parse(await response.json());
  return (connz.connections ?? []).some(
    (connection) =>
      Date.parse(connection.start) >= since &&
      (connection.subscriptions_list ?? []).includes(subject)
  );
}

/**
 * The freeze every session plugin must survive. The listener holds the session's durable consumer
 * and relays it on `notifications.envoy.deliver.<id>`: the session's registration is answered
 * `consumer` and its relay subject is subscribed, and a direct message is shown live. Then the
 * session's NATS path is frozen and a second message sent while it is cut off: it must be shown
 * after the connection nats.js dialled in place of the frozen one subscribed the relay subject
 * again and the session registered a new epoch, so the listener restarted the relay at the
 * consumer's ack floor. A third message, sent after that registration, is relayed on the new
 * connection. Each of the three is shown once, and none as a frame the session was told it had
 * been away for: the relay delivers all three.
 *
 * The host runs on nats.js's real ping and dial timers against a real listener, so each wait is
 * real time, and each ends at its row's deadline. How long the recovery took is logged for
 * reading, not judged: the minute the spec allows is held on the session's own connection, by
 * the settings the plugin dialled it with and the steps its status events show it took.
 */
export async function runFreezeScenario(host: FreezeScenarioHost): Promise<void> {
  const { label, stack, sessionID, proxy, shown, waitFor } = host;
  // The subject the session's own connection takes every message on.
  const sessionSubject = deliverSubject(sessionID);
  const send = async (marker: string) => {
    const answer = await fetch(`${stack.envoyUrl}/v1/messages/send`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ target_session: sessionID, source: "agent", message: marker }),
    });
    expect(answer.status).toBe(200);
  };
  const run = crypto.randomUUID();
  // The scenario's own connection, straight to natsd, reads the listener's session entry.
  const observer = await connect({ servers: [stack.natsUrl], name: `stall-observer-${run}` });
  try {
    const sessions = await observer.jetstream().views.kv("envoy_sessions", { bindOnly: true });
    let firstEpoch: string | undefined;
    await waitFor(async () => {
      const entry = await sessionEntry(sessions, sessionID);
      if (entry === undefined) return false;
      firstEpoch = entry.delivery === "consumer" ? entry.epoch : undefined;
      if (firstEpoch === undefined) return false;
      return await subscribedSince(stack.monitorUrl, sessionSubject, 0);
    }, "the session's registration answered consumer, with its relay subject subscribed");

    const live = `stall-live-before-${run}`;
    await send(live);
    await waitFor(() => shown(live).length > 0, "the live message");

    const frozenAt = proxy.freeze();
    const missed = `stall-missed-${run}`;
    await send(missed);
    await waitFor(() => shown(missed).length > 0, "the message sent while the path was frozen");
    const shownAt = Date.now();
    const resubscribed = proxy.connections.find(
      (opened) => opened.acceptedAt >= frozenAt && opened.subscribedAt !== undefined
    );
    console.log(
      `${label}: resubscribed ${(((resubscribed?.subscribedAt ?? Number.NaN) - frozenAt) / 1000).toFixed(1)} s, missed message shown ${((shownAt - frozenAt) / 1000).toFixed(1)} s after the freeze`
    );
    // The connection nats.js dialled after the freeze subscribed again before the missed message
    // could be shown: the proxy forwards nothing on the frozen one.
    expect(resubscribed?.subscribedAt).toBeDefined();
    // The minute, on the plugin's own connection: the settings it dialled with price a recovery
    // under it, and its status events show the recovery took those steps and no others. The
    // reconnect's event comes before the registration the missed message waited on; the wait
    // below is for the seam's copy of it.
    await waitFor(
      () => host.natsDial()?.statuses.some((status) => status.type === Events.Reconnect) ?? false,
      "the session's NATS client to report its reconnect"
    );
    const dial = host.natsDial();
    if (dial === undefined) throw new Error("the session dialled no NATS connection");
    expect(recoveryBoundMs(dial.options, 0)).toBeLessThan(RECOVERY_BOUND_MS);
    expectRecoveredByPingOut(dial, 0);
    // The connection that replaced the frozen one carries the session's relay subject.
    expect(await subscribedSince(stack.monitorUrl, sessionSubject, frozenAt)).toBe(true);
    // The relay restarted only on a registration that named a new epoch, still on consumer
    // delivery.
    const renewed = await sessionEntry(sessions, sessionID);
    expect(renewed?.delivery).toBe("consumer");
    expect(renewed?.epoch).toBeDefined();
    expect(renewed?.epoch).not.toBe(firstEpoch);

    const after = `stall-live-after-${run}`;
    await send(after);
    await waitFor(() => shown(after).length > 0, "the message after the reconnect");

    // Each shown once, and the relay delivered all three, so none carries the line a frame stored
    // while the session was away would carry.
    for (const marker of [live, missed, after]) {
      const messages = shown(marker);
      expect(messages).toHaveLength(1);
      expect(messages[0]?.includes(STORED_WHILE_AWAY_SUFFIX)).toBe(false);
    }
  } finally {
    await observer.close();
  }
}
