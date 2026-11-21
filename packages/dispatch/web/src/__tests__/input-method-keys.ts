// biome-ignore lint/style/noRestrictedImports: user-event cannot mark a key as an input method's (`isComposing`, keyCode 229), and every row that sends such a key sends it here
import { fireEvent } from "@testing-library/react";

/** What each `keyDown` of `pressAsInputMethod` returned, as `dispatchEvent` answers it: `false`
 *  where a listener called `preventDefault`. */
export interface InputMethodPresses {
  /** The key pressed in a composition, marked `isComposing`. */
  readonly composing: boolean;
  /** The same key, plain. */
  readonly plain: boolean;
  /** The key that ends a composition as Safari sends it, after its `compositionend`: unmarked,
   *  with keyCode 229 (WebKit bug 165004). */
  readonly safari: boolean;
}

/**
 * Sends `key` to `target` as an input method's key, in both of the forms `inputMethodKey` reads
 * (`hooks/inputMethodKey.ts`), then plain, each with what `held` holds - a modifier pressed with
 * it - and answers what each `keyDown` returned. `beforePlain` runs between the two and the plain
 * key, where a row reads what the input method's keys left.
 *
 * A reader typing Chinese, Japanese or Korean confirms a word with Enter and drops a candidate with
 * Escape, and those keys are the input method's: a handler that reads the predicate leaves them
 * alone and acts on the plain key. A handler that calls `preventDefault` as it acts answers
 * `{ composing: true, plain: false, safari: true }`; with its predicate taken out, the input
 * method's keys go the same way as the plain one and answer `false` too.
 */
export function pressAsInputMethod(
  target: Document | Element | Window,
  key: string,
  {
    beforePlain,
    held = {},
  }: { readonly beforePlain?: () => void; readonly held?: KeyboardEventInit } = {}
): InputMethodPresses {
  // user-event cannot mark a key as an input method's, so the two such keys are fired, and the
  // plain key goes the same way, so only the composition mark differs.
  const composing = fireEvent.keyDown(target, { ...held, isComposing: true, key });
  const safari = fireEvent.keyDown(target, { ...held, isComposing: false, key, keyCode: 229 });
  beforePlain?.();
  const plain = fireEvent.keyDown(target, { ...held, key });
  return { composing, plain, safari };
}
