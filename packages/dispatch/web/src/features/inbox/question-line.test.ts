import { expect, test } from "bun:test";

import { questionLine } from "./question-line";

test("questionLine leaves a plain question as it is", () => {
  expect(questionLine("Which approach?")).toBe("Which approach?");
});

test("questionLine drops a heading marker", () => {
  expect(questionLine("## Ship behind a flag?")).toBe("Ship behind a flag?");
});

test("questionLine keeps emphasised words without their markers", () => {
  expect(questionLine("Ship **behind a flag**, or _wait_ for the review?")).toBe(
    "Ship behind a flag, or wait for the review?"
  );
  // Underscores inside a word are the word's, not emphasis.
  expect(questionLine("Rename snake_case_name?")).toBe("Rename snake_case_name?");
});

test("questionLine keeps a link's text and drops its target", () => {
  expect(questionLine("Approve [the rollout plan](dispatch://CORE-1/spec) today?")).toBe(
    "Approve the rollout plan today?"
  );
});

test("questionLine keeps inline code without its backticks", () => {
  expect(questionLine("Should `retries` stay at ``3``?")).toBe("Should retries stay at 3?");
});

test("questionLine reads a backslash-escaped punctuation mark as the mark alone", () => {
  expect(questionLine("1\\. Verify Linux")).toBe("1. Verify Linux");
  // Escaped, a star is a star and not emphasis.
  expect(questionLine("Ship \\*all\\* of it")).toBe("Ship *all* of it");
  // Escaped backticks outside code are backticks, and open no code span.
  expect(questionLine("Type \\`ls\\` here")).toBe("Type `ls` here");
});

test("questionLine keeps a code span's backslashes and stars as written", () => {
  // A backslash escapes nothing inside code, so one before the closing backtick is the code's
  // own, and the span still closes there.
  expect(questionLine("Use `C:\\temp\\`")).toBe("Use C:\\temp\\");
  expect(questionLine("Open `\\\\server\\share` now")).toBe("Open \\\\server\\share now");
  expect(questionLine("Escape `a\\.b` and `a*b*c`")).toBe("Escape a\\.b and a*b*c");
  // A run of backticks nothing closes is backticks as written.
  expect(questionLine("A stray ` backtick")).toBe("A stray ` backtick");
});

test("questionLine keeps a private-use character the question holds, beside escapes and code", () => {
  // A font's icon is a private-use code point: it is the question's own, and stays.
  expect(questionLine("Icon \uf000 Review this")).toBe("Icon \uf000 Review this");
  expect(questionLine("Literal \ue100 text")).toBe("Literal \ue100 text");
  // U+E000 is the first code point an escape or a code span could be held as: one the line
  // holds already is not taken, and both are given back as written.
  expect(questionLine("\ue000 1\\. `a*b` \ue001")).toBe("\ue000 1. a*b \ue001");
});

test("questionLine takes the first line that holds anything", () => {
  expect(questionLine("\n   \n# The question\n\nAnd the detail below it.\n")).toBe("The question");
});
