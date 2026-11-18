// The GraphQL half of the gh stand-in (gh-standin.ts): a `gh api graphql` call's query, read as
// GitHub reads it, and answered from the world a route holds, field by field, as GitHub answers it.
// A route that answers GraphQL holds the world as a tree (`graphql` in its answer): the fields the
// world holds, under the names GitHub gives them. A field with arguments that pick what it is (a
// repository by its owner and name, a pull request by its number, an object by its oid or its
// expression) is held under its name and those arguments, `object(oid:"<sha>")`, in the canonical
// form below, or under `name(*)` for every other value of them; a field whose arguments only page
// it (`first`, `last`, `after`, `before`) is held under its bare name. A node held once and read
// under several names is written once under `$refs` and named by `{"$ref": <name>}`; a node the
// stand-in keeps (the review threads) is named by `{"$keep": <name>}`; and a name GitHub cannot
// resolve is `{"$error": {type, message}}`, whose message may name the field's arguments as
// `{<argument>}`. The answer is the query's own selection of that tree and nothing more, in the
// order the query asks: a field the tree does not hold, a fragment on a node with no `__typename`,
// a selection on a scalar or none on an object, two selections answering one key, a page the tree
// cannot state (a cursor, a slice that leaves nodes out where the query reads the page), a fragment
// spread, a directive, a variable the query does not declare a value for: each is a request the rig
// does not model (Unmodeled), which the stand-in refuses. An `$error` answers GitHub's: null where
// the field stands, and the error with its path and its place in the query.

/** A request the rig does not model, which the stand-in refuses as such. */
export class Unmodeled extends Error {}

type Scalar = null | boolean | number | string;
/** An argument's value with its variables put in: an enum value stays a name of its own. */
export type Value = Scalar | { enum: string } | Value[] | { [key: string]: Value };
export type Selection =
  | {
      kind: "field";
      alias: string | undefined;
      name: string;
      args: Record<string, Value>;
      sub: Selection[] | undefined;
      line: number;
      column: number;
    }
  | { kind: "fragment"; on: string; sub: Selection[] };
export type Document = { operation: string; selections: Selection[] };

type Token = {
  kind: "name" | "punct" | "string" | "number";
  text: string;
  line: number;
  column: number;
};

function tokens(text: string): Token[] {
  const out: Token[] = [];
  let line = 1;
  let lineStart = 0;
  let i = 0;
  while (i < text.length) {
    const c = text[i] as string;
    if (c === "\n") {
      line += 1;
      i += 1;
      lineStart = i;
      continue;
    }
    if (/[\s,\ufeff]/.test(c)) {
      i += 1;
      continue;
    }
    if (c === "#") {
      while (i < text.length && text[i] !== "\n") i += 1;
      continue;
    }
    const column = i - lineStart + 1;
    if (text.startsWith("...", i)) {
      out.push({ kind: "punct", text: "...", line, column });
      i += 3;
    } else if ("{}():!=$[]@|&".includes(c)) {
      out.push({ kind: "punct", text: c, line, column });
      i += 1;
    } else if (c === '"') {
      if (text.startsWith('"""', i)) throw new Unmodeled("a block string");
      let j = i + 1;
      while (j < text.length && text[j] !== '"') j += text[j] === "\\" ? 2 : 1;
      if (j >= text.length) throw new Unmodeled("an unterminated string");
      out.push({ kind: "string", text: JSON.parse(text.slice(i, j + 1)), line, column });
      i = j + 1;
    } else if (/[-0-9]/.test(c)) {
      const m = /^-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/.exec(text.slice(i));
      if (!m) throw new Unmodeled(`an unexpected ${c}`);
      out.push({ kind: "number", text: m[0], line, column });
      i += m[0].length;
    } else if (/[_A-Za-z]/.test(c)) {
      const m = /^[_A-Za-z][_0-9A-Za-z]*/.exec(text.slice(i)) as RegExpExecArray;
      out.push({ kind: "name", text: m[0], line, column });
      i += m[0].length;
    } else throw new Unmodeled(`an unexpected ${c}`);
  }
  return out;
}

