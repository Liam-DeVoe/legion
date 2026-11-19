/** The rows of the test files below start processes and wait for them, and each states its limit
 *  as the sum of what it waits for (`rowMs` of `packages/test-fixtures/src/row-waits.ts`), and
 *  counts those waits as it makes them (`rowOf`). A row that states no limit runs under Bun's own
 *  5 s, which load runs past: the `--cli` row of `dispatch-keyed-comment.test.ts` did, in 4 of 20
 *  runs on a shared Linux machine on 2026-11-19. So this file reads their source, and refuses:
 *
 *  - a row or a hook that starts a process and states no limit of its own. A process is started
 *    by `Bun.spawn`, `Bun.spawnSync` or `Bun.$`, or by an export of `node:child_process` under the
 *    name the file imports it by, in the body itself or in a function of the file's own that the
 *    body calls, at any depth. A row's limit is its third argument, or its second where the third
 *    is its body, as Bun reads both; a hook's is its second;
 *  - a row that counts `rowOf(<waits>)` and states anything but `rowMs(<waits>)`, and a row that
 *    states `rowMs(<waits>)` and counts no `rowOf(<waits>)`, compared as the file writes them.
 *
 *  What it does not read: a process started by a function another module exports (a fixture's
 *  helper), and a file limit (`setDefaultTimeout`), which no file below writes. The files are those
 *  of this repository whose rows started processes with no limit of their own when the rule came
 *  in; `packages/dispatch/e2e/row-limits.ts` holds the rows of `packages/dispatch/e2e/__tests__`
 *  that wait for a bounded child to its own rule. */

import { expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import * as ts from "typescript";

const root = join(import.meta.dir, "..", "..", "..");

/** The test files this file reads, from the root of the repository. */
const files = [
  "scripts/e2e/lib/dispatch-keyed-comment.test.ts",
  "scripts/e2e/lib/dispatch-live-events.test.ts",
  "scripts/e2e/lib/leftovers.test.ts",
  "scripts/e2e/lib/nats-image.test.ts",
  "scripts/e2e/lib/released-port-probe.test.ts",
  "scripts/e2e/lib/smoke-cleanup.test.ts",
  "scripts/e2e/lib/sweep-order.test.ts",
  "packages/pi-envoy/extensions/envoy.test.ts",
  "packages/dispatch/e2e/__tests__/harness-postgres-removal.test.ts",
];

/** Bun's own calls that start a process, as a file writes them. */
const bunStarts = new Set(["Bun.spawn", "Bun.spawnSync", "Bun.$"]);

/** The exports of `node:child_process` that start a process. */
const childProcessStarts = new Set([
  "exec",
  "execFile",
  "execFileSync",
  "execSync",
  "fork",
  "spawn",
  "spawnSync",
]);

/** The hooks of `bun:test` that take a limit as their second argument. */
const hooks = new Set(["beforeAll", "afterAll", "beforeEach", "afterEach"]);

/** What `limitsOfRows` read of one file: how many rows and hooks, how many of those start a
 *  process, and each that the rule refuses, one line each with its line in the file. */
interface Read {
  readonly rows: number;
  readonly starting: number;
  readonly refused: string[];
}

/** The rows and hooks of `source`, a test file named `file`, as the rule above reads them. */
function limitsOfRows(file: string, source: string): Read {
  const parsed = ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true);
  // The names the file reaches `node:child_process`'s starts by: each import of one, and each
  // namespace or default import of the module.
  const starters = new Set<string>();
  const modules = new Set<string>();
  for (const statement of parsed.statements) {
    if (
      !ts.isImportDeclaration(statement) ||
      !ts.isStringLiteral(statement.moduleSpecifier) ||
      !["node:child_process", "child_process"].includes(statement.moduleSpecifier.text)
    ) {
      continue;
    }
    const clause = statement.importClause;
    if (clause?.name !== undefined) modules.add(clause.name.text);
    const bindings = clause?.namedBindings;
    if (bindings !== undefined && ts.isNamespaceImport(bindings)) modules.add(bindings.name.text);
    if (bindings !== undefined && ts.isNamedImports(bindings)) {
      for (const element of bindings.elements) {
        if (childProcessStarts.has((element.propertyName ?? element.name).text)) {
          starters.add(element.name.text);
        }
      }
    }
  }
  // The functions of the file's own, at any scope, by name.
  const functions = new Map<string, ts.Node>();
  const collect = (node: ts.Node): void => {
    if (ts.isFunctionDeclaration(node) && node.name !== undefined) {
      functions.set(node.name.text, node);
    } else if (
      ts.isVariableDeclaration(node) &&
      ts.isIdentifier(node.name) &&
      node.initializer !== undefined &&
      (ts.isArrowFunction(node.initializer) || ts.isFunctionExpression(node.initializer))
    ) {
      functions.set(node.name.text, node.initializer);
    }
    ts.forEachChild(node, collect);
  };
  collect(parsed);
  // The start `node` makes, as the file writes its callee, where it makes one.
  const startOf = (node: ts.Node): string | undefined => {
    const callee = ts.isCallExpression(node)
      ? node.expression
      : ts.isTaggedTemplateExpression(node)
        ? node.tag
        : undefined;
    if (callee === undefined) return undefined;
    const text = callee.getText(parsed);
    if (bunStarts.has(text)) return text;
    if (ts.isIdentifier(callee) && starters.has(callee.text)) return text;
    if (
      ts.isPropertyAccessExpression(callee) &&
      ts.isIdentifier(callee.expression) &&
      modules.has(callee.expression.text) &&
      childProcessStarts.has(callee.name.text)
    ) {
      return text;
    }
    return undefined;
  };
  // The first start under `body`, itself or through a function of the file's own it calls, each
  // function read once.
  const startUnder = (body: ts.Node, seen: Set<string>): string | undefined => {
    let found: string | undefined;
    const visit = (node: ts.Node): void => {
      if (found !== undefined) return;
      found = startOf(node);
      if (found !== undefined) return;
      if (ts.isCallExpression(node) && ts.isIdentifier(node.expression)) {
        const name = node.expression.text;
        const called = functions.get(name);
        if (called !== undefined && !seen.has(name)) {
          seen.add(name);
          const inner = startUnder(called, seen);
          if (inner !== undefined) {
            found = `${inner} through ${name}`;
            return;
          }
        }
      }
      ts.forEachChild(node, visit);
    };
    visit(body);
    return found;
  };
  // Whether `node` is a body rather than a limit: a function written there, or the name of one of
  // the file's own.
  const isBody = (node: ts.Expression | undefined): boolean =>
    node !== undefined &&
    (ts.isArrowFunction(node) ||
      ts.isFunctionExpression(node) ||
      (ts.isIdentifier(node) && functions.has(node.text)));
  const isRow = (callee: ts.Expression): boolean =>
    ts.isIdentifier(callee)
      ? callee.text === "test" || callee.text === "it"
      : (ts.isPropertyAccessExpression(callee) || ts.isCallExpression(callee)) &&
        isRow(callee.expression);
  // The argument of a call of `name`, as the file writes it, where `node` is one.
  const argumentOf = (node: ts.Node | undefined, name: string): string | undefined =>
    node !== undefined &&
    ts.isCallExpression(node) &&
    ts.isIdentifier(node.expression) &&
    node.expression.text === name
      ? (node.arguments[0]?.getText(parsed) ?? "")
      : undefined;
  let rows = 0;
  let starting = 0;
  const refused: string[] = [];
  const visit = (node: ts.Node): void => {
    if (ts.isCallExpression(node)) {
      const hook =
        ts.isIdentifier(node.expression) && hooks.has(node.expression.text)
          ? node.expression.text
          : undefined;
      const row = hook === undefined && isRow(node.expression) && node.arguments.length >= 2;
      if (hook !== undefined || row) {
        rows += 1;
        const [first, second, third] = node.arguments;
        const [body, limit] =
          hook !== undefined ? [first, second] : isBody(second) ? [second, third] : [third, second];
        const line = parsed.getLineAndCharacterOfPosition(node.getStart(parsed)).line + 1;
        const at = `${file}:${line}`;
        const bodyNode =
          body !== undefined && ts.isIdentifier(body) ? functions.get(body.text) : body;
        const start = bodyNode === undefined ? undefined : startUnder(bodyNode, new Set());
        if (start !== undefined) starting += 1;
        const counted: string[] = [];
        const count = (inner: ts.Node): void => {
          const waits = argumentOf(inner, "rowOf");
          if (waits !== undefined) counted.push(waits);
          ts.forEachChild(inner, count);
        };
        if (body !== undefined) count(body);
        const summed = argumentOf(limit, "rowMs");
        const stated = limit?.getText(parsed) ?? "no limit";
        if (start !== undefined && limit === undefined) {
          refused.push(`${at}: ${hook ?? "a row"} starts ${start} and states no limit`);
        } else if (counted.length > 0 && counted.some((waits) => waits !== summed)) {
          refused.push(`${at}: counts rowOf(${counted.join("), rowOf(")}), states ${stated}`);
        } else if (counted.length === 0 && summed !== undefined) {
          refused.push(`${at}: states ${stated}, counts no rowOf(${summed})`);
        }
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(parsed);
  return { rows, starting, refused };
}

test("each row of these files that starts a process states its limit as the sum of what it waits for, and counts it", () => {
  let starting = 0;
  for (const file of files) {
    const read = limitsOfRows(file, readFileSync(join(root, file), "utf8"));
    // A file read as holding no row is a file the scan could not read, never one that passes.
    expect({ file, rows: read.rows > 0, refused: read.refused }).toEqual({
      file,
      rows: true,
      refused: [],
    });
    starting += read.starting;
  }
  // The rows of these files that start a process when the rule came in, which a scan that no longer
  // sees a start would not find: a floor under the scan.
  expect(starting).toBeGreaterThanOrEqual(36);
});

// The controls of the check above: a copy of a file above whose row states no limit, and planted
// rows and hooks of each shape the rule reads, which it must name, beside the shapes it must pass.
test("a row or a hook that starts a process and states no limit is named, and so is a limit that is not the sum its row counts", () => {
  const keyed = "scripts/e2e/lib/dispatch-keyed-comment.test.ts";
  const text = readFileSync(join(root, keyed), "utf8");
  const unlimited = text.replace("  },\n  rowMs(cliCheck)\n);", "  }\n);");
  expect(unlimited).not.toBe(text);
  expect(limitsOfRows(keyed, unlimited).refused).toEqual([
    expect.stringMatching(
      /^scripts\/e2e\/lib\/dispatch-keyed-comment\.test\.ts:\d+: a row starts Bun\.spawn through run and states no limit$/
    ),
  ]);
  const uncounted = text.replace("rowOf(cliCheck)", "rowOf(oneCheck)");
  expect(limitsOfRows(keyed, uncounted).refused).toEqual([
    expect.stringMatching(/:\d+: counts rowOf\(oneCheck\), states rowMs\(cliCheck\)$/),
  ]);

  const planted = [
    'import { execFileSync as git } from "node:child_process";',
    'import * as child from "node:child_process";',
    "function helper() {\n  return Bun.spawnSync(['true']);\n}",
    'test("a spawn", () => {\n  Bun.spawn(["true"]);\n});',
    'test("a helper", () => {\n  helper();\n});',
    'test.skipIf(false)("a renamed import", () => {\n  git("git", ["status"]);\n});',
    'test("a namespace", async () => {\n  child.spawn("true");\n});',
    'test("the shell", async () => {\n  await Bun.$`true`;\n});',
    "afterAll(() => {\n  helper();\n});",
    'test("a regular expression", () => {\n  /x/.exec("x");\n});',
    'test("a stated limit", () => {\n  helper();\n}, 5_000);',
    'test("a limit first", { timeout: 5_000 }, () => {\n  helper();\n});',
    "afterEach(() => helper(), 5_000);",
    'test("a number", () => {\n  run(rowOf(waits));\n}, 600_000);',
    'test("another sum", () => {\n  run(rowOf(waits));\n}, rowMs(other));',
    'test("a sum it does not count", () => {}, rowMs(waits));',
    'test("the sum", () => {\n  run(rowOf(waits));\n}, rowMs(waits));',
  ].join("\n");
  const read = limitsOfRows("planted.test.ts", planted);
  expect(read.refused).toEqual([
    "planted.test.ts:6: a row starts Bun.spawn and states no limit",
    "planted.test.ts:9: a row starts Bun.spawnSync through helper and states no limit",
    "planted.test.ts:12: a row starts git and states no limit",
    "planted.test.ts:15: a row starts child.spawn and states no limit",
    "planted.test.ts:18: a row starts Bun.$ and states no limit",
    "planted.test.ts:21: afterAll starts Bun.spawnSync through helper and states no limit",
    "planted.test.ts:34: counts rowOf(waits), states 600_000",
    "planted.test.ts:37: counts rowOf(waits), states rowMs(other)",
    "planted.test.ts:40: states rowMs(waits), counts no rowOf(waits)",
  ]);
  expect(read.starting).toBe(9);
});
