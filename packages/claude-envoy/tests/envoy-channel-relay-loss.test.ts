// Ways a running session loses its relay subject without its connection being replaced at once,
// each driven on `FakeNats`, which refuses a subscribe, a publish and a flush on a connection
// closed for good and ends a refused subscription with its error, as nats.js does:
// - the relay subscription ends with an error of its own while the connection stays up (a
//   permissions refusal): nothing took it again, and every registration asked the listener to
//   relay to a subscription that no longer existed, with no outage told;
// - the subscription taken again in its place is not yet the server's when the flush behind it
//   is lost to a redial or times out, and the server can still refuse it after the reconnect;
// - the connection closes for good between a `/clear`'s deregistration of the old id and its
//   registration of the new one: the credential move must wait for the connection that replaces
//   it, and the outage is told, naming the close;
// - a `/clear` waits to be taken up while the server still registers under the id Claude Code
//   left: the connection closed for good before any heartbeat took the `/clear` up, or the
//   listener refused the old id's DELETE on the new connection; what that registration's answer
//   would tell is the old id's, and is told to neither conversation;
// - the relay subscription ends as the session shuts down, with a registration still queued for
//   it: the leaving session is told no outage, and nothing registers behind the DELETE.
import { expect, test } from "bun:test"
import { deliverSubject } from "@legion/envoy-client/consumer-delivery"
import type { SubscribeInput } from "@legion/envoy-client/transport"
import { startChannelSession } from "../src/envoy-channel-server"
import { readReplayState, replayStateFile } from "../src/replay-state"
import { SessionIdentity, sessionHandoffFile, writeSessionHandoff } from "../src/session-identity"
import {
  connectionClosedError,
  FakeNats,
  FakeNotifier,
  noInterest,
  recordingClient,
  sessionOptions,
  settled,
  useScratchState,
  waitFor,
} from "./channel-session-harness"
import { isolatePaneEnvironment } from "./pane-environment"

isolatePaneEnvironment()

const scratchState = useScratchState()

/** nats.js's error for a server's refusal, as it reaches a subscription or the closed connection. */
function serverRefusal(code: string, text: string): Error {
  return Object.assign(new Error(text), { name: "NatsError", code })
}

/** The notices the server wrote for itself under `heading`: the one write that names no event. */
function notices(notifier: FakeNotifier, heading: string): string[] {
  return notifier.notifications
    .filter((written) => written.eventId === undefined && written.content.includes(heading))
    .map((written) => written.content)
}

/** What session `id`'s state file under `directory` says it was last told of its inbound; nothing
 *  when the file is missing or cannot be read. */
async function toldInFile(directory: string, id: string): Promise<boolean | undefined> {
  return (await readReplayState(replayStateFile(directory, id)).catch(() => undefined))
    ?.inboundOutageTold
}

test("a relay subscription the server ends with an error is told as an outage naming why, and the next heartbeat takes it again at a new epoch", async () => {
  const SESSION = "ses_relay_lost"
  const RELAY = deliverSubject(SESSION)
  const nats = new FakeNats()
  const notifier = new FakeNotifier()
  const relaySubscribes = (): number =>
    nats.calls.filter((call) => call === `nats.subscribe ${RELAY}`).length
  /** Each registration the listener saw, with how many times the relay had been subscribed. */
  const registrations: { readonly input: SubscribeInput; readonly relaySubscribes: number }[] = []
  const session = await startChannelSession(
    sessionOptions(new SessionIdentity(SESSION, "/tmp"), scratchState(), {
      connection: nats,
      notifier,
      heartbeatMs: 25,
      client: recordingClient([], {
        subscribe: async (input) => {
          registrations.push({ input, relaySubscribes: relaySubscribes() })
          return noInterest(input.sessionID)
        },
      }),
    }),
  )
  try {
    const firstEpoch = registrations.at(-1)?.input.delivery?.epoch
    const failedAt = registrations.length
    nats.failSubscription(
      RELAY,
      serverRefusal(
        "PERMISSIONS_VIOLATION",
        `Permissions Violation for Subscription to "${RELAY}"`,
      ),
    )
    await waitFor(
      async () => notices(notifier, "Envoy inbound restored").length === 1,
      "the outage told and then its end",
      () => JSON.stringify(notifier.notifications),
    )
    const told = notices(notifier, "Envoy inbound unavailable")
    expect(told).toHaveLength(1)
    expect(told[0]).toContain(
      "because its relay subject could not be opened: the server refused a permission (its text is withheld)",
    )
    // Taken again, once, on the same connection, and registered at a new epoch.
    expect(relaySubscribes()).toBe(2)
    expect(nats.liveSubscriptions(RELAY)).toBe(1)
    const after = registrations.slice(failedAt)
    expect(after.length).toBeGreaterThan(0)
    // No registration asked the listener to relay to the subscription that had ended.
    for (const registration of after) expect(registration.relaySubscribes).toBe(2)
    expect(after[0]?.input.delivery?.epoch).not.toBe(firstEpoch)
    // A frame on the subject taken again reaches the session.
    nats.emitRelay(SESSION, {
      subject: `notifications.agent.${SESSION}`,
      seq: 4,
      raw: JSON.stringify({
        event_id: "evt-after-relay-loss",
        source: "agent",
        source_session: "ses_peer",
        topic: `notifications.agent.${SESSION}`,
        dedupe_key: "delivery-after-relay-loss",
        issued_at: 1_760_000_000_000,
        payload_summary: "peer message after the relay came back",
      }),
      reply: "_INBOX.after-relay-loss",
    })
    await waitFor(
      async () => nats.repliesOn("_INBOX.after-relay-loss").includes("done"),
      "the frame on the relay taken again",
    )
    expect(
      notifier.notifications.filter((written) => written.eventId === "evt-after-relay-loss"),
    ).toHaveLength(1)
  } finally {
    await session.shutdown()
  }
}, 30_000)

