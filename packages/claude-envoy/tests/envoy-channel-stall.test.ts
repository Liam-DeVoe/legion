import { afterAll, beforeAll, describe, expect, test } from "bun:test"
import { existsSync, readFileSync } from "node:fs"
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join, resolve } from "node:path"
import {
  type EnvoyStack,
  envoyStackEnabled,
  startEnvoyStack,
  useEnvoyStackDirectory,
} from "@legion/envoy-client/test/envoy-stack"
import { type StallProxy, startStallProxy } from "@legion/envoy-client/test/stall-proxy"
import {
  expectRecoveredByPingOut,
  type NatsDial,
  RECOVERY_BOUND_MS,
  recoveryBoundMs,
  runFreezeScenario,
} from "@legion/envoy-client/test/stall-scenario"
import { bunCacheOff } from "@legion/test-fixtures/bun-child-environment"
import { settled, until } from "@legion/test-fixtures/wait"
import type { Subprocess } from "bun"
import { type ModCallArguments, modSocketPath } from "../hooks/mod-protocol"
import { CHANNEL_RECORD_FILE, type ChannelRecordLine } from "../src/channel-record"
import { startFakeMod } from "./fake-mod"

const channelEntry = resolve(import.meta.dir, "..", "bin", "envoy-channel.ts")

/** The exit this file held a server to on SIGTERM before its waits ended at the row's deadline:
 *  the bounded waits a shutdown arms on its way out must sum to less. */
const EXIT_BOUND_MS = 15_000

/** What one `session.message` the server made carries: the message's own words, which the mod
 *  writes into the session as the prompt of a new turn or as a row of the turn running
 *  (INFRA-1026). */
function shownText(args: ModCallArguments["session.message"]): string {
  return args.message.text
}

/** A channel server process, initialized, whose only NATS path goes through `proxy`. */
interface ChannelServer {
  readonly server: Subprocess<"pipe", "pipe", "pipe">
  readonly proxy: StallProxy
  readonly sessionID: string
  readonly home: string
  /** The channel messages Claude Code would be shown, in the order the server sent them. */
  readonly channel: readonly string[]
  /** What the server wrote to its record so far (`../src/channel-record`). */
  record(): readonly ChannelRecordLine[]
  /** The server's NATS connection as its record gives it: the settings it dialled with and each
   *  status event since; undefined before it dialled. */
  natsDial(): NatsDial | undefined
  /** Resolves once `condition` holds; throws when the server exits first, or at the row's
   *  deadline, naming `what` and what the server wrote to stderr. */
  waitFor(condition: () => boolean | Promise<boolean>, what: string): Promise<void>
  /** Stops the server (SIGTERM, and SIGKILL when it has not exited by the row's deadline), closes
   *  the proxy, removes its home. */
  stop(): Promise<void>
}

/**
 * Runs `body` against a channel server started for it, then stops the server. A failure of the
 * body is the row's own, named by the wait or the assertion that failed: a body that failed at
 * the row's deadline leaves the stop's own wait no time, and that wait's give-up is reported only
 * when the body passed.
 */
async function withChannelServer(
  stack: EnvoyStack,
  sessionPrefix: string,
  body: (server: ChannelServer) => Promise<void>,
): Promise<void> {
  const server = await startChannelServer(stack, sessionPrefix)
  let failure: { readonly error: unknown } | undefined
  try {
    await body(server)
  } catch (error) {
    failure = { error }
  }
  try {
    await server.stop()
  } catch (error) {
    if (failure === undefined) throw error
  }
  if (failure !== undefined) throw failure.error
}

/**
 * Starts the channel server from this checkout as Claude Code would, for a new session whose
 * Envoy is `stack`'s listener and whose NATS goes through a stall proxy to `stack`'s natsd, and
 * sends it the MCP initialize handshake.
 */
