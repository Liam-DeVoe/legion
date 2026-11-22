/**
 * How the links of a document open, in place of upstream's `markdownLinkClickPlugin`, which left a
 * plain click in an editable view to place the caret and showed an Open link card instead
 * (dispatch://INFRA-2160, R1 and R2). The editor has no router, so the host opens each target
 * itself through `openLink` (`CreateProofEditorOptions`): a `dispatch://` reference and a link to
 * one of the host's own pages in the same tab, another site in a new one. Without `openLink` this
 * plugin is not installed and a link is the browser's.
 *
 * It acts on the links the editor draws: a link mark's anchor, a reference title
 * (`./reference-decorations.ts`), a link a node view draws as its own content (Dispatch's
 * picture fallback), and a pointer a typed block's header draws (the `serves` and `needs` links
 * of a plan's task or a Verification document's check, Dispatch's `typed-block-drawing.ts`). An
 * anchor deeper inside a host island - below an element of the editor that is
 * `contenteditable="false"` and is neither the anchor, the anchor's parent nor a typed block's
 * header, such as the slot a host draws a thread into - is the host's, and left to it.
 * ProseMirror sets `contenteditable="false"` on a widget's own element and on a leaf node view's
 * root, so a title and the picture fallback are no island to themselves. A typed block's header
 * (`[data-proof-block-summary]`) is `contenteditable="false"` so that the reader cannot edit it,
 * and is the block's own drawing rather than a slot, so its pointers are the editor's; one in a
 * block a host draws inside its island, such as a comment's in a thread's slot, is the host's.
 *
 * - A primary click with no modifier opens the link, in every access mode and inside commented
 *   text, whatever the page held selected before it; Ctrl or Cmd opens it in a new tab. A click is
 *   told from a drag by the pointer alone (`clickedLink`): a press on the link whose pointer moves
 *   more than `CLICK_SLOP` before the release ends a drag, which opens nothing. That press lasts
 *   as long as ProseMirror's own press, to the click after it (`LinkPress`): a reconfigure of the
 *   editor's plugins during it forgets nothing. A redraw of the pressed link opens nothing, since
 *   the browser sends no click once the element the press landed on is removed. ProseMirror has
 *   no built-in `click` handler, so this one runs for every click inside the editor, a widget's
 *   and a node view's included, whatever their `stopEvent` says. The press of a Ctrl or Cmd click
 *   on a link mark or a header's pointer is prevented and taken, so ProseMirror, which reads it
 *   as selecting the node it lands in, selects nothing, and the reader's caret or selection stays
 *   as it was. A title prevents every press itself, and the picture fallback keeps every press
 *   from the editor and puts the caret after itself on a primary one (Dispatch's
 *   `picture-view.ts`).
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

/** The attribute of the header a typed block's drawing puts above its content
 *  (Dispatch's `typed-block-drawing.ts`): `contenteditable="false"`, and no host island. */
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

/** A link the editor drew, and the target it opens: the `dispatch://` reference it carries as
 *  `data-dispatch-href` (a link mark to a reference, a title, the picture fallback and a header's
 *  pointer carry one), else its `href`. Empty for a link whose target the sanitizer emptied (a
 *  scheme it refuses), which opens nothing. */
interface EditorLink {
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
    target: anchor.getAttribute("data-dispatch-href") ?? anchor.getAttribute("href") ?? "",
  };
}

/** How far the pointer may move from a press on a link, in CSS pixels along either axis, for the
 *  release to be a click rather than the end of a drag: 4, the usual drag threshold, and the one
 *  ProseMirror reads its own presses by (prosemirror-view's `updateAllowDefault`), so that a
 *  release ProseMirror hands to `handleClick` as a click is one this plugin reads as a click too
 *  (`opensLinkOn`). */
const CLICK_SLOP = 4;

/** Each view's noted press. */
const linkPresses = new WeakMap<EditorView, LinkPress>();

