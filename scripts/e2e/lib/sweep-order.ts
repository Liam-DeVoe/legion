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

/** The removal of the Postgres of dead stacks. */
export const sweep = "remove_leftover_postgres";
/** The mark of a start's own Postgres, which the line after it creates the container with. */
export const mark = "postgres_mark";

/** A removal, a mark or a refusal that a driver's start reaches. */
export interface Call {
  readonly kind: "removal" | "mark" | "refusal";
  /** The removal's or the mark's name, or the command that ends the start: `exit`, `return` or
   *  a function that always ends it, such as `fail`. */
  readonly name: string;
  /** The file and line of that command. */
  readonly at: string;
  /** The file and line of the driver's own command through which the start reaches it. */
  readonly from: string;
  /** The functions on the way to it, the outermost first. */
  readonly via: readonly string[];
  /** For a refusal: what its check reads that is not one of the start's inputs. */
  readonly reads: readonly string[];
  /** The file and line of the first command of the branch that ends the start: the branch of
   *  the `if`, the `case`, the loop or the `&&` or `||` list the command is in. */
  readonly begins: string;
}

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

// ---- The shell's words, commands and lists, as bash parses them ----------------------------------

/** A word: its text, where it starts, the commands of each substitution in it, and each parameter
 *  it expands (a name, a digit or a special parameter, and `!name` for an indirect one). */
interface Word {
  readonly text: string;
  readonly at: string;
  readonly subs: List[];
  readonly vars: string[];
}
interface Redirect {
  readonly op: string;
  readonly target: Word;
}
/** NAME=VALUE before a command or as an argument of `local`, `declare` and the like; each word
 *  of an array is a value. */
interface Assignment {
  readonly name: string;
  readonly values: Word[];
}
type Command =
  | {
      readonly k: "simple";
      readonly at: string;
      readonly assigns: Assignment[];
      readonly words: Word[];
      readonly redirects: Redirect[];
    }
  | {
      readonly k: "group";
      readonly at: string;
      readonly subshell: boolean;
      readonly body: List;
      readonly redirects: Redirect[];
    }
  | {
      readonly k: "if";
      readonly at: string;
      readonly arms: { readonly cond: List | null; readonly body: List }[];
      readonly redirects: Redirect[];
    }
  | {
      readonly k: "loop";
      readonly at: string;
      readonly cond: List;
      readonly body: List;
      readonly redirects: Redirect[];
    }
  | {
      readonly k: "for";
      readonly at: string;
      readonly name: string | null;
      readonly words: Word[];
      readonly body: List;
      readonly redirects: Redirect[];
    }
  | {
      readonly k: "case";
      readonly at: string;
      readonly subject: Word;
      readonly arms: { readonly patterns: Word[]; readonly body: List }[];
      readonly redirects: Redirect[];
    }
  | { readonly k: "function"; readonly at: string; readonly name: string; readonly body: Command }
  | { readonly k: "arith"; readonly at: string; readonly text: string; readonly vars: string[] };
interface Pipeline {
  readonly negated: boolean;
  readonly commands: Command[];
}
/** Pipelines joined by `&&` and `||`. */
interface AndOr {
  readonly pipelines: Pipeline[];
}
type List = AndOr[];

type Token =
  | { readonly t: "word"; readonly word: Word }
  | { readonly t: "op"; readonly op: string; readonly line: number; readonly glued: boolean }
  | { readonly t: "redirect"; readonly op: string; readonly line: number }
  | { readonly t: "newline"; readonly line: number }
  | { readonly t: "end"; readonly line: number };

/** A script the parser cannot read, with the line where it stopped. */
class Unparsed extends Error {}

const controlOps = [";;&", "&&", "||", ";;", ";&", "|&", ";", "&", "|", "(", ")"];
const redirectOps = ["&>>", "<<<", "<<-", "&>", ">>", "<<", "<&", ">&", "<>", ">|", "<", ">"];
const special = "?!@*#$-0123456789";
// A here-document's operator, `<<` or `<<-`, with its descriptor; `<<<` is a here-string.
const heredocOp = /(^|[^<])<<-?$/;

/** The characters of a script, read into tokens on demand, with a parser of its own for each
 *  command substitution, so a `)` of a `case` in one does not close it. */
class Lexer {
  pos = 0;
  line: number;
  private readonly heredocs: { word: string; tabs: boolean }[] = [];

  constructor(
    readonly src: string,
    readonly file: string,
    line = 1
  ) {
    this.line = line;
  }

  fail(why: string): never {
    throw new Unparsed(`${this.file}:${this.line}: ${why}`);
  }

  /** Whether the text at the cursor matches `re`. */
  ahead(re: RegExp): boolean {
    return re.test(this.src.slice(this.pos, this.pos + 80));
  }

