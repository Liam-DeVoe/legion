import { useQuery } from "@tanstack/react-query";
import { useCallback, useMemo } from "react";

import type { IssueDetails } from "../../../api/types";
import { compareTimestamps } from "../../../lib/timestamps";
import { standsFailed } from "../../refs/reference-target";
import { wordsOf } from "../../refs/tree-fragment";
import { type DecisionNode, decisionNodes } from "../decision-nodes/decision-nodes";
import { type ChangeComment, decisionNodeChanges } from "../decision-review";
import type { TreeNode } from "../issue-tree";
import { treeRead } from "../spec-filter/tree-read";
import { treeContext } from "../tree-drafts";
import { nodesInOrder } from "../tree-keys";
import { issueTreeQuery, type TreeAnswer, viewQuery } from "../tree-read/tree-read";
import { issueAsksQuery } from "../useIssueTree";
import {
  askIdOf,
  type DecisionsEntry,
  type DecisionsParts,
  decisionsParts,
  placements,
} from "./decisions-parts";

/**
 * The one seam between the page's reads and the Decisions view: it
 * turns the decisions view, the tree read and the issue's asks into the view's
 * parts, so no other file of the view names a read. Once the server's decisions model gives the
 * view its entries, that model's read replaces this module and leaves the view as it is.
 *
 * The decisions are the decision nodes of the decisions view (`?view=decisions`), which holds
 * the issue's decisions wherever they are written. The page holds that view on the issue's node
 * (`TreeNode.views`), and each decision is read as every reader of a decision reads it
 * (`decisionNodes`). A decision's Change is the comment the tree read hangs under its node
 * (`decision-review.ts`' `decisionNodeChanges`), so no quote is matched for it. A view that
 * holds no decision gives none, and no error.
 *
 * The page reads the view the first time the filter is on in a load (`ViewReadProvider`). A Keep
 * or a Change drops every view the page holds (`DecisionActions.tsx`), and every change of the
 * issue marks it stale and reads it again while the answer stays drawn
 * (`subscribeTreeFreshness`). Until the view answers, and where its read fails, the decisions
 * are the decision nodes of the tree read the page holds, which has the same nodes with the same
 * members. So every decision stands in its part with its state, never in the folded foot for want
 * of a row, and the focus stays with it.
 *
 * The reads are the page's own, under the keys it reads them by (`useIssueTree.ts`,
 * `ViewReadProvider`): the tree read, the decisions view and the issue's asks, the asks by the
 * tree's own definition of that read (`issueAsksQuery`), each watched and never sent, so the view
 * adds no request of its own and reads none of them again when it is opened.
 *
 * The view draws four parts. A decision another person made is no part of its own: it stands
 * under the reader's decisions, with that person's name on its row, so the model's `others` is
 * put in `yours`, newest first, and `others` is left empty.
 *
 * The seam says too, beside the parts, whether the view knows what the issue holds
 * (`readsHeld`), which the outline's keys (`useTreeKeymap.ts`) and the body's empty line read.
 * The view's rows are the tree read's: the parts walk the page's tree (`decisionsParts`), which
 * holds the issue alone until the tree read answers (`issue-tree.ts`'s `buildIssueTree`), so
 * before then no decision has a row to stand on, whatever the decisions view holds. So until the
 * tree read has answered, and the asks read too, a view with no question or no decision to go to
 * may be one whose questions and decisions have not arrived, so `q` and `w` with nowhere to go
 * say nothing rather than that there is none, and the body draws no line that the issue holds
 * nothing, as the outline does (`slots/body.tsx`).
 *
 * The seam says too which of the two kinds of read the entries come from stands refused
 * (`unreadable`), which of them is being read now (`retrying`) and how many times each has
 * failed (`failures`), and asks one of them again (`retry`). The decisions' reads are the tree
 * read and the decisions view: they stand refused where the tree read stands failed, or where it
 * is still out and the decisions view stands failed, and their Retry reads again each of the two
 * that stands failed, or the tree read where neither does. The asks read is the other: a
 * person's decision is a question they answered, and the open questions are the issue's open
 * asks, which only the asks read says. A read stands refused by the page's one rule for a failed
 * read (`standsFailed`): it holds no answer, and a fetch of it failed since it last held one. Its
 * status is not the rule: TanStack Query sets a read that holds no answer back to pending, its
 * error cleared, the moment it runs again, and the read stays refused until it answers. The body
 * draws a line with a Retry for each refused read, in place of the entries that read would give;
 * while the read runs again the line stays, with the focus its Retry holds, the Retry reads busy,
 * and a Retry pressed then asks nothing more. The line says the read's failure again only for a
 * new one: a run that ends with none, cancelled or paused while the browser is offline, leaves
 * the count as it was, and with it the line's alert. How a read stands and how it is asked again
 * belong to the read, and only this module names one, so they are read here and the body knows
 * a read only by the word its line is drawn for. A read whose refetch fails over an answer it
 * already holds keeps that answer, which the view still draws from, so it is not among the
 * refused.
 *
 * The seam says too which decisions are a person's write-up of an answered question
 * (`writeUps`) - a person's decision node that settles an answered ask of this issue (`asks`),
 * which the seam leaves out of every part - and which questions each one restates: an address
 * naming a write-up lands on the row of a question it answers, and only the decisions and the
 * asks can tell a write-up from a person's decision of its own.
 *
 * The hook is the module's one export but `changesOf`, which the parts' tests read the Changes
 * by, and its answer's type is its own, so the read that replaces this module has one name to
 * give the view and nothing else of it to keep.
 */

