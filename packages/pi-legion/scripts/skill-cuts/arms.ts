/**
 * The six arms of a case: `old` (the skill's base text, `--old`), `new` (the
 * cut text, `--new`), `ours` (a skill of ours, `--ours`, under its own name: see below), `none`
 * (no skill), `minus` (a copy of the old directory less every line
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
 * In each arm but `none` and `ours`, each read result of a file of the skill (the case's
 * `skillReads`) is answered from the arm's file the way the pinned Oh My Pi 18.18.6 answers a
 * `skill://` read: one
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
 * `ours` is a skill whose frontmatter name (and directory name) may differ from the case's skill
 * (`ArmDirs.ours` gives its directory and its name). Its profile is the case's less each copy of
 * their skill's source, as `none` leaves them out, with ours under our name in the place of the
 * copy listed under theirs; where the profile already lists a skill of another source under our
 * name, the `--ours` text replaces that entry, so no name is listed twice (context.ts refuses one
 * installed twice), and the arm records that it replaced it. Its installed directory is theirs
 * with our name as its last part. Each history read of a file of theirs becomes a read of
 * `skill://<ours>` at the same path inside the skill (the call's `path` argument), answered from
 * our directory at our installed directory, when ours has that file; when ours lacks it, the read
 * call and its result are removed as `none` removes them, and the arm records the read's path
 * inside the skill. So a renamed unchanged copy of their skill gives old's texts but for the
 * name. A reply's read of their skill gets Oh My Pi's unknown-skill text (`unknownSkill`): the
 * arm does not install it.
 *
 * `skillFile(arm, file)` answers one new read the same way, at the arm's `installedDir`, and is
 * what replay.ts answers a reply's read with; cases.ts's `skillReadFile(arm.skill, target)` says
 * which file a read reads.
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

/** One arm of a case. */
export interface Arm {
  readonly name: ArmName;
  /** The skill the arm installs, as `skill://<skill>` names it: the case's skill, or in `ours`
   *  our skill's name. */
  readonly skill: string;
  /** The arm's directory of the skill, absolute, or undefined for `none`. The model never sees
   *  it. */
  readonly skillDir: string | undefined;
  /** The skill's directory as the model sees it, the same in every arm of a case but `ours`: the
   * installed directory the case's first `[Skill file: …]` line of the skill shows, or
   * `/skills/<skill>` when the case has none; in `ours`, that directory with our name as its last
   * part. */
  readonly installedDir: string;
  /** The case's messages, as this arm gives them. */
  readonly messages: SessionMessage[];
  /** The skills the arm's profile installs: the case's own, less each copy of the case's skill's
   * source for `none` (cases.ts's `testedCopy`), and for `ours` less those too, with ours in the
   * place of the copy listed under the case's skill or of the skill listed under our name. Each
   * copy of the arm's skill's source (`testedCopy(profileSkills, skill)`) is installed from
   * `skillDir`. */
  readonly profileSkills: ProfileSkill[];
  /** In `ours` only: the case's skill (`theirs`), which the arm does not install; whether our text
   *  replaced a skill of our name that the profile held (`replacedOwn`); and the path inside the
   *  skill of each history read of theirs removed because ours has no file there
   *  (`removedReads`), in the history's order. */
  readonly ours?: {
    readonly theirs: string;
    readonly replacedOwn: boolean;
    readonly removedReads: readonly string[];
  };
}

/** The directories the arms are built from: each is the skill's own directory, SKILL.md at its
 * top. `new` is given with `--new`, and `ours`, with its name, with `--ours`. */
export interface ArmDirs {
  readonly old: string;
  readonly new?: string;
  readonly ours?: { readonly dir: string; readonly name: string };
  /** The line `minus` leaves out in place of the case's line. */
  readonly plantedLine?: string;
}

/** Oh My Pi's answer to a read of `skill`, which `arm` does not install: one line naming it, then
 *  the names Oh My Pi lists the arm's profile skills under (cases.ts's `listedName`). */
export function unknownSkill(arm: Arm, skill: string): { text: string; isError: true } {
  const names = arm.profileSkills.map((each) => listedName(each));
  const available = names.length > 0 ? names.join(", ") : "none";
  return { text: `Unknown skill: ${skill}\nAvailable: ${available}`, isError: true };
}

/** The answer to a read of `file` (a path inside the skill's directory) from the arm's files,
 * shown at `shown`: the header and the text, or the not-found text with `isError`. */
