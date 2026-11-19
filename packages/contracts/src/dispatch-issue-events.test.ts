import { expect, test } from "bun:test";
import {
  ActorActSchema,
  ActorOriginSchema,
  ActorSchema,
  ChangedReferenceSchema,
  DispatchEventSchema,
  ExternalLinkSchema,
  IssueClaimEventPayloadSchema,
  IssueClaimSchema,
  IssueComponentsSchema,
  IssueEventPayloadSchema,
  IssueReleasedEventPayloadSchema,
  PublishedEventSchema,
  WrittenIssueEventPayloadSchema,
} from "./dispatch-api";
import { EnvelopeSchema } from "./envelope";

/**
 * The issue event envelopes Dispatch's outbox publishes, shared with the Go writer
 * (`packages/envoy/internal/dispatch/outbox`, whose `TestTheBuiltIssueEnvelopesAreTheSharedFixtures`
 * requires the envelope it builds to equal each one, and the five together to hold every field it
 * writes) and with the Legion daemon's intake test, which requires every field the daemon reads.
 */
const EXAMPLES = [
  "issue-updated-every-field.json",
  "issue-claimed-every-field.json",
  "issue-updated-no-field-moved.json",
  "issue-updated-null-fields.json",
  "issue-released-null-claim.json",
] as const;

/** The schema each event type's payload is written by (`DispatchEvent`), and for `issue.*` the
 *  tolerant schema a reader of the bus reads it with; a claim event's reader reads the written one. */
const PAYLOADS: Record<string, { written: unknown; read?: typeof IssueEventPayloadSchema }> = {
  "issue.updated": { written: WrittenIssueEventPayloadSchema, read: IssueEventPayloadSchema },
  "issue.claimed": { written: IssueClaimEventPayloadSchema },
  "issue.released": { written: IssueReleasedEventPayloadSchema },
};

/** Each key a schema declares that Dispatch never writes on these events, by the object that
 *  declares it, with the reason: the Go test's `neverWritten` says the same of the Go types. An
 *  example that carries one fails, and so does a declared key that no example carries and that is
 *  not named here. */
const NEVER: Record<string, Record<string, string>> = {
  envelope: {
    expires_at: "the outbox's envelope() sets none",
    kept_at: "set on a role's kept copy (outbox/role.go), not the issue topic's copy these are",
    mention_dedupe_keys:
      "set on a role's kept copy (outbox/role.go), not the issue topic's copy these are",
    payload_ref: "the outbox's envelope() sets none",
    sender: "the outbox's envelope() sets none",
    in_reply_to: "set on an ask.answered, an ask.resolved, a comment and a message alone",
    supersedes: "the outbox's envelope() sets none",
    urgency: "set on an ask.* event alone",
    expects_reply: "the outbox's envelope() leaves it out of every event's envelope",
  },
  event: {
    trees: "the outbox publishes the event log's row, which holds no trees",
  },
  "issue.updated payload": {
    forced: "on an issue.created alone",
    references_changed_truncated:
      "an issue.updated names at most 41 issues: its parent and its blockers before and after the patch, 20 at most each, below the 100 of a kind that truncates",
    references_changed_truncated_kinds: "as references_changed_truncated",
  },
  "user actor": {
    act: "set by Dispatch's actingPerson alone, which no issue handler calls",
  },
  "changed reference": {
    issue_key: "an issue write names issue targets alone, which carry no address",
    artifact_id: "an issue write names issue targets alone, which carry no address",
    ref: "an issue write names issue targets alone, which carry no address",
  },
};

/** Each key a schema declares that Dispatch writes on some of these events and leaves out of
 *  others, with the reason it is absent. Some example still carries each. */
const SOMETIMES: Record<string, Record<string, string>> = {
  "issue.updated payload": {
    references_changed: "absent when the patch moved no issue's edges",
    thread_logins: "present only when the patch moved the status or the assignee",
  },
  "session actor": {
    origin: "absent when the request named no place",
    owner: "absent under a token that names a service",
    service: "absent under a token that names a person",
    project: "absent under a token that names no single project",
  },
};

