import { expect, spyOn, test } from "bun:test";
import { QueryClient } from "@tanstack/react-query";

import { api } from "../../../api/client";
import type {
  Artifact,
  Ask,
  Comment,
  Decision,
  DecisionLink,
  DisplayTree,
  IssueCoverage,
  IssueDetails,
  RequirementCoverage,
} from "../../../api/types";
import { decisionChanges, decisionNodeChanges } from "../decision-review";
import {
  assemble,
  buildIssueTree,
  drawnLine,
  firstLineOf,
  type IssueTreeReads,
  lineWords,
  nodeWords,
  type TreeNode,
} from "../issue-tree";
import { blockNodeId, treeContext } from "../tree-drafts";
import {
  blocksOfRole,
  type TreeDraftMark,
  type TreeWords,
  treeDraftMarks,
  treeDraftsFrom,
  treeIdOf,
} from "./tree-nodes";
import { issueTreeQuery, type TreeAnswer, type TreeAnswerNode, type TreeTuple } from "./tree-read";

/**
 * The tree read's answer as F's drafts (`dispatch://INFRA-4062/artifact/plan-md#b-t4`). Each case
 * writes the answer as the contract describes it (dispatch://INFRA-1253 R8): every node with its
 * `parent`, in the read's walk order, a typed block with the `document` it is written in, and the
 * tuples between them. The tree is built from the drafts as `buildIssueTree` builds it, with
 * `assemble`, F's word rules and F's reading of the issue's names.
 */

const KEY = "CORE-1";

/** A supertag no part's step drafts, so a node of it is drawn by no row whatever later parts add
 *  to `treeDraftSteps` (a lens, a finding, a risk and a constraint are part W's, a criterion and
 *  a reading part Y's). */
const UNDRAWN = "unknown_kind";

function artifact(id: string, slug: string, name: string, primary = false): Artifact {
  return {
    created_at: "2026-11-05T00:00:00Z",
    created_by: { id: "alice", kind: "user" },
    id,
    issue_key: KEY,
    kind: "doc",
    name,
    primary,
    project: "CORE",
    ref_key: `artifact:${id}`,
    session_id: null,
    slug,
    versions: [],
  };
}

const SPEC = artifact("artifact-spec", "spec", "spec.md", true);
const PLAN = artifact("artifact-plan", "plan-md", "plan.md");
const VERIFICATION = artifact("artifact-verification", "verification-md", "verification.md");
const DECISIONS = artifact("artifact-decisions", "decisions-md", "decisions.md");

function issueDetails(): IssueDetails {
  return {
    advances: [],
    advances_revision: 0,
    artifacts: [SPEC, PLAN, VERIFICATION, DECISIONS],
    assignee: null,
    blocked_by: [],
    children: [],
    claim: null,
    closed_at: null,
    components: { ids: [], inherited_from: null, mode: "inherit", reason: null, unknown: [] },
    created_at: "2026-11-05T00:00:00Z",
    created_by: { id: "alice", kind: "user" },
    design_gates: [],
    external_links: [],
    key: KEY,
    labels: [],
    last_seq: 1,
    number: 1,
    open_asks: [],
    parent: null,
    plan_artifact_id: "",
    decisions_artifact_id: "",
    primary_artifact_id: SPEC.id,
    verification_artifact_id: "",
    priority: null,
    project: "CORE",
    progress: { children: null, tasks: null },
    rank: "U",
    referenced_by_count: 0,
    repo: null,
    resolved_repo: null,
    route: null,
    route_holder: null,
    route_status: null,
    route_status_reason: null,
    holder_state: null,
    last_seen: null,
    takeable_at: null,
    status: "todo",
    title: "The issue page draws one ordered body",
    updated_at: "2026-11-05T00:00:00Z",
  };
}

/** The server's reading of a text of one paragraph per string, as a read sends it with
 *  `Dispatch-Trees: 1`. */
function reading(...paragraphs: string[]): DisplayTree {
  return {
    content: paragraphs.map((text) => ({ content: [{ text, type: "text" }], type: "paragraph" })),
    type: "doc",
  };
}

/** One node of the answer, its words stored and read alike unless the case says otherwise. */
function node(
  id: string,
  supertag: string,
  parent: string | null,
  extra: Partial<TreeAnswerNode> = {}
): TreeAnswerNode {
  const text = `Words of ${id}.`;
  return {
    attributes: {},
    author: null,
    id,
    made_at: "2026-11-05T00:00:00Z",
    parent,
    rank: null,
    state: "live",
    supertag,
    text,
    text_tree: reading(text),
    visibility: "project",
    ...extra,
  };
}

