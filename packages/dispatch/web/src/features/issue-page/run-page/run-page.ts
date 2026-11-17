import {
  GOING_ERROR_ANSWER_DID_NOT_ARRIVE,
  GOING_ERROR_NO_MODEL_CONFIGURED,
  GOING_EVENT_CHANGED,
  GOING_EVENT_CUT,
  GOING_EVENT_ERROR,
  GOING_EVENT_ITEM,
  GOING_EVENT_NONE,
  GOING_EVENT_TEXT,
} from "@legion/contracts";
import { queryOptions, type UseQueryResult, useQuery } from "@tanstack/react-query";

import { ApiError, api } from "../../../api/client";
import { queryRetry } from "../../../api/query-client";
import type { GraphEdge, GraphReferences } from "../../../api/types";
import { referencesQuery } from "../../refs/reference-target";
import { buildReferencePath, parseNodeReference } from "../../refs/routes";
import { drawnLine, firstLineOf } from "../issue-tree";
import {
  type EMPTY_STATE_LINES,
  type NODE_REFERENCES_WORDS,
  type NODE_TYPE_NAMES,
  type STATE_LINES,
  sentenceCase,
} from "../page-words";
import { plainWords } from "../tree-drafts";

/**
 * A command bar run's own page (dispatch://INFRA-4064 R3), read without a browser: its address,
 * its one read, and what the page draws of the answer. A run is an `interaction` node, which only
 * the person who asked it can read; the page draws the asker's whole question, each frame of the
 * answer in its own order and words, and the nodes the run changed.
 */

/**
 * One frame of a run's answer, as the graph read carries it on the run's node. This is
 * INFRA-1950's generated `GraphFrame`, written here until that branch reaches this stack; the
 * merge which brings it replaces `RunFrame` with `GraphFrame` from `../../../api/types`.
 */
export interface RunFrame {
  data: { [key: string]: unknown } | null;
  name: string;
  sequence: number;
}

/** The run's frames, as its node carries them: none where the node carries none. */
export function runFramesOf(node: GraphReferences["node"]): readonly RunFrame[] {
  // INFRA-1950's generated `GraphFrame`, written here until that branch reaches this stack: the
  // merge which brings it gives `GraphNode` its `frames` and replaces this cast and `RunFrame`
  // with `GraphFrame` from `../../../api/types`.
  return (node as GraphReferences["node"] & { readonly frames?: readonly RunFrame[] }).frames ?? [];
}

/** A run's id: the lowercase canonical uuid the last segment of its address holds. */
const RUN_ID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/;

/** Whether `id` can name a run at all. Anything else names no run, so nothing is asked for it. */
export function isRunId(id: string | undefined): id is string {
  return id !== undefined && RUN_ID.test(id);
}

/** The `dispatch://` address of the run `id`. */
export function runAddress(id: string): string {
  return `dispatch://interaction/${id}`;
}

/** The edge kind from a node a run changed to that run. */
const CHANGED_BY_RUN = "changed_by_run";

/** Whether a read failed because there is no run to read: the address names none (400), or none
 *  the reader may open (404), which the page cannot tell apart and neither can a retry. */
function isMissingRun(error: unknown): boolean {
  return error instanceof ApiError && (error.status === 400 || error.status === 404);
}

/**
 * The run `id` with the nodes it changed (`GET /api/v1/references?to=<address>&kind=changed_by_run`).
 * Its key is under the backlink panels' `["references", "to", <address>]` (`referencesQuery`), so
 * an event that refreshes the references at the run's address refreshes this read too, and the
 * kind after the address keeps it apart from the unfiltered read of the same address. A run that
 * is missing is asked for once; any other failure is asked again as every read is (`queryRetry`).
 */
export const runQuery = (id: string) =>
  queryOptions({
    queryKey: [...referencesQuery(runAddress(id)).queryKey, CHANGED_BY_RUN] as const,
    queryFn: () => api.getReferences(runAddress(id), { kind: CHANGED_BY_RUN }),
    retry: (failures: number, error: Error) => !isMissingRun(error) && queryRetry(failures, error),
  });

/** `useRun`'s answer: the read, and whether there is no run the reader may open at this id. */
export interface RunRead {
  /** True where `id` is no run id, which sends no request, and where the read answered 400 or
   *  404. */
  readonly notFound: boolean;
  readonly query: UseQueryResult<GraphReferences>;
}

/** The run at the page's `id`, read only where `id` can name one (`isRunId`). */
export function useRun(id: string | undefined): RunRead {
  const named = isRunId(id);
  const query = useQuery({ ...runQuery(id ?? ""), enabled: named });
  return { notFound: !named || isMissingRun(query.error), query };
}

/** The tables of `page-words.ts` the view's words come from, as the module names them. */
export interface RunWords {
  readonly EMPTY_STATE_LINES: Pick<typeof EMPTY_STATE_LINES, "run_none">;
  readonly NODE_REFERENCES_WORDS: Pick<typeof NODE_REFERENCES_WORDS, "kinds">;
  readonly NODE_TYPE_NAMES: typeof NODE_TYPE_NAMES;
  readonly STATE_LINES: Pick<
    typeof STATE_LINES,
    "run_changed" | "run_cut" | "run_did_not_arrive" | "run_failed" | "run_no_model"
  >;
}

