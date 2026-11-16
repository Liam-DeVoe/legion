import { expect, test } from "bun:test";

import type {
  Artifact,
  Comment,
  Decision,
  IssueCoverage,
  IssueDecisions,
  IssueDetails,
} from "../../api/types";
import { buildIssueTree, type TreeNode } from "./issue-tree";
import { treeContext } from "./tree-drafts";
import { waitingOnReader } from "./waiting";

/**
 * What waits on the reader, read two ways from the same reads: the header's count
 * (`waitingOnReader`) and the tree's own `waiting` on each node (dispatch://INFRA-1196 R7, R13,
 * R35). The two must agree, or the count puts the focus on a node that does not wait, or counts
 * one it can never reach.
 */

const KEY = "CORE-1";

/** One open ask as the issue read carries it. */
type OpenAsk = NonNullable<IssueDetails["open_asks"]>[number];

function issueDocument(id: string, slug: string, primary = false): Artifact {
  return {
    created_at: "2026-11-05T00:00:00Z",
    created_by: { id: "alice", kind: "user" },
    id,
    issue_key: KEY,
    kind: "doc",
    name: slug,
    primary,
    project: "CORE",
    ref_key: `artifact:${id}`,
    session_id: null,
    slug,
    versions: [],
  };
}

const spec = issueDocument("artifact-spec", "spec", true);
const made = issueDocument("artifact-decisions", "decisions-md");

/** One open ask of the issue, waiting on the person `person`. */
function openAsk(id: string, kind: "approval" | "question", person: string): OpenAsk {
  return {
    anchor: null,
    answer: null,
    artifact_id: kind === "approval" ? spec.id : null,
    asked_of: null,
    asked_of_at: null,
    asked_of_by: null,
    author: { id: "planner", kind: "session" },
    block_id: null,
    created_at: "2026-11-05T00:00:00Z",
    edited_at: null,
    id,
    issue_key: KEY,
    kind,
    last_reply: null,
    multiple: false,
    options: [],
    question: `Ask ${id}?`,
    state: "open",
    urgency: "med",
    waiting_on: "human",
    waiting_on_person: person,
  };
}

const issue: IssueDetails = {
  advances: [],
  advances_revision: 0,
  artifacts: [spec, made],
  assignee: "alice",
  blocked_by: [],
  children: [],
  claim: null,
  closed_at: null,
  components: { ids: [], inherited_from: null, mode: "inherit", reason: null, unknown: [] },
  created_at: "2026-11-05T00:00:00Z",
  created_by: { id: "alice", kind: "user" },
  decisions_artifact_id: made.id,
  design_gates: [],
  external_links: [],
  holder_state: null,
  key: KEY,
  labels: [],
  last_seen: null,
  last_seq: 1,
  number: 1,
  open_asks: [
    openAsk("mine", "question", "alice"),
    openAsk("theirs", "question", "bob"),
    openAsk("approve-spec", "approval", "alice"),
  ],
  parent: null,
  plan_artifact_id: "",
  primary_artifact_id: spec.id,
  priority: null,
  progress: { children: null, tasks: null },
  project: "CORE",
  rank: "U",
  referenced_by_count: 0,
  repo: null,
  resolved_repo: null,
  route: null,
  route_holder: null,
  route_status: null,
  route_status_reason: null,
  status: "todo",
  takeable_at: null,
  title: "The issue page draws one ordered body",
  updated_at: "2026-11-05T00:00:00Z",
  verification_artifact_id: "",
};

const coverage: IssueCoverage = {
  gaps: [],
  has_plan: false,
  issue: KEY,
  requirements: [
    {
      block_id: "r1",
      checks: 1,
      gaps: [],
      plan_tasks: 0,
      ref: `dispatch://${KEY}/spec#b-r1`,
      text: "The first.",
    },
  ],
  spec_artifact_id: spec.id,
};

