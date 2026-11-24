import { afterEach, beforeEach, expect, setDefaultTimeout, test } from "bun:test"
import { mkdir, mkdtemp, rm, stat, writeFile } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join, resolve } from "node:path"
import { consumerDelivery } from "@legion/envoy-client/test/subscribe-answer"
import { envoyToolSpecs } from "@legion/envoy-client/tool-contract"
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret"
import { Client } from "@modelcontextprotocol/sdk/client/index.js"
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js"
import { modSocketPath } from "../hooks/mod-protocol"
import { waitFor } from "./channel-session-harness"
import { startFakeMod } from "./fake-mod"
import { FakeNatsServer } from "./fake-nats-server"
import { connectedToServer } from "./mcp-connect"
import { natsGate } from "./nats-gate"
import { ROW_TIMEOUT_MS } from "./reopen-server"
import { beforeTheGuard, JOB_ROOM_GUARD_MS, rowStarted, untilTheGuard } from "./row-deadline"
import { NO_TRANSPILER_CACHE } from "./transpiler-cache"

const entry = resolve(import.meta.dir, "..", "bin", "envoy-channel.ts")

// The rows' hang guard is the claude-envoy job's room (`JOB_ROOM_GUARD_MS`, 240 s,
// `./row-deadline.ts`), not six times a reading: this file's rows read up to 67 209 ms over 20 runs
// at the 96 rung (the first row below, in the Job infra-1580-r27mc-74dnt of the cluster's `builds`
// namespace, two CPUs and 8 GiB, 205 busy loops for the 96 rung, row A 31.8 s, at 82306880), where
// `row-deadline.ts`'s 120 s was 1.8 times it, and the two broker-token rows' 60_000 and 90_000
// were 1.3 and 1.4 times their 46 399 and 62 505 ms. With the waits below ending on their facts
// and the guard in place, the worst over 20 runs at that rung is 82 600 ms, the broker-wait row,
// whose 45 s bound of before would have failed that run (infra-1580-r27mc2-86dmr, 122 busy loops,
// row A 36.1 s, at 0faaee16). Six times it is 496 s, past that room, so the room decides 240_000,
// 2.9 times the worst. The dial row keeps its own 600 s (`ROW_TIMEOUT_MS`, `./reopen-server.ts`),
// 7.1 times its 84 395 ms there.
beforeEach(rowStarted)
setDefaultTimeout(JOB_ROOM_GUARD_MS)

/** Where each session's home is made. Its mod socket is `<home>/claude-envoy/ce-<hash>.sock`, and
 *  that whole path has 100 bytes to fit in (`MOD_SOCKET_PATH_MAX_BYTES`): under `tmpdir()` a deep
 *  `TMPDIR` (a dev box's `/mnt/scratch/...`, a Legion pod's) leaves none, and the session then names
 *  no socket. So the home goes under `/tmp`, whatever `TMPDIR` is, as channel-fatal-exit.test.ts's
 *  does. */
const SOCKET_ROOT = "/tmp"

/** A session home of its own, under {@link SOCKET_ROOT}. */
function sessionHome(prefix: string): Promise<string> {
  return mkdtemp(join(SOCKET_ROOT, prefix))
}

/**
 * Aborted by the file's `afterEach` once the row running now has ended, at its limit included,
 * and then replaced for the next row. A row hands the signal it began with to its fake mod
 * (`startFakeMod`'s `signal`): the server here is the MCP SDK's child process, which the row does
 * not hold, so the mod's probe cannot stop on that server's exit and stops on the row's end.
 */
let rowEnd = new AbortController()
afterEach(() => {
  rowEnd.abort()
  rowEnd = new AbortController()
})

// The rows below start each session in a home of its own and reach it at its mod socket; a home
// under a deep TMPDIR named no socket, and two of them failed (the review of #4140's forward merge).
test("a session home's mod socket fits however deep TMPDIR is", async () => {
  const was = process.env["TMPDIR"]
  const deep = await mkdtemp(join("/tmp", `claude-envoy-depth-${"d".repeat(64)}-`))
  await mkdir(deep, { recursive: true })
  process.env["TMPDIR"] = deep
  let home: string | undefined
  try {
    expect(tmpdir()).toBe(deep)
    home = await sessionHome("claude-envoy-mcp-")
    expect(modSocketPath("ses_depth", { XDG_RUNTIME_DIR: home })).toBeDefined()
  } finally {
    if (was === undefined) delete process.env["TMPDIR"]
    else process.env["TMPDIR"] = was
    if (home !== undefined) await rm(home, { recursive: true, force: true })
    await rm(deep, { recursive: true, force: true })
  }
})

