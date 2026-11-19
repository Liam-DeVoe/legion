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

test("questionLine takes the first line that holds anything", () => {
  expect(questionLine("\n   \n# The question\n\nAnd the detail below it.\n")).toBe("The question");
});