/** A typed block of one of the issue's documents, at `position` in it. */
function block(
  id: string,
  supertag: string,
  parent: string,
  document: Artifact,
  position: number,
  extra: Partial<TreeAnswerNode> = {}
): TreeAnswerNode {
  return node(id, supertag, parent, {
    document: { artifact_id: document.id, position, slug: document.slug },
    ...extra,
  });
}

/** The issue's own node, the root every other node hangs from. */
const ISSUE = node(KEY, "issue", null, { text: "The issue page draws one ordered body" });

/** One answer of the tree read, every page merged: the issue's node, then `nodes` in the walk's
 *  order, on an issue that has converted unless the case says otherwise. */
function answer(
  nodes: readonly TreeAnswerNode[],
  tuples: readonly TreeTuple[] = [],
  extra: Partial<TreeAnswer> = {}
): TreeAnswer {
  return {
    converted: true,
    cursor: null,
    nodes: [ISSUE, ...nodes],
    tree: "artifact-tree",
    tuples: [...tuples],
    view_of: null,
    ...extra,
  };
}

function tuple(kind: string, from: string, to: string): TreeTuple {
  return { from, kind, to };
}

/** What `buildIssueTree` hands `treeDraftsFrom`: F's reading of the reads' names, with F's word
 *  rules, the chip's line among them (`chipLine`, `drawnLine(firstLineOf(…))` of the first block
 *  of a reading). */
function naming(reads: IssueTreeReads): TreeWords {
  const chipLine = (tree: DisplayTree): string =>
    drawnLine(firstLineOf(lineWords({ text: "", tree })));
  return { ...treeContext(reads, chipLine), chipLine, lineWords };
}

/** The drafts `tree` holds, and the tree `assemble` builds from them. */
function drawn(tree: TreeAnswer, reads: IssueTreeReads = { issue: issueDetails() }) {
  const context = naming(reads);
  const drafts = treeDraftsFrom(tree, reads, context);
  return { drafts, root: assemble(reads.issue, [...drafts.values()], context.chipOf) };
}

/** The node `id` under `root`, wherever it sits; the first placement of a node drawn twice. */
function nodeOf(root: TreeNode, id: string): TreeNode | undefined {
  if (root.id === id) return root;
  for (const child of root.children) {
    const found = nodeOf(child, id);
    if (found !== undefined) return found;
  }
  return undefined;
}

/** Every placement of `id`, with the node each one sits under. */
function placementsOf(root: TreeNode, id: string): { parent: string; node: TreeNode }[] {
  const found: { parent: string; node: TreeNode }[] = [];
  const walk = (one: TreeNode): void => {
    for (const child of one.children) {
      if (child.id === id) found.push({ node: child, parent: one.id });
      walk(child);
    }
  };
  walk(root);
  return found;
}

/** Every node of the tree, the root first, each before its children. */
function nodesOf(root: TreeNode): TreeNode[] {
  return [root, ...root.children.flatMap(nodesOf)];
}

function requirement(id: string): RequirementCoverage {
  return {
    block_id: id,
    checks: 1,
    gaps: [],
    plan_tasks: 1,
    ref: `dispatch://${KEY}/spec#b-${id}`,
    text: `Words of ${id}.`,
  };
}

function coverage(requirements: readonly RequirementCoverage[]): IssueCoverage {
  return {
    gaps: [],
    has_plan: true,
    issue: KEY,
    requirements: [...requirements],
    spec_artifact_id: SPEC.id,
  };
}

/** One question asked of the issue. */
function question(id: string, extra: Partial<Ask> = {}): Ask {
  return {
    anchor: null,
    answer: null,
    author: { id: "planner", kind: "session" },
    artifact_id: null,
    asked_of: null,
    asked_of_at: null,
    asked_of_by: null,
    block_id: null,
    created_at: "2026-11-05T00:00:00Z",
    edited_at: null,
    id,
    issue_key: KEY,
    kind: "question",
    multiple: false,
    opened_event_id: 1,
    options: [],
    question: `Question ${id}?`,
    state: "open",
    urgency: "med",
    ...extra,
  };
}

