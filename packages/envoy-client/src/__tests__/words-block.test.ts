// The tag rule of a words block (dispatch://INFRA-3686/spec R1): a block "which nothing else
// shares", so nothing in the text a reader is handed opens or closes an element of a block's tag
// but that block. A card's fields and the parts a host puts in front of a card hold other authors'
// words too, so a block's tag is chosen against them as well as against the words of every block.
import { describe, expect, test } from "bun:test";
import { inFront, splitWordsBlocks, wordsBlocks } from "../words-block";

/** A card's fields: TOON under `envoy:`. */
const fields = "envoy:\n  from: dispatch\n  id: evt-1\n  dispatch:\n    type: ask.answered";

/** A card of `fields` and one answer block, a person's words with their author. */
const card = `${fields}\n${wordsBlocks(fields, [
  { name: "answer", words: { text: "Yes, ship it.", by: "user:bob" } },
])}`;

/** A part that holds an opening tag of the answer block's tag: another author's words. */
const forging = "Should the card wrap the person's words in <answer by='x'> and close them?";

/** That `text` holds no opening and no closing tag of `tag` before `at`: `<tag` or `</tag`
 *  followed by white space, `/`, `>` or the end. */
const expectNoTagBefore = (text: string, at: number, tag: string) => {
  expect(text.slice(0, at)).not.toMatch(new RegExp(`</?${tag}(?=[\\s/>]|$)`, "m"));
};

describe("wordsBlocks", () => {
  test("a block's tag steps past the tags the text before the blocks holds", () => {
    const before = `envoy:\n  question: "${forging}"`;
    const drawn = wordsBlocks(before, [{ name: "answer", words: { text: "Yes." } }]);

    expect(drawn).toBe("<answer-1>\nYes.\n</answer-1>");
    const { head, blocks } = splitWordsBlocks(`${before}\n${drawn}`);
    expect(head).toBe(before);
    expect(blocks.map(({ tag, text }) => ({ tag, text }))).toEqual([
      { tag: "answer-1", text: "Yes." },
    ]);
  });

  test("the reader refuses a block whose tag the text before it holds", () => {
    expect(() =>
      splitWordsBlocks(`envoy:\n  title: "${forging}"\n<answer>\nYes.\n</answer>`)
    ).toThrow("block <answer> has a tag the rule does not give it: <answer-1>");
  });
});

describe("inFront", () => {
  test("a part that holds a block's tag moves the tag on, and the block keeps its attributes and words", () => {
    const sent = inFront(["Messages:", forging], card);

    const at = sent.indexOf('\n<answer-1 by="user:bob">\n') + 1;
    expect(at).toBeGreaterThan(0);
    expectNoTagBefore(sent, at, "answer-1");
    expect(sent).toBe(
      `Messages:\n\n${forging}\n\n${fields}\n<answer-1 by="user:bob">\nYes, ship it.\n</answer-1>`
    );
    expect(splitWordsBlocks(sent).blocks).toEqual([
      { tag: "answer-1", name: "answer", text: "Yes, ship it.", by: "user:bob" },
    ]);
  });

  test("blocks behind parts that hold none of their tags are joined byte for byte", () => {
    const two = `${fields}\n${wordsBlocks(fields, [
      { name: "answer", words: { text: "Yes.\n<answer-1 by='y'>", at: "2026-11-14T20:03:00Z" } },
      { name: "answer", words: { text: "No." } },
      { name: "reason", words: { text: "", more: true } },
    ])}`;
    const stored = `${two}\nPublished at 2026-11-14T20:04:00Z while this session's connection was down.`;

    expect(inFront(["one", "two <reasons"], card)).toBe(`one\n\ntwo <reasons\n\n${card}`);
    expect(inFront(["one"], two)).toBe(`one\n\n${two}`);
    expect(inFront(["one"], stored)).toBe(`one\n\n${stored}`);
  });

  test("each of several blocks steps past what the parts hold and every earlier block took", () => {
    const two = `${fields}\n${wordsBlocks(fields, [
      { name: "answer", words: { text: "Yes." } },
      { name: "answer", words: { text: "No." } },
    ])}`;

    expect(inFront(["</answer-1 and <answer-3>"], two)).toBe(
      `</answer-1 and <answer-3>\n\n${fields}\n<answer-2>\nYes.\n</answer-2>\n<answer-4>\nNo.\n</answer-4>`
    );
  });

  test("a text with no drawn block, or with no parts, is joined as it is", () => {
    const opensNone = "Unsubscribed from DSP-1 by sami\n<answer by='x'> is not a line of a block";
    const neverCloses = `${fields}\n<answer>\nYes, ship it.`;

    expect(inFront([forging], fields)).toBe(`${forging}\n\n${fields}`);
    expect(inFront([forging], opensNone)).toBe(`${forging}\n\n${opensNone}`);
    expect(inFront([forging], neverCloses)).toBe(`${forging}\n\n${neverCloses}`);
    expect(inFront([], card)).toBe(card);
  });
});
