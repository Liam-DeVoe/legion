// dialChannelNats: the channel server's NATS connection, opened with the session's credential and,
// where that is the broker token, kept renewed for the connection's life and stopped on request.
import { afterEach, expect, test } from "bun:test"
import { mkdtemp, writeFile } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { forgetBrokerClients } from "@legion/envoy-client/broker-credential"
import {
  type BrokerNatsCredential,
  brokerNatsCredential,
} from "@legion/envoy-client/nats-credential"
import { type Authenticator, type ConnectionOptions, type NatsConnection, nkeys } from "nats"
import { dialChannelNats } from "../src/envoy-channel-server"

const servers: { stop: (closeActive?: boolean) => void }[] = []
afterEach(() => {
  forgetBrokerClients()
  for (const server of servers.splice(0)) server.stop(true)
})

/** A broker whose token exchange mints a numbered token that lives two seconds. */
function shortTokenBroker() {
  const state = { minted: 0 }
  const server = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    fetch: () => {
      state.minted += 1
      return Response.json({
        access_token: `channel-access-${state.minted}`,
        issued_token_type: "urn:ietf:params:oauth:token-type:access_token",
        token_type: "DPoP",
        expires_in: 2,
      })
    },
  })
  servers.push(server)
  return { url: `http://127.0.0.1:${server.port}`, state }
}

/** A connection as the keeper sees it: each connect and reconnect asks the authenticator for the
 * CONNECT's credential, as nats.js does, and records the token it presented. */
function fakeDial() {
  const presented: string[] = []
  let reconnects = 0
  const ended = Promise.withResolvers<void>()
  const dial = async (options: ConnectionOptions): Promise<NatsConnection> => {
    const present = () => {
      const authenticator = options.authenticator as Authenticator | undefined
      const credential = authenticator?.("nonce") as { auth_token?: string } | undefined
      if (credential?.auth_token !== undefined) presented.push(credential.auth_token)
    }
    present()
    return {
      reconnect: async () => {
        reconnects += 1
        present()
      },
      isClosed: () => false,
      closed: () => ended.promise,
    } as unknown as NatsConnection
  }
  return { dial, presented, reconnects: () => reconnects }
}

test("dialChannelNats keeps the broker token renewed until it is stopped", async () => {
  const broker = shortTokenBroker()
  const home = await mkdtemp(join(tmpdir(), "channel-nats-"))
  const tokenFile = join(home, "token")
  await writeFile(tokenFile, "projected.service.account.token\n")
  const credential = brokerNatsCredential(
    {
      HOME: home,
      AGENT_SECRETS_URL: broker.url,
      AGENT_SECRETS_MACHINE_IDENTITY: "kubernetes",
      AGENT_SECRETS_MACHINE_TOKEN_FILE: tokenFile,
    },
    "ses_channel",
  )
  const fake = fakeDial()
  const warnings: string[] = []
  const opened = await dialChannelNats(
    { natsUrls: ["nats://nats.test:4222"], natsCredential: { kind: "broker", broker: credential } },
    "claude-envoy-channel-ses_channel",
    (text) => warnings.push(text),
    fake.dial,
  )
  // The token is taken before the dial, so the first CONNECT presents it.
  expect(fake.presented).toEqual(["channel-access-1"])
  expect(opened.followSession).toBeDefined()
  const deadline = Date.now() + 5_000
  while (fake.reconnects() === 0 && Date.now() < deadline) await Bun.sleep(50)
  expect(fake.reconnects()).toBeGreaterThan(0)
  expect(fake.presented.at(-1)).not.toBe("channel-access-1")
  expect(warnings).toEqual([])
  opened.stop()
  const reconnects = fake.reconnects()
  const minted = broker.state.minted
  await Bun.sleep(2_500)
  expect(fake.reconnects()).toBe(reconnects)
  expect(broker.state.minted).toBe(minted)
}, 15_000)

test("dialChannelNats runs no keeper and no follow for a connection with no broker token", async () => {
  const fake = fakeDial()
  const opened = await dialChannelNats(
    { natsUrls: ["nats://nats.test:4222"], natsCredential: { kind: "none" } },
    "claude-envoy-channel-ses_plain",
    () => {},
    fake.dial,
  )
  expect(opened.followSession).toBeUndefined()
  expect(fake.presented).toEqual([])
  await Bun.sleep(1_200)
  expect(fake.reconnects()).toBe(0)
  opened.stop()
})

