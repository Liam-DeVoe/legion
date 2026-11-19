// A record of the timers a program arms, for a test that holds the program's way to a fact to the
// delays the program chose on that way, rather than to how long the way took: a loaded machine
// stretches a program's time as much as a test's, so no verdict compares two readings of a clock
// (docs/testing.md, R2 and R4), while a delay a timer was armed with is a number the program
// chose, and a load cannot change it. A defect that makes the program wait - a timer of its own
// on the way, a sleep - shows here with the delay it waits, whatever the machine does.
//
// It wraps `setTimeout`, `setInterval` and `Bun.sleep` of the process it is installed in. A timer
// is recorded only when the code that armed it is the program's own: the first frame of the
// arming stack outside this module lies under one of the roots the test names, and is no test
// file. A library's timers (nats.js's pings, say) and the test's own are left out, since the
// first frame of theirs is the library's or the test's.

import { appendFileSync } from "node:fs";

/** Where the preload (`./timer-record-preload`) appends the record, one JSON line a step. */
export const TIMER_RECORD_FILE = "LEGION_TIMER_RECORD_FILE";
/** The roots of the program's own code, for the preload: absolute directories, `:`-separated. */
export const TIMER_RECORD_ROOTS = "LEGION_TIMER_RECORD_ROOTS";

/** One step of the record: a timer the program armed, with the delay it chose and the line that
 *  armed it, or that timer's callback starting (an interval's, at each tick; a sleep's, as it
 *  resolves). Timers are numbered in the order they were armed. */
export type TimerStep =
  | { readonly timer: number; readonly delayMs: number; readonly from: string }
  | { readonly fired: number };

/** A timer of the record that was armed and fired between two steps of a record it shares. */
export interface TimerOnTheWay {
  readonly delayMs: number;
  readonly from: string;
}

const thisFile = new URL(import.meta.url).pathname;

/**
 * The file and line of one frame of a stack, or undefined for a frame that names no file. A
 * module loaded with a query after its path (Oh My Pi loads an extension as `file.ts?mtime=…`, a
 * test imports `./envoy.ts?nats-stall`) names the file without it.
 */
function frameLocation(frame: string): { readonly file: string; readonly at: string } | undefined {
  const match = /\(?(?:file:\/\/)?(\/[^():?]+)(?:\?[^():]*)?:(\d+)(?::\d+)?\)?\s*$/.exec(frame);
  if (match === null) return undefined;
  const [, file, line] = match;
  if (file === undefined || line === undefined) return undefined;
  return { file, at: `${file}:${line}` };
}

/** Whether `file` is a test's rather than the program's. */
function isTestFile(file: string): boolean {
  return (
    /\.test\.[cm]?[jt]sx?$/.test(file) || file.includes("/__tests__/") || file.includes("/test/")
  );
}

/**
 * Installs the record in this process until the returned function is called: from then on each
 * timer whose arming code lies under one of `roots` (absolute directories) is handed to `write`
 * as it is armed, and again as its callback starts. `write` runs synchronously inside the arming
 * call, so a test that appends its own steps to the same record reads one order of both.
 */
export function recordTimers(
  roots: readonly string[],
  write: (step: TimerStep) => void
): () => void {
  const prefixes = roots.map((root) => (root.endsWith("/") ? root : `${root}/`));
  const original = {
    setTimeout: globalThis.setTimeout,
    setInterval: globalThis.setInterval,
    sleep: Bun.sleep,
  };
  let armed = 0;
  /** The line that armed the timer being armed now, when it is the program's own. */
  const programFrame = (): string | undefined => {
    for (const frame of (new Error().stack ?? "").split("\n").slice(1)) {
      const location = frameLocation(frame);
      if (location === undefined || location.file === thisFile) continue;
      const own = prefixes.some((prefix) => location.file.startsWith(prefix));
      return own && !isTestFile(location.file) ? location.at : undefined;
    }
    return undefined;
  };
  const arm = (delay: unknown): number | undefined => {
    const from = programFrame();
    if (from === undefined) return undefined;
    armed += 1;
    const delayMs = typeof delay === "number" && Number.isFinite(delay) ? Math.max(0, delay) : 0;
    write({ timer: armed, delayMs, from });
    return armed;
  };
  const firing =
    (timer: number, handler: (...args: unknown[]) => unknown) =>
    (...args: unknown[]): unknown => {
      write({ fired: timer });
      return handler(...args);
    };
  const wrapped = (scheduler: typeof setTimeout | typeof setInterval) =>
    ((handler: unknown, delay?: unknown, ...args: unknown[]) => {
      const timer = typeof handler === "function" ? arm(delay) : undefined;
      const callback =
        timer === undefined ? handler : firing(timer, handler as (...args: unknown[]) => unknown);
      return (scheduler as (...args: unknown[]) => unknown)(callback, delay, ...args);
    }) as unknown;
  globalThis.setTimeout = wrapped(original.setTimeout) as typeof setTimeout;
  globalThis.setInterval = wrapped(original.setInterval) as typeof setInterval;
  Bun.sleep = ((until: number | Date) => {
    const timer = typeof until === "number" ? arm(until) : undefined;
    const sleeping = original.sleep(until);
    return timer === undefined ? sleeping : sleeping.then(() => write({ fired: timer }));
  }) as typeof Bun.sleep;
  return () => {
    globalThis.setTimeout = original.setTimeout;
    globalThis.setInterval = original.setInterval;
    Bun.sleep = original.sleep;
  };
}

/**
 * Installs the record as the preload does, when `env` names a file (`TIMER_RECORD_FILE`) and the
 * program's roots (`TIMER_RECORD_ROOTS`): each step is appended to the file as one JSON line. A
 * write that fails is dropped, so the record never changes what the program does.
 */
export function recordTimersFromEnvironment(env: Record<string, string | undefined>): void {
  const file = env[TIMER_RECORD_FILE];
  const roots = (env[TIMER_RECORD_ROOTS] ?? "").split(":").filter((root) => root !== "");
  if (file === undefined || file === "" || roots.length === 0) return;
  recordTimers(roots, (step) => {
    try {
      appendFileSync(file, `${JSON.stringify(step)}\n`);
    } catch {
      // The test's record only: a write it cannot make changes nothing the program does.
    }
  });
}

/** Whether `step`, read from a record it may share with other steps, is a timer's arming. */
function isArming(step: unknown): step is Extract<TimerStep, { timer: number }> {
  return typeof step === "object" && step !== null && "timer" in step && "delayMs" in step;
}

/** Whether `step` is a timer's callback starting. */
function isFiring(step: unknown): step is Extract<TimerStep, { fired: number }> {
  return typeof step === "object" && step !== null && "fired" in step;
}

/**
 * The timers of `steps` armed after the step at index `after` whose callback started before the
 * step at index `before`: the program's waits that can lie between the two, each with the delay
 * the program chose. A timer the program cleared, or one still pending at `before`, delayed
 * nothing on the way and is not among them.
 */
export function timersBetween(
  steps: readonly unknown[],
  after: number,
  before: number
): TimerOnTheWay[] {
  const fired = new Set<number>();
  for (const step of steps.slice(0, Math.max(0, before))) {
    if (isFiring(step)) fired.add(step.fired);
  }
  return steps
    .slice(after + 1, Math.max(after + 1, before))
    .filter(isArming)
    .filter((step) => fired.has(step.timer))
    .map(({ delayMs, from }) => ({ delayMs, from }));
}
