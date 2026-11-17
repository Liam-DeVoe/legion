/**
 * How the links of a document open, in place of upstream's `markdownLinkClickPlugin`, which left a
 * plain click in an editable view to place the caret and showed an Open link card instead
 * (dispatch://INFRA-2160, R1 and R2). The editor has no router, so the host opens each target
 * itself through `openLink` (`CreateProofEditorOptions`): a `dispatch://` reference and a link to
 * one of the host's own pages in the same tab, another site in a new one. Without `openLink` this
 * plugin is not installed and a link is the browser's.
 *
 * It acts on the links the editor draws: a link mark's anchor, a reference title
 * (`./reference-decorations.ts`), and a link a node view draws as its own content (Dispatch's
 * picture fallback). An anchor deeper inside a host island - below an element of the editor that
 * is `contenteditable="false"` and is neither the anchor nor the anchor's parent, such as the slot
 * a host draws a thread into - is the host's, and left to it. ProseMirror sets
 * `contenteditable="false"` on a widget's own element and on a leaf node view's root, so a title
 * and the picture fallback are no island to themselves.
 *
 * - A primary click with no modifier opens the link, in every access mode and inside commented
 *   text, unless a drag selected text across it; Ctrl or Cmd opens it in a new tab. ProseMirror has
 *   no built-in `click` handler, so this one runs for every click inside the editor, a widget's and
 *   a node view's included, whatever their `stopEvent` says.
 * - A middle click (`auxclick`, button 1) opens it in a new tab, which also cancels the browser's
 *   own new tab for an anchor with a real `href`. Its press and its release are prevented, so the
 *   browser pastes nothing into the document (Linux pastes its primary selection on a middle
 *   button's release). The press reaches a link mark here; a title prevents every press itself,
 *   and the picture fallback prevents a middle one (Dispatch's `picture-view.ts`).
 * - Alt+Enter (Option+Enter on a Mac) is the host's key, not the editor's: the host's keymap binds
 *   it to the handle's `openLinkAtCaret`, so a reader finds it in the host's list of keys and can
 *   change it. ProseMirror prevents the default of every Enter it does not handle, and a host
 *   listener that skips a prevented press would never hear it, so with a link at the caret the
 *   press is left to the host; should nothing after the editor take it, it is prevented then, so
 *   the browser writes no new line into the text.
 */

import type { Mark } from "@milkdown/kit/prose/model";
import { type EditorState, Plugin, PluginKey } from "@milkdown/kit/prose/state";
import type { EditorView } from "@milkdown/kit/prose/view";
import { $prose } from "@milkdown/kit/utils";

import { referenceAt } from "./reference-decorations";

/** Opens a link's own target for the host - a `dispatch://` reference, a web address, a `mailto:`
 *  address - in a new tab when `newTab` says so: a Ctrl, Cmd or middle click. Where the target
 *  goes and how is the host's to decide. */
export type OpenLink = (target: string, how: { readonly newTab: boolean }) => void;

const linkOpenKey = new PluginKey("dispatch-link-open");

/** Whether an element between `anchor` and the editor's root, other than the anchor itself and
 *  its parent, is `contenteditable="false"`: the anchor is inside a host island. */
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
    if (node.getAttribute("contenteditable") === "false") {
      return true;
    }
  }
  return false;
}

/** A link the editor drew, and the target it opens: the `dispatch://` reference a link mark or a
 *  title carries as `data-dispatch-href`, else its `href`. Empty for a link whose target the
 *  sanitizer emptied (a scheme it refuses), which opens nothing. */
interface EditorLink {
  readonly anchor: Element;
  readonly target: string;
}

/** The link the editor drew that `target` is inside, if any; a target that is no element is
 *  inside none. */
function linkAt(view: EditorView, target: EventTarget | null): EditorLink | undefined {
  const element = target as Partial<Element> | null;
  const anchor = typeof element?.closest === "function" ? (element as Element).closest("a") : null;
  if (anchor === null || !view.dom.contains(anchor) || inIsland(view, anchor)) {
    return undefined;
  }
  return {
    anchor,
    target: anchor.getAttribute("data-dispatch-href") ?? anchor.getAttribute("href") ?? "",
  };
}

/** The link a primary click opens, or undefined for a click that opens none: the one a drag that
 *  ended on it selected text across - which the click must not throw away by opening the link -
 *  opens nothing. A title prevents its own press, so an older selection elsewhere may stand while
 *  it is clicked; only one across the link counts. A Ctrl or Cmd click is never a drag's end:
 *  ProseMirror reads its press as selecting the node it lands in, which leaves a selection across
 *  the link that the reader never made. */
function clickedLink(view: EditorView, event: MouseEvent): EditorLink | undefined {
  if (event.button !== 0 || event.shiftKey || event.altKey) {
    return undefined;
  }
  const link = linkAt(view, event.target);
  if (link === undefined || event.ctrlKey || event.metaKey) {
    return link;
  }
  const selection = view.dom.ownerDocument.getSelection();
  const dragged =
    selection !== null && !selection.isCollapsed && selection.containsNode(link.anchor, true);
  return dragged ? undefined : link;
}

/** Whether the editor opens a link for `event`, a primary click or the release ProseMirror reads
 *  as one (`handleClick`): what `./dispatch-mark-events.ts` stands down on, so a click on a link
 *  inside commented text opens the link rather than the comment. False where this plugin is not
 *  installed. */
export function opensLinkOn(view: EditorView, event: MouseEvent): boolean {
  return linkOpenKey.get(view.state) !== undefined && clickedLink(view, event) !== undefined;
}

/** What Alt+Enter opens at the caret: the target of the link the caret is in or at either end
 *  of, else the `dispatch://` reference of the title drawn over the text the caret holds or
 *  touches (`referenceAt`: the title gives way to that text while the caret is there), else
 *  undefined. A link mark keeps its target as written, a `dispatch://` one included; only its
 *  drawn `href` is sanitized. */
export function linkAtCaret(state: EditorState): string | undefined {
  const { $head } = state.selection;
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

/** The plugin that opens the document's links through the host's `openLink` (the module's doc
 *  says which links and how). */
export function dispatchLinkOpenPlugin(openLink: OpenLink) {
  return $prose(
    () =>
      new Plugin({
        key: linkOpenKey,
        props: {
          handleDOMEvents: {
            click(view, event) {
              const link = clickedLink(view, event);
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
            mousedown: preventMiddleButton,
            mouseup: preventMiddleButton,
            keydown(view, event) {
              const altEnter =
                event.key === "Enter" &&
                event.altKey &&
                !event.ctrlKey &&
                !event.metaKey &&
                !event.shiftKey;
              if (!view.editable || !altEnter || linkAtCaret(view.state) === undefined) {
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
