import type { KeyboardEvent as ReactKeyboardEvent } from "react";

/**
 * Whether `event` is an input method's key, which acts on nothing of the page's: one pressed in
 * a composition, or the one that ends it, which Safari sends after its `compositionend` with
 * `isComposing` false and keyCode 229 (WebKit bug 165004). A reader typing Chinese, Japanese or
 * Korean confirms a word with Enter, and that Enter is the input method's, not a press of the
 * field's own: every key handler of a field a reader types words into reads this first, React's
 * `onKeyDown` and a native listener alike, so no handler keeps a copy of its own.
 */
export function inputMethodKey(event: KeyboardEvent | ReactKeyboardEvent): boolean {
  const key = "nativeEvent" in event ? event.nativeEvent : event;
  return key.isComposing || key.keyCode === 229;
}
