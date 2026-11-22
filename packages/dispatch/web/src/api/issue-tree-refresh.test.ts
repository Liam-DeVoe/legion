import { expect, test } from "bun:test";
import { QueryClient } from "@tanstack/react-query";

import {
  type TreeNodeExtra,
  treeAnswer,
  treeNode,
} from "../features/issue-page/tree-read/tree-fixtures";
import { viewKey } from "../features/issue-page/tree-read/tree-read";
import { eventRefreshTargets } from "./event-refresh";
import { issueTreeKey } from "./queries";
import { refreshQueries } from "./query-refresh";
import type { DispatchEvent, IssueTreeParent } from "./types";

/**
 * The issue page's tree read is told of every event of its issue: each one names the tree's key
 * (`issueTreeKey`), by prefix, so the views under it (`["issue-tree", key, view]`) are reached
 * too. An event of another issue reaches a tree or a view where its answer holds a node of that
 * issue, or a node standing under one, of a kind the event can move, and an issue write each
 * ancestor's tree it names; an event of no issue reaches none.
 */

/** An event of `type`, with the payload its reader reads; the feed's union is wider than this
 *  test needs. */
function eventOf(
  type: string,
  payload: Record<string, unknown>,
  issueKey: string | null
): DispatchEvent {
  return {
    actor: { id: "alice", kind: "user" },
    artifact_id: typeof payload.artifact_id === "string" ? payload.artifact_id : null,
    created_at: "2026-11-05T00:00:00Z",
    id: 1,
    issue_key: issueKey,
    notify: true,
    payload,
    project: "CORE",
    seq: 1,
    type,
  } as unknown as DispatchEvent;
}

const ISSUE_EVENTS: readonly DispatchEvent[] = [
  eventOf("comment.created", { body: "A reply.", id: "comment-1", kind: "comment" }, "CORE-1"),
  eventOf("decision.kept", { block_id: "block-1" }, "CORE-1"),
  eventOf("issue.updated", { changes: ["status"] }, "CORE-1"),
  eventOf("artifact.version", { artifact_id: "artifact-1", number: 2 }, "CORE-1"),
  eventOf("ask.opened", { id: "ask-1" }, "CORE-1"),
  eventOf("issue.claimed", {}, "CORE-1"),
  eventOf("handoff.recorded", {}, "CORE-1"),
  eventOf("child.status", {}, "CORE-1"),
];

test("every event of an issue names its tree read, by prefix", () => {
  for (const event of ISSUE_EVENTS) {
    expect(eventRefreshTargets(event, "alice"), event.type).toContainEqual({
      queryKey: issueTreeKey("CORE-1"),
    });
  }
});

test("an event of an issue marks its tree and each view stale, and no other issue's", () => {
  const client = new QueryClient();
  const tree = issueTreeKey("CORE-1");
  const view = [...tree, "spec"];
  const other = issueTreeKey("CORE-2");
  for (const queryKey of [tree, view, other]) client.setQueryData(queryKey, { nodes: [] });
  refreshQueries(client, eventRefreshTargets(ISSUE_EVENTS[0] as DispatchEvent, "alice"));
  expect(client.getQueryState(tree)?.isInvalidated, "the tree").toBe(true);
  expect(client.getQueryState(view)?.isInvalidated, "a view under it").toBe(true);
  expect(client.getQueryState(other)?.isInvalidated, "another issue's tree").toBe(false);
  const issueless = eventOf("project.updated", {}, null);
  expect(
    eventRefreshTargets(issueless, "alice").some((target) => target.queryKey[0] === "issue-tree"),
    "an event of no issue"
  ).toBe(false);
});

