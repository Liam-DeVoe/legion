import type { Fragment, Node as ProseMirrorNode } from "@milkdown/kit/prose/model";
import type * as ProseMirrorState from "@milkdown/kit/prose/state";
import type { EditorState, Plugin, Transaction } from "@milkdown/kit/prose/state";
import type { EditorView } from "@milkdown/kit/prose/view";
import type * as YProseMirror from "y-prosemirror";

import { BLOCK_ID_ATTRIBUTE } from "../doc/block-id";
import { refocusWhenSettled } from "../doc/refocus-when-settled";
import { scrollElementToTop } from "../doc/scroll-block";
import { importWhenOnline } from "../shell/DeploymentResilience";
import { bringOnScreen, FOOT_HEIGHT_PROPERTY, IDENTITY_HEIGHT_PROPERTY } from "./identity-block";

/**
 * Putting the caret in a node's own text, where that text lives, and holding the editor that
 * text is drawn in to the window while the row holds it.
 *
 * The edit key draws the node's document in the row (`document-stage.tsx`) and then calls these:
 * `caretInBlock` places the caret at the start of that node's own block, and `holdEditorAtBlock`
 * opens the editor's box at that block. The caret is a DOM selection inside the editor and
 * nothing else: the editor's own observer reads the selection back as the document's, so this
 * touches neither the room nor the editor. While the row holds the editor, `holdEditsToBlock`
 * keeps every edit the editor makes inside that block, and `confineKey` and `selectionIntoBlock`
 * keep the reader's caret there.
 *
 * The editor is the document's whole editor, its box opened at the node's block, and the keys
 * the editor answers for a whole document reached past that block: measured on a preview,
 * Ctrl+Home moved the caret before the spec's `## Summary`, and a `Y` typed then made it
 * `## YSummary`, scrolled out of the box above the row; Up after Left put the caret before a
 * heading above the statement. An edit of text the reader cannot see is the loss the caret
 * placed at the top of the document was, so the row's editor keeps its selection, and every
 * edit, inside the node's block. Free editing of the whole document stays on the document page.
 *
 * `focusBlock` on the editor handle is neither: it scrolls the block into the middle of the
 * screen - the page as well as the box - and pulses it, which is what a link into a document
 * wants, and it places no caret (`@legion/proof-editor`'s `lib.ts`). A row wants the caret,
 * because editing is what the row opened the document for, and the page around the row where
 * the reader left it.
 */

/** The mark of the box the editor of a row's document scrolls in (`StagedDocument.tsx`). */
export const EDITOR_BOX_ATTRIBUTE = "data-editor-box";

/**
 * Places the caret at the start of `blockId`'s block of the editor in `host`, and answers whether
 * it did. An editor that draws no such block gets neither the focus nor a caret: it is made
 * editable before it has read the document and may draw the document's blocks over more than
 * one frame after that, and a caret put anywhere else in it - its first text, where it has drawn
 * other blocks before the node's - sends the keys the reader presses next to the top of the
 * document, which is saved to the shared document. The focus stays where it was then: on the
 * row, as Enter left it, where no key edits the document. A block with no text a caret can
 * enter - an empty paragraph - takes the caret in the block itself, which the editor reads as a
 * place in that block: an editable given the focus with no caret put in it takes the next key
 * wherever the browser puts its own caret. The focus moves the page by nothing: the box the
 * editor scrolls in is `holdEditorAtBlock`'s to place. The editor takes the caret as its own in
 * the task that gives it the focus (`selectInEditor`), so no key reaches it before its selection
 * is the block's. Called again, it puts back a caret the editor dropped or moved as it redrew
 * itself (`useNodeEditing.ts`), in the node's block and nowhere else.
 */
export function caretInBlock(host: HTMLElement, blockId: string | undefined): boolean {
  const editable = host.querySelector<HTMLElement>('[contenteditable="true"]');
  const block = editable === null ? null : blockIn(editable, blockId);
  if (editable === null || block === null) return false;
  editable.focus({ preventScroll: true });
  const [start, offset] = edgesOf(block).start;
  selectInEditor(start, offset, start, offset);
  return true;
}

/** Whether the editor in `host` draws `blockId`'s block, which is when a caret can go in it
 *  (`caretInBlock`). */
