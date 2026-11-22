/**
 * The runner's readers (the `read` command's prompts), and the one way each prompt of `prompts/`
 * reaches the model, which judge.ts takes too.
 *
 * A prompt file, `prompts/<name>.md`, is a header and a text. The header, between two `---` lines,
 * pins every setting of the model call, one `<key>: <value>` per line: `model`, `thinking_budget`
 * (the thinking budget in tokens), `max_tokens`, `temperature` (1, the only value the Messages API
 * takes with thinking on) and `effort` (`output_config.effort`). Each request made with the prompt
 * sends all five (`promptBody`), so no setting is left to the provider's default, and a request
 * log of the bodies shows each one. The text is the system prompt.
 *
 * A reader is one prompt with a zod schema of its input and one of its answer: one plain Messages
 * API call through the gateway per input batch (`runReader`), whose answer is the JSON object the
 * reply's text holds (whole, or in one fenced block). A reply whose answer does not parse, or that
 * the schema refuses, is asked again, READER_TRIES times in all; each refusal is kept and returned,
 * so a run can count them (a names answer refused for a reason that is only that the job is not
 * clear from the name alone says NAME_ONLY_REFUSAL). An answer's schema is built for its input
 * (`Reader.answer`), so it also refuses an answer that names an id, a file, a line or a message the
 * input does not hold.
 *
 * The readers, each with the input it reads and the answer it gives:
 * - `case`: the lines of a skill's base text a case may name (those the session's own read
 *   of each file held, each with its number in the file; a file the session did not read is not
 *   shown) and the session around the skill's read; the first step the skill governs, its message
 *   index, the line it exercises, a criterion stating pass and fail, a violating step (a plausible
 *   next step that breaks the line and otherwise looks valid, which the judge must fail), and each
 *   message before the step that states the line's instruction in any wording, with words quoted
 *   from it; or no step. The case builder drops a proposal whose line is not a whole line of the
 *   base text, or of the session's read of its file, or that names a restatement.
 * - `restate`: a case's line and one window of the messages before its step (a long message
 *   in parts); each message that states the line's instruction in any wording, with words quoted
 *   from it. The case builder reads every message before the step this way, window by window, and
 *   drops a case on any.
 * - `audit`: a case's line, the messages before its step as the judge reads them, and its
 *   real step, never its criterion; whether the step follows the line, breaks it, or is not a
 *   step the line governs, with a reason. The case builder keeps a case only when it follows.
 * - `map`: a skill's base and cut texts and the rule-by-rule audit of the cut, if any; each rule
 *   of the base text with its line in the cut text or why it is gone, called kept, narrowed,
 *   widened or lost, with the audit's words where the audit answers it.
 * - `attack`: a skill's cut text; each vague phrase, undefined term, rule nobody can check and
 *   line that reads two ways, by file and line.
 * - `same-job`: every skill's id, repository, name and description; each pair whose
 *   descriptions give the same task to load them for.
 * - `sentence`: skills' SKILL.md texts; each place one holds the sentence "disagrees with the
 *   code" in any wording.
 * - `names`: every skill's name and description, its load test's fault if any, and the names
 *   in use; each skill nominated (with its risk, the task, the reason and one to three new names)
 *   or not. Each skill with a fault is nominated.
 * - `tasks`: a skill's name and description and first user messages of real sessions; search
 *   terms, and at least TASKS_PER_LABEL need and near-miss tasks, each labelled, each taken from a
 *   message by its id or written and marked written. Each need task says whether the skill governs
 *   its first step (`firstStep`: an agent doing it would read the skill at once, before any other
 *   act), and at least TASKS_PER_LABEL need tasks are ones it governs; the schema refuses a need
 *   task that does not say. In `control` mode each message is a need task of the positive control
 *   (a session whose first assistant message read the skill): the answer takes every message, once,
 *   as a need task, and says only whether the skill governs its first step.
 * - `craft`: one skill's files, the style text's rules with their ids (style.ts), the craft list's
 *   properties that a reader checks (never one a behaviour test checks: `craftInput`), and each
 *   nearest skill of theirs with its files; each style rule and reader property judged once (met,
 *   gap or not applicable), and each gap with its file, its line (for a missing element, the line
 *   it belongs after, 0 before line 1), the rule it is against (`S<n>`, `C<n>`, or
 *   `nearest:<skill>:<path>:<line>`, a line of a nearest skill), its kind (`present` or `missing`)
 *   and why. A rule judged gap has a gap, and a gap of a style rule or property is of a rule
 *   judged gap.
 * - `nearest`: one skill's name, whole description and SKILL.md body, and candidate skills (name,
 *   source and whole description of each); the nearest one or two candidates, each by its name and
 *   source with a verdict (`equivalent` or `partial`), what it lacks of the skill's job and a
 *   reason, and a verdict over them (`none` exactly when it names none, else the best entry's).
 * - `claim`: a claim about a skill, one gap of a craft answer and the skill's files; whether the
 *   gap states the claim, with a reason (`control craft`).
 * - `proposal-target`: the whole diff of a closed pull request with each file's hunks (the part
 *   every item of that pull request shares, sent first and marked for the prompt cache:
 *   `Reader.cached`), then one item of a list of a later pull request's body (its id, state and
 *   text), the repository a file target is in, and each skill the readers read (its repository
 *   and name); the hunks that make the item, each a file of the diff and a hunk's index in it, and
 *   its targets, each a file of the repository by its path, a skill of the list, or none, with why.
 * - `proposal`: the item, its hunks, each target's text today (a skill's files as
 *   `<skill>/<path>`, a file of the repository, or that the repository holds no such file), the
 *   gaps of each target skill's findings entry, numbered across them, the style rules and the
 *   reader properties; the gaps the change fixes (each a finding by its number, or a style rule or
 *   property), the gaps it makes (each a rule), each with its file and line where given, and a
 *   one-line summary.
 * - `written-case`: a skill's base text, its session cases (each one's real step and criterion;
 *   none, when it has none) and the first user message of a recorded session that read it, and how
 *   many tasks to write; at most that many tasks of that session's kind of work in which a line of
 *   the skill decides the agent's first step after it reads the skill, each with the task (the
 *   user's message), the line (its file, number and text: a line of the base text, which the schema
 *   checks), a criterion, a step that follows the line and a violating step (`build-cases
 *   --written`).
 *
 * The positive controls of the map, attack, same-job and sentence readers (`runControl`) each
 * plant a known finding in a copy of real input, run the reader once, and hold only when its
 * answer names that finding.
 */
import { createHash } from "node:crypto";
import { readdirSync, readFileSync } from "node:fs";
import path from "node:path";
import { messageFor } from "@legion/envoy-client/errors";
import { z } from "zod";
import { textLines } from "./cases";
import { type CraftList, readerProperties } from "./craftlist";
import type { Gateway, MessagesBody, ReplyMessage, Spent } from "./gateway";
import { TaskLabel } from "./score";
import type { StyleRule } from "./style";

/** The directory of the prompt files. */
export const PROMPTS_DIR = path.join(import.meta.dir, "prompts");
/** The most times a reader asks for one batch before it fails. */
export const READER_TRIES = 3;
/** The fewest tasks of each label a load test takes: at least 5 need and 5 near-miss. */
export const TASKS_PER_LABEL = 5;
/** What a names answer is refused for when a nomination's reason or task says only that the job is
 *  not clear from the name alone; a run counts the refusals that hold it. */
export const NAME_ONLY_REFUSAL = "says only that the job is not clear from the name alone";

/** The settings a prompt file's header pins. With thinking on, the Messages API takes temperature
 *  1 only, and a thinking budget of 1024 tokens or more, below `max_tokens`. */
export const PromptSettings = z
  .strictObject({
    model: z.string().min(1),
    thinking_budget: z.int().min(1024),
    max_tokens: z.int().positive(),
    temperature: z.literal(1),
    effort: z.enum(["low", "medium", "high", "max"]),
  })
  .refine((settings) => settings.thinking_budget < settings.max_tokens, {
    message: "is not below max_tokens",
    path: ["thinking_budget"],
  });
type PromptSettings = z.infer<typeof PromptSettings>;

/** A prompt file: its name (the file's, less `.md`), the settings its header pins, and its text. */
export interface Prompt {
  readonly name: string;
  readonly settings: PromptSettings;
  readonly text: string;
}

const HEADER = /^---\n([\s\S]*?)\n---\n/;
const HEADER_LINE = /^([a-z_]+): *(\S(?:.*\S)?) *$/;
const NUMBER = /^-?\d+(?:\.\d+)?$/;

/** The issues of `list` under the path `at`. An answer no option of a union takes is read as the
 *  option it came closest to, the one with the fewest issues, so the refusal names what is
 *  wrong. */
function issueLines(list: readonly z.core.$ZodIssue[], at: readonly PropertyKey[]): string[] {
  return list.flatMap((issue) => {
    const where = [...at, ...issue.path];
    if (issue.code === "invalid_union" && issue.errors.length > 0) {
      const closest = issue.errors.reduce((best, each) =>
        each.length < best.length ? each : best
      );
      return issueLines(closest, where);
    }
    return [`${where.map(String).join(".") || "the value"}: ${issue.message}`];
  });
}

/** Each issue of `error`, as `<path>: <message>`, joined. */
function issues(error: z.ZodError): string {
  return issueLines(error.issues, []).join("; ");
}

/** The prompt file `file` (the text of `prompts/<name>.md`). Throws naming the file when its header
 *  is missing, holds a line that is not `<key>: <value>`, gives a key twice, or does not pin each
 *  setting of PromptSettings and nothing else, or when no text follows it. */
