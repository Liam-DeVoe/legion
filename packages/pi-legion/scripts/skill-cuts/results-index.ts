/**
 * The index of the results of the skills of each set set-count counted (README.md,
 * "results-index"):
 *
 *   bun scripts/skill-cuts/cli.ts results-index --count <set-count out dir> --nearest <dir>
 *     --cases <dir> --results <dir>
 *
 * It lists each skill that set-count's `summary.json` (in `--count`) gives at AT_LEAST or more
 * own-copy sessions (its `atLeast20`), in that order, beside:
 *   - its nearest row: a JSON file at any depth under `--nearest` that is a `nearest` row
 *     (`{row: "nearest", skill: {name}, answer}`) of the skill's name; a row whose every reply was
 *     refused (`answer` null) names no nearest skill, so it is none;
 *   - its cases file: `cases-<skill>.jsonl` or `cases-<skill>.jsonl.gz` in `--cases`;
 *   - its result: `results-<skill>/` or `results-<skill>.tar.gz` in `--results`, holding
 *     `report.txt` (what `behaviour report` printed for the skill: the shallowest file of that
 *     name in it), whose `verdict:` line it prints; a result with no report, or a report with no
 *     verdict line, is none.
 * It calls no model and reads nothing else. Exit status: 0 when every skill has all three; 1,
 * naming each skill that lacks one; 2 refused (an argument missing, a summary.json that is not
 * set-count's, a directory it cannot read, or a `.json` file under `--nearest` that is no JSON or
 * is a nearest row of another shape).
 */
import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import path from "node:path";
import { z } from "zod";
import { parse, Refused, required, schemaFaults } from "./common";
import { AT_LEAST } from "./set-count";

const USAGE =
  "usage: cli.ts results-index --count <set-count out dir> --nearest <dir> --cases <dir> --results <dir>";

/** What results-index reads of set-count's summary.json: the skills at AT_LEAST or more. */
const Summary = z.looseObject({
  atLeast20: z.array(z.looseObject({ set: z.string(), skill: z.string(), own: z.number() })),
});

/** What results-index reads of a `nearest` row (cli.ts's `nearest`). */
const NearestRow = z.looseObject({
  row: z.literal("nearest"),
  skill: z.looseObject({ name: z.string().min(1) }),
  answer: z
    .looseObject({
      verdict: z.string(),
      nearest: z.array(z.looseObject({ name: z.string(), source: z.string() })),
    })
    .nullable(),
});
type NearestRow = z.infer<typeof NearestRow>;

/** What `main` writes to. */
export interface IndexIo {
  /** Writes `text` to standard output as it is. */
  out(text: string): void;
  /** Writes one line to standard error. */
  err(line: string): void;
}

/** The JSON value of `file`, which is `what`. Throws a refusal naming the file. */
function readJson(file: string, what: string): unknown {
  let text: string;
  try {
    text = readFileSync(file, "utf8");
  } catch (error) {
    throw new Refused(`${what} ${file} cannot be read: ${String(error)}`);
  }
  try {
    return JSON.parse(text);
  } catch {
    throw new Refused(`${file} is not JSON`);
  }
}

/** The names under `dir` (`recursive`: at any depth; else only those directly under it), or a
 *  refusal when it cannot be read. */
function listed(dir: string, flag: string, recursive = true): string[] {
  try {
    return readdirSync(dir, { recursive, encoding: "utf8" }).sort();
  } catch (error) {
    throw new Refused(`${flag} ${dir} cannot be read: ${String(error)}`);
  }
}

/** Each nearest row under `dir`, by its skill's name: each row's file and answer, in path order.
 *  A JSON file that is no nearest row is passed over. */
function nearestRows(dir: string): Map<string, { file: string; row: NearestRow }[]> {
  const rows = new Map<string, { file: string; row: NearestRow }[]>();
  for (const name of listed(dir, "--nearest").filter((each) => each.endsWith(".json"))) {
    const file = path.join(dir, name);
    if (!statSync(file).isFile()) continue;
    const value = readJson(file, "the nearest row");
    if (typeof value !== "object" || value === null || Reflect.get(value, "row") !== "nearest") {
      continue;
    }
    const row = NearestRow.safeParse(value);
    if (!row.success) {
      throw new Refused(`${file} is no nearest row: ${schemaFaults(row.error)}`);
    }
    const skill = row.data.skill.name;
    rows.set(skill, [...(rows.get(skill) ?? []), { file, row: row.data }]);
  }
  return rows;
}

