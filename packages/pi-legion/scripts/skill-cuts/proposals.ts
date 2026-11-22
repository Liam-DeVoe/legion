/**
 * The parts of the `proposals` command (README.md, "proposals"): the items of the named lists of a
 * pull request's body (`parseProposalItems`), the files and hunks of a `gh pr diff` output
 * (`splitDiff`), and the run that reads each item with the `proposal-target` and `proposal` readers
 * (readers.ts) and writes one entry per item (`runProposals`). cli.ts reads the flags and the
 * files, opens the gateway and calls these.
 *
 * An item is a line `` - `<id>` (<state>): <text> `` of a list: the `- ` items that follow the
 * line a `--list` text starts, inside the section of the `--section` heading, up to the next
 * non-item line after a blank line, or the next heading. The state is any text in the
 * parentheses. A list no `--list` names is never read.
 *
 * Each item is read in two calls. `proposal-target` gets the whole diff of the pull request its
 * id's prefix names (`41-1.1` → the diff of `41`), first and marked for the prompt cache, then the
 * item and the skills of each skills root; it answers the item's hunks and its targets. When every
 * target is `none`, the item's entry says no target was found, with the body's own words of the
 * item, and no second call is made. Else `proposal` gets the item, its hunks, each target's text
 * (a file of the repository root, or a skill's files from its skills root), the gaps of each
 * target skill's findings entry, the style rules and the reader properties; it answers what the
 * change fixes and makes.
 */
import { createHash } from "node:crypto";
import { existsSync, readFileSync, statSync } from "node:fs";
import path from "node:path";
import { z } from "zod";
import { json, Refused, writeWhole } from "./common";
import type { CraftList } from "./craftlist";
import { BudgetSpent, type Gateway, usdOf } from "./gateway";
import {
  CraftGap,
  diffFilesByPath,
  loadPrompt,
  type ProposalAnswer,
  type ProposalInput,
  type ProposalTargetAnswer,
  type ProposalTargetInput,
  ReaderFailed,
  type ReaderResult,
  rulesInput,
  runReader,
  skillFiles,
  userParts,
} from "./readers";
import type { StyleRule } from "./style";

// The items of a pull request's body.

/** An item of a named list: its id, its state (the text in its parentheses), its text, the
 *  `--list` text whose list holds it, and its line in the body, from 1. */
export interface ProposalItem {
  readonly id: string;
  readonly state: string;
  readonly text: string;
  readonly list: string;
  readonly line: number;
}

/** The item lines of named lists that the parser cannot read, each with its line: the command
 *  exits 1. */
export class ItemFaults extends Error {
  constructor(readonly faults: readonly string[]) {
    super(
      `${faults.length} item line${faults.length === 1 ? "" : "s"} of a named list cannot be read:\n${faults.map((fault) => `  ${fault}`).join("\n")}`
    );
    this.name = "ItemFaults";
  }
}

