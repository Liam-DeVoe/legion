import { afterAll, beforeAll, describe, expect, test } from "bun:test"
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises"
import { join, resolve } from "node:path"
import {
  type EnvoyStack,
  envoyStackEnabled,
  startEnvoyStack,
  useEnvoyStackDirectory,
} from "@legion/envoy-client/test/envoy-stack"
import { type StallProxy, startStallProxy } from "@legion/envoy-client/test/stall-proxy"
import { RECOVERY_BOUND_MS, runFreezeScenario } from "@legion/envoy-client/test/stall-scenario"
import type { Subprocess } from "bun"
import { type ModCallArguments, modSocketPath } from "../hooks/mod-protocol"
import { startFakeMod } from "./fake-mod"

const channelEntry = resolve(import.meta.dir, "..", "bin", "envoy-channel.ts")

/** Where the socket homes are made. A mod socket is `<home>/claude-envoy/ce-<hash>.sock`, and that
 *  whole path has 100 bytes to fit in (`MOD_SOCKET_PATH_MAX_BYTES`): under `tmpdir()` a deep
 *  `TMPDIR` (a dev box's `/mnt/scratch/...`, a Legion pod's) leaves none, so the homes go under
 *  `/tmp`, whatever `TMPDIR` is, as channel-fatal-exit.test.ts's do. */