// Each row below starts a channel server of its own, with Bun's transpiler cache off
// (`./transpiler-cache.ts`), and waits for what that server does: its answer to `initialize`
// (`connectedToServer`), each later step and each MCP request in the first two rows and in the dial
// row, until 5 s inside the row's own hang guard (`untilTheGuard`, `./row-deadline.ts`: this file's
// default, `JOB_ROOM_GUARD_MS`, 240 s, and the dial row's own 600 s), so a step that never comes
// fails naming itself before Bun's line would. The two broker-token rows wait the same way, and the
// session-home row starts no server. The first two rows had 5 s waits and a 20 s limit, and at
// loads of 720 to 1,150 the first row's server, writing that cache into its empty HOME, had not
// started within them; the dial row's `initialize` had the MCP SDK's 60 s inside its 600 s, and at
// a load of 1,200 to 1,400 it failed there at 99 s (dispatch://INFRA-3904).

// What Claude Code sees over MCP, with Dispatch configured: the Envoy tools and nothing else, since
// agents reach Dispatch through the `dispatch` command the plugin's bin/ puts on the Bash tool's
// PATH. A terminal session reaches the model only through its mod (INFRA-1026), so it advertises
// no delivery mode until the mod connects, and Aside, BTW and Send once one has.
test("over MCP the server offers only the Envoy tools, refuses dispatch_ask, points at the dispatch command, and advertises nothing until its mod connects", async () => {
  const ended = rowEnd.signal
  const broker = new FakeNatsServer()
  const dispatchRequests: string[] = []
  const dispatch = Bun.serve({
    port: 0,
    fetch: (request) => {
      dispatchRequests.push(`${request.method} ${new URL(request.url).pathname}`)
      return Response.json({ error: "unexpected" }, { status: 500 })
    },
  })
  const advertised: unknown[] = []
  const listener = Bun.serve({
    port: 0,
    fetch: async (request) => {
      const url = new URL(request.url)
      if (request.method === "POST" && url.pathname === "/v1/interests/subscribe") {
        const body = (await request.json()) as {
          session_id: string
          topics: string[]
          capabilities?: unknown
        }
        advertised.push(body.capabilities)
        return Response.json({
          session_id: body.session_id,
          machine_id: "test",
          dir: "/tmp",
          topics: body.topics,
          // Every stand-in listener answers the session's own consumer
          // (`@legion/envoy-client/test/subscribe-answer`).
          delivery: consumerDelivery(body.session_id),
        })
      }
      if (request.method === "DELETE") return new Response(null, { status: 200 })
      return Response.json({ error: "not found" }, { status: 404 })
    },
  })
  const stateDirectory = await sessionHome("claude-envoy-mcp-")
  const dispatchToken = fixtureSecret("dsp")
  const client = new Client({ name: "test", version: "0" })
  const transport = new StdioClientTransport({
    command: "bun",
    args: [entry],
    cwd: stateDirectory,
    env: {
      PATH: process.env["PATH"] ?? "",
      HOME: stateDirectory,
      ENVOY_NATS_URL: broker.url,
      ENVOY_URL: `http://127.0.0.1:${listener.port}`,
      CLAUDE_CODE_SESSION_ID: "ses_mcp",
      CLAUDE_PLUGIN_DATA: stateDirectory,
      // The socket the server binds for its mod, and the one the fake mod below connects to,
      // are both resolved from this base (`resolveModSocketPath`).
      XDG_RUNTIME_DIR: stateDirectory,
      DISPATCH_URL: `http://127.0.0.1:${dispatch.port}`,
      DISPATCH_TOKEN: dispatchToken,
      ...NO_TRANSPILER_CACHE,
    },
    stderr: "pipe",
  })
  let stderr = ""
  transport.stderr?.on("data", (chunk: Buffer | string) => {
    stderr += String(chunk)
  })

  try {
    await connectedToServer(client, transport, () => stderr, JOB_ROOM_GUARD_MS)
    // The server answers tool calls only once its channel session has started.
    await waitFor(
      () =>
        client
          .callTool({ name: "envoy_whoami", arguments: {} }, undefined, {
            timeout: untilTheGuard(JOB_ROOM_GUARD_MS),
          })
          .then(
            () => true,
            (error: unknown) => {
              if (String(error).includes("still starting")) return false
              throw error
            },
          ),
      "the channel session to start",
      () => stderr,
      untilTheGuard(JOB_ROOM_GUARD_MS),
    )

    const { tools } = await client.listTools(undefined, {
      timeout: untilTheGuard(JOB_ROOM_GUARD_MS),
    })
    expect(tools.map(({ name }) => name)).toEqual(envoyToolSpecs.map(({ name }) => name))
    // Nothing until the mod connects: without one no message reaches the model at all.
    expect(advertised.length).toBeGreaterThan(0)
    for (const modes of advertised) expect(modes).toEqual([])

    const socketPath = modSocketPath("ses_mcp", { XDG_RUNTIME_DIR: stateDirectory })
    if (socketPath === undefined) throw new Error("the session names no mod socket")
    const mod = await startFakeMod(socketPath, { sessionId: "ses_mcp", signal: ended })
    try {
      // The mod's hello makes the session register again, with what it can take now.
      await waitFor(
        async () => (advertised.at(-1) as string[] | undefined)?.length === 3,
        "the session to advertise its delivery modes",
        () => JSON.stringify(advertised),
        untilTheGuard(JOB_ROOM_GUARD_MS),
      )
      expect(advertised.at(-1)).toEqual(["aside", "btw", "steer"])
    } finally {
      await mod.stop()
    }
    const instructions = client.getInstructions() ?? ""
    expect(instructions).toContain("`dispatch` command")
    expect(instructions).not.toContain("Dispatch tools")

    await expect(
      client.callTool(
        { name: "dispatch_ask", arguments: { issue: "DSP-3", question: "Ship?" } },
        undefined,
        { timeout: untilTheGuard(JOB_ROOM_GUARD_MS) },
      ),
    ).rejects.toThrow("Unsupported Envoy tool: dispatch_ask")
    expect(dispatchRequests).toEqual([])
  } finally {
    await client.close().catch(() => undefined)
    dispatch.stop(true)
    listener.stop(true)
    await broker.stop()
    await rm(stateDirectory, { recursive: true, force: true })
  }
})

