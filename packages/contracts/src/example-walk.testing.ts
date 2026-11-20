import { objectSchema, objectShape, schemaNode, takes } from "./schema-inspection.testing";

/**
 * How the Dispatch contract tests hold a shared example file to the schemas that read it: the one
 * walk that `dispatch-issue-events.test.ts` (the issue event envelopes Dispatch's outbox
 * publishes) and `dispatch-inbound-parity.test.ts` (the targeted-delivery fixture) share. Each
 * test keeps its own schemas, which of them it walks where, and its exception tables with their
 * reasons; this module holds the rule. It is used by tests alone, and nothing of the package
 * exports it.
 *
 * - **Keys.** Each object of an example must hold every key its schema declares, except a key
 *   `never` names for that object (one the writer never writes there) or the example's `absent`
 *   names for that key's path (one that write leaves out), and must hold no key `never` names.
 *   An object a writer's strict schema reads must hold no key that schema does not declare. An
 *   exception no walk used is stale, and fails too (`staleExceptions`).
 * - **Values** (`unheldValues`). Each closed set of values a walked schema declares - an enum, a
 *   literal, a union of them, a boolean - must have each of its values held by some example,
 *   unless the test's table names the value at one of the places the set is read, with the
 *   reason. The sets of the union members no example takes count too. So a schema that drops a
 *   value fails to parse the example that holds it, and one that gains a value fails here.
 * - **Forms** (`widenings`). Each key of an object a walk reads with `forms` must be refused
 *   absent, and refused null, by its schema, unless some example holds it in that form or the
 *   test's table names the form with the reason the schema takes it. So a schema that starts
 *   taking a form its writer never writes - a required key made optional, a value made nullable
 *   - fails here.
 */

/** One example a walk reads: its name in the problems, and each key a schema declares that the
 *  example leaves out, by the key's path, with the reason. */
export interface WalkedExample {
  readonly name: string;
  readonly absent: Readonly<Record<string, string>>;
}

/** The rules of one test's walks. */
export interface WalkRules {
  /** Each key a schema declares that its writer never writes, by the name of the object that
   *  declares it, with the reason. */
  readonly never: Readonly<Record<string, Readonly<Record<string, string>>>>;
  /** The name each object schema is known by; an object not named here is named by the place
   *  the walk reaches it at. */
  readonly labels: ReadonlyMap<unknown, string>;
  /** What the test calls its table of `absent` entries, as the problems name it. */
  readonly absentTable: string;
}

/** How a walk reads its value: `tolerant` for a reader's schema, which keeps the keys it declares
 *  and passes the others over, and `forms` to hold each key's empty forms (`widenings`). */
export interface WalkMode {
  readonly tolerant: boolean;
  readonly forms: boolean;
}

/** One closed set of values a schema declares: its values, the places it is read at, and the
 *  values the examples hold there, each as its JSON. */
interface ValueSet {
  readonly values: readonly unknown[];
  readonly places: Set<string>;
  readonly held: Set<string>;
}

/** What the examples show of one key's two empty forms: whether its schema takes each, and
 *  whether an example holds each. */
interface KeyForms {
  takesAbsent: boolean;
  takesNull: boolean;
  holdsAbsent: boolean;
  holdsNull: boolean;
}

function isObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

/** The closed set of values a schema reads a value as, through its wrappers, and the schema that
 *  declares it: an enum's or a literal's values, `true` and `false` for a boolean, and every
 *  member's for a union of such sets. Undefined for any other schema. */
function valueSet(
  schema: unknown,
  where: string
): { readonly schema: unknown; readonly values: readonly unknown[] } | undefined {
  const node = schemaNode(schema, where);
  switch (node.kind) {
    case "wrapper":
      return valueSet(node.inner, where);
    case "leaf":
      if (node.closed) return { schema, values: node.values };
      return node.json.length === 1 && node.json[0] === "boolean"
        ? { schema, values: [true, false] }
        : undefined;
    case "union": {
      const sets = node.options.map((option) => valueSet(option, where));
      if (sets.length === 0) return undefined;
      const values: unknown[] = [];
      for (const set of sets) {
        if (set === undefined) return undefined;
        values.push(...set.values);
      }
      return { schema, values };
    }
    default:
      return undefined;
  }
}

/**
 * The walks of one test over its examples, and the problems they found. `walk` reads one value of
 * an example with the schema it is read by; once every value is walked, `staleExceptions`, and
 * for a test that asks for them `unheldValues` and `widenings`, add what only every walk together
 * can show.
 */
export class ExampleWalk {
  /** Each problem the walks found, in the order they found it. */
  readonly problems: string[] = [];
  private readonly declared = new Map<string, Set<string>>();
  private readonly excused = new Map<string, Set<string>>();
  private readonly values = new Map<unknown, ValueSet>();
  private readonly forms = new Map<string, KeyForms>();