/** The two kinds of read the entries come from, in the order the body says which of them stands
 *  refused: the decisions' reads, the tree read and the decisions view, and the asks. */
type DecisionsRead = "decisions" | "asks";

/** The seam's answer: the view's parts, and beside them whether the view knows what the issue
 *  holds, which of its reads stands refused, which runs now and how many times each has failed,
 *  and how to ask one again. */
interface DecisionsEntries {
  /** How many times each of the decisions' reads and the asks has failed, by its word (TanStack
   *  Query's `errorUpdateCount`, the tree read's and the decisions view's together). It keys the
   *  alert of a refused read's line (`FailedReadLine`'s `failure`), so each new failure is said
   *  again, and a run that ends with none, cancelled or paused while the browser is offline, says
   *  nothing again. */
  readonly failures: Readonly<Record<DecisionsRead, number>>;
  /** Which entry stands in which part, each part in the order it is drawn. Another person's
   *  decisions stand in `yours`, so `others` is empty. */
  readonly parts: DecisionsParts;
  /** Whether the view knows what the issue holds: the tree read, which its rows come from, and
   *  the asks have both answered. A read that failed holds no answer. */
  readonly readsHeld: boolean;
  /** Asks `read` again, by its own queries' refetch: the asks read, and for the decisions each
   *  of the tree read and the decisions view that stands refused, or the tree read, which the
   *  rows come from, where neither does. */
  readonly retry: (read: DecisionsRead) => void;
  /** Each of the decisions' reads and the asks, in that order, that is being read now, a first
   *  read, a Retry's or any other (TanStack Query's `isFetching`): for the decisions, the tree
   *  read or the decisions view. A refused read stays refused while it runs again, so its line
   *  stays drawn, its Retry busy. */
  readonly retrying: readonly DecisionsRead[];
  /** Each of the decisions' reads and the asks, in that order, that stands refused
   *  (`standsFailed`): it holds no answer, and a fetch of it failed since it last held one. A read
   *  stays here while it runs again, until it answers. */
  readonly unreadable: readonly DecisionsRead[];
  /** Each person's write-up of an answered question of this issue (a person's decision node that
   *  settles an answered ask of this issue), by its node's id, with the node ids of the answered
   *  questions it restates. The list draws no row for a write-up, so an address naming one lands
   *  on the row of a question it answers. Undefined while the asks read holds no answer and is
   *  not refused: until it answers, an answer it brings may make any person's decision a
   *  write-up. */
  readonly writeUps: ReadonlyMap<string, ReadonlySet<string>> | undefined;
}

/** The Decisions view's parts and how the reads they come from stand, from the issue, the
 *  reader's login, the whole tree and the tree as the Decisions view leaves it (`presetView`). */
