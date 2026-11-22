/**
 * The five arms of a case: `old` (the skill's base text, `--old`), `new` (the
 * cut text, `--new`), `none` (no skill), `minus` (a copy of the old directory less every line
 * of the case's baseFile that is the case's line, trimmed, or less every line of any of its files
 * that is `--planted-line`, trimmed, when one is given: a rule written twice is still the rule, so
 * each copy of it goes) and `twin` (the old directory again, built as `old` is: the false-loss
 * control's second copy of the old arm, with its own capture and runs). Only the skill's directory
 * differs between arms: the messages are the case's own but for the text of the skill's files in
 * its read results, and the profile is the case's own, each copy of the skill's source (cases.ts's
 * `testedCopy`: its repository and path, under its own name or a namespace) installed from the
 * arm's directory, which `none` leaves out; a copy of another source that shares the name stays
 * as the profile has it in every arm.
 *
 * In each arm but `none`, each read result of a file of the skill (the case's `skillReads`) is
 * answered from the arm's file the way the pinned Oh My Pi 18.18.6 answers a `skill://` read: one
 * line `[Skill file: <absolute path>]`, then the file's text less its final newline (F1). The path
 * is never the arm's own directory: it is the one the real result shows, the skill's installed
 * path, so no arm's directory name reaches the model and the lines are the same in every arm. A
 * file the arm lacks gets Oh My Pi's not-found result, in the shape of F1's read of a missing
 * skill (one text part, empty `details`, `isError`): `File not found: <that absolute path>` when
 * the arm has the skill's directory, and `Unknown skill: <skill>\nAvailable: <the profile's other
 * skills>` when it has none. Those texts are the pinned Oh My Pi's own, read from its answers to
 * `read skill://alpha/references/missing.md` and `read skill://gamma` in a scratch profile. In
 * `none`, each read call of the skill and its result are removed, and an assistant message left
 * with no text and no tool call goes too.
 *
 * `skillFile(arm, file)` answers one new read the same way, at the arm's `installedDir`, and is
 * what replay.ts answers a reply's read with; cases.ts's `skillReadFile(arm.skill, target)` says
 * which file a read reads.
 *
 * A skill can reach a session other than by its listing line and its reads, and each arm puts its
 * own text there too (`Injection`, `arm.injections`):
 *
 * - **A plugin's block.** The Envoy plugin puts dispatch-first into every request of a session
 *   with Dispatch (PLUGIN_INJECTED; packages/pi-envoy/extensions/envoy.ts): a user message of one
 *   text part, stamped 0, after the leading compaction summaries, unless the message there already
 *   opens with the marker (pi-envoy's `withDispatchFirst`), its text `readDispatchFirstContext` of
 *   the plugin's own `skills/dispatch-first/SKILL.md` (packages/envoy-client/src/dispatch-first.ts),
 *   read as the plugin loads, so a plugin that finds no such file does not load. Oh My Pi stores
 *   nothing a `context` handler returns, so no transcript holds the block, and a capture runs with
 *   Dispatch off (capture.ts). So for a case of dispatch-first whose profile installs the plugin,
 *   each arm but `none` puts the block, from the arm's SKILL.md through the plugin's own function,
 *   at that place in its messages (taking out any block there), and `none` puts none. A case does
 *   not record whether its session had Dispatch: the runner takes it that it did. context.ts
 *   installs a hidden stand-in of the file where the arm has no copy under that name (`none`), so
 *   the plugin loads in every arm and lists nothing more.
 * - **An autoloaded block.** Oh My Pi gives a subagent each skill its agent definition's
 *   `autoloadSkills` names as a `custom` message of type `skill-prompt` (SKILL_PROMPT) before its
 *   task, `display: false`, its details the skill's name and path (packages/coding-agent/src/task/
 *   executor.ts lines 5573-5607 at the pin, the tag v18.18.6-sami.20261119-162325 of
 *   cybermonklabs/oh-my-pi), its text `prompt.render` of prompts/skills/autoload.md trimmed
 *   (extensibility/skills.ts lines 968-976): the SKILL.md less its frontmatter, trimmed (lines
 *   901-906), a blank line, `---`, a blank line and `Skill: <path>`, all of it then put through
 *   Oh My Pi's `format` (packages/utils/src/prompt.ts lines 192-308 and 544-548; `formatPrompt`).
 *   The transcript keeps that message, so the case's messages hold it: each arm but `none`
 *   rebuilds its text from the arm's SKILL.md under the recorded path (`autoloadedText`), and
 *   `none` leaves the message out.
 *
 * A case whose messages hold a block of its skill that the runner cannot rebuild for each arm
 * (`injectionFault`: a `skill-prompt` message of the skill in another form, such as one the user
 * invoked with `/skill:`, or a message that opens with the plugin's marker away from where the
 * plugin puts its block) is refused: buildArm throws InjectionRefused naming it.
 */
