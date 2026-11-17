import { expect, test } from "bun:test";

import { eventRefreshTargets } from "./event-refresh";
import type { DispatchEvent } from "./types";

/**
 * What refreshes the read a document's decision blocks take their answer state, answer, answerer,
 * time and a copy's source from: the asks of the document's owner, `["asks", <issue>]` for an
 * issue's documents and `["artifact", <id>, "asks"]` for a project document's. A person's answer
 * is held on the ask alone and changes no byte of the document, so the card learns of it through
 * the ask's own events - an answer, a resolution, an opening - and never through the document.
 */

/** An event of `type` about the ask `id`, whose block is on the document `artifact`. */
function askEvent(type: string, artifact: string, issueKey: string | null): DispatchEvent {
  return {
    actor: { id: "alice", kind: "user" },
    artifact_id: issueKey === null ? artifact : null,
    created_at: "2026-11-16T00:00:00Z",
    id: 1,
    issue_key: issueKey,
    notify: true,
    payload: { block_artifact: { id: artifact }, block_id: "q1", id: "ask-1" },
    project: "CORE",
    seq: 1,
    type,
  } as unknown as DispatchEvent;
}

/** Whether a target of the event's refresh reads key, by prefix or exactly. */
function refreshes(event: DispatchEvent, key: readonly unknown[]): boolean {
  return eventRefreshTargets(event).some(
    (target) =>
      target.queryKey.length <= key.length &&
      target.queryKey.every((part, index) => part === key[index]) &&
      (target.exact !== true || target.queryKey.length === key.length)
  );
}

test.each([
  "ask.answered",
  "ask.resolved",
  "ask.opened",
])("%s refreshes the asks an issue document's decision blocks read", (type) => {
  expect(refreshes(askEvent(type, "artifact-1", "CORE-1"), ["asks", "CORE-1"])).toBe(true);
});

test.each([
  "ask.answered",
  "ask.resolved",
  "ask.opened",
])("%s refreshes the asks a project document's decision blocks read", (type) => {
  expect(refreshes(askEvent(type, "artifact-2", null), ["artifact", "artifact-2", "asks"])).toBe(
    true
  );
});
