import { expect, test } from "bun:test";

import type { Decision } from "../../../api/types";
import type { TreeNode } from "../issue-tree";
import { presetView } from "../presets";
import { treeContext } from "../tree-drafts";
import { firstSentenceOf, type SpecGroup, specDecidedBy } from "./spec-decided-by";
import {
  answeredAsk,
  decision,
  decisionNode,
  decisionsRead,
  issueDetails,
  issueTree,
  KEY,
  node,
  READER,
  READER_ACTOR,
  shaped,
  statement,
  statementRef,
} from "./spec-fixtures";

/**
 * What decided each statement of the Spec filter, read as the body draws it
 * (dispatch://INFRA-1196 R5, R17, R35).
 *
 * Each case builds the tree the page would build and hands it to the Spec filter's own view
 * (`presetView`), so the model is read over the rows the filter really draws, with their parent
 * lines, and over the whole tree the checks and the asks sit in. The addressing is the page's
 * own (`treeContext`), so a `shaped` tie is resolved here exactly as the tree resolves one.
 */

/** How this issue addresses a node, which is how a tie finds the statement it names. */
const { nodeIdOf } = treeContext({ issue: issueDetails() });

/** The Spec filter's body over `tree`, as the page reads it. */
function bodyOf(
  tree: TreeNode,
  made: readonly Decision[] = [],
  reader: string | undefined = READER
): readonly SpecGroup[] {
  return specDecidedBy({
    decisions: decisionsRead(made),
    nodeIdOf,
    reader,
    root: tree,
    view: presetView(tree, "spec", "").root,
  });
}

// R5: the first sentence is word for word, and a sentence as long as the person wrote it: the
// row's 100-character cut is R1's, and no cut of an answer.
test("a first sentence ends at its first sentence end, and holds the whole text where there is none", () => {
  expect(firstSentenceOf("The node types are all. You have to click.")).toBe(
    "The node types are all."
  );
  expect(firstSentenceOf("Does the built page work? Yes.")).toBe("Does the built page work?");
  expect(firstSentenceOf("And I can't even write!  Like, where do I write the text?")).toBe(
    "And I can't even write!"
  );
  expect(firstSentenceOf("No end at all")).toBe("No end at all");
  expect(firstSentenceOf("")).toBe("");
  // A sentence of 140 characters is drawn whole: R5 sets no limit of characters.
  const long = `${"word ".repeat(30)}end.`;
  expect(firstSentenceOf(`${long} And more.`)).toBe(long);
  // Line breaks inside the sentence are spaces, because the line it is drawn on is one line.
  expect(firstSentenceOf("One line\nand the next. Then more.")).toBe("One line and the next.");
});

// R5: the same decision stands once in the column beside the statements it shaped, however many
// of them there are.
test("one decision stands once beside the run of statements it shaped", () => {
  const groups = bodyOf(issueTree([statement("r1"), statement("r2"), decisionNode("d1")]), [
    decision({
      block_id: "d1",
      kind: "agent",
      links: [shaped(statementRef("r1")), shaped(statementRef("r2"))],
      text: "One read answers every node.",
    }),
  ]);
  expect(groups).toHaveLength(1);
  expect(groups[0]?.runs).toHaveLength(1);
  expect(groups[0]?.runs[0]?.decision?.id).toBe("decisions-md#b-d1");
  expect(groups[0]?.runs[0]?.statements.map((one) => one.node.id)).toEqual([
    "spec#b-r1",
    "spec#b-r2",
  ]);
});

// A run is the statements one decision shaped, one after another: a statement another decision
// shaped opens a run of its own, and the first decision opens a second run after it.
test("a statement another decision shaped opens a run of its own", () => {
  const groups = bodyOf(
    issueTree([
      statement("r1"),
      statement("r2"),
      statement("r3"),
      decisionNode("d1"),
      decisionNode("d2"),
    ]),
    [
      decision({
        block_id: "d1",
        kind: "agent",
        links: [shaped(statementRef("r1")), shaped(statementRef("r3"))],
      }),
      decision({ block_id: "d2", kind: "agent", links: [shaped(statementRef("r2"))] }),
    ]
  );
  expect(
    groups[0]?.runs.map((run) => [
      run.decision?.id,
      run.statements.map((one) => one.node.id).join(" "),
    ])
  ).toEqual([
    ["decisions-md#b-d1", "spec#b-r1"],
    ["decisions-md#b-d2", "spec#b-r2"],
    ["decisions-md#b-d1", "spec#b-r3"],
  ]);
});

