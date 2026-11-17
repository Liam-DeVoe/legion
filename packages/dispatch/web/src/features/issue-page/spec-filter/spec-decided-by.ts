import type { Actor, CheckBlock, Decision, DisplayTree, IssueDecisions } from "../../../api/types";
import { parseNodeReference } from "../../refs/routes";
import type { TextSource } from "../../refs/text-references";
import { wordsOf } from "../../refs/tree-fragment";
import { drawnLine, firstLineOf, lineWords, type TreeNode } from "../issue-tree";
import { referenceStandIns } from "../one-line";
import {
  isReader,
  NODE_TYPE_NAMES,
  type NodeState,
  type NodeType,
  type StatementState,
} from "../page-words";
import { nodesInOrder } from "../tree-keys";
import { statementStandings } from "../verification-filter/statement-state";

/**
 * What decided each statement of the Spec filter, and the mark each statement carries
 * (dispatch://INFRA-1196 R5, R17, R22, R35).
 *
 * The Spec filter draws two columns: the statements, and beside each run of statements one
 * decision shaped, that decision in the words it came from (R5). This module is that reading of
 * the tree, and nothing else: it is pure, holds no React and makes no request, so the body
 * (`SpecFilterBody.tsx`) and the unit tests read one model.
 *
 * What a statement's decisions are, in the order they are drawn:
 *
 * 1. **The answered question it hangs under.** A statement that hangs under an answered ask
 *    carries that ask's line with no tuple, "since that decision is its parent" (R5), and "the
 *    line is drawn whether or not" other decisions apply, so it heads the statement's run and
 *    its parent line and its decision line stay one line.
 * 2. **Each `shaped` tie.** A decision whose link names the statement shaped it
 *    (`Decision.links[].to`, dispatch://INFRA-1150 R1). The first decision of the statement
 *    heads its run; every other one is named at the end of the statement's own line, since R5
 *    draws "the decisions that informed it" and a statement may rest on several. A replaced
 *    decision draws no line at all (R5).
 * 3. **Nothing.** A statement no decision shaped is drawn as such, which is what makes a
 *    missing tie visible (R35).
 *
 * What a person's decision quotes is the person's own answer, never a restatement of it
 * (dispatch://INFRA-1150 R4): the first sentence of the answer's plain words (`plainWords`,
 * `firstSentenceOf`), with each reference in it named in plain words once the sentence is cut
 * (`referenceStandIns`), or, where they wrote none, the options they chose; the whole answer is
 * carried beside it as stored, for the focus on the line to draw character for character (R5).
 * A section of `decisions.md` the server marks a person's whose answered question this tree
 * does not hold is the agent's record of that decision, so it is drawn as an agent's words and
 * never quoted as the person's (R9).
 *
 * An agent's decision carries its own text whole, which the cell draws as a row draws a node's
 * words (`RowText`), and is quoted by its first line, by R1's one-line rule (`lineWords`,
 * `firstLineOf`), with each reference named once the line is cut: the line under a statement
 * quotes it as one link, and a text drawn with its own links cannot stand inside one
 * (departure 2 of dispatch://INFRA-3939's plan). With it goes the one word the model holds for
 * how sure it was (`Decision.unsure`).
 *
 * Whether a statement's words changed after the decision beside it is the model's half, on
 * dispatch://INFRA-1150's branch: the view draws nothing of it.
 *
 * One part of R5 today's reads do not hold, which the body draws nothing in the place of: an
 * agent's level of uncertainty and its reason. The decisions read answers one boolean, `unsure`
 * (dispatch://INFRA-1150); the level and the reason arrive with it.
 */

/** Whose words a run of statements came from, which the column's heading says: the reader's own,
 *  another person's, or an agent's. */
export type SpecSaid = "agent" | "person" | "you";

/** The mark at the right of a statement's row: how it stands at the commit the page names, in
 *  the Verification filter's own reading, or that no check serves it (R10, R17). */
export type SpecMark = StatementState | "no_check";