/** A zod schema's definition, read through either spelling zod gives it. */
function defOf(schema: unknown): Record<string, unknown> | undefined {
  if (schema === null || typeof schema !== "object") return undefined;
  const held = "_def" in schema ? schema._def : "def" in schema ? schema.def : undefined;
  return held !== null && typeof held === "object" ? { ...held } : undefined;
}

/** The object schema a schema is built on, read through `.readonly()`, `.optional()` and
 *  `.nullable()`. */
function objectOf(schema: unknown): unknown {
  const def = defOf(schema);
  if (def === undefined) return undefined;
  if (def.type === "object") return schema;
  return def.innerType === undefined ? undefined : objectOf(def.innerType);
}

function shapeOf(object: unknown): Record<string, unknown> {
  const shape = defOf(object)?.shape;
  return shape !== null && typeof shape === "object" ? { ...shape } : {};
}

/** The name of each object these envelopes are read with, the one NEVER and SOMETIMES use; an
 *  object not named here (an `advances` entry) is named by where it is. */
const LABELS = new Map<unknown, string>([
  [objectOf(EnvelopeSchema), "envelope"],
  [objectOf(PublishedEventSchema), "event"],
  [objectOf(WrittenIssueEventPayloadSchema), "issue.updated payload"],
  [objectOf(IssueClaimEventPayloadSchema), "issue.claimed payload"],
  [objectOf(IssueReleasedEventPayloadSchema), "issue.released payload"],
  [objectOf(IssueClaimSchema), "claim"],
  [objectOf(IssueComponentsSchema), "components"],
  [objectOf(ExternalLinkSchema), "external link"],
  [objectOf(ActorOriginSchema), "origin"],
  [objectOf(ActorActSchema), "act"],
  [objectOf(ChangedReferenceSchema), "changed reference"],
  ...ActorSchema.options.map((option): [unknown, string] => {
    const kind = defOf(shapeOf(objectOf(option)).kind)?.values;
    return [objectOf(option), `${Array.isArray(kind) ? kind[0] : "unknown"} actor`];
  }),
]);

function isObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

/** Whether a schema takes a value. */
function takes(schema: unknown, value: unknown): boolean {
  if (schema === null || typeof schema !== "object" || !("safeParse" in schema)) return false;
  const parse = schema.safeParse;
  if (typeof parse !== "function") return false;
  const answer: unknown = parse.call(schema, value);
  return isObject(answer) && answer.success === true;
}

/** The keys each named object declares, and the ones the examples carry. */
type Coverage = Map<string, { declared: string[]; carried: Set<string> }>;

/**
 * Walks one value of an example with the schema it is read by. Each object must hold every key its
 * schema declares, except one NEVER or SOMETIMES names for that object, and no key its schema does
 * not declare; each key it holds is recorded as carried. A union is read by the member that takes
 * the value, which is how a reader reads it.
 */
