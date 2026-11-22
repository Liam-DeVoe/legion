import { Component, type ReactNode, type RefObject } from "react";

import { refPreviewCardContains, refPreviewOpenedIn } from "../refs/ref-preview-card";

export interface AnchorRow {
  id: string;
  /** How the row was chosen: keyboard focus within it, the pointer resting on it, or, failing
   *  both, the row nearest the list's visible centre. */
  via: "center" | "focus" | "pointer";
}

/** A row above the current one that a press of the reader's is taking away (`leaving`), as the
 *  commit's snapshot reads it, immediately before the row goes. */
interface Leaving {
  /** How far the current row moves up once this row has gone from above it: the whole space the
   *  row takes in the list (`spaceTaken`), the part above the view's top included. */
  by: number;
  id: string;
  /** The element right before the row where that element is no row (a group's heading), and how
   *  far the current row moves up once that element has gone too, read as `by` is. Null where a
   *  row, or nothing, stands right before the row. A group's heading goes with the last row of
   *  its group. */
  lead: { by: number; element: Element } | null;
}

interface Snapshot extends AnchorRow {
  /** The row's `group` attribute value immediately before the commit; null without one. */
  group: string | null;
  /** The rows above this one that a press of the reader's is taking away; empty where the host
   *  names none. */
  leaving: readonly Leaving[];
  /** The top of this component's own root, in viewport pixels, immediately before a commit in
   *  which `leaving` names rows; null otherwise. What stands above the list can shrink in the
   *  commit in which such a row goes, because of the same press, as a page's control offered only
   *  while such rows are listed goes with the last of them. `keepPlace` counts that shrink only
   *  where such a row went. */
  rootTop: number | null;
  /** The row's top, in viewport pixels, immediately before the commit. */
  top: number;
}

/** What the reader can see of the rows, top and bottom, in viewport pixels. */
interface View {
  bottom: number;
  top: number;
}

/** The space `element` takes in its list, in pixels: from its own top to the top of the element
 *  after it, the gap between them included, else to its own bottom. What a row a press of the
 *  reader's takes away frees for the rows after it (`ViewportAnchor`'s `leaving`), and what the
 *  selection bar's hand-off reads to tell where each row will stand once such rows have gone
 *  (`components/SelectionBar.tsx`), so the two agree. */
export function spaceTaken(element: Element): number {
  const box = element.getBoundingClientRect();
  const after = element.nextElementSibling;
  return (after === null ? box.bottom : after.getBoundingClientRect().top) - box.top;
}

export interface ViewportAnchorProps {
  as?: "div" | "section";
  children: ReactNode;
  className?: string;
  /** When false at commit time, the commit is left to shift the view (the Conversation while it
   *  follows the latest turn). Defaults to always compensating. */
  enabled?: () => boolean;
  /** Attribute naming the group a row sits in, e.g. `data-inbox-section`. A row whose value
   *  changed across a commit was relocated - it left its place for another group - rather than
   *  shifted by content around it, and the view is not dragged after it. */
  group?: string;
  /** Attribute naming each row and carrying its id, e.g. `data-inbox-row`. */
  item: string;
  /** Whether a press of the reader's is taking row `id` away from its place: a Dismiss, or a
   *  write that moves the row into another group once it lands. The anchor does not hold the
   *  current row in place against the going of such a row above it: the current row and the rows
   *  after it move up by the whole space the row took, and the view does not scroll. It is asked
   *  of the render that last drew the rows and of the render being committed alike, because a
   *  write can stop naming its rows in the same commit that moves them. With no function, no row
   *  is named. */
  leaving?: (id: string) => boolean;
  label?: string;
  /** Receives the rendered root element, for a host that also queries the rows itself. */
  rootRef?: RefObject<HTMLElement | null>;
  /** The element the rows scroll in, when they scroll in one rather than the document (the
   *  Inbox beside its reading pane, where each column scrolls in itself). The anchor then reads
   *  and moves that element's `scrollTop`, and measures the list against its box. With no ref,
   *  or one holding null, the window scrolls the rows as it always has. */
  scroller?: RefObject<HTMLElement | null>;
}

const probeSteps = 5;
const probeStepPx = 8;