/** The decision that shaped a run of statements, as the left column draws it (R5). */
export interface SpecDecision {
  /** When it was decided: the person's answer's time, or the decision's last change. */
  readonly at?: string;
  /** Who decided it, where the model holds an author. */
  readonly by?: Actor;
  /** The options a person chose, in the ask's own words; empty where they chose none. */
  readonly chose: readonly string[];
  /** The node that holds the whole of it, which the link to it names: the answered ask whose
   *  answer this is, or the decision. */
  readonly id: string;
  /** The decision node the tie came from, where the tree holds it: the node the cell is drawn
   *  as, which Details draws while the cell holds the focus. A person's decision is drawn by
   *  their answer (`id`), and this is the section of `decisions.md` that names it. */
  readonly node?: string;
  /** The question the words answer, its first line as R1 cuts a row's with each reference
   *  named after the cut; absent where the words answer no question. */
  readonly question?: string;
  /** The one line the decision is quoted by under a statement, in plain words with each
   *  reference named after its cut (`referenceStandIns`): the first sentence of a person's
   *  answer read to its plain words, or the options they chose where they wrote nothing, or the
   *  first line of an agent's decision's own words. */
  readonly quote: string;
  /** The node's own `dispatch://` address, which the link to the whole of it follows. */
  readonly reference?: string;
  readonly said: SpecSaid;
  /** How the decision stands with the reader, where the model gives it a state. */
  readonly state?: NodeState;
  /** What the node is: the ask whose answer this is, or the decision. */
  readonly type: NodeType;
  /** Whether the model marks this decision unsure (`Decision.unsure`). */
  readonly unsure: boolean;
  /** A person's whole answer as the store holds it, where it holds anything other than what
   *  `quote` draws - more than its first sentence, a mark or a link's address `quote` reads as
   *  plain words, or a reference `quote` names in words: what the focus on the decision's line
   *  draws, character for character (R5). */
  readonly whole?: string;
  /** An agent's decision's own text whole, as the cell draws it (`RowText`): as stored, the
   *  server's reading of it where one was sent, and the record it is. Absent for a person's. */
  readonly words?: {
    readonly markdown: string;
    readonly source?: TextSource;
    readonly tree?: DisplayTree;
  };
}

/** One statement of the Spec filter, as its row draws it. */
export interface SpecStatement {
  /** Every other decision that shaped it, beyond the one its run is headed by (R5). */
  readonly alsoShapedBy: readonly SpecDecision[];
  readonly mark: SpecMark;
  /** The node as the filter's view left it, whose words the row draws. */
  readonly node: TreeNode;
}

/** One run of statements that one decision shaped, which the bracket in the left column ties
 *  together (R5: "the same decision stands in a column beside the statements it shaped"). */
export interface SpecRun {
  /** The decision that shaped every statement of the run; absent where none did (R35). */
  readonly decision?: SpecDecision;
  readonly statements: readonly SpecStatement[];
}

/** The node a group of statements hangs under, which the filter draws as no row of its own
 *  (R2): the question that was answered, or the comment they were written under. */
export interface SpecParent {
  readonly at?: string;
  readonly by?: Actor;
  readonly id: string;
  /** Its own words' first line, as R1 draws one (`lineWords`, `drawnLine`), with each reference
   *  named after the cut; its kind's word where it has no words. */
  readonly line: string;
  readonly type: NodeType;
}

/** The statements under one parent, in the order the filter draws them. */
export interface SpecGroup {
  /** The node they hang under; absent for the statements that hang under the issue itself. */
  readonly parent?: SpecParent;
  readonly runs: readonly SpecRun[];
}

/** What the Spec filter's body draws. */
export interface SpecReading {
  readonly groups: readonly SpecGroup[];
  /** Every row the filter kept that is no statement: a node an address named, which outranks
   *  the filter (`presets.ts`), drawn by its kind and its words with no mark and no decision
   *  beside it (R19, R20). */
  readonly others: readonly TreeNode[];
}

/**
 * The Spec filter's body, read from the tree, the decisions and the results.
 *
 * `view` is the Spec filter's own view of the tree (`presetView`), whose rows are the
 * statements in the order the page draws them; `root` is the whole tree, which holds the asks,
 * the decisions and each statement's checks, none of which the Spec filter draws as rows (R2).
 * `nodeIdOf` is how this issue addresses a node, which is the one rule the tree is built with
 * (`tree-drafts.ts`'s `treeContext`), so a tie resolves to the node it names however its
 * address is written. `nameOf` is how the page names a reference a plain line cites
 * (`one-line.ts`'s `useReferenceNames`). `checks` is the issue's results read, which each
 * statement's mark is read from as the Verification filter reads it (`statement-state.ts`).
 */