test("a failed dial names each server by scheme, host and port, never a credential its URL holds", async () => {
  const refused = async (): Promise<NatsConnection> => {
    throw new Error("CONNECTION_REFUSED")
  }
  // Each canary URL is built here from its parts, so no source line reads as a credential-bearing
  // URL to the push leak check.
  const userinfo = (...parts: string[]) => `${parts.join(":")}@`
  const servers = [
    `nats://${userinfo("E2_SENTINEL_TOKEN")}127.0.0.1:4222`,
    `tls://${userinfo("operator", "E2_SENTINEL_PASSWORD")}nats.example:4443`,
    // nats.js dials a host and port with no scheme, so the message names it as written.
    "127.0.0.1:4223",
    `${userinfo("E2_SENTINEL_BARE")}localhost:4224`,
    "E2_SENTINEL_MALFORMED nats address",
  ]
  const failure = await dialChannelNats(
    { natsUrls: servers, natsCredential: { kind: "none" } },
    "claude-envoy-channel-ses_redact",
    () => {},
    refused,
  ).then(
    () => "dialled",
    (error: unknown) => (error instanceof Error ? error.message : String(error)),
  )
  expect(failure).toBe(
    "could not connect to NATS at nats://127.0.0.1:4222, tls://nats.example:4443, 127.0.0.1:4223, localhost:4224, a server address that does not read as a host presenting no credential: CONNECTION_REFUSED",
  )
  expect(failure).not.toContain("E2_SENTINEL")
})

test("a credential pasted as a bare server address is never named", async () => {
  // A throwaway user nkey seed, made here: it parses as a host name, as a pasted token's segments
  // do, so a value that does not read as a host name is not named.
  const seed = new TextDecoder().decode(nkeys.createUser().getSeed())
  const segments = Array.from({ length: 3 }, () =>
    crypto.randomUUID().replaceAll("-", "").repeat(2),
  )
  // nats.js's own error names the host it could not resolve, lowercased, as getaddrinfo does.
  const refused = async (): Promise<NatsConnection> => {
    throw new Error(`getaddrinfo EAI_AGAIN ${seed.toLowerCase()}`)
  }
  const failure = await dialChannelNats(
    {
      natsUrls: [seed, `nats://${seed}`, segments.join("."), "nats.example:4222"],
      natsCredential: { kind: "none" },
    },
    "claude-envoy-channel-ses_seed",
    () => {},
    refused,
  ).then(
    () => "dialled",
    (error: unknown) => (error instanceof Error ? error.message : String(error)),
  )
  expect(failure.toLowerCase()).not.toContain(seed.toLowerCase())
  for (const segment of segments) expect(failure.toLowerCase()).not.toContain(segment.toLowerCase())
  expect(failure).toBe(
    "could not connect to NATS at a server address that does not read as a host, a server address that does not read as a host, a server address that does not read as a host, nats.example:4222 presenting no credential: getaddrinfo EAI_AGAIN <a server address not shown>",
  )
})

test("a token holding an unencoded / is named by none of its segments", async () => {
  // The URL's authority ends at the `/`, so its host is the token's first segment, which nats.js
  // resolves and names in its error.
  const userinfo = (...parts: string[]) => `${parts.join(":")}@`
  const refused = async (): Promise<NatsConnection> => {
    throw new Error("getaddrinfo EAI_AGAIN e2sentineltok")
  }
  const failure = await dialChannelNats(
    {
      natsUrls: [`nats://${userinfo("e2sentineltok/def+gh==")}127.0.0.1:4222`],
      natsCredential: { kind: "none" },
    },
    "claude-envoy-channel-ses_slash",
    () => {},
    refused,
  ).then(
    () => "dialled",
    (error: unknown) => (error instanceof Error ? error.message : String(error)),
  )
  expect(failure).toBe(
    "could not connect to NATS at a server address that does not read as a host presenting no credential: getaddrinfo EAI_AGAIN <a server address not shown>",
  )
})

test("a stop ends the wait for the token and for the dial, and closes a connection made late", async () => {
  const STOPPING = "the channel server stopped before its NATS subscriptions reached the server"
  // The dial: pending until after the stop, then a connection the stop must close.
  const made = Promise.withResolvers<NatsConnection>()
  let closed = 0
  const late = {
    close: async () => {
      closed += 1
    },
  } as unknown as NatsConnection
  const stopping = new AbortController()
  const dialing = dialChannelNats(
    { natsUrls: ["nats://nats.test:4222"], natsCredential: { kind: "none" } },
    "claude-envoy-channel-ses_stop",
    () => {},
    // The stop lands once the dial has begun.
    () => {
      queueMicrotask(() => stopping.abort())
      return made.promise
    },
    stopping.signal,
  ).then(
    () => "dialled",
    (error: unknown) => (error instanceof Error ? error.message : String(error)),
  )
  expect(await dialing).toBe(STOPPING)
  made.resolve(late)
  await Bun.sleep(10)
  expect(closed).toBe(1)

  // The token: a trade that never answers is waited for no longer than the stop.
  const waiting = new AbortController()
  // A broker credential whose trade never answers; nothing else of it is reached before the dial.
  const broker = {
    ready: () => new Promise<void>(() => undefined),
  } as unknown as BrokerNatsCredential
  const trading = dialChannelNats(
    { natsUrls: ["nats://nats.test:4222"], natsCredential: { kind: "broker", broker } },
    "claude-envoy-channel-ses_trade",
    () => {},
    async () => {
      throw new Error("dialled before the token")
    },
    waiting.signal,
  ).then(
    () => "dialled",
    (error: unknown) => (error instanceof Error ? error.message : String(error)),
  )
  setTimeout(() => waiting.abort(), 20)
  expect(await trading).toBe(STOPPING)
})
