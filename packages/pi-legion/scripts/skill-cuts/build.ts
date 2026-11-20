/**
 * The builders of INFRA-4676's inputs (plan, R2 "Candidates" and "Cases", "The case context", and
 * R6's load test): `build-cases` turns real sessions into the case file the `behaviour` commands
 * read (cases.ts), and `build-tasks` writes the tasks file the load test reads (load.ts).
 * `main(argv, io)` runs either; `argv` starts with the command's name, so cli.ts can pass its own
 * whole. A store is read only with the configuration's `psqlCommand` (config.ts), one query a
 * call, each a SELECT of this runner's SQL (countsql.ts); every model call is a reader of
 * readers.ts through gateway.ts, signed in by `--key-file`.
 *
 *   bun scripts/skill-cuts/build.ts build-cases --config <skill-cuts.json> --key-file <file>
 *       --cut <id> --skill <name> --old <skill dir at the cut's base> --sessions <file>
 *       --seed <n> --copies <R4 out dir> --out <cases.jsonl> [--max <n>]
 *   bun scripts/skill-cuts/build.ts build-tasks --config <skill-cuts.json> --key-file <file>
 *       --skill <dir> --store <db>... --out <tasks.jsonl>
 *
 * **build-cases.** `--sessions` holds the candidate sessions, one JSON line each:
 * `{"store", "hash", "path"?}`, `hash` the file hash count.sql names a file by (16 hex characters
 * of md5 of its path). A line without `path` is resolved with sessions.sql, one query per store
 * and SESSION_BATCH hashes. The candidates are taken in the order judge.ts's `blindOrder` gives
 * them under `--seed` (the summary records the seed and the order), one case per session, until
 * `--max` cases are built. For each session:
 *
 * 1. Its transcript is pulled by path with pull.sql, PULL_PAGE parts a call, paging by offset, and
 *    joined with psql-rows.ts's `pullText`. Its lines are parsed as transcript.ts's entries; the
 *    text after the last newline, a line still being written, is left out when it does not parse.
 * 2. The session's messages are those of its current branch (the parent chain of its last entry),
 *    in order, as Oh My Pi 18.18.6 gives them to the model: each `message` (less an assistant
 *    message it leaves out: one marked `retryRecovery`, or an error with no content), each
 *    `custom_message` as a `custom` message (less the two plan-mode kinds it leaves out), each
 *    `branch_summary` as a `branchSummary` message. A string `content` is made one text block.
 * 3. The first successful read of a file of the skill (`read` of a `skill://<skill>` URL, as
 *    cases.ts's `skillReadFile` reads one) is found, and the copy its `[Skill file: <path>]` line
 *    names must map, by the prefix table of `--copies`, to the cut repository's copy of the skill.
 * 4. The case reader (`skill-cuts read --prompt case`) reads the skill's base text (`--old`) and the
 *    messages from that read's call on, bounded: at most READER_MESSAGES messages and
 *    READER_CHARS characters; and before them, nearest first, at most READER_EARLIER_MESSAGES
 *    messages and READER_EARLIER_CHARS characters of those after the last compaction or context
 *    reset before the read. Each message is cut to MESSAGE_CHARS (its start and its end kept), and
 *    each read result of the skill's files is left out with a note. It proposes the first step
 *    the skill governs, its message index, the line it exercises, the criterion, a violating step
 *    (a plausible next step that breaks the line and otherwise looks valid), and each message
 *    before the step that states the line's instruction in any wording; or no step. The session's
 *    record in the summary says how many of the messages before the step the reader read, beside
 *    how many the case holds (`restatementCheck`).
 * 5. The case: `messages` the model's messages before the step, `realStep` the real assistant
 *    message at it, `violatingStep` the reader's violating step as an assistant message (its text,
 *    then each tool call), `skillReads` the index of each read result of a `skill://<skill>` file
 *    in `messages`, `model` and `thinking` the branch's last `model_change` and
 *    `thinking_level_change` before the step, `sessionDigest` its last `system-prompt-digest`
 *    before the step, `sessionStart` the header's timestamp, `source` the store and the SHA-256 of
 *    the path (never the path), `context` the store's (`devbox`, or `legion:<project>` with the
 *    role), and `profile` (below). Where a compaction precedes the step and every read of the
 *    skill follows it, `messages` is what Oh My Pi gives the model after it: the summary as a
 *    `compactionSummary` message, the entries it kept, and those after it.
 * 6. Secrets are taken out of the case (`removeSecrets`: private keys, bearer tokens, known token
 *    shapes, credentials in URLs, and key, token, secret and password assignments), each counted
 *    in `secretsRemoved`.
 *
 * A session gives no case, and its drop is counted by reason (DROP_REASONS), when: the store holds
 * no file of its hash; the pull fails or returns nothing; a line is not an entry; it has no
 * header; no read of a skill file succeeded; the first read is not the cut repository's copy; a
 * Legion session's first user message names no phase; the reader's every reply is refused, its
 * call fails, or it finds no step (or a step at or before the read); the line is not a whole line
 * of the base text, or no successful read result of its file before the step holds it; a skill
 * read precedes a compaction (or a context reset) that precedes the step, or the compaction is of
 * a kind this file does not rebuild (one that keeps data for the provider, or keeps no entry of
 * the branch); an assistant message before the step quotes the line (less a leading list, heading
 * or quote mark); the case reader finds a message before the step that states the line's
 * instruction in other words; the model before the step is not an `anthropic/` one, or there is
 * none; a read before the step reads a file of the skill by its path or with a selector, which
 * arms.ts does not rebuild (a path that ends `/<skill>/SKILL.md`, holds `skills/<skill>/`, or is
 * under the skill's installed directory); a GitHub read the profile needs fails; or the profile's
 * units install no copy of the skill from the cut repository.
 *
 * **The role** of a Legion session is read from its first user message: the daemon starts a phase
 * worker with `Continue <title>. Issue: <key>. Phase: <phase>.` (packages/daemon/internal/workflow/
 * effects.go), and the phase's role is phase.go's `RoleFor` (PHASE_ROLES). The case's `evidence`
 * says so.
 *
 * **The profile** (plan, "The case context"), from the files of R4's `copies --out` directory
 * (prefix-table.tsv, installers.tsv, manifests.tsv, repositories.tsv) and the session's own reads,
 * each part with its evidence in the case:
 * - The units installed: each unit (`via`) of each prefix row that a `[Skill file: <path>]` of a
 *   read result before the step is under; and, for a Legion context, each Oh My Pi unit with a
 *   prefix row under `/opt/legion/` (a package the worker image carries) and each unit an
 *   `omp-plugins` manifest installs. A read path no row maps is listed as left out.
 * - Skills: each copy a unit ships, stages, links or installs (installers.tsv), by name, the first
 *   unit's copy winning. Plugins: an Oh My Pi package's directory (the `omp-package` manifest's), or
 *   a Claude Code plugin's directory (its skills' parent), named by its last part.
 * - Commits: each repository at the last commit of its default branch (repositories.tsv, else
 *   GitHub's) at or before the session's start, read with `gh api` GET calls (io.gh).
 * - Context files: for a Legion context, the `AGENTS.md` of the repository of the session's
 *   working directory (`/legion/workspaces/<owner>/<name>/…`) when it has one at that commit, and
 *   the project's instructions file named by its project file (a `legion-project` manifest of
 *   manifests.tsv, `instructions:` read at that commit, under `<dir>/instructions/`). Each one R4's
 *   output or the session does not name is listed as left out.
 *
 * Output: the case file `--out` (one JSON line per case, written whole after each case and checked
 * with cases.ts's `loadCases` against `--old` at the end) and `<out>.summary.json`: the seed and
 * the order, the sessions tried, each session's outcome and the reader's dollars on it, the cases
 * built, the drops by reason, the bounds, the pulls, and the gateway's spend.
 *
 * **build-tasks** (plan, R6 "Load test"): the tasks reader reads the skill's name and description
 * (`--skill`'s SKILL.md frontmatter) with no messages and proposes search terms; tasks.sql runs
 * per store and per UTC day of the configuration's window for first user messages holding them;
 * the distinct messages (at most TASK_MESSAGES, spread evenly over the stores' rows in path order)
 * go to the tasks reader again, which picks at least 5 need and 5 near-miss tasks from them, each
 * by its id, and writes the missing ones. The tasks file holds one JSON line per task, labelled
 * before any run: `{"id", "text", "label", "source": "session", "store", "pathHash"}` or
 * `{"id", "text", "label", "source": "written"}`. `<out>.summary.json` holds the terms, each query's
 * row count (or its error), the messages offered, each task's reason, and the spend.
 *
 * Exit status: 0 written; 1 failed; 2 refused before any store read or model call.
 */
import { createHash } from "node:crypto";
import { mkdirSync, readFileSync, renameSync, statSync, writeFileSync } from "node:fs";
import path from "node:path";
import { parseArgs } from "node:util";
import { messageFor } from "@legion/envoy-client/errors";
import { z } from "zod";
import {
  readCalls,
  SessionEntry,
  SessionMessage,
  skillHeaderPath,
  stripSkillHeader,
} from "../skill-scenarios/transcript";
import {
  Case,
  loadCases,
  type Profile,
  type ProfileSkill,
  pathHash,
  skillReadFile,
  Thinking,
  textLines,
} from "./cases";
import { loadConfig, type SkillCutsConfig, storeNamed } from "./config";
import { frontmatter, type Gh, parseTsv, prefixMatches, spawnGh } from "./copies";
import { mtimeRange, PULL_PAGE, pullSql, sessionsSql, tasksSql } from "./countsql";
import { createGateway, type Gateway, type Spent } from "./gateway";
import { blindOrder, MESSAGE_CHARS, renderMessage, skillReadResults } from "./judge";
import { PullRow, psqlRows, pullText } from "./psql-rows";
import {
  CaseAnswer,
  type CaseInput,
  ReaderFailed,
  runReader,
  skillFiles,
  TasksAnswer,
  type TasksInput,
} from "./readers";

