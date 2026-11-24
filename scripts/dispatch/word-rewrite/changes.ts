import { z } from "zod";
import type { Actor, Event } from "./dispatch-api.ts";
import type { Item } from "./inventory.ts";

/**
 * The events after the last write-mode run's cursor that changed an item or took it out of a
 * place R6 allows (spec version 37). A report lists apart, as changed since, each occurrence the
 * write mode has not judged in such an item, or that a code keeps as judge-refused, with the
 * event; it is no failure, and the next write-mode run judges it. The events that count:
 *
 * - a version of the document: `artifact.version` (the document at `payload.artifact_id`), and
 *   `artifact.created` (at `payload.artifact`, the document itself), which is how Dispatch writes
 *   a document's first version: it writes no `artifact.version` for it;
 * - its issue's `issue.created`, which names the title and every document of the issue, and
 *   carries the first version of the issue's spec (`payload.primary_artifact_id`), which has no
 *   artifact event of its own;
 * - an `issue.updated` that moved the title (it names the title) or reopened the issue (its
 *   `changed` names `closed_at`; it names the title and every document of the issue, which the
 *   reopen takes out of `closed-issue`). A status move that neither closes nor reopens counts
 *   for nothing, and so does a close, which Dispatch writes as `issue.closed`;
 * - the `ask.answered` or `ask.resolved` that closed an approval request naming the document,
 *   which takes it out of `approval-request`;
 * - the document's `artifact.changes_requested`: a person's Request changes takes the approval
 *   away.
 *
 * A comment, a claim and every other event change nothing a report lists. Dispatch files an issue
 * document's events under its issue with no top-level `artifact_id`, so the document is read from
 * the payload, whose shape differs by type.
 *
 * The tool's own edits and the tester's count for nothing (spec version 40): a version or an
 * `issue.updated` whose actor is a session that ran write mode or the settle command, as the
 * state keeps their ids (`sessions`). Another session's version counts, whatever its summary
 * says.
 */

/** One event that changed an item, as the report names it. */
export interface Change {
  readonly event: number;
  readonly author: Actor;
  /** The version a version event made of the document; null for any other event. */
  readonly version: number | null;
}

const Numbered = z.looseObject({ number: z.int() });

const Payload = z.looseObject({
  artifact_id: z.string().nullable().optional(),
  primary_artifact_id: z.string().nullable().optional(),
  artifact: z
    .looseObject({ id: z.string(), versions: z.array(Numbered).nullable().optional() })
    .nullable()
    .optional(),
  version: z.union([z.int(), Numbered]).nullable().optional(),
  changed: z.array(z.string()).nullable().optional(),
  kind: z.string().nullable().optional(),
  approval: z
    .looseObject({
      artifact_id: z.string().nullable().optional(),
      documents: z
        .array(z.looseObject({ artifact_id: z.string() }))
        .nullable()
        .optional(),
    })
    .nullable()
    .optional(),
});
type Payload = z.infer<typeof Payload>;

/** The number of the version a version event made, or null where its payload names none. */
function versionOf(event: Event, payload: Payload): number | null {
  if (event.type === "artifact.created") return payload.artifact?.versions?.at(-1)?.number ?? 1;
  const version = payload.version;
  if (version === undefined || version === null) return null;
  return typeof version === "number" ? version : version.number;
}

/** The documents an approval request's closing event names. */
function approvalDocuments(payload: Payload): string[] {
  const approval = payload.approval;
  if (payload.kind !== "approval" || approval === undefined || approval === null) return [];
  const named = (approval.documents ?? []).map((document) => document.artifact_id);
  if (approval.artifact_id !== undefined && approval.artifact_id !== null) {
    named.push(approval.artifact_id);
  }
  return named;
}

/** The events after a cursor that changed an item, the newest for each item. */
export class Changes {
  /** Events that name every item of an issue: its creation and a reopen. */
  readonly #issues = new Map<string, Change>();
  /** Title moves, by issue key. */
  readonly #titles = new Map<string, Change>();
  /** Versions, closed approval requests and changes requested, by document id. */
  readonly #documents = new Map<string, Change>();

  /** `sessions`: the sessions that ran the tool, whose versions and `issue.updated` events count
   *  for nothing. */
  constructor(private readonly sessions: ReadonlySet<string>) {}

  /** Takes the next page of events after the cursor, oldest first. */
  add(events: readonly Event[]): void {
    for (const event of events) {
      const parsed = Payload.safeParse(event.payload);
      const payload: Payload = parsed.success ? parsed.data : {};
      const change = { event: event.id, author: event.actor, version: null };
      const key = event.issue_key;
      const own =
        event.actor.kind === "session" &&
        event.actor.id !== undefined &&
        this.sessions.has(event.actor.id);
      switch (event.type) {
        case "artifact.created":
        case "artifact.version": {
          const document = event.artifact_id ?? payload.artifact_id ?? payload.artifact?.id;
          if (own || document === undefined || document === null) break;
          this.#documents.set(document, { ...change, version: versionOf(event, payload) });
          break;
        }
        case "artifact.changes_requested": {
          const document = event.artifact_id ?? payload.artifact_id;
          if (document !== undefined && document !== null) this.#documents.set(document, change);
          break;
        }
        case "ask.answered":
        case "ask.resolved":
          for (const document of approvalDocuments(payload)) this.#documents.set(document, change);
          break;
        case "issue.created": {
          if (key !== null) this.#issues.set(key, change);
          const spec = payload.primary_artifact_id;
          if (spec !== undefined && spec !== null)
            this.#documents.set(spec, { ...change, version: 1 });
          break;
        }
        case "issue.updated": {
          if (own || key === null) break;
          const changed = payload.changed ?? [];
          if (changed.includes("closed_at")) this.#issues.set(key, change);
          if (changed.includes("title")) this.#titles.set(key, change);
          break;
        }
      }
    }
  }

  /** The newest event after the cursor that changed `item`, or undefined for none; of two that
   *  are one event, the one that names the document, which may cite its version. */
  of(item: Item): Change | undefined {
    const key = item.issue?.key;
    const candidates = [
      item.artifact === null ? undefined : this.#documents.get(item.artifact.id),
      key === undefined ? undefined : this.#issues.get(key),
      item.kind === "title" && key !== undefined ? this.#titles.get(key) : undefined,
    ];
    let newest: Change | undefined;
    for (const candidate of candidates) {
      if (candidate !== undefined && (newest === undefined || candidate.event > newest.event)) {
        newest = candidate;
      }
    }
    return newest;
  }
}

/** An author as a report line names it: `<kind>:<id>`, with `,owner:<login>` for a session
 *  that acts for a person. */
export function authorName(actor: Actor): string {
  const owner = actor.owner === undefined || actor.owner === null ? "" : `,owner:${actor.owner}`;
  return `${actor.kind}:${actor.id ?? "-"}${owner}`;
}

/** The link of the version a change made of an item's document, or null. */
export function versionLink(item: Item, change: Change): string | null {
  if (change.version === null || item.artifact === null) return null;
  const owner = item.issue?.key ?? item.project;
  return `dispatch://${owner}/artifact/${item.artifact.slug}@v${change.version}`;
}
