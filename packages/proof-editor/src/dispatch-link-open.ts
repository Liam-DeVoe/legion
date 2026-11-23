/**
 * How the links of a document open (dispatch://INFRA-2160, R1 and R2). The editor has no router,
 * so it hands each link's target to the host's `openLink` (`CreateProofEditorOptions`), which
 * opens it: a `dispatch://` reference and a link to one of the host's own pages in the same tab,
 * another site in a new one. This plugin stands where upstream's `markdownLinkClickPlugin` would,
 * which leaves a plain click in an editable view to place the caret and shows an Open link card.
 * Without `openLink` this plugin is not installed and a link is the browser's.
 *
 * It acts on the links the editor draws (`linkAt`): a link mark's anchor, a reference title
 * (`./reference-decorations.ts`), a link a node view draws as its own content (Dispatch's
 * picture fallback, `picture-view.ts`), and a pointer a typed block's header draws (the `serves`
 * and `needs` links of a plan's task or a Verification document's check, Dispatch's
 * `typed-block-drawing.ts`). An anchor deeper inside a host island - below an element of the
 * editor that is `contenteditable="false"` and is neither the anchor, the anchor's parent nor a
 * typed block's header, such as the slot a host draws a thread into - is the host's, and left to
 * it. ProseMirror sets `contenteditable="false"` on a widget's own element and on a leaf node
 * view's root, so a title and the picture fallback are no island to themselves. A typed block's
 * header (`[data-proof-block-summary]`) is `contenteditable="false"` so that the reader cannot
 * edit it, and is the block's own drawing rather than a slot, so its pointers are the editor's;
 * one in a block a host draws inside its island, such as a comment's in a thread's slot, is the
 * host's.
 *
 * Each link opens through exactly one path, and every other click on it opens nothing:
 *
 * - `handleClick` opens a plain primary pointer click - button 0, no Ctrl, Cmd, Shift or Alt - on
 *   a link whose press reaches ProseMirror (`pressReachesEditor`): a link mark, and a typed block
 *   header's pointer. ProseMirror tells that click from a drag itself. prosemirror-view's
 *   `handlers.mousedown` (`src/input.ts`) starts a `LeftMouseDown` for the press, which hears the
 *   pointer on the view's root until the release, and calls `handleClick` from its `up`
 *   (`handleSingleClick`) only when the pointer never went more than 4 px from the press along
 *   either axis (`updateAllowDefault`; a press with Shift is never a click to it). That press
 *   ends only at its release, at a move with no button held, at a `dragstart`, at the next press,
 *   or at the view's destroy (`destroyInput`): a reconfigure of the editor's plugins leaves it
 *   going. It runs in a view the reader cannot edit too, such as a past version or the Inbox's
 *   reading pane: `initInput` holds back only the handlers of `editHandlers` while the view is
 *   not editable, and `mousedown` is not one of them. `handleClick` answers true, so ProseMirror
 *   prevents the release's default and selects nothing for it (`selectClickedLeaf`,
 *   `selectClickedNode`).
 * - The DOM `click` event opens, with neither Shift nor Alt held, and in a new tab with Ctrl or
 *   Cmd: a click no pointer made (`noPointerMade`: Enter on a focused link, assistive
 *   technology, a script); a Ctrl or Cmd primary click, whose press `takeLinkPress` takes, so
 *   that ProseMirror never sees that press and `handleClick` never runs for it; and any primary
 *   click on a link whose press never reaches ProseMirror, a title's and the picture fallback's.
 *   A drag on such a link is a click by the platform's rule: its press and its release land on
 *   the one element, so the browser sends a click, as it does for a native `<a>` after a drag
 *   inside it, and the link opens. A title prevents its own press, so that drag selects no text.
 *   Measured in builds Job `infra-2160-title-drag-t5nlk` at b1ede4f7 (2026-11-23): a 30 px drag
 *   across a drawn title opened the other issue in Chromium and Firefox, in the editable spec
 *   and in a project document, and left the page's selection empty. ProseMirror has no `click`
 *   handler of its own, so the listener `ensureListeners` adds for this plugin's calls it
 *   (`runCustomHandler`) for every click inside the editor, with no `eventBelongsToView` check: a
 *   widget's and a node view's included, whatever their `stopEvent` says.
 * - Every other click on a link only prevents the browser's default, and opens nothing: a
 *   pointer's click on a link whose press reaches ProseMirror, which
 *   `handleClick` opened if ProseMirror read it as a click and which ended a drag if not, and a
 *   Shift or Alt click. So nothing opens twice, and the browser never follows the `href`.
 * - A middle click (`auxclick`, button 1) opens it in a new tab, which also cancels the browser's
 *   own new tab for an anchor with a real `href`. Its press and its release are prevented, so the
 *   browser pastes nothing into the document (Linux pastes its primary selection on a middle
 *   button's release). The press reaches a link mark and a header's pointer here; a title prevents
 *   every press itself, and the picture fallback prevents a middle one (Dispatch's
 *   `picture-view.ts`).
 * - Alt+Enter (Option+Enter on a Mac) is the host's key, not the editor's: the host's keymap binds
 *   it to the handle's `openLinkAtCaret`, so a reader finds it in the host's list of keys and can
 *   change it. ProseMirror prevents the default of every Enter it does not handle, and a host
 *   listener that skips a prevented press would never hear it, so with a link at the caret the
 *   press is left to the host; should nothing after the editor take it, it is prevented then, so
 *   the browser writes no new line into the text. The caret is read where the page's selection
 *   holds it (`caretPos`), since the editor's state can still hold it where it was before the key
 *   pressed just before.
 */

