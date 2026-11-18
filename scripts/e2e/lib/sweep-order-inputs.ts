/**
 * The input-flow analysis of the order search (lib/sweep-order.ts): which variables of a driver
 * and its libraries can hold a command's output, and what a check reads that is not one of the
 * start's inputs, over the scripts lib/sweep-order-shell.ts parsed.
 */

import {
  alone,
  type Command,
  type Definition,
  declarers,
  eachCommand,
  heredocOp,
  type List,
  nameOf,
  type Script,
  type Word,
} from "./sweep-order-shell";

// ---- What a check reads -------------------------------------------------------------------------

/** The commands a check runs, or the words it reads, in the function whose positional parameters
 *  they read (null at the top level). */
export type Guard =
  | { readonly list: List; readonly ctx: string | null }
  | { readonly words: readonly Word[]; readonly ctx: string | null };

// The commands that read nothing of the machine: they test or print what they are given, or set a
// variable or a parameter, or work on the text of a path (`dirname`, `basename`), or print the
// start's own working directory (`pwd`) or the names of its own shell (`compgen`). `[`, `[[` and
// `test` only with no test of a file. `cat` with no operand reads only its standard input, as a
// usage text written as a here-document does; `cd`, `.` and `source` only of a path the start's
// inputs name (`Flow.rooted`), such as its own checkout.
const inputCommands: Record<string, true> = {
  "[": true,
  "[[": true,
  test: true,
  echo: true,
  printf: true,
  ":": true,
  true: true,
  false: true,
  local: true,
  declare: true,
  typeset: true,
  readonly: true,
  export: true,
  unset: true,
  shift: true,
  return: true,
  exit: true,
  break: true,
  continue: true,
  set: true,
  shopt: true,
  let: true,
  dirname: true,
  basename: true,
  pwd: true,
  compgen: true,
};
// The commands that read only a path of the start's inputs where each operand is one (`Flow.rooted`).
const rootedCommands: Record<string, true> = { cd: true, ".": true, source: true };
const fileTests = /^-([abcdefgGhkLNOprsStuwx]|nt|ot|ef)$/;
// The variables the shell sets from what a command did, whatever the scripts assign.
const shellSet = ["BASH_REMATCH", "REPLY", "MAPFILE", "OPTARG", "PIPESTATUS", "COPROC"];
const identifier = /^[A-Za-z_][A-Za-z0-9_]*$/;

/** The variables of a driver and its libraries that can hold a command's output, and the
 *  functions whose positional parameters can: a data flow over every assignment, to a fixed
 *  point. */
export class Flow {
  private readonly tainted = new Set<string>(shellSet);
  // The functions whose positional parameters can hold a command's output; "" is the script's.
  private readonly impureArgs = new Set<string>();
  private readonly assigns: { name: string; value: Word; ctx: string | null }[] = [];
  private readonly calls: { fn: string; args: Word[]; ctx: string | null }[] = [];
  private readonly positional: { value: Word; ctx: string | null }[] = [];
  // A function that sets the variable its Nth argument names (`printf -v "$1"`, `read "$2"`).
  private readonly dynamic: { fn: string; index: number }[] = [];

  constructor(
    scripts: readonly Script[],
    private readonly functions: ReadonlyMap<string, readonly Definition[]>
  ) {
    for (const script of scripts)
      eachCommand(script.top, null, (c, ctx) => this.collect(c, ctx, functions));
    const called = new Set(this.calls.map((c) => c.fn));
    // A function that no command of these files calls gets what nobody can read here.
    for (const name of functions.keys()) if (!called.has(name)) this.impureArgs.add(name);
    for (let changed = true; changed; ) {
      changed = false;
      const add = (set: Set<string>, name: string) => {
        if (set.has(name)) return;
        set.add(name);
        changed = true;
      };
      for (const a of this.assigns) if (this.impure(a.value, a.ctx)) add(this.tainted, a.name);
      for (const c of this.calls)
        if (c.args.some((w) => this.impure(w, c.ctx))) add(this.impureArgs, c.fn);
      for (const p of this.positional)
        if (this.impure(p.value, p.ctx)) add(this.impureArgs, p.ctx ?? "");
      for (const d of this.dynamic) {
        for (const c of this.calls) {
          const w = c.fn === d.fn ? c.args[d.index - 1] : undefined;
          if (w !== undefined && identifier.test(w.text)) add(this.tainted, w.text);
        }
      }
    }
  }

