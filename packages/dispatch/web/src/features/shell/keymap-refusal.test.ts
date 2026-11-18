import { expect, test } from "bun:test";

import { catalogueEntry, NO_PERSON_KEYS } from "./keymap-catalogue";
import { refusalOf } from "./keymap-refusal";

test("e for Snooze is refused, naming Dismiss, the action of the same page that has it", () => {
  const verdict = refusalOf("inbox.snooze", ["e"], NO_PERSON_KEYS);
  expect(verdict).toMatchObject({ kind: "refused", other: "inbox.dismiss" });
  expect(verdict.kind === "refused" && verdict.message).toContain("Dismiss, or Move to Inbox");
});

test("e for Snooze is taken once the person gave Dismiss another key", () => {
  expect(refusalOf("inbox.snooze", ["e"], { "inbox.dismiss": ["q"] })).toEqual({
    kind: "accepted",
    notices: [],
  });
});

test("g is refused, naming an action whose first press is g", () => {
  const verdict = refusalOf("inbox.snooze", ["g"], NO_PERSON_KEYS);
  expect(verdict.kind).toBe("refused");
  if (verdict.kind !== "refused" || verdict.other === undefined) throw new Error("no action named");
  const other = catalogueEntry(verdict.other);
  expect(other.keys.some((key) => key.startsWith("g "))).toBe(true);
  expect(verdict.message).toContain(other.label);
});

test("a key whose first press is another action's key is refused too", () => {
  // `l` is the Inbox's `Move the focus into the reading pane`, so `l x` could never be pressed.
  const verdict = refusalOf("inbox.snooze", ["l x"], NO_PERSON_KEYS);
  expect(verdict).toMatchObject({ kind: "refused", other: "inbox.read-pane" });
});

test("1 is refused: the digits keep their own actions", () => {
  expect(refusalOf("inbox.snooze", ["1"], NO_PERSON_KEYS).kind).toBe("refused");
  for (const key of ["Space", "Shift+Space", "Enter", "Escape", "Tab", "ArrowDown"]) {
    expect(refusalOf("inbox.snooze", [key], NO_PERSON_KEYS).kind, key).toBe("refused");
  }
  // The same keys with Alt are free: the document's Alt+Enter is one.
  expect(refusalOf("inbox.snooze", ["Alt+Enter"], NO_PERSON_KEYS).kind).toBe("accepted");
});

test("a key the browser keeps for itself is refused", () => {
  for (const key of ["$mod+w", "Control+t", "Meta+n", "$mod+Tab"]) {
    expect(refusalOf("global.create", [key], NO_PERSON_KEYS).kind, key).toBe("refused");
  }
});

test("an action whose keys all keep their own action takes no key", () => {
  expect(refusalOf("inbox.option", ["q"], NO_PERSON_KEYS).kind).toBe("refused");
  expect(refusalOf("inbox.option", [], NO_PERSON_KEYS).kind).toBe("accepted");
});

test("c is taken on the Inbox with the notice that Create issue does not work there", () => {
  expect(refusalOf("inbox.snooze", ["c"], NO_PERSON_KEYS)).toEqual({
    kind: "accepted",
    notices: ["Create issue does not work on the Inbox."],
  });
  // And the other way round: a global action taking a page's key.
  expect(refusalOf("global.create", ["e"], NO_PERSON_KEYS)).toMatchObject({
    kind: "accepted",
    notices: expect.arrayContaining(["Create issue does not work on the Inbox."]),
  });
});

test("a key of the issue page's document is refused on the issue page", () => {
  expect(refusalOf("issue.labels", ["Alt+Enter"], NO_PERSON_KEYS)).toMatchObject({
    kind: "refused",
    other: "document.open-link",
  });
  expect(refusalOf("document.open-link", ["l"], NO_PERSON_KEYS)).toMatchObject({
    kind: "refused",
    other: "issue.labels",
  });
});

test("the List's and the Board's own o are no duplicate, but both meet the project page's keys", () => {
  expect(refusalOf("project.open", ["o"], NO_PERSON_KEYS)).toEqual({
    kind: "accepted",
    notices: [],
  });
  expect(refusalOf("board.open", ["o"], NO_PERSON_KEYS)).toEqual({
    kind: "accepted",
    notices: [],
  });
  // The Board's `p` is free on the List, whose tab never mounts beside the Board.
  expect(refusalOf("project.open", ["p"], NO_PERSON_KEYS).kind).toBe("accepted");
  // The project page's own `v` is drawn above both.
  expect(refusalOf("board.open", ["v"], NO_PERSON_KEYS)).toMatchObject({
    kind: "refused",
    other: "project.toggle-view",
  });
  expect(refusalOf("project.open", ["v"], NO_PERSON_KEYS)).toMatchObject({
    kind: "refused",
    other: "project.toggle-view",
  });
});

test("a key listed twice, or a key that is the first press of another key of the action, is refused", () => {
  expect(refusalOf("inbox.snooze", ["q", "q"], NO_PERSON_KEYS).kind).toBe("refused");
  expect(refusalOf("inbox.snooze", ["q", "q w"], NO_PERSON_KEYS).kind).toBe("refused");
  expect(refusalOf("inbox.snooze", ["q w", "q"], NO_PERSON_KEYS).kind).toBe("refused");
  expect(refusalOf("inbox.snooze", ["Shift++"], NO_PERSON_KEYS).kind).toBe("refused");
  // Two chords that share a first press are no prefix of each other, as `g i` and `g a` are not.
  expect(refusalOf("inbox.snooze", ["q w", "q e"], NO_PERSON_KEYS).kind).toBe("accepted");
});
