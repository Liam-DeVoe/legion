import { expect, test } from "bun:test";
import { z } from "zod";
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
import { objectSchema, objectShape, schemaNode, takes } from "./schema-inspection.testing";

/**
 * The issue event envelopes Dispatch's outbox publishes, shared with the Go writer
 * (`packages/envoy/internal/dispatch/outbox`, whose `TestTheBuiltIssueEnvelopesAreTheSharedFixtures`
 * requires the envelope it builds to equal each one, and the five together to hold every field it
 * writes, set and in the form it writes when it holds nothing) and with the Legion daemon's intake
 * test, which requires every field the daemon reads.
 */
const EXAMPLES = [
  "issue-updated-every-field.json",
  "issue-claimed-every-field.json",
  "issue-updated-no-field-moved.json",
  "issue-updated-null-fields.json",
  "issue-released-null-claim.json",
] as const;

type Example = (typeof EXAMPLES)[number];

/** The schema each event type's payload is written by (`DispatchEvent`), and for `issue.*` the
 *  tolerant schema a reader of the bus reads it with; a claim event's reader reads the written one. */
const PAYLOADS: Record<string, { written: unknown; read?: typeof IssueEventPayloadSchema }> = {
  "issue.updated": { written: WrittenIssueEventPayloadSchema, read: IssueEventPayloadSchema },
  "issue.claimed": { written: IssueClaimEventPayloadSchema },
  "issue.released": { written: IssueReleasedEventPayloadSchema },
};

/** Each key a schema declares that Dispatch never writes on these events, by the object that
 *  declares it, with the reason: the Go test's `neverWritten` says the same of the Go types. An
 *  example that carries one fails. */
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

/** The token reasons of an actor's absent keys (api.brokerTokenActor): a broker-issued token names
 *  a person or a service, never both, and a project only beside a person. */
const SERVICE_CALLER = "its token names a service, so no person and no project";
const PERSON_CALLER = "its token names a person, so no service";
const PERSON_ONLY_CALLER =
  "its token names a person and no single project, so no service and no project";
const NO_PLACE = "its requests name no place";

/**
 * Each key a schema declares that one example does not hold, by the example and the key's path in
 * it, with the reason that write leaves it out. Every other key each object of an example's
 * schemas declares, NEVER's apart, must be in that object: so a key removed from an example fails
 * here, and so does an entry here that names a key its example holds or a path no schema reaches.
 * The paths start at the `envelope`, its `event` and that event's `payload`.
 */
const ABSENT: Record<Example, Record<string, string>> = {
  "issue-updated-every-field.json": {
    "event.actor.owner": SERVICE_CALLER,
    "event.actor.project": SERVICE_CALLER,
    "event.payload.claim.actor.owner": SERVICE_CALLER,
    "event.payload.claim.actor.project": SERVICE_CALLER,
  },
  "issue-claimed-every-field.json": {
    "event.actor.service": PERSON_CALLER,
    "event.payload.claim.actor.service": PERSON_CALLER,
    "event.payload.previous_claim.actor.owner": SERVICE_CALLER,
    "event.payload.previous_claim.actor.project": SERVICE_CALLER,
  },
  "issue-updated-no-field-moved.json": {
    "event.actor.service": PERSON_CALLER,
    "event.payload.references_changed": "the write moved no issue's edges",
    "event.payload.thread_logins": "the write moved neither the status nor the assignee",
  },
  "issue-updated-null-fields.json": {
    "envelope.source_session": "a person wrote it, so the envelope has no session source",
    "event.actor.proven": "the person was named by the body, not proved by the credential",
    "event.payload.references_changed": "the write moved no issue's edges",
  },
  "issue-released-null-claim.json": {
    "event.actor.origin": NO_PLACE,
    "event.actor.service": PERSON_ONLY_CALLER,
    "event.actor.project": PERSON_ONLY_CALLER,
    "event.payload.previous_claim.actor.origin": NO_PLACE,
    "event.payload.previous_claim.actor.service": PERSON_ONLY_CALLER,
    "event.payload.previous_claim.actor.project": PERSON_ONLY_CALLER,
  },
};

