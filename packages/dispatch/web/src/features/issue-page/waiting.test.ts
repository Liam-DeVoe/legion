import { expect, test } from "bun:test";

import type {
  Artifact,
  ArtifactBlock,
  Comment,
  Decision,
  DisplayTree,
  IssueCoverage,
  IssueDecisions,
  IssueDetails,
} from "../../api/types";
import { wordsOf } from "../refs/tree-fragment";
import { anchorQuote, changeAnchor, decisionChangeComments } from "./decision-review";
import { buildIssueTree, type DocumentBlocks, type TreeNode } from "./issue-tree";
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
 *  read's statements, each addressed as the tree addresses it, and none while that read has not
 *  answered. */
const nodeIdOf = treeContext({ issue }).nodeIdOf;
function statementsOf(read: IssueCoverage | undefined): (reference: string) => boolean {
  const statements = new Set(read?.requirements?.map((one) => nodeIdOf(one.ref)));
  return (reference) => statements.has(nodeIdOf(reference));
}
const isStatement = statementsOf(coverage);

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
      content: [{ attrs: { level: 2 }, content: [run(heading)], type: "heading" }, ...body],
      type: "doc",
    },
  };
}

/** Where the server puts a Change of each of `rows` written with the anchor `changeAnchor` gives
 *  it (`chosen`), and where that row's own first block below its heading starts (`own`), both in
 *  the decisions document's text as `pmdoc.FindQuote` reads it: the words of every row's reading
 *  joined by one space. The server lists every match of the quote, overlapping ones included, and
 *  takes the one the occurrence names. A row's words start one space after the rows above it,
 *  and its first block one space after its heading's words. */
function changePlaces(rows: readonly Decision[]): {
  chosen: (number | undefined)[];
  own: number[];
} {
  const words = rows.map((row) => wordsOf(row.text_tree as DisplayTree));
  const text = words.join(" ");
  const chosen = rows.map((row) => {
    const anchor = changeAnchor(row, rows);
    if (anchor === undefined) return undefined;
    const matches: number[] = [];
    for (let at = text.indexOf(anchor.quote); at !== -1; at = text.indexOf(anchor.quote, at + 1)) {
      matches.push(at);
    }
    return matches[anchor.occurrence];
  });
  const own = rows.map(
    (row, index) =>
      words.slice(0, index).reduce((start, one) => start + one.length + 1, 0) +
      row.heading.length +
      1
  );
  return { chosen, own };
}

// R7, R13, R35, and the tester's F4 and F6: what waits on the reader is each open question whose
// turn is theirs, each decision of the review list - an agent's decision that waits, that a tie
// names a statement for, and that the reader has not asked to change since it changed - and each
// open approval request whose turn is theirs. The tree marks the same question and the same
// decision; an approval request is no node of it. While the login is not known, nothing waits.
test("what waits on the reader is their open questions, the review list and their approval requests", () => {
  const counted = waitingOnReader(issue, "alice", decisions, isStatement, [change], undefined);
  expect(counted).toEqual({ approvals: [spec.id], asks: 1, decisions: 1, total: 3 });

  const root = buildIssueTree({ comments: [change], coverage, decisions, issue, viewer: "alice" });
  // The decisions no question holds are drawn above the conversation (`ROOT_ORDER`).
  expect(waitingIn(root)).toEqual(["decisions-md#b-listed", "mine"]);

  const unknown = waitingOnReader(issue, undefined, decisions, isStatement, [change], undefined);
  expect(unknown.total).toBe(0);
  expect(waitingIn(buildIssueTree({ comments: [change], coverage, decisions, issue }))).toEqual([]);
});

