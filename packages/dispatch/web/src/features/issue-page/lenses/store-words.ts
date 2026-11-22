import type { DisplayTree } from "../../../api/types";
import { referenceSpans } from "../../refs/reference-spans";
import { parseIssuePath } from "../../refs/routes";
import { storeAddressReference } from "../../refs/store-address";
import { wordsOf } from "../../refs/tree-fragment";
import { outlineRowPath } from "../issue-address";
import { chipWords } from "../node-row-chip";
import { NODE_TYPE_NAMES } from "../page-words";
import { storedReading } from "../stored-reading";
import { addressesAsShortForms, type Draft, plainWords, storeKindWord } from "../tree-drafts";
import type { TreeDraftContext } from "../tree-read/tree-nodes";
import type { TreeAnswer, TreeAnswerNode } from "../tree-read/tree-read";
import {
  addressOf,
  constraintWords,
  kindOf,
  type LensType,
  lensChipWords,
  lensTypeOf,
  STORED_WORDS,
  storeAddressOf,
  storedFacts,
  storedRowWords,
} from "./lens-drafts";

/**
 * A store address in a row's words on the issue page (decision 388): `dispatch://lens/<uuid>`,
 * `dispatch://risk/<uuid>`, `dispatch://constraint/<uuid>`, and a finding's
 * `dispatch://lens/<uuid>#b-<id>`. No route names one (`refs/store-address.ts`), and a uuid is
 * no word a reader reads, so every one-line words write one as its kind's word
 * (`tree-drafts.ts`'s `addressesAsShortForms`). Where the tree read holds the node and a step
 * drafted its row, a row's words read it by the node's name instead, linked to that row
 * (`?node=`). `storeWords` is the mark of `tree-read/tree-nodes.ts`'s `treeDraftMarks` that
 * writes them so, and `storeLinkPath` the check a row makes before it follows such a link.
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

/**
 * The mark of `tree-read/tree-nodes.ts`'s `treeDraftMarks` that names each store address in
 * `draft`'s words. It runs once every node has been offered to the steps, so it knows every row
 * and its line.
 *
 * - A draft with a reading, the server's or an ask's question's: that reading, named
 *   (`storeAddressesIn`).
 * - A draft with none whose text holds a store address: one paragraph of its text's plain words,
 *   named (`namedLine`), which its row then draws its words from, the links with them. Its other
 *   addresses stay as written, which the row's link pass links as it links a text drawn as
 *   stored (`RowText.tsx`'s `StoredText`), and its line writes short as every line does.
 * - A lens's, a risk's and a constraint's own draft, whose words `lensDrafts` read from their
 *   attributes (`STORED_WORDS`): each attribute that holds a store address read again with its
 *   names (`nameStored`).
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
    case "constraint":
    case "lens":
    case "risk":
      nameStored(draft, node, draft.type, index);
      return;
    default: {
      if (draft.tree !== undefined) {
        draft.tree = storeAddressesIn(draft.tree, index);
        return;
      }
      const named = namedLine(draft.text, index, (words) => words);
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
      ? plainWords(storedRowWords(node, type) ?? "")
      : type === "constraint"
        ? constraintWords(node)
        : (context.lineOf(node.id) ?? "");
  return chipWords(words === "" ? NODE_TYPE_NAMES[type] : words);
}

/** Whether the stored string `raw` can hold a store address: only a `dispatch://` span is one. */
function cites(raw: string | undefined): raw is string {
  return raw?.includes("dispatch://") === true;
}

/**
 * The reading of the stored string `raw` with each store address in it named, as one paragraph
 * of the very words `plainWords` reads from it: the text runs of its reading (`storedReading`),
 * in order and spaced as `wordsOf` joins them, a run in code - marked as inline code, or inside
 * a code block - as written, and every other run with each store address of a lens, a risk, a
 * constraint or a finding in it named (`namedRuns`: a linked run of the node's name, or its
 * kind's word) and each other address written by `others`, white space collapsed. Every run is
 * plain text, and only a name's carries a mark, its link. A row draws a verdict, a heading, a
 * fact line and a constraint's words that cite nothing as `plainWords` reads them, each other
 * address written short (`addressesAsShortForms`), so the same words that cite a node are drawn
 * as the same one line, the cited words alone changed, with none of the lists and emphasis a
 * parse made in the page would draw (R20); a text drawn with no reading keeps its other
 * addresses as written, for the row's link pass. Undefined where it names none outside code, so
 * a draft keeps the words it has.
 */
