/**
 * The work a parse does, as the parse itself counts it, for the rows that hold a heavy body's parse
 * to a cost in proportion to its length.
 *
 * A row reads four counts around one parse (packages/proof-editor/AGENTS.md, "The four dependency
 * patches"):
 *
 * - `tokenizerSteps` of `micromark` (`patches/micromark@4.0.2.patch`): one step for each code
 *   micromark's tokenizers hand a state, a code a construct read and gave back counting again each
 *   time it is read again. Every construct runs through it - CommonMark's, the extensions' and the
 *   `list` and `blockQuote` src/block-nesting.ts rewrites - in whatever parser runs it, so it is the
 *   whole tokenization of the parse the row makes: Milkdown's `ParserState`, through remark.
 * - `lazyLineScanSteps` of `micromark`, one step per event the lazy-line test reads.
 * - `labelEndScanSteps` of `micromark-core-commonmark`
 *   (`patches/micromark-core-commonmark@2.0.3.patch`), one per event a label end's search for its
 *   opening `[` reads and one per character its definition lookup reads.
 * - `nestingScanSteps` of src/block-nesting.ts, one per event `openedOnLine` reads: the one loop of
 *   that module outside the tokenizer's states.
 *
 * The last three are the scans that read events rather than codes, which the tokenizer's count does
 * not see. The counts are the code's own record of the work, so a busy machine does not move them,
 * and a parse put back to reading every event or every code again on each line, each `]` or each
 * opener reads a number of steps that grows with the square of the body.
 *
 * A row reads each count at the shape's full size and at `1/growthStep` of its growing dimension,
 * and holds them three ways: each count moved as far as the body says it must (`expectFloors`), the
 * full size took no more steps a character than the highest healthy reading allows
 * (`expectWorkInProportion`), and no count grew more than `growthSlack` times faster than the
 * dimension did (`expectWorkGrowsLinearly`).
 *
 * The counters run over the whole process. A parse is synchronous and the rows of a file run one at
 * a time, so the steps between the two readings are that parse's alone.
 */

import { expect } from "bun:test";
import { lazyLineScanSteps, tokenizerSteps } from "micromark";
import { labelEndScanSteps } from "micromark-core-commonmark";
import { nestingScanSteps } from "../src/block-nesting";

/**
 * The most scan steps - lazy-line, label-end and nesting together - a parse may take per character
 * of its body at the full size.
 *
 * The highest healthy reading is 3.0 a character: a run of 15,000 brackets and as many closers,
 * where each `]` reads the events added since the label end before it stopped looking and the
 * opening below them (89,995 steps on 30,000 characters, in both modes, 2026-11-14). The lazy
 * shapes read 0.5 a character (one event for each lazy line) and the nesting scan at most 0.19 (100
 * items nested by indentation then blank lines, 2026-11-18). This bound is twice the highest. A
 * scan that reads every event again reads a number of steps that grows with the square of the body
 * instead: thousands a character at these lengths.
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
};

/** The four counts of one parse. */
type WorkCounts = {
  readonly tokenizer: number;
  readonly lazyLine: number;
  readonly labelEnd: number;
  readonly nesting: number;
};

/** What each count is, as a failure names it. */
const countNames: Record<keyof WorkCounts, string> = {
  tokenizer: "tokenizer steps (codes handed a state)",
  lazyLine: "lazy-line steps",
  labelEnd: "label-end steps",
  nesting: "nesting-scan steps",
};

function readCounts(): WorkCounts {
  return {
    tokenizer: tokenizerSteps(),
    lazyLine: lazyLineScanSteps(),
    labelEnd: labelEndScanSteps(),
    nesting: nestingScanSteps(),
  };
}

/** What one parse of a body counted, and what it threw where it threw. */
type WorkReading<T> = {
  /** The body parsed. */
  readonly body: string;
  /** The size of the shape's growing dimension the body was grown to. */
  readonly size: number;
  readonly counts: WorkCounts;
  /** Whether the parse threw. A parse that throws `undefined` has thrown too, which is why this
   *  stands beside `thrown` rather than being read off it. */
  readonly threw: boolean;
  /** What the parse threw, `undefined` where it threw nothing. */
  readonly thrown: unknown;
  /** What the parse returned, `undefined` where it threw. */
  readonly value: T | undefined;
};

