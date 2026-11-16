import { expect, test } from "bun:test";

import { namedLine, oneLineCut } from "./one-line";
import { NODE_TYPE_NAMES } from "./page-words";

/** A face that draws every character 7 px wide, so each budget below is a sum a reader can
 *  check by hand. */
const widthOf = (text: string) => text.length * 7;

/** The line a row `room` px wide draws of `text`, with nothing beside it. */
function alone(text: string, room: number) {
  return oneLineCut({ after: 0, before: 0, room, text, widthOf });
}

/**
 * R1, R14: a Contents entry draws its node's first line on one line, and no entry truncates a
 * word, at every width the pane is drawn at (dispatch://INFRA-1196). The cut is made in pixels,
 * against the room the entry's own box leaves the line, so the ellipsis is part of what has to
 * fit.
 */
test("a one-line row cuts its first line at a word to the pixels it has, never inside one", () => {
  // Short enough: the whole line, and no ellipsis, since nothing was cut.
  expect(alone("Short enough.", 200)).toEqual({
    cut: false,
    line: "Short enough.",
    under: false,
    wraps: false,
  });

  // A sentence that ends inside the room ends the line, as it does on a row that wraps, and
  // says the text carries on.
  expect(alone("One sentence. And another.", 200)).toEqual({
    cut: true,
    line: "One sentence.",
    under: false,
    wraps: false,
  });

  // A whole sentence is drawn with no ellipsis after its own mark (`drawnLine`), so none is
  // counted: the sentence fits a room its 13 characters fill, where its ellipsis would not.
  expect(alone("One sentence. And another.", 13 * 7 + 1).line).toBe("One sentence.");

  // Longer than the room: the line ends at the last word whose run and ellipsis fit, so the
  // stored text carries on at a word break. 169 px less the pixel kept for rounding is 24
  // characters: "The page draws the" and its ellipsis are 19, "The page draws the whole" and
  // its are 25.
  const long = "The page draws the whole issue in one reading, with nothing lost.";
  const cut = alone(long, 169);
  expect(cut).toEqual({ cut: true, line: "The page draws the", under: false, wraps: false });
  expect(long.charAt(cut.line.length)).toBe(" ");

  // The ellipsis is counted: a run that fits only without it is one word too long.
  expect(alone(long, 8 * 7 + 1).line).toBe("The");
  expect(alone(long, 9 * 7 + 1).line).toBe("The page");

  // An empty text draws an empty line.
  expect(alone("", 100)).toEqual({ cut: false, line: "", under: false, wraps: false });
});

/**
 * The parts beside the line take from it: the twisty and the bullet before it, the waiting dot
 * and the marks after it. Where they leave it less than its first word, it goes on a line of its
 * own under them, where only the waiting dot and the marks share its width - an entry that kept
 * it beside them would draw it 0 px wide, and hide the parts after it past its own edge.
 */
test("a one-line row's first line goes under its other words where they leave it no word", () => {
  const long = "Does the fold open to the frontier on a phone too?";
  // 300 px, 200 before and 60 after: 39 px beside them, the pixel for rounding kept, and
  // "Does…" is 35.
  expect(oneLineCut({ after: 60, before: 200, room: 300, text: long, widthOf })).toEqual({
    cut: true,
    line: "Does",
    under: false,
    wraps: false,
  });
  // 230 before leaves 9 px, which holds no word: the line goes under, where it has
  // 300 - 60 - 1 = 239 px, which is 34 characters, and "Does the fold open to the frontier"
  // with its ellipsis is 35.
  expect(oneLineCut({ after: 60, before: 230, room: 300, text: long, widthOf })).toEqual({
    cut: true,
    line: "Does the fold open to the",
    under: true,
    wraps: false,
  });
  // Parts before it as wide as the row itself send it under too, where it is drawn whole.
  expect(oneLineCut({ after: 0, before: 400, room: 400, text: long, widthOf })).toEqual({
    cut: false,
    line: long,
    under: true,
    wraps: false,
  });
});

/**
 * A first word no line of the row holds - a path, a URL - is drawn whole and wraps inside
 * itself: the row's box cutting its tail would cut inside a word, which is the one thing R1
 * bans, and `firstLineOf` has already held a token longer than its own limit to that limit.
 */
test("a first word no line of the row holds is drawn whole, wrapping inside itself", () => {
  const path = `${"a".repeat(60)}/b.ts and the words after it.`;
  expect(oneLineCut({ after: 0, before: 100, room: 200, text: path, widthOf })).toEqual({
    cut: true,
    line: `${"a".repeat(60)}/b.ts`,
    under: true,
    wraps: true,
  });
  expect(alone("a".repeat(40), 100)).toEqual({
    cut: false,
    line: "a".repeat(40),
    under: true,
    wraps: true,
  });
});

/**
 * Reading (b) of dispatch://INFRA-3939's plan, the tester's F20: a plain line the page draws is
 * no link, so it names each reference in plain words - a block by the page's name for it, an
 * issue by its key, a whole document by the page's word for one - and drops a typed block's
 * source line, which is markup and no word of the text. No address, block id or fence is left.
 */
test("namedLine names each reference and drops a typed block's source line", () => {
  const block = "dispatch://KEY-1/spec#b-r1";
  const text = [
    ":::requirement{#r1}",
    `The page cites ${block}, dispatch://KEY-1 and dispatch://KEY-1/artifact/notes.`,
  ].join("\n");
  const line = namedLine(text, (reference) =>
    reference === block ? "the whole issue" : undefined
  );
  expect(line).toBe(`The page cites the whole issue, KEY-1 and ${NODE_TYPE_NAMES.document}.`);
  for (const mark of [":::", "#b-", "dispatch://"]) {
    expect(line).not.toContain(mark);
  }
});
