/**
 * The work a parse does, counted, for the rows that hold a heavy body's parse to a cost in
 * proportion to its length.
 *
 * A row reads its counts around one parse (packages/proof-editor/AGENTS.md, "The dependency
 * patches"). The patches of `micromark` and `micromark-core-commonmark` count into one registry,
 * `workCounts()` of `micromark-core-commonmark` (`patches/micromark-core-commonmark@2.0.3.patch`,
 * `lib/work-counts.js`, which micromark's patched modules count into too), each counter by its
 * name there. The first five counts are held to a cost a character as well as to a linear growth:
 *
 * - `tokenizer`: one step for each code micromark's tokenizers hand a state, a code a construct
 *   read and gave back counting again each time it is read again. Every construct runs through it -
 *   CommonMark's, the extensions' and the `list` and `blockQuote` src/block-nesting.ts rewrites - in
 *   whatever parser runs it, so it is the whole tokenization of the parse the row makes: Milkdown's
 *   `ParserState`, through remark.
 * - `lazyLine`, one step per event the lazy-line test reads.
 * - `labelEndScan`, one per event a label end's search for its opening `[` reads and one per
 *   character its definition lookup reads.
 * - `nestingScanSteps` of src/block-nesting.ts, one per event `openedOnLine` reads: the one loop of
 *   that module outside the tokenizer's states.
 * - `directiveScanSteps` of src/directive-container.ts, one per event its resolver
 *   (`resolveDirectiveContainers`) and its looks back while it tokenizes read.
 *
 * The last four are the scans that read events rather than codes, which the tokenizer's count does
 * not see. The stages that walk the events once a tokenizer has them, and the tree after them, are
 * counted after them, each held to a linear growth alone:
 *
 * - `textResolve`: the events, chunks and spaces the string and text resolvers (`resolveAllText`,
 *   `resolveAllLineSuffixes`) read, and every event they copy or write back.
 * - `attention`, `labelEnd`, `headingAtx`, `setextUnderline` and `codeText`: the same, for each
 *   resolver of `micromark-core-commonmark`, each `splice` counted from its own arguments
 *   (`spliceCounted`: each event it removes or puts in, and each event after them it shifts), so a
 *   resolver that splices once a line shows the square of the paragraph here; and `content`, the
 *   events `micromark-util-subtokenize` hands content back.
 * - `resolvedEvents` and `postprocessedEvents`: the events every tokenizer's resolvers hand on, and
 *   the events `postprocess` hands the compile.
 * - `milkdownRunnerCalls` of src/lib-headless.ts: each mdast node and mark Milkdown's parser state
 *   hands a spec's runner, counted where this package builds the specs, with no patch; and
 *   `milkdownMarkOpens`, each mark a runner opens (an ask nested inside asks opens one, not one
 *   for each ask around it: src/dispatch-marks.ts).
 * - `htmlFlowResolveSteps` of src/html-flow.ts: each event the HTML (flow) resolver reads and
 *   each its splice shifts, which the tokenizer's count does not see.
 * - `blockIdWalkSteps` and `leafMarkWalkSteps` (src/editor/schema/block-ids.ts and leaf-marks.ts):
 *   the nodes `withBlockIds` visits, and the nodes `withoutLeafMarks` visits and the earlier
 *   children it copies, the two walks of the ProseMirror document every headless parse ends with.
 *   ProseMirror compares node types by identity, so no Proxy stands in for that document, and
 *   each walk counts itself.
 * - The stages no patch reaches, each counted from outside through a counting `Proxy` it is handed
 *   in place of what it takes (`counted`), one step for every read and every write it makes of an
 *   object it reaches through it: each mdast transform of the parse, this package's own and the
 *   ones it installs, by the name of its plugin, handed the tree; `micromark-util-subtokenize`,
 *   handed the events `postprocess` gives it and the events content's resolver gives it; the GFM
 *   and front matter extensions, their resolvers handed the events, their compile handlers the
 *   token and the compile's own state (whose stack is the tree it builds) and their transforms the
 *   tree; and `mdast-util-from-markdown`'s compile, handed the events. micromark's document
 *   tokenizer hands its events on through the Proxy itself (`document.resolveAll`, set below on
 *   micromark's own initializer): the steps go to subtokenize until `postprocessedEvents` moves,
 *   which is the moment `postprocess` hands them to the compile, and to the compile after it.
 *
 * What a count cannot see: work a stage does on a copy of its own (subtokenize's `SpliceBuffer`
 * splices, a transform's clone of a node), on the input string, or on a structure that is the
 * stage's alone until it returns it - the compile's own handlers writing the tree they build,
 * which `mdast-util-from-markdown` keeps private (`compiler` is not exported). That package is
 * pinned at 2.0.3 with a patch of its own (`patches/mdast-util-from-markdown@2.0.3.patch`), so its
 * handlers change only when that pin moves; the patch is made again then, and the handlers are
 * read again with it.
 *
 * The counts are the code's own record of the work, or the Proxy's of what each stage touched, so
 * a busy machine does not move them, and a parse put back to reading every event, node or code
 * again on each line, each `]` or each opener reads a number of steps that grows with the square
 * of the body.
 *
 * A row reads the kept counts at the shape's full size and at `1/growthStep` of its growing
 * dimension, and the stages' steps one step down, at `1/growthStep` and its square (or at the
 * kept counts' two sizes, for a shape whose `smallest` size lies above that square): a Proxy traps
 * every read and write a stage makes, which at the full size would cost the row far more than its
 * guard, and a walk that grows with the square of the body shows at that step too. It holds them
 * three ways: each count moved as far as the body says it must (`expectFloors`), the full size took
 * no more steps a character than the highest healthy reading allows (`expectWorkInProportion`),
 * and no count grew more than `growthSlack` times faster than the dimension did
 * (`expectWorkGrowsLinearly`).
 *
 * The counters run over the whole process. A parse is synchronous and the rows of a file run one at
 * a time, so the steps between two readings are that parse's alone.
 */

