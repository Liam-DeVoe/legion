/**
 * The search for a shell stack's start that can refuse, or creates its Postgres, before it
 * removes the Postgres of dead stacks.
 *
 * R3 of pull request #4236: "The next start of a test stack removes each Postgres of a stack that
 * it can show dead. … It leaves a Postgres whose stack it cannot judge, and it names that Postgres
 * in one line." A shell stack's start removes them with `remove_leftover_postgres`
 * (lib/leftovers.sh). A start that ends before its removal because of what a dead run left (a
 * port its container still publishes, a lock a pane of it still holds, a container, a process, a
 * file of the run) ends the same way at every later start while the leftovers stay, so the dead
 * stack's Postgres stays and no line names it. A start that marks its own Postgres
 * (`postgres_mark`, the line before its create) before its removal creates it beside the dead
 * stacks' Postgres, which a name the dead run used can refuse.
 *
 * The search reads a refusal by what it does, not by its name. A refusal is a command that ends
 * the start: an `exit` whose status is not 0; a `return` whose status is not 0 in a function the
 * start calls as a command of its own, which `set -e` makes the start's end; and a call of a
 * function that always ends the start, such as `fail`: one whose body runs such an `exit` as a
 * command of its own and holds no `return`. Its check is each thing that decides whether it runs:
 * the condition of each `if`, `elif`, `while` and `until` around it, the words of each `for` and
 * `case` around it, each command before it in its `&&` and `||` list and in its pipeline, each
 * `return` before it in its function, and the same for each call on its way to it.
 *
 * The rule. A check reads only the start's inputs where each of its commands is `[`, `[[` or
 * `test` with no test of a file, `command -v`, `echo`, `printf`, `:`, `true`, `false` or a builtin
 * that sets a variable or a parameter, with no redirection but to /dev/null or to a descriptor, and
 * where each variable it reads is an input: one that no assignment in the driver or its libraries
 * gives a command's output (a command substitution, `read`, `mapfile`, `printf -v`, a `for` over
 * such words, `$?`, `$!` or an indirect `${!name}`), or a positional parameter that every call
 * gives such words. So a check of an input, of a tool on PATH or of the model route can come
 * before the removal. Every other check can read what a dead run left, as far as a program can
 * tell, and so can a refusal that no check decides: the removal must come before it.
 *
 * The search reads every shell script under `scripts/` and `packages/` of ROOT as bash parses it:
 * each command after `;`, `&&`, `||`, `|`, `then`, `do`, `else`, `elif`, `if`, `while`, `until`,
 * `!`, `{`, `(`, `$(` or a backquote, on one line or on many. It follows the start from each
 * script's top-level commands into each function the script or a library it sources
 * (`. ".../lib/<name>.sh"`) defines, and into each library's own top-level commands where the
 * script sources it. A script whose start reaches the removal is a driver. A driver is a finding
 * where one path of its start reaches a refusal of that kind, or a mark, before the removal: a
 * removal in an arm of an `if` or a `case` counts after it only where every arm removes. A `case`
 * on an input one of whose arms reaches the removal is the script's choice of command (rig.sh's
 * `batch`), so the search reads only the arms that reach the removal or a mark.
 *
 *   bun scripts/e2e/lib/sweep-order.ts [ROOT]
 *
 * prints one `DRIVER` line for each driver, one `FOUND` line for each refusal or mark before its
 * removal, with its file and line and what its check reads, and a `COUNT` line, and exits 0 with
 * no finding and 1 with a finding. It exits 2, printing UNKNOWN, where it read no driver at all,
 * since a search that read nothing has passed nothing, and where it could not parse a script that
 * names the removal or sources lib/leftovers.sh, or a library a driver sources (an `UNREAD` line
 * names each script it could not parse).
 */

