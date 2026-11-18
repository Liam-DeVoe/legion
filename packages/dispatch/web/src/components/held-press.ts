/**
 * What a button spreads onto itself to be held while it cannot be pressed - its write is out,
 * its step has no page to go to - rather than disabled: `aria-disabled`, and a press that does
 * nothing then. The browser takes the focus off a control that disables itself under the reader's
 * press, dropping a keyboard reader onto the page, and a dialog the press opened would hand the
 * focus back to an opener that can no longer take it. Held, the button keeps the focus and its
 * Tab stop, and the `refused:` variant (`styles.css`) paints it as it paints a disabled one.
 */
export function heldPress(
  held: boolean,
  press: () => void
): { "aria-disabled": true | undefined; onClick: () => void } {
  return {
    "aria-disabled": held || undefined,
    onClick: () => {
      if (!held) press();
    },
  };
}

/** What a text field spreads onto itself to be held while its write is out, rather than
 *  disabled, for the reason `heldPress` gives: `aria-disabled`, and `readOnly`, so the field
 *  keeps the focus and its Tab stop and takes no typing then. Its class paints it held
 *  (`aria-disabled:opacity-50`). */
export function heldField(held: boolean): { "aria-disabled": true | undefined; readOnly: boolean } {
  return { "aria-disabled": held || undefined, readOnly: held };
}