/** The query `text` of a call, its variables put in from `variables` (an undeclared one is null). */
export function parse(text: string, variables: Record<string, unknown>): Document {
  const ts = tokens(text);
  let at = 0;
  const peek = () => ts[at];
  const next = () => {
    const t = ts[at];
    if (t === undefined) throw new Unmodeled("a query that ends early");
    at += 1;
    return t;
  };
  const expect = (text: string) => {
    const t = next();
    if (t.text !== text || t.kind === "string") throw new Unmodeled(`${t.text} where ${text} goes`);
    return t;
  };
  const skipBalanced = (open: string, close: string) => {
    let depth = 0;
    do {
      const t = next();
      if (t.kind === "punct" && t.text === open) depth += 1;
      if (t.kind === "punct" && t.text === close) depth -= 1;
    } while (depth > 0);
  };
  const value = (): Value => {
    const t = next();
    if (t.kind === "string") return t.text;
    if (t.kind === "number") return Number(t.text);
    if (t.kind === "punct" && t.text === "$") {
      const name = next().text;
      const given = variables[name];
      return given === undefined ? null : (given as Value);
    }
    if (t.kind === "punct" && t.text === "[") {
      const list: Value[] = [];
      while (peek()?.text !== "]") list.push(value());
      expect("]");
      return list;
    }
    if (t.kind === "punct" && t.text === "{") {
      const object: Record<string, Value> = {};
      while (peek()?.text !== "}") {
        const key = next().text;
        expect(":");
        object[key] = value();
      }
      expect("}");
      return object;
    }
    if (t.kind === "name") {
      if (t.text === "true") return true;
      if (t.text === "false") return false;
      if (t.text === "null") return null;
      return { enum: t.text };
    }
    throw new Unmodeled(`a value ${t.text}`);
  };
  const selections = (): Selection[] => {
    expect("{");
    const out: Selection[] = [];
    while (peek()?.text !== "}") {
      const t = next();
      if (t.kind === "punct" && t.text === "...") {
        const on = next();
        if (on.text !== "on") throw new Unmodeled("a fragment spread");
        const type = next().text;
        if (peek()?.text === "@") throw new Unmodeled("a directive");
        out.push({ kind: "fragment", on: type, sub: selections() });
        continue;
      }
      if (t.kind !== "name") throw new Unmodeled(`${t.text} where a field goes`);
      let alias: string | undefined;
      let name = t.text;
      if (peek()?.text === ":") {
        next();
        alias = name;
        name = next().text;
      }
      const args: Record<string, Value> = {};
      if (peek()?.text === "(") {
        next();
        while (peek()?.text !== ")") {
          const key = next().text;
          expect(":");
          args[key] = value();
        }
        expect(")");
      }
      if (peek()?.text === "@") throw new Unmodeled("a directive");
      const sub = peek()?.text === "{" ? selections() : undefined;
      out.push({ kind: "field", alias, name, args, sub, line: t.line, column: t.column });
    }
    expect("}");
    return out;
  };
  let operation = "query";
  const first = peek();
  if (first?.kind === "name") {
    if (first.text === "fragment") throw new Unmodeled("a fragment definition");
    operation = next().text;
    if (peek()?.kind === "name") next();
    if (peek()?.text === "(") skipBalanced("(", ")");
    if (peek()?.text === "@") throw new Unmodeled("a directive");
  }
  const document = { operation, selections: selections() };
  if (at !== ts.length) throw new Unmodeled("a second definition");
  return document;
}

/** A value as the canonical text writes it. */
function render(value: Value): string {
  if (value === null || typeof value !== "object") return JSON.stringify(value);
  if (Array.isArray(value)) return `[${value.map(render).join(",")}]`;
  if ("enum" in value && typeof value.enum === "string" && Object.keys(value).length === 1)
    return value.enum;
  return `{${Object.keys(value)
    .sort()
    .map((key) => `${key}:${render((value as Record<string, Value>)[key] as Value)}`)
    .join(",")}}`;
}
const PAGING: Record<string, true> = { first: true, last: true, after: true, before: true };
/** The arguments of a field that pick what it is, as the tree keys it: `name(a:1,b:"x")`. */
function keyOf(name: string, args: Record<string, Value>): string {
  const picking = Object.keys(args)
    .filter((key) => !PAGING[key])
    .sort();
  if (picking.length === 0) return name;
  return `${name}(${picking.map((key) => `${key}:${render(args[key] as Value)}`).join(",")})`;
}

/** The query as routes match it: no aliases, no whitespace but one space between selections,
 * every variable put in, arguments sorted, `mutation`/`subscription` ahead of a non-query. */
export function canonical(document: Document): string {
  const set = (selections: Selection[]): string =>
    `{${selections
      .map((s) =>
        s.kind === "fragment"
          ? `...on ${s.on}${set(s.sub)}`
          : `${s.name}${
              Object.keys(s.args).length === 0
                ? ""
                : `(${Object.keys(s.args)
                    .sort()
                    .map((key) => `${key}:${render(s.args[key] as Value)}`)
                    .join(",")})`
            }${s.sub === undefined ? "" : set(s.sub)}`
      )
      .join(" ")}}`;
  return (document.operation === "query" ? "" : document.operation) + set(document.selections);
}

