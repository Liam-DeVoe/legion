/**
 * The builder of role-line cases (R8's arm): `skill-cuts build-role-cases` turns sessions of one
 * Legion role into cases of one line of that role's prompt (cases.ts's `rolePromptLine`), which
 * `behaviour` runs with `--old-role-prompt-at` and `--new-role-prompt-at` (cli.ts, "Role prompts
 * at commits"). It takes build-cases' store, seed, copies and count options and its `case` reader,
 * and reuses what build.ts exports; build.ts is not changed for it.
 *
 *   bun scripts/skill-cuts/cli.ts build-role-cases --config <skill-cuts.json> --key-file <file>
 *       --cut <id> --skill <name> --old <skill dir> --role <role> --line <text>
 *       --old-role-prompt-at <commit> --sessions <file> --seed <n> --copies <dir> --count <dir>
 *       --out <cases.jsonl> [--max <n>] [--budget-usd <n>] [--repos <dir>] [--cache-dir <dir>]
 *
 * `--sessions` is build-cases' sessions file (`{"store", "hash", "path"?}` a line; a line without
 * `path` is resolved with sessions.sql), its candidates taken in judge.ts's `blindOrder` under
 * `--seed`, until `--max` cases (every candidate without it). `--old` is the directory of the
 * skill the role loads first (`legion-worker` for a worker role, `legion-retro` for a retro
 * session) at the base, `--cut` the cut whose repository ships it, `--line` the role line (one
 * line of a prompt part, compared trimmed), and `--old-role-prompt-at` the base commit the cases'
 * lines are numbered at, the one `behaviour --old-role-prompt-at` checks them against. Each
 * session, one at a time:
 *
 * 1. Its transcript is pulled into `<out>.spool/` (build.ts's `pullTranscript`) and read
 *    (`readTranscript`): a session of a store whose context is no Legion project's, a subagent's
 *    (its `session_init` line), one no form gives a role (build.ts's `sessionRole`) or one of
 *    another role than `--role` is dropped. The messages a case can be built from are the
 *    branch's, from its first, up to its first compaction or context reset.
 * 2. A session none of whose models there the gateway serves is dropped (`model-not-served`).
 * 3. Its profile is built as build-cases builds a case's (`build-cases --reprofile` through
 *    build.ts's `main`, with `--config`, `--old`, `--copies` and `--count`), for a draft case at
 *    its first assistant message: so its evidence names the daemon image's commit.
 * 4. Its role prompt is composed as a capture composes it (context.ts's `composedRolePrompt`: at
 *    the daemon image's commit, else at the plugin's); a session whose prompt holds no line equal
 *    to `--line` is dropped as `line-absent`, before any reader call. The line's part is the first
 *    part of the prompt that holds it; a part that does not hold it at `--old-role-prompt-at` is
 *    dropped as `line-not-at-base`.
 * 5. The `case` reader (readers.ts) is offered that one line, under its number in the part at the
 *    base, and reads the session window by window from its first message, as build-cases reads
 *    one from the skill's read: each window at most READER_MESSAGES messages and READER_CHARS
 *    characters, with the READER_EARLIER_MESSAGES (READER_EARLIER_CHARS) before it, at most
 *    READER_WINDOWS windows, until it proposes a step. A session it finds no step in, that it
 *    reads as material, whose step's line is not the one offered, or before whose step it lists a
 *    message that states the line is dropped.
 * 6. The case: the messages before the step, `realStep` and `violatingStep` as build-cases writes
 *    them, the model and thinking level before the step, `skillReads` the read results of
 *    `skill://<skill>` files before the step (cases.ts's `skillReadFile`), its secrets taken out
 *    (build.ts's `removeSecrets`), its profile built again for the step (as in 3), `baseFile` and
 *    `rolePromptLine.part` the part, `line` the part's line at the base with its number and the
 *    part's line count, and `source` the store and the SHA-256 of the transcript's path. Its
 *    `evidence` adds `role prompt line`: the commit the session's prompt was composed at and the
 *    part's line there and at the base. Each case is appended to `--out` whole.
 *
 * `<out>.summary.json` holds the inputs, the order, each session's record (its outcome, its drop
 * and the reason, its windows and the reader's dollars on it), the drops by reason as build-cases
 * counts them, and the gateway's spend. The case file is loaded at the end as `behaviour` loads it
 * (`loadCasesAt` at `--old-role-prompt-at`). An `--out` that exists is refused: a build does not
 * go on from another. Exit status: 0 written; 1 failed; 2 refused before any store read or model
 * call; 3 stopped at `--budget-usd`, with the cases built so far written.
 */
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { messageFor } from "@legion/envoy-client/errors";
import { outRefusal } from "../skill-scenarios/production-dispatch";
import { readTargets, type SessionEntry, type SessionMessage } from "../skill-scenarios/transcript";
import { modelId, sessionModel } from "./attribute";
import {
  main as buildMain,
  type BuildIo,
  DROP_REASONS,
  type Evidence,
  fileHash,
  pullTranscript,
  READER_CHARS,
  READER_EARLIER_CHARS,
  READER_EARLIER_MESSAGES,
  READER_MESSAGES,
  READER_WINDOWS,
  readTranscript,
  removeSecrets,
  sessionInit,
  sessionRole,
} from "./build";
import {
  Case,
  loadCasesAt,
  pathHash,
  type Profile,
  skillReadFile,
  Thinking,
  textLines,
} from "./cases";
import { configFrom, json, parse, Refused, required, writeWhole } from "./common";
import { type SkillCutsConfig, storeNamed } from "./config";
import {
  type ContextCase,
  composedRolePrompt,
  ghSource,
  localSource,
  type RepoSource,
  rolePromptParts,
} from "./context";
import { sessionsSql } from "./countsql";
import { BudgetSpent, createGateway, type Gateway, usdOf } from "./gateway";
import { blindOrder, cut, MESSAGE_CHARS, renderMessage } from "./judge";
import { psqlRows } from "./psql-rows";
import { CaseAnswer, type CaseInput, ReaderFailed, runReader } from "./readers";