export function parsePrompt(name: string, file: string): Prompt {
  const where = `prompts/${name}.md`;
  const header = HEADER.exec(file);
  if (header === null) {
    throw new Error(
      `${where} has no header: a line "---", one "<key>: <value>" per setting, and a line "---"`
    );
  }
  const fields: Record<string, string | number> = {};
  for (const line of (header[1] ?? "").split("\n")) {
    const field = HEADER_LINE.exec(line);
    if (field === null) throw new Error(`${where}: a header line is not "<key>: <value>": ${line}`);
    const [, key = "", value = ""] = field;
    if (key in fields) throw new Error(`${where}: the header gives ${key} twice`);
    fields[key] = NUMBER.test(value) ? Number(value) : value;
  }
  const settings = PromptSettings.safeParse(fields);
  if (!settings.success)
    throw new Error(`${where}: the header is refused: ${issues(settings.error)}`);
  const text = file.slice(header[0].length).trim();
  if (text === "") throw new Error(`${where} holds no prompt text after its header`);
  return { name, settings: settings.data, text };
}

/** A prompt as `loadPrompt` read it, with the SHA-256 of the file's bytes it parsed. */
export interface LoadedPrompt extends Prompt {
  readonly sha256: string;
}

const prompts = new Map<string, LoadedPrompt>();

/** The prompt `prompts/<name>.md`, read once per process: its bytes parsed (`parsePrompt`) and
 *  hashed, so the hash is of the text every request with it sends. */
export function loadPrompt(name: string): LoadedPrompt {
  let prompt = prompts.get(name);
  if (prompt === undefined) {
    const bytes = readFileSync(path.join(PROMPTS_DIR, `${name}.md`));
    prompt = {
      ...parsePrompt(name, bytes.toString("utf8")),
      sha256: createHash("sha256").update(bytes).digest("hex"),
    };
    prompts.set(name, prompt);
  }
  return prompt;
}

/** One text block of a request's user message. `cache` marks it for the prompt cache: a part that
 *  every request of a batch shares, sent before the part that differs. */
export interface UserPart {
  readonly text: string;
  readonly cache?: boolean;
}

/** The request body of one call with `prompt`: its pinned settings, the prompt as the system
 *  prompt, and `parts` as the one user message. The reply streams, and the gateway reads it
 *  whole. */
export function promptBody(prompt: Prompt, parts: readonly UserPart[]): MessagesBody {
  const { model, thinking_budget, max_tokens, temperature, effort } = prompt.settings;
  return {
    model,
    max_tokens,
    temperature,
    thinking: { type: "enabled", budget_tokens: thinking_budget },
    output_config: { effort },
    stream: true,
    system: [{ type: "text", text: prompt.text }],
    messages: [
      {
        role: "user",
        content: parts.map(({ text, cache }) =>
          cache === true
            ? { type: "text", text, cache_control: { type: "ephemeral" } }
            : { type: "text", text }
        ),
      },
    ],
  };
}

/** The text a reply's text blocks hold, joined; its thinking is no part of it. */
function answerText(message: ReplyMessage): string {
  return message.content
    .flatMap((block) =>
      block.type === "text" && typeof block.text === "string" ? [block.text] : []
    )
    .join("\n");
}

const FENCED = /^```(?:json)?[ \t]*\n([\s\S]*?)\n```$/;

/** The JSON value an answer's text holds: the text whole, or the one fenced block it is. Throws
 *  when it holds none. */
function answerJson(text: string): unknown {
  const trimmed = text.trim();
  const json = FENCED.exec(trimmed)?.[1] ?? trimmed;
  try {
    return JSON.parse(json);
  } catch {
    const shown = trimmed.length > 200 ? `${trimmed.slice(0, 200)}…` : trimmed;
    throw new Error(`the answer is not one JSON value: ${JSON.stringify(shown)}`);
  }
}

/** `value` as `schema` parses it. Throws naming each issue. */
export function parsedAnswer<T>(schema: z.ZodType<T>, value: unknown): T {
  const parsed = schema.safeParse(value);
  if (!parsed.success) throw new Error(`the answer is refused: ${issues(parsed.error)}`);
  return parsed.data;
}

/** What asking got: the first answer that parsed, or undefined when none did; why each reply before
 *  it was refused; and what each reply used, in the order sent. */
interface Asked<T> {
  readonly answer: T | undefined;
  readonly refusals: readonly string[];
  readonly spent: readonly Spent[];
}

/** Sends `body` and reads the reply's answer as `schema` parses its JSON (`answerJson`,
 *  `parsedAnswer`), sending it again while that throws, `tries` times in all. A send that rejects
 *  (the gateway's BudgetSpent among them) rejects. */
export async function ask<T>(
  body: MessagesBody,
  schema: z.ZodType<T>,
  gateway: Gateway,
  tries: number
): Promise<Asked<T>> {
  const refusals: string[] = [];
  const spent: Spent[] = [];
  for (let n = 0; n < tries; n += 1) {
    const reply = await gateway.send(body);
    spent.push(reply.spent);
    try {
      return {
        answer: parsedAnswer(schema, answerJson(answerText(reply.message))),
        refusals,
        spent,
      };
    } catch (error) {
      const why = messageFor(error);
      const stop = reply.message.stop_reason;
      refusals.push(stop === "end_turn" ? why : `${why} (the reply stopped at ${stop})`);
    }
  }
  return { answer: undefined, refusals, spent };
}

/** A reader: the schema of its input, the schema of its answer to an input, and its input as the
 *  user message. A reader with `cached` sends that part first, marked for the prompt cache: the
 *  part each input of a run shares (`promptBody`), before the part `render` gives. */
export interface Reader<I, A> {
  readonly input: z.ZodType<I>;
  answer(input: I): z.ZodType<A>;
  render(input: I): string;
  cached?(input: I): string;
}

/** A file of a skill: its path inside the skill's directory, and its text. */
const File = z.strictObject({ path: z.string().min(1), text: z.string() });
type File = z.infer<typeof File>;
/** An id the caller gives an item of a batch, which the answer names it by. */
const Id = z.string().min(1);
/** A file and a line of it, from 1. */
const Place = z.strictObject({ file: z.string().min(1), line: z.int().positive() });
type Place = z.infer<typeof Place>;
/** One line of text: not blank, no line break. */
const OneLine = z
  .string()
  .refine((text) => text.trim() !== "" && !text.includes("\n"), "is not one non-blank line");
/** Text that is not blank. */
const NonBlank = z.string().refine((text) => text.trim() !== "", "is blank");

/** Adds the issue `message` at `at` to `ctx`. */
function refuse(ctx: z.RefinementCtx, at: PropertyKey[], message: string): void {
  ctx.addIssue({ code: "custom", path: at, message });
}

/** Refuses each id of `items` given twice. */
function uniqueIds(items: readonly { id: string }[], ctx: z.RefinementCtx, at: PropertyKey): void {
  const seen = new Set<string>();
  for (const [index, { id }] of items.entries()) {
    if (seen.has(id)) refuse(ctx, [at, index, "id"], `gives the id ${id} twice`);
    seen.add(id);
  }
}

/** The number of lines of each file, by its path, as `numbered` numbers them. */
function lineCounts(files: readonly File[]): Map<string, number> {
  return new Map(files.map((file) => [file.path, textLines(file.text).length]));
}

/** Refuses `place` when its file is no file of `files` or its line is past the file's last. */
function checkPlace(
  ctx: z.RefinementCtx,
  at: PropertyKey[],
  place: Place,
  counts: ReadonlyMap<string, number>,
  text: string
): void {
  const count = counts.get(place.file);
  if (count === undefined) refuse(ctx, [...at, "file"], `is no file of the ${text}`);
  else if (place.line > count) {
    refuse(
      ctx,
      [...at, "line"],
      `is past the last line, ${count}, of ${place.file} in the ${text}`
    );
  }
}

/** A text with each line numbered from 1, `<number>: <line>`. */
function numbered(text: string): string {
  return textLines(text)
    .map((line, index) => `${index + 1}: ${line}`)
    .join("\n");
}

/** Each file as `<file path="...">`, its lines numbered. */
function fileBlocks(files: readonly File[]): string {
  return files
    .map((file) => `<file path="${file.path}">\n${numbered(file.text)}\n</file>`)
    .join("\n\n");
}

// The case reader.

/** A message of a transcript as the case reader reads it: its index in the session, its role, and
 *  its text (judge.ts's `renderMessage`, with the skill's read results left out). */
const TranscriptMessage = z.strictObject({
  index: z.int().nonnegative(),
  role: z.string().min(1),
  text: z.string(),
});
/** A file of the skill as the case reader is offered it: its path, and only the lines of it a case
 *  may name (the base text's lines the session's own read of the file held), each with its number
 *  in the file. */
const OfferedFile = z.strictObject({
  path: z.string().min(1),
  lines: z.array(z.strictObject({ number: z.int().positive(), text: OneLine })).min(1),
});
export const CaseInput = z.strictObject({
  skill: z.string().min(1),
  files: z.array(OfferedFile).min(1),
  messages: z.array(TranscriptMessage).min(1),
});
export type CaseInput = z.infer<typeof CaseInput>;
/** A step the case reader writes: its text (empty when it holds only tool calls) and its tool
 *  calls, each a tool's name and its arguments, as the transcript shows a step. */
const WrittenStep = z
  .strictObject({
    text: z.string(),
    toolCalls: z.array(
      z.strictObject({
        name: z.string().min(1),
        arguments: z.record(z.string(), z.unknown()),
      })
    ),
  })
  .refine((step) => step.text.trim() !== "" || step.toolCalls.length > 0, {
    message: "holds no text and no tool call",
  });
/** The messages before the step that state the line's instruction in any wording, a quote of it
 *  among them, each with the words quoted from it: the case reader's and the restatement reader's
 *  answer. */
const Restatements = z.array(
  z.strictObject({ messageIndex: z.int().nonnegative(), quote: z.string().min(1) })
);
const CaseProposal = z.strictObject({
  step: z.strictObject({ messageIndex: z.int().nonnegative(), what: z.string().min(1) }),
  line: z.strictObject({ file: z.string().min(1), text: OneLine }),
  criterion: z.strictObject({ pass: z.string().min(1), fail: z.string().min(1) }),
  violatingStep: WrittenStep,
  restatements: Restatements,
});
const NoCase = z.strictObject({ step: z.null(), reason: z.string().min(1) });
export const CaseAnswer = z.union([CaseProposal, NoCase]);
export type CaseAnswer = z.infer<typeof CaseAnswer>;

