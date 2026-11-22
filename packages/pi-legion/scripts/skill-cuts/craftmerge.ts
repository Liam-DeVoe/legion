/**
 * The merge of fresh readings of the craft reader on one skill's same inputs (README.md,
 * "craft-merge"):
 *
 *   bun scripts/skill-cuts/cli.ts craft-merge --entry <entry.json> --entry <entry.json>
 *     [--entry <entry.json>]... --out <merged.json>
 *
 * Each `--entry` is an entry `craft` wrote (cli.ts's `craftRead`). The craft reader's answer
 * varies between fresh requests of the same input, so a skill's findings come from several
 * readings: the merged entry lists each gap that any reading lists, with the count of readings
 * that list it. Two gaps are one gap when they have the same file, line and rule; their kinds may
 * differ, and each reading's kind is kept. It calls no model and reads nothing but the entries.
 *
 * Refusals (exit 2): the arguments (fewer than two `--entry`); an entry it cannot read, that is
 * no JSON or no craft entry; one reading given twice (`givenTwice`: two `--entry` of one file, or
 * of the same bytes), naming both, since it would count each of its gaps as found by two
 * readings; an entry whose inputs differ from the first entry's (`inputFaults`), naming each
 * difference; and an entry whose answer is null (every reply refused).
 *
 * It writes `--out` whole (`merge`) and prints one line: the skill, its merged gaps, how many of
 * them are in n of n readings, n-1 of n, … 1 of n, and the spend.
 */
import { createHash } from "node:crypto";
import { readFileSync, realpathSync } from "node:fs";
import path from "node:path";
import { messageFor } from "@legion/envoy-client/errors";
import { z } from "zod";
import { json, parse, Refused, required, schemaFaults, writeWhole } from "./common";
import { CraftAnswer, type CraftGap } from "./readers";

const USAGE =
  "usage: cli.ts craft-merge --entry <entry.json> --entry <entry.json> [--entry <entry.json>]... --out <merged.json>";

/** A file as a craft entry names it: its path inside its skill and the SHA-256 of its text. */
const FileDigest = z.looseObject({ path: z.string().min(1), sha256: z.string().min(1) });
type FileDigest = z.infer<typeof FileDigest>;

/** What craft-merge reads of a craft entry (cli.ts's `craftRead`): the inputs it compares, the
 *  reader's settings, the answer (null when every reply was refused) and the spend. */
const CraftEntry = z.looseObject({
  entry: z.literal("craft"),
  skill: z.looseObject({
    name: z.string().min(1),
    dir: z.string().min(1),
    files: z.array(FileDigest),
  }),
  style: z.looseObject({ sha256: z.string().min(1) }),
  craft: z.looseObject({ sha256: z.string().min(1) }),
  nearest: z.array(z.looseObject({ name: z.string().min(1), files: z.array(FileDigest) })),
  reader: z.looseObject({}),
  answer: CraftAnswer.nullable(),
  usd: z.number().nonnegative(),
});
type CraftEntry = z.infer<typeof CraftEntry>;

/** One `--entry`: its file as given and its real path (every link and `..` resolved), the SHA-256
 *  of its bytes, its JSON value as the file holds it (the merged entry copies the first entry's
 *  inputs from it, each field as it was written), and what CraftEntry reads of it. */
interface Reading {
  readonly file: string;
  readonly realPath: string;
  readonly sha256: string;
  readonly value: Readonly<Record<string, unknown>>;
  readonly entry: CraftEntry;
}

/** The entry in `file`. Throws a refusal naming the file when it cannot be read, or is no JSON or
 *  no craft entry. */
function readEntry(file: string): Reading {
  let bytes: Buffer;
  try {
    bytes = readFileSync(file);
  } catch (error) {
    throw new Refused(`cannot read the entry ${file}: ${messageFor(error)}`);
  }
  let value: unknown;
  try {
    value = JSON.parse(bytes.toString("utf8"));
  } catch {
    throw new Refused(`the entry ${file} is not JSON`);
  }
  const entry = CraftEntry.safeParse(value);
  if (!entry.success) {
    throw new Refused(`the entry ${file} is no craft entry: ${schemaFaults(entry.error)}`);
  }
  return {
    file,
    realPath: realpathSync(file),
    sha256: createHash("sha256").update(bytes).digest("hex"),
    // CraftEntry read it as an object.
    value: value as Record<string, unknown>,
    entry: entry.data,
  };
}

