/**
 * The capture of one arm of a case (plan, R5 `capture.ts`): the request body the pinned Oh My Pi
 * 18.18.6 sends for the arm's messages in the case's context, which replay.ts then sends to the
 * gateway. `captureBody(c, arm, options)` runs
 *
 *     omp --profile capture -p --mode json --no-session --model <case model>
 *         --thinking <case thinking> --hook replay-hook.ts [--extension <plugin>]...
 *         [--append-system-prompt <file>] <placeholder prompt>
 *
 * with a scratch HOME, in the case's context (context.ts), against a recorder on 127.0.0.1
 * (recorder.ts) that every model provider of the scratch profile points at. The hook's `context`
 * handler gives the model the arm's messages and its `tool_call` handler blocks every call. The
 * recorder keeps the turn's request and answers it with a fixed end-of-turn reply, so no tool runs
 * and the turn's request never reaches the gateway. One capture per case and arm: the body, as Oh
 * My Pi sent it byte for byte, and its record are saved under `outDir`, and a capture already
 * saved there is read back rather than made again.
 *
 * **The skill listing.** Oh My Pi lists each installed skill in its system prompt under a hint,
 * not under the skill's `description`: a line of at most 160 characters that it asks its `smol`
 * model to write from the name and the description (packages/coding-agent/src/extensibility/
 * skill-descriptions.ts at the pinned tag: a one-message request of the prompt
 * `prompts/skills/compress-description.md`, which ends `Skill: <name>` / `Description: <text>`,
 * 1024 tokens at most, temperature 0, no reasoning). It keeps each hint in the profile's
 * `skill-descriptions.db` (`skill_descriptions(key, description)`, the key the SHA-256 of the
 * prompt, the name and the description) and lists a cut-down `description` in its place until the
 * hint is written, which it does in the background once the session exists. So the arm's listing
 * is the hint Oh My Pi would list for the arm's text: each hint is kept in the runner's cache
 * (`<cacheDir>/hints/<key of the Oh My Pi version, the name and the description>.json`) with the
 * key Oh My Pi gave it, and written into the scratch profile's database before the run. A skill
 * the cache lacks needs one run more first, in which the recorder sends each hint request to
 * `options.hints` (the real gateway, `gatewayHints`) and holds the turn until every missing hint
 * is written; Oh My Pi then stores each hint itself, and the capture keeps it. Every capture of one
 * skill text under one Oh My Pi version therefore lists the same hint. A skill whose frontmatter
 * sets `hide` or `disable-model-invocation` is installed but never listed, so it gets no hint. A
 * capture whose listing is not exactly the profile's listed skills under their hints is refused.
 *
 * **The digests.** The record holds the session's `system-prompt-digest` (`sessionDigest`, from
 * the case) and the capture's own, `promptDigest`, which is Oh My Pi's algorithm
 * (packages/coding-agent/src/session/session-tools.ts, `systemPromptDigest`): a wyhash chain over
 * the system prompt's blocks, seeded with their count, in hex. Oh My Pi hashes its prompt blocks
 * before it sends them; the body's `system` blocks are those blocks less any that is blank or
 * holds a credential it redacts, so the two agree whenever no block is either.
 */
import { Database } from "bun:sqlite";
import { randomBytes } from "node:crypto";
import {
  existsSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  renameSync,
  rmSync,
  writeFileSync,
} from "node:fs";
import { writeFile } from "node:fs/promises";
import path from "node:path";
import { messageFor } from "@legion/envoy-client/errors";
import { linkOmpNatives } from "@legion/pi-shared/test/omp-natives";
import type { Arm, ArmName } from "./arms";
import type { Case } from "./cases";
import { buildContext, type ContextRecord, cacheKey, legionRole, type RepoSource } from "./context";
import type { Gateway, MessagesBody } from "./gateway";
import { messageSse, type RecordedRequest, sseResponse, startRecorder } from "./recorder";
import { HOOK_LOG_VARIABLE, MESSAGES_VARIABLE } from "./replay-hook";

/** The scratch profile's name. */
const PROFILE = "capture";
/** The run's own first message, which the hook replaces with the arm's messages. */
const PLACEHOLDER = "placeholder prompt";
/** The hook file Oh My Pi loads. */
const HOOK = path.join(import.meta.dir, "replay-hook.ts");
/** The longest the recorder holds a run's turn while it waits for the hints it is owed. */
const HINT_WAIT_MS = 180_000;
/** The longest one Oh My Pi run may take, by default. */
const RUN_TIMEOUT_MS = 600_000;
/** Oh My Pi's own bound on a hint it keeps (`MAX_COMPRESSED_CHARS`). */
const MAX_HINT_CHARS = 160;
/** What the scratch profile gives Oh My Pi as its provider credential: the recorder reads none,
 *  and no gateway ever gets this one. */
