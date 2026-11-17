import { keepPreviousData, useQueries, useQuery } from "@tanstack/react-query";
import { useMemo } from "react";

import { api } from "../../api/client";
import {
  issueCoverageQuery,
  issueResultsQuery,
  referenceTargetsQuery,
  whoAmIQuery,
} from "../../api/queries";
import type { IssueDetails } from "../../api/types";
import {
  ambiguousTargets,
  buildIssueTree,
  type IssueTreeReads,
  nodeWords,
  type TreeNode,
  type TreeViews,
  targetNames,
} from "./issue-tree";
import { usePublishReferenceNames } from "./reference-names";
import { citedReferences } from "./row-references";
import {
  REFRESHED_BY_EVENTS,
  type TreeBlocksRead,
  type TreeFilter,
  useTreeRead,
  viewQuery,
} from "./tree-read/tree-read";

/** The issue's tree, and how far the tree read it is built from has got. */
export interface IssueTreeView {
  /** Whether the tree read holds an answer for every page, which `root` is built from: what a
   *  view that draws from the blocks it holds waits on. A refetch that fails over an answer
   *  already held keeps it held - TanStack keeps a query's data through a failed refetch, and
   *  `root` is still built from it - so the view keeps the text it drew, which waiting on
   *  `blocksRead` would blank, since that failure turns it false (dispatch://INFRA-1196 decision
   *  98). That is the one case where the two differ: both are false before the first answer and
   *  after a first read that fails. */
  readonly blocksHeld: boolean;
  /** The typed blocks of the document that plays `role` - the spec, the plan, the Verification
   *  document - from the tree read, answered as that document's blocks read answered them
   *  (`tree-read/tree-nodes.ts`'s `blocksOfRole`, dispatch://INFRA-4062 R5), so a view reads its
   *  blocks here and costs no request of its own. */
  readonly blocksOf: (role: TreeFilter) => TreeBlocksRead;
  /** Whether the tree read has answered every page. Until then a typed block - the spec's
   *  summary among them - may be missing from `root` because it has not arrived rather than
   *  because the issue holds none, which is what the head waits on before it says a spec has no
   *  summary (`IssueThreeThings.tsx`). A read that failed has not answered either: the page
   *  cannot say what a read it could not make holds. */
  readonly blocksRead: boolean;
  readonly root: TreeNode;
}

/**
 * Every `dispatch://` address the tree holds: each node's own, so a block's words can be read,
 * each chip's, so a chip can name its target, and each one a node's words or its answer cite, so
 * a row links what its words name from this read and makes none per mention
 * (`row-references.ts`). This is what the batched reference read is given, which is why the
 * page draws every chip and every link in one request rather than one per node (R21); a block
 * whose words come from that read's own answer cites what nothing else cites, which one more
 * read names (`useIssueTree`). It is here, beside the reads it is the key of, and not beside
 * the model, which makes no read; what a text cites is read by `citedReferences`
 * (`row-references.ts`), over the one reader of a text's citations (`text-references.ts`).
 */
function treeReferences(root: TreeNode): string[] {
  const refs = new Set<string>();
  const walk = (node: TreeNode): void => {
    if (node.reference !== undefined) refs.add(node.reference);
    for (const chip of [...node.chips, ...node.alsoUnder]) refs.add(chip.ref);
    for (const reference of citedReferences(nodeWords(node))) refs.add(reference);
    for (const reference of citedReferences(node.answer?.text ?? "")) refs.add(reference);
    for (const child of node.children) walk(child);
  };
  walk(root);
  return [...refs];
}

/** The names the page publishes until the batched name read answers: none, and the same empty
 *  map on every render, so no link beside the tree draws again for a read that names nothing. */
const NO_TARGET_NAMES: ReadonlyMap<string, string> = new Map();

/** The references the page publishes as ambiguous until the batched name read answers: none,
 *  and the same empty set on every render, as `NO_TARGET_NAMES` is the same empty map. */
const NO_AMBIGUOUS_TARGETS: ReadonlySet<string> = new Set();

