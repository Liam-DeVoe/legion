// The citations of the CI files, held to rules under which they stay true when the file they cite
// moves: a line number goes stale when a line is added above it, and nothing else notices.
//
// 1. ci_job_reads.py's own convention, from its docstring: a why cites the step (at()) or the
//    source line that reads it. Where a why cites a line of a file that imports the read, that
//    line holds the import. ci_job_reads.py itself lists every Read of GATES and RULES.
// 2. A comment of ci.yaml names a helper under a package's test/ directory, which other packages'
//    tests import, by a name `grep` finds and never by a line number.
// 3. With CITATIONS_BASE set to the commit a change starts from (HEAD_REF, the commit it ends at,
//    is HEAD unless set): no comment line, and no line of a Markdown or text file, that the change
//    adds cites another file by a line number, but in ci_job_reads.py, whose own convention is
//    rule 1. A line the change also removes is a move, not an addition. A checkout with no history,
//    as CI's is, cannot read the change, so this rule runs only where a reader names the base:
//    CITATIONS_BASE=<base> bun test ./.github/scripts/ci-citations.test.ts
import { expect, test } from "bun:test";
import { existsSync, readFileSync } from "node:fs";
import * as path from "node:path";

const root = path.resolve(import.meta.dir, "..", "..");
const base = process.env.CITATIONS_BASE ?? "";
const head = process.env.HEAD_REF ?? "HEAD";

/** A citation by line: `<path>.<ext>:<n>`, `#L<n>`, a bare `:<n>` in backticks, or "line(s) <n>". */
const CITE =
  /[\w./-]+\.(?:ts|tsx|go|py|yaml|yml|json|md|sh|js|mjs|toml|txt|sql)(?:#L|:)\d+|`:\d+(?:[-,]\d+)*`|\blines? \d+(?:[-,]\d+)*\b/;

function lines(spec: string): number[] {
  return spec.split(",").flatMap((part) => {
    const [from, to] = part.split("-").map(Number);
    if (from === undefined) return [];
    if (to === undefined) return [from];
    return Array.from({ length: to - from + 1 }, (_, i) => from + i);
  });
}

/** The specifiers an import of `read` (a repository path) from `importer` can name it by. */
function specifiers(read: string, importer: string): string[] {
  const names: string[] = [];
  const module = read.replace(/\.tsx?$/, "");
  const inPackage = /^packages\/([^/]+)\/(.+)$/.exec(module);
  if (inPackage) {
    names.push(`@legion/${inPackage[1]}/${inPackage[2]}`);
    names.push(`@legion/${inPackage[1]}/${inPackage[2]?.replace(/^src\//, "")}`);
  }
  const relative = path.relative(path.dirname(importer), module);
  names.push(relative.startsWith(".") ? relative : `./${relative}`);
  return names;
}

test("each line ci_job_reads.py cites in a file that imports the read holds that import", () => {
  const listed = Bun.spawnSync(
    [
      "python3",
      "-c",
      [
        "import json, sys",
        "sys.path.insert(0, '.github/scripts')",
        "import ci_job_reads as c",
        "from ci_filter_coverage import Read",
        "rows = [(g.filter, r.glob, r.why) for g in c.GATES for r in g.reads]",
        "rows += [(s, r.glob, r.why) for s, rs in c.RULES.items() for r in rs if isinstance(r, Read)]",
        "print(json.dumps(rows))",
      ].join("\n"),
    ],
    { cwd: root }
  );
  expect(listed.stderr.toString()).toBe("");
  expect(listed.exitCode).toBe(0);
  const reads = JSON.parse(listed.stdout.toString()) as [string, string, string][];
  let checked = 0;
  const wrong: string[] = [];
  for (const [owner, read, why] of reads) {
    if (!/\.tsx?$/.test(read)) continue;
    for (const cited of why.matchAll(/([\w./@-]+\.tsx?):(\d+(?:[-,]\d+)*)/g)) {
      const named = cited[1] ?? "";
      const file = existsSync(path.join(root, named))
        ? named
        : (Bun.spawnSync(["git", "ls-files", `packages/*/${named}`], { cwd: root })
            .stdout.toString()
            .split("\n")
            .filter(Boolean)
            .at(0) ?? "");
      if (file === "") continue;
      const names = specifiers(read, file);
      const imports = readFileSync(path.join(root, file), "utf8")
        .split("\n")
        .flatMap((line, i) =>
          /\bfrom\s+["']|\bimport\s*\(?\s*["']/.test(line) &&
          names.some((name) => line.includes(`"${name}"`) || line.includes(`'${name}'`))
            ? [i + 1]
            : []
        );
      if (imports.length === 0) continue;
      checked += 1;
      if (!lines(cited[2] ?? "").some((n) => imports.includes(n)))
        wrong.push(
          `${owner}: ${read} cites ${file}:${cited[2]}, whose import of it is at line ${imports.join(",")}`
        );
    }
  }
  expect(checked).toBeGreaterThan(0);
  expect(wrong).toEqual([]);
});

test("a comment of ci.yaml names a package's test helper by a name, not a line", () => {
  const comments = readFileSync(path.join(root, ".github/workflows/ci.yaml"), "utf8")
    .split("\n")
    .map((line, i) => ({ line, n: i + 1 }))
    .filter(({ line }) => /^\s*#.*packages\/[^/\s]+\/test\/[\w./-]+\.tsx?/.test(line));
  expect(comments.length).toBeGreaterThan(0);
  expect(
    comments.filter(({ line }) => CITE.test(line)).map(({ line, n }) => `${n}: ${line.trim()}`)
  ).toEqual([]);
});

/** Whether `text`, a line of `file`, is a comment or a line of prose: its citation is a text's. */
function isText(file: string, text: string): boolean {
  if (/\.(md|txt)$/.test(file)) return true;
  const trimmed = text.trim();
  return /^(\/\/|#|\*|\/\*|<!--|--)/.test(trimmed) || /\s(\/\/|#)\s/.test(trimmed);
}

test.skipIf(base === "")(
  "no comment or text line that the change adds cites another file by a line number",
  () => {
    const diff = Bun.spawnSync(
      ["git", "diff", "--unified=0", "--no-color", "--no-renames", base, head],
      { cwd: root }
    );
    expect(diff.exitCode).toBe(0);
    const added: { file: string; text: string }[] = [];
    const removed = new Set<string>();
    let file: string | undefined;
    let files = 0;
    for (const raw of diff.stdout.toString().split("\n")) {
      if (raw.startsWith("--- ")) continue;
      if (raw.startsWith("+++ ")) {
        file = raw.startsWith("+++ b/") ? raw.slice(6) : undefined;
        files += 1;
      } else if (raw.startsWith("-")) removed.add(raw.slice(1).trim());
      else if (raw.startsWith("+") && file !== undefined) added.push({ file, text: raw.slice(1) });
    }
    expect(files).toBeGreaterThan(0);
    const hits = added
      .filter(({ file }) => file !== ".github/scripts/ci_job_reads.py")
      .filter(({ text }) => !removed.has(text.trim()))
      .filter(({ file, text }) => isText(file, text) && CITE.test(text))
      .map(({ file, text }) => `${file}: ${text.trim()}`);
    expect(hits).toEqual([]);
  }
);