// An O(1) hit test for the row nearest the centre of the list's own visible portion, rather than
// a getBoundingClientRect scan over every row (hundreds of forced layout reads per update on a
// long list). The probe point is derived from the list's bounding rect - not
// window.innerWidth/innerHeight - because the main column is rarely centred in (or as wide as)
// the viewport: the sidebar and margin panel narrow it well before the window itself is narrow,
// and hit-testing the window's centre can land in a neighbouring column entirely, missing every
// probe. elementFromPoint at the exact centre can still miss within the column - landing on a
// gap, heading, or composer - in which case this probes downward first, then upward, in small
// steps: a content change lands between two rows more often than exactly on one, and probing
// downward first means the answer for a point in the gap is consistently "whichever row is
// below", matching reading order. What is visible is the window's own box, or `scroller`'s when
// the rows scroll in one: a list scrolled inside an element reaches above and below that
// element's edges, not the window's, and probing a point the scroller clips lands on whatever
// covers it instead.
function rowAtCenter(
  root: HTMLElement,
  selector: string,
  scroller: HTMLElement | null
): HTMLElement | null {
  const rect = root.getBoundingClientRect();
  const box = scroller === null ? null : scroller.getBoundingClientRect();
  const visibleLeft = Math.max(rect.left, box?.left ?? 0);
  const visibleRight = Math.min(rect.right, box?.right ?? window.innerWidth);
  const x = (visibleLeft + visibleRight) / 2;
  const visibleTop = Math.max(rect.top, box === null ? 0 : box.top);
  const visibleBottom = Math.min(rect.bottom, box === null ? window.innerHeight : box.bottom);
  const y = (visibleTop + visibleBottom) / 2;
  const hit = (offset: number): HTMLElement | null =>
    document.elementFromPoint(x, y + offset)?.closest<HTMLElement>(selector) ?? null;

  let found = hit(0);
  for (let step = 1; found === null && step <= probeSteps; step += 1) {
    found = hit(step * probeStepPx);
  }
  for (let step = 1; found === null && step <= probeSteps; step += 1) {
    found = hit(-step * probeStepPx);
  }
  return found !== null && root.contains(found) ? found : null;
}

function rowWithin(root: HTMLElement, selector: string, node: Element | null): HTMLElement | null {
  const row = node?.closest<HTMLElement>(selector) ?? null;
  return row !== null && root.contains(row) ? row : null;
}

/**
 * Keeps the row the reader is currently on pinned to its viewport position across commits that
 * shift content around it: a row inserted or removed above, a reorder within its section, a
 * banner appearing. The current row is the one with keyboard focus within it, else the one under
 * the pointer, else the one nearest the list's visible centre - the first two hold even at the
 * top of the page (the hand is on that row), the centre only once the reader has scrolled, since
 * a reader at the top is looking for arrivals and should see them land. A row that itself leaves
 * its place for another `group` (a section) is not followed: the reader's place is where the row
 * was, not where it went. The pointer moving from a row onto the hover card a reference in that
 * row opened keeps that row the current one while the card is open: the card is the row's own
 * doing and sits outside the list only because it is `fixed`.
 *
 * One shift is not compensated: a row that a press of the reader's takes away from above the
 * current row (`leaving`: a Dismiss, or a snooze that moves its rows into another group). The
 * reader asked for that row to go, so the current row and the rows after it move up by the whole
 * space the row took, the part above the view's top included, and the view does not scroll. In
 * the commit in which such a row goes, what stands above the list can shrink because of the same
 * press - the Inbox's **Mark all read** goes with the last unread rows, and below `sm` the header
 * loses the line it stood on - and that shrink moves the rows up too, with no scroll. A shrink
 * above the list in a commit in which no such row goes is compensated, and so is content that
 * grows above the list in any commit. A current row that holds the focus and is left partly in
 * sight is then brought whole into sight at its nearest edge, and a focus left wholly out of
 * sight is not scrolled to: the selection bar's hand-off has put the focus on a row that will
 * stand in sight once those rows have gone (`useLastMarkedPlace`). The browser's own scroll
 * anchoring is turned off on the box that scrolls the rows in every commit in which a row above
 * the current one is named as leaving. The snapshot taken before the commit cannot tell whether
 * that row goes in it, and the browser would hold the view against that going. It is turned back
 * on as soon as this component has kept its place. A row that goes in the same commit without
 * the reader's press, by a live update or another device, is compensated as any other shift is,
 * so the rows in view do not jump under it.
 *
 * Rows are the elements matching `[item]`, identified by that attribute's value, and must keep
 * their identity across the commit (a remounted row is a new node and cannot be found again).
 * The document scrolls them unless the host names a `scroller`, in which case that element's
 * `scrollTop` is both what is read and what is moved, and the window's scroll is left alone.
 *
 * A class because `getSnapshotBeforeUpdate` has no hook equivalent, and the measurement must be
 * taken in the commit phase, immediately before React mutates the DOM: a render can complete and
 * then wait for its commit (React yields between the two for non-urgent updates), and anything
 * measured during that render is stale by the time the commit lands if the reader scrolled or a
 * click moved the page in between - the Conversation's Jump to latest was undone exactly this
 * way by a render-phase measurement. The same timing is why nothing cached from a `scroll`
 * handler can be trusted here: a scroll performed immediately before a live update may not have
 * dispatched its event before React commits. The one place that is cached (`held`) is therefore
 * used only while the box it was saved against still sits at the offset it was saved at, so
 * such a scroll drops it instead of being undone by it. `componentDidUpdate` then compensates
 * exactly once, before paint.
 */
