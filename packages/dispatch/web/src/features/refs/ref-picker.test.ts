import { expect, test } from "bun:test";

import {
  applyInsertion,
  groupRows,
  insertionFor,
  insertionText,
  kindOrder,
  matchesQuery,
  orderedRows,
  REF_PICKER_HEADINGS,
  type RefPickerRow,
  readTrigger,
} from "./ref-picker";

function row(kind: RefPickerRow["kind"], label: string, reference: string): RefPickerRow {
  return { detail: "", id: `${kind}-${label}`, kind, label, reference };
}

/** One set of rows, one of every kind the three doors rank differently. */
const rows: RefPickerRow[] = [
  row("issue", "Fix the thing", "dispatch://CORE-1"),
  row("document", "goals.md", "dispatch://CORE/artifact/goals-md"),
  row("comment", "a remark", "dispatch://CORE-1/comment/c1"),
  row("component", "The relay", "dispatch://CORE/component/relay"),
  row("agent", "Planner", "dispatch://agent/planner-session"),
  row("role", "reviewer", "dispatch://role/reviewer"),
  row("person", "alice@theorem.dev", "dispatch://person/alice@theorem.dev"),
];

test("the three doors rank one set of rows three ways, and each reaches every kind", () => {
  const kindsOf = (door: "all" | "parties" | "issues") =>
    groupRows(rows, door).map((group) => group.kind);

  expect(kindsOf("all")).toEqual([
    "document",
    "issue",
    "comment",
    "component",
    "agent",
    "role",
    "person",
  ]);
  expect(kindsOf("parties")).toEqual([
    "person",
    "agent",
    "role",
    "document",
    "issue",
    "comment",
    "component",
  ]);
  expect(kindsOf("issues")).toEqual([
    "issue",
    "document",
    "comment",
    "component",
    "agent",
    "role",
    "person",
  ]);
  // Every door reaches every kind: the three orders are the same set, and so is the kind list.
  for (const door of ["all", "parties", "issues"] as const) {
    expect([...kindOrder(door)].sort()).toEqual([...kindOrder("all")].sort());
    expect(orderedRows(groupRows(rows, door))).toHaveLength(rows.length);
  }
});

test("each group is headed by its kind's own name", () => {
  expect(groupRows(rows, "parties").map((group) => group.heading)).toEqual([
    "People",
    "Agents",
    "Roles",
    "Documents",
    "Issues",
    "Comments",
    "Components",
  ]);
  expect(REF_PICKER_HEADINGS["ask-part"]).toBe("Ask parts");
});

test("a trigger opens only at a word boundary", () => {
  expect(readTrigger("@")).toEqual({ chars: "@", door: "parties", query: "", start: 0 });
  expect(readTrigger("see @pl")).toEqual({ chars: "@", door: "parties", query: "pl", start: 4 });
  expect(readTrigger("(#CO")).toEqual({ chars: "#", door: "issues", query: "CO", start: 1 });
  expect(readTrigger("a line\n#")).toEqual({ chars: "#", door: "issues", query: "", start: 7 });
  expect(readTrigger("see [[go")).toEqual({ chars: "[[", door: "all", query: "go", start: 4 });

  // Mid-word: an email's `@`, an anchor's `#`, and a `[[` glued to a word open nothing.
  expect(readTrigger("alice@theorem.dev")).toBeUndefined();
  expect(readTrigger("issue#5")).toBeUndefined();
  expect(readTrigger("note[[")).toBeUndefined();
});

test("a space typed after a trigger closes it, which leaves a heading a heading", () => {
  expect(readTrigger("# ")).toBeUndefined();
  expect(readTrigger("# Heading")).toBeUndefined();
  expect(readTrigger("@planner now")).toBeUndefined();
  // The latest trigger still standing is the one that is open.
  expect(readTrigger("# Heading with @pl")).toEqual({
    chars: "@",
    door: "parties",
    query: "pl",
    start: 15,
  });
});

