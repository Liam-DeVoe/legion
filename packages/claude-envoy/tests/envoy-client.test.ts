import { afterAll, beforeAll, expect, test } from "bun:test"
import { writeFileSync } from "node:fs"
import { createServer, type Server } from "node:net"
import { join, resolve } from "node:path"
import { REDIRECT_CODES, redirectServers } from "@legion/test-fixtures/redirect-servers"
import { temporaryDirectory } from "@legion/test-fixtures/temporary-directories"

/** The idempotency key the shared transport mints for a send that names none. */
const MINTED_KEY = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/

/** The sentence of the listener's 429 for a session past its sending limit
 *  (`senderRateLimitedAnswer`, packages/envoy/cmd/listener/sender_limit.go). */
const RATE_LIMITED_SENTENCE =
  "Your sending is rate limited. This message was not sent. This listener lets one session send " +
  "4 messages a minute and 2 at once. You can send it again after 15 seconds."

type SendBody = {
  readonly target_session: string
  readonly message: string
  readonly idempotency_key: string
}

const requests: { path: string; authorization: string | null; body: unknown }[] = []
// Whether the listener refuses every send for its sender's rate, as it does past the limit.
let rateLimited = false
const server = Bun.serve({
  port: 0,
  fetch: async (request) => {
    const body = (await request.json()) as SendBody
    requests.push({
      path: new URL(request.url).pathname,
      authorization: request.headers.get("authorization"),
      body,
    })
    if (rateLimited) {
      return Response.json(
        {
          error: RATE_LIMITED_SENTENCE,
          reason: "sender_rate_limited",
          retry_after_seconds: 15,
          messages_per_minute: 4,
          burst: 2,
        },
        { status: 429, headers: { "Retry-After": "15" } },
      )
    }
    // The listener's answer to a stored send: the envelope it stored, and the recipient.
    return Response.json({
      event_id: "evt_sent",
      source: "agent",
      source_event_id: "agent.evt_sent",
      topic: `notifications.agent.${body.target_session}`,
      dedupe_key: `agent.${body.target_session}.${body.idempotency_key}`,
      issued_at: 1,
      payload_summary: body.message,
      trace_id: "trace-sent",
      recipient: body.target_session,
    })
  },
})
const scratch = temporaryDirectory("claude-envoy-send")
const configFile = join(scratch, "envoy.json")
beforeAll(() => {
  writeFileSync(
    configFile,
    JSON.stringify({
      envoy: { enabled: true, url: `http://127.0.0.1:${server.port}`, token: "listener-bearer" },
    }),
  )
})
afterAll(() => {
  server.stop(true)
})

/** Runs `envoy-send` with `words` in `scratch`, with `env` and PATH as its whole environment. */
async function envoySend(
  words: readonly string[],
  env: Readonly<Record<string, string>>,
): Promise<{ readonly exitCode: number; readonly stdout: string; readonly stderr: string }> {
  const send = Bun.spawn(["bun", resolve(import.meta.dir, "../bin/envoy-send.ts"), ...words], {
    cwd: scratch,
    env: { PATH: process.env["PATH"] ?? "", HOME: scratch, ...env },
    stdout: "pipe",
    stderr: "pipe",
  })
  const [exitCode, stdout, stderr] = await Promise.all([
    send.exited,
    new Response(send.stdout).text(),
    new Response(send.stderr).text(),
  ])
  return { exitCode, stdout, stderr }
}

test("envoy-send sends to the listener and with the bearer its envoy.json names", async () => {
  requests.length = 0
  const sent = await envoySend(["ses_receiver", "file", "send"], { ENVOY_CONFIG_FILE: configFile })
  expect(sent.exitCode).toBe(0)
  expect(sent.stdout).toBe("Sent Envoy message to ses_receiver.\n")
  // A shell that names no session sends no source_session.
  expect(requests).toEqual([
    {
      path: "/v1/messages/send",
      authorization: "Bearer listener-bearer",
      body: {
        source: "agent",
        target_session: "ses_receiver",
        message: "file send",
        idempotency_key: expect.stringMatching(MINTED_KEY),
      },
    },
  ])
})

test("envoy-send whose sender is rate limited sends its session as source_session, prints the listener's sentence and exits 1", async () => {
  requests.length = 0
  rateLimited = true
  try {
    const sent = await envoySend(["ses_receiver", "limited", "send"], {
      ENVOY_CONFIG_FILE: configFile,
      ENVOY_SESSION_ID: "ses_sender",
    })
    expect(sent.exitCode).toBe(1)
    expect(sent.stderr).toBe(
      `Claude Envoy send failed: ${RATE_LIMITED_SENTENCE} (reason: sender_rate_limited)\n`,
    )
    // One request: a refusal is not sent again.
    expect(requests).toEqual([
      {
        path: "/v1/messages/send",
        authorization: "Bearer listener-bearer",
        body: {
          source: "agent",
          source_session: "ses_sender",
          target_session: "ses_receiver",
          message: "limited send",
          idempotency_key: expect.stringMatching(MINTED_KEY),
        },
      },
    ])
  } finally {
    rateLimited = false
  }
})

test("envoy-send with no Envoy configured refuses, saying why, and sends nothing", async () => {
  requests.length = 0
  const missing = join(scratch, "absent", "envoy.json")
  const sent = await envoySend(["ses_receiver", "nothing"], { ENVOY_CONFIG_FILE: missing })
  expect(sent.exitCode).toBe(1)
  expect(sent.stderr).toBe(
    `Claude Envoy send failed: Envoy is off: no envoy.json at ${missing}: Envoy is off\n`,
  )
  expect(requests).toEqual([])
})