// A mod says hello once per Claude Code process, so a server Claude Code starts again inside
// that process hears none, and neither does a listener added after the hello landed: the session
// then advertised nothing until the next heartbeat, up to 90 s later, and Dispatch showed it as
// taking no messages (INFRA-1026 review). A poll says a mod is there just as well.
test("the session advertises its delivery modes for a mod that polls and never says hello", async () => {
  const ended = rowEnd.signal
  const broker = new FakeNatsServer()
  const advertised: unknown[] = []
  const listener = Bun.serve({
    port: 0,
    fetch: async (request) => {
      const url = new URL(request.url)
      if (request.method === "POST" && url.pathname === "/v1/interests/subscribe") {
        const body = (await request.json()) as {
          session_id: string
          topics: string[]
          capabilities?: unknown
        }
        advertised.push(body.capabilities)
        return Response.json({
          session_id: body.session_id,
          machine_id: "test",
          dir: "/tmp",
          topics: body.topics,
          delivery: consumerDelivery(body.session_id),
        })
      }
      if (request.method === "DELETE") return new Response(null, { status: 200 })
      return Response.json({ error: "not found" }, { status: 404 })
    },
  })
  const stateDirectory = await sessionHome("claude-envoy-quiet-")
  const client = new Client({ name: "test", version: "0" })
  const transport = new StdioClientTransport({
    command: "bun",
    args: [entry],
    cwd: stateDirectory,
    env: {
      PATH: process.env["PATH"] ?? "",
      HOME: stateDirectory,
      ENVOY_NATS_URL: broker.url,
      ENVOY_URL: `http://127.0.0.1:${listener.port}`,
      CLAUDE_CODE_SESSION_ID: "ses_quiet",
      CLAUDE_PLUGIN_DATA: stateDirectory,
      XDG_RUNTIME_DIR: stateDirectory,
      ...NO_TRANSPILER_CACHE,
    },
    stderr: "pipe",
  })
  let stderr = ""
  transport.stderr?.on("data", (chunk: Buffer | string) => {
    stderr += String(chunk)
  })

  try {
    await connectedToServer(client, transport, () => stderr, JOB_ROOM_GUARD_MS)
    await waitFor(
      () =>
        client
          .callTool({ name: "envoy_whoami", arguments: {} }, undefined, {
            timeout: untilTheGuard(JOB_ROOM_GUARD_MS),
          })
          .then(
            () => true,
            (error: unknown) => {
              if (String(error).includes("still starting")) return false
              throw error
            },
          ),
      "the channel session to start",
      () => stderr,
      untilTheGuard(JOB_ROOM_GUARD_MS),
    )
    expect(advertised.at(-1)).toEqual([])

    const socketPath = modSocketPath("ses_quiet", { XDG_RUNTIME_DIR: stateDirectory })
    if (socketPath === undefined) throw new Error("the session names no mod socket")
    await waitFor(
      () =>
        stat(socketPath).then(
          () => true,
          () => false,
        ),
      "the server to bind its mod socket",
      () => stderr,
      untilTheGuard(JOB_ROOM_GUARD_MS),
    )
    const mod = await startFakeMod(socketPath, {
      sessionId: "ses_quiet",
      hello: false,
      signal: ended,
    })
    try {
      await waitFor(
        async () => (advertised.at(-1) as string[] | undefined)?.length === 3,
        "the session to advertise its delivery modes",
        () => JSON.stringify(advertised),
        untilTheGuard(JOB_ROOM_GUARD_MS),
      )
      expect(advertised.at(-1)).toEqual(["aside", "btw", "steer"])
    } finally {
      await mod.stop()
    }
  } finally {
    await client.close().catch(() => undefined)
    listener.stop(true)
    await broker.stop()
    await rm(stateDirectory, { recursive: true, force: true })
  }
})

