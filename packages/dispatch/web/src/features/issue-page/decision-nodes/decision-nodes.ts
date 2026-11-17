import type { Actor, Decision, DecisionLink, DisplayTree, IssueDetails } from "../../../api/types";
import { buildDispatchReference, parseDispatchReference } from "../../refs/routes";
import { wordsOf } from "../../refs/tree-fragment";
import type { IssueTreeReads, TreeAsk } from "../issue-tree";
import type { TreeAnswer, TreeAnswerNode, TreeTuple } from "../tree-read/tree-read";

/**
 * The decisions of a tree read's answer, or of one of its views, in the shape INFRA-1150's
 * decisions read answers them (`Decision`), so every reader of that read - the Decisions filter's
 * parts, the review list, the Spec filter's column, the header's count, Keep and Change - reads
 * them with no change of its own (dispatch://INFRA-4097/artifact/plan-md#b-t3).
 *
 * A decision is a node of supertag `decision`, whose members the read answers on the node by the
 * decisions read's names and values (dispatch://INFRA-1253 R8), or a person's: a question they
 * answered that carries `shaped` ties (dispatch://INFRA-1150 R1), whose members are the ones
 * today's decisions read gives a person's decision (`api/decisions.go`'s `readDecisions`). Who
 * decided is no member of either: it is the person a `decided_by` tuple names, whom the read
 * answers as context.
 *
 * Beside the answer this reads the issue's key, which a node of this issue is addressed in, and
 * the asks read: the answer holds no ask's answer, and it names an ask block by its block, so the
 * asks read is what says an ask is answered and which ask a block is, as it is for the outline
 * (`tree-read/tree-nodes.ts`).
 *
 * This module is pure, and it imports no code of `issue-tree.ts` or of `tree-read/`, only their
 * types: the outline's steps (`tree-read/tree-nodes.ts`) are to read the decisions from here, and
 * `issue-tree.ts` is to build the outline from those steps (that module's own comment), so either
 * import would close a cycle (`biome.json`'s `noImportCycles`). So a node's address is made here
 * by the outline's own rule (`tree-nodes.ts`'s `referenceOf`, which is private to it), with the
 * same `buildDispatchReference`, and the heading is cut by `decisionQuote` rather than
 * `issue-tree.ts`'s `lineWords`.
 */

/** One decision of the answer, as the decisions read answers a decision. */
export interface DecisionNode extends Decision {
  /** Who decided: the person the node's `decided_by` tuple names, whose node the answer holds as
   *  context; null where it holds none. */
  readonly author: Actor | null;
  /** The id of the document the node is a block of (its `document` member), which Keep and a
   *  Change's anchor are written against; undefined for an ask of the conversation, which is a
   *  block of no document. */
  readonly document: string | undefined;
  /** The tree node the decision is. */
  readonly node: TreeAnswerNode;
}

/** What `decisionNodes` reads beside the answer: the issue, for its key, and the asks read. The
 *  page's reads (`IssueTreeReads`) pass as they are. */
export type DecisionReads = Pick<IssueTreeReads, "asks"> & {
  readonly issue: Pick<IssueDetails, "key">;
};

/** The answer and the reads, looked up by the read's own ids. */
interface Lookup {
  /** The row of the asks read that the ask node is: an ask block's by the block the row names,
   *  an ask of the conversation's by its own id. */
  readonly askOf: (node: TreeAnswerNode) => TreeAsk | undefined;
  readonly key: string;
  readonly nodeOf: (readId: string) => TreeAnswerNode | undefined;
  /** The tuples of the answer that start at the node `readId` names, in the answer's order. */
  readonly tuplesFrom: (readId: string) => readonly TreeTuple[];
}

/**
 * Every decision of `answer`, in the answer's own order: one for each node of supertag `decision`
 * and one for each answered question that carries `shaped` ties, and none for any other node. A
 * decision node the read places in no document has no address (`referenceOf`), which is how every
 * reader finds a decision, and no document Keep could be written against, so it is none.
 */
export function decisionNodes(answer: TreeAnswer, reads: DecisionReads): DecisionNode[] {
  const lookup = lookupOf(answer, reads);
  return answer.nodes.flatMap((node) => {
    const decided =
      node.supertag === "decision"
        ? fromDecision(node, lookup)
        : node.supertag === "ask"
          ? fromAnsweredAsk(node, lookup)
          : undefined;
    return decided === undefined ? [] : [decided];
  });
}

/** The id of the document the decision is a block of, which Keep and a Change's anchor are
 *  written against; undefined where it is a block of none. */
export function decisionDocument(decision: DecisionNode): string | undefined {
  return decision.document;
}

