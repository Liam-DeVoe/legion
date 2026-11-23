import { type FocusEvent, type RefObject, useEffect, useLayoutEffect, useRef } from "react";

import { REQUEST_DEADLINE_MS } from "../api/client";
import { commitsOnBlur, PANEL_ATTRIBUTE, takesText } from "../features/shell/keymap";
import { focusOnDocument } from "../features/shell/roving";
import { SEND_DEADLINE_MS } from "../features/writes/keyed-write";
import { isLive, type Writes } from "../features/writes/write-store";
import { CONTROL } from "./control-names";
import { ROW_SELECTOR } from "./row-marker";

/**
 * The page's focus hand-offs: `useFocusPlace`, a component's own hand-off for a control a press
 * takes out of it, and `useFocusRescue`, the page's one rescue, mounted once in the app shell,
 * for every loss of the focus no component's hand-off answered. A row is `row-marker.ts`'s one
 * rule, `ROW_SELECTOR`. A loss is the reader's while a press of theirs is still live on the lost
 * control, inside any row around it, or inside the dialog, panel or selection bar it was lost from
 * (`causedByReader`). A Tab that moves the focus inside the region a press was made in, while
 * that press's own write is out, leaves the press live, since the region's later going is that
 * press's own doing (`notePress`). A loss the reader caused lands on what their press drew in the
 * control's place - a form opened there, an edit mode - and never on another item shown in that
 * place, as the Inbox's reading column names the question it shows (`SHOWN_ITEM_ATTRIBUTE`); else
 * on the same control in the row that moved up into that place, else on that row's first control,
 * else on the control now at its place - one that moved there from the lost control's own row,
 * card or toolbar, and not one that only took its index in the Tab order (`atPlaceOf`) - or the
 * nearest one before it, else on the region's own stop. A
 * component's hand-off walks back the same way inside its own container, and then tries the
 * fallback the component names, else the region's own stop. A component's hand-off answers only
 * for its own controls: a focus in a dialog, a panel or a selection bar drawn inside its container
 * is that region's. A loss the reader caused there is answered inside a dialog or a panel by the
 * page's rescue, and in a selection bar by the bar's own hand-off (`SelectionBar`). The page's
 * rescue leaves a loss there that the reader did not cause as `leaveUncaused` says. Each walk
 * back passes over every control that takes typed text (`takesText`) and every control the
 * reader cannot see now (`inSightNow`). It lands a control with `land`, which scrolls a control
 * partly in sight to its nearest edge and one wholly in sight not at all. A region's own stop
 * takes the focus with no scroll (`focusOwnStop`). A loss the reader did not cause leaves the
 * focus on the body, as the page did before it had a rescue, and scrolls nothing, save the
 * exceptions `leaveUncaused` names. A loss of a control the reader had scrolled out of their
 * sight scrolls nothing and focuses no control (`outOfSight`): the focus stays on the body,
 * except in a dialog or a panel, which takes it itself, for a loss the reader caused, and in an
 * open modal dialog, whose own stop takes it, for one they did not (`leaveUnseen`).
 */

/** The form controls: the focus reaches one only while it is not `disabled` (`FOCUSABLE`), and a
 *  row holds one as a control either way (`ROW_CONTROL`). */
const FORM_FIELDS = ["button", "textarea", "input", "select"];

/** A link or a form control the page has not taken out of the Tab order: `tabindex="-1"` is a
 *  page's word that the keyboard does not stop there - a tab of a tab list other than the
 *  selected one, and a control of a row of the issue page's outline while that row holds no Tab
 *  stops (`tabStops`), since the keys move the focus between those rows. A hand-off that counted
 *  them would land where no key could have taken the reader: at 390 px in Chromium, "66 more rows
 *  below" on the outline drew its rows in its place, the rescue landed on the first one's "more ·
 *  106 words", and that row, holding the focus, opened its body and drew its words whole. */
const IN_TAB_ORDER = ':not([tabindex="-1"])';

/** An element given a Tab stop of its own: any `tabindex` but -1. */
const TAB_STOP = `[tabindex]${IN_TAB_ORDER}`;

/** What the keyboard focus can reach: a link, a form control that is not `disabled`, each left
 *  in the Tab order (`IN_TAB_ORDER`), or anything given a Tab stop of its own. A control refused
 *  with `aria-disabled` is one, since it keeps its Tab stop. */
const FOCUSABLE = [
  `a[href]${IN_TAB_ORDER}`,
  ...FORM_FIELDS.map((tag) => `${tag}:not(:disabled)${IN_TAB_ORDER}`),
  TAB_STOP,
].join(", ");

/** The controls the focus can reach inside `container`, in order. One under a `hidden` ancestor -
 *  a composer kept mounted, out of sight, for a send it has out - takes no focus, so it is never
 *  where a dialog's trap starts or wraps, nor where a hand-off lands. */
export function focusableIn(container: Element): HTMLElement[] {
  return [...container.querySelectorAll<HTMLElement>(FOCUSABLE)].filter(
    (element) => element.closest("[hidden]") === null
  );
}

/** A ref that focuses its element once, as it enters the page: React calls a ref callback again
 *  only when the callback itself is a new one, so this single function never pulls the focus back
 *  on a later render, after the reader has moved it on (AskCard's **Ask back instead**, the issue
 *  title's editor). */
export function focusOnMount(element: HTMLElement | null): void {
  element?.focus();
}

/** Where a hand-off sends the focus a control took away with it, nearest first: the control now
 *  at `index` in `controls` where it stands at the lost control's place (`atPlace`), then each
 *  control before that place, walking back. `index` is the lost control's place among `controls`
 *  as they stand now: how many of them come before the point it left. The page's rescue and a
 *  component's hand-off count the place of a control taken away off the spot it went from, once
 *  it has gone (`placeOfRemoval`; `Loss`'s `index`, `useFocusPlace`, `openerPlace`), and the
 *  rescue counts that of a control hidden where it still stands (`handOn`), so however many
 *  controls went or were hidden together, the walk starts where they were. */
function placesFrom(
  controls: readonly HTMLElement[],
  index: number,
  atPlace: (control: Element) => boolean
): HTMLElement[] {
  const before = controls.slice(0, Math.min(index, controls.length)).reverse();
  const at = controls[index];
  return at === undefined || !atPlace(at) ? before : [at, ...before];
}

/** Whether `element` stands in a row (`ROW_SELECTOR`) inside `within` other than `within` itself. */
function inRowWithin(element: Element, within: Element): boolean {
  const row = element.closest(ROW_SELECTOR);
  return row !== null && row !== within && within.contains(row);
}

/** The rule that tells whether a control now at a lost control's index in the order of `within`,
 *  its region or its container, stands at the lost control's place. Such a control moved into the
 *  place by the same change from inside the lost control's own row, card or toolbar, as Blocked on
 *  you moves into the place Mark all read leaves, and as a row's Expand drawn outside its card
 *  moves into the place the card's Answer leaves. `rows` are the rows the lost control stood in
 *  inside `within`, nearest first. Where it stood in one, the control must stand in one of them
 *  that is still in the page: rows nest, and an ask card in an Inbox row is the lost control's row
 *  as much as the `<li>` around it. Where it stood in none, the control must stand in no row of
 *  `within` either. A control that only takes the lost control's index in the Tab order, from
 *  another row or item, is at no place of its, so the walk goes back before the place and the lost
 *  control's own row or card comes first: Unsubscribe on an Inbox card's recipients lands on the
 *  card's Unfollow, not on the next question's own checkbox. A row that moved up into a taken
 *  row's place is offered apart from this walk (`controlsOfRowMovedUp`). */
function atPlaceOf(rows: readonly Element[], within: Element): (control: Element) => boolean {
  if (rows.length === 0) return (control) => !inRowWithin(control, within);
  const own = rows.filter((row) => row.isConnected && within.contains(row));
  return (control) => own.some((row) => row.contains(control));
}

/** `element`'s place among the controls the focus can reach in `container` (`focusableIn`'s
 *  order): how many of them come before it in the page, counted up to it and no further.
 *  `counted` says whether the element is one of them. One that is not - disabled, hidden, or
 *  given no Tab stop of its own - is placed where the control after it is counted. */
function placeIn(container: Element, element: Element): { index: number; counted: boolean } {
  let index = 0;
  for (const control of container.querySelectorAll(FOCUSABLE)) {
    if (control.closest("[hidden]") !== null) continue;
    if (control === element) return { counted: true, index };
    if (!(control.compareDocumentPosition(element) & Node.DOCUMENT_POSITION_FOLLOWING)) break;
    index += 1;
  }
  return { counted: false, index };
}

/** Where the keyboard focus sat: the element, the container it is counted in, its place among
 *  that container's focusable controls, the rows it stood in inside that container, nearest
 *  first (`atPlaceOf`), and where it stood in that container (`spotsOf`): the place and the spots
 *  are read as it takes the focus and again after each commit while it holds it. */
interface Place {
  readonly container: Element;
  readonly element: Element;
  index: number;
  readonly rows: readonly Element[];
  spots: readonly Spot[];
}

/**
 * Keeps the keyboard focus off the page's body when a press takes away the element holding it
 * while the component around it stays - a page control that removes itself, a menu's trigger
 * that goes once its value is picked. The focus goes to the control now at that element's place
 * in its container where that control moved there from the element's own row, or from no row
 * where the element stood in none (`atPlaceOf`), else to the nearest one before that place,
 * passing over every control that takes typed text, as the rescue's walk back does (`walkBack`):
 * the reader never chose such a field, and their next key would be typed into it. Where no other
 * control stands there, the focus goes to what `fallback` names for that container (a table row's
 * fallback is the row itself, and a row that has left the page names none).
 *
 * The hand-off keeps the rescue's sight rule as well. It passes over each of those controls, the
 * fallback included, that the reader cannot see now (`inSightNow`), and lands the first one in
 * sight that takes the focus with `land`. A control partly in sight is scrolled to its nearest
 * edge, the least distance that shows it whole. A control wholly in sight scrolls nothing. The
 * browser's own scroll of a focused control would pull the page back to one out of sight: the
 * Delivery page's **clear brush window**, with its Window select scrolled out above the window,
 * scrolled the page 46 to 147 px back to that select. Where no control in sight takes the focus,
 * the focus goes with no scroll to the own stop (`focusOwnStop`) of the region the container is
 * in (`handOffRegionOf`): the dialog or panel holding it, else the shell's main content. Where
 * that region is not in the page and drawn, nothing moves. Nothing moves either where neither the
 * walk nor `fallback` names any control, and the page's rescue answers that loss.
 *
 * The component puts the handler this returns on its root as `onFocus`, which every focus inside
 * it bubbles to, from a portal too, since React bubbles through its own tree. `containerOf` names
 * the container a focused element is counted in, or null for one outside every container, whose
 * going moves nothing. A focus in a dialog, a panel or a selection bar drawn inside the container
 * (`inRegionWithin`) is no focus on a control of the component either, so its going moves nothing
 * here. In a dialog or a panel, the page's rescue answers a loss the reader caused inside that
 * region, by a walk back among the region's own controls in sight, else on the region's own stop
 * with no scroll (`handOn`), and never by a walk out of the region to the component's controls.
 * In a selection bar, the bar's own hand-off (`SelectionBar`) answers a loss the reader caused.
 * The page's rescue leaves a loss there that the reader did not cause as `leaveUncaused` says. An
 * element React bubbled up from a portal is no control of the container, so it is counted at the
 * place of the one counted before it there, in that one's rows and spots, and not at all without
 * one: a nested panel's input is counted where the trigger that opened that panel is.
 *
 * The place is counted again once the element has gone, off the spot it went from in the
 * container (`placeOfRemoval`): its place counted while it stood there takes in every control
 * before it that went with it, as an Inbox card's subscriber chip takes its name link away with
 * its Unsubscribe, and the walk would start at the control after them. Where no spot of it went
 * from a parent still in the container, the place counted while it stood there is used.
 *
 * The check runs after every commit of the component, so after the one that took the element
 * away, and moves the focus only when the element has left the page and the focus is on the
 * body. An element still in the page that no longer holds the focus is one the reader moved off,
 * so it is forgotten, and its going later moves nothing. Nor does the going of an element the
 * reader had scrolled out of their sight (`outOfSight`), the page's rescue's rule read from the
 * rescue's own record: a live update taking a control out of a row drawn beyond the view, or
 * Mark all read going while the reader is scrolled away, pulls neither a list nor the page back
 * to the control at its place. Nor does a going the reader did not cause (`causedByReader`): the
 * hand-off moves the focus to a row only for a loss the reader caused, and the page's rescue
 * leaves an uncaused one on the body, or on an open modal dialog's own stop, with no scroll.
 */
export function useFocusPlace(
  containerOf: (element: Element) => Element | null,
  fallback: (container: Element) => HTMLElement | null
): (event: FocusEvent) => void {
  const place = useRef<Place | null>(null);
  useLayoutEffect(() => {
    const held = place.current;
    if (held === null) return;
    if (held.element === document.activeElement) {
      // Read again from the page this commit left, so a control drawn or taken away before it
      // moves its place with it. A portal's element keeps the place it was handed.
      if (held.container.contains(held.element)) {
        const { counted, index } = placeIn(held.container, held.element);
        if (counted) held.index = index;
        held.spots = spotsOf(held.element, held.container);
      }
      return;
    }
    place.current = null;
    if (held.element.isConnected || !focusOnDocument() || outOfSight(held.element)) return;
    if (!causedByReader(held.element)) return;
    let walked: HTMLElement[] = [];
    if (held.container.isConnected) {
      const controls = focusableIn(held.container);
      const spot = held.spots.find((each) => goneFrom(each, held.container));
      const index = spot === undefined ? held.index : placeOfRemoval(controls, spot);
      walked = walkBack(controls, index, atPlaceOf(held.rows, held.container));
    }
    const fallbackControl = fallback(held.container);
    const candidates = fallbackControl === null ? walked : [...walked, fallbackControl];
    if (candidates.length === 0) return;
    // One read of each box's style for the whole hand-off (`clippingBoxes`).
    const clips = new Map<Element, boolean>();
    for (const candidate of candidates) {
      if (inSightNow(candidate, clips) && land(candidate)) return;
    }
    const region = handOffRegionOf(held.container);
    if (region !== null) focusOwnStop(region);
  });
  return (event) => {
    const element = event.target;
    const container = containerOf(element);
    if (container === null || inRegionWithin(container, element)) {
      place.current = null;
      return;
    }
    const before = place.current;
    const carried = before?.container === container ? before : null;
    if (!container.contains(element)) {
      // A portal's element: at the place of the control counted before it, or nowhere.
      place.current =
        carried === null
          ? null
          : { container, element, index: carried.index, rows: carried.rows, spots: carried.spots };
      return;
    }
    const { counted, index } = placeIn(container, element);
    place.current = {
      container,
      element,
      index: counted ? index : (carried?.index ?? -1),
      rows: rowsAround(element).filter((row) => row !== container && container.contains(row)),
      spots: spotsOf(element, container),
    };
  };
}

