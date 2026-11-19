import { expect, test } from "bun:test";
import { controlCharacterPattern } from "@legion/contracts";
import { checkSync } from "recheck/lib/browser";

import { referencePattern } from "./reference-spans";
import { dispatchReferencePattern, issueKeyOnlyPattern, projectKeyOnlyPattern } from "./routes";
import { githubIssuePath, trailingRun, whitespaceOnly } from "./Unfurl";

/**
 * Whether `expression` can take more than one pass over a text, decided from the expression as
 * written rather than from a run of it: a regular expression that backtracks does it inside one
 * call, where neither a clock nor a count of calls can see it. `recheck`'s automaton checker
 * proves the answer from the expression's automaton. This is its pure JavaScript build
 * (`recheck/lib/browser`), which runs in this process: no worker, no Java and no native binary.
 * Its own deadline is off (`timeout: null`), so a loaded machine slows the answer and never
 * changes it.
 */
function verdict(expression: RegExp) {
  return checkSync(expression.source, expression.flags, { checker: "automaton", timeout: null });
}

// Every expression the one-pass rows' bodies reach (`composer-references.test.ts`,
// `Unfurl.test.tsx`, `text-references.test.ts`): the reference grammar runs over a whole body, a
// reference read from it is parsed with the next four, and a GitHub URL's path is read with the
// sixth. The last two are what the unfurl cards read a body with between its references and
// after each one.
test.each([
  ["the reference grammar", referencePattern],
  ["a control character", controlCharacterPattern],
  ["a `dispatch://` reference", dispatchReferencePattern],
  ["an issue key", issueKeyOnlyPattern],
  ["a project key", projectKeyOnlyPattern],
  ["a GitHub issue's path", githubIssuePath],
  ["the whitespace between references", whitespaceOnly],
  ["the run after a reference", trailingRun],
])("the expression of %s reads a text in one pass", (_name, expression) => {
  expect(verdict(expression)).toMatchObject({ checker: "automaton", status: "safe" });
});

// The control of the rows above: the expressions those readers replaced, which the same check
// calls vulnerable. The unfurl cards once matched a body against the first, which splits a body
// of repeated schemes every way it can and ran for minutes in V8; the trim once cut a
// reference's closing run with the second, and a mention's trailing `_` and `-` with the third
// (`trimmedMention`), each of which reads a run again from every position in it.
test.each([
  ["a body of references", /^(?:(?:dispatch:\/\/|https?:\/\/)\S+\s*)+$/, { type: "exponential" }],
  ["the end of a reference", /[),.;:!?]+$/, { degree: 2, type: "polynomial" }],
  ["the end of a mention", /[_-]+$/, { degree: 2, type: "polynomial" }],
] as const)("the check calls the old expression for %s vulnerable", (_name, expression, complexity) => {
  expect(verdict(expression)).toMatchObject({
    checker: "automaton",
    complexity,
    status: "vulnerable",
  });
});
