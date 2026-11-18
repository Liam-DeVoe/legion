// The census of `callLibrary` (packages/contracts/src/remote-text.ts), the one way this workspace
// calls openid-client, and the TypeScript half of boundary/main.go beside it. It reads, through
// the TypeScript compiler's own checker, every export of openid-client at the version the lockfile
// pins — each function, each class's constructor, and each method of a class's instances and of
// the class itself, any of which can throw — and then every production source file of the
// projects it is given, and fails on each reference to one of them that is not inside the arrow
// function passed alone to `callLibrary`, nor inside a function nested in that arrow, naming the
// line. No list of names is written here: the closed set is the library's.
//
// Usage, from the repository root: bun packages/envoy/internal/redact/testdata/wholeuse/boundary.ts [-api] [tsconfig.json ...]
// With no tsconfig it reads every packages/*/tsconfig.json.
import { readdirSync } from "node:fs";
import { dirname, join, relative, resolve } from "node:path";
import ts from "typescript";

const LIBRARY = "openid-client";

const args = process.argv.slice(2);
const listAPI = args.includes("-api");
const root = process.cwd();
const named = args.filter((arg) => arg !== "-api");
const configs =
  named.length > 0
    ? named
    : readdirSync(join(root, "packages"))
        .map((name) => join(root, "packages", name, "tsconfig.json"))
        .filter((path) => ts.sys.fileExists(path));

/** The symbol a reference names, past an import's alias. */
function target(checker: ts.TypeChecker, symbol: ts.Symbol | undefined): ts.Symbol | undefined {
  if (symbol === undefined) return undefined;
  return symbol.flags & ts.SymbolFlags.Alias ? checker.getAliasedSymbol(symbol) : symbol;
}

/** Each export of the library's module: its functions, constructors and methods, by symbol. */
function libraryAPI(checker: ts.TypeChecker, module: ts.Symbol): Map<ts.Symbol, string> {
  const api = new Map<ts.Symbol, string>();
  for (const exported of checker.getExportsOfModule(module)) {
    const symbol = target(checker, exported);
    if (symbol === undefined) continue;
    const name = exported.getName();
    if (symbol.flags & ts.SymbolFlags.Function) api.set(symbol, `function ${name}`);
    if (!(symbol.flags & ts.SymbolFlags.Class)) continue;
    api.set(symbol, `constructor ${name}`);
    const declaration = symbol.valueDeclaration ?? symbol.declarations?.[0];
    const sides = [checker.getDeclaredTypeOfSymbol(symbol)];
    if (declaration !== undefined)
      sides.push(checker.getTypeOfSymbolAtLocation(symbol, declaration));
    for (const side of sides) {
      for (const member of side.getProperties()) {
        if (member.flags & ts.SymbolFlags.Method)
          api.set(member, `method ${name}.${member.getName()}`);
      }
    }
  }
  return api;
}

/** Whether `node` is the arrow function passed alone to `callLibrary`. */
function boundaryArrow(checker: ts.TypeChecker, node: ts.Node): boolean {
  if (!ts.isArrowFunction(node) || !ts.isCallExpression(node.parent)) return false;
  const call = node.parent;
  if (call.arguments.length !== 1 || call.arguments[0] !== node) return false;
  const callee = target(checker, checker.getSymbolAtLocation(call.expression));
  const file = callee?.declarations?.[0]?.getSourceFile().fileName ?? "";
  return callee?.getName() === "callLibrary" && file.endsWith("/contracts/src/remote-text.ts");
}

/** Whether the reference `node` lies inside a boundary arrow, and in no other function first. */
function insideBoundary(checker: ts.TypeChecker, node: ts.Node): boolean {
  for (let at: ts.Node = node.parent; !ts.isSourceFile(at); at = at.parent) {
    if (boundaryArrow(checker, at)) return true;
    if (ts.isFunctionLike(at)) return false;
  }
  return false;
}

const lines: string[] = [];
const apiCounts = new Map<string, number>();
let apiSize = 0;
let references = 0;
let failed = 0;
let files = 0;
const listed = new Set<string>();
for (const config of configs) {
  const read = ts.readConfigFile(config, ts.sys.readFile);
  if (read.error)
    throw new Error(`${config}: ${ts.flattenDiagnosticMessageText(read.error.messageText, "\n")}`);
  const parsed = ts.parseJsonConfigFileContent(read.config, ts.sys, dirname(resolve(config)));
  const program = ts.createProgram(parsed.fileNames, parsed.options);
  const checker = program.getTypeChecker();
  const resolved = ts.resolveModuleName(
    LIBRARY,
    resolve(config),
    parsed.options,
    ts.sys
  ).resolvedModule;
  const libraryFile =
    resolved === undefined ? undefined : program.getSourceFile(resolved.resolvedFileName);
  const module = libraryFile === undefined ? undefined : checker.getSymbolAtLocation(libraryFile);
  if (module === undefined) continue;
  const api = libraryAPI(checker, module);
  if (apiSize === 0) {
    apiSize = api.size;
    for (const kind of api.values()) {
      const what = kind.split(" ")[0] as string;
      apiCounts.set(what, (apiCounts.get(what) ?? 0) + 1);
      if (listAPI) console.log(`LIBRARY\t${kind}`);
    }
  }
  const fileNames = new Set(parsed.fileNames);
  for (const source of program.getSourceFiles()) {
    const name = source.fileName;
    if (
      source.isDeclarationFile ||
      !fileNames.has(name) ||
      name.includes("/node_modules/") ||
      name.includes("/__tests__/") ||
      name.includes("/test/") ||
      name.endsWith(".test.ts")
    ) {
      continue;
    }
    files++;
    const visit = (node: ts.Node): void => {
      if (
        ts.isIdentifier(node) &&
        !ts.isPartOfTypeNode(node) &&
        !ts.isImportSpecifier(node.parent)
      ) {
        const symbol = target(checker, checker.getSymbolAtLocation(node));
        const kind = symbol === undefined ? undefined : api.get(symbol);
        if (kind !== undefined) {
          const at = source.getLineAndCharacterOfPosition(node.getStart(source));
          const where = `${relative(root, name)}:${at.line + 1}:${at.character + 1}`;
          if (!listed.has(where)) {
            listed.add(where);
            references++;
            const ok = insideBoundary(checker, node);
            if (!ok) failed++;
            lines.push(
              `${ok ? "ok" : "FAIL"}\t${where}\t${kind}\t${ok ? "inside callLibrary" : "not inside the arrow passed alone to callLibrary"}`
            );
          }
        }
      }
      ts.forEachChild(node, visit);
    };
    visit(source);
  }
}
for (const line of lines.sort()) console.log(line);
if (apiSize === 0) {
  console.log(`UNKNOWN: no project resolves ${LIBRARY}, so there is nothing to check`);
  process.exit(2);
}
console.log(
  `API\t${LIBRARY}\t${[...apiCounts].map(([what, count]) => `${count} ${what}s`).join(", ")}`
);
console.log(
  `SUMMARY\tlibrary API=${apiSize}\tproduction files=${files}\treferences=${references}\tfailures=${failed}`
);
process.exit(failed > 0 ? 1 : 0);