async function startChannelServer(
  stack: EnvoyStack,
  sessionPrefix: string,
): Promise<ChannelServer> {
  const proxy = await startStallProxy(stack.natsHostPort)
  const home = await mkdtemp(join(tmpdir(), "claude-envoy-stall-"))
  const configDirectory = join(home, "claude-config")
  await mkdir(configDirectory)
  await writeFile(
    join(configDirectory, "envoy.json"),
    JSON.stringify({ envoy: { enabled: true, url: stack.envoyUrl, natsUrls: [proxy.url] } }),
  )
  const sessionID = `${sessionPrefix}_${crypto.randomUUID().replaceAll("-", "")}`
  const recordFile = join(home, "channel-record.jsonl")
  const server = Bun.spawn(["bun", channelEntry], {
    cwd: home,
    env: {
      ...bunCacheOff,
      PATH: process.env["PATH"] ?? "",
      HOME: home,
      CLAUDE_CONFIG_DIR: configDirectory,
      CLAUDE_CODE_SESSION_ID: sessionID,
      CLAUDE_PLUGIN_DATA: home,
      // The base the server and the mod below both resolve the link's socket from.
      XDG_RUNTIME_DIR: home,
      // No heartbeat registration runs in the test's window: the reconnect's registration, on a
      // new epoch, is the only one that restarts the relay.
      ENVOY_HEARTBEAT_MS: "240000",
      // The server's own record of its dial and its bounded waits (`../src/channel-record`).
      [CHANNEL_RECORD_FILE]: recordFile,
    },
    stdin: "pipe",
    stdout: "pipe",
    stderr: "pipe",
  })
  const socketPath = modSocketPath(sessionID, { XDG_RUNTIME_DIR: home })
  if (socketPath === undefined) throw new Error("the session names no mod socket")
  // Every delivery reaches the model as the mod's own write - the row of a turn running, or the
  // prompt of a new turn while the session is idle, which this one is throughout (`show` in
  // `../src/mod-session`) - so the mod is this test's view of what the session was shown.
  const channel: string[] = []
  const mod = await startFakeMod(socketPath, {
    sessionId: sessionID,
    answers: {
      "session.message": (args) => {
        channel.push(shownText(args))
        // The prompt an idle session took, which is what answers the frame: a prompt carries
        // no row of its own (`../hooks/mod-protocol.ts`, `ModCallResults["session.message"]`).
        return { as: "prompt" }
      },
    },
  })
  let stderr = ""
  void (async () => {
    for await (const chunk of server.stderr.pipeThrough(new TextDecoderStream())) stderr += chunk
  })()
  server.stdin.write(
    `${JSON.stringify({
      jsonrpc: "2.0",
      id: 1,
      method: "initialize",
      params: {
        protocolVersion: "2025-06-18",
        capabilities: {},
        clientInfo: { name: "test", version: "0" },
      },
    })}\n`,
  )
  server.stdin.write(`${JSON.stringify({ jsonrpc: "2.0", method: "notifications/initialized" })}\n`)
  const record = (): ChannelRecordLine[] =>
    existsSync(recordFile)
      ? readFileSync(recordFile, "utf8")
          .split("\n")
          .filter((line) => line !== "")
          .map((line) => JSON.parse(line) as ChannelRecordLine)
      : []
  return {
    server,
    proxy,
    sessionID,
    home,
    channel,
    record,
    natsDial() {
      const lines = record()
      const dial = lines.find((line) => "dial" in line)
      if (dial === undefined || !("dial" in dial)) return undefined
      return {
        options: dial.dial,
        statuses: lines.flatMap((line) =>
          "status" in line ? [{ type: line.status, data: line.data }] : [],
        ),
      }
    },
    // The server runs on nats.js's real ping and dial timers and a real listener, so each wait
    // asks again through the shared wait until its condition holds; a server that exits first
    // ends the wait at once, and so does the row's deadline.
    async waitFor(condition, what) {
      try {
        await until(what, async () => {
          if (server.exitCode !== null) {
            throw new Error(`the channel server exited ${server.exitCode} waiting for ${what}`)
          }
          return await condition()
        })
      } catch (error) {
        throw new Error(
          `${error instanceof Error ? error.message : String(error)}; its stderr: ${stderr}`,
          { cause: error },
        )
      }
    },
    async stop() {
      await mod.stop()
      server.kill("SIGTERM")
      try {
        await settled("the channel server's exit on SIGTERM", server.exited)
      } finally {
        // A server still running when the row's deadline passed goes too, rather than outliving
        // the run (a no-op for one that exited).
        server.kill("SIGKILL")
        await server.exited
        await proxy.close()
        await rm(home, { recursive: true, force: true })
      }
    },
  }
}

// One rollout state is left: every listener gives the session a consumer and relays it. A
// listener that can give none refuses the registration, which `envoy-client`'s own stack tests
// cover; there is no core path to fall back to here.
// Reserved out here, at the file's module scope: the file's own `afterAll` removes it once the
// `describe` has stopped the stack writing into it.
const stackDirectory = useEnvoyStackDirectory()

