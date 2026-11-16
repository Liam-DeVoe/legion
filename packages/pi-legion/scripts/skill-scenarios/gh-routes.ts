// The routes of GitHub a worker world answers, and the sweep that reads which routes the role
// texts name. GitHub answers the same routes whichever label asks, so the routes a world must
// answer are every `gh` read the role texts of either label name — `skills/` and
// `packages/daemon/internal/prompts/` of each checkout — and every route the census saw an agent
// call, each of those declared with the run that called it (`WORLD_ROUTES`). A route a text
// names that the world does not answer is a refusal production never gives, and it voids the run
// that meets it; a route the world answers that nothing names is a world that tells an agent
// more than it was asked about.
//
// A route is written as a method and a path template: `GET repos/{owner}/{repo}/commits/{ref}/
// status` for a REST call, `GRAPHQL query repository.pullRequest.reviewThreads` for a GraphQL
// query (its fields from the root down to the first that selects more than one, or a page of a
// connection), `GRAPHQL mutation resolveReviewThread` for a mutation, and `GH pr view` for a gh
// command of its own. The gh stand-in reads its GraphQL calls with this module's parser and
// answers them with its executor, so the query the sweep names and the query the world answers
// are read one way.
import { readdirSync, readFileSync, statSync } from "node:fs";
import path from "node:path";
import { FIELD_FLAGS, methodOf, valueAt } from "./gh-call";

/** A route a text names or a call reaches: its key and whether it reads or writes. */
export interface RouteKey {
  readonly key: string;
  readonly kind: "read" | "write";
}

/** The `gh pr`, `gh run` and `gh auth` commands that read: every other one writes. */
const READ_COMMANDS: Readonly<Record<string, readonly string[]>> = {
  pr: ["view", "list", "diff", "checks", "status"],
  run: ["view", "list", "watch", "download"],
  auth: ["status"],
};

/** The flags of `gh api` that take the argument after them as their value, when written alone. */
const API_VALUE_FLAGS = [
  "-X",
  "--method",
  "-f",
  "-F",
  "--field",
  "--raw-field",
  "-H",
  "--header",
  "-q",
  "--jq",
  "-t",
  "--template",
  "--input",
  "--hostname",
  "-p",
  "--preview",
  "--cache",
];

/** The endpoint `gh api <argv...>` names: its first argument that is no flag and no flag's value,
 * or undefined for a call that names none. */
export function endpointOf(argv: readonly string[]): string | undefined {
  for (let index = 1; index < argv.length; index++) {
    const arg = argv[index] ?? "";
    if (arg.startsWith("-")) {
      if (API_VALUE_FLAGS.includes(arg)) index++;
      continue;
    }
    return arg;
  }
  return undefined;
}

/** A path segment that a text writes as a placeholder (`<n>`, `{owner}`, `$base_path`, `<approved
 * sha>...<new head>`) rather than as a value. */
const PLACEHOLDER = /[<>{}$]/;

/** The route `gh <argv...>` reaches, or undefined for a call that names none (a bare `gh api`, a
 * `gh pr` with no command, a GraphQL call whose query is a file or no query this parser reads).
 * A path segment written as a placeholder is `{}`; a value stays as it is written, and a route
 * template's parameter matches either (`templateMatches`). The query string is no part of the
 * route: GitHub routes `actions/runs?head_sha=…` to the runs list and filters it. */
