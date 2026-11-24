import { type FocusEvent, type SyntheticEvent, useLayoutEffect, useRef } from "react";

import { REQUEST_DEADLINE_MS } from "../api/client";
import { focusOnDocument } from "../features/shell/roving";
import { SEND_DEADLINE_MS } from "../features/writes/keyed-write";
import { CONTROL } from "./control-names";
import { commitsOnBlur, PANEL_ATTRIBUTE, takesText } from "./key-targets";
import { ROW_SELECTOR } from "./row-marker";

/**
 * The page's focus hand-offs: a component's own hand-off for a control a press takes out of it
 * (`useFocusPlace`), and the press that arms it (`armHandOff`). A row is `row-marker.ts`'s one
 * rule, `ROW_SELECTOR`. Every component that hands its focus off arms the hand-off with its own
 * press, at its root (`armOnPress`), and a loss is the reader's only while a press that armed it
 * is still live there (`causedByReader`, `pressOnRowMark`): on the lost control, inside any row
 * around it, or inside the dialog, panel or selection bar it was lost from. A hold on what
 * answers the press keeps it live (`holdHandOff`), and so does its control's own hold, up to the
 * write deadline. A Tab that moves the focus inside the region a press was made in, while that
 * press's own write is out, leaves the press live, since the region's later going is that press's
 * own doing. Nothing armed, a loss is answered as the page answered it before it had any
 * hand-off: the focus stays on the body, and nothing scrolls.
 *
 * A loss the reader caused lands on the control now at the lost control's place in the
 * component's container - one that moved there from the lost control's own row, card or toolbar,
 * and not one that only took its index in the Tab order (`atPlaceOf`) - or the nearest one before
 * it, else on the fallback the component names, else on the region's own stop. Where neither the
 * container nor the fallback offers a control, the hand-off walks back the same way over the
 * region its container was in (`walkBackInRegion`). A component's hand-off answers only for its
 * own controls: a focus in a dialog, a panel or a selection bar drawn inside its container is
 * that region's, and in a selection bar the bar's own hand-off answers a loss the reader caused
 * (`SelectionBar`). Each walk back passes over every control that takes typed text (`takesText`)
 * and every control the reader cannot see now (`inSightNow`). It lands a control with `land`,
 * which scrolls a control partly in sight to its nearest edge and one wholly in sight not at all.
 * A region's own stop takes the focus with no scroll (`focusOwnStop`). A loss of a control the
 * reader had scrolled out of their sight, read against what the reader could see while the press
 * was armed, scrolls nothing and focuses no control (`outOfSight`).
 */

/** The form controls: the focus reaches one only while it is not `disabled` (`FOCUSABLE`), and a
 *  row holds one as a control either way (`ROW_CONTROL`). */
const FORM_FIELDS = ["button", "textarea", "input", "select"];

/** A link, a disclosure's summary or a form control the page has not taken out of the Tab order:
 *  `tabindex="-1"` is a page's word that the keyboard does not stop there - a tab of a tab list
 *  other than the selected one, and a control of a row of the issue page's outline while that row
 *  holds no Tab stops (`tabStops`), since the keys move the focus between those rows. A hand-off
 *  that counted them would land where no key could have taken the reader: at 390 px in Chromium,
 *  "66 more rows below" on the outline drew its rows in its place, a hand-off landed on the first
 *  one's "more · 106 words", and that row, holding the focus, opened its body and drew its words
 *  whole. */
const IN_TAB_ORDER = ':not([tabindex="-1"])';

/** An element given a Tab stop of its own: any `tabindex` but -1. */
const TAB_STOP = `[tabindex]${IN_TAB_ORDER}`;

/** A disclosure's summary: the first `<summary>` of a `<details>`, which the keyboard reaches as
 *  it reaches a button, to open and close its disclosure. */
const SUMMARY = "details > summary:first-of-type";

/** What the keyboard focus can reach: a link, a disclosure's summary, a form control that is not
 *  `disabled`, each left in the Tab order (`IN_TAB_ORDER`), or anything given a Tab stop of its
 *  own. A control refused with `aria-disabled` is one, since it keeps its Tab stop. */
