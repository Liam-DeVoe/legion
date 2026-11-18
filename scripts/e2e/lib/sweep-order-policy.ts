/**
 * The removal-order policy of the order search (lib/sweep-order.ts): the walk of one driver's
 * start from its top-level commands, through each function and library it reaches, and which of
 * the refusals and marks on its paths come before its removal, as lib/sweep-order.ts's header
 * states the rule.
 */

import { Flow, type Guard, Reader } from "./sweep-order-inputs";
import {
  type AndOr,
  alone,
  type Command,
  type Definition,
  declarers,
  type List,
  nameOf,
  type Script,
  sourcedLibrary,
  type Word,
} from "./sweep-order-shell";

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
  /** Whether `guards` is the command's own run alone (`Walk.failure`), which no caller adds to. */
  readonly own?: true;
}
type Step =
  | { readonly k: "event"; readonly event: Event }
  | { readonly k: "choice"; readonly arms: readonly (readonly Step[])[] }
  | { readonly k: "loop"; readonly body: readonly Step[] };

/** Where the walk is: the function it is in, the checks around it in that function, the checks
 *  of each `return` it already passed there, whether a status that is not 0 ends the start,
 *  whether the walk is inside a command or process substitution, whose commands `set -e` does
 *  not end the start on, the driver's own command through which a library's top-level commands
 *  run, and, in a function, the options at each `return` the walk passed in it. */
interface Frame {
  readonly fn: string | null;
  readonly local: readonly Guard[];
  readonly early: Guard[];
  readonly plain: boolean;
  readonly substitution: boolean;
  readonly through: string | undefined;
  readonly returns?: Options[];
}

/** Whether a failed command may end the start where nothing tests its status, on some path the
 *  walk came by to where it is: `set -e` (or `set -o errexit`, or `-e` on the script's first line)
 *  may be on, or a `trap` on ERR that ends the start may be set. Bash keeps them per shell: a
 *  subshell, a command or process substitution, a command of a pipeline of more than one and a
 *  list in the background each start from the options of the shell that runs them and change
 *  none of its own. Where paths join after a branch that may or may not run, or a loop that may
 *  run again, each is on where it may be on along one of them (`join`), so the search fails safe. */
interface Options {
  readonly errexit: boolean;
  readonly errTrap: boolean;
}

/** The options after paths join: each on where it is on along one of them. */
function join(first: Options, second: Options): Options {
  return { errexit: first.errexit || second.errexit, errTrap: first.errTrap || second.errTrap };
}

/** The walk of one driver's start, from its top-level commands. */
class Walk {
  /** Each function's steps, and the options after it, by its name, the frame's flags and the
   *  options it was called with. */
  private readonly memo = new Map<
    string,
    { readonly steps: readonly Step[]; readonly exit: Options }
  >();
  private readonly sourced = new Set<string>();
  /** The options as far as the walk has come (`Options`). */
  private state: Options = { errexit: false, errTrap: false };

  constructor(
    private readonly reader: Reader,
    private readonly scripts: ReadonlyMap<string, Script>
  ) {}

  /** The steps of a start whose top-level list is `top` and whose first line gives bash
   *  `options` (`Script.options`). */
  start(top: List, options: readonly string[]): Step[] {
    const out: Step[] = [];
    const frame = {
      fn: null,
      local: [],
      early: [],
      plain: true,
      substitution: false,
      through: undefined,
    };
    this.options("set", options);
    this.list(top, frame, out, undefined);
    return out;
  }

  /** Walks what runs in a shell of its own: the options it changes are not the walk's after it. */
  private scoped(walk: () => void): void {
    const before = this.state;
    walk();
    this.state = before;
  }

  /** Walks what may not run: the options after it are those after it joined with those before. */
  private maybe(walk: () => void): void {
    const before = this.state;
    walk();
    this.state = join(before, this.state);
  }

