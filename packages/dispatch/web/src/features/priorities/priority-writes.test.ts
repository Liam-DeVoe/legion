import { expect, test } from "bun:test";
import { expectedText, pressedText, settledText, type TextChain } from "./priority-writes";

/** A press as the hook makes it: what the write names as `expected`, and the chain after it. */
function press(chain: TextChain | undefined, text: string, held: string) {
  return { chain: pressedText(chain, text, held), expected: expectedText(chain, held) };
}

/** The chain after one of its writes settled, which a test knows is still there. */
function settle(chain: TextChain, refused: boolean): TextChain {
  const next = settledText(chain, refused);
  if (next === undefined) throw new Error("the chain ended");
  return next;
}

test("the first write names the text the page reads", () => {
  expect(press(undefined, "One", "Old").expected).toBe("Old");
});

test("a write pressed while one is out names the text of the one out, which the server holds by its turn", () => {
  const first = press(undefined, "One", "Old");
  const second = press(first.chain, "Two", "Old");
  expect(second.expected).toBe("One");
  // And a third, the second still out after the first landed, names the second's.
  expect(press(settle(second.chain, false), "Three", "Old").expected).toBe("Two");
});

test("once the write lands and the page's read has not moved, the next names the text written", () => {
  const first = press(undefined, "One", "Old");
  expect(press(settle(first.chain, false), "Two", "Old").expected).toBe("One");
});

test("a read catching up with the writes part way names the last text, and one that caught up names it", () => {
  const first = press(undefined, "One", "Old");
  const second = press(first.chain, "Two", "Old");
  const landed = settle(settle(second.chain, false), false);
  // The read shows the first write's answer, the second's not yet: the server holds the second.
  expect(press(landed, "Three", "One").expected).toBe("Two");
  expect(press(landed, "Three", "Two").expected).toBe("Two");
});

test("once the read moves to another person's text, the next write names that text", () => {
  const first = press(undefined, "One", "Old");
  const landed = settle(first.chain, false);
  expect(press(landed, "Two", "Written elsewhere").expected).toBe("Written elsewhere");
});

test("a read that caught up ends the chain: a text read before it is then another person's", () => {
  const first = press(undefined, "One", "Old");
  // The read caught up with the first write before the second was pressed.
  const second = press(settle(first.chain, false), "Two", "One");
  expect(second.expected).toBe("One");
  // Another person writes "Old" again once the second landed: the server holds it, and a write
  // naming "Two" would be refused each time it was pressed.
  expect(press(settle(second.chain, false), "Three", "Old").expected).toBe("Old");
});

test("a refused write breaks the chain: once none is out, the next write names the text read", () => {
  const first = press(undefined, "One", "Old");
  const second = press(first.chain, "Two", "Old");
  // The first is refused while the second is out: the second still goes after it.
  const refused = settle(second.chain, true);
  expect(press(refused, "Three", "Written elsewhere").expected).toBe("Two");
  expect(settledText(refused, false)).toBeUndefined();
  expect(press(settledText(refused, false), "Three", "Written elsewhere").expected).toBe(
    "Written elsewhere"
  );
  // And one refused with nothing else out ends it at once.
  expect(settledText(first.chain, true)).toBeUndefined();
});
