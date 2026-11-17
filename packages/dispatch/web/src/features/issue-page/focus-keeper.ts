import { type RefObject, useEffect, useLayoutEffect, useRef } from "react";

/** What a region lost the focus to, as each fallback of `useFocusKeeper` reads it. */
export interface FocusLoss {
  /** The region's root. */
  readonly root: HTMLElement;
  /** The element that held the focus, now removed, disabled, hidden or inert. */
  readonly lost: HTMLElement;
  /** The elements the same change added inside the region, in the order they were added. */
  readonly added: readonly HTMLElement[];
  /**
   * Where the lost element stood at the change: its parent while it is still in the document;
   * for a removal, the element the change took it (or an ancestor of it) out of, while that
   * element is still in the document; else null. A fallback that wants the control next to the
   * lost one starts from here.
   */
  readonly place: HTMLElement | null;
}

/** One place the focus may go once it is lost, or nothing when this fallback has none to give. */
export type FocusFallback = (loss: FocusLoss) => HTMLElement | null | undefined;

/**
 * Whether the element can take the focus as it stands: it is on the page, not disabled, and
 * neither it nor an ancestor is hidden or inert. It reads no layout, so a test's DOM, which has
 * none, answers as a browser does.
 */
export function canHoldFocus(element: Element | null | undefined): element is HTMLElement {
  return (
    element instanceof HTMLElement &&
    element.isConnected &&
    !element.matches(":disabled") &&
    element.closest("[hidden], [inert]") === null
  );
}

/** The changes that can take the focus away: a removal, and a disable, a hide or an inert. */
const LOSSES: MutationObserverInit = {
  attributeFilter: ["disabled", "hidden", "inert"],
  attributes: true,
  childList: true,
  subtree: true,
};

/** What the reader does that ends the keeper's claim to give a control its focus back: a key or a
 *  pointer press, anywhere on the page. */
const READER_ACTS = ["keydown", "pointerdown"] as const;

/** The elements `records` added, in the order they were added. */
function addedBy(records: readonly MutationRecord[]): HTMLElement[] {
  const added: HTMLElement[] = [];
  for (const record of records) {
    for (const node of record.addedNodes) {
      if (node instanceof HTMLElement) added.push(node);
    }
  }
  return added;
}

/** Where `lost` stood at the change `records` heard, as `FocusLoss.place` says. Of two removals
 *  that took it out, the later one is read, since that is where it stood last. */
function placeOf(lost: HTMLElement, records: readonly MutationRecord[]): HTMLElement | null {
  if (lost.isConnected) return lost.parentElement;
  let place: HTMLElement | null = null;
  for (const record of records) {
    const { target } = record;
    if (record.type !== "childList" || !(target instanceof HTMLElement) || !target.isConnected) {
      continue;
    }
    for (const removed of record.removedNodes) {
      if (removed.contains(lost)) place = target;
    }
  }
  return place;
}

/**
 * Keeps a reader's place in a region when a change takes away the control that holds the focus
 * (decision 216). It remembers which element inside `root` holds the focus. When that element is
 * removed, disabled, hidden or made inert while it holds the focus, the browser drops the focus to
 * the page's body; the keeper then asks `fallbacks` in order and focuses the first answer that
 * can hold the focus, so a keyboard reader goes on from where they were rather than from the top
 * of the page. A fallback reads the loss: the lost element, where it stood and what the same
 * change added, so it can find the control drawn in the lost one's place.
 *
 * The keeper waits one task before it moves the focus, and moves it only if the focus is still on
 * the body or on the lost element then: a site that hands the focus on itself, even from a passive
 * effect that runs after the change, keeps its own choice.
 *
 * When the keeper moved the focus off a control because that control was disabled, and the
 * control is enabled again while the focus still rests where the keeper put it, the keeper gives
 * the focus back to it: a Retry that is disabled while its read runs again and enabled when that
 * read fails keeps the reader on the Retry. Any act of the reader in between ends that claim - a
 * key or a pointer press anywhere on the page, or a focus on any other element - and so do the
 * focus leaving the region and the control being removed.
 *
 * Apart from that give-back, it never moves the focus when nothing was lost. A focus the reader
 * moved elsewhere, a focus the reader put on the body (a press on the page, a `blur()`), and a
 * focus a handler already handed to another control all stay where they are. When no fallback can
 * hold the focus it stays on the body. A region nobody has focused costs nothing: the keeper
 * watches the region only while the focus is inside it.
 *
 * `fallbacks` may be a new array on every render; the keeper always asks the latest one.
 */
