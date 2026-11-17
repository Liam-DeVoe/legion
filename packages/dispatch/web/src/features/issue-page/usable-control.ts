import { DETAILS_ATTRIBUTE } from "./margin-slot";

/** An element that hides what is under it, or takes it out of reach of a press and the focus. */
const SHUT = '[hidden], [inert], [aria-hidden="true"]';

/**
 * Whether `control` takes a press now: the one rule Add's entries and the key `l` read before
 * they list, offer or press a control another component draws, so what is offered is exactly
 * what a press opens. A control is usable while it is on the page; neither `disabled` nor busy
 * (`aria-disabled="true"`, as the page's controls are while they save: the `refused:` variant of
 * `styles.css` reads the same pair); under no element, itself included, that is `hidden`,
 * `inert` or `aria-hidden="true"`; and drawn (`checkVisibility()`), which also catches a box the
 * stylesheet hides.
 *
 * `opensDetails` is for a reach that opens Details itself before it presses (`useOpenDetailsAt`
 * for `l`, Add's reach into a shut drawer): the Details drawer's own `aside`, `hidden` while
 * Details is closed (`features/margin/Margin.tsx`), is then no refusal. The elements around the
 * control are read up to that `aside` only, and while it is hidden the control is not asked
 * whether it is drawn, since nothing under a hidden `aside` is. Without this, `l` with Details
 * closed would never open Details at Labels.
 */
export function usableControl(
  control: Element | null,
  { opensDetails = false }: { opensDetails?: boolean } = {}
): boolean {
  if (control === null || !control.isConnected) return false;
  if (control.matches(':disabled, [aria-disabled="true"]')) return false;
  const details = opensDetails ? control.closest(`aside[${DETAILS_ATTRIBUTE}]`) : null;
  for (
    let node: Element | null = control;
    node !== null && node !== details;
    node = node.parentElement
  ) {
    if (node.matches(SHUT)) return false;
  }
  if (details?.hasAttribute("hidden") === true) return true;
  // `checkVisibility()` needs Safari 17.4, Chrome 105 or Firefox 106, and Dispatch still runs on
  // browsers without it (`shell/roving.ts`): there the attributes above are the whole rule.
  return typeof control.checkVisibility !== "function" || control.checkVisibility();
}
