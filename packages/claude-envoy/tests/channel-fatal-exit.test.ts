// The channel server's process ends with its session: a start that fails (here a hub that refuses
// its NATS credential, which the session's own broker token makes a start can meet) closes the mod
// link's socket and exits non-zero, and a server whose parent is gone (its stdin at end of file)
// exits too, rather than living on with its socket bound for a session nobody runs.
import { afterEach, expect, test } from "bun:test"
import { existsSync } from "node:fs"
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises"
import { join, resolve } from "node:path"
import { bunCacheOff } from "@legion/test-fixtures/bun-child-environment"
import { settled, span, until } from "@legion/test-fixtures/wait"
import type { Subprocess } from "bun"
import { resolveModSocketPath } from "../hooks/mod-protocol"

/** Where each session's home is made. Its mod socket is `<home>/claude-envoy/ce-<hash>.sock`, and
 *  that whole path has 100 bytes to fit in (`MOD_SOCKET_PATH_MAX_BYTES`): under `tmpdir()` a
 *  46-byte `TMPDIR` (a dev box's `/mnt/scratch/...`; a Legion pod's is longer) leaves none, so the
 *  home goes under `/tmp`, whatever `TMPDIR` is (the class of dispatch://INFRA-4085). */
const SOCKET_ROOT = "/tmp"

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

/** A NATS server that admits a client with no credential, answers the first PING (which a client's
 * connect waits for) and then goes silent: every later PING, a flush's, is left unanswered, as on
 * a path to the hub that dropped after admitting the session. `pings()` counts the PINGs seen. */
function silentHub(): { url: string; pings(): number } {
  let pings = 0
  const server = Bun.listen({
    hostname: "127.0.0.1",
    port: 0,
    socket: {
      open(socket) {
        const info = {
          server_id: "silent-hub",
          server_name: "silent-hub",
          version: "2.10.22",
          proto: 1,
          host: "127.0.0.1",
          port: 0,
          headers: true,
          max_payload: 1_048_576,
        }
        socket.write(`INFO ${JSON.stringify(info)}\r\n`)
      },
      data(socket, data) {
        for (const line of data.toString().split("\r\n")) {
          if (line !== "PING") continue
          pings += 1
          if (pings === 1) socket.write("PONG\r\n")
        }
      },
    },
  })
  cleanups.push(() => server.stop(true))
  return { url: `nats://127.0.0.1:${server.port}`, pings: () => pings }
}

/** A NATS server that takes the connection and never sends its INFO, so a client's dial waits on
 *  it until nats.js's own dial timeout. `connections()` counts the clients that reached it. */
function muteHub(): { url: string; connections(): number } {
  let connections = 0
  const server = Bun.listen({
    hostname: "127.0.0.1",
    port: 0,
    socket: {
      open() {
        connections += 1
      },
      data() {},
    },
  })
  cleanups.push(() => server.stop(true))
  return { url: `nats://127.0.0.1:${server.port}`, connections: () => connections }
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
  const home = await mkdtemp(join(SOCKET_ROOT, "claude-envoy-fatal-"))
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
      ...bunCacheOff,
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
  const socket = resolveModSocketPath(sessionID, { XDG_RUNTIME_DIR: home })
  if (socket.path === undefined)
    throw new Error(`the session names no mod socket: ${socket.refusal}`)
  const socketPath = socket.path
  return { server, socketPath, stderr: () => stderr }
}

/** The server's exit code once it exits; past the row's deadline the wait fails, naming `what`
 *  and what the server's stderr said. */
async function exitOf(started: Started, what: string): Promise<number> {
  return await settled(`${what}; its stderr said: ${started.stderr()}`, started.server.exited)
}

/** Holds the row while the server is expected to run on: a child process that does not exit
 *  leaves no state to read but its exit code, its stdout is not read here, and no later action's
 *  answer is ordered after a spontaneous exit, so a span it is, after which it must still run. */
async function stillRunning(started: Started): Promise<void> {
  await span(500, "a child process that does not exit leaves nothing to read but its exit code")
  expect(started.server.exitCode, `exited: ${started.stderr()}`).toBeNull()
}

test("a start the hub refuses closes the mod socket and exits non-zero, naming the setting", async () => {
  const hub = refusingHub()
  const started = await startServer([hub])
  // Its stdin stays open: the parent is alive, and the server still ends with its session.
  const code = await exitOf(started, "the exit of a start the hub refused")
  // The failure says which setting named the hub, which credential it refused, and why; it names
  // no part of the hub's address, which the setting holds.
  expect(started.stderr()).toMatch(
    /could not connect to NATS at the server envoy\.natsUrls in \S+envoy\.json names presenting no credential: Authorization Violation/,
  )
  expect(started.stderr()).not.toContain(hub)
  expect(code).not.toBe(0)
  expect(existsSync(started.socketPath)).toBe(false)
})

test("a server whose parent is gone exits on its stdin's end of file, its socket removed", async () => {
  // Inbound off: the session runs with no NATS, so nothing but the parent ends it.
  const started = await startServer([])
  await until("the mod socket bound", () => existsSync(started.socketPath))
  await stillRunning(started)
  started.server.stdin.end()
  await exitOf(started, "the exit after stdin closed")
  expect(existsSync(started.socketPath)).toBe(false)
})

test("a stop asked for while the start waits on a silent hub exits 0, its socket removed", async () => {
  // Admitted, then the hub answers no flush: the start waits, as on a path that dropped after
  // admitting the session. A stop then is an asked-for stop, not a failure.
  const hub = silentHub()
  const started = await startServer([hub.url])
  await until(
    "the hub's second ping and the mod socket bound",
    () => hub.pings() >= 2 && existsSync(started.socketPath),
  )
  await stillRunning(started)
  started.server.kill("SIGTERM")
  const code = await exitOf(started, "the exit after SIGTERM")
  expect(code, started.stderr()).toBe(0)
  expect(started.stderr()).not.toContain("channel server failed")
  expect(existsSync(started.socketPath)).toBe(false)
})

test("a stop asked for while the dial waits on a mute hub exits 0, its socket removed", async () => {
  const hub = muteHub()
  const started = await startServer([hub.url])
  await until(
    "the hub's first connection and the mod socket bound",
    () => hub.connections() >= 1 && existsSync(started.socketPath),
  )
  started.server.kill("SIGTERM")
  // The stop, not nats.js's 20 s dial timeout, ends the wait: a way out that waited for the dial
  // would meet the server's own exit bound (`SHUTDOWN_EXIT_MS`, 3 s) and exit 1 on it.
  const code = await exitOf(started, "the exit after SIGTERM")
  expect(code, started.stderr()).toBe(0)
  expect(started.stderr()).not.toContain("channel server failed")
  expect(existsSync(started.socketPath)).toBe(false)
})