/** A dialog, modal or not - the search bar, the Inbox drawer, every `useDialog` dialog, and the
 *  panel a `PanelTrigger` opens - or any other panel an `AnchoredPanel` draws, which carries the
 *  keymap's `PANEL_ATTRIBUTE`: a `MultiSelect` list's, which has no role of its own. Each is a
 *  region of its own, and a press inside it is a press in it. Once it has gone with the element
 *  the press landed on, the press reads as one on the control it was opened from as well
 *  (`pressedOn`), since the focus goes back to its opener when it goes. */
const DIALOG = `[role="dialog"], [${PANEL_ATTRIBUTE}]`;

/** An open modal dialog: a `<dialog>` shown modally (`:modal`, the picture lightbox), or an
 *  element named a dialog that says it is modal (`aria-modal="true"`) - every `useDialog` dialog
 *  that does, the Inbox drawer, the phone's navigation drawer, and the issue page's margin and
 *  contents while the phone draws them as sheets. The page behind one is not the reader's to
 *  reach, so a loss the reader did not cause leaves the focus on its own stop rather than on the
 *  body (`leaveUncaused`). */
const MODAL = 'dialog:modal, [role="dialog"][aria-modal="true"]';

/** Whether `element` is a row of a table or list or inside one: whether it or an element around
 *  it is a row by `row-marker.ts`'s one rule, `ROW_SELECTOR`: a press on a row's mark is read by
 *  it (`pressOnRowMark`). */
function inRow(element: Element): boolean {
  return element.closest(ROW_SELECTOR) !== null;
}

/** Every row around `element` by `row-marker.ts`'s one rule (`ROW_SELECTOR`), itself included,
 *  nearest first: rows nest, as an ask card's `<article>` in an Inbox row's `<li>`, and a loss
 *  is read against each of them (`pressPlaces`, `rowPlacesOf`). */
function rowsAround(element: Element): Element[] {
  const rows: Element[] = [];
  for (
    let row = element.closest(ROW_SELECTOR);
    row !== null;
    row = row.parentElement?.closest(ROW_SELECTOR) ?? null
  ) {
    rows.push(row);
  }
  return rows;
}

/** The one checkbox a row draws for its mark (the project List's `Select <key>`, the Inbox's
 *  mark): the reader empties a selection from a row by it, from the bar itself otherwise. */
const MARK = 'input[type="checkbox"]';

/** The regions of their own a focus can sit in: a dialog or a panel (`DIALOG`), or the selection
 *  bar. A press that empties a selection could land in one and still be the reader's own, and a
 *  focus in one drawn inside a component's container is that region's (`inRegionWithin`). */
const PRESS_REGION = `${DIALOG}, [data-control="${CONTROL.selectionBar}"]`;

/** Whether the page draws `element`: under no `hidden` ancestor, and given a box. */
function rendered(element: Element): boolean {
  return element.closest("[hidden]") === null && element.getClientRects().length > 0;
}

/** The region a control is counted in: the dialog or panel holding it (`DIALOG`), modal or not,
 *  else the shell's main content (`main`), else the document. */
function regionOf(element: Element, main: HTMLElement | null): Element {
  return element.closest(DIALOG) ?? (main?.contains(element) ? main : document.body);
}

/** The controls the focus can reach in `region` that the page draws and that are the region's
 *  own - none of a dialog inside it, and, for the document, none of the main content's either -
 *  since a loss is answered inside the region it was lost in. The rescue counts places among
 *  these and lands only on these, so a control drawn at one width alone - `md:hidden`, say -
 *  neither holds a place nor is handed a focus it cannot show. */
function drawnIn(region: Element, main: HTMLElement | null): HTMLElement[] {
  return focusableIn(region).filter(
    (control) => regionOf(control, main) === region && rendered(control)
  );
}

/** Whether `region` is still a place the focus can be answered in: in the page and drawn. A
 *  dialog kept mounted under `hidden` - the phone thread holding a reply - has gone as much as
 *  one taken away. */
function present(region: Element): boolean {
  return region.isConnected && rendered(region);
}

/** The region whose own stop takes the focus where a component's hand-off finds no control in
 *  sight to land on (`useFocusPlace`): the dialog or panel holding `container` (`DIALOG`), as
 *  `regionOf` reads it, else the shell's main content - the `main` element around `container`,
 *  else the page's - while that region is in the page and drawn (`present`), else null. */
function handOffRegionOf(container: Element): HTMLElement | null {
  const region =
    container.closest(DIALOG) ?? container.closest("main") ?? document.querySelector("main");
  return region instanceof HTMLElement && present(region) ? region : null;
}

/** Whether `element` sits in a region of its own (`PRESS_REGION`) that `container` holds and is
 *  not: a dialog, a panel or a selection bar a page draws inside a component's container, as the
 *  Delivery page draws its Session groups dialog in its control row's slot. Such a focus is the
 *  region's, so a component's hand-off notes it nowhere (`useFocusPlace`). A region around
 *  `container`, as the search bar's dialog is around its control row, is none of these. */
function inRegionWithin(container: Element, element: Element): boolean {
  const region = element.closest(PRESS_REGION);
  return region !== null && region !== container && container.contains(region);
}

/** Whether `element` stands as a control the reader can use: not disabled, and drawn. A focus
 *  lost from one that does not was taken from the reader (`taken`); the reader leaves one that
 *  still does (`left`). */
function stands(element: Element): boolean {
  return !element.matches(":disabled") && rendered(element);
}

/** How many of `controls` come before `node` in the page. */
function countBefore(controls: readonly Element[], node: Node): number {
  let index = 0;
  for (const control of controls) {
    if (
      control === node ||
      !(control.compareDocumentPosition(node) & Node.DOCUMENT_POSITION_FOLLOWING)
    ) {
      break;
    }
    index += 1;
  }
  return index;
}

/** Whether the keyboard focus is lost: on the body, or still on an element that has left the
 *  page. Chromium, Firefox and WebKit, as Playwright runs them, name the body at once when the
 *  focused element or a parent of it is removed (measured: read at once, in a microtask, a task and
 *  a frame); an element hidden or disabled under the focus stays named until the task after the
 *  next frame. The element that has left the page is checked as well, so an engine that names it
 *  until its focus fixup is read the same way. The one check for a removed focus: the rescue below
 *  asks it, and so do a list closing over its own text box (`MultiSelect`) and the Inbox's hand-off
 *  of a focus its reading column's question took with it (`Inbox.tsx`). */
export function focusLost(): boolean {
  const focused = document.activeElement;
  return focused === null || focused === document.body || !focused.isConnected;
}

/** Focuses `element` with no scroll of the engine's own, and, once it holds the focus, scrolls it
 *  to its nearest edge where the reader can see a part of it but not the whole: the least distance
 *  that shows it whole. A control wholly in sight, or wholly out of it, scrolls nothing. The
 *  engine's own scroll of a focused control follows the engine's own rule: Chromium and Firefox
 *  centre a control that is not wholly in view, and Firefox moved a control half cut by a dialog's
 *  top edge 405 px. WebKit scrolls a focused control only a frame later, by when a list's own
 *  scroll keeping may have moved the page again. So every engine lands a control the same way.
 *  Answers whether it took the focus. */
function land(element: HTMLElement): boolean {
  element.focus({ preventScroll: true });
  if (document.activeElement !== element) return false;
  const clips = new Map<Element, boolean>();
  if (inSightNow(element, clips) && !inSightWhole(element, clips)) {
    element.scrollIntoView({ block: "nearest", inline: "nearest" });
  }
  return true;
}

/** A row that will move up the page by `by` pixels once the rows a press is still taking away
 *  from above it have gone: an element inside it is read where it will stand then
 *  (`SelectionBar`'s hand-off). */
export interface MovedUp {
  readonly by: number;
  readonly row: Element;
}

/** The box of `element`: where it will stand once `moved`'s row has moved up, where the element
 *  is inside that row, else where it stands now. */
function boxOf(
  element: Element,
  moved: MovedUp | undefined
): { bottom: number; left: number; right: number; top: number } {
  const box = element.getBoundingClientRect();
  const by = moved?.row.contains(element) ? moved.by : 0;
  return { bottom: box.bottom - by, left: box.left, right: box.right, top: box.top - by };
}

/** What is in the reader's sight through `boxes`, the boxes around an element that can hide it
 *  (`Sight`): the viewport cut to each of them, a box inside `moved`'s row read where it will
 *  stand. */
function sightOf(
  boxes: Iterable<Element>,
  moved: MovedUp | undefined
): { bottom: number; left: number; right: number; top: number } {
  let top = 0;
  let left = 0;
  let bottom = window.innerHeight;
  let right = window.innerWidth;
  for (const box of boxes) {
    const edges = boxOf(box, moved);
    top = Math.max(top, edges.top);
    left = Math.max(left, edges.left);
    bottom = Math.min(bottom, edges.bottom);
    right = Math.min(right, edges.right);
  }
  return { bottom, left, right, top };
}

/** Whether any part of `element` is in the reader's sight: its box meets the viewport and every
 *  box in `boxes`, the boxes around it that can hide it (`Sight`). An element whose edge only
 *  touches the edge of what is in sight counts as in sight. With `moved`, the element is read
 *  where it will stand once that row has moved up. */
function inSight(element: Element, boxes: Iterable<Element>, moved?: MovedUp): boolean {
  const sight = sightOf(boxes, moved);
  const drawn = boxOf(element, moved);
  return (
    drawn.bottom >= sight.top &&
    drawn.top <= sight.bottom &&
    drawn.right >= sight.left &&
    drawn.left <= sight.right
  );
}

/** The overflow values that clip what overflows a box. */
const CLIPPING_OVERFLOW: Record<string, true> = {
  auto: true,
  clip: true,
  hidden: true,
  scroll: true,
};

/** The boxes around `element` that clip what overflows them, nearest first: every ancestor whose
 *  overflow clips (`CLIPPING_OVERFLOW`), short of the body and the root, whose overflow is the
 *  viewport's. `clips` keeps whether each ancestor read clips, so a walk that measures many
 *  controls of one list reads each box's style once. */
function clippingBoxes(element: Element, clips?: Map<Element, boolean>): Element[] {
  const boxes: Element[] = [];
  for (
    let box = element.parentElement;
    box !== null && box !== document.body && box !== document.documentElement;
    box = box.parentElement
  ) {
    let clipping = clips?.get(box);
    if (clipping === undefined) {
      const { overflowX, overflowY } = getComputedStyle(box);
      clipping = CLIPPING_OVERFLOW[overflowX] === true || CLIPPING_OVERFLOW[overflowY] === true;
      clips?.set(box, clipping);
    }
    if (clipping) boxes.push(box);
  }
  return boxes;
}

/** The element the focus was last noted on, and whether the reader can still see it: read off
 *  every scroll that can move it - the page's, or a box's around it, which `boxes` keeps - and
 *  every resize of the window, against every box around it that clips it, which `boxes` keeps
 *  from then on, while it is in the page and drawn. The rescue reads it when it answers a loss
 *  of that element, which may have gone by then, so it holds what the last read before that saw. */
interface Sight {
  readonly boxes: Set<Element>;
  readonly element: HTMLElement;
  seen: boolean;
  /** Whether a read since the element took the focus has found it in sight: the read at the
   *  focus itself, or one at a scroll or a resize after it (`readSight`). */
  shown: boolean;
}

/** Reads again whether the reader can see `watched`'s element, against the boxes it keeps. One
 *  out of sight now is out of the reader's sight only once a read since it took the focus has
 *  found it in sight: until then no scroll took it from them. A control focused below the fold
 *  is one the engine is still to scroll into view - WebKit does so a frame after the focus - and
 *  a scroll the page makes first can leave it there. Measured in WebKit at 390x664: the Inbox's
 *  list keeping its rows in place (`ViewportAnchor`) scrolled the page 203 px before the engine's
 *  own scroll, which had not come by the Enter on the thread's Dismiss just focused, and that
 *  press's loss was read as one the reader had scrolled out of sight, and left on the body. */
function readSight(watched: Sight): void {
  const now = inSight(watched.element, watched.boxes);
  watched.shown ||= now;
  watched.seen = now || !watched.shown;
}

/** The page's one record of what the reader can see of the element the focus was last noted on,
 *  which the page's rescue keeps (`useFocusRescue`), mounted once in the app shell: null while no
 *  rescue is mounted. Two component hand-offs read it too (`useFocusPlace`, `SelectionBar`), so
 *  one record decides for them and for the rescue. */
let sight: Sight | null = null;

/** Whether the reader had scrolled `element` out of their sight, as the page's rescue last read
 *  it: false for an element the rescue has not noted, and wherever no rescue is mounted. The
 *  rescue asks this of a loss's element before it answers the loss, and the two component
 *  hand-offs that read it (`useFocusPlace`, `SelectionBar`) ask it before they move the focus, so
 *  none of them pulls a list or the page back to a place the reader scrolled away from. */
export function outOfSight(element: Element): boolean {
  return sight?.element === element && !sight.seen;
}

/** Whether a focus rescue is mounted, so the page is recording the reader's presses. Where none
 *  is - a component read in a unit test without the shell - every loss keeps the base hand-off,
 *  as it did before this rule. */
let rescueMounted = false;

/** A control that cannot be pressed now: held while its write is out (`aria-disabled`,
 *  `held-press.ts`), or disabled, itself or through the fieldset around it. */
const HELD = '[aria-disabled="true"], :disabled';

/** The reader's latest press (`isPress`) - a pointer press, a key that acts rather than edits a
 *  field's text, or a pick - which tells a loss the reader caused from one an update made under
 *  them: where it landed, the dialog or panel it landed in (`DIALOG`) and the loss that region was
 *  opened from (`openedFrom`), the elements it can read as (`pressedOn`) that were already held
 *  when it was made (`heldBefore`, `heldAt`), its own release while that is still to come
 *  (`Release`; none for a pick's `change` or `input`), its action - the writes it started and the
 *  holds still on over what answers them (`writes`, `answers`) - whether it can still be the cause
 *  of a loss (`live`, `answerPress`), and what stops the timers and the watch that end it. */
