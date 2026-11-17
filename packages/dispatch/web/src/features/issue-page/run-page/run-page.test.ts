import { expect, test } from "bun:test";
import {
  GOING_ERROR_ANSWER_DID_NOT_ARRIVE,
  GOING_ERROR_ANSWER_FAILED,
  GOING_ERROR_NO_MODEL_CONFIGURED,
  GOING_EVENT_CHANGED,
  GOING_EVENT_CUT,
  GOING_EVENT_END,
  GOING_EVENT_ERROR,
  GOING_EVENT_ITEM,
  GOING_EVENT_NONE,
  GOING_EVENT_TEXT,
} from "@legion/contracts";

import { graphNode } from "../../../__tests__/graph-fixture";
import type { GraphEdge, GraphReferences } from "../../../api/types";
import {
  EMPTY_STATE_LINES,
  NODE_REFERENCES_WORDS,
  NODE_TYPE_NAMES,
  STATE_LINES,
  sentenceCase,
} from "../page-words";
import { type RunFrame, type RunWords, runView } from "./run-page";

const WORDS: RunWords = { EMPTY_STATE_LINES, NODE_REFERENCES_WORDS, NODE_TYPE_NAMES, STATE_LINES };

const RUN = "3f2b8c1e-5d4a-4b6f-9e2d-1a7c0b9e8f43";
const ASKED_AT = "2026-11-17T09:00:00Z";
const QUESTION = "When does the release ship?\n\nAnd who **owns** it?";

/** The run's read: its node, carrying `frames` as INFRA-1950's answer does, and `edges`. */
function answerOf(frames: readonly RunFrame[], edges: readonly GraphEdge[] = []): GraphReferences {
  return {
    edges: [...edges],
    node: Object.assign(
      graphNode({
        id: RUN,
        kind: "interaction",
        made_at: ASKED_AT,
        ref: `dispatch://interaction/${RUN}`,
        text: QUESTION,
      }),
      { frames }
    ),
  };
}

/** An edge from the issue CORE-1, which the run changed. */
const CHANGED_ISSUE: GraphEdge = {
  created_at: "2026-11-17T09:00:05Z",
  direction: "in",
  excerpt: { text: "Ship the **release** on Friday. Then rest." },
  kind: "changed_by_run",
  node: graphNode({ id: "CORE-1", issue_key: "CORE-1", kind: "issue", ref: "dispatch://CORE-1" }),
  source_seq: null,
};

/** A frame of the answer. */
function frame(sequence: number, name: string, data: RunFrame["data"] = null): RunFrame {
  return { data, name, sequence };
}

test("the view holds the question, the time, each frame in order with its drawing, and the changed nodes", () => {
  // The frames as the read may hold them, out of their order.
  const frames = [
    frame(5, GOING_EVENT_CHANGED, { dropped: 2 }),
    frame(2, GOING_EVENT_TEXT, { text: "ships on Friday\\." }),
    frame(6, GOING_EVENT_END, { items: 1, words: 120 }),
    frame(3, GOING_EVENT_ITEM, {
      href: "/issues/CORE-1",
      id: "CORE-1",
      kind: "issue",
      title: "The release plan",
    }),
    frame(1, GOING_EVENT_TEXT, { text: "The release " }),
    frame(4, GOING_EVENT_CUT, { words: 120 }),
  ];
  const view = runView(answerOf(frames, [CHANGED_ISSUE]), WORDS);

  expect(view.question).toBe(QUESTION);
  expect(view.at).toBe(ASKED_AT);
  // In `sequence` order: the two texts as one answer, the item by its title and its link alone,
  // the cut and the changed frames as their sentences, and nothing for the end.
  expect(view.parts).toEqual([
    { kind: "answer", markdown: "The release ships on Friday\\." },
    { href: "/issues/CORE-1", kind: "item", title: "The release plan" },
    { kind: "line", text: `${STATE_LINES.run_cut.before}120${STATE_LINES.run_cut.after}` },
    {
      kind: "line",
      text: `${STATE_LINES.run_changed.before}2${STATE_LINES.run_changed.after}`,
    },
  ]);
  // The issue it changed, by its kind's word and the first line of its words, linked to it.
  expect(view.changed).toEqual([
    {
      href: "/issues/CORE-1",
      key: "dispatch://CORE-1",
      kind: sentenceCase(NODE_REFERENCES_WORDS.kinds.issue),
      line: "Ship the release on Friday.",
    },
  ]);

  // A node two edges name is one changed node.
  expect(runView(answerOf([], [CHANGED_ISSUE, CHANGED_ISSUE]), WORDS).changed).toHaveLength(1);
  // A count that is no number draws as 0.
  expect(runView(answerOf([frame(1, GOING_EVENT_CUT, { words: "many" })]), WORDS).parts).toEqual([
    { kind: "line", text: `${STATE_LINES.run_cut.before}0${STATE_LINES.run_cut.after}` },
  ]);

  // No answer at all, and each way a run fails, by its line; a code the page does not know, and
  // none, draw the answer having failed.
  const lines = (frames: readonly RunFrame[]) =>
    runView(answerOf(frames), WORDS).parts.map((part) => (part.kind === "line" ? part.text : ""));
  expect(lines([frame(1, GOING_EVENT_NONE), frame(2, GOING_EVENT_END)])).toEqual([
    EMPTY_STATE_LINES.run_none,
  ]);
  const errors = [
    GOING_ERROR_NO_MODEL_CONFIGURED,
    GOING_ERROR_ANSWER_DID_NOT_ARRIVE,
    GOING_ERROR_ANSWER_FAILED,
    "SOMETHING_ELSE",
    undefined,
  ];
  expect(errors.map((code) => lines([frame(1, GOING_EVENT_ERROR, { code })]))).toEqual([
    [STATE_LINES.run_no_model],
    [STATE_LINES.run_did_not_arrive],
    [STATE_LINES.run_failed],
    [STATE_LINES.run_failed],
    [STATE_LINES.run_failed],
  ]);
  // A frame of a name the page does not know draws nothing.
  expect(lines([frame(1, "progress", { text: "searching" })])).toEqual([]);
});
