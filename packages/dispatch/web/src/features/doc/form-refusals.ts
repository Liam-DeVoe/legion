import { useCallback, useSyncExternalStore } from "react";

import type { ApiError } from "../../api/client";

/**
 * Dispatch's refusal of a person's Approve for the form of The Spec (`SPEC_FORM`), kept by the
 * approval it refused: an approval request's ask (`ask:<id>`, the answer form of `AskCard`) or a
 * document's header (`artifact:<id>`, `ApprovalChip`).
 *
 * A refusal belongs to that approval, not to the card or the header that shows it, for the reason
 * an unsent answer belongs to its ask (`inbox/answer-drafts.ts`): a host can draw its card again
 * while the answer is out. The issue page's Needs you does that every time, because the answer
 * takes the ask's row out of the Inbox read and the refusal puts it back. The card drawn again
 * reads the refusal here, and with it picks Request changes and takes the focus that the press
 * owes.
 *
 * Each refusal holds the version it judged, and a read for another version finds none, since the
 * findings are of the text that version holds. The next press of the same approval, or an answer
 * that Dispatch records, lets the refusal go.
 */
interface FormRefusal {
  readonly error: ApiError;
  readonly version: number | undefined;
  /** Whether the press it answers still owes the focus to a surface that shows it: true until
   *  one takes it (`takeFormRefusalFocus`). */
  focusOwed: boolean;
}

const refusals = new Map<string, FormRefusal>();
const listeners = new Map<string, Set<() => void>>();

/** Keeps `error` as the refusal of `key` at `version`, or lets the refusal of `key` go. */
export function setFormRefusal(
  key: string,
  refusal: { readonly error: ApiError; readonly version: number | undefined } | undefined
): void {
  if (refusal === undefined && !refusals.has(key)) return;
  if (refusal === undefined) refusals.delete(key);
  else refusals.set(key, { ...refusal, focusOwed: true });
  for (const listener of listeners.get(key) ?? []) listener();
}

/** The refusal of `key` at `version`, or undefined. The component re-renders when it changes. */
export function useFormRefusal(key: string, version: number | undefined): ApiError | undefined {
  const subscribe = useCallback(
    (listener: () => void) => {
      const keyListeners = listeners.get(key) ?? new Set<() => void>();
      listeners.set(key, keyListeners);
      keyListeners.add(listener);
      return () => {
        keyListeners.delete(listener);
        if (keyListeners.size === 0) listeners.delete(key);
      };
    },
    [key]
  );
  const refusal = useSyncExternalStore(subscribe, () => refusals.get(key));
  return refusal === undefined || refusal.version !== version ? undefined : refusal.error;
}

/** Whether the press that `key`'s refusal answers still owes the focus. The first surface that
 *  asks takes it, so a surface drawn later leaves the focus where the reader put it. */
export function takeFormRefusalFocus(key: string): boolean {
  const refusal = refusals.get(key);
  if (refusal === undefined || !refusal.focusOwed) return false;
  refusal.focusOwed = false;
  return true;
}

/** Whether the focus is on no control, or on one inside `surface`: where a surface may give the
 *  focus that a refusal's press owes without taking it from where the reader moved it. */
export function focusIsFreeFor(surface: Element | null): boolean {
  const active = document.activeElement;
  return active === null || active === document.body || surface?.contains(active) === true;
}

/** Forgets every refusal, for the unit-test preload, since the store outlives any one render. */
export function resetFormRefusals(): void {
  refusals.clear();
}
