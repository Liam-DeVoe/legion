import { expect, spyOn, test } from "bun:test"
import * as fsPromises from "node:fs/promises"
import { mkdir, readFile, writeFile } from "node:fs/promises"
import { dirname, join, resolve } from "node:path"
import { agentStreamControlSubject } from "@legion/contracts"
import { createEnvoyClient } from "@legion/envoy-client/transport"
import { createLegionDaemonClient } from "@legion/pi-shared/daemon-client"
import { modSocketPath } from "../hooks/mod-protocol"
import { legion } from "../package.json" with { type: "json" }
import { LIVE_VIEW_READ_FAILED } from "../src/agent-stream-runner"
import {
  type ChannelNotifier,
  DELIVERY_FAILED_LINE,
  executeEnvoyTool,
  startChannelSession,
} from "../src/envoy-channel-server"
import { inboundOffChannelSession } from "../src/envoy-off"
import { startLegionClaim } from "../src/legion-claim"
import {
  readPersistedRole,
  roleStateFile,
  SessionIdentity,
  sessionHandoffFile,
  writePersistedRole,
  writeSessionHandoff,
} from "../src/session-identity"
import {
  deliveryRaw,
  directSubject,
  FakeNats,
  noInterest,
  recordingClient,
  sessionOptions,
  settled,
  useScratchState,
  waitFor,
} from "./channel-session-harness"
import { type FakeMod, startFakeMod } from "./fake-mod"
import { isolatePaneEnvironment } from "./pane-environment"
import { PROCESS_WAIT_MS, ROW_TIMEOUT_MS, startReopenServer } from "./reopen-server"

// A session whose stop has begun registers, unsubscribes and claims nothing, and writes no role
// record (R24 of dispatch://INFRA-3589), whichever path is still running when the stop begins:
// the Legion claim's first-turn bound, `unfollow`, a role restore whose removal of the old record
// the stop overtakes, and the rest of a start the stop overtakes.

isolatePaneEnvironment()

const scratchState = useScratchState()

/** Holds a call on the wire until the row resolves what this answers. */
function held(): { readonly promise: Promise<void>; readonly resolve: () => void } {
  return Promise.withResolvers<void>()
}

// A Legion pane's claim boot registers and says ready, and the claim waits for the first turn or
// its bound. The server's stop begins before either: it aborts the boot's signal, and its
// shutdown's DELETE is still on the wire when the bound would have come. The bound does not fire:
// the claim sends no role claim and writes no role record.
test("the Legion claim's bound firing during shutdown() sends no claim and writes no role record", async () => {
  const session = "0b9c2f4e-2d3a-4c55-9a1e-6f7d8e9a0b1c"
  const stateDirectory = scratchState()
  const configDir = scratchState()
  await mkdir(join(configDir, "projects", "p"), { recursive: true })
  await writeFile(join(configDir, "projects", "p", `${session}.jsonl`), "{}\n")
  const registered: Record<string, unknown> = JSON.parse(
    await readFile(
      resolve(import.meta.dir, "../../contracts/fixtures/daemon-api", "register.json"),
      "utf8",
    ),
  )
  const daemonFetch = (async (input: string | URL | Request) =>
    new URL(input.toString()).pathname.endsWith("/claims/register")
      ? Response.json(registered)
      : new Response(null, { status: 204 })) as typeof fetch
  const booting = new AbortController()
  const deleting = held()
  const calls: string[] = []
  const client = recordingClient(calls, {
    unregisterSession: async ({ sessionID }) => {
      calls.push(`unregister ${sessionID}`)
      await deleting.promise
    },
  })
  const channel = await startChannelSession(
    sessionOptions(new SessionIdentity(session, "/tmp"), stateDirectory, {
      client,
      startSignal: booting.signal,
    }),
  )
  const claim = startLegionClaim({
    pane: { daemonUrl: "http://daemon.test", bootToken: "boot-1", configDir, launcherSaves: false },
    sessionID: session,
    daemon: createLegionDaemonClient("http://daemon.test", {
      pluginContract: legion.daemonApiVersion,
      fetch: daemonFetch,
    }),
    clientInstanceID: "a1b2c3d4e5f60718293a4b5c6d7e8f90",
    client,
    session: channel,
    signal: booting.signal,
    log: () => undefined,
    stop: () => undefined,
    // No turn starts: only the bound can claim here.
    onFirstTurn: () => undefined,
    env: { LEGION_CLAIM_FIRST_TURN_BOUND_MS: "300" },
    pollMs: 5,
  })
  let stopping: Promise<void> | undefined
  try {
    await claim.booted
    // The server's stop: the abort first, then the shutdown, whose DELETE is held.
    booting.abort()
    stopping = channel.shutdown()
    await waitFor(async () => calls.includes(`unregister ${session}`), "the stop's DELETE")
    // The claim step answers once it ran, or once the stop took it off.
    await claim.claimed
    await settled()

    expect(calls.filter((call) => call.startsWith("setRole"))).toEqual([])
    expect(await readPersistedRole(roleStateFile(stateDirectory, session))).toBeUndefined()
  } finally {
    booting.abort()
    deleting.resolve()
    await stopping
    await channel.shutdown()
  }
})

