import { expect, jest, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import * as ts from "typescript";

import { afterOnFakeClock, onFakeClock } from "./fake-clock";

/**
 * R6 of dispatch://INFRA-983: no unit test synchronises on the real clock. A row that waits for a
 * product timer moves the fake clock (`web/src/__tests__/fake-clock.ts`); a wait that must stay on
 * the real clock says why above it, in the words the kept ones use ("This one stays on the real
 * clock", then the reason), and docs/testing.md, "The clock of a unit test", is the rule.
 *
 * This reads every unit test file and test helper under `web/src` - a `*.test.ts(x)`, a file of a
 * `__tests__` folder, a `*-fixture.ts(x)` or `*-fixtures.ts(x)` helper - for every call that
 * starts a wait of real time, whatever its delay, whatever its callback is and whatever wraps the
 * call: a `setTimeout`, a `setInterval`, a `setImmediate` or a `requestAnimationFrame`, global or
 * a member of any object (`window.setTimeout`, `globalThis.setInterval`,
 * `window.requestAnimationFrame`); the `setTimeout`, `setInterval`, `setImmediate` and
 * `scheduler.wait` of `node:timers` and `node:timers/promises` (or `timers` and
 * `timers/promises`), named, aliased or reached through a namespace or default import
 * (`t.scheduler.wait`); a `Bun.sleep` or a `Bun.sleepSync`; and every local name an import gives
 * one of those (`import { setTimeout as delay }`, `import { setImmediate as nextTurn }`,
 * `import { sleep as pause } from "bun"`). It follows no callback and no resolver: a
 * trace of a resolver through the data flow always misses a form, such as a resolver handed to a
 * helper that calls `setTimeout`, so the call is found where it starts, and so is the timer of a
 * row that stands in for a product's. A call passes in two ways alone. One is `afterOnFakeClock`
 * (`fake-clock.ts`), which refuses the real clock itself: a row that stands in for an answer that
 * takes time to come sets its timer there, and the `setTimeout` inside that helper's own body is
 * passed by its file and its function's name. The other is a comment within `REASON_WINDOW` lines
 * above the call that holds the words "stays on the real clock" and after them a reason of at
 * least `REASON_WORDS` words of letters, on that line or on the comment lines that continue it; a
 * comment that only mentions the real clock passes nothing. The file is read by the TypeScript
 * parser, so a call is found however it is spelled or wrapped (inside `act(...)`, across lines),
 * and a comment or a string that spells one is none. A zero-delay timer is a wait of real time
 * too: what has run by its turn - a timer of the product's, a render, a promise of the library's -
 * depends on how loaded the machine is, so a row waits for the condition its step stands for, or
 * moves the fake clock over the product's own zero-delay timer with `tick(0)`. A frame and a turn
 * of the loop are of the same kind (dispatch://INFRA-983, the architect's ruling): a row that
 * awaits a `requestAnimationFrame` or a `setImmediate` waits one turn for a thing to have
 * happened, instead of waiting for the thing. Bun's fake clock holds neither: happy-dom runs a
 * frame as a `setImmediate` of the real loop, and `jest.advanceTimersByTime` runs neither, so a
 * row waits for what the frame or the turn brings, and a wait that has nothing else to wait for
 * says why it stays. The scan reads a test's own calls: a product function the row tests that sets
 * a timer itself is the product's code, not a wait of the row's. The act-rule inputs
 * (`*.fixture.tsx`) are written to break rules and are left out. A file that imports `bun:test` or
 * Testing Library is a test file or a helper whatever its name, and the scan holds itself to
 * reading every such file.
 */

const webSource = fileURLToPath(new URL("..", import.meta.url));

/** A comment within this many lines above a wait may carry its reason: the kept waits carry it in
 *  the doc comment of the helper that holds them, a few lines above the timer. */
const REASON_WINDOW = 12;

/** The words a comment says a wait stays on the real clock in, and how many words of letters, at
 *  least, the reason after them holds. */
const MARKER = "stays on the real clock";
const REASON_WORDS = 3;

const comment = /^\s*(?:\/\/|\/\*|\*|\{\/\*)/;

/** A word of letters: `why`, `can't` and `fake-clock` are one each, `(INFRA-1505)` and `500`
 *  none. */
const wordOfLetters = /(?<![\w-])[A-Za-z]+(?:['’-][A-Za-z]+)*(?![\w-])/g;

/** The calls that start a wait of real time by their name alone, global, a member of any object,
 *  or under the name an import gives them: the timers, the turn of `setImmediate` and the frame
 *  of `requestAnimationFrame`. */
const waitCalls: ReadonlySet<string> = new Set([
  "setTimeout",
  "setInterval",
  "setImmediate",
  "requestAnimationFrame",
]);

/** The modules whose `setTimeout`, `setInterval`, `setImmediate` and `scheduler.wait` wait real
 *  time. */
const timerModules = ["node:timers", "node:timers/promises", "timers", "timers/promises"];

/** The one timer passed by its place: the `setTimeout` in the body of `afterOnFakeClock`, which
 *  refuses the real clock before it sets it. */
const fakeClockFile = "__tests__/fake-clock.ts";
const fakeClockHelper = "afterOnFakeClock";

/** Every `.ts(x)` file under `directory` but the act-rule inputs (`*.fixture.tsx`). */
function sourceFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) return entry.name === "node_modules" ? [] : sourceFiles(path);
    if (!/\.tsx?$/.test(entry.name) || /\.fixture\.tsx?$/.test(entry.name)) return [];
    return [path];
  });
}

