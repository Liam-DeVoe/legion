// The mod cannot import `@legion/envoy-client`, so `../hooks/words-in-front.ts` writes the rule of
// its `inFront` a second time. The two must give the same text for the same input, or a card in
// the prompt at the rows a turn never read takes other tags than the same card in the Codex
// inbox's hook text.
import { expect, test } from "bun:test"
import { inFront as shared } from "@legion/envoy-client/words-block"
import { inFront as mods } from "../hooks/words-in-front"

const fields = "envoy:\n  from: dispatch\n  id: evt-1\n  dispatch:\n    type: ask.answered"
const answer = `<answer by="user:bob" at="2026-11-14T20:03:00Z">\nYes, ship it.\n</answer>`

const cases: readonly {
  readonly label: string
  readonly parts: string[]
  readonly text: string
}[] = [
  { label: "no parts", parts: [], text: `${fields}\n${answer}` },
  { label: "a text with no block", parts: ["<answer by='x'>"], text: fields },
  { label: "parts that hold no tag", parts: ["one", "two"], text: `${fields}\n${answer}` },
  { label: "a part that opens the tag", parts: ["<answer by='x'>"], text: `${fields}\n${answer}` },
  {
    label: "parts that hold suffixed tags",
    parts: ["</answer-1 and <answer-12", "<answer-0"],
    text: `${fields}\n${answer}`,
  },
  {
    label: "blocks of one name and of two",
    parts: ["<answer"],
    text: `${fields}\n<answer>\nYes.\n</answer>\n<answer-1>\nNo.\n</answer-1>\n<reason more="true">\n\n</reason>`,
  },
  {
    label: "a block whose words hold a step of its tag",
    parts: ["<answer"],
    text: `${fields}\n<answer-2>\nYes.\n<answer-1 by='y'>\n</answer-2>`,
  },
  {
    label: "text after the blocks",
    parts: ["<answer"],
    text: `${fields}\n${answer}\nPublished at 2026-11-14T20:04:00Z while this session's connection was down.`,
  },
  { label: "a block at the start", parts: ["<answer"], text: answer },
  {
    label: "a line that opens no block",
    parts: ["<answer"],
    text: `${fields}\n<answer by='x'> is no block\n${answer}`,
  },
  { label: "a block that never closes", parts: ["<answer"], text: `${fields}\n<answer>\nYes.` },
  {
    label: "a label line in front of the card",
    parts: ["Envoy delivered a message.", "<answer by='x'>"],
    text: `[fence] An Envoy message delivered to this session:\n${fields}\n${answer}`,
  },
]

test("the mod's inFront gives the text the shared inFront gives", () => {
  for (const { label, parts, text } of cases) {
    expect(mods(parts, text), label).toBe(shared(parts, text))
  }
})
