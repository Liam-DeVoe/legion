import { parseBlockPointerValue } from "@legion/contracts";

import { primarySpec } from "../../api/issue-cache";
import type {
  Actor,
  Artifact,
  ArtifactWithTrees,
  Ask,
  CheckBlock,
  CheckResult,
  Decision,
  DisplayTree,
  IssueChild,
  IssueDetails,
  ReferenceTarget,
} from "../../api/types";
import { compareTimestamps, isTimestamp } from "../../lib/timestamps";
import { referenceSpans } from "../refs/reference-spans";
import {
  buildDispatchReference,
  parseDispatchReference,
  parseNodeReference,
  shortForm,
} from "../refs/routes";
import { type TextSource, textSourceOf } from "../refs/text-references";
import { wordsOf } from "../refs/tree-fragment";
import { agentsOnlyIds } from "./agents-only";
import { checkFacts, resultFacts } from "./check-facts";
import {
  type ChangeComment,
  decisionChangeComments,
  decisionChanges,
  decisionsDocumentId,
  inReviewList,
} from "./decision-review";
import { artifactsBySlug } from "./issue-artifacts";
import type {
  IssueTreeReads,
  NodeAnswer,
  NodeChip,
  NodeCounts,
  NodeFact,
  NodeOption,
  TreeAsk,
} from "./issue-tree";
import type { LensFacts } from "./lenses/lens-drafts";
import {
  type AskState,
  blockAttributeLabel,
  type CheckState,
  coverageGapKind,
  type DecisionState,
  type GapKind,
  NODE_STATE_WORDS,
  NODE_TYPE_NAMES,
  NODE_TYPES,
  type NodeState,
  type NodeType,
  PRESET_NAMES,
} from "./page-words";

/**
 * One draft node per read of the issue page, for `buildIssueTree` to place
 * (dispatch://INFRA-1196 R4, `dispatch://INFRA-1196/artifact/plan-md#b-t13`).
 *
 * Every function here is pure and takes the reads as its input. A builder may enrich a draft an
 * earlier one made - the decisions read names a block of `decisions.md` the document's own blocks
 * already drew, the coverage read names a block of the spec - so the order they run in is the
 * order `treeDrafts` calls them in, and each says what it adds.
 *
 * A node's own words come from the read that carries them: the coverage read for a requirement,
 * the results read for a check and its results, the decisions read for a decision, the
 * conversation for an ask, a comment and a message, and the one batched reference read for every
 * other block, whose `name` for a block is that block's stored excerpt. No document's text is
 * read, and no request is made per node. Where the read sends the server's reading of those
 * words beside them - every one of these reads does, but for a result - the draft keeps it with
 * the record the words are, which is what the row draws (`TreeNode.tree`).
 *
 * The tree is the issue's conversation and what it revealed (dispatch://INFRA-1196 R1, R3): a
 * document, an artifact and a handoff are nodes of the issue and not of its conversation, so no
 * builder here drafts one. A document's typed blocks are drafted without it, under the issue
 * unless the model gives a block a parent of its own (R4). The issue's children are rows of the
 * tree, one group of them where the model places them, which today is under the issue: the page
 * draws no section of them above the outline (the architect's ruling 5 on R12).
 */

/** The id of the row of one block of one document, which is the block's own address inside the
 *  issue: a block id alone is unique only inside its document, and an issue carries several. */
export function blockNodeId(slug: string, blockId: string): string {
  return `${slug}#b-${blockId}`;
}

/** One node as it is being built: a `TreeNode`'s own fields, plus where the model placed it. */
export interface Draft {
  /** When it was made, where its read records one (`TreeNode.at`). */
  at?: string;
  author?: Actor;
  /** A person's answer to this ask, which the ask's own row and body draw (`TreeNode.answer`). */
  answer?: NodeAnswer;
  chips: NodeChip[];
  /** What the coverage read counts for a requirement it holds (`addRequirements`); absent on
   *  every other draft, a `requirement` block no coverage row holds among them, which is how the
   *  review list tells a statement from a block (`treeDrafts`). */
  counts?: NodeCounts;
  /** What the node's body draws under its text (`check-facts.ts`). */
  facts: NodeFact[];
  gaps: GapKind[];
  readonly id: string;
  /** The child issue this node is, as the issue read carries it (`TreeNode.child`). */
  child?: IssueChild;
  /** The person who kept this decision as it reads now (`TreeNode.keptBy`). */
  keptBy?: Actor;
  /** The lens facts of a lens, a finding, a risk, a constraint or a statement a constraint names
   *  (`TreeNode.lens`). */
  lens?: LensFacts;
  /** Whether this ask takes more than one of its options (`TreeNode.multiple`). */
  multiple?: true;
  /** The choices an ask offers (`TreeNode.options`). */
  options?: NodeOption[];
  order: number;
  /** The nodes this one sits under, in the order the model placed them: the first is where it is
   *  edited, every other a transclusion. `[]` is "under the issue". */
  parents: string[];
  /** What each `block[]` attribute of the block this node is points at, by the attribute's own
   *  name; empty for a node that is no block. */
  pointers: Readonly<Record<string, readonly string[]>>;
  question: boolean;
  /** The id the tree read (`tree-read/tree-nodes.ts`'s `treeDraftsFrom`) gives the node this
   *  draft was made from (`TreeNode.readId`); absent on a draft made from another read. */
  readId?: string;
  reference?: string;
  replaced: boolean;
  /** The record `text` is, where the draft's read names one (`TreeNode.source`). */
  source?: TextSource;
  state?: NodeState;
  text: string;
  /** The server's reading of `text`, where the draft's read sends one (`TreeNode.tree`). */
  tree?: DisplayTree;
  type: NodeType;
  /** Whether the model marks this decision unsure (`Decision.unsure`). */
  unsure?: true;
  /** Whether this node waits on the reader: a decision in their review list (`inReviewList`),
   *  or an open question whose turn is the reader's own (`waiting_on_person`). */
  waiting: boolean;
}

/**
 * Where a child of the issue sits among the issue's own children: the typed blocks of its
 * documents first, the spec's before the rest, each in its own document's order, so the
 * questions inside the spec open at the top of the tree; then the decisions no question holds,
 * then the requirements, then the group of its child issues, and the conversation last: the
 * questions asked of the issue itself and the turns, together, in the order they were made
 * (`orderConversation`), so a question and a comment the reader writes from the line at the
 * foot both land at the outline's end, where the reader is taken once it is sent. A node's order
 * under any other parent is its own place in what it came from.
 */
export const ROOT_ORDER = {
  /** The first block of the issue's spec; every other document's blocks follow, one whole
   *  number per document (`documentRanks`), which keeps them under the decisions' `1000`. */
  block: 0,
  /** The group of child issues, each a fraction of one past it in the issue read's order, so
   *  however many children an issue has none reaches the conversation's orders. */
  child: 3000,
  /** A question of the issue's own and a turn: `orderConversation` counts up from here. */
  conversation: 4000,
  decision: 1000,
  requirement: 2000,
} as const;

/** What every builder needs of the reads: how this issue addresses a node, and what a reference
 *  points at. Held as one value so the placement and the names are read the same way everywhere. */
export interface TreeContext {
  /** A chip naming what `reference` points at, by the one batched reference read and the
   *  issue's own documents (`treeContext`): one chip per reference, the same value on every
   *  call. */
  readonly chipOf: (reference: string) => NodeChip;
  /** The words of the stored excerpt the batched reference read names `reference`'s target by
   *  (`ReferenceTarget.name`), with a typed block's directive left out (`blockWords`); `""` for
   *  a target the read has not answered for and for one nothing answers to now. It is a block's
   *  source, cut at 480 runes and unread, so no chip draws it: it is where a node's citations
   *  are read from beside its words, and a block's words where the read sent no reading. */
  readonly excerptOf: (reference: string) => string;
  readonly key: string;
  /** The node `reference` names, as this issue addresses it; undefined for anything else. */
  readonly nodeIdOf: (reference: string) => string | undefined;
  /** The slug of the issue's spec, `""` for an issue with none. */
  readonly specSlug: string;
  /** The slug of one document of the issue, by its artifact id. */
  readonly slugOf: (artifactId: string) => string | undefined;
  /** The server's reading of what `reference` points at, where the batched reference read sent
   *  one: a typed block's own inner content, whole, references kept (`ReferenceTarget.text_tree`). */
  readonly treeOf: (reference: string) => DisplayTree | undefined;
  /** The reader's login, which an open question waits on where its turn is theirs; undefined
   *  until the login resolves, and nothing waits on nobody. */
  readonly viewer: string | undefined;
}

