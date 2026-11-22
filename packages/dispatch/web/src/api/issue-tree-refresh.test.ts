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
 * issue, or a node standing under one, of a kind the event can move - for an ask's event, one the
 * ask is on - and an issue write that may have moved its issue's parent each ancestor's tree it
 * names; an event of no issue reaches none.
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
  eventOf("issue.updated", { changed: ["status"] }, "CORE-1"),
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

/** The child CORE-2's Verification view, which carries its parent CORE-1's requirement as
 *  context, read-only, as the route answers it. */
function viewHoldingParent(client: QueryClient): readonly unknown[] {
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
  return childView;
}

test("an event of a parent's spec marks stale a child's view that holds a node of the parent, and no tree that holds none", () => {
  const client = new QueryClient();
  const childView = viewHoldingParent(client);
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
  const moved = eventOf("issue.updated", { ancestors: ["CORE-1"], changed: ["parent"] }, "CORE-2");
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

// A descendant's view carries its ancestor's requirement as context, and a version of the
// ancestor's documents, which can move the requirement, marks the view stale (the case of a
// parent's spec above). The ancestor's comment or message moves no node of the ancestor's that
// the view holds, and the view holds no comment of another issue, so neither marks it stale.
test("an ancestor's comment or message marks stale no descendant's view that holds the ancestor's requirement", () => {
  const events: readonly DispatchEvent[] = [
    eventOf("comment.created", { body: "A reply.", id: "comment-1", kind: "comment" }, "CORE-1"),
    eventOf("comment.edited", { body: "A reply, reworded.", id: "comment-1" }, "CORE-1"),
    eventOf("message.created", { body: "Look at the spec.", id: "message-1" }, "CORE-1"),
  ];
  for (const event of events) {
    const client = new QueryClient();
    const childView = viewHoldingParent(client);
    refreshQueries(client, eventRefreshTargets(event, "alice"));
    expect(client.getQueryState(childView)?.isInvalidated, event.type).toBe(false);
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

// A write of a child or a grandchild that moves nothing the parent's answers hold of it - its
// status, assignee, priority, labels, or nothing at all - leaves the parent's tree and views
// alone: the tree's issue node carries none of those fields, and the child row draws its status
// and holder from the parent's issue detail, which the write refreshes by its `ancestors`. The
// event names each ancestor, as the server writes it; only a create or a parent move can put the
// issue under a tree whose answer does not hold it yet.
test("a write of a child that moves nothing its parent's answers hold of it marks no tree or view of the parent stale", () => {
  const cases: readonly (readonly [string, Record<string, unknown>, string])[] = [
    ["status", { ancestors: ["CORE-1"], changed: ["status"] }, "CORE-2"],
    ["assignee", { ancestors: ["CORE-1"], changed: ["assignee"] }, "CORE-2"],
    ["priority", { ancestors: ["CORE-1"], changed: ["priority"] }, "CORE-2"],
    ["labels", { ancestors: ["CORE-1"], changed: ["labels"] }, "CORE-2"],
    ["nothing", { ancestors: ["CORE-1"], changed: [] }, "CORE-2"],
    ["a grandchild's status", { ancestors: ["CORE-2", "CORE-1"], changed: ["status"] }, "CORE-3"],
  ];
  for (const [moved, payload, key] of cases) {
    const client = new QueryClient();
    const { outline, verification } = holdingChild(client);
    client.setQueryData(["issue", "CORE-1"], { key: "CORE-1" });
    refreshQueries(client, eventRefreshTargets(eventOf("issue.updated", payload, key), "alice"));
    expect(
      {
        detail: client.getQueryState(["issue", "CORE-1"])?.isInvalidated,
        outline: client.getQueryState(outline)?.isInvalidated,
        verification: client.getQueryState(verification)?.isInvalidated,
      },
      `a write of ${key} that moves ${moved}`
    ).toEqual({ detail: true, outline: false, verification: false });
  }
});

// A write of a child reads each ancestor's tree it names, and every view of it, where it may have
// moved the child's parent: a create under the ancestor and a parent move put the child's nodes in
// an answer that holds none of them yet, and a write that does not say what it moved may have
// moved the parent. A write that moved no field reads none of them.
test("an issue write marks stale each ancestor's tree it names only where it may have moved the issue's parent", () => {
  const cases: readonly (readonly [string, string, Record<string, unknown>, boolean])[] = [
    [
      "a create under the parent",
      "issue.created",
      { ancestors: ["CORE-1"], changed: ["title", "status", "parent"] },
      true,
    ],
    ["a parent move", "issue.updated", { ancestors: ["CORE-1"], changed: ["parent"] }, true],
    ["a write that does not say what it moved", "issue.updated", { ancestors: ["CORE-1"] }, true],
    ["a write that moved no field", "issue.updated", { ancestors: ["CORE-1"], changed: [] }, false],
  ];
  for (const [name, type, payload, marks] of cases) {
    const client = new QueryClient();
    const parent = issueTreeKey("CORE-1");
    const spec = viewKey("CORE-1", "spec");
    client.setQueryData(
      parent,
      treeAnswer([treeNode("CORE-1", "issue", null)], [], { issue: "CORE-1" })
    );
    client.setQueryData(
      spec,
      treeAnswer([treeNode("r1", "requirement", null, { in: "spec" })], [], { view: "spec" })
    );
    refreshQueries(client, eventRefreshTargets(eventOf(type, payload, "CORE-2"), "alice"));
    expect(
      {
        spec: client.getQueryState(spec)?.isInvalidated,
        tree: client.getQueryState(parent)?.isInvalidated,
      },
      name
    ).toEqual({ spec: marks, tree: marks });
  }
});

// A child's title is a member of the node its parent's outline holds of it, so the outline reads
// it again through that node. A grandparent's answer holds no node of the child - it lists its own
// children alone - so neither its outline nor a view of it reads the title again, though the
// write names the grandparent among the child's ancestors.
test("a child's title change marks its parent's outline that holds its node, and no grandparent's outline or view that holds none", () => {
  const client = new QueryClient();
  const { outline } = holdingChild(client);
  const grandparent = issueTreeKey("CORE-0");
  const grandparentSpec = viewKey("CORE-0", "spec");
  client.setQueryData(
    grandparent,
    treeAnswer(
      [
        treeNode("CORE-0", "issue", null),
        treeNode("CORE-1", "issue", "CORE-0", { issue: "CORE-1", read_only: true }),
      ],
      [],
      { issue: "CORE-0" }
    )
  );
  client.setQueryData(
    grandparentSpec,
    treeAnswer([treeNode("r0", "requirement", null, { in: "spec" })], [], {
      issue: "CORE-0",
      view: "spec",
    })
  );
  const retitled = eventOf(
    "issue.updated",
    { ancestors: ["CORE-1", "CORE-0"], changed: ["title"], title: "Draw each child, once" },
    "CORE-2"
  );
  refreshQueries(client, eventRefreshTargets(retitled, "alice"));
  expect({
    grandparent: client.getQueryState(grandparent)?.isInvalidated,
    grandparentSpec: client.getQueryState(grandparentSpec)?.isInvalidated,
    parent: client.getQueryState(outline)?.isInvalidated,
  }).toEqual({ grandparent: false, grandparentSpec: false, parent: true });
});

// An ask's event moves only what the ask is on - its typed block, or the block its anchor's mark
// is in, and the document it is written in - and a block found invalid or repaired only that
// block. So a tree of the parent reads a child's ask event again where its answer holds one of
// those, and a plain ask of the child, with no block and no anchor, is on none of them.
test("a child's ask event marks stale a tree of its parent only where its answer holds what the ask is on", () => {
  const plain = { anchor: null, id: "ask-2", question: "Which outline?", state: "open" };
  const cases: readonly (readonly [string, DispatchEvent, boolean])[] = [
    ["a plain ask opened", eventOf("ask.opened", plain, "CORE-2"), false],
    [
      "a plain ask answered",
      eventOf("ask.answered", { ...plain, state: "answered" }, "CORE-2"),
      false,
    ],
    [
      "a block ask written in the document of the child's check",
      eventOf(
        "ask.opened",
        {
          ...plain,
          block_artifact: { id: "artifact-2", primary: false, slug: "verification" },
          block_id: "q1",
        },
        "CORE-2"
      ),
      true,
    ],
    [
      "an ask anchored in the child's check",
      eventOf(
        "ask.edited",
        {
          ...plain,
          anchor: {
            artifact_id: "artifact-2",
            block_id: "k1",
            mark_id: "mark-1",
            orphaned: false,
            quote: "Draw",
            version: 2,
          },
        },
        "CORE-2"
      ),
      true,
    ],
    [
      "the child's check found invalid",
      eventOf(
        "block.invalid",
        { block_id: "k1", block_type: "check", reason: "no such job", version: 3 },
        "CORE-2"
      ),
      true,
    ],
    [
      "another block of the child repaired",
      eventOf("block.repaired", { block_id: "q9", version: 3 }, "CORE-2"),
      false,
    ],
  ];
  for (const [name, event, marks] of cases) {
    const client = new QueryClient();
    const { outline, verification } = holdingChild(client);
    refreshQueries(client, eventRefreshTargets(event, "alice"));
    expect(
      {
        outline: client.getQueryState(outline)?.isInvalidated,
        verification: client.getQueryState(verification)?.isInvalidated,
      },
      name
    ).toEqual({ outline: marks, verification: marks });
  }
});

// A child's view carries as context an ask of its parent's that a person answered and that
// shaped a node of the view - the parent's requirement it carries - and the ask's event, which
// can reword it or take its answer back, reaches the view by the ask's own node, wherever the
// ask's typed block is written.
test("an ancestor's ask event marks stale a descendant's view that carries the answered ask", () => {
  const client = new QueryClient();
  const childView = viewKey("CORE-2", "spec");
  client.setQueryData(
    childView,
    treeAnswer(
      [
        treeNode("CORE-2", "issue", null),
        treeNode("r1", "requirement", null, {
          in: "spec",
          issue: "CORE-1",
          read_only: true,
          role: "context",
        }),
        treeNode("ask-1", "ask", null, { decision: {}, role: "context", text: "Which outline?" }),
      ],
      [],
      { issue: "CORE-2", view: "spec" }
    )
  );
  const reworded = eventOf(
    "ask.edited",
    {
      anchor: null,
      block_artifact: { id: "artifact-decisions", primary: false, slug: "decisions" },
      block_id: "q1",
      id: "ask-1",
    },
    "CORE-1"
  );
  refreshQueries(client, eventRefreshTargets(reworded, "alice"));
  expect(client.getQueryState(childView)?.isInvalidated, "the child's view").toBe(true);
});