// A session follows a topic, and its stop begins: the shutdown's DELETE is still on the wire when
// the topic is unfollowed. The session drops nothing and sends no removal, as `follow` adds
// nothing once the stop has begun.
test("unfollow once the stop has begun drops nothing and sends no removal", async () => {
  const session = "ses_unfollow_while_stopping"
  const topic = "notifications.dispatch.issue.DSP-7.>"
  const deleting = held()
  const calls: string[] = []
  const client = recordingClient(calls, {
    unregisterSession: async ({ sessionID }) => {
      calls.push(`unregister ${sessionID}`)
      await deleting.promise
    },
  })
  const channel = await startChannelSession(
    sessionOptions(new SessionIdentity(session, "/tmp"), scratchState(), { client }),
  )
  let stopping: Promise<void> | undefined
  try {
    expect(await channel.follow([topic])).not.toEqual([])
    stopping = channel.shutdown()
    await waitFor(async () => calls.includes(`unregister ${session}`), "the stop's DELETE")
    const before = calls.length

    const removed = await channel.unfollow([topic])
    await settled()

    expect(removed).toEqual([])
    expect(calls.slice(before)).toEqual([])
  } finally {
    deleting.resolve()
    await stopping
    await channel.shutdown()
  }
})

// A `/clear` moves the session to a new id, and the heartbeat takes the role back from the old
// id's record: the claim lands, the old record is removed, and the new one is written. The stop
// begins while that removal runs. The new record is not written.
test("a role restore whose removal of the old record the stop overtakes writes no new record", async () => {
  const stateDirectory = scratchState()
  const booting = new AbortController()
  const options = sessionOptions(new SessionIdentity("ses_old", "/tmp"), stateDirectory, {
    client: recordingClient([], {
      // The listener names this session the holder once a claim landed, so no heartbeat claims
      // again on its own.
      getRole: async (role) => ({ role, holder: "ses_new", last_seen: 1, state: "live" }),
      subscribe: async (input) => noInterest(input.sessionID),
    }),
    heartbeatMs: 25,
    handoffPid: 881,
    startSignal: booting.signal,
  })
  const oldRecord = roleStateFile(stateDirectory, "ses_old")
  const newRecord = roleStateFile(stateDirectory, "ses_new")
  await mkdir(dirname(oldRecord), { recursive: true })
  await writePersistedRole(oldRecord, {
    session_id: "ses_old",
    role: "reviewer",
    client_instance_id: options.clientInstanceID,
  })
  const handoff = sessionHandoffFile(stateDirectory, 881)
  await writeSessionHandoff(handoff, "ses_old")
  // The removal of the old record begins the stop as it starts: the server's stop aborts the
  // start signal, which is the session's stop reading.
  const realRm = fsPromises.rm
  const removing = held()
  const remove = spyOn(fsPromises, "rm").mockImplementation(async (path, rmOptions) => {
    if (String(path) === oldRecord) {
      booting.abort()
      removing.resolve()
    }
    return realRm(path, rmOptions)
  })
  const channel = await startChannelSession(options)
  try {
    await writeSessionHandoff(handoff, "ses_new")
    await removing.promise
    await channel.shutdown()
    await settled()

    expect(await readPersistedRole(newRecord)).toBeUndefined()
  } finally {
    remove.mockRestore()
    booting.abort()
    await channel.shutdown()
  }
})

// The server's stop begins while the session still starts, after the start's connection
// confirmed its subscriptions: the start's first registration is being built. Nothing of the
// start goes on after it: no registration is sent, no role is claimed back from the session's
// record, and the record is left as it was.
test("a stop that begins after the start's flush registers nothing, claims nothing and writes no role record", async () => {
  const session = "ses_stopped_while_starting"
  const stateDirectory = scratchState()
  const record = roleStateFile(stateDirectory, session)
  await mkdir(dirname(record), { recursive: true })
  // A record another process wrote, which a process holding no role takes back at its start.
  const written = { session_id: session, role: "reviewer", client_instance_id: "another-process" }
  await writePersistedRole(record, written)
  const booting = new AbortController()
  const calls: string[] = []
  const channel = await startChannelSession(
    sessionOptions(new SessionIdentity(session, "/tmp"), stateDirectory, {
      client: recordingClient(calls),
      startSignal: booting.signal,
      // The title is read as the first registration is built: the server's stop lands then.
      title: () => {
        booting.abort()
        return ""
      },
    }),
  )
  try {
    expect(
      calls.filter((call) => call.startsWith("subscribe") || call.startsWith("setRole")),
    ).toEqual([])
    expect(await readPersistedRole(record)).toEqual(written)
  } finally {
    await channel.shutdown()
  }
})