export function drawsBlock(host: HTMLElement, blockId: string | undefined): boolean {
  const editable = host.querySelector<HTMLElement>('[contenteditable="true"]');
  return editable !== null && blockIn(editable, blockId) !== null;
}

/**
 * Puts the selection from `anchor` to `focus` in the editor holding the focus, and has the
 * editor take it as its own at once. The editor reads a selection put in it only once the
 * browser reports the change - its observer listens for the document's `selectionchange`
 * (`prosemirror-view`'s `DOMObserver`) - which the browser does a task later, and later still on
 * a busy page. Until then a key the editor handles itself (Delete, Backspace, Enter, through its
 * keymap) acts on the editor's own selection, and the editor writes that selection back over the
 * DOM's as it takes the focus (its focus handler, 20 ms after it) and as it redraws: measured in
 * Chromium on a busy page, the editor held the focus with its selection at the top of the
 * document for 0.27 to 0.84 s after Enter put the caret in a statement, and ten Deletes pressed
 * then removed the spec's first heading. So the report is made here, in the same task: the
 * editor reads the selection on it, and the browser's own report, when it comes, holds nothing
 * new. One move, rather than a selection emptied and then filled: where a change of the
 * selection is reported the moment it is made (happy-dom), the emptied one reads as a caret out
 * of the block (`caretOutOfBlock`), which places it again, and again.
 */
export function selectInEditor(
  anchor: Node,
  anchorOffset: number,
  focus: Node,
  focusOffset: number
): void {
  window.getSelection()?.setBaseAndExtent(anchor, anchorOffset, focus, focusOffset);
  document.dispatchEvent(new Event("selectionchange"));
}

/**
 * Whether the editor in `host` holds the focus with the caret out of `blockId`'s block, where
 * `caretInBlock` put it: the caret moves out with the editable as the editor makes its views
 * again (`useNodeEditing.ts`). False where the editor draws no such block, where `caretInBlock`
 * puts no caret either.
 */
export function caretOutOfBlock(host: HTMLElement, blockId: string | undefined): boolean {
  const editable = host.querySelector<HTMLElement>('[contenteditable="true"]');
  const block = editable === null ? null : blockIn(editable, blockId);
  if (editable === null || block === null || !editable.contains(document.activeElement)) {
    return false;
  }
  const anchor = window.getSelection()?.anchorNode;
  return anchor === null || anchor === undefined || !block.contains(anchor);
}

/** The keys a row's editor takes wherever its selection stands: they move the focus out of the
 *  editor or end the editing (`useNodeEditing.ts`), or are a modifier held for the next key, and
 *  none of them edits. */
const PASSING_KEYS: Readonly<Record<string, true>> = {
  Alt: true,
  AltGraph: true,
  CapsLock: true,
  Control: true,
  Escape: true,
  Meta: true,
  Shift: true,
  Tab: true,
};

/** The keys held with Ctrl or Meta that the editor answers with an edit, or the browser does, by
 *  `event.key` in lower case: select-all, the marks (bold, italic, code, a browser's underline),
 *  cut and paste, undo and redo, a Mac's Ctrl+D and Ctrl+H, a list item's sink and lift, the
 *  deletes of a word and Enter's way out of a code block (`prosemirror-commands`' base keymaps,
 *  `@milkdown/preset-commonmark`, `@milkdown/plugin-history`, `@milkdown/plugin-collab`). Every
 *  block shortcut holds Alt as well (`Mod-Alt-0` to `Mod-Alt-8`, `Mod-Alt-c`). */
const EDITING_CHORDS: Readonly<Record<string, true>> = {
  "[": true,
  "]": true,
  a: true,
  b: true,
  backspace: true,
  d: true,
  delete: true,
  e: true,
  enter: true,
  h: true,
  i: true,
  u: true,
  v: true,
  x: true,
  y: true,
  z: true,
};

/** The platforms ProseMirror reads as a Mac (`prosemirror-keymap`). */
const MAC_PLATFORM = /Mac|iP(hone|[oa]d)/;

/** A place in the page, as each end of a selection is one. */
type Point = readonly [node: Node, offset: number];

