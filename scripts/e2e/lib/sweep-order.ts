/**
 * The search for a shell stack's start that refuses, or creates its Postgres, before it removes
 * the Postgres of dead stacks.
 *
 * R3 of pull request #4236: "The next start of a test stack removes each Postgres of a stack that
 * it can show dead. … It leaves a Postgres whose stack it cannot judge, and it names that Postgres
 * in one line." A shell stack's start removes them with `remove_leftover_postgres`
 * (lib/leftovers.sh). Three checks of a start refuse it because of what a dead run left, and each
 * ends the start through the driver's `fail`: `refuse_leftovers` (a dead run's containers and
 * processes), `ports_free` (a port a dead run's container still publishes) and the skill scenarios'
 * `lock_services` (the lock file a dead batch leaves). A start that runs one of them before its
 * removal never reaches the removal while the dead run's leftovers stay, so the dead stack's
 * Postgres stays and no line names it, at every later start too. A start that marks its own
 * Postgres (`postgres_mark`, the line before its create) before its removal creates it beside
 * the dead stacks' Postgres, which a name the dead run used can refuse.
 *
 * The search reads every shell script under `scripts/` and `packages/` of ROOT and follows each
 * script's own order of calls from its top-level lines, into each function it or a library it
 * sources (`. ".../lib/<name>.sh"`) defines, at the start of a line as `name() {`. A script whose
 * calls reach the removal is a driver; a driver whose calls reach one of the three refusals, or a
 * mark, before the removal is a finding. It reads text, not branches: a call in either arm of an
 * `if` counts where it is written.
 *
 *   bun scripts/e2e/lib/sweep-order.ts [ROOT]
 *
 * prints one `DRIVER` line for each driver, one `FOUND` line for each refusal or mark before its
 * removal, and a `COUNT` line, and exits 0 with no finding, 1 with a finding, and 2, printing
 * UNKNOWN, where it read no driver at all, since a search that read nothing has passed nothing.
 */

