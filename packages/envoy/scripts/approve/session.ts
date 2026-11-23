// packages/envoy/scripts/approve/session.ts
//
// The agent session of scripts/approve-check.sh, on the stack `dev-stack.sh --approve` stands up:
// a session on a box where a person signed in (`agent-secrets login`), whose Dispatch credential is
// the broker credential the TypeScript client builds from this process's environment. Each command
// is one step of the check and prints one JSON line of what it saw, never a token or a placeholder.
//
// Dispatch serves under https://<the approve host> (DISPATCH_URL) and listens on loopback
// (APPROVE_CHECK_DISPATCH_LOCAL). A request through Cipher's proxy goes to the public URL, which the
// proxy terminates and sends on to Dispatch's own address; a direct request is signed for the
// public URL, as every request of a box is in production, and sent to the loopback address, which
// stands in for the name resolving there.
//
//   setup                         as the person: a project, an issue and its spec, saved to the state file
//   approve                       the spec's version reviewed approved through the proxy (reviewArtifact)
//   approve-then-revoked          the same review twice on one approve route, the sign-in revoked between
//   box <method> <path> [json]    a request with the box's own Dispatch token, direct
//   proxy <method> <path> [json]  a request with the approve placeholder, through the proxy
//   reviews                       the spec's reviews, read with the box's token
//   comments                      the issue's comments, read with the box's token
//   agent-tokens                  the person's agent tokens, read with the box's token
//   scan <broker log> <path>...   every candidate credential and private key in the paths and this
//                                 process's environment, against the hashes and bindings the broker logged