// The relay subject is taken again only once the server has answered a flush behind the
// subscribe: a flush lost to a redial (rejected) or one that never comes back (timed out) says
// nothing of whether the server took the subscription, and a refusal the server sends after the
// reconnect would come after the registration had asked the listener to relay to it and told the
// session it receives again. While the server refuses the subject, no registration asks for it,
// the outage is told once, and nothing tells the session it receives again.
for (const lost of [
  { name: "rejected by a redial", flushTimeoutMs: undefined },
  { name: "timed out", flushTimeoutMs: 50 },
] as const) {
  test(`a relay subscription taken again behind a flush ${lost.name} is not confirmed: nothing asks for it until a flush confirms it`, async () => {
    const SESSION = "ses_relay_unconfirmed"
    const RELAY = deliverSubject(SESSION)
    const nats = new FakeNats()
    const notifier = new FakeNotifier()
    const relaySubscribes = (): number =>
      nats.calls.filter((call) => call === `nats.subscribe ${RELAY}`).length
    const registrations: SubscribeInput[] = []
    const session = await startChannelSession(
      sessionOptions(new SessionIdentity(SESSION, "/tmp"), scratchState(), {
        connection: nats,
        notifier,
        heartbeatMs: 25,
        ...(lost.flushTimeoutMs === undefined
          ? {}
          : { handoffFlushTimeoutMs: lost.flushTimeoutMs }),
        client: recordingClient([], {
          subscribe: async (input) => {
            registrations.push(input)
            return noInterest(input.sessionID)
          },
        }),
      }),
    )
    const refusal = serverRefusal(
      "PERMISSIONS_VIOLATION",
      `Permissions Violation for Subscription to "${RELAY}"`,
    )
    /** Registrations before the outage was told: every one after it asked for the relay. */
    let sentAt = registrations.length
    const counts = () => ({
      restored: notices(notifier, "Envoy inbound restored").length,
      sentWhileRefused: registrations.length - sentAt,
      unavailable: notices(notifier, "Envoy inbound unavailable").length,
    })
    try {
      const firstEpoch = registrations.at(-1)?.delivery?.epoch
      // The server refuses the subject from now on, and the connection redials: no flush comes
      // back until the test says so.
      nats.refuse(RELAY, refusal)
      if (lost.flushTimeoutMs === undefined) nats.failFlushes = Number.POSITIVE_INFINITY
      else nats.stallFlush = true
      nats.failSubscription(RELAY, refusal)
      await waitFor(
        async () => notices(notifier, "Envoy inbound unavailable").length === 1,
        "the outage told",
      )
      sentAt = registrations.length
      // Heartbeats take the subject again behind flushes that never come back.
      const lostAt = relaySubscribes()
      await waitFor(
        async () => relaySubscribes() >= lostAt + 3 || counts().sentWhileRefused > 0,
        "three attempts behind flushes that never came back",
        () => JSON.stringify(counts()),
      )
      // The connection is back: its flushes are answered, and the server's refusal of the
      // subscription it held reaches it; heartbeats take the subject again, and are refused.
      nats.failFlushes = 0
      nats.stallFlush = false
      nats.deliverRefusals()
      const backAt = relaySubscribes()
      await waitFor(
        async () => relaySubscribes() >= backAt + 3,
        "three attempts the server refused",
        () => JSON.stringify(counts()),
      )
      expect(counts()).toEqual({ restored: 0, sentWhileRefused: 0, unavailable: 1 })
      // Every subscription an unconfirmed flush left was given up: none is live while the server
      // refuses the subject. nats.js keeps a subscription the session does not unsubscribe, and
      // the relay's next one would be one more beside it.
      expect(nats.liveSubscriptions(RELAY)).toBe(0)

      // The server takes the subject again: the next heartbeat's flush confirms it, a new epoch
      // is registered, and the end of the outage is told once.
      nats.refuse(RELAY, undefined)
      await waitFor(
        async () => notices(notifier, "Envoy inbound restored").length === 1,
        "the end of the outage told",
        () => JSON.stringify(counts()),
      )
      expect(nats.liveSubscriptions(RELAY)).toBe(1)
      expect(registrations.at(-1)?.delivery?.epoch).not.toBe(firstEpoch)
      expect(notices(notifier, "Envoy inbound unavailable")).toHaveLength(1)
    } finally {
      await session.shutdown()
    }
  }, 30_000)
}