/**
 * The reads as every builder reads them. `lineOf` is the one line a place that draws one line
 * for a node draws, from the server's reading of the node's words: `buildIssueTree` and
 * `targetNames` give R1's rule over the words of the reading's first block (`chipLine`, which
 * is `drawnLine(firstLineOf(…))` of what `lineWords` reads), which this module cannot import,
 * since `issue-tree.ts` imports it, and a caller that reads no chip's name gives none, which
 * names a block by the whole reading's words (`wordsOf`).
 *
 * A chip names its target by the words a reader reads it by, and never by markup:
 *
 * - **A target nothing answers to now** by nothing, `""`, which the chip draws as gone. A short
 *   reference two or more targets carry the id of is such a target, which the read answers
 *   `ambiguous` as well, and its chip carries `ambiguous` so it can draw the reference as
 *   written rather than as a target deleted (dispatch://INFRA-1196 decision 62).
 * - **A block** - the reference names one, or the read answers that its target is one - by
 *   `lineOf` of the server's reading of it (`ReferenceTarget.text_tree`). Where the read
 *   answered and sent no reading, or one that holds no words, a block of this issue whose
 *   document the tree read is named by the page's word for its kind (`NODE_TYPE_NAMES`), never
 *   by its id (R22); another issue's block, whose kind no read of this page gives, by `""`. A
 *   block the read has not answered for yet is `""` too, which the chip draws as not yet named
 *   (`NodeChip.answered`). The read's `name` for a block is its stored excerpt, which is the
 *   block's source: a typed block's directive and the markdown its words are written in are
 *   markup and no word of it (R21).
 * - **A whole document** - a `spec` or an `artifact` reference naming no block - by the page's
 *   word for it (`documentWord`), and never by its file name.
 * - **Anything else** by the read's `name` for it. An ask's, a comment's and a message's name is
 *   the question or the body as stored, which is markdown whose marks are no word of it (R21),
 *   so it is read to plain words (`plainWords`); an issue's and a component's name is a title,
 *   which is no markdown, and is left as the read gives it.
 */
export function treeContext(
  reads: IssueTreeReads,
  lineOf: (reading: DisplayTree) => string = wordsOf
): TreeContext {
  const key = reads.issue.key;
  const artifacts = reads.issue.artifacts ?? [];
  const slugs = new Map(artifacts.map((artifact) => [artifact.id, artifact.slug]));
  const documents = artifactsBySlug(reads.issue);
  const spec = specOf(reads.issue);
  const specSlug = spec?.slug ?? "";
  const names = new Map((reads.targets ?? []).map((target) => [target.ref, target]));
  // The kind of each typed block of the documents the tree read, by its node id, which is all a
  // chip can say of a block of this issue the read sent no words for.
  const types: readonly string[] = NODE_TYPES;
  const kinds = new Map<string, NodeType>();
  for (const document of reads.documents ?? []) {
    const slug = slugs.get(document.artifactId);
    if (slug === undefined) continue;
    for (const block of document.blocks) {
      if (types.includes(block.type)) {
        kinds.set(blockNodeId(slug, block.id), block.type as NodeType);
      }
    }
  }
  const nodeIdOf = (reference: string): string | undefined => {
    const route = parseNodeReference(reference);
    if (route === undefined || !("key" in route) || route.key !== key) return undefined;
    const block = "block" in route ? route.block : undefined;
    if (route.kind === "spec") {
      return block === undefined ? specSlug : blockNodeId(specSlug, block.id);
    }
    if (route.kind === "artifact") {
      return block === undefined ? route.slug : blockNodeId(route.slug, block.id);
    }
    if (route.kind === "ask" || route.kind === "comment" || route.kind === "message") {
      return route.id;
    }
    return route.kind === "issue" ? key : undefined;
  };
  const nameOf = (reference: string, target: ReferenceTarget | undefined): string => {
    const route = parseNodeReference(reference);
    const block = route !== undefined && "block" in route ? route.block : undefined;
    if (block !== undefined || target?.kind === "block") {
      const reading = target?.text_tree;
      const line = reading === undefined ? "" : lineOf(reading);
      if (line !== "" || target === undefined) return line;
      const kind = kinds.get(nodeIdOf(reference) ?? "");
      return kind === undefined ? "" : NODE_TYPE_NAMES[kind];
    }
    // Another issue's document is named by no role of this issue's.
    if (route?.kind === "spec") {
      return documentWord(reads.issue, route.key === key ? spec : undefined);
    }
    if (route?.kind === "artifact") {
      return documentWord(reads.issue, route.key === key ? documents.get(route.slug) : undefined);
    }
    if (target?.kind === "ask" || target?.kind === "comment" || target?.kind === "message") {
      return plainWords(target.name);
    }
    return blockWords(target?.name ?? "");
  };
  // One chip per reference, made the first time it is asked for: a tree asks once for every
  // citation of a target, and no caller changes a chip - its fields are read-only (`NodeChip`),
  // and a caller that adds one, a `needs` label or a link's `stale`, spreads it into a chip of
  // its own - so every place that cites the target holds the same one.
  const chips = new Map<string, NodeChip>();
  return {
    chipOf: (reference: string): NodeChip => {
      const held = chips.get(reference);
      if (held !== undefined) return held;
      const target = names.get(reference);
      const gone = target?.gone ?? false;
      const chip: NodeChip = {
        ...(target?.ambiguous === true ? { ambiguous: true } : {}),
        answered: target !== undefined,
        gone,
        name: gone ? "" : nameOf(reference, target),
        ref: reference,
      };
      chips.set(reference, chip);
      return chip;
    },
    excerptOf: (reference: string): string => {
      const target = names.get(reference);
      return target === undefined || target.gone ? "" : blockWords(target.name);
    },
    key,
    nodeIdOf,
    specSlug,
    slugOf: (artifactId: string) => slugs.get(artifactId),
    treeOf: (reference: string) => names.get(reference)?.text_tree,
    viewer: reads.viewer,
  };
}

/**
 * The issue's spec: the document the issue read names by its id (`primarySpec`), the one the
 * tree reads the blocks of (`useIssueTree.ts`), so a reference to the spec resolves against the
 * document whose blocks are drawn. The document's own flag answers only where the issue names
 * no spec (`""`), as `documentWord` reads it.
 */
function specOf(issue: IssueDetails): Artifact | undefined {
  return issue.primary_artifact_id === ""
    ? issue.artifacts?.find((artifact) => artifact.primary)
    : primarySpec(issue);
}

/**
 * The page's word for one document of the issue, which a chip naming the whole document draws:
 * the name of the filter that draws what it holds (`PRESET_NAMES`) for the spec and for the
 * plan, Verification and decisions documents, and the page's word for a document
 * (`NODE_TYPE_NAMES.document`) for any other, another issue's included (`undefined`). A
 * document's own name is its file name, `spec.md`, which is no word a reader reads it by (R21).
 *
 * Which document plays which part is the issue read's answer, the id it names for each
 * (`primary_artifact_id`, `plan_artifact_id`, `verification_artifact_id`,
 * `decisions_artifact_id`), the same ids the tree reads the documents' blocks by
 * (`useIssueTree.ts`): the artifact's own `role` is absent on a document a rolling deploy left
 * with none, which the server still finds by its slug. The document's own flag and role answer
 * only where the issue names no id for that part: no spec (`""`), and a Dispatch older than
 * document roles (absent).
 */
function documentWord(
  issue: IssueDetails,
  document: Pick<ArtifactWithTrees, "id" | "primary" | "role"> | undefined
): string {
  if (document === undefined) return NODE_TYPE_NAMES.document;
  const spec = issue.primary_artifact_id;
  if (spec === "" ? document.primary : document.id === spec) return PRESET_NAMES.spec;
  for (const [role, id] of [
    ["plan", issue.plan_artifact_id],
    ["verification", issue.verification_artifact_id],
    ["decisions", issue.decisions_artifact_id],
  ] as const) {
    if (id === undefined ? document.role === role : document.id === id) return PRESET_NAMES[role];
  }
  return NODE_TYPE_NAMES.document;
}

/** A directive's own opening line, `:::plan_task{#t1 serves="…"}`, and its closing fence. */
const DIRECTIVE_OPENER = /^:{3,}[a-z_]/;
const DIRECTIVE_FENCE = /^:{3,}\s*$/;

