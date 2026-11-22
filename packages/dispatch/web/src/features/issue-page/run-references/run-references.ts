import { useQuery } from "@tanstack/react-query";
import { useMemo } from "react";

import type { GraphEdge, IssueDetails } from "../../../api/types";
import type { ExtraReferenceRow } from "../../refs/ReferencedBy";
import { standsFailed } from "../../refs/reference-target";
import { buildRunPath } from "../../refs/routes";
import { retryRead } from "../details-error";
import { NODE_REFERENCES_WORDS, sentenceCase } from "../page-words";
import { plainWords } from "../tree-drafts";
import { issueTreeQuery, type TreeAnswer, type TreeAnswerNode } from "../tree-read/tree-read";

/**
 * The command bar runs that changed the issue or a node of it, as rows of Details' Referenced by.
 * A run writes one `changed_by_run` tuple from each node it created or changed to itself, an end
 * of the store kind `interaction` named by the run's id. The tree read answers each run such a
 * tuple of a node it holds names as a context node of that kind and of the `interaction`
 * supertag, by the run's id, with the asker's whole question as its text (`issue_tree_read.go`'s
 * `readContext`). Only the asker may see a run, so another reader's tree holds none and draws no
 * row.
 */

/** The store kind a `changed_by_run` tuple names a command bar run by, and the supertag the tree
 *  read answers the run's node with. */
const RUN = "interaction";

/** The tuple kind from a node a run created or changed to that run. */
const CHANGED_BY_RUN: GraphEdge["kind"] = "changed_by_run";

const NO_ROWS: readonly ExtraReferenceRow[] = [];

/** When `run` was made, as a time that orders; a run with no time, or a time that does not parse,
 *  orders as the oldest. */
function madeAt(run: TreeAnswerNode): number {
  const time = run.made_at === null ? Number.NaN : Date.parse(run.made_at);
  return Number.isNaN(time) ? Number.NEGATIVE_INFINITY : time;
}

/** A run with when it was made (`madeAt`), which orders it. */
type DatedRun = { readonly run: TreeAnswerNode; readonly time: number };

/** Newest first, and runs made at the same time by id, byte by byte. */
function newestFirst(a: DatedRun, b: DatedRun): number {
  if (a.time !== b.time) return a.time < b.time ? 1 : -1;
  if (a.run.id === b.run.id) return 0;
  return a.run.id < b.run.id ? -1 : 1;
}

/** The row of one run: its kind and its edge in the page's words, its question read to plain
 *  words, when it was made - no `at` where the read answers no time, so the row draws none - and
 *  the run's own page. Only the key and the link hold the run's id. */
function runRow(run: TreeAnswerNode): ExtraReferenceRow {
  const excerpt = plainWords(run.text);
  return {
    ...(run.made_at === null ? {} : { at: run.made_at }),
    edge: NODE_REFERENCES_WORDS.edges.changed_by_run,
    ...(excerpt.trim() === "" ? {} : { excerpt }),
    href: buildRunPath(run.id),
    key: run.id,
    kind: sentenceCase(NODE_REFERENCES_WORDS.kinds.interaction),
  };
}

/** One row for each run of `tree` that a `changed_by_run` tuple names (`tuple.to`), once however
 *  many nodes it changed, newest first. A run no such tuple names draws no row, and nor does a
 *  tree not read yet. */
export function runRows(tree: TreeAnswer | undefined): readonly ExtraReferenceRow[] {
  if (tree === undefined) return NO_ROWS;
  const changed = new Set<string>();
  for (const tuple of tree.tuples ?? []) {
    if (tuple.kind === CHANGED_BY_RUN && tuple.to.kind === RUN) changed.add(tuple.to.id);
  }
  if (changed.size === 0) return NO_ROWS;
  const runs: DatedRun[] = [];
  for (const node of tree.nodes ?? []) {
    // Taking the id out of the set as its node is taken keeps one run to one row.
    if (node.supertag === RUN && changed.delete(node.id)) {
      runs.push({ run: node, time: madeAt(node) });
    }
  }
  if (runs.length === 0) return NO_ROWS;
  return runs.sort(newestFirst).map(({ run }) => runRow(run));
}

/** The run rows of the issue's tree read, and how that read stands, as Details says a read of
 *  its own failed (`DetailsErrorLine`). */
export interface RunRowsRead {
  /** The rows (`runRows`), the same value until the read's answer changes; none while the read
   *  holds no answer. */
  readonly rows: readonly ExtraReferenceRow[];
  /** Whether the rows are known: the read holds an answer, or stands failed. */
  readonly settled: boolean;
  /** Whether the read stands failed (`standsFailed`): it holds no answer, and a read of it has
   *  failed. It stays so while a read sent again is out. A refresh that fails over an answer the
   *  read holds is no such failure, and the rows that answer drew stay. */
  readonly failed: boolean;
  /** Which failure the read's line says (`DetailsErrorLine`'s `failure`): the read's
   *  `errorUpdateCount`. */
  readonly failure: number;
  /** Reads the tree again, joining a read that is out (`retryRead`). */
  readonly retry: () => void;
  /** Whether a read is out while the read stands failed: its Retry reads busy. */
  readonly retrying: boolean;
}

/**
 * The rows of the runs that changed `issue` or a node of it, from the tree read the outline made.
 * It observes that read without enabling it, so it sends no request of its own: the page's first
 * load and the reader opening Referenced by send none for runs, and only `retry` reads the tree
 * again. Only the asker's tree read holds a run, so another reader's rows are none.
 */
export function useRunRows(issue: IssueDetails): RunRowsRead {
  const read = useQuery({ ...issueTreeQuery(issue.key), enabled: false, select: runRows });
  const { data: rows, errorUpdateCount, refetch } = read;
  const failed = standsFailed(read);
  // `isFetching` is read only while the read stands failed, the one time its Retry is drawn.
  const retrying = failed && read.isFetching;
  return useMemo(
    () => ({
      failed,
      failure: errorUpdateCount,
      retry: retryRead({ refetch }),
      retrying,
      rows: rows ?? NO_ROWS,
      settled: rows !== undefined || failed,
    }),
    [errorUpdateCount, failed, refetch, retrying, rows]
  );
}