/** Exit statuses. */
export const EXIT = { ok: 0, failed: 1, refused: 2 } as const;
/** The most messages the case reader reads from the skill's read on. */
export const READER_MESSAGES = 40;
/** The most characters of rendered messages the case reader reads from the skill's read on. */
export const READER_CHARS = 120_000;
/** The most messages before the skill's read that the case reader reads, for its search for a
 *  restatement of the line. */
export const READER_EARLIER_MESSAGES = 20;
/** The most characters of rendered messages before the skill's read that the case reader reads. */
export const READER_EARLIER_CHARS = 40_000;
/** The most hashes one sessions.sql query names. */
export const SESSION_BATCH = 500;
/** The most first user messages the second tasks reader call reads. */
export const TASK_MESSAGES = 60;
/** The longest one try of a reader call may take: a reply with a long thinking budget is slow. */
const READER_TIMEOUT_MS = 600_000;
const DAY_MS = 86_400_000;

/** Why a session gives no case. */
export const DROP_REASONS = {
  "not-in-store": "the store holds no file of the session's hash",
  "pull-failed": "the transcript pull failed or returned nothing",
  unparseable: "a line of the transcript is not one entry",
  "no-header": "the transcript has no session header with a start time",
  "no-read": "no read of a skill:// file of the skill succeeded",
  "not-cut-copy":
    "the skill's first read is not of the cut repository's copy (by the prefix table)",
  "no-role": "a Legion session's first user message names no phase",
  "reader-failed": "the case reader's every reply was refused",
  "reader-error": "the case reader's call failed",
  "no-step": "the case reader found no step the skill governs",
  "step-not-after-read": "the case reader's step is at or before the skill's read",
  "line-not-in-base": "the proposed line is not a whole line of the base text",
  "line-not-in-read": "no successful read result of the line's file before the step holds the line",
  "read-before-compaction":
    "a skill read precedes a compaction (or a context reset) that precedes the step",
  "compaction-unsupported":
    "a compaction before the step is of a kind this builder does not rebuild",
  "line-quoted": "an assistant message before the step quotes the line",
  "history-restates-line":
    "a message before the step states the line's instruction in other words (the case reader's finding)",
  "no-model": "no model_change or thinking_level_change before the step",
  "model-not-anthropic": "the model before the step is not an anthropic/ one",
  "read-by-path": "a read before the step reads a file of the skill by its path or with a selector",
  "profile-failed": "a GitHub read the profile needs failed",
} as const;
export type DropReason = keyof typeof DROP_REASONS;

/** The role that works each phase (packages/daemon/internal/phase/phase.go, `RoleFor`). */
export const PHASE_ROLES: Readonly<Record<string, string>> = {
  admitted: "architect",
  accepting: "architect",
  planning: "planner",
  implementing: "implementer",
  retro: "implementer",
  testing: "tester",
  reviewing: "reviewer",
  merging: "merger",
};
/** The daemon's first line to a phase worker (workflow/effects.go, `continueLine`). */
const CONTINUE_LINE = /^Continue [\s\S]*?\. Issue: [A-Z][A-Z0-9]*-\d+\. Phase: ([a-z_]+)\./;
/** The custom message kinds Oh My Pi 18.18.6 leaves out of the model's messages (plan mode's). */
const LEFT_OUT_CUSTOM = new Set(["prewalk-plan", "vibe-mode-context"]);
/** Where a Legion pod's worker image installs its Oh My Pi packages (worker.Dockerfile). */
const IMAGE_ROOT = "/opt/legion/";
/** A Legion workspace: `/legion/workspaces/<owner>/<name>/<workspace>`. */
const LEGION_WORKSPACE = /^\/legion\/workspaces\/([A-Za-z0-9-]+)\/([A-Za-z0-9._-]+)(?:\/|$)/;
/** What stands in for each secret the scan takes out. */
export const SECRET_REMOVED = "[secret removed]";

/** What `main` uses of the world. */
export interface BuildIo {
  /** Writes `text` to standard output as it is. */
  out(text: string): void;
  /** Writes one line to standard error. */
  err(line: string): void;
  /** Runs gh (copies.ts's `spawnGh` in a real run). */
  readonly gh: Gh;
}

/** A command's refusal of its arguments, before any store read or model call. */
class Refused extends Error {}

/** Writes `text` to `file` whole: into a fresh file beside it that is then renamed over it. */
function writeWhole(file: string, text: string): void {
  mkdirSync(path.dirname(file), { recursive: true });
  const fresh = `${file}.${process.pid}.tmp`;
  writeFileSync(fresh, text);
  renameSync(fresh, file);
}

/** A value that is an object, as a record of its fields. */
function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/** `text` cut to `max` characters, its start and end kept, with a note of what the cut left out. */
function cut(text: string, max: number): string {
  if (text.length <= max) return text;
  const half = Math.floor(max / 2);
  return `${text.slice(0, half)}\n[… ${text.length - 2 * half} characters left out …]\n${text.slice(-half)}`;
}

/** The dollars of `spent`, summed. */
function usdOf(spent: readonly Spent[]): number {
  return spent.reduce((sum, each) => sum + each.usd, 0);
}

// ---------------------------------------------------------------------------------------------
// The store

/** The rows of `query` run by the store command `command` on the database `db`. Throws with the
 *  command's first error line when it fails. */
async function storeRows(command: string, db: string, query: string): Promise<unknown[]> {
  const child = Bun.spawn([command, db], {
    stdin: new Blob([query]),
    stdout: "pipe",
    stderr: "pipe",
  });
  const [stdout, stderr, code] = await Promise.all([
    new Response(child.stdout).text(),
    new Response(child.stderr).text(),
    child.exited,
  ]);
  if (code !== 0) {
    const line = stderr.split("\n").find((each) => each.trim() !== "") ?? "";
    throw new Error(`the store command exited ${code}: ${line.slice(0, 300)}`);
  }
  return psqlRows(stdout);
}

/** The text of the file `filePath` of the store `db`, pulled PULL_PAGE parts a call, and the calls
 *  it took. */
export async function pullTranscript(
  command: string,
  db: string,
  filePath: string
): Promise<{ text: string; calls: number }> {
  const rows: unknown[] = [];
  let after = -2;
  let calls = 0;
  for (;;) {
    const page = await storeRows(command, db, pullSql(filePath, after));
    calls += 1;
    rows.push(...page);
    if (page.length < PULL_PAGE) break;
    after = PullRow.parse(page[page.length - 1]).o;
  }
  return { text: pullText(rows), calls };
}

/** The file hash count.sql and sessions.sql name a file by: md5 of its path, 16 hex characters. */
export function fileHash(filePath: string): string {
  return createHash("md5").update(filePath).digest("hex").slice(0, 16);
}

const SessionRow = z.strictObject({ path: z.string().min(1), hash: z.string() });

/** The paths of the files of `hashes` in the store `db`, by hash, SESSION_BATCH a query. */
async function resolvePaths(
  command: string,
  db: string,
  hashes: readonly string[]
): Promise<{ paths: Map<string, string>; queries: number }> {
  const paths = new Map<string, string>();
  let queries = 0;
  for (let start = 0; start < hashes.length; start += SESSION_BATCH) {
    const rows = await storeRows(
      command,
      db,
      sessionsSql(hashes.slice(start, start + SESSION_BATCH))
    );
    queries += 1;
    for (const row of rows) {
      const { path: filePath, hash } = SessionRow.parse(row);
      paths.set(hash, filePath);
    }
  }
  return { paths, queries };
}

// ---------------------------------------------------------------------------------------------
// The transcript

/** The entries of a transcript's text. The text after its last newline is a line still being
 *  written when it does not parse, and is left out; any other line that is not one entry throws.
 *  A message's string `content` is made one text block, as Oh My Pi reads it. */
export function transcriptEntries(text: string): SessionEntry[] {
  const lines = text.split("\n");
  const entries: SessionEntry[] = [];
  for (const [index, line] of lines.entries()) {
    if (line.trim() === "") continue;
    let value: unknown;
    try {
      value = JSON.parse(line);
    } catch (error) {
      if (index === lines.length - 1) break;
      throw new Error(`line ${index + 1} is not JSON: ${messageFor(error)}`);
    }
    if (isRecord(value) && isRecord(value.message) && typeof value.message.content === "string") {
      value.message.content = [{ type: "text", text: value.message.content }];
    }
    const parsed = SessionEntry.safeParse(value);
    if (!parsed.success) {
      throw new Error(`line ${index + 1} is not a transcript entry: ${parsed.error.message}`);
    }
    entries.push(parsed.data);
  }
  return entries;
}

/** The entries of the session's current branch, root first: the parent chain of its last entry
 *  that has an id. The header and an entry with no id are no part of it. */
export function currentBranch(entries: readonly SessionEntry[]): SessionEntry[] {
  const inBranch = (entry: SessionEntry) =>
    typeof entry.id === "string" && entry.type !== "session";
  const byId = new Map<string, SessionEntry>();
  for (const entry of entries) if (inBranch(entry)) byId.set(String(entry.id), entry);
  const chain: SessionEntry[] = [];
  const seen = new Set<string>();
  let at = entries.findLast(inBranch);
  while (at !== undefined && !seen.has(String(at.id))) {
    chain.push(at);
    seen.add(String(at.id));
    at = typeof at.parentId === "string" ? byId.get(at.parentId) : undefined;
  }
  return chain.reverse();
}