/** A plain primary press on a link (`notePress`), until its click, the next press, or its end with
 *  no click on the link: the target of the link it landed on, where the pointer was, and whether
 *  the pointer has since gone more than `CLICK_SLOP` from there. The press is matched to its
 *  release and its click by the link's target, not by the anchor element, because a target holds
 *  less state. From the press to the release it hears the pointer where ProseMirror hears its own
 *  press's, on the view's root, adding and dropping those listeners as prosemirror-view's
 *  `LeftMouseDown` does, so a drag that leaves the editor and comes back to the press is still a
 *  drag, as ProseMirror reads it. A reconfigure of the editor's plugins, which destroys and
 *  remakes the plugin's view, ends neither this press nor ProseMirror's; the view's own destroy
 *  ends both. A redraw of the link while the button is held, such as the new anchor ProseMirror
 *  draws for a collaborator's bold over its words, opens nothing, however the press is matched:
 *  the browser sends no click once the element the press landed on is removed. Blink's
 *  `MouseEventManager::HandleRemoveSubtree` sets `mousedown_element_` to null when that element
 *  is removed (`third_party/blink/renderer/core/input/mouse_event_manager.cc`: "We don't
 *  dispatch click events if the mousedown node is removed before a mouseup event. It is
 *  compatible with IE and Firefox."), and Blink sends a click only while that element is still
 *  connected. A run of dispatch://INFRA-2160 in Firefox and WebKit, in which a collaborator's
 *  bold drew the pressed link again, opened nothing either. */
class LinkPress {
  readonly target: string;
  readonly x: number;
  readonly y: number;
  moved = false;
  private readonly view: EditorView;

  /** A move more than `CLICK_SLOP` from the press with the button held marks the press as moved.
   *  A move with no button held ends the press, as it ends ProseMirror's: the browser took it for
   *  a drag of its own, for which Chromium sends neither a release nor a click. */
  private readonly move = (event: Event) => {
    const pointer = event as MouseEvent;
    if (pointer.buttons === 0) {
      this.done();
    } else if (movedFrom(this, pointer)) {
      this.moved = true;
    }
  };

  /** The release, after which no move is heard. A release on the press's link, an anchor
   *  `linkAt` finds the press's target for, keeps the note for the click that follows it; a
   *  release anywhere else sends no click to the link, and ends the press. */
  private readonly up = (event: Event) => {
    this.stopHearing();
    if (linkAt(this.view, event.target)?.target !== this.target) {
      this.done();
    }
  };

  constructor(view: EditorView, target: string, event: MouseEvent) {
    this.view = view;
    this.target = target;
    this.x = event.clientX;
    this.y = event.clientY;
    view.root.addEventListener("mousemove", this.move);
    view.root.addEventListener("mouseup", this.up);
  }

  /** Forgets the press, and hears no more of its pointer. */
  done(): void {
    this.stopHearing();
    if (linkPresses.get(this.view) === this) {
      linkPresses.delete(this.view);
    }
  }

  private stopHearing(): void {
    this.view.root.removeEventListener("mousemove", this.move);
    this.view.root.removeEventListener("mouseup", this.up);
  }
}

/** Whether the pointer of `event` is more than `CLICK_SLOP` from `press` along either axis. */
function movedFrom(press: LinkPress, event: MouseEvent): boolean {
  return (
    Math.abs(event.clientX - press.x) > CLICK_SLOP || Math.abs(event.clientY - press.y) > CLICK_SLOP
  );
}

/** Notes a plain primary press on a link - no Ctrl, Cmd, Shift or Alt - and forgets the note of
 *  the press before it, on every press, a modified one on that same link included. The press is
 *  not taken: ProseMirror places the caret for it, or starts a drag from it. */
function notePress(view: EditorView, event: MouseEvent): void {
  linkPresses.get(view)?.done();
  const plain =
    event.button === 0 && !event.ctrlKey && !event.metaKey && !event.shiftKey && !event.altKey;
  const link = plain ? linkAt(view, event.target) : undefined;
  if (link !== undefined) {
    linkPresses.set(view, new LinkPress(view, link.target, event));
  }
}