/** `text` as a restatement's quote is looked for in it: a JSON string's escapes of a quote mark,
 *  a backslash and a line break read as those characters, each run of white space as one space,
 *  in lower case. A message's tool calls are rendered with their arguments as JSON. */
function looseText(text: string): string {
  return text
    .replaceAll("\\n", " ")
    .replaceAll('\\"', '"')
    .replaceAll("\\\\", "\\")
    .replace(/\s+/g, " ")
    .trim()
    .toLowerCase();
}

/** What the case reader's `<skill>` says of the lines it shows. */
const OFFERED_NOTE =
  "Only the lines a case may name are shown: the lines of each file that the agent's own read of that file held, each with its number in the file. A line not shown cannot be a case's line, and a file not shown was not read.";

const caseReader: Reader<CaseInput, CaseAnswer> = {
  input: CaseInput,
  answer: (input) =>
    CaseAnswer.superRefine((answer, ctx) => {
      if (answer.step === null) return;
      const step = answer.step.messageIndex;
      const message = input.messages.find((each) => each.index === step);
      if (message === undefined) {
        refuse(ctx, ["step", "messageIndex"], "is no message index of the transcript");
      } else if (message.role !== "assistant") {
        refuse(
          ctx,
          ["step", "messageIndex"],
          `is a ${message.role} message, not an assistant message`
        );
      }
      if (!input.files.some((file) => file.path === answer.line.file)) {
        refuse(ctx, ["line", "file"], "is no file of the skill shown");
      }
      // A restatement is a message before the step that holds the words quoted from it.
      for (const [index, { messageIndex, quote }] of answer.restatements.entries()) {
        const at = ["restatements", index];
        const restating = input.messages.find((each) => each.index === messageIndex);
        if (restating === undefined) {
          refuse(ctx, [...at, "messageIndex"], "is no message index of the transcript");
        } else if (messageIndex >= step) {
          refuse(ctx, [...at, "messageIndex"], `is not before the step, message ${step}`);
        } else if (!looseText(restating.text).includes(looseText(quote))) {
          refuse(ctx, [...at, "quote"], `is not words of message ${messageIndex}`);
        }
      }
    }),
  render: (input) =>
    [
      `<skill name="${input.skill}">\n${OFFERED_NOTE}\n\n${input.files
        .map(
          (file) =>
            `<file path="${file.path}">\n${file.lines.map((line) => `${line.number}: ${line.text}`).join("\n")}\n</file>`
        )
        .join("\n\n")}\n</skill>`,
      `<transcript>\n${input.messages
        .map(
          (message) =>
            `<message index="${message.index}" role="${message.role}">\n${message.text}\n</message>`
        )
        .join("\n\n")}\n</transcript>`,
    ].join("\n\n"),
};

// The restatement reader: the whole history before a case's step, window by window.

/** One window of the messages before a case's step. A message longer than a window comes in
 *  parts, each under the message's index, its text opening with a note of which part it is. */
const RestateInput = z.strictObject({
  line: OneLine,
  messages: z.array(TranscriptMessage).min(1),
});
type RestateInput = z.infer<typeof RestateInput>;
export const RestateAnswer = z.strictObject({ restatements: Restatements });
export type RestateAnswer = z.infer<typeof RestateAnswer>;

const restateReader: Reader<RestateInput, RestateAnswer> = {
  input: RestateInput,
  answer: (input) =>
    RestateAnswer.superRefine((answer, ctx) => {
      // A restatement names a message of the window, and its quote is words of that message
      // (of one of its parts).
      for (const [index, { messageIndex, quote }] of answer.restatements.entries()) {
        const at = ["restatements", index];
        const parts = input.messages.filter((each) => each.index === messageIndex);
        if (parts.length === 0) {
          refuse(ctx, [...at, "messageIndex"], "is no message index of the window");
        } else if (!parts.some((part) => looseText(part.text).includes(looseText(quote)))) {
          refuse(ctx, [...at, "quote"], `is not words of message ${messageIndex}`);
        }
      }
    }),
  render: (input) =>
    [
      `<line>\n${input.line}\n</line>`,
      `<messages>\n${input.messages
        .map(
          (message) =>
            `<message index="${message.index}" role="${message.role}">\n${message.text}\n</message>`
        )
        .join("\n\n")}\n</messages>`,
    ].join("\n\n"),
};

// The audit reader: the real step against the line, without the criterion.

/** A case's line, the messages before its step as the judge reads them (judge.ts, `beforeText`),
 *  and its real step (judge.ts, `stepText`). Never the case's criterion. */
const AuditInput = z.strictObject({
  line: OneLine,
  before: z.string(),
  step: z.string().min(1),
});
type AuditInput = z.infer<typeof AuditInput>;
const AuditVerdict = z.enum(["follows", "breaks", "not-governed"]);
export const AuditAnswer = z.strictObject({ verdict: AuditVerdict, reason: z.string().min(1) });
export type AuditAnswer = z.infer<typeof AuditAnswer>;

const auditReader: Reader<AuditInput, AuditAnswer> = {
  input: AuditInput,
  answer: () => AuditAnswer,
  render: (input) =>
    [
      `<line>\n${input.line}\n</line>`,
      `<before>\n${input.before}\n</before>`,
      `<step>\n${input.step}\n</step>`,
    ].join("\n\n"),
};

// The map reader.

export const MapInput = z.strictObject({
  skill: z.string().min(1),
  base: z.array(File).min(1),
  /** Empty when the cut deleted the skill. */
  cut: z.array(File),
  /** The rule-by-rule audit's text for this skill, or null when it has none. */
  audit: z.string().min(1).nullable(),
});
export type MapInput = z.infer<typeof MapInput>;
const RuleStatus = z.enum(["kept", "narrowed", "widened", "lost"]);
const MapRule = z
  .strictObject({
    file: z.string().min(1),
    line: z.int().positive(),
    rule: z.string().min(1),
    status: RuleStatus,
    cut: Place.nullable(),
    reason: z.string().min(1),
    audit: z.string().min(1).nullable(),
  })
  .refine((rule) => (rule.status === "lost") === (rule.cut === null), {
    message: "a lost rule has no place in the cut text, and every other rule has one",
    path: ["cut"],
  });
const MapAnswer = z.strictObject({ rules: z.array(MapRule).min(1) });
type MapAnswer = z.infer<typeof MapAnswer>;

const mapReader: Reader<MapInput, MapAnswer> = {
  input: MapInput,
  answer: (input) =>
    MapAnswer.superRefine((answer, ctx) => {
      const base = lineCounts(input.base);
      const cut = lineCounts(input.cut);
      for (const [index, rule] of answer.rules.entries()) {
        checkPlace(ctx, ["rules", index], rule, base, "base text");
        if (rule.cut !== null) checkPlace(ctx, ["rules", index, "cut"], rule.cut, cut, "cut text");
        if (input.audit === null && rule.audit !== null) {
          refuse(ctx, ["rules", index, "audit"], "cites an audit, and none was given");
        }
      }
    }),
  render: (input) =>
    [
      `<base>\n${fileBlocks(input.base)}\n</base>`,
      `<cut>\n${input.cut.length === 0 ? "The cut deleted the skill: it has no file." : fileBlocks(input.cut)}\n</cut>`,
      `<audit>\n${input.audit ?? "None."}\n</audit>`,
    ].join("\n\n"),
};

// The attack reader.

export const AttackInput = z.strictObject({
  skill: z.string().min(1),
  files: z.array(File).min(1),
});
export type AttackInput = z.infer<typeof AttackInput>;
const FindingKind = z.enum(["vague", "undefined-term", "uncheckable", "two-readings"]);
const AttackAnswer = z.strictObject({
  findings: z.array(
    z.strictObject({
      kind: FindingKind,
      file: z.string().min(1),
      line: z.int().positive(),
      quote: z.string().min(1),
      why: z.string().min(1),
    })
  ),
});
type AttackAnswer = z.infer<typeof AttackAnswer>;

const attackReader: Reader<AttackInput, AttackAnswer> = {
  input: AttackInput,
  answer: (input) =>
    AttackAnswer.superRefine((answer, ctx) => {
      const counts = lineCounts(input.files);
      for (const [index, finding] of answer.findings.entries()) {
        checkPlace(ctx, ["findings", index], finding, counts, "skill");
      }
    }),
  render: (input) => `<skill name="${input.skill}">\n${fileBlocks(input.files)}\n</skill>`,
};

// The same-job reader.

export const SameJobInput = z
  .strictObject({
    skills: z
      .array(
        z.strictObject({
          id: Id,
          repo: z.string().min(1),
          name: z.string().min(1),
          description: z.string(),
        })
      )
      .min(2),
  })
  .superRefine((input, ctx) => uniqueIds(input.skills, ctx, "skills"));
export type SameJobInput = z.infer<typeof SameJobInput>;
const SameJobAnswer = z.strictObject({
  pairs: z.array(z.strictObject({ a: Id, b: Id, task: z.string().min(1), why: z.string().min(1) })),
});
type SameJobAnswer = z.infer<typeof SameJobAnswer>;

const sameJobReader: Reader<SameJobInput, SameJobAnswer> = {
  input: SameJobInput,
  answer: (input) =>
    SameJobAnswer.superRefine((answer, ctx) => {
      const ids = new Set(input.skills.map((skill) => skill.id));
      for (const [index, pair] of answer.pairs.entries()) {
        for (const side of ["a", "b"] as const) {
          if (!ids.has(pair[side])) refuse(ctx, ["pairs", index, side], "is no id of the skills");
        }
        if (pair.a === pair.b) refuse(ctx, ["pairs", index, "b"], "pairs a skill with itself");
      }
    }),
  render: (input) =>
    `<skills>\n${input.skills
      .map(
        (skill) =>
          `<skill id="${skill.id}" repo="${skill.repo}" name="${skill.name}">\n${skill.description}\n</skill>`
      )
      .join("\n")}\n</skills>`,
};

// The sentence reader.