import {
  cpSync,
  existsSync,
  readdirSync,
  readFileSync,
  realpathSync,
  rmSync,
  statSync,
  writeFileSync,
} from "node:fs";
import path from "node:path";
import {
  DISPATCH_FIRST_MARKER,
  readDispatchFirstContext,
} from "@legion/envoy-client/dispatch-first";
import {
  readTargets,
  type SessionMessage,
  skillFileHeader,
  skillHeaderPath,
  stripSkillHeader,
} from "../skill-scenarios/transcript";
import {
  type Case,
  listedName,
  type ProfileSkill,
  skillReadFile,
  testedCopy,
  textLines,
} from "./cases";
import type { Arm as ScoreArm } from "./score";

/** The name of an arm, as run records carry it. */
export type ArmName = ScoreArm;

/** The name a profile gives the Envoy plugin: its directory's (build.ts names each plugin after
 *  its directory, `packages/pi-envoy` in cybermonklabs/legion). */
export const ENVOY_PLUGIN = "pi-envoy";

/** The skill a plugin puts into a session's requests itself, under the name the plugin reads its
 *  file by, and that plugin (see the file's header). */
export const PLUGIN_INJECTED = { skill: "dispatch-first", plugin: ENVOY_PLUGIN } as const;

/** The custom message type of a skill's text Oh My Pi puts into a session
 *  (SKILL_PROMPT_MESSAGE_TYPE, packages/tui/src/chat/messages.ts line 21 at the pin). */
export const SKILL_PROMPT = "skill-prompt";

/** A block of the case's skill that reaches its session beside the listing line and the reads,
 *  and where the arm's messages hold it (`at`; null for `none`, which holds none): one a plugin
 *  puts into each request (`plugin`, as a profile names it; `name`, the skill's name the plugin
 *  reads its file under), or one Oh My Pi autoloads into a subagent (`path`, the SKILL.md path the
 *  block names, as recorded). */
export type Injection =
  | {
      readonly by: "plugin";
      readonly plugin: string;
      readonly name: string;
      readonly at: number | null;
    }
  | { readonly by: "autoload"; readonly path: string; readonly at: number | null };

/** What buildArm, and a capture, throw for a case that holds a block of its skill the runner
 *  cannot give each arm's text, its message naming the case: behaviour refuses the case on it
 *  (cli.ts). */
export class InjectionRefused extends Error {}

/** One arm of a case. */
export interface Arm {
  readonly name: ArmName;
  /** The case's skill, as `skill://<skill>` names it. */
  readonly skill: string;
  /** The arm's directory of the skill, absolute, or undefined for `none`. The model never sees
   *  it. */
  readonly skillDir: string | undefined;
  /** The skill's directory as the model sees it, the same in every arm of a case: the installed
   * directory the case's first `[Skill file: …]` line of the skill shows, or `/skills/<skill>`
   * when the case has none. */
  readonly installedDir: string;
  /** The case's messages, as this arm gives them. */
  readonly messages: SessionMessage[];
  /** The skills the arm's profile installs: the case's own, less each copy of the case's skill's
   * source for `none` (cases.ts's `testedCopy`). Each such copy is installed from `skillDir`. */
  readonly profileSkills: ProfileSkill[];
  /** Each block of the skill that reaches the session beside its listing line and its reads, and
   *  where this arm holds it (Injection). Absent, or empty, when the case has none. */
  readonly injections?: readonly Injection[];
}

