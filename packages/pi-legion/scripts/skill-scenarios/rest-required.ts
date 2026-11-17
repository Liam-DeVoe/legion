// The fields GitHub's REST API description marks required in the success answer of each REST
// operation the world serves, and the reading of an answer against them: rest-required.json,
// derived by this module's `derive` from github/rest-api-description at DESCRIPTION_COMMIT and
// never written by hand. A REST route the world serves (gh-routes.ts's WORLD_ROUTES, the gh
// stand-in's, and github-routes.ts's GITHUB_ROUTES, the HTTP stand-in Dispatch reads) answers
// every field its operation's success schema requires, as the description reads it: through
// `$ref`, every part of an `allOf`, the closest of a `oneOf` or an `anyOf` (the one the answer
// lacks the fewest fields of), and each item of an array (`absentFields`), the reading of
// INFRA-764's read search, which found the world's answers short of 79 fields of the repository,
// 37 of the pull request and 17 of every user (2026-11-17).
//
// The extract keeps, for each route, the operation it is and that operation's success status and
// media type, and the required tree of its schema: each named schema it reaches once, under
// `schemas`, and each part of a schema only where something below it is required, so a cycle of
// schemas (a repository's template repository) is a name and no copy. A route's answer that is
// no JSON (a raw file, a diff, a commit's SHA under its media type) has no schema to check.
//
//   bun rest-required.ts <api.github.com.json>
//
// writes the extract from `descriptions/api.github.com/api.github.com.json` of
// github/rest-api-description at DESCRIPTION_COMMIT (fetch it with `curl -fsSLO
// https://raw.githubusercontent.com/github/rest-api-description/<commit>/descriptions/api.github.com/api.github.com.json`),
// then formats it as the repository's biome does. A route the description has no operation for
// fails the derivation, naming it.
import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { z } from "zod";
import { WORLD_ROUTES } from "./gh-routes";
import { GITHUB_ROUTES } from "./github-routes";

/** The extract this directory keeps. */
export const REST_REQUIRED = path.join(import.meta.dir, "rest-required.json");
/** The commit of github/rest-api-description the extract is derived from. */
export const DESCRIPTION_COMMIT = "c9c95fe34ced05fe076001f5c55704742bc8e7f8";
const DESCRIPTION_FILE = "descriptions/api.github.com/api.github.com.json";

/** A schema as the extract keeps it: a named schema (`ref`), the items of an array, every part of
 * an `allOf`, the closest of a `oneOf` or an `anyOf`, or an object's required fields and the
 * fields below which something is required. `{}` requires nothing. */
export type Required =
  | { readonly ref: string }
  | { readonly items: Required }
  | { readonly allOf: readonly Required[] }
  | { readonly oneOf: readonly Required[] }
  | { readonly anyOf: readonly Required[] }
  | {
      readonly required?: readonly string[];
      readonly properties?: Readonly<Record<string, Required>>;
    };
const Node: z.ZodType<Required> = z.lazy(() =>
  z.union([
    z.strictObject({ ref: z.string() }),
    z.strictObject({ items: Node }),
    z.strictObject({ allOf: z.array(Node) }),
    z.strictObject({ oneOf: z.array(Node) }),
    z.strictObject({ anyOf: z.array(Node) }),
    z.strictObject({
      required: z.array(z.string()).optional(),
      properties: z.record(z.string(), Node).optional(),
    }),
  ])
);
const Operation = z.strictObject({
  operation_id: z.string(),
  status: z.number(),
  media: z.string(),
  schema: Node.optional(),
});
/** One operation of the description as the extract keeps it: its id, the status of its success,
 * the media type the world answers it in, and the required tree of that answer, where it is JSON. */
export type RestOperation = z.infer<typeof Operation>;
const Extract = z.strictObject({
  source: z.string(),
  commit: z.string(),
  file: z.string(),
  routes: z.record(z.string(), z.string()),
  operations: z.record(z.string(), Operation),
  schemas: z.record(z.string(), Node),
});
type Extract = z.infer<typeof Extract>;

let extract: Extract | undefined;
function loaded(): Extract {
  extract ??= Extract.parse(JSON.parse(readFileSync(REST_REQUIRED, "utf8")));
  return extract;
}

/** The commit of the description the committed extract was derived from. */
export function extractCommit(): string {
  return loaded().commit;
}

/** The operation of the description the world's REST route `route` is (a WORLD_ROUTES key such as
 * `GET repos/{owner}/{repo}/pulls/{number}`, or a GITHUB_ROUTES one), with its key (`GET
 * /repos/{owner}/{repo}/pulls/{pull_number}`), or undefined for a route the extract does not
 * hold. */
export function restOperation(route: string): (RestOperation & { key: string }) | undefined {
  const { routes, operations } = loaded();
  const key = routes[route];
  const operation = key === undefined ? undefined : operations[key];
  return key === undefined || operation === undefined ? undefined : { ...operation, key };
}

const isObject = (value: unknown): value is Record<string, unknown> =>
  typeof value === "object" && value !== null && !Array.isArray(value);

