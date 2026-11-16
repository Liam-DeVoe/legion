// packages/envoy-client/src/__tests__/nats-credential.test.ts
//
// A session's NATS credential: which one `envoySettings` resolves (a seed, the session's broker
// token, or none, and none by default whatever the machine's broker route says), what the broker
// token's authenticator puts on a CONNECT, how the keeper moves the connection to a renewed token
// before the old one lapses, and, on a real nats-server behind the real auth service, that a
// connection whose token lapsed while the broker was away is admitted again once a token is live.

import { afterEach, beforeAll, describe, expect, test } from "bun:test";
import { createHash, createPublicKey, verify } from "node:crypto";
import { closeSync, mkdirSync, openSync, readFileSync, writeFileSync } from "node:fs";
import * as path from "node:path";
import { thumbprintOf } from "@legion/broker-client/test/sign-ins";
import { temporaryDirectories } from "@legion/test-fixtures/temporary-directories";
import type { Subprocess } from "bun";
import { connect, Events, type NatsConnection } from "nats";
import {
  awaitReady,
  buildGoBinaries,
  envoyStackEnabled,
  freePorts,
  stopProcess,
  useEnvoyStackDirectory,
} from "../../test/envoy-stack";
import { forgetBrokerClients } from "../broker-credential";
import { readEnvoyConfig } from "../envoy-config";
import { type EnvoyEnvironment, envoySettings } from "../envoy-settings";
import { natsConnectionOptions } from "../nats-connection";
import {
  brokerNatsCredential,
  followNatsCredential,
  keepNatsCredential,
  type NatsCredential,
} from "../nats-credential";

afterEach(forgetBrokerClients);

const servers: { stop: (closeActive?: boolean) => void }[] = [];
afterEach(() => {
  for (const server of servers.splice(0)) server.stop(true);
});

const tempDir = temporaryDirectories("nats-credential");

/** One token the stub broker minted: what it handed out, the session and key it was traded
 *  for, and when it lapses. */
interface Minted {
  readonly value: string;
  readonly session: string;
  readonly jkt: string;
  readonly expiresAt: number;
}

/**
 * A secrets broker: its token exchange mints a token good for `seconds`, numbering each one and
 * binding it to the key the trade's DPoP proof embeds, and its introspection route answers each
 * token it minted live until it lapses, as a session's (`ProjectScopePerson`). While `down` is
 * set the exchange answers 503, as a broker that is away.
 */
function stubBroker(seconds: number) {
  const minted: Minted[] = [];
  const state = { down: false, minted };
  const server = Bun.serve({
    port: 0,
    hostname: "127.0.0.1",
    fetch: async (request) => {
      const form = new URLSearchParams(await request.text());
      if (new URL(request.url).pathname === "/v1/oauth/introspect") {
        const held = minted.find((one) => one.value === form.get("token"));
        if (held === undefined || held.expiresAt <= Date.now())
          return Response.json({ active: false });
        return Response.json({
          active: true,
          session: held.session,
          person: "ada@example.com",
          client_id: "dispatch-token-exchange",
          project_scope: "person",
          jkt: held.jkt,
          exp: Math.floor(held.expiresAt / 1000),
        });
      }
      if (state.down) return new Response("the broker is away", { status: 503 });
      const header = JSON.parse(
        Buffer.from((request.headers.get("DPoP") ?? "").split(".")[0] ?? "", "base64url").toString()
      ) as { readonly jwk: JsonWebKey };
      const value = `session-access-${minted.length + 1}`;
      minted.push({
        value,
        session: form.get("session") ?? "",
        jkt: await thumbprintOf(header.jwk),
        expiresAt: Date.now() + seconds * 1000,
      });
      return Response.json({
        access_token: value,
        issued_token_type: "urn:ietf:params:oauth:token-type:access_token",
        token_type: "DPoP",
        expires_in: seconds,
      });
    },
  });
  servers.push(server);
  return { url: `http://127.0.0.1:${server.port}`, state };
}

/** A pod's projected token file. */
function projectedFile(): string {
  const file = path.join(tempDir(), "token");
  writeFileSync(file, "projected.service.account.token\n");
  return file;
}

/** The environment of a pod that has an identity to trade at `brokerUrl` and holds no NATS seed:
 *  what `dev-stack.sh`, the compose stacks and the load test set, whatever their hub runs. */
function podEnvironment(brokerUrl: string): EnvoyEnvironment {
  return {
    HOME: tempDir(),
    ENVOY_NATS_URL: "nats://nats.internal.example:4222",
    AGENT_SECRETS_URL: brokerUrl,
    AGENT_SECRETS_MACHINE_IDENTITY: "kubernetes",
    AGENT_SECRETS_MACHINE_TOKEN_FILE: projectedFile(),
  };
}

