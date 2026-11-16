import { expect, test } from "bun:test";
import { referenceReading } from "@legion/contracts";

import {
  draftParties,
  inLinkWords,
  isPartyRoute,
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
// worded link keeps its author's words (R8). Which address names a party is this reader's own
// grammar (`isPartyRoute`), and the rest of the rule is `@legion/contracts`'s.
test("a mention is a party's link whose words start with the mention sign", () => {
  const reads = (words: string, address: string) => {
    const route = readTextReferences(address).references[0]?.route;
    return referenceReading({
      address,
      party: route !== undefined && isPartyRoute(route),
      words,
    });
  };
  expect(reads("@Bob", AGENT)).toBe("mention");
  expect(reads("@reviewer", "dispatch://role/reviewer")).toBe("mention");
  expect(reads("@alice", "dispatch://person/alice@example.com")).toBe("mention");
  expect(reads("Bob", AGENT)).toBe("words");
  expect(reads(" @Bob", AGENT)).toBe("words");
  expect(reads("@the fix", "dispatch://CORE-1")).toBe("words");
  expect(reads("@Bob", "dispatch://agent/")).toBe("words");
});

/** The link the first reference `body` holds stands in (`linkOf`). */
function linkAt(body: string) {
  const [found] = readTextReferences(body).references;
  if (found === undefined) throw new Error(`${body} holds no reference`);
  return linkOf(body, found);
}

/** What `partyMentionSpan` takes out of `body` for the session the tests name. */
function mentionTaken(body: string): string | undefined {
  const span = partyMentionSpan(body, `session:${SESSION}`);
  return span === undefined ? undefined : body.slice(span.start, span.end);
}

/** What a draft's pill reads the first reference of `body` as, as the chips ask it
 *  (`conversation/draft-pills.tsx`): the link around it, and the one rule over that link. */
function pillReads(body: string): string {
  const [found] = readTextReferences(body).references;
  if (found?.route === undefined) throw new Error(`${body} holds no reference`);
  const link = linkOf(body, found);
  return referenceReading({
    address: found.text,
    party: isPartyRoute(found.route),
    words: link?.words,
    wordsFormatting: link?.formatting,
  });
}

// What a draft's pill reads a reference by: the link around it, as a reader of the stored text
// sees it - its words, or a picture, which is drawn as the picture and is no link.
test("a link is read around its address, a picture is told apart, and a reference on its own has none", () => {
  expect(linkAt(`Ask [@Planner](${AGENT}) now.`)).toEqual({
    formatting: "plain",
    picture: false,
    words: "@Planner",
  });
  expect(linkAt(`Ask [@Planner \\[2\\]](${AGENT}) now.`)).toEqual({
    formatting: "plain",
    picture: false,
    words: "@Planner [2]",
  });
  expect(linkAt(`Ask ${AGENT} now.`)).toBe(undefined);
  expect(linkAt("![shot.png](dispatch://CORE-1/artifact/shot-png@v1)")).toEqual({
    formatting: "plain",
    picture: true,
    words: "shot.png",
  });
});

// A hand-written link is read as the renderer reads it, whole: its words may hold brackets that
// balance, a title may follow its address, and its address may stand in angle brackets.
test("a link whose words hold brackets that balance is read whole", () => {
  const body = `Ask [@Alice [x]](${AGENT}) now.`;
  expect(linkAt(body)?.words).toBe("@Alice [x]");
  expect(mentionTaken(body)).toBe(`[@Alice [x]](${AGENT})`);
});

test("a link with a title after its address is read to its closing parenthesis", () => {
  const body = `Ask [@Alice](${AGENT} "the planner") now.`;
  expect(linkAt(body)?.words).toBe("@Alice");
  expect(mentionTaken(body)).toBe(`[@Alice](${AGENT} "the planner")`);
});

test("a link whose address stands in angle brackets is read whole", () => {
  const body = `Ask [@Alice](<${AGENT}>) now.`;
  expect(linkAt(body)?.words).toBe("@Alice");
  expect(mentionTaken(body)).toBe(`[@Alice](<${AGENT}>)`);
});

// Markdown reads `\@` as `@`, so a link whose sign is written escaped is the mention the
// rendered record draws (`reference-naming.test.tsx`): the draft's reader takes its words
// through the one function that reads written words (`@legion/contracts`'s `readWrittenWords`).
test("a link whose words open with an escaped mention sign reads as the mention it is drawn as", () => {
  const body = `Ask [\\@Alice \\(lead\\)](${AGENT}) now.`;
  expect(linkAt(body)?.words).toBe("@Alice (lead)");
  expect(pillReads(body)).toBe("mention");
});

// The formatting rule every reader of a record applies (`@legion/contracts`'s
// `dispatch-mentions.ts`), read out of a draft's characters, in its three cases. First: code
// anywhere in a link's words keeps the words.
test("a draft's link with code anywhere in its words reads as its words", () => {
  expect(linkAt(`Ask [\`@Alice\`](${AGENT}) now.`)).toEqual({
    formatting: "code",
    picture: false,
    words: "@Alice",
  });
  expect(pillReads(`Ask [\`@Alice\`](${AGENT}) now.`)).toBe("words");
  expect(pillReads(`Ask [@Alice \`lead\`](${AGENT}) now.`)).toBe("words");
});

// Second: formatting over all of a link's words is the same record as that formatting around
// the link, so the link is still a mention, and its words are read without the runs that
// write the formatting - inside the brackets, around the link, or around the sentence.
test("a draft's link whose words are all under one formatting reads as a mention", () => {
  expect(linkAt(`Ask [**@Alice**](${AGENT}) now.`)).toEqual({
    formatting: "plain",
    picture: false,
    words: "@Alice",
  });
  expect(pillReads(`Ask [**@Alice**](${AGENT}) now.`)).toBe("mention");
  expect(pillReads(`Ask *[@Alice](${AGENT})* now.`)).toBe("mention");
  expect(pillReads(`~~Ask [@Alice](${AGENT}) now.~~`)).toBe("mention");
});

// Third: formatting over some of a link's words makes them the author's words.
test("a draft's link whose words are formatted in part reads as its words", () => {
  expect(linkAt(`Ask [@Alice **lead**](${AGENT}) now.`)).toEqual({
    formatting: "mixed",
    picture: false,
    words: "@Alice lead",
  });
  expect(pillReads(`Ask [@Alice **lead**](${AGENT}) now.`)).toBe("words");
  expect(pillReads(`Ask [*@Alice* lead](${AGENT}) now.`)).toBe("words");
  expect(pillReads(`Ask [@Alice ~~lead~~](${AGENT}) now.`)).toBe("words");
});

// The renderer links no reference a link's words hold (`RefLink.tsx`'s `linkPassReads`), while a
// link's address is the reference the link is.
test("a reference in a link's words is told from the link's address", () => {
  const inWords = (body: string) =>
    readTextReferences(body).references.map((found) => [found.text, inLinkWords(body, found)]);
  expect(inWords("[see CORE-1 here](https://x)")).toEqual([["CORE-1", true]]);
  expect(inWords("[CORE-1](dispatch://CORE-2)")).toEqual([
    ["CORE-1", true],
    ["dispatch://CORE-2", false],
  ]);
  expect(inWords("CORE-1 and [the fix](dispatch://CORE-2)")).toEqual([
    ["CORE-1", false],
    ["dispatch://CORE-2", false],
  ]);
  // A bracket a code span holds closes no link's words, as CommonMark reads a code span first.
  expect(inWords("[CORE-1 `]` too](dispatch://CORE-2)")).toEqual([
    ["CORE-1", true],
    ["dispatch://CORE-2", false],
  ]);
});