export function specDecidedBy({
  checks,
  decisions,
  nameOf,
  nodeIdOf,
  reader,
  root,
  view,
}: {
  readonly checks: readonly CheckBlock[];
  readonly decisions: IssueDecisions | undefined;
  readonly nameOf: (reference: string) => string | undefined;
  readonly nodeIdOf: (reference: string) => string | undefined;
  /** Who is reading, so a decision of theirs reads as their own words (R34). */
  readonly reader: string | undefined;
  /** The whole tree. */
  readonly root: TreeNode;
  /** The tree as the Spec filter leaves it. */
  readonly view: TreeNode;
}): SpecReading {
  const nodes = nodesById(root);
  const asks = asksById(nodes);
  const shaped = shapedBy({ asks, decisions, nameOf, nodeIdOf, nodes, reader });
  const standings = statementStandings(root, checks);
  const groups: {
    parent?: SpecParent;
    runs: { decision?: SpecDecision; statements: SpecStatement[] }[];
  }[] = [];
  // Every row the Spec filter kept, in the order it draws them: the view's nodes but its root.
  const rows = nodesInOrder(view).slice(1);
  for (const node of rows.filter((row) => row.type === "requirement")) {
    const parent = parentOf(node, nodes, nameOf);
    const under = answerUnder(parent, nodes, reader, nameOf);
    // The answered question the statement hangs under first, then each tie, each decision once.
    const ties = [
      ...(under === undefined ? [] : [under]),
      ...(shaped.get(node.id) ?? []).filter((made) => made.id !== under?.id),
    ];
    const decision = ties.at(0);
    const standing = standings.get(node.id);
    const statement: SpecStatement = {
      alsoShapedBy: ties.slice(1),
      mark:
        node.gaps.includes("no_check") || standing === undefined || !standing.covered
          ? "no_check"
          : standing.state,
      node,
    };
    const group = groups.at(-1);
    if (group === undefined || group.parent?.id !== parent?.id) {
      groups.push({
        ...(parent === undefined ? {} : { parent }),
        runs: [{ ...(decision === undefined ? {} : { decision }), statements: [statement] }],
      });
      continue;
    }
    const run = group.runs.at(-1);
    if (run === undefined || run.decision?.id !== decision?.id) {
      group.runs.push({
        ...(decision === undefined ? {} : { decision }),
        statements: [statement],
      });
      continue;
    }
    run.statements.push(statement);
  }
  return { groups, others: rows.filter((row) => row.type !== "requirement") };
}

/**
 * The first sentence of `text`, word for word: everything up to the first full stop, question
 * mark or exclamation mark followed by a space or the end of the text, and the whole text where
 * it holds none, with no limit of characters (R5). Line breaks inside it are spaces, because
 * the line it is drawn on is one line. A leading label is no sentence end (decision 51 of
 * dispatch://INFRA-1196): a first word of at most five characters that ends in `.` and holds a
 * digit (`R1.`, `12.`) opens the sentence, as part F's `firstLineOf` reads one.
 *
 * It is not `firstLineOf`, which cuts a row's first line at 100 characters (R1): a person's
 * answer is quoted word for word, and a sentence of theirs is as long as they wrote it.
 */
export function firstSentenceOf(text: string): string {
  const whole = text.replace(/\s+/gu, " ").trim();
  const label = LEADING_LABEL.exec(whole)?.[0] ?? "";
  const rest = /^.*?[.!?](?=\s|$)/u.exec(whole.slice(label.length))?.[0];
  return rest === undefined ? whole : `${label}${rest}`;
}

/** A leading label (decision 51): a first word of at most five characters, holding a digit and
 *  ending in `.`, which part F's `firstLineOf` reads with the same pattern. */
export const LEADING_LABEL = /^(?=\S*\d)\S{0,4}\.(?=\s|$)/u;

/** A blank line, which ends a paragraph: no mark and no code span reaches past one. */
const BLANK_LINE = /\n\s*\n/u;

