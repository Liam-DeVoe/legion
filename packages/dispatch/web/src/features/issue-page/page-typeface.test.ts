import { expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import * as ts from "typescript";
import { subsetRanges } from "../../fonts/inter/subset-script";

/**
 * The page draws its own words in its typeface, the cut of Inter that `fonts/inter/subset.sh`
 * writes, whose `unicode-range` is that script's UNICODES (`fonts/inter/subset.test.ts`). A
 * character outside UNICODES is drawn in a face of the reader's own machine, at that face's
 * width, or in none. So every string, template and JSX text of the page's own source - each file
 * of this directory but its tests and fixtures, `page-words.ts` among them - holds only
 * characters UNICODES keeps. A comment is not text the page draws, and neither is a regular
 * expression.
 *
 * A shape the page draws as an inline svg is named by a character it never draws as text:
 * `SHAPE_NAMES` lists each such character with the files that name it, and is held to them.
 */

/** `issue-status.ts`'s `statusGlyph` names a status's shape by a character, which `StatusGlyph`
 *  draws as an svg with no text; the page's typeface lacks ◐. */
const SHAPE_NAMES: Readonly<Record<string, readonly string[]>> = {
  "◐": ["StatusGlyph.tsx", "issue-status.ts"],
};

const ranges = subsetRanges();

/** Each string, template part and JSX text of `path`, with the line its text starts on. */
function pageTexts(path: string): { line: number; text: string }[] {
  const source = ts.createSourceFile(
    path,
    readFileSync(join(import.meta.dir, path), "utf8"),
    ts.ScriptTarget.Latest,
    true,
    path.endsWith(".tsx") ? ts.ScriptKind.TSX : ts.ScriptKind.TS
  );
  const texts: { line: number; text: string }[] = [];
  const visit = (node: ts.Node): void => {
    if (ts.isStringLiteral(node) || ts.isTemplateLiteralToken(node) || ts.isJsxText(node)) {
      const { line } = source.getLineAndCharacterOfPosition(node.getStart());
      texts.push({ line: line + 1, text: node.text });
    }
    ts.forEachChild(node, visit);
  };
  visit(source);
  return texts;
}

const pageFiles = readdirSync(import.meta.dir, { encoding: "utf8", recursive: true })
  .filter((path) => /\.tsx?$/.test(path) && !/\.test\.tsx?$/.test(path) && !/fixture/.test(path))
  .sort();

const outside: string[] = [];
const shapesNamed = new Set<string>();
for (const path of pageFiles) {
  for (const { line, text } of pageTexts(path)) {
    for (const character of text) {
      const point = character.codePointAt(0) ?? 0;
      if (ranges.some(([first, last]) => first <= point && point <= last)) continue;
      if (SHAPE_NAMES[character]?.includes(path)) {
        shapesNamed.add(`${character} ${path}`);
        continue;
      }
      outside.push(
        `${path}:${line}: ${character} (U+${point.toString(16).toUpperCase().padStart(4, "0")})`
      );
    }
  }
}

test("the page's own text holds only characters its typeface keeps", () => {
  expect(pageFiles).toContain("page-words.ts");
  expect(outside).toEqual([]);
});

test("each character SHAPE_NAMES lists still names a shape in each file it lists", () => {
  expect([...shapesNamed].sort()).toEqual(
    Object.entries(SHAPE_NAMES)
      .flatMap(([character, paths]) => paths.map((path) => `${character} ${path}`))
      .sort()
  );
});