/** A decision's link to the node `ref` addresses, as the decisions read answers one. */
function linkTo(ref: string): DecisionLink {
  return {
    created_at: "2026-11-05T00:00:00Z",
    created_version: 1,
    from_block: "d1",
    stale: false,
    to: {
      changed_at: null,
      changed_by: null,
      id: ref,
      kind: "block",
      made_at: null,
      made_by: null,
      parent: null,
      parent_version: null,
      ref,
    },
  };
}

/** A person's comment the read hangs under `parent`, made at `madeAt`. */
function personsComment(id: string, parent: string, madeAt: string): TreeAnswerNode {
  return node(id, "comment", parent, { author: { id: "alice", kind: "user" }, made_at: madeAt });
}

// R1, R2: each node stands under the parent the read answers, and siblings stand in the read's
// order: a requirement the read walks first though its document places it second, and two
// comments in the order the read answers them, though their ids sort the other way.
test("a node stands under its parent, in the read's order", () => {
  const { root } = drawn(
    answer([
      block("r2", "requirement", KEY, SPEC, 1),
      node("c-b", "comment", "r2", { made_at: "2026-11-05T10:00:00Z" }),
      node("c-a", "comment", "r2", { made_at: "2026-11-05T11:00:00Z" }),
      block("r1", "requirement", KEY, SPEC, 0),
      node("m1", "message", KEY),
    ])
  );

  expect(root.children.map((child) => child.id)).toEqual([
    blockNodeId("spec", "r2"),
    blockNodeId("spec", "r1"),
    "m1",
  ]);
  expect(nodeOf(root, blockNodeId("spec", "r2"))?.children.map((child) => child.id)).toEqual([
    "c-b",
    "c-a",
  ]);
});

// R1: a node's words are the server's reading of them (`text_tree`), read by F's word rules, and
// never its stored text, whose marks are no word of it.
test("a node's words come from its text_tree", () => {
  const { root } = drawn(
    answer([
      block("r1", "requirement", KEY, SPEC, 0, {
        text: "**Draw** the tree from `one` read.\n\nThen the rest.",
        text_tree: reading("Draw the tree from one read.", "Then the rest."),
      }),
      node("c1", "comment", KEY, {
        text: "_Agreed_, ship it.",
        text_tree: reading("Agreed, ship it."),
      }),
    ])
  );

  const words = [blockNodeId("spec", "r1"), "c1"].map((id) => {
    const one = nodeOf(root, id);
    return one === undefined ? undefined : [nodeWords(one), lineWords(one)];
  });
  expect(words).toEqual([
    ["Draw the tree from one read. Then the rest.", "Draw the tree from one read."],
    ["Agreed, ship it.", "Agreed, ship it."],
  ]);
});

// R1, R2: on an issue that has not converted, a typed block stands under the parent the read
// answers - a plan task under the question it answers, not under the issue as F drew every block -
// and its `document` member names the document it is written in and its place there, which is
// its order among that document's blocks whatever its place in the walk.
test("an unconverted issue's typed block stands under the parent the read answers, its document and order from its document member", () => {
  const tree = answer(
    [
      node("q1", "ask", KEY),
      block("t2", "plan_task", "q1", PLAN, 1),
      block("r1", "requirement", KEY, SPEC, 0),
      block("t1", "plan_task", "r1", PLAN, 0),
    ],
    [tuple("serves", "t1", "r1")],
    { converted: false, documents: { plan: PLAN.id, spec: SPEC.id }, tree: null }
  );
  const reads = { issue: issueDetails() };
  const { drafts, root } = drawn(tree, reads);
  const second = blockNodeId("plan-md", "t2");

  expect(placementsOf(root, second).map((one) => one.parent)).toEqual(["q1"]);
  expect(placementsOf(root, blockNodeId("plan-md", "t1")).map((one) => one.parent)).toEqual([
    blockNodeId("spec", "r1"),
  ]);
  expect({
    reference: drafts.get(second)?.reference,
    source: drafts.get(second)?.source,
  }).toEqual({
    reference: `dispatch://${KEY}/artifact/plan-md#b-t2`,
    source: { id: PLAN.id, kind: "artifact" },
  });
  expect(blocksOfRole(tree, "plan", reads.issue)?.map((one) => [one.id, one.from])).toEqual([
    ["t1", 0],
    ["t2", 1],
  ]);
});