/** One ASCII punctuation character: what a backslash escapes, and what a code span and an
 *  autolink hold that the marks below would otherwise read as marks. */
const PUNCTUATION = /[!-/:-@[-`{-~]/gu;

/**
 * What markdown reads literally, each where it starts, left to right: a backslash before an
 * ASCII punctuation character, which is that character; a code span, a run of backticks closed
 * by the next run of the same count, which is its content verbatim and inside which a backslash
 * escapes nothing; an autolink, `<https://…>`, which is its URL; and a run of backticks no run
 * of its count closes, which is that run as written. They are one pattern because each decides
 * the others: a backtick a backslash escapes opens no code span, and a backslash inside a code
 * span escapes nothing.
 */
const LITERAL =
  /\\([!-/:-@[-`{-~])|(`+)(?!`)([\s\S]*?[^`])\2(?!`)|<([A-Za-z][A-Za-z0-9+.-]{1,31}:[^\s<>]*)>|`+/gu;

/** A heading marker at the start of a line, `#` to `######` and a space, which markdown allows
 *  up to three spaces of indent before. */
const HEADING_MARKER = /^ {0,3}#{1,6}[ \t]+/gmu;

/** An image, `![alt](src)`, whose words the server's reading of it holds none of. */
const IMAGE = /!\[[^\]]*\]\([^)]*\)/gu;

/** A stored block that holds no words once its images are left out: nothing but white space
 *  and the marks of a horizontal rule. `issue-tree.ts`'s `NO_WORDS`, which that module keeps to
 *  itself (`decisionLineWords`), and which goes with it once part F exports its reading. */
const NO_WORDS = /^[\s*_-]*$/u;

/** A link, `[words](target)`, which reads as its words; its target is no word of the text. */
const LINK = /\[([^\]]*)\]\([^)]*\)/gu;

/** `**strong**`, which stands wherever it is written. */
const STAR_STRONG = /\*\*(?=\S)([\s\S]*?\S)\*\*/gu;

/** `__strong__`, which markdown reads only where no letter or digit stands either side of it. */
const UNDERSCORE_STRONG = /(?<![\p{L}\p{N}])__(?=\S)([\s\S]*?\S)__(?![\p{L}\p{N}])/gu;

/** `~~struck~~`, which stands wherever it is written. */
const STRUCK = /~~(?=\S)([\s\S]*?\S)~~/gu;

/** `*emphasis*`, which stands wherever it is written. */
const STAR_EMPHASIS = /\*(?=\S)([^*]*?\S)\*/gu;

/** `_emphasis_`, which markdown reads only between characters that are no letter or digit, so
 *  `snake_case_name` is one word. */
const UNDERSCORE_EMPHASIS = /(?<![\p{L}\p{N}])_(?=\S)([^_]*?\S)_(?![\p{L}\p{N}])/gu;

/** A private-use code point: one of the Basic Multilingual Plane's, U+E000 to U+F8FF, or one of
 *  the supplementary planes 15 and 16, U+F0000 to U+10FFFF, which are private-use but for the
 *  two noncharacters each plane ends with. */
const PRIVATE_USE = /[\ue000-\uf8ff\u{f0000}-\u{10ffff}]/gu;

/** The private-use characters `plainWords` may hold its stand-ins in, as the first and the last
 *  code point of each run of them: 50 blocks of 128 in the Basic Multilingual Plane, then 1,024
 *  in the supplementary planes 15 and 16. */
const STAND_IN_RANGES = [
  [0xe000, 0xf8ff],
  [0xf0000, 0x10ffff],
] as const;

/**
 * The first code point of the first block of 128 private-use characters that `paragraph` holds
 * none of, so a stand-in made in it, the block's first code point plus an ASCII character's
 * code, is never a character the author wrote. A block of the Basic Multilingual Plane is taken
 * first; only a paragraph holding a character in every one of its 50 blocks reaches the
 * supplementary planes. A paragraph holding a character in every one of the 1,074 blocks has
 * none to give: it holds no stand-ins, and a mark inside what markdown reads literally in it is
 * read as a mark.
 */
function standInBlock(paragraph: string): number | undefined {
  const held = new Set<number>();
  for (const [character] of paragraph.matchAll(PRIVATE_USE)) {
    held.add((character.codePointAt(0) ?? 0) & ~0x7f);
  }
  for (const [first, last] of STAND_IN_RANGES) {
    for (let block = first; block < last; block += 0x80) {
      if (!held.has(block)) return block;
    }
  }
  return undefined;
}

