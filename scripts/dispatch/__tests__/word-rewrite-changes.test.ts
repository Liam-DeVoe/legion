import { describe, expect, test } from "bun:test";
import { z } from "zod";
import { Changes } from "../word-rewrite/changes.ts";
import { type Event, Event as EventSchema } from "../word-rewrite/dispatch-api.ts";
import type { Item } from "../word-rewrite/inventory.ts";
import { runTool } from "../word-rewrite/main.ts";
import { emptyState, serializeState } from "../word-rewrite/state.ts";
import { verdictKey } from "../word-rewrite/verdicts.ts";
import {
  deps,
  FakeDispatch,
  fixture,
  input,
  options,
  STATE_DOCUMENT,
  TOOL_SESSION,
} from "./word-rewrite-fake.ts";

/**
 * "Changed since" (`changes.ts`, spec versions 33 to 44): which events after the last write-mode
 * run's cursor change an item or take it out of a place R6 allows, read against the shapes
 * Dispatch writes (`event-log.json`, `issue-events.json`).
 */

const LOG = EventSchema.array().parse(fixture("event-log.json"));
const ISSUE_EVENTS = EventSchema.array().parse(fixture("issue-events.json"));
const PERSON = { kind: "user", id: "alice" };
const ANOTHER = { kind: "session", id: "another-session" };

/** The ids a captured event's payload names. */
const Named = z.looseObject({
  artifact_id: z.string().optional(),
  primary_artifact_id: z.string().optional(),
  artifact: z.looseObject({ id: z.string() }).optional(),
});
const named = (event: Event) => Named.parse(event.payload);

function logEvent(type: string): Event {
  const found = LOG.find((event) => event.type === type);
  if (found === undefined) throw new Error(`event-log.json holds no ${type}`);
  return found;
}

/** An item of an issue: its title (no artifact) or one of its documents. */
function itemOf(key: string, artifact: string | null): Item {
  const issue = {
    key,
    project: "SEED",
    title: "t",
    status: "todo",
    closed: false,
    createdBy: null,
  };
  return {
    kind: artifact === null ? "title" : "issue-document",
    project: "SEED",
    id: artifact ?? key,
    owner: { kind: "issue", key },
    issue,
    artifact:
      artifact === null
        ? null
        : {
            id: artifact,
            issue_key: key,
            project: "SEED",
            slug: "doc-md",
            name: "doc.md",
            kind: "doc",
            versions: null,
          },
  };
}

/** The changes `events` make, the tool's sessions being `sessions`. */
function changes(events: readonly Event[], sessions: readonly string[] = []): Changes {
  const read = new Changes(new Set(sessions));
  read.add(events);
  return read;
}

/** `event` with its type, actor and payload changed, as Dispatch writes those shapes. */
function shaped(event: Event, change: Partial<Event>): Event {
  return EventSchema.parse({ ...event, ...change });
}

describe("what counts as changed since", () => {
  test("artifact.version (event-log.json) counts for its document, named in the payload, with the version it made", () => {
    const version = logEvent("artifact.version");
    const document = named(version).artifact_id ?? "";
    const change = changes([version]).of(itemOf("SEED-10", document));
    expect(change).toEqual({ event: version.id, author: version.actor, version: 2 });
  });

  test("artifact.created (event-log.json) counts for the document it holds, as its first version", () => {
    const created = logEvent("artifact.created");
    const document = named(created).artifact?.id ?? "";
    expect(changes([created]).of(itemOf("SEED-10", document))?.version).toBe(1);
  });

  test("issue.created (issue-events.json) counts for the title and every document, and is the spec's first version", () => {
    const created = ISSUE_EVENTS.find((event) => event.type === "issue.created");
    if (created === undefined) throw new Error("issue-events.json holds no issue.created");
    const key = created.issue_key ?? "";
    const spec = named(created).primary_artifact_id ?? "";
    const read = changes([created]);
    expect(read.of(itemOf(key, null))?.event).toBe(created.id);
    expect(read.of(itemOf(key, "another-document"))?.event).toBe(created.id);
    expect(read.of(itemOf(key, spec))?.version).toBe(1);
  });

  test("an issue.updated that changed the title (event-log.json) counts for the title alone", () => {
    const retitled = logEvent("issue.updated");
    const read = changes([retitled]);
    expect(read.of(itemOf("SEED-10", null))?.event).toBe(retitled.id);
    expect(read.of(itemOf("SEED-10", "a-document"))).toBeUndefined();
  });

  test("a reopen, an issue.updated whose changed names closed_at, counts for every item of the issue", () => {
    const reopened = shaped(logEvent("issue.updated"), {
      payload: { status: "todo", changed: ["status", "closed_at"] },
    });
    const read = changes([reopened]);
    expect(read.of(itemOf("SEED-10", null))?.event).toBe(reopened.id);
    expect(read.of(itemOf("SEED-10", "a-document"))?.event).toBe(reopened.id);
  });

  for (const type of ["ask.answered", "ask.resolved"]) {
    test(`an ${type} that closed an approval request counts for the documents it names`, () => {
      const closing = shaped(logEvent("issue.updated"), {
        type,
        actor: PERSON,
        payload: {
          kind: "approval",
          approval: { artifact_id: "named", documents: [{ artifact_id: "also-named" }] },
        },
      });
      const read = changes([closing]);
      expect(read.of(itemOf("SEED-10", "named"))?.author).toEqual(PERSON);
      expect(read.of(itemOf("SEED-10", "also-named"))?.event).toBe(closing.id);
      expect(read.of(itemOf("SEED-10", "unnamed"))).toBeUndefined();
    });
  }

  test("artifact.changes_requested counts for its document", () => {
    const requested = shaped(logEvent("artifact.version"), {
      type: "artifact.changes_requested",
      actor: PERSON,
      payload: { artifact_id: "requested", version: 3, kind: "person" },
    });
    expect(changes([requested]).of(itemOf("SEED-10", "requested"))?.event).toBe(requested.id);
  });

  test("a comment, a claim, a plain status move and a close count for nothing", () => {
    const base = logEvent("issue.updated");
    const events = [
      shaped(base, { type: "comment.created", payload: { body: "a comment" } }),
      shaped(base, { type: "issue.claimed", payload: { reason: "claimed" } }),
      shaped(base, { payload: { status: "in_progress", changed: ["status"] } }),
      shaped(base, {
        type: "issue.closed",
        payload: { status: "done", changed: ["status", "closed_at"] },
      }),
    ];
    const read = changes(events);
    expect(read.of(itemOf("SEED-10", null))).toBeUndefined();
    expect(read.of(itemOf("SEED-10", "a-document"))).toBeUndefined();
  });

  test("a version or title edit of a session that ran the tool counts for nothing; another session's version counts, whatever its summary names (spec version 40)", () => {
    const version = logEvent("artifact.version");
    const document = named(version).artifact_id ?? "";
    const own = shaped(version, { actor: TOOL_SESSION });
    const ownTitle = shaped(logEvent("issue.updated"), { actor: TOOL_SESSION });
    const tools = [TOOL_SESSION.id];
    expect(changes([own], tools).of(itemOf("SEED-10", document))).toBeUndefined();
    expect(changes([ownTitle], tools).of(itemOf("SEED-10", null))).toBeUndefined();
    const summarized = shaped(version, {
      actor: ANOTHER,
      payload: {
        artifact_id: document,
        version: { number: 3, summary: "INFRA-2017: the word is replaced" },
      },
    });
    expect(changes([summarized], tools).of(itemOf("SEED-10", document))?.author).toEqual(ANOTHER);
  });
});

