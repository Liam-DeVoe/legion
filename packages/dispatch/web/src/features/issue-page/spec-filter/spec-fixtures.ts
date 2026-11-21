import type {
  Actor,
  Artifact,
  Decision,
  DecisionLink,
  IssueCoverage,
  IssueDecisions,
  IssueDetails,
} from "../../../api/types";
import {
  artifact,
  DECISIONS_SLUG,
  KEY,
  node,
  READER,
  READER_ACTOR,
  SPEC_SLUG,
} from "../decisions-filter/decisions-fixtures";
import type { TreeAsk, TreeNode } from "../issue-tree";

/**
 * What the Spec filter's two unit tests are given: one issue, one tree and one decisions read,
 * built by hand, so the model and the body are read without a server
 * (`spec-decided-by.test.ts`, `SpecFilterBody.drawn.test.tsx`). A case that needs the tree as
 * the page's model places its nodes builds it with `buildIssueTree` from the reads below
 * (`askRead`, `coverageRead`) instead.
 *
 * The issue's key, the reader, a document, a node and the issue's tree are the Decisions
 * filter's own fixtures (`decisions-filter/decisions-fixtures.ts`), the same in value and in
 * what they build. What is here differs from that module's fixture of the same name, and is the
 * Spec view's own: the issue's decisions document is `artifact-dec`, which `decisionsRead` names
 * as its document, and its `artifacts` is typed as present, so a case can add a document; a
 * statement is marked a statement, carries its counts and opens with its number; a `shaped` tie
 * names a requirement; a decision is given its kind and waits for no review.
 *
 * The addresses are the ones this issue would answer with: a statement is a block of the spec,
 * a decision a block of `decisions.md`, and an ask its own id, so a tie resolves through the
 * page's own rule for addressing a node (`tree-drafts.ts`'s `treeContext`) rather than through
 * a rule written here.
 */

/** The issue, with its spec and its decisions document, which is all the addressing rule reads
 *  of it. Its `artifacts` is typed as present, so a case can add a document to the list. */