/**
 * The words of a stored markdown text as a reader reads them, on one line. It is part F's
 * `plainWords` (`tree-drafts.ts`, decision 88 of dispatch://INFRA-1196), less F's last step,
 * which writes a bare `dispatch://` address as its short form: this view names each reference
 * in plain words instead (`referenceStandIns`). J imports F's and deletes this copy once
 * F's tip is under J's base (decision 94).
 *
 * A place that draws a stored text as plain words draws this, since its marks are markup and no
 * word of it (R21), and a mark after a sentence's end (`today.**`) would hide that end from
 * `firstSentenceOf`. Each paragraph is read on its own, since no mark pairs across a blank line.
 * What markdown reads literally (`LITERAL`) - an escaped punctuation character (`1\.` is `1.`),
 * a code span's content, an autolink's URL - is read first, and each ASCII punctuation character
 * of it is held as a private-use stand-in while the marks are taken off, so no mark is read
 * inside it, and put back after. The stand-ins are made in a block of private-use characters
 * the paragraph holds none of (`standInBlock`), and only that block's characters are put back,
 * so a private-use character the author wrote of their own - an icon from an icon font or a
 * terminal - is kept as they wrote it. Then a heading marker at the start of any line goes, an
 * image goes whole, a link reads as its words, and strong, emphasis and struck marks go until
 * none is left, so nested marks (`***x***`) go too. White space collapses to single spaces.
 */
function plainWords(markdown: string): string {
  const paragraphs = markdown.split(BLANK_LINE).map((paragraph) => {
    const block = standInBlock(paragraph);
    let words = paragraph
      .replace(
        LITERAL,
        (written, escaped?: string, _run?: string, code?: string, url?: string) =>
          (escaped ?? code ?? url)?.replace(PUNCTUATION, (character) =>
            block === undefined ? character : String.fromCodePoint(block + character.charCodeAt(0))
          ) ?? written
      )
      .replace(HEADING_MARKER, "")
      .replace(IMAGE, "")
      .replace(LINK, "$1");
    for (let marked = ""; marked !== words; ) {
      marked = words;
      words = words
        .replace(STAR_STRONG, "$1")
        .replace(UNDERSCORE_STRONG, "$1")
        .replace(STRUCK, "$1")
        .replace(STAR_EMPHASIS, "$1")
        .replace(UNDERSCORE_EMPHASIS, "$1");
    }
    if (block === undefined) return words;
    return words.replace(PRIVATE_USE, (character) => {
      const code = (character.codePointAt(0) ?? 0) - block;
      return code >= 0 && code < 0x80 ? String.fromCharCode(code) : character;
    });
  });
  return paragraphs.join(" ").replace(/\s+/gu, " ").trim();
}

/** Every node of the tree by its id, the first placement of a node drawn under more than one
 *  parent, which is where it is edited (`TreeNode.transcluded`). */
function nodesById(root: TreeNode): ReadonlyMap<string, TreeNode> {
  const nodes = new Map<string, TreeNode>();
  const walk = (node: TreeNode): void => {
    if (!nodes.has(node.id)) nodes.set(node.id, node);
    for (const child of node.children) walk(child);
  };
  walk(root);
  return nodes;
}

/** Each ask of the tree by the id a decision names it with (`Decision.asks`), which is the ask's
 *  own id and not the id of the row it is drawn as: an ask the server indexed from a block of a
 *  document is drawn as that block (`tree-drafts.ts`'s `addAsks`). */
function asksById(nodes: ReadonlyMap<string, TreeNode>): ReadonlyMap<string, TreeNode> {
  const asks = new Map<string, TreeNode>();
  for (const node of nodes.values()) {
    if (node.type !== "ask" || node.reference === undefined) continue;
    const route = parseNodeReference(node.reference);
    if (route?.kind === "ask") asks.set(route.id, node);
  }
  return asks;
}

/** The decisions that shaped each statement, by the statement's node id, in the order the
 *  decisions read answers them. */
