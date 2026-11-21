import { hashKey, queryOptions, useQueries, useQuery } from "@tanstack/react-query";
import { useMemo, useRef } from "react";

import { api } from "../../api/client";
import {
  issueCoverageQuery,
  issueResultsQuery,
  referenceTargetsQuery,
  whoAmIQuery,
} from "../../api/queries";
import type { IssueDetails, ReferenceTarget } from "../../api/types";
import { issueDecisionsQuery } from "../doc/spec-marks";
import { EVENT_FRESH } from "../refs/reference-target";
import {
  buildIssueTree,
  type IssueTreeReads,
  nodeWords,
  type TreeNode,
  type TreeViews,
  targetNaming,
} from "./issue-tree";
import { usePublishReferenceNames } from "./reference-names";
import { citedReferences } from "./row-references";
import {
  TREE_FILTERS,
  type TreeAnswer,
  type TreeRead,
  useTreeRead,
  viewQuery,
} from "./tree-read/tree-read";

/** The issue's tree, and how far the tree read it is built from has got (`TreeRead`): `root` is
 *  built from what that read holds, and keeps standing on it through a refetch that fails. */
export interface IssueTreeView extends Pick<TreeRead, "blocksHeld" | "blocksOf" | "blocksRead"> {
  /** The names line's state while the names stand failed (`useNamesFailures`), and none while
   *  they do not: what a view reads to draw the page's line that the names failed. The names
   *  reads are the batched one and the one more that names what a block's stored source cites
   *  (`useIssueTree`); the names stand failed from a failure of one that holds no answer under
   *  its set of addresses until every one of them asked for holds an answer under its set. The
   *  names in hand still stand meanwhile - the tree and what the page publishes are built from
   *  the last answer each read gave - so a view keeps every row's words and every chip's name
   *  beside that line. A first read that fails holds none; a read under a new set of addresses
   *  that fails stands failed while the tree keeps the last names. The line stays while a read
   *  that failed runs again; while another read the tree is built from runs again after it
   *  failed, which holds the batched read back - it keeps the set it was last given meanwhile
   *  (`useIssueTree`); while a names read is out under a new set of addresses, whichever read
   *  failed - that other read answered with words that cite an address the tree did not hold,
   *  say, or a comment brought the address the one more read failed on into the batched read's
   *  set; and while one read answers and another does not. It says a failure again only for a
   *  new one (`NamesFailure.failure`). A refresh that fails over an answer a read holds counts
   *  nothing (`standsFailed`). */
  readonly namesFailure: NamesFailure | undefined;
  /** Whether the names the tree is built from are held: the batched names read has answered
   *  (`useIssueTree`). That read is always made: the root's words write the issue's own key
   *  (`issue-tree.ts`'s `assemble`), and a key written bare is an address the read names
   *  (`row-references.ts`'s `citedReferences`). It names what a chip points at outside the tree
   *  read's answer and gives a block its stored source (`TreeContext.excerptOf`), which a view
   *  that reads the names waits on. The one more read is no part of this: it names only the
   *  whole documents a block's stored source cites that the batched read was not given, which
   *  is no node's own address. The batched read's last answer stands while its read of a new set
   *  of addresses is out or has failed (`namesFailure`), so the names stay held then; where its
   *  very first read fails it holds none, and this is false until it answers. */
  readonly namesHeld: boolean;
  readonly root: TreeNode;
}