/** The paths of the fields `schema` requires that `value` lacks, as the description reads them
 * (`.field`, `[]` for an array's items): a named schema is read through its name in `schemas`
 * (the committed extract's), every part of an `allOf` is required, a `oneOf` or an `anyOf` is the
 * variant `value` lacks the fewest fields of, an array's every item is read, and an object's
 * required fields are read on an object and each field below which something is required on the
 * object or array `value` holds there. */
export function absentFields(
  value: unknown,
  schema: Required,
  schemas: Readonly<Record<string, Required>> = loaded().schemas,
  at = ""
): string[] {
  let node = schema;
  while ("ref" in node) {
    const named = schemas[node.ref];
    if (named === undefined) throw new Error(`the extract holds no schema ${node.ref}`);
    node = named;
  }
  if ("items" in node)
    return (Array.isArray(value) ? value : []).flatMap((item) =>
      absentFields(item, node.items, schemas, `${at}[]`)
    );
  if ("allOf" in node) return node.allOf.flatMap((part) => absentFields(value, part, schemas, at));
  if ("oneOf" in node || "anyOf" in node)
    return ("oneOf" in node ? node.oneOf : node.anyOf)
      .map((variant) => absentFields(value, variant, schemas, at))
      .reduce((closest, one) => (one.length < closest.length ? one : closest));
  if (!isObject(value)) return [];
  return [
    ...(node.required ?? []).filter((field) => !(field in value)).map((field) => `${at}.${field}`),
    ...Object.entries(node.properties ?? {}).flatMap(([field, below]) => {
      const held = value[field];
      return typeof held === "object" && held !== null
        ? absentFields(held, below, schemas, `${at}.${field}`)
        : [];
    }),
  ];
}

/** The REST routes the world serves: every WORLD_ROUTES route of an HTTP verb, written without
 * its leading slash, and every GITHUB_ROUTES route, written with it, with the media type the
 * world answers it in. */
export function servedRoutes(): { route: string; accept?: string }[] {
  return [
    ...WORLD_ROUTES.filter((row) => /^(GET|POST|PUT|PATCH|DELETE) /.test(row.route)).map((row) => ({
      route: row.route,
    })),
    ...GITHUB_ROUTES.map((row) => ({
      route: row.route,
      ...(row.accept === undefined ? {} : { accept: row.accept }),
    })),
  ];
}

// The description, as far as the derivation reads it.
type Json = Record<string, unknown>;
const JSON_MEDIA = "application/json";

/** The OpenAPI path of `paths` that the world's `template` is, for `method`: the same segments,
 * where a world parameter (`{name}`, `{name+}`) stands for a parameter of the description's. */
