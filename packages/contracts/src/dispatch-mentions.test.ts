import { expect, test } from "bun:test";
import {
  isMentionWords,
  MENTION_SIGN,
  mentionDrawn,
  mentionName,
  readWrittenWords,
  referenceReading,
  type WordsFormatting,
  wordsFormatting,
  wordsPieceUnder,
} from "./dispatch-mentions";

const AGENT = "dispatch://agent/s1";

// R8's three readings, each with the case beside it that reads another way.
test("an address on its own and a link whose words are its address read as the name", () => {
  expect(referenceReading({ address: AGENT, party: true })).toBe("name");
  expect(referenceReading({ address: AGENT, party: true, words: AGENT })).toBe("name");
  // A Markdown autolink of a bare `www.` address gives its link an `http://` destination.
  const page = "http://www.dispatch.test/issues/CORE-1";
  expect(
    referenceReading({ address: page, party: false, words: "www.dispatch.test/issues/CORE-1" })
  ).toBe("name");
  expect(referenceReading({ address: page, party: false, words: "the issue" })).toBe("words");
});

test("a party's link whose words open with the mention sign is a mention", () => {
  expect(referenceReading({ address: AGENT, party: true, words: "@Old name" })).toBe("mention");
  expect(referenceReading({ address: AGENT, party: true, words: "Old name" })).toBe("words");
  expect(referenceReading({ address: AGENT, party: true, words: " @Old name" })).toBe("words");
  // The same words to anything but a party keep their author's words.
  expect(referenceReading({ address: "dispatch://CORE-1", party: false, words: "@the fix" })).toBe(
    "words"
  );
});

/** A mark of Proof's schema, as far as `wordsPieceUnder` reads one. */
function mark(name: string): { type: { name: string; spec: { code?: boolean } } } {
  return { type: { name, spec: name === "inlineCode" ? { code: true } : {} } };
}

/** What a party's link whose words are `@x`, formatted as `formatting` says, reads as. */
function mentionReads(formatting: WordsFormatting): string {
  return referenceReading({
    address: AGENT,
    party: true,
    words: "@x",
    wordsFormatting: formatting,
  });
}

// The formatting rule's first case: a code span's `@` is the code's characters, never a sign
// its writer wrote, so code anywhere in the words keeps them.
test("code anywhere in a link's words keeps the words", () => {
  const code = wordsPieceUnder([mark("inlineCode"), mark("link")]);
  const plain = wordsPieceUnder([mark("link")]);
  expect(wordsFormatting([code])).toBe("code");
  expect(wordsFormatting([code, plain])).toBe("mixed");
  expect(isMentionWords("@x", "code")).toBe(false);
  expect(mentionReads("code")).toBe("words");
  expect(mentionReads("mixed")).toBe("words");
  // Words that are all code still name their target where they are its address.
  expect(
    referenceReading({ address: AGENT, party: true, words: AGENT, wordsFormatting: "code" })
  ).toBe("name");
});

// Its second case: in the parsed document `[**@x**](…)` and `**[@x](…)**` are one text, so a
// mark over all of the words is formatting around the link, and the link is still a mention. A
// mark that is no formatting of the author's - a comment's anchor, a search hit - changes
// nothing.
test("formatting over all of a link's words is still a mention", () => {
  const bold = wordsPieceUnder([mark("strong"), mark("link")]);
  const commented = wordsPieceUnder([mark("strong"), mark("link"), mark("proofComment")]);
  const marked = wordsPieceUnder([mark("strong"), mark("link"), mark("searchHighlight")]);
  expect(wordsFormatting([bold])).toBe("plain");
  expect(wordsFormatting([bold, commented, marked])).toBe("plain");
  expect(isMentionWords("@x")).toBe(true);
  expect(mentionReads("plain")).toBe("mention");
});

// Its third case: formatting over some of the words is the author formatting the words, which
// are then theirs, whatever they spell.
test("formatting over some of a link's words makes them the author's words", () => {
  const bold = wordsPieceUnder([mark("strong"), mark("link")]);
  const plain = wordsPieceUnder([mark("link")]);
  const struck = wordsPieceUnder([mark("link"), mark("strike_through")]);
  expect(wordsFormatting([plain, bold])).toBe("mixed");
  expect(wordsFormatting([bold, struck])).toBe("mixed");
  expect(mentionReads("mixed")).toBe("words");
  expect(
    referenceReading({ address: AGENT, party: true, words: AGENT, wordsFormatting: "mixed" })
  ).toBe("words");
});

// A reader of a text nothing has parsed reads the same three cases out of the characters, and a
// link's words as a reader sees them: each escape undone, and the runs that write formatting
// gone. A run that opens nothing is the character it is.
test("written words read as a parsed document's do, formatting and all", () => {
  expect(readWrittenWords("@Planner")).toEqual({ formatting: "plain", words: "@Planner" });
  expect(readWrittenWords("\\@Planner \\[2\\]")).toEqual({
    formatting: "plain",
    words: "@Planner [2]",
  });
  expect(readWrittenWords("a\\\\b \\n C:\\dir")).toEqual({
    formatting: "plain",
    words: "a\\b \\n C:\\dir",
  });
  // All of the words under one formatting, however it is written.
  for (const written of ["**@Planner**", "_@Planner_", "~~@Planner~~", "***@Planner***"]) {
    expect(readWrittenWords(written)).toEqual({ formatting: "plain", words: "@Planner" });
  }
  expect(readWrittenWords("**@Plan**__ner__")).toEqual({ formatting: "plain", words: "@Planner" });
  // Some of them.
  expect(readWrittenWords("@Planner **now**")).toEqual({
    formatting: "mixed",
    words: "@Planner now",
  });
  expect(readWrittenWords("**@Planner** now")).toEqual({
    formatting: "mixed",
    words: "@Planner now",
  });
  // Code.
  expect(readWrittenWords("`@Planner`")).toEqual({ formatting: "code", words: "@Planner" });
  expect(readWrittenWords("@Planner `now`")).toEqual({
    formatting: "mixed",
    words: "@Planner now",
  });
  // Runs that write no formatting: inside a word, escaped, or closed by nothing.
  for (const written of ["@snake_case_name", "@a*b", "@x `y", "2 * 3 * 4"]) {
    expect(readWrittenWords(written)).toEqual({ formatting: "plain", words: written });
  }
  expect(readWrittenWords("\\*\\*@x\\*\\*")).toEqual({ formatting: "plain", words: "**@x**" });
});

test("a mention draws the sign and the name", () => {
  expect(MENTION_SIGN).toBe("@");
  expect(mentionName("Planner now")).toBe("@Planner now");
});

// A party that exists with no name of its own reads as the sign and its short form, so the
// words its writer chose never pass for a current name; one that has not answered keeps them.
test("a mention draws the party's name, its short form where it has none, and nothing unanswered", () => {
  expect(mentionDrawn({ known: false, title: "Planner now" }, "agent/s1")).toBe("@Planner now");
  expect(mentionDrawn({ known: true, title: undefined }, "agent/s1")).toBe("@agent/s1");
  expect(mentionDrawn({ known: false, title: undefined }, "agent/s1")).toBe(undefined);
});
