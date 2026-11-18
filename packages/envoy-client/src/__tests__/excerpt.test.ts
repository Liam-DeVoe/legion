// packages/envoy-client/src/__tests__/excerpt.test.ts
//
// The tokens a client presented lately (`SentTokens`, excerpt.ts), which every excerpt of an
// answer redacts beside the credential's secret now.

import { expect, test } from "bun:test";
import { fixtureSecret } from "@legion/test-fixtures/fixture-secret";
import { excerpt, SentTokens } from "../excerpt";

// F2 (the review of be17859eca): the token the credential holds now is one of the tokens it
// presented, so it is scanned once, not once as `secret()` and again as a kept token.
test("the token held now and the same token presented are one token to redact", () => {
  const live = fixtureSecret("live");
  const sent = new SentTokens();
  sent.note({ Authorization: `DPoP ${live}`, DPoP: "proof" });
  expect(sent.with(live)).toEqual([live]);
  expect(sent.with("")).toEqual([live]);
});

// The newest first, each once, and at most four besides the one held now.
test("a client keeps the tokens it presented, newest first, each once, at most four", () => {
  const tokens = Array.from({ length: 6 }, (_, index) => fixtureSecret(`t${index}`));
  const sent = new SentTokens();
  for (const token of tokens) sent.note({ Authorization: `Bearer ${token}` });
  sent.note({ authorization: `Bearer ${tokens[4]}` });
  const current = fixtureSecret("current");
  expect(sent.with(current)).toEqual([current, tokens[4], tokens[5], tokens[3], tokens[2]]);
});

// Every kept token is redacted from an answer, the one held now included.
test("an excerpt redacts every token a client presented and the one it holds now", () => {
  const old = fixtureSecret("old");
  const live = fixtureSecret("live");
  const sent = new SentTokens();
  sent.note({ Authorization: `DPoP ${old}` });
  const shown = excerpt(`echo ${old} and ${live}`, sent.with(live));
  expect(shown).toBe("echo [redacted] and [redacted]");
});