/**
 * The words of the name the batched reference read gives a target (`ReferenceTarget.name`).
 * The read names a document's block by its stored excerpt, which is the block's source: a typed
 * block is a directive, so its excerpt opens on the directive's line - its type and its
 * attributes - and, where the excerpt holds the whole block, ends on its closing fence. Both are
 * markup and no word of the block (R21), so they are left out. Any other name is left as the
 * read gives it.
 */
export function blockWords(name: string): string {
  const lines = name.trimEnd().split("\n");
  if (!DIRECTIVE_OPENER.test(lines[0] ?? "")) return name;
  lines.shift();
  if (DIRECTIVE_FENCE.test(lines.at(-1) ?? "")) lines.pop();
  return lines.join("\n").trim();
}

/** A blank line, which ends a paragraph: no mark and no code span reaches past one. A stored
 *  text's blocks are read one at a time between them (`plainWords`, `lineWords`). */
export const BLANK_LINE = /\n\s*\n/u;

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

/**
 * The markers a line of a blockquote or a list item opens on, at the start of the line, each
 * container's in turn as they nest: a blockquote's `>` and the one space or tab after it, which
 * markdown allows up to three spaces of indent before, once per quote (`> > deep`); and a
 * bullet (`-`, `+` or `*`) or an ordered marker (one to nine digits and `.` or `)`), which must
 * be followed by a space or a tab, after any indent, since a nested item carries its parent's,
 * with a task box (`[ ]`, `[x]`) after it, which the server's reading holds as the item's state
 * and no word of it. `**bold**` at a line's start opens on no marker, since its first `*` is
 * followed by another and not by white space; nor does what `LITERAL` read, which is held as a
 * stand-in by then, so `1\.` and `\-` stay words.
 */
const BLOCK_MARKER =
  /^(?: {0,3}>[ \t]?|[ \t]*(?:[-+*]|\d{1,9}[.)])[ \t]+(?:\[[ \txX]\][ \t]+)?)+/gmu;

/** A heading marker at the start of a line, `#` to `######` and a space, which markdown allows
 *  up to three spaces of indent before, and which a blockquote's or a list item's markers
 *  (`BLOCK_MARKER`) may stand before. */
const HEADING_MARKER = /^ {0,3}#{1,6}[ \t]+/gmu;

/** An image, `![alt](src)`, whose words the server's reading of it holds none of. */
const IMAGE = /!\[[^\]]*\]\([^)]*\)/gu;

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

/** Each ASCII punctuation character, in code order, as `PUNCTUATION` picks them out of the
 *  printable ASCII characters but the space: the 32 each hold of `Holds` has a stand-in for. */
const PUNCTUATION_CHARACTERS: readonly string[] =
  String.fromCharCode(...Array.from({ length: 0x7f - 0x21 }, (_, at) => 0x21 + at)).match(
    PUNCTUATION
  ) ?? [];

/** The private-use code points, in the order a text's stand-ins are taken from them: the Basic
 *  Multilingual Plane's area, U+E000 to U+F8FF, then plane 15's and plane 16's. */
const PRIVATE_USE: readonly (readonly [first: number, last: number])[] = [
  [0xe000, 0xf8ff],
  [0xf0000, 0xffffd],
  [0x100000, 0x10fffd],
];

/** One private-use character a text holds: an author's own, or a stand-in `Holds` chose. */
const PRIVATE_USE_CHARACTER = /\p{Co}/gu;

/** One hold's stand-ins: each ASCII punctuation character's (`held`), and the character each
 *  stand-in is held for (`back`). */
interface Hold {
  readonly back: ReadonlyMap<string, string>;
  readonly held: ReadonlyMap<string, string>;
}

/**
 * The private-use stand-ins one text's `LITERAL` characters are held as while the marks are
 * taken off, in two holds that `plainWords` puts back at two times. An escaped character
 * (`escaped`) is put back before bare addresses are written short, so `see dispatch://CORE-2\.`
 * reads `see CORE-2.`. A code span's content and an autolink's URL (`verbatim`) are put back
 * after, so an address in code stays as its author wrote it, and an autolink's, written short as
 * it is read, is not read again.
 */
interface Holds {
  readonly escaped: Hold;
  readonly verbatim: Hold;
}

/** The holds of a text `LITERAL` reads nothing of, which hold nothing and put nothing back. */
const NO_HOLDS: Holds = {
  escaped: { back: new Map(), held: new Map() },
  verbatim: { back: new Map(), held: new Map() },
};

/**
 * The holds of `text`, their stand-ins chosen for it alone: the private-use code points in order
 * (`PRIVATE_USE`), skipping each one the text holds, the escaped hold's 32 first and the verbatim
 * hold's 32 next. A stand-in is so never a character the author wrote, and an author's own
 * private-use character - an icon font's, a pasted terminal glyph - reads as written. Undefined
 * for a text that holds so many private-use characters that fewer than 64 are left free.
 */
function holdsFor(text: string): Holds | undefined {
  const free = freePrivateUse(new Set(text.match(PRIVATE_USE_CHARACTER)));
  const hold = (): Hold | undefined => {
    const back = new Map<string, string>();
    const held = new Map<string, string>();
    for (const character of PUNCTUATION_CHARACTERS) {
      const standIn = free.next();
      if (standIn.done === true) return undefined;
      back.set(standIn.value, character);
      held.set(character, standIn.value);
    }
    return { back, held };
  };
  const escaped = hold();
  const verbatim = hold();
  return escaped === undefined || verbatim === undefined ? undefined : { escaped, verbatim };
}

/** Each private-use character, in `PRIVATE_USE`'s order, that is none of `taken`. */
function* freePrivateUse(taken: ReadonlySet<string>): Generator<string, void> {
  for (const [first, last] of PRIVATE_USE) {
    for (let point = first; point <= last; point += 1) {
      const character = String.fromCodePoint(point);
      if (!taken.has(character)) yield character;
    }
  }
}

/** `text` with each stand-in of `hold` the ASCII character it is held for, and every other
 *  character, an author's own private-use one included, as written. */
function putBack(text: string, hold: Hold): string {
  return hold.back.size === 0
    ? text
    : text.replace(PRIVATE_USE_CHARACTER, (character) => hold.back.get(character) ?? character);
}

/**
 * The words of a stored markdown text as a reader reads them, on one line: what a place that
 * draws a stored text as plain words draws, since its marks are markup and no word of it (R21),
 * and a mark after a sentence's end (`today.**`) would hide that end from `firstLineOf`.
 *
 * Each paragraph is read on its own, since no mark pairs across a blank line. What markdown
 * reads literally (`LITERAL`) - an escaped punctuation character (`1\.` is `1.`), a code span's
 * content, an autolink's URL, which reads as a link's words read, its address written short - is
 * read first, and each ASCII punctuation character of it is held as a private-use stand-in while
 * the marks are taken off, so no mark is read inside it. The stand-ins are chosen for each text
 * from the private-use code points it does not hold (`holdsFor`) and read back by what each was
 * chosen for, so an author's own private-use character - an icon font's, a pasted terminal
 * glyph - is no stand-in and reads as written. Then a blockquote's and a list item's markers at
 * the start of any line go (`BLOCK_MARKER`), and a heading marker after them, an image goes
 * whole, a link reads as its words, and strong, emphasis and struck marks go until none is left,
 * so nested marks (`***x***`) go too. The escaped characters are put back, and white space
 * collapses to single spaces.
 *
 * Then a bare `dispatch://` address reads as its target's short form (`addressesAsShortForms`,
 * decision 95): every place that reads a stored text to plain words draws it on one line - a
 * node's line where the node has no reading (`lineWords`), and the name of a chip naming an
 * ask, a comment or a message (`treeContext`) - and no one-line view draws a raw address (R22).
 * A link already reads as its words, so `[the spec](dispatch://…)` reads `the spec`. Last, the
 * characters of code spans and autolinks are put back, so an address in code reads as its
 * author wrote it, as a rendered body draws it: a code span holding
 * `dispatch read dispatch://CORE-2` keeps its address.
 *
 * A text `LITERAL` reads something of that holds so many private-use characters that fewer than
 * the 64 stand-ins it needs are left free - more than 137,404 of the 137,468 - has nothing to
 * hold its literal characters as, and reads as its own words with white space collapsed and no
 * mark taken off: markup shown is less wrong than characters changed.
 */
export function plainWords(markdown: string): string {
  return plainWordsWith(markdown, addressesAsShortForms);
}

