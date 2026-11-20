/** The rules of the source of `v-no-leftover-postgres`'s rows and of the CI steps that run them,
 *  which no row of `harness-postgres-removal.test.ts` sees at run time: the start's removal of a
 *  container reads the time and runs `docker` only through its machine; each row that counts its
 *  waits states its limit as the program's sum of them (`limitsNotSummed` of
 *  `@legion/test-fixtures/row-sums`, the one reading `scripts/e2e/lib/row-waits.test.ts` holds its
 *  own files to); no stand-in lives a fixed time; and each CI step that runs those rows exports when
 *  its own limit ends it. Each reads files and plants copies, and starts no process. */

import { expect, test } from "bun:test";
import { readdirSync, readFileSync } from "node:fs";
import { basename, join } from "node:path";
import { fileURLToPath } from "node:url";
import { limitsNotSummed } from "@legion/test-fixtures/row-sums";
import ts from "typescript";
import { stepEndsVariable } from "./step-end";

/** The globals that read the time or wait, or run a program. */
const clockGlobals = [
  "Date",
  "performance",
  "setTimeout",
  "setInterval",
  "setImmediate",
  "Bun",
  "process",
];

/** The objects a source also reaches a global through, as a member: `globalThis.setTimeout`,
 *  `window["setTimeout"]`. */
const globalObjects = new Set(["globalThis", "global", "window", "self"]);

/** The globals of `clockGlobals`, and every name an import of `node:timers`,
 *  `node:timers/promises` or `node:child_process` binds in `source`. */
function clockNames(source: ts.SourceFile): Set<string> {
  const real = new Set(clockGlobals);
  for (const statement of source.statements) {
    if (
      ts.isImportDeclaration(statement) &&
      ts.isStringLiteral(statement.moduleSpecifier) &&
      /^(node:)?(timers|timers\/promises|child_process)$/.test(statement.moduleSpecifier.text)
    ) {
      const clause = statement.importClause;
      if (clause?.name !== undefined) real.add(clause.name.text);
      const bindings = clause?.namedBindings;
      if (bindings !== undefined && ts.isNamespaceImport(bindings)) real.add(bindings.name.text);
      if (bindings !== undefined && ts.isNamedImports(bindings)) {
        for (const element of bindings.elements) real.add(element.name.text);
      }
    }
  }
  return real;
}

/** Each place under `root`, a node of `source`, that reads a clock or runs a program, as
 *  "<line>:<column> <text>": a name of `real` that is no property's name, the same name as a
 *  member of a global object (`globalObjects`, dotted or indexed), and `AbortSignal.timeout`.
 *  Nothing under a node `skip` answers true for is read. */
function clockReads(
  source: ts.SourceFile,
  root: ts.Node,
  real: ReadonlySet<string>,
  skip: (node: ts.Node) => boolean
): string[] {
  const found: string[] = [];
  const place = (node: ts.Node) => {
    const { line, character } = source.getLineAndCharacterOfPosition(node.getStart(source));
    return `${line + 1}:${character + 1} ${node.getText(source)}`;
  };
  const ofGlobal = (object: ts.Expression) =>
    ts.isIdentifier(object) && globalObjects.has(object.text);
  const visit = (node: ts.Node): void => {
    if (ts.isImportDeclaration(node) || skip(node)) return;
    if (ts.isIdentifier(node) && real.has(node.text)) {
      const { parent } = node;
      // A name after a dot, or the name of a property or a method, is no global and no import.
      const property =
        (ts.isPropertyAccessExpression(parent) && parent.name === node) ||
        ((ts.isPropertyAssignment(parent) ||
          ts.isPropertySignature(parent) ||
          ts.isMethodSignature(parent)) &&
          parent.name === node);
      if (!property) found.push(place(node));
    }
    if (
      (ts.isPropertyAccessExpression(node) &&
        ofGlobal(node.expression) &&
        clockGlobals.includes(node.name.text)) ||
      (ts.isElementAccessExpression(node) &&
        ofGlobal(node.expression) &&
        ts.isStringLiteralLike(node.argumentExpression) &&
        clockGlobals.includes(node.argumentExpression.text)) ||
      (ts.isPropertyAccessExpression(node) &&
        node.name.text === "timeout" &&
        node.expression.getText(source) === "AbortSignal")
    ) {
      found.push(place(node));
    }
    ts.forEachChild(node, visit);
  };
  ts.forEachChild(root, visit);
  return found;
}