/** Whether `path` is a unit test file or a test helper, by its name or its folder. */
function isTestFile(path: string): boolean {
  return (
    /\.test\.tsx?$/.test(path) ||
    /-fixtures?\.tsx?$/.test(path) ||
    path.split("/").includes("__tests__")
  );
}

interface RealWait {
  readonly site: string;
  readonly reasoned: boolean;
}

/** The names a file's calls start a wait of real time through, by what its imports bind. */
interface WaitNames {
  /** Names whose call starts one: each of `waitCalls`, and every local name an import gives one of
   *  them, or `sleep` or `sleepSync` of "bun" (`delay`, `nextTurn`, `pause`). */
  readonly calls: Set<string>;
  /** Names of a timers module whole (`t` of `import * as t from "node:timers/promises"`), whose
   *  `scheduler.wait` starts one; its `setTimeout` and `setImmediate` are members of `waitCalls`,
   *  found as any is. */
  readonly timers: Set<string>;
  /** Names of a timers module's `scheduler`, whose `wait` starts one. */
  readonly schedulers: Set<string>;
  /** Names of "bun" whole, `Bun` among them, whose `sleep` and `sleepSync` start one. */
  readonly buns: Set<string>;
}

/** The names `source`'s imports give the calls that start a wait of real time. */
function waitNames(source: ts.SourceFile): WaitNames {
  const names: WaitNames = {
    buns: new Set(["Bun"]),
    calls: new Set(waitCalls),
    schedulers: new Set(),
    timers: new Set(),
  };
  for (const statement of source.statements) {
    if (!ts.isImportDeclaration(statement) || !ts.isStringLiteral(statement.moduleSpecifier)) {
      continue;
    }
    const from = statement.moduleSpecifier.text;
    const timers = timerModules.includes(from);
    const whole = from === "bun" ? names.buns : timers ? names.timers : undefined;
    const clause = statement.importClause;
    if (clause?.name !== undefined) whole?.add(clause.name.text);
    const bindings = clause?.namedBindings;
    if (bindings === undefined) continue;
    if (ts.isNamespaceImport(bindings)) {
      whole?.add(bindings.name.text);
      continue;
    }
    for (const element of bindings.elements) {
      const imported = (element.propertyName ?? element.name).text;
      if (waitCalls.has(imported)) {
        names.calls.add(element.name.text);
      } else if (from === "bun" && (imported === "sleep" || imported === "sleepSync")) {
        names.calls.add(element.name.text);
      } else if (timers && imported === "scheduler") {
        names.schedulers.add(element.name.text);
      }
    }
  }
  return names;
}

/** The object and the name of a member (`window.setTimeout`, `window["setTimeout"]`). */
function member(expression: ts.Expression): { object: ts.Expression; name: string } | undefined {
  if (ts.isPropertyAccessExpression(expression)) {
    return { name: expression.name.text, object: expression.expression };
  }
  return ts.isElementAccessExpression(expression) &&
    ts.isStringLiteralLike(expression.argumentExpression)
    ? { name: expression.argumentExpression.text, object: expression.expression }
    : undefined;
}

function isOneOf(expression: ts.Expression, names: ReadonlySet<string>): boolean {
  return ts.isIdentifier(expression) && names.has(expression.text);
}