// Under NATS_AUTH=broker the start waits for the session's token for as long as the server lives,
// since Claude Code does not start a channel server again: a tool call meanwhile says what the start
// waits on, where it said only that the server was starting (the review of 2fa6fbf7cf, m1).
test(
  "a tool call while the start waits for its broker token says what the start waits on",
  async () => {
    const broker = Bun.serve({
      port: 0,
      hostname: "127.0.0.1",
      fetch: async (request) => {
        await request.text()
        return new Response("the broker is away", { status: 503 })
      },
    })
    const stateDirectory = await sessionHome("claude-envoy-broker-wait-")
    const tokenFile = join(stateDirectory, "projected-token")
    await writeFile(tokenFile, `${fixtureSecret("proj")}\n`)
    const client = new Client({ name: "test", version: "0" })
    const transport = new StdioClientTransport({
      command: "bun",
      args: [entry],
      cwd: stateDirectory,
      env: {
        PATH: process.env["PATH"] ?? "",
        HOME: stateDirectory,
        ENVOY_NATS_URL: "nats://127.0.0.1:1",
        ENVOY_URL: "http://127.0.0.1:1",
        CLAUDE_CODE_SESSION_ID: "ses_broker_wait",
        CLAUDE_PLUGIN_DATA: stateDirectory,
        XDG_RUNTIME_DIR: stateDirectory,
        NATS_AUTH: "broker",
        AGENT_SECRETS_URL: `http://127.0.0.1:${broker.port}`,
        AGENT_SECRETS_MACHINE_IDENTITY: "kubernetes",
        AGENT_SECRETS_MACHINE_TOKEN_FILE: tokenFile,
        ...NO_TRANSPILER_CACHE,
      },
      stderr: "pipe",
    })
    // The start has reached its wait for the token once its stderr says what the first trade came
    // to: the token client writes that line at the wait after the trade, and the start names its
    // wait before it trades. A start that never gets there is named with what its stderr said, 5 s
    // inside the row's guard (`beforeTheGuard`); a 45 s bound decided it before, against a row that
    // cost 46 399 ms on a loaded machine.
    let stderr = ""
    const waiting = Promise.withResolvers<void>()
    transport.stderr?.on("data", (chunk: Buffer | string) => {
      stderr += String(chunk)
      if (stderr.includes("trading again")) waiting.resolve()
    })
    try {
      await connectedToServer(client, transport, () => stderr, JOB_ROOM_GUARD_MS)
      await beforeTheGuard(
        waiting.promise,
        "the start's wait for the token",
        () => `its stderr said: ${stderr.slice(-2_000)}`,
        undefined,
        JOB_ROOM_GUARD_MS,
      )
      const said = await client
        .callTool({ name: "envoy_whoami", arguments: {} }, undefined, {
          timeout: untilTheGuard(JOB_ROOM_GUARD_MS),
        })
        .then(
          () => {
            throw new Error("the session started though its broker answers every trade 503")
          },
          (error: unknown) => String(error),
        )
      expect(said).toContain(
        "Envoy channel server is still starting: it waits for this session's broker token, and its stderr says what each trade came to",
      )
    } finally {
      await client.close().catch(() => undefined)
      broker.stop(true)
      await rm(stateDirectory, { recursive: true, force: true })
    }
  },
  JOB_ROOM_GUARD_MS,
)