export class ViewportAnchor extends Component<ViewportAnchorProps, object, Snapshot | null> {
  private root: HTMLElement | null = null;
  private hovered: HTMLElement | null = null;
  /** Set while the pointer left the hovered row for the hover card a reference in that row
   *  opened. The card is `fixed` and portaled into `document.body`
   *  (`features/refs/RefPreview.tsx`), so no later boundary event on this root says where the
   *  pointer went from there, and the row is held as the reader's place only for as long as
   *  that card is open - once the pointer leaves it the card closes itself
   *  (`REF_PREVIEW_CLOSE_DELAY_MS`) and `interacted` lets the row go. */
  private onOwnCard = false;
  /** The row the reader's hand is on, the viewport top they last saw it at, and the two things
   *  that keep that top true: the offset the box was at when it was saved, and the group the
   *  row was in. The top is read rather than re-measured because the view can be wrong when a
   *  commit arrives: a change this component's own commits never see has already moved the
   *  row. A card below the hand answering itself, a thread body arriving, Markdown finishing -
   *  each is a child's own commit, which re-renders the child alone, so
   *  `getSnapshotBeforeUpdate` is never called for it. Measured in the Inbox drawer: the
   *  answered row's card shrinks by a card's height one commit before the list drops the row,
   *  and the reader's row ended exactly that far out.
   *
   *  Kept only where the host names a `scroller`. Where the rows scroll in the document the
   *  browser's own scroll anchoring covers that drift, and a cached place there would only undo
   *  a scroll whose event has not arrived yet; in a named box the browser does not cover it,
   *  which is what `contentResized` stands in for.
   *
   *  A different `scrollTop` means the view moved without this component hearing of it, and a
   *  different `group` means the row was relocated rather than shifted. On either the top no
   *  longer says where the reader's place is, and the commit measures the row instead. */
  private held: { group: string | null; id: string; scrollTop: number; top: number } | null = null;
  private contentSize: ResizeObserver | null = null;
  /** Set by a scroll this component made, so the scroll event it raises is not read as the
   *  reader moving the view. Scroll events run before resize observations in a frame, so
   *  without this the compensation's own event would record the place it just corrected. */
  private selfScrolled = false;

  /** Where the pointer last was, in viewport pixels. A row moving under a pointer that has not
   *  moved makes the browser dispatch `pointerout`/`pointerover` again, with the same
   *  coordinates: that is the list moving under the hand, which is the thing being compensated
   *  for, so it must not be read as the reader looking somewhere new. */
  private pointerAt: { x: number; y: number } | null = null;
  /** The box whose own scroll anchoring is turned off for a press's commit, and the inline
   *  `overflow-anchor` it had before (`holdNativeAnchoring`). Null while it is on. */
  private nativeAnchoring: { box: HTMLElement; priority: string; value: string } | null = null;
  /** The frame request that gives the box its scroll anchoring back once a rendering update has
   *  run with it off (`releaseAfterRendering`). Null while none is pending. */
  private releaseFrame: number | null = null;

  /** The box the host named, while it holds one: what this anchor reads its offset from and
   *  what it moves. Null where the rows scroll in the document, which is the window's own
   *  scroll and the browser's own anchoring. */
  private scrollerElement(): HTMLElement | null {
    return this.props.scroller?.current ?? null;
  }