  heredoc(word: string, tabs: boolean): void {
    this.heredocs.push({ word: word.replace(/["'\\]/g, ""), tabs });
  }

  next(): Token {
    let glued = true;
    for (;;) {
      const c = this.src[this.pos];
      if (c === " " || c === "\t") {
        this.pos++;
        glued = false;
      } else if (c === "\\" && this.src[this.pos + 1] === "\n") {
        this.pos += 2;
        this.line++;
        glued = false;
      } else if (c === "#") {
        while (this.pos < this.src.length && this.src[this.pos] !== "\n") this.pos++;
      } else break;
    }
    const line = this.line;
    if (this.pos >= this.src.length) return { t: "end", line };
    const c = this.src[this.pos];
    if (c === "\n") {
      this.pos++;
      this.line++;
      this.readHeredocs();
      return { t: "newline", line };
    }
    if ((c === "<" || c === ">") && this.src[this.pos + 1] === "(") return this.word();
    if (this.src.startsWith("((", this.pos)) {
      this.pos += 2;
      return { t: "op", op: "((", line, glued };
    }
    for (const op of redirectOps) {
      if (this.src.startsWith(op, this.pos)) {
        this.pos += op.length;
        return { t: "redirect", op, line };
      }
    }
    for (const op of controlOps) {
      if (this.src.startsWith(op, this.pos)) {
        this.pos += op.length;
        return { t: "op", op, line, glued };
      }
    }
    return this.word();
  }

  private readHeredocs(): void {
    for (const { word, tabs } of this.heredocs.splice(0)) {
      while (this.pos < this.src.length) {
        const end = this.src.indexOf("\n", this.pos);
        const text = this.src.slice(this.pos, end < 0 ? this.src.length : end);
        this.pos = end < 0 ? this.src.length : end + 1;
        this.line++;
        if ((tabs ? text.replace(/^\t+/, "") : text) === word) break;
      }
    }
  }

  /** One word: up to a blank, a newline or an operator outside quotes; in `[[`, up to a blank or
   *  a newline only. */
  private word(cond = false): Token {
    const line = this.line;
    const subs: List[] = [];
    const vars: string[] = [];
    let text = "";
    const stops = cond ? " \t\n" : " \t\n;&|()<>";
    while (this.pos < this.src.length) {
      const c = this.src[this.pos];
      if ((c === "<" || c === ">") && this.src[this.pos + 1] === "(" && !cond) {
        this.pos += 2;
        subs.push(new Parser(this).untilParen());
        text += `${c}(…)`;
      } else if (c === "(" && /[@!?*+]$/.test(text)) {
        // An extended glob, @(…) and the like, is one word.
        text += this.balanced();
      } else if (stops.includes(c)) {
        break;
      } else if (c === "\\") {
        if (this.src[this.pos + 1] === "\n") this.line++;
        text += this.src.slice(this.pos, this.pos + 2);
        this.pos += 2;
      } else if (c === "'") {
        const end = this.src.indexOf("'", this.pos + 1);
        if (end < 0) this.fail("a single quote that does not close");
        text += this.src.slice(this.pos, end + 1);
        this.count(this.pos, end);
        this.pos = end + 1;
      } else if (c === '"') {
        text += this.double(subs, vars);
      } else if (c === "$") {
        text += this.dollar(subs, vars);
      } else if (c === "`") {
        text += this.backquote(subs);
      } else {
        text += c;
        this.pos++;
      }
    }
    const next = this.src[this.pos];
    if (
      !cond &&
      (next === "<" || next === ">") &&
      /^(\d+|\{[A-Za-z_][A-Za-z0-9_]*\})$/.test(text)
    ) {
      for (const op of redirectOps) {
        if (this.src.startsWith(op, this.pos)) {
          this.pos += op.length;
          return { t: "redirect", op: `${text}${op}`, line };
        }
      }
    }
    return { t: "word", word: { text, at: `${this.file}:${line}`, subs, vars } };
  }

  private count(from: number, to: number): void {
    for (let i = from; i < to; i++) if (this.src[i] === "\n") this.line++;
  }

  /** A parenthesised run of text, returned whole. */
  private balanced(): string {
    const start = this.pos;
    let depth = 0;
    while (this.pos < this.src.length) {
      const c = this.src[this.pos];
      this.pos++;
      if (c === "\\") this.pos++;
      else if (c === "(") depth++;
      else if (c === ")") {
        depth--;
        if (depth === 0) break;
      } else if (c === "\n") this.line++;
    }
    return this.src.slice(start, this.pos);
  }

  private double(subs: List[], vars: string[]): string {
    const start = this.pos;
    this.pos++;
    while (this.pos < this.src.length && this.src[this.pos] !== '"') {
      const c = this.src[this.pos];
      if (c === "\\") {
        if (this.src[this.pos + 1] === "\n") this.line++;
        this.pos += 2;
      } else if (c === "$") this.dollar(subs, vars, true);
      else if (c === "`") this.backquote(subs);
      else {
        if (c === "\n") this.line++;
        this.pos++;
      }
    }
    if (this.pos >= this.src.length) this.fail("a double quote that does not close");
    this.pos++;
    return this.src.slice(start, this.pos);
  }

  /** A `$` expansion: a command substitution parsed whole, an arithmetic one, a `${…}` with what
   *  it holds, or a parameter. Inside double quotes, `$'` and `$"` are a `$` alone. Returns its
   *  text. */
  private dollar(subs: List[], vars: string[], quoted = false): string {
    const start = this.pos;
    const next = this.src[this.pos + 1];
    if (this.src.startsWith("$((", this.pos)) {
      this.pos += 3;
      this.arithmetic(vars, subs);
    } else if (next === "(") {
      this.pos += 2;
      subs.push(new Parser(this).untilParen());
    } else if (next === "{") {
      this.pos += 2;
      this.brace(subs, vars);
    } else if (next === "'" && !quoted) {
      this.pos += 2;
      while (this.pos < this.src.length && this.src[this.pos] !== "'") {
        if (this.src[this.pos] === "\\") this.pos++;
        if (this.src[this.pos] === "\n") this.line++;
        this.pos++;
      }
      this.pos++;
    } else if (next === '"' && !quoted) {
      this.pos++;
      this.double(subs, vars);
    } else {
      const name = /^[A-Za-z_][A-Za-z0-9_]*/.exec(this.src.slice(this.pos + 1, this.pos + 65));
      if (name !== null) {
        vars.push(name[0]);
        this.pos += 1 + name[0].length;
      } else if (next !== undefined && special.includes(next)) {
        vars.push(next);
        this.pos += 2;
      } else this.pos++;
    }
    return this.src.slice(start, this.pos);
  }

  /** The inside of `${…}`: its parameter, then what follows it to the closing brace. */
  private brace(subs: List[], vars: string[]): void {
    const head = /^([#!]?)([A-Za-z_][A-Za-z0-9_]*|[?!@*#$\-0-9])/.exec(
      this.src.slice(this.pos, this.pos + 66)
    );
    if (head !== null) {
      // `${#name}` is a length, and `${!name}` reads the variable that name names.
      vars.push(head[1] === "!" ? `!${head[2]}` : head[2]);
      this.pos += head[0].length;
    }
    let depth = 0;
    while (this.pos < this.src.length) {
      const c = this.src[this.pos];
      if (c === "}" && depth === 0) {
        this.pos++;
        return;
      }
      if (c === "\\") this.pos += 2;
      else if (c === '"') this.double(subs, vars);
      else if (c === "'") {
        const end = this.src.indexOf("'", this.pos + 1);
        if (end < 0) this.fail("a single quote in a parameter expansion that does not close");
        this.count(this.pos, end);
        this.pos = end + 1;
      } else if (c === "$") this.dollar(subs, vars);
      else if (c === "`") this.backquote(subs);
      else {
        if (c === "{") depth++;
        else if (c === "}") depth--;
        else if (c === "\n") this.line++;
        this.pos++;
      }
    }
    this.fail("a parameter expansion that does not close");
  }

  /** The inside of `$((…))` or `((…))`, through its closing `))`: each bare name in it is a
   *  parameter it reads. Returns its text. */
  arithmetic(vars: string[], subs: List[]): string {
    const start = this.pos;
    let depth = 0;
    while (this.pos < this.src.length) {
      const c = this.src[this.pos];
      if (c === ")" && depth === 0 && this.src[this.pos + 1] === ")") {
        const text = this.src.slice(start, this.pos);
        this.pos += 2;
        for (const name of text.matchAll(/[A-Za-z_][A-Za-z0-9_]*/g)) vars.push(name[0]);
        return text;
      }
      if (c === "$") {
        this.dollar(subs, vars);
        continue;
      }
      if (c === "(") depth++;
      else if (c === ")") depth--;
      else if (c === "\n") this.line++;
      this.pos++;
    }
    return this.fail("an arithmetic (( that does not close");
  }

  private backquote(subs: List[]): string {
    const start = this.pos;
    const line = this.line;
    let inner = "";
    this.pos++;
    while (this.pos < this.src.length && this.src[this.pos] !== "`") {
      const c = this.src[this.pos];
      if (c === "\\" && "`\\$".includes(this.src[this.pos + 1] ?? "")) {
        inner += this.src[this.pos + 1];
        this.pos += 2;
        continue;
      }
      if (c === "\n") this.line++;
      inner += c;
      this.pos++;
    }
    if (this.pos >= this.src.length) this.fail("a backquote that does not close");
    this.pos++;
    subs.push(new Parser(new Lexer(inner, this.file, line)).program());
    return this.src.slice(start, this.pos);
  }

  /** The words of `[[ … ]]` after its `[[`, through its `]]`. */
  condition(): Word[] {
    const words: Word[] = [];
    for (;;) {
      const c = this.src[this.pos];
      if (c === " " || c === "\t") this.pos++;
      else if (c === "\n") {
        this.pos++;
        this.line++;
      } else if (c === "\\" && this.src[this.pos + 1] === "\n") {
        this.pos += 2;
        this.line++;
      } else if (this.pos >= this.src.length) this.fail("a [[ that does not close");
      else if (this.ahead(/^\]\](?=$|[\s;&|)])/)) {
        this.pos += 2;
        return words;
      } else {
        const token = this.word(true);
        if (token.t === "word") words.push(token.word);
      }
    }
  }
}

const reserved = (t: Token, ...words: string[]) => t.t === "word" && words.includes(t.word.text);
const op = (t: Token, ...ops: string[]) => t.t === "op" && ops.includes(t.op);
const declarers: Record<string, true> = {
  local: true,
  declare: true,
  typeset: true,
  export: true,
  readonly: true,
};
const assignment = /^([A-Za-z_][A-Za-z0-9_]*)(\[[^\]]*\])?\+?=/;

/** A recursive-descent parser of bash's grammar over one lexer, one token ahead. */
class Parser {
  private held: Token | undefined;

