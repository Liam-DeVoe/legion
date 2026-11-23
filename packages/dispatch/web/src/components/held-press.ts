import { useEffect, useState } from "react";

import { beginAnswerHold } from "./focus-place";

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

/**
 * What a text field spreads onto itself to be held while a write holds the form it is part of:
 * `aria-disabled`, and read-only, so it takes no typing. It is never disabled, so the field the
 * reader submitted from with Enter keeps the focus, as a held button does (`heldPress`).
 */
export function heldField(held: boolean): {
  "aria-disabled": true | undefined;
  readOnly: boolean;
} {
  return { "aria-disabled": held || undefined, readOnly: held };
}

/**
 * Whether a control whose press started a write is held: from the first render with `pending`
 * until the commit after `pending` turns false. A list that holds the write itself, as the Inbox
 * does a row's snooze (`useAskSnoozeWrites`) and its Assign to me (`AskAssignWritesProvider`),
 * lets go of the row in the answer's own commit, while this hold is still on. The hold is part of
 * the action of the reader's press that started the write while it is on (`beginAnswerHold`), so
 * the page's rescue reads the row's going as the press's doing (`causedByReader`), and the focus
 * goes to the control now at the row's place rather than to the body.
 */
export function useHeldThroughAnswer(pending: boolean): boolean {
  const [answering, setAnswering] = useState(false);
  useEffect(() => {
    if (!pending) return;
    setAnswering(true);
    return () => setAnswering(false);
  }, [pending]);
  const held = pending || answering;
  useEffect(() => (held ? beginAnswerHold() : undefined), [held]);
  return held;
}