export function useFocusKeeper(
  root: RefObject<HTMLElement | null>,
  fallbacks: readonly FocusFallback[]
): void {
  // Read through a ref, because subscribing again for each render's new array would forget which
  // element held the focus.
  const latestFallbacks = useRef(fallbacks);
  useLayoutEffect(() => {
    latestFallbacks.current = fallbacks;
  });

  useEffect(() => {
    const region = root.current;
    if (region === null) return;
    const page = region.ownerDocument;
    /** The element inside the region that holds the focus, or null while the focus is outside. */
    let held: HTMLElement | null = null;
    /** The observer's records since the last check: what a change added, and where it took an
     *  element out. */
    let heard: MutationRecord[] = [];
    /** Whether the reader has pressed the pointer somewhere other than `held` since it took the
     *  focus. Such a press is the reader moving the focus: a press on the page blurs the control
     *  to the body, and the handler that blur runs may remove it before the check below runs. */
    let pointerMoved = false;
    /** The timer of the keeper's move, which waits one task after the check that found the loss,
     *  or undefined while no move waits. */
    let waiting: number | undefined;
    /** The keeper's last move of the focus off a control that turned disabled: that control, and
     *  where the keeper put the focus. Null once the control has the focus back, the reader has
     *  acted or the focus has moved on. */
    let given: { readonly from: HTMLElement; readonly to: HTMLElement } | null = null;

    const notePress = (event: PointerEvent) => {
      // A press on the control that holds the focus leaves the focus there and focuses nothing
      // anew, so no focusin would clear the mark: the write that press starts may then take the
      // control away, which is a loss the keeper owes the reader.
      if (held !== null && event.target instanceof Node && held.contains(event.target)) return;
      pointerMoved = true;
    };
    const hear = (records: readonly MutationRecord[]) => {
      for (const record of records) heard.push(record);
    };
    const observer = new MutationObserver((records) => {
      hear(records);
      check();
    });
    const dropGiven = () => {
      given = null;
      for (const type of READER_ACTS) page.removeEventListener(type, dropGiven, true);
    };
    const give = (from: HTMLElement, to: HTMLElement) => {
      given = { from, to };
      for (const type of READER_ACTS) {
        page.addEventListener(type, dropGiven, { capture: true, passive: true });
      }
    };
    const hold = (element: HTMLElement) => {
      if (held === null) {
        observer.observe(region, LOSSES);
        page.addEventListener("pointerdown", notePress, true);
      }
      held = element;
      pointerMoved = false;
    };
    const forget = () => {
      held = null;
      heard = [];
      pointerMoved = false;
      window.clearTimeout(waiting);
      waiting = undefined;
      dropGiven();
      observer.disconnect();
      page.removeEventListener("pointerdown", notePress, true);
    };
    /** Whether the focus `lost` held is lost: it is on the body or still on `lost`, no press of
     *  the reader's put it there, and `lost` can no longer hold it. Every other answer is final,
     *  and where the focus has left the region the keeper stops watching it. */
    const isLost = (lost: HTMLElement): boolean => {
      const active = page.activeElement;
      const onBody = active === null || active === page.body;
      if (!onBody && active !== lost) {
        // The focus moved to another element. Inside the region the focusin has already made it
        // the one held, and a handler that handed the focus on is obeyed; outside it, the reader
        // has left the region.
        if (!region.contains(active)) forget();
        return false;
      }
      if (onBody && pointerMoved) {
        // The reader's own press put the focus on the page.
        forget();
        return false;
      }
      if (canHoldFocus(lost) && region.contains(lost)) {
        // Nothing was lost. A focus on the body is where the reader put it.
        if (onBody) forget();
        return false;
      }
      return true;
    };
    /** Gives the focus back to the control the keeper moved it off, once that control can hold
     *  it again and the focus still rests where the keeper put it. */
    const giveBack = () => {
      if (given === null) return;
      const { from, to } = given;
      if (!region.contains(from)) {
        // The control is gone, so there is nothing to give the focus back to.
        dropGiven();
        return;
      }
      if (!canHoldFocus(from) || page.activeElement !== to) return;
      dropGiven();
      from.focus();
    };
    const check = () => {
      if (held === null) return;
      hear(observer.takeRecords());
      giveBack();
      // A give-back moved the focus, and its focusin made the control given it the one held.
      const lost = held;
      if (!isLost(lost)) {
        heard = [];
        return;
      }
      // The move waits one task: a site's own hand-on may run in a passive effect, which for a
      // change from outside an event runs after this check, and the site's choice wins.
      waiting ??= window.setTimeout(move, 0);
    };
    const move = () => {
      waiting = undefined;
      // The focus may have moved since the check, so the loss is read again, of whatever the
      // region holds now.
      const lost = held;
      if (lost === null) return;
      hear(observer.takeRecords());
      const change = heard;
      heard = [];
      if (!isLost(lost)) return;
      const loss: FocusLoss = {
        added: addedBy(change),
        lost,
        place: placeOf(lost, change),
        root: region,
      };
      // Only a control that is merely disabled gets the focus back: enabled again it is the same
      // control in the same place, where a removed, hidden or inert one is out of the reader's way.
      const disabled =
        lost.isConnected && lost.matches(":disabled") && lost.closest("[hidden], [inert]") === null;
      for (const fallback of latestFallbacks.current) {
        const next = fallback(loss);
        if (canHoldFocus(next)) {
          // One move is kept at a time: this one replaces whatever an older move kept.
          if (disabled) give(lost, next);
          else dropGiven();
          next.focus();
          if (!region.contains(page.activeElement)) forget();
          return;
        }
      }
      forget();
    };
    const noteFocus = (event: FocusEvent) => {
      if (!(event.target instanceof HTMLElement)) return;
      // A focus anywhere but where the keeper put it moves the focus on, past any give-back.
      if (given !== null && event.target !== given.to) dropGiven();
      hold(event.target);
    };
    const noteLeave = (event: FocusEvent) => {
      const next = event.relatedTarget;
      if (next instanceof Node && region.contains(next)) return;
      // Chromium blurs a disabled control by itself, so this focusout can come before or after
      // the observer hears the change; the check waits a microtask for the change's records.
      queueMicrotask(check);
    };

    // A control that took the focus as it mounted did so before this effect could hear it.
    const active = page.activeElement;
    if (active instanceof HTMLElement && region.contains(active)) hold(active);
    region.addEventListener("focusin", noteFocus);
    region.addEventListener("focusout", noteLeave);
    return () => {
      region.removeEventListener("focusin", noteFocus);
      region.removeEventListener("focusout", noteLeave);
      forget();
    };
  }, [root]);
}