interface Press {
  /** The holds begun while the press is live that are still on (`beginAnswerHold`). */
  readonly answers: Set<object>;
  /** Reads again whether anything still keeps the press live (`keepLive`), at each change of the
   *  page's writes and of `answers`, and, for a press that started no write and that a hold on
   *  what answers it or a control it holds keeps live, of the page (`watchHold`), while that watch
   *  is on: up to the write deadline, and only while no hold on what answers the press is on.
   *  Outside `keepLive` it does nothing. */
  changed: () => void;
  readonly heldBefore: ReadonlySet<Element>;
  live: boolean;
  /** The page's writes that were live as the press was made (`isLive`), by id: none of them is
   *  the press's. */
  readonly liveBefore: ReadonlySet<number>;
  /** The region the press was made in: the dialog, panel or selection bar around the control it
   *  landed on (`PRESS_REGION`), else the shell's main content while that holds it, else null. A
   *  Tab that moves the focus inside it while the press's own write is out leaves the press as
   *  the cause of a later loss there (`notePress`). */
  readonly madeIn: Element | null;
  readonly opener: Loss | null;
  readonly region: Element | null;
  release: Release | null;
  /** Whether a write that goes live now is the press's (`noteWrites`): from the press, and again
   *  from its release, until a frame and a task after it (`answerPress`). */
  starting: boolean;
  stop: () => void;
  readonly target: Element;
  /** The writes the press started, by id (`noteWrites`). */
  readonly writes: Set<number>;
}

/** The release a press waits for: the event that releases it (`pointerup`, `keyup`) from the
 *  pointer or the key that made the press (`by`, `pressedBy`), so that another pointer or key let
 *  go while the press is down - Space let go on a control Enter is still pressing - is not taken
 *  for it. */
interface Release {
  readonly by: number | string | null;
  readonly type: "keyup" | "pointerup";
}

/** The reader's latest press, kept by the page's rescue while it is mounted, else null. */
let lastPress: Press | null = null;

/** The event that releases a press made by each event the rescue notes a press by. */
const RELEASE: Readonly<Record<string, "keyup" | "pointerup">> = {
  keydown: "keyup",
  pointerdown: "pointerup",
};

/** What made `event`: its pointer (`pointerId`), or its key (`code`), else null. */
function pressedBy(event: Event): number | string | null {
  if (event instanceof PointerEvent) return event.pointerId;
  return event instanceof KeyboardEvent ? event.code : null;
}

/** Whether `event` is a press of the reader's (`Press`), the act that can make a loss in its row,
 *  dialog or panel theirs (`causedByReader`). A pointer press is one, and so is any key on a
 *  control that takes no typed text - Enter, Space, Escape, the arrows, a page key. In a field
 *  that takes typed text (`takesText`) a character typed is none, whatever it is typed with, and
 *  only a key that acts on the page rather than on the text is one: Escape, Tab, Enter in a
 *  single-line `<input>`, which commits it, as the issue title's editor does, and a key held
 *  with Ctrl, Meta or Alt that types no character (`typesCharacter`), a shortcut such as
 *  Ctrl+Enter or Meta+Enter that sends. A character or editing key there - a printable key, one
 *  held with AltGr or Option, Space, Backspace, Delete, the arrows, Home and End, Enter in a
 *  `<textarea>` or a content-editable element, which inserts a line - only edits the field. A
 *  `change` or an `input` is one on every control but a field whose value is typed text alone
 *  (`commitsOnBlur`), where a `change` is the field's commit on blur, which Chromium fires as an
 *  update takes the focused field away, and an `input` is typing: elsewhere each comes at the
 *  reader's pick, which Playwright and assistive tech make without a key or a pointer. Firefox
 *  fires `input` alone as the digit that completes a date is typed, and a value set without keys
 *  fires `input` and then a `change` the page may already have taken the control away from at that
 *  `input`. Why a walk-back passes over a date or time box all the same is `takesText`'s. */
function isPress(event: Event): event is Event & { readonly target: Element } {
  const { target } = event;
  if (!(target instanceof Element)) return false;
  if (event.type === "change" || event.type === "input") return !commitsOnBlur(target);
  if (!(event instanceof KeyboardEvent) || !takesText(target)) return true;
  const { key } = event;
  return (
    key === "Escape" ||
    key === "Tab" ||
    (key === "Enter" && target instanceof HTMLInputElement) ||
    ((event.ctrlKey || event.metaKey || event.altKey) && !typesCharacter(event))
  );
}

/** Whether `event`, a key held with Ctrl, Meta or Alt, types a character all the same. It does
 *  while AltGr is held (`AltGraph`), which Windows reports as Ctrl and Alt together and Linux on
 *  its own, as a German layout types `@` with AltGr and Q; and while Alt is held without Ctrl or
 *  Meta on a key that is one character, as macOS's Option types `@`, `[`, `{` or `|`. */
function typesCharacter(event: KeyboardEvent): boolean {
  return (
    event.getModifierState("AltGraph") ||
    (event.altKey && !event.ctrlKey && !event.metaKey && event.key.length === 1)
  );
}

/** The nearest of `opener` and the openers before it whose region is still in the page and drawn
 *  (`present`), else null: where a focus lost from a region that has gone goes back to. */
function drawnOpener(opener: Loss | null): Loss | null {
  let from = opener;
  while (from !== null && !present(from.region)) from = from.opener;
  return from;
}

/** The control `element` is held through while it is in the page: the nearest of itself and the
 *  elements around it that cannot be pressed now (`HELD`) - the button a span or an icon of it is
 *  drawn in - else null. */
function heldControl(element: Element): Element | null {
  return element.isConnected ? element.closest(HELD) : null;
}

/** Whether `element` is in the page and cannot be pressed now (`HELD`). */
function isHeld(element: Element): boolean {
  return heldControl(element) !== null;
}

/** The elements a press on `target` can read as (`pressedOn`) - `target`, and the control each
 *  region around it was opened from, `opener` and each opener before it - that are held now
 *  (`isHeld`). Read as the press is made, before what it does: a control held then takes the
 *  press without a write, so its hold is no doing of the press (`watchHold`). */
function heldAt(target: Element, opener: Loss | null): ReadonlySet<Element> {
  const held = new Set<Element>();
  if (isHeld(target)) held.add(target);
  for (let from = opener; from !== null; from = from.opener) {
    if (isHeld(from.element)) held.add(from.element);
  }
  return held;
}

/** The elements `press` reads as now: the one it landed on, and - once that element has left the
 *  page with the dialog or panel it landed in (`present`) - the control that region was opened
 *  from too, else the nearest opener before it whose region is still drawn (`drawnOpener`). A
 *  list's pick or a dialog's confirm takes its region away and hands the focus back to the
 *  trigger that opened it; a write the press made that takes that trigger, or the row around it,
 *  away only once the server answers is still the press's doing while the trigger is held. The
 *  element it landed on still counts, for a loss inside the region that went with it. A press in
 *  no dialog or panel, or in one opened from the body, is read as it landed. */
function pressedOn(press: Press): Element[] {
  const { opener, region, target } = press;
  if (target.isConnected || region === null || present(region)) return [target];
  const from = drawnOpener(opener);
  return from === null ? [target] : [target, from.element];
}

/** Whether a press on `target` lands on a control `press` holds: one `press` reads as
 *  (`pressedOn`), held now by a hold that began after `press` (not in `heldBefore`), with `target`
 *  that control or inside it. The controls are compared, not the nodes the presses landed on, so
 *  a press on a span or an icon of the button counts, and so does a press on the button after
 *  one on a span of it. */
function onHeldControl(press: Press, target: Element): boolean {
  return pressedOn(press).some(
    (each) => !press.heldBefore.has(each) && heldControl(each)?.contains(target) === true
  );
}

/** The page's writes (`Writes`), as the page's rescue was handed them (`useFocusRescue`), else
 *  null: a press's action is the writes it starts among them (`noteWrites`). */
let pageWrites: Writes | null = null;

/** The ids of the page's writes that are live now (`isLive`): waiting their turn, or out, within
 *  their deadline or past it. One that landed, was refused or was discarded is not. */
function liveWriteIds(): Set<number> {
  const ids = new Set<number>();
  for (const write of pageWrites?.rows() ?? []) {
    if (isLive(write)) ids.add(write.id);
  }
  return ids;
}

/** Whether a write `press` started is still live among the page's writes (`liveWriteIds`). */
function writeOut(press: Press): boolean {
  const live = liveWriteIds();
  for (const id of press.writes) {
    if (live.has(id)) return true;
  }
  return false;
}

/** Run at every change of the page's writes: notes into the reader's latest press, while a write
 *  that goes live is its own (`starting`), each write live now that was not as it was made
 *  (`liveBefore`), and has the press read its action again (`changed`). */
function noteWrites(): void {
  const press = lastPress;
  if (press === null) return;
  if (press.starting) {
    for (const id of liveWriteIds()) {
      if (!press.liveBefore.has(id)) press.writes.add(id);
    }
  }
  press.changed();
}

/**
 * Notes a hold on a control that is part of what answers a write: the control held while the
 * write is out and through the commit after its answer (`useHeldThroughAnswer`), through the
 * re-read that draws what the write changed (`useReadBound`), until the update that carries the
 * write is drawn (`useHoldUntilDrawn`), or through the whole run of a project List action, its
 * later writes and the read of the list after them included, until its controls are let go
 * (`issues-table-actions.ts`'s `perform`). A hold begun while the reader's press is live
 * (`recentPress`) is part of that press's action, which keeps the press live while the hold is
 * on, up to `ACTION_BOUND_MS` from its release (`keepLive`): the row or the form that what
 * answers the write takes away is still the press's doing (`causedByReader`). A hold begun while
 * no press is live is no press's. Answers what ends the hold; ending it again does nothing.
 */
export function beginAnswerHold(): () => void {
  const press = recentPress();
  if (press === null) return () => undefined;
  const hold = {};
  press.answers.add(hold);
  press.changed();
  return () => {
    if (press.answers.delete(hold)) press.changed();
  };
}

/** The longest a press's action keeps it live, counted from its release: the app's write
 *  deadline (`SEND_DEADLINE_MS`), the longest a press's write keeps it live, and then the request
 *  deadline (`REQUEST_DEADLINE_MS`), which bounds the hold through the re-read that answers the
 *  write (`useReadBound`). It is the longest a write that lands within its deadline, and then its
 *  re-read, can take. */
const ACTION_BOUND_MS = SEND_DEADLINE_MS + REQUEST_DEADLINE_MS;

/**
 * Keeps `press` live while what answers it is still to run: a press is the cause only of the loss
 * of its own control and of what its own action takes away. It is live from the press, and again
 * from its own release, which a click answers (`noteRelease`), until a frame and a task later
 * (`afterComponents`), by when React has committed what the press did and run its effects. Every
 * write that goes live among the page's writes in that time is the press's (`starting`,
 * `noteWrites`). From then on it stays live while a hold on what answers it (`beginAnswerHold`)
 * is on, and, up to the app's write deadline, while a write it started is still live - for a
 * press that started one - or else while a control it reads as is held by a hold that began after
 * it (`keepLive`, `watchHold`). Once its action is answered, a control the press reads as that is
 * held keeps a press that started a write live no longer: a control held because there is nothing
 * to do - Rename once the name is the group's, Create group with no name, a Save with nothing
 * changed - is no part of the action, so an update that takes it away after that is a loss the
 * reader did not cause. Two bounds, counted from the release, end the watch: at the app's write
 * deadline (`SEND_DEADLINE_MS`) a write still out or a control still held keeps the press live no
 * longer, and at `ACTION_BOUND_MS` - the write deadline and then the request deadline
 * (`REQUEST_DEADLINE_MS`) - the watch ends whatever is still on. Where the rescue was handed no
 * writes, no press starts one.
 */
function answerPress(press: Press): void {
  press.stop();
  press.live = true;
  press.starting = true;
  press.stop = afterComponents(() => {
    press.starting = false;
    if (press.writes.size > 0) keepLive(press, () => writeOut(press));
    else watchHold(press);
  });
}

/**
 * Keeps `press` live while a hold on what answers it, begun while it is live, is on (`answers`,
 * `beginAnswerHold`), at any time, and while `kept` answers true - a write the press started still
 * live, or a control it holds - up to the app's write deadline (`SEND_DEADLINE_MS`) alone. The
 * press ends a frame and a task after the last of these ends (`afterComponents`), so the commit
 * that draws the answer and its effects are still the press's. A hold, a write's turn or a
 * control's hold that begins again in that time keeps the press live. Both bounds are counted
 * from the press's release - from the press itself for a pick's `change` or `input`, which has
 * none. At the write deadline `kept` stops counting and `unwatch` runs, while a hold on what
 * answers the press still counts: a write that landed within its deadline is answered by the
 * re-read or the draw after it, whose hold has a deadline of its own (`useReadBound`'s,
 * `REQUEST_DEADLINE_MS` from the read's start) and can end after the write deadline. At
 * `ACTION_BOUND_MS` the watch ends whatever is still on, so a hold that never ends keeps no press
 * live past it, and the press ends a frame and a task later. `press.changed` reads again whether
 * anything still keeps the press live. `watch` starts and `unwatch` stops whatever has `kept` read
 * again as the page changes (the page watch `watchHold` hands in). It runs only while `kept` alone
 * can keep the press live: before the write deadline, with no hold on what answers the press on.
 * A hold that begins or ends reads again at once (`beginAnswerHold`), against the page as it
 * stands then, so a change of the page made while the watch was off is read all the same.
 */
function keepLive(
  press: Press,
  kept: () => boolean,
  watch: () => void = () => undefined,
  unwatch: () => void = () => undefined
): void {
  /** What cancels the press's end, from the moment nothing keeps it live until the end runs. */
  let ending: (() => void) | null = null;
  /** Whether the write deadline has passed: from then on `kept` keeps the press live no longer. */
  let late = false;
  const stop = () => {
    press.changed = () => undefined;
    unwatch();
    window.clearTimeout(deadline);
    window.clearTimeout(bound);
    ending?.();
    ending = null;
  };
  const read = () => {
    if (press.answers.size > 0) unwatch();
    else if (!late) watch();
    if (press.answers.size > 0 || (!late && kept())) {
      ending?.();
      ending = null;
      return;
    }
    ending ??= afterComponents(() => {
      stop();
      press.live = false;
    });
  };
  const deadline = window.setTimeout(() => {
    late = true;
    unwatch();
    read();
  }, SEND_DEADLINE_MS);
  const bound = window.setTimeout(() => {
    stop();
    endAfterComponents(press);
  }, ACTION_BOUND_MS);
  press.changed = read;
  press.stop = stop;
  read();
}