  /** What the reader can see of the rows: the box the host named, cut to the window, else the
   *  window. */
  private view(): View {
    const box = this.scrollerElement()?.getBoundingClientRect();
    return {
      bottom: Math.min(window.innerHeight, box?.bottom ?? window.innerHeight),
      top: Math.max(0, box?.top ?? 0),
    };
  }

  /** Turns the browser's own scroll anchoring off on the box that scrolls the rows for the
   *  commit in flight: the box the host named, else the document's root, whose value the window
   *  takes. The browser would hold the view against the going of the rows a press of the reader's
   *  takes away, which is the shift this component lets happen. The value is read back at once,
   *  so it is in force before React removes the rows. A hold still pending from an earlier press
   *  keeps the value the box had before it. `releaseAfterRendering` turns it back on. */
  private holdNativeAnchoring(): void {
    this.cancelRelease();
    const box = this.scrollerElement() ?? document.documentElement;
    if (this.nativeAnchoring?.box !== box) {
      this.releaseNativeAnchoring();
      const { style } = box;
      this.nativeAnchoring = {
        box,
        priority: style.getPropertyPriority("overflow-anchor"),
        value: style.getPropertyValue("overflow-anchor"),
      };
    }
    box.style.setProperty("overflow-anchor", "none");
    void getComputedStyle(box).overflowAnchor;
  }

  /** Gives the box its scroll anchoring back once a rendering update has run with it off: two
   *  frames on, since a frame's callbacks run before that frame's layout. WebKit adjusts for the
   *  shift a commit made at a layout after it rather than in it, so anchoring turned back on in
   *  the commit's own task let it scroll the window by the content that left above the anchor
   *  (a Dismiss that takes Mark all read out of the Inbox's control row at a phone's width
   *  scrolled 52 px), a move this component had already let happen. */
  private releaseAfterRendering(): void {
    if (this.nativeAnchoring === null) return;
    this.cancelRelease();
    this.releaseFrame = requestAnimationFrame(() => {
      this.releaseFrame = requestAnimationFrame(() => {
        this.releaseFrame = null;
        this.releaseNativeAnchoring();
      });
    });
  }

  private cancelRelease(): void {
    if (this.releaseFrame === null) return;
    cancelAnimationFrame(this.releaseFrame);
    this.releaseFrame = null;
  }

  /** Gives the box back the scroll anchoring `holdNativeAnchoring` turned off, with the inline
   *  value it had, at once. */
  private releaseNativeAnchoring(): void {
    this.cancelRelease();
    const held = this.nativeAnchoring;
    if (held === null) return;
    this.nativeAnchoring = null;
    if (held.value === "") {
      held.box.style.removeProperty("overflow-anchor");
    } else {
      held.box.style.setProperty("overflow-anchor", held.value, held.priority);
    }
  }

  /** The group `row` is in, null where the host names none. A kept place belongs to one: a row
   *  that is in another group was relocated rather than shifted, and the place no longer says
   *  where the reader was looking. */
  private groupOf(row: Element): string | null {
    const { group } = this.props;
    return group === undefined ? null : row.getAttribute(group);
  }

  /** Records where the hand's row sits now as the place to keep it, with the box offset and the
   *  group that place belongs to. Called where the view is the reader's own: the pointer moving
   *  onto a row or off one, and a scroll they made. A host whose rows scroll in the document
   *  keeps nothing. */
  private readonly rememberHeld = (): void => {
    const scroller = this.scrollerElement();
    const current = scroller === null ? null : this.interacted();
    if (scroller === null || current === null) {
      this.held = null;
      return;
    }
    this.held = {
      group: this.groupOf(current.row),
      id: current.id,
      scrollTop: scroller.scrollTop,
      top: current.row.getBoundingClientRect().top,
    };
  };

  /** The kept top for `id`, while it is still the place the reader saw: the box the host named
   *  has not moved since, and the row is still in the group it was saved in. Null otherwise,
   *  and the caller measures the row. */
  private heldTop(id: string, group: string | null): number | null {
    const held = this.held;
    const scroller = this.scrollerElement();
    if (held === null || scroller === null) return null;
    const holds = held.id === id && held.group === group && held.scrollTop === scroller.scrollTop;
    return holds ? held.top : null;
  }

