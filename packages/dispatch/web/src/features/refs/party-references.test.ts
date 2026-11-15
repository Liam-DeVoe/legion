import { expect, test } from "bun:test";

import {
  draftParties,
  isPartyMention,
  linkOf,
  partyMention,
  partyMentionSpan,
} from "./party-references";
import { partyOfAddress } from "./routes";
import { readTextReferences } from "./text-references";

const SESSION = "01a1058e-f14f-7684-87eb-3dc885955551";
const AGENT = `dispatch://agent/${SESSION}`;

// Who a draft addresses is in its own words, and a code context is where words are shown rather
// than written to somebody: the server drops a party it holds before any delivery rule reads the
// set (`api.commentPartiesOf`), and the composer must say the same of the draft in front of the
// reader. The three contexts below are the three the shared reader knows (`code-ranges.ts`).

test("a party written as a worded link's address is delivered to", () => {
  expect(draftParties(`Over to [Planner](${AGENT}) now.`)).toEqual({
    named: true,
    targets: [`session:${SESSION}`],
  });
});

test("a party in a fenced code block is no target and names nobody", () => {
  expect(draftParties(`Run this:\n\n\`\`\`sh\ndispatch send ${AGENT}\n\`\`\`\n`)).toEqual({
    named: false,
    targets: [],
  });
});

test("a party in an indented code block is no target and names nobody", () => {
  expect(draftParties(`Pasted from the handbook:\n\n    ${AGENT}\n`)).toEqual({
    named: false,
    targets: [],
  });
});

test("a party in a code span is no target and names nobody", () => {
  expect(draftParties("Run `dispatch message --to dispatch://role/reviewer` first.")).toEqual({
    named: false,
    targets: [],
  });
});

// A person is named but never delivered to (R5), and naming one is what makes a typed `/btw` a
// command: in code it does neither, so the draft's first word stays the reader's own.
test("a person in a code span leaves a draft naming nobody", () => {
  expect(draftParties("Mail `dispatch://person/alice@example.com` about it.")).toEqual({
    named: false,
    targets: [],
  });
});

test("a person named outside code names the draft without delivering to one", () => {
  expect(draftParties("Mail dispatch://person/alice@example.com about it.")).toEqual({
    named: true,
    targets: [],
  });
});

// Each occurrence stands on its own, as it does for the server: the one outside code names the
// party, whatever a fence elsewhere in the draft shows.
test("a party a draft also writes outside code is delivered to", () => {
  expect(
    draftParties("Run `dispatch://role/reviewer` and ask [the reviewer](dispatch://role/reviewer).")
  ).toEqual({ named: true, targets: ["role:reviewer"] });
});

test("a draft naming parties lists each once, in the order it names them", () => {
  expect(
    draftParties(`[Reviewer](dispatch://role/reviewer), [Planner](${AGENT}) and ${AGENT} again.`)
  ).toEqual({ named: true, targets: ["role:reviewer", `session:${SESSION}`] });
});

// The mention a channel takes out of its draft when the reader leaves it (`seededDraft`): the
// link's words with its address, and nothing at all where the only occurrence is in code - that
// text is the reader's own, not an address this channel owes.
test("a party's mention is found with the words that name it", () => {
  const body = `Over to [Planner](${AGENT}) now.`;

  expect(partyMentionSpan(body, `session:${SESSION}`)).toEqual({
    end: body.indexOf(") now.") + 1,
    start: body.indexOf("[Planner]"),
  });
});

test("a party written only in code is mentioned nowhere", () => {
  expect(partyMentionSpan(`Run \`dispatch send ${AGENT}\` first.`, `session:${SESSION}`)).toBe(
    undefined
  );
});

// A reference names an agent by the wider rule the Agents page shows session ids under, where a
// delivery route is `session:[A-Za-z0-9_-]+` (the server's `model.ParseRoute`). A body can
// therefore write an agent address no route reaches, and the server refuses such a write
// (`api.checkMentionRoute`): the composer says the same of the draft in front of the reader
// rather than listing a party who is told and then showing a refusal that says otherwise.
test("an agent address no delivery route reaches names the draft and is delivered to nobody", () => {
  expect(draftParties("[x](dispatch://agent/a.b) please look.")).toEqual({
    named: true,
    targets: [],
  });
  expect(partyMentionSpan("[x](dispatch://agent/a.b) please look.", "session:a.b")).toBe(undefined);
});

test("a delivery address is read as a party only where a route is written that way", () => {
  expect(partyOfAddress(`session:${SESSION}`)).toEqual({ kind: "agent-session", session: SESSION });
  expect(partyOfAddress("role:reviewer")).toEqual({ kind: "role", role: "reviewer" });
  for (const target of [
    "session:a.b",
    "session:",
    "session:x) [y](https://evil.example",
    "role:Reviewer",
    "role:",
    "agent:s1",
    SESSION,
  ]) {
    expect(partyOfAddress(target)).toBe(undefined);
    expect(partyMention(target, "Planner")).toBe(undefined);
  }
});

// The mention a channel seeds and a saved draft's old `@Name` becomes (`held-sends.ts`): the
// mention sign and the party's name, which every reader then reads as `@` and the party's
// current name (R4).
test("a party's mention is written as the mention sign and its name", () => {
  expect(partyMention(`session:${SESSION}`, "Planner")).toBe(`[@Planner](${AGENT})`);
  expect(partyMention("role:reviewer", " reviewer ")).toBe("[@reviewer](dispatch://role/reviewer)");
  // A party with no name that fits on one line is mentioned by its address.
  expect(partyMention(`session:${SESSION}`, "two\nlines")).toBe(`[@session:${SESSION}](${AGENT})`);
});

// A worded link reads as a party's current name only where its words start with the mention
// sign and its address names a party, a hand-written one as much as a picked one; every other
// worded link keeps its author's words (R8).
test("a mention is a party's link whose words start with the mention sign", () => {
  const route = (address: string) => readTextReferences(address).references[0]?.route;
  expect(isPartyMention("@Bob", route(AGENT))).toBe(true);
  expect(isPartyMention("@reviewer", route("dispatch://role/reviewer"))).toBe(true);
  expect(isPartyMention("@alice", route("dispatch://person/alice@example.com"))).toBe(true);
  expect(isPartyMention("Bob", route(AGENT))).toBe(false);
  expect(isPartyMention(" @Bob", route(AGENT))).toBe(false);
  expect(isPartyMention("@the fix", route("dispatch://CORE-1"))).toBe(false);
  expect(isPartyMention("@Bob", undefined)).toBe(false);
});

// What a draft's pill reads a reference by: the link around it, as a reader of the stored text
// sees it - its words, or a picture, which is drawn as the picture and is no link.
test("a link is read around its address, a picture is told apart, and a reference on its own has none", () => {
  const linkAt = (body: string) => {
    const [found] = readTextReferences(body).references;
    if (found === undefined) throw new Error(`${body} holds no reference`);
    return linkOf(body, found);
  };
  expect(linkAt(`Ask [@Planner](${AGENT}) now.`)).toEqual({ picture: false, words: "@Planner" });
  expect(linkAt(`Ask [@Planner \\[2\\]](${AGENT}) now.`)).toEqual({
    picture: false,
    words: "@Planner [2]",
  });
  expect(linkAt(`Ask ${AGENT} now.`)).toBe(undefined);
  expect(linkAt("![shot.png](dispatch://CORE-1/artifact/shot-png@v1)")).toEqual({
    picture: true,
    words: "shot.png",
  });
});