/**
 * Keeps `press`, which started no write, live while a hold on what answers it is on, as a press
 * that started a write is: a press whose action starts its first write only a frame and a task or
 * more after it notes no write (`starting`), as the List's merge reads every issue before it moves
 * any, and the holds through that action and the re-read after it still answer the press. It is
 * live too, up to the app's write deadline (`SEND_DEADLINE_MS`), while a control it reads as
 * (`pressedOn`) is held (`HELD`) by any hold that began after the press - what the press did to
 * its own control, as a pager's Next is held on the last page it led to. It ends a frame and a
 * task after the last of these ends (`keepLive`): the hold ended, the control taken away, or its
 * `aria-disabled` or `disabled` gone. With neither on a frame and a task after its release, the
 * press ends then. A control already held when the press was made (`heldBefore`) - the pager's
 * Next on its last page, a select or a trigger held while an earlier write is out - took the press
 * without a write, so its hold keeps the press live no longer than a press on a control held by
 * nothing. The control's hold is watched rather than polled, up to the write deadline alone and
 * only while no hold on what answers the press is on (`keepLive`'s `watch`), so a control that
 * stays held keeps no watch over the page past the deadline, and the watch ends at
 * `ACTION_BOUND_MS` from the release at the latest. A press past its watch is no cause, so an
 * update after it - a live update taking the row away, a refetch emptying the selection bar - is
 * a loss the reader did not cause.
 */
function watchHold(press: Press): void {
  // Once the press's target and its region have left the page, which they never come back to,
  // what the press reads as is kept rather than walked again at every change the watch sees.
  let kept: Element[] | null = null;
  const pressed = (): Element[] => {
    if (kept !== null) return kept;
    const now = pressedOn(press);
    const { region, target } = press;
    if (!target.isConnected && (region === null || !region.isConnected)) kept = now;
    return now;
  };
  const held = () => pressed().some((each) => !press.heldBefore.has(each) && isHeld(each));
  if (press.answers.size === 0 && !held()) {
    press.live = false;
    return;
  }
  // The whole document is watched, so that the control going with a parent of it, a fieldset
  // around it enabled again, or the region the press landed in going, ends the hold too.
  const watcher = new MutationObserver(() => press.changed());
  keepLive(
    press,
    held,
    () =>
      watcher.observe(document.body, {
        attributeFilter: ["aria-disabled", "disabled"],
        childList: true,
        subtree: true,
      }),
    () => watcher.disconnect()
  );
}

/** Ends `press` a frame and a task from now (`afterComponents`), unless `press.stop` cancels the
 *  end first. */
function endAfterComponents(press: Press): void {
  press.stop = afterComponents(() => {
    press.live = false;
  });
}

/** The reader's latest press while it can still be the cause of a loss (`answerPress`). */
function recentPress(): Press | null {
  return lastPress?.live ? lastPress : null;
}

/** The element the focus was last noted on by the page's rescue (`useFocusRescue`), with the
 *  dialog, panel or selection bar around it then (`PRESS_REGION`), read as it took the focus,
 *  while it was in the page; null while no rescue is mounted. */
let noted: { readonly element: Element; readonly region: Element | null } | null = null;

/** The dialog, panel or selection bar (`PRESS_REGION`) `element`'s focus was lost from: the one
 *  around it while it is in the page, and for one that has left the page the one the rescue
 *  noted around it as it took the focus (`noted`). Chromium's `focusout` reads a control taken
 *  away while it is still in the page; Firefox and WebKit fire none, so their loss is read once it
 *  has gone, where the region around it has gone from it unless it went with it. Read from the
 *  note, one loss is read in the same region in every engine, and so is the going a component's
 *  hand-off reads after its commit (`useFocusPlace`, `SelectionBar`). */
function pressRegionOf(element: Element): Element | null {
  if (!element.isConnected && noted?.element === element) return noted.region;
  return element.closest(PRESS_REGION);
}

/** The places a press could have taken `element`'s focus from and still be the reader's own doing:
 *  the element itself, every row around it (`rowsAround`, by the rule `inRow` reads), and the
 *  dialog, panel or selection bar it was lost from (`pressRegionOf`). Every row counts, not the
 *  nearest alone: a press in an Inbox row outside the ask card it holds is a press in that row, so
 *  a loss inside the card is the reader's own. A press in a list's panel is in that panel, so a
 *  pick that takes the panel away is the reader's own, and the focus goes back to the trigger
 *  that opened it. */
function pressPlaces(element: Element): Element[] {
  const places: Element[] = [element, ...rowsAround(element)];
  const region = pressRegionOf(element);
  if (region !== null) places.push(region);
  return places;
}

/**
 * Whether the reader caused the loss of `element`'s focus: their latest press (`isPress`) - a
 * pointer press, a key that acts rather than edits a field's text, or a pick - is still live, what
 * answers it not yet run (`answerPress`), and landed on the element, inside any row it was in, or
 * inside the dialog, panel or selection bar it was lost from (`pressPlaces`), read at every element
 * the press reads as now (`pressedOn`): a pick in a list, a confirm in a dialog, that has gone
 * counts as a press on the trigger the list or the dialog was opened from as well. A loss the
 * reader caused keeps the hand-off that pulls the focus to the control now at the lost one's place;
 * one they did not - a live update taking a row away, a refetch emptying a bar - leaves the focus
 * on the body, or on an open modal dialog's own stop, and scrolls nothing (`leaveUncaused`). The
 * rescue and every component hand-off that can move the focus to another row read this one rule;
 * a hand-off to the lost control's own row, as AskCard's is, is no move to another row.
 */
export function causedByReader(element: Element): boolean {
  if (!rescueMounted) return true;
  const press = recentPress();
  if (press === null) return false;
  const pressed = pressedOn(press);
  return pressPlaces(element).some((place) =>
    pressed.some((each) => place === each || place.contains(each))
  );
}

/** Whether the reader's latest press, while it is still live (`answerPress`), was on a row's mark,
 *  read at every element the press reads as now (`pressedOn`): the selection bar reads this beside
 *  `causedByReader`, since unticking the last row by its mark empties the bar while the focus may
 *  rest in it (a checkbox a click leaves unfocused, as WebKit does). */
export function pressOnRowMark(): boolean {
  const press = recentPress();
  return press !== null && pressedOn(press).some((each) => inRow(each) && each.matches(MARK));
}

/** The open modal dialog (`MODAL`) a loss in `region` is answered in, else null: of the ones in
 *  the page and drawn (`present`), the innermost that holds `region`, the region the loss was in -
 *  the last of those in the page's order, since a modal nested in another, as a `DeclineDialog`
 *  in the Inbox drawer's, comes after it - else the last in the page's order, drawn over the ones
 *  before it. */
function openModal(region: Element): HTMLElement | null {
  const open = [...document.querySelectorAll<HTMLElement>(MODAL)].filter(present);
  return open.filter((modal) => modal.contains(region)).at(-1) ?? open.at(-1) ?? null;
}

/** Focuses `dialog`, an open modal dialog, as its own stop, with no scroll. One with no Tab stop
 *  of its own is given `tabindex="-1"` for the focus and has it taken off again once the focus
 *  leaves, so the engine's own Tab order is left as it was. */
function landOnDialog(dialog: HTMLElement): void {
  const needsStop = !dialog.hasAttribute("tabindex");
  if (needsStop) dialog.tabIndex = -1;
  const took = focusNoScroll(dialog);
  if (!needsStop) return;
  if (took) {
    dialog.addEventListener("blur", () => dialog.removeAttribute("tabindex"), { once: true });
  } else {
    dialog.removeAttribute("tabindex");
  }
}

/** Answers a loss the reader did not cause (`causedByReader`), lost in `region`, as the page did
 *  before it had a rescue: the focus stays on the body, where the loss left it, and nothing
 *  scrolls. Whatever control stands near the lost place, the page chose it and the reader did
 *  not, and their next key would act on it or be typed into it. One exception is an open modal
 *  dialog (`openModal`), which takes the focus itself as its own stop (`landOnDialog`), since a
 *  focus that leaves an open modal reaches the page behind it. The other is the control that held
 *  the focus: it gets the focus back, with no scroll, on three conditions. First, it comes back:
 *  disabled and enabled again, however long it stayed disabled (`useFocusRescue`'s `wait`), or
 *  hidden and drawn again before the rescue's first check (`check`). Second, the focus is still
 *  on the body when it comes back. Third, the reader has pressed nothing since the loss: any
 *  pointer press, and any key that is a press of theirs (`isPress`) - every key on the body,
 *  where the focus waits - ends that check or wait (`readerMoved`, `noteKey`), so `j` or `k` on
 *  the page leaves the focus there. This rule governs the rescue only where no component hands
 *  the focus off. A component that hands a lost focus to its own region's stop may do so whoever
 *  caused the loss: AskCard to its own row or the reading column's stop (`handOffFocus`), the
 *  Inbox's reading column to its stop (`Inbox.tsx`). */
function leaveUncaused(region: Element): void {
  const modal = openModal(region);
  if (modal !== null) landOnDialog(modal);
}

/** Answers a loss the reader can no longer see, in a region still in the page: nothing scrolls,
 *  and the focus goes to no control the reader did not pick. For a loss the reader caused it
 *  stays where it is, on the body, except in a dialog or a panel (`DIALOG`), which takes the
 *  focus itself so that the page behind a modal one is never reached - given `tabindex="-1"` for
 *  it where it has no Tab stop of its own, as when it loses every control. A loss they did not
 *  cause is answered as every such loss is (`leaveUncaused`): on the body, or on an open modal
 *  dialog's own stop, and never on a panel that is no modal. */
function leaveUnseen(loss: Loss): void {
  const { caused, region } = loss;
  if (!caused) {
    leaveUncaused(region);
    return;
  }
  if (!(region instanceof HTMLElement) || !region.matches(DIALOG)) return;
  focusOwnStop(region);
}

/** Where a node stood in the page while it was there: the parent holding it and the siblings
 *  either side of it, `previous` and `next`, null at either end. */
interface Spot {
  readonly next: Node | null;
  readonly node: Node;
  readonly parent: Node;
  readonly previous: Node | null;
}

/** Where `element` stands in `within` - a loss's region, or a component's container - and where
 *  each parent of it up to `within`'s own children stands, nearest first: read while the element
 *  is still in the page, so that the spot a press later takes it, or a parent of it, away from is
 *  known (`controlsInPlace`, and the place counted off it in `useFocusPlace`), and so are the
 *  spots of the rows around it (`controlsOfRowMovedUp`). */
function spotsOf(element: Element, within: Element): Spot[] {
  const spots: Spot[] = [];
  let node: Node = element;
  for (let parent = node.parentNode; parent !== null && node !== within; parent = node.parentNode) {
    spots.push({ next: node.nextSibling, node, parent, previous: node.previousSibling });
    node = parent;
  }
  return spots;
}

/** The rows (`ROW_SELECTOR`) the element `spots` were read for stood in, nearest first: those of
 *  the spots' own nodes that are rows. Read from the spots rather than from the page, since a
 *  control taken away on its own has no row around it by the time its loss is answered, while
 *  the spots hold the parent chain as it stood while it was still there (`atPlaceOf`). */
function rowsIn(spots: readonly Spot[]): Element[] {
  const rows: Element[] = [];
  for (const { node } of spots) {
    if (node instanceof Element && node.matches(ROW_SELECTOR)) rows.push(node);
  }
  return rows;
}

/** Whether `spot`'s node has left the page while the parent it stood in is still in `within`: a
 *  loss's region, or a component's container (`useFocusPlace`). */
function goneFrom(spot: Spot, within: Element): boolean {
  return !spot.node.isConnected && spot.parent.isConnected && within.contains(spot.parent);
}

/** What a row holds as its controls, by markup alone: what the focus can reach (`FOCUSABLE`), and
 *  a button or form field disabled now as well, since a press can disable its own control before
 *  its row goes - the loss is noted at the disable's `focusout` then - and that control keeps its
 *  place in the row. */
const ROW_CONTROL = [
  ...["a[href]", ...FORM_FIELDS].map((tag) => `${tag}${IN_TAB_ORDER}`),
  TAB_STOP,
].join(", ");

/** The controls `row` holds, in order, by markup alone (`ROW_CONTROL`): the row itself first
 *  where it is one, then each inside it. Counted by markup and not by what the page draws, so a
 *  row counts the same whether it is still in the page, as at Chromium's `focusout`, or has left
 *  it, as when Firefox and WebKit report its removal alone, where nothing is drawn. */
function rowControls(row: Element): Element[] {
  const inside = [...row.querySelectorAll(ROW_CONTROL)];
  return row.matches(ROW_CONTROL) ? [row, ...inside] : inside;
}

/** Where a control stood in a row around it: the row - an element around it, itself included,
 *  that `ROW_SELECTOR` names, inside the control's region - and the control's place among that
 *  row's controls (`rowControls`), -1 where it is none of them. */
interface RowPlace {
  readonly index: number;
  readonly row: Element;
}

/** Where `element` stands in each row around it inside `region` (`RowPlace`), nearest first;
 *  none where no row inside the region holds it. Every row is kept, not the nearest alone, since
 *  rows nest and a press can take an outer row away with the inner one in it: an Inbox row with
 *  the ask card it holds. Read as its loss is noted, while the rows still hold it: in the page,
 *  at Chromium's `focusout`, or off the removal Firefox and WebKit report alone, once a row has
 *  left the page with the element still in it, when the rows around it are that row and those
 *  inside it. The element has gone by the time the loss is answered, so its places are kept. */
function rowPlacesOf(element: Element, region: Element): RowPlace[] {
  const places: RowPlace[] = [];
  for (const row of rowsAround(element)) {
    // A row around the region holds every control of it, and so does each row around that one.
    if (row.contains(region)) break;
    places.push({ index: rowControls(row).indexOf(element), row });
  }
  return places;
}

/** A focus an element lost: the element, the region it was counted in, its place there among
 *  the controls the page draws, the path of the page it was focused on, whether the page took it
 *  from the reader (`taken`: the element was disabled, hidden or taken away) rather than the
 *  reader moving off it, whether the reader left it (`left`), and - in a dialog - the loss the
 *  dialog was opened from (`opener`): the place the focus left for it, or null for a dialog the
 *  focus entered from the body. A loss kept only as a dialog's opener holds no arrivals
 *  (`UNPLACED`), but its item, its spots and its row places: the hand-off reads its element,
 *  region, place, item, path, opener, spots and row places. A loss the reader did not cause holds
 *  none of these and no item, nor a place (-1): it is answered without any of them
 *  (`leaveUncaused`). */