function shapedBy({
  asks,
  decisions,
  nameOf,
  nodeIdOf,
  nodes,
  reader,
}: {
  asks: ReadonlyMap<string, TreeNode>;
  decisions: IssueDecisions | undefined;
  nameOf: (reference: string) => string | undefined;
  nodeIdOf: (reference: string) => string | undefined;
  nodes: ReadonlyMap<string, TreeNode>;
  reader: string | undefined;
}): ReadonlyMap<string, readonly SpecDecision[]> {
  const shaped = new Map<string, SpecDecision[]>();
  for (const decision of decisions?.decisions ?? []) {
    // A replaced decision draws no line (R5): it is drawn where it sits in the outline, and a
    // statement it shaped is no longer standing on it.
    if (decision.replaced) continue;
    const links = decision.links ?? [];
    if (links.length === 0) continue;
    const made = decisionOf(decision, { asks, nameOf, nodeIdOf, nodes, reader });
    for (const link of links) {
      const id = link.to.ref === undefined ? undefined : nodeIdOf(link.to.ref);
      if (id === undefined) continue;
      const held = shaped.get(id) ?? [];
      // A decision that names one statement in two of its blocks is one decision of it.
      if (!held.some((one) => one.id === made.id)) held.push(made);
      shaped.set(id, held);
    }
  }
  return shaped;
}

/** One decision as the column draws it: a person's by the answer they gave, an agent's by its
 *  own words. */
function decisionOf(
  decision: Decision,
  {
    asks,
    nameOf,
    nodeIdOf,
    nodes,
    reader,
  }: {
    asks: ReadonlyMap<string, TreeNode>;
    nameOf: (reference: string) => string | undefined;
    nodeIdOf: (reference: string) => string | undefined;
    nodes: ReadonlyMap<string, TreeNode>;
    reader: string | undefined;
  }
): SpecDecision {
  const node = nodes.get(nodeIdOf(decision.ref) ?? "");
  // A person's decision is their answer to a question (dispatch://INFRA-1150 R3), so the words
  // the column quotes are the answer's own and never the decision's restatement of them (R4).
  const answered = (decision.asks ?? [])
    .map((ask) => asks.get(ask))
    .find((ask) => ask?.answer !== undefined);
  if (decision.kind === "person" && answered?.answer !== undefined) {
    return {
      ...answerDecision(answered, reader, nameOf),
      ...(node === undefined ? {} : { node: node.id }),
    };
  }
  // Every other section is drawn by its own words, which are an agent's: its decision, or its
  // record of a person's decision whose answered question this tree does not hold - one the
  // tree places elsewhere, or one whose read failed. That record is never put in the person's
  // mouth (R9: "no agent's words are drawn under a person's name").
  const at = node?.at ?? decision.changed_at ?? undefined;
  const tree = decision.text_tree ?? node?.tree;
  // The one line is cut from the words of the first block that holds any, as every one-line
  // place cuts a node's (`lineWords`), so the decision's own `##` heading never runs into the
  // paragraph under it, and with none of the symbols the cell draws as formatting.
  const words = node !== undefined ? lineWords(node) : decisionLineWords(decision);
  return {
    ...(at === undefined ? {} : { at }),
    ...(node?.author === undefined ? {} : { by: node.author }),
    chose: [],
    id: node?.id ?? decision.ref,
    ...(node === undefined ? {} : { node: node.id }),
    quote: namedFirstLine(words, nameOf),
    reference: decision.ref,
    said: "agent",
    ...(node?.state === undefined ? {} : { state: node.state }),
    type: "decision",
    unsure: decision.unsure,
    words: {
      markdown: decision.text,
      ...(node?.source === undefined ? {} : { source: node.source }),
      ...(tree === undefined ? {} : { tree }),
    },
  };
}

/** A person's answer to `ask` as the column draws it: the first sentence of its plain words
 *  with each reference named, or the options they chose where they wrote none (R5), with the
 *  whole answer as stored wherever it is not what the quote draws. */
