import { expect, spyOn, test } from "bun:test"
import * as fsPromises from "node:fs/promises"
import { mkdir, readFile, writeFile } from "node:fs/promises"
import { dirname, join, resolve } from "node:path"
import { createLegionDaemonClient } from "@legion/pi-shared/daemon-client"
import { legion } from "../package.json" with { type: "json" }
import { startChannelSession } from "../src/envoy-channel-server"
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
  noInterest,
  recordingClient,
  sessionOptions,
  settled,
  useScratchState,
  waitFor,
} from "./channel-session-harness"
import { isolatePaneEnvironment } from "./pane-environment"

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