interface Loss {
  /** The elements the page added from the moment the element went until the loss is answered,
   *  noted into it while its check or its wait is out - none the page only moved, which stood in
   *  the page before (`noteArrivals`): what can now stand where it stood (`standingAt`). Null where
   *  nothing reads them (`UNPLACED`). */
  readonly arrived: Set<Node> | null;
  readonly caused: boolean;
  readonly element: HTMLElement;
  /** The element's place among the controls its region draws: how many of them come before the
   *  point it left. A control taken away is counted off the removal that took it, once it has
   *  gone (`placeOfRemoval`): off that removal alone where it went with no `focusout`, as in
   *  Firefox and WebKit, and again off it for a loss noted at a `focusout` while the control still
   *  stood in the page - Chromium's as a commit takes it away, or any engine's as it is disabled
   *  or hidden under the focus - where the removal comes while the loss's check or wait is out
   *  (the page's watcher in `useFocusRescue`). A control hidden rather than taken away is counted
   *  where it still stands as its loss is answered (`handOn`). A loss kept as a dialog's opener
   *  keeps the place counted as the focus left it for the dialog, and the walk back from it counts
   *  the place again (`openerPlace`). */
  index: number;
  /** The item the element was in (`itemOf`), read as the loss is noted where the element is still
   *  in it, as its row places are, else the one it was in as it took the focus; null where it was
   *  in none and where nothing reads it (`UNPLACED`): what tells another item shown where the
   *  element stood from what the reader's press drew there (`controlsInPlace`), and the one item
   *  whose controls a walk back from the element's place may land on, beside controls in no item
   *  (`handOn`). */
  readonly item: string | null;
  /** Whether the reader left the element (`record`): the focus went to nothing from it while it
   *  was enabled and drawn, and it still stood in the page so once the event that moved the focus
   *  had run. Set in a microtask after the loss, long before its check, which then does nothing. */
  left: boolean;
  readonly opener: Loss | null;
  readonly path: string;
  readonly region: Element;
  /** Where the element stood in each row around it, nearest first, read as the loss is noted
   *  (`rowPlacesOf`), and none where it stood in no row: each row, and the element's place among
   *  its controls, by which the row that moves up into the place of the row that left offers the
   *  focus (`controlsOfRowMovedUp`). */
  readonly rowPlaces: readonly RowPlace[];
  /** Where the element stood, and where each parent of it up to the region stood, nearest first
   *  (`controlsInPlace`, `controlsOfRowMovedUp`): read at its `focusout` while it is still in the
   *  page, or, for an element taken away with no `focusout`, off the last record that took it
   *  (`removalOf`), ahead of the spots read when it took the focus. They are also where the rows
   *  the element stood in are read from (`rowsIn`, `atPlaceOf`), since they hold the parent chain
   *  as it stood while the element was still in the page: a control taken away on its own has no
   *  row around it by the time its loss is answered. */
  readonly spots: readonly Spot[];
  readonly taken: boolean;
}

/** What a loss holds where nothing reads where its element stood: no arrivals, no item and no
 *  row places (`Loss`). A loss kept only as a dialog's opener takes its own item, spots and row
 *  places beside these: the spots say the rows it stood in (`rowsIn`) and the spot it went from
 *  (`openerPlace`), and with the row places they say which row moved up into the place of a row
 *  it was taken away with (`controlsOfRowMovedUp`). */
const UNPLACED: Pick<Loss, "arrived" | "item" | "rowPlaces" | "spots"> = {
  arrived: null,
  item: null,
  rowPlaces: [],
  spots: [],
};

/** The longest the rescue waits for an animation frame before it goes on without one. WebKit can
 *  hold a page's frames back while nothing on it is drawn - measured in headless WebKit, a check
 *  waited 775 ms for its frame, and another had none in 2 s while the page's timers ran - and a
 *  keyboard reader is not to sit on the body for that. */
export const FRAME_WAIT_MS = 100;

/** Runs `then` once the components have had their turn with a loss of focus: a frame, or
 *  `FRAME_WAIT_MS` where no frame comes, and then a task later. Answers what cancels it.
 *
 *  That is after the commit a press of the reader's makes and after its effects: React commits
 *  an update made in an event of theirs before the next paint and runs its passive effects at
 *  the end of that commit. A commit at normal priority - one an update outside any event makes,
 *  as a write's answer or a read's own bound does - runs its passive effects in a task of
 *  React's scheduler, which can come after this. A component whose hand-off must run in such a
 *  commit commits it synchronously, so the hand-off runs with it: the project List's bar goes
 *  that way (`features/project/issues-table-actions.ts`). */
function afterComponents(then: () => void): () => void {
  let task: number | undefined;
  const go = () => {
    if (task !== undefined) return;
    window.cancelAnimationFrame(frame);
    window.clearTimeout(late);
    task = window.setTimeout(then, 0);
  };
  const frame = window.requestAnimationFrame(go);
  const late = window.setTimeout(go, FRAME_WAIT_MS);
  return () => {
    window.cancelAnimationFrame(frame);
    window.clearTimeout(late);
    window.clearTimeout(task);
  };
}

/** A removal `records` tell of: where the node the record took stood (`spot`), and which of the
 *  records took the lost element out of the page (`at`), so that what the page added from there on
 *  can be told from what it held before. */
interface Removal {
  readonly at: number;
  readonly spot: Spot;
}

/** The last of `records` that took `node` away, itself or with a parent: its index (`at`), the
 *  record, and the node it removed that holds `node` (`taken`). The last, since a node the batch
 *  moved - a removal and an addition of it - and then took away left the page from the place the
 *  last record names, not the one the move took it from. */
function recordTaking(
  records: readonly MutationRecord[],
  node: Node
): { at: number; record: MutationRecord; taken: Node } | undefined {
  let found: { at: number; record: MutationRecord; taken: Node } | undefined;
  for (const [at, record] of records.entries()) {
    for (const taken of record.removedNodes) {
      if (taken.contains(node)) found = { at, record, taken };
    }
  }
  return found;
}

/** The removal in `records` that took `element` away, itself or with a parent, read off the last
 *  record that took it (`recordTaking`) at a point still in the page: where the parent it was
 *  taken from went too, later in the same batch, the last record that took that parent. */
function removalOf(records: readonly MutationRecord[], element: Element): Removal | undefined {
  let node: Node = element;
  let removal: Removal | undefined;
  for (;;) {
    const found = recordTaking(records, node);
    if (found === undefined) return removal;
    const { at, record, taken } = found;
    removal = {
      at: removal?.at ?? at,
      spot: {
        next: record.nextSibling,
        node: taken,
        parent: record.target,
        previous: record.previousSibling,
      },
    };
    if (record.target.isConnected) return removal;
    node = record.target;
  }
}

/** The place of a control taken away from `spot`, itself or with a parent, among `controls`: how
 *  many of them come before the point it was taken from - before the sibling after it while that
 *  is still in the page, else up to and through the sibling before it, else through the parent it
 *  was taken from. */
function placeOfRemoval(controls: readonly Element[], spot: Spot): number {
  const next = spot.next?.isConnected ? spot.next : null;
  const through = spot.previous?.isConnected ? spot.previous : spot.parent;
  let index = 0;
  for (const control of controls) {
    const before =
      next === null
        ? through.contains(control) ||
          control.compareDocumentPosition(through) & Node.DOCUMENT_POSITION_FOLLOWING
        : control.compareDocumentPosition(next) & Node.DOCUMENT_POSITION_FOLLOWING;
    if (!before) break;
    index += 1;
  }
  return index;
}

/** What now stands at `spot`: the elements its parent holds between the siblings its node stood
 *  between that the page added since the loss (`arrived`). A sibling no longer in that parent
 *  bounds nothing, so the parent's start or end bounds that side instead; only what arrived
 *  counts, so an element that stood in the parent before the loss is never taken for what stands
 *  there, and a block taken away with nothing drawn in its place leaves nothing standing. */
function standingAt(spot: Spot, arrived: ReadonlySet<Node>): Element[] {
  const { parent } = spot;
  const previous = spot.previous?.parentNode === parent ? spot.previous : null;
  const next = spot.next?.parentNode === parent ? spot.next : null;
  const standing: Element[] = [];
  for (
    let node = previous === null ? parent.firstChild : previous.nextSibling;
    node !== null && node !== next;
    node = node.nextSibling
  ) {
    if (node instanceof Element && arrived.has(node)) standing.push(node);
  }
  return standing;
}

/** The first `<form>` among `elements`, itself one of them or inside one, else null. */
function formIn(elements: readonly Element[]): Element | null {
  for (const element of elements) {
    const form = element.matches("form") ? element : element.querySelector("form");
    if (form !== null) return form;
  }
  return null;
}

/** The attribute that names the item a region shows, by its row's id. The Inbox writes it on its
 *  reading column, which shows the next question when a press takes away the one it shows: what
 *  then stands where the pressed control stood is in another item, not what the press drew in its
 *  place (`controlsInPlace`). The rescue alone reads it. */
export const SHOWN_ITEM_ATTRIBUTE = "data-shown-item";

/** An element that names the item it shows (`SHOWN_ITEM_ATTRIBUTE`). */
const SHOWN_ITEM = `[${SHOWN_ITEM_ATTRIBUTE}]`;

/** The item `element` is in: the one the nearest element around it, itself included, names
 *  (`SHOWN_ITEM_ATTRIBUTE`), else null. An element taken away with the region it was in still has
 *  that region around it, so a removed element is read the same way; one taken away from inside
 *  the region has none around it any more, so the rescue notes its item as it takes the focus. */
function itemOf(element: Element): string | null {
  return element.closest(SHOWN_ITEM)?.getAttribute(SHOWN_ITEM_ATTRIBUTE) ?? null;
}

/** Whether `element` is in another item than `item`: the item it is in (`itemOf`), where it is in
 *  one, is not `item`. */
function inAnotherItem(element: Element, item: string | null): boolean {
  const own = itemOf(element);
  return own !== null && own !== item;
}

/** Whether any of `elements` is in or holds another item than `item`: the item it is in
 *  (`inAnotherItem`), where it is in one, or an item an element inside it names. Both, since the
 *  region can stay with its mark changed to the next item while only what it holds is drawn again,
 *  and what arrived in it then carries no mark of its own. */
function showsAnother(elements: readonly Element[], item: string | null): boolean {
  for (const element of elements) {
    if (inAnotherItem(element, item)) return true;
    for (const marked of element.querySelectorAll(SHOWN_ITEM)) {
      if (marked.getAttribute(SHOWN_ITEM_ATTRIBUTE) !== item) return true;
    }
  }
  return false;
}

/** The controls of what the reader's press drew where `loss`'s element left the page, among
 *  `drawn`, in the order the focus is offered them: those of a `<form>` there first - the editor a
 *  press opened in the control's place, as Change answer and Add a priority open theirs - so that
 *  a header the new content repeats above the form is not taken for the place, then the rest.
 *  None where nothing new stands there (`standingAt`). None either where what stands there is in
 *  or holds another item than the one the element was in (`showsAnother`): another item shown in
 *  the place, as the Inbox's reading column shows the next question once a press takes away the
 *  one it showed - drawn again, or kept with its mark changed - and not what the press drew
 *  there, and the column hands the focus to its own stop itself (`Inbox.tsx`). The place is the
 *  nearest of the loss's spots whose node has gone while the parent it stood in is still in the
 *  region: the element's own spot, or that of the parent it went with. There is none while the
 *  element and every parent of it are still in the page - a control disabled, hidden or moved.
 *  Only what arrived counts: a row that moved up into the place of a row taken away stood in the
 *  page before the loss, so it is nothing new there, and `controlsOfRowMovedUp` answers for it
 *  instead. None either for a loss that notes no arrivals (`UNPLACED`). */
function controlsInPlace(loss: Loss, drawn: readonly HTMLElement[]): HTMLElement[] {
  const spot = loss.spots.find((each) => goneFrom(each, loss.region));
  if (spot === undefined || loss.arrived === null) return [];
  const standing = standingAt(spot, loss.arrived);
  if (standing.length === 0 || showsAnother(standing, loss.item)) return [];
  const controls = drawn.filter((control) => standing.some((each) => each.contains(control)));
  const form = formIn(standing);
  if (form === null) return controls;
  return [
    ...controls.filter((control) => form.contains(control)),
    ...controls.filter((control) => !form.contains(control)),
  ];
}

/** The first element among `node` and the siblings after it, else null. */
function elementFrom(node: Node | null): Element | null {
  for (let each = node; each !== null; each = each.nextSibling) {
    if (each instanceof Element) return each;
  }
  return null;
}

/** The controls of the row that now stands where a row around `loss`'s element stood, among
 *  `drawn`, in the order the focus is offered them: the same control as the lost one, so the
 *  reader can go on - unpinning, as Dismiss goes on to the next item - then that row's first
 *  control. The row that left is the one of the rows around the element (`rowPlaces`) whose own
 *  spot's node has left the page while the parent it stood in is still in the region
 *  (`goneFrom`): with rows nested, the outermost one that went, as an Inbox row a press takes
 *  away with the ask card in it, since each row inside it went with a parent of its own. The row
 *  now at its place is the element that stood right after it in that parent, still there and a
 *  row (`ROW_SELECTOR`) - the row that moved up, the same element. The same control is the one at
 *  the lost control's place among that row's controls (`rowControls`), of the same tag and role,
 *  and drawn: read by markup in both rows, so every engine finds the same one. None where the
 *  element stood in no row, where each row around it is still in the page or went with a parent
 *  of its own, where no row stood after the one that left, or where that row draws no control. */
function controlsOfRowMovedUp(loss: Loss, drawn: readonly HTMLElement[]): HTMLElement[] {
  const { element, region, rowPlaces, spots } = loss;
  let left: { readonly index: number; readonly spot: Spot } | null = null;
  for (const { index, row } of rowPlaces) {
    const spot = spots.find(({ node }) => node === row);
    if (spot !== undefined && goneFrom(spot, region)) {
      left = { index, spot };
      break;
    }
  }
  if (left === null) return [];
  const { index, spot } = left;
  const moved = elementFrom(spot.next);
  if (moved === null || moved.parentNode !== spot.parent || !moved.matches(ROW_SELECTOR)) {
    return [];
  }
  return controlsLikeIn(moved, index, element, drawn);
}

/** The controls `row` offers a focus lost at `index` among the controls of the row that stood at
 *  `row`'s place, in the order the focus is offered them: the same control, which is the one at
 *  that place among `row`'s controls (`rowControls`), of `like`'s tag and role, so the reader can
 *  go on, and then `row`'s first control. Only controls among `drawn` are offered, and none at
 *  all where `row` draws none. Read by markup in both rows, so every engine finds the same one. */
function controlsLikeIn(
  row: Element,
  index: number,
  like: Element,
  drawn: readonly HTMLElement[]
): HTMLElement[] {
  const controls = drawn.filter((control) => row.contains(control));
  const first = controls[0];
  if (first === undefined) return [];
  const same = rowControls(row)[index];
  const alike =
    same instanceof HTMLElement &&
    same !== first &&
    controls.includes(same) &&
    same.tagName === like.tagName &&
    same.getAttribute("role") === like.getAttribute("role");
  return alike ? [same, first] : [first];
}