import { expect } from "bun:test";
import { dirname, join } from "node:path";
import { content, workCount, workCounts } from "micromark-core-commonmark";
import type { Processor } from "unified";
import { nestingScanSteps } from "../src/block-nesting";
import { directiveScanSteps } from "../src/directive-container";
import { blockIdWalkSteps } from "../src/editor/schema/block-ids";
import { leafMarkWalkSteps } from "../src/editor/schema/leaf-marks";
import { htmlFlowResolveSteps } from "../src/html-flow";
import { milkdownMarkOpens, milkdownRunnerCalls } from "../src/lib-headless";

/**
 * The most scan steps - lazy-line, label-end, nesting and directive together - a parse may take
 * per character of its body at the full size.
 *
 * The highest healthy reading is 3.0 a character: a run of 15,000 brackets and as many closers,
 * where each `]` reads the events added since the label end before it stopped looking and the
 * opening below them (89,995 steps on 30,000 characters, in both modes, 2026-11-14). The lazy
 * shapes read 0.5 a character (one event for each lazy line), the nesting scan at most 0.19 (100
 * items nested by indentation then blank lines, 2026-11-18), and the directive scans at most 2.13
 * (1,000 nested openers then 1,000 closing lines: 19,104 steps on 9,000 characters, 2.24 with the
 * other scans, in both modes, 2026-11-19). This bound is twice the highest. A scan that reads
 * every event again reads a number of steps that grows with the square of the body instead:
 * thousands a character at these lengths.
 */
export const scanStepsPerCharacter = 6;

/**
 * The most tokenizer steps a parse may take per character of its body at the full size.
 *
 * The highest healthy reading is 56.9 a character: a chain of 15,999 list markers on one line
 * (1,821,156 steps on 31,999 characters, in both modes, 2026-11-18); `49 items nested by
 * indentation then blank lines` reads 51.2. This bound is twice the highest. A body is tokenized at
 * least once, so no parse reads under one a character (`expectFloors`).
 */
export const tokenizerStepsPerCharacter = 114;

/**
 * The step a growth check compares across: the full shape against an eighth of its growing
 * dimension.
 *
 * A count in proportion to the dimension reads about 8 at this step, less where a part of the body
 * that does not grow (the nesting above a run of blank lines) is counted on both sides; a count
 * that grows with the square of it reads about 64.
 */
export const growthStep = 8;

/**
 * How many times faster than its growing dimension a count may grow and still read as linear, at
 * `growthStep`: the bound is 16.
 *
 * The counts are exact, so the slack pays for no noise: it is the room between the highest healthy
 * ratio, 8.88 (the tokenizer's steps on 1,000 nested openers then 1,000 closing lines, in both
 * modes, 2026-11-18), and the bound, and it leaves a count that grows with the square, about 64,
 * four times past the bound. A count growing as `n log n` reads about 10 here and passes.
 */
export const growthSlack = 2;

/**
 * A body a row parses, as a reader is sent it and as the row grows it.
 *
 * `grow` builds the shape at a size of the dimension whose growth costs: the items of a list, the
 * lines after a marker, the brackets of a run, the blank lines under a nesting, the openers of a
 * `:::` run. A shape whose nesting is fixed keeps it while its blank lines shrink, because what
 * costs there is the blank lines times the blocks open above them.
 */