describe.skipIf(!envoyStackEnabled)(
  "the channel server against a real natsd and Envoy listener",
  () => {
    let stack: EnvoyStack | undefined
    beforeAll(async () => {
      stack = await startEnvoyStack({ work: stackDirectory })
    })
    afterAll(async () => {
      await stack?.stop()
    })

    test("a session whose NATS path freezes subscribes its relay subject again on the new connection within 60 s, registers a new epoch, and shows each message once", async () => {
      if (stack === undefined) throw new Error("the Envoy stack did not start")
      const running = stack
      const { envoyUrl } = running
      await withChannelServer(running, "ses_stall", async (channelServer) => {
        const { proxy, sessionID, channel, waitFor, natsDial } = channelServer
        await waitFor(
          async () => (await fetch(`${envoyUrl}/v1/registry/${sessionID}`)).ok,
          "the session's registration",
        )
        // The bundle runs from this checkout, outside Claude Code's plugin cache.
        const registration = {
          session_id: sessionID,
          plugin: "@sjawhar/claude-envoy",
          plugin_version: "source",
        }
        expect(await (await fetch(`${envoyUrl}/v1/registry/${sessionID}`)).json()).toMatchObject(
          registration,
        )
        expect(await (await fetch(`${envoyUrl}/v1/sessions`)).json()).toContainEqual(
          expect.objectContaining(registration),
        )
        await runFreezeScenario({
          label: "claude-envoy stall",
          stack: running,
          sessionID,
          proxy,
          shown: (marker) => channel.filter((content) => content.includes(marker)),
          natsDial,
          waitFor,
        })
      })
    })

    test("a session whose redial once hung still exits on SIGTERM", async () => {
      if (stack === undefined) throw new Error("the Envoy stack did not start")
      const { envoyUrl } = stack
      await withChannelServer(stack, "ses_stall_exit", async (channelServer) => {
        const { server, proxy, sessionID, waitFor, record, natsDial } = channelServer
        await waitFor(
          async () =>
            (await fetch(`${envoyUrl}/v1/registry/${sessionID}`)).ok &&
            proxy.connections[0]?.subscribedAt !== undefined,
          "the session's registration and first subscription",
        )
        // The first redial after the freeze hangs (accepted, never answered); the next forwards.
        proxy.stallNext(1)
        const frozenAt = proxy.freeze()
        await waitFor(
          () =>
            proxy.connections.some(
              (opened) => opened.acceptedAt >= frozenAt && opened.subscribedAt !== undefined,
            ),
          "the session to subscribe again after the hung redial",
        )
        // Past the hung redial within the minute, by the server's own record of its dial.
        await waitFor(
          () => natsDial()?.statuses.some((status) => status.type === "reconnect") ?? false,
          "the server's NATS client to report its reconnect",
        )
        const dial = natsDial()
        if (dial === undefined) throw new Error("the server recorded no NATS dial")
        expect(recoveryBoundMs(dial.options, 1)).toBeLessThan(RECOVERY_BOUND_MS)
        expectRecoveredByPingOut(dial, 1)
        // The hung redial must not hold the process: it exits on SIGTERM, which the shared wait
        // reads through its exit, failing at the row's deadline while the server still runs. The
        // server promises no exit time of its own, only bounded steps on the way out, and its
        // record names each with the delay it armed (R4: the bound the server chose, read across
        // the process boundary, not a clock): the NATS drain's second and the settle's
        // `SHUTDOWN_SETTLE_MS`. Together they stay under the 15 s this row held its exit to; that
        // it ran the shutdown to its end is its exit, 0 and not the signal's, and the
        // deregistration the shutdown sends reaching the listener.
        server.kill("SIGTERM")
        const exitCode = await settled("the channel server's exit on SIGTERM", server.exited)
        expect({ exitCode, signal: server.signalCode }).toEqual({ exitCode: 0, signal: null })
        expect((await fetch(`${envoyUrl}/v1/registry/${sessionID}`)).ok).toBe(false)
        const waits = record().flatMap((line) => ("wait" in line ? [line] : []))
        expect(waits.map((wait) => wait.wait)).toEqual(["drain", "settle"])
        expect(waits.reduce((armed, wait) => armed + wait.armedMs, 0)).toBeLessThan(EXIT_BOUND_MS)
      })
    })
  },
)
