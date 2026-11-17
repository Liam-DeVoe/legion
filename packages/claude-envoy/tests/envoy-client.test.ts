import { afterAll, expect, test } from "bun:test"
import { writeFileSync } from "node:fs"
import { createServer, type Server, type Socket } from "node:net"
import { join, resolve } from "node:path"
import {
  REDIRECT_CODES,
  type RedirectServers,
  redirectCase,
  redirectServers,
  UNNAMED_TARGETS,
} from "@legion/test-fixtures/redirect-servers"
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories"
import { sendEnvoyMessage } from "../src/envoy-client"

const requests: { path: string; authorization: string | null; body: string }[] = []
const server = Bun.serve({
  port: 0,
  fetch: async (request) => {
    requests.push({
      path: new URL(request.url).pathname,
      authorization: request.headers.get("authorization"),
      body: await request.text(),
    })
    return new Response(null, { status: 202 })
  },
})
const scratch = temporaryDirectory("claude-envoy-send")
afterAll(() => {
  server.stop(true)
})

test("sends a direct message through Envoy's HTTP API, with no bearer for a listener that asks for none", async () => {
  requests.length = 0
  await sendEnvoyMessage({
    baseUrl: `http://127.0.0.1:${server.port}`,
    credential: undefined,
    targetSession: "ses_receiver",
    message: "adapter send proof",
  })
  expect(requests).toEqual([
    {
      path: "/v1/messages/send",
      authorization: null,
      body: JSON.stringify({ target_session: "ses_receiver", message: "adapter send proof" }),
    },
  ])
})

/** What `call` settled with by the next turn of the event loop, or "pending": the test's bound. */
async function outcome(call: Promise<unknown>): Promise<unknown> {
  const bound = Promise.withResolvers<"pending">()
  setImmediate(() => bound.resolve("pending"))
  return await Promise.race([
    call.then(
      (value) => value,
      (error: unknown) => error,
    ),
    bound.promise,
  ])
}

test("a send hands the credential its deadline: waiting on a pod's missing registration, it fails at that deadline naming the session, and sends once the registration lands", async () => {
  requests.length = 0
  let registered = false
  const given: (AbortSignal | undefined)[] = []
  // A broker credential while the pod's registration is missing: it answers nothing until its
  // caller's deadline ends, then says the session's registration is pending.
  const credential = {
    scheme: "DPoP" as const,
    headers: (_method: string, _url: string, _session?: string, signal?: AbortSignal) => {
      given.push(signal)
      if (registered) return { Authorization: "DPoP registered-token" }
      const { promise, reject } = Promise.withResolvers<Readonly<Record<string, string>>>()
      signal?.addEventListener(
        "abort",
        () => reject(new Error("registration pending: implementer-g1")),
        {
          once: true,
        },
      )
      return promise
    },
    stale: () => {},
    secret: () => "",
  }
  const options = {
    baseUrl: `http://127.0.0.1:${server.port}`,
    credential,
    targetSession: "ses_receiver",
    message: "sent once registered",
  }
  // The send's deadline is a signal the test ends, standing in for the call's own timer.
  const deadline = new AbortController()
  const send = sendEnvoyMessage({ ...options, signal: deadline.signal })
  await new Promise((resolve) => setImmediate(resolve))
  expect(given[0], "the send handed its credential no deadline").toBeInstanceOf(AbortSignal)
  // The control: while the deadline is live the send is still waiting on the credential.
  expect(await outcome(send)).toBe("pending")
  deadline.abort()
  const refused = await outcome(send)
  expect(refused, "the send did not return at its deadline").toBeInstanceOf(Error)
  expect((refused as Error).message).toContain("implementer-g1")
  expect(requests).toEqual([])
  registered = true
  await sendEnvoyMessage({ ...options, signal: new AbortController().signal })
  expect(requests.map((one) => one.authorization)).toEqual(["DPoP registered-token"])
})

test("envoy-send sends to the listener and with the bearer its envoy.json names", async () => {
  requests.length = 0
  const file = join(scratch, "envoy.json")
  writeFileSync(
    file,
    JSON.stringify({
      envoy: { enabled: true, url: `http://127.0.0.1:${server.port}`, token: "listener-bearer" },
    }),
  )
  const send = Bun.spawn(
    ["bun", resolve(import.meta.dir, "../bin/envoy-send.ts"), "ses_receiver", "file", "send"],
    {
      cwd: scratch,
      env: { PATH: process.env["PATH"] ?? "", HOME: scratch, ENVOY_CONFIG_FILE: file },
      stdout: "pipe",
      stderr: "pipe",
    },
  )
  expect(await send.exited).toBe(0)
  expect(await new Response(send.stdout).text()).toBe("Sent Envoy message to ses_receiver.\n")
  expect(requests).toEqual([
    {
      path: "/v1/messages/send",
      authorization: "Bearer listener-bearer",
      body: JSON.stringify({ target_session: "ses_receiver", message: "file send" }),
    },
  ])
})