export type WorkShape = {
  /** What the body is, as a row's name says it. */
  name: string;
  /** The body at `size` of its growing dimension. */
  grow: (size: number) => string;
  /** The size at which the body is the one the row stands for. */
  full: number;
  /** How many lazy lines the body holds at `size`: each takes at least one lazy-line step. */
  lazyLines?: (size: number) => number;
  /**
   * Whether the body opens a list, a quote or a `:::` container, whose opening reads
   * `nestingScanSteps`'s walk at least once.
   */
  opensContainers: boolean;
  /** Whether the body opens a `:::` container, whose parse reads `directiveScanSteps`'s walks at
   *  least once, in a mode that refuses the body too: the refusal comes after them. */
  opensDirectives?: boolean;
  /** How many blocks of HTML the body holds at `size`: the HTML (flow) resolver reads at least
   *  one event of each. */
  htmlBlocks?: (size: number) => number;
  /** How many asks the body holds at `size`, each a mark its runner opens, in a parse that
   *  returns a document. */
  asks?: (size: number) => number;
  /**
   * The least size at which the body grows as it does at the full size, where a part that does not
   * grow (a nesting bound) is all of the body below it: the stages are not read at a size under it.
   */
  smallest?: number;
};

/** The counts the code keeps, by what a failure names each. */
type KeptCounts = {
  readonly tokenizer: number;
  readonly lazyLine: number;
  readonly labelEnd: number;
  readonly nesting: number;
  readonly directive: number;
  readonly textResolve: number;
  readonly attentionResolve: number;
  readonly labelEndResolve: number;
  readonly headingAtxResolve: number;
  readonly setextUnderlineResolve: number;
  readonly codeTextResolve: number;
  readonly contentHandedBack: number;
  readonly resolvedHandedOn: number;
  readonly postprocessedHandedOn: number;
  readonly runnerCalls: number;
  readonly markOpens: number;
  readonly htmlFlowResolve: number;
  readonly blockIdWalk: number;
  readonly leafMarkWalk: number;
};

/** What each kept count is, as a failure names it. */
const countNames: Record<keyof KeptCounts, string> = {
  tokenizer: "tokenizer steps (codes handed a state)",
  lazyLine: "lazy-line steps",
  labelEnd: "label-end steps",
  nesting: "nesting-scan steps",
  directive: "directive-scan steps",
  textResolve: "text-resolver steps",
  attentionResolve: "attention-resolver steps",
  labelEndResolve: "label-end-resolver steps",
  headingAtxResolve: "ATX-heading-resolver steps",
  setextUnderlineResolve: "setext-underline-resolver steps",
  codeTextResolve: "code-text-resolver steps",
  contentHandedBack: "events subtokenize handed content",
  resolvedHandedOn: "events the tokenizers' resolvers handed on",
  postprocessedHandedOn: "events postprocess handed the mdast compile",
  runnerCalls: "Milkdown runner calls",
  markOpens: "marks Milkdown's runners opened",
  htmlFlowResolve: "HTML (flow) resolver steps",
  blockIdWalk: "nodes withBlockIds visited",
  leafMarkWalk: "withoutLeafMarks steps",
};

/** The counts the code keeps, read now. */
function readKeptCounts(): KeptCounts {
  const counted = workCounts();
  return {
    tokenizer: counted.tokenizer,
    lazyLine: counted.lazyLine,
    labelEnd: counted.labelEndScan,
    nesting: nestingScanSteps(),
    directive: directiveScanSteps(),
    textResolve: counted.textResolve,
    attentionResolve: counted.attention,
    labelEndResolve: counted.labelEnd,
    headingAtxResolve: counted.headingAtx,
    setextUnderlineResolve: counted.setextUnderline,
    codeTextResolve: counted.codeText,
    contentHandedBack: counted.content,
    resolvedHandedOn: counted.resolvedEvents,
    postprocessedHandedOn: counted.postprocessedEvents,
    runnerCalls: milkdownRunnerCalls(),
    markOpens: milkdownMarkOpens(),
    htmlFlowResolve: htmlFlowResolveSteps(),
    blockIdWalk: blockIdWalkSteps(),
    leafMarkWalk: leafMarkWalkSteps(),
  };
}

/** The stage subtokenize is while `postprocess` runs it, as a failure names it. */
const POSTPROCESS_SUBTOKENIZE = "subtokenize's steps on the events postprocess hands it";
/** The stage subtokenize is while content's resolver runs it. */
const CONTENT_SUBTOKENIZE = "subtokenize's steps on the events content's resolver hands it";
/** The stage the compile is, once `postprocess` has handed it the events. */
const COMPILE = "the mdast compile's steps on the events";