/**
 * The words of the decision's first paragraph, which a Change is anchored to: the server matches
 * an anchor's quote against the document's own text (`pmdoc.FindQuote`), never its markdown, so
 * they are read from the server's reading of the decision's words (`text_tree`), whose top-level
 * blocks are its paragraphs, a heading passed over, with a list's items one space apart and a hard
 * break as one space (`wordsOf`). A decision read with no reading - a pod of the build before
 * during a rolling deploy - or with a reading that holds no blocks is quoted by its first stored
 * paragraph, which is all the browser has. Undefined for a decision that holds no paragraph.
 */
export function decisionQuote(decision: {
  readonly text: string;
  readonly text_tree?: DisplayTree;
}): string | undefined {
  const tree = decision.text_tree;
  if (tree === undefined || !Array.isArray(tree.content)) return firstParagraph(decision.text);
  const block = (tree.content as DisplayTree[]).find((node) => node.type !== "heading");
  return block === undefined ? undefined : wordsOf(block);
}

/**
 * A decision node, with the members the read answers on it as they are. The read answers each of
 * them on every decision node; the client types them optional since no other node carries them,
 * so one the read left out reads as the decisions read's own empty value (`model.Decision`'s, as
 * `readDecisions` starts each decision): an agent's, naming no ask and no link, never changed,
 * kept or replaced, and waiting on nobody.
 */
function fromDecision(node: TreeAnswerNode, lookup: Lookup): DecisionNode | undefined {
  const ref = referenceOf(node, lookup);
  if (node.document === undefined || ref === undefined) return undefined;
  return {
    ...wordsAndAuthor(node, lookup),
    asks: node.asks ?? [],
    changed_at: node.changed_at ?? null,
    changed_version: node.changed_version ?? 0,
    document: node.document.artifact_id,
    kept: node.kept ?? null,
    kind: node.kind ?? "agent",
    links: node.links ?? [],
    ref,
    replaced: node.replaced === true,
    text_sha256: node.text_sha256 ?? "",
    waiting_review: node.waiting_review === true,
  };
}

/**
 * A person's decision: an ask the asks read says is answered, where it carries `shaped` ties. It
 * carries none of a decision's members, so it reads as today's decisions read reads a person's
 * decision, a section of `decisions.md` that names the ask: of the kind `person`, naming the ask
 * itself, waiting for nobody's review, never kept nor replaced, made when it was answered and at
 * no version of a document, with no sum, since only a decision that waits is kept; and its links
 * are its `shaped` ties (`shapedLinks`). Undefined for any other ask.
 */
function fromAnsweredAsk(node: TreeAnswerNode, lookup: Lookup): DecisionNode | undefined {
  const ask = lookup.askOf(node);
  if (ask?.state !== "answered") return undefined;
  if (!lookup.tuplesFrom(node.id).some((tuple) => tuple.kind === "shaped")) return undefined;
  return {
    ...wordsAndAuthor(node, lookup),
    asks: [ask.id],
    changed_at: ask.answer?.at ?? null,
    changed_version: 0,
    document: node.document?.artifact_id,
    kept: null,
    kind: "person",
    links: shapedLinks(node, lookup),
    ref: buildDispatchReference({ id: ask.id, key: node.issue ?? lookup.key, kind: "ask" }),
    replaced: false,
    text_sha256: "",
    waiting_review: false,
  };
}

/** The members every decision reads from its node alike: its block, its words and the heading
 *  read from them, whether its agent was unsure, and who decided it. */
function wordsAndAuthor(
  node: TreeAnswerNode,
  lookup: Lookup
): Pick<
  DecisionNode,
  "author" | "block_id" | "heading" | "node" | "text" | "text_tree" | "unsure"
> {
  return {
    author: authorOf(node, lookup),
    block_id: node.id,
    // A converted decision's old `##` heading is its first paragraph (INFRA-1150 T11), so the
    // heading every accessible name reads ("Keep <heading>") is that paragraph's words.
    heading: decisionQuote(node) ?? "",
    node,
    text: node.text,
    ...(node.text_tree === undefined ? {} : { text_tree: node.text_tree }),
    // The conversion writes an `Unsure:` paragraph as the attribute `uncertainty="high"`.
    unsure: node.unsure === true || node.attributes.uncertainty === "high",
  };
}

/** Who decided `node`: the person its `decided_by` tuple names, where the answer holds that
 *  person's node; null otherwise. */
function authorOf(node: TreeAnswerNode, lookup: Lookup): Actor | null {
  for (const tuple of lookup.tuplesFrom(node.id)) {
    const person = tuple.kind === "decided_by" ? lookup.nodeOf(tuple.to) : undefined;
    if (person?.supertag === "person") return { id: person.id, kind: "user" };
  }
  return null;
}