/** The credential `envoySettings` resolves for `env` and an envoy.json holding `file` (none
 *  when undefined), or the reason inbound messages are off. */
function resolved(
  env: EnvoyEnvironment,
  file?: unknown
): NatsCredential | { readonly off: string } {
  const config = path.join(tempDir(), "envoy.json");
  if (file !== undefined) writeFileSync(config, JSON.stringify(file));
  const settings = envoySettings(env, readEnvoyConfig(config, env, { cwd: tempDir() }), {
    session: "ses_a",
  });
  if (!settings.enabled) throw new Error(`Envoy is off: ${settings.error ?? settings.notice}`);
  return "off" in settings.inbound ? settings.inbound : settings.inbound.natsCredential;
}

describe("which credential a session connects to NATS with", () => {
  test("a broker route, no seed and no NATS_AUTH resolves to none", () => {
    // The machine's broker route says nothing about the hub: dev-stack.sh without --callout, the
    // compose stacks and the load test each set it while their hubs run no auth service, where a
    // broker token on the CONNECT would lose the session its inbound messages.
    expect(resolved(podEnvironment("http://broker.test"))).toEqual({ kind: "none" });
    expect(resolved({ ...podEnvironment("http://broker.test"), NATS_AUTH: "" })).toEqual({
      kind: "none",
    });
  });

  test("a seed and no NATS_AUTH resolves to the seed, as it does today", () => {
    expect(resolved({ ...podEnvironment("http://broker.test"), NATS_NKEY_SEED: "SUSEED" })).toEqual(
      { kind: "seed", seed: { value: "SUSEED", source: "NATS_NKEY_SEED" } }
    );
  });

  test("NATS_AUTH=broker with a broker route resolves to the session's broker token", () => {
    const credential = resolved({ ...podEnvironment("http://broker.test"), NATS_AUTH: "broker" });
    expect("kind" in credential && credential.kind).toBe("broker");
  });

  test("envoy.natsAuth broker resolves to it too, and NATS_AUTH overrides it", () => {
    const env = podEnvironment("http://broker.test");
    const fromFile = resolved(env, { envoy: { natsAuth: "broker" } });
    expect("kind" in fromFile && fromFile.kind).toBe("broker");
    expect(resolved({ ...env, NATS_AUTH: "none" }, { envoy: { natsAuth: "broker" } })).toEqual({
      kind: "none",
    });
  });

  test("NATS_AUTH=none resolves to none", () => {
    expect(resolved({ ...podEnvironment("http://broker.test"), NATS_AUTH: "none" })).toEqual({
      kind: "none",
    });
  });

  test("NATS_AUTH=broker with a key dir and no machine identity resolves inbound off", () => {
    // Every enrolled Legion pod's environment before it is given an identity to trade: a key
    // directory, a broker, and nothing this client can prove.
    const env: EnvoyEnvironment = {
      HOME: tempDir(),
      ENVOY_NATS_URL: "nats://nats.internal.example:4222",
      AGENT_SECRETS_URL: "http://broker.test",
      AGENT_SECRETS_KEY_DIR: tempDir(),
      NATS_AUTH: "broker",
    };
    expect(resolved(env)).toEqual({
      off: "NATS_AUTH is broker, but AGENT_SECRETS_MACHINE_IDENTITY names no identity this machine proves",
    });
  });

  test("NATS_AUTH=broker with no broker resolves inbound off naming the gap", () => {
    const env = {
      ...podEnvironment("http://broker.test"),
      AGENT_SECRETS_URL: "",
      NATS_AUTH: "broker",
    };
    expect(resolved(env)).toEqual({
      off: "NATS_AUTH is broker, but AGENT_SECRETS_URL names no secrets broker to trade it at",
    });
  });

  test("a NATS_AUTH beside a seed resolves inbound off naming both", () => {
    for (const value of ["broker", "none"]) {
      const env = {
        ...podEnvironment("http://broker.test"),
        NATS_AUTH: value,
        NATS_NKEY_SEED: "SUSEED",
      };
      expect(resolved(env)).toEqual({
        off: `NATS_AUTH is ${value}, and NATS_NKEY_SEED names an nkey seed: set one of the two`,
      });
    }
  });

  test("another value resolves inbound off naming the setting and its two values", () => {
    const env = podEnvironment("http://broker.test");
    expect(resolved({ ...env, NATS_AUTH: "seed" })).toEqual({
      off: 'NATS_AUTH is "seed", which is neither broker nor none',
    });
    const fromFile = resolved(env, { envoy: { natsAuth: "Broker" } });
    expect("off" in fromFile && fromFile.off).toMatch(
      /^envoy\.natsAuth in \/\S+\/envoy\.json is "Broker", which is neither broker nor none$/
    );
  });

  test("a caller that opens no NATS connection resolves no credential", () => {
    const env = { ...podEnvironment("http://broker.test"), NATS_AUTH: "broker" };
    const config = readEnvoyConfig(path.join(tempDir(), "envoy.json"), env, { cwd: tempDir() });
    const settings = envoySettings(env, config, { nats: false });
    expect(settings.enabled && settings.inbound).toEqual({
      off: "this session opens no NATS connection",
    });
  });
});

