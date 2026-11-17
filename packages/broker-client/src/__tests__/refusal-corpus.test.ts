import { describe, expect, test } from "bun:test";
import { randomBytes } from "node:crypto";
import { readFileSync } from "node:fs";
import { join } from "node:path";
import { isKnownBrokerRefusal, KNOWN_OAUTH_ERROR_CODES } from "../refusal-text";
import { BrokerRefusedError } from "../token-client";

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

  // The codes shown are the library's own (the Go code corpus writes them from its error values).
  test("the codes the client shows are the codes the broker's library writes", () => {
    const codes: string[] = JSON.parse(
      readFileSync(
        join(import.meta.dir, "../../../envoy/internal/broker/oauth/testdata/refusal-codes.json"),
        "utf8"
      )
    );
    expect(codes.length).toBeGreaterThan(5);
    expect([...KNOWN_OAUTH_ERROR_CODES].sort()).toEqual(codes);
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

  // The policy's refusals are pinned to their own sentences (P-j): a refusal of another wording,
  // which could carry a token cut into pieces too short for the scrub, hex-encoded or reversed,
  // is withheld.
  const LIBRARY =
    "The request is missing a required parameter, includes an invalid parameter value, includes a parameter more than once, or is otherwise malformed.";
  const token = randomBytes(30).toString("base64url");
  test.each([
    ["cut every 9 characters", token.match(/.{1,9}/g)?.join(".") ?? ""],
    ["hex", Buffer.from(token).toString("hex")],
    ["reversed", [...token].reverse().join("")],
  ])("a policy refusal of another wording, with the token %s, is withheld", (_, encoded) => {
    const description = `${LIBRARY} The subject token was refused: ${encoded}.`;
    expect(isKnownBrokerRefusal(description, sent)).toBe(false);
    expect(new BrokerRefusedError(400, "invalid_request", description, "probe", sent).said).toBe(
      "(its text is withheld)"
    );
  });
});
