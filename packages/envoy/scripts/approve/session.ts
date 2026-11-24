// packages/envoy/scripts/approve/session.ts
//
// The agent session of scripts/approve-check.sh, on the stack `dev-stack.sh --approve` stands up:
// a session on a box where a person signed in (`agent-secrets login`), whose Dispatch credential is
// the broker credential the TypeScript client builds from this process's environment. Each command
// is one step of the check and prints one JSON line of what it saw, never a token or a placeholder.
//
// Dispatch serves under https://<the approve host> (DISPATCH_URL). On the local stack it listens on
// loopback (APPROVE_CHECK_DISPATCH_LOCAL). A request through Cipher's proxy goes to the public URL,
// which the proxy terminates and sends on to Dispatch's own address; a direct request is signed for
// the public URL, as every request of a box is in production, and sent to the loopback address,
// which stands in for the name resolving there. With APPROVE_CHECK_DISPATCH_LOCAL unset, the run is
// on a box (k31 Part 1), and every request goes to the public URL itself.
//
//   setup                         as the person: a project, an issue and its spec, saved to the state file
//   approve                       the spec's version reviewed approved through the proxy (reviewArtifact)
//   approve-then-revoked          the same review twice on one approve route, the sign-in revoked between
//   box <method> <path> [json]    a request with the box's own Dispatch token, direct
//   proxy <method> <path> [json]  a request with the approve placeholder, through the proxy
//   reviews                       the spec's reviews, read with the box's token
//   comments                      the issue's comments, read with the box's token
//   agent-tokens                  the person's agent tokens, read with the box's token
//   scan <broker log> <path>...   every candidate credential and private key in the paths and in the
//                                 environment of each process of the session, against the hashes and
//                                 bindings the broker logged
//
// The scan reads the environment of each process APPROVE_CHECK_SESSION_PIDS names. On the local
// stack every step of the session is one process started with the one environment approve-check.sh
// gives it, so this process's own environment is the session's. On a box the session is processes
// of their own, so a scan there with no process named reads none of them: it prints UNKNOWN and
// exits 3 rather than report a clean environment it never read.

import { createHash, createPrivateKey } from "node:crypto";
import {
  closeSync,
  lstatSync,
  openSync,
  readdirSync,
  readFileSync,
  readSync,
  type Stats,
  statSync,
  writeFileSync,
} from "node:fs";
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
const LOCAL = (env["APPROVE_CHECK_DISPATCH_LOCAL"] ?? "").trim().replace(/\/+$/, "");

/** A fetch that sends a request of the public URL to Dispatch's loopback address, and one through
 *  a proxy as it is: the proxy itself reaches Dispatch's own address. */