export const SentenceInput = z
  .strictObject({
    skills: z
      .array(
        z.strictObject({
          id: Id,
          repo: z.string().min(1),
          path: z.string().min(1),
          text: z.string(),
        })
      )
      .min(1),
  })
  .superRefine((input, ctx) => uniqueIds(input.skills, ctx, "skills"));
export type SentenceInput = z.infer<typeof SentenceInput>;
const SentenceAnswer = z.strictObject({
  found: z.array(z.strictObject({ id: Id, line: z.int().positive(), quote: z.string().min(1) })),
});
type SentenceAnswer = z.infer<typeof SentenceAnswer>;

const sentenceReader: Reader<SentenceInput, SentenceAnswer> = {
  input: SentenceInput,
  answer: (input) =>
    SentenceAnswer.superRefine((answer, ctx) => {
      const counts = new Map(input.skills.map((skill) => [skill.id, textLines(skill.text).length]));
      for (const [index, found] of answer.found.entries()) {
        const count = counts.get(found.id);
        if (count === undefined) refuse(ctx, ["found", index, "id"], "is no id of the skills");
        else if (found.line > count) {
          refuse(
            ctx,
            ["found", index, "line"],
            `is past the last line, ${count}, of skill ${found.id}`
          );
        }
      }
    }),
  render: (input) =>
    input.skills
      .map(
        (skill) =>
          `<skill id="${skill.id}" repo="${skill.repo}" path="${skill.path}">\n${numbered(skill.text)}\n</skill>`
      )
      .join("\n\n"),
};

// The names reader.

/** The words a reason can be made of and still say only that the job is not clear from the name
 *  alone: none of them names a task the name suggests or hides. */
const NAME_ONLY_WORDS = new Set(
  [
    "a an the this that these those its it it's they their skill skills skill's job jobs task tasks",
    "work purpose purposes use uses usage role what which who how does do did doing done is isn't",
    "are aren't was wasn't be being been has have not no nothing clear unclear obvious unobvious",
    "evident apparent explicit self-explanatory from by in on of to for at about as with alone",
    "itself only just solely name names named naming its say says tell tells show shows make makes",
    "explain explains convey conveys describe describes indicate indicates reveal reveals suggest",
    "suggests give gives hint hints any idea reader readers agent agents user users someone one",
    "can can't cannot could couldn't would wouldn't will won't doesn't don't know known guess",
    "infer understand without reading description very really fully entirely quite enough exactly",
    "immediately clearly obviously vague generic ambiguous unspecific nonspecific opaque cryptic",
    "hard difficult too so much or and but",
  ]
    .join(" ")
    .split(" ")
);

/** Whether `text` says only that the job is not clear from the skill's name `name` alone: every
 *  word of it is one of NAME_ONLY_WORDS, the name and its parts left aside. */