  /** Called once a compensation has put `id` at `to` from the place it was kept at, `from`. `to`
   *  is `from` save where rows a press of the reader's took away let the row move up
   *  (`leaving`). The row is at its kept place again, so that place belongs to the offset the
   *  compensation left behind. Without this the first compensation would invalidate the kept
   *  place for every later one, and the next commit would put a row the press let move up back
   *  where it stood before. */
  private restampHeld(id: string, from: number, to: number): void {
    const held = this.held;
    const scroller = this.scrollerElement();
    if (held === null || scroller === null || held.id !== id || held.top !== from) return;
    held.scrollTop = scroller.scrollTop;
    held.top = to;
  }

  /** True when this pointer event is the hand moving rather than the list moving under it. */
  private handMoved(event: PointerEvent): boolean {
    const at = this.pointerAt;
    this.pointerAt = { x: event.clientX, y: event.clientY };
    return at === null || at.x !== event.clientX || at.y !== event.clientY;
  }

  /** Moves the box this anchor owns and remembers that it did. */
  private scrollBy(delta: number): void {
    const scroller = this.scrollerElement();
    if (scroller === null) {
      const before = window.scrollY;
      window.scrollBy(0, delta);
      this.selfScrolled = window.scrollY !== before;
      return;
    }
    const before = scroller.scrollTop;
    scroller.scrollTop += delta;
    this.selfScrolled = scroller.scrollTop !== before;
  }

  private readonly viewScrolled = (event: Event): void => {
    const scroller = this.scrollerElement();
    const mine = scroller === null ? event.target === document : event.target === scroller;
    if (!mine) return;
    if (this.selfScrolled) {
      this.selfScrolled = false;
      return;
    }
    this.rememberHeld();
  };

  /** The root's content changed height outside a commit of this component's. Puts the hand's
   *  row back where the reader last saw it, in the box the host named; with no such box the
   *  document's own scroll anchoring owns this and nothing here runs. The kept place is used
   *  under the same conditions a commit uses it under, the row's group included. */
  private readonly contentResized = (): void => {
    const held = this.held;
    const root = this.root;
    // No box check of its own: nothing is ever held without one (`rememberHeld`), and `heldTop`
    // below answers null where the host names none.
    if (held === null || root === null) return;
    const element = this.rowById(root, held.id);
    if (element === null) {
      this.held = null;
      return;
    }
    const top = this.heldTop(held.id, this.groupOf(element));
    if (top === null) return;
    const delta = element.getBoundingClientRect().top - top;
    if (delta === 0) return;
    this.scrollBy(delta);
    this.restampHeld(held.id, top, top);
  };

  componentDidMount(): void {
    // Capture, because `scroll` does not bubble: one listener catches the window's and the
    // named box's alike, and the handler reads a rect only for the box this anchor owns.
    window.addEventListener("scroll", this.viewScrolled, true);
  }

  componentWillUnmount(): void {
    window.removeEventListener("scroll", this.viewScrolled, true);
    this.contentSize?.disconnect();
    this.contentSize = null;
    this.releaseNativeAnchoring();
  }

  // Both listeners hang off the rendered root, not `document`: `pointerover`/`pointerout` bubble,
  // so a pointer entering a row still reaches the root, and leaving a row for anything outside
  // still fires `pointerout` on the row being left with the same `relatedTarget`.
  private readonly pointerOver = (event: PointerEvent): void => {
    const root = this.root;
    if (root === null || !(event.target instanceof Element)) {
      return;
    }
    this.hovered = rowWithin(root, `[${this.props.item}]`, event.target);
    this.onOwnCard = false;
    if (this.handMoved(event)) this.rememberHeld();
  };

  // A pointer moving from a row onto the card that row's own reference opened has left nothing
  // the reader can see leaving: the card is the row's doing, drawn outside the list only
  // because it is `fixed`. Releasing the row there is what moved the row under the reader when
  // a refetch landed with the pointer on the card.
  private readonly pointerOut = (event: PointerEvent): void => {
    const row = this.hovered;
    if (
      row === null ||
      (event.relatedTarget instanceof Node && row.contains(event.relatedTarget))
    ) {
      return;
    }
    if (refPreviewCardContains(event.relatedTarget) && refPreviewOpenedIn(row)) {
      this.onOwnCard = true;
      return;
    }
    this.hovered = null;
    this.onOwnCard = false;
    if (this.handMoved(event)) this.rememberHeld();
  };

