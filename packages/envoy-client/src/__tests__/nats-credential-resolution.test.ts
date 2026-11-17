// packages/envoy-client/src/__tests__/nats-credential-resolution.test.ts
//
// Which NATS credential `envoySettings` resolves for a session: a seed, the session's broker
// token, or none, and none by default whatever the machine's broker route says; which settings
// choose the broker token, which servers it goes to, and the setting a refusal names; and the
// reason inbound messages are off where the settings conflict or leave a gap.

import { describe, expect, test } from "bun:test";
import { writeFileSync } from "node:fs";
import * as path from "node:path";
import { readEnvoyConfig } from "../envoy-config";
import { type EnvoyEnvironment, envoySettings } from "../envoy-settings";
import type { NatsCredential } from "../nats-credential";
import { natsBrokerStandIns } from "./broker-stand-ins";

const { podEnvironment, tempDir } = natsBrokerStandIns("nats-credential-resolution");

/** The credential `envoySettings` resolves for `env` and an envoy.json holding `file` (none
 *  when undefined), or the reason inbound messages are off. */
async function resolved(
  env: EnvoyEnvironment,
  file?: unknown
): Promise<NatsCredential | { readonly off: string }> {
  const config = path.join(tempDir(), "envoy.json");
  if (file !== undefined) writeFileSync(config, JSON.stringify(file));
  const settings = await envoySettings(env, readEnvoyConfig(config, env, { cwd: tempDir() }), {
    session: "ses_a",
  });
  if (!settings.enabled) throw new Error(`Envoy is off: ${settings.error ?? settings.notice}`);
  return "off" in settings.inbound ? settings.inbound : settings.inbound.natsCredential;
}