// R2: a node whose parent is a kind the page does not draw - here a kind no part's step drafts -
// stands under its nearest ancestor the page draws, and under the issue where it has none.
test("a node whose parent is not drawn stands under its nearest drawn ancestor", () => {
  const { root } = drawn(
    answer([
      block("r1", "requirement", KEY, SPEC, 0),
      node("other-1", UNDRAWN, "r1"),
      node("other-2", UNDRAWN, "other-1"),
      personsComment("c1", "other-2", "2026-11-05T00:00:00Z"),
      node("other-3", UNDRAWN, KEY),
      personsComment("c2", "other-3", "2026-11-05T00:00:00Z"),
    ])
  );

  expect(placementsOf(root, "c1").map((one) => one.parent)).toEqual([blockNodeId("spec", "r1")]);
  expect(placementsOf(root, "c2").map((one) => one.parent)).toEqual([KEY]);
});

// R1, root R4: a check stands under the requirement the read answers and again under each other
// requirement its `serves` tuples name, transcluded there; a plan task's `needs` tuple is a chip
// labelled as F labels it; a decision's `shaped` tuple is a chip naming what it shaped by that
// node's line, and places nothing.
test("a tuple between two nodes becomes F's chip and placement", () => {
  const { root } = drawn(
    answer(
      [
        block("r1", "requirement", KEY, SPEC, 0),
        block("c1", "check", "r1", VERIFICATION, 0),
        block("t1", "plan_task", "r1", PLAN, 0),
        block("t2", "plan_task", "r1", PLAN, 1),
        block("r2", "requirement", KEY, SPEC, 1, {
          text_tree: reading("R2. The second statement. Its reason."),
        }),
        node("d1", "decision", KEY, { kept: null, waiting_review: true }),
      ],
      [
        tuple("serves", "c1", "r1"),
        tuple("serves", "c1", "r2"),
        tuple("serves", "t1", "r1"),
        tuple("serves", "t2", "r1"),
        tuple("needs", "t2", "t1"),
        tuple("shaped", "d1", "r2"),
      ]
    ),
    { coverage: coverage([requirement("r1"), requirement("r2")]), issue: issueDetails() }
  );
  const first = blockNodeId("spec", "r1");
  const second = blockNodeId("spec", "r2");

  expect(
    placementsOf(root, blockNodeId("verification-md", "c1")).map((one) => [
      one.parent,
      one.node.transcluded,
      one.node.alsoUnder.map((chip) => chip.ref),
    ])
  ).toEqual([
    [first, false, [`dispatch://${KEY}/spec#b-r2`]],
    [second, true, [`dispatch://${KEY}/spec#b-r1`]],
  ]);
  expect(nodeOf(root, blockNodeId("plan-md", "t2"))?.chips).toEqual([
    {
      answered: true,
      gone: false,
      label: "needs",
      name: "Words of t1.",
      ref: `dispatch://${KEY}/artifact/plan-md#b-t1`,
    },
  ]);
  expect(placementsOf(root, "d1").map((one) => one.parent)).toEqual([KEY]);
  expect(nodeOf(root, "d1")?.chips).toEqual([
    {
      answered: true,
      gone: false,
      name: "R2. The second statement.",
      ref: `dispatch://${KEY}/spec#b-r2`,
      stale: false,
    },
  ]);
});

// R7: a node of a kind no step drafts makes no draft and no row, and the answer the page keeps
// (`reads.tree`) still holds every node whole - a lens, a risk under it and the catalogue's
// constraint among them, each with its state and its visibility - for the parts that draw them.
test("a node of a kind the page does not draw reaches reads.tree and draws no row", () => {
  const reads: IssueTreeReads & { readonly tree: TreeAnswer } = {
    issue: issueDetails(),
    tree: answer(
      [
        block("r1", "requirement", KEY, SPEC, 0),
        node("other-1", UNDRAWN, "r1", { state: "overtaken", visibility: "author" }),
        node("lens-1", "lens", "r1", { visibility: "author" }),
        node("risk-1", "risk", "lens-1", { state: "proposed" }),
        node("constraint-1", "constraint", null, { mark: "context" }),
      ],
      [tuple("constrained_by", "r1", "constraint-1")]
    ),
  };
  const held = structuredClone(reads.tree);
  const root = buildIssueTree(reads);

  expect(nodesOf(root).filter((one) => one.readId === "other-1" || one.id === "other-1")).toEqual(
    []
  );
  expect(reads.tree).toEqual(held);
  expect(reads.tree.nodes.map((one) => [one.id, one.supertag, one.state, one.visibility])).toEqual(
    expect.arrayContaining([
      ["other-1", UNDRAWN, "overtaken", "author"],
      ["lens-1", "lens", "live", "author"],
      ["risk-1", "risk", "proposed", "project"],
      ["constraint-1", "constraint", "live", "project"],
    ])
  );
});