/** A session whose one queued delivery's write fails once the row lets it (`failing`), after the
 *  notifier was handed it (`writing`); `booting` is the server's stop reading. */
async function failingDelivery() {
  const booting = new AbortController()
  const writing = held()
  const failing = held()
  const notifier: ChannelNotifier = {
    notification: async () => {
      writing.resolve()
      await failing.promise
      throw new Error("the bridge did not take the message")
    },
  }
  const channel = await startChannelSession(
    sessionOptions(new SessionIdentity("ses_claude", "/tmp"), scratchState(), {
      notifier,
      startSignal: booting.signal,
    }),
  )
  return { booting, writing, failing, channel }
}

/** Every byte written to stderr from now until `restore`, which nothing else then sees. */
function stderrWrites(): { readonly written: string[]; readonly restore: () => void } {
  const written: string[] = []
  const stderr = spyOn(process.stderr, "write").mockImplementation((chunk) => {
    written.push(String(chunk))
    return true
  })
  return { written, restore: () => stderr.mockRestore() }
}

// A frame queued before the stop whose write fails after the stop began: the queue's failure line
// is a stderr line of the session's own, so its tell gate drops it.
test("a delivery queued before the stop that fails after the stop began says nothing on stderr", async () => {
  const { booting, writing, failing, channel } = await failingDelivery()
  const stderr = stderrWrites()
  try {
    const delivered = channel.delivery
      .enqueue({ subject: directSubject, raw: deliveryRaw })
      .catch(() => false)
    await writing.promise
    // The server's stop: the abort is the session's stop reading.
    booting.abort()
    failing.resolve()
    expect(await delivered).toBe(false)
    await settled()

    expect(stderr.written.filter((line) => line.startsWith(DELIVERY_FAILED_LINE))).toEqual([])
  } finally {
    stderr.restore()
    await channel.shutdown()
  }
})

// The control of the row above: the same failed write with no stop begun says so on stderr.
test("control: a delivery that fails with no stop begun says so on stderr", async () => {
  const { writing, failing, channel } = await failingDelivery()
  const stderr = stderrWrites()
  try {
    const delivered = channel.delivery
      .enqueue({ subject: directSubject, raw: deliveryRaw })
      .catch(() => false)
    await writing.promise
    failing.resolve()
    expect(await delivered).toBe(false)
    await settled()

    expect(stderr.written.filter((line) => line.startsWith(DELIVERY_FAILED_LINE))).toEqual([
      `${DELIVERY_FAILED_LINE}the bridge did not take the message\n`,
    ])
  } finally {
    stderr.restore()
    await channel.shutdown()
  }
})

/** A connection whose drain and close fail, and stay open, as a stalled link's can. */
class UnclosableNats extends FakeNats {
  override drain(): Promise<void> {
    return Promise.reject(new Error("the drain failed"))
  }

  override close(): Promise<void> {
    return Promise.reject(new Error("the close failed"))
  }
}

// The stop's own lines are written while the stop runs (R24 of dispatch://INFRA-3589): the
// shutdown writes its failed DELETE, and the drain and the close of its connection that failed
// (`ChannelTopics.close`), to stderr with no tell gate, which drops every other line of the
// session's own from the moment the stop begins.
test("a shutdown whose DELETE, drain and close fail writes those three lines on stderr, and no other line", async () => {
  const connection = new UnclosableNats()
  const channel = await startChannelSession(
    sessionOptions(new SessionIdentity("ses_claude", "/tmp"), scratchState(), {
      connection,
      client: recordingClient([], {
        unregisterSession: async () => {
          throw new Error("the listener did not answer")
        },
      }),
    }),
  )
  const stderr = stderrWrites()
  try {
    await channel.shutdown()

    // `natsFailureReason` names an error nats.js did not raise by its class alone.
    expect(stderr.written).toEqual([
      "envoy-channel: draining the broker connection failed — Error (message withheld)\n",
      "envoy-channel: closing the broker connection failed — Error (message withheld)\n",
      "envoy-channel: session deregistration failed — the listener did not answer\n",
    ])
  } finally {
    stderr.restore()
    await connection.closeForGood()
  }
})