/**
 * Keeps a key pressed in the editor in `host` from taking its caret out of `blockId`'s block,
 * before the editor acts on it: called on the key in the capture phase, so before ProseMirror's
 * own listener on the editable, which acts on a key on its keydown - its keymap moves its own
 * selection, and a key it leaves to the browser moves the DOM's - and ignores a key whose default
 * is prevented (`prosemirror-view`'s `eventBelongsToView`). Every edit is held to the block by
 * the editor itself (`holdEditsToBlock`), which refuses a move of its selection out of the block
 * too; but by then the browser has moved the page's caret, and scrolled the box to it, and the
 * editor writes its own selection back over that caret a task later. So a key is refused
 * (`preventDefault`) where it would take the caret out of the block:
 *
 * - every key that could edit (`couldEdit`) while the selection is not inside the block, or the
 *   editor draws no such block: before the caret is placed, or a Tab into the editable before the
 *   block is drawn, no key edits the document, while the browser's own keys - find, print, the
 *   address bar, a function key - still reach it;
 * - Up on the block's first line and Down on its last, Left at its start and Right at its end,
 *   PageUp and PageDown, which leave the caret where it is.
 *
 * Ctrl+Home and Ctrl+End (Meta too, and Meta+Up and Meta+Down, a Mac's keys for them) put the
 * caret at the block's start and end, and Ctrl+A (Meta+A on a Mac) selects the block's text
 * alone, which the editor takes as its own at once (`selectInEditor`); with Shift held a move
 * extends the selection to the block's end it stops at. A Mac's Ctrl+A is the editor's own move
 * to the start of the line's paragraph (`prosemirror-commands`' `macBaseKeymap`), which stays in
 * the block, and is left to it.
 */
export function confineKey(
  host: HTMLElement,
  blockId: string | undefined,
  event: KeyboardEvent
): void {
  const editable = editableHolding(host, event.target);
  if (editable === null || event.defaultPrevented || PASSING_KEYS[event.key] === true) return;
  const block = blockIn(editable, blockId);
  const selected = block === null ? null : selectionIn(block);
  if (block === null || selected === null) {
    if (couldEdit(event)) event.preventDefault();
    return;
  }
  const taken = keyAtBlock(event, block, ...selected);
  if (taken === undefined) return;
  event.preventDefault();
  if (taken !== null) selectInEditor(taken[0][0], taken[0][1], taken[1][0], taken[1][1]);
}

/** Whether `event` is a key the editor or the browser could edit the document with, or move its
 *  caret with: any key but a function key, held with neither Ctrl nor Meta, and those held with
 *  either that the editor binds (`EDITING_CHORDS`, and every one with Alt held as well, which a
 *  Mac's Alt and Windows' AltGr type letters with too). */
function couldEdit(event: KeyboardEvent): boolean {
  if (/^F\d+$/.test(event.key)) return false;
  if (!event.ctrlKey && !event.metaKey) return true;
  return event.altKey || EDITING_CHORDS[event.key.toLowerCase()] === true;
}

/** What the hold on a row's editor is built of: ProseMirror's plugins, and the key y-prosemirror
 *  marks each transaction the collaboration brings with. Both are the editor library's own,
 *  which sits behind its own `import()` (`doc/editor.ts`), so they are imported here only once a
 *  row is edited, and kept from then on. */
export type HoldParts = readonly [state: typeof ProseMirrorState, sync: typeof YProseMirror];
let loadingHoldParts: Promise<HoldParts> | undefined;

/** Loads what `holdEditsToBlock` is built of, once. A row asks for it as it starts editing, so
 *  it has loaded by the time the page hands the row its editor, and the row gives the hold what
 *  this answers. A load that failed is the caller's to answer for, and the next call loads
 *  again. */
export function loadEditsHold(): Promise<HoldParts> {
  loadingHoldParts ??= importWhenOnline(() =>
    Promise.all([import("@milkdown/kit/prose/state"), import("y-prosemirror")])
  ).catch((error: unknown) => {
    loadingHoldParts = undefined;
    throw error;
  });
  return loadingHoldParts;
}

/** The attribute a block of the editor's document holds its id in (`@legion/proof-editor`'s
 *  `BLOCK_ID_ATTR`), which it draws as `BLOCK_ID_ATTRIBUTE`. */
const BLOCK_ID_PROPERTY = "blockId";