// R1 (plan's map, `id`): a typed block of this issue keeps F's `<slug>#b-<id>` from its
// `document`, an ask F's id from the block the asks read names, a comment, a message and a child
// issue their own ids, the issue its key, and a node of the issue's own tree with no document
// the read's id, so every reader of a node's id reads the tree as it read F's.
test("a block keeps F's id where the read names its document", () => {
  const tree = answer([
    block("r1", "requirement", KEY, SPEC, 0),
    block("c1", "check", "r1", VERIFICATION, 0),
    node("q1", "ask", KEY),
    node("q2", "ask", KEY),
    node("c-1", "comment", KEY),
    node("m-1", "message", KEY),
    node("CORE-2", "issue", KEY, { text: "A child of the issue" }),
    node("d-node", "decision", KEY, { kept: null }),
  ]);
  const reads: IssueTreeReads = {
    asks: [question("q1", { artifact_id: SPEC.id, block_id: "q-block" }), question("q2")],
    issue: issueDetails(),
  };
  const ids = [
    KEY,
    blockNodeId("spec", "r1"),
    blockNodeId("verification-md", "c1"),
    blockNodeId("spec", "q-block"),
    "q2",
    "c-1",
    "m-1",
    "CORE-2",
    "d-node",
  ];

  expect(tree.nodes.map((one) => treeIdOf(tree, reads)(one.id))).toEqual(ids);
  expect([...drawn(tree, reads).drafts.keys()]).toEqual(ids.slice(1));
});

// R1 (plan's map, `id`, decision 130): a block of another issue keeps the read's id even where its
// document carries this issue's slug, as a child's Verification document does, so it is never
// taken for this issue's block; and a check of an issue under this one, which the read reaches
// through its `serves`, is out of this part's scope and draws no row, here or under the
// requirement it serves.
test("another issue's block keeps the read's id where its document has this issue's slug", () => {
  const tree = answer(
    [
      block("r1", "requirement", KEY, SPEC, 0),
      block("c1", "check", "r1", VERIFICATION, 0),
      node("CORE-2", "issue", KEY, { text: "A child of the issue" }),
      node("c9", "check", "CORE-2", {
        document: {
          artifact_id: "artifact-child-verification",
          position: 0,
          slug: "verification-md",
        },
        issue: "CORE-2",
      }),
      node("result-9", "result", "c9", { issue: "CORE-2" }),
    ],
    [tuple("serves", "c1", "r1"), tuple("serves", "c9", "r1")]
  );
  const reads = { issue: issueDetails() };
  const { drafts, root } = drawn(tree, reads);

  expect(treeIdOf(tree, reads)("c9")).toBe("c9");
  expect([...drafts.entries()].map(([id, draft]) => [id, draft.readId])).toEqual([
    [blockNodeId("spec", "r1"), "r1"],
    [blockNodeId("verification-md", "c1"), "c1"],
    ["CORE-2", "CORE-2"],
  ]);
  expect(nodeOf(root, blockNodeId("spec", "r1"))?.children.map((child) => child.id)).toEqual([
    blockNodeId("verification-md", "c1"),
  ]);
});

