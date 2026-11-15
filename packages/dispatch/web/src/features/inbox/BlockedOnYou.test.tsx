import { expect, test } from "bun:test";

import type { CredentialPendingRow, InboxRow } from "../../api/types";
import { blockedOnYouText, needsYou } from "./BlockedOnYou";
import { withWaitingOnPerson } from "./inbox-fixture";

function request(record_id: string, requested_at: string): CredentialPendingRow {
  return {
    identifiers: ["DEMO_API_KEY"],
    kind: "agent_secret",
    record_id,
    requested_at,
    session: { enrollment: null, request: null },
  };
}

/** One open ask. The person it waits on is resolved as the server resolves it
 *  (`coalesce(a.asked_of, i.assignee, p.default_assignee)`, through `withWaitingOnPerson`), so
 *  an override that moves the issue's assignee, or hands the ask to somebody, moves that person
 *  with it. */
function ask(id: string, overrides: Partial<InboxRow> = {}): InboxRow {
  const row: InboxRow = {
    anchor: null,
    answer: null,
    artifact_id: null,
    asked_of: null,
    asked_of_at: null,
    asked_of_by: null,
    author: { id: "session-1", kind: "session" },
    block_id: null,
    created_at: "2026-09-11T00:00:00Z",
    edited_at: null,
    id,
    issue: { assignee: "alice", key: "CORE-1", title: "Fix the thing" },
    issue_key: "CORE-1",
    kind: "question",
    last_reply: null,
    multiple: false,
    opened_event_id: 1,
    options: [],
    thread: {
      answers: [],
      claimant: null,
      edits: [],
      followers: [],
      replies: [],
      route: null,
      route_status: null,
      route_holder: null,
      route_status_reason: null,
    },
    priority: null,
    snoozed_until: null,
    question: "Which approach?",
    state: "open",
    waiting_on: "human",
    urgency: "med",
    ...overrides,
  };
  return withWaitingOnPerson(row);
}

// The broker's `requested_at` reaches the SPA verbatim, and the count this rule gives also drives
// the shell's Inbox badges, outside every page's error boundary.
test("a request whose time cannot be read still counts, and the control's age comes from the rest", () => {
  const unreadable = request("record-1", "yesterday");
  const threeHoursAgo = request(
    "record-2",
    new Date(Date.now() - 3 * 60 * 60 * 1000).toISOString()
  );

  expect(blockedOnYouText(needsYou([], [unreadable, threeHoursAgo], "alice"))).toBe(
    "Blocked on you: 2 items, oldest 3h"
  );
  expect(blockedOnYouText(needsYou([], [unreadable], "alice"))).toBe("Blocked on you: 1 item");
});

// One meaning for every count of what waits on the reader: whose turn it is AND whose issue it
// is, whatever the list on screen is filtered to. A credential request is nobody's issue and
// anyone signed in may decide one, so it is counted whether or not the login has resolved.
test("the count is the reader's own questions, and no question before the login resolves", () => {
  const asks = [
    ask("ask-mine"),
    ask("ask-bob", { issue: { assignee: "bob", key: "CORE-2", title: "Bob's" } }),
    ask("ask-nobody", { issue: { assignee: null, key: "CORE-3", title: "Nobody's" } }),
    // A document question belongs to no issue, so it is nobody's.
    ask("ask-doc", { issue: undefined, issue_key: null }),
    // Waiting on the agent, on the reader's own issue: not the reader's move.
    ask("ask-agent", { waiting_on: "agent" }),
  ];

  // No question is the reader's yet; the credential request waiting on them still counts.
  expect(needsYou(asks, [request("record-1", "yesterday")], undefined).count).toBe(1);
  expect(needsYou(asks, [], "alice").count).toBe(1);
});