/**
 * Holds every edit the editor `view` makes of its own to the block `heldBlock` names, while the
 * row holds the editor: a ProseMirror plugin whose `filterTransaction` refuses a transaction of
 * the editor's own that changes anything outside that block - a join with the block beside it, a
 * split into two, a block made around it or beside it - or changes the block's own type or
 * attributes - a heading lowered a level, a paragraph made a heading, a quote, a list or a code
 * block - or puts a line break in a heading of it, or leaves the selection outside it. The
 * editor's keymap, its input rules, a paste, a drop and every change the browser makes to the page
 * that the editor reads back reach the document as such a transaction, so none of them needs a
 * guard of its own: a refused one changes nothing, and the editor draws the page again from its
 * document.
 *
 * A heading stores as one line of markdown, and measured in Chromium, two lines pasted at the
 * start of a decision's `## ` heading stored it as a heading of two lines, `Two\`, `lines...` and
 * `---` under them. A heading's `id` is no attribute the reader edits: Milkdown derives it from the
 * heading's words (`syncHeadingIdPlugin`), and writes it in a transaction of its own after every
 * change of them, to each heading whose name that change moves.
 *
 * Every transaction the collaboration brings applies, wherever it lands, each marked with
 * y-prosemirror's `ySyncPluginKey`: what y-prosemirror draws from the room - a collaborator's
 * edit, the span of a mark set there - and the editor's projection of the room's `marks` map
 * (`doc/editor.ts`'s `bindRemoteMarks`, through `proof-sdk-upstream`'s `applyRemoteMarks`, which
 * marks its transaction as y-prosemirror marks its own), which carries the marks' records and
 * takes the spans of the suggestions the room has accepted or rejected out of the text. The
 * block's place is mapped through each transaction, and found again by its id where one moved it
 * elsewhere. Mod-z is answered first by Milkdown's own history, as a local transaction this
 * filter checks like any other: two rows of one document share one editor, so an undo that would
 * take back an earlier row's edit, outside this row's block, is refused. A transaction that
 * changes neither the document nor the selection - a decoration drawn, a mark stored for the next
 * letter - leaves the document as it was, and applies.
 *
 * The plugin goes on once `parts` have loaded - what it is built of, which the row asks for as it
 * starts editing (`loadEditsHold`) - and `onHeld` is called as it goes on: the row puts no caret
 * in the editor before that (`useNodeEditing.ts`). Where they had loaded before the page handed
 * the editor over, that is in the task it was handed over in, before a key or a press can reach
 * the editor; the editor may have drawn the document by then. A load that failed puts no plugin
 * on, and is the row's to answer for, which asked for it. A change of the editor's plugins makes
 * ProseMirror make each plugin's view again (`useNodeEditing.ts`). The collaborators' cursors go
 * on later in the same way, keeping every plugin the editor holds (`@legion/proof-editor`'s
 * `installCollabCursorsWhenReady`). While `heldBlock` names no block, or the document holds no
 * block of its id, no edit of the editor's own goes in. Answers what takes the plugin off again,
 * or keeps it from going on.
 */
export function holdEditsToBlock(
  view: EditorView,
  heldBlock: () => string | undefined,
  parts: Promise<HoldParts>,
  onHeld: () => void
): () => void {
  let held: Plugin | undefined;
  let letGo = false;
  void parts.then(
    ([state, sync]) => {
      if (letGo || view.isDestroyed) return;
      const plugin: Plugin<number | undefined> = new state.Plugin<number | undefined>({
        filterTransaction: (transaction, editorState) =>
          transaction.getMeta(sync.ySyncPluginKey) !== undefined ||
          (!transaction.docChanged && !transaction.selectionSet) ||
          keepsToBlock(transaction, editorState, heldBlock(), plugin.getState(editorState)),
        // The block's start, as last found: the guess a transaction checks before it searches.
        state: {
          apply: (transaction, start, _, { doc }) =>
            !transaction.docChanged
              ? start
              : blockStart(
                  doc,
                  heldBlock(),
                  start === undefined ? undefined : transaction.mapping.map(start, 1)
                ),
          init: (_, { doc }) => blockStart(doc, heldBlock(), undefined),
        },
      });
      held = plugin;
      view.updateState(view.state.reconfigure({ plugins: [...view.state.plugins, plugin] }));
      onHeld();
    },
    // The row answers for a load that failed, which asked for it.
    () => undefined
  );
  return () => {
    const plugin = held;
    held = undefined;
    letGo = true;
    if (plugin === undefined || view.isDestroyed) return;
    view.updateState(
      view.state.reconfigure({ plugins: view.state.plugins.filter((each) => each !== plugin) })
    );
  };
}