/** The directories the arms are built from: each is the skill's own directory, SKILL.md at its
 * top. */
export interface ArmDirs {
  readonly old: string;
  readonly new: string;
  /** The line `minus` leaves out in place of the case's line. */
  readonly plantedLine?: string;
}

/** The answer to a read of `file` (a path inside the skill's directory) from the arm's files,
 * shown at `shown`: the header and the text, or the not-found text with `isError`. */
function answer(arm: Arm, file: string, shown: string): { text: string; isError: boolean } {
  if (arm.skillDir === undefined) {
    const names = arm.profileSkills.map((skill) => listedName(skill));
    const available = names.length > 0 ? names.join(", ") : "none";
    return { text: `Unknown skill: ${arm.skill}\nAvailable: ${available}`, isError: true };
  }
  const full = path.resolve(arm.skillDir, file);
  const inside = path.relative(arm.skillDir, full);
  if (inside === ".." || inside.startsWith(`..${path.sep}`) || path.isAbsolute(inside)) {
    throw new Error(`${file} is not a path inside the skill directory ${arm.skillDir}`);
  }
  const stats = statSync(full, { throwIfNoEntry: false });
  if (stats === undefined) return { text: `File not found: ${shown}`, isError: true };
  if (!stats.isFile()) throw new Error(`${full} is not a file: an arm answers reads of files only`);
  const text = readFileSync(full, "utf8").replace(/\n$/, "");
  return { text: `${skillFileHeader(shown)}\n${text}`, isError: false };
}

/** The answer to a new read of `file` (a path inside the skill's directory) from the arm's files,
 * at the arm's `installedDir`: the header and the text, or the not-found text with `isError`. */
export function skillFile(arm: Arm, file: string): { text: string; isError: boolean } {
  return answer(arm, file, path.join(arm.installedDir, file));
}

/** A read result of the case's messages answered from the arm's files at `shown`: F1's shape of a
 * read of a skill file, or of a read that failed. */
function answered(
  message: SessionMessage,
  arm: Arm,
  read: { file: string; target: string; shown: string }
): SessionMessage {
  const { text, isError } = answer(arm, read.file, read.shown);
  if (isError) return { ...message, content: [{ type: "text", text }], details: {}, isError };
  const body = stripSkillHeader(text);
  const lineNumbers = textLines(body).map((_, index) => index + 1);
  return {
    ...message,
    content: [{ type: "text", text }],
    details: {
      resolvedPath: read.shown,
      totalLines: lineNumbers.length,
      rowPrefixes: false,
      displayContent: { text: body, startLine: 1, lineNumbers },
      meta: { source: { type: "internal", value: read.target } },
      ...(read.file.endsWith(".md") ? { contentType: "text/markdown" } : {}),
    },
    isError,
  };
}

/** The directory `dir` as an absolute path, or a throw naming the arm when it is no directory. */
function directory(dir: string, name: ArmName): string {
  if (!statSync(dir, { throwIfNoEntry: false })?.isDirectory()) {
    throw new Error(`the ${name} directory ${dir} is not a directory`);
  }
  return path.resolve(dir);
}

/** Leaves out of `file` each line that is `line` (trimmed). Returns how many it left out. */
function removeLine(file: string, line: string): number {
  const text = readFileSync(file, "utf8");
  const lines = text.split("\n");
  const kept = lines.filter((each) => each.trim() !== line.trim());
  if (kept.length < lines.length) writeFileSync(file, kept.join("\n"));
  return lines.length - kept.length;
}

/** Whether `from`, a path of the old directory `old`, is copied into minus: not when it is a link
 *  that names nothing, so a read of it answers not found from the copy as from `old`. Throws when
 *  it is a link to `old` or to a directory that holds it, whose copy would hold itself. */