const USAGE =
  "usage: cli.ts build-role-cases --config <skill-cuts.json> --key-file <file> --cut <id> --skill <name> --old <dir> --role <role> --line <text> --old-role-prompt-at <commit> --sessions <file> --seed <n> --copies <dir> --count <dir> --out <cases.jsonl> [--max <n>] [--budget-usd <n>] [--repos <dir>] [--cache-dir <dir>]";

/** The exit statuses (the file's header). */
const EXIT = { ok: 0, failed: 1, refused: 2, stopped: 3 } as const;

/** The longest one reader call may take: build-cases' own limit. */
const READER_TIMEOUT_MS = 600_000;

/** Why a session gives no role-line case: build-cases' reasons where the check is its own
 *  (DROP_REASONS), and the role line's. */
export const ROLE_DROP_REASONS = {
  "not-in-store": DROP_REASONS["not-in-store"],
  "pull-failed": DROP_REASONS["pull-failed"],
  unparseable: DROP_REASONS.unparseable,
  "no-header": DROP_REASONS["no-header"],
  "not-legion": "the session's store holds no Legion project's sessions, so it has no role prompt",
  subagent:
    "the session is a subagent's (its transcript has a session_init line): it runs no role prompt",
  "no-role": DROP_REASONS["no-role"],
  "other-role": "the session is of another role than --role",
  "no-model": DROP_REASONS["no-model"],
  "model-not-served":
    "no route of the gateway serves the session's model before the step, or none of its models before its first compaction",
  "profile-failed": "build-cases --reprofile gave the session no profile",
  "compose-failed":
    "the session's role prompt could not be composed: a read of its plugin repository, or Compose, failed",
  "line-absent":
    "the session's role prompt, composed at its daemon image's commit (else its plugin's), holds no line equal to --line",
  "line-not-at-base":
    "the prompt part that holds the line at the session's commit does not hold it at --old-role-prompt-at",
  "reader-failed": DROP_REASONS["reader-failed"],
  "reader-error": DROP_REASONS["reader-error"],
  "no-step": "the case reader found no step the role line governs",
  "prompt-as-material":
    "the case reader finds that the agent's work is on the role prompt's own text, so its next step does not follow the line",
  "line-not-offered": "the case reader's step names another line than the one offered",
  "history-restates-line":
    "the case reader finds a message before the step that states the line's instruction",
} as const;
export type RoleDropReason = keyof typeof ROLE_DROP_REASONS;

/** One candidate of the sessions file (build-cases' form). */
interface Candidate {
  readonly store: string;
  readonly hash: string;
  readonly path?: string;
}

/** What the profile step is given beside a draft case: build-cases' inputs, and a directory of
 *  its own. */
export interface ProfileInputs {
  readonly config: string;
  readonly old: string;
  readonly copies: string;
  readonly count: string;
  readonly dir: string;
}

/** A session's profile and its evidence, or why it has none. */
export type ProfileOutcome =
  | { readonly profile: Profile; readonly evidence: readonly Evidence[] }
  | { readonly failed: string };

/** What build-role-cases uses of the world: build.ts's io, and the profile step, which is
 *  `build-cases --reprofile` (`reprofiled`) when absent. */
export interface RoleCasesIo extends BuildIo {
  readonly profile?: (draft: Case, inputs: ProfileInputs) => Promise<ProfileOutcome>;
}

/** The profile of the draft case `draft` as `build-cases --reprofile` builds it, through build.ts's
 *  `main` with `io`: the draft is written to `inputs.dir`, and its profile and evidence read back.
 *  The draft is a skill case, as `--reprofile` loads one: its line a line of `--old`'s SKILL.md
 *  (`draftCase`). */