/** The words of a stored markdown text as `plainWords` reads them, with no address written
 *  short: its marks, markers, links, images, escapes and code spans read as they read there, an
 *  author's own private-use character as written, and a bare address and an autolink's URL stay
 *  as written (dispatch://INFRA-1196 decision 140). A caller that names each reference itself
 *  reads these, since a short form leaves it no address to name. */
export function plainWordsRaw(markdown: string): string {
  return plainWordsWith(markdown, (words) => words);
}

/** The words `plainWords` reads, each bare address in them and each autolink's URL written by
 *  `shorten` where `plainWords` writes it short (`addressesAsShortForms`). A text `LITERAL` reads
 *  nothing of holds nothing, so no stand-in is chosen for it and none is put back. */
function plainWordsWith(markdown: string, shorten: (words: string) => string): string {
  const holds = markdown.search(LITERAL) === -1 ? NO_HOLDS : holdsFor(markdown);
  if (holds === undefined) return markdown.replace(/\s+/gu, " ").trim();
  const paragraphs = markdown.split(BLANK_LINE).map((paragraph) => {
    let words = paragraph
      .replace(LITERAL, (written, escaped?: string, _run?: string, code?: string, url?: string) => {
        const hold = escaped === undefined ? holds.verbatim : holds.escaped;
        const literal = escaped ?? code ?? (url === undefined ? undefined : shorten(url));
        return (
          literal?.replace(PUNCTUATION, (character) => hold.held.get(character) ?? character) ??
          written
        );
      })
      .replace(BLOCK_MARKER, "")
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
    return putBack(words, holds.escaped);
  });
  const words = paragraphs.join(" ").replace(/\s+/gu, " ").trim();
  return putBack(shorten(words), holds.verbatim);
}

/**
 * `words` with each bare `dispatch://` address in them written as its target's short form, as a
 * link to it draws it (`shortForm`): `dispatch://CORE-2` reads `CORE-2`, and
 * `dispatch://INFRA-1196/spec#b-r1` reads `INFRA-1196 spec block`. It is for the words a place
 * that draws one line for a node cuts that line from, since no one-line view draws a raw address
 * (dispatch://INFRA-1196 R22, decision 95): the server's reading keeps a bare address in a node's
 * words as a plain text run, which is no link, and a stored text holds it as written, so both
 * meet it.
 *
 * An address is found as the page finds a reference in text (`referenceSpans`), and only the
 * address itself is written short: what `trimReference` cuts off its end stays where it stood, so
 * `see dispatch://CORE-2.` reads `see CORE-2.`. A span that names no target
 * (`parseDispatchReference`) is left as written.
 */
export function addressesAsShortForms(words: string): string {
  let shortened = "";
  let cursor = 0;
  for (const { start, value } of referenceSpans(words, "dispatch://")) {
    const route = parseDispatchReference(value);
    if (route === undefined) continue;
    shortened += `${words.slice(cursor, start)}${shortForm(route)}`;
    cursor = start + value.length;
  }
  return shortened + words.slice(cursor);
}

/** Every draft the reads hold, by its node id. */
export function treeDrafts(reads: IssueTreeReads, context: TreeContext): Map<string, Draft> {
  const drafts = new Map<string, Draft>();
  addDocumentBlocks(drafts, reads, context);
  addChildren(drafts, reads);
  const asks = addAsks(drafts, reads, context);
  const placed = decisionChangeComments(
    reads.decisions,
    reads.comments,
    reads.issue.artifacts,
    reads.documents
  );
  const changes = decisionChanges(
    reads.decisions,
    reads.comments,
    reads.issue.artifacts,
    reads.documents,
    placed
  );
  addTurns(drafts, reads, context, asks, placed);
  orderConversation(drafts);
  addDecisions(drafts, reads, context, asks, changes);
  addRequirements(drafts, reads, context);
  addChecks(drafts, reads, context);
  // What waits for the reader's review is the review list's one rule (R35): the tie to a
  // statement decides it, and a decision the reader asked to change waits on its agent, not on
  // the reader (`decision-review.ts`). A statement is a requirement the coverage read holds - a
  // draft `addRequirements` gave that read's counts - as the header's count is handed the
  // coverage read's statements (`waitingOnReader`'s `isStatement`): a spec's `requirement` block
  // drawn before that read answers, and one of another document, which no coverage row holds, is
  // a block and nothing more, and a tie to it puts no decision on the list. It is read once every
  // statement has its draft. Nothing waits on a reader whose login has not resolved, as
  // `waiting.ts` counts it.
  for (const decision of reads.decisions?.decisions ?? []) {
    const draft = drafts.get(context.nodeIdOf(decision.ref) ?? "");
    if (draft === undefined) continue;
    draft.waiting =
      context.viewer !== undefined &&
      inReviewList(
        decision,
        (reference) => drafts.get(context.nodeIdOf(reference) ?? "")?.counts !== undefined,
        changes.has(decision.ref)
      );
  }
  servePlacements(drafts, context);
  addDocumentChips(drafts, reads, context);
  return drafts;
}

/**
 * The conversation's own nodes under the issue - a question asked of the issue itself and each
 * turn - in the one order they were made, a question among the comments it was asked between
 * (`ROOT_ORDER.conversation`): the questions and the reader's threads are the Contents list's
 * entries in that order, as the approved pictures draw them, and a write from the line at the
 * foot is the newest, so it lands at the end, where the page takes the reader once it is sent.
 * Two made in the same instant are drawn by id, so the same reads always draw the same tree.
 */
function orderConversation(drafts: Map<string, Draft>): void {
  const conversation = [...drafts.values()].filter(
    (draft) => draft.order >= ROOT_ORDER.conversation
  );
  conversation.sort(
    (one, other) =>
      compareTimestamps(one.at ?? "", other.at ?? "") || one.id.localeCompare(other.id)
  );
  conversation.forEach((draft, index) => {
    draft.order = ROOT_ORDER.conversation + index;
  });
}

/**
 * A chip that opens each document of the issue a node's own words cite (R3): a document is no
 * node of the outline, so a block that names one carries a chip of its own for it, which opens
 * the document at its own address in place of the outline (`NodeRow.tsx`'s `Chip`). The chip is
 * named by the page's word for the document, as every chip naming a whole document is
 * (`documentWord`): the filter's name for the spec and for the plan, Verification and decisions
 * documents, and "document" for any other, never the file name the issue's own read carries. It
 * addresses the document as `artifact/<slug>` - the spec too, whose `spec` reference is the Spec
 * filter's address. A citation of one block of a document names that block, which the text's
 * own link opens, and no chip.
 */
export function addDocumentChips(
  drafts: Map<string, Draft>,
  reads: IssueTreeReads,
  context: TreeContext
): void {
  const bySlug = artifactsBySlug(reads.issue);
  if (bySlug.size === 0) return;
  for (const draft of drafts.values()) {
    // The words a node is drawn by can leave a citation out - the coverage read drops every
    // `://` word of a requirement (`api/results.go`'s `blockWords`) - so the node's own source,
    // the excerpt the reference read names it by, is read as well.
    const source = draft.reference === undefined ? "" : context.excerptOf(draft.reference);
    for (const span of [
      ...referenceSpans(draft.text, "dispatch://"),
      ...referenceSpans(source, "dispatch://"),
    ]) {
      const route = parseNodeReference(span.value);
      if (route === undefined || !("key" in route) || route.key !== context.key) continue;
      if ("block" in route && route.block !== undefined) continue;
      const slug =
        route.kind === "artifact" ? route.slug : route.kind === "spec" ? context.specSlug : "";
      const artifact = bySlug.get(slug);
      if (artifact === undefined) continue;
      const ref = buildDispatchReference({ key: context.key, kind: "artifact", slug });
      if (draft.chips.some((chip) => chip.ref === ref)) continue;
      draft.chips = [
        ...draft.chips,
        { answered: true, gone: false, name: documentWord(reads.issue, artifact), ref },
      ];
    }
  }
}

/** Where each document of the issue stands among its own, by its artifact id: the spec
 *  (`specOf`) first and the rest in the order the issue carries them, which is the order their
 *  blocks are drawn in under the issue (`ROOT_ORDER.block`). */
function documentRanks(reads: IssueTreeReads): ReadonlyMap<string, number> {
  const specId = specOf(reads.issue)?.id;
  const ranks = new Map<string, number>();
  (reads.issue.artifacts ?? []).forEach((artifact, index) => {
    ranks.set(artifact.id, artifact.id === specId ? 0 : index + 1);
  });
  return ranks;
}