// The channel server dials NATS again for its life, and an Envoy tool called meanwhile is told
// why the start is still waiting, which until then only reached stderr. Once the connection is up
// that reason is gone: what the start waits on next (here, the listener) is not the dial.
test(
  "an Envoy tool called while the start dials NATS again is told the dial's last failure, and not once the connection is up",
  async () => {
    const broker = new FakeNatsServer()
    const gate = natsGate(broker.url)
    const held = Promise.withResolvers<void>()
    const listener = Bun.serve({
      port: 0,
      fetch: async (request) => {
        const url = new URL(request.url)
        if (request.method === "POST" && url.pathname === "/v1/interests/subscribe") {
          const body = (await request.json()) as { session_id: string; topics: string[] }
          await held.promise
          return Response.json({
            session_id: body.session_id,
            machine_id: "test",
            dir: "/tmp",
            topics: body.topics,
            delivery: consumerDelivery(body.session_id),
          })
        }
        if (request.method === "DELETE") return new Response(null, { status: 200 })
        return Response.json({ error: "not found" }, { status: 404 })
      },
    })
    const stateDirectory = await sessionHome("claude-envoy-dial-")
    const client = new Client({ name: "test", version: "0" })
    const transport = new StdioClientTransport({
      command: "bun",
      args: [entry],
      cwd: stateDirectory,
      env: {
        PATH: process.env["PATH"] ?? "",
        HOME: stateDirectory,
        ENVOY_NATS_URL: gate.url,
        ENVOY_URL: `http://127.0.0.1:${listener.port}`,
        CLAUDE_CODE_SESSION_ID: "ses_dialing",
        CLAUDE_PLUGIN_DATA: stateDirectory,
        XDG_RUNTIME_DIR: stateDirectory,
        ...NO_TRANSPILER_CACHE,
      },
      stderr: "pipe",
    })
    let stderr = ""
    transport.stderr?.on("data", (chunk: Buffer | string) => {
      stderr += String(chunk)
    })
    // Each request waits for its answer until the row's named deadline, not the MCP SDK's 60 s.
    const whoami = () =>
      client
        .callTool({ name: "envoy_whoami", arguments: {} }, undefined, {
          timeout: untilTheGuard(ROW_TIMEOUT_MS),
        })
        .then(
          () => "answered",
          (error: unknown) => String(error),
        )
    let answer = ""
    const state = () => `the tool last said: ${answer}; the server's stderr: ${stderr}`

    try {
      await connectedToServer(client, transport, () => stderr, ROW_TIMEOUT_MS)
      await waitFor(
        async () => {
          answer = await whoami()
          return answer.includes("dialing again in")
        },
        "the tool to name the dial's last failure",
        state,
        untilTheGuard(ROW_TIMEOUT_MS),
      )
      expect(answer).toContain("Envoy channel server is still starting: could not connect to NATS")

      gate.open()
      await waitFor(
        async () => broker.subscribed.length > 0,
        "the session's subscriptions to reach NATS",
        state,
        untilTheGuard(ROW_TIMEOUT_MS),
      )
      // Connected, and still starting: the listener holds the registration.
      expect(await whoami()).toContain("Envoy channel server is still starting")
      expect(await whoami()).not.toContain("could not connect")

      held.resolve()
      await waitFor(
        async () => {
          answer = await whoami()
          return answer === "answered"
        },
        "the channel session to start",
        state,
        untilTheGuard(ROW_TIMEOUT_MS),
      )
    } finally {
      held.resolve()
      await client.close().catch(() => undefined)
      listener.stop(true)
      gate.stop()
      await broker.stop()
      await rm(stateDirectory, { recursive: true, force: true })
    }
  },
  ROW_TIMEOUT_MS,
)