  /** A variable that a command sets: tainted by its name, or by the name its caller passes. */
  private target(w: Word | undefined, ctx: string | null): void {
    if (w === undefined) return;
    if (identifier.test(w.text)) this.tainted.add(w.text);
    const positional = /^"?\$\{?([1-9])\}?"?$/.exec(w.text);
    if (positional !== null && ctx !== null)
      this.dynamic.push({ fn: ctx, index: Number(positional[1]) });
  }

  private collect(
    c: Command,
    ctx: string | null,
    functions: ReadonlyMap<string, readonly Definition[]>
  ): void {
    if (c.k === "for" && c.name !== null) {
      for (const value of c.words) this.assigns.push({ name: c.name, value, ctx });
      return;
    }
    if (c.k === "arith" || c.k === "for") {
      const text = c.k === "arith" ? c.text : c.words.map((w) => w.text).join(" ");
      const set =
        /([A-Za-z_][A-Za-z0-9_]*)\s*(?:[-+*/%&|^]?=(?!=)|\+\+|--)|(?:\+\+|--)\s*([A-Za-z_][A-Za-z0-9_]*)/g;
      for (const m of text.matchAll(set)) this.tainted.add(m[1] ?? m[2]);
      return;
    }
    if (c.k !== "simple") return;
    const name = nameOf(c.words);
    const args = c.words.slice(1);
    const nameRef = args.some((w) => /^-[a-zA-Z]*n/.test(w.text));
    for (const a of c.assigns) {
      if (name !== undefined && declarers[name] === true && nameRef) this.tainted.add(a.name);
      for (const value of a.values) this.assigns.push({ name: a.name, value, ctx });
    }
    if (name === undefined) return;
    if (functions.has(name)) this.calls.push({ fn: name, args, ctx });
    // The words after the options, where `valued` names the options that take a value.
    const operands = (valued: string) => {
      const rest: Word[] = [];
      for (let i = 0; i < args.length; i++) {
        const flag = /^-([A-Za-z]+)$/.exec(args[i].text);
        if (flag === null) rest.push(args[i]);
        else if (valued.includes(flag[1].slice(-1))) {
          if (name === "read" && flag[1].endsWith("a")) this.target(args[i + 1], ctx);
          i++;
        }
      }
      return rest;
    };
    switch (name) {
      case "read":
        for (const w of operands("adinNptu")) this.target(w, ctx);
        return;
      case "mapfile":
      case "readarray": {
        const rest = operands("dnOsuCc");
        if (rest.length === 0) this.tainted.add("MAPFILE");
        else this.target(rest[rest.length - 1], ctx);
        return;
      }
      case "printf":
        if (args[0]?.text === "-v") this.target(args[1], ctx);
        return;
      case "getopts":
        this.target(args[1], ctx);
        return;
      case "let":
        for (const w of args) {
          const m = /^["']?([A-Za-z_][A-Za-z0-9_]*)/.exec(w.text);
          if (m !== null) this.tainted.add(m[1]);
        }
        return;
      case "set": {
        const dashes = args.findIndex((w) => w.text === "--");
        if (dashes >= 0)
          for (const value of args.slice(dashes + 1)) this.positional.push({ value, ctx });
        return;
      }
    }
  }

  /** Whether a word can hold a command's output: a command substitution in it that reads more
   *  than the start's inputs (`commandReads`, a call of a function counted as such a read), or a
   *  parameter that can. `$(cd "$(dirname "$0")/../.." && pwd)` reads only the place of the
   *  checkout the start runs from, and `$(compgen -e)` only the names of its own environment. */
  impure(w: Word, ctx: string | null): boolean {
    return (
      w.subs.some((sub) => {
        const out = new Set<string>();
        eachCommand(sub, ctx, (c, inner) =>
          commandReads(c, inner, out, this, (name) =>
            this.functions.has(name) ? [name] : undefined
          )
        );
        return out.size > 0;
      }) || w.vars.some((v) => this.variable(v, ctx) !== undefined)
    );
  }

  /** Whether the word `w` names a path that the start's inputs name: it starts with an expansion,
   *  and it holds nothing but inputs (`impure`). `"$(dirname "$0")/.."`, `"$root/scripts"` and
   *  `"$here/../.."` are such words, where `root` and `here` hold only the checkout's place; an
   *  absolute path written in the script is not, since a dead run can leave a file there. */
  rooted(w: Word, ctx: string | null): boolean {
    return /^"?\$/.test(w.text) && !this.impure(w, ctx);
  }

  /** Why a parameter can hold a command's output, or undefined where it holds only inputs. */
  variable(v: string, ctx: string | null): string | undefined {
    if (v === "?" || v === "!") return `$${v}`;
    if (v.startsWith("!")) return `\${${v}}`;
    if (/^[1-9@*#]$/.test(v))
      return this.impureArgs.has(ctx ?? "") ? `$${v} of ${ctx ?? "the script"}` : undefined;
    if (v === "$" || v === "-" || v === "0") return undefined;
    return this.tainted.has(v) ? `$${v}` : undefined;
  }
}

/** A driver's functions, what its variables hold, and what each check reads. */
export class Reader {
  private readonly bodies = new Map<string, readonly string[]>();
  private readonly reporters = new Map<string, boolean>();

  constructor(
    readonly functions: ReadonlyMap<string, readonly Definition[]>,
    readonly flow: Flow
  ) {}

  /** What the checks read that is not an input. An empty answer: they read only inputs. */
  reads(guards: readonly Guard[]): string[] {
    const out = new Set<string>();
    for (const g of guards) {
      if ("list" in g) this.list(g.list, g.ctx, out);
      else {
        for (const w of g.words) {
          for (const sub of w.subs) this.list(sub, g.ctx, out);
          this.vars(w, g.ctx, out);
        }
      }
    }
    return [...out];
  }

  private list(list: List, ctx: string | null, out: Set<string>): void {
    eachCommand(list, ctx, (c, inner) => this.command(c, inner, out));
  }

  private vars(w: Word, ctx: string | null, out: Set<string>): void {
    for (const v of w.vars) {
      const why = this.flow.variable(v, ctx);
      if (why !== undefined) out.add(why);
    }
  }

  /** One command alone: its words' parameters, its redirections and, for a function, its body;
   *  eachCommand reaches each command inside it. */
  private command(c: Command, ctx: string | null, out: Set<string>): void {
    commandReads(c, ctx, out, this.flow, (name) =>
      this.functions.has(name)
        ? this.body(name).map((r) => (r.includes(" in ") ? r : `${r} in ${name}`))
        : undefined
    );
  }

  /** What a function's body reads, each of its definitions. */
  private body(name: string): readonly string[] {
    const known = this.bodies.get(name);
    if (known !== undefined) return known;
    this.bodies.set(name, []);
    const out = new Set<string>();
    for (const d of this.functions.get(name) ?? []) this.list(alone(d.body), name, out);
    const reads = [...out];
    this.bodies.set(name, reads);
    return reads;
  }

  /** Whether a function always ends the start: each of its definitions has a body in braces that
   *  runs, as a command of its own, an `exit` whose status is not 0 or a call of such a function,
   *  and holds no `return` before it. A body in parentheses ends only its subshell. */
  reporter(name: string): boolean {
    const known = this.reporters.get(name);
    if (known !== undefined) return known;
    this.reporters.set(name, false);
    const definitions = this.functions.get(name) ?? [];
    const ends =
      definitions.length > 0 &&
      definitions.every(
        (d) => d.body.k === "group" && !d.body.subshell && this.endsAlways(d.body.body)
      );
    this.reporters.set(name, ends);
    return ends;
  }

  private endsAlways(body: List): boolean {
    for (const ao of body) {
      let returns = false;
      eachCommand([ao], null, (c) => {
        if (c.k === "simple" && nameOf(c.words) === "return") returns = true;
      });
      if (returns) return false;
      const c = ao.pipelines.length === 1 ? ao.pipelines[0].commands : [];
      const name = c.length === 1 && c[0].k === "simple" ? nameOf(c[0].words) : undefined;
      if (name === undefined || c[0].k !== "simple") continue;
      if (name === "exit" && c[0].words[1]?.text !== "0") return true;
      if (this.functions.has(name) && this.reporter(name)) return true;
    }
    return false;
  }
}

/** What one command alone reads that is not an input, added to `out`: its words' parameters
 *  (`flow.variable`), its redirections, and its program, unless that is one of `inputCommands` (a
 *  file test aside), `cat` with no operand, or one of `rootedCommands` whose every operand is a
 *  path of the start's inputs (`flow.rooted`); for a function, what `body` answers of its body,
 *  or undefined where the name is no function. */
function commandReads(
  c: Command,
  ctx: string | null,
  out: Set<string>,
  flow: Flow,
  body: (name: string) => readonly string[] | undefined
): void {
  const vars = (w: Word) => {
    for (const v of w.vars) {
      const why = flow.variable(v, ctx);
      if (why !== undefined) out.add(why);
    }
  };
  if (c.k === "function") return;
  if (c.k === "arith") {
    for (const v of c.vars) {
      const why = flow.variable(v, ctx);
      if (why !== undefined) out.add(why);
    }
    return;
  }
  for (const r of c.redirects) {
    const dup = /[<>]&$/.test(r.op) && /^(\d+-?|-)$/.test(r.target.text);
    const quiet = /^["']?\/dev\/(null|stdout|stderr|fd\/\d+)["']?$/.test(r.target.text);
    if (/<<<$/.test(r.op)) vars(r.target);
    else if (!heredocOp.test(r.op) && !dup && !quiet) out.add(`${r.op} ${r.target.text}`);
  }
  if (c.k === "for") for (const w of c.words) vars(w);
  if (c.k === "case") for (const w of [c.subject, ...c.arms.flatMap((a) => a.patterns)]) vars(w);
  if (c.k !== "simple") return;
  for (const a of c.assigns) for (const w of a.values) vars(w);
  for (const w of c.words) vars(w);
  let words = c.words;
  if (words[0]?.text === "command") {
    if (words[1]?.text === "-v" || words[1]?.text === "-V") return;
    words = words.slice(words[1]?.text === "-p" ? 2 : 1);
  }
  if (words.length === 0) return;
  const name = nameOf(words);
  const called = name === undefined ? undefined : body(name);
  if (name === undefined) out.add(`a command named by ${words[0].text}`);
  else if (called !== undefined) {
    for (const r of called) out.add(r);
  } else if (name === "exec" && words.length === 1) {
    // `exec` with redirections alone: the redirections above are what it reads.
  } else if (name === "cat" && words.length === 1) {
    // `cat` with no operand reads its standard input, whose redirection is read above.
  } else if (rootedCommands[name] === true) {
    const operands = words.slice(1).filter((w) => w.text !== "--");
    if (operands.length === 0 || !operands.every((w) => flow.rooted(w, ctx))) out.add(name);
  } else if (inputCommands[name] !== true) out.add(name);
  else if (name === "[" || name === "[[" || name === "test") {
    for (const w of words) if (fileTests.test(w.text)) out.add(`${name} ${w.text}`);
  }
}