test("envoy-send with no Envoy configured refuses, saying why, and sends nothing", async () => {
  requests.length = 0
  const missing = join(scratch, "absent", "envoy.json")
  const send = Bun.spawn(
    ["bun", resolve(import.meta.dir, "../bin/envoy-send.ts"), "ses_receiver", "nothing"],
    {
      cwd: scratch,
      env: { PATH: process.env["PATH"] ?? "", HOME: scratch, ENVOY_CONFIG_FILE: missing },
      stdout: "pipe",
      stderr: "pipe",
    },
  )
  expect(await send.exited).toBe(1)
  expect(await new Response(send.stderr).text()).toBe(
    `Claude Envoy send failed: Envoy is off: no envoy.json at ${missing}: Envoy is off\n`,
  )
  expect(requests).toEqual([])
})

/** A broker credential as a send presents one: a made-up access token and DPoP proof. */
const madeUpBroker = {
  scheme: "DPoP" as const,
  headers: () => ({ Authorization: "DPoP made-up-access-token", DPoP: "made-up-dpop-proof" }),
  stale: () => {},
  secret: () => "made-up-access-token",
}

/** The send to the first of `servers`, with the made-up broker credential: what it refused with,
 * or undefined when it sent. */
async function sendRefusal(servers: RedirectServers): Promise<unknown> {
  return await sendEnvoyMessage({
    baseUrl: servers.origin,
    credential: madeUpBroker,
    targetSession: "ses_receiver",
    message: "a made-up message",
  }).then(
    () => undefined,
    (error: unknown) => error,
  )
}

const FIX = "set the Envoy URL (ENVOY_URL, or envoy.json's envoy.url) to the listener's own address"

// fetch would send the broker's DPoP proof on to the origin a redirect names, and a 307's or 308's
// message with it (it drops `Authorization` alone), so a send follows no redirect: the 3xx is the
// answer, and the send fails naming it. One case for each code, so a send that follows again fails
// at each.
for (const code of REDIRECT_CODES) {
  test(redirectCase(code), async () => {
    const servers = redirectServers({ atElsewhere: () => new Response(null, { status: 202 }) })
    servers.code = code
    try {
      const refusal = await sendRefusal(servers)
      expect(servers.received, `the other origin after a ${code}`).toEqual([])
      expect(refusal, `the send after a ${code}`).toBeInstanceOf(Error)
      expect((refusal as Error).message).toBe(
        `POST ${servers.origin}/v1/messages/send answered ${code}, redirecting to ${servers.elsewhere}, and a send follows no redirect: ${FIX}`,
      )
    } finally {
      servers.stop()
    }
  })
}

// A send that read a redirect's body before its status would wait on a body that never ends, and
// refuse nothing; the test's limit is that wait's bound.
test("a redirect whose body never ends is refused naming its status, before any of its body is read", async () => {
  const servers = redirectServers()
  servers.endlessBody = true
  try {
    expect(((await sendRefusal(servers)) as Error).message).toBe(
      `POST ${servers.origin}/v1/messages/send answered 307, redirecting to ${servers.elsewhere}, and a send follows no redirect: ${FIX}`,
    )
  } finally {
    servers.stop()
  }
}, 10_000)

// A redirect within the listener's own address names no address the send lacks, and the send's
// path is its own whatever the URL's, so its refusal says what to check. One that names nowhere is
// not named as the listener's own address, and a host longer than any host name is never printed.
test("a redirect to another path of the same address, to no target, or to an overlong host is refused saying so", async () => {
  const servers = redirectServers()
  try {
    const said: string[] = []
    for (const { location } of UNNAMED_TARGETS) {
      servers.location = location
      said.push(((await sendRefusal(servers)) as Error).message)
    }
    expect(said).toEqual(
      UNNAMED_TARGETS.map(
        ({ where, sameAddress }) =>
          `POST ${servers.origin}/v1/messages/send answered 307, ${where}, and a send follows no redirect: ${sameAddress ? "the client builds that path itself, so the server at that address, or a proxy in front of it, chose the other one: check what answers there" : FIX}`,
      ),
    )
  } finally {
    servers.stop()
  }
})

