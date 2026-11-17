import { describe, expect, test } from "bun:test";
import { randomBytes } from "node:crypto";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { BrokerRefusedError, isKnownBrokerRefusal } from "../token-client";

// The broker's refusals as its own OAuth library writes them on the wire: the library's fixed
// sentence, then the hint, or the broker's `<CODE>: ` ahead of its message. The Go broker's
// refusal-corpus test writes this file from the broker's library and checks each hint stands in
// its source, so this list and the broker's sentences cannot drift apart unnoticed (#4140, B1).
const corpus: string[] = JSON.parse(
  readFileSync(
    join(
      import.meta.dir,
      "../../../envoy/internal/broker/oauth/testdata/refusal-descriptions.json"
    ),
    "utf8"
  )
);
const sent = { person: "ada@example.com", session: "ses_corpus" };

describe("the broker's refusals as it writes them", () => {
  test("the corpus is not empty", () => {
    expect(corpus.length).toBeGreaterThan(10);
  });

  test.each(corpus)("%p prints whole", (description) => {
    expect(isKnownBrokerRefusal(description, sent)).toBe(true);
    expect(new BrokerRefusedError(400, "invalid_grant", description, "probe", sent).said).toBe(
      description
    );
  });

  // A slot holds only what the trade sent: another person, another session, or a token in a slot
  // the trade did not fill is withheld.
  test.each(
    corpus.filter((text) => text.includes(sent.person) || text.includes(sent.session))
  )("%p with a token in its slot is withheld", (description) => {
    const token = randomBytes(20).toString("hex");
    const echoed = description
      .replaceAll(sent.person, `${token}@example.com`)
      .replaceAll(sent.session, token);
    expect(isKnownBrokerRefusal(echoed, sent)).toBe(false);
    expect(
      Bun.inspect(new BrokerRefusedError(400, "invalid_grant", echoed, "probe", sent))
    ).not.toContain(token);
  });
});
