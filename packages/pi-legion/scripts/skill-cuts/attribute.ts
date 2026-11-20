/**
 * R1 of the skill-cuts plan, the attribution: the read count's totals (count.sql's rows, one psql
 * output per store and UTC day of file mtime) set against R4's copies (copies.ts's output), into
 * one row per skill copy with its sessions, the report beside it, and each cut's ranking.
 *
 *   bun scripts/skill-cuts/attribute.ts --config <skill-cuts.json> --count <dir> --copies <dir>
 *     --spans <file> --out <dir> [--changed <cut id>=<file>]...
 *
 * `main(argv, io)` is exported so that cli.ts runs it as `skill-cuts attribute`; attribute.test.ts
 * calls the pure parts. It only reads its inputs, and it writes only under `--out`, which must not
 * be inside `--count` or `--copies`.
 *
 * Inputs.
 *   --count    The count's raw output. `<store>/<YYYY-MM-DD>.txt` for each store of the
 *              configuration and each UTC day, or `<store>/<YYYY-MM-DD>T<HH>.txt` for each hour of
 *              a day whose query timed out: each is the psql output of one count.sql query
 *              (`psqlRows`), its count rows and then one summary row, as count.sql's header says. A
 *              query that has no `.txt` has no output: it failed, or it has not run yet. `run.log`
 *              lists every query run, `<time>\t<store>\t<day or hour>\trc=<status>\tseconds=<n>\t
 *              <its stderr>`, a `FAILED ...` line for each query given up, and `END <time>` when
 *              the run ended.
 *   --copies   R4's output (copies.ts): skill-copies.tsv, prefix-table.tsv, installers.tsv,
 *              loaders.tsv and repositories.tsv, each with the columns copies.ts exports for it.
 *   --spans    One JSON line per store: `{store, earliest_mtime_ms, files}`, the store's earliest
 *              file mtime (epoch milliseconds) and its number of files.
 *   --changed  `<cut id>=<file>`, once for each cut of the configuration to rank. The file holds
 *              the cut's changed skills: one skill directory of the cut's repository per line, as
 *              skill-copies.tsv's `path` writes it.
 *
 * Rules.
 *   A session is one transcript file: a 16-hex file hash of a count row, with its store. Each one
 *   is counted once, however many queries of its store hold it: a resumed session whose mtime day
 *   moved while the count ran is in two queries. A `read` row counts in `sessions`, and a
 *   `reference` row (a read of `skill://<name>/<other>` or of `<name>/references/...`) in
 *   `reference_sessions`. A call that has no result in its file is not a read: the report counts
 *   such sessions per store, and no copy counts them.
 *   The copy of a count row is decided in this order:
 *   1. The prefix table. When every prefix-table.tsv row whose pattern matches the row's copy path
 *      maps to the same copy, the row goes to that copy. The match is `prefixRegExp`, the one
 *      matcher that `prefixMatches` uses, compiled once for each row.
 *   2. The store's context. The units (packages, plugins, a host's user skills, each named with its
 *      manifest in prefix-table.tsv's `via`) that the store's own step-1 reads come from are the
 *      units its context installs. Their copies are the installers.tsv rows of those units and the
 *      prefix rows' own copies. When they hold exactly one copy of the row's skill name, the row
 *      goes to that copy. A store with no step-1 read installs no unit.
 *   3. Otherwise the row is unattributed for its skill name.
 *   Each distinct copy-path prefix (`copyPrefix`: the path up to the skill's directory) that step 1
 *   does not map is listed with its sessions and where they went. No prefix is dropped.
 *   Two copies of one name are a tie when their sessions differ by at most the number of that
 *   name's unattributed sessions.
 *   A copy is loaded when code, an agent definition or an installer loads it into a session: its
 *   skill-copies.tsv `loaded_by` is not empty, an installers.tsv row loads it (action `loads`: an
 *   agent definition's autoloadSkills, a Python loader's list), a loaders.tsv row of its name
 *   matches `code-load`, or INJECTED_SKILLS holds its name.
 *   Oh My Pi installs a copy when an Oh My Pi session read it (every store holds Oh My Pi
 *   sessions), when a store's context installs it (step 2), or when an installers.tsv row of host
 *   `omp` installs it (an installer script that installs into Oh My Pi, an Oh My Pi plugins
 *   package.json, a package.json with `omp.skills`, the prepack script). Claude Code installs a
 *   copy when an installers.tsv row of host `claude-code` installs it. Status: `read` when the copy
 *   has a session; else `not countable yet` when only Claude Code installs it; else `no Oh My Pi
 *   read; Claude Code not counted yet` when both hosts install it; else `no read`.
 *
 * Outputs under --out.
 *   skill-read-count.tsv   READ_COUNT_COLUMNS, one row per copy of skill-copies.tsv, in its order:
 *                          repository, path, name, sessions, reference_sessions,
 *                          unattributed_sessions (of the name), tie (each copy of the name it ties
 *                          with, `<repository> <path>`, joined by `; `), loaded (yes or no), status.
 *   skill-read-count.md    The end time and the window; the stores with their spans, earliest
 *                          record and earliest read; each failed, split or missing query; the
 *                          attribution rule with the units each store's context installs; the
 *                          unmapped prefixes with their sessions; the ties; the loaded list; per
 *                          repository, the skills with no read; the two Claude Code lists; per
 *                          repository, the bytes of its skill names and descriptions at the commit
 *                          R4 read; the rankings.
 *   <cut id>/ranking.json  For each `--changed` cut, the cut repository's changed skills, each
 *                          `{skill, sessions}` (the sessions of that repository's copy), in the
 *                          order of score.ts's `rankSkills` (most sessions first, a tie by name).
 */