/**
 * The issue's tree, from the page's reads (dispatch://INFRA-1196 R20, dispatch://INFRA-4062 R1
 * and R5). This hook is where the reads are; `buildIssueTree` is pure and holds none of them, so
 * the model is testable without a server and the page makes no request per node.
 *
 * The tree is the issue's tree read (`useTreeRead`), every page of it, which places, orders and
 * words every node; no document's blocks are read. Every decision is one of its nodes, with its
 * review state, and a person's comment the read hangs under a decision is a change they asked of
 * it (`decision-review.ts`), so no decisions read is made. Beside it stand the reads that tell
 * what the answer does not, each under the key another surface of this issue reads too, so
 * opening Details, the history or the Contents pane after the tree finds them answered rather
 * than fetching again: the coverage, the results, the issue's asks, its comments, its messages,
 * and the reader's login, which says which open questions wait on the reader
 * (`IssueTreeReads.viewer`).
 *
 * Each filter's view read (`viewQuery`), the decisions view among them,
 * is watched here and never sent: `ViewReadProvider`
 * (`tree-read/view-read.tsx`) reads a view the first time its filter is on, and whatever it holds
 * is set on the issue's node (`TreeNode.views`), which `presetView` and `filterSignals` draw a
 * filter from, and the Decisions filter its decisions (`decisions-entries.ts`). The root
 * keeps its identity while neither the tree nor a held view changes.
 *
 * The names come last and all at once: the tree is built, every address it holds is read in one
 * batched request (`referenceTargetsQuery`; a tree holding more addresses than the server reads
 * at once costs one request per batch of them, which no ordinary issue reaches), and the tree is
 * built again with the names in it. A block's stored source, which that read names it by, may
 * cite a whole document of the issue no word of the tree cites, which a chip on the block opens
 * (`tree-drafts.ts`'s `addDocumentChips`), so that address is in no address the read was given:
 * one more read names those addresses alone, made only where a source cites one, and the tree is
 * built with both answers. That is the last read of a round: the second's answer adds no chip.
 * Until the names answer the tree draws, with a chip naming its target's address; on every read
 * after the first the names already in hand stand while the new set loads.
 */
