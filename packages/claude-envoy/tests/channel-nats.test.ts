// dialChannelNats: the channel server's NATS connection, opened with the session's credential and,
// where that is the broker token, kept renewed for the connection's life and stopped on request.
import { afterEach, expect, test } from "bun:test"
import { mkdtemp, writeFile } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { forgetBrokerClients } from "@legion/envoy-client/broker-credential"
import { brokerNatsCredential } from "@legion/envoy-client/nats-credential"
import type { Authenticator, ConnectionOptions, NatsConnection } from "nats"
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
    "could not connect to NATS at nats://127.0.0.1:4222, tls://nats.example:4443, a server address that does not parse presenting no credential: CONNECTION_REFUSED",
  )
  expect(failure).not.toContain("E2_SENTINEL")
})