/** Puts `draft` in, over whatever draft held its id. */
function add(drafts: Map<string, Draft>, draft: Draft): Draft {
  drafts.set(draft.id, draft);
  return draft;
}

/** A draft with nothing on it but its id and its type, for a builder to fill in. */
export function blank(id: string, type: NodeType, order: number): Draft {
  return {
    chips: [],
    facts: [],
    gaps: [],
    id,
    order,
    parents: [],
    pointers: {},
    question: false,
    replaced: false,
    text: "",
    type,
    waiting: false,
  };
}

/**
 * Each document's typed blocks, in document order. A block of no declared node type - a
 * paragraph, a heading, a table - is the document's own text and no node of the tree. A block's
 * `block[]` attributes are kept as its pointers, which is where `serves` and `needs` are read
 * from; its words come from the batched reference read, since nothing reads the document's text.
 *
 * Every block sits under the issue, and never under its document, which is no node of the tree
 * (R3, R4): a builder after this one places a block the model gives a parent of its own - a
 * requirement by its `from`, a check and a plan task by their `serves`. Among the issue's own
 * children a document's blocks keep the order they have in it, one document after another.
 */
function addDocumentBlocks(
  drafts: Map<string, Draft>,
  reads: IssueTreeReads,
  context: TreeContext
): void {
  const types: readonly string[] = NODE_TYPES;
  const ranks = documentRanks(reads);
  for (const document of reads.documents ?? []) {
    const slug = context.slugOf(document.artifactId);
    if (slug === undefined) continue;
    const rank = ranks.get(document.artifactId) ?? 0;
    document.blocks.forEach((block, index) => {
      if (!types.includes(block.type)) return;
      const reference = buildDispatchReference({
        block: { id: block.id },
        key: context.key,
        kind: "artifact",
        slug,
      });
      const pointers: Record<string, readonly string[]> = {};
      for (const attribute of block.attributes ?? []) {
        if (attribute.kind === "block[]") {
          pointers[attribute.name] = parseBlockPointerValue(attribute.value) ?? [];
        }
      }
      // The document's rank, and the block's place inside it as a fraction of one, so no two
      // documents' blocks interleave.
      const order = ROOT_ORDER.block + rank + index / document.blocks.length;
      const draft = add(drafts, blank(blockNodeId(slug, block.id), block.type as NodeType, order));
      draft.pointers = pointers;
      draft.reference = reference;
      draft.text = context.excerptOf(reference);
      // The block's own words whole, as the server reads them, where the reference read sent
      // them: the excerpt above is the block's stored source, cut at 480 runes and unread.
      const tree = context.treeOf(reference);
      if (tree !== undefined) {
        draft.tree = tree;
        draft.source = textSourceOf("artifact", document.artifactId);
      }
      // A check with no result recorded against it is in no state the results read would name,
      // and `no-result` is that state (`NODE_STATE_WORDS.check`), so a check drawn before the
      // results read answers says so rather than drawing a state nothing recorded.
      if (block.type === "check") draft.state = "no-result";
      if (block.type === "ask") draft.state = "open";
    });
  }
}

/**
 * The issue's children, each a node of the tree and a row of the outline, one group of them in
 * the issue read's order: its title as its words, and how it stands, what blocks it, who holds it
 * and its key as the tag drawn from the child itself (the architect's ruling 5 on R12,
 * `IssueChildren.tsx`). The model places a child under the words that filed it once it holds
 * that edge (dispatch://INFRA-1253); today it holds none, so the group sits under the issue. A
 * child's own tree is its own page, so a child holds no node below it.
 */
function addChildren(drafts: Map<string, Draft>, reads: IssueTreeReads): void {
  const children = reads.issue.children ?? [];
  children.forEach((child, index) => {
    const order = ROOT_ORDER.child + index / children.length;
    const draft = add(drafts, blank(child.key, "child_issue", order));
    draft.child = child;
    draft.text = child.title;
  });
}

/**
 * Every question asked of the issue: the block the server indexed it from, where it has one,
 * else a node of its own under the issue. An approval request belongs to the document it reviews
 * rather than to the reasoning, so the tree draws none.
 *
 * A person's answer is part of the ask and no node of its own (dispatch://INFRA-1150 R3): it is
 * kept on the ask's draft as the store holds it, which the ask's own row and body draw.
 *
 * Answers the node id each ask is drawn under, by the ask's own id, which is how a decision
 * names the question it answers (`Decision.asks`).
 */
function addAsks(
  drafts: Map<string, Draft>,
  reads: IssueTreeReads,
  context: TreeContext
): ReadonlyMap<string, string> {
  const asks = new Map<string, TreeAsk>();
  for (const ask of [...(reads.issue.open_asks ?? []), ...(reads.asks ?? [])]) {
    if (ask.kind === "question") asks.set(ask.id, ask);
  }
  const placed = new Map<string, string>();
  for (const ask of asks.values()) {
    // The document holding the typed `ask` block is `block_artifact`; `artifact_id` is the
    // document an anchored ask is anchored in, which a block ask does not carry.
    const slug = ask.block_artifact?.slug ?? context.slugOf(ask.artifact_id ?? "");
    const block = ask.block_id ?? undefined;
    const id = slug !== undefined && block !== undefined ? blockNodeId(slug, block) : ask.id;
    const draft = drafts.get(id) ?? add(drafts, blank(id, "ask", ROOT_ORDER.conversation));
    placed.set(ask.id, id);
    draft.at = ask.created_at;
    draft.author = ask.author;
    if (ask.answer !== null) draft.answer = answerOf(ask.answer);
    if ((ask.options ?? []).length > 0) {
      draft.options = (ask.options ?? []).map((option) => ({
        ...(option.description === undefined || option.description === ""
          ? {}
          : { description: option.description }),
        label: option.label,
      }));
    }
    if (ask.multiple) draft.multiple = true;
    draft.reference = buildDispatchReference({ id: ask.id, key: context.key, kind: "ask" });
    draft.source = textSourceOf("ask", ask.id);
    draft.state = askState(ask);
    draft.text = ask.question;
    draft.tree = ask.question_tree;
    // An open question is what the fold unfolds the path to, whatever its depth (R6).
    draft.question = ask.state === "open";
    // An open question whose turn is the reader's own waits on the reader, and is what the
    // header's count of what waits puts the focus on (R13, `waiting.ts`): the person the server
    // resolved it waits on (`waiting_on_person`), as every other count of what waits on the
    // reader reads it (`inbox/BlockedOnYou.tsx`'s `waitingOnYou`). A question waiting on
    // somebody else is open all the same, and the fold still unfolds the path to it.
    draft.waiting =
      ask.state === "open" &&
      ask.waiting_on === "human" &&
      context.viewer !== undefined &&
      ask.waiting_on_person === context.viewer;
  }
  return placed;
}

/** An ask's answer as the tree holds it: the person it is theirs, with the agent they answered
 *  through where they did, and their choices and words as stored. */
export function answerOf(answer: NonNullable<Ask["answer"]>): NodeAnswer {
  return {
    at: answer.at,
    by: { id: answer.user, kind: "user", ...(answer.act === undefined ? {} : { act: answer.act }) },
    selected: answer.selected ?? [],
    text: answer.text ?? "",
  };
}

/**
 * How an ask stands, in one of the words the page declares for an ask (`NODE_STATE_WORDS.ask`):
 * the kind of its resolution for an ask that was closed rather than answered, so the tree says
 * "Declined" where the card and the margin beside it say "Declined", and the `state` the server
 * wrote for every other ask. The server types both as plain strings (`Ask.state`,
 * `AskResolution.kind`), and a token the page declares no word for is drawn as no state rather
 * than as the server's own token (R21).
 */
export function askState(ask: Ask): AskState | undefined {
  const words: Readonly<Record<string, string | undefined>> = NODE_STATE_WORDS.ask;
  const token =
    ask.state === "resolved" && ask.resolution !== undefined ? ask.resolution.kind : ask.state;
  return words[token] === undefined ? undefined : (token as AskState);
}

/** One turn of the conversation as the tree holds it: a comment and a message are the same row
 *  here, told apart by the kind they are drawn as and by the address they carry. */