// fixture-secret-next-line: a placeholder the recorder never reads, and never a credential
const RECORDER_CREDENTIAL = "placeholder-the-recorder-reads-none";
/** The made-up tree and issue a Legion capture's pane claims, and how many times its run is made
 *  when it ends before the turn's request. */
const CAPTURE_TREE = "CAPTURE-1";
const CAPTURE_ISSUE = "CAPTURE-2";
const CAPTURE_TRIES = 3;

/** An Oh My Pi run that ended with no model request a capture can take. */
class RunEnded extends Error {}

/** The tools a body defines and the `xd://` devices its system prompt lists, the Legion and
 *  Envoy plugins' among them, and each plugin tool left out with why. */
function pluginTools(
  body: MessagesBody,
  context: ContextRecord,
  pane: boolean,
  role: { readonly role: string } | undefined,
  stderr: string
): CaptureRecord["pluginTools"] {
  const tools = Array.isArray(body.tools)
    ? body.tools.flatMap((tool: unknown) => {
        const name = typeof tool === "object" && tool !== null ? Reflect.get(tool, "name") : null;
        return typeof name === "string" ? [name] : [];
      })
    : [];
  const devices = systemTexts(body).flatMap((text) =>
    [...text.matchAll(/^- xd:\/\/([A-Za-z0-9_-]+)/gm)].map((match) => match[1] ?? "")
  );
  const leftOut = context.plugins
    .filter((plugin) => plugin.as === "left out")
    .map((plugin) => `${plugin.name}: its build failed: ${plugin.reason ?? ""}`);
  if (role?.role === "controller") {
    leftOut.push("legion: a controller session is not reproduced, so its tool is not defined");
  } else if (pane && !tools.includes("legion")) {
    const said = stderr
      .split("\n")
      .filter((line) => line.startsWith("[legion]") || line.includes("Extension error"));
    leftOut.push(`legion: the plugin defined no tool: ${said.join(" ")}`);
  }
  return {
    tools,
    devices,
    legion: tools.includes("legion"),
    envoy: devices.filter((name) => name.startsWith("envoy_")),
    leftOut,
  };
}

/** The counts the hook's log holds: how many messages of its own run it dropped, per request. */
function hookCounts(file: string): number[] {
  if (!existsSync(file)) return [];
  return readFileSync(file, "utf8")
    .split("\n")
    .filter((line) => line !== "")
    .flatMap((line) => {
      const entry: unknown = JSON.parse(line);
      const dropped =
        typeof entry === "object" && entry !== null ? Reflect.get(entry, "dropped") : null;
      return typeof dropped === "number" ? [dropped] : [];
    });
}
/** The providers Oh My Pi would otherwise try to discover models from. */
const DISABLED_PROVIDERS = [
  "amazon-bedrock",
  "bedrock-mantle",
  "google",
  "google-vertex",
  "ollama",
  "llama.cpp",
  "lm-studio",
];

/** A hint a model wrote: its text, the model that answered, and what the call used. */
export interface HintReply {
  readonly text: string;
  readonly model: string;
  readonly usage?: unknown;
}

/** Answers one of Oh My Pi's hint requests: its body as Oh My Pi built it, and the `anthropic-*`
 *  headers Oh My Pi sent with it. */
export type HintWriter = (
  body: MessagesBody,
  headers: Readonly<Record<string, string>>
) => Promise<HintReply>;

/** The `anthropic-*` headers of a recorded request: the API version, and the betas the fields of
 *  Oh My Pi's body need (adaptive thinking's `block_binding`, `context_management` and
 *  `output_config.effort` are refused without theirs). */
function anthropicHeaders(headers: Readonly<Record<string, string>>): Record<string, string> {
  return Object.fromEntries(
    Object.entries(headers).filter(([name]) => name.startsWith("anthropic-"))
  );
}

/** Hint requests answered by the real model through `gateway`, each body sent with the
 *  `anthropic-*` headers Oh My Pi sent beside it, its spend counted with the rest of the gateway's
 *  and kept in the capture's record (`usage`, gateway.ts's `Spent`). */
export function gatewayHints(gateway: Gateway): HintWriter {
  return async (body, headers) => {
    const reply = await gateway.send(body, { headers });
    const text = reply.message.content
      .map((block) => (block.type === "text" && typeof block.text === "string" ? block.text : ""))
      .join("");
    return { text, model: reply.message.model, usage: reply.spent };
  };
}