function copiedIntoMinus(old: string, from: string): boolean {
  if (from === old) return true;
  if (!existsSync(from)) return false;
  const back = path.relative(realpathSync(from), realpathSync(old));
  if (back !== ".." && !back.startsWith(`..${path.sep}`) && !path.isAbsolute(back)) {
    throw new Error(`${from} names a directory that holds ${old}, so minus cannot copy it`);
  }
  return true;
}

/** The `minus` directory: a copy of `old` under `scratch`, less each line of its baseFile that is
 * the case's line (trimmed), or less each line that is `plantedLine` in each file of the copy that
 * holds it. Each symbolic link of `old` is copied as the file or directory it names, so an edit of
 * the copy never writes through a link into `old` or elsewhere; a link that names nothing is left
 * out (`copiedIntoMinus`). Throws when no line is left out. */
function minusDirectory(c: Case, dirs: ArmDirs, scratch: string): string {
  const old = directory(dirs.old, "old");
  if (dirs.plantedLine?.trim() === "") throw new Error("the planted line is blank");
  const dir = path.resolve(scratch, "minus");
  rmSync(dir, { recursive: true, force: true });
  cpSync(old, dir, {
    recursive: true,
    dereference: true,
    filter: (from) => copiedIntoMinus(old, from),
  });
  const { plantedLine } = dirs;
  const removed =
    plantedLine === undefined
      ? removeLine(path.join(dir, c.baseFile), c.line.text)
      : readdirSync(dir, { recursive: true, encoding: "utf8" })
          .map((file) => path.join(dir, file))
          .filter((file) => statSync(file).isFile())
          .reduce((sum, file) => sum + removeLine(file, plantedLine), 0);
  if (removed === 0) {
    const what =
      plantedLine === undefined
        ? `its line is not in ${c.baseFile}`
        : `the planted line is in no file of ${old}`;
    throw new Error(`case ${c.id}: the minus arm would equal old: ${what}`);
  }
  return dir;
}

/** A table row and a table's separator row, as Oh My Pi's `format` finds them. */
const TABLE_ROW = /^\|.*\|$/;
const TABLE_SEP = /^\|[-:\s|]+\|$/;

/** A table separator row with each cell `---`, `:---`, `---:` or `:---:`, as `format` writes it. */
function compactSeparator(line: string): string {
  const cells = line
    .split("|")
    .filter((cell) => cell.trim() !== "")
    .map((cell) => {
      const trimmed = cell.trim();
      const left = trimmed.startsWith(":");
      const right = trimmed.endsWith(":");
      return left && right ? ":---:" : left ? ":---" : right ? "---:" : "---";
    });
  return `|${cells.join("|")}|`;
}

/**
 * `content` as Oh My Pi's `format` leaves a rendered prompt (packages/utils/src/prompt.ts lines
 * 192-308 at the pin, with its defaults: no ASCII-symbol or RFC 2119 rewrite): each line less its
 * trailing whitespace; a line that opens a ``` or ~~~ fence, and each line inside one, kept as it
 * is; elsewhere a run of two or more blank lines dropped whole, a single blank line dropped where
 * it opens the text, the blank lines before a closing tag line (`</name>`) dropped, and a table
 * row's cells less their padding (a separator row's each `---`, `:---`, `---:` or `:---:`); then
 * the text's last blank lines dropped.
 */