/** The names line's state while the tree's names stand failed (`IssueTreeView.namesFailure`). */
export interface NamesFailure {
  /** How many times the names reads have failed since every one of them asked for last held an
   *  answer under its set of addresses (`useNamesFailures`): one for each failure of a read that
   *  holds no answer under its set, as TanStack Query's `errorUpdateCount` rises under that set.
   *  A read moved to another set adds none of the failures that set's query counted before, and
   *  one read answering while another does not takes none away, so the count changes only with
   *  a new failure, and the line draws its alert again only then. It is no read's own
   *  `errorUpdateCount`. */
  readonly failure: number;
  /** Asks again each names read that holds no answer under its set of addresses (its own
   *  `refetch`), and no other read. */
  readonly retry: () => void;
  /** Whether one of those reads runs, again or under a new set of addresses (TanStack Query's
   *  `isFetching`). */
  readonly retrying: boolean;
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

/** What the one more names read holds where nothing is left for it to name: no names, and the
 *  same empty answer on every render, so the answer it gave before stands over nothing. */
const NO_MORE_TARGETS: { readonly targets: readonly ReferenceTarget[] } = { targets: [] };

/** The set of addresses the batched names read is given until the gate first opens: none, and
 *  the same empty set on every render, so the read's options are made once meanwhile. */
const NO_REFS: readonly string[] = [];

/**
 * The answer a names read gave last: its own while it holds one, and the last one it held while
 * its read under a new set of addresses is out or has failed. TanStack's `placeholderData` would
 * hold that answer only while the read is out: a read under a new key that fails holds no data,
 * where a refetch that fails under the same key keeps its own. The ref is written during render
 * only with the answer the read holds in that render, so it never stands over a newer one: a read
 * that holds an answer is drawn from it.
 */
function useLastAnswer<T>(answer: T | undefined): T | undefined {
  const last = useRef(answer);
  if (answer !== undefined) last.current = answer;
  return last.current;
}

/** One names read as `useNamesFailures` counts it, in one render. */
interface NamesRead {
  /** How many times its query has failed (TanStack Query's `errorUpdateCount`). */
  readonly errorUpdateCount: number;
  /** Its key, hashed (`hashKey`): the set of addresses it is asked for now. */
  readonly hash: string;
  /** Whether it holds an answer under that set. */
  readonly held: boolean;
}

/**
 * How many times the names reads have failed since every one of them asked for last held an
 * answer under its set of addresses: one count for the names line, which stands while it is
 * above 0 (`IssueTreeView.namesFailure`). Each new failure of a read that holds no answer under
 * its set counts one - its query's `errorUpdateCount` rising under the same key - and the count
 * goes back to 0 only once every read holds an answer under its set. A names read's key is the
 * set of addresses it names (`queryKey`), so a read under a new set is a query of its own, which
 * may have failed before the last answer: a read moved to another set counts none of the
 * failures that set's query counted before, and the count stays as it was while that read is
 * out. So the line stays, with the same alert (`FailedReadLine`'s `failure`), while a read under
 * a new set is out after a failure, whichever read failed, and while one read answers and
 * another does not, and says a failure again only for a new one. A failure of a read that holds
 * an answer - a refresh that failed - counts nothing, as `standsFailed` counts none. A read
 * first seen as the tree's slot is mounted, which has no set before, counts the failures its
 * query holds where it holds no answer, as `standsFailed` reads them. `reads` are the same reads
 * in the same order on every render. The ref is written during render, as `useLastAnswer`'s is,
 * with the reads' state in that render, and a second render of the same state counts nothing
 * more.
 */
function useNamesFailures(reads: readonly NamesRead[]): number {
  const last = useRef<{ failures: number; seen: readonly NamesRead[] } | undefined>(undefined);
  const before = last.current;
  let failures = 0;
  if (reads.some((read) => !read.held)) {
    failures = before?.failures ?? 0;
    for (const [index, read] of reads.entries()) {
      if (read.held) continue;
      const seen = before?.seen[index];
      if (seen === undefined) failures += read.errorUpdateCount;
      else if (seen.hash === read.hash) {
        failures += Math.max(0, read.errorUpdateCount - seen.errorUpdateCount);
      }
    }
  }
  last.current = { failures, seen: reads };
  return failures;
}

/** The issue's asks as the tree reads them: every ask, open or closed (`listIssueAsks` with
 *  `all`), under the key the margin, a document's ask blocks and the feed's ask rules read and
 *  refresh (`["asks", key]`). One definition, so a view that observes the same read takes the
 *  key from here and cannot drift from it; such a view, like the tree, adds how it is kept fresh
 *  (`EVENT_FRESH`). */
export const issueAsksQuery = (key: string) =>
  queryOptions({
    queryKey: ["asks", key],
    queryFn: () => api.listIssueAsks(key, "all"),
  });

/** The issue's comments as the tree reads them (`listComments`), under the key the feed's rules
 *  refresh (`["comments", key]`); a view of the same read takes its key from here. */
export const issueCommentsQuery = (key: string) =>
  queryOptions({
    queryKey: ["comments", key],
    queryFn: () => api.listComments(key),
  });

/** The issue's messages as the tree reads them (`listIssueMessages`), under the key the feed's
 *  rules refresh (`["issue-messages", key]`); a view of the same read takes its key from here. */
export const issueMessagesQuery = (key: string) =>
  queryOptions({
    queryKey: ["issue-messages", key],
    queryFn: () => api.listIssueMessages(key),
  });

/** The reads a tree is built from (`IssueTreeReads`), each compared by identity. */
const TREE_INPUTS = [
  "asks",
  "comments",
  "coverage",
  "decisions",
  "issue",
  "results",
  "targets",
  "viewer",
] as const satisfies readonly (keyof IssueTreeReads)[];

/** How many trees are kept for one answer of the tree read: the tree before the names answer,
 *  with the first read's names and with both reads' names, and one more. */
const TREES_PER_ANSWER = 4;

/** The trees built for each answer of the tree read, newest last, each with its reads. */
const builtTrees = new WeakMap<
  TreeAnswer,
  readonly { readonly reads: IssueTreeReads; readonly root: TreeNode }[]
>();

/**
 * `buildIssueTree` of `reads`, built once for one set of reads: a tree already built from the
 * same answer of the tree read and the same other reads, each the same object, is that tree.
 * Every read keeps its answer's object until it answers anew, so a slot the stage mounts again
 * on the same reads - the outline drawn again as a view is pressed off - builds nothing, and a
 * read that changed builds a tree of its own. The trees are held by the answer, so they go with
 * it.
 */
function builtTree(reads: IssueTreeReads): TreeNode {
  const answer = reads.tree;
  if (answer === undefined) return buildIssueTree(reads);
  const held = builtTrees.get(answer) ?? [];
  const found = held.find((one) => TREE_INPUTS.every((input) => one.reads[input] === reads[input]));
  if (found !== undefined) return found.root;
  const root = buildIssueTree(reads);
  builtTrees.set(answer, [...held.slice(1 - TREES_PER_ANSWER), { reads, root }]);
  return root;
}

/**
 * The issue's tree, from the page's reads (root R20). This hook is where the reads are;
 * `buildIssueTree` is pure and holds none of them, so the model is testable without a server and
 * the page makes no request per node.
 *
 * The tree is the issue's tree read (`useTreeRead`), every page of it, which places, orders and
 * words every node; no document's blocks are read. Beside it stand the reads that tell what the
 * answer does not, each under the key another surface of this issue reads too, so opening
 * Details, the history or the Contents pane after the tree finds them answered rather than
 * fetching again: the coverage, the decisions, the results, the issue's asks, its comments -
 * which also say which decision of the decisions read a person asked to change
 * (`decision-review.ts`) - its messages, and the reader's login, which says which open questions
 * wait on the reader (`IssueTreeReads.viewer`).
 *
 * Each filter's view read (`viewQuery`) is watched here and never sent: `ViewReadProvider`
 * (`tree-read/view-read.tsx`) reads a view the first time its filter is on, and whatever it holds,
 * with the decisions read, is set on the issue's node (`TreeNode.views`), which `presetView` and
 * `filterSignals` draw a filter from (R4). The root keeps its identity while neither the tree nor
 * a held view changes.
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
 * after the first the names already in hand stand while the new set loads, and after its read
 * fails (`IssueTreeView.namesFailure`).
 */
export function useIssueTree(issue: IssueDetails): IssueTreeView {
  const key = issue.key;
  const read = useTreeRead(issue);
  // Every read here takes `EVENT_FRESH`: events refresh it, and a slot mounted again reads
  // nothing.
  // The page's stage mounts the tree's slot again on every press of a view, so under the app's
  // 30 s `staleTime` (`main.tsx`) a press more than 30 s after the load would read each one
  // again, though nothing it holds had changed.
  //
  // Of the feed's rules (`api/event-refresh.ts`), these refresh the reads the tree is built from:
  // `["comments", key]` on a comment event and on a document's upload or new version;
  // `["asks", key]` on an ask event, a reply on an ask, a review of a document and a block
  // repaired or found invalid; `["decisions", key]` on an ask event, a Keep and a document's new
  // version; `["issue-messages", key]` on a message event; `["issue", key, "coverage"]` on a
  // document's upload or new version; `["issue", key, "results"]` on those too, on a result
  // recorded or retracted and on a pull request moved to another issue; and a names read
  // (`["reference-targets", …]`) on an event that changes the words or the name under an address
  // its key holds, on the issue or project document that address is of: a comment's edit, an
  // ask's edit, a document's creation, new version, rename or removal, and an issue's write that
  // changes its title (`referenceNameTargets`). A names read's key is the set of addresses it
  // names, so a tree that comes to hold another set reads again under a key of its own, and only
  // a change under an address the tree already names needs a rule. No rule names `["whoami"]`,
  // which the tree reads with `EVENT_FRESH` too: the reader's login does not change while a page
  // is open. The tree read and its views keep the same rule but for a focus, which reads them
  // again only while the live feed is down (`TREE_FRESH`), since their events already reach them
  // while it is up.
  const coverage = useQuery({ ...issueCoverageQuery(key), ...EVENT_FRESH });
  const decisions = useQuery({ ...issueDecisionsQuery(key, true), ...EVENT_FRESH });
  const results = useQuery({ ...issueResultsQuery(key), ...EVENT_FRESH });
  const asks = useQuery({ ...issueAsksQuery(key), ...EVENT_FRESH });
  // Under the keys the margin, the history and the focused row's facts read too, so a reader
  // opening the panel after the tree finds them answered rather than read a second time.
  const comments = useQuery({ ...issueCommentsQuery(key), ...EVENT_FRESH });
  // Under the key the history and the margin read too, and one of the reads the names wait on
  // (`answered`); the model reads no message from it, since the tree read answers each as a node.
  const messages = useQuery({ ...issueMessagesQuery(key), ...EVENT_FRESH });
  // The reader, whose own turn an open question may be (`IssueTreeReads.viewer`): the shell's
  // one read of who is signed in, so it costs no request of its own.
  const who = useQuery({ ...whoAmIQuery(), ...EVENT_FRESH });
  const viewer = who.data?.login;
  // Each filter's view read, watched by an observer that is never enabled, so it sends nothing
  // and holds whatever `ViewReadProvider` read for the load; the answers in `TREE_FILTERS`' order.
  const [specView, verificationView, planView] = useQueries({
    queries: TREE_FILTERS.map((filter) => ({ ...viewQuery(key, filter), enabled: false })),
  });
  const spec = specView?.data;
  const verification = verificationView?.data;
  const plan = planView?.data;
  const views = useMemo((): TreeViews | undefined => {
    const held: TreeViews = {
      ...(decisions.data === undefined ? {} : { decisions: decisions.data }),
      ...(plan === undefined ? {} : { plan }),
      ...(spec === undefined ? {} : { spec }),
      ...(verification === undefined ? {} : { verification }),
    };
    return Object.keys(held).length === 0 ? undefined : held;
  }, [decisions.data, plan, spec, verification]);
  // One value, so the model is given the reads and nothing else.
  const reads = useMemo<IssueTreeReads>(
    () => ({
      asks: asks.data,
      comments: comments.data,
      coverage: coverage.data,
      decisions: decisions.data,
      issue,
      results: results.data,
      tree: read.answer,
      viewer,
    }),
    [
      asks.data,
      comments.data,
      coverage.data,
      decisions.data,
      issue,
      read.answer,
      results.data,
      viewer,
    ]
  );
  const unnamed = useMemo(() => builtTree(reads), [reads]);
  const refs = useMemo(() => treeReferences(unnamed), [unnamed]);
  // One request for every name, not one per read that landed. Each of these reads adds to the
  // tree as it arrives - the tree read every node, the decisions read a decision no node holds,
  // the asks an answer, the comments a Change moved under its decision - so asking while any is
  // still out would ask for the addresses known so far and ask again for the rest: measured on
  // this pod, that cost the first load two reads and sometimes three, where the design is "the
  // names come last and all at once" (above) and the first load's budget is 40 requests. The
  // reader's login is one of the reads this gate waits on: it adds no node, but the tree is
  // built again when it answers, since it says which questions wait on the reader. Every one of
  // these reads is always enabled, so each ends in an answer or an error and none of them holds
  // the names for good. Nothing waits on the names: until they land the tree draws with each chip
  // naming its target's address.
  const answered =
    !read.pending &&
    !asks.isPending &&
    !comments.isPending &&
    !coverage.isPending &&
    !decisions.isPending &&
    !messages.isPending &&
    !results.isPending &&
    !who.isPending;
  // The last answer stands while the next one loads and after it fails, because the key is the
  // set of addresses itself: one comment, message, ask or block arriving changes that set, and
  // without this the whole page would fall back to no names at all - every chip with its raw
  // address - until the new read lands, and for as long as it keeps failing (`useLastAnswer`).
  // Merging the two answers by `ref` would say no more than this does: the new read names every
  // address the tree now holds, so the only names it lacks are the ones the previous answer
  // carries, which is what is kept here. A name that moved is a stale word until a read under the
  // new key succeeds, where a dropped one is the whole page's names at once; while that read
  // stands failed - until its Retry, window focus or an event reads it again (`EVENT_FRESH`) -
  // the view says so (`IssueTreeView.namesFailure`), and goes on saying so while a read under a
  // new set is out, until every names read holds an answer under its set (`useNamesFailures`).
  //
  // The gate shuts again while a read that holds no answer runs again - a failed read's Retry,
  // window focus or an event's refresh of it, which TanStack puts back in `pending` - and the
  // names read keeps the set it was last given until the gate opens, by the same `useLastAnswer`,
  // which holds the last value given other than undefined. Moved to the set of none, which is no
  // read, the line's Retry would ask the read of that empty set, which names nothing, and the
  // set it failed under would be read again each time the gate opened on it once more. Kept, the
  // line says that read's state while another read runs: whether it runs again, and a Retry that
  // asks it again. Where the read that ran answers with words that cite an address the tree did
  // not hold, the gate opens on a new set, and the line stays while the read under it is out,
  // with the same alert and the focus its Retry holds (`useNamesFailures`). Until the gate first
  // opens the set is none.
  const askedFor = useLastAnswer(answered ? refs : undefined) ?? NO_REFS;
  const targetsQuery = useMemo(() => referenceTargetsQuery(askedFor), [askedFor]);
  const targetsHash = useMemo(() => hashKey(targetsQuery.queryKey), [targetsQuery]);
  const targets = useQuery({ ...targetsQuery, ...EVENT_FRESH });
  const firstAnswer = useLastAnswer(targets.data);
  const first = useMemo(
    () => (firstAnswer === undefined ? undefined : (firstAnswer.targets ?? [])),
    [firstAnswer]
  );
  const firstTree = useMemo(
    () => (first === undefined ? unnamed : builtTree({ ...reads, targets: first })),
    [first, reads, unnamed]
  );
  // What the named tree holds that the first read was not given: a block's stored source comes
  // from that read's answer, so a whole document it cites is known only now (above). An empty
  // set is no request; the names already in hand stand while a new non-empty set loads and after
  // its read fails, and the line stays while a read under a new set is out, as above
  // (`useNamesFailures`).
  //
  // An address leaves this set only once the first read is asked for it (`askedFor`), not once
  // the tree holds it some other way (`refs`): while the gate is shut, a comment that cites it
  // brings it into the tree's own set and not into the first read's, which keeps the set it was
  // given until the gate opens, and an address measured against the tree's set left the one
  // more read with no read asked for it - every read then held an answer, and the line left the
  // page with the focus its Retry held. An address the tree holds some other way comes into this
  // set only as before, where it was in it already: the first read names it once the gate opens,
  // and no second read is asked for it meanwhile. The set this held last (`kept`) is written
  // during render, as `useLastAnswer`'s ref is, with the set of that render.
  const kept = useRef<readonly string[]>([]);
  const extra = useMemo(() => {
    if (first === undefined) return [];
    const asked = new Set(askedFor);
    const held = new Set(refs);
    const before = new Set(kept.current);
    return treeReferences(firstTree).filter(
      (reference) => !asked.has(reference) && (!held.has(reference) || before.has(reference))
    );
  }, [askedFor, first, firstTree, refs]);
  kept.current = extra;
  const moreQuery = useMemo(() => referenceTargetsQuery(extra), [extra]);
  const moreHash = useMemo(() => hashKey(moreQuery.queryKey), [moreQuery]);
  const more = useQuery({ ...moreQuery, ...EVENT_FRESH });
  // Both answers, the second's standing for an address both name that it is asked for now
  // (`extra`), and the first's for every other: an answer the second gave for an older set,
  // kept while its new set loads or after that read fails, may name an address the first read
  // has taken over since, whose name it holds from before. Where nothing is left for the second
  // read to name, the answer it gave before is dropped: its query is disabled and never answers
  // again, so that answer would have the page publish for good a name for an address no read
  // asks for any more. A link beside the tree draws a published name in place of the one it
  // reads of its own (`useKnownReferenceName`), and the feed refreshes a names read only for an
  // address its key holds (`referenceNameTargets`), so no event would ever move that name.
  const moreHeld = extra.length === 0 ? NO_MORE_TARGETS : more.data;
  const moreAnswer = useLastAnswer(moreHeld);
  const named = useMemo(() => {
    const added = moreAnswer?.targets ?? [];
    if (first === undefined || added.length === 0) return first;
    const asked = new Set(extra);
    const ordered = [
      ...added.filter((target) => !asked.has(target.ref)),
      ...first,
      ...added.filter((target) => asked.has(target.ref)),
    ];
    return [...new Map(ordered.map((target) => [target.ref, target])).values()];
  }, [extra, first, moreAnswer]);
  // Whether the names the tree is built from are held (`IssueTreeView.namesHeld`): the first
  // read holds an answer. It is always made, since the root's words cite the issue's own key.
  // The one more read is no part of it: it names only the whole documents a block's stored
  // source cites, and gives no node its words.
  const namesHeld = first !== undefined;
  // The names stand failed, the names in hand standing through it (above), from a failure of a
  // names read that holds no answer under its set until every one of them asked for holds one,
  // by one count of their failures (`useNamesFailures`), so the line says a failure again only
  // for a new one: neither a read under a new set while it is out nor one read answering while
  // another does not changes it. The second read holds an answer where nothing is left for it to
  // name (`NO_MORE_TARGETS`): its query is disabled then and would never run again to answer, so
  // a line that waited on it would stay for good. Where a comment cites the address the second
  // read failed on, that read goes on naming it until the first read is asked for it (`extra`),
  // which it is not while the gate is shut, so the line stays on the second read's failure
  // meanwhile; once the first read is asked for the address, the line stays on the first read's
  // read of that set, until that read answers. The Retry asks again each names read that holds
  // no answer under its set, and the line is busy while one of those runs.
  const targetsAnswered = targets.data !== undefined;
  const moreAnswered = moreHeld !== undefined;
  const failure = useNamesFailures([
    { errorUpdateCount: targets.errorUpdateCount, hash: targetsHash, held: targetsAnswered },
    { errorUpdateCount: more.errorUpdateCount, hash: moreHash, held: moreAnswered },
  ]);
  const retrying = (!targetsAnswered && targets.isFetching) || (!moreAnswered && more.isFetching);
  const { refetch: refetchTargets } = targets;
  const { refetch: refetchMore } = more;
  const namesFailure = useMemo(
    (): NamesFailure | undefined =>
      failure > 0
        ? {
            failure,
            retry: () => {
              if (!targetsAnswered) void refetchTargets();
              if (!moreAnswered) void refetchMore();
            },
            retrying,
          }
        : undefined,
    [failure, moreAnswered, refetchMore, refetchTargets, retrying, targetsAnswered]
  );
  // The same answer every link of this page would otherwise read one pointer at a time, which a
  // link in a document, in a decision's card or in a comment's preview shows, and a row's
  // in-place chip too (`reference-names.tsx`, R20). Every one of those draws the name it is
  // given as it is, so the name published for a target is the name a chip of the tree draws
  // for it, by the one rule both read (`targetNaming`): a block's drawn first line, a whole
  // document's word on the page, a question's or a comment's words with no markup. Beside the
  // names go the references the read answered ambiguous, read with them by that same rule: a
  // short reference two targets carry has no name, as a gone one has none, and a link draws it
  // as written and links nothing where it reads a gone one's own, which the names alone would
  // not tell it. A chip's name is read from the issue, the tree read and the names alone
  // (`treeContext`), so the map and the set are built from those and nothing else: an answer of
  // any other read - an ask, a comment, the coverage, the reader's login - publishes nothing
  // new, which would draw every link beside the tree again. Until the names answer there is
  // nothing to publish.
  const { ambiguous, names } = useMemo(
    () =>
      named === undefined
        ? { ambiguous: NO_AMBIGUOUS_TARGETS, names: NO_TARGET_NAMES }
        : targetNaming({ issue, targets: named, tree: read.answer }),
    [issue, named, read.answer]
  );
  usePublishReferenceNames(names, ambiguous);
  const built = useMemo(
    () =>
      named === undefined || named === first ? firstTree : builtTree({ ...reads, targets: named }),
    [first, firstTree, named, reads]
  );
  const root = useMemo(() => (views === undefined ? built : { ...built, views }), [built, views]);
  return useMemo(
    () => ({
      blocksHeld: read.blocksHeld,
      blocksOf: read.blocksOf,
      blocksRead: read.blocksRead,
      namesFailure,
      namesHeld,
      root,
    }),
    [read.blocksHeld, read.blocksOf, read.blocksRead, namesFailure, namesHeld, root]
  );
}