/** Where `blockId`'s block starts in `doc`: at `guess` where that block still stands there, else
 *  wherever the document holds it, and undefined where it holds no such block. */
function blockStart(
  doc: ProseMirrorNode,
  blockId: string | undefined,
  guess: number | undefined
): number | undefined {
  if (blockId === undefined) return undefined;
  if (guess !== undefined && doc.nodeAt(guess)?.attrs[BLOCK_ID_PROPERTY] === blockId) return guess;
  let found: number | undefined;
  doc.descendants((node, position) => {
    if (found !== undefined) return false;
    if (node.attrs[BLOCK_ID_PROPERTY] !== blockId) return !node.isTextblock;
    found = position;
    return false;
  });
  return found;
}

/** Whether `transaction`, one of the editor's own in `state`, keeps to `blockId`'s block, found
 *  from `guess`: the block stands after it where the mapping puts it, one node of the type and
 *  attributes it had, with no more line breaks in its headings; what stands before it and after
 *  it reads as it did; and the selection is inside it. */
function keepsToBlock(
  transaction: Transaction,
  state: EditorState,
  blockId: string | undefined,
  guess: number | undefined
): boolean {
  const from = blockStart(state.doc, blockId, guess);
  const block = from === undefined ? null : state.doc.nodeAt(from);
  if (from === undefined || block === null) return false;
  const to = from + block.nodeSize;
  // Content put right before the block, or right after it, is outside it.
  const start = transaction.mapping.map(from, 1);
  const end = transaction.mapping.map(to, -1);
  const kept = transaction.doc.nodeAt(start);
  const { selection } = transaction;
  return (
    kept !== null &&
    start + kept.nodeSize === end &&
    kept.hasMarkup(block.type, attributesAsRead(block, kept), block.marks) &&
    headingBreaks(kept) <= headingBreaks(block) &&
    sameReading(state.doc.content.cut(0, from), transaction.doc.content.cut(0, start)) &&
    sameReading(state.doc.content.cut(to), transaction.doc.content.cut(end)) &&
    selection.from > start &&
    selection.to < end
  );
}

/** `node`'s attributes, but for a heading's `id`, which reads as `other`'s: Milkdown derives it
 *  from the heading's words (`holdEditsToBlock`). */
function attributesAsRead(node: ProseMirrorNode, other: ProseMirrorNode): ProseMirrorNode["attrs"] {
  return node.type.name === "heading" ? { ...node.attrs, id: other.attrs.id } : node.attrs;
}

/** Whether `before` and `after` hold the same nodes, by type, attributes, marks and text, a
 *  heading's `id` aside (`attributesAsRead`). */
function sameReading(before: Fragment, after: Fragment): boolean {
  if (before.eq(after)) return true;
  if (before.childCount !== after.childCount) return false;
  for (let index = 0; index < before.childCount; index += 1) {
    const was = before.child(index);
    const is = after.child(index);
    if (
      !is.hasMarkup(was.type, attributesAsRead(was, is), was.marks) ||
      (was.isText ? was.text !== is.text : !sameReading(was.content, is.content))
    ) {
      return false;
    }
  }
  return true;
}

/** The line breaks the headings of `node` hold: a hard break reads as one
 *  (`@milkdown/preset-commonmark`'s `leafText`), as a line of text does. */
function headingBreaks(node: ProseMirrorNode): number {
  let breaks = 0;
  const count = (each: ProseMirrorNode): boolean => {
    if (each.type.name !== "heading") return !each.isTextblock;
    breaks += each.textContent.split("\n").length - 1;
    return false;
  };
  if (count(node)) node.descendants(count);
  return breaks;
}

/**
 * Brings each end of the selection that stands out of `blockId`'s block, while the editor in
 * `host` holds the focus, back to the block's nearer end - its start for an end above it, its
 * end for one below - as the editor's own at once (`selectInEditor`). The backstop for a move
 * `confineKey` does not name: a press on another block of the editor, a drag across blocks, a
 * platform's own key for a move. A selection whose ends both stand in the block is left alone,
 * so the report of the move this makes, which calls this again, changes nothing.
 */
