import { keepPreviousData, useQueries, useQuery } from "@tanstack/react-query";
import { useMemo } from "react";

import { api } from "../../api/client";
import { primarySpec, roleDocument } from "../../api/issue-cache";
import {
  artifactBlocksQuery,
  issueCoverageQuery,
  issueResultsQuery,
  referenceTargetsQuery,
  whoAmIQuery,
} from "../../api/queries";
import type { IssueDetails } from "../../api/types";
import { issueDecisionsQuery } from "../doc/spec-marks";
import { wordsOf } from "../refs/tree-fragment";
import {
  buildIssueTree,
  type DocumentBlocks,
  type IssueTreeReads,
  nodeWords,
  type TreeNode,
} from "./issue-tree";
import { usePublishReferenceNames } from "./reference-names";
import { citedReferences } from "./row-references";
import { blockWords } from "./tree-drafts";

/** The issue's tree, and how far the reads it is built from have got. */
export interface IssueTreeView {
  /** Whether the blocks of every document the tree reads (`treeDocumentIds`) have been read.
   *  Until then a typed block - the spec's summary among them - may be missing from `root`
   *  because it has not arrived rather than because its document holds none, which is what the
   *  head waits on before it says a spec has no summary (`IssueThreeThings.tsx`). A read that
   *  failed has not answered either: the page cannot say what a document it could not read
   *  holds. */
  readonly blocksRead: boolean;
  readonly root: TreeNode;
}

/**
 * The documents whose blocks the tree reads: the spec and the plan, Verification and decisions
 * documents, whose typed blocks are the ones the page draws (the root spec's R2), each the one
 * the issue read names by its id (`primarySpec`, `roleDocument`). That id is the server's own
 * answer to which document plays the part - the one in that role, or a role-less one under the
 * role's slug, which is how a row a rolling deploy left with no role still plays it
 * (`api/artifacts.go`'s `documentPlays`) - and the coverage and results reads find the
 * document by the same rule. The artifact's own `role` is no answer: the server omits it on
 * such a row, so a plan read by its role would leave that plan's tasks out of the tree while
 * the coverage counts them. A block read for every document of the issue was 25 of a first
 * load's 47 requests on the tester's built page (the tester's finding F17 on
 * dispatch://INFRA-1196), so no other document's blocks are read: four reads at most, and
 * fewer where one document plays two parts or the issue holds none in a part.
 */