export function useIssueTree(issue: IssueDetails): IssueTreeView {
  const key = issue.key;
  const read = useTreeRead(issue);
  // Each read an event refreshes is read again by that event alone, never by its age, so a slot
  // the stage mounts again later sends none of them (`REFRESHED_BY_EVENTS`).
  const coverage = useQuery({ ...issueCoverageQuery(key), ...REFRESHED_BY_EVENTS });
  const results = useQuery({ ...issueResultsQuery(key), ...REFRESHED_BY_EVENTS });
  const asks = useQuery({
    ...REFRESHED_BY_EVENTS,
    queryKey: ["asks", key],
    queryFn: () => api.listIssueAsks(key, "all"),
  });
  // Under the keys the margin, the history and the focused row's facts read too, so a reader
  // opening the panel after the tree finds them answered rather than read a second time. The
  // tree read answers each comment as a node, so the comments build no part of the tree; the
  // read is held here so that a comment written, a Change among them, reads the tree again
  // (`subscribeTreeFreshness`).
  const comments = useQuery({
    ...REFRESHED_BY_EVENTS,
    queryKey: ["comments", key],
    queryFn: () => api.listComments(key),
  });
  const messages = useQuery({
    ...REFRESHED_BY_EVENTS,
    queryKey: ["issue-messages", key],
    queryFn: () => api.listIssueMessages(key),
  });
  // The reader, whose own turn an open question may be (`IssueTreeReads.viewer`): the shell's
  // one read of who is signed in, so it costs no request of its own.
  const who = useQuery(whoAmIQuery());
  const viewer = who.data?.login;
  // Each filter's view read, watched by an observer that is never enabled, so it sends nothing
  // and holds whatever `ViewReadProvider` read for the load.
  const [specView, verificationView, planView, decisionsView] = useQueries({
    queries: [
      { ...viewQuery(key, "spec"), enabled: false },
      { ...viewQuery(key, "verification"), enabled: false },
      { ...viewQuery(key, "plan"), enabled: false },
      { ...viewQuery(key, "decisions"), enabled: false },
    ],
  });
  const spec = specView?.data;
  const verification = verificationView?.data;
  const plan = planView?.data;
  const decisions = decisionsView?.data;
  const views = useMemo((): TreeViews | undefined => {
    const held: TreeViews = {
      ...(decisions === undefined ? {} : { decisions }),
      ...(plan === undefined ? {} : { plan }),
      ...(spec === undefined ? {} : { spec }),
      ...(verification === undefined ? {} : { verification }),
    };
    return Object.keys(held).length === 0 ? undefined : held;
  }, [decisions, plan, spec, verification]);
  // One value, so the model is given the reads and nothing else.
  const reads = useMemo<IssueTreeReads>(
    () => ({
      asks: asks.data,
      coverage: coverage.data,
      issue,
      messages: messages.data,
      results: results.data,
      tree: read.answer,
      viewer,
    }),
    [asks.data, coverage.data, issue, messages.data, read.answer, results.data, viewer]
  );
  const unnamed = useMemo(() => buildIssueTree(reads), [reads]);
  const refs = useMemo(() => treeReferences(unnamed), [unnamed]);
  // One request for every name, not one per read that landed. Each of these reads adds to the
  // tree as it arrives - the tree read every node, the asks an answer - so asking while any is
  // still out would ask for the addresses known so far and ask again for the rest: measured on
  // this pod, that cost the first load two reads and sometimes three, where the design is "the
  // names come last and all at once" (above) and the issue's own budget is 40 requests
  // (dispatch://INFRA-1196 c-requests). The reader's login is one of the reads this gate waits
  // on: it adds no node, but the tree is built again when it answers, since it says which
  // questions wait on the reader. Every one of these reads is always enabled, so each ends in an
  // answer or an error and none of them holds the names for good. Nothing waits on the names:
  // until they land the tree draws with each chip naming its target's address.
  const answered =
    !read.pending &&
    !asks.isPending &&
    !comments.isPending &&
    !coverage.isPending &&
    !messages.isPending &&
    !results.isPending &&
    !who.isPending;
  // The last answer stands while the next one loads, because the key is the set of addresses
  // itself: one comment, message, ask or block arriving changes that set, and without this the
  // whole page would fall back to no names at all - every chip with its raw address - until the
  // new read lands. Merging the two answers by `ref` would say no more than this does: the new
  // read names every address the tree now holds, so the only names it lacks are the ones the
  // previous answer carries, which is what is kept here. A name that moved is a stale word for as
  // long as one read, where a dropped one is the whole page's names at once.
  const targets = useQuery({
    ...referenceTargetsQuery(answered ? refs : []),
    ...REFRESHED_BY_EVENTS,
    placeholderData: keepPreviousData,
  });
  const first = useMemo(
    () => (targets.data === undefined ? undefined : (targets.data.targets ?? [])),
    [targets.data]
  );
  const firstTree = useMemo(
    () => (first === undefined ? unnamed : buildIssueTree({ ...reads, targets: first })),
    [first, reads, unnamed]
  );
  // What the named tree holds that the first read was not given: a block's stored source comes
  // from that read's answer, so a whole document it cites is known only now (above). An empty
  // set is no request, and the names already in hand stand while a new non-empty set loads, as
  // above.
  const extra = useMemo(() => {
    if (first === undefined) return [];
    const asked = new Set(refs);
    return treeReferences(firstTree).filter((reference) => !asked.has(reference));
  }, [first, firstTree, refs]);
  const more = useQuery({
    ...referenceTargetsQuery(extra),
    ...REFRESHED_BY_EVENTS,
    placeholderData: keepPreviousData,
  });
  // Both answers, the second's standing for an address both name. Where nothing is left for the
  // second read to name, it has no answer: its query is disabled and would hold its last answer
  // as placeholder for good, over the first read's newer names for the same addresses.
  const named = useMemo(() => {
    const added = extra.length === 0 ? [] : (more.data?.targets ?? []);
    if (first === undefined || added.length === 0) return first;
    return [...new Map([...first, ...added].map((target) => [target.ref, target])).values()];
  }, [extra, first, more.data]);
  // The same answer every link of this page would otherwise read one pointer at a time, which a
  // link in a document, in a decision's card or in a comment's preview shows, and a row's
  // in-place chip too (`reference-names.tsx`, R20). Every one of those draws the name it is
  // given as it is, so the name published for a target is the name a chip of the tree draws
  // for it, by the one rule both read (`targetNames`): a block's drawn first line, a whole
  // document's word on the page, a question's or a comment's words with no markup. Beside the
  // names go the references the read answered ambiguous (`ambiguousTargets`), by that same
  // rule: a short reference two targets carry has no name, as a gone one has none, and a link
  // draws it as written and links nothing (decision 62) where it reads a gone one's own, which
  // the names alone would not tell it. A chip's name is read from the issue, the tree read and
  // the names alone (`treeContext`), so the map and the set are built from those and nothing
  // else: an answer of any other read - an ask, a comment, the coverage, the reader's login -
  // publishes nothing new, which would draw every link beside the tree again. Until the names
  // answer there is nothing to publish.
  const names = useMemo(
    () =>
      named === undefined
        ? NO_TARGET_NAMES
        : targetNames({ issue, targets: named, tree: read.answer }),
    [issue, named, read.answer]
  );
  const ambiguous = useMemo(
    () =>
      named === undefined
        ? NO_AMBIGUOUS_TARGETS
        : ambiguousTargets({ issue, targets: named, tree: read.answer }),
    [issue, named, read.answer]
  );
  usePublishReferenceNames(names, ambiguous);
  const built = useMemo(
    () =>
      named === undefined || named === first
        ? firstTree
        : buildIssueTree({ ...reads, targets: named }),
    [first, firstTree, named, reads]
  );
  const root = useMemo(() => (views === undefined ? built : { ...built, views }), [built, views]);
  return useMemo(
    () => ({
      blocksHeld: read.blocksHeld,
      blocksOf: read.blocksOf,
      blocksRead: read.blocksRead,
      root,
    }),
    [read.blocksHeld, read.blocksOf, read.blocksRead, root]
  );
}
