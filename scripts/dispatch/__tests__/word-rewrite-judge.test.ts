import { describe, expect, test } from "bun:test";
import {
  buildPrompt,
  checkAnswer,
  confined,
  judge,
  occurrenceSpans,
  type Question,
  readAnswer,
  sentenceSpans,
} from "../word-rewrite/judge.ts";
import { builtInMap } from "../word-rewrite/name-map.ts";
import { FakeModel } from "./word-rewrite-fake.ts";

/** The judge's prompt and its check of each answer. This file is one of the places the word may
 *  stand (R1, kind 4). */

const PARAGRAPH =
  "The release is due on Monday. This issue was parked until it lands. The tests pass.";
const JUDGED = occurrenceSpans(PARAGRAPH);

function question(paragraph = PARAGRAPH, overrides: Partial<Question> = {}): Question {
  return {
    paragraph,
    judged: occurrenceSpans(paragraph),
    kept: [],
    kind: "issue document",
    status: "backlog",
    statusEvents: ["2026-11-24T10:04:00Z todo", "2026-11-24T10:30:00Z backlog"],
    entries: builtInMap(220),
    ...overrides,
  };
}

describe("confined", () => {
  test("takes a rewrite of the occurrence's sentence alone", () => {
    const after =
      "The release is due on Monday. This issue was moved to backlog until it lands. The tests pass.";
    expect(confined(PARAGRAPH, after, JUDGED)).toBe(true);
  });

  test("refuses a rewrite that changes a sentence without an occurrence", () => {
    const after =
      "The release is due on Tuesday. This issue was moved to backlog until it lands. The tests pass.";
    expect(confined(PARAGRAPH, after, JUDGED)).toBe(false);
    const tail =
      "The release is due on Monday. This issue was moved to backlog until it lands. Tests pass.";
    expect(confined(PARAGRAPH, tail, JUDGED)).toBe(false);
  });

  test("refuses a rewrite that still holds the word", () => {
    const after =
      "The release is due on Monday. This issue was parked in backlog until it lands. The tests pass.";
    expect(confined(PARAGRAPH, after, JUDGED)).toBe(false);
  });

  test("keeps the stretch between two rewritten sentences as it was", () => {
    const two = "It was parked. Nothing else moved. It is still parked.";
    const spans = occurrenceSpans(two);
    expect(
      confined(two, "It was moved to backlog. Nothing else moved. It is still in backlog.", spans)
    ).toBe(true);
    expect(
      confined(two, "It was moved to backlog. Something moved. It is still in backlog.", spans)
    ).toBe(false);
  });

  test("keeps an occurrence a place leaves, and refuses a rewrite that drops it", () => {
    const mixed = "The parked-decision notice stays. The tree was parked.";
    const [kept, judged] = occurrenceSpans(mixed);
    if (kept === undefined || judged === undefined) throw new Error("two occurrences");
    expect(
      confined(
        mixed,
        "The parked-decision notice stays. The tree was moved to backlog.",
        [judged],
        [kept]
      )
    ).toBe(true);
    expect(
      confined(
        mixed,
        "The kept-decision notice stays. The tree was moved to backlog.",
        [judged],
        [kept]
      )
    ).toBe(false);
  });

  test("checks a kept occurrence by its place: a renamed kept name beside a judged use left as it was is refused", () => {
    const one = "The parked-decision notice says the tree is parked.";
    const [kept, judged] = occurrenceSpans(one);
    if (kept === undefined || judged === undefined) throw new Error("two occurrences");
    const asked = question(one, { judged: [judged], kept: [kept] });
    const renamed = "The kept-decision notice says the tree is parked.";
    expect(confined(one, renamed, [judged], [kept])).toBe(false);
    expect(checkAnswer(asked, { kind: "rewrite", paragraph: renamed })).toEqual({
      kind: "judge-refused",
      reason: 'the rewrite changed "parked-decision", which had to stay',
    });
    const right = "The parked-decision notice says the tree is moved to backlog.";
    expect(checkAnswer(asked, { kind: "rewrite", paragraph: right })).toEqual({
      kind: "rewrite",
      paragraph: right,
    });
  });

  test("names the kept name a rewrite changed, though the rewrite holds no trace of the word", () => {
    const one = "The parked-decision notice says the tree is parked.";
    const [kept, judged] = occurrenceSpans(one);
    if (kept === undefined || judged === undefined) throw new Error("two occurrences");
    const asked = question(one, { judged: [judged], kept: [kept] });
    for (const changed of [
      "The kept-decision notice says the tree is moved to backlog.",
      "The notice says the tree is moved to backlog.",
    ]) {
      expect(checkAnswer(asked, { kind: "rewrite", paragraph: changed })).toEqual({
        kind: "judge-refused",
        reason: 'the rewrite changed "parked-decision", which had to stay',
      });
    }
  });

  test("sentences end at a stop before white space, or at a line end, and cover the text", () => {
    const text = "One. Two!\nThree? four";
    const spans = sentenceSpans(text);
    expect(spans.map((span) => text.slice(span.start, span.end))).toEqual([
      "One.",
      " Two!",
      "\n",
      "Three?",
      " four",
    ]);
  });
});