  constructor(private readonly lx: Lexer) {}

  private peek(): Token {
    this.held ??= this.lx.next();
    return this.held;
  }

  private take(): Token {
    const t = this.peek();
    this.held = undefined;
    return t;
  }

  private expect(...words: string[]): void {
    const t = this.take();
    if (!reserved(t, ...words) && !op(t, ...words)) this.lx.fail(`expected ${words.join(" or ")}`);
  }

  private lines(): void {
    while (this.peek().t === "newline") this.take();
  }

  program(): List {
    const list = this.list(() => false);
    if (this.take().t !== "end") this.lx.fail("text after the end of the script");
    return list;
  }

  /** A substitution's list, through its closing parenthesis. */
  untilParen(): List {
    const list = this.list((t) => op(t, ")"));
    if (!op(this.take(), ")")) this.lx.fail("a $( that does not close");
    return list;
  }

  private list(stop: (t: Token) => boolean): List {
    const items: List = [];
    for (;;) {
      this.lines();
      const t = this.peek();
      if (t.t === "end" || stop(t)) return items;
      items.push(this.andOr());
      const after = this.peek();
      if (op(after, ";", "&") || after.t === "newline") this.take();
    }
  }

  private andOr(): AndOr {
    const pipelines = [this.pipeline()];
    while (op(this.peek(), "&&", "||")) {
      this.take();
      this.lines();
      pipelines.push(this.pipeline());
    }
    return { pipelines };
  }