function namedLine(
  raw: string | undefined,
  index: () => StoreIndex,
  others: (words: string) => string
): DisplayTree | undefined {
  if (!cites(raw)) return undefined;
  // The plain words since the last name, each name's run, and whether the next run stands apart
  // from the words before it, as `wordsOf` spaces them.
  const parts: (DisplayTree | string)[] = [];
  let words = "";
  let apart = false;
  let named = false;
  const read = (node: DisplayTree, inCode: boolean): void => {
    if (typeof node.text === "string") {
      if (apart && (words !== "" || parts.length > 0)) words += " ";
      apart = false;
      const marks = Array.isArray(node.marks) ? (node.marks as DisplayTree[]) : NO_MARKS;
      const code = inCode || marks.some((mark) => mark.type === "inlineCode");
      const runs = code ? undefined : namedRuns({ type: "text" }, node.text, index);
      if (runs === undefined) {
        words += code ? node.text : others(node.text);
        return;
      }
      named = true;
      for (const run of runs) {
        if (run.marks === undefined) {
          words += others(typeof run.text === "string" ? run.text : "");
          continue;
        }
        if (words !== "") parts.push(words);
        words = "";
        parts.push(run);
      }
      return;
    }
    apart = true;
    if (!Array.isArray(node.content)) return;
    const code = inCode || node.type === "code_block";
    for (const child of node.content as DisplayTree[]) read(child, code);
    apart = true;
  };
  read(storedReading(raw), false);
  if (!named) return undefined;
  if (words !== "") parts.push(words);
  const content = parts.flatMap((part, at): DisplayTree[] => {
    if (typeof part !== "string") return [part];
    let text = part.replace(/\s+/gu, " ");
    if (at === 0) text = text.trimStart();
    if (at === parts.length - 1) text = text.trimEnd();
    return text === "" ? [] : [{ text, type: "text" }];
  });
  return { content: [{ content, type: "paragraph" }], type: "doc" };
}

/** `draft`'s words and their reading from `named`, a lens's verdict or a risk's heading read
 *  with its names (`namedLine`), where there is one. */
function nameLine(draft: Draft, named: DisplayTree | undefined): void {
  if (named === undefined) return;
  draft.text = wordsOf(named);
  draft.tree = named;
}

/**
 * `draft`'s words that `lensDrafts` read from the attributes of `node`, of kind `type`, read
 * again with each store address in them named (`namedLine`), each only where it names one: a
 * lens's verdict and a risk's heading with their named reading (`nameLine`), so the row links
 * the names; a constraint's words (`constraintWords`) and each fact line (`STORED_WORDS`) as that
 * reading's words, by the rule `lensDrafts` reads them by (`storedFacts`). Those draw as text
 * (`LensLines.tsx`'s `Words`), so they gain the name and no link.
 */
function nameStored(
  draft: Draft,
  node: TreeAnswerNode,
  type: "constraint" | "lens" | "risk",
  index: () => StoreIndex
): void {
  const words = (raw: string): string => {
    const named = namedLine(raw, index, addressesAsShortForms);
    return named === undefined ? plainWords(raw) : wordsOf(named);
  };
  if (type === "constraint") draft.text = constraintWords(node, words);
  else nameLine(draft, namedLine(storedRowWords(node, type), index, addressesAsShortForms));
  const facts = draft.lens;
  const cited = STORED_WORDS.some(
    (stored) =>
      stored.type === type &&
      stored.fact !== undefined &&
      cites(node.attributes?.[stored.attribute])
  );
  if (facts !== undefined && cited) draft.lens = { ...facts, ...storedFacts(node, type, words) };
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
 * `DrawnText`) then makes the path the link's address (`storeLinkPath`). An address is found as
 * `addressesAsShortForms` finds one (`referenceSpans`), and what `trimReference` cuts off its
 * end stays where it stood. A run that already links to a store address is written as
 * `linkedRuns` writes it, and one that links anywhere else stays as its author wrote it, since a
 * link holds no second link. A run in code - marked as inline code, or inside a code block -
 * stays as its author wrote it, as `withShortAddresses` keeps it; `inCode` says `node` stands
 * inside a code block, so none of its runs is named.
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
 *  named: its own words, a kind's word written into them, and a linked run for each name; a run
 *  that links to a store address as `linkedRuns` writes it. Undefined where it names none: it
 *  holds no store address, stands in inline code, or links anywhere else. The index (`index`) is
 *  asked for only here, at a store address of a lens, a risk, a constraint or a finding. */