// An inbound-off session (`./envoy-off`) reads the server's stop too: a role claim that was on the
// wire when the stop began is refused when its answer comes, and leaves no record.
test("inbound-off: an envoy_role_set claim the listener answers after the stop began keeps no record", async () => {
  const stateDirectory = scratchState()
  const identity = new SessionIdentity("ses_inbound_off", "/tmp")
  let stopping = false
  const session = inboundOffChannelSession({
    identity,
    stateDirectory,
    why: "no NATS server is configured",
    stopping: () => stopping,
  })
  const client = createEnvoyClient({
    baseUrl: "http://envoy.test",
    // The server's stop begins while the claim is on the wire; the listener grants it.
    fetch: async () => {
      stopping = true
      return Response.json({
        session_id: identity.id,
        machine_id: "devbox",
        dir: "/work",
        topics: ["notifications.role.reviewer"],
      })
    },
  })

  await expect(
    executeEnvoyTool(
      { identity, client, session, clientInstanceID: "a1b2c3d4e5f60718293a4b5c6d7e8f90" },
      "envoy_role_set",
      { role: "reviewer" },
    ),
  ).rejects.toThrow(
    "this session is stopping, so it claims no role: role reviewer is not held here",
  )
  await session.rememberRole("reviewer")
  expect(await readPersistedRole(roleStateFile(stateDirectory, identity.id))).toBeUndefined()
})

// The live view's reads of the mod are on the wire when the server's stop begins; the stop closes
// the mod's link, which rejects them. The view's warnings go through the server's tell gate, which
// drops them. Whole processes: the mod is `./fake-mod`, which never answers those reads.
test(
  "the live view's reads the stop's close of the mod link ends say nothing on stderr",
  async () => {
    const sessionId = "ses_view_at_stop"
    const run = await startReopenServer({
      sessionId,
      env: (stateDirectory) => ({
        CLAUDE_CODE_SESSION_ID: sessionId,
        CLAUDE_PLUGIN_DATA: stateDirectory,
      }),
    })
    let mod: FakeMod | undefined
    try {
      const socketPath = modSocketPath(sessionId, { XDG_RUNTIME_DIR: run.stateDirectory })
      if (socketPath === undefined) throw new Error("the session names no mod socket")
      const never = new Promise<never>(() => undefined)
      mod = await startFakeMod(socketPath, {
        sessionId,
        answers: { "session.model": () => never, "command.list": () => never },
      })
      await waitFor(
        async () => run.broker.subscribed.includes(agentStreamControlSubject(sessionId)),
        "the live view started",
        run.state,
        PROCESS_WAIT_MS,
      )
      // An event of the conversation: the view reads the session's model and commands.
      await mod.send({ kind: "turn-start", turnId: "turn-at-stop" })
      const asked = mod
      await waitFor(
        async () => asked.madeOf("session.model").length > 0,
        "the live view's read of the model on the wire",
        run.state,
        PROCESS_WAIT_MS,
      )

      run.server.kill("SIGTERM")
      await run.server.exited
      await run.stderrEnded

      expect(run.stderr()).not.toContain(LIVE_VIEW_READ_FAILED)
    } finally {
      await mod?.stop()
      await run.stop()
    }
  },
  ROW_TIMEOUT_MS,
)

// The control of the row above: no stop begins, and the mod answers the view's read of the model
// with an error. The view's warning is on stderr. The same whole processes.
test(
  "control: a live view read that fails with no stop begun says so on stderr",
  async () => {
    const sessionId = "ses_view_no_stop"
    const run = await startReopenServer({
      sessionId,
      env: (stateDirectory) => ({
        CLAUDE_CODE_SESSION_ID: sessionId,
        CLAUDE_PLUGIN_DATA: stateDirectory,
      }),
    })
    let mod: FakeMod | undefined
    try {
      const socketPath = modSocketPath(sessionId, { XDG_RUNTIME_DIR: run.stateDirectory })
      if (socketPath === undefined) throw new Error("the session names no mod socket")
      mod = await startFakeMod(socketPath, {
        sessionId,
        answers: {
          "session.model": () => Promise.reject(new Error("the session's model is unreadable")),
        },
      })
      await waitFor(
        async () => run.broker.subscribed.includes(agentStreamControlSubject(sessionId)),
        "the live view started",
        run.state,
        PROCESS_WAIT_MS,
      )
      // An event of the conversation: the view reads the session's model, and the read fails.
      await mod.send({ kind: "turn-start", turnId: "turn-no-stop" })
      await waitFor(
        async () => run.stderr().includes(LIVE_VIEW_READ_FAILED),
        "the live view's warning on stderr",
        run.state,
        PROCESS_WAIT_MS,
      )

      expect(run.stderr()).toContain(`envoy-channel: ${LIVE_VIEW_READ_FAILED}model — `)
    } finally {
      await mod?.stop()
      await run.stop()
    }
  },
  ROW_TIMEOUT_MS,
)