/** An agent's decision that waits for a person's review, tied to the statements `shaped`. */
function waitingDecision(blockId: string, shaped: readonly string[]): Decision {
  return {
    asks: [],
    block_id: blockId,
    changed_at: "2026-11-05T10:00:00Z",
    changed_version: 2,
    heading: `Decision ${blockId}`,
    kept: null,
    kind: "agent",
    links: shaped.map((one) => ({
      created_at: "2026-11-05T00:00:00Z",
      created_version: 1,
      from_block: blockId,
      stale: false,
      to: {
        block_id: one,
        changed_at: null,
        changed_by: null,
        id: spec.id,
        kind: "block",
        made_at: null,
        made_by: null,
        parent: null,
        parent_version: null,
        ref: `dispatch://${KEY}/spec#b-${one}`,
      },
    })),
    ref: `dispatch://${KEY}/artifact/decisions-md#b-${blockId}`,
    replaced: false,
    text: `## Decision ${blockId}\n\nThe words of ${blockId}.`,
    text_sha256: "sum",
    unsure: false,
    waiting_review: true,
  };
}

const decisions: IssueDecisions = {
  decisions: [
    waitingDecision("listed", ["r1"]),
    waitingDecision("untied", []),
    waitingDecision("changed", ["r1"]),
  ],
  document: `dispatch://${KEY}/artifact/decisions-md`,
  open_questions: [],
};

/** The Change a person asked of the decision `changed`, after its words last changed. */
const change: Comment = {
  anchor: {
    artifact_id: made.id,
    block_id: "changed-words",
    mark_id: "change-1",
    orphaned: false,
    quote: "The words of changed.",
    version: 3,
  },
  answers: null,
  artifact_id: made.id,
  ask_id: null,
  author: { id: "alice", kind: "user" },
  body: "Say which pages this covers.",
  created_at: "2026-11-05T11:00:00Z",
  deliveries: [],
  edited_at: null,
  id: "change-1",
  issue_key: KEY,
  mentions: [],
  reply_block: null,
  reply_to: null,
  resolved: false,
  resolved_at: null,
  resolved_by: null,
  suggestion: null,
  turn: null,
};

/** Whether `reference` names a statement of the issue, as the header reads it: by the coverage
 *  read's statements, each addressed as the tree addresses it. */
const nodeIdOf = treeContext({ issue }).nodeIdOf;
const statements = new Set(coverage.requirements?.map((one) => nodeIdOf(one.ref)));
const isStatement = (reference: string): boolean => statements.has(nodeIdOf(reference));

/** The id of every node of `root` that waits on the reader. */
function waitingIn(root: TreeNode): string[] {
  return [
    ...(root.waiting ? [root.id] : []),
    ...root.children.flatMap((child) => waitingIn(child)),
  ];
}

// R7, R13, R35, and the tester's F4 and F6: what waits on the reader is each open question whose
// turn is theirs, each decision of the review list - an agent's decision that waits, that a tie
// names a statement for, and that the reader has not asked to change since it changed - and each
// open approval request whose turn is theirs. The tree marks the same question and the same
// decision; an approval request is no node of it. While the login is not known, nothing waits.
test("what waits on the reader is their open questions, the review list and their approval requests", () => {
  const counted = waitingOnReader(issue, "alice", decisions, isStatement, [change]);
  expect(counted).toEqual({ approvals: [spec.id], asks: 1, decisions: 1, total: 3 });

  const root = buildIssueTree({ comments: [change], coverage, decisions, issue, viewer: "alice" });
  // The decisions no question holds are drawn above the conversation (`ROOT_ORDER`).
  expect(waitingIn(root)).toEqual(["decisions-md#b-listed", "mine"]);

  expect(waitingOnReader(issue, undefined, decisions, isStatement, [change]).total).toBe(0);
  expect(waitingIn(buildIssueTree({ comments: [change], coverage, decisions, issue }))).toEqual([]);
});
