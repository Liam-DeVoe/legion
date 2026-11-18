import { isIdPrefix, itemFromSearch } from "@legion/contracts";
import {
  type QueryFilters,
  type QueryKey,
  useIsFetching,
  useQueryClient,
} from "@tanstack/react-query";
import { useMemo } from "react";

import type { Ask, Comment, IssueDetails, MessageRead } from "../../api/types";
import { type ItemLanding, type ItemRoute, useItemLanding } from "../issue/useItemLanding";
import { askQuery, commentQuery, messageQuery, referencesQuery } from "../refs/reference-target";
import { type IssueRoute, parseIssuePath } from "../refs/routes";
import { prefixReference } from "../refs/text-references";
import {
  type AddressTarget,
  agentsOnlyItem,
  type IssueAddress,
  pathToTarget,
} from "./issue-address";
import type { TreeNode } from "./issue-tree";

/** The node an address names, read against the tree (`useAddressTarget`). Each field is one
 *  value for as long as the address and the tree say the same; the object holding them is new
 *  on every render. */
export interface AddressedNode {
  /** The id of the node the address names, or nothing: what a view keeps whatever its filter
   *  or its words match (`presets.ts`'s `presetView`). */
  readonly named: ReadonlySet<string>;
  /** The chain down to the node the address names, the issue first, which the tree unfolds and
   *  puts the focus on the last node of; undefined where the address names no node or the
   *  tree holds none it names. */
  readonly reveal: readonly TreeNode[] | undefined;
  /** The node the address names, as the address says it. */
  readonly target: AddressTarget | undefined;
}

/**
 * The node `address` names, resolved against the whole tree `root` - the page's and every
 * filter's - so a view keeps it even where its filter or its words would not have matched it.
 * Every target an address names, `/children` among them, is one a tree unfolds and focuses.
 *
 * The target is read by what it says rather than by the value `issueAddress` built, which is a
 * new one on every render: the tree is walked again only when the address or the tree changes.
 */
export function useAddressTarget(
  root: TreeNode,
  address: Pick<IssueAddress, "target">
): AddressedNode {
  const targetKind = address.target?.kind;
  const targetId = address.target?.id;
  // Which document a block target belongs to, where its address names one: a block id alone
  // names a block of any document of the issue (`pathToTarget`).
  const targetDocument = address.target?.kind === "block" ? address.target.document : undefined;
  const target = useMemo<AddressTarget | undefined>(() => {
    if (targetKind === undefined || targetId === undefined) return undefined;
    if (targetKind === "block") return { document: targetDocument, id: targetId, kind: "block" };
    return { id: targetId, kind: targetKind } as AddressTarget;
  }, [targetDocument, targetId, targetKind]);
  const reveal = useMemo(() => pathToTarget(root, target), [root, target]);
  const named = useMemo(() => {
    const node = reveal?.at(-1);
    return new Set(node === undefined ? [] : [node.id]);
  }, [reveal]);
  return { named, reveal, target };
}

/**
 * The ask, comment or message `address` names as a node of the outline, by its route, which is
 * how the old page's landing reads an item (`issue/useItemLanding.ts`): an item's own route
 * (`/asks/<id>`, `/comments/<id>`, `/messages/<id>`), or the item a preset's address names
 * beside it (`?ask=`, `?comment=`) under the issue's key. Undefined for every other address,
 * and for an item the address names beside the document it opens (`documentItem`: `?mark=`, or
 * an item at a pinned version), whose page is that document's and not the outline.
 */
export function outlineItemRoute(
  route: IssueRoute,
  search: string,
  address: Pick<IssueAddress, "documentItem" | "target">
): ItemRoute | undefined {
  if (address.target?.kind !== "item" || address.documentItem !== undefined) return undefined;
  if (route.kind === "ask" || route.kind === "comment" || route.kind === "message") return route;
  const item = itemFromSearch(search);
  return item === null || item === undefined
    ? undefined
    : { id: item.id, key: route.key, kind: item.kind };
}

/**
 * Where this page goes for the old page's landing `to` (`useItemLanding`'s `redirect` and
 * `rewrite`): `to` itself, unless it is an address of the issue page naming the item beside the
 * document it is the address of - `?ask=` or `?comment=`, as `documentItemPath` writes it -
 * which this page reads as the item's node of the outline again (`issueAddress`): the node the
 * tree does not hold, so the landing sent the reader there again and again. That one is the
 * document's address with the item marked (`?mark=`), the address "View in document" builds,
 * which opens the document with the item beside it (`documentItem`) and asks for no node.
 */