interface Turn {
  /** When it was made, which is the order the conversation is read in. */
  readonly at: string;
  readonly author: Actor;
  readonly body: string;
  /** The server's reading of `body`. */
  readonly bodyTree?: DisplayTree;
  readonly id: string;
  readonly kind: "comment" | "message";
  /** The node it answers, `null` for a root turn under the issue. */
  readonly replyTo: string | null;
}

/**
 * The conversation's own turns: each comment and each message a person wrote, or that sits in a
 * thread a person wrote in, a node of this tree, a root turn under the issue and a reply under
 * the entry it answers (R1, R16). A thread head a page never read is no node, so a reply whose
 * parent is missing falls back to the issue, as every node whose parent is absent does
 * (`assemble`). A thread no person wrote in is no node at all: it is the history's, in the panel
 * (`agents-only.ts`, R15), until a person replies in it.
 *
 * One conversation is one order, so the turns are drawn by the time they were made and not by
 * their place in the read that carried them: the comments and the messages are two lists, each
 * counting from zero, and a message sent today drawn among yesterday's comments is the reader
 * reading the conversation out of order (`orderConversation`).
 *
 * A comment is drawn under the entry it answers: `answers`, the comment its write named, and
 * never `reply_to`, which that same write climbed to the head of the thread before it stored
 * (`packages/envoy/internal/dispatch/api/comment_create.go`'s `threadHeadOf`), so a reply to a
 * reply parented by `reply_to` would be drawn under the thread's head. A comment stored before
 * `comments.answers` existed (migration 20261105014808) carries none, and its thread's head is
 * then the one entry it names. A reply to an ask names the ask alone (`ask_id`) and is drawn
 * under the ask's node, whatever state the ask is in, a resolved one included
 * (dispatch://INFRA-3677). An approval request is no node (`addAsks`), so the replies under one
 * are no node either where only agents wrote them, and sit under the issue once a person has
 * written among them. A message carries no such column, so its thread stays as
 * `in_reply_to` holds it. A comment that is a change a person asked of a decision, and answers
 * nothing else, is drawn under that decision: every change newer than the decision's words, and
 * not only the newest, which `changeOf` holds by the comment's id (`decision-review.ts`'s
 * `decisionChangeComments`), so each change stands tied to the decision it changes.
 *
 * An address naming a comment or a message of the outline (`/comments/<id>`, `/messages/<id>`,
 * `?comment=`) is therefore an address of a row of this tree.
 */
function addTurns(
  drafts: Map<string, Draft>,
  reads: IssueTreeReads,
  context: TreeContext,
  asks: ReadonlyMap<string, string>,
  changeOf: ReadonlyMap<string, string>
): void {
  // The asks `addAsks` drew its nodes from, which is what tells a reply under a question the
  // tree draws from one under an approval request, which it does not.
  const history = agentsOnlyIds(reads.comments, reads.messages, [
    ...(reads.issue.open_asks ?? []),
    ...(reads.asks ?? []),
  ]);
  const turns: Turn[] = [
    ...(reads.comments ?? []).map((comment) => ({
      at: comment.created_at,
      author: comment.author,
      body: comment.body,
      bodyTree: comment.body_tree,
      id: comment.id,
      kind: "comment" as const,
      replyTo:
        comment.answers ??
        comment.reply_to ??
        (comment.ask_id === null
          ? (context.nodeIdOf(changeOf.get(comment.id) ?? "") ?? null)
          : (asks.get(comment.ask_id) ?? comment.ask_id)),
    })),
    ...(reads.messages ?? [])
      .flatMap((thread) => [thread.message, ...(thread.replies ?? [])])
      .map((message) => ({
        at: message.created_at,
        author: message.author,
        body: message.body,
        bodyTree: message.body_tree,
        id: message.id,
        kind: "message" as const,
        replyTo: message.in_reply_to,
      })),
  ].filter((turn) => !history.has(turn.id));
  for (const turn of turns) {
    const draft = add(drafts, blank(turn.id, turn.kind, ROOT_ORDER.conversation));
    draft.at = turn.at;
    draft.author = turn.author;
    draft.parents = turn.replyTo === null ? [] : [turn.replyTo];
    draft.reference = buildDispatchReference({ id: turn.id, key: context.key, kind: turn.kind });
    draft.source = textSourceOf(turn.kind, turn.id);
    draft.text = turn.body;
    draft.tree = turn.bodyTree;
  }
}

/**
 * Each decision of the issue's `decisions.md`, under the question it answers (R4): the asks its
 * blocks mention (`Decision.asks`), the first of them where it is edited and every other one a
 * transclusion, as a check sits under each requirement it serves. An ask the tree draws no node
 * for - another issue's, or an approval request - places nothing and stays the decision's chip,
 * so a decision that names no question of this issue sits under the issue, as any node whose
 * spine parent is absent does, and never inside `decisions.md`, which is no node at all. It
 * carries no gap for that: a lack every such decision shares tells none of them apart (R10). It
 * names every spec block it links as a chip, since a link places nothing (`shaped` is
 * dispatch://INFRA-1150's, `packages/contracts/src/dispatch-api.ts:2519-2534`).
 */
function addDecisions(
  drafts: Map<string, Draft>,
  reads: IssueTreeReads,
  context: TreeContext,
  asks: ReadonlyMap<string, string>,
  changes: ReadonlyMap<string, ChangeComment>
): void {
  // The words are a block of the decisions document, which is what their references resolve
  // against (`TreeNode.source`). The read names that document by its address, and a source
  // names a document by its id.
  const source = textSourceOf(
    "artifact",
    decisionsDocumentId(reads.issue.artifacts, reads.decisions)
  );
  (reads.decisions?.decisions ?? []).forEach((decision, index) => {
    const id = context.nodeIdOf(decision.ref);
    if (id === undefined) return;
    const held = drafts.get(id);
    const draft = add(drafts, blank(id, "decision", ROOT_ORDER.decision + index));
    const answers = decision.asks ?? [];
    // A target with no address of its own is named by nothing, so there is no chip to draw for
    // it (`GraphNode.ref` is optional). The section links a block once for each of its
    // paragraphs that links it, and names an ask once for each that cites it
    // (`api/decisions.go`), so each target is one chip, stale where any of its links is.
    const linked = new Map<string, NodeChip>();
    for (const link of decision.links ?? []) {
      const ref = link.to.ref;
      if (ref === undefined) continue;
      const stale = link.stale || linked.get(ref)?.stale === true;
      linked.set(ref, { ...context.chipOf(ref), stale });
    }
    draft.chips = [
      ...linked.values(),
      ...[...new Set(answers)]
        .filter((ask) => !asks.has(ask))
        .map((ask) =>
          context.chipOf(buildDispatchReference({ id: ask, key: context.key, kind: "ask" }))
        ),
    ];
    draft.parents = answers.flatMap((ask) => {
      const parent = asks.get(ask);
      return parent === undefined ? [] : [parent];
    });
    if (decision.changed_at !== null) draft.at = decision.changed_at;
    draft.pointers = held?.pointers ?? {};
    draft.reference = decision.ref;
    draft.replaced = decision.replaced;
    draft.source = source;
    draft.text = decision.text;
    draft.tree = decision.text_tree;
    // How the decision stands with the reader, drawn as this node's own state word and shape
    // the way every other state is (R21). Whether it waits for the reader's review is the
    // review list's rule, read once every statement has its draft (`treeDrafts`).
    draft.state = decisionState(decision, changes.has(decision.ref));
    if (decision.kept !== null && !decision.waiting_review) {
      draft.keptBy = { id: decision.kept.kept_by, kind: "user" };
    }
    if (decision.unsure) draft.unsure = true;
  });
}

/**
 * How a decision stands with the reader, in the states the page draws
 * (`NODE_STATE_WORDS.decision`, R2): replaced by a later one first, since nothing waits on it
 * any longer; then a change the reader asked for, which waits on its agent and not on the
 * reader (`decision-review.ts`); then waiting for review; then kept, by the person whose name the
 * row draws after it (`TreeNode.keptBy`). A person's own decision is in none of them: it is
 * theirs, and the row draws no state.
 */
function decisionState(decision: Decision, changed: boolean): DecisionState | undefined {
  if (decision.replaced) return "replaced";
  if (changed) return "change_requested";
  if (decision.waiting_review) return "waiting_review";
  return decision.kept === null ? undefined : "kept";
}

