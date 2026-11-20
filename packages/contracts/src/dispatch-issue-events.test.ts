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
import { ExampleWalk, type WalkedExample } from "./example-walk.testing";
import { objectSchema, objectShape } from "./schema-inspection.testing";

/**
 * The issue event envelopes Dispatch's outbox publishes, shared with the Go writer
 * (`packages/envoy/internal/dispatch/outbox`, whose `TestTheBuiltIssueEnvelopesAreTheSharedFixtures`
 * requires the envelope it builds to equal each one, and the examples together to hold every field
 * it writes, set and in the form it writes when it holds nothing) and with the Legion daemon's
 * intake test, which requires every field the daemon reads.
 */
const EXAMPLES = [
  "issue-updated-every-field.json",
  "issue-claimed-every-field.json",
  "issue-claimed-forced.json",
  "issue-updated-no-field-moved.json",
  "issue-released-closed.json",
  "issue-updated-null-fields.json",
  "issue-claimed-first.json",
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
/** A session a personal token's request body names (api.bearerSessionActor): the token proves its
 *  person, the actor's owner, and proves no session, service or project. */
const NAMED_BY_BODY =
  "a personal token's body named the session, and the token proves only its person";
/** The origin a session outside tmux sends: where it runs, and no tmux pane or title. */
const OUTSIDE_TMUX = "the session runs outside tmux, so its origin names no pane";
const PERSON_WROTE = "a person wrote it, so the envelope has no session source";

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
  "issue-claimed-forced.json": {
    "event.actor.origin.tmux": OUTSIDE_TMUX,
    "event.actor.origin.pane": OUTSIDE_TMUX,
    "event.actor.origin.session_title": OUTSIDE_TMUX,
    "event.actor.service": NAMED_BY_BODY,
    "event.actor.project": NAMED_BY_BODY,
    "event.actor.proven": NAMED_BY_BODY,
    "event.payload.claim.actor.origin.tmux": OUTSIDE_TMUX,
    "event.payload.claim.actor.origin.pane": OUTSIDE_TMUX,
    "event.payload.claim.actor.origin.session_title": OUTSIDE_TMUX,
    "event.payload.claim.actor.service": NAMED_BY_BODY,
    "event.payload.claim.actor.project": NAMED_BY_BODY,
    "event.payload.claim.actor.proven": NAMED_BY_BODY,
    "event.payload.previous_claim.actor.service": PERSON_CALLER,
  },
  "issue-updated-no-field-moved.json": {
    "event.actor.service": PERSON_CALLER,
    "event.payload.references_changed": "the write moved no issue's edges",
    "event.payload.thread_logins": "the write moved neither the status nor the assignee",
  },
  "issue-released-closed.json": {
    "envelope.source_session": PERSON_WROTE,
    "event.payload.previous_claim.actor.service": PERSON_CALLER,
  },
  "issue-updated-null-fields.json": {
    "envelope.source_session": PERSON_WROTE,
    "event.payload.references_changed": "the write moved no issue's edges",
  },
  "issue-claimed-first.json": {
    "event.actor.origin": NO_PLACE,
    "event.actor.service": PERSON_ONLY_CALLER,
    "event.actor.project": PERSON_ONLY_CALLER,
    "event.payload.claim.actor.origin": NO_PLACE,
    "event.payload.claim.actor.service": PERSON_ONLY_CALLER,
    "event.payload.claim.actor.project": PERSON_ONLY_CALLER,
    "event.payload.previous_claim": "a first claim replaces no claim",
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

const OTHER_SOURCE = "another producer's envelope: Dispatch's outbox writes source dispatch alone";
const ISSUE_TARGETS = "an issue write names issue targets alone (api.issueEdgeChanges)";

/**
 * Each value of a closed set a schema declares that no example holds, by the place it is read at
 * (`<object>.<key>`) and the value's JSON, with the reason. Every other value of every such set -
 * an enum, a literal, a union of them, a boolean, a union member no example takes - must be held
 * by an example: so a schema that drops a value Dispatch writes fails to read the example holding
 * it, and one that gains a value fails here until an example holds it or this names it.
 */
const UNHELD: Record<string, Record<string, string>> = {
  "envelope.source": {
    '"agent"': OTHER_SOURCE,
    '"human"': OTHER_SOURCE,
    '"envoy"': OTHER_SOURCE,
    '"github"': OTHER_SOURCE,
    '"slack"': OTHER_SOURCE,
    '"whatsapp"': OTHER_SOURCE,
    '"ghostwispr"': OTHER_SOURCE,
  },
  "event.quiet": {
    true: "events.Quiet holds none of these events quiet: it names delivery receipts, anchor refreshes, settings, targeted messages, quiet ask edits and unnamed versions",
  },
  "system actor.kind": {
    '"system"':
      "no server-owned writer appends an issue.updated, issue.claimed or issue.released: the system actors (the architecture importer, document settlement, the delivery timeline and attribution) write other events",
  },
  "changed reference.kind": {
    '"ask"': ISSUE_TARGETS,
    '"artifact"': ISSUE_TARGETS,
    '"block"': ISSUE_TARGETS,
  },
  "issue.updated payload.priority": {
    "0": "two issue.updated examples hold a priority and one holds none; dispatch-api.test.ts reads each of the four",
    "3": "two issue.updated examples hold a priority and one holds none; dispatch-api.test.ts reads each of the four",
  },
};

const OLDER = (what: string) =>
  `the written schema reads an event retained from a Dispatch older than ${what}, which carries none`;
const OMITTED = (field: string) =>
  `Go writes it only when it holds something (${field}, omitempty), and no example needs it empty`;

/**
 * Each empty form - a key absent, a key null - that a payload schema of these events takes and no
 * example holds, by the place (`<object>.<key>`) and the form, with the reason the schema takes
 * it. Every other form a payload schema takes must be one an example holds: so a payload key made
 * optional or nullable where Go always writes it fails here.
 */
const WIDER: Record<string, { absent?: string; null?: string }> = {
  "issue.updated payload.advances": {
    absent: OLDER("priorities"),
    null: "Go writes a nil list as null (model.Issue.Advances has no omitempty); an issue read fills it, so these events hold []",
  },
  "issue.updated payload.advances_revision": { absent: OLDER("priorities") },
  "issue.updated payload.verification_artifact_id": { absent: OLDER("document roles") },
  "issue.updated payload.plan_artifact_id": { absent: OLDER("document roles") },
  "issue.updated payload.decisions_artifact_id": { absent: OLDER("the design gate's steps") },
  "issue.updated payload.changed": { absent: OLDER("the changed list") },
  "issue.updated payload.ancestors": { absent: OLDER("the parent chain") },
  "issue.updated payload.created_by": {
    null: "Go's model.Issue.CreatedBy is a pointer, which the API document declares nullable (the spellings test pairs the two); every issue holds its creator since migration 0001 (the Go test's neverEmpty)",
  },
  "user actor.proven": {
    absent:
      "an actor stored before the proven mark carries none (model.Actor.Proven), and a payload carries stored actors: an older issue's created_by, a claim taken before the mark",
  },
  "origin.host": { absent: OMITTED("model.ActorOrigin.Host") },
  "origin.machine": { absent: OMITTED("model.ActorOrigin.Machine") },
  "origin.cwd": { absent: OMITTED("model.ActorOrigin.Cwd") },
  "external link.kind": { absent: OMITTED("model.ExternalLink.Kind") },
};

/** The name each object of the schemas is known by in NEVER, UNHELD, WIDER and the problems; an
 *  object not named here (an `advances` entry) is named by where it is. The reading schema of an
 *  update's payload reads the object the written one writes, so it takes that object's name. */
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

/** The value at a path of zod's issues in `value`, as JSON, for a problem to show. */
function heldAt(value: unknown, path: readonly PropertyKey[]): string {
  let held: unknown = value;
  for (const step of path) {
    held =
      held !== null && typeof held === "object"
        ? (held as Record<PropertyKey, unknown>)[step]
        : undefined;
  }
  return held === undefined ? "nothing" : JSON.stringify(held);
}

/** What `schema` makes of an example's value, or undefined and a problem for each place it
 *  refuses, naming what the example holds there: a schema that drops a value an example holds
 *  names it. */
function parsed(
  schema: z.core.$ZodType,
  value: unknown,
  what: string,
  problems: string[]
): unknown {
  const result = z.safeParse(schema, value);
  if (result.success) return result.data;
  for (const issue of result.error.issues) {
    problems.push(
      `${what} is refused at ${issue.path.map(String).join(".") || "its top"}, which holds ${heldAt(value, issue.path)}: ${issue.message}`
    );
  }
  return undefined;
}

test("the issue events Dispatch publishes are envelopes, events and payloads these schemas read, every key, value and form declared", async () => {
  const walk = new ExampleWalk({ never: NEVER, labels: LABELS, absentTable: "ABSENT" });
  const examples: WalkedExample[] = EXAMPLES.map((file) => ({ name: file, absent: ABSENT[file] }));
  const strict = { tolerant: false, forms: false };
  const reader = { tolerant: true, forms: false };
  for (const example of examples) {
    const file = example.name;
    const path = new URL(`../../daemon/internal/intake/testdata/dispatch/${file}`, import.meta.url);
    const envelope = JSON.parse(await Bun.file(path).text());
    // The envelope schema keeps only what it declares, so an undeclared key is dropped here.
    const readEnvelope = parsed(EnvelopeSchema, envelope, `${file}'s envelope`, walk.problems);
    if (readEnvelope !== undefined) expect(readEnvelope).toEqual(envelope);
    const event = JSON.parse(envelope.payload);
    const readEvent = parsed(PublishedEventSchema, event, `${file}'s event`, walk.problems);
    if (readEvent !== undefined) expect(readEvent).toEqual(event);
    // A reader of the bus takes the frame by its tolerant schema, the payload by its type's own.
    const frame = parsed(DispatchEventSchema, event, `${file}'s frame`, walk.problems);
    if (frame !== undefined) {
      expect(frame).toMatchObject({ issue_key: event.issue_key, type: event.type });
    }
    const payload = PAYLOADS[event.type];
    if (payload === undefined)
      throw new Error(`${file} is a ${event.type}, which PAYLOADS names no schema for`);
    const written = payload.written as z.core.$ZodType;
    const readPayload = parsed(written, event.payload, `${file}'s payload`, walk.problems);
    if (readPayload !== undefined) expect(readPayload).toEqual(event.payload);
    walk.walk(EnvelopeSchema, envelope, example, "envelope", "envelope", strict);
    walk.walk(PublishedEventSchema, event, example, "event", "event", strict);
    walk.walk(DispatchEventSchema, event, example, "event", "event", reader);
    // The payload's keys are held to their forms too (WIDER): a payload schema that takes a key
    // absent or null where Go always writes it fails.
    walk.walk(written, event.payload, example, "event.payload", `${event.type} payload`, {
      tolerant: false,
      forms: true,
    });
    if (payload.read !== undefined) {
      // The reading schema keeps each key it declares, as Dispatch wrote it, and each key it
      // declares is one the example holds, unless NEVER or ABSENT names it: so a reader of a key
      // Dispatch does not write fails here.
      const shape = objectShape(payload.read, "IssueEventPayloadSchema") ?? {};
      const kept = Object.fromEntries(
        Object.entries(event.payload).filter(([key]) => key in shape)
      );
      expect(payload.read.parse(event.payload)).toEqual(kept);
      walk.walk(
        payload.read,
        event.payload,
        example,
        "event.payload",
        `${event.type} payload`,
        reader
      );
    }
  }
  walk.staleExceptions(examples);
  walk.unheldValues(UNHELD);
  walk.widenings(WIDER);
  expect(walk.problems).toEqual([]);
});