const localFetch = Object.assign(
  async (input: Parameters<typeof fetch>[0], init?: RequestInit & { proxy?: string }) => {
    const url = String(input);
    if (LOCAL === "" || init?.proxy !== undefined || !url.startsWith(PUBLIC)) {
      return await fetch(input, init);
    }
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
  return JSON.parse(readFileSync(setting("APPROVE_CHECK_STATE"), "utf8")) as State;
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
  writeFileSync(setting("APPROVE_CHECK_STATE"), JSON.stringify({ issue: key, artifact, version }));
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

/** What a walk of the named paths found: every file to read, every path it could not read, and
 *  every symbolic link whose target does not exist, which holds no data to read. */
interface Walk {
  readonly files: string[];
  readonly skipped: string[];
  readonly dangling: string[];
}

/**
 * Every file under `path`, depth first, into `walk.files`, whatever its size (`fileChunks` reads a
 * large one in pieces). A path or a directory that cannot be read goes into `walk.skipped`, named,
 * never dropped. A symbolic link is followed to what it names; one whose target does not exist
 * holds no data, and goes into `walk.dangling`, named.
 */
function files(path: string, walk: Walk): void {
  let stat: Stats;
  try {
    stat = statSync(path);
  } catch (error) {
    const missing = (error as NodeJS.ErrnoException).code === "ENOENT";
    let link = false;
    try {
      link = lstatSync(path).isSymbolicLink();
    } catch {
      // the path itself does not exist
    }
    if (missing && link) walk.dangling.push(path);
    else walk.skipped.push(path);
    return;
  }
  if (stat.isDirectory()) {
    let entries: string[];
    try {
      entries = readdirSync(path);
    } catch {
      walk.skipped.push(path);
      return;
    }
    for (const entry of entries) files(join(path, entry), walk);
  } else if (stat.isFile()) {
    walk.files.push(path);
  }
}

/** The size of a piece of a file the scan reads at once, and how far each piece reaches back
 *  into the one before it: far more than any credential the scan looks for (a broker token is 87
 *  characters, an EC private key's PEM a few hundred and its JWK a flat object of a few hundred,
 *  escaped in a JSON string a little more), so a credential that crosses a piece's end is whole in
 *  the next piece. A JSON document or line larger than a piece is never whole in one, so the scan
 *  reads keys out of each flat object rather than out of whole documents (`take`). */
const CHUNK_BYTES = 8 * 1024 * 1024;
const CHUNK_OVERLAP = 64 * 1024;

/** The one buffer every piece of every file is read into. */
const CHUNK_BUFFER = Buffer.allocUnsafe(CHUNK_BYTES);

/** Each piece of the file at `path` in turn, as Latin-1 text, so a file of any size is read
 *  whole without holding it all: pieces of `CHUNK_BYTES` that reach `CHUNK_OVERLAP` back, each
 *  filled until it is full or the file ends (a read returns no byte). */
function* fileChunks(path: string): Generator<string> {
  const fd = openSync(path, "r");
  try {
    for (let at = 0; ; at += CHUNK_BYTES - CHUNK_OVERLAP) {
      let got = 0;
      while (got < CHUNK_BYTES) {
        const read = readSync(fd, CHUNK_BUFFER, got, CHUNK_BYTES - got, at + got);
        if (read === 0) break;
        got += read;
      }
      yield CHUNK_BUFFER.toString("latin1", 0, got);
      if (got < CHUNK_BYTES) return;
    }
  } finally {
    closeSync(fd);
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

/** Every string value anywhere in a parsed JSON `value`, pushed onto `into`. */
function jsonStrings(value: unknown, into: string[]): void {
  if (typeof value === "string") {
    into.push(value);
  } else if (Array.isArray(value)) {
    for (const item of value) jsonStrings(item, into);
  } else if (typeof value === "object" && value !== null) {
    for (const item of Object.values(value)) jsonStrings(item, into);
  }
}

/** `text` with the escapes a JSON or shell string writes a line ending, a tab, a quote, a slash or
 *  a backslash in decoded, which is how a PEM key kept as one string's value reads as PEM. */
function unescaped(text: string): string {
  const decoded: Record<string, string> = { n: "\n", r: "\r", t: "\t" };
  return text.replace(/\\(["\\/nrt])/g, (_, escaped: string) => decoded[escaped] ?? escaped);
}

/** `text` with a form's `+` read as a space and each `%XX` decoded to its byte, which is how a
 *  token in a logged URL query (`%20<token>`, `%3D<token>`) or a form body (`DPoP+<token>`) reads
 *  as itself. */
function urlDecoded(text: string): string {
  return text
    .replace(/\+/g, " ")
    .replace(/%([0-9A-Fa-f]{2})/g, (_, hex: string) =>
      String.fromCharCode(Number.parseInt(hex, 16))
    );
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
  const pids = (env["APPROVE_CHECK_SESSION_PIDS"] ?? "").split(/\s+/).filter(Boolean);
  // Each reason the scan compared nothing somewhere it was asked to look: with any, its result is
  // UNKNOWN and it exits 3, a pass of nothing.
  const unknown: string[] = [];
  // What the broker logged: each minted token's SHA-256 and the thumbprint of the key it is bound to.
  const logged = new Map<string, { grant: unknown; expires_at: unknown }>();
  const bindings = new Set<string>();
  let logText = "";
  try {
    logText = readFileSync(logPath, "utf8");
  } catch (error) {
    unknown.push(`the broker log ${logPath} could not be read (${String(error)})`);
  }
  for (const line of logText.split("\n")) {
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
  if (logged.size === 0) {
    unknown.push(
      "the broker log names no minted token's SHA-256 (no `broker: minted a grant's token` line), so no candidate's hash is compared with anything"
    );
  }
  if (bindings.size === 0) {
    unknown.push(
      "the broker log names no key binding (no `jkt` on a minted token's line), so no key's thumbprint is compared with anything"
    );
  }
  const base64url = new Set<string>();
  const token68 = new Set<string>();
  const answers = new Set<string>();
  const keys = new Map<string, Jwk>();
  // take reads one text in every form a credential can sit in it: as written; with the escapes a
  // JSON or shell string writes a line ending, a quote or a slash in decoded, so a PEM key a file
  // keeps as one JSON string (a sign-in record's box_key) reads as PEM; with a URL's
  // percent-encoding and a form's `+` for a space decoded (urlDecoded), so a token in a logged query
  // or form body reads as itself; each flat JSON object in it, for a private key kept as a JWK,
  // which a piece holds whole as the overlap holds a PEM key; and, where the text holds a whole
  // JSON document or line, each of its string values, which may itself hold JSON or PEM. Each form
  // of the text is read once.
  const take = (root: string): void => {
    const seen = new Set<string>();
    const queue = [root];
    for (let text = queue.pop(); text !== undefined; text = queue.pop()) {
      if (seen.has(text)) continue;
      seen.add(text);
      queue.push(unescaped(text), urlDecoded(text));
      for (const match of text.matchAll(/[A-Za-z0-9_-]{20,}/g)) base64url.add(match[0]);
      // RFC 7235's token68: `=` only as trailing padding. A broker token holds a `.` (authelia's
      // HMAC strategy writes `%s.%s`), so the base64url pattern finds only its halves, and an `=`
      // inside the class would take the `NAME=` before a token as part of it.
      for (const match of text.matchAll(/[A-Za-z0-9._~+/-]{20,}=*/g)) token68.add(match[0]);
      // The broker token is exactly 43 base64url characters, a `.`, and 43 more (32 bytes each,
      // authelia's HMAC strategy), so the 43 characters on each side of every `.` are a candidate,
      // whatever is before or after it (`cache_<token>`, `<uuid>.<token>`, `/v1/<token>`,
      // `DPoP+<token>`): the patterns above take such a run as part of the token. A lookahead, so
      // each `.` gives one candidate, in time linear in the text. A token that is itself encoded
      // again, base64 of it among them, is outside k31's forms and not read.
      for (const match of text.matchAll(/(?=([A-Za-z0-9_-]{43}\.[A-Za-z0-9_-]{43}))/g))
        token68.add(match[1] as string);
      for (const block of text.matchAll(
        /-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----/g
      )) {
        try {
          const key = createPrivateKey(block[0]).export({ format: "jwk" }) as Jwk;
          keys.set(JSON.stringify(key), key);
        } catch {
          // not a key this runtime reads; counted as no key
        }
      }
      // A JWK is a flat object of a few hundred bytes: each flat object holding a "d" member is
      // read on its own, so a key inside a document or a line no piece holds whole is still found.
      for (const flat of text.matchAll(/\{[^{}]{1,16384}\}/g)) {
        if (!flat[0].includes('"d"')) continue;
        try {
          const found: Jwk[] = [];
          privateJwks(JSON.parse(flat[0]), found);
          for (const key of found) keys.set(JSON.stringify(key), key);
        } catch {
          // not JSON
        }
      }
      for (const chunk of [text, ...text.split("\n")]) {
        let value: unknown;
        try {
          value = JSON.parse(chunk);
        } catch {
          continue;
        }
        dpopAnswers(value, answers);
        const found: Jwk[] = [];
        privateJwks(value, found);
        for (const key of found) keys.set(JSON.stringify(key), key);
        jsonStrings(value, queue);
      }
    }
  };
  // Every file under every named path, each read whole in pieces (fileChunks). A path, a
  // directory or a file that could not be read is named in files_skipped, and makes the scan
  // UNKNOWN: a file it did not read is a file it cannot call clean. A symbolic link whose target
  // does not exist holds no data; it is named in dangling_links and read as nothing.
  const read: string[] = [];
  const skipped: string[] = [];
  const dangling: string[] = [];
  for (const path of paths) {
    const walk: Walk = { files: [], skipped, dangling };
    files(path, walk);
    let readHere = 0;
    for (const file of walk.files) {
      try {
        for (const piece of fileChunks(file)) take(piece);
        read.push(file);
        readHere += 1;
      } catch {
        skipped.push(file);
      }
    }
    if (readHere === 0) unknown.push(`no file under ${path} could be read`);
  }
  if (read.length === 0) unknown.push("the scan read no file");
  // Each environment variable read, by name, for the report: the scan names the variables it read
  // and the ones that hold a minted token, and prints no value of any. Each variable reads
  // `NAME=value`, as a shell or an env file writes it.
  const environment = new Map<string, string[]>();
  const keepEnvironment = (entries: readonly string[]): void => {
    for (const entry of entries) {
      const at = entry.indexOf("=");
      if (at <= 0) continue;
      const name = entry.slice(0, at);
      environment.set(name, [...(environment.get(name) ?? []), entry.slice(at + 1)]);
    }
  };
  // The environment of each process of the session: this one's, and every pid named.
  const own = Object.entries(env).map(([name, value]) => `${name}=${value}`);
  keepEnvironment(own);
  take(own.join("\n"));
  for (const pid of pids) {
    try {
      const entries = readFileSync(`/proc/${pid}/environ`).toString("latin1").split("\0");
      keepEnvironment(entries);
      take(entries.join("\n"));
    } catch {
      skipped.push(`/proc/${pid}/environ`);
    }
  }
  if (LOCAL === "" && pids.length === 0) {
    // On a box the session's processes are not this one, so a scan that names none of them reads
    // no environment of the session, and would call clean what it never saw.
    unknown.push(
      "APPROVE_CHECK_SESSION_PIDS names no process of the session, and outside the local stack (APPROVE_CHECK_DISPATCH_LOCAL unset) the session's processes are not this one"
    );
  }
  for (const missed of skipped) unknown.push(`${missed} could not be read`);
  const candidates = new Set<string>([...base64url, ...token68, ...answers]);
  const matched: { sha256: string; grant: unknown; expires_at: unknown }[] = [];
  for (const candidate of candidates) {
    const hash = sha256(candidate);
    const entry = logged.get(hash);
    if (entry !== undefined) matched.push({ sha256: hash, ...entry });
  }
  const keyMatches = new Set<string>();
  for (const key of keys.values()) {
    const thumbprint = ecThumbprint(key);
    if (thumbprint !== "" && bindings.has(thumbprint)) keyMatches.add(thumbprint);
  }
  // The diagnostic, which decides nothing: each candidate whose SHA-256 is a token the broker
  // logged minting, presented to Dispatch as a broker token under the DPoP scheme with a proof of
  // a throwaway key, to say whether that token still acts. No other value leaves the box: an
  // environment variable, a file's contents or a DPoP answer that is no minted token is another
  // secret of the session's, which Dispatch has no reason to see.
  const diagnostic: Record<string, number> = {};
  const presented = [...candidates].filter((candidate) => logged.has(sha256(candidate)));
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
    perMatch[sha256(candidate)] = said;
  }
  // The environment variables the scan read, by name, and those whose value holds a minted token.
  const environmentMatches = [...environment]
    .filter(([, values]) =>
      values.some((value) => presented.some((token) => value.includes(token)))
    )
    .map(([name]) => name)
    .sort();
  const result =
    unknown.length > 0 ? "UNKNOWN" : matched.length + keyMatches.size > 0 ? "found" : "clean";
  say({
    step: "scan",
    result,
    ...(unknown.length > 0 ? { unknown } : {}),
    files_read: read.length,
    files_skipped: skipped,
    dangling_links: dangling,
    candidates: candidates.size,
    base64url: base64url.size,
    token68: token68.size,
    dpop_answers: answers.size,
    private_keys: keys.size,
    logged_hashes: logged.size,
    logged_bindings: bindings.size,
    hash_matches: matched,
    key_matches: [...keyMatches],
    diagnostic,
    diagnostic_presented: presented.length,
    diagnostic_of_matches: perMatch,
    environment_names: [...environment.keys()].sort(),
    environment_matches: environmentMatches,
  });
  // UNKNOWN exits 3 whatever else it found: a match beside it is still in hash_matches or
  // key_matches, for approve-check to call the step a failure.
  if (result === "UNKNOWN") process.exit(3);
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
