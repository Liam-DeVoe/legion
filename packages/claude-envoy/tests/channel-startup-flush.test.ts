// The startup flush outlasts a redial: nats.js rejects every pending flush with DISCONNECT each
// time the connection redials, and a channel server whose hub was restarting as it started must
// come up once the connection is back, not end the Claude Code session's channel for good.
import { expect, test } from "bun:test"
import { startChannelSession } from "../src/envoy-channel-server"
import { SessionIdentity } from "../src/session-identity"
import {
  FakeNats,
  recordingClient,
  sessionOptions,
  useScratchState,
  waitFor,
} from "./channel-session-harness"
import { isolatePaneEnvironment } from "./pane-environment"

isolatePaneEnvironment()

const scratchState = useScratchState()

test("a startup flush the redial rejects is asked again, and the session registers", async () => {
  const calls: string[] = []
  const nats = new FakeNats(calls)
  nats.failFlushes = 2
  const identity = new SessionIdentity("ses_redial", "/tmp")
  const session = await startChannelSession(
    sessionOptions(identity, scratchState(), {
      connection: nats,
      client: recordingClient(calls),
      heartbeatMs: 60_000,
    }),
  )
  try {
    await waitFor(
      async () => calls.some((call) => call.startsWith("subscribe ses_redial")),
      "the first registration",
    )
    // Two flushes the redial rejected, then the one that reached the server.
    expect(nats.flushes).toBe(3)
  } finally {
    await session.shutdown()
  }
})

test("a startup flush on a connection closed for good still fails the start", async () => {
  const nats = new FakeNats([])
  nats.failFlushes = 1
  nats.closed = true
  const identity = new SessionIdentity("ses_closed", "/tmp")
  const started = startChannelSession(
    sessionOptions(identity, scratchState(), {
      connection: nats,
      client: recordingClient([]),
      heartbeatMs: 60_000,
    }),
  )
  await expect(started).rejects.toThrow(
    "the NATS connection closed before its subscriptions reached the server: DISCONNECT",
  )
})