/** The steps of each stage counted through a Proxy in the parse being read; undefined between
 *  readings, when every stage gets what it takes as it is. */
let staged: Map<string, number> | undefined;

/** Every Proxy a stage was handed, and what it stands in for. */
const fronted = new WeakMap<object, object>();

/**
 * `root` as a stage is handed it: a Proxy that counts to `stage` (or to the stage it names at each
 * step) every read and every write the stage makes of `root` and of each object it reaches
 * through it. An object the stage writes that it was not handed is its own, and it reads it back
 * as it wrote it, so the stage's own identity checks hold; what it reaches through the Proxy it
 * reaches through one Proxy for each object, so they hold there too.
 */
function counted<T extends object>(stage: string | (() => string), root: T): T {
  const proxies = new WeakMap<object, object>();
  const owned = new WeakSet<object>();
  const step = (): void => {
    if (staged === undefined) return;
    const name = typeof stage === "string" ? stage : stage();
    staged.set(name, (staged.get(name) ?? 0) + 1);
  };
  const front = (value: unknown): unknown => {
    if (typeof value !== "object" || value === null || fronted.has(value) || owned.has(value)) {
      return value;
    }
    const known = proxies.get(value);
    if (known !== undefined) return known;
    const proxy = new Proxy(value, handler);
    proxies.set(value, proxy);
    fronted.set(proxy, value);
    return proxy;
  };
  const handler: ProxyHandler<object> = {
    get(target, key) {
      step();
      return front(Reflect.get(target, key));
    },
    set(target, key, value) {
      step();
      const raw = (typeof value === "object" && value !== null && fronted.get(value)) || value;
      if (typeof raw === "object" && raw !== null && !proxies.has(raw)) owned.add(raw);
      return Reflect.set(target, key, raw);
    },
    deleteProperty(target, key) {
      step();
      return Reflect.deleteProperty(target, key);
    },
    has(target, key) {
      step();
      return Reflect.has(target, key);
    },
    ownKeys(target) {
      step();
      return Reflect.ownKeys(target);
    },
    getOwnPropertyDescriptor(target, key) {
      step();
      return Reflect.getOwnPropertyDescriptor(target, key);
    },
    defineProperty(target, key, descriptor) {
      step();
      return Reflect.defineProperty(target, key, descriptor);
    },
  };
  return front(root) as T;
}

/** What `value` stands for, where it is a Proxy a stage was handed. */
function unfronted<T>(value: T): T {
  return ((typeof value === "object" && value !== null && fronted.get(value)) || value) as T;
}

/**
 * Puts back, everywhere in the mdast tree `tree`, what each Proxy a stage left in it stands for:
 * a node a stage reached through its Proxy and wrote into a node of its own. The next stage, and
 * Milkdown, read the tree itself.
 */
function unfrontTree<T>(tree: T, seen = new WeakSet<object>()): T {
  const raw = unfronted(tree);
  if (typeof raw !== "object" || raw === null || seen.has(raw)) return raw;
  seen.add(raw);
  const node = raw as Record<string, unknown>;
  for (const key of Object.keys(node)) {
    const value = node[key];
    const put = unfrontTree(value, seen);
    if (put !== value) node[key] = put;
  }
  return raw;
}

/**
 * Puts back, in the events `events` and each event's token, what each Proxy a stage left there
 * stands for. A token's own links (`previous`, `next`, `_tokenizer`) reach the tokenizers' whole
 * state, which no walk here follows: only what a stage wrote into them is put back.
 */
function unfrontEvents<T>(events: T): T {
  const raw = unfronted(events);
  if (!Array.isArray(raw)) return raw;
  for (let index = 0; index < raw.length; index++) {
    const event: unknown = unfronted(raw[index]);
    raw[index] = event;
    if (!Array.isArray(event)) continue;
    for (let part = 0; part < event.length; part++) {
      const item: unknown = unfronted(event[part]);
      event[part] = item;
      if (typeof item !== "object" || item === null) continue;
      const fields = item as Record<string, unknown>;
      for (const key of Object.keys(fields)) {
        const value = fields[key];
        const put = unfronted(value);
        if (put !== value) fields[key] = put;
      }
    }
  }
  return raw;
}

// micromark's document tokenizer runs its initializer's `resolveAll` last, at the end of the
// document, and hands on what it returns: postprocess's subtokenize and the compile take the events
// through the Proxy from here. Its file is not among micromark's exports, so it is read by its
// path beside the module those exports load; it is that module, so the object is micromark's own.
const micromarkDocument = (
  (await import(
    join(dirname(Bun.resolveSync("micromark", import.meta.dir)), "lib", "initialize", "document.js")
  )) as { document: { resolveAll?: (events: unknown[]) => unknown[] } }
).document;
micromarkDocument.resolveAll = (events) => {
  if (staged === undefined) return events;
  // Subtokenize's until postprocess hands the events on, which `postprocessedEvents` counts.
  const before = workCount("postprocessedEvents");
  return counted(
    () => (workCount("postprocessedEvents") === before ? POSTPROCESS_SUBTOKENIZE : COMPILE),
    events
  );
};