/**
 * An answered ask's `shaped` ties as the decisions read answers a decision's links: each naming
 * the node the tie reaches by that node's address, where it has one, as the outline's chip names
 * it (`tree-nodes.ts`'s `chipTo`), and not stale, since the answer says no tie is. A tie reaches a
 * block - `shaped` is a `block[]` - and the members of the link no reader reads are the ones the
 * tuple holds no value for: the ask's own time and no version.
 */
function shapedLinks(node: TreeAnswerNode, lookup: Lookup): DecisionLink[] {
  return lookup.tuplesFrom(node.id).flatMap((tuple) => {
    if (tuple.kind !== "shaped") return [];
    const target = lookup.nodeOf(tuple.to);
    const ref =
      target === undefined
        ? parseDispatchReference(tuple.to) === undefined
          ? undefined
          : tuple.to
        : referenceOf(target, lookup);
    if (ref === undefined) return [];
    return [
      {
        created_at: node.made_at,
        created_version: 0,
        from_block: node.id,
        stale: false,
        to: {
          changed_at: null,
          changed_by: null,
          id: tuple.to,
          kind: "block",
          made_at: null,
          made_by: null,
          parent: null,
          parent_version: null,
          ref,
        },
      },
    ];
  });
}

/**
 * The node's own `dispatch://` address, as the outline makes it (`tree-nodes.ts`'s
 * `referenceOf`, and `chipTo`'s for an issue): a comment's and a message's own, an ask's by the
 * ask it is, a typed block's in the document its `document` member names, and an issue's own. A
 * node of another issue is addressed in that issue. Anything else has none.
 */
function referenceOf(node: TreeAnswerNode, lookup: Lookup): string | undefined {
  const key = node.issue ?? lookup.key;
  if (node.supertag === "comment" || node.supertag === "message") {
    return buildDispatchReference({ id: node.id, key, kind: node.supertag });
  }
  // The ask the asks read says it is, else its own id where it is no block of a document.
  const ask =
    node.supertag === "ask"
      ? (lookup.askOf(node)?.id ?? (node.document === undefined ? node.id : undefined))
      : undefined;
  if (ask !== undefined) return buildDispatchReference({ id: ask, key, kind: "ask" });
  if (node.document !== undefined) {
    return buildDispatchReference({
      block: { id: node.id },
      key,
      kind: "artifact",
      slug: node.document.slug,
    });
  }
  return node.supertag === "issue"
    ? buildDispatchReference({ key: node.id, kind: "issue" })
    : undefined;
}

/** The answer's nodes and tuples, and the asks read, each looked up by the read's own ids; a
 *  later row of the asks read over an earlier one of the same place, as the outline reads it. */
function lookupOf(answer: TreeAnswer, reads: DecisionReads): Lookup {
  const nodes = new Map(answer.nodes.map((node) => [node.id, node]));
  const tuples = new Map<string, TreeTuple[]>();
  for (const tuple of answer.tuples) {
    const from = tuples.get(tuple.from);
    if (from === undefined) tuples.set(tuple.from, [tuple]);
    else from.push(tuple);
  }
  const asksById = new Map<string, TreeAsk>();
  const asksByBlock = new Map<string, TreeAsk>();
  for (const ask of reads.asks ?? []) {
    asksById.set(ask.id, ask);
    // The block an ask was indexed from, in the document that holds that block.
    const document = ask.block_artifact?.id ?? ask.artifact_id;
    if (document !== null && ask.block_id !== null) {
      asksByBlock.set(blockKey(document, ask.block_id), ask);
    }
  }
  return {
    askOf: (node) =>
      node.document === undefined
        ? asksById.get(node.id)
        : asksByBlock.get(blockKey(node.document.artifact_id, node.id)),
    key: reads.issue.key,
    nodeOf: (readId) => nodes.get(readId),
    tuplesFrom: (readId) => tuples.get(readId) ?? [],
  };
}

/** One key for block `blockId` of the document `documentId`. */
function blockKey(documentId: string, blockId: string): string {
  return `${documentId}#${blockId}`;
}

/** The decision's first stored paragraph, as markdown: what a decision read with no reading is
 *  quoted by (`decisionQuote`). A heading line is passed over, as the reading's heading is. */
function firstParagraph(text: string): string | undefined {
  // A blank line holds nothing once trimmed, so it is never the paragraph.
  for (const piece of text.split(/\n\s*\n/)) {
    const paragraph = piece.trim();
    if (paragraph !== "" && !paragraph.startsWith("#")) return paragraph;
  }
  return undefined;
}
