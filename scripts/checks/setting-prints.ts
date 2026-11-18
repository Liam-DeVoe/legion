/**
 * The search for every line that prints a setting's value that can be a URL: a variable of the
 * environment, an argument of the command line or a field of a file that the program reads, which
 * can hold a user and a password.
 *
 *   bun scripts/checks/setting-prints.ts [--all] [--base <rev> | --scope <file>]
 *
 * It follows the DATA, not names. A value is followed from where the program reads it
 * (`process.env`, `Bun.env`, `import.meta.env`, `process.argv`, `Bun.argv`, and what a file read
 * gives) through every name, field, parameter, return value, array, object, template, `+`, `join`
 * and parse error it reaches, whatever those are called, to every line that prints it, logs it or
 * writes it into a command: a `console` call, a write to standard output or standard error, a log
 * call, the message of an error, a `throw`, a child's command line, and the error of a `new URL(…)`
 * that Bun prints whole. The one function, `shownSettingUrl` (and `unshownSettingUrl`, which names
 * a value by its length), ends a value's path, and so does a value whose type cannot hold text (a
 * number, a boolean, a set of literal strings). A `new URL(…)` of a setting writes no line only
 * where a guard runs before it on every path: an `if` before it, in a block around it, that ends
 * the function, the loop or the program when `shownSettingUrl` does not show that same value (or
 * `URL.canParse` refuses it), or a branch (`if`, `?:`, `&&`, `||`) taken only when the judge
 * admits it. The value is a `const`, a parameter its function never assigns, or a member of one
 * that no line of the file assigns; the guard stands in the same function, or in one that makes
 * the arrow or function expression the parse is in after the guard. A judge whose answer nothing
 * reads, or one that runs after the parse, admits nothing.
 *
 * A line it reports is where a setting AS READ is written into text: the template, `+`, `join`,
 * `String` or format that writes it, a parse error that holds it, or a print of the raw value. A
 * print of text that another line built is an end of that line, not a second line. A function's
 * answer is read for each call from what that call hands it: the line that writes a parameter into
 * the answer is that call's line, with that call's settings only, and a setting read inside the
 * function is named for each call that answers with it.
 *
 * Which settings can be a URL is read from the data too: a variable of the environment (by its
 * name, wherever it is read), an argument or a field of a file is one when its value is the URL a
 * URL taker is handed, or the head of its text (`new URL`, `fetch`, a request, a socket, a NATS or
 * Postgres client, ky, the one function), or when it is handed to another program (a child's
 * command line or environment), whose use of it the search cannot read. A read of the environment
 * by a key the search cannot name is always one.
 *
 * The Go half, `scripts/checks/setting-prints.go`, does the same over the Go Dispatch server's
 * command and routes packages, where the one function is `redact.URL`; this program runs it and
 * reads its lines.
 *
 * Its scope is the files this pull request changes (what differs from where the branch left
 * `--base`, origin/main unless named, or the list `--scope` names), and each file whose written
 * text a line of those files prints. Each line it finds there is either fixed, so that it names
 * the value through the one function, or listed in `setting-prints.judged.ts` with why it cannot
 * carry a user part. It exits 0 when every line it finds is judged, and 1 when one is not or when a
 * judgement names a line it no longer finds. Each run also reads planted shapes (`PLANTED`, and the
 * Go half's own): each line marked `found` there must be found and each marked `quiet` must not,
 * else it says UNKNOWN and exits 2, since a search that cannot see those shapes would say the same
 * about the tree whether it looked or not. `--all` prints the judged lines and the lines of
 * settings that reach no URL too.
 *
 * What it reads: every TypeScript file git tracks, but tests (`*.test.ts`, `*.spec.ts`, `*.e2e.ts`,
 * and `__tests__`, `test`, `tests` and `fixtures` directories) and declarations. What it does not
 * follow: a value through a library function other than the string, array, object, path, URL, JSON
 * and parse functions it names (`KEEPS`), so a library's own error is not read; an error a `throw`
 * hands to a catch (the `throw` is an end itself); a call through an interface whose implementation
 * it cannot resolve; a value that leaves through a network answer or a file; JSX. A field of an
 * object type the tree declares is read from every write of it, in every function. A pass is a
 * claim about those shapes and no other.
 */
import { readFileSync } from "node:fs";
import path from "node:path";
import ts from "typescript";
import { JUDGED, type Judged } from "./setting-prints.judged";

const root = path.resolve(import.meta.dir, "../..");

/** The planted shapes. A line that ends `// planted: found` must be found, and one that ends
 *  `// planted: quiet` must not be; a line that is neither must not be found either, since it
 *  writes no setting as read. They are read with the tree, as one more file of it. */
const PLANTED = `import { shownSettingUrl } from "${root}/packages/contracts/src/setting-url";

interface PlantedBanner {
  readonly env: Readonly<Record<string, string | undefined>>;
}
function plantedQuote(value: string): string {
  return /^[a-z]+$/.test(value) ? value : \`'\${value}'\`; // planted: found
}
function plantedCommand(banner: PlantedBanner): string {
  const words = ["PLANTED_URL"].map((name) => \`\${name}=\${plantedQuote(banner.env[name] ?? "")}\`); // planted: found
  return ["PLANTED=1", ...words].join(" ");
}
console.log("run: " + plantedCommand({ env: process.env }));
console.log(plantedQuote("a constant")); // planted: quiet
const plantedArgument = process.argv[2] ?? "";
async function plantedFetch(where: string): Promise<void> {
  await fetch(new URL("/planted", where)); // planted: found
}
await plantedFetch(plantedArgument);
await fetch(process.env.PLANTED_ERROR ?? "");
function plantedError(): never {
  throw new Error(\`planted \${process.env.PLANTED_ERROR}\`); // planted: found
}
plantedError();
const plantedWords = ["planted", Bun.env.PLANTED_JOINED];
process.stderr.write(\`\${plantedWords.join(" ")}\\n\`); // planted: found
console.warn("planted: " + Bun.env.PLANTED_JOINED); // planted: found
try {
  new URL(Bun.env.PLANTED_JOINED ?? ""); // planted: found
} catch (error) {
  console.error(String(error));
}
console.log(\`a home that is never a URL: \${process.env.PLANTED_HOME}\`); // planted: quiet
console.log(\`judged \${shownSettingUrl(process.env.PLANTED_JUDGED ?? "") ?? "nothing"}\`); // planted: quiet
console.log(\`a port \${Number(process.env.PLANTED_ERROR)}\`); // planted: quiet
const plantedAdmitted = process.env.PLANTED_ADMITTED ?? "";
if (shownSettingUrl(plantedAdmitted) === undefined) process.exit(2);
const plantedOrigin = new URL(plantedAdmitted).origin; // planted: quiet
void plantedOrigin;
const plantedIgnored = process.env.PLANTED_IGNORED ?? "";
shownSettingUrl(plantedIgnored);
const plantedIgnoredHost = new URL(plantedIgnored).host; // planted: found
void plantedIgnoredHost;
const plantedLate = process.env.PLANTED_LATE ?? "";
const plantedLateHost = new URL(plantedLate).host; // planted: found
if (shownSettingUrl(plantedLate) === undefined) process.exit(2);
void plantedLateHost;
`;
const plantedFile = path.join(root, "scripts/checks/setting-prints.planted.ts");