test("a connection closed for good between a /clear's deregistration and its registration tells the outage once, and the /clear completes on the new connection", async () => {
  const SESSION = "ses_clear_closed"
  const NEXT = "ses_clear_closed_next"
  const directory = scratchState()
  const handoff = sessionHandoffFile(directory, 779)
  await writeSessionHandoff(handoff, SESSION)
  const first = new FakeNats()
  const second = new FakeNats()
  const notifier = new FakeNotifier()
  /** Which connection each credential move ran on. */
  const moves: string[] = []
  const follow = (connection: FakeNats, name: string) => async (): Promise<void> => {
    moves.push(name)
    // `followNatsCredential` on a connection closed for good fails, as it waits for a reconnect
    // that never comes.
    if (connection.isClosed()) throw connectionClosedError()
  }
  const reopened = Promise.withResolvers<void>()
  const registered: string[] = []
  const session = await startChannelSession(
    sessionOptions(new SessionIdentity(SESSION, "/tmp"), directory, {
      connection: first,
      followSession: follow(first, "first"),
      notifier,
      heartbeatMs: 25,
      handoffPid: 779,
      reopen: async () => {
        await reopened.promise
        return { connection: second, followSession: follow(second, "second") }
      },
      client: recordingClient([], {
        subscribe: async (input) => {
          registered.push(input.sessionID)
          return noInterest(input.sessionID)
        },
        unregisterSession: async ({ sessionID }) => {
          // The hub closes the connection as the old id leaves, before the new id registers.
          if (sessionID === SESSION) {
            await first.closeForGood(
              serverRefusal("AUTHORIZATION_VIOLATION", "'Authorization Violation'"),
            )
          }
        },
      }),
    }),
  )
  try {
    await writeSessionHandoff(handoff, NEXT)
    await waitFor(
      async () => notices(notifier, "Envoy inbound unavailable").length === 1,
      "the outage told",
      () => JSON.stringify({ moves, registered }),
    )
    expect(notices(notifier, "Envoy inbound unavailable")[0]).toContain(
      "because its relay subject could not be opened: Authorization Violation",
    )
    // The new id's file was bound at the handoff, so the outage it was told is recorded there
    // while no connection is live: a server Claude Code starts again on the new id meanwhile
    // tells it the outage's end.
    await waitFor(
      async () => (await toldInFile(directory, NEXT)) === true,
      "the new id's file recording its outage before the connection is back",
      () => JSON.stringify({ moves, registered }),
    )
    // Several heartbeats pass with no connection, and none moves the credential on the closed one
    // or flushes or subscribes on it. An absence needs the time to pass: eight 25 ms heartbeats.
    const callsBefore = first.flushes + first.subscribes
    await Bun.sleep(200)
    expect(moves).toEqual([])
    expect(first.flushes + first.subscribes).toBe(callsBefore)
    expect(registered).not.toContain(NEXT)

    reopened.resolve()
    await waitFor(
      async () =>
        registered.includes(NEXT) &&
        second.subscriptions.has(deliverSubject(NEXT)) &&
        notices(notifier, "Envoy inbound restored").length === 1,
      "the /clear completed on the new connection, and the end told",
      () => JSON.stringify({ moves, registered }),
    )
    expect(moves).toEqual(["second"])
    expect(notices(notifier, "Envoy inbound unavailable")).toHaveLength(1)
    expect(notices(notifier, "Envoy inbound restored")).toHaveLength(1)
  } finally {
    await session.shutdown()
  }
}, 30_000)