async function reprofiled(
  draft: Case,
  inputs: ProfileInputs,
  io: BuildIo
): Promise<ProfileOutcome> {
  mkdirSync(inputs.dir, { recursive: true });
  const from = path.join(inputs.dir, `${encodeURIComponent(draft.id)}.draft.jsonl`);
  const to = path.join(inputs.dir, `${encodeURIComponent(draft.id)}.profiled.jsonl`);
  rmSync(to, { force: true });
  writeFileSync(from, `${JSON.stringify(draft)}\n`);
  const said: string[] = [];
  const code = await buildMain(
    [
      ...["build-cases", "--config", inputs.config, "--old", inputs.old],
      ...["--copies", inputs.copies, "--count", inputs.count, "--reprofile", from, "--out", to],
    ],
    { ...io, out: (text) => said.push(text), err: (line) => said.push(line) }
  );
  if (code !== EXIT.ok || !existsSync(to)) {
    return { failed: `build-cases --reprofile exited ${code}: ${said.join(" ").trim()}` };
  }
  const built = Case.parse(JSON.parse(readFileSync(to, "utf8")));
  return { profile: built.profile, evidence: built.evidence ?? [] };
}

/** The fields whose strings the secret scan leaves alone: ids, and the signatures a replay must
 *  send back byte for byte (build.ts's own list). */
const UNSCANNED: Readonly<Record<string, true>> = {
  id: true,
  toolCallId: true,
  responseId: true,
  thinkingSignature: true,
  signature: true,
};

/** `value` with every secret of its strings taken out (build.ts's `removeSecrets`), the fields of
 *  UNSCANNED left alone, and how many it took out. */
function scrubbed<T>(value: T): { value: T; removed: number } {
  let removed = 0;
  const walk = (node: unknown): unknown => {
    if (typeof node === "string") {
      const result = removeSecrets(node);
      removed += result.removed;
      return result.text;
    }
    if (Array.isArray(node)) return node.map(walk);
    if (typeof node === "object" && node !== null) {
      return Object.fromEntries(
        Object.entries(node).map(([key, each]) => [
          key,
          Object.hasOwn(UNSCANNED, key) ? each : walk(each),
        ])
      );
    }
    return node;
  };
  return { value: walk(value) as T, removed };
}

/** The candidates of the sessions file `file`, each once. Refuses a line that is not one, or that
 *  names a store the configuration does not name. */
function readCandidates(file: string, config: SkillCutsConfig): Candidate[] {
  let text: string;
  try {
    text = readFileSync(file, "utf8");
  } catch (error) {
    throw new Refused(`cannot read --sessions ${file}: ${messageFor(error)}`);
  }
  const seen = new Set<string>();
  const candidates: Candidate[] = [];
  for (const [index, line] of text.split("\n").entries()) {
    if (line.trim() === "") continue;
    let value: unknown;
    try {
      value = JSON.parse(line);
    } catch (error) {
      throw new Refused(`${file}:${index + 1} is not JSON: ${messageFor(error)}`);
    }
    const store = Reflect.get(Object(value), "store");
    const hash = Reflect.get(Object(value), "hash");
    const where = Reflect.get(Object(value), "path");
    if (
      typeof store !== "string" ||
      typeof hash !== "string" ||
      !/^[0-9a-f]{16}$/.test(hash) ||
      (where !== undefined && (typeof where !== "string" || fileHash(where) !== hash))
    ) {
      throw new Refused(`${file}:${index + 1} is not {"store", "hash", "path"?} of one file`);
    }
    if (!config.stores.some((each) => each.name === store)) {
      throw new Refused(
        `${file}:${index + 1} names the store ${store}, which the configuration does not name`
      );
    }
    if (seen.has(`${store}\0${hash}`)) continue;
    seen.add(`${store}\0${hash}`);
    candidates.push({ store, hash, ...(where === undefined ? {} : { path: where }) });
  }
  return candidates;
}

/** The path of each file of `hashes` in the store `db`, by hash, in one sessions.sql query through
 *  the store command `command`. */
async function resolvePaths(
  command: string,
  db: string,
  hashes: readonly string[]
): Promise<Map<string, string>> {
  const paths = new Map<string, string>();
  if (hashes.length === 0) return paths;
  const child = Bun.spawn([command, db], {
    stdin: new Blob([sessionsSql(hashes)]),
    stdout: "pipe",
    stderr: "pipe",
  });
  const [stdout, stderr, code] = await Promise.all([
    new Response(child.stdout).text(),
    new Response(child.stderr).text(),
    child.exited,
  ]);
  if (code !== 0) throw new Error(`the store command exited ${code}: ${stderr.trim()}`);
  for (const row of psqlRows(stdout)) {
    const where = Reflect.get(Object(row), "path");
    const hash = Reflect.get(Object(row), "hash");
    if (typeof where === "string" && typeof hash === "string") paths.set(hash, where);
  }
  return paths;
}

