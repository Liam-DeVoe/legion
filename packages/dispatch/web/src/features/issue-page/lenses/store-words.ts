import type { DisplayTree } from "../../../api/types";
import { referenceSpans } from "../../refs/reference-spans";
import { storeAddressReference } from "../../refs/store-address";
import { wordsOf } from "../../refs/tree-fragment";
import { presetPath } from "../issue-address";
import { chipWords } from "../node-row-chip";
import { NODE_TYPE_NAMES } from "../page-words";
import { storedReading } from "../stored-reading";
import {
  addressesAsShortForms,
  type Draft,
  plainWords,
  storeKindWord,
  withShortAddresses,
} from "../tree-drafts";
import type { TreeDraftContext } from "../tree-read/tree-nodes";
import type { TreeAnswer, TreeAnswerNode } from "../tree-read/tree-read";
import {
  addressOf,
  constraintWords,
  kindOf,
  type LensType,
  lensChipWords,
  lensTypeOf,
} from "./lens-drafts";

/**
 * A store address in a row's words on the issue page (decision 388): `dispatch://lens/<uuid>`,
 * `dispatch://risk/<uuid>`, `dispatch://constraint/<uuid>`, and a finding's
 * `dispatch://lens/<uuid>#b-<id>`. No route names one (`refs/store-address.ts`), and a uuid is
 * no word a reader reads, so every one-line words write one as its kind's word
 * (`tree-drafts.ts`'s `addressesAsShortForms`). Where the tree read holds the node and a step
 * drafted its row, a row's words read it by the node's name instead, linked to that row
 * (`?node=`). `storeWords` is the mark of `tree-read/tree-nodes.ts`'s `treeDraftMarks` that
 * writes them so.
 *
 * Every fact comes from the tree read; nothing here sends a request. This module imports only
 * types from `tree-read/`, which imports it, so no import cycle closes (`biome.json`'s
 * `noImportCycles`).
 */

/** A node a store address in a row's words names. */
interface StoreNode {
  /** The words a row reads it by (`nameOf`). */
  readonly name: string;
  /** The address of its row (`outlineRowPath`), as a lens chip links it: the `title` of the link
   *  mark a row's words name it in (`namedRuns`). */
  readonly path: string;
}

/** Every node a row's words can name by its store address, by that address as
 *  `storeAddressReference` writes it. */
type StoreIndex = ReadonlyMap<string, StoreNode>;

/** The marks of a run that stands in no mark. */
const NO_MARKS: readonly DisplayTree[] = [];

/** The fact lines of a risk's and a constraint's own draft whose words are one stored attribute's
 *  (`lensDrafts`), each with the name of that attribute. */
const FACT_ATTRIBUTES = [
  ["earlyWarning", "early_warning"],
  ["prevention", "prevention"],
  ["reference", "reference"],
  ["takenWhere", "taken_where"],
] as const;

/** One fact line of `FACT_ATTRIBUTES`. */
type StoredFact = (typeof FACT_ATTRIBUTES)[number][0];

/**
 * The mark of `tree-read/tree-nodes.ts`'s `treeDraftMarks` that names each store address in
 * `draft`'s words (`storeAddressesIn`). It runs once every node has been offered to the steps,
 * so it knows every row and its line.
 *
 * - A draft with a reading, the server's or an ask's question's: that reading, named.
 * - A draft with none whose text holds a store address: its stored text's reading
 *   (`storedReading`), named, which its row then draws its words from, the links with them.
 * - A lens's, a risk's and a constraint's own draft, whose words `lensDrafts` read from their
 *   attributes: each attribute that holds a store address read again from its named reading, as
 *   `plainWords` reads it otherwise. A lens's verdict and a risk's heading keep that reading as
 *   the draft's, so the row links the names. A constraint's words and a fact line - an early
 *   warning, a prevention, a reference, where a measurement was taken - draw as text
 *   (`LensLines.tsx`'s `Words`), so they gain the name and no link.
 *
 * A child issue's title is no markdown and its row draws it as written, so it is left as it is;
 * its line reads a store address as its kind's word, as every line does.
 */
export function storeWords(
  draft: Draft,
  node: TreeAnswerNode,
  answer: TreeAnswer,
  context: TreeDraftContext
): void {
  const index = () => storeIndex(answer, context);
  switch (draft.type) {
    case "child_issue":
      return;
    case "lens":
      nameLine(draft, namedReading(node.attributes?.verdict, index));
      return;
    case "risk":
      nameLine(draft, namedReading(node.attributes?.heading, index));
      nameFacts(draft, node, index);
      return;
    case "constraint":
      draft.text = constraintWords(node, (raw) => {
        const named = namedReading(raw, index);
        return named === undefined ? plainWords(raw) : readingWords(named);
      });
      nameFacts(draft, node, index);
      return;
    default: {
      if (draft.tree !== undefined) {
        draft.tree = storeAddressesIn(draft.tree, index);
        return;
      }
      const named = namedReading(draft.text, index);
      if (named !== undefined) draft.tree = named;
    }
  }
}