/** Each place in `text`, the source of the container removal module, that reads a clock or runs a
 *  program other than through the machine's own (`machine`), and each number in that machine, as
 *  "<line>:<column> <text>": a limit the removal set on the real clock, which the row on the row's
 *  clock cannot see, is one of them (`clockReads`). */
function offTheMachine(text: string): string[] {
  const source = ts.createSourceFile("removal.ts", text, ts.ScriptTarget.Latest, true);
  let machine: ts.Node | undefined;
  for (const statement of source.statements) {
    if (ts.isVariableStatement(statement)) {
      for (const declaration of statement.declarationList.declarations) {
        if (ts.isIdentifier(declaration.name) && declaration.name.text === "machine") {
          machine = declaration;
        }
      }
    }
  }
  if (machine === undefined) return ["no declaration of machine"];
  const numbers: string[] = [];
  const count = (node: ts.Node): void => {
    if (ts.isNumericLiteral(node)) {
      const { line, character } = source.getLineAndCharacterOfPosition(node.getStart(source));
      numbers.push(`${line + 1}:${character + 1} ${node.getText(source)}`);
    }
    ts.forEachChild(node, count);
  };
  count(machine);
  return [
    ...clockReads(source, source, clockNames(source), (node) => node === machine),
    ...numbers,
  ];
}

/** Each function of `text`, the sweep's source (`e2e/harness-postgres-removal.ts`), that the
 *  removal of one container runs under: each that calls `removeContainer`, and each that calls one
 *  of those, at any depth (`under`); and each place in their bodies that reads a clock or runs a
 *  program (`found`, `clockReads`, prefixed with the function's name). A limit put on the removal
 *  by a caller, a timer raced against it, is one of them, and no row on the row's clock, which
 *  calls `removeContainer` itself, runs that caller. */
function aroundTheRemoval(text: string): { under: string[]; found: string[] } {
  const source = ts.createSourceFile("sweep.ts", text, ts.ScriptTarget.Latest, true);
  const functions = new Map<string, ts.FunctionDeclaration>();
  for (const statement of source.statements) {
    if (ts.isFunctionDeclaration(statement) && statement.name !== undefined) {
      functions.set(statement.name.text, statement);
    }
  }
  const calls = (body: ts.Node, names: ReadonlySet<string>): boolean => {
    let found = false;
    const visit = (node: ts.Node): void => {
      if (found) return;
      if (ts.isCallExpression(node) && ts.isIdentifier(node.expression)) {
        found = names.has(node.expression.text);
      }
      ts.forEachChild(node, visit);
    };
    visit(body);
    return found;
  };
  const under = new Set(["removeContainer"]);
  for (let grew = true; grew; ) {
    grew = false;
    for (const [name, declaration] of functions) {
      if (!under.has(name) && calls(declaration, under)) {
        under.add(name);
        grew = true;
      }
    }
  }
  under.delete("removeContainer");
  const real = clockNames(source);
  const found = [...under].flatMap((name) => {
    const declaration = functions.get(name);
    return declaration === undefined
      ? []
      : clockReads(source, declaration, real, () => false).map((place) => `${name} ${place}`);
  });
  return { under: [...under].sort(), found };
}