// R3, R7 and R13: a Change belongs to the decision whose section of the decisions document holds
// the block the person wrote it in (`Anchor.block_id`), though another decision holds its words
// too. Here the first decision holds the second's words and is tied to no statement, so it is on
// no review list, and the Change is written in the second's paragraph: the second decision then
// waits on its agent, and nothing waits on the reader, in the count as in the tree. A count that
// read the quote alone would put the Change on the first decision and count the second as
// waiting. Where the anchor stores no block, the quote alone decides: the first decision whose
// words hold it.
test("waitingOnReader counts a Change under the decision whose block holds its anchor", () => {
  const worded = (blockId: string, shaped: readonly string[], text: string): Decision => ({
    ...waitingDecision(blockId, shaped),
    text,
  });
  const first = worded("d1", [], "## Keep the model pure\n\nKeep the model pure. Draw it once.");
  const second = worded("d2", ["r1"], "## Draw it once\n\nDraw it once.");
  const both: IssueDecisions = { ...decisions, decisions: [first, second] };
  const anchor = {
    artifact_id: made.id,
    block_id: "d2-words",
    mark_id: "c-change",
    orphaned: false,
    quote: "Draw it once.",
    version: 3,
  };
  const moved: Comment = { ...change, anchor, id: "c-change" };
  // The decisions document's blocks in their order, as the blocks read answers them: each
  // decision's heading, then its paragraph.
  const placed = (id: string, type: string, from: number, to: number): ArtifactBlock => ({
    attributes: [],
    from,
    id,
    references: { asks: 0, comments: 0 },
    to,
    token: id,
    type,
  });
  const documents: DocumentBlocks[] = [
    {
      artifactId: made.id,
      blocks: [
        placed("d1", "heading", 0, 24),
        placed("d1-words", "paragraph", 26, 62),
        placed("d2", "heading", 64, 80),
        placed("d2-words", "paragraph", 82, 96),
      ],
    },
  ];
  const counted = waitingOnReader(issue, "alice", both, isStatement, [moved], documents);
  const root = buildIssueTree({
    comments: [moved],
    coverage,
    decisions: both,
    documents,
    issue,
    viewer: "alice",
  });
  const waitingDecisions = waitingIn(root).filter((id) => id.startsWith("decisions-md#"));

  expect(counted.decisions).toBe(0);
  expect(waitingDecisions).toEqual([]);
  expect(counted.decisions).toBe(waitingDecisions.length);
  expect(parentsOf(root, moved.id)).toEqual(["decisions-md#b-d2"]);
  expect(decisionChangeComments(both, [moved], issue.artifacts, documents)).toEqual(
    new Map([["c-change", second.ref]])
  );

  const unplaced: Comment = { ...moved, anchor: { ...anchor, block_id: null } };
  expect(decisionChangeComments(both, [unplaced], issue.artifacts, documents)).toEqual(
    new Map([["c-change", first.ref]])
  );
});

// R7, R13 and R35: a statement is a requirement the coverage read holds, as the header's count is
// handed the statements. A decision tied only to a `requirement` block of the plan, which no
// coverage row holds, is on no review list; nor is one tied to the spec's `r1` while the coverage
// read has not answered, though the spec's blocks read has drawn that block already. The tree
// marks neither and the count counts neither, and the decision tied to `r1` once the coverage
// read holds it waits, in the tree as in the count.
test("a tie to a requirement the coverage read does not hold puts no decision on the review list", () => {
  const plan = issueDocument("artifact-plan", "plan-md");
  const planned: IssueDetails = {
    ...issue,
    artifacts: [spec, made, plan],
    plan_artifact_id: plan.id,
  };
  const requirementBlock = (id: string): ArtifactBlock => ({
    attributes: [],
    from: 0,
    id,
    references: { asks: 0, comments: 0 },
    to: 1,
    token: id,
    type: "requirement",
  });
  const documents: DocumentBlocks[] = [
    { artifactId: spec.id, blocks: [requirementBlock("r1")] },
    { artifactId: plan.id, blocks: [requirementBlock("p1")] },
  ];
  const tiedToPlan = waitingDecision("planned", ["p1"]);
  const toPlan: Decision = {
    ...tiedToPlan,
    links: (tiedToPlan.links ?? []).map((link) => ({
      ...link,
      to: { ...link.to, id: plan.id, ref: `dispatch://${KEY}/artifact/plan-md#b-p1` },
    })),
  };
  const both: IssueDecisions = {
    ...decisions,
    decisions: [waitingDecision("listed", ["r1"]), toPlan],
  };
  const waitingDecisions = (read: IssueCoverage | undefined): string[] =>
    waitingIn(
      buildIssueTree({
        ...(read === undefined ? {} : { coverage: read }),
        decisions: both,
        documents,
        issue: planned,
        viewer: "alice",
      })
    ).filter((id) => id.startsWith("decisions-md#"));

  expect(waitingDecisions(coverage)).toEqual(["decisions-md#b-listed"]);
  expect(waitingOnReader(planned, "alice", both, isStatement, [], documents).decisions).toBe(1);

  expect(waitingDecisions(undefined)).toEqual([]);
  expect(
    waitingOnReader(planned, "alice", both, statementsOf(undefined), [], documents).decisions
  ).toBe(0);
});