/** The index of each build, one per draft context, shared by every draft: it reads which nodes
 *  were drafted and their lines, which the page's other reads change while the answer stands, as
 *  `lens-drafts.ts`'s `lensPlaces` reads them. */
const storeIndexes = new WeakMap<TreeDraftContext, StoreIndex>();

/** Every node of the answer that `lensDrafts` drafts as a lens, a risk, a constraint or a finding
 *  and that has a row, by its store address (`addressOf`): its name (`nameOf`) and the path of its
 *  row. It is made at the first store address of such a node a run's words name (`namedRuns`), so
 *  a build whose words name none makes none; no draft's words are named before then, so a name is
 *  read from the words as drafted. */
function storeIndex(answer: TreeAnswer, context: TreeDraftContext): StoreIndex {
  const held = storeIndexes.get(context);
  if (held !== undefined) return held;
  const index = new Map<string, StoreNode>();
  for (const node of answer.nodes ?? []) {
    const type = lensTypeOf(node, context);
    if (type === undefined || !context.drafted(node.id)) continue;
    const address = storeAddressReference(addressOf(node, context) ?? "");
    if (address === undefined || index.has(address)) continue;
    index.set(address, {
      name: nameOf(node, type, context),
      path: outlineRowPath(context.key, node.id),
    });
  }
  storeIndexes.set(context, index);
  return index;
}

/** The address of the row of the node whose id in the tree read is `readId` on the page of the
 *  issue `issueKey`: the outline's, at its `?node=`, as a lens chip (`NodeLink.tsx`) and a store
 *  address in a row's words (`storeIndex`) link that row. */
export function outlineRowPath(issueKey: string, readId: string): string {
  return `${presetPath(issueKey, "conversation")}?node=${encodeURIComponent(readId)}`;
}

/** The name a row's words read `node` by: a lens by its kind's words as its chip names it
 *  (`lensChipWords`), a risk by its heading, a constraint by its words (`constraintWords`), a
 *  finding by its row's line (`TreeDraftContext.lineOf`), and the page's word for its kind where
 *  those hold none; each cut as a chip's words are (`chipWords`). Each is read as the one-line
 *  words read it, so an address in it reads as its kind's word or its short form, and no name
 *  holds another node's. */
function nameOf(node: TreeAnswerNode, type: LensType, context: TreeDraftContext): string {
  if (type === "lens") return lensChipWords(addressesAsShortForms(kindOf(node, context))).words;
  const words =
    type === "risk"
      ? plainWords(node.attributes?.heading ?? "")
      : type === "constraint"
        ? constraintWords(node)
        : (context.lineOf(node.id) ?? "");
  return chipWords(words === "" ? NODE_TYPE_NAMES[type] : words);
}

/** Whether the stored string `raw` can hold a store address: only a `dispatch://` span is one. */
function cites(raw: string | undefined): raw is string {
  return raw?.includes("dispatch://") === true;
}

/** The reading of the stored string `raw` with each store address in it named
 *  (`storeAddressesIn`), or undefined where it names none, so a draft that cites none keeps the
 *  words it has. */
function namedReading(raw: string | undefined, index: () => StoreIndex): DisplayTree | undefined {
  if (!cites(raw)) return undefined;
  const reading = storedReading(raw);
  const named = storeAddressesIn(reading, index);
  return named === reading ? undefined : named;
}

/** The words of `reading` on one line, as `plainWords` reads a stored string's reading: each
 *  other address written short, white space collapsed. Where `reading` names no store address,
 *  these are the very words `plainWords` reads. */
function readingWords(reading: DisplayTree): string {
  return wordsOf(withShortAddresses(reading)).replace(/\s+/gu, " ").trim();
}

/** `draft`'s words and their reading from `named`, a lens's verdict or a risk's heading read
 *  with its names (`namedReading`), where there is one. */
function nameLine(draft: Draft, named: DisplayTree | undefined): void {
  if (named === undefined) return;
  draft.text = readingWords(named);
  draft.tree = named;
}

/** `draft`'s fact lines (`FACT_ATTRIBUTES`) read again from their attributes with each store
 *  address named, each only where `lensDrafts` drew it and its attribute names one. */