export interface CaptureOptions {
  /** The Oh My Pi binary. */
  readonly omp: string;
  /** Where each body and its record are saved. */
  readonly outDir: string;
  /** Where each case gets its scratch directory, left in place after its capture. */
  readonly scratchDir: string;
  /** The cache of hints and of context.ts's builds. */
  readonly cacheDir: string;
  /** Where the context's repositories are read. */
  readonly source: RepoSource;
  /** A `models.yml` of the context's Oh My Pi. Its providers of the case's model and the hint
   *  model are kept, each pointed at the recorder with a placeholder key. */
  readonly models: string;
  /** Writes the hints the cache lacks. */
  readonly hints: HintWriter;
  /** The model that writes hints (the context's `smol` role), the case's model by default. */
  readonly hintModel?: string;
  /** Oh My Pi settings files laid over the scratch profile's, as `PI_CONFIG_FILES` names them. */
  readonly configFiles?: readonly string[];
  /** The longest one Oh My Pi run may take. */
  readonly timeoutMs?: number;
}

/** What a capture used and saw. */
export interface CaptureRecord {
  readonly case: string;
  readonly arm: ArmName;
  readonly skill: string;
  readonly capturedAt: string;
  readonly omp: { readonly binary: string; readonly version: string };
  readonly model: string;
  readonly thinking: string;
  readonly hintModel: string;
  readonly configFiles: readonly string[];
  /** The `anthropic-*` headers Oh My Pi sent with the turn's request, which a replay of the body
   *  sends too. */
  readonly requestHeaders: Readonly<Record<string, string>>;
  /** The session's digest, from the case, and the capture's own (`promptDigest`). */
  readonly sessionDigest: string | null;
  readonly captureDigest: string;
  /** The case's scratch directory, as the last capture of it left it. */
  readonly scratch: string;
  /** Each skill the body lists, with the text it lists it under. */
  readonly listing: readonly { readonly name: string; readonly hint: string }[];
  /** Each installed skill's hint: the runner's cache key, Oh My Pi's key, and whether this
   *  capture had it written. */
  readonly hints: readonly {
    readonly skill: string;
    readonly hint: string;
    readonly key: string;
    readonly ompKey: string;
    readonly written: boolean;
  }[];
  /** Each hint request sent to `options.hints`. */
  readonly hintCalls: readonly {
    readonly skill: string;
    readonly model: string;
    readonly usage?: unknown;
  }[];
  /** Requests that were neither the turn nor a hint request; each was refused. */
  readonly otherRequests: readonly { readonly path: string; readonly bytes: number }[];
  /** Oh My Pi's own lines on extensions that failed to load. */
  readonly extensionErrors: readonly string[];
  readonly context: ContextRecord;
  /** For a Legion context, the pane environment the capture gave Oh My Pi (made-up keys, every
   *  endpoint a listener of this process on 127.0.0.1), or null. */
  readonly legion: {
    readonly role: string;
    readonly tree: string;
    readonly issue: string;
    readonly endpoints: Readonly<Record<string, string>>;
    readonly dispatch: string;
  } | null;
  /** Every request the capture's listeners took, as `<method> <path>` (the query left out), and
   *  how many connections the NATS sink took: all of them went to 127.0.0.1. */
  readonly connections: { readonly http: readonly string[]; readonly nats: number };
  /** The tools the body defines (`tools`) and the `xd://` devices its system prompt lists, the
   *  plugins' among them, and each plugin tool left out with why. */
  readonly pluginTools: {
    readonly tools: readonly string[];
    readonly devices: readonly string[];
    readonly legion: boolean;
    readonly envoy: readonly string[];
    readonly leftOut: readonly string[];
  };
  /** How many messages the run itself added that the hook dropped, at each model request. */
  readonly hookDropped: readonly number[];
  /** How the capture run ended: its exit code or signal, and why when it was not 0. A run that
   *  ends after the recorder stored the turn's request still gives that request. */
  readonly exit: {
    readonly code: number | null;
    readonly signal: string | null;
    readonly reason: string;
  };
  /** How many Oh My Pi runs the capture took, and how long each part took, in milliseconds. */
  readonly runs: number;
  readonly timings: {
    readonly contextMs: number;
    readonly hintRunMs: number;
    readonly captureRunMs: number;
    readonly totalMs: number;
  };
}

/** A saved capture: the body, its record, and their files. */
export interface Capture {
  readonly body: MessagesBody;
  readonly record: CaptureRecord;
  readonly bodyFile: string;
  readonly recordFile: string;
}