export function formatPrompt(content: string): string {
  const lines = content.split("\n");
  const result: string[] = [];
  const dropBlanks = () => {
    while (result.length > 0 && result.at(-1) === "") result.pop();
  };
  const blank = (line: string | undefined) => line !== undefined && !/\S/.test(line);
  let fenced = false;
  for (let i = 0; i < lines.length; i++) {
    let line = (lines[i] ?? "").trimEnd();
    // The indent: spaces and tabs, or any leading whitespace where a character above ASCII
    // follows them, as `format` measures it.
    let s = 0;
    let first = line.charCodeAt(0);
    while (first === 32 || first === 9) first = line.charCodeAt(++s);
    if (first >= 128) {
      s = line.length - line.trimStart().length;
      first = line.charCodeAt(s);
    }
    if (
      (first === 96 || first === 126) &&
      (line.startsWith("```", s) || line.startsWith("~~~", s))
    ) {
      fenced = !fenced;
      result.push(line);
      continue;
    }
    if (fenced) {
      result.push(line);
      continue;
    }
    const rest = line.slice(s);
    // A closing tag line, `</name>` of `a`-`z`, `-` and `_`.
    const closing = first === 60 && /^<\/[a-z_-]+>$/.test(rest);
    if (first === 124 && TABLE_SEP.test(rest)) {
      line = `${line.slice(0, s)}${compactSeparator(rest)}`;
    } else if (first === 124 && TABLE_ROW.test(rest)) {
      line = `${line.slice(0, s)}${rest
        .split("|")
        .map((cell) => cell.trim())
        .join("|")}`;
    }
    if (s >= line.length) {
      const next = lines[i + 1];
      if (next === undefined || blank(next)) {
        dropBlanks();
        while (blank(lines[i + 1])) i++;
        continue;
      }
      if (result.length === 0 || result.at(-1) === "") continue;
    }
    if (closing) dropBlanks();
    result.push(line);
  }
  dropBlanks();
  return result.join("\n");
}

/** The body of a SKILL.md's text as Oh My Pi reads it for an autoloaded block
 *  (`loadSkillPromptBody`: `parseFrontmatter` with `repair: false`, packages/utils/src/
 *  frontmatter.ts lines 148-163 at the pin): CRLF as LF, the text after the first `\n---` past
 *  an opening `---` (the whole text when it opens with none, or has no such line), trimmed. */
function skillBody(content: string): string {
  const text = content.replace(/\r\n?/g, "\n");
  const end = text.startsWith("---") ? text.indexOf("\n---", 3) : -1;
  return (end === -1 ? text : text.slice(end + 4)).trim();
}

/** The text of Oh My Pi's autoloaded block of the SKILL.md in `skillDir`, naming `file` as its
 *  path: prompts/skills/autoload.md rendered with no arguments (`<body>`, a blank line, `---`, a
 *  blank line, `Skill: <file>`), put through `formatPrompt` and trimmed. */
export function autoloadedText(skillDir: string, file: string): string {
  const body = skillBody(readFileSync(path.join(skillDir, "SKILL.md"), "utf8"));
  return formatPrompt(`${body}\n\n---\n\nSkill: ${file}\n`).trim();
}

/** `details.name` and `details.path` of a message, where they are strings. */
function detailsOf(message: SessionMessage): { name?: string; path?: string } {
  const { details } = message;
  if (typeof details !== "object" || details === null) return {};
  const name: unknown = Reflect.get(details, "name");
  const file: unknown = Reflect.get(details, "path");
  return {
    ...(typeof name === "string" ? { name } : {}),
    ...(typeof file === "string" ? { path: file } : {}),
  };
}

/** Whether `message` is a `skill-prompt` message of `skill`, in any form. */
function isSkillPrompt(message: SessionMessage, skill: string): boolean {
  return (
    message.role === "custom" &&
    message.customType === SKILL_PROMPT &&
    detailsOf(message).name === skill
  );
}

/** The SKILL.md path an autoloaded block of `skill` names, when `message` is one in the form Oh My
 *  Pi writes it (see the file's header): a `skill-prompt` message of the skill the user did not
 *  invoke, of one text part that ends `Skill: <its details.path>` after a blank line, `---` and a
 *  blank line. Else undefined. */
function autoloadedPath(message: SessionMessage, skill: string): string | undefined {
  if (!isSkillPrompt(message, skill) || message.attribution === "user") return undefined;
  const file = detailsOf(message).path;
  const [part, ...more] = message.content ?? [];
  if (file === undefined || file === "" || more.length > 0 || part?.type !== "text") {
    return undefined;
  }
  return part.text?.endsWith(`\n\n---\n\nSkill: ${file}`) === true ? file : undefined;
}

/** Where the Envoy plugin puts its block among `messages`: after the leading compaction
 *  summaries (pi-envoy's `withDispatchFirst`). */