export function issueDetails(): IssueDetails & { artifacts: Artifact[] } {
  return {
    advances: [],
    advances_revision: 0,
    artifacts: [
      artifact("artifact-spec", SPEC_SLUG, true),
      artifact("artifact-dec", DECISIONS_SLUG),
    ],
    assignee: null,
    blocked_by: [],
    children: [],
    claim: null,
    closed_at: null,
    components: { ids: [], inherited_from: null, mode: "inherit", reason: null, unknown: [] },
    created_at: "2026-11-05T00:00:00Z",
    created_by: READER_ACTOR,
    decisions_artifact_id: "artifact-dec",
    design_gates: [],
    external_links: [],
    holder_state: null,
    key: KEY,
    labels: [],
    last_seen: null,
    last_seq: 1,
    number: 1,
    open_asks: [],
    parent: null,
    plan_artifact_id: "",
    primary_artifact_id: "artifact-spec",
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
}

/** One `shaped` tie, from a decision to the node its link names. */
export function shaped(ref: string): DecisionLink {
  return {
    created_at: "2026-11-14T10:00:00Z",
    created_version: 1,
    from_block: "d1",
    stale: false,
    to: {
      changed_at: null,
      changed_by: null,
      id: ref,
      kind: "requirement",
      made_at: null,
      made_by: null,
      parent: null,
      parent_version: null,
      ref,
    },
  };
}

/** One decision of the issue's decisions document, as the decisions read answers it. */
export function decision(
  partial: Partial<Decision> & { block_id: string; kind: "agent" | "person" }
): Decision {
  return {
    asks: [],
    changed_at: "2026-11-14T12:00:00Z",
    changed_version: 1,
    heading: partial.block_id,
    kept: null,
    links: [],
    ref: `dispatch://${KEY}/artifact/${DECISIONS_SLUG}#b-${partial.block_id}`,
    replaced: false,
    text: partial.block_id,
    text_sha256: "",
    unsure: false,
    waiting_review: false,
    ...partial,
  };
}

/** The decisions read over `made`. */
export function decisionsRead(made: readonly Decision[]): IssueDecisions {
  return { decisions: [...made], document: "artifact-dec", open_questions: [] };
}

/** The address of one statement, which is one block of the issue's spec. */
export const statementRef = (block: string) => `dispatch://${KEY}/spec#b-${block}`;

/** One statement of the spec, by its block id, with its number in its own words as the spec
 *  writes one: a requirement of the issue's own spec, which the page's model marks a statement
 *  (`isStatement`). */
export function statement(block: string, partial: Partial<TreeNode> = {}): TreeNode {
  return node({
    counts: { checks: 1, planTasks: 1 },
    id: `spec#b-${block}`,
    reference: statementRef(block),
    statement: true,
    text: `**${block.toUpperCase()}.** The page draws ${block}.`,
    type: "requirement",
    ...partial,
  });
}

/** The question a person answered, with their answer on it as the tree holds one. */
export function answeredAsk(
  id: string,
  answer: { at?: string; by?: Actor; selected?: string[]; text: string },
  children: readonly TreeNode[] = []
): TreeNode {
  return node({
    answer: {
      at: answer.at ?? "2026-11-14T09:00:00Z",
      by: answer.by ?? READER_ACTOR,
      selected: answer.selected ?? [],
      text: answer.text,
    },
    at: "2026-11-14T08:00:00Z",
    author: { id: "session-architect", kind: "agent" },
    children: [...children],
    id,
    reference: `dispatch://${KEY}/ask/${id}`,
    state: "answered",
    text: "Does the built page work for you?",
    type: "ask",
  });
}

/** One decision of the decisions document as the tree draws it: the node the decisions read's
 *  own entry is addressed to. */
export function decisionNode(block: string, partial: Partial<TreeNode> = {}): TreeNode {
  return node({
    at: "2026-11-13T07:00:00Z",
    author: { id: "session-planner", kind: "agent" },
    id: `${DECISIONS_SLUG}#b-${block}`,
    reference: `dispatch://${KEY}/artifact/${DECISIONS_SLUG}#b-${block}`,
    text: `The decision ${block}.`,
    type: "decision",
    ...partial,
  });
}

/** A question asked of the issue, as the asks read answers it, which the reader answered with
 *  `text`: what the page's model builds the answered question's node from. */
export function askRead(id: string, text: string): TreeAsk {
  return {
    anchor: null,
    answer: { at: "2026-11-14T09:00:00Z", selected: [], text, user: READER },
    artifact_id: null,
    asked_of: null,
    asked_of_at: null,
    asked_of_by: null,
    author: { id: "session-architect", kind: "agent" },
    block_id: null,
    created_at: "2026-11-14T08:00:00Z",
    edited_at: null,
    id,
    issue_key: KEY,
    kind: "question",
    multiple: false,
    options: [],
    question: "Does the built page work for you?",
    state: "answered",
    urgency: "med",
  };
}

/** The coverage read over the statements `blocks`, in that order, each served by a check: what
 *  the page's model builds each statement's node from. */
export function coverageRead(blocks: readonly string[]): IssueCoverage {
  return {
    gaps: [],
    has_plan: false,
    issue: KEY,
    requirements: blocks.map((block) => ({
      block_id: block,
      checks: 1,
      gaps: [],
      plan_tasks: 1,
      ref: statementRef(block),
      text: `**${block.toUpperCase()}.** The page draws ${block}.`,
    })),
    spec_artifact_id: "artifact-spec",
  };
}

/** One row of the table that fixes where the page ends a first sentence (`SENTENCE_TABLE`). */
export interface SentenceRow {
  /** The row's id, which every test names in its message. */
  readonly id: string;
  /** The text as written, `\n` a line break. */
  readonly text: string;
  /** The first sentence the function finds in the text as written: `firstSentenceOf`'s answer,
   *  and `firstLineOf`'s line, since every text here is under the line's limit. */
  readonly first: string;
  /** The first sentence a reader of the stored text draws: a person's quote of it as their
   *  answer, a comment's Contents entry and the first of the issue's three sentences. It differs
   *  from `first` only where the text opens with a list marker, which markdown reads as no word. */
  readonly stored: string;
  /** Whether the text stands as a summary's first sentence: not where it opens with a list
   *  marker, which makes the summary a list. */
  readonly summary: boolean;
}

/**
 * Where the page ends a first sentence, as one table: each row one text and the first sentence
 * every reader of `issue-tree.ts`'s `firstSentenceLength` must find in it. A text the rule reads
 * wrong, or a text that uses an abbreviation the rule does not list, becomes a new row here, not
 * a new rule. Each reader runs every row: the function itself (`firstSentenceOf`, `firstLineOf`),
 * a person's quote, a comment's Contents entry and the issue's three sentences.
 *
 * Row r1 is not here: a quote of an answer that cites a labelled statement ends where the
 * person's sentence ends, however the name it is drawn by opens, and that row needs a page that
 * names the statement. The case "a person's quote that cites a labelled statement ends where
 * their sentence ends" in `spec-decided-by.test.ts` pins it.
 */
export const SENTENCE_TABLE: readonly SentenceRow[] = [
  // A full stop before a word in lower case ends an abbreviation.
  {
    first: "Keep the memo, e.g. in the pane.",
    id: "a1",
    stored: "Keep the memo, e.g. in the pane.",
    summary: true,
    text: "Keep the memo, e.g. in the pane. Then more.",
  },
  // A number after a listed abbreviation is part of the sentence and opens no label.
  {
    first: "Use the old page (see p. 4) for now.",
    id: "a2",
    stored: "Use the old page (see p. 4) for now.",
    summary: true,
    text: "Use the old page (see p. 4) for now. Then more.",
  },
  {
    first: "Draw it whole, as on p. 4.",
    id: "a3",
    stored: "Draw it whole, as on p. 4.",
    summary: true,
    text: "Draw it whole, as on p. 4. Then more words follow.",
  },
  {
    first: "Draw it whole, as on p. 1.",
    id: "a4",
    stored: "Draw it whole, as on p. 1.",
    summary: true,
    text: "Draw it whole, as on p. 1. Then more words follow.",
  },
  {
    first: "See fig. 3.",
    id: "a5",
    stored: "See fig. 3.",
    summary: true,
    text: "See fig. 3. It shows the cut.",
  },
  {
    first: "See fig. 1.",
    id: "a6",
    stored: "See fig. 1.",
    summary: true,
    text: "See fig. 1. It shows the cut.",
  },
  // A number that holds a full stop.
  {
    first: "Keep v. 1.2 for now.",
    id: "a7",
    stored: "Keep v. 1.2 for now.",
    summary: true,
    text: "Keep v. 1.2 for now. Then move.",
  },
  // The abbreviations are read without regard to case.
  {
    first: "See Fig. 3.",
    id: "a8",
    stored: "See Fig. 3.",
    summary: true,
    text: "See Fig. 3. It shows the cut.",
  },
  // A full stop before a number ends the sentence where no listed abbreviation stands before it.
  {
    first: "We shipped it in 2026.",
    id: "n1",
    stored: "We shipped it in 2026.",
    summary: true,
    text: "We shipped it in 2026. 3 items are left.",
  },
  // The rule's one accepted miss: "no." before a number is far more often "number" than a
  // sentence's last word, so the text runs as one sentence.
  {
    first: "The answer is no. 3 rows changed.",
    id: "n2",
    stored: "The answer is no. 3 rows changed.",
    summary: true,
    text: "The answer is no. 3 rows changed.",
  },
  // A closing quote or bracket after the mark is part of the sentence.
  {
    first: 'He said "go."',
    id: "q1",
    stored: 'He said "go."',
    summary: true,
    text: 'He said "go." Then he went.',
  },
  {
    first: "Keep it (as written.)",
    id: "q2",
    stored: "Keep it (as written.)",
    summary: true,
    text: "Keep it (as written.) Then more.",
  },
  {
    first: "She wrote \u201cdone.\u201d",
    id: "q3",
    stored: "She wrote \u201cdone.\u201d",
    summary: true,
    text: "She wrote \u201cdone.\u201d Then she left.",
  },
  {
    first: "Keep it 'as is.'",
    id: "q4",
    stored: "Keep it 'as is.'",
    summary: true,
    text: "Keep it 'as is.' Then more.",
  },
  // A question or exclamation mark ends the sentence before any word, in lower case too.
  {
    first: "Is this it?",
    id: "x1",
    stored: "Is this it?",
    summary: true,
    text: "Is this it? yes, it is. Then more.",
  },
  {
    first: "Stop!",
    id: "x2",
    stored: "Stop!",
    summary: true,
    text: "Stop! now it works. Then more.",
  },
  {
    first: "Does the built page work?",
    id: "x3",
    stored: "Does the built page work?",
    summary: true,
    text: "Does the built page work? Yes.",
  },
  // A leading label ends no sentence.
  {
    first: "R1. Every node is whole first.",
    id: "l1",
    stored: "R1. Every node is whole first.",
    summary: true,
    text: "R1. Every node is whole first. Then more.",
  },
  // A leading number at the start of a stored text is a list marker in markdown.
  {
    first: "12. The page draws it.",
    id: "l2",
    stored: "The page draws it.",
    summary: false,
    text: "12. The page draws it. Then more.",
  },
  // A full stop before a numbered label ends the sentence before it.
  {
    first: "Two steps follow.",
    id: "s1",
    stored: "Two steps follow.",
    summary: true,
    text: "Two steps follow. 1. Open it. 2. Close it.",
  },
  {
    first: "Two steps follow.",
    id: "s2",
    stored: "Two steps follow.",
    summary: true,
    text: "Two steps follow. 1. Open it.",
  },
  {
    first: "Keep the old page.",
    id: "s3",
    stored: "Keep the old page.",
    summary: true,
    text: "Keep the old page. 2. Then drop it.",
  },
  // A label may follow a question or exclamation mark as well.
  {
    first: "Two steps follow.",
    id: "s4",
    stored: "Two steps follow.",
    summary: true,
    text: "Two steps follow. 1. Open it! 2. Close it.",
  },
  // A number after a listed abbreviation opens no label.
  {
    first: "Read p. 1.",
    id: "s5",
    stored: "Read p. 1.",
    summary: true,
    text: "Read p. 1. Then read p. 2. Then stop.",
  },
  // A label may follow a colon, which ends no sentence.
  {
    first: "Steps: 1. Open it.",
    id: "s6",
    stored: "Steps: 1. Open it.",
    summary: true,
    text: "Steps: 1. Open it. 2. Close it.",
  },
  // A list typed at the start: the function reads its marker as a label, and a stored text's
  // readers read the list's first item, whose marker is no word.
  {
    first: "1. Keep the old page.",
    id: "t1",
    stored: "Keep the old page.",
    summary: false,
    text: "1. Keep the old page. 2. Then drop it.",
  },
  {
    first: "1. Keep the old page.",
    id: "t2",
    stored: "Keep the old page.",
    summary: false,
    text: "1. Keep the old page.\n2. Then drop it.",
  },
  {
    first: "3. Keep the old page.",
    id: "t3",
    stored: "Keep the old page.",
    summary: false,
    text: "3. Keep the old page. 4. Then drop it.",
  },
  {
    first: "1. Keep the old page.",
    id: "t4",
    stored: "Keep the old page.",
    summary: false,
    text: "1. Keep the old page. Then drop it.",
  },
];