  private pipeline(): Pipeline {
    let negated = false;
    while (reserved(this.peek(), "!", "time")) {
      if (reserved(this.take(), "!")) negated = true;
    }
    const commands = [this.command()];
    while (op(this.peek(), "|", "|&")) {
      this.take();
      this.lines();
      commands.push(this.command());
    }
    return { negated, commands };
  }

  private command(): Command {
    const t = this.peek();
    const at =
      t.t === "word" ? t.word.at : `${this.lx.file}:${t.t === "end" ? this.lx.line : t.line}`;
    if (op(t, "(")) {
      this.take();
      const body = this.list((s) => op(s, ")"));
      this.expect(")");
      return { k: "group", at, subshell: true, body, redirects: this.redirects() };
    }
    if (op(t, "((")) {
      this.take();
      const vars: string[] = [];
      const text = this.lx.arithmetic(vars, []);
      return { k: "arith", at, text, vars };
    }
    if (t.t === "word") {
      switch (t.word.text) {
        case "if":
          return this.ifCommand(at);
        case "while":
        case "until":
          return this.loop(at);
        case "for":
          return this.forCommand(at);
        case "case":
          return this.caseCommand(at);
        case "{": {
          this.take();
          const body = this.list((s) => reserved(s, "}"));
          this.expect("}");
          return { k: "group", at, subshell: false, body, redirects: this.redirects() };
        }
        case "function": {
          this.take();
          const name = this.take();
          if (name.t !== "word") return this.lx.fail("a function with no name");
          if (this.lx.ahead(/^[ \t]*\([ \t]*\)/)) {
            this.expect("(");
            this.expect(")");
          }
          this.lines();
          return { k: "function", at, name: name.word.text, body: this.command() };
        }
      }
      if (/^[A-Za-z_][A-Za-z0-9_.:-]*$/.test(t.word.text) && this.lx.ahead(/^[ \t]*\([ \t]*\)/)) {
        this.take();
        this.expect("(");
        this.expect(")");
        this.lines();
        return { k: "function", at, name: t.word.text, body: this.command() };
      }
    }
    return this.simple(at);
  }

  private redirects(): Redirect[] {
    const out: Redirect[] = [];
    while (this.peek().t === "redirect") out.push(this.redirect());
    return out;
  }

  private redirect(): Redirect {
    const t = this.take();
    const target = this.take();
    if (t.t !== "redirect" || target.t !== "word")
      return this.lx.fail("a redirection with no word");
    if (heredocOp.test(t.op)) this.lx.heredoc(target.word.text, t.op.endsWith("-"));
    return { op: t.op, target: target.word };
  }

  private simple(at: string): Command {
    const assigns: Assignment[] = [];
    const words: Word[] = [];
    const redirects: Redirect[] = [];
    for (;;) {
      const t = this.peek();
      if (t.t === "redirect") {
        redirects.push(this.redirect());
        continue;
      }
      if (t.t !== "word") break;
      this.take();
      const name = assignment.exec(t.word.text);
      if (name !== null && (words.length === 0 || declarers[words[0].text] === true)) {
        const next = this.peek();
        if (t.word.text.endsWith("=") && next.t === "op" && next.op === "(" && next.glued) {
          this.take();
          const values: Word[] = [];
          for (;;) {
            this.lines();
            const v = this.take();
            if (op(v, ")")) break;
            if (v.t !== "word") return this.lx.fail("an array that does not close");
            values.push(v.word);
          }
          assigns.push({ name: name[1], values });
        } else assigns.push({ name: name[1], values: [t.word] });
        continue;
      }
      words.push(t.word);
      if (words.length === 1 && t.word.text === "[[") words.push(...this.lx.condition());
    }
    if (assigns.length === 0 && words.length === 0 && redirects.length === 0) {
      const t = this.peek();
      this.lx.fail(`unexpected ${t.t === "op" || t.t === "redirect" ? t.op : t.t}`);
    }
    return { k: "simple", at, assigns, words, redirects };
  }

  private ifCommand(at: string): Command {
    this.take();
    const arms: { cond: List | null; body: List }[] = [];
    for (;;) {
      const cond = this.list((s) => reserved(s, "then"));
      this.expect("then");
      const body = this.list((s) => reserved(s, "elif", "else", "fi"));
      arms.push({ cond, body });
      const t = this.take();
      if (reserved(t, "elif")) continue;
      if (reserved(t, "else")) {
        arms.push({ cond: null, body: this.list((s) => reserved(s, "fi")) });
        this.expect("fi");
      } else if (!reserved(t, "fi")) this.lx.fail("an if with no fi");
      return { k: "if", at, arms, redirects: this.redirects() };
    }
  }

