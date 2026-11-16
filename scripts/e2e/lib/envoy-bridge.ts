// scripts/e2e/lib/envoy-bridge.ts: relay the sandbox repository's GitHub subjects from production
// NATS (read-only) into a live proof's own NATS (stage3-devbox-workflow.sh starts it). It
// subscribes upstream to exactly `notifications.github.<owner>.<repo>.>`, a dot in either name
// written `_` as Envoy publishes it, and republishes each message unchanged downstream; it never
// publishes upstream, and it never touches Dispatch issue subjects — the instance's scratch
// Dispatch server publishes its own, and two rigs on one issue stream admitted each other's issues
// (docs/solutions/legion).
import { connect, type NatsConnection, type Subscription } from "nats";
import { EnvelopeSchema } from "../../../packages/contracts/src/envelope";
import { githubSubject } from "../../../packages/contracts/src/subject";
import { natsAuthOptions } from "../../../packages/envoy-client/src/nats-auth";
import {
  NATS_ADDRESS_NOT_SHOWN,
  natsDialedHost,
  natsServerForLog,
  natsUnnamedParts,
  replaceEachOnce,
} from "../../../packages/envoy-client/src/nats-connection";
import { operatorNatsSeed } from "./operator-nats-seed";

const requiredEnvelopeFields = [
  "event_id",
  "source",
  "source_event_id",
  "topic",
  "dedupe_key",
  "issued_at",
  "payload_summary",
  "trace_id",
] as const;

const envelopeFields: Record<string, true> = {
  event_id: true,
  source: true,
  source_event_id: true,
  source_session: true,
  topic: true,
  dedupe_key: true,
  issued_at: true,
  expires_at: true,
  payload_summary: true,
  payload: true,
  payload_ref: true,
  trace_id: true,
  sender: true,
  in_reply_to: true,
  supersedes: true,
  urgency: true,
  expects_reply: true,
};

export interface BridgeConfig {
  repository: string;
  subjects: string[];
  upstreamUrl: string;
  downstreamUrl: string;
}

export type EnvelopeValidation =
  | { ok: true; shape: "current" }
  | { ok: false; missing: string[]; extra: string[]; errors: string[] };

export function bridgeConfigFromEnvironment(
  environment: Record<string, string | undefined>
): BridgeConfig {
  const repository = environment.SMOKE_REPO?.trim() ?? "";
  if (!/^[A-Za-z0-9][A-Za-z0-9.-]*\/[A-Za-z0-9][A-Za-z0-9._-]*$/.test(repository)) {
    throw new Error("SMOKE_REPO must be a literal <owner>/<repo> repository");
  }
  const downstreamUrl = environment.SMOKE_RIG_NATS?.trim() ?? "";
  if (!downstreamUrl) throw new Error("SMOKE_RIG_NATS is required");
  const upstreamUrl = environment.SMOKE_UPSTREAM_NATS?.trim() ?? "";
  if (!upstreamUrl) throw new Error("SMOKE_UPSTREAM_NATS is required");
  if (!upstreamNatsUrl.test(upstreamUrl) || !dialsCheckedHost(upstreamUrl)) {
    throw new Error(
      "SMOKE_UPSTREAM_NATS is not one NATS URL naming a fully-qualified host: a bare alias resolves through whatever search domain the box has; name the production Envoy NATS as the operator's envoy.json does (envoy.natsUrls, nats://<host>.<tailnet>.ts.net:4222)"
    );
  }
  const [owner, name] = repository.split("/");
  return {
    repository,
    subjects: [githubSubject(owner, name, ">")],
    upstreamUrl,
    downstreamUrl,
  };
}