export function routeOf(argv: readonly string[]): RouteKey | undefined {
  const [program, command] = argv;
  if (program === "pr" || program === "run" || program === "auth") {
    const name = (command ?? "").replace(/[.,:;]+$/, "");
    if (!/^[a-z][a-z-]*$/.test(name)) return undefined;
    const read = READ_COMMANDS[program]?.includes(name) ?? false;
    return { key: `GH ${program} ${name}`, kind: read ? "read" : "write" };
  }
  if (program !== "api") return undefined;
  const endpoint = endpointOf(argv)?.replace(/[.,:;]+$/, "");
  if (endpoint === undefined || endpoint === "") return undefined;
  if (endpoint === "graphql") {
    const query = graphqlQueryOf(argv);
    if (query === undefined) return undefined;
    const operation = parseGraphql(query);
    return operation === undefined
      ? undefined
      : { key: graphqlKey(operation), kind: operation.type === "query" ? "read" : "write" };
  }
  const segments = (endpoint.replace(/^\//, "").split("?")[0] ?? "")
    .split("/")
    .filter((segment) => segment !== "")
    .map((segment) => (PLACEHOLDER.test(segment) ? "{}" : segment));
  const method = methodOf(argv);
  return { key: `${method} ${segments.join("/")}`, kind: method === "GET" ? "read" : "write" };
}

/** The query text a `gh api graphql` call sends in its `query` field, or undefined when it sends
 * none, or sends a file (`-F query=@q.graphql`), whose text a role text does not hold. */
export function graphqlQueryOf(argv: readonly string[]): string | undefined {
  let query: string | undefined;
  for (const index of argv.keys()) {
    const pair = valueAt(argv, index, FIELD_FLAGS);
    if (pair?.startsWith("query=")) query = pair.slice("query=".length);
  }
  return query === undefined || query.startsWith("@") ? undefined : query;
}

/** Whether the route `key` is one `template` answers: the same method, and a path whose segments
 * are the template's, where a `{name}` segment of the template is any one segment and a
 * `{name+}` one is one segment or more. A GraphQL or gh command key is its template exactly. */
export function templateMatches(template: string, key: string): boolean {
  const [method = "", rest = ""] = splitKey(template);
  const [keyMethod = "", keyRest = ""] = splitKey(key);
  if (method !== keyMethod) return false;
  if (method === "GRAPHQL" || method === "GH") return rest === keyRest;
  const want = rest.split("/");
  const got = keyRest.split("/");
  const walk = (w: number, g: number): boolean => {
    if (w === want.length) return g === got.length;
    const segment = want[w] ?? "";
    if (/^\{\w+\+\}$/.test(segment)) {
      for (let end = g + 1; end <= got.length; end++) if (walk(w + 1, end)) return true;
      return false;
    }
    if (g === got.length) return false;
    const value = got[g] ?? "";
    if (/^\{\w+\}$/.test(segment)) return walk(w + 1, g + 1);
    return value === segment && walk(w + 1, g + 1);
  };
  return walk(0, 0);
}

function splitKey(key: string): [string, string] {
  const space = key.indexOf(" ");
  return space < 0 ? [key, ""] : [key.slice(0, space), key.slice(space + 1)];
}

/** One `gh` command a role text names: the route it reaches, where the text names it and the
 * command as the text writes it. */
export interface Named extends RouteKey {
  readonly file: string;
  readonly line: number;
  readonly command: string;
}

/** The directories of a checkout that hold its role texts: the skills, and the daemon's prompts. */
export const TEXT_DIRS = ["skills", path.join("packages", "daemon", "internal", "prompts")];

/** Every `gh` route the role texts under `root` name (`TEXT_DIRS`), with the file and line that
 * name it, and every GraphQL call they write whose query this module cannot read (`unread`): a
 * sweep that skipped a query it could not read would pass a text whose route the world refuses.
 * A bare `gh api graphql` in prose sends no query and names no route. A Go test file is no role
 * text, and the sweep reads none. */
export function sweep(root: string): { named: Named[]; unread: Omit<Named, "key" | "kind">[] } {
  const named: Named[] = [];
  const unread: Omit<Named, "key" | "kind">[] = [];
  for (const dir of TEXT_DIRS) {
    for (const file of filesUnder(path.join(root, dir))) {
      if (file.endsWith("_test.go")) continue;
      const relative = path.relative(root, file);
      for (const found of commandsIn(readFileSync(file, "utf8"))) {
        const route = routeOf(found.argv);
        const command = `gh ${found.argv.join(" ")}`;
        if (route !== undefined)
          named.push({ ...route, file: relative, line: found.line, command });
        else if (found.argv[0] === "api" && graphqlQueryOf(found.argv) !== undefined)
          unread.push({ file: relative, line: found.line, command });
      }
    }
  }
  return { named, unread };
}

function filesUnder(dir: string): string[] {
  let entries: string[];
  try {
    entries = readdirSync(dir).sort();
  } catch {
    return [];
  }
  return entries.flatMap((entry) => {
    const full = path.join(dir, entry);
    return statSync(full).isDirectory() ? filesUnder(full) : [full];
  });
}

/** Where a `gh api`, `gh pr` or `gh run` command starts in a text. */
const GH_COMMAND = /(?<![\w./-])gh[ \t]+(?:api|pr|run)\b/g;

/** Every `gh api`, `gh pr` and `gh run` command `text` writes, as the argv after `gh` a shell
 * would hand it, with the line it starts on. A command ends where a shell command ends — an
 * unescaped newline, `|`, `&&`, `;`, `)` or `>` outside quotes — and, written inside a
 * Markdown code span, at the span's closing backtick, the span's own line breaks being spaces.
 * A backslash at a line's end continues the command, as in a shell. A placeholder in angle
 * brackets is one word, spaces and all (`<approved sha>...<new head>`). */
export function commandsIn(text: string): { argv: string[]; line: number }[] {
  const found: { argv: string[]; line: number }[] = [];
  for (const match of text.matchAll(GH_COMMAND)) {
    const start = match.index ?? 0;
    const argv = wordsFrom(text, start, text[start - 1] === "`").slice(1);
    found.push({ argv, line: text.slice(0, start).split("\n").length });
  }
  return found;
}

function wordsFrom(text: string, start: number, inline: boolean): string[] {
  const words: string[] = [];
  let word: string | undefined;
  const end = () => {
    if (word !== undefined) words.push(word);
    word = undefined;
  };
  let index = start;
  while (index < text.length) {
    const char = text[index] ?? "";
    if (inline && char === "`") break;
    if (char === "\\" && text[index + 1] === "\n") {
      end();
      index += 2;
      continue;
    }
    if (char === "\n" && !inline) break;
    if (char === " " || char === "\t" || char === "\n") {
      end();
      index++;
      continue;
    }
    if (char === "'" || char === '"') {
      const close = closing(text, index, char);
      const span = close < 0 ? "" : text.slice(index + 1, close);
      if (close < 0 || (inline && span.includes("`"))) break;
      word = (word ?? "") + (char === '"' ? span.replace(/\\(["\\$`])/g, "$1") : span);
      index = close + 1;
      continue;
    }
    if (char === "<") {
      const close = text.indexOf(">", index);
      const span = close < 0 ? "" : text.slice(index, close + 1);
      if (close < 0 || span.includes("\n") || span.length > 80 || span.slice(1).includes("<"))
        break;
      word = (word ?? "") + span;
      index = close + 1;
      continue;
    }
    if ("|;&()>".includes(char)) break;
    word = (word ?? "") + char;
    index++;
  }
  end();
  return words;
}

/** The index of the quote that closes the one at `open`, or -1: a double quote skips each
 * backslash-escaped character, as a shell does. */
function closing(text: string, open: number, quote: string): number {
  for (let index = open + 1; index < text.length; index++) {
    if (quote === '"' && text[index] === "\\") index++;
    else if (text[index] === quote) return index;
  }
  return -1;
}

// GraphQL, as far as reading which route a query reaches and answering it from a world goes:
// operations, variables, fields with arguments and aliases, inline fragments. A fragment
// spread, a directive or a fragment definition is no query any text or call of these worlds
// writes, and a query holding one reads as none.

type Value =
  | { readonly kind: "var"; readonly name: string }
  | { readonly kind: "scalar"; readonly value: string | number | boolean | null }
  | { readonly kind: "enum"; readonly value: string }
  | { readonly kind: "list"; readonly values: readonly Value[] }
  | { readonly kind: "object"; readonly fields: Readonly<Record<string, Value>> };

export type Selection =
  | {
      readonly kind: "field";
      readonly alias: string | undefined;
      readonly name: string;
      readonly args: Readonly<Record<string, Value>>;
      readonly selections: readonly Selection[] | undefined;
    }
  | {
      readonly kind: "fragment";
      readonly on: string | undefined;
      readonly selections: readonly Selection[];
    };

export interface Operation {
  readonly type: "query" | "mutation" | "subscription";
  readonly variables: readonly { name: string; type: string }[];
  readonly selections: readonly Selection[];
}

const GRAPHQL_TOKEN =
  /\s+|,|#[^\n]*|(\.\.\.)|([{}()[\]:!=$@])|("(?:[^"\\]|\\.)*")|(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)|([_A-Za-z][_0-9A-Za-z]*)/y;

/** The operation `text` holds, or undefined when it holds none this parser reads. */
export function parseGraphql(text: string): Operation | undefined {
  const tokens: string[] = [];
  GRAPHQL_TOKEN.lastIndex = 0;
  while (GRAPHQL_TOKEN.lastIndex < text.length) {
    const match = GRAPHQL_TOKEN.exec(text);
    if (match === null) return undefined;
    const token = match[1] ?? match[2] ?? match[3] ?? match[4] ?? match[5];
    if (token !== undefined) tokens.push(token);
  }
  let at = 0;
  const peek = () => tokens[at];
  const take = (want?: string): string => {
    const token = tokens[at++];
    if (token === undefined || (want !== undefined && token !== want))
      throw new Error(`expected ${want ?? "a token"}, read ${token ?? "the end"}`);
    return token;
  };
  const name = (): string => {
    const token = take();
    if (!/^[_A-Za-z]/.test(token)) throw new Error(`expected a name, read ${token}`);
    return token;
  };
  const value = (): Value => {
    const token = take();
    if (token === "$") return { kind: "var", name: name() };
    if (token === "[") {
      const values: Value[] = [];
      while (peek() !== "]") values.push(value());
      take("]");
      return { kind: "list", values };
    }
    if (token === "{") {
      const fields: Record<string, Value> = {};
      while (peek() !== "}") {
        const key = name();
        take(":");
        fields[key] = value();
      }
      take("}");
      return { kind: "object", fields };
    }
    if (token.startsWith('"')) return { kind: "scalar", value: JSON.parse(token) as string };
    if (/^-?\d/.test(token)) return { kind: "scalar", value: Number(token) };
    if (token === "true" || token === "false") return { kind: "scalar", value: token === "true" };
    if (token === "null") return { kind: "scalar", value: null };
    return { kind: "enum", value: token };
  };
  const typeText = (): string => {
    if (peek() === "[") {
      take("[");
      const inner = typeText();
      take("]");
      return `[${inner}]${peek() === "!" ? take("!") : ""}`;
    }
    return `${name()}${peek() === "!" ? take("!") : ""}`;
  };
  const selectionSet = (): Selection[] => {
    take("{");
    const selections: Selection[] = [];
    while (peek() !== "}") {
      if (peek() === "...") {
        take("...");
        if (peek() !== "on") throw new Error("a fragment spread is no query this reads");
        take("on");
        const on = name();
        selections.push({ kind: "fragment", on, selections: selectionSet() });
        continue;
      }
      let field = name();
      let alias: string | undefined;
      if (peek() === ":") {
        take(":");
        alias = field;
        field = name();
      }
      const args: Record<string, Value> = {};
      if (peek() === "(") {
        take("(");
        while (peek() !== ")") {
          const key = name();
          take(":");
          args[key] = value();
        }
        take(")");
      }
      if (peek() === "@") throw new Error("a directive is no query this reads");
      const selections_ = peek() === "{" ? selectionSet() : undefined;
      selections.push({ kind: "field", alias, name: field, args, selections: selections_ });
    }
    take("}");
    return selections;
  };
  try {
    let type: Operation["type"] = "query";
    const variables: { name: string; type: string }[] = [];
    if (peek() !== "{") {
      const word = name();
      if (word !== "query" && word !== "mutation" && word !== "subscription") return undefined;
      type = word;
      if (peek() !== "(" && peek() !== "{") name();
      if (peek() === "(") {
        take("(");
        while (peek() !== ")") {
          take("$");
          const variable = name();
          take(":");
          const declared = typeText();
          if (peek() === "=") {
            take("=");
            value();
          }
          variables.push({ name: variable, type: declared });
        }
        take(")");
      }
    }
    const selections = selectionSet();
    if (at !== tokens.length) return undefined;
    return { type, variables, selections };
  } catch {
    return undefined;
  }
}

/** The route key of `operation`: a mutation by its first field, a query by the path of fields
 * from the root down to the first that selects more than one field, or a page of a connection
 * (`nodes`, `edges`, `pageInfo`). */
export function graphqlKey(operation: Operation): string {
  if (operation.type !== "query") {
    const first = operation.selections.find((selection) => selection.kind === "field");
    return `GRAPHQL ${operation.type} ${first?.kind === "field" ? first.name : ""}`;
  }
  const descend = (selections: readonly Selection[]): string[] => {
    const fields = selections.filter(
      (selection) => selection.kind !== "field" || selection.name !== "__typename"
    );
    const [only] = fields;
    if (fields.length !== 1 || only?.kind !== "field") return [];
    if (["nodes", "edges", "pageInfo"].includes(only.name)) return [];
    return [only.name, ...(only.selections === undefined ? [] : descend(only.selections))];
  };
  return `GRAPHQL query ${descend(operation.selections).join(".")}`;
}

/** What GitHub's GraphQL endpoint answers: the data, and the errors where there are any, which
 * gh prints to stderr and exits 1 on. */
export interface GraphAnswer {
  readonly data?: Record<string, unknown> | null;
  readonly errors?: readonly { type?: string; path?: string[]; message: string }[];
}

type Json = Record<string, unknown>;
/** Whether `value` is a JSON object: the one guard the world's answers are read by, here and in
 * the gh stand-in. */
export const isObject = (value: unknown): value is Json =>
  typeof value === "object" && value !== null && !Array.isArray(value);

/** The cursor of the item at `index` of a connection, opaque as GitHub's are, and back. */
const cursorOf = (index: number) => Buffer.from(`cursor:v2:${index}`).toString("base64");
function indexOf(cursor: unknown): number | undefined {
  if (typeof cursor !== "string") return undefined;
  const match = /^cursor:v2:(\d+)$/.exec(Buffer.from(cursor, "base64").toString());
  return match === null ? undefined : Number(match[1]);
}

/** `value` as the call's `variables` give it: a variable its value (null where the call sends
 * none), a list and an input object each of their values. */
function evaluated(value: Value, variables: Readonly<Record<string, unknown>>): unknown {
  switch (value.kind) {
    case "var":
      return variables[value.name] ?? null;
    case "scalar":
    case "enum":
      return value.value;
    case "list":
      return value.values.map((one) => evaluated(one, variables));
    case "object":
      return Object.fromEntries(
        Object.entries(value.fields).map(([key, field]) => [key, evaluated(field, variables)])
      );
  }
}

/** The arguments `field` is called with, as the call's `variables` give them: what the gh
 * stand-in reads a mutation's `input` from (gh-standin.ts's `threadCall`), by the reading the
 * executor below answers a query's arguments with. */
export function argumentsOf(
  field: Selection & { kind: "field" },
  variables: Readonly<Record<string, unknown>>
): Record<string, unknown> {
  return Object.fromEntries(
    Object.entries(field.args).map(([key, value]) => [key, evaluated(value, variables)])
  );
}

/** `operation` answered from `graph`, the world's state as GitHub's schema names it, with
 * `variables` the call's own. A list in the graph is a connection, paged as GitHub pages one
 * (`first`, `last`, `after`, `before`, `nodes`, `edges`, `pageInfo`, `totalCount`) and filtered by
 * `itemTypes` where a timeline is asked for by them; an object whose `__args` names the arguments
 * that find it (a repository by owner and name, a pull request by number) answers null and
 * GitHub's NOT_FOUND where a call names another; a field the graph does not hold is null. A
 * variable the operation declares non-null and the call does not send is GitHub's refusal of the
 * whole query. */
export function executeGraphql(
  operation: Operation,
  graph: Json,
  variables: Readonly<Record<string, unknown>>
): GraphAnswer {
  const missing = operation.variables.find(
    (variable) =>
      variable.type.endsWith("!") &&
      (variables[variable.name] === undefined || variables[variable.name] === null)
  );
  if (missing !== undefined)
    return {
      errors: [
        {
          message: `Variable $${missing.name} of type ${missing.type} was provided invalid value`,
        },
      ],
    };
  const errors: { type: string; path: string[]; message: string }[] = [];
  const items = new WeakSet<unknown[]>();
  const project = (selections: readonly Selection[], value: Json, at: string[]): Json => {
    const out: Json = {};
    for (const selection of selections) {
      if (selection.kind === "fragment") {
        if (selection.on === undefined || value.__typename === selection.on)
          Object.assign(out, project(selection.selections, value, at));
        continue;
      }
      const key = selection.alias ?? selection.name;
      out[key] =
        selection.name === "__typename"
          ? (value.__typename ?? null)
          : resolve(selection, value[selection.name], [...at, key]);
    }
    return out;
  };
  const resolve = (field: Selection & { kind: "field" }, raw: unknown, at: string[]): unknown => {
    if (raw === undefined || raw === null) return null;
    if (field.selections === undefined) return raw;
    if (Array.isArray(raw) && items.has(raw))
      return raw.map((item) => (isObject(item) ? project(field.selections ?? [], item, at) : item));
    const args = argumentsOf(field, variables);
    const list = Array.isArray(raw)
      ? raw
      : isObject(raw) && Array.isArray(raw.nodes)
        ? raw.nodes
        : undefined;
    if (list !== undefined) {
      const page = connection(list, args, at);
      return page === undefined ? null : project(field.selections, page, at);
    }
    if (!isObject(raw)) return null;
    const finds = raw.__args;
    if (isObject(finds))
      for (const [name, want] of Object.entries(args))
        if (name in finds && String(finds[name]).toLowerCase() !== String(want).toLowerCase()) {
          errors.push({ type: "NOT_FOUND", path: at, message: notFound(field.name, args) });
          return null;
        }
    return project(field.selections, raw, at);
  };
  const connection = (list: unknown[], args: Json, at: string[]): Json | undefined => {
    const types = Array.isArray(args.itemTypes)
      ? args.itemTypes.map((type) =>
          String(type)
            .toLowerCase()
            .replace(/(?:^|_)([a-z])/g, (_, letter: string) => letter.toUpperCase())
        )
      : undefined;
    const all = list
      .map((node, index) => ({ node, index }))
      .filter(
        ({ node }) =>
          types === undefined || (isObject(node) && types.includes(String(node.__typename)))
      );
    let from = 0;
    let to = all.length;
    for (const [name, bound] of [
      ["after", 1],
      ["before", 0],
    ] as const) {
      if (args[name] === undefined || args[name] === null) continue;
      const index = indexOf(args[name]);
      if (index === undefined) {
        errors.push({
          type: "INVALID_CURSOR_ARGUMENTS",
          path: at,
          message: `\`${String(args[name])}\` does not appear to be a valid cursor.`,
        });
        return undefined;
      }
      const position = all.findIndex((entry) => entry.index === index);
      if (bound === 1) from = Math.max(from, position + 1);
      else to = Math.min(to, position < 0 ? to : position);
    }
    let window = all.slice(from, to);
    const first = typeof args.first === "number" ? args.first : undefined;
    const last = typeof args.last === "number" ? args.last : undefined;
    const before = window.length;
    if (first !== undefined) window = window.slice(0, first);
    if (last !== undefined) window = window.slice(Math.max(0, window.length - last));
    const nodes = window.map(({ node }) => node);
    const edges = window.map(({ node, index }) => ({ cursor: cursorOf(index), node }));
    items.add(nodes);
    items.add(edges);
    const startEntry = window[0];
    const endEntry = window[window.length - 1];
    return {
      nodes,
      edges,
      totalCount: all.length,
      pageInfo: {
        hasNextPage:
          first !== undefined ? window.length < before || to < all.length : to < all.length,
        hasPreviousPage: last !== undefined ? window.length < before || from > 0 : from > 0,
        startCursor: startEntry === undefined ? null : cursorOf(startEntry.index),
        endCursor: endEntry === undefined ? null : cursorOf(endEntry.index),
      },
    };
  };
  const data = project(operation.selections, graph, []);
  return errors.length === 0 ? { data } : { data, errors };
}

/** GitHub's NOT_FOUND message for the object `field` names by `args`. */
function notFound(field: string, args: Json): string {
  if (field === "repository")
    return `Could not resolve to a Repository with the name '${String(args.owner)}/${String(args.name)}'.`;
  if (field === "pullRequest")
    return `Could not resolve to a PullRequest with the number of ${String(args.number)}.`;
  return `Could not resolve to a ${field[0]?.toUpperCase() ?? ""}${field.slice(1)}.`;
}

/** Why a world answers a route no role text of either label names: a recorded run whose agent
 * called it (`run`, `<batch>/<run>/calls.jsonl:<line>`), or, where no recorded run called it, why
 * GitHub serves it to a role (`world`). A world that stopped answering such a route would give a
 * refusal production does not, so it stays, and says why. */
export type Evidence =
  | { readonly run: string; readonly reason: string }
  | { readonly world: string };

/** One route a world answers: its key (`templateMatches`), what its answer is read from, and,
 * for a route no role text of either label names, its evidence. */
export interface WorldRoute {
  readonly route: string;
  readonly answer: string;
  readonly evidence?: Evidence;
}

/** Every route a worker world answers (rig.sh's fixture_write and the gh stand-in's own answers).
 * A route a role text names needs no evidence; a route neither label's texts name carries its
 * evidence (`Evidence`). A call outside this table is refused as `gh: Not Found (HTTP 404)`
 * (gh-standin.ts), which `rig.sh health` reports as a world missing an answer. */
export const WORLD_ROUTES: readonly WorldRoute[] = [
  // Reads.
  {
    route: "GET repos/{owner}/{repo}",
    answer: "world.json: the repository, its default branch and GitHub's default merge settings",
  },
  {
    route: "GET repos/{owner}/{repo}/pulls/{number}",
    answer: "world.json and the remote: the pull request at the branch's tip",
  },
  {
    route: "GET repos/{owner}/{repo}/pulls/{number}/reviews",
    answer: "the world's reviews, then the reviews calls.jsonl records the run creating",
  },
  {
    route: "GET repos/{owner}/{repo}/pulls/{number}/reviews/{id}",
    answer: "the review calls.jsonl records the run creating, or the world's review by id, or 404",
    evidence: {
      run: "round17-forty/threads-closed-text-fixes-base-2/calls.jsonl:33",
      reason: "a reviewer reads one review back by its id",
    },
  },
  {
    route: "GET repos/{owner}/{repo}/pulls/{number}/reviews/{id}/comments",
    answer: "the comments of that review's threads: the world's, and the ones its create carried",
    evidence: {
      run: "round17-forty/reviewer-text-findings-text-only-head-2/calls.jsonl:26",
      reason: "a reviewer reads back the inline comments of the review it created",
    },
  },
  {
    route: "GET repos/{owner}/{repo}/pulls/{number}/comments",
    answer:
      "every comment of the review threads: the world's, then each one calls.jsonl records the run making",
    evidence: {
      run: "round17-forty/reviewer-text-findings-text-only-head-2/calls.jsonl:25",
      reason: "a reviewer lists the pull request's inline comments beside its threads",
    },
  },
  {
    route: "GET repos/{owner}/{repo}/issues/{number}/comments",
    answer: "the pull request's conversation: the comments calls.jsonl records the run posting",
    evidence: {
      run: "round17-forty/reviewer-text-findings-defect-base-2/calls.jsonl:29",
      reason: "a reviewer reads the pull request's conversation before it reviews",
    },
  },
  {
    route: "GET repos/{owner}/{repo}/commits/{ref}/check-runs",
    answer: "the world's check runs, reported on the ref the remote resolves, with its head_sha",
  },
  {
    route: "GET repos/{owner}/{repo}/commits/{ref}/status",
    answer: "the world's commit statuses, none, so `state` pending; the ref resolved on the remote",
  },
  {
    route: "GET repos/{owner}/{repo}/actions/runs",
    answer: "the world's workflow runs: none, its checks being check runs of no Actions workflow",
  },
  {
    route: "GET repos/{owner}/{repo}/actions/runs/{run_id}/jobs",
    answer: "404, as GitHub answers a run it has not got: the world holds no workflow run",
  },
  {
    route: "GET repos/{owner}/{repo}/rules/branches/{branch+}",
    answer: "the base branch's rulesets: none, or the one requiring conversation resolution",
  },
  {
    route: "GET repos/{owner}/{repo}/branches/{branch}/protection",
    answer: "the base branch's protection: the checks it requires, and conversation resolution",
    evidence: {
      world:
        "GitHub's protection read of the base branch the merge gate reads, which a reviewer reaches for its required checks",
    },
  },
  {
    route: "GET repos/{owner}/{repo}/branches/{branch+}",
    answer: "the base branch, protected, with the checks its protection requires",
  },
  {
    route: "GET repos/{owner}/{repo}/compare/{basehead+}",
    answer: "the remote: the comparison of the two refs",
  },
  {
    route: "GET repos/{owner}/{repo}/git/ref/{ref+}",
    answer: "the remote: the ref and the commit it points at, or 404",
  },
  {
    route: "GET repos/{owner}/{repo}/contents/{path+}",
    answer: "the remote: the file or directory at the ref, raw or as GitHub's JSON, or 404",
    evidence: {
      run: "not-counted-r24/threads-closed-text-fixes-head-3/calls.jsonl:16",
      reason: "a head reviewer re-reads the fixed file at the head through the API",
    },
  },
  {
    route: "GET user",
    answer: "world.json: the login the run's token acts as",
    evidence: { world: "the identity read every gh session can make" },
  },
  {
    route: "GH auth status",
    answer: "world.json: the login the run's token acts as",
    evidence: { world: "the identity read every gh session can make" },
  },
  {
    route: "GH pr view",
    answer: "world.json and the remote: the pull request at the branch's tip",
  },
  { route: "GH pr list", answer: "world.json: the one pull request open on the issue branch" },
  { route: "GH pr diff", answer: "the remote: the issue branch against the base" },
  { route: "GH pr checks", answer: "the world's check runs of the branch's tip" },
  { route: "GH run view", answer: "gh's refusal: the world holds no workflow run" },
  { route: "GH run watch", answer: "gh's refusal: the world holds no workflow run" },
  {
    route: "GRAPHQL query repository.pullRequest.reviewThreads",
    answer: "the world's review threads and every call calls.jsonl records on them, paged",
  },
  { route: "GRAPHQL query viewer.login", answer: "world.json: the login the run's token acts as" },
  {
    route: "GRAPHQL query repository.pullRequest",
    answer: "world.json and the remote: the pull request at the branch's tip, in no merge queue",
  },
  {
    route: "GRAPHQL query repository.pullRequest.timelineItems",
    answer: "the world's timeline events: none",
  },
  // Writes.
  {
    route: "POST repos/{owner}/{repo}/pulls/{number}/reviews",
    answer: "the review the call creates, its state, head and body the call's own",
  },
  {
    route: "POST repos/{owner}/{repo}/pulls/{number}/reviews/{id}/events",
    answer: "the pending review calls.jsonl records the run creating, or 422",
    evidence: { world: "submits the pending review the world's own create leaves" },
  },
  {
    route: "PATCH repos/{owner}/{repo}/pulls/{number}",
    answer: "the pull request with the body the call leaves",
  },
  {
    route: "POST repos/{owner}/{repo}/pulls/{number}/comments/{id}/replies",
    answer: "the reply, in the thread of the comment the path names, or 404",
    evidence: { world: "the REST form of the review-thread reply" },
  },
  {
    route: "POST repos/{owner}/{repo}/pulls/{number}/comments",
    answer:
      "the comment the call makes: a thread of its own, or a reply where it names in_reply_to",
    evidence: {
      run: "round17-forty/reviewer-text-findings-text-only-head-5/calls.jsonl:21",
      reason: "a reviewer posts an inline comment on its own",
    },
  },
  { route: "POST repos/{owner}/{repo}/git/refs", answer: "the write route's created object" },
  { route: "GH pr comment", answer: "the comment's URL; the comment kept for the conversation" },
  {
    route: "GH pr edit",
    answer: "the pull request's URL; the body the call leaves kept",
    evidence: {
      run: "round17-forty/reviewer-text-findings-defect-base-4/calls.jsonl:32",
      reason: "a role writes the body's sections with gh pr edit",
    },
  },
  {
    route: "GH pr review",
    answer: "the review's URL; the review at the branch's tip",
    evidence: { world: "the CLI form of the review create the texts name" },
  },
  {
    route: "GH pr ready",
    answer: "the pull request's URL",
    evidence: { world: "the CLI form of marking the pull request ready, which its author may" },
  },
  {
    route: "GRAPHQL mutation resolveReviewThread",
    answer: "the thread the call names, resolved, or GitHub's NOT_FOUND",
  },
  {
    route: "GRAPHQL mutation addPullRequestReviewThreadReply",
    answer: "the reply the call posts in the thread it names, or GitHub's NOT_FOUND",
    evidence: {
      run: "round17-forty/reviewer-text-findings-defect-base-5/calls.jsonl:34",
      reason: "a reviewer replies on a review thread through GraphQL",
    },
  },
];

/** The row of `WORLD_ROUTES` that answers `key`, or undefined: the first that matches, so a
 * narrower route stands ahead of a wider one it shares a prefix with. */
export function worldRouteOf(key: string): WorldRoute | undefined {
  return WORLD_ROUTES.find((row) => templateMatches(row.route, key));
}
