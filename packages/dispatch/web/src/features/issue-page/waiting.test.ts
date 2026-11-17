import { expect, test } from "bun:test";

import type {
  Artifact,
  Comment,
  Decision,
  DecisionKeep,
  DisplayTree,
  IssueCoverage,
  IssueDetails,
} from "../../api/types";
import { wordsOf } from "../refs/tree-fragment";
import { decisionNodes } from "./decision-nodes/decision-nodes";
import { anchorQuote, changeAnchor } from "./decision-review";
import { buildIssueTree, type TreeNode } from "./issue-tree";
import { treeContext } from "./tree-drafts";
import type { TreeAnswer, TreeAnswerNode } from "./tree-read/tree-read";
import { waitingOnReader } from "./waiting";

/**
 * What waits on the reader, read two ways from the same reads: the header's count
 * (`waitingOnReader`) and the tree's own `waiting` on each node (dispatch://INFRA-1196 R7, R13,
 * R35). The two must agree, or the count puts the focus on a node that does not wait, or counts
 * one it can never reach. Both read the tree read's answer, which each case writes as the
 * contract gives it (dispatch://INFRA-1253 R8), and its decisions are that answer's decision
 * nodes (`decisionNodes`).
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
  artifacts: [spec],
  assignee: "alice",
  blocked_by: [],
  children: [],
  claim: null,
  closed_at: null,
  components: { ids: [], inherited_from: null, mode: "inherit", reason: null, unknown: [] },
  created_at: "2026-11-05T00:00:00Z",
  created_by: { id: "alice", kind: "user" },
  decisions_artifact_id: "",
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

/** The issue with no open ask, so a case's count is its decisions' alone. */
const quiet: IssueDetails = { ...issue, open_asks: [] };

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

/** A person's comment on the decision `changed`, anchored to its words, after they last
 *  changed. */