/** The last value `pick` reads from an entry of `branch` before `at`. */
function lastBefore<T>(
  branch: readonly SessionEntry[],
  at: number,
  pick: (entry: SessionEntry) => T | undefined
): T | undefined {
  for (let index = at - 1; index >= 0; index -= 1) {
    const entry = branch[index];
    const value = entry === undefined ? undefined : pick(entry);
    if (value !== undefined) return value;
  }
  return undefined;
}

/** The model a `model_change` entry names. */
const modelChange = (entry: SessionEntry) =>
  entry.type === "model_change" && typeof entry.model === "string" ? entry.model : undefined;

/** Whether a route of the gateway serves `model` (attribute.ts's `sessionModel`, by its id). */
const servedBy = (model: string, servedIds: ReadonlySet<string>) =>
  sessionModel({ model, source: "model_change" }, servedIds)?.served === true;

/** One session's record in the summary. */
interface SessionRecord {
  store: string;
  hash: string;
  pathHash?: string;
  outcome: "case" | "dropped" | "not-needed";
  reason?: RoleDropReason;
  detail?: string;
  windows?: number;
  readerUsd: number;
}

/** What one session's build reads beside the session. */
interface RoleRun {
  readonly config: SkillCutsConfig;
  readonly cut: { readonly id: string; readonly repo: string };
  readonly skill: string;
  readonly role: string;
  readonly line: string;
  readonly base: string;
  readonly skillLine: Case["line"];
  readonly gateway: Gateway;
  readonly source: RepoSource;
  readonly cacheDir: string;
  readonly servedIds: ReadonlySet<string>;
  readonly profile: (draft: Case) => Promise<ProfileOutcome>;
}

/** A dropped session's outcome. */
class Dropped extends Error {
  constructor(
    readonly reason: RoleDropReason,
    readonly detail: string
  ) {
    super(detail);
  }
}

/** A draft case of the session, as `build-cases --reprofile` loads one to build its profile
 *  (`reprofiled`): the fields `c` at a step, with a skill case's line, a line of `--old`'s SKILL.md
 *  (`run.skillLine`), and a profile that installs the skill alone, both of which the profile step
 *  replaces. */
function draftCase(run: RoleRun, c: StepFields, model: string): Case {
  return Case.parse({
    ...c,
    model: c.model ?? model,
    thinking: "off",
    baseFile: "SKILL.md",
    line: run.skillLine,
    criterion: { pass: "a draft for its profile", fail: "a draft for its profile" },
    violatingStep: {
      role: "assistant",
      content: [{ type: "text", text: "a draft for its profile" }],
    },
    profile: {
      skills: [{ repo: run.cut.repo, commit: run.base, path: ".", name: run.skill }],
      plugins: [],
      contextFiles: [],
    },
  });
}

/** A case's fields that its step gives (`roleCaseOf`): its messages are those before the step,
 *  `model` the last `model_change` before it (undefined: none). */
interface StepFields {
  readonly id: string;
  readonly cut: string;
  readonly skill: string;
  readonly model: string | undefined;
  readonly context: string;
  readonly messages: SessionMessage[];
  readonly skillReads: number[];
  readonly realStep: SessionMessage;
  readonly source: Case["source"];
  readonly sessionStart: string;
  readonly secretsRemoved: number;
  readonly sessionDigest: string | null;
  readonly evidence: Evidence[];
}

/** Where the role line stands, for a session whose profile is `c`'s (`locateLine`): the commit
 *  its prompt was composed at, as the evidence says it, the part that holds the line, and that
 *  part at the base, with the line's number in it and its text there. */
interface LineAt {
  readonly composedAt: string;
  readonly part: string;
  readonly baseLines: readonly string[];
  readonly lineNumber: number;
  readonly baseLine: string;
}

/** Where `run.line` stands in the role prompt of `c`, composed as a capture composes it
 *  (context.ts's `composedRolePrompt`): the first part that holds it, read at the commit the
 *  prompt was composed at, and that part at the base (`run.base`). Throws Dropped: `line-absent`
 *  when the composed prompt holds no such line, `line-not-at-base` when the part at the base does
 *  not hold it, and `compose-failed` when a read or Compose fails. */