/** A hint in the runner's cache. */
interface CachedHint {
  readonly omp: string;
  readonly name: string;
  readonly description: string;
  readonly ompKey: string;
  readonly hint: string;
  readonly model: string;
  readonly writtenAt: string;
}

/** The files of the capture of `caseId`'s arm `arm` under `outDir`. */
export function capturePaths(
  outDir: string,
  caseId: string,
  arm: ArmName
): { readonly body: string; readonly record: string } {
  const stem = path.join(outDir, `${encodeURIComponent(caseId)}.${arm}`);
  return { body: `${stem}.body.json`, record: `${stem}.capture.json` };
}

/** The texts of a body's `system` blocks, in order. */
function systemTexts(body: unknown): string[] {
  const system = typeof body === "object" && body !== null ? Reflect.get(body, "system") : null;
  if (typeof system === "string") return [system];
  if (!Array.isArray(system)) return [];
  return system.flatMap((block: unknown) => {
    const text = typeof block === "object" && block !== null ? Reflect.get(block, "text") : null;
    return typeof text === "string" ? [text] : [];
  });
}

/** Oh My Pi's `system-prompt-digest` of the prompt a body's `system` blocks carry. */
export function promptDigest(body: unknown): string {
  const blocks = systemTexts(body);
  let hash = BigInt(blocks.length);
  for (const block of blocks) hash = BigInt(Bun.hash.wyhash(block, hash));
  return hash.toString(16);
}

/** The skills a body's system prompt lists (`<skills>` … `</skills>`), each with the text it is
 *  listed under, in order. */
export function skillListing(body: unknown): Map<string, string> {
  const listing = new Map<string, string>();
  for (const text of systemTexts(body)) {
    const block = /<skills>\n([\s\S]*?)<\/skills>/.exec(text)?.[1];
    for (const line of block?.split("\n") ?? []) {
      const match = /^- ([^:\s]+): (.*)$/.exec(line);
      if (match?.[1] !== undefined && match[2] !== undefined) listing.set(match[1], match[2]);
    }
  }
  return listing;
}

/** `model` with its provider, `anthropic` when it names none. */
function qualified(model: string): string {
  return model.includes("/") ? model : `anthropic/${model}`;
}

/** The skill a request asks a hint for, when it is one of Oh My Pi's hint requests: no system
 *  prompt, no tools, one user message ending `Skill: <name>` and `Description: <text>`. */
function hintSkill(body: Record<string, unknown>): string | undefined {
  if (body.system !== undefined || body.tools !== undefined) return undefined;
  const messages = body.messages;
  if (!Array.isArray(messages) || messages.length !== 1) return undefined;
  const content: unknown = messages[0]?.content;
  const text =
    typeof content === "string"
      ? content
      : Array.isArray(content)
        ? content.map((part) => (typeof part?.text === "string" ? part.text : "")).join("")
        : "";
  return /\nSkill: ([^\n]+)\nDescription: /.exec(text)?.[1];
}

/** Writes `text` to `file` whole: into a fresh file that is then renamed over it. */
async function writeWhole(file: string, text: string): Promise<void> {
  mkdirSync(path.dirname(file), { recursive: true });
  const fresh = `${file}.part-${process.pid}`;
  await writeFile(fresh, text);
  renameSync(fresh, file);
}

const versions = new Map<string, Promise<string>>();

/** What `omp --version` prints for `binary`. */
function ompVersion(binary: string): Promise<string> {
  let version = versions.get(binary);
  if (version === undefined) {
    version = (async () => {
      const child = Bun.spawn([binary, "--version"], { stdout: "pipe", stderr: "ignore" });
      const [text, code] = await Promise.all([new Response(child.stdout).text(), child.exited]);
      if (code !== 0) throw new Error(`${binary} --version failed with exit ${code}`);
      return text.trim();
    })();
    versions.set(binary, version);
  }
  return version;
}

/** The scratch profile's `models.yml` and `config.yml` (each JSON, which YAML reads): the
 *  providers of `models` that serve `wanted`, pointed at `url`, and the model roles. */