export function useDecisionsEntries({
  issue,
  reader,
  root,
  view,
}: {
  issue: IssueDetails;
  /** Who is reading, which tells their own decisions from other people's. */
  reader: string | undefined;
  /** The whole tree, which holds each ask and each decision and each statement a tie names, and
   *  on the issue's node the decisions view the page holds (`TreeNode.views`). */
  root: TreeNode;
  /** The tree as the Decisions view leaves it. */
  view: TreeNode;
}): DecisionsEntries {
  // The tree read, which holds each Change under its decision, and the decisions until the view
  // answers.
  const issueTreeRead = useQuery(treeRead(issueTreeQuery(issue.key)));
  const tree = issueTreeRead.data;
  // The decisions view read, watched the same way for how it stands: the page reads it
  // (`ViewReadProvider`) and holds its answer on the issue's node (`TreeNode.views`).
  const viewRead = useQuery(treeRead(viewQuery(issue.key, "decisions")));
  // The issue's asks, which say which answered question is a person's decision
  // (`decisionNodes`), watched the same way: the page's tree reads them under this key.
  const asksRead = useQuery(treeRead(issueAsksQuery(issue.key)));
  const asks = asksRead.data;
  // The decisions view the page holds, and the tree read wherever the view holds no answer.
  const held = root.views?.decisions ?? tree;
  const decisions = useMemo(
    () => (held === undefined ? undefined : decisionNodes(held, { asks, issue })),
    [asks, held, issue]
  );
  const changes = useMemo(() => changesOf(tree, decisions ?? []), [decisions, tree]);
  // The rows are the tree read's (see above): the view knows what the issue holds once that read
  // has answered, and the asks read too.
  const readsHeld = tree !== undefined && asks !== undefined;
  // A read stands refused by the page's one rule (`standsFailed`), as the Spec view judges the
  // same reads: it holds no answer, and a fetch of it failed since it last held one. Not by its
  // status: a refetch that fails over the answer the read last gave sets the error and keeps that
  // answer, which the view draws from, and a read that holds no answer reads pending again, its
  // error cleared, the moment it runs again, though it stays refused until it answers. A refused
  // decisions view counts only while neither it nor the tree read holds an answer.
  const treeRefused = standsFailed(issueTreeRead);
  const viewRefused = standsFailed(viewRead);
  const decisionsRefused = treeRefused || (held === undefined && viewRefused);
  const asksRefused = standsFailed(asksRead);
  const unreadable = useMemo<readonly DecisionsRead[]>(
    () => [
      ...(decisionsRefused ? (["decisions"] as const) : []),
      ...(asksRefused ? (["asks"] as const) : []),
    ],
    [asksRefused, decisionsRefused]
  );
  const decisionsFetching = issueTreeRead.isFetching || viewRead.isFetching;
  const asksFetching = asksRead.isFetching;
  const retrying = useMemo<readonly DecisionsRead[]>(
    () => [
      ...(decisionsFetching ? (["decisions"] as const) : []),
      ...(asksFetching ? (["asks"] as const) : []),
    ],
    [asksFetching, decisionsFetching]
  );
  const decisionsFailures = issueTreeRead.errorUpdateCount + viewRead.errorUpdateCount;
  const asksFailures = asksRead.errorUpdateCount;
  const failures = useMemo<Readonly<Record<DecisionsRead, number>>>(
    () => ({ asks: asksFailures, decisions: decisionsFailures }),
    [asksFailures, decisionsFailures]
  );
  // Each read is asked again by its own query, so a Retry asks for its reads and no other.
  const { refetch: readTree } = issueTreeRead;
  const { refetch: readView } = viewRead;
  const { refetch: readAsks } = asksRead;
  const retry = useCallback(
    (read: DecisionsRead) => {
      if (read === "asks") {
        void readAsks();
        return;
      }
      if (treeRefused || !viewRefused) void readTree();
      if (viewRefused) void readView();
    },
    [readAsks, readTree, readView, treeRefused, viewRefused]
  );
  // How this issue addresses a node, which is the one rule the tree was built with, so a
  // `shaped` tie resolves to the node it names (`tree-drafts.ts`). `nodeIdOf` names no block by
  // a line, so the line rule it is handed is the reading's words, as the parts' tests hand it.
  const nodeIdOf = useMemo(() => treeContext({ issue }, wordsOf).nodeIdOf, [issue]);
  // The write-ups, read as the model reads the decisions (`decisionsParts`): an answered question
  // is an ask the tree holds with an answer on it, named by the ask's own id, which is how a
  // decision names the question it settles (`asks`), and a decision's row is the one its node's
  // address names. A write-up is a person's decision node that settles an answered ask of this
  // issue: a person's decision is the answered ask itself, so the node restates it. A node drawn
  // under two parents is one node, read at its first placement in the reading order, as the
  // model reads it (`placements`).
  const writeUps = useMemo(() => {
    if (asks === undefined && !asksRefused) return undefined;
    const questions = new Map<string, string>();
    const decided: TreeNode[] = [];
    for (const { node } of placements(root).values()) {
      const id = askIdOf(node);
      if (id !== undefined && node.answer !== undefined) questions.set(id, node.id);
      if (node.type === "decision") decided.push(node);
    }
    const byRef = new Map((decisions ?? []).map((decision) => [decision.ref, decision]));
    const found = new Map<string, ReadonlySet<string>>();
    for (const node of decided) {
      const decision = byRef.get(node.reference ?? "");
      if (decision?.kind !== "person") continue;
      const restated = (decision.asks ?? []).flatMap((ask) => questions.get(ask) ?? []);
      if (restated.length > 0) found.set(node.id, new Set(restated));
    }
    return found;
  }, [asks, asksRefused, decisions, root]);
  // The model's parts, from its inputs alone: how a read stands is no input of the model, and
  // neither are the write-ups, so while only those change it is not worked out again.
  const model = useMemo(
    () =>
      decisionsParts({
        asks,
        changes,
        decisions,
        issueKey: issue.key,
        nodeIdOf,
        reader,
        root,
        view,
      }),
    [asks, changes, decisions, issue.key, nodeIdOf, reader, root, view]
  );
  // The view's parts, from the model and the write-ups once the asks read tells them: while only
  // how a read stands changes, the parts keep their identity and nothing the body works out from
  // them is worked out again. A write-up is no entry of its own (R2, R9): the question it
  // restates is the person's decision, so its decision entry leaves every part.
  const parts = useMemo<DecisionsParts>(() => {
    const own = <Entry extends DecisionsEntry>(entries: readonly Entry[]): Entry[] =>
      entries.filter((entry) => entry.kind !== "decision" || !writeUps?.has(entry.node.id));
    const yours =
      model.others.length === 0
        ? model.yours
        : newestFirst([...model.yours, ...model.others], root);
    return {
      ...model,
      agents: own(model.agents),
      others: [],
      review: own(model.review),
      yours: own(yours),
    };
  }, [model, root, writeUps]);
  return { failures, parts, readsHeld, retry, retrying, unreadable, writeUps };
}