  private loop(at: string): Command {
    this.take();
    const cond = this.list((s) => reserved(s, "do"));
    this.expect("do");
    const body = this.list((s) => reserved(s, "done"));
    this.expect("done");
    return { k: "loop", at, cond, body, redirects: this.redirects() };
  }

  private forCommand(at: string): Command {
    this.take();
    let name: string | null = null;
    const words: Word[] = [];
    if (op(this.peek(), "((")) {
      this.take();
      const vars: string[] = [];
      const text = this.lx.arithmetic(vars, []);
      words.push({ text: `((${text}))`, at, subs: [], vars });
    } else {
      const t = this.take();
      if (t.t !== "word") return this.lx.fail("a for with no name");
      name = t.word.text;
      this.lines();
      if (reserved(this.peek(), "in")) {
        this.take();
        for (let w = this.peek(); w.t === "word"; w = this.peek()) {
          words.push(w.word);
          this.take();
        }
      } else words.push({ text: '"$@"', at, subs: [], vars: ["@"] });
    }
    if (op(this.peek(), ";")) this.take();
    this.lines();
    this.expect("do");
    const body = this.list((s) => reserved(s, "done"));
    this.expect("done");
    return { k: "for", at, name, words, body, redirects: this.redirects() };
  }

  private caseCommand(at: string): Command {
    this.take();
    const subject = this.take();
    if (subject.t !== "word") return this.lx.fail("a case with no word");
    this.lines();
    this.expect("in");
    const arms: { patterns: Word[]; body: List }[] = [];
    for (;;) {
      this.lines();
      if (reserved(this.peek(), "esac")) {
        this.take();
        return { k: "case", at, subject: subject.word, arms, redirects: this.redirects() };
      }
      if (op(this.peek(), "(")) this.take();
      const patterns: Word[] = [];
      for (;;) {
        const p = this.take();
        if (p.t !== "word") return this.lx.fail("a case pattern that is no word");
        patterns.push(p.word);
        if (!op(this.peek(), "|")) break;
        this.take();
      }
      this.expect(")");
      const body = this.list((s) => op(s, ";;", ";&", ";;&") || reserved(s, "esac"));
      if (op(this.peek(), ";;", ";&", ";;&")) this.take();
      arms.push({ patterns, body });
    }
  }
}

// ---- What each script holds ---------------------------------------------------------------------

/** A function a script defines. */
interface Definition {
  readonly name: string;
  readonly body: Command;
}

/** One script, parsed: its top-level list, every function it defines, and each library of
 *  scripts/e2e/lib it sources, by its path under the root. */
interface Script {
  readonly top: List;
  readonly definitions: readonly Definition[];
  readonly libraries: readonly string[];
}

// A library of scripts/e2e/lib a script sources: `. "$root/scripts/e2e/lib/<name>.sh"`.
const library = /\/lib\/([A-Za-z0-9_.-]+\.sh)["']?$/;

/** A list of one command. */
const alone = (c: Command): List => [{ pipelines: [{ negated: false, commands: [c] }] }];

/** Each command of `list` in the order of its text, with the function each is in (null at the
 *  top level): the commands of each compound command, of each substitution in each word, and of
 *  each function's body. */
function eachCommand(
  list: List,
  ctx: string | null,
  visit: (c: Command, ctx: string | null) => void
): void {
  const words = (ws: readonly Word[], ctx: string | null) => {
    for (const w of ws) for (const sub of w.subs) eachCommand(sub, ctx, visit);
  };
  const one = (c: Command, ctx: string | null): void => {
    visit(c, ctx);
    if (c.k === "function") {
      one(c.body, c.name);
      return;
    }
    if (c.k === "arith") return;
    words(
      c.redirects.map((r) => r.target),
      ctx
    );
    switch (c.k) {
      case "simple":
        words([...c.assigns.flatMap((a) => a.values), ...c.words], ctx);
        break;
      case "group":
        eachCommand(c.body, ctx, visit);
        break;
      case "if":
        for (const arm of c.arms) {
          if (arm.cond !== null) eachCommand(arm.cond, ctx, visit);
          eachCommand(arm.body, ctx, visit);
        }
        break;
      case "loop":
        eachCommand(c.cond, ctx, visit);
        eachCommand(c.body, ctx, visit);
        break;
      case "for":
        words(c.words, ctx);
        eachCommand(c.body, ctx, visit);
        break;
      case "case":
        words([c.subject], ctx);
        for (const arm of c.arms) {
          words(arm.patterns, ctx);
          eachCommand(arm.body, ctx, visit);
        }
        break;
    }
  };
  for (const ao of list) for (const p of ao.pipelines) for (const c of p.commands) one(c, ctx);
}

/** The library of scripts/e2e/lib that a `.` or `source` command sources, by its path. */
function sourcedLibrary(c: Command): string | undefined {
  if (c.k !== "simple" || c.words.length < 2) return undefined;
  if (c.words[0].text !== "." && c.words[0].text !== "source") return undefined;
  const lib = library.exec(c.words[1].text);
  return lib === null ? undefined : `scripts/e2e/lib/${lib[1]}`;
}

/** A command's name, where its first word is plain text. */
function nameOf(words: readonly Word[]): string | undefined {
  const w = words[0];
  if (w === undefined || w.subs.length > 0 || w.vars.length > 0) return undefined;
  return /^[A-Za-z0-9_./:+[\]-]+$/.test(w.text) ? w.text : undefined;
}

// ---- What a check reads -------------------------------------------------------------------------

/** The commands a check runs, or the words it reads, in the function whose positional parameters
 *  they read (null at the top level). */
type Guard =
  | { readonly list: List; readonly ctx: string | null }
  | { readonly words: readonly Word[]; readonly ctx: string | null };

// The commands that read nothing of the machine: they test or print what they are given, or set a
// variable or a parameter. `[`, `[[` and `test` only with no test of a file.
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
};
const fileTests = /^-([abcdefgGhkLNOprsStuwx]|nt|ot|ef)$/;
// The variables the shell sets from what a command did, whatever the scripts assign.
const shellSet = ["BASH_REMATCH", "REPLY", "MAPFILE", "OPTARG", "PIPESTATUS", "COPROC"];
const identifier = /^[A-Za-z_][A-Za-z0-9_]*$/;

/** The variables of a driver and its libraries that can hold a command's output, and the
 *  functions whose positional parameters can: a data flow over every assignment, to a fixed
 *  point. */
class Flow {
  private readonly tainted = new Set<string>(shellSet);
  // The functions whose positional parameters can hold a command's output; "" is the script's.
  private readonly impureArgs = new Set<string>();
  private readonly assigns: { name: string; value: Word; ctx: string | null }[] = [];
  private readonly calls: { fn: string; args: Word[]; ctx: string | null }[] = [];
  private readonly positional: { value: Word; ctx: string | null }[] = [];
  // A function that sets the variable its Nth argument names (`printf -v "$1"`, `read "$2"`).
  private readonly dynamic: { fn: string; index: number }[] = [];

