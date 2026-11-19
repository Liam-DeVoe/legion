import { z } from "zod";

/**
 * How the Dispatch contract tests read a zod schema: the one reading of zod's definitions that
 * `dispatch-api-spellings.test.ts` (which field names, which absences and which value types a
 * schema declares) and `dispatch-issue-events.test.ts` (which keys each object of a shared example
 * must hold) share. It is used by tests alone, and nothing of the package exports it.
 *
 * A schema is read as one node of these kinds, each with the schemas it holds:
 *
 * - `object`: its shape, by key;
 * - `array`: its element, and `record`: its value type;
 * - `union`: its options, a discriminated union's included;
 * - `intersection`: its two parts, each of which a value must satisfy;
 * - `wrapper`: the one schema it reads a value through - the inner type of `optional`,
 *   `nullable`, `readonly`, `default`, `prefault`, `nonoptional`, `catch`, `success` and
 *   `promise`, the schema a `lazy` answers, and of a `pipe` the input it parses (`in`), or the
 *   output when the input is a transform, as `z.preprocess` builds;
 * - `leaf`: a value of no inner schema, with the JSON kinds it takes (`json`: `string`,
 *   `number`, `boolean`, `null`, or `any` for a schema that takes every value; none for `never`,
 *   `undefined` and `void`), `closed` when it takes a closed set of values (an `enum` or a
 *   `literal`, whose kinds are their values').
 *
 * Any other kind (`tuple`, `map`, `set`, a bare `transform`, ...) is an error naming it and where
 * it is: a schema these tests cannot read is reported, never taken as read.
 */
export type SchemaNode =
  | { readonly kind: "object"; readonly shape: Readonly<Record<string, z.core.$ZodType>> }
  | { readonly kind: "array"; readonly element: z.core.$ZodType }
  | { readonly kind: "record"; readonly value: z.core.$ZodType }
  | { readonly kind: "union"; readonly options: readonly z.core.$ZodType[] }
  | { readonly kind: "intersection"; readonly parts: readonly z.core.$ZodType[] }
  | { readonly kind: "wrapper"; readonly inner: z.core.$ZodType }
  | { readonly kind: "leaf"; readonly closed: boolean; readonly json: readonly JsonKind[] };

/** A kind of JSON value, or `any` for a schema that takes every value. */
export type JsonKind = "string" | "number" | "boolean" | "null" | "object" | "array" | "any";

const WRAPPERS: Readonly<Record<string, true>> = {
  optional: true,
  nullable: true,
  readonly: true,
  default: true,
  prefault: true,
  nonoptional: true,
  catch: true,
  success: true,
  promise: true,
};

/** Each leaf kind of an open set of values, and the JSON kinds it takes: a `bigint`, a `date` and
 *  a `symbol` take no JSON value at all, and `custom` is a check whose value zod does not type. */
const LEAVES: Readonly<Record<string, readonly JsonKind[]>> = {
  any: ["any"],
  bigint: [],
  boolean: ["boolean"],
  custom: ["any"],
  date: [],
  nan: ["number"],
  never: [],
  null: ["null"],
  number: ["number"],
  string: ["string"],
  symbol: [],
  template_literal: ["string"],
  undefined: [],
  unknown: ["any"],
  void: [],
};

/** The JSON kind of one value an `enum` or a `literal` takes. */
function jsonKindOf(value: unknown): JsonKind {
  if (value === null) return "null";
  if (typeof value === "string" || typeof value === "number" || typeof value === "boolean") {
    return typeof value as JsonKind;
  }
  return "any";
}

/** The node a schema is, read at `where` (the place an error names). */
export function schemaNode(schema: unknown, where: string): SchemaNode {
  if (!(schema instanceof z.core.$ZodType)) {
    throw new Error(`${where} is not a zod schema`);
  }
  const def = schema._zod.def;
  if (def.type === "object" && schema instanceof z.core.$ZodObject) {
    return { kind: "object", shape: schema._zod.def.shape };
  }
  if (def.type === "array" && schema instanceof z.core.$ZodArray) {
    return { kind: "array", element: schema._zod.def.element };
  }
  if (def.type === "record" && schema instanceof z.core.$ZodRecord) {
    return { kind: "record", value: schema._zod.def.valueType };
  }
  if (def.type === "union" && schema instanceof z.core.$ZodUnion) {
    return { kind: "union", options: schema._zod.def.options };
  }
  if (def.type === "intersection" && schema instanceof z.core.$ZodIntersection) {
    return { kind: "intersection", parts: [schema._zod.def.left, schema._zod.def.right] };
  }
  if (def.type === "lazy" && schema instanceof z.core.$ZodLazy) {
    return { kind: "wrapper", inner: schema._zod.def.getter() };
  }
  if (def.type === "pipe" && schema instanceof z.core.$ZodPipe) {
    const input = schema._zod.def.in;
    return {
      kind: "wrapper",
      inner: input._zod.def.type === "transform" ? schema._zod.def.out : input,
    };
  }
  if (
    WRAPPERS[def.type] === true &&
    "innerType" in def &&
    def.innerType instanceof z.core.$ZodType
  ) {
    return { kind: "wrapper", inner: def.innerType };
  }
  if (def.type === "enum" && schema instanceof z.core.$ZodEnum) {
    const values = Object.values(schema._zod.def.entries);
    return { kind: "leaf", closed: true, json: [...new Set(values.map(jsonKindOf))] };
  }
  if (def.type === "literal" && schema instanceof z.core.$ZodLiteral) {
    const values: readonly unknown[] = schema._zod.def.values;
    return { kind: "leaf", closed: true, json: [...new Set(values.map(jsonKindOf))] };
  }
  const json = LEAVES[def.type];
  if (json !== undefined) return { kind: "leaf", closed: false, json };
  throw new Error(`${where} is a zod ${def.type} schema, which the contract tests do not read`);
}

/** The shape of the object a schema reads a value as, through its wrappers; undefined for a
 *  schema that is no object there (a union, an intersection, a list, a map or a leaf). */
export function objectShape(
  schema: unknown,
  where: string
): Readonly<Record<string, z.core.$ZodType>> | undefined {
  const node = schemaNode(schema, where);
  if (node.kind === "object") return node.shape;
  return node.kind === "wrapper" ? objectShape(node.inner, where) : undefined;
}

/** The object schema a schema reads a value as, through its wrappers: the schema itself where it
 *  is an object, else the object one of its wrappers holds, else undefined. */
export function objectSchema(schema: unknown, where: string): z.core.$ZodType | undefined {
  const node = schemaNode(schema, where);
  if (node.kind === "object" && schema instanceof z.core.$ZodType) return schema;
  return node.kind === "wrapper" ? objectSchema(node.inner, where) : undefined;
}

/** Whether a schema takes a value: zod's own parse of it succeeds. */
export function takes(schema: unknown, value: unknown): boolean {
  if (!(schema instanceof z.core.$ZodType)) return false;
  return z.safeParse(schema, value).success;
}