/** The shallowest `report.txt` of `names`, paths inside a result, or undefined. */
function shallowestReport(names: readonly string[]): string | undefined {
  const depth = (member: string) => member.split("/").filter((part) => part !== ".").length;
  return names
    .filter((each) => path.basename(each) === "report.txt")
    .sort((a, b) => depth(a) - depth(b))[0];
}

/** The text of a result's report (`report.txt`, the shallowest of that name), from its directory
 *  or its tarball, or why it has none. */
function reportOf(result: string): { text: string } | { none: string } {
  if (statSync(result).isDirectory()) {
    const member = shallowestReport(readdirSync(result, { recursive: true, encoding: "utf8" }));
    if (member === undefined) return { none: "no report.txt" };
    return { text: readFileSync(path.join(result, member), "utf8") };
  }
  const list = Bun.spawnSync(["tar", "-tzf", result]);
  if (list.exitCode !== 0) {
    return { none: `its tarball cannot be read: ${list.stderr.toString().trim()}` };
  }
  const member = shallowestReport(list.stdout.toString().split("\n"));
  if (member === undefined) return { none: "no report.txt" };
  const read = Bun.spawnSync(["tar", "-xzOf", result, member]);
  if (read.exitCode !== 0) {
    return { none: `its ${member} cannot be read: ${read.stderr.toString().trim()}` };
  }
  return { text: read.stdout.toString() };
}

/** The first of `names` that is in `dir`, as a path, or undefined. */
function firstIn(dir: string, names: readonly string[]): string | undefined {
  return names.map((name) => path.join(dir, name)).find((file) => existsSync(file));
}

/** `skill-cuts results-index`. Returns the exit status: 0 whole, 1 a skill lacks a part, 2
 *  refused (thrown as Refused, which cli.ts's `main` turns into 2). */
export function main(argv: readonly string[], io: IndexIo): number {
  const values = parse(
    argv,
    {
      count: { type: "string" },
      nearest: { type: "string" },
      cases: { type: "string" },
      results: { type: "string" },
    },
    USAGE
  );
  const given = required(values, ["count", "nearest", "cases", "results"], USAGE);
  const summaryFile = path.join(given.count, "summary.json");
  const summary = Summary.safeParse(readJson(summaryFile, "set-count's summary"));
  if (!summary.success) {
    throw new Refused(`${summaryFile} is not set-count's summary: ${schemaFaults(summary.error)}`);
  }
  const rows = nearestRows(given.nearest);
  // Only that each root can be read: its files are looked up by name.
  listed(given.cases, "--cases", false);
  listed(given.results, "--results", false);

  const lacking: string[] = [];
  const lines = summary.data.atLeast20.map(({ set, skill, own }) => {
    const missing: string[] = [];
    const found = rows.get(skill) ?? [];
    const row = found.find((each) => each.row.answer !== null) ?? found[0];
    let nearest = "no row";
    if (row?.row.answer === null) nearest = `every reply refused (${row.file})`;
    else if (row?.row.answer !== undefined) {
      const named = row.row.answer.nearest.map((each) => `${each.name} (${each.source})`);
      nearest = `${row.row.answer.verdict}${named.length === 0 ? "" : `: ${named.join(", ")}`} (${row.file})`;
    }
    if (row?.row.answer == null) missing.push("no nearest row");
    const cases = firstIn(given.cases, [`cases-${skill}.jsonl`, `cases-${skill}.jsonl.gz`]);
    if (cases === undefined) missing.push("no cases file");
    const result = firstIn(given.results, [`results-${skill}`, `results-${skill}.tar.gz`]);
    let resultWords = "none";
    if (result !== undefined) {
      const report = reportOf(result);
      const verdict =
        "text" in report
          ? report.text.split("\n").find((line) => line.startsWith("verdict: "))
          : undefined;
      resultWords = `${result}: ${verdict ?? ("none" in report ? report.none : "its report.txt has no verdict line")}`;
      if (verdict === undefined) missing.push("no result");
    } else missing.push("no result");
    if (missing.length > 0) lacking.push(`${set}/${skill} (${missing.join(", ")})`);
    return `${set}/${skill} (${own} sessions): nearest: ${nearest}; cases: ${cases ?? "none"}; result: ${resultWords}`;
  });
  const total = lines.length;
  io.out(
    `${[
      `results-index: ${total} skill${total === 1 ? "" : "s"} at ${AT_LEAST} or more sessions in ${summaryFile}; ${total - lacking.length} with a nearest row, a cases file and a result`,
      ...lines,
    ].join("\n")}\n`
  );
  if (lacking.length === 0) return 0;
  io.err(
    `results-index: ${lacking.length} of ${total} skills lack a nearest row, a cases file or a result: ${lacking.join(", ")}`
  );
  return 1;
}
