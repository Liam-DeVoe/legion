import { expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { DISPATCH_READING_SURFACES } from "@legion/contracts";

/**
 * The gate of the reading list's walk (INFRA-1702's R9, `e2e/reading-rows.ts`): every rendered
 * row of `DISPATCH_READING_SURFACES` has exactly one driver among the `reading-*.e2e.ts` specs,
 * a row titled with the surface's id followed by a colon, and no driver names a row the list
 * does not hold or does not render. A row the list gains with no driver fails here, by name,
 * before any browser runs.
 */

const e2eDirectory = fileURLToPath(new URL("..", import.meta.url));

/** A driver's title, as its spec writes it: `test("<surface>: ...` or `test.fixme("<surface>: ...`,
 *  the string on the call's own line or the next. A row marked fixme still names its surface, so
 *  the defect it waits on is visible here rather than hidden as a missing driver. */
const DRIVER = /\btest(?:\.fixme)?\(\s*["'`]([a-z0-9-]+):/gu;

/** Every driver the reading specs declare, by surface, with the files declaring it. */
function drivers(): Map<string, string[]> {
  const found = new Map<string, string[]>();
  const specs = readdirSync(e2eDirectory).filter(
    (name) => name.startsWith("reading-") && name.endsWith(".e2e.ts")
  );
  for (const spec of specs) {
    const source = readFileSync(`${e2eDirectory}/${spec}`, "utf8");
    for (const match of source.matchAll(DRIVER)) {
      const surface = match[1] ?? "";
      found.set(surface, [...(found.get(surface) ?? []), spec]);
    }
  }
  return found;
}

const declared = drivers();

test.each(
  DISPATCH_READING_SURFACES.filter((row) => row.rendered).map((row) => [row.surface] as const)
)("the rendered surface %s has one driver", (surface) => {
  const files = declared.get(surface) ?? [];
  expect(
    files.length === 1
      ? surface
      : `${surface} has ${files.length} drivers in the reading specs: ${files.join(", ") || "none"}`
  ).toBe(surface);
});

test("every driver names a rendered row of the reading list", () => {
  const rendered = new Set(
    DISPATCH_READING_SURFACES.filter((row) => row.rendered).map((row) => row.surface as string)
  );
  const stray = [...declared.keys()].filter((surface) => !rendered.has(surface));
  expect(stray).toEqual([]);
});

test("every row the list does not render says why", () => {
  const silent = DISPATCH_READING_SURFACES.filter(
    (row) => !row.rendered && (row.reason ?? "").trim() === ""
  ).map((row) => row.surface);
  expect(silent).toEqual([]);
});