/** Throws a refusal naming the first two of `readings` that are one reading given twice: one
 *  file (the same real path), or two files of the same bytes (the same SHA-256). */
function givenTwice(readings: readonly Reading[]): void {
  const byPath = new Map<string, Reading>();
  const bySha256 = new Map<string, Reading>();
  for (const reading of readings) {
    const samePath = byPath.get(reading.realPath);
    const sameBytes = bySha256.get(reading.sha256);
    const earlier = samePath ?? sameBytes;
    if (earlier !== undefined) {
      const what =
        samePath !== undefined ? "the same file" : `the same content (sha256 ${reading.sha256})`;
      throw new Refused(
        `the entries ${earlier.file} and ${reading.file} are one reading given twice: ${what}`
      );
    }
    byPath.set(reading.realPath, reading);
    bySha256.set(reading.sha256, reading);
  }
}

/** How the files `files` differ from the first entry's files `first`, each in words; `what`
 *  names a file ("skill file", "nearest skill <name>'s file"). */
function fileFaults(
  what: string,
  first: readonly FileDigest[],
  files: readonly FileDigest[]
): string[] {
  const before = new Map(first.map((file) => [file.path, file.sha256]));
  const paths = new Set(files.map((file) => file.path));
  const faults: string[] = [];
  for (const file of files) {
    const sha256 = before.get(file.path);
    if (sha256 === undefined) {
      faults.push(`it has the ${what} ${file.path}, which the first has not`);
    } else if (sha256 !== file.sha256) {
      faults.push(`its ${what} ${file.path} has sha256 ${file.sha256}, not ${sha256}`);
    }
  }
  for (const file of first) {
    if (!paths.has(file.path)) faults.push(`it has no ${what} ${file.path}`);
  }
  return faults;
}

/** Each way the inputs of `entry` differ from those of the first entry `first`, in words: the
 *  name of the skill's directory, the skill's files and their SHA-256, the style text's SHA-256,
 *  the craft list's SHA-256, and the nearest skills, by name, with their files and SHA-256. */
function inputFaults(first: CraftEntry, entry: CraftEntry): string[] {
  const faults: string[] = [];
  const [dir, firstDir] = [path.basename(entry.skill.dir), path.basename(first.skill.dir)];
  if (dir !== firstDir) faults.push(`its skill directory is ${dir}, not ${firstDir}`);
  faults.push(...fileFaults("skill file", first.skill.files, entry.skill.files));
  if (entry.style.sha256 !== first.style.sha256) {
    faults.push(`its style text has sha256 ${entry.style.sha256}, not ${first.style.sha256}`);
  }
  if (entry.craft.sha256 !== first.craft.sha256) {
    faults.push(`its craft list has sha256 ${entry.craft.sha256}, not ${first.craft.sha256}`);
  }
  const nearest = new Map(first.nearest.map((skill) => [skill.name, skill.files]));
  for (const skill of entry.nearest) {
    const files = nearest.get(skill.name);
    if (files === undefined) {
      faults.push(`it reads the nearest skill ${skill.name}, which the first does not`);
    } else faults.push(...fileFaults(`nearest skill ${skill.name}'s file`, files, skill.files));
  }
  const names = new Set(entry.nearest.map((skill) => skill.name));
  for (const skill of first.nearest) {
    if (!names.has(skill.name)) faults.push(`it does not read the nearest skill ${skill.name}`);
  }
  return faults;
}

/** A gap of the merged entry: its file, line and rule, how many readings list it of how many,
 *  and the kind and why of each listing, in reading order. */
interface MergedGap {
  readonly file: string;
  readonly line: number;
  readonly rule: string;
  readings: number;
  readonly of: number;
  readonly kinds: CraftGap["kind"][];
  readonly why: string[];
}

/** A rule of the merged entry with how many readings judged it each verdict. */
interface JudgedCounts {
  readonly rule: string;
  gap: number;
  met: number;
  notApplicable: number;
}

/** The field of JudgedCounts that counts each verdict. */
const VERDICT_FIELD = { gap: "gap", met: "met", "not-applicable": "notApplicable" } as const;

/** -1, 0 or 1 as `a` sorts before, with or after `b` in code-unit order. */
const byText = (a: string, b: string) => (a < b ? -1 : a > b ? 1 : 0);

/** The number a rule id's digits make (10 for `S10`), 0 for none. */
const ruleNumber = (rule: string) => Number(rule.replace(/\D/g, "") || 0);