// The dial's warnings name what the start waits on while it dials; the broker token's keeper warns
// through the same channel once the connection is up, and a keeper warning is no reason the start
// is waiting. Here the first token lives two seconds and every later trade is refused, so the
// keeper warns about a second after the dial, while the listener holds the start's registration:
// a tool call then is told the start registers, not the keeper's warning.
test(
  "a broker token keeper's warning while the start registers leaves the tool told what the start waits on",
  async () => {
    let trades = 0
    const tokens = Bun.serve({
      port: 0,
      hostname: "127.0.0.1",
      fetch: async (request) => {
        await request.text()
        trades += 1
        if (trades > 1) return new Response("the broker is away", { status: 503 })
        return Response.json({
          access_token: fixtureSecret("channel"),
          issued_token_type: "urn:ietf:params:oauth:token-type:access_token",
          token_type: "DPoP",
          expires_in: 2,
        })
      },
    })
    const broker = new FakeNatsServer({ nonce: "fake-nonce" })
    const held = Promise.withResolvers<void>()
    const listener = Bun.serve({
      port: 0,
      fetch: async (request) => {
        const url = new URL(request.url)
        if (request.method === "POST" && url.pathname === "/v1/interests/subscribe") {
          const body = (await request.json()) as { session_id: string; topics: string[] }
          await held.promise
          return Response.json({
            session_id: body.session_id,
            machine_id: "test",
            dir: "/tmp",
            topics: body.topics,
            delivery: consumerDelivery(body.session_id),
          })
        }
        if (request.method === "DELETE") return new Response(null, { status: 200 })
        return Response.json({ error: "not found" }, { status: 404 })
      },
    })
    const stateDirectory = await sessionHome("claude-envoy-keeper-")
    const tokenFile = join(stateDirectory, "projected-token")
    await writeFile(tokenFile, `${fixtureSecret("proj")}\n`)
    const client = new Client({ name: "test", version: "0" })
    const transport = new StdioClientTransport({
      command: "bun",
      args: [entry],
      cwd: stateDirectory,
      env: {
        PATH: process.env["PATH"] ?? "",
        HOME: stateDirectory,
        ENVOY_NATS_URL: broker.url,
        ENVOY_URL: `http://127.0.0.1:${listener.port}`,
        CLAUDE_CODE_SESSION_ID: "ses_keeper_warns",
        CLAUDE_PLUGIN_DATA: stateDirectory,
        XDG_RUNTIME_DIR: stateDirectory,
        NATS_AUTH: "broker",
        AGENT_SECRETS_URL: `http://127.0.0.1:${tokens.port}`,
        AGENT_SECRETS_MACHINE_IDENTITY: "kubernetes",
        AGENT_SECRETS_MACHINE_TOKEN_FILE: tokenFile,
        ...NO_TRANSPILER_CACHE,
      },
      stderr: "pipe",
    })
    let stderr = ""
    transport.stderr?.on("data", (chunk: Buffer | string) => {
      stderr += String(chunk)
    })
    const whoami = () =>
      client
        .callTool({ name: "envoy_whoami", arguments: {} }, undefined, {
          timeout: untilTheGuard(JOB_ROOM_GUARD_MS),
        })
        .then(
          () => "answered",
          (error: unknown) => String(error),
        )

    try {
      await connectedToServer(client, transport, () => stderr, JOB_ROOM_GUARD_MS)
      // Connected, and the start's registration held: the keeper warns about the lapsing token.
      // Each wait ends on its fact, and names itself 5 s inside the row's guard; deadlines of 30 s
      // and 15 s decided it before, against a row that cost 62 505 ms on a loaded machine.
      await waitFor(
        async () => broker.subscribed.length > 0,
        "the session's subscriptions to reach NATS",
        () => stderr,
        untilTheGuard(JOB_ROOM_GUARD_MS),
      )
      await waitFor(
        async () => stderr.includes("envoy-channel: the NATS connection's broker token"),
        "the keeper's warning",
        () => stderr,
        untilTheGuard(JOB_ROOM_GUARD_MS),
      )
      const answer = await whoami()
      expect(answer).toContain(
        "Envoy channel server is still starting: it registers this session with Envoy",
      )
      expect(answer).not.toContain("broker token")
    } finally {
      held.resolve()
      await client.close().catch(() => undefined)
      listener.stop(true)
      tokens.stop(true)
      await broker.stop()
      await rm(stateDirectory, { recursive: true, force: true })
    }
  },
  JOB_ROOM_GUARD_MS,
)
