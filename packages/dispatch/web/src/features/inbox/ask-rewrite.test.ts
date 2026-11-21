import { expect, test } from "bun:test";
import { act, renderHook } from "@testing-library/react";

import { toggleOriginal, useOriginalShown } from "./ask-rewrite";

// Which rewritten texts show their original is one module-level store, kept by the text's id,
// and rows draw their texts under the same ids. The row leaves its text showing the original, as
// a row that presses a badge once does. A store the preload does not empty after each row keeps
// that, and the row run again (`--rerun-each`), or a later row that draws a text of that id, finds
// the text on its original before anyone pressed anything.
test("a rewritten text shows its rewrite until its badge is pressed, from a store the row starts empty", () => {
  const { result, unmount } = renderHook(() => useOriginalShown("comment-rewritten"));
  try {
    expect(result.current, "the text opened on its original").toBe(false);
    act(() => toggleOriginal("comment-rewritten"));
    expect(result.current, "the press did not show the original").toBe(true);
  } finally {
    unmount();
  }
});