function writeProfile(
  agentDir: string,
  options: {
    readonly url: string;
    readonly models: string;
    readonly model: string;
    readonly hintModel: string;
    readonly skillsDir: string;
  }
): void {
  const parsed: unknown = Bun.YAML.parse(readFileSync(options.models, "utf8"));
  const providers: unknown =
    typeof parsed === "object" && parsed !== null ? Reflect.get(parsed, "providers") : undefined;
  if (typeof providers !== "object" || providers === null) {
    throw new Error(`${options.models} holds no providers`);
  }
  const kept: Record<string, unknown> = {};
  for (const wanted of [options.model, options.hintModel]) {
    const [provider = "", id] = wanted.split(/\/(.*)/s);
    const entry: unknown = Reflect.get(providers, provider);
    const models: unknown =
      typeof entry === "object" && entry !== null ? Reflect.get(entry, "models") : undefined;
    const listed =
      Array.isArray(models) && models.some((model) => Reflect.get(Object(model), "id") === id);
    if (!listed) throw new Error(`${options.models} defines no model ${wanted}`);
    if (Reflect.get(Object(entry), "api") !== "anthropic-messages") {
      throw new Error(`the model ${wanted} is not served on the Anthropic Messages route`);
    }
    const { headers: _headers, ...rest } = entry as Record<string, unknown>;
    kept[provider] = {
      ...rest,
      baseUrl: options.url,
      auth: "apiKey",
      apiKey: RECORDER_CREDENTIAL,
    };
  }
  mkdirSync(agentDir, { recursive: true });
  writeFileSync(
    path.join(agentDir, "models.yml"),
    `${JSON.stringify({ providers: kept }, null, 2)}\n`
  );
  writeFileSync(
    path.join(agentDir, "config.yml"),
    `${JSON.stringify(
      {
        modelRoles: { default: options.model, smol: options.hintModel },
        disabledProviders: DISABLED_PROVIDERS,
        skills: { customDirectories: [options.skillsDir] },
      },
      null,
      2
    )}\n`
  );
}

/** Oh My Pi's lines on extensions that failed to load: from its log files under `profileDir`,
 *  and from `stderr`. */
function extensionErrors(profileDir: string, stderr: string): string[] {
  const errors = stderr.split("\n").filter((line) => /extension.*fail|fail.*extension/i.test(line));
  const logs = path.join(profileDir, "logs");
  for (const file of existsSync(logs) ? readdirSync(logs) : []) {
    for (const line of readFileSync(path.join(logs, file), "utf8").split("\n")) {
      if (line.includes('"Failed to load extension"')) errors.push(line);
    }
  }
  return errors;
}

/** What a capture reads of a case: its id, skill, model and thinking level, its context and
 *  profile, and the session's digest. A load test's task (load.ts) is one of these too. */
export type CaptureCase = Pick<
  Case,
  "id" | "skill" | "model" | "thinking" | "context" | "profile" | "sessionDigest"
>;

/**
 * Captures the request body of `c`'s arm `arm`, or reads back the capture saved already. Throws
 * when Oh My Pi fails or sends no turn, when a hint cannot be written, or when the body does not
 * list exactly the arm's installed skills under their hints.
 */