/**
 * Every requirement of the issue's spec sits under the node its `from` names. No requirement
 * carries one - the `requirement` block declares no attributes at all
 * (`packages/envoy/internal/dispatch/pmdoc/schema/blocks.json:70-75`, read 2026-11-05) - so each
 * sits under the issue, beside the two counts the coverage read holds for it and the gaps that
 * read already carries. An absent `from` is no gap of its own: every requirement lacks one, so it
 * tells none of them apart (R10). Where a decision claims to have shaped a requirement whose own
 * origin names another node, the requirement is placed by its `from` and the disagreement is
 * drawn as a gap, and a decision is never a requirement's parent (R4): a `from` that names one
 * places nothing, and the requirement sits under the issue.
 */
function addRequirements(
  drafts: Map<string, Draft>,
  reads: IssueTreeReads,
  context: TreeContext
): void {
  const shapedBy = new Set<string>();
  for (const decision of reads.decisions?.decisions ?? []) {
    for (const link of decision.links ?? []) {
      const shaped = link.to.ref === undefined ? undefined : context.nodeIdOf(link.to.ref);
      if (shaped === undefined) continue;
      shapedBy.add(shaped);
    }
  }
  // The spec, which a statement's words are a block of and its references resolve against.
  const specId = specOf(reads.issue)?.id;
  (reads.coverage?.requirements ?? []).forEach((requirement, index) => {
    const id = blockNodeId(context.specSlug, requirement.block_id);
    const held = drafts.get(id);
    // The `from` edge is the single placement authority (R4): the requirement's own pointer,
    // never the decision that claims to have shaped it.
    const origin = context.nodeIdOf(held?.pointers.from?.[0] ?? "");
    const draft = add(drafts, blank(id, "requirement", ROOT_ORDER.requirement + index));
    draft.chips = held?.chips ?? [];
    draft.counts = { checks: requirement.checks, planTasks: requirement.plan_tasks };
    draft.gaps = requirementGaps(requirement.gaps ?? [], origin, shapedBy.has(id));
    draft.parents = origin === undefined || drafts.get(origin)?.type === "decision" ? [] : [origin];
    draft.pointers = held?.pointers ?? {};
    draft.reference = requirement.ref;
    draft.text = requirement.text;
    // The server's reading of the statement's own words, with the references the coverage
    // read's plain words leave out: the read's own, else the one its block carried.
    const tree = requirement.text_tree ?? held?.tree;
    if (tree !== undefined) {
      draft.tree = tree;
      draft.source = held?.source ?? textSourceOf("artifact", specId);
    }
  });
}

/** The gaps on one requirement: the one the coverage read carries that the page draws, and a
 *  decision claiming to have shaped a requirement that came from elsewhere. */
export function requirementGaps(
  coverage: readonly string[],
  origin: string | undefined,
  shaped: boolean
): GapKind[] {
  const gaps = coverage.flatMap((line): GapKind[] => {
    const kind = coverageGapKind(line);
    return kind === undefined ? [] : [kind];
  });
  if (origin !== undefined && shaped) gaps.push("shaped_disagreement");
  return gaps;
}

/**
 * Each check of the issue's Verification document with the results recorded against it: the
 * check's own words, how it stands and the four attributes its body draws, and one node per
 * result under the check it was recorded against (R4, "a result under its check"), each with
 * what that result recorded (`check-facts.ts`). How it stands is the page's one reading of it,
 * at the one commit the page names for the issue's results (`checkStanding`), so the row, the
 * fold's frontier, the Verification filter's failing count and its body read one state. The
 * check's `serves` is no part of this read (`CheckBlock` carries none), so its placement is the
 * block's own pointer, below.
 */
function addChecks(drafts: Map<string, Draft>, reads: IssueTreeReads, context: TreeContext): void {
  const checks = reads.results?.checks ?? [];
  const commit = namedCommit(checks);
  for (const check of checks) {
    const slug = context.slugOf(check.artifact_id);
    if (slug === undefined) continue;
    const id = blockNodeId(slug, check.block_id);
    const held = drafts.get(id);
    const draft = add(drafts, blank(id, "check", held?.order ?? ROOT_ORDER.block));
    draft.facts = checkFacts(check);
    draft.parents = held?.parents ?? [];
    draft.pointers = held?.pointers ?? {};
    draft.reference = check.ref;
    draft.state = checkStanding(check, commit).state;
    draft.text = check.text;
    const tree = check.text_tree ?? held?.tree;
    if (tree !== undefined) {
      draft.tree = tree;
      draft.source = held?.source ?? textSourceOf("artifact", check.artifact_id);
    }
    (check.results ?? []).forEach((result, index) => {
      const row = add(drafts, blank(result.id, "result", index));
      row.at = result.created_at;
      row.author = result.actor;
      row.facts = resultFacts(result);
      row.parents = [id];
      row.text = result.observed;
    });
  }
}

/**
 * How a check stands, in the states `NODE_STATE_WORDS.check` names. Whether it passed is the
 * server's own answer over every result of the check (`pass_stands`, main's e98fc56ef8): a pass
 * recorded before a later fail still stands, while a retracted pass, a pass recorded against an
 * older wording of the check and a pass at a commit the check no longer compares with stand for
 * nothing. The other states read what no pass standing is:
 *
 * - **`no-result`,** no result recorded at all, which the results say by themselves.
 * - **`unreadable`,** a payload carrying no `pass_stands` at all, which is a server that does
 *   not send the field - an older build behind the same load balancer as the one this page
 *   came from. It is read before everything the answer decides, because without it the page
 *   knows nothing of how the check stands; it is the page's own blindness and no verdict of
 *   the check's.
 * - **`retracted`,** every result of the check withdrawn (`CheckResultRetracted`, which is on
 *   the result and never on the check): nothing stands for it and nothing stands against it,
 *   so neither "Passed" nor "Failed" is true of it, and the withdrawal is what the reader is
 *   owed.
 * - **`older`,** a newest result that stands and no longer speaks for the work as it stands,
 *   whatever it found, because its commit is not one the check compared with or the check's
 *   own words have moved under it.
 * - **`failed`,** a newest result that stands, at the work the check compares with.
 * - **`unmarked`,** where nothing could be judged: a repository Dispatch cannot read, or a
 *   commit it cannot place.
 *
 * A retracted result is no evidence, as the server counts it (`results_comparison.go`'s
 * `passStandsOf` skips it), so `older` and `failed` are read from the newest result that still
 * stands and never from a withdrawn one: a pass taken back at the compared commit over an older
 * pass at another commit is `older`, and a pass taken back over a fail that stands is `failed`,
 * whatever commit the withdrawn pass ran at.
 *
 * It is how a check stands against the basis the check itself carries, and the page draws it as
 * `checkStanding` turns it over at the one commit the page names for the issue's results, so
 * one check reads one state wherever it is drawn, each told apart by a word and a shape (R15,
 * R21). `e2e/issue-layout-seed-support.ts` waits on seeded checks by this function, so a seed
 * waits for the states the page draws rather than for a copy of this rule.
 */
export function checkState(check: CheckStateReads): CheckState {
  const results = check.results ?? [];
  if (results.length === 0) return "no-result";
  if (check.pass_stands === undefined) return "unreadable";
  if (check.pass_stands.answer === "yes") return "passed";
  if (check.pass_stands.answer === "unknown") return "unmarked";
  // The results arrive newest first, so this is the newest one a retraction left standing.
  const standing = results.find((result) => result.retracted === undefined);
  if (standing === undefined) return "retracted";
  if (standing.check_words_moved === true) return "older";
  return standing.commit_stands?.answer === "other_commit" ? "older" : "failed";
}

/** What a check's state is read from: the server's answer over its results and the results
 *  themselves. `pass_stands` is declared required by the API types and is missing from what
 *  the deployed server sends, so it is optional here and the page reads a payload without it
 *  rather than throwing over the whole issue page. */
export type CheckStateReads = Pick<CheckBlock, "results"> & {
  readonly pass_stands?: CheckBlock["pass_stands"];
};

/**
 * The one commit the page names for the issue's results: the commit of the newest result
 * recorded against any check of the issue that still stands and names one. A result nobody
 * withdrew is the evidence the page reads, so a withdrawn result names no commit here, and a
 * result recorded with no commit at all names none either.
 *
 * It is the Verification filter's commit (R17, `verification-filter/statement-state.ts`, which
 * exports it), read here because the tree reads every check's state at it (`checkStanding`),
 * and this module cannot import that one, which imports this.
 */
export function namedCommit(checks: readonly CheckBlock[]): string | undefined {
  const named = checks.flatMap((check) =>
    standingResults(check).filter((result) => result.commit_sha !== "")
  );
  return newestFirst(named)[0]?.commit_sha;
}

