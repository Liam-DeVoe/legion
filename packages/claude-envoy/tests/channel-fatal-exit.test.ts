// The channel server's process ends with its session: a start that fails (here a hub that refuses
// its NATS credential, which the session's own broker token makes a start can meet) closes the mod
// link's socket and exits non-zero, and a server whose parent is gone (its stdin at end of file)
// exits too, rather than living on with its socket bound for a session nobody runs.
import { afterEach, expect, test } from "bun:test"
import { existsSync } from "node:fs"
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join, resolve } from "node:path"
import type { Subprocess } from "bun"
import { modSocketPath } from "../hooks/mod-protocol"

const channelEntry = resolve(import.meta.dir, "..", "bin", "envoy-channel.ts")

const cleanups: (() => Promise<void> | void)[] = []
afterEach(async () => {
  for (const cleanup of cleanups.splice(0).reverse()) await cleanup()
})

/** A NATS server that refuses every client: the INFO asking for a credential, then nats-server's
 * own refusal of the CONNECT, which is what a hub's auth service answers a token it does not admit. */
function refusingHub(): string {
  const server = Bun.listen({
    hostname: "127.0.0.1",
    port: 0,
    socket: {
      open(socket) {
        const info = {
          server_id: "refusing-hub",
          server_name: "refusing-hub",
          version: "2.10.22",
          proto: 1,
          host: "127.0.0.1",
          port: 0,
          headers: true,
          auth_required: true,
          max_payload: 1_048_576,
          nonce: "refusing-hub-nonce",
        }
        socket.write(`INFO ${JSON.stringify(info)}\r\n`)
      },
      data(socket, data) {
        if (data.toString().includes("CONNECT")) {
          socket.write("-ERR 'Authorization Violation'\r\n")
          socket.end()
        }
      },
    },
  })
  cleanups.push(() => server.stop(true))
  return `nats://127.0.0.1:${server.port}`
}

interface Started {
  readonly server: Subprocess<"pipe", "ignore", "pipe">
  readonly socketPath: string
  stderr(): string
}

/** Starts the channel server from this checkout as Claude Code would, with `natsUrls` its Envoy's
 *  NATS servers (none: inbound off), its stdin held open as a live parent holds it, and sends the
 *  MCP initialize handshake. */
async function startServer(natsUrls: readonly string[]): Promise<Started> {
  const home = await mkdtemp(join(tmpdir(), "claude-envoy-fatal-"))
  cleanups.push(() => rm(home, { recursive: true, force: true }))
  const configDirectory = join(home, "claude-config")
  await mkdir(configDirectory)
  await writeFile(
    join(configDirectory, "envoy.json"),
    JSON.stringify({
      envoy: {
        enabled: true,
        url: "http://127.0.0.1:9",
        ...(natsUrls.length > 0 ? { natsUrls } : {}),
      },
      dispatch: { enabled: false },
    }),
  )
  const sessionID = `ses_fatal_${crypto.randomUUID().replaceAll("-", "")}`
  const server = Bun.spawn(["bun", channelEntry], {
    cwd: home,
    env: {
      PATH: process.env["PATH"] ?? "",
      HOME: home,
      CLAUDE_CONFIG_DIR: configDirectory,
      CLAUDE_CODE_SESSION_ID: sessionID,
      CLAUDE_PLUGIN_DATA: home,
      XDG_RUNTIME_DIR: home,
      ENVOY_HEARTBEAT_MS: "240000",
    },
    stdin: "pipe",
    stdout: "ignore",
    stderr: "pipe",
  })
  cleanups.push(async () => {
    server.kill("SIGKILL")
    await server.exited
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
  server.stdin.flush()
  const socketPath = modSocketPath(sessionID, { XDG_RUNTIME_DIR: home })
  if (socketPath === undefined) throw new Error("the session names no mod socket")
  return { server, socketPath, stderr: () => stderr }
}

/** The server's exit code once it exits, or undefined when it is still running after `ms`. */
async function exitWithin(server: Subprocess, ms: number): Promise<number | undefined> {
  const late = Bun.sleep(ms).then(() => undefined)
  return Promise.race([server.exited, late])
}

test("a start the hub refuses closes the mod socket and exits non-zero, naming the hub", async () => {
  const hub = refusingHub()
  const started = await startServer([hub])
  // Its stdin stays open: the parent is alive, and the server still ends with its session.
  const code = await exitWithin(started.server, 8_000)
  // The failure says which hub refused which credential, and why.
  expect(started.stderr()).toContain(
    `could not connect to NATS at ${hub} presenting no credential: 'Authorization Violation'`,
  )
  expect(code, `still running: ${started.stderr()}`).toBeDefined()
  expect(code).not.toBe(0)
  expect(existsSync(started.socketPath)).toBe(false)
}, 20_000)

test("a server whose parent is gone exits on its stdin's end of file, its socket removed", async () => {
  // Inbound off: the session runs with no NATS, so nothing but the parent ends it.
  const started = await startServer([])
  const deadline = Date.now() + 8_000
  while (!existsSync(started.socketPath) && Date.now() < deadline) await Bun.sleep(50)
  expect(existsSync(started.socketPath), started.stderr()).toBe(true)
  expect(await exitWithin(started.server, 500)).toBeUndefined()
  started.server.stdin.end()
  const code = await exitWithin(started.server, 8_000)
  expect(code, `still running after stdin closed: ${started.stderr()}`).toBeDefined()
  expect(existsSync(started.socketPath)).toBe(false)
}, 20_000)
