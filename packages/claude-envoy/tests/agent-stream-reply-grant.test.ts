/**
 * The live view's replay answer on a Claude Code session (`startAgentStream`,
 * `../src/agent-stream-runner`) through a real nats-server, on a connection authenticated as a
 * session user whose grant is the one the NATS auth service gives a session: publish its replies
 * under `_INBOX.*.<session id>.>` alone (`sessionGrantConfig`). Dispatch asks for the replay on a
 * reply subject naming the session (`sessionReplySubject`), and the answer arrives. Asked on the
 * reply subject Dispatch used before, nats.go's own request inbox, the same answer is refused by
 * the server, which logs the refusal, and reaches nobody: the control that the grant is enforced
 * and that the requester hears what the session publishes.
 *
 * The mod is a silent stand-in: the replay answer is taken from the stream's own ring, which a
 * session no mod has spoken to holds empty, and the subject it goes to is what this row is about.
 *
 * Docker-backed: the claude-envoy job runs this suite with LEGION_E2E=1 after pulling the image.
 */
import { afterAll, beforeAll, describe, expect, test } from "bun:test"
import { agentStreamControlSubject } from "@legion/contracts"
import { natsConnectionOptions } from "@legion/envoy-client/nats-connection"
import {
  type NatsServer,
  sessionGrantConfig,
  sessionReplySubject,
  startNatsServerWithConfig,
  useNatsServerDirectory,
} from "@legion/envoy-client/test/nats-server"
import { connect, createInbox, type NatsConnection, nkeys } from "nats"
import type { ModEvent } from "../hooks/mod-protocol"
import { startAgentStream } from "../src/agent-stream-runner"
import type { ModLink } from "../src/mod-link"

/** nats.js types its `nkeys` export `any`. */
const testKeys: { createUser(): { getSeed(): Uint8Array; getPublicKey(): string } } = nkeys

const serverDirectory = useNatsServerDirectory()

const SESSION = "0c3b6f6e-0000-4000-8000-0000000000aa"

// Destructured: this package's typecheck forbids `process.env.X`.
const { LEGION_E2E } = process.env

/** A mod link no mod ever speaks on: no event, no session id move, every call refused. */
function silentLink(): { readonly link: ModLink; readonly close: () => void } {
  const ending = Promise.withResolvers<void>()
  // No event ever comes: the iterator's one answer is its end, once the link closes.
  const events: AsyncIterableIterator<ModEvent> = {
    next: async () => {
      await ending.promise
      return { done: true, value: undefined }
    },
    [Symbol.asyncIterator]() {
      return this
    },
  }
  const link: ModLink = {
    close: async () => ending.resolve(),
    connected: () => false,
    events,
    onConnected: () => undefined,
    onSessionId: () => undefined,
    request: () => Promise.reject(new Error("no mod speaks on this link")),
    sessionId: () => SESSION,
    socketPath: "/scratch/claude-envoy-reply-grant.sock",
  }
  return { close: () => ending.resolve(), link }
}

/** Asks `connection` for the session's replay on `reply` and answers the first answer's text, or
 *  undefined when none arrives within `withinMs`. */
async function askReplay(
  connection: NatsConnection,
  reply: string,
  withinMs: number,
): Promise<string | undefined> {
  const answers = connection.subscribe(reply, { max: 1 })
  await connection.flush()
  connection.publish(
    agentStreamControlSubject(SESSION),
    new TextEncoder().encode(JSON.stringify({ type: "replay", v: 1 })),
    { reply },
  )
  const timer = setTimeout(() => answers.unsubscribe(), withinMs)
  try {
    for await (const answer of answers) return answer.string()
    return undefined
  } finally {
    clearTimeout(timer)
  }
}

describe.skipIf(LEGION_E2E !== "1")("the replay answer under a session's own NATS grant", () => {
  const sessionUser = testKeys.createUser()
  const requesterUser = testKeys.createUser()
  let server: NatsServer | undefined
  const open: NatsConnection[] = []

  beforeAll(async () => {
    server = await startNatsServerWithConfig(
      serverDirectory,
      sessionGrantConfig({
        requesterKey: requesterUser.getPublicKey(),
        sessionID: SESSION,
        sessionKey: sessionUser.getPublicKey(),
      }),
    )
  }, 120_000)
  afterAll(async () => {
    for (const connection of open) await connection.close()
    server?.stop()
  }, 60_000)

  test("answers a replay asked on its own reply prefix, and nothing on nats.go's request inbox", async () => {
    if (server === undefined) throw new Error("the NATS server did not start")
    const decoder = new TextDecoder()
    const session = await connect({
      ...natsConnectionOptions({
        name: "claude-envoy-reply-grant",
        seed: { source: "the row's session user", value: decoder.decode(sessionUser.getSeed()) },
        servers: [server.url],
      }),
      reconnect: false,
    })
    open.push(session)
    const requester = await connect({
      ...natsConnectionOptions({
        name: "claude-envoy-reply-grant-requester",
        seed: {
          source: "the row's requester user",
          value: decoder.decode(requesterUser.getSeed()),
        },
        servers: [server.url],
      }),
      reconnect: false,
    })
    open.push(requester)
    const mod = silentLink()
    const runner = startAgentStream({
      connection: session,
      followsSessionId: false,
      link: mod.link,
      sessionId: SESSION,
      warn: () => undefined,
    })
    try {
      // The control subscription is the runner's own; the first replay answered says it is
      // in place, and the server has taken its SUB before any later request.
      const deadline = Date.now() + 10_000
      let answer: string | undefined
      while (answer === undefined) {
        if (Date.now() > deadline) throw new Error("no replay answer on the session's own prefix")
        answer = await askReplay(requester, sessionReplySubject(createInbox(), SESSION), 500)
      }
      expect(JSON.parse(answer)).toMatchObject({ session_id: SESSION, v: 1 })

      // The control: the reply subject Dispatch asked on before, nats.go's request inbox.
      const before = `${createInbox()}.1`
      expect(await askReplay(requester, before, 1_500)).toBeUndefined()
      const refused = `Publish Violation`
      let logged = ""
      const logDeadline = Date.now() + 10_000
      while (
        !logged.split("\n").some((line) => line.includes(refused) && line.includes(`"${before}"`))
      ) {
        if (Date.now() > logDeadline) {
          throw new Error(`the server never logged a refused publish to ${before}:\n${logged}`)
        }
        logged = await server.log()
      }
      // And the session still answers on its own prefix after the refusal.
      const again = await askReplay(requester, sessionReplySubject(createInbox(), SESSION), 5_000)
      expect(again === undefined ? undefined : JSON.parse(again)).toMatchObject({
        session_id: SESSION,
      })
    } finally {
      runner.stop()
      mod.close()
    }
  }, 60_000)
})