type Tree = unknown;
export type GraphqlError = {
  type: string;
  path: (string | number)[];
  locations: { line: number; column: number }[];
  message: string;
};

/** The query's own selection of `tree` (the route's `graphql`), with `$refs` and the stand-in's
 * `keep` resolving the names a tree gives its shared nodes; and the errors GitHub would give. */
export function project(
  document: Document,
  tree: Tree,
  refs: Record<string, Tree>,
  keep: (marker: Record<string, unknown>) => Tree
): { data: unknown; errors: GraphqlError[] } {
  const errors: GraphqlError[] = [];
  const resolve = (node: Tree): Tree => {
    let at = node;
    for (let hops = 0; hops < 8; hops += 1) {
      if (typeof at !== "object" || at === null || Array.isArray(at)) return at;
      if ("$ref" in at) {
        const named = refs[String((at as { $ref: unknown }).$ref)];
        if (named === undefined) throw new Unmodeled("a node the tree names and does not hold");
        at = named;
      } else if ("$keep" in at) at = keep(at as Record<string, unknown>);
      else return at;
    }
    throw new Unmodeled("a node that names itself");
  };
  const page = (node: Record<string, Tree>, args: Record<string, Value>, sub: Selection[]) => {
    if ((args.after ?? null) !== null || (args.before ?? null) !== null)
      throw new Unmodeled("a page past the first");
    const { first, last } = args;
    if (first === undefined && last === undefined) return node;
    if (!Array.isArray(node.nodes)) throw new Unmodeled("a page of a field that is no connection");
    const all = node.nodes as Tree[];
    const count = typeof first === "number" ? first : typeof last === "number" ? last : all.length;
    if (count >= all.length) return node;
    if (sub.some((s) => s.kind === "field" && s.name === "pageInfo"))
      throw new Unmodeled("a page that leaves nodes out, read with its page info");
    return { ...node, nodes: typeof first === "number" ? all.slice(0, count) : all.slice(-count) };
  };
  const select = (node: Tree, selections: Selection[], path: (string | number)[]): unknown => {
    const at = resolve(node);
    if (at === null) return null;
    if (Array.isArray(at))
      return at.map((item, index) => select(item, selections, [...path, index]));
    if (typeof at !== "object") throw new Unmodeled("a selection on a scalar");
    const object = at as Record<string, Tree>;
    const out: Record<string, unknown> = {};
    const fill = (from: Selection[]) => {
      for (const s of from) {
        if (s.kind === "fragment") {
          if (typeof object.__typename !== "string")
            throw new Unmodeled("a fragment on an untyped node");
          if (object.__typename === s.on) fill(s.sub);
          continue;
        }
        const key = s.alias ?? s.name;
        if (key in out) throw new Unmodeled("two selections of one key");
        const held = keyOf(s.name, s.args);
        let value = object[held];
        if (value === undefined && held !== s.name) value = object[`${s.name}(*)`];
        if (value === undefined) throw new Unmodeled(`a field the world does not hold: ${held}`);
        value = resolve(value);
        if (
          typeof value === "object" &&
          value !== null &&
          !Array.isArray(value) &&
          "$error" in value
        ) {
          const { type, message } = (value as { $error: { type: string; message: string } }).$error;
          errors.push({
            type,
            path: [...path, key],
            locations: [{ line: s.line, column: s.column }],
            message: message.replace(/\{(\w+)\}/g, (named, arg: string) =>
              s.args[arg] === undefined ? named : String(s.args[arg] as Scalar)
            ),
          });
          out[key] = null;
          continue;
        }
        if (s.sub === undefined) {
          const leaf = Array.isArray(value)
            ? value.every((v) => typeof v !== "object" || v === null)
            : typeof value !== "object" || value === null;
          if (!leaf) throw new Unmodeled("an object read with no selection");
          out[key] = value;
          continue;
        }
        if (value === null) {
          out[key] = null;
          continue;
        }
        if (typeof value !== "object") throw new Unmodeled("a selection on a scalar");
        const paged = Array.isArray(value)
          ? value
          : page(value as Record<string, Tree>, s.args, s.sub);
        out[key] = select(paged, s.sub, [...path, key]);
      }
    };
    fill(selections);
    return out;
  };
  const data = select(tree, document.selections, []);
  return { data, errors };
}