import { existsSync, mkdirSync, readdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { parseArgs } from "node:util";
import { z } from "zod";
import { loadConfig, type SkillCutsConfig } from "./config";
import {
  INSTALLERS_COLUMNS,
  LOADERS_COLUMNS,
  PREFIX_TABLE_COLUMNS,
  parseTsv,
  prefixRegExp,
  REPOSITORIES_COLUMNS,
  SKILL_COPIES_COLUMNS,
  tsv,
} from "./copies";
import { psqlRows } from "./psql-rows";
import { rankSkills } from "./score";

// ---------------------------------------------------------------------------------------------
// the count's output

const Hash = z.string().regex(/^[0-9a-f]{16}$/, { error: "a file hash of 16 hex characters" });
const Count = z.number().int().nonnegative();

/** One count row of count.sql. */
export const CountRow = z.strictObject({
  row: z.literal("count"),
  store: z.string(),
  day: z.string().regex(/^\d{4}-\d{2}-\d{2}$/),
  skill: z.string().min(1),
  copy: z.string(),
  kind: z.enum(["read", "reference"]),
  files: Count,
  hashes: z.array(Hash),
  unpaired: Count,
  unpaired_hashes: z.array(Hash),
});
export type CountRow = z.infer<typeof CountRow>;

/** The summary row that ends a count.sql result. */
export const SummaryRow = z.strictObject({
  row: z.literal("summary"),
  store: z.string(),
  mtime_from: z.number().int(),
  mtime_to: z.number().int(),
  files: Count,
  parts: Count,
  bytes: Count,
  lines_parsed: Count,
  bad_lines: Count,
  shared_call_ids: Count,
});
export type SummaryRow = z.infer<typeof SummaryRow>;

const QueryRow = z.discriminatedUnion("row", [CountRow, SummaryRow]);

/** One query's output: its store, its name (`YYYY-MM-DD`, or `YYYY-MM-DDTHH` for an hour), its
 *  count rows and its summary. */
export interface QueryOutput {
  readonly store: string;
  readonly name: string;
  readonly rows: readonly CountRow[];
  readonly summary: SummaryRow;
}

const QUERY_FILE = /^\d{4}-\d{2}-\d{2}(T\d{2})?\.txt$/;

/** One line naming each fault of a value that does not fit its schema. */
function faultsOf(error: z.ZodError): string {
  return error.issues
    .map((issue) => `${issue.path.join(".") || "the value"}: ${issue.message}`)
    .join("; ");
}

/** The output of the query `name` of the store `store`; throws when the text is not one whole
 *  count.sql result of that store: no psql table, a row of another shape or store, a row after the
 *  summary, hashes that do not match their counts, or no summary (the query did not end). */
export function parseQueryOutput(store: string, name: string, text: string): QueryOutput {
  const rows: CountRow[] = [];
  let summary: SummaryRow | undefined;
  for (const [index, value] of psqlRows(text).entries()) {
    const at = `row ${index + 1}`;
    const parsed = QueryRow.safeParse(value);
    if (!parsed.success) throw new Error(`${at} is not a count.sql row: ${faultsOf(parsed.error)}`);
    const row = parsed.data;
    if (row.store !== store) throw new Error(`${at} is of the store ${JSON.stringify(row.store)}`);
    if (summary !== undefined) throw new Error(`${at} comes after the summary row`);
    if (row.row === "summary") {
      summary = row;
      continue;
    }
    if (row.hashes.length !== row.files || row.unpaired_hashes.length !== row.unpaired) {
      throw new Error(`${at}: its hashes do not match its files or unpaired counts`);
    }
    rows.push(row);
  }
  if (summary === undefined) throw new Error("no summary row: the query did not end");
  return { store, name, rows, summary };
}

/** One query line of the run log. */
export interface LogEntry {
  readonly store: string;
  readonly name: string;
  readonly rc: number;
  /** The query's stderr, as the log keeps it (its first 200 characters, on one line). */
  readonly error: string;
}

/** What the run log says: each query line, each `FAILED` line, each line it could not read, and
 *  the time of its `END` line (undefined while the run goes on). */
export interface RunLog {
  readonly entries: readonly LogEntry[];
  readonly failed: readonly string[];
  readonly unread: readonly string[];
  readonly ended: string | undefined;
}

const LOG_ENTRY =
  /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z\t([^\t]+)\t(\d{4}-\d{2}-\d{2}(?:T\d{2})?)\trc=(\d+)\tseconds=[^\t]*(?:\t(.*))?$/;

export function parseRunLog(text: string): RunLog {
  const entries: LogEntry[] = [];
  const failed: string[] = [];
  const unread: string[] = [];
  let ended: string | undefined;
  for (const line of text.split("\n")) {
    const entry = LOG_ENTRY.exec(line);
    if (entry !== null) {
      entries.push({
        store: entry[1] ?? "",
        name: entry[2] ?? "",
        rc: Number(entry[3]),
        error: (entry[4] ?? "").trim(),
      });
    } else if (line.startsWith("FAILED ")) failed.push(line);
    else if (line.startsWith("END ")) ended = line.slice("END ".length).trim();
    else if (/^\d{4}-\d{2}-\d{2}T/.test(line)) unread.push(line);
  }
  return { entries, failed, unread, ended };
}

/** What the count directory holds for the configured stores. */
export interface CountRead {
  readonly outputs: readonly QueryOutput[];
  /** Each `.txt` that is not one whole count.sql result, and why. */
  readonly unreadable: readonly { store: string; name: string; why: string }[];
  /** The run log, or undefined when the directory holds none. */
  readonly log: RunLog | undefined;
  /** Each directory of the count that the configuration names no store for. */
  readonly otherStores: readonly string[];
}

/** Reads the count directory `dir` for the stores `stores`. */
export function readCount(dir: string, stores: readonly string[]): CountRead {
  if (!existsSync(dir)) throw new Error(`the count directory ${dir} does not exist`);
  const outputs: QueryOutput[] = [];
  const unreadable: { store: string; name: string; why: string }[] = [];
  for (const store of stores) {
    const storeDir = path.join(dir, store);
    if (!existsSync(storeDir)) continue;
    for (const file of readdirSync(storeDir).sort()) {
      if (!QUERY_FILE.test(file)) continue;
      const name = file.slice(0, -".txt".length);
      try {
        outputs.push(
          parseQueryOutput(store, name, readFileSync(path.join(storeDir, file), "utf8"))
        );
      } catch (error) {
        unreadable.push({ store, name, why: (error as Error).message });
      }
    }
  }
  const logFile = path.join(dir, "run.log");
  const log = existsSync(logFile) ? parseRunLog(readFileSync(logFile, "utf8")) : undefined;
  const otherStores = readdirSync(dir, { withFileTypes: true })
    .filter((entry) => entry.isDirectory() && !stores.includes(entry.name))
    .map((entry) => entry.name)
    .sort();
  return { outputs, unreadable, log, otherStores };
}

/** A query of the count that has no whole output, or that was split, and what happened to it. */
export interface QueryIssue {
  readonly store: string;
  /** The query's day or hour, or `""` for an issue of the run log itself. */
  readonly name: string;
  readonly what: "failed" | "split" | "no output" | "unreadable" | "run log";
  readonly detail: string;
}

const DAY_MS = 86_400_000;
const STATEMENT_TIMEOUT = /statement timeout/i;
const HOURS = Array.from({ length: 24 }, (_, hour) => hour);
const twoDigits = (value: number) => String(value).padStart(2, "0");

/** The UTC days (YYYY-MM-DD) from the day of `first` to the day of `last`, both included. */
function daysFrom(first: number, last: number): string[] {
  const days: string[] = [];
  for (let at = Math.floor(first / DAY_MS) * DAY_MS; at <= last; at += DAY_MS) {
    days.push(new Date(at).toISOString().slice(0, 10));
  }
  return days;
}

/**
 * Each query that failed, was split into hours, or has no output, from the run log and the
 * directory: a query whose last log line has a non-zero status (a day that timed out is split, and
 * its hours that have no output are named); a `FAILED` line the log lines do not already show; a
 * `.txt` that is not a whole result; and each day of the window (and, once the log has its `END`
 * line, each day from the window's end to the run's end) with no output of the day or of an hour.
 */
export function queryIssues(config: SkillCutsConfig, count: CountRead): QueryIssue[] {
  const issues: QueryIssue[] = [];
  const log = count.log;
  if (log === undefined) {
    issues.push({
      store: "",
      name: "",
      what: "run log",
      detail: "the count directory holds no run.log, so no query's status is known",
    });
  }
  for (const line of log?.unread ?? []) {
    issues.push({ store: "", name: "", what: "run log", detail: `a line not read: ${line}` });
  }
  const last = new Map<string, LogEntry>();
  for (const entry of log?.entries ?? []) last.set(`${entry.store}\t${entry.name}`, entry);
  const windowEnd = Date.parse(config.windowEnd);
  const expected = daysFrom(Date.parse(config.windowStart), windowEnd - 1);
  if (log?.ended !== undefined && !Number.isNaN(Date.parse(log.ended))) {
    for (const day of daysFrom(windowEnd, Date.parse(log.ended)))
      if (!expected.includes(day)) expected.push(day);
  }
  for (const { name: store } of config.stores) {
    const names = new Set(
      count.outputs.filter((output) => output.store === store).map((output) => output.name)
    );
    const hoursOf = (day: string) => HOURS.filter((hour) => names.has(`${day}T${twoDigits(hour)}`));
    const listed = new Set<string>();
    const add = (name: string, what: QueryIssue["what"], detail: string) => {
      listed.add(name);
      issues.push({ store, name, what, detail });
    };
    for (const entry of last.values()) {
      if (entry.store !== store || entry.rc === 0) continue;
      if (entry.name.length === 10 && STATEMENT_TIMEOUT.test(entry.error)) {
        const read = hoursOf(entry.name);
        const missing = HOURS.filter((hour) => !read.includes(hour));
        add(
          entry.name,
          "split",
          `timed out, so it was split into hours: ${read.length} of 24 hour queries have output` +
            (missing.length > 0 ? `; hours ${missing.map(twoDigits).join(", ")} have none` : "")
        );
      } else if (!names.has(entry.name)) {
        add(entry.name, "failed", `rc=${entry.rc}${entry.error === "" ? "" : `: ${entry.error}`}`);
      }
    }
    for (const line of log?.failed ?? []) {
      const failed = /^FAILED (\S+) (\d{4}-\d{2}-\d{2})(?: hour (\d+))?/.exec(line);
      if (failed === null || failed[1] !== store) continue;
      const day = failed[2] ?? "";
      const name = failed[3] === undefined ? day : `${day}T${twoDigits(Number(failed[3]))}`;
      if (!listed.has(name)) add(name, "failed", `the run log says: ${line}`);
    }
    for (const unreadable of count.unreadable.filter((item) => item.store === store)) {
      add(unreadable.name, "unreadable", `its output is not a whole result: ${unreadable.why}`);
    }
    for (const day of expected) {
      if (names.has(day) || hoursOf(day).length > 0 || listed.has(day)) continue;
      const entry = last.get(`${store}\t${day}`);
      add(
        day,
        "no output",
        entry === undefined
          ? "the run log has no line for it: not run yet"
          : `the run log gives rc=${entry.rc}, and no output of it is here`
      );
    }
  }
  return issues;
}

// ---------------------------------------------------------------------------------------------
// the spans file and R4's tables

/** One line of the spans file. */
export const Span = z.looseObject({
  store: z.string().min(1),
  earliest_mtime_ms: z.number().int().nullable(),
  files: Count,
});
export type Span = z.infer<typeof Span>;

/** The spans in `text`, one JSON line each; throws naming the line of a fault. */
export function parseSpans(text: string, source = "the spans file"): Span[] {
  const spans: Span[] = [];
  for (const [index, line] of text.split("\n").entries()) {
    if (line.trim() === "") continue;
    let value: unknown;
    try {
      value = JSON.parse(line);
    } catch (error) {
      throw new Error(`${source} line ${index + 1} is not JSON: ${(error as Error).message}`);
    }
    const parsed = Span.safeParse(value);
    if (!parsed.success) throw new Error(`${source} line ${index + 1}: ${faultsOf(parsed.error)}`);
    spans.push(parsed.data);
  }
  return spans;
}

type Columns<C extends readonly string[]> = { readonly [K in C[number]]: string };
export type SkillCopiesRow = Columns<typeof SKILL_COPIES_COLUMNS>;
export type PrefixTableRow = Columns<typeof PREFIX_TABLE_COLUMNS>;
export type InstallersRow = Columns<typeof INSTALLERS_COLUMNS>;
export type LoadersRow = Columns<typeof LOADERS_COLUMNS>;
export type RepositoriesRow = Columns<typeof REPOSITORIES_COLUMNS>;

/** The tables of R4's output this file reads. */
export interface R4Tables {
  readonly copies: readonly SkillCopiesRow[];
  readonly prefixes: readonly PrefixTableRow[];
  readonly installers: readonly InstallersRow[];
  readonly loaders: readonly LoadersRow[];
  readonly repositories: readonly RepositoriesRow[];
}

/** The rows of the TSV `file` of `dir`; throws when its header row is not `columns`, in order. */
function readTable<C extends readonly string[]>(
  dir: string,
  file: string,
  columns: C
): Columns<C>[] {
  const where = path.join(dir, file);
  let text: string;
  try {
    text = readFileSync(where, "utf8");
  } catch (error) {
    throw new Error(`cannot read ${where}: ${(error as Error).message}`);
  }
  const header = text.split("\n")[0] ?? "";
  if (header !== columns.join("\t")) {
    throw new Error(
      `${where}: its columns are ${header.split("\t").join(", ")}, not copies.ts's ${columns.join(", ")}`
    );
  }
  return parseTsv(text) as Columns<C>[];
}

/** R4's tables in its output directory `dir`. */
export function readR4(dir: string): R4Tables {
  return {
    copies: readTable(dir, "skill-copies.tsv", SKILL_COPIES_COLUMNS),
    prefixes: readTable(dir, "prefix-table.tsv", PREFIX_TABLE_COLUMNS),
    installers: readTable(dir, "installers.tsv", INSTALLERS_COLUMNS),
    loaders: readTable(dir, "loaders.tsv", LOADERS_COLUMNS),
    repositories: readTable(dir, "repositories.tsv", REPOSITORIES_COLUMNS),
  };
}

// ---------------------------------------------------------------------------------------------
// the attribution

/** The installers.tsv actions that put a skill into a host's sessions. */
export const INSTALL_ACTIONS: Readonly<Record<string, true>> = {
  ships: true,
  stages: true,
  installs: true,
  links: true,
  enables: true,
};

/** Skills that code puts into every session, by name, with what does it. */
export const INJECTED_SKILLS: Readonly<Record<string, string>> = {
  "dispatch-first":
    "pi-envoy injects it into each session that has Dispatch (packages/envoy-client/src/dispatch-first.ts)",
};

export const STATUSES = [
  "read",
  "no read",
  "not countable yet",
  "no Oh My Pi read; Claude Code not counted yet",
] as const;
export type Status = (typeof STATUSES)[number];

export const READ_COUNT_COLUMNS = [
  "repository",
  "path",
  "name",
  "sessions",
  "reference_sessions",
  "unattributed_sessions",
  "tie",
  "loaded",
  "status",
] as const;

/**
 * The copy-path prefix of a count row's copy: the path up to and with the skill's directory.
 * `skill://<name>/` for a `skill://` path. Otherwise the last directory named as the skill that a
 * `SKILL.md` (with any read selector after it) or a `references` directory follows; else the
 * directory of a `SKILL.md`; else the directory before the first `references`; else the path's
 * directory.
 */
export function copyPrefix(copyPath: string, skill: string): string {
  if (copyPath.startsWith("skill://")) return `skill://${skill}/`;
  const parts = copyPath.split("/");
  const upTo = (end: number) => `${parts.slice(0, end).join("/")}/`;
  const isSkillFile = (part: string | undefined) => part?.startsWith("SKILL.md") === true;
  for (let index = parts.length - 2; index >= 0; index--) {
    const next = parts[index + 1];
    if (parts[index] === skill && (isSkillFile(next) || next === "references"))
      return upTo(index + 1);
  }
  if (isSkillFile(parts[parts.length - 1]) && parts.length > 1) return upTo(parts.length - 1);
  const references = parts.indexOf("references");
  if (references > 0) return upTo(references);
  return parts.length > 1 ? upTo(parts.length - 1) : copyPath;
}

/** A unit (`<unit> (<repository>:<manifest path>)` in prefix-table.tsv's `via`) as a key. */
const unitKey = (unit: string, repository: string, manifest: string) =>
  `${unit}\t${repository}\t${manifest}`;
/** The unit of a key, as prefix-table.tsv's `via` writes it. */
function unitLabel(key: string): string {
  const [unit = "", repository = "", manifest = ""] = key.split("\t");
  return `${unit} (${repository}:${manifest})`;
}

/** The unit keys a prefix row's `via` names; throws on an item of another shape. */
export function viaUnits(via: string): string[] {
  return via
    .split("; ")
    .filter((item) => item !== "")
    .map((item) => {
      const match = /^(.+) \(([^\s:()]+\/[^\s:()]+):(.+)\)$/.exec(item);
      if (match === null) {
        throw new Error(
          `a via item that is not <unit> (<repository>:<manifest path>): ${JSON.stringify(item)}`
        );
      }
      return unitKey(match[1] ?? "", match[2] ?? "", match[3] ?? "");
    });
}

const copyKey = (repository: string, dir: string) => `${repository}\t${dir}`;
/** A copy as the report names it: `<repository> <path>`. */
const shown = (key: string) => key.replace("\t", " ");

function entry<K, V>(map: Map<K, V>, key: K, make: () => V): V {
  let value = map.get(key);
  if (value === undefined) {
    value = make();
    map.set(key, value);
  }
  return value;
}

/** One copy's counts. */
export interface CopyCount {
  readonly repository: string;
  readonly path: string;
  readonly name: string;
  readonly sessions: number;
  readonly referenceSessions: number;
  /** The read sessions of the copy's name that no copy got. */
  readonly unattributed: number;
  /** Each copy of the name it ties with, `<repository> <path>`. */
  readonly ties: readonly string[];
  /** What loads it into a session; empty when nothing does. */
  readonly loadedBy: readonly string[];
  readonly ompInstalls: boolean;
  readonly claudeInstalls: boolean;
  readonly status: Status;
}

/** A copy-path prefix the prefix table does not map, and where its sessions went. */
export interface UnmappedPrefix {
  readonly prefix: string;
  readonly skills: readonly string[];
  readonly sessions: number;
  readonly referenceSessions: number;
  /** Each outcome (`<store>: to <copy> ...` or `<store>: unattributed: <why>`) with its sessions,
   *  read and reference together. */
  readonly outcomes: readonly { readonly outcome: string; readonly sessions: number }[];
}

/** A unit a store's context installs, with the sessions of the store that read from it. */
export interface StoreUnit {
  readonly unit: string;
  readonly sessions: number;
  readonly copies: number;
}

/** What the count gives for one store. */
export interface StoreCount {
  readonly store: string;
  readonly context: string;
  readonly outputs: number;
  readonly filesRead: number;
  readonly bytesRead: number;
  readonly badLines: number;
  readonly sharedCallIds: number;
  readonly readSessions: number;
  readonly unpairedSessions: number;
  /** The first-read day of its earliest read in the window, or undefined with none. */
  readonly earliestRead: string | undefined;
  readonly units: readonly StoreUnit[];
}

/** A skill name the count holds reads of that no copy of R4 has. */
export interface NameWithoutCopy {
  readonly name: string;
  readonly sessions: number;
  readonly referenceSessions: number;
}

export interface Attribution {
  readonly copies: readonly CopyCount[];
  readonly unmapped: readonly UnmappedPrefix[];
  /** Each prefix the table maps, with the copies (`<repository> <path>`) it maps to. */
  readonly mappedPrefixes: ReadonlyMap<string, ReadonlySet<string>>;
  readonly stores: readonly StoreCount[];
  readonly namesWithoutCopy: readonly NameWithoutCopy[];
  /** The distinct (store, file) sessions with a read. */
  readonly readSessions: number;
  /** The distinct read sessions some row of which went to no copy. */
  readonly unattributedSessions: number;
}

type TableDecision =
  | { readonly copy: string; readonly units: readonly string[] }
  | { readonly copy: undefined; readonly copies: readonly string[] };

/** A prefix-table.tsv row as the attribution reads it: its pattern, the last directory name of its
 *  prefix, its copy and the units of its `via`. */
interface PrefixMatcher {
  readonly pattern: RegExp;
  readonly last: string;
  readonly copy: string;
  readonly units: readonly string[];
}

/** The copy prefix-table.tsv's rows map a copy path to. Only the rows whose prefix's last
 *  directory name is a directory of the path (or holds a `*`) are tried. */
function prefixTable(rows: readonly PrefixMatcher[]) {
  const byLast = new Map<string, PrefixMatcher[]>();
  const wild: PrefixMatcher[] = [];
  for (const row of rows) {
    if (row.last.includes("*") || row.last === "") wild.push(row);
    else entry(byLast, row.last, (): PrefixMatcher[] => []).push(row);
  }
  const decisions = new Map<string, TableDecision>();
  return (copyPath: string): TableDecision => {
    const known = decisions.get(copyPath);
    if (known !== undefined) return known;
    const candidates = new Set(wild);
    for (const part of copyPath.split("/"))
      for (const row of byLast.get(part) ?? []) candidates.add(row);
    const matched = [...candidates].filter((row) => row.pattern.test(copyPath));
    const found = [...new Set(matched.map((row) => row.copy))];
    const [only] = found;
    const decision: TableDecision =
      found.length === 1 && only !== undefined
        ? { copy: only, units: [...new Set(matched.flatMap((row) => row.units))] }
        : { copy: undefined, copies: found };
    decisions.set(copyPath, decision);
    return decision;
  };
}

/** The attribution of the count's rows `outputs` to R4's copies, by the rules of this file's
 *  header. Throws when R4's tables name a copy that skill-copies.tsv does not hold. */
export function attribute(
  config: SkillCutsConfig,
  outputs: readonly QueryOutput[],
  r4: R4Tables
): Attribution {
  const copies = new Map<string, SkillCopiesRow>();
  for (const row of r4.copies) copies.set(copyKey(row.repository, row.path), row);
  const known = (key: string, where: string) => {
    if (!copies.has(key))
      throw new Error(`${where} names ${shown(key)}, which skill-copies.tsv does not hold`);
    return key;
  };

  // The copies of each unit: its installer rows, and the copies its prefix rows map to.
  const members = new Map<string, Set<string>>();
  for (const [index, row] of r4.installers.entries()) {
    if (!Object.hasOwn(INSTALL_ACTIONS, row.action) || row.copy_path === "") continue;
    const key = known(
      copyKey(row.copy_repository, row.copy_path),
      `installers.tsv row ${index + 2}`
    );
    entry(
      members,
      unitKey(row.unit, row.manifest_repository, row.manifest_path),
      () => new Set()
    ).add(key);
  }
  const decide = prefixTable(
    r4.prefixes.map((row, index) => {
      const key = known(copyKey(row.repository, row.path), `prefix-table.tsv row ${index + 2}`);
      const units = viaUnits(row.via);
      for (const unit of units) entry(members, unit, () => new Set<string>()).add(key);
      const parts = row.prefix.split("/").filter((part) => part !== "");
      return {
        pattern: prefixRegExp(row.prefix),
        last: parts[parts.length - 1] ?? "",
        copy: key,
        units,
      };
    })
  );

  // Step 2's units: those each store's own mapped reads come from, with their sessions.
  const storeUnits = new Map<string, Map<string, Set<string>>>();
  for (const output of outputs) {
    for (const row of output.rows) {
      if (row.hashes.length === 0) continue;
      const decision = decide(row.copy);
      if (decision.copy === undefined) continue;
      const units = entry(storeUnits, row.store, () => new Map<string, Set<string>>());
      for (const unit of decision.units) {
        const sessions = entry(units, unit, () => new Set<string>());
        for (const hash of row.hashes) sessions.add(hash);
      }
    }
  }
  const contextCopies = new Map<string, Map<string, Set<string>>>();
  const installedInContext = new Set<string>();
  for (const [store, units] of storeUnits) {
    const byName = new Map<string, Set<string>>();
    for (const unit of units.keys()) {
      for (const key of members.get(unit) ?? []) {
        entry(byName, copies.get(key)?.name ?? "", () => new Set()).add(key);
        installedInContext.add(key);
      }
    }
    contextCopies.set(store, byName);
  }

  const reads = new Map<string, Set<string>>();
  const references = new Map<string, Set<string>>();
  const unattributed = new Map<string, Set<string>>();
  const unattributedReferences = new Map<string, Set<string>>();
  const unmapped = new Map<
    string,
    {
      skills: Set<string>;
      read: Set<string>;
      reference: Set<string>;
      outcomes: Map<string, Set<string>>;
    }
  >();
  const mappedPrefixes = new Map<string, Set<string>>();
  const allReads = new Set<string>();
  const stats = new Map<
    string,
    {
      outputs: number;
      files: number;
      bytes: number;
      bad: number;
      shared: number;
      read: Set<string>;
      unpaired: Set<string>;
      earliest: string | undefined;
    }
  >();
  for (const output of outputs) {
    const store = entry(stats, output.store, () => ({
      outputs: 0,
      files: 0,
      bytes: 0,
      bad: 0,
      shared: 0,
      read: new Set<string>(),
      unpaired: new Set<string>(),
      earliest: undefined as string | undefined,
    }));
    store.outputs++;
    store.files += output.summary.files;
    store.bytes += output.summary.bytes;
    store.bad += output.summary.bad_lines;
    store.shared += output.summary.shared_call_ids;
    for (const row of output.rows) {
      for (const hash of row.unpaired_hashes) store.unpaired.add(hash);
      if (row.hashes.length === 0) continue;
      const sessions = row.hashes.map((hash) => `${row.store}\t${hash}`);
      if (row.kind === "read") {
        for (const hash of row.hashes) store.read.add(hash);
        for (const session of sessions) allReads.add(session);
        if (store.earliest === undefined || row.day < store.earliest) store.earliest = row.day;
      }
      const prefix = copyPrefix(row.copy, row.skill);
      const decision = decide(row.copy);
      let copy: string | undefined;
      if (decision.copy !== undefined) {
        copy = decision.copy;
        entry(mappedPrefixes, prefix, () => new Set()).add(shown(copy));
      } else {
        const context = [...(contextCopies.get(row.store)?.get(row.skill) ?? [])];
        const table =
          decision.copies.length > 1
            ? `prefix rows match ${decision.copies.length} copies (${decision.copies.map(shown).join(", ")}), and `
            : "";
        let outcome: string;
        if (context.length === 1 && context[0] !== undefined) {
          copy = context[0];
          outcome = `${row.store}: to ${shown(copy)}, the one copy of ${row.skill} its context installs`;
        } else {
          outcome =
            `${row.store}: unattributed: ${table}its context installs ` +
            (context.length === 0
              ? `no copy of ${row.skill}`
              : `${context.length} copies of ${row.skill} (${context.map(shown).join(", ")})`);
        }
        const item = entry(unmapped, prefix, () => ({
          skills: new Set<string>(),
          read: new Set<string>(),
          reference: new Set<string>(),
          outcomes: new Map<string, Set<string>>(),
        }));
        item.skills.add(row.skill);
        const went = entry(item.outcomes, outcome, () => new Set<string>());
        for (const session of sessions) {
          (row.kind === "read" ? item.read : item.reference).add(session);
          went.add(session);
        }
      }
      const counted =
        copy === undefined
          ? entry(
              row.kind === "read" ? unattributed : unattributedReferences,
              row.skill,
              () => new Set<string>()
            )
          : entry(row.kind === "read" ? reads : references, copy, () => new Set<string>());
      for (const session of sessions) counted.add(session);
    }
  }

  // Each copy's loaders, installs, ties and status.
  const loadedBy = new Map<string, Set<string>>();
  const loadedByName = new Map<string, Set<string>>();
  const ompInstalled = new Set<string>();
  const claudeInstalled = new Set<string>();
  for (const row of r4.installers) {
    const key = row.copy_path === "" ? undefined : copyKey(row.copy_repository, row.copy_path);
    if (row.action === "loads") {
      const how = `${row.manifest_repository}:${row.manifest_path} (${row.kind} ${row.unit})`;
      if (key === undefined) entry(loadedByName, row.skill, () => new Set()).add(how);
      else entry(loadedBy, key, () => new Set()).add(how);
    }
    if (key === undefined || !Object.hasOwn(INSTALL_ACTIONS, row.action)) continue;
    if (row.host === "omp") ompInstalled.add(key);
    if (row.host === "claude-code") claudeInstalled.add(key);
  }
  for (const loader of r4.loaders) {
    if (!loader.match.split(",").includes("code-load")) continue;
    entry(loadedByName, loader.skill, () => new Set()).add(
      `${loader.repository}:${loader.path} (code-load, ${loader.source})`
    );
  }
  const byName = new Map<string, SkillCopiesRow[]>();
  for (const row of r4.copies) entry(byName, row.name, () => []).push(row);
  const counts = r4.copies.map((row): CopyCount => {
    const key = copyKey(row.repository, row.path);
    const sessions = reads.get(key)?.size ?? 0;
    const referenceSessions = references.get(key)?.size ?? 0;
    const free = unattributed.get(row.name)?.size ?? 0;
    const ties = (byName.get(row.name) ?? [])
      .filter((other) => other !== row)
      .map((other) => copyKey(other.repository, other.path))
      .filter((other) => Math.abs((reads.get(other)?.size ?? 0) - sessions) <= free)
      .map(shown);
    const loaders = new Set([
      ...row.loaded_by.split("; ").filter((item) => item !== ""),
      ...(loadedBy.get(key) ?? []),
      ...(loadedByName.get(row.name) ?? []),
    ]);
    const injected = INJECTED_SKILLS[row.name];
    if (injected !== undefined) loaders.add(injected);
    const ompInstalls =
      sessions + referenceSessions > 0 || installedInContext.has(key) || ompInstalled.has(key);
    const claudeInstalls = claudeInstalled.has(key);
    const status: Status =
      sessions > 0
        ? "read"
        : claudeInstalls && !ompInstalls
          ? "not countable yet"
          : claudeInstalls
            ? "no Oh My Pi read; Claude Code not counted yet"
            : "no read";
    return {
      repository: row.repository,
      path: row.path,
      name: row.name,
      sessions,
      referenceSessions,
      unattributed: free,
      ties,
      loadedBy: [...loaders].sort(),
      ompInstalls,
      claudeInstalls,
      status,
    };
  });

  const namesWithoutCopy = [...new Set([...unattributed.keys(), ...unattributedReferences.keys()])]
    .filter((name) => !byName.has(name))
    .sort()
    .map((name) => ({
      name,
      sessions: unattributed.get(name)?.size ?? 0,
      referenceSessions: unattributedReferences.get(name)?.size ?? 0,
    }));
  const unattributedSessions = new Set([...unattributed.values()].flatMap((set) => [...set])).size;
  return {
    copies: counts,
    unmapped: [...unmapped]
      .map(([prefix, item]) => ({
        prefix,
        skills: [...item.skills].sort(),
        sessions: item.read.size,
        referenceSessions: item.reference.size,
        outcomes: [...item.outcomes]
          .map(([outcome, sessions]) => ({ outcome, sessions: sessions.size }))
          .sort((a, b) => b.sessions - a.sessions || a.outcome.localeCompare(b.outcome)),
      }))
      .sort(
        (a, b) =>
          b.sessions - a.sessions ||
          b.referenceSessions - a.referenceSessions ||
          a.prefix.localeCompare(b.prefix)
      ),
    mappedPrefixes,
    stores: config.stores.map((store): StoreCount => {
      const stat = stats.get(store.name);
      return {
        store: store.name,
        context: store.context,
        outputs: stat?.outputs ?? 0,
        filesRead: stat?.files ?? 0,
        bytesRead: stat?.bytes ?? 0,
        badLines: stat?.bad ?? 0,
        sharedCallIds: stat?.shared ?? 0,
        readSessions: stat?.read.size ?? 0,
        unpairedSessions: stat?.unpaired.size ?? 0,
        earliestRead: stat?.earliest,
        units: [...(storeUnits.get(store.name) ?? [])]
          .map(([unit, sessions]) => ({
            unit: unitLabel(unit),
            sessions: sessions.size,
            copies: members.get(unit)?.size ?? 0,
          }))
          .sort((a, b) => b.sessions - a.sessions || a.unit.localeCompare(b.unit)),
      };
    }),
    namesWithoutCopy,
    readSessions: allReads.size,
    unattributedSessions,
  };
}

// ---------------------------------------------------------------------------------------------
// rankings

/** One cut's ranking: its changed skills with the sessions of its repository's copy, in
 *  `rankSkills` order, and each changed skill directory R4 read no copy at. */
export interface Ranking {
  readonly cut: string;
  readonly repository: string;
  readonly rows: readonly { readonly skill: string; readonly sessions: number }[];
  readonly missing: readonly string[];
}

/** The ranking of the cut `cut` of `repository` whose changed skill directories are `changed` (the
 *  lines of its `--changed` file). A directory R4 read no copy at is ranked with 0 sessions, under
 *  its directory's name, and named in `missing`. */
export function rankChanged(
  attribution: Attribution,
  cut: string,
  repository: string,
  changed: readonly string[]
): Ranking {
  const counts = new Map(
    attribution.copies.map((count) => [copyKey(count.repository, count.path), count])
  );
  const missing: string[] = [];
  const rows = [
    ...new Set(changed.map((line) => line.trim().replace(/\/+$/, "")).filter((l) => l !== "")),
  ].map((dir) => {
    const count = counts.get(copyKey(repository, dir));
    if (count === undefined) missing.push(dir);
    return { skill: count?.name ?? path.posix.basename(dir), sessions: count?.sessions ?? 0 };
  });
  const byName = new Map<string, { skill: string; sessions: number }[]>();
  for (const row of rows.toSorted((a, b) => b.sessions - a.sessions))
    entry(byName, row.skill, () => []).push(row);
  const ranked = rankSkills(rows).flatMap((skill) => {
    const next = byName.get(skill)?.shift();
    return next === undefined ? [] : [next];
  });
  return { cut, repository, rows: ranked, missing };
}

// ---------------------------------------------------------------------------------------------
// output

/** skill-read-count.tsv's text. */
export function readCountTsv(attribution: Attribution): string {
  return tsv(
    READ_COUNT_COLUMNS,
    attribution.copies.map((count) => [
      count.repository,
      count.path,
      count.name,
      count.sessions,
      count.referenceSessions,
      count.unattributed,
      count.ties.join("; "),
      count.loadedBy.length > 0 ? "yes" : "no",
      count.status,
    ])
  );
}

/** `text` as Markdown inline code. */
function code(text: string): string {
  return text.includes("`") ? `\`\` ${text} \`\`` : `\`${text}\``;
}
/** `text` as one Markdown table cell. */
const cell = (text: string | number) => String(text).replace(/\|/g, "\\|");
const iso = (ms: number) => new Date(ms).toISOString().replace(".000Z", "Z");
/** `count` with `word`, or with `plural` unless the count is 1: `1 session`, `2 sessions`. */
const counted = (count: number, word: string, plural = `${word}s`) =>
  `${count} ${count === 1 ? word : plural}`;

/** Everything the report says. */
export interface Report {
  readonly config: SkillCutsConfig;
  readonly attribution: Attribution;
  readonly count: CountRead;
  readonly issues: readonly QueryIssue[];
  readonly spans: readonly Span[];
  readonly r4: R4Tables;
  readonly rankings: readonly Ranking[];
}

/** skill-read-count.md's text. */
export function readCountMarkdown(report: Report): string {
  const { config, attribution, count, issues, spans, r4, rankings } = report;
  const lines: string[] = [];
  const say = (...text: string[]) => lines.push(...text);
  const windowStart = Date.parse(config.windowStart);
  const windowEnd = Date.parse(config.windowEnd);
  const windowDays = (windowEnd - windowStart) / DAY_MS;
  const of = (status: Status) => attribution.copies.filter((copy) => copy.status === status);
  const repositories = [...new Set(attribution.copies.map((copy) => copy.repository))].sort();
  const names = new Set(attribution.copies.map((copy) => copy.name));

  say(
    "# Skill read count",
    "",
    `End time ${config.windowEnd}. The window is [${config.windowStart}, ${config.windowEnd}), ${windowDays} days. Each query read the files of one store whose mtime is in one UTC day (or one hour of a day whose query timed out), from the window's start on; a read counts when its call's timestamp is in the window. A session is one transcript file, so a subagent's file is a session of its own, and a file that two queries hold is one session.`,
    "",
    `${attribution.copies.length} copies of ${names.size} names in ${repositories.length} repositories: ` +
      STATUSES.map((status) => `${of(status).length} ${status}`).join(", ") +
      `. ${counted(attribution.readSessions, "session")} read a skill; ${attribution.unattributedSessions} of them read a skill that no copy got.`,
    "",
    "## Stores",
    "",
    "| Store | Context | Earliest record | Files in the store | Earliest read in the window | Sessions with a read | Queries with output | Files read | Bytes read | Lines not JSON | Call ids in two files |",
    "| --- | --- | --- | ---: | --- | ---: | ---: | ---: | ---: | ---: | ---: |"
  );
  for (const store of attribution.stores) {
    const span = spans.find((candidate) => candidate.store === store.store);
    const earliest =
      span === undefined
        ? "not measured"
        : span.earliest_mtime_ms === null
          ? "no record"
          : iso(span.earliest_mtime_ms);
    say(
      `| ${cell(store.store)} | ${cell(store.context)} | ${earliest} | ${span?.files ?? "not measured"} | ${store.earliestRead ?? "no read"} | ${store.readSessions} | ${store.outputs} | ${store.filesRead} | ${store.bytesRead} | ${store.badLines} | ${store.sharedCallIds} |`
    );
  }
  say("");
  for (const store of attribution.stores) {
    const span = spans.find((candidate) => candidate.store === store.store);
    if (span === undefined) {
      say(
        `- ${store.store}: the spans file has no line for it, so its earliest record is not known.`
      );
    } else if (span.earliest_mtime_ms !== null && span.earliest_mtime_ms > windowStart) {
      const days = Math.round(((windowEnd - span.earliest_mtime_ms) / DAY_MS) * 10) / 10;
      say(
        `- ${store.store} starts after the window's start: its records span ${iso(span.earliest_mtime_ms)} to the end time, ${days} of the window's ${windowDays} days.`
      );
    }
    if (store.unpairedSessions > 0) {
      say(
        `- ${store.store}: a call with no result in its file, in ${counted(store.unpairedSessions, "session")}: such a call is not a read, and no copy counts it.`
      );
    }
  }
  say(
    '- The transcript store is not read: the cluster store that a pod reads holds no Claude Code transcript until the Claude Code transcripts are moved into it, so this count holds no Claude Code session. "Claude Code" below lists the skills that leaves out.'
  );
  for (const dir of count.otherStores) {
    say(
      `- The count directory holds ${code(dir)}, which the configuration names no store for; it is not read.`
    );
  }

  say("", "## Queries that failed, were split, or have no output", "");
  if (issues.length === 0)
    say("None: each query of the window has its output, and the run log lists no failure.");
  for (const issue of issues) {
    say(
      `- ${issue.store === "" ? "" : `${issue.store} `}${issue.name === "" ? "" : `${issue.name} `}(${issue.what}): ${issue.detail}`
    );
  }
  if (count.log !== undefined && count.log.ended === undefined) {
    say(
      "",
      "The run log has no END line: the count was still running, so its days after the window's end are not checked."
    );
  }

  say(
    "",
    "## How a read is attributed",
    "",
    "1. The prefix table (prefix-table.tsv): a read goes to the copy that every row whose prefix matches its copy path maps to, when they map to one copy.",
    "2. The store's context: the units (packages, plugins, a host's user skills) that the store's own reads of step 1 come from are the units its context installs. When their installer rows hold exactly one copy of the read's skill name, the read goes to that copy.",
    "3. Otherwise the read is unattributed for its skill name.",
    "",
    "Two copies of one name are a tie when their sessions differ by at most that name's unattributed sessions.",
    "",
    "### The units each store's context installs",
    ""
  );
  for (const store of attribution.stores) {
    if (store.units.length === 0) {
      say(
        `- ${store.store} (${store.context}): none, since no read of this store maps by the prefix table; each of its reads is unattributed.`
      );
      continue;
    }
    say(`- ${store.store} (${store.context}):`);
    for (const unit of store.units) {
      say(
        `  - ${unit.unit}: ${counted(unit.sessions, "session")} read from it; it holds ${counted(unit.copies, "copy", "copies")}`
      );
    }
  }

  const unmappedSessions = attribution.unmapped.reduce((sum, item) => sum + item.sessions, 0);
  say(
    "",
    "### Copy paths the prefix table does not map",
    "",
    `${counted(attribution.unmapped.length, "prefix", "prefixes")}. A session is counted under each prefix it read from (${counted(unmappedSessions, "read session")} over all prefixes).`,
    ""
  );
  if (attribution.unmapped.length > 0) {
    say(
      "| Prefix | Skill | Sessions | Reference sessions | Where they went |",
      "| --- | --- | ---: | ---: | --- |"
    );
    for (const item of attribution.unmapped) {
      const went = item.outcomes
        .map((o) => `${o.outcome} (${counted(o.sessions, "session")})`)
        .join("; ");
      say(
        `| ${cell(code(item.prefix))} | ${cell(item.skills.join(", "))} | ${item.sessions} | ${item.referenceSessions} | ${cell(went)} |`
      );
    }
  }

  say("", "### Ties", "");
  const tied = attribution.copies.filter((copy) => copy.ties.length > 0);
  if (tied.length === 0) say("None.");
  for (const copy of tied) {
    say(
      `- ${copy.name}: ${copy.repository} ${copy.path} (${counted(copy.sessions, "session")}) ties with ${copy.ties.join(", ")}; the name has ${counted(copy.unattributed, "unattributed session")}.`
    );
  }

  say("", "### Skills read that no copy R4 read has", "");
  if (attribution.namesWithoutCopy.length === 0) say("None.");
  for (const item of attribution.namesWithoutCopy) {
    say(
      `- ${item.name}: ${counted(item.sessions, "session")}, ${counted(item.referenceSessions, "reference session")}`
    );
  }

  say(
    "",
    "## Loaded skills",
    "",
    "A skill that code, an agent definition or an installer loads into a session. R6 leaves these out.",
    ""
  );
  const loaded = attribution.copies.filter((copy) => copy.loadedBy.length > 0);
  if (loaded.length === 0) say("None.");
  for (const copy of loaded) {
    say(
      `- ${copy.name} (${copy.repository} ${copy.path}, ${counted(copy.sessions, "session")}): ${copy.loadedBy.join("; ")}`
    );
  }

  say("", "## Skills with no read, per repository", "");
  for (const repository of repositories) {
    const unread = attribution.copies.filter(
      (copy) => copy.repository === repository && copy.status !== "read"
    );
    say(`### ${repository}`, "");
    if (unread.length === 0) say("None.");
    for (const copy of unread) {
      const free =
        copy.unattributed > 0
          ? `; ${counted(copy.unattributed, "session")} read ${copy.name} unattributed`
          : "";
      say(`- ${copy.name} (${copy.path}): ${copy.status}${free}`);
    }
    say("");
  }

  say(
    "## Claude Code",
    "",
    'This count reads no Claude Code session (see "Stores"). Oh My Pi installs a copy when an Oh My Pi session read it (every store holds Oh My Pi sessions), when a store\'s context installs it (the units above), or when an installers.tsv row of host omp installs it (an installer script into Oh My Pi, an Oh My Pi plugins package.json, a package.json with omp.skills, the prepack script). Claude Code installs it when an installers.tsv row of host claude-code does.',
    "",
    "### Not countable yet: only Claude Code installs them, and no Oh My Pi session read them",
    ""
  );
  const notCountable = of("not countable yet");
  if (notCountable.length === 0) say("None.");
  for (const copy of notCountable) say(`- ${copy.name} (${copy.repository} ${copy.path})`);
  say("", "### No Oh My Pi read; Claude Code not counted yet: both hosts install them", "");
  const both = of("no Oh My Pi read; Claude Code not counted yet");
  if (both.length === 0) say("None.");
  for (const copy of both) say(`- ${copy.name} (${copy.repository} ${copy.path})`);

  say(
    "",
    "## Bytes of skill names and descriptions, per repository",
    "",
    "| Repository | Commit | Skills | Name bytes | Description bytes | Total bytes |",
    "| --- | --- | ---: | ---: | ---: | ---: |"
  );
  for (const repository of repositories) {
    const rows = r4.copies.filter((copy) => copy.repository === repository);
    const nameBytes = rows.reduce((sum, copy) => sum + Buffer.byteLength(copy.name, "utf8"), 0);
    const descriptionBytes = rows.reduce((sum, copy) => sum + Number(copy.description_bytes), 0);
    const commit =
      r4.repositories.find((repo) => repo.repository === repository)?.commit ||
      "not in repositories.tsv";
    say(
      `| ${cell(repository)} | ${commit} | ${rows.length} | ${nameBytes} | ${descriptionBytes} | ${nameBytes + descriptionBytes} |`
    );
  }

  say("", "## Rankings", "");
  if (rankings.length === 0) say("None: no --changed file was given.");
  for (const ranking of rankings) {
    const top = ranking.rows
      .slice(0, 5)
      .map((row) => `${row.skill} (${row.sessions})`)
      .join(", ");
    say(
      `- ${ranking.cut} (${ranking.repository}): ${ranking.rows.length} changed skills, in ${code(`${ranking.cut}/ranking.json`)}; first: ${top || "none"}.` +
        (ranking.missing.length > 0
          ? ` R4 read no copy at ${ranking.missing.map(code).join(", ")}: ranked with 0 sessions.`
          : "")
    );
  }
  const unranked = config.cuts.filter((cut) => !rankings.some((ranking) => ranking.cut === cut.id));
  if (unranked.length > 0) {
    say(`- Not ranked (no --changed file): ${unranked.map((cut) => cut.id).join(", ")}.`);
  }
  return `${lines.join("\n")}\n`;
}

// ---------------------------------------------------------------------------------------------
// the command

const USAGE =
  "usage: attribute.ts --config <skill-cuts.json> --count <dir> --copies <dir> --spans <file> --out <dir> [--changed <cut id>=<file>]...";

/** What `main` writes to. */
export interface Output {
  out(text: string): void;
  err(text: string): void;
}

/** Whether `inner` is `outer` or a path inside it. */
function inside(inner: string, outer: string): boolean {
  const relative = path.relative(path.resolve(outer), path.resolve(inner));
  return relative === "" || (!relative.startsWith("..") && !path.isAbsolute(relative));
}

/** The command. Returns the exit status: 0 (the count is written), 1 (an input could not be read
 *  or does not fit), 2 (refused). */
export function main(argv: readonly string[], io: Output): number {
  const refuse = (why: string) => {
    io.err(`attribute: ${why}`);
    return 2;
  };
  let values: {
    config?: string;
    count?: string;
    copies?: string;
    spans?: string;
    out?: string;
    changed?: string[];
  };
  try {
    ({ values } = parseArgs({
      args: [...argv],
      options: {
        config: { type: "string" },
        count: { type: "string" },
        copies: { type: "string" },
        spans: { type: "string" },
        out: { type: "string" },
        changed: { type: "string", multiple: true },
      },
      strict: true,
      allowPositionals: false,
    }));
  } catch (error) {
    return refuse(`${(error as Error).message}. ${USAGE}`);
  }
  const { config: configFile, count: countDir, copies: copiesDir, spans: spansFile, out } = values;
  if (
    configFile === undefined ||
    countDir === undefined ||
    copiesDir === undefined ||
    spansFile === undefined ||
    out === undefined
  ) {
    return refuse(`--config, --count, --copies, --spans and --out are required. ${USAGE}`);
  }
  for (const [flag, dir] of [
    ["--count", countDir],
    ["--copies", copiesDir],
  ] as const) {
    if (inside(out, dir))
      return refuse(`--out ${out} is inside ${flag} ${dir}, which is only read`);
  }
  let config: SkillCutsConfig;
  try {
    config = loadConfig(configFile);
  } catch (error) {
    return refuse((error as Error).message);
  }
  const changed: { cut: string; repository: string; file: string }[] = [];
  for (const item of values.changed ?? []) {
    const at = item.indexOf("=");
    const id = at < 0 ? "" : item.slice(0, at);
    const file = at < 0 ? "" : item.slice(at + 1);
    if (id === "" || file === "")
      return refuse(`--changed ${JSON.stringify(item)} is not <cut id>=<file>`);
    const cut = config.cuts.find((candidate) => candidate.id === id);
    if (cut === undefined) {
      return refuse(
        `--changed names the cut ${JSON.stringify(id)}, which the configuration does not; it names ${config.cuts.map((c) => c.id).join(", ")}`
      );
    }
    if (!/^[A-Za-z0-9._-]+$/.test(id) || id === "." || id === "..") {
      return refuse(`the cut id ${JSON.stringify(id)} cannot name a directory under --out`);
    }
    if (changed.some((entry) => entry.cut === id))
      return refuse(`--changed names the cut ${id} twice`);
    changed.push({ cut: id, repository: cut.repo, file });
  }
  try {
    const count = readCount(
      countDir,
      config.stores.map((store) => store.name)
    );
    const r4 = readR4(copiesDir);
    const spans = parseSpans(readFileSync(spansFile, "utf8"), spansFile);
    const attribution = attribute(config, count.outputs, r4);
    const issues = queryIssues(config, count);
    const rankings = changed.map((item) =>
      rankChanged(
        attribution,
        item.cut,
        item.repository,
        readFileSync(item.file, "utf8").split("\n")
      )
    );
    mkdirSync(out, { recursive: true });
    const written: string[] = [];
    const write = (name: string, text: string) => {
      const file = path.join(out, name);
      mkdirSync(path.dirname(file), { recursive: true });
      writeFileSync(file, text);
      written.push(file);
    };
    write("skill-read-count.tsv", readCountTsv(attribution));
    write(
      "skill-read-count.md",
      readCountMarkdown({ config, attribution, count, issues, spans, r4, rankings })
    );
    for (const ranking of rankings) {
      write(path.join(ranking.cut, "ranking.json"), `${JSON.stringify(ranking.rows, null, 2)}\n`);
    }
    const read = attribution.copies.filter((copy) => copy.status === "read").length;
    io.out(
      `${count.outputs.length} query outputs, ${attribution.copies.length} copies (${read} read), ` +
        `${attribution.readSessions} read sessions (${attribution.unattributedSessions} unattributed), ` +
        `${attribution.unmapped.length} unmapped prefixes, ${issues.length} queries failed, split or without output\n` +
        written.map((file) => `${file}\n`).join("")
    );
    return 0;
  } catch (error) {
    io.err(`attribute: ${(error as Error).message}`);
    return 1;
  }
}

if (import.meta.main) {
  process.exit(
    main(Bun.argv.slice(2), {
      out: (text) => process.stdout.write(text),
      err: (text) => process.stderr.write(`${text}\n`),
    })
  );
}