/** The controls the row now standing at a moved row's place offers the focus that `control`, a
 *  control of the moved row `from`, holds: the same control - the one at `control`'s place among
 *  the row's controls, of its tag and role - and then that row's first control, each drawn
 *  (`controlsLikeIn`). The Agents page hands the focus on this way where a pin's move leaves it
 *  on a control the reader cannot see whole (`AgentsPage`'s `focusAfterPin`), the rule the page's
 *  rescue follows for a row a press takes away (`controlsOfRowMovedUp`). */
export function controlsLikeRow(row: Element, control: Element, from: Element): HTMLElement[] {
  return controlsLikeIn(
    row,
    rowControls(from).indexOf(control),
    control,
    focusableIn(row).filter(rendered)
  );
}

/** Focuses `target` without a scroll: what a press drew where its control stood, which stands
 *  where the reader was, a control given back after a loss the reader did not cause, a modal
 *  dialog's own stop, a region's own stop (`focusOwnStop`), and the control the Agents page hands
 *  a pin's moved focus to (`controlsLikeRow`). Answers whether it took the focus. */
export function focusNoScroll(target: HTMLElement): boolean {
  target.focus({ preventScroll: true });
  return document.activeElement === target;
}

/** Focuses `region` at its own stop without a scroll. A region with no Tab stop of its own is
 *  given `tabindex="-1"` for it. The page's rescue ends there for a loss the reader caused
 *  (`handOn`, `leaveUnseen`), a component's hand-off ends there where no control in sight takes
 *  the focus (`useFocusPlace`), and so does the selection bar's hand-off in a list
 *  (`focusListStop`). */
export function focusOwnStop(region: HTMLElement): void {
  if (!region.hasAttribute("tabindex")) region.tabIndex = -1;
  focusNoScroll(region);
}

/** The controls a loss the reader caused walks back to, nearest first: the control now at `index`
 *  in `controls`, then each control before that place (`placesFrom`), save every control that
 *  takes typed text (`takesText`). The reader never chose such a field, and their next key would
 *  be typed into it: Delete group on the delivery editor's only group would walk back to the "New
 *  group name" box. The walk passes over it to the nearest control before it that takes no text.
 *  Where only such fields stand there, it offers nothing; a region still present, other than the
 *  document, takes the focus at its own stop (`handOn`), and a component's hand-off tries its
 *  `fallback` (`useFocusPlace`). Both the page's rescue and a component's hand-off also pass over
 *  every control the walk offers that the reader cannot see now (`inSightNow`), as they reach it:
 *  a landing scrolls its control into view (`land`), and the nearest control before the lost
 *  place can lie far from where the reader is. */
function walkBack(
  controls: readonly HTMLElement[],
  index: number,
  atPlace: (control: Element) => boolean
): HTMLElement[] {
  return placesFrom(controls, index, atPlace).filter((control) => !takesText(control));
}

/** Whether the reader can see any part of `element` now: its box meets the viewport and every box
 *  around it that clips it (`clippingBoxes`, which reads each box's style once in `clips`). The
 *  page's rescue and a component's hand-off (`useFocusPlace`) read it of each control their walk
 *  back reaches, as they reach it, the hand-off of its fallback too, and the selection bar's
 *  hand-off reads it of each row mark it tries (`SelectionBar`'s `markShown`), with `moved`
 *  where the mark's row will move up once the rows a press is taking away have gone. */
export function inSightNow(
  element: Element,
  clips: Map<Element, boolean>,
  moved?: MovedUp
): boolean {
  return inSight(element, clippingBoxes(element, clips), moved);
}

/** Whether the reader can see the whole of `element` now: its box lies within the viewport and
 *  within every box around it that clips it (`clippingBoxes`, which reads each box's style once
 *  in `clips`). A landing partly in sight (`inSightNow`) and not wholly in it is scrolled to its
 *  nearest edge (`land`); one wholly in sight scrolls nothing, and the Agents page reads it of
 *  the control a pin's move left the focus on. */
export function inSightWhole(element: Element, clips: Map<Element, boolean>): boolean {
  const sight = sightOf(clippingBoxes(element, clips), undefined);
  const drawn = boxOf(element, undefined);
  return (
    drawn.top >= sight.top &&
    drawn.bottom <= sight.bottom &&
    drawn.left >= sight.left &&
    drawn.right <= sight.right
  );
}

/** Whether `element` reaches above what the reader can see, the viewport cut to every box around
 *  it that clips it: the top of its box stands above the top of that. With `moved`, it is read
 *  where it will stand once that row has moved up. One that is also not in sight (`inSightNow`)
 *  stands wholly above it. */
export function aboveSightNow(
  element: Element,
  clips: Map<Element, boolean>,
  moved?: MovedUp
): boolean {
  return boxOf(element, moved).top < sightOf(clippingBoxes(element, clips), moved).top;
}

/** Hands on the focus `loss`'s element took with it when it went. A loss the reader did not
 *  cause - an update of the server's, which can draw a form of its own where the control stood,
 *  or take a row away - is handed nowhere (`leaveUncaused`): whatever stands there now, whatever
 *  row moved up and whatever control is near, the page chose it and the reader did not, so the
 *  focus stays on the body, or on the stop of an open modal dialog, and nothing scrolls. A loss
 *  the reader caused is answered inside the region it was lost in while that region is still in
 *  the page and drawn (`present`). Where something now stands where the element stood - what
 *  their press drew in its place - the focus lands on that first: on the first control of a
 *  `<form>` there, the editor the press opened in the control's place, else on the first control
 *  of what stands there (`controlsInPlace`), focused with no scroll, since it stands where the
 *  reader was, but never on another item shown there, as the Inbox's reading column names the
 *  question it shows (`SHOWN_ITEM_ATTRIBUTE`). Where nothing new stands there, or only another
 *  item, and the press took away a row the element was in, the focus lands on the same control
 *  in the row that now stands at that row's place - the row that stood right after it, moved up -
 *  so the reader can go on, else on that row's first control (`controlsOfRowMovedUp`), and that
 *  control is scrolled to its nearest edge where it is partly in sight (`land`). Only where no
 *  row stands there, or that row draws no control to take it, does the focus walk back
 *  (`walkBack`): to the control the region now draws at the element's place, which is a control
 *  that moved into the place from the element's own row, card or toolbar (`atPlaceOf`), else the
 *  nearest one before it however many went together, passing over every control that takes typed
 *  text, every control the reader cannot see now (`inSightNow`) and every control of another item
 *  than the one the element was in (`inAnotherItem`), such as the next question the Inbox's
 *  reading column draws in its place, else the region itself, which is every region but the
 *  document, focused with no scroll. The place of an element hidden rather than taken away is
 *  counted where it still stands, among the controls the region draws now (`countBefore`), and
 *  that of one taken away off the removal that took it (`Loss`'s `index`), so the controls that
 *  went with it before it never count. A control that only takes the element's index in the
 *  region's Tab order, from another row or item, is at no place of the element's: Unsubscribe on
 *  an Inbox card's recipients would land on the next question's own checkbox. The walk takes only
 *  a control in sight, because a landing may scroll its control to its nearest edge (`land`), and
 *  the nearest control before the lost place can lie far from it. Jump to latest on an agent's
 *  transcript goes once it has scrolled the transcript to its end, and the control before it is
 *  the transcript's last: in Chromium, landing there scrolled the transcript from its end,
 *  2515 px down, back to 1146 px.
 *  From a region that has gone - a dialog or a panel that closed, or one hidden while it stays
 *  mounted - the focus goes only back to the place it was opened from (its `opener`, or that
 *  region's own opener where it has gone too): that element, the control that opened it - a
 *  list's trigger - if it can still take the focus, scrolled to its nearest edge where it is
 *  partly in sight, else the same control in the row that moved up into the place of a row that
 *  control was taken away with, else that row's first control (`controlsOfRowMovedUp`), else the
 *  control now at its place, read by the same rule (`atPlaceOf`, against the rows that control
 *  stood in), else the nearest one before it, by the same walk back. Each of these is taken only
 *  in sight, passing over the controls of another item than the one that control was in. That
 *  place is counted where the control still stands, or off the spot it went from
 *  (`openerPlace`). A dialog opened from the body hands the focus nowhere: the body is where the
 *  reader was. */
function handOn(loss: Loss, main: HTMLElement | null): void {
  const { caused, region } = loss;
  if (!caused) {
    leaveUncaused(region);
    return;
  }
  let back: HTMLElement[] = [];
  // The item the walk's place was in: the element's, else its opener's. The walk lands on no
  // control of another item.
  let item = loss.item;
  if (present(region)) {
    const drawn = drawnIn(region, main);
    for (const control of controlsInPlace(loss, drawn)) {
      if (focusNoScroll(control)) return;
    }
    for (const control of controlsOfRowMovedUp(loss, drawn)) {
      if (land(control)) return;
    }
    // A control hidden rather than taken away still stands in the page: its place is counted
    // where it stands, among the controls drawn now, since the count made at its `focusout` - one
    // made before the hide, at a disable or at a `blur()` - takes in every control before it that
    // was hidden with it.
    const index = loss.element.isConnected ? countBefore(drawn, loss.element) : loss.index;
    back = walkBack(drawn, index, atPlaceOf(rowsIn(loss.spots), region));
  } else {
    const opener = drawnOpener(loss.opener);
    if (opener === null || opener.path !== loss.path) return;
    if (opener.element.isConnected && rendered(opener.element) && land(opener.element)) return;
    const drawn = drawnIn(opener.region, main);
    back = [
      ...controlsOfRowMovedUp(opener, drawn),
      ...walkBack(
        drawn,
        openerPlace(opener, drawn),
        atPlaceOf(rowsIn(opener.spots), opener.region)
      ),
    ];
    item = opener.item;
  }
  const clips = new Map<Element, boolean>();
  for (const candidate of back) {
    if (inAnotherItem(candidate, item) || !inSightNow(candidate, clips)) continue;
    if (land(candidate)) return;
  }
  if (!present(region) || region === document.body || !(region instanceof HTMLElement)) return;
  focusOwnStop(region);
}

/** The place among `drawn`, the controls its region draws now, that `opener` - the control a
 *  dialog or a panel was opened from - left: where it still stands, for one hidden rather than
 *  taken away (`countBefore`), else off the spot it went from, the nearest of its spots whose node
 *  has gone from a parent still in its region (`goneFrom`, `placeOfRemoval`), else the place
 *  counted as the focus left it for the dialog. That count was made while the control still stood
 *  in the page, so it takes in every control before it that went or was hidden with it since: a
 *  confirm that takes its dialog away, and its write's answer that takes away the opener's list
 *  with a name link before the opener. */
function openerPlace(opener: Loss, drawn: readonly HTMLElement[]): number {
  if (opener.element.isConnected) return countBefore(drawn, opener.element);
  const spot = opener.spots.find((each) => goneFrom(each, opener.region));
  return spot === undefined ? opener.index : placeOfRemoval(drawn, spot);
}