import type { Mark } from "@milkdown/kit/prose/model";
import { Plugin, PluginKey } from "@milkdown/kit/prose/state";
import type { EditorView } from "@milkdown/kit/prose/view";
import { $prose } from "@milkdown/kit/utils";

import { asElement } from "./record-mark-target";
import { referenceAt } from "./reference-decorations";

/** Opens a link's own target for the host - a `dispatch://` reference, a web address, a `mailto:`
 *  address - in a new tab when `newTab` says so: a Ctrl, Cmd or middle click. Where the target
 *  goes and how is the host's to decide. */
export type OpenLink = (target: string, how: { readonly newTab: boolean }) => void;

const linkOpenKey = new PluginKey("dispatch-link-open");

/** The attribute of the header a typed block's drawing puts above its content (Dispatch's
 *  `typed-block-drawing.ts`): `contenteditable="false"`, yet no host island (`inIsland`), and the
 *  presses on its pointers reach ProseMirror (`pressReachesEditor`). */
const TYPED_BLOCK_HEADER = "data-proof-block-summary";

/** Whether an element between `anchor` and the editor's root, other than the anchor itself, its
 *  parent and a typed block's header, is `contenteditable="false"`: the anchor is inside a host
 *  island. */
function inIsland(view: EditorView, anchor: Element): boolean {
  const parent = anchor.parentElement;
  if (parent === null || parent === view.dom) {
    return false;
  }
  for (
    let node = parent.parentElement;
    node !== null && node !== view.dom;
    node = node.parentElement
  ) {
    if (
      node.getAttribute("contenteditable") === "false" &&
      !node.hasAttribute(TYPED_BLOCK_HEADER)
    ) {
      return true;
    }
  }
  return false;
}