/**
 * The Change each of `decisions` waits on, by the decision's own address (`Decision.ref`), as
 * the parts read one (`decisionsParts`' `changes`): the comment the tree read hangs under the
 * decision's node that `decisionNodeChanges` gives it, with its words, its author and the time
 * it was made. The decisions are the decisions view's, or the tree read's, and the Changes the
 * tree read's, which answer one node by the same id. None while the tree read has not answered.
 */
export function changesOf(
  tree: TreeAnswer | undefined,
  decisions: readonly DecisionNode[]
): ReadonlyMap<string, ChangeComment> {
  const changes = new Map<string, ChangeComment>();
  if (tree === undefined) return changes;
  const asked = decisionNodeChanges(tree);
  for (const decision of decisions) {
    const comment = asked.get(decision.node.id);
    // A Change is a person's comment the read records a time for (`decisionNodeChanges`), so it
    // always has an author, the first the read lists for it, and a time.
    const author = comment?.author?.[0];
    if (comment === undefined || author === undefined || comment.made_at === null) continue;
    changes.set(decision.ref, {
      author,
      body: comment.text,
      created_at: comment.made_at,
      id: comment.id,
    });
  }
  return changes;
}

/**
 * `entries` with the newest first, in the order G's model keeps each part
 * (`decisions-parts.ts`'s `newestFirst`), so the reader's decisions and another person's read as
 * one part: an entry the model holds no time for stands last, and two entries of one moment
 * stand in the order their nodes first stand in the page's reading order (`nodesInOrder`),
 * which is the order the outline draws them in. The same reads always draw the same list.
 */
function newestFirst(
  entries: readonly DecisionsEntry[],
  root: TreeNode
): readonly DecisionsEntry[] {
  // Each node's place in the reading order; a node drawn under two parents is one node, so its
  // first placement is its place.
  const place = new Map<string, number>();
  for (const node of nodesInOrder(root)) if (!place.has(node.id)) place.set(node.id, place.size);
  return [...entries].sort((one, other) => {
    const left = one.at;
    const right = other.at;
    if (left !== undefined && right !== undefined) {
      const newer = compareTimestamps(right, left);
      if (newer !== 0) return newer;
    } else if (left !== right) {
      return left === undefined ? 1 : -1;
    }
    // Every entry's node is a node of `root`, which the model read its entries from.
    return (place.get(one.node.id) ?? place.size) - (place.get(other.node.id) ?? place.size);
  });
}