// A redirect the send followed printed "Sent" and exited 0 with the other origin's answer: a
// false success. A 3xx fails the command, naming the status and where it pointed.
test("envoy-send answered with a redirect exits 1 naming it, and prints no Sent", async () => {
  const servers = redirectServers({ atElsewhere: () => new Response(null, { status: 202 }) })
  const file = join(scratch, "redirected-envoy.json")
  writeFileSync(
    file,
    JSON.stringify({ envoy: { enabled: true, url: servers.origin, token: "made-up-bearer" } }),
  )
  try {
    for (const code of REDIRECT_CODES) {
      servers.code = code
      const send = Bun.spawn(
        ["bun", resolve(import.meta.dir, "../bin/envoy-send.ts"), "ses_receiver", "redirected"],
        {
          cwd: scratch,
          env: { PATH: process.env["PATH"] ?? "", HOME: scratch, ENVOY_CONFIG_FILE: file },
          stdout: "pipe",
          stderr: "pipe",
        },
      )
      const [exit, stdout, stderr] = await Promise.all([
        send.exited,
        new Response(send.stdout).text(),
        new Response(send.stderr).text(),
      ])
      expect(servers.received, `the other origin after a ${code}`).toEqual([])
      expect({ exit, stdout, stderr }).toEqual({
        exit: 1,
        stdout: "",
        stderr: `Claude Envoy send failed: POST ${servers.origin}/v1/messages/send answered ${code}, redirecting to ${servers.elsewhere}, and a send follows no redirect: set the Envoy URL (ENVOY_URL, or envoy.json's envoy.url) to the listener's own address\n`,
      })
    }
  } finally {
    servers.stop()
  }
})

// A listener URL may carry a user and a password, which the send keeps; no line it prints does. The
// password is made for this run, so no line of the test holds a credential.
test("envoy-send prints no user and no password of its Envoy URL, whatever the listener answers", async () => {
  const password = crypto.randomUUID()
  const answering = (status: number) =>
    Bun.serve({ port: 0, hostname: "127.0.0.1", fetch: () => new Response("no", { status }) })
  const servers = [401, 403, 404, 500].map((status) => ({ status, server: answering(status) }))
  // A listener that drops each connection unanswered, and a port nothing listens on.
  const listen = async (server: Server) => {
    const listening = Promise.withResolvers<void>()
    server.listen(0, "127.0.0.1", listening.resolve)
    await listening.promise
    const address = server.address()
    if (address === null || typeof address === "string") throw new Error("no port")
    return address.port
  }
  const dropping = createServer((socket) => socket.destroy())
  const dropped = await listen(dropping)
  const closed = createServer()
  const refused = await listen(closed)
  closed.close()
  const statusText = {
    401: "Unauthorized",
    403: "Forbidden",
    404: "Not Found",
    500: "Internal Server Error",
  }
  try {
    const printed: Record<string, { exit: number; holdsUserinfo: boolean; line: unknown }> = {}
    const want: typeof printed = {}
    for (const [answer, port, wanted] of [
      ...servers.map(
        ({ status, server }) =>
          [
            String(status),
            server.port,
            `Claude Envoy send failed: Request failed with status code ${status} ${statusText[status as keyof typeof statusText]}: POST http://127.0.0.1:${server.port}/v1/messages/send\n`,
          ] as const,
      ),
      // Bun's own words for a failed connection, which name no URL.
      [
        "a dropped connection",
        dropped,
        expect.stringMatching(/^Claude Envoy send failed: ECONNRESET: /),
      ] as const,
      [
        "a refused connection",
        refused,
        expect.stringMatching(/^Claude Envoy send failed: ECONNREFUSED: /),
      ] as const,
    ]) {
      const file = join(scratch, `userinfo-envoy-${answer}.json`)
      const url = `http://made-up-user:${password}@127.0.0.1:${port}`
      writeFileSync(
        file,
        JSON.stringify({ envoy: { enabled: true, url, token: "made-up-bearer" } }),
      )
      const send = Bun.spawn(
        ["bun", resolve(import.meta.dir, "../bin/envoy-send.ts"), "ses_receiver", "userinfo"],
        {
          cwd: scratch,
          env: { PATH: process.env["PATH"] ?? "", HOME: scratch, ENVOY_CONFIG_FILE: file },
          stdout: "pipe",
          stderr: "pipe",
        },
      )
      const [exit, stdout, stderr] = await Promise.all([
        send.exited,
        new Response(send.stdout).text(),
        new Response(send.stderr).text(),
      ])
      const output = `${stdout}${stderr}`
      printed[answer] = {
        exit,
        holdsUserinfo: output.includes(password) || output.includes("made-up-user"),
        line: output.replaceAll(password, "<the password>"),
      }
      want[answer] = { exit: 1, holdsUserinfo: false, line: wanted }
    }
    expect(printed).toEqual(want)
  } finally {
    for (const { server } of servers) server.stop(true)
    dropping.close()
  }
}, 30_000)

