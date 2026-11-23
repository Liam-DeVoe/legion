import { expect, test } from "bun:test";

import type { GoingPlanAnswer, GoingPlanRow, GoingUndoLine } from "../../api/types";
import { clockWords } from "../search/blocks/block-cells";
import type { PlanDraft } from "./going-store";
import {
  cardTitle,
  changeLabel,
  fieldWords,
  keptValue,
  labelMarks,
  planTitle,
  removalWords,
  reviewWords,
  rowChanges,
  rowKind,
  rowLabel,
  rowOutcome,
  rowRemoves,
  specSentences,
  undoneChangeLabel,
  undoneTitle,
  undoWords,
  valueWords,
  withChangesUnticked,
} from "./plan-rows";

// What a plan's rows say to the person (design v3, state 6, rule 6): the card's title made from
// its rows, each field in the issue page's words, each value as the page shows it and never as
// JSON, each reason Auto-review leaves a row to the person in words, and the rows that take
// something out.

function row(number: number, extra: Partial<GoingPlanRow>): GoingPlanRow {
  return {
    arguments: {},
    changed: false,
    claim: null,
    class: "field",
    fields: [],
    issue: "",
    item: "",
    needs: [],
    now: null,
    result: null,
    row: number,
    state: "waiting",
    tool: "dispatch_issue_update",
    waits: [],
    ...extra,
  };
}

function update(number: number, key: string, args: Record<string, unknown>): GoingPlanRow {
  return row(number, {
    arguments: { issue: key, ...args },
    fields: Object.keys(args),
    issue: key,
    item: key,
  });
}

function newIssue(number: number, title: string): GoingPlanRow {
  return row(number, {
    arguments: { project: "DEMO", title },
    class: "create_plain",
    tool: "dispatch_issue",
  });
}

function message(number: number): GoingPlanRow {
  return row(number, {
    arguments: { body: "Please look.", issue: "DEMO-15" },
    class: "message",
    issue: "DEMO-15",
    item: "DEMO-15",
    tool: "dispatch_message",
  });
}

const plan: GoingPlanAnswer = {
  mode: "ask",
  plan: 1,
  rows: [],
  run: "run-1",
  state: "waiting",
  summary: "",
  version: "v1",
};

test("the card's title is a sentence made from its rows", () => {
  expect(planTitle([update(0, "DEMO-10", { priority: 2 })])).toBe("Change DEMO-10");
  // Two changes of one item still name it.
  expect(
    planTitle([update(0, "DEMO-10", { priority: 2 }), update(1, "DEMO-10", { title: "Dome" })])
  ).toBe("Change DEMO-10");
  expect(
    planTitle([
      newIssue(0, "North half"),
      newIssue(1, "South half"),
      update(2, "DEMO-3", { parent: "$0" }),
      update(3, "DEMO-4", { parent: "$0" }),
      update(4, "DEMO-5", { parent: "$1" }),
    ])
  ).toBe("File 2 issues and move 3");
  expect(planTitle([message(0)])).toBe("Send 1 message");
  expect(planTitle([update(0, "DEMO-10", { status: "done" })])).toBe("Close DEMO-10");
  expect(planTitle([update(0, "DEMO-10", { priority: 1 }), message(1), message(2)])).toBe(
    "Change DEMO-10 and send 2 messages"
  );
  expect(
    planTitle([
      message(0),
      row(1, { arguments: { ask: "ask-1" }, class: "ask_answer", tool: "dispatch_resolve_ask" }),
    ])
  ).toBe("Send 1 message and make 1 other change");
});

test("each field is named as the issue page names it", () => {
  expect(fieldWords("priority")).toBe("Priority");
  expect(fieldWords("labels")).toBe("Labels");
  expect(fieldWords("parent")).toBe("Parent");
  expect(fieldWords("blocked_by")).toBe("Blocked by");
  expect(fieldWords("route")).toBe("Messages go to");
  expect(fieldWords("repo")).toBe("Repository");
  expect(fieldWords("claim")).toBe("Claimed by");
  expect(fieldWords("rank")).toBe("Place on the board");
});

