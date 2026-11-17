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
//   bun production-shapes.ts <ruleset reads dir> <production reads json> <out json>
//
// writes the pin from the two sets of production reads INFRA-764 made, then formats it as the
// repository's biome does.
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
/** The read the pin names `name`. A name it does not hold throws, naming it. */
export function pinnedRead(name: string): PinnedRead {
  pinned ??= new Map(
    Pin.parse(JSON.parse(readFileSync(PRODUCTION_SHAPES, "utf8"))).reads.map((read) => [
      read.name,
      read,
    ])
  );
  const read = pinned.get(name);
  if (read === undefined) throw new Error(`${PRODUCTION_SHAPES} pins no read named ${name}`);
  return read;
}

/** `read`'s command, after its `gh`, with each placeholder filled from `values`, so a row reads
 * the world with the very call production was read with. A placeholder `values` does not fill
 * throws. */
export function argvOf(read: PinnedRead, values: Readonly<Record<string, string>>): string[] {
  return read.command.slice(1).map((word) =>
    word.replace(/<(\w+)>/g, (whole, name: string) => {
      const value = values[name];
      if (value === undefined) throw new Error(`${read.name}: no value for ${whole}`);
      return value;
    })
  );
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

/** The pin of the two sets of production reads: `rulesetDir`, one `NAME.json`, `NAME.err` and
 * `NAME.exit` per read of a branch a ruleset alone protects, with its README naming when they were
 * read; and `readsFile`, every distinct read of a pull request INFRA-764's round 25 made, with its
 * `substitutions` naming the production values its commands carry and its README when it was read.
 * Throws where a value of production would be left in the pin. */
export function derive(rulesetDir: string, readsFile: string): z.infer<typeof Pin> {
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
  const reads = [...rulesetReads, ...prReads];
  const unique = new Set(reads.map(({ name }) => name));
  if (unique.size !== reads.length) throw new Error("two reads share a name");
  const pin = {
    made_by:
      "production-shapes.ts (bun production-shapes.ts <ruleset reads dir> <production reads json> <out json>), from INFRA-764's production reads of a branch a ruleset alone protects and of every distinct read round 25's runs made, each read from a Legion issue pod with the role App token its gh gives",
    reads,
  };
  const text = JSON.stringify(pin);
  const leaked = [
    owner,
    given.head,
    given.base,
    given.review,
    given.comment,
    ...ids,
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
  const [rulesetDir, readsFile, out, ...rest] = Bun.argv.slice(2);
  if (rulesetDir === undefined || readsFile === undefined || out === undefined || rest.length > 0) {
    console.error(
      "usage: bun production-shapes.ts <ruleset reads dir> <production reads json> <out json>"
    );
    process.exit(2);
  }
  writeFileSync(out, `${JSON.stringify(derive(rulesetDir, readsFile), null, 2)}\n`);
  const formatted = Bun.spawnSync(["bunx", "biome", "format", "--write", out], {
    stdout: "inherit",
    stderr: "inherit",
  });
  process.exit(formatted.exitCode);
}