test("an event of a parent's spec marks stale a child's view that holds a node of the parent, and no tree that holds none", () => {
  const client = new QueryClient();
  // The child's Verification view carries the parent's requirement as context, read-only.
  const childView = viewKey("CORE-2", "verification");
  const requirement = treeNode("r1", "requirement", null, {
    in: "spec",
    issue: "CORE-1",
    read_only: true,
    role: "context",
    text: "The outline draws every child.",
  });
  client.setQueryData(
    childView,
    treeAnswer([treeNode("CORE-2", "issue", null), requirement], [], {
      issue: "CORE-2",
      view: "verification",
    })
  );
  // The child's whole tree and another issue's tree, each holding nodes of its own issue alone.
  const childTree = issueTreeKey("CORE-2");
  client.setQueryData(
    childTree,
    treeAnswer([treeNode("CORE-2", "issue", null)], [], { issue: "CORE-2" })
  );
  const other = issueTreeKey("CORE-3");
  client.setQueryData(
    other,
    treeAnswer(
      [treeNode("CORE-3", "issue", null), treeNode("comment-3", "comment", "CORE-3")],
      [],
      { issue: "CORE-3" }
    )
  );
  const specVersion = eventOf(
    "artifact.version",
    { artifact_id: "artifact-spec", number: 2 },
    "CORE-1"
  );
  refreshQueries(client, eventRefreshTargets(specVersion, "alice"));
  expect(
    client.getQueryState(childView)?.isInvalidated,
    "the child's view that holds the parent's requirement"
  ).toBe(true);
  expect(
    client.getQueryState(childTree)?.isInvalidated,
    "the child's tree, which holds no node of the parent"
  ).toBe(false);
  expect(client.getQueryState(other)?.isInvalidated, "another issue's tree").toBe(false);
});

// A write of a child names each ancestor's tree (`ancestors`): a new child, or one moved under
// the ancestor, is a node the ancestor's answer does not hold yet, so no answer that holds a node
// of the child reaches it.
test("an issue write marks stale each ancestor's tree it names, whose answer holds no node of the issue", () => {
  const client = new QueryClient();
  const parent = issueTreeKey("CORE-1");
  client.setQueryData(
    parent,
    treeAnswer([treeNode("CORE-1", "issue", null)], [], { issue: "CORE-1" })
  );
  const moved = eventOf("issue.updated", { ancestors: ["CORE-1"], changes: ["parent"] }, "CORE-2");
  refreshQueries(client, eventRefreshTargets(moved, "alice"));
  expect(
    client.getQueryState(parent)?.isInvalidated,
    "the parent's tree, which holds no node of the child"
  ).toBe(true);
});

// An issue write with no `ancestors` - one an older server wrote mid-rollout, or one written
// before the field existed and replayed with the feed's gap - falls back to what any ancestor
// could be, as the issue's detail does (every loaded detail refreshes). A new child's create is
// the one event that reaches its parent: the parent's tree holds no node of the child yet.
test("an issue write that names no ancestors marks every held tree stale, as it marks every issue's detail", () => {
  const client = new QueryClient();
  const parent = issueTreeKey("CORE-1");
  client.setQueryData(
    parent,
    treeAnswer([treeNode("CORE-1", "issue", null)], [], { issue: "CORE-1" })
  );
  client.setQueryData(["issue", "CORE-1"], { key: "CORE-1" });
  const created = eventOf("issue.created", { parent: "CORE-1" }, "CORE-2");
  refreshQueries(client, eventRefreshTargets(created, "alice"));
  expect(
    {
      detail: client.getQueryState(["issue", "CORE-1"])?.isInvalidated,
      tree: client.getQueryState(parent)?.isInvalidated,
    },
    "the parent's detail and its tree, after a child's create that names no ancestors"
  ).toEqual({ detail: true, tree: true });
});

/** What the route marks each node of the child CORE-2 with in an answer of another issue. */
const CHILD: TreeNodeExtra = { issue: "CORE-2", read_only: true };

/** The parent CORE-1's answers that hold nodes of its child CORE-2, as the route answers them:
 *  its outline, which holds the child's node, a check of the child's that serves the parent's
 *  requirement under that node, and a result under the check; and its Verification view, which
 *  holds the requirement, the check and the result, the check under the child's node, which
 *  that view does not hold. */
function holdingChild(client: QueryClient): {
  readonly outline: readonly unknown[];
  readonly verification: readonly unknown[];
} {
  const requirement = treeNode("r1", "requirement", "CORE-1", {
    in: "spec",
    text: "Every child is drawn.",
  });
  const check = (parent: string | IssueTreeParent) =>
    treeNode("k1", "check", parent, { ...CHILD, in: "verification", text: "Draw CORE-2." });
  const result = treeNode("result-1", "result", "k1", CHILD);
  const outline = issueTreeKey("CORE-1");
  client.setQueryData(
    outline,
    treeAnswer([
      treeNode("CORE-1", "issue", null),
      requirement,
      treeNode("CORE-2", "issue", "CORE-1", { ...CHILD, text: "Draw each child" }),
      check("CORE-2"),
      result,
    ])
  );
  const verification = viewKey("CORE-1", "verification");
  const childNode: IssueTreeParent = {
    first_line: "Draw each child",
    id: "CORE-2",
    issue: "CORE-2",
    kind: "issue",
    supertag: "issue",
  };
  client.setQueryData(
    verification,
    treeAnswer([treeNode("CORE-1", "issue", null), requirement, check(childNode), result], [], {
      view: "verification",
    })
  );
  return { outline, verification };
}