/** The name each object of the schemas is known by in NEVER and in the problems; an object not
 *  named here (an `advances` entry) is named by where it is. The reading schema of an update's
 *  payload reads the object the written one writes, so it takes that object's name. */
const LABELS = new Map<unknown, string>([
  [objectSchema(EnvelopeSchema, "EnvelopeSchema"), "envelope"],
  [objectSchema(PublishedEventSchema, "PublishedEventSchema"), "event"],
  [
    objectSchema(WrittenIssueEventPayloadSchema, "WrittenIssueEventPayloadSchema"),
    "issue.updated payload",
  ],
  [objectSchema(IssueEventPayloadSchema, "IssueEventPayloadSchema"), "issue.updated payload"],
  [
    objectSchema(IssueClaimEventPayloadSchema, "IssueClaimEventPayloadSchema"),
    "issue.claimed payload",
  ],
  [
    objectSchema(IssueReleasedEventPayloadSchema, "IssueReleasedEventPayloadSchema"),
    "issue.released payload",
  ],
  [objectSchema(IssueClaimSchema, "IssueClaimSchema"), "claim"],
  [objectSchema(IssueComponentsSchema, "IssueComponentsSchema"), "components"],
  [objectSchema(ExternalLinkSchema, "ExternalLinkSchema"), "external link"],
  [objectSchema(ActorOriginSchema, "ActorOriginSchema"), "origin"],
  [objectSchema(ActorActSchema, "ActorActSchema"), "act"],
  [objectSchema(ChangedReferenceSchema, "ChangedReferenceSchema"), "changed reference"],
  ...ActorSchema.options.map((option): [unknown, string] => {
    const kind = objectShape(option, "ActorSchema")?.kind;
    const values = kind instanceof z.core.$ZodLiteral ? kind._zod.def.values : [];
    return [objectSchema(option, "ActorSchema"), `${String(values[0])} actor`];
  }),
]);