// Decision 92 of dispatch://INFRA-1196: a Change is a comment anchored to the decision's first
// block below its heading, and the server refuses a quote that matches more than one place in the
// decisions document unless the write names which one. Here the first and the third decision open
// with the same sentence: each Change names the occurrence that is its own block's, and the
// server, choosing among the quote's matches as `pmdoc.FindQuote` does, puts each Change on its
// own decision's block. An occurrence of 0 for every decision would put the third's Change on the
// first decision. A decision whose heading is all it holds has no block to anchor a Change to.
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

  expect(rows.map((row) => changeAnchor(row, rows))).toEqual([
    { occurrence: 0, quote: "Keep the model pure." },
    { occurrence: 0, quote: "Read it from one place." },
    { occurrence: 1, quote: "Keep the model pure." },
  ]);
  const places = changePlaces(rows);
  expect(places.chosen).toEqual(places.own);

  expect(changeAnchor(readDecision("d4", "Heading only", "", []), rows)).toBeUndefined();
});

// Decision 92 of dispatch://INFRA-1196: the server's text holds one space between two list items
// (`buildFlattenedText`), so a decision whose first block below its heading is a list is quoted by
// the list's words, its items one space apart, and the same words in an earlier decision's list
// are a match above it. The second decision's list holds "Hold the deploy." and "Then ship.":
// where the first decision's list holds only "Hold the deploy.", the quote matches nowhere in it
// and the occurrence is 0; where it holds both items, the quote matches there first and the
// occurrence is 1. Either way the server puts the Change on the second decision's list.
test("a Change of a decision whose first block is a list counts the same words inside an earlier decision's list item", () => {
  const second = readDecision("d2", "Ship after the hold", "- Hold the deploy.\n- Then ship.", [
    bulletList(["Hold the deploy.", "Then ship."]),
  ]);
  expect(anchorQuote(second)).toBe("Hold the deploy. Then ship.");

  for (const [items, occurrence] of [
    [["Hold the deploy."], 0],
    [["Hold the deploy.", "Then ship."], 1],
  ] as const) {
    const first = readDecision("d1", "Freeze", items.map((item) => `- ${item}`).join("\n"), [
      bulletList(items),
    ]);
    const rows = [first, second];
    expect(changeAnchor(second, rows)).toEqual({
      occurrence,
      quote: "Hold the deploy. Then ship.",
    });
    const places = changePlaces(rows);
    expect(places.chosen).toEqual(places.own);
  }
});

// Decision 92 of dispatch://INFRA-1196: the server lists every match of a quote, overlapping ones
// included (`findAll`), so a match that starts in a decision's heading and runs into its first
// block is listed before the block's own. The second decision's heading ends with "go" and its
// first paragraph is "go go go": its words, "Ready, set, go go go go", hold the quote where the
// heading's "go" starts and again at the paragraph, and the first decision's "go go go go" holds
// it twice more, overlapping. The paragraph's own match is the fourth, occurrence 3; with no
// decision above it, the second.
test("a Change counts a match that starts in the decision's heading and runs into its first block", () => {
  const first = readDecision("d1", "Chant", "go go go go", [paragraph("go go go go")]);
  const second = readDecision("d2", "Ready, set, go", "go go go", [paragraph("go go go")]);
  const rows = [first, second];

  expect(changeAnchor(second, rows)).toEqual({ occurrence: 3, quote: "go go go" });
  expect(changeAnchor(second, [second])).toEqual({ occurrence: 1, quote: "go go go" });
  const places = changePlaces(rows);
  expect(places.chosen).toEqual(places.own);
});

// Decision 92 of dispatch://INFRA-1196, from a pod of the build before during a rolling deploy,
// whose read sends no reading of a decision's text: each decision is then counted by its stored
// markdown, and its first block starts at its first paragraph below its heading, the paragraph
// `anchorQuote` quotes there. Two decisions opening with the same paragraph name its first and
// its second occurrence. A decision the read does not hold is counted with no decision above it.
test("a Change of a decision read with no reading counts the decisions' stored markdown", () => {
  const worded = (blockId: string, text: string): Decision => ({
    ...waitingDecision(blockId, []),
    text,
  });
  const first = worded("d1", "## Purity\n\nKeep the model pure.");
  const second = worded("d2", "## Purity again\n\nKeep the model pure.\n\nTest it alone.");
  const rows = [first, second];

  expect(rows.map((row) => changeAnchor(row, rows))).toEqual([
    { occurrence: 0, quote: "Keep the model pure." },
    { occurrence: 1, quote: "Keep the model pure." },
  ]);
  expect(changeAnchor(second, [first])).toEqual({ occurrence: 0, quote: "Keep the model pure." });
});