export function landingPath(to: string): string {
  const url = new URL(to, "http://dispatch.invalid");
  const item = itemFromSearch(url.search);
  if (item === null || item === undefined || parseIssuePath(url.pathname) === undefined) return to;
  url.searchParams.delete(item.kind);
  url.searchParams.set("mark", item.id);
  return `${url.pathname}${url.search}${url.hash}`;
}

/**
 * The chain from the issue down to the nearest node the tree `root` holds above `id`, a turn of
 * a thread only agents wrote in (`agentsOnlyItem`), which the outline draws no node for: the
 * entry the turn was written under - the comment its write answered (`answers`, else the head of
 * its thread, `reply_to`), the ask it replies under (`ask_id`), the message it answers
 * (`in_reply_to`) - where the tree holds it, else that entry's own, as `tree-drafts.ts`'s
 * `addTurns` places each turn. Where the tree holds none of them - the head of such a thread
 * sits under the issue, and an approval request is no node (`addAsks`) - it is the issue alone.
 */
export function agentsOnlyAncestor(
  root: TreeNode,
  id: string,
  comments: readonly Pick<Comment, "answers" | "ask_id" | "id" | "reply_to">[] | undefined,
  messages: readonly MessageRead[] | undefined
): readonly TreeNode[] {
  const under = new Map<string, string | null>();
  for (const comment of comments ?? []) {
    under.set(comment.id, comment.answers ?? comment.reply_to ?? comment.ask_id);
  }
  for (const thread of messages ?? []) {
    for (const message of [thread.message, ...(thread.replies ?? [])]) {
      under.set(message.id, message.in_reply_to);
    }
  }
  // Each entry is walked once, so reads whose entries answer each other end at the issue.
  const walked = new Set([id]);
  for (let above = under.get(id); typeof above === "string"; above = under.get(above)) {
    if (walked.has(above)) break;
    walked.add(above);
    const chain = pathToTarget(root, { id: above, kind: "item" });
    if (chain !== undefined) return chain;
  }
  return [root];
}

/** Where an address naming a node lands while the tree holds no node of it, once every read
 *  the tree is built from has answered (`useAddressFallback`). */
export type AddressFallback =
  /**
   * A turn of a thread only agents wrote in, which the outline leaves out on purpose
   * (`agents-only.ts`): `ancestor` is the chain down to the turn's nearest node the tree holds
   * (`agentsOnlyAncestor`), the issue alone where it holds none, which the outline lands on and
   * says at its row that the thread is the agents' alone.
   *
   * It is a kind of its own, never `missing`: the turn is there, in the panel's history. Opening
   * Details at that history with the turn `id` marked is the panel's to do, and the part that
   * builds it takes this kind over from the outline's landing on the ancestor.
   */
  | {
      readonly ancestor: readonly TreeNode[];
      readonly id: string;
      readonly kind: "agents-only";
    }
  /**
   * The item as the old page's landing reads it (`issue/useItemLanding.ts`), once that read has
   * answered: an item of another issue, or one that stands beside a document (`redirect`); an id
   * written short, which names one item (`rewrite`) or several (`ambiguous`); or none this issue
   * holds (`missing`), or a read that failed (`unavailable`). `route` is the item's route, which
   * the match list and the failure name the item by. `retrying` is whether the read the landing
   * answers from runs again, as a Retry of a failed one asks, and `failure` how many times that
   * read has failed (its `errorUpdateCount`), which tells one failure's line from the next.
   */
  | {
      readonly failure: number;
      readonly kind: "landing";
      readonly landing: Exclude<ItemLanding, { kind: "pending" | "turn" }>;
      readonly retrying: boolean;
      readonly route: ItemRoute;
    }
  /**
   * A block or the issue's children, named by an address that opens no document - a `#b-`
   * fragment on the issue's own address or on a view's, and `/children` - where the tree holds
   * no node of it: a block id no node carries, or an issue with no children. No read is left
   * that would draw it, so the page says in the outline's place that the link names nothing on
   * it, rather than leave the reader on the page's body.
   */
  | {
      readonly kind: "absent";
      readonly target: Extract<AddressTarget, { kind: "block" | "children" }>;
    };

/** The reads of the issue's conversation the tree's turns come from, as the page holds them:
 *  each undefined until it has answered. */
export interface ConversationReads {
  readonly asks: readonly Pick<Ask, "id" | "kind">[] | undefined;
  readonly comments:
    | readonly Pick<Comment, "answers" | "ask_id" | "author" | "id" | "reply_to">[]
    | undefined;
  readonly messages: readonly MessageRead[] | undefined;
}

/** What `useIsFetching` counts where the landing reads nothing: no query. */
const NO_READ: QueryFilters = { predicate: () => false };