describe("which credential a session connects to NATS with", () => {
  test("a broker route, no seed and no NATS_AUTH resolves to none", async () => {
    // The machine's broker route says nothing about the hub: dev-stack.sh without --callout, for
    // one, exports a broker route beside a hub that runs no auth service, where a broker token on
    // the CONNECT would lose the session its inbound messages.
    expect(await resolved(podEnvironment("http://broker.test"))).toEqual({ kind: "none" });
    expect(await resolved({ ...podEnvironment("http://broker.test"), NATS_AUTH: "" })).toEqual({
      kind: "none",
    });
  });

  test("a seed and no NATS_AUTH resolves to the seed, as it does today", async () => {
    expect(
      await resolved({ ...podEnvironment("http://broker.test"), NATS_NKEY_SEED: "SUSEED" })
    ).toEqual({ kind: "seed", seed: { value: "SUSEED", source: "NATS_NKEY_SEED" } });
  });

  test("NATS_AUTH=broker with a broker route resolves to the session's broker token", async () => {
    const credential = await resolved({
      ...podEnvironment("http://broker.test"),
      NATS_AUTH: "broker",
    });
    expect("kind" in credential && credential.kind).toBe("broker");
  });

  test("envoy.natsAuth broker resolves to it too, and NATS_AUTH overrides it", async () => {
    const env = podEnvironment("http://broker.test");
    const file = { envoy: { natsUrls: ["nats://nats.internal.example:4222"], natsAuth: "broker" } };
    const fromFile = await resolved(env, file);
    expect("kind" in fromFile && fromFile.kind).toBe("broker");
    expect(await resolved({ ...env, NATS_AUTH: "none" }, file)).toEqual({ kind: "none" });
  });

  test("a hub with no auth service is refused naming the setting that chose the broker token", async () => {
    // The remedy is the one that undoes the choice: unsetting NATS_AUTH does nothing for a
    // session whose envoy.json chose the broker token.
    const env = podEnvironment("http://broker.test");
    const fromFile = await resolved(env, {
      envoy: { natsUrls: ["nats://nats.internal.example:4222"], natsAuth: "broker" },
    });
    if (!("kind" in fromFile) || fromFile.kind !== "broker")
      throw new Error("no broker credential");
    expect(() => fromFile.broker.authenticator(undefined)).toThrow(
      /^envoy\.natsAuth in \/\S+\/envoy\.json is broker, but the NATS server sent no nonce to sign: it runs no auth service, so take natsAuth out of that file for this hub$/
    );
    const fromVariable = await resolved({ ...env, NATS_AUTH: "broker" });
    if (!("kind" in fromVariable) || fromVariable.kind !== "broker")
      throw new Error("no broker credential");
    expect(() => fromVariable.broker.authenticator(undefined)).toThrow(
      "NATS_AUTH is broker, but the NATS server sent no nonce to sign: it runs no auth service, so unset NATS_AUTH for this hub"
    );
  });

  test("envoy.natsAuth broker goes only to the servers written beside it", async () => {
    // As a file's seed does: a session's token and the proof it signs would otherwise go to any
    // server ENVOY_NATS_URL names, which could relay that proof to the hub it was meant for.
    const env = podEnvironment("http://broker.test");
    for (const natsUrls of [["nats://nats.elsewhere.example:4222"], undefined]) {
      const fromFile = await resolved(env, { envoy: { natsUrls, natsAuth: "broker" } });
      expect("off" in fromFile && fromFile.off).toMatch(
        /^ENVOY_NATS_URL names other servers than \/\S+\/envoy\.json's envoy\.natsUrls; set NATS_AUTH=broker for them$/
      );
    }
    // The variable is the environment's own choice, for whatever servers it names.
    const fromVariable = await resolved(
      { ...env, NATS_AUTH: "broker" },
      { envoy: { natsUrls: ["nats://nats.elsewhere.example:4222"] } }
    );
    expect("kind" in fromVariable && fromVariable.kind).toBe("broker");
  });

  test("NATS_AUTH=none resolves to none", async () => {
    expect(await resolved({ ...podEnvironment("http://broker.test"), NATS_AUTH: "none" })).toEqual({
      kind: "none",
    });
  });

  test("NATS_AUTH=broker with a key dir and no machine identity resolves inbound off", async () => {
    // Every enrolled Legion pod's environment before it is given an identity to trade: a key
    // directory, a broker, and nothing this client can prove.
    const env: EnvoyEnvironment = {
      HOME: tempDir(),
      ENVOY_NATS_URL: "nats://nats.internal.example:4222",
      AGENT_SECRETS_URL: "http://broker.test",
      AGENT_SECRETS_KEY_DIR: tempDir(),
      NATS_AUTH: "broker",
    };
    expect(await resolved(env)).toEqual({
      off: "NATS_AUTH is broker, but AGENT_SECRETS_MACHINE_IDENTITY names no identity this machine proves",
    });
  });

  test("NATS_AUTH=broker with no broker resolves inbound off naming the gap", async () => {
    const env = {
      ...podEnvironment("http://broker.test"),
      AGENT_SECRETS_URL: "",
      NATS_AUTH: "broker",
    };
    expect(await resolved(env)).toEqual({
      off: "NATS_AUTH is broker, but AGENT_SECRETS_URL names no secrets broker to trade it at",
    });
  });

  test("NATS_AUTH=broker beside a seed resolves inbound off naming both", async () => {
    const env = {
      ...podEnvironment("http://broker.test"),
      NATS_AUTH: "broker",
      NATS_NKEY_SEED: "SUSEED",
    };
    expect(await resolved(env)).toEqual({
      off: "NATS_AUTH is broker, and NATS_NKEY_SEED names an nkey seed: set one of the two",
    });
  });

  test("NATS_AUTH=none beside a seed resolves to the seed", async () => {
    // A seed is the credential wherever one is set (the plan's table); only `broker` beside it
    // names two credentials at once.
    const env = {
      ...podEnvironment("http://broker.test"),
      NATS_AUTH: "none",
      NATS_NKEY_SEED: "SUSEED",
    };
    expect(await resolved(env)).toEqual({
      kind: "seed",
      seed: { value: "SUSEED", source: "NATS_NKEY_SEED" },
    });
  });

  test("another value resolves inbound off naming the setting and its two values, not the value", async () => {
    // A value is never echoed: a seed or token pasted into the wrong setting is a credential.
    const env = podEnvironment("http://broker.test");
    expect(await resolved({ ...env, NATS_AUTH: "SUAPASTEDSEED" })).toEqual({
      off: "NATS_AUTH is neither broker nor none",
    });
    const fromFile = await resolved(env, { envoy: { natsAuth: "Broker" } });
    expect("off" in fromFile && fromFile.off).toMatch(
      /^envoy\.natsAuth in \/\S+\/envoy\.json is neither broker nor none$/
    );
  });

  test("a caller that opens no NATS connection resolves no credential", async () => {
    const env = { ...podEnvironment("http://broker.test"), NATS_AUTH: "broker" };
    const config = readEnvoyConfig(path.join(tempDir(), "envoy.json"), env, { cwd: tempDir() });
    const settings = await envoySettings(env, config, { nats: false });
    expect(settings.enabled && settings.inbound).toEqual({
      off: "this session opens no NATS connection",
    });
  });
});