/** The link a primary click opens, or undefined for a click that opens none: the end of a drag on
 *  the link, a press noted on a link to that same target whose pointer went more than
 *  `CLICK_SLOP` from it before this click, opens nothing, so the words the drag selected stay
 *  selected. The press and the click are matched by the link's target (`LinkPress` says why).
 *  What the page held selected before the press counts for nothing: a sentence selected across
 *  the link, as for the selection bar's Comment, leaves the first click on the link a click,
 *  which opens it (dispatch://INFRA-2160, R1). Every other primary click on a link opens it, one
 *  no press was noted for included: a Ctrl or Cmd click, whose press `takeLinkPress` takes, a
 *  title's, which prevents its own press, and one a script or assistive technology makes. A drag
 *  that starts off the link and ends on it sends its click to the nearest element that holds
 *  both its press and its release, which is no link, so it opens nothing either; a click of it
 *  sent to the anchor would find no press noted for the link, and open it. */
function clickedLink(view: EditorView, event: MouseEvent): EditorLink | undefined {
  if (event.button !== 0 || event.shiftKey || event.altKey) {
    return undefined;
  }
  const link = linkAt(view, event.target);
  const press = linkPresses.get(view);
  const dragged =
    link !== undefined && press?.target === link.target && (press.moved || movedFrom(press, event));
  return dragged ? undefined : link;
}

/** Whether the editor opens a link for `event`, a primary click or the release ProseMirror reads
 *  as one (`handleClick`): what `./dispatch-mark-events.ts` stands down on, so a click on a link
 *  inside commented text opens the link rather than the comment. ProseMirror reads a release as a
 *  click only when its pointer stayed within `CLICK_SLOP` of the press, as `clickedLink` does, so
 *  the release and the click after it get the same answer. False where this plugin is not
 *  installed. */
export function opensLinkOn(view: EditorView, event: MouseEvent): boolean {
  return linkOpenKey.get(view.state) !== undefined && clickedLink(view, event) !== undefined;
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
 *  selection, for a click that only opens a tab. ProseMirror runs that handler only for a press no
 *  `handleDOMEvents` handler took, and hears the release only through the listener the handler
 *  adds, so a press taken here changes no selection; prevented, it moves no caret and focuses
 *  nothing either. A plain primary press on a link is noted (`notePress`), not taken. */
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

/** The plugin that opens the document's links through the host's `openLink` (the module's doc
 *  says which links and how). */
export function dispatchLinkOpenPlugin(openLink: OpenLink) {
  return $prose(
    () =>
      new Plugin({
        key: linkOpenKey,
        // A press under way stops hearing the page once the view is destroyed, as ProseMirror's
        // does. ProseMirror destroys every plugin view on a reconfigure of the editor's plugins
        // too, and makes them anew, while its own press goes on: the note goes on with it, for
        // the new plugin view to end at the view's destroy. `EditorView.destroy` destroys the
        // plugin views before the view reads as `isDestroyed`, so which of the two this is can be
        // read only once the destroy has run, in a microtask.
        view: (editorView) => ({
          destroy: () =>
            queueMicrotask(() => {
              if (editorView.isDestroyed) {
                linkPresses.get(editorView)?.done();
              }
            }),
        }),
        props: {
          handleDOMEvents: {
            click(view, event) {
              const link = clickedLink(view, event);
              linkPresses.get(view)?.done();
              if (link === undefined) {
                return false;
              }
              event.preventDefault();
              if (link.target !== "") {
                openLink(link.target, { newTab: event.ctrlKey || event.metaKey });
              }
              return true;
            },
            auxclick(view, event) {
              const link = event.button === 1 ? linkAt(view, event.target) : undefined;
              if (link === undefined) {
                return false;
              }
              event.preventDefault();
              if (link.target !== "") {
                openLink(link.target, { newTab: true });
              }
              return true;
            },
            mousedown(view, event) {
              notePress(view, event);
              return takeLinkPress(view, event);
            },
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