/** Whether a press on `anchor`, a link the editor drew, reaches ProseMirror's own `mousedown`
 *  handler, and so starts the press whose release ProseMirror hands to `handleClick` when it was a
 *  click. prosemirror-view's `initInput` runs that handler, and this plugin's `mousedown` before
 *  it (`runCustomHandler`), only when `eventBelongsToView` holds: the press was not prevented
 *  before it reached the view's root, and no view on its path answers true to `stopEvent`.
 *
 *  It is read from the drawing, with no state: the nearest element from the anchor itself up to
 *  the view's root, the root excluded, that carries a `contenteditable` attribute decides, and
 *  with none up to the root the press reaches ProseMirror. `"false"` there is a widget's own
 *  element or a leaf node view's root, on which ProseMirror sets it (`WidgetViewDesc.create`,
 *  `NodeViewDesc.create`, `src/viewdesc.ts`), and the presses on the links those draw stop short
 *  of ProseMirror; or it is a typed block's header, whose presses do not. Each link the editor
 *  draws, and why:
 *
 *  - A link mark's anchor holds the document's own text, inside no `contenteditable` element:
 *    reached. No view on its path stops a press: a typed block's drawing has no `stopEvent`,
 *    Dispatch's decision block (`AskBlockView`, `ask-block.ts`) stops only events outside its
 *    content hole, and the view of a block a thread is anchored to (`AnchoredBlockView`) only
 *    those in the thread's slot.
 *  - A reference title is a widget's own element: not reached. It prevents its own press, and its
 *    widget's `stopEvent` answers true (`./reference-decorations.ts`).
 *  - The picture fallback's link stands in a leaf node view's root: not reached. The view's
 *    `stopEvent` answers true for every event on the link but a `dragstart` (Dispatch's
 *    `picture-view.ts`).
 *  - A typed block header's pointer stands in the `contenteditable="false"` header
 *    (`TYPED_BLOCK_HEADER`): reached, since the block's drawing - the schema's `toDOM`
 *    (`./block-schema.ts`), or Dispatch's `typedBlockView` (`ask-block.ts`) - has no `stopEvent`,
 *    and nothing prevents the press.
 *
 *  The root is excluded because it is `contenteditable="false"` in a view the reader cannot edit
 *  (prosemirror-view's `computeDocDeco`), whose presses reach ProseMirror all the same. A new
 *  drawing that puts a link under a `contenteditable="false"` element of its own and lets the
 *  presses on it go on to ProseMirror is named here, as the typed block's header is; unnamed, its
 *  link reads as not reached and opens on its `click` alone, the end of a drag on it included. */
function pressReachesEditor(view: EditorView, anchor: Element): boolean {
  for (
    let node: Element | null = anchor;
    node !== null && node !== view.dom;
    node = node.parentElement
  ) {
    const editable = node.getAttribute("contenteditable");
    if (editable !== null) {
      return editable !== "false" || node.hasAttribute(TYPED_BLOCK_HEADER);
    }
  }
  return true;
}

/** A link the editor drew, the target it opens, and whether a press on it reaches ProseMirror
 *  (`pressReachesEditor`), which says the path its pointer clicks open through. The target is the
 *  `dispatch://` reference the link carries as `data-dispatch-href` (a link mark to a reference, a
 *  title, the picture fallback and a header's pointer carry one), else its `href`; it is empty for
 *  a link whose target the sanitizer emptied (a scheme it refuses), which opens nothing. */
interface EditorLink {
  readonly pressReachesEditor: boolean;
  readonly target: string;
}

/** The link the editor drew that `target` is inside, if any; a target that is no element is
 *  inside none. */
function linkAt(view: EditorView, target: EventTarget | null): EditorLink | undefined {
  const anchor = asElement(target)?.closest("a") ?? null;
  if (anchor === null || !view.dom.contains(anchor) || inIsland(view, anchor)) {
    return undefined;
  }
  return {
    pressReachesEditor: pressReachesEditor(view, anchor),
    target: anchor.getAttribute("data-dispatch-href") ?? anchor.getAttribute("href") ?? "",
  };
}

/** The link `handleClick` opens for `event`, the release of a press ProseMirror read as a click: a
 *  plain primary one, with no Ctrl, Cmd, Shift or Alt, on a link whose press reaches ProseMirror.
 *  ProseMirror hands over the release of no Ctrl or Cmd press on a link, which `takeLinkPress`
 *  takes, and of no Shift press (`updateAllowDefault`); an Alt one it hands over opens nothing. A
 *  release on a link whose press never reaches ProseMirror, after a press beside it, opens nothing
 *  either: that link opens on its own `click` alone. */
function releasedLink(view: EditorView, event: MouseEvent): EditorLink | undefined {
  if (event.button !== 0 || event.ctrlKey || event.metaKey || event.shiftKey || event.altKey) {
    return undefined;
  }
  const link = linkAt(view, event.target);
  return link?.pressReachesEditor === true ? link : undefined;
}