/** How one check stands at the commit the page names for the issue's results. */
export interface CheckStanding {
  /** How it stands, in the states `NODE_STATE_WORDS.check` names. */
  readonly state: CheckState;
  /** The result that state stands on: the fail at the named commit; else, for a check that
   *  passed, the newest pass that still stands, since a pass recorded before a later fail is
   *  what the server's `pass_stands` answered over; and for every other check the newest result
   *  that stands at all. Absent where none stands. */
  readonly stands?: CheckResult;
}

/**
 * How a check stands at `commit`, the one commit the page names for the issue's results
 * (`namedCommit`): the page's one reading of a check's state (R10, R17). The tree's check node
 * holds it (`addChecks`), so the outline's row, the fold's frontier (`tree-fold.ts`) and the
 * Verification filter's failing count (`presets.ts`'s `filterSignals`) read it, and so does
 * the Verification filter's body (`statement-state.ts`'s `checkReading`): one check never reads
 * Passed on its row and Failed in the filter.
 *
 * It is `checkState`'s answer with two turns, because R17 reads every result of the issue
 * against the one commit the page names and `checkState` compares a result with the basis its
 * own check carries:
 *
 * - **A fail at the named commit is a fail.** Where the newest result that stands and ran at
 *   that commit failed, the check reads Failed there, whatever older pass the server still
 *   counts as standing (`pass_stands` says yes for any pass at a basis, later fails or not), so
 *   its statement is Not met and not Unknown behind an "Out of date". A fail recorded against
 *   words the check no longer has turns nothing over: it stands for nothing, as `checkState`
 *   and the server's `standingCommit` (`results_comparison.go`) read it, since it is evidence
 *   about a check that no longer reads that way.
 * - **A pass or a fail at another commit is out of date.** Otherwise a pass or a fail whose
 *   result was recorded against another commit reads "Out of date", because that result no
 *   longer speaks for the work the rest of the page is read against, and R17 says such a result
 *   makes its statement Unknown. A result that names no commit at all turns nothing over: the
 *   page has no ground to call it older, and it is no evidence at the named commit either.
 *
 * Where no commit is named - no result that stands names one - it is `checkState`'s answer.
 */
export function checkStanding(check: CheckStateReads, commit: string | undefined): CheckStanding {
  const standing = newestFirst(standingResults(check));
  const here =
    commit === undefined ? undefined : standing.find((result) => result.commit_sha === commit);
  const failedHere = here?.verdict === "fail" && here.check_words_moved !== true;
  const state = failedHere ? "failed" : checkState(check);
  const stands = failedHere
    ? here
    : state === "passed"
      ? standing.find(({ verdict }) => verdict === "pass")
      : standing[0];
  const elsewhere =
    commit !== undefined &&
    stands !== undefined &&
    stands.commit_sha !== "" &&
    stands.commit_sha !== commit;
  return {
    state: elsewhere && (state === "passed" || state === "failed") ? "older" : state,
    ...(stands === undefined ? {} : { stands }),
  };
}

/** Every result of `check` nobody withdrew, which is every result that stands as evidence. */
function standingResults(check: CheckStateReads): readonly CheckResult[] {
  return (check.results ?? []).filter((result) => result.retracted === undefined);
}

/** `results` newest first, by the time each was recorded. The server already answers newest
 *  first; this reads the times themselves, so the order is the model's own and no caller
 *  depends on the order a read happened to arrive in. */
export function newestFirst(results: readonly CheckResult[]): readonly CheckResult[] {
  return [...results].sort(newerFirst);
}

/** Orders two results newest first by the time each was recorded, to the microsecond the server
 *  writes (`compareTimestamps`), which `Date.parse` would cut to the millisecond and so tie. A
 *  time that cannot be read (`isTimestamp`) is the oldest possible, so a result with no readable
 *  time never outranks one that has one, two such tie, and no time throws. */
function newerFirst(first: CheckResult, second: CheckResult): number {
  const firstRead = isTimestamp(first.created_at);
  const secondRead = isTimestamp(second.created_at);
  if (firstRead && secondRead) return compareTimestamps(second.created_at, first.created_at);
  return Number(secondRead) - Number(firstRead);
}

/**
 * A check and a plan task sit under each requirement their `serves` names, edited under the
 * first and transcluded under the rest (R4); what their `needs` names is a chip, one per target
 * (`needsChips`), as every edge that is not the spine is. A `serves` naming no requirement of
 * this issue places nothing, so the block keeps its place under the issue.
 *
 * Two of the chain's gaps are read here, from the pointers this pass already resolves (R10):
 *
 * - **A citation whose target is no longer a requirement.** `serves` points only at a
 *   requirement (`packages/envoy/internal/dispatch/pmdoc/schema/blocks.json:81-84`), and the
 *   write that introduces one is refused where it does not (`pmdoc.CheckBlockPointers`); a
 *   target retyped after that write leaves the citation naming a node of another kind, which
 *   the drafts hold the type of. A reference this issue does not address, or one no draft
 *   answers, is no such citation: the page cannot say what it is not holding, and a target
 *   nothing answers to at all is already drawn as a chip that says so.
 * - **A plan task nobody asked for.** A task naming no requirement it serves and no
 *   declaration it attempts is work no reason stands behind. `attempts` is the declarations
 *   route of dispatch://INFRA-1278, read from the same `block[]` attributes as every other
 *   pointer, so the day it lands this pass reads it and nothing else changes. A check is held
 *   to neither: a check is there to prove a requirement, and it attempts no declaration at
 *   all.
 */
function servePlacements(drafts: Map<string, Draft>, context: TreeContext): void {
  for (const draft of drafts.values()) {
    if (draft.type !== "check" && draft.type !== "plan_task") continue;
    const named = (draft.pointers.serves ?? []).map(context.nodeIdOf);
    const serves = named.filter(
      (id): id is string => id !== undefined && drafts.get(id)?.type === "requirement"
    );
    if (serves.length > 0) draft.parents = serves;
    const elsewhere = named.some((id) => {
      const target = id === undefined ? undefined : drafts.get(id);
      return target !== undefined && target.type !== "requirement";
    });
    if (elsewhere) draft.gaps = [...draft.gaps, "serves_not_requirement"];
    if (
      draft.type === "plan_task" &&
      (draft.pointers.serves ?? []).length === 0 &&
      (draft.pointers.attempts ?? []).length === 0
    ) {
      draft.gaps = [...draft.gaps, "nobody_asked"];
    }
    draft.chips = [...draft.chips, ...needsChips(draft.pointers.needs ?? [], context)];
  }
}

/**
 * The chips of what a block `needs`, one per target (dispatch://INFRA-1196 decision 143), in the
 * order its pointers first name each. The server accepts one task needed by a pinned and an
 * unpinned pointer at once (`#b-a1@v1 #b-a1`), and both need the one task, so the pointers are
 * told apart by what they name with any version pin dropped (`pinDropped`). A target named both
 * ways is drawn by its unpinned pointer's chip, which names the block's current words; one only
 * pinned pointers name, by the first one's.
 */
function needsChips(needs: readonly string[], context: TreeContext): NodeChip[] {
  const needed = new Map<string, { readonly pinned: boolean; readonly reference: string }>();
  for (const reference of needs) {
    const { pinned, target } = pinDropped(reference);
    const held = needed.get(target);
    if (held === undefined || (held.pinned && !pinned)) needed.set(target, { pinned, reference });
  }
  return [...needed.values()].map(({ reference }) => ({
    ...context.chipOf(reference),
    label: blockAttributeLabel("needs"),
  }));
}

/**
 * What `reference` names with any version pin dropped, a block's (`#b-a1@v1`) and a document's
 * (`artifact/plan-md@v1`), as the reference its route writes with none (`target`), and whether
 * it pinned one (`pinned`). A reference that does not parse is its own target, pinning nothing.
 */
function pinDropped(reference: string): { readonly pinned: boolean; readonly target: string } {
  const route = parseNodeReference(reference);
  if (route === undefined) return { pinned: false, target: reference };
  const block = "block" in route ? route.block : undefined;
  const version = "version" in route ? route.version : undefined;
  return {
    pinned: block?.pin !== undefined || version !== undefined,
    target: buildDispatchReference({
      ...route,
      ...(block === undefined ? {} : { block: { id: block.id } }),
      ...(version === undefined ? {} : { version: undefined }),
    }),
  };
}