function namedRuns(
  run: DisplayTree,
  text: string,
  index: () => StoreIndex
): DisplayTree[] | undefined {
  const marks = Array.isArray(run.marks) ? (run.marks as DisplayTree[]) : NO_MARKS;
  if (marks.some((mark) => mark.type === "inlineCode")) return undefined;
  const link = marks.find((mark) => mark.type === "link");
  if (link !== undefined) return linkedRuns(run, text, link, index);
  if (!cites(text)) return undefined;
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

/**
 * The runs the text run `run`, of words `text`, is written as where its link mark `link` links to
 * a store address of a lens, a risk, a constraint or a finding: an autolink, whose words are that
 * address, is named as a bare address is (`namedRuns`); the author's own words are kept, linked as
 * a name is - the address as the link's `href`, the path of the node's row as its `title` - where
 * `index` holds the node, and with no link otherwise, which would go nowhere. Undefined for a link
 * to anything else, which stays as its author wrote it.
 */
function linkedRuns(
  run: DisplayTree,
  text: string,
  link: DisplayTree,
  index: () => StoreIndex
): DisplayTree[] | undefined {
  const href = (link.attrs as { readonly href?: unknown } | undefined)?.href;
  const address = typeof href === "string" ? storeAddressReference(href) : undefined;
  if (address === undefined || storeKindWord(address) === undefined) return undefined;
  // The run as it stands with no link: its other marks, or none.
  const { marks: _, ...bare } = run;
  const marks = (run.marks as DisplayTree[]).filter((mark) => mark !== link);
  const unlinked = marks.length === 0 ? bare : { ...bare, marks };
  if (storeAddressReference(text) === address) return namedRuns(unlinked, text, index);
  const named = index().get(address);
  if (named === undefined) return [unlinked];
  return [
    {
      ...unlinked,
      marks: [...marks, { attrs: { href: address, title: named.path }, type: "link" }],
    },
  ];
}

/** Where the page stands, as the router holds it (`useLocation`). */
interface PageAddress {
  readonly pathname: string;
  readonly search: string;
}

/**
 * The path a link in a row's words to the store address `href` opens (`RowText.tsx`'s link
 * pass): its link mark's `title`, `title`, only where that is the very path a name's link holds
 * (`namedRuns`) - the outline of the issue the page's address `page` names, read as the page's
 * slots read it (`parseIssuePath`), at the `?node=` of the node `href` names (`outlineRowPath`).
 * Undefined for any other title, so an author's own title on a link to a store address opens
 * nothing, and for any other address.
 */
export function storeLinkPath(href: string, title: string, page: PageAddress): string | undefined {
  const address = storeAddressReference(href);
  const issueKey = parseIssuePath(page.pathname, page.search)?.key;
  const query = title.indexOf("?");
  if (address === undefined || issueKey === undefined || query === -1) return undefined;
  const readId = new URLSearchParams(title.slice(query + 1)).get("node");
  return readId !== null && namesNode(address, readId) && title === outlineRowPath(issueKey, readId)
    ? title
    : undefined;
}

/**
 * Whether `readId`, a node's id in the tree read, is that of the node the store address `address`
 * names, as `addressOf` builds an address from a read id (`storeAddressOf`): with a leading
 * `<kind>/` taken off, the address's id (`dispatch://<kind>/<id>`) for a lens, a risk and a
 * constraint. For a finding (`dispatch://lens/<id>#b-<block>`), the block id is the part after the
 * read id's last `#`, or the whole read id where it holds none, and where it holds one the part
 * before it is the lens's id; a read id of the block id alone names no lens, as `addressOf`
 * takes the lens from the finding's parent.
 */
function namesNode(address: string, readId: string): boolean {
  if (address.includes("#")) {
    const hash = readId.lastIndexOf("#");
    const lens =
      hash === -1
        ? address.slice("dispatch://lens/".length, address.indexOf("#"))
        : readId.slice(0, hash);
    return storeAddressReference(storeAddressOf("finding", readId, lens)) === address;
  }
  const kind = address.slice("dispatch://".length, address.lastIndexOf("/"));
  if (kind !== "constraint" && kind !== "lens" && kind !== "risk") return false;
  return storeAddressReference(storeAddressOf(kind, readId)) === address;
}