/** Parses `shape` once at `size`, reading the four counts around the parse, and keeps what it threw. */
function readWork<T>(shape: WorkShape, size: number, parse: (body: string) => T): WorkReading<T> {
  const body = shape.grow(size);
  const before = readCounts();
  let value: T | undefined;
  let threw = false;
  let thrown: unknown;
  try {
    value = parse(body);
  } catch (cause) {
    threw = true;
    thrown = cause;
  }
  const after = readCounts();
  const counts: WorkCounts = {
    tokenizer: after.tokenizer - before.tokenizer,
    lazyLine: after.lazyLine - before.lazyLine,
    labelEnd: after.labelEnd - before.labelEnd,
    nesting: after.nesting - before.nesting,
  };
  return { body, size, counts, threw, thrown, value };
}

/**
 * Fails unless each count moved as far as the body says it must: a step for every character
 * tokenized, a lazy-line step for every lazy line, a label-end step for every `]`, and a
 * nesting-scan step where the body opens a container. A counter that stopped counting - a patch
 * not applied, a counter read from a second copy of its package - reads 0 here, and the bounds
 * below would then pass on nothing.
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
}

/**
 * Fails when the parse at the full size took more than `scanStepsPerCharacter` scan steps or
 * `tokenizerStepsPerCharacter` tokenizer steps per character of its body, naming the steps it took
 * and the steps the body allows.
 */
function expectWorkInProportion(what: string, reading: WorkReading<unknown>): void {
  const { body, counts } = reading;
  const scans = counts.lazyLine + counts.labelEnd + counts.nesting;
  const scansAllowed = scanStepsPerCharacter * body.length;
  expect(
    scans,
    `${what} took ${scans} scan steps (${counts.lazyLine} lazy-line, ${counts.labelEnd} ` +
      `label-end, ${counts.nesting} nesting-scan) on ${body.length} characters, where at most ` +
      `${scansAllowed} (${scanStepsPerCharacter} a character) are allowed`
  ).toBeLessThanOrEqual(scansAllowed);
  const tokenizerAllowed = tokenizerStepsPerCharacter * body.length;
  expect(
    counts.tokenizer,
    `${what} took ${counts.tokenizer} ${countNames.tokenizer} on ${body.length} characters, ` +
      `where at most ${tokenizerAllowed} (${tokenizerStepsPerCharacter} a character) are allowed`
  ).toBeLessThanOrEqual(tokenizerAllowed);
}

/**
 * Fails when a count grew more than `growthSlack` times faster from the small size to the full
 * one than the shape's growing dimension did. A count that read nothing at either size has nothing
 * to grow; one that read nothing at the small size and something at the full one grew without
 * bound.
 */
function expectWorkGrowsLinearly(
  what: string,
  small: WorkReading<unknown>,
  full: WorkReading<unknown>
): void {
  const bound = growthSlack * growthStep;
  for (const key of Object.keys(countNames) as (keyof WorkCounts)[]) {
    const from = small.counts[key];
    const to = full.counts[key];
    if (from === 0 && to === 0) continue;
    expect(
      to,
      `${what} took ${to} ${countNames[key]} at ${full.size} of its growing dimension against ` +
        `${from} at ${small.size}, ${(to / from).toFixed(2)} times for a dimension ${growthStep} ` +
        `times larger (at most ${bound} times)`
    ).toBeLessThanOrEqual(bound * from);
  }
}

/**
 * Parses `shape` at `1/growthStep` of its growing dimension and then at the full size, and holds
 * the two readings to the floors, the full size to the bounds a character, and the counts to the
 * growth check: the assertions both helpers below share.
 */
function workWithinBounds<T>(
  what: string,
  shape: WorkShape,
  parse: (body: string) => T
): { small: WorkReading<T>; full: WorkReading<T> } {
  const small = readWork(shape, Math.floor(shape.full / growthStep), parse);
  const full = readWork(shape, shape.full, parse);
  expectFloors(what, shape, small);
  expectFloors(what, shape, full);
  expectWorkInProportion(what, full);
  expectWorkGrowsLinearly(what, small, full);
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