/** Whether a call of `callee` starts a wait of real time, by the names `names` holds. */
function startsWait(callee: ts.Expression, names: WaitNames): boolean {
  if (ts.isIdentifier(callee)) return names.calls.has(callee.text);
  const reached = member(callee);
  if (reached === undefined) return false;
  const { name, object } = reached;
  if (waitCalls.has(name)) return true;
  if (name === "sleep" || name === "sleepSync") {
    return isOneOf(object, names.buns) || member(object)?.name === "Bun";
  }
  if (name !== "wait") return false;
  const owner = member(object);
  return (
    isOneOf(object, names.schedulers) ||
    (owner?.name === "scheduler" && isOneOf(owner.object, names.timers))
  );
}

/** Whether `call` is the `setTimeout` in the body of `fake-clock.ts`'s `afterOnFakeClock`, and in
 *  no function nested in it. */
function isFakeClockTimer(file: string, call: ts.CallExpression): boolean {
  const enclosing = ts.findAncestor(call.parent, ts.isFunctionLike);
  return (
    file === fakeClockFile &&
    enclosing !== undefined &&
    ts.isFunctionDeclaration(enclosing) &&
    enclosing.name?.text === fakeClockHelper
  );
}

/** Whether a comment within `REASON_WINDOW` lines above line `index` says the wait there stays on
 *  the real clock and why: a comment line holds `MARKER`, and the rest of that line with the
 *  comment lines that continue it holds `REASON_WORDS` words of letters or more. */
function saysWhy(lines: readonly string[], index: number): boolean {
  for (let at = Math.max(0, index - REASON_WINDOW); at < index; at += 1) {
    const line = lines[at] ?? "";
    const marker = line.indexOf(MARKER);
    if (marker === -1 || !comment.test(line)) continue;
    let reason = line.slice(marker + MARKER.length);
    for (let next = at + 1; next < index && !reason.includes("*/"); next += 1) {
      const continued = lines[next] ?? "";
      if (!comment.test(continued)) break;
      reason += ` ${continued}`;
    }
    if ((reason.match(wordOfLetters) ?? []).length >= REASON_WORDS) return true;
  }
  return false;
}

/** The calls in `text` that start a wait of real time, each with whether a comment above it says
 *  why it stays on the real clock. */