  private readonly setRoot = (node: HTMLElement | null): void => {
    if (this.root !== null) {
      this.root.removeEventListener("pointerover", this.pointerOver);
      this.root.removeEventListener("pointerout", this.pointerOut);
    }
    this.contentSize?.disconnect();
    this.contentSize = null;
    this.root = node;
    if (node !== null) {
      node.addEventListener("pointerover", this.pointerOver);
      node.addEventListener("pointerout", this.pointerOut);
      // Not every environment the unit tests run in has one (happy-dom); there the hold is the
      // commit path alone, which is what those tests exercise.
      if (typeof ResizeObserver !== "undefined") {
        this.contentSize = new ResizeObserver(this.contentResized);
        this.contentSize.observe(node);
      }
    }
    const { rootRef } = this.props;
    if (rootRef !== undefined) rootRef.current = node;
  };

  /** The row the reader's hand is on: focus within a row, else the pointer resting on one. No
   *  layout reads, so a parent may call it on every render. */
  interacted(): (AnchorRow & { row: HTMLElement }) | null {
    const root = this.root;
    if (root === null) return null;
    const selector = `[${this.props.item}]`;
    const focused = rowWithin(root, selector, document.activeElement);
    const resting =
      this.hovered !== null && (!this.onOwnCard || refPreviewOpenedIn(this.hovered))
        ? this.hovered
        : null;
    const hovered = resting !== null && root.contains(resting) ? resting : null;
    const chosen =
      focused !== null
        ? { row: focused, via: "focus" as const }
        : hovered !== null
          ? { row: hovered, via: "pointer" as const }
          : null;
    return chosen === null ? null : { ...chosen, id: this.idOf(chosen.row) };
  }

  /** `interacted()`, else - only once the reader has scrolled - the row nearest the list's
   *  visible centre (a hit-test probe: layout reads). The row a commit will anchor to. The box
   *  that scrolls is `scroller`'s element while the host gave one and it holds one, else the
   *  window, and `componentDidUpdate` moves whichever this read. */
  current(): (AnchorRow & { row: HTMLElement }) | null {
    const interacted = this.interacted();
    if (interacted !== null) return interacted;
    const root = this.root;
    if (root === null) return null;
    const scroller = this.scrollerElement();
    if ((scroller === null ? window.scrollY : scroller.scrollTop) <= 0) return null;
    const row = rowAtCenter(root, `[${this.props.item}]`, scroller);
    return row === null ? null : { id: this.idOf(row), row, via: "center" };
  }

  private idOf(row: HTMLElement): string {
    const id = row.getAttribute(this.props.item);
    if (id === null) {
      throw new Error(`viewport anchor row is missing its ${this.props.item} value`);
    }
    return id;
  }

  /** The row of `root` whose `item` attribute is `id`, else null. */
  private rowById(root: HTMLElement, id: string): HTMLElement | null {
    return root.querySelector<HTMLElement>(`[${this.props.item}="${CSS.escape(id)}"]`);
  }

  /** The rows above `row` that the host names as leaving (`leaving`), in the render that last
   *  drew them (`previous`) or in this one, each with how far `row` moves up once it has gone:
   *  the whole space it takes (`spaceTaken`), the part above the view's top included. Layout
   *  reads for the rows named alone; none where the host names none. */
  private leavingAbove(row: HTMLElement, previous: Readonly<ViewportAnchorProps>): Leaving[] {
    const root = this.root;
    const { leaving } = this.props;
    if (root === null || (previous.leaving === undefined && leaving === undefined)) return [];
    const selector = `[${this.props.item}]`;
    const found: Leaving[] = [];
    for (const each of root.querySelectorAll<HTMLElement>(selector)) {
      if (each === row) break;
      const id = this.idOf(each);
      if (previous.leaving?.(id) !== true && leaving?.(id) !== true) continue;
      const lead = each.previousElementSibling;
      found.push({
        by: spaceTaken(each),
        id,
        lead:
          lead === null || lead.matches(selector) ? null : { by: spaceTaken(lead), element: lead },
      });
    }
    return found;
  }

  /** How far the rows `leaving` names let `row` move up, now the commit is in: each row no longer
   *  above `row` - gone from the list, or moved to a place after it - its own `by`, and the
   *  element before it its `by` too where that element went as well. */
  private freedAbove(root: HTMLElement, row: HTMLElement, leaving: readonly Leaving[]): number {
    let freed = 0;
    for (const { by, id, lead } of leaving) {
      const now = this.rowById(root, id);
      const above =
        now !== null && (row.compareDocumentPosition(now) & Node.DOCUMENT_POSITION_PRECEDING) !== 0;
      if (above) continue;
      freed += by;
      if (lead !== null && !lead.element.isConnected) freed += lead.by;
    }
    return freed;
  }