/** An entry's time as epoch milliseconds, or 0. */
function entryTime(entry: SessionEntry): number {
  const ms = Date.parse(entry.timestamp ?? "");
  return Number.isNaN(ms) ? 0 : ms;
}

/** Whether an assistant message is one Oh My Pi leaves out of the model's messages: a retry's
 *  recovery, or an error that holds no content. */
function leftOutAssistant(message: SessionMessage): boolean {
  if (message.retryRecovery === true) return true;
  if (message.stopReason !== "error") return false;
  return !(message.content ?? []).some((block) =>
    block.type === "toolCall"
      ? true
      : block.type === "text"
        ? (block.text ?? "").trim() !== ""
        : block.type === "thinking" &&
          (String(block.thinking ?? "").trim() !== "" ||
            String(block.thinkingSignature ?? "").trim() !== "")
  );
}

/** The message the model reads for `entry`, or undefined for an entry it reads none of. */
function messageOf(entry: SessionEntry): SessionMessage | undefined {
  if (entry.type === "message" && entry.message !== undefined) {
    return entry.message.role === "assistant" && leftOutAssistant(entry.message)
      ? undefined
      : entry.message;
  }
  if (entry.type === "custom_message" && !LEFT_OUT_CUSTOM.has(entry.customType ?? "")) {
    const content =
      typeof entry.content === "string" ? [{ type: "text", text: entry.content }] : entry.content;
    return SessionMessage.parse({
      role: "custom",
      customType: entry.customType,
      content,
      display: entry.display,
      details: entry.details,
      attribution: entry.attribution,
      timestamp: entryTime(entry),
    });
  }
  if (entry.type === "branch_summary" && typeof entry.summary === "string") {
    return {
      role: "branchSummary",
      summary: entry.summary,
      fromId: entry.fromId,
      timestamp: entryTime(entry),
    };
  }
  return undefined;
}

/** One message of the branch, and its entry's place in the branch. */
interface Item {
  readonly message: SessionMessage;
  readonly at: number;
}

/** A transcript, read: its branch and the messages of it, in order. */
interface Timeline {
  readonly branch: readonly SessionEntry[];
  readonly items: readonly Item[];
}

function timeline(entries: readonly SessionEntry[]): Timeline {
  const branch = currentBranch(entries);
  const items: Item[] = [];
  for (const [at, entry] of branch.entries()) {
    const message = messageOf(entry);
    if (message !== undefined) items.push({ message, at });
  }
  return { branch, items };
}

/** The read calls of `messages`, by call id: their target. */
function readTargets(messages: readonly SessionMessage[]): Map<string, string> {
  return new Map(
    readCalls(messages.map((message) => ({ message }))).map((call) => [call.id, call.target])
  );
}

/** The text of a message's text blocks, joined. */
function messageText(message: SessionMessage): string {
  return (message.content ?? [])
    .flatMap((block) => (block.type === "text" ? [block.text ?? ""] : []))
    .join("\n");
}

/** A skill read of the messages: the read result's index, its call's index, the file it read, and
 *  the path its `[Skill file: …]` line names. */
interface SkillRead {
  readonly index: number;
  readonly call: number;
  readonly file: string;
  readonly header: string | undefined;
  readonly ok: boolean;
}

/** Each read result of a `skill://<skill>` file in `messages`, in order. */
function skillReads(messages: readonly SessionMessage[], skill: string): SkillRead[] {
  const targets = readTargets(messages);
  const calls = new Map<string, number>();
  for (const [index, message] of messages.entries()) {
    if (message.role !== "assistant") continue;
    for (const block of message.content ?? []) {
      if (block.type === "toolCall" && block.id !== undefined) calls.set(block.id, index);
    }
  }
  const reads: SkillRead[] = [];
  for (const [index, message] of messages.entries()) {
    if (message.role !== "toolResult" || message.toolName !== "read") continue;
    const target = targets.get(message.toolCallId ?? "");
    const file = target === undefined ? undefined : skillReadFile(skill, target);
    if (file === undefined) continue;
    const header = skillHeaderPath(message.content?.[0]?.text ?? "");
    reads.push({
      index,
      call: calls.get(message.toolCallId ?? "") ?? index,
      file,
      header,
      ok: message.isError !== true && header !== undefined,
    });
  }
  return reads;
}

/** Why a read call of `messages` reads a file of the skill in a way arms.ts does not rebuild, or
 *  undefined: a `skill://<skill>` URL with a selector, or a path (its selector left aside) that
 *  ends `/<skill>/SKILL.md`, holds `skills/<skill>/`, or is under one of `installedDirs`. */
