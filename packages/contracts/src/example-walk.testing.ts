import {
  closedValues,
  type JsonKind,
  jsonKinds,
  objectSchema,
  objectShape,
  schemaNode,
  takes,
} from "./schema-inspection.testing";

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
 * - **Closed sets** (`closedSets`). Each place a walked schema reads a closed set of values at -
 *   an enum, a literal or a union of them, through its wrappers, a union member's no example
 *   takes included - must be one the test's list names, and each place the list names must read
 *   a closed set alone and refuse a value outside it. So a set that is opened - turned into a
 *   string, or joined with one, or given a fallback that takes any value - fails here by its
 *   place, and so does a new closed set until the list names it.
 * - **Forms** (`widenings`). Each key of every object a walk reads, each element of a list it
 *   reads and each value of a map must be refused by its schema in every form no example holds
 *   there - a key absent, a value null, a value of each other kind of JSON (a string, a number, a
 *   boolean, an object, a list), or any value at all - unless the test's table names the form
 *   with the reason the schema takes it. So a schema that starts taking a form its writer never
 *   writes - a required key made optional, a value made nullable, a number that takes a string, a
 *   list whose element takes null - fails here, whichever schema it is: an envelope's, an
 *   event's, a payload's or a tolerant reader's.
 *
 * Every exception is by the name of the object it is for (`labels`, or the place the walk reaches
 * an unnamed object at), so each name must be one schema's: two schemas the walk reads under one
 * name, which an exception for one would excuse in the other, fail too.
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
   *  the walk reaches it at. A name is one schema's. */
  readonly labels: ReadonlyMap<unknown, string>;
  /** What the test calls its table of `absent` entries, as the problems name it. */
  readonly absentTable: string;
}

/** How a walk reads its value: `tolerant` for a reader's schema, which keeps the keys it declares
 *  and passes the others over. */
export interface WalkMode {
  readonly tolerant: boolean;
}

/** A form a value takes at a place: the key left out (`absent`), or a value of one kind of JSON;
 *  `any` is a schema's taking a value of every kind. */
export type Form = "absent" | JsonKind;

/** A test's table of the forms its schemas take that no example holds, by the place
 *  (`<object>.<key>`, `<place>[]` for a list's element, `<place>.*` for a map's value), then by
 *  the form, each with the reason the schema takes it. */
export type Wider = Readonly<Record<string, Readonly<Partial<Record<Form, string>>>>>;

/** The forms of a value besides `absent` that `widenings` reads, `any` apart: each kind of JSON. */
const KINDS: readonly Form[] = ["null", "string", "number", "boolean", "object", "array"];

/** One closed set of values a schema declares: its values, the places it is read at, and the
 *  values the examples hold there, each as its JSON. */
interface ValueSet {
  readonly values: readonly unknown[];
  readonly places: Set<string>;
  readonly held: Set<string>;
}

/** What the walks show of one place's forms: each its schema takes, and each an example holds. */
interface PlaceForms {
  readonly taken: Set<Form>;
  readonly held: Set<Form>;
}

/** What the walks show of the set of values one place reads: its closed set's values, or
 *  undefined where the schema reads an open set there, and each value outside that set the
 *  schema took, as its JSON. */
interface PlaceSet {
  readonly values: readonly unknown[] | undefined;
  readonly outside: Set<string>;
}

function isObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

/** The kind of JSON value a value is. */
function kindOf(value: unknown): JsonKind {
  if (value === null) return "null";
  if (Array.isArray(value)) return "array";
  const kind = typeof value;
  return kind === "string" || kind === "number" || kind === "boolean" ? kind : "object";
}

/** One value of each kind a closed set holds that is not in the set: a string and a number
 *  beside its own, and the boolean it does not hold. */