/** A listener that takes every connection and never answers it, and the port it holds. */
async function silentListener(): Promise<{ readonly port: number; readonly stop: () => void }> {
  const held: Socket[] = []
  const server = createServer((socket) => {
    held.push(socket)
  })
  const listening = Promise.withResolvers<void>()
  server.listen(0, "127.0.0.1", listening.resolve)
  await listening.promise
  const address = server.address()
  if (address === null || typeof address === "string") throw new Error("no port")
  return {
    port: address.port,
    stop: () => {
      for (const socket of held) socket.destroy()
      server.close()
    },
  }
}

// A send whose listener never answers ends at its own deadline or at its caller's signal, and the
// line it fails with is that end's own: the send's catch hands on an error it did not make as it
// was thrown, and a `DOMException`'s message cannot be written.
test("a send its caller aborts ends with the abort's own error", async () => {
  const listener = await silentListener()
  try {
    const caller = new AbortController()
    const send = sendEnvoyMessage({
      baseUrl: `http://127.0.0.1:${listener.port}`,
      credential: undefined,
      targetSession: "ses_receiver",
      message: "never answered",
      signal: caller.signal,
    }).then(
      () => undefined,
      (error: unknown) => error,
    )
    caller.abort()
    expect(await send).toBe(caller.signal.reason)
  } finally {
    listener.stop()
  }
})

// The send's own 5 s deadline, through the command a person runs: the line names the timeout. The
// limit is a hang guard over the command's start and that deadline, with room for a loaded
// machine.
test("envoy-send to a listener that never answers exits 1 saying the send timed out", async () => {
  const listener = await silentListener()
  const file = join(scratch, "silent-envoy.json")
  writeFileSync(
    file,
    JSON.stringify({
      envoy: { enabled: true, url: `http://127.0.0.1:${listener.port}`, token: "made-up-bearer" },
    }),
  )
  try {
    const send = Bun.spawn(
      ["bun", resolve(import.meta.dir, "../bin/envoy-send.ts"), "ses_receiver", "unanswered"],
      {
        cwd: scratch,
        env: { PATH: process.env["PATH"] ?? "", HOME: scratch, ENVOY_CONFIG_FILE: file },
        stdout: "pipe",
        stderr: "pipe",
      },
    )
    const [exit, stdout, stderr] = await Promise.all([
      send.exited,
      new Response(send.stdout).text(),
      new Response(send.stderr).text(),
    ])
    expect({ exit, stdout, stderr }).toEqual({
      exit: 1,
      stdout: "",
      stderr: "Claude Envoy send failed: The operation timed out.\n",
    })
  } finally {
    listener.stop()
  }
}, 30_000)

// The line that names the URL is the send's own error, made where the URL is cleaned of its user
// and password, and the error it replaces is its cause.
test("a send refused by its listener names the URL with no user and no password, and keeps the refusal as its cause", async () => {
  const password = crypto.randomUUID()
  const refusing = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    fetch: () => new Response("no", { status: 401 }),
  })
  try {
    const error = await sendEnvoyMessage({
      baseUrl: `http://made-up-user:${password}@127.0.0.1:${refusing.port}`,
      credential: undefined,
      targetSession: "ses_receiver",
      message: "refused",
    }).then(
      () => undefined,
      (thrown: unknown) => thrown,
    )
    expect(error).toBeInstanceOf(Error)
    expect((error as Error).message).toBe(
      `Request failed with status code 401 Unauthorized: POST http://127.0.0.1:${refusing.port}/v1/messages/send`,
    )
    expect((error as Error).cause).toBeInstanceOf(Error)
    expect(((error as Error).cause as Error).name).toBe("HTTPError")
  } finally {
    refusing.stop(true)
  }
})