const SOCKET_ROOT = "/tmp"

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
  /** Resolves once `condition` holds; throws when the server exits or `deadline`, where one is
   *  given, passes first. */
  waitFor(
    condition: () => boolean | Promise<boolean>,
    deadline: number | undefined,
    what: string,
  ): Promise<void>
  /** Stops the server (SIGTERM, then SIGKILL after 10 s), closes the proxy, removes its home. */
  stop(): Promise<void>
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
  const home = await mkdtemp(join(SOCKET_ROOT, "claude-envoy-stall-"))
  const configDirectory = join(home, "claude-config")
  await mkdir(configDirectory)
  await writeFile(
    join(configDirectory, "envoy.json"),
    JSON.stringify({ envoy: { enabled: true, url: stack.envoyUrl, natsUrls: [proxy.url] } }),
  )
  const sessionID = `${sessionPrefix}_${crypto.randomUUID().replaceAll("-", "")}`
  const server = Bun.spawn(["bun", channelEntry], {
    cwd: home,
    env: {
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
    },
    stdin: "pipe",
    stdout: "pipe",
    stderr: "pipe",
  })
  const socketPath = modSocketPath(sessionID, { XDG_RUNTIME_DIR: home })
  if (socketPath === undefined) throw new Error("the session names no mod socket")
  // Read before the probe below, so a server that writes while it starts never blocks on a full
  // pipe.
  let stderr = ""
  void (async () => {
    for await (const chunk of server.stderr.pipeThrough(new TextDecoderStream())) stderr += chunk
  })()
  // Every delivery reaches the model as the mod's own write - the row of a turn running, or the
  // prompt of a new turn while the session is idle, which this one is throughout (`show` in
  // `../src/mod-session`) - so the mod is this test's view of what the session was shown.
  const channel: string[] = []
  const mod = await startFakeMod(socketPath, {
    sessionId: sessionID,
    exited: server.exited,
    answers: {
      "session.message": (args) => {
        channel.push(shownText(args))
        // The prompt an idle session took, which is what answers the frame: a prompt carries
        // no row of its own (`../hooks/mod-protocol.ts`, `ModCallResults["session.message"]`).
        return { as: "prompt" }
      },
    },
  })
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
  return {
    server,
    proxy,
    sessionID,
    home,
    channel,
    // The server runs on nats.js's real ping and dial timers and a real listener, so each wait
    // polls the real clock until its condition holds. Only a wait whose time the row asserts has a
    // deadline (`RECOVERY_BOUND_MS`). A wait given none ends at the row's limit, which names the
    // row, or when the server exits.
    async waitFor(condition, deadline, what) {
      while (!(await condition())) {
        if (server.exitCode !== null) {
          throw new Error(
            `the channel server exited ${server.exitCode} waiting for ${what}: ${stderr}`,
          )
        }
        if (deadline !== undefined && Date.now() > deadline) {
          throw new Error(`timed out waiting for ${what}: ${stderr}`)
        }
        await Bun.sleep(100)
      }
    },
    async stop() {
      await mod.stop()
      server.kill("SIGTERM")
      const killer = setTimeout(() => server.kill("SIGKILL"), 10_000)
      await server.exited
      clearTimeout(killer)
      await proxy.close()
      await rm(home, { recursive: true, force: true })
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
      // A hang guard: what can hang is the stack's start, which builds natsd and the listener with
      // `go build` and waits for each to serve. It is the start pi-envoy's live describe makes
      // (`startEnvoyStack`). Worst measured cost of that start: 249 197.32 ms over 20 runs at the
      // 96 rung in the Job infra-1580-krows-kfh-tgvmp of the cluster's `builds` namespace, two
      // CPUs and 8 GiB (218 busy loops, at which row A's runs took 33.5 s against the
      // calibration's 31.2 s), 2026-11-22T08:32Z to 09:41Z. Six times the worst is 1 495 s. That
      // is past the 700 s one hang leaves this step of the `envoy-session-stall` job
      // (`.github/workflows/ci-job-envoy-session-stall.yaml`): its `timeout-minutes: 20` less its
      // other steps' 500 s at d7aa4f9b, run 44984499551, where this step took 72 s of the 572 s.
      // So that ceiling decides 600_000, which is 2.4 times the worst.
    }, 600_000)
    afterAll(async () => {
      await stack?.stop()
      // A hang guard: what can hang is the stop of natsd or the listener. Worst measured cost:
      // 4 703.96 ms over the same 20 runs, so 60_000 is 12.8 times the worst.
    }, 60_000)

    test("a session whose NATS path freezes subscribes its relay subject again on the new connection within 60 s, registers a new epoch, and shows each message once", async () => {
      if (stack === undefined) throw new Error("the Envoy stack did not start")
      const { envoyUrl } = stack
      const { proxy, sessionID, channel, waitFor, stop } = await startChannelServer(
        stack,
        "ses_stall",
      )
      try {
        await waitFor(
          async () => (await fetch(`${envoyUrl}/v1/registry/${sessionID}`)).ok,
          undefined,
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
          stack,
          sessionID,
          proxy,
          shown: (marker) => channel.filter((content) => content.includes(marker)),
          waitFor,
        })
      } finally {
        await stop()
      }
      // A hang guard, not a bound on work: what can hang is a wait for the registration or a
      // message that never comes.
      // Worst measured cost: 120 895.29 ms over 20 runs at the 96 rung in the Job
      // infra-1580-krows-kc-4bm4r of the cluster's `builds` namespace, two CPUs and 8 GiB (199
      // busy loops, at which row A's runs took 39.1 s against the calibration's 31.2 s),
      // 2026-11-22T18:21Z to 20:50Z; 116 398.78 ms in the Job infra-1580-krows-kc-g4zvc (120
      // busy loops, row A at 34.2 s), 2026-11-22T15:21Z to 17:53Z. Six times the worst is past
      // the 700 s this step of the `envoy-session-stall` job leaves one hang, less the stack's
      // slowest start (249 s, the `beforeAll` above) and the other row's worst (177 s), so
      // that ceiling decides 270_000, 2.2 times the worst.
    }, 270_000)

    test("a session whose redial once hung still exits on SIGTERM", async () => {
      if (stack === undefined) throw new Error("the Envoy stack did not start")
      const { envoyUrl } = stack
      const { server, proxy, sessionID, waitFor, stop } = await startChannelServer(
        stack,
        "ses_stall_exit",
      )
      try {
        await waitFor(
          async () =>
            (await fetch(`${envoyUrl}/v1/registry/${sessionID}`)).ok &&
            proxy.connections[0]?.subscribedAt !== undefined,
          undefined,
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
          frozenAt + RECOVERY_BOUND_MS,
          "the session to subscribe again after the hung redial",
        )
        const signalledAt = Date.now()
        server.kill("SIGTERM")
        // No timer bounds the exit. A server that never exits holds the row to its limit, which
        // fails the row by name. Bun's runner then sends SIGTERM to every process the file's rows
        // started that still runs. This server handles SIGTERM once (`process.once` in
        // `../src/envoy-channel-server.ts`), so that second signal ends it, and the `finally`
        // stops the rest.
        await server.exited
        const exitSeconds = ((Date.now() - signalledAt) / 1000).toFixed(1)
        console.log(`claude-envoy stall exit: SIGTERM -> exited after ${exitSeconds} s`)
        // It exited of itself, through its SIGTERM handler. A process a signal ended has that
        // signal here, and no exit code.
        expect(server.signalCode).toBeNull()
      } finally {
        await stop()
      }
      // A hang guard, not a bound on work: what can hang is a wait for the registration or an
      // exit that never comes.
      // Worst measured cost: 177 296.68 ms over 20 runs at the 96 rung in the Job
      // infra-1580-krows-kc-4bm4r of the cluster's `builds` namespace, two CPUs and 8 GiB (199
      // busy loops, at which row A's runs took 39.1 s against the calibration's 31.2 s),
      // 2026-11-22T18:21Z to 20:50Z; 118 302.75 ms in the Job infra-1580-krows-kc-g4zvc (120
      // busy loops, row A at 34.2 s), 2026-11-22T15:21Z to 17:53Z. Six times the worst is past
      // the 700 s this step of the `envoy-session-stall` job leaves one hang, less the stack's
      // slowest start (249 s, the `beforeAll` above) and the other row's worst (121 s), so
      // that ceiling decides 330_000, 1.9 times the worst.
    }, 330_000)
  },
)