import { type Dirent, readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";

/** The checks that refuse a start because of what a dead run left. */
export const refusals = ["refuse_leftovers", "ports_free", "lock_services"] as const;
/** The removal of the Postgres of dead stacks. */
export const sweep = "remove_leftover_postgres";
/** The mark of a start's own Postgres, which the line after it creates the container with. */
export const mark = "postgres_mark";
/** What must not come before the removal. */
const early = new Set<string>([...refusals, mark]);

/** A call the search follows: a refusal, the removal or a mark, with the file and line of it. */
export interface Call {
  readonly name: string;
  readonly at: string;
}

/** A line of code: its text and where it is. */
interface Line {
  readonly text: string;
  readonly at: string;
}

/** What one script holds: its top-level lines, its functions and the libraries it sources. */
interface Script {
  readonly top: readonly Line[];
  readonly functions: ReadonlyMap<string, readonly Line[]>;
  readonly libraries: readonly string[];
}

/** One driver whose calls reach a refusal or a mark before the removal. */
export interface Finding {
  readonly driver: string;
  /** Each refusal and mark before its first removal. */
  readonly before: readonly Call[];
  readonly sweep: string;
}

/** A driver: a script whose calls reach the removal. */
export interface Driver {
  readonly driver: string;
  /** Where its first removal is. */
  readonly sweep: string;
  /** Every refusal and removal its calls reach, in the order it reaches them. */
  readonly calls: readonly Call[];
  /** The functions it or its libraries define whose calls reach a refusal or the removal. */
  readonly reaching: readonly string[];
}

/** What the search read and found. */
export interface Census {
  readonly drivers: readonly Driver[];
  readonly findings: readonly Finding[];
}

// A function as the scripts define one, at the start of a line (lib/script-functions.ts).
const opener = /^([A-Za-z_][A-Za-z0-9_]*)\(\) \{(.*)$/;
// A one-line definition: its body ends in the closing brace, perhaps with a redirection after it.
const oneLine = /(?:^|[\s;])\}(?:\s*[<>&|].*)?\s*$/;
// The closing line of a definition: a `}` at the start of a line.
const closer = /^\}/;
// A library of scripts/e2e/lib a script sources (lib/script-functions.ts).
const sourced = /^\s*\. ".*\/lib\/([A-Za-z0-9_.-]+\.sh)"\s*$/;
// A heredoc whose body is text, not code: `<<WORD`, `<<-WORD`, `<<'WORD'`, `<<"WORD"`.
const heredoc = /(?:^|[^<])<<(-?)\s*(['"]?)([A-Za-z_][A-Za-z0-9_]*)\2/;

/** Reads `path`'s lines into its top-level lines, its functions and the libraries it sources. */
function parse(path: string, shown: string): Script {
  const lines = readFileSync(path, "utf8").split("\n");
  const top: Line[] = [];
  const functions = new Map<string, Line[]>();
  const libraries: string[] = [];
  let body: Line[] | undefined;
  let until: { word: string; tabs: boolean } | undefined;
  lines.forEach((text, index) => {
    const at = `${shown}:${index + 1}`;
    if (until !== undefined) {
      if ((until.tabs ? text.replace(/^\t+/, "") : text) === until.word) until = undefined;
      return;
    }
    const library = sourced.exec(text);
    if (library !== null) libraries.push(library[1]);
    const doc = /^\s*#/.test(text) ? null : heredoc.exec(text);
    if (doc !== null) until = { word: doc[3], tabs: doc[1] === "-" };
    if (body === undefined) {
      const definition = opener.exec(text);
      if (definition === null) {
        top.push({ text, at });
        return;
      }
      const [, name, rest] = definition;
      if (oneLine.test(rest) && doc === null) {
        functions.set(name, [{ text: rest, at }]);
        return;
      }
      body = [{ text: rest, at }];
      functions.set(name, body);
      return;
    }
    if (closer.test(text)) {
      body = undefined;
      return;
    }
    body.push({ text, at });
  });
  return { top, functions, libraries };
}

/** Each name of `names` that `text` calls, in the order it calls them: a name in a command's
 *  place, at the start of the line or after `;`, `&`, `|`, `(`, `{`, `!`, `$(`, `then`, `do`,
 *  `else`, `if`, `while` or `until`, and not where it defines a function. */
function callsIn(text: string, names: ReadonlySet<string>): string[] {
  if (/^\s*#/.test(text)) return [];
  const found: string[] = [];
  const command =
    /(?:^|[;&|({!`]|\$\(|\b(?:then|do|else|elif|if|while|until)\s)\s*([A-Za-z_][A-Za-z0-9_]*)(?=$|[\s;&|)`])(?!\s*\(\))/g;
  for (const match of text.matchAll(command)) {
    if (names.has(match[1])) found.push(match[1]);
  }
  return found;
}

/** The scripts under `scripts/` and `packages/` of `root`, outside node_modules, by their paths
 *  under `root`. */
function shellScripts(root: string): string[] {
  const found: string[] = [];
  const walk = (directory: string) => {
    let entries: Dirent[];
    try {
      entries = readdirSync(directory, { withFileTypes: true });
    } catch {
      return;
    }
    for (const entry of entries) {
      if (entry.name === "node_modules" || entry.name === ".git") continue;
      const path = join(directory, entry.name);
      if (entry.isDirectory()) walk(path);
      else if (entry.isFile() && entry.name.endsWith(".sh")) found.push(relative(root, path));
    }
  };
  walk(join(root, "scripts"));
  walk(join(root, "packages"));
  return found.sort();
}

/** The search over the scripts of `root`. */
export function sweepOrder(root: string): Census {
  const cache = new Map<string, Script>();
  const read = (shown: string): Script | undefined => {
    if (!cache.has(shown)) {
      try {
        cache.set(shown, parse(join(root, shown), shown));
      } catch {
        return undefined;
      }
    }
    return cache.get(shown);
  };
  const primitive = new Set<string>([...early, sweep]);
  const drivers: Driver[] = [];
  const findings: Finding[] = [];
  for (const shown of shellScripts(root)) {
    const script = read(shown);
    if (script === undefined) continue;
    // The functions the script can call: its own first, then each library it sources, and each
    // library those source, in that order.
    const functions = new Map<string, readonly Line[]>();
    const pending = [shown];
    const seen = new Set<string>();
    for (let next = pending.shift(); next !== undefined; next = pending.shift()) {
      if (seen.has(next)) continue;
      seen.add(next);
      const it = read(next);
      if (it === undefined) continue;
      for (const [name, body] of it.functions) if (!functions.has(name)) functions.set(name, body);
      for (const library of it.libraries) pending.push(`scripts/e2e/lib/${library}`);
    }
    const names = new Set<string>([...primitive, ...functions.keys()]);
    // The calls the top-level lines make, in order, with each function they call followed into,
    // each function's calls read once (a function that calls itself again adds nothing more).
    const memo = new Map<string, readonly Call[]>();
    const follow = (lines: readonly Line[]): Call[] => {
      const out: Call[] = [];
      for (const line of lines) {
        for (const name of callsIn(line.text, names)) {
          if (primitive.has(name)) out.push({ name, at: line.at });
          else if (memo.has(name)) out.push(...(memo.get(name) ?? []));
          else {
            memo.set(name, []);
            const inner = follow(functions.get(name) ?? []);
            memo.set(name, inner);
            out.push(...inner);
          }
        }
      }
      return out;
    };
    const calls = follow(script.top);
    const first = calls.findIndex((call) => call.name === sweep);
    if (first < 0) continue;
    const reaching = [...memo].filter(([, inner]) => inner.length > 0).map(([name]) => name);
    drivers.push({ driver: shown, sweep: calls[first].at, calls, reaching });
    const before = calls.slice(0, first).filter((call) => early.has(call.name));
    if (before.length > 0) findings.push({ driver: shown, before, sweep: calls[first].at });
  }
  return { drivers, findings };
}

if (import.meta.main) {
  const root = process.argv[2] ?? join(import.meta.dir, "..", "..", "..");
  const { drivers, findings } = sweepOrder(root);
  for (const { driver, sweep: at } of drivers)
    console.log(`DRIVER ${driver} removes first at ${at}`);
  for (const finding of findings) {
    for (const call of finding.before) {
      console.log(
        `FOUND ${finding.driver}: ${call.name} at ${call.at}, before the removal at ${finding.sweep}`
      );
    }
  }
  if (drivers.length === 0) {
    console.log(`UNKNOWN: no script under ${root} reaches ${sweep}, so the search read no driver`);
    process.exit(2);
  }
  const kinds = [...early].map(
    (name) =>
      `${name}=${findings.filter((finding) => finding.before.some((call) => call.name === name)).length}`
  );
  console.log(`COUNT drivers=${drivers.length} found=${findings.length} ${kinds.join(" ")}`);
  process.exit(findings.length === 0 ? 0 : 1);
}