function answerDecision(
  ask: TreeNode,
  reader: string | undefined,
  nameOf: (reference: string) => string | undefined
): SpecDecision {
  const answer = ask.answer;
  if (answer === undefined) throw new Error("an answered ask with no answer");
  // A link the person wrote reads as its words and a mark as nothing (`plainWords`), so the
  // quote is their words and none of the markdown they were stored in. Each reference is named
  // after the sentence is cut, so a name that opens with a label ("R2.") ends none of it.
  const references = referenceStandIns(nameOf);
  const written = firstSentenceOf(references.standIn(plainWords(answer.text)));
  const quote = written === "" ? answer.selected.join(", ") : references.named(written);
  const stored = answer.text.trim();
  return {
    at: answer.at,
    by: answer.by,
    chose: answer.selected,
    id: ask.id,
    question: namedFirstLine(lineWords(ask), nameOf),
    quote,
    ...(ask.reference === undefined ? {} : { reference: ask.reference }),
    said: isReader(answer.by, reader) ? "you" : "person",
    type: "ask",
    unsure: false,
    // A quote read to plain words, or naming a reference in words, is not the characters the
    // person wrote, so the focus draws those too, though the answer is one sentence.
    ...(stored === "" || stored === quote ? {} : { whole: answer.text }),
  };
}

/** The question a statement hangs under, where that question was answered: its answer is the
 *  decision that shaped the statement, with no tuple (R5). */
function answerUnder(
  parent: SpecParent | undefined,
  nodes: ReadonlyMap<string, TreeNode>,
  reader: string | undefined,
  nameOf: (reference: string) => string | undefined
): SpecDecision | undefined {
  if (parent === undefined) return undefined;
  const node = nodes.get(parent.id);
  if (node === undefined || node.type !== "ask" || node.answer === undefined) return undefined;
  return answerDecision(node, reader, nameOf);
}

/** The node a statement hangs under, where the filter draws it as no row (R2), with who made it
 *  and when, and its own words' first line, which its heading draws by R1's rule. */
function parentOf(
  node: TreeNode,
  nodes: ReadonlyMap<string, TreeNode>,
  nameOf: (reference: string) => string | undefined
): SpecParent | undefined {
  const line = node.parentLine;
  if (line === undefined) return undefined;
  const parent = nodes.get(line.id);
  // Read from the node and not from `line.line`, which `presets.ts` cuts by the same rule
  // (`parentLineOf`) with each reference in it still its address: the heading names each one in
  // plain words, and counts it against the line's limit as the name it draws.
  const words = parent === undefined ? "" : namedFirstLine(lineWords(parent), nameOf);
  return {
    ...(parent?.at === undefined ? {} : { at: parent.at }),
    ...(parent?.author === undefined ? {} : { by: parent.author }),
    id: line.id,
    // A parent with no words is named by its kind's word, never by its id (decision 66).
    line: words === "" ? NODE_TYPE_NAMES[line.type] : words,
    type: line.type,
  };
}

/** `words`' first line as a one-line place draws it (R1: `firstLineOf`, `drawnLine`), cut with
 *  each reference it cites unnamed and each named after the cut (`referenceStandIns`). */
function namedFirstLine(words: string, nameOf: (reference: string) => string | undefined): string {
  const references = referenceStandIns(nameOf);
  return references.named(drawnLine(firstLineOf(references.standIn(words))));
}

/**
 * The words an agent's decision the tree holds no node for is quoted by, as `lineWords` reads a
 * node's: the first top-level block of its reading that holds any, else the first block of its
 * stored text that holds any, to its blank line, with a leading heading marker left out and a
 * heading ending at its own line. F's `lineWords` reads a node, and keeps its reading of a bare
 * reading (`firstBlockWords`) to itself, so this is the same reading of a decision's own two
 * fields: a copy, which this part deletes for F's export of that reading over a text and its
 * tree once F is under this part's base (decision 94), as it does its copy of `plainWords`.
 */
function decisionLineWords({ text, text_tree }: Decision): string {
  if (text_tree !== undefined) {
    const blocks = Array.isArray(text_tree.content) ? (text_tree.content as DisplayTree[]) : [];
    return blocks.map((block) => wordsOf(block)).find((words) => words.trim() !== "") ?? "";
  }
  for (const stored of text.split(BLANK_LINE)) {
    const block = stored.trimStart();
    const words = /^#{1,6} +(.*)/u.exec(block)?.[1] ?? block;
    if (!NO_WORDS.test(words.replace(IMAGE, ""))) return words;
  }
  return "";
}