const HEADING = /^(#{1,6})\s+(.*\S)\s*$/;
const FENCE_LINE = /^\s*(?:```|~~~)/;
const ITEM_MARK = /^[-*+]\s/;
/** An item line: its id in backticks, its state in parentheses (which may hold one level of
 *  parentheses of its own), a colon and its text. */
const ITEM = /^[-*+]\s+`([^`]+)`\s+\(((?:[^()]|\([^()]*\))+)\):\s+(.*\S)\s*$/;
/** An id an entry's file can be named by. */
const ID = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;

/** Whether each line of `lines` is in a fenced code block: a fence line (``` or ~~~) itself, or a
 *  line between a fence and the next. A line in a fenced block is no heading and no list's line;
 *  cli.ts's `removeSection` reads a SKILL.md's headings by this rule too. */
export function fencedLines(lines: readonly string[]): boolean[] {
  let fenced = false;
  return lines.map((line) => {
    const fence = FENCE_LINE.test(line);
    if (fence) fenced = !fenced;
    return fence || fenced;
  });
}

/**
 * The items of each list of `body` that `lists` names, list by list in the order given, each in
 * its order. A list is the `- ` items after the one line of the section of the heading `section`
 * that starts with its text, up to the next non-item line after a blank line, or the next heading;
 * a line indented under an item goes on with its text. Throws a refusal when no heading or more
 * than one is `section`, when no line of the section or more than one starts with a list's text,
 * when two lists name one line, or when no item follows a list's line; and ItemFaults naming each
 * line of a named list that is not an item of the form `` - `<id>` (<state>): <text> ``, gives an
 * id with a character other than a letter, a digit, `.`, `_` and `-`, or gives an id again, or
 * that follows an item, is no item and is not indented.
 */
export function parseProposalItems(
  body: string,
  section: string,
  lists: readonly string[]
): ProposalItem[] {
  const lines = body.replace(/\r\n?/g, "\n").split("\n");
  // A line in a fenced block is no heading and no list's line.
  const inFence = fencedLines(lines);
  const headings = lines.map((line, index) => {
    const heading = inFence[index] ? null : HEADING.exec(line);
    return heading === null ? undefined : { level: heading[1]?.length ?? 1, text: heading[2] };
  });
  const starts = headings.flatMap((heading, index) => (heading?.text === section ? [index] : []));
  const [start = 0] = starts;
  if (starts.length !== 1) {
    throw new Refused(
      `--section ${JSON.stringify(section)} names ${starts.length === 0 ? "no heading" : `${starts.length} headings`} of the body`
    );
  }
  const level = headings[start]?.level ?? 1;
  let end = start + 1;
  while (end < lines.length && (headings[end]?.level ?? 7) > level) end += 1;

  const items: { id: string; state: string; text: string; list: string; line: number }[] = [];
  const faults: string[] = [];
  const idLines = new Map<string, number>();
  const listLines = new Map<number, string>();
  for (const list of lists) {
    const found: number[] = [];
    for (let index = start + 1; index < end; index += 1) {
      const line = lines[index] ?? "";
      const plain = !inFence[index] && headings[index] === undefined && !ITEM_MARK.test(line);
      if (plain && line.startsWith(list)) found.push(index);
    }
    const [at = 0] = found;
    if (found.length !== 1) {
      throw new Refused(
        `--list ${JSON.stringify(list)} starts ${found.length === 0 ? "no line" : `${found.length} lines (${found.map((index) => index + 1).join(", ")})`} of the section ${JSON.stringify(section)}`
      );
    }
    const other = listLines.get(at);
    if (other !== undefined) {
      throw new Refused(
        `--list ${JSON.stringify(list)} and --list ${JSON.stringify(other)} start one line, ${at + 1}, of the body`
      );
    }
    listLines.set(at, list);
    const where = (index: number) => `line ${index + 1}, in the list ${JSON.stringify(list)},`;
    let count = 0;
    let blank = false;
    let current: (typeof items)[number] | undefined;
    for (let index = at + 1; index < end; index += 1) {
      const line = lines[index] ?? "";
      if (line.trim() === "") {
        blank = true;
        continue;
      }
      if (headings[index] !== undefined) break;
      if (ITEM_MARK.test(line)) {
        count += 1;
        blank = false;
        current = undefined;
        const [, id = "", state = "", text = ""] = ITEM.exec(line) ?? [];
        const first = idLines.get(id);
        if (id === "") {
          faults.push(`${where(index)} is not "- \`<id>\` (<state>): <text>": ${line}`);
        } else if (!ID.test(id)) {
          faults.push(
            `${where(index)} gives the id ${JSON.stringify(id)}, which holds a character other than a letter, a digit, ".", "_" and "-"`
          );
        } else if (first !== undefined) {
          faults.push(`${where(index)} gives the id ${id} of line ${first} again`);
        } else {
          idLines.set(id, index + 1);
          current = { id, state: state.trim(), text, list, line: index + 1 };
          items.push(current);
        }
        continue;
      }
      if (/^\s/.test(line) && count > 0) {
        if (current !== undefined) current.text = `${current.text} ${line.trim()}`;
        blank = false;
        continue;
      }
      if (blank || count === 0) break;
      faults.push(`${where(index)} follows an item, is not indented and is no item: ${line}`);
    }
    if (count === 0) {
      throw new Refused(
        `no item follows the line of --list ${JSON.stringify(list)} (line ${at + 1} of the body)`
      );
    }
  }
  if (faults.length > 0) throw new ItemFaults(faults);
  return items;
}

/** The prefix of item `id` that names its pull request's diff: its text before the first `-`. */
export function idPrefix(id: string): string {
  const at = id.indexOf("-");
  return at < 0 ? id : id.slice(0, at);
}

// The files and hunks of a diff.

/** A hunk of a diff: its `@@` line, and its lines after it as the diff gives them. */
export interface DiffHunk {
  readonly header: string;
  readonly lines: readonly string[];
}

/** A file of a diff: its path before and after the change (null before for a file the change
 *  adds, after for one it deletes), how the change touches it, and its hunks (none for a binary
 *  file, a mode change or a rename with no change of text). */
export interface DiffFile {
  readonly before: string | null;
  readonly after: string | null;
  readonly status: "modified" | "added" | "deleted" | "renamed" | "copied";
  readonly hunks: readonly DiffHunk[];
}

const HUNK_HEADER = /^@@ -\d+(?:,(\d+))? \+\d+(?:,(\d+))? @@/;
const GIT_LINE = "diff --git ";

/** A path of a `diff --git`, `---`, `+++`, `rename` or `copy` line: unquoted where git quoted it,
 *  less its `a/` or `b/` prefix where given, and less what follows a tab. */
function diffPath(text: string, prefix?: string): string {
  const plain = text.startsWith('"') ? (JSON.parse(text) as string) : (text.split("\t")[0] ?? "");
  return prefix !== undefined && plain.startsWith(prefix) ? plain.slice(prefix.length) : plain;
}

/** The two paths of a `diff --git a/<path> b/<path>` line whose paths are one, or undefined. */
function samePaths(line: string): string | undefined {
  const rest = line.slice(GIT_LINE.length);
  const length = (rest.length - "a/ b/".length) / 2;
  const name = rest.slice("a/".length, "a/".length + length);
  return rest === `a/${name} b/${name}` ? name : undefined;
}

/**
 * The files of the diff `text`, `gh pr diff`'s output: each from its `diff --git` line, with its
 * extended header lines (`new file mode`, `deleted file mode`, `rename from` and `rename to`,
 * `copy from` and `copy to`, `---` and `+++`) and its hunks, each hunk's lines as many as its `@@`
 * line counts, with any `\ No newline at end of file` line. An empty line in a hunk is read as a
 * line that stays. Throws naming the line: text before the first `diff --git`, a `@@` line it
 * cannot read, a hunk line that starts with none of ` `, `-`, `+` and `\`, a hunk with fewer or
 * more lines than its `@@` line counts, a line after a hunk that starts no hunk and no file, a
 * file whose paths it cannot read, or a diff with no file.
 */
export function splitDiff(text: string): DiffFile[] {
  const lines = text.split("\n");
  if (lines.at(-1) === "") lines.pop();
  const files: DiffFile[] = [];
  let index = 0;
  while (index < lines.length) {
    const gitLine = lines[index] ?? "";
    if (!gitLine.startsWith(GIT_LINE)) {
      throw new Error(`line ${index + 1} of the diff starts no file: ${gitLine}`);
    }
    const fileLine = index + 1;
    index += 1;
    let before: string | null | undefined;
    let after: string | null | undefined;
    let status: DiffFile["status"] = "modified";
    for (; index < lines.length; index += 1) {
      const line = lines[index] ?? "";
      if (line.startsWith(GIT_LINE) || line.startsWith("@@ ")) break;
      if (line.startsWith("new file mode ")) status = "added";
      else if (line.startsWith("deleted file mode ")) status = "deleted";
      else if (line.startsWith("rename from ")) {
        status = "renamed";
        before = diffPath(line.slice("rename from ".length));
      } else if (line.startsWith("rename to ")) after = diffPath(line.slice("rename to ".length));
      else if (line.startsWith("copy from ")) {
        status = "copied";
        before = diffPath(line.slice("copy from ".length));
      } else if (line.startsWith("copy to ")) after = diffPath(line.slice("copy to ".length));
      else if (line.startsWith("--- ")) {
        before = line === "--- /dev/null" ? null : diffPath(line.slice(4), "a/");
      } else if (line.startsWith("+++ ")) {
        after = line === "+++ /dev/null" ? null : diffPath(line.slice(4), "b/");
      }
    }
    const hunks: DiffHunk[] = [];
    while (index < lines.length && (lines[index] ?? "").startsWith("@@ ")) {
      const header = lines[index] ?? "";
      const counts = HUNK_HEADER.exec(header);
      if (counts === null)
        throw new Error(`line ${index + 1} of the diff is no @@ line: ${header}`);
      const headerLine = index + 1;
      let old = Number(counts[1] ?? 1);
      let fresh = Number(counts[2] ?? 1);
      const body: string[] = [];
      index += 1;
      while (old > 0 || fresh > 0 || (lines[index] ?? "").startsWith("\\")) {
        if (index >= lines.length) {
          throw new Error(
            `the hunk at line ${headerLine} of the diff ends before the lines its @@ line counts`
          );
        }
        const line = lines[index] ?? "";
        const mark = line[0] ?? " ";
        if (mark === " ") {
          old -= 1;
          fresh -= 1;
        } else if (mark === "-") old -= 1;
        else if (mark === "+") fresh -= 1;
        else if (mark !== "\\") {
          throw new Error(
            `line ${index + 1} of the diff, in the hunk at line ${headerLine}, starts with none of " ", "-", "+" and "\\": ${line}`
          );
        }
        if (old < 0 || fresh < 0) {
          throw new Error(
            `the hunk at line ${headerLine} of the diff holds more lines than its @@ line counts`
          );
        }
        body.push(line);
        index += 1;
      }
      hunks.push({ header, lines: body });
    }
    if (index < lines.length && !(lines[index] ?? "").startsWith(GIT_LINE)) {
      throw new Error(
        `line ${index + 1} of the diff follows a hunk and starts no hunk and no file: ${lines[index]}`
      );
    }
    const same = samePaths(gitLine);
    if (status === "added") before = null;
    if (status === "deleted") after = null;
    before = before === undefined ? same : before;
    after = after === undefined ? same : after;
    if (before === undefined || after === undefined || (before === null && after === null)) {
      throw new Error(
        `the file at line ${fileLine} of the diff has paths it cannot read: ${gitLine}`
      );
    }
    files.push({ before, after, status, hunks });
  }
  if (files.length === 0) throw new Error("the diff holds no file");
  return files;
}

// The run.

/** What the proposal reader takes of a craft entry of the findings file (cli.ts's `craftRead`
 *  writes it): its skill's directory and source, and its gaps, or null where every reply was
 *  refused. An entry is a skill's when its source is the skills root's repository and its
 *  directory's name is the skill's, as `craft-coverage` matches them. */
export const FindingsEntry = z.looseObject({
  entry: z.literal("craft"),
  skill: z.looseObject({ dir: z.string().min(1), source: z.string().nullable() }),
  answer: z.looseObject({ gaps: z.array(CraftGap) }).nullable(),
});
export type FindingsEntry = z.infer<typeof FindingsEntry>;

/** A file read, which the entry names: its path, the words its flag gives it, and its SHA-256. */
interface Source {
  readonly file: string;
  readonly source: string;
  readonly sha256: string;
}

/** A pull request's diff: the id prefix its `--pr-diff` gives it, its file, its SHA-256, its whole
 *  text, and its files (`splitDiff`). */
export interface PullDiff {
  readonly pr: string;
  readonly file: string;
  readonly sha256: string;
  readonly text: string;
  readonly files: readonly DiffFile[];
}

/** What one run reads, all of it before any call. */
export interface ProposalsRun {
  readonly items: readonly ProposalItem[];
  readonly body: Source;
  /** Each pull request's diff, by its id prefix. */
  readonly diffs: ReadonlyMap<string, PullDiff>;
  /** Each skills root: its repository (the words its `--skills-root` gives it, which a craft
   *  entry's source matches) and its directory, with the name of each `<name>/SKILL.md` in it. */
  readonly skillsRoots: readonly {
    readonly repo: string;
    readonly dir: string;
    readonly names: readonly string[];
  }[];
  /** The tree of the repository a `dotfiles` target's path is in. */
  readonly repoRoot: { readonly repo: string; readonly dir: string };
  readonly style: Source & { readonly rules: readonly StyleRule[] };
  readonly craft: Source & { readonly list: CraftList };
  readonly findings: {
    readonly file: string;
    readonly sha256: string;
    readonly entries: readonly { readonly line: number; readonly entry: FindingsEntry }[];
  };
  readonly out: string;
}

/** What `runProposals` writes to. */
export interface ProposalsIo {
  out(text: string): void;
  err(line: string): void;
}

/** The SHA-256 of `text`, in hex. */
function sha256(text: string): string {
  return createHash("sha256").update(text).digest("hex");
}

/** The file of item `id`'s entry under `out`. */
export function entryFile(out: string, id: string): string {
  return path.join(out, `${id}.json`);
}

/** The items of `items` that have no entry under `out` yet, which a run reads; an item with an
 *  entry is not read again. Throws a refusal naming an entry that is no JSON, or that is of
 *  another state or text of its item than the body's, before any call. */
export function pendingItems(items: readonly ProposalItem[], out: string): ProposalItem[] {
  return items.filter((item) => {
    const file = entryFile(out, item.id);
    if (!existsSync(file)) return true;
    let entry: unknown;
    try {
      entry = JSON.parse(readFileSync(file, "utf8"));
    } catch {
      throw new Refused(
        `the entry ${file} is not JSON: move it away to read item ${item.id} again`
      );
    }
    const same = z
      .looseObject({
        id: z.literal(item.id),
        state: z.literal(item.state),
        text: z.literal(item.text),
      })
      .safeParse(entry);
    if (!same.success) {
      throw new Refused(
        `the entry ${file} is of another state or text of item ${item.id} than the body's: move it away to read the item again`
      );
    }
    return false;
  });
}

/** The gateways of a run: one for each reader, so the request log names each call's reader. */
export interface ProposalsGateways {
  readonly target: Gateway;
  readonly proposal: Gateway;
}

/** Writes reader `name`'s refusals of item `id` to `<out>/<id>.refused.json`, and says so. */
function writeRefused(run: ProposalsRun, id: string, error: ReaderFailed, io: ProposalsIo): void {
  const file = path.join(run.out, `${id}.refused.json`);
  writeWhole(
    file,
    json({
      id,
      reader: error.reader,
      refusals: error.refusals,
      spent: error.spent,
      usd: usdOf(error.spent),
    })
  );
  io.err(`proposals: ${id}: ${error.message}; no entry; written to ${file}`);
}

/** Reads `item` with both readers and writes its entry and the user messages sent; returns
 *  whether it has an entry. A reader whose every reply was refused leaves it none, and writes its
 *  refusals beside. */
async function readItem(
  run: ProposalsRun,
  item: ProposalItem,
  shared: {
    readonly targetInputs: ReadonlyMap<string, ProposalTargetInput["diff"]>;
    /** Each diff's files by path (readers.ts's `diffFilesByPath`), by its prefix. */
    readonly files: ReadonlyMap<string, ReadonlyMap<string, DiffFile>>;
  },
  gateways: ProposalsGateways,
  io: ProposalsIo
): Promise<boolean> {
  const diff = run.diffs.get(idPrefix(item.id));
  const diffInput = shared.targetInputs.get(idPrefix(item.id));
  const files = shared.files.get(idPrefix(item.id));
  if (diff === undefined || diffInput === undefined || files === undefined) {
    throw new Error(`item ${item.id} has no diff of its prefix ${idPrefix(item.id)}`);
  }
  const targetInput: ProposalTargetInput = {
    diff: diffInput,
    item: { id: item.id, state: item.state, text: item.text },
    repository: run.repoRoot.repo,
    skills: run.skillsRoots.flatMap((root) =>
      root.names.map((name) => ({ repo: root.repo, name }))
    ),
  };
  const input = path.join(run.out, `${item.id}.input.md`);
  const message = (name: "proposal-target" | "proposal", value: unknown) =>
    `<!-- the user message of the ${name} request -->\n${userParts(name, value)
      .map((part) => part.text)
      .join("\n\n")}\n`;
  writeWhole(input, message("proposal-target", targetInput));
  let target: ReaderResult;
  try {
    target = await runReader("proposal-target", targetInput, gateways.target);
  } catch (error) {
    if (!(error instanceof ReaderFailed)) throw error;
    writeRefused(run, item.id, error, io);
    return false;
  }
  // runReader parsed it with the proposal-target reader's answer schema.
  const targetAnswer = target.answer as ProposalTargetAnswer;
  const targetUsd = usdOf(target.spent);
  const hunks = targetAnswer.hunks.map(({ file, hunk }) => {
    const changed = files.get(file);
    const at = changed?.hunks[hunk];
    if (changed === undefined || at === undefined) throw new Error(`no hunk ${hunk} of ${file}`);
    return {
      file: changed.after ?? changed.before ?? file,
      hunk,
      header: at.header,
      lines: at.lines,
    };
  });
  const rules = rulesInput(run.style.rules, run.craft.list);
  const base = {
    entry: "proposal",
    id: item.id,
    state: item.state,
    text: item.text,
    list: item.list,
    line: item.line,
    body: run.body,
    diff: { pr: diff.pr, file: diff.file, sha256: diff.sha256 },
    style: {
      file: run.style.file,
      source: run.style.source,
      sha256: run.style.sha256,
      rules: run.style.rules.length,
    },
    craft: {
      file: run.craft.file,
      source: run.craft.source,
      sha256: run.craft.sha256,
      readerProperties: rules.properties.map((property) => property.id),
    },
    findings: { file: run.findings.file, sha256: run.findings.sha256 },
    input,
    target: {
      reader: { prompt: "proposal-target", settings: loadPrompt("proposal-target").settings },
      answer: targetAnswer,
      refusals: target.refusals,
      spent: target.spent,
      usd: targetUsd,
    },
    hunks: hunks.map(({ file, hunk, header }) => ({ file, hunk, header })),
  };
  const found = targetAnswer.targets.flatMap((each) => (each.kind === "none" ? [] : [each]));
  if (found.length === 0) {
    writeWhole(
      entryFile(run.out, item.id),
      json({
        ...base,
        targets: [],
        noTarget: { bodyWords: item.text, why: targetAnswer.targets.map((each) => each.why) },
        reader: null,
        answer: null,
        refusals: [],
        spent: [],
        usd: targetUsd,
      })
    );
    io.out(`proposals: ${item.id}: no target found; $${targetUsd.toFixed(4)} at list price\n`);
    return true;
  }
  const targets: unknown[] = [];
  const texts: ProposalInput["targets"] = [];
  const findings: ProposalInput["findings"] = [];
  for (const each of found) {
    if (each.kind === "dotfiles") {
      const file = path.join(run.repoRoot.dir, each.path);
      const text = statSync(file, { throwIfNoEntry: false })?.isFile()
        ? readFileSync(file, "utf8")
        : null;
      const repo = run.repoRoot.repo;
      targets.push({
        kind: "dotfiles",
        repo,
        path: each.path,
        file,
        sha256: text === null ? null : sha256(text),
        why: each.why,
      });
      texts.push({ kind: "dotfiles", repo, path: each.path, text });
      continue;
    }
    const { repo, name } = each.skill;
    const root = run.skillsRoots.find((candidate) => candidate.repo === repo);
    if (root === undefined) throw new Error(`no skills root is of ${repo}`);
    const dir = path.join(root.dir, name);
    const files = skillFiles(dir);
    const entry = run.findings.entries.findLast(
      ({ entry }) => entry.skill.source === repo && path.basename(entry.skill.dir) === name
    );
    targets.push({
      kind: "skill",
      repo,
      name,
      dir,
      files: files.map((file) => ({ path: file.path, sha256: sha256(file.text) })),
      findings:
        entry === undefined
          ? null
          : { line: entry.line, gaps: entry.entry.answer?.gaps.length ?? null },
      why: each.why,
    });
    texts.push({ kind: "skill", repo, name, files });
    if (entry?.entry.answer) findings.push({ repo, name, gaps: entry.entry.answer.gaps });
  }
  const proposalInput: ProposalInput = {
    item: targetInput.item,
    hunks: hunks.map(({ file, hunk, header, lines }) => ({
      file,
      hunk,
      header,
      lines: [...lines],
    })),
    targets: texts,
    findings,
    ...rules,
  };
  writeWhole(
    input,
    `${message("proposal-target", targetInput)}\n${message("proposal", proposalInput)}`
  );
  let proposal: ReaderResult;
  try {
    proposal = await runReader("proposal", proposalInput, gateways.proposal);
  } catch (error) {
    if (!(error instanceof ReaderFailed)) throw error;
    writeRefused(run, item.id, error, io);
    return false;
  }
  // runReader parsed it with the proposal reader's answer schema.
  const answer = proposal.answer as ProposalAnswer;
  const usd = targetUsd + usdOf(proposal.spent);
  writeWhole(
    entryFile(run.out, item.id),
    json({
      ...base,
      targets,
      noTarget: null,
      reader: { prompt: "proposal", settings: loadPrompt("proposal").settings },
      answer,
      refusals: proposal.refusals,
      spent: proposal.spent,
      usd,
    })
  );
  io.out(
    `proposals: ${item.id}: fixes ${answer.fixes.length}, makes ${answer.makes.length}; $${usd.toFixed(4)} at list price\n`
  );
  return true;
}

/** Waits for each of `reads`, then throws the first rejection, a spent budget before any other. */
async function settle(reads: readonly Promise<boolean>[]): Promise<void> {
  const failed = (await Promise.allSettled(reads)).flatMap((each) =>
    each.status === "rejected" ? [each.reason as unknown] : []
  );
  if (failed.length > 0) throw failed.find((each) => each instanceof BudgetSpent) ?? failed[0];
}

/**
 * Reads each item of `pending` (`pendingItems`) with both readers and writes its entry to
 * `<out>/<id>.json` and the user messages sent to `<out>/<id>.input.md`. The first item of each
 * pull request goes alone, so that the others of it read its diff from the prompt cache; then the
 * rest, as many at once as the gateway lets. Returns the ids of `run`'s items that have no entry
 * at the end. A send that rejects (the gateway's BudgetSpent among them) rejects, once the reads
 * in flight have ended.
 */
export async function runProposals(
  run: ProposalsRun,
  pending: readonly ProposalItem[],
  gateways: ProposalsGateways,
  io: ProposalsIo
): Promise<string[]> {
  const targetInputs = new Map(
    [...run.diffs].map(([prefix, diff]) => [
      prefix,
      {
        pr: diff.pr,
        text: diff.text,
        files: diff.files.map(({ before, after, hunks }) => ({
          before,
          after,
          hunks: hunks.map((hunk) => hunk.header),
        })),
      },
    ])
  );
  const files = new Map(
    [...run.diffs].map(([prefix, diff]) => [prefix, diffFilesByPath(diff.files)])
  );
  const firsts = new Map<string, ProposalItem>();
  for (const item of pending) {
    if (!firsts.has(idPrefix(item.id))) firsts.set(idPrefix(item.id), item);
  }
  const first = new Set(firsts.values());
  const read = (item: ProposalItem) => readItem(run, item, { targetInputs, files }, gateways, io);
  await settle([...first].map(read));
  await settle(pending.filter((item) => !first.has(item)).map(read));
  return run.items.flatMap((item) => (existsSync(entryFile(run.out, item.id)) ? [] : [item.id]));
}
