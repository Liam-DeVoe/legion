/**
 * The load test of R6 and R7 (plan, R5 `load.ts`): whether a host's agent loads a skill for a task,
 * given what the host lists for the skill. `skill-cuts load` (cli.ts) runs it:
 *
 *     bun scripts/skill-cuts/cli.ts load --config <file> --key-file <file> --skill <dir>
 *         [--name <name>] --tasks <file> --host omp|claude --profile <file> [--runs 3] --out <dir>
 *
 * - `--skill` is the skill's directory as written: its description is the one tested. The skill
 *   is tested under `--name` when one is given, else under its frontmatter `name` (its
 *   directory's name when the frontmatter names none). `--name` changes the frontmatter `name`
 *   and the directory's name in a scratch copy (`renamedSkill`) and nothing else: the skill's own
 *   text, its references and every other file that names the skill stay as they are, and the
 *   command says so.
 * - The tasks file (`loadTasks`) holds one JSON object per line (Task): `{id, text, label,
 *   source: "session", store, pathHash}` for a real session's first request (its store, and
 *   cases.ts's `pathHash` of the transcript's path, never the path), or `{id, text, label,
 *   source: "written"}`; `label` is `need` or `near-miss`.
 * - The profile file (`loadProfile`) is one JSON object: cases.ts's `Profile` (the other skills
 *   installed beside it, each a `ProfileSkill`; the Oh My Pi plugins; the context files; an
 *   appended system prompt), the `context` (`devbox`, the default, or `legion:<project>:<role>`),
 *   and for Claude Code `claude`, the plugin layout (ClaudeLayout).
 *
 * For each task the host's first request is captured once, against the recorder (recorder.ts),
 * so no host process reaches the gateway or runs a tool:
 *
 * - Oh My Pi: capture.ts's `captureBody`, with the task's text as a history of one user message.
 *   Oh My Pi lists each skill under a hint that it has a model write from the name and the
 *   description, so what is tested is that hint; the capture writes it once per Oh My Pi version,
 *   name and description, through the gateway, and keeps it in its cache.
 * - Claude Code: a scratch `CLAUDE_CONFIG_DIR` and HOME, into which `claude plugin install`
 *   installs a local marketplace that holds the profile's skills and the tested skill under its
 *   name, each in its plugin; then `claude -p --output-format stream-json --verbose --model <id>
 *   -- <task>` in the context's working directory, with `ANTHROPIC_BASE_URL` at the recorder. The
 *   first request that holds tools and the task's text is kept. Claude Code lists each plugin
 *   skill as `<plugin>:<name>` with its description (`claudeListing`). Thinking `off` sets
 *   `CLAUDE_CODE_DISABLE_THINKING`, a level of `claude --effort` goes as that flag, and `auto`
 *   leaves Claude Code's own default.
 *
 * Then that one body is sent `--runs` times to the gateway (gateway.ts), with the `anthropic-*`
 * headers the host sent beside it, on the route of its API: an `anthropic/<id>` model's body goes
 * to the Messages route, and an `openai/<id>` model's (Oh My Pi only; its hints still come from an
 * Anthropic `--hint-model`) to the Responses route. A run loads the skill when the first assistant
 * message reads it (`firstMessageLoad`, over the message's tool calls in either API: `tool_use`
 * blocks, or `function_call` items with their arguments parsed): for Oh My Pi, a `read` of
 * `skill://<name>`, `skill://<name>/SKILL.md` or a path ending `/<name>/SKILL.md`; for Claude
 * Code, a `Skill` call naming `<name>` or `<plugin>:<name>`, or a `Read` of a path ending
 * `/<name>/SKILL.md`. These are R1's reads (count.sql): a read selector after the last `/`
 * (`:raw`, `:1-40`) is taken off first, and a read of a reference is no load. One tool call of
 * the message that reads the skill is enough, whatever comes before it. A run never goes past that
 * first message: no tool call is answered, so a skill that another skill sends the agent to is
 * never counted as loaded by its own description.
 *
 * Under `--out`: `summary.json` (the test's settings, what the host lists for the skill, each
 * task's loads and verdict, the skill's verdict, the spend), `runs/<task id>.jsonl` (one JSON line
 * per run, LoadRun), and `captures/` (each task's body and its record). A task whose run file holds
 * its runs is skipped, so a command started again goes on from what was saved; an `--out` written
 * by a test of other settings (another skill text, name, host, model, thinking level, run count or
 * profile), or by a task of the same id with another text, is refused. The verdicts are score.ts's:
 * a task's is `taskFault` and the skill's `descriptionFault`, given only once every task has its
 * runs and they are LOAD_RUNS.
 */