// One NATS server URL: an optional scheme and user info, a host with a dot, an optional port, and
// nothing after it. The client dials whatever follows the last "://", so a path or a query could
// name a host other than the one checked here. Stage 3 holds the same pattern.
const upstreamNatsUrl =
  /^(?:[A-Za-z][A-Za-z0-9+.-]*:\/\/)?(?:([^@/?#,\s]+)@)?([A-Za-z0-9_-]+(?:\.[A-Za-z0-9_-]+)+)(?::\d+)?\/?$/;

// Whether the host nats.js dials (natsDialedHost, its own hostPort) is the host the pattern
// checked. nats.js reads the URL as an http: one, which ends the host at a `\` in the user info,
// so `nats://tok\x@<fqdn>` passes the pattern and dials the bare name `tok`.
function dialsCheckedHost(upstreamUrl: string): boolean {
  const [, , host] = upstreamNatsUrl.exec(upstreamUrl) ?? [];
  return host !== undefined && natsDialedHost(upstreamUrl) === host.toLowerCase();
}

// The operator's upstream names production infrastructure, so a client error that quotes the
// value, its user info, its host or the host nats.js dials is printed with the variable's name in
// their place.
function upstreamReplacements(upstreamUrl: string): (readonly [string, string])[] {
  const [, userInfo, host] = upstreamNatsUrl.exec(upstreamUrl) ?? [];
  return [upstreamUrl, userInfo, host, natsDialedHost(upstreamUrl)]
    .filter((value): value is string => value !== undefined)
    .map((value) => [value, "SMOKE_UPSTREAM_NATS"] as const);
}

// One pass over the text as the client wrote it (replaceEachOnce): the variable's name a match
// puts in is never searched again.
export function redactUpstream(text: string, upstreamUrl: string): string {
  return replaceEachOnce(text, upstreamReplacements(upstreamUrl));
}

/** The line the bridge prints once both connections answer. The rig's server is named as a log
 *  names a NATS server (natsServerForLog): SMOKE_RIG_NATS may hold a credential too. */
export function bridgeReadyLine(config: BridgeConfig): string {
  return `BRIDGE READY subjects=${config.subjects.join(",")} upstream=SMOKE_UPSTREAM_NATS downstream=${natsServerForLog(config.downstreamUrl)}`;
}

/** The line the bridge prints when it stops on `error`: the client's own text, with neither
 *  connection's credential in it (natsUnnamedParts for the rig's) and the upstream's host not
 *  named. Both in one pass: replaced one after the other, a rig credential that is a label of
 *  the upstream's host broke the whole-host match, and the rest of the host was printed. A part
 *  of both is the upstream's. */
export function bridgeFailureLine(error: unknown, config: BridgeConfig): string {
  const message = error instanceof Error ? error.message : String(error);
  const rig = natsUnnamedParts([config.downstreamUrl]).map(
    (part) => [part, NATS_ADDRESS_NOT_SHOWN] as const
  );
  return `BRIDGE UNHEALTHY ${replaceEachOnce(message, [...rig, ...upstreamReplacements(config.upstreamUrl)])}`;
}

export function envelopeValidation(data: string | Uint8Array): EnvelopeValidation {
  const text = typeof data === "string" ? data : new TextDecoder().decode(data);
  let candidate: unknown;
  try {
    candidate = JSON.parse(text);
  } catch (error) {
    return {
      ok: false,
      missing: [],
      extra: [],
      errors: [`invalid JSON: ${error instanceof Error ? error.message : String(error)}`],
    };
  }
  if (typeof candidate !== "object" || candidate === null || Array.isArray(candidate)) {
    return {
      ok: false,
      missing: [...requiredEnvelopeFields],
      extra: [],
      errors: ["envelope must be a JSON object"],
    };
  }
  const envelope = candidate as Record<string, unknown>;
  const missing = requiredEnvelopeFields.filter((field) => !(field in envelope));
  const extra = Object.keys(envelope)
    .filter((field) => !(field in envelopeFields))
    .sort();
  const parsed = EnvelopeSchema.safeParse(envelope);
  const errors = parsed.success
    ? []
    : parsed.error.issues.map((issue) => `${issue.path.join(".") || "envelope"}: ${issue.message}`);
  if (missing.length === 0 && extra.length === 0 && errors.length === 0) {
    return { ok: true, shape: "current" };
  }
  return { ok: false, missing, extra, errors };
}

function validationFailure(validation: Exclude<EnvelopeValidation, { ok: true }>): string {
  return `missing=[${validation.missing.join(",")}] extra=[${validation.extra.join(",")}] errors=[${validation.errors.join("; ")}]`;
}

async function forwardMessages(
  subscription: Subscription,
  subject: string,
  downstream: NatsConnection
): Promise<void> {
  const subjectPrefix = subject.slice(0, -1);
  let firstMessage = true;
  for await (const message of subscription) {
    if (!message.subject.startsWith(subjectPrefix)) {
      throw new Error(`refusing out-of-scope upstream subject ${message.subject}`);
    }
    if (firstMessage) {
      firstMessage = false;
      const validation = envelopeValidation(message.data);
      if (!validation.ok) {
        const reason = validationFailure(validation);
        console.error(`BRIDGE UNHEALTHY first-envelope subject=${message.subject} ${reason}`);
        throw new Error(`first bridged envelope is incompatible: ${reason}`);
      }
      console.log(
        `BRIDGE VALIDATION shape=${validation.shape} subject=${message.subject} bytes=${message.data.byteLength}`
      );
    }
    downstream.publishMessage(message);
    await downstream.flush();
    console.log(`BRIDGED subject=${message.subject} bytes=${message.data.byteLength}`);
  }
}

export async function runBridge(config: BridgeConfig): Promise<void> {
  // The production server's nkey user, when it requires one: the seed a command run from the
  // operator's shell sends to that server (operator-nats-seed.ts).
  const seed = operatorNatsSeed(
    config.upstreamUrl,
    "SMOKE_UPSTREAM_NATS",
    process.env,
    process.cwd()
  );
  const upstream = await connect({
    servers: config.upstreamUrl,
    name: `legion-e2e-bridge-upstream-${config.repository}`,
    ...natsAuthOptions(seed === undefined ? { kind: "none" } : { kind: "seed", seed }),
    reconnect: true,
    maxReconnectAttempts: -1,
    reconnectTimeWait: 2_000,
  });
  let downstream: NatsConnection | undefined;
  try {
    downstream = await connect({
      servers: config.downstreamUrl,
      name: `legion-e2e-bridge-downstream-${config.repository}`,
      reconnect: true,
      maxReconnectAttempts: -1,
      reconnectTimeWait: 2_000,
    });
    const activeDownstream = downstream;
    const subscriptions = config.subjects.map((subject) => upstream.subscribe(subject));
    const stop = () => {
      for (const subscription of subscriptions) subscription.unsubscribe();
    };
    process.once("SIGINT", stop);
    process.once("SIGTERM", stop);
    try {
      await upstream.flush();
      await downstream.flush();
      console.log(bridgeReadyLine(config));
      await Promise.all(
        subscriptions.map((subscription, index) =>
          forwardMessages(subscription, config.subjects[index] as string, activeDownstream)
        )
      );
    } finally {
      process.off("SIGINT", stop);
      process.off("SIGTERM", stop);
    }
  } finally {
    await Promise.allSettled([upstream.drain(), ...(downstream ? [downstream.drain()] : [])]);
  }
}

if (import.meta.main) {
  const config = bridgeConfigFromEnvironment(process.env);
  await runBridge(config).catch((error) => {
    console.error(bridgeFailureLine(error, config));
    process.exitCode = 1;
  });
}