async function locateLine(run: RoleRun, c: ContextCase): Promise<LineAt> {
  const wanted = run.line.trim();
  try {
    const composed = await composedRolePrompt(c, { source: run.source, cacheDir: run.cacheDir });
    const made = composed.record;
    if (made === null || made === "case") {
      throw new Dropped("line-absent", `its profile composes no role prompt (${String(made)})`);
    }
    const composedAt = `${made.commit} (the ${made.commitOf}'s commit of ${made.repo})`;
    if (!composed.text.split("\n").some((each) => each.trim() === wanted)) {
      throw new Dropped(
        "line-absent",
        `the ${run.role}'s role prompt composed at ${composedAt} holds no line ${JSON.stringify(run.line)}`
      );
    }
    let part: string | undefined;
    for (const each of made.parts) {
      const text = new TextDecoder().decode(await run.source.file(made.repo, made.commit, each));
      if (textLines(text).some((line) => line.trim() === wanted)) {
        part = each;
        break;
      }
    }
    if (part === undefined) {
      throw new Dropped("line-absent", `no part of the prompt composed at ${composedAt} holds it`);
    }
    const atBase = await rolePromptParts(run.base, run.source).part(c, part);
    if (typeof atBase === "string" || atBase.text === undefined) {
      throw new Dropped(
        "line-not-at-base",
        typeof atBase === "string" ? atBase : `${atBase.where} does not exist`
      );
    }
    const baseLines = textLines(atBase.text);
    const lineNumber = baseLines.findIndex((line) => line.trim() === wanted) + 1;
    if (lineNumber === 0) {
      throw new Dropped(
        "line-not-at-base",
        `${atBase.where} holds no line ${JSON.stringify(run.line)}`
      );
    }
    return { composedAt, part, baseLines, lineNumber, baseLine: baseLines[lineNumber - 1] ?? "" };
  } catch (error) {
    if (error instanceof Dropped) throw error;
    throw new Dropped("compose-failed", messageFor(error));
  }
}

/** The role-line case of the session of `candidate`, whose transcript is pulled into `spool`, or a
 *  throw of Dropped naming why it gives none. Adds what the reader spent to `record`. */
