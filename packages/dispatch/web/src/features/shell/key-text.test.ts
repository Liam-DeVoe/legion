import { expect, test } from "bun:test";

import { keyText, keyTokens } from "./key-text";

/** Each modifier a key may hold as a binding writes it, and its glyph on a Mac and elsewhere: the
 *  one table every surface that draws a key reads (`KEY_GLYPHS`). */
const MODIFIER_GLYPHS = [
  ["$mod", "⌘", "Ctrl"],
  ["Control", "⌃", "Ctrl"],
  ["Ctrl", "⌃", "Ctrl"],
  ["Shift", "⇧", "Shift"],
  ["Alt", "⌥", "Alt"],
  ["Meta", "⌘", "Meta"],
] as const;

// On a Mac a modifier is drawn as its keycap prints it, and `$mod` is Cmd; elsewhere it is a
// word, and `$mod` is Ctrl. `isMac` names the platform, so neither is the test browser's own.
test("keyTokens and keyText draw each modifier from the one table, on a Mac and elsewhere", () => {
  for (const [modifier, mac, other] of MODIFIER_GLYPHS) {
    for (const [isMac, glyph] of [
      [true, mac],
      [false, other],
    ] as const) {
      const said = `${modifier}, isMac ${isMac}`;
      expect(keyTokens(`${modifier}+x`, isMac), said).toEqual([
        { key: `0:${modifier}+x`, parts: [glyph, "x"], token: `${glyph}+x` },
      ]);
      expect(keyText(`${modifier}+x`, isMac), said).toBe(`${glyph}+x`);
    }
  }
  // Each press of a key of two in turn, a space between them, as `?` draws them.
  expect(keyText("g Shift+i", true)).toBe("g ⇧+i");
  expect(keyText("g Shift+i", false)).toBe("g Shift+i");
});

// The two hints under an issue's comment box (`MentionComposer`): `$mod+K` opens the reference
// picker and `$mod+Enter` sends, each drawn with the platform's `$mod`.
test("the comment box's two keys are ⌘+K and ⌘+Enter on a Mac, and Ctrl+K and Ctrl+Enter elsewhere", () => {
  expect([keyText("$mod+K", true), keyText("$mod+Enter", true)]).toEqual(["⌘+K", "⌘+Enter"]);
  expect([keyText("$mod+K", false), keyText("$mod+Enter", false)]).toEqual([
    "Ctrl+K",
    "Ctrl+Enter",
  ]);
});