// The transport every client sends through follows no redirect (`EnvoyRedirectError`,
// `@legion/envoy-client/transport`): fetch would send the listener's bearer or the broker's DPoP
// proof, and the message, on to the origin a 3xx names. Through the command a person runs, a 3xx
// fails the send naming the status and where it pointed, exits 1 and prints no `Sent`, at each
// code, and is not sent again. Before, the send followed it and printed `Sent` on the other
// origin's answer.
test("envoy-send answered with a redirect exits 1 naming it, sends nothing to the other origin, and prints no Sent", async () => {
  let asked = 0
  const servers = redirectServers({
    redirects: () => {
      asked += 1
      return true
    },
    atElsewhere: () => new Response(null, { status: 202 }),
  })
  const file = join(scratch, "redirected-envoy.json")
  writeFileSync(
    file,
    JSON.stringify({ envoy: { enabled: true, url: servers.origin, token: "made-up-bearer" } }),
  )
  try {
    for (const code of REDIRECT_CODES) {
      servers.code = code
      asked = 0
      const sent = await envoySend(["ses_receiver", "redirected"], { ENVOY_CONFIG_FILE: file })
      expect(servers.received, `the other origin after a ${code}`).toEqual([])
      expect({ ...sent, asked }).toEqual({
        exitCode: 1,
        stdout: "",
        stderr: `Claude Envoy send failed: POST ${servers.origin}/v1/messages/send answered ${code}, redirecting to ${servers.elsewhere}, and the Envoy client follows no redirect: set the Envoy URL (ENVOY_URL, or envoy.json's envoy.url) to the listener's own address\n`,
        asked: 1,
      })
    }
  } finally {
    servers.stop()
  }
}, 30_000)

// A listener URL may carry a user and a password, which the send keeps; no line it prints does:
// the transport names each request's URL with neither (`shownSettingUrl`). The password is made
// for this run, so no line of the test holds a credential.
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
  try {
    const printed: Record<string, { exit: number; holdsUserinfo: boolean; line: unknown }> = {}
    const want: typeof printed = {}
    for (const [answer, port, wanted] of [
      ...servers.map(
        ({ status, server }) =>
          [
            String(status),
            server.port,
            // A 500 is tried again, and a send whose last answer is a 500 is not confirmed
            // delivered: its line holds the refusal's own line.
            status === 500
              ? expect.stringContaining(
                  `(POST http://127.0.0.1:${server.port}/v1/messages/send failed with 500: no)`,
                )
              : `Claude Envoy send failed: POST http://127.0.0.1:${server.port}/v1/messages/send failed with ${status}: no\n`,
          ] as const,
      ),
      // Bun's own words for a failed connection, which name no URL.
      [
        "a dropped connection",
        dropped,
        expect.stringMatching(/^Claude Envoy send failed: /),
      ] as const,
      [
        "a refused connection",
        refused,
        expect.stringMatching(/^Claude Envoy send failed: /),
      ] as const,
    ]) {
      const file = join(scratch, `userinfo-envoy-${answer}.json`)
      const url = `http://made-up-user:${password}@127.0.0.1:${port}`
      writeFileSync(
        file,
        JSON.stringify({ envoy: { enabled: true, url, token: "made-up-bearer" } }),
      )
      const { exitCode, stdout, stderr } = await envoySend(["ses_receiver", "userinfo"], {
        ENVOY_CONFIG_FILE: file,
      })
      const output = `${stdout}${stderr}`
      printed[answer] = {
        exit: exitCode,
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

// The Envoy URL may carry a user and a password. One the URL parser does not read as an http or
// https URL with a host turns Envoy off before any client is built, named by its length alone: the
// parser's own error would hold the value whole. One that holds an `@` is also told how to write a
// user part. Among them, a user part that holds a `/` or a `#` (a base64 token can) makes the
// parser read a host taken from it, here a listener of the test's own, which gets no request. The
// password is made for this run.
test("envoy-send whose Envoy URL is no http or https URL with a host, or holds an @ that no user part ends, sends nothing and names its length alone", async () => {
  const password = crypto.randomUUID()
  let asked = 0
  const listener = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    fetch: () => {
      asked += 1
      return Response.json({ ok: true })
    },
  })
  try {
    const values = [
      `made-up-user:${password}@127.0.0.1:1`,
      `mailto:made-up-user:${password}@127.0.0.1`,
      `//made-up-user:${password}@127.0.0.1:1`,
      `http://made-up-user:${password}/x@127.0.0.1:1`,
      `http://127.0.0.1:${listener.port}/${password}@envoy.example`,
      `http://127.0.0.1:${listener.port}#${password}@envoy.example`,
    ]
    const userPart =
      "; if it carries a user part, write each @, /, ?, # and \\ in that part percent-encoded (@ as %40, / as %2F), or remove the user part"
    const said: { readonly exit: number; readonly line: string }[] = []
    for (const value of values) {
      const { exitCode, stdout, stderr } = await envoySend(["ses_receiver", "never sent"], {
        ENVOY_CONFIG_FILE: configFile,
        ENVOY_URL: value,
      })
      said.push({ exit: exitCode, line: `${stdout}${stderr}` })
    }
    expect({ said, asked }).toEqual({
      said: values.map((value) => ({
        exit: 1,
        line: `Claude Envoy send failed: Envoy is off: ENVOY_URL must be an http or https URL: its value is ${value.length} characters long${value.includes("@") ? userPart : ""}\n`,
      })),
      asked: 0,
    })
  } finally {
    listener.stop(true)
  }
}, 30_000)