/** Whether this plugin's `handleClick` opens a link for `event`, the release ProseMirror hands to
 *  `handleClick` as a click (`releasedLink`): what `./dispatch-mark-events.ts`, whose
 *  `handleClick` runs before this one, stands down on, so that a click on a link inside commented
 *  text opens the link and reports no comment. A link whose target the sanitizer emptied counts:
 *  `handleClick` takes its click, and opens nothing. False where this plugin is not installed. */
export function opensLinkOn(view: EditorView, event: MouseEvent): boolean {
  return linkOpenKey.get(view.state) !== undefined && releasedLink(view, event) !== undefined;
}

/** Where the caret is: in an editor that has the focus, the head of the page's selection, else
 *  the head of the editor's own. A caret an arrow key moved natively reaches ProseMirror's state
 *  only once the page's `selectionchange` for it has run, or once ProseMirror's own handler of the
 *  next key reads the page's selection, before that key's keymaps; a `handleDOMEvents` handler
 *  runs before that read, and a busy page can run the next key before the `selectionchange`
 *  (dispatch://INFRA-2160, k-link-keys). Read from the state alone, Alt+Enter pressed straight
 *  after an arrow found the caret where it stood before the arrow, beside no link, so it was left
 *  to ProseMirror, which prevents every Enter it does not handle, and the host's key never heard
 *  it. Without the focus the page's selection is not the editor's, and the state's caret is. */
function caretPos(view: EditorView): number {
  const selection = view.dom.ownerDocument.getSelection();
  const node = selection?.focusNode ?? null;
  if (selection !== null && node !== null && view.hasFocus() && view.dom.contains(node)) {
    try {
      return view.posAtDOM(node, selection.focusOffset);
    } catch {
      // A point inside something the editor draws no document position for: the state's caret.
    }
  }
  return view.state.selection.head;
}

/** What Alt+Enter opens at the caret (`caretPos`): the target of the link the caret is in or at
 *  either end of, else the `dispatch://` reference of the title drawn over the text the caret
 *  holds or touches (`referenceAt`: the title gives way to that text while the caret is there),
 *  else undefined. A link mark keeps its target as written, a `dispatch://` one included; only
 *  its drawn `href` is sanitized. */
export function linkAtCaret(view: EditorView): string | undefined {
  const { state } = view;
  const $head = state.doc.resolve(caretPos(view));
  const isLink = (mark: Mark) => mark.type.name === "link";
  const href: unknown = (
    $head.nodeAfter?.marks.find(isLink) ?? $head.nodeBefore?.marks.find(isLink)
  )?.attrs.href;
  if (typeof href === "string" && href !== "") {
    return href;
  }
  return referenceAt(state, $head.pos)?.href;
}

/** Prevents the browser's own handling of `event` - a new line in the text - once the press has
 *  reached the window, unless a listener there took it first: the host's keymap listens on the
 *  window, and so heard it before this one, which is added later. */
function preventUnlessTaken(view: EditorView, event: KeyboardEvent): void {
  const window = view.dom.ownerDocument.defaultView;
  if (window === null) {
    event.preventDefault();
    return;
  }
  window.addEventListener(
    "keydown",
    (seen) => {
      if (seen === event && !event.defaultPrevented) {
        event.preventDefault();
      }
    },
    { once: true }
  );
}

/** A middle button's press or release on a link: prevented, so the browser pastes nothing. */
function preventMiddleButton(view: EditorView, event: MouseEvent): boolean {
  if (event.button !== 1 || linkAt(view, event.target) === undefined) {
    return false;
  }
  event.preventDefault();
  return true;
}