export function realWaits(file: string, text: string): RealWait[] {
  const source = ts.createSourceFile(
    file,
    text,
    ts.ScriptTarget.Latest,
    true,
    file.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS
  );
  const names = waitNames(source);
  const lines = text.split("\n");
  const found: RealWait[] = [];
  const visit = (node: ts.Node): void => {
    if (
      ts.isCallExpression(node) &&
      startsWait(node.expression, names) &&
      !isFakeClockTimer(file, node)
    ) {
      const index = source.getLineAndCharacterOfPosition(node.getStart(source)).line;
      found.push({
        reasoned: saysWhy(lines, index),
        site: `${file}:${index + 1}: ${(lines[index] ?? "").trim()}`,
      });
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  return found;
}

const sources = sourceFiles(webSource);
const files = sources
  .filter(isTestFile)
  .filter((path) => !path.endsWith("real-clock-waits.test.ts"));
const waits = files.flatMap((path) =>
  realWaits(relative(webSource, path), readFileSync(path, "utf8"))
);

test("the scan reads the unit suite and finds the waits it keeps", () => {
  expect(files.length).toBeGreaterThan(100);
  // Margin.landing.test.tsx's `quiet` stays on the real clock with its reason.
  expect(waits.some((wait) => wait.reasoned)).toBe(true);
});

test("the scan reads every file that imports bun:test or Testing Library", () => {
  const unread = sources
    .filter((path) => !isTestFile(path))
    .filter((path) =>
      /from "(?:bun:test|@testing-library\/[\w-]+)"/.test(readFileSync(path, "utf8"))
    )
    .map((path) => relative(webSource, path));
  expect(unread, "a test helper the scan does not read by its name").toEqual([]);
});

/** A marker a kept wait could carry: the words, and a reason after them. */
const MARKED =
  "// This one stays on the real clock (INFRA-1505): a real long task that no fake clock can stand in for.";

// The reason after the marker words is a sentence: the one word "why." these rows once carried
// says nothing, and the marker's own row below refuses it.
test("a wait of real time is found, a zero-delay one too, and its reason is read from the comment above it", () => {
  const bare = ["await act(async () => {", "  setTimeout(waited.resolve, 50);", "});"].join("\n");
  const kept = [MARKED, bare].join("\n");
  const zero = "setTimeout(resolve, 0);";
  const zeroKept = [MARKED, zero].join("\n");
  expect(realWaits("bare.test.ts", bare).map((wait) => wait.reasoned)).toEqual([false]);
  expect(realWaits("kept.test.ts", kept).map((wait) => wait.reasoned)).toEqual([true]);
  expect(realWaits("zero.test.ts", zero).map((wait) => wait.reasoned)).toEqual([false]);
  expect(realWaits("zero-kept.test.ts", zeroKept).map((wait) => wait.reasoned)).toEqual([true]);
  expect(realWaits("sleep.test.ts", "await Bun.sleep(0);").map((wait) => wait.reasoned)).toEqual([
    false,
  ]);
});

// docs/testing.md, "The clock of a unit test", says this scan fails every timer whatever its delay
// and whatever its callback. These are the spellings of an awaited timer the scan once found by
// tracing the resolver a `new Promise` hands its executor, `r` or `done` as often as `resolve`,
// called from an arrow too; it traces no resolver now, and each is still found once. Each of these
// waits 50 ms of real time.
test("an awaited timer is found whatever its resolver is called and however the timer calls it", () => {
  const spellings = [
    "await new Promise((r) => setTimeout(r, 50));",
    "await new Promise<void>((done) => {\n  setTimeout(done, 50);\n});",
    "await new Promise((resolve) => setTimeout(() => resolve(), 50));",
  ];
  expect(spellings.map((text) => realWaits("spelling.test.ts", text).length)).toEqual([1, 1, 1]);
});

// A timer is found however it is reached and whatever its callback does, each once: with no delay,
// a resolver taken from `Promise.withResolvers` whole or under a name of its own, a member
// `setTimeout`, inside `act(...)`, a resolver stored under another name, a function that calls one,
// and the two this row once listed as found not at all because they settle no promise of the row's:
// a timer that stands in for a product's (`setShown`) and a timer whose callback calls
// `Promise.resolve`. Each waits real time all the same, and a row that waits for one waits on how
// loaded the machine is. A row that stands in for an answer sets its timer with `afterOnFakeClock`,
// which refuses the real clock and is passed; and only a comment or a string that spells a timer is
// found not at all.
test("a timer is found however it is reached and whatever its callback does, and one spelled in a comment or a string is not", () => {
  const found = [
    "await new Promise((resolve) => setTimeout(resolve));",
    "const w = Promise.withResolvers<void>();\nsetTimeout(w.resolve, 50);\nawait w.promise;",
    "const { promise, resolve: land } = Promise.withResolvers<void>();\nsetTimeout(land, 50);\nawait promise;",
    "await new Promise((r) => window.setTimeout(r, 50));",
    "await act(() => new Promise((r) => setTimeout(r, 50)));",
    "let release = () => {};\nconst held = new Promise<void>((r) => {\n  release = r;\n});\nsetTimeout(release, 50);\nawait held;",
    "setTimeout(function () {\n  landed = true;\n  older.resolve(value);\n}, 50);",
    "const timer = setTimeout(() => setShown(true), 50);",
    "setTimeout(() => void Promise.resolve(), 0);",
  ];
  expect(found.map((text) => realWaits("found.test.tsx", text).length)).toEqual(found.map(() => 1));
  expect(realWaits("answer.test.tsx", "afterOnFakeClock(50, () => setShown(true));")).toEqual([]);
  const none = [
    "// await new Promise((r) => setTimeout(r, 50));",
    'const source = "await new Promise((r) => setTimeout(r, 50));";',
  ];
  expect(none.map((text) => realWaits("none.test.tsx", text).length)).toEqual(none.map(() => 0));
});

// docs/testing.md says this scan fails a `Bun.sleep` and an awaited timer. Bun's sleep is also
// imported by name from "bun", and the standard way to wait in Node, which Bun implements, is the
// promise `setTimeout` of "node:timers/promises", often imported under another name. Each of these
// waits 50 ms of real time.
test("Bun's sleep imported by name and the promise setTimeout of node:timers/promises are found", () => {
  const imported = [
    'import { sleep } from "bun";\nawait sleep(50);',
    'import { sleep as pause } from "bun";\nawait pause(50);',
    'import { setTimeout } from "node:timers/promises";\nawait setTimeout(50);',
    'import { setTimeout as delay } from "node:timers/promises";\nawait delay(50);',
  ];
  expect(imported.map((text) => realWaits("imported.test.ts", text).length)).toEqual(
    imported.map(() => 1)
  );
});

// The scan follows no callback, because a trace of a resolver through the data flow always misses
// a form: here the resolver `r` is handed to a helper, whose own `setTimeout` waits 50 ms of real
// time for it. The wait is found where it starts, in the helper, and refused for want of a reason.
test("a timer is found in the helper a resolver is handed to", () => {
  const helper = [
    "function after(ms: number, done: () => void) { setTimeout(done, ms); }",
    "await new Promise<void>((r) => after(50, r));",
  ].join("\n");
  expect(realWaits("helper.test.ts", helper)).toEqual([
    {
      reasoned: false,
      site: "helper.test.ts:1: function after(ms: number, done: () => void) { setTimeout(done, ms); }",
    },
  ]);
});

// Every other call that starts a wait of real time is found once and refused without a reason:
// the timers of node:timers and its promises, under the bare specifiers too, named, aliased, or
// reached through a namespace or a default import; Bun's sleeps, the blocking one included, by
// `Bun` or by an import from "bun"; a `setInterval`; and a `setTimeout` of `globalThis`, of `self`
// or of a window reached by its key. An import alone binds a name and starts no wait.
test("every timer of node:timers, its promises and Bun is found, however an import names it", () => {
  const refused = [
    'import { setTimeout as delay } from "node:timers/promises";\nawait delay(50);',
    'import { scheduler } from "node:timers/promises";\nawait scheduler.wait(50);',
    'import { scheduler as later } from "timers/promises";\nawait later.wait(50);',
    'import * as timers from "node:timers/promises";\nawait timers.setTimeout(50);',
    'import * as timers from "node:timers/promises";\nawait timers.scheduler.wait(50);',
    'import timers from "node:timers";\ntimers.setInterval(run, 50);',
    'import { setTimeout as later } from "timers";\nlater(run, 50);',
    'import { setInterval as every } from "node:timers";\nevery(run, 50);',
    "Bun.sleepSync(50);",
    'import { sleepSync } from "bun";\nsleepSync(50);',
    'import { sleepSync as block } from "bun";\nblock(50);',
    'import * as bun from "bun";\nawait bun.sleep(50);',
    "setInterval(run, 50);",
    "globalThis.setTimeout(run, 50);",
    "self.setInterval(run, 50);",
    'window["setTimeout"](run, 50);',
  ];
  expect(
    refused.map((text) => realWaits("refused.test.ts", text).map((wait) => wait.reasoned))
  ).toEqual(refused.map(() => [false]));
  const imports = [
    'import { setTimeout as delay } from "node:timers/promises";',
    'import { scheduler } from "node:timers/promises";',
    'import { sleep, sleepSync } from "bun";',
  ];
  expect(imports.map((text) => realWaits("imports.test.ts", text).length)).toEqual(
    imports.map(() => 0)
  );
});

// A frame and a turn of the loop are waits of the kind a zero-delay timer is, by the architect's
// ruling on dispatch://INFRA-983: a row that awaits one waits one turn for a thing to have
// happened, instead of waiting for the thing. Each of these is found once and refused without a
// reason: a `requestAnimationFrame` and a `setImmediate`, global, of `window` or of `globalThis`,
// awaited or not, inside `act(...)` too; and the `setImmediate` of node:timers and its promises,
// aliased, or reached through a namespace or a default import. Under the marker words and a
// reason each passes, as a timer does; and a spy on the frame, a cancel of a frame or a turn, and
// an import alone start no wait.
test("a frame and a turn are found, global or a member, and the setImmediate of node:timers however an import names it", () => {
  const refused = [
    "requestAnimationFrame(run);",
    "window.requestAnimationFrame(run);",
    "globalThis.requestAnimationFrame(run);",
    "await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));",
    "await act(() => new Promise<void>((done) => window.requestAnimationFrame(() => done())));",
    "setImmediate(run);",
    "window.setImmediate(run);",
    "globalThis.setImmediate(run);",
    "await new Promise<void>((resolve) => setImmediate(resolve));",
    'import { setImmediate as nextTurn } from "node:timers";\nnextTurn(run);',
    'import { setImmediate as nextTurn } from "node:timers/promises";\nawait nextTurn();',
    'import * as timers from "node:timers";\ntimers.setImmediate(run);',
    'import timers from "timers";\ntimers.setImmediate(run);',
  ];
  expect(
    refused.map((text) => realWaits("refused.test.tsx", text).map((wait) => wait.reasoned))
  ).toEqual(refused.map(() => [false]));
  const kept = [MARKED, "requestAnimationFrame(run);", "setImmediate(run);"].join("\n");
  expect(realWaits("kept.test.tsx", kept).map((wait) => wait.reasoned)).toEqual([true, true]);
  const none = [
    'const frame = spyOn(window, "requestAnimationFrame").mockImplementation(() => 0);',
    "cancelAnimationFrame(frame);",
    "clearImmediate(turn);",
    'import { setImmediate as nextTurn } from "node:timers/promises";',
  ];
  expect(none.map((text) => realWaits("none.test.tsx", text).length)).toEqual(none.map(() => 0));
});

// A call passes when a comment within `REASON_WINDOW` lines above it holds the words "stays on the
// real clock" and after them a reason of `REASON_WORDS` words of letters or more, on that line or
// on the comment lines that continue it. Nothing less passes: a comment that only mentions the
// real clock, the marker words with no reason or a one-word one, a reason in a comment of its own
// below a line that is none, the words in a string, or a marker the wait stands too far below.
test("a wait passes under the marker words and a reason after them, and under nothing less", () => {
  const reasoned = (above: string): boolean[] =>
    realWaits("marked.test.ts", `${above}\nsetTimeout(run, 50);`).map((wait) => wait.reasoned);
  const lines = (count: number): string => Array.from({ length: count }, () => "run();").join("\n");
  const marked = [
    MARKED,
    "// This one stays on the real clock (INFRA-1505)\n// for a long task that no fake clock models.",
    "/** This one stays on the real clock\n *  (INFRA-1505): the margin corrects a card in a frame. */",
    `${MARKED}\n${lines(REASON_WINDOW - 1)}`,
  ];
  const unmarked = [
    "// This waits on the real clock for the long task to end.",
    "// Real clock: a real long task that no fake clock can stand in for.",
    "// This one stays on the real clock (INFRA-1505).",
    "// This one stays on the real clock (INFRA-1505): why.",
    "// This one stays on the real clock (INFRA-1505).\n\n// A real long task that no fake clock can stand in for.",
    'const note = "This one stays on the real clock: a real long task no fake clock stands in for.";',
    `${MARKED}\n${lines(REASON_WINDOW)}`,
  ];
  expect(marked.map(reasoned)).toEqual(marked.map(() => [true]));
  expect(unmarked.map(reasoned)).toEqual(unmarked.map(() => [false]));
});

// A row that stands in for an answer sets its timer with `afterOnFakeClock`, which refuses the real
// clock, so the scan passes the call with no reason. The `setTimeout` inside that helper passes by
// its place alone, `afterOnFakeClock`'s own body in `fake-clock.ts`: the same function in another
// file, another function of that file, and a function nested inside the helper are each found.
test("afterOnFakeClock passes, and so does the timer in its own body in fake-clock.ts, and no other", () => {
  expect(realWaits("answer.test.tsx", "afterOnFakeClock(50, run);")).toEqual([]);
  const helper =
    "export function afterOnFakeClock(ms: number, fn: () => void) {\n  return setTimeout(fn, ms);\n}";
  const other =
    "export function later(ms: number, fn: () => void) {\n  return setTimeout(fn, ms);\n}";
  const nested =
    "export function afterOnFakeClock(ms: number, fn: () => void) {\n  queueMicrotask(() => setTimeout(fn, ms));\n}";
  expect(realWaits("__tests__/fake-clock.ts", helper)).toEqual([]);
  expect(realWaits("__tests__/other-clock.ts", helper)).toHaveLength(1);
  expect(realWaits("__tests__/fake-clock.ts", other)).toHaveLength(1);
  expect(realWaits("__tests__/fake-clock.ts", nested)).toHaveLength(1);
});

// The timer a row sets for an answer it stands in for (`afterOnFakeClock`) is no wait of real
// time: it refuses the real clock, and on the fake one it runs once the clock has moved its delay.
test("afterOnFakeClock refuses the real clock and runs once the fake one has moved its delay", async () => {
  let ran = 0;
  const run = () => {
    ran += 1;
  };
  expect(() => afterOnFakeClock(50, run)).toThrow("runs on the fake clock alone");
  await onFakeClock(async () => {
    afterOnFakeClock(50, run);
    jest.advanceTimersByTime(49);
    expect(ran).toBe(0);
    jest.advanceTimersByTime(1);
    expect(ran).toBe(1);
  });
});

test("no unit test waits on the real clock without saying why", () => {
  expect(waits.filter((wait) => !wait.reasoned).map((wait) => wait.site)).toEqual([]);
});