  constructor(private readonly rules: WalkRules) {}

  /**
   * Walks one value of an example at `path` (its place in the example) with the schema it is read
   * by, `name` naming the object there when `labels` does not. A union is read by the member that
   * takes the value, as a reader reads it, and a null holds no key to walk (the schema's own parse
   * took it). A schema kind the inspection does not read is an error, so no part of an example
   * goes unread.
   */
  walk(
    schema: unknown,
    value: unknown,
    example: WalkedExample,
    path: string,
    name: string,
    mode: WalkMode
  ): void {
    const where = `${example.name} ${path}`;
    const set = valueSet(schema, where);
    if (set !== undefined) {
      this.hold(set, name, value);
      return;
    }
    if (value === null) return;
    const node = schemaNode(schema, where);
    switch (node.kind) {
      case "wrapper":
        this.walk(node.inner, value, example, path, name, mode);
        return;
      case "array":
        if (Array.isArray(value)) {
          value.forEach((item, index) => {
            this.walk(node.element, item, example, `${path}[${index}]`, `${name}[]`, mode);
          });
        }
        return;
      case "record":
        if (isObject(value)) {
          for (const [key, entry] of Object.entries(value)) {
            this.walk(node.value, entry, example, `${path}.${key}`, `${name}.*`, mode);
          }
        }
        return;
      case "union": {
        const member = node.options.find((option) => takes(option, value));
        if (member === undefined) {
          this.problems.push(`${example.name}'s ${path} is taken by no member of its union`);
          return;
        }
        for (const option of node.options) {
          if (option !== member) this.declareValues(option, name);
        }
        this.walk(member, value, example, path, name, mode);
        return;
      }
      case "intersection":
        // Each part would read the object's keys as its own, so neither part's strictness says
        // which keys the object may hold: the walk does not read one, and says so.
        throw new Error(
          `${example.name}'s ${path} is read by an intersection, which this walk does not read`
        );
      case "leaf":
        return;
      case "object":
        this.object(schema, node.shape, value, example, path, name, mode);
    }
  }

  private object(
    schema: unknown,
    shape: Readonly<Record<string, unknown>>,
    value: unknown,
    example: WalkedExample,
    path: string,
    name: string,
    mode: WalkMode
  ): void {
    if (!isObject(value)) return;
    const label = this.rules.labels.get(schema) ?? name;
    const declared = this.declared.get(label) ?? new Set<string>();
    this.declared.set(label, declared);
    const never = this.rules.never[label] ?? {};
    for (const [key, field] of Object.entries(shape)) {
      declared.add(key);
      const at = `${path}.${key}`;
      const written = never[key] === undefined;
      if (mode.forms && written) this.form(schema, value, `${label}.${key}`, key);
      if (key in value) {
        if (!written) {
          this.problems.push(
            `${example.name}'s ${at} is the ${label}'s "${key}", which NEVER says is never written`
          );
        }
        this.walk(field, value[key], example, at, `${label}.${key}`, mode);
        continue;
      }
      if (written) {
        const set = valueSet(field, `${example.name} ${at}`);
        if (set !== undefined) this.hold(set, `${label}.${key}`, undefined);
      }
      if (example.absent[at] !== undefined) {
        const excused = this.excused.get(example.name) ?? new Set<string>();
        this.excused.set(example.name, excused);
        excused.add(at);
      } else if (written) {
        this.problems.push(
          `${example.name} holds no ${at}, which the ${label} declares and neither NEVER nor ${this.rules.absentTable} names`
        );
      }
    }
    if (!mode.tolerant) {
      for (const key of Object.keys(value)) {
        if (!(key in shape)) {
          this.problems.push(
            `${example.name}'s ${path} holds "${key}", which the ${label} does not declare`
          );
        }
      }
    }
  }

  /** Records that `place` reads `set`, and holds `value` there when it is a value. */
  private hold(
    set: { readonly schema: unknown; readonly values: readonly unknown[] },
    place: string,
    value: unknown
  ): void {
    const entry = this.values.get(set.schema) ?? {
      values: set.values,
      places: new Set<string>(),
      held: new Set<string>(),
    };
    this.values.set(set.schema, entry);
    entry.places.add(place);
    if (value !== null && value !== undefined) entry.held.add(JSON.stringify(value));
  }

  /** Records the value sets of a union member no example's value took: its own set, or each set
   *  its object's keys read, so a value of a member no example holds is not passed over. */
  private declareValues(option: unknown, name: string): void {
    const own = valueSet(option, name);
    if (own !== undefined) {
      this.hold(own, name, undefined);
      return;
    }
    const shape = objectShape(option, name);
    if (shape === undefined) return;
    const label = this.rules.labels.get(objectSchema(option, name)) ?? name;
    for (const [key, field] of Object.entries(shape)) {
      if (this.rules.never[label]?.[key] !== undefined) continue;
      const set = valueSet(field, `${label}.${key}`);
      if (set !== undefined) this.hold(set, `${label}.${key}`, undefined);
    }
  }