/**
 * The key of the one read the old page's landing answers `route` from (`useItemLanding`), and
 * which its `unavailable` landing's `retry` asks again: the graph read that resolves an id
 * written short (`useWholeRouteRead`), else the item's own read under its whole id.
 */
function landingReadKey(route: ItemRoute): QueryKey {
  if (isIdPrefix(route.id)) return referencesQuery(prefixReference(route)).queryKey;
  if (route.kind === "ask") return askQuery(route.id).queryKey;
  if (route.kind === "comment") return commentQuery(route.id).queryKey;
  return messageQuery(route.key, route.id).queryKey;
}

/**
 * Where `address` lands where it names a node the tree holds none of (`reveal` undefined), once
 * every read the tree is built from has answered (`readsHeld`): before that the node may not
 * have arrived yet. Undefined while the tree holds the node, while the reads are out, for an
 * address naming no node or opening a document, and while the old page's landing is still
 * reading the item (`pending`) for the first time.
 *
 * An ask, a comment or a message of the outline (`outlineItemRoute`) is told from the
 * conversation's reads where it is a turn of a thread only agents wrote in, as the tree leaves
 * it out (`agentsOnlyItem`, with the asks the tree draws its nodes from: the issue's open asks
 * and the asks read), and lands on its nearest drawn ancestor (`agents-only`). Every other item
 * is read as the old page reads one (`useItemLanding`), and nothing is read for an item the tree
 * holds. Its `turn` - an item of this issue beside no document, whose page is the outline -
 * draws the outline as it stands, which lands on the node once a read draws it. A read that
 * failed is pending again while it runs again, since it holds no answer, and stays the failure
 * it was until that run ends, so its line stays drawn, busy, with its Retry and the focus. A
 * block or the children is `absent`, as no read is left to draw it.
 */
export function useAddressFallback({
  address,
  issue,
  reads,
  readsHeld,
  reveal,
  root,
  route,
  search,
}: {
  readonly address: Pick<IssueAddress, "document" | "documentItem" | "target">;
  readonly issue: IssueDetails;
  readonly reads: ConversationReads;
  readonly readsHeld: boolean;
  readonly reveal: readonly TreeNode[] | undefined;
  /** The whole tree, which the address's node was looked for in. */
  readonly root: TreeNode;
  readonly route: IssueRoute;
  readonly search: string;
}): AddressFallback | undefined {
  const item = outlineItemRoute(route, search, address);
  const unheld = readsHeld && reveal === undefined ? item : undefined;
  const unheldId = unheld?.id;
  const { asks, comments, messages } = reads;
  const openAsks = issue.open_asks;
  const agentsOnly = useMemo(
    () =>
      unheldId === undefined
        ? undefined
        : agentsOnlyItem({ id: unheldId, kind: "item" }, comments, messages, [
            ...(openAsks ?? []),
            ...(asks ?? []),
          ]),
    [asks, comments, messages, openAsks, unheldId]
  );
  const ancestor = useMemo(
    () =>
      agentsOnly === undefined
        ? undefined
        : agentsOnlyAncestor(root, agentsOnly, comments, messages),
    [agentsOnly, comments, messages, root]
  );
  // The issue's own route reads nothing: the landing reads an item only where it falls back.
  const read = unheld === undefined || agentsOnly !== undefined ? undefined : unheld;
  const landing = useItemLanding(read ?? { key: route.key, kind: "issue" }, issue);
  const client = useQueryClient();
  const readKey = read === undefined ? undefined : landingReadKey(read);
  const retrying =
    useIsFetching(readKey === undefined ? NO_READ : { exact: true, queryKey: readKey }) > 0;
  // How many times the read has failed, by the cache's own count: the read's observer
  // (`useItemLanding`) and `useIsFetching` draw the page again as the read's state changes.
  const failure =
    readKey === undefined ? 0 : (client.getQueryState(readKey)?.errorUpdateCount ?? 0);
  if (agentsOnly !== undefined && ancestor !== undefined) {
    return { ancestor, id: agentsOnly, kind: "agents-only" };
  }
  const target = address.target;
  if (
    readsHeld &&
    reveal === undefined &&
    address.document === undefined &&
    (target?.kind === "block" || target?.kind === "children")
  ) {
    return { kind: "absent", target };
  }
  if (unheld === undefined || landing === undefined || landing.kind === "turn") return undefined;
  if (landing.kind === "pending") {
    if (readKey === undefined || !retrying || failure === 0) return undefined;
    const again = { exact: true, queryKey: readKey };
    const failed = { kind: "unavailable", retry: () => void client.refetchQueries(again) } as const;
    return { failure, kind: "landing", landing: failed, retrying, route: unheld };
  }
  return { failure, kind: "landing", landing, retrying, route: unheld };
}