// Content's resolver hands its events to subtokenize: the construct is the one micromark's flow
// tokenizer attempts, from `micromark-core-commonmark`.
const resolveContent = content.resolve;
if (resolveContent === undefined)
  throw new Error("micromark-core-commonmark's content has no resolver");
content.resolve = (events, context) =>
  staged === undefined
    ? resolveContent(events, context)
    : unfrontEvents(resolveContent(counted(CONTENT_SUBTOKENIZE, events), context));

/** The constructs whose resolvers already hand their events to a counting Proxy. */
const countedConstructs = new WeakSet<object>();
/** The compile handlers and transforms already counted, by the function they wrap. */
const countedFunctions = new WeakSet<object>();

/** A micromark construct as far as its resolvers go. */
type ResolvingConstruct = {
  resolve?: (events: unknown[], context: unknown) => unknown[];
  resolveAll?: (events: unknown[], context: unknown) => unknown[];
  resolveTo?: (events: unknown[], context: unknown) => unknown[];
};

/** Hands each resolver of `construct` its events through a Proxy counting to `stage`. */
function countResolvers(stage: string, construct: ResolvingConstruct): void {
  if (countedConstructs.has(construct)) return;
  countedConstructs.add(construct);
  for (const key of ["resolve", "resolveAll", "resolveTo"] as const) {
    const resolver = construct[key];
    if (resolver === undefined) continue;
    construct[key] = (events, context) =>
      staged === undefined
        ? resolver(events, context)
        : unfrontEvents(resolver(counted(stage, events), context));
  }
}

/** Hands every construct of the micromark extension `extension` to `countResolvers`. */
function countExtension(stage: string, extension: Record<string, unknown>): void {
  for (const field of Object.values(extension)) {
    if (typeof field !== "object" || field === null || Array.isArray(field)) continue;
    for (const constructs of Object.values(field)) {
      for (const construct of Array.isArray(constructs) ? constructs : [constructs]) {
        if (typeof construct === "object" && construct !== null) countResolvers(stage, construct);
      }
    }
  }
}

/** A compile extension of `mdast-util-from-markdown`, as far as its handlers and transforms go. */
type CompileExtension = {
  enter?: Record<string, (this: unknown, token: object) => void>;
  exit?: Record<string, (this: unknown, token: object) => void>;
  transforms?: ((tree: object) => object | undefined)[];
};

/** Hands each handler of `extension` the token and the compile's state, and each transform the
 *  tree, through a Proxy counting to a stage of its own: `plugin`'s handler or transform, by the
 *  token type it handles or the transform's name. */
function countCompileExtension(plugin: string, extension: CompileExtension): void {
  for (const [side, handlers] of [
    ["enter", extension.enter],
    ["exit", extension.exit],
  ] as const) {
    if (handlers === undefined) continue;
    for (const [type, handler] of Object.entries(handlers)) {
      if (countedFunctions.has(handler)) continue;
      const stage = `${plugin}'s compile ${side} handler for ${type}: its steps`;
      const wrapped = function (this: unknown, token: object): void {
        if (staged === undefined) handler.call(this, token);
        else handler.call(counted(stage, this as object), counted(stage, token));
      };
      countedFunctions.add(wrapped);
      handlers[type] = wrapped;
    }
  }
  extension.transforms = extension.transforms?.map((transform, index) => {
    if (countedFunctions.has(transform)) return transform;
    const stage = `${plugin}'s compile transform ${transform.name === "" ? index : transform.name}: its steps on the tree`;
    const wrapped = (tree: object) => {
      if (staged === undefined) return transform(tree);
      const raw = unfrontTree(tree);
      const result = transform(counted(stage, raw));
      unfrontTree(raw);
      return result === undefined ? undefined : unfrontTree(result);
    };
    countedFunctions.add(wrapped);
    return wrapped;
  });
}

/** The extensions whose parse work the rows count by Proxy, by the plugin that installs them. */
const COUNTED_EXTENSIONS = new Set(["remarkGfm", "remarkFrontmatter"]);

/**
 * `observeParser` for the rows (src/lib-headless.ts): counts each stage of every parse `processor`
 * runs through a Proxy, while a row reads one. Each plugin's transform is handed the tree, and the
 * GFM and front matter plugins' micromark and compile extensions what each part of them takes.
 */