  /** Records what one object shows of `key`'s empty forms: the form it holds the key in, or, for
   *  a key it holds, whether its schema would take the object with the key absent or null. */
  private form(
    schema: unknown,
    value: Readonly<Record<string, unknown>>,
    place: string,
    key: string
  ): void {
    const forms = this.forms.get(place) ?? {
      takesAbsent: false,
      takesNull: false,
      holdsAbsent: false,
      holdsNull: false,
    };
    this.forms.set(place, forms);
    if (!(key in value)) {
      forms.holdsAbsent = true;
      return;
    }
    const without = Object.fromEntries(Object.entries(value).filter(([held]) => held !== key));
    if (takes(schema, without)) forms.takesAbsent = true;
    if (value[key] === null) {
      forms.holdsNull = true;
    } else if (takes(schema, { ...value, [key]: null })) {
      forms.takesNull = true;
    }
  }

  /** Adds a problem for each exception no walk used: an example's `absent` entry for a key the
   *  example holds or a path no schema reaches, and a `never` entry for a key no schema these
   *  examples are read by declares. */
  staleExceptions(examples: readonly WalkedExample[]): void {
    for (const example of examples) {
      const excused = this.excused.get(example.name);
      for (const at of Object.keys(example.absent)) {
        if (!excused?.has(at)) {
          this.problems.push(
            `${this.rules.absentTable} names ${example.name}'s ${at}, which the example holds or no schema it is read by declares there`
          );
        }
      }
    }
    for (const [label, keys] of Object.entries(this.rules.never)) {
      for (const key of Object.keys(keys)) {
        if (!this.declared.get(label)?.has(key)) {
          this.problems.push(
            `NEVER names the ${label}'s "${key}", which no schema these examples are read by declares`
          );
        }
      }
    }
  }

  /**
   * Adds a problem for each value of a closed set that no example holds and `unheld` gives no
   * reason for, and for each `unheld` entry that names a value an example holds, or a value no
   * set read at that place declares. `unheld` is by place (`<object>.<key>`), then by the value's
   * JSON (`"system"`, `true`, `3`).
   */
  unheldValues(unheld: Readonly<Record<string, Readonly<Record<string, string>>>>): void {
    const used = new Set<string>();
    for (const set of this.values.values()) {
      const places = [...set.places].sort();
      for (const value of set.values) {
        const shown = JSON.stringify(value);
        const named = places.filter((place) => unheld[place]?.[shown] !== undefined);
        for (const place of named) used.add(`${place} ${shown}`);
        if (set.held.has(shown)) {
          for (const place of named) {
            this.problems.push(`UNHELD names ${shown} at ${place}, which an example holds`);
          }
        } else if (named.length === 0) {
          this.problems.push(
            `no example holds ${shown} at ${places.join(", ")}, a value its schema declares, and UNHELD gives no reason`
          );
        }
      }
    }
    for (const [place, values] of Object.entries(unheld)) {
      for (const shown of Object.keys(values)) {
        if (!used.has(`${place} ${shown}`)) {
          this.problems.push(`UNHELD names ${shown} at ${place}, which no schema declares there`);
        }
      }
    }
  }

  /**
   * Adds a problem for each empty form - a key absent, a key null - that the schema of a key some
   * walk read with `forms` takes, that no example holds, and that `wider` gives no reason for; and
   * for each `wider` entry for a form its schema refuses, an example holds, or no walk read.
   * `wider` is by place (`<object>.<key>`), then by the form.
   */
  widenings(wider: Readonly<Record<string, Readonly<{ absent?: string; null?: string }>>>): void {
    const used = new Set<string>();
    for (const [place, forms] of [...this.forms].sort(([a], [b]) => a.localeCompare(b))) {
      for (const [form, taken, held] of [
        ["absent", forms.takesAbsent, forms.holdsAbsent],
        ["null", forms.takesNull, forms.holdsNull],
      ] as const) {
        const reason = wider[place]?.[form];
        if (reason === undefined) {
          if (taken && !held) {
            this.problems.push(
              `the schema of ${place} takes it ${form}, a form no example holds, and WIDER gives no reason it may`
            );
          }
          continue;
        }
        used.add(`${place} ${form}`);
        if (held) this.problems.push(`WIDER names ${place} ${form}, which an example holds`);
        else if (!taken)
          this.problems.push(`WIDER names ${place} ${form}, which its schema refuses`);
      }
    }
    for (const [place, forms] of Object.entries(wider)) {
      for (const form of Object.keys(forms)) {
        if (!used.has(`${place} ${form}`)) {
          this.problems.push(`WIDER names ${place} ${form}, which no walk read`);
        }
      }
    }
  }
}