/**
 * The proof a CONNECT's signature field carries, verified with the key its own header embeds:
 * nats.js sends what the authenticator answered, and the auth service reads it base64url-encoded,
 * as nats.go sends one. Throws when the signature does not verify.
 */
function verifiedProof(sig: string): {
  readonly header: { readonly typ: string; readonly alg: string; readonly jwk: JsonWebKey };
  readonly claims: Record<string, unknown>;
} {
  const [header = "", claims = "", signature = ""] = Buffer.from(sig, "base64url")
    .toString("utf8")
    .split(".");
  const decoded = (part: string) => JSON.parse(Buffer.from(part, "base64url").toString("utf8"));
  const read = { header: decoded(header), claims: decoded(claims) };
  const key = createPublicKey({ key: read.header.jwk, format: "jwk" });
  const signed = Buffer.from(`${header}.${claims}`);
  const raw = Buffer.from(signature, "base64url");
  if (!verify("sha256", signed, { key, dsaEncoding: "ieee-p1363" }, raw)) {
    throw new Error("the connection proof's signature does not verify with its own key");
  }
  return read;
}

describe("the broker token's authenticator", () => {
  test("authenticator signs the server's nonce", async () => {
    const broker = stubBroker(600);
    const credential = brokerNatsCredential(podEnvironment(broker.url), "ses_a");
    // Before the token is in hand it presents nothing, and never throws: nats.js closes a
    // connection for good when its authenticator throws.
    expect(credential.authenticator("nonce-0")).toBeUndefined();
    await credential.ready();
    const auth = credential.authenticator("nonce-1") as { auth_token: string; sig: string };
    expect(Object.keys(auth).sort()).toEqual(["auth_token", "sig"]);
    const [minted] = broker.state.minted;
    expect(auth.auth_token).toBe(minted?.value ?? "no token minted");
    expect(minted?.session).toBe("ses_a");
    const proof = verifiedProof(auth.sig);
    expect(proof.header.typ).toBe("dpop+jwt");
    expect(proof.header.alg).toBe("ES256");
    expect(proof.claims.nonce).toBe("nonce-1");
    expect(proof.claims.htm).toBe("CONNECT");
    expect(proof.claims.htu).toBe("nats:connect");
    expect(proof.claims.ath).toBe(createHash("sha256").update(auth.auth_token).digest("base64url"));
    // Signed by the key the token was traded with, which the broker bound it to.
    expect(await thumbprintOf(proof.header.jwk)).toBe(minted?.jkt ?? "no key");
    // Another connection's nonce is another proof of the same token.
    const again = credential.authenticator("nonce-2") as { auth_token: string; sig: string };
    expect(again.auth_token).toBe(auth.auth_token);
    expect(verifiedProof(again.sig).claims.nonce).toBe("nonce-2");
    // A CONNECT with no nonce cannot carry a proof, so it carries no credential.
    expect(credential.authenticator(undefined)).toBeUndefined();
    // When the token in hand lapses: the answer's expires_in, read when it arrived.
    const lapses = credential.expiresAt().getTime();
    expect(Math.abs(lapses - (minted?.expiresAt ?? 0))).toBeLessThan(2_000);
  });
});

/** A connection as the keeper sees it: each reconnect is a new CONNECT, which asks the
 *  credential's authenticator again, as nats.js does. */
function fakeConnection(onConnect: () => void) {
  let closed = false;
  const ended = Promise.withResolvers<undefined>();
  const reconnects: number[] = [];
  return {
    reconnects,
    connection: {
      reconnect: async () => {
        reconnects.push(Date.now());
        onConnect();
      },
      isClosed: () => closed,
      closed: () => ended.promise,
    },
    close: () => {
      closed = true;
      ended.resolve(undefined);
    },
  };
}