// R5: a person's decision is drawn in their own answer's first sentence, word for word, with
// the question it answers, who answered and when, and the options they chose.
test("a person's decision quotes their answer's first sentence, with the question, who and when", () => {
  const ask = answeredAsk("ask-1", {
    selected: ["Changes needed"],
    text: "The node types are all, like, completely so you have to click. And I can't even write.",
  });
  const groups = bodyOf(issueTree([ask, statement("r1")]), [
    decision({
      asks: ["ask-1"],
      block_id: "d1",
      kind: "person",
      links: [shaped(statementRef("r1"))],
    }),
  ]);
  const made = groups[0]?.runs[0]?.decision;
  expect(made?.quote).toBe("The node types are all, like, completely so you have to click.");
  expect(made?.question).toBe("Does the built page work for you?");
  expect(made?.chose).toEqual(["Changes needed"]);
  expect(made?.at).toBe("2026-11-14T09:00:00Z");
  expect(made?.by).toEqual(READER_ACTOR);
  expect(made?.id).toBe("ask-1");
  expect(made?.reference).toBe(`dispatch://${KEY}/ask/ask-1`);
  expect(made?.said).toBe("you");
  expect(made?.type).toBe("ask");
});

// R5: where the person wrote nothing beside their choice, the option they chose is what the
// column quotes, since there are no words of theirs to quote.
test("an answer with no words of its own is quoted by the option the person chose", () => {
  const ask = answeredAsk("ask-1", { selected: ["1024 px"], text: "" });
  const groups = bodyOf(issueTree([ask, statement("r1")]), [
    decision({
      asks: ["ask-1"],
      block_id: "d1",
      kind: "person",
      links: [shaped(statementRef("r1"))],
    }),
  ]);
  expect(groups[0]?.runs[0]?.decision?.quote).toBe("1024 px");
});

// R34: the reader's own words are theirs; another person's are a person's, and the page says so
// rather than telling the reader they said something they did not.
test("another person's answer is not drawn as the reader's own words", () => {
  const ask = answeredAsk("ask-1", {
    by: { id: "bob", kind: "user" },
    text: "Keep it at six lines.",
  });
  const groups = bodyOf(issueTree([ask, statement("r1")]), [
    decision({
      asks: ["ask-1"],
      block_id: "d1",
      kind: "person",
      links: [shaped(statementRef("r1"))],
    }),
  ]);
  expect(groups[0]?.runs[0]?.decision?.said).toBe("person");
});

// R5: an agent's decision is drawn in its own text, with how sure it was and how it stands with
// the reader.
test("an agent's decision is quoted by its own words, with how sure it was and how it stands", () => {
  const made = node({
    at: "2026-11-13T07:00:00Z",
    author: { id: "session-planner", kind: "agent" },
    id: "decisions-md#b-d1",
    reference: `dispatch://${KEY}/artifact/decisions-md#b-d1`,
    state: "waiting_review",
    text: "One batched read names every address.",
    type: "decision",
    unsure: true,
    waiting: true,
  });
  const groups = bodyOf(issueTree([statement("r1"), made]), [
    decision({
      block_id: "d1",
      kind: "agent",
      links: [shaped(statementRef("r1"))],
      text: "One batched read names every address.",
      unsure: true,
      waiting_review: true,
    }),
  ]);
  expect(groups[0]?.runs[0]?.decision).toMatchObject({
    at: "2026-11-13T07:00:00Z",
    by: { id: "session-planner", kind: "agent" },
    id: "decisions-md#b-d1",
    quote: "One batched read names every address.",
    said: "agent",
    state: "waiting_review",
    type: "decision",
    unsure: true,
  });
});