export function countParseStages(processor: Processor): void {
  processor.attachers.forEach(([attacher, ...settings], index) => {
    const name = attacher.name === "" ? `plugin ${index}` : attacher.name;
    const transformStage = `the mdast transform ${name}'s steps on the tree`;
    const counting = function (this: Processor, ...options: unknown[]) {
      const micromarkBefore = (this.data("micromarkExtensions") ?? []).length;
      const compileBefore = (this.data("fromMarkdownExtensions") ?? []).length;
      const transformer = (attacher as (...options: unknown[]) => unknown).apply(this, options);
      if (COUNTED_EXTENSIONS.has(name)) {
        for (const extension of (this.data("micromarkExtensions") ?? []).slice(micromarkBefore)) {
          countExtension(
            `${name}'s micromark resolvers' steps on the events`,
            extension as Record<string, unknown>
          );
        }
        for (const extension of (this.data("fromMarkdownExtensions") ?? []).slice(compileBefore)) {
          for (const one of Array.isArray(extension) ? extension : [extension]) {
            countCompileExtension(name, one as CompileExtension);
          }
        }
      }
      if (typeof transformer !== "function") return transformer;
      const transform = transformer as (...args: unknown[]) => unknown;
      const wrapped = function (this: unknown, ...args: unknown[]): unknown {
        if (staged === undefined) return transform.apply(this, args);
        const raw = unfrontTree(args[0]);
        const result = transform.apply(this, [
          counted(transformStage, raw as object),
          ...args.slice(1),
        ]);
        unfrontTree(raw);
        return result === undefined ? undefined : unfrontTree(result);
      };
      // unified calls a transformer that declares a third parameter as one that takes `next`.
      Object.defineProperty(wrapped, "length", { value: transform.length });
      return wrapped;
    };
    processor.attachers[index] = [counting as typeof attacher, ...settings];
  });
}

/** The counts of one parse: the code's own, and each stage's steps through its Proxy. */
type WorkCounts = KeptCounts & { readonly stages: ReadonlyMap<string, number> };

/** What one parse of a body counted, and what it threw where it threw. */
type WorkReading<T> = {
  /** The body parsed. */
  readonly body: string;
  /** The size of the shape's growing dimension the body was grown to. */
  readonly size: number;
  readonly counts: WorkCounts;
  /** Whether the stages no patch reaches were handed their counting Proxies in this parse. */
  readonly staged: boolean;
  /** Whether the parse threw. A parse that throws `undefined` has thrown too, which is why this
   *  stands beside `thrown` rather than being read off it. */
  readonly threw: boolean;
  /** What the parse threw, `undefined` where it threw nothing. */
  readonly thrown: unknown;
  /** What the parse returned, `undefined` where it threw. */
  readonly value: T | undefined;
};

/**
 * Parses `shape` once at `size`, reading the counts around the parse, and keeps what it threw;
 * where `staging`, each stage no patch reaches is handed its counting Proxy too, which costs a
 * trap for every read and write a stage makes, so only the smaller sizes are read so.
 */
function readWork<T>(
  shape: WorkShape,
  size: number,
  parse: (body: string) => T,
  staging: boolean
): WorkReading<T> {
  const body = shape.grow(size);
  const stages = new Map<string, number>();
  const before = readKeptCounts();
  let value: T | undefined;
  let threw = false;
  let thrown: unknown;
  staged = staging ? stages : undefined;
  try {
    value = parse(body);
  } catch (cause) {
    threw = true;
    thrown = cause;
  } finally {
    staged = undefined;
  }
  const after = readKeptCounts();
  const moved = Object.fromEntries(
    (Object.keys(after) as (keyof KeptCounts)[]).map((key) => [key, after[key] - before[key]])
  ) as KeptCounts;
  return { body, size, counts: { ...moved, stages }, staged: staging, threw, thrown, value };
}

/**
 * Fails unless each count moved as far as the body says it must: a step for every character
 * tokenized, a lazy-line step for every lazy line, a label-end step for every `]`, and a
 * nesting-scan step where the body opens a container; and, for a parse that returned a document,
 * every stage counted steps. A counter that stopped counting - a patch not applied, a counter read
 * from a second copy of its package, a stage no longer handed its Proxy - reads 0 here, and the
 * bounds below would then pass on nothing.
 */