/** Whether git's path is one this search reads. */
export function readPath(file: string): boolean {
  if (!/\.[cm]?tsx?$/.test(file) || /\.d\.[cm]?ts$/.test(file)) return false;
  if (/(^|\/)(__tests__|tests?|fixtures?)\//.test(file)) return false;
  return !/[._](test|spec)\.[cm]?tsx?$/.test(file) && !/\.e2e\.tsx?$/.test(file);
}

/** The functions that end a value's path: the one function and its words for a value it hides. */
const SANITIZERS = new Set(["shownSettingUrl", "unshownSettingUrl"]);
/** The calls whose answer, read by a guard, admits a value to a `new URL(…)` of it. */
const JUDGES = new Set(["shownSettingUrl", "canParse"]);
/** Calls and classes that take their argument as a URL, or hand it to another program. */
const URL_TAKERS = new Set([
  "URL",
  "canParse",
  "fetch",
  "Request",
  "WebSocket",
  "EventSource",
  "connect",
  "postgres",
  "Client",
  "Pool",
  "SQL",
  "shownSettingUrl",
  "unshownSettingUrl",
]);

/** Methods whose answer holds their receiver's text: of a string, an array, a map, a URL. */
const KEEPS_RECEIVER = new Set([
  "at",
  "charAt",
  "concat",
  "entries",
  "fill",
  "filter",
  "find",
  "findLast",
  "flat",
  "get",
  "getAll",
  "join",
  "json",
  "keys",
  "match",
  "matchAll",
  "normalize",
  "padEnd",
  "padStart",
  "pop",
  "repeat",
  "replace",
  "replaceAll",
  "reverse",
  "shift",
  "slice",
  "sort",
  "splice",
  "split",
  "substr",
  "substring",
  "text",
  "toLocaleLowerCase",
  "toLocaleString",
  "toLocaleUpperCase",
  "toLowerCase",
  "toReversed",
  "toSorted",
  "toString",
  "toUpperCase",
  "trim",
  "trimEnd",
  "trimStart",
  "values",
  "valueOf",
  "with",
  "catch",
  "finally",
]);
/** Methods that also take text from their arguments. */
const KEEPS_ARGUMENTS = new Set([
  "concat",
  "replace",
  "replaceAll",
  "padEnd",
  "padStart",
  "with",
  "splice",
  "fill",
]);
/** Methods whose answer is what their callback returns, given each element of the receiver. */
const MAPPERS = new Set(["map", "flatMap", "then"]);
/** Methods that hand each element of their receiver to their callback. */
const VISITORS = new Set([
  "map",
  "flatMap",
  "forEach",
  "filter",
  "find",
  "findLast",
  "findIndex",
  "findLastIndex",
  "some",
  "every",
  "sort",
  "toSorted",
  "then",
]);
/** Methods that put their arguments into their receiver. */
const MUTATORS = new Set(["push", "unshift", "add", "set", "append", "splice", "fill"]);
/** Library functions whose answer holds their arguments' text. */
const KEEPS = new Set([
  "String",
  "encodeURIComponent",
  "encodeURI",
  "decodeURIComponent",
  "decodeURI",
  "structuredClone",
  "btoa",
  "atob",
  "escape",
  "unescape",
  "entries",
  "values",
  "fromEntries",
  "assign",
  "freeze",
  "from",
  "of",
  "stringify",
  "parse",
  "safeParse",
  "parseAsync",
  "safeParseAsync",
  "parseArgs",
  "resolve",
  "all",
  "allSettled",
  "race",
  "any",
  "join",
  "relative",
  "dirname",
  "basename",
  "extname",
  "normalize",
  "format",
  "inspect",
  "fileURLToPath",
  "pathToFileURL",
  "exec",
  "dedent",
]);
/** Calls whose answer is text made from their arguments or receiver: a line that writes the value. */
const RENDERS = new Set(["String", "join", "toString", "stringify", "format", "inspect"]);
/** Classes whose instance holds the text it was made from. */
const KEEPING_CLASSES = new Set([
  "URL",
  "URLSearchParams",
  "Map",
  "Set",
  "Headers",
  "Array",
  "String",
]);
/** A call of one of these, with no body the search can read, prints or logs its arguments. */
const LOG_NAMES = new Set([
  "log",
  "info",
  "warn",
  "warning",
  "error",
  "debug",
  "trace",
  "fatal",
  "notice",
  "print",
  "println",
  "say",
  "report",
  "die",
  "abort",
  "panic",
  "complain",
  "usage",
  "refuse",
  "setFailed",
]);
/** A call of one of these writes its arguments into a child's command line. */
const COMMANDS = new Set(["spawn", "spawnSync", "exec", "execSync", "execFile", "execFileSync"]);

/** Whether `callee` starts a child: one of `COMMANDS` called by its name, or as a member of the
 *  child-process module, but not a regular expression's `exec`. */
function childCommand(callee: ts.Expression): boolean {
  const e = strip(callee);
  if (!COMMANDS.has(nameOf(e))) return false;
  return (
    !ts.isPropertyAccessExpression(e) ||
    /^(child_process|childProcess|cp)$/.test(nameOf(e.expression))
  );
}
/** A call of one of these reads a file. */
const FILE_READS = new Set(["readFileSync", "readFile"]);

type Labels = ReadonlySet<number>;
const EMPTY: Labels = new Set();

/** Where a value came from. `keyed` is the setting as a whole (the environment, the command
 *  line, a file's text) that a member read names more closely; `depth` counts those reads. A
 *  variable of the environment is `global`, the same wherever it is read; any other setting read
 *  inside a function is named again for each call that answers with it (`called`), so a helper's
 *  file read is one setting for each of its callers. */
interface Source {
  readonly kind: "source";
  readonly what: string;
  readonly where: string;
  readonly keyed?: "environment" | "command line" | "file";
  readonly depth: number;
  readonly global?: boolean;
  readonly called?: boolean;
  /** A setting named for a call: the setting it names inside the function. */
  readonly origin?: number;
  /** A member read of a setting as a whole: that setting, and the key read. */
  readonly parent?: number;
  readonly key?: string;
}
interface Written {
  readonly kind: "written";
  readonly how: string;
  readonly file: string;
  readonly line: number;
  readonly expression: string;
  readonly sources: Set<number>;
  /** The settings written at the head of the text, where a URL's scheme and host stand. */
  readonly leading: Set<number>;
  readonly ends: Set<string>;
  /** For the line as one call fills it in: the line of the function that wrote the parameter. */
  readonly base?: number;
}
interface Fn {
  readonly kind: "function";
  readonly node: FunctionWithBody;
}
/** What a function's parameter holds, as its answer is read: the call's argument stands in. */
interface Param {
  readonly kind: "parameter";
  readonly fn: ts.Node;
  readonly index: number;
  /** The parameter as a line of the function wrote it into text: that line's label, which a call
   *  fills in with what its own argument holds, and whether it wrote it at the head of the text. */
  readonly rendered: boolean;
  readonly writer: number;
  readonly leading: boolean;
}
type Label = Source | Written | Fn | Param;

/** One line the search finds, with what writes the value there, which settings it comes from,
 *  and where it is printed. */
export interface Finding {
  readonly file: string;
  readonly line: number;
  readonly text: string;
  readonly hows: readonly string[];
  readonly expressions: readonly string[];
  readonly sources: readonly string[];
  readonly ends: readonly string[];
}

function union(...sets: Labels[]): Labels {
  let first: Labels | undefined;
  let out: Set<number> | undefined;
  for (const set of sets) {
    if (set.size === 0) continue;
    if (first === undefined) {
      first = set;
      continue;
    }
    if (out === undefined) out = new Set(first);
    for (const label of set) out.add(label);
  }
  return out ?? first ?? EMPTY;
}

type FunctionWithBody = ts.SignatureDeclaration & { readonly body?: ts.Node };

function withBody(node: ts.Node): node is FunctionWithBody {
  return (
    (ts.isFunctionDeclaration(node) ||
      ts.isFunctionExpression(node) ||
      ts.isArrowFunction(node) ||
      ts.isMethodDeclaration(node) ||
      ts.isConstructorDeclaration(node) ||
      ts.isGetAccessorDeclaration(node) ||
      ts.isSetAccessorDeclaration(node)) &&
    node.body !== undefined
  );
}

function strip(expression: ts.Expression): ts.Expression {
  let e = expression;
  while (
    ts.isParenthesizedExpression(e) ||
    ts.isAsExpression(e) ||
    ts.isNonNullExpression(e) ||
    ts.isTypeAssertionExpression(e) ||
    ts.isSatisfiesExpression(e) ||
    ts.isAwaitExpression(e)
  ) {
    e = e.expression;
  }
  return e;
}

function nameOf(expression: ts.Expression): string {
  const e = strip(expression);
  if (ts.isIdentifier(e)) return e.text;
  if (ts.isPropertyAccessExpression(e)) return e.name.text;
  if (ts.isElementAccessExpression(e) && ts.isStringLiteral(e.argumentExpression)) {
    return e.argumentExpression.text;
  }
  return "";
}

/** `process.env`, `Bun.env` and `import.meta.env`, the environment as an object. */
function environment(expression: ts.Expression): boolean {
  const e = strip(expression);
  if (!ts.isPropertyAccessExpression(e) || e.name.text !== "env") return false;
  const owner = strip(e.expression);
  if (ts.isIdentifier(owner)) return owner.text === "process" || owner.text === "Bun";
  if (ts.isMetaProperty(owner)) return true;
  return (
    ts.isPropertyAccessExpression(owner) &&
    owner.name.text === "process" &&
    ts.isIdentifier(owner.expression) &&
    owner.expression.text === "globalThis"
  );
}

/** `process.argv` and `Bun.argv`, the command line. */
function commandLine(expression: ts.Expression): boolean {
  const e = strip(expression);
  return (
    ts.isPropertyAccessExpression(e) &&
    e.name.text === "argv" &&
    ts.isIdentifier(e.expression) &&
    (e.expression.text === "process" || e.expression.text === "Bun")
  );
}

/** The key a member read names: a property's name, a literal index's text, or undefined. */
function keyOf(
  expression: ts.PropertyAccessExpression | ts.ElementAccessExpression
): string | undefined {
  if (ts.isPropertyAccessExpression(expression)) return expression.name.text;
  const key = strip(expression.argumentExpression);
  return ts.isStringLiteralLike(key) || ts.isNumericLiteral(key) ? key.text : undefined;
}

/** The search over `files` (absolute paths), with `extra` files of text that is not on disk. */
export function search(files: readonly string[], extra: ReadonlyMap<string, string>) {
  const options: ts.CompilerOptions = {
    target: ts.ScriptTarget.ESNext,
    module: ts.ModuleKind.ESNext,
    moduleResolution: ts.ModuleResolutionKind.Bundler,
    jsx: ts.JsxEmit.ReactJSX,
    allowImportingTsExtensions: true,
    resolveJsonModule: true,
    skipLibCheck: true,
    noEmit: true,
    strict: true,
    types: ["bun"],
  };
  const host = ts.createCompilerHost(options, true);
  const readFile = host.readFile.bind(host);
  const fileExists = host.fileExists.bind(host);
  const getSourceFile = host.getSourceFile.bind(host);
  host.readFile = (file) => extra.get(file) ?? readFile(file);
  host.fileExists = (file) => extra.has(file) || fileExists(file);
  host.getSourceFile = (file, language, onError, create) => {
    const text = extra.get(file);
    return text === undefined
      ? getSourceFile(file, language, onError, create)
      : ts.createSourceFile(file, text, language, true);
  };
  const program = ts.createProgram([...files, ...extra.keys()], options, host);
  const checker = program.getTypeChecker();
  const ours = new Set(
    [...files, ...extra.keys()]
      .map((file) => program.getSourceFile(file))
      .filter((file): file is ts.SourceFile => file !== undefined)
  );
  const ourNode = (node: ts.Node) => ours.has(node.getSourceFile());

  const labels: Label[] = [];
  const labelIds = new Map<string, number>();
  const intern = (key: string, make: () => Label): number => {
    const known = labelIds.get(key);
    if (known !== undefined) return known;
    labels.push(make());
    labelIds.set(key, labels.length - 1);
    return labels.length - 1;
  };
  const lineOf = (node: ts.Node) =>
    node.getSourceFile().getLineAndCharacterOfPosition(node.getStart()).line + 1;
  const where = (node: ts.Node) =>
    `${path.relative(root, node.getSourceFile().fileName)}:${lineOf(node)}`;
  const one = (id: number): Labels => new Set([id]);
  /** A setting as a whole, read at `node`. */
  const setting = (node: ts.Node, keyed: Source["keyed"], what: string): number =>
    intern(`s ${where(node)} ${what}`, () => ({
      kind: "source",
      what,
      where: where(node),
      keyed,
      depth: 0,
    }));
  /** A variable of the environment, the same label wherever it is read. */
  const variable = (name: string, node: ts.Node): number =>
    intern(`v ${name}`, () => ({
      kind: "source",
      what: `the environment variable ${name}`,
      where: where(node),
      depth: 1,
      global: true,
    }));
  /** The setting `id` as the call at `call` answers with it. */
  const calledAt = (id: number, call: ts.Node): number => {
    const label = labels[id];
    if (label?.kind !== "source" || label.global || label.called) return id;
    return intern(`c ${id} ${call.getSourceFile().fileName} ${call.getStart()}`, () => ({
      ...label,
      where: `${label.where}, through the call at ${where(call)}`,
      called: true,
      origin: id,
    }));
  };
  /** `value` with each setting as a whole in it named more closely by `key`: the variable of the
   *  environment of that name, the argument or field of that key. A member of a value that holds
   *  the environment whose key is not spelled as a variable (`options.path` of options that hold
   *  it) is a field of that value, not a read of the environment. */
  const derive = (value: Labels, key: string | undefined, node: ts.Node): Labels => {
    let out: Set<number> | undefined;
    for (const id of value) {
      const label = labels[id];
      if (label?.kind !== "source" || label.keyed === undefined || key === undefined) continue;
      out ??= new Set(value);
      out.delete(id);
      if (label.keyed === "environment") {
        if (/^[A-Z][A-Z0-9_]*$/.test(key)) out.add(variable(key, node));
      } else {
        const what = `${label.what} ${/^\d+$/.test(key) ? `[${key}]` : `.${key}`}`;
        const depth = label.depth + 1;
        out.add(
          intern(`d ${id} ${key}`, () => ({
            kind: "source",
            what,
            where: label.where,
            keyed: depth < 3 ? label.keyed : undefined,
            depth,
            called: label.called,
            parent: id,
            key,
          }))
        );
      }
    }
    return out ?? value;
  };
  const fnLabel = (node: FunctionWithBody): number =>
    intern(`f ${node.getSourceFile().fileName} ${node.pos}`, () => ({ kind: "function", node }));
  const paramLabel = (fn: ts.Node, index: number, writer = -1, leading = false): number =>
    intern(`p ${fn.getSourceFile().fileName} ${fn.pos} ${index} ${writer} ${leading}`, () => ({
      kind: "parameter",
      fn,
      index,
      rendered: writer !== -1,
      writer,
      leading,
    }));
  /** The settings a value carries: each it holds as read, and each that a line wrote into the text
   *  it holds. */
  const settingsIn = (set: Labels): number[] => {
    const out = new Set<number>();
    for (const id of set) {
      const label = labels[id];
      if (label?.kind === "source") out.add(id);
      else if (label?.kind === "written") for (const each of label.sources) out.add(each);
    }
    return [...out];
  };
  const carries = (set: Labels) => settingsIn(set).length > 0;
  /** The settings a value holds as read, and those a line wrote at the head of its text. */
  const leadingIn = (set: Labels): number[] => {
    const out = new Set<number>();
    for (const id of set) {
      const label = labels[id];
      if (label?.kind === "source") out.add(id);
      else if (label?.kind === "written") for (const each of label.leading) out.add(each);
    }
    return [...out];
  };

  // Two readings of the tree, grown together to a fixed point. The symbolic one reads each
  // function with its parameters as themselves, so a function's answer is a summary that each call
  // fills in with its own arguments; the concrete one hands each parameter what every call hands
  // it, and is what the lines that print read.
  let symbolic = false;
  const concreteCarried = new Map<ts.Node, Set<number>>();
  const symbolicCarried = new Map<ts.Node, Set<number>>();
  const returns = new Map<ts.Node, Set<number>>();
  const thrown = new Map<ts.Node, Set<number>>();
  const caught = new Map<ts.Node, Set<number>>();
  const urls = new Set<number>();
  let changed = false;
  const grow = (map: Map<ts.Node, Set<number>>, key: ts.Node, value: Labels) => {
    if (value.size === 0) return;
    let held = map.get(key);
    if (held === undefined) {
      held = new Set();
      map.set(key, held);
    }
    for (const id of value) {
      if (!held.has(id)) {
        held.add(id);
        changed = true;
      }
    }
  };
  const carry = (key: ts.Node, value: Labels) => {
    if (ourNode(key)) grow(symbolic ? symbolicCarried : concreteCarried, key, value);
  };

  /** A setting the value holds as read, not yet written into text by a line, or in the symbolic
   *  reading such a parameter. */
  const unwritten = (id: number) => {
    const label = labels[id];
    return label?.kind === "source" || (symbolic && label?.kind === "parameter" && !label.rendered);
  };
  /** The value at `node` written into text by `how`. When the value holds a setting as read, the
   *  line is labelled, and the text it makes carries that label in place of the setting: a line
   *  that writes the text again is not a second line that writes the setting. A parameter of the
   *  symbolic reading becomes that parameter written by this line, which each call fills in with
   *  what its own argument holds (`writtenAt`); the two readings label a line apart, so a call's
   *  answer names only the settings that call hands it. */
  const write = (node: ts.Node, how: string, value: Labels, leading = false): Labels => {
    const read = [...value].filter(unwritten);
    if (read.length === 0) return value;
    const id = intern(
      `w ${node.getSourceFile().fileName} ${node.getStart()} ${how} ${symbolic ? "s" : "c"}`,
      () => ({
        kind: "written",
        how,
        file: path.relative(root, node.getSourceFile().fileName),
        line: lineOf(node),
        expression: node.getText().replace(/\s+/g, " ").slice(0, 160),
        sources: new Set(),
        leading: new Set(),
        ends: new Set(),
      })
    );
    return written(id, read, value, leading);
  };
  /** `value` with each setting it holds as read, `read`, written by the line `id`. */
  const written = (
    id: number,
    read: readonly number[],
    value: Labels,
    leading: boolean
  ): Labels => {
    const line = labels[id];
    const out = new Set(value);
    for (const each of read) {
      out.delete(each);
      const label = labels[each];
      if (label?.kind === "source" && line?.kind === "written") {
        line.sources.add(each);
        if (leading) line.leading.add(each);
      }
      if (label?.kind === "parameter") out.add(paramLabel(label.fn, label.index, id, leading));
    }
    out.add(id);
    return out;
  };
  /** What the argument `value` of the call `call` holds once the line `writer` of the function
   *  wrote it: that line as this call fills it in. */
  const writtenAt = (writer: number, call: ts.Node, value: Labels, leading: boolean): Labels => {
    const read = [...value].filter(unwritten);
    const line = labels[writer];
    if (read.length === 0 || line?.kind !== "written") return value;
    const base = line.base ?? writer;
    const first = labels[base];
    if (first?.kind !== "written") return value;
    const id = intern(`r ${base} ${call.getSourceFile().fileName} ${call.getStart()}`, () => ({
      kind: "written",
      how: first.how,
      file: first.file,
      line: first.line,
      expression: first.expression,
      sources: new Set(),
      leading: new Set(),
      ends: new Set(),
      base,
    }));
    return written(id, read, value, leading);
  };

  const declarationsOf = (symbol: ts.Symbol | undefined): readonly ts.Node[] => {
    if (symbol === undefined) return [];
    let s = symbol;
    if (s.flags & ts.SymbolFlags.Alias) {
      try {
        s = checker.getAliasedSymbol(s);
      } catch {
        return [];
      }
    }
    return s.declarations ?? [];
  };
  const symbolAt = (node: ts.Node): ts.Symbol | undefined => {
    try {
      return checker.getSymbolAtLocation(node);
    } catch {
      return undefined;
    }
  };
  const carriedBy = (declarations: readonly ts.Node[]): Labels => {
    const map = symbolic ? symbolicCarried : concreteCarried;
    return union(...declarations.map((d) => map.get(d) ?? EMPTY));
  };

  const bodies = new Map<ts.Node, FunctionWithBody | undefined>();
  /** The function of this tree, with a body, that `call` runs. */
  const bodyOf = (call: ts.CallLikeExpression): FunctionWithBody | undefined => {
    if (bodies.has(call)) return bodies.get(call);
    let found: FunctionWithBody | undefined;
    try {
      const declaration = checker.getResolvedSignature(call)?.declaration;
      if (declaration !== undefined && !ts.isJSDocSignature(declaration)) {
        if (withBody(declaration)) found = declaration;
        else {
          const named = ts.getNameOfDeclaration(declaration);
          found = declarationsOf(named === undefined ? undefined : symbolAt(named)).find(withBody);
        }
      }
    } catch {
      found = undefined;
    }
    if (found !== undefined && !ourNode(found)) found = undefined;
    const foundName = found === undefined ? undefined : ts.getNameOfDeclaration(found);
    if (foundName !== undefined && ts.isIdentifier(foundName) && SANITIZERS.has(foundName.text)) {
      found = undefined;
    }
    bodies.set(call, found);
    return found;
  };
  /** The function a value names: an inline one, or one a name or a member is bound to. */
  const functionAt = (expression: ts.Expression | undefined): FunctionWithBody | undefined => {
    if (expression === undefined) return undefined;
    const e = strip(expression);
    if (withBody(e)) return e;
    if (!ts.isIdentifier(e) && !ts.isPropertyAccessExpression(e)) return undefined;
    for (const d of declarationsOf(symbolAt(ts.isIdentifier(e) ? e : e.name))) {
      if (withBody(d) && ourNode(d)) return d;
      if (ts.isVariableDeclaration(d) && d.initializer !== undefined) {
        const init = strip(d.initializer);
        if (withBody(init) && ourNode(init)) return init;
      }
    }
    return undefined;
  };

  const inertTypes = new Map<ts.Node, boolean>();
  /** Whether a value of this expression's type cannot hold the text of a setting. */
  const inert = (node: ts.Expression): boolean => {
    const known = inertTypes.get(node);
    if (known !== undefined) return known;
    let answer = false;
    try {
      const holds = (type: ts.Type): boolean => {
        if (type.flags & (ts.TypeFlags.Any | ts.TypeFlags.Unknown)) return true;
        if (type.isUnion()) return type.types.some(holds);
        if (type.isIntersection()) return type.types.every(holds);
        const none =
          ts.TypeFlags.NumberLike |
          ts.TypeFlags.BooleanLike |
          ts.TypeFlags.BigIntLike |
          ts.TypeFlags.Void |
          ts.TypeFlags.Undefined |
          ts.TypeFlags.Null |
          ts.TypeFlags.Never |
          ts.TypeFlags.ESSymbolLike |
          ts.TypeFlags.EnumLike |
          ts.TypeFlags.StringLiteral;
        if (type.flags & none) return false;
        return !(type.getCallSignatures().length > 0 && type.getProperties().length === 0);
      };
      answer = !holds(checker.getTypeAtLocation(node));
    } catch {
      answer = false;
    }
    inertTypes.set(node, answer);
    return answer;
  };

  /** The symbol of the name at the root of `value` (a name, or a chain of members of one) when no
   *  line assigns it again: a `const`, or a parameter that no line of its function assigns. A chain
   *  is one only when no line of its file assigns a member written the same way. */
  const steady = (value: ts.Expression): ts.Symbol | undefined => {
    const whole = strip(value);
    let e = whole;
    while (ts.isPropertyAccessExpression(e)) e = strip(e.expression);
    if (!ts.isIdentifier(e)) return undefined;
    const symbol = symbolAt(e);
    const declarations = declarationsOf(symbol);
    const declaration = declarations[0];
    if (symbol === undefined || declaration === undefined || declarations.length !== 1)
      return undefined;
    let fixed = false;
    if (ts.isVariableDeclaration(declaration) || ts.isBindingElement(declaration)) {
      fixed = (ts.getCombinedNodeFlags(declaration) & ts.NodeFlags.Const) !== 0;
    } else if (ts.isParameter(declaration)) {
      fixed = !assignedIn(declaration.parent, (target) => symbolAt(target) === symbol);
    }
    if (!fixed) return undefined;
    if (whole === e) return symbol;
    const text = whole.getText();
    return assignedIn(whole.getSourceFile(), (target) => target.getText() === text)
      ? undefined
      : symbol;
  };
  /** Whether a line in `node` assigns a place `matches` (after `strip`). */
  const assignedIn = (node: ts.Node, matches: (target: ts.Expression) => boolean): boolean => {
    let found = false;
    const visit = (n: ts.Node) => {
      if (found) return;
      if (
        ts.isBinaryExpression(n) &&
        n.operatorToken.kind >= ts.SyntaxKind.FirstAssignment &&
        n.operatorToken.kind <= ts.SyntaxKind.LastAssignment &&
        matches(strip(n.left))
      ) {
        found = true;
        return;
      }
      if (
        (ts.isPrefixUnaryExpression(n) || ts.isPostfixUnaryExpression(n)) &&
        (n.operator === ts.SyntaxKind.PlusPlusToken ||
          n.operator === ts.SyntaxKind.MinusMinusToken) &&
        matches(strip(n.operand))
      ) {
        found = true;
        return;
      }
      ts.forEachChild(n, visit);
    };
    visit(node);
    return found;
  };
  /** Whether `a` and `b` are the same steady value, written the same way. */
  const sameValue = (a: ts.Expression, b: ts.Expression): boolean => {
    const symbol = steady(a);
    return (
      symbol !== undefined &&
      steady(b) === symbol &&
      strip(a).getText().replace(/\s+/g, "") === strip(b).getText().replace(/\s+/g, "")
    );
  };
  /** What `expression` is of `value`'s judgement: the answer of `shownSettingUrl(value)`
   *  ("shown"), or of `URL.canParse(value)` with no base ("parses"), called here or held by a
   *  `const` that was given it. */
  const judgement = (
    expression: ts.Expression,
    value: ts.Expression
  ): "shown" | "parses" | undefined => {
    const e = strip(expression);
    if (ts.isCallExpression(e)) {
      const name = nameOf(e.expression);
      const first = e.arguments[0];
      if (!JUDGES.has(name) || first === undefined || !sameValue(first, value)) return undefined;
      if (name === "shownSettingUrl") return "shown";
      const callee = strip(e.expression);
      return e.arguments.length === 1 &&
        ts.isPropertyAccessExpression(callee) &&
        nameOf(callee.expression) === "URL"
        ? "parses"
        : undefined;
    }
    if (ts.isIdentifier(e) && steady(e) !== undefined) {
      const declaration = declarationsOf(symbolAt(e))[0];
      if (declaration !== undefined && ts.isVariableDeclaration(declaration)) {
        const init = declaration.initializer;
        if (init !== undefined && ts.isIdentifier(declaration.name)) return judgement(init, value);
      }
    }
    return undefined;
  };
  /** Whether `c` compares `value`'s judgement with the answer the judge gives a value it refuses:
   *  `undefined` from `shownSettingUrl` (`null` too, compared loosely), `false` from `canParse`. */
  const comparesWithRefusal = (c: ts.BinaryExpression, value: ts.Expression): boolean => {
    const op = c.operatorToken.kind;
    const loose =
      op === ts.SyntaxKind.EqualsEqualsToken || op === ts.SyntaxKind.ExclamationEqualsToken;
    for (const [side, other] of [
      [c.left, c.right],
      [c.right, c.left],
    ] as const) {
      const kind = judgement(side, value);
      const answer = strip(other);
      if (kind === "parses" && answer.kind === ts.SyntaxKind.FalseKeyword) return true;
      if (
        kind === "shown" &&
        ((ts.isIdentifier(answer) && answer.text === "undefined") ||
          ts.isVoidExpression(answer) ||
          (loose && answer.kind === ts.SyntaxKind.NullKeyword))
      )
        return true;
    }
    return false;
  };
  /** Whether `condition` holds whenever the judge does not admit `value`. */
  const refusing = (condition: ts.Expression, value: ts.Expression): boolean => {
    const c = strip(condition);
    if (ts.isPrefixUnaryExpression(c) && c.operator === ts.SyntaxKind.ExclamationToken)
      return admitting(c.operand, value);
    if (!ts.isBinaryExpression(c)) return false;
    const op = c.operatorToken.kind;
    if (op === ts.SyntaxKind.BarBarToken)
      return refusing(c.left, value) || refusing(c.right, value);
    return (
      (op === ts.SyntaxKind.EqualsEqualsEqualsToken || op === ts.SyntaxKind.EqualsEqualsToken) &&
      comparesWithRefusal(c, value)
    );
  };
  /** Whether `condition` holds only when the judge admits `value`. */
  const admitting = (condition: ts.Expression, value: ts.Expression): boolean => {
    const c = strip(condition);
    if (judgement(c, value) !== undefined) return true;
    if (ts.isPrefixUnaryExpression(c) && c.operator === ts.SyntaxKind.ExclamationToken)
      return refusing(c.operand, value);
    if (!ts.isBinaryExpression(c)) return false;
    const op = c.operatorToken.kind;
    if (op === ts.SyntaxKind.AmpersandAmpersandToken)
      return admitting(c.left, value) || admitting(c.right, value);
    return (
      (op === ts.SyntaxKind.ExclamationEqualsEqualsToken ||
        op === ts.SyntaxKind.ExclamationEqualsToken) &&
      comparesWithRefusal(c, value)
    );
  };
  /** Whether no line after `statement` runs once it has run: it throws, returns, breaks, goes on
   *  to the next turn of its loop, or calls a function that never returns (`process.exit`). */
  const exits = (statement: ts.Statement): boolean => {
    if (
      ts.isThrowStatement(statement) ||
      ts.isReturnStatement(statement) ||
      ts.isBreakStatement(statement) ||
      ts.isContinueStatement(statement)
    )
      return true;
    if (ts.isBlock(statement)) return statement.statements.some(exits);
    if (ts.isIfStatement(statement))
      return (
        statement.elseStatement !== undefined &&
        exits(statement.thenStatement) &&
        exits(statement.elseStatement)
      );
    if (ts.isExpressionStatement(statement)) {
      const call = strip(statement.expression);
      if (!ts.isCallExpression(call)) return false;
      try {
        const signature = checker.getResolvedSignature(call);
        return (
          signature !== undefined &&
          (checker.getReturnTypeOfSignature(signature).flags & ts.TypeFlags.Never) !== 0
        );
      } catch {
        return false;
      }
    }
    return false;
  };
  /** Whether a guard admits `value` before it is read at its own place on every path: an `if`
   *  before it, in a block around it, that exits when the judge refuses the value, or a branch
   *  around it taken only when the judge admits it. It looks out through an arrow or function
   *  expression, made after any guard before it, but not through a declared function or method,
   *  which a line before the guard can call. */
  const guarded = (value: ts.Expression): boolean => {
    if (steady(value) === undefined) return false;
    let child: ts.Node = value;
    for (let p = value.parent; p !== undefined; child = p, p = p.parent) {
      if (
        ts.isBlock(p) ||
        ts.isSourceFile(p) ||
        ts.isCaseClause(p) ||
        ts.isDefaultClause(p) ||
        ts.isModuleBlock(p)
      ) {
        const before = p.statements.slice(
          0,
          Math.max(p.statements.indexOf(child as ts.Statement), 0)
        );
        for (const statement of before) {
          if (
            ts.isIfStatement(statement) &&
            refusing(statement.expression, value) &&
            exits(statement.thenStatement)
          )
            return true;
        }
      } else if (ts.isIfStatement(p)) {
        if (child === p.thenStatement && admitting(p.expression, value)) return true;
        if (child === p.elseStatement && refusing(p.expression, value)) return true;
      } else if (ts.isConditionalExpression(p)) {
        if (child === p.whenTrue && admitting(p.condition, value)) return true;
        if (child === p.whenFalse && refusing(p.condition, value)) return true;
      } else if (ts.isBinaryExpression(p) && child === p.right) {
        const op = p.operatorToken.kind;
        if (op === ts.SyntaxKind.AmpersandAmpersandToken && admitting(p.left, value)) return true;
        if (op === ts.SyntaxKind.BarBarToken && refusing(p.left, value)) return true;
      } else if (withBody(p) && !ts.isArrowFunction(p) && !ts.isFunctionExpression(p)) {
        return false;
      }
    }
    return false;
  };
  /** Whether each value of `expression` that carries a setting is guarded where it is read. */
  const admitted = (expression: ts.Expression): boolean => {
    const e = strip(expression);
    if (ts.isIdentifier(e) || ts.isPropertyAccessExpression(e)) return guarded(e);
    if (ts.isTemplateExpression(e)) {
      return e.templateSpans.every(
        (span) => !carries(ev(span.expression)) || admitted(span.expression)
      );
    }
    const either = (part: ts.Expression) => !carries(ev(part)) || admitted(part);
    if (ts.isConditionalExpression(e)) return either(e.whenTrue) && either(e.whenFalse);
    if (ts.isBinaryExpression(e) && e.operatorToken.kind === ts.SyntaxKind.QuestionQuestionToken) {
      return either(e.left) && either(e.right);
    }
    return false;
  };

  let concreteMemo = new Map<ts.Node, Labels>();
  let symbolicMemo = new Map<ts.Node, Labels>();
  const ev = (expression: ts.Expression): Labels => {
    const memo = symbolic ? symbolicMemo : concreteMemo;
    const known = memo.get(expression);
    if (known !== undefined) return known;
    memo.set(expression, EMPTY);
    let value = evaluate(expression);
    if (value.size > 0 && inert(expression)) {
      value = new Set([...value].filter((id) => labels[id]?.kind === "function"));
    }
    memo.set(expression, value);
    return value;
  };
  /** What the call `call` of `fn` answers: each of its parameters filled in by `argument(index)`,
   *  and each other setting of its summary named for this call. */
  const answer = (
    fn: ts.Node | undefined,
    argument: (index: number) => Labels,
    call: ts.Node
  ): Labels => {
    if (fn === undefined) return EMPTY;
    const summary = returns.get(fn);
    if (summary === undefined) return EMPTY;
    const out = new Set<number>();
    for (const id of summary) {
      const label = labels[id];
      if (label?.kind === "parameter" && label.fn === fn) {
        const value = argument(label.index);
        for (const each of label.rendered
          ? writtenAt(label.writer, call, value, label.leading)
          : value)
          out.add(each);
      } else out.add(calledAt(id, call));
    }
    return out;
  };
  const argumentsOf =
    (fn: FunctionWithBody, args: readonly ts.Expression[]) =>
    (index: number): Labels => {
      const parameter = fn.parameters[index];
      if (parameter === undefined) return EMPTY;
      if (parameter.dotDotDotToken !== undefined) return union(...args.slice(index).map(ev));
      const arg = args[index];
      return arg === undefined ? EMPTY : ev(arg);
    };
  const fnsIn = (value: Labels): FunctionWithBody[] =>
    [...value].flatMap((id) => {
      const label = labels[id];
      return label?.kind === "function" ? [label.node] : [];
    });

  const evaluate = (expression: ts.Expression): Labels => {
    const e = strip(expression);
    if (e !== expression) return ev(e);
    if (ts.isIdentifier(e)) {
      if (e.text === "undefined") return EMPTY;
      const declarations = declarationsOf(symbolAt(e));
      const fn = declarations.find((d): d is FunctionWithBody => withBody(d) && ourNode(d));
      return union(carriedBy(declarations), fn === undefined ? EMPTY : one(fnLabel(fn)));
    }
    if (ts.isPropertyAccessExpression(e) || ts.isElementAccessExpression(e)) {
      if (environment(e)) return one(setting(e, "environment", "the environment"));
      if (commandLine(e)) return one(setting(e, "command line", e.getText()));
      const key = keyOf(e);
      if (environment(e.expression)) {
        return key === undefined
          ? one(setting(e, "environment", `${e.expression.getText()}[${e.getText()}]`))
          : one(variable(key, e));
      }
      const declarations = declarationsOf(symbolAt(ts.isPropertyAccessExpression(e) ? e.name : e));
      const fn = declarations.find((d): d is FunctionWithBody => withBody(d) && ourNode(d));
      const fnValue = fn === undefined ? EMPTY : one(fnLabel(fn));
      const base = ev(e.expression);
      const derived = derive(base, key, e);
      // A property the tree declares holds what its writes put there, which the concrete reading
      // reads in every function: not what the rest of the value holding it holds. Of that value,
      // only a member read of a setting as a whole names more.
      if (declarations.some((d) => ourNode(d) && !withBody(d))) {
        const map = concreteCarried;
        const written = union(...declarations.map((d) => map.get(d) ?? EMPTY));
        return union(written, new Set([...derived].filter((id) => !base.has(id))), fnValue);
      }
      return union(carriedBy(declarations), derived, fnValue);
    }
    if (ts.isCallExpression(e)) return evaluateCall(e);
    if (ts.isNewExpression(e)) {
      const name = nameOf(e.expression);
      const args = e.arguments ?? [];
      const body = bodyOf(e);
      if (body !== undefined)
        return union(answer(body, argumentsOf(body, args), e), ...args.map(ev));
      if (KEEPING_CLASSES.has(name) || /Error$/.test(name)) return union(...args.map(ev));
      return EMPTY;
    }
    if (ts.isTemplateExpression(e)) {
      return union(
        ...e.templateSpans.map((span, index) =>
          write(
            span.expression,
            "a template",
            ev(span.expression),
            index === 0 && e.head.text === ""
          )
        )
      );
    }
    if (ts.isTaggedTemplateExpression(e)) {
      if (nameOf(e.tag) === "$" || !ts.isTemplateExpression(e.template)) return EMPTY;
      return union(
        ...e.template.templateSpans.map((span) =>
          write(span.expression, "a template", ev(span.expression))
        )
      );
    }
    if (ts.isBinaryExpression(e)) {
      const op = e.operatorToken.kind;
      if (op === ts.SyntaxKind.PlusToken || op === ts.SyntaxKind.PlusEqualsToken) {
        return union(
          write(e.left, "a string +", ev(e.left), true),
          write(e.right, "a string +", ev(e.right))
        );
      }
      if (
        op === ts.SyntaxKind.QuestionQuestionToken ||
        op === ts.SyntaxKind.BarBarToken ||
        op === ts.SyntaxKind.AmpersandAmpersandToken
      ) {
        return union(ev(e.left), ev(e.right));
      }
      if (
        op === ts.SyntaxKind.CommaToken ||
        (op >= ts.SyntaxKind.FirstAssignment && op <= ts.SyntaxKind.LastAssignment)
      ) {
        return ev(e.right);
      }
      return EMPTY;
    }
    if (ts.isConditionalExpression(e)) return union(ev(e.whenTrue), ev(e.whenFalse));
    if (ts.isArrayLiteralExpression(e)) {
      return union(
        ...e.elements.map((element) => (ts.isOmittedExpression(element) ? EMPTY : ev(element)))
      );
    }
    if (ts.isSpreadElement(e)) return ev(e.expression);
    if (ts.isObjectLiteralExpression(e)) {
      return union(
        ...e.properties.map((property) => {
          if (ts.isPropertyAssignment(property)) return ev(property.initializer);
          if (ts.isShorthandPropertyAssignment(property)) {
            return carriedBy(declarationsOf(checker.getShorthandAssignmentValueSymbol(property)));
          }
          if (ts.isSpreadAssignment(property)) return ev(property.expression);
          return EMPTY;
        })
      );
    }
    return EMPTY;
  };

  const evaluateCall = (call: ts.CallExpression): Labels => {
    const callee = strip(call.expression);
    const name = nameOf(callee);
    if (SANITIZERS.has(name)) return EMPTY;
    const isFileRead =
      FILE_READS.has(name) ||
      (name === "file" &&
        ts.isPropertyAccessExpression(callee) &&
        nameOf(callee.expression) === "Bun");
    if (isFileRead) return one(setting(call, "file", `the text of a file (${callee.getText()})`));
    const body = bodyOf(call);
    if (body !== undefined) return answer(body, argumentsOf(body, call.arguments), call);
    const through = fnsIn(ev(callee));
    if (through.length > 0)
      return union(...through.map((fn) => answer(fn, argumentsOf(fn, call.arguments), call)));
    const args = call.arguments.map((arg) => (withBody(strip(arg)) ? EMPTY : ev(arg)));
    if (ts.isPropertyAccessExpression(callee)) {
      const receiver = callee.expression;
      if (MAPPERS.has(name)) {
        const fn = functionAt(call.arguments[0]);
        return fn === undefined
          ? ev(receiver)
          : answer(fn, (index) => (index === 0 ? ev(receiver) : EMPTY), call);
      }
      if (name === "reduce" || name === "reduceRight") {
        const fn = functionAt(call.arguments[0]);
        const start = args[1] ?? EMPTY;
        return union(
          start,
          answer(
            fn,
            (index) =>
              index === 0 ? union(start, ev(receiver)) : index === 1 ? ev(receiver) : EMPTY,
            call
          )
        );
      }
      if (KEEPS_RECEIVER.has(name)) {
        const value = union(ev(receiver), KEEPS_ARGUMENTS.has(name) ? union(...args) : EMPTY);
        return RENDERS.has(name) ? write(callee.name, `${name}()`, value) : value;
      }
    }
    if (KEEPS.has(name)) {
      let value = union(...args);
      if (name === "from" && call.arguments[1] !== undefined) {
        const fn = functionAt(call.arguments[1]);
        value = union(
          value,
          answer(fn, (index) => (index === 0 ? (args[0] ?? EMPTY) : EMPTY), call)
        );
      }
      return RENDERS.has(name) ? write(call, `${name}()`, value) : value;
    }
    return EMPTY;
  };

  /** `value` into each name `name` binds: a member's key names the setting it reads more closely. */
  const bind = (name: ts.BindingName, value: Labels) => {
    if (ts.isIdentifier(name)) {
      carry(name.parent, value);
      return;
    }
    name.elements.forEach((element, index) => {
      if (ts.isOmittedExpression(element)) return;
      const key = ts.isArrayBindingPattern(name)
        ? String(index)
        : element.propertyName === undefined
          ? ts.isIdentifier(element.name)
            ? element.name.text
            : undefined
          : ts.isIdentifier(element.propertyName) || ts.isStringLiteral(element.propertyName)
            ? element.propertyName.text
            : undefined;
      let each = element.dotDotDotToken === undefined ? derive(value, key, element) : value;
      if (element.initializer !== undefined) each = union(each, ev(element.initializer));
      if (ts.isIdentifier(element.name)) carry(element, each);
      else bind(element.name, each);
    });
  };
  /** `value` into the place an assignment writes. */
  const assign = (target: ts.Expression, value: Labels) => {
    const t = strip(target);
    if (ts.isIdentifier(t)) {
      for (const d of declarationsOf(symbolAt(t))) carry(d, value);
    } else if (ts.isPropertyAccessExpression(t)) {
      for (const d of declarationsOf(symbolAt(t.name))) carry(d, value);
      assign(t.expression, value);
    } else if (ts.isElementAccessExpression(t)) {
      assign(t.expression, value);
    } else if (ts.isArrayLiteralExpression(t)) {
      for (const element of t.elements)
        assign(ts.isSpreadElement(element) ? element.expression : element, value);
    } else if (ts.isObjectLiteralExpression(t)) {
      for (const property of t.properties) {
        if (ts.isPropertyAssignment(property)) assign(property.initializer, value);
        else if (ts.isShorthandPropertyAssignment(property)) {
          for (const d of declarationsOf(checker.getShorthandAssignmentValueSymbol(property)))
            carry(d, value);
        } else if (ts.isSpreadAssignment(property)) assign(property.expression, value);
      }
    } else if (ts.isBinaryExpression(t) && t.operatorToken.kind === ts.SyntaxKind.EqualsToken) {
      assign(t.left, value);
    }
  };
  /** The declarations of the property `property` of the type its object literal is written as. */
  const contextual = (property: ts.ObjectLiteralElementLike): readonly ts.Node[] => {
    const object = property.parent;
    if (!ts.isObjectLiteralExpression(object) || property.name === undefined) return [];
    const key =
      ts.isIdentifier(property.name) || ts.isStringLiteral(property.name)
        ? property.name.text
        : undefined;
    if (key === undefined) return [];
    let type: ts.Type | undefined;
    try {
      type = checker.getContextualType(object);
    } catch {
      return [];
    }
    if (type === undefined) return [];
    return (type.isUnion() ? type.types : [type]).flatMap((t) =>
      declarationsOf(t.getProperty(key))
    );
  };

  const enclosingFunction = (node: ts.Node): ts.Node => {
    for (let p = node.parent; p !== undefined; p = p.parent) {
      if (withBody(p) || ts.isSourceFile(p)) return p;
    }
    return node.getSourceFile();
  };
  const enclosingTry = (node: ts.Node): ts.TryStatement | undefined => {
    let child: ts.Node = node;
    for (let p = node.parent; p !== undefined; child = p, p = p.parent) {
      if (withBody(p) || ts.isSourceFile(p)) return undefined;
      if (ts.isTryStatement(p) && p.tryBlock === child && p.catchClause !== undefined) return p;
    }
    return undefined;
  };
  const handledByCatch = (call: ts.Node): boolean => {
    const parent = call.parent;
    return (
      parent !== undefined &&
      ts.isPropertyAccessExpression(parent) &&
      parent.expression === call &&
      (parent.name.text === "catch" || parent.name.text === "then")
    );
  };
  /** A parse error that carries `value`, out of `site`: to the catch of the try around it, or out
   *  of its function. */
  const raise = (site: ts.Node, value: Labels) => {
    if (symbolic || value.size === 0) return;
    const handler = enclosingTry(site);
    if (handler !== undefined) grow(caught, handler, value);
    else grow(thrown, enclosingFunction(site), value);
  };

  const calls = new Map<ts.Node, number>();
  let collecting = false;
  /** A place that prints `value`: a line for it, and an end for each line that wrote it. */
  const end = (argument: ts.Expression, kind: string, site: ts.Node) => {
    const value = write(argument, kind, ev(argument));
    for (const id of value) {
      const label = labels[id];
      if (label?.kind === "written" && label.sources.size > 0)
        label.ends.add(`${kind} at ${where(site)}`);
    }
  };
  const sinkOf = (
    call: ts.CallExpression | ts.NewExpression
  ): { kind: string; args: readonly ts.Expression[] } | undefined => {
    const callee = strip(call.expression);
    const name = nameOf(callee);
    const args = call.arguments ?? [];
    const text = callee.getText();
    if (/Error$/.test(name)) return { kind: `the message of ${name}`, args };
    if (ts.isNewExpression(call)) return undefined;
    if (/^console\.\w+$/.test(text)) return { kind: text, args };
    if (
      (name === "write" || name === "writeSync") &&
      /(^|\.)(stdout|stderr)$/.test(text.replace(/\.write(Sync)?$/, ""))
    ) {
      return { kind: text, args };
    }
    if (
      text === "Bun.write" &&
      args[0] !== undefined &&
      /^Bun\.(stdout|stderr)$/.test(args[0].getText())
    ) {
      return { kind: `Bun.write(${args[0].getText()})`, args: args.slice(1) };
    }
    if (
      name === "writeSync" &&
      args[0] !== undefined &&
      /^(1|2|process\.(stdout|stderr)\.fd)$/.test(args[0].getText())
    ) {
      return { kind: "writeSync to standard output or error", args: args.slice(1) };
    }
    if (childCommand(callee) || text === "Bun.spawn" || text === "Bun.spawnSync") {
      return {
        kind: `the command line of ${text}`,
        args: args.slice(0, ts.isArrayLiteralExpression(strip(args[0] ?? callee)) ? 1 : 2),
      };
    }
    if (text === "Promise.reject") return { kind: "a rejected promise", args };
    if (bodyOf(call) === undefined && LOG_NAMES.has(name) && fnsIn(ev(callee)).length === 0) {
      return { kind: `a log call (${text})`, args };
    }
    return undefined;
  };

  const flowCall = (call: ts.CallExpression | ts.NewExpression) => {
    const args = call.arguments ?? [];
    const callee = strip(call.expression);
    const name = nameOf(callee);
    const owner = ts.isPropertyAccessExpression(callee) ? nameOf(callee.expression) : "";
    const body = bodyOf(call);
    const targets =
      body !== undefined ? [body] : ts.isCallExpression(call) ? fnsIn(ev(callee)) : [];
    for (const target of targets) {
      if (!symbolic) {
        target.parameters.forEach((parameter, index) => {
          bind(parameter.name, argumentsOf(target, args)(index));
        });
      }
      if (!handledByCatch(call)) raise(call, thrown.get(target) ?? EMPTY);
    }
    if (ts.isCallExpression(call) && ts.isPropertyAccessExpression(callee) && !symbolic) {
      const receiver = callee.expression;
      if (VISITORS.has(name)) {
        const fn = functionAt(args[0]);
        const first = fn?.parameters[0];
        if (first !== undefined) bind(first.name, ev(receiver));
        const second = fn?.parameters[1];
        if (second !== undefined && (name === "sort" || name === "toSorted"))
          bind(second.name, ev(receiver));
      }
      if (name === "reduce" || name === "reduceRight") {
        const fn = functionAt(args[0]);
        const [accumulator, element] = fn?.parameters ?? [];
        if (accumulator !== undefined)
          bind(accumulator.name, union(ev(receiver), args[1] === undefined ? EMPTY : ev(args[1])));
        if (element !== undefined) bind(element.name, ev(receiver));
      }
      if (name === "catch" || name === "then") {
        const fn = functionAt(name === "catch" ? args[0] : args[1]);
        const inner = strip(receiver);
        const failing = ts.isCallExpression(inner)
          ? union(
              ...[bodyOf(inner), ...fnsIn(ev(inner.expression))].map((t) =>
                t === undefined ? EMPTY : (thrown.get(t) ?? EMPTY)
              )
            )
          : EMPTY;
        const first = fn?.parameters[0];
        if (first !== undefined) bind(first.name, failing);
      }
    }
    if (
      ts.isCallExpression(call) &&
      ts.isPropertyAccessExpression(callee) &&
      MUTATORS.has(name) &&
      body === undefined
    ) {
      assign(callee.expression, union(...args.map(ev)));
    }
    if (
      ts.isCallExpression(call) &&
      name === "assign" &&
      owner === "Object" &&
      args[0] !== undefined
    ) {
      assign(args[0], union(...args.slice(1).map(ev)));
    }
    if (name === "from" && args[1] !== undefined && !symbolic) {
      const fn = functionAt(args[1]);
      const first = fn?.parameters[0];
      if (first !== undefined && args[0] !== undefined) bind(first.name, ev(args[0]));
    }
    if (ts.isNewExpression(call) && name === "URL") {
      const parsed = args.filter((arg) => carries(ev(arg)) && !admitted(arg));
      if (parsed.length > 0) {
        raise(
          call,
          write(
            call,
            "the error of new URL, which holds what it could not parse",
            union(...parsed.map(ev))
          )
        );
      }
    }
    // A URL taker reads its first argument as a URL (`new URL` and `URL.canParse` their base too);
    // a child is handed its command line and its environment, whose use the search cannot read.
    const spawns = childCommand(callee) || /^Bun\.spawn(Sync)?$/.test(callee.getText());
    const takesUrl = URL_TAKERS.has(name) || /^ky\b/.test(callee.getText());
    if (!symbolic && body === undefined && (takesUrl || spawns)) {
      const handed = spawns
        ? args.flatMap((arg) => {
            const value = strip(arg);
            if (!ts.isObjectLiteralExpression(value)) return [arg];
            return value.properties.flatMap((property) =>
              ts.isPropertyAssignment(property) && /^(env|cmd)$/.test(property.name.getText())
                ? [property.initializer]
                : []
            );
          })
        : name === "URL" || name === "canParse"
          ? args
          : args.slice(0, 1);
      // A setting is read as a URL when it is the URL handed, or the head of its text (the base a
      // template or a `+` puts a path after); one written after the head is a part of a path.
      for (const arg of handed) {
        const value = ev(arg);
        for (const id of spawns ? settingsIn(value) : leadingIn(value)) urls.add(id);
      }
    }
    if (collecting) {
      const sink = sinkOf(call);
      if (sink !== undefined) {
        for (const arg of sink.args) {
          const value = strip(arg);
          if (ts.isArrayLiteralExpression(value)) {
            for (const element of value.elements)
              end(ts.isSpreadElement(element) ? element.expression : element, sink.kind, call);
          } else end(arg, sink.kind, call);
        }
      }
    }
  };
  const flow = (node: ts.Node) => {
    if (ts.isVariableDeclaration(node)) {
      const loop = node.parent.parent;
      if (
        node.initializer !== undefined &&
        !ts.isForOfStatement(loop) &&
        !ts.isForInStatement(loop)
      ) {
        bind(node.name, ev(node.initializer));
      }
    } else if (ts.isParameter(node)) {
      if (node.initializer !== undefined) bind(node.name, ev(node.initializer));
    } else if (ts.isBinaryExpression(node)) {
      const op = node.operatorToken.kind;
      if (op >= ts.SyntaxKind.FirstAssignment && op <= ts.SyntaxKind.LastAssignment) {
        assign(node.left, op === ts.SyntaxKind.PlusEqualsToken ? ev(node) : ev(node.right));
      }
    } else if (ts.isPropertyAssignment(node) && ts.isObjectLiteralExpression(node.parent)) {
      const value = ev(node.initializer);
      carry(node, value);
      for (const d of contextual(node)) carry(d, value);
    } else if (ts.isShorthandPropertyAssignment(node)) {
      const value = carriedBy(declarationsOf(checker.getShorthandAssignmentValueSymbol(node)));
      carry(node, value);
      for (const d of contextual(node)) carry(d, value);
    } else if (ts.isPropertyDeclaration(node)) {
      if (node.initializer !== undefined) carry(node, ev(node.initializer));
    } else if (ts.isReturnStatement(node)) {
      // Both readings read the answer: the symbolic one is the summary, and the concrete one hands
      // each line that writes a parameter into text what the calls hand it.
      if (node.expression !== undefined) {
        const value = ev(node.expression);
        if (symbolic) grow(returns, enclosingFunction(node), value);
      }
    } else if (ts.isArrowFunction(node)) {
      if (!ts.isBlock(node.body)) {
        const value = ev(node.body);
        if (symbolic) grow(returns, node, value);
      }
    } else if (ts.isForOfStatement(node)) {
      const value = ev(node.expression);
      const init = node.initializer;
      if (ts.isVariableDeclarationList(init))
        for (const d of init.declarations) bind(d.name, value);
      else assign(init, value);
    } else if (ts.isCatchClause(node)) {
      const declaration = node.variableDeclaration;
      if (declaration !== undefined) bind(declaration.name, caught.get(node.parent) ?? EMPTY);
    } else if (ts.isCallExpression(node) || ts.isNewExpression(node)) {
      flowCall(node);
    } else if (ts.isThrowStatement(node)) {
      if (collecting) end(node.expression, "a throw", node);
    } else if (
      ts.isTaggedTemplateExpression(node) &&
      nameOf(node.tag) === "$" &&
      ts.isTemplateExpression(node.template)
    ) {
      for (const span of node.template.templateSpans) {
        if (!symbolic) for (const id of settingsIn(ev(span.expression))) urls.add(id);
        if (collecting) end(span.expression, "a Bun shell command", node);
      }
    }
    ts.forEachChild(node, flow);
  };

  // How many calls of the tree reach each function, and each parameter as itself for the symbolic
  // reading.
  const prepare = (node: ts.Node) => {
    if (ts.isCallExpression(node) || ts.isNewExpression(node)) {
      const body = bodyOf(node);
      if (body !== undefined) calls.set(body, (calls.get(body) ?? 0) + 1);
    }
    if (withBody(node)) {
      symbolic = true;
      node.parameters.forEach((parameter, index) => {
        bind(parameter.name, one(paramLabel(node, index)));
      });
      symbolic = false;
    }
    ts.forEachChild(node, prepare);
  };
  for (const file of ours) prepare(file);

  let rounds = 0;
  do {
    changed = false;
    concreteMemo = new Map();
    symbolicMemo = new Map();
    symbolic = true;
    for (const file of ours) flow(file);
    symbolic = false;
    for (const file of ours) flow(file);
    rounds += 1;
  } while (changed && rounds < 100);
  if (changed) throw new Error(`the search reached no fixed point in ${rounds} rounds`);
  collecting = true;
  concreteMemo = new Map();
  for (const file of ours) flow(file);
  // A parse error that leaves a file's top level, or a function no call of the tree reaches, is
  // printed by the runtime, or by a caller the search does not read.
  for (const [fn, value] of thrown) {
    if (!ts.isSourceFile(fn) && (calls.get(fn) ?? 0) > 0) continue;
    for (const id of value) {
      const label = labels[id];
      if (label?.kind === "written") {
        label.ends.add(
          ts.isSourceFile(fn)
            ? `an error out of the top level of ${path.relative(root, fn.fileName)}`
            : `an error out of ${where(fn)}, which no call of the tree reaches`
        );
      }
    }
  }

  /** Whether a setting can be a URL: one that reaches a URL or another program, or a read of the
   *  environment by a key the search cannot name. */
  const url = (id: number): boolean => {
    const label = labels[id];
    if (label?.kind !== "source") return false;
    if (urls.has(id) || label.keyed === "environment") return true;
    // A setting named for a call is one when the setting it names inside the function is, and so
    // is a member read of it when the same read of that setting is.
    if (label.origin !== undefined && url(label.origin)) return true;
    const parent = label.parent === undefined ? undefined : labels[label.parent];
    if (parent?.kind === "source" && parent.origin !== undefined) {
      const counterpart = labelIds.get(`d ${parent.origin} ${label.key}`);
      return counterpart !== undefined && url(counterpart);
    }
    return false;
  };
  const lines = new Map<
    string,
    Finding & {
      hows: string[];
      expressions: string[];
      sources: string[];
      ends: string[];
      urlish: boolean;
    }
  >();
  for (const label of labels) {
    if (label.kind !== "written" || label.ends.size === 0 || label.sources.size === 0) continue;
    const key = `${label.file}:${label.line}`;
    let finding = lines.get(key);
    if (finding === undefined) {
      const file = program.getSourceFile(path.join(root, label.file));
      const text = file === undefined ? "" : (file.text.split("\n")[label.line - 1] ?? "").trim();
      finding = {
        file: label.file,
        line: label.line,
        text,
        hows: [],
        expressions: [],
        sources: [],
        ends: [],
        urlish: false,
      };
      lines.set(key, finding);
    }
    if (!finding.hows.includes(label.how)) finding.hows.push(label.how);
    if (!finding.expressions.includes(label.expression)) finding.expressions.push(label.expression);
    for (const id of label.sources) {
      const s = labels[id];
      if (s?.kind !== "source") continue;
      if (url(id)) finding.urlish = true;
      const named = `${s.what} (${s.where})${url(id) ? "" : ", which reaches no URL"}`;
      if (!finding.sources.includes(named)) finding.sources.push(named);
    }
    for (const each of label.ends) if (!finding.ends.includes(each)) finding.ends.push(each);
  }
  const all = [...lines.values()].sort((a, b) => a.file.localeCompare(b.file) || a.line - b.line);
  let printing = 0;
  const countEnds = (node: ts.Node) => {
    if ((ts.isCallExpression(node) || ts.isNewExpression(node)) && sinkOf(node) !== undefined)
      printing += 1;
    ts.forEachChild(node, countEnds);
  };
  for (const file of ours) countEnds(file);
  return {
    findings: all.filter((f) => f.urlish),
    quiet: all.filter((f) => !f.urlish),
    files: ours.size,
    rounds,
    printing,
  };
}

/** The planted lines, by number, marked `found` or `quiet`. */
export function plantedMarks(text: string): { found: number[]; quiet: number[] } {
  const found: number[] = [];
  const quiet: number[] = [];
  text.split("\n").forEach((line, index) => {
    if (/\/\/ planted: found$/.test(line)) found.push(index + 1);
    if (/\/\/ planted: quiet$/.test(line)) quiet.push(index + 1);
  });
  return { found, quiet };
}

const strings = (value: unknown): value is string[] =>
  Array.isArray(value) && value.every((each) => typeof each === "string");

/** A finding as the Go half prints it, read as what it must be, or undefined. */
function goFinding(f: unknown): Finding | undefined {
  if (
    typeof f === "object" &&
    f !== null &&
    "file" in f &&
    typeof f.file === "string" &&
    "line" in f &&
    typeof f.line === "number" &&
    "text" in f &&
    typeof f.text === "string" &&
    "hows" in f &&
    strings(f.hows) &&
    "expressions" in f &&
    strings(f.expressions) &&
    "sources" in f &&
    strings(f.sources) &&
    "ends" in f &&
    strings(f.ends)
  ) {
    const { file, text, hows, expressions, sources, ends } = f;
    return { file, line: f.line, text, hows, expressions, sources, ends };
  }
  return undefined;
}

/** One line of the Go half's output, read as what it must be, or an error naming the line. */
function goRecord(line: string): {
  finding?: Finding;
  quiet?: Finding;
  summary?: string;
  blind?: string[];
} {
  const record: unknown = JSON.parse(line);
  if (typeof record === "object" && record !== null) {
    if ("summary" in record && typeof record.summary === "string")
      return { summary: record.summary };
    if ("blind" in record && strings(record.blind)) return { blind: record.blind };
    const finding = "finding" in record ? goFinding(record.finding) : undefined;
    if (finding !== undefined) return { finding };
    const quiet = "quiet" in record ? goFinding(record.quiet) : undefined;
    if (quiet !== undefined) return { quiet };
  }
  throw new Error(`the Go half printed a line this program cannot read: ${line.slice(0, 200)}`);
}

/** The lines the Go half finds, those of settings that reach no URL, and what its planted shapes
 *  gave. It reads the packages of the Go Dispatch server this pull request changes: the command's
 *  and the routes'. */
function goFindings(): { findings: Finding[]; quiet: Finding[]; summary: string; blind: string[] } {
  const run = Bun.spawnSync(
    [
      "go",
      "run",
      "scripts/checks/setting-prints.go",
      "packages/envoy",
      "cmd/dispatch/main.go",
      "internal/dispatch/routes/devsignin.go",
    ],
    { cwd: root, stderr: "pipe", stdout: "pipe" }
  );
  const findings: Finding[] = [];
  const quiet: Finding[] = [];
  let summary = "";
  const blind: string[] = [];
  for (const line of run.stdout.toString().split("\n")) {
    if (line === "") continue;
    const record = goRecord(line);
    if (record.finding !== undefined) findings.push(record.finding);
    if (record.quiet !== undefined) quiet.push(record.quiet);
    if (record.summary !== undefined) summary = record.summary;
    if (record.blind !== undefined) blind.push(...record.blind);
  }
  if (run.exitCode !== 0 || summary === "") {
    blind.push(
      `the Go half exited ${run.exitCode}: ${run.stderr.toString().trim().slice(0, 2000)}`
    );
  }
  return { findings, quiet, summary, blind };
}

function printFinding(finding: Finding, judged: Judged | undefined, quiet: boolean) {
  console.log(`${finding.file}:${finding.line}: ${finding.text}`);
  console.log(`  written by ${finding.hows.join("; ")}: ${finding.expressions.join(" | ")}`);
  console.log(`  from ${finding.sources.join("; ")}`);
  console.log(`  printed by ${finding.ends.join("; ")}`);
  console.log(
    quiet
      ? "  quiet: no setting it prints reaches a URL"
      : judged === undefined
        ? "  NOT JUDGED"
        : `  judged: ${judged.why}`
  );
}

/** The value of the option `name` on the command line, or undefined. */
function option(name: string): string | undefined {
  const index = process.argv.indexOf(name);
  return index === -1 ? undefined : process.argv[index + 1];
}

/** The files this pull request changes: those `--scope <file>` lists, one a line; else every file
 *  that differs from where the branch left `--base` (origin/main unless named), the working tree's
 *  changes and its new files among them. Undefined when git cannot say. */
function changedFiles(): Set<string> | undefined {
  const list = option("--scope");
  if (list !== undefined)
    return new Set(
      readFileSync(list, "utf8")
        .split("\n")
        .filter((line) => line !== "")
    );
  const git = (...args: string[]) =>
    Bun.spawnSync(["git", ...args], { cwd: root, stdout: "pipe", stderr: "pipe" });
  const base = git("merge-base", option("--base") ?? "origin/main", "HEAD");
  if (base.exitCode !== 0) return undefined;
  const changed = git("diff", "--name-only", base.stdout.toString().trim());
  const added = git("ls-files", "--others", "--exclude-standard");
  if (changed.exitCode !== 0 || added.exitCode !== 0) return undefined;
  return new Set(
    [...changed.stdout.toString().split("\n"), ...added.stdout.toString().split("\n")].filter(
      (line) => line !== ""
    )
  );
}

/** Whether a finding is in the scope: its line is in a file the pull request changes, or a line of
 *  such a file prints the text it wrote. */
function inScope(finding: Finding, scope: ReadonlySet<string>): boolean {
  if (scope.has(finding.file)) return true;
  return finding.ends.some((end) =>
    [...end.matchAll(/((?:[\w@.-]+\/)*[\w@.-]+\.(?:[cm]?tsx?|go))\b/g)].some((match) =>
      scope.has(match[1] ?? "")
    )
  );
}

if (import.meta.main) {
  const all = process.argv.includes("--all");
  const scope = changedFiles();
  if (scope === undefined || scope.size === 0) {
    console.log(
      "UNKNOWN: git names no file this pull request changes; name them with --scope <file>"
    );
    process.exit(2);
  }
  const listed = Bun.spawnSync(["git", "ls-files", "-z"], { cwd: root, stdout: "pipe" })
    .stdout.toString()
    .split("\0");
  const files = listed
    .filter((file) => file !== "" && readPath(file))
    .map((file) => path.join(root, file));
  const typescript = search(files, new Map([[plantedFile, PLANTED]]));
  const go = goFindings();
  const plantedName = path.relative(root, plantedFile);
  const marks = plantedMarks(PLANTED);
  const plantedFound = new Set(
    typescript.findings.filter((f) => f.file === plantedName).map((f) => f.line)
  );
  const blind = [
    ...marks.found
      .filter((line) => !plantedFound.has(line))
      .map((line) => `the planted line ${plantedName}:${line} was not found`),
    ...marks.quiet
      .filter((line) => plantedFound.has(line))
      .map((line) => `the planted line ${plantedName}:${line} was found`),
    ...[...plantedFound]
      .filter((line) => !marks.found.includes(line) && !marks.quiet.includes(line))
      .map((line) => `the planted line ${plantedName}:${line}, which is not marked, was found`),
    ...go.blind,
  ];
  const everywhere = [...typescript.findings.filter((f) => f.file !== plantedName), ...go.findings];
  const findings = everywhere.filter((f) => inScope(f, scope));
  const used = new Set<Judged>();
  let open = 0;
  for (const finding of findings) {
    const judged = JUDGED.find((j) => j.file === finding.file && finding.text.includes(j.text));
    if (judged !== undefined) used.add(judged);
    else open += 1;
    if (judged === undefined || all) printFinding(finding, judged, false);
  }
  const quiet = [...typescript.quiet.filter((f) => f.file !== plantedName), ...go.quiet].filter(
    (f) => inScope(f, scope)
  );
  if (all) for (const finding of quiet) printFinding(finding, undefined, true);
  const stale = JUDGED.filter((j) => !used.has(j));
  for (const j of stale)
    console.log(`STALE ${j.file}: no line found holds ${JSON.stringify(j.text)}`);
  console.log(
    `Scope: ${scope.size} files this pull request changes, and the files whose written text a line of those prints; ${everywhere.length - findings.length} lines outside it are not listed. TypeScript: ${typescript.files - 1} files read (and the planted one), ${typescript.printing} calls that print, ${typescript.rounds} rounds; ${quiet.length} more lines in the scope print a setting that reaches no URL. Go: ${go.summary}`
  );
  if (blind.length > 0 || typescript.files <= 1) {
    for (const line of blind) console.log(`BLIND ${line}`);
    console.log(
      `UNKNOWN: the search cannot see a shape it must see; ${findings.length} lines found, ${open} not judged`
    );
    process.exit(2);
  }
  if (open > 0 || stale.length > 0) {
    console.log(
      `FAIL ${findings.length} lines print a setting that can be a URL; ${open} not judged; ${stale.length} judgements name no line`
    );
    process.exit(1);
  }
  console.log(
    `PASS ${findings.length} lines print a setting that can be a URL, each judged; the planted shapes were found`
  );
}