async function roleCaseOf(
  run: RoleRun,
  candidate: { readonly store: string; readonly path: string },
  spool: string,
  record: SessionRecord
): Promise<Case> {
  const store = storeNamed(run.config, candidate.store);
  if (!store.context.startsWith("legion:")) {
    throw new Dropped("not-legion", `the store ${store.name}'s context is ${store.context}`);
  }
  const t = (() => {
    try {
      return readTranscript(spool);
    } catch (error) {
      throw new Dropped("unparseable", messageFor(error));
    }
  })();
  const header = t.entries.find((entry) => entry.type === "session");
  const sessionStart = header?.timestamp;
  if (sessionStart === undefined || Number.isNaN(Date.parse(sessionStart))) {
    throw new Dropped("no-header", "no session line with an ISO timestamp");
  }
  if (sessionInit(t.entries) !== undefined) {
    throw new Dropped("subagent", "its transcript has a session_init line");
  }
  // sessionRole reads the first user message, the first custom message and each Envoy message
  // whole (as build-cases reads them).
  const all = t.items.map((item) => item.slim);
  const firstUser = all.findIndex((message) => message.role === "user");
  const firstCustom = all.findIndex((message) => message.role === "custom");
  const roleMessages = all.map((message, index) =>
    index === firstUser ||
    index === firstCustom ||
    (message.role === "custom" && message.customType === "envoy-message")
      ? t.message(index)
      : message
  );
  const found = sessionRole(t.branch, roleMessages, candidate.path);
  if ("detail" in found) throw new Dropped("no-role", found.detail);
  if (found.role !== run.role) {
    throw new Dropped("other-role", `its role is ${found.role}, not ${run.role}`);
  }
  const context = `${store.context}:${found.role}`;
  const roleEvidence: Evidence = { item: `role ${found.role}`, source: found.how };

  // The messages a case can be built from: the branch's, to its first compaction or reset.
  const boundary = t.branch.findIndex(
    (entry) => entry.type === "compaction" || entry.type === "reset_boundary"
  );
  const readable = t.items.filter((item) => boundary < 0 || item.at < boundary);
  const models = t.branch.slice(0, boundary < 0 ? undefined : boundary).flatMap((entry) => {
    const model = modelChange(entry);
    return model === undefined ? [] : [model];
  });
  const [firstModel] = models;
  if (firstModel === undefined) throw new Dropped("no-model", "no model_change");
  if (!models.some((model) => servedBy(model, run.servedIds))) {
    throw new Dropped("model-not-served", [...new Set(models)].join(", "));
  }
  const firstReply = readable.findIndex((item) => item.slim.role === "assistant");
  if (firstReply < 0) {
    throw new Dropped("no-step", "no assistant message before its first compaction");
  }

  const hash = pathHash(candidate.path);
  const id = `${run.cut.id}-role-${hash.slice(0, 12)}`;
  /** The case's fields at the step `step` (an index of `t.items`). */
  const atStep = (step: number): StepFields => {
    const messages = Array.from({ length: step }, (_, index) => t.message(index));
    const targets = readTargets(messages);
    const skillReads = messages.flatMap((message, index) =>
      message.role === "toolResult" &&
      skillReadFile(run.skill, targets.get(message.toolCallId ?? "") ?? "") !== undefined
        ? [index]
        : []
    );
    const at = t.items[step]?.at ?? 0;
    return {
      id,
      cut: run.cut.id,
      skill: run.skill,
      model: lastBefore(t.branch, at, modelChange),
      context,
      messages,
      skillReads,
      realStep: t.message(step),
      source: { store: store.name, pathHash: hash },
      sessionStart,
      secretsRemoved: 0,
      sessionDigest:
        lastBefore(t.branch, at, (entry) =>
          entry.type === "custom" &&
          entry.customType === "system-prompt-digest" &&
          typeof entry.data === "string"
            ? entry.data
            : undefined
        ) ?? null,
      evidence: [roleEvidence],
    };
  };

  // The profile at the session's first reply, for the commit its role prompt was composed at,
  // and where the line stands in that prompt, before any reader reads the session.
  const first = await run.profile(draftCase(run, atStep(firstReply), firstModel));
  if ("failed" in first) throw new Dropped("profile-failed", first.failed);
  const { composedAt, part, baseLines, lineNumber, baseLine } = await locateLine(run, {
    id,
    context,
    profile: first.profile,
    evidence: [...first.evidence],
  });
  const wanted = run.line.trim();

  // The case reader, window by window from the session's first message.
  const shown = (index: number): CaseInput["messages"][number] => {
    const message = t.message(index);
    return {
      index,
      role: message.role ?? "message",
      text: cut(
        typeof message.summary === "string"
          ? `${renderMessage(message)}\n${message.summary}`
          : renderMessage(message),
        MESSAGE_CHARS
      ),
    };
  };
  const offered = [{ path: part, lines: [{ number: lineNumber, text: baseLine.trim() }] }];
  let answer: Extract<CaseAnswer, { line: unknown }> | undefined;
  let noStep = "";
  let start = 0;
  let windows = 0;
  while (answer === undefined && windows < READER_WINDOWS && start < readable.length) {
    windows += 1;
    record.windows = windows;
    const window: CaseInput["messages"] = [];
    let chars = 0;
    for (
      let index = start;
      index < readable.length && window.length < READER_MESSAGES;
      index += 1
    ) {
      const each = shown(index);
      if (window.length > 0 && chars + each.text.length > READER_CHARS) break;
      chars += each.text.length;
      window.push(each);
    }
    const earlier: CaseInput["messages"] = [];
    let earlierChars = 0;
    for (
      let index = start - 1;
      index >= 0 && earlier.length < READER_EARLIER_MESSAGES;
      index -= 1
    ) {
      const each = shown(index);
      if (earlier.length > 0 && earlierChars + each.text.length > READER_EARLIER_CHARS) break;
      earlierChars += each.text.length;
      earlier.unshift(each);
    }
    let given: CaseAnswer;
    try {
      const result = await runReader(
        "case",
        {
          skill: `role prompt of the ${run.role}`,
          files: offered,
          messages: [...earlier, ...window],
        },
        run.gateway
      );
      record.readerUsd += usdOf(result.spent);
      given = CaseAnswer.parse(result.answer);
    } catch (error) {
      if (error instanceof BudgetSpent) throw error;
      if (error instanceof ReaderFailed) {
        record.readerUsd += usdOf(error.spent);
        throw new Dropped("reader-failed", error.message);
      }
      throw new Dropped("reader-error", messageFor(error));
    }
    if ("material" in given) {
      throw new Dropped("prompt-as-material", `window ${windows}: ${given.material.why}`);
    }
    if (given.step === null) noStep = given.reason;
    else answer = given;
    start = (window.at(-1)?.index ?? start) + 1;
  }
  if (answer === undefined) {
    throw new Dropped("no-step", `${noStep} (${windows} windows, to message ${start - 1})`);
  }
  if (answer.line.text.trim() !== wanted) {
    throw new Dropped("line-not-offered", JSON.stringify(answer.line.text));
  }
  if (answer.restatements.length > 0) {
    const indices = [...new Set(answer.restatements.map((each) => each.messageIndex))];
    throw new Dropped(
      "history-restates-line",
      `the case reader finds the line's instruction stated in message ${indices.join(", ")}, before the step at message ${answer.step.messageIndex}`
    );
  }
  const step = answer.step.messageIndex;
  const stepAt = t.items[step]?.at ?? 0;
  const fields = atStep(step);
  const thinking = Thinking.safeParse(
    lastBefore(t.branch, stepAt, (entry) =>
      entry.type === "thinking_level_change" && typeof entry.thinkingLevel === "string"
        ? entry.thinkingLevel
        : undefined
    )
  );
  const model = fields.model;
  if (model === undefined || !thinking.success) {
    throw new Dropped(
      "no-model",
      model === undefined ? "no model_change" : "no thinking level Oh My Pi takes"
    );
  }
  if (!servedBy(model, run.servedIds)) throw new Dropped("model-not-served", model);

  // The profile again, for the step's own prompt build.
  const profiled = await run.profile(draftCase(run, fields, model));
  if ("failed" in profiled) throw new Dropped("profile-failed", profiled.failed);
  const secrets = scrubbed({
    messages: fields.messages,
    realStep: fields.realStep,
    violatingStep: {
      role: "assistant",
      content: [
        ...(answer.violatingStep.text.trim() === ""
          ? []
          : [{ type: "text", text: answer.violatingStep.text }]),
        ...answer.violatingStep.toolCalls.map((call, n) => ({
          type: "toolCall",
          id: `violating_${n + 1}`,
          name: call.name,
          arguments: call.arguments,
        })),
      ],
    } satisfies SessionMessage,
    criterion: answer.criterion,
  });
  return Case.parse({
    ...fields,
    model,
    thinking: thinking.data,
    baseFile: part,
    line: { text: baseLine, lineNumber, lineCount: baseLines.length },
    rolePromptLine: { role: run.role, part },
    criterion: secrets.value.criterion,
    profile: profiled.profile,
    messages: secrets.value.messages,
    realStep: secrets.value.realStep,
    violatingStep: secrets.value.violatingStep,
    secretsRemoved: secrets.removed,
    evidence: [
      roleEvidence,
      ...profiled.evidence.filter((each) => each.item !== roleEvidence.item),
      {
        item: "role prompt line",
        source: `the ${run.role}'s role prompt composed at ${composedAt} holds the line in its part ${part}, which holds it at --old-role-prompt-at ${run.base} as line ${lineNumber} of ${baseLines.length}`,
      },
    ],
  });
}

