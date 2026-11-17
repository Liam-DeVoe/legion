import { useState } from "react";

import { ApiError, writeRequest } from "../../../api/client";
import { parseNodeReference } from "../../refs/routes";
import { useKeyedWrite } from "../../writes/useKeyedWrite";
import type { TreeNode } from "../issue-tree";
import { ANSWER_LINES, CONTROL_LABELS } from "../page-words";
import type { RoomFacts } from "./room-target";

/**
 * A criterion written under a node of the outline (dispatch://INFRA-4066 R2, R6): one block
 * appended to the issue's tree artifact, whose `parent` is the node's own `dispatch://` address
 * and whose words are the box's text as typed.
 *
 * The send is one `POST /api/v1/artifacts/{id}/edits` on the tree artifact (R6), a keyed write
 * the line shows the refusal of itself. A refusal is read by its status alone, never by the
 * server's text (the plan's decision "A refusal"): `criterionAnswer` answers the key of
 * `ANSWER_LINES` the line draws, and `criterionAnswerLine` that key's sentence.
 */

/** Where a criterion is written: the issue it is written into, the node it stands under (`parent`,
 *  the node's own `dispatch://` address) and the issue's tree artifact (`tree`, the tree read's
 *  `tree`), which it is appended to. */
export interface CriterionAim {
  readonly issueKey: string;
  readonly parent: string;
  readonly tree: string;
}

/** The key of `ANSWER_LINES` a refused criterion's line draws. */
export type CriterionAnswer = keyof Pick<
  typeof ANSWER_LINES,
  "criterion_gone" | "criterion_not_written"
>;

/** A line Markdown or pmdoc would open as a block of its own, read once its leading spaces are
 *  off: a heading, a quote, a table row, and a typed block or its fence (any line opening with
 *  `:`, so no typed line opens a block inside the criterion or closes it); a bullet, its item
 *  empty or not; a fence; a setext underline, `---` among them; a thematic break; a table's
 *  delimiter row that opens with a hyphen, which turns the line above it into a table's header;
 *  and a link or footnote definition, which goldmark takes out of its paragraph. Each is escaped
 *  before its first character, which is ASCII punctuation, so the backslash is drawn as nothing. */