export function selectionIntoBlock(host: HTMLElement, blockId: string | undefined): void {
  const editable = host.querySelector<HTMLElement>('[contenteditable="true"]');
  const block = editable === null ? null : blockIn(editable, blockId);
  const selection = window.getSelection();
  const anchor = selection?.anchorNode ?? null;
  const focus = selection?.focusNode ?? null;
  if (
    editable === null ||
    block === null ||
    selection === null ||
    anchor === null ||
    focus === null ||
    !editable.contains(document.activeElement) ||
    (block.contains(anchor) && block.contains(focus))
  ) {
    return;
  }
  const { start, end } = edgesOf(block);
  const inside = (point: Point): Point => {
    if (block.contains(point[0])) return point;
    return compare(point, start) < 0 ? start : end;
  };
  const [anchorNode, anchorOffset] = inside([anchor, selection.anchorOffset]);
  const [focusNode, focusOffset] = inside([focus, selection.focusOffset]);
  selectInEditor(anchorNode, anchorOffset, focusNode, focusOffset);
}

/**
 * What `event` does with the selection from `anchor` to `focus` inside `block`: nothing of this
 * guard's (`undefined`), nothing at all (`null`), or the selection it makes instead, as its
 * anchor and its focus (`confineKey`).
 */
function keyAtBlock(
  event: KeyboardEvent,
  block: HTMLElement,
  anchor: Point,
  focus: Point
): readonly [Point, Point] | null | undefined {
  const { start, end } = edgesOf(block);
  const jump = event.ctrlKey || event.metaKey;
  const collapsed = compare(anchor, focus) === 0;
  const extendTo = (edge: Point): readonly [Point, Point] => [event.shiftKey ? anchor : edge, edge];
  // The end of the selection a vertical move starts from: the focus where Shift extends the
  // selection, and otherwise the end on the move's side, where the browser collapses it to.
  const earlier = compare(anchor, focus) <= 0 ? anchor : focus;
  const later = earlier === anchor ? focus : anchor;
  switch (event.key) {
    case "Home":
      return jump ? extendTo(start) : undefined;
    case "End":
      return jump ? extendTo(end) : undefined;
    case "a":
    case "A":
      return (event.metaKey || (event.ctrlKey && !MAC_PLATFORM.test(navigator.platform))) &&
        !event.altKey &&
        !event.shiftKey
        ? [start, end]
        : undefined;
    case "ArrowUp":
      if (event.metaKey) return extendTo(start);
      if (!sameLine(event.shiftKey ? focus : earlier, start)) return undefined;
      return event.shiftKey ? extendTo(start) : null;
    case "ArrowDown":
      if (event.metaKey) return extendTo(end);
      if (!sameLine(event.shiftKey ? focus : later, end)) return undefined;
      return event.shiftKey ? extendTo(end) : null;
    case "ArrowLeft":
      return (collapsed || event.shiftKey) && compare(focus, start) <= 0 ? null : undefined;
    case "ArrowRight":
      return (collapsed || event.shiftKey) && compare(focus, end) >= 0 ? null : undefined;
    case "PageUp":
    case "PageDown":
      return null;
    // Backspace, Delete, Enter and the editor's own block keys are held to the block by the
    // editor itself (`holdEditsToBlock`).
    default:
      return undefined;
  }
}

/**
 * Opens the editor in `host` at `blockId`'s block, in a box no taller than the window. The
 * editor holds the whole document, and drawn whole in the row it made the row as tall as the
 * document - 35,635 px for the spec of a real issue of some 160 rows - and took everything
 * under the row down by as much. So the box it scrolls in (`EDITOR_BOX_ATTRIBUTE`) reaches from
 * where it stands down to the box at the foot of the screen and no further: the page stays where
 * the reader had it, and every line of the box is on screen, so the editor, which keeps its
 * caret in view as the reader writes, scrolls the box and never the page. The box - never the
 * page - is then scrolled so the block stands at its top, where the row's words stood, and once
 * more after the drawing above the block has settled (`refocusWhenSettled`): a picture or a
 * diagram above it draws after the editor opened, and pushes it down.
 *
 * A row so low on the screen that less than half the room between the band at the top and the
 * box at the foot stands under it would leave an editor a line or two tall, so its box takes
 * that half, and the page is scrolled just as far as brings the box on screen (`bringOnScreen`,
 * which leaves a box already on screen where it is).
 *
 * Answers what drops that second scroll.
 */