  /** Walks what may run again, a loop's body, by `once`, which answers the steps of one turn:
   *  where a turn may turn on an option that was off when the loop began, a later turn begins with
   *  it on, and the turn is walked again from there, so its steps are those of such a turn. The
   *  options after it are those of every turn joined with those before. */
  private turns<T>(frame: Frame, once: () => T): T {
    const before = this.state;
    const sourced = new Set(this.sourced);
    const early = frame.early.length;
    let steps = once();
    const again = join(before, this.state);
    if (again.errexit !== before.errexit || again.errTrap !== before.errTrap) {
      this.sourced.clear();
      for (const lib of sourced) this.sourced.add(lib);
      frame.early.length = early;
      this.state = again;
      steps = once();
    }
    this.state = join(again, this.state);
    return steps;
  }

  /** The steps of a list. `begins` is the first command of the branch the list is; undefined,
   *  each command of the list begins its own. */
  private list(list: List, frame: Frame, out: Step[], begins: string | undefined): void {
    for (const ao of list) {
      const walk = () =>
        this.andOr(ao, frame, out, begins ?? ao.pipelines[0]?.commands[0]?.at ?? "");
      if (ao.background === true) this.scoped(walk);
      else walk();
    }
  }

  private andOr(ao: AndOr, frame: Frame, out: Step[], begins: string): void {
    ao.pipelines.forEach((p, i) => {
      const before = ao.pipelines
        .slice(0, i)
        .map((q) => ({ list: [{ pipelines: [q] }], ctx: frame.fn }));
      const last = i === ao.pipelines.length - 1;
      const steps: Step[] = [];
      const run = () =>
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
          const one = () => this.command(c, inner, steps, i > 0 ? c.at : begins);
          // Each command of a pipeline of more than one runs in a subshell of its own.
          if (p.commands.length > 1) this.scoped(one);
          else one();
        });
      if (i === 0) {
        run();
        out.push(...steps);
      } else {
        this.maybe(run);
        out.push({ k: "choice", arms: [steps, []] });
      }
    });
  }

  private subs(words: readonly Word[], frame: Frame, out: Step[], begins: string): void {
    for (const w of words) this.substitutions(w.subs, frame, out, begins);
  }

  /** The steps of command or process substitutions, each in a shell of its own. */
  private substitutions(lists: readonly List[], frame: Frame, out: Step[], begins: string): void {
    for (const sub of lists)
      this.scoped(() => this.list(sub, { ...frame, substitution: true }, out, begins));
  }

  private command(c: Command, frame: Frame, out: Step[], begins: string): void {
    if (c.k === "function") return;
    if (c.k === "arith") {
      // `(( … ))` fails where its expression is 0, as `set -e` reads a status; the command
      // substitutions in it run first.
      this.substitutions(c.subs, frame, out, begins);
      this.failure(
        c,
        `((${c.text.trim()}))`,
        [{ list: alone(c), ctx: frame.fn }],
        frame,
        out,
        begins
      );
      return;
    }
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
        if (c.subshell) this.scoped(() => this.list(c.body, frame, out, begins));
        else this.list(c.body, frame, out, begins);
        return;
      case "if": {
        // The options after each branch that runs, and after the last condition where no branch
        // runs.
        const exits: Options[] = [];
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
          const tested = this.state;
          this.list(
            body,
            { ...frame, local: [...frame.local, ...decided] },
            taken,
            body[0]?.pipelines[0]?.commands[0]?.at
          );
          exits.push(this.state);
          this.state = tested;
          if (cond === null) return [...steps, ...taken];
          let rest: Step[] = [];
          if (index + 1 < c.arms.length) rest = arm(index + 1, decided);
          else exits.push(tested);
          steps.push({ k: "choice", arms: [taken, rest] });
          return steps;
        };
        out.push(...arm(0, []));
        this.state = exits.reduce(join);
        return;
      }
      case "loop": {
        const local = [...frame.local, { list: c.cond, ctx: frame.fn }];
        const { cond, body } = this.turns(frame, () => {
          const cond: Step[] = [];
          const body: Step[] = [];
          this.list(c.cond, { ...frame, plain: false }, cond, undefined);
          this.list(c.body, { ...frame, local }, body, c.body[0]?.pipelines[0]?.commands[0]?.at);
          return { cond, body };
        });
        out.push(...cond, { k: "loop", body });
        return;
      }
      case "for": {
        this.subs(c.words, frame, out, begins);
        const local = [...frame.local, { words: c.words, ctx: frame.fn }];
        const body = this.turns(frame, () => {
          const body: Step[] = [];
          this.list(c.body, { ...frame, local }, body, c.body[0]?.pipelines[0]?.commands[0]?.at);
          return body;
        });
        out.push({ k: "loop", body });
        return;
      }
      case "case": {
        this.subs([c.subject], frame, out, begins);
        const entry = this.state;
        const exits: Options[] = [];
        const arms = c.arms.map((arm) => {
          const steps: Step[] = [];
          const local = [...frame.local, { words: [c.subject, ...arm.patterns], ctx: frame.fn }];
          this.state = entry;
          this.list(
            arm.body,
            { ...frame, local },
            steps,
            arm.body[0]?.pipelines[0]?.commands[0]?.at
          );
          exits.push(this.state);
          return steps;
        });
        if (!c.arms.some((arm) => arm.patterns.some((p) => p.text === "*"))) {
          arms.push([]);
          exits.push(entry);
        }
        this.state = exits.reduce(join);
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

  /** A command whose failure ends the start where `set -e` is on or a `trap` on ERR ends it, and
   *  nothing tests its status. Its own run is its whole check (`checks`): whatever decides that it
   *  runs, whether it fails is decided by what it reads, so it counts unless that is only the
   *  start's inputs, as `[ -n "$x" ]` and `true` read. */
  private failure(
    c: Command,
    shown: string,
    checks: readonly Guard[],
    frame: Frame,
    out: Step[],
    begins: string
  ): void {
    if (!(this.state.errexit || this.state.errTrap) || !frame.plain || frame.substitution) return;
    const from = frame.fn === null ? (frame.through ?? c.at) : undefined;
    const name = `${shown} under set -e`;
    out.push({
      k: "event",
      event: { kind: "refusal", name, at: c.at, begins, from, via: [], guards: checks, own: true },
    });
  }

  /** What a `set` or a `trap` command does to whether a failure ends the start. */
  private options(name: string, args: readonly string[]): void {
    if (name === "set") {
      for (let i = 0; i < args.length; i++) {
        const arg = args[i] ?? "";
        if (arg === "--" || arg === "-") return;
        if ((arg === "-o" || arg === "+o") && args[i + 1] === "errexit")
          this.state = { ...this.state, errexit: arg === "-o" };
        else if (/^-[A-Za-z]+$/.test(arg) && arg.includes("e"))
          this.state = { ...this.state, errexit: true };
        else if (/^\+[A-Za-z]+$/.test(arg) && arg.includes("e"))
          this.state = { ...this.state, errexit: false };
      }
      return;
    }
    // `trap ACTION ... ERR`: an action that exits, or calls a function that always does, ends
    // the start on each failure as `set -e` does; `trap - ERR` and `trap '' ERR` do not.
    if (!args.slice(1).includes("ERR")) return;
    const action = (args[0] ?? "").replace(/^(['"])(.*)\1$/s, "$2");
    const errTrap =
      /(^|[\s;&|])exit([\s;&|]|$)/.test(action) ||
      action
        .split(/[\s;&|]+/)
        .some((word) => this.reader.functions.has(word) && this.reader.reporter(word));
    this.state = { ...this.state, errTrap };
  }

  private simple(c: Command & { k: "simple" }, frame: Frame, out: Step[], begins: string): void {
    const words = [...c.assigns.flatMap((a) => a.values), ...c.words];
    this.subs(words, frame, out, begins);
    const name = nameOf(c.words);
    const self = [{ list: alone(c), ctx: frame.fn }];
    if (name === undefined) {
      if (c.words.length > 0) {
        this.failure(c, c.words[0]?.text ?? "", self, frame, out, begins);
        return;
      }
      // Assignments alone have the status of their last command substitution, and none fails
      // where they hold none.
      const subs = words.flatMap((w) => w.subs.map((list) => ({ list, ctx: frame.fn })));
      if (subs.length > 0) {
        this.failure(c, `${c.assigns[0]?.name ?? ""}=$(…)`, subs, frame, out, begins);
      }
      return;
    }
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
      frame.returns?.push(this.state);
      return;
    }
    const definitions = this.reader.functions.get(name);
    if (definitions === undefined) {
      if (name === "set" || name === "trap") {
        this.options(
          name,
          c.words.slice(1).map((w) => w.text)
        );
        return;
      }
      // `local`, `export` and the like give their own status, not that of what they assign.
      if (declarers[name] !== true) this.failure(c, name, self, frame, out, begins);
      return;
    }
    if (this.reader.reporter(name)) {
      out.push(this.event("refusal", name, c.at, frame, begins));
      return;
    }
    const key = `${name} ${frame.plain} ${frame.substitution} ${this.state.errexit} ${this.state.errTrap}`;
    let inner = this.memo.get(key);
    if (inner === undefined) {
      // A call of the function inside its own body ends with the options it began with.
      this.memo.set(key, { steps: [], exit: this.state });
      const entry = this.state;
      const exits: Options[] = [];
      const arms = definitions.map((d) => {
        this.state = entry;
        const steps: Step[] = [];
        const returns: Options[] = [];
        const own = {
          fn: name,
          local: [],
          early: [],
          plain: frame.plain,
          substitution: frame.substitution,
          through: undefined,
          returns,
        };
        // A body in braces runs in the caller's shell, and one in parentheses in a subshell of
        // its own, whose options are not the caller's after it.
        const body = d.body;
        if (body.k !== "group") this.list(alone(body), own, steps, undefined);
        else if (body.subshell) this.scoped(() => this.list(body.body, own, steps, undefined));
        else this.list(body.body, own, steps, undefined);
        exits.push(returns.reduce(join, this.state));
        return steps;
      });
      inner = {
        steps: arms.length === 1 ? arms[0] : [{ k: "choice", arms }],
        exit: exits.reduce(join),
      };
      this.memo.set(key, inner);
    }
    this.state = inner.exit;
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
          event: {
            ...e,
            from: e.from ?? from,
            via,
            guards: e.own === true ? e.guards : [...prefix, ...e.guards],
          },
        };
      });
    out.push(...place(inner.steps));
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

/** The order of one driver's start, `own`, with the scripts it and its libraries are and the
 *  functions they define: every removal, mark and refusal it reaches, in the order of its text,
 *  and each refusal and mark before its removal on one of its paths, with where its first removal
 *  is; undefined where it reaches no removal. */
export function orderOf(
  own: Script,
  scripts: ReadonlyMap<string, Script>,
  functions: ReadonlyMap<string, readonly Definition[]>
):
  | { readonly calls: readonly Call[]; readonly before: readonly Call[]; readonly sweep: string }
  | undefined {
  const reader = new Reader(functions, new Flow([...scripts.values()], functions));
  const steps = new Walk(reader, scripts).start(own.top, own.options);
  if (!reaches(steps, "removal")) return undefined;
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
  if (first === undefined) return undefined;
  const seen = new Set<string>();
  const listed = before.map(call).filter((c) => {
    const key = `${c.name} ${c.at} ${c.from}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
  return { calls, before: listed, sweep: first.at };
}
