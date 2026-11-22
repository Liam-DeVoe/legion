import type { KeyboardEvent as ReactKeyboardEvent } from "react";

/**
 * Whether `event` is an input method's key, which acts on nothing of the page's: one pressed in
 * a composition, or the one that ends it, which Safari sends after its `compositionend` with
 * `isComposing` false and keyCode 229 (WebKit bug 165004). A reader typing Chinese, Japanese or
 * Korean confirms a word with Enter and drops a candidate with Escape, and those keys are the
 * input method's, not presses of the field's own.
 *
 * Who reads it, and no one keeps a check of their own: every handler of Enter or Escape typed
 * into a text field - an input, a textarea or a contenteditable - React's `onKeyDown` and a
 * native listener alike, the field's own or one on the document or the window that a field's key
 * reaches, and every handler that passes over a composition, the key registry's among them
 * (`features/shell/keymap.ts`), which the page's bound keys go through. A listener that only
 * notes that a key was pressed acts on no key and is not in that range; nor is one that only ends
 * a held press, which any key ends (`features/issue-page/press-focus.ts`'s `trackPress`), such as
 * the outline's Escape that lets go of the row a press kept (`IssueTree.tsx`'s `useReleaseFocus`);
 * nor are Arrow, Tab and Backspace handlers of a field, unless they pass over a composition, as
 * the row editor's Tab does (`features/issue-page/useNodeEditing.ts`).
 */
export function inputMethodKey(event: KeyboardEvent | ReactKeyboardEvent): boolean {
  const key = "nativeEvent" in event ? event.nativeEvent : event;
  return key.isComposing || key.keyCode === 229;
}