function answer(arm: Arm, file: string, shown: string): { text: string; isError: boolean } {
  if (arm.skillDir === undefined) return unknownSkill(arm, arm.skill);
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

/** The result of the read call `toolCallId` of `read.target`, which reads `read.file` (a path
 *  inside the skill's directory), answered from the arm's files at `read.shown` as every arm but
 *  `none` answers a history read: F1's shape of a read of a skill file, or of a read that failed.
 *  `build-cases --written` answers a written case's read so, from `--old`. */
export function readResult(
  arm: Arm,
  read: { file: string; target: string; shown: string },
  toolCallId: string
): SessionMessage {
  return answered({ role: "toolResult", toolCallId, toolName: "read" }, arm, read);
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

/** A read of a file of the case's skill in its history: the file (a path inside the skill), the
 *  target its call read, the path its result's header shows, and its call's id. */
interface HistoryRead {
  readonly file: string;
  readonly target: string;
  readonly header: string | undefined;
  readonly id: string;
}

/** `messages` less each read result at an index of `removed` and each call of those reads, an
 *  assistant message left with no text and no tool call going too: what `none` gives of the
 *  history. */
function withoutReads(
  messages: readonly SessionMessage[],
  removed: ReadonlyMap<number, HistoryRead>
): SessionMessage[] {
  const ids = new Set([...removed.values()].map((read) => read.id));
  return messages.flatMap((message, index): SessionMessage[] => {
    if (removed.has(index)) return [];
    const content = message.content;
    if (message.role !== "assistant" || content === undefined) return [structuredClone(message)];
    const kept = content.filter((part) => !(part.type === "toolCall" && ids.has(part.id ?? "")));
    if (kept.length === content.length) return [structuredClone(message)];
    if (!kept.some((part) => part.type === "text" || part.type === "toolCall")) return [];
    return [structuredClone({ ...message, content: kept })];
  });
}

/** The arm of ours of case `c` (the module's comment says what it holds), from `ours`, our
 *  directory and name; `reads` are the case's reads of its skill's files, by index, and
 *  `installedDir` their installed directory. */
function oursArm(
  c: Case,
  ours: { readonly dir: string; readonly name: string },
  reads: ReadonlyMap<number, HistoryRead>,
  installedDir: string
): Arm {
  const skillDir = directory(ours.dir, "ours");
  // Theirs goes as `none` leaves it out: each copy of its source. Ours takes the place of the copy
  // listed under their name, or of a skill of another source listed under ours, never beside it.
  const tested = testedCopy(c.profile.skills, c.skill);
  const replacedOwn = c.profile.skills.some(
    (skill) => !tested(skill) && listedName(skill) === ours.name
  );
  const profileSkills = c.profile.skills.flatMap((skill) => {
    if (!tested(skill)) return [skill];
    if (replacedOwn || listedName(skill) !== c.skill) return [];
    return [{ repo: skill.repo, commit: skill.commit, path: skill.path, name: ours.name }];
  });
  const arm: Arm = {
    name: "ours",
    skill: ours.name,
    skillDir,
    installedDir: path.join(path.dirname(installedDir), ours.name),
    messages: [],
    profileSkills: structuredClone(profileSkills),
  };
  const lacking = new Map(
    [...reads].filter(
      ([, read]) =>
        !statSync(path.resolve(skillDir, read.file), { throwIfNoEntry: false })?.isFile()
    )
  );
  // Each kept read's call, by its id, reads our skill's URL at the same path inside the skill.
  const theirs = `skill://${c.skill}`;
  const targets = new Map(
    [...reads]
      .filter(([index]) => !lacking.has(index))
      .map(([, read]) => [read.id, `skill://${ours.name}${read.target.slice(theirs.length)}`])
  );
  const messages = c.messages.map((message, index): SessionMessage => {
    const read = reads.get(index);
    if (read !== undefined && !lacking.has(index)) {
      const target = targets.get(read.id) ?? read.target;
      const shown = path.join(arm.installedDir, read.file);
      return structuredClone(answered(message, arm, { file: read.file, target, shown }));
    }
    const content = message.content;
    if (message.role !== "assistant" || content === undefined) return structuredClone(message);
    const renamed = content.map((part) => {
      const target = part.type === "toolCall" ? targets.get(part.id ?? "") : undefined;
      if (target === undefined) return part;
      return { ...part, arguments: { ...Object(part.arguments), path: target } };
    });
    return structuredClone({ ...message, content: renamed });
  });
  return {
    ...arm,
    messages: withoutReads(messages, lacking),
    ours: {
      theirs: c.skill,
      replacedOwn,
      removedReads: [...lacking.values()].map((read) => read.file),
    },
  };
}

/** The arm `name` of case `c`, from the skill directories `dirs`. `minus` writes its copy of the
 * old directory to `<scratch>/minus`, replacing one there. Throws naming the case when a
 * skillReads index is not a read of a `skill://<skill>` file, or when `dirs` lacks the arm's
 * directory. */
export function buildArm(c: Case, name: ArmName, dirs: ArmDirs, scratch: string): Arm {
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

  if (name === "ours") {
    if (dirs.ours === undefined) throw new Error(`case ${c.id}: the ours arm needs --ours`);
    return oursArm(c, dirs.ours, reads, installedDir);
  }
  if (name === "none") {
    const messages = withoutReads(c.messages, reads);
    const tested = testedCopy(c.profile.skills, c.skill);
    return {
      name,
      skill: c.skill,
      skillDir: undefined,
      installedDir,
      messages,
      profileSkills: structuredClone(c.profile.skills.filter((skill) => !tested(skill))),
    };
  }

  const given = name === "new" ? dirs.new : dirs.old;
  if (given === undefined) throw new Error(`case ${c.id}: the new arm needs --new`);
  const skillDir = name === "minus" ? minusDirectory(c, dirs, scratch) : directory(given, name);
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
  return { ...arm, messages };
}