/** A press on a link that the editor takes and prevents: a middle button's (`preventMiddleButton`),
 *  and the primary button's with Ctrl or Cmd and neither Shift nor Alt, the press of a click that
 *  opens the link in a new tab. ProseMirror's own `mousedown` handler reads that press as
 *  selecting the node it lands in (Ctrl, or Cmd on a Mac: its `selectNodeModifier`), and selects
 *  it on the release: for a link mark, the paragraph around it, in place of the reader's caret or
 *  selection, for a click that only opens a tab. `initInput` runs that handler only for a press no
 *  `handleDOMEvents` handler took (`runCustomHandler`), and hears the release only through the
 *  listener the handler adds, so a press taken here changes no selection and its click opens the
 *  link; prevented, it moves no caret and focuses nothing either. `initInput` offers this handler
 *  only a press that reaches ProseMirror (`eventBelongsToView`), so a title's and the picture
 *  fallback's presses never come here: a title prevents every press itself, and the picture
 *  fallback prevents a middle one. A plain primary press on a link is not taken: it is
 *  ProseMirror's, which places the caret for it or starts a drag from it, and hands its release to
 *  `handleClick` when it was a click. */
function takeLinkPress(view: EditorView, event: MouseEvent): boolean {
  const newTab =
    event.button === 0 && (event.ctrlKey || event.metaKey) && !event.shiftKey && !event.altKey;
  if (!newTab) {
    return preventMiddleButton(view, event);
  }
  if (linkAt(view, event.target) === undefined) {
    return false;
  }
  event.preventDefault();
  return true;
}

/** Whether no pointer made the click `event`: Enter on a focused link, assistive technology or a
 *  script. A click is a `PointerEvent` in the engines Dispatch runs in, whose `pointerType` is
 *  empty for such a click and names the pointer ("mouse", "pen", "touch") for a pointer's.
 *  `detail`, the click count, does not tell them apart in every engine: Enter's click on a focused
 *  link carries `detail` 0 in Chromium but 1 in Firefox (with `mozInputSource` 6, the keyboard),
 *  measured in builds Job `infra-2160-probe-click-new-6gxl8` at b1ede4f7 (2026-11-23), where a
 *  mouse's click carried `pointerType` "mouse" in both. So `detail` is read only for a click that
 *  carries no `pointerType`. */
function noPointerMade(event: MouseEvent): boolean {
  const pointerType: unknown = (event as Partial<PointerEvent>).pointerType;
  return typeof pointerType === "string" ? pointerType === "" : event.detail === 0;
}

/** The plugin that opens the document's links through the host's `openLink` (the module's doc
 *  says which links, through which path, and why). */
export function dispatchLinkOpenPlugin(openLink: OpenLink) {
  /** Hands `link`'s target to the host, unless the sanitizer emptied it. */
  const open = (link: EditorLink, newTab: boolean) => {
    if (link.target !== "") {
      openLink(link.target, { newTab });
    }
  };
  return $prose(
    () =>
      new Plugin({
        key: linkOpenKey,
        props: {
          handleClick(view, _pos, event) {
            const link = releasedLink(view, event);
            if (link === undefined) {
              return false;
            }
            open(link, false);
            return true;
          },
          handleDOMEvents: {
            click(view, event) {
              // `click` is the primary button's (a middle click opens on `auxclick`).
              const link = event.button === 0 ? linkAt(view, event.target) : undefined;
              if (link === undefined) {
                return false;
              }
              event.preventDefault();
              // The click's own path: one no pointer made, one with Ctrl or Cmd, and one on a
              // link whose press never reaches ProseMirror; never one with Shift or Alt. A plain
              // pointer click on a link whose press reaches ProseMirror is `handleClick`'s.
              const opens =
                !event.shiftKey &&
                !event.altKey &&
                (noPointerMade(event) ||
                  event.ctrlKey ||
                  event.metaKey ||
                  !link.pressReachesEditor);
              if (opens) {
                open(link, event.ctrlKey || event.metaKey);
              }
              return true;
            },
            auxclick(view, event) {
              const link = event.button === 1 ? linkAt(view, event.target) : undefined;
              if (link === undefined) {
                return false;
              }
              event.preventDefault();
              open(link, true);
              return true;
            },
            mousedown: takeLinkPress,
            mouseup: preventMiddleButton,
            keydown(view, event) {
              const altEnter =
                event.key === "Enter" &&
                event.altKey &&
                !event.ctrlKey &&
                !event.metaKey &&
                !event.shiftKey;
              if (!view.editable || !altEnter || linkAtCaret(view) === undefined) {
                return false;
              }
              preventUnlessTaken(view, event);
              return true;
            },
          },
        },
      })
  );
}