function walk(
  schema: unknown,
  value: unknown,
  where: string,
  name: string,
  coverage: Coverage,
  problems: string[]
): void {
  const def = defOf(schema);
  if (def === undefined) return;
  switch (def.type) {
    case "readonly":
    case "optional":
    case "nullable":
      walk(def.innerType, value, where, name, coverage, problems);
      return;
    case "array":
      if (Array.isArray(value)) {
        value.forEach((item, index) => {
          walk(def.element, item, `${where}[${index}]`, `${name}[]`, coverage, problems);
        });
      }
      return;
    case "record":
      if (isObject(value)) {
        for (const [key, entry] of Object.entries(value)) {
          walk(def.valueType, entry, `${where}.${key}`, `${name}.*`, coverage, problems);
        }
      }
      return;
    case "union": {
      const options: unknown[] = Array.isArray(def.options) ? def.options : [];
      const member = options.find((option) => takes(option, value));
      if (member !== undefined) walk(member, value, where, name, coverage, problems);
      return;
    }
    case "object": {
      if (!isObject(value)) return;
      const label = LABELS.get(schema) ?? name;
      const shape = shapeOf(schema);
      const entry = coverage.get(label) ?? { declared: Object.keys(shape), carried: new Set() };
      coverage.set(label, entry);
      for (const [key, field] of Object.entries(shape)) {
        if (key in value) {
          entry.carried.add(key);
          walk(field, value[key], `${where}.${key}`, `${label}.${key}`, coverage, problems);
        } else if (NEVER[label]?.[key] === undefined && SOMETIMES[label]?.[key] === undefined) {
          problems.push(
            `${where} holds no "${key}", which the ${label} declares and every such example carries`
          );
        }
      }
      for (const key of Object.keys(value)) {
        if (!(key in shape))
          problems.push(`${where} holds "${key}", which the ${label} does not declare`);
      }
    }
  }
}

test("the issue events Dispatch publishes are envelopes, events and payloads these schemas read, every key declared", async () => {
  const coverage: Coverage = new Map();
  const problems: string[] = [];
  for (const file of EXAMPLES) {
    const path = new URL(`../../daemon/internal/intake/testdata/dispatch/${file}`, import.meta.url);
    const envelope = JSON.parse(await Bun.file(path).text());
    // The envelope schema keeps only what it declares, so an undeclared key is dropped here.
    expect(EnvelopeSchema.parse(envelope)).toEqual(envelope);
    const event = JSON.parse(envelope.payload);
    expect(PublishedEventSchema.parse(event)).toEqual(event);
    // A reader of the bus takes the frame by its tolerant schema, the payload by its type's own.
    expect(DispatchEventSchema.parse(event)).toMatchObject({
      issue_key: event.issue_key,
      type: event.type,
    });
    const payload = PAYLOADS[event.type];
    if (payload === undefined)
      throw new Error(`${file} is a ${event.type}, which PAYLOADS names no schema for`);
    const written = payload.written as { parse: (value: unknown) => unknown };
    expect(written.parse(event.payload)).toEqual(event.payload);
    if (payload.read !== undefined) {
      // It keeps the fields it reads, each as Dispatch wrote it.
      const read = Object.fromEntries(
        Object.keys(payload.read.shape).map((key) => [key, event.payload[key]])
      );
      expect(payload.read.parse(event.payload)).toEqual(read);
    }
    walk(EnvelopeSchema, envelope, file, "envelope", coverage, problems);
    walk(PublishedEventSchema, event, `${file} event`, "event", coverage, problems);
    walk(
      payload.written,
      event.payload,
      `${file} payload`,
      `${event.type} payload`,
      coverage,
      problems
    );
  }
  // Every key a schema declares is one some example carries, unless NEVER names it: so a schema
  // that grows a key Dispatch does not write fails here.
  for (const [label, { declared, carried }] of coverage) {
    for (const key of declared) {
      const never = NEVER[label]?.[key] !== undefined;
      if (never && carried.has(key)) {
        problems.push(
          `an example carries the ${label}'s "${key}", which NEVER says Dispatch never writes`
        );
      } else if (!never && !carried.has(key)) {
        problems.push(
          `the ${label} declares "${key}", which no example carries and NEVER does not name`
        );
      }
    }
  }
  for (const [list, entries] of [
    ["NEVER", NEVER],
    ["SOMETIMES", SOMETIMES],
  ] as const) {
    for (const [label, keys] of Object.entries(entries)) {
      for (const key of Object.keys(keys)) {
        if (!coverage.get(label)?.declared.includes(key)) {
          problems.push(
            `${list} names the ${label}'s "${key}", which no schema these examples are read by declares`
          );
        }
      }
    }
  }
  expect(problems).toEqual([]);
});