// The rows of `harness-postgres-removal.test.ts` on the row's clock see a limit of the removal on
// that clock; one on the real clock never fires in their moment of real time, and they call
// `removeContainer` itself, not the sweep that runs it. So this row reads the source: the removal
// module reads the time, waits and runs `docker` only through its machine, and the machine's own
// sets no number; and no function of the sweep that the removal runs under reads a clock at all.
// Its controls are such limits, planted in copies of the sources, which it must name: a timer
// called bare and as a member of the global object, an `AbortSignal.timeout`, a number in the
// machine, and a timer raced against the removal by its caller; and the machine's own timers,
// which it must pass.
test("v-no-leftover-postgres: a start's removal of a dead stack's container reads the time, waits and runs docker only through its machine, which sets no limit of its own, and no function the removal runs under sets one", () => {
  const text = readFileSync(
    fileURLToPath(new URL("../harness-postgres-container-removal.ts", import.meta.url)),
    "utf8"
  );

  expect(offTheMachine(text)).toEqual([]);
  expect(offTheMachine(`${text}\nsetTimeout(() => undefined, 120_000);\n`)).toEqual([
    expect.stringMatching(/^\d+:1 setTimeout$/),
  ]);
  for (const member of ["globalThis.setTimeout", 'globalThis["setTimeout"]', "window.setTimeout"]) {
    expect(offTheMachine(`${text}\n${member}(() => undefined, 120_000);\n`)).toEqual([
      expect.stringMatching(new RegExp(`^\\d+:1 ${member.replace(/[.[\]]/g, "\\$&")}$`)),
    ]);
  }
  expect(
    offTheMachine(`${text}\nconst limit = AbortSignal.any([AbortSignal.timeout(120_000)]);\n`)
  ).toEqual([expect.stringMatching(/^\d+:\d+ AbortSignal\.timeout$/)]);
  const capped = text.replace("sleep(ms, undefined,", "sleep(Math.min(ms, 120_000), undefined,");
  expect(capped).not.toBe(text);
  expect(offTheMachine(capped)).toEqual([expect.stringMatching(/^\d+:\d+ 120_000$/)]);
  // The machine's own timers are the clock the rows move, and pass, as a member of the global
  // object too.
  const ownTimer = text.replace(
    "sleep: (ms, signal) => sleep(ms, undefined, { signal })",
    "sleep: (ms, signal) => new Promise((done) => void globalThis.setTimeout(done, ms, { signal }))"
  );
  expect(ownTimer).not.toBe(text);
  expect(offTheMachine(ownTimer)).toEqual([]);

  const sweep = readFileSync(
    fileURLToPath(new URL("../harness-postgres-removal.ts", import.meta.url)),
    "utf8"
  );
  const read = aroundTheRemoval(sweep);
  // The control that the walk found the callers: the container sweep and the start's sweep.
  expect(read.under).toEqual(
    expect.arrayContaining(["removeContainers", "removeLeftoverPostgres"])
  );
  expect(read.found).toEqual([]);
  const raced = sweep.replace(
    "const removal = await removeContainer(",
    "const removal = await Promise.race([new Promise<never>((_, fail) => setTimeout(fail, 120_000)), removeContainer("
  );
  expect(raced).not.toBe(sweep);
  expect(aroundTheRemoval(raced).found).toEqual([
    expect.stringMatching(/^removeContainers \d+:\d+ setTimeout$/),
  ]);
  const globalCap = sweep.replace(
    "  const self = stackOwner();\n  const left: Left",
    "  globalThis.setTimeout(() => process.exit(1), 120_000);\n  const self = stackOwner();\n  const left: Left"
  );
  expect(globalCap).not.toBe(sweep);
  expect(aroundTheRemoval(globalCap).found).toEqual([
    expect.stringMatching(/^removeLeftoverPostgres \d+:\d+ globalThis\.setTimeout$/),
    expect.stringMatching(/^removeLeftoverPostgres \d+:\d+ process$/),
  ]);
});

/** The test files whose rows count their waits with these fixtures (`rowOf`), which the shared
 *  reading holds to the sum rule (`limitsNotSummed`). */
const summedFiles = [
  "harness-postgres-holders.test.ts",
  "harness-postgres-image-mount.test.ts",
  "harness-postgres-image-port.test.ts",
  "harness-postgres-image-ready.test.ts",
  "harness-postgres-leftover.test.ts",
  "harness-postgres-marks.test.ts",
  "harness-postgres-removal.test.ts",
];

/** The files that start the stand-ins of these rows, or write their scripts: the rows' own files
 *  and their fixtures. */
const standInFiles = [
  ...summedFiles,
  "harness-postgres-leftover-fixtures.ts",
  "harness-postgres-provider-children.ts",
  "harness-postgres-removal-fixtures.ts",
  "harness-postgres-waits.ts",
  "planted-marks.ts",
];

/** The ways a script's text gives a process a life of a number it writes: a `sleep` of a number
 *  that the process `exec`s into, a deadline of bash's `SECONDS` plus a number, and a timer of a
 *  number whose callback does nothing or ends the process, which keeps it alive or ends it. */
const fixedLifeForms = [
  /\bexec\b[^;\n]*\bsleep\s+\d+/,
  /\bSECONDS\s*\+\s*\d+/,
  /setTimeout\(\s*\(\)\s*=>\s*(?:\{\s*\}|process\.exit\([^)]*\))\s*,\s*[\d_]+\s*\)/,
];