// The other order: the connection closes for good while the session runs, the session is told
// it receives nothing, and only then does a `/clear` land. The handoff waits for a live
// connection (its flush is refused on the closed one), so the id the server registers under is
// still the old one when the new connection opens. The registration that the new connection
// carries must not tell the end of the old id's outage to the conversation Claude Code moved to,
// and must not clear the old id's record: the old conversation never saw that end, so a server
// that resumes the old id later still owes it.
test("a /clear made after the connection closed for good tells the new conversation nothing of the old id's outage, and the old id's file keeps it told", async () => {
  const SESSION = "ses_closed_then_clear"
  const NEXT = "ses_closed_then_clear_next"
  const directory = scratchState()
  const handoff = sessionHandoffFile(directory, 781)
  await writeSessionHandoff(handoff, SESSION)
  const first = new FakeNats()
  const second = new FakeNats()
  const notifier = new FakeNotifier()
  const reopened = Promise.withResolvers<void>()
  const registered: string[] = []
  const session = await startChannelSession(
    sessionOptions(new SessionIdentity(SESSION, "/tmp"), directory, {
      connection: first,
      followSession: async () => undefined,
      notifier,
      heartbeatMs: 25,
      handoffPid: 781,
      reopen: async () => {
        await reopened.promise
        return { connection: second, followSession: async () => undefined }
      },
      client: recordingClient([], {
        subscribe: async (input) => {
          registered.push(input.sessionID)
          return noInterest(input.sessionID)
        },
      }),
    }),
  )
  try {
    await first.closeForGood(serverRefusal("AUTHORIZATION_VIOLATION", "'Authorization Violation'"))
    await waitFor(
      async () => (await toldInFile(directory, SESSION)) === true,
      "the old id told its outage, and its file recording it",
      () => JSON.stringify({ registered }),
    )
    expect(notices(notifier, "Envoy inbound unavailable")).toHaveLength(1)

    // Claude Code moves to the new conversation. Several heartbeats pass with no connection, and
    // none takes the handoff up: none flushes the closed connection for it. A handoff taken up
    // here would run on the connection a reopen sets before that connection is admitted, and tell
    // the new id a refusal and then its end. An absence needs the time to pass, eight 25 ms
    // heartbeats; no fake clock drives the server's own heartbeat interval.
    const flushesBefore = first.flushes
    await writeSessionHandoff(handoff, NEXT)
    await Bun.sleep(200)
    expect(registered).not.toContain(NEXT)
    expect(first.flushes).toBe(flushesBefore)

    reopened.resolve()
    await waitFor(
      async () => registered.includes(NEXT) && second.subscriptions.has(deliverSubject(NEXT)),
      "the /clear completed on the new connection",
      () => JSON.stringify({ registered }),
    )
    // One more registration of the new id, so an answer the reopen owed has been handled.
    const after = registered.length
    await waitFor(
      async () => registered.length >= after + 2,
      "two more registrations",
      () => JSON.stringify({ registered }),
    )
    expect(notices(notifier, "Envoy inbound restored")).toEqual([])
    expect(notices(notifier, "Envoy inbound unavailable")).toHaveLength(1)
    expect(await toldInFile(directory, SESSION)).toBe(true)
    expect(await toldInFile(directory, NEXT)).not.toBe(true)
  } finally {
    await session.shutdown()
  }
}, 30_000)