function blockPlace(messages: readonly SessionMessage[]): number {
  const at = messages.findIndex((message) => message.role !== "compactionSummary");
  return at === -1 ? messages.length : at;
}

/** Whether `message` opens with the plugin's marker: a user or custom message whose first part's
 *  text does, as the plugin finds its own block (pi-envoy's `leadingText`). */
function opensWithMarker(message: SessionMessage | undefined): boolean {
  return (
    (message?.role === "user" || message?.role === "custom") &&
    message.content?.[0]?.text?.startsWith(DISPATCH_FIRST_MARKER) === true
  );
}

/** Whether the plugin of PLUGIN_INJECTED puts the case's skill into its session's requests: the
 *  case's skill is the one it reads, listed under that bare name, and the profile installs it. */
function pluginInjects(c: Pick<Case, "skill" | "profile">): boolean {
  return (
    c.skill === PLUGIN_INJECTED.skill &&
    c.profile.plugins.some((plugin) => plugin.name === PLUGIN_INJECTED.plugin)
  );
}

/** `messages` with the plugin's block (see the file's header) of the skill in `skillDir` at its
 *  place, less any block there already, or with none when `skillDir` is undefined; and where the
 *  block went (null for none). */
export function withPluginBlock(
  messages: readonly SessionMessage[],
  skillDir: string | undefined
): { messages: SessionMessage[]; at: number | null } {
  const at = blockPlace(messages);
  const kept =
    messages[at]?.role === "user" && opensWithMarker(messages[at])
      ? messages.toSpliced(at, 1)
      : [...messages];
  if (skillDir === undefined) return { messages: kept, at: null };
  const text = readDispatchFirstContext(path.join(skillDir, "SKILL.md"));
  const block: SessionMessage = { role: "user", content: [{ type: "text", text }], timestamp: 0 };
  return { messages: kept.toSpliced(at, 0, block), at };
}

/** Why the arms of `c` cannot each give the session their own text of its skill, or undefined
 *  when they can: each `skill-prompt` message of the skill that is not an autoloaded block in the
 *  form Oh My Pi writes one (one the user invoked with `/skill:`, one with arguments or another
 *  shape), and, where the plugin puts the skill into the requests, each message that opens with
 *  its marker away from where it puts its block. */
export function injectionFault(
  c: Pick<Case, "skill" | "profile" | "messages">
): string | undefined {
  const faults: string[] = [];
  for (const [index, message] of c.messages.entries()) {
    if (isSkillPrompt(message, c.skill) && autoloadedPath(message, c.skill) === undefined) {
      const invoked = message.attribution === "user" ? ", one the user invoked," : "";
      faults.push(
        `its message ${index} is a ${SKILL_PROMPT} message of ${c.skill}${invoked} not in the form of Oh My Pi's autoloaded block (one text part that ends "Skill: <its details.path>")`
      );
    }
  }
  if (pluginInjects(c)) {
    const at = blockPlace(c.messages);
    for (const [index, message] of c.messages.entries()) {
      if (index === at && message.role === "user") continue;
      if (opensWithMarker(message)) {
        faults.push(
          `its message ${index} opens with ${DISPATCH_FIRST_MARKER} away from where the ${PLUGIN_INJECTED.plugin} plugin puts its block (message ${at}, after the leading compaction summaries)`
        );
      }
    }
  }
  return faults.length === 0 ? undefined : faults.join("; ");
}

/** `messages`, an arm's, with each block of `c`'s skill that reaches the session beside its
 *  listing line and its reads given the text of the skill in `skillDir`, or left out when it is
 *  undefined (`none`): each autoloaded block rebuilt under its recorded path, and the plugin's
 *  block put in where the plugin puts it into the case's session. */