export function holdEditorAtBlock(host: HTMLElement, blockId: string | undefined): () => void {
  const box = host.querySelector<HTMLElement>(`[${EDITOR_BOX_ATTRIBUTE}]`);
  if (box === null) return () => {};
  const published = getComputedStyle(document.documentElement);
  const height = (property: string) => {
    const value = Number.parseFloat(published.getPropertyValue(property));
    return Number.isFinite(value) ? value : 0;
  };
  const floor = window.innerHeight - height(FOOT_HEIGHT_PROPERTY);
  const room = floor - height(IDENTITY_HEIGHT_PROPERTY);
  const under = floor - box.getBoundingClientRect().top;
  // Whole pixels down, so a box that ends on the line the foot's box starts at is not read as
  // reaching under it by a fraction and scrolled for it.
  box.style.maxHeight = `${Math.floor(Math.min(room, Math.max(under, room / 2)))}px`;
  bringOnScreen(box);
  const place = () => {
    const block = blockIn(box, blockId);
    if (block !== null) scrollElementToTop(block, box);
  };
  place();
  return refocusWhenSettled(place, box.querySelector('[contenteditable="true"]') ?? undefined);
}

/** `blockId`'s block of the editor in `root`, or null where it draws none. */
function blockIn(root: HTMLElement, blockId: string | undefined): HTMLElement | null {
  return blockId === undefined
    ? null
    : root.querySelector<HTMLElement>(`[${BLOCK_ID_ATTRIBUTE}="${CSS.escape(blockId)}"]`);
}

/** The editable of the editor in `host`, where it holds `target`: the element a key or an input
 *  is sent to while the editable holds the focus. */
function editableHolding(host: HTMLElement, target: EventTarget | null): HTMLElement | null {
  const editable = host.querySelector<HTMLElement>('[contenteditable="true"]');
  return editable !== null && target instanceof Node && editable.contains(target) ? editable : null;
}

/** The selection's anchor and focus, where both stand inside `block`, or null. */
function selectionIn(block: HTMLElement): readonly [Point, Point] | null {
  const selection = window.getSelection();
  const anchor = selection?.anchorNode ?? null;
  const focus = selection?.focusNode ?? null;
  if (selection === null || anchor === null || focus === null) return null;
  if (!block.contains(anchor) || !block.contains(focus)) return null;
  return [
    [anchor, selection.anchorOffset],
    [focus, selection.focusOffset],
  ];
}

/** The first place and the last of `block` a caret can stand at: the start of its first text a
 *  caret can enter and the end of its last, or the block itself where it holds none. An atom's
 *  text and a collaborator's cursor label sit in `contenteditable="false"`, where a caret cannot
 *  go. */
function edgesOf(block: HTMLElement): { readonly end: Point; readonly start: Point } {
  const walker = document.createTreeWalker(block, NodeFilter.SHOW_TEXT);
  let first: Node | null = null;
  let last: Node | null = null;
  for (let node = walker.nextNode(); node !== null; node = walker.nextNode()) {
    if (node.parentElement?.isContentEditable !== true) continue;
    first ??= node;
    last = node;
  }
  return {
    end: last === null ? [block, 0] : [last, last.textContent?.length ?? 0],
    start: [first ?? block, 0],
  };
}

/** Where `point` stands against `edge` in the page: before it (negative), at it (zero) or after
 *  it (positive). */
function compare(point: Point, edge: Point): number {
  const range = document.createRange();
  range.setStart(edge[0], edge[1]);
  return range.comparePoint(point[0], point[1]);
}

/** Whether a caret at `point` stands on the line a caret at `edge` does: the middle of its box
 *  inside the other's. A place the page draws no box for counts as on that line, so a move whose
 *  line cannot be read stays in the block. */
function sameLine(point: Point, edge: Point): boolean {
  const caret = caretBox(point);
  const line = caretBox(edge);
  if (caret === null || line === null) return true;
  const middle = (caret.top + caret.bottom) / 2;
  return middle >= line.top && middle <= line.bottom;
}

/** The box of a caret at `point`: the collapsed range's own, or the box of the element it is in
 *  where the browser draws the range none - a place between elements, an empty paragraph. */
function caretBox([node, offset]: Point): DOMRect | null {
  const range = document.createRange();
  range.setStart(node, offset);
  const own: DOMRect | undefined = range.getClientRects()[0];
  if (own !== undefined) return own;
  return (node instanceof Element ? node : node.parentElement)?.getBoundingClientRect() ?? null;
}