// The `/clear` comes first, and the connection closes for good before any heartbeat takes it up:
// the registration the close sends still goes out under the id Claude Code left, and is refused
// for want of a relay subject. Claude Code is on the new conversation already, so the old id's
// outage told there would reach a conversation that never had it, be recorded in the old id's
// file as told to a conversation that never saw it, and leave the new id never told its end. The
// `/clear` waits for the new connection (`handoffWaiting`), and nothing is told meanwhile.
test("a /clear not yet taken up when the connection closes for good tells the new conversation no outage of the old id's, and records none", async () => {
  const SESSION = "ses_clear_then_closed"
  const NEXT = "ses_clear_then_closed_next"
  const directory = scratchState()
  const handoff = sessionHandoffFile(directory, 785)
  await writeSessionHandoff(handoff, SESSION)
  const first = new FakeNats()
  const second = new FakeNats()
  const notifier = new FakeNotifier()
  const reopenAsked = Promise.withResolvers<void>()
  const reopened = Promise.withResolvers<void>()
  const registered: string[] = []
  // A registration the row holds at the listener while the `/clear` is written and the connection
  // closes, so no heartbeat can take the `/clear` up on the live connection in between.
  let hold: PromiseWithResolvers<void> | undefined
  const holding = Promise.withResolvers<void>()
  const session = await startChannelSession(
    sessionOptions(new SessionIdentity(SESSION, "/tmp"), directory, {
      connection: first,
      followSession: async () => undefined,
      notifier,
      handoffPid: 785,
      reopen: async () => {
        reopenAsked.resolve()
        await reopened.promise
        return { connection: second, followSession: async () => undefined }
      },
      client: recordingClient([], {
        subscribe: async (input) => {
          registered.push(input.sessionID)
          const held = hold
          hold = undefined
          if (held !== undefined) {
            holding.resolve()
            await held.promise
          }
          return noInterest(input.sessionID)
        },
      }),
    }),
  )
  const released = Promise.withResolvers<void>()
  try {
    hold = released
    const following = session.follow(["notifications.team.deploy"]).catch(() => [])
    await holding.promise
    await writeSessionHandoff(handoff, NEXT)
    await first.closeForGood(serverRefusal("AUTHORIZATION_VIOLATION", "'Authorization Violation'"))
    // The close is taken up (`connectionClosed`): its registration waits behind the held one.
    await reopenAsked.promise
    released.resolve()
    await following
    // One more registration through the chain, so the close's own, under the old id and refused,
    // has run, and a notice it told has been shown, before the new connection opens.
    await session.follow(["notifications.team.deploy"]).catch(() => [])
    await settled()
    reopened.resolve()
    await waitFor(
      async () => registered.includes(NEXT) && second.subscriptions.has(deliverSubject(NEXT)),
      "the /clear completed on the new connection",
      () => JSON.stringify({ registered }),
    )
    expect(notices(notifier, "Envoy inbound unavailable")).toEqual([])
    expect(notices(notifier, "Envoy inbound restored")).toEqual([])
    expect(await toldInFile(directory, SESSION)).not.toBe(true)
    expect(await toldInFile(directory, NEXT)).not.toBe(true)
  } finally {
    released.resolve()
    reopened.resolve()
    await session.shutdown()
  }
}, 30_000)

// The connection closes for good, the session is told it receives nothing, and a `/clear` lands.
// On the new connection the `/clear` is taken up first, and the listener refuses the old id's
// DELETE once: the take-up fails before the id switch, and the registration after it goes out
// under the old id and is accepted. The end of the old id's outage told there would reach the
// conversation Claude Code moved to, and the old id's file would record an end its own
// conversation never saw. The `/clear` waits (`handoffWaiting`) for the next heartbeat.
test("a /clear whose old id's DELETE is refused once on the new connection tells the new conversation no end of the old id's outage, and the old id's file keeps it told", async () => {
  const SESSION = "ses_closed_clear_refused"
  const NEXT = "ses_closed_clear_refused_next"
  const directory = scratchState()
  const handoff = sessionHandoffFile(directory, 787)
  await writeSessionHandoff(handoff, SESSION)
  const first = new FakeNats()
  const second = new FakeNats()
  const notifier = new FakeNotifier()
  const reopened = Promise.withResolvers<void>()
  const registered: string[] = []
  let deleteRefusals = 0
  const session = await startChannelSession(
    sessionOptions(new SessionIdentity(SESSION, "/tmp"), directory, {
      connection: first,
      followSession: async () => undefined,
      notifier,
      heartbeatMs: 25,
      handoffPid: 787,
      reopen: async () => {
        await reopened.promise
        return { connection: second, followSession: async () => undefined }
      },
      client: recordingClient([], {
        subscribe: async (input) => {
          registered.push(input.sessionID)
          return noInterest(input.sessionID)
        },
        // The old id's first DELETE is refused: the take-up on the new connection's registration.
        unregisterSession: async ({ sessionID }) => {
          if (sessionID !== SESSION || deleteRefusals > 0) return
          deleteRefusals += 1
          throw new Error("listener unavailable")
        },
      }),
    }),
  )
  try {
    await first.closeForGood(serverRefusal("AUTHORIZATION_VIOLATION", "'Authorization Violation'"))
    await waitFor(
      async () => (await toldInFile(directory, SESSION)) === true,
      "the old id told its outage, and its file recording it",
      () => JSON.stringify({ registered }),
    )
    expect(notices(notifier, "Envoy inbound unavailable")).toHaveLength(1)
    await writeSessionHandoff(handoff, NEXT)

    // No registration reaches the listener while no connection is live: the next is the reopen's.
    const reopenedAt = registered.length
    reopened.resolve()
    await waitFor(
      async () => registered.includes(NEXT) && second.subscriptions.has(deliverSubject(NEXT)),
      "the /clear completed on the new connection",
      () => JSON.stringify({ registered, deleteRefusals }),
    )
    // The old id's DELETE was refused, and the old id registered on the new connection, accepted,
    // before the next heartbeat took the `/clear` up.
    expect(deleteRefusals).toBe(1)
    expect(registered.slice(reopenedAt, registered.indexOf(NEXT))).toContain(SESSION)
    expect(notices(notifier, "Envoy inbound restored")).toEqual([])
    expect(notices(notifier, "Envoy inbound unavailable")).toHaveLength(1)
    expect(await toldInFile(directory, SESSION)).toBe(true)
    expect(await toldInFile(directory, NEXT)).not.toBe(true)
  } finally {
    reopened.resolve()
    await session.shutdown()
  }
}, 30_000)