// A mention's words start with `@` right after a `[`, which is a word boundary: the picker must
// open neither over the mention it has just written nor where the caret moves into a written
// link's words or address, since a pick there would write a link inside a link.
test("no trigger opens over a written link, though a mention's words start with one", () => {
  const mention = "[@Planner](dispatch://agent/planner-session)";
  expect(readTrigger("[@reviewer](dispatch://role/reviewer)")).toBeUndefined();
  expect(readTrigger(`see ${mention} and`)).toBeUndefined();
  // The caret in the mention's words, at their end, and in its address.
  expect(readTrigger(mention, "[@Plan".length)).toBeUndefined();
  expect(readTrigger(mention, "[@Planner".length)).toBeUndefined();
  expect(readTrigger(mention, "[@Planner](dispatch://agent/plan".length)).toBeUndefined();
  // Words that hold an escaped bracket, as `partyLink` writes a title holding one.
  expect(readTrigger("[@A\\[b\\]](dispatch://agent/a)", "[@A\\[b".length)).toBeUndefined();
  // A `#` right after the `](` that opens an address is that address, not a door.
  expect(readTrigger("[the plan](#plan)", "[the plan](#pl".length)).toBeUndefined();
  // A `[` the reader typed is still a word boundary while no link closes after the caret, and a
  // link later in the text does not close the words the reader is typing now.
  expect(readTrigger("[@reviewer")).toEqual({
    chars: "@",
    door: "parties",
    query: "reviewer",
    start: 1,
  });
  expect(readTrigger(`ask @pl then ${mention}`, "ask @pl".length)).toEqual({
    chars: "@",
    door: "parties",
    query: "pl",
    start: 4,
  });
});

test("the browser-side filter matches a row's words, its detail and its address", () => {
  const agent = {
    ...row("agent", "Planner", "dispatch://agent/planner-session"),
    detail: "/w/one",
  };
  expect(matchesQuery(agent, "")).toBe(true);
  expect(matchesQuery(agent, "plan")).toBe(true);
  expect(matchesQuery(agent, "/w/")).toBe(true);
  expect(matchesQuery(agent, "planner-session")).toBe(true);
  expect(matchesQuery(agent, "reviewer")).toBe(false);
});

test("a party pick inserts its mention and every other pick the bare address", () => {
  // A mention's words are `@` and the name: a reader reads it as `@` and the party's current
  // name, so a session renamed since still reads as it is called now (R4).
  expect(insertionText(row("agent", "Planner", "dispatch://agent/planner-session"))).toBe(
    "[@Planner](dispatch://agent/planner-session)"
  );
  expect(insertionText(row("role", "reviewer", "dispatch://role/reviewer"))).toBe(
    "[@reviewer](dispatch://role/reviewer)"
  );
  expect(
    insertionText(row("person", "alice@theorem.dev", "dispatch://person/alice@theorem.dev"))
  ).toBe("[@alice@theorem.dev](dispatch://person/alice@theorem.dev)");
  // A name holding a bracket cannot break out of the link's words.
  expect(insertionText(row("agent", "Planner [2]", "dispatch://agent/two"))).toBe(
    "[@Planner \\[2\\]](dispatch://agent/two)"
  );

  expect(insertionText(row("issue", "Fix the thing", "dispatch://CORE-1"))).toBe(
    "dispatch://CORE-1"
  );
  expect(insertionText(row("component", "The relay", "dispatch://CORE/component/relay"))).toBe(
    "dispatch://CORE/component/relay"
  );
  expect(insertionText(row("document-block", "First words", "dispatch://CORE-1/spec#b-one"))).toBe(
    "dispatch://CORE-1/spec#b-one"
  );
});

test("an insertion replaces the trigger through the caret and keeps the text after it", () => {
  const text = "see #CO and the rest";
  const caret = "see #CO".length;
  const trigger = readTrigger(text.slice(0, caret));
  if (trigger === undefined) throw new Error("the trigger reads nothing");
  const insertion = insertionFor(row("issue", "Fix", "dispatch://CORE-1"), trigger, caret);

  expect(insertion).toEqual({
    end: 7,
    start: 4,
    text: "dispatch://CORE-1",
    trigger: "#",
  });
  expect(applyInsertion(text, insertion)).toEqual({
    caret: "see dispatch://CORE-1".length,
    text: "see dispatch://CORE-1 and the rest",
  });
});

// A host that writes marks rather than Markdown takes the mention apart (the document body's
// `DocumentRefPicker`): the words it shows and the address it names, which is the same mention
// `text` writes as Markdown.
test("a party's insertion carries its words and its address, and no other pick does", () => {
  const text = "ask @pl";
  const trigger = readTrigger(text);
  if (trigger === undefined) throw new Error("the trigger reads nothing");

  const party = insertionFor(
    row("agent", "Planner", "dispatch://agent/planner-session"),
    trigger,
    text.length
  );
  expect(party).toEqual({
    end: 7,
    link: { href: "dispatch://agent/planner-session", words: "@Planner" },
    start: 4,
    text: "[@Planner](dispatch://agent/planner-session)",
    trigger: "@",
  });

  const issue = insertionFor(row("issue", "Fix", "dispatch://CORE-1"), trigger, text.length);
  expect(issue.link).toBeUndefined();
});