function readByPath(
  messages: readonly SessionMessage[],
  skill: string,
  installedDirs: readonly string[]
): string | undefined {
  const url = `skill://${skill}`;
  for (const [index, message] of messages.entries()) {
    for (const { target } of readCalls([{ message }])) {
      if (target === url || target.startsWith(`${url}/`) || target.startsWith(`${url}:`)) {
        if (skillReadFile(skill, target) === undefined) {
          return `message ${index} reads ${target}, with a selector`;
        }
        continue;
      }
      if (/^[a-z][a-z0-9+.-]*:\/\//i.test(target)) continue;
      const file = target.replace(/:[^/]*$/, "");
      if (
        file.endsWith(`/${skill}/SKILL.md`) ||
        file === `${skill}/SKILL.md` ||
        file.includes(`skills/${skill}/`) ||
        installedDirs.some((dir) => file.startsWith(`${dir}/`))
      ) {
        return `message ${index} reads ${target} by its path`;
      }
    }
  }
  return undefined;
}

/** The text a line is quoted by: the line less a leading list, heading or quote mark, trimmed. */
function quotedText(line: string): string {
  const bare = line
    .trim()
    .replace(/^(?:[-*+]|\d+[.)]|#{1,6}|>)\s+/, "")
    .trim();
  return bare === "" ? line.trim() : bare;
}

/** Every string inside `value`. */
function strings(value: unknown): string[] {
  if (typeof value === "string") return [value];
  if (Array.isArray(value)) return value.flatMap(strings);
  if (isRecord(value)) return Object.values(value).flatMap(strings);
  return [];
}

/** The index of the first assistant message of `messages` that quotes `line`, or undefined: its
 *  text, its thinking or a string of its tool calls' arguments holds the line. */
function quotingMessage(messages: readonly SessionMessage[], line: string): number | undefined {
  const quote = quotedText(line);
  const found = messages.findIndex(
    (message) =>
      message.role === "assistant" &&
      (message.content ?? []).some((block) =>
        strings([block.text, block.thinking, block.arguments]).some((text) => text.includes(quote))
      )
  );
  return found === -1 ? undefined : found;
}

const CompactionEntry = z.looseObject({
  type: z.literal("compaction"),
  id: z.string(),
  summary: z.string(),
  firstKeptEntryId: z.string(),
  tokensBefore: z.number(),
  timestamp: z.string(),
});

/** The model's messages before the branch entry at `stepAt`: the items before it, or, after the
 *  branch's last compaction or context reset before it, what Oh My Pi gives the model after that.
 *  A drop when a skill read (at `firstReadAt`) precedes that compaction or reset, or when the
 *  compaction is of a kind this file does not rebuild. */
function messagesBefore(
  t: Timeline,
  stepAt: number,
  firstReadAt: number
): { messages: SessionMessage[] } | { drop: DropReason; detail: string } {
  let boundary = -1;
  for (let at = stepAt - 1; at >= 0; at -= 1) {
    const type = t.branch[at]?.type;
    if (type === "compaction" || type === "reset_boundary") {
      boundary = at;
      break;
    }
  }
  const before = (from: number) =>
    t.items.filter((item) => item.at > from && item.at < stepAt).map((item) => item.message);
  if (boundary < 0) return { messages: before(-1) };
  const entry = t.branch[boundary];
  if (firstReadAt < boundary) {
    return {
      drop: "read-before-compaction",
      detail: `the skill read at branch entry ${firstReadAt} precedes the ${entry?.type} at entry ${boundary}`,
    };
  }
  if (entry?.type === "reset_boundary") return { messages: before(boundary) };
  const compaction = CompactionEntry.safeParse(entry);
  const kept = compaction.success
    ? t.branch.findIndex(
        (each, at) => at < boundary && each.id === compaction.data.firstKeptEntryId
      )
    : -1;
  if (
    !compaction.success ||
    kept < 0 ||
    entry?.preserveData !== undefined ||
    entry?.providerReplayThroughEntryId !== undefined ||
    entry?.method === "snapcompact" ||
    (isRecord(entry?.details) && entry.details.kind === "experimental-context-rollover")
  ) {
    return {
      drop: "compaction-unsupported",
      detail: `the compaction at branch entry ${boundary} keeps no entry of the branch, or keeps provider data`,
    };
  }
  const summary = SessionMessage.parse({
    role: "compactionSummary",
    summary: compaction.data.summary,
    shortSummary: entry.shortSummary,
    tokensBefore: compaction.data.tokensBefore,
    tokensAfter: entry.tokensAfter,
    method: entry.method,
    warning: entry.warning,
    timestamp: entryTime(entry),
  });
  const keptMessages = t.items
    .filter((item) => item.at >= kept && item.at < boundary)
    .map((item) => item.message);
  return { messages: [summary, ...keptMessages, ...before(boundary)] };
}

/** The last entry of `type` in the branch before `at` that `pick` reads a value from. */
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

// ---------------------------------------------------------------------------------------------
// Secrets

/** One secret shape: a pattern whose whole match, or the group `group` of it, is a secret. */
interface SecretShape {
  readonly name: string;
  readonly pattern: RegExp;
  /** The match less the secret: the text kept around it, with SECRET_REMOVED in its place. */
  readonly keep: (match: string, ...groups: string[]) => string;
}

const whole = () => SECRET_REMOVED;

/** The secret shapes the scan takes out, in order. */
const SECRET_SHAPES: readonly SecretShape[] = [
  {
    name: "private key",
    pattern: /-----BEGIN ([A-Z0-9 ]*)PRIVATE KEY-----[\s\S]*?(?:-----END \1PRIVATE KEY-----|$)/g,
    keep: whole,
  },
  {
    name: "bearer",
    pattern: /\b(Bearer\s+)[A-Za-z0-9._~+/=-]{16,}/gi,
    keep: (_match, kept) => `${kept}${SECRET_REMOVED}`,
  },
  {
    name: "credential in a URL",
    pattern: /\b([a-z][a-z0-9+.-]*:\/\/[^\s:/@]+:)[^\s@/]+@/gi,
    keep: (_match, kept) => `${kept}${SECRET_REMOVED}@`,
  },
  {
    name: "token",
    pattern:
      /\b(?:sk-[A-Za-z0-9_-]{20,}|gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}|xox[abprs]-[A-Za-z0-9-]{10,}|glpat-[A-Za-z0-9_-]{20,}|AKIA[0-9A-Z]{16}|AIza[0-9A-Za-z_-]{35}|ya29\.[A-Za-z0-9_-]{20,}|eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,})/g,
    keep: whole,
  },
  {
    name: "assignment",
    pattern:
      /\b([A-Za-z0-9_.-]*(?:api[_-]?key|apikey|secret|token|passwd|password|private[_-]?key|access[_-]?key)[A-Za-z0-9_.-]*["']?\s*[:=]\s*["']?)((?=[A-Za-z0-9_+/=.~-]*[0-9])(?=[A-Za-z0-9_+/=.~-]*[A-Za-z])[A-Za-z0-9_+/=.~-]{12,})/gi,
    keep: (_match, kept) => `${kept}${SECRET_REMOVED}`,
  },
];

/** `text` with each secret of SECRET_SHAPES taken out, and how many it took out. */
export function removeSecrets(text: string): { text: string; removed: number } {
  let removed = 0;
  let out = text;
  for (const shape of SECRET_SHAPES) {
    out = out.replace(shape.pattern, (match: string, ...groups: unknown[]) => {
      const kept = shape.keep(match, ...groups.filter((group) => typeof group === "string"));
      if (kept === match) return match;
      removed += 1;
      return kept;
    });
  }
  return { text: out, removed };
}

/** The fields whose strings the scan leaves alone: ids, and the signatures a replay must send
 *  back byte for byte. */
const UNSCANNED = new Set(["id", "toolCallId", "responseId", "thinkingSignature", "signature"]);

/** `value` with every secret of its strings taken out, and how many. */
function scrubbed<T>(value: T): { value: T; removed: number } {
  let removed = 0;
  const walk = (node: unknown): unknown => {
    if (typeof node === "string") {
      const result = removeSecrets(node);
      removed += result.removed;
      return result.text;
    }
    if (Array.isArray(node)) return node.map(walk);
    if (isRecord(node)) {
      return Object.fromEntries(
        Object.entries(node).map(([key, each]) => [key, UNSCANNED.has(key) ? each : walk(each)])
      );
    }
    return node;
  };
  return { value: walk(value) as T, removed };
}

// ---------------------------------------------------------------------------------------------
// The profile

/** One part of a case's evidence. */
export interface Evidence {
  readonly item: string;
  readonly source: string;
}

/** R4's output, as the profile reads it. */
interface Copies {
  readonly prefixes: Record<string, string>[];
  readonly installers: Record<string, string>[];
  readonly manifests: Record<string, string>[];
  readonly branches: Map<string, string>;
}

/** The columns each file of R4's output must have. */
const COPIES_FILES = {
  "prefix-table.tsv": ["prefix", "repository", "path", "name", "via"],
  "installers.tsv": [
    "manifest_repository",
    "manifest_path",
    "kind",
    "action",
    "unit",
    "host",
    "skill",
    "copy_repository",
    "copy_path",
  ],
  "manifests.tsv": ["repository", "path", "kind", "says"],
  "repositories.tsv": ["repository", "branch", "commit"],
} as const;

/** R4's output under `dir`. Throws a refusal naming a file that is missing or lacks a column. */
function readCopies(dir: string): Copies {
  const tables = new Map<string, Record<string, string>[]>();
  for (const [file, columns] of Object.entries(COPIES_FILES)) {
    let text: string;
    try {
      text = readFileSync(path.join(dir, file), "utf8");
    } catch (error) {
      throw new Refused(`--copies ${dir} has no ${file}: ${messageFor(error)}`);
    }
    const header = (text.split("\n")[0] ?? "").split("\t");
    const missing = columns.filter((column) => !header.includes(column));
    if (missing.length > 0) {
      throw new Refused(`--copies ${dir}: ${file} has no column ${missing.join(", ")}`);
    }
    tables.set(file, parseTsv(text));
  }
  const table = (file: keyof typeof COPIES_FILES) => tables.get(file) ?? [];
  return {
    prefixes: table("prefix-table.tsv"),
    installers: table("installers.tsv"),
    manifests: table("manifests.tsv"),
    branches: new Map(
      table("repositories.tsv").map((row) => [row.repository ?? "", row.branch ?? ""])
    ),
  };
}

/** A unit of a prefix row's `via`: `<unit> (<manifest repository>:<manifest path>)`. */
interface Via {
  readonly unit: string;
  readonly repository: string;
  readonly manifest: string;
}

function vias(via: string): Via[] {
  return via.split("; ").flatMap((each) => {
    const match = /^(.+) \(([^:()]+):(.+)\)$/.exec(each);
    return match
      ? [{ unit: match[1] ?? "", repository: match[2] ?? "", manifest: match[3] ?? "" }]
      : [];
  });
}

/** A repository path as R4 writes it, `.` for the root, as a path. */
function repoPath(shown: string): string {
  return shown === "." ? "" : shown;
}

/** The GitHub reads the profile makes, each made once. */
class GithubReads {
  private readonly made = new Map<
    string,
    Promise<{ code: number; stdout: string; stderr: string }>
  >();

  constructor(
    private readonly gh: Gh,
    private readonly branches: ReadonlyMap<string, string>
  ) {}

  private call(args: readonly string[]) {
    const key = args.join("\0");
    let call = this.made.get(key);
    if (call === undefined) {
      call = this.gh(args);
      this.made.set(key, call);
    }
    return call;
  }

  private async ok(args: readonly string[]): Promise<string> {
    const result = await this.call(args);
    if (result.code !== 0) {
      const line = result.stderr.split("\n").find((each) => each.trim() !== "") ?? "";
      throw new Error(`gh ${args.join(" ")} failed: ${line}`);
    }
    return result.stdout;
  }

  /** The default branch of `repo`: repositories.tsv's, else GitHub's. */
  async branch(repo: string): Promise<{ branch: string; from: string }> {
    const known = this.branches.get(repo);
    if (known !== undefined && known !== "") return { branch: known, from: "repositories.tsv" };
    const branch = (await this.ok(["api", `repos/${repo}`, "--jq", ".default_branch"])).trim();
    if (branch === "") throw new Error(`GitHub names no default branch of ${repo}`);
    return { branch, from: `gh api repos/${repo}` };
  }

  /** The last commit of `branch` of `repo` at or before `at`. */
  async commitAt(repo: string, branch: string, at: string): Promise<string> {
    const sha = (
      await this.ok([
        "api",
        "-X",
        "GET",
        `repos/${repo}/commits`,
        "-f",
        `sha=${branch}`,
        "-f",
        `until=${at}`,
        "-f",
        "per_page=1",
        "--jq",
        ".[0].sha",
      ])
    ).trim();
    if (!/^[0-9a-f]{40}$/.test(sha))
      throw new Error(`no commit of ${repo} ${branch} at or before ${at}`);
    return sha;
  }

  /** The text of `file` of `repo` at `commit`, or undefined when it has none. */
  async text(repo: string, commit: string, file: string): Promise<string | undefined> {
    const args = [
      "api",
      "-X",
      "GET",
      `repos/${repo}/contents/${file.split("/").map(encodeURIComponent).join("/")}`,
      "-f",
      `ref=${commit}`,
      "-H",
      "Accept: application/vnd.github.raw",
    ];
    const result = await this.call(args);
    if (result.code === 0) return result.stdout;
    if (/\b404\b|Not Found/.test(result.stderr)) return undefined;
    return this.ok(args);
  }
}

/** What the profile of one case is built from: its context and project, each successful read
 *  result of its messages whose first line is `[Skill file: <path>]` (any skill's), the session's
 *  start, and its working directory. */
interface ProfileInput {
  readonly context: string;
  readonly project: string | undefined;
  readonly reads: readonly { readonly index: number; readonly header: string }[];
  readonly sessionStart: string;
  readonly cwd: string | undefined;
}

/** The profile of a case, with the evidence for each part. Throws when a GitHub read fails. */
async function caseProfile(
  copies: Copies,
  github: GithubReads,
  input: ProfileInput
): Promise<{ profile: Profile; evidence: Evidence[] }> {
  const evidence: Evidence[] = [];
  // Units by their `via` text: one unit id (`user-skills:omp`) can come from many manifests.
  const units = new Map<string, { via: Via; why: string }>();
  const viaKey = (via: Via) => `${via.unit} (${via.repository}:${via.manifest})`;
  const shown = new Set<string>();
  for (const read of input.reads) {
    if (shown.has(read.header)) continue;
    shown.add(read.header);
    const rows = copies.prefixes.filter((row) => prefixMatches(row.prefix ?? "", read.header));
    if (rows.length === 0) {
      evidence.push({
        item: `left out: the copy read at ${read.header}`,
        source: `message ${read.index}: no row of prefix-table.tsv maps that path`,
      });
    }
    for (const row of rows) {
      for (const via of vias(row.via ?? "")) {
        if (units.has(viaKey(via))) continue;
        units.set(viaKey(via), {
          via,
          why: `message ${read.index} reads [Skill file: ${read.header}], under the prefix-table.tsv row ${row.prefix}`,
        });
      }
    }
  }
  if (input.context.startsWith("legion:")) {
    const ompUnits = new Set(
      copies.installers
        .filter((row) => row.host === "omp")
        .map((row) => `${row.unit} (${row.manifest_repository}:${row.manifest_path})`)
    );
    for (const row of copies.prefixes) {
      if (!(row.prefix ?? "").startsWith(IMAGE_ROOT)) continue;
      for (const via of vias(row.via ?? "")) {
        if (units.has(viaKey(via)) || !ompUnits.has(viaKey(via))) continue;
        units.set(viaKey(via), {
          via,
          why: `a Legion pod's worker image carries it: the prefix-table.tsv row ${row.prefix}`,
        });
      }
    }
    for (const row of copies.installers) {
      if (row.kind !== "omp-plugins") continue;
      const via = {
        unit: row.unit ?? "",
        repository: row.manifest_repository ?? "",
        manifest: row.manifest_path ?? "",
      };
      if (units.has(viaKey(via))) continue;
      if ((row.copy_path ?? "") === "") {
        evidence.push({
          item: `left out: the plugin ${row.unit}`,
          source: `installers.tsv: ${via.repository}:${via.manifest} installs it, and R4 read no repository copy of it${row.note ? ` (${row.note})` : ""}`,
        });
        continue;
      }
      units.set(viaKey(via), {
        via,
        why: `installers.tsv: ${via.repository}:${via.manifest} installs it`,
      });
    }
  }

  // The skills and plugins of each unit; each repository's commit is read once.
  const skills = new Map<string, Omit<ProfileSkill, "commit">>();
  const plugins = new Map<string, { repo: string; path: string; name: string; why: string }>();
  const memberActions = new Set(["ships", "stages", "links", "installs"]);
  for (const [key, { via, why }] of units) {
    const id = via.unit;
    const members = copies.installers.filter(
      (row) =>
        row.unit === id &&
        row.manifest_repository === via.repository &&
        row.manifest_path === via.manifest &&
        (row.copy_path ?? "") !== "" &&
        memberActions.has(row.action ?? "")
    );
    evidence.push({ item: `unit ${key}`, source: why });
    for (const row of members) {
      const name = row.skill ?? "";
      const copy = { repo: row.copy_repository ?? "", path: repoPath(row.copy_path ?? ""), name };
      const known = skills.get(name);
      if (known !== undefined) {
        if (known.repo !== copy.repo || known.path !== copy.path) {
          evidence.push({
            item: `left out: skill ${name} from ${copy.repo} ${copy.path}`,
            source: `installers.tsv: ${id} ${row.action} it too; the copy of ${known.repo} ${known.path} came first`,
          });
        }
        continue;
      }
      skills.set(name, copy);
      evidence.push({
        item: `skill ${name} (${copy.repo} ${copy.path})`,
        source: `installers.tsv: ${id} ${row.action} it (${row.manifest_repository}:${row.manifest_path})`,
      });
    }
    const pluginOf = (): { repo: string; path: string; name: string } | undefined => {
      if (id.startsWith("package:")) {
        const manifest = copies.installers.find(
          (row) => row.unit === id && row.kind === "omp-package"
        );
        if (manifest === undefined) return undefined;
        const dir = path.posix.dirname(manifest.manifest_path ?? "");
        return {
          repo: manifest.manifest_repository ?? "",
          path: dir,
          name: path.posix.basename(dir),
        };
      }
      if (id.startsWith("plugin:")) {
        const roots = new Set(
          members.map((row) => {
            const copyPath = repoPath(row.copy_path ?? "");
            const tail = `/skills/${path.posix.basename(copyPath)}`;
            return copyPath.endsWith(tail)
              ? `${row.copy_repository}\0${copyPath.slice(0, -tail.length)}`
              : "";
          })
        );
        const [root] = roots;
        if (roots.size !== 1 || root === undefined || root === "") return undefined;
        const [repo = "", dir = ""] = root.split("\0");
        return { repo, path: dir, name: path.posix.basename(dir) };
      }
      return undefined;
    };
    const plugin = pluginOf();
    if (plugin === undefined) {
      if (!id.startsWith("user-skills:")) {
        evidence.push({
          item: `left out: a plugin directory of ${id}`,
          source:
            "installers.tsv names no Oh My Pi package manifest of it, and its skills share no plugin directory",
        });
      }
    } else if (!plugins.has(plugin.name)) {
      plugins.set(plugin.name, { ...plugin, why: `the directory of ${id}` });
    }
  }

  // Context files: the session repository's AGENTS.md and the project's instructions file.
  const contextFiles: { repo: string; path: string }[] = [];
  const legion = input.context.startsWith("legion:");
  if (legion) {
    const workspace = LEGION_WORKSPACE.exec(input.cwd ?? "");
    if (workspace === null) {
      evidence.push({
        item: "left out: the session repository's AGENTS.md",
        source: `the session's working directory ${input.cwd ?? "(none)"} is no Legion workspace /legion/workspaces/<owner>/<name>/`,
      });
    } else {
      contextFiles.push({ repo: `${workspace[1]}/${workspace[2]}`, path: "AGENTS.md" });
    }
    const project = copies.manifests.find(
      (row) =>
        row.kind === "legion-project" && (row.says ?? "").startsWith(`project ${input.project}:`)
    );
    if (project === undefined) {
      evidence.push({
        item: `left out: the instructions file of the project ${input.project}`,
        source: "manifests.tsv of R4's output holds no legion-project manifest of that project",
      });
    } else {
      contextFiles.push({ repo: project.repository ?? "", path: `project:${project.path ?? ""}` });
    }
  } else {
    evidence.push({
      item: "left out: context files",
      source: "a devbox session's working directory names no repository",
    });
  }

  const commits = new Map<string, string>();
  const commitOf = async (repo: string): Promise<string> => {
    const known = commits.get(repo);
    if (known !== undefined) return known;
    const { branch, from } = await github.branch(repo);
    const sha = await github.commitAt(repo, branch, input.sessionStart);
    commits.set(repo, sha);
    evidence.push({
      item: `commit ${sha} of ${repo}`,
      source: `the last commit of ${branch} (from ${from}) at or before the session's start ${input.sessionStart}: gh api repos/${repo}/commits?sha=${branch}&until=${input.sessionStart}`,
    });
    return sha;
  };

  const profileSkills: ProfileSkill[] = [];
  for (const skill of [...skills.values()].sort((a, b) =>
    a.name < b.name ? -1 : a.name > b.name ? 1 : 0
  )) {
    profileSkills.push({ ...skill, commit: await commitOf(skill.repo) });
  }
  const profilePlugins: Profile["plugins"] = [];
  for (const plugin of plugins.values()) {
    const commit = await commitOf(plugin.repo);
    profilePlugins.push({ repo: plugin.repo, commit, path: plugin.path, name: plugin.name });
    evidence.push({
      item: `plugin ${plugin.name} (${plugin.repo} ${plugin.path})`,
      source: plugin.why,
    });
  }
  const profileFiles: Profile["contextFiles"] = [];
  for (const file of contextFiles) {
    const commit = await commitOf(file.repo);
    if (file.path.startsWith("project:")) {
      const projectFile = file.path.slice("project:".length);
      const text = await github.text(file.repo, commit, projectFile);
      const fields = text === undefined ? undefined : (Bun.YAML.parse(text) as unknown);
      const instructions = isRecord(fields) ? fields.instructions : undefined;
      if (typeof instructions !== "string" || instructions === "") {
        evidence.push({
          item: `left out: the instructions file of the project ${input.project}`,
          source: `${file.repo} ${projectFile} at ${commit} names no instructions file`,
        });
        continue;
      }
      const instructionsPath = path.posix.join(
        path.posix.dirname(path.posix.dirname(projectFile)),
        "instructions",
        instructions
      );
      profileFiles.push({ repo: file.repo, commit, path: instructionsPath });
      evidence.push({
        item: `context file ${file.repo} ${instructionsPath}`,
        source: `the project file ${projectFile} (manifests.tsv) says instructions: ${instructions}`,
      });
      continue;
    }
    if ((await github.text(file.repo, commit, file.path)) === undefined) {
      evidence.push({
        item: `left out: ${file.repo} ${file.path}`,
        source: `the repository of the session's working directory ${input.cwd} has no ${file.path} at ${commit}`,
      });
      continue;
    }
    profileFiles.push({ repo: file.repo, commit, path: file.path });
    evidence.push({
      item: `context file ${file.repo} ${file.path}`,
      source: `the session's working directory ${input.cwd} is a workspace of ${file.repo}, which has ${file.path} at ${commit}`,
    });
  }
  return {
    profile: { skills: profileSkills, plugins: profilePlugins, contextFiles: profileFiles },
    evidence,
  };
}

// ---------------------------------------------------------------------------------------------
// build-cases

const BUILD_CASES_USAGE =
  "usage: build.ts build-cases --config <skill-cuts.json> --key-file <file> --cut <id> --skill <name> --old <dir> --sessions <file> --seed <n> --copies <dir> --out <cases.jsonl> [--max <n>]";
const BUILD_TASKS_USAGE =
  "usage: build.ts build-tasks --config <skill-cuts.json> --key-file <file> --skill <dir> --store <db>... --out <tasks.jsonl>";

/** One candidate session of the sessions file. */
const Candidate = z.strictObject({
  store: z.string().min(1),
  hash: z.string().regex(/^[0-9a-f]{16}$/, "is not 16 hex characters"),
  path: z.string().min(1).optional(),
});
type Candidate = z.infer<typeof Candidate>;

/** Parses `argv` with `options`, strictly. Throws a refusal. */
function parse<O extends NonNullable<Parameters<typeof parseArgs>[0]>["options"]>(
  argv: readonly string[],
  options: O,
  usage: string
) {
  try {
    return parseArgs({ args: [...argv], options, strict: true, allowPositionals: false }).values;
  } catch (error) {
    throw new Refused(`${messageFor(error)}. ${usage}`);
  }
}

/** The value of each option `names` names, or a refusal naming the ones missing. */
function required<K extends string>(
  values: Partial<Record<K, unknown>>,
  names: readonly K[],
  usage: string
): Record<K, string> {
  const missing = names.filter((name) => typeof values[name] !== "string");
  if (missing.length > 0) {
    throw new Refused(`${missing.map((name) => `--${name}`).join(", ")} required. ${usage}`);
  }
  return values as Record<K, string>;
}

/** The configuration in `file`, or a refusal. */
function configFrom(file: string): SkillCutsConfig {
  try {
    return loadConfig(file);
  } catch (error) {
    throw new Refused(messageFor(error));
  }
}

/** A refusal unless `file` is a file. */
function mustBeFile(file: string, what: string): void {
  if (!statSync(file, { throwIfNoEntry: false })?.isFile())
    throw new Refused(`${what} ${file} is not a file`);
}

/** The gateway of a builder's readers. */
function readerGateway(config: SkillCutsConfig, keyFile: string): Gateway {
  return createGateway({
    baseUrl: config.gatewayBaseUrl,
    keyFile,
    concurrency: 1,
    timeoutMs: READER_TIMEOUT_MS,
  });
}

/** The candidates of the sessions file `file`, each once. Throws a refusal naming each line at
 *  fault: not JSON, not a candidate, a store the configuration does not name, or a path whose hash
 *  is not its `hash`. */
function readCandidates(file: string, config: SkillCutsConfig): Candidate[] {
  let text: string;
  try {
    text = readFileSync(file, "utf8");
  } catch (error) {
    throw new Refused(`cannot read --sessions ${file}: ${messageFor(error)}`);
  }
  const faults: string[] = [];
  const seen = new Set<string>();
  const candidates: Candidate[] = [];
  for (const [index, line] of text.split("\n").entries()) {
    if (line.trim() === "") continue;
    const where = `${file}:${index + 1}`;
    let value: unknown;
    try {
      value = JSON.parse(line);
    } catch (error) {
      faults.push(`${where} is not JSON: ${messageFor(error)}`);
      continue;
    }
    const parsed = Candidate.safeParse(value);
    if (!parsed.success) {
      faults.push(
        `${where} is not {"store", "hash", "path"?}: ${parsed.error.issues.map((issue) => `${issue.path.join(".") || "the line"}: ${issue.message}`).join("; ")}`
      );
      continue;
    }
    const candidate = parsed.data;
    if (!config.stores.some((store) => store.name === candidate.store)) {
      faults.push(
        `${where} names the store ${candidate.store}, which the configuration does not name`
      );
      continue;
    }
    if (candidate.path !== undefined && fileHash(candidate.path) !== candidate.hash) {
      faults.push(
        `${where}: its path's hash is ${fileHash(candidate.path)}, not ${candidate.hash}`
      );
      continue;
    }
    const key = `${candidate.store}\0${candidate.hash}`;
    if (seen.has(key)) continue;
    seen.add(key);
    candidates.push(candidate);
  }
  if (faults.length > 0) throw new Refused(`refused --sessions lines:\n${faults.join("\n")}`);
  return candidates;
}

/** What one session gave: a case, or a drop with its reason. */
type Outcome =
  | { readonly case: Case; readonly step: number }
  | { readonly drop: DropReason; readonly detail: string };

/** What one session's build reads beside the session. */
interface CaseRun {
  readonly config: SkillCutsConfig;
  readonly cut: { readonly id: string; readonly repo: string };
  readonly skill: string;
  readonly old: string;
  readonly files: CaseInput["files"];
  readonly copies: Copies;
  readonly github: GithubReads;
  readonly gateway: Gateway;
}

/** One session's record in the summary. `restatementCheck`, once the case's messages are known:
 *  how many of the messages before the step the case reader read, beside how many the case holds,
 *  so a check that read only some of them shows it. */
interface SessionRecord {
  store: string;
  hash: string;
  pathHash?: string;
  pulls?: number;
  readerUsd: number;
  outcome: string;
  reason?: DropReason;
  detail?: string;
  restatementCheck?: { read: number; before: number };
}

/** The case of one session's transcript `text`, or why it gives none. Adds what the reader spent
 *  and what its restatement check read to `record`. */
async function caseOf(
  run: CaseRun,
  store: { readonly name: string; readonly context: string },
  transcriptPath: string,
  text: string,
  record: SessionRecord
): Promise<Outcome> {
  const drop = (reason: DropReason, detail: string): Outcome => ({ drop: reason, detail });
  let entries: SessionEntry[];
  try {
    entries = transcriptEntries(text);
  } catch (error) {
    return drop("unparseable", messageFor(error));
  }
  const header = entries.find((entry) => entry.type === "session");
  const sessionStart = header?.timestamp;
  if (
    sessionStart === undefined ||
    !z.iso.datetime({ offset: true }).safeParse(sessionStart).success
  ) {
    return drop("no-header", "no session line with an ISO timestamp");
  }
  const t = timeline(entries);
  const allMessages = t.items.map((item) => item.message);
  const reads = skillReads(allMessages, run.skill);
  const first = reads.find((read) => read.ok);
  if (first === undefined) {
    return drop(
      "no-read",
      `${reads.length} read results of skill://${run.skill} files, none successful`
    );
  }
  const copyRows = run.copies.prefixes.filter(
    (row) => row.name === run.skill && prefixMatches(row.prefix ?? "", first.header ?? "")
  );
  if (!copyRows.some((row) => row.repository === run.cut.repo)) {
    return drop(
      "not-cut-copy",
      copyRows.length === 0
        ? `no prefix row of ${run.skill} maps ${first.header}`
        : `${first.header} maps to ${copyRows.map((row) => `${row.repository} ${row.path}`).join(", ")}`
    );
  }

  // The context, and for a Legion store, the role from the first user message.
  let context = store.context;
  let project: string | undefined;
  const evidence: Evidence[] = [];
  if (store.context.startsWith("legion:")) {
    project = store.context.slice("legion:".length);
    const firstUser = t.items.find((item) => item.message.role === "user");
    const phase = CONTINUE_LINE.exec(
      firstUser === undefined ? "" : messageText(firstUser.message)
    )?.[1];
    const role = phase === undefined ? undefined : PHASE_ROLES[phase];
    if (phase === undefined || role === undefined) {
      return drop(
        "no-role",
        phase === undefined
          ? "the first user message does not start `Continue <title>. Issue: <key>. Phase: <phase>.`"
          : `the phase ${phase} has no role`
      );
    }
    context = `${store.context}:${role}`;
    evidence.push({
      item: `role ${role}`,
      source: `the session's first user message (not its path) starts the daemon's line "Continue … Phase: ${phase}.", and ${phase} is worked by the ${role} (phase.go, RoleFor)`,
    });
  } else {
    evidence.push({ item: "no role", source: `the store's context is ${store.context}` });
  }

  // The case reader, on the messages from the read's call on, and, for its search for a
  // restatement of the line, the messages before it that the model still read there: those after
  // the last compaction or context reset before the read, nearest first.
  const from = Math.min(first.call, first.index);
  const hidden = skillReadResults(allMessages, run.skill);
  const shown = (index: number, message: SessionMessage): CaseInput["messages"][number] => ({
    index,
    role: message.role ?? "message",
    text: hidden.has(index)
      ? "[tool result: read]\n[the text of a file of the skill, left out: <skill> holds it]"
      : cut(
          typeof message.summary === "string"
            ? `${renderMessage(message)}\n${message.summary}`
            : renderMessage(message),
          MESSAGE_CHARS
        ),
  });
  const windowMessages: CaseInput["messages"] = [];
  let chars = 0;
  for (
    let index = from;
    index < allMessages.length && windowMessages.length < READER_MESSAGES;
    index += 1
  ) {
    const message = allMessages[index];
    if (message === undefined) break;
    const each = shown(index, message);
    if (windowMessages.length > 0 && chars + each.text.length > READER_CHARS) break;
    chars += each.text.length;
    windowMessages.push(each);
  }
  const readAt = t.items[from]?.at ?? 0;
  const boundary = t.branch.findLastIndex(
    (entry, at) => at < readAt && (entry.type === "compaction" || entry.type === "reset_boundary")
  );
  const earlier: CaseInput["messages"] = [];
  let earlierChars = 0;
  for (let index = from - 1; index >= 0 && earlier.length < READER_EARLIER_MESSAGES; index -= 1) {
    const item = t.items[index];
    if (item === undefined || item.at <= boundary) break;
    const each = shown(index, item.message);
    if (earlier.length > 0 && earlierChars + each.text.length > READER_EARLIER_CHARS) break;
    earlierChars += each.text.length;
    earlier.unshift(each);
  }
  let answer: CaseAnswer;
  try {
    const result = await runReader(
      "case",
      { skill: run.skill, files: run.files, messages: [...earlier, ...windowMessages] },
      run.gateway
    );
    record.readerUsd += usdOf(result.spent);
    answer = CaseAnswer.parse(result.answer);
  } catch (error) {
    if (error instanceof ReaderFailed) {
      record.readerUsd += usdOf(error.spent);
      return drop("reader-failed", error.message);
    }
    return drop("reader-error", messageFor(error));
  }
  if (answer.step === null) return drop("no-step", answer.reason);
  const stepIndex = answer.step.messageIndex;
  if (stepIndex <= first.index) {
    return drop("step-not-after-read", `step ${stepIndex}, read result ${first.index}`);
  }
  const stepItem = t.items[stepIndex];
  if (stepItem === undefined || stepItem.message.role !== "assistant") {
    return drop("no-step", `message ${stepIndex} is no assistant message`);
  }

  // The model and thinking level before the step.
  const model = lastBefore(t.branch, stepItem.at, (entry) =>
    entry.type === "model_change" && typeof entry.model === "string" ? entry.model : undefined
  );
  const thinking = Thinking.safeParse(
    lastBefore(t.branch, stepItem.at, (entry) =>
      entry.type === "thinking_level_change" && typeof entry.thinkingLevel === "string"
        ? entry.thinkingLevel
        : undefined
    )
  );
  if (model === undefined || !thinking.success) {
    return drop(
      "no-model",
      model === undefined ? "no model_change" : "no thinking level Oh My Pi takes"
    );
  }
  if (!model.startsWith("anthropic/")) return drop("model-not-anthropic", model);

  // The messages before the step, and the checks of the line against them.
  const firstAt = t.items[first.index]?.at ?? 0;
  const before = messagesBefore(t, stepItem.at, firstAt);
  if ("drop" in before) return drop(before.drop, before.detail);
  const messages = before.messages;
  record.restatementCheck = {
    read: earlier.length + windowMessages.filter((each) => each.index < stepIndex).length,
    before: messages.length,
  };
  const caseReads = skillReads(messages, run.skill);
  const installedDirs = caseReads.flatMap((read) =>
    read.header?.endsWith(`/${read.file}`) ? [read.header.slice(0, -(read.file.length + 1))] : []
  );
  const byPath = readByPath(messages, run.skill, installedDirs);
  if (byPath !== undefined) return drop("read-by-path", byPath);

  const file = run.files.find((each) => each.path === answer.line.file);
  const wanted = answer.line.text.trim();
  const baseLines = file === undefined ? [] : textLines(file.text);
  const lineNumber = baseLines.findIndex((line) => line.trim() === wanted) + 1;
  if (file === undefined || lineNumber === 0) {
    return drop("line-not-in-base", `${answer.line.file}: ${JSON.stringify(answer.line.text)}`);
  }
  const held = caseReads.some((read) => {
    const message = messages[read.index];
    return (
      read.ok &&
      read.file === file.path &&
      textLines(stripSkillHeader(message?.content?.[0]?.text ?? "")).some(
        (line) => line.trim() === wanted
      )
    );
  });
  if (!held) {
    return drop(
      "line-not-in-read",
      `no successful read result of ${file.path} before message ${stepIndex} holds ${JSON.stringify(wanted)}`
    );
  }
  const quoting = quotingMessage(messages, wanted);
  if (quoting !== undefined) {
    return drop("line-quoted", `message ${quoting} of the case's messages quotes it`);
  }
  if (answer.restatements.length > 0) {
    const indices = [...new Set(answer.restatements.map((each) => each.messageIndex))];
    return drop(
      "history-restates-line",
      `the case reader finds the line's instruction stated in message${indices.length === 1 ? "" : "s"} ${indices.join(", ")} of the session, before the step at message ${stepIndex}`
    );
  }

  // The profile.
  let profiled: { profile: Profile; evidence: Evidence[] };
  try {
    profiled = await caseProfile(run.copies, run.github, {
      context,
      project,
      reads: messages.flatMap((message, index) => {
        if (
          message.role !== "toolResult" ||
          message.toolName !== "read" ||
          message.isError === true
        ) {
          return [];
        }
        const shownPath = skillHeaderPath(message.content?.[0]?.text ?? "");
        return shownPath === undefined ? [] : [{ index, header: shownPath }];
      }),
      sessionStart,
      cwd: typeof header?.cwd === "string" ? header.cwd : undefined,
    });
  } catch (error) {
    return drop("profile-failed", messageFor(error));
  }
  if (
    !profiled.profile.skills.some((each) => each.name === run.skill && each.repo === run.cut.repo)
  ) {
    return drop(
      "not-cut-copy",
      `the units the session's context installs ship no ${run.skill} of ${run.cut.repo}`
    );
  }

  const digest = lastBefore(t.branch, stepItem.at, (entry) =>
    entry.type === "custom" &&
    entry.customType === "system-prompt-digest" &&
    typeof entry.data === "string"
      ? entry.data
      : undefined
  );
  const hash = pathHash(transcriptPath);
  const secrets = scrubbed({
    messages,
    realStep: stepItem.message,
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
    },
    criterion: answer.criterion,
  });
  const built: Case = Case.parse({
    id: `${run.cut.id}-${hash.slice(0, 12)}`,
    cut: run.cut.id,
    skill: run.skill,
    baseFile: file.path,
    line: { text: answer.line.text, lineNumber, lineCount: baseLines.length },
    criterion: secrets.value.criterion,
    model,
    thinking: thinking.data,
    context,
    profile: profiled.profile,
    messages: secrets.value.messages,
    skillReads: caseReads.map((read) => read.index),
    realStep: secrets.value.realStep,
    violatingStep: secrets.value.violatingStep,
    source: { store: store.name, pathHash: hash },
    sessionStart,
    secretsRemoved: secrets.removed,
    sessionDigest: digest ?? null,
    evidence: [...evidence, ...profiled.evidence],
  });
  return { case: built, step: stepIndex };
}

/** `skill-cuts build-cases`. */
async function buildCases(argv: readonly string[], io: BuildIo): Promise<number> {
  const values = parse(
    argv,
    {
      config: { type: "string" },
      "key-file": { type: "string" },
      cut: { type: "string" },
      skill: { type: "string" },
      old: { type: "string" },
      sessions: { type: "string" },
      seed: { type: "string" },
      copies: { type: "string" },
      out: { type: "string" },
      max: { type: "string" },
    },
    BUILD_CASES_USAGE
  );
  const given = required(
    values,
    ["config", "key-file", "cut", "skill", "old", "sessions", "seed", "copies", "out"],
    BUILD_CASES_USAGE
  );
  if (!/^\d+$/.test(given.seed)) throw new Refused(`--seed ${given.seed} is not a whole number`);
  const max = values.max === undefined ? Number.POSITIVE_INFINITY : Number(values.max);
  if (values.max !== undefined && (!/^\d+$/.test(values.max) || max < 1)) {
    throw new Refused(`--max ${values.max} is not a whole number of 1 or more`);
  }
  const config = configFrom(given.config);
  const cut = config.cuts.find((each) => each.id === given.cut);
  if (cut === undefined) {
    throw new Refused(
      `--cut ${given.cut} is no cut of the configuration (${config.cuts.map((each) => each.id).join(", ")})`
    );
  }
  mustBeFile(path.join(given.old, "SKILL.md"), "--old's SKILL.md");
  mustBeFile(given["key-file"], "--key-file");
  const files = skillFiles(given.old);
  const copies = readCopies(given.copies);
  const candidates = readCandidates(given.sessions, config);
  const order = blindOrder(candidates, given.seed).map(({ item }) => item);

  // Paths for the candidates that lack one, per store.
  const paths = new Map<string, string>();
  let sessionQueries = 0;
  for (const store of config.stores) {
    const hashes = order
      .filter((each) => each.store === store.name && each.path === undefined)
      .map((each) => each.hash);
    if (hashes.length === 0) continue;
    const resolved = await resolvePaths(config.psqlCommand, store.name, hashes);
    sessionQueries += resolved.queries;
    for (const [hash, filePath] of resolved.paths) paths.set(`${store.name}\0${hash}`, filePath);
  }

  const run: CaseRun = {
    config,
    cut,
    skill: given.skill,
    old: given.old,
    files,
    copies,
    github: new GithubReads(io.gh, copies.branches),
    gateway: readerGateway(config, given["key-file"]),
  };
  const summaryFile = `${given.out}.summary.json`;
  const cases: Case[] = [];
  const caseRecords: Record<string, unknown>[] = [];
  const sessions: SessionRecord[] = [];
  const drops: Partial<Record<DropReason, number>> = {};
  let pulls = 0;
  const write = () => {
    writeWhole(given.out, cases.map((each) => `${JSON.stringify(each)}\n`).join(""));
    writeWhole(
      summaryFile,
      `${JSON.stringify(
        {
          command: "build-cases",
          cut: cut.id,
          skill: given.skill,
          seed: given.seed,
          order: order.map(({ store, hash }) => ({ store, hash })),
          bounds: {
            readerMessages: READER_MESSAGES,
            readerChars: READER_CHARS,
            messageChars: MESSAGE_CHARS,
            pullPage: PULL_PAGE,
            max: Number.isFinite(max) ? max : null,
          },
          sessionsTried: sessions.length,
          casesBuilt: cases.length,
          drops,
          sessions,
          cases: caseRecords,
          sessionQueries,
          pullCalls: pulls,
          readerSpend: run.gateway.spend(),
        },
        null,
        2
      )}\n`
    );
  };
  write();
  for (const [position, candidate] of order.entries()) {
    if (cases.length >= max) break;
    const store = storeNamed(config, candidate.store);
    const record: SessionRecord = {
      store: store.name,
      hash: candidate.hash,
      readerUsd: 0,
      outcome: "",
    };
    sessions.push(record);
    const transcriptPath = candidate.path ?? paths.get(`${store.name}\0${candidate.hash}`);
    const sessionOutcome = async (): Promise<Outcome> => {
      if (transcriptPath === undefined) {
        return { drop: "not-in-store", detail: "sessions.sql gave no row of this hash" };
      }
      record.pathHash = pathHash(transcriptPath);
      let pulled: { text: string; calls: number };
      try {
        pulled = await pullTranscript(config.psqlCommand, store.name, transcriptPath);
      } catch (error) {
        return { drop: "pull-failed", detail: messageFor(error) };
      }
      pulls += pulled.calls;
      record.pulls = pulled.calls;
      if (pulled.text === "") return { drop: "pull-failed", detail: "the pull returned no row" };
      return caseOf(run, store, transcriptPath, pulled.text, record);
    };
    const outcome = await sessionOutcome();
    if ("drop" in outcome) {
      record.outcome = "dropped";
      record.reason = outcome.drop;
      record.detail = outcome.detail;
      drops[outcome.drop] = (drops[outcome.drop] ?? 0) + 1;
      io.out(
        `build-cases: ${position + 1}/${order.length} ${store.name} ${candidate.hash}: dropped (${outcome.drop}) ${outcome.detail}\n`
      );
    } else {
      const built = outcome.case;
      cases.push(built);
      record.outcome = `case ${built.id}`;
      caseRecords.push({
        id: built.id,
        pathHash: built.source.pathHash,
        store: built.source.store,
        model: built.model,
        thinking: built.thinking,
        context: built.context,
        step: outcome.step,
        messages: built.messages.length,
        skillReads: built.skillReads,
        baseFile: built.baseFile,
        line: built.line,
        secretsRemoved: built.secretsRemoved,
        profile: {
          skills: built.profile.skills.length,
          plugins: built.profile.plugins.map((plugin) => plugin.name),
          contextFiles: built.profile.contextFiles.map((file) => `${file.repo} ${file.path}`),
        },
      });
      io.out(
        `build-cases: ${position + 1}/${order.length} ${store.name} ${candidate.hash}: case ${built.id} (step ${outcome.step}, ${built.model}, ${built.context})\n`
      );
    }
    write();
  }
  // The file must load as the behaviour commands load it.
  loadCases(given.out, given.old);
  const usd = run.gateway.spend().usd;
  io.out(
    `build-cases: ${cases.length} cases of ${sessions.length} sessions tried, $${usd.toFixed(4)} of readers at list price; written to ${given.out} and ${summaryFile}\n`
  );
  return EXIT.ok;
}

// ---------------------------------------------------------------------------------------------
// build-tasks

const TaskRow = z.strictObject({ path: z.string().min(1), text: z.string() });

/** The UTC days of [start, end), as YYYY-MM-DD. */
function windowDays(start: string, end: string): string[] {
  const days: string[] = [];
  const first = new Date(start);
  let at = Date.UTC(first.getUTCFullYear(), first.getUTCMonth(), first.getUTCDate());
  const stop = Date.parse(end);
  for (; at < stop; at += DAY_MS) days.push(new Date(at).toISOString().slice(0, 10));
  return days;
}

/** `items`, or at most `max` of them spread evenly, in their order. */
function spread<T>(items: readonly T[], max: number): T[] {
  if (items.length <= max) return [...items];
  return Array.from({ length: max }, (_, n) => items[Math.floor((n * items.length) / max)] as T);
}

/** `skill-cuts build-tasks`. */
async function buildTasks(argv: readonly string[], io: BuildIo): Promise<number> {
  const values = parse(
    argv,
    {
      config: { type: "string" },
      "key-file": { type: "string" },
      skill: { type: "string" },
      store: { type: "string", multiple: true },
      out: { type: "string" },
    },
    BUILD_TASKS_USAGE
  );
  const given = required(values, ["config", "key-file", "skill", "out"], BUILD_TASKS_USAGE);
  const stores = values.store ?? [];
  if (stores.length === 0) throw new Refused(`--store required. ${BUILD_TASKS_USAGE}`);
  const config = configFrom(given.config);
  for (const store of stores) {
    try {
      storeNamed(config, store);
    } catch (error) {
      throw new Refused(messageFor(error));
    }
  }
  mustBeFile(given["key-file"], "--key-file");
  const skillFile = path.join(given.skill, "SKILL.md");
  mustBeFile(skillFile, "--skill's SKILL.md");
  const fields = frontmatter(readFileSync(skillFile, "utf8")) ?? {};
  const skill = {
    name:
      typeof fields.name === "string" && fields.name !== ""
        ? fields.name
        : path.basename(path.resolve(given.skill)),
    description: typeof fields.description === "string" ? fields.description : "",
  };
  const gateway = readerGateway(config, given["key-file"]);

  const proposal = await runReader("tasks", { skill, messages: [] } satisfies TasksInput, gateway);
  const terms = TasksAnswer.parse(proposal.answer).terms;
  io.out(`build-tasks: terms ${terms.map((term) => JSON.stringify(term)).join(", ")}\n`);

  const queries: { store: string; day: string; rows: number | null; error?: string }[] = [];
  const found: { store: string; path: string; text: string }[] = [];
  for (const store of stores) {
    for (const day of windowDays(config.windowStart, config.windowEnd)) {
      try {
        const rows = await storeRows(
          config.psqlCommand,
          store,
          tasksSql(mtimeRange(day), config.windowStart, config.windowEnd, terms)
        );
        queries.push({ store, day, rows: rows.length });
        for (const row of rows) found.push({ store, ...TaskRow.parse(row) });
      } catch (error) {
        queries.push({ store, day, rows: null, error: messageFor(error) });
        io.err(`build-tasks: ${store} ${day}: ${messageFor(error)}`);
      }
    }
  }
  const distinct = new Map<string, { store: string; path: string; text: string }>();
  for (const row of found) {
    const key = row.text.trim();
    if (key !== "" && !distinct.has(key)) distinct.set(key, row);
  }
  const offered = spread([...distinct.values()], TASK_MESSAGES).map((row, n) => ({
    id: `m${n + 1}`,
    ...row,
  }));
  const picked = await runReader(
    "tasks",
    { skill, messages: offered.map(({ id, text }) => ({ id, text })) } satisfies TasksInput,
    gateway
  );
  const answer = TasksAnswer.parse(picked.answer);
  const byId = new Map(offered.map((message) => [message.id, message]));
  const tasks = answer.tasks.map((task, n) => {
    const id = `t${n + 1}`;
    if (task.from === "written")
      return { id, text: task.text, label: task.label, source: "written" as const };
    const message = byId.get(task.messageId);
    if (message === undefined)
      throw new Error(`the tasks reader names no message ${task.messageId}`);
    return {
      id,
      text: message.text,
      label: task.label,
      source: "session" as const,
      store: message.store,
      pathHash: pathHash(message.path),
    };
  });
  writeWhole(given.out, tasks.map((task) => `${JSON.stringify(task)}\n`).join(""));
  const count = (label: string, source: string) =>
    tasks.filter((task) => task.label === label && task.source === source).length;
  const summaryFile = `${given.out}.summary.json`;
  writeWhole(
    summaryFile,
    `${JSON.stringify(
      {
        command: "build-tasks",
        skill: skill.name,
        stores,
        window: { start: config.windowStart, end: config.windowEnd },
        terms,
        queries,
        messagesFound: found.length,
        messagesDistinct: distinct.size,
        messagesOffered: offered.length,
        tasks: answer.tasks.map((task, n) => ({
          id: `t${n + 1}`,
          label: task.label,
          source: task.from,
          why: task.why,
        })),
        counts: {
          need: { session: count("need", "session"), written: count("need", "written") },
          "near-miss": {
            session: count("near-miss", "session"),
            written: count("near-miss", "written"),
          },
        },
        readerSpend: gateway.spend(),
      },
      null,
      2
    )}\n`
  );
  io.out(
    `build-tasks: ${tasks.length} tasks (${tasks.filter((task) => task.source === "session").length} from sessions) from ${offered.length} first user messages offered; $${gateway.spend().usd.toFixed(4)} of readers at list price; written to ${given.out} and ${summaryFile}\n`
  );
  return EXIT.ok;
}

// ---------------------------------------------------------------------------------------------
// The command

const USAGE = [BUILD_CASES_USAGE, BUILD_TASKS_USAGE].join("\n");

/** `skill-cuts build-cases …` and `skill-cuts build-tasks …`: `argv` starts with the command's
 *  name. Returns the exit status (EXIT). */
export async function main(argv: readonly string[], io: BuildIo): Promise<number> {
  const [command, ...rest] = argv;
  try {
    switch (command) {
      case "build-cases":
        return await buildCases(rest, io);
      case "build-tasks":
        return await buildTasks(rest, io);
      default:
        throw new Refused(`no command ${JSON.stringify(command ?? "")}.\n${USAGE}`);
    }
  } catch (error) {
    if (error instanceof Refused) {
      io.err(`${command ?? "build"}: ${error.message}`);
      return EXIT.refused;
    }
    io.err(`${command}: ${messageFor(error)}`);
    return EXIT.failed;
  }
}

if (import.meta.main) {
  process.exit(
    await main(Bun.argv.slice(2), {
      out: (text) => process.stdout.write(text),
      err: (line) => process.stderr.write(`${line}\n`),
      gh: spawnGh,
    })
  );
}