/**
 * The page's one focus rescue, mounted once in the app shell: after a loss of the focus to the
 * body - a press, or an update that takes the focused control away - it answers a focus the
 * element holding it took with it when it was disabled, hidden, taken away or drawn again under a
 * new key, since a keyboard reader left on the body starts again from the page's first link, a
 * walk through everything above the control they were using. It leaves the focus on the body
 * where that is where the reader put it or left the control (below): a dialog opened from the
 * body that closes, the reader's Escape, their click on the page or their Enter that commits a
 * field, and a control they had scrolled out of their sight; and for every loss they did not cause,
 * as the page did before it had a rescue, save the two exceptions `leaveUncaused` names.
 *
 * Every element that takes the focus is noted as it does (`focusin`), with its region - the
 * dialog or panel holding it (`DIALOG`), modal or not, else the shell's main content (`main`),
 * else the document - the path of the page it was focused on, which a navigation does not change
 * before the old page goes, where it stands (`spotsOf`), the item it is in (`itemOf`), and the
 * dialog, panel or selection bar around it that a press is counted in (`noted`). A dialog or
 * panel the focus enters for the first time notes where it came from: the loss of the element
 * that handed it over - a list's trigger, for the list's text box - or none when it came from the
 * body. A loss is seen two ways.
 * A control disabled, hidden or left by the reader fires a `focusout` in every engine, while it is
 * still in the page. A control taken away, itself or with a parent, is seen by watching the page
 * (a MutationObserver over the document) in every engine, since Firefox and WebKit fire no
 * `focusout` for it; Chromium fires one as well, and whichever comes first is the loss recorded.
 * Each loss holds its element, its region, the path, whether the page took the element (`taken`),
 * whether the reader caused the loss (`causedByReader`), and its dialog's opener. The reader caused
 * it when a press of theirs (`isPress`) is still live in its row, dialog or panel (`answerPress`):
 * a press is live while a hold on what answers it is on, and besides, one that started a write
 * among the page's `writes` while that write is out, and one that started none while a control it
 * reads as is held. A key, a `change` or an `input` that is no press neither starts a press nor
 * ends the live one (`notePress`). A loss the reader caused holds its place among the controls the
 * region draws too, and where the element stood in the page: the parent it stood in and the
 * siblings either side of it, and the same for each parent of it up to the region (`spotsOf`). For
 * a `focusout` they are read there and then, while the element is still in the page; for a control
 * taken away with no `focusout`, the last record that took it - its parent and its siblings at that
 * moment, since a control the batch moved first was taken from there - is that spot, with the spots
 * read as the element took the focus behind it for a record whose parent has gone too. It holds
 * each row around the element, and the element's place among that row's controls (`rowPlacesOf`),
 * and the item the element was in (`itemOf`), read then too where the element is still in it, else
 * the one noted as it took the focus: the element has gone by the time the loss is answered, and
 * one taken away from inside the region that names its item has no item around it then. From the
 * loss on, while its check or its wait is out, the page's watcher notes every element the page adds
 * (`arrived`), but none it only moves - a keyed node React moves with `insertBefore`, which one
 * batch reports as its removal and its addition, stood in the page before; for a loss read off a
 * removal, the batch that took the element counts from that last record on. A loss the reader did
 * not cause is answered without any of these (`leaveUncaused`), so none of them is read for it. The
 * focus moving from one control to another in the same region is no loss. Nor is the reader leaving
 * a control: their Enter that commits a field with the page's own `blur()`, or their click on
 * the page's text, sends the focus to nothing from a control still enabled and drawn, and the
 * focus stays where that left it. Such a loss is marked left (`left`) when the control still
 * stands so in a microtask queued from its `focusout`, once the event that moved the focus has
 * run, and nothing answers it, even when a commit after that takes the control away - the issue
 * title's editor, whose save draws the heading in its place. A control a commit takes away fires
 * Chromium's `focusout` during the removal itself, while it is still in the page, so by that
 * microtask it has gone, and its loss is answered. A loss noted at a `focusout` can count its
 * place while the control, and every control before it that goes with it, still stand in the
 * page: Chromium's as a commit takes the control away, or one at a disable or a `blur()` before
 * the control is hidden or taken away. So where the page's watcher sees the removal that takes
 * the control while the loss's check or wait is out, the place is counted again off that removal,
 * as for a control taken away with no `focusout`; and the place of a control hidden rather than
 * taken away is counted where it still stands as the loss is answered (`handOn`). A new loss
 * cancels whatever is still out for the one before it.
 *
 * The check runs a frame and a task after the loss, once React has committed and run its passive
 * effects, so every component's own hand-off has had its turn first: whatever moves the focus
 * in the commit that took the control away or in an effect after it - every `useFocusPlace`
 * hand-off (ControlRow, DataTable, PanelTrigger and the pages' own), SelectionBar's
 * `returnFocus`, AskCard's, `MultiSelect`'s return to its trigger, `useDialog`'s return to the
 * opener, the Agents composer's take-back and the issue page's own keeper among them - and the
 * Inbox reading column's hand-off to its own stop (`Inbox.tsx`, the layout effect after
 * `paneScroller`), which runs on the layout lane so that it comes before this check. Each knows
 * its own target better than a place counted in the page does - the row marked last, the opener,
 * the composer's box - so the rescue answers only a loss none of them answered, and acts only
 * while the focus is still lost. A component's own hand-off to its region's stop is the exception
 * `leaveUncaused` names. The rescue leaves alone a loss the reader left (above), and a loss on a
 * page the reader has since left, since the new page owns its focus. A taken element that is
 * back by then gets the focus back, whoever caused its loss; after a loss the reader did not
 * cause, only on the conditions `leaveUncaused` names.
 *
 * An element that went - taken away, hidden, no longer drawn, or replaced under a new key - hands
 * the focus on (`handOn`). For a loss the reader caused (`causedByReader`), it is answered inside
 * the region it was lost in, and what their press drew where the element stood takes it first:
 * the elements the page added since the loss between the siblings either side of the element, or
 * of the parent it went with, in the parent it was taken from, which is still in the region
 * (`standingAt`). Where that holds a `<form>` - an editor the press opened in the control's
 * place, as Change answer and Add a priority do - the form's first control takes the focus, so
 * that a header the new content repeats above the form is not taken for the place; else the
 * first control of what stands there does. It is focused with no scroll, since it stands where
 * the reader was. That is for what the press itself drew there - a form opened in place, an edit
 * mode - and never for another item shown in its place, as the Inbox's reading column names the
 * question it shows (`SHOWN_ITEM_ATTRIBUTE`): what stands there in or holding another item than
 * the element's - the column drawn again for the next question, or kept with its mark changed -
 * takes nothing, the column handing the focus to its own stop, and the loss is answered as where
 * nothing new stands. Where nothing new stands there and the press took away a row the element was
 * in - with rows nested, the outermost that went, as an Inbox row with the ask card it holds - the
 * row that stood right after that row has moved up into its place, the same element, so nothing
 * new there, and the same control in it takes the focus, at the lost one's place among the row's
 * controls and of its tag and role, else that row's first control (`controlsOfRowMovedUp`), so the
 * reader can go on, unpinning as Dismiss goes on to the next item. Only where no row stands
 * there - a block taken away with nothing in its place, or the last row - or that row draws no
 * control does the focus walk back, as `handOn` states, passing over every control that takes
 * typed text (`walkBack`), every control out of the reader's sight (`inSightNow`) and every
 * control of another item shown in the place (`inAnotherItem`), else to the region's own stop
 * with no scroll; that walk stays inside the region, so the page behind a modal dialog is never
 * reached. A loss the reader did not cause - an update of
 * the server's, which can draw a form of its own where the control stood, as a withdrawn
 * question's answer form gives way to a Reply form, or a live update taking a row away - is handed
 * nowhere (`leaveUncaused`): the focus stays on the body, as it did before the page had a rescue,
 * and nothing scrolls, since the page, not the reader, chose whatever stands near the lost place,
 * and their next key would act on it or be typed into it. An open modal dialog takes the focus
 * then, as its own stop, since a focus that leaves an open modal reaches the page behind it. Only
 * a region that has gone hands the focus out of it, and only for a loss the reader caused: from a
 * dialog or a panel that closed it goes only back to the place it was opened from - a list a pick
 * takes away to its trigger; a dialog opened from the body - a single-key shortcut with nothing
 * focused - leaves the focus on the body, where the reader was, and never puts it in a text box
 * whose next key would be typed. An element disabled under the focus, itself or through the
 * fieldset around it, gets the focus back when it is enabled again, however long its hold lasts:
 * the wait is on the hold itself, watched rather than polled, since its write's latency is the
 * server's and a hold through a read can outlast the write deadline, and it keeps no deadline of
 * its own. Where the element goes or is hidden instead, the focus is handed on as from an element
 * that went. So it is for a control disabled by an update the reader did not cause, only on the
 * conditions `leaveUncaused` names. That settle too waits a frame and a task, so a component
 * that watches the same control - the Agents composer, giving its box the focus back - acts on
 * the change first. Short of that, and of a new loss, a check or a wait ends only when the reader
 * moves on, whoever caused its loss: a pointer press anywhere - onto a control, or onto the page
 * itself - and a key that is a press of theirs (`isPress`, `readerMoved`, `noteKey`), which is
 * every key on the body, where the focus waits, and in a field that takes typed text only a key
 * that acts on the page. Once the reader acts, the focus is theirs, and their act's own action
 * decides where it goes: so `j`, `k` or any other key on the page leaves the focus where the
 * reader is using it, a control enabled again after that takes nothing back, and one that goes
 * after it hands nothing on. The reader's Escape also marks the loss it causes in its own task as
 * the reader leaving the control (`leaving`): a control it takes away or leaves - the issue
 * title's editor, which it closes - is not answered, and the focus stays where Escape left it
 * rather than going to what is drawn in its place, as the Inbox's and the Board's own Escape
 * leave it on the page.
 * The control a loss the reader caused lands on in the row that moved up, or walks back to, is
 * scrolled into view (`land`), and so is a control given back after a loss they caused; what
 * stands where the element stood after a loss they caused, a control given back after a loss
 * they did not cause, and a modal dialog's own stop are focused with no scroll.
 *
 * A loss the reader can no longer see is not answered: the element was scrolled out of their
 * sight - out of the viewport, or out of a scroll box around it - before it went, or before the
 * control a wait is on came back, and its region is still in the page. Every scroll that can move
 * the noted element, the page's or a box's around it, reads whether any of it is still in sight
 * (`Sight`), while a loss of it could still be answered; the document's capture listener hears a
 * box's scroll before the box's own listeners, so a virtual list that takes the row away in its
 * listener has it read while it is still in the page. So does every resize of the window, which
 * can move the element with nothing scrolled - a box sized to the window shrinks under its row,
 * which the virtual list may then stop drawing - read against every box around it that clips
 * it, since none of them has to have scrolled. An element no longer drawn is read no more: what
 * the last read before it went saw stands. Nothing scrolls and no control the reader did not
 * pick takes the focus, so neither a list nor the page is pulled back to a row the reader
 * scrolled away from - a live update taking that row away, say - and their next key acts on
 * nothing they did not choose. The focus stays on the body (`leaveUnseen`), except, for a loss
 * the reader caused, in a dialog or a panel (`DIALOG`), which takes it itself without a scroll,
 * and, for one they did not, in an open modal dialog, whose own stop takes it (`leaveUncaused`),
 * so the page behind a modal one is never reached. The record is the page's one (`outOfSight`),
 * which two component hand-offs read too (`useFocusPlace`, `SelectionBar`), so neither answers
 * such a loss before the rescue's check. Only an element a read has found in sight since it took
 * the focus can be read out of it (`readSight`): one focused below the fold, which a scroll the
 * page made before the engine's own left there, was never in the reader's sight.
 *
 * `writes` is the page's write store (`writesOf`): the writes a press starts there are its action,
 * which keeps it live (`answerPress`). Where none is handed - a unit case read without the app's
 * store - no press starts a write, and each is watched on a control it holds and on the holds on
 * what answers it (`watchHold`).
 */