// R3: a person's comment the read hangs under a decision node, made after the decision's words
// last changed, is its Change: the decision reads Change requested and waits on its agent, not
// on the reader. A person's comment older than its words, and an agent's, stand under it too,
// and neither is a Change, so without the newer one the decision waits for the reader's review.
test("a Change is the comment the read hangs under its decision", () => {
  const decision = node("d1", "decision", KEY, {
    changed_at: "2026-11-05T00:00:00Z",
    kept: null,
    links: [linkTo(`dispatch://${KEY}/spec#b-r1`)],
    replaced: false,
    waiting_review: true,
  });
  const earlier = [
    block("r1", "requirement", KEY, SPEC, 0),
    decision,
    personsComment("c-older", "d1", "2026-11-04T00:00:00Z"),
    node("c-agent", "comment", "d1", {
      author: { id: "planner", kind: "session" },
      made_at: "2026-11-07T00:00:00Z",
    }),
  ];
  const reads: IssueTreeReads = {
    coverage: coverage([requirement("r1")]),
    issue: issueDetails(),
    viewer: "alice",
  };
  const changed = answer([...earlier, personsComment("c-change", "d1", "2026-11-06T00:00:00Z")]);
  const { root } = drawn(changed, reads);

  expect([...decisionNodeChanges(changed)].map(([id, comment]) => [id, comment.id])).toEqual([
    ["d1", "c-change"],
  ]);
  expect(
    ["c-older", "c-agent", "c-change"].map((id) => placementsOf(root, id).map((one) => one.parent))
  ).toEqual([["d1"], ["d1"], ["d1"]]);
  expect(nodeOf(root, "d1")).toMatchObject({ state: "change_requested", waiting: false });

  const unchanged = answer(earlier);
  expect(decisionNodeChanges(unchanged).size).toBe(0);
  expect(nodeOf(drawn(unchanged, reads).root, "d1")).toMatchObject({
    state: "waiting_review",
    waiting: true,
  });
});

// R2's second difference (decision 128): an ask block carrying `shaped` ties draws them as chips
// on its row in the outline `buildIssueTree` draws, each naming a node the ask shaped by that
// node's line, beside how the asks read says it stands.
test("an ask's shaped ties are chips on its row, each naming a node the ask shaped", () => {
  const root = buildIssueTree({
    asks: [question("ask-1", { artifact_id: SPEC.id, block_id: "q1", state: "answered" })],
    issue: issueDetails(),
    tree: answer(
      [
        block("q1", "ask", KEY, SPEC, 0),
        block("r1", "requirement", KEY, SPEC, 1),
        block("r2", "requirement", KEY, SPEC, 2),
      ],
      [tuple("shaped", "q1", "r1"), tuple("shaped", "q1", "r2")]
    ),
  });

  expect(nodeOf(root, blockNodeId("spec", "q1"))).toMatchObject({
    chips: [
      {
        answered: true,
        gone: false,
        name: "Words of r1.",
        ref: `dispatch://${KEY}/artifact/spec#b-r1`,
      },
      {
        answered: true,
        gone: false,
        name: "Words of r2.",
        ref: `dispatch://${KEY}/artifact/spec#b-r2`,
      },
    ],
    reference: `dispatch://${KEY}/ask/ask-1`,
    state: "answered",
  });
});

// R3, decision 65: a decision of today's decisions read - a section of `decisions.md`, which no
// node of the answer holds - finds its Change by its words, with no block of the decisions
// document read: the change is the first decision whose words hold the anchor's quote. The read
// hangs the comment under the issue, since no node holds the decision, and the outline draws it
// under the decision it changes, which reads Change requested; a comment quoting words no
// decision holds is no change and stays where the read hangs it.
test("a decisions.md section finds its Change by its words", () => {
  const section = (id: string, heading: string, words: string): Decision => ({
    asks: [],
    block_id: id,
    changed_at: "2026-11-05T00:00:00Z",
    changed_version: 1,
    heading,
    kept: null,
    kind: "agent",
    links: [],
    ref: `dispatch://${KEY}/artifact/decisions-md#b-${id}`,
    replaced: false,
    text: `## ${heading}\n\n${words}`,
    text_sha256: "sum",
    unsure: false,
    waiting_review: true,
  });
  const anchored = (id: string, quote: string): Comment => ({
    anchor: {
      artifact_id: DECISIONS.id,
      block_id: `${id}-block`,
      mark_id: id,
      orphaned: false,
      quote,
      version: 1,
    },
    answers: null,
    artifact_id: null,
    ask_id: null,
    author: { id: "alice", kind: "user" },
    body: `Change ${id}.`,
    created_at: "2026-11-06T00:00:00Z",
    deliveries: [],
    edited_at: null,
    id,
    issue_key: KEY,
    mentions: [],
    reply_block: null,
    reply_to: null,
    resolved: false,
    resolved_at: null,
    resolved_by: null,
    suggestion: null,
    turn: null,
  });
  const keep = section("d1", "Keep it", "Keep the model pure.");
  const draw = section("d2", "Draw it", "Draw it once.");
  const comments = [
    anchored("c-change", "Draw it once."),
    anchored("c-stray", "Words no decision holds."),
  ];
  const decisions = {
    decisions: [keep, draw],
    document: `dispatch://${KEY}/artifact/decisions-md`,
  };

  const changes = decisionChanges(decisions, comments, issueDetails().artifacts, undefined);
  expect([...changes].map(([ref, comment]) => [ref, comment.id])).toEqual([[draw.ref, "c-change"]]);
  const root = buildIssueTree({
    comments,
    decisions: { ...decisions, open_questions: [] },
    issue: issueDetails(),
    tree: answer(
      comments.map((one) =>
        node(one.id, "comment", KEY, { author: one.author, made_at: one.created_at })
      )
    ),
  });
  expect(
    ["c-change", "c-stray"].map((id) => placementsOf(root, id).map((one) => one.parent))
  ).toEqual([[blockNodeId("decisions-md", "d2")], [KEY]]);
  expect(nodeOf(root, blockNodeId("decisions-md", "d2"))?.state).toBe("change_requested");
  expect(nodeOf(root, blockNodeId("decisions-md", "d1"))?.state).toBe("waiting_review");
});