/** One piece of the answer, in the order its frames came: the model's text, one thing it cites,
 *  or a line the page says of the answer. */
export type RunPart =
  | { readonly kind: "answer"; readonly markdown: string }
  | { readonly kind: "item"; readonly href: string; readonly title: string }
  | { readonly kind: "line"; readonly text: string };

/** One node the run changed. */
export interface RunChange {
  /** The node's address, which makes it one entry however many edges name it. */
  readonly key: string;
  /** The page's word for the node's kind, sentence-cased. */
  readonly kind: string;
  /** The first line of the node's words, as every place that draws one line for a node draws
   *  it; `""` where the read carries none. */
  readonly line: string;
  /** The page that opens the node; none where its address names no page. */
  readonly href?: string;
}

/** What the run's page draws. */
export interface RunView {
  /** The asker's question, whole and as typed. */
  readonly question: string;
  /** When the run was asked; null where the read says nothing. */
  readonly at: string | null;
  readonly parts: readonly RunPart[];
  readonly changed: readonly RunChange[];
}

/** A frame's count, where a count that is no number reads as 0. */
function countOf(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}

/** A frame's string field, where any other value reads as `""`. */
function stringOf(value: unknown): string {
  return typeof value === "string" ? value : "";
}

/** The line an `error` frame draws for its code: `GOING_ERROR_ANSWER_FAILED`, a code the page
 *  does not know, and no code at all are each the answer having failed. */
function errorLine(code: unknown, lines: RunWords["STATE_LINES"]): string {
  switch (code) {
    case GOING_ERROR_ANSWER_DID_NOT_ARRIVE:
      return lines.run_did_not_arrive;
    case GOING_ERROR_NO_MODEL_CONFIGURED:
      return lines.run_no_model;
    default:
      return lines.run_failed;
  }
}

/** The parts `frames` draw, in `sequence` order: a run of `text` frames is one answer, and an
 *  `end` frame, or a frame of a name the page does not know, draws nothing. */
function partsOf(frames: readonly RunFrame[], words: RunWords): RunPart[] {
  const lines = words.STATE_LINES;
  const parts: RunPart[] = [];
  for (const frame of [...frames].sort((a, b) => a.sequence - b.sequence)) {
    const data = frame.data ?? {};
    switch (frame.name) {
      case GOING_EVENT_TEXT: {
        const last = parts.at(-1);
        const text = stringOf(data.text);
        if (last?.kind === "answer") {
          parts[parts.length - 1] = { kind: "answer", markdown: `${last.markdown}${text}` };
        } else {
          parts.push({ kind: "answer", markdown: text });
        }
        break;
      }
      case GOING_EVENT_ITEM:
        parts.push({ href: stringOf(data.href), kind: "item", title: stringOf(data.title) });
        break;
      case GOING_EVENT_CUT:
        parts.push({
          kind: "line",
          text: `${lines.run_cut.before}${countOf(data.words)}${lines.run_cut.after}`,
        });
        break;
      case GOING_EVENT_CHANGED:
        parts.push({
          kind: "line",
          text: `${lines.run_changed.before}${countOf(data.dropped)}${lines.run_changed.after}`,
        });
        break;
      case GOING_EVENT_NONE:
        parts.push({ kind: "line", text: words.EMPTY_STATE_LINES.run_none });
        break;
      case GOING_EVENT_ERROR:
        parts.push({ kind: "line", text: errorLine(data.code, lines) });
        break;
    }
  }
  return parts;
}

/** The nodes `edges` name, once each, in the read's order: each by its kind's word, the first
 *  line of its words, and the page its address opens. */
function changedOf(edges: readonly GraphEdge[], words: RunWords): RunChange[] {
  const kinds: Readonly<Record<string, string>> = words.NODE_REFERENCES_WORDS.kinds;
  const types: Readonly<Record<string, string>> = words.NODE_TYPE_NAMES;
  const changed = new Map<string, RunChange>();
  for (const edge of edges) {
    const { id, kind, ref } = edge.node;
    const key = ref ?? `${kind}:${id}`;
    if (changed.has(key)) continue;
    const route = ref === undefined ? undefined : parseNodeReference(ref);
    changed.set(key, {
      href: route === undefined ? undefined : buildReferencePath(route),
      key,
      kind: sentenceCase(kinds[kind] ?? types[kind] ?? kind),
      line: drawnLine(firstLineOf(plainWords(edge.excerpt?.text ?? ""))),
    });
  }
  return [...changed.values()];
}

/** What the run's page draws of `answer`, the run's read (`runQuery`), in `words`. */
export function runView(answer: GraphReferences, words: RunWords): RunView {
  return {
    at: answer.node.made_at,
    changed: changedOf(answer.edges ?? [], words),
    parts: partsOf(runFramesOf(answer.node), words),
    question: answer.node.text ?? "",
  };
}