function withInjections(
  c: Case,
  messages: readonly SessionMessage[],
  skillDir: string | undefined
): { messages: SessionMessage[]; injections: Injection[] } {
  const injections: Injection[] = [];
  // Each autoloaded block the arm holds, by its message, with the path it names.
  const rebuilt = new Map<SessionMessage, string>();
  let out: SessionMessage[] = [];
  for (const message of messages) {
    const file = autoloadedPath(message, c.skill);
    if (file === undefined) out.push(message);
    else if (skillDir === undefined) injections.push({ by: "autoload", path: file, at: null });
    else {
      const block = {
        ...message,
        content: [{ type: "text", text: autoloadedText(skillDir, file) }],
      };
      rebuilt.set(block, file);
      out.push(block);
    }
  }
  if (pluginInjects(c)) {
    const placed = withPluginBlock(out, skillDir);
    out = placed.messages;
    injections.push({
      by: "plugin",
      plugin: PLUGIN_INJECTED.plugin,
      name: PLUGIN_INJECTED.skill,
      at: placed.at,
    });
  }
  for (const [index, message] of out.entries()) {
    const file = rebuilt.get(message);
    if (file !== undefined) injections.push({ by: "autoload", path: file, at: index });
  }
  return { messages: out, injections };
}

/** The arm `name` of case `c`, from the skill directories `dirs`. `minus` writes its copy of the
 * old directory to `<scratch>/minus`, replacing one there. Throws naming the case when a
 * skillReads index is not a read of a `skill://<skill>` file, and InjectionRefused when the case
 * holds a block of its skill the arms cannot each give their text (`injectionFault`). */
export function buildArm(c: Case, name: ArmName, dirs: ArmDirs, scratch: string): Arm {
  const fault = injectionFault(c);
  if (fault !== undefined) throw new InjectionRefused(`case ${c.id}: ${fault}`);
  const targets = readTargets(c.messages);
  const reads = new Map(
    c.skillReads
      .toSorted((a, b) => a - b)
      .map((index) => {
        const message = c.messages[index];
        const target = targets.get(message?.toolCallId ?? "") ?? "";
        const file = message?.role === "toolResult" ? skillReadFile(c.skill, target) : undefined;
        if (file === undefined) {
          throw new Error(`case ${c.id}: skillReads index ${index} is not a read of a skill file`);
        }
        const header = skillHeaderPath(message?.content?.[0]?.text ?? "");
        return [index, { file, target, header, id: message?.toolCallId ?? "" }];
      })
  );
  // The installed directory: the first header's path less the file it read.
  let installedDir = path.join("/skills", c.skill);
  for (const { file, header } of reads.values()) {
    if (header?.endsWith(`/${file}`)) {
      installedDir = header.slice(0, -(file.length + 1));
      break;
    }
  }

  if (name === "none") {
    const ids = new Set([...reads.values()].map((read) => read.id));
    const messages = c.messages.flatMap((message, index): SessionMessage[] => {
      if (reads.has(index)) return [];
      const content = message.content;
      if (message.role !== "assistant" || content === undefined) return [structuredClone(message)];
      const kept = content.filter((part) => !(part.type === "toolCall" && ids.has(part.id ?? "")));
      if (kept.length === content.length) return [structuredClone(message)];
      if (!kept.some((part) => part.type === "text" || part.type === "toolCall")) return [];
      return [structuredClone({ ...message, content: kept })];
    });
    const tested = testedCopy(c.profile.skills, c.skill);
    return {
      name,
      skill: c.skill,
      skillDir: undefined,
      installedDir,
      ...withInjections(c, messages, undefined),
      profileSkills: structuredClone(c.profile.skills.filter((skill) => !tested(skill))),
    };
  }

  const skillDir =
    name === "minus"
      ? minusDirectory(c, dirs, scratch)
      : directory(dirs[name === "twin" ? "old" : name], name);
  const arm: Arm = {
    name,
    skill: c.skill,
    skillDir,
    installedDir,
    messages: [],
    profileSkills: structuredClone(c.profile.skills),
  };
  const messages = c.messages.map((message, index) => {
    const read = reads.get(index);
    if (read === undefined) return structuredClone(message);
    // The real result's own header path, else the installed directory's: never the arm's.
    const shown = read.header ?? path.join(installedDir, read.file);
    return structuredClone(answered(message, arm, { ...read, shown }));
  });
  return { ...arm, ...withInjections(c, messages, skillDir) };
}