export async function captureBody(
  c: CaptureCase,
  arm: Arm,
  options: CaptureOptions
): Promise<Capture> {
  const role = legionRole(c.context);
  // A controller session is not reproduced: it claims with a controller secret, not a boot.
  const pane = role !== undefined && role.role !== "controller" ? role : undefined;
  const issue = pane?.isRoot === true ? CAPTURE_TREE : CAPTURE_ISSUE;
  const files = capturePaths(options.outDir, c.id, arm.name);
  if (existsSync(files.body) && existsSync(files.record)) {
    return {
      body: JSON.parse(readFileSync(files.body, "utf8")) as MessagesBody,
      record: JSON.parse(readFileSync(files.record, "utf8")) as CaptureRecord,
      bodyFile: files.body,
      recordFile: files.record,
    };
  }
  const started = performance.now();
  const model = qualified(c.model);
  const hintModel = qualified(options.hintModel ?? c.model);
  const version = await ompVersion(options.omp);
  // One directory per case, whatever the arm, so every path Oh My Pi shows is the same in each.
  const root = path.join(options.scratchDir, cacheKey(c.id).slice(0, 16));
  const contextStarted = performance.now();
  const context = await buildContext(c, arm, {
    source: options.source,
    cacheDir: options.cacheDir,
    root,
  });
  const contextMs = Math.round(performance.now() - contextStarted);
  const home = path.join(root, "home");
  const profileDir = path.join(home, ".omp", "profiles", PROFILE);
  const agentDir = path.join(profileDir, "agent");
  rmSync(profileDir, { recursive: true, force: true });
  mkdirSync(agentDir, { recursive: true });
  // Hardlinks to the binary's one copy of its natives (omp-natives.ts), made again for each arm.
  rmSync(path.join(home, ".omp", "natives"), { recursive: true, force: true });
  await linkOmpNatives(options.omp, home);
  const messagesFile = path.join(root, "messages.json");
  await writeWhole(messagesFile, JSON.stringify(arm.messages));

  // Each listed skill's hint, from the cache where it holds one. A hidden skill is not listed, so
  // Oh My Pi writes it no hint.
  const hintDir = path.join(options.cacheDir, "hints");
  const listed = context.skills.filter((skill) => !skill.hidden);
  const hints = listed.map((skill) => {
    const key = cacheKey(version, skill.name, skill.description);
    const file = path.join(hintDir, `${key}.json`);
    const cached = existsSync(file)
      ? (JSON.parse(readFileSync(file, "utf8")) as CachedHint)
      : undefined;
    return { skill, key, file, cached, written: false };
  });
  const database = path.join(agentDir, "skill-descriptions.db");
  const seeded = new Set<string>();
  {
    const db = new Database(database, { create: true });
    try {
      db.run(
        "CREATE TABLE IF NOT EXISTS skill_descriptions (key TEXT PRIMARY KEY, description TEXT NOT NULL)"
      );
      const put = db.prepare("INSERT OR REPLACE INTO skill_descriptions VALUES (?, ?)");
      for (const { cached } of hints) {
        if (cached === undefined) continue;
        put.run(cached.ompKey, cached.hint);
        seeded.add(cached.ompKey);
      }
      put.finalize();
    } finally {
      db.close();
    }
  }

  // What the recorder does with each request, by the run it is in.
  const wanted = new Set(hints.filter((h) => h.cached === undefined).map((h) => h.skill.name));
  const written = new Map<string, HintReply>();
  const allWritten = Promise.withResolvers<void>();
  if (wanted.size === 0) allWritten.resolve();
  const hintCalls: CaptureRecord["hintCalls"][number][] = [];
  const otherRequests: CaptureRecord["otherRequests"][number][] = [];
  const turns: { readonly body: string; readonly headers: Record<string, string> }[] = [];
  let hintRun = false;
  const route = async (request: RecordedRequest): Promise<Response | undefined> => {
    // The stand-in daemon: a made-up registration and its ready, and an empty turn outcome.
    // Every other route of every endpoint is refused.
    if (request.path === "/legion/v1/claims/register" && pane !== undefined) {
      return Response.json({
        claimToken: `capture-${issue.toLowerCase()}-${pane.role}`,
        tree: CAPTURE_TREE,
        issue,
        role: pane.role,
        generation: 1,
        secret: randomBytes(18).toString("base64url"),
        promptAgents: [],
      });
    }
    if (
      request.path === "/legion/v1/claims/ready" ||
      request.path === "/legion/v1/claims/turn-outcome"
    ) {
      return new Response(null, { status: 204 });
    }
    if (request.method !== "POST" || !request.path.startsWith("/v1/messages")) {
      return new Response("refused by the capture's stand-in", { status: 503 });
    }
    const body = JSON.parse(request.body) as MessagesBody;
    const skill = hintSkill(body);
    if (body.tools !== undefined || body.system !== undefined) {
      if (!hintRun) {
        turns.push({ body: request.body, headers: anthropicHeaders(request.headers) });
        return undefined;
      }
      // The hint run's turn waits for the hints it is owed, then a moment for Oh My Pi to store
      // the last one before the run ends.
      await Promise.race([allWritten.promise, Bun.sleep(HINT_WAIT_MS)]);
      await Bun.sleep(500);
      return undefined;
    }
    if (hintRun && skill !== undefined && wanted.has(skill)) {
      try {
        const reply = await options.hints(body, anthropicHeaders(request.headers));
        hintCalls.push({ skill, model: reply.model, usage: reply.usage });
        written.set(skill, reply);
        if ([...wanted].every((name) => written.has(name))) allWritten.resolve();
        return sseResponse(
          messageSse({
            id: `msg_hint_${hintCalls.length}`,
            model: reply.model,
            content: [{ type: "text", text: reply.text }],
            stopReason: "end_turn",
          })
        );
      } catch (error) {
        return new Response(`the hint could not be written: ${messageFor(error)}`, {
          status: 502,
        });
      }
    }
    otherRequests.push({ path: request.path, bytes: request.body.length });
    return new Response("the recorder answers only the turn and the hints it is owed", {
      status: 400,
    });
  };

  const recorder = startRecorder({ route });
  // The NATS servers the Envoy plugin dials: a sink that counts each connection and ends it.
  let natsConnections = 0;
  const nats = Bun.listen({
    hostname: "127.0.0.1",
    port: 0,
    socket: {
      open(socket) {
        natsConnections += 1;
        socket.end();
      },
      data() {},
    },
  });
  const endpoints = {
    LEGION_DAEMON_URL: recorder.url,
    ENVOY_URL: `${recorder.url}/envoy`,
    ENVOY_NATS_URL: `nats://127.0.0.1:${nats.port}`,
  };
  // The pane environment a Legion role's Oh My Pi has, each value made up for this capture.
  const paneEnvironment: Record<string, string> =
    pane === undefined
      ? {}
      : {
          LEGION_ROLE: pane.role,
          LEGION_TREE: CAPTURE_TREE,
          LEGION_ISSUE: issue,
          LEGION_STATE_DIR: path.join(root, "legion-state"),
          LEGION_BOOT_TOKEN: randomBytes(18).toString("base64url"),
          DISPATCH_SESSION_ID: "skill-cuts-capture",
          ...endpoints,
        };
  let stderr = "";
  let runs = 0;
  let exit: CaptureRecord["exit"] = { code: 0, signal: null, reason: "" };
  let hookLog = "";
  /** One Oh My Pi run. The hint run holds no pane environment: the skill listing does not depend
   *  on it, and its role claim would end the run before the hints are written. */
  const runOmp = async (withPane: boolean): Promise<number> => {
    const runStarted = performance.now();
    runs += 1;
    const tmp = path.join(root, "tmp");
    const sessions = path.join(root, "sessions");
    rmSync(sessions, { recursive: true, force: true });
    for (const dir of [tmp, sessions]) mkdirSync(dir, { recursive: true });
    const out = path.join(root, `omp-${runs}.out.jsonl`);
    const err = path.join(root, `omp-${runs}.err.txt`);
    hookLog = path.join(root, `hook-${runs}.jsonl`);
    const child = Bun.spawn(
      [
        options.omp,
        "--profile",
        PROFILE,
        "-p",
        "--mode",
        "json",
        // A Legion pane refuses a session with no transcript on disk; a scratch one serves.
        "--session-dir",
        sessions,
        "--model",
        model,
        "--thinking",
        c.thinking,
        "--hook",
        HOOK,
        ...context.extensions.flatMap((dir) => ["--extension", dir]),
        ...(context.appendSystemPrompt === undefined
          ? []
          : ["--append-system-prompt", context.appendSystemPrompt]),
        PLACEHOLDER,
      ],
      {
        cwd: context.workspace,
        env: {
          HOME: home,
          PATH: "/usr/local/bin:/usr/bin:/bin",
          TMPDIR: tmp,
          TZ: "UTC",
          PI_NO_TITLE: "1",
          [MESSAGES_VARIABLE]: messagesFile,
          [HOOK_LOG_VARIABLE]: hookLog,
          ...(options.configFiles === undefined || options.configFiles.length === 0
            ? {}
            : { PI_CONFIG_FILES: options.configFiles.join(":") }),
          ...(withPane ? paneEnvironment : {}),
        },
        stdin: "ignore",
        stdout: Bun.file(out),
        stderr: Bun.file(err),
      }
    );
    const timeoutMs = options.timeoutMs ?? RUN_TIMEOUT_MS;
    const timer = setTimeout(() => child.kill("SIGKILL"), timeoutMs);
    const code = await child.exited;
    clearTimeout(timer);
    stderr = existsSync(err) ? readFileSync(err, "utf8") : "";
    const ended = `${child.signalCode ?? `exit ${code}`}${child.signalCode === "SIGKILL" ? ` after ${timeoutMs} ms` : ""}`;
    const tail = stderr.trim().split("\n").slice(-6).join("\n");
    exit = {
      code: child.signalCode === null ? code : null,
      signal: child.signalCode,
      reason: code === 0 ? "" : tail,
    };
    // A run that ended after the recorder stored the turn's request has given that request.
    if (code !== 0 && (hintRun || turns.length === 0)) {
      throw new RunEnded(`case ${c.id} arm ${arm.name}: Oh My Pi ended with ${ended}: ${tail}`);
    }
    return Math.round(performance.now() - runStarted);
  };

  try {
    writeProfile(agentDir, {
      url: recorder.url,
      models: options.models,
      model,
      hintModel,
      skillsDir: context.skillsDir,
    });
    let hintRunMs = 0;
    if (wanted.size > 0) {
      hintRun = true;
      hintRunMs = await runOmp(false);
      hintRun = false;
      const db = new Database(database);
      let rows: { key: string; description: string }[];
      try {
        rows = db.query("SELECT key, description FROM skill_descriptions").all() as typeof rows;
      } finally {
        db.close();
      }
      const taken = new Set(seeded);
      for (const entry of hints) {
        if (entry.cached !== undefined) continue;
        const reply = written.get(entry.skill.name);
        if (reply === undefined) {
          throw new Error(
            `case ${c.id} arm ${arm.name}: Oh My Pi asked for no hint of skill ${entry.skill.name} within ${HINT_WAIT_MS} ms`
          );
        }
        const hint = reply.text.trim();
        const row = rows.find((r) => r.description === hint && !taken.has(r.key));
        if (hint === "" || /[\r\n]/.test(hint) || hint.length > MAX_HINT_CHARS || !row) {
          throw new Error(
            `case ${c.id} arm ${arm.name}: Oh My Pi kept no hint of skill ${entry.skill.name}: the model wrote ${JSON.stringify(reply.text)}, and Oh My Pi keeps one line of at most ${MAX_HINT_CHARS} characters`
          );
        }
        taken.add(row.key);
        const cached: CachedHint = {
          omp: version,
          name: entry.skill.name,
          description: entry.skill.description,
          ompKey: row.key,
          hint,
          model: reply.model,
          writtenAt: new Date().toISOString(),
        };
        await writeWhole(entry.file, `${JSON.stringify(cached, null, 2)}\n`);
        entry.cached = cached;
        entry.written = true;
      }
    }
    // A Legion pane's run can end before its turn's request (its role claim exits the process
    // when it fails), so such a run is made again, at most CAPTURE_TRIES times in all.
    let captureRunMs = 0;
    for (let attempt = 1; ; attempt++) {
      try {
        captureRunMs = await runOmp(true);
        break;
      } catch (error) {
        if (!(error instanceof RunEnded) || pane === undefined || attempt >= CAPTURE_TRIES) {
          throw error;
        }
      }
    }
    const turn = turns[0];
    if (turn === undefined) {
      throw new Error(`case ${c.id} arm ${arm.name}: Oh My Pi sent no model request for the turn`);
    }
    const body = JSON.parse(turn.body) as MessagesBody;

    // The listing must be the installed skills Oh My Pi lists, each under its hint.
    const listing = skillListing(body);
    const faults: string[] = [];
    for (const { skill, cached } of hints) {
      const shown = listing.get(skill.name);
      if (shown === undefined) faults.push(`it does not list ${skill.name}`);
      else if (shown !== cached?.hint) {
        faults.push(`it lists ${skill.name} as ${JSON.stringify(shown)}, not under its hint`);
      }
    }
    const names = new Set(listed.map((skill) => skill.name));
    for (const name of listing.keys()) {
      if (!names.has(name)) faults.push(`it lists ${name}, which the profile does not list`);
    }
    if (faults.length > 0) {
      throw new Error(
        `case ${c.id} arm ${arm.name}: the captured body is refused: ${faults.join("; ")}`
      );
    }

    const record: CaptureRecord = {
      case: c.id,
      arm: arm.name,
      skill: c.skill,
      capturedAt: new Date().toISOString(),
      omp: { binary: options.omp, version },
      model,
      thinking: c.thinking,
      hintModel,
      configFiles: options.configFiles ?? [],
      requestHeaders: turn.headers,
      sessionDigest: c.sessionDigest,
      captureDigest: promptDigest(body),
      scratch: root,
      listing: [...listing].map(([name, hint]) => ({ name, hint })),
      hints: hints.map(({ skill, key, cached, written: was }) => ({
        skill: skill.name,
        hint: cached?.hint ?? "",
        key,
        ompKey: cached?.ompKey ?? "",
        written: was,
      })),
      hintCalls,
      otherRequests,
      extensionErrors: extensionErrors(profileDir, stderr),
      context: context.record,
      legion:
        pane === undefined
          ? null
          : {
              role: pane.role,
              tree: CAPTURE_TREE,
              issue,
              endpoints,
              dispatch:
                "off: its read of people's answers on each request would put its failure into the request",
            },
      connections: {
        http: recorder.requests.map((request) => `${request.method} ${request.path.split("?")[0]}`),
        nats: natsConnections,
      },
      pluginTools: pluginTools(body, context.record, pane !== undefined, role, stderr),
      hookDropped: hookCounts(hookLog),
      exit,
      runs,
      timings: {
        contextMs,
        hintRunMs,
        captureRunMs,
        totalMs: Math.round(performance.now() - started),
      },
    };
    await writeWhole(files.body, turn.body);
    await writeWhole(files.record, `${JSON.stringify(record, null, 2)}\n`);
    return { body, record, bodyFile: files.body, recordFile: files.record };
  } finally {
    await recorder.close();
    nats.stop(true);
  }
}