function pathOf(paths: Json, method: string, template: string): string | undefined {
  const parameter = (segment: string) => /^\{[^}]+\}$/.test(segment);
  const segments = template.replace(/^\//, "").split("/");
  const found = Object.keys(paths).filter((candidate) => {
    const item = paths[candidate];
    if (!isObject(item) || !isObject(item[method])) return false;
    const theirs = candidate.replace(/^\//, "").split("/");
    return (
      theirs.length === segments.length &&
      theirs.every((segment, index) => {
        const ours = segments[index] ?? "";
        return parameter(segment) ? parameter(ours) : segment === ours;
      })
    );
  });
  if (found.length > 1) throw new Error(`${method} ${template} is ${found.join(" and ")}`);
  return found[0];
}

/** The extract of `description` (GitHub's REST API description, parsed) for `served`. */
export function derive(description: Json, served = servedRoutes()): Extract {
  const paths = description.paths;
  const components = description.components;
  if (!isObject(paths) || !isObject(components)) throw new Error("no paths or no components");
  const named = isObject(components.schemas) ? components.schemas : {};
  const responses = isObject(components.responses) ? components.responses : {};
  const deref = (value: unknown, table: Json): unknown => {
    let at = value;
    while (isObject(at) && typeof at.$ref === "string") at = table[at.$ref.split("/").pop() ?? ""];
    return at;
  };

  // Every named schema the served operations reach, and whether anything below it is required.
  const reached = new Map<string, Json>();
  const reach = (schema: unknown): void => {
    if (!isObject(schema)) return;
    if (typeof schema.$ref === "string") {
      const name = schema.$ref.split("/").pop() ?? "";
      if (reached.has(name)) return;
      const body = named[name];
      if (!isObject(body)) throw new Error(`no schema ${name}`);
      reached.set(name, body);
      reach(body);
      return;
    }
    for (const part of [
      schema.items,
      ...(Array.isArray(schema.allOf) ? schema.allOf : []),
      ...(Array.isArray(schema.oneOf) ? schema.oneOf : []),
      ...(Array.isArray(schema.anyOf) ? schema.anyOf : []),
      ...Object.values(isObject(schema.properties) ? schema.properties : {}),
    ])
      reach(part);
  };

  const routes: Record<string, string> = {};
  const operations: Record<
    string,
    { operation_id: string; status: number; media: string; schema?: unknown }
  > = {};
  for (const { route, accept } of served) {
    const [verb = "", template = ""] = route.split(" ");
    const method = verb.toLowerCase();
    const found = pathOf(paths, method, template);
    if (found === undefined) throw new Error(`no operation of the description is ${route}`);
    const key = `${verb} ${found}`;
    routes[route] = key;
    const operation = (paths[found] as Json)[method] as Json;
    const answered = Object.entries(isObject(operation.responses) ? operation.responses : {})
      .filter(([status]) => /^2\d\d$/.test(status))
      .sort(([one], [other]) => Number(one) - Number(other))[0];
    if (answered === undefined) throw new Error(`${key} has no success answer`);
    const response = deref(answered[1], responses);
    const content = isObject(response) && isObject(response.content) ? response.content : {};
    const media = accept ?? JSON_MEDIA;
    const body = content[media];
    const schema = isObject(body) ? body.schema : undefined;
    reach(schema);
    operations[key] = {
      operation_id: String(operation.operationId),
      status: Number(answered[0]),
      media,
      ...(schema === undefined ? {} : { schema }),
    };
  }

  // A schema holds something required where it requires a field, or a part of it does: read to a
  // fixed point, since schemas name each other in cycles.
  const holds = new Map<string, boolean>([...reached.keys()].map((name) => [name, false]));
  const holding = (schema: unknown): boolean => {
    if (!isObject(schema)) return false;
    if (typeof schema.$ref === "string")
      return holds.get(schema.$ref.split("/").pop() ?? "") ?? false;
    if ("items" in schema) return holding(schema.items);
    if (Array.isArray(schema.allOf)) return schema.allOf.some(holding);
    const variants = Array.isArray(schema.oneOf)
      ? schema.oneOf
      : Array.isArray(schema.anyOf)
        ? schema.anyOf
        : undefined;
    if (variants !== undefined) return variants.every(holding);
    return (
      (Array.isArray(schema.required) && schema.required.length > 0) ||
      Object.values(isObject(schema.properties) ? schema.properties : {}).some(holding)
    );
  };
  for (let changed = true; changed; ) {
    changed = false;
    for (const [name, body] of reached)
      if (!holds.get(name) && holding(body)) {
        holds.set(name, true);
        changed = true;
      }
  }

  // Each schema as `absentFields` reads it, in the order the tester's reading takes its parts:
  // an array's items, then an `allOf`, then a `oneOf` or an `anyOf`, then an object.
  const reduce = (schema: unknown): Required => {
    if (!isObject(schema) || !holding(schema)) return {};
    if (typeof schema.$ref === "string") return { ref: schema.$ref.split("/").pop() ?? "" };
    if ("items" in schema) return { items: reduce(schema.items) };
    if (Array.isArray(schema.allOf)) return { allOf: schema.allOf.filter(holding).map(reduce) };
    if (Array.isArray(schema.oneOf)) return { oneOf: schema.oneOf.map(reduce) };
    if (Array.isArray(schema.anyOf)) return { anyOf: schema.anyOf.map(reduce) };
    const required = Array.isArray(schema.required) ? schema.required.map(String) : [];
    const properties = Object.entries(isObject(schema.properties) ? schema.properties : {})
      .filter(([, below]) => holding(below))
      .map(([field, below]) => [field, reduce(below)] as const);
    return {
      ...(required.length > 0 ? { required } : {}),
      ...(properties.length > 0 ? { properties: Object.fromEntries(properties) } : {}),
    };
  };
  const sorted = <T>(record: Record<string, T>) =>
    Object.fromEntries(Object.entries(record).sort(([one], [other]) => (one < other ? -1 : 1)));
  return Extract.parse({
    source: "github/rest-api-description",
    commit: DESCRIPTION_COMMIT,
    file: DESCRIPTION_FILE,
    routes: sorted(routes),
    operations: sorted(
      Object.fromEntries(
        Object.entries(operations).map(([key, { schema, ...rest }]) => [
          key,
          { ...rest, ...(schema === undefined ? {} : { schema: reduce(schema) }) },
        ])
      )
    ),
    schemas: sorted(
      Object.fromEntries(
        [...reached].filter(([name]) => holds.get(name)).map(([name, body]) => [name, reduce(body)])
      )
    ),
  });
}

if (import.meta.main) {
  const [file, ...rest] = Bun.argv.slice(2);
  if (file === undefined || rest.length > 0) {
    console.error("usage: bun rest-required.ts <api.github.com.json>");
    process.exit(2);
  }
  const description: unknown = JSON.parse(readFileSync(file, "utf8"));
  if (!isObject(description)) throw new Error(`${file} is no JSON object`);
  writeFileSync(REST_REQUIRED, `${JSON.stringify(derive(description), null, 2)}\n`);
  const formatted = Bun.spawnSync(["bunx", "biome", "format", "--write", REST_REQUIRED], {
    stdout: "inherit",
    stderr: "inherit",
  });
  process.exit(formatted.exitCode);
}