const BLOCK_STARTS: readonly RegExp[] = [
  /^[#>|:]/,
  /^[-*+](?:[ \t]|$)/,
  /^(?:`{3}|~{3})/,
  /^(?:=+|-+)[ \t]*$/,
  /^([-*_])(?:[ \t]*\1){2,}[ \t]*$/,
  /^-[-: \t]*\|[-:| \t]*$/,
  /^\[[^\]]+\]:/,
];

/** An ordered list item's digits: it is escaped before the `.` or `)` after them, since a
 *  backslash before a digit would be drawn, a digit being no punctuation Markdown escapes. */
const ORDERED_ITEM = /^\d{1,9}(?=[.)](?:[ \t]|$))/;

/** One typed line as a paragraph's line: its leading spaces off, since a paragraph draws none of
 *  them and four would open code, and one backslash where it would open another block. A blank
 *  line stays blank, a paragraph break. */
function paragraphLine(typed: string): string {
  const line = typed.replace(/^[ \t]+/, "");
  const digits = ORDERED_ITEM.exec(line)?.[0];
  if (digits !== undefined) return `${digits}\\${line.slice(digits.length)}`;
  return BLOCK_STARTS.some((start) => start.test(line)) ? `\\${line}` : line;
}

/** `value` as a directive attribute's quoted value holds it. Both parsers read the value to the
 *  next `"` and decode the character references in it, giving a backslash no meaning (pmdoc's
 *  `parseDirectiveAttributes`, micromark-extension-directive), so `&`, `"` and a line ending are
 *  written as references, as `typedBlockIdAttribute` (`@legion/contracts`) writes an id's, and a
 *  backslash stands as itself. */
function attributeValue(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll('"', "&#x22;")
    .replaceAll("\n", "&#xA;")
    .replaceAll("\r", "&#xD;");
}

/**
 * The criterion `text` makes under the node `parent` (its whole `dispatch://` address), as the
 * plan's decision "The criterion's text" says: its directive with no block id, which the server
 * mints, each line of the text written so the block holds `paragraph+` and its words read back
 * as typed (`paragraphLine`), and the closing fence. A blank line stays a paragraph break and a
 * single line break a soft break, as a comment's body keeps them. The text's trailing whitespace
 * is dropped; a text that is blank is the caller's to refuse (`useCriterionLine`'s `send`).
 */
export function criterionMarkdown(parent: string, text: string): string {
  return [
    `:::criterion{parent="${attributeValue(parent)}"}`,
    ...text.trimEnd().split("\n").map(paragraphLine),
    ":::",
  ].join("\n");
}

/** `POST /api/v1/artifacts/{id}/edits` on the tree artifact `tree`: one insert of `markdown` at
 *  the artifact's end (R6). */
export function criterionRequest(tree: string, markdown: string) {
  return writeRequest(
    "post",
    "/api/v1/artifacts/{id}/edits",
    { id: tree },
    { ops: [{ after: "end", markdown, op: "insert" }] }
  );
}

/** What the line says of a refused criterion (R2), by the refusal's status alone: a 404 says the
 *  row it was written under is gone, and any other refusal that it was not written - a parent
 *  comment deleted meanwhile answers `400 INVALID_MARKDOWN` (pmdoc's `itemReach`), which says
 *  the same. */
export function criterionAnswer(error: unknown): CriterionAnswer {
  return error instanceof ApiError && error.status === 404
    ? "criterion_gone"
    : "criterion_not_written";
}

/** The sentence the line draws for a refused criterion (R2): `answer`'s entry of `ANSWER_LINES`,
 *  so the box reads it from part B's list and holds no word of its own. */
export function criterionAnswerLine(answer: CriterionAnswer): string {
  return ANSWER_LINES[answer];
}

/**
 * The node a criterion is written under (R2): an ask, a comment or a message of the outline. Its
 * `id` is the node's own row id, which the line marks (`data-criterion-under`) and a landing
 * names (`Landing.under`), and `IssueTree.tsx`'s `landingParent` finds the node by it; `kind` is
 * the conversation item the node is; `address` is the node's whole `dispatch://` address, which
 * the criterion names as its `parent`; and its words, its reading and its record are what the
 * line's target line cuts to their first line.
 */
export interface CriterionItem extends Pick<TreeNode, "source" | "text" | "tree"> {
  /** The node's own `dispatch://` address (`TreeNode.reference`): the criterion's `parent`. */
  readonly address: string;
  /** The node's row id (`TreeNode.id`). */
  readonly id: string;
  /** The item of the conversation the node is, by the kind its address names. */
  readonly kind: "ask" | "comment" | "message";
}

/** The node a criterion can be written under, where `node` is one: an ask, a comment or a message
 *  whose own address names an item of one of those kinds. Undefined for every other node - a
 *  statement, a check, a decision, the issue itself - and for one with no such address. Whether
 *  the act is offered on it is the writer's (`OutlineWriter.takesCriterion`), which also asks
 *  that the address names this issue and that the issue takes a criterion (`criterionOffered`). */
export function criterionItemOf(
  node: Pick<TreeNode, "id" | "reference" | "source" | "text" | "tree" | "type">
): CriterionItem | undefined {
  if (node.type !== "ask" && node.type !== "comment" && node.type !== "message") return undefined;
  const address = node.reference;
  const route = address === undefined ? undefined : parseNodeReference(address);
  if (address === undefined || route === undefined || !("id" in route)) return undefined;
  if (route.kind !== "ask" && route.kind !== "comment" && route.kind !== "message") {
    return undefined;
  }
  return {
    address,
    id: node.id,
    kind: route.kind,
    ...(node.source === undefined ? {} : { source: node.source }),
    text: node.text,
    ...(node.tree === undefined ? {} : { tree: node.tree }),
  };
}

/** Whether a node of this issue offers the act that writes a criterion (R2): only where the
 *  issue's tree read answers a `tree` and no `view_of`. No facts - an issue whose tree read the
 *  cache does not hold, which is every issue until part T's read is on the branch - offer none. */
export function criterionOffered(facts: RoomFacts | undefined): boolean {
  return facts !== undefined && facts.tree !== null && facts.viewOf === null;
}

/** A criterion's aim, and who hears of its landing: the line, which lands the reader on the new
 *  criterion and then lets its text go. */
export interface CriterionWriteOptions extends CriterionAim {
  /** Told of a landing: a 200, or `null` for one Dispatch made and kept no answer for. */
  readonly onLanded: () => void;
}

/**
 * The criterion's keyed write (the plan's decision "The write"): each `mutate` is one press of
 * the line's markdown, in press order with every other criterion written under the same node
 * of the same tree (`criterion:<tree>:<parent>`), shown by the pending-writes indicator only
 * while it is out, since the line shows its refusal itself. A landing is told to `onLanded`.
 * The landing invalidates no read yet: part T's tree read is not on this branch, and t8 has it
 * refresh `issueTreeKey(issueKey)` here, before `onLanded`.
 */
export function useCriterionWrite({ onLanded, parent, tree }: CriterionWriteOptions) {
  return useKeyedWrite({
    inline: true,
    label: () => CONTROL_LABELS.write_criterion,
    onSuccess: () => onLanded(),
    request: (markdown: string) => criterionRequest(tree, markdown),
    target: () => `criterion:${tree}:${parent}`,
  });
}

/** The state of the line that writes a criterion. */
export interface CriterionLine {
  /** The box's text, kept while the write is out and after a refusal. */
  readonly text: string;
  /** Sets the text, and lets the last refusal's answer go. */
  readonly setText: (text: string) => void;
  /** Writes the text as a criterion, unless it is blank or a write of it is still out. */
  readonly send: () => void;
  /** Whether the criterion's write is out. */
  readonly pending: boolean;
  /** The key of `ANSWER_LINES` the last refusal draws, until the next change of the text or the
   *  next send; `undefined` while there is none. */
  readonly answer: CriterionAnswer | undefined;
}

/**
 * The state of the box's criterion line (R2, the plan's decision "The line"): its text, which
 * stays while the write is out and after a refusal, and the refusal's answer, which goes at the
 * next change of the text or the next send (a new press clears the write's error). A landing,
 * a 200 or `null`, is told to `onLanded` and then lets the text go.
 */
export function useCriterionLine({ onLanded, ...aim }: CriterionWriteOptions): CriterionLine {
  const [text, setTyped] = useState("");
  const write = useCriterionWrite({
    ...aim,
    onLanded: () => {
      onLanded();
      setTyped("");
    },
  });
  const pending = write.isPending;
  return {
    answer: write.error === null ? undefined : criterionAnswer(write.error),
    pending,
    send: () => {
      if (pending || text.trim() === "") return;
      write.mutate(criterionMarkdown(aim.parent, text));
    },
    setText: (next) => {
      // A refused write is settled, so letting its error go leaves nothing out.
      if (write.error !== null) write.reset();
      setTyped(next);
    },
    text,
  };
}
