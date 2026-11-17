import { BLOCK_ID_ATTRIBUTE } from "../doc/block-id";
import { refocusWhenSettled } from "../doc/refocus-when-settled";
import { scrollElementToTop } from "../doc/scroll-block";
import { bringOnScreen, FOOT_HEIGHT_PROPERTY, IDENTITY_HEIGHT_PROPERTY } from "./identity-block";

/**
 * Putting the caret in a node's own text, where that text lives, and holding the editor that
 * text is drawn in to the window while the row holds it.
 *
 * The edit key draws the node's document in the row (`document-stage.tsx`) and then calls these:
 * `caretInBlock` places the caret at the start of that node's own block, and `holdEditorAtBlock`
 * opens the editor's box at that block. The caret is a DOM selection inside the editor and
 * nothing else: the editor's own observer reads the selection back as the document's, so this
 * touches neither the room nor the editor. While the row holds the editor, `confineKey`,
 * `confineInput` and `selectionIntoBlock` keep the reader's keys and selection inside that block.
 *
 * The editor is the document's whole editor, its box opened at the node's block, and the keys
 * the editor answers for a whole document reached past that block: measured on a preview,
 * Ctrl+Home moved the caret before the spec's `## Summary`, and a `Y` typed then made it
 * `## YSummary`, scrolled out of the box above the row; Up after Left put the caret before a
 * heading above the statement. An edit of text the reader cannot see is the loss the caret
 * placed at the top of the document was, so the row's editor keeps its selection, and every
 * key, inside the node's block. Free editing of the whole document stays on the document page.
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

/** The mark of a typed block's own box (`doc/typed-block-drawing.ts`): the schema makes each
 *  such block isolating (`@legion/proof-editor`'s `block-schema.ts`), so no split, join or lift
 *  the editor makes crosses its sides. */
const TYPED_BLOCK_ATTRIBUTE = "data-proof-block-type";

/** The platforms ProseMirror reads as a Mac (`prosemirror-keymap`), and the keys its keymap
 *  takes there with Ctrl for Backspace and Delete (`prosemirror-commands`' `macBaseKeymap`). */
const MAC_PLATFORM = /Mac|iP(hone|[oa]d)/;
const MAC_CTRL_DELETES: Readonly<Record<string, string | undefined>> = {
  d: "Delete",
  h: "Backspace",
};

/** A place in the page, as each end of a selection is one. */
type Point = readonly [node: Node, offset: number];

/**
 * Keeps a key pressed in the editor in `host` inside `blockId`'s block, before the editor acts
 * on it: called on the key in the capture phase, so before ProseMirror's own listener on the
 * editable, which acts on a key on its keydown - its keymap edits through its own selection,
 * and a key it leaves to the browser moves the DOM's - and ignores a key whose default is
 * prevented (`prosemirror-view`'s `eventBelongsToView`). A key is refused (`preventDefault`)
 * where it would take the caret or an edit out of the block:
 *
 * - any key but those that move the focus or end the editing (`PASSING_KEYS`) while the
 *   selection is not inside the block, or the editor draws no such block: before the caret is
 *   placed, or a Tab into the editable before the block is drawn, no key edits the document;
 * - Up on the block's first line and Down on its last, Left at its start and Right at its end,
 *   PageUp and PageDown, which leave the caret where it is;
 * - Backspace at its start and Delete at its end, which join it to the block beside it, and on
 *   a Mac Ctrl+H and Ctrl+D, which the editor takes for them;
 * - Enter in a block that is no typed block, which splits it and puts the caret in the new
 *   block after it - a typed block holds its paragraphs, and the split stays inside it.
 *
 * Ctrl+Home and Ctrl+End (Meta too, and Meta+Up and Meta+Down, a Mac's keys for them) put the
 * caret at the block's start and end, and Ctrl+A (or Meta+A) selects the block's text alone,
 * which the editor takes as its own at once (`selectInEditor`); with Shift held a move extends
 * the selection to the block's end it stops at.
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
    event.preventDefault();
    return;
  }
  const taken = keyAtBlock(event, block, ...selected);
  if (taken === undefined) return;
  event.preventDefault();
  if (taken !== null) selectInEditor(taken[0][0], taken[0][1], taken[1][0], taken[1][1]);
}

/**
 * Refuses an edit the browser is about to make in the editor in `host` outside `blockId`'s
 * block: one made with the selection out of the block, or whose target ranges reach past it.
 * The editor leaves typed text, and a key its keymap does not answer, to the browser, and reads
 * the change from the page after it; this is the backstop for such a key `confineKey` does not
 * name. Called on the `beforeinput` in the capture phase.
 */