  constructor(scripts: readonly Script[], functions: ReadonlyMap<string, readonly Definition[]>) {
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

  /** Whether a word can hold a command's output. */
  impure(w: Word, ctx: string | null): boolean {
    return w.subs.length > 0 || w.vars.some((v) => this.variable(v, ctx) !== undefined);
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
class Reader {
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
    if (c.k === "function") return;
    if (c.k === "arith") {
      for (const v of c.vars) {
        const why = this.flow.variable(v, ctx);
        if (why !== undefined) out.add(why);
      }
      return;
    }
    for (const r of c.redirects) {
      const dup = /[<>]&$/.test(r.op) && /^(\d+-?|-)$/.test(r.target.text);
      const quiet = /^["']?\/dev\/(null|stdout|stderr|fd\/\d+)["']?$/.test(r.target.text);
      if (/<<<$/.test(r.op)) this.vars(r.target, ctx, out);
      else if (!heredocOp.test(r.op) && !dup && !quiet) out.add(`${r.op} ${r.target.text}`);
    }
    if (c.k === "for") for (const w of c.words) this.vars(w, ctx, out);
    if (c.k === "case")
      for (const w of [c.subject, ...c.arms.flatMap((a) => a.patterns)]) this.vars(w, ctx, out);
    if (c.k !== "simple") return;
    for (const a of c.assigns) for (const w of a.values) this.vars(w, ctx, out);
    for (const w of c.words) this.vars(w, ctx, out);
    let words = c.words;
    if (words[0]?.text === "command") {
      if (words[1]?.text === "-v" || words[1]?.text === "-V") return;
      words = words.slice(words[1]?.text === "-p" ? 2 : 1);
    }
    if (words.length === 0) return;
    const name = nameOf(words);
    if (name === undefined) out.add(`a command named by ${words[0].text}`);
    else if (this.functions.has(name)) {
      for (const r of this.body(name)) out.add(r.includes(" in ") ? r : `${r} in ${name}`);
    } else if (name === "exec" && words.length === 1) {
      // `exec` with redirections alone: the redirections above are what it reads.
    } else if (inputCommands[name] !== true) out.add(name);
    else if (name === "[" || name === "[[" || name === "test") {
      for (const w of words) if (fileTests.test(w.text)) out.add(`${name} ${w.text}`);
    }
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

// ---- The start's path ---------------------------------------------------------------------------

/** A removal, a mark or a refusal on the path, with what decides whether it runs. */
interface Event {
  readonly kind: Call["kind"];
  readonly name: string;
  readonly at: string;
  readonly begins: string;
  readonly from: string | undefined;
  readonly via: readonly string[];
  readonly guards: readonly Guard[];
}
type Step =
  | { readonly k: "event"; readonly event: Event }
  | { readonly k: "choice"; readonly arms: readonly (readonly Step[])[] }
  | { readonly k: "loop"; readonly body: readonly Step[] };

/** Where the walk is: the function it is in, the checks around it in that function, the checks
 *  of each `return` it already passed there, whether a status that is not 0 ends the start, and
 *  the driver's own command through which a library's top-level commands run. */
interface Frame {
  readonly fn: string | null;
  readonly local: readonly Guard[];
  readonly early: Guard[];
  readonly plain: boolean;
  readonly through: string | undefined;
}

/** The walk of one driver's start, from its top-level commands. */
class Walk {
  private readonly memo = new Map<string, readonly Step[]>();
  private readonly sourced = new Set<string>();

  constructor(
    private readonly reader: Reader,
    private readonly scripts: ReadonlyMap<string, Script>
  ) {}

  start(top: List): Step[] {
    const out: Step[] = [];
    const frame = { fn: null, local: [], early: [], plain: true, through: undefined };
    this.list(top, frame, out, undefined);
    return out;
  }

  /** The steps of a list. `begins` is the first command of the branch the list is; undefined,
   *  each command of the list begins its own. */
  private list(list: List, frame: Frame, out: Step[], begins: string | undefined): void {
    for (const ao of list)
      this.andOr(ao, frame, out, begins ?? ao.pipelines[0]?.commands[0]?.at ?? "");
  }

  private andOr(ao: AndOr, frame: Frame, out: Step[], begins: string): void {
    ao.pipelines.forEach((p, i) => {
      const before = ao.pipelines
        .slice(0, i)
        .map((q) => ({ list: [{ pipelines: [q] }], ctx: frame.fn }));
      const last = i === ao.pipelines.length - 1;
      const steps: Step[] = [];
      p.commands.forEach((c, j) => {
        const others = p.commands.filter((_, k) => k !== j);
        const local = [
          ...frame.local,
          ...before,
          ...(others.length > 0
            ? [{ list: [{ pipelines: [{ negated: false, commands: others }] }], ctx: frame.fn }]
            : []),
        ];
        const inner = { ...frame, local, plain: frame.plain && last && !p.negated };
        this.command(c, inner, steps, i > 0 ? c.at : begins);
      });
      if (i === 0) out.push(...steps);
      else out.push({ k: "choice", arms: [steps, []] });
    });
  }

  private subs(words: readonly Word[], frame: Frame, out: Step[], begins: string): void {
    for (const w of words) for (const sub of w.subs) this.list(sub, frame, out, begins);
  }

  private command(c: Command, frame: Frame, out: Step[], begins: string): void {
    if (c.k === "function" || c.k === "arith") return;
    // A redirection's process substitution runs before the command.
    this.subs(
      c.redirects.map((r) => r.target),
      frame,
      out,
      begins
    );
    switch (c.k) {
      case "simple":
        this.simple(c, frame, out, begins);
        return;
      case "group":
        this.list(c.body, frame, out, begins);
        return;
      case "if": {
        const arm = (index: number, prior: readonly Guard[]): Step[] => {
          const { cond, body } = c.arms[index];
          const steps: Step[] = [];
          const taken: Step[] = [];
          const decided = cond === null ? prior : [...prior, { list: cond, ctx: frame.fn }];
          if (cond !== null)
            this.list(
              cond,
              { ...frame, local: [...frame.local, ...prior], plain: false },
              steps,
              undefined
            );
          this.list(
            body,
            { ...frame, local: [...frame.local, ...decided] },
            taken,
            body[0]?.pipelines[0]?.commands[0]?.at
          );
          if (cond === null) return [...steps, ...taken];
          const rest = index + 1 < c.arms.length ? arm(index + 1, decided) : [];
          steps.push({ k: "choice", arms: [taken, rest] });
          return steps;
        };
        out.push(...arm(0, []));
        return;
      }
      case "loop": {
        this.list(c.cond, { ...frame, plain: false }, out, undefined);
        const body: Step[] = [];
        const local = [...frame.local, { list: c.cond, ctx: frame.fn }];
        this.list(c.body, { ...frame, local }, body, c.body[0]?.pipelines[0]?.commands[0]?.at);
        out.push({ k: "loop", body });
        return;
      }
      case "for": {
        this.subs(c.words, frame, out, begins);
        const body: Step[] = [];
        const local = [...frame.local, { words: c.words, ctx: frame.fn }];
        this.list(c.body, { ...frame, local }, body, c.body[0]?.pipelines[0]?.commands[0]?.at);
        out.push({ k: "loop", body });
        return;
      }
      case "case": {
        this.subs([c.subject], frame, out, begins);
        const arms = c.arms.map((arm) => {
          const steps: Step[] = [];
          const local = [...frame.local, { words: [c.subject, ...arm.patterns], ctx: frame.fn }];
          this.list(
            arm.body,
            { ...frame, local },
            steps,
            arm.body[0]?.pipelines[0]?.commands[0]?.at
          );
          return steps;
        });
        if (!c.arms.some((arm) => arm.patterns.some((p) => p.text === "*"))) arms.push([]);
        // A case on an input one of whose arms reaches the removal is the script's choice of
        // command: the arms that reach neither it nor a mark are other commands of the script.
        const starting = arms.filter((arm) => reaches(arm, "removal") || reaches(arm, "mark"));
        const input = this.reader.reads([{ words: [c.subject], ctx: frame.fn }]).length === 0;
        const removing = arms.some((arm) => reaches(arm, "removal"));
        out.push({ k: "choice", arms: input && removing ? starting : arms });
        return;
      }
    }
  }

  private event(kind: Call["kind"], name: string, at: string, frame: Frame, begins: string): Step {
    const from = frame.fn === null ? (frame.through ?? at) : undefined;
    const guards = [...frame.local, ...frame.early];
    return { k: "event", event: { kind, name, at, begins, from, via: [], guards } };
  }

  private simple(c: Command & { k: "simple" }, frame: Frame, out: Step[], begins: string): void {
    const words = [...c.assigns.flatMap((a) => a.values), ...c.words];
    this.subs(words, frame, out, begins);
    const name = nameOf(c.words);
    if (name === undefined) return;
    if (name === sweep || name === mark) {
      out.push(this.event(name === sweep ? "removal" : "mark", name, c.at, frame, begins));
      return;
    }
    const lib = sourcedLibrary(c);
    if (lib !== undefined) {
      const script = this.scripts.get(lib);
      if (script === undefined || this.sourced.has(lib)) return;
      this.sourced.add(lib);
      const through = frame.fn === null ? (frame.through ?? c.at) : frame.through;
      this.list(script.top, { ...frame, through }, out, undefined);
      return;
    }
    const status = c.words[1]?.text;
    if (name === "exit") {
      if (status !== "0") out.push(this.event("refusal", name, c.at, frame, begins));
      else frame.early.push(...frame.local);
      return;
    }
    if (name === "return" && frame.fn !== null) {
      if (status !== "0" && frame.plain) out.push(this.event("refusal", name, c.at, frame, begins));
      frame.early.push(...frame.local);
      return;
    }
    const definitions = this.reader.functions.get(name);
    if (definitions === undefined) return;
    if (this.reader.reporter(name)) {
      out.push(this.event("refusal", name, c.at, frame, begins));
      return;
    }
    const key = `${name} ${frame.plain}`;
    let inner = this.memo.get(key);
    if (inner === undefined) {
      this.memo.set(key, []);
      const arms = definitions.map((d) => {
        const steps: Step[] = [];
        const body = d.body.k === "group" ? d.body.body : alone(d.body);
        const own = { fn: name, local: [], early: [], plain: frame.plain, through: undefined };
        this.list(body, own, steps, undefined);
        return steps;
      });
      inner = arms.length === 1 ? arms[0] : [{ k: "choice", arms }];
      this.memo.set(key, inner);
    }
    const from = frame.fn === null ? (frame.through ?? c.at) : undefined;
    const prefix = [...frame.local, ...frame.early];
    const place = (steps: readonly Step[]): Step[] =>
      steps.map((s) => {
        if (s.k === "choice") return { k: "choice", arms: s.arms.map(place) };
        if (s.k === "loop") return { k: "loop", body: place(s.body) };
        const e = s.event;
        const via = [name, ...e.via];
        return {
          k: "event",
          event: { ...e, from: e.from ?? from, via, guards: [...prefix, ...e.guards] },
        };
      });
    out.push(...place(inner));
  }
}

/** Whether steps reach a removal, or a mark, anywhere. */
function reaches(steps: readonly Step[], kind: "removal" | "mark"): boolean {
  return steps.some((s) =>
    s.k === "event"
      ? s.event.kind === kind
      : s.k === "choice"
        ? s.arms.some((arm) => reaches(arm, kind))
        : reaches(s.body, kind)
  );
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
          const top = new Parser(new Lexer(text, shown)).program();
          const definitions: Definition[] = [];
          const libraries: string[] = [];
          eachCommand(top, null, (c) => {
            if (c.k === "function") definitions.push({ name: c.name, body: c.body });
            const lib = sourcedLibrary(c);
            if (lib !== undefined) libraries.push(lib);
          });
          script = { top, definitions, libraries };
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
    const reader = new Reader(functions, new Flow([...scripts.values()], functions));
    const steps = new Walk(reader, scripts).start(own.top);
    if (blind !== undefined)
      unreadable(shown, `${shown}: sources ${blind}, which the search cannot parse`, true);
    if (!reaches(steps, "removal")) continue;
    const reads = new Map<Event, readonly string[]>();
    const readsOf = (e: Event): readonly string[] => {
      let known = reads.get(e);
      if (known === undefined) {
        known =
          e.kind !== "refusal"
            ? []
            : e.guards.length === 0
              ? ["nothing: no check decides it, so it ends every start that reaches it"]
              : reader.reads(e.guards);
        reads.set(e, known);
      }
      return known;
    };
    // A refusal whose check reads only inputs is no call of the class.
    const counts = (e: Event) => e.kind !== "refusal" || readsOf(e).length > 0;
    const call = (e: Event): Call => ({
      kind: e.kind,
      name: e.name,
      at: e.at,
      from: e.from ?? e.at,
      via: e.via,
      reads: readsOf(e),
      begins: e.begins,
    });
    const calls: Call[] = [];
    const flat = (s: readonly Step[]): void => {
      for (const step of s) {
        if (step.k === "event") {
          if (counts(step.event)) calls.push(call(step.event));
        } else if (step.k === "choice") for (const arm of step.arms) flat(arm);
        else flat(step.body);
      }
    };
    flat(steps);
    // Each path: a removal counts after a choice only where every arm of it removes.
    const before: Event[] = [];
    const judge = (s: readonly Step[], reached: boolean): boolean => {
      let now = reached;
      for (const step of s) {
        if (step.k === "event") {
          if (step.event.kind === "removal") now = true;
          else if (!now && counts(step.event)) before.push(step.event);
        } else if (step.k === "choice") {
          let all = true;
          for (const arm of step.arms) all = judge(arm, now) && all;
          now = all;
        } else judge(step.body, now);
      }
      return now;
    };
    judge(steps, false);
    const first = calls.find((c) => c.kind === "removal");
    if (first === undefined) continue;
    drivers.push({ driver: shown, sweep: first.at, calls });
    const seen = new Set<string>();
    const listed = before.map(call).filter((c) => {
      const key = `${c.name} ${c.at} ${c.from}`;
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    });
    if (listed.length > 0) findings.push({ driver: shown, before: listed, sweep: first.at });
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