export function nameOnlyReason(text: string, name: string): boolean {
  const own = new Set([name, ...name.split("-")].map((word) => word.toLowerCase()));
  const words = text
    .toLowerCase()
    .replaceAll("’", "'")
    .split(/[^a-z0-9'-]+/)
    .map((word) => word.replace(/^['-]+|['-]+$/g, ""))
    .filter((word) => word !== "" && !own.has(word));
  return words.every((word) => NAME_ONLY_WORDS.has(word));
}

const NamesInput = z
  .strictObject({
    skills: z
      .array(
        z.strictObject({
          id: Id,
          repo: z.string().min(1),
          name: z.string().min(1),
          description: z.string(),
          /** What the skill's load test found wrong with the description, or null. */
          descriptionFault: z.string().min(1).nullable(),
        })
      )
      .min(1),
    /** Every skill name in the copies output's list: no proposed name may be one. */
    namesTaken: z.array(z.string().min(1)),
  })
  .superRefine((input, ctx) => uniqueIds(input.skills, ctx, "skills"));
type NamesInput = z.infer<typeof NamesInput>;
const SkillName = z
  .string()
  .regex(/^[a-z0-9]+(?:-[a-z0-9]+)*$/, "is not lowercase words joined by -");
const NamesAnswer = z.strictObject({
  skills: z.array(
    z.discriminatedUnion("nominated", [
      z.strictObject({ id: Id, nominated: z.literal(false) }),
      z.strictObject({
        id: Id,
        nominated: z.literal(true),
        risk: z.enum(["wrong-task", "missed"]),
        task: z.string().min(1),
        reason: z.string().min(1),
        proposed: z.array(SkillName).min(1).max(3),
      }),
    ])
  ),
});
type NamesAnswer = z.infer<typeof NamesAnswer>;

const namesReader: Reader<NamesInput, NamesAnswer> = {
  input: NamesInput,
  answer: (input) =>
    NamesAnswer.superRefine((answer, ctx) => {
      const skills = new Map(input.skills.map((skill) => [skill.id, skill]));
      const taken = new Set(input.namesTaken);
      const answered = new Set<string>();
      for (const [index, entry] of answer.skills.entries()) {
        const skill = skills.get(entry.id);
        if (skill === undefined) {
          refuse(ctx, ["skills", index, "id"], "is no id of the skills");
          continue;
        }
        if (answered.has(entry.id))
          refuse(ctx, ["skills", index, "id"], `answers ${entry.id} twice`);
        answered.add(entry.id);
        if (!entry.nominated) {
          if (skill.descriptionFault !== null) {
            refuse(
              ctx,
              ["skills", index, "nominated"],
              "is false for a skill whose description has a fault"
            );
          }
          continue;
        }
        for (const field of ["reason", "task"] as const) {
          if (nameOnlyReason(entry[field], skill.name)) {
            refuse(ctx, ["skills", index, field], NAME_ONLY_REFUSAL);
          }
        }
        for (const [at, name] of entry.proposed.entries()) {
          if (name === skill.name)
            refuse(ctx, ["skills", index, "proposed", at], "is the skill's own name");
          else if (taken.has(name))
            refuse(ctx, ["skills", index, "proposed", at], "is a name in use");
          else if (entry.proposed.indexOf(name) !== at) {
            refuse(ctx, ["skills", index, "proposed", at], "is proposed twice");
          }
        }
      }
      for (const skill of input.skills) {
        if (!answered.has(skill.id)) refuse(ctx, ["skills"], `gives no entry for ${skill.id}`);
      }
    }),
  render: (input) =>
    [
      `<skills>\n${input.skills
        .map((skill) => {
          const fault =
            skill.descriptionFault === null ? "" : `\n<fault>${skill.descriptionFault}</fault>`;
          return `<skill id="${skill.id}" repo="${skill.repo}" name="${skill.name}">\n${skill.description}${fault}\n</skill>`;
        })
        .join("\n")}\n</skills>`,
      `<names-taken>\n${input.namesTaken.join("\n")}\n</names-taken>`,
    ].join("\n\n"),
};

// The tasks reader.

export const TasksInput = z
  .strictObject({
    skill: z.strictObject({ name: z.string().min(1), description: z.string() }),
    /** First user messages of real sessions, each with an id: those a query returned (`pick`),
     *  or the first requests of sessions whose first assistant message read the skill
     *  (`control`). */
    messages: z.array(z.strictObject({ id: Id, text: z.string().min(1) })),
    /** `pick` (when absent): propose terms, and pick and write the tasks. `control`: each message
     *  is a need task of the positive control, unchosen; the reader only says whether the skill
     *  governs its first step. */
    mode: z.enum(["pick", "control"]).optional(),
  })
  .superRefine((input, ctx) => uniqueIds(input.messages, ctx, "messages"));
export type TasksInput = z.infer<typeof TasksInput>;
/** Whether a task's first step is one the skill governs (an agent doing the task would read the
 *  skill at once, before any other act), and why. */
const FirstStep = z.strictObject({ governed: z.boolean(), reason: z.string().min(1) });
const TaskAnswerFields = {
  label: TaskLabel,
  why: z.string().min(1),
  firstStep: FirstStep.optional(),
};
export const TasksAnswer = z.strictObject({
  terms: z.array(z.string().min(1)).max(6),
  tasks: z.array(
    z.discriminatedUnion("from", [
      z.strictObject({ ...TaskAnswerFields, from: z.literal("session"), messageId: Id }),
      z.strictObject({ ...TaskAnswerFields, from: z.literal("written"), text: z.string().min(1) }),
    ])
  ),
});
export type TasksAnswer = z.infer<typeof TasksAnswer>;

/** What the tasks reader's `<control>` part says. */
const CONTROL_NOTE =
  "Each message is the first request of a session whose agent read the skill in its first reply.";

const tasksReader: Reader<TasksInput, TasksAnswer> = {
  input: TasksInput,
  answer: (input) =>
    TasksAnswer.superRefine((answer, ctx) => {
      const ids = new Set(input.messages.map((message) => message.id));
      const used = new Set<string>();
      for (const [index, task] of answer.tasks.entries()) {
        if (task.label === "need" && task.firstStep === undefined) {
          refuse(
            ctx,
            ["tasks", index, "firstStep"],
            "is missing: a need task says whether the skill governs its first step"
          );
        }
        if (task.from !== "session") continue;
        if (!ids.has(task.messageId))
          refuse(ctx, ["tasks", index, "messageId"], "is no id of the messages");
        else if (used.has(task.messageId)) {
          refuse(ctx, ["tasks", index, "messageId"], "takes a message a task already took");
        }
        used.add(task.messageId);
      }
      if (input.mode === "control") {
        for (const [index, task] of answer.tasks.entries()) {
          if (task.label !== "need" || task.from !== "session") {
            refuse(ctx, ["tasks", index], "is not a need task of a message: a control task is one");
          }
        }
        for (const id of ids) {
          if (!used.has(id)) refuse(ctx, ["tasks"], `leaves out the message ${id}`);
        }
        return;
      }
      if (answer.terms.length === 0) refuse(ctx, ["terms"], "holds no term");
      const governed = answer.tasks.filter(
        (task) => task.label === "need" && task.firstStep?.governed === true
      ).length;
      if (governed < TASKS_PER_LABEL) {
        refuse(
          ctx,
          ["tasks"],
          `holds ${governed} need tasks whose first step the skill governs, fewer than ${TASKS_PER_LABEL}`
        );
      }
      const nearMiss = answer.tasks.filter((task) => task.label === "near-miss").length;
      if (nearMiss < TASKS_PER_LABEL) {
        refuse(ctx, ["tasks"], `holds ${nearMiss} near-miss tasks, fewer than ${TASKS_PER_LABEL}`);
      }
    }),
  render: (input) =>
    [
      ...(input.mode === "control" ? [`<control>\n${CONTROL_NOTE}\n</control>`] : []),
      `<skill name="${input.skill.name}">\n<description>\n${input.skill.description}\n</description>\n</skill>`,
      `<first-messages>\n${
        input.messages.length === 0
          ? "None."
          : input.messages
              .map((message) => `<message id="${message.id}">\n${message.text}\n</message>`)
              .join("\n\n")
      }\n</first-messages>`,
    ].join("\n\n"),
};

// The craft reader: one skill of ours against the style text, the craft list's reader properties
// and its nearest skills of theirs.

const StyleId = z.string().regex(/^S[1-9]\d*$/, "is not S<n>");
const PropertyId = z.string().regex(/^C[1-9]\d*$/, "is not C<n>");
/** What the craft reader's `<style>` calls each part of the style text. */
const PART_NAMES = {
  order: "the order table",
  rules: "the rules",
  check: "the check before you commit",
} as const;
/** A skill and its files: a nearest skill of theirs. */
const NamedSkill = z.strictObject({
  name: z.string().regex(/^[^:\s]+$/, "is not a skill name with no colon and no space"),
  files: z.array(File).min(1),
});
/** A rule of the style text (style.ts's `styleRules`), as the craft and proposal readers take it. */
const StyleRuleInput = z.strictObject({
  id: StyleId,
  line: z.int().positive(),
  part: z.enum(["order", "rules", "check"]),
  text: z.string().min(1),
});
type StyleRuleInput = z.infer<typeof StyleRuleInput>;
/** A property of the craft list that a reader checks, as the craft and proposal readers take it:
 *  what it states and what the reader looks for, never how it is checked. */
const ReaderPropertyInput = z.strictObject({
  id: PropertyId,
  property: OneLine,
  readerLooksFor: z.string().min(1),
});
type ReaderPropertyInput = z.infer<typeof ReaderPropertyInput>;

export const CraftInput = z
  .strictObject({
    skill: z.string().min(1),
    files: z.array(File).min(1),
    /** The style text's rules (`rulesInput`). */
    style: z.array(StyleRuleInput).min(1),
    /** The craft list's properties that a reader checks, and only those (`rulesInput`). */
    properties: z.array(ReaderPropertyInput),
    nearest: z.array(NamedSkill),
  })
  .superRefine((input, ctx) => {
    uniqueIds(input.style, ctx, "style");
    uniqueIds(input.properties, ctx, "properties");
    uniqueIds(
      input.nearest.map((skill) => ({ id: skill.name })),
      ctx,
      "nearest"
    );
  });
export type CraftInput = z.infer<typeof CraftInput>;
const CraftVerdict = z.enum(["met", "gap", "not-applicable"]);
/** A gap of a craft answer: where it is (for a missing element, the line it belongs after, 0
 *  before line 1), the rule it is against, whether something is wrong or absent, and why. */
export const CraftGap = z.strictObject({
  file: z.string().min(1),
  line: z.int().nonnegative(),
  rule: z.string().min(1),
  kind: z.enum(["present", "missing"]),
  why: z.string().min(1),
});
export type CraftGap = z.infer<typeof CraftGap>;
export const CraftAnswer = z.strictObject({
  judged: z.array(z.strictObject({ rule: z.string().min(1), verdict: CraftVerdict })),
  gaps: z.array(CraftGap),
});
export type CraftAnswer = z.infer<typeof CraftAnswer>;
/** A gap's rule against a nearest skill: `nearest:<skill>:<path>:<line>`. */
const NEAREST_RULE = /^nearest:([^:]+):(.+):(\d+)$/;

/** Each rule of the style text, and each property of `list` that a reader checks (never one a
 *  behaviour test checks), as the craft and proposal readers take them. */
export function rulesInput(
  style: readonly StyleRule[],
  list: CraftList
): { style: StyleRuleInput[]; properties: ReaderPropertyInput[] } {
  return {
    style: style.map(({ id, line, part, text }) => ({ id, line, part, text })),
    properties: readerProperties(list).map(({ id, property, readerLooksFor }) => ({
      id,
      property,
      readerLooksFor,
    })),
  };
}

/** The craft reader's input for the skill `skill` and its files: the style text's rules and the
 *  reader properties of `list` (`rulesInput`), and each nearest skill of theirs with its files. */
export function craftInput(given: {
  readonly skill: string;
  readonly files: readonly File[];
  readonly style: readonly StyleRule[];
  readonly list: CraftList;
  readonly nearest: readonly { readonly name: string; readonly files: readonly File[] }[];
}): CraftInput {
  return {
    skill: given.skill,
    files: [...given.files],
    ...rulesInput(given.style, given.list),
    nearest: given.nearest.map(({ name, files }) => ({ name, files: [...files] })),
  };
}

/** The style rules as the craft and proposal readers' `<style>` gives them: each with its id, its
 *  line in the style text and its part. */
function styleBlock(style: readonly StyleRuleInput[]): string {
  return `<style>\n${style
    .map(
      (rule) =>
        `<rule id="${rule.id}" line="${rule.line}" part="${PART_NAMES[rule.part]}">\n${rule.text}\n</rule>`
    )
    .join("\n")}\n</style>`;
}

/** The reader properties as the craft and proposal readers' `<craft-properties>` gives them. */
function propertiesBlock(properties: readonly ReaderPropertyInput[]): string {
  return `<craft-properties>\n${
    properties.length === 0
      ? "None."
      : properties
          .map(
            (each) =>
              `<property id="${each.id}">\n<states>${each.property}</states>\n<looks-for>${each.readerLooksFor}</looks-for>\n</property>`
          )
          .join("\n")
  }\n</craft-properties>`;
}

const craftReader: Reader<CraftInput, CraftAnswer> = {
  input: CraftInput,
  answer: (input) =>
    CraftAnswer.superRefine((answer, ctx) => {
      const ids = new Set([...input.style, ...input.properties].map((rule) => rule.id));
      const judged = new Map<string, z.infer<typeof CraftVerdict>>();
      for (const [index, { rule, verdict }] of answer.judged.entries()) {
        if (!ids.has(rule)) {
          refuse(
            ctx,
            ["judged", index, "rule"],
            "is no style rule or reader property of the input"
          );
        } else if (judged.has(rule)) refuse(ctx, ["judged", index, "rule"], `judges ${rule} twice`);
        else judged.set(rule, verdict);
      }
      for (const id of ids) if (!judged.has(id)) refuse(ctx, ["judged"], `judges no ${id}`);
      const counts = lineCounts(input.files);
      const nearest = new Map(input.nearest.map((skill) => [skill.name, lineCounts(skill.files)]));
      const gapped = new Set<string>();
      for (const [index, gap] of answer.gaps.entries()) {
        const at = ["gaps", index];
        const count = counts.get(gap.file);
        if (count === undefined) refuse(ctx, [...at, "file"], "is no file of the skill");
        else if (gap.kind === "present" && gap.line === 0) {
          refuse(ctx, [...at, "line"], "is 0, and a present gap names a line from 1");
        } else if (gap.line > count) {
          refuse(ctx, [...at, "line"], `is past the last line, ${count}, of ${gap.file}`);
        }
        if (ids.has(gap.rule)) {
          gapped.add(gap.rule);
          if (judged.has(gap.rule) && judged.get(gap.rule) !== "gap") {
            refuse(ctx, [...at, "rule"], `is ${gap.rule}, which the answer does not judge gap`);
          }
          continue;
        }
        const [, skill = "", file = "", line = "0"] = NEAREST_RULE.exec(gap.rule) ?? [];
        const files = nearest.get(skill);
        const lines = files?.get(file);
        if (skill === "") {
          refuse(
            ctx,
            [...at, "rule"],
            "is no style rule, reader property or nearest skill's line of the input: give S<n>, C<n> or nearest:<skill>:<path>:<line>"
          );
        } else if (files === undefined) {
          refuse(ctx, [...at, "rule"], `names ${skill}, no nearest skill of the input`);
        } else if (lines === undefined) {
          refuse(ctx, [...at, "rule"], `names ${file}, no file of the nearest skill ${skill}`);
        } else if (Number(line) < 1 || Number(line) > lines) {
          refuse(
            ctx,
            [...at, "rule"],
            `names line ${line}, past the last line, ${lines}, of ${file} of ${skill}`
          );
        }
      }
      for (const [index, { rule, verdict }] of answer.judged.entries()) {
        if (verdict === "gap" && ids.has(rule) && !gapped.has(rule)) {
          refuse(ctx, ["judged", index, "verdict"], `judges ${rule} gap, and no gap names it`);
        }
      }
    }),
  render: (input) =>
    [
      `<skill name="${input.skill}">\n${fileBlocks(input.files)}\n</skill>`,
      styleBlock(input.style),
      propertiesBlock(input.properties),
      `<nearest>\n${
        input.nearest.length === 0
          ? "None: no nearest skill of theirs was given."
          : input.nearest
              .map((skill) => `<skill name="${skill.name}">\n${fileBlocks(skill.files)}\n</skill>`)
              .join("\n")
      }\n</nearest>`,
    ].join("\n\n"),
};

// The nearest reader.

export const NearestInput = z
  .strictObject({
    /** The skill: its name, its whole description and its SKILL.md less the frontmatter. */
    skill: z.strictObject({ name: z.string().min(1), description: z.string(), body: z.string() }),
    /** Each candidate: its name, the source it comes from, and its whole description. */
    candidates: z
      .array(
        z.strictObject({
          name: z.string().min(1),
          source: z.string().min(1),
          description: z.string(),
        })
      )
      .min(1),
  })
  .superRefine((input, ctx) =>
    uniqueIds(
      input.candidates.map((candidate) => ({ id: `${candidate.name} of ${candidate.source}` })),
      ctx,
      "candidates"
    )
  );
export type NearestInput = z.infer<typeof NearestInput>;
const NearestVerdict = z.enum(["equivalent", "partial"]);
export const NearestAnswer = z.strictObject({
  verdict: z.enum(["equivalent", "partial", "none"]),
  nearest: z
    .array(
      z.strictObject({
        name: z.string().min(1),
        source: z.string().min(1),
        verdict: NearestVerdict,
        missing: z.string().min(1),
        reason: z.string().min(1),
      })
    )
    .max(2),
  reason: z.string().min(1),
});
export type NearestAnswer = z.infer<typeof NearestAnswer>;

const nearestReader: Reader<NearestInput, NearestAnswer> = {
  input: NearestInput,
  answer: (input) =>
    NearestAnswer.superRefine((answer, ctx) => {
      const candidates = new Set(input.candidates.map((each) => `${each.name} of ${each.source}`));
      const named = new Set<string>();
      for (const [index, entry] of answer.nearest.entries()) {
        const candidate = `${entry.name} of ${entry.source}`;
        if (!candidates.has(candidate))
          refuse(ctx, ["nearest", index], `${candidate} is no candidate`);
        else if (named.has(candidate)) refuse(ctx, ["nearest", index], `names ${candidate} twice`);
        named.add(candidate);
      }
      const best = answer.nearest.some((entry) => entry.verdict === "equivalent")
        ? "equivalent"
        : "partial";
      if (answer.nearest.length === 0) {
        if (answer.verdict !== "none")
          refuse(ctx, ["verdict"], "is not none, and the answer names no candidate");
      } else if (answer.verdict === "none") {
        refuse(ctx, ["verdict"], "is none, and the answer names a candidate");
      } else if (answer.verdict !== best) {
        refuse(ctx, ["verdict"], `is ${answer.verdict}, not the best entry's verdict, ${best}`);
      }
    }),
  render: (input) =>
    [
      `<skill name="${input.skill.name}">\n<description>\n${input.skill.description}\n</description>\n<body>\n${input.skill.body}\n</body>\n</skill>`,
      `<candidates>\n${input.candidates
        .map(
          (each) =>
            `<candidate name="${each.name}" source="${each.source}">\n${each.description}\n</candidate>`
        )
        .join("\n")}\n</candidates>`,
    ].join("\n\n"),
};

// The claim reader: whether one gap of a craft answer states a claim about the skill.

export const ClaimInput = z
  .strictObject({ claim: OneLine, gap: CraftGap, files: z.array(File).min(1) })
  .superRefine((input, ctx) => {
    if (!input.files.some((file) => file.path === input.gap.file))
      refuse(ctx, ["gap", "file"], "is no file of the skill");
  });
export type ClaimInput = z.infer<typeof ClaimInput>;
export const ClaimAnswer = z.strictObject({ states: z.boolean(), reason: NonBlank });
export type ClaimAnswer = z.infer<typeof ClaimAnswer>;

const claimReader: Reader<ClaimInput, ClaimAnswer> = {
  input: ClaimInput,
  answer: () => ClaimAnswer,
  render: (input) =>
    [
      `<claim>\n${input.claim}\n</claim>`,
      `<gap file="${input.gap.file}" line="${input.gap.line}" rule="${input.gap.rule}" kind="${input.gap.kind}">\n${input.gap.why}\n</gap>`,
      `<skill>\n${fileBlocks(input.files)}\n</skill>`,
    ].join("\n\n"),
};

// The proposal readers: where one proposal of a closed pull request goes today, and what its change
// fixes and makes there.

/** An item of a list of a pull request's body (proposals.ts's `parseProposalItems`): its id, its
 *  state (the words in its parentheses) and its text. */
const ProposalItem = z.strictObject({ id: Id, state: OneLine, text: NonBlank });
/** A path inside a repository: relative, with no empty, `.` or `..` part. */
const RepoPath = z
  .string()
  .refine(
    (value) => value.split("/").every((part) => part !== "" && part !== "." && part !== ".."),
    "is not a path inside the repository"
  );
/** A skill a reader reads: its repository (as its skills root names it) and its directory's name. */
const ListedSkill = z.strictObject({ repo: z.string().min(1), name: z.string().min(1) });
/** A file of a diff: its path before and after the change (null before for a file the change
 *  adds, after for one it deletes), and the `@@` line of each of its hunks. */
const ChangedFile = z.strictObject({
  before: z.string().min(1).nullable(),
  after: z.string().min(1).nullable(),
  hunks: z.array(z.string().min(1)),
});

/** The files of a diff by each path that names one, before or after the change: where a path
 *  names two files (a file renamed away from it, and a new file at it), the file the diff shows at
 *  that path (its path after the change, or before for a file the change deletes); else the first
 *  file of the diff it names. The proposal-target answer check and proposals.ts's item read take
 *  a hunk's file from it, so both take the same one. */
export function diffFilesByPath<
  F extends { readonly before: string | null; readonly after: string | null },
>(files: readonly F[]): Map<string, F> {
  const byPath = new Map<string, F>();
  for (const file of files) {
    const shown = file.after ?? file.before;
    if (shown !== null && !byPath.has(shown)) byPath.set(shown, file);
  }
  for (const file of files) {
    for (const name of [file.before, file.after]) {
      if (name !== null && !byPath.has(name)) byPath.set(name, file);
    }
  }
  return byPath;
}

export const ProposalTargetInput = z
  .strictObject({
    /** The closed pull request's diff: its number, its whole text and each of its files. */
    diff: z.strictObject({ pr: Id, text: z.string().min(1), files: z.array(ChangedFile).min(1) }),
    item: ProposalItem,
    /** The repository a `dotfiles` target's path is in, at the commit read. */
    repository: z.string().min(1),
    skills: z.array(ListedSkill).min(1),
  })
  .superRefine((input, ctx) => {
    const paths = new Set<string>();
    for (const [index, { before, after }] of input.diff.files.entries()) {
      const at = ["diff", "files", index];
      const name = after ?? before;
      if (name === null) refuse(ctx, at, "has no path before or after the change");
      else if (paths.has(name)) refuse(ctx, at, `gives the file ${name} twice`);
      else paths.add(name);
    }
    uniqueIds(
      input.skills.map((skill) => ({ id: `${skill.name} of ${skill.repo}` })),
      ctx,
      "skills"
    );
  });
export type ProposalTargetInput = z.infer<typeof ProposalTargetInput>;
const ProposalTarget = z.discriminatedUnion("kind", [
  z.strictObject({ kind: z.literal("dotfiles"), path: RepoPath, why: NonBlank }),
  z.strictObject({ kind: z.literal("skill"), skill: ListedSkill, why: NonBlank }),
  z.strictObject({ kind: z.literal("none"), why: NonBlank }),
]);
/** The hunks that make an item, each a file of the diff (by its path before or after the change)
 *  and the index of a hunk of it, from 0; and its targets. */
export const ProposalTargetAnswer = z.strictObject({
  hunks: z.array(z.strictObject({ file: z.string().min(1), hunk: z.int().nonnegative() })).min(1),
  targets: z.array(ProposalTarget).min(1),
});
export type ProposalTargetAnswer = z.infer<typeof ProposalTargetAnswer>;

const proposalTargetReader: Reader<ProposalTargetInput, ProposalTargetAnswer> = {
  input: ProposalTargetInput,
  answer: (input) =>
    ProposalTargetAnswer.superRefine((answer, ctx) => {
      const files = diffFilesByPath(input.diff.files);
      const named = new Set<string>();
      for (const [index, { file: name, hunk }] of answer.hunks.entries()) {
        const file = files.get(name);
        if (file === undefined) {
          refuse(ctx, ["hunks", index, "file"], "is no file of the diff");
          continue;
        }
        const path = file.after ?? file.before;
        if (hunk >= file.hunks.length) {
          refuse(
            ctx,
            ["hunks", index, "hunk"],
            file.hunks.length === 0
              ? `names a hunk of ${path}, which has none`
              : `is past the last hunk, ${file.hunks.length - 1}, of ${path}`
          );
        } else if (named.has(`${hunk} of ${path}`)) {
          refuse(ctx, ["hunks", index], `names hunk ${hunk} of ${path} twice`);
        }
        named.add(`${hunk} of ${path}`);
      }
      const skills = new Set(input.skills.map((skill) => `${skill.name} of ${skill.repo}`));
      const targets = new Set<string>();
      for (const [index, target] of answer.targets.entries()) {
        if (target.kind === "none") continue;
        const key =
          target.kind === "dotfiles" ? target.path : `${target.skill.name} of ${target.skill.repo}`;
        if (target.kind === "skill" && !skills.has(key)) {
          refuse(ctx, ["targets", index, "skill"], `${key} is no skill of the list`);
        } else if (targets.has(key))
          refuse(ctx, ["targets", index], `names the target ${key} twice`);
        targets.add(key);
      }
    }),
  cached: (input) =>
    [
      `<diff pr="${input.diff.pr}">\n${input.diff.text}\n</diff>`,
      `<hunks>\n${input.diff.files
        .map(({ before, after, hunks }) => {
          const moved = before !== null && after !== null && before !== after;
          const how = before === null ? ' added="yes"' : after === null ? ' deleted="yes"' : "";
          const list =
            hunks.length === 0
              ? "no hunk"
              : hunks.map((header, index) => `hunk ${index}: ${header}`).join("\n");
          return `<file path="${after ?? before}"${moved ? ` before="${before}"` : ""}${how}>\n${list}\n</file>`;
        })
        .join("\n")}\n</hunks>`,
    ].join("\n\n"),
  render: (input) =>
    [
      `<repository>\n${input.repository}\n</repository>`,
      `<item id="${input.item.id}" state="${input.item.state}">\n${input.item.text}\n</item>`,
      `<skills>\n${input.skills
        .map((skill) => `<skill repo="${skill.repo}" name="${skill.name}"/>`)
        .join("\n")}\n</skills>`,
    ].join("\n\n"),
};

/** A target of a proposal with its text today: a file of the repository (`text` null when the
 *  repository holds no such file at the commit read), or a skill a reader reads, with its files. */
const TargetText = z.discriminatedUnion("kind", [
  z.strictObject({
    kind: z.literal("dotfiles"),
    repo: z.string().min(1),
    path: RepoPath,
    text: z.string().nullable(),
  }),
  z.strictObject({
    kind: z.literal("skill"),
    repo: z.string().min(1),
    name: z.string().min(1),
    files: z.array(File).min(1),
  }),
]);
export const ProposalInput = z
  .strictObject({
    item: ProposalItem,
    /** The hunks that make the item (`proposal-target`'s answer): each with its file, its index in
     *  that file, its `@@` line and its lines. */
    hunks: z
      .array(
        z.strictObject({
          file: z.string().min(1),
          hunk: z.int().nonnegative(),
          header: z.string().min(1),
          lines: z.array(z.string()),
        })
      )
      .min(1),
    targets: z.array(TargetText).min(1),
    /** The gaps of the findings entry of each target skill that has one with an answer. */
    findings: z.array(
      z.strictObject({ repo: z.string().min(1), name: z.string().min(1), gaps: z.array(CraftGap) })
    ),
    style: z.array(StyleRuleInput).min(1),
    properties: z.array(ReaderPropertyInput),
  })
  .superRefine((input, ctx) => {
    uniqueIds(input.style, ctx, "style");
    uniqueIds(input.properties, ctx, "properties");
    const skills = new Set(
      input.targets.flatMap((target) =>
        target.kind === "skill" ? [`${target.name} of ${target.repo}`] : []
      )
    );
    for (const [index, { name, repo }] of input.findings.entries()) {
      if (!skills.has(`${name} of ${repo}`)) {
        refuse(ctx, ["findings", index], `are of ${name} of ${repo}, which is no target skill`);
      }
    }
  });
export type ProposalInput = z.infer<typeof ProposalInput>;
const ProposalRule = z.string().regex(/^[SC][1-9]\d*$/, "is not S<n> or C<n>");
/** What a proposal's change fixes (a finding of the input by its number, or a gap against a style
 *  rule or a reader property) and makes (a gap against one), each with where and why, and a
 *  summary. A file is one the input names; a line is a line of it, and needs its file. */
export const ProposalAnswer = z.strictObject({
  fixes: z.array(
    z.strictObject({
      finding: z.int().nonnegative().optional(),
      rule: ProposalRule.optional(),
      file: z.string().min(1).optional(),
      line: z.int().positive().optional(),
      why: NonBlank,
    })
  ),
  makes: z.array(
    z.strictObject({
      rule: ProposalRule,
      file: z.string().min(1).optional(),
      line: z.int().positive().optional(),
      why: NonBlank,
    })
  ),
  summary: OneLine,
});
export type ProposalAnswer = z.infer<typeof ProposalAnswer>;

const proposalReader: Reader<ProposalInput, ProposalAnswer> = {
  input: ProposalInput,
  answer: (input) =>
    ProposalAnswer.superRefine((answer, ctx) => {
      const ids = new Set([...input.style, ...input.properties].map((rule) => rule.id));
      const findings = input.findings.reduce((sum, entry) => sum + entry.gaps.length, 0);
      // Each file by the name the request gives it, with its line count where the request holds
      // its text: each hunk's file, each dotfiles target's path, and each target skill's files as
      // `<skill>/<path>`.
      const files = new Map<string, number | undefined>(
        input.hunks.map((hunk) => [hunk.file, undefined])
      );
      for (const target of input.targets) {
        if (target.kind === "dotfiles") {
          files.set(target.path, target.text === null ? undefined : textLines(target.text).length);
        } else {
          for (const file of target.files) {
            files.set(`${target.name}/${file.path}`, textLines(file.text).length);
          }
        }
      }
      const checkPlace = (at: PropertyKey[], place: { file?: string; line?: number }) => {
        const count = place.file === undefined ? undefined : files.get(place.file);
        if (place.file !== undefined && !files.has(place.file)) {
          refuse(ctx, [...at, "file"], "is no file of the targets or the hunks");
        } else if (place.line === undefined) return;
        else if (place.file === undefined) refuse(ctx, [...at, "line"], "names a line and no file");
        else if (count !== undefined && place.line > count) {
          refuse(ctx, [...at, "line"], `is past the last line, ${count}, of ${place.file}`);
        }
      };
      for (const [index, fix] of answer.fixes.entries()) {
        const at = ["fixes", index];
        if (fix.finding !== undefined && fix.rule !== undefined) {
          refuse(ctx, at, "gives both a finding and a rule: a fix is one or the other");
        } else if (fix.finding === undefined && fix.rule === undefined) {
          refuse(ctx, at, "gives neither a finding nor a rule");
        }
        if (fix.finding !== undefined && fix.finding >= findings) {
          refuse(ctx, [...at, "finding"], `is no finding of the input, which holds ${findings}`);
        }
        if (fix.rule !== undefined && !ids.has(fix.rule)) {
          refuse(ctx, [...at, "rule"], "is no style rule or reader property of the input");
        }
        checkPlace(at, fix);
      }
      for (const [index, make] of answer.makes.entries()) {
        if (!ids.has(make.rule)) {
          refuse(ctx, ["makes", index, "rule"], "is no style rule or reader property of the input");
        }
        checkPlace(["makes", index], make);
      }
    }),
  render: (input) => {
    const gaps = input.findings.flatMap((entry) => entry.gaps.map((gap) => ({ entry, gap })));
    return [
      `<item id="${input.item.id}" state="${input.item.state}">\n${input.item.text}\n</item>`,
      `<hunks>\n${input.hunks
        .map(
          (hunk) =>
            `<hunk file="${hunk.file}" index="${hunk.hunk}">\n${[hunk.header, ...hunk.lines].join("\n")}\n</hunk>`
        )
        .join("\n")}\n</hunks>`,
      `<targets>\n${input.targets
        .map((target) =>
          target.kind === "dotfiles"
            ? `<target kind="dotfiles" repo="${target.repo}">\n${
                target.text === null
                  ? `<file path="${target.path}">\nNone: the repository holds no such file at this commit.\n</file>`
                  : fileBlocks([{ path: target.path, text: target.text }])
              }\n</target>`
            : `<target kind="skill" repo="${target.repo}" name="${target.name}">\n${fileBlocks(
                target.files.map((file) => ({
                  path: `${target.name}/${file.path}`,
                  text: file.text,
                }))
              )}\n</target>`
        )
        .join("\n")}\n</targets>`,
      `<findings>\n${
        gaps.length === 0
          ? "None: no target skill has a findings entry with an answer."
          : gaps
              .map(
                ({ entry, gap }, index) =>
                  `<finding index="${index}" skill="${entry.name}" repo="${entry.repo}" file="${entry.name}/${gap.file}" line="${gap.line}" rule="${gap.rule}" kind="${gap.kind}">\n${gap.why}\n</finding>`
              )
              .join("\n")
      }\n</findings>`,
      styleBlock(input.style),
      propertiesBlock(input.properties),
    ].join("\n\n");
  },
};

// The written-case reader: tasks in a recorded session's context whose first step a line of the
// skill decides (`build-cases --written`).

/** A session case of the skill as the written-case reader reads it: its real step (judge.ts's
 *  `stepText`) and its criterion. */
const SessionCaseText = z.strictObject({
  step: NonBlank,
  criterion: z.strictObject({ pass: NonBlank, fail: NonBlank }),
});
export const WrittenCaseInput = z.strictObject({
  skill: z.string().min(1),
  /** The skill's base text, every `.md` file of `--old` (`skillFiles`). */
  files: z.array(File).min(1),
  sessionCases: z.array(SessionCaseText),
  /** The first user message of the context session, whose context the tasks go in. */
  firstMessage: NonBlank,
  /** The most tasks to write. */
  count: z.int().positive(),
});
export type WrittenCaseInput = z.infer<typeof WrittenCaseInput>;
const WrittenTask = z.strictObject({
  task: NonBlank,
  line: z.strictObject({ file: z.string().min(1), number: z.int().positive(), text: OneLine }),
  criterion: z.strictObject({ pass: NonBlank, fail: NonBlank }),
  followingStep: WrittenStep,
  violatingStep: WrittenStep,
});
export const WrittenCaseAnswer = z.strictObject({ tasks: z.array(WrittenTask).min(1) });
export type WrittenCaseAnswer = z.infer<typeof WrittenCaseAnswer>;

const writtenCaseReader: Reader<WrittenCaseInput, WrittenCaseAnswer> = {
  input: WrittenCaseInput,
  answer: (input) =>
    WrittenCaseAnswer.superRefine((answer, ctx) => {
      if (answer.tasks.length > input.count) {
        refuse(
          ctx,
          ["tasks"],
          `holds ${answer.tasks.length} tasks, more than the ${input.count} asked for`
        );
      }
      // Each task's line is a line of the base text: its file, its number and its text.
      for (const [index, { line }] of answer.tasks.entries()) {
        const at = ["tasks", index, "line"];
        const file = input.files.find((each) => each.path === line.file);
        if (file === undefined) {
          refuse(ctx, [...at, "file"], "is no file of the base text");
          continue;
        }
        const lines = textLines(file.text);
        if (line.number > lines.length) {
          refuse(ctx, [...at, "number"], `is past the last line, ${lines.length}, of ${line.file}`);
        } else if (lines[line.number - 1]?.trim() !== line.text.trim()) {
          refuse(ctx, [...at, "text"], `is not line ${line.number} of ${line.file}`);
        }
      }
    }),
  render: (input) =>
    [
      `<skill name="${input.skill}">\n${fileBlocks(input.files)}\n</skill>`,
      `<session-cases>\n${
        input.sessionCases.length === 0
          ? "None."
          : input.sessionCases
              .map(
                (each, n) =>
                  `<case n="${n + 1}">\n<step>\n${each.step}\n</step>\n<criterion>\nPass: ${each.criterion.pass}\nFail: ${each.criterion.fail}\n</criterion>\n</case>`
              )
              .join("\n")
      }\n</session-cases>`,
      `<first-message>\n${input.firstMessage}\n</first-message>`,
      `<count>${input.count}</count>`,
    ].join("\n\n"),
};

/** Every reader, by the name of its prompt file. */
export const READERS = {
  case: caseReader,
  restate: restateReader,
  audit: auditReader,
  map: mapReader,
  attack: attackReader,
  "same-job": sameJobReader,
  sentence: sentenceReader,
  names: namesReader,
  tasks: tasksReader,
  craft: craftReader,
  nearest: nearestReader,
  claim: claimReader,
  "proposal-target": proposalTargetReader,
  proposal: proposalReader,
  "written-case": writtenCaseReader,
} as const;
export type ReaderName = keyof typeof READERS;
export const READER_NAMES = Object.keys(READERS) as ReaderName[];

/** A reader's answer, the refusals of the replies before it, and what each reply used. */
export interface ReaderResult {
  readonly answer: unknown;
  readonly refusals: readonly string[];
  readonly spent: readonly Spent[];
}

/** A reader whose every reply was refused. */
export class ReaderFailed extends Error {
  constructor(
    readonly reader: ReaderName,
    readonly refusals: readonly string[],
    readonly spent: readonly Spent[]
  ) {
    super(
      `the ${reader} reader's answer was refused on each of ${refusals.length} tries: ${refusals.join(" | ")}`
    );
    this.name = "ReaderFailed";
  }
}

/** The parts of reader `name`'s user message for `input`, already parsed by its input schema: its
 *  cached part first, marked for the prompt cache, where it has one, then the rest. */
export function userParts(name: ReaderName, input: unknown): UserPart[] {
  const reader: Reader<unknown, unknown> = READERS[name];
  const rest = { text: reader.render(input) };
  return reader.cached === undefined ? [rest] : [{ text: reader.cached(input), cache: true }, rest];
}

/** The request body of reader `name` for `input`, already parsed by its input schema. */
export function readerBody(name: ReaderName, input: unknown): MessagesBody {
  return promptBody(loadPrompt(name), userParts(name, input));
}

/** Runs reader `name` on one input batch: one call, asked again while the answer is refused,
 *  READER_TRIES times in all. Throws when the input is refused, and ReaderFailed when every reply
 *  is. A send that rejects (the gateway's BudgetSpent among them) rejects. */
export async function runReader(
  name: ReaderName,
  input: unknown,
  gateway: Gateway
): Promise<ReaderResult> {
  const reader: Reader<unknown, unknown> = READERS[name];
  const given = reader.input.safeParse(input);
  if (!given.success)
    throw new Error(`the ${name} reader's input is refused: ${issues(given.error)}`);
  const schema = reader.answer(given.data);
  const asked = await ask(readerBody(name, given.data), schema, gateway, READER_TRIES);
  if (asked.answer === undefined) throw new ReaderFailed(name, asked.refusals, asked.spent);
  return { answer: asked.answer, refusals: asked.refusals, spent: asked.spent };
}

// The readers' positive controls. Each plants a known finding in a copy of real
// input, runs the reader once on it, and holds only when the answer names that finding: the map
// reader calls a deleted rule lost, the attack reader lists an added vague line, the same-job
// reader pairs a planted rewording of a description with its original, and the sentence reader
// finds a planted rewording of the sentence.

/** The line the attack control adds. */
export const ATTACK_PLANTED_LINE = "Run the tests where needed.";

/** The `.md` files of the skill directory `dir` (SKILL.md and its references), SKILL.md first and
 *  the rest by path, as the map, attack, case and craft readers take them. */
export function skillFiles(dir: string): File[] {
  return readdirSync(dir, { recursive: true, withFileTypes: true })
    .filter((entry) => entry.isFile() && entry.name.endsWith(".md"))
    .map((entry) => path.relative(dir, path.join(entry.parentPath, entry.name)))
    .sort(
      (a, b) => Number(b === "SKILL.md") - Number(a === "SKILL.md") || (a < b ? -1 : a > b ? 1 : 0)
    )
    .map((file) => ({ path: file, text: readFileSync(path.join(dir, file), "utf8") }));
}

/** A control: the reader's real input, and what to plant in it. `map`: the line of the cut text to
 *  delete, which the base text must hold word for word (a rule the cut kept), so the rule at that
 *  base line is the one that must be lost. `attack`: the line to add at the end of SKILL.md.
 *  `same-job`: a skill entry to add, whose description rewords that of the skill `copyOf`.
 *  `sentence`: a wording of the sentence to add at the end of the skill `into`. */
export type Control =
  | { readonly reader: "map"; readonly input: MapInput; readonly deleteLine: string }
  | { readonly reader: "attack"; readonly input: AttackInput; readonly plantLine: string }
  | {
      readonly reader: "same-job";
      readonly input: SameJobInput;
      readonly planted: SameJobInput["skills"][number];
      readonly copyOf: string;
    }
  | {
      readonly reader: "sentence";
      readonly input: SentenceInput;
      readonly into: string;
      readonly sentence: string;
    };

/** Whether a control held, and why. */
export interface Held {
  readonly held: boolean;
  readonly why: string;
}

/** A control's planted input, and its check of the reader's answer. */
interface Planted {
  readonly input: unknown;
  held(answer: unknown): Held;
}

/** The first line of `files` that is `line` (trimmed), or undefined. */
function placeOf(files: readonly File[], line: string): Place | undefined {
  for (const file of files) {
    const index = textLines(file.text).findIndex((each) => each.trim() === line.trim());
    if (index >= 0) return { file: file.path, line: index + 1 };
  }
  return undefined;
}

/** `text` with `line` added at its end as a paragraph of its own, and the line's number. */
function withLineAdded(text: string, line: string): { text: string; line: number } {
  const lines = [...textLines(text), "", line];
  return { text: `${lines.join("\n")}\n`, line: lines.length };
}

/** The planted input of `control` and its check. Throws when the control cannot be planted: a
 *  line to delete that the cut or the base text lacks, an id given twice, or an id the input
 *  lacks. */
export function plantControl(control: Control): Planted {
  switch (control.reader) {
    case "map": {
      const { input, deleteLine } = control;
      const cut = placeOf(input.cut, deleteLine);
      const base = placeOf(input.base, deleteLine);
      if (cut === undefined)
        throw new Error(`the map control's line is no line of the cut text: ${deleteLine}`);
      if (base === undefined) {
        throw new Error(
          `the map control's line is no line of the base text, so its rule cannot be found: ${deleteLine}`
        );
      }
      const files = input.cut.map((file) => {
        if (file.path !== cut.file) return file;
        const lines = textLines(file.text).filter((_, index) => index !== cut.line - 1);
        return { ...file, text: `${lines.join("\n")}\n` };
      });
      return {
        input: { ...input, cut: files },
        held: (answer) => {
          const rule = MapAnswer.parse(answer).rules.find(
            (each) => each.file === base.file && each.line === base.line
          );
          const at = `the rule at ${base.file}:${base.line} of the base text`;
          if (rule === undefined)
            return { held: false, why: `the map gives no rule at ${base.file}:${base.line}` };
          return { held: rule.status === "lost", why: `${at} is ${rule.status}: ${rule.reason}` };
        },
      };
    }
    case "attack": {
      const { input, plantLine } = control;
      const skill = input.files.find((file) => file.path === "SKILL.md") ?? input.files[0];
      if (skill === undefined) throw new Error("the attack control's skill has no file");
      const added = withLineAdded(skill.text, plantLine);
      return {
        input: {
          ...input,
          files: input.files.map((file) => (file === skill ? { ...file, text: added.text } : file)),
        },
        held: (answer) => {
          const found = AttackAnswer.parse(answer).findings.find(
            (each) => each.file === skill.path && each.line === added.line
          );
          return found === undefined
            ? { held: false, why: `no finding at ${skill.path}:${added.line}, the added line` }
            : { held: true, why: `${found.kind} at ${skill.path}:${added.line}: ${found.why}` };
        },
      };
    }
    case "same-job": {
      const { input, planted, copyOf } = control;
      if (!input.skills.some((skill) => skill.id === copyOf)) {
        throw new Error(`the same-job control's original ${copyOf} is no id of the skills`);
      }
      if (input.skills.some((skill) => skill.id === planted.id)) {
        throw new Error(
          `the same-job control's planted id ${planted.id} is an id of the skills already`
        );
      }
      return {
        input: { skills: [...input.skills, planted] },
        held: (answer) => {
          const pair = SameJobAnswer.parse(answer).pairs.find(
            ({ a, b }) => (a === planted.id && b === copyOf) || (a === copyOf && b === planted.id)
          );
          return pair === undefined
            ? { held: false, why: `no pair of ${planted.id} and ${copyOf}` }
            : { held: true, why: `${planted.id} and ${copyOf} are paired: ${pair.task}` };
        },
      };
    }
    case "sentence": {
      const { input, into, sentence } = control;
      const skill = input.skills.find((each) => each.id === into);
      if (skill === undefined)
        throw new Error(`the sentence control's skill ${into} is no id of the skills`);
      const added = withLineAdded(skill.text, sentence);
      return {
        input: {
          skills: input.skills.map((each) =>
            each === skill ? { ...each, text: added.text } : each
          ),
        },
        held: (answer) => {
          const found = SentenceAnswer.parse(answer).found.find(
            (each) => each.id === into && each.line === added.line
          );
          return found === undefined
            ? {
                held: false,
                why: `no sentence found in ${into} at line ${added.line}, the added line`,
              }
            : { held: true, why: `${into} line ${added.line}: ${found.quote}` };
        },
      };
    }
  }
}

/** Runs `control`: its reader on its planted input, once, and its check of the answer. */
export async function runControl(
  control: Control,
  gateway: Gateway
): Promise<Held & { result: ReaderResult }> {
  const planted = plantControl(control);
  const result = await runReader(control.reader, planted.input, gateway);
  return { ...planted.held(result.answer), result };
}