function outsideValues(values: readonly unknown[]): readonly unknown[] {
  const outside: unknown[] = [];
  if (values.some((value) => typeof value === "string")) {
    let text = "outside the set";
    while (values.includes(text)) text += "+";
    outside.push(text);
  }
  const numbers = values.filter((value): value is number => typeof value === "number");
  if (numbers.length > 0) outside.push(Math.max(...numbers) + 1);
  if (values.some((value) => typeof value === "boolean")) {
    outside.push(...[true, false].filter((value) => !values.includes(value)));
  }
  return outside;
}

/** How a problem names a form a schema takes. */
function described(form: Form): string {
  switch (form) {
    case "absent":
    case "null":
      return `it ${form}`;
    case "any":
      return "any value";
    case "object":
    case "array":
      return `an ${form}`;
    default:
      return `a ${form}`;
  }
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
 * an example with the schema it is read by; once every value is walked, `staleExceptions`,
 * `closedSets` and `widenings`, and for a test that asks for them `unheldValues`, add what only
 * every walk together can show.
 */
export class ExampleWalk {
  /** Each problem the walks found, in the order they found it. */
  readonly problems: string[] = [];
  private readonly declared = new Map<string, Set<string>>();
  private readonly excused = new Map<string, Set<string>>();
  private readonly values = new Map<unknown, ValueSet>();
  private readonly forms = new Map<string, PlaceForms>();
  private readonly sets = new Map<string, PlaceSet>();
  /** The schema each name stands for, as the walks met it, and each name met for two. */
  private readonly named = new Map<string, unknown>();
  private readonly namedTwice = new Set<string>();

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
          this.inside(node.element, value, `${name}[]`, `${where}[]`);
          value.forEach((item, index) => {
            this.walk(node.element, item, example, `${path}[${index}]`, `${name}[]`, mode);
          });
        }
        return;
      case "record":
        if (isObject(value)) {
          this.inside(node.value, Object.values(value), `${name}.*`, `${where}.*`);
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
    const known = this.named.get(label);
    if (known === undefined) {
      this.named.set(label, schema);
    } else if (known !== schema && !this.namedTwice.has(label)) {
      this.namedTwice.add(label);
      this.problems.push(
        `two schemas are read as the ${label}, so an exception for one would excuse the other: give each a name of its own`
      );
    }
    const declared = this.declared.get(label) ?? new Set<string>();
    this.declared.set(label, declared);
    const never = this.rules.never[label] ?? {};
    for (const [key, field] of Object.entries(shape)) {
      declared.add(key);
      const at = `${path}.${key}`;
      const written = never[key] === undefined;
      if (written) this.form(schema, field, value, `${label}.${key}`, key, `${example.name} ${at}`);
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
   *  its object's keys read, so a value of a member no example holds is not passed over, and a
   *  closed set of it is read as `closedSets` reads every other. */
  private declareValues(option: unknown, name: string): void {
    const own = valueSet(option, name);
    if (own !== undefined) {
      this.hold(own, name, undefined);
      this.readSet(option, name, name, (outside) => takes(option, outside));
      return;
    }
    const shape = objectShape(option, name);
    if (shape === undefined) return;
    const label = this.rules.labels.get(objectSchema(option, name)) ?? name;
    for (const [key, field] of Object.entries(shape)) {
      if (this.rules.never[label]?.[key] !== undefined) continue;
      const place = `${label}.${key}`;
      const set = valueSet(field, place);
      if (set !== undefined) this.hold(set, place, undefined);
      this.readSet(field, place, place, (outside) => takes(field, outside));
    }
  }

  /** The forms one place's record holds. */
  private placeForms(place: string): PlaceForms {
    const forms = this.forms.get(place) ?? { taken: new Set<Form>(), held: new Set<Form>() };
    this.forms.set(place, forms);
    return forms;
  }

  /** Records the set of values `field` reads at `place`, and each value outside a closed set of
   *  it that `takesOutside` says the schema reading it there takes. */
  private readSet(
    field: unknown,
    place: string,
    where: string,
    takesOutside: (outside: unknown) => boolean
  ): void {
    const values = closedValues(field, where);
    const entry = this.sets.get(place) ?? { values, outside: new Set<string>() };
    this.sets.set(place, entry);
    for (const outside of values === undefined ? [] : outsideValues(values)) {
      if (takesOutside(outside)) entry.outside.add(JSON.stringify(outside));
    }
  }

  /** Records what one object shows of its key's forms - the form it holds the key in, and
   *  whether its schema would take the object with the key absent or null - and each kind of
   *  JSON the key's own schema takes besides null, with the set of values it reads. */
  private form(
    schema: unknown,
    field: unknown,
    value: Readonly<Record<string, unknown>>,
    place: string,
    key: string,
    where: string
  ): void {
    const forms = this.placeForms(place);
    for (const kind of jsonKinds(field, where)) {
      if (kind !== "null") forms.taken.add(kind);
    }
    this.readSet(
      field,
      place,
      where,
      (outside) => key in value && takes(schema, { ...value, [key]: outside })
    );
    if (!(key in value)) {
      forms.held.add("absent");
      return;
    }
    const without = Object.fromEntries(Object.entries(value).filter(([held]) => held !== key));
    if (takes(schema, without)) forms.taken.add("absent");
    forms.held.add(kindOf(value[key]));
    if (value[key] !== null && takes(schema, { ...value, [key]: null })) {
      forms.taken.add("null");
    }
  }

  /** Records what a list's elements or a map's values show at `place`: each kind of JSON their
   *  schema `element` takes, null among them, each kind they hold, and the set of values it
   *  reads. */
  private inside(element: unknown, items: readonly unknown[], place: string, where: string): void {
    const forms = this.placeForms(place);
    for (const kind of jsonKinds(element, where)) forms.taken.add(kind);
    for (const item of items) forms.held.add(kindOf(item));
    this.readSet(element, place, where, (outside) => takes(element, outside));
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
   * Adds a problem for each place a walk read a closed set of values at that `closed` does not
   * name; and for each place `closed` names that no walk read, where the schema reads an open set
   * of values (a string, a closed set joined with an open one), or where the schema reading it
   * took a value outside its set. `closed` is by place (`<object>.<key>`, `<place>[]`, `<place>.*`).
   */
  closedSets(closed: readonly string[]): void {
    const listed = new Set(closed);
    for (const [place, set] of [...this.sets].sort(([a], [b]) => a.localeCompare(b))) {
      if (!listed.has(place)) {
        if (set.values !== undefined) {
          this.problems.push(
            `${place} reads the closed set ${JSON.stringify(set.values)}, which CLOSED does not name`
          );
        }
        continue;
      }
      if (set.values === undefined) {
        this.problems.push(`CLOSED names ${place}, where its schema reads an open set of values`);
        continue;
      }
      for (const outside of [...set.outside].sort()) {
        this.problems.push(
          `the schema of ${place} takes ${outside}, a value outside its closed set ${JSON.stringify(set.values)}`
        );
      }
    }
    for (const place of closed) {
      if (!this.sets.has(place)) this.problems.push(`CLOSED names ${place}, which no walk read`);
    }
  }

  /**
   * Adds a problem for each form - a key absent, a value null, a value of another kind of JSON,
   * any value - that the schema of a place some walk read takes, that no example holds, and that
   * `wider` gives no reason for; and for each `wider` entry for a form its schema refuses, an
   * example holds, or no walk read. A schema that takes any value at a place is read as that one
   * form, `any`, there.
   */
  widenings(wider: Wider): void {
    const used = new Set<string>();
    for (const [place, forms] of [...this.forms].sort(([a], [b]) => a.localeCompare(b))) {
      const kinds: readonly Form[] = forms.taken.has("any") ? ["any"] : KINDS;
      for (const form of ["absent", ...kinds] as const) {
        const taken = forms.taken.has(form);
        const held = forms.held.has(form);
        const reason = wider[place]?.[form];
        if (reason === undefined) {
          if (taken && !held) {
            this.problems.push(
              `the schema of ${place} takes ${described(form)}, a form no example holds, and WIDER gives no reason it may`
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