// These run on the wall clock, with two-second tokens: the keeper's timer and the token client's
// expiry both read real time, and each renewal is a real HTTP trade at the stub broker, so a fake
// clock would drive one half of the exchange and not the other.
describe("keepNatsCredential", () => {
  test("keepNatsCredential renews and reconnects before expiry", async () => {
    const broker = stubBroker(2);
    const credential = brokerNatsCredential(podEnvironment(broker.url), "ses_a");
    await credential.ready();
    const first = credential.authenticator("nonce-1") as { auth_token: string };
    const lapses = credential.expiresAt().getTime();
    const fake = fakeConnection(() => credential.authenticator("nonce-again"));
    const warnings: string[] = [];
    const stop = keepNatsCredential(fake.connection, credential, (text) => warnings.push(text));
    try {
      const deadline = Date.now() + 5_000;
      while (fake.reconnects.length === 0 && Date.now() < deadline) await Bun.sleep(50);
      expect(fake.reconnects.length).toBeGreaterThan(0);
      // Moved to the next token before the one the connection presented lapsed.
      expect(fake.reconnects[0] ?? Number.POSITIVE_INFINITY).toBeLessThan(lapses);
      const next = credential.authenticator("nonce-3") as { auth_token: string };
      expect(next.auth_token).not.toBe(first.auth_token);
      expect(broker.state.minted.length).toBeGreaterThanOrEqual(2);
      expect(warnings).toEqual([]);
    } finally {
      stop();
    }
    // Stopped, it trades and reconnects no more.
    const reconnected = fake.reconnects.length;
    const traded = broker.state.minted.length;
    await Bun.sleep(1_500);
    expect(fake.reconnects.length).toBe(reconnected);
    expect(broker.state.minted.length).toBe(traded);
  }, 15_000);

  test("a closed connection ends the keeper", async () => {
    const broker = stubBroker(2);
    const credential = brokerNatsCredential(podEnvironment(broker.url), "ses_a");
    await credential.ready();
    credential.authenticator("nonce-1");
    const fake = fakeConnection(() => credential.authenticator("nonce-again"));
    keepNatsCredential(fake.connection, credential, () => {});
    fake.close();
    await Bun.sleep(1_500);
    expect(fake.reconnects).toEqual([]);
    expect(broker.state.minted.length).toBe(1);
  }, 15_000);
});

const stackDirectory = useEnvoyStackDirectory();

/** A natsd behind the real auth service, which asks `brokerUrl`'s introspection route about each
 *  session token; `refusals` counts the connections the service has refused so far. */
interface CalloutHub {
  readonly natsUrl: string;
  refusals(): number;
  stop(): Promise<void>;
}

/** Starts a {@link CalloutHub} in its own directory under the stack directory, from the binaries
 *  the describe's `beforeAll` built. */
async function startCalloutHub(name: string, brokerUrl: string): Promise<CalloutHub> {
  const work = path.join(stackDirectory, name);
  const bin = path.join(stackDirectory, "bin");
  const keys = path.join(work, "callout-keys");
  mkdirSync(keys, { recursive: true });
  const [natsPort, monitorPort] = await freePorts(2);
  const { PATH = "" } = process.env;
  const budget = { timeoutMs: 60_000, tailLines: 10 };
  const processes: Subprocess[] = [];
  const stop = async () => {
    for (const child of processes.reverse()) await stopProcess(child, "SIGTERM");
  };
  try {
    const natsLog = path.join(work, "natsd.log");
    const natsOut = openSync(natsLog, "a");
    const natsd = Bun.spawn(
      [
        path.join(bin, "natsd"),
        ...["-p", String(natsPort), "-m", String(monitorPort)],
        ...["-store_dir", path.join(work, "nats-store"), "-callout-keys", keys],
      ],
      { env: { PATH, HOME: work }, stdout: natsOut, stderr: natsOut }
    );
    closeSync(natsOut);
    processes.push(natsd);
    await awaitReady("natsd", natsd, natsLog, budget, async () => {
      const answer = await fetch(`http://127.0.0.1:${monitorPort}/healthz`);
      return answer.ok;
    });
    const natsUrl = `nats://127.0.0.1:${natsPort}`;
    const calloutLog = path.join(work, "calloutd.log");
    const calloutOut = openSync(calloutLog, "a");
    const calloutd = Bun.spawn(
      [
        path.join(bin, "calloutd"),
        ...["-keys", keys, "-nats", natsUrl],
        ...["-introspect", `${brokerUrl}/v1/oauth/introspect`],
      ],
      { env: { PATH, HOME: work }, stdout: calloutOut, stderr: calloutOut }
    );
    closeSync(calloutOut);
    processes.push(calloutd);
    await awaitReady("calloutd", calloutd, calloutLog, budget, async () =>
      readFileSync(calloutLog, "utf8").includes("calloutd: ready")
    );
    const refusals = () =>
      readFileSync(calloutLog, "utf8")
        .split("\n")
        .filter((line) => line.includes("refused a NATS connection")).length;
    return { natsUrl, refusals, stop };
  } catch (error) {
    await stop();
    throw error;
  }
}