function treeDocumentIds(issue: IssueDetails): string[] {
  const ids = new Set<string>();
  for (const document of [
    primarySpec(issue),
    roleDocument(issue, "plan"),
    roleDocument(issue, "verification"),
    roleDocument(issue, "decisions"),
  ]) {
    if (document !== undefined) ids.add(document.id);
  }
  return [...ids];
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

/**
 * The issue's tree, from the page's reads (dispatch://INFRA-1196 R20). This hook is where the
 * reads are; `buildIssueTree` is pure and holds none of them, so the model is testable without a
 * server and the page makes no request per node.
 *
 * The reads are the issue's own (handed in, because the page already has it), the coverage, the
 * decisions, the results, the issue's asks, and the blocks of the spec and of the plan,
 * Verification and decisions documents (`treeDocumentIds`) - ids, types and
 * attributes, never text, which is how a `serves` edge is held without reading a document the
 * reader has not opened (R20). Every one of them is a key another surface of this issue already
 * reads, so opening the Conversation or the Decisions panel after the tree finds them answered
 * rather than fetching again. The conversation's comments and messages are nodes of this tree
 * too, its turns, and the comments also say which decisions a person asked to change
 * (`tree-drafts.ts`'s `treeDrafts`, `decision-review.ts`). No document is a node of the tree
 * (R3), so nothing here reads a document's own text. The reader's login says which open
 * questions wait on the reader (`IssueTreeReads.viewer`).
 *
 * The names come last and all at once: the tree is built, every address it holds is read in one
 * batched request (`referenceTargetsQuery`; a tree holding more addresses than the server reads
 * at once costs one request per batch of them, which no ordinary issue reaches), and the tree is
 * built again with the names in it. A typed block that is no statement, check, ask or decision -
 * a plan task, a callout - has no words until that read answers, so what its words cite is in
 * no address the read was given: one more read names those addresses alone, made only where
 * such words cite one, and the tree is built with both answers. That is the last read of a
 * round: a node's words come from the names of node addresses, every one of which the first
 * read holds, so the second's answer changes no node's words. Until the names answer the tree
 * draws, with a chip naming its target's address and a block of a document carrying no words
 * yet; on every read after the first the names already in hand stand while the new set loads.
 */
export function useIssueTree(issue: IssueDetails): IssueTreeView {
  const key = issue.key;
  const coverage = useQuery(issueCoverageQuery(key));
  const decisions = useQuery(issueDecisionsQuery(key, true));
  const results = useQuery(issueResultsQuery(key));
  const asks = useQuery({
    queryKey: ["asks", key],
    queryFn: () => api.listIssueAsks(key, "all"),
  });
  // Under the keys the margin, the history and the focused row's facts read too, so a reader
  // opening the panel after the tree finds them answered rather than read a second time.
  const comments = useQuery({
    queryKey: ["comments", key],
    queryFn: () => api.listComments(key),
  });
  const messages = useQuery({
    queryKey: ["issue-messages", key],
    queryFn: () => api.listIssueMessages(key),
  });
  // The reader, whose own turn an open question may be (`IssueTreeReads.viewer`): the shell's
  // one read of who is signed in, so it costs no request of its own.
  const who = useQuery(whoAmIQuery());
  const viewer = who.data?.login;
  const documentIds = treeDocumentIds(issue);
  // `combine` is what keeps one value for every document's blocks: TanStack holds its answer and
  // replaces it only when a document's own read changes, where a map over the results would be a
  // new array every render and build the tree again with it. It answers whether any of them is
  // still waiting too, which is part of what holds the one batched name read back below, and
  // whether every one has answered with its blocks (`IssueTreeView.blocksRead`).
  const documents = useQueries({
    combine: (answers): { blocks: DocumentBlocks[]; pending: boolean; read: boolean } => ({
      blocks: answers.map((answer, index) => ({
        artifactId: documentIds[index] ?? "",
        blocks: answer.data ?? [],
      })),
      pending: answers.some((answer) => answer.isPending),
      read: answers.every((answer) => answer.isSuccess),
    }),
    queries: documentIds.map((id) => artifactBlocksQuery(id)),
  });
  // One value, so the model is given the reads and nothing else.
  const reads = useMemo<IssueTreeReads>(
    () => ({
      asks: asks.data,
      comments: comments.data,
      coverage: coverage.data,
      decisions: decisions.data,
      documents: documents.blocks,
      issue,
      messages: messages.data,
      results: results.data,
      viewer,
    }),
    [
      asks.data,
      comments.data,
      coverage.data,
      decisions.data,
      documents.blocks,
      issue,
      messages.data,
      results.data,
      viewer,
    ]
  );
  const unnamed = useMemo(() => buildIssueTree(reads), [reads]);
  const refs = useMemo(() => treeReferences(unnamed), [unnamed]);
  // One request for every name, not one per read that landed. Each of these reads adds nodes to
  // the tree as it arrives, so asking while any is still out would ask for the addresses known
  // so far and ask again for the rest: measured on this pod, that cost the first load two reads
  // and sometimes three, where the design is "the names come last and all at once" (above) and
  // the issue's own budget is 40 requests (dispatch://INFRA-1196 c-requests). The reader's login
  // is one of the reads this gate waits on: it adds no node, but the tree is built again when
  // it answers, since it says which questions wait on the reader. Every one of these reads is
  // always enabled, so each ends in an answer or an error and none of them holds the names for
  // good. Nothing waits on the names: until they land the tree draws with each chip naming its
  // target's address.
  const treeRead =
    !asks.isPending &&
    !comments.isPending &&
    !coverage.isPending &&
    !decisions.isPending &&
    !messages.isPending &&
    !results.isPending &&
    !who.isPending &&
    !documents.pending;
  // The last answer stands while the next one loads, because the key is the set of addresses
  // itself: one comment, message, ask or block arriving changes that set, and without this the
  // whole page would fall back to no names at all - every block row drawn with empty text and
  // every chip with its raw address - until the new read lands. Merging the two answers by
  // `ref` would say no more than this does: the new read names every address the tree now
  // holds, so the only names it lacks are the ones the previous answer carries, which is what
  // is kept here. A name that moved is a stale word for as long as one read, where a dropped
  // one is the whole page's words at once.
  const targets = useQuery({
    ...referenceTargetsQuery(treeRead ? refs : []),
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
  // What the named tree's words cite that the first read was not given: a typed block's words
  // come from that read's answer, so what they cite is known only now (above). An empty set is
  // no request, and the names already in hand stand while a new non-empty set loads, as above.
  const extra = useMemo(() => {
    if (first === undefined) return [];
    const asked = new Set(refs);
    return treeReferences(firstTree).filter((reference) => !asked.has(reference));
  }, [first, firstTree, refs]);
  const more = useQuery({
    ...referenceTargetsQuery(extra),
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
  // The same answer every link of this page would otherwise read one pointer at a time: a
  // block's own words, which a link in a document, in a decision's card or in a comment's
  // preview shows (`reference-names.tsx`, R20). Every one of those draws the name it is given
  // as it is, so what is published is a name's words and no markup: the server's reading of a
  // block where the read sends one (`ReferenceTarget.text_tree`), since the read's `name` for a
  // block is its stored excerpt, which opens on a typed block's directive line
  // (`:::plan_task{#t1 …}`), and that name with its directive left out where it sends none
  // (`blockWords`), which is every other target's name as it stands.
  const names = useMemo(
    () =>
      new Map(
        (named ?? [])
          .filter((target) => !target.gone && target.name !== null)
          .map((target) => [
            target.ref,
            target.text_tree === undefined
              ? blockWords(target.name ?? "")
              : wordsOf(target.text_tree),
          ])
      ),
    [named]
  );
  usePublishReferenceNames(names);
  const root = useMemo(
    () =>
      named === undefined || named === first
        ? firstTree
        : buildIssueTree({ ...reads, targets: named }),
    [first, firstTree, named, reads]
  );
  return useMemo(() => ({ blocksRead: documents.read, root }), [documents.read, root]);
}