// R5: a replaced decision draws no line, so the statement it shaped stands with none.
test("a replaced decision shapes nothing", () => {
  const groups = bodyOf(issueTree([statement("r1")]), [
    decision({
      block_id: "d1",
      kind: "agent",
      links: [shaped(statementRef("r1"))],
      replaced: true,
    }),
  ]);
  expect(groups[0]?.runs[0]?.decision).toBeUndefined();
});

// R5: a statement that hangs under an answered question carries that answer the same way, with
// no tuple, since that decision is its parent.
test("a statement under an answered question carries that answer with no tie", () => {
  const groups = bodyOf(
    issueTree([
      answeredAsk("ask-1", { text: "Put the memo in the pane. Not above the outline." }, [
        statement("r1"),
      ]),
    ])
  );
  expect(groups[0]?.parent).toMatchObject({
    id: "ask-1",
    line: "Does the built page work for you?",
    type: "ask",
  });
  expect(groups[0]?.runs[0]?.decision).toMatchObject({
    id: "ask-1",
    quote: "Put the memo in the pane.",
    said: "you",
    type: "ask",
  });
});

// R35: what makes a missing tie visible is the Spec filter, where a statement with no decision
// beside it is drawn as such.
test("a statement no decision shaped stands in a run with no decision", () => {
  const groups = bodyOf(issueTree([statement("r1")]));
  expect(groups[0]?.runs).toHaveLength(1);
  expect(groups[0]?.runs[0]?.decision).toBeUndefined();
  expect(groups[0]?.runs[0]?.statements).toHaveLength(1);
});

// R5 draws "the decisions that informed it": the first heads the run, and every other one is
// named on the statement's own line.
test("every other decision that shaped a statement is named after the first", () => {
  const groups = bodyOf(issueTree([statement("r1"), decisionNode("d1"), decisionNode("d2")]), [
    decision({ block_id: "d1", kind: "agent", links: [shaped(statementRef("r1"))] }),
    decision({ block_id: "d2", kind: "agent", links: [shaped(statementRef("r1"))] }),
  ]);
  expect(groups[0]?.runs[0]?.decision?.id).toBe("decisions-md#b-d1");
  expect(groups[0]?.runs[0]?.statements[0]?.alsoShapedBy.map((one) => one.id)).toEqual([
    "decisions-md#b-d2",
  ]);
});

// R17: the mark says how the checks that serve a statement stand, and a statement that carries
// a check is covered, never proven.
test("a statement's mark is read from the checks that serve it", () => {
  const check = (id: string, state: string): TreeNode =>
    node({ id, state: state as TreeNode["state"], type: "check" });
  const groups = bodyOf(
    issueTree([
      statement("r1", { children: [check("c1", "passed"), check("c2", "passed")] }),
      statement("r2", { children: [check("c3", "passed"), check("c4", "failed")] }),
      statement("r3", { children: [check("c5", "no-result")] }),
      statement("r4", { counts: { checks: 0, planTasks: 0 }, gaps: ["no_check"] }),
    ])
  );
  expect(groups[0]?.runs[0]?.statements.map((one) => [one.node.id, one.mark])).toEqual([
    ["spec#b-r1", "covered"],
    ["spec#b-r2", "failing"],
    ["spec#b-r3", "not_run"],
    ["spec#b-r4", "no_check"],
  ]);
});

// R2: a statement's parent is no row of the Spec filter, so it heads the group its statements
// stand in, with who made it and when.
test("statements under different parents stand in groups of their own", () => {
  const groups = bodyOf(
    issueTree([
      answeredAsk("ask-1", { text: "Keep the memo in the pane." }, [statement("r1")]),
      answeredAsk("ask-2", { text: "Draw the decisions beside the statements." }, [
        statement("r2"),
        statement("r3"),
      ]),
      statement("r4"),
    ])
  );
  expect(
    groups.map((group) => [
      group.parent?.id,
      group.runs.flatMap((run) => run.statements.map((one) => one.node.id)).join(" "),
    ])
  ).toEqual([
    ["ask-1", "spec#b-r1"],
    ["ask-2", "spec#b-r2 spec#b-r3"],
    [undefined, "spec#b-r4"],
  ]);
  expect(groups[0]?.parent).toMatchObject({
    at: "2026-11-14T08:00:00Z",
    by: { id: "session-architect", kind: "agent" },
  });
});