const FOCUSABLE = [
  `a[href]${IN_TAB_ORDER}`,
  `${SUMMARY}${IN_TAB_ORDER}`,
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
 *  on a later render, after the reader has moved it on (the issue title's editor). */
export function focusOnMount(element: HTMLElement | null): void {
  element?.focus();
}

/** Where a hand-off sends the focus a control took away with it, nearest first: the control now
 *  at `index` in `controls` where it stands at the lost control's place (`atPlace`), then each
 *  control before that place, walking back. `index` is the lost control's place among `controls`
 *  as they stand now: how many of them come before the point it left. A component's hand-off
 *  counts the place of a control taken away off the spot it went from, once it has gone
 *  (`placeLeft`), so however many controls went together, the walk starts where they were. */
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
 *  card's Unfollow, not on the next question's own checkbox. */
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
 *  that container's focusable controls, and where it stood in that container (`spotsOf`), which
 *  also say the rows it stood in there (`rowsIn`): the place and the spots are read as it takes
 *  the focus and again after each commit while it holds it. The same is read of the region the
 *  container is in (`around`). */
interface Place {
  /** Where the element stood in the region its container is in (`RegionPlace`), else null where
   *  no region holds it. */
  readonly around: RegionPlace | null;
  readonly container: Element;
  readonly element: Element;
  index: number;
  spots: readonly Spot[];
}

/** Where a component's focused control stood in the region its container is in
 *  (`handOffRegionAround`): the region, the control's place among the controls the focus can
 *  reach there (`placeIn`), and where it stood in it (`spotsOf`). Read as the control takes the
 *  focus, since a container that went has no ancestors left to say its region, and again after
 *  each commit while it holds the focus, for the hand-off's own walk back over the region
 *  (`walkBackInRegion`). */
interface RegionPlace {
  index: number;
  readonly region: HTMLElement;
  spots: readonly Spot[];
}

/** Where `element`, a control of `container`, stands in the region `container` is in
 *  (`RegionPlace`); null where no region holds the element. */
function regionPlaceOf(container: Element, element: Element): RegionPlace | null {
  const region = handOffRegionAround(container);
  if (region === null || !region.contains(element)) return null;
  return { index: placeIn(region, element).index, region, spots: spotsOf(element, region) };
}

/** The walk back a component's hand-off makes over the region its container was in where its
 *  container offers no control: among the controls the region draws that are its own
 *  (`drawnIn`), from the place `element` left there (`placeLeft`), to the control now at that
 *  place where it moved there from the element's own row, or from no row where the element stood
 *  in none (`atPlaceOf`), else to the nearest one before it, passing over every control that
 *  takes typed text (`walkBack`) and every control the reader cannot see now (`inSightNow`), and
 *  landed with `land`. Where none of them takes the focus, the region's own stop takes it with
 *  no scroll (`focusOwnStop`). A region no longer in the page and drawn (`present`) moves
 *  nothing. */
function walkBackInRegion(element: Element, around: RegionPlace): void {
  const { region, spots } = around;
  if (!present(region)) return;
  const drawn = drawnIn(region);
  const back = walkBack(
    drawn,
    placeLeft({ ...around, element }, region, drawn),
    atPlaceOf(rowsIn(spots), region)
  );
  const clips = new Map<Element, boolean>();
  for (const candidate of back) {
    if (inSightNow(candidate, clips) && land(candidate, clips)) return;
  }
  focusOwnStop(region);
}

/**
 * Keeps the keyboard focus off the page's body when a press takes away the element holding it
 * while the component around it stays - a page control that removes itself, a menu's trigger
 * that goes once its value is picked. The focus goes to the control now at that element's place
 * in its container where that control moved there from the element's own row, or from no row
 * where the element stood in none (`atPlaceOf`), else to the nearest one before that place,
 * passing over every control that takes typed text (`walkBack`): the reader never chose such a
 * field, and their next key would be typed into it. Where no other control stands there, the
 * focus goes to what `fallback` names for that container (a table row's fallback is the row
 * itself, and a row that has left the page names none). A fallback that takes typed text, as a
 * search box does, is passed over by the same rule, so it names no control.
 *
 * The hand-off keeps a sight rule as well. It passes over each of those controls, the fallback
 * included, that the reader cannot see now (`inSightNow`), and lands the first one in sight that
 * takes the focus with `land`. A control partly in sight is scrolled to its nearest edge, the
 * least distance that shows it whole. A control wholly in sight scrolls nothing. The browser's
 * own scroll of a focused control would pull the page back to one out of sight: the Delivery
 * page's **clear brush window**, with the Window control scrolled out above the window, scrolled
 * the page 46 to 147 px back to that control. Where no control in sight takes the focus, the
 * focus goes with no scroll to the own stop (`focusOwnStop`) of the region the container is in
 * (`handOffRegionOf`): the dialog or panel holding it, else the shell's main content. Where that
 * region is not in the page and drawn, nothing moves. Where neither the walk nor `fallback` names
 * any control - the container gone, as the project List's head goes with the table when a column
 * filter's pick sends the List to read its issues, or holding none the focus can reach, as the
 * Agents page's header with its select-all disabled - the hand-off walks back over the region the
 * container was in as the element took the focus, from the place the element left there
 * (`walkBackInRegion`).
 *
 * The component puts the handler this returns on its root as `onFocus`, which every focus inside
 * it bubbles to, from a portal too, since React bubbles through its own tree. `containerOf` names
 * the container a focused element is counted in, or null for one outside every container, whose
 * going moves nothing. A focus in a dialog, a panel or a selection bar drawn inside the container
 * (`inRegionWithin`) is no focus on a control of the component either, so its going moves nothing
 * here, and the hand-off never walks out of that region to the component's controls; in a
 * selection bar, the bar's own hand-off (`SelectionBar`) answers a loss the reader caused. An
 * element React bubbled up from a portal is no control of the container, so it is counted at the
 * place of the one counted before it there, in that one's spots, and not at all without one: a
 * nested panel's input is counted where the trigger that opened that panel is.
 *
 * The place is counted again once the element has gone, off the spot it went from in the
 * container (`placeLeft`): its place counted while it stood there takes in every control before
 * it that went with it, as an Inbox card's subscriber chip takes its name link away with its
 * Unsubscribe, and the walk would start at the control after them. Where no spot of it went from
 * a parent still in the container, the place counted while it stood there is used.
 *
 * The check runs after every commit of the component, so after the one that took the element
 * away, and moves the focus only when the element has left the page and the focus is on the
 * body. An element still in the page that no longer holds the focus is one the reader moved off,
 * so it is forgotten, and its going later moves nothing. Nor does the going of an element the
 * reader had scrolled out of their sight (`outOfSight`), read from the armed press's record: a
 * live update taking a control out of a row drawn beyond the view, or Mark all read going while
 * the reader is scrolled away, pulls neither a list nor the page back to the control at its
 * place. Nor does a going the reader did not cause (`causedByReader`): a loss is the reader's
 * only while a press that armed the hand-off is live (`armHandOff`), which is why the component
 * puts `armOnPress` on the same root as this handler, and any other loss leaves the focus on the
 * body with no scroll.
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
        const { around } = held;
        if (around?.region.contains(held.element)) {
          around.index = placeIn(around.region, held.element).index;
          around.spots = spotsOf(held.element, around.region);
        }
      }
      return;
    }
    place.current = null;
    if (held.element.isConnected || !focusOnDocument() || outOfSight(held.element)) return;
    if (!causedByReader(held.element)) return;
    let walked: HTMLElement[] = [];
    if (held.container.isConnected) {
      const controls = focusableIn(held.container);
      walked = walkBack(
        controls,
        placeLeft(held, held.container, controls),
        atPlaceOf(rowsIn(held.spots), held.container)
      );
    }
    const fallbackControl = fallback(held.container);
    const candidates =
      fallbackControl === null || takesText(fallbackControl)
        ? walked
        : [...walked, fallbackControl];
    if (candidates.length === 0) {
      if (held.around !== null) walkBackInRegion(held.element, held.around);
      return;
    }
    // One read of each box's style for the whole hand-off (`clippingBoxes`).
    const clips = new Map<Element, boolean>();
    for (const candidate of candidates) {
      if (inSightNow(candidate, clips) && land(candidate, clips)) return;
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
      place.current = carried === null ? null : { ...carried, element };
      return;
    }
    const { counted, index } = placeIn(container, element);
    place.current = {
      around: regionPlaceOf(container, element),
      container,
      element,
      index: counted ? index : (carried?.index ?? -1),
      spots: spotsOf(element, container),
    };
  };
}

/** An element of role `dialog`, modal or not - the search bar, the Inbox drawer, every `useDialog`
 *  dialog, the panel a `PanelTrigger` opens, a popover such as `ContextRing`'s - or any other
 *  panel an `AnchoredPanel` draws, which carries the `PANEL_ATTRIBUTE` (`key-targets.ts`): a
 *  `MultiSelect` list's, which has no role of its own. Each is a region of its own, and a press
 *  inside it is a press in it (`PRESS_REGION`). */
const DIALOG = `[role="dialog"], [${PANEL_ATTRIBUTE}]`;

/** Whether `element` is a row of a table or list or inside one: whether it or an element around
 *  it is a row by `row-marker.ts`'s one rule, `ROW_SELECTOR`: a press on a row's mark is read by
 *  it (`pressOnRowMark`). */
function inRow(element: Element): boolean {
  return element.closest(ROW_SELECTOR) !== null;
}

/** Every row around `element` by `row-marker.ts`'s one rule (`ROW_SELECTOR`), itself included,
 *  nearest first: rows nest, as an ask card's `<article>` in an Inbox row's `<li>`, and a loss
 *  is read against each of them (`pressPlaces`). */
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

/** Whether `element` is a row's mark: the one checkbox a row draws (`MARK`), inside a row
 *  (`inRow`). A press on one is read apart (`pressOnRowMark`), and the selection bar arms the
 *  hand-off for one (`SelectionBar`). */
export function isRowMark(element: Element): boolean {
  return inRow(element) && element.matches(MARK);
}

/** The regions of their own a focus can sit in: a dialog or a panel (`DIALOG`), or the selection
 *  bar. A press that empties a selection could land in one and still be the reader's own, and a
 *  focus in one drawn inside a component's container is that region's (`inRegionWithin`). */
const PRESS_REGION = `${DIALOG}, [data-control="${CONTROL.selectionBar}"]`;

/** Whether the page draws `element`: under no `hidden` ancestor, and given a box. */
function rendered(element: Element): boolean {
  return element.closest("[hidden]") === null && element.getClientRects().length > 0;
}

/** The controls the focus can reach in `region` that the page draws and that are the region's
 *  own - none of a dialog or a panel inside it (`DIALOG`) - since a loss is answered inside the
 *  region it was lost in. A hand-off's walk back over a region counts places among these and
 *  lands only on these (`walkBackInRegion`), so a control drawn at one width alone - `md:hidden`,
 *  say - neither holds a place nor is handed a focus it cannot show. */
function drawnIn(region: Element): HTMLElement[] {
  return focusableIn(region).filter(
    (control) => (control.closest(DIALOG) ?? region) === region && rendered(control)
  );
}

/** Whether `region` is still a place the focus can be answered in: in the page and drawn. A
 *  dialog kept mounted under `hidden` - the phone thread holding a reply - has gone as much as
 *  one taken away. */
function present(region: Element): boolean {
  return region.isConnected && rendered(region);
}

/** The region a component's container is in: the dialog or panel holding `container`
 *  (`DIALOG`), else the shell's main content - the `main` element around `container`, else the
 *  page's - else null. */
function handOffRegionAround(container: Element): HTMLElement | null {
  const region =
    container.closest(DIALOG) ?? container.closest("main") ?? document.querySelector("main");
  return region instanceof HTMLElement ? region : null;
}

/** The region whose own stop takes the focus where a component's hand-off finds no control in
 *  sight to land on (`useFocusPlace`): the one `container` is in (`handOffRegionAround`) while
 *  that region is in the page and drawn (`present`), else null. */
function handOffRegionOf(container: Element): HTMLElement | null {
  const region = handOffRegionAround(container);
  return region !== null && present(region) ? region : null;
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
 *  a frame, in INFRA-3135's builds Jobs `infra-3135-removed-4qsm9` and `infra-3135-removed2-nkzh5`,
 *  2026-11-22); an element hidden or disabled under the focus stays named until the task after the
 *  next frame. The element that has left the page is checked as well, so an engine that names it
 *  until its focus fixup is read the same way. The one check for a removed focus: a list
 *  (`MultiSelect`), as it closes over its own text box and after a press on one of its rows, and
 *  the Inbox's hand-off of a focus its reading column's question took with it (`Inbox.tsx`) ask
 *  it. */
export function focusLost(): boolean {
  const focused = document.activeElement;
  return focused === null || focused === document.body || !focused.isConnected;
}

/** Focuses `element` with no scroll of the engine's own and, once it holds the focus, scrolls it
 *  to its nearest edge unless the reader can see the whole of it: WebKit scrolls a focused control
 *  into view only a frame later, by when a list's own scroll keeping may have moved the page
 *  again, and Chromium and Firefox centre a control that is not wholly in view. The Inbox's
 *  hand-off after the reader's action (`pane-selection.ts`), which brings the item it focuses into
 *  view wherever it stood, and scrolls nothing for a row the reader already sees whole - the row
 *  that moves up into the place of the rows a press took away, which the list's own scroll
 *  keeping holds where the reader saw it (`ViewportAnchor`). A component's own hand-off scrolls a
 *  control only where the reader sees a part of it (`land`). Answers whether it took the focus. */
export function focusIntoView(element: HTMLElement): boolean {
  if (!focusNoScroll(element)) return false;
  if (!inSightWhole(element, new Map())) {
    element.scrollIntoView({ block: "nearest", inline: "nearest" });
  }
  return true;
}

/** Focuses `element` with no scroll of the engine's own, and, once it holds the focus, scrolls it
 *  to its nearest edge where the reader can see a part of it but not the whole: the least distance
 *  that shows it whole. A control wholly in sight, or wholly out of it, scrolls nothing. The
 *  engine's own scroll of a focused control follows the engine's own rule: Chromium and Firefox
 *  centre a control that is not wholly in view, and Firefox moved a control half cut by a dialog's
 *  top edge 405 px. WebKit scrolls a focused control only a frame later, by when a list's own
 *  scroll keeping may have moved the page again. So every engine lands a control the same way.
 *  `clips` is the caller's read of which boxes clip (`clippingBoxes`), where it made one: a focus
 *  changes no box's overflow. Answers whether it took the focus. */
function land(element: HTMLElement, clips = new Map<Element, boolean>()): boolean {
  element.focus({ preventScroll: true });
  if (document.activeElement !== element) return false;
  const sight = sightOf(clippingBoxes(element, clips), undefined);
  const drawn = boxOf(element, undefined);
  if (meets(drawn, sight) && !liesWithin(drawn, sight)) {
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

/** A box's edges, in pixels from the viewport's top left corner. */
interface Edges {
  readonly bottom: number;
  readonly left: number;
  readonly right: number;
  readonly top: number;
}

/** The box of `element`: where it will stand once `moved`'s row has moved up, where the element
 *  is inside that row, else where it stands now. */
function boxOf(element: Element, moved: MovedUp | undefined): Edges {
  const box = element.getBoundingClientRect();
  const by = moved?.row.contains(element) ? moved.by : 0;
  return { bottom: box.bottom - by, left: box.left, right: box.right, top: box.top - by };
}

/** What is in the reader's sight through `boxes`, the boxes around an element that can hide it
 *  (`Sight`): the viewport cut to each of them, a box inside `moved`'s row read where it will
 *  stand. */
function sightOf(boxes: Iterable<Element>, moved: MovedUp | undefined): Edges {
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

/** Whether `drawn` meets `sight`: a box whose edge only touches the edge of what is in sight
 *  counts as meeting it. */
function meets(drawn: Edges, sight: Edges): boolean {
  return (
    drawn.bottom >= sight.top &&
    drawn.top <= sight.bottom &&
    drawn.right >= sight.left &&
    drawn.left <= sight.right
  );
}

/** Whether `drawn` lies wholly within `sight`. */
function liesWithin(drawn: Edges, sight: Edges): boolean {
  return (
    drawn.top >= sight.top &&
    drawn.bottom <= sight.bottom &&
    drawn.left >= sight.left &&
    drawn.right <= sight.right
  );
}

/** Whether any part of `element` is in the reader's sight: its box meets the viewport and every
 *  box in `boxes`, the boxes around it that can hide it (`Sight`). An element whose edge only
 *  touches the edge of what is in sight counts as in sight. With `moved`, the element is read
 *  where it will stand once that row has moved up. */
function inSight(element: Element, boxes: Iterable<Element>, moved?: MovedUp): boolean {
  return meets(boxOf(element, moved), sightOf(boxes, moved));
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

/** The element the focus was last noted on while a press is armed, and whether the reader can
 *  still see it: read at every animation frame while the press is armed (`armPress`), against
 *  every box around it that clips it, which `boxes` keeps, while it is in the page and drawn. A
 *  hand-off reads it when it answers a loss of that element, which may have gone by then, so it
 *  holds what the last read before that saw (`outOfSight`). */
interface Sight {
  readonly boxes: Set<Element>;
  readonly element: HTMLElement;
  seen: boolean;
  /** Whether a read since the element took the focus has found it in sight: the read at the
   *  focus itself, or one at a frame after it (`readSight`). */
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

/** Whether the reader had scrolled `element` out of their sight, as the armed press last read it
 *  (`ArmedPress`'s `sight`): false for an element it has not noted, and with nothing armed. The
 *  two component hand-offs (`useFocusPlace`, `SelectionBar`) ask it before they move the focus,
 *  so neither pulls a list or the page back to a place the reader scrolled away from. A read at
 *  the loss would be wrong, since an element that has left the page has the viewport's top-left
 *  corner for its box, which counts as in sight. */
export function outOfSight(element: Element): boolean {
  const watched = armed?.sight ?? null;
  return watched?.element === element && !watched.seen;
}

/** A control that cannot be pressed now: held while its write is out (`aria-disabled`,
 *  `held-press.ts`), or disabled, itself or through the fieldset around it. */
const HELD = '[aria-disabled="true"], :disabled';

/** Whether `event` is a press of the reader's: what arms a component's hand-off at its root
 *  (`armOnPress`), and what ends an armed press (`armHandOff`). A pointer press is one, and so is
 *  any key on a control that takes no typed text - Enter, Space, Escape, the arrows, a page key.
 *  In a field that takes typed text (`takesText`) a character typed is none, whatever it is
 *  typed with, and only a key that acts on the page rather than on the text is one: Escape, Tab,
 *  Enter in a single-line `<input>`, which commits it, as the issue title's editor does, and a
 *  key held with Ctrl, Meta or Alt that types no character (`typesCharacter`), a shortcut such as
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

/** The longest an armed press's action keeps it live, counted from the arm (`armHandOff`): the
 *  app's write deadline (`SEND_DEADLINE_MS`), the longest its control's hold keeps it live, and
 *  then the request deadline (`REQUEST_DEADLINE_MS`), which bounds the hold through the re-read
 *  that answers the write (`useReadBound`). It is the longest one write that lands within its
 *  deadline, and then its re-read, can take, and it bounds every hold over the press the same
 *  way, counted from the same arm: an action of several writes, as the List's Start next, too. */
const ACTION_BOUND_MS = SEND_DEADLINE_MS + REQUEST_DEADLINE_MS;

/** The control that opened the panel `target` is in: the one naming that panel among what it
 *  opened (`aria-controls`, a list of ids), a menu's trigger or a list's. Null outside every panel.
 *  The panel is portalled out of the row the control stands in, so a row reads the focus in it as
 *  on that control (`ViewportAnchor`'s `interacted`). */
export function openerOf(target: Element): Element | null {
  const panel = target.closest(`[${PANEL_ATTRIBUTE}][id]`);
  return panel === null
    ? null
    : document.querySelector(`[aria-controls~="${CSS.escape(panel.id)}"]`);
}

/** The panel `trigger` names as what it opened (`aria-controls`), the other way from `openerOf`:
 *  a menu's, a list's, or the box a disclosure draws under it. Null while the trigger names none
 *  or the page holds no element of that id. */
export function panelOf(trigger: Element): HTMLElement | null {
  const id = trigger.getAttribute("aria-controls");
  return id === null ? null : document.getElementById(id);
}
/** The keys that only modify another key: one pressed alone acts on nothing, so it leaves an
 *  armed press armed (`armHandOff`). */
const LONE_MODIFIERS: Readonly<Record<string, true>> = {
  Alt: true,
  Control: true,
  Meta: true,
  Shift: true,
};

/** The press that armed the component hand-offs (`armHandOff`): what tells a loss the reader
 *  caused from one an update made under them (`causedByReader`, `pressOnRowMark`), and what the
 *  reader could see of the focused element while it was armed (`outOfSight`). */
interface ArmedPress {
  /** The holds on what answers the press, begun while it is armed, that are still on
   *  (`holdHandOff`). */
  readonly answers: Set<object>;
  /** Reads again whether anything still keeps the press armed; nothing once it has ended. */
  changed: () => void;
  /** The control the press landed on: the nearest of the element it landed on and the elements
   *  around it that a row holds as a control by markup (`ROW_CONTROL`) - the button a span or an
   *  icon of it is drawn in - else that element. */
  readonly control: Element;
  /** Ends the press now: its timers, its watch, its listeners and its frame loop stop. */
  readonly end: () => void;
  /** Whether `control` was already held (`HELD`) as the press was made: that hold took the press
   *  without a write, so it keeps the press armed no longer than a press on a control held by
   *  nothing. */
  readonly heldBefore: boolean;
  /** The focused element and the dialog, panel or selection bar around it (`PRESS_REGION`), read
   *  at the arm and at every focus while the press is armed, which `pressRegionOf` reads for an
   *  element that has left the page. */
  note: { readonly element: Element; readonly region: Element | null } | null;
  /** The roots the press was armed at (`armOnPress`): the root of each component that hands its
   *  focus off whose React tree the press bubbled through. A press in a portal - a pick in a list
   *  a control of the component opened, which the page draws outside it - is a press inside that
   *  component all the same, as React reads it, so a place that holds one of these holds the
   *  press (`causedByReader`): the bulk Snooze picked in the Inbox bar's list is a press in the
   *  bar, whose going after the write lands is the reader's. */
  readonly roots: Set<Element>;
  /** What the reader can see of the focused element (`Sight`), read at the arm and at every
   *  animation frame while the press is armed, while that element is in the page and drawn; null
   *  while no element has held the focus since the arm. */
  sight: Sight | null;
}

/** The armed press while one is (`armHandOff`), else null. */
let armed: ArmedPress | null = null;

/**
 * Arms the component hand-offs for the reader's press on `pressed`: a loss counts as the reader's
 * only while a press that armed them is live, and with nothing armed a loss is answered as the
 * page answered it before it had any hand-off, on the body with nothing scrolled
 * (`causedByReader`). A component calls it at its own root through `armOnPress`, which hands it
 * that root (`root`); the selection bar also arms it for a press on a row's mark
 * (`SelectionBar`), with no root.
 *
 * The press records the control it landed on (`ArmedPress`'s `control`), the root of the
 * component it was armed at (`roots`), the region it was made in (`madeIn`: the dialog, panel or
 * selection bar around that control, `PRESS_REGION`, else the main element holding it, else
 * none), whether that control was already held (`heldBefore`), and the focused element with the
 * region around it (`note`). A second press while one is armed ends the one before it, save two,
 * which add only the root they are armed at. A press on the armed control again - a checkbox's
 * `click` and then its `change`, or the same click heard at a component's root inside another's -
 * restarts nothing. Nor does a press on a control the armed press holds now by a hold that began
 * after it, or on anything inside that control (`HELD`): a double click or a repeated Enter on a
 * held control takes the press without a write, so the first press stays the cause, with its
 * bounds as they were.
 *
 * The press is live from the arm until a frame and a task after the last of what keeps it live
 * ends (`afterComponents`), so the commit that draws its answer and the effects after it are
 * still its own. A press that holds nothing ends a frame and a task after the arm. A hold on what
 * answers it, begun while it is live (`holdHandOff`), keeps it live until the hold ends. Its
 * control held by a hold that began after the arm keeps it live too, up to the app's write
 * deadline (`SEND_DEADLINE_MS`) from the arm: watched rather than polled, with a watch on the
 * page's `aria-disabled`, `disabled` and children, so the control going, its fieldset enabled
 * again or its write refused ends that hold. Nothing pulls the focus back to a control a refused
 * write lets go. At `ACTION_BOUND_MS` from the arm the press stops counting whatever is still on
 * and ends a frame and a task later.
 *
 * The reader's next act disarms it, read by listeners on the document installed only while a
 * press is armed: a pointer press anywhere outside the armed control, and any key that is a press
 * of theirs (`isPress`) and no modifier pressed alone, other than a key on the armed control.
 * Tab, with Shift or without, moves the focus off that control to another, so it disarms the
 * press as well, save while the press's write is out - a hold on what answers it on, or its
 * control held as above - where it waits for the focus to land (`focusin`, or the Tab's own
 * `keyup`): a landing inside the region the press was made in keeps it live, since that region's
 * later going is the press's own doing, as the Inbox's Decline dialog goes with its row when the
 * decline lands, and a landing anywhere else disarms it. Escape arms nothing at a component's
 * root (`armOnPress`): a control it takes away is the reader leaving it. The selection bar alone
 * arms on it (`SelectionBar`), whose hand-off counts Escape from a control inside it as the
 * reader's.
 *
 * While armed, the press reads at every animation frame what the reader can see of the focused
 * element (`readSight`): an element never yet in sight is not counted as scrolled away, since
 * WebKit scrolls a focused control into view a frame after the focus. The read stops while that
 * element is not in the page and drawn, so what the last read before it went saw stands, and the
 * loop stops with the press.
 */
export function armHandOff(pressed: Element, root?: Element): void {
  const control = pressed.closest(ROW_CONTROL) ?? pressed;
  const before = armed;
  if (
    before !== null &&
    (before.control === control ||
      (!before.heldBefore && heldControl(before.control)?.contains(pressed) === true))
  ) {
    if (root !== undefined) before.roots.add(root);
    return;
  }
  before?.end();
  armed = armPress(control, root);
}

/** The React handler a component that hands its focus off puts on its root, the one that carries
 *  its `useFocusPlace` handler, as `onClickCapture`, `onChangeCapture` and `onKeyDownCapture`: it
 *  arms the hand-off for the element a press landed on (`armHandOff`), at that root, before the
 *  press's own handler runs. A pick fires `change`, and Enter and Space on a button fire `click`;
 *  a `change` that is no press - the `input` React reports as a text field's change, at every
 *  character typed - arms nothing (`isPress`). A key arms where it is a press of a control that
 *  acts at the key itself rather than at a `click`: Enter on any element, as a list's text box
 *  picks its active row at Enter's `keydown` (`MultiSelect`), and Space on one that takes no
 *  typed text (`takesText`), where it presses rather than types; any other key arms nothing, and
 *  Escape none either, since a control it takes away is the reader leaving it. So a keyboard
 *  reader arms what a pointer reader arms. It makes nothing interactive. */
export function armOnPress(event: SyntheticEvent): void {
  const pressed = event.nativeEvent;
  if (pressed instanceof KeyboardEvent) {
    const { key, target } = pressed;
    if (!(target instanceof Element)) return;
    if (key === "Enter" || (key === " " && !takesText(target))) {
      armHandOff(target, event.currentTarget);
    }
    return;
  }
  if (isPress(pressed)) armHandOff(pressed.target, event.currentTarget);
}

/** Begins a hold on what answers the armed press (`armHandOff`): begun while that press is live,
 *  it keeps the press live until the function it answers runs, up to `ACTION_BOUND_MS` from the
 *  arm. Begun while nothing is armed, it does nothing. Every hold on what answers a write begins
 *  one: the control held while the write is out and through the commit after its answer
 *  (`useHeldThroughAnswer`), through the re-read that draws what the write changed
 *  (`useReadBound`), until the update that carries the write is drawn (`useHoldUntilDrawn`), and
 *  through the whole run of a project List action (`issues-table-actions.ts`'s `perform`). So the
 *  row or the form that what answers the write takes away is still the press's doing
 *  (`causedByReader`). Answers what ends the hold; ending it again does nothing. */
export function holdHandOff(): () => void {
  const press = armed;
  if (press === null) return () => undefined;
  const hold = {};
  press.answers.add(hold);
  press.changed();
  return () => {
    if (press.answers.delete(hold)) press.changed();
  };
}

/** A new record of what the reader can see of `element` (`Sight`), read now against every box
 *  around it that clips it (`clippingBoxes`): seen, and shown where any of it is in sight now. */
function sightOfFocused(element: HTMLElement): Sight {
  const boxes = new Set(clippingBoxes(element));
  return { boxes, element, seen: true, shown: inSight(element, boxes) };
}

/** Arms a press on `control`, armed at `root` where a component's root armed it (`armHandOff`):
 *  starts its timers, its watch, its listeners and its frame loop, and answers it. */
function armPress(control: Element, root: Element | undefined): ArmedPress {
  const heldBefore = isHeld(control);
  const madeIn = control.closest(PRESS_REGION) ?? control.closest("main");
  /** What cancels the press's end, from the moment nothing keeps it live until the end runs. */
  let ending: (() => void) | null = null;
  /** Whether the write deadline has passed: from then on the control's hold counts no longer. */
  let late = false;
  /** Whether a Tab of the reader's is still to land while the press's write is out. */
  let tabbing = false;
  /** The animation frame the next read of what the reader can see waits for, else 0. */
  let frame = 0;
  /** Whether the press has ended: from then on none of its timers, watches or listeners acts. */
  let over = false;
  let watching = false;
  const watcher = new MutationObserver(() => press.changed());
  /** Whether the press's write is out: a hold on what answers it is on, or, up to the write
   *  deadline, its control is held by a hold that began after the arm. */
  const writeOut = () => press.answers.size > 0 || (!late && !heldBefore && isHeld(control));
  const read = () => {
    // The watch on the control's hold runs only while that hold alone can keep the press live.
    if (press.answers.size > 0 || late) {
      watcher.disconnect();
      watching = false;
    } else if (!watching) {
      watching = true;
      watcher.observe(document.body, {
        attributeFilter: ["aria-disabled", "disabled"],
        childList: true,
        subtree: true,
      });
    }
    if (writeOut()) {
      ending?.();
      ending = null;
      return;
    }
    ending ??= afterComponents(end);
  };
  const look = () => {
    frame = 0;
    const watched = press.sight;
    if (over || watched === null || !watched.element.isConnected || !rendered(watched.element)) {
      return;
    }
    readSight(watched);
    frame = window.requestAnimationFrame(look);
  };
  const noteFocused = (element: Element) => {
    press.note = { element, region: element.closest(PRESS_REGION) };
    if (!(element instanceof HTMLElement) || press.sight?.element === element) return;
    press.sight = sightOfFocused(element);
    if (frame === 0) frame = window.requestAnimationFrame(look);
  };
  const settleTab = (landing: Element | null) => {
    if (!tabbing) return;
    tabbing = false;
    if (landing === null || madeIn?.contains(landing) !== true) end();
  };
  const onPointer = (event: PointerEvent) => {
    if (!(event.target instanceof Node && control.contains(event.target))) end();
  };
  const onKey = (event: KeyboardEvent) => {
    if (!isPress(event) || LONE_MODIFIERS[event.key] === true) return;
    if (event.key === "Tab") {
      if (madeIn !== null && writeOut()) tabbing = true;
      else end();
    } else if (!control.contains(event.target)) {
      end();
    }
  };
  const onKeyUp = (event: KeyboardEvent) => {
    if (event.key === "Tab") settleTab(document.activeElement);
  };
  const onFocus = (event: globalThis.FocusEvent) => {
    const { target } = event;
    if (!(target instanceof Element)) return;
    settleTab(target);
    if (armed === press) noteFocused(target);
  };
  const end = () => {
    if (over) return;
    over = true;
    press.changed = () => undefined;
    watcher.disconnect();
    window.clearTimeout(deadline);
    window.clearTimeout(bound);
    ending?.();
    window.cancelAnimationFrame(frame);
    document.removeEventListener("pointerdown", onPointer, true);
    document.removeEventListener("keydown", onKey, true);
    document.removeEventListener("keyup", onKeyUp, true);
    document.removeEventListener("focusin", onFocus, true);
    if (armed === press) armed = null;
  };
  const press: ArmedPress = {
    answers: new Set(),
    changed: () => read(),
    control,
    end,
    heldBefore,
    note: null,
    roots: new Set(root === undefined ? [] : [root]),
    sight: null,
  };
  const deadline = window.setTimeout(() => {
    late = true;
    press.changed();
  }, SEND_DEADLINE_MS);
  const bound = window.setTimeout(() => {
    if (over) return;
    press.changed = () => undefined;
    watcher.disconnect();
    ending?.();
    ending = afterComponents(end);
  }, ACTION_BOUND_MS);
  document.addEventListener("pointerdown", onPointer, true);
  document.addEventListener("keydown", onKey, true);
  document.addEventListener("keyup", onKeyUp, true);
  document.addEventListener("focusin", onFocus, true);
  const focused = document.activeElement;
  if (focused !== null && focused !== document.body) noteFocused(focused);
  read();
  return press;
}

/** The dialog, panel or selection bar (`PRESS_REGION`) `element`'s focus was lost from: the one
 *  around it while it is in the page, and for one that has left the page the one the armed press
 *  noted around it as it took the focus (`ArmedPress`'s `note`), since the region around an
 *  element that has gone has gone from it unless it went with it. So the going a component's
 *  hand-off reads after its commit (`useFocusPlace`, `SelectionBar`) is read in the region the
 *  element was in. */
function pressRegionOf(element: Element): Element | null {
  const note = armed?.note ?? null;
  if (!element.isConnected && note?.element === element) return note.region;
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
 * Whether the reader caused the loss of `element`'s focus: the armed press (`armHandOff`) is
 * still live, and one of `element`'s places (`pressPlaces`) - the element, any row it was in, or
 * the dialog, panel or selection bar it was lost from - is the press's control or holds it, or
 * holds a root the press was armed at (`ArmedPress`'s `roots`), as a pick in a list portalled out
 * of the selection bar is a press in the bar. A loss the reader caused keeps the hand-off that
 * pulls the focus to the control now at the lost one's place; one they did not - a live update
 * taking a row away, a refetch emptying a bar - and every loss with nothing armed leaves the focus
 * on the body, and scrolls nothing. Every component hand-off that can move the focus to another
 * row reads this one rule; a hand-off to the lost control's own row, as AskCard's is, is no move
 * to another row.
 */
export function causedByReader(element: Element): boolean {
  const press = armed;
  if (press === null) return false;
  return pressPlaces(element).some((place) => {
    if (place.contains(press.control)) return true;
    for (const root of press.roots) {
      if (place.contains(root)) return true;
    }
    return false;
  });
}

/** Whether the armed press (`armHandOff`) was on a row's mark, while that mark is still in the
 *  page: the selection bar reads this beside `causedByReader`, since unticking the last row by
 *  its mark empties the bar while the focus may rest in it (a checkbox a click leaves unfocused,
 *  as WebKit does). The bar arms the hand-off for a press on a row's mark itself (`SelectionBar`):
 *  unticking a mark leaves its row where it was, and a mark gone from the page with its row is no
 *  mark the reader can have just pressed. */
export function pressOnRowMark(): boolean {
  return armed?.control.isConnected === true && isRowMark(armed.control);
}

/** Where a node stood in the page while it was there: the parent holding it and the siblings
 *  either side of it, `previous` and `next`, null at either end. */
interface Spot {
  readonly next: Node | null;
  readonly node: Node;
  readonly parent: Node;
  readonly previous: Node | null;
}

/** Where `element` stands in `within` - a component's container, or the region it is in - and
 *  where each parent of it up to `within`'s own children stands, nearest first: read while the
 *  element is still in the page, so that the spot a press later takes it, or a parent of it,
 *  away from is known (the place counted off it in `placeLeft`), and so are the rows around it
 *  (`rowsIn`). */
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
 *  component's container, or the region it is in (`placeLeft`). */
function goneFrom(spot: Spot, within: Element): boolean {
  return !spot.node.isConnected && spot.parent.isConnected && within.contains(spot.parent);
}

/** What a row holds as its controls, by markup alone: what the focus can reach (`FOCUSABLE`), and
 *  a button or form field disabled now as well, which keeps its place in the row (`rowControls`).
 *  The control a press landed on is the nearest of these around it (`armHandOff`). */
const ROW_CONTROL = [
  ...["a[href]", SUMMARY, ...FORM_FIELDS].map((tag) => `${tag}${IN_TAB_ORDER}`),
  TAB_STOP,
].join(", ");

/** The controls `row` holds, in order, by markup alone (`ROW_CONTROL`): the row itself first
 *  where it is one, then each inside it. Counted by markup and not by what the page draws, so
 *  the same place in two rows names the same control whatever either draws (`controlsLikeIn`). */
function rowControls(row: Element): Element[] {
  const inside = [...row.querySelectorAll(ROW_CONTROL)];
  return row.matches(ROW_CONTROL) ? [row, ...inside] : inside;
}

/** The longest an armed press waits for an animation frame before it goes on without one
 *  (`afterComponents`). WebKit can hold a page's frames back while nothing on it is drawn -
 *  measured in headless WebKit, one wait for a frame lasted 775 ms, and another had none in 2 s
 *  while the page's timers ran - and a press is not to stay live that long past its answer. */
export const FRAME_WAIT_MS = 100;

/** Runs `then` once the components have had their turn with what a press did: a frame, or
 *  `FRAME_WAIT_MS` where no frame comes, and then a task later. Answers what cancels it.
 *
 *  That is after the commit a press of the reader's makes and after its effects: React commits
 *  an update made in an event of theirs before the next paint and runs its passive effects at
 *  the end of that commit. A commit at normal priority - one an update outside any event makes,
 *  as a write's answer or a read's own bound does - runs its passive effects in a task of
 *  React's scheduler, which can come after this. A component whose hand-off must run in such a
 *  commit commits it synchronously, so the hand-off runs with it: the project List's bar goes
 *  that way (`features/project/issues-table-actions.ts`). Once cancelled it does nothing, even
 *  where a timer it set fires after all: a cancel made under another clock than the one the
 *  timer was set on - a unit test's fake clock, installed or put back between the two - leaves
 *  that timer set, and a zero-delay timer set on the fake clock and left there holds up every
 *  zero-delay timer after the real clock is back. An armed press can outlive the test that made
 *  it (`armHandOff`), so its end is cancelled under the next test's clock. */
function afterComponents(then: () => void): () => void {
  let task: number | undefined;
  let cancelled = false;
  const go = () => {
    if (task !== undefined || cancelled) return;
    window.cancelAnimationFrame(frame);
    window.clearTimeout(late);
    task = window.setTimeout(() => {
      if (!cancelled) then();
    }, 0);
  };
  const frame = window.requestAnimationFrame(go);
  const late = window.setTimeout(go, FRAME_WAIT_MS);
  return () => {
    cancelled = true;
    window.cancelAnimationFrame(frame);
    window.clearTimeout(late);
    window.clearTimeout(task);
  };
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
 *  on a control the reader cannot see whole (`AgentsPage`'s `focusAfterPin`). */
export function controlsLikeRow(row: Element, control: Element, from: Element): HTMLElement[] {
  return controlsLikeIn(
    row,
    rowControls(from).indexOf(control),
    control,
    focusableIn(row).filter(rendered)
  );
}

/** Focuses `target` without a scroll: a region's own stop (`focusOwnStop`), and the control the
 *  Agents page hands a pin's moved focus to (`controlsLikeRow`). Answers whether it took the
 *  focus. */
export function focusNoScroll(target: HTMLElement): boolean {
  target.focus({ preventScroll: true });
  return document.activeElement === target;
}

/** Focuses `region` at its own stop without a scroll. A region with no Tab stop of its own is
 *  given `tabindex="-1"` for it. A component's hand-off ends there where no control in sight
 *  takes the focus (`useFocusPlace`, `walkBackInRegion`), and so does the selection bar's
 *  hand-off in a list (`focusListStop`). */
export function focusOwnStop(region: HTMLElement): void {
  if (!region.hasAttribute("tabindex")) region.tabIndex = -1;
  focusNoScroll(region);
}

/** The controls a loss the reader caused walks back to, nearest first: the control now at `index`
 *  in `controls`, then each control before that place (`placesFrom`), save every control that
 *  takes typed text (`takesText`). The reader never chose such a field, and their next key would
 *  be typed into it: Delete group on the delivery editor's only group would walk back to the "New
 *  group name" box. The walk passes over it to the nearest control before it that takes no text.
 *  Where only such fields stand there, it offers nothing: a component's hand-off then tries its
 *  `fallback`, else walks back over its region, else lands on that region's own stop
 *  (`useFocusPlace`). A component's hand-off also passes over every control the walk offers that
 *  the reader cannot see now (`inSightNow`), as it reaches it: a landing scrolls its control into
 *  view (`land`), and the nearest control before the lost place can lie far from where the
 *  reader is. */
function walkBack(
  controls: readonly HTMLElement[],
  index: number,
  atPlace: (control: Element) => boolean
): HTMLElement[] {
  return placesFrom(controls, index, atPlace).filter((control) => !takesText(control));
}

/** Whether the reader can see any part of `element` now: its box meets the viewport and every box
 *  around it that clips it (`clippingBoxes`, which reads each box's style once in `clips`). A
 *  component's hand-off (`useFocusPlace`) reads it of each control its walk back reaches, as it
 *  reaches it, and of its fallback too, and the selection bar's hand-off reads it of each row
 *  mark it tries (`SelectionBar`'s `markShown`), with `moved` where the mark's row will move up
 *  once the rows a press is taking away have gone. */
export function inSightNow(
  element: Element,
  clips: Map<Element, boolean>,
  moved?: MovedUp
): boolean {
  return inSight(element, clippingBoxes(element, clips), moved);
}

/** Whether the reader can see the whole of `element` now: its box lies within the viewport and
 *  within every box around it that clips it (`clippingBoxes`, which reads each box's style once
 *  in `clips`). A landing partly in sight and not wholly in it is scrolled to its nearest edge
 *  (`land`); one wholly in sight scrolls nothing, and the Agents page reads it of the control a
 *  pin's move left the focus on. */
export function inSightWhole(element: Element, clips: Map<Element, boolean>): boolean {
  return liesWithin(boxOf(element, undefined), sightOf(clippingBoxes(element, clips), undefined));
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

/** The place among `controls`, the controls the focus can reach in `within` now, that `lost`
 *  left: a component's control in its container, or in the region its container is in
 *  (`useFocusPlace`, `walkBackInRegion`). It is where the control still stands, for one hidden
 *  rather than taken away (`countBefore`), else off the spot it went from, the nearest of its
 *  spots whose node has gone from a parent still in `within` (`goneFrom`, `placeOfRemoval`), else
 *  the place counted while it stood there. That count was made while the control still stood in
 *  the page, so it takes in every control before it that went or was hidden with it since: an
 *  Inbox card's subscriber chip takes its name link away with its Unsubscribe. */
function placeLeft(
  lost: { readonly element: Element; readonly index: number; readonly spots: readonly Spot[] },
  within: Element,
  controls: readonly Element[]
): number {
  if (lost.element.isConnected) return countBefore(controls, lost.element);
  const spot = lost.spots.find((each) => goneFrom(each, within));
  return spot === undefined ? lost.index : placeOfRemoval(controls, spot);
}