describe("the answer", () => {
  test("is the rewrite or an allowed reason, as one JSON object", () => {
    expect(readAnswer('{"rewrite": "text"}')).toEqual({ kind: "rewrite", paragraph: "text" });
    expect(readAnswer('Here: {"allowed": "quote"}')).toEqual({ kind: "allowed", allowed: "quote" });
  });

  test("that is not the JSON shape is judge-refused", () => {
    for (const text of [
      "I would rather not.",
      '{"rewrite": 3}',
      '{"allowed": "because"}',
      '{"rewrite": "a", "allowed": "quote"}',
      "[]",
      "{broken",
    ]) {
      expect(readAnswer(text).kind).toBe("judge-refused");
    }
  });

  test("a refused first answer is asked once more, naming what changed outside the sentence", async () => {
    const rewrite =
      "The release is due on Monday. This issue was moved to backlog until it lands. The tests pass.";
    const answers = [
      JSON.stringify({ rewrite: "Everything changed." }),
      JSON.stringify({ rewrite }),
    ];
    const model = new FakeModel(() => answers.shift() ?? null);
    expect(await judge(question(), model)).toEqual({
      judgement: { kind: "rewrite", paragraph: rewrite },
      asks: 2,
    });
    expect(model.prompts).toHaveLength(2);
    expect(model.prompts[0]).not.toContain("was refused");
    expect(model.prompts[1]).toContain(
      'Your earlier answer to this paragraph was refused: the rewrite changed the text before the marked sentences, which had to stay exactly "The release is due on Monday."'
    );
  });

  test("an answer that is not the shape is asked again, saying why; two refusals are the judgement", async () => {
    const model = new FakeModel(() => "I would rather not.");
    const judged = await judge(question(), model);
    expect(judged.asks).toBe(2);
    expect(judged.judgement.kind).toBe("judge-refused");
    expect(model.prompts[1]).toContain("the answer was not one JSON object");
  });

  test("a confined rewrite is taken at once, and no answer at all decides nothing", async () => {
    const rewrite =
      "The release is due on Monday. This issue was moved to backlog until it lands. The tests pass.";
    expect(await judge(question(), new FakeModel(() => JSON.stringify({ rewrite })))).toEqual({
      judgement: { kind: "rewrite", paragraph: rewrite },
      asks: 1,
    });
    expect(await judge(question(), new FakeModel(() => null))).toEqual({
      judgement: { kind: "no-answer" },
      asks: 1,
    });
  });

  test("the prompt gives the paragraph, the status, its moves, the names that apply and the marks", () => {
    const prompt = buildPrompt(question());
    expect(prompt.system).toContain('{"rewrite"');
    expect(prompt.user).toContain(PARAGRAPH);
    expect(prompt.user).toContain("The issue's status: backlog.");
    expect(prompt.user).toContain("2026-11-24T10:30:00Z backlog");
    expect(prompt.user).toContain("- parked-decision -> kept-decision (a code name)");
    expect(prompt.user).toContain(`- at ${JUDGED[0]?.start}: "park"`);
  });

  test("the prompt has a status use written in Dispatch's status words, and never held, on hold or paused", () => {
    const { system } = buildPrompt(question());
    for (const words of [
      '"moved to backlog"',
      '"in backlog"',
      '"in icebox"',
      '"in backlog or icebox"',
    ]) {
      expect(system).toContain(words);
    }
    const flat = system.replace(/\s+/g, " ");
    expect(flat).toContain(
      'Never write "held", "on hold", "paused", "shelved", "set aside", "deferred", "suspended" or any other word for a status use'
    );
    // A status use speaks of work in the tracker: an issue, a tree, a plan, a branch of work.
    expect(system).toContain("an issue, a tree, a plan, a branch of work or a release");
  });
});
