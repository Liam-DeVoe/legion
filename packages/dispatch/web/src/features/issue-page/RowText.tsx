import {
  createContext,
  Fragment,
  type ReactNode,
  type RefObject,
  useCallback,
  useContext,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";

import type { DisplayTree } from "../../api/types";
import { MarkdownBody } from "../refs/MarkdownBody";
import { TextAskCard } from "../refs/markdown-cards";
import { useMarkdownHeadline } from "../refs/markdown-engine";
import {
  buildReferencePath,
  type DispatchReferenceRoute,
  parseNodeReference,
} from "../refs/routes";
import type { TextSource } from "../refs/text-references";
import { chipWords } from "./node-row-chip";
import { authorText } from "./page-structure";
import { NODE_TYPE_NAMES, PRESET_NAMES } from "./page-words";
import { useKnownReferenceNames } from "./reference-names";
import {
  knownIssueKeys,
  linkRowReferences,
  ROW_LINK,
  rowReferences,
  rowReferenceWords,
} from "./row-references";

/**
 * A node's own text, as its row in the outline draws it (dispatch://INFRA-1196 R16, R20, R34),
 * in the size and the colour of the words it is drawn among.
 *
 * A text whose read sent the server's reading of it (`tree`) is drawn from that reading, with
 * the renderer every other surface draws a stored text with (`MarkdownBody`): a heading, bold
 * words, a list and a code span are drawn as what they are, never as the `#`, `**`, `-` and
 * backticks the author typed, and the browser parses nothing to draw them. A text whose read
 * sent none is drawn as stored, because the only other way to format it would be to parse it
 * here, which R20 refuses.
 *
 * Either way a reference in the words - a `dispatch://` reference, a dashboard address, the key
 * of an issue that exists - is a link to its target, named from the page's one batched read of
 * what its references point at (`row-references.ts`): an issue by its key and title, and a block
 * or a document as a chip in place, named as a tie's chip is, in plain words (`chipName`,
 * `PlainReferenceNamesProvider`, part G's R4). The row asks the server nothing for it (R21), so
 * the body's own link pass, which reads one target for each reference, is given nothing to link.
 *
 * The words are a reading of the text, as it was when it was drawn as stored: an `ask` block in a
 * document is a node of the tree of its own, where a reader answers it, so the text draws its
 * question and options rather than a second card to answer it in (`TextAskCard`). Whatever the
 * reading draws that takes the focus - a link, a reference, a picture's control - is out of the
 * tab order, as every other control inside a row is: the tree is one tab stop, and a key
 * reaches a row's links instead (R9, `useTreeKeymap.ts`'s `rowLinks`). A view that no tree's
 * keys reach passes `tabStops`, which keeps them in the tab order, the links a later pass makes
 * included.
 */
export function RowText({
  markdown,
  source,
  tabStops = false,
  tree,
}: {
  /** The text as stored: drawn as it is where `tree` is absent. */
  markdown: string;
  /** The record the text is (`MarkdownBody`'s `source`). */
  source?: TextSource;
  /** Whether the controls inside the text keep their place in the tab order: false in the
   *  outline, whose keys reach them, and true for a view that no tree's keys reach. */
  tabStops?: boolean;
  /** The server's reading of `markdown`, where its read sent one. */
  tree?: DisplayTree;
}): ReactNode {
  return tree === undefined ? (
    <StoredText markdown={markdown} tabStops={tabStops} />
  ) : (
    <DrawnText markdown={markdown} source={source} tabStops={tabStops} tree={tree} />
  );
}

/**
 * The page's word for the whole document `route` names, or undefined where it names no whole
 * document (part G's R4, ruling 5): today's model holds no title for a document and its name is
 * its file name, which no chip draws, so a document is named by the page's word for one, and the
 * issue's spec by the Spec's name.
 */
export function documentWord(route: DispatchReferenceRoute | undefined): string | undefined {
  if (route === undefined) return undefined;
  if (route.kind === "spec") return route.block === undefined ? PRESET_NAMES.spec : undefined;
  const document =
    route.kind === "artifact" || route.kind === "document" || route.kind === "agent-artifact";
  return document && !("block" in route && route.block !== undefined)
    ? NODE_TYPE_NAMES.document
    : undefined;
}

/** The words a chip draws for the target `route` names, whose name the batched read gave as
 *  `name`: a whole document by the page's word for it (`documentWord`), and anything else, a
 *  block among them, by its first words (`chipWords`). A typed block's name is its stored
 *  excerpt, which opens on the block's source line, so its source lines are left out first. */
export function chipName(route: DispatchReferenceRoute | undefined, name: string): string {
  return documentWord(route) ?? chipWords(withoutDirectiveLines(name));
}

/** The names the batched read brought, with every reference but an issue's named as a tie's chip
 *  is (`chipName`). A document `markdown` cites is named by the page's word for it whether or not
 *  the read has answered yet, so its file name and its slug are never drawn while it is in
 *  flight. */
function chipNames(
  names: ReadonlyMap<string, string>,
  markdown: string
): ReadonlyMap<string, string> {
  const named = new Map(names);
  for (const [reference, name] of names) {
    const route = parseNodeReference(reference);
    if (route !== undefined && route.kind !== "issue") named.set(reference, chipName(route, name));
  }
  for (const one of rowReferences(markdown, [])) {
    if (one.route.kind === "issue" || named.has(one.reference)) continue;
    const words = chipName(one.route, "");
    if (words !== "") named.set(one.reference, words);
  }
  return named;
}

/**
 * The batched read's names, with each name but an issue's read as plain words, as the chip at
 * the end of a node's words reads its target's name (`NodeRow.tsx`'s `Chip`,
 * `useMarkdownHeadline`): a block's name is its stored excerpt, which is markdown, so a chip in
 * a node's words draws no mark its author typed either (part G's R4, the tester's finding F18).
 * An issue's name is its title, kept as the read sent it. A name is left out until its reading
 * answers, so the chip in place draws what it draws before the names come, never the marks.
 * This reading goes once F's `chipOf` names each target from the server's reading of it
 * (decision 49).
 */
const PlainReferenceNames = createContext<ReadonlyMap<string, string> | undefined>(undefined);

/** Whether the batched read's name for `reference` is kept as it was sent: an issue's, and that
 *  of a reference the grammar cannot read. */
function keptAsSent(reference: string): boolean {
  const route = parseNodeReference(reference);
  return route === undefined || route.kind === "issue";
}

/** Reads every name the batched read brought as plain words, once for every row under it: the
 *  page's slots draw it around the views that draw rows (`slots/providers.tsx`). */
export function PlainReferenceNamesProvider({ children }: { children: ReactNode }): ReactNode {
  const read = useKnownReferenceNames();
  const [plain, setPlain] = useState<ReadonlyMap<string, string>>(() => new Map());
  const take = useCallback((name: string, words: string) => {
    setPlain((held) => (held.get(name) === words ? held : new Map(held).set(name, words)));
  }, []);
  const names = useMemo(() => {
    const named = new Map<string, string>();
    for (const [reference, name] of read) {
      const source = withoutDirectiveLines(name);
      const words = keptAsSent(reference) ? name : source === "" ? "" : plain.get(source);
      if (words !== undefined) named.set(reference, words);
    }
    return named;
  }, [plain, read]);
  // One reading for each name, whichever references share it.
  const sources = useMemo(() => {
    const held = new Set<string>();
    for (const [reference, name] of read) {
      const source = withoutDirectiveLines(name);
      if (!keptAsSent(reference) && source !== "") held.add(source);
    }
    return [...held];
  }, [read]);
  return (
    <PlainReferenceNames.Provider value={names}>
      {sources.map((source) => (
        <PlainName key={source} name={source} onRead={take} />
      ))}
      {children}
    </PlainReferenceNames.Provider>
  );
}

/** Reads one name as plain words, as the chip at the end of a node's words does, and hands them
 *  to `onRead` once the reading answers. It draws nothing. */
function PlainName({
  name,
  onRead,
}: {
  name: string;
  onRead: (name: string, words: string) => void;
}): null {
  const plain = useMarkdownHeadline(undefined, name.length, name);
  useLayoutEffect(() => {
    if (plain !== undefined) onRead(name, plain.text);
  }, [name, onRead, plain]);
  return null;
}

/** The names a node's words name their references by: read as plain words where the page's
 *  slots draw `PlainReferenceNamesProvider`, and as the batched read sent them where nothing
 *  does, as in a test that draws a row alone. */
function useRowReferenceNames(): ReadonlyMap<string, string> {
  const known = useKnownReferenceNames();
  return useContext(PlainReferenceNames) ?? known;
}

/** The mark a reference in a node's words carries where it is drawn as a chip (`styles.css`). */
const ROW_CHIP = "data-row-chip";

/** A typed block's source line in a node's words: its opening `:::requirement{#r1}`, or its
 *  closing `:::`. It is markup and no word of the node (part G's R4, the tester's F20): a
 *  decision's text holds the lines of a block its section quotes, and both its stored text and
 *  its reading carry them as they stand. */
const DIRECTIVE_LINE = /^[ \t]*:{3,}(?:[a-z_][^\n]*)?[ \t]*$\n?/gmu;

/** `text` without a typed block's source lines, and with no blank line left where one stood. */
function withoutDirectiveLines(text: string): string {
  return text.includes(":::") ? text.replace(DIRECTIVE_LINE, "") : text;
}

/** Takes a typed block's source lines out of the words a reading painted into `root`, as the
 *  row's link pass edits them (`linkRowReferences`): every text outside code, and a paragraph
 *  that held nothing else is drawn as none. Run again after every paint; it finds nothing to do
 *  twice. */
function dropDirectiveLines(root: HTMLElement): void {
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT);
  const texts: Text[] = [];
  for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
    if (
      node instanceof Text &&
      node.data.includes(":::") &&
      node.parentElement?.closest("code, pre") == null
    ) {
      texts.push(node);
    }
  }
  for (const text of texts) {
    const kept = withoutDirectiveLines(text.data);
    if (kept === text.data) continue;
    text.data = kept;
    const block = text.parentElement?.closest("p");
    if (block instanceof HTMLElement && (block.textContent ?? "").trim() === "")
      block.hidden = true;
  }
}