export function useFocusRescue(main: RefObject<HTMLElement | null>, writes?: Writes): void {
  useEffect(() => {
    rescueMounted = true;
    pageWrites = writes ?? null;
    const unsubscribe = writes?.subscribe(noteWrites);
    /** The element holding the focus, its region, the path it was focused on, and where it stood
     *  (`spotsOf`) and the item it was in (`itemOf`) as it took the focus, until it loses the
     *  focus by a `focusout`; one still noted here once it has left the page went with no
     *  `focusout` at all. */
    let focused: Pick<Loss, "element" | "item" | "path" | "region" | "spots"> | null = null;
    /** The loss each dialog or panel (`DIALOG`) the focus has entered was opened from, or null for
     *  one it entered from the body. */
    const openedFrom = new Map<Element, Loss | null>();
    /** Where a `focusout` is handing the focus, and the loss it leaves in another region. */
    let moving: { from: Loss | null; to: Element } | null = null;
    /** The check or the wait still out for the latest loss: what cancels it, and that loss, into
     *  whose `arrived` the page's watcher notes the page's new elements while it is out. */
    let pending: { readonly cancel: () => void; readonly loss: Loss } | null = null;
    /** The reader's Tab a live press's region may keep, while the focus has not landed yet: the
     *  Tab's own event, and the press it was pressed during (`notePress`, `settleTab`). Null
     *  while no Tab is waiting. */
    let tabbing: { readonly event: Event & { readonly target: Element }; press: Press } | null =
      null;
    /** Notes into `arrived` each element `records` added to the page from the record at `from`
     *  on, as one a loss's place may now hold. A node an earlier record of the same batch removed,
     *  and that had not arrived, is one the page moved - React moves a keyed node with
     *  `insertBefore`, which the batch reports as its removal and then its addition - so it stood
     *  in the page before the loss, and it is not noted. */
    const noteArrivals = (records: readonly MutationRecord[], arrived: Set<Node>, from = 0) => {
      const removed = new Set<Node>();
      for (const [at, each] of records.entries()) {
        if (at >= from) {
          for (const node of each.addedNodes) {
            if (node instanceof Element && !removed.has(node)) arrived.add(node);
          }
        }
        for (const node of each.removedNodes) {
          if (!arrived.has(node)) removed.add(node);
        }
      }
    };
    /** Whether the reader had scrolled `loss`'s element out of their sight before it went, in a
     *  region still in the page. */
    const unseen = (loss: Loss) => outOfSight(loss.element) && present(loss.region);

    /** Waits for `loss`'s element, disabled under the focus by the reader's press or by an update,
     *  to be enabled again, to go or to be hidden, however long that takes: the wait is on the hold
     *  itself and keeps no deadline of its own, so a control held through a read that outlasts the
     *  write deadline still gets the focus back once it is let go. Only `check` begins it, so a loss
     *  the reader left (`left`) never has one. A control enabled again gets the focus back while the
     *  focus is still lost; after a loss the reader did not cause, only on the conditions
     *  `leaveUncaused` names. One that goes or is hidden is handed on (`handOn`). A pointer press,
     *  or a key that is a press of the reader's (`isPress`) - every key on the body - ends the wait,
     *  whoever caused the loss (`readerMoved`, `noteKey`), so the control enabled again after it
     *  gets nothing, and one that goes after it hands nothing on. */
    const wait = (loss: Loss): (() => void) => {
      const { element } = loss;
      /** Cancels the settle once it is scheduled. */
      let settling: (() => void) | undefined;
      const settle = () => {
        pending = null;
        if (!focusLost() || window.location.pathname !== loss.path) return;
        if (unseen(loss)) {
          leaveUnseen(loss);
          return;
        }
        // The very control back, whoever caused the loss: a press of the reader's since has ended
        // this wait already (`noteKey`, `readerMoved`). One gone or hidden, or one that takes no
        // focus, is handed on as from a control that went.
        if (element.isConnected && rendered(element)) {
          if (loss.caused ? land(element) : focusNoScroll(element)) return;
        }
        handOn(loss, main.current);
      };
      // The element enabled again, gone or hidden: settled a frame and a task later, as the first
      // check is, so a component answering the same change - its own watcher, started after this
      // one, such as the Agents composer's take-back, or an effect - moves the focus first. The
      // first change of a burst schedules it, and the watching ends there.
      const later = () => {
        if (settling !== undefined) return;
        watcher.disconnect();
        settling = afterComponents(settle);
      };
      // The whole document is watched, not only the element's own `disabled` and its
      // ancestors', so that the element going - with its dialog, say - ends the wait too.
      const watcher = new MutationObserver(() => {
        const held =
          element.isConnected &&
          element.matches(":disabled") &&
          element.closest("[hidden]") === null;
        if (!held) later();
      });
      watcher.observe(document.body, {
        attributeFilter: ["disabled", "hidden"],
        childList: true,
        subtree: true,
      });
      return () => {
        watcher.disconnect();
        settling?.();
      };
    };

    /** Answers `loss` a frame and a task after it, while the focus is still lost on the page it
     *  was lost on; a loss the reader left (`left`) it leaves where the reader put the focus. A
     *  control taken and back by then - hidden and drawn again, disabled and enabled again - gets
     *  the focus back, whoever caused the loss; after a loss the reader did not cause, only on the
     *  conditions `leaveUncaused` names. A pointer press after the loss, or a key after it that is
     *  a press of the reader's (`isPress`), cancels this check, whoever caused the loss
     *  (`readerMoved`, `noteKey`). */
    const check = (loss: Loss) => {
      pending = null;
      if (loss.left || window.location.pathname !== loss.path || !focusLost()) return;
      const { element } = loss;
      if (unseen(loss)) {
        leaveUnseen(loss);
      } else if (!element.isConnected || !rendered(element)) {
        handOn(loss, main.current);
      } else if (element.matches(":disabled")) {
        pending = { cancel: wait(loss), loss };
      } else if (loss.taken) {
        if (loss.caused) land(element);
        else focusNoScroll(element);
      }
    };

    const noteFocus = (event: globalThis.FocusEvent) => {
      const element = event.target;
      if (!(element instanceof HTMLElement)) return;
      // Where the Tab the live press's region may keep landed decides it (`notePress`).
      settleTab(element);
      const region = regionOf(element, main.current);
      if (region.matches(DIALOG) && !openedFrom.has(region)) {
        for (const dialog of openedFrom.keys()) {
          if (!dialog.isConnected) openedFrom.delete(dialog);
        }
        openedFrom.set(region, moving?.to === element ? moving.from : null);
      }
      moving = null;
      focused = {
        element,
        item: itemOf(element),
        path: window.location.pathname,
        region,
        spots: spotsOf(element, region),
      };
      noted = { element, region: element.closest(PRESS_REGION) };
      // The same element taking the focus again - the window given back the focus - keeps what
      // the scrolls since it was first noted saw. A new one is read where it took the focus,
      // before the engine has scrolled it into view.
      if (sight?.element !== element) {
        const boxes = new Set<Element>();
        sight = { boxes, element, seen: true, shown: inSight(element, boxes) };
      }
    };

    const record = (event: globalThis.FocusEvent) => {
      const element = event.target;
      if (!(element instanceof HTMLElement)) return;
      const path = focused?.element === element ? focused.path : window.location.pathname;
      focused = null;
      pending?.cancel();
      pending = null;
      const region = regionOf(element, main.current);
      const next = event.relatedTarget;
      if (leaving && !(next instanceof Element)) return;
      // A move from one control to another in the same region - j and k, the arrows - is no
      // loss, and counts nothing.
      if (next instanceof Element && regionOf(next, main.current) === region) {
        moving = { from: null, to: next };
        return;
      }
      const caused = causedByReader(element);
      const fields = {
        caused,
        element,
        // Read only where something reads it: a loss the reader caused walks back from it
        // (`handOn`), and the place a focus going to another region left is where the dialog it
        // enters goes back to.
        index:
          caused || next instanceof Element
            ? countBefore(drawnIn(region, main.current), element)
            : -1,
        left: false,
        opener: openedFrom.get(region) ?? null,
        path,
        region,
        taken: !stands(element),
      };
      // A focus going straight to another region is kept only as the place it left: the opener
      // of the dialog it is entering, whose arrivals the hand-off never reads. The item it was in
      // is read, since a walk back from its place lands in no other, and so are its spots and its
      // row places, which say the rows it stood in (`rowsIn`), the spot it went from
      // (`openerPlace`) and the row that moved up into its row's place (`controlsOfRowMovedUp`):
      // the hand-off back to that place reads them (`handOn`).
      if (next instanceof Element) {
        moving = {
          from: {
            ...fields,
            ...UNPLACED,
            item: itemOf(element),
            rowPlaces: rowPlacesOf(element, region),
            spots: spotsOf(element, region),
          },
          to: next,
        };
        return;
      }
      // What the page changed before this loss is none of what a press draws in its place: those
      // records are dropped unread, and only the ones after it are noted as arrivals.
      watcher.takeRecords();
      // A loss the reader did not cause is answered without where its element stood
      // (`leaveUncaused`), so none of that is read.
      const loss: Loss = caused
        ? {
            ...fields,
            arrived: new Set<Node>(),
            item: itemOf(element),
            rowPlaces: rowPlacesOf(element, region),
            spots: spotsOf(element, region),
          }
        : { ...fields, ...UNPLACED };
      pending = { cancel: afterComponents(() => check(loss)), loss };
      // The reader leaving the control - their Enter that commits it with the page's own
      // `blur()`, their click on the page's text - is no loss: the control still stands in the
      // page, enabled and drawn, once the event that moved the focus has run, and is marked left
      // then. A control a commit takes away fires Chromium's `focusout` during the removal
      // itself, so by this microtask it has gone, and its loss stays one to answer.
      if (!loss.taken) {
        queueMicrotask(() => {
          loss.left = element.isConnected && stands(element);
        });
      }
    };

    // A focused element taken away with no `focusout` - Firefox's and WebKit's way, for the
    // element or a parent of it - is read off the last mutation that took it, at the place it was
    // taken from, or at the region's end where no record of this batch names it. Every batch
    // that comes while a loss's check or wait is out is noted first as that loss's arrivals,
    // where it notes any. Where that batch took the loss's element away, the loss's place is
    // counted again off that removal: a loss noted at a `focusout` counted it while the element
    // still stood in the page, with every control before it that went with it, as an Inbox card's
    // subscriber chip takes its name link away with its Unsubscribe, and the walk back would start
    // at the control after them. A loss the reader did not cause notes no arrivals and keeps no
    // place.
    const watcher = new MutationObserver((records) => {
      const out = pending?.loss;
      if (out?.arrived) {
        noteArrivals(records, out.arrived);
        const removal = out.element.isConnected ? undefined : removalOf(records, out.element);
        if (removal !== undefined && out.region.isConnected) {
          out.index = placeOfRemoval(drawnIn(out.region, main.current), removal.spot);
        }
      }
      const held = focused;
      if (held === null || held.element.isConnected) return;
      focused = null;
      if (leaving || !focusLost()) return;
      pending?.cancel();
      const caused = causedByReader(held.element);
      const fields = {
        ...held,
        caused,
        left: false,
        opener: openedFrom.get(held.region) ?? null,
        taken: true,
      };
      // A loss the reader did not cause is answered without where its element stood
      // (`leaveUncaused`), so none of that is read.
      if (!caused) {
        const loss: Loss = { ...fields, ...UNPLACED, index: -1 };
        pending = { cancel: afterComponents(() => check(loss)), loss };
        return;
      }
      const removal = removalOf(records, held.element);
      const index =
        removal === undefined || !held.region.isConnected
          ? drawnIn(held.region, main.current).length
          : placeOfRemoval(drawnIn(held.region, main.current), removal.spot);
      // What this batch added from the last record that took the element on is what can stand in
      // its place; what it added before that record is not.
      const arrived = new Set<Node>();
      noteArrivals(records, arrived, removal?.at ?? 0);
      const loss: Loss = {
        ...fields,
        arrived,
        index,
        // Read where the element went with the region that names its item, else as it took the
        // focus: one taken away from inside that region has none around it now.
        item: itemOf(held.element) ?? held.item,
        rowPlaces: rowPlacesOf(held.element, held.region),
        spots: removal === undefined ? held.spots : [removal.spot, ...held.spots],
      };
      pending = { cancel: afterComponents(() => check(loss)), loss };
    });
    watcher.observe(document.body, { childList: true, subtree: true });
    /** The record a scroll or a resize reads into: the noted element's, while a loss of it could
     *  still be answered - it holds the focus, or a check or a wait is out - and while the page
     *  still draws it. A hidden element's box is the viewport's top-left corner, which would read
     *  as in sight, so what the last read before it went saw stands. */
    const readable = (): Sight | null => {
      const watched = sight;
      if (watched === null || (focused === null && pending === null)) return null;
      return watched.element.isConnected && rendered(watched.element) ? watched : null;
    };
    // A scroll that can move the noted element reads whether the reader can still see it. A box's
    // `scroll` does not bubble; the document hears it in its capture phase, before the box's own
    // listeners.
    const noteScroll = (event: Event) => {
      const watched = readable();
      if (watched === null) return;
      const { target } = event;
      if (target instanceof Element) {
        if (!target.contains(watched.element)) return;
        watched.boxes.add(target);
      }
      readSight(watched);
    };
    // A resize moves the noted element with nothing scrolled - a shorter window, a zoom, a box
    // sized to the window shrinking under it - so it reads against every box around it that clips
    // it, none of which has to have scrolled, and keeps those boxes for the scrolls after it.
    const noteResize = () => {
      const watched = readable();
      if (watched === null) return;
      for (const box of clippingBoxes(watched.element)) watched.boxes.add(box);
      readSight(watched);
    };
    /** Starts the reader's act on `event`, which `notePress` has read as a press, and ends the
     *  act before it with the watch on its action or hold. */
    const startPress = (event: Event & { readonly target: Element }) => {
      tabbing = null;
      lastPress?.stop();
      const region = event.target.closest(DIALOG);
      const opener = region === null ? null : (openedFrom.get(region) ?? null);
      const release = RELEASE[event.type];
      const held = main.current;
      lastPress = {
        answers: new Set(),
        changed: () => undefined,
        heldBefore: heldAt(event.target, opener),
        live: true,
        liveBefore: liveWriteIds(),
        madeIn:
          event.target.closest(PRESS_REGION) ??
          (held?.contains(event.target) === true ? held : null),
        opener,
        region,
        release: release === undefined ? null : { by: pressedBy(event), type: release },
        starting: true,
        stop: () => undefined,
        target: event.target,
        writes: new Set(),
      };
      answerPress(lastPress);
    };
    /** Settles the Tab a live press's region may keep (`tabbing`), once the focus has landed: the
     *  press stays the cause of a later loss in that region where the focus landed inside it, and
     *  the Tab starts an act of its own where it landed anywhere else. Where no Tab is waiting,
     *  it does nothing. */
    const settleTab = (landing: Element | null) => {
      const waiting = tabbing;
      if (waiting === null) return;
      tabbing = null;
      if (landing !== null && waiting.press.madeIn?.contains(landing) === true) return;
      startPress(waiting.event);
    };
    /** Records the reader's own act, a press (`isPress`) - a pointer press, a key that acts rather
     *  than edits a field's text, or a pick, which Playwright and assistive tech make with an
     *  `input` and a `change` alone - so a loss while it is live can be told from one an update
     *  made under them (`causedByReader`), with the dialog or panel it landed in and the loss that
     *  region was opened from, read now, while the region is still in the page, for when a pick or
     *  a confirm takes it away (`pressedOn`), the controls it can read as that are already held
     *  (`heldAt`), read before the act runs, the page's writes live as it is made (`liveBefore`),
     *  the region it was made in (`madeIn`), and the release it waits for (`Release`). A key, a
     *  `change` or an `input` that is no press - a character typed in a field, the field's commit
     *  on blur, the `input` of its typing - neither starts an act nor ends the live one. A newer
     *  act ends the one before it, and the watch on that one's action or hold with it, except an
     *  act on a control the live act holds by a hold that began after it, or on anything inside
     *  that control (`onHeldControl`) - a double click, a repeated Enter, any other key or pointer
     *  press on that control or on a span or an icon of it: the held control takes the act without
     *  a write, so the act before it stays the cause, with its release and its watch's bounds as
     *  they were (`noteRelease`).
     *
     *  Tab, with Shift or without, is read as a new act on a held control, since it moves the
     *  focus off that control to another. One exception: while the act's own write is still out,
     *  a Tab that moves the focus inside the region that act was made in (`madeIn`) leaves it
     *  live. The region's later going is that act's own doing - the Inbox's Decline dialog, which
     *  goes with its row when the decline lands - so a loss there is one the reader caused. The
     *  act stays live for that alone: the Tab still ends the rescue's check or wait, so a write
     *  refused after it pulls the focus back to no control the reader has moved off
     *  (`readerMoved`, `noteKey`). Where the focus lands anywhere else, the Tab ends the act and
     *  is noted as an act of its own (`settleTab`), and a loss of the control it moved the focus
     *  to is judged on its own. Other keys that move the focus away, such as the Inbox's j and k,
     *  are passed over like any other key on the held control. Answers whether `event` is a
     *  press. */
    const notePress = (event: Event): boolean => {
      if (!isPress(event)) return false;
      const movesOff = event instanceof KeyboardEvent && event.key === "Tab";
      if (!movesOff && lastPress?.live === true && onHeldControl(lastPress, event.target)) {
        return true;
      }
      if (
        movesOff &&
        lastPress?.live === true &&
        lastPress.madeIn !== null &&
        writeOut(lastPress)
      ) {
        tabbing = { event, press: lastPress };
        return true;
      }
      startPress(event);
      return true;
    };
    /** The release of the reader's latest press - the `pointerup` of the pointer it was made by,
     *  the `keyup` of its key (`Release`) - which is what a click answers: a control pressed by the
     *  pointer, or by Space, acts on the release, however long the reader held it down. The press
     *  is live again from there, as if made then, on the control it was made on, and from that
     *  release alone, once: the release is spent then, so an act on the control the press holds,
     *  which the rescue does not note (`notePress`), and that act's release leave the press's
     *  watch and its bounds as they were (`answerPress`). A Tab's own release settles the Tab a
     *  live press's region may keep against where the focus stands then, for a Tab that moved it
     *  nowhere, so no such Tab outlives the key (`settleTab`). */
    const noteRelease = (event: Event) => {
      if (event instanceof KeyboardEvent && event.type === "keyup" && event.key === "Tab") {
        settleTab(document.activeElement);
      }
      const press = lastPress;
      if (press?.release?.type !== event.type || press.release.by !== pressedBy(event)) return;
      press.release = null;
      answerPress(press);
    };
    /** A pointer press: the reader's press (`notePress`), and the reader moving on, which ends any
     *  check or wait still out, whoever caused its loss. */
    const readerMoved = (event: Event) => {
      notePress(event);
      pending?.cancel();
      pending = null;
    };
    /** Whether the reader's Escape is being answered: set by its `keydown` until the next task,
     *  by when the commit it caused has run. */
    let leaving = false;
    /** A key the reader presses: noted as their press where it is one (`notePress`). A key that is
     *  a press of theirs (`isPress`) also ends any check or wait still out, whoever caused its
     *  loss, as a pointer press does (`readerMoved`): once the reader acts, their key's own action
     *  decides where the focus goes. That is every key on the body, where the focus waits after a
     *  loss, and in a field that takes typed text only a key that acts on the page. So the control
     *  that held the focus, coming back after `j`, `k` or any other key on the page, takes nothing,
     *  and one that goes after it hands nothing on. Escape also marks the reader leaving a control
     *  until the next task (`leaving`). */
    const noteKey = (event: KeyboardEvent) => {
      if (notePress(event)) {
        pending?.cancel();
        pending = null;
      }
      if (event.key !== "Escape") return;
      leaving = true;
      window.setTimeout(() => {
        leaving = false;
      }, 0);
    };
    document.addEventListener("focusin", noteFocus, true);
    document.addEventListener("focusout", record, true);
    document.addEventListener("pointerdown", readerMoved, true);
    document.addEventListener("pointerup", noteRelease, true);
    document.addEventListener("keydown", noteKey, true);
    document.addEventListener("keyup", noteRelease, true);
    document.addEventListener("change", notePress, true);
    document.addEventListener("input", notePress, true);
    document.addEventListener("scroll", noteScroll, { capture: true, passive: true });
    window.addEventListener("resize", noteResize);
    return () => {
      unsubscribe?.();
      watcher.disconnect();
      document.removeEventListener("focusin", noteFocus, true);
      document.removeEventListener("focusout", record, true);
      document.removeEventListener("pointerdown", readerMoved, true);
      document.removeEventListener("pointerup", noteRelease, true);
      document.removeEventListener("keydown", noteKey, true);
      document.removeEventListener("keyup", noteRelease, true);
      document.removeEventListener("change", notePress, true);
      document.removeEventListener("input", notePress, true);
      document.removeEventListener("scroll", noteScroll, true);
      window.removeEventListener("resize", noteResize);
      pending?.cancel();
      tabbing = null;
      sight = null;
      noted = null;
      pageWrites = null;
      rescueMounted = false;
      lastPress?.stop();
      lastPress = null;
    };
  }, [main, writes]);
}