export function confineInput(
  host: HTMLElement,
  blockId: string | undefined,
  event: InputEvent
): void {
  const editable = editableHolding(host, event.target);
  if (editable === null || event.defaultPrevented) return;
  const block = blockIn(editable, blockId);
  if (
    block !== null &&
    selectionIn(block) !== null &&
    event
      .getTargetRanges()
      .every((range) => block.contains(range.startContainer) && block.contains(range.endContainer))
  ) {
    return;
  }
  event.preventDefault();
}

/**
 * Refuses a paste into the editor in `host` that would change more than the words of `blockId`'s
 * block: any paste while the selection is out of the block, and one whose text holds a line
 * break into a block that is no typed block. The editor reads each line of a pasted text as a
 * paragraph of its own (`prosemirror-view`'s `parseFromClipboard`), and those after the first go
 * after the node's block, or into a heading as a line break: measured in Chromium, two lines
 * pasted at the start of a decision's `## ` heading stored it as a heading of two lines, `Two\`,
 * `lines...` and `---` under them. A typed block holds the paragraphs a paste makes in it, and a
 * paste of one line stays in the block it is made in. Called on the `paste` in the capture
 * phase, before the editor's own listener, which ignores a paste whose default is prevented.
 */
export function confinePaste(
  host: HTMLElement,
  blockId: string | undefined,
  event: ClipboardEvent
): void {
  const editable = editableHolding(host, event.target);
  if (editable === null || event.defaultPrevented) return;
  const block = blockIn(editable, blockId);
  if (
    block !== null &&
    selectionIn(block) !== null &&
    (block.hasAttribute(TYPED_BLOCK_ATTRIBUTE) ||
      !/[\r\n]/.test(event.clipboardData?.getData("text/plain") ?? ""))
  ) {
    return;
  }
  event.preventDefault();
}

/**
 * Refuses a drag that starts in the editor in `host`, and a drop into it: a drop edits the
 * document where it lands, in whichever block that is, and text dragged out of the node's block
 * is moved, not copied, wherever the editor takes it. Called on `dragstart` and `drop` in the
 * capture phase, before the editor's own listeners, which ignore them once their default is
 * prevented.
 */
export function confineDrag(host: HTMLElement, event: Event): void {
  if (editableHolding(host, event.target) !== null) event.preventDefault();
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
  // A Mac's editor keys for Backspace and Delete: Ctrl+H and Ctrl+D, and Alt+D, which the editor
  // reads by the key's code since a Mac types a letter of its own with Alt held.
  const mac = MAC_PLATFORM.test(navigator.platform);
  const macDelete =
    mac && event.ctrlKey && !event.metaKey && !event.altKey
      ? MAC_CTRL_DELETES[event.key.toLowerCase()]
      : mac && event.altKey && !event.ctrlKey && !event.metaKey && event.code === "KeyD"
        ? "Delete"
        : undefined;
  switch (macDelete ?? event.key) {
    case "Home":
      return jump ? extendTo(start) : undefined;
    case "End":
      return jump ? extendTo(end) : undefined;
    case "a":
    case "A":
      return jump && !event.altKey && !event.shiftKey ? [start, end] : undefined;
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
    case "Backspace":
      return collapsed && compare(focus, start) <= 0 ? null : undefined;
    case "Delete":
      return collapsed && compare(focus, end) >= 0 ? null : undefined;
    case "Enter":
      return event.shiftKey || block.hasAttribute(TYPED_BLOCK_ATTRIBUTE) ? undefined : null;
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