describe.skipIf(!envoyStackEnabled)(
  "a session's connection to a hub behind the auth service",
  () => {
    beforeAll(async () => {
      await buildGoBinaries({
        bin: path.join(stackDirectory, "bin"),
        packages: ["./internal/testnats/cmd/natsd", "./internal/testnats/cmd/calloutd"],
        stack: "auth callout",
        binaries: "natsd and calloutd",
      });
    }, 300_000);

    test("reconnects after two refusals once a token is live again", async () => {
      // Tokens live three seconds, so the user the service admits a connection as lapses inside
      // the test, as a 15-minute one does inside a session; the server expires it on its own
      // clock, so this runs on the wall clock.
      const broker = stubBroker(3);
      const hub = await startCalloutHub("reconnect", broker.url);
      let connection: NatsConnection | undefined;
      let stopKeeping = () => {};
      try {
        const credential = brokerNatsCredential(podEnvironment(broker.url), "ses_e2e");
        await credential.ready();
        const opened = await connect(
          natsConnectionOptions({
            servers: [hub.natsUrl],
            name: "nats-credential-e2e",
            credential: { kind: "broker", broker: credential },
          })
        );
        connection = opened;
        const reconnected: number[] = [];
        void (async () => {
          for await (const status of opened.status()) {
            if (status.type === Events.Reconnect) reconnected.push(Date.now());
          }
        })();
        // Its own delivery channel is what the service grants a session.
        opened.subscribe("notifications.envoy.deliver.ses_e2e");
        await opened.flush();
        const warnings: string[] = [];
        stopKeeping = keepNatsCredential(opened, credential, (text) => warnings.push(text));

        // The broker goes away: the token lapses with no other to take its place, the server
        // drops the user it admitted, and each reconnect is refused.
        broker.state.down = true;
        const deadline = Date.now() + 20_000;
        while (hub.refusals() < 2 && Date.now() < deadline) await Bun.sleep(100);
        expect(hub.refusals()).toBeGreaterThanOrEqual(2);
        expect(opened.isClosed()).toBe(false);
        expect(warnings.length).toBeGreaterThan(0);
        expect(warnings.join("\n")).not.toContain("session-access-");

        // The broker is back: a token is traded and the connection is admitted again.
        const back = Date.now();
        broker.state.down = false;
        while (!reconnected.some((at) => at > back) && Date.now() < back + 20_000)
          await Bun.sleep(100);
        expect(reconnected.some((at) => at > back)).toBe(true);
        expect(opened.isClosed()).toBe(false);
        expect(await opened.rtt()).toBeGreaterThanOrEqual(0);
      } finally {
        stopKeeping();
        await connection?.close().catch(() => {});
        await hub.stop();
      }
    }, 120_000);

    test("a session switch subscribes the new session only once the connection is admitted as it", async () => {
      const broker = stubBroker(600);
      const hub = await startCalloutHub("switch", broker.url);
      let connection: NatsConnection | undefined;
      try {
        // The session name a plugin hands its credential: read at each trade, so a switch moves
        // the credential to the new session's token.
        let session = "ses_before";
        const credential = brokerNatsCredential(podEnvironment(broker.url), () => session);
        await credential.ready();
        const opened = await connect(
          natsConnectionOptions({
            servers: [hub.natsUrl],
            name: "nats-credential-switch",
            credential: { kind: "broker", broker: credential },
          })
        );
        connection = opened;
        const before = opened.subscribe("notifications.envoy.deliver.ses_before");
        await opened.flush();
        expect(before.isClosed()).toBe(false);

        // The host switches: the old session's subject goes, and the new one's opens once the
        // connection presents the new session's token.
        before.unsubscribe();
        session = "ses_after";
        await followNatsCredential(opened, credential);
        // A subscribe the server refuses closes the subscription (nats.js's permission error).
        const after = opened.subscribe("notifications.envoy.deliver.ses_after");
        await opened.flush();
        expect(after.isClosed()).toBe(false);
        expect(broker.state.minted.map((one) => one.session)).toEqual(["ses_before", "ses_after"]);
        // Nothing to move when the session has not changed.
        await followNatsCredential(opened, credential);
        expect(broker.state.minted.length).toBe(2);
      } finally {
        await connection?.close().catch(() => {});
        await hub.stop();
      }
    }, 120_000);
  }
);