// R5: a role's blocks come from the tree read in the shape the blocks read answered them: the
// blocks of that role's document in their `document.position` order, each with its attributes -
// a list of pointers as `block[]` - and the asks and comments the read hangs under it; and, on an
// issue that has converted whose blocks name no document, the nodes of this issue the role's
// query takes, in the walk's order.
test("blocksOf answers a role's typed blocks in the blocks read's shape", () => {
  const issue = issueDetails();
  const unconverted = answer(
    [
      block("r2", "requirement", KEY, SPEC, 1, {
        attributes: { serves: [`dispatch://${KEY}/spec#b-r1`] },
      }),
      block("r1", "requirement", KEY, SPEC, 0),
      personsComment("c1", "r1", "2026-11-05T00:00:00Z"),
      node("q1", "ask", "r1"),
      block("t1", "plan_task", KEY, PLAN, 0),
    ],
    [],
    { converted: false, documents: { spec: SPEC.id }, tree: null }
  );

  // Each block carries its words beside the blocks read's members, which `ArtifactBlock` lacks.
  expect<unknown>(blocksOfRole(unconverted, "spec", issue)).toEqual([
    {
      attributes: [],
      from: 0,
      id: "r1",
      references: { asks: 1, comments: 1 },
      text: "Words of r1.",
      text_tree: reading("Words of r1."),
      to: 0,
      token: "",
      type: "requirement",
    },
    {
      attributes: [{ kind: "block[]", name: "serves", value: `dispatch://${KEY}/spec#b-r1` }],
      from: 1,
      id: "r2",
      references: { asks: 0, comments: 0 },
      text: "Words of r2.",
      text_tree: reading("Words of r2."),
      to: 1,
      token: "",
      type: "requirement",
    },
  ]);

  const converted = answer([
    node("criterion-1", "criterion", KEY),
    node("r1", "requirement", "criterion-1"),
    node("r-above", "requirement", "CORE-0", { issue: "CORE-0" }),
  ]);
  expect(blocksOfRole(converted, "spec", issue)?.map((one) => [one.id, one.from])).toEqual([
    ["criterion-1", 1],
    ["r1", 2],
  ]);
});

// The read id and the context a later part's step or mark reads (parts W, X, Y, U): every draft
// carries the answer's own id, which the tree's node keeps on each of its placements, and the
// context answers a node's tree id and its line once every node has been offered, and nothing of
// a node no draft stands for.
test("a draft carries its read id, and the context answers its tree id and its line", () => {
  const tree = answer([
    block("r1", "requirement", KEY, SPEC, 0, {
      text_tree: reading("R1. Draw the tree from one read. Then the rest."),
    }),
    node("other-1", UNDRAWN, "r1"),
    personsComment("c1", "other-1", "2026-11-05T00:00:00Z"),
  ]);
  const seen: unknown[][] = [];
  const probe: TreeDraftMark = (draft, read, _answer, context) => {
    seen.push([
      draft.readId,
      context.treeIdOf(read.id),
      context.lineOf(read.id),
      context.drafted(read.id),
    ]);
    if (read.id === "c1") {
      seen.push([
        context.nodeOf("other-1")?.supertag,
        context.treeIdOf("other-1"),
        context.lineOf("other-1"),
        context.drafted("other-1"),
      ]);
    }
  };
  // A later part adds its mark to the list with one line; this case adds the probe for its run.
  const marks = treeDraftMarks as TreeDraftMark[];
  marks.push(probe);
  try {
    const { root } = drawn(tree);

    expect(seen).toEqual([
      ["r1", blockNodeId("spec", "r1"), "R1. Draw the tree from one read. Then the rest.", true],
      ["c1", "c1", "Words of c1.", true],
      [UNDRAWN, "other-1", undefined, false],
    ]);
    expect([nodeOf(root, blockNodeId("spec", "r1"))?.readId, nodeOf(root, "c1")?.readId]).toEqual([
      "r1",
      "c1",
    ]);
  } finally {
    marks.splice(marks.indexOf(probe), 1);
  }
});