test("each value shows as the issue page shows it, and none as its JSON", () => {
  expect(valueWords("priority", null)).toBe("No priority");
  expect(valueWords("priority", 2)).toBe("P2");
  expect(valueWords("labels", ["board", "accessibility"])).toBe("board, accessibility");
  expect(valueWords("labels", [])).toBe("None");
  expect(valueWords("status", "in_progress")).toBe("In progress");
  expect(valueWords("assignee", null)).toBe("None");
  expect(valueWords("assignee", "alice@example.com")).toBe("alice@example.com");
  expect(valueWords("blocked_by", ["DEMO-2", "$0"], new Map([[0, "North half"]]))).toBe(
    "DEMO-2, North half (new)"
  );
  // A text that reads as "$N" names no new issue where the field takes no issue.
  expect(valueWords("title", "$0")).toBe("$0");
  expect(valueWords("rank", { after: "DEMO-2", before: "DEMO-5", rank: "a0" })).toBe(
    "after DEMO-2, before DEMO-5"
  );
  expect(valueWords("advances", { ids: ["p-1", "p-2"], revision: 3 })).toBe("2 priorities");
  expect(valueWords("components", { ids: [], mode: "inherit", reason: null })).toBe(
    "Inherited from the issue above"
  );
  expect(valueWords("external_links", [{ kind: "pr", url: "https://example.com/pr/1" }])).toBe(
    "https://example.com/pr/1"
  );
  expect(valueWords("claim", null)).toBe("Nobody");
  expect(valueWords("claim", { id: "alice", kind: "user" })).toBe("alice");
  expect(valueWords("options", [{ label: "Ship Friday" }, { label: "Hold" }])).toBe(
    "Ship Friday, Hold"
  );
  expect(valueWords("multiple", true)).toBe("Yes");
  expect(valueWords("urgency", "high")).toBe("High");
  for (const value of [{ ids: [], revision: 1 }, ["a"], { unknown: 1 }, 7, false]) {
    expect(valueWords("anything", value)).not.toMatch(/[{}[\]"]/u);
  }
});

test("a change of labels keeps, adds and takes out labels, whatever their case", () => {
  expect(labelMarks(["board", "A11y"], ["Board", "accessibility"])).toEqual([
    { label: "Board", mark: "kept" },
    { label: "accessibility", mark: "added" },
    { label: "A11y", mark: "removed" },
  ]);
});

test("a row's fields hold the value now and the new value; a claim's new holder is the person or nobody", () => {
  const changes = rowChanges(
    row(0, {
      arguments: { issue: "DEMO-10", labels: ["board", "accessibility"], priority: 2 },
      fields: ["labels", "priority"],
      item: "DEMO-10",
      now: { labels: ["board"], priority: null },
    }),
    { priority: 1 },
    new Map()
  );
  expect(changes).toEqual([
    {
      edit: "labels",
      edited: false,
      field: "labels",
      namesNewIssue: false,
      next: ["board", "accessibility"],
      now: ["board"],
    },
    { edit: "priority", edited: true, field: "priority", namesNewIssue: false, next: 1, now: null },
  ]);
  const claim = (release: boolean) =>
    rowChanges(
      row(0, {
        arguments: release ? { issue: "DEMO-10", release } : { issue: "DEMO-10" },
        class: "claim",
        fields: ["claim"],
        now: release ? { id: "alice", kind: "user" } : null,
        tool: "dispatch_claim",
      }),
      undefined,
      new Map()
    ).map((change) => [
      valueWords(change.field, change.now),
      valueWords(change.field, change.next),
    ]);
  expect(claim(false)).toEqual([["Nobody", "You"]]);
  expect(claim(true)).toEqual([["alice", "Nobody"]]);
  // A field another tool's read names but its arguments give no value shows no line.
  expect(
    rowChanges(
      row(0, {
        arguments: { body: "Hi", issue: "DEMO-1" },
        fields: ["comment"],
        tool: "dispatch_comment",
      }),
      undefined,
      new Map()
    )
  ).toEqual([]);
});

test("a row that closes, deletes or removes something is told apart from one that adds", () => {
  const removes = (planned: GoingPlanRow) =>
    rowRemoves(planned, rowChanges(planned, undefined, new Map()));
  const labels = (now: string[], next: string[]) =>
    row(0, {
      arguments: { issue: "DEMO-1", labels: next },
      fields: ["labels"],
      item: "DEMO-1",
      now: { labels: now },
    });
  expect(removes(labels(["board", "a11y"], ["board"]))).toBe(true);
  expect(removes(labels(["board"], ["board", "a11y"]))).toBe(false);
  expect(removes(update(0, "DEMO-1", { status: "done" }))).toBe(true);
  expect(removes(update(0, "DEMO-1", { status: "todo" }))).toBe(false);
  expect(
    removes(
      row(0, {
        arguments: { issue: "DEMO-1", parent: "" },
        fields: ["parent"],
        now: { parent: "DEMO-9" },
      })
    )
  ).toBe(true);
  expect(
    removes(row(0, { arguments: { issue: "DEMO-1", release: true }, tool: "dispatch_claim" }))
  ).toBe(true);
  expect(removes(row(0, { arguments: { issue: "DEMO-1" }, tool: "dispatch_claim" }))).toBe(false);
  expect(
    removes(row(0, { arguments: { priority: "p-1" }, tool: "dispatch_delete_priority" }))
  ).toBe(true);
  expect(removes(newIssue(0, "North half"))).toBe(false);
});

// A preview's row that added a label replaced the issue's labels, and its red mark said "Removes"
// and no more. A row that adds labels (labels_add) shows the labels now with those it adds, the
// added one marked, nothing taken out; a row that takes values out names each of them.
test("a row that adds labels keeps the issue's others and shows them, and nothing as removed", () => {
  const adds = (now: string[], added: string[]) =>
    row(0, {
      arguments: { issue: "DEMO-10", labels_add: added, priority: 2 },
      fields: ["labels", "priority"],
      item: "DEMO-10",
      now: { labels: now, priority: null },
    });
  const [labels] = rowChanges(adds(["board"], ["accessibility"]), undefined, new Map());
  expect(labels).toEqual({
    edit: "labels",
    edited: false,
    field: "labels",
    namesNewIssue: false,
    next: ["board", "accessibility"],
    now: ["board"],
  });
  expect(valueWords("labels", labels?.now)).toBe("board");
  expect(valueWords("labels", labels?.next)).toBe("board, accessibility");
  expect(labelMarks(labels?.now, labels?.next)).toEqual([
    { label: "board", mark: "kept" },
    { label: "accessibility", mark: "added" },
  ]);
  const planned = adds(["board", "API"], [" api ", "ui"]);
  const changes = rowChanges(planned, undefined, new Map());
  // A label the issue holds in another case is one it holds, and is not added twice.
  expect(changes[0]?.next).toEqual(["board", "API", "ui"]);
  expect(rowRemoves(planned, changes)).toBe(false);
  // The person's edit of the labels is the whole set they chose, which may take one out.
  const edited = rowChanges(planned, { labels: ["board", "ui"] }, new Map());
  expect(edited[0]).toMatchObject({ edited: true, next: ["board", "ui"] });
  expect(removalWords(planned, edited)).toBe("Removes API");
});

test("a row that takes values out names each of them, and a close says it closes", () => {
  const words = (planned: GoingPlanRow) =>
    removalWords(planned, rowChanges(planned, undefined, new Map()));
  const labels = (now: string[], next: string[], extra: Record<string, unknown> = {}) =>
    row(0, {
      arguments: { issue: "DEMO-1", labels: next, ...extra },
      fields: ["labels", ...Object.keys(extra)],
      item: "DEMO-1",
      now: { labels: now },
    });
  expect(words(labels(["board", "a11y"], ["accessibility"]))).toBe("Removes board and a11y");
  expect(words(labels(["board"], ["accessibility"]))).toBe("Removes board");
  expect(words(labels(["board", "api", "ui"], []))).toBe("Removes board, api and ui");
  expect(words(update(0, "DEMO-1", { status: "done" }))).toBe("Closes");
  expect(words(labels(["board"], [], { status: "done" }))).toBe("Closes, and removes board");
  expect(
    words(
      row(0, {
        arguments: { blocked_by: ["DEMO-4"], issue: "DEMO-1" },
        fields: ["blocked_by"],
        now: { blocked_by: ["DEMO-3", "DEMO-4"] },
      })
    )
  ).toBe("Removes DEMO-3");
  expect(
    words(
      row(0, {
        arguments: { issue: "DEMO-1", parent: "" },
        fields: ["parent"],
        now: { parent: "DEMO-9" },
      })
    )
  ).toBe("Removes DEMO-9");
  expect(words(row(0, { arguments: { priority: "p-1" }, tool: "dispatch_delete_priority" }))).toBe(
    "Removes"
  );
});

test("Auto-review says why each row waits, in words, and that a row it let through ran", () => {
  const auto = { ...plan, mode: "auto_review" as const };
  const reasons: Record<string, string> = {
    ask_answer: "Waits for you: it answers a question in your name",
    broadcast: "Waits for you: it sends a message to several agent sessions",
    claimed_by_session: "Waits for you: an agent session holds this issue's claim",
    decision: "Waits for you: it keeps a decision in your name",
    delivers_to_session: "Waits for you: Dispatch hands this change to an agent session at once",
    message: "Waits for you: it sends a message",
    result: "Waits for you: it records a check result in your name",
    review: "Waits for you: it gives a review in your name",
    setting: "Waits for you: it changes a setting of Dispatch",
  };
  for (const [reason, words] of Object.entries(reasons)) {
    expect(reviewWords(auto, row(0, { waits: [reason] }))).toBe(words);
  }
  expect(reviewWords(auto, row(0, { waits: ["claimed_by_session", "delivers_to_session"] }))).toBe(
    "Waits for you: an agent session holds this issue's claim; Dispatch hands this change to an agent session at once"
  );
  expect(reviewWords(auto, row(0, { class: "text" }))).toBe(
    "Waits for you: it changes text, which Auto-review never runs"
  );
  expect(reviewWords(auto, row(0, { class: "field" }))).toBe(
    "Waits for you: your words did not ask for this change"
  );
  // A reason this build has no words for shows none of its token.
  expect(reviewWords(auto, row(0, { waits: ["new_reason"] }))).toBe(
    "Waits for you: your words did not ask for this change"
  );
  expect(reviewWords(auto, row(0, { state: "done" }))).toBe("Ran · passed review");
  // In Ask every row waits, and no reason shows.
  expect(reviewWords(plan, row(0, { waits: ["claimed_by_session"] }))).toBeUndefined();
  expect(reviewWords({ ...auto, state: "done" }, row(0, { state: "done" }))).toBeUndefined();
});

test("a change is named as its step is, as an order, a new issue by its title, a document by its name", () => {
  expect(changeLabel("dispatch_issue_update", { issue: "DEMO-3" })).toBe("Update DEMO-3");
  expect(changeLabel("dispatch_claim", { issue: "DEMO-3" })).toBe("Change the claim on DEMO-3");
  expect(changeLabel("dispatch_message", { body: "Hi", issue: "DEMO-1" })).toBe(
    "Send a message to DEMO-1"
  );
  expect(changeLabel("dispatch_issue", { project: "DEMO", title: "North half" })).toBe(
    "File North half"
  );
  expect(changeLabel("dispatch_issue_update", { issue: "$0" }, new Map([[0, "North half"]]))).toBe(
    "Update North half (new)"
  );
  expect(changeLabel("dispatch_no_such_tool", {})).toBe("Make a change");
  expect(
    rowLabel(
      row(0, {
        arguments: { artifact: "spec", issue: "DEMO-1", ops: [], summary: "" },
        tool: "dispatch_doc_edit",
      })
    )
  ).toBe("Edit the text of spec");
  expect(undoneChangeLabel("dispatch_issue", "DEMO-9")).toBe("File DEMO-9");
  expect(undoneChangeLabel("dispatch_message", "DEMO-1")).toBe("Send a message to DEMO-1");
});

test("each kind of row is told apart", () => {
  expect(rowKind(newIssue(0, "North half"))).toBe("new_issue");
  expect(rowKind(update(0, "DEMO-1", { priority: 1 }))).toBe("change");
  expect(rowKind(message(0))).toBe("message");
  expect(rowKind(row(0, { arguments: { delivery: "aside" }, tool: "dispatch_broadcast" }))).toBe(
    "message"
  );
  expect(
    rowKind(row(0, { arguments: { quote: "a", replace_with: "b" }, tool: "dispatch_suggest" }))
  ).toBe("document");
  expect(rowKind(row(0, { tool: "dispatch_claim" }))).toBe("claim");
  expect(rowKind(row(0, { tool: "dispatch_resolve_ask" }))).toBe("other");
});

test("a new issue's spec shows as its first two or three sentences, in plain words", () => {
  expect(
    specSentences(
      "# The dome\n\nThe dome splits in **two**. Each half turns alone.\n\n- The motor stays.\n- Nothing else moves."
    )
  ).toBe("The dome splits in two. Each half turns alone. The motor stays.");
  const long = `${"A".repeat(200)}. ${"B".repeat(150)}. Short.`;
  expect(specSentences(long)).toBe(`${"A".repeat(200)}.`);
});

test("a row whose item changed after the plan showed is unticked once for each change, with what needs it", () => {
  const changed = (title: string): GoingPlanAnswer => ({
    ...plan,
    rows: [
      row(0, {
        arguments: { issue: "DEMO-1", title: "Dome" },
        changed: true,
        fields: ["title"],
        now: { title },
      }),
      row(1, { arguments: { issue: "DEMO-2", status: "todo" }, needs: [0] }),
    ],
  });
  const empty: PlanDraft = { edits: new Map(), plan: 1, run: "run-1", unticked: new Set() };
  const once = withChangesUnticked(empty, changed("Bob's dome"));
  expect([...once.unticked]).toEqual([0, 1]);
  // The person ticks it again, and the same change does not untick it twice.
  const ticked = { ...once, unticked: new Set<number>() };
  expect(withChangesUnticked(ticked, changed("Bob's dome"))).toBe(ticked);
  // Another change of the item unticks it again.
  expect([...withChangesUnticked(ticked, changed("Carol's dome")).unticked]).toEqual([0, 1]);
});

// After approval (design v2, state 7): the card's title follows its rows, each row says how it
// stands in words, and Undo's answer is said per change, a kept value named as data.
function undoLine(extra: Partial<GoingUndoLine>): GoingUndoLine {
  return {
    fields: [],
    item: "DEMO-1",
    outcome: { code: "", message: "written back", ok: true, status: 200 },
    plan: 1,
    row: 0,
    tool: "dispatch_issue_update",
    undo: "write_back",
    ...extra,
  };
}

/** A plan in `state` whose rows, one per issue, stand in `states`. */
function sentPlan(
  state: GoingPlanAnswer["state"],
  states: readonly GoingPlanRow["state"][]
): GoingPlanAnswer {
  return {
    ...plan,
    rows: states.map((stands, number) => ({
      ...update(number, `DEMO-${number}`, { priority: 1 }),
      state: stands,
    })),
    state,
  };
}

test("the card's title says Applying 1 of 3 while its rows run, then Applied with the time it ended", () => {
  expect(
    cardTitle(sentPlan("running", ["running", "approved", "approved"]), undefined, undefined)
  ).toBe("Applying 1 of 3…");
  expect(
    cardTitle(
      sentPlan("running", ["done", "running", "approved", "unticked"]),
      undefined,
      undefined
    )
  ).toBe("Applying 2 of 3…");
  const at = Date.parse("2026-11-22T16:42:00Z");
  expect(cardTitle(sentPlan("done", ["done", "done", "done"]), undefined, at)).toBe(
    `Applied 3 changes · ${clockWords(at)}`
  );
  expect(cardTitle(sentPlan("done", ["done", "failed", "skipped"]), undefined, undefined)).toBe(
    "Applied 1 of 3 changes"
  );
  expect(cardTitle(sentPlan("done", ["unticked"]), undefined, undefined)).toBe("Nothing applied");
  expect(cardTitle(sentPlan("rejected", ["waiting"]), undefined, undefined)).toBe(
    "Discarded · nothing ran"
  );
  // While it waits, the title is the sentence its rows make.
  expect(cardTitle(sentPlan("waiting", ["waiting"]), undefined, undefined)).toBe("Change DEMO-0");
});

test("after Undo the title counts the changes reverted, never a message", () => {
  const lines = [
    undoLine({ row: 0 }),
    undoLine({ row: 1 }),
    undoLine({ row: 2, tool: "dispatch_message", undo: "not_undone" }),
  ];
  expect(undoneTitle(lines)).toBe("Undone · 2 changes reverted");
  expect(cardTitle({ ...plan, state: "done" }, lines, Date.now())).toBe(
    "Undone · 2 changes reverted"
  );
  expect(
    undoneTitle([
      undoLine({ row: 0 }),
      undoLine({
        outcome: { code: "GOING_UNDO_CHANGED", message: "kept", ok: false, status: 409 },
      }),
    ])
  ).toBe("Undone · 1 of 2 changes reverted");
  expect(undoneTitle([])).toBe("Undone · nothing to revert");
});

test("a row that no longer waits says how it stands: a message's result, a failure, a row not run", () => {
  const sent = (delivery: "sent" | "queued" | "refused", words: string): GoingPlanRow => ({
    ...message(0),
    result: { code: "", delivery, message: words, status: delivery === "refused" ? 409 : 201 },
    state: delivery === "refused" ? "failed" : "done",
  });
  expect(rowOutcome(sent("sent", ""))).toEqual({ failed: false, text: "Sent" });
  expect(rowOutcome(sent("queued", ""))).toEqual({
    failed: false,
    text: "Queued: Dispatch delivers it when the session can take it",
  });
  expect(rowOutcome(sent("refused", "the session is gone"))).toEqual({
    failed: true,
    text: "Refused: the session is gone",
  });
  expect(
    rowOutcome({
      ...update(1, "DEMO-1", { priority: 1 }),
      result: { code: "FORBIDDEN", delivery: null, message: "you have no access", status: 403 },
      state: "failed",
    })
  ).toEqual({ failed: true, text: "you have no access" });
  expect(rowOutcome({ ...update(2, "DEMO-2", { priority: 1 }), state: "skipped" })).toEqual({
    failed: false,
    text: "Not run: it needs the change above",
  });
  expect(rowOutcome({ ...update(3, "DEMO-3", { priority: 1 }), state: "unticked" })?.text).toBe(
    "Not run: you left it unticked"
  );
  for (const state of ["approved", "running", "done", "waiting"] as const) {
    expect(rowOutcome({ ...update(4, "DEMO-4", { priority: 1 }), state })).toBeUndefined();
  }
});

test("Undo's answer names a kept value as data, and a message sent, not undone", () => {
  expect(
    keptValue({
      by: { id: "bob", kind: "user" },
      field: "title",
      message: `holds "Bob's dome" now, which user bob wrote after this run, so it is kept`,
      now: "Bob's dome",
      ok: false,
    })
  ).toEqual({ by: { id: "bob", kind: "user" }, now: "Bob's dome" });
  // A value cleared since is kept as no value, which is data too.
  expect(keptValue({ field: "route", message: "holds null now…", now: null, ok: false })).toEqual({
    by: undefined,
    now: null,
  });
  // An answer of a server that named the value in its message alone, and a field written back.
  expect(keptValue({ field: "title", message: "holds …", ok: false })).toBeUndefined();
  expect(
    keptValue({ field: "title", message: "written back", now: "x", ok: true })
  ).toBeUndefined();
  expect(undoWords(undoLine({ tool: "dispatch_message", undo: "not_undone" }))).toBe(
    "Sent, not undone"
  );
  expect(undoWords(undoLine({}))).toBe("Reverted");
  expect(
    undoWords(
      undoLine({ outcome: { code: "X", message: "the issue was deleted", ok: false, status: 404 } })
    )
  ).toBe("Not reverted: the issue was deleted");
});