import { type Dirent, readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";

import { type Call, orderOf, sweep } from "./sweep-order-policy";
import { type Definition, readScript, type Script, Unparsed } from "./sweep-order-shell";

export { type Call, mark, sweep } from "./sweep-order-policy";

/** One driver whose start reaches a refusal or a mark before the removal on one of its paths. */
export interface Finding {
  readonly driver: string;
  /** Each refusal and mark before the removal. */
  readonly before: readonly Call[];
  /** Where its first removal is. */
  readonly sweep: string;
}

/** A driver: a script whose start reaches the removal. */
export interface Driver {
  readonly driver: string;
  /** Where its first removal is. */
  readonly sweep: string;
  /** Every removal, mark and refusal its start reaches, in the order of its text. */
  readonly calls: readonly Call[];
}

/** A script the search could not parse, and whether that leaves a driver unread. */
export interface Unread {
  readonly script: string;
  readonly reason: string;
  readonly matters: boolean;
}

/** What the search read and found. */
export interface Census {
  readonly drivers: readonly Driver[];
  readonly findings: readonly Finding[];
  readonly unread: readonly Unread[];
}

// ---- The search ---------------------------------------------------------------------------------

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
  // Each script by its path under the root: parsed, unparsed, or no file there (a library path
  // that names no file of scripts/e2e/lib).
  const cache = new Map<string, Script | Unparsed | undefined>();
  const read = (shown: string): Script | Unparsed | undefined => {
    if (!cache.has(shown)) {
      let script: Script | Unparsed | undefined;
      let text: string | undefined;
      try {
        text = readFileSync(join(root, shown), "utf8");
      } catch {}
      if (text !== undefined) {
        try {
          script = readScript(text, shown);
        } catch (error) {
          script =
            error instanceof Unparsed
              ? error
              : new Unparsed(`${shown}: ${error instanceof Error ? error.message : String(error)}`);
        }
      }
      cache.set(shown, script);
    }
    return cache.get(shown);
  };
  const drivers: Driver[] = [];
  const findings: Finding[] = [];
  const unread = new Map<string, Unread>();
  const unreadable = (script: string, reason: string, matters: boolean) => {
    const known = unread.get(script);
    unread.set(script, { script, reason, matters: matters || (known?.matters ?? false) });
  };
  for (const shown of shellScripts(root)) {
    const own = read(shown);
    if (own instanceof Unparsed) {
      const text = readFileSync(join(root, shown), "utf8");
      unreadable(shown, own.message, text.includes(sweep) || text.includes("leftovers.sh"));
      continue;
    }
    if (own === undefined) continue;
    // The functions the script can call: its own, then each library it sources, and each library
    // those source, in that order.
    const scripts = new Map<string, Script>();
    const functions = new Map<string, Definition[]>();
    const pending = [shown];
    let blind: string | undefined;
    for (let next = pending.shift(); next !== undefined; next = pending.shift()) {
      if (scripts.has(next)) continue;
      const it = read(next);
      if (it instanceof Unparsed) {
        unreadable(next, it.message, true);
        blind = next;
        continue;
      }
      if (it === undefined) continue;
      scripts.set(next, it);
      for (const d of it.definitions) functions.set(d.name, [...(functions.get(d.name) ?? []), d]);
      pending.push(...it.libraries);
    }
    if (blind !== undefined)
      unreadable(shown, `${shown}: sources ${blind}, which the search cannot parse`, true);
    const order = orderOf(own, scripts, functions);
    if (order === undefined) continue;
    drivers.push({ driver: shown, sweep: order.sweep, calls: order.calls });
    if (order.before.length > 0)
      findings.push({ driver: shown, before: order.before, sweep: order.sweep });
  }
  return { drivers, findings, unread: [...unread.values()] };
}

if (import.meta.main) {
  const root = process.argv[2] ?? join(import.meta.dir, "..", "..", "..");
  const { drivers, findings, unread } = sweepOrder(root);
  for (const { driver, sweep: at } of drivers)
    console.log(`DRIVER ${driver} removes first at ${at}`);
  for (const finding of findings) {
    for (const c of finding.before) {
      const via = c.via.length > 0 ? ` through ${c.via.join(", ")}` : "";
      const reached = c.from === c.at ? via : `, reached from ${c.from}${via}`;
      const reads = c.kind === "refusal" ? `, whose check reads ${c.reads.join("; ")}` : "";
      console.log(
        `FOUND ${finding.driver}: ${c.name} at ${c.at}${reached}${reads}, before the removal at ${finding.sweep}`
      );
    }
  }
  for (const u of unread) console.log(`UNREAD ${u.script}: ${u.reason}`);
  const blind = unread.filter((u) => u.matters);
  if (drivers.length === 0 || blind.length > 0) {
    console.log(
      drivers.length === 0
        ? `UNKNOWN: no script under ${root} reaches ${sweep}, so the search read no driver`
        : `UNKNOWN: the search could not parse ${blind.map((u) => u.script).join(", ")}, which a driver is or calls`
    );
    process.exit(2);
  }
  const before = findings.flatMap((f) => f.before);
  const refusals = before.filter((c) => c.kind === "refusal").length;
  console.log(
    `COUNT drivers=${drivers.length} found=${findings.length} refusals=${refusals} marks=${before.length - refusals}`
  );
  process.exit(findings.length === 0 ? 0 : 1);
}
