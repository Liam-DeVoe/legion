import { afterEach, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { temporaryDirectory } from "./temporary-directories";
import { recordTimers, type TimerStep, timersBetween } from "./timer-record";
import { settled, until } from "./wait";

const root = temporaryDirectory("timer-record");
let uninstall: (() => void) | undefined;
afterEach(() => {
  uninstall?.();
  uninstall = undefined;
});

/** A program module under `root`, written for the row, whose timers are the program's own. */
async function programModule(name: string, source: string): Promise<Record<string, unknown>> {
  const file = join(root, name);
  await Bun.write(file, source);
  return (await import(file)) as Record<string, unknown>;
}

test("records the program's timers with the delays it chose, in the order they ran", async () => {
  const steps: TimerStep[] = [];
  uninstall = recordTimers([root], (step) => steps.push(step));
  const program = await programModule(
    "waits.ts",
    [
      "export function later(done: () => void): void { setTimeout(done, 5); }",
      "export function cleared(): void { clearTimeout(setTimeout(() => {}, 60_000)); }",
      "export async function slept(): Promise<void> { await Bun.sleep(3); }",
    ].join("\n")
  );
  const fired = Promise.withResolvers<void>();
  (program["later"] as (done: () => void) => void)(fired.resolve);
  (program["cleared"] as () => void)();
  await (program["slept"] as () => Promise<void>)();
  await fired.promise;
  // The test's own timers are not the program's: the shared wait polls on one of them.
  await until("a poll of the test's own", () => true);
  expect(
    steps.filter((step) => "timer" in step).map((step) => "timer" in step && step.delayMs)
  ).toEqual([5, 60_000, 3]);
  const arming = steps.find((step) => "timer" in step);
  expect(arming !== undefined && "from" in arming ? arming.from : "").toStartWith(
    `${join(root, "waits.ts")}:1`
  );
  expect(
    timersBetween(steps, -1, steps.length)
      .map((timer) => timer.delayMs)
      .sort()
  ).toEqual([3, 5]);
});

test("reads a module loaded with a query after its path as the file it is", async () => {
  // Oh My Pi loads an extension as `file.ts?mtime=…`, and its stack frames name it so.
  const steps: TimerStep[] = [];
  uninstall = recordTimers([root], (step) => steps.push(step));
  const file = join(root, "queried.ts");
  await Bun.write(
    file,
    "export function later(): void { clearTimeout(setTimeout(() => {}, 9)); }\n"
  );
  const program = (await import(`${file}?mtime=1795072600396`)) as { later: () => void };
  program.later();
  expect(steps).toEqual([{ timer: 1, delayMs: 9, from: `${file}:1` }]);
});

test("leaves out the timers of code under no root, a library's or the test's", () => {
  const steps: TimerStep[] = [];
  uninstall = recordTimers([join(root, "elsewhere")], (step) => steps.push(step));
  clearTimeout(setTimeout(() => {}, 10));
  expect(steps).toEqual([]);
});

test("reads between two steps of a record it shares: only timers armed after the first and fired before the second", () => {
  const steps: unknown[] = [
    { timer: 1, delayMs: 1_000, from: "a.ts:1" },
    { reconnected: true },
    { timer: 2, delayMs: 40_000, from: "b.ts:2" },
    { timer: 3, delayMs: 60_000, from: "c.ts:3" },
    { fired: 1 },
    { fired: 2 },
    { shown: "the message" },
    { fired: 3 },
  ];
  expect(timersBetween(steps, 1, 6)).toEqual([{ delayMs: 40_000, from: "b.ts:2" }]);
});

test("the preload appends each step to the file it is given, in a process of its own", async () => {
  const file = join(root, "preloaded.jsonl");
  const program = join(root, "program.ts");
  await Bun.write(program, "setTimeout(() => {}, 7);\n");
  const child = Bun.spawn(
    ["bun", "--preload", join(import.meta.dir, "timer-record-preload.ts"), program],
    {
      env: { ...process.env, LEGION_TIMER_RECORD_FILE: file, LEGION_TIMER_RECORD_ROOTS: root },
      stdout: "ignore",
      stderr: "inherit",
    }
  );
  expect(await settled("the recorded program's exit", child.exited)).toBe(0);
  const steps = readFileSync(file, "utf8")
    .split("\n")
    .filter(Boolean)
    .map((line) => JSON.parse(line) as TimerStep);
  expect(steps).toEqual([{ timer: 1, delayMs: 7, from: `${program}:1` }, { fired: 1 }]);
});
