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
import {
  decisionChangeComments,
  decisionChanges,
  decisionNodes,
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
} from "./issue-tree";
import {
  type AskState,
  type CheckState,
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
 * The outline's rules for the drafts `buildIssueTree` places (root R4), which the tree read
 * keeps: how this issue addresses a node and names what a reference points
 * at (`treeContext`), a node's words read to plain words, how a check stands (`checkStanding`),
 * and the drafts of today's decisions read (`addDecisions`), which no node of the tree read holds
 * while decisions are sections of `decisions.md`.
 *
 * Every other node is drafted from the tree read (`tree-read/tree-nodes.ts`'s
 * `treeDraftsFrom`), which places and orders each one. Every function here is pure and takes the
 * reads as its input, and no request is made per node. Where a read sends the server's reading
 * of a node's words beside them, the draft keeps it with the record the words are, which is what
 * the row draws (`TreeNode.tree`).
 *
 * The tree is the issue's conversation and what it revealed (dispatch://INFRA-1196 R1, R3): a
 * document, an artifact and a handoff are nodes of the issue and not of its conversation, so
 * nothing here drafts one.
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
  /** What the coverage read counts for a requirement it holds (`tree-read/tree-nodes.ts`);
   *  absent on every other draft, a `requirement` no coverage row holds among them, which is how
   *  the review list tells a statement from a block (`addDecisions`). */
  counts?: NodeCounts;
  /** What the node's body draws under its text (`check-facts.ts`). */
  facts: NodeFact[];
  gaps: GapKind[];
  readonly id: string;
  /** The child issue this node is, as the issue read carries it (`TreeNode.child`). */
  child?: IssueChild;
  /** The person who kept this decision as it reads now (`TreeNode.keptBy`). */
  keptBy?: Actor;
  /** Whether this ask takes more than one of its options (`TreeNode.multiple`). */
  multiple?: true;
  /** The choices an ask offers (`TreeNode.options`). */
  options?: NodeOption[];
  order: number;
  /** The nodes this one sits under, in the order the model placed them: the first is where it is
   *  edited, every other a transclusion. `[]` is "under the issue". */
  parents: string[];
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
  // The kind of each typed block of this issue the tree read answers, by its node id, which is
  // all a chip can say of a block the reference read sent no words for.
  const types: readonly string[] = NODE_TYPES;
  const kinds = new Map<string, NodeType>();
  for (const node of reads.tree?.nodes ?? []) {
    if (node.document !== undefined && node.issue === undefined && types.includes(node.supertag)) {
      kinds.set(blockNodeId(node.document.slug, node.id), node.supertag as NodeType);
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
 * The issue's spec: the document the issue read names by its id (`primarySpec`), the one whose
 * blocks the tree read answers as the spec's (`tree-read/tree-nodes.ts`' `blocksOfRole`), so a
 * reference to the spec resolves against the document whose blocks are drawn. The document's own
 * flag answers only where the issue names no spec (`""`), as `documentWord` reads it.
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

/** A draft with nothing on it but its id and its type, for a builder to fill in. */
export function blank(id: string, type: NodeType, order: number): Draft {
  return {
    chips: [],
    facts: [],
    gaps: [],
    id,
    order,
    parents: [],
    question: false,
    replaced: false,
    text: "",
    type,
    waiting: false,
  };
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

/**
 * Each decision of today's decisions read that no node of the tree read holds
 * (`decisionNodes`) - a section of the issue's `decisions.md`, until those move onto decision
 * nodes - drafted as the outline drafted it, beside the drafts of the tree read, with its
 * Changes under it.
 *
 * It stands under the question it answers (R4): the asks its blocks mention (`Decision.asks`),
 * the first of them where it is edited and every other one a transclusion, as a check stands
 * under each requirement it serves. An ask the tree draws no node for - another issue's, or an
 * approval request - places nothing and stays the decision's chip, so a decision that names no
 * question of this issue stands under the issue, as any node whose parent is absent does, and
 * never inside `decisions.md`, which is no node at all. Wherever it stands it comes before its
 * siblings, but for the issue's memo, the first callout of its spec, which comes first: the
 * outline drew these decisions above the statements and the conversation. It names every node it
 * links as a chip, since a link places nothing, and each whole document its words cite
 * (`addDocumentChips`).
 *
 * Its Change is a comment of the issue whose quote its words hold, where they are the first
 * decision's words to hold it, with no block of the decisions document read
 * (`decisionChangeComments`). The read hangs that comment under the issue, since no node holds
 * the decision, so it is drawn under the decision it
 * changes, which then waits on its agent. Whether a decision waits on the reader is the review
 * list's rule (`inReviewList`), read once every statement has its draft, as the header's count
 * reads it (`waiting.ts`).
 *
 * `asksOf` answers the node each ask of the issue is drawn by, by the ask's id (`askNodeIds`),
 * asked only where a decision is drawn here.
 */
export function addDecisions(
  drafts: Map<string, Draft>,
  reads: IssueTreeReads,
  context: TreeContext,
  asksOf: () => ReadonlyMap<string, string>
): void {
  const all = reads.decisions?.decisions ?? [];
  const held = decisionNodes(all, reads.tree);
  const rows = all.filter((decision) => !held.has(decision.ref));
  if (rows.length === 0) return;
  const asks = asksOf();
  const artifacts = reads.issue.artifacts;
  const placed = decisionChangeComments(
    reads.decisions,
    reads.comments,
    artifacts,
    undefined,
    reads.tree
  );
  const changes = decisionChanges(reads.decisions, reads.comments, artifacts, undefined, placed);
  // The words are a block of the decisions document, which is what their references resolve
  // against (`TreeNode.source`). The read names that document by its address, and a source
  // names a document by its id.
  const source = textSourceOf("artifact", decisionsDocumentId(artifacts, reads.decisions));
  const memo = [...drafts.values()]
    .filter(
      (draft) =>
        draft.type === "callout" &&
        draft.parents.length === 0 &&
        draft.id.startsWith(blockNodeId(context.specSlug, ""))
    )
    .sort((one, other) => one.order - other.order)[0];
  const first = memo?.order ?? -1;
  // A statement is a requirement the coverage read holds, as the header's count is handed the
  // coverage read's statements (`waitingOnReader`'s `isStatement`): a `requirement` no coverage
  // row holds is a block and nothing more, and a tie to it puts no decision on the list.
  const isStatement = (reference: string): boolean =>
    drafts.get(context.nodeIdOf(reference) ?? "")?.counts !== undefined;
  const made = new Map<string, Draft>();
  rows.forEach((decision, index) => {
    const id = context.nodeIdOf(decision.ref);
    if (id === undefined) return;
    // Each a fraction of one past the memo's order, so they stand together before every
    // sibling the read orders after it.
    const draft = blank(id, "decision", first + (index + 1) / (rows.length + 1));
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
    draft.reference = decision.ref;
    draft.replaced = decision.replaced;
    draft.source = source;
    draft.text = decision.text;
    draft.tree = decision.text_tree;
    // How the decision stands with the reader, drawn as this node's own state word and shape
    // the way every other state is (R21).
    const changed = changes.has(decision.ref);
    draft.state = decisionState(decision, changed);
    markKeptAndUnsure(draft, decision);
    // Nothing waits on a reader whose login has not resolved, as `waiting.ts` counts it.
    draft.waiting = context.viewer !== undefined && inReviewList(decision, isStatement, changed);
    made.set(id, draft);
  });
  addDocumentChips(made, reads, context);
  for (const [id, draft] of made) drafts.set(id, draft);
  // Each Change of these decisions stands under the decision it changes, where the read hangs it
  // under the issue: a comment the read places under another node is a reply, in its thread.
  for (const [comment, ref] of placed) {
    const draft = drafts.get(comment);
    const decision = context.nodeIdOf(ref);
    if (
      draft?.type === "comment" &&
      draft.parents.length === 0 &&
      decision !== undefined &&
      made.has(decision)
    ) {
      draft.parents = [decision];
    }
  }
}

/**
 * How a decision stands with the reader, in the states the page draws
 * (`NODE_STATE_WORDS.decision`, R2): replaced by a later one first, since nothing waits on it
 * any longer; then a change the reader asked for, which waits on its agent and not on the
 * reader (`decision-review.ts`); then waiting for review; then kept, by the person whose name the
 * row draws after it (`TreeNode.keptBy`). A person's own decision is in none of them: it is
 * theirs, and the row draws no state. A decision of the decisions read and a decision node of
 * the tree read (`tree-read/tree-nodes.ts`' `draftDecision`) both stand by it, the node by the
 * same members, each optional there.
 */
export function decisionState(
  decision: Partial<Pick<Decision, "kept" | "replaced" | "waiting_review">>,
  changed: boolean
): DecisionState | undefined {
  if (decision.replaced) return "replaced";
  if (changed) return "change_requested";
  if (decision.waiting_review) return "waiting_review";
  return decision.kept == null ? undefined : "kept";
}

/** Who kept a decision as it reads now, whose name its row draws after its state word, unless it
 *  waits for review again; and whether its agent marked it unsure. One rule for a decision of the
 *  decisions read and a decision node, as `decisionState` is. */
export function markKeptAndUnsure(
  draft: Draft,
  decision: Partial<Pick<Decision, "kept" | "unsure" | "waiting_review">>
): void {
  if (decision.kept != null && decision.waiting_review !== true) {
    draft.keptBy = { id: decision.kept.kept_by, kind: "user" };
  }
  if (decision.unsure === true) draft.unsure = true;
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
 * holds it (`tree-read/tree-nodes.ts`' `draftServing`), so the outline's row, the fold's frontier
 * (`tree-fold.ts`) and the Verification filter's failing count (`presets.ts`'s `filterSignals`)
 * read it, and so does the Verification filter's body (`statement-state.ts`'s `checkReading`):
 * one check never reads Passed on its row and Failed in the filter.
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
