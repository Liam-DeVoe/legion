import { type RefObject, useEffect, useLayoutEffect, useRef } from "react";

/** What a region lost the focus to, as each fallback of `useFocusKeeper` reads it. */
export interface FocusLoss {
  /** The region's root. */
  readonly root: HTMLElement;
  /** The element that held the focus, now removed, disabled, hidden or inert. */
  readonly lost: HTMLElement;
  /** The elements the same change added inside the region, in the order they were added. */
  readonly added: readonly HTMLElement[];
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

/**
 * Keeps a reader's place in a region when a change takes away the control that holds the focus
 * (decision 216). It remembers which element inside `root` holds the focus. When that element is
 * removed, disabled, hidden or made inert while it holds the focus, the browser drops the focus to
 * the page's body; the keeper then asks `fallbacks` in order and focuses the first answer that
 * can hold the focus, so a keyboard reader goes on from where they were rather than from the top
 * of the page. A fallback reads the loss: the lost element and what the same change added, so it
 * can find the control drawn in the lost one's place.
 *
 * It never moves the focus when nothing was lost. A focus the reader moved elsewhere, a focus the
 * reader put on the body (a press on the page, a `blur()`), and a focus a handler already handed
 * to another control all stay where they are. When no fallback can hold the focus it stays on the
 * body. A region nobody has focused costs nothing: the keeper watches the region only while the
 * focus is inside it.
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
    /** The elements added inside the region since the last check. */
    let added: HTMLElement[] = [];
    /** Whether the reader has pressed the pointer somewhere other than `held` since it took the
     *  focus. Such a press is the reader moving the focus: a press on the page blurs the control
     *  to the body, and the handler that blur runs may remove it before the check below runs. */
    let pointerMoved = false;

    const notePress = (event: PointerEvent) => {
      // A press on the control that holds the focus leaves the focus there and focuses nothing
      // anew, so no focusin would clear the mark: the write that press starts may then take the
      // control away, which is a loss the keeper owes the reader.
      if (held !== null && event.target instanceof Node && held.contains(event.target)) return;
      pointerMoved = true;
    };
    const noteAdded = (records: readonly MutationRecord[]) => {
      for (const record of records) {
        for (const node of record.addedNodes) {
          if (node instanceof HTMLElement) added.push(node);
        }
      }
    };
    const observer = new MutationObserver((records) => {
      noteAdded(records);
      check();
    });
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
      added = [];
      pointerMoved = false;
      observer.disconnect();
      page.removeEventListener("pointerdown", notePress, true);
    };
    const check = () => {
      const lost = held;
      if (lost === null) return;
      noteAdded(observer.takeRecords());
      const change = added;
      added = [];
      const active = page.activeElement;
      const onBody = active === null || active === page.body;
      if (!onBody && active !== lost) {
        // The focus moved to another element. Inside the region the focusin has already made it
        // the one held, and a handler that handed the focus on is obeyed; outside it, the reader
        // has left the region.
        if (!region.contains(active)) forget();
        return;
      }
      if (onBody && pointerMoved) {
        // The reader's own press put the focus on the page.
        forget();
        return;
      }
      if (canHoldFocus(lost) && region.contains(lost)) {
        // Nothing was lost. A focus on the body is where the reader put it.
        if (onBody) forget();
        return;
      }
      const loss: FocusLoss = { added: change, lost, root: region };
      for (const fallback of latestFallbacks.current) {
        const next = fallback(loss);
        if (canHoldFocus(next)) {
          next.focus();
          if (!region.contains(page.activeElement)) forget();
          return;
        }
      }
      forget();
    };
    const noteFocus = (event: FocusEvent) => {
      if (event.target instanceof HTMLElement) hold(event.target);
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
