import { expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

/**
 * R6 of dispatch://INFRA-983: no unit test synchronises on the real clock. A row that waits for a
 * product timer moves the fake clock (`web/src/__tests__/fake-clock.ts`); a wait that must stay on
 * the real clock says why beside it, in the form the kept ones use ("This one stays on the real
 * clock (INFRA-1505)"), and docs/testing.md, "The clock of a unit test", is the rule.
 *
 * This reads every unit test file and test helper under `web/src` - a `*.test.ts(x)`, a file of a
 * `__tests__` folder, a `*-fixture.ts(x)` helper - for a wait of real time: an awaited timer whose
 * callback is a promise's `resolve` and whose delay is not `0`, or a `Bun.sleep` of more than `0`.
 * A zero-delay timer orders tasks and waits no time, so it is not one. The act-rule inputs
 * (`*.fixture.tsx`) are written to break rules and are left out.
 */

const webSource = fileURLToPath(new URL("..", import.meta.url));

/** A comment within this many lines above a wait may carry its reason: the kept waits carry it in
 *  the doc comment of the helper that holds them, a few lines above the timer. */
const REASON_WINDOW = 12;

const awaitedTimer = /setTimeout\(\s*(?:\w+\.)?resolve\s*,\s*([^)]+?)\s*\)/g;
const sleep = /\bBun\.sleep\(\s*([^)]*?)\s*\)/g;
const comment = /^\s*(?:\/\/|\/\*|\*|\{\/\*)/;

function testFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) return entry.name === "node_modules" ? [] : testFiles(path);
    if (!/\.tsx?$/.test(entry.name) || /\.fixture\.tsx?$/.test(entry.name)) return [];
    const isTest =
      /\.test\.tsx?$/.test(entry.name) ||
      /-fixture\.tsx?$/.test(entry.name) ||
      path.split("/").includes("__tests__");
    return isTest ? [path] : [];
  });
}

interface RealWait {
  readonly site: string;
  readonly reasoned: boolean;
}

/** The waits of real time in `text`, each with whether a comment above it names the real clock. */
export function realWaits(file: string, text: string): RealWait[] {
  const lines = text.split("\n");
  const found: RealWait[] = [];
  for (const pattern of [awaitedTimer, sleep]) {
    for (const match of text.matchAll(pattern)) {
      if ((match[1] ?? "").trim() === "0") continue;
      const index = text.slice(0, match.index).split("\n").length - 1;
      if (comment.test(lines[index] ?? "")) continue;
      const above = lines.slice(Math.max(0, index - REASON_WINDOW), index);
      const reasoned = above.some((line) => comment.test(line) && /real clock/i.test(line));
      found.push({ reasoned, site: `${file}:${index + 1}: ${(lines[index] ?? "").trim()}` });
    }
  }
  return found;
}

const files = testFiles(webSource).filter((path) => !path.endsWith("real-clock-waits.test.ts"));
const waits = files.flatMap((path) =>
  realWaits(relative(webSource, path), readFileSync(path, "utf8"))
);

test("the scan reads the unit suite and finds the waits it keeps", () => {
  expect(files.length).toBeGreaterThan(100);
  // Margin.landing.test.tsx's `quiet` stays on the real clock with its reason.
  expect(waits.some((wait) => wait.reasoned)).toBe(true);
});

test("a wait of real time is found, and its reason is read from the comment above it", () => {
  const bare = ["await act(async () => {", "  setTimeout(waited.resolve, 50);", "});"].join("\n");
  const kept = ["// This one stays on the real clock (INFRA-1505): why.", bare].join("\n");
  const zero = "setTimeout(resolve, 0);";
  expect(realWaits("bare.test.ts", bare).map((wait) => wait.reasoned)).toEqual([false]);
  expect(realWaits("kept.test.ts", kept).map((wait) => wait.reasoned)).toEqual([true]);
  expect(realWaits("zero.test.ts", zero)).toEqual([]);
});

test("no unit test waits on the real clock without saying why", () => {
  expect(waits.filter((wait) => !wait.reasoned).map((wait) => wait.site)).toEqual([]);
});