const change: Comment = {
  anchor: {
    artifact_id: spec.id,
    block_id: "changed",
    mark_id: "change-1",
    orphaned: false,
    quote: "The words of changed.",
    version: 3,
  },
  answers: null,
  artifact_id: spec.id,
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

/** One node of the tree read's answer, under the issue unless the case gives it another parent,
 *  with nothing on it but what the case gives it. */
function treeNode(
  id: string,
  supertag: string,
  extra: Partial<TreeAnswerNode> = {}
): TreeAnswerNode {
  return {
    attributes: {},
    author: null,
    id,
    made_at: "2026-11-05T00:00:00Z",
    parent: KEY,
    rank: null,
    state: "live",
    supertag,
    text: "",
    visibility: "project",
    ...extra,
  };
}

/** The tree read's answer for the issue: its own node, then `nodes` in the read's order. */
function answerOf(nodes: readonly TreeAnswerNode[]): TreeAnswer {
  return {
    converted: false,
    cursor: null,
    nodes: [treeNode(KEY, "issue", { parent: null }), ...nodes],
    tree: null,
    tuples: [],
    view_of: null,
  };
}

/** The spec's statement `r1` as the tree read answers it, a typed block of the spec. */
const statement = treeNode("r1", "requirement", {
  document: { artifact_id: spec.id, position: 0, slug: "spec" },
  text: "The first.",
});

/** The issue's open asks as the tree read answers them, each under the issue. */
const askNodes = (issue.open_asks ?? []).map((ask) =>
  treeNode(ask.id, "ask", { author: ask.author, made_at: ask.created_at, text: ask.question })
);

/** A person's comment as the tree read answers it, under the node `parent`. */
function commentNode(comment: Comment, parent: string): TreeAnswerNode {
  return treeNode(comment.id, "comment", {
    author: comment.author,
    made_at: comment.created_at,
    parent,
    text: comment.body,
  });
}

/** An agent's decision as the tree read answers its node: a decision block of the spec, with the
 *  members the read answers on it (`waitingDecision`'s) and the words `The words of <id>.`,
 *  unless the case gives it others. */
function decisionNode(
  id: string,
  shaped: readonly string[],
  extra: Partial<TreeAnswerNode> = {}
): TreeAnswerNode {
  const row = waitingDecision(id, shaped);
  return treeNode(id, "decision", {
    asks: row.asks,
    changed_at: row.changed_at,
    changed_version: row.changed_version,
    document: { artifact_id: spec.id, position: 1, slug: "spec" },
    kept: row.kept,
    kind: row.kind,
    links: row.links,
    replaced: row.replaced,
    text: `The words of ${id}.`,
    text_sha256: row.text_sha256,
    waiting_review: row.waiting_review,
    ...extra,
  });
}

/** Whether `reference` names a statement of the issue, as the header reads it: by the coverage
 *  read's statements, each addressed as the tree addresses it, and none while that read has not
 *  answered. */
const nodeIdOf = treeContext({ issue }).nodeIdOf;
function statementsOf(read: IssueCoverage | undefined): (reference: string) => boolean {
  const statements = new Set(read?.requirements?.map((one) => nodeIdOf(one.ref)));
  return (reference) => statements.has(nodeIdOf(reference));
}
const isStatement = statementsOf(coverage);

/** The decisions the count of `about` is handed over `tree`: its decision nodes, as the header
 *  reads them (`IssuePropertiesRow.tsx`). */
function decisionsOf(tree: TreeAnswer, about: IssueDetails = issue) {
  return decisionNodes(tree, { issue: about });
}

/** How many decisions wait on alice over `tree`, in the count, and the id of each node of the
 *  tree that waits on her, with the statements `read` holds: none while it has not answered. */
function waits(
  tree: TreeAnswer,
  read: IssueCoverage | undefined,
  about: IssueDetails = quiet
): readonly [number, readonly string[]] {
  return [
    waitingOnReader(about, "alice", decisionsOf(tree, about), statementsOf(read), tree).decisions,
    waitingIn(
      buildIssueTree({
        ...(read === undefined ? {} : { coverage: read }),
        issue: about,
        tree,
        viewer: "alice",
      })
    ),
  ];
}

/** The id of every node of `root` that waits on the reader. */
function waitingIn(root: TreeNode): string[] {
  return [
    ...(root.waiting ? [root.id] : []),
    ...root.children.flatMap((child) => waitingIn(child)),
  ];
}

/** The id of each node of `root` that a placement of the node `id` sits under. */
function parentsOf(root: TreeNode, id: string): string[] {
  return root.children.flatMap((child) => [
    ...(child.id === id ? [root.id] : []),
    ...parentsOf(child, id),
  ]);
}

/** A run of text of a reading (`Decision.text_tree`), as the server writes one. */
function run(text: string): DisplayTree {
  return { text, type: "text" };
}

/** A paragraph of a reading, holding `text`. */
function paragraph(text: string): DisplayTree {
  return { content: [run(text)], type: "paragraph" };
}

/** A heading of a reading, of level `level`, holding `text`. */
function headingNode(level: number, text: string): DisplayTree {
  return { attrs: { level }, content: [run(text)], type: "heading" };
}

/** A bullet list of a reading, each item one paragraph holding its words. */
function bulletList(items: readonly string[]): DisplayTree {
  return {
    content: items.map((item) => ({
      attrs: { spread: false },
      content: [paragraph(item)],
      type: "list_item",
    })),
    type: "bullet_list",
  };
}

/** A decision whose section is the heading `heading` and then `markdown`, with the server's
 *  reading of it (`text_tree`): the heading, then the blocks `body`. */
function readDecision(
  blockId: string,
  heading: string,
  markdown: string,
  body: readonly DisplayTree[]
): Decision {
  return {
    ...waitingDecision(blockId, []),
    heading,
    text: `## ${heading}\n\n${markdown}`,
    text_tree: {
      content: [headingNode(2, heading), ...body],
      type: "doc",
    },
  };
}

/** The top-level nodes `row`'s section adds to the reading of the whole decisions document: its
 *  heading, at `level` (`##`, or `###` under `## Replaced`), then the blocks of its own reading
 *  below its heading - the very nodes, so `changePlaces` finds its first block among them. */
function sectionOf(row: Decision, level = 2): DisplayTree[] {
  const reading = row.text_tree?.content;
  return [headingNode(level, row.heading), ...(Array.isArray(reading) ? reading.slice(1) : [])];
}

/** The server's reading of a whole decisions document (`markdown_tree` of its text read): its
 *  title, the paragraph `intro` below it, then `nodes`. */
function decisionsDocument(intro: string, nodes: readonly DisplayTree[]): DisplayTree {
  return {
    content: [headingNode(1, "Decisions of CORE-1"), paragraph(intro), ...nodes],
    type: "doc",
  };
}

/** The first block below its heading of `row`'s own reading: the block a Change of it quotes. */
function firstBlockOf(row: Decision): DisplayTree {
  const reading = row.text_tree?.content;
  const block: DisplayTree | undefined = Array.isArray(reading)
    ? reading.find((node: DisplayTree) => node.type !== "heading")
    : undefined;
  if (block === undefined) throw new Error(`${row.block_id} holds no block below its heading`);
  return block;
}

/** Where the server puts a Change of each of `rows` written with the anchor `changeAnchor` gives
 *  it over `document`, the reading of the whole decisions document (`chosen`), and where that
 *  row's own first block below its heading starts (`own`), both in the document's text as
 *  `pmdoc.FindQuote` reads it: the words of `document`. The server lists every match of the
 *  quote, overlapping ones included, and takes the one the occurrence names; an anchor with no
 *  occurrence chooses nothing here. A row's first block is one of `document`'s own top-level nodes
 *  (`sectionOf`), and its words are the last of the document's words up to and including it. */
function changePlaces(
  rows: readonly Decision[],
  document: DisplayTree
): { chosen: (number | undefined)[]; own: number[] } {
  const text = wordsOf(document);
  const nodes: readonly DisplayTree[] = Array.isArray(document.content) ? document.content : [];
  const chosen = rows.map((row) => {
    const anchor = changeAnchor(row, rows, document);
    if (anchor?.occurrence === undefined) return undefined;
    const matches: number[] = [];
    for (let at = text.indexOf(anchor.quote); at !== -1; at = text.indexOf(anchor.quote, at + 1)) {
      matches.push(at);
    }
    return matches[anchor.occurrence];
  });
  const own = rows.map((row) => {
    const block = firstBlockOf(row);
    const upTo = nodes.slice(0, nodes.indexOf(block) + 1);
    return wordsOf({ ...document, content: upTo }).length - wordsOf(block).length;
  });
  return { chosen, own };
}

// R7, R13, R35, and the tester's F4 and F6: what waits on the reader is each open question whose
// turn is theirs, each decision of the review list - an agent's decision that waits, that a tie
// names a statement for, and that the reader has not asked to change since it changed - and each
// open approval request whose turn is theirs. The tree marks the same question and the same
// decision; an approval request is no node of it. While the login is not known, nothing waits.
test("what waits on the reader is their open questions, the review list and their approval requests", () => {
  const tree = answerOf([
    statement,
    ...askNodes,
    decisionNode("listed", ["r1"]),
    decisionNode("untied", []),
    decisionNode("changed", ["r1"]),
    commentNode(change, "changed"),
  ]);
  const decisions = decisionsOf(tree);
  const counted = waitingOnReader(issue, "alice", decisions, isStatement, tree);
  expect(counted).toEqual({ approvals: [spec.id], asks: 1, decisions: 1, total: 3 });
  expect(waitingIn(buildIssueTree({ coverage, issue, tree, viewer: "alice" }))).toEqual([
    "mine",
    "spec#b-listed",
  ]);

  const unknown = waitingOnReader(issue, undefined, decisions, isStatement, tree);
  expect(unknown.total).toBe(0);
  expect(waitingIn(buildIssueTree({ coverage, issue, tree }))).toEqual([]);
});

// R7, R13, R35: a decision the count counts is a decision node of the tree read, by the review
// state the read answers on it: one that waits for a person's review and that a link ties to a
// statement. One kept as it reads now, one replaced, and one that waits but is tied to nothing
// do not count, and the tree marks the same one waiting. Before the tree read answers there is
// no decision to count.
test("waitingOnReader counts a waiting decision node", () => {
  const keep: DecisionKeep = {
    artifact_id: spec.id,
    block_id: "kept",
    changed_version: 2,
    kept_at: "2026-11-05T12:00:00Z",
    kept_by: "alice",
  };
  const tree = answerOf([
    statement,
    decisionNode("waits", ["r1"]),
    decisionNode("kept", ["r1"], { kept: keep, waiting_review: false }),
    decisionNode("replaced", ["r1"], { replaced: true, waiting_review: false }),
    decisionNode("untied", []),
  ]);

  expect(waitingOnReader(quiet, "alice", decisionsOf(tree, quiet), isStatement, tree)).toEqual({
    approvals: [],
    asks: 0,
    decisions: 1,
    total: 1,
  });
  expect(waits(tree, coverage)).toEqual([1, ["spec#b-waits"]]);
  expect(waitingOnReader(quiet, "alice", [], isStatement, undefined).decisions).toBe(0);
});

// R3, R13: a Change is the comment the tree read hangs under its decision, which is where the
// read hangs a comment anchored inside the decision's words. A person's comment anchored to the
// second decision's words that the read hangs elsewhere - under the issue, or under the first
// decision, whose words hold the same sentence - is no Change of it, and the second decision
// still waits on the reader; once the read hangs it under the second decision, that decision
// waits on its agent, in the count as in the tree. No quote is matched for it.
test("a Change is only the comment under its decision", () => {
  const first = decisionNode("d1", [], { text: "Keep the model pure. Draw it once." });
  const second = decisionNode("d2", ["r1"], { text: "Draw it once." });
  const asked: Comment = {
    ...change,
    anchor: {
      artifact_id: spec.id,
      block_id: "d2",
      mark_id: "c-asked",
      orphaned: false,
      quote: "Draw it once.",
      version: 3,
    },
    id: "c-asked",
  };
  const hungUnder = (parent: string) => {
    const tree = answerOf([statement, first, second, commentNode(asked, parent)]);
    const root = buildIssueTree({ coverage, issue: quiet, tree, viewer: "alice" });
    return { drawnUnder: parentsOf(root, asked.id), waits: waits(tree, coverage) };
  };

  expect(hungUnder(KEY)).toEqual({ drawnUnder: [KEY], waits: [1, ["spec#b-d2"]] });
  expect(hungUnder("d1")).toEqual({ drawnUnder: ["spec#b-d1"], waits: [1, ["spec#b-d2"]] });
  expect(hungUnder("d2")).toEqual({ drawnUnder: ["spec#b-d2"], waits: [0, []] });
});

// R3 and choice 1 of dispatch://INFRA-4097's plan: a person's comment under a decision is a
// Change only while it is newer than the decision's words and than the Keep the read answers on
// it. One made before the words last changed asked for a change they have since had, and one
// made before that Keep was answered by it, so the decision waits on the reader again; one newer
// than both is a Change, and the decision waits on its agent, in the count as in the tree.
test("a Change older than the decision's last change is no Change", () => {
  // The decision's words last changed at 10:00 (`waitingDecision`).
  const keptAt = (at: string): Partial<TreeAnswerNode> => ({
    kept: { artifact_id: spec.id, block_id: "d1", changed_version: 2, kept_at: at, kept_by: "bob" },
  });
  const madeAt = (at: string, extra: Partial<TreeAnswerNode> = {}) =>
    waits(
      answerOf([
        statement,
        decisionNode("d1", ["r1"], extra),
        commentNode({ ...change, created_at: at }, "d1"),
      ]),
      coverage
    );

  expect(madeAt("2026-11-05T09:00:00Z")).toEqual([1, ["spec#b-d1"]]);
  expect(madeAt("2026-11-05T11:00:00Z")).toEqual([0, []]);
  expect(madeAt("2026-11-05T11:00:00Z", keptAt("2026-11-05T12:00:00Z"))).toEqual([
    1,
    ["spec#b-d1"],
  ]);
  expect(madeAt("2026-11-05T13:00:00Z", keptAt("2026-11-05T12:00:00Z"))).toEqual([0, []]);
});

// R7, R13 and R35: a statement is a requirement the coverage read holds, as the header's count is
// handed the statements. A decision tied only to a `requirement` block of the plan, which no
// coverage row holds, is on no review list; nor is one tied to the spec's `r1` while the coverage
// read has not answered, though the tree read answers that block already. The tree marks neither
// and the count counts neither, and the decision tied to `r1` once the coverage read holds it
// waits, in the tree as in the count.
test("a tie to a requirement the coverage read does not hold puts no decision on the review list", () => {
  const plan = issueDocument("artifact-plan", "plan-md");
  const planned: IssueDetails = { ...quiet, artifacts: [spec, plan], plan_artifact_id: plan.id };
  const tiedToPlan = decisionNode("planned", ["p1"]);
  const tree = answerOf([
    statement,
    treeNode("p1", "requirement", {
      document: { artifact_id: plan.id, position: 0, slug: "plan-md" },
    }),
    decisionNode("listed", ["r1"]),
    {
      ...tiedToPlan,
      links: (tiedToPlan.links ?? []).map((link) => ({
        ...link,
        to: { ...link.to, id: plan.id, ref: `dispatch://${KEY}/artifact/plan-md#b-p1` },
      })),
    },
  ]);

  expect(waits(tree, coverage, planned)).toEqual([1, ["spec#b-listed"]]);
  expect(waits(tree, undefined, planned)).toEqual([0, []]);
});

// R13: a decision node no document holds has no address and no document a Keep could be written
// against, so it is no decision of the page's (`decisionNodes`): though the read answers it
// waiting and tied to a statement, the count does not count it and the tree does not mark it
// waiting.
test("a decision node no document holds waits on nobody", () => {
  const tree = answerOf([statement, decisionNode("loose", ["r1"], { document: undefined })]);

  expect(decisionsOf(tree)).toEqual([]);
  expect(waits(tree, coverage)).toEqual([0, []]);
});

/** An intro of the decisions document that holds none of the quotes below. */
const PLAIN_INTRO = "The decisions this issue made.";

// Decision 92 of dispatch://INFRA-1196: a Change is a comment anchored to the decision's first
// block below its heading, and the server refuses a quote that matches more than one place in the
// decisions document unless the write names which one. Here the first and the third decision open
// with the same sentence: each Change names the occurrence that is its own block's, counted over
// the whole document, and the server, choosing among the quote's matches as `pmdoc.FindQuote`
// does, puts each Change on its own decision's block. Where the document's intro quotes that
// sentence too, the intro's match comes first and moves both decisions' occurrences up by one. An
// occurrence of 0 for every decision would put the third's Change on the first decision. A
// decision whose heading is all it holds has no block to anchor a Change to.
test("a Change of a decision whose first sentence another decision also opens with names its own occurrence", () => {
  const first = readDecision("d1", "Purity", "Keep the model pure.\n\nDraw it once.", [
    paragraph("Keep the model pure."),
    paragraph("Draw it once."),
  ]);
  const second = readDecision("d2", "One read", "Read it from one place.", [
    paragraph("Read it from one place."),
  ]);
  const third = readDecision("d3", "Purity again", "Keep the model pure.\n\nTest it alone.", [
    paragraph("Keep the model pure."),
    paragraph("Test it alone."),
  ]);
  const rows = [first, second, third];
  const sections = rows.flatMap((row) => sectionOf(row));

  for (const [intro, occurrences] of [
    [PLAIN_INTRO, [0, 0, 1]],
    ["Keep the model pure.", [1, 0, 2]],
  ] as const) {
    const document = decisionsDocument(intro, sections);
    expect(rows.map((row) => changeAnchor(row, rows, document))).toEqual([
      { occurrence: occurrences[0], quote: "Keep the model pure." },
      { occurrence: occurrences[1], quote: "Read it from one place." },
      { occurrence: occurrences[2], quote: "Keep the model pure." },
    ]);
    const places = changePlaces(rows, document);
    expect(places.chosen).toEqual(places.own);
  }

  const headingOnly = readDecision("d4", "Heading only", "", []);
  expect(changeAnchor(headingOnly, rows, decisionsDocument(PLAIN_INTRO, sections))).toBeUndefined();
});

// Decision 92 of dispatch://INFRA-1196 as amended: the server counts a quote's places over the
// whole decisions document, the text above its first decision included, which no row of the
// decisions read holds. Here the intro quotes the first decision's opening sentence at the end of
// its own words: that is the quote's first match, so the first decision's own block is its
// second, occurrence 1. A count that began at the first decision's heading would name 0, and the
// server would put the Change on the intro, outside every decision.
test("a Change counts the quote's match in the decisions document's intro above the first decision", () => {
  const first = readDecision("d1", "Purity", "Keep the model pure.", [
    paragraph("Keep the model pure."),
  ]);
  const second = readDecision("d2", "One read", "Read it from one place.", [
    paragraph("Read it from one place."),
  ]);
  const rows = [first, second];
  const document = decisionsDocument(
    "Each decision below serves one rule. Keep the model pure.",
    rows.flatMap((row) => sectionOf(row))
  );

  expect(changeAnchor(first, rows, document)).toEqual({
    occurrence: 1,
    quote: "Keep the model pure.",
  });
  expect(changeAnchor(second, rows, document)).toEqual({
    occurrence: 0,
    quote: "Read it from one place.",
  });
  const places = changePlaces(rows, document);
  expect(places.chosen).toEqual(places.own);
});

// Decision 92 of dispatch://INFRA-1196 as amended: the server counts over the whole document,
// the `## Replaced` section and a `#` section included, and the decisions read holds every
// decision in document order, the replaced ones in their place. The decisions are found by the
// server's section rule: a `##` heading outside `## Replaced`, a `###` one under it, and a heading
// inside an open decision that does not close it - "Purity"'s own `### Why` - is part of it, no
// decision. Here "Purity" opens with "Keep the model pure."; "Purity, first draft", replaced,
// opens with it under `## Replaced`; a `#` section after it quotes it; and "Purity again", after
// that section, opens with it too. With the intro quoting it as well, the three decisions name
// occurrences 1, 2 and 4, the `#` section's match being the fourth, and the server puts each
// Change on its own decision's block.
test("a Change counts the quote's matches in a replaced decision and in a # section", () => {
  const purity = readDecision("d1", "Purity", "Keep the model pure.\n\n### Why\n\nOne read.", [
    paragraph("Keep the model pure."),
    headingNode(3, "Why"),
    paragraph("One read draws it."),
  ]);
  const firstDraft: Decision = {
    ...readDecision("d2", "Purity, first draft", "Keep the model pure.\n\nDraw it twice.", [
      paragraph("Keep the model pure."),
      paragraph("Draw it twice."),
    ]),
    replaced: true,
  };
  const again = readDecision("d3", "Purity again", "Keep the model pure.\n\nTest it alone.", [
    paragraph("Keep the model pure."),
    paragraph("Test it alone."),
  ]);
  const rows = [purity, firstDraft, again];
  const document = decisionsDocument("Each decision below serves one rule. Keep the model pure.", [
    ...sectionOf(purity),
    headingNode(2, "Replaced"),
    ...sectionOf(firstDraft, 3),
    headingNode(1, "Appendix"),
    paragraph("Keep the model pure. Every decision above says so."),
    ...sectionOf(again),
  ]);

  expect(rows.map((row) => changeAnchor(row, rows, document))).toEqual([
    { occurrence: 1, quote: "Keep the model pure." },
    { occurrence: 2, quote: "Keep the model pure." },
    { occurrence: 4, quote: "Keep the model pure." },
  ]);
  const places = changePlaces(rows, document);
  expect(places.chosen).toEqual(places.own);
});

// Decision 92 of dispatch://INFRA-1196: the server's text holds one space between two list items
// (`buildFlattenedText`), so a decision whose first block below its heading is a list is quoted by
// the list's words, its items one space apart, and the same words in an earlier decision's list
// are a match above it. The second decision's list holds "Hold the deploy." and "Then ship.":
// where the first decision's list holds only "Hold the deploy.", the quote matches nowhere in it;
// where it holds both items, the quote matches there first and the occurrence is one more. An
// intro quoting the list's words adds one more again. Either way the server puts the Change on
// the second decision's list.
test("a Change of a decision whose first block is a list counts the same words inside an earlier decision's list item", () => {
  const second = readDecision("d2", "Ship after the hold", "- Hold the deploy.\n- Then ship.", [
    bulletList(["Hold the deploy.", "Then ship."]),
  ]);
  expect(anchorQuote(second)).toBe("Hold the deploy. Then ship.");

  for (const [items, inFirst] of [
    [["Hold the deploy."], 0],
    [["Hold the deploy.", "Then ship."], 1],
  ] as const) {
    const first = readDecision("d1", "Freeze", items.map((item) => `- ${item}`).join("\n"), [
      bulletList(items),
    ]);
    const rows = [first, second];
    for (const [intro, inIntro] of [
      [PLAIN_INTRO, 0],
      ["Hold the deploy. Then ship.", 1],
    ] as const) {
      const document = decisionsDocument(
        intro,
        rows.flatMap((row) => sectionOf(row))
      );
      expect(changeAnchor(second, rows, document)).toEqual({
        occurrence: inIntro + inFirst,
        quote: "Hold the deploy. Then ship.",
      });
      const places = changePlaces(rows, document);
      expect(places.chosen).toEqual(places.own);
    }
  }
});

// Decision 92 of dispatch://INFRA-1196: the server lists every match of a quote, overlapping ones
// included (`findAll`), so a match that starts in a decision's heading and runs into its first
// block is listed before the block's own. The second decision's heading ends with "go" and its
// first paragraph is "go go go": its words, "Ready, set, go go go go", hold the quote where the
// heading's "go" starts and again at the paragraph, and the first decision's "go go go go" holds
// it twice more, overlapping. The paragraph's own match is the fourth, occurrence 3; in a document
// holding no decision above it, the second. An intro of "go go go" adds one match above each.
test("a Change counts a match that starts in the decision's heading and runs into its first block", () => {
  const first = readDecision("d1", "Chant", "go go go go", [paragraph("go go go go")]);
  const second = readDecision("d2", "Ready, set, go", "go go go", [paragraph("go go go")]);
  const rows = [first, second];

  for (const [intro, inIntro] of [
    [PLAIN_INTRO, 0],
    ["go go go", 1],
  ] as const) {
    const document = decisionsDocument(
      intro,
      rows.flatMap((row) => sectionOf(row))
    );
    expect(changeAnchor(second, rows, document)).toEqual({
      occurrence: 3 + inIntro,
      quote: "go go go",
    });
    const places = changePlaces(rows, document);
    expect(places.chosen).toEqual(places.own);
    const alone = decisionsDocument(intro, sectionOf(second));
    expect(changeAnchor(second, [second], alone)).toEqual({
      occurrence: 1 + inIntro,
      quote: "go go go",
    });
  }
});

// The occurrence is counted only where the decisions document's reading lines up with the
// decisions read: the heading the server's section rule finds at the decision's own index in the
// read reads as the decision's heading, and the node right below it is a block that reads as the
// quote. Where the reading is not in hand, where it is of another version than the read - a
// decision the read holds that the reading lacks, a heading or a first block worded otherwise -
// where a sub-heading stands right below the decision's heading, or where the read does not hold
// the decision, the anchor is the quote alone: the server anchors it where the quote stands once
// and refuses it where the quote stands more than once, rather than land it on another place.
test("a Change names no occurrence where the decisions document's reading does not line up with the decisions read", () => {
  const first = readDecision("d1", "Purity", "Keep the model pure.", [
    paragraph("Keep the model pure."),
  ]);
  const second = readDecision("d2", "One read", "Read it from one place.", [
    paragraph("Read it from one place."),
  ]);
  const third = readDecision("d3", "Purity again", "Keep the model pure.\n\nTest it alone.", [
    paragraph("Keep the model pure."),
    paragraph("Test it alone."),
  ]);
  const rows = [first, second, third];
  const intro = "Keep the model pure.";
  const quoteAlone = { quote: "Keep the model pure." };
  const sectionsWith = (thirdSection: readonly DisplayTree[]): DisplayTree =>
    decisionsDocument(intro, [...sectionOf(first), ...sectionOf(second), ...thirdSection]);

  expect(changeAnchor(third, rows, sectionsWith(sectionOf(third)))).toEqual({
    occurrence: 2,
    quote: "Keep the model pure.",
  });

  expect(changeAnchor(third, rows, undefined)).toStrictEqual(quoteAlone);
  const lacking = decisionsDocument(intro, [...sectionOf(first), ...sectionOf(third)]);
  expect(changeAnchor(third, rows, lacking)).toStrictEqual(quoteAlone);
  expect(changeAnchor(second, rows, lacking)).toStrictEqual({ quote: "Read it from one place." });
  const renamed = sectionsWith([
    headingNode(2, "Purity, again"),
    paragraph("Keep the model pure."),
    paragraph("Test it alone."),
  ]);
  expect(changeAnchor(third, rows, renamed)).toStrictEqual(quoteAlone);
  const reworded = sectionsWith([
    headingNode(2, "Purity again"),
    paragraph("Keep the model simple."),
    paragraph("Test it alone."),
  ]);
  expect(changeAnchor(third, rows, reworded)).toStrictEqual(quoteAlone);

  const underWhy = readDecision("d3", "Purity again", "### Why\n\nKeep the model pure.", [
    headingNode(3, "Why"),
    paragraph("Keep the model pure."),
  ]);
  expect(anchorQuote(underWhy)).toBe("Keep the model pure.");
  expect(
    changeAnchor(underWhy, [first, second, underWhy], sectionsWith(sectionOf(underWhy)))
  ).toStrictEqual(quoteAlone);

  const document = sectionsWith(sectionOf(third));
  expect(changeAnchor(third, [first, second], document)).toStrictEqual(quoteAlone);
  expect(changeAnchor(third, undefined, document)).toStrictEqual(quoteAlone);
});

/** The reading of a decisions document whose intro quotes "Keep the model pure.", and whose
 *  decisions "Purity" and "Purity again" each open with it. */
const PURITY_DOCUMENT = decisionsDocument("Keep the model pure.", [
  headingNode(2, "Purity"),
  paragraph("Keep the model pure."),
  headingNode(2, "Purity again"),
  paragraph("Keep the model pure."),
  paragraph("Test it alone."),
]);

// Decision 92 of dispatch://INFRA-1196, from a pod of the build before during a rolling deploy,
// whose read sends no reading of a decision's text: the quote is then the first paragraph below
// the heading as markdown (`anchorQuote`), and the count still comes from the document's reading.
// Where that markdown is the paragraph's words, the reading lines up, and the two decisions
// opening with the paragraph name its places after the intro's. Where the markdown holds markup -
// `**` - the paragraph's words do not read as the quote, so the anchor names no occurrence: the
// server finds such a quote only by reading it as markdown, which this count does not mirror.
test("a Change of a decision read with no reading names an occurrence only where its markdown is the document's words", () => {
  const worded = (blockId: string, heading: string, text: string): Decision => ({
    ...waitingDecision(blockId, []),
    heading,
    text,
  });
  const first = worded("d1", "Purity", "## Purity\n\nKeep the model pure.");
  const second = worded(
    "d2",
    "Purity again",
    "## Purity again\n\nKeep the model pure.\n\nTest it alone."
  );
  const rows = [first, second];

  expect(rows.map((row) => changeAnchor(row, rows, PURITY_DOCUMENT))).toEqual([
    { occurrence: 1, quote: "Keep the model pure." },
    { occurrence: 2, quote: "Keep the model pure." },
  ]);

  const marked = worded("d1", "Purity", "## Purity\n\n**Keep** the model pure.");
  expect(anchorQuote(marked)).toBe("**Keep** the model pure.");
  const strong = decisionsDocument(PLAIN_INTRO, [
    headingNode(2, "Purity"),
    {
      content: [
        { marks: [{ type: "strong" }], text: "Keep", type: "text" },
        run(" the model pure."),
      ],
      type: "paragraph",
    },
  ]);
  expect(changeAnchor(marked, [marked], strong)).toStrictEqual({
    quote: "**Keep** the model pure.",
  });
});

// One rule reads a decision for a Change: a reading that holds no blocks - a `text_tree` with no
// `content` - is no reading, so the decision is read by its stored markdown. Its quote is then its
// first paragraph's markdown, which the document's reading lines up with here. Read as that
// reading's words, which are none, the decision would hold no block to quote.
test("a decision whose reading holds no blocks is read by its stored markdown", () => {
  const worded = (blockId: string, heading: string, text: string): Decision => ({
    ...waitingDecision(blockId, []),
    heading,
    text,
    text_tree: { type: "doc" },
  });
  const first = worded("d1", "Purity", "## Purity\n\nKeep the model pure.");
  const second = worded(
    "d2",
    "Purity again",
    "## Purity again\n\nKeep the model pure.\n\nTest it alone."
  );
  const rows = [first, second];

  expect(anchorQuote(first)).toBe("Keep the model pure.");
  expect(rows.map((row) => changeAnchor(row, rows, PURITY_DOCUMENT))).toEqual([
    { occurrence: 1, quote: "Keep the model pure." },
    { occurrence: 2, quote: "Keep the model pure." },
  ]);
});