describe("a report's changed-since section", () => {
  test("lists an occurrence not judged that another session's version added, with the event, its author and the version's link", async () => {
    const dispatch = new FakeDispatch();
    const issue = dispatch.issue("SEED", "An issue");
    const document = dispatch.document({ issue: issue.key }, "notes.md", "Notes.\n");
    const cursor = dispatch.log.length;
    dispatch.document(
      STATE_DOCUMENT,
      "rewrite-state.md",
      serializeState({
        ...emptyState(),
        cursor,
        writeCursor: cursor,
        whole: true,
        finished: ["SEED"],
        sessions: [TOOL_SESSION.id],
      })
    );
    dispatch.editAsOther(document.id, "Notes.", input("notes").trimEnd(), ANOTHER);
    const event = dispatch.log.length;
    const run = deps(dispatch);
    expect(await runTool(options({ stateDocument: STATE_DOCUMENT }), run)).toBe(0);
    expect(run.lines).toContain(
      `changed-since kind=issue-document project=SEED issue=${issue.key} document=notes-md byte=${Buffer.byteLength("The notes say it was ")} event=${event} author=session:another-session version=dispatch://${issue.key}/artifact/notes-md@v2`
    );
    expect(run.lines).toContain("changed-since 1");
  });

  test("a judge-refused occurrence is listed under a place a program tells while it holds, and as changed since once an event after the cursor ends that place", async () => {
    const dispatch = new FakeDispatch();
    const issue = dispatch.issue("SEED", "An issue");
    const document = dispatch.document({ issue: issue.key }, "notes.md", input("notes"));
    dispatch.close(issue.key);
    const key = verdictKey(document.id, input("notes").trimEnd(), 0);
    const cursor = dispatch.log.length;
    dispatch.document(
      STATE_DOCUMENT,
      "rewrite-state.md",
      serializeState({
        ...emptyState(),
        cursor,
        writeCursor: cursor,
        whole: true,
        finished: ["SEED"],
        verdicts: new Map([[key, "j" as const]]),
        refused: [{ project: "SEED", issue: issue.key, id: document.id, key }],
        sessions: [TOOL_SESSION.id],
      })
    );
    const closed = deps(dispatch);
    expect(await runTool(options({ stateDocument: STATE_DOCUMENT }), closed)).toBe(0);
    expect(
      closed.lines.some(
        (line) => line.includes("document=notes-md") && line.includes("reason=closed-issue")
      )
    ).toBe(true);
    expect(closed.lines).toContain("changed-since 0");

    dispatch.moveStatus(issue.key, "todo");
    const reopen = dispatch.log.length;
    const reopened = deps(dispatch);
    expect(await runTool(options({ stateDocument: STATE_DOCUMENT }), reopened)).toBe(0);
    expect(
      reopened.lines.some((line) => line.includes(`judge-refused item=${document.id} key=${key}`))
    ).toBe(true);
    expect(
      reopened.lines.some(
        (line) =>
          line.startsWith("changed-since kind=issue-document") && line.includes(`event=${reopen}`)
      )
    ).toBe(true);
    expect(reopened.lines).toContain("not-judged 1");
  });
});
