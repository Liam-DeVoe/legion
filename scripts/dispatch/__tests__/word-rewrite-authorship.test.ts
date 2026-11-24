import { describe, expect, test } from "bun:test";
import { Authorship, titleAuthor, versionClass } from "../word-rewrite/authorship.ts";
import { type Actor, Event, Text, type Version } from "../word-rewrite/dispatch-api.ts";
import { fixture } from "./word-rewrite-fake.ts";

const session: Actor = { kind: "session", id: "s1" };
const person: Actor = { kind: "user", id: "alice@example.com" };
const act: Actor = {
  kind: "user",
  id: "alice@example.com",
  act: { session: "s1", token_name: "seed", scopes: ["person"] },
};

/** A document's versions, each its authors and its markdown, served as `versions/<n>` serves
 *  them: a past version lists typed blocks alone, so these plain paragraphs list none. */
function history(versions: readonly { authors: Actor[] | null; markdown: string }[]) {
  const listed: Version[] = versions.map((version, index) => ({
    number: index + 1,
    authors: version.authors,
  }));
  const reads = {
    read: [] as number[],
    version: async (_id: string, number: number) => {
      reads.read.push(number);
      const version = versions[number - 1];
      if (version === undefined) throw new Error(`no version ${number}`);
      return { markdown: version.markdown, number, authors: version.authors, blocks: [] };
    },
  };
  return { listed, reads };
}

function block(text: string, sentence = text) {
  return { id: "p1", text, sentence };
}

/** Who wrote a block does not hang on what it says, so these texts need no occurrence. */
const MADE = "The session wrote that the issue waits for the release.";
const OTHER = "Another paragraph.";

describe("who can have written a block", () => {
  test("a block a person made: version 1 by kind user", async () => {
    const { listed, reads } = history([{ authors: [person], markdown: `${MADE}\n` }]);
    expect(await new Authorship("d", listed, reads).authorOf(block(MADE))).toBe("person");
  });

  test("a block a session made and a person changed later", async () => {
    const changed = "The session wrote that the issue waits for the release, and a person agreed.";
    const { listed, reads } = history([
      { authors: [session], markdown: `${MADE}\n\n${OTHER}\n` },
      { authors: [person], markdown: `${changed}\n\n${OTHER}\n` },
    ]);
    expect(await new Authorship("d", listed, reads).authorOf(block(changed))).toBe("person");
  });

  test("a block whose only person-authored version changed another block is the tool's", async () => {
    const { listed, reads } = history([
      { authors: [session], markdown: `${MADE}\n\n${OTHER}\n` },
      { authors: [person], markdown: `${MADE}\n\nA person rewrote this one.\n` },
    ]);
    expect(await new Authorship("d", listed, reads).authorOf(block(MADE))).toBe("session");
  });

  test("an act write: an agent through a person's token is recorded as the person", async () => {
    const { listed, reads } = history([
      { authors: [session], markdown: `${OTHER}\n` },
      { authors: [act], markdown: `${OTHER}\n\n${MADE}\n` },
    ]);
    expect(versionClass([act])).toBe("person");
    expect(await new Authorship("d", listed, reads).authorOf(block(MADE))).toBe("person");
  });

  test("a version with authors null", async () => {
    const { listed, reads } = history([{ authors: null, markdown: `${MADE}\n` }]);
    expect(await new Authorship("d", listed, reads).authorOf(block(MADE))).toBe("unknown");
  });

  test("a version that mixes a person and a session", async () => {
    const { listed, reads } = history([{ authors: [person, session], markdown: `${MADE}\n` }]);
    expect(versionClass([person, session])).toBe("unknown");
    expect(await new Authorship("d", listed, reads).authorOf(block(MADE))).toBe("unknown");
  });

  test("a system author, and a version Dispatch did not list, read as unknown", () => {
    expect(versionClass([{ kind: "system", id: "document-settlement" }])).toBe("unknown");
    expect(versionClass([])).toBe("unknown");
  });

  test("a document whose versions are all a session's reads no version", async () => {
    const { listed, reads } = history([
      { authors: [session], markdown: `${MADE}\n` },
      { authors: [session], markdown: `${MADE}\n\n${OTHER}\n` },
    ]);
    const authorship = new Authorship("d", listed, reads);
    expect(authorship.suspect).toBe(false);
    expect(await authorship.authorOf(block(MADE))).toBe("session");
    expect(reads.read).toEqual([]);
  });

  test("a typed block whose token a system version moved, from the captured versions", async () => {
    // version-blocks-2.json is the system's settlement, which wrote the ask block's state.
    const text = Text.parse(fixture("text-blocks.json"));
    const ask = text.blocks?.find((candidate) => candidate.type === "ask");
    if (ask === undefined) throw new Error("text-blocks.json holds no ask block");
    const markdown = Buffer.from(text.markdown).subarray(ask.from, ask.to).toString("utf8");
    const versions: Version[] = [1, 2, 3].map((number) => {
      const read = fixture(`version-blocks-${number}.json`) as { authors: Actor[] | null };
      return { number, authors: read.authors };
    });
    const reads = {
      version: async (_id: string, number: number) => fixture(`version-blocks-${number}.json`),
    };
    const author = await new Authorship("d", versions, reads).authorOf({
      id: ask.id,
      text: markdown,
      sentence: markdown,
    });
    expect(author).toBe("unknown");
  });
});

describe("who can have written a title", () => {
  test("the creator, when no event set the title since: issue-events.json", () => {
    const events = Event.array().parse(fixture("issue-events.json"));
    expect(titleAuthor(events, null)).toBe("session");
  });

  test("the actor of the last event that set the title", () => {
    const events = Event.array().parse(fixture("issue-events.json"));
    const retitled = {
      ...events[0],
      id: 9_999,
      seq: 99,
      type: "issue.updated",
      actor: person,
      payload: { title: "A person's title", changed: ["title"] },
    };
    expect(titleAuthor([...events, Event.parse(retitled)], session)).toBe("person");
  });

  test("no event and no creator is unknown", () => {
    expect(titleAuthor([], null)).toBe("unknown");
  });
});