import { createHash, createPrivateKey } from "node:crypto";
import { readdirSync, readFileSync, type Stats, statSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { newDpopKey } from "../../../cipher-client/src/dpop";
import type { ApproveRoute } from "../../../envoy-client/src/bearer";
import { brokerCredential } from "../../../envoy-client/src/broker-credential";
import { DispatchClient } from "../../../envoy-client/src/dispatch-http";

/** A JWK as the scan reads one: its members by name. */
type Jwk = Record<string, unknown>;

/** The RFC 7638 thumbprint of an EC key's public half, as the broker logs a binding's: the
 *  SHA-256, base64url, of its required members (crv, kty, x, y) in that order with no whitespace.
 *  A key of another type has none the broker could have logged. */
function ecThumbprint(jwk: Jwk): string {
  if (jwk["kty"] !== "EC") return "";
  const members = { crv: jwk["crv"], kty: jwk["kty"], x: jwk["x"], y: jwk["y"] };
  return createHash("sha256").update(JSON.stringify(members)).digest("base64url");
}

const env = process.env as Record<string, string | undefined>;

function setting(name: string): string {
  const value = (env[name] ?? "").trim();
  if (value === "") fail(`${name} is unset`);
  return value;
}

function fail(message: string): never {
  process.stderr.write(`approve-session: ${message}\n`);
  process.exit(2);
}

function say(answer: Record<string, unknown>): void {
  process.stdout.write(`${JSON.stringify(answer)}\n`);
}

const PUBLIC = setting("DISPATCH_URL").replace(/\/+$/, "");
const LOCAL = setting("APPROVE_CHECK_DISPATCH_LOCAL").replace(/\/+$/, "");
const STATE = setting("APPROVE_CHECK_STATE");

/** A fetch that sends a request of the public URL to Dispatch's loopback address, and one through
 *  a proxy as it is: the proxy itself reaches Dispatch's own address. */
const localFetch = Object.assign(
  async (input: Parameters<typeof fetch>[0], init?: RequestInit & { proxy?: string }) => {
    const url = String(input);
    if (init?.proxy !== undefined || !url.startsWith(PUBLIC)) return await fetch(input, init);
    return await fetch(LOCAL + url.slice(PUBLIC.length), init);
  },
  { preconnect: () => {} }
) as typeof fetch;

const credential = brokerCredential(env);

/** The state `setup` wrote: the issue, its spec's artifact and the version reviewed. */
interface State {
  readonly issue: string;
  readonly artifact: string;
  readonly version: number;
}

function state(): State {
  return JSON.parse(readFileSync(STATE, "utf8")) as State;
}

/** What one answer was: its status, Dispatch's or the proxy's code, and the proxy's own headers. */
async function described(answer: Response): Promise<Record<string, unknown>> {
  const text = await answer.text();
  let body: unknown;
  try {
    body = JSON.parse(text);
  } catch {
    body = undefined;
  }
  const record = (typeof body === "object" && body !== null ? body : {}) as Record<string, unknown>;
  return {
    status: answer.status,
    code: record["code"] ?? null,
    proxy_code: answer.headers.get("Cipher-Proxy-Code"),
    forwarded: answer.headers.get("Cipher-Proxy-Forwarded"),
    // Dispatch's error, or the proxy's own refusal, which is plain text naming its rule.
    error: (typeof record["error"] === "string" ? String(record["error"]) : text.trim()).slice(
      0,
      300
    ),
    body: record,
  };
}

/** A request with the box's own Dispatch token, signed for the public URL, sent direct. */
async function box(method: string, path: string, body?: unknown): Promise<Response> {
  const url = PUBLIC + path;
  const headers = await credential.headers(method, url);
  return await localFetch(url, {
    method,
    headers: {
      ...headers,
      Accept: "application/json",
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
    },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
}

async function approveRoute(): Promise<ApproveRoute> {
  const route = await credential.approveRoute?.();
  if (route === undefined)
    fail("the credential has no approve route: no person is signed in on this box");
  return route;
}

/** A request with the approve placeholder, through the proxy the route names. */
async function proxied(
  route: ApproveRoute,
  method: string,
  path: string,
  body?: unknown
): Promise<Response> {
  const proxy = new URL(route.proxyUrl);
  proxy.username = "agent";
  proxy.password = route.placeholder;
  const init: RequestInit & { proxy: string; tls: { ca: string } } = {
    method,
    headers: {
      Accept: "application/json",
      Authorization: `DPoP ${route.placeholder}`,
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
    },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    proxy: proxy.toString(),
    tls: { ca: route.caCertificate },
  };
  return await fetch(PUBLIC + path, init);
}

async function setup(): Promise<void> {
  const project = await described(
    await box("POST", "/api/v1/projects", { key: "APV", name: "Approve check" })
  );
  if (project.status !== 201 && project.status !== 200)
    fail(`the project: ${JSON.stringify(project)}`);
  const issue = await described(
    await box("POST", "/api/v1/issues", {
      project: "APV",
      title: "A document to approve",
      spec: "# Approve check\n\nThe document a session approves through Cipher's proxy.\n",
    })
  );
  const key = (issue.body as Record<string, unknown>)["key"];
  if (typeof key !== "string") fail(`the issue: ${JSON.stringify(issue)}`);
  const read = await described(await box("GET", `/api/v1/issues/${key}`));
  const issueBody = read.body as Record<string, unknown>;
  const artifact = issueBody["primary_artifact_id"];
  if (typeof artifact !== "string") fail(`the issue read: ${JSON.stringify(read)}`);
  const summary = await described(await box("GET", `/api/v1/issues/${key}/summary`));
  const documents = ((summary.body as Record<string, unknown>)["documents"] ?? []) as Record<
    string,
    unknown
  >[];
  const version = Number(
    documents.find((document) => document["id"] === artifact)?.["latest_version"] ?? 0
  );
  if (!(version > 0)) fail(`the spec's version: ${JSON.stringify(summary)}`);
  writeFileSync(STATE, JSON.stringify({ issue: key, artifact, version }));
  say({ step: "setup", issue: key, artifact, version });
}

async function approve(): Promise<void> {
  const { artifact, version } = state();
  const client = new DispatchClient(PUBLIC, credential, localFetch);
  try {
    const review = (await client.reviewArtifact(artifact, version, {
      state: "approved",
    })) as unknown as Record<string, unknown>;
    say({
      step: "approve",
      recorded: true,
      state: review["state"],
      version: review["version"] ?? review["version_number"],
      actor: review["actor"],
    });
  } catch (error) {
    say({
      step: "approve",
      recorded: false,
      error: error instanceof Error ? error.message : String(error),
    });
  }
}

async function approveThenRevoked(): Promise<void> {
  const { artifact, version } = state();
  const route = await approveRoute();
  const path = `/api/v1/artifacts/${artifact}/versions/${version}/reviews`;
  const first = await described(await proxied(route, "POST", path, { state: "approved" }));
  const revoke = setting("APPROVE_CHECK_REVOKE_COMMAND");
  const ran = Bun.spawnSync(["bash", "-c", revoke], { stdout: "pipe", stderr: "pipe" });
  const second = await described(await proxied(route, "POST", path, { state: "approved" }));
  say({
    step: "approve-then-revoked",
    first: { status: first.status, code: first.code },
    revoke_exit: ran.exitCode,
    second,
  });
}

/** Every file under `path`, depth first; a file larger than 16 MiB is skipped, and named. */
function files(path: string, into: string[], skipped: string[]): void {
  let stat: Stats;
  try {
    stat = statSync(path);
  } catch {
    return;
  }
  if (stat.isDirectory()) {
    for (const entry of readdirSync(path)) files(join(path, entry), into, skipped);
  } else if (stat.isFile()) {
    if (stat.size > 16 * 1024 * 1024) skipped.push(path);
    else into.push(path);
  }
}

function sha256(text: string): string {
  return createHash("sha256").update(text).digest("hex");
}

/** Each `access_token` of a JSON object whose `token_type` is DPoP, anywhere in `value`. */
function dpopAnswers(value: unknown, into: Set<string>): void {
  if (Array.isArray(value)) {
    for (const item of value) dpopAnswers(item, into);
  } else if (typeof value === "object" && value !== null) {
    const record = value as Record<string, unknown>;
    if (
      typeof record["access_token"] === "string" &&
      String(record["token_type"]).toLowerCase() === "dpop"
    ) {
      into.add(record["access_token"]);
    }
    for (const item of Object.values(record)) dpopAnswers(item, into);
  }
}

/** Each JWK with a private part (`d`), anywhere in `value`. */
function privateJwks(value: unknown, into: Jwk[]): void {
  if (Array.isArray(value)) {
    for (const item of value) privateJwks(item, into);
  } else if (typeof value === "object" && value !== null) {
    const record = value as Record<string, unknown>;
    if (typeof record["kty"] === "string" && typeof record["d"] === "string") into.push(record);
    for (const item of Object.values(record)) privateJwks(item, into);
  }
}

async function scan(): Promise<void> {
  const [, , , logPath, ...paths] = process.argv;
  if (logPath === undefined || paths.length === 0)
    fail("scan needs the broker log and at least one path");
  // What the broker logged: each minted token's SHA-256 and the thumbprint of the key it is bound to.
  const logged = new Map<string, { grant: unknown; expires_at: unknown }>();
  const bindings = new Set<string>();
  for (const line of readFileSync(logPath, "utf8").split("\n")) {
    let record: Record<string, unknown>;
    try {
      record = JSON.parse(line) as Record<string, unknown>;
    } catch {
      continue;
    }
    if (
      record["message"] === "broker: minted a grant's token" &&
      typeof record["token_sha256"] === "string"
    ) {
      logged.set(record["token_sha256"], {
        grant: record["grant"],
        expires_at: record["expires_at"],
      });
      if (typeof record["jkt"] === "string") bindings.add(record["jkt"]);
    }
  }
  const read: string[] = [];
  const skipped: string[] = [];
  for (const path of paths) files(path, read, skipped);
  const texts: string[] = read.map((path) => readFileSync(path).toString("latin1"));
  // The environment of each process of the session: this one's, and every pid named.
  texts.push(
    Object.entries(env)
      .map(([name, value]) => `${name}=${value}`)
      .join("\n")
  );
  for (const pid of (env["APPROVE_CHECK_SESSION_PIDS"] ?? "").split(/\s+/).filter(Boolean)) {
    try {
      texts.push(readFileSync(`/proc/${pid}/environ`).toString("latin1").replaceAll("\0", "\n"));
    } catch {
      skipped.push(`/proc/${pid}/environ`);
    }
  }
  const base64url = new Set<string>();
  const token68 = new Set<string>();
  const answers = new Set<string>();
  const keys: Jwk[] = [];
  for (const text of texts) {
    for (const match of text.matchAll(/[A-Za-z0-9_-]{20,}/g)) base64url.add(match[0]);
    for (const match of text.matchAll(/[A-Za-z0-9._~+/=-]{20,}/g)) token68.add(match[0]);
    for (const block of text.matchAll(
      /-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----/g
    )) {
      try {
        keys.push(createPrivateKey(block[0]).export({ format: "jwk" }) as Jwk);
      } catch {
        // not a key this runtime reads; counted as no key
      }
    }
    for (const chunk of [text, ...text.split("\n")]) {
      try {
        const value: unknown = JSON.parse(chunk);
        dpopAnswers(value, answers);
        privateJwks(value, keys);
      } catch {
        // not JSON
      }
    }
  }
  const candidates = new Set<string>([...base64url, ...token68, ...answers]);
  const matched: { sha256: string; grant: unknown; expires_at: unknown }[] = [];
  for (const candidate of candidates) {
    const hash = sha256(candidate);
    const entry = logged.get(hash);
    if (entry !== undefined) matched.push({ sha256: hash, ...entry });
  }
  const keyMatches: string[] = [];
  for (const key of keys) {
    const thumbprint = ecThumbprint(key);
    if (thumbprint !== "" && bindings.has(thumbprint)) keyMatches.push(thumbprint);
  }
  // The diagnostic, which decides nothing: each candidate a matched hash or a DPoP answer names,
  // and at most 100 others, presented to Dispatch as a broker token under the DPoP scheme with a
  // proof of a throwaway key.
  const diagnostic: Record<string, number> = {};
  const presented = new Set<string>([
    ...answers,
    ...[...candidates].filter((candidate) => logged.has(sha256(candidate))),
  ]);
  for (const candidate of candidates) {
    if (presented.size >= 100 + answers.size + matched.length) break;
    presented.add(candidate);
  }
  const throwaway = await newDpopKey();
  const perMatch: Record<string, string> = {};
  for (const candidate of presented) {
    const url = `${PUBLIC}/api/v1/projects`;
    const answer = await described(
      await localFetch(url, {
        headers: {
          Authorization: `DPoP ${candidate}`,
          DPoP: await throwaway.proof({ method: "GET", url, accessToken: candidate }),
        },
      })
    );
    const said = `${answer.status} ${String(answer.code)}`;
    diagnostic[said] = (diagnostic[said] ?? 0) + 1;
    if (logged.has(sha256(candidate))) perMatch[sha256(candidate)] = said;
  }
  say({
    step: "scan",
    files_read: read.length,
    files_skipped: skipped,
    candidates: candidates.size,
    base64url: base64url.size,
    token68: token68.size,
    dpop_answers: answers.size,
    private_keys: keys.length,
    logged_hashes: logged.size,
    logged_bindings: bindings.size,
    hash_matches: matched,
    key_matches: keyMatches,
    diagnostic,
    diagnostic_of_matches: perMatch,
  });
}

async function main(): Promise<void> {
  const [command] = process.argv.slice(2);
  switch (command) {
    case "setup":
      return await setup();
    case "approve":
      return await approve();
    case "approve-then-revoked":
      return await approveThenRevoked();
    case "box":
    case "proxy": {
      // The method, the path, and a JSON body where one is given.
      const [method = "GET", path = "/", text] = process.argv.slice(3);
      const body: unknown = text === undefined ? undefined : JSON.parse(text);
      const sent =
        command === "box"
          ? await box(method, path, body)
          : await proxied(await approveRoute(), method, path, body);
      return say({ step: command, ...(await described(sent)) });
    }
    case "reviews": {
      const { artifact } = state();
      return say({
        step: "reviews",
        ...(await described(await box("GET", `/api/v1/artifacts/${artifact}/reviews`))),
      });
    }
    case "comments": {
      const { issue } = state();
      return say({
        step: "comments",
        ...(await described(await box("GET", `/api/v1/issues/${issue}/comments`))),
      });
    }
    case "agent-tokens":
      return say({
        step: "agent-tokens",
        ...(await described(await box("GET", "/api/v1/me/agent-tokens"))),
      });
    case "scan":
      return await scan();
    default:
      fail(`unknown command ${String(command)}`);
  }
}

await main();
process.exit(0);