  /** Brings the control that holds the focus in `row` whole into sight at its nearest edge where
   *  the reader can see a part of it, which the page's sight rule allows for a landing partly in
   *  sight (`components/focus-place.ts`): an edge that only touches the view's edge counts as in
   *  sight. A control wholly in sight, or wholly out of it, is left where it is. */
  private showFocused(row: HTMLElement): void {
    const focused = document.activeElement;
    if (focused === null || !row.contains(focused)) return;
    const { bottom, top } = this.view();
    const box = focused.getBoundingClientRect();
    const meets = box.bottom >= top && box.top <= bottom;
    const whole = box.top >= top && box.bottom <= bottom;
    if (meets && !whole) focused.scrollIntoView({ block: "nearest" });
  }

  getSnapshotBeforeUpdate(previousProps: Readonly<ViewportAnchorProps>): Snapshot | null {
    const snapshot = this.measure(previousProps);
    // A commit no press of the reader's takes rows away in is one the browser's own anchoring
    // holds the view through, so a hold still pending from a press ends before it.
    if (snapshot === null || snapshot.leaving.length === 0) this.releaseNativeAnchoring();
    return snapshot;
  }

  /** The row the reader's hand is on and where it stands before the commit, with the rows a
   *  press of theirs takes away above it, the native anchoring held off where there are any. */
  private measure(previousProps: Readonly<ViewportAnchorProps>): Snapshot | null {
    const { enabled } = this.props;
    if (enabled !== undefined && !enabled()) return null;
    const current = this.current();
    if (current === null) return null;
    const at = this.groupOf(current.row);
    // Where the reader last saw the row they have their hand on, while that place still holds:
    // a change no commit of this component's saw may already have moved it (`held`), and
    // measuring again there would take that drift for the place to keep.
    const held = this.heldTop(current.id, at);
    const leaving = this.leavingAbove(current.row, previousProps);
    const top = held ?? current.row.getBoundingClientRect().top;
    let rootTop: number | null = null;
    if (leaving.length > 0) {
      rootTop = this.root?.getBoundingClientRect().top ?? null;
      this.holdNativeAnchoring();
    }
    return { group: at, id: current.id, leaving, rootTop, top, via: current.via };
  }

  componentDidUpdate(
    _previousProps: Readonly<ViewportAnchorProps>,
    _previousState: Readonly<object>,
    snapshot: Snapshot | null
  ): void {
    try {
      this.keepPlace(snapshot);
    } finally {
      this.releaseAfterRendering();
    }
  }

  /** Puts the row `snapshot` measured back at its top, less the space the rows a press of the
   *  reader's took away from above it freed, once the commit is in. Where such a row went in that
   *  commit, less what the list itself moved up in it too (`rootTop`): content above the list
   *  that shrank with the press. Where none went, a shrink above the list is no doing of the
   *  press's, and it is made up for as any other shift is. */
  private keepPlace(snapshot: Snapshot | null): void {
    const root = this.root;
    if (snapshot === null || root === null) return;
    const element = this.rowById(root, snapshot.id);
    if (element === null) return;
    const { group } = this.props;
    if (group !== undefined && element.getAttribute(group) !== snapshot.group) return;
    const left = this.freedAbove(root, element, snapshot.leaving);
    const shrunk =
      left === 0 || snapshot.rootTop === null
        ? 0
        : Math.max(0, snapshot.rootTop - root.getBoundingClientRect().top);
    const freed = left + shrunk;
    const top = snapshot.top - freed;
    const delta = element.getBoundingClientRect().top - top;
    if (delta === 0 && freed === 0) return;
    if (delta !== 0) this.scrollBy(delta);
    this.restampHeld(snapshot.id, snapshot.top, top);
    // The rows a press of the reader's took away moved this row up, and a focus in it must end
    // in the reader's sight.
    if (freed > 0 && snapshot.via === "focus") this.showFocused(element);
  }

  render(): ReactNode {
    const { as: Tag = "div", children, className, label } = this.props;
    return (
      <Tag aria-label={label} className={className} ref={this.setRoot}>
        {children}
      </Tag>
    );
  }
}