/** A text drawn as stored, each reference in it the link the batched read names, and no typed
 *  block's source line in it. */
function StoredText({
  markdown: stored,
  tabStops,
}: {
  markdown: string;
  tabStops: boolean;
}): ReactNode {
  const markdown = useMemo(() => withoutDirectiveLines(stored), [stored]);
  const read = useRowReferenceNames();
  const names = useMemo(() => chipNames(read, markdown), [markdown, read]);
  const found = useMemo(() => rowReferences(markdown, knownIssueKeys(names)), [markdown, names]);
  let at = 0;
  return (
    <p className="whitespace-pre-wrap break-words" {...authorText}>
      {found.map((one) => {
        const before = markdown.slice(at, one.start);
        at = one.end;
        const { key, words } = rowReferenceWords(one, names);
        return (
          <Fragment key={`${one.start}:${one.reference}`}>
            {before}
            <a
              className={ROW_LINK}
              data-row-chip={one.route.kind === "issue" ? undefined : ""}
              data-row-reference={one.reference}
              href={buildReferencePath(one.route)}
              tabIndex={tabStops ? undefined : -1}
            >
              {key === undefined ? (
                words
              ) : (
                <>
                  <span className="font-semibold whitespace-nowrap">{key}</span>
                  {words === "" ? "" : ` ${words}`}
                </>
              )}
            </a>
          </Fragment>
        );
      })}
      {markdown.slice(at)}
    </p>
  );
}

