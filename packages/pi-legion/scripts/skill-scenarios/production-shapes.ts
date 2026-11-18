// The shapes of production's answers to the reads the world answers, which a row holds a world's
// answer to: production-shapes.json, derived by this module's `derive` from the production reads
// its operator hands it and never written by hand. Each pinned read keeps the command it was read
// with, when it was read, its exit, its stderr and its answer's shape: every JSON path the answer
// holds and the kind of the value there, never the value. The answers are a public pull request's
// and repository's, so their bodies, logins and ids stay out of the tree; the commands are kept
// with their values written as placeholders (`<owner>`, `<repo>`, `<number>`, `<head>`, `<base>`,
// `<review>`, `<comment>`, `<path>`, `<id>`), and a derivation that leaves one of production's
// values in the file fails. stderr is gh's own line, and an error body's `message`,
// `documentation_url` and `status` are GitHub's own texts, which the tree keeps.
//
// A row compares a world's answer with its pin (`shapeDiff`): the paths production has that the
// world lacks, the paths the world has that production does not, and the paths whose kinds
// differ. An array the world holds empty is not compared below itself, and nor is a variant of a
// list (a ruleset's rule of one `type`) the world holds none of, since how many items a list holds
// is the world's state and no shape; an array production held empty has no item shape pinned, so
// an item the world holds there is a path production does not show.
//
// One read is no gh call: `legion read_record` (`RECORD_READ`), the issue record the `legion`
// tool's `read_record` answered in a Legion issue pod, which the daemon stand-in's record is held
// to (daemon-standin.test.ts). Its two maps keyed by a name of the issue's are pinned as lists
// (`rolesListed`), so the pin names no role and no pair of phases.
//
//   bun production-shapes.ts <ruleset reads dir> <production reads json> <read_record json> \
//     [<later reads json>...] <out json>
//
// writes the pin from the sets of production reads INFRA-764 made, then formats it as the
// repository's biome does: the reads of a branch a ruleset protects, the distinct reads of round
// 25, the issue record, and each later set of reads (`LaterReads`): the tester's reads of every
// read the world served with no pin (prod-reads-19.json, 2026-11-18) and the implementer's of the
// ones that set does not hold (implementer-reads.json, 2026-11-18).
import { readdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { z } from "zod";

/** The pin this directory keeps. */
export const PRODUCTION_SHAPES = path.join(import.meta.dir, "production-shapes.json");

/** An answer's shape: each JSON path it holds (`.` the answer itself, `.key` a field, `[]` an
 * array's items and `[type=x]` its items of one variant), in the order the answer first holds
 * it, and the kinds of JSON value there, `|`-joined in byte order where they differ. */
export type Shape = Record<string, string>;

const PinnedRead = z.object({
  name: z.string(),
  command: z.array(z.string()),
  read_at: z.string(),
  exit: z.number(),
  stderr: z.string(),
  shape: z.record(z.string(), z.string()),
});
/** One read production answered: its name, the command it was read with (its values written as
 * placeholders), when it was read, its exit and stderr, and its answer's shape. */
export type PinnedRead = z.infer<typeof PinnedRead>;
const Pin = z.object({ made_by: z.string(), reads: z.array(PinnedRead) });

let pinned: Map<string, PinnedRead> | undefined;
/** Every read the pin holds, by its name. */
function pin(): Map<string, PinnedRead> {
  pinned ??= new Map(
    Pin.parse(JSON.parse(readFileSync(PRODUCTION_SHAPES, "utf8"))).reads.map((read) => [
      read.name,
      read,
    ])
  );
  return pinned;
}
/** The read the pin names `name`. A name it does not hold throws, naming it. */
export function pinnedRead(name: string): PinnedRead {
  const read = pin().get(name);
  if (read === undefined) throw new Error(`${PRODUCTION_SHAPES} pins no read named ${name}`);
  return read;
}
/** Every read the pin holds, in its order: what a row that holds each read of a kind lists. */
export function pinnedReads(): PinnedRead[] {
  return [...pin().values()];
}

/** `text` with each placeholder (`<name>`) filled from `values`. A placeholder `values` does not
 * fill throws, naming `read`. */
function filled(read: PinnedRead, text: string, values: Readonly<Record<string, string>>): string {
  return text.replace(/<(\w+)>/g, (whole, name: string) => {
    const value = values[name];
    if (value === undefined) throw new Error(`${read.name}: no value for ${whole}`);
    return value;
  });
}

/** `read`'s command, after its `gh`, with each placeholder filled from `values`, so a row reads
 * the world with the very call production was read with. A placeholder `values` does not fill
 * throws. */
export function argvOf(read: PinnedRead, values: Readonly<Record<string, string>>): string[] {
  return read.command.slice(1).map((word) => filled(read, word, values));
}

/** `read`'s stderr with each placeholder filled from `values`: the line gh printed, naming the
 * world's repository, branch and the rest where production's named its own. */
export function stderrOf(read: PinnedRead, values: Readonly<Record<string, string>>): string {
  return filled(read, read.stderr, values);
}

const isObject = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

function kindOf(value: unknown): string {
  if (value === null) return "null";
  if (Array.isArray(value)) return "array";
  return typeof value;
}

/** The fields a list's items are told apart by, as GitHub names a rule's kind and GraphQL an
 * object's. */
const DISCRIMINATORS = ["type", "__typename"];
/** The field `list`, at `at`, groups its items by, or undefined to read them as one. */
type Grouping = (at: string, list: readonly unknown[]) => string | undefined;

const ROOT = ".";

function shapeInto(value: unknown, at: string, into: Shape, grouping: Grouping): void {
  const kinds = new Set(into[at]?.split("|"));
  kinds.add(kindOf(value));
  into[at] = [...kinds].sort().join("|");
  if (Array.isArray(value)) {
    const field = grouping(at, value);
    for (const item of value) {
      const tag =
        field !== undefined && isObject(item) && typeof item[field] === "string"
          ? `[${field}=${item[field]}]`
          : "[]";
      shapeInto(item, `${at}${tag}`, into, grouping);
    }
  } else if (isObject(value))
    for (const [key, inner] of Object.entries(value))
      shapeInto(inner, `${at === ROOT ? "" : at}.${key}`, into, grouping);
}

/** A list's items are read per variant where each names one (`DISCRIMINATORS`) and they do not
 * all have one shape: a ruleset's `deletion` rule holds a `type` alone, its `pull_request` rule
 * the parameters of a review. */
const byVariant: Grouping = (_, list) =>
  DISCRIMINATORS.find(
    (field) =>
      list.every((item) => isObject(item) && typeof item[field] === "string") &&
      new Set(list.map((item) => JSON.stringify(Object.entries(shapeOf(item)).sort()))).size > 1
  );

/** The shape of `value`, as the pin holds production's. */
export function shapeOf(value: unknown): Shape {
  const into: Shape = {};
  shapeInto(value, ROOT, into, byVariant);
  return into;
}

/** A path of a variant: the list's path, the field and its value. */
const VARIANT = /^(.*)\[(\w+)=[^\]]*\]$/;
/** Whether `at` is a path below `above`. */
const under = (at: string, above: string) =>
  at !== above &&
  (above === ROOT || (at.startsWith(above) && /^[.[]/.test(at.slice(above.length))));

/** How a world's answer differs in shape from `read`'s: the paths production has that the world
 * lacks (`missing`), the paths the world has that production does not (`extra`), each the
 * highest of its kind, and each path whose kinds differ (`kinds`), which is compared no further
 * down. The world's lists are read per variant where production's are. An array the world holds
 * empty, and a variant of a list the world holds none of, is not compared below itself. */
export function shapeDiff(
  answer: unknown,
  read: Pick<PinnedRead, "shape">
): { missing: string[]; extra: string[]; kinds: string[] } {
  const given = read.shape;
  const fields = new Map<string, string>();
  for (const at of Object.keys(given)) {
    const [, list, field] = VARIANT.exec(at) ?? [];
    if (list !== undefined && field !== undefined) fields.set(list, field);
  }
  const world: Shape = {};
  shapeInto(answer, ROOT, world, (at) => fields.get(at));
  const paths = Object.keys(world);
  const kinds: string[] = [];
  const differing: string[] = [];
  for (const at of paths) {
    const production = given[at];
    const held = world[at] ?? "";
    if (production === undefined) continue;
    if (held.split("|").every((kind) => production.split("|").includes(kind))) continue;
    kinds.push(`${at}: ${production} in production, ${held} in the world`);
    differing.push(at);
  }
  const empty = paths.filter(
    (at) => world[at]?.split("|").includes("array") && !paths.some((other) => under(other, at))
  );
  const variantAbsent = (at: string) =>
    [...at.matchAll(/\[\w+=[^\]]*\]/g)].some(
      (found) => world[at.slice(0, (found.index ?? 0) + found[0].length)] === undefined
    );
  const highest = (found: string[]) =>
    found.filter((at) => !found.some((above) => under(at, above)));
  const compared = (at: string) => !differing.some((above) => under(at, above));
  return {
    missing: highest(
      Object.keys(given).filter(
        (at) =>
          world[at] === undefined &&
          compared(at) &&
          !empty.some((above) => under(at, above)) &&
          !variantAbsent(at)
      )
    ),
    extra: highest(paths.filter((at) => given[at] === undefined && compared(at))),
    kinds,
  };
}