/** `skill-cuts build-role-cases`: the file's header. Returns the exit status. */
export async function main(argv: readonly string[], io: RoleCasesIo): Promise<number> {
  try {
    return await build(argv, io);
  } catch (error) {
    if (error instanceof Refused) {
      io.err(`build-role-cases: ${error.message}`);
      return EXIT.refused;
    }
    throw error;
  }
}

/** The build (`main`), whose refusals throw. */
async function build(argv: readonly string[], io: RoleCasesIo): Promise<number> {
  const values = parse(
    argv,
    {
      config: { type: "string" },
      "key-file": { type: "string" },
      cut: { type: "string" },
      skill: { type: "string" },
      old: { type: "string" },
      role: { type: "string" },
      line: { type: "string" },
      "old-role-prompt-at": { type: "string" },
      sessions: { type: "string" },
      seed: { type: "string" },
      copies: { type: "string" },
      count: { type: "string" },
      out: { type: "string" },
      max: { type: "string" },
      "budget-usd": { type: "string" },
      repos: { type: "string" },
      "cache-dir": { type: "string" },
    },
    USAGE
  );
  const given = required(
    values,
    [
      ...["config", "key-file", "cut", "skill", "old", "role", "line", "old-role-prompt-at"],
      ...["sessions", "seed", "copies", "count", "out"],
    ] as const,
    USAGE
  );
  const config = configFrom(given.config);
  const cutOf = config.cuts.find((each) => each.id === given.cut);
  if (cutOf === undefined) throw new Refused(`--cut ${given.cut} is no cut of the configuration`);
  if (given.line.trim() === "" || given.line.includes("\n")) {
    throw new Refused("--line is not one non-blank line");
  }
  const skillFile = path.join(given.old, "SKILL.md");
  if (!existsSync(skillFile)) throw new Refused(`--old ${given.old} holds no SKILL.md`);
  for (const dir of [given.copies, given.count]) {
    if (!existsSync(dir)) throw new Refused(`${dir} is not a directory`);
  }
  const max = values.max === undefined ? undefined : Number(values.max);
  if (max !== undefined && (!Number.isInteger(max) || max < 1)) {
    throw new Refused(`--max ${values.max} is not a whole number of 1 or more`);
  }
  const budgetUsd = values["budget-usd"] === undefined ? undefined : Number(values["budget-usd"]);
  if (budgetUsd !== undefined && (!Number.isFinite(budgetUsd) || budgetUsd < 0)) {
    throw new Refused(
      `--budget-usd ${values["budget-usd"]} is not a number of dollars of 0 or more`
    );
  }
  const spoolDir = `${given.out}.spool`;
  for (const file of [given.out, spoolDir]) {
    const refusal = outRefusal(file);
    if (refusal !== undefined) throw new Refused(refusal);
  }
  if (existsSync(given.out)) {
    throw new Refused(
      `--out ${given.out} exists: a build does not go on from another; give a new file`
    );
  }
  const candidates = readCandidates(given.sessions, config);
  // A skill line the draft cases name, so build-cases --reprofile loads them (`draftCase`).
  const skillLines = textLines(readFileSync(skillFile, "utf8"));
  const skillAt = skillLines.findIndex((line) => line.trim() !== "");
  if (skillAt < 0) throw new Refused(`${skillFile} holds no line`);
  const gateway = createGateway({
    baseUrl: config.gatewayBaseUrl,
    keyFile: given["key-file"],
    concurrency: 1,
    timeoutMs: READER_TIMEOUT_MS,
    ...(budgetUsd === undefined ? {} : { budgetUsd }),
  });
  const inputs: ProfileInputs = {
    config: given.config,
    old: given.old,
    copies: given.copies,
    count: given.count,
    dir: path.join(spoolDir, "profiles"),
  };
  const run: RoleRun = {
    config,
    cut: cutOf,
    skill: given.skill,
    role: given.role,
    line: given.line,
    base: given["old-role-prompt-at"],
    skillLine: {
      text: skillLines[skillAt] ?? "",
      lineNumber: skillAt + 1,
      lineCount: skillLines.length,
    },
    gateway,
    source: values.repos === undefined ? ghSource() : localSource(values.repos),
    cacheDir: values["cache-dir"] ?? path.join(tmpdir(), "skill-cuts-cache"),
    servedIds: new Set(config.servedModels.map(modelId)),
    profile: (draft) => (io.profile ?? ((d, i) => reprofiled(d, i, io)))(draft, inputs),
  };

  const order = blindOrder(candidates, given.seed).map(({ item }) => item);
  const records: SessionRecord[] = [];
  let built = 0;
  let stopped: string | undefined;
  mkdirSync(path.dirname(path.resolve(given.out)), { recursive: true });
  writeFileSync(given.out, "");
  mkdirSync(spoolDir, { recursive: true });
  try {
    // Each store's paths of the candidates that name none, in one sessions.sql query a store.
    const paths = new Map<string, string>();
    for (const store of new Set(
      order.filter((each) => each.path === undefined).map((each) => each.store)
    )) {
      const hashes = order
        .filter((each) => each.store === store && each.path === undefined)
        .map((each) => each.hash);
      for (const [hash, where] of await resolvePaths(config.psqlCommand, store, hashes)) {
        paths.set(`${store}\0${hash}`, where);
      }
    }
    for (const candidate of order) {
      const record: SessionRecord = {
        store: candidate.store,
        hash: candidate.hash,
        outcome: "dropped",
        readerUsd: 0,
      };
      records.push(record);
      if (stopped !== undefined || (max !== undefined && built >= max)) {
        record.outcome = "not-needed";
        continue;
      }
      const where = candidate.path ?? paths.get(`${candidate.store}\0${candidate.hash}`);
      if (where === undefined) {
        record.reason = "not-in-store";
        record.detail = DROP_REASONS["not-in-store"];
        continue;
      }
      record.pathHash = pathHash(where);
      const spool = path.join(spoolDir, `${candidate.store}-${candidate.hash}.jsonl`);
      try {
        const pulled = await pullTranscript(
          config.psqlCommand,
          candidate.store,
          where,
          spool
        ).catch((error: unknown) => {
          throw new Dropped("pull-failed", messageFor(error));
        });
        if (pulled.bytes === 0) throw new Dropped("pull-failed", "the pull returned no row");
        const c = await roleCaseOf(run, { store: candidate.store, path: where }, spool, record);
        writeFileSync(given.out, `${JSON.stringify(c)}\n`, { flag: "a" });
        record.outcome = "case";
        built += 1;
        io.out(`build-role-cases: ${c.id}: a case of ${candidate.store} ${candidate.hash}\n`);
      } catch (error) {
        if (error instanceof BudgetSpent) {
          stopped = error.message;
          record.outcome = "not-needed";
          continue;
        }
        if (!(error instanceof Dropped)) throw error;
        record.reason = error.reason;
        record.detail = error.detail;
        io.out(
          `build-role-cases: ${candidate.store} ${candidate.hash}: dropped, ${error.reason}: ${error.detail}\n`
        );
      } finally {
        rmSync(spool, { force: true });
      }
    }
  } finally {
    rmSync(spoolDir, { recursive: true, force: true });
  }
  const drops: Partial<Record<RoleDropReason, number>> = {};
  for (const record of records) {
    if (record.reason !== undefined) drops[record.reason] = (drops[record.reason] ?? 0) + 1;
  }
  writeWhole(
    `${given.out}.summary.json`,
    json({
      command: "build-role-cases",
      inputs: {
        cut: given.cut,
        skill: given.skill,
        old: given.old,
        role: given.role,
        line: given.line,
        oldRolePromptAt: given["old-role-prompt-at"],
        sessions: given.sessions,
        seed: given.seed,
        copies: given.copies,
        count: given.count,
        max: max ?? null,
      },
      order: order.map((each) => `${each.store} ${each.hash}`),
      sessions: records,
      cases: built,
      drops,
      spend: gateway.spend(),
      stopped: stopped ?? null,
    })
  );
  // The file must load as `behaviour` loads it, at the base commit.
  if (built > 0) await loadCasesAt(given.out, given.old, rolePromptParts(run.base, run.source));
  io.out(
    `build-role-cases: ${built} cases of ${records.length} sessions written to ${given.out}\n`
  );
  if (stopped !== undefined) {
    io.err(`build-role-cases: stopped: ${stopped}`);
    return EXIT.stopped;
  }
  return EXIT.ok;
}