/** What takes the focus in a drawn text: every control a reading can hold. */
const FOCUSABLE = "a[href], area[href], button, input, select, textarea, iframe, [tabindex]";

/**
 * Keeps every control inside `box` out of the tab order while `out` holds: the ones it is drawn
 * with, and the ones that arrive after - a reference's link once its target answers, a picture
 * once it loads - as they arrive. The outline is one tab stop and its keys reach a row's controls
 * (R9); a view that no tree's keys reach passes `out` false and leaves them where they are.
 */
export function useOutOfTabOrder(box: RefObject<HTMLElement | null>, out: boolean): void {
  useLayoutEffect(() => {
    const element = box.current;
    if (!out || element === null) return;
    const untab = (): void => {
      for (const control of element.querySelectorAll<HTMLElement>(FOCUSABLE)) {
        if (control.tabIndex !== -1) control.tabIndex = -1;
      }
    };
    untab();
    const watching = new MutationObserver(untab);
    watching.observe(element, { childList: true, subtree: true });
    return () => watching.disconnect();
  }, [box, out]);
}

/** A text drawn from its reading, with every control it holds taken out of the tab order unless
 *  `tabStops` holds: the ones it is drawn with, and the ones the reading puts in after - a
 *  reference's link once its target answers, a picture once it loads - as they arrive. */
function DrawnText({
  markdown,
  source,
  tabStops,
  tree,
}: {
  markdown: string;
  source: TextSource | undefined;
  tabStops: boolean;
  tree: DisplayTree;
}): ReactNode {
  const drawn = useRef<HTMLDivElement>(null);
  const read = useRowReferenceNames();
  const names = useMemo(() => chipNames(read, markdown), [markdown, read]);
  // After every paint of the reading, and again when the batched read brings the names. The
  // row's pass takes its links out of the tab order as it makes them; a view with tab stops
  // puts them back.
  const link = useCallback(() => {
    const element = drawn.current;
    if (element === null) return;
    dropDirectiveLines(element);
    linkRowReferences(element, names);
    for (const anchor of element.querySelectorAll<HTMLAnchorElement>("a[data-row-reference]")) {
      const route = parseNodeReference(anchor.getAttribute("data-row-reference") ?? "");
      if (route !== undefined && route.kind !== "issue") {
        anchor.setAttribute(ROW_CHIP, "");
        // A whole document is named by the page's word for one, as `chipNames` names it, also
        // where only the reading cites it: a statement's stored words leave its references out.
        const word = documentWord(route);
        if (word !== undefined && anchor.textContent !== word) anchor.textContent = word;
      }
      if (tabStops) anchor.removeAttribute("tabindex");
    }
  }, [names, tabStops]);
  useLayoutEffect(link, [link]);
  useOutOfTabOrder(drawn, !tabStops);
  return (
    <div className="min-w-0 break-words" ref={drawn} {...authorText}>
      <TextAskCard.Provider value={undefined}>
        {/* The body's own link pass is held to the references of `referenceText`: none, so it
            reads no target, and the row's pass links what the words cite (`linkRowReferences`). */}
        <MarkdownBody
          markdown={markdown}
          onRendered={link}
          referenceText=""
          source={source}
          tree={tree}
        />
      </TextAskCard.Provider>
    </div>
  );
}