/** The merged entry of `readings`, each with its answer in `answers`: the first entry's inputs
 *  (skill, style, craft, nearest, reader), each gap any reading lists, merged by file, line and
 *  rule (sorted so, the line as a number and the rule as text), and each rule any reading judged
 *  with its count of each verdict (sorted by the rule's letter, then its number: C1, C3, S1, S2,
 *  S10). */
function merge(readings: readonly [Reading, ...Reading[]], answers: readonly CraftAnswer[]) {
  const of = readings.length;
  const gaps = new Map<string, MergedGap>();
  const judged = new Map<string, JudgedCounts>();
  for (const [index, answer] of answers.entries()) {
    const listed = new Set<string>();
    for (const { file, line, rule, kind, why } of answer.gaps) {
      const key = JSON.stringify([file, line, rule]);
      let gap = gaps.get(key);
      if (gap === undefined) {
        gap = { file, line, rule, readings: 0, of, kinds: [], why: [] };
        gaps.set(key, gap);
      }
      if (!listed.has(key)) {
        gap.readings++;
        listed.add(key);
      }
      gap.kinds.push(kind);
      gap.why.push(`reading ${index + 1}: ${why}`);
    }
    for (const { rule, verdict } of answer.judged) {
      let counts = judged.get(rule);
      if (counts === undefined) {
        counts = { rule, gap: 0, met: 0, notApplicable: 0 };
        judged.set(rule, counts);
      }
      counts[VERDICT_FIELD[verdict]]++;
    }
  }
  const { skill, style, craft, nearest, reader } = readings[0].value;
  const merged = [...gaps.values()].sort(
    (a, b) => byText(a.file, b.file) || a.line - b.line || byText(a.rule, b.rule)
  );
  return {
    entry: "craft-merged",
    ...{ skill, style, craft, nearest, reader },
    readings: of,
    entries: readings.map((reading) => ({ path: reading.file, sha256: reading.sha256 })),
    gaps: merged,
    judged: [...judged.values()].sort(
      (a, b) =>
        byText(a.rule.charAt(0), b.rule.charAt(0)) || ruleNumber(a.rule) - ruleNumber(b.rule)
    ),
    noGap: merged.length === 0,
    usd: readings.reduce((sum, reading) => sum + reading.entry.usd, 0),
  };
}

/** What `main` writes to. */
export interface CraftMergeIo {
  /** Writes `text` to standard output as it is. */
  out(text: string): void;
}

/** `skill-cuts craft-merge`. Returns the exit status: 0 merged, 2 refused (thrown as Refused,
 *  which cli.ts's `main` turns into 2). */
export function main(argv: readonly string[], io: CraftMergeIo): number {
  const values = parse(
    argv,
    { entry: { type: "string", multiple: true }, out: { type: "string" } },
    USAGE
  );
  const given = required(values, ["out"], USAGE);
  const files = values.entry ?? [];
  const [firstFile, ...moreFiles] = files;
  if (firstFile === undefined || moreFiles.length === 0) {
    throw new Refused(`craft-merge takes two --entry or more, not ${files.length}. ${USAGE}`);
  }
  const first = readEntry(firstFile);
  const readings: [Reading, ...Reading[]] = [first, ...moreFiles.map(readEntry)];
  givenTwice(readings);
  for (const reading of readings.slice(1)) {
    const faults = inputFaults(first.entry, reading.entry);
    if (faults.length > 0) {
      throw new Refused(
        `the entry ${reading.file} reads other inputs than the first, ${first.file}: ${faults.join("; ")}`
      );
    }
  }
  const answers = readings.map(({ file, entry }) => {
    if (entry.answer === null) {
      throw new Refused(`the entry ${file} has no answer: every reply was refused`);
    }
    return entry.answer;
  });
  const merged = merge(readings, answers);
  writeWhole(given.out, json(merged));
  const of = readings.length;
  const counts = Array.from({ length: of }, (_, at) => of - at).map(
    (count) => `${count} of ${of}: ${merged.gaps.filter((gap) => gap.readings === count).length}`
  );
  const gapWords =
    merged.gaps.length === 0
      ? `no gap in ${of} readings`
      : `${merged.gaps.length} gap${merged.gaps.length === 1 ? "" : "s"} in ${of} readings (in ${counts.join("; ")})`;
  io.out(
    `craft-merge: ${first.entry.skill.name}: ${gapWords}, $${merged.usd.toFixed(4)} at list price; written to ${given.out}\n`
  );
  return 0;
}