// A relay subscription that ends while a heartbeat's registration is still on the wire queues a
// registration behind it, which names the outage. Shutdown then closes the relay and sends the
// DELETE once its bounded wait for the chain gives up. The queued registration runs after that:
// it must tell the leaving session nothing (`inboundOutage` says nothing once shutdown began),
// which the shutdown guards alone ensure. That it registers nothing after the DELETE is kept by
// more than `register`'s own guard: the stopped relay sets `relayMissing`, so `registerWithRelay`
// sends nothing, and `closeRelay()` removes the relay; this row cannot tell those apart.
test("a registration queued behind a held listener call as the session shuts down tells no outage and registers nothing after the DELETE", async () => {
  const SESSION = "ses_relay_shutdown"
  const RELAY = deliverSubject(SESSION)
  const nats = new FakeNats()
  const notifier = new FakeNotifier()
  const calls: string[] = []
  // The start's registration is answered at once; the first heartbeat's is held until the test
  // answers it, after the DELETE.
  const held = Promise.withResolvers<void>()
  const heldAsked = Promise.withResolvers<void>()
  let registrations = 0
  const session = await startChannelSession(
    sessionOptions(new SessionIdentity(SESSION, "/tmp"), scratchState(), {
      connection: nats,
      notifier,
      heartbeatMs: 25,
      // The shutdown's wait for the chain gives up quickly, so the DELETE goes out while the
      // heartbeat's registration still holds the chain.
      registrationSettleMs: 100,
      client: recordingClient(calls, {
        subscribe: async (input) => {
          calls.push(`subscribe ${input.sessionID}`)
          registrations += 1
          if (registrations > 1) {
            heldAsked.resolve()
            await held.promise
          }
          return noInterest(input.sessionID)
        },
      }),
    }),
  )
  try {
    await heldAsked.promise
    // The relay subscription ends with the server's refusal: a registration is queued behind the
    // held one, and would refuse itself and name the outage.
    nats.failSubscription(
      RELAY,
      serverRefusal(
        "PERMISSIONS_VIOLATION",
        `Permissions Violation for Subscription to "${RELAY}"`,
      ),
    )
    await waitFor(async () => nats.liveSubscriptions(RELAY) === 0, "the relay subscription ended")

    await session.shutdown()
    const deregisteredAt = calls.indexOf(`unregister ${SESSION}`)
    expect(deregisteredAt).toBeGreaterThan(-1)

    // The held registration is answered now, after the DELETE, and the queued one runs behind it.
    // Its absence needs the time to pass: a few turns are ample for a chain with no wait left.
    held.resolve()
    await Bun.sleep(200)
    expect(notices(notifier, "Envoy inbound unavailable")).toEqual([])
    expect(notices(notifier, "Envoy inbound restored")).toEqual([])
    expect(calls.slice(deregisteredAt + 1).filter((call) => call.startsWith("subscribe "))).toEqual(
      [],
    )
  } finally {
    held.resolve()
    await session.shutdown()
  }
}, 30_000)