function expectFloors(what: string, shape: WorkShape, reading: WorkReading<unknown>): void {
  const { body, size, counts } = reading;
  const at = `${what} at ${size} of its growing dimension (${body.length} characters)`;
  expect(
    counts.tokenizer,
    `${at} took ${counts.tokenizer} ${countNames.tokenizer}`
  ).toBeGreaterThanOrEqual(body.length);
  const lazyLines = shape.lazyLines?.(size) ?? 0;
  expect(
    counts.lazyLine,
    `${at} took ${counts.lazyLine} ${countNames.lazyLine} for its ${lazyLines} lazy lines`
  ).toBeGreaterThanOrEqual(lazyLines);
  const closers = body.split("]").length - 1;
  expect(
    counts.labelEnd,
    `${at} took ${counts.labelEnd} ${countNames.labelEnd} for its ${closers} closing brackets`
  ).toBeGreaterThanOrEqual(closers);
  if (shape.opensContainers) {
    expect(
      counts.nesting,
      `${at} opens containers and took no ${countNames.nesting}`
    ).toBeGreaterThan(0);
  }
  if (shape.opensDirectives === true) {
    expect(
      counts.directive,
      `${at} opens \`:::\` containers and took no ${countNames.directive}`
    ).toBeGreaterThan(0);
  }
  const htmlBlocks = shape.htmlBlocks?.(size) ?? 0;
  expect(
    counts.htmlFlowResolve,
    `${at} took ${counts.htmlFlowResolve} ${countNames.htmlFlowResolve} for its ${htmlBlocks} blocks of HTML`
  ).toBeGreaterThanOrEqual(htmlBlocks);
  // A parse that returned a document went through every stage: its text was resolved, its
  // tokenizers and `postprocess` handed events on, Milkdown ran its runners on the tree, and the
  // document was walked for its block ids and its leaves' marks; and, where the stages were handed
  // their Proxies, subtokenize and the compile read the events and every transform read the tree.
  if (!reading.threw) {
    for (const key of [
      "textResolve",
      "resolvedHandedOn",
      "postprocessedHandedOn",
      "runnerCalls",
      "blockIdWalk",
      "leafMarkWalk",
    ] as const) {
      expect(
        counts[key],
        `${at} returned a document and took no ${countNames[key]}`
      ).toBeGreaterThan(0);
    }
    const asks = shape.asks?.(size) ?? 0;
    expect(
      counts.markOpens,
      `${at} returned a document and took ${counts.markOpens} ${countNames.markOpens} for its ${asks} asks`
    ).toBeGreaterThanOrEqual(asks);
    if (reading.staged) {
      const transforms = [...counts.stages.keys()].filter((stage) =>
        stage.startsWith("the mdast transform ")
      );
      expect(
        transforms.length,
        `${at} returned a document and no transform was counted`
      ).toBeGreaterThan(0);
      for (const stage of [POSTPROCESS_SUBTOKENIZE, COMPILE, ...transforms]) {
        expect(
          counts.stages.get(stage) ?? 0,
          `${at} returned a document and took no ${stage}`
        ).toBeGreaterThan(0);
      }
    }
  }
}

/**
 * Fails when the parse at the full size took more than `scanStepsPerCharacter` scan steps or
 * `tokenizerStepsPerCharacter` tokenizer steps per character of its body, naming the steps it took
 * and the steps the body allows.
 */
function expectWorkInProportion(what: string, reading: WorkReading<unknown>): void {
  const { body, counts } = reading;
  const scans = counts.lazyLine + counts.labelEnd + counts.nesting + counts.directive;
  const scansAllowed = scanStepsPerCharacter * body.length;
  expect(
    scans,
    `${what} took ${scans} scan steps (${counts.lazyLine} lazy-line, ${counts.labelEnd} ` +
      `label-end, ${counts.nesting} nesting-scan, ${counts.directive} directive-scan) on ` +
      `${body.length} characters, where at most ${scansAllowed} (${scanStepsPerCharacter} a ` +
      `character) are allowed`
  ).toBeLessThanOrEqual(scansAllowed);
  const tokenizerAllowed = tokenizerStepsPerCharacter * body.length;
  expect(
    counts.tokenizer,
    `${what} took ${counts.tokenizer} ${countNames.tokenizer} on ${body.length} characters, ` +
      `where at most ${tokenizerAllowed} (${tokenizerStepsPerCharacter} a character) are allowed`
  ).toBeLessThanOrEqual(tokenizerAllowed);
}

/**
 * Fails when a count grew more than `growthSlack` times faster from `small` to `full` than the
 * shape's growing dimension did: the kept counts, or, for two staged readings, each stage's steps.
 * A count that read nothing at either size has nothing to grow; one that read nothing at the small
 * size and something at the full one grew without bound.
 */