// A child's comment, message or follower moves nothing of the child's that its parent's answers
// hold - its node, its check, the result under the check - and no answer holds a comment of
// another issue, so neither answer is told of it. A version of the child's documents writes its
// checks, and a result stands under its check: both reach each answer that holds the check.
test("a child's comment or message marks stale no tree of its parent that holds the child's nodes, and its document version or result does", () => {
  const cases: readonly (readonly [DispatchEvent, boolean])[] = [
    [
      eventOf("comment.created", { body: "A reply.", id: "comment-2", kind: "comment" }, "CORE-2"),
      false,
    ],
    [eventOf("comment.edited", { body: "A reply, reworded.", id: "comment-2" }, "CORE-2"), false],
    [
      eventOf("message.created", { body: "Look at the outline.", id: "message-2" }, "CORE-2"),
      false,
    ],
    [eventOf("ask.follower_added", { ask_id: "ask-2", follower: "bob" }, "CORE-2"), false],
    [eventOf("artifact.version", { artifact_id: "artifact-2", number: 2 }, "CORE-2"), true],
    [
      eventOf(
        "result.recorded",
        { check_block_id: "k1", check_issue: "CORE-2", issue: "CORE-2", verdict: "pass" },
        "CORE-2"
      ),
      true,
    ],
  ];
  for (const [event, marks] of cases) {
    const client = new QueryClient();
    const { outline, verification } = holdingChild(client);
    refreshQueries(client, eventRefreshTargets(event, "alice"));
    expect(
      {
        outline: client.getQueryState(outline)?.isInvalidated,
        verification: client.getQueryState(verification)?.isInvalidated,
      },
      event.type
    ).toEqual({ outline: marks, verification: marks });
  }
});

// A descendant's view carries its ancestor's requirement as context. A version of the
// ancestor's documents can move the requirement; the ancestor's comment or message moves no node
// of the ancestor's that the view holds, and the view holds no comment of another issue.
test("an ancestor's comment marks stale no descendant's view that holds the ancestor's requirement, and its document version does", () => {
  const cases: readonly (readonly [DispatchEvent, boolean])[] = [
    [
      eventOf("comment.created", { body: "A reply.", id: "comment-1", kind: "comment" }, "CORE-1"),
      false,
    ],
    [eventOf("comment.edited", { body: "A reply, reworded.", id: "comment-1" }, "CORE-1"), false],
    [eventOf("message.created", { body: "Look at the spec.", id: "message-1" }, "CORE-1"), false],
    [eventOf("artifact.version", { artifact_id: "artifact-spec", number: 2 }, "CORE-1"), true],
  ];
  for (const [event, marks] of cases) {
    const client = new QueryClient();
    const childView = viewKey("CORE-2", "verification");
    const requirement = treeNode("r1", "requirement", null, {
      in: "spec",
      issue: "CORE-1",
      read_only: true,
      role: "context",
      text: "Every child is drawn.",
    });
    client.setQueryData(
      childView,
      treeAnswer([treeNode("CORE-2", "issue", null), requirement], [], {
        issue: "CORE-2",
        view: "verification",
      })
    );
    refreshQueries(client, eventRefreshTargets(event, "alice"));
    expect(client.getQueryState(childView)?.isInvalidated, event.type).toBe(marks);
  }
});

// A write of the child that moves what the parent's outline draws of the child's node - its
// title here - reaches a tree that holds the node by the node alone. The event names no
// ancestor, so nothing but the node the parent's answer holds can reach the parent's tree.
test("a child's title change marks stale a tree that holds the child's node", () => {
  const client = new QueryClient();
  const { outline } = holdingChild(client);
  const retitled = eventOf("issue.updated", { ancestors: [], changed: ["title"] }, "CORE-2");
  refreshQueries(client, eventRefreshTargets(retitled, "alice"));
  expect(client.getQueryState(outline)?.isInvalidated, "the parent's outline").toBe(true);
});
