import { expect, test } from "bun:test";

import { quoteMarkdown, withQuote } from "./quote-reply";

test("a one-line selection is one quote line, a blank line after it, and the cursor under that", () => {
  const quoted = withQuote("", "go with B per the train");
  expect(quoted).toEqual({
    caret: "> go with B per the train\n\n".length,
    text: "> go with B per the train\n\n",
  });
});

test("each line of a multi-line selection is a paragraph of the quote", () => {
  // Two paragraphs as the page joins them, a blank line between, and an option's label under.
  const selected = "The R3 audit decides it.\n\nRecommended: ship it as is.\nShip as is";
  expect(quoteMarkdown(selected)).toBe(
    "> The R3 audit decides it.\n>\n> Recommended: ship it as is.\n>\n> Ship as is"
  );
  const quoted = withQuote("", selected);
  expect(quoted?.text).toBe(
    "> The R3 audit decides it.\n>\n> Recommended: ship it as is.\n>\n> Ship as is\n\n"
  );
  expect(quoted?.caret).toBe(quoted?.text.length);
});

test("the page's own spacing goes: carriage returns, no-break spaces, edge and trailing blanks", () => {
  expect(quoteMarkdown("\n  first line  \r\nsecond\u00a0line\r\n\r\n\r\n")).toBe(
    ">   first line\n>\n> second line"
  );
});

test("a quote goes at the end of what the box holds, after a blank line", () => {
  expect(withQuote("Ship it.", "It writes nothing")?.text).toBe(
    "Ship it.\n\n> It writes nothing\n\n"
  );
  // The box's own trailing lines and spaces are the one blank line the quote stands after.
  expect(withQuote("Ship it.\n\n\n  ", "It writes nothing")?.text).toBe(
    "Ship it.\n\n> It writes nothing\n\n"
  );
  // A box holding only blank space is an empty one.
  expect(withQuote(" \n", "It writes nothing")?.text).toBe("> It writes nothing\n\n");
});

test("a second quote follows the first, and the cursor is under the newest", () => {
  const first = withQuote("", "the ledger moves first");
  const second = withQuote(`${first?.text}Agreed.`, "the mirror replays");
  expect(second?.text).toBe("> the ledger moves first\n\nAgreed.\n\n> the mirror replays\n\n");
  expect(second?.caret).toBe(second?.text.length);
  expect(second?.text.slice(0, second.caret).endsWith("> the mirror replays\n\n")).toBe(true);
});

test("a selection of blank space quotes nothing", () => {
  expect(quoteMarkdown(" \n\t\n")).toBe("");
  expect(withQuote("Ship it.", "  \n ")).toBeNull();
});