/** The command each ruleset read was made with (`gh api <endpoint>`), by the name of its files. */
const RULESET_READS: Readonly<Record<string, string>> = {
  "rules-branches-main": "repos/<owner>/<repo>/rules/branches/main",
  rulesets: "repos/<owner>/<repo>/rulesets",
  "ruleset-<id>": "repos/<owner>/<repo>/rulesets/<id>",
  "branches-main": "repos/<owner>/<repo>/branches/main",
  "branches-main-protection": "repos/<owner>/<repo>/branches/main/protection",
};

/** `text` as the JSON it holds, or `text` itself where it holds none (a text answer). */
function parsed(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

/** The name the pin holds production's `read_record` under. */
export const RECORD_READ = "legion read_record";

/** An issue record with its two maps keyed by a name of the issue's read as lists of their
 * values: `workers`, keyed by role, and a row's `backwardMoves`, keyed by the pair of phases a
 * move went between. Which roles hold a row and which moves an issue has made are its state and
 * no shape, so a shape that named them would hold a world to the roles production's one issue
 * happened to have run; as lists, every row is read under one path and the kinds of each member
 * are joined over the rows. A list the world holds empty is compared no further down
 * (`shapeDiff`), so a record with no row at all passes this comparison: the row that holds a
 * world's record to the pin holds its roles by name as well. */
export function rolesListed(record: unknown): unknown {
  if (!isObject(record) || !isObject(record.workers)) return record;
  return {
    ...record,
    workers: Object.values(record.workers).map((row) =>
      isObject(row) && isObject(row.backwardMoves)
        ? { ...row, backwardMoves: Object.values(row.backwardMoves) }
        : row
    ),
  };
}

/** A production `read_record` as INFRA-764 kept it: a note naming when it was read, and the
 * record the tool answered. */
const RecordRead = z.object({ note: z.string(), record: z.unknown() });

/** A path of a record's shape whose every key is a word of the contract's: the root, or keys of
 * letters starting in lower case, each a list or not. */
const RECORD_PATH = /^\.$|^(\.[a-z][A-Za-z]*(\[\])*)+$/;

/** A later set of production reads, as INFRA-764 kept each: where it was read (`repo`), the pull
 * requests it read (`pull_requests`, by a name of the set's own), and each read with the item of
 * the unpinned list it reads (`item`), its name, the command it was run with, when, its exit, its
 * stderr and its whole answer, parsed where it is JSON. */
const LaterReads = z.object({
  source: z.string(),
  repo: z.string(),
  pull_requests: z.record(z.string(), z.string()).optional(),
  reads: z.array(
    z.object({
      item: z.string(),
      name: z.string(),
      command: z.array(z.string()),
      read_at: z.string(),
      exit: z.number(),
      stderr: z.string(),
      answer: z.unknown(),
    })
  ),
});

/** A commit's SHA as a command names one. */
const SHA = /\b[0-9a-f]{40}\b/g;

/** The reads of one later set (`LaterReads`), each with production's values written as
 * placeholders in its name, its command and its stderr — which, unlike round 25's, carries them:
 * a `gh run view` names the run's URL on the repository, and `gh pr checks` the pull request's
 * branch — and the values the set's leak check looks for. The values are read off the set itself:
 * the repository; each pull request's number; the two sides of a comparison its commands name,
 * `<base>` and `<head>`, and any other commit they name as `<head>`, of which one alone may be
 * named; each branch its answers name a pull request's head by (`headRefName`), `<branch>`, which
 * production's names with an issue key of its deployment; an App's slug a filter names
 * (`app/<app>`); and a file's path (`<path>`). */
function laterReads(file: string): {
  reads: PinnedRead[];
  values: string[];
  answers: unknown[];
} {
  const set = LaterReads.parse(JSON.parse(readFileSync(file, "utf8")));
  const [owner = "", repo = ""] = set.repo.split("/");
  const numbers = Object.values(set.pull_requests ?? {});
  const commands = set.reads.map(({ command }) => command.join(" ")).join("\n");
  const bases = new Set(
    [...commands.matchAll(/compare\/([0-9a-f]{40})\.\.\./g)].map((found) => found[1] ?? "")
  );
  const heads = new Set(
    [...commands.matchAll(SHA)].map(([sha]) => sha).filter((sha) => !bases.has(sha))
  );
  if (bases.size > 1 || heads.size > 1)
    throw new Error(
      `${file} names more than one base or head commit, which one placeholder cannot tell apart`
    );
  const branches = new Set(
    set.reads.flatMap(({ answer }) =>
      isObject(answer) && typeof answer.headRefName === "string" ? [answer.headRefName] : []
    )
  );
  const apps = new Set([...commands.matchAll(/\bapp\/([\w-]+)/g)].map((found) => found[1] ?? ""));
  const placeholders = (text: string) => {
    let written = text
      .replaceAll(`${owner}/${repo}`, "<owner>/<repo>")
      .replaceAll(`"${owner}"`, '"<owner>"')
      .replaceAll(`"${repo}"`, '"<repo>"')
      .replace(new RegExp(`^(\\w+)=${owner}$`), "$1=<owner>")
      .replace(new RegExp(`^(\\w+)=${repo}$`), "$1=<repo>")
      .replace(/\/contents\/[^?\s]+/, "/contents/<path>")
      .replace(/\bapp\/[\w-]+/g, "app/<app>");
    for (const sha of bases) written = written.replaceAll(sha, "<base>");
    for (const sha of heads) written = written.replaceAll(sha, "<head>");
    for (const branch of branches) written = written.replaceAll(branch, "<branch>");
    for (const number of numbers)
      written = written.replace(new RegExp(`(?<![\\w])${number}(?![\\w])`, "g"), "<number>");
    return written;
  };
  return {
    reads: set.reads.map((read) => ({
      name: placeholders(read.name),
      command: read.command.map(placeholders),
      read_at: read.read_at,
      exit: read.exit,
      stderr: placeholders(read.stderr),
      shape: shapeOf(read.answer),
    })),
    values: [owner, ...bases, ...heads, ...branches, ...apps],
    answers: set.reads.map(({ answer }) => answer),
  };
}

/** The pin of the sets of production reads: `rulesetDir`, one `NAME.json`, `NAME.err` and
 * `NAME.exit` per read of a branch a ruleset alone protects, with its README naming when they were
 * read; `readsFile`, every distinct read of a pull request INFRA-764's round 25 made, with its
 * `substitutions` naming the production values its commands carry and its README when it was read;
 * each of `laterFiles`, a later set of reads (`laterReads`), each read with the time it was made;
 * and, where one is given, `recordFile`, one `read_record` of a Legion issue with a note naming
 * when it was read, pinned last under `RECORD_READ` by its shape alone (`rolesListed`). Throws
 * where a value of production would be left in the pin. */
export function derive(
  rulesetDir: string,
  readsFile: string,
  recordFile?: string,
  laterFiles: readonly string[] = []
): z.infer<typeof Pin> {
  const readme = (dir: string) => readFileSync(path.join(dir, "README.txt"), "utf8");
  const rulesetAt = /read at (\d{4}-\d\d-\d\dT[\d:]+Z)/.exec(readme(rulesetDir))?.[1];
  const readsAt = /\(read (\d{4}-\d\d-\d\d about [\d:]+Z)/.exec(
    readme(path.dirname(readsFile))
  )?.[1];
  if (rulesetAt === undefined || readsAt === undefined)
    throw new Error("a README names no time its reads were made");
  const answers: unknown[] = [];
  const file = (name: string, ext: string) =>
    readFileSync(path.join(rulesetDir, `${name}.${ext}`), "utf8");
  const names = readdirSync(rulesetDir)
    .filter((entry) => entry.endsWith(".json"))
    .map((entry) => entry.slice(0, -".json".length))
    .sort();
  const ids = z
    .array(z.object({ id: z.number() }))
    .parse(parsed(file("rulesets", "json")))
    .map(({ id }) => String(id));
  const rulesetReads = names.map((name) => {
    const named = name.replace(/^ruleset-(\d+)$/, (whole, id: string) =>
      ids.includes(id) ? "ruleset-<id>" : whole
    );
    const endpoint = RULESET_READS[named];
    if (endpoint === undefined) throw new Error(`${rulesetDir}: no command is known for ${name}`);
    const answer = parsed(file(name, "json"));
    answers.push(answer);
    return {
      name: named,
      command: ["gh", "api", endpoint],
      read_at: rulesetAt,
      exit: Number(file(name, "exit").trim()),
      stderr: file(name, "err"),
      shape: shapeOf(answer),
    };
  });
  const Reads = z.object({
    substitutions: z.object({
      repo: z.string(),
      pr: z.string(),
      head: z.string(),
      base: z.string(),
      review: z.string(),
      comment: z.string(),
    }),
    reads: z.array(
      z.object({
        kind: z.string(),
        key: z.string(),
        command: z.array(z.string()),
        exit: z.number(),
        stderr: z.string(),
        answer: z.unknown(),
      })
    ),
  });
  const source = Reads.parse(JSON.parse(readFileSync(readsFile, "utf8")));
  const { substitutions: given } = source;
  // `<world> -> <production>` for the two the world renames, the production value for the rest.
  const production = (text: string) => text.split(" -> ").at(-1) ?? text;
  const [owner = "", repo = ""] = production(given.repo).split("/");
  const number = production(given.pr);
  const placeholders = (word: string) =>
    word
      .replaceAll(given.head, "<head>")
      .replaceAll(given.base, "<base>")
      .replaceAll(given.review, "<review>")
      .replaceAll(given.comment, "<comment>")
      .replaceAll(`${owner}/${repo}`, "<owner>/<repo>")
      .replaceAll(`"${owner}"`, '"<owner>"')
      .replaceAll(`"${repo}"`, '"<repo>"')
      .replace(new RegExp(`^(\\w+)=${owner}$`), "$1=<owner>")
      .replace(new RegExp(`^(\\w+)=${repo}$`), "$1=<repo>")
      .replace(new RegExp(`(?<![\\w])${number}(?![\\w])`, "g"), "<number>")
      .replace(/\/contents\/[^?]+/, "/contents/<path>");
  const prReads = source.reads.map((read) => {
    answers.push(read.answer);
    return {
      name: `${read.kind} ${read.key}`,
      command: read.command.map(placeholders),
      read_at: readsAt,
      exit: read.exit,
      stderr: read.stderr,
      shape: shapeOf(read.answer),
    };
  });
  const later = laterFiles.map(laterReads);
  for (const set of later) answers.push(...set.answers);
  const reads = [...rulesetReads, ...prReads, ...later.flatMap((set) => set.reads)];
  if (recordFile !== undefined) {
    const { note, record } = RecordRead.parse(JSON.parse(readFileSync(recordFile, "utf8")));
    const readAt = /read (\d{4}-\d\d-\d\dT[\d:]+Z)/.exec(note)?.[1];
    if (readAt === undefined) throw new Error(`${recordFile}'s note names no time it was read`);
    const shape = shapeOf(rolesListed(record));
    // The record's values can reach the pin through its shape alone, whose paths are its keys:
    // a map keyed by a value of the issue's (an issue key, a session, a pair of phases) would
    // write that value there. Every key the daemon's contract gives the record is one
    // camel-cased word of letters (all 112 paths of the read pinned on 2026-11-17 are), so a
    // path holding anything else holds a value, and is refused by name.
    const valued = Object.keys(shape).filter((at) => !RECORD_PATH.test(at));
    if (valued.length > 0)
      throw new Error(`the pin would hold production's values: ${valued.slice(0, 5).join(", ")}`);
    reads.push({
      name: RECORD_READ,
      // The tool and its op as one word, since it is no argv a row runs: the tool's own name,
      // alone, is the name of production's repository, a value the gh reads' answers hold and
      // the check below refuses.
      command: [RECORD_READ],
      read_at: readAt,
      exit: 0,
      stderr: "",
      shape,
    });
  }
  const unique = new Set(reads.map(({ name }) => name));
  if (unique.size !== reads.length) throw new Error("two reads share a name");
  const pin = {
    made_by:
      "production-shapes.ts (bun production-shapes.ts <ruleset reads dir> <production reads json> <read_record json> [<later reads json>...] <out json>), from INFRA-764's production reads of a branch a ruleset alone protects, of every distinct read round 25's runs made, of one issue's record as the legion tool's read_record answers it, and of each read the world served with no pin, each read from a Legion issue pod with the role App token its gh gives",
    reads,
  };
  // gh's own words — the group and the command a call names (`gh auth status`, `gh pr view`) and
  // a `--json` list of its fields — are never a value of production's, while an answer can hold
  // one of them as a value (round 25's check runs name GitHub Actions' App, whose `events` list
  // `milestone` and `status`), so they are left out of what the check reads. A `gh api` call's
  // endpoint is no word of gh's, and stays.
  const text = JSON.stringify({
    ...pin,
    reads: reads.map((read) => ({
      ...read,
      command: read.command.map((word, at) =>
        read.command[at - 1] === "--json" || (read.command[1] !== "api" && at <= 2) ? "" : word
      ),
    })),
  });
  const leaked = [
    owner,
    given.head,
    given.base,
    given.review,
    given.comment,
    ...ids,
    ...later.flatMap((set) => set.values),
    ...answers.flatMap(leaves),
  ].filter((value) => value !== "" && text.includes(value));
  if (leaked.length > 0)
    throw new Error(`the pin would hold production's values: ${leaked.slice(0, 5).join(", ")}`);
  return pin;
}

/** Every value of `answer` that would be production's own in the pin, as it would be written
 * there: a string of six characters or more, quoted, other than an error body's GitHub texts
 * (`message`, `documentation_url`, `status`) and a variant's name; and a number of five digits or
 * more, an id. */
function leaves(answer: unknown): string[] {
  if (typeof answer === "string") return answer.length >= 6 ? [JSON.stringify(answer)] : [];
  if (typeof answer === "number") return Math.abs(answer) >= 10_000 ? [String(answer)] : [];
  if (Array.isArray(answer)) return answer.flatMap(leaves);
  if (!isObject(answer)) return [];
  return Object.entries(answer).flatMap(([key, value]) =>
    DISCRIMINATORS.includes(key) ||
    ("message" in answer && ["message", "documentation_url", "status"].includes(key))
      ? []
      : leaves(value)
  );
}

if (import.meta.main) {
  const args = Bun.argv.slice(2);
  const [rulesetDir, readsFile, recordFile] = args;
  const out = args.at(-1);
  if (
    rulesetDir === undefined ||
    readsFile === undefined ||
    recordFile === undefined ||
    out === undefined ||
    args.length < 4
  ) {
    console.error(
      "usage: bun production-shapes.ts <ruleset reads dir> <production reads json> <read_record json> [<later reads json>...] <out json>"
    );
    process.exit(2);
  }
  const pin = derive(rulesetDir, readsFile, recordFile, args.slice(3, -1));
  writeFileSync(out, `${JSON.stringify(pin, null, 2)}\n`);
  const formatted = Bun.spawnSync(["bunx", "biome", "format", "--write", out], {
    stdout: "inherit",
    stderr: "inherit",
  });
  process.exit(formatted.exitCode);
}
