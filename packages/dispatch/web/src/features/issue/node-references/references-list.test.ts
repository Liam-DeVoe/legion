import { expect, test } from "bun:test";

import { graphNodeFields } from "../../../__tests__/graph-fixture";
import type { GraphEdge } from "../../../api/types";
import { nodeWordsOf, referenceRows } from "./references-list";

const AT = "2026-11-15T10:00:00Z";

function edge(fields: {
  excerpt?: string;
  id: string;
  kind: string;
  nodeKind: string;
  ref?: string;
  text?: string;
}): GraphEdge {
  return {
    created_at: AT,
    direction: "in",
    ...(fields.excerpt === undefined ? {} : { excerpt: { text: fields.excerpt } }),
    kind: fields.kind,
    node: {
      ...graphNodeFields(),
      id: fields.id,
      kind: fields.nodeKind,
      ...(fields.ref === undefined ? {} : { ref: fields.ref }),
      ...(fields.text === undefined ? {} : { text: fields.text }),
    },
    source_seq: null,
  };
}

test("one row per citing node, in the order the read answers them", () => {
  const rows = referenceRows([
    edge({
      excerpt: "Thread 1 opens here.",
      id: "c-1",
      kind: "mentions",
      nodeKind: "comment",
      ref: "dispatch://LAY-2/comment/c-1",
    }),
    edge({
      excerpt: "Check 1 of the redesign.",
      id: "v-1",
      kind: "serves",
      nodeKind: "block",
      ref: "dispatch://LAY-2/artifact/verification-md#b-c1",
    }),
  ]);
  expect(rows.map((row) => row.key)).toEqual(["comment:c-1", "block:v-1"]);
  expect(rows.map((row) => row.line)).toEqual(["Thread 1 opens here.", "Check 1 of the redesign."]);
  expect(rows.map((row) => row.ref)).toEqual([
    "dispatch://LAY-2/comment/c-1",
    "dispatch://LAY-2/artifact/verification-md#b-c1",
  ]);
});

test("a node that cites the row twice is one row", () => {
  const rows = referenceRows([
    edge({
      excerpt: "It names it here.",
      id: "c-1",
      kind: "mentions",
      nodeKind: "comment",
      ref: "dispatch://LAY-2/comment/c-1",
    }),
    edge({
      excerpt: "And again here.",
      id: "c-1",
      kind: "mentions",
      nodeKind: "comment",
      ref: "dispatch://LAY-2/comment/c-1",
    }),
  ]);
  expect(rows).toHaveLength(1);
  expect(rows[0]?.line).toBe("It names it here.");
});

test("a tuple of the model names the row over a mention, and keeps the mention's place", () => {
  const rows = referenceRows([
    edge({
      excerpt: "Task 8 of the redesign.",
      id: "p-8",
      kind: "mentions",
      nodeKind: "block",
      ref: "dispatch://LAY-2/artifact/plan-md#b-t8",
    }),
    edge({
      excerpt: "Thread 1 opens here.",
      id: "c-1",
      kind: "mentions",
      nodeKind: "comment",
      ref: "dispatch://LAY-2/comment/c-1",
    }),
    edge({
      excerpt: "Task 8 of the redesign.",
      id: "p-8",
      kind: "needs",
      nodeKind: "block",
      ref: "dispatch://LAY-2/artifact/plan-md#b-t8",
    }),
  ]);
  expect(rows.map((row) => `${row.key} ${row.kind}`)).toEqual([
    "block:p-8 needs",
    "comment:c-1 mentions",
  ]);
});

test("a typed block's words leave out its directive line and its closing fence", () => {
  const excerpt =
    ':::check{#c1 serves="dispatch://LAY-2/spec#b-r1" when="before_merge"}\n' +
    "Check 1 of the redesign holds its own row and its own state.\n" +
    ":::";
  expect(nodeWordsOf(edge({ excerpt, id: "v-1", kind: "serves", nodeKind: "block" }))).toBe(
    "Check 1 of the redesign holds its own row and its own state."
  );
});

test("a node's own text names it where the read sends no excerpt", () => {
  const rows = referenceRows([
    edge({
      id: "LAY-9",
      kind: "child_of",
      nodeKind: "issue",
      ref: "dispatch://LAY-9",
      text: "The rail becomes an icon rail.",
    }),
  ]);
  expect(rows[0]?.line).toBe("The rail becomes an icon rail.");
});

test("a line is cut where every one line of a node is cut", () => {
  const rows = referenceRows([
    edge({
      excerpt: "It opens on one sentence. The rest of the block follows it and is not drawn.",
      id: "c-1",
      kind: "mentions",
      nodeKind: "comment",
      ref: "dispatch://LAY-2/comment/c-1",
    }),
  ]);
  expect(rows[0]?.line).toBe("It opens on one sentence.");
});

test("a node the read carries no words and no address of is still one row", () => {
  const rows = referenceRows([edge({ id: "s-1", kind: "mentions", nodeKind: "session" })]);
  expect(rows).toEqual([
    { key: "session:s-1", kind: "mentions", line: "", nodeKind: "session", ref: undefined },
  ]);
});