function expectWorkGrowsLinearly(
  what: string,
  small: WorkReading<unknown>,
  full: WorkReading<unknown>,
  counted: "kept" | "stages"
): void {
  const bound = growthSlack * growthStep;
  const counts: [string, number, number][] =
    counted === "kept"
      ? (Object.keys(countNames) as (keyof KeptCounts)[]).map((key) => [
          countNames[key],
          small.counts[key],
          full.counts[key],
        ])
      : [...new Set([...small.counts.stages.keys(), ...full.counts.stages.keys()])].map((stage) => [
          stage,
          small.counts.stages.get(stage) ?? 0,
          full.counts.stages.get(stage) ?? 0,
        ]);
  for (const [name, from, to] of counts) {
    if (from === 0 && to === 0) continue;
    expect(
      to,
      `${what} took ${to} ${name} at ${full.size} of its growing dimension against ${from} at ` +
        `${small.size}, ${(to / from).toFixed(2)} times for a dimension ${growthStep} times larger ` +
        `(at most ${bound} times)`
    ).toBeLessThanOrEqual(bound * from);
  }
}

/**
 * Parses `shape` at `1/growthStep` of its growing dimension and then at the full size, and holds
 * the two readings to the floors, the full size to the bounds a character, and the kept counts to
 * the growth check; and holds each stage's steps through its Proxy to the same check one step
 * down, from `1/growthStep` squared to `1/growthStep`, where the Proxy's trap on every read and
 * write costs an eighth of what it would at the full size. The assertions both helpers below
 * share.
 */
function workWithinBounds<T>(
  what: string,
  shape: WorkShape,
  parse: (body: string) => T
): { small: WorkReading<T>; full: WorkReading<T> } {
  const smallSize = Math.floor(shape.full / growthStep);
  const leastSize = Math.floor(smallSize / growthStep);
  // A shape whose part that does not grow is the whole body below its `smallest` size reads its
  // stages at the full size and an eighth of it instead, which is the size its cost is read at.
  const stagedAtFull = leastSize < (shape.smallest ?? 1);
  const small = readWork(shape, smallSize, parse, true);
  const full = readWork(shape, shape.full, parse, stagedAtFull);
  const least = stagedAtFull ? undefined : readWork(shape, leastSize, parse, true);
  for (const reading of [least, small, full]) {
    if (reading !== undefined) expectFloors(what, shape, reading);
  }
  expectWorkInProportion(what, full);
  expectWorkGrowsLinearly(what, small, full, "kept");
  if (least === undefined) expectWorkGrowsLinearly(what, small, full, "stages");
  else expectWorkGrowsLinearly(what, least, small, "stages");
  return { small, full };
}

/**
 * What the parse of `shape`'s full body produced, once both sizes have been read and found within
 * the bounds above.
 *
 * A parse that threw where it was to return a document is reported only after the counts have
 * passed, because a break can make a parse both dearer and throwing and the cost is what the row
 * is for. The full size's throw is reported first, since the row reads its document from that
 * side; a small side that threw is reported after it rather than left to pass on the full size
 * alone. The throw reaches the runner as itself, with its own stack.
 */
export function parsedWithinWorkBounds<T>(
  what: string,
  shape: WorkShape,
  parse: (body: string) => T
): T {
  const { small, full } = workWithinBounds(what, shape, parse);
  if (full.threw) throw full.thrown;
  if (small.threw) throw small.thrown;
  if (full.value === undefined) throw new Error("the parse produced no document");
  return full.value;
}

/**
 * Fails unless the parse of one side was refused with an `Error` whose message matches `refused`.
 * A throw that is not an `Error` is thrown on as it is, so it reaches the runner with its own stack
 * rather than as a message that matched nothing.
 */
function expectRefusal(
  what: string,
  side: string,
  reading: WorkReading<unknown>,
  refused: RegExp
): void {
  if (!reading.threw)
    throw new Error(`${what} at the ${side} size was expected to be refused and was not`);
  if (!(reading.thrown instanceof Error)) throw reading.thrown;
  expect(
    reading.thrown.message,
    `${what} at the ${side} size was refused by something else`
  ).toMatch(refused);
}

/**
 * Holds a parse that refuses `shape` to the counts above, and then to the refusal itself at both
 * sizes: each must throw an `Error` whose message matches `refused`.
 *
 * The counts are asserted first, for the reason above: a break that makes a refused parse dearer is
 * read as the cost failure the row is for, not as a missing refusal. Both sides are then checked
 * against the pattern, because a side that failed with something else - a `RangeError` from a stack
 * it overflowed, say - is not the refusal the row reads, and its counts are then another failure's.
 */
export function refusalWithinWorkBounds(
  what: string,
  shape: WorkShape,
  parse: (body: string) => unknown,
  refused: RegExp
): void {
  const { small, full } = workWithinBounds(what, shape, parse);
  expectRefusal(what, "full", full, refused);
  expectRefusal(what, "small", small, refused);
}
