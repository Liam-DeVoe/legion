import { type FocusEvent, type RefObject, useEffect, useLayoutEffect, useRef } from "react";

import { focusOnDocument } from "../features/shell/roving";
import { SEND_DEADLINE_MS } from "../features/writes/keyed-write";
import { CONTROL } from "./control-names";

/** What the keyboard focus can reach: a link, a form control that is not `disabled`, or anything
 *  given a Tab stop of its own. A control refused with `aria-disabled` is one, since it keeps its
 *  Tab stop. */
const FOCUSABLE = [
  "a[href]",
  "button:not(:disabled)",
  "textarea:not(:disabled)",
  "input:not(:disabled)",
  "select:not(:disabled)",
  '[tabindex]:not([tabindex="-1"])',
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
 *  at `index` in `controls`, then each control before that place, walking back. However many
 *  controls went together, the focus lands where they were, and by the same walk in every engine
 *  whether the loss was counted while they were still in the page (Chromium's `focusout`) or
 *  after they went (the removal Firefox and WebKit report alone). */
function placesFrom(controls: readonly HTMLElement[], index: number): HTMLElement[] {
  const before = controls.slice(0, Math.min(index, controls.length)).reverse();
  const at = controls[index];
  return at === undefined ? before : [at, ...before];
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

/** Where the keyboard focus sat: the element, the container it is counted in, and its place
 *  among that container's focusable controls. */
interface Place {
  readonly container: Element;
  readonly element: Element;
  index: number;
}

/**
 * Keeps the keyboard focus off the page's body when a press takes away the element holding it
 * while the component around it stays - a page control that removes itself, a menu's trigger
 * that goes once its value is picked. The focus goes to the control now at that element's place
 * in its container, else to the nearest one before it, else to what `fallback` names for that
 * container (a table row's fallback is the row itself, and a row that has left the page names none).
 *
 * The component puts the handler this returns on its root as `onFocus`, which every focus inside
 * it bubbles to, from a portal too, since React bubbles through its own tree. `containerOf` names
 * the container a focused element is counted in, or null for one outside every container, whose
 * going moves nothing. An element React bubbled up from a portal is no control of the container,
 * so it is counted at the place of the one counted before it there, and not at all without one:
 * a nested panel's input is counted where the trigger that opened that panel is.
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
 * answers an uncaused one into no row and with no scroll.
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
      }
      return;
    }
    place.current = null;
    if (held.element.isConnected || !focusOnDocument() || outOfSight(held.element)) return;
    if (!causedByReader(held.element)) return;
    const placed = held.container.isConnected
      ? placesFrom(focusableIn(held.container), held.index)[0]
      : undefined;
    (placed ?? fallback(held.container))?.focus();
  });
  return (event) => {
    const element = event.target;
    const container = containerOf(element);
    if (container === null) {
      place.current = null;
      return;
    }
    const before = place.current;
    const carried = before?.container === container ? before.index : null;
    if (!container.contains(element)) {
      // A portal's element: at the place of the control counted before it, or nowhere.
      place.current = carried === null ? null : { container, element, index: carried };
      return;
    }
    const { counted, index } = placeIn(container, element);
    place.current = { container, element, index: counted ? index : (carried ?? -1) };
  };
}

/** A dialog, modal or not: the search bar, the Inbox drawer, every `useDialog` dialog, and the
 *  panel a `PanelTrigger` opens. */
const DIALOG = '[role="dialog"]';

/** What a row of a table or list is: a `<tr>`, anything given a row role, or an Inbox row. A loss
 *  the reader did not cause lands in none of these, and in no control inside one. */
const ROW = 'tr, [role="row"], [data-inbox-row]';

/** The one checkbox a row draws for its mark (the project List's `Select <key>`, the Inbox's
 *  mark): the reader empties a selection from a row by it, from the bar itself otherwise. */
const MARK = 'input[type="checkbox"]';

/** The region a press that empties a selection could land in and still be the reader's own: a
 *  dialog or a panel (both `role="dialog"`), or the selection bar. */
const PRESS_REGION = `${DIALOG}, [data-control="${CONTROL.selectionBar}"]`;

/** How long the rescue waits for a control a press disabled to be enabled again before it hands
 *  the focus on as if that control had gone: the app's write deadline, past which a write is
 *  late and a composer lets go of what it holds the reader to. A slow send never ends the wait
 *  before its composer's own take-back. */
const RESCUE_WAIT_MS = SEND_DEADLINE_MS;

/** Whether the page draws `element`: under no `hidden` ancestor, and given a box. */
function rendered(element: Element): boolean {
  return element.closest("[hidden]") === null && element.getClientRects().length > 0;
}

/** The region a control is counted in: the dialog holding it, modal or not, else the shell's main
 *  content (`main`), else the document. */
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
 *  page. WebKit and Firefox move the focus off a removed element only at the next rendering
 *  update (the HTML spec's focus fixup), so until then the document still names it. The one
 *  check for a removed focus: the rescue below asks it, and so does a list closing over its own
 *  text box (`MultiSelect`). */
export function focusLost(): boolean {
  const focused = document.activeElement;
  return focused === null || focused === document.body || !focused.isConnected;
}

/** Focuses `element` and, once it holds the focus, scrolls it into view: WebKit scrolls a focused
 *  control into view only a frame later, by when a list's own scroll keeping may have moved the
 *  page again. Answers whether it took the focus. */
function land(element: HTMLElement): boolean {
  element.focus();
  if (document.activeElement !== element) return false;
  element.scrollIntoView({ block: "nearest", inline: "nearest" });
  return true;
}

/** Whether any part of `element` is in the reader's sight: its box meets the viewport and every
 *  box in `boxes`, the boxes around it that can hide it (`Sight`). An element whose edge only
 *  touches the edge of what is in sight counts as in sight. */
function inSight(element: Element, boxes: Iterable<Element>): boolean {
  let top = 0;
  let left = 0;
  let bottom = window.innerHeight;
  let right = window.innerWidth;
  for (const box of boxes) {
    const edges = box.getBoundingClientRect();
    top = Math.max(top, edges.top);
    left = Math.max(left, edges.left);
    bottom = Math.min(bottom, edges.bottom);
    right = Math.min(right, edges.right);
  }
  const drawn = element.getBoundingClientRect();
  return drawn.bottom >= top && drawn.top <= bottom && drawn.right >= left && drawn.left <= right;
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
 *  viewport's. */
function clippingBoxes(element: Element): Element[] {
  const boxes: Element[] = [];
  for (
    let box = element.parentElement;
    box !== null && box !== document.body && box !== document.documentElement;
    box = box.parentElement
  ) {
    const { overflowX, overflowY } = getComputedStyle(box);
    if (CLIPPING_OVERFLOW[overflowX] || CLIPPING_OVERFLOW[overflowY]) boxes.push(box);
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
 *  rescue is mounted. The component hand-offs read it too, so one record decides for all. */
let sight: Sight | null = null;

/** Whether the reader had scrolled `element` out of their sight, as the page's rescue last read
 *  it: false for an element the rescue has not noted, and wherever no rescue is mounted. Every
 *  hand-off asks this of the element that went before it moves the focus, so none pulls a list
 *  or the page back to a place the reader scrolled away from (`useFocusPlace`, `SelectionBar`). */
export function outOfSight(element: Element): boolean {
  return sight?.element === element && !sight.seen;
}

/** Whether a focus rescue is mounted, so the page is recording the reader's presses. Where none
 *  is - a component read in a unit test without the shell - every loss keeps the base hand-off,
 *  as it did before this rule. */
let rescueMounted = false;

/** The reader's latest act, a `pointerdown`, a `keydown` or a `change` they committed, with when
 *  it was made: what tells a loss the reader caused from one an update made under them. */
let lastPress: { at: number; target: Element } | null = null;

/** The reader's latest press while it is still recent enough to have caused a loss: within the
 *  rescue's wait (`RESCUE_WAIT_MS`), the bound a write a press started can take. */
function recentPress(): { target: Element } | null {
  if (lastPress === null) return null;
  return performance.now() - lastPress.at <= RESCUE_WAIT_MS ? lastPress : null;
}

/** The places a press could have taken `element`'s focus from and still be the reader's own doing:
 *  the element itself, the row it is in, and the dialog, panel or selection bar it was lost from. */
function pressPlaces(element: Element): Element[] {
  const places: Element[] = [element];
  const row = element.closest(ROW);
  if (row !== null) places.push(row);
  const region = element.closest(PRESS_REGION);
  if (region !== null) places.push(region);
  return places;
}

/**
 * Whether the reader caused the loss of `element`'s focus: an act of theirs - a `pointerdown`, a
 * `keydown`, or a `change` they committed (a select's pick) - came within the wait before it and
 * landed on the element, inside the row it was in, or inside the dialog, panel or selection bar it
 * was lost from (`pressPlaces`). A loss the
 * reader caused keeps the hand-off that pulls the focus to the control now at the lost one's
 * place; one they did not - a live update taking a row away, a refetch emptying a bar - moves the
 * focus into no row and scrolls nothing. The rescue and every component hand-off that can move the
 * focus to a row read this one rule.
 */
export function causedByReader(element: Element): boolean {
  if (!rescueMounted) return true;
  const press = recentPress();
  if (press === null) return false;
  return pressPlaces(element).some(
    (place) => place === press.target || place.contains(press.target)
  );
}

/** Whether the reader's latest press, within the wait, was on a row's mark: the selection bar
 *  reads this beside `causedByReader`, since unticking the last row by its mark empties the bar
 *  while the focus may rest in it (a checkbox a click leaves unfocused, as WebKit does). */
export function pressOnRowMark(): boolean {
  const press = recentPress();
  return press !== null && press.target.closest(ROW) !== null && press.target.matches(MARK);
}

/** Answers a loss the reader can no longer see, in a region still in the page: nothing scrolls,
 *  and the focus goes to no control the reader did not pick. It stays where it is, on the body,
 *  except in a dialog, which takes the focus itself so that the page behind a modal one is
 *  never reached - given `tabindex="-1"` for it where it has no Tab stop of its own, as when
 *  it loses every control. */
function leaveUnseen(region: Element): void {
  if (!(region instanceof HTMLElement) || !region.matches(DIALOG)) return;
  if (!region.hasAttribute("tabindex")) region.tabIndex = -1;
  region.focus({ preventScroll: true });
}

/** A focus an element lost: the element, the region it was counted in, its place there among
 *  the controls the page draws, the path of the page it was focused on, whether the page took it
 *  from the reader (`taken`: the element was disabled, hidden or taken away) rather than the
 *  reader moving off it, and - in a dialog - the loss the dialog was opened from (`opener`):
 *  the place the focus left for it, or null for a dialog the focus entered from the body. */
interface Loss {
  readonly caused: boolean;
  readonly element: HTMLElement;
  readonly index: number;
  readonly opener: Loss | null;
  readonly path: string;
  readonly region: Element;
  readonly taken: boolean;
}

/** The longest the rescue waits for an animation frame before it goes on without one. WebKit can
 *  hold a page's frames back while nothing on it is drawn - measured in headless WebKit, a check
 *  waited 775 ms for its frame, and another had none in 2 s while the page's timers ran - and a
 *  keyboard reader is not to sit on the body for that. React has committed and run its passive
 *  effects well inside this. */
const FRAME_WAIT_MS = 100;

/** Runs `then` once the components have had their turn with a loss of focus: a frame, or
 *  `FRAME_WAIT_MS` where no frame comes, and then a task later, after React has committed and run
 *  its passive effects. Answers what cancels it. */
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

/** The record of `records` that took `element` away, itself or with a parent, read at a point
 *  still in the page: where the parent it was taken from went too, later in the same batch, the
 *  record that took that parent. */
function removalOf(
  records: readonly MutationRecord[],
  element: Element
): MutationRecord | undefined {
  let node: Node = element;
  let found: MutationRecord | undefined;
  for (;;) {
    const record = records.find((each) =>
      [...each.removedNodes].some((removed) => removed.contains(node))
    );
    if (record === undefined) return found;
    found = record;
    if (record.target.isConnected) return record;
    node = record.target;
  }
}

/** The place of a control `record` took away, itself or with a parent, among the controls
 *  `region` draws: how many of them come before the point it was taken from - before the sibling
 *  after it while that is still in the page, else up to and through the sibling before it, else
 *  through the parent it was taken from. */
function placeOfRemoval(region: Element, record: MutationRecord, main: HTMLElement | null): number {
  const next = record.nextSibling?.isConnected ? record.nextSibling : null;
  const through = record.previousSibling?.isConnected ? record.previousSibling : record.target;
  let index = 0;
  for (const control of drawnIn(region, main)) {
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

/** Focuses `target` without a scroll, so a loss the reader did not cause never scrolls the page.
 *  Answers whether it took the focus. */
function focusNoScroll(target: HTMLElement): boolean {
  target.focus({ preventScroll: true });
  return document.activeElement === target;
}

/** The box a loss the reader did not cause lands in rather than a row: the nearest box around
 *  `row` that clips what overflows it and sits inside `region` (`clippingBoxes`), so the focus
 *  stays by the list; else the region itself, unless that is the page's body, which is no target. */
function boxAround(row: Element, region: Element): HTMLElement | null {
  for (const box of clippingBoxes(row)) {
    if (region.contains(box) && box instanceof HTMLElement) return box;
  }
  return region instanceof HTMLElement && region !== document.body ? region : null;
}

/** Focuses a box or region a loss the reader did not cause lands on, and only while the reader can
 *  see it (`inSight`), so the focus never lands where they cannot see it. One with no Tab stop of
 *  its own is given `tabindex="-1"` for the focus and has it taken off again once the focus leaves,
 *  so the engine's own Tab order is left as it was. Answers whether it took the focus. */
function landBox(box: HTMLElement): boolean {
  if (!box.isConnected || !rendered(box) || !inSight(box, clippingBoxes(box))) return false;
  const needsStop = !box.hasAttribute("tabindex");
  if (needsStop) box.tabIndex = -1;
  const took = focusNoScroll(box);
  if (needsStop) {
    if (took) box.addEventListener("blur", () => box.removeAttribute("tabindex"), { once: true });
    else box.removeAttribute("tabindex");
  }
  return took;
}

/** Hands a loss the reader did not cause to no row and with no scroll: the first candidate in no
 *  row takes the focus where it is, since the loss itself was in sight; a candidate in a row sends
 *  the focus to the box that row is drawn in instead (`boxAround`), never into the row and only
 *  while the reader can see the box; where none takes it, the region itself does if the reader can
 *  see it, else the focus stays on the body, where the base left it. */
function handOnQuietly(candidates: readonly HTMLElement[], region: Element): void {
  for (const candidate of candidates) {
    if (!candidate.isConnected || !rendered(candidate)) continue;
    const row = candidate.closest(ROW);
    if (row === null) {
      if (focusNoScroll(candidate)) return;
    } else {
      const box = boxAround(row, region);
      if (box !== null && landBox(box)) return;
    }
  }
  if (present(region) && region instanceof HTMLElement && region !== document.body) {
    landBox(region);
  }
}

/** Hands on the focus `loss`'s element took with it when it went, inside the region it was lost
 *  in while that region is still in the page and drawn (`present`). A loss the reader caused keeps
 *  the walk it always had: to the control the region now draws at its place, else the nearest one
 *  before it however many went together (`placesFrom`), else the region itself, which is every
 *  region but the document, and that control is scrolled into view (`land`). A loss they did not
 *  cause lands in no row and scrolls nothing (`handOnQuietly`): a candidate in no row takes the
 *  focus where it is, a candidate in a row sends it to the box that row is drawn in, and nothing
 *  moves where the reader cannot see it. From a region that has gone - a dialog that closed, or
 *  one hidden while it stays mounted - the focus goes only back to the place the dialog was opened
 *  from (its `opener`, or that dialog's own opener where it has gone too): that element, the
 *  control that opened it, if it can still take the focus, else the control now at its place, else
 *  the nearest one before it. A dialog opened from the body hands the focus nowhere: the body is
 *  where the reader was. */
function handOn(loss: Loss, main: HTMLElement | null, caused: boolean): void {
  const { region } = loss;
  let candidates: HTMLElement[] = [];
  let answerRegion: Element = region;
  if (present(region)) {
    candidates = placesFrom(drawnIn(region, main), loss.index);
  } else {
    let opener = loss.opener;
    while (opener !== null && !present(opener.region)) opener = opener.opener;
    if (opener !== null && opener.path === loss.path) {
      candidates = [opener.element, ...placesFrom(drawnIn(opener.region, main), opener.index)];
      answerRegion = opener.region;
    }
  }
  if (!caused) {
    handOnQuietly(candidates, answerRegion);
    return;
  }
  for (const candidate of candidates) {
    if (!candidate.isConnected || !rendered(candidate)) continue;
    if (land(candidate)) return;
  }
  if (!present(region) || region === document.body || !(region instanceof HTMLElement)) return;
  if (!region.hasAttribute("tabindex")) region.tabIndex = -1;
  land(region);
}

/**
 * The page's one focus rescue, mounted once in the app shell: after any loss of the focus to the
 * body - a press, or an update that takes the focused control away - the keyboard focus never
 * stays on the page's body because the element holding it was disabled, hidden, taken away or
 * drawn again under a new key, unless the reader had scrolled that element out of their sight
 * (below). A keyboard reader left on the body starts again from the page's first link, a walk
 * through everything above the control they were using.
 *
 * Every element that takes the focus is noted as it does (`focusin`), with its region - the
 * dialog holding it, modal or not, else the shell's main content (`main`), else the document -
 * and the path of the page it was focused on, which a navigation does not change before the old
 * page goes. A dialog the focus enters for the first time notes where it came from: the loss of
 * the element that handed it over, or none when it came from the body. A loss is seen two ways.
 * A control disabled, hidden or left by the reader fires a `focusout` in every engine, while it is
 * still in the page. A control taken away, itself or with a parent, is seen by watching the page
 * (a MutationObserver over the document) in every engine, since Firefox and WebKit fire no
 * `focusout` for it; Chromium fires one as well, and whichever comes first is the loss recorded.
 * Each loss holds its element, its region, its place among the controls the region draws, the
 * path, whether the page took the element (`taken`), whether the reader caused the loss
 * (`causedByReader`), and its dialog's opener. A move from one
 * control to another in the same region is no loss. A new loss cancels whatever is still out for
 * the one before it.
 *
 * The check runs a frame and a task after the loss, once React has committed and run its passive
 * effects, so every component's own hand-off has had its turn first: every `useFocusPlace`
 * hand-off (ControlRow, DataTable, PanelTrigger and the pages' own), SelectionBar's
 * `returnFocus`, AskCard's, `useDialog`'s return to the opener, the Agents composer's take-back.
 * Each knows its own target better than a place counted in the page does - the row marked last,
 * the opener, the composer's box - so the rescue answers only a loss none of them answered, and
 * acts only while the focus is still lost. It leaves alone a loss on a page the reader has since
 * left, since the new page owns its focus, and a loss the reader made whose element is still in
 * the page, enabled and drawn - a click on the page itself, a deliberate `.blur()` such as the
 * Inbox's and the Board's Escape. A taken element that is back by then gets the focus back.
 *
 * An element that went - taken away, hidden, no longer drawn, or replaced under a new key - hands
 * the focus on (`handOn`) inside the region it was lost in. A loss the reader caused - a press of
 * theirs within the wait on the lost control, inside its row, or inside the region (`causedByReader`)
 * - goes to the control the region now draws at its place, else the nearest one before it however
 * many went together, else the region itself, and that control is scrolled into view; a loss they
 * did not cause goes into no row and scrolls nothing (`handOnQuietly`), to a control in no row, or
 * to the box a lost row was drawn in while the reader can see it, else the body. So the page behind
 * a modal dialog is never reached. Only a region that has gone hands the focus
 * out of it: from a dialog that closed it goes only back to the place the dialog was opened from; a
 * dialog opened from the body - a single-key shortcut with nothing focused - leaves the focus on
 * the body, where the reader was, and never puts it in a text box whose next key would be typed.
 * An element the press disabled, itself or through the fieldset around it, gets the focus back
 * when it is enabled again, watched rather than polled, since its write's latency is the
 * server's; if it goes instead, or is still disabled after `RESCUE_WAIT_MS`, the focus is handed
 * on as from an element that went. That settle too waits a frame and a task, so a component that
 * watches the same control - the Agents composer, giving its box the focus back - acts on the
 * change first. A pointer press anywhere while a check or a wait is out is the reader moving on -
 * onto a control, or onto the page itself - and ends it, so a control enabled again later never
 * pulls back a focus the reader left on the page. So is the reader's Escape: a control it takes
 * away or leaves - the issue title's editor, whose heading would open it again if it took the
 * focus - is the reader leaving it, and the focus stays where Escape left it, as the Inbox's and
 * the Board's own Escape leave it on the page. The element a loss the reader caused lands on is
 * scrolled into view (`land`); a loss they did not cause scrolls nothing.
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
 * nothing they did not choose. The focus stays on the body, except in a dialog, which takes it
 * itself without a scroll, so the page behind a modal one is never reached (`leaveUnseen`). The
 * record is the page's one (`outOfSight`), which the component hand-offs read too, so none of
 * them answers such a loss before the rescue's check. Only an element a read has found in sight
 * since it took the focus can be read out of it (`readSight`): one focused below the fold, which
 * a scroll the page made before the engine's own left there, was never in the reader's sight.
 */
export function useFocusRescue(main: RefObject<HTMLElement | null>): void {
  useEffect(() => {
    rescueMounted = true;
    /** The element holding the focus, its region and the path it was focused on, until it loses
     *  the focus by a `focusout`; one still noted here once it has left the page went with no
     *  `focusout` at all. */
    let focused: { element: HTMLElement; path: string; region: Element } | null = null;
    /** The loss each dialog the focus has entered was opened from, or null for one it entered
     *  from the body. */
    const openedFrom = new Map<Element, Loss | null>();
    /** Where a `focusout` is handing the focus, and the loss it leaves in another region. */
    let moving: { from: Loss | null; to: Element } | null = null;
    /** Cancels the check or the wait still out for the latest loss. */
    let pending: (() => void) | null = null;
    /** Whether the reader had scrolled `loss`'s element out of their sight before it went, in a
     *  region still in the page. */
    const unseen = (loss: Loss) => outOfSight(loss.element) && present(loss.region);

    /** Waits for `loss`'s element, which the press disabled, to be enabled again or to go. */
    const wait = (loss: Loss): (() => void) => {
      const { element } = loss;
      /** Cancels the settle once it is scheduled. */
      let settling: (() => void) | undefined;
      const settle = () => {
        pending = null;
        if (!focusLost() || window.location.pathname !== loss.path) return;
        if (unseen(loss)) {
          leaveUnseen(loss.region);
          return;
        }
        if (element.isConnected && rendered(element)) {
          if (loss.caused ? land(element) : focusNoScroll(element)) return;
        }
        handOn(loss, main.current, loss.caused);
      };
      // The element enabled again, gone, or the wait run out: settled a frame and a task later,
      // as the first check is, so a component answering the same change - its own watcher,
      // started after this one, such as the Agents composer's take-back, or an effect - moves
      // the focus first. The first change of a burst schedules it, and the watching ends there.
      const later = () => {
        if (settling !== undefined) return;
        watcher.disconnect();
        window.clearTimeout(bound);
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
      const bound = window.setTimeout(later, RESCUE_WAIT_MS);
      watcher.observe(document.body, {
        attributeFilter: ["disabled", "hidden"],
        childList: true,
        subtree: true,
      });
      return () => {
        watcher.disconnect();
        window.clearTimeout(bound);
        settling?.();
      };
    };

    const check = (loss: Loss) => {
      pending = null;
      if (window.location.pathname !== loss.path || !focusLost()) return;
      const { element } = loss;
      if (unseen(loss)) {
        leaveUnseen(loss.region);
      } else if (!element.isConnected || !rendered(element)) {
        handOn(loss, main.current, loss.caused);
      } else if (element.matches(":disabled")) {
        pending = wait(loss);
      } else if (loss.taken) {
        if (loss.caused) land(element);
        else focusNoScroll(element);
      }
    };

    const noteFocus = (event: globalThis.FocusEvent) => {
      const element = event.target;
      if (!(element instanceof HTMLElement)) return;
      const region = regionOf(element, main.current);
      if (region.matches(DIALOG) && !openedFrom.has(region)) {
        for (const dialog of openedFrom.keys()) {
          if (!dialog.isConnected) openedFrom.delete(dialog);
        }
        openedFrom.set(region, moving?.to === element ? moving.from : null);
      }
      moving = null;
      focused = { element, path: window.location.pathname, region };
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
      pending?.();
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
      const loss: Loss = {
        caused: causedByReader(element),
        element,
        index: countBefore(drawnIn(region, main.current), element),
        opener: openedFrom.get(region) ?? null,
        path,
        region,
        taken: element.matches(":disabled") || !rendered(element),
      };
      // A focus going straight to another region is kept only as the place it left: the opener
      // of the dialog it is entering.
      if (next instanceof Element) moving = { from: loss, to: next };
      else pending = afterComponents(() => check(loss));
    };

    // A focused element taken away with no `focusout` - Firefox's and WebKit's way, for the
    // element or a parent of it - is read off the mutation that took it, at the place it was
    // taken from, or at the region's end where no record of this batch names it.
    const watcher = new MutationObserver((records) => {
      const held = focused;
      if (held === null || held.element.isConnected) return;
      focused = null;
      if (leaving || !focusLost()) return;
      const removal = removalOf(records, held.element);
      const index =
        removal === undefined || !held.region.isConnected
          ? drawnIn(held.region, main.current).length
          : placeOfRemoval(held.region, removal, main.current);
      pending?.();
      const loss: Loss = {
        caused: causedByReader(held.element),
        ...held,
        index,
        opener: openedFrom.get(held.region) ?? null,
        taken: true,
      };
      pending = afterComponents(() => check(loss));
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
    /** Records the reader's own act - a `pointerdown`, a `keydown`, or a `change` they committed, a
     *  select's pick that Playwright and assistive tech make without a key or a pointer - so a loss
     *  after it can be told from one an update made under them (`causedByReader`). */
    const notePress = (event: Event) => {
      if (event.target instanceof Element) {
        lastPress = { at: performance.now(), target: event.target };
      }
    };
    const readerMoved = (event: Event) => {
      notePress(event);
      pending?.();
      pending = null;
    };
    /** Whether the reader's Escape is being answered: set by its `keydown` until the next task,
     *  by when the commit it caused has run. */
    let leaving = false;
    const noteKey = (event: KeyboardEvent) => {
      notePress(event);
      if (event.key !== "Escape") return;
      leaving = true;
      window.setTimeout(() => {
        leaving = false;
      }, 0);
    };
    document.addEventListener("focusin", noteFocus, true);
    document.addEventListener("focusout", record, true);
    document.addEventListener("pointerdown", readerMoved, true);
    document.addEventListener("keydown", noteKey, true);
    document.addEventListener("change", notePress, true);
    document.addEventListener("scroll", noteScroll, { capture: true, passive: true });
    window.addEventListener("resize", noteResize);
    return () => {
      watcher.disconnect();
      document.removeEventListener("focusin", noteFocus, true);
      document.removeEventListener("focusout", record, true);
      document.removeEventListener("pointerdown", readerMoved, true);
      document.removeEventListener("keydown", noteKey, true);
      document.removeEventListener("change", notePress, true);
      document.removeEventListener("scroll", noteScroll, true);
      window.removeEventListener("resize", noteResize);
      pending?.();
      sight = null;
      rescueMounted = false;
      lastPress = null;
    };
  }, [main]);
}