import {
  cpSync,
  existsSync,
  mkdirSync,
  readFileSync,
  renameSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import path from "node:path";
import { messageFor } from "@legion/envoy-client/errors";
import { z } from "zod";
import type { Arm } from "./arms";
import {
  type CaptureCase,
  type CaptureRecord,
  captureBody,
  capturePaths,
  type HintWriter,
} from "./capture";
import { Profile, type ProfileSkill, type Thinking } from "./cases";
import {
  buildContext,
  type ContextRecord,
  cacheKey,
  type RepoSource,
  skillFrontmatter,
} from "./context";
import {
  BudgetSpent,
  type ContentBlock,
  type Gateway,
  type MessagesBody,
  PRICES,
  type Reply,
  type RequestBody,
  type ToolCall,
  toolCalls,
} from "./gateway";
import { type RecordedRequest, startRecorder } from "./recorder";
import {
  descriptionFault,
  LOAD_RUNS,
  NEAR_MISS_MAX_LOADS,
  NEED_MIN_LOADS,
  TaskLabel,
  taskFault,
} from "./score";

/** The hosts a load test runs in. */
export const Host = z.enum(["omp", "claude"]);
export type Host = z.infer<typeof Host>;
/** A thinking level, as `omp --thinking` takes it (cases.ts's `Thinking`). */
export type ThinkingLevel = z.infer<typeof Thinking>;

/** The model a load test runs unless `--model` says otherwise: the one the pinned hosts' fixture
 *  runs used (testdata/CAPTURE.md, F1 to F3). */
export const LOAD_MODEL = "anthropic/claude-sonnet-5-5";
/** Each host's thinking level unless `--thinking` says otherwise, as those fixture runs set it:
 *  Oh My Pi's `--thinking off` (F1, F2), and Claude Code's own default (F3), `auto` here. */
export const LOAD_THINKING: Readonly<Record<Host, ThinkingLevel>> = { omp: "off", claude: "auto" };
/** R6's smallest test: this many tasks of each label. A smaller test still gets its verdict, with
 *  a note that says so. */
export const MIN_TASKS_PER_LABEL = 5;

/** The longest one Claude Code run may take, by default. */
const RUN_TIMEOUT_MS = 600_000;
/** What a Claude Code run is given as its provider credential: the recorder reads none, and no
 *  gateway ever gets this one. */
const RECORDER_CREDENTIAL = "not-a-secret-the-recorder-reads-none";
/** Claude Code's line before its skill listing. */
const CLAUDE_LISTING = "The following skills are available for use with the Skill tool:";
/** The arm a load test's capture is: the skill's text as written, which c-diagnosis calls the old
 *  description. */
const ARM = "old";
/** A skill, plugin or marketplace name: what can name a directory and a listing entry. */
const NAME = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;
/** A skill file's frontmatter, its text in group 1. */
const FRONTMATTER = /^---\r?\n([\s\S]*?)\r?\n---(\r?\n|$)/;
/** The levels `claude --effort` takes. */
const CLAUDE_EFFORTS: readonly ThinkingLevel[] = ["low", "medium", "high", "xhigh", "max"];

/** A refusal of the load test's inputs, before any model call. */
export class LoadRefused extends Error {}
/** A load test that stops on its own rule: the budget is spent, or the memory passed its limit. */
export class LoadStopped extends Error {}

const Name = z.string().regex(NAME, { error: "a name of letters, digits, '.', '_' and '-'" });

/** Where a task comes from. */
export const TaskSource = z.enum(["session", "written"]);

const TaskFields = {
  id: z.string().min(1),
  text: z.string().refine((text) => text.trim() !== "", "is blank"),
  label: TaskLabel,
};
/** One task of a tasks file: a real session's first request, with its store and the hash of its
 *  transcript's path, or a task the reader wrote. */
export const Task = z.discriminatedUnion("source", [
  z.strictObject({
    ...TaskFields,
    source: z.literal("session"),
    store: z.string().min(1),
    pathHash: z.string().regex(/^[0-9a-f]{64}$/, { error: "a SHA-256 in hex" }),
  }),
  z.strictObject({ ...TaskFields, source: z.literal("written") }),
]);
export type Task = z.infer<typeof Task>;

/** Where Claude Code installs the skills: a local marketplace named `marketplace` whose `plugins`
 *  each hold the profile's skills they name, and `skillPlugin`, the plugin the tested skill goes
 *  in (one of `plugins`, or a plugin of its own). */
export const ClaudeLayout = z.strictObject({
  marketplace: Name,
  plugins: z.array(z.strictObject({ name: Name, skills: z.array(Name) })),
  skillPlugin: Name,
});
export type ClaudeLayout = z.infer<typeof ClaudeLayout>;

/** A load test's profile: what the host installs beside the tested skill. */
export const LoadProfile = Profile.extend({
  context: z
    .string()
    .regex(/^(legion:[^:\s]+:[^:\s]+|devbox)$/, { error: "devbox or legion:<project>:<role>" })
    .default("devbox"),
  claude: ClaudeLayout.optional(),
});
export type LoadProfile = z.infer<typeof LoadProfile>;

/** What one reply used: gateway.ts's `Spent`. */
const SpentRecord = z.strictObject({
  model: z.string(),
  input: z.number(),
  output: z.number(),
  cacheRead: z.number(),
  cacheWrite: z.number(),
  reasoning: z.number().optional(),
  usd: z.number(),
});

/** One run of one task: whether the first assistant message the gateway gave for the task's body
 *  loaded the skill and by which call, the names of its tool calls in order, its stop reason, the
 *  request id, what it used, and what the host lists for the skill (Oh My Pi's hint, or Claude
 *  Code's listed description). */
export const LoadRun = z.strictObject({
  task: z.string().min(1),
  run: z.int().positive(),
  label: TaskLabel,
  source: TaskSource,
  host: Host,
  name: z.string().min(1),
  loaded: z.boolean(),
  loadedBy: z.string().nullable(),
  toolCalls: z.array(z.string()),
  stopReason: z.string().nullable(),
  requestId: z.string().nullable(),
  usage: SpentRecord,
  listed: z.string(),
});
export type LoadRun = z.infer<typeof LoadRun>;

/** The skill a load test looks for in a reply: its name, and for Claude Code its plugin. */
export interface TestedSkill {
  readonly name: string;
  readonly plugin?: string;
}

/** What the first assistant message of a run did: whether it loaded the skill, the call that did
 *  (`read skill://…`, `Skill <plugin>:<name>`, `Read <path>`), and its tool calls' names. */
export interface FirstMessage {
  readonly loaded: boolean;
  readonly loadedBy: string | null;
  readonly toolCalls: readonly string[];
}

/** One task's runs so far: how many were made, how many loaded the skill and which, whether it
 *  has the runs the test asks for, and its verdict (score.ts's `taskFault`), null until it has
 *  them or when they are not LOAD_RUNS. */
export interface TaskOutcome {
  readonly made: number;
  readonly loads: number;
  readonly loadedRuns: readonly number[];
  readonly complete: boolean;
  readonly fault: boolean | null;
}

/** The skill's verdict: a description fault (score.ts's `descriptionFault`), none, or no verdict
 *  yet (null); and the reason in words. */
export interface LoadVerdict {
  readonly descriptionFault: boolean | null;
  readonly reason: string;
}

/** Writes `text` to `file` whole: into a fresh file beside it that is then renamed over it. */
function writeWhole(file: string, text: string): void {
  mkdirSync(path.dirname(file), { recursive: true });
  const fresh = `${file}.${process.pid}.tmp`;
  writeFileSync(fresh, text);
  renameSync(fresh, file);
}

/** `value` as JSON, two spaces deep, with a final newline. */
function json(value: unknown): string {
  return `${JSON.stringify(value, null, 2)}\n`;
}

/** Each issue of a zod error as `<path>: <message>`, joined. */
function issueText(error: z.ZodError): string {
  return error.issues
    .map((issue) => `${issue.path.join(".") || "the value"}: ${issue.message}`)
    .join("; ");
}

/** The tasks of `file`. Throws one LoadRefused that names each line it refuses and why (a line
 *  that is not a task, or an id given twice), or that the file holds no task. */
export function loadTasks(file: string): Task[] {
  let text: string;
  try {
    text = readFileSync(file, "utf8");
  } catch (error) {
    throw new LoadRefused(`cannot read the tasks file ${file}: ${messageFor(error)}`);
  }
  const tasks: Task[] = [];
  const refusals: string[] = [];
  for (const [index, line] of text.split("\n").entries()) {
    if (line.trim() === "") continue;
    const where = `${file}:${index + 1}`;
    let raw: unknown;
    try {
      raw = JSON.parse(line);
    } catch (error) {
      refusals.push(`${where} is not JSON: ${messageFor(error)}`);
      continue;
    }
    const parsed = Task.safeParse(raw);
    if (!parsed.success) {
      refusals.push(`${where} is not a task: ${issueText(parsed.error)}`);
      continue;
    }
    if (tasks.some((task) => task.id === parsed.data.id)) {
      refusals.push(`${where}: the task id ${parsed.data.id} is given twice`);
      continue;
    }
    tasks.push(parsed.data);
  }
  if (refusals.length > 0) throw new LoadRefused(`refused tasks:\n${refusals.join("\n")}`);
  if (tasks.length === 0) throw new LoadRefused(`the tasks file ${file} holds no task`);
  return tasks;
}

/** The profile of `file`, checked for a test of the skill `name` in `host`. Throws a LoadRefused
 *  naming each fault: a file that is not a profile; a profile skill named `name` (the tested skill
 *  is not one of the other skills) or a name given twice; and for Claude Code a profile with no
 *  plugin layout, or one that puts a profile skill in no plugin or in two, names a skill the
 *  profile does not install, or names a plugin twice. */
export function loadProfile(file: string, host: Host, name: string): LoadProfile {
  let raw: unknown;
  try {
    raw = JSON.parse(readFileSync(file, "utf8"));
  } catch (error) {
    throw new LoadRefused(`cannot read the profile ${file} as JSON: ${messageFor(error)}`);
  }
  const parsed = LoadProfile.safeParse(raw);
  if (!parsed.success) {
    throw new LoadRefused(`the profile ${file} is refused: ${issueText(parsed.error)}`);
  }
  const profile = parsed.data;
  const faults: string[] = [];
  const names = profile.skills.map((skill) => skill.name);
  if (names.includes(name)) {
    faults.push(`it installs a skill named ${name}, the name the skill is tested under`);
  }
  for (const [index, skill] of names.entries()) {
    if (names.indexOf(skill) !== index) faults.push(`it installs ${skill} twice`);
  }
  const layout = profile.claude;
  if (host === "claude" && layout === undefined) {
    faults.push("it has no `claude` plugin layout, which the Claude Code host needs");
  } else if (host === "claude" && layout !== undefined) {
    const held = layout.plugins.flatMap((plugin) => plugin.skills);
    for (const skill of names) {
      const count = held.filter((each) => each === skill).length;
      if (count !== 1) faults.push(`its layout puts ${skill} in ${count} plugins, not 1`);
    }
    for (const skill of new Set(held)) {
      if (!names.includes(skill))
        faults.push(`its layout names ${skill}, which it does not install`);
    }
    const plugins = layout.plugins.map((plugin) => plugin.name);
    for (const [index, plugin] of plugins.entries()) {
      if (plugins.indexOf(plugin) !== index) faults.push(`its layout names ${plugin} twice`);
    }
  }
  if (faults.length > 0) {
    throw new LoadRefused(`the profile ${file} is refused: ${faults.join("; ")}`);
  }
  return profile;
}

/** The frontmatter `name` of the skill in `dir`, or the directory's own name when it gives none. */
export function skillName(dir: string): string {
  const text = readFileSync(path.join(dir, "SKILL.md"), "utf8");
  const block = FRONTMATTER.exec(text)?.[1];
  const parsed: unknown = block === undefined ? undefined : Bun.YAML.parse(block);
  const name = typeof parsed === "object" && parsed !== null ? Reflect.get(parsed, "name") : null;
  return typeof name === "string" && name.trim() !== "" ? name.trim() : path.basename(dir);
}

/**
 * A copy of the skill in `dir` under the name `name`, at `<scratch>/<name>`, replacing one there:
 * the same files byte for byte, but for its SKILL.md's frontmatter `name` line, which becomes
 * `name: <name>` (added at the frontmatter's top when it has none). Nothing else that names the
 * skill changes: its own text, its references, and every file outside it. Throws a LoadRefused
 * when the SKILL.md has no frontmatter.
 */
export function renamedSkill(dir: string, name: string, scratch: string): string {
  const file = path.join(dir, "SKILL.md");
  const text = readFileSync(file, "utf8");
  const open = /^---\r?\n/.exec(text)?.[0];
  const block = FRONTMATTER.exec(text)?.[1];
  if (open === undefined || block === undefined) {
    throw new LoadRefused(`${file} has no frontmatter, so it has no name to change`);
  }
  const named = /^name:.*$/m.test(block)
    ? block.replace(/^name:.*$/m, `name: ${name}`)
    : `name: ${name}\n${block}`;
  const dest = path.join(scratch, name);
  rmSync(dest, { recursive: true, force: true });
  mkdirSync(scratch, { recursive: true });
  cpSync(dir, dest, { recursive: true, verbatimSymlinks: true });
  writeFileSync(
    path.join(dest, "SKILL.md"),
    `${open}${named}${text.slice(open.length + block.length)}`
  );
  return dest;
}

/** Whether `target` (a path or a `skill://` URL) reads the SKILL.md of skill `name`, as R1 counts
 *  a read: a read selector after the last `/` is taken off first. */
function readsSkillFile(target: string, name: string): boolean {
  const bare = target.replace(/:[^/]*$/, "");
  return (
    bare === `skill://${name}` ||
    bare === `skill://${name}/` ||
    bare === `${name}/SKILL.md` ||
    bare.endsWith(`/${name}/SKILL.md`)
  );
}

/** How the tool call `call` of a reply loads `skill` in `host` (`read <path>`, `Skill <name>`,
 *  `Read <path>`), or undefined when it does not. */
function loadingCall(host: Host, call: ToolCall, skill: TestedSkill): string | undefined {
  const { input } = call;
  const field = (key: string): string | undefined => {
    const value = typeof input === "object" && input !== null ? Reflect.get(input, key) : null;
    return typeof value === "string" ? value : undefined;
  };
  if (host === "omp") {
    const target = call.name === "read" ? field("path") : undefined;
    return target !== undefined && readsSkillFile(target, skill.name)
      ? `read ${target}`
      : undefined;
  }
  if (call.name === "Skill") {
    const named = field("skill") ?? field("command");
    const names = [skill.name];
    if (skill.plugin !== undefined) names.push(`${skill.plugin}:${skill.name}`);
    return named !== undefined && names.includes(named.replace(/^\/+/, ""))
      ? `Skill ${named}`
      : undefined;
  }
  const file = call.name === "Read" ? field("file_path") : undefined;
  return file !== undefined && readsSkillFile(file, skill.name) ? `Read ${file}` : undefined;
}

/** What the first assistant message of a run, the reply's `content` in either API, did with
 *  `skill` in `host` (see the file's header). */
export function firstMessageLoad(
  host: Host,
  content: readonly ContentBlock[],
  skill: TestedSkill
): FirstMessage {
  const calls = toolCalls(content);
  const loadedBy = calls.map((call) => loadingCall(host, call, skill)).find((how) => how) ?? null;
  return {
    loaded: loadedBy !== null,
    loadedBy,
    toolCalls: calls.map((call) => call.name ?? ""),
  };
}

/** The outcome of a task labelled `label` from its `runs`, of the `wanted` the test asks for. */
export function taskOutcome(
  label: TaskLabel,
  runs: readonly LoadRun[],
  wanted: number
): TaskOutcome {
  const loadedRuns = runs.filter((run) => run.loaded).map((run) => run.run);
  const complete = runs.length >= wanted;
  return {
    made: runs.length,
    loads: loadedRuns.length,
    loadedRuns,
    complete,
    fault: complete && wanted === LOAD_RUNS ? taskFault({ label, loads: loadedRuns.length }) : null,
  };
}

/** The skill's verdict from its tasks' outcomes, of `wanted` runs a task. */
export function skillOutcome(
  tasks: readonly {
    readonly id: string;
    readonly label: TaskLabel;
    readonly outcome: TaskOutcome;
  }[],
  wanted: number
): LoadVerdict {
  const lacking = tasks.filter((task) => !task.outcome.complete).map((task) => task.id);
  if (lacking.length > 0) {
    return {
      descriptionFault: null,
      reason: `no verdict yet: tasks ${lacking.join(", ")} lack runs`,
    };
  }
  if (wanted !== LOAD_RUNS) {
    return {
      descriptionFault: null,
      reason: `no verdict: R6's rule is of ${LOAD_RUNS} runs a task, and this test runs ${wanted}`,
    };
  }
  const fault = descriptionFault(
    tasks.map((task) => ({ label: task.label, loads: task.outcome.loads }))
  );
  const faulty = tasks.filter((task) => task.outcome.fault).map((task) => task.id);
  return {
    descriptionFault: fault,
    reason: fault
      ? `a description fault: tasks ${faulty.join(", ")}`
      : `no description fault in ${tasks.length} tasks`,
  };
}

/** Every text of a request body: its system blocks, and each message's text parts. */
function bodyTexts(body: unknown): string[] {
  const texts: string[] = [];
  const take = (content: unknown): void => {
    if (typeof content === "string") texts.push(content);
    if (!Array.isArray(content)) return;
    for (const part of content) {
      const text = typeof part === "object" && part !== null ? Reflect.get(part, "text") : null;
      if (typeof text === "string") texts.push(text);
    }
  };
  if (typeof body !== "object" || body === null) return texts;
  take(Reflect.get(body, "system"));
  const messages: unknown = Reflect.get(body, "messages");
  for (const message of Array.isArray(messages) ? messages : []) {
    take(typeof message === "object" && message !== null ? Reflect.get(message, "content") : null);
  }
  return texts;
}

/** The skills a Claude Code request body lists for its Skill tool, each name (`<plugin>:<name>`
 *  for a plugin's skill) with its listed description, in order. A description's further lines,
 *  up to the next entry, are kept with it. */
export function claudeListing(body: unknown): Map<string, string> {
  const listing = new Map<string, string>();
  for (const text of bodyTexts(body)) {
    const start = text.indexOf(CLAUDE_LISTING);
    if (start < 0) continue;
    let last: string | undefined;
    const lines = text
      .slice(start + CLAUDE_LISTING.length)
      .replace(/^\n+/, "")
      .split("\n");
    for (const line of lines) {
      if (line.trim() === "") break;
      const entry = /^- ([^\s:]+(?::[^\s:]+)?): (.*)$/.exec(line);
      if (entry?.[1] !== undefined && entry[2] !== undefined) {
        last = entry[1];
        listing.set(last, entry[2]);
      } else if (last !== undefined) {
        listing.set(last, `${listing.get(last)}\n${line}`);
      }
    }
  }
  return listing;
}

/** The files of the capture of task `taskId` in `host` under `out`. */
export function loadCapturePaths(
  out: string,
  host: Host,
  taskId: string
): { readonly body: string; readonly record: string } {
  const dir = path.join(out, "captures");
  if (host === "omp") return capturePaths(dir, taskId, ARM);
  const stem = path.join(dir, `${encodeURIComponent(taskId)}.claude`);
  return { body: `${stem}.body.json`, record: `${stem}.capture.json` };
}

/** The run file of task `taskId` under `out`. */
export function loadRunFile(out: string, taskId: string): string {
  return path.join(out, "runs", `${encodeURIComponent(taskId)}.jsonl`);
}

/** What a Claude Code capture used and saw. */
export interface ClaudeCaptureRecord {
  readonly task: string;
  readonly capturedAt: string;
  readonly claude: { readonly binary: string; readonly version: string };
  readonly model: string;
  readonly thinking: ThinkingLevel;
  readonly skill: TestedSkill;
  /** The request's path, and the `anthropic-*` headers Claude Code sent with it, which each run
   *  sends with the body. */
  readonly requestPath: string;
  readonly requestHeaders: Readonly<Record<string, string>>;
  /** The tested skill's listed description, and every skill the body lists. */
  readonly listed: string;
  readonly listing: readonly { readonly name: string; readonly description: string }[];
  /** The other requests the run sent, each answered by the recorder. */
  readonly otherRequests: readonly { readonly path: string; readonly bytes: number }[];
  readonly context: ContextRecord;
  readonly runMs: number;
}

/** A task's captured body, the headers it goes with, and what the host lists for the skill. */
interface Captured {
  readonly body: RequestBody;
  readonly headers: Readonly<Record<string, string>>;
  readonly listed: string;
}

/** The scratch Claude Code home of one test: the environment each run gets, the working
 *  directory, the appended system prompt, Claude Code's version, and the context's record. */
interface ClaudeSetup {
  readonly env: Record<string, string>;
  readonly workspace: string;
  readonly prompt: string | undefined;
  readonly version: string;
  readonly context: ContextRecord;
}

/** A task's runs after `runTask`: every run saved, and each run that failed or that the budget
 *  refused. */
interface TaskRuns {
  readonly runs: LoadRun[];
  readonly failures: string[];
  readonly budgetSpent: boolean;
}

/** What a load test reads and writes beside its inputs. */
export interface LoadIo {
  /** Writes `text` to standard output as it is. */
  out(text: string): void;
  /** Writes one line to standard error. */
  err(line: string): void;
  /** The process's peak resident memory so far, in bytes. */
  peakRssBytes(): number;
}

export interface LoadOptions {
  readonly io: LoadIo;
  readonly host: Host;
  /** The skill's directory as written. */
  readonly skillDir: string;
  /** A digest of that directory's files, which the settings of `--out` hold. */
  readonly skillDigest: string;
  /** The name it is tested under, when not its own. */
  readonly name?: string;
  readonly tasksFile: string;
  readonly profileFile: string;
  readonly runs: number;
  readonly out: string;
  /** The model (`anthropic/<id>`, or `<id>`) and the thinking level. */
  readonly model: string;
  readonly thinking: ThinkingLevel;
  /** Where the runs go: counted and logged. */
  readonly gateway: Gateway;
  /** Writes Oh My Pi's hints (capture.ts's `gatewayHints`). */
  readonly hints: HintWriter;
  /** Where the profile's skills, plugins and context files are read. */
  readonly source: RepoSource;
  /** The scratch directory, and the cache of context.ts's builds and of the hints. */
  readonly scratch: string;
  readonly cacheDir: string;
  /** Oh My Pi: its binary; the `models.yml` a capture needs (a missing capture fails without it);
   *  the model that writes hints; settings files laid over the scratch profile's. */
  readonly omp: string;
  readonly models?: string;
  readonly hintModel?: string;
  readonly configFiles: readonly string[];
  /** Claude Code's binary. */
  readonly claude: string;
  /** The request log, named in the summary. */
  readonly requestLog: string;
  /** The most the peak resident memory may reach: a task that ends above it stops the test. */
  readonly memoryLimitBytes: number;
  /** The longest one host run may take. */
  readonly timeoutMs?: number;
}

/** What the test came to: each task or run that failed. */
export interface LoadResult {
  readonly failures: readonly string[];
}

/** The settings of a test that an `--out` holds: a test of other settings is refused there. */
interface Settings {
  readonly skillText: string;
  readonly name: string;
  readonly renamedFrom: string | null;
  readonly host: Host;
  readonly model: string;
  readonly thinking: ThinkingLevel;
  readonly runs: number;
  readonly hintModel: string | null;
  readonly profile: LoadProfile;
}

/** A test's summary, as `summary.json` holds it. Only what a later command checks is read back. */
const SavedSummary = z.looseObject({
  settings: z.unknown(),
  tasks: z.array(z.looseObject({ id: z.string(), text: z.string() })),
});

/** Runs `argv` to its end with `env` and returns its standard output; throws, naming it and its
 *  last output lines, when it fails. */
async function mustRun(argv: readonly string[], env: Record<string, string>): Promise<string> {
  const child = Bun.spawn([...argv], { env, stdin: "ignore", stdout: "pipe", stderr: "pipe" });
  const [stdout, stderr, code] = await Promise.all([
    new Response(child.stdout).text(),
    new Response(child.stderr).text(),
    child.exited,
  ]);
  if (code !== 0) {
    const tail = `${stdout}\n${stderr}`.trim().split("\n").slice(-12).join("\n");
    throw new Error(`\`${argv.join(" ")}\` failed with exit ${code}: ${tail}`);
  }
  return stdout;
}

/** The dollars the hint calls of an Oh My Pi capture's record cost (each call's `usage` is
 *  gateway.ts's `Spent`). */
function hintUsd(record: CaptureRecord): number {
  return record.hintCalls.reduce((sum, call) => {
    const usd: unknown =
      typeof call.usage === "object" && call.usage !== null ? Reflect.get(call.usage, "usd") : 0;
    return sum + (typeof usd === "number" ? usd : 0);
  }, 0);
}

/** One load test, as `runLoad` runs it: its options, settings, scratch root and tested skill. */
class LoadTest {
  readonly tested: TestedSkill;
  private claudeReady: Promise<ClaudeSetup> | undefined;

  constructor(
    readonly o: LoadOptions,
    readonly settings: Settings,
    readonly root: string,
    readonly skillDir: string
  ) {
    const plugin = settings.profile.claude?.skillPlugin;
    this.tested =
      o.host === "claude" && plugin !== undefined
        ? { name: settings.name, plugin }
        : { name: settings.name };
  }

  say(line: string): void {
    this.o.io.out(`load: ${line}\n`);
  }

  /** The case a capture of `id` reads, and the arm it installs: the profile's skills and the
   *  tested skill from its directory, with `text` as the one message. The tested skill's entry
   *  names the local directory: context.ts installs an arm's own skill from `skillDir` and reads
   *  no repository for it. */
  caseOf(id: string, text: string): { c: CaptureCase; arm: Arm } {
    const { profile, name, model, thinking } = this.settings;
    const own: ProfileSkill = {
      repo: "local/skill-directory",
      commit: this.settings.skillText,
      path: this.skillDir,
      name,
    };
    const caseProfile: Profile = {
      skills: [...profile.skills, own],
      plugins: profile.plugins,
      contextFiles: profile.contextFiles,
      ...(profile.appendSystemPrompt === undefined
        ? {}
        : { appendSystemPrompt: profile.appendSystemPrompt }),
    };
    return {
      c: {
        id,
        skill: name,
        model,
        thinking,
        context: profile.context,
        profile: caseProfile,
        sessionDigest: null,
      },
      arm: {
        name: ARM,
        skill: name,
        skillDir: this.skillDir,
        installedDir: path.join("/skills", name),
        messages: [
          {
            role: "user",
            content: [{ type: "text", text }],
            attribution: "user",
            timestamp: Date.now(),
          },
        ],
        profileSkills: caseProfile.skills,
      },
    };
  }

  /** Task `task`'s body through Oh My Pi, captured or read back. */
  async ompCapture(task: Task): Promise<Captured> {
    const files = loadCapturePaths(this.o.out, "omp", task.id);
    if (this.o.models === undefined && !(existsSync(files.body) && existsSync(files.record))) {
      throw new Error(
        `it has no capture in ${path.dirname(files.body)}, and no --models is given to make one`
      );
    }
    const { c, arm } = this.caseOf(task.id, task.text);
    const capture = await captureBody(c, arm, {
      omp: this.o.omp,
      outDir: path.dirname(files.body),
      scratchDir: path.join(this.root, "captures"),
      cacheDir: this.o.cacheDir,
      source: this.o.source,
      models: this.o.models ?? "",
      hints: this.o.hints,
      ...(this.o.hintModel === undefined ? {} : { hintModel: this.o.hintModel }),
      configFiles: this.o.configFiles,
      ...(this.o.timeoutMs === undefined ? {} : { timeoutMs: this.o.timeoutMs }),
    });
    const listed = capture.record.listing.find((entry) => entry.name === this.tested.name)?.hint;
    if (listed === undefined) throw new Error(`its capture does not list ${this.tested.name}`);
    return { body: capture.body, headers: capture.record.requestHeaders, listed };
  }

  /** The scratch Claude Code home of this test, made once (ClaudeSetup): the profile's skills
   *  and the tested one laid out by context.ts, and a local marketplace of the layout's plugins
   *  installed into a scratch CLAUDE_CONFIG_DIR. */
  claudeSetup(): Promise<ClaudeSetup> {
    this.claudeReady ??= (async () => {
      const layout = this.settings.profile.claude;
      if (layout === undefined) throw new Error("the profile has no Claude Code layout");
      const root = path.join(this.root, "claude");
      rmSync(root, { recursive: true, force: true });
      const { c, arm } = this.caseOf("claude-profile", "");
      // Oh My Pi's plugins are no Claude Code plugins: only the skills and context files go in.
      const context = await buildContext({ ...c, profile: { ...c.profile, plugins: [] } }, arm, {
        source: this.o.source,
        cacheDir: this.o.cacheDir,
        root: path.join(root, "context"),
      });
      const plugins = layout.plugins.map((plugin) => ({ ...plugin, skills: [...plugin.skills] }));
      const own = plugins.find((plugin) => plugin.name === layout.skillPlugin);
      if (own === undefined) plugins.push({ name: layout.skillPlugin, skills: [this.tested.name] });
      else own.skills.push(this.tested.name);
      const market = path.join(root, "marketplace");
      writeWhole(
        path.join(market, ".claude-plugin", "marketplace.json"),
        json({
          name: layout.marketplace,
          owner: { name: layout.marketplace },
          plugins: plugins.map((plugin) => ({
            name: plugin.name,
            source: `./plugins/${plugin.name}`,
          })),
        })
      );
      for (const plugin of plugins) {
        const dir = path.join(market, "plugins", plugin.name);
        writeWhole(
          path.join(dir, ".claude-plugin", "plugin.json"),
          json({
            name: plugin.name,
            version: "0.0.0",
            description: `The ${plugin.name} plugin of a skill-cuts load test's profile.`,
          })
        );
        for (const name of plugin.skills) {
          const installed = context.skills.find((skill) => skill.name === name);
          if (installed === undefined) throw new Error(`the context installs no skill ${name}`);
          cpSync(installed.dir, path.join(dir, "skills", name), {
            recursive: true,
            verbatimSymlinks: true,
          });
        }
      }
      const home = path.join(root, "home");
      const tmp = path.join(root, "tmp");
      const config = path.join(root, "config");
      for (const dir of [home, tmp, config]) mkdirSync(dir, { recursive: true });
      const env: Record<string, string> = {
        HOME: home,
        PATH: "/usr/local/bin:/usr/bin:/bin",
        TMPDIR: tmp,
        TZ: "UTC",
        CLAUDE_CONFIG_DIR: config,
        DISABLE_AUTOUPDATER: "1",
        DISABLE_TELEMETRY: "1",
        DISABLE_ERROR_REPORTING: "1",
        CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: "1",
      };
      const version = (await mustRun([this.o.claude, "--version"], env)).trim();
      await mustRun([this.o.claude, "plugin", "marketplace", "add", market], env);
      for (const plugin of plugins) {
        await mustRun(
          [this.o.claude, "plugin", "install", `${plugin.name}@${layout.marketplace}`],
          env
        );
      }
      return {
        env,
        workspace: context.workspace,
        prompt:
          context.appendSystemPrompt === undefined
            ? undefined
            : readFileSync(context.appendSystemPrompt, "utf8"),
        version,
        context: context.record,
      };
    })();
    return this.claudeReady;
  }

  /** Task `task`'s body through Claude Code, captured or read back. */
  async claudeCapture(task: Task): Promise<Captured> {
    const files = loadCapturePaths(this.o.out, "claude", task.id);
    if (existsSync(files.body) && existsSync(files.record)) {
      const record = JSON.parse(readFileSync(files.record, "utf8")) as ClaudeCaptureRecord;
      return {
        body: JSON.parse(readFileSync(files.body, "utf8")) as MessagesBody,
        headers: record.requestHeaders,
        listed: record.listed,
      };
    }
    const setup = await this.claudeSetup();
    const { thinking } = this.settings;
    const model = this.settings.model.replace(/^anthropic\//, "");
    const logs = path.join(this.root, "claude", "runs", cacheKey(task.id).slice(0, 16));
    mkdirSync(logs, { recursive: true });
    const stderrFile = path.join(logs, "stderr.txt");
    const recorder = startRecorder();
    const started = performance.now();
    let requests: RecordedRequest[];
    try {
      const child = Bun.spawn(
        [
          this.o.claude,
          "-p",
          "--output-format",
          "stream-json",
          "--verbose",
          "--model",
          model,
          ...(CLAUDE_EFFORTS.includes(thinking) ? ["--effort", thinking] : []),
          ...(setup.prompt === undefined ? [] : ["--append-system-prompt", setup.prompt]),
          // After `--`, so a task's text that starts with `-` is read as the prompt.
          "--",
          task.text,
        ],
        {
          cwd: setup.workspace,
          env: {
            ...setup.env,
            ANTHROPIC_BASE_URL: recorder.url,
            ANTHROPIC_API_KEY: RECORDER_CREDENTIAL,
            // A request for a smaller model asks for the tested one; the recorder answers each.
            ANTHROPIC_DEFAULT_HAIKU_MODEL: model,
            ANTHROPIC_SMALL_FAST_MODEL: model,
            ...(thinking === "off" ? { CLAUDE_CODE_DISABLE_THINKING: "1" } : {}),
          },
          stdin: "ignore",
          stdout: Bun.file(path.join(logs, "stream.jsonl")),
          stderr: Bun.file(stderrFile),
        }
      );
      const timeoutMs = this.o.timeoutMs ?? RUN_TIMEOUT_MS;
      const timer = setTimeout(() => child.kill("SIGKILL"), timeoutMs);
      const code = await child.exited;
      clearTimeout(timer);
      if (code !== 0) {
        const tail = readFileSync(stderrFile, "utf8").trim().split("\n").slice(-12).join("\n");
        throw new Error(`Claude Code ended with ${child.signalCode ?? `exit ${code}`}: ${tail}`);
      }
      requests = [...recorder.requests];
    } finally {
      await recorder.close();
    }
    const runMs = Math.round(performance.now() - started);
    const turn = requests.find((request) => {
      if (request.method !== "POST" || request.path.split("?")[0] !== "/v1/messages") return false;
      const body: unknown = JSON.parse(request.body);
      const tools = typeof body === "object" && body !== null ? Reflect.get(body, "tools") : null;
      return Array.isArray(tools) && bodyTexts(body).some((text) => text.includes(task.text));
    });
    if (turn === undefined) throw new Error("Claude Code sent no model request that holds it");
    const body = JSON.parse(turn.body) as MessagesBody;
    const listing = claudeListing(body);
    const key = `${this.tested.plugin}:${this.tested.name}`;
    const listed = listing.get(key);
    if (listed === undefined) {
      const names = [...listing.keys()].join(", ") || "no skill";
      throw new Error(`its captured body is refused: Claude Code lists ${names}, not ${key}`);
    }
    const record: ClaudeCaptureRecord = {
      task: task.id,
      capturedAt: new Date().toISOString(),
      claude: { binary: this.o.claude, version: setup.version },
      model,
      thinking,
      skill: this.tested,
      requestPath: turn.path,
      requestHeaders: Object.fromEntries(
        Object.entries(turn.headers).filter(([name]) => name.startsWith("anthropic-"))
      ),
      listed,
      listing: [...listing].map(([name, description]) => ({ name, description })),
      otherRequests: requests
        .filter((request) => request !== turn)
        .map((request) => ({ path: request.path, bytes: request.body.length })),
      context: setup.context,
      runMs,
    };
    writeWhole(files.body, turn.body);
    writeWhole(files.record, json(record));
    return { body, headers: record.requestHeaders, listed };
  }

  /** The runs task `id` has saved. Throws a LoadRefused when its file holds a line that is not
   *  its next run. */
  savedRuns(id: string): LoadRun[] {
    const file = loadRunFile(this.o.out, id);
    if (!existsSync(file)) return [];
    const runs: LoadRun[] = [];
    for (const [index, line] of readFileSync(file, "utf8").split("\n").entries()) {
      if (line.trim() === "") continue;
      let raw: unknown;
      try {
        raw = JSON.parse(line);
      } catch {
        raw = undefined;
      }
      const parsed = LoadRun.safeParse(raw);
      if (!parsed.success || parsed.data.task !== id || parsed.data.run !== runs.length + 1) {
        throw new LoadRefused(`${file}:${index + 1} is not run ${runs.length + 1} of task ${id}`);
      }
      runs.push(parsed.data);
    }
    return runs;
  }

  /** The run of `task` numbered `run` that `reply` is, as its run file keeps it. */
  runOf(task: Task, run: number, reply: Reply, listed: string): LoadRun {
    const first = firstMessageLoad(this.o.host, reply.message.content, this.tested);
    return {
      task: task.id,
      run,
      label: task.label,
      source: task.source,
      host: this.o.host,
      name: this.tested.name,
      loaded: first.loaded,
      loadedBy: first.loadedBy,
      toolCalls: [...first.toolCalls],
      stopReason: reply.message.stop_reason,
      requestId: reply.requestId ?? null,
      usage: { ...reply.spent },
      listed,
    };
  }

  /** Makes the runs `task` lacks beside `saved`: the first alone and the rest after it at once
   *  (the body is the same, so the cache the first one writes serves the others), and saves its
   *  run file when they end. */
  async runTask(task: Task, saved: readonly LoadRun[]): Promise<TaskRuns> {
    const captured =
      this.o.host === "omp" ? await this.ompCapture(task) : await this.claudeCapture(task);
    const send = () => this.o.gateway.send(captured.body, { headers: captured.headers });
    const opening = await Promise.allSettled([send()]);
    const others = await Promise.allSettled(
      Array.from({ length: this.settings.runs - saved.length - 1 }, send)
    );
    const runs = [...saved];
    const failures: string[] = [];
    let budgetSpent = false;
    for (const result of [...opening, ...others]) {
      if (result.status === "fulfilled") {
        runs.push(this.runOf(task, runs.length + 1, result.value, captured.listed));
      } else if (result.reason instanceof BudgetSpent) budgetSpent = true;
      else failures.push(`task ${task.id}: a run failed: ${messageFor(result.reason)}`);
    }
    writeWhole(
      loadRunFile(this.o.out, task.id),
      runs.map((run) => `${JSON.stringify(run)}\n`).join("")
    );
    return { runs, failures, budgetSpent };
  }
}

/** Task `task`'s verdict in words. */
function verdictWords(task: Task, outcome: TaskOutcome, wanted: number): string {
  if (!outcome.complete) {
    return `${outcome.made} of ${wanted} runs made; run the command again for the rest`;
  }
  if (outcome.fault === null) return `no verdict: R6's rule is of ${LOAD_RUNS} runs a task`;
  if (!outcome.fault) return "no fault";
  return task.label === "need"
    ? `a fault: a need task loads it in fewer than ${NEED_MIN_LOADS} of ${LOAD_RUNS} runs`
    : `a fault: a near-miss task loads it in more than ${NEAR_MISS_MAX_LOADS} of ${LOAD_RUNS} runs`;
}

/** Runs the load test `o` names (see the file's header). Throws a LoadRefused before any model
 *  call when an input is refused, and a LoadStopped, after saving, when the budget is spent or the
 *  peak memory passes `o.memoryLimitBytes`. */
export async function runLoad(o: LoadOptions): Promise<LoadResult> {
  const given = path.resolve(o.skillDir);
  if (!statSync(path.join(given, "SKILL.md"), { throwIfNoEntry: false })?.isFile()) {
    throw new LoadRefused(`--skill ${o.skillDir} holds no SKILL.md`);
  }
  const own = skillName(given);
  const name = o.name ?? own;
  if (!NAME.test(name)) {
    throw new LoadRefused(`${JSON.stringify(name)} is not a name a skill can be listed under`);
  }
  if (skillFrontmatter(given).hidden) {
    throw new LoadRefused(
      `${o.skillDir} sets hide or disable-model-invocation, so no host lists it for its agent to load`
    );
  }
  if (!Number.isInteger(o.runs) || o.runs < 1) {
    throw new LoadRefused(`--runs ${o.runs} is not a whole number of 1 or more`);
  }
  const slash = o.model.indexOf("/");
  const provider = slash < 0 ? "anthropic" : o.model.slice(0, slash);
  const modelId = o.model.slice(slash + 1);
  if (provider !== "anthropic" && provider !== "openai") {
    throw new LoadRefused(
      `--model ${o.model}: the runs go to the gateway's Anthropic Messages route (anthropic/<id>) or its OpenAI Responses route (openai/<id>)`
    );
  }
  if (provider === "openai" && o.host === "claude") {
    throw new LoadRefused(
      `--model ${o.model}: Claude Code calls the Anthropic Messages API only, so its model is anthropic/<id>`
    );
  }
  if (PRICES[modelId] === undefined) {
    throw new LoadRefused(
      `gateway.ts has no list price for ${modelId}, so the runs' dollars could not be counted`
    );
  }
  if (o.host === "claude" && o.thinking === "minimal") {
    throw new LoadRefused(
      `Claude Code has no thinking level minimal: it takes off, auto, or ${CLAUDE_EFFORTS.join(", ")} (--effort)`
    );
  }
  const tasks = loadTasks(o.tasksFile);
  const profile = loadProfile(o.profileFile, o.host, name);
  const settings: Settings = {
    skillText: o.skillDigest,
    name,
    renamedFrom: name === own ? null : own,
    host: o.host,
    model: `${provider}/${modelId}`,
    thinking: o.thinking,
    runs: o.runs,
    hintModel: o.host === "omp" ? (o.hintModel ?? null) : null,
    profile,
  };

  const summaryFile = path.join(o.out, "summary.json");
  if (existsSync(summaryFile)) {
    let raw: unknown;
    try {
      raw = JSON.parse(readFileSync(summaryFile, "utf8"));
    } catch {
      raw = undefined;
    }
    const saved = SavedSummary.safeParse(raw);
    if (!saved.success) throw new LoadRefused(`${summaryFile} is not a load test's summary`);
    if (JSON.stringify(saved.data.settings) !== JSON.stringify(settings)) {
      throw new LoadRefused(
        `${o.out} holds a load test of other settings (${JSON.stringify(saved.data.settings)}) than this one's (${JSON.stringify(settings)}): use another --out`
      );
    }
    for (const task of tasks) {
      const before = saved.data.tasks.find((each) => each.id === task.id);
      if (before !== undefined && before.text !== task.text) {
        throw new LoadRefused(
          `${o.out} holds runs of a task ${task.id} with another text: give the new text another id, or use another --out`
        );
      }
    }
  }

  const root = path.join(o.scratch, `load-${cacheKey(JSON.stringify(settings)).slice(0, 16)}`);
  const test = new LoadTest(
    o,
    settings,
    root,
    settings.renamedFrom === null ? given : renamedSkill(given, name, path.join(root, "skill"))
  );
  const runsOf = new Map(tasks.map((task) => [task.id, test.savedRuns(task.id)]));
  const outcomes = new Map(
    tasks.map((task) => [task.id, taskOutcome(task.label, runsOf.get(task.id) ?? [], o.runs)])
  );
  const outcomeOf = (task: Task): TaskOutcome => outcomes.get(task.id) as TaskOutcome;
  let peak = 0;
  /** Writes the summary as the runs stand; returns the skill's verdict and the test's dollars so
   *  far at list price, its runs' and its hints'. */
  const writeSummary = (): { verdict: LoadVerdict; usd: number } => {
    const verdict = skillOutcome(
      tasks.map((task) => ({ id: task.id, label: task.label, outcome: outcomeOf(task) })),
      o.runs
    );
    const runs = [...runsOf.values()].flat();
    const runsUsd = runs.reduce((sum, run) => sum + run.usage.usd, 0);
    let hintsUsd = 0;
    for (const task of o.host === "omp" ? tasks : []) {
      const record = loadCapturePaths(o.out, "omp", task.id).record;
      if (existsSync(record)) {
        hintsUsd += hintUsd(JSON.parse(readFileSync(record, "utf8")) as CaptureRecord);
      }
    }
    writeWhole(
      summaryFile,
      json({
        settings,
        skillDir: given,
        tested: {
          name,
          renamedFrom: settings.renamedFrom,
          changed:
            settings.renamedFrom === null
              ? "nothing"
              : "the frontmatter name and the directory's name, in a scratch copy; nothing else that names the skill",
        },
        listed: runs[0]?.listed ?? null,
        tasks: tasks.map((task) => ({ ...task, ...outcomeOf(task) })),
        verdict,
        spend: { runsUsd, hintsUsd, usd: runsUsd + hintsUsd },
        peakRssBytes: peak,
        requestLog: o.requestLog,
        updatedAt: new Date().toISOString(),
      })
    );
    return { verdict, usd: runsUsd + hintsUsd };
  };
  writeSummary();

  const count = (label: TaskLabel) => tasks.filter((task) => task.label === label).length;
  test.say(
    `the skill ${name} on ${o.host === "omp" ? "Oh My Pi" : "Claude Code"}, model ${settings.model}, thinking ${o.thinking}: ${count("need")} need and ${count("near-miss")} near-miss tasks, ${o.runs} runs each`
  );
  if (settings.renamedFrom !== null) {
    test.say(
      `${own} is tested under the name ${name}: its frontmatter name and its directory's name are changed in a scratch copy, and nothing else that names the skill changes (its own text, its references, other skills and code still say ${own})`
    );
  }
  const failures: string[] = [];
  let listedSaid = false;
  for (const task of tasks) {
    const saved = runsOf.get(task.id) ?? [];
    const what = `task ${task.id} (${task.label}, ${task.source === "session" ? "from a session" : "written"})`;
    if (saved.length >= o.runs) {
      const outcome = outcomeOf(task);
      test.say(
        `${what}: its run file is complete, skipped: loaded in ${outcome.loads} of ${outcome.made} runs: ${verdictWords(task, outcome, o.runs)}`
      );
      continue;
    }
    let ran: TaskRuns;
    try {
      ran = await test.runTask(task, saved);
    } catch (error) {
      if (error instanceof LoadRefused) throw error;
      failures.push(`task ${task.id}: ${messageFor(error)}`);
      o.io.err(`load: ${what}: ${messageFor(error)}`);
      continue;
    }
    runsOf.set(task.id, ran.runs);
    const outcome = taskOutcome(task.label, ran.runs, o.runs);
    outcomes.set(task.id, outcome);
    const listed = ran.runs[0]?.listed;
    if (!listedSaid && listed !== undefined) {
      listedSaid = true;
      test.say(
        o.host === "omp"
          ? `Oh My Pi lists ${name} under the hint ${JSON.stringify(listed)}`
          : `Claude Code lists ${test.tested.plugin}:${name} as ${JSON.stringify(listed)}`
      );
    }
    const each = ran.runs.map((run) => `run ${run.run} ${run.loaded ? "loads" : "does not"}`);
    test.say(
      `${what}: loaded in ${outcome.loads} of ${outcome.made} runs (${each.join(", ")}): ${verdictWords(task, outcome, o.runs)}`
    );
    for (const failure of ran.failures) {
      failures.push(failure);
      o.io.err(`load: ${failure}`);
    }
    peak = Math.max(peak, o.io.peakRssBytes());
    writeSummary();
    if (ran.budgetSpent) {
      throw new LoadStopped(
        `the budget is spent, so no new model call starts; the runs so far are saved under ${o.out}`
      );
    }
    if (peak > o.memoryLimitBytes) {
      throw new LoadStopped(
        `the runner's peak resident memory, ${Math.round(peak / 2 ** 20)} MiB, is above 1 GiB, so the test stops after task ${task.id}; its runs are saved under ${o.out}`
      );
    }
  }
  peak = Math.max(peak, o.io.peakRssBytes());
  const { verdict, usd } = writeSummary();
  const small =
    count("need") < MIN_TASKS_PER_LABEL || count("near-miss") < MIN_TASKS_PER_LABEL
      ? ` (R6's test has at least ${MIN_TASKS_PER_LABEL} need and ${MIN_TASKS_PER_LABEL} near-miss tasks; this one has ${count("need")} and ${count("near-miss")})`
      : "";
  test.say(
    `the skill ${name}: ${verdict.reason}${small}; the test's runs and hints so far cost $${usd.toFixed(4)} at list price; written to ${summaryFile}`
  );
  return { failures };
}