function nameFacts(draft: Draft, node: TreeAnswerNode, index: () => StoreIndex): void {
  const facts = draft.lens;
  if (facts === undefined) return;
  let named: Partial<Record<StoredFact, string>> | undefined;
  for (const [fact, attribute] of FACT_ATTRIBUTES) {
    if (facts[fact] === undefined) continue;
    const reading = namedReading(node.attributes?.[attribute], index);
    if (reading === undefined) continue;
    named ??= {};
    named[fact] = readingWords(reading);
  }
  if (named !== undefined) draft.lens = { ...facts, ...named };
}

/**
 * `node`, a reading or a node inside one, with each store address in its text runs written as
 * the name of the node it names, in a run that carries a link mark beside the run's own marks,
 * where `index` holds the node, and as its kind's word (`storeKindWord`) with the run's own marks
 * otherwise. The link mark holds the store address as its `href`, as `storeAddressReference`
 * writes it, and the path of the node's row as its `title`
 * (`{ type: "link", attrs: { href, title } }`): the body's reference pass (`refs/RefLink.tsx`'s
 * `collectReferenceAnchors`) reads a link to a path of an issue's page as a reference to the
 * issue, which it draws by the issue's title at the issue's own path, while it leaves alone a
 * link to a store address, which no route names. The row's link pass (`RowText.tsx`'s
 * `DrawnText`) then makes the path the link's address. An address is found as
 * `addressesAsShortForms` finds one (`referenceSpans`), and what `trimReference` cuts off its
 * end stays where it stood. A run in code - marked as inline code, or inside a code block -
 * stays as its author wrote it, as `withShortAddresses` keeps it, and so does a run that already
 * links somewhere, since a link holds no second link; `inCode` says `node` stands inside a code
 * block, so none of its runs is named.
 *
 * `node` itself is never changed, since a reading is shared (`storedReading`). A run that
 * names an address is copied, and so is each node above it; a node that holds none is answered
 * as it is, so a reading with none is answered whole, the very object: a row's reading keeps its
 * identity across reads, which `RowText.tsx`'s `useSameValue` and the paint rely on.
 */
function storeAddressesIn(node: DisplayTree, index: () => StoreIndex, inCode = false): DisplayTree {
  const content = node.content;
  if (!Array.isArray(content)) return node;
  const code = inCode || node.type === "code_block";
  const children = content as DisplayTree[];
  // The children up to the first one named are the node's own; from there on they are copied,
  // each as the walk answered it, a run that names a node as the runs it was written as.
  let changed: DisplayTree[] | undefined;
  for (const [at, child] of children.entries()) {
    let written: readonly DisplayTree[] | undefined;
    if (typeof child.text === "string") {
      written = code ? undefined : namedRuns(child, child.text, index);
    } else {
      const walked = storeAddressesIn(child, index, code);
      written = walked === child ? undefined : [walked];
    }
    if (written === undefined) {
      changed?.push(child);
      continue;
    }
    changed ??= children.slice(0, at);
    changed.push(...written);
  }
  return changed === undefined ? node : { ...node, content: changed };
}

/** The runs the text run `run`, of words `text`, is written as with each store address in it
 *  named: its own words, a kind's word written into them, and a linked run for each name. Undefined
 *  where it names none: it holds no store address, or stands in inline code or in a link. The
 *  index (`index`) is asked for only here, at a store address of a lens, a risk, a constraint or a
 *  finding. */
function namedRuns(
  run: DisplayTree,
  text: string,
  index: () => StoreIndex
): DisplayTree[] | undefined {
  if (!cites(text)) return undefined;
  const marks = Array.isArray(run.marks) ? (run.marks as DisplayTree[]) : NO_MARKS;
  if (marks.some((mark) => mark.type === "inlineCode" || mark.type === "link")) return undefined;
  const runs: DisplayTree[] = [];
  // The run's own words since the last name, the kind's words written into them included.
  let words = "";
  let cursor = 0;
  for (const { start, value } of referenceSpans(text, "dispatch://")) {
    const address = storeAddressReference(value);
    if (address === undefined) continue;
    const word = storeKindWord(address);
    if (word === undefined) continue;
    words += text.slice(cursor, start);
    cursor = start + value.length;
    const named = index().get(address);
    if (named === undefined) {
      words += word;
      continue;
    }
    if (words !== "") runs.push({ ...run, text: words });
    runs.push({
      ...run,
      marks: [...marks, { attrs: { href: address, title: named.path }, type: "link" }],
      text: named.name,
    });
    words = "";
  }
  if (cursor === 0) return undefined;
  words += text.slice(cursor);
  if (words !== "") runs.push({ ...run, text: words });
  return runs;
}