// R1: the tree read asks for its first page, then for the page its cursor names, in turn, and
// answers once the last page has answered, the pages merged, so the tree is built once and never
// from part of the walk: while the second page is out the read holds no answer. The tree then
// holds each node of both pages once, a node of the second page under its parent on the first,
// and a tuple whose near end is on the second page places it.
test("the cursor's pages are read in turn and the tree is built once", async () => {
  const first: TreeAnswer = {
    ...answer([block("r1", "requirement", KEY, SPEC, 0), node("c1", "comment", "r1")]),
    cursor: "page-2",
  };
  const second: TreeAnswer = {
    converted: true,
    cursor: null,
    nodes: [
      block("t1", "plan_task", "r1", PLAN, 0),
      node("c2", "comment", "r1", { made_at: "2026-11-06T00:00:00Z" }),
    ],
    tree: "artifact-tree",
    tuples: [tuple("serves", "t1", "r1")],
    view_of: null,
  };
  // The second page answers when the case releases it, and says when it was asked for.
  const later = Promise.withResolvers<TreeAnswer>();
  const asked = Promise.withResolvers<void>();
  const read = spyOn(api, "getIssueTree").mockImplementation(async (_key, page) => {
    if (page?.cursor === undefined) return first;
    asked.resolve();
    return later.promise;
  });
  try {
    const client = new QueryClient();
    const query = issueTreeQuery(KEY);
    const reading = client.fetchQuery(query);
    await asked.promise;
    expect(read.mock.calls.map(([key, page]) => [key, page])).toEqual([
      [KEY, {}],
      [KEY, { cursor: "page-2" }],
    ]);
    expect(read.mock.calls.every(([, , init]) => init?.signal instanceof AbortSignal)).toBe(true);
    expect(client.getQueryData(query.queryKey)).toBeUndefined();

    later.resolve(second);
    const root = buildIssueTree({ issue: issueDetails(), tree: await reading });
    expect(read).toHaveBeenCalledTimes(2);
    expect(nodesOf(root).map((one) => one.id)).toEqual([
      KEY,
      blockNodeId("spec", "r1"),
      "c1",
      blockNodeId("plan-md", "t1"),
      "c2",
    ]);
  } finally {
    read.mockRestore();
  }
});

// INFRA-1196 decision 181: the tree read honours TanStack's abort signal, so a read given up - a
// reset, the last observer gone - asks for no next page, and a walk of many pages stops there.
test("a read given up asks for no next page", async () => {
  const first: TreeAnswer = {
    ...answer([block("r1", "requirement", KEY, SPEC, 0)]),
    cursor: "page-2",
  };
  const out = Promise.withResolvers<TreeAnswer>();
  const read = spyOn(api, "getIssueTree").mockImplementation(async (_key, page) =>
    page?.cursor === undefined ? out.promise : answer([])
  );
  try {
    const client = new QueryClient();
    const query = issueTreeQuery(KEY);
    const reading = client.fetchQuery(query).catch(() => undefined);
    await client.cancelQueries({ queryKey: query.queryKey });
    await reading;
    out.resolve(first);
    // The walk goes on in a promise TanStack no longer hands out, so the case waits for the turn
    // of the event loop after it: every step of the walk up to its next request is a microtask.
    const turn = Promise.withResolvers<void>();
    setTimeout(turn.resolve, 0);
    await turn.promise;
    expect(read).toHaveBeenCalledTimes(1);
    expect(client.getQueryData(query.queryKey)).toBeUndefined();
  } finally {
    read.mockRestore();
  }
});