/** The stand-ins of `source`, a file named `file` that starts some, whose life is a number the
 *  file writes, one line each, with the line and the text that states it: an argument of a command
 *  that is a number alone (`["sleep", "600"]`, or a copy of `sleep` run with `"600"`), in a script
 *  one of `fixedLifeForms`, and a program run in a script with a number alone for its argument
 *  just after a substitution that names it (`${postgres} 600 </dev/null &`). A stand-in waits for
 *  the row instead: for the end of its parent, the row's process, or of a pipe that process holds
 *  (`untilThisEnds`, `untilParentEnds`, `parentLives` of `planted-marks.ts`), or, where it must
 *  outlive the process that started it, for the time its row has left (`lifeOf`). A poll's step
 *  under a second and the time a stand-in takes to answer a call, which a row sets as what it
 *  tests, are no such life, and none of the forms reads them. */
function fixedLives(file: string, source: string): string[] {
  const parsed = ts.createSourceFile(file, source, ts.ScriptTarget.Latest, true);
  const found: string[] = [];
  const at = (node: ts.Node, what: string) =>
    found.push(
      `${file}:${parsed.getLineAndCharacterOfPosition(node.getStart(parsed)).line + 1}: ${what}`
    );
  const visit = (node: ts.Node): void => {
    if (ts.isArrayLiteralExpression(node)) {
      for (const element of node.elements) {
        if (ts.isStringLiteral(element) && /^\d+$/.test(element.text)) {
          at(element, `"${element.text}" in ${node.getText(parsed)}`);
        }
      }
    }
    if (
      ts.isStringLiteral(node) ||
      ts.isNoSubstitutionTemplateLiteral(node) ||
      ts.isTemplateHead(node) ||
      ts.isTemplateMiddle(node) ||
      ts.isTemplateTail(node)
    ) {
      for (const form of fixedLifeForms) {
        const match = form.exec(node.text);
        if (match !== null) at(node, match[0]);
      }
      // A number, a space and then a redirection or `&`: not a descriptor's own `2>`.
      const after = /^\s+\d+\s+[<>&]/.exec(node.text);
      if (after !== null && (ts.isTemplateMiddle(node) || ts.isTemplateTail(node))) {
        at(node, after[0].trim());
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(parsed);
  return found;
}

// A row's limit is Bun's clock, which no fixture sees: a limit written as a number beside a row
// that counts its waits fires inside the bounds of what the row waits for, as a start of the pool
// given a fixed 600 s did on 2026-11-19 while its removal of one container could wait 600 s. So
// each row of these files states `rowMs` of the waits it counts. The controls are rows planted
// in a copy of a file: one that counts its waits and states a number, and one that states the sum
// of waits it does not count, each of which the check must name.
//
// The same holds of a stand-in's life: the hostile mark's stand-in, given a fixed 600 s, ended
// inside the bounds of the row's third start of the pool, which took 620 s, on 2026-11-19. So no
// stand-in of these files lives a number those files write (`fixedLives`). Its controls are
// stand-ins planted in a copy of a file: one of each form with a fixed life, which the check must
// name by line, and stand-ins that wait for the row, which it must pass. Each planted number comes
// through a substitution, so this file's own text holds none.
test("every row that counts its waits states its limit as the program's sum of those waits, and no stand-in lives a fixed time", () => {
  for (const name of summedFiles) {
    const text = readFileSync(fileURLToPath(new URL(`./${name}`, import.meta.url)), "utf8");
    const read = limitsNotSummed(name, text);
    // A file read as holding no row is one the read could not see into, never one that passes.
    expect({ name, rows: read.calls > 0, found: read.refused }).toEqual({
      name,
      rows: true,
      found: [],
    });
  }
  const planted = [
    "const waits: Waits = { children: 1, reads: 0, bounds: [] };",
    'test.skipIf(false)("a number", async () => {\n  rowOf(waits);\n}, 600_000);',
    'test("another\'s sum", async () => {\n  rowOf(waits);\n}, rowMs(other));',
    'test("a sum it does not count", async () => {}, rowMs(waits));',
    'test("the program\'s", async () => {\n  rowOf(waits);\n}, rowMs(waits));',
    'it("an it row", async () => {\n  rowOf(waits);\n}, 600_000);',
    'test("a limit before its body", rowMs(other), async () => {\n  rowOf(waits);\n});',
  ].join("\n");
  expect(limitsNotSummed("planted.test.ts", planted).refused).toEqual([
    "planted.test.ts:2: counts rowOf(waits), states 600_000",
    "planted.test.ts:5: counts rowOf(waits), states rowMs(other)",
    "planted.test.ts:8: states rowMs(waits), counts no rowOf(waits)",
    "planted.test.ts:12: counts rowOf(waits), states 600_000",
    "planted.test.ts:15: counts rowOf(waits), states rowMs(other)",
  ]);

  for (const name of standInFiles) {
    const text = readFileSync(fileURLToPath(new URL(`./${name}`, import.meta.url)), "utf8");
    expect({ name, found: fixedLives(name, text) }).toEqual({ name, found: [] });
  }
  const fixed = 600;
  const plantedLives = [
    `const held = standIn(tmp, ["sleep", "${fixed}"]);`,
    `const docker = standInDocker(tmp, ['exec -a "$0" sleep ${fixed}']);`,
    `const script = "end=$((SECONDS + ${fixed}))";`,
    `const claimer = "setTimeout(() => process.exit(1), ${fixed}_000);";`,
    `const holder = [process.execPath, "--eval", "setTimeout(() => {}, ${fixed}_000)"];`,
    `const pgCtl = \`\${JSON.stringify(postgres)} ${fixed} </dev/null &\`;`,
    "const lives = standIn(tmp, untilThisEnds());",
    "const outlives = standInDocker(tmp, [neverEnds(row)]);",
    `const pgCtlOfRow = \`end=$((SECONDS + \${lifeOf(row)}))\\n\${postgres} "$((end - SECONDS))" &\`;`,
    "const child = [process.execPath, '--eval', untilParentEnds];",
    "const answers = standInDocker(tmp, ['  ps) sleep 150 & wait $!; echo \"$4\" ;;']);",
  ].join("\n");
  expect(fixedLives("planted.ts", plantedLives)).toEqual([
    `planted.ts:1: "${fixed}" in ["sleep", "${fixed}"]`,
    `planted.ts:2: exec -a "$0" sleep ${fixed}`,
    `planted.ts:3: SECONDS + ${fixed}`,
    `planted.ts:4: setTimeout(() => process.exit(1), ${fixed}_000)`,
    `planted.ts:5: setTimeout(() => {}, ${fixed}_000)`,
    `planted.ts:6: ${fixed} <`,
  ]);
});

/** The workflows of the repository, by file name. */
const workflowsDirectory = fileURLToPath(new URL("../../../../.github/workflows", import.meta.url));

/** A step of a workflow as `Bun.YAML` reads it, with the fields the next check reads. */
interface WorkflowStep {
  readonly name?: string;
  readonly run?: unknown;
  readonly "working-directory"?: unknown;
  readonly "timeout-minutes"?: unknown;
}

/** Each step of `workflows` that runs a row of these files (`summedFiles`), named by its file,
 *  its job and its name, with the files of rows it runs (`steps`), and each such step that gives
 *  those rows no end of its own, one line each (`faults`): one with no `timeout-minutes` of its
 *  own, one whose script does not export `stepEndsVariable` as the time its own limit ends it, in
 *  whole seconds counted from its start, one that exports it after its script's first line, so
 *  what runs before it reads no end, and one whose limit is above its job's. A step runs a row of
 *  a file that its script names, and of each file of `lane`, the files of these the package's
 *  harness lane runs (`test:harness`), where it runs that lane (`bun run test`, `test:harness` or
 *  `test:repeat`) in `packages/dispatch`. A row then ends inside its step (`Row.cut`) and not at
 *  the step's own clock, which names no row. */
function stepsThatCutNoRow(
  workflows: readonly { readonly file: string; readonly text: string }[],
  lane: readonly string[]
): { steps: string[]; faults: string[] } {
  const steps: string[] = [];
  const faults: string[] = [];
  const exports = new RegExp(
    `^export ${stepEndsVariable}=\\$\\(\\( \\$\\(date -u \\+%s\\) \\+ (.+?) \\* 60 \\)\\)$`
  );
  for (const { file, text } of workflows) {
    const parsed = Bun.YAML.parse(text) as {
      jobs?: Record<string, { "timeout-minutes"?: unknown; steps?: WorkflowStep[] }>;
    };
    for (const [job, body] of Object.entries(parsed.jobs ?? {})) {
      for (const [index, step] of (body.steps ?? []).entries()) {
        if (typeof step.run !== "string") continue;
        const run = step.run;
        const runsLane =
          step["working-directory"] === "packages/dispatch" &&
          /\bbun run (?:test|test:harness|test:repeat)\b/.test(run);
        const rows = summedFiles.filter(
          (name) => run.includes(name) || (runsLane && lane.includes(name))
        );
        if (rows.length === 0) continue;
        const where = `${file}: ${job}, step ${JSON.stringify(step.name ?? `${index + 1}`)}, which runs ${rows.join(", ")}`;
        steps.push(where);
        const minutes = step["timeout-minutes"];
        const jobMinutes = body["timeout-minutes"];
        const lines = run.split("\n").map((line) => line.trim());
        const exported = lines.map((line) => exports.exec(line)?.[1]).find((m) => m !== undefined);
        if (minutes === undefined) {
          faults.push(`${where}, has no timeout-minutes of its own, so it gives its rows no end`);
        } else if (exported === undefined) {
          faults.push(`${where}, exports no ${stepEndsVariable}`);
        } else if (exports.exec(lines[0] ?? "") === null) {
          faults.push(`${where}, exports ${stepEndsVariable} after its first line`);
        } else if (exported !== String(minutes)) {
          faults.push(
            `${where}, exports an end ${exported} minutes from its start, and its limit is ${String(minutes)}`
          );
        }
        if (typeof minutes === "number" && typeof jobMinutes === "number" && minutes > jobMinutes) {
          faults.push(`${where}, gives itself ${minutes} minutes, above its job's ${jobMinutes}`);
        }
      }
    }
  }
  return { steps, faults };
}

// The largest row of these files may wait hours within its parts' own bounds, and the CI steps
// that run them give them minutes, by the measured budget of dispatch-browser.yaml's rule. So
// each such step exports when its own limit ends it, and a row whose step ends first ends at that
// end (`Row.cut`), naming the wait it was in, and still removes what it made. This row reads every
// workflow for a step that runs a row of these files and exports no such end. Its controls are
// planted steps: one with no export, one with no limit of its own, one whose export is not its
// limit, one that exports it after its first line, and one as the head's steps are.
test("every CI step that runs these rows exports when its own limit ends it", () => {
  const manifest = JSON.parse(
    readFileSync(fileURLToPath(new URL("../../package.json", import.meta.url)), "utf8")
  ) as { scripts: Record<string, string> };
  const ignored = [
    ...(manifest.scripts["test:harness"] ?? "").matchAll(/--path-ignore-patterns[ =](\S+)/g),
  ].map((match) => basename(match[1] ?? ""));
  const lane = summedFiles.filter((name) => !ignored.includes(name));
  const workflows = readdirSync(workflowsDirectory)
    .filter((name) => name.endsWith(".yaml") || name.endsWith(".yml"))
    .map((name) => ({ file: name, text: readFileSync(join(workflowsDirectory, name), "utf8") }));
  const head = stepsThatCutNoRow(workflows, lane);
  // The control that the read saw the steps: the dispatch-ui job's harness lane, the visual and
  // alone jobs' provider steps, and the weekly repeat's lanes.
  expect(lane.length).toBeGreaterThan(0);
  expect(head.steps.length).toBeGreaterThanOrEqual(4);
  expect(head.faults).toEqual([]);

  const step = (lines: readonly string[]) =>
    [
      "jobs:",
      "  visual:",
      "    timeout-minutes: 14",
      "    steps:",
      "      - name: rows",
      ...lines.map((line) => `        ${line}`),
      "        working-directory: packages/dispatch",
      "",
    ].join("\n");
  const rows = "bun test e2e/__tests__/harness-postgres-leftover.test.ts";
  const export2 = `export ${stepEndsVariable}=$(( $(date -u +%s) + 2 * 60 ))`;
  const where = 'planted.yaml: visual, step "rows", which runs harness-postgres-leftover.test.ts';
  const planted = (lines: readonly string[]) =>
    stepsThatCutNoRow([{ file: "planted.yaml", text: step(lines) }], lane).faults;
  expect(planted(["timeout-minutes: 2", "run: |", `  ${rows}`])).toEqual([
    `${where}, exports no ${stepEndsVariable}`,
  ]);
  expect(planted(["run: |", `  ${export2}`, `  ${rows}`])).toEqual([
    `${where}, has no timeout-minutes of its own, so it gives its rows no end`,
  ]);
  expect(planted(["timeout-minutes: 3", "run: |", `  ${export2}`, `  ${rows}`])).toEqual([
    `${where}, exports an end 2 minutes from its start, and its limit is 3`,
  ]);
  expect(planted(["timeout-minutes: 2", "run: |", `  ${rows}`, `  ${export2}`])).toEqual([
    `${where}, exports ${stepEndsVariable} after its first line`,
  ]);
  expect(planted(["timeout-minutes: 2", "run: |", `  ${export2}`, `  ${rows}`])).toEqual([]);
});