function isObject(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

/** What one walk of an example reads with: the example, its ABSENT entries and the ones a walk
 *  excused, every key each named object declares, and whether the schema is a tolerant reader,
 *  which keeps the keys it declares and passes the others over. */
interface Reading {
  readonly file: Example;
  readonly absent: Readonly<Record<string, string>>;
  readonly excused: Set<string>;
  readonly declared: Map<string, Set<string>>;
  readonly problems: string[];
  readonly tolerant: boolean;
}

/**
 * Walks one value of an example at `path` with the schema it is read by. Each object must hold
 * every key its schema declares, except one NEVER names for that object or ABSENT names for that
 * path, and must hold no key NEVER names; a written schema's object must hold no key its schema
 * does not declare. A union is read by the member that takes the value, which is how a reader
 * reads it, and a null holds no key to walk (the schema's own parse above took it). A schema kind
 * the inspection does not read is an error, so no part of an example goes unread.
 */
function walk(schema: unknown, value: unknown, path: string, name: string, reading: Reading): void {
  const node = schemaNode(schema, `${reading.file} ${path}`);
  if (value === null) return;
  switch (node.kind) {
    case "wrapper":
      walk(node.inner, value, path, name, reading);
      return;
    case "array":
      if (Array.isArray(value)) {
        value.forEach((item, index) => {
          walk(node.element, item, `${path}[${index}]`, `${name}[]`, reading);
        });
      }
      return;
    case "record":
      if (isObject(value)) {
        for (const [key, entry] of Object.entries(value)) {
          walk(node.value, entry, `${path}.${key}`, `${name}.*`, reading);
        }
      }
      return;
    case "union": {
      const member = node.options.find((option) => takes(option, value));
      if (member === undefined) {
        reading.problems.push(`${reading.file}'s ${path} is taken by no member of its union`);
      } else {
        walk(member, value, path, name, reading);
      }
      return;
    }
    case "intersection":
      // Each part would read the object's keys as its own, so neither part's strictness says
      // which keys the object may hold: the walk does not read one, and says so.
      throw new Error(
        `${reading.file}'s ${path} is read by an intersection, which this walk does not read`
      );
    case "leaf":
      return;
    case "object": {
      if (!isObject(value)) return;
      const label = LABELS.get(schema) ?? name;
      const declared = reading.declared.get(label) ?? new Set<string>();
      reading.declared.set(label, declared);
      for (const [key, field] of Object.entries(node.shape)) {
        declared.add(key);
        const at = `${path}.${key}`;
        const never = NEVER[label]?.[key] !== undefined;
        if (key in value) {
          if (never) {
            reading.problems.push(
              `${reading.file}'s ${at} is the ${label}'s "${key}", which NEVER says Dispatch never writes`
            );
          }
          walk(field, value[key], at, `${label}.${key}`, reading);
        } else if (reading.absent[at] !== undefined) {
          reading.excused.add(at);
        } else if (!never) {
          reading.problems.push(
            `${reading.file} holds no ${at}, which the ${label} declares and neither NEVER nor ABSENT names`
          );
        }
      }
      if (!reading.tolerant) {
        for (const key of Object.keys(value)) {
          if (!(key in node.shape)) {
            reading.problems.push(
              `${reading.file}'s ${path} holds "${key}", which the ${label} does not declare`
            );
          }
        }
      }
    }
  }
}

test("the issue events Dispatch publishes are envelopes, events and payloads these schemas read, every key declared", async () => {
  const declared = new Map<string, Set<string>>();
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
    if (event.type === "issue.claimed") {
      // model.IssueClaimEventPayload writes both with no omitempty tag, so readers must not
      // accept a retained claimed event that loses either one.
      for (const field of ["key", "reason"] as const) {
        for (const [form, value] of [
          ["absent", undefined],
          ["null", null],
        ] as const) {
          if (
            IssueClaimEventPayloadSchema.safeParse({ ...event.payload, [field]: value }).success
          ) {
            problems.push(
              `IssueClaimEventPayloadSchema takes ${file}'s payload with "${field}" ${form}, which Go never writes`
            );
          }
        }
      }
    }
    const reading = (tolerant: boolean): Reading => ({
      file,
      absent: ABSENT[file],
      excused,
      declared,
      problems,
      tolerant,
    });
    const excused = new Set<string>();
    walk(EnvelopeSchema, envelope, "envelope", "envelope", reading(false));
    walk(PublishedEventSchema, event, "event", "event", reading(false));
    walk(DispatchEventSchema, event, "event", "event", reading(true));
    walk(payload.written, event.payload, "event.payload", `${event.type} payload`, reading(false));
    if (payload.read !== undefined) {
      // The reading schema keeps each key it declares, as Dispatch wrote it, and each key it
      // declares is one the example holds, unless NEVER or ABSENT names it: so a reader of a key
      // Dispatch does not write fails here.
      const shape = objectShape(payload.read, "IssueEventPayloadSchema") ?? {};
      const kept = Object.fromEntries(
        Object.entries(event.payload).filter(([key]) => key in shape)
      );
      expect(payload.read.parse(event.payload)).toEqual(kept);
      walk(payload.read, event.payload, "event.payload", `${event.type} payload`, reading(true));
    }
    for (const at of Object.keys(ABSENT[file])) {
      if (!excused.has(at)) {
        problems.push(
          `ABSENT names ${file}'s ${at}, which the example holds or no schema these examples are read by declares there`
        );
      }
    }
  }
  for (const [label, keys] of Object.entries(NEVER)) {
    for (const key of Object.keys(keys)) {
      if (!declared.get(label)?.has(key)) {
        problems.push(
          `NEVER names the ${label}'s "${key}", which no schema these examples are read by declares`
        );
      }
    }
  }
  expect(problems).toEqual([]);
});
